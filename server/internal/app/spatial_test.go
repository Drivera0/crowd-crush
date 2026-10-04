package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/geo"
	"github.com/Drivera0/crowd-crush/server/internal/hub"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/sim"
	"github.com/Drivera0/crowd-crush/server/internal/store"
)

func testServer(t *testing.T, opt Options) (*App, *httptest.Server) {
	t.Helper()
	if opt.Detect.VenueW == 0 {
		opt.Detect = detect.DefaultConfig()
	}
	if opt.RecordingsDir == "" {
		opt.RecordingsDir = t.TempDir()
	}
	a := New(opt)
	mux := http.NewServeMux()
	a.Routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return a, srv
}

func do(t *testing.T, method, url string, body any, out any) int {
	t.Helper()
	var rd *bytes.Reader
	if s, ok := body.(string); ok {
		rd = bytes.NewReader([]byte(s))
	} else {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, rd)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}

func sq(x0, y0, x1, y1 float64) []protocol.Point {
	return []protocol.Point{{x0, y0}, {x1, y0}, {x1, y1}, {x0, y1}}
}

func TestAreasAPI(t *testing.T) {
	dir := t.TempDir()
	a, srv := testServer(t, Options{DataDir: dir})

	var got []protocol.Area
	if code := do(t, "GET", srv.URL+"/api/areas", nil, &got); code != 200 || got == nil || len(got) != 0 {
		t.Fatalf("GET empty: %d %v", code, got)
	}
	// Default zones while there are no areas: A left half, B right half.
	snap := a.snapshotLocked(hub.Now())
	if len(snap.Zones) != 0 { // no detector step yet
		t.Logf("zones before first step: %d", len(snap.Zones))
	}

	bad := map[string]string{
		"empty id":   `[{"id":"","name":"x","sens":"normal","poly":[[0,0],[1,0],[1,1]]}]`,
		"duplicate":  `[{"id":"a","poly":[[0,0],[1,0],[1,1]]},{"id":"a","poly":[[0,0],[1,0],[1,1]]}]`,
		"two points": `[{"id":"a","poly":[[0,0],[1,0]]}]`,
		"long name":  `[{"id":"a","name":"` + strings.Repeat("n", 41) + `","poly":[[0,0],[1,0],[1,1]]}]`,
		"bad sens":   `[{"id":"a","sens":"extreme","poly":[[0,0],[1,0],[1,1]]}]`,
		"rest id":    `[{"id":"rest","poly":[[0,0],[1,0],[1,1]]}]`,
		"not json":   `{"id":"a"}`,
	}
	for name, body := range bad {
		if code := do(t, "PUT", srv.URL+"/api/areas", body, nil); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, code)
		}
	}
	var many []protocol.Area
	for i := 0; i <= MaxAreas; i++ {
		many = append(many, protocol.Area{ID: fmt.Sprint("a", i), Poly: sq(0, 0, 1, 1)})
	}
	if code := do(t, "PUT", srv.URL+"/api/areas", many, nil); code != http.StatusBadRequest {
		t.Errorf("%d areas: status %d, want 400", len(many), code)
	}

	areas := []protocol.Area{
		{ID: "a1", Name: "Stage front", Sens: "high", Poly: []protocol.Point{{-3, 2}, {10, 2}, {10, 8}, {2, 99}}},
		{ID: "a2", Poly: sq(14, 4, 20, 12)},
	}
	if code := do(t, "PUT", srv.URL+"/api/areas", areas, &got); code != 200 {
		t.Fatalf("PUT: %d", code)
	}
	if got[0].Poly[0] != (protocol.Point{0, 2}) || got[0].Poly[3] != (protocol.Point{2, 16}) {
		t.Errorf("points not clamped to the venue: %v", got[0].Poly)
	}
	if got[1].Name != "a2" || got[1].Sens != "normal" {
		t.Errorf("defaults not filled: %+v", got[1])
	}

	// Zones are now the areas plus "rest".
	a.mu.Lock()
	a.live.step(hub.Now())
	snap = a.snapshotLocked(hub.Now())
	a.mu.Unlock()
	var ids []string
	for _, z := range snap.Zones {
		ids = append(ids, fmt.Sprintf("%s/%v/%s", z.ID, z.Custom, z.Sens))
	}
	if fmt.Sprint(ids) != "[a1/true/high a2/true/normal rest/false/normal]" {
		t.Errorf("zones %v", ids)
	}

	// Persisted: a new app on the same data dir loads them.
	if _, err := os.Stat(filepath.Join(dir, "areas.json")); err != nil {
		t.Fatal(err)
	}
	_, srv2 := testServer(t, Options{DataDir: dir})
	got = nil
	do(t, "GET", srv2.URL+"/api/areas", nil, &got)
	if len(got) != 2 || got[0].Name != "Stage front" {
		t.Errorf("reloaded areas %+v", got)
	}

	// Clearing them brings the default zones back.
	do(t, "PUT", srv.URL+"/api/areas", []protocol.Area{}, &got)
	a.mu.Lock()
	a.live.step(hub.Now())
	snap = a.snapshotLocked(hub.Now())
	a.mu.Unlock()
	if len(snap.Zones) != 2 || snap.Zones[0].ID != "A" || snap.Zones[1].Custom {
		t.Errorf("default zones not back: %+v", snap.Zones)
	}
}

func TestVenueAPI(t *testing.T) {
	dir := t.TempDir()
	a, srv := testServer(t, Options{DataDir: dir})
	var v protocol.Venue
	do(t, "GET", srv.URL+"/api/venue", nil, &v)
	if v.W != 24 || v.H != 16 || v.Geo {
		t.Fatalf("default venue %+v", v)
	}
	for name, body := range map[string]string{
		"tiny":    `{"w":0.5,"h":16}`,
		"bad lat": `{"w":24,"h":16,"lat":95,"lon":0,"geo":true}`,
		"garbage": `[1,2]`,
	} {
		if code := do(t, "PUT", srv.URL+"/api/venue", body, nil); code != 400 {
			t.Errorf("%s: %d, want 400", name, code)
		}
	}
	want := protocol.Venue{W: 30, H: 20, Lat: 49.2781, Lon: -122.9199, Bearing: -90, Geo: true}
	if code := do(t, "PUT", srv.URL+"/api/venue", want, &v); code != 200 || v.Bearing != 270 || v.W != 30 {
		t.Fatalf("PUT venue: %d %+v", code, v)
	}
	var cfg protocol.Config
	do(t, "GET", srv.URL+"/api/config", nil, &cfg)
	if cfg.VenueW != 30 || cfg.VenueH != 20 || !cfg.Geo || cfg.NeighbourRadius <= 0 {
		t.Errorf("config %+v", cfg)
	}
	if c := a.Config(); c.VenueW != 30 {
		t.Errorf("detector venue %v", c.VenueW)
	}
	_, srv2 := testServer(t, Options{DataDir: dir})
	do(t, "GET", srv2.URL+"/api/venue", nil, &v)
	if v.W != 30 || v.Lat != 49.2781 || !v.Geo {
		t.Errorf("reloaded venue %+v", v)
	}
}

// TestGPS: fixes are converted with the venue anchor, smoothed, gated by
// accuracy, clamped (outside flag), ignored without an anchor, and never
// recorded as coordinates.
func TestGPS(t *testing.T) {
	anchor := geo.Anchor{Lat: 49.2781, Lon: -122.9199, Bearing: 0}
	recDir := t.TempDir()
	a, _ := testServer(t, Options{RecordingsDir: recDir, NoLocate: true}) // the position estimator off: this test pins the GPS path it replaces (locate_test.go covers that path with it on)
	a.PhoneHello("p1", 1, 1, "test")

	// No anchor yet: ignored.
	lat, lon := anchor.ToLatLon(5, 5)
	a.PhoneGPS("p1", lat, lon, 5)
	if m := a.live.meta["p1"]; m.x != 1 || m.acc != 0 {
		t.Fatalf("GPS used without an anchor: %+v", m)
	}

	if _, err := a.SetVenue(protocol.Venue{W: 24, H: 16, Lat: anchor.Lat, Lon: anchor.Lon, Geo: true}); err != nil {
		t.Fatal(err)
	}
	name, err := a.StartRecording("gps-privacy")
	if err != nil {
		t.Fatal(err)
	}
	a.PhoneGPS("p1", lat, lon, 5)
	m := a.live.meta["p1"]
	if abs(m.x-5) > 0.01 || abs(m.y-5) > 0.01 || m.acc != 5 || m.src() != protocol.SrcGPS || m.outside {
		t.Fatalf("first fix: %+v", m)
	}
	// A poor fix (acc > GPSMaxAcc) is ignored.
	lat2, lon2 := anchor.ToLatLon(15, 5)
	a.PhoneGPS("p1", lat2, lon2, 40)
	if abs(m.x-5) > 0.01 {
		t.Fatalf("inaccurate fix moved the phone to %.2f", m.x)
	}
	// A good one moves it part of the way (smoothing).
	a.PhoneGPS("p1", lat2, lon2, 10)
	if want := 5 + 10*geo.Weight(10); abs(m.x-want) > 0.01 {
		t.Fatalf("smoothed x %.2f, want %.2f", m.x, want)
	}
	// Outside the venue: clamped, flagged, out of every zone.
	a.PhonePos("p1", 3, 3) // manual placement resets the smoothing
	if m.src() != protocol.SrcManual {
		t.Fatal("pos should make the phone manual")
	}
	lat3, lon3 := anchor.ToLatLon(-10, 5)
	a.PhoneGPS("p1", lat3, lon3, 5)
	if !m.outside || m.x != 0 {
		t.Fatalf("outside fix: %+v", m)
	}
	a.mu.Lock()
	a.live.step(hub.Now())
	snap := a.snapshotLocked(hub.Now())
	a.mu.Unlock()
	if n := snap.Nodes[0]; !n.Outside || n.Zone != "" || n.Src != "gps" || n.Acc != 5 {
		t.Errorf("node %+v", n)
	}
	if _, _, err := a.StopRecording(); err != nil {
		t.Fatal(err)
	}

	// The recording holds metres only: no lat/lon keys, no coordinates.
	b, err := os.ReadFile(filepath.Join(recDir, name))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, bad := range []string{`"lat"`, `"lon"`, "49.27", "-122.9", "122.91"} {
		if strings.Contains(text, bad) {
			t.Errorf("recording contains %s:\n%s", bad, text)
		}
	}
	if !strings.Contains(text, `"k":"pos"`) || !strings.Contains(text, `"acc":5`) {
		t.Errorf("recording lacks pos records with acc:\n%s", text)
	}
	// And it replays: positions come back from the pos records.
	recs, err := store.ReadJSONL(filepath.Join(recDir, name))
	if err != nil {
		t.Fatal(err)
	}
	rs := &replayState{recs: recs, p: newPipeline(ReplayConfig(a.Config(), recs))}
	a.feedReplay(rs, recs[len(recs)-1].T)
	if rm := rs.p.meta["p1"]; rm == nil || !rm.outside || rm.acc != 5 {
		t.Errorf("replayed phone %+v", rm)
	}
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// TestLegacyRecordingPositions: an old row/col recording places phones on
// the legacy line (x = 4 + 0.6·col, y = venue height / 2).
func TestLegacyRecordingPositions(t *testing.T) {
	recs := []store.Record{
		{K: store.KindMeta, T: 1000, Label: "old", Rows: 1, Cols: 8},
		{K: store.KindHello, T: 1000, ID: "p0"},
		{K: store.KindHello, T: 1000, ID: "p5", Col: 5},
		{K: store.KindM, T: 1100, ID: "p7", Col: 7, CT: 1100}, // auto-recorder line without a hello
	}
	cfg := ReplayConfig(detect.DefaultConfig(), recs)
	a := New(Options{Detect: detect.DefaultConfig()})
	rs := &replayState{recs: recs, p: newPipeline(cfg)}
	a.feedReplay(rs, 2000)
	for id, want := range map[string][2]float64{"p0": {4, 8}, "p5": {7, 8}, "p7": {8.2, 8}} {
		m := rs.p.meta[id]
		if m == nil || abs(m.x-want[0]) > 1e-9 || abs(m.y-want[1]) > 1e-9 {
			t.Errorf("%s at %+v, want %v", id, m, want)
		}
	}
}

// TestDensityAlert: the gather scenario, replayed through the app, raises a
// red density alert with a crowding briefing, then the cluster clears.
func TestDensityAlert(t *testing.T) {
	sc, err := sim.NewLayout("gather", 40, 1, sim.CrowdLayout(true))
	if err != nil {
		t.Fatal(err)
	}
	const t0 = 1_700_000_000_000
	var recs []store.Record
	recs = append(recs, store.Record{K: store.KindMeta, T: t0, Label: "gather", W: 24, H: 16})
	for i := 0; i < sc.N(); i++ {
		x, y := sc.Pos(i)
		recs = append(recs, store.Record{K: store.KindHello, T: t0, ID: fmt.Sprint("g", i), X: store.F(x), Y: store.F(y)})
	}
	const dur = 120.0
	for ms := int64(500); ms <= dur*1000; ms += 500 {
		for i := 0; i < sc.N(); i++ {
			x, y := sc.PosAt(i, float64(ms)/1000)
			recs = append(recs, store.Record{K: store.KindPos, T: t0 + ms, ID: fmt.Sprint("g", i), X: store.F(x), Y: store.F(y)})
		}
	}
	for _, e := range sc.Generate(t0, dur) {
		recs = append(recs, store.Record{K: store.KindM, T: e.T, ID: fmt.Sprint("g", e.Phone), CT: e.T, AX: e.AX, AY: e.AY, AZ: e.AZ, Rot: e.Rot})
	}
	sortRecords(recs)

	a := New(Options{Detect: detect.DefaultConfig(), RecordingsDir: t.TempDir()})
	a.replay = &replayState{name: "gather", recs: recs, recStart: t0, recEnd: t0 + dur*1000, wallStart: t0, speed: 1,
		p: newPipeline(ReplayConfig(a.Config(), recs))}
	a.applyZones(a.replay.p)
	sawRedCluster, endBusy := false, false
	for now := int64(t0); now <= t0+dur*1000; now += 250 {
		a.detectTick(now)
		a.mu.Lock()
		if a.replay == nil {
			a.mu.Unlock()
			break
		}
		endBusy = false
		for _, c := range a.replay.p.clusters {
			if c.Level == protocol.LevelRed {
				sawRedCluster = true
			}
			if c.Level != protocol.LevelCalm {
				endBusy = true
			}
		}
		a.mu.Unlock()
	}
	if !sawRedCluster {
		t.Fatal("no cluster went red")
	}
	if endBusy {
		t.Error("a cluster is still above calm after the crowd dispersed")
	}
	// The briefing runs in the background (template text, no keys).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		var red protocol.Alert
		n := 0
		for _, al := range a.alerts {
			if al.Kind == protocol.KindDensity && al.Level == protocol.LevelRed {
				red = al
				n++
			}
		}
		a.mu.Unlock()
		if n > 1 {
			t.Fatalf("one incident per zone and kind, got %d red density alerts", n)
		}
		// The briefing updates the same alert (same ID) when it arrives.
		if red.Brief != "" {
			if red.Zone == "" || red.ID == "" || red.Score < detect.DefaultConfig().DensityDanger {
				t.Errorf("red density alert %+v", red)
			}
			if !strings.Contains(red.Brief, "people packed into") || !strings.Contains(red.Brief, "Zone "+red.Zone) || red.Headline == "" || red.Action == "" {
				t.Errorf("briefing %+v", red)
			}
			t.Logf("alert %+v", red)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no red density alert with a briefing: %+v", a.alerts)
}

// sortRecords orders records stably by time, like ReadJSONL.
func sortRecords(recs []store.Record) {
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].T < recs[j].T })
}
