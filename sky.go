// sky.go — "Polarnacht": die Sternenkarte des SnowFox-Launchers (reine Logik, ohne Gio)
//
// Jedes Spiel ist ein Stern. Jede Plattform bildet ein Sternbild (Cluster): Steam, GOG, …
// Innerhalb eines Sternbilds liegen die zuletzt gespielten Spiele in der Mitte, der Rest
// spiralt nach außen (Vogel-Spirale, gleichmäßige Verteilung). Linien verbinden die Sterne
// eines Sternbilds als minimaler Spannbaum. Navigiert wird räumlich: der Stick springt zum
// nächsten Stern in die gedrückte Richtung. Die Kamera gleitet dem Fokus hinterher und kann
// auf die ganze Karte herauszoomen.
package main

import (
	"hash/fnv"
	"math"
	"sort"
	"strings"
)

const (
	skySpacing  = 78.0  // dp, Vogel-Konstante: Abstand der Sterne
	skyNodeR    = 28.0  // dp, Radius eines Sterns bei Zoom 1
	skyGap      = 300.0 // dp, Abstand zwischen den Sternbildern
	goldenAngle = 2.399963229728653
)

type skyItem struct {
	Key, Platform, Title string
	Recent               int64 // Unix-Zeit des letzten Starts (0 = nie)
}

type skyNode struct {
	X, Y    float64
	Cluster int
}

type skyCluster struct {
	Platform  string
	CX, CY, R float64
	Nodes     []int // Indizes in skyMap.Nodes, zuletzt gespielt zuerst
}

type skyMap struct {
	Nodes                  []skyNode
	Clusters               []skyCluster
	Edges                  [][2]int
	MinX, MinY, MaxX, MaxY float64
	Sig                    uint64
}

func hashStr(s string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(s))
	return h.Sum32()
}

func skySignature(items []skyItem) uint64 {
	h := fnv.New64a()
	var b [8]byte
	for _, it := range items {
		h.Write([]byte(it.Key))
		h.Write([]byte{0})
		h.Write([]byte(it.Platform))
		h.Write([]byte{1})
		for i := 0; i < 8; i++ {
			b[i] = byte(uint64(it.Recent) >> (8 * i))
		}
		h.Write(b[:])
	}
	return h.Sum64()
}

func buildSky(items []skyItem) *skyMap {
	m := &skyMap{Nodes: make([]skyNode, len(items)), Sig: skySignature(items)}
	if len(items) == 0 {
		return m
	}

	// 1) nach Plattform gruppieren (Reihenfolge des ersten Auftretens)
	var order []string
	groups := map[string][]int{}
	for i, it := range items {
		if _, ok := groups[it.Platform]; !ok {
			order = append(order, it.Platform)
		}
		groups[it.Platform] = append(groups[it.Platform], i)
	}

	// 2) je Sternbild: sortieren und lokal als Vogel-Spirale anordnen
	for ci, p := range order {
		idx := groups[p]
		sort.SliceStable(idx, func(a, b int) bool {
			ia, ib := items[idx[a]], items[idx[b]]
			if ia.Recent != ib.Recent {
				return ia.Recent > ib.Recent
			}
			return strings.ToLower(ia.Title) < strings.ToLower(ib.Title)
		})
		rot := float64(hashStr(p)%628) / 100
		for k, gi := range idx {
			r := skySpacing * math.Sqrt(float64(k))
			a := rot + float64(k)*goldenAngle
			m.Nodes[gi] = skyNode{X: r * math.Cos(a), Y: r * math.Sin(a), Cluster: ci}
		}
		extent := 0.0
		if len(idx) > 1 {
			extent = skySpacing * math.Sqrt(float64(len(idx)-1))
		}
		m.Clusters = append(m.Clusters, skyCluster{Platform: p, R: extent + skyNodeR*1.8, Nodes: idx})
	}

	// 3) größtes Sternbild in die Mitte, die anderen rundherum
	big := 0
	for i, c := range m.Clusters {
		if len(c.Nodes) > len(m.Clusters[big].Nodes) {
			big = i
		}
	}
	var sats []int
	for i := range m.Clusters {
		if i != big {
			sats = append(sats, i)
		}
	}
	for j, ci := range sats {
		ang := 2 * math.Pi * float64(j) / float64(len(sats))
		d := m.Clusters[big].R + m.Clusters[ci].R + skyGap
		m.Clusters[ci].CX = d * math.Cos(ang)
		m.Clusters[ci].CY = d * math.Sin(ang) * 0.8
	}
	for _, c := range m.Clusters {
		for _, gi := range c.Nodes {
			m.Nodes[gi].X += c.CX
			m.Nodes[gi].Y += c.CY
		}
	}

	// 4) Linien: minimaler Spannbaum je Sternbild (Prim)
	for _, c := range m.Clusters {
		n := len(c.Nodes)
		if n < 2 {
			continue
		}
		in := make([]bool, n)
		dist := make([]float64, n)
		from := make([]int, n)
		for i := range dist {
			dist[i], from[i] = math.Inf(1), -1
		}
		dist[0] = 0
		for step := 0; step < n; step++ {
			u := -1
			for i := 0; i < n; i++ {
				if !in[i] && (u < 0 || dist[i] < dist[u]) {
					u = i
				}
			}
			in[u] = true
			if from[u] >= 0 {
				m.Edges = append(m.Edges, [2]int{c.Nodes[from[u]], c.Nodes[u]})
			}
			for v := 0; v < n; v++ {
				if in[v] {
					continue
				}
				a, b := m.Nodes[c.Nodes[u]], m.Nodes[c.Nodes[v]]
				if d := math.Hypot(a.X-b.X, a.Y-b.Y); d < dist[v] {
					dist[v], from[v] = d, u
				}
			}
		}
	}

	// 5) Grenzen
	m.MinX, m.MinY = math.Inf(1), math.Inf(1)
	m.MaxX, m.MaxY = math.Inf(-1), math.Inf(-1)
	for _, n := range m.Nodes {
		m.MinX, m.MaxX = math.Min(m.MinX, n.X-skyNodeR), math.Max(m.MaxX, n.X+skyNodeR)
		m.MinY, m.MaxY = math.Min(m.MinY, n.Y-skyNodeR), math.Max(m.MaxY, n.Y+skyNodeR)
	}
	return m
}

// neighbor: nächster Stern von "from" aus in Richtung (dx, dy); -1 wenn keiner in dieser Richtung liegt.
// Erst ein Kegel von etwa ±55°, danach (falls leer) jeder Stern in der Halbebene.
func (m *skyMap) neighbor(from int, dx, dy float64) int {
	if from < 0 || from >= len(m.Nodes) {
		return -1
	}
	best, bestScore := -1, math.Inf(1)
	for _, maxPerp := range []float64{1.4, 1e9} {
		for j, n := range m.Nodes {
			if j == from {
				continue
			}
			vx, vy := n.X-m.Nodes[from].X, n.Y-m.Nodes[from].Y
			along := vx*dx + vy*dy
			if along <= 1e-6 {
				continue
			}
			perp := math.Abs(vx*dy - vy*dx)
			if perp > along*maxPerp {
				continue
			}
			if score := along + 2*perp; score < bestScore {
				best, bestScore = j, score
			}
		}
		if best >= 0 {
			return best
		}
	}
	return -1
}

// clusterOf liefert den Index des Sternbilds einer Plattform oder -1
func (m *skyMap) clusterOf(platform string) int {
	for i, c := range m.Clusters {
		if c.Platform == platform {
			return i
		}
	}
	return -1
}

// ─── Kamera ───────────────────────────────────────────────────────────────────

type skyCam struct {
	X, Y, Z float64
	ready   bool
}

// step: weiches Nachziehen, unabhängig von der Framerate
func (c *skyCam) step(tx, ty, tz, dt float64) {
	if !c.ready {
		c.X, c.Y, c.Z, c.ready = tx, ty, tz, true
		return
	}
	k := 1 - math.Exp(-7*dt)
	kz := 1 - math.Exp(-6*dt)
	c.X += (tx - c.X) * k
	c.Y += (ty - c.Y) * k
	c.Z += (tz - c.Z) * kz
}

func (c *skyCam) settled(tx, ty, tz float64) bool {
	return math.Abs(tx-c.X) < 0.3 && math.Abs(ty-c.Y) < 0.3 && math.Abs(tz-c.Z) < 0.003
}

// skyFitZoom: Zoom, bei dem die ganze Karte in einen Bereich (w × h in dp) passt
func skyFitZoom(m *skyMap, w, h float64) float64 {
	bw, bh := m.MaxX-m.MinX, m.MaxY-m.MinY
	if bw <= 0 || bh <= 0 || w <= 0 || h <= 0 {
		return 1
	}
	z := math.Min(w/(bw*1.12), h/(bh*1.12))
	return math.Max(0.1, math.Min(1, z))
}
