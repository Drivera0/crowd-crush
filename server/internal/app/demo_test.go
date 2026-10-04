package app

import (
	"fmt"
	"math"
	"testing"

	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/sim"
)

const demoT0 = int64(1_700_000_000_000)

func demoApp(t testing.TB) *App {
	t.Helper()
	return New(Options{Detect: detect.DefaultConfig(), RecordingsDir: t.TempDir(), DataDir: t.TempDir()})
}

// lineRun lines n phones up at the demo spot and plays a scripted scenario
// (package sim, line layout) through the live pipeline on a fake clock.
type lineRun struct {
	maxLevel  string
	redAt     float64 // s, -1 = never
	waveSteps int
	alerts    []protocol.Alert
}

func runLine(t testing.TB, a *App, scenario string, n int, dur float64, seed int64) lineRun {
	t.Helper()
	sc, err := sim.NewLayout(scenario, n, seed, sim.LineLayout(1, n))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("p%d", i)
		a.PhoneHelloAuto(id, "test")
		a.PhoneSync(id, 0, 20)
	}
	out := lineRun{maxLevel: protocol.LevelCalm, redAt: -1}
	evs := sc.Generate(demoT0, dur)
	j := 0
	for now := demoT0; now <= demoT0+int64(dur*1000); now += 250 {
		for j < len(evs) && evs[j].T <= now {
			e := evs[j]
			a.PhoneMotion(fmt.Sprintf("p%d", e.Phone), protocol.Motion{T: e.T, AX: e.AX, AY: e.AY, AZ: e.AZ, Rot: e.Rot}, e.T)
			j++
		}
		a.detectTick(now)
		a.mu.Lock()
		if len(a.live.last.Waves()) > 0 {
			out.waveSteps++
		}
		for _, z := range a.live.last.Zones {
			if levelRank[z.Level] > levelRank[out.maxLevel] {
				out.maxLevel = z.Level
			}
			if z.Level == protocol.LevelRed && out.redAt < 0 {
				out.redAt = float64(now-demoT0) / 1000
			}
		}
		a.mu.Unlock()
	}
	a.mu.Lock()
	out.alerts = append(out.alerts, a.alerts...)
	a.mu.Unlock()
	return out
}

func demoOn(t testing.TB, a *App, x, y float64) {
	t.Helper()
	if _, err := a.SetDemo(protocol.DemoSpot{On: true, X: x, Y: y}, false); err != nil {
		t.Fatal(err)
	}
}

// TestDemoSpotPlacement: phones that join without a position line up at the
// spot 0.6 m apart in join order, a phone that leaves frees nothing while
// the server still knows it, a known phone keeps its place, and staff can
// move one.
func TestDemoSpotPlacement(t *testing.T) {
	a := demoApp(t)
	// Demo spot off: a phone without a position is not put anywhere.
	a.PhoneHelloAuto("x", "test")
	a.mu.Lock()
	if m := a.live.meta["x"]; m == nil || !m.unplaced {
		t.Fatalf("demo spot off: %+v, want an unplaced phone", m)
	}
	a.mu.Unlock()
	a.PhoneGone("x")
	demoOn(t, a, 6, 8)
	if d := a.Demo(); !d.On || d.Spacing != DemoSpacing {
		t.Fatalf("demo = %+v", d)
	}
	pos := func(id string) (float64, float64) {
		a.mu.Lock()
		defer a.mu.Unlock()
		m := a.live.meta[id]
		if m == nil {
			t.Fatalf("no phone %s", id)
		}
		return m.x, m.y
	}
	for i, id := range []string{"a", "b", "c"} {
		a.PhoneHelloAuto(id, "test")
		x, y := pos(id)
		if math.Abs(x-(6+0.6*float64(i))) > 1e-9 || y != 8 {
			t.Errorf("phone %s at %.2f, %.2f; want %.2f, 8", id, x, y, 6+0.6*float64(i))
		}
	}
	// Neighbours in the row are within the detector's neighbour radius.
	if DemoSpacing >= a.Config().NeighbourRadius {
		t.Errorf("spacing %.2f m is not inside the neighbour radius %.2f m", DemoSpacing, a.Config().NeighbourRadius)
	}
	// b reconnects without a position: it stays put.
	a.PhoneGone("b")
	a.PhoneHelloAuto("b", "test")
	if x, _ := pos("b"); math.Abs(x-6.6) > 1e-9 {
		t.Errorf("b moved to %.2f on reconnect", x)
	}
	// Staff drag c to the front; d joins and takes the free place c left.
	if _, err := a.MovePhone("c", 5.4, 8); err != nil {
		t.Fatal(err)
	}
	a.PhoneHelloAuto("d", "test")
	if x, _ := pos("d"); math.Abs(x-7.2) > 1e-9 {
		t.Errorf("d at %.2f, want the free place at 7.2", x)
	}
	if _, err := a.MovePhone("nobody", 1, 1); err != ErrNoPhone {
		t.Errorf("moving an unknown phone: %v", err)
	}
	// A phone that sends its own position is left where it says.
	a.PhoneHello("e", 20, 3, "test")
	if x, y := pos("e"); x != 20 || y != 3 {
		t.Errorf("e at %.1f, %.1f", x, y)
	}
	// Arrange lines everyone up again in join order.
	if _, err := a.SetDemo(protocol.DemoSpot{On: true, X: 2, Y: 4}, true); err != nil {
		t.Fatal(err)
	}
	seen := map[float64]bool{}
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		x, y := pos(id)
		if y != 4 || x < 2-1e-9 || x > 2+4*0.6+1e-9 || seen[math.Round(x*10)] {
			t.Errorf("after arrange %s at %.2f, %.2f", id, x, y)
		}
		seen[math.Round(x*10)] = true
	}
	// The row wraps at the venue's edge instead of stacking phones on it.
	b := demoApp(t)
	demoOn(t, b, 23.5, 15.9)
	b.PhoneHelloAuto("p", "test")
	b.PhoneHelloAuto("q", "test")
	b.mu.Lock()
	p, q := b.live.meta["p"], b.live.meta["q"]
	if math.Hypot(p.x-q.x, p.y-q.y) < 0.5 {
		t.Errorf("two phones on one spot at the venue edge: %.2f, %.2f", p.x, p.y)
	}
	b.mu.Unlock()
	if _, err := a.SetDemo(protocol.DemoSpot{On: true, X: 1, Y: 1, Spacing: 5}, false); err == nil {
		t.Error("spacing 5 m accepted")
	}
}

// TestPhoneNames: every live phone gets a name and colour, the same one
// when it reconnects, different from its neighbours'; simulated phones get
// none.
func TestPhoneNames(t *testing.T) {
	a := demoApp(t)
	names, colours := map[string]string{}, map[string]bool{}
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("judge-%d", i)
		a.PhoneHello(id, 3+float64(i), 5, "iPhone")
		a.mu.Lock()
		m := a.live.meta[id]
		a.mu.Unlock()
		if m.name == "" || m.color == "" {
			t.Fatalf("%s has no name", id)
		}
		for other, n := range names {
			if n == m.name {
				t.Fatalf("%s and %s are both %q", id, other, n)
			}
		}
		if colours[m.color] {
			t.Errorf("%s shares colour %s", id, m.color)
		}
		names[id], colours[m.color] = m.name, true
	}
	// Reconnect, even after the server forgot the phone: same name.
	a.PhoneGone("judge-3")
	a.mu.Lock()
	forget(a.live, demoT0*2)
	_, still := a.live.meta["judge-3"]
	a.mu.Unlock()
	if still {
		t.Fatal("phone not forgotten")
	}
	a.PhoneHello("judge-3", 6, 5, "iPhone")
	a.mu.Lock()
	got := a.live.meta["judge-3"].name
	snap := a.snapshotLocked(demoT0)
	a.mu.Unlock()
	if got != names["judge-3"] {
		t.Errorf("judge-3 came back as %q, was %q", got, names["judge-3"])
	}
	for _, n := range snap.Nodes {
		if n.Name != names[n.ID] || n.Color == "" {
			t.Errorf("node %s: name %q colour %q", n.ID, n.Name, n.Color)
		}
	}
	// The phone is told its name.
	a.PhoneSync("judge-0", 0, 20)
	a.detectTick(demoT0)
	a.mu.Lock()
	st := a.phoneStatesLocked()["judge-0"]
	a.mu.Unlock()
	if st.Name != names["judge-0"] || st.Color == "" || st.Sim {
		t.Errorf("state = %+v", st)
	}
}

// TestShake: shaking a phone flags its node at once, tells the phone, makes
// it "handling" and never raises an alert, even when the whole line shakes;
// a crowd push and jumping are not shakes.
func TestShake(t *testing.T) {
	a := demoApp(t)
	demoOn(t, a, 6, 8)
	ids := []string{"p0", "p1", "p2"}
	for _, id := range ids {
		a.PhoneHelloAuto(id, "test")
		a.PhoneSync(id, 0, 20)
	}
	node := func(now int64, id string) protocol.Node {
		a.mu.Lock()
		defer a.mu.Unlock()
		for _, n := range a.snapshotLocked(now).Nodes {
			if n.ID == id {
				return n
			}
		}
		t.Fatalf("no node %s", id)
		return protocol.Node{}
	}
	now := demoT0
	feed := func(dur int64, mo func(i int, t float64) protocol.Motion) {
		for end := now + dur; now < end; now += 100 {
			for i, id := range ids {
				m := mo(i, float64(now-demoT0)/1000)
				m.T = now
				a.PhoneMotion(id, m, now)
			}
			if now%250 == 0 {
				a.detectTick(now)
			}
		}
	}
	calm := func(int, float64) protocol.Motion { return protocol.Motion{AX: 0.02, Rot: 3} }
	feed(3000, calm)
	if n := node(now, "p1"); n.Shake || n.Status != protocol.StatusOK {
		t.Fatalf("calm phone: %+v", n)
	}
	// Only p1 shakes: ±12 m/s² side to side with the wrist turning, the
	// gyroscope below the handling threshold (the flag must not depend on it).
	shakeT := now
	feed(300, func(i int, tt float64) protocol.Motion {
		if i != 1 {
			return calm(i, tt)
		}
		return protocol.Motion{AX: 12 * math.Sin(2*math.Pi*3.1*tt+0.7), AZ: 6, Rot: 150}
	})
	if n := node(now, "p1"); !n.Shake {
		t.Errorf("p1 not flagged %d ms into the shake", now-shakeT)
	}
	if node(now, "p0").Shake || node(now, "p2").Shake {
		t.Error("a neighbour was flagged")
	}
	// Then everyone shakes hard for 20 s, each a little after the last (the
	// pattern of a travelling wave): still no alert, everyone is "handling".
	feed(20000, func(i int, tt float64) protocol.Motion {
		return protocol.Motion{AX: 9 * math.Sin(2*math.Pi*0.6*(tt-0.25*float64(i))), AZ: 5, Rot: 140}
	})
	for _, id := range ids {
		if n := node(now, id); !n.Shake || n.Status != protocol.StatusHandling {
			t.Errorf("%s while shaking: shake %v status %s", id, n.Shake, n.Status)
		}
	}
	a.mu.Lock()
	nAlerts := len(a.alerts)
	a.mu.Unlock()
	if nAlerts != 0 {
		t.Errorf("shaking raised %d alert(s): %+v", nAlerts, a.alerts)
	}
	// It clears soon after they stop.
	feed(3000, calm)
	if n := node(now, "p1"); n.Shake {
		t.Error("still flagged 3 s after the shake")
	}
	// A crowd push (a few m/s², hardly any rotation) and a jump landing
	// (hard, but one summary and no rotation) are not shakes.
	var s shakeState
	for i := int64(0); i < 50; i++ {
		s.add(protocol.Motion{AX: 3.5 * math.Sin(float64(i)), AZ: 1, Rot: 40}, demoT0+i*100)
		if i%5 == 0 {
			s.add(protocol.Motion{AY: 11, Rot: 30}, demoT0+i*100)
		}
	}
	if s.until != 0 {
		t.Error("a push or a jump counted as a shake")
	}
}

// TestThreePhoneLine: the line demo with only three phones at the demo
// spot. A push passed down the line makes wave edges and turns the zone
// red; everyone jumping or dancing together, standing or walking does not.
func TestThreePhoneLine(t *testing.T) {
	for _, tc := range []struct {
		scenario string
		red      bool
	}{
		{"wave", true}, {"dance", false}, {"calm", false}, {"walk", false}, {"jump-stagger", false}, {"handle", false},
	} {
		for seed := int64(1); seed <= 5; seed++ {
			a := demoApp(t)
			demoOn(t, a, 6, 8)
			r := runLine(t, a, tc.scenario, 3, 70, seed)
			t.Logf("%-12s seed %d: max %s, red at %.1f s, %d wave steps, %d alerts", tc.scenario, seed, r.maxLevel, r.redAt, r.waveSteps, len(r.alerts))
			if tc.red && r.redAt < 0 {
				t.Errorf("%s seed %d: three phones in a line never went red (max %s, %d wave steps)", tc.scenario, seed, r.maxLevel, r.waveSteps)
			}
			if !tc.red && r.maxLevel != protocol.LevelCalm {
				t.Errorf("%s seed %d: went %s", tc.scenario, seed, r.maxLevel)
			}
		}
	}
}

// TestReceipt: the receipt reports what the server holds, and says truthfully
// whether readings were stored.
func TestReceipt(t *testing.T) {
	a := demoApp(t)
	a.PhoneHello("0123456789abcdef", 3.26, 7.5, "iPhone")
	a.PhoneSync("0123456789abcdef", 0, 20)
	now := demoT0
	for i := 0; i < 25; i++ {
		a.PhoneMotion("0123456789abcdef", protocol.Motion{T: now, AX: 0.1}, now)
		now += 100
	}
	r, err := a.Receipt("0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != "01234567" || r.Name == "" || r.Messages != 25 || r.Kept != 25 || r.X != 3.3 || r.Y != 7.5 || r.Src != protocol.SrcManual {
		t.Errorf("receipt = %+v", r)
	}
	if r.Recorded || r.Stored {
		t.Errorf("nothing is stored in this test (no sink, no recording): %+v", r)
	}
	if _, err := a.StartRecording("receipt"); err != nil {
		t.Fatal(err)
	}
	a.PhoneMotion("0123456789abcdef", protocol.Motion{T: now, AX: 0.1}, now)
	a.StopRecording()
	if r, _ = a.Receipt("0123456789abcdef"); !r.Recorded || r.Messages != 26 {
		t.Errorf("after a recording: %+v", r)
	}
	if _, err := a.Receipt("nobody"); err != ErrNoPhone {
		t.Errorf("unknown phone: %v", err)
	}
}
