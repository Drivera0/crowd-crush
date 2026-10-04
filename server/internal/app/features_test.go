package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Drivera0/crowd-crush/server/internal/brief"
	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/hub"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/sign"
)

const ft0 = int64(1_700_000_000_000)

func rawReq(t *testing.T, method, url, ctype string, body []byte) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, url, bytes.NewReader(body))
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func testPNG(w, h int) []byte {
	var buf bytes.Buffer
	png.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h)))
	return buf.Bytes()
}

func bp(v bool) *bool { return &v }

// ---- venue: template, layout, floor plan ----

func TestVenueLayout(t *testing.T) {
	dir := t.TempDir()
	_, srv := testServer(t, Options{DataDir: dir})
	body := `{"w":30,"h":20,"template":" theatre ","floorplan":true,"layout":{
	  "stage":[[10,-2],[20,0],[20,3],[10,3]],
	  "exits":[{"id":"","name":"  Main   doors ","x0":14,"y0":20,"x1":16,"y1":25},{"id":"x","name":"Gone","x0":40,"y0":5,"x1":45,"y1":5}],
	  "walls":[[0,0,40,0],[1,1,1,1]]}}`
	var v protocol.Venue
	if code := do(t, "PUT", srv.URL+"/api/venue", body, &v); code != 200 {
		t.Fatalf("PUT: %d", code)
	}
	if v.Template != "theatre" || v.Floorplan || v.Layout == nil {
		t.Fatalf("venue %+v (floorplan is the server's to set)", v)
	}
	l := v.Layout
	if l.Stage[0] != (protocol.Point{10, 0}) || len(l.Exits) != 1 || l.Exits[0].ID != "exit-1" || l.Exits[0].Name != "Main doors" ||
		l.Exits[0].Y1 != 20 || len(l.Walls) != 1 || l.Walls[0][2] != 30 {
		t.Fatalf("layout not clamped/cleaned: %+v", l)
	}
	walls := make([]string, MaxWalls+1)
	for i := range walls {
		walls[i] = "[0,0,1,1]"
	}
	for name, bad := range map[string]string{
		"too many walls": `{"w":30,"h":20,"layout":{"walls":[` + strings.Join(walls, ",") + `]}}`,
		"long exit name": `{"w":30,"h":20,"layout":{"exits":[{"name":"` + strings.Repeat("x", MaxLayoutName+1) + `","x0":0,"y0":0,"x1":1,"y1":0}]}}`,
		"2-point stage":  `{"w":30,"h":20,"layout":{"stage":[[0,0],[1,1]]}}`,
		"long template":  `{"w":30,"h":20,"template":"` + strings.Repeat("t", MaxTemplate+1) + `"}`,
	} {
		if code := do(t, "PUT", srv.URL+"/api/venue", bad, nil); code != 400 {
			t.Errorf("%s: %d, want 400", name, code)
		}
	}
	// Saved with the venue; the simulator uses the layout's walls and exits.
	_, srv2 := testServer(t, Options{DataDir: dir})
	do(t, "GET", srv2.URL+"/api/venue", nil, &v)
	if v.Template != "theatre" || v.Layout == nil || len(v.Layout.Exits) != 1 {
		t.Fatalf("reloaded %+v", v)
	}
	var st protocol.SimStatus
	do(t, "GET", srv2.URL+"/api/sim", nil, &st)
	if len(st.Exits) != 1 || st.Exits[0].Name != "Main doors" || st.Exits[0].Y0 != 20 {
		t.Errorf("sim exits should come from the layout: %+v", st.Exits)
	}
}

func TestFloorplanAPI(t *testing.T) {
	dir := t.TempDir()
	_, srv := testServer(t, Options{DataDir: dir})
	img := testPNG(40, 20)
	resp, b := rawReq(t, "POST", srv.URL+"/api/venue/floorplan", "image/png", img)
	var v protocol.Venue
	if json.Unmarshal(b, &v); resp.StatusCode != 200 || !v.Floorplan {
		t.Fatalf("upload: %d %s", resp.StatusCode, b)
	}
	resp, b = rawReq(t, "GET", srv.URL+"/api/venue/floorplan", "", nil)
	if resp.StatusCode != 200 || !bytes.Equal(b, img) || resp.Header.Get("Content-Type") != "image/png" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("GET: %d %v", resp.StatusCode, resp.Header)
	}
	// Sniffed, not trusted: a text file labelled as a PNG is refused.
	if resp, _ := rawReq(t, "POST", srv.URL+"/api/venue/floorplan", "image/png", []byte("hello, not an image")); resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("text upload: %d", resp.StatusCode)
	}
	big := append(append([]byte{}, img...), make([]byte, MaxFloorplanSize)...)
	if resp, _ := rawReq(t, "POST", srv.URL+"/api/venue/floorplan", "image/png", big); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("9 MB upload: %d", resp.StatusCode)
	}
	// PUT /api/venue can't clear the flag; a restart keeps the image.
	do(t, "PUT", srv.URL+"/api/venue", `{"w":24,"h":16,"floorplan":false}`, &v)
	if !v.Floorplan {
		t.Error("PUT cleared floorplan")
	}
	_, srv2 := testServer(t, Options{DataDir: dir})
	do(t, "GET", srv2.URL+"/api/venue", nil, &v)
	if resp, b := rawReq(t, "GET", srv2.URL+"/api/venue/floorplan", "", nil); !v.Floorplan || resp.StatusCode != 200 || !bytes.Equal(b, img) {
		t.Fatalf("after restart: %+v %d", v, resp.StatusCode)
	}
	resp, b = rawReq(t, "DELETE", srv2.URL+"/api/venue/floorplan", "", nil)
	if json.Unmarshal(b, &v); resp.StatusCode != 200 || v.Floorplan {
		t.Fatalf("DELETE: %d %s", resp.StatusCode, b)
	}
	if resp, _ := rawReq(t, "GET", srv2.URL+"/api/venue/floorplan", "", nil); resp.StatusCode != 404 {
		t.Errorf("GET after delete: %d", resp.StatusCode)
	}
}

func TestFloorplanAnalyze(t *testing.T) {
	_, srv := testServer(t, Options{})
	resp, b := rawReq(t, "POST", srv.URL+"/api/venue/floorplan/analyze", "", nil)
	if resp.StatusCode != 503 || !strings.Contains(string(b), `"error":"Gemini isn't configured (GEMINI_API_KEY)"`) {
		t.Fatalf("no key: %d %s", resp.StatusCode, b)
	}

	answer := `{"w":40,"h":20,"confidence":"high","notes":"Scale from the dimension labels.",
	  "stage":[{"x":250,"y":0},{"x":750,"y":0},{"x":750,"y":100},{"x":250,"y":100}],
	  "exits":[{"name":"Main","x0":450,"y0":1000,"x1":550,"y1":1000}],"walls":[{"x0":0,"y0":0,"x1":1000,"y1":0}]}`
	failing := false
	gem := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failing {
			http.Error(w, `{"error":{"message":"quota"}}`, http.StatusTooManyRequests)
			return
		}
		out, _ := json.Marshal(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": answer}}}}}})
		w.Write(out)
	}))
	defer gem.Close()
	a, srv := testServer(t, Options{Brief: brief.New("k", "").WithBase(gem.URL)})
	if resp, _ := rawReq(t, "POST", srv.URL+"/api/venue/floorplan/analyze", "", nil); resp.StatusCode != 404 {
		t.Errorf("no plan: %d", resp.StatusCode)
	}
	rawReq(t, "POST", srv.URL+"/api/venue/floorplan", "image/png", testPNG(400, 200))
	var s protocol.FloorplanSuggestion
	resp, b = rawReq(t, "POST", srv.URL+"/api/venue/floorplan/analyze", "", nil)
	if json.Unmarshal(b, &s); resp.StatusCode != 200 || s.W != 40 || s.H != 20 || s.Confidence != "high" ||
		len(s.Layout.Stage) != 4 || len(s.Layout.Exits) != 1 || s.Layout.Exits[0].X0 != 18 || s.Layout.Exits[0].Y0 != 20 {
		t.Fatalf("analyze: %d %s", resp.StatusCode, b)
	}
	if v := a.Venue(); v.W != 24 || v.Layout != nil {
		t.Errorf("analyze must not save anything: %+v", v)
	}
	failing = true
	if resp, b := rawReq(t, "POST", srv.URL+"/api/venue/floorplan/analyze", "", nil); resp.StatusCode != 502 || !strings.Contains(string(b), "error") {
		t.Errorf("Gemini failure: %d %s", resp.StatusCode, b)
	}
}

// ---- area rules ----

// ruleApp is an app whose cluster density alerts are out of the way, so
// only the area rules raise density.
func ruleApp(t *testing.T, areas []protocol.Area) *App {
	t.Helper()
	cfg := detect.DefaultConfig()
	cfg.DensityWatch, cfg.DensityDanger = 1000, 2000
	a := New(Options{Detect: cfg, RecordingsDir: t.TempDir()})
	if _, err := a.SetAreas(areas); err != nil {
		t.Fatal(err)
	}
	return a
}

func addPhones(a *App, prefix string, pts [][2]float64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i, p := range pts {
		id := fmt.Sprintf("%s%d", prefix, i)
		a.helloIn(a.live, ft0, id, p[0], p[1], "test")
		a.syncIn(a.live, ft0, id, 0, 10)
	}
}

// tick feeds every connected phone a calm reading and runs the detector.
func tick(a *App, now int64) {
	a.mu.Lock()
	for id, m := range a.live.meta {
		if m.connected {
			a.motionIn(a.live, id, protocol.Motion{T: now, AX: 0.01, AY: 0.01, AZ: 0.01, Rot: 1}, now)
		}
	}
	a.mu.Unlock()
	a.detectTick(now)
}

func zoneLevelOf(a *App, id string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, z := range a.snapshotLocked(hub.Now()).Zones {
		if z.ID == id {
			return z.Level
		}
	}
	return ""
}

func alertsOf(a *App, kind string) []protocol.Alert {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []protocol.Alert
	for _, al := range a.alerts {
		if al.Kind == kind {
			out = append(out, al)
		}
	}
	return out
}

func TestDensityRule(t *testing.T) {
	a := ruleApp(t, []protocol.Area{{ID: "g", Name: "Gate", Poly: sq(10, 6, 12, 8),
		Rules: &protocol.AlertRules{Density: 4, DensityHoldS: 2}}})
	var pts [][2]float64 // 20 phones in 4 m²: 5 per m²
	for i := 0; i < 5; i++ {
		for j := 0; j < 4; j++ {
			pts = append(pts, [2]float64{10.2 + 0.4*float64(i), 6.2 + 0.5*float64(j)})
		}
	}
	addPhones(a, "p", pts)
	now := ft0
	for ; now <= ft0+1500; now += 250 {
		tick(a, now)
	}
	if lv := zoneLevelOf(a, "g"); lv != protocol.LevelCalm {
		t.Fatalf("level %s before the hold time", lv)
	}
	for ; now <= ft0+3000; now += 250 {
		tick(a, now)
	}
	if lv := zoneLevelOf(a, "g"); lv != protocol.LevelRed {
		t.Fatalf("zone level %s, want red (rule level merged into the zone)", lv)
	}
	als := alertsOf(a, protocol.KindRule)
	if len(als) != 1 || als[0].Level != protocol.LevelRed || als[0].Score < 4 || als[0].ID == "" {
		t.Fatalf("want one rule incident (yellow then red, same id): %+v", als)
	}
	a.mu.Lock()
	zl, lvl, zone := a.alertLevels(a.live)
	a.mu.Unlock()
	if zl["g"] != protocol.LevelRed || lvl != protocol.LevelRed || zone != "g" {
		t.Errorf("sign levels %v %s %s", zl, lvl, zone)
	}
	// Everyone leaves: the level clears, the card stays until resolved.
	for i := range pts {
		a.PhoneGone(fmt.Sprintf("p%d", i))
	}
	for end := now + 1000; now <= end; now += 250 {
		tick(a, now)
	}
	als = alertsOf(a, protocol.KindRule)
	if zoneLevelOf(a, "g") != protocol.LevelCalm || len(als) != 2 || als[0].Status != protocol.StatusOpen ||
		als[1].Level != protocol.LevelCalm || als[1].Status != protocol.StatusResolved {
		t.Fatalf("after leaving: %s %+v", zoneLevelOf(a, "g"), als)
	}
}

func TestCapacityRule(t *testing.T) {
	a := ruleApp(t, []protocol.Area{{ID: "gate", Name: "Gate", Poly: sq(2, 2, 12, 12),
		Rules: &protocol.AlertRules{MaxPhones: 3, Message: "Open gate 3 and hold the queue", Notify: &protocol.Notify{Sign: bp(false), Light: bp(false)}, Push: bp(false)}, Light: "A"},
		{ID: "bar", Name: "Bar", Poly: sq(14, 2, 20, 8)}})
	addPhones(a, "p", [][2]float64{{3, 3}, {6, 3}, {9, 3}, {3, 6}, {6, 6}})
	now := ft0
	for ; now <= ft0+3000; now += 250 {
		tick(a, now)
	}
	if lv := zoneLevelOf(a, "gate"); lv != protocol.LevelCalm {
		t.Fatalf("red after only 3 s: %s", lv)
	}
	for ; now <= ft0+3500; now += 250 {
		tick(a, now)
	}
	if lv := zoneLevelOf(a, "gate"); lv != protocol.LevelRed {
		t.Fatalf("5 phones > 3 for over 3 s: %s", lv)
	}
	a.mu.Lock()
	var nopush bool
	for _, z := range a.live.det.Zones() {
		if z.ID == "gate" {
			nopush = z.NoPush
		}
	}
	zl, lvl, _ := a.alertLevels(a.live)
	a.lightLevels(zl, []string{"A"})
	a.mu.Unlock()
	if !nopush {
		t.Error("push:false should turn wave detection off for the zone")
	}
	if lvl != protocol.LevelCalm || zl["A"] != protocol.LevelCalm {
		t.Errorf("notify sign/light off: sign %s, light %s", lvl, zl["A"])
	}
	// The staff message is the briefing's action (template here: no key).
	deadline := time.Now().Add(3 * time.Second)
	var al protocol.Alert
	for time.Now().Before(deadline) {
		if als := alertsOf(a, protocol.KindRule); len(als) == 1 && als[0].Brief != "" {
			al = als[0]
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if al.Action != "Open gate 3 and hold the queue" || !strings.Contains(al.Headline, "Gate") || !strings.Contains(al.Headline, "5 people") ||
		al.Brief != al.Headline+" "+al.Action || al.AudioURL != "" {
		t.Fatalf("rule briefing %+v", al)
	}
	a.PhoneGone("p3")
	a.PhoneGone("p4")
	tick(a, now)
	if lv := zoneLevelOf(a, "gate"); lv != protocol.LevelCalm {
		t.Fatalf("back under capacity: %s", lv)
	}
}

func TestRuleValidation(t *testing.T) {
	cfg := New(Options{}).liveConfig()
	poly := sq(1, 1, 3, 3)
	for name, r := range map[string]protocol.AlertRules{
		"density":  {Density: -1},
		"hold":     {Density: 3, DensityHoldS: -2},
		"phones":   {MaxPhones: -5},
		"message":  {Message: strings.Repeat("m", MaxRuleMessage+1)},
		"too high": {Density: 500},
	} {
		if _, err := validAreas([]protocol.Area{{ID: "a", Poly: poly, Rules: &r}}, cfg); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	got, err := validAreas([]protocol.Area{{ID: "a", Poly: poly, Rules: &protocol.AlertRules{Message: "  Open   gate  ", Density: 3.5}}}, cfg)
	if err != nil || got[0].Rules.Message != "Open gate" {
		t.Fatalf("%+v %v", got, err)
	}
}

// ---- alerts: ids, ack, resolve, escalation ----

func TestAlertIncidents(t *testing.T) {
	a, srv := testServer(t, Options{EscalateAfter: 2 * time.Second})
	info := func() brief.Info { return brief.Info{Zone: "A", Level: "red"} }
	a.mu.Lock()
	y, _ := a.raiseLocked("live", protocol.KindWave, "A", "calm", "yellow", 0.35, ft0, false, false, info)
	r, job := a.raiseLocked("live", protocol.KindWave, "A", "yellow", "red", 0.65, ft0+1000, false, false, info)
	back, _ := a.raiseLocked("live", protocol.KindWave, "A", "red", "yellow", 0.5, ft0+2000, false, false, info)
	calm, _ := a.raiseLocked("live", protocol.KindWave, "A", "yellow", "calm", 0.1, ft0+3000, false, false, info)
	again, _ := a.raiseLocked("live", protocol.KindWave, "A", "calm", "yellow", 0.4, ft0+4000, false, false, info)
	other, _ := a.raiseLocked("live", protocol.KindDensity, "A", "yellow", "red", 4.5, ft0+4000, false, false, info)
	sim, _ := a.raiseLocked("sim", protocol.KindWave, "A", "calm", "yellow", 0.3, ft0+4000, true, false, info)
	a.mu.Unlock()
	if y.ID == "" || y.Status != protocol.StatusOpen || r.ID != y.ID || r.Level != protocol.LevelRed || r.T != ft0+1000 || job == nil {
		t.Fatalf("yellow→red should update the incident and ask for a briefing: %+v %+v %v", y, r, job)
	}
	if back.ID != y.ID || back.Level != protocol.LevelRed || back.Score != 0.65 {
		t.Errorf("red→yellow keeps the worst level: %+v", back)
	}
	if calm.ID == y.ID || calm.Level != protocol.LevelCalm || calm.Status != protocol.StatusResolved {
		t.Errorf("calm is its own resolved notice: %+v", calm)
	}
	if again.ID != y.ID {
		t.Errorf("a new push before staff resolve lands on the same card")
	}
	if other.ID == y.ID || sim.ID == y.ID || other.ID == sim.ID {
		t.Errorf("kinds and data sources are separate incidents")
	}

	// Ack and resolve over HTTP; unknown ids are 404.
	if code := do(t, "POST", srv.URL+"/api/alerts/nope/ack", nil, nil); code != 404 {
		t.Errorf("unknown ack: %d", code)
	}
	if code := do(t, "POST", srv.URL+"/api/alerts/nope/resolve", nil, nil); code != 404 {
		t.Errorf("unknown resolve: %d", code)
	}
	var got protocol.Alert
	if code := do(t, "POST", srv.URL+"/api/alerts/"+y.ID+"/ack", nil, &got); code != 200 || got.Status != protocol.StatusAck || got.AckAt == 0 || got.ID != y.ID {
		t.Fatalf("ack: %d %+v", code, got)
	}
	// Acknowledged: never escalates. The open density incident does, once.
	a.escalate(ft0 + 1000 + 5000)
	waitFor(t, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		al, _ := a.updateAlertLocked(other.ID, func(*protocol.Alert, *incident) {})
		return al.Escalated
	})
	a.mu.Lock()
	acked, _ := a.updateAlertLocked(y.ID, func(*protocol.Alert, *incident) {})
	hold := a.signHold
	a.mu.Unlock()
	if acked.Escalated || hold == 0 {
		t.Errorf("acked alert escalated (%v) / sign not held red (%d)", acked.Escalated, hold)
	}
	a.mu.Lock()
	a.incidents[other.ID].escalating = false // even if asked again
	a.mu.Unlock()
	a.escalate(ft0 + 60_000)
	time.Sleep(50 * time.Millisecond)

	if code := do(t, "POST", srv.URL+"/api/alerts/"+y.ID+"/resolve", nil, &got); code != 200 || got.Status != protocol.StatusResolved || got.ResolvedAt == 0 {
		t.Fatalf("resolve: %d %+v", code, got)
	}
	a.mu.Lock()
	fresh, _ := a.raiseLocked("live", protocol.KindWave, "A", "calm", "yellow", 0.4, ft0+9000, false, false, info)
	a.mu.Unlock()
	if fresh.ID == y.ID {
		t.Error("after resolve, the next change opens a new incident")
	}
	// New dashboards get the log with statuses.
	var hist protocol.Alerts
	json.Unmarshal(a.DashWelcome()[0], &hist)
	st := map[string]string{}
	esc := 0
	for _, al := range hist.Alerts {
		st[al.ID] = al.Status
		if al.Escalated {
			esc++
		}
	}
	if st[y.ID] != protocol.StatusResolved || st[fresh.ID] != protocol.StatusOpen || esc != 1 {
		t.Errorf("history statuses %v, escalated %d", st, esc)
	}
}

// TestAlertBriefingUpdatesSameID: the test alert's briefing arrives as an
// update of the same alert, with headline and action.
func TestAlertBriefingUpdatesSameID(t *testing.T) {
	a := New(Options{Detect: detect.DefaultConfig(), RecordingsDir: t.TempDir()})
	a.TestAlert()
	waitFor(t, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return len(a.alerts) == 1 && a.alerts[0].Brief != ""
	})
	a.mu.Lock()
	al := a.alerts[0]
	a.mu.Unlock()
	if !al.Test || al.Headline == "" || al.Action == "" || al.Brief != al.Headline+" "+al.Action || al.Status != protocol.StatusOpen {
		t.Fatalf("%+v", al)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out")
}

// ---- boards: positions, beacons, peers ----

func TestHardwarePositionsAndPeers(t *testing.T) {
	board := func(name, peer string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"kind":"zone-light","level":"calm","rssi":-50,"uptime":9,"name":%q,"mac":"C72C","peers":[{"name":%q,"rssi":-63,"dist":2.44,"age":3},{"name":"PULSE-Z","rssi":-90,"dist":30,"age":1}]}`, name, peer)
		}))
	}
	la, lb := board("PULSE-A", "PULSE-B"), board("PULSE-B", "PULSE-A")
	defer la.Close()
	defer lb.Close()
	dir := t.TempDir()
	c := sign.New("http://127.0.0.1:1,A=" + la.URL + ",B=" + lb.URL)
	a, srv := testServer(t, Options{DataDir: dir, Sign: c})
	a.mu.Lock()
	a.hw = hardwareList(c.Probe(context.Background()), nil, nil, hub.Now())
	a.mu.Unlock()

	if code := do(t, "PUT", srv.URL+"/api/hardware/Z/pos", `{"x":1,"y":1}`, nil); code != 404 {
		t.Errorf("unknown board: %d", code)
	}
	if code := do(t, "PUT", srv.URL+"/api/hardware/A/pos", `{"x":1}`, nil); code != 400 {
		t.Errorf("missing y: %d", code)
	}
	var hw []protocol.Hardware
	do(t, "PUT", srv.URL+"/api/hardware/a/pos", `{"x":2,"y":3}`, &hw)
	if code := do(t, "PUT", srv.URL+"/api/hardware/B/pos", `{"x":5,"y":700}`, &hw); code != 200 {
		t.Fatalf("PUT B: %d", code)
	}
	if code := do(t, "PUT", srv.URL+"/api/hardware/sign/pos", `{"x":12,"y":1}`, &hw); code != 200 {
		t.Fatalf("PUT sign: %d", code)
	}
	by := map[string]protocol.Hardware{}
	for _, h := range hw {
		by[hwKey(h)] = h
	}
	A, B := by["A"], by["B"]
	if A.X == nil || *A.X != 2 || *A.Y != 3 || B.Y == nil || *B.Y != 16 || by["sign"].X == nil {
		t.Fatalf("positions (B clamped to the venue): %+v %+v", A, B)
	}
	if A.Beacon != "PULSE-A" || len(A.Peers) != 2 || A.Peers[0].Name != "PULSE-B" || A.Peers[0].RSSI != -63 || A.Peers[0].Dist != 2.44 {
		t.Fatalf("beacon/peers %+v", A)
	}
	// Both placed and hearing each other: the map distance sits next to the estimate.
	if d := A.Peers[0].MapDist; d == nil || *d != 13.34 {
		t.Errorf("mapDist A→B = %v, want 13.34", d)
	}
	if A.Peers[1].MapDist != nil {
		t.Error("no mapDist for a board that isn't on the map")
	}
	// Saved.
	b2 := New(Options{Detect: detect.DefaultConfig(), DataDir: dir, Sign: c})
	if p := b2.hwPos["A"]; p != (protocol.Point{2, 3}) {
		t.Errorf("reloaded positions %v", b2.hwPos)
	}
}
