package app

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/geo"
	"github.com/Drivera0/crowd-crush/server/internal/hub"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

func locApp(t *testing.T) *App {
	t.Helper()
	return New(Options{Detect: detect.DefaultConfig(), RecordingsDir: t.TempDir(), DataDir: t.TempDir()})
}

// locTick runs one pipeline step of the live pipeline at time now.
func locTick(a *App, now int64) {
	a.mu.Lock()
	a.live.step(now)
	a.mu.Unlock()
}

func nodeOf(t *testing.T, a *App, id string) protocol.Node {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, n := range a.snapshotLocked(hub.Now()).Nodes {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("no node %s", id)
	return protocol.Node{}
}

// A phone that joins with no position starts at the entry spot, with an
// honest uncertainty, on a spot of its own; without an entry spot it stays
// unplaced as before.
func TestLocateEntry(t *testing.T) {
	a := locApp(t)
	a.PhoneHelloAuto("nowhere", "iPhone")
	locTick(a, hub.Now())
	if m := towerMeta(t, a, "nowhere"); !m.unplaced {
		t.Fatalf("no entry spot set, yet the phone was placed at %.1f, %.1f", m.x, m.y)
	}
	if _, err := a.SetLocate(protocol.LocateConfig{On: true, Entry: protocol.EntrySpot{On: true, X: 4, Y: 14}}); err != nil {
		t.Fatal(err)
	}
	a.PhoneHelloAuto("a", "iPhone")
	a.PhoneHelloAuto("b", "iPhone")
	locTick(a, hub.Now())
	ma, mb := towerMeta(t, a, "a"), towerMeta(t, a, "b")
	for _, m := range []nodeMeta{ma, mb} {
		if m.unplaced || math.Hypot(m.x-4, m.y-14) > 4 || m.acc < 1 || m.acc > 4 {
			t.Errorf("at the entry spot: %.2f, %.2f ± %.1f unplaced %v", m.x, m.y, m.acc, m.unplaced)
		}
	}
	if math.Hypot(ma.x-mb.x, ma.y-mb.y) < 0.35 {
		t.Errorf("two phones on the same spot: %.2f, %.2f and %.2f, %.2f", ma.x, ma.y, mb.x, mb.y)
	}
	n := nodeOf(t, a, "a")
	if n.Src != protocol.SrcEst || n.Unplaced || n.Acc <= 0 || len(n.Loc) == 0 || n.Loc[0] != protocol.LocEntry {
		t.Errorf("node %+v", n)
	}
	// The phone that joined before there was an entry spot is placed there too
	// once it says hello again.
	a.PhoneHelloAuto("nowhere", "iPhone")
	locTick(a, hub.Now())
	if m := towerMeta(t, a, "nowhere"); m.unplaced {
		t.Error("still unplaced after the entry spot was set")
	}
	// Saying hello again does not make the estimate exact.
	a.PhoneHelloAuto("a", "iPhone")
	locTick(a, hub.Now())
	if m := towerMeta(t, a, "a"); m.acc < 1 || math.Hypot(m.x-ma.x, m.y-ma.y) > 0.01 {
		t.Errorf("after a second hello: %.2f, %.2f ± %.1f, was %.2f, %.2f ± %.1f", m.x, m.y, m.acc, ma.x, ma.y, ma.acc)
	}
}

// A phone placed by hand is exactly where it was put, estimator or not.
func TestLocateExactUnchanged(t *testing.T) {
	a := locApp(t)
	a.PhoneHello("p", 6.6, 8, "test")
	now := hub.Now()
	for i := 0; i < 1200; i++ { // five minutes of standing
		now += 250
		a.mu.Lock()
		a.motionIn(a.live, "p", protocol.Motion{T: now, AX: 0.01}, now)
		a.live.step(now)
		a.mu.Unlock()
	}
	m := towerMeta(t, a, "p")
	if m.x != 6.6 || m.y != 8 || m.acc != 0 || m.src() != protocol.SrcManual || m.loc.lost {
		t.Fatalf("placed by hand: %.3f, %.3f acc %.1f src %s", m.x, m.y, m.acc, m.src())
	}
	if n := nodeOf(t, a, "p"); n.Raw != nil || n.Src != protocol.SrcManual {
		t.Errorf("node %+v", n)
	}
	a.PhonePos("p", 3, 3)
	locTick(a, now+250)
	if m := towerMeta(t, a, "p"); m.x != 3 || m.y != 3 || m.acc != 0 {
		t.Errorf("moved by hand: %.3f, %.3f acc %.1f", m.x, m.y, m.acc)
	}
}

// GPS with the estimator on: fixes become positions in venue metres, a fix
// off the edge of the map is someone at the edge (kept, not dropped), the
// raw position is there for the dashboard, and no coordinate is recorded.
func TestLocateGPS(t *testing.T) {
	anchor := geo.Anchor{Lat: 49.2781, Lon: -122.9199, Bearing: 0}
	recDir := t.TempDir()
	a := New(Options{Detect: detect.DefaultConfig(), RecordingsDir: recDir, DataDir: t.TempDir()})
	if _, err := a.SetVenue(protocol.Venue{W: 24, H: 16, Lat: anchor.Lat, Lon: anchor.Lon, Geo: true}); err != nil {
		t.Fatal(err)
	}
	name, err := a.StartRecording("locate-gps")
	if err != nil {
		t.Fatal(err)
	}
	a.PhoneHelloAuto("g", "Android")
	now := hub.Now()
	fix := func(x, y, acc float64) {
		lat, lon := anchor.ToLatLon(x, y)
		now += 1000
		a.mu.Lock()
		a.syncIn(a.live, now, "g", 0, 20)
		a.motionIn(a.live, "g", protocol.Motion{T: now}, now)
		a.gpsLocked(now, "g", lat, lon, acc)
		a.live.step(now)
		a.mu.Unlock()
	}
	fix(5, 5, 5)
	m := towerMeta(t, a, "g")
	if m.unplaced || math.Hypot(m.x-5, m.y-5) > 0.1 || m.acc < 4 || m.acc > 6 || m.src() != protocol.SrcGPS {
		t.Fatalf("first fix: %.2f, %.2f ± %.1f src %s unplaced %v", m.x, m.y, m.acc, m.src(), m.unplaced)
	}
	// A fix too poor to use changes nothing.
	fix(15, 5, 40)
	if m := towerMeta(t, a, "g"); math.Hypot(m.x-5, m.y-5) > 0.1 {
		t.Errorf("an inaccurate fix moved the phone to %.2f, %.2f", m.x, m.y)
	}
	// Fixes 6 m off the left edge: the phone is at the edge, inside, counted.
	for i := 0; i < 30; i++ {
		fix(-6, 5, 5)
	}
	m = towerMeta(t, a, "g")
	if m.outside || m.x < 0 || m.x > 3 || m.loc.lost {
		t.Errorf("fixes off the edge: %.2f, %.2f outside %v lost %v", m.x, m.y, m.outside, m.loc.lost)
	}
	n := nodeOf(t, a, "g")
	if n.Outside || n.Zone == "" || n.Src != protocol.SrcGPS || n.Acc <= 0 || n.Raw == nil {
		t.Errorf("node %+v", n)
	}
	a.mu.Lock()
	counted := false
	for _, p := range a.live.pts {
		counted = counted || p.ID == "g"
	}
	a.mu.Unlock()
	if !counted {
		t.Error("the phone at the edge doesn't count toward density")
	}
	if _, _, err := a.StopRecording(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(recDir, name))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`"lat"`, `"lon"`, "49.27", "-122.9", "122.91"} {
		if strings.Contains(string(b), bad) {
			t.Errorf("recording contains %s", bad)
		}
	}
	if !strings.Contains(string(b), `"k":"hello"`) {
		t.Errorf("the phone's first position isn't recorded as its hello:\n%s", b)
	}
}

// A tower check-in calibrates GPS: the phone's fixes are 9 m off, the
// estimator learns that at the tower and keeps the phone where it stands.
func TestLocateTowerGPS(t *testing.T) {
	a := locApp(t)
	anchor := geo.Anchor{Lat: 49.2781, Lon: -122.9199, Bearing: 30}
	if _, err := a.SetVenue(protocol.Venue{W: 24, H: 16, Lat: anchor.Lat, Lon: anchor.Lon, Bearing: anchor.Bearing, Geo: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetHardwarePos("laptop", 12, 8); err != nil {
		t.Fatal(err)
	}
	const bx, by = 7.0, -5.5
	if !a.PhoneHelloAt("cal", "laptop", "iPhone") {
		t.Fatal("not checked in")
	}
	spot := towerMeta(t, a, "cal")
	now := hub.Now()
	for i := 0; i < 30; i++ {
		now += 1000
		lat, lon := anchor.ToLatLon(spot.x+bx, spot.y+by)
		a.mu.Lock()
		a.motionIn(a.live, "cal", protocol.Motion{T: now}, now)
		a.gpsLocked(now, "cal", lat, lon, 6)
		a.live.step(now)
		a.mu.Unlock()
	}
	m := towerMeta(t, a, "cal")
	if d := math.Hypot(m.x-spot.x, m.y-spot.y); d > 1 {
		t.Errorf("30 s of fixes 9 m off moved a checked-in phone %.2f m", d)
	}
	if n := nodeOf(t, a, "cal"); n.Raw == nil || math.Hypot(n.Raw[0]-(spot.x+bx), n.Raw[1]-(spot.y+by)) > 1 {
		t.Errorf("raw position %v, want the GPS fix %.1f, %.1f", n.Raw, spot.x+bx, spot.y+by)
	}
}

// The phone's own dead reckoning moves it on the map, turned by the
// venue's bearing.
func TestLocateDR(t *testing.T) {
	a := locApp(t)
	b := 90.0 // the map's up is east
	if _, err := a.SetLocate(protocol.LocateConfig{On: true, Entry: protocol.EntrySpot{On: true, X: 12, Y: 12, Sigma: 0.5}, Bearing: &b}); err != nil {
		t.Fatal(err)
	}
	a.PhoneHelloAuto("w", "Android")
	locTick(a, hub.Now())
	start := towerMeta(t, a, "w")
	a.PhoneDR("w", protocol.DR{Steps: 0})
	a.PhoneDR("w", protocol.DR{Steps: 12, E: 8}) // 8 m east = up the map
	locTick(a, hub.Now()+250)
	m := towerMeta(t, a, "w")
	if math.Abs(m.x-start.x) > 0.3 || math.Abs((start.y-m.y)-8) > 0.3 {
		t.Errorf("walked 8 m east with the map's up east: moved %.2f, %.2f; want 0, -8", m.x-start.x, m.y-start.y)
	}
	if n := nodeOf(t, a, "w"); !strings.Contains(strings.Join(n.Loc, ","), protocol.LocSteps) {
		t.Errorf("sources %v lack steps", n.Loc)
	}
}

// Turning the estimator off brings the old paths back; GET and PUT
// /api/locate.
func TestLocateSwitch(t *testing.T) {
	a, srv := testServer(t, Options{DataDir: t.TempDir()})
	get := func() protocol.LocateStatus {
		t.Helper()
		r, err := http.Get(srv.URL + "/api/locate")
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		var st protocol.LocateStatus
		if err := json.NewDecoder(r.Body).Decode(&st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	put := func(c any) int {
		t.Helper()
		b, _ := json.Marshal(c)
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/locate", bytes.NewReader(b))
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	if st := get(); !st.On || st.Entry.On {
		t.Fatalf("default %+v, want on without an entry spot", st)
	}
	if code := put(map[string]any{"on": true, "entry": map[string]any{"on": true, "x": 3, "y": 99, "sigma": 2}}); code != 200 {
		t.Fatalf("PUT: %d", code)
	}
	if st := get(); !st.Entry.On || st.Entry.X != 3 || st.Entry.Y != 16 || st.Entry.Sigma != 2 {
		t.Errorf("entry %+v, want 3, 16 (clamped) ± 2", st.Entry)
	}
	if code := put(map[string]any{"on": true, "entry": map[string]any{"sigma": 99}}); code != 400 {
		t.Errorf("bad sigma: %d, want 400", code)
	}
	a.PhoneHelloAuto("p", "iPhone")
	locTick(a, hub.Now())
	if st := get(); st.Phones != 1 {
		t.Errorf("status %+v, want one phone", st)
	}
	if code := put(protocol.LocateConfig{}); code != 200 {
		t.Fatalf("PUT off: %d", code)
	}
	a.mu.Lock()
	off := a.live.loc == nil
	a.mu.Unlock()
	if !off || get().On {
		t.Error("the estimator is still on")
	}
	a.PhoneHelloAuto("q", "iPhone")
	locTick(a, hub.Now())
	if m := towerMeta(t, a, "q"); !m.unplaced {
		t.Error("with the estimator off a phone without a position was placed")
	}
}

// The crowd simulation with phones that never say where they are: everyone
// walks in from the entry spot, the estimator follows, and the snapshot
// says how far off it is.
func TestLocateSim(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	a := locApp(t)
	if _, err := a.SetLocate(protocol.LocateConfig{On: true, Entry: protocol.EntrySpot{On: true, X: 4, Y: 14.5}}); err != nil {
		t.Fatal(err)
	}
	one, yes := 1.0, true
	req := SimStart{People: 80, Participation: 0.6, Seed: 5, WalkIn: true, WalkInS: 30,
		Imperfections: &SimImperfections{Heading: &one, NoPos: &yes}}
	if err := a.startSimAt(req, simT0); err != nil {
		t.Fatal(err)
	}
	r := &simRunner{t: t, a: a, now: simT0}
	r.until(70, nil)
	a.mu.Lock()
	snap := a.snapshotLocked(r.now)
	a.mu.Unlock()
	if snap.Sim == nil || snap.Sim.Loc == nil {
		t.Fatal("no position error in the snapshot")
	}
	e := snap.Sim.Loc
	t.Logf("%d phones: mean %.2f m, median %.2f m, p95 %.2f m, %d lost", e.N, e.Mean, e.Median, e.P95, e.Lost)
	if e.N < 20 || e.Median > 6 {
		t.Errorf("position error %+v", *e)
	}
	walked := 0
	for _, n := range snap.Nodes {
		if n.Unplaced {
			t.Fatalf("node %s is unplaced", n.ID)
		}
		if strings.Contains(strings.Join(n.Loc, ","), protocol.LocSteps) {
			walked++
		}
	}
	if walked < len(snap.Nodes)/2 {
		t.Errorf("only %d of %d phones were dead-reckoned", walked, len(snap.Nodes))
	}
	if st := a.Locate(); st.Error == nil || st.Phones == 0 || st.StepMs <= 0 {
		t.Errorf("status %+v", st)
	}
	// WalkIn without an entry spot is refused.
	b := locApp(t)
	if err := b.startSimAt(SimStart{WalkIn: true}, simT0); err == nil {
		t.Error("walkIn without an entry spot was accepted")
	}
}
