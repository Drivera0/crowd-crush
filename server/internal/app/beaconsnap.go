package app

import (
	"hash/fnv"
	"log"
	"math"
	"sort"

	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/store"
)

// Walk to a board: the Bluetooth snap.
//
// Signal strength is a poor ruler at a distance (beacons.go: metres, not
// centimetres) but a good one for "right next to it": a few centimetres
// from a board the signal is far stronger than a metre away. So when a
// phone hears one board much more strongly than every other, strongly
// enough to be within about half a metre of it, for a few seconds in a row,
// it is put right beside that board on the map (src "beacon", "Near Zone
// light A" on the phone and the dashboard). When no board has been that
// near for a while it goes back where it was: its place in the demo row,
// the spot it tapped, or wherever staff or anything else put it.
//
// Every range source counts, the same ones as the beacon fix: the phone's
// own scan (the Android app, or Chrome's scanning flag), connect mode and
// the boards hearing the app's advert (beaconlinks.go). A board heard both
// ways is one range (the geometric mean of the two distances).
//
// Two boards closer together than about snapRatio × snapEnterM can't be
// told apart this way: on a table, keep them at least 1.5 m apart (the
// table-demo setup does: tabledemo.go).
const (
	// snapEnterM: nearer than this to a board (by its signal) to snap. With
	// the default model (−64 dBm at 1 m, n 2.2) that is −57 dBm or stronger.
	snapEnterM = 0.5
	// snapRatio: every other board must sound at least this much farther
	// away. 2.3× the distance is 8 dB at n 2.2: more than the few dB a hand,
	// a body or the phone's orientation changes from one second to the next.
	snapRatio = 2.3
	// snapAgreeN, snapAgreeMs: reports in a row (about one a second) and
	// the time they must span that all agree on the same board.
	snapAgreeN  = 3
	snapAgreeMs = 2000
	// snapGapMs: a gap between agreeing reports longer than this starts over.
	snapGapMs = 2500
	// snapLeaveM: hysteresis. Once beside a board the phone stays while that
	// board sounds nearer than this (−66 dBm at the default model, 8 dB
	// weaker than snapping in) and no other board sounds snapRatio nearer.
	snapLeaveM = 1.2
	// snapLeaveMs: no board near for this long (or no reports at all) and
	// the phone goes back where it was.
	snapLeaveMs = 5000
	// snapSpotM: how far from the board's marker the phone is drawn.
	snapSpotM = 0.4
)

// snapState is a phone's snap (demoMove.snap).
type snapState struct {
	key, label string // the board it stands beside ("" = none) and its name
	nearAt     int64  // last report in which that board still counted as near
	// The board the latest reports agree on, before snapping (or switching).
	cand              string
	candSince, candAt int64
	candN             int
	prev              snapPrev // where it was before the snap
}

// snapPrev is where a phone stood before it walked up to a board.
type snapPrev struct {
	x, y           float64
	pinned, tapped bool
	unplaced       bool
	slot           int // the place in the demo row (1-based) it stood on, 0 = none
}

// snapped reports whether the phone stands beside a board right now (and
// nothing else has moved it since).
func (d *demoMove) snapped(m *nodeMeta) bool {
	return d.snap.key != "" && m.bcn.owns(m)
}

// nearLabel is "Zone light A" while the phone stands beside that board.
func (d *demoMove) nearLabel(m *nodeMeta) string {
	if d.snapped(m) {
		return d.snap.label
	}
	return ""
}

// snapBoard is one placed board with the phone's distance to it.
type snapBoard struct {
	key, label string
	x, y, d    float64
}

// snapBoardsLocked turns a fix's heard list into one distance per placed
// board, nearest first. Caller holds mu.
func (a *App) snapBoardsLocked(heard []protocol.BeaconHeard) []snapBoard {
	placed := map[string]protocol.BeaconBoard{}
	for _, b := range a.beaconBoardsLocked() {
		if b.X != nil {
			placed[b.Name] = b
		}
	}
	logSum := map[string]float64{}
	count := map[string]int{}
	for _, h := range heard {
		if _, ok := placed[h.Name]; !ok {
			continue
		}
		logSum[h.Name] += math.Log(math.Max(h.Dist, 0.01))
		count[h.Name]++
	}
	out := make([]snapBoard, 0, len(count))
	for name, n := range count {
		b := placed[name]
		out = append(out, snapBoard{key: b.Key, label: b.Label, x: *b.X, y: *b.Y, d: math.Exp(logSum[name] / float64(n))})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].d != out[j].d {
			return out[i].d < out[j].d
		}
		return out[i].key < out[j].key
	})
	return out
}

// snapEnters reports whether the nearest board is near enough, and far
// enough ahead of the others, to snap to.
func snapEnters(bs []snapBoard) bool {
	return len(bs) > 0 && bs[0].d < snapEnterM && (len(bs) == 1 || bs[1].d >= bs[0].d*snapRatio)
}

// snapUpdateLocked takes a phone's new beacon fix (beaconUpdateLocked). It
// reports true when the snap places the phone (the caller then leaves its
// position alone). Caller holds mu.
func (a *App) snapUpdateLocked(id string, m *nodeMeta, fix protocol.BeaconFix, now int64) bool {
	s := &m.dm.snap
	if s.key != "" && !m.bcn.owns(m) {
		*s = snapState{} // something else moved it since: the snap is over
	}
	bs := a.snapBoardsLocked(fix.Heard)
	enter := snapEnters(bs)
	// Agreement: the same board, report after report.
	switch {
	case !enter || (s.key != "" && bs[0].key == s.key):
		s.cand, s.candN = "", 0
	case s.cand == bs[0].key && now-s.candAt <= snapGapMs:
		s.candN++
		s.candAt = now
	default:
		s.cand, s.candSince, s.candAt, s.candN = bs[0].key, now, now, 1
	}
	if s.cand != "" && s.candN >= snapAgreeN && now-s.candSince >= snapAgreeMs {
		a.snapToLocked(now, id, m, bs[0])
		return true
	}
	if s.key == "" {
		return false
	}
	for _, b := range bs {
		if b.key == s.key && b.d < snapLeaveM && b.d < bs[0].d*snapRatio {
			s.nearAt = now // still beside it
		}
	}
	if now-s.nearAt > snapLeaveMs {
		a.unsnapLocked(now, id, m)
	}
	return true
}

// snapTickLocked ends the snap of phones that stopped reporting (or walked
// away) for snapLeaveMs, every detector step. Caller holds mu.
func (a *App) snapTickLocked(now int64) {
	for id, m := range a.live.meta {
		s := &m.dm.snap
		if s.key == "" {
			continue
		}
		if !m.bcn.owns(m) {
			*s = snapState{}
			continue
		}
		if now-s.nearAt > snapLeaveMs {
			a.unsnapLocked(now, id, m)
		}
	}
}

// snapToLocked puts the phone beside board b. Where it stood before is
// remembered for when it walks away (a place in the demo row is kept free
// for it). Caller holds mu.
func (a *App) snapToLocked(now int64, id string, m *nodeMeta, b snapBoard) {
	s := &m.dm.snap
	if s.key == "" {
		s.prev = snapPrev{x: m.x, y: m.y, pinned: m.pinned, unplaced: m.unplaced,
			tapped: a.demo.On && m.dm.tapped && m.x == m.dm.tx && m.y == m.dm.ty}
		if a.demo.On {
			if i := a.demoOnSlotLocked(m); i >= 0 {
				m.dm.slot = i + 1 // kept for it while it is away
				s.prev.slot = i + 1
			}
		}
	}
	x, y := a.snapSpot(id, b.key, b.x, b.y)
	m.x, m.y = a.liveConfig().Clamp(x, y)
	m.acc, m.outside, m.pinned = 0, false, false
	m.tower, m.bias = "", gpsBias{}
	m.gps.Reset()
	m.dm.tapped = false
	m.bcn.x, m.bcn.y, m.bcn.placed = m.x, m.y, true
	s.key, s.label, s.nearAt = b.key, b.label, now
	s.cand, s.candN = "", 0
	log.Printf("phone %s walked up to %s (Bluetooth, about %.2f m)", short(id), b.label, b.d)
	if a.placedLocked(a.live, now, id, m) {
		return // its first position: recorded as its hello
	}
	a.live.place(id, m)
	a.record(store.Record{K: store.KindPos, T: now, ID: id, X: store.F(r2(m.x)), Y: store.F(r2(m.y))})
}

// unsnapLocked: no board near any more. The phone goes back where it was.
// Caller holds mu.
func (a *App) unsnapLocked(now int64, id string, m *nodeMeta) {
	s := m.dm.snap
	p := s.prev
	log.Printf("phone %s left %s", short(id), s.label)
	// A board it is walking up to now (agreement in progress) stays counted.
	defer func() { m.dm.snap = snapState{cand: s.cand, candSince: s.candSince, candAt: s.candAt, candN: s.candN} }()
	switch {
	case p.unplaced:
		// It had no position before: it stays by the board, as an ordinary
		// beacon placement the next fixes may move (beacons.go).
	case p.slot > 0 && a.demo.On:
		a.demoBackLocked(now, id, m, p.slot-1)
	default:
		m.bcn.placed = false
		a.posIn(a.live, now, id, p.x, p.y)
		m.pinned = p.pinned
		if p.tapped && a.demo.On {
			m.dm.tapped, m.dm.tx, m.dm.ty = true, m.x, m.y
		}
	}
}

// snapSpot is where a phone stands beside the board at (bx, by):
// snapSpotM from it, in front of it (toward +y on the map, where the phones
// stand at the table demo) at an angle picked from the phone's id so two
// phones at one board don't cover each other, kept inside the venue.
func (a *App) snapSpot(id, key string, bx, by float64) (x, y float64) {
	h := fnv.New32a()
	h.Write([]byte(id + "#" + key))
	ang := math.Pi/2 + (float64(h.Sum32()%1000)/999-0.5)*(2*math.Pi/3) // ±60° around +y
	cfg := a.liveConfig()
	for i := 0; i < 16; i++ {
		x, y = bx+snapSpotM*math.Cos(ang), by+snapSpotM*math.Sin(ang)
		if x >= 0 && y >= 0 && x <= cfg.VenueW && y <= cfg.VenueH {
			return x, y
		}
		ang += math.Pi / 8
	}
	return cfg.Clamp(x, y)
}
