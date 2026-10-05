// sky_ui.go — "Polarnacht": Zeichnen der Sternenkarte (Layout, Navigation und Kamera: sky.go)
//
//	Hintergrund   Polarlicht, Sterne mit Tiefe (Parallax), leichter Schneefall
//	Sternbilder   eine Plattform = ein Sternbild, Linien als Spannbaum, Name darüber
//	Sterne        Spiele als runde Cover; Fokus leuchtet; Downloads als Ring um den Stern
//	Panel         links: Poster, Titel, Status, Hauptaktion (A) und Optionen (X)
//	Übersicht     LT/RT halten: Kamera zoomt auf die ganze Karte, das Panel gleitet weg
package main

import (
	"image"
	"image/color"
	"math"
	"strings"
	"time"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
)

type skySeg struct{ x1, y1, x2, y2 float32 }

// hash01: deterministische Pseudo-Zufallszahl in [0,1) (für Sterne und Schneeflocken)
func hash01(i, salt uint32) float64 {
	x := i*2654435761 ^ salt*40503
	x ^= x >> 15
	x *= 2246822519
	x ^= x >> 13
	x *= 3266489917
	x ^= x >> 16
	return float64(x%100000) / 100000
}

func wrapf(a, b float64) float64 { return a - math.Floor(a/b)*b }

func clampf(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

func fillDot(gtx layout.Context, x, y, r float64, col color.NRGBA) {
	ri := int(r + 0.5)
	if ri < 1 {
		ri = 1
	}
	cx, cy := int(x), int(y)
	paint.FillShape(gtx.Ops, col, clip.UniformRRect(image.Rect(cx-ri, cy-ri, cx+ri, cy+ri), ri).Op(gtx.Ops))
}

func strokeSegs(gtx layout.Context, segs []skySeg, col color.NRGBA, width float32) {
	if len(segs) == 0 {
		return
	}
	var p clip.Path
	p.Begin(gtx.Ops)
	for _, sg := range segs {
		p.MoveTo(f32.Pt(sg.x1, sg.y1))
		p.LineTo(f32.Pt(sg.x2, sg.y2))
	}
	paint.FillShape(gtx.Ops, col, clip.Stroke{Path: p.End(), Width: width}.Op())
}

// strokeArc: Kreisbogen von Winkel start, Länge sweep (Bogenmaß) als Linienzug
func strokeArc(gtx layout.Context, cx, cy, r, start, sweep float64, col color.NRGBA, width float32) {
	n := int(48 * sweep / (2 * math.Pi))
	if n < 3 {
		n = 3
	}
	var p clip.Path
	p.Begin(gtx.Ops)
	for i := 0; i <= n; i++ {
		a := start + sweep*float64(i)/float64(n)
		pt := f32.Pt(float32(cx+r*math.Cos(a)), float32(cy+r*math.Sin(a)))
		if i == 0 {
			p.MoveTo(pt)
		} else {
			p.LineTo(pt)
		}
	}
	paint.FillShape(gtx.Ops, col, clip.Stroke{Path: p.End(), Width: width}.Op())
}

// labelAt zeichnet einen Text waagerecht zentriert bei (cx, y)
func (ui *UI) labelAt(gtx layout.Context, txt string, col color.NRGBA, size unit.Sp, bold bool, cx, y float64, maxW int) {
	g2 := gtx
	g2.Constraints = layout.Constraints{Max: image.Pt(maxW, 4000)}
	m := op.Record(gtx.Ops)
	d := ui.labelBlock(g2, txt, col, size, bold, 1, text.Middle)
	call := m.Stop()
	st := op.Offset(image.Pt(int(cx)-d.Size.X/2, int(y))).Push(gtx.Ops)
	call.Add(gtx.Ops)
	st.Pop()
}

// ─── Hintergrund ──────────────────────────────────────────────────────────────

func (ui *UI) drawSkyBackground(gtx layout.Context, W, H, u, t, camX, camY float64) {
	size := image.Pt(int(W), int(H))
	cl := clip.Rect{Max: size}.Push(gtx.Ops)
	defer cl.Pop()

	// Horizont-Schimmer unten
	paint.LinearGradientOp{
		Stop1: f32.Pt(0, float32(H*0.25)), Stop2: f32.Pt(0, float32(H)),
		Color1: withAlpha(colAccentLo, 0), Color2: withAlpha(colAccentLo, 95),
	}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)

	// Polarlicht: zwei weiche Bänder, die langsam wandern
	band := func(y0, hgt float64, col color.NRGBA, amax uint8) {
		mid := y0 + hgt/2
		up := clip.Rect{Min: image.Pt(0, int(y0)), Max: image.Pt(size.X, int(mid))}.Push(gtx.Ops)
		paint.LinearGradientOp{Stop1: f32.Pt(0, float32(y0)), Stop2: f32.Pt(0, float32(mid)), Color1: withAlpha(col, 0), Color2: withAlpha(col, amax)}.Add(gtx.Ops)
		paint.PaintOp{}.Add(gtx.Ops)
		up.Pop()
		dn := clip.Rect{Min: image.Pt(0, int(mid)), Max: image.Pt(size.X, int(y0+hgt))}.Push(gtx.Ops)
		paint.LinearGradientOp{Stop1: f32.Pt(0, float32(mid)), Stop2: f32.Pt(0, float32(y0+hgt)), Color1: withAlpha(col, amax), Color2: withAlpha(col, 0)}.Add(gtx.Ops)
		paint.PaintOp{}.Add(gtx.Ops)
		dn.Pop()
	}
	band(H*0.17+math.Sin(t*0.11)*34*u, 230*u, colAccent, 40)
	band(H*0.33+math.Sin(t*0.08+1.7)*28*u, 190*u, colSuccess, 16)

	// Sterne: drei Tiefen, die sich beim Kameraflug unterschiedlich schnell bewegen
	for i := uint32(0); i < 120; i++ {
		depth := 0.3 + 0.7*hash01(i, 3)
		x := wrapf(hash01(i, 1)*W-camX*0.05*depth, W)
		y := wrapf(hash01(i, 2)*H-camY*0.05*depth, H)
		tw := 0.65 + 0.35*math.Sin(t*(0.5+hash01(i, 4)*1.5)+float64(i))
		a := uint8(clampf((60+130*depth)*tw, 0, 255))
		fillDot(gtx, x, y, (0.7+1.1*depth)*u*0.8, withAlpha(colText, a))
	}

	// Schnee
	for i := uint32(0); i < 56; i++ {
		vy := (14 + 34*hash01(i, 6)) * u
		amp := (6 + 14*hash01(i, 7)) * u
		x := wrapf(hash01(i, 5)*W+math.Sin(t*0.6+float64(i))*amp, W)
		y := wrapf(hash01(i, 8)*H+t*vy, H)
		a := uint8(50 + 90*hash01(i, 9))
		fillDot(gtx, x, y, (0.9+1.6*hash01(i, 10))*u*0.7, color.NRGBA{R: 255, G: 255, B: 255, A: a})
	}
}

// ─── Sterne ───────────────────────────────────────────────────────────────────

func (ui *UI) drawStar(gtx layout.Context, g *Game, x, y, r float64, focused, withCovers bool, job *installJob, t float64) {
	acc := platformAccent(g.Platform)
	rr := int(math.Round(r))
	if rr < 2 {
		rr = 2
	}
	cx, cy := int(math.Round(x)), int(math.Round(y))
	rect := image.Rect(cx-rr, cy-rr, cx+rr, cy+rr)
	faded := g.Ghost || g.Status == "nicht-installiert"

	if focused { // pulsierendes Leuchten
		pulse := 0.5 + 0.5*math.Sin(t*3.2)
		for i := 3; i >= 1; i-- {
			grow := int(float64(gtx.Dp(unit.Dp(6))) * float64(i) * (1 + 0.12*pulse))
			gr := image.Rect(rect.Min.X-grow, rect.Min.Y-grow, rect.Max.X+grow, rect.Max.Y+grow)
			paint.FillShape(gtx.Ops, withAlpha(colAccent, uint8(30-7*i)), clip.UniformRRect(gr, rr+grow).Op(gtx.Ops))
		}
	}

	if !withCovers { // herausgezoomt: einfache Punkte
		col := withAlpha(acc, 235)
		if faded {
			col = withAlpha(acc, 110)
		}
		if focused {
			col = colAccentHi
		}
		paint.FillShape(gtx.Ops, col, clip.UniformRRect(rect, rr).Op(gtx.Ops))
	} else {
		cl := clip.UniformRRect(rect, rr).Push(gtx.Ops)
		paint.FillShape(gtx.Ops, colSurface, clip.Rect(rect).Op())
		side := 2 * rr
		if imgOp, ok := coverImage(g); ok {
			is := imgOp.Size()
			sc := float32(side) / float32(is.X)
			if sy := float32(side) / float32(is.Y); sy > sc {
				sc = sy
			}
			sw, sh := int(float32(is.X)*sc), int(float32(is.Y)*sc)
			ox := rect.Min.X + (side-sw)/2
			oy := rect.Min.Y - int(0.4*float64(sh-side)) // Ausschnitt leicht oberhalb der Mitte
			st := op.Offset(image.Pt(ox, oy)).Push(gtx.Ops)
			tr := op.Affine(f32.Affine2D{}.Scale(f32.Pt(0, 0), f32.Pt(sc, sc))).Push(gtx.Ops)
			imgOp.Add(gtx.Ops)
			paint.PaintOp{}.Add(gtx.Ops)
			tr.Pop()
			st.Pop()
		} else { // ohne Cover: Akzentfläche mit Symbol
			paint.FillShape(gtx.Ops, withAlpha(acc, 60), clip.Rect(rect).Op())
			sym := platformSymbol(g.Platform)
			if g.Ghost {
				sym = "+"
			}
			g2 := gtx
			g2.Constraints = layout.Exact(image.Pt(side, side))
			st := op.Offset(rect.Min).Push(gtx.Ops)
			layout.Center.Layout(g2, func(gtx layout.Context) layout.Dimensions {
				return ui.label(gtx, sym, withAlpha(acc, 235), unit.Sp(float32(rr)/gtx.Metric.PxPerDp*0.95), true)
			})
			st.Pop()
		}
		if faded {
			paint.FillShape(gtx.Ops, withAlpha(colBg, 120), clip.Rect(rect).Op())
		}
		cl.Pop()

		// Rand
		ringW, ringCol := gtx.Dp(unit.Dp(2)), withAlpha(acc, 200)
		switch {
		case focused:
			ringW, ringCol = gtx.Dp(unit.Dp(3)), colAccentHi
		case faded:
			ringCol = withAlpha(acc, 110)
		}
		half := ringW / 2
		ring := clip.UniformRRect(image.Rect(rect.Min.X+half, rect.Min.Y+half, rect.Max.X-half, rect.Max.Y-half), rr-half)
		paint.FillShape(gtx.Ops, ringCol, clip.Stroke{Path: ring.Path(gtx.Ops), Width: float32(ringW)}.Op())

		if g.Status == "update-verfugbar" { // Update-Punkt unten rechts
			fillDot(gtx, x+0.72*r, y+0.72*r, 0.26*r, colWarning)
		}
	}

	// Download: Ring um den Stern (Fortschritt, bei unbekanntem Stand ein kreisender Bogen)
	if job != nil {
		rad := r + float64(gtx.Dp(unit.Dp(7)))
		w := float32(gtx.Dp(unit.Dp(3)))
		strokeArc(gtx, x, y, rad, -math.Pi/2, 2*math.Pi, withAlpha(colText, 30), w)
		if job.pct > 0 {
			strokeArc(gtx, x, y, rad, -math.Pi/2, 2*math.Pi*job.pct/100, colAccentHi, w)
		} else {
			strokeArc(gtx, x, y, rad, t*4, math.Pi/2, colAccentHi, w)
		}
	}
}

// coverImage liefert das Cover-Bild des Spiels, falls bereits geladen
func coverImage(g *Game) (paint.ImageOp, bool) {
	imgOp, ok := covers.get(g.coverKey())
	if !ok || imgOp.Size() == (image.Point{}) {
		return paint.ImageOp{}, false
	}
	return imgOp, true
}

// ─── Karte ────────────────────────────────────────────────────────────────────

func (ui *UI) layoutSky(gtx layout.Context) {
	s := ui.state
	size := gtx.Constraints.Max
	W, H := float64(size.X), float64(size.Y)
	u := float64(gtx.Metric.PxPerDp)
	t := gtx.Now.Sub(ui.t0).Seconds()

	// ständige, ruhige Animation (Schnee, Sterne, Polarlicht): etwa 30 Bilder pro Sekunde
	gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(33 * time.Millisecond)})

	if len(s.filtered) == 0 || s.focusIndex >= len(s.filtered) {
		ui.drawSkyBackground(gtx, W, H, u, t, 0, 0)
		layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return ui.layoutEmptyState(gtx, s.activePlatform)
		})
		return
	}

	m := s.ensureSky()
	focus := s.focusIndex

	panelPx := float64(gtx.Dp(unit.Dp(400)))
	topPx := float64(gtx.Dp(unit.Dp(84)))
	botPx := float64(gtx.Dp(unit.Dp(64)))

	// Panel gleitet beim Herauszoomen weg
	target := 1.0
	if s.overview {
		target = 0
	}
	ui.panelVis += (target - ui.panelVis) * (1 - math.Exp(-9*ui.dt))
	vis := ui.panelVis

	mapCX := W/2 + (panelPx/2)*vis
	mapCY := topPx + (H-topPx-botPx)/2

	// Kamera
	fx, fy := m.Nodes[focus].X, m.Nodes[focus].Y
	tx, ty, tz := fx, fy, 1.0
	if s.overview {
		tx, ty = (m.MinX+m.MaxX)/2, (m.MinY+m.MaxY)/2
		tz = skyFitZoom(m, (W-90*u)/u, (H-topPx-botPx-30*u)/u)
	}
	s.cam.step(tx, ty, tz, ui.dt)
	z := s.cam.Z
	toScreen := func(wx, wy float64) (float64, float64) {
		return mapCX + (wx-s.cam.X)*z*u, mapCY + (wy-s.cam.Y)*z*u
	}

	ui.drawSkyBackground(gtx, W, H, u, t, s.cam.X*u, s.cam.Y*u)

	// Sternbild-Linien
	groups := map[int][]skySeg{}
	var hot []skySeg
	for _, e := range m.Edges {
		a, b := m.Nodes[e[0]], m.Nodes[e[1]]
		x1, y1 := toScreen(a.X, a.Y)
		x2, y2 := toScreen(b.X, b.Y)
		if math.Max(x1, x2) < -60 || math.Min(x1, x2) > W+60 || math.Max(y1, y2) < -60 || math.Min(y1, y2) > H+60 {
			continue
		}
		sg := skySeg{float32(x1), float32(y1), float32(x2), float32(y2)}
		if e[0] == focus || e[1] == focus {
			hot = append(hot, sg)
		} else {
			groups[a.Cluster] = append(groups[a.Cluster], sg)
		}
	}
	lineW := float32(1.6 * u)
	for ci, segs := range groups {
		strokeSegs(gtx, segs, withAlpha(platformAccent(m.Clusters[ci].Platform), 64), lineW)
	}
	strokeSegs(gtx, hot, withAlpha(colAccentHi, 215), float32(2.8*u))

	// Sternbild-Namen
	for _, c := range m.Clusters {
		n := 0
		for _, gi := range c.Nodes {
			if !s.filtered[gi].Ghost {
				n++
			}
		}
		name := strings.ToUpper(platformLabel(c.Platform))
		if n > 0 {
			name += "  ·  " + itoa(n)
		}
		x, y := toScreen(c.CX, c.CY-c.R-34)
		if x < -200 || x > W+200 || y < -60 || y > H+60 {
			continue
		}
		fs, a := unit.Sp(12), uint8(190)
		if z < 0.6 {
			fs, a = unit.Sp(18), 230
		}
		ui.labelAt(gtx, name, withAlpha(platformAccent(c.Platform), a), fs, true, x, y, gtx.Dp(unit.Dp(320)))
	}

	// Sterne
	ns := clampf(z*1.2, 0.12, 1)
	rBase := skyNodeR * u * ns
	withCovers := rBase >= 15*u
	margin := rBase*3 + 60
	for i := range m.Nodes {
		if i == focus {
			continue
		}
		x, y := toScreen(m.Nodes[i].X, m.Nodes[i].Y)
		if x < -margin || x > W+margin || y < -margin || y > H+margin {
			continue
		}
		g := &s.filtered[i]
		ui.drawStar(gtx, g, x, y, rBase, false, withCovers, s.installs[gameKey(g)], t)
	}
	fgm := &s.filtered[focus]
	fxs, fys := toScreen(fx, fy)
	fr := rBase * 1.45
	ui.drawStar(gtx, fgm, fxs, fys, fr, true, withCovers, s.installs[gameKey(fgm)], t)

	// Namen: der fokussierte Stern und seine Nachbarn
	if !s.overview {
		for i := range m.Nodes {
			if i == focus {
				continue
			}
			d := math.Hypot(m.Nodes[i].X-fx, m.Nodes[i].Y-fy)
			if d > 190 || z < 0.7 {
				continue
			}
			x, y := toScreen(m.Nodes[i].X, m.Nodes[i].Y)
			ui.labelAt(gtx, s.filtered[i].Title, withAlpha(colText, uint8(170*(1-d/190))), unit.Sp(11), false, x, y+rBase+float64(gtx.Dp(unit.Dp(6))), gtx.Dp(unit.Dp(170)))
		}
		ui.labelAt(gtx, fgm.Title, colText, unit.Sp(16), true, fxs, fys+fr+float64(gtx.Dp(unit.Dp(12))), gtx.Dp(unit.Dp(300)))
	} else {
		ui.labelAt(gtx, "ÜBERSICHT", withAlpha(colAccentHi, 230), unit.Sp(13), true, W/2, topPx*0.36, gtx.Dp(unit.Dp(300)))
	}

	// Detail-Panel
	if vis > 0.02 {
		ui.layoutSkyPanel(gtx, fgm, vis, panelPx, topPx, botPx)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// ─── Detail-Panel ─────────────────────────────────────────────────────────────

func (ui *UI) layoutPoster(gtx layout.Context, g *Game, w, h int) layout.Dimensions {
	rr := gtx.Dp(unit.Dp(14))
	cg := gtx
	cg.Constraints = layout.Exact(image.Pt(w, h))
	cl := clip.UniformRRect(image.Rect(0, 0, w, h), rr).Push(gtx.Ops)
	ui.layoutCover(cg, g)
	if g.Status == "nicht-installiert" {
		paint.FillShape(gtx.Ops, withAlpha(colBg, 70), clip.Rect{Max: image.Pt(w, h)}.Op())
	}
	cl.Pop()
	rw := gtx.Dp(unit.Dp(1))
	ring := clip.UniformRRect(image.Rect(rw/2, rw/2, w-rw/2, h-rw/2), rr-rw/2)
	paint.FillShape(gtx.Ops, withAlpha(colText, 40), clip.Stroke{Path: ring.Path(gtx.Ops), Width: float32(rw)}.Op())
	return layout.Dimensions{Size: image.Pt(w, h)}
}

func (ui *UI) layoutMetaRow(gtx layout.Context, g *Game) layout.Dimensions {
	acc := platformAccent(g.Platform)
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return drawPill(gtx, withAlpha(acc, 45), func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(3), Bottom: unit.Dp(3), Left: unit.Dp(10), Right: unit.Dp(10)}.Layout(gtx,
					func(gtx layout.Context) layout.Dimensions {
						return ui.label(gtx, strings.ToUpper(g.Platform), acc, unit.Sp(10), true)
					})
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if g.Genre == "" {
				return layout.Dimensions{}
			}
			return layout.Inset{Left: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return ui.label(gtx, g.Genre, colDim, unit.Sp(13), false)
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: unit.Dp(14)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				sz := gtx.Dp(unit.Dp(7))
				paint.FillShape(gtx.Ops, g.statusColor(), clip.UniformRRect(image.Rectangle{Max: image.Pt(sz, sz)}, sz/2).Op(gtx.Ops))
				return layout.Dimensions{Size: image.Pt(sz, sz)}
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: unit.Dp(7)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return ui.label(gtx, g.statusLabel(), colDim, unit.Sp(13), false)
			})
		}),
	)
}

// actionPill: "A  Spielen" / "X  Optionen": Taste und Aktion stehen immer zusammen,
// so ist eindeutig, was welche Taste tut
func (ui *UI) actionPill(gtx layout.Context, key, label string, primary bool) layout.Dimensions {
	white := color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	bg, fg, keyBg, keyFg := withAlpha(colText, 22), colDim, withAlpha(colText, 40), colText
	if primary {
		bg, fg, keyBg, keyFg = colAccent, white, color.NRGBA{R: 255, G: 255, B: 255, A: 55}, white
	}
	return drawPill(gtx, bg, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: unit.Dp(8), Bottom: unit.Dp(8), Left: unit.Dp(9), Right: unit.Dp(22)}.Layout(gtx,
			func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return drawPill(gtx, keyBg, func(gtx layout.Context) layout.Dimensions {
							return layout.Inset{Top: unit.Dp(4), Bottom: unit.Dp(4), Left: unit.Dp(11), Right: unit.Dp(11)}.Layout(gtx,
								func(gtx layout.Context) layout.Dimensions {
									return ui.label(gtx, key, keyFg, unit.Sp(14), true)
								})
						})
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Left: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return ui.label(gtx, label, fg, unit.Sp(15), primary)
						})
					}),
				)
			})
	})
}

func (ui *UI) layoutSkyPanel(gtx layout.Context, g *Game, vis, panelPx, topPx, botPx float64) {
	s := ui.state
	size := gtx.Constraints.Max

	// Abdunkelung hinter dem Panel: Sterne darunter stören nicht
	solid := int(panelPx * 0.85)
	fade := int(panelPx * 1.35)
	a := uint8(247 * vis)
	cl := clip.Rect{Max: image.Pt(solid, size.Y)}.Push(gtx.Ops)
	paint.ColorOp{Color: withAlpha(colBg, a)}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	cl.Pop()
	cl = clip.Rect{Min: image.Pt(solid, 0), Max: image.Pt(fade, size.Y)}.Push(gtx.Ops)
	paint.LinearGradientOp{Stop1: f32.Pt(float32(solid), 0), Stop2: f32.Pt(float32(fade), 0), Color1: withAlpha(colBg, a), Color2: withAlpha(colBg, 0)}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	cl.Pop()

	availH := size.Y - int(topPx) - int(botPx)
	panelW := gtx.Dp(unit.Dp(340))
	posterH := imin(gtx.Dp(unit.Dp(345)), availH*44/100)
	posterW := posterH * 2 / 3

	slide := int(-(1 - vis) * float64(panelW+gtx.Dp(unit.Dp(120))))
	st := op.Offset(image.Pt(slide, int(topPx))).Push(gtx.Ops)
	defer st.Pop()

	pg := gtx
	pg.Constraints = layout.Exact(image.Pt(panelW+gtx.Dp(unit.Dp(56)), availH))
	acts := s.actionsFor(g)
	job := s.installs[gameKey(g)]

	layout.W.Layout(pg, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Left: unit.Dp(56)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Max.X = panelW
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return ui.layoutPoster(gtx, g, posterW, posterH)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: unit.Dp(16)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return ui.layoutMetaRow(gtx, g)
					})
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return ui.labelBlock(gtx, g.Title, colText, unit.Sp(30), true, 3, text.Start)
					})
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if job == nil {
						return layout.Dimensions{}
					}
					return layout.Inset{Top: unit.Dp(16)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return ui.layoutInstallProgress(gtx, job)
					})
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: unit.Dp(20)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						children := []layout.FlexChild{
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return ui.actionPill(gtx, "A", acts[0].label, true)
							}),
						}
						if len(acts) > 1 {
							children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return layout.Inset{Left: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
									return ui.actionPill(gtx, "X", "Optionen", false)
								})
							}))
						}
						return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx, children...)
					})
				}),
			)
		})
	})
}
