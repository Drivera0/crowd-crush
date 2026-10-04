package crowdsim

import (
	"math"

	"github.com/Drivera0/crowd-crush/server/internal/crowd"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// Truth is what the simulation knows and Pulse has to guess.
//
//   - MaxDensity: the highest local density of any person: people whose
//     centres are within 1 m of them (themselves included) ÷ π m².
//     WindowDensity, the most people in any 3 × 3 m square (sliding in
//     0.5 m steps) ÷ 9 m², is kept too, but it can't pass ~5.3/m² here:
//     discs of radius 0.20–0.26 m pack at most ~5.5/m² (real bodies are
//     narrower than the circle around their shoulders). The 1 m count reads
//     ~6/m² when someone is wedged in, touching all round.
//   - MaxPressure: the highest per-person pressure, N/m.
//   - Crushing: people whose pressure is ≥ CrushPressure (1600 N/m) or whose
//     local density is > CrushDensity (6/m²).
//   - Dangerous: ≥ 3 people at ≥ 1600 N/m, or ≥ 5 people above 6/m², held
//     for DangerHoldSec (1 s). DangerAt is when that stretch began (the
//     earlier moment, so the lead time is never flattered by the hold).
type Truth struct {
	MaxDensity    float64
	WindowDensity float64
	MaxPressure   float64
	Crushing      int
	Dangerous     bool
	DangerAt      float64 // s since start; < 0 = not yet
	since         float64 // start of the current dangerous stretch; < 0 = none
	counts        []int
	prefix        []int
}

// Init resets the truth.
func (t *Truth) Init() { t.DangerAt, t.since = -1, -1 }

// Truth is the latest ground truth.
func (w *World) Truth() Truth { return w.truth }

// measure computes per-person density and the ground truth for this tick.
func (w *World) measure() {
	tr := &w.truth
	w.grid.build(w.agents)
	tr.MaxPressure, tr.MaxDensity, tr.Crushing = 0, 0, 0
	highP, highD := 0, 0
	for i, a := range w.agents {
		n := 1
		w.grid.near(w.agents, i, densityRadius, func(*Agent) { n++ })
		a.Density = float64(n) / (math.Pi * densityRadius * densityRadius)
		tr.MaxPressure = math.Max(tr.MaxPressure, a.Pressure)
		tr.MaxDensity = math.Max(tr.MaxDensity, a.Density)
		if a.Density > CrushDensity {
			highD++
		}
		if a.Pressure >= CrushPressure {
			highP++
		}
		if a.Pressure >= CrushPressure || a.Density > CrushDensity {
			tr.Crushing++
		}
	}
	tr.WindowDensity = w.windowDensity()
	tr.update(w.T, highP >= dangerPressureCount || highD >= dangerDensityCount)
}

// update tracks how long the crowd has been dangerous; DangerAt is the
// start of the first stretch that lasted DangerHoldSec.
func (tr *Truth) update(t float64, danger bool) {
	switch {
	case !danger:
		tr.since = -1
	case tr.since < 0:
		tr.since = t
	}
	tr.Dangerous = danger && t-tr.since >= DangerHoldSec-1e-9
	if tr.Dangerous && tr.DangerAt < 0 {
		tr.DangerAt = tr.since
	}
}

// windowDensity is the max people per m² over 3 × 3 m squares.
func (w *World) windowDensity() float64 {
	tr := &w.truth
	nx := int(math.Ceil(w.G.W/windowCell)) + 1
	ny := int(math.Ceil(w.G.H/windowCell)) + 1
	if len(tr.counts) != nx*ny {
		tr.counts = make([]int, nx*ny)
		tr.prefix = make([]int, (nx+1)*(ny+1))
	}
	for i := range tr.counts {
		tr.counts[i] = 0
	}
	for _, a := range w.agents {
		cx := int(math.Floor(a.X / windowCell))
		cy := int(math.Floor(a.Y / windowCell))
		if cx < 0 || cy < 0 || cx >= nx || cy >= ny {
			continue
		}
		tr.counts[cy*nx+cx]++
	}
	P := tr.prefix
	for y := 0; y < ny; y++ {
		for x := 0; x < nx; x++ {
			P[(y+1)*(nx+1)+x+1] = tr.counts[y*nx+x] + P[y*(nx+1)+x+1] + P[(y+1)*(nx+1)+x] - P[y*(nx+1)+x]
		}
	}
	k := int(math.Round(windowM / windowCell))
	best := 0
	for y := 0; y+k <= ny; y++ {
		for x := 0; x+k <= nx; x++ {
			s := P[(y+k)*(nx+1)+x+k] - P[y*(nx+1)+x+k] - P[(y+k)*(nx+1)+x] + P[y*(nx+1)+x]
			best = max(best, s)
		}
	}
	return float64(best) / (windowM * windowM)
}

// MaxCatchUp is the most simulated time AdvanceTo runs in one call; if the
// server fell further behind (a pause, a slow machine), the sim skips the
// gap rather than racing to catch up.
const MaxCatchUp = 1.0

// AdvanceTo steps the world until it reaches server time nowMs.
func (w *World) AdvanceTo(nowMs int64) {
	target := float64(nowMs-w.StartMs) / 1000
	if target-w.T > MaxCatchUp {
		w.StartMs += int64((target - w.T - MaxCatchUp) * 1000)
		target = w.T + MaxCatchUp
	}
	for w.T+Dt/2 <= target {
		w.Step()
	}
}

// Bodies is everyone as [x, y, pressure, hasPhone, density] for the
// dashboard: x, y to the centimetre, pressure in whole N/m, density =
// PackedDensity to one decimal.
func (w *World) Bodies() [][5]float64 {
	out := make([][5]float64, len(w.agents))
	for i, a := range w.agents {
		ph := 0.0
		if a.phone != nil {
			ph = 1
		}
		out[i] = [5]float64{math.Round(a.X*100) / 100, math.Round(a.Y*100) / 100, math.Round(a.Pressure), ph, round1(w.PackedDensity(a))}
	}
	return out
}

// PackedDensity is how tightly person a is packed in (people/m²): the
// people within 1 m ÷ the part of that disc people can stand on (inside the
// venue, off the stage; crowd.OpenFraction). Agent.Density divides by the
// whole disc and so reads about half for someone against the barrier, where
// the crush is worst; the ground truth keeps using Agent.Density (a
// conservative count), this is for showing who is packed in.
func (w *World) PackedDensity(a *Agent) float64 {
	g := w.G
	open := func(x, y float64) bool {
		return x >= 0 && y >= 0 && x <= g.W && y <= g.H && !g.inStage(x, y)
	}
	return a.Density / crowd.OpenFraction(a.X, a.Y, densityRadius, open)
}

// Status describes the world for GET /api/sim (the app adds the alert time).
func (w *World) Status() protocol.SimStatus {
	s := protocol.SimStatus{Running: true, T: round1(w.T), People: len(w.agents), Phones: w.Phones(),
		Participation: w.Participation, Action: w.Action}
	s.Exits, s.Walls = GeometryJSON(w.G)
	tr := w.truth
	s.Truth = &protocol.SimTruth{MaxDensity: round2(tr.MaxDensity), MaxPressure: math.Round(tr.MaxPressure), Crushing: tr.Crushing}
	if tr.DangerAt >= 0 {
		v := round1(tr.DangerAt)
		s.Truth.DangerAt = &v
	}
	return s
}

// GeometryJSON is the venue's exits and walls for the dashboard.
func GeometryJSON(g *Geometry) ([]protocol.SimExit, [][4]float64) {
	exits := make([]protocol.SimExit, len(g.Exits))
	for i, e := range g.Exits {
		exits[i] = protocol.SimExit{ID: e.ID, Name: e.Name, X0: round2(e.X0), Y0: round2(e.Y0), X1: round2(e.X1), Y1: round2(e.Y1), Open: e.Open}
	}
	walls := make([][4]float64, len(g.Walls))
	for i, s := range g.Walls {
		walls[i] = [4]float64{round2(s.X0), round2(s.Y0), round2(s.X1), round2(s.Y1)}
	}
	return exits, walls
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round2(v float64) float64 { return math.Round(v*100) / 100 }
