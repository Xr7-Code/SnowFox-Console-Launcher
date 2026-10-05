// recent.go — merkt sich, wann ein Spiel zuletzt gestartet wurde
// (Datei: ~/.config/snowfox/recent.json). Zuletzt gespielte Sterne liegen in der Mitte
// ihres Sternbilds.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

func recentPath() string {
	return filepath.Join(os.Getenv("HOME"), ".config", "snowfox", "recent.json")
}

func loadRecent() map[string]int64 {
	m := map[string]int64{}
	if b, err := os.ReadFile(recentPath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func (s *LauncherState) saveRecent() {
	s.mu.Lock()
	cp := make(map[string]int64, len(s.recent))
	for k, v := range s.recent {
		cp[k] = v
	}
	s.mu.Unlock()
	_ = writeJSON(recentPath(), cp)
}
