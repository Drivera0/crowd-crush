package detect

import (
	"fmt"
	"math"
	"math/rand"
	"testing"

	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/sim"
)

// outcome summarises a run through the detector.
type outcome struct {
	maxLevel     string         // worst level any zone reached
	redAt        float64        // seconds until the first red (-1 if never)
	yellowAt     float64        // seconds until the first yellow or worse (-1 if never)
	waveSteps    int            // steps with at least one wave edge
	handlingSeen bool           // some node was marked handling
	swayingSeen  bool           // some node was marked swaying or wave
	directions   map[string]int // wave direction counts at red
	finalLevels  map[string]string
}

var levelRank = map[string]int{protocol.LevelCalm: 0, protocol.LevelYellow: 1, protocol.LevelRed: 2}

// clockErrMs adds a fixed clock-sync error per phone, like real phones after
// NTP-style correction.
func runScenario(t *testing.T, name string, n int, dur float64, clockErrMs int64) outcome {
	return runScenarioFlip(t, name, n, dur, clockErrMs, false)
}

// flipOdd holds every other phone upside down (x axis negated).
func runScenarioFlip(t *testing.T, name string, n int, dur float64, clockErrMs int64, flipOdd bool) outcome {
	t.Helper()
	return runScenarioCfg(t, name, n, dur, clockErrMs, flipOdd, 42, DefaultConfig())
}

// runScenarioCfg runs one scenario in the line layout with a given
// simulator seed and config.
func runScenarioCfg(t *testing.T, name string, n int, dur float64, clockErrMs int64, flipOdd bool, seed int64, cfg Config) outcome {
	t.Helper()
	return runScenarioLayout(t, name, n, dur, clockErrMs, flipOdd, seed, cfg, sim.LineLayout(1, n))
}

// runScenarioLayout runs one scenario with phones placed by lay. Moving
// phones report their position every 500 ms, like the phone page does.
func runScenarioLayout(t *testing.T, name string, n int, dur float64, clockErrMs int64, flipOdd bool, seed int64, cfg Config, lay sim.Layout) outcome {
	t.Helper()
	sc, err := sim.NewLayout(name, n, seed, lay)
	if err != nil {
		t.Fatal(err)
	}
	d := New(cfg)
	if lay.Kind == sim.LayoutLine {
		d.SetZones(lineZones(cfg, lay.Cols))
	}
	rng := rand.New(rand.NewSource(7))
	errs := make([]int64, n)
	for i := 0; i < n; i++ {
		x, y := sc.Pos(i)
		d.SetPhone(fmt.Sprint(i), x, y)
		if clockErrMs > 0 {
			errs[i] = rng.Int63n(2*clockErrMs+1) - clockErrMs
		}
	}
	const t0 = 1_700_000_000_000
	evs := sc.Generate(t0, dur)
	out := outcome{maxLevel: protocol.LevelCalm, redAt: -1, yellowAt: -1, directions: map[string]int{}}
	j := 0
	moves := sc.Moves()
	for now := int64(t0); now <= t0+int64(dur*1000); now += 250 {
		if moves && (now-t0)%500 == 0 {
			for i := 0; i < n; i++ {
				x, y := sc.PosAt(i, float64(now-t0)/1000)
				d.SetPhone(fmt.Sprint(i), x, y)
			}
		}
		for j < len(evs) && evs[j].T <= now {
			e := evs[j]
			ax := e.AX
			if flipOdd && e.Phone%2 == 1 {
				ax = -ax
			}
			d.Add(fmt.Sprint(e.Phone), Sample{T: e.T + errs[e.Phone], AX: ax, AY: e.AY, AZ: e.AZ, Rot: e.Rot})
			j++
		}
		r := d.Step(now)
		if len(r.Waves()) > 0 {
			out.waveSteps++
		}
		for _, p := range r.Phones {
			out.handlingSeen = out.handlingSeen || p.Status == protocol.StatusHandling
			out.swayingSeen = out.swayingSeen || p.Status == protocol.StatusSwaying || p.Status == protocol.StatusWave
		}
		for _, z := range r.Zones {
			if levelRank[z.Level] > levelRank[out.maxLevel] {
				out.maxLevel = z.Level
			}
			if z.Level != protocol.LevelCalm && out.yellowAt < 0 {
				out.yellowAt = float64(now-t0) / 1000
			}
			if z.Level == protocol.LevelRed {
				if out.redAt < 0 {
					out.redAt = float64(now-t0) / 1000
				}
				out.directions[z.Direction]++
			}
		}
		out.finalLevels = map[string]string{}
		for _, z := range r.Zones {
			out.finalLevels[z.ID] = z.Level
		}
	}
	return out
}

func TestScenarios(t *testing.T) {
	tests := []struct {
		scenario     string
		dur          float64
		wantMaxLevel string // worst level allowed / required
		exact        bool   // maxLevel must equal wantMaxLevel
		wantRedBy    float64
		wantHandling bool
		wantSway     bool
		maxWaveSteps int // -1 = don't care
	}{
		{scenario: "calm", dur: 60, wantMaxLevel: "calm", exact: true, maxWaveSteps: 0},
		{scenario: "walk", dur: 60, wantMaxLevel: "calm", exact: true, maxWaveSteps: 8},
		{scenario: "dance", dur: 60, wantMaxLevel: "calm", exact: true, wantSway: true, maxWaveSteps: 8},
		{scenario: "handle", dur: 60, wantMaxLevel: "calm", exact: true, wantHandling: true, maxWaveSteps: 0},
		{scenario: "shove", dur: 40, wantMaxLevel: "yellow", maxWaveSteps: -1, wantSway: true},
		{scenario: "wave", dur: 70, wantMaxLevel: "red", exact: true, wantRedBy: 30, wantSway: true, maxWaveSteps: -1},
		// The same wave while everyone jumps to a beat: the vertical veto must
		// not hide it (jumping is rhythmic, so it never vetoes). Slower than a
		// clean wave because the jumping leaks into x as noise.
		{scenario: "wave-jump", dur: 90, wantMaxLevel: "red", exact: true, wantRedBy: 75, wantSway: true, maxWaveSteps: -1},

		// False-positive scenarios: real crowd behaviour that looks a bit like
		// a travelling wave.
		//
		// Swaying to music with a lag gradient: periodic, so the mirror peak
		// half a period away makes the lag ambiguous (0.5 Hz); at 0.2 Hz the
		// mirror is out of the lag range, but the amplitude is small and the
		// few edges that pass don't form chains.
		{scenario: "sway", dur: 90, wantMaxLevel: "calm", exact: true, wantSway: true, maxWaveSteps: 8},
		{scenario: "sway-slow", dur: 90, wantMaxLevel: "calm", exact: true, wantSway: true, maxWaveSteps: 80},
		// Stadium wave: travels at 250 ms/person like a crush wave, but it is
		// vertical (vetoed: stronger, non-rhythmic vertical motion travelling
		// with it). Without the veto this reaches red.
		{scenario: "mexican", dur: 90, wantMaxLevel: "calm", exact: true, maxWaveSteps: 8},
		// One person squeezing past is a genuine single travelling jolt, the
		// same shape as a shove: a brief yellow is the honest answer, red would
		// need it to keep coming (the 8 s score smoothing makes a single event
		// of ≤ 6 s unable to reach 0.6). TestSingleEventsDecay checks it clears.
		{scenario: "walkpast", dur: 60, wantMaxLevel: "yellow", maxWaveSteps: -1},
		// A procession walking past, lightly brushing about half the phones:
		// travelling but gappy and gentle, so hops rarely chain. Yellow allowed
		// (repeated travelling contact is borderline), calm expected.
		{scenario: "procession", dur: 90, wantMaxLevel: "yellow", maxWaveSteps: 60},
		// The line walks off together with nearly the same cadence: periodic.
		{scenario: "march", dur: 90, wantMaxLevel: "calm", exact: true, maxWaveSteps: 8},
		// Pockets, drops and fumbles: independent per phone, never a chain.
		{scenario: "pocket", dur: 90, wantMaxLevel: "calm", exact: true, wantHandling: true, maxWaveSteps: 4},
		// Neighbours bumping pairwise: isolated edges, removed by the chain rule.
		{scenario: "bump", dur: 90, wantMaxLevel: "calm", exact: true, maxWaveSteps: 20},
		// Jumping to a beat with 0–300 ms reaction delays: vertical and periodic.
		{scenario: "jump-stagger", dur: 90, wantMaxLevel: "calm", exact: true, maxWaveSteps: 8},
	}
	for _, tt := range tests {
		for _, clockErr := range []int64{0, 25} {
			t.Run(fmt.Sprintf("%s/clockerr=%d", tt.scenario, clockErr), func(t *testing.T) {
				o := runScenario(t, tt.scenario, 8, tt.dur, clockErr)
				t.Logf("max=%s redAt=%.1f waveSteps=%d handling=%v sway=%v dirs=%v",
					o.maxLevel, o.redAt, o.waveSteps, o.handlingSeen, o.swayingSeen, o.directions)
				if tt.exact && o.maxLevel != tt.wantMaxLevel {
					t.Errorf("max level %s, want %s", o.maxLevel, tt.wantMaxLevel)
				}
				if !tt.exact && levelRank[o.maxLevel] > levelRank[tt.wantMaxLevel] {
					t.Errorf("max level %s, want at most %s", o.maxLevel, tt.wantMaxLevel)
				}
				if tt.wantRedBy > 0 && (o.redAt < 0 || o.redAt > tt.wantRedBy) {
					t.Errorf("red at %.1fs, want by %.0fs", o.redAt, tt.wantRedBy)
				}
				if tt.wantMaxLevel == "red" {
					for d := range o.directions {
						if d != "+x" {
							t.Errorf("wave direction %q, want +x", d)
						}
					}
				}
				if tt.wantHandling && !o.handlingSeen {
					t.Error("no node was ever marked handling")
				}
				if tt.wantSway && !o.swayingSeen {
					t.Error("no node was ever marked swaying")
				}
				if tt.maxWaveSteps >= 0 && o.waveSteps > tt.maxWaveSteps {
					t.Errorf("%d steps with wave edges, want ≤ %d", o.waveSteps, tt.maxWaveSteps)
				}
			})
		}
	}
}

func TestFlippedPhonesStillSeeWave(t *testing.T) {
	o := runScenarioFlip(t, "wave", 8, 70, 25, true)
	if o.maxLevel != protocol.LevelRed || o.redAt > 60 {
		t.Fatalf("max %s red at %.1f s; want red within 60 s", o.maxLevel, o.redAt)
	}
	if c := runScenarioFlip(t, "dance", 8, 60, 25, true); c.maxLevel != protocol.LevelCalm {
		t.Fatalf("flipped dance reached %s", c.maxLevel)
	}
}

func TestShoveDecays(t *testing.T) {
	o := runScenario(t, "shove", 8, 60, 0)
	for z, l := range o.finalLevels {
		if l != protocol.LevelCalm {
			t.Errorf("zone %s still %s 50 s after a single shove", z, l)
		}
	}
}

// TestSingleEventsDecay: one person walking past is at most a brief yellow.
func TestSingleEventsDecay(t *testing.T) {
	o := runScenario(t, "walkpast", 8, 60, 0)
	if o.maxLevel == protocol.LevelRed {
		t.Errorf("walkpast reached red")
	}
	for z, l := range o.finalLevels {
		if l != protocol.LevelCalm {
			t.Errorf("zone %s still %s 45 s after one person walked past", z, l)
		}
	}
}

// TestFalsePositivesAcrossSeeds runs the false-positive scenarios with other
// random crowds (timings, amplitudes, tilts) so the outcomes don't hinge on
// one lucky seed, and checks the true positives still fire in each.
func TestFalsePositivesAcrossSeeds(t *testing.T) {
	atMost := map[string]string{
		"sway": "calm", "sway-slow": "calm", "mexican": "calm", "march": "calm",
		"pocket": "calm", "bump": "calm", "jump-stagger": "calm",
		"walkpast": "yellow", "procession": "yellow",
	}
	for seed := int64(1); seed <= 8; seed++ {
		for name, want := range atMost {
			o := runScenarioCfg(t, name, 8, 90, 25, false, seed, DefaultConfig())
			if levelRank[o.maxLevel] > levelRank[want] {
				t.Errorf("%s seed %d reached %s, want at most %s", name, seed, o.maxLevel, want)
			}
		}
		if o := runScenarioCfg(t, "wave", 8, 70, 25, false, seed, DefaultConfig()); o.redAt < 0 || o.redAt > 30 {
			t.Errorf("wave seed %d red at %.1f s, want within 30 s", seed, o.redAt)
		}
		if o := runScenarioCfg(t, "wave-jump", 8, 90, 25, false, seed, DefaultConfig()); o.redAt < 0 {
			t.Errorf("wave-jump seed %d never reached red", seed)
		}
	}
}

// TestGuardsAreNeeded keeps the false-positive scenarios honest: each guard
// must actually be what stops its scenario, otherwise the scenario is too
// easy to prove anything.
func TestGuardsAreNeeded(t *testing.T) {
	noVeto := DefaultConfig()
	noVeto.VerticalRatio = 0
	if o := runScenarioCfg(t, "mexican", 8, 90, 25, false, 42, noVeto); o.maxLevel == protocol.LevelCalm {
		t.Errorf("mexican stays calm even without the vertical veto")
	}
	noChain := DefaultConfig()
	noChain.MinChain = 0
	with := runScenarioCfg(t, "bump", 8, 90, 25, false, 42, DefaultConfig())
	without := runScenarioCfg(t, "bump", 8, 90, 25, false, 42, noChain)
	if without.waveSteps < 2*with.waveSteps+10 {
		t.Errorf("bump: %d steps with wave edges without chains, %d with; the chain rule should remove most", without.waveSteps, with.waveSteps)
	}
}

func TestXcorrFindsLag(t *testing.T) {
	const n, step = 121, 50
	a := make([]float64, n)
	b := make([]float64, n)
	va := make([]bool, n)
	for i := range a {
		tt := float64(i * step)
		a[i] = math.Sin(2 * math.Pi * 0.5 * tt / 1000)
		b[i] = math.Sin(2 * math.Pi * 0.5 * (tt - 230) / 1000) // b lags a by 230 ms
		va[i] = true
	}
	lag, corr, _, ok := xcorr(a, b, va, va, 30, 91, false)
	if !ok || corr < 0.95 {
		t.Fatalf("corr=%v ok=%v", corr, ok)
	}
	if ms := lag * step; math.Abs(ms-230) > 15 {
		t.Fatalf("lag %.0f ms, want ~230", ms)
	}
}

func TestStaleAndZones(t *testing.T) {
	cfg := DefaultConfig()
	d := New(cfg)
	if got := d.ZoneOf(13, 5); got != "B" {
		t.Fatalf("zone of x=13 = %s", got)
	}
	if got := d.ZoneOf(cfg.VenueW, cfg.VenueH); got != "B" {
		t.Fatalf("zone of the bottom-right corner = %q", got)
	}
	d.SetPhone("a", 0, 0)
	d.Add("a", Sample{T: 1000})
	r := d.Step(1000 + cfg.StaleMs + 1)
	if r.Phones[0].Status != protocol.StatusStale {
		t.Fatalf("status %s, want stale", r.Phones[0].Status)
	}
	if ZoneName(0) != "A" || ZoneName(25) != "Z" || ZoneName(26) != "AA" {
		t.Fatal("ZoneName")
	}
}

// lineZones splits a line of cols phones into zones of 4 phones each, the
// way the grid detector did, so the line scenarios keep their outcomes.
func lineZones(cfg Config, cols int) []ZoneDef {
	var out []ZoneDef
	for c0, i := 0, 0; c0 < cols; c0, i = c0+4, i+1 {
		x0, _ := cfg.LegacyPos(0, c0)
		x0 -= cfg.LegacySpacing / 2
		x1 := x0 + 4*cfg.LegacySpacing
		if c0 == 0 {
			x0 = 0
		}
		if c0+4 >= cols {
			x1 = cfg.VenueW
		}
		out = append(out, ZoneDef{ID: ZoneName(i), Name: "Zone " + ZoneName(i), Poly: Rect(x0, 0, x1, cfg.VenueH)})
	}
	return out
}
