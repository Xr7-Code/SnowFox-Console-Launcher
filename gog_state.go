// gog_state.go — GOG im Launcher-Zustand: Aktionen, Installation, Deinstallation, Sync
//
// Alle Methoden mit "Locked" im Namen (und doAction/activateAction/startGogInstall/
// uninstallGog) erwarten, dass der Aufrufer s.mu hält. Hintergrund-Goroutinen
// gehen ausschließlich über s.update(...).
package main

import (
	"context"
	"errors"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

type installJob struct {
	pct    float64 // 0 = unbekannt
	line   string
	cancel context.CancelFunc
}

type gameAction struct {
	label string
	kind  string // play | install | cancel | uninstall
}

func gameKey(g *Game) string { return g.Platform + ":" + strconv.Itoa(g.ID) }

func (s *LauncherState) currentGame() *Game {
	if s.focusIndex < 0 || s.focusIndex >= len(s.filtered) {
		return nil
	}
	return &s.filtered[s.focusIndex]
}

func (s *LauncherState) toastOKLocked(msg string) {
	s.toast = msg
	s.toastOK = true
	s.toastUntil = time.Now().Add(4 * time.Second)
}

func (s *LauncherState) snapshotGames() []Game {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Game(nil), s.allGames...)
}

// actionsFor: alle Aktionen des fokussierten Sterns (die erste ist die Hauptaktion auf A,
// alle zusammen erscheinen im Optionen-Menü auf X)
func (s *LauncherState) actionsFor(g *Game) []gameAction {
	if g.Ghost {
		return []gameAction{{"Mit GOG verbinden", "gog-login"}}
	}
	if g.Platform == "gog" && g.Managed {
		var acts []gameAction
		switch {
		case s.installs[gameKey(g)] != nil:
			acts = append(acts, gameAction{"Abbrechen", "cancel"})
		case g.Status == "nicht-installiert":
			acts = append(acts, gameAction{"Installieren", "install"})
		default:
			acts = append(acts, gameAction{"Spielen", "play"}, gameAction{"Deinstallieren", "uninstall"})
		}
		acts = append(acts, gameAction{"Bibliothek aktualisieren", "refresh"})
		if s.gogLoggedIn {
			acts = append(acts, gameAction{"Von GOG abmelden", "gog-logout"})
		}
		return acts
	}
	return []gameAction{{"Spielen", "play"}}
}

func (s *LauncherState) activateAction() {
	if g := s.currentGame(); g != nil {
		s.doAction(s.actionsFor(g)[0].kind)
	}
}

func (s *LauncherState) doAction(kind string) {
	g := s.currentGame()
	if g == nil {
		return
	}
	gg := *g
	switch kind {
	case "play":
		s.confirmLaunch()
	case "install":
		s.startGogInstall(gg)
	case "cancel":
		if job := s.installs[gameKey(&gg)]; job != nil {
			job.cancel()
		}
	case "uninstall":
		s.overlay = OverlayUninstall
		s.overlaySelection = 1 // Standard: Abbrechen
	case "gog-login":
		s.startGogLogin()
	case "gog-logout":
		if s.gog != nil {
			if err := s.gog.logout(); err != nil {
				s.toastLocked("Abmelden fehlgeschlagen: " + err.Error())
				return
			}
			s.applyGogLocked()
			s.toastOKLocked("Von GOG abgemeldet. Installierte Spiele bleiben spielbar.")
		}
	case "refresh":
		s.toastOKLocked("GOG-Bibliothek wird aktualisiert…")
		go s.syncGog()
	}
}

// ─── Liste pflegen ────────────────────────────────────────────────────────────

func (s *LauncherState) setStatusLocked(platform string, id int, status string) {
	for i := range s.allGames {
		if s.allGames[i].Platform == platform && s.allGames[i].ID == id {
			s.allGames[i].Status = status
		}
	}
	for i := range s.filtered {
		if s.filtered[i].Platform == platform && s.filtered[i].ID == id {
			s.filtered[i].Status = status
		}
	}
}

// refilterKeepFocus baut die gefilterte Liste neu und hält den Fokus auf demselben Spiel
func (s *LauncherState) refilterKeepFocus() {
	cur := ""
	if g := s.currentGame(); g != nil {
		cur = gameKey(g)
	}
	s.filtered = append(s.filtered[:0], s.allGames...)
	s.focusIndex = 0
	for i := range s.filtered {
		if gameKey(&s.filtered[i]) == cur {
			s.focusIndex = i
			break
		}
	}
	s.syncActivePlatform()
}

func (s *LauncherState) replaceStoreLocked(platform string, games []Game) {
	keep := make([]Game, 0, len(s.allGames)+len(games))
	for _, g := range s.allGames {
		if g.Platform != platform || !g.Managed {
			keep = append(keep, g)
		}
	}
	keep = append(keep, games...)
	sort.SliceStable(keep, func(i, j int) bool {
		return strings.ToLower(keep[i].Title) < strings.ToLower(keep[j].Title)
	})
	s.allGames = keep
	s.refilterKeepFocus()
}

// applyGogLocked liest Bibliothek-Cache + installierte Spiele neu in die Liste
func (s *LauncherState) applyGogLocked() {
	if s.gog == nil {
		return
	}
	games := s.gog.games()
	for i := range games {
		if s.installs[gameKey(&games[i])] != nil {
			games[i].Status = "wird-heruntergeladen"
		}
	}
	s.replaceStoreLocked("gog", games)
	s.gogLoggedIn = s.gog.loggedIn()
}

// syncGog: Bibliothek von GOG holen (Hintergrund-Goroutine, OHNE Lock aufrufen)
func (s *LauncherState) syncGog() {
	if s.gog == nil || !s.gog.loggedIn() {
		return
	}
	if _, err := s.gog.syncLibrary(); err != nil {
		s.update(func() { s.toastLocked("GOG: " + err.Error()) })
		return
	}
	s.update(func() {
		s.applyGogLocked()
		s.toastOKLocked("GOG-Bibliothek aktualisiert")
	})
}

// ─── Installieren / Deinstallieren ────────────────────────────────────────────

func (s *LauncherState) startGogInstall(g Game) {
	key := gameKey(&g)
	if s.installs[key] != nil || s.gog == nil {
		return
	}
	p := gogProduct{ID: g.ID, Title: g.Title, Linux: g.HasLinux, Windows: g.HasWindows}
	osName, err := pickOS(p)
	if err != nil {
		s.toastLocked(err.Error())
		return
	}
	if osName == "windows" {
		if _, err := exec.LookPath("wine"); err != nil {
			s.toastLocked("Für Windows-Spiele fehlt Wine (sudo apt install wine)")
			return
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	job := &installJob{cancel: cancel}
	s.installs[key] = job
	s.setStatusLocked("gog", g.ID, "wird-heruntergeladen")
	s.toastOKLocked("Installation gestartet: " + g.Title)

	go func() {
		in, err := s.gog.install(ctx, p, osName, func(pct float64, info string) {
			s.mu.Lock()
			if pct > 0 {
				job.pct = pct
			}
			job.line = info
			s.mu.Unlock()
			s.window.Invalidate()
		})
		if err == nil {
			inst := s.gog.loadInstalled()
			inst[in.ID] = in
			err = s.gog.saveInstalled(inst)
		}
		s.update(func() {
			canceled := ctx.Err() != nil
			cancel()
			delete(s.installs, key)
			switch {
			case canceled:
				s.setStatusLocked("gog", g.ID, "nicht-installiert")
				s.toastLocked("Installation abgebrochen")
			case err != nil:
				s.setStatusLocked("gog", g.ID, "nicht-installiert")
				s.toastLocked("Installation fehlgeschlagen: " + err.Error())
			default:
				s.applyGogLocked()
				s.toastOKLocked(g.Title + " ist installiert")
			}
		})
	}()
}

func (s *LauncherState) uninstallGog(g Game) {
	if s.gog == nil {
		return
	}
	go func() {
		inst := s.gog.loadInstalled()
		in, ok := inst[g.ID]
		var err error
		if !ok {
			err = errors.New("nicht als installiert verzeichnet")
		} else {
			err = s.gog.uninstall(in)
		}
		if err == nil {
			delete(inst, g.ID)
			err = s.gog.saveInstalled(inst)
		}
		s.update(func() {
			if err != nil {
				s.toastLocked("Deinstallation fehlgeschlagen: " + err.Error())
				return
			}
			s.applyGogLocked()
			s.toastOKLocked(g.Title + " wurde deinstalliert")
		})
	}()
}

// ─── GOG-Anmeldung im Launcher ────────────────────────────────────────────────

func (s *LauncherState) startGogLogin() {
	if s.gog == nil {
		return
	}
	if s.gog.bin == "" {
		s.toastLocked("gogdl nicht gefunden (Heroic installieren)")
		return
	}
	s.gogLoginStatus = ""
	if s.gogLogin == nil {
		l, err := startGogLoginServer(
			func(code string) error { return s.gog.loginWithCode(code) },
			func() { s.update(func() { s.finishGogLoginLocked() }) },
		)
		if err != nil {
			s.toastLocked("Anmeldeseite nicht gestartet: " + err.Error())
			return
		}
		s.gogLogin = l
	}
	s.overlay = OverlayGogLogin
}

func (s *LauncherState) stopGogLoginLocked() {
	if s.gogLogin != nil {
		s.gogLogin.close()
		s.gogLogin = nil
	}
	if s.overlay == OverlayGogLogin {
		s.overlay = OverlayNone
	}
}

func (s *LauncherState) finishGogLoginLocked() {
	s.stopGogLoginLocked()
	s.applyGogLocked()
	s.toastOKLocked("GOG verbunden. Bibliothek wird geladen…")
	go s.syncGog()
}

// loginFromClipboard: Strg+V im Anmeldedialog (Goroutine, ohne Lock aufrufen)
func (s *LauncherState) loginFromClipboard() {
	txt, err := clipboardText()
	code := extractGogCode(txt)
	if err != nil || code == "" {
		msg := "Die Zwischenablage enthält keine GOG-Adresse"
		if err != nil {
			msg = err.Error()
		}
		s.update(func() { s.gogLoginStatus = msg })
		return
	}
	s.update(func() { s.gogLoginStatus = "Anmeldung läuft…" })
	if err := s.gog.loginWithCode(code); err != nil {
		s.update(func() { s.gogLoginStatus = err.Error() })
		return
	}
	s.update(func() { s.finishGogLoginLocked() })
}
