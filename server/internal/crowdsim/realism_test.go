package crowdsim

import (
	"math"
	"sort"
	"testing"
)

func TestRealismPresets(t *testing.T) {
	for name, want := range map[string]Realism{"": {}, "ideal": {}, "realistic": {GPS: 1, Carry: 1, Dropout: 1}, "harsh": {GPS: 2, Carry: 2, Dropout: 2}} {
		got, err := RealismPreset(name)
		if err != nil || got != want {
			t.Errorf("%q: %+v, %v", name, got, err)
		}
	}
	if _, err := RealismPreset("perfect"); err == nil {
		t.Error("unknown preset accepted")
	}
	if !(Realism{}).Ideal() || (Realism{Carry: 1}).Ideal() {
		t.Error("Ideal()")
	}
	for _, bad := range []Realism{{GPS: -1}, {Carry: 3.5}, {Dropout: math.NaN()}, {Carry: 1, ForceCarry: 9}} {
		if bad.Validate() == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	if _, err := New(Config{W: 24, H: 16, People: 10, Participation: 0.6, Seed: 1, Realism: Realism{GPS: 9}}); err == nil {
		t.Error("world accepted GPS strength 9")
	}
}

// messyWorld is a still crowd with messy phones.
func messyWorld(t testing.TB, people int, seed int64, rl Realism) *World {
	t.Helper()
	w, err := New(Config{W: 24, H: 16, People: people, Participation: 0.6, Seed: seed, StartMs: 1_700_000_000_000, Realism: rl})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// TestIdealUntouched: asking for ideal phones by name changes nothing, and
// messy phones don't change the crowd itself (same bodies, same truth).
func TestIdealUntouched(t *testing.T) {
	a := newWorld(t, 80, 3)
	b := messyWorld(t, 80, 3, Realism{GPS: 1, Carry: 1, Dropout: 1})
	for a.T < 8 {
		a.Step()
		b.Step()
	}
	if len(a.Agents()) != len(b.Agents()) {
		t.Fatalf("%d vs %d people", len(a.Agents()), len(b.Agents()))
	}
	for i, p := range a.Agents() {
		q := b.Agents()[i]
		if p.X != q.X || p.Y != q.Y || (p.PhoneID() == "") != (q.PhoneID() == "") {
			t.Fatalf("person %d differs with messy phones: %.4f,%.4f vs %.4f,%.4f", i, p.X, p.Y, q.X, q.Y)
		}
	}
	for _, e := range a.Events() {
		if e.Kind == EvGPS || e.Auto || len(e.M.G) != 0 {
			t.Fatalf("ideal phone sent %+v", e)
		}
	}
}

// TestGPSError: fixes about once a second, a median error of a few metres
// that stays correlated over 10 s, and a reported accuracy that covers
// roughly two thirds of the errors.
func TestGPSError(t *testing.T) {
	w := messyWorld(t, 250, 4, Realism{GPS: 1})
	w.Churn, w.Trips = false, false
	type fix struct{ t, ex, ey, acc float64 }
	fixes := map[string][]fix{}
	where := func() map[string][2]float64 {
		m := map[string][2]float64{}
		for _, a := range w.Agents() {
			if id := a.PhoneID(); id != "" {
				m[id] = [2]float64{a.X, a.Y}
			}
		}
		return m
	}
	hello := 0
	for w.T < 60 {
		w.Step()
		pos := where()
		for _, e := range w.Events() {
			switch e.Kind {
			case EvHello:
				hello++
				if !e.Auto {
					t.Fatal("a GPS phone's hello must not carry a position")
				}
			case EvPos:
				t.Fatal("a GPS phone sent an exact position")
			case EvGPS:
				p := pos[e.ID]
				fixes[e.ID] = append(fixes[e.ID], fix{w.T, e.X - p[0], e.Y - p[1], e.Acc})
			}
		}
	}
	if hello != w.Phones() || len(fixes) != w.Phones() {
		t.Fatalf("%d hellos, %d phones with fixes, %d phones", hello, len(fixes), w.Phones())
	}
	var errs []float64
	inside, total := 0, 0
	var num, den float64
	for id, fs := range fixes {
		if len(fs) < 50 || len(fs) > 70 {
			t.Fatalf("%s: %d fixes in 60 s", id, len(fs))
		}
		for i, f := range fs {
			d := math.Hypot(f.ex, f.ey)
			errs = append(errs, d)
			total++
			if d <= f.acc {
				inside++
			}
			if f.acc < 3 {
				t.Fatalf("accuracy %.1f m", f.acc)
			}
			if i+10 < len(fs) {
				num += f.ex * fs[i+10].ex
				den += f.ex * f.ex
			}
		}
	}
	sort.Float64s(errs)
	med := errs[len(errs)/2]
	cover := float64(inside) / float64(total)
	t.Logf("median error %.1f m, p90 %.1f m, %.0f %% within the reported accuracy, 10 s autocorrelation %.2f",
		med, errs[len(errs)*9/10], 100*cover, num/den)
	if med < 3.5 || med > 8 {
		t.Errorf("median horizontal error %.1f m, want about 5", med)
	}
	if cover < 0.5 || cover > 0.85 {
		t.Errorf("%.0f %% of fixes within the reported accuracy, want roughly 68", 100*cover)
	}
	if num/den < 0.5 {
		t.Errorf("error 10 s apart correlates %.2f: it should drift, not jump about", num/den)
	}
}

// TestCarry: the mix of carry states, gravity consistent with them, and
// the body's horizontal push still there once levelled with g.
func TestCarry(t *testing.T) {
	const n = 4000
	counts := make([]int, 4)
	offAxis := 0
	for i := 0; i < n; i++ {
		d := NewDevice("p", Realism{Carry: 1}, int64(i)+1)
		c := d.Carry()
		counts[c]++
		var evs []Event
		var sumH, sumX float64
		k, burst := 0, false
		// Standing, pushed left-right at 0.5 Hz, 1 m/s².
		for tick := 1; tick <= 100; tick++ {
			tt := float64(tick) * Dt
			evs = d.Tick(Raw{T: tt, X: 5, Y: 5, BX: math.Sin(2 * math.Pi * 0.5 * tt)}, Env{}, evs[:0])
			for _, e := range evs {
				if e.Kind != EvMotion {
					continue
				}
				g := e.M.G
				if len(g) != 3 {
					t.Fatalf("no gravity on a carried phone's summary: %+v", e.M)
				}
				if l := math.Sqrt(g[0]*g[0] + g[1]*g[1] + g[2]*g[2]); math.Abs(l-1) > 0.02 {
					t.Fatalf("gravity not a unit vector: %v", g)
				}
				if e.M.Rot > 100 {
					burst = true // a gesture, or the phone changing hands
				}
				if d.Carry() != c {
					continue
				}
				if c == CarryChest && g[1] > -0.95 {
					t.Fatalf("chest phone's gravity %v, want about (0, −1, 0)", g)
				}
				v := e.M.AX*g[0] + e.M.AY*g[1] + e.M.AZ*g[2]
				hx, hy, hz := e.M.AX-v*g[0], e.M.AY-v*g[1], e.M.AZ-v*g[2]
				sumH += hx*hx + hy*hy + hz*hz
				sumX += e.M.AX * e.M.AX
				k++
			}
		}
		if k == 0 || d.Carry() != c || burst {
			continue
		}
		h := math.Sqrt(sumH / float64(k)) // true RMS of the push over 2 s ≈ 0.7
		switch c {
		case CarryChest, CarryPocket:
			if h < 0.5 || h > 0.9 {
				t.Fatalf("%s: levelled horizontal RMS %.2f, want ≈ 0.7", CarryNames[c], h)
			}
		case CarryHand, CarryBag:
			if h < 0.3 || h > 1.2 {
				t.Fatalf("%s: levelled horizontal RMS %.2f", CarryNames[c], h)
			}
		}
		if math.Sqrt(sumX/float64(k)) < 0.5*h {
			offAxis++ // device x is no longer where the push is
		}
	}
	share := func(c int) float64 { return float64(counts[c]) / n }
	t.Logf("chest %.2f hand %.2f pocket %.2f bag %.2f; push mostly off device x in %.0f %% of phones",
		share(CarryChest), share(CarryHand), share(CarryPocket), share(CarryBag), 100*float64(offAxis)/n)
	for c, want := range []float64{0.25, 0.25, 0.35, 0.15} {
		if math.Abs(share(c)-want) > 0.03 {
			t.Errorf("%s: %.2f of phones, want %.2f", CarryNames[c], share(c), want)
		}
	}
	if offAxis < n/10 {
		t.Errorf("only %d of %d phones read the sideways push off their x axis", offAxis, n)
	}
}

// TestPocketLegSwing: a pocket phone on a walker reads the leg's swing as
// rotation and its gravity vector swings with the thigh.
func TestPocketLegSwing(t *testing.T) {
	var d *Device
	for seed := int64(1); ; seed++ {
		if d = NewDevice("p", Realism{Carry: 1}, seed); d.Carry() == CarryPocket {
			break
		}
	}
	var evs []Event
	maxRot, gMin, gMax := 0.0, 2.0, -2.0
	step := 0.0
	for tick := 1; tick <= 250; tick++ {
		step += 2 * math.Pi * 1.8 * Dt
		evs = d.Tick(Raw{T: float64(tick) * Dt, Gait: 1, Step: step, BY: 1.5 * math.Sin(step)}, Env{}, evs[:0])
		for _, e := range evs {
			if e.Kind == EvMotion && d.Carry() == CarryPocket {
				maxRot = math.Max(maxRot, e.M.Rot)
				for _, v := range e.M.G {
					gMin, gMax = math.Min(gMin, math.Abs(v)), math.Max(gMax, math.Abs(v))
				}
			}
		}
	}
	if maxRot < 50 || maxRot > 250 {
		t.Errorf("leg swing rotation rate %.0f °/s, want 80–150", maxRot)
	}
	if gMax-gMin < 0.05 {
		t.Errorf("gravity vector didn't swing with the leg")
	}
}

// TestDropouts: phones go silent and come back with a hello, messages
// arrive late but in order, and timestamps carry a clock error.
func TestDropouts(t *testing.T) {
	w := messyWorld(t, 250, 5, Realism{Dropout: 1})
	w.Churn, w.Trips = false, false
	phones := w.Phones()
	lastT := map[string]int64{}
	lastSeen := map[string]float64{}
	motions, gones, hellos, late, clumps := 0, 0, 0, 0, 0
	var silent []float64
	var clockErr []float64
	for w.T < 120 {
		w.Step()
		now := w.StartMs + int64(math.Round(w.T*1000))
		perPhone := map[string]int{}
		for _, e := range w.Events() {
			switch e.Kind {
			case EvHello:
				hellos++
				if e.Auto {
					t.Fatal("without GPS the hello carries the position")
				}
			case EvGone:
				gones++
			case EvMotion:
				motions++
				if e.M.T <= lastT[e.ID] {
					t.Fatalf("%s: summary out of order", e.ID)
				}
				if len(e.M.G) != 0 {
					t.Fatal("gravity sent with carry off")
				}
				lastT[e.ID] = e.M.T
				lastSeen[e.ID] = w.T
				d := float64(now - e.M.T)
				clockErr = append(clockErr, d)
				if d > 300 {
					late++
				}
				perPhone[e.ID]++
			}
		}
		for _, n := range perPhone {
			if n >= 3 {
				clumps++
			}
		}
		if math.Mod(w.T, 1) < Dt {
			n := 0
			for _, at := range lastSeen {
				if w.T-at > 2 {
					n++
				}
			}
			silent = append(silent, float64(n)/float64(phones))
		}
	}
	mean := 0.0
	for _, v := range silent {
		mean += v / float64(len(silent))
	}
	rate := float64(motions) / float64(phones) / 120
	t.Logf("%d phones: %.1f summaries/s each, %.0f %% silent on average, %d closed sockets, %d hellos, %d summaries > 300 ms late, %d clumps",
		phones, rate, 100*mean, gones, hellos, late, clumps)
	if mean < 0.04 || mean > 0.2 {
		t.Errorf("%.0f %% of phones silent on average, want about 10", 100*mean)
	}
	if gones == 0 || hellos <= phones {
		t.Errorf("%d closed sockets, %d hellos for %d phones: nobody dropped and came back", gones, hellos, phones)
	}
	if late == 0 || clumps == 0 {
		t.Errorf("%d late summaries, %d clumps: no stalls", late, clumps)
	}
	if rate < 7 || rate > 9.9 {
		t.Errorf("%.1f summaries/s per phone", rate)
	}
}

// TestRealisticRuns: everything on at once, through a surge, without a
// bad number.
func TestRealisticRuns(t *testing.T) {
	for _, name := range []string{RealismRealistic, RealismHarsh} {
		rl, _ := RealismPreset(name)
		w := messyWorld(t, 150, 6, rl)
		n := 0
		for w.T < 30 {
			if math.Abs(w.T-5) < Dt/2 {
				w.Apply(Action{Type: ActSurge})
			}
			w.Step()
			for _, e := range w.Events() {
				n++
				for _, v := range []float64{e.X, e.Y, e.Acc, e.M.AX, e.M.AY, e.M.AZ, e.M.Rot} {
					if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > 5000 {
						t.Fatalf("%s: bad value in %+v", name, e)
					}
				}
			}
		}
		if n == 0 {
			t.Fatalf("%s: no messages", name)
		}
	}
}
