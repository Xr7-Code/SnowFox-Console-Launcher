// pad.go — Auswertung der Controller-Eingaben (reine Logik, ohne cgo, daher testbar)
//
// Richtungen kommen aus drei Quellen: Steuerkreuz (Hat-Achse oder Tasten) und
// linker Stick. Das Steuerkreuz hat Vorrang. Der Stick wird auf -1..1 normiert
// (funktioniert damit auch bei Controllern mit Wertebereich 0..255), nutzt eine
// Hysterese gegen Zittern und liefert nur die dominante Achse.
// Das Auto-Repeat läuft über einen Timer (tick), nicht über neue Achsen-Ereignisse:
// ein still gehaltener Stick sendet nämlich keine Ereignisse mehr.
package main

import (
	"sync"
	"time"
)

const (
	padStickPress   = 0.55 // ab hier gilt der Stick als gedrückt
	padStickRelease = 0.35 // erst darunter gilt er als losgelassen
	padInitialDelay = 380 * time.Millisecond
	padRepeatDelay  = 110 * time.Millisecond
)

type padState struct {
	mu             sync.Mutex
	emit           func(string)
	hatX, hatY     int
	dpad           [4]bool // Tasten-Steuerkreuz: oben, unten, links, rechts
	stickX, stickY float64
	trig           [2]bool // LT, RT gedrückt (halten = Übersicht)
	zoom           bool
	dir            string
	next           time.Time
}

func newPadState(emit func(string)) *padState { return &padState{emit: emit} }

func absf(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// normAxis rechnet einen Rohwert auf -1..1 um (Mitte = Mitte des Wertebereichs)
func normAxis(v, min, max int32) float64 {
	if max <= min {
		return 0
	}
	center := float64(min+max) / 2
	half := float64(max-min) / 2
	n := (float64(v) - center) / half
	if n > 1 {
		return 1
	}
	if n < -1 {
		return -1
	}
	return n
}

// stickDirection: dominante Achse, mit Hysterese für die bisherige Richtung cur
func stickDirection(x, y float64, cur string) string {
	th := func(same bool) float64 {
		if same {
			return padStickRelease
		}
		return padStickPress
	}
	ax, ay := absf(x), absf(y)
	if ax >= ay {
		if x <= -th(cur == "LEFT") {
			return "LEFT"
		}
		if x >= th(cur == "RIGHT") {
			return "RIGHT"
		}
	} else {
		if y <= -th(cur == "UP") {
			return "UP"
		}
		if y >= th(cur == "DOWN") {
			return "DOWN"
		}
	}
	// Dominante Achse reicht nicht: bisherige Richtung halten, solange sie über der Release-Schwelle liegt
	switch cur {
	case "LEFT":
		if x <= -padStickRelease {
			return cur
		}
	case "RIGHT":
		if x >= padStickRelease {
			return cur
		}
	case "UP":
		if y <= -padStickRelease {
			return cur
		}
	case "DOWN":
		if y >= padStickRelease {
			return cur
		}
	}
	return ""
}

func (p *padState) direction() string {
	switch {
	case p.hatY < 0 || p.dpad[0]:
		return "UP"
	case p.hatY > 0 || p.dpad[1]:
		return "DOWN"
	case p.hatX < 0 || p.dpad[2]:
		return "LEFT"
	case p.hatX > 0 || p.dpad[3]:
		return "RIGHT"
	}
	return stickDirection(p.stickX, p.stickY, p.dir)
}

// refresh: Lock wird gehalten. Meldet eine neue Richtung sofort und plant das Repeat.
func (p *padState) refresh(now time.Time) {
	d := p.direction()
	if d == p.dir {
		return
	}
	p.dir = d
	if d != "" {
		p.emit(d)
		p.next = now.Add(padInitialDelay)
	}
}

func (p *padState) setHat(code uint16, v int32, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch code {
	case 0x10: // ABS_HAT0X
		p.hatX = int(v)
	case 0x11: // ABS_HAT0Y
		p.hatY = int(v)
	}
	p.refresh(now)
}

// setStick: axis 0 = X, 1 = Y; n ist normiert (-1..1)
func (p *padState) setStick(axis int, n float64, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if axis == 0 {
		p.stickX = n
	} else {
		p.stickY = n
	}
	p.refresh(now)
}

// normTrigger rechnet einen Trigger auf 0..1 um (Ruhelage = min)
func normTrigger(v, min, max int32) float64 {
	if max <= min {
		return 0
	}
	n := float64(v-min) / float64(max-min)
	if n < 0 {
		return 0
	}
	if n > 1 {
		return 1
	}
	return n
}

// updateZoom: Lock gehalten. Meldet ZOOM_ON / ZOOM_OFF, sobald sich "mindestens ein Trigger gehalten" ändert.
func (p *padState) updateZoom() {
	any := p.trig[0] || p.trig[1]
	if any != p.zoom {
		p.zoom = any
		if any {
			p.emit("ZOOM_ON")
		} else {
			p.emit("ZOOM_OFF")
		}
	}
}

// setTrigger: which 0 = LT, 1 = RT; n ist 0..1 (mit Hysterese)
func (p *padState) setTrigger(which int, n float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.trig[which] {
		p.trig[which] = n > 0.25
	} else {
		p.trig[which] = n > 0.5
	}
	p.updateZoom()
}

func (p *padState) button(code uint16, v int32, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch code {
	case 0x138, 0x139: // BTN_TL2 / BTN_TR2: Trigger als Taste
		if v != 2 {
			p.trig[code-0x138] = v != 0
			p.updateZoom()
		}
		return
	case 0x220, 0x221, 0x222, 0x223: // BTN_DPAD_UP/DOWN/LEFT/RIGHT
		if v == 2 {
			return
		}
		p.dpad[code-0x220] = v != 0
		p.refresh(now)
		return
	}
	if v != 1 { // nur der echte Druck zählt, kein Loslassen, kein Tastatur-Repeat
		return
	}
	if cmd := buttonToCmd(code); cmd != "" {
		p.emit(cmd)
	}
}

// tick wird alle ~10 ms aufgerufen und sendet das Auto-Repeat
func (p *padState) tick(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dir != "" && !now.Before(p.next) {
		p.emit(p.dir)
		p.next = now.Add(padRepeatDelay)
	}
}

// reset: bei Trennung des Controllers
func (p *padState) reset() {
	p.mu.Lock()
	p.hatX, p.hatY, p.stickX, p.stickY = 0, 0, 0, 0
	p.dpad = [4]bool{}
	p.trig = [2]bool{}
	if p.zoom {
		p.zoom = false
		p.emit("ZOOM_OFF")
	}
	p.dir = ""
	p.mu.Unlock()
}

// buttonToCmd: Tastencodes nach linux/input-event-codes.h.
// A/Kreuz = Bestätigen, B/Kreis = Zurück. X und Y (Quadrat/Dreieck) sind je nach
// Controller-Treiber unterschiedlich belegt und bekommen deshalb nur Zusatzfunktionen.
func buttonToCmd(code uint16) string {
	switch code {
	case 0x130: // BTN_SOUTH (A / Kreuz)
		return "ENTER"
	case 0x131: // BTN_EAST (B / Kreis)
		return "BACK"
	case 0x133: // BTN_NORTH (X bzw. Dreieck)
		return "OPTIONS"
	case 0x134: // BTN_WEST (Y bzw. Quadrat)
		return "REFRESH"
	case 0x136: // BTN_TL
		return "LB"
	case 0x137: // BTN_TR
		return "RB"
	case 0x13a: // BTN_SELECT
		return "BACK"
	case 0x13b: // BTN_START
		return "START"
	case 0x13c: // BTN_MODE (Xbox Guide / PS-Taste)
		return "GUIDE"
	}
	return ""
}
