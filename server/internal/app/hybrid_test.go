package app

import (
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// hybridRun drives an App on a fake clock as the Run loop does: real
// phones streaming calm motion at 10 Hz, hybridSync + physics every 50 ms,
// the detector every 250 ms.
type hybridRun struct {
	t    testing.TB
	a    *App
	now  int64
	real []string
}

func newHybridRun(t testing.TB, phones int, x, y float64) *hybridRun {
	t.Helper()
	r := &hybridRun{t: t, a: demoApp(t), now: simT0}
	if phones > 0 {
		demoOn(t, r.a, x, y)
	}
	for i := 0; i < phones; i++ {
		id := fmt.Sprintf("judge-%d", i)
		r.a.PhoneHelloAuto(id, "iPhone")
		r.a.PhoneSync(id, 0, 30)
		r.real = append(r.real, id)
	}
	return r
}

func (r *hybridRun) sec() float64 { return float64(r.now-simT0) / 1000 }

func (r *hybridRun) until(end float64, every func()) {
	for r.sec() < end-1e-9 {
		r.now += 50
		if r.now%100 == 0 {
			for i, id := range r.real {
				// A phone held by someone standing still.
				r.a.PhoneMotion(id, protocol.Motion{T: r.now, AX: 0.03 * math.Sin(float64(r.now)/700+float64(i)), AZ: 0.02, Rot: 4}, r.now)
			}
		}
		r.a.hybridSync(r.now)
		r.a.simTick(r.now)
		if (r.now-simT0)%int64(DetectEvery/1e6) == 0 {
			r.a.detectTick(r.now)
			if every != nil {
				every()
			}
		}
	}
}

func (r *hybridRun) states() map[string]protocol.PhoneState {
	r.a.mu.Lock()
	defer r.a.mu.Unlock()
	return r.a.phoneStatesLocked()
}

// TestHybridSurge: real phones standing in a simulated crowd. "Surge around
// the phones" starts the simulation and packs it around them; each real
// phone is a node of the sim pipeline and a pinned body, its state comes
// from the simulation (sim: true), goes red and carries a move. When the
// simulation stops, the phones are back on the live pipeline: calm, no
// move, sim false.
func TestHybridSurge(t *testing.T) {
	r := newHybridRun(t, 4, 11, 9)
	r.until(2, nil)
	for id, st := range r.states() {
		if st.Sim || st.Zone != protocol.LevelCalm || st.Move != nil {
			t.Fatalf("before the sim, %s: %+v", id, st)
		}
	}
	res, err := r.a.surgePhonesAt(r.now, 7)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Started || res.Phones != 4 || math.Abs(res.X-11.9) > 0.01 || res.Y != 9 {
		t.Fatalf("surge = %+v", res)
	}
	redAt, moveAt := map[string]float64{}, map[string]float64{}
	t0 := r.sec()
	r.until(t0+45, func() {
		for id, st := range r.states() {
			if !st.Sim {
				t.Fatalf("%.1f s: %s's state is not from the simulation: %+v", r.sec(), id, st)
			}
			if _, ok := redAt[id]; !ok && st.Zone == protocol.LevelRed {
				redAt[id] = r.sec() - t0
			}
			if _, ok := moveAt[id]; !ok && st.Move != nil {
				moveAt[id] = r.sec() - t0
				if l := math.Hypot(st.Move.DX, st.Move.DY); math.Abs(l-1) > 0.02 {
					t.Errorf("%s: move is not a unit vector: %+v", id, st.Move)
				}
			}
		}
	})
	t.Logf("red after %v s, move after %v s", redAt, moveAt)
	for _, id := range r.real {
		if _, ok := redAt[id]; !ok {
			t.Errorf("%s never went red inside the simulated crush", id)
		}
		if _, ok := moveAt[id]; !ok {
			t.Errorf("%s never got a move", id)
		}
	}
	// On the dashboard: real nodes among the simulated ones, named.
	r.a.mu.Lock()
	snap := r.a.snapshotLocked(r.now)
	pins := 0
	r.a.mu.Unlock()
	r.a.sim.mu.Lock()
	pins = len(r.a.sim.w.Pins())
	pressed := 0.0
	for _, p := range r.a.sim.w.Pins() {
		pressed = math.Max(pressed, p.Pressure())
	}
	r.a.sim.mu.Unlock()
	real, simulated := 0, 0
	for _, n := range snap.Nodes {
		if n.Real {
			real++
			if n.Name == "" || n.Color == "" || n.UA != "iPhone" || n.Status == protocol.StatusStale || n.Status == protocol.StatusConnecting {
				t.Errorf("real node %+v", n)
			}
		} else {
			simulated++
			if n.Name != "" {
				t.Errorf("simulated phone has a name: %+v", n)
			}
		}
	}
	if snap.Mode != "sim" || real != 4 || simulated == 0 || pins != 4 {
		t.Errorf("mode %s, %d real nodes, %d simulated, %d pinned bodies", snap.Mode, real, simulated, pins)
	}
	t.Logf("highest pressure on a real person: %.0f N/m", pressed)
	// The whole chain fired: a red incident in the sim, with a briefing asked for.
	reds := 0
	r.a.mu.Lock()
	for _, al := range r.a.alerts {
		if al.Level == protocol.LevelRed && !al.Test {
			reds++
			t.Logf("alert: %s %s red at %.1f s", al.Kind, al.Zone, float64(al.T-simT0)/1000-t0)
		}
	}
	r.a.mu.Unlock()
	if reds == 0 {
		t.Error("no red alert")
	}

	if err := r.a.StopSim(); err != nil {
		t.Fatal(err)
	}
	r.until(r.sec()+1, nil)
	st := r.states()
	if len(st) != 4 {
		t.Fatalf("%d states after the sim", len(st))
	}
	for id, s := range st {
		if s.Sim || s.Zone != protocol.LevelCalm || s.Move != nil || s.Name == "" {
			t.Errorf("after the sim, %s: %+v", id, s)
		}
	}
	r.a.mu.Lock()
	if r.a.surge != nil {
		t.Error("the surge director outlived the simulation")
	}
	r.a.mu.Unlock()
}

// TestHybridSurgeSpots: the surge reaches red wherever the judges stand
// (mid-floor, by a side wall, near the stage, at the back) and for three to
// five phones.
func TestHybridSurgeSpots(t *testing.T) {
	t.Parallel()
	for i, c := range []struct {
		x, y   float64
		phones int
	}{{11, 9, 3}, {2, 8, 4}, {10, 4, 5}, {19, 13, 3}, {6, 11, 1}} {
		r := newHybridRun(t, c.phones, c.x, c.y)
		r.until(1, nil)
		if _, err := r.a.surgePhonesAt(r.now, int64(100+i)); err != nil {
			t.Fatal(err)
		}
		t0, redAt := r.sec(), -1.0
		r.until(t0+40, func() {
			if redAt >= 0 {
				return
			}
			n := 0
			for _, st := range r.states() {
				if st.Zone == protocol.LevelRed && st.Move != nil {
					n++
				}
			}
			if n == c.phones {
				redAt = r.sec() - t0
			}
		})
		t.Logf("%d phones at %.0f, %.0f: all red with a move after %.1f s", c.phones, c.x, c.y, redAt)
		if redAt < 0 {
			t.Errorf("%d phones at %.0f, %.0f never all went red", c.phones, c.x, c.y)
		}
	}
}

// TestHybridNoPhones: with nobody real connected, a simulation with the
// hybrid step in the loop is bit-for-bit the simulation without it, and
// there is nothing to surge around.
func TestHybridNoPhones(t *testing.T) {
	run := func(hybrid bool) ([][4]float64, []protocol.Node) {
		r := newHybridRun(t, 0, 0, 0)
		if err := r.a.startSimAt(SimStart{People: 120, Participation: 0.6, Seed: 3}, simT0); err != nil {
			t.Fatal(err)
		}
		for r.sec() < 12 {
			r.now += 50
			if hybrid {
				r.a.hybridSync(r.now)
			}
			r.a.simTick(r.now)
			if (r.now-simT0)%250 == 0 {
				r.a.detectTick(r.now)
			}
		}
		r.a.mu.Lock()
		defer r.a.mu.Unlock()
		snap := r.a.snapshotLocked(r.now)
		return snap.Sim.Bodies, snap.Nodes
	}
	b0, n0 := run(false)
	b1, n1 := run(true)
	if !reflect.DeepEqual(b0, b1) || !reflect.DeepEqual(n0, n1) {
		t.Error("the simulation differs with the hybrid step and no real phones")
	}
	for _, n := range n1 {
		if n.Real || n.Name != "" {
			t.Errorf("node %+v", n)
		}
	}
	a := demoApp(t)
	if _, err := a.SurgePhones(); err != ErrNoPhones {
		t.Errorf("surge with no phones: %v", err)
	}
}

// TestHybridPhoneComesAndGoes: a phone that joins while the simulation runs
// appears in it, follows when staff move it, and leaves it when it
// disconnects.
func TestHybridPhoneComesAndGoes(t *testing.T) {
	r := newHybridRun(t, 0, 0, 0)
	if err := r.a.startSimAt(SimStart{People: 60, Participation: 0.6, Seed: 5}, simT0); err != nil {
		t.Fatal(err)
	}
	r.until(1, nil)
	r.a.PhoneHello("late", 5, 12, "Android")
	r.a.PhoneSync("late", 0, 30)
	r.real = []string{"late"}
	r.until(3, nil)
	find := func() (protocol.Node, bool) {
		r.a.mu.Lock()
		defer r.a.mu.Unlock()
		for _, n := range r.a.snapshotLocked(r.now).Nodes {
			if n.ID == "late" {
				return n, true
			}
		}
		return protocol.Node{}, false
	}
	n, ok := find()
	if !ok || !n.Real || n.X != 5 || n.Y != 12 || n.Status != protocol.StatusOK {
		t.Fatalf("late phone in the sim: %+v (found %v)", n, ok)
	}
	if st := r.states()["late"]; !st.Sim {
		t.Errorf("state = %+v", st)
	}
	if _, err := r.a.MovePhone("late", 7, 11); err != nil {
		t.Fatal(err)
	}
	r.until(4, nil)
	if n, _ := find(); n.X != 7 || n.Y != 11 {
		t.Errorf("after the move: %+v", n)
	}
	r.a.sim.mu.Lock()
	pins := r.a.sim.w.Pins()
	if len(pins) != 1 || pins[0].X != 7 || pins[0].Y != 11 {
		t.Errorf("pins = %+v", pins)
	}
	r.a.sim.mu.Unlock()
	r.a.PhoneGone("late")
	r.real = nil
	r.until(5, nil)
	if n, ok := find(); ok && n.Status != protocol.StatusStale {
		t.Errorf("after leaving: %+v", n)
	}
	r.a.sim.mu.Lock()
	if n := len(r.a.sim.w.Pins()); n != 0 {
		t.Errorf("%d pinned bodies after the phone left", n)
	}
	r.a.sim.mu.Unlock()
}
