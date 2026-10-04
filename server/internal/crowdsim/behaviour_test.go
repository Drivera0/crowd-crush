package crowdsim

import (
	"math"
	"testing"
)

// stillWorld is a concert crowd with no trips and no arrivals or
// departures: everyone just stands.
func stillWorld(t testing.TB, people int, seed int64) *World {
	w := newWorld(t, people, seed)
	w.Churn, w.Trips = false, false
	return w
}

// TestStandingNoJitter: a standing crowd stands still. Mean speed stays
// under 0.05 m/s and nobody paces back and forth (path length over 20 s).
func TestStandingNoJitter(t *testing.T) {
	t.Parallel()
	w := stillWorld(t, 250, 11)
	run(w, 8) // settle the random placement
	last := map[int][2]float64{}
	for _, a := range w.Agents() {
		last[a.ID] = [2]float64{a.X, a.Y}
	}
	path, speed, n := 0.0, 0.0, 0
	maxSpeed := 0.0
	for end := w.T + 20; w.T < end-1e-9; {
		w.Step()
		for _, a := range w.Agents() {
			p := last[a.ID]
			path += math.Hypot(a.X-p[0], a.Y-p[1])
			last[a.ID] = [2]float64{a.X, a.Y}
			v := math.Hypot(a.VX, a.VY)
			speed += v
			maxSpeed = math.Max(maxSpeed, v)
			n++
		}
	}
	mean := speed / float64(n)
	perPerson := path / float64(len(w.Agents()))
	t.Logf("standing crowd: mean speed %.4f m/s (max %.3f), path %.3f m per person in 20 s", mean, maxSpeed, perPerson)
	if mean >= 0.05 {
		t.Errorf("standing crowd mean speed %.3f m/s, want < 0.05", mean)
	}
	if perPerson > 0.3 {
		t.Errorf("standing people moved %.2f m each in 20 s: jitter or pacing", perPerson)
	}
}

// TestRoutineIdleStill: with trips and arrivals on, the people standing
// (not on their way somewhere) are still as still.
func TestRoutineIdleStill(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 250, 12)
	run(w, 8)
	speed, n, walking := 0.0, 0, 0
	for end := w.T + 20; w.T < end-1e-9; {
		w.Step()
		for _, a := range w.Agents() {
			if a.grp != nil && (a.grp.purpose == pIdle || a.grp.purpose == pAtPOI) {
				speed += math.Hypot(a.VX, a.VY)
				n++
			} else {
				walking++
			}
		}
	}
	mean := speed / float64(n)
	t.Logf("routine: idle mean speed %.4f m/s; %.1f people on the move on average", mean, float64(walking)/1000)
	if mean >= 0.05 {
		t.Errorf("idle mean speed %.3f m/s, want < 0.05", mean)
	}
}

// TestGroups: about 60 % of people are in groups of 2–4, and walking
// members stay within ~1.2 m of their group's centre on average.
func TestGroups(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 250, 13)
	w.Churn = false
	inGroups, sizes := 0, map[int]int{}
	for _, g := range w.groups {
		sizes[len(g.members)]++
		if len(g.members) > 1 {
			inGroups += len(g.members)
		}
	}
	share := float64(inGroups) / float64(len(w.Agents()))
	t.Logf("%.0f %% of people in groups; groups by size %v", 100*share, sizes)
	if share < 0.5 || share > 0.72 {
		t.Errorf("%.2f of people in groups, want 0.5–0.7", share)
	}
	if err := w.Apply(Action{Type: ActIntermission}); err != nil {
		t.Fatal(err)
	}
	sum, n, worst := 0.0, 0, 0.0
	for end := w.T + 60; w.T < end-1e-9; {
		w.Step()
		if w.ticks%10 != 0 {
			continue
		}
		for _, g := range w.groups {
			if !g.walking || len(g.members) < 2 {
				continue
			}
			d := 0.0
			for _, a := range g.members {
				d += math.Hypot(a.X-g.cx, a.Y-g.cy)
			}
			d /= float64(len(g.members))
			sum += d
			n++
			worst = math.Max(worst, d)
		}
	}
	if n == 0 {
		t.Fatal("no group walked")
	}
	mean := sum / float64(n)
	t.Logf("walking groups: mean member distance to centroid %.2f m (worst sample %.2f m, %d samples)", mean, worst, n)
	if mean > 1.2 {
		t.Errorf("walking groups spread %.2f m from their centroid, want < 1.2", mean)
	}
}

// TestSmoothMotion: nobody turns faster than they can, walking or standing,
// and desired speed never jumps.
func TestSmoothMotion(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 200, 14)
	w.Apply(Action{Type: ActIntermission})
	hd, sp := map[int]float64{}, map[int]float64{}
	maxTurn, maxFace, maxDSp := 0.0, 0.0, 0.0
	for end := w.T + 30; w.T < end-1e-9; {
		w.Step()
		for _, a := range w.Agents() {
			if h, ok := hd[a.ID]; ok {
				maxTurn = math.Max(maxTurn, math.Abs(angDiff(a.hd, h))/Dt)
				maxDSp = math.Max(maxDSp, (a.sp-sp[a.ID])/Dt)
			}
			maxFace = math.Max(maxFace, math.Abs(angDiff(a.face, a.prevFace))/Dt)
			hd[a.ID], sp[a.ID] = a.hd, a.sp
		}
	}
	t.Logf("max heading turn %.2f rad/s, body turn %.2f rad/s, desired-speed rise %.2f m/s²", maxTurn, maxFace, maxDSp)
	if maxTurn > turnSlow+1e-6 || maxFace > turnSlow+1e-6 {
		t.Errorf("turned at %.2f / %.2f rad/s (limit %.1f)", maxTurn, maxFace, turnSlow)
	}
	if maxDSp > accUp+1e-6 {
		t.Errorf("desired speed rose at %.2f m/s² (limit %.1f)", maxDSp, accUp)
	}
}

// TestIntermission: many groups go to the POIs at once, crowd around them,
// meet the first ones coming back (bidirectional flow), and return.
func TestIntermission(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 150, 15)
	w.Churn = false
	if len(w.POIs()) != 3 {
		t.Fatalf("POIs %+v", w.POIs())
	}
	w.Apply(Action{Type: ActIntermission})
	both, peakAt, peakNear := false, 0.0, 0
	maxDens := 0.0
	for w.T < 200 {
		w.Step()
		if w.ticks%25 != 0 {
			continue
		}
		to, back, near := 0, 0, 0
		for _, g := range w.groups {
			switch g.purpose {
			case pToPOI:
				to += len(g.members)
			case pBack:
				back += len(g.members)
			}
		}
		for _, a := range w.Agents() {
			for _, p := range w.POIs() {
				if math.Hypot(a.X-p.X, a.Y-p.Y) < 4.5 {
					near++
					maxDens = math.Max(maxDens, a.Density)
					break
				}
			}
		}
		if to >= 5 && back >= 5 {
			both = true
		}
		if near > peakNear {
			peakNear, peakAt = near, w.T
		}
	}
	idle := 0
	for _, g := range w.groups {
		if g.purpose == pIdle {
			idle += len(g.members)
		}
	}
	tr := w.Truth()
	t.Logf("peak %d of 150 people at the POIs at %.0f s (local density up to %.1f /m²); bidirectional: %v; %d idle at 200 s; max pressure %.0f N/m",
		peakNear, peakAt, maxDens, both, idle, tr.MaxPressure)
	if peakNear < 40 {
		t.Errorf("only %d people at the POIs at the peak", peakNear)
	}
	if !both {
		t.Error("never saw people going and coming back at the same time")
	}
	if idle < 110 {
		t.Errorf("only %d of 150 back and standing at 200 s", idle)
	}
	if tr.DangerAt >= 0 {
		t.Errorf("an intermission became dangerous at %.1f s", tr.DangerAt)
	}
}

// TestDancePhones: on dance, phones read beat sway while the bodies stay
// where they are.
func TestDancePhones(t *testing.T) {
	w := stillWorld(t, 150, 16)
	run(w, 5)
	rms := func(sec float64) (lat, vert, speed float64) {
		n, ns := 0, 0
		for end := w.T + sec; w.T < end-1e-9; {
			w.Step()
			for _, e := range w.Events() {
				if e.Kind == EvMotion {
					lat += e.M.AX * e.M.AX
					vert += e.M.AY * e.M.AY
					n++
				}
			}
			for _, a := range w.Agents() {
				speed += math.Hypot(a.VX, a.VY)
				ns++
			}
		}
		return math.Sqrt(lat / float64(n)), math.Sqrt(vert / float64(n)), speed / float64(ns)
	}
	cl, cv, cs := rms(10)
	w.Apply(Action{Type: ActDance})
	run(w, 3)
	dl, dv, ds := rms(10)
	t.Logf("calm: lateral %.2f, vertical %.2f m/s², speed %.3f m/s; dance: lateral %.2f, vertical %.2f m/s², speed %.3f m/s",
		cl, cv, cs, dl, dv, ds)
	if dl < 3*cl || dv < 3*cv {
		t.Errorf("dance barely shows on the phones")
	}
	if ds >= 0.05 {
		t.Errorf("dancing crowd moves: %.3f m/s", ds)
	}
}

// TestChurn: people arrive and leave through the exits at a low rate,
// with hello and gone events for their phones.
func TestChurn(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 200, 17)
	w.Trips = false
	p0 := w.Phones()
	hello, gone := 0, 0
	for w.T < 120 {
		w.Step()
		for _, e := range w.Events() {
			switch e.Kind {
			case EvHello:
				hello++
			case EvGone:
				gone++
			}
		}
	}
	t.Logf("120 s: %d people (started with 200); %d phones arrived, %d left", len(w.Agents()), hello-p0, gone)
	if gone == 0 {
		t.Error("nobody left in 2 minutes")
	}
	if n := len(w.Agents()); n == 200 || n < 180 || n > 220 {
		t.Errorf("%d people after 2 minutes of churn", n)
	}
}

// TestBottleneck: flow through a 1 m door from a waiting crowd, against
// experiments: Kretz et al. (2006) and Seyfried et al. (2009) measured
// specific flows of ~1.6–1.9 persons/(m·s) through ~1 m bottlenecks.
func TestBottleneck(t *testing.T) {
	t.Parallel()
	sum := 0.0
	for seed := int64(1); seed <= 2; seed++ {
		j := bottleneckFlow(1.0, 120, seed)
		t.Logf("seed %d: %.2f persons/(m·s) through a 1 m door", seed, j)
		sum += j / 2
	}
	t.Logf("mean %.2f persons/(m·s) (experiments ~1.6–1.9)", sum)
	if sum < 1.3 || sum > 2.2 {
		t.Errorf("bottleneck flow %.2f persons/(m·s), want ~1.5–2 (1.3–2.2 accepted)", sum)
	}
}

// bottleneckFlow is the specific flow (persons per metre of door per
// second) between the 10th and the (n−20)th person out.
func bottleneckFlow(door float64, n int, seed int64) float64 {
	w := NewBottleneck(door, n, seed)
	var outAt []float64
	for w.T < 120 && len(w.Agents()) > 0 {
		before := len(w.Agents())
		w.Step()
		for k := len(w.Agents()); k < before; k++ {
			outAt = append(outAt, w.T)
		}
	}
	i0, i1 := 10, len(outAt)-20
	if i1 <= i0 {
		return 0
	}
	return float64(i1-i0) / (outAt[i1] - outAt[i0]) / door
}

// flowSpeed runs a (counter)flow corridor: mean speed along each person's
// own direction and lane order over 10 s after 20 s of warm-up.
func flowSpeed(rho, share float64, seed int64) (v, phi0, phi float64) {
	w := NewCounterflow(12, 4, rho, share, seed)
	phi0 = w.LaneOrder(0.5)
	run(w, 20)
	n := 0
	for end := w.T + 10; w.T < end-1e-9; {
		w.Step()
		v += w.MeanSpeedOwn()
		phi += w.LaneOrder(0.5)
		n++
	}
	return v / float64(n), phi0, phi / float64(n)
}

// TestCounterflow: in a corridor with half the people walking each way at
// 1 /m², lanes form (lane order well above a random mix) and people walk
// slower than in one-way flow with the same steering. At 2 /m² it is only
// logged: there the outcome depends on the seed (lanes, or gridlock).
func TestCounterflow(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		rho    float64
		seed   int64
		assert bool
	}{{1, 1, true}, {1, 2, true}, {2, 1, false}, {2, 2, false}} {
		uni, _, _ := flowSpeed(c.rho, 0, c.seed)
		v, phi0, phi := flowSpeed(c.rho, 0.5, c.seed)
		t.Logf("ρ = %.1f /m², seed %d: one-way %.2f m/s, counterflow %.2f m/s (%.0f %%); lane order %.2f at the start → %.2f",
			c.rho, c.seed, uni, v, 100*v/uni, phi0, phi)
		if !c.assert {
			continue
		}
		if v >= uni {
			t.Errorf("counterflow (%.2f m/s) not slower than one-way (%.2f m/s)", v, uni)
		}
		if v < 0.5*uni {
			t.Errorf("counterflow jammed: %.2f m/s", v)
		}
		if phi < 0.6 || phi < phi0+0.25 {
			t.Errorf("no lanes: order %.2f (start %.2f)", phi, phi0)
		}
	}
}
