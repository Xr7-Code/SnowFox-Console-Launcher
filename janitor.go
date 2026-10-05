// janitor.go — räumt nach "Zu Desktop/Server beenden" Fenster auf Workspace 8 auf
//
// Der Launcher beendet sich selbst. Bleibt danach trotzdem ein (z.B. schwarzes) Fenster auf
// dem Launcher-Workspace stehen, schließt dieser Aufräumer es. Er läuft als eigener,
// abgekoppelter Prozess (./snowfox_launcher -janitor) für 25 Sekunden und schreibt auf, was
// er gefunden hat: ~/.cache/snowfox/janitor.log. So sieht man auch, WER das Fenster war.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

type i3Win struct {
	ID           int64
	Class, Title string
}

func collectWindows(n i3Node, ws string, in bool, out *[]i3Win) {
	if n.Type == "workspace" && n.Name == ws {
		in = true
	}
	if in && n.Window > 0 {
		*out = append(*out, i3Win{ID: n.ID, Class: n.WindowProperties.Class, Title: n.WindowProperties.Title})
	}
	for _, c := range n.Nodes {
		collectWindows(c, ws, in, out)
	}
	for _, c := range n.FloatingNodes {
		collectWindows(c, ws, in, out)
	}
}

// startJanitor startet den Aufräumer abgekoppelt (überlebt das Ende des Launchers)
func startJanitor() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe, "-janitor")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		log.Printf("[SnowFox] Aufräumer nicht gestartet: %v", err)
	}
}

func runJanitor() {
	logPath := filepath.Join(os.Getenv("HOME"), ".cache", "snowfox", "janitor.log")
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
	f, _ := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	logf := func(format string, a ...any) {
		if f != nil {
			fmt.Fprintf(f, time.Now().Format("15:04:05 ")+format+"\n", a...)
		}
	}
	logf("--- Aufräumer gestartet (Workspace %s, 25 s)", wsLauncher)

	seen := map[int64]bool{}
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		if out, err := exec.Command("i3-msg", "-t", "get_tree").Output(); err == nil {
			var root i3Node
			if json.Unmarshal(out, &root) == nil {
				var wins []i3Win
				collectWindows(root, wsLauncher, false, &wins)
				for _, w := range wins {
					if !seen[w.ID] {
						seen[w.ID] = true
						logf("schließe Fenster: id=%d class=%q title=%q", w.ID, w.Class, w.Title)
					}
					_ = exec.Command("i3-msg", fmt.Sprintf("[con_id=%d] kill", w.ID)).Run()
				}
				if len(wins) > 0 {
					_ = exec.Command("i3-msg", "workspace", "1").Run()
				}
			}
		} else {
			logf("i3-msg get_tree fehlgeschlagen: %v", err)
		}
		time.Sleep(time.Second)
	}
	logf("--- Aufräumer beendet (%d Fenster geschlossen)", len(seen))
}
