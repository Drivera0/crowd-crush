package app

import (
	"fmt"
	"testing"

	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/sim"
)

// tableRun lines n phones up at the demo spot and plays a table-demo case
// (sim/table.go) through the live pipeline, phones sending gravity.
type tableRun struct {
	maxLevel   string
	yellowAt   float64 // s after the case starts, -1 = never
	redAt      float64
	pairWaves  bool // a snapshot wave was marked pair
	together   bool // a snapshot carried a "moving as one" group
	tableSnap  bool // a snapshot said the profile is on
	maxPress   string
	alertKinds map[string]string // kind → worst level
	causes     map[string]string // wave alert cause ("" = a crowd push) → worst level
}

func runTableApp(t *testing.T, a *App, name string, n int, seed int64, dur float64) tableRun {
	t.Helper()
	sc, err := sim.NewTable(name, n, seed, dur)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("p%d", i)
		a.PhoneHelloAuto(id, "test")
		a.PhoneSync(id, 0, 20)
	}
	out := tableRun{maxLevel: protocol.LevelCalm, yellowAt: -1, redAt: -1, maxPress: protocol.LevelCalm, alertKinds: map[string]string{}, causes: map[string]string{}}
	start := demoT0 + int64(sim.TableLeadIn*1000)
	evs := sc.Generate(demoT0, dur)
	j := 0
	for now := demoT0; now <= demoT0+int64(dur*1000); now += 250 {
		for j < len(evs) && evs[j].T <= now {
			e := evs[j]
			a.PhoneMotion(fmt.Sprintf("p%d", e.Phone), protocol.Motion{T: e.T, AX: e.AX, AY: e.AY, AZ: e.AZ, Rot: e.Rot,
				G: []float64{e.G[0], e.G[1], e.G[2]}}, e.T)
			j++
		}
		a.detectTick(now)
		a.mu.Lock()
		s := a.snapshotLocked(now)
		a.mu.Unlock()
		out.tableSnap = out.tableSnap || s.Table
		out.together = out.together || len(s.Together) > 0
		for _, w := range s.Waves {
			out.pairWaves = out.pairWaves || w.Pair
		}
		for _, nd := range s.Nodes {
			if nd.Press != "" && levelRank[nd.Press] > levelRank[out.maxPress] {
				out.maxPress = nd.Press
			}
		}
		for _, c := range s.Clusters {
			if levelRank[c.Level] > levelRank[out.maxPress] {
				out.maxPress = c.Level
			}
		}
		for _, z := range s.Zones {
			if levelRank[z.Level] > levelRank[out.maxLevel] {
				out.maxLevel = z.Level
			}
			since := float64(now-start) / 1000
			if z.Level != protocol.LevelCalm && out.yellowAt < 0 {
				out.yellowAt = since
			}
			if z.Level == protocol.LevelRed && out.redAt < 0 {
				out.redAt = since
			}
		}
	}
	a.mu.Lock()
	for _, al := range a.alerts {
		if levelRank[al.Level] > levelRank[out.alertKinds[al.Kind]] {
			out.alertKinds[al.Kind] = al.Level
		}
		if al.Kind == protocol.KindWave && al.Level != protocol.LevelCalm && levelRank[al.Level] > levelRank[out.causes[al.Cause]] {
			out.causes[al.Cause] = al.Level
		}
	}
	a.mu.Unlock()
	t.Logf("%s n=%d: max %s yellow %.1f s red %.1f s pair %v together %v packed %s alerts %v", name, n, out.maxLevel, out.yellowAt, out.redAt, out.pairWaves, out.together, out.maxPress, out.alertKinds)
	return out
}

func tableApp(t *testing.T, cfg detect.Config, demoOn bool, x, y float64) *App {
	t.Helper()
	a := New(Options{Detect: cfg, RecordingsDir: t.TempDir(), DataDir: t.TempDir()})
	if _, err := a.SetDemo(protocol.DemoSpot{On: demoOn, X: x, Y: y}, false); err != nil {
		t.Fatal(err)
	}
	return a
}

// TestTableDemoProfile: the profile is on exactly while the demo spot is
// on, and does what docs/TABLE-DEMO.md says at the table.
func TestTableDemoProfile(t *testing.T) {
	cfg := detect.DefaultConfig()

	// Three judges pushing: red within ~10 s with the demo spot on.
	r := runTableApp(t, tableApp(t, cfg, true, 12, 8), "push", 3, 1, 25)
	if !r.tableSnap || r.maxLevel != protocol.LevelRed || r.redAt < 0 || r.redAt > 10 {
		t.Errorf("3 pushing, demo on: %+v", r)
	}
	if r.alertKinds[protocol.KindWave] != protocol.LevelRed || r.causes[""] != protocol.LevelRed || r.causes["pair"] != "" || r.causes["together"] != "" {
		t.Errorf("3 pushing: alerts %v causes %v, want a red wave alert with no table cause", r.alertKinds, r.causes)
	}

	// Two judges pushing: yellow, never red, the wave marked pair.
	r = runTableApp(t, tableApp(t, cfg, true, 12, 8), "push", 2, 1, 25)
	if r.maxLevel != protocol.LevelYellow || !r.pairWaves || r.alertKinds[protocol.KindWave] != protocol.LevelYellow {
		t.Errorf("2 pushing, demo on: %+v", r)
	}
	if r.causes["pair"] != protocol.LevelYellow || r.causes[""] != "" {
		t.Errorf("2 pushing: wave alert causes %v, want pair (yellow) only", r.causes)
	}

	// Jumping together: calm.
	r = runTableApp(t, tableApp(t, cfg, true, 12, 8), "jump", 4, 1, 30)
	if r.maxLevel != protocol.LevelCalm || r.together {
		t.Errorf("4 jumping, demo on: %+v", r)
	}

	// Pressed together, swaying as one: yellow, with a group.
	r = runTableApp(t, tableApp(t, cfg, true, 12, 8), "together", 3, 1, 35)
	if r.maxLevel != protocol.LevelYellow || !r.together {
		t.Errorf("3 moving as one, demo on: %+v", r)
	}
	if r.causes["together"] != protocol.LevelYellow || r.causes[""] != "" {
		t.Errorf("3 moving as one: wave alert causes %v, want together (yellow) only", r.causes)
	}

	// Demo spot off (phones placed by hand at the same spot): no profile.
	a := tableApp(t, cfg, false, 12, 8)
	for i := 0; i < 2; i++ {
		a.PhoneHello(fmt.Sprintf("p%d", i), 12+0.6*float64(i), 8, "test")
	}
	r = runTableApp(t, a, "push", 2, 1, 25)
	if r.tableSnap || r.maxLevel != protocol.LevelCalm || r.pairWaves {
		t.Errorf("2 pushing, demo off: %+v", r)
	}
}

// TestTableDemoDensity: four or five judges lined up in a corner of the
// venue don't read as packed while the demo spot is on, even when the venue
// is configured for low participation; with it off the configured
// participation applies again.
func TestTableDemoDensity(t *testing.T) {
	cfg := detect.DefaultConfig()
	cfg.Participation = 0.3
	a := tableApp(t, cfg, true, 0.2, 0.2)
	if r := runTableApp(t, a, "still", 5, 1, 20); r.maxPress != protocol.LevelCalm {
		t.Errorf("5 judges in a corner, demo on: packed %s", r.maxPress)
	}
	a.mu.Lock()
	part := a.live.cfg().Participation
	a.mu.Unlock()
	if part != 1 {
		t.Errorf("participation %v with the demo spot on, want 1", part)
	}
	if _, err := a.SetDemo(protocol.DemoSpot{On: false, X: 0.2, Y: 0.2}, false); err != nil {
		t.Fatal(err)
	}
	a.detectTick(demoT0 + 30_000)
	a.mu.Lock()
	part, on := a.live.cfg().Participation, a.live.det.Table()
	a.mu.Unlock()
	if part != 0.3 || on {
		t.Errorf("demo off: participation %v, profile %v; want 0.3, off", part, on)
	}

	// Control: the same row placed by hand with the demo spot off reads
	// as packed at 0.3 participation (what the profile is there to avoid).
	b := tableApp(t, cfg, false, 0.2, 0.2)
	for i := 0; i < 5; i++ {
		b.PhoneHello(fmt.Sprintf("p%d", i), 0.2+0.6*float64(i), 0.2, "test")
	}
	if r := runTableApp(t, b, "still", 5, 1, 20); r.maxPress == protocol.LevelCalm {
		t.Errorf("control: 5 phones in a corner at 0.3 participation read calm; the test proves nothing")
	}
}
