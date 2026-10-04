package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Drivera0/crowd-crush/server/internal/brief"
	"github.com/Drivera0/crowd-crush/server/internal/crowdsim"
	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/hub"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/sign"
	"github.com/Drivera0/crowd-crush/server/internal/store"
)

func drillOut(d protocol.DrillRecord, key string) protocol.DrillOutput {
	for _, o := range d.Outputs {
		if o.Key == key {
			return o
		}
	}
	return protocol.DrillOutput{}
}

// TestDrillChoices: POST /api/test-alert takes the place, level, kind and
// outputs; an empty body is the old test alert; what each output did is
// reported truthfully (nothing configured here: template text, no clip, no
// boards) and kept in the history.
func TestDrillChoices(t *testing.T) {
	dir := t.TempDir()
	a, srv := testServer(t, Options{DataDir: dir, EscalateAfter: time.Second})
	areas := []protocol.Area{
		{ID: "gate", Name: "North gate", Poly: sq(1, 1, 6, 6), Light: "A",
			Rules: &protocol.AlertRules{MaxPhones: 40, Notify: &protocol.Notify{Voice: bp(false)}}},
		{ID: "bar", Name: "Bar", Poly: sq(10, 1, 16, 6)},
	}
	if _, err := a.SetAreas(areas); err != nil {
		t.Fatal(err)
	}
	a.detectTick(hub.Now())

	// Readiness with nothing configured.
	var st protocol.DrillStatus
	if code := do(t, "GET", srv.URL+"/api/drill", nil, &st); code != 200 {
		t.Fatalf("GET /api/drill: %d", code)
	}
	ready := map[string]protocol.DrillReady{}
	for _, r := range st.Outputs {
		ready[r.Key] = r
	}
	if ready["briefing"].State != protocol.ReadyFallback || ready["voice"].State != protocol.ReadyFallback || ready["sign"].State != protocol.ReadyNone {
		t.Errorf("readiness with no keys and no boards: %+v", st.Outputs)
	}
	if len(st.Zones) != 3 || st.Zones[0].ID != "gate" || st.Zones[0].Name != "North gate" || st.Zones[0].Light != "A" || st.Zones[0].Voice || !st.Zones[1].Voice {
		t.Errorf("zones: %+v", st.Zones)
	}

	// A crowding warning at the bar, briefing only.
	no, yes := false, true
	var d protocol.DrillRecord
	req := protocol.DrillRequest{Zone: "bar", Level: "yellow", Kind: "density",
		Outputs: &protocol.DrillOutputs{Briefing: &yes, Voice: &no, Sign: &no, Lights: &[]string{}}}
	if code := do(t, "POST", srv.URL+"/api/test-alert", req, &d); code != 200 {
		t.Fatalf("drill: %d", code)
	}
	if d.Zone != "bar" || d.Where != "Bar" || d.Level != "yellow" || d.Kind != "density" || d.ID == "" {
		t.Fatalf("drill record: %+v", d)
	}
	if o := drillOut(d, "voice"); o.State != protocol.DrillSkipped {
		t.Errorf("voice was switched off: %+v", o)
	}
	if o := drillOut(d, "sign"); o.State != protocol.DrillSkipped {
		t.Errorf("sign was switched off: %+v", o)
	}
	waitFor(t, func() bool {
		var s protocol.DrillStatus
		do(t, "GET", srv.URL+"/api/drill", nil, &s)
		return len(s.History) == 1 && drillOut(s.History[0], "briefing").State == protocol.DrillOK
	})
	do(t, "GET", srv.URL+"/api/drill", nil, &st)
	h := st.History[0]
	if !strings.HasPrefix(h.Brief, drillPrefix+"Bar: people bunching up") || h.AudioURL != "" ||
		!strings.Contains(drillOut(h, "briefing").Note, "template") {
		t.Errorf("briefing: %q, outputs %+v", h.Brief, h.Outputs)
	}
	a.mu.Lock()
	al := a.alerts[len(a.alerts)-1]
	a.mu.Unlock()
	if !al.Test || al.Kind != "density" || al.Level != "yellow" || al.Zone != "bar" || !strings.HasPrefix(al.Headline, drillPrefix) || al.Source != "" {
		t.Errorf("the drill's alert: %+v", al)
	}

	// Over capacity at the gate: the area's own limit, its message rules, its
	// light (not connected), voice off by the area's rules.
	if code := do(t, "POST", srv.URL+"/api/test-alert", protocol.DrillRequest{Zone: "gate", Kind: "rule"}, &d); code != 200 {
		t.Fatalf("drill: %d", code)
	}
	if d.Level != "red" || drillOut(d, "voice").State != protocol.DrillSkipped || !strings.Contains(drillOut(d, "voice").Note, "this area") ||
		drillOut(d, "light:A").State != protocol.DrillSkipped || drillOut(d, "sign").State != protocol.DrillSkipped {
		t.Errorf("gate drill: %+v", d)
	}
	waitFor(t, func() bool {
		var s protocol.DrillStatus
		do(t, "GET", srv.URL+"/api/drill", nil, &s)
		return strings.Contains(s.History[0].Brief, "limit of 40 people")
	})

	// The old call: no body. Bad choices: 400.
	var legacy map[string]any
	if code := do(t, "POST", srv.URL+"/api/test-alert", "", &legacy); code != 200 || legacy["zone"] == "" || legacy["level"] != "red" || legacy["kind"] != "wave" {
		t.Errorf("empty body: %d %v", code, legacy)
	}
	for _, bad := range []protocol.DrillRequest{{Zone: "nowhere"}, {Level: "calm"}, {Kind: "fire"}} {
		if code := do(t, "POST", srv.URL+"/api/test-alert", bad, nil); code != 400 {
			t.Errorf("%+v: %d, want 400", bad, code)
		}
	}
	// Drills never escalate, whatever their level.
	a.escalate(hub.Now() + 10*60_000)
	time.Sleep(30 * time.Millisecond)
	a.mu.Lock()
	for _, al := range a.alerts {
		if al.Escalated {
			t.Errorf("a drill escalated: %+v", al)
		}
	}
	n := len(a.drills)
	a.mu.Unlock()
	if n != 3 {
		t.Errorf("%d drills in the history, want 3", n)
	}
}

// TestDrillBoardsAndServices: with a sign, a zone light and Gemini
// configured, the drill reaches the boards chosen for it, reports an offline
// board as failed, and says who wrote the briefing.
func TestDrillBoardsAndServices(t *testing.T) {
	hits := make(chan string, 16)
	board := func(name string) *httptest.Server {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/level" {
				hits <- name + ":" + r.URL.Query().Get("v")
			}
			w.Write([]byte(`{"kind":"sign","level":"calm"}`))
		}))
		t.Cleanup(s.Close)
		return s
	}
	signSrv, lightA := board("sign"), board("A")
	gem := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{
			map[string]any{"text": `{"headline":"North gate is filling up.","action":"Hold the queue."}`}}}}}})
	}))
	t.Cleanup(gem.Close)
	c := sign.New(signSrv.URL + ",A=" + lightA.URL + ",B=http://127.0.0.1:1")
	a, srv := testServer(t, Options{DataDir: t.TempDir(), Sign: c, Brief: brief.New("k", "").WithBase(gem.URL)})
	if _, err := a.SetAreas([]protocol.Area{{ID: "gate", Name: "North gate", Poly: sq(1, 1, 6, 6), Light: "A"}}); err != nil {
		t.Fatal(err)
	}
	a.detectTick(hub.Now())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	st := c.Probe(ctx)
	cancel()
	a.mu.Lock()
	a.hw = hardwareList(st, a.hw, a.areas, hub.Now())
	a.mu.Unlock()

	var ds protocol.DrillStatus
	do(t, "GET", srv.URL+"/api/drill", nil, &ds)
	ready := map[string]protocol.DrillReady{}
	for _, r := range ds.Outputs {
		ready[r.Key] = r
	}
	if ready["briefing"].State != protocol.ReadyOK || ready["sign"].State != protocol.ReadyOK || ready["light:A"].State != protocol.ReadyOK ||
		ready["light:B"].State != protocol.ReadyOffline || len(ready["light:A"].Areas) != 1 {
		t.Fatalf("readiness: %+v", ds.Outputs)
	}

	var d protocol.DrillRecord
	req := protocol.DrillRequest{Zone: "gate", Level: "yellow", Outputs: &protocol.DrillOutputs{Lights: &[]string{"a", "B"}}}
	if code := do(t, "POST", srv.URL+"/api/test-alert", req, &d); code != 200 {
		t.Fatalf("drill: %d", code)
	}
	if drillOut(d, "sign").State != protocol.DrillOK || drillOut(d, "light:A").State != protocol.DrillOK || drillOut(d, "light:B").State != protocol.DrillFailed {
		t.Errorf("boards: %+v", d.Outputs)
	}
	got := map[string]bool{}
	for !got["sign:yellow"] || !got["A:yellow"] { // the calm level of the first detector tick arrives first
		select {
		case h := <-hits:
			got[h] = true
		case <-time.After(3 * time.Second):
			t.Fatalf("boards hit: %v, want the sign and light A at yellow", got)
		}
	}
	if !got["sign:yellow"] || !got["A:yellow"] {
		t.Errorf("boards hit: %v", got)
	}
	waitFor(t, func() bool {
		do(t, "GET", srv.URL+"/api/drill", nil, &ds)
		return drillOut(ds.History[0], "briefing").State == protocol.DrillOK
	})
	h := ds.History[0]
	if h.Brief != drillPrefix+"North gate is filling up. Hold the queue." || drillOut(h, "briefing").Note != "written by Gemini" ||
		drillOut(h, "voice").State != protocol.DrillSkipped {
		t.Errorf("briefing %q, outputs %+v", h.Brief, h.Outputs)
	}
}

// TestSimAlertsMarkedAndCleared: alerts raised by a simulation carry
// source "sim" and leave the log when it stops or restarts; real alerts and
// drills stay.
func TestSimAlertsMarkedAndCleared(t *testing.T) {
	a := New(Options{Detect: detect.DefaultConfig(), RecordingsDir: t.TempDir()})
	info := func() brief.Info { return brief.Info{Zone: "A", Level: "red"} }
	a.mu.Lock()
	real, _ := a.raiseLocked("live", protocol.KindWave, "A", "calm", "yellow", 0.4, ft0, false, false, info)
	a.mu.Unlock()
	a.TestAlert()
	count := func() (sim, other int) {
		a.mu.Lock()
		defer a.mu.Unlock()
		for _, al := range a.alerts {
			if al.Source == "sim" {
				sim++
			} else {
				other++
			}
		}
		return
	}
	run := func(seed int64) {
		if err := a.startSimAt(SimStart{People: 250, Participation: 0.6, Seed: seed}, simT0); err != nil {
			t.Fatal(err)
		}
		r := &simRunner{t: t, a: a, now: simT0}
		r.act(protocolAction("stage"))
		r.until(20, nil)
		r.act(protocolAction("surge"))
		r.until(45, nil)
	}
	run(1)
	sim, other := count()
	if sim == 0 || other != 2 {
		t.Fatalf("after a surge: %d sim alerts, %d others (want some, and the real one plus the drill)", sim, other)
	}
	if real.Source != "" {
		t.Errorf("a live alert has source %q", real.Source)
	}
	// Restart: the new run starts clean.
	if err := a.StopSim(); err != nil {
		t.Fatal(err)
	}
	if sim, other = count(); sim != 0 || other != 2 {
		t.Errorf("after stop: %d sim alerts, %d others", sim, other)
	}
	a.mu.Lock()
	open := len(a.openInc)
	a.mu.Unlock()
	if open != 1 {
		t.Errorf("%d open incidents after the sim stopped, want the real one", open)
	}
	run(2)
	if sim, _ = count(); sim == 0 {
		t.Fatal("the second run raised nothing")
	}
	// Starting a replay replaces the simulation and its alerts too.
	dir := a.opt.RecordingsDir
	w, err := store.CreateJSONL(filepath.Join(dir, "calm-x.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	w.Write(store.Record{K: store.KindMeta, T: 1000, Label: "calm", W: 24, H: 16})
	w.Write(store.Record{K: store.KindHello, T: 1000, ID: "p1", X: store.F(3), Y: store.F(3)})
	for i := int64(0); i < 50; i++ {
		w.Write(store.Record{K: store.KindM, T: 1000 + i*100, ID: "p1", AX: 0.01})
	}
	w.Close()
	if err := a.StartReplay(context.Background(), "calm-x.jsonl", 1); err != nil {
		t.Fatal(err)
	}
	if sim, other = count(); sim != 0 || other != 2 {
		t.Errorf("after a replay replaced the sim: %d sim alerts, %d others", sim, other)
	}
}

func protocolAction(typ string) crowdsim.Action { return crowdsim.Action{Type: typ} }

// TestLegacyRecordingVenue: a recording from before free positions plays in
// the default venue it was made in (default zones, phones in a row in the
// middle), whatever the venue and areas are today; a recording with a venue
// size plays in that. GET /api/recordings says which.
func TestLegacyRecordingVenue(t *testing.T) {
	src := filepath.Join("..", "..", "..", "recordings", "sim-wave.jsonl")
	b, err := os.ReadFile(src)
	if err != nil {
		t.Skip("no recordings/sim-wave.jsonl")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sim-wave.jsonl"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	a, srv := testServer(t, Options{RecordingsDir: dir, DataDir: t.TempDir()})
	if _, err := a.SetVenue(protocol.Venue{W: 60, H: 40}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetAreas([]protocol.Area{{ID: "big", Name: "Arena floor", Poly: sq(0, 0, 60, 40)}}); err != nil {
		t.Fatal(err)
	}
	var list struct {
		Files []RecordingInfo `json:"files"`
	}
	if code := do(t, "GET", srv.URL+"/api/recordings", nil, &list); code != 200 || len(list.Files) != 1 {
		t.Fatalf("recordings: %d %+v", code, list)
	}
	f := list.Files[0]
	if !f.Legacy || f.W != 24 || f.H != 16 || f.Phones != 8 || f.Seconds < 60 || f.Label != "sim-wave" {
		t.Errorf("recording info: %+v", f)
	}
	if err := a.StartReplay(context.Background(), "sim-wave.jsonl", 4); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	rs := a.replay
	rs.wallStart = hub.Now() - 5000 // 20 s into the run at 4×
	a.mu.Unlock()
	a.detectTick(hub.Now())
	a.mu.Lock()
	s := a.snapshotLocked(hub.Now())
	a.mu.Unlock()
	if s.Mode != "replay" || s.Venue.W != 24 || s.Venue.H != 16 {
		t.Fatalf("replay snapshot: mode %s, venue %+v", s.Mode, s.Venue)
	}
	if len(s.Zones) != 2 || s.Zones[0].Custom {
		t.Errorf("a legacy replay uses the default zones, got %+v", s.Zones)
	}
	if len(s.Nodes) != 8 {
		t.Fatalf("%d phones in the replay, want 8", len(s.Nodes))
	}
	for _, n := range s.Nodes {
		if n.Y != 8 || n.X < 1 || n.X > 12 {
			t.Errorf("phone %s at %.1f, %.1f: not in the row in the middle of the 24 × 16 m room", n.ID, n.X, n.Y)
		}
	}
	a.StopReplay()
	a.mu.Lock()
	s = a.snapshotLocked(hub.Now())
	a.mu.Unlock()
	if s.Mode != "live" || s.Venue.W != 60 {
		t.Errorf("after the replay: mode %s, venue %+v", s.Mode, s.Venue)
	}
}

// TestClearHardwarePos: DELETE /api/hardware/pos takes every board off the map.
func TestClearHardwarePos(t *testing.T) {
	dir := t.TempDir()
	c := sign.New("http://127.0.0.1:1,A=http://127.0.0.1:1")
	a, srv := testServer(t, Options{DataDir: dir, Sign: c})
	if code := do(t, "PUT", srv.URL+"/api/hardware/A/pos", map[string]float64{"x": 3, "y": 4}, nil); code != 200 {
		t.Fatalf("place: %d", code)
	}
	if code := do(t, "DELETE", srv.URL+"/api/hardware/pos", nil, nil); code != 200 {
		t.Fatalf("clear: %d", code)
	}
	a.mu.Lock()
	n := len(a.hwPos)
	a.mu.Unlock()
	b, _ := os.ReadFile(filepath.Join(dir, hardwareFile))
	if n != 0 || strings.TrimSpace(string(b)) != "{}" {
		t.Errorf("%d positions left, file %q", n, b)
	}
}
