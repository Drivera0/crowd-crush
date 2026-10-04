package detect

import (
	"fmt"
	"math"
	"math/rand"
	"testing"

	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/sim"
)

// runRough runs a scenario with every phone's position off by a fixed
// random error (median errM metres, as a GPS bias is over a minute or two)
// and a reported accuracy to match (the 68 % radius).
func runRough(t *testing.T, name string, n int, dur float64, seed int64, cfg Config, lay sim.Layout, errM float64) outcome {
	t.Helper()
	sc, err := sim.NewLayout(name, n, seed, lay)
	if err != nil {
		t.Fatal(err)
	}
	d := New(cfg)
	if lay.Kind == sim.LayoutLine {
		d.SetZones(lineZones(cfg, lay.Cols))
	}
	rng := rand.New(rand.NewSource(seed*31 + 5))
	sigma := errM / 1.1774 // median of a 2-D error → per-axis σ
	ex, ey, errs := make([]float64, n), make([]float64, n), make([]int64, n)
	place := func(i int, tt float64) {
		x, y := sc.PosAt(i, tt)
		x, y = cfg.Fold(x+ex[i], y+ey[i])
		id := fmt.Sprint(i)
		d.SetPhone(id, x, y)
		d.SetAccuracy(id, 1.51*sigma)
	}
	for i := 0; i < n; i++ {
		ex[i], ey[i] = sigma*rng.NormFloat64(), sigma*rng.NormFloat64()
		errs[i] = rng.Int63n(51) - 25
		place(i, 0)
	}
	const t0 = 1_700_000_000_000
	evs := sc.Generate(t0, dur)
	out := outcome{maxLevel: protocol.LevelCalm, redAt: -1, yellowAt: -1, directions: map[string]int{}}
	j := 0
	for now := int64(t0); now <= t0+int64(dur*1000); now += 250 {
		if sc.Moves() && (now-t0)%500 == 0 {
			for i := 0; i < n; i++ {
				place(i, float64(now-t0)/1000)
			}
		}
		for j < len(evs) && evs[j].T <= now {
			e := evs[j]
			d.Add(fmt.Sprint(e.Phone), Sample{T: e.T + errs[e.Phone], AX: e.AX, AY: e.AY, AZ: e.AZ, Rot: e.Rot})
			j++
		}
		r := d.Step(now)
		if len(r.Waves()) > 0 {
			out.waveSteps++
		}
		for _, e := range r.Edges {
			if !e.Motion {
				t.Fatalf("%s: a pair of roughly placed phones was paired by distance", name)
			}
		}
		for _, z := range r.Zones {
			if levelRank[z.Level] > levelRank[out.maxLevel] {
				out.maxLevel = z.Level
			}
			if z.Level == protocol.LevelRed && out.redAt < 0 {
				out.redAt = float64(now-t0) / 1000
			}
		}
	}
	return out
}

// TestRoughPositionsWave: with every position 5 m off (median), the push
// wave is still found, by motion, on the line and in a crowd; with accuracy
// ignored (AccPairScale 0: the phones are taken to stand where they say)
// it is not, which is what the map-only detector did.
func TestRoughPositionsWave(t *testing.T) {
	for seed := int64(31); seed <= 34; seed++ {
		for _, lay := range []sim.Layout{sim.LineLayout(1, 8), sim.CrowdLayout(true)} {
			n := 8
			if lay.Kind == sim.LayoutCrowd {
				n = crowdN
			}
			for _, name := range []string{"wave", "wave-jump"} {
				o := runRough(t, name, n, 90, seed, DefaultConfig(), lay, 5)
				if o.redAt < 0 {
					t.Errorf("%s %s seed %d: never red with 5 m position error (max %s)", name, lay.Kind, seed, o.maxLevel)
				}
			}
		}
	}
	off := DefaultConfig()
	off.AccPairScale = 0
	reds := 0
	for seed := int64(31); seed <= 34; seed++ {
		lay := sim.LineLayout(1, 8)
		sc, _ := sim.NewLayout("wave", 8, seed, lay)
		d := New(off)
		d.SetZones(lineZones(off, lay.Cols))
		rng := rand.New(rand.NewSource(seed*31 + 5))
		for i := 0; i < 8; i++ {
			x, y := sc.Pos(i)
			d.SetPhone(fmt.Sprint(i), x+4.2*rng.NormFloat64(), y+4.2*rng.NormFloat64())
			d.SetAccuracy(fmt.Sprint(i), 6.4)
		}
		const t0 = 1_700_000_000_000
		evs := sc.Generate(t0, 70)
		j := 0
		for now := int64(t0); now <= t0+70_000; now += 250 {
			for j < len(evs) && evs[j].T <= now {
				e := evs[j]
				d.Add(fmt.Sprint(e.Phone), Sample{T: e.T, AX: e.AX, AY: e.AY, AZ: e.AZ, Rot: e.Rot})
				j++
			}
			for _, z := range d.Step(now).Zones {
				if z.Level == protocol.LevelRed {
					reds++
					now = t0 + 70_000
					break
				}
			}
		}
	}
	if reds > 1 {
		t.Errorf("with accuracy ignored, %d/4 scrambled lines still went red: the test proves nothing", reds)
	}
}

// TestRoughPositionsLookAlikes: finding neighbours by motion among everyone
// within metres must not turn the look-alikes into alarms. None may go red;
// the ones that are not a travelling horizontal disturbance at all must
// stay calm.
func TestRoughPositionsLookAlikes(t *testing.T) {
	atMost := map[string]string{
		"calm": "calm", "walk": "calm", "dance": "calm", "handle": "calm", "march": "calm",
		"pocket": "calm", "bump": "calm", "jump-stagger": "calm",
		// Travelling, but not a push: yellow is tolerated, as it is for the
		// single shove and the people squeezing past with exact positions.
		// (A slow sway passed from row to row and a stadium wave stay calm
		// with exact positions because of where the phones stand; rough
		// positions can't say that, and some crowds reach yellow.)
		"sway": "yellow", "sway-slow": "yellow", "mexican": "yellow",
		"shove": "yellow", "walkpast": "yellow", "procession": "yellow",
	}
	for seed := int64(31); seed <= 33; seed++ {
		for name, want := range atMost {
			for _, lay := range []sim.Layout{sim.LineLayout(1, 8), sim.CrowdLayout(true)} {
				n := 8
				if lay.Kind == sim.LayoutCrowd {
					n = crowdN
				}
				o := runRough(t, name, n, 90, seed, DefaultConfig(), lay, 5)
				if levelRank[o.maxLevel] > levelRank[want] {
					t.Errorf("%s %s seed %d reached %s with 5 m position error, want at most %s", name, lay.Kind, seed, o.maxLevel, want)
				}
			}
		}
	}
}

// pulse is one shove: a damped sway starting at t = 0.
func pulse(t float64) float64 {
	if t < 0 {
		return 0
	}
	return math.Exp(-t/0.9) * math.Sin(2*math.Pi*0.6*t)
}

// feedPulses gives each phone a repeating shove delayed by its lag (s) and
// returns the detector after 12 s, ready to step.
func feedPulses(t *testing.T, cfg Config, lags map[string]float64, acc float64) (*Detector, int64) {
	t.Helper()
	d := New(cfg)
	rng := rand.New(rand.NewSource(3))
	for id := range lags {
		d.SetPhone(id, 2+20*rng.Float64(), 2+12*rng.Float64()) // anywhere: the map says nothing
		d.SetAccuracy(id, acc)
	}
	const t0 = int64(1_700_000_000_000)
	for ms := int64(0); ms <= 12_000; ms += 100 {
		for id, lag := range lags {
			tt := float64(ms)/1000 - lag
			var x float64
			if lag < 0 { // unrelated: its own noise
				x = 0.8 * math.Sin(2*math.Pi*0.37*float64(ms)/1000+float64(len(id)))
			} else {
				for k := 0.0; k < 5; k++ {
					x += pulse(tt - 2.5*k)
				}
			}
			d.Add(id, Sample{T: t0 + ms, AX: 1.5*x + 0.02*rng.NormFloat64(), Rot: 10})
		}
	}
	return d, t0 + 12_000
}

// TestMotionChainNeedsClosure: three phones hit one after the other (lags
// that add up) are a chain; a single pair is not, however well it
// correlates, and nothing pairs phones by where the map puts them.
func TestMotionChainNeedsClosure(t *testing.T) {
	cfg := DefaultConfig()
	d, now := feedPulses(t, cfg, map[string]float64{"a": 0, "b": 0.25, "c": 0.5, "d": 0.75}, 6)
	r := d.Step(now)
	if len(r.Waves()) < 3 {
		t.Fatalf("four phones hit 250 ms apart: %d wave edges, want ≥ 3", len(r.Waves()))
	}
	for _, e := range r.Waves() {
		if !e.Motion {
			t.Errorf("edge %s–%s not marked as found by motion", e.From, e.To)
		}
	}
	ex, ok := d.Explain("a", "b")
	if !ok {
		t.Fatal("no explanation for a motion pair")
	}
	found := false
	for _, c := range ex.Checks {
		found = found || c.Name == "Neighbours by motion"
	}
	if !found {
		t.Errorf("explanation doesn't say the pair was found by motion: %+v", ex.Checks)
	}

	// Two phones only: a pair, not a chain.
	d, now = feedPulses(t, cfg, map[string]float64{"a": 0, "b": 0.25, "x": -1, "y": -1}, 6)
	if w := d.Step(now).Waves(); len(w) != 0 {
		t.Errorf("an isolated pair made %d wave edges", len(w))
	}

	// Exactly placed phones far apart are never compared.
	d, now = feedPulses(t, cfg, map[string]float64{"a": 0, "b": 0.25, "c": 0.5, "d": 0.75}, 0)
	if r := d.Step(now); len(r.Edges) != 0 {
		t.Errorf("exactly placed phones metres apart: %d pairs compared", len(r.Edges))
	}
}

// TestMotionBudget: each roughly placed phone is compared with at most
// MotionPairs others per step, however many are in range, and the pairs
// that looked like a wave hop are compared again in the next step.
func TestMotionBudget(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MotionPairs = 4
	d := New(cfg)
	const n = 60
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < n; i++ {
		id := fmt.Sprint(i)
		d.SetPhone(id, 10+rng.Float64(), 8+rng.Float64())
		d.SetAccuracy(id, 6)
	}
	const t0 = int64(1_700_000_000_000)
	for ms := int64(0); ms <= 8000; ms += 100 {
		for i := 0; i < n; i++ {
			x := 0.0
			for k := 0.0; k < 4; k++ {
				x += pulse(float64(ms)/1000 - 0.05*float64(i) - 2.5*k)
			}
			d.Add(fmt.Sprint(i), Sample{T: t0 + ms, AX: 1.5 * x, Rot: 10})
		}
	}
	d.Step(t0 + 8000)
	var sp []*phone
	for i := 0; i < n; i++ {
		sp = append(sp, d.phones[fmt.Sprint(i)])
	}
	d.seq++
	pairs := d.motionPairs(sp)
	if len(pairs) == 0 || len(pairs) > n*cfg.MotionPairs {
		t.Fatalf("%d pairs for %d phones with a budget of %d each", len(pairs), n, cfg.MotionPairs)
	}
	stuck := 0
	for _, p := range sp {
		stuck += len(p.stuck)
	}
	if stuck == 0 {
		t.Error("no motion neighbours remembered after a step full of wave hops")
	}
}

// TestShortHandling: a half-second burst of rotation (a gesture with the
// phone in the hand) masks under a second of readings; a long one masks
// its length plus the full settle time.
func TestShortHandling(t *testing.T) {
	gap := func(burstMs int64) int64 {
		d := New(DefaultConfig())
		d.SetPhone("a", 1, 1)
		const t0 = int64(1_000_000)
		var invalid int64
		for ms := int64(0); ms < 10_000; ms += 100 {
			rot := 10.0
			if ms >= 3000 && ms < 3000+burstMs {
				rot = 300
			}
			d.Add("a", Sample{T: t0 + ms, AX: 0.3 * math.Sin(float64(ms)/300), Rot: rot})
		}
		for _, pt := range d.phones["a"].pts {
			if !pt.valid {
				invalid += 100
			}
		}
		return invalid
	}
	if g := gap(500); g > 900 {
		t.Errorf("a 0.5 s gesture masked %d ms of readings, want ≤ 900", g)
	}
	if g := gap(3000); g < 3900 {
		t.Errorf("3 s of handling masked only %d ms, want the burst plus the 1 s settle", g)
	}
	old := DefaultConfig()
	old.HandlingShortMs = 0
	d := New(old)
	d.SetPhone("a", 1, 1)
	d.Add("a", Sample{T: 1000, Rot: 300})
	if d.phones["a"].handlingUntil != 1000+old.HandlingSettleMs {
		t.Error("with handlingShortMs 0 every burst should get the full settle time")
	}
}

func TestFoldAndOutside(t *testing.T) {
	cfg := DefaultConfig() // 24 × 16
	for _, c := range []struct{ x, y, wx, wy float64 }{
		{5, 5, 5, 5}, {-3, 5, 3, 5}, {26, 5, 22, 5}, {5, -2, 5, 2}, {-1, 17, 1, 15}, {-100, 5, 24, 5},
	} {
		if x, y := cfg.Fold(c.x, c.y); x != c.wx || y != c.wy {
			t.Errorf("Fold(%.0f, %.0f) = (%.0f, %.0f), want (%.0f, %.0f)", c.x, c.y, x, y, c.wx, c.wy)
		}
	}
	if cfg.Outside(-3, 5, 6) || !cfg.Outside(-7, 5, 6) || !cfg.Outside(-0.1, 5, 0) || cfg.Outside(5, 5, 0) {
		t.Error("Outside: a fix within its accuracy of the edge is inside, beyond it outside; exact positions as before")
	}
	cfg.OutsideAccFactor = 0
	if !cfg.Outside(-3, 5, 6) {
		t.Error("outsideAccFactor 0: any distance off the venue is outside")
	}
}
