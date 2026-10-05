// gog_ui.go — Oberfläche für Aktionen, Installationsfortschritt und Deinstallations-Dialog
package main

import (
	"fmt"
	"image"
	"image/color"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
)

// layoutInstallProgress: Balken (bekannter Prozentwert) oder wandernder Balken (unbekannt)
func (ui *UI) layoutInstallProgress(gtx layout.Context, job *installJob) layout.Dimensions {
	w := gtx.Dp(unit.Dp(380))
	h := gtx.Dp(unit.Dp(6))
	known := job.pct > 0

	label := "Wird heruntergeladen…"
	if known {
		label = fmt.Sprintf("Installation: %.1f %%", job.pct)
		if job.line != "" {
			label += "  ·  " + job.line
		}
	}

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			paint.FillShape(gtx.Ops, withAlpha(colText, 26), clip.UniformRRect(image.Rect(0, 0, w, h), h/2).Op(gtx.Ops))
			if known {
				fw := imax(h, imin(w, int(float64(w)*job.pct/100)))
				paint.FillShape(gtx.Ops, colAccent, clip.UniformRRect(image.Rect(0, 0, fw, h), h/2).Op(gtx.Ops))
			} else {
				const period = int64(1500 * time.Millisecond)
				t := float64(gtx.Now.UnixNano()%period) / float64(period)
				seg := w / 4
				x := int(t*float64(w+seg)) - seg
				x0, x1 := imax(0, x), imin(w, x+seg)
				if x1 > x0 {
					paint.FillShape(gtx.Ops, colAccent, clip.UniformRRect(image.Rect(x0, 0, x1, h), h/2).Op(gtx.Ops))
				}
				gtx.Execute(op.InvalidateCmd{})
			}
			return layout.Dimensions{Size: image.Pt(w, h)}
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Max.X = w
				return ui.labelBlock(gtx, label, colDim, unit.Sp(12), false, 1, text.Start)
			})
		}),
	)
}

func (ui *UI) layoutUninstallOverlay(gtx layout.Context) layout.Dimensions {
	s := ui.state
	g := s.currentGame()
	if g == nil {
		return layout.Dimensions{}
	}
	return ui.modal(gtx, unit.Dp(420), func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return ui.labelBlock(gtx, g.Title, colText, unit.Sp(20), true, 2, text.Start)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return ui.labelBlock(gtx, "Die Spieldateien werden von der Festplatte gelöscht. Spielstände im Wine-Prefix bleiben erhalten.", colDim, unit.Sp(12), false, 3, text.Start)
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(20), Bottom: unit.Dp(20)}.Layout(gtx, ui.divider)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return ui.optionButton(gtx, "Deinstallieren", "", colError, s.overlaySelection == 0)
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

// layoutOptionsOverlay: alle Aktionen des fokussierten Sterns als Liste (X)
func (ui *UI) layoutOptionsOverlay(gtx layout.Context) layout.Dimensions {
	s := ui.state
	g := s.currentGame()
	if g == nil {
		return layout.Dimensions{}
	}
	acts := s.actionsFor(g)
	sel := imin(s.overlaySelection, len(acts)-1)
	return ui.modal(gtx, unit.Dp(420), func(gtx layout.Context) layout.Dimensions {
		children := []layout.FlexChild{
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return ui.labelBlock(gtx, g.Title, colText, unit.Sp(20), true, 2, text.Start)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(20), Bottom: unit.Dp(20)}.Layout(gtx, ui.divider)
			}),
		}
		for i, a := range acts {
			i, a := i, a
			accent := colAccentHi
			if a.kind == "uninstall" || a.kind == "cancel" || a.kind == "gog-logout" {
				accent = colError
			}
			children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				pad := unit.Dp(0)
				if i > 0 {
					pad = unit.Dp(8)
				}
				return layout.Inset{Top: pad}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return ui.optionButton(gtx, a.label, "", accent, i == sel)
				})
			}))
		}
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return ui.hintLine(gtx, "A  Auswählen      B  Schließen")
		}))
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	})
}

// drawQR zeichnet den QR-Code dunkel auf weiß (so lesen ihn alle Handys zuverlässig)
func drawQR(gtx layout.Context, bm [][]bool, size int) layout.Dimensions {
	white := color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	paint.FillShape(gtx.Ops, white, clip.UniformRRect(image.Rect(0, 0, size, size), size/20).Op(gtx.Ops))
	if n := len(bm); n > 0 {
		cell := size / n
		off := (size - cell*n) / 2
		for y, row := range bm {
			for x := 0; x < len(row); {
				if !row[x] {
					x++
					continue
				}
				x0 := x
				for x < len(row) && row[x] { // Reihen zusammenfassen: weniger Zeichenbefehle
					x++
				}
				r := image.Rect(off+x0*cell, off+y*cell, off+x*cell, off+(y+1)*cell)
				paint.FillShape(gtx.Ops, colBg, clip.Rect(r).Op())
			}
		}
	}
	return layout.Dimensions{Size: image.Pt(size, size)}
}

func (ui *UI) loginStep(gtx layout.Context, n, txt string) layout.Dimensions {
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return ui.symbolBadge(gtx, n, colAccentHi, unit.Dp(26), unit.Sp(12))
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return ui.labelBlock(gtx, txt, colText, unit.Sp(13), false, 2, text.Start)
			})
		}),
	)
}

func (ui *UI) layoutGogLoginOverlay(gtx layout.Context) layout.Dimensions {
	s := ui.state
	l := s.gogLogin
	return ui.modal(gtx, unit.Dp(780), func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				var bm [][]bool
				if l != nil {
					bm = l.QR
				}
				return drawQR(gtx, bm, gtx.Dp(unit.Dp(250)))
			}),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: unit.Dp(32)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return ui.label(gtx, "GOG verbinden", colText, unit.Sp(22), true)
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layout.Inset{Top: unit.Dp(18)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								return ui.loginStep(gtx, "1", "Mit dem Handy scannen (gleiches WLAN)")
							})
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layout.Inset{Top: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								return ui.loginStep(gtx, "2", "Bei GOG anmelden")
							})
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layout.Inset{Top: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								return ui.loginStep(gtx, "3", "Adresse der leeren Seite kopieren und auf der Seite einfügen")
							})
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							if l == nil {
								return layout.Dimensions{}
							}
							return layout.Inset{Top: unit.Dp(16)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								return ui.labelBlock(gtx, l.URL, colDim, unit.Sp(11), false, 2, text.Start)
							})
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							if s.gogLoginStatus == "" {
								return layout.Dimensions{}
							}
							return layout.Inset{Top: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								return ui.labelBlock(gtx, s.gogLoginStatus, colWarning, unit.Sp(12), true, 3, text.Start)
							})
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return ui.hintLine(gtx, "Strg+V  Adresse aus Zwischenablage      B  Abbrechen")
						}),
					)
				})
			}),
		)
	})
}
