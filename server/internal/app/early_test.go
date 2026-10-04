package app

import (
	"fmt"
	"math/rand"
	"os"
	"testing"

	"github.com/Drivera0/crowd-crush/server/internal/crowdsim"
	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// alertTimes runs a sim script and returns, in sim seconds, the first
// non-calm alert, the first early warning and the first red at or after
// sim second after (-1 = none), checked after every detector tick, plus the
// truth's dangerAt (-1 = never).
func alertTimes(t testing.TB, earlyWarnS float64, seed int64, dur, after float64, script func(r *simRunner, rng *rand.Rand) func(int)) (first, early, red, danger float64) {
	cfg := detect.DefaultConfig()
	cfg.EarlyWarnS = earlyWarnS
	a := New(Options{Detect: cfg, RecordingsDir: t.TempDir()})
	if err := a.startSimAt(SimStart{People: 250, Participation: 0.6, Seed: seed}, simT0); err != nil {
		t.Fatal(err)
	}
	r := &simRunner{t: t, a: a, now: simT0}
	var every func(int)
	if script != nil {
		every = script(r, rand.New(rand.NewSource(seed)))
	}
	first, early, red, danger = -1, -1, -1, -1
	seen := map[string]bool{}
	for r.sec() < dur-1e-9 {
		r.now += 50
		if every != nil && (r.now-simT0)%1000 == 0 {
			every(int(r.sec()))
		}
		a.simTick(r.now)
		if (r.now-simT0)%250 != 0 {
			continue
		}
		a.detectTick(r.now)
		a.mu.Lock()
		for _, al := range a.alerts {
			if al.Level == protocol.LevelCalm || al.Test || seen[al.ID+al.Level] || float64(al.T-simT0)/1000 < after {
				continue
			}
			seen[al.ID+al.Level] = true
			if first < 0 {
				first = r.sec()
			}
			if al.Early && early < 0 {
				early = r.sec()
			}
			if al.Level == protocol.LevelRed && red < 0 {
				red = r.sec()
			}
		}
		a.mu.Unlock()
	}
	if d := a.SimStatus().Truth.DangerAt; d != nil {
		danger = *d
	}
	return
}

func calmSurge(r *simRunner, rng *rand.Rand) func(int) {
	return func(s int) {
		if s == 30 {
			r.act(crowdsim.Action{Type: "surge", Strength: fp(0.7)})
		}
		if s > 30 && s%3 == 0 {
			r.act(crowdsim.Action{Type: "shove", X: fp(6 + 12*rng.Float64()), Y: fp(3 + 3*rng.Float64()), DX: fp(0), DY: fp(-1)})
		}
	}
}

func attractScript(r *simRunner, rng *rand.Rand) func(int) {
	return func(s int) {
		if s == 5 {
			r.act(crowdsim.Action{Type: "attract", X: fp(12), Y: fp(10)})
		}
	}
}

// TestSimEarlyWarning: on a sudden surge the density trend raises an early
// (yellow, early:true) density alert before the density alone would, and
// before the red; red itself doesn't move. A calm crowd gets no early
// warning. (Alerts before the surge at 30 s are left out.)
func TestSimEarlyWarning(t *testing.T) {
	first, early, red, danger := alertTimes(t, 30, 2, 37, 30, calmSurge)
	first0, _, red0, _ := alertTimes(t, 0, 2, 37, 30, calmSurge)
	t.Logf("early on: first %.2f early %.2f red %.2f; off: first %.2f red %.2f; danger %.2f", first, early, red, first0, red0, danger)
	if early < 0 || early != first || early >= first0 {
		t.Errorf("early warning at %.2f s, first alert %.2f s; without it first alert at %.2f s", early, first, first0)
	}
	if red != red0 {
		t.Errorf("red moved: %.2f with early warning, %.2f without", red, red0)
	}
	if f, e, _, _ := alertTimes(t, 30, 1, 40, 0, nil); e >= 0 {
		t.Errorf("calm crowd: early warning at %.2f s (first alert %.2f s)", e, f)
	}
}

// TestSimEarlySweep prints lead times with and without the early warning
// over 5 seeds (env-gated, ~15 s):
// PULSE_SIMSWEEP=1 go test ./server/internal/app -run SimEarlySweep -v
func TestSimEarlySweep(t *testing.T) {
	if os.Getenv("PULSE_SIMSWEEP") == "" {
		t.Skip("set PULSE_SIMSWEEP=1")
	}
	f := func(v float64) string {
		if v < 0 {
			return "  —  "
		}
		return fmt.Sprintf("%5.2f", v)
	}
	lead := func(danger, v float64) string {
		if v < 0 || danger < 0 {
			return "  —  "
		}
		return fmt.Sprintf("%+5.2f", danger-v)
	}
	runs := []struct {
		name   string
		dur    float64
		script func(r *simRunner, rng *rand.Rand) func(int)
	}{{"calm→surge", 50, calmSurge}, {"calm", 120, nil}, {"attract", 90, attractScript}}
	for _, run := range runs {
		for _, ew := range []float64{0, 30} {
			for seed := int64(1); seed <= 5; seed++ {
				after := 0.0
				if run.name == "calm→surge" {
					after = 30
				}
				first, early, red, danger := alertTimes(t, ew, seed, run.dur, after, run.script)
				t.Logf("%-10s earlyWarnS=%2.0f seed %d: danger %s  first alert %s (lead %s)  early %s  red %s (lead %s)",
					run.name, ew, seed, f(danger), f(first), lead(danger, first), f(early), f(red), lead(danger, red))
			}
		}
	}
}
