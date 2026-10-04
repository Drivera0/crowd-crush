package crowdsim

import (
	"math"
	"math/rand"
)

// Calibration against Weidmann's fundamental diagram (U. Weidmann,
// "Transporttechnik der Fussgänger", ETH Zürich, 1993): the mean walking
// speed of a crowd at density ρ,
//
//	v(ρ) = v0 · (1 − exp(−γ · (1/ρ − 1/ρmax))),  v0 = 1.34 m/s, γ = 1.913 m⁻², ρmax = 5.4 /m²
//
// is the standard benchmark for pedestrian models (Vadere and JuPedSim
// test against it). The test is a straight corridor with periodic ends:
// everyone walks along it, and the mean speed is measured at a fixed
// global density.

// Weidmann is the fundamental diagram's speed (m/s) at density rho (/m²).
func Weidmann(rho float64) float64 {
	const v0, gamma, rhoMax = 1.34, 1.913, 5.4
	if rho >= rhoMax {
		return 0
	}
	return v0 * (1 - math.Exp(-gamma*(1/rho-1/rhoMax)))
}

const actCorridor = "corridor"

// NewCorridor fills a periodic corridor (length × width m, walls along the
// long sides) with people at the given density, all walking toward +x.
// Nobody carries a phone.
func NewCorridor(length, width, density float64, seed int64) *World {
	w := &World{T: 0, Participation: 1e-9, Action: actCorridor, rng: rand.New(rand.NewSource(seed)),
		periodic: length}
	g := &Geometry{W: length, H: width, BarrierY: -1}
	g.Walls = []Seg{{-length, 0, 2 * length, 0}, {-length, width, 2 * length, width}}
	w.G = g
	w.solid = g.solid()
	w.truth.Init()
	n := int(math.Round(density * length * width))
	// Place on a jittered lattice so even the densest case fits.
	cols := int(math.Ceil(math.Sqrt(float64(n) * length / width)))
	rows := (n + cols - 1) / cols
	for i := 0; i < n; i++ {
		c, r := i%cols, i/cols
		x := (float64(c) + 0.5 + 0.1*(w.rng.Float64()-0.5)) * length / float64(cols)
		y := (float64(r) + 0.5 + 0.1*(w.rng.Float64()-0.5)) * width / float64(rows)
		a := w.newAgent(x, y)
		a.phone = nil
		// Very dense: shrink a little so the start isn't badly overlapped;
		// the bodies relax within a second.
		w.agents = append(w.agents, a)
	}
	return w
}

// MeanSpeedX is the crowd's mean velocity along +x.
func (w *World) MeanSpeedX() float64 {
	if len(w.agents) == 0 {
		return 0
	}
	s := 0.0
	for _, a := range w.agents {
		s += a.VX
	}
	return s / float64(len(w.agents))
}

// NewCounterflow is NewCorridor with a share of the people (chosen at
// random) walking the other way, steering as they do in the venue (the
// wider headings and passing on the right). share 0.5 is bidirectional
// flow; share 0 is the one-way reference with the same steering.
func NewCounterflow(length, width, density, share float64, seed int64) *World {
	w := NewCorridor(length, width, density, seed)
	w.counter = true
	for _, a := range w.agents {
		if w.rng.Float64() < share {
			a.cdir = -1
		}
		a.hd = math.Pi * (1 - a.cdir) / 2
		a.face, a.prevFace = a.hd, a.hd
	}
	return w
}

// MeanSpeedOwn is the crowd's mean velocity along each person's own
// walking direction (corridor and counterflow).
func (w *World) MeanSpeedOwn() float64 {
	if len(w.agents) == 0 {
		return 0
	}
	s := 0.0
	for _, a := range w.agents {
		s += a.VX * a.cdir
	}
	return s / float64(len(w.agents))
}

// LaneOrder is the lane order parameter of a counterflow (Rex & Löwen
// 2007; used for pedestrians by e.g. Feliciani & Nishinari 2016): the
// corridor is cut into strips one body wide along the walking direction;
// for each person, φ_i = ((n₊ − n₋)/(n₊ + n₋))² over the people in their
// strip, and LaneOrder is the mean of φ_i. Fully separated lanes give 1;
// a random mix gives about 1/(people per strip).
func (w *World) LaneOrder(strip float64) float64 {
	ns := int(math.Ceil(w.G.H / strip))
	plus, minus := make([]int, ns), make([]int, ns)
	idx := func(a *Agent) int { return max(0, min(ns-1, int(a.Y/strip))) }
	for _, a := range w.agents {
		if a.cdir > 0 {
			plus[idx(a)]++
		} else {
			minus[idx(a)]++
		}
	}
	phi := 0.0
	for _, a := range w.agents {
		i := idx(a)
		p, m := float64(plus[i]), float64(minus[i])
		phi += ((p - m) / (p + m)) * ((p - m) / (p + m))
	}
	return phi / float64(len(w.agents))
}

// Bottleneck room (m): people wait in front of a door in the middle of
// the right-hand wall.
const (
	bottleW = 10.0
	bottleH = 8.0
)

// NewBottleneck builds a 10 × 8 m room with a door `door` m wide in the
// middle of its right wall and n people waiting in the 6 m in front of it,
// all leaving through it (the disperse behaviour, as one-person groups).
// Nobody carries a phone. Used to compare the flow through the door with
// bottleneck experiments (Kretz et al. 2006; Seyfried et al. 2009).
func NewBottleneck(door float64, n int, seed int64) *World {
	w := &World{Participation: 1e-9, Action: ActDisperse, rng: rand.New(rand.NewSource(seed))}
	W, H := bottleW, bottleH
	y0, y1 := H/2-door/2, H/2+door/2
	g := &Geometry{W: W, H: H, BarrierY: -1, custom: true}
	e := &Exit{ID: "door", Name: "Door", X0: W, Y0: y0, X1: W, Y1: y1, Open: true, nx: 1}
	g.Exits = []*Exit{e}
	g.Walls = []Seg{{0, 0, W, 0}, {0, H, W, H}, {0, 0, 0, H}, {W, 0, W, y0}, {W, y1, W, H}}
	w.G = g
	w.solid = g.solid()
	w.truth.Init()
	for tries := 0; len(w.agents) < n && tries < n*400; tries++ {
		x := W - 6 + 5.6*w.rng.Float64()
		y := 0.4 + (H-0.8)*w.rng.Float64()
		r := 0.20 + 0.06*w.rng.Float64()
		if !w.free(x, y, r) {
			continue
		}
		a := w.newAgent(x, y)
		a.R, a.phone = r, nil
		a.hd, a.face = 0, 0
		w.agents = append(w.agents, a)
		gr := w.addGroup([]*Agent{a})
		gr.exit = e
		w.setOff(gr, pEvac, 0, 0)
	}
	return w
}
