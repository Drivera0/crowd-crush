package app

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/hub"
	"github.com/Drivera0/crowd-crush/server/internal/locate"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/sign"
)

// rowOf is every lined-up phone's place in the row, and the "go back to"
// place of every phone off it, as the phones are told.
func rowOf(t *testing.T, a *App) (rows map[string]int, spots map[string]int) {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	rows = map[string]int{}
	r := a.demoRowsLocked()
	for id, x := range r {
		rows[id] = x.N
	}
	return rows, a.demoSpotsLocked(r)
}

func lineUp(t *testing.T, a *App, ids ...string) {
	t.Helper()
	for _, id := range ids {
		a.PhoneHelloAuto(id, "test")
		a.PhoneSync(id, 0, 20)
	}
}

// TestTapMoveAndBack: a phone in the row taps a new spot: it goes there,
// loses its number, its place is kept for it (a new phone doesn't take it)
// and the estimator leaves the tapped spot alone; "back to my place" puts
// it back on its number.
func TestTapMoveAndBack(t *testing.T) {
	a := demoApp(t)
	demoOn(t, a, 6, 8)
	lineUp(t, a, "a", "b", "c")
	a.PhonePos("b", 10, 12)
	m := towerMeta(t, a, "b")
	if m.x != 10 || m.y != 12 || m.pinned || m.dm.slot != 2 || !m.dm.tapped {
		t.Fatalf("after the tap: %.2f, %.2f pinned %v kept %d tapped %v", m.x, m.y, m.pinned, m.dm.slot, m.dm.tapped)
	}
	rows, spots := rowOf(t, a)
	if rows["a"] != 1 || rows["c"] != 3 || rows["b"] != 0 || spots["b"] != 2 {
		t.Errorf("rows %v spots %v", rows, spots)
	}
	// The place b left is kept: d joins after c.
	lineUp(t, a, "d")
	if m := towerMeta(t, a, "d"); math.Abs(m.x-7.8) > 1e-9 || m.y != 8 {
		t.Errorf("d joined at %.2f, %.2f, want 7.8, 8 (b's place is kept)", m.x, m.y)
	}
	// The estimator doesn't move the tapped spot, nor the lined-up phones.
	a.mu.Lock()
	held := a.heldLocked(a.live.meta["b"])
	a.mu.Unlock()
	if !held {
		t.Fatal("a tapped spot isn't held")
	}
	estPull(t, a, "b", 3, 3)
	if m := towerMeta(t, a, "b"); m.x != 10 || m.y != 12 {
		t.Errorf("estimator moved the tapped phone to %.2f, %.2f", m.x, m.y)
	}
	// Tapping again moves it again; its kept place stays 2.
	a.PhonePos("b", 9, 12)
	if m := towerMeta(t, a, "b"); m.x != 9 || m.dm.slot != 2 {
		t.Errorf("second tap: %.2f kept %d", m.x, m.dm.slot)
	}
	// Back to the row.
	res, err := a.DemoBack("b")
	if err != nil || res.N != 2 || math.Abs(res.X-6.6) > 1e-9 || res.Y != 8 {
		t.Fatalf("back: %+v %v", res, err)
	}
	m = towerMeta(t, a, "b")
	if !m.pinned || m.dm != (demoMove{}) || math.Abs(m.x-6.6) > 1e-9 {
		t.Errorf("after back: pinned %v dm %+v at %.2f", m.pinned, m.dm, m.x)
	}
	if rows, _ := rowOf(t, a); rows["b"] != 2 || rows["d"] != 4 {
		t.Errorf("rows after back %v", rows)
	}
	// Somebody took the kept place meanwhile (staff dragged them there): back
	// goes to the first free place.
	a.PhonePos("a", 2, 2)
	if _, err := a.MovePhone("d", 6, 8); err != nil {
		t.Fatal(err)
	}
	if res, _ := a.DemoBack("a"); res.N != 4 {
		t.Errorf("a back on %d, want the free place 4", res.N)
	}
	if _, err := a.DemoBack("nobody"); err != ErrNoPhone {
		t.Errorf("unknown phone: %v", err)
	}
	// With the demo spot off a tap is an ordinary placement, and there is no row.
	if _, err := a.SetDemo(protocol.DemoSpot{On: false, X: 6, Y: 8}, false); err != nil {
		t.Fatal(err)
	}
	a.PhonePos("c", 15, 3)
	if m := towerMeta(t, a, "c"); m.dm.tapped || m.dm.slot != 0 {
		t.Errorf("tap with the demo spot off: %+v", m.dm)
	}
	if _, err := a.DemoBack("c"); err != ErrDemoOff {
		t.Errorf("back with the demo spot off: %v", err)
	}
}

// estPull makes the position estimator want phone id at (x, y) (as if its
// steps said it walked there), then runs a detector step.
func estPull(t *testing.T, a *App, id string, x, y float64) {
	t.Helper()
	a.mu.Lock()
	l := a.live.loc
	if l == nil {
		a.mu.Unlock()
		t.Fatal("estimator off")
	}
	l.est.Fix(id, hub.Now(), locate.Fix{X: x, Y: y, Exact: true})
	a.mu.Unlock()
	a.detectTick(hub.Now())
}

// The control for estPull: a phone placed by hand with the demo spot off
// is the estimator's to move, so the test above means something.
func TestEstPullMovesFreePhones(t *testing.T) {
	a := demoApp(t)
	a.PhoneHello("free", 10, 12, "test")
	estPull(t, a, "free", 3, 3)
	if m := towerMeta(t, a, "free"); m.x == 10 && m.y == 12 {
		t.Error("estPull didn't move a free phone: the held tests prove nothing")
	}
}

// TestDemoSwap: staff drop one phone's dot on another's place in the row and
// the two swap; dropped on a free place it is lined up there.
func TestDemoSwap(t *testing.T) {
	a := demoApp(t)
	demoOn(t, a, 6, 8)
	lineUp(t, a, "a", "b", "c")
	nameC := towerMeta(t, a, "c").name
	d, err := a.MovePhone("a", 7.25, 8.05) // dropped on c (place 3), a little off its centre
	if err != nil || d.Swapped != nameC || d.X != 7.2 || d.Y != 8 {
		t.Fatalf("swap: %+v %v (want swapped with %q)", d, err, nameC)
	}
	rows, _ := rowOf(t, a)
	if rows["a"] != 3 || rows["c"] != 1 || rows["b"] != 2 {
		t.Errorf("rows after the swap %v", rows)
	}
	// Over HTTP, the same endpoint as a drag.
	mux := http.NewServeMux()
	a.Routes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/node/b/pos", strings.NewReader(`{"x":6,"y":8}`))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var nd protocol.NodeDetail
	_ = json.NewDecoder(res.Body).Decode(&nd)
	res.Body.Close()
	if res.StatusCode != 200 || nd.Swapped == "" {
		t.Errorf("PUT pos onto c: %d %+v", res.StatusCode, nd)
	}
	if rows, _ := rowOf(t, a); rows["b"] != 1 || rows["c"] != 2 || rows["a"] != 3 {
		t.Errorf("rows after the HTTP swap %v", rows)
	}
	// Dropped on a free place: lined up there, no swap.
	if d, _ := a.MovePhone("b", 8.4, 8); d.Swapped != "" {
		t.Errorf("free place: swapped %q", d.Swapped)
	}
	if rows, _ := rowOf(t, a); rows["b"] != 5 {
		t.Errorf("b on %d, want 5", rows["b"])
	}
	// A phone that tapped away keeps its place; dropping another phone there
	// moves its kept place to the dropped phone's old one.
	a.PhonePos("c", 12, 12)
	if _, err := a.MovePhone("a", 6.6, 8); err != nil { // onto c's kept place 2
		t.Fatal(err)
	}
	if m := towerMeta(t, a, "c"); m.dm.slot != 3 {
		t.Errorf("c keeps place %d, want a's old place 3", m.dm.slot)
	}
	// Dropped anywhere else: as before, the pin stays and the place is free.
	if _, err := a.MovePhone("a", 15, 3); err != nil {
		t.Fatal(err)
	}
	if m := towerMeta(t, a, "a"); !m.pinned || m.x != 15 || m.dm.slot != 0 {
		t.Errorf("dropped off the row: pinned %v at %.1f kept %d", m.pinned, m.x, m.dm.slot)
	}
}

// snapApp: zone lights A and B on the map 1.5 m apart (as the table demo
// lays them out), the demo spot on in front of them.
func snapApp(t *testing.T, ax, bx float64) *App {
	t.Helper()
	a := New(Options{Detect: detect.DefaultConfig(), RecordingsDir: t.TempDir(), DataDir: t.TempDir(),
		Sign: sign.New("A=http://127.0.0.1:9,B=http://127.0.0.1:9")})
	for k, x := range map[string]float64{"A": ax, "B": bx} {
		if _, err := a.SetHardwarePos(k, x, 8); err != nil {
			t.Fatal(err)
		}
	}
	demoOn(t, a, 4.5, 9)
	return a
}

// snapSource feeds what a phone at (px, py) measures to boards A and B at
// time now, by one of the three ways a range reaches the server.
type snapSource func(a *App, id string, px, py float64, now int64, bs ...board)

func viaScan(a *App, id string, px, py float64, now int64, bs ...board) {
	a.mu.Lock()
	defer a.mu.Unlock()
	m := a.live.meta[id]
	m.bcn.scan, m.bcn.scanAt = seenAt(px, py, bs...), now
	a.beaconUpdateLocked(id, m, now)
}

func viaLinks(heard bool) snapSource {
	return func(a *App, id string, px, py float64, now int64, bs ...board) {
		a.mu.Lock()
		defer a.mu.Unlock()
		for _, b := range bs {
			l := protocol.BeaconLink{ID: id, RSSI: math.Round(rssiAt(px, py, b.x, b.y, 0)), Age: 0}
			got := boardLinks{Links: []protocol.BeaconLink{l}}
			if heard {
				l.ID = id[:8]
				got = boardLinks{Heard: []protocol.BeaconLink{l}}
			}
			a.beaconLinksLocked(strings.TrimPrefix(b.name, "PULSE-"), b.name, got, true, now)
		}
		a.linksSettleLocked(now)
	}
}

// TestBeaconSnap: a phone walked right up to a board is put beside it after
// a few seconds of agreement, stays while it lingers within the hysteresis,
// and goes back to its place in the row once no board is near for 5 s. The
// same for each source of ranges.
func TestBeaconSnap(t *testing.T) {
	for name, feed := range map[string]snapSource{"scan": viaScan, "conn": viaLinks(false), "adv": viaLinks(true)} {
		t.Run(name, func(t *testing.T) {
			a := snapApp(t, 4.5, 6)
			A, B := board{"PULSE-A", 4.5, 8}, board{"PULSE-B", 6, 8}
			const id = "1a2b3c4d-0000-4000-8000-000000000001"
			lineUp(t, a, "x", id) // id is #2 at 5.1, 9
			t0 := hub.Now()
			// At its place in the row (about 1.2 m from A): nothing happens.
			for i := 0; i < 4; i++ {
				feed(a, id, 5.1, 9, t0+int64(i)*1000, A, B)
			}
			if m := towerMeta(t, a, id); !m.pinned || m.dm.snap.key != "" {
				t.Fatalf("snapped from its place in the row: %+v", m.dm.snap)
			}
			// Walks up to A (15 cm away): two reports aren't enough, the third is.
			t1 := t0 + 10_000
			feed(a, id, 4.6, 8.1, t1, A, B)
			feed(a, id, 4.6, 8.1, t1+1000, A, B)
			if m := towerMeta(t, a, id); m.dm.snap.key != "" {
				t.Fatal("snapped after 1 s")
			}
			feed(a, id, 4.6, 8.1, t1+2000, A, B)
			m := towerMeta(t, a, id)
			if d := math.Hypot(m.x-4.5, m.y-8); m.dm.snap.key != "A" || m.src() != protocol.SrcBeacon || math.Abs(d-snapSpotM) > 0.01 || m.pinned {
				t.Fatalf("after 2 s beside A: key %q src %s %.2f m from A pinned %v", m.dm.snap.key, m.src(), d, m.pinned)
			}
			rows, spots := rowOf(t, a)
			if rows[id] != 0 || spots[id] != 2 {
				t.Errorf("beside A: row %v spots %v", rows, spots)
			}
			a.detectTick(t1 + 2000)
			a.mu.Lock()
			st := a.phoneStatesLocked()[id]
			snap := a.snapshotLocked(t1 + 2000)
			a.mu.Unlock()
			if st.Near != "Zone light A" || st.Spot != 2 || st.Row != nil {
				t.Errorf("state beside A: near %q spot %d row %v", st.Near, st.Spot, st.Row)
			}
			for _, n := range snap.Nodes {
				if n.ID == id && (n.Near != "Zone light A" || n.Src != protocol.SrcBeacon) {
					t.Errorf("node beside A: %+v", n)
				}
			}
			// The estimator leaves it there.
			estPull(t, a, id, 10, 10)
			if m2 := towerMeta(t, a, id); m2.x != m.x || m2.y != m.y {
				t.Errorf("estimator moved the snapped phone to %.2f, %.2f", m2.x, m2.y)
			}
			// Lingering 0.9 m away (inside the hysteresis) for 8 s: still beside A.
			for i := 1; i <= 8; i++ {
				now := t1 + 2000 + int64(i)*1000
				feed(a, id, 4.5, 8.9, now, A, B)
				a.mu.Lock()
				a.snapTickLocked(now)
				a.mu.Unlock()
			}
			if m := towerMeta(t, a, id); m.dm.snap.key != "A" {
				t.Fatal("left A while within 1.2 m of it")
			}
			// Walks away (3 m): back on its place in the row 5 s later, not sooner.
			t2 := t1 + 11_000
			for i := 0; i <= 6; i++ {
				now := t2 + int64(i)*1000
				feed(a, id, 4.5, 11, now, A, B)
				a.mu.Lock()
				a.snapTickLocked(now)
				a.mu.Unlock()
				m := towerMeta(t, a, id)
				if i <= 4 && m.dm.snap.key != "A" {
					t.Fatalf("left A after only %d s", i)
				}
			}
			m = towerMeta(t, a, id)
			if m.dm.snap.key != "" || !m.pinned || math.Abs(m.x-5.1) > 1e-9 || m.y != 9 || m.src() != protocol.SrcManual {
				t.Errorf("after walking away: snap %q pinned %v at %.2f, %.2f src %s", m.dm.snap.key, m.pinned, m.x, m.y, m.src())
			}
			if rows, _ := rowOf(t, a); rows[id] != 2 {
				t.Errorf("back in the row at %d, want 2", rows[id])
			}
		})
	}
}

// TestBeaconSnapEdges: boards too close together can't be told apart; a
// phone that stops reporting goes back; switching boards needs agreement
// too; a tapped spot comes back after a snap.
func TestBeaconSnapEdges(t *testing.T) {
	// Boards 0.3 m apart (side by side on a table): no snap.
	a := snapApp(t, 4.5, 4.8)
	A, B := board{"PULSE-A", 4.5, 8}, board{"PULSE-B", 4.8, 8}
	lineUp(t, a, "p")
	t0 := hub.Now()
	for i := 0; i < 6; i++ {
		viaScan(a, "p", 4.5, 8.15, t0+int64(i)*1000, A, B)
	}
	if m := towerMeta(t, a, "p"); m.dm.snap.key != "" {
		t.Errorf("snapped to %q with the boards 30 cm apart", m.dm.snap.key)
	}

	// Boards 1.5 m apart.
	a = snapApp(t, 4.5, 6)
	A, B = board{"PULSE-A", 4.5, 8}, board{"PULSE-B", 6, 8}
	lineUp(t, a, "p")
	a.PhonePos("p", 10, 10) // tapped a spot first
	for i := 0; i < 3; i++ {
		viaScan(a, "p", 4.5, 8.1, t0+int64(i)*1000, A, B)
	}
	if m := towerMeta(t, a, "p"); m.dm.snap.key != "A" {
		t.Fatal("no snap to A")
	}
	// A gap of 3 s between agreeing reports starts the count over.
	t1 := t0 + 3000
	viaScan(a, "p", 6, 8.1, t1, A, B)
	viaScan(a, "p", 6, 8.1, t1+3500, A, B)
	viaScan(a, "p", 6, 8.1, t1+4500, A, B)
	if m := towerMeta(t, a, "p"); m.dm.snap.key == "B" {
		t.Fatal("switched to B without 3 agreeing reports in a row")
	}
	viaScan(a, "p", 6, 8.1, t1+5500, A, B)
	if m := towerMeta(t, a, "p"); m.dm.snap.key != "B" || math.Hypot(m.x-6, m.y-8) > snapSpotM+0.01 {
		t.Fatalf("after 3 agreeing reports beside B: %q at %.2f, %.2f", m.dm.snap.key, m.x, m.y)
	}
	// Stops reporting: back to the tapped spot after 5 s (the tick).
	a.mu.Lock()
	a.snapTickLocked(t1 + 5500 + 4000)
	a.mu.Unlock()
	if m := towerMeta(t, a, "p"); m.dm.snap.key != "B" {
		t.Fatal("left B after 4 s of silence")
	}
	a.mu.Lock()
	a.snapTickLocked(t1 + 5500 + 5100)
	a.mu.Unlock()
	m := towerMeta(t, a, "p")
	if m.dm.snap.key != "" || m.x != 10 || m.y != 10 || !m.dm.tapped || m.pinned {
		t.Errorf("after silence: snap %q at %.2f, %.2f tapped %v pinned %v", m.dm.snap.key, m.x, m.y, m.dm.tapped, m.pinned)
	}
	// Staff dragging a snapped phone ends the snap.
	for i := 0; i < 3; i++ {
		viaScan(a, "p", 4.5, 8.1, t1+20_000+int64(i)*1000, A, B)
	}
	// Off the row's slots by a clear margin: (12, 3) is exactly half a
	// spacing from one, which rounds either way depending on the CPU.
	if _, err := a.MovePhone("p", 12, 3.3); err != nil {
		t.Fatal(err)
	}
	if m := towerMeta(t, a, "p"); m.dm.snap.key != "" || m.x != 12 || m.src() != protocol.SrcManual {
		t.Errorf("after a staff drag: %+v at %.1f src %s", m.dm.snap, m.x, m.src())
	}
}

// TestTableDemoOneZone: after "Set up table demo" the four phones that join
// are all in one zone (the table profile and the wave chain work per zone),
// the Bluetooth boards are at least 1.5 m apart, and that zone's light is
// the one a push or a drill there lights. Also on a 30 × 22 m venue.
func TestTableDemoOneZone(t *testing.T) {
	for _, size := range [][2]float64{{24, 16}, {30, 22}} {
		s := sign.New("http://127.0.0.1:1,A=http://127.0.0.1:1,B=http://127.0.0.1:1")
		a := New(Options{Detect: detect.DefaultConfig(), RecordingsDir: t.TempDir(), DataDir: t.TempDir(), Sign: s})
		if _, err := a.SetVenue(protocol.Venue{W: size[0], H: size[1]}); err != nil {
			t.Fatal(err)
		}
		res, err := a.TableDemo(nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		lineUp(t, a, "p1", "p2", "p3", "p4")
		a.mu.Lock()
		zone := ""
		for _, id := range []string{"p1", "p2", "p3", "p4"} {
			m := a.live.meta[id]
			z := a.live.det.ZoneOf(m.x, m.y)
			if zone == "" {
				zone = z
			}
			if z != zone {
				t.Errorf("%v: %s at %.1f, %.1f is in zone %q, p1 in %q", size, id, m.x, m.y, z, zone)
			}
		}
		// Every spot beside a board is in that zone too.
		for _, k := range []string{"A", "B", "sign"} {
			p := a.hwPos[k]
			x, y := a.snapSpot("p1", k, p[0], p[1])
			if z := a.live.det.ZoneOf(x, y); z != zone {
				t.Errorf("%v: beside %s is zone %q", size, k, z)
			}
		}
		beacons := []string{"A", "sign", "B"}
		for i := range beacons {
			for j := i + 1; j < len(beacons); j++ {
				p, q := a.hwPos[beacons[i]], a.hwPos[beacons[j]]
				if d := math.Hypot(p[0]-q[0], p[1]-q[1]); d < tableBeaconGap-1e-9 {
					t.Errorf("%v: %s and %s %.2f m apart", size, beacons[i], beacons[j], d)
				}
			}
		}
		if a.hwPos["A"][0] >= a.hwPos["B"][0] {
			t.Errorf("A not left of B: %v %v", a.hwPos["A"], a.hwPos["B"])
		}
		light := a.lightFor(zone)
		a.mu.Unlock()
		if zone != "A" || light != "A" {
			t.Errorf("%v: table in zone %q lit by %q, want A, A", size, zone, light)
		}
		if len(res.Notes) == 0 || !strings.Contains(strings.Join(res.Notes, " "), "push lights zone light A") {
			t.Errorf("%v: notes %q", size, res.Notes)
		}
	}
}

// TestTableDemoPushOneZone: a push along the four phones at the table makes
// only zone A go up, so light A is the one that lights.
func TestTableDemoPushOneZone(t *testing.T) {
	s := sign.New("A=http://127.0.0.1:1,B=http://127.0.0.1:1")
	a := New(Options{Detect: detect.DefaultConfig(), RecordingsDir: t.TempDir(), DataDir: t.TempDir(), Sign: s})
	if _, err := a.TableDemo(nil, nil); err != nil {
		t.Fatal(err)
	}
	r := runLine(t, a, "wave", 4, 40, 1)
	if r.maxLevel != protocol.LevelRed {
		t.Errorf("a push along the table row only reached %s", r.maxLevel)
	}
	for _, al := range r.alerts {
		if al.Zone != "A" {
			t.Errorf("alert in zone %s: %+v", al.Zone, al)
		}
	}
	t.Logf("table push: max %s, red at %.1f s, %d alerts", r.maxLevel, r.redAt, len(r.alerts))
}
