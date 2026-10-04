package crowd

import (
	"fmt"
	"math"
	"testing"

	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/sim"
)

func TestDBSCAN(t *testing.T) {
	pts := []Point{
		{"a", 0, 0}, {"b", 0.5, 0}, {"c", 1.0, 0}, {"d", 1.5, 0}, // a chain: one group
		{"e", 10, 10}, {"f", 10.4, 10}, // only two: noise at minPts 3
		{"g", 20, 5}, // alone
	}
	g := DBSCAN(pts, 0.6, 3)
	if len(g) != 1 || len(g[0]) != 4 {
		t.Fatalf("groups %v, want one of 4", g)
	}
	if g := DBSCAN(pts, 0.6, 2); len(g) != 2 {
		t.Fatalf("minPts 2: groups %v, want 2", g)
	}
}

func TestDescribe(t *testing.T) {
	// Four phones on a 1 m square: centroid in the middle, radius √0.5+0.5.
	pts := []Point{{"a", 0, 0}, {"b", 1, 0}, {"c", 0, 1}, {"d", 1, 1}}
	c := describe(pts, []int{0, 1, 2, 3}, 0.5)
	r := math.Sqrt(0.5) + 0.5
	if math.Abs(c.X-0.5) > 1e-9 || math.Abs(c.R-r) > 1e-9 || c.Count != 4 || c.People != 8 {
		t.Fatalf("%+v", c)
	}
	if want := 4 / (math.Pi * r * r); math.Abs(c.Density-want) > 1e-9 || math.Abs(c.Est-2*want) > 1e-9 {
		t.Fatalf("density %.3f est %.3f", c.Density, c.Est)
	}
	// Tiny clusters use at least 1 m².
	c = describe([]Point{{"a", 0, 0}, {"b", 0, 0}, {"c", 0, 0}}, []int{0, 1, 2}, 1)
	if c.Density != 3 {
		t.Fatalf("density %.2f, want 3 (area floor 1 m²)", c.Density)
	}
}

func TestTrackingKeepsIDs(t *testing.T) {
	tr := NewTracker(ConfigFrom(detect.DefaultConfig()))
	group := func(cx float64) []Point {
		return []Point{{"a", cx, 5}, {"b", cx + 0.5, 5}, {"c", cx, 5.5}}
	}
	cs, _ := tr.Update(0, append(group(3), group(15)...))
	if len(cs) != 2 {
		t.Fatalf("%d clusters", len(cs))
	}
	ids := map[string]bool{cs[0].ID: true, cs[1].ID: true}
	// Both drift 1 m: same IDs.
	cs, _ = tr.Update(250, append(group(4), group(16)...))
	for _, c := range cs {
		if !ids[c.ID] {
			t.Fatalf("cluster at %.1f got new ID %s", c.X, c.ID)
		}
	}
	// One jumps 6 m: new ID.
	cs, _ = tr.Update(500, append(group(4), group(22)...))
	fresh := 0
	for _, c := range cs {
		if !ids[c.ID] {
			fresh++
		}
	}
	if fresh != 1 {
		t.Fatalf("want one new cluster ID, got %d (%v)", fresh, cs)
	}
}

// gatherRun plays the gather scenario's positions through a tracker.
type gatherRun struct {
	formingSeen, dispersingSeen bool
	maxLevel                    string
	redAt                       float64
	maxEst                      float64
	endLevels                   []string
	changes                     []Change
}

var levelRank = map[string]int{protocol.LevelCalm: 0, protocol.LevelYellow: 1, protocol.LevelRed: 2}

func runGather(t *testing.T, n int, seed int64, cfg detect.Config, verbose bool) gatherRun {
	t.Helper()
	sc, err := sim.NewLayout("gather", n, seed, sim.CrowdLayout(true))
	if err != nil {
		t.Fatal(err)
	}
	tr := NewTracker(ConfigFrom(cfg))
	out := gatherRun{maxLevel: protocol.LevelCalm, redAt: -1}
	for ms := int64(0); ms <= 120_000; ms += 250 {
		sec := float64(ms) / 1000
		pts := make([]Point, n)
		for i := range pts {
			x, y := sc.PosAt(i, sec)
			pts[i] = Point{fmt.Sprint(i), x, y}
		}
		cs, ch := tr.Update(ms, pts)
		out.changes = append(out.changes, ch...)
		out.endLevels = nil
		for _, c := range cs {
			if verbose && ms%5000 == 0 {
				t.Logf("t=%3.0f %s n=%d r=%.2f est=%.2f %s %s", sec, c.ID, c.Count, c.R, c.Est, c.Trend, c.Level)
			}
			if c.Count >= 10 && sec > 10 && sec < 45 && c.Trend == Forming {
				out.formingSeen = true
			}
			if c.Count >= 5 && sec > GatherLeaveSec && c.Trend == Dispersing {
				out.dispersingSeen = true
			}
			if levelRank[c.Level] > levelRank[out.maxLevel] {
				out.maxLevel = c.Level
			}
			if c.Level == protocol.LevelRed && out.redAt < 0 {
				out.redAt = sec
			}
			out.maxEst = math.Max(out.maxEst, c.Est)
			out.endLevels = append(out.endLevels, c.Level)
		}
	}
	return out
}

const GatherLeaveSec = sim.GatherLeave

// TestGatherFormsThenDisperses: the gather scenario packs people in front
// of the stage past the danger density (red), the cluster is seen forming
// on the way in and dispersing on the way out, and nothing is left above
// calm at the end.
func TestGatherFormsThenDisperses(t *testing.T) {
	for _, seed := range []int64{1, 2, 3} {
		o := runGather(t, 40, seed, detect.DefaultConfig(), testing.Verbose() && seed == 1)
		t.Logf("seed %d: max %s red at %.1f s, peak %.1f people/m², forming=%v dispersing=%v", seed, o.maxLevel, o.redAt, o.maxEst, o.formingSeen, o.dispersingSeen)
		if o.maxLevel != protocol.LevelRed {
			t.Errorf("seed %d: max level %s, want red (peak %.1f/m²)", seed, o.maxLevel, o.maxEst)
		}
		if !o.formingSeen || !o.dispersingSeen {
			t.Errorf("seed %d: forming seen %v, dispersing seen %v", seed, o.formingSeen, o.dispersingSeen)
		}
		for _, l := range o.endLevels {
			if l != protocol.LevelCalm {
				t.Errorf("seed %d: a cluster is still %s after everyone left", seed, l)
			}
		}
		// The way up passes through yellow; the red change is what alerts.
		sawRed := false
		for _, c := range o.changes {
			if c.From == protocol.LevelYellow && c.To == protocol.LevelRed {
				sawRed = true
			}
		}
		if !sawRed {
			t.Errorf("seed %d: no yellow → red change", seed)
		}
	}
}

// TestParticipation: with half the crowd running the page, the same phones
// mean twice the people.
func TestParticipation(t *testing.T) {
	cfg := detect.DefaultConfig()
	full := runGather(t, 20, 1, cfg, false)
	cfg.Participation = 0.5
	half := runGather(t, 20, 1, cfg, false)
	if math.Abs(half.maxEst-2*full.maxEst) > 1e-6 {
		t.Fatalf("peak %.2f at 50%% participation vs %.2f at 100%%", half.maxEst, full.maxEst)
	}
}
