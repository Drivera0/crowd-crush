package crowdsim

import (
	"math"
	"sort"
	"testing"
)

// thirds splits the crowd by distance from the barrier and returns the
// mean pressure (N/m) and packed density (/m²) of the front and back third,
// and how many people stand within 1 m of the barrier.
func thirds(w *World) (fp, fd, bp, bd float64, front int) {
	as := append([]*Agent(nil), w.Agents()...)
	sort.Slice(as, func(i, j int) bool { return as[i].Y < as[j].Y })
	n := len(as) / 3
	for _, a := range as[:n] {
		fp += a.Pressure / float64(n)
		fd += w.PackedDensity(a) / float64(n)
	}
	for _, a := range as[len(as)-n:] {
		bp += a.Pressure / float64(n)
		bd += w.PackedDensity(a) / float64(n)
	}
	for _, a := range as {
		if a.Y-w.G.BarrierY < 1 {
			front++
		}
	}
	return
}

// averaged runs sec seconds and returns thirds averaged over them (the
// pressure in a crush moves from person to person, so one tick is noisy).
func averaged(w *World, sec float64) (fp, fd, bp, bd float64) {
	n := 0.0
	for end := w.T + sec; w.T < end-1e-9; {
		w.Step()
		a, b, c, d, _ := thirds(w)
		fp, fd, bp, bd, n = fp+a, fd+b, bp+c, bd+d, n+1
	}
	return fp / n, fd / n, bp / n, bd / n
}

// backEdge is how far from the barrier the crowd reaches (90th percentile, m).
func backEdge(w *World) float64 {
	var ys []float64
	for _, a := range w.Agents() {
		ys = append(ys, a.Y-w.G.BarrierY)
	}
	sort.Float64s(ys)
	return ys[len(ys)*9/10]
}

// TestCrushPersists: a surge that is left alone does not fade. After two
// (and three) minutes the front third is still packed past the danger
// density and still under pressure, far more than the back third; people
// are still being crushed; and only "calm" lets it go, from the back.
func TestCrushPersists(t *testing.T) {
	t.Parallel()
	for _, seed := range []int64{3, 11} {
		w := newWorld(t, 250, seed)
		w.Apply(Action{Type: ActStage})
		run(w, 20)
		w.Apply(Action{Type: ActSurge, Strength: f64(0.7)})
		run(w, 30) // the first impact is over
		p15, d15, _, _ := averaged(w, 10)
		for _, at := range []float64{120, 180} {
			run(w, 20+at-5-w.T)
			fp, fd, bp, bd := averaged(w, 5)
			tr := w.Truth()
			t.Logf("seed %d, %3.0f s of surge: front third %.0f N/m, %.1f /m² (was %.0f, %.1f at 30–40 s); back third %.0f N/m, %.1f /m²; %d crushing, max %.0f N/m",
				seed, at, fp, fd, p15, d15, bp, bd, tr.Crushing, tr.MaxPressure)
			if fd < 4 {
				t.Errorf("seed %d, %.0f s: front third at %.1f /m², want ≥ 4 (danger density)", seed, at, fd)
			}
			if fd < bd+1.5 {
				t.Errorf("seed %d, %.0f s: front third %.1f /m² not well above the back third (%.1f)", seed, at, fd, bd)
			}
			if fp < 300 || fp < 0.6*p15 {
				t.Errorf("seed %d, %.0f s: front-third pressure %.0f N/m faded (was %.0f at 30–40 s)", seed, at, fp, p15)
			}
			if bp > fp/10 {
				t.Errorf("seed %d, %.0f s: back-third pressure %.0f N/m, front %.0f: no gradient", seed, at, bp, fp)
			}
			if !tr.Dangerous || tr.Crushing < dangerPressureCount || tr.MaxPressure < CrushPressure {
				t.Errorf("seed %d, %.0f s: truth no longer dangerous (%d crushing, max %.0f N/m)", seed, at, tr.Crushing, tr.MaxPressure)
			}
		}
		// The director says calm: pressure goes, and the crowd loosens from
		// the back while the front rows are still where they were.
		_, _, _, _, front0 := thirds(w)
		back0 := backEdge(w)
		w.Churn, w.Trips = false, false
		w.Apply(Action{Type: ActCalm})
		tBack, tFront := -1.0, -1.0
		for t0 := w.T; w.T < t0+10-1e-9; {
			w.Step()
			if tBack < 0 && backEdge(w) >= back0+0.3 {
				tBack = w.T - t0
			}
			if _, d, _, _, _ := thirds(w); tFront < 0 && d < 3.5 {
				tFront = w.T - t0
			}
		}
		run(w, 10)
		fp, fd, _, _, front := thirds(w)
		t.Logf("seed %d, 20 s after calm: front third %.0f N/m, %.1f /m²; %d of %d still within 1 m of the barrier; back edge %.1f → %.1f m (moved after %.1f s; front under 3.5 /m² after %.1f s)",
			seed, fp, fd, front, front0, back0, backEdge(w), tBack, tFront)
		if tBack < 0 || tFront < 0 || tBack > tFront {
			t.Errorf("seed %d: the crowd did not loosen from the back first (back moved after %.1f s, front loosened after %.1f s)", seed, tBack, tFront)
		}
		if fp > 50 || w.Truth().Crushing > 0 {
			t.Errorf("seed %d: 20 s after calm the front third is still at %.0f N/m (%d crushing)", seed, fp, w.Truth().Crushing)
		}
		run(w, 40)
		_, fd2, _, bd2, front2 := thirds(w)
		t.Logf("seed %d, 60 s after calm: front third %.1f /m², back third %.1f /m², %d within 1 m of the barrier, back edge %.1f m", seed, fd2, bd2, front2, backEdge(w))
		if fd2 > 3 || fd2 > fd+0.1 {
			t.Errorf("seed %d: the front did not stay loose after calm (%.1f → %.1f /m²)", seed, fd, fd2)
		}
	}
}

// TestTurbulence: the squeezed part of a surge is not frozen. Helbing,
// Johansson & Al-Abideen (2007) put the onset of crowd turbulence at a
// "crowd pressure" ρ·Var(v) of 0.02 /s²; the people under compression here
// are past it, and a calm crowd is nowhere near.
func TestTurbulence(t *testing.T) {
	t.Parallel()
	crowdPressure := func(w *World, sec float64, squeezed bool) float64 {
		type acc struct{ n, sx, sy, sxx, syy, d, p float64 }
		m := map[int]*acc{}
		for end := w.T + sec; w.T < end-1e-9; {
			w.Step()
			for _, a := range w.Agents() {
				c := m[a.ID]
				if c == nil {
					c = &acc{}
					m[a.ID] = c
				}
				c.n++
				c.sx, c.sy = c.sx+a.VX, c.sy+a.VY
				c.sxx, c.syy = c.sxx+a.VX*a.VX, c.syy+a.VY*a.VY
				c.d += w.PackedDensity(a)
				c.p += a.Pressure
			}
		}
		cp, n := 0.0, 0.0
		for _, c := range m {
			if squeezed && c.p/c.n < struggleFrom {
				continue
			}
			v := c.sxx/c.n - (c.sx/c.n)*(c.sx/c.n) + c.syy/c.n - (c.sy/c.n)*(c.sy/c.n)
			cp += c.d / c.n * v
			n++
		}
		if n == 0 {
			return 0
		}
		return cp / n
	}
	calm := stillWorld(t, 250, 3)
	run(calm, 8)
	if cp := crowdPressure(calm, 10, false); cp > 0.005 {
		t.Errorf("calm crowd: crowd pressure %.4f /s², want ~0", cp)
	}
	w := newWorld(t, 250, 3)
	w.Apply(Action{Type: ActStage})
	run(w, 20)
	w.Apply(Action{Type: ActSurge, Strength: f64(0.7)})
	for _, at := range []float64{40, 120} {
		run(w, 20+at-w.T)
		cp := crowdPressure(w, 10, true)
		t.Logf("%.0f s of surge: crowd pressure of the squeezed people %.3f /s²", at, cp)
		if cp < 0.02 {
			t.Errorf("%.0f s of surge: crowd pressure %.4f /s², want ≥ 0.02 (turbulent)", at, cp)
		}
	}
	if ov := maxOverlap(w); ov > 0.15 {
		t.Errorf("bodies overlap by %.2f m", ov)
	}
}

// TestPeelAway: some of the people at the loose back of a surge step back
// and watch; nobody in the packed part can, and most keep pushing.
func TestPeelAway(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 250, 3)
	w.Apply(Action{Type: ActStage})
	run(w, 20)
	w.Apply(Action{Type: ActSurge, Strength: f64(0.7)})
	run(w, 120)
	n := 0
	for _, a := range w.Agents() {
		if a.peel == peelNo {
			continue
		}
		n++
		if a.Pressure > 0 || w.PackedDensity(a) > 3 || a.Y-w.G.BarrierY < peelFront {
			t.Errorf("person %d stepped back but is at %.0f N/m, %.1f /m², %.1f m from the barrier",
				a.ID, a.Pressure, w.PackedDensity(a), a.Y-w.G.BarrierY)
		}
	}
	t.Logf("%d of %d people stepped back from the surge", n, len(w.Agents()))
	if n < 3 || float64(n) > peelShare*float64(len(w.Agents())) {
		t.Errorf("%d people stepped back, want a few (at most %.0f %%)", n, 100*peelShare)
	}
	// A new surge order brings them back in.
	w.Apply(Action{Type: ActSurge, Strength: f64(0.7)})
	for _, a := range w.Agents() {
		if a.peel != peelNo {
			t.Fatalf("person %d still stepped back after a new surge", a.ID)
		}
	}
	if math.IsNaN(w.Truth().MaxPressure) {
		t.Fatal("NaN pressure")
	}
}
