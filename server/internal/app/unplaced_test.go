package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// TestUnplacedPhonesCountForNothing: twenty phones that said hello without
// a position (GPS phones whose fix never gets good enough) are connected
// and streaming, but make no cluster, no neighbour pair, no zone score and
// no alert, are flagged unplaced for the dashboard and get no guidance.
// Before this they all sat on the default grid cell and read as a crush.
func TestUnplacedPhonesCountForNothing(t *testing.T) {
	a := demoApp(t)
	name, err := a.StartRecording("unplaced")
	if err != nil {
		t.Fatal(err)
	}
	const n = 20
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("gps-%02d", i)
		a.PhoneHelloAuto(id, "iPhone")
		a.PhoneSync(id, 0, 25)
		// A fix too rough to use (and the venue has no geo-anchor anyway).
		a.PhoneGPS(id, 49.2781, -122.9199, 80)
	}
	now := demoT0
	for ; now < demoT0+20_000; now += 100 {
		for i := 0; i < n; i++ {
			// Everyone swaying hard, each a little after the last: on one
			// spot this would be a crush with a wave through it.
			tt := float64(now-demoT0)/1000 - 0.25*float64(i)
			a.PhoneMotion(fmt.Sprintf("gps-%02d", i), protocol.Motion{T: now, AX: 1.5 * math.Sin(2*math.Pi*0.5*tt), Rot: 5}, now)
		}
		if now%250 == 0 {
			a.detectTick(now)
		}
	}
	a.mu.Lock()
	snap := a.snapshotLocked(now)
	states := a.phoneStatesLocked()
	nAlerts := len(a.alerts)
	a.mu.Unlock()
	if nAlerts != 0 || len(snap.Clusters) != 0 || len(snap.Links) != 0 || len(snap.Waves) != 0 {
		t.Errorf("unplaced phones raised %d alert(s), %d cluster(s), %d link(s), %d wave(s)", nAlerts, len(snap.Clusters), len(snap.Links), len(snap.Waves))
	}
	for _, z := range snap.Zones {
		if z.Level != protocol.LevelCalm || z.Score != 0 {
			t.Errorf("zone %s: %s, score %.2f", z.ID, z.Level, z.Score)
		}
	}
	if snap.Status == nil || snap.Status.Level != protocol.LevelCalm {
		t.Errorf("status = %+v", snap.Status)
	}
	if len(snap.Nodes) != n || snap.Stats.Phones != n {
		t.Fatalf("%d nodes, %d phones; want %d connected", len(snap.Nodes), snap.Stats.Phones, n)
	}
	for _, nd := range snap.Nodes {
		if !nd.Unplaced || nd.Zone != "" || nd.Name == "" {
			t.Errorf("node %+v: want unplaced, in no zone, named", nd)
		}
	}
	for id, st := range states {
		if st.Zone != protocol.LevelCalm || st.Move != nil {
			t.Errorf("%s: state %+v", id, st)
		}
	}
	if r, err := a.Receipt("gps-00"); err != nil || r.Src != "none" || r.Messages == 0 {
		t.Errorf("receipt = %+v, %v", r, err)
	}

	// A position places it: the phone's own tap, or staff dragging its dot.
	a.PhonePos("gps-00", 5, 5)
	if _, err := a.MovePhone("gps-01", 5.6, 5); err != nil {
		t.Fatal(err)
	}
	a.PhoneHello("gps-02", 6.2, 5, "iPhone")
	for end := now + 3000; now < end; now += 100 {
		for i := 0; i < 3; i++ {
			a.PhoneMotion(fmt.Sprintf("gps-%02d", i), protocol.Motion{T: now, AX: 0.02, Rot: 3}, now)
		}
		if now%250 == 0 {
			a.detectTick(now)
		}
	}
	a.mu.Lock()
	snap = a.snapshotLocked(now)
	a.mu.Unlock()
	placed := 0
	for _, nd := range snap.Nodes {
		if !nd.Unplaced {
			placed++
			if nd.Zone != "A" || nd.Y != 5 {
				t.Errorf("placed node %+v", nd)
			}
		}
	}
	if placed != 3 || len(snap.Links) != 2 {
		t.Errorf("%d placed phones, %d links; want 3 in a row with 2 links", placed, len(snap.Links))
	}

	// The recording never holds an unplaced phone: each one appears with
	// the hello of its first position, and only its readings from then on.
	a.StopRecording()
	f, err := os.Open(filepath.Join(a.opt.RecordingsDir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ids := map[string]int{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var rec struct {
			K, ID string
			X, Y  *float64
		}
		json.Unmarshal(sc.Bytes(), &rec)
		if rec.ID == "" {
			continue
		}
		if ids[rec.ID] == 0 && (rec.K != "hello" || rec.X == nil || rec.Y == nil) {
			t.Errorf("first record of %s is %q without a position", rec.ID, rec.K)
		}
		ids[rec.ID]++
	}
	if len(ids) != 3 {
		t.Errorf("recording holds %d phones, want the 3 that were placed: %v", len(ids), ids)
	}
}

// TestUnplacedOverWebSocket: over the real socket, a hello with no position
// is unplaced, while a legacy hello with row/col (cell 0, 0 included) and a
// hello with x/y are placed as before; with the demo spot on, the same
// position-less hello is lined up.
func TestUnplacedOverWebSocket(t *testing.T) {
	a := demoApp(t)
	mux := http.NewServeMux()
	a.Routes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hello := func(raw string) {
		t.Helper()
		ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/phone", nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ws.CloseNow() })
		if err := ws.Write(ctx, websocket.MessageText, []byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	meta := func(id string) nodeMeta {
		t.Helper()
		for i := 0; i < 200; i++ {
			a.mu.Lock()
			m := a.live.meta[id]
			a.mu.Unlock()
			if m != nil {
				a.mu.Lock()
				defer a.mu.Unlock()
				return *a.live.meta[id]
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("phone %s never joined", id)
		return nodeMeta{}
	}
	hello(`{"type":"hello","id":"nowhere","ua":"iPhone"}`)
	hello(`{"type":"hello","id":"legacy00","row":0,"col":0}`)
	hello(`{"type":"hello","id":"legacy03","row":0,"col":3}`)
	hello(`{"type":"hello","id":"tapped","x":3,"y":4}`)
	cfg := a.Config()
	if m := meta("nowhere"); !m.unplaced {
		t.Errorf("hello without a position: placed at %.1f, %.1f", m.x, m.y)
	}
	x0, y0 := cfg.LegacyPos(0, 0)
	if m := meta("legacy00"); m.unplaced || m.x != x0 || m.y != y0 {
		t.Errorf("legacy cell 0,0: %+v, want %.1f, %.1f", m, x0, y0)
	}
	x3, _ := cfg.LegacyPos(0, 3)
	if m := meta("legacy03"); m.unplaced || m.x != x3 {
		t.Errorf("legacy cell 0,3 at %.1f, want %.1f", m.x, x3)
	}
	if m := meta("tapped"); m.unplaced || m.x != 3 || m.y != 4 {
		t.Errorf("hello with x/y: %+v", m)
	}
	demoOn(t, a, 10, 9)
	hello(`{"type":"hello","id":"judge","ua":"Android"}`)
	if m := meta("judge"); m.unplaced || m.x != 10 || m.y != 9 {
		t.Errorf("demo spot on: %+v", m)
	}
	// Lining phones up places the one that was nowhere.
	if _, err := a.SetDemo(protocol.DemoSpot{On: true, X: 10, Y: 9}, true); err != nil {
		t.Fatal(err)
	}
	if m := meta("nowhere"); m.unplaced || m.y != 9 {
		t.Errorf("after lining up: %+v", m)
	}
}

// TestSimGPSPhonesStartUnplaced: with realism on, simulated phones locate
// themselves by GPS, so each is unplaced from its hello until its first
// usable fix, and none of them is ever at the default grid cell.
func TestSimGPSPhonesStartUnplaced(t *testing.T) {
	a := demoApp(t)
	if err := a.startSimAt(SimStart{People: 150, Participation: 0.6, Seed: 11, Realism: "harsh"}, simT0); err != nil {
		t.Fatal(err)
	}
	lx, ly := a.Config().LegacyPos(0, 0)
	sawUnplaced := false
	for now := simT0 + 50; now <= simT0+12_000; now += 50 {
		a.simTick(now)
		if (now-simT0)%250 != 0 {
			continue
		}
		a.detectTick(now)
		a.mu.Lock()
		stacked := 0
		for _, m := range a.sim.p.meta {
			if m.unplaced {
				sawUnplaced = true
			} else if math.Hypot(m.x-lx, m.y-ly) < 0.05 {
				stacked++
			}
		}
		reds := 0
		for _, al := range a.alerts {
			if al.Level == protocol.LevelRed {
				reds++
			}
		}
		a.mu.Unlock()
		if stacked > 1 {
			t.Fatalf("%.1f s: %d simulated phones stacked on the default cell", float64(now-simT0)/1000, stacked)
		}
		if reds > 0 {
			t.Fatalf("%.1f s: a calm simulated crowd with messy phones went red", float64(now-simT0)/1000)
		}
	}
	if !sawUnplaced {
		t.Error("no simulated phone was ever unplaced: GPS phones should start that way")
	}
	a.mu.Lock()
	placed := 0
	for _, m := range a.sim.p.meta {
		if !m.unplaced {
			placed++
		}
	}
	a.mu.Unlock()
	if placed == 0 {
		t.Error("no simulated phone got a usable fix in 12 s")
	}
}
