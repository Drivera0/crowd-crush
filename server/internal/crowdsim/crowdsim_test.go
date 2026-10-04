package crowdsim

import (
	"math"
	"sort"
	"testing"
)

func newWorld(t testing.TB, people int, seed int64) *World {
	t.Helper()
	w, err := New(Config{W: 24, H: 16, People: people, Participation: 0.6, Seed: seed, StartMs: 1_000_000})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func run(w *World, sec float64) {
	for end := w.T + sec; w.T < end-1e-9; {
		w.Step()
	}
}

func f64(v float64) *float64 { return &v }

// maxOverlap is the deepest body overlap (m) between any two people.
func maxOverlap(w *World) float64 {
	ov := 0.0
	as := w.Agents()
	for i, a := range as {
		for _, b := range as[i+1:] {
			ov = math.Max(ov, a.R+b.R-math.Hypot(a.X-b.X, a.Y-b.Y))
		}
	}
	return ov
}

func TestPhysicsSanity(t *testing.T) {
	w := newWorld(t, 250, 1)
	if len(w.Agents()) != 250 {
		t.Fatalf("placed %d of 250", len(w.Agents()))
	}
	for i := 0; i < 50*30; i++ {
		w.Step()
		if i%50 != 0 {
			continue
		}
		for _, a := range w.Agents() {
			for _, v := range []float64{a.X, a.Y, a.VX, a.VY, a.AX, a.AY, a.Pressure, a.Density} {
				if math.IsNaN(v) || math.IsInf(v, 0) {
					t.Fatalf("t=%.1f: agent %d has a non-finite value: %+v", w.T, a.ID, *a)
				}
			}
			// Walls hold: inside the venue, never in the stage pit.
			if a.X < a.R*0.5 || a.Y < a.R*0.5 || a.X > w.G.W-a.R*0.5 || a.Y > w.G.H-a.R*0.5 || w.G.inStage(a.X, a.Y) {
				t.Fatalf("t=%.1f: agent %d at %.2f, %.2f went through a wall", w.T, a.ID, a.X, a.Y)
			}
		}
		if ov := maxOverlap(w); ov > 0.03 {
			t.Fatalf("t=%.1f: bodies overlap by %.3f m in calm", w.T, ov)
		}
	}
	if len(w.Agents()) != 250 {
		t.Errorf("%d people left the venue while calm", 250-len(w.Agents()))
	}
}

func TestWallsHoldUnderSurge(t *testing.T) {
	w := newWorld(t, 400, 2)
	w.Apply(Action{Type: ActSurge, Strength: f64(1)})
	run(w, 20)
	for _, a := range w.Agents() {
		if a.Y < w.G.BarrierY+a.R*0.5 && a.X > w.G.BarrierX0 && a.X < w.G.BarrierX1 {
			t.Fatalf("agent %d pushed through the barrier: %.2f, %.2f", a.ID, a.X, a.Y)
		}
		if a.X < 0 || a.Y < 0 || a.X > w.G.W || a.Y > w.G.H {
			t.Fatalf("agent %d outside the venue: %.2f, %.2f", a.ID, a.X, a.Y)
		}
	}
	// Squeezed, but no explosion: overlaps stay a few cm.
	if ov := maxOverlap(w); ov > 0.08 {
		t.Errorf("max overlap %.3f m under a full surge", ov)
	}
}

// TestDisperse: everyone reaches an exit and leaves; closed exits are walls.
func TestDisperse(t *testing.T) {
	w := newWorld(t, 120, 3)
	if err := w.Apply(Action{Type: ActExit, ID: "exit-l", Open: new(bool)}); err != nil {
		t.Fatal(err)
	}
	phones := w.Phones()
	w.Apply(Action{Type: ActDisperse})
	gone := 0
	for i := 0; i < 50*90 && len(w.Agents()) > 0; i++ {
		w.Step()
		for _, e := range w.Events() {
			if e.Kind == EvGone {
				gone++
			}
		}
	}
	if n := len(w.Agents()); n > 0 {
		t.Fatalf("%d of 120 still inside after %.0f s", n, w.T)
	}
	if gone != phones {
		t.Errorf("%d gone events for %d phones", gone, phones)
	}
	t.Logf("120 people out in %.1f s", w.T)
	// Closing every exit makes disperse impossible.
	w2 := newWorld(t, 10, 4)
	for _, e := range w2.G.Exits {
		w2.Apply(Action{Type: ActExit, ID: e.ID, Open: new(bool)})
	}
	if err := w2.Apply(Action{Type: ActDisperse}); err == nil {
		t.Error("disperse with every exit closed should fail")
	}
}

func TestAttractFormsGroup(t *testing.T) {
	w := newWorld(t, 200, 5)
	w.Apply(Action{Type: ActAttract, X: f64(12), Y: f64(11)})
	run(w, 40)
	near := 0
	for _, a := range w.Agents() {
		if math.Hypot(a.X-12, a.Y-11) < 2.5 {
			near++
		}
	}
	tr := w.Truth()
	t.Logf("%d people within 2.5 m of the point; max density %.1f, pressure %.0f", near, tr.MaxDensity, tr.MaxPressure)
	if near < 40 {
		t.Errorf("only %d people gathered at the point", near)
	}
	if tr.MaxPressure >= CrushPressure {
		t.Errorf("a group gathering (not pushing) reached %.0f N/m", tr.MaxPressure)
	}
}

// TestSurgeVsCalm: a surge packs the front past 6/m² with crushing
// pressure; calm stays loose.
func TestSurgeVsCalm(t *testing.T) {
	calm := newWorld(t, 250, 6)
	run(calm, 30)
	tc := calm.Truth()
	surge := newWorld(t, 250, 6)
	surge.Apply(Action{Type: ActSurge, Strength: f64(0.7)})
	p0 := 0.0
	run(surge, 1)
	p0 = surge.Truth().MaxPressure
	run(surge, 29)
	ts := surge.Truth()
	t.Logf("calm: density %.1f (3×3 m %.1f), pressure %.0f; surge: density %.1f (3×3 m %.1f), pressure %.0f → %.0f N/m, %d crushing, danger at %.1f s",
		tc.MaxDensity, tc.WindowDensity, tc.MaxPressure, ts.MaxDensity, ts.WindowDensity, p0, ts.MaxPressure, ts.Crushing, ts.DangerAt)
	if tc.MaxDensity > 4 || tc.MaxPressure > 100 || tc.DangerAt >= 0 {
		t.Errorf("calm: density %.1f, pressure %.0f, danger at %.1f", tc.MaxDensity, tc.MaxPressure, tc.DangerAt)
	}
	if ts.MaxDensity <= CrushDensity || ts.MaxPressure < CrushPressure || ts.Crushing < 5 || ts.DangerAt < 0 {
		t.Errorf("surge: density %.1f, pressure %.0f, crushing %d, danger at %.1f", ts.MaxDensity, ts.MaxPressure, ts.Crushing, ts.DangerAt)
	}
	// Pressure is at the barrier, where the crowd is pushed against it.
	var front, back []float64
	for _, a := range surge.Agents() {
		if a.Y < surge.G.BarrierY+1.5 {
			front = append(front, a.Pressure)
		} else if a.Y > surge.G.BarrierY+4 {
			back = append(back, a.Pressure)
		}
	}
	if median(front) <= median(back) {
		t.Errorf("pressure at the barrier (median %.0f) not above the back (median %.0f)", median(front), median(back))
	}
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[len(s)/2]
}

// TestShovePropagates: a sideways shove in a packed crowd reaches people
// further away later; nothing scripts the delay.
func TestShovePropagates(t *testing.T) {
	for _, seed := range []int64{3, 7} {
		w := newWorld(t, 250, seed)
		w.Apply(Action{Type: ActStage})
		run(w, 20)
		// Shove from the left, a couple of metres behind the barrier.
		sx, sy := w.G.BarrierX0+2, w.G.BarrierY+2
		w.Shove(sx, sy, 1, 0, 0.8)
		t0 := w.T
		type rec struct{ d, peak, at float64 }
		recs := map[int]*rec{}
		sm := map[int]float64{}
		for w.T < t0+3 {
			w.Step()
			for _, a := range w.Agents() {
				along, perp := a.X-sx, math.Abs(a.Y-sy)
				if along < 0 || along > 6 || perp > 1.2 {
					continue
				}
				// Lightly smoothed acceleration along the push (100 ms).
				s := sm[a.ID] + (a.AX-sm[a.ID])*0.2
				sm[a.ID] = s
				r := recs[a.ID]
				if r == nil {
					r = &rec{d: along}
					recs[a.ID] = r
				}
				if s > r.peak {
					r.peak, r.at = s, w.T-t0
				}
			}
		}
		var near, far []float64
		for _, r := range recs {
			switch {
			case r.d < 1.5:
				near = append(near, r.at)
			case r.d >= 3 && r.d < 5:
				far = append(far, r.at)
			}
		}
		tn, tf := median(near), median(far)
		t.Logf("seed %d: peak after %.2f s within 1.5 m (%d people), %.2f s at 3–5 m (%d people)", seed, tn, len(near), tf, len(far))
		if len(near) < 3 || len(far) < 3 {
			t.Fatalf("too few people near the shove line: %d, %d", len(near), len(far))
		}
		if tf < tn+0.15 {
			t.Errorf("seed %d: far peak %.2f s not later than near peak %.2f s", seed, tf, tn)
		}
	}
}

func TestPhoneMessages(t *testing.T) {
	w := newWorld(t, 100, 8)
	phones := w.Phones()
	if phones < 40 || phones > 80 {
		t.Fatalf("%d phones for 100 people at participation 0.6", phones)
	}
	hello, sync := map[string]bool{}, map[string]bool{}
	lastT := map[string]int64{}
	motions := map[string]int{}
	posAt := map[string][]float64{}
	walk := false
	for w.T < 10 {
		if !walk && w.T > 2 {
			w.Apply(Action{Type: ActAttract, X: f64(12), Y: f64(12)})
			walk = true
		}
		w.Step()
		for _, e := range w.Events() {
			switch e.Kind {
			case EvHello:
				hello[e.ID] = true
			case EvSync:
				if !hello[e.ID] || e.RTT <= 0 {
					t.Fatalf("sync before hello or bad rtt: %+v", e)
				}
				sync[e.ID] = true
			case EvMotion:
				if !sync[e.ID] {
					t.Fatalf("motion before sync: %s", e.ID)
				}
				if lastT[e.ID] != 0 && e.M.T-lastT[e.ID] != 100 {
					t.Fatalf("%s: summaries %d ms apart", e.ID, e.M.T-lastT[e.ID])
				}
				lastT[e.ID] = e.M.T
				motions[e.ID]++
				for _, v := range []float64{e.M.AX, e.M.AY, e.M.AZ, e.M.Rot} {
					if math.IsNaN(v) || math.Abs(v) > 1000 {
						t.Fatalf("bad reading %+v", e.M)
					}
				}
			case EvPos:
				posAt[e.ID] = append(posAt[e.ID], w.T)
			}
		}
	}
	if len(hello) != phones || len(motions) != phones {
		t.Fatalf("%d hellos, %d phones streaming, %d phones", len(hello), len(motions), phones)
	}
	for id, n := range motions {
		if n < 95 || n > 101 {
			t.Errorf("%s: %d summaries in 10 s", id, n)
		}
	}
	// Server clock: the last summary is at StartMs + ~10 s.
	for _, v := range lastT {
		if v < w.StartMs+9800 || v > w.StartMs+10100 {
			t.Errorf("summary time %d, want ≈ %d", v, w.StartMs+10000)
		}
		break
	}
	moved := 0
	for id, ts := range posAt {
		moved++
		for i := 1; i < len(ts); i++ {
			if ts[i]-ts[i-1] < 0.5-1e-9 {
				t.Fatalf("%s: positions %.2f s apart (max 2 Hz)", id, ts[i]-ts[i-1])
			}
		}
	}
	if moved == 0 {
		t.Error("no phone sent a position while people walked to the attraction")
	}
}

// TestPhoneAxes: a walking person's phone bounces vertically (y) at the
// step frequency; a person shoved sideways reads it on x, not z.
func TestPhoneAxes(t *testing.T) {
	w := newWorld(t, 1, 9)
	a := w.Agents()[0]
	if a.phone == nil {
		a.phone = newPhone(w, a)
	}
	// Facing the stage (−y): a shove toward +x is "right".
	a.phone.facing = -math.Pi / 2
	run(w, 1)
	w.Events()
	w.Shove(a.X, a.Y, 1, 0, 1)
	var sx, sz float64
	for i := 0; i < 15; i++ {
		w.Step()
		for _, e := range w.Events() {
			if e.Kind == EvMotion {
				sx += math.Abs(e.M.AX)
				sz += math.Abs(e.M.AZ)
			}
		}
	}
	if sx < 3*sz {
		t.Errorf("sideways shove: |x| %.2f vs |z| %.2f", sx, sz)
	}
}

func TestApplyValidation(t *testing.T) {
	w := newWorld(t, 20, 10)
	bad := []Action{
		{Type: "dance"},
		{Type: ""},
		{Type: ActAttract},
		{Type: ActAttract, X: f64(5)},
		{Type: ActAttract, X: f64(-1), Y: f64(5)},
		{Type: ActShove, X: f64(5), Y: f64(5)},
		{Type: ActShove, X: f64(5), Y: f64(5), DX: f64(0), DY: f64(0)},
		{Type: ActSurge, Strength: f64(1.5)},
		{Type: ActExit, ID: "exit-bl"},
		{Type: ActExit, ID: "nope", Open: new(bool)},
		{Type: ActSpawn, X: f64(5), Y: f64(5)},
		{Type: ActSpawn, X: f64(5), Y: f64(5), N: new(int)},
		{Type: ActSpawn, X: f64(math.NaN()), Y: f64(5), N: func() *int { n := 3; return &n }()},
	}
	for _, act := range bad {
		if err := w.Apply(act); err == nil {
			t.Errorf("%+v: no error", act)
		}
	}
	n := 15
	if err := w.Apply(Action{Type: ActSpawn, X: f64(12), Y: f64(12), N: &n}); err != nil {
		t.Fatal(err)
	}
	if len(w.Agents()) != 35 {
		t.Errorf("%d people after spawning 15 into 20", len(w.Agents()))
	}
	for _, typ := range []string{ActCalm, ActStage, ActSurge} {
		if err := w.Apply(Action{Type: typ}); err != nil {
			t.Errorf("%s: %v", typ, err)
		}
	}
}

func TestTruthDangerHold(t *testing.T) {
	var tr Truth
	tr.Init()
	now := 0.0
	// Dangerous for 0.5 s, then calm, then dangerous for 1.2 s.
	step := func(danger bool) {
		now += Dt
		tr.update(now, danger)
	}
	for i := 0; i < 25; i++ {
		step(true)
	}
	for i := 0; i < 10; i++ {
		step(false)
	}
	if tr.DangerAt >= 0 {
		t.Fatalf("0.5 s of danger counted: %.2f", tr.DangerAt)
	}
	start := now + Dt
	for i := 0; i < 60; i++ {
		step(true)
	}
	if math.Abs(tr.DangerAt-start) > 1e-6 {
		t.Fatalf("danger at %.2f, want the start of the stretch %.2f", tr.DangerAt, start)
	}
}

func BenchmarkStep250(b *testing.B)  { benchStep(b, 250) }
func BenchmarkStep500(b *testing.B)  { benchStep(b, 500) }
func BenchmarkStep1000(b *testing.B) { benchStep(b, 1000) }

func benchStep(b *testing.B, n int) {
	w := newWorld(b, n, 1)
	w.Apply(Action{Type: ActSurge, Strength: f64(0.7)})
	run(w, 5)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w.Step()
		w.Events()
	}
}
