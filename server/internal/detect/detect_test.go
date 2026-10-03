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
	sc, err := sim.New(name, n, 1, n, 42)
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.Rows, cfg.Cols = 1, n
	d := New(cfg)
	rng := rand.New(rand.NewSource(7))
	errs := make([]int64, n)
	for i := 0; i < n; i++ {
		r, c := sc.Pos(i)
		d.SetPhone(fmt.Sprint(i), r, c)
		if clockErrMs > 0 {
			errs[i] = rng.Int63n(2*clockErrMs+1) - clockErrMs
		}
	}
	const t0 = 1_700_000_000_000
	evs := sc.Generate(t0, dur)
	out := outcome{maxLevel: protocol.LevelCalm, redAt: -1, directions: map[string]int{}}
	j := 0
	for now := int64(t0); now <= t0+int64(dur*1000); now += 250 {
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
		{scenario: "wave", dur: 70, wantMaxLevel: "red", exact: true, wantRedBy: 60, wantSway: true, maxWaveSteps: -1},
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
						if d != "+col" {
							t.Errorf("wave direction %q, want +col", d)
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
	if got := d.ZoneOf(0, 5); got != "B" {
		t.Fatalf("zone of col 5 = %s", got)
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
