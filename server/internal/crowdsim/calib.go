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
