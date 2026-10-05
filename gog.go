// gog.go — GOG-Anbindung über gogdl (aus dem Heroic-Projekt)
//
// Aufgabenteilung:
//   - gogdl:  Login-Token, Download/Installation, Start (launch)
//   - dieses Programm: Bibliothek (GOG-Web-API), Cover, Installationsverzeichnis,
//     Deinstallation (Ordner löschen), Fortschritt
//
// Dateien:
//
//	~/.config/snowfox/gog/auth.json       Tokens (nur für den Benutzer lesbar)
//	~/.config/snowfox/gog/library.json    Cache der Bibliothek (schneller Start, offline nutzbar)
//	~/.config/snowfox/gog/installed.json  Installierte Spiele
//	~/.config/snowfox/gog/prefixes/<id>   Wine-Prefix je Spiel (bleibt beim Deinstallieren erhalten: Spielstände)
//	~/.cache/snowfox/covers/gog/<id>.jpg  Cover
//	~/Games/GOG/<Titel>                   Installationsordner (Flag -gog-dir)
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const gogClientID = "46899977096215655"

// Endpunkte als Variablen, damit Tests sie umbiegen können
var (
	gogLoginURL   = "https://auth.gog.com/auth?client_id=" + gogClientID + "&redirect_uri=https%3A%2F%2Fembed.gog.com%2Fon_login_success%3Forigin%3Dclient&response_type=code&layout=galaxy"
	gogOwnedURL   = "https://embed.gog.com/user/data/games"
	gogProductURL = "https://api.gog.com/products/%d"
	gogGamesDBURL = "https://gamesdb.gog.com/platforms/gog/external_releases/%d"
)

var gogHTTP = &http.Client{Timeout: 20 * time.Second}

type gogConfig struct {
	bin       string // Pfad zu gogdl ("" = nicht gefunden)
	dir       string
	auth      string
	library   string
	installed string
	prefixes  string
	covers    string
	root      string // Installationsordner
	mu        sync.Mutex
}

func newGogConfig(root string) *gogConfig {
	home := os.Getenv("HOME")
	dir := filepath.Join(home, ".config", "snowfox", "gog")
	if root == "" {
		root = filepath.Join(home, "Games", "GOG")
	}
	c := &gogConfig{
		dir:       dir,
		auth:      filepath.Join(dir, "auth.json"),
		library:   filepath.Join(dir, "library.json"),
		installed: filepath.Join(dir, "installed.json"),
		prefixes:  filepath.Join(dir, "prefixes"),
		covers:    filepath.Join(home, ".cache", "snowfox", "covers", "gog"),
		root:      root,
	}
	c.bin = findGogdl(dir)
	return c
}

func findGogdl(cfgDir string) string {
	home := os.Getenv("HOME")
	cands := []string{
		filepath.Join(cfgDir, "bin", "gogdl"),
		filepath.Join(home, ".local", "bin", "gogdl"),
		"/opt/Heroic/resources/app.asar.unpacked/build/bin/x64/linux/gogdl",
	}
	if p, err := exec.LookPath("gogdl"); err == nil {
		return p
	}
	for _, p := range cands {
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// ─── Token ────────────────────────────────────────────────────────────────────

type gogTokenEntry struct {
	AccessToken  string  `json:"access_token"`
	RefreshToken string  `json:"refresh_token"`
	ExpiresIn    float64 `json:"expires_in"`
	LoginTime    float64 `json:"loginTime"`
}

func (e gogTokenEntry) expired() bool {
	return float64(time.Now().Unix()) > e.LoginTime+e.ExpiresIn-60
}

func (c *gogConfig) readToken() (gogTokenEntry, error) {
	b, err := os.ReadFile(c.auth)
	if err != nil {
		return gogTokenEntry{}, err
	}
	var m map[string]gogTokenEntry
	if err := json.Unmarshal(b, &m); err != nil {
		return gogTokenEntry{}, err
	}
	e, ok := m[gogClientID]
	if !ok {
		for _, v := range m {
			e, ok = v, true
			break
		}
	}
	if !ok || e.AccessToken == "" {
		return gogTokenEntry{}, errors.New("keine Zugangsdaten in auth.json")
	}
	return e, nil
}

func (c *gogConfig) loggedIn() bool {
	_, err := c.readToken()
	return err == nil
}

// token liefert einen gültigen Access-Token; abgelaufene werden über "gogdl auth" erneuert
func (c *gogConfig) token() (string, error) {
	e, err := c.readToken()
	if err != nil {
		return "", err
	}
	if !e.expired() {
		return e.AccessToken, nil
	}
	if c.bin == "" {
		return "", errors.New("gogdl nicht gefunden")
	}
	cmd := exec.Command(c.bin, "--auth-config-path", c.auth, "auth")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard // gogdl druckt die Tokens → nie anzeigen
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("Token-Erneuerung fehlgeschlagen: %w", err)
	}
	e, err = c.readToken()
	if err != nil {
		return "", err
	}
	if e.expired() {
		return "", errors.New("GOG-Login abgelaufen, bitte neu anmelden (-gog-login)")
	}
	return e.AccessToken, nil
}

// loginWithCode meldet mit dem Code aus der GOG-Weiterleitung an (CLI und Launcher nutzen dasselbe)
func (c *gogConfig) loginWithCode(code string) error {
	if c.bin == "" {
		return errors.New("gogdl nicht gefunden (Heroic installieren oder gogdl nach ~/.config/snowfox/gog/bin/ legen)")
	}
	code = extractGogCode(code)
	if code == "" {
		return errors.New("kein Code gefunden")
	}
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd := exec.Command(c.bin, "--auth-config-path", c.auth, "auth", "--code", code)
	cmd.Stdout = io.Discard // enthält die Tokens: nie anzeigen
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Login fehlgeschlagen (Code abgelaufen? Er gilt nur kurz): %v %s", err, lastLine(stderr.String()))
	}
	_ = os.Chmod(c.auth, 0o600)
	if !c.loggedIn() {
		return errors.New("Login scheinbar erfolgreich, aber auth.json ist leer")
	}
	return nil
}

// logout entfernt nur die Zugangsdaten; installierte Spiele und Bibliothek bleiben
func (c *gogConfig) logout() error {
	if err := os.Remove(c.auth); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// gogLoginCLI: Login im Terminal (./snowfox_launcher -gog-login), Alternative zum Login im Launcher
func gogLoginCLI(c *gogConfig, in io.Reader, out io.Writer) error {
	if c.bin == "" {
		return errors.New("gogdl nicht gefunden (Heroic installieren oder gogdl nach ~/.config/snowfox/gog/bin/ legen)")
	}
	fmt.Fprintln(out, "1. Diese Adresse im Browser öffnen und bei GOG anmelden:")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "   "+gogLoginURL)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "2. Danach erscheint eine fast leere Seite. In der Adresszeile steht ...?code=XXXX")
	fmt.Fprintln(out, "   Den ganzen Link oder nur den Teil nach code= hier einfügen:")
	fmt.Fprint(out, "> ")
	line, _ := bufio.NewReader(in).ReadString('\n')
	if err := c.loginWithCode(line); err != nil {
		return err
	}
	fmt.Fprintln(out, "Fertig. GOG ist verbunden.")
	return nil
}

func extractGogCode(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "code="); i >= 0 {
		s = s[i+len("code="):]
		if j := strings.IndexAny(s, "&# "); j >= 0 {
			s = s[:j]
		}
	}
	return s
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexAny(s, "\r\n"); i >= 0 {
		return strings.TrimSpace(s[i+1:])
	}
	return s
}

// ─── Bibliothek ───────────────────────────────────────────────────────────────

type gogProduct struct {
	ID      int    `json:"id"`
	Title   string `json:"title"`
	Linux   bool   `json:"linux"`
	Windows bool   `json:"windows"`
	Cover   string `json:"cover"` // lokaler Pfad
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (c *gogConfig) loadLibrary() []gogProduct {
	b, err := os.ReadFile(c.library)
	if err != nil {
		return nil
	}
	var out []gogProduct
	_ = json.Unmarshal(b, &out)
	return out
}

func httpGetJSON(url, bearer string, v any) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := gogHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func (c *gogConfig) ownedIDs(tok string) ([]int, error) {
	var d struct {
		Owned []int `json:"owned"`
	}
	if err := httpGetJSON(gogOwnedURL, tok, &d); err != nil {
		return nil, fmt.Errorf("Bibliothek abrufen: %w", err)
	}
	return d.Owned, nil
}

// fetchProduct holt Titel und Plattformen; ok=false für DLCs und nicht abrufbare IDs
func fetchProduct(id int) (gogProduct, bool) {
	var d struct {
		Title    string `json:"title"`
		GameType string `json:"game_type"`
		Compat   struct {
			Windows bool `json:"windows"`
			Linux   bool `json:"linux"`
		} `json:"content_system_compatibility"`
	}
	if err := httpGetJSON(fmt.Sprintf(gogProductURL, id), "", &d); err != nil {
		return gogProduct{}, false
	}
	if d.Title == "" || d.GameType == "dlc" {
		return gogProduct{}, false
	}
	return gogProduct{ID: id, Title: d.Title, Linux: d.Compat.Linux, Windows: d.Compat.Windows}, true
}

// fetchCover lädt das Poster-Cover (GOG GamesDB) in den Cache; "" wenn nichts gefunden
func (c *gogConfig) fetchCover(id int) string {
	path := filepath.Join(c.covers, strconv.Itoa(id)+".jpg")
	if st, err := os.Stat(path); err == nil && st.Size() > 0 {
		return path
	}
	var d struct {
		Game struct {
			Vertical struct {
				URLFormat string `json:"url_format"`
			} `json:"vertical_cover"`
		} `json:"game"`
	}
	if err := httpGetJSON(fmt.Sprintf(gogGamesDBURL, id), "", &d); err != nil {
		log.Printf("[SnowFox] GOG-Cover %d: %v", id, err)
		return ""
	}
	u := strings.NewReplacer("{formatter}", "", "{ext}", "jpg").Replace(d.Game.Vertical.URLFormat)
	if u == "" {
		log.Printf("[SnowFox] GOG-Cover %d: keine vertical_cover.url_format in der Antwort", id)
		return ""
	}
	resp, err := gogHTTP.Get(u)
	if err != nil || resp.StatusCode != 200 {
		if resp != nil {
			resp.Body.Close()
		}
		log.Printf("[SnowFox] GOG-Cover %d: Download fehlgeschlagen (%v)", id, err)
		return ""
	}
	defer resp.Body.Close()
	if err := os.MkdirAll(c.covers, 0o755); err != nil {
		return ""
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return ""
	}
	_, err = io.Copy(f, resp.Body)
	f.Close()
	if err != nil {
		os.Remove(tmp)
		return ""
	}
	if os.Rename(tmp, path) != nil {
		return ""
	}
	return path
}

// syncLibrary gleicht mit GOG ab (nur neue Spiele werden einzeln abgefragt) und speichert den Cache
func (c *gogConfig) syncLibrary() ([]gogProduct, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	tok, err := c.token()
	if err != nil {
		return nil, err
	}
	ids, err := c.ownedIDs(tok)
	if err != nil {
		return nil, err
	}

	cache := map[int]gogProduct{}
	for _, p := range c.loadLibrary() {
		cache[p.ID] = p
	}

	// Fehlende Produkte parallel abfragen (max. 6 gleichzeitig)
	var wg sync.WaitGroup
	var rmu sync.Mutex
	sem := make(chan struct{}, 6)
	result := map[int]gogProduct{}
	for _, id := range ids {
		if p, ok := cache[id]; ok && p.Title != "" {
			rmu.Lock()
			result[id] = p
			rmu.Unlock()
			continue
		}
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if p, ok := fetchProduct(id); ok {
				rmu.Lock()
				result[id] = p
				rmu.Unlock()
			}
		}(id)
	}
	wg.Wait()

	// Cover (nur wenn noch nicht vorhanden); über eine Kopie iterieren, nicht über die Map
	snapshot := make([]gogProduct, 0, len(result))
	for _, p := range result {
		snapshot = append(snapshot, p)
	}
	for _, p := range snapshot {
		wg.Add(1)
		go func(p gogProduct) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			p.Cover = c.fetchCover(p.ID)
			rmu.Lock()
			result[p.ID] = p
			rmu.Unlock()
		}(p)
	}
	wg.Wait()

	out := make([]gogProduct, 0, len(result))
	for _, p := range result {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title) })
	if err := writeJSON(c.library, out); err != nil {
		return out, err
	}
	return out, nil
}

// ─── Installierte Spiele ──────────────────────────────────────────────────────

type gogInstalled struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	Path  string `json:"path"` // Spielordner (dort liegt goggame-<id>.info)
	OS    string `json:"os"`   // linux | windows
}

func (c *gogConfig) loadInstalled() map[int]gogInstalled {
	out := map[int]gogInstalled{}
	b, err := os.ReadFile(c.installed)
	if err != nil {
		return out
	}
	var list []gogInstalled
	if json.Unmarshal(b, &list) == nil {
		for _, in := range list {
			out[in.ID] = in
		}
	}
	return out
}

func (c *gogConfig) saveInstalled(m map[int]gogInstalled) error {
	list := make([]gogInstalled, 0, len(m))
	for _, in := range m {
		list = append(list, in)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return writeJSON(c.installed, list)
}

// ─── Installieren ─────────────────────────────────────────────────────────────

// Ausgabe von gogdl (Heroic), z.B.:
//
//	[PROGRESS] INFO: = Progress: 0.87 9132549/1050761285, Running for: 00:00:01, ETA: 00:01:56
//	[PROGRESS] INFO:  + Download	- 3.52 MiB/s (raw) / 3.52 MiB/s (decompressed)
//
// "Progress: 0.87" ist bereits ein Prozentwert (ohne %-Zeichen).
var (
	reProgress = regexp.MustCompile(`Progress:\s*(\d+(?:\.\d+)?)\s+\d+/\d+.*?ETA:\s*([\d:]+)`)
	reSpeed    = regexp.MustCompile(`Download\s*-\s*(\d+(?:\.\d+)?\s*\w+/s)`)
	rePercent  = regexp.MustCompile(`(\d{1,3}(?:\.\d+)?)\s*%`) // Fallback für andere Ausgabeformate
)

// joinInfo: kurzer Text für die Oberfläche, z.B. "3.5 MiB/s · noch 00:01:56"
func joinInfo(speed, eta string) string {
	var parts []string
	if speed != "" {
		parts = append(parts, speed)
	}
	if eta != "" && eta != "00:00:00" {
		parts = append(parts, "noch "+eta)
	}
	return strings.Join(parts, " · ")
}

// scanLines trennt bei \n UND \r (Fortschrittsanzeigen überschreiben sich mit \r)
func scanLines(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func sanitizeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || r < 32 {
			return '_'
		}
		return r
	}, s)
	s = strings.Trim(strings.TrimSpace(s), ".")
	if s == "" {
		s = "Spiel"
	}
	return s
}

// pickOS wählt Linux (nativ) vor Windows (Wine)
func pickOS(p gogProduct) (string, error) {
	switch {
	case p.Linux:
		return "linux", nil
	case p.Windows:
		return "windows", nil
	}
	return "", errors.New("keine installierbare Plattform")
}

func findGameRoot(dir string, id int) string {
	name := fmt.Sprintf("goggame-%d.info", id)
	found := ""
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			if rel != "." && strings.Count(rel, string(os.PathSeparator)) >= 2 {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == name {
			found = filepath.Dir(p)
			return filepath.SkipAll
		}
		return nil
	})
	if found == "" {
		return dir
	}
	return found
}

// install lädt ein Spiel mit gogdl. progress(pct, info): pct < 0 = unverändert, info = Kurztext (Tempo/Restzeit).
func (c *gogConfig) install(ctx context.Context, p gogProduct, osName string, progress func(pct float64, info string)) (gogInstalled, error) {
	if c.bin == "" {
		return gogInstalled{}, errors.New("gogdl nicht gefunden")
	}
	dir := filepath.Join(c.root, sanitizeName(p.Title))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return gogInstalled{}, err
	}

	cmd := exec.CommandContext(ctx, c.bin, "--auth-config-path", c.auth,
		"download", strconv.Itoa(p.ID), "--path", dir, "--platform", osName, "--skip-dlcs")
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	// Abbrechen beendet die ganze Prozessgruppe (gogdl startet ggf. Kindprozesse);
	// WaitDelay verhindert, dass Wait() an offenen Pipes hängen bleibt.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 3 * time.Second
	if err := cmd.Start(); err != nil {
		return gogInstalled{}, err
	}

	var last, speed, eta string
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		sc.Split(scanLines)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			last = line
			log.Printf("[gogdl] %s", line)
			switch {
			case reProgress.MatchString(line):
				m := reProgress.FindStringSubmatch(line)
				eta = m[2]
				if v, err := strconv.ParseFloat(m[1], 64); err == nil && v >= 0 && v <= 100 {
					progress(v, joinInfo(speed, eta))
				}
			case reSpeed.MatchString(line):
				speed = reSpeed.FindStringSubmatch(line)[1]
				progress(-1, joinInfo(speed, eta))
			default:
				if m := rePercent.FindStringSubmatch(line); m != nil {
					if v, err := strconv.ParseFloat(m[1], 64); err == nil && v >= 0 && v <= 100 {
						progress(v, "")
					}
				}
			}
		}
	}()

	err := cmd.Wait()
	pw.Close()
	<-done
	if ctx.Err() != nil {
		return gogInstalled{}, ctx.Err()
	}
	if err != nil {
		return gogInstalled{}, fmt.Errorf("gogdl: %v %s", err, last)
	}
	return gogInstalled{ID: p.ID, Title: p.Title, Path: findGameRoot(dir, p.ID), OS: osName}, nil
}

// ─── Deinstallieren ───────────────────────────────────────────────────────────

// uninstall löscht den Spielordner, aber NUR innerhalb des Installationsordners.
// Der Wine-Prefix (Spielstände!) bleibt bewusst erhalten.
func (c *gogConfig) uninstall(in gogInstalled) error {
	root, err := filepath.Abs(c.root)
	if err != nil {
		return err
	}
	p, err := filepath.Abs(in.Path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("%s liegt außerhalb von %s, wird nicht gelöscht", p, root)
	}
	top := filepath.Join(root, strings.Split(rel, string(os.PathSeparator))[0])
	return os.RemoveAll(top)
}

// ─── Starten ──────────────────────────────────────────────────────────────────

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func shellJoin(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = shellQuote(a)
	}
	return strings.Join(q, " ")
}

// launchCmd baut den Startbefehl (gogdl launch …); Windows-Spiele laufen über Wine
func (c *gogConfig) launchCmd(in gogInstalled) (string, error) {
	if c.bin == "" {
		return "", errors.New("gogdl nicht gefunden")
	}
	args := []string{c.bin, "--auth-config-path", c.auth, "launch", "--platform", in.OS}
	if in.OS == "windows" {
		wine, err := exec.LookPath("wine")
		if err != nil {
			return "", errors.New("Wine fehlt (sudo apt install wine)")
		}
		prefix := filepath.Join(c.prefixes, strconv.Itoa(in.ID))
		if err := os.MkdirAll(prefix, 0o755); err != nil {
			return "", err
		}
		args = append(args, "--wine", wine, "--wine-prefix", prefix)
	}
	args = append(args, in.Path, strconv.Itoa(in.ID))
	return shellJoin(args), nil
}

// games baut die Spieleliste für den Launcher aus Cache + installierten Spielen
func (c *gogConfig) games() []Game {
	inst := c.loadInstalled()
	var out []Game
	for _, p := range c.loadLibrary() {
		g := Game{
			ID: p.ID, Title: p.Title, Platform: "gog",
			Genre: osLabel(p), Cover: p.Cover,
			HasLinux: p.Linux, HasWindows: p.Windows, Managed: true,
		}
		if in, ok := inst[p.ID]; ok {
			g.Status = "bereit"
			cmd, err := c.launchCmd(in)
			if err != nil {
				g.Note = err.Error()
			}
			g.LaunchCmd = cmd
		} else {
			g.Status = "nicht-installiert"
		}
		out = append(out, g)
	}
	if !c.loggedIn() {
		// Platzhalter-Stern: Enter darauf startet die Anmeldung im Launcher
		note := ""
		if c.bin == "" {
			note = "gogdl nicht gefunden (Heroic installieren)"
		}
		out = append(out, Game{
			ID: -1, Title: "GOG verbinden", Platform: "gog", Genre: "Konto verbinden",
			Status: "ghost", Managed: true, Ghost: true, Note: note,
		})
	}
	return out
}

func osLabel(p gogProduct) string {
	switch {
	case p.Linux && p.Windows:
		return "Linux · Windows"
	case p.Linux:
		return "Linux"
	case p.Windows:
		return "Windows (Wine)"
	}
	return ""
}
