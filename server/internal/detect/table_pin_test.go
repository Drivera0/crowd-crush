package detect

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/sim"
)

// TestTableDemo pins the table-demo outcomes (sim/table.go) with the
// profile off (what the detector does without it) and on, over tableSeeds
// random rows each.
func TestTableDemo(t *testing.T) {
	type want struct {
		maxLevel   string  // worst level allowed in every run
		minLevel   string  // … and reached in every run
		yellowBy   float64 // first yellow within this many seconds (0 = don't care)
		redBy      float64 // first red within this many seconds (0 = don't care)
		together   int     // runs (of tableSeeds) that must show a group moving as one; -1 = none may
		noWaveNode bool    // no node may be marked wave (red dot)
	}
	calm := want{maxLevel: protocol.LevelCalm, minLevel: protocol.LevelCalm, together: -1}
	tests := []struct {
		name    string
		n       []int
		off, on want
	}{
		{"still", []int{2, 3, 4, 5}, calm, calm},
		{"jump", []int{2, 3, 4, 5}, calm, calm},
		{"dance", []int{2, 3, 4, 5}, calm, calm},
		{"walk", []int{2, 3, 4, 5}, calm, calm},
		{"handle", []int{2, 3, 4, 5}, calm, calm},
		// Two phones: never a chain. Off: nothing at all. On: a push between
		// the two is yellow within a second or two, never red, no red dots.
		{"push", []int{2}, calm, want{maxLevel: "yellow", minLevel: "yellow", yellowBy: 3, together: -1, noWaveNode: true}},
		{"push1", []int{2}, calm, want{maxLevel: "yellow", minLevel: "yellow", yellowBy: 3, together: -1, noWaveNode: true}},
		// Three to five: a push passed along the row. Off: red in ~10 s if
		// it keeps coming, a single push yellow. On: red in ~8 s if it keeps
		// coming (a push every 3 s), a single push still only yellow.
		{"push", []int{3, 4, 5}, want{maxLevel: "red", minLevel: "red", yellowBy: 8, redBy: 13, together: -1},
			want{maxLevel: "red", minLevel: "red", yellowBy: 6, redBy: 10, together: -1}},
		{"push1", []int{3, 4, 5}, want{maxLevel: "yellow", minLevel: "yellow", yellowBy: 8, together: -1},
			want{maxLevel: "yellow", minLevel: "yellow", yellowBy: 6, together: -1}},
		// Pressed together and moved as one: invisible without the profile;
		// with it a yellow "moving as one" in most runs, never red.
		{"together", []int{2, 3, 4, 5}, calm, want{maxLevel: "yellow", together: 8}},
	}
	for _, tt := range tests {
		for _, n := range tt.n {
			for _, table := range []bool{false, true} {
				w := tt.off
				if table {
					w = tt.on
				}
				t.Run(fmt.Sprintf("%s/n=%d/table=%v", tt.name, n, table), func(t *testing.T) {
					tog := 0
					for seed := int64(1); seed <= tableSeeds; seed++ {
						o := runTable(t, tt.name, n, seed, sim.TableLeadIn+tableDur, DefaultConfig(), table)
						if o.levelBeforeStart != protocol.LevelCalm {
							t.Errorf("seed %d: %s while everyone stood still", seed, o.levelBeforeStart)
						}
						if o.togetherBeforeCase {
							t.Errorf("seed %d: moving as one while everyone stood still", seed)
						}
						if levelRank[o.maxLevel] > levelRank[w.maxLevel] || levelRank[o.maxLevel] < levelRank[w.minLevel] {
							t.Errorf("seed %d: max level %s, want %s..%s", seed, o.maxLevel, w.minLevel, w.maxLevel)
						}
						if w.yellowBy > 0 && (o.yellowAt < 0 || o.yellowAt > w.yellowBy) {
							t.Errorf("seed %d: yellow at %.1f s, want by %.0f s", seed, o.yellowAt, w.yellowBy)
						}
						if w.redBy > 0 && (o.redAt < 0 || o.redAt > w.redBy) {
							t.Errorf("seed %d: red at %.1f s, want by %.0f s", seed, o.redAt, w.redBy)
						}
						if w.noWaveNode && o.nodeWaveSteps > 0 {
							t.Errorf("seed %d: %d steps with a node marked wave", seed, o.nodeWaveSteps)
						}
						if o.togetherAt >= 0 {
							tog++
						}
					}
					if w.together < 0 && tog > 0 {
						t.Errorf("%d runs showed a group moving as one, want none", tog)
					}
					if w.together > 0 && tog < w.together {
						t.Errorf("%d of %d runs showed a group moving as one, want at least %d", tog, tableSeeds, w.together)
					}
				})
			}
		}
	}
}

// TestTableSinglePushMargin: one push along the row must not be able to
// reach red with the profile on. The wave edges of one push last about one
// correlation window, and Table.SmoothMs is chosen so that much raises the
// score to well below redScore.
func TestTableSinglePushMargin(t *testing.T) {
	cfg := DefaultConfig()
	worst := 0.0
	for n := 3; n <= 5; n++ {
		for seed := int64(1); seed <= 20; seed++ {
			worst = max(worst, runTable(t, "push1", n, seed, 30, cfg, true).maxScore)
		}
	}
	t.Logf("highest zone score after a single push: %.2f (red above %.2f)", worst, cfg.RedScore)
	if worst > cfg.RedScore-0.04 {
		t.Errorf("a single push reaches %.2f, too close to red (%.2f)", worst, cfg.RedScore)
	}
}

// TestTableSinglePushDecays: some time after one push everything is calm.
func TestTableSinglePushDecays(t *testing.T) {
	for n := 2; n <= 5; n++ {
		if o := runTable(t, "push1", n, 3, 45, DefaultConfig(), true); o.finalLevel != protocol.LevelCalm {
			t.Errorf("n=%d: still %s 40 s after a single push", n, o.finalLevel)
		}
	}
}

// runLineTable runs one of the line scenarios (sim.go: an ideal phone on
// the chest, no gravity) for n phones in the default zones (all in one
// zone), profile on or off.
func runLineTable(t *testing.T, name string, n int, seed int64, dur float64, table bool) (tableOutcome, []float64) {
	t.Helper()
	sc, err := sim.NewLayout(name, n, seed, sim.LineLayout(1, n))
	if err != nil {
		t.Fatal(err)
	}
	d := New(DefaultConfig())
	d.SetTable(table)
	for i := 0; i < n; i++ {
		x, y := sc.Pos(i)
		d.SetPhone(fmt.Sprint(i), x, y)
	}
	evs := sc.Generate(tableT0, dur)
	o := tableOutcome{maxLevel: protocol.LevelCalm, yellowAt: -1, redAt: -1, togetherAt: -1}
	var scores []float64
	j := 0
	for now := int64(tableT0); now <= tableT0+int64(dur*1000); now += 250 {
		for j < len(evs) && evs[j].T <= now {
			e := evs[j]
			d.Add(fmt.Sprint(e.Phone), Sample{T: e.T, AX: e.AX, AY: e.AY, AZ: e.AZ, Rot: e.Rot})
			j++
		}
		r := d.Step(now)
		s := float64(now-tableT0) / 1000
		if len(r.Together) > 0 && o.togetherAt < 0 {
			o.togetherAt = s
		}
		for _, z := range r.Zones {
			scores = append(scores, z.Score)
			if levelRank[z.Level] > levelRank[o.maxLevel] {
				o.maxLevel = z.Level
			}
			if z.Level != protocol.LevelCalm && o.yellowAt < 0 {
				o.yellowAt = s
			}
			if z.Level == protocol.LevelRed && o.redAt < 0 {
				o.redAt = s
			}
		}
		for _, e := range r.Edges {
			if e.Pair || e.Together {
				o.pairSteps++
			}
		}
	}
	return o, scores
}

// TestTableLineScenarios runs every line scenario (sim.go) at 2–5 phones
// with the profile on: only the true positives may go red (and only where
// they do without it), the look-alikes that must stay calm stay calm, and
// nothing is "moving as one".
func TestTableLineScenarios(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	mayRed := map[string]bool{"wave": true, "wave-jump": true}
	// Honest yellows: a push really did travel (a shove, someone squeezing
	// past, a procession brushing people, two people bumping), and the
	// stadium wave between two phones now and then.
	mayYellow := map[string]bool{"shove": true, "walkpast": true, "procession": true, "bump": true, "mexican": true}
	for _, name := range sim.Scenarios {
		if name == "gather" {
			continue
		}
		for n := 2; n <= 5; n++ {
			for seed := int64(1); seed <= 4; seed++ {
				off, _ := runLineTable(t, name, n, seed, 90, false)
				on, _ := runLineTable(t, name, n, seed, 90, true)
				switch {
				case on.togetherAt >= 0:
					t.Errorf("%s n=%d seed %d: moving as one", name, n, seed)
				case on.maxLevel == protocol.LevelRed && (!mayRed[name] || off.maxLevel != protocol.LevelRed):
					t.Errorf("%s n=%d seed %d: red with the profile (without: %s)", name, n, seed, off.maxLevel)
				case on.maxLevel == protocol.LevelYellow && !mayRed[name] && !mayYellow[name]:
					t.Errorf("%s n=%d seed %d: yellow with the profile", name, n, seed)
				}
				if n == 2 && off.maxLevel != protocol.LevelCalm {
					t.Errorf("%s n=2 seed %d: %s without the profile (two phones can't chain)", name, seed, off.maxLevel)
				}
			}
		}
	}
}

// TestTableOnlySmallZones: a zone with more than Table.MaxPhones phones
// runs exactly as without the profile, score for score.
func TestTableOnlySmallZones(t *testing.T) {
	for _, name := range []string{"wave", "sway-slow", "bump", "shove"} {
		_, off := runLineTable(t, name, 8, 42, 60, false)
		_, on := runLineTable(t, name, 8, 42, 60, true)
		if len(off) != len(on) {
			t.Fatalf("%s: %d vs %d scores", name, len(off), len(on))
		}
		for i := range off {
			if off[i] != on[i] {
				t.Fatalf("%s: score %d differs: %v off, %v on", name, i, off[i], on[i])
			}
		}
	}
}

// TestTableOffIsOff: SetTable(false) after it was on leaves no trace: the
// same input gives the same scores as a detector that never had it.
func TestTableOffIsOff(t *testing.T) {
	run := func(toggle bool) []float64 {
		sc, _ := sim.NewTable("push", 3, 5, 40)
		d := New(DefaultConfig())
		for i := 0; i < 3; i++ {
			d.SetPhone(fmt.Sprint(i), 12+0.6*float64(i), 8)
		}
		if toggle {
			d.SetTable(true)
			d.SetTable(false)
		}
		var out []float64
		evs := sc.Generate(tableT0, 40)
		j := 0
		for now := int64(tableT0); now <= tableT0+40_000; now += 250 {
			for j < len(evs) && evs[j].T <= now {
				e := evs[j]
				d.Add(fmt.Sprint(e.Phone), Sample{T: e.T, AX: e.AX, AY: e.AY, AZ: e.AZ, Rot: e.Rot, G: e.G})
				j++
			}
			for _, z := range d.Step(now).Zones {
				out = append(out, z.Score)
			}
		}
		return out
	}
	a, b := run(false), run(true)
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("score %d: %v vs %v", i, a[i], b[i])
		}
	}
}

// TestTableExplain: "Why did it fire?" says which table rule applied.
func TestTableExplain(t *testing.T) {
	check := func(name string, n int, rule string, wantPass bool, find func(Result) (Edge, bool)) {
		t.Helper()
		sc, _ := sim.NewTable(name, n, 2, 40)
		d := New(DefaultConfig())
		d.SetTable(true)
		for i := 0; i < n; i++ {
			d.SetPhone(fmt.Sprint(i), 12+0.6*float64(i), 8)
		}
		evs := sc.Generate(tableT0, 40)
		j := 0
		for now := int64(tableT0); now <= tableT0+40_000; now += 250 {
			for j < len(evs) && evs[j].T <= now {
				e := evs[j]
				d.Add(fmt.Sprint(e.Phone), Sample{T: e.T, AX: e.AX, AY: e.AY, AZ: e.AZ, Rot: e.Rot, G: e.G})
				j++
			}
			e, ok := find(d.Step(now))
			if !ok {
				continue
			}
			ex, ok := d.Explain(e.From, e.To)
			if !ok {
				t.Fatalf("%s: no explanation", name)
			}
			var names []string
			for _, c := range ex.Checks {
				names = append(names, c.Name)
				if strings.HasPrefix(c.Name, rule) {
					if c.Pass != wantPass {
						t.Errorf("%s: %q pass=%v (%s), want %v", name, c.Name, c.Pass, c.Detail, wantPass)
					}
					t.Logf("%s: %s: %s", name, c.Name, c.Detail)
					return
				}
			}
			t.Fatalf("%s: no %q check among %v", name, rule, names)
		}
		t.Fatalf("%s: the edge never appeared", name)
	}
	check("push1", 2, "Two-phone push", true, func(r Result) (Edge, bool) {
		for _, e := range r.Edges {
			if e.Pair {
				return e, true
			}
		}
		return Edge{}, false
	})
	check("together", 3, "Moving as one", true, func(r Result) (Edge, bool) {
		for _, e := range r.Edges {
			if e.Together {
				return e, true
			}
		}
		return Edge{}, false
	})
	// Dancing: the pair moves together, but to a beat.
	check("dance", 3, "Moving as one", false, func(r Result) (Edge, bool) {
		if r.T < tableT0+20_000 {
			return Edge{}, false
		}
		return r.Edges[0], len(r.Edges) > 0
	})
}

// TestTableConfig: the profile's scores must stay below red.
func TestTableConfig(t *testing.T) {
	if err := DefaultConfig().Validate(); err != nil {
		t.Fatal(err)
	}
	for _, f := range []func(*Config){
		func(c *Config) { c.Table.PairScore = c.RedScore },
		func(c *Config) { c.Table.TogetherScore = 0.7 },
		func(c *Config) { c.Table.SmoothMs = 0 },
		func(c *Config) { c.Table.TogetherWindowMs = 8000 }, // not more than twice the rhythm lag
	} {
		c := DefaultConfig()
		f(&c)
		if c.Validate() == nil {
			t.Errorf("accepted %+v", c.Table)
		}
	}
	c := DefaultConfig()
	c.Table.MaxPhones = 0 // profile off: its other settings don't matter
	c.Table.SmoothMs = 0
	if err := c.Validate(); err != nil {
		t.Error(err)
	}
}
