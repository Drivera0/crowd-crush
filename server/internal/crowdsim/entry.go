package crowdsim

import (
	"errors"
	"math"
	"sort"
)

// Entry: everyone comes in at one spot (Config.Entry).
//
// Pulse's join flow is one shared QR code shown at a known place on the
// venue map. A phone that scans it is standing near that place, and from
// then on its owner walks wherever they like. This models it: the venue
// starts empty; the same crowd New would have placed (same people, same
// groups, same standing spots) instead arrives at (X, Y) over the first
// Over seconds, group by group in random order, appears within Spread
// metres of the spot when there is room, and walks to its spot in
// formation like any arriving group (behaviour.go, pArrive). A phone says
// hello on the tick its owner appears.
type Entry struct {
	X, Y float64
	// Over: arrivals are spread uniformly over this many seconds (0 = 60).
	Over float64
	// Spread: how far from the spot people appear (m; 0 = 1.5).
	Spread float64
}

// queueEntry takes the placed crowd out of the venue and queues it at the
// entry. Called once, after the initial placement has settled.
func (w *World) queueEntry(e Entry) error {
	if !(e.X >= 0 && e.Y >= 0 && e.X <= w.G.W && e.Y <= w.G.H) {
		return errors.New("the entry spot must be inside the venue")
	}
	if e.Over <= 0 {
		e.Over = 60
	}
	if e.Spread <= 0 {
		e.Spread = 1.5
	}
	if !w.clear(e.X, e.Y, 0.3) {
		return errors.New("the entry spot is inside a wall or the stage")
	}
	w.entry = &e
	for _, a := range w.agents {
		a.homeX, a.homeY, a.spotX, a.spotY = a.X, a.Y, a.X, a.Y
		a.VX, a.VY, a.AX, a.AY = 0, 0, 0, 0
	}
	w.pending = w.groups
	w.pendingAt = make([]float64, len(w.pending))
	for i := range w.pendingAt {
		w.pendingAt[i] = e.Over * w.rng.Float64()
	}
	sort.Sort(byArrival{w})
	w.agents, w.groups = nil, nil
	return nil
}

type byArrival struct{ w *World }

func (b byArrival) Len() int           { return len(b.w.pending) }
func (b byArrival) Less(i, j int) bool { return b.w.pendingAt[i] < b.w.pendingAt[j] }
func (b byArrival) Swap(i, j int) {
	b.w.pending[i], b.w.pending[j] = b.w.pending[j], b.w.pending[i]
	b.w.pendingAt[i], b.w.pendingAt[j] = b.w.pendingAt[j], b.w.pendingAt[i]
}

// Waiting is how many people have yet to come in through the entry.
func (w *World) Waiting() int {
	n := 0
	for _, g := range w.pending {
		n += len(g.members)
	}
	return n
}

// enter lets the next groups in, in order, while there is room at the spot.
func (w *World) enter() {
	for len(w.pending) > 0 && w.pendingAt[0] <= w.T {
		g := w.pending[0]
		e := w.entry
		var at [][2]float64
		for _, a := range g.members {
			ok := false
			for k := 0; k < 30 && !ok; k++ {
				ang := 2 * math.Pi * w.rng.Float64()
				d := e.Spread * math.Sqrt(w.rng.Float64())
				x, y := e.X+d*math.Cos(ang), e.Y+d*math.Sin(ang)
				if !w.free(x, y, a.R) {
					continue
				}
				ok = true
				for _, p := range at {
					if math.Hypot(p[0]-x, p[1]-y) < 2*a.R+0.05 {
						ok = false
					}
				}
				if ok {
					at = append(at, [2]float64{x, y})
				}
			}
			if !ok {
				return // the spot is full: try again next tick
			}
		}
		tx, ty := 0.0, 0.0
		for i, a := range g.members {
			a.X, a.Y = at[i][0], at[i][1]
			a.spotX, a.spotY = a.homeX, a.homeY
			tx += a.homeX
			ty += a.homeY
		}
		n := float64(len(g.members))
		tx, ty = tx/n, ty/n
		for _, a := range g.members {
			a.hd = math.Atan2(ty-a.Y, tx-a.X)
			a.face, a.prevFace = a.hd, a.hd
			w.agents = append(w.agents, a)
		}
		w.groups = append(w.groups, g)
		w.setOff(g, pArrive, tx, ty)
		g.until = w.T + w.rng.ExpFloat64()*tripEvery
		w.pending, w.pendingAt = w.pending[1:], w.pendingAt[1:]
	}
}
