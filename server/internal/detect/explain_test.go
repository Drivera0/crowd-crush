package detect

import (
	"fmt"
	"math"
	"testing"

	"github.com/Drivera0/crowd-crush/server/internal/sim"
)

// stepUntil runs a line scenario through d and stops at the first step
// where stop says so (or at the end); it returns that step's result.
func stepUntil(t *testing.T, name string, n int, dur float64, stop func(Result) bool) (*Detector, Result) {
	t.Helper()
	sc, err := sim.NewLayout(name, n, 42, sim.LineLayout(1, n))
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	d := New(cfg)
	d.SetZones(lineZones(cfg, n))
	for i := 0; i < n; i++ {
		x, y := sc.Pos(i)
		d.SetPhone(fmt.Sprint(i), x, y)
	}
	const t0 = 1_700_000_000_000
	evs := sc.Generate(t0, dur)
	j := 0
	var r Result
	for now := int64(t0); now <= t0+int64(dur*1000); now += 250 {
		for j < len(evs) && evs[j].T <= now {
			e := evs[j]
			d.Add(fmt.Sprint(e.Phone), Sample{T: e.T, AX: e.AX, AY: e.AY, AZ: e.AZ, Rot: e.Rot})
			j++
		}
		r = d.Step(now)
		if stop(r) {
			break
		}
	}
	return d, r
}

func TestExplainWaveEdge(t *testing.T) {
	d, r := stepUntil(t, "wave", 8, 40, func(r Result) bool { return len(r.Waves()) >= 3 })
	w := r.Waves()
	if len(w) == 0 {
		t.Fatal("no wave edge")
	}
	e := w[0]
	ex, ok := d.Explain(e.To, e.From) // either order
	if !ok {
		t.Fatal("wave edge not explained")
	}
	cfg := d.Config()
	n := int(cfg.CorrWindowMs/cfg.StepMs) + 1
	lags := 2*int(cfg.MaxLagMs/cfg.StepMs) + 1
	if ex.From != e.From || ex.To != e.To || !ex.Wave || ex.LagMs != e.LagMs || ex.StepMs != cfg.StepMs {
		t.Errorf("explain %s→%s wave %v lag %d, edge %+v", ex.From, ex.To, ex.Wave, ex.LagMs, e)
	}
	if len(ex.A) != n || len(ex.B) != n || len(ex.Lags) != lags || len(ex.Corr) != lags ||
		ex.Lags[0] != -cfg.MaxLagMs || ex.Lags[lags-1] != cfg.MaxLagMs {
		t.Errorf("sizes a %d b %d lags %d corr %d (first lag %d)", len(ex.A), len(ex.B), len(ex.Lags), len(ex.Corr), ex.Lags[0])
	}
	if math.Abs(ex.Peak-e.Corr) > 0.002 || ex.Second >= ex.Peak {
		t.Errorf("peak %.3f second %.3f, edge corr %.3f", ex.Peak, ex.Second, e.Corr)
	}
	// The curve's maximum is the peak, near the edge's lag.
	best, at := -1.0, int64(0)
	for i, c := range ex.Corr {
		if c != nil && *c > best {
			best, at = *c, ex.Lags[i]
		}
	}
	if math.Abs(best-ex.Peak) > 1e-9 || math.Abs(float64(at-e.LagMs)) > float64(cfg.StepMs) {
		t.Errorf("curve max %.3f at %d ms, peak %.3f lag %d", best, at, ex.Peak, e.LagMs)
	}
	if len(ex.Checks) != 7 {
		t.Fatalf("%d checks: %+v", len(ex.Checks), ex.Checks)
	}
	for _, c := range ex.Checks {
		if !c.Pass || c.Detail == "" {
			t.Errorf("wave edge failed check %+v", c)
		}
	}
	t.Logf("%+v", ex.Checks)
	if _, ok := d.Explain(e.From, "nobody"); ok {
		t.Error("explained a pair that isn't a neighbour pair")
	}
}

// TestExplainDance: moving together has no wave edges; the explanation
// says which test failed (the lag is ~0).
func TestExplainDance(t *testing.T) {
	d, r := stepUntil(t, "dance", 8, 20, func(Result) bool { return false })
	if len(r.Edges) == 0 {
		t.Fatal("no edges")
	}
	e := r.Edges[0]
	ex, ok := d.Explain(e.From, e.To)
	if !ok || ex.Wave {
		t.Fatalf("ok %v wave %v", ok, ex.Wave)
	}
	failed := map[string]bool{}
	for _, c := range ex.Checks {
		if !c.Pass {
			failed[c.Name] = true
		}
	}
	if !failed["Wave-like lag"] && !failed["Unambiguous peak"] {
		t.Errorf("dance edge: checks %+v", ex.Checks)
	}
	t.Logf("%+v", ex.Checks)
}
