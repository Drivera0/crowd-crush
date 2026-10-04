package app

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"

	"github.com/Drivera0/crowd-crush/server/internal/hub"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// Moving about at the table demo. The judges stand in a numbered row (the
// demo spot, demo.go). A web page can't measure where a phone is to the
// decimetre, so moving shows up on the big screen in the honest ways:
//
//   - Tap to move: the phone's "I moved" map (zoomed to the row: GET
//     /api/demo/row) sends an ordinary "pos". While the demo spot is on
//     that drops its number and keeps its place in the row free for it
//     (demoMove.slot); POST /api/demo/back puts it back there. The tapped
//     spot is exact: the position estimator leaves it alone (heldLocked).
//   - Swap: staff drag one phone's dot onto another's place in the row
//     (PUT /api/node/{id}/pos) and the two swap places.
//   - Walk to a board: beaconsnap.go.

// demoMove is a live phone's state for all of that (nodeMeta.dm).
type demoMove struct {
	// slot: the place in the row (1-based) this phone left (a tap, walking
	// up to a board). Nobody else is lined up
	// there until it goes back, is lined up again or is forgotten. 0 = none.
	slot int
	// tapped: the phone tapped its own spot while the demo spot was on and
	// stands at (tx, ty): an exact placement, held while it is still there.
	tapped bool
	tx, ty float64
	snap   snapState // beside a board (beaconsnap.go)
}

// heldLocked reports whether a phone stands exactly where it was put and
// the position estimator must not move it: lined up at the demo spot,
// a spot it tapped at the table, or beside a board it walked up to.
// Caller holds mu.
func (a *App) heldLocked(m *nodeMeta) bool {
	return m.pinned || m.dm.snapped(m) || (a.demo.On && m.dm.tapped && m.x == m.dm.tx && m.y == m.dm.ty)
}

// demoSlotTakenLocked reports whether slot i (0-based) is taken by a known
// phone other than skip: standing on it, or keeping it while away.
// Caller holds mu.
func (a *App) demoSlotTakenLocked(i int, skip ...string) bool {
	sp, _, _ := a.demoGrid()
	x, y := a.demoSlotLocked(i)
outer:
	for id, m := range a.live.meta {
		for _, s := range skip {
			if id == s {
				continue outer
			}
		}
		if m.dm.slot == i+1 || (!m.unplaced && math.Hypot(m.x-x, m.y-y) < sp/2) {
			return true
		}
	}
	return false
}

// demoFreeIndexLocked is the first slot (0-based) nobody known to the
// server stands on or keeps. Caller holds mu.
func (a *App) demoFreeIndexLocked(skip string) int {
	for i := 0; i < 2000; i++ {
		if !a.demoSlotTakenLocked(i, skip) {
			return i
		}
	}
	return 2000
}

// demoOnSlotLocked is the slot (0-based) a lined-up phone stands on, -1
// if none. Caller holds mu.
func (a *App) demoOnSlotLocked(m *nodeMeta) int {
	if !m.pinned || m.unplaced {
		return -1
	}
	return a.demoSlotOfLocked(m.x, m.y)
}

// demoTapLocked handles a "pos" from the phone while the demo spot is on:
// it leaves its place in the row (kept for it), and the tapped spot is
// exact. It reports false when the demo spot is off and the caller places
// the phone as before. Caller holds mu.
func (a *App) demoTapLocked(now int64, id string, x, y float64) bool {
	if !a.demo.On {
		return false
	}
	m := a.live.meta[id]
	if m == nil {
		return true
	}
	if i := a.demoOnSlotLocked(m); i >= 0 {
		m.dm.slot = i + 1
	}
	m.dm.snap = snapState{}
	m.bcn.placed = false
	m.pinned = false
	a.posIn(a.live, now, id, x, y)
	m.dm.tapped, m.dm.tx, m.dm.ty = true, m.x, m.y
	return true
}

// ErrDemoOff: the demo spot is off, so there is no row to go back to.
var ErrDemoOff = errors.New("the demo spot is off: there is no row")

// DemoBack is POST /api/demo/back: the phone goes back to its place in the
// row (the one it left, or the first free one if somebody stands there now).
func (a *App) DemoBack(id string) (protocol.DemoBack, error) {
	now := hub.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	m := a.live.meta[id]
	if m == nil {
		return protocol.DemoBack{}, ErrNoPhone
	}
	if !a.demo.On {
		return protocol.DemoBack{}, ErrDemoOff
	}
	if i := a.demoOnSlotLocked(m); i >= 0 {
		return protocol.DemoBack{N: i + 1, X: r2(m.x), Y: r2(m.y)}, nil // already there
	}
	i := a.demoBackLocked(now, id, m, m.dm.slot-1)
	return protocol.DemoBack{N: i + 1, X: r2(m.x), Y: r2(m.y)}, nil
}

// demoBackLocked lines phone id up on slot prefer (0-based), or the first
// free slot if prefer is -1 or taken. Its tap, snap and kept place end.
// Returns the slot. Caller holds mu.
func (a *App) demoBackLocked(now int64, id string, m *nodeMeta, prefer int) int {
	i := prefer
	if i < 0 || a.demoSlotTakenLocked(i, id) {
		i = a.demoFreeIndexLocked(id)
	}
	a.demoLineUpLocked(now, id, m, i)
	return i
}

// demoLineUpLocked puts phone id on slot i (0-based), pinned, whoever has
// it. Its tap, snap and kept place end. Caller holds mu.
func (a *App) demoLineUpLocked(now int64, id string, m *nodeMeta, i int) {
	m.dm = demoMove{}
	m.bcn.placed = false
	x, y := a.demoSlotLocked(i)
	a.posIn(a.live, now, id, x, y)
	m.pinned = true
}

// demoDropLocked is a staff drag while the demo spot is on. Dropped on a
// place in the row, the phone is lined up there; if another phone stands
// on it, that one takes the dragged phone's old place (or the first free
// one) and its name is returned. Dropped anywhere else it goes there as
// before (a lined-up phone keeps its pin; its old place is free for the
// next phone that joins).
// Caller holds mu.
func (a *App) demoDropLocked(now int64, id string, m *nodeMeta, x, y float64) (swapped string) {
	x, y = a.liveConfig().Clamp(x, y)
	from := a.demoOnSlotLocked(m)
	if from < 0 && m.dm.slot > 0 {
		from = m.dm.slot - 1
	}
	m.dm.snap = snapState{}
	m.bcn.placed = false
	j := a.demoSlotOfLocked(x, y)
	if j < 0 {
		// Staff moved it on purpose: the place it stood on is free for the
		// next phone that joins (a place it already kept while away stays kept).
		a.posIn(a.live, now, id, x, y)
		if m.dm.tapped {
			m.dm.tx, m.dm.ty = m.x, m.y // still where the person is: held there
		}
		return ""
	}
	// Who has place j now: standing on it, or keeping it while away.
	sp, _, _ := a.demoGrid()
	jx, jy := a.demoSlotLocked(j)
	other, standing := "", false
	for oid, om := range a.live.meta {
		if oid == id {
			continue
		}
		if !om.unplaced && math.Hypot(om.x-jx, om.y-jy) < sp/2 {
			other, standing = oid, true
			break
		}
		if om.dm.slot == j+1 && other == "" {
			other = oid
		}
	}
	a.demoLineUpLocked(now, id, m, j)
	if other == "" || j == from {
		return ""
	}
	om := a.live.meta[other]
	if !standing {
		// It was away from place j: it keeps the dragged phone's old place instead.
		om.dm.slot = 0
		if from >= 0 && !a.demoSlotTakenLocked(from, other) {
			om.dm.slot = from + 1
		}
		return ""
	}
	a.demoBackLocked(now, other, om, from)
	return om.name
}

// demoSpotsLocked is, for every connected phone off the row while the demo
// spot is on, the place it goes back to: the one kept for it, else the
// first free one (1-based). rows are the phones on the row. Caller holds mu.
func (a *App) demoSpotsLocked(rows map[string]*protocol.DemoRow) map[string]int {
	if !a.demo.On {
		return nil
	}
	out := map[string]int{}
	free := -1
	for id, m := range a.live.meta {
		if !m.connected || m.unplaced || rows[id] != nil {
			continue
		}
		if m.dm.slot > 0 && !a.demoSlotTakenLocked(m.dm.slot-1, id) {
			out[id] = m.dm.slot
			continue
		}
		if free < 0 {
			free = a.demoFreeIndexLocked("")
		}
		out[id] = free + 1
	}
	return out
}

// DemoView is GET /api/demo/row: the row, the boards and the phones, for
// the phone's zoomed "I moved" map.
func (a *App) DemoView() protocol.DemoView {
	a.mu.Lock()
	defer a.mu.Unlock()
	d := a.demo
	v := protocol.DemoView{On: d.On, X: d.X, Y: d.Y, Spacing: d.Spacing,
		Phones: []protocol.DemoViewPhone{}, Boards: []protocol.DemoViewBoard{}}
	if !d.On {
		return v
	}
	sp, cols, _ := a.demoGrid()
	v.Spacing, v.Cols = sp, cols
	slots := a.demoSlotsLocked()
	for _, id := range sortedKeys(a.live.meta) {
		m := a.live.meta[id]
		if !m.connected || m.unplaced || m.outside {
			continue
		}
		v.Phones = append(v.Phones, protocol.DemoViewPhone{N: slots[id], X: math.Round(m.x*10) / 10, Y: math.Round(m.y*10) / 10,
			Name: m.name, Color: m.color, Near: m.dm.nearLabel(m)})
	}
	for _, k := range sortedKeys(a.hwPos) {
		p := a.hwPos[k]
		v.Boards = append(v.Boards, protocol.DemoViewBoard{Key: k, Label: towerName(k), X: p[0], Y: p[1]})
	}
	return v
}

func (a *App) demoMoveRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/demo/row", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, a.DemoView())
	})
	mux.HandleFunc("POST /api/demo/back", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); (err != nil && !errors.Is(err, io.EOF)) || req.ID == "" {
			httpError(w, errors.New(`want {"id": "<the phone's session id>"}`), http.StatusBadRequest)
			return
		}
		res, err := a.DemoBack(req.ID)
		switch {
		case errors.Is(err, ErrNoPhone):
			httpError(w, err, http.StatusNotFound)
		case errors.Is(err, ErrDemoOff):
			httpError(w, err, http.StatusConflict)
		case err != nil:
			httpError(w, err, http.StatusBadRequest)
		default:
			writeJSON(w, res)
		}
	})
}
