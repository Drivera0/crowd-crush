package app

import (
	"testing"

	"github.com/Drivera0/crowd-crush/server/internal/crowdsim"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// TestPackedNodes: two minutes into a surge the crowd has stopped moving,
// and the dots still say who is being crushed: the phones at the barrier
// are red (most of them with motion status "ok": a still, packed phone is
// not calm), the loose back of the crowd is not, and after "calm" nobody is.
func TestPackedNodes(t *testing.T) {
	t.Parallel()
	r := startSim(t, 250, 3)
	r.until(5, nil)
	r.act(crowdsim.Action{Type: "stage"})
	r.until(25, nil)
	r.act(crowdsim.Action{Type: "surge", Strength: fp(0.7)})
	count := func() (core, coreRed, coreCalm, stillRed, edge, edgeRed, red int, snap protocol.Snapshot) {
		r.a.mu.Lock()
		snap = r.a.snapshotLocked(r.now)
		r.a.mu.Unlock()
		for _, n := range snap.Nodes {
			if n.Press == protocol.LevelRed {
				red++
			}
			switch {
			case n.Y < crowdsim.StageDepth+1.2 && n.X > 9 && n.X < 15:
				core++
				if n.Press == protocol.LevelRed {
					coreRed++
					if n.Status == protocol.StatusOK {
						stillRed++
					}
				}
				if n.Press == "" {
					coreCalm++
				}
			case n.Dens < 1.5:
				edge++
				if n.Press != "" || n.Crush > 0.2 {
					edgeRed++
				}
			}
		}
		return
	}
	for _, at := range []float64{145, 205} { // 120 s and 180 s of surge
		r.until(at, nil)
		core, coreRed, coreCalm, stillRed, edge, edgeRed, red, snap := count()
		t.Logf("%.0f s of surge: %d phones at the barrier, %d red (%d of them motion-status ok), %d calm; %d loose phones, %d of them coloured; %d red in all of %d",
			at-25, core, coreRed, stillRed, coreCalm, edge, edgeRed, red, len(snap.Nodes))
		if core < 10 || coreRed < core*8/10 || coreCalm > 0 {
			t.Errorf("%.0f s: %d of %d phones at the barrier are red, %d calm", at-25, coreRed, core, coreCalm)
		}
		if stillRed < coreRed/2 {
			t.Errorf("%.0f s: only %d of the %d red phones are still (status ok)", at-25, stillRed, coreRed)
		}
		if edge < 3 || edgeRed > 0 {
			t.Errorf("%.0f s: %d of %d loose phones are coloured", at-25, edgeRed, edge)
		}
		if red > len(snap.Nodes)*8/10 {
			t.Errorf("%.0f s: %d of %d phones red: no gradient", at-25, red, len(snap.Nodes))
		}
		for _, n := range snap.Nodes {
			if n.Press == protocol.LevelRed && (n.Dens < 3.6 || n.Crush < 0.6) {
				t.Errorf("node %s red at %.1f /m², crush %.2f", n.ID, n.Dens, n.Crush)
			}
		}
		if b := snap.Sim.Bodies; len(b) == 0 || b[0][4] <= 0 {
			t.Fatalf("sim bodies carry no density: %v", b[:1])
		}
	}
	r.act(crowdsim.Action{Type: "calm"})
	r.until(245, nil)
	if _, coreRed, _, _, _, _, red, _ := count(); red > 0 {
		t.Errorf("40 s after calm %d phones are still red (%d at the barrier)", red, coreRed)
	}
}

// TestPackedPhoneState: a real phone standing still at the barrier of a
// simulated surge is told it is in danger (red, with a way out), and its
// dot is red although its motion status is ok.
func TestPackedPhoneState(t *testing.T) {
	t.Parallel()
	r := newHybridRun(t, 1, 12, 2.0)
	if err := r.a.startSimAt(SimStart{People: 250, Participation: 0.6, Seed: 5}, simT0); err != nil {
		t.Fatal(err)
	}
	id := r.real[0]
	r.until(5, nil)
	if st := r.states()[id]; st.Zone == protocol.LevelRed {
		t.Fatalf("before the surge the phone is %q", st.Zone)
	}
	if err := r.a.SimAction(crowdsim.Action{Type: "stage"}); err != nil {
		t.Fatal(err)
	}
	r.until(25, nil)
	if err := r.a.SimAction(crowdsim.Action{Type: "surge", Strength: fp(0.7)}); err != nil {
		t.Fatal(err)
	}
	r.until(120, nil)
	st := r.states()[id]
	node := func() protocol.Node {
		r.a.mu.Lock()
		snap := r.a.snapshotLocked(r.now)
		r.a.mu.Unlock()
		for _, n := range snap.Nodes {
			if n.ID == id {
				return n
			}
		}
		return protocol.Node{}
	}
	n := node()
	t.Logf("phone state %s/%s, move %+v; node status %s, %.1f /m², press %q, crush %.2f", st.Node, st.Zone, st.Move, n.Status, n.Dens, n.Press, n.Crush)
	if st.Zone != protocol.LevelRed || st.Move == nil {
		t.Errorf("a still phone in the crushed front is %q (move %v), want red with guidance", st.Zone, st.Move)
	}
	if n.Press != protocol.LevelRed || n.Status == protocol.StatusWave {
		t.Errorf("its dot: status %s, press %q, want a packed (red) dot", n.Status, n.Press)
	}
	// A phone that left counts toward no density: no value.
	r.a.PhoneGone(id)
	r.real = nil
	r.until(121, nil)
	if n := node(); n.ID != id || n.Dens != 0 || n.Press != "" || n.Crush != 0 {
		t.Errorf("a phone that left has a crush level: dens %.1f press %q crush %.2f", n.Dens, n.Press, n.Crush)
	}
}

// TestSurgeAroundPhonesHolds: the press around the real phones does not let
// go by itself. Two and a half minutes after the click the phones are still
// red, their dots are still packed in and the people around them are still
// under pressure; "calm" ends it.
func TestSurgeAroundPhonesHolds(t *testing.T) {
	t.Parallel()
	r := newHybridRun(t, 3, 11, 9)
	r.until(1, nil)
	if _, err := r.a.surgePhonesAt(r.now, 7); err != nil {
		t.Fatal(err)
	}
	check := func(at float64) (red, packed int, pressure float64) {
		r.until(at, nil)
		for _, st := range r.states() {
			if st.Zone == protocol.LevelRed && st.Move != nil {
				red++
			}
		}
		r.a.mu.Lock()
		snap := r.a.snapshotLocked(r.now)
		r.a.mu.Unlock()
		for _, n := range snap.Nodes {
			if n.Real && n.Press != "" {
				packed++ // tight (yellow) or dangerously packed (red)
			}
		}
		return red, packed, r.a.SimStatus().Truth.MaxPressure
	}
	for _, at := range []float64{40, 100, 150} {
		red, packed, p := check(at)
		t.Logf("%.0f s after the click: %d of 3 phones red, %d dots packed in, peak pressure %.0f N/m", at-1, red, packed, p)
		if red != 3 || packed != 3 || p < crowdsim.CrushPressure/2 {
			t.Errorf("%.0f s: %d phones red, %d dots packed, pressure %.0f: the press let go", at-1, red, packed, p)
		}
	}
	if err := r.a.SimAction(crowdsim.Action{Type: "calm"}); err != nil {
		t.Fatal(err)
	}
	red, _, p := check(200)
	r.a.mu.Lock()
	snap := r.a.snapshotLocked(r.now)
	r.a.mu.Unlock()
	dots := 0
	for _, n := range snap.Nodes {
		if n.Press == protocol.LevelRed {
			dots++
		}
	}
	if red != 0 || dots != 0 || p > 200 {
		t.Errorf("50 s after calm: %d phones red, %d red dots, pressure %.0f", red, dots, p)
	}
}
