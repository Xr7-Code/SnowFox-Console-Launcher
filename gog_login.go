// gog_login.go — GOG-Anmeldung direkt im Launcher
//
// GOG kennt keinen Geräte-Code-Ablauf: Man meldet sich im Browser an und landet auf einer
// leeren Seite, deren Adresse den Code enthält (...?code=XXXX). Damit man auf der Couch nichts
// abtippen muss, gibt es zwei Wege:
//
//  1. Handy: Der Launcher zeigt einen QR-Code auf eine kleine Seite im lokalen Netz.
//     Dort tippt man "Bei GOG anmelden", kopiert danach die Adresse der leeren Seite und fügt
//     sie auf der Launcher-Seite ein. Der Launcher erledigt den Rest.
//  2. Tastatur: Strg+V übernimmt die Adresse aus der Zwischenablage (xclip, xsel oder wl-paste).
//
// Der Server läuft nur, solange der Anmeldedialog offen ist (höchstens 10 Minuten), horcht auf
// einem zufälligen Port und akzeptiert nur Anfragen mit dem geheimen Pfad aus dem QR-Code.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"os/exec"
	"time"
)

type gogLogin struct {
	URL string
	QR  [][]bool
	srv *http.Server
}

func (l *gogLogin) close() {
	if l != nil && l.srv != nil {
		l.srv.Close()
	}
}

const loginPageCSS = `body{margin:0;padding:28px 22px;background:#1a1825;color:#e0d9f5;font:16px/1.5 system-ui,sans-serif}
h1{font-size:22px;margin:0 0 4px}small{color:#6e6a8a}
a.b,button{display:block;box-sizing:border-box;width:100%;margin:14px 0;padding:14px;border:0;border-radius:12px;
background:#9d6fe8;color:#fff;font:600 16px system-ui,sans-serif;text-align:center;text-decoration:none}
textarea{box-sizing:border-box;width:100%;height:96px;border-radius:12px;border:1px solid #2a2840;background:#201e2e;color:#e0d9f5;padding:10px;font:14px monospace}
.ok{color:#89c98a}.err{color:#e87a7a}ol{padding-left:20px}`

func loginPage(token, msg, cls string) string {
	note := ""
	if msg != "" {
		note = fmt.Sprintf(`<p class="%s">%s</p>`, cls, html.EscapeString(msg))
	}
	return fmt.Sprintf(`<!doctype html><html lang="de"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>SnowFox · GOG verbinden</title><style>%s</style></head><body>
<h1>GOG verbinden</h1><small>SnowFox Console</small>%s
<ol><li>Bei GOG anmelden:</li></ol>
<a class="b" href="%s" target="_blank" rel="noopener">1. Bei GOG anmelden</a>
<ol start="2"><li>Danach erscheint eine fast leere Seite. Kopiere ihre Adresse (oben im Browser) und füge sie hier ein:</li></ol>
<form method="post" action="/%s/submit"><textarea name="code" placeholder="https://embed.gog.com/on_login_success?...code=..." autocomplete="off"></textarea>
<button type="submit">2. Verbinden</button></form></body></html>`,
		loginPageCSS, note, html.EscapeString(gogLoginURL), token)
}

// newLoginHandler: reine HTTP-Logik (testbar). login führt die Anmeldung aus, onSuccess meldet dem Launcher den Erfolg.
func newLoginHandler(token string, login func(code string) error, onSuccess func()) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/"+token, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprint(w, loginPage(token, "", ""))
	})
	mux.HandleFunc("/"+token+"/submit", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			http.Redirect(w, r, "/"+token, http.StatusSeeOther)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		code := extractGogCode(r.FormValue("code"))
		if code == "" {
			fmt.Fprint(w, loginPage(token, "Kein Code gefunden. Bitte die ganze Adresse der leeren Seite einfügen.", "err"))
			return
		}
		if err := login(code); err != nil {
			fmt.Fprint(w, loginPage(token, err.Error(), "err"))
			return
		}
		fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<body style="background:#1a1825;color:#89c98a;font:22px system-ui;padding:40px 24px">GOG ist verbunden.<br><small style="color:#6e6a8a;font-size:15px">Du kannst diese Seite schließen.</small></body>`)
		go onSuccess()
	})
	return mux
}

// lanIP: Adresse im lokalen Netz (der UDP-Trick sendet keine Pakete)
func lanIP() string {
	if c, err := net.Dial("udp", "10.255.255.255:1"); err == nil {
		defer c.Close()
		if a, ok := c.LocalAddr().(*net.UDPAddr); ok && !a.IP.IsLoopback() && a.IP.To4() != nil {
			return a.IP.String()
		}
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil {
				return ipn.IP.String()
			}
		}
	}
	return "127.0.0.1"
}

// startGogLoginServer startet die Seite und liefert URL + QR-Code für den Anmeldedialog
func startGogLoginServer(login func(code string) error, onSuccess func()) (*gogLogin, error) {
	var tb [16]byte
	if _, err := rand.Read(tb[:]); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(tb[:])

	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		return nil, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	url := fmt.Sprintf("http://%s:%d/%s", lanIP(), port, token)

	srv := &http.Server{
		Handler:           newLoginHandler(token, login, onSuccess),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
	}
	go srv.Serve(ln)
	time.AfterFunc(10*time.Minute, func() { srv.Close() })

	return &gogLogin{URL: url, QR: makeQR(url), srv: srv}, nil
}

// clipboardText liest die Zwischenablage über wl-paste, xclip oder xsel
func clipboardText() (string, error) {
	for _, c := range [][]string{{"wl-paste", "-n"}, {"xclip", "-selection", "clipboard", "-o"}, {"xsel", "-ob"}} {
		if _, err := exec.LookPath(c[0]); err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		out, err := exec.CommandContext(ctx, c[0], c[1:]...).Output()
		cancel()
		if err == nil && len(out) > 0 {
			return string(out), nil
		}
	}
	return "", errors.New("keine Zwischenablage verfügbar (sudo apt install xclip)")
}
