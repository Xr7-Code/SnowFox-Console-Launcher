// SnowFoxOS Console Launcher — main.go
// Copyright (c) 2026 Alexander Valentin Ludwig (Xr7-Code)
//
// Erwartet: controller.go (cgo) und pad.go (Eingabelogik), siehe startController(ch chan string)
// Zusätzliche Abhängigkeit: go get golang.org/x/image
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	_ "image/png"
	"log"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"gioui.org/app"
	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/font/opentype"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget/material"
	xdraw "golang.org/x/image/draw"
)

// ─── Workspaces ───────────────────────────────────────────────────────────────

const (
	wsLauncher = "8"
	wsGame     = "9"
)

// ─── Farben (SnowFox GTK-Theme) ───────────────────────────────────────────────

var (
	colBg        = hexColor(0x1a1825)
	colSurface   = hexColor(0x201e2e)
	colSurfaceHi = hexColor(0x2a2840)
	colText      = hexColor(0xe0d9f5)
	colDim       = hexColor(0x6e6a8a)
	colAccent    = hexColor(0x9d6fe8)
	colAccentHi  = hexColor(0xb899ff)
	colAccentLo  = hexColor(0x5c3d99)
	colError     = hexColor(0xe87a7a)
	colSuccess   = hexColor(0x89c98a)
	colWarning   = hexColor(0xe8c97a)
	colBorder    = hexColor(0x2a2840)
	colOverlay   = color.NRGBA{R: 12, G: 10, B: 20, A: 215}
)

func hexColor(hex uint32) color.NRGBA {
	return color.NRGBA{R: uint8(hex >> 16), G: uint8((hex >> 8) & 0xff), B: uint8(hex & 0xff), A: 255}
}

func withAlpha(c color.NRGBA, a uint8) color.NRGBA {
	c.A = a
	return c
}

func imax(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func imin(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ─── Datenmodell ──────────────────────────────────────────────────────────────

type Game struct {
	ID        int    `json:"id"`
	Title     string `json:"title"`
	Platform  string `json:"platform"`
	Genre     string `json:"genre"`
	Status    string `json:"status"`
	LaunchCmd string `json:"launchCmd"`
	Cover     string `json:"cover"` // optional: lokaler Pfad zu einem Cover-Bild

	// Von der GOG-Anbindung gesetzt (nicht in games.json)
	Managed    bool   `json:"-"` // vom Store-Backend verwaltet
	HasLinux   bool   `json:"-"`
	HasWindows bool   `json:"-"`
	Note       string `json:"-"` // Hinweis, warum ein Start nicht möglich ist
	Ghost      bool   `json:"-"` // Platzhalter-Stern (z.B. "GOG verbinden")
}

func (g *Game) coverKey() string {
	return g.Platform + "|" + strconv.Itoa(g.ID) + "|" + g.Title
}

func (g *Game) statusColor() color.NRGBA {
	switch g.Status {
	case "bereit":
		return colSuccess
	case "update-verfugbar":
		return colWarning
	case "wird-heruntergeladen":
		return colError
	default:
		return colDim
	}
}

func (g *Game) statusLabel() string {
	switch g.Status {
	case "bereit":
		return "Bereit"
	case "update-verfugbar":
		return "Update verfügbar"
	case "wird-heruntergeladen":
		return "Wird geladen…"
	case "nicht-installiert":
		return "Nicht installiert"
	case "ghost":
		return "Nicht verbunden"
	default:
		return g.Status
	}
}

var platforms = []string{"steam", "gog", "epic", "retro", "godot"}
var platformLabels = map[string]string{
	"all": "Alle", "steam": "Steam", "epic": "Epic", "gog": "GOG", "retro": "Retro", "godot": "Godot",
}

func platformSymbol(p string) string {
	switch p {
	case "steam":
		return "S"
	case "epic":
		return "E"
	case "gog", "godot":
		return "G"
	case "retro":
		return "R"
	default:
		return "?"
	}
}

func platformAccent(p string) color.NRGBA {
	switch p {
	case "steam":
		return hexColor(0x7aa7e8)
	case "epic":
		return hexColor(0xd8dae8)
	case "gog":
		return hexColor(0xc58be8)
	case "godot":
		return hexColor(0x89c98a)
	case "retro":
		return hexColor(0xe87a7a)
	default:
		return colAccentHi
	}
}

// ─── Cover-Cache (thread-sicher, lazy, verkleinert) ───────────────────────────

type coverCache struct {
	mu     sync.RWMutex
	images map[string]paint.ImageOp
}

var covers = &coverCache{images: make(map[string]paint.ImageOp)}

func (c *coverCache) get(key string) (paint.ImageOp, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	op, ok := c.images[key]
	return op, ok
}

func (c *coverCache) set(key string, op paint.ImageOp) {
	c.mu.Lock()
	c.images[key] = op
	c.mu.Unlock()
}

var steamRoots []string

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func steamCoverPath(id int) string {
	for _, r := range steamRoots {
		dir := filepath.Join(r, "appcache", "librarycache", strconv.Itoa(id))
		for _, name := range []string{"library_600x900.jpg", "library_capsule.jpg"} {
			if p := filepath.Join(dir, name); fileExists(p) {
				return p
			}
			// Neuere Steam-Clients legen die Bilder in Hash-Unterordner
			if m, _ := filepath.Glob(filepath.Join(dir, "*", name)); len(m) > 0 {
				return m[0]
			}
		}
	}
	return ""
}

func loadCoverImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	src, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	// Auf max. 400px Breite verkleinern, spart RAM (600x900 RGBA = 2 MB)
	b := src.Bounds()
	const maxW = 400
	if b.Dx() <= maxW {
		return src, nil
	}
	h := b.Dy() * maxW / b.Dx()
	dst := image.NewRGBA(image.Rect(0, 0, maxW, h))
	xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, b, xdraw.Over, nil)
	return dst, nil
}

// coverLoop lädt fehlende Cover im Hintergrund (auch für später hinzugekommene Spiele)
func coverLoop(s *LauncherState) {
	for {
		games := s.snapshotGames()
		for i := range games {
			g := &games[i]
			key := g.coverKey()
			if _, ok := covers.get(key); ok {
				continue
			}
			path := g.Cover
			if path == "" && g.Platform == "steam" {
				path = steamCoverPath(g.ID)
			}
			if path == "" {
				continue
			}
			img, err := loadCoverImage(path)
			if err != nil {
				continue
			}
			covers.set(key, paint.NewImageOp(img))
			s.window.Invalidate()
		}
		time.Sleep(5 * time.Second)
	}
}

// ─── Schriften ────────────────────────────────────────────────────────────────

func buildFontCollection() ([]text.FontFace, bool) {
	var collection []text.FontFace
	hasInter := false
	dirs := []string{"/usr/share/fonts/opentype/inter/", "/usr/share/fonts/truetype/inter/"}
	files := map[string]font.Weight{
		"Inter-Regular.otf": font.Normal,
		"Inter-Medium.otf":  font.Medium,
		"Inter-Bold.otf":    font.Bold,
	}
	for _, dir := range dirs {
		for file, weight := range files {
			b, err := os.ReadFile(dir + file)
			if err != nil {
				continue
			}
			face, err := opentype.Parse(b)
			if err != nil {
				continue
			}
			collection = append(collection, text.FontFace{
				Font: font.Font{Typeface: "Inter", Weight: weight},
				Face: face,
			})
			hasInter = true
		}
	}
	collection = append(collection, gofont.Collection()...)
	return collection, hasInter
}

// ─── Musik (mpv mit sanftem Fade, sonst ffplay ohne Fade) ─────────────────────

type MusicPlayer struct {
	mu     sync.Mutex
	file   string
	cmd    *exec.Cmd
	volume int // 0–100
	sock   string
	useMpv bool
}

func newMusicPlayer(file string) *MusicPlayer {
	_, err := exec.LookPath("mpv")
	return &MusicPlayer{
		file:   file,
		volume: 30,
		useMpv: err == nil,
		sock:   filepath.Join(os.TempDir(), fmt.Sprintf("snowfox-mpv-%d.sock", os.Getpid())),
	}
}

func (m *MusicPlayer) play() {
	if m.file == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()

	var cmd *exec.Cmd
	if m.useMpv {
		os.Remove(m.sock)
		cmd = exec.Command("mpv", "--no-video", "--loop=inf", "--really-quiet",
			fmt.Sprintf("--volume=%d", m.volume), "--input-ipc-server="+m.sock, m.file)
	} else {
		cmd = exec.Command("ffplay", "-nodisp", "-loop", "0",
			"-af", fmt.Sprintf("volume=%.2f", float64(m.volume)/100),
			"-loglevel", "quiet", m.file)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("[SnowFox] Musik-Fehler: %v", err)
		return
	}
	m.cmd = cmd
	go cmd.Wait() // Zombie-Prozess vermeiden
	log.Printf("[SnowFox] Musik gestartet: %s", m.file)
}

func (m *MusicPlayer) stopLocked() {
	if m.cmd != nil && m.cmd.Process != nil {
		m.cmd.Process.Kill()
	}
	m.cmd = nil
}

func (m *MusicPlayer) stop() {
	m.mu.Lock()
	m.stopLocked()
	m.mu.Unlock()
}

func (m *MusicPlayer) setVolume(v int) {
	c, err := net.DialTimeout("unix", m.sock, 200*time.Millisecond)
	if err != nil {
		return
	}
	defer c.Close()
	fmt.Fprintf(c, `{"command":["set_property","volume",%d]}`+"\n", v)
}

// fadeOut ruft done IMMER asynchron auf (wichtig: Aufrufer hält evtl. den State-Lock)
func (m *MusicPlayer) fadeOut(done func()) {
	go func() {
		m.mu.Lock()
		playing, mpv, start := m.cmd != nil, m.useMpv, m.volume
		m.mu.Unlock()
		if playing && mpv {
			for v := start; v > 0; v-- {
				m.setVolume(v)
				time.Sleep(30 * time.Millisecond)
			}
		}
		m.stop()
		done()
	}()
}

// ─── i3-Helfer ────────────────────────────────────────────────────────────────

type i3Node struct {
	Type             string   `json:"type"`
	Name             string   `json:"name"`
	Nodes            []i3Node `json:"nodes"`
	FloatingNodes    []i3Node `json:"floating_nodes"`
	Window           int      `json:"window"`
	ID               int64    `json:"id"`
	WindowProperties struct {
		Class string `json:"class"`
		Title string `json:"title"`
	} `json:"window_properties"`
}

func i3(args ...string) {
	if err := exec.Command("i3-msg", args...).Run(); err != nil {
		log.Printf("[SnowFox] i3-msg %v: %v", args, err)
	}
}

func workspaceHasWindows(wsName string) bool {
	out, err := exec.Command("i3-msg", "-t", "get_tree").Output()
	if err != nil {
		return false
	}
	var root i3Node
	if err := json.Unmarshal(out, &root); err != nil {
		return false
	}
	return nodeHasWindows(root, wsName, false)
}

func nodeHasWindows(n i3Node, ws string, in bool) bool {
	if n.Type == "workspace" && n.Name == ws {
		in = true
	}
	if in && n.Window > 0 {
		return true
	}
	for _, c := range n.Nodes {
		if nodeHasWindows(c, ws, in) {
			return true
		}
	}
	for _, c := range n.FloatingNodes {
		if nodeHasWindows(c, ws, in) {
			return true
		}
	}
	return false
}

func runVisible(name string, args ...string) {
	full := name + " " + strings.Join(args, " ")
	log.Printf("[SnowFox] $ %s", full)
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Printf("[SnowFox] FEHLER bei '%s': %v", full, err)
	}
}

// ─── Launcher-Zustand ─────────────────────────────────────────────────────────

type OverlayMode int

const (
	OverlayNone      OverlayMode = iota
	OverlayIngame                // Guide während ein Spiel läuft
	OverlayLaunch                // Bestätigung vor dem Start
	OverlaySystem                // Guide ohne Spiel → Systemmenü
	OverlayUninstall             // Bestätigung vor dem Deinstallieren
	OverlayOptions               // Optionen-Menü (X): alle Aktionen des fokussierten Spiels
	OverlayGogLogin              // GOG-Anmeldung (QR-Code / Zwischenablage)
)

type LauncherState struct {
	mu sync.Mutex // schützt ALLES unten (außer terminateRequested)

	window         *app.Window
	allGames       []Game
	filtered       []Game
	activePlatform string
	focusIndex     int
	focusChangedAt time.Time

	overlay          OverlayMode
	overlaySelection int

	uiPaused   bool
	launching  bool
	busyMsg    string // Übergangs-Overlay (Wechsel zu Desktop/Server, Neustart …)
	activeGame *Game
	activeCmd  *exec.Cmd

	terminateRequested atomic.Bool

	toast      string
	toastUntil time.Time
	toastOK    bool

	gog         *gogConfig
	gogLoggedIn bool
	installs    map[string]*installJob

	// Sternenkarte
	sky      *skyMap
	cam      skyCam
	overview bool             // LT/RT gehalten: auf die ganze Karte herauszoomen
	recent   map[string]int64 // zuletzt gespielt (Spiel-Schlüssel → Unix-Zeit)

	gogLogin       *gogLogin
	gogLoginStatus string

	music   *MusicPlayer
	inputCh chan string
}

func newState(games []Game, musicFile string) *LauncherState {
	s := &LauncherState{
		allGames:       games,
		activePlatform: "all",
		installs:       make(map[string]*installJob),
		recent:         make(map[string]int64),
		inputCh:        make(chan string, 32),
		music:          newMusicPlayer(musicFile),
	}
	s.applyFilter()
	return s
}

// update führt f unter Lock aus und zeichnet neu (für Hintergrund-Goroutinen)
func (s *LauncherState) update(f func()) {
	s.mu.Lock()
	f()
	s.mu.Unlock()
	s.window.Invalidate()
}

func (s *LauncherState) toastLocked(msg string) {
	s.toast = msg
	s.toastOK = false
	s.toastUntil = time.Now().Add(4 * time.Second)
}

func (s *LauncherState) applyFilter() {
	// Auf der Sternenkarte sind immer ALLE Spiele zu sehen; Plattformen sind Sternbilder, keine Filter
	s.filtered = append(s.filtered[:0], s.allGames...)
	s.focusIndex = 0
	s.focusChangedAt = time.Now()
	s.syncActivePlatform()
}

// syncActivePlatform: der markierte Reiter folgt dem Fokus
func (s *LauncherState) syncActivePlatform() {
	if g := s.currentGame(); g != nil {
		s.activePlatform = g.Platform
	}
}

func platformLabel(p string) string {
	if l, ok := platformLabels[p]; ok {
		return l
	}
	return strings.ToUpper(p)
}

// visiblePlatforms: Plattformen mit mindestens einem Stern (feste Reihenfolge, Unbekannte hinten)
func (s *LauncherState) visiblePlatforms() []string {
	seen := map[string]bool{}
	for i := range s.filtered {
		seen[s.filtered[i].Platform] = true
	}
	var out []string
	for _, p := range platforms {
		if seen[p] {
			out = append(out, p)
			delete(seen, p)
		}
	}
	var extra []string
	for p := range seen {
		extra = append(extra, p)
	}
	sort.Strings(extra)
	return append(out, extra...)
}

func (s *LauncherState) setFocus(i int) {
	if i < 0 || i >= len(s.filtered) || i == s.focusIndex {
		return
	}
	s.focusIndex = i
	s.focusChangedAt = time.Now()
	s.syncActivePlatform()
}

func (s *LauncherState) skyItems() []skyItem {
	items := make([]skyItem, len(s.filtered))
	for i := range s.filtered {
		g := &s.filtered[i]
		k := gameKey(g)
		items[i] = skyItem{Key: k, Platform: g.Platform, Title: g.Title, Recent: s.recent[k]}
	}
	return items
}

// ensureSky baut die Karte neu, wenn sich Spiele oder "zuletzt gespielt" geändert haben
func (s *LauncherState) ensureSky() *skyMap {
	items := s.skyItems()
	if sig := skySignature(items); s.sky == nil || s.sky.Sig != sig {
		s.sky = buildSky(items)
	}
	return s.sky
}

// moveFocus springt räumlich zum nächsten Stern in Richtung (dx, dy)
func (s *LauncherState) moveFocus(dx, dy float64) {
	if len(s.filtered) == 0 {
		return
	}
	if j := s.ensureSky().neighbor(s.focusIndex, dx, dy); j >= 0 {
		s.setFocus(j)
	}
}

func (s *LauncherState) jumpToPlatform(p string) {
	m := s.ensureSky()
	ci := m.clusterOf(p)
	if ci < 0 || len(m.Clusters[ci].Nodes) == 0 {
		s.toastLocked("Keine Spiele für " + platformLabel(p))
		return
	}
	s.setFocus(m.Clusters[ci].Nodes[0])
}

func (s *LauncherState) selectPlatform(i int) {
	s.mu.Lock()
	if s.overlay == OverlayNone && !s.uiPaused {
		if vis := s.visiblePlatforms(); i >= 0 && i < len(vis) {
			s.jumpToPlatform(vis[i])
		}
	}
	s.mu.Unlock()
	s.window.Invalidate()
}

func (s *LauncherState) cyclePlatform(dir int) {
	vis := s.visiblePlatforms()
	if len(vis) == 0 {
		return
	}
	idx := 0
	for i, p := range vis {
		if p == s.activePlatform {
			idx = i
			break
		}
	}
	s.jumpToPlatform(vis[(idx+dir+len(vis))%len(vis)])
}

func (s *LauncherState) confirmLaunch() {
	if len(s.filtered) == 0 || s.launching {
		return
	}
	g := s.filtered[s.focusIndex]
	if g.Status == "wird-heruntergeladen" {
		s.toastLocked("„" + g.Title + "“ wird noch heruntergeladen")
		return
	}
	if g.LaunchCmd == "" {
		msg := "Kein Startbefehl für „" + g.Title + "“"
		if g.Note != "" {
			msg = g.Note
		}
		s.toastLocked(msg)
		return
	}
	s.overlay = OverlayLaunch
	s.overlaySelection = 0
}

// launch: Lock wird vom Aufrufer gehalten
func (s *LauncherState) launch() {
	if len(s.filtered) == 0 || s.launching {
		return
	}
	g := s.filtered[s.focusIndex]
	s.activeGame = &g
	s.overlay = OverlayNone
	s.launching = true
	s.terminateRequested.Store(false)
	s.music.fadeOut(func() { s.runGame(g) })
}

// runGame läuft in einer Goroutine, OHNE Lock
func (s *LauncherState) runGame(g Game) {
	k := gameKey(&g)
	s.update(func() {
		s.launching = false
		s.uiPaused = true
		s.recent[k] = time.Now().Unix() // rückt den Stern ins Zentrum seines Sternbilds
	})
	go s.saveRecent()

	i3("workspace", wsGame)
	time.Sleep(200 * time.Millisecond)

	cmd := exec.Command("sh", "-c", g.LaunchCmd)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // eigene Prozessgruppe
	if err := cmd.Start(); err != nil {
		log.Printf("[SnowFox] Startfehler: %v", err)
		s.update(func() {
			s.activeGame, s.activeCmd, s.uiPaused = nil, nil, false
			s.toastLocked("Start fehlgeschlagen: " + err.Error())
		})
		i3("workspace", wsLauncher)
		s.music.play()
		return
	}
	s.update(func() { s.activeCmd = cmd })

	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	s.watchGame(done)

	s.update(func() {
		s.activeCmd, s.activeGame = nil, nil
		s.uiPaused = false
		s.overlay = OverlayNone
	})
	s.terminateRequested.Store(false)
	i3("workspace", wsLauncher)
	s.music.play()
}

// watchGame wartet, bis das Spiel wirklich beendet ist (Befehl tot + Workspace 5s leer)
func (s *LauncherState) watchGame(done <-chan struct{}) {
	appeared := false
	for i := 0; i < 30 && !s.terminateRequested.Load(); i++ {
		if workspaceHasWindows(wsGame) {
			appeared = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	empty := 0
	for !s.terminateRequested.Load() {
		running := true
		select {
		case <-done:
			running = false
		default:
		}
		if workspaceHasWindows(wsGame) {
			empty = 0
			appeared = true
		} else {
			empty++
		}
		if !running && empty >= 10 {
			return
		}
		if !appeared && !running {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// terminateGame: Lock wird vom Aufrufer gehalten
func (s *LauncherState) terminateGame() {
	log.Println("[SnowFox] Beende Spiel…")
	s.terminateRequested.Store(true)
	cmd := s.activeCmd
	go func() {
		if cmd != nil && cmd.Process != nil {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		}
		i3("[workspace="+wsGame+"]", "kill")
	}()
}

// systemAction: Lock wird vom Aufrufer gehalten.
// Wichtig: "snowfox node …" wird ABGEKOPPELT gestartet (eigene Session) und wir
// warten nicht darauf. Vorher blockierte runVisible() so lange, wie das Skript
// lief, und der Launcher blieb mit seinem Fenster auf Workspace 8 stehen.
func (s *LauncherState) systemAction(sel int) {
	s.music.stop()
	msgs := []string{"Wechsle zum Desktop…", "Wechsle zum Server…", "Starte neu…", "Fahre herunter…"}
	s.busyMsg = msgs[sel]

	go func() {
		// Notbremse: spätestens nach 8 s beendet sich der Launcher selbst
		var watchdog *time.Timer
		if sel < 2 {
			watchdog = time.AfterFunc(8*time.Second, func() {
				log.Println("[SnowFox] Notbremse: Launcher beendet sich selbst")
				startJanitor()
				os.Exit(0)
			})
		}
		fail := func(err error) {
			if watchdog != nil {
				watchdog.Stop()
			}
			log.Printf("[SnowFox] Systemaktion fehlgeschlagen: %v", err)
			s.update(func() {
				s.busyMsg = ""
				s.toastLocked("Fehlgeschlagen: " + err.Error())
			})
		}

		runVisible("i3-msg", "[workspace="+wsGame+"]", "kill")
		runVisible("i3-msg", "workspace", "1")
		time.Sleep(300 * time.Millisecond)

		switch sel {
		case 0, 1:
			target := "desktop"
			if sel == 1 {
				target = "server"
			}
			log.Printf("[SnowFox] $ snowfox node %s (abgekoppelt)", target)
			cmd := exec.Command("snowfox", "node", target)
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			if err := cmd.Start(); err != nil {
				fail(err)
				return
			}
			go cmd.Wait()
			startJanitor() // räumt danach Fenster auf Workspace 8 auf (siehe janitor.go)
			time.Sleep(500 * time.Millisecond)
			os.Exit(0)
		case 2:
			if err := exec.Command("systemctl", "reboot").Run(); err != nil {
				fail(err)
			}
		case 3:
			if err := exec.Command("systemctl", "poweroff").Run(); err != nil {
				fail(err)
			}
		}
	}()
}

func (s *LauncherState) handleInput(cmd string) {
	s.mu.Lock()
	defer s.window.Invalidate()
	defer s.mu.Unlock()

	// Übersicht (LT/RT bzw. Taste Z gehalten): wirkt nur auf der Karte ohne Dialog
	switch cmd {
	case "ZOOM_OFF":
		s.overview = false
		return
	case "ZOOM_ON":
		if s.overlay == OverlayNone && !s.uiPaused && !s.launching && s.busyMsg == "" {
			s.overview = true
		}
		return
	}

	switch s.overlay {

	case OverlayIngame:
		resume := func() {
			s.overlay = OverlayNone
			go func() { time.Sleep(100 * time.Millisecond); i3("workspace", wsGame) }()
		}
		switch cmd {
		case "UP":
			s.overlaySelection = 0
		case "DOWN":
			s.overlaySelection = 1
		case "ENTER":
			if s.overlaySelection == 0 {
				resume()
			} else {
				s.overlay = OverlayNone
				s.terminateGame()
			}
		case "GUIDE", "BACK":
			resume()
		}

	case OverlayLaunch:
		switch cmd {
		case "UP", "DOWN":
			s.overlaySelection = 1 - s.overlaySelection
		case "ENTER":
			if s.overlaySelection == 0 {
				s.launch()
			} else {
				s.overlay = OverlayNone
			}
		case "BACK":
			s.overlay = OverlayNone
		}

	case OverlaySystem:
		switch cmd {
		case "UP":
			s.overlaySelection = imax(0, s.overlaySelection-1)
		case "DOWN":
			s.overlaySelection = imin(3, s.overlaySelection+1)
		case "ENTER":
			s.overlay = OverlayNone
			s.systemAction(s.overlaySelection)
		case "BACK", "GUIDE":
			s.overlay = OverlayNone
		}

	case OverlayUninstall:
		switch cmd {
		case "UP", "DOWN":
			s.overlaySelection = 1 - s.overlaySelection
		case "ENTER":
			s.overlay = OverlayNone
			if s.overlaySelection == 0 {
				if g := s.currentGame(); g != nil {
					s.uninstallGog(*g)
				}
			}
		case "BACK", "GUIDE":
			s.overlay = OverlayNone
		}

	case OverlayOptions:
		g := s.currentGame()
		if g == nil {
			s.overlay = OverlayNone
			break
		}
		acts := s.actionsFor(g)
		switch cmd {
		case "UP":
			s.overlaySelection = imax(0, s.overlaySelection-1)
		case "DOWN":
			s.overlaySelection = imin(len(acts)-1, s.overlaySelection+1)
		case "ENTER", "START":
			kind := acts[imin(s.overlaySelection, len(acts)-1)].kind
			s.overlay = OverlayNone
			s.doAction(kind)
		case "BACK", "OPTIONS", "GUIDE":
			s.overlay = OverlayNone
		}

	case OverlayGogLogin:
		switch cmd {
		case "BACK", "GUIDE":
			s.stopGogLoginLocked()
		case "PASTE":
			go s.loginFromClipboard()
		}

	case OverlayNone:
		if s.launching || s.busyMsg != "" {
			return
		}
		if s.uiPaused {
			if cmd == "GUIDE" {
				go i3("workspace", wsLauncher)
				s.overlay = OverlayIngame
				s.overlaySelection = 0
			}
			return
		}
		switch cmd {
		case "GUIDE":
			s.overlay = OverlaySystem
			s.overlaySelection = 0
		case "LEFT":
			s.moveFocus(-1, 0)
		case "RIGHT":
			s.moveFocus(1, 0)
		case "UP":
			s.moveFocus(0, -1)
		case "DOWN":
			s.moveFocus(0, 1)
		case "ENTER", "START": // Hauptaktion des fokussierten Sterns
			s.activateAction()
		case "OPTIONS": // X: alle Aktionen als Menü
			if g := s.currentGame(); g != nil && len(s.actionsFor(g)) > 1 {
				s.overlay = OverlayOptions
				s.overlaySelection = 0
			}
		case "REFRESH":
			s.toastOKLocked("GOG-Bibliothek wird aktualisiert…")
			go s.syncGog()
		case "LB":
			s.cyclePlatform(-1)
		case "RB":
			s.cyclePlatform(1)
		}
	}
}

// ─── Spiele laden ─────────────────────────────────────────────────────────────

func findSteamRoots() []string {
	home := os.Getenv("HOME")
	cands := []string{
		filepath.Join(home, ".steam/debian-installation"),
		filepath.Join(home, ".steam/steam"),
		filepath.Join(home, ".local/share/Steam"),
		filepath.Join(home, ".var/app/com.valvesoftware.Steam/.local/share/Steam"),
	}
	seen := map[string]bool{}
	var out []string
	for _, c := range cands {
		if st, err := os.Stat(filepath.Join(c, "steamapps")); err != nil || !st.IsDir() {
			continue
		}
		real, err := filepath.EvalSymlinks(c)
		if err != nil {
			real = c
		}
		if !seen[real] {
			seen[real] = true
			out = append(out, real)
		}
	}
	return out
}

func steamLibraries(root string) []string {
	libs := []string{filepath.Join(root, "steamapps")}
	f, err := os.Open(filepath.Join(root, "steamapps", "libraryfolders.vdf"))
	if err != nil {
		return libs
	}
	defer f.Close()
	re := regexp.MustCompile(`"path"\s+"(.+)"`)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if m := re.FindStringSubmatch(sc.Text()); m != nil {
			p := filepath.Join(strings.ReplaceAll(m[1], `\\`, `\`), "steamapps")
			dup := false
			for _, l := range libs {
				if l == p {
					dup = true
				}
			}
			if !dup {
				libs = append(libs, p)
			}
		}
	}
	return libs
}

var (
	reName  = regexp.MustCompile(`"name"\s+"(.+)"`)
	reAppID = regexp.MustCompile(`"appid"\s+"(\d+)"`)
	reFlags = regexp.MustCompile(`"StateFlags"\s+"(\d+)"`)
)

func isSteamTool(name string) bool {
	n := strings.ToLower(name)
	if strings.HasPrefix(n, "proton") || strings.HasPrefix(n, "steamworks") || strings.HasPrefix(n, "steamvr") {
		return true
	}
	for _, kw := range []string{"steam linux runtime", "steam vr", "redistributable", "vcredist"} {
		if strings.Contains(n, kw) {
			return true
		}
	}
	return false
}

func getSteamGames() []Game {
	steamRoots = findSteamRoots()
	if len(steamRoots) == 0 {
		log.Printf("[SnowFox] Kein Steam-Ordner gefunden")
		return nil
	}
	var games []Game
	seen := map[int]bool{}
	for _, root := range steamRoots {
		for _, lib := range steamLibraries(root) {
			files, err := os.ReadDir(lib)
			if err != nil {
				continue
			}
			for _, file := range files {
				if !strings.HasSuffix(file.Name(), ".acf") {
					continue
				}
				f, err := os.Open(filepath.Join(lib, file.Name()))
				if err != nil {
					continue
				}
				var name, idStr string
				flags := 4
				sc := bufio.NewScanner(f)
				for sc.Scan() {
					line := sc.Text()
					if m := reName.FindStringSubmatch(line); m != nil {
						name = m[1]
					}
					if m := reAppID.FindStringSubmatch(line); m != nil {
						idStr = m[1]
					}
					if m := reFlags.FindStringSubmatch(line); m != nil {
						flags, _ = strconv.Atoi(m[1])
					}
				}
				f.Close()
				if name == "" || idStr == "" || isSteamTool(name) {
					continue
				}
				id, _ := strconv.Atoi(idStr)
				if seen[id] {
					continue
				}
				seen[id] = true

				// StateFlags: Bit 4 = installiert, Bit 2 = Update nötig (bitte testen)
				status := "bereit"
				if flags&4 == 0 {
					status = "wird-heruntergeladen"
				} else if flags&2 != 0 {
					status = "update-verfugbar"
				}
				games = append(games, Game{
					ID: id, Title: name, Platform: "steam", Genre: "Steam", Status: status,
					LaunchCmd: "steam steam://rungameid/" + idStr,
				})
			}
		}
	}
	log.Printf("[SnowFox] %d Steam-Spiele gefunden", len(games))
	return games
}

func loadGames(path string) ([]Game, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var games []Game
	err = json.NewDecoder(f).Decode(&games)
	for i := range games {
		if games[i].Status == "" {
			games[i].Status = "bereit"
		}
	}
	return games, err
}

// ─── UI ───────────────────────────────────────────────────────────────────────

type UI struct {
	th        *material.Theme
	state     *LauncherState
	lastFrame time.Time
	dt        float64
	nextTick  time.Time
	ease      float32   // Fokus-Animation 0→1 (pro Frame berechnet)
	t0        time.Time // Start (für Schnee/Polarlicht)
	panelVis  float64   // 1 = Panel sichtbar, 0 = weggeglitten (Übersicht)
}

func newUI(s *LauncherState) *UI {
	th := material.NewTheme()
	coll, hasInter := buildFontCollection()
	th.Shaper = text.NewShaper(text.WithCollection(coll))
	if hasInter {
		th.Face = "Inter"
	}
	th.Palette.Bg = colBg
	th.Palette.Fg = colText
	return &UI{th: th, state: s, t0: time.Now(), panelVis: 1}
}

func (ui *UI) layout(gtx layout.Context) layout.Dimensions {
	s := ui.state
	s.mu.Lock()
	defer s.mu.Unlock()

	// Frame-Zeit für zeitbasierte Animationen
	ui.dt = gtx.Now.Sub(ui.lastFrame).Seconds()
	if ui.lastFrame.IsZero() || ui.dt > 0.1 || ui.dt <= 0 {
		ui.dt = 0.016
	}
	ui.lastFrame = gtx.Now

	// Uhr: genau zur nächsten Minute neu zeichnen
	if gtx.Now.After(ui.nextTick) {
		ui.nextTick = gtx.Now.Truncate(time.Minute).Add(time.Minute)
		gtx.Execute(op.InvalidateCmd{At: ui.nextTick})
	}

	paint.Fill(gtx.Ops, colBg)

	ui.layoutSky(gtx) // Sternenkarte + Detail-Panel füllen den ganzen Bildschirm

	// Leiste oben und unten liegen darüber (transparent)
	dims := layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(ui.layoutTopBar),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Dimensions{Size: gtx.Constraints.Max}
		}),
		layout.Rigid(ui.layoutBottomBar),
	)

	og := gtx
	og.Constraints = layout.Exact(dims.Size)

	switch s.overlay {
	case OverlayIngame:
		ui.layoutIngameOverlay(og)
	case OverlayLaunch:
		ui.layoutLaunchOverlay(og)
	case OverlaySystem:
		ui.layoutSystemOverlay(og)
	case OverlayUninstall:
		ui.layoutUninstallOverlay(og)
	case OverlayOptions:
		ui.layoutOptionsOverlay(og)
	case OverlayGogLogin:
		ui.layoutGogLoginOverlay(og)
	}
	if s.launching || s.busyMsg != "" {
		ui.layoutLoadingOverlay(og)
	}
	ui.layoutToast(og)
	return dims
}

// ─── Top-Bar ──────────────────────────────────────────────────────────────────

func (ui *UI) layoutTopBar(gtx layout.Context) layout.Dimensions {
	s := ui.state
	return layout.Inset{Top: unit.Dp(22), Bottom: unit.Dp(18), Left: unit.Dp(56), Right: unit.Dp(56)}.Layout(gtx,
		func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Spacing: layout.SpaceBetween}.Layout(gtx,
				// Brand
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							sz := gtx.Dp(unit.Dp(12))
							paint.FillShape(gtx.Ops, colAccent, clip.UniformRRect(image.Rectangle{Max: image.Pt(sz, sz)}, sz/3).Op(gtx.Ops))
							return layout.Dimensions{Size: image.Pt(sz, sz)}
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layout.Inset{Left: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								return ui.label(gtx, "SnowFox", colText, unit.Sp(18), true)
							})
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layout.Inset{Left: unit.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								return ui.label(gtx, "Console", colDim, unit.Sp(18), false)
							})
						}),
					)
				}),
				// Tabs
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return layout.Center.Layout(gtx, ui.layoutTabs)
				}),
				// Status + Uhr
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							if s.uiPaused && s.activeGame != nil {
								return layout.Inset{Right: unit.Dp(16)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
									return drawPill(gtx, withAlpha(colSuccess, 40), func(gtx layout.Context) layout.Dimensions {
										return layout.Inset{Top: unit.Dp(4), Bottom: unit.Dp(4), Left: unit.Dp(10), Right: unit.Dp(10)}.Layout(gtx,
											func(gtx layout.Context) layout.Dimensions {
												return ui.label(gtx, "Spielt: "+s.activeGame.Title, colSuccess, unit.Sp(11), true)
											})
									})
								})
							}
							return layout.Dimensions{}
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return ui.label(gtx, gtx.Now.Format("15:04"), colDim, unit.Sp(15), false)
						}),
					)
				}),
			)
		})
}

func (ui *UI) layoutTabs(gtx layout.Context) layout.Dimensions {
	children := []layout.FlexChild{
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Right: unit.Dp(16)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return ui.keyCap(gtx, "LB")
			})
		}),
	}
	for _, p := range ui.state.visiblePlatforms() {
		p := p
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: unit.Dp(14), Right: unit.Dp(14)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return ui.layoutTab(gtx, platformLabel(p), p == ui.state.activePlatform)
			})
		}))
	}
	children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Left: unit.Dp(16)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return ui.keyCap(gtx, "RB")
		})
	}))
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx, children...)
}

func (ui *UI) layoutTab(gtx layout.Context, label string, active bool) layout.Dimensions {
	col := colDim
	if active {
		col = colText
	}
	m := op.Record(gtx.Ops)
	d := ui.label(gtx, label, col, unit.Sp(15), active)
	call := m.Stop()
	call.Add(gtx.Ops)

	gap := gtx.Dp(unit.Dp(7))
	bar := gtx.Dp(unit.Dp(3))
	if active {
		r := image.Rect(0, d.Size.Y+gap, d.Size.X, d.Size.Y+gap+bar)
		paint.FillShape(gtx.Ops, colAccent, clip.UniformRRect(r, bar/2).Op(gtx.Ops))
	}
	return layout.Dimensions{Size: image.Pt(d.Size.X, d.Size.Y+gap+bar)}
}

func (ui *UI) layoutCover(gtx layout.Context, g *Game) {
	w, h := gtx.Constraints.Max.X, gtx.Constraints.Max.Y
	sz := image.Pt(w, h)
	accent := platformAccent(g.Platform)

	paint.FillShape(gtx.Ops, colSurface, clip.Rect{Max: sz}.Op())

	if imgOp, ok := covers.get(g.coverKey()); ok && imgOp.Size() != (image.Point{}) {
		is := imgOp.Size()
		sc := float32(w) / float32(is.X)
		if sy := float32(h) / float32(is.Y); sy > sc {
			sc = sy
		}
		ox := (w - int(float32(is.X)*sc)) / 2
		oy := (h - int(float32(is.Y)*sc)) / 2
		st := op.Offset(image.Pt(ox, oy)).Push(gtx.Ops)
		tr := op.Affine(f32.Affine2D{}.Scale(f32.Pt(0, 0), f32.Pt(sc, sc))).Push(gtx.Ops)
		imgOp.Add(gtx.Ops)
		paint.PaintOp{}.Add(gtx.Ops)
		tr.Pop()
		st.Pop()
	} else {
		// Fallback: Farbverlauf + Symbol + Titel
		gs := clip.Rect{Max: sz}.Push(gtx.Ops)
		paint.LinearGradientOp{
			Stop1: f32.Pt(0, 0), Stop2: f32.Pt(0, float32(h)),
			Color1: withAlpha(accent, 70), Color2: colSurface,
		}.Add(gtx.Ops)
		paint.PaintOp{}.Add(gtx.Ops)
		gs.Pop()

		layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return ui.label(gtx, platformSymbol(g.Platform), withAlpha(accent, 210), unit.Sp(52), true)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: unit.Dp(10), Left: unit.Dp(14), Right: unit.Dp(14)}.Layout(gtx,
						func(gtx layout.Context) layout.Dimensions {
							gtx.Constraints.Min.X = gtx.Constraints.Max.X
							return ui.labelBlock(gtx, g.Title, colText, unit.Sp(13), true, 3, text.Middle)
						})
				}),
			)
		})
	}

	// Status-Badge oben links (nur wenn nicht "bereit")
	if g.Status != "bereit" {
		layout.NW.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: unit.Dp(10), Left: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return drawPill(gtx, g.statusColor(), func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: unit.Dp(3), Bottom: unit.Dp(3), Left: unit.Dp(9), Right: unit.Dp(9)}.Layout(gtx,
						func(gtx layout.Context) layout.Dimensions {
							lbl := "UPDATE"
							switch g.Status {
							case "wird-heruntergeladen":
								lbl = "LÄDT"
							case "nicht-installiert":
								lbl = "NICHT INSTALLIERT"
							}
							return ui.label(gtx, lbl, colBg, unit.Sp(9), true)
						})
				})
			})
		})
	}

	// Plattform-Badge oben rechts
	layout.NE.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: unit.Dp(10), Right: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return drawPill(gtx, color.NRGBA{R: 12, G: 10, B: 20, A: 170}, func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(3), Bottom: unit.Dp(3), Left: unit.Dp(9), Right: unit.Dp(9)}.Layout(gtx,
					func(gtx layout.Context) layout.Dimensions {
						return ui.label(gtx, strings.ToUpper(g.Platform), platformAccent(g.Platform), unit.Sp(9), true)
					})
			})
		})
	})
}

// ─── Leerer Zustand ───────────────────────────────────────────────────────────

func emptyTexts(p string, gogConnected bool) (title, desc string) {
	switch p {
	case "epic":
		return "Epic Games noch nicht verbunden", "Die Epic-Anbindung ist in Arbeit.\nBis dahin: Spiele manuell eintragen."
	case "gog":
		if gogConnected {
			return "Keine GOG-Spiele gefunden", "F5 aktualisiert die Bibliothek."
		}
		return "GOG noch nicht verbunden", "Einmalig im Terminal anmelden,\ndanach hier F5 drücken."
	case "godot":
		return "Keine Godot-Spiele gefunden", "Exportierte Linux-Binaries (.x86_64)\nin games.json eintragen."
	case "retro":
		return "Keine ROMs gefunden", "ROMs mit Emulator-Startbefehl\nin games.json eintragen."
	default:
		return "Keine Spiele gefunden", "Spiele in games.json eintragen."
	}
}

func (ui *UI) layoutEmptyState(gtx layout.Context, platform string) layout.Dimensions {
	w := gtx.Dp(unit.Dp(480))
	gtx.Constraints.Min.X, gtx.Constraints.Max.X = w, w
	accent := platformAccent(platform)
	title, desc := emptyTexts(platform, ui.state.gogLoggedIn)

	steps := [][2]string{
		{"Eintrag in games.json anlegen", `"platform": "` + platform + `"`},
		{"Startbefehl festlegen", `"launchCmd": "…"`},
		{"Launcher neu starten", "snowfox node console"},
	}
	if platform == "gog" {
		steps = [][2]string{
			{"Im Launcher-Ordner ausführen", "./snowfox_launcher -gog-login"},
			{"Link öffnen, anmelden, Code einfügen", "Anleitung erscheint im Terminal"},
			{"Launcher öffnen, dann", "F5"},
		}
		if ui.state.gogLoggedIn {
			steps = [][2]string{{"Bibliothek aktualisieren", "F5"}}
		}
	}

	return ui.panel(gtx, colSurface, unit.Dp(20), withAlpha(colText, 14), func(gtx layout.Context) layout.Dimensions {
		return layout.UniformInset(unit.Dp(36)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			children := []layout.FlexChild{
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return ui.symbolBadge(gtx, platformSymbol(platform), accent, unit.Dp(56), unit.Sp(26))
					})
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: unit.Dp(20)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints.Min.X = gtx.Constraints.Max.X
						return ui.labelBlock(gtx, title, colText, unit.Sp(18), true, 2, text.Middle)
					})
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints.Min.X = gtx.Constraints.Max.X
						return ui.labelBlock(gtx, desc, colDim, unit.Sp(13), false, 3, text.Middle)
					})
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: unit.Dp(22), Bottom: unit.Dp(22)}.Layout(gtx, ui.divider)
				}),
			}
			for i, st := range steps {
				i, st := i, st
				children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					pad := unit.Dp(0)
					if i > 0 {
						pad = unit.Dp(12)
					}
					return layout.Inset{Top: pad}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return ui.symbolBadge(gtx, strconv.Itoa(i+1), accent, unit.Dp(24), unit.Sp(11))
							}),
							layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
								return layout.Inset{Left: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
									return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
										layout.Rigid(func(gtx layout.Context) layout.Dimensions {
											return ui.label(gtx, st[0], colText, unit.Sp(12), false)
										}),
										layout.Rigid(func(gtx layout.Context) layout.Dimensions {
											return layout.Inset{Top: unit.Dp(3)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
												return ui.panel(gtx, withAlpha(accent, 22), unit.Dp(5), color.NRGBA{}, func(gtx layout.Context) layout.Dimensions {
													return layout.Inset{Top: unit.Dp(2), Bottom: unit.Dp(2), Left: unit.Dp(7), Right: unit.Dp(7)}.Layout(gtx,
														func(gtx layout.Context) layout.Dimensions {
															return ui.label(gtx, st[1], accent, unit.Sp(10), true)
														})
												})
											})
										}),
									)
								})
							}),
						)
					})
				}))
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
		})
	})
}

// ─── Bottom-Bar ───────────────────────────────────────────────────────────────

func (ui *UI) layoutBottomBar(gtx layout.Context) layout.Dimensions {
	return layout.Inset{Top: unit.Dp(10), Bottom: unit.Dp(20), Left: unit.Dp(56), Right: unit.Dp(56)}.Layout(gtx,
		func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, 0)}
				}),
				layout.Rigid(ui.layoutHints),
			)
		})
}

func (ui *UI) layoutHints(gtx layout.Context) layout.Dimensions {
	type hint struct {
		key, txt string
		col      color.NRGBA
	}
	var hints []hint
	if g := ui.state.currentGame(); g != nil {
		acts := ui.state.actionsFor(g)
		hints = append(hints, hint{"A", acts[0].label, colSuccess})
		if len(acts) > 1 {
			hints = append(hints, hint{"X", "Optionen", colAccentHi})
		}
	}
	hints = append(hints,
		hint{"LB RB", "Sternbild", colAccentHi},
		hint{"LT halten", "Übersicht", colAccentHi},
		hint{"Guide", "System", colDim},
	)
	children := make([]layout.FlexChild, len(hints))
	for i, h := range hints {
		h := h
		children[i] = layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: unit.Dp(24)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return drawPill(gtx, withAlpha(h.col, 38), func(gtx layout.Context) layout.Dimensions {
							return layout.Inset{Top: unit.Dp(3), Bottom: unit.Dp(3), Left: unit.Dp(9), Right: unit.Dp(9)}.Layout(gtx,
								func(gtx layout.Context) layout.Dimensions {
									return ui.label(gtx, h.key, h.col, unit.Sp(11), true)
								})
						})
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Left: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return ui.label(gtx, h.txt, colDim, unit.Sp(12), false)
						})
					}),
				)
			})
		})
	}
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx, children...)
}

// ─── Overlays ─────────────────────────────────────────────────────────────────

// modal zeichnet Dimm-Schicht + zentriertes Panel
func (ui *UI) modal(gtx layout.Context, width unit.Dp, content layout.Widget) layout.Dimensions {
	paint.FillShape(gtx.Ops, colOverlay, clip.Rect{Max: gtx.Constraints.Max}.Op())
	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		w := gtx.Dp(width)
		gtx.Constraints.Min.X, gtx.Constraints.Max.X = w, w
		return ui.panel(gtx, colSurface, unit.Dp(22), withAlpha(colAccentHi, 45), func(gtx layout.Context) layout.Dimensions {
			return layout.UniformInset(unit.Dp(32)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return content(gtx)
			})
		})
	})
}

func (ui *UI) hintLine(gtx layout.Context, txt string) layout.Dimensions {
	return layout.Inset{Top: unit.Dp(22)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return ui.label(gtx, txt, colDim, unit.Sp(11), false)
	})
}

func (ui *UI) layoutIngameOverlay(gtx layout.Context) layout.Dimensions {
	s := ui.state
	name := ""
	if s.activeGame != nil {
		name = s.activeGame.Title
	}
	return ui.modal(gtx, unit.Dp(400), func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return ui.labelBlock(gtx, name, colText, unit.Sp(20), true, 2, text.Start)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(4)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return ui.label(gtx, "Läuft im Hintergrund", colSuccess, unit.Sp(12), false)
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(20), Bottom: unit.Dp(20)}.Layout(gtx, ui.divider)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return ui.optionButton(gtx, "Fortsetzen", "", colAccentHi, s.overlaySelection == 0)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return ui.optionButton(gtx, "Spiel beenden", "", colError, s.overlaySelection == 1)
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return ui.hintLine(gtx, "A  Bestätigen      Guide  Schließen")
			}),
		)
	})
}

func (ui *UI) layoutLaunchOverlay(gtx layout.Context) layout.Dimensions {
	s := ui.state
	if s.focusIndex >= len(s.filtered) {
		return layout.Dimensions{}
	}
	g := &s.filtered[s.focusIndex]
	return ui.modal(gtx, unit.Dp(400), func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return ui.labelBlock(gtx, g.Title, colText, unit.Sp(20), true, 2, text.Start)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							acc := platformAccent(g.Platform)
							return drawPill(gtx, withAlpha(acc, 40), func(gtx layout.Context) layout.Dimensions {
								return layout.Inset{Top: unit.Dp(3), Bottom: unit.Dp(3), Left: unit.Dp(9), Right: unit.Dp(9)}.Layout(gtx,
									func(gtx layout.Context) layout.Dimensions {
										return ui.label(gtx, strings.ToUpper(g.Platform), acc, unit.Sp(10), true)
									})
							})
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layout.Inset{Left: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								return ui.label(gtx, g.Genre, colDim, unit.Sp(12), false)
							})
						}),
					)
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(20), Bottom: unit.Dp(20)}.Layout(gtx, ui.divider)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return ui.optionButton(gtx, "Starten", "", colAccentHi, s.overlaySelection == 0)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return ui.optionButton(gtx, "Abbrechen", "", colDim, s.overlaySelection == 1)
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return ui.hintLine(gtx, "A  Bestätigen      B  Abbrechen")
			}),
		)
	})
}

func (ui *UI) layoutSystemOverlay(gtx layout.Context) layout.Dimensions {
	s := ui.state
	opts := []struct {
		label, sub string
		col        color.NRGBA
	}{
		{"Zu Desktop beenden", "snowfox node desktop", colAccentHi},
		{"Zu Server beenden", "snowfox node server", colAccent},
		{"Neustart", "systemctl reboot", colWarning},
		{"Herunterfahren", "systemctl poweroff", colError},
	}
	return ui.modal(gtx, unit.Dp(440), func(gtx layout.Context) layout.Dimensions {
		children := []layout.FlexChild{
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						sz := gtx.Dp(unit.Dp(12))
						paint.FillShape(gtx.Ops, colAccent, clip.UniformRRect(image.Rectangle{Max: image.Pt(sz, sz)}, sz/3).Op(gtx.Ops))
						return layout.Dimensions{Size: image.Pt(sz, sz)}
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Left: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return ui.label(gtx, "SnowFox", colText, unit.Sp(18), true)
						})
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Left: unit.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return ui.label(gtx, "System", colDim, unit.Sp(18), false)
						})
					}),
				)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(20), Bottom: unit.Dp(20)}.Layout(gtx, ui.divider)
			}),
		}
		for i, o := range opts {
			i, o := i, o
			children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				pad := unit.Dp(0)
				if i > 0 {
					pad = unit.Dp(8)
				}
				return layout.Inset{Top: pad}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return ui.optionButton(gtx, o.label, o.sub, o.col, s.overlaySelection == i)
				})
			}))
		}
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return ui.hintLine(gtx, "A  Ausführen      B / Guide  Schließen")
		}))
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	})
}

func (ui *UI) layoutLoadingOverlay(gtx layout.Context) layout.Dimensions {
	s := ui.state
	gtx.Execute(op.InvalidateCmd{})
	name := ""
	if s.activeGame != nil {
		name = s.activeGame.Title
	}
	title := "Wird gestartet…"
	if s.busyMsg != "" {
		title, name = s.busyMsg, ""
	}
	return ui.modal(gtx, unit.Dp(360), func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Center.Layout(gtx, ui.spinner)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(20)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return ui.label(gtx, title, colText, unit.Sp(18), true)
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					return ui.labelBlock(gtx, name, colDim, unit.Sp(12), false, 1, text.Middle)
				})
			}),
		)
	})
}

func (ui *UI) spinner(gtx layout.Context) layout.Dimensions {
	sz := gtx.Dp(unit.Dp(44))
	const period = 1_200_000_000
	base := float64(gtx.Now.UnixNano()%period) / period * 2 * math.Pi
	const segs = 8
	dot := gtx.Dp(unit.Dp(5))
	r := float64(sz)/2 - float64(dot)
	for i := 0; i < segs; i++ {
		ang := base + float64(i)/segs*2*math.Pi
		x := int(math.Cos(ang)*r) + sz/2
		y := int(math.Sin(ang)*r) + sz/2
		alpha := uint8(50 + 205*i/segs)
		rect := image.Rect(x-dot/2, y-dot/2, x+dot/2+1, y+dot/2+1)
		paint.FillShape(gtx.Ops, withAlpha(colAccentHi, alpha), clip.UniformRRect(rect, dot/2).Op(gtx.Ops))
	}
	return layout.Dimensions{Size: image.Pt(sz, sz)}
}

func (ui *UI) layoutToast(gtx layout.Context) {
	s := ui.state
	if s.toast == "" {
		return
	}
	if gtx.Now.After(s.toastUntil) {
		s.toast = ""
		return
	}
	gtx.Execute(op.InvalidateCmd{At: s.toastUntil})
	layout.S.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Bottom: unit.Dp(110)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			tc := colError
			if s.toastOK {
				tc = colSuccess
			}
			return ui.panel(gtx, colSurfaceHi, unit.Dp(14), withAlpha(tc, 150), func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(12), Bottom: unit.Dp(12), Left: unit.Dp(20), Right: unit.Dp(20)}.Layout(gtx,
					func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								sz := gtx.Dp(unit.Dp(8))
								paint.FillShape(gtx.Ops, tc, clip.UniformRRect(image.Rectangle{Max: image.Pt(sz, sz)}, sz/2).Op(gtx.Ops))
								return layout.Dimensions{Size: image.Pt(sz, sz)}
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return layout.Inset{Left: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
									return ui.label(gtx, s.toast, colText, unit.Sp(13), false)
								})
							}),
						)
					})
			})
		})
	})
}

// ─── Wiederverwendbare Bausteine ──────────────────────────────────────────────

// panel: gefüllte Rundung mit optionalem Rand
func (ui *UI) panel(gtx layout.Context, bg color.NRGBA, radius unit.Dp, border color.NRGBA, w layout.Widget) layout.Dimensions {
	m := op.Record(gtx.Ops)
	dims := w(gtx)
	call := m.Stop()
	rr := gtx.Dp(radius)
	shape := clip.UniformRRect(image.Rectangle{Max: dims.Size}, rr)
	if bg.A > 0 {
		paint.FillShape(gtx.Ops, bg, shape.Op(gtx.Ops))
	}
	if border.A > 0 {
		paint.FillShape(gtx.Ops, border, clip.Stroke{Path: shape.Path(gtx.Ops), Width: float32(gtx.Dp(unit.Dp(1)))}.Op())
	}
	call.Add(gtx.Ops)
	return dims
}

func drawPill(gtx layout.Context, bg color.NRGBA, w layout.Widget) layout.Dimensions {
	m := op.Record(gtx.Ops)
	dims := w(gtx)
	call := m.Stop()
	paint.FillShape(gtx.Ops, bg, clip.UniformRRect(image.Rectangle{Max: dims.Size}, dims.Size.Y/2).Op(gtx.Ops))
	call.Add(gtx.Ops)
	return dims
}

// keyCap: kleine Tasten-Kappe (LB / RB)
func (ui *UI) keyCap(gtx layout.Context, txt string) layout.Dimensions {
	return ui.panel(gtx, colSurface, unit.Dp(6), withAlpha(colText, 20), func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: unit.Dp(3), Bottom: unit.Dp(3), Left: unit.Dp(8), Right: unit.Dp(8)}.Layout(gtx,
			func(gtx layout.Context) layout.Dimensions {
				return ui.label(gtx, txt, colDim, unit.Sp(10), true)
			})
	})
}

// symbolBadge: Quadrat mit Rundung und exakt zentriertem Zeichen
func (ui *UI) symbolBadge(gtx layout.Context, sym string, accent color.NRGBA, size unit.Dp, textSize unit.Sp) layout.Dimensions {
	sz := gtx.Dp(size)
	paint.FillShape(gtx.Ops, withAlpha(accent, 38), clip.UniformRRect(image.Rectangle{Max: image.Pt(sz, sz)}, sz/3).Op(gtx.Ops))
	g := gtx
	g.Constraints = layout.Exact(image.Pt(sz, sz))
	layout.Center.Layout(g, func(gtx layout.Context) layout.Dimensions {
		return ui.label(gtx, sym, accent, textSize, true)
	})
	return layout.Dimensions{Size: image.Pt(sz, sz)}
}

func (ui *UI) optionButton(gtx layout.Context, label, sub string, accent color.NRGBA, selected bool) layout.Dimensions {
	bg := withAlpha(accent, 8)
	fg := withAlpha(colDim, 200)
	border := color.NRGBA{}
	subCol := withAlpha(colDim, 150)
	if selected {
		bg = withAlpha(accent, 52)
		fg = colText
		border = accent
		subCol = withAlpha(colText, 170)
	}
	return ui.panel(gtx, bg, unit.Dp(12), border, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Inset{Top: unit.Dp(14), Bottom: unit.Dp(14), Left: unit.Dp(18), Right: unit.Dp(18)}.Layout(gtx,
			func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					// Marker: nur die gewählte Zeile hat den Punkt in Akzentfarbe
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						sz := gtx.Dp(unit.Dp(10))
						if selected {
							paint.FillShape(gtx.Ops, accent, clip.UniformRRect(image.Rectangle{Max: image.Pt(sz, sz)}, sz/2).Op(gtx.Ops))
						}
						return layout.Dimensions{Size: image.Pt(sz+gtx.Dp(unit.Dp(14)), sz)}
					}),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return ui.label(gtx, label, fg, unit.Sp(15), selected)
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								if sub == "" {
									return layout.Dimensions{}
								}
								return layout.Inset{Top: unit.Dp(3)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
									return ui.label(gtx, sub, subCol, unit.Sp(10), false)
								})
							}),
						)
					}),
				)
			})
	})
}

func (ui *UI) divider(gtx layout.Context) layout.Dimensions {
	sz := image.Pt(gtx.Constraints.Max.X, 1)
	paint.FillShape(gtx.Ops, withAlpha(colAccentHi, 20), clip.Rect{Max: sz}.Op())
	return layout.Dimensions{Size: sz}
}

func (ui *UI) label(gtx layout.Context, txt string, col color.NRGBA, size unit.Sp, bold bool) layout.Dimensions {
	l := material.Label(ui.th, size, txt)
	l.Color = col
	if bold {
		l.Font.Weight = font.Bold
	}
	return l.Layout(gtx)
}

func (ui *UI) labelBlock(gtx layout.Context, txt string, col color.NRGBA, size unit.Sp, bold bool, lines int, align text.Alignment) layout.Dimensions {
	l := material.Label(ui.th, size, txt)
	l.Color = col
	l.MaxLines = lines
	l.Truncator = "…"
	l.Alignment = align
	if bold {
		l.Font.Weight = font.Bold
	}
	return l.Layout(gtx)
}

// ─── Controller (mit Neuverbindung) ───────────────────────────────────────────

// startControllerSafe: startController() kehrt sofort zurück und sucht selbst dauerhaft
// nach Controllern. Es wird deshalb genau EINMAL aufgerufen (eine Wiederholschleife
// hier würde alle paar Sekunden einen weiteren Leser starten → Eingaben vervielfachen sich).
func startControllerSafe(ch chan string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[SnowFox] Controller-Fehler: %v", r)
		}
	}()
	startController(ch)
}

// ─── Main ─────────────────────────────────────────────────────────────────────

func main() {
	gamesPath := flag.String("games", "games.json", "Pfad zur games.json")
	musicFile := flag.String("music", "", "Pfad zur Hintergrundmusik (mp3/ogg/flac)")
	gogDir := flag.String("gog-dir", "", "Installationsordner für GOG-Spiele (Standard: ~/Games/GOG)")
	gogLogin := flag.Bool("gog-login", false, "GOG-Login im Terminal (Alternative zum Login im Launcher)")
	janitor := flag.Bool("janitor", false, "intern: räumt nach dem Beenden Fenster auf Workspace 8 auf")
	flag.Parse()

	if *janitor {
		runJanitor()
		return
	}

	gog := newGogConfig(*gogDir)
	if *gogLogin {
		if err := gogLoginCLI(gog, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "Fehler:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if *musicFile == "" {
		exePath, _ := os.Executable()
		dir := filepath.Dir(exePath)
		for _, ext := range []string{".mp3", ".ogg", ".flac", ".wav", ".opus"} {
			c := filepath.Join(dir, "music"+ext)
			if _, err := os.Stat(c); err == nil {
				*musicFile = c
				log.Printf("[SnowFox] Musik gefunden: %s", c)
				break
			}
		}
	}

	games := getSteamGames()
	if local, err := loadGames(*gamesPath); err == nil {
		log.Printf("[SnowFox] %d manuelle Spiele aus %s geladen", len(local), *gamesPath)
		games = append(games, local...)
	}
	games = append(games, gog.games()...) // Cache: sofort da, auch offline
	if len(games) == 0 {
		log.Println("[SnowFox] Keine Spiele gefunden — Demo-Modus")
		games = []Game{
			{ID: 1, Title: "Hollow Knight", Platform: "steam", Genre: "Metroidvania", Status: "bereit", LaunchCmd: "steam steam://rungameid/367520"},
			{ID: 2, Title: "Stardew Valley", Platform: "steam", Genre: "Simulation", Status: "bereit", LaunchCmd: "steam steam://rungameid/413150"},
			{ID: 3, Title: "Hades", Platform: "steam", Genre: "Roguelike", Status: "update-verfugbar", LaunchCmd: "steam steam://rungameid/1145360"},
			{ID: 4, Title: "Disco Elysium", Platform: "gog", Genre: "RPG", Status: "bereit", LaunchCmd: "lutris lutris:gog-disco-elysium"},
			{ID: 5, Title: "Rocket League", Platform: "epic", Genre: "Sport", Status: "bereit", LaunchCmd: "heroic launch com.epicgames.Sugar"},
			{ID: 6, Title: "Super Mario Bros", Platform: "retro", Genre: "Platformer", Status: "wird-heruntergeladen", LaunchCmd: "fceux ~/ROMs/NES/smb.nes"},
		}
	}
	sort.SliceStable(games, func(i, j int) bool {
		return strings.ToLower(games[i].Title) < strings.ToLower(games[j].Title)
	})

	state := newState(games, *musicFile)
	state.recent = loadRecent()
	state.gog = gog
	state.gogLoggedIn = gog.loggedIn()

	w := new(app.Window)
	state.window = w
	w.Option(app.Title("SnowFoxOS Console"), app.Fullscreen.Option())

	ui := newUI(state)

	go coverLoop(state)
	go state.syncGog() // Bibliothek im Hintergrund abgleichen

	if *musicFile != "" {
		state.music.play()
	}

	startControllerSafe(state.inputCh)

	go func() {
		for cmd := range state.inputCh {
			state.handleInput(cmd)
		}
	}()

	go func() {
		var ops op.Ops
		for {
			switch e := w.Event().(type) {
			case app.DestroyEvent:
				state.music.stop()
				os.Exit(0)

			case app.FrameEvent:
				gtx := app.NewContext(&ops, e)

				for {
					ev, ok := gtx.Event(key.Filter{})
					if !ok {
						break
					}
					ke, ok := ev.(key.Event)
					if !ok {
						continue
					}
					if ke.Name == "Z" { // Übersicht: Taste halten
						if ke.State == key.Press {
							state.handleInput("ZOOM_ON")
						} else {
							state.handleInput("ZOOM_OFF")
						}
						continue
					}
					if ke.State != key.Press {
						continue
					}
					switch ke.Name {
					case key.NameUpArrow, "W":
						state.handleInput("UP")
					case key.NameDownArrow, "S":
						state.handleInput("DOWN")
					case key.NameLeftArrow, "A":
						state.handleInput("LEFT")
					case key.NameRightArrow, "D":
						state.handleInput("RIGHT")
					case key.NameReturn, key.NameEnter, key.NameSpace:
						state.handleInput("ENTER")
					case key.NameEscape, key.NameBack:
						state.handleInput("BACK")
					case "Q", ",":
						state.handleInput("LB")
					case "E", ".":
						state.handleInput("RB")
					case key.NameTab:
						if ke.Modifiers.Contain(key.ModShift) {
							state.handleInput("LB")
						} else {
							state.handleInput("RB")
						}
					case "G", key.NameF1:
						state.handleInput("GUIDE")
					case key.NameF5:
						state.handleInput("REFRESH")
					case "X":
						state.handleInput("OPTIONS")
					case "V":
						if ke.Modifiers.Contain(key.ModCtrl) {
							state.handleInput("PASTE")
						}
					default:
						// Ziffern 1..n wählen direkt eine Plattform
						if n, err := strconv.Atoi(string(ke.Name)); err == nil && n >= 1 && n <= len(platforms) {
							state.selectPlatform(n - 1)
						}
					}
				}

				ui.layout(gtx)
				e.Frame(gtx.Ops)
			}
		}
	}()

	app.Main()
}
