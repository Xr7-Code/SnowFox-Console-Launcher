// SnowFoxOS Console Launcher — main.go
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"image/jpeg"
	_ "image/jpeg"
	_ "image/png"

	"gioui.org/f32"

	"gioui.org/app"
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
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// ─── Farben ───────────────────────────────────────────────────────────────────

var (
	colorBgBase      = hexColor(0x11111b)
	colorBgAlt       = hexColor(0x1e1e2e)
	colorBgCard      = hexColor(0x181825)
	colorAccentPrim  = hexColor(0xb4befe)
	colorAccentSec   = hexColor(0xcba6f7)
	colorTextPrim    = hexColor(0xcdd6f4)
	colorTextMuted   = hexColor(0x6c7086)
	colorAlert       = hexColor(0xf38ba8)
	colorSuccess     = hexColor(0xa6e3a1)
	colorWarning     = hexColor(0xf9e2af)
	colorBorderDim   = color.NRGBA{R: 180, G: 190, B: 254, A: 18}
	colorFocusBorder = hexColor(0xb4befe)
	colorOverlayBg   = color.NRGBA{R: 0, G: 0, B: 0, A: 200}
)

func hexColor(hex uint32) color.NRGBA {
	return color.NRGBA{
		R: uint8(hex >> 16),
		G: uint8((hex >> 8) & 0xff),
		B: uint8(hex & 0xff),
		A: 255,
	}
}

func withAlpha(c color.NRGBA, a uint8) color.NRGBA {
	c.A = a
	return c
}

// ─── Cover-Cache ──────────────────────────────────────────────────────────────

type coverCache struct {
	images map[int]paint.ImageOp
}

var covers = &coverCache{images: make(map[int]paint.ImageOp)}

func (c *coverCache) get(appID int) (paint.ImageOp, bool) {
	op, ok := c.images[appID]
	return op, ok
}

func (c *coverCache) load(appID int) {
	if _, ok := c.images[appID]; ok {
		return
	}
	path := fmt.Sprintf("%s/.steam/debian-installation/appcache/librarycache/%d/library_600x900.jpg",
		os.Getenv("HOME"), appID)
	f, err := os.Open(path)
	if err != nil {
		c.images[appID] = paint.ImageOp{} // kein Cover vorhanden
		return
	}
	defer f.Close()
	img, err := jpeg.Decode(f)
	if err != nil {
		c.images[appID] = paint.ImageOp{}
		return
	}
	c.images[appID] = paint.NewImageOp(img)
}

func loadAllCovers(games []Game) {
	for _, g := range games {
		if g.Platform == "steam" && g.ID > 0 {
			covers.load(g.ID)
		}
	}
}

// ─── Datenmodell ──────────────────────────────────────────────────────────────

type Game struct {
	ID        int    `json:"id"`
	Title     string `json:"title"`
	Platform  string `json:"platform"`
	Genre     string `json:"genre"`
	Status    string `json:"status"`
	Emoji     string `json:"emoji"`
	LaunchCmd string `json:"launchCmd"`
}

func (g *Game) statusColor() color.NRGBA {
	switch g.Status {
	case "bereit":
		return colorSuccess
	case "update-verfugbar":
		return colorWarning
	case "wird-heruntergeladen":
		return colorAlert
	default:
		return colorTextMuted
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
	default:
		return g.Status
	}
}

// ─── Schriften ────────────────────────────────────────────────────────────────

func buildFontCollection() []text.FontFace {
	var collection []text.FontFace

	// Inter laden falls vorhanden
	fontDirs := []string{
		"/usr/share/fonts/opentype/inter/",
		"/usr/share/fonts/truetype/inter/",
	}
	fonts := map[string]font.Weight{
		"Inter-Regular.otf": font.Normal,
		"Inter-Medium.otf":  font.Medium,
		"Inter-Bold.otf":    font.Bold,
	}
	for _, dir := range fontDirs {
		for file, weight := range fonts {
			if b, err := os.ReadFile(dir + file); err == nil {
				if face, err := opentype.Parse(b); err == nil {
					collection = append(collection, text.FontFace{
						Font: font.Font{Typeface: "Inter", Weight: weight},
						Face: face,
					})
				}
			}
		}
	}

	// Fallback: Go-Standardschriften
	collection = append(collection, gofont.Collection()...)
	return collection
}

// ─── Musik (via ffplay) ───────────────────────────────────────────────────────

type MusicPlayer struct {
	file    string
	cmd     *exec.Cmd
	volume  float64 // 0.0 – 1.0
	fading  bool
	fadeDir int // -1 = out, +1 = in
}

func newMusicPlayer(file string) *MusicPlayer {
	return &MusicPlayer{file: file, volume: 0.2}
}

func (m *MusicPlayer) play() {
	if m.file == "" {
		return
	}
	m.stop()
	vol := int(m.volume * 100)
	// -nodisp: kein Videofenster
	// -autoexit: beendet sich wenn Datei fertig (loop verhindert das)
	// -loop 0: unendlich wiederholen
	// -af volume: Lautstärke setzen
	m.cmd = exec.Command("ffplay",
		"-nodisp",
		"-loop", "0",
		"-af", fmt.Sprintf("volume=%d/100", vol),
		"-loglevel", "quiet",
		m.file,
	)
	m.cmd.Stdin = nil
	m.cmd.Stdout = nil
	m.cmd.Stderr = nil
	if err := m.cmd.Start(); err != nil {
		log.Printf("[SnowFox] Musik-Fehler: %v", err)
		m.cmd = nil
	} else {
		log.Printf("[SnowFox] Musik gestartet: %s", m.file)
	}
}

func (m *MusicPlayer) stop() {
	if m.cmd != nil && m.cmd.Process != nil {
		m.cmd.Process.Kill()
		m.cmd = nil
	}
}

// FadeOut startet das Ausblenden — wird in einer Goroutine mit Ticker gemacht
func (m *MusicPlayer) fadeOut(done func()) {
	if m.cmd == nil {
		done()
		return
	}
	go func() {
		steps := 20
		for i := steps; i >= 0; i-- {
			vol := int(float64(i) / float64(steps) * 100)
			// ffplay hat kein Runtime-Volume-Control — wir starten neu mit niedrigem Volume
			// und stoppen dann. Für echtes Fading würde man pactl/amixer nutzen.
			_ = vol
			time.Sleep(50 * time.Millisecond)
		}
		m.stop()
		done()
	}()
}

func (m *MusicPlayer) fadeOutWithPactl(done func()) {
	if m.cmd == nil || m.file == "" {
		done()
		return
	}
	go func() {
		// Fade via ffplay neu starten mit sinkender Lautstärke
		// Wir stoppen ffplay und spielen kurz mit sinkendem Volume neu ab
		steps := 18
		for i := steps; i >= 0; i-- {
			vol := int(float64(i) / float64(steps) * float64(m.volume) * 100)
			m.stop()
			if i > 0 {
				m.cmd = exec.Command("ffplay",
					"-nodisp", "-loop", "0",
					"-af", fmt.Sprintf("volume=%d/100", vol),
					"-loglevel", "quiet",
					m.file,
				)
				m.cmd.Stdin = nil
				m.cmd.Stdout = nil
				m.cmd.Stderr = nil
				m.cmd.Start()
			}
			time.Sleep(55 * time.Millisecond)
		}
		m.stop()
		done()
	}()
}

// ─── App-Zustand ──────────────────────────────────────────────────────────────

var platforms = []string{"all", "steam", "epic", "gog", "retro"}
var platformLabels = map[string]string{
	"all":   "Alle",
	"steam": "Steam",
	"epic":  "Epic Games",
	"gog":   "GOG",
	"retro": "Retro",
}

type OverlayMode int

const (
	OverlayNone    OverlayMode = iota
	OverlayIngame              // Xbox-Taste während Spiel läuft
	OverlayLaunch              // Bestätigung vor dem Start
	OverlaySystem              // Xbox-Taste ohne laufendes Spiel → System-Menü
)

type LauncherState struct {
	allGames        []Game
	filtered        []Game
	activePlatform  string
	focusIndex      int
	uiPaused        bool
	inputCh         chan string
	cols            int
	scrollY         float32
	window          *app.Window
	activeCmd       *exec.Cmd
	activeGame      *Game
	overlay         OverlayMode
	overlaySelection int
	music           *MusicPlayer
	launching       bool // Ladeanimation
	terminateRequested bool
}

func newState(games []Game, musicFile string) *LauncherState {
	s := &LauncherState{
		allGames:       games,
		activePlatform: "all",
		inputCh:        make(chan string, 32),
		cols:           5,
		music:          newMusicPlayer(musicFile),
	}
	s.applyFilter()
	return s
}

func (s *LauncherState) applyFilter() {
	s.filtered = s.filtered[:0]
	for _, g := range s.allGames {
		if s.activePlatform == "all" || g.Platform == s.activePlatform {
			s.filtered = append(s.filtered, g)
		}
	}
	s.focusIndex = 0
	s.scrollY = 0
}

func (s *LauncherState) cyclePlatform(dir int) {
	if s.overlay != OverlayNone {
		return
	}
	idx := 0
	for i, p := range platforms {
		if p == s.activePlatform {
			idx = i
			break
		}
	}
	idx = (idx + dir + len(platforms)) % len(platforms)
	s.activePlatform = platforms[idx]
	s.applyFilter()
}

func (s *LauncherState) moveFocus(dir int) {
	n := len(s.filtered)
	if n == 0 {
		return
	}
	cols := s.cols
	switch dir {
	case 0:
		s.focusIndex = imax(0, s.focusIndex-cols)
	case 1:
		s.focusIndex = imin(n-1, s.focusIndex+cols)
	case 2:
		s.focusIndex = imax(0, s.focusIndex-1)
	case 3:
		s.focusIndex = imin(n-1, s.focusIndex+1)
	}
}

func (s *LauncherState) confirmLaunch() {
	if len(s.filtered) == 0 || s.launching {
		return
	}
	g := s.filtered[s.focusIndex]
	if g.Status == "wird-heruntergeladen" {
		return
	}
	s.overlay = OverlayLaunch
	s.overlaySelection = 0
	s.window.Invalidate()
}

type i3Node struct {
	Type          string   `json:"type"`
	Name          string   `json:"name"`
	Nodes         []i3Node `json:"nodes"`
	FloatingNodes []i3Node `json:"floating_nodes"`
	Window        int      `json:"window"`
}

// Prüft, ob sich auf dem angegebenen Workspace aktive Fenster befinden
func workspaceHasWindows(wsName string) bool {
	out, err := exec.Command("i3-msg", "-t", "get_tree").Output()
	if err != nil {
		return false
	}
	var root i3Node
	if err := json.Unmarshal(out, &root); err != nil {
		return false
	}
	return checkNodeHasWindows(root, wsName, false)
}

func checkNodeHasWindows(node i3Node, wsName string, inWorkspace bool) bool {
	if node.Type == "workspace" && node.Name == wsName {
		inWorkspace = true
	}
	if inWorkspace && node.Window > 0 {
		return true
	}
	for _, n := range node.Nodes {
		if checkNodeHasWindows(n, wsName, inWorkspace) {
			return true
		}
	}
	for _, n := range node.FloatingNodes {
		if checkNodeHasWindows(n, wsName, inWorkspace) {
			return true
		}
	}
	return false
}

func (s *LauncherState) launch() {
	if len(s.filtered) == 0 || s.launching {
		return
	}
	g := s.filtered[s.focusIndex]
	s.activeGame = &g
	s.overlay = OverlayNone
	s.launching = true
	s.terminateRequested = false
	s.window.Invalidate()

	// Musik ausblenden, dann Spiel starten
	s.music.fadeOutWithPactl(func() {
		s.launching = false
		s.uiPaused = true
		s.window.Invalidate()

		go func() {
			// Auf Workspace 9 wechseln
			exec.Command("i3-msg", "workspace", "9").Run()
			time.Sleep(200 * time.Millisecond) // i3 Zeit zum Umschalten geben

			cmd := exec.Command("sh", "-c", g.LaunchCmd)
			s.activeCmd = cmd
			if err := cmd.Start(); err != nil {
				log.Printf("[SnowFox] Startfehler: %v", err)
				s.activeCmd = nil
				s.activeGame = nil
				s.uiPaused = false
				exec.Command("i3-msg", "workspace", "8").Run()
				s.window.Invalidate()
				return
			}

			doneCh := make(chan struct{})
			go func() {
				cmd.Wait()
				close(doneCh)
			}()

			// 1. Phase: Warten, bis das erste Fenster (z.B. Steam-Ladebox) auftaucht
			windowAppeared := false
			for i := 0; i < 30; i++ { 
				if s.terminateRequested {
					break
				}
				if workspaceHasWindows("9") {
					windowAppeared = true
					break
				}
				time.Sleep(500 * time.Millisecond)
			}

			// 2. Phase: Überwachung mit Karenzzeit (Debounce)
			consecutiveEmptyChecks := 0
			graceThreshold := 10 // 10 Checks * 500ms = 5 Sekunden Karenzzeit

			for {
				if s.terminateRequested {
					break
				}

				cmdRunning := true
				select {
				case <-doneCh:
					cmdRunning = false
				default:
				}

				hasWindow := workspaceHasWindows("9")

				if !hasWindow {
					consecutiveEmptyChecks++
				} else {
					consecutiveEmptyChecks = 0
				}

				// Spiel beendet, wenn Befehl tot UND Workspace seit 5 Sek leer ist
				if !cmdRunning && consecutiveEmptyChecks >= graceThreshold {
					break
				}

				if !windowAppeared && !cmdRunning {
					break
				}

				time.Sleep(500 * time.Millisecond)
			}

			// Zurück zum Launcher aufräumen (Spiel ist jetzt zu)
			s.activeCmd = nil
			s.activeGame = nil
			s.uiPaused = false
			s.overlay = OverlayNone
			s.terminateRequested = false
			exec.Command("i3-msg", "workspace", "8").Run()
			s.music.play()
			s.window.Invalidate()
		}()
	})
}

func (s *LauncherState) terminateGame() {
	log.Println("[SnowFox] Beende Spiel via i3...")
	
	// Schließt das aktive Fenster auf Workspace 9 radikal aber sauber
	exec.Command("i3-msg", "[workspace=9] kill").Run()
	
	// Signalisiert der launch()-Schleife, das Warten sofort abzubrechen
	s.terminateRequested = true
}

// runVisible führt einen Befehl aus und gibt ihn mit Ausgabe in die Konsole
func runVisible(name string, args ...string) {
	fullCmd := name
	for _, a := range args {
		fullCmd += " " + a
	}
	log.Printf("[SnowFox] $ %s", fullCmd)
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err != nil {
		log.Printf("[SnowFox] FEHLER bei '%s': %v", fullCmd, err)
	} else {
		log.Printf("[SnowFox] OK: '%s'", fullCmd)
	}
}

func (s *LauncherState) handleInput(cmd string, w *app.Window) {
	switch s.overlay {

	case OverlayIngame:
		switch cmd {
		case "UP":
			s.overlaySelection = 0
		case "DOWN":
			s.overlaySelection = 1
		case "ENTER":
			if s.overlaySelection == 0 {
				s.overlay = OverlayNone
				go func() {
					time.Sleep(100 * time.Millisecond)
					exec.Command("i3-msg", "workspace", "9").Run()
				}()
			} else {
				s.terminateGame()
			}
		case "GUIDE", "BACK":
			s.overlay = OverlayNone
			go func() {
				time.Sleep(100 * time.Millisecond)
				exec.Command("i3-msg", "workspace", "9").Run()
			}()
		}

	case OverlayLaunch:
		switch cmd {
		case "UP", "DOWN":
			if s.overlaySelection == 0 {
				s.overlaySelection = 1
			} else {
				s.overlaySelection = 0
			}
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
		// 4 Optionen: 0=Desktop, 1=Server, 2=Neustart, 3=Herunterfahren
		switch cmd {
		case "UP":
			if s.overlaySelection > 0 {
				s.overlaySelection--
			}
		case "DOWN":
			if s.overlaySelection < 3 {
				s.overlaySelection++
			}
		case "ENTER":
			s.overlay = OverlayNone
			w.Invalidate()
			switch s.overlaySelection {
			case 0:
				s.music.stop()
				go func() {
					// Erst Spiel auf WS9 schließen (falls vorhanden)
					runVisible("i3-msg", "[workspace=9]", "kill")
					runVisible("i3-msg", "workspace", "1")
					time.Sleep(300 * time.Millisecond)
					runVisible("snowfox", "node", "desktop")
					// Launcher selbst beenden
					os.Exit(0)
				}()
			case 1:
				s.music.stop()
				go func() {
					runVisible("i3-msg", "[workspace=9]", "kill")
					runVisible("i3-msg", "workspace", "1")
					time.Sleep(300 * time.Millisecond)
					runVisible("snowfox", "node", "server")
					os.Exit(0)
				}()
			case 2:
				s.music.stop()
				go func() {
					runVisible("i3-msg", "[workspace=9]", "kill")
					runVisible("i3-msg", "workspace", "1")
					time.Sleep(300 * time.Millisecond)
					runVisible("systemctl", "reboot")
				}()
			case 3:
				s.music.stop()
				go func() {
					runVisible("i3-msg", "[workspace=9]", "kill")
					runVisible("i3-msg", "workspace", "1")
					time.Sleep(300 * time.Millisecond)
					runVisible("systemctl", "poweroff")
				}()
			}
		case "BACK", "GUIDE":
			s.overlay = OverlayNone
		}

	case OverlayNone:
		// Spiel läuft: nur GUIDE erlaubt
		if s.uiPaused {
			if cmd == "GUIDE" {
				exec.Command("i3-msg", "workspace", "8").Run()
				s.overlay = OverlayIngame
				s.overlaySelection = 0
			}
			return
		}

		// Kein Spiel aktiv: GUIDE öffnet System-Overlay
		if cmd == "GUIDE" {
			s.overlay = OverlaySystem
			s.overlaySelection = 0
			w.Invalidate()
			return
		}

		switch cmd {
		case "UP":
			s.moveFocus(0)
		case "DOWN":
			s.moveFocus(1)
		case "LEFT":
			s.moveFocus(2)
		case "RIGHT":
			s.moveFocus(3)
		case "ENTER", "START":
			s.confirmLaunch()
		case "LB":
			s.cyclePlatform(-1)
		case "RB":
			s.cyclePlatform(1)
		}
	}
	w.Invalidate()
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

func calcCols(widthPx int, metric unit.Metric) int {
	cardPx := int(math.Round(float64(metric.PxPerDp) * 185))
	cols := widthPx / (cardPx + 20)
	if cols < 2 {
		return 2
	}
	if cols > 6 {
		return 6
	}
	return cols
}

func rowCount(total, cols int) int {
	return (total + cols - 1) / cols
}

func platformBadge(p string) string {
	return strings.ToUpper(p)
}

func platformSymbol(p string) string {
	switch p {
	case "steam":
		return "S"
	case "epic":
		return "E"
	case "gog":
		return "G"
	case "godot":
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
		return hexColor(0x89b4fa)
	case "epic":
		return hexColor(0xa8d8ea)
	case "gog":
		return hexColor(0xf9e2af)
	case "godot":
		return hexColor(0x478cbf) // Godot Blau
	case "retro":
		return hexColor(0xf38ba8)
	default:
		return colorAccentPrim
	}
}

// ─── UI ───────────────────────────────────────────────────────────────────────

type UI struct {
	th          *material.Theme
	state       *LauncherState
	clock       string
	list        widget.List
	launchAnim  float32 // 0→1 Ladeanimation
	focusAnim   float32 // 0→1 Fokus-Rahmen Lerp
	spinnerAng  float32 // Spinner-Winkel
	lastFocus   int     // letzter Fokus-Index für Änderungserkennung
}

func newUI(s *LauncherState) *UI {
	th := material.NewTheme()
	th.Shaper = text.NewShaper(text.WithCollection(buildFontCollection()))
	th.Palette.Bg = colorBgBase
	th.Palette.Fg = colorTextPrim
	return &UI{th: th, state: s}
}

func (ui *UI) layout(gtx layout.Context) layout.Dimensions {
	cols := calcCols(gtx.Constraints.Max.X, gtx.Metric)
	ui.state.cols = cols

	// Hintergrund
	paint.Fill(gtx.Ops, colorBgBase)

	// Haupt-Layout
	dims := layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(ui.layoutStatusBar),
		layout.Rigid(ui.layoutDivider),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return ui.layoutGrid(gtx, cols)
		}),
		layout.Rigid(ui.layoutDivider),
		layout.Rigid(ui.layoutActionBar),
	)

	// Overlays über allem
	overlayGtx := gtx
	overlayGtx.Constraints = layout.Exact(dims.Size)

	switch ui.state.overlay {
	case OverlayIngame:
		ui.layoutIngameOverlay(overlayGtx)
	case OverlayLaunch:
		ui.layoutLaunchOverlay(overlayGtx)
	case OverlaySystem:
		ui.layoutSystemOverlay(overlayGtx)
	}

	// Ladebildschirm
	if ui.state.launching {
		ui.layoutLoadingOverlay(overlayGtx)
	}

	return dims
}

// ─── Status-Bar ───────────────────────────────────────────────────────────────

func (ui *UI) layoutStatusBar(gtx layout.Context) layout.Dimensions {
	return layout.Inset{
		Top: unit.Dp(18), Bottom: unit.Dp(18),
		Left: unit.Dp(48), Right: unit.Dp(48),
	}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{
			Axis:      layout.Horizontal,
			Alignment: layout.Middle,
			Spacing:   layout.SpaceBetween,
		}.Layout(gtx,
			// Brand
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Right: unit.Dp(32)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							// Kleines farbiges Quadrat als Logo-Ersatz
							sz := gtx.Dp(unit.Dp(10))
							paint.FillShape(gtx.Ops, colorAccentSec,
								clip.RRect{Rect: image.Rectangle{Max: image.Pt(sz, sz)}, NW: 3, NE: 3, SW: 3, SE: 3}.Op(gtx.Ops))
							return layout.Dimensions{Size: image.Pt(sz, sz)}
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layout.Inset{Left: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								return ui.label(gtx, "SnowFox", colorTextPrim, unit.Sp(17), true)
							})
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layout.Inset{Left: unit.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								return ui.label(gtx, "Console", colorTextMuted, unit.Sp(17), false)
							})
						}),
					)
				})
			}),
			// Kategorie-Tabs zentriert
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return layout.Center.Layout(gtx, ui.layoutCategoryTabs)
			}),
			// Rechts: Spiel-Status + Uhr
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: unit.Dp(32)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							if ui.state.uiPaused && ui.state.activeGame != nil {
								return layout.Inset{Right: unit.Dp(16)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
									return drawPill(gtx, colorSuccess, func(gtx layout.Context) layout.Dimensions {
										return ui.label(gtx, "Spielt: "+ui.state.activeGame.Title, colorBgBase, unit.Sp(11), true)
									})
								})
							}
							return layout.Dimensions{}
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return ui.label(gtx, ui.clock, colorTextMuted, unit.Sp(14), false)
						}),
					)
				})
			}),
		)
	})
}

// ─── Kategorie-Tabs ───────────────────────────────────────────────────────────

// countForPlatform zählt Spiele pro Plattform
func (ui *UI) countForPlatform(p string) int {
	if p == "all" {
		return len(ui.state.allGames)
	}
	n := 0
	for _, g := range ui.state.allGames {
		if g.Platform == p {
			n++
		}
	}
	return n
}

func (ui *UI) layoutCategoryTabs(gtx layout.Context) layout.Dimensions {
	return drawRoundedRect(gtx, colorBgAlt, unit.Dp(10), func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{
			Top: unit.Dp(4), Bottom: unit.Dp(4),
			Left: unit.Dp(4), Right: unit.Dp(4),
		}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			children := make([]layout.FlexChild, len(platforms))
			for i, p := range platforms {
				p := p
				active := p == ui.state.activePlatform
				count := ui.countForPlatform(p)
				children[i] = layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return ui.layoutTab(gtx, platformLabels[p], count, active)
				})
			}
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx, children...)
		})
	})
}

func (ui *UI) layoutTab(gtx layout.Context, label string, count int, active bool) layout.Dimensions {
	bg := color.NRGBA{} // transparent
	fg := colorTextMuted
	badgeBg := withAlpha(colorTextMuted, 40)
	badgeFg := colorTextMuted
	if active {
		bg = colorAccentSec
		fg = colorBgBase
		badgeBg = withAlpha(colorBgBase, 60)
		badgeFg = colorBgBase
	}
	return layout.Inset{Left: unit.Dp(2), Right: unit.Dp(2)}.Layout(gtx,
		func(gtx layout.Context) layout.Dimensions {
			return drawRoundedRect(gtx, bg, unit.Dp(7), func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{
					Top: unit.Dp(7), Bottom: unit.Dp(7),
					Left: unit.Dp(14), Right: unit.Dp(10),
				}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return ui.label(gtx, label, fg, unit.Sp(12), active)
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							if count == 0 {
								return layout.Dimensions{}
							}
							return layout.Inset{Left: unit.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								return drawPill(gtx, badgeBg, func(gtx layout.Context) layout.Dimensions {
									return layout.Inset{
										Top: unit.Dp(1), Bottom: unit.Dp(1),
										Left: unit.Dp(5), Right: unit.Dp(5),
									}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
										return ui.label(gtx, fmt.Sprintf("%d", count), badgeFg, unit.Sp(10), true)
									})
								})
							})
						}),
					)
				})
			})
		},
	)
}

// ─── Grid ─────────────────────────────────────────────────────────────────────

func (ui *UI) layoutGrid(gtx layout.Context, cols int) layout.Dimensions {
	gap := gtx.Dp(unit.Dp(18))
	paddingH := gtx.Dp(unit.Dp(48))
	usableW := gtx.Constraints.Max.X - 2*paddingH
	cardW := (usableW - (cols-1)*gap) / cols
	coverH := cardW * 9 / 6 // etwas kompakter als 3:2
	metaH := gtx.Dp(unit.Dp(72))
	cardH := coverH + metaH
	rowH := cardH + gap

	total := len(ui.state.filtered)
	totalRows := rowCount(total, cols)

	// Smooth scroll zu fokussierter Karte
	focusRow := ui.state.focusIndex / cols
	targetY := float32(focusRow*rowH) - (float32(gtx.Constraints.Max.Y)/2 - float32(rowH)/2)
	maxScroll := float32(totalRows*rowH - gtx.Constraints.Max.Y + gap)
	if maxScroll < 0 {
		maxScroll = 0
	}
	if targetY < 0 {
		targetY = 0
	}
	if targetY > maxScroll {
		targetY = maxScroll
	}

	diff := targetY - ui.state.scrollY
	if math.Abs(float64(diff)) > 0.5 {
		ui.state.scrollY += diff * 0.12
		gtx.Execute(op.InvalidateCmd{})
	} else {
		ui.state.scrollY = targetY
	}

	// Clip
	defer clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops).Pop()

	if total == 0 {
		return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return ui.layoutEmptyState(gtx, ui.state.activePlatform)
		})
	}

	firstRow := int(ui.state.scrollY/float32(rowH)) - 1
	lastRow := int((ui.state.scrollY+float32(gtx.Constraints.Max.Y))/float32(rowH)) + 1

	for r := firstRow; r <= lastRow; r++ {
		if r < 0 || r >= totalRows {
			continue
		}
		rowY := float32(r)*float32(rowH) - ui.state.scrollY

		for c := 0; c < cols; c++ {
			idx := r*cols + c
			if idx >= total {
				break
			}
			cardX := float32(paddingH) + float32(c)*float32(cardW+gap)
			stack := op.Offset(image.Pt(int(cardX), int(rowY))).Push(gtx.Ops)
			cardGtx := gtx
			cardGtx.Constraints = layout.Exact(image.Pt(cardW, cardH))
			ui.layoutCard(cardGtx, &ui.state.filtered[idx], idx == ui.state.focusIndex, coverH, metaH)
			stack.Pop()
		}
	}

	return layout.Dimensions{Size: gtx.Constraints.Max}
}

// ─── Leerer Zustand ───────────────────────────────────────────────────────────

func (ui *UI) layoutEmptyState(gtx layout.Context, platform string) layout.Dimensions {
	gtx.Constraints.Max.X = gtx.Dp(unit.Dp(480))
	gtx.Constraints.Min.X = gtx.Dp(unit.Dp(480))

	accent := platformAccent(platform)

	return drawRoundedRect(gtx, colorBgAlt, unit.Dp(20), func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{
			Top: unit.Dp(40), Bottom: unit.Dp(40),
			Left: unit.Dp(40), Right: unit.Dp(40),
		}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,

				// Großes Platform-Symbol
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					sz := gtx.Dp(unit.Dp(56))
					paint.FillShape(gtx.Ops, withAlpha(accent, 30),
						clip.RRect{
							Rect: image.Rectangle{Max: image.Pt(sz, sz)},
							NW: sz / 4, NE: sz / 4, SW: sz / 4, SE: sz / 4,
						}.Op(gtx.Ops))
					// Symbol zentriert im Quadrat
					offset := op.Offset(image.Pt(sz/4, 4)).Push(gtx.Ops)
					l := material.Label(ui.th, unit.Sp(28), platformSymbol(platform))
					l.Color = accent
					l.Font.Weight = font.Bold
					l.Layout(gtx)
					offset.Pop()
					return layout.Dimensions{Size: image.Pt(sz, sz)}
				}),

				// Titel
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: unit.Dp(20)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						title := ui.emptyStateTitle(platform)
						return ui.label(gtx, title, colorTextPrim, unit.Sp(18), true)
					})
				}),

				// Beschreibung
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						desc := ui.emptyStateDesc(platform)
						l := material.Label(ui.th, unit.Sp(13), desc)
						l.Color = colorTextMuted
						l.Alignment = text.Middle
						return l.Layout(gtx)
					})
				}),

				// Divider
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: unit.Dp(24), Bottom: unit.Dp(24)}.Layout(gtx, ui.layoutDivider)
				}),

				// Setup-Schritte
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return ui.layoutEmptyStateSteps(gtx, platform, accent)
				}),
			)
		})
	})
}

func (ui *UI) emptyStateTitle(platform string) string {
	switch platform {
	case "epic":
		return "Epic Games nicht verbunden"
	case "gog":
		return "GOG nicht verbunden"
	case "godot":
		return "Keine Godot-Spiele gefunden"
	case "retro":
		return "Keine ROMs gefunden"
	default:
		return "Keine Spiele gefunden"
	}
}

func (ui *UI) emptyStateDesc(platform string) string {
	switch platform {
	case "epic":
		return "Legendary CLI installieren und anmelden,\num Epic Games-Bibliothek zu laden"
	case "gog":
		return "Comet CLI installieren und anmelden,\num GOG-Bibliothek zu laden"
	case "godot":
		return "Godot-Spiele als .x86_64 Binary\nin games.json eintragen"
	case "retro":
		return "ROM-Dateien in games.json\nmit platform: retro eintragen"
	default:
		return "Spiele in games.json eintragen"
	}
}

func (ui *UI) layoutEmptyStateSteps(gtx layout.Context, platform string, accent color.NRGBA) layout.Dimensions {
	type step struct {
		num  string
		text string
		cmd  string
	}

	var steps []step
	switch platform {
	case "epic":
		steps = []step{
			{"1", "Legendary installieren", "pip3 install legendary-gl"},
			{"2", "Bei Epic anmelden", "legendary auth"},
			{"3", "Spiele synchronisieren", "legendary list"},
			{"4", "Launcher neu starten", "snowfox node console"},
		}
	case "gog":
		steps = []step{
			{"1", "Comet herunterladen", "github.com/nicohman/comet"},
			{"2", "Bei GOG anmelden", "comet auth"},
			{"3", "Bibliothek laden", "comet list"},
			{"4", "Launcher neu starten", "snowfox node console"},
		}
	case "godot":
		steps = []step{
			{"1", "Spiel exportieren", "Godot → Export → Linux/X11"},
			{"2", "Binary ausführbar machen", "chmod +x spiel.x86_64"},
			{"3", "In games.json eintragen", `"platform": "godot"`},
			{"4", "Launcher neu starten", "snowfox node console"},
		}
	default:
		return layout.Dimensions{}
	}

	children := make([]layout.FlexChild, len(steps))
	for i, s := range steps {
		s := s
		i := i
		children[i] = layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			pad := unit.Dp(0)
			if i > 0 {
				pad = unit.Dp(10)
			}
			return layout.Inset{Top: pad}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					// Nummer
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						sz := gtx.Dp(unit.Dp(22))
						paint.FillShape(gtx.Ops, withAlpha(accent, 40),
							clip.RRect{
								Rect: image.Rectangle{Max: image.Pt(sz, sz)},
								NW: sz / 2, NE: sz / 2, SW: sz / 2, SE: sz / 2,
							}.Op(gtx.Ops))
						offset := op.Offset(image.Pt(sz/4, 3)).Push(gtx.Ops)
						l := material.Label(ui.th, unit.Sp(11), s.num)
						l.Color = accent
						l.Font.Weight = font.Bold
						l.Layout(gtx)
						offset.Pop()
						return layout.Dimensions{Size: image.Pt(sz, sz)}
					}),
					// Text + Befehl
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Left: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									return ui.label(gtx, s.text, colorTextPrim, unit.Sp(12), false)
								}),
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									return layout.Inset{Top: unit.Dp(2)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
										return drawRoundedRect(gtx, withAlpha(accent, 15), unit.Dp(4), func(gtx layout.Context) layout.Dimensions {
											return layout.Inset{
												Top: unit.Dp(2), Bottom: unit.Dp(2),
												Left: unit.Dp(6), Right: unit.Dp(6),
											}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
												return ui.label(gtx, s.cmd, withAlpha(accent, 220), unit.Sp(10), true)
											})
										})
									})
								}),
							)
						})
					}),
				)
			})
		})
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

// ─── Karte ────────────────────────────────────────────────────────────────────

func (ui *UI) layoutCard(gtx layout.Context, g *Game, focused bool, coverH, metaH int) layout.Dimensions {
	cardW := gtx.Constraints.Max.X
	cardH := coverH + metaH
	sz := image.Pt(cardW, cardH)

	// Standard-Randbreite
	bw := gtx.Dp(unit.Dp(2))
	rr := gtx.Dp(unit.Dp(14))

	// Fokus-Animation: Alpha Lerp zwischen ungefokusstem und fokussiertem Zustand
	var borderCol color.NRGBA
	if focused {
		// Interpoliere focusAnim → 1.0
		if ui.focusAnim < 1.0 {
			ui.focusAnim += 0.08
			if ui.focusAnim > 1.0 {
				ui.focusAnim = 1.0
			}
			gtx.Execute(op.InvalidateCmd{})
		}
		a := uint8(float32(255) * ui.focusAnim)
		borderCol = color.NRGBA{
			R: uint8(float32(colorBgAlt.R) + float32(colorFocusBorder.R-colorBgAlt.R)*ui.focusAnim),
			G: uint8(float32(colorBgAlt.G) + float32(colorFocusBorder.G-colorBgAlt.G)*ui.focusAnim),
			B: uint8(float32(colorBgAlt.B) + float32(colorFocusBorder.B-colorBgAlt.B)*ui.focusAnim),
			A: a,
		}
		borderCol = withAlpha(colorFocusBorder, uint8(80+float32(175)*ui.focusAnim))
		bw = gtx.Dp(unit.Dp(2 + 2*ui.focusAnim))
	} else {
		// Beim Verlassen des Fokus: Reset
		if ui.lastFocus != ui.state.focusIndex {
			ui.focusAnim = 0
			ui.lastFocus = ui.state.focusIndex
		}
		borderCol = colorBgAlt
	}

	// Rahmen zeichnen
	paint.FillShape(gtx.Ops, borderCol, clip.RRect{
		Rect: image.Rectangle{Max: sz},
		NW: rr, NE: rr, SW: rr, SE: rr,
	}.Op(gtx.Ops))

	// Inner clip
	irr := rr - bw
	if irr < 0 {
		irr = 0
	}
	defer clip.RRect{
		Rect: image.Rectangle{
			Min: image.Pt(bw, bw),
			Max: image.Pt(sz.X-bw, sz.Y-bw),
		},
		NW: irr, NE: irr, SW: irr, SE: irr,
	}.Push(gtx.Ops).Pop()

	paint.Fill(gtx.Ops, colorBgAlt)

	// Cover
	coverGtx := gtx
	coverGtx.Constraints = layout.Exact(image.Pt(cardW, coverH))
	ui.layoutCardCover(coverGtx, g, focused)

	// Meta
	metaStack := op.Offset(image.Pt(0, coverH)).Push(gtx.Ops)
	metaGtx := gtx
	metaGtx.Constraints = layout.Exact(image.Pt(cardW, metaH))
	ui.layoutCardMeta(metaGtx, g)
	metaStack.Pop()

	return layout.Dimensions{Size: sz}
}

func (ui *UI) layoutCardCover(gtx layout.Context, g *Game, focused bool) layout.Dimensions {
	w := gtx.Constraints.Max.X
	h := gtx.Constraints.Max.Y
	sz := image.Pt(w, h)

	// Hintergrund
	paint.FillShape(gtx.Ops, colorBgCard, clip.Rect{Max: sz}.Op())

	// Cover-Bild falls vorhanden
	if imgOp, ok := covers.get(g.ID); ok && imgOp.Size() != (image.Point{}) {
		imgSz := imgOp.Size()
		scaleX := float32(w) / float32(imgSz.X)
		scaleY := float32(h) / float32(imgSz.Y)
		// Cover-Fit: Bild füllt die Karte (crop wenn nötig)
		scale := scaleX
		if scaleY > scaleX {
			scale = scaleY
		}
		scaledW := int(float32(imgSz.X) * scale)
		scaledH := int(float32(imgSz.Y) * scale)
		offX := (w - scaledW) / 2
		offY := (h - scaledH) / 2

		// Clip auf Kartengröße
		defer clip.Rect{Max: sz}.Push(gtx.Ops).Pop()

		// Offset + Skalierung
		imgStack := op.Offset(image.Pt(offX, offY)).Push(gtx.Ops)
		aff := op.Affine(f32.Affine2D{}.Scale(f32.Pt(0, 0), f32.Pt(scale, scale)))
		aff.Add(gtx.Ops)
		imgOp.Add(gtx.Ops)
		paint.PaintOp{}.Add(gtx.Ops)
		imgStack.Pop()
	} else {
		// Fallback: Symbol + Genre
		accent := platformAccent(g.Platform)
		// Subtiler Gradient-Effekt: zwei Farbebenen
		paint.FillShape(gtx.Ops, withAlpha(accent, 8),
			clip.Rect{Min: image.Pt(0, h/2), Max: sz}.Op())

		layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					l := material.Label(ui.th, unit.Sp(54), platformSymbol(g.Platform))
					l.Color = withAlpha(accent, 200)
					l.Font.Weight = font.Bold
					return l.Layout(gtx)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return ui.label(gtx, strings.ToUpper(g.Genre), colorTextMuted, unit.Sp(9), false)
					})
				}),
			)
		})
	}

	// Unterer Akzentstreifen
	accent := platformAccent(g.Platform)
	accentH := gtx.Dp(unit.Dp(3))
	paint.FillShape(gtx.Ops, withAlpha(accent, 200),
		clip.Rect{Min: image.Pt(0, h-accentH), Max: sz}.Op())

	// Platform Badge oben rechts
	layout.NE.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: unit.Dp(10), Right: unit.Dp(10)}.Layout(gtx,
			func(gtx layout.Context) layout.Dimensions {
				return drawPill(gtx, color.NRGBA{0, 0, 0, 140}, func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{
						Top: unit.Dp(3), Bottom: unit.Dp(3),
						Left: unit.Dp(8), Right: unit.Dp(8),
					}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return ui.label(gtx, platformBadge(g.Platform), colorTextPrim, unit.Sp(8), true)
					})
				})
			},
		)
	})

	return layout.Dimensions{Size: sz}
}

func (ui *UI) layoutCardMeta(gtx layout.Context, g *Game) layout.Dimensions {
	return layout.Inset{
		Top: unit.Dp(12), Bottom: unit.Dp(10),
		Left: unit.Dp(14), Right: unit.Dp(10),
	}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return ui.label(gtx, g.Title, colorTextPrim, unit.Sp(12), true)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(5)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							// Status-Punkt
							dotSz := gtx.Dp(unit.Dp(6))
							paint.FillShape(gtx.Ops, g.statusColor(),
								clip.RRect{
									Rect: image.Rectangle{Max: image.Pt(dotSz, dotSz)},
									NW: dotSz / 2, NE: dotSz / 2, SW: dotSz / 2, SE: dotSz / 2,
								}.Op(gtx.Ops))
							return layout.Dimensions{Size: image.Pt(dotSz, dotSz)}
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layout.Inset{Left: unit.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								return ui.label(gtx, g.statusLabel(), colorTextMuted, unit.Sp(10), false)
							})
						}),
					)
				})
			}),
		)
	})
}

// ─── Action-Bar ───────────────────────────────────────────────────────────────

func (ui *UI) layoutActionBar(gtx layout.Context) layout.Dimensions {
	return layout.Inset{
		Top: unit.Dp(14), Bottom: unit.Dp(14),
		Left: unit.Dp(48), Right: unit.Dp(48),
	}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		// Zeigt Controller-Hints wenn Spiel läuft, sonst Tastatur-Hints
		hints := []struct {
			key string
			txt string
			col color.NRGBA
		}{
			{"Enter / A", "Starten", colorAccentPrim},
			{"Q·E / Tab", "Plattform", colorTextMuted},
			{"WASD / Pfeile", "Navigieren", colorTextMuted},
			{"1-6", "Direkt-Auswahl", colorTextMuted},
			{"F1 / G", "System", colorTextMuted},
			{"Esc / B", "Zurück", colorAlert},
		}
		children := make([]layout.FlexChild, len(hints))
		for i, h := range hints {
			h := h
			children[i] = layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Right: unit.Dp(28)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return drawPill(gtx, colorBgAlt, func(gtx layout.Context) layout.Dimensions {
								return layout.Inset{
									Top: unit.Dp(3), Bottom: unit.Dp(3),
									Left: unit.Dp(8), Right: unit.Dp(8),
								}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
									return ui.label(gtx, h.key, h.col, unit.Sp(11), true)
								})
							})
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layout.Inset{Left: unit.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								return ui.label(gtx, h.txt, colorTextMuted, unit.Sp(12), false)
							})
						}),
					)
				})
			})
		}
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx, children...)
	})
}

func (ui *UI) layoutDivider(gtx layout.Context) layout.Dimensions {
	sz := image.Pt(gtx.Constraints.Max.X, 1)
	paint.FillShape(gtx.Ops, colorBorderDim, clip.Rect{Max: sz}.Op())
	return layout.Dimensions{Size: sz}
}

// ─── Ingame Overlay (Xbox-Taste) ──────────────────────────────────────────────

func (ui *UI) layoutIngameOverlay(gtx layout.Context) layout.Dimensions {
	s := ui.state

	// Dimm-Schicht
	paint.FillShape(gtx.Ops, colorOverlayBg, clip.Rect{Max: gtx.Constraints.Max}.Op())

	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.X = gtx.Dp(unit.Dp(380))
		gtx.Constraints.Min.X = gtx.Dp(unit.Dp(380))

		return drawRoundedRect(gtx, colorBgAlt, unit.Dp(20), func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{
				Top: unit.Dp(36), Bottom: unit.Dp(28),
				Left: unit.Dp(36), Right: unit.Dp(36),
			}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,

					// Spiel-Name
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						name := ""
						if s.activeGame != nil {
							name = s.activeGame.Title
						}
						return ui.label(gtx, name, colorTextPrim, unit.Sp(20), true)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Top: unit.Dp(4)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return ui.label(gtx, "Läuft auf Workspace 9", colorTextMuted, unit.Sp(12), false)
						})
					}),

					// Trennlinie
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Top: unit.Dp(20), Bottom: unit.Dp(20)}.Layout(gtx, ui.layoutDivider)
					}),

					// Optionen
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return ui.overlayButton(gtx, "Fortsetzen", colorAccentPrim, s.overlaySelection == 0)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Top: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return ui.overlayButton(gtx, "Spiel beenden", colorAlert, s.overlaySelection == 1)
						})
					}),

					// Hinweis
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Top: unit.Dp(24)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return ui.label(gtx, "[A] Bestätigen   [Guide] Schließen", colorTextMuted, unit.Sp(11), false)
						})
					}),
				)
			})
		})
	})
}

// ─── Launch Overlay (Bestätigung) ─────────────────────────────────────────────

func (ui *UI) layoutLaunchOverlay(gtx layout.Context) layout.Dimensions {
	s := ui.state
	var g *Game
	if s.focusIndex < len(s.filtered) {
		g = &s.filtered[s.focusIndex]
	}
	if g == nil {
		return layout.Dimensions{}
	}

	paint.FillShape(gtx.Ops, colorOverlayBg, clip.Rect{Max: gtx.Constraints.Max}.Op())

	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.X = gtx.Dp(unit.Dp(380))
		gtx.Constraints.Min.X = gtx.Dp(unit.Dp(380))

		return drawRoundedRect(gtx, colorBgAlt, unit.Dp(20), func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{
				Top: unit.Dp(36), Bottom: unit.Dp(28),
				Left: unit.Dp(36), Right: unit.Dp(36),
			}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,

					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return ui.label(gtx, g.Title, colorTextPrim, unit.Sp(20), true)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Top: unit.Dp(4)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									return drawPill(gtx, withAlpha(platformAccent(g.Platform), 40), func(gtx layout.Context) layout.Dimensions {
										return layout.Inset{
											Top: unit.Dp(3), Bottom: unit.Dp(3),
											Left: unit.Dp(8), Right: unit.Dp(8),
										}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
											return ui.label(gtx, platformBadge(g.Platform), platformAccent(g.Platform), unit.Sp(10), true)
										})
									})
								}),
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									return layout.Inset{Left: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
										return ui.label(gtx, g.Genre, colorTextMuted, unit.Sp(12), false)
									})
								}),
							)
						})
					}),

					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Top: unit.Dp(20), Bottom: unit.Dp(20)}.Layout(gtx, ui.layoutDivider)
					}),

					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return ui.overlayButton(gtx, "Starten", colorAccentPrim, s.overlaySelection == 0)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Top: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return ui.overlayButton(gtx, "Abbrechen", colorTextMuted, s.overlaySelection == 1)
						})
					}),

					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Top: unit.Dp(24)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return ui.label(gtx, "[A] Bestätigen   [B] Abbrechen", colorTextMuted, unit.Sp(11), false)
						})
					}),
				)
			})
		})
	})
}

// ─── System Overlay (Guide ohne Spiel) ────────────────────────────────────────

func (ui *UI) layoutSystemOverlay(gtx layout.Context) layout.Dimensions {
	s := ui.state

	paint.FillShape(gtx.Ops, colorOverlayBg, clip.Rect{Max: gtx.Constraints.Max}.Op())

	type sysOption struct {
		label   string
		sub     string
		col     color.NRGBA
		danger  bool
	}
	options := []sysOption{
		{"Zu Desktop beenden", "snowfox node desktop", colorAccentPrim, false},
		{"Zu Server beenden",  "snowfox node server",  colorAccentSec,  false},
		{"Neustart",           "systemctl reboot",     colorWarning,    false},
		{"Herunterfahren",     "systemctl poweroff",   colorAlert,      true},
	}

	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.X = gtx.Dp(unit.Dp(420))
		gtx.Constraints.Min.X = gtx.Dp(unit.Dp(420))

		return drawRoundedRect(gtx, colorBgAlt, unit.Dp(20), func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{
				Top: unit.Dp(36), Bottom: unit.Dp(32),
				Left: unit.Dp(36), Right: unit.Dp(36),
			}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				children := make([]layout.FlexChild, 0, 10)

				// Header
				children = append(children,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								// Kleines Quadrat als Icon
								sz := gtx.Dp(unit.Dp(10))
								paint.FillShape(gtx.Ops, colorAccentSec,
									clip.RRect{Rect: image.Rectangle{Max: image.Pt(sz, sz)},
										NW: 3, NE: 3, SW: 3, SE: 3}.Op(gtx.Ops))
								return layout.Dimensions{Size: image.Pt(sz, sz)}
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return layout.Inset{Left: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
									return ui.label(gtx, "SnowFox", colorTextPrim, unit.Sp(18), true)
								})
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return layout.Inset{Left: unit.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
									return ui.label(gtx, "System", colorTextMuted, unit.Sp(18), false)
								})
							}),
						)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Top: unit.Dp(20), Bottom: unit.Dp(20)}.Layout(gtx, ui.layoutDivider)
					}),
				)

				// Optionen
				for i, opt := range options {
					i, opt := i, opt
					children = append(children,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							pad := unit.Dp(0)
							if i > 0 {
								pad = unit.Dp(8)
							}
							return layout.Inset{Top: pad}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								return ui.sysOverlayButton(gtx, opt.label, opt.sub, opt.col, s.overlaySelection == i)
							})
						}),
					)
				}

				// Hinweis
				children = append(children,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Top: unit.Dp(24)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return ui.label(gtx, "[A] Ausführen   [B / Guide] Schließen", colorTextMuted, unit.Sp(11), false)
						})
					}),
				)

				return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
			})
		})
	})
}

func (ui *UI) sysOverlayButton(gtx layout.Context, label, sub string, accentCol color.NRGBA, selected bool) layout.Dimensions {
	bg := withAlpha(accentCol, 12)
	labelCol := colorTextMuted
	subCol := withAlpha(colorTextMuted, 120)
	borderCol := color.NRGBA{}

	if selected {
		bg = withAlpha(accentCol, 28)
		labelCol = accentCol
		subCol = withAlpha(accentCol, 160)
		borderCol = withAlpha(accentCol, 160)
	}

	bw := gtx.Dp(unit.Dp(1))
	rr := gtx.Dp(unit.Dp(10))

	macro := op.Record(gtx.Ops)
	inner := layout.Inset{
		Top: unit.Dp(12), Bottom: unit.Dp(12),
		Left: unit.Dp(16), Right: unit.Dp(16),
	}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return ui.label(gtx, label, labelCol, unit.Sp(14), selected)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(3)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return ui.label(gtx, sub, subCol, unit.Sp(10), false)
				})
			}),
		)
	})
	call := macro.Stop()
	sz := inner.Size

	if selected {
		paint.FillShape(gtx.Ops, borderCol, clip.RRect{
			Rect: image.Rectangle{Max: sz},
			NW: rr, NE: rr, SW: rr, SE: rr,
		}.Op(gtx.Ops))
		paint.FillShape(gtx.Ops, bg, clip.RRect{
			Rect: image.Rectangle{
				Min: image.Pt(bw, bw),
				Max: image.Pt(sz.X-bw, sz.Y-bw),
			},
			NW: rr - bw, NE: rr - bw, SW: rr - bw, SE: rr - bw,
		}.Op(gtx.Ops))
	} else {
		paint.FillShape(gtx.Ops, bg, clip.RRect{
			Rect: image.Rectangle{Max: sz},
			NW: rr, NE: rr, SW: rr, SE: rr,
		}.Op(gtx.Ops))
	}
	call.Add(gtx.Ops)
	return inner
}

// ─── Lade-Overlay ─────────────────────────────────────────────────────────────

func (ui *UI) layoutLoadingOverlay(gtx layout.Context) layout.Dimensions {
	paint.FillShape(gtx.Ops, colorOverlayBg, clip.Rect{Max: gtx.Constraints.Max}.Op())

	// Spinner-Winkel animieren
	ui.spinnerAng += 0.06
	if ui.spinnerAng > math.Pi*2 {
		ui.spinnerAng -= math.Pi * 2
	}
	gtx.Execute(op.InvalidateCmd{})

	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return drawRoundedRect(gtx, colorBgAlt, unit.Dp(20), func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{
				Top: unit.Dp(40), Bottom: unit.Dp(40),
				Left: unit.Dp(56), Right: unit.Dp(56),
			}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,

					// Spinner
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						sz := gtx.Dp(unit.Dp(40))
						center := image.Pt(sz/2, sz/2)
						segments := 8
						for i := 0; i < segments; i++ {
							ang := float64(i)/float64(segments)*math.Pi*2 + float64(ui.spinnerAng)
							r := float64(sz/2) - 4
							x := int(math.Cos(ang)*r) + center.X
							y := int(math.Sin(ang)*r) + center.Y
							dotSz := gtx.Dp(unit.Dp(4))
							alpha := uint8(40 + (215 * i / segments))
							paint.FillShape(gtx.Ops, withAlpha(colorAccentPrim, alpha),
								clip.RRect{
									Rect: image.Rectangle{
										Min: image.Pt(x-dotSz/2, y-dotSz/2),
										Max: image.Pt(x+dotSz/2, y+dotSz/2),
									},
									NW: dotSz / 2, NE: dotSz / 2,
									SW: dotSz / 2, SE: dotSz / 2,
								}.Op(gtx.Ops))
						}
						return layout.Dimensions{Size: image.Pt(sz, sz)}
					}),

					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Top: unit.Dp(20)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return ui.label(gtx, "Wird gestartet…", colorTextPrim, unit.Sp(18), true)
						})
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Top: unit.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return ui.label(gtx, "Musik wird ausgeblendet", colorTextMuted, unit.Sp(12), false)
						})
					}),
				)
			})
		})
	})
}

// ─── Overlay-Button ───────────────────────────────────────────────────────────

func (ui *UI) overlayButton(gtx layout.Context, label string, accentCol color.NRGBA, selected bool) layout.Dimensions {
	bg := withAlpha(accentCol, 20)
	fg := colorTextMuted
	borderCol := color.NRGBA{}
	if selected {
		bg = withAlpha(accentCol, 35)
		fg = accentCol
		borderCol = withAlpha(accentCol, 180)
	}

	bw := gtx.Dp(unit.Dp(1))
	rr := gtx.Dp(unit.Dp(10))

	// Aufzeichnen für Größenmessung
	macro := op.Record(gtx.Ops)
	inner := layout.Inset{
		Top: unit.Dp(13), Bottom: unit.Dp(13),
		Left: unit.Dp(18), Right: unit.Dp(18),
	}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return ui.label(gtx, label, fg, unit.Sp(14), selected)
	})
	call := macro.Stop()
	sz := inner.Size

	// Border
	if selected {
		paint.FillShape(gtx.Ops, borderCol, clip.RRect{
			Rect: image.Rectangle{Max: sz},
			NW: rr, NE: rr, SW: rr, SE: rr,
		}.Op(gtx.Ops))
		inner2 := image.Rectangle{
			Min: image.Pt(bw, bw),
			Max: image.Pt(sz.X-bw, sz.Y-bw),
		}
		paint.FillShape(gtx.Ops, bg, clip.RRect{
			Rect: inner2,
			NW: rr - bw, NE: rr - bw, SW: rr - bw, SE: rr - bw,
		}.Op(gtx.Ops))
	} else {
		paint.FillShape(gtx.Ops, bg, clip.RRect{
			Rect: image.Rectangle{Max: sz},
			NW: rr, NE: rr, SW: rr, SE: rr,
		}.Op(gtx.Ops))
	}
	call.Add(gtx.Ops)
	return inner
}

// ─── Hilfsformen ──────────────────────────────────────────────────────────────

func drawRoundedRect(gtx layout.Context, bg color.NRGBA, radius unit.Dp, w layout.Widget) layout.Dimensions {
	macro := op.Record(gtx.Ops)
	dims := w(gtx)
	call := macro.Stop()
	rr := gtx.Dp(radius)
	paint.FillShape(gtx.Ops, bg, clip.RRect{
		Rect: image.Rectangle{Max: dims.Size},
		NW: rr, NE: rr, SW: rr, SE: rr,
	}.Op(gtx.Ops))
	call.Add(gtx.Ops)
	return dims
}

func drawPill(gtx layout.Context, bg color.NRGBA, w layout.Widget) layout.Dimensions {
	macro := op.Record(gtx.Ops)
	dims := w(gtx)
	call := macro.Stop()
	rr := dims.Size.Y / 2
	paint.FillShape(gtx.Ops, bg, clip.RRect{
		Rect: image.Rectangle{Max: dims.Size},
		NW: rr, NE: rr, SW: rr, SE: rr,
	}.Op(gtx.Ops))
	call.Add(gtx.Ops)
	return dims
}

func (ui *UI) label(gtx layout.Context, txt string, col color.NRGBA, size unit.Sp, bold bool) layout.Dimensions {
	l := material.Label(ui.th, size, txt)
	l.Color = col
	if bold {
		l.Font.Weight = font.Bold
	}
	return l.Layout(gtx)
}

// ─── Games laden ──────────────────────────────────────────────────────────────

func getSteamGames() []Game {
	var games []Game
	steamPath := filepath.Join(os.Getenv("HOME"), ".steam/debian-installation/steamapps/")
	files, err := os.ReadDir(steamPath)
	if err != nil {
		log.Printf("[SnowFox] Steam-Ordner nicht gefunden: %v", err)
		return games
	}
	reName := regexp.MustCompile(`"name"\s+"(.+)"`)
	reAppID := regexp.MustCompile(`"appid"\s+"(\d+)"`)
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".acf") {
			continue
		}
		path := filepath.Join(steamPath, file.Name())
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		var name, appIDStr string
		for scanner.Scan() {
			line := scanner.Text()
			if m := reName.FindStringSubmatch(line); len(m) > 1 {
				name = m[1]
			}
			if m := reAppID.FindStringSubmatch(line); len(m) > 1 {
				appIDStr = m[1]
			}
		}
		f.Close()
		if name != "" && appIDStr != "" {
			// Proton, Runtime, Worker und Tools herausfiltern
			nameLower := strings.ToLower(name)
			skip := false
			for _, kw := range []string{
				"proton", "steam linux runtime", "steamworks",
				"steam vr", "steamvr", "valve steam", "steam client",
				"directx", "vcredist", "dotnet", "microsoft visual",
				"steam native", "scout", "soldier", "sniper", "medic",
			} {
				if strings.Contains(nameLower, kw) {
					skip = true
					break
				}
			}
			if skip {
				continue
			}
			appID, _ := strconv.Atoi(appIDStr)
			games = append(games, Game{
				ID:        appID,
				Title:     name,
				Platform:  "steam",
				Genre:     "Steam",
				Status:    "bereit",
				LaunchCmd: fmt.Sprintf("steam steam://rungameid/%s", appIDStr),
			})
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
	return games, json.NewDecoder(f).Decode(&games)
}

// ─── Controller Safe-Wrapper ──────────────────────────────────────────────────

func startControllerSafe(ch chan string) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[SnowFox] Controller-Fehler: %v", r)
			}
		}()
		startController(ch)
	}()
}

func hasWindowDescendant(node i3Node) bool {
	if node.Window > 0 {
		return true
	}
	for _, n := range node.Nodes {
		if hasWindowDescendant(n) {
			return true
		}
	}
	for _, n := range node.FloatingNodes {
		if hasWindowDescendant(n) {
			return true
		}
	}
	return false
}

// ─── Main ─────────────────────────────────────────────────────────────────────

func main() {
	gamesPath := flag.String("games", "games.json", "Pfad zur games.json")
	musicFile := flag.String("music", "", "Pfad zur Hintergrundmusik (mp3/ogg/flac)")
	flag.Parse()

	// Automatische Musik-Erkennung im Projektordner
	if *musicFile == "" {
		exePath, _ := os.Executable()
		exeDir := filepath.Dir(exePath)
		for _, ext := range []string{".mp3", ".ogg", ".flac", ".wav", ".opus"} {
			candidate := filepath.Join(exeDir, "music"+ext)
			if _, err := os.Stat(candidate); err == nil {
				*musicFile = candidate
				log.Printf("[SnowFox] Musik gefunden: %s", candidate)
				break
			}
		}
	}

	// Spiele laden
	games := getSteamGames()
	if localGames, err := loadGames(*gamesPath); err == nil {
		log.Printf("[SnowFox] %d manuelle Spiele aus %s geladen", len(localGames), *gamesPath)
		games = append(games, localGames...)
	}
	if len(games) == 0 {
		log.Println("[SnowFox] Keine Spiele gefunden — Demo-Modus")
		games = []Game{
			{1, "Hollow Knight", "steam", "Metroidvania", "bereit", "", "steam steam://rungameid/367520"},
			{2, "Stardew Valley", "steam", "Simulation", "bereit", "", "steam steam://rungameid/413150"},
			{3, "Hades", "steam", "Roguelike", "update-verfugbar", "", "steam steam://rungameid/1145360"},
			{4, "Disco Elysium", "gog", "RPG", "bereit", "", "lutris lutris:gog-disco-elysium"},
			{5, "Rocket League", "epic", "Sport", "bereit", "", "heroic launch com.epicgames.Sugar"},
			{6, "Super Mario Bros", "retro", "Platformer", "bereit", "", "fceux /home/xr7-code/ROMs/NES/smb.nes"},
		}
	}

	// Zustand
	state := newState(games, *musicFile)

	// Fenster
	w := new(app.Window)
	state.window = w
	w.Option(app.Title("SnowFoxOS Console"), app.Fullscreen.Option())

	// UI
	ui := newUI(state)
	ui.clock = time.Now().Format("15:04")

	// Cover im Hintergrund laden
	go loadAllCovers(games)

	// Musik starten
	if *musicFile != "" {
		state.music.play()
	}

	// Uhr
	go func() {
		for {
			time.Sleep(30 * time.Second)
			ui.clock = time.Now().Format("15:04")
			w.Invalidate()
		}
	}()

	// Controller
	startControllerSafe(state.inputCh)

	// Input-Loop
	go func() {
		for cmd := range state.inputCh {
			state.handleInput(cmd, w)
		}
	}()

	// Event-Loop
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
					if ke, ok := ev.(key.Event); ok && ke.State == key.Press {
						switch ke.Name {
						// ── Navigation ──────────────────────────
						case key.NameUpArrow, "W":
							state.handleInput("UP", w)
						case key.NameDownArrow, "S":
							state.handleInput("DOWN", w)
						case key.NameLeftArrow, "A":
							state.handleInput("LEFT", w)
						case key.NameRightArrow, "D":
							state.handleInput("RIGHT", w)

						// ── Bestätigen / Abbrechen ───────────────
						case key.NameReturn, key.NameSpace:
							state.handleInput("ENTER", w)
						case key.NameEscape, key.NameBack:
							state.handleInput("BACK", w)

						// ── Plattform-Wechsel ────────────────────
						// Q/E wie Controller LB/RB
						case "Q", ",":
							state.handleInput("LB", w)
						case "E", ".":
							state.handleInput("RB", w)
						// Tab vorwärts, Shift+Tab rückwärts
						case key.NameTab:
							state.handleInput("RB", w)

						// ── System-Overlay ───────────────────────
						case "G", key.NameF1:
							state.handleInput("GUIDE", w)

						// ── Schnell-Shortcuts (nur ohne Overlay) ─
						// Zifferntasten 1-6 für direkte Plattform-Auswahl
						case "1":
							if state.overlay == OverlayNone && !state.uiPaused {
								state.activePlatform = platforms[0]
								state.applyFilter()
								w.Invalidate()
							}
						case "2":
							if state.overlay == OverlayNone && !state.uiPaused && len(platforms) > 1 {
								state.activePlatform = platforms[1]
								state.applyFilter()
								w.Invalidate()
							}
						case "3":
							if state.overlay == OverlayNone && !state.uiPaused && len(platforms) > 2 {
								state.activePlatform = platforms[2]
								state.applyFilter()
								w.Invalidate()
							}
						case "4":
							if state.overlay == OverlayNone && !state.uiPaused && len(platforms) > 3 {
								state.activePlatform = platforms[3]
								state.applyFilter()
								w.Invalidate()
							}
						case "5":
							if state.overlay == OverlayNone && !state.uiPaused && len(platforms) > 4 {
								state.activePlatform = platforms[4]
								state.applyFilter()
								w.Invalidate()
							}
						case "6":
							if state.overlay == OverlayNone && !state.uiPaused && len(platforms) > 5 {
								state.activePlatform = platforms[5]
								state.applyFilter()
								w.Invalidate()
							}
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
