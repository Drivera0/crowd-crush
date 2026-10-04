package app

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/hub"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// TestCapacityGuidance: a small area with capacity 3 and 4 phones in it
// turns red, and all 4 phones are told to move out of it (reason density);
// a phone outside gets no move. Every state carries x, y, w, h.
func TestCapacityGuidance(t *testing.T) {
	area := sq(10, 6, 14, 10)
	a := ruleApp(t, []protocol.Area{{ID: "pit", Name: "Pit", Sens: "high", Poly: area,
		Rules: &protocol.AlertRules{MaxPhones: 3}}})
	addPhones(a, "p", [][2]float64{{11, 7}, {13, 7}, {11, 9}, {12.5, 8.5}})
	addPhones(a, "out", [][2]float64{{20, 12}})
	now := ft0
	for ; now <= ft0+4000; now += 250 {
		tick(a, now)
	}
	if lv := zoneLevelOf(a, "pit"); lv != protocol.LevelRed {
		t.Fatalf("pit %s, want red (4 phones > 3)", lv)
	}
	a.mu.Lock()
	states := a.phoneStatesLocked()
	a.mu.Unlock()
	poly := make([][2]float64, len(area))
	for i, p := range area {
		poly[i] = [2]float64{p[0], p[1]}
	}
	for _, id := range []string{"p0", "p1", "p2", "p3"} {
		st := states[id]
		if st.Zone != protocol.LevelRed || st.Move == nil || st.Move.Reason != protocol.ReasonDensity {
			t.Fatalf("%s: %+v (move %+v)", id, st, st.Move)
		}
		// Following the arrow leaves the area: 3 m along it is outside.
		x, y := st.X+3*st.Move.DX, st.Y+3*st.Move.DY
		if detect.InPolygon(poly, x, y) {
			t.Errorf("%s at %.1f,%.1f: move (%.2f, %.2f) stays in the area", id, st.X, st.Y, st.Move.DX, st.Move.DY)
		}
		if math.Abs(math.Hypot(st.Move.DX, st.Move.DY)-1) > 0.02 {
			t.Errorf("%s: not a unit vector %+v", id, st.Move)
		}
		if st.W != 24 || st.H != 16 || st.Bearing != nil {
			t.Errorf("%s: venue %gx%g bearing %v", id, st.W, st.H, st.Bearing)
		}
	}
	if st := states["out0"]; st.Move != nil || st.X != 20 || st.Y != 12 {
		t.Errorf("phone outside the area: %+v", st)
	}
	// Back under capacity: moves cleared.
	a.PhoneGone("p3")
	for end := now + 1000; now <= end; now += 250 {
		tick(a, now)
	}
	a.mu.Lock()
	states = a.phoneStatesLocked()
	a.mu.Unlock()
	if st := states["p0"]; st.Move != nil || st.Zone != protocol.LevelCalm {
		t.Errorf("after clearing: %+v", st)
	}
	b, _ := json.Marshal(states["p0"])
	if strings.Contains(string(b), "move") || !strings.Contains(string(b), `"x":11`) {
		t.Errorf("wire: %s", b)
	}
}

// TestGuidanceBearing: a GPS-anchored venue sends its bearing.
func TestGuidanceBearing(t *testing.T) {
	a, _ := testServer(t, Options{Venue: protocol.Venue{W: 24, H: 16, Lat: 49.2, Lon: -122.9, Bearing: 30, Geo: true}})
	addPhones(a, "p", [][2]float64{{5, 5}})
	tick(a, ft0)
	a.mu.Lock()
	st := a.phoneStatesLocked()["p0"]
	a.mu.Unlock()
	if st.Bearing == nil || *st.Bearing != 30 {
		t.Fatalf("bearing %v", st.Bearing)
	}
}

// TestSameState: states compare by value, including the move.
func TestSameState(t *testing.T) {
	b := 10.0
	s1 := protocol.PhoneState{Type: "state", Node: "ok", Zone: "red", Move: &protocol.Move{DX: 1, Reason: "density"}, Bearing: &b, X: 1}
	s2 := s1
	s2.Move = &protocol.Move{DX: 1, Reason: "density"}
	if !sameState(s1, s2) {
		t.Error("equal states differ")
	}
	s2.Move = &protocol.Move{DX: 0.9, Reason: "density"}
	if sameState(s1, s2) {
		t.Error("different moves equal")
	}
	s2.Move = nil
	if sameState(s1, s2) {
		t.Error("move vs none equal")
	}
}

// TestEdgeAPIAndStats: /api/edge explains a neighbour pair (either order),
// 404s for other pairs and 400s without both ids; snapshots carry
// detectMs and the last snapshot's size.
func TestEdgeAPIAndStats(t *testing.T) {
	a, srv := testServer(t, Options{})
	addPhones(a, "p", [][2]float64{{5, 5}, {5.6, 5}, {6.2, 5}, {15, 5}})
	now := ft0
	for ; now <= ft0+8000; now += 250 {
		tick(a, now)
	}
	var ex protocol.EdgeExplain
	if code := do(t, "GET", srv.URL+"/api/edge?from=p1&to=p0", nil, &ex); code != 200 {
		t.Fatalf("edge: %d", code)
	}
	if ex.From != "p0" || ex.To != "p1" || ex.Wave || len(ex.Checks) == 0 || len(ex.Lags) != len(ex.Corr) || ex.StepMs != 50 {
		t.Errorf("explain %+v", ex)
	}
	if ex.Checks[0].Name != "Both phones moving" || ex.Checks[0].Pass {
		t.Errorf("still phones: first check %+v", ex.Checks[0])
	}
	if code := do(t, "GET", srv.URL+"/api/edge?from=p0&to=p3", nil, nil); code != http.StatusNotFound {
		t.Errorf("far pair: %d", code)
	}
	if code := do(t, "GET", srv.URL+"/api/edge?from=p0", nil, nil); code != http.StatusBadRequest {
		t.Errorf("missing to: %d", code)
	}
	a.mu.Lock()
	a.snapBytes = 1234
	s := a.snapshotLocked(hub.Now())
	a.mu.Unlock()
	if s.Stats.DetectMs <= 0 || s.Stats.DetectMs != math.Round(s.Stats.DetectMs*100)/100 || s.Stats.SnapshotBytes != 1234 {
		t.Errorf("stats %+v", s.Stats)
	}
}
