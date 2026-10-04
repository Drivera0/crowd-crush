package app

import (
	"fmt"
	"math"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/Drivera0/crowd-crush/server/internal/brief"
	"github.com/Drivera0/crowd-crush/server/internal/crowd"
	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/hub"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// Fixes from the usability test: one density estimate, one overall status,
// alerts attributed to the drawn area, drills kept apart, audit trail,
// clearing the timeline, join URL reachability.

const testPart = 0.6

// crowdWithPatch is a thin crowd (thin people/m² at testPart) over the
// rectangle (0, 0)–(w, h) with a packed patch (packed people/m²) over
// (px0, py0)–(px1, py1), on grids.
func crowdWithPatch(w, h, thin, px0, py0, px1, py1, packed float64) [][2]float64 {
	var pts [][2]float64
	in := func(x, y float64) bool { return x >= px0 && x < px1 && y >= py0 && y < py1 }
	if thin > 0 {
		s := 1 / math.Sqrt(thin*testPart)
		for x := s / 2; x < w; x += s {
			for y := s / 2; y < h; y += s {
				if !in(x, y) {
					pts = append(pts, [2]float64{x, y})
				}
			}
		}
	}
	s := 1 / math.Sqrt(packed*testPart)
	for x := px0 + s/2; x < px1; x += s {
		for y := py0 + s/2; y < py1; y += s {
			pts = append(pts, [2]float64{x, y})
		}
	}
	return pts
}

func partApp(t *testing.T, cfg detect.Config, areas []protocol.Area) *App {
	t.Helper()
	cfg.Participation = testPart
	a := New(Options{Detect: cfg, RecordingsDir: t.TempDir()})
	if _, err := a.SetAreas(areas); err != nil {
		t.Fatal(err)
	}
	return a
}

// oldAreaEst is how area rules read density before: max(area-wide phones ÷
// polygon area, the 90th percentile of the inside phones' local densities
// among themselves) ÷ participation.
func oldAreaEst(pts [][2]float64, x0, y0, x1, y1 float64) float64 {
	var in []crowd.Point
	for i, p := range pts {
		if p[0] >= x0 && p[0] <= x1 && p[1] >= y0 && p[1] <= y1 {
			in = append(in, crowd.Point{ID: fmt.Sprint(i), X: p[0], Y: p[1]})
		}
	}
	var ks []int
	for _, p := range in {
		k := 0
		for _, q := range in {
			if math.Hypot(p.X-q.X, p.Y-q.Y) <= crowd.LocalR {
				k++
			}
		}
		ks = append(ks, k)
	}
	sort.Ints(ks)
	q := float64(ks[min(len(ks)-1, int(crowd.PeakQuantile*float64(len(ks))))]) / localDiscM2
	return math.Max(float64(len(in))/((x1-x0)*(y1-y0)), q) / testPart
}

// TestAreaDensityRulePackedGroup: a packed group (6 people/m²) inside a
// large area of thin crowd, at 60 % participation, reads about the true
// people density and fires a 5 /m² rule.
func TestAreaDensityRulePackedGroup(t *testing.T) {
	a := ruleAppPart(t, []protocol.Area{{ID: "floor", Name: "Main floor", Poly: sq(0, 0, 20, 14),
		Rules: &protocol.AlertRules{Density: 5, DensityHoldS: 1}}})
	pts := crowdWithPatch(20, 14, 1.5, 8, 5, 12, 9, 6)
	addPhones(a, "p", pts)
	now := ft0
	for ; now <= ft0+2500; now += 250 {
		tick(a, now)
	}
	a.mu.Lock()
	st := *a.live.rules["floor"]
	a.mu.Unlock()
	t.Logf("%d phones: area rule reads %.2f people/m² (old estimate %.2f); truth 6 in the patch, 1.5 around it",
		len(pts), st.est, oldAreaEst(pts, 0, 0, 20, 14))
	if st.est < 5.2 || st.est > 7 {
		t.Errorf("area density %.2f people/m², want about 6", st.est)
	}
	if lv := zoneLevelOf(a, "floor"); lv != protocol.LevelRed {
		t.Fatalf("5 /m² rule: zone %s, want red", lv)
	}
	if als := alertsOf(a, protocol.KindRule); len(als) != 1 || als[0].Score < 5 {
		t.Fatalf("rule alerts %+v", als)
	}
}

// ruleAppPart is ruleApp at 60 % participation.
func ruleAppPart(t *testing.T, areas []protocol.Area) *App {
	cfg := detect.DefaultConfig()
	cfg.DensityWatch, cfg.DensityDanger = 1000, 2000
	return partApp(t, cfg, areas)
}

// TestAreaDensityNearOutline: phones just past the outline count as
// neighbours of the phones inside.
func TestAreaDensityNearOutline(t *testing.T) {
	a := ruleAppPart(t, []protocol.Area{{ID: "edge", Name: "Edge", Poly: sq(10, 0, 20, 14),
		Rules: &protocol.AlertRules{Density: 5, DensityHoldS: 1}}})
	// A 4 × 4 m packed patch straddling the outline at x = 10.
	addPhones(a, "p", crowdWithPatch(0, 0, 0, 8, 5, 12, 9, 6))
	for now := ft0; now <= ft0+2500; now += 250 {
		tick(a, now)
	}
	a.mu.Lock()
	est := a.live.rules["edge"].est
	a.mu.Unlock()
	if est < 4.5 {
		t.Errorf("straddling patch reads %.2f people/m² inside the area", est)
	}
}

// TestCapacityInPeople: maxPhones is a capacity in people (phones ÷ participation).
func TestCapacityInPeople(t *testing.T) {
	a := ruleAppPart(t, []protocol.Area{{ID: "g", Name: "Gate", Poly: sq(0, 0, 20, 14),
		Rules: &protocol.AlertRules{MaxPhones: 50}}})
	var pts [][2]float64
	for i := 0; i < 40; i++ { // 40 phones = about 67 people
		pts = append(pts, [2]float64{1 + float64(i%10)*1.9, 1 + float64(i/10)*3})
	}
	addPhones(a, "p", pts)
	now := ft0
	for ; now <= ft0+3500; now += 250 {
		tick(a, now)
	}
	if lv := zoneLevelOf(a, "g"); lv != protocol.LevelRed {
		t.Fatalf("67 people > 50: %s", lv)
	}
	als := alertsOf(a, protocol.KindRule)
	if len(als) != 1 || als[0].Score != 67 {
		t.Fatalf("capacity alert should score people: %+v", als)
	}
}

// stageApp: a drawn "Stage front" area in the top-left corner with a packed
// patch in it, a thin crowd over the rest of the floor (one big cluster
// whose centroid is far outside the area), and a left side exit.
func stageApp(t *testing.T) *App {
	t.Helper()
	cfg := detect.DefaultConfig()
	a := partApp(t, cfg, []protocol.Area{{ID: "n2549bj", Name: "Stage front", Poly: sq(1, 1, 7, 7)}})
	if _, err := a.SetVenue(protocol.Venue{W: 24, H: 16, Layout: &protocol.VenueLayout{
		Exits: []protocol.LayoutExit{{ID: "l", Name: "Left side exit", X0: 0, Y0: 3, X1: 0, Y1: 5},
			{ID: "r", Name: "Right side exit", X0: 24, Y0: 10, X1: 24, Y1: 12}}}}); err != nil {
		t.Fatal(err)
	}
	addPhones(a, "p", crowdWithPatch(20, 14, 1.5, 2, 2, 6, 6, 6))
	return a
}

// TestDensityAttributedToArea: the density incident, its briefing and the
// overall status name the drawn area holding the cluster's densest spot,
// with the nearest exit; the cluster carries est.
func TestDensityAttributedToArea(t *testing.T) {
	a := stageApp(t)
	for now := ft0; now <= ft0+3000; now += 250 {
		tick(a, now)
	}
	a.mu.Lock()
	snap := a.snapshotLocked(hub.Now())
	a.mu.Unlock()
	if len(snap.Clusters) != 1 {
		t.Fatalf("%d clusters", len(snap.Clusters))
	}
	c := snap.Clusters[0]
	t.Logf("cluster at (%.1f, %.1f): density %.2f phones/m² of the disc, est %.2f people/m²; status %+v", c.X, c.Y, c.Density, c.Est, *snap.Status)
	if c.Est < 5 || c.Est > 7 || c.Level != protocol.LevelRed {
		t.Errorf("cluster est %.2f level %s, want about 6 and red", c.Est, c.Level)
	}
	st := snap.Status
	if st == nil || st.Level != protocol.LevelRed || st.Kind != protocol.StatusKindDensity || st.Zone != "n2549bj" ||
		st.Where != "Stage front" || st.Score < 0.8 || st.Score > 1 || math.Abs(st.Density-c.Est) > 1e-9 {
		t.Errorf("status %+v", st)
	}
	var al protocol.Alert
	waitFor(t, func() bool {
		als := alertsOf(a, protocol.KindDensity)
		if len(als) == 1 && als[0].Level == protocol.LevelRed && als[0].Brief != "" {
			al = als[0]
			return true
		}
		return false
	})
	if al.Zone != "n2549bj" || !strings.Contains(al.Headline, "Stage front") || strings.Contains(al.Brief, "n2549bj") ||
		!strings.Contains(al.Action, "Left side exit") {
		t.Errorf("density alert %+v", al)
	}
}

func TestStatusCalmAndRisk(t *testing.T) {
	a := New(Options{Detect: detect.DefaultConfig(), RecordingsDir: t.TempDir()})
	addPhones(a, "p", [][2]float64{{2, 2}, {10, 10}})
	tick(a, ft0)
	a.mu.Lock()
	st := *a.snapshotLocked(hub.Now()).Status
	a.mu.Unlock()
	if st.Level != protocol.LevelCalm || st.Zone != "" || st.Where != "" || st.Kind != "" || st.Score >= 0.5 {
		t.Errorf("calm status %+v", st)
	}
	for _, c := range []struct {
		v     float64
		level string
		lo    float64
		hi    float64
	}{
		{0, protocol.LevelCalm, 0, 0}, {1, protocol.LevelCalm, 0.25, 0.25}, {3, protocol.LevelCalm, 0.49, 0.49},
		{3, protocol.LevelYellow, 0.65, 0.65}, {1, protocol.LevelYellow, 0.5, 0.5}, {4, protocol.LevelRed, 0.8, 0.8},
		{8, protocol.LevelRed, 1, 1}, {20, protocol.LevelRed, 1, 1}, {3, protocol.LevelRed, 0.8, 0.8},
	} {
		if r := risk(c.v, 2, 4, c.level); r < c.lo-1e-9 || r > c.hi+1e-9 {
			t.Errorf("risk(%g, %s) = %.3f, want %.2f..%.2f", c.v, c.level, r, c.lo, c.hi)
		}
	}
}

// TestDrillsAndHistory: a test alert never escalates and stays out of the
// AI's history (only counted as a drill); the history leads with the live
// situation and names places, never area ids.
func TestDrillsAndHistory(t *testing.T) {
	a := stageApp(t)
	now := ft0
	for ; now <= ft0+3000; now += 250 {
		tick(a, now)
	}
	a.TestAlert()
	a.escalate(hub.Now() + 10*60_000)
	waitFor(t, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		for _, al := range a.alerts {
			if al.Test && al.Brief != "" {
				return true
			}
		}
		return false
	})
	a.mu.Lock()
	for _, al := range a.alerts {
		if al.Test && al.Escalated {
			t.Errorf("test alert escalated: %+v", al)
		}
	}
	a.mu.Unlock()
	h := a.History()
	t.Log(h)
	if !strings.Contains(h, "Current situation: RED at Stage front") || !strings.Contains(h, "Drills: 1 test alert") {
		t.Errorf("history lacks the live situation or the drill count")
	}
	if strings.Contains(h, "n2549bj") || strings.Contains(h, "[test]") {
		t.Errorf("history leaks an area id or lists the drill as an alert")
	}
	if !strings.Contains(h, "Active incidents") || !strings.Contains(h, "Stage front: density red") {
		t.Errorf("history lacks the active density incident")
	}
}

// TestAuditAndClear: ack/resolve take {by} / {by, note} (empty bodies still
// work), the fields are kept in the log; clear drops resolved and test
// alerts and keeps open real incidents.
func TestAuditAndClear(t *testing.T) {
	a, srv := testServer(t, Options{})
	a.mu.Lock()
	open, _ := a.raiseLocked("live", protocol.KindWave, "A", "calm", "yellow", 0.4, ft0, false, false, nil)
	toAck, _ := a.raiseLocked("live", protocol.KindDensity, "A", "calm", "yellow", 2.5, ft0, false, false, nil)
	toResolve, _ := a.raiseLocked("live", protocol.KindWave, "B", "calm", "yellow", 0.4, ft0, false, false, nil)
	a.mu.Unlock()
	a.TestAlert()

	var got protocol.Alert
	if code := do(t, "POST", srv.URL+"/api/alerts/"+toAck.ID+"/ack", map[string]string{"by": "  Sam   (gate 2) "}, &got); code != 200 ||
		got.AckBy != "Sam (gate 2)" || got.Status != protocol.StatusAck {
		t.Fatalf("ack: %d %+v", code, got)
	}
	if code := do(t, "POST", srv.URL+"/api/alerts/"+toResolve.ID+"/resolve", map[string]string{"note": strings.Repeat("x", 281)}, nil); code != 400 {
		t.Errorf("281-char note: %d, want 400", code)
	}
	if code := do(t, "POST", srv.URL+"/api/alerts/"+toResolve.ID+"/resolve", "{bad", nil); code != 400 {
		t.Errorf("bad JSON: %d, want 400", code)
	}
	if code := do(t, "POST", srv.URL+"/api/alerts/nope/resolve", map[string]string{"by": "x"}, nil); code != 404 {
		t.Errorf("unknown id: %d", code)
	}
	got = protocol.Alert{}
	if code := do(t, "POST", srv.URL+"/api/alerts/"+toResolve.ID+"/resolve", map[string]string{"by": "Ana", "note": "False alarm: band encore."}, &got); code != 200 ||
		got.ResolvedBy != "Ana" || got.Note != "False alarm: band encore." || got.Status != protocol.StatusResolved {
		t.Fatalf("resolve: %d %+v", code, got)
	}
	got = protocol.Alert{}
	if code := do(t, "POST", srv.URL+"/api/alerts/"+open.ID+"/ack", "", &got); code != 200 || got.ID != open.ID || got.Status != protocol.StatusAck || got.AckBy != "" {
		t.Fatalf("empty-body ack: %d %+v", code, got)
	}
	a.mu.Lock()
	welcome := a.alerts
	a.mu.Unlock()
	kept := false
	for _, al := range welcome {
		kept = kept || (al.ID == toResolve.ID && al.Note != "" && al.ResolvedBy == "Ana")
	}
	if !kept {
		t.Error("audit fields not kept in the log")
	}

	var cleared protocol.Alerts
	if code := do(t, "POST", srv.URL+"/api/alerts/clear", nil, &cleared); code != 200 || cleared.Type != protocol.TypeAlerts {
		t.Fatalf("clear: %d %+v", code, cleared)
	}
	ids := map[string]bool{}
	for _, al := range cleared.Alerts {
		ids[al.ID] = true
		if al.Test || al.Status == protocol.StatusResolved {
			t.Errorf("clear kept %+v", al)
		}
	}
	if !ids[open.ID] || !ids[toAck.ID] || ids[toResolve.ID] || len(ids) != 2 {
		t.Errorf("after clear: %v", ids)
	}
	// The open incident still gets updates under its id.
	a.mu.Lock()
	up, _ := a.raiseLocked("live", protocol.KindWave, "A", "yellow", "red", 0.7, ft0+1000, false, false, func() brief.Info { return brief.Info{Zone: "A", Level: "red"} })
	a.mu.Unlock()
	if up.ID != open.ID {
		t.Error("clear lost the open incident")
	}
}

func TestJoinURL(t *testing.T) {
	for host, want := range map[string]string{
		"localhost:8080": ReachLocal, "127.0.0.1:8080": ReachLocal, "[::1]:8080": ReachLocal,
		"192.168.1.20:8080": ReachLAN, "10.0.0.5": ReachLAN, "laptop:8080": ReachLAN, "pulse.local": ReachLAN,
		"pulse.example.tech": ReachPublic, "203.0.113.9": ReachPublic, "100.100.1.1": ReachLAN,
	} {
		r := httptest.NewRequest("GET", "/api/join", nil)
		r.Host = host
		if got := JoinURL("", r); got.Reachable != want || got.URL != "http://"+host+"/" {
			t.Errorf("%s: %+v, want %s", host, got, want)
		}
	}
	r := httptest.NewRequest("GET", "/api/join", nil)
	r.Host = "localhost:8080"
	if got := JoinURL("https://pulse.example.tech/", r); got.URL != "https://pulse.example.tech/" || got.Reachable != ReachPublic {
		t.Errorf("PUBLIC_URL: %+v", got)
	}
	r.Header.Set("X-Forwarded-Host", "pulse.example.tech")
	r.Header.Set("X-Forwarded-Proto", "https")
	if got := JoinURL("", r); got.URL != "https://pulse.example.tech/" || got.Reachable != ReachPublic {
		t.Errorf("tunnel: %+v", got)
	}
	_, srv := testServer(t, Options{PublicURL: "https://pulse.example.tech"})
	var got protocol.JoinInfo
	if code := do(t, "GET", srv.URL+"/api/join", nil, &got); code != 200 || got.Reachable != ReachPublic {
		t.Errorf("GET /api/join: %d %+v", code, got)
	}
}
