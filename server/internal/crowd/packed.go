package crowd

import (
	"math"

	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// How packed in each person is. A crowd crush hurts the people in the
// middle of the mass and the ones pressed against a barrier or a wall, and
// those people are not moving: the motion detector calls every one of them
// "ok". So next to its motion status every phone gets a crush level from
// the one thing Pulse knows about a still phone: how many phones are around
// it.
//
// Dens is the estimated people per m² around the phone: the phones within
// LocalR of it (itself included; a wider disc for a phone whose position is
// only known to ± Acc, as in LocalPeakAmong) ÷ the part of that disc people
// can stand on (OpenFraction: inside the venue, off the stage) ÷
// participation, smoothed over PackedTauMs. It is the same local density
// the clusters' peak is made of, per phone, with the edge correction that
// matters for one person standing at the barrier.
//
// Level runs Dens through the cluster levels' state machine: yellow above
// Watch, red above Danger, each held for HoldMs and clearing Margin below
// (so a dot does not flicker), and Crush puts Dens on a 0..1 scale for
// drawing (Crush01).

// PackedTauMs is the smoothing time of a phone's local density.
const PackedTauMs = 1500

// PackedMoveSpeed (m/s): a phone whose net speed over its recent positions
// (up to FlowWindowMs) is at least this is moving (Packed.Moving).
const PackedMoveSpeed = 0.1

// Packed is one phone's crush level.
type Packed struct {
	Dens  float64 // estimated people per m² around it
	Level string  // calm | yellow | red
	Crush float64 // 0..1 (Crush01 of Dens)
	// Moving: the phone itself has been walking (net PackedMoveSpeed or
	// more over the last few seconds, placed to within FlowMaxAcc): someone
	// moving along a dense aisle is in a queue, not pinned, so a yellow is
	// shown calm (flow.go). Red is never masked.
	Moving bool
}

type packedState struct {
	dens  float64
	t     int64
	level detect.LevelState
}

// PackedTracker follows every phone's crush level. Not safe for concurrent use.
type PackedTracker struct {
	st    map[string]*packedState
	moves moves
}

// NewPackedTracker creates a tracker.
func NewPackedTracker() *PackedTracker {
	return &PackedTracker{st: map[string]*packedState{}, moves: moves{}}
}

// Crush01 puts an estimated density on the 0..1 scale the dashboard draws:
// 0 up to half the watch density (standing free), 0.35 at watch, 0.7 at
// danger and 1 at 1.5 × danger (6 /m² at the defaults: bodies touching all
// round), linear in between.
func Crush01(dens, watch, danger float64) float64 {
	lerp := func(v, a, b, ya, yb float64) float64 { return ya + (yb-ya)*(v-a)/(b-a) }
	switch {
	case dens <= watch/2:
		return 0
	case dens <= watch:
		return lerp(dens, watch/2, watch, 0, 0.35)
	case dens <= danger:
		return lerp(dens, watch, danger, 0.35, 0.7)
	case dens < 1.5*danger:
		return lerp(dens, danger, 1.5*danger, 0.7, 1)
	}
	return 1
}

// Update takes the phones that count at time now (the same points the
// clusters get) and returns each one's crush level. open reports whether
// people can stand at a point (nil = anywhere). Phones that are not in pts
// (stale, outside, unplaced, lost) have no entry and lose their history.
func (t *PackedTracker) Update(now int64, pts []Point, cfg Config, open func(x, y float64) bool) map[string]Packed {
	part := cfg.Participation
	if part <= 0 {
		part = 1
	}
	th := detect.Thresholds{Yellow: cfg.Watch, Red: cfg.Danger,
		YMargin: cfg.Watch * cfg.Margin, RMargin: cfg.Danger * cfg.Margin, Hold: cfg.HoldMs}
	out := make(map[string]Packed, len(pts))
	if cfg.flowOn() {
		t.moves.add(now, pts, cfg.FlowMaxAcc)
	}
	r2 := LocalR * LocalR
	for _, p := range pts {
		r, rr := LocalR, r2
		if u := cfg.AccDisc * p.Acc; u > LocalR {
			r, rr = u, u*u
		}
		k := 0
		for _, q := range pts {
			dx, dy := p.X-q.X, p.Y-q.Y
			if dx*dx+dy*dy <= rr {
				k++
			}
		}
		frac := 1.0
		if open != nil {
			frac = OpenFraction(p.X, p.Y, r, open)
		}
		dens := float64(max(k, 1)) / (math.Pi * rr * frac) / part
		s := t.st[p.ID]
		if s == nil {
			s = &packedState{dens: dens, level: detect.NewLevelState()}
			t.st[p.ID] = s
		} else if dt := float64(now - s.t); dt > 0 {
			s.dens += (dens - s.dens) * (1 - math.Exp(-dt/PackedTauMs))
		}
		s.t = now
		s.level.Update(now, s.dens, th)
		pk := Packed{Dens: s.dens, Level: s.level.Level, Crush: Crush01(s.dens, cfg.Watch, cfg.Danger)}
		if vx, vy, ok := t.moves.velocity(p.ID); ok && cfg.flowOn() && math.Hypot(vx, vy) >= PackedMoveSpeed {
			pk.Moving = true
			if pk.Level == protocol.LevelYellow && s.dens < cfg.flowWatch() {
				pk.Level = protocol.LevelCalm
			}
		}
		out[p.ID] = pk
	}
	for id := range t.st {
		if _, ok := out[id]; !ok {
			delete(t.st, id)
		}
	}
	return out
}

// PackedRank orders crush levels (calm < yellow < red).
func PackedRank(level string) int {
	switch level {
	case protocol.LevelRed:
		return 2
	case protocol.LevelYellow:
		return 1
	}
	return 0
}
