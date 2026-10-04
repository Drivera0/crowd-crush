package crowd

import (
	"math"
	"sort"
)

// Flow: are people still getting out of a dense crowd?
//
// Density alone can't tell a crush building from a queue. An aisle
// emptying a 500-seat hall runs at about 3 people/m² for a minute or two,
// which is normal: Fruin's walkway levels of service E/F are a moving queue
// at that density, and the SFPE's hydraulic model has stairs and corridors
// carrying their most people per metre per second near 2 /m². What turns a
// dense crowd dangerous is the flow stopping: people still arriving and
// nobody getting out (a crowd pressing to a stage, a queue backing up
// behind turnstiles that can't take it).
//
// Measured (docs/EVAL.md has the run): around a cluster's densest spot,
// speed and velocity spread don't separate the two (an aisle just filling
// and a crowd walking up to a stage both read 0.2–0.3 m/s, converging);
// what does is whether anyone leaves the spot. At a stage front or a
// barrier nobody does, for the whole surge; in a moving aisle someone does
// every few seconds.
//
// So, from the phones' own positions (no new sensor), each cluster gets:
//
//   - Out / In: people per second (phones ÷ participation) crossing out of
//     / into the disc of radius LocalR around its densest spot over the last
//     FlowLagMs: inside then and now FlowEdge clear of it (out), or the
//     reverse (in). Flow = Out per metre of the disc's width.
//   - Speed: the net speed of the phones in that disc (m/s, words only).
//   - Flowing: at least FlowMinOut phones out. Then, for FlowMemoryMs, the
//     crowd still counts as flowing while it doesn't start packing (people
//     arriving, at least two phones in, with nobody leaving): a moving queue
//     stops and starts.
//
// A flowing cluster raises no early warning (the density rise is a route
// filling, and it drains) and is not on watch below FlowWatch (default:
// the danger density). Red is never touched: the state machine runs on the
// density exactly as before, a flowing yellow is only shown as calm, so a
// truly packed crowd goes red at the same moment, flowing or not.
//
// What it can't see: the first seconds of a route filling, before anyone
// has had time to reach its far side, look exactly like packing (people
// in, nobody out), so a filling aisle may still raise a short watch.
//
// Rough positions: from positions with metres of error a standing phone
// "leaves" and "arrives" as its fix wanders, so only phones placed to
// within FlowMaxAcc count; with fewer than FlowMinPhones of them in the
// disc the flow is unknown (FlowKnown false) and the old rule applies.
// The same holds for phones placed by hand: they never move, so they never
// flow, and a still crowd keeps today's rule.

const (
	FlowWindowMs  = 8000 // positions kept per phone
	FlowLagMs     = 6000 // crossings of the spot's edge are counted over this
	FlowMinSpanMs = 3000 // a phone's velocity needs this much history
	FlowMinPhones = 3    // phones in the disc needed to say anything
	FlowEdge      = 0.5  // m a phone must clear the disc's edge by
)

type stamp struct {
	t    int64
	x, y float64
}

// moves is each phone's recent positions.
type moves map[string][]stamp

// add records the positions at time now. A phone placed worse than maxAcc
// (or no longer in pts) loses its history.
func (m moves) add(now int64, pts []Point, maxAcc float64) {
	seen := make(map[string]bool, len(pts))
	for _, p := range pts {
		if p.Acc > maxAcc {
			continue
		}
		seen[p.ID] = true
		h := append(m[p.ID], stamp{now, p.X, p.Y})
		cut := 0
		for cut < len(h)-1 && h[cut+1].t <= now-FlowWindowMs {
			cut++
		}
		if cut > 0 {
			h = append(h[:0], h[cut:]...)
		}
		m[p.ID] = h
	}
	for id := range m {
		if !seen[id] {
			delete(m, id)
		}
	}
}

// at is where the phone was at time t (its newest sample at or before t),
// ok if its history reaches back that far.
func (m moves) at(id string, t int64) (x, y float64, ok bool) {
	h := m[id]
	if len(h) == 0 || h[0].t > t {
		return 0, 0, false
	}
	i := sort.Search(len(h), func(i int) bool { return h[i].t > t }) - 1
	return h[i].x, h[i].y, true
}

// velocity is the phone's mean velocity over its recorded window (m/s),
// ok when the window spans at least FlowMinSpanMs.
func (m moves) velocity(id string) (vx, vy float64, ok bool) {
	h := m[id]
	if len(h) < 2 {
		return 0, 0, false
	}
	a, b := h[0], h[len(h)-1]
	if b.t-a.t < FlowMinSpanMs {
		return 0, 0, false
	}
	dt := float64(b.t-a.t) / 1000
	return (b.x - a.x) / dt, (b.y - a.y) / dt, true
}

// flowOn reports whether the flow rule is on.
func (c Config) flowOn() bool { return c.FlowMinOut > 0 }

func (c Config) flowWatch() float64 {
	if c.FlowWatch > 0 {
		return c.FlowWatch
	}
	return c.Danger
}

// flow fills c's motion fields from the phones around its densest spot at
// time now (the positions are already in t.moves).
func (t *Tracker) flow(c *Cluster, pts []Point, now int64) {
	cfg := t.cfg
	if !cfg.flowOn() {
		return
	}
	r2 := LocalR * LocalR
	rOut := (LocalR + FlowEdge) * (LocalR + FlowEdge)
	var mx, my float64
	nv, out, in := 0, 0, 0
	for _, p := range pts {
		d1 := (p.X-c.PeakX)*(p.X-c.PeakX) + (p.Y-c.PeakY)*(p.Y-c.PeakY)
		if d1 <= r2 {
			if vx, vy, ok := t.moves.velocity(p.ID); ok {
				mx += vx
				my += vy
				nv++
			}
		}
		x0, y0, ok := t.moves.at(p.ID, now-FlowLagMs)
		if !ok {
			continue
		}
		d0 := (x0-c.PeakX)*(x0-c.PeakX) + (y0-c.PeakY)*(y0-c.PeakY)
		switch {
		case d0 <= r2 && d1 >= rOut:
			out++
		case d0 >= rOut && d1 <= r2:
			in++
		}
	}
	if nv < FlowMinPhones {
		return
	}
	part := cfg.Participation
	if part <= 0 {
		part = 1
	}
	sec := float64(FlowLagMs) / 1000
	c.FlowKnown = true
	c.Speed = math.Hypot(mx, my) / float64(nv)
	c.Out = float64(out) / part / sec
	c.In = float64(in) / part / sec
	c.Flow = c.Out / (2 * LocalR)
	c.inN, c.outN = in, out
	c.Flowing = float64(out) >= cfg.FlowMinOut
}

// flowState applies the track's memory: a crowd that was flowing within
// FlowMemoryMs and has not started packing since (at least two phones in,
// none out) is still flowing.
//
// The result is then held: Flowing only changes once the new answer has
// stood for FlowHoldMs, so a crowd on the edge doesn't flicker between
// watch and calm.
func (t *Tracker) flowState(tr *track, f *Cluster, now int64) {
	switch {
	case !f.FlowKnown:
	case f.Flowing:
		tr.flowAt = now
	case f.outN == 0 && f.inN >= 2:
		tr.flowAt = 0 // packing: forget the flow
	case tr.flowAt > 0 && now-tr.flowAt <= t.cfg.FlowMemoryMs:
		f.Flowing = true
	}
	switch {
	case f.Flowing == tr.flowing:
		tr.flowSince = 0
	case tr.flowSince == 0:
		tr.flowSince = now
	case now-tr.flowSince >= FlowHoldMs:
		tr.flowing, tr.flowSince = f.Flowing, 0
	}
	f.Flowing = tr.flowing
}

// FlowHoldMs: how long a new flowing / not flowing answer must stand.
const FlowHoldMs = 2000

// Motion words, for briefings and the dashboard: what the crowd at a
// cluster's densest spot is doing.
const (
	MotionFlowing = "flowing" // dense, but people are getting out
	MotionPacking = "packing" // people arriving and nobody getting out
	MotionStill   = "still"   // packed and barely moving
	MotionUnknown = ""        // positions too rough, or too few phones, to tell
)

// Motion names the crowd's motion at the cluster's densest spot.
func (c Cluster) Motion() string {
	switch {
	case !c.FlowKnown:
		return MotionUnknown
	case c.Flowing:
		return MotionFlowing
	case c.In > 0:
		return MotionPacking
	}
	return MotionStill
}

// MotionText says it in words for staff: why a density level is what it is.
func MotionText(m string) string {
	switch m {
	case MotionFlowing:
		return "dense but moving: people are getting out"
	case MotionPacking:
		return "packing in: people arriving and nobody getting out"
	case MotionStill:
		return "packed and barely moving"
	}
	return ""
}
