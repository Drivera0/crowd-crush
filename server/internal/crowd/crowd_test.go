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
		{ID: "a", X: 0, Y: 0}, {ID: "b", X: 0.5, Y: 0}, {ID: "c", X: 1.0, Y: 0}, {ID: "d", X: 1.5, Y: 0}, // a chain: one group
		{ID: "e", X: 10, Y: 10}, {ID: "f", X: 10.4, Y: 10}, // only two: noise at minPts 3
		{ID: "g", X: 20, Y: 5}, // alone
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
	pts := []Point{{ID: "a", X: 0, Y: 0}, {ID: "b", X: 1, Y: 0}, {ID: "c", X: 0, Y: 1}, {ID: "d", X: 1, Y: 1}}
	c := describe(pts, []int{0, 1, 2, 3}, 0.5, AccDisc)
	r := math.Sqrt(0.5) + 0.5
	if math.Abs(c.X-0.5) > 1e-9 || math.Abs(c.R-r) > 1e-9 || c.Count != 4 || c.People != 8 {
		t.Fatalf("%+v", c)
	}
	if want := 4 / (math.Pi * r * r); math.Abs(c.Density-want) > 1e-9 || math.Abs(c.Est-2*want) > 1e-9 {
		t.Fatalf("density %.3f est %.3f", c.Density, c.Est)
	}
	// Tiny clusters use at least 1 m².
	c = describe([]Point{{ID: "a", X: 0, Y: 0}, {ID: "b", X: 0, Y: 0}, {ID: "c", X: 0, Y: 0}}, []int{0, 1, 2}, 1, AccDisc)
	if c.Density != 3 {
		t.Fatalf("density %.2f, want 3 (area floor 1 m²)", c.Density)
	}
}

func TestTrackingKeepsIDs(t *testing.T) {
	tr := NewTracker(ConfigFrom(detect.DefaultConfig()))
	group := func(cx float64) []Point {
		return []Point{{ID: "a", X: cx, Y: 5}, {ID: "b", X: cx + 0.5, Y: 5}, {ID: "c", X: cx, Y: 5.5}}
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
			pts[i] = Point{ID: fmt.Sprint(i), X: x, Y: y}
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

// ring is n phones evenly on a disc of radius r around (10, 10) (plus the centre).
func ring(n int, r float64) []Point {
	pts := []Point{{ID: "c", X: 10, Y: 10}}
	for i := 0; i < n; i++ {
		a := 2 * math.Pi * float64(i) / float64(n)
		pts = append(pts, Point{ID: fmt.Sprint(i), X: 10 + r*math.Cos(a), Y: 10 + r*math.Sin(a)})
	}
	return pts
}

// TestEarlyWarning: a group packing in steadily is yellow (early) before
// its density alone would make it yellow, and reports rate and ETA; the
// same group standing still never warns early.
func TestEarlyWarning(t *testing.T) {
	cfg := ConfigFrom(detect.DefaultConfig())
	tr := NewTracker(cfg)
	var early *Change
	var normalYellow int64 = -1
	plain := NewTracker(func() Config { c := cfg; c.EarlyWarnS = 0; return c }())
	for ms := int64(0); ms <= 40_000; ms += 250 {
		// 24 phones, radius shrinking from 3.5 m to 1 m over 15 s, then holding.
		r := math.Max(1, 3.5-2.5*float64(ms)/15_000)
		pts := ring(24, r)
		cs, ch := tr.Update(ms, pts)
		_, pch := plain.Update(ms, pts)
		for i := range ch {
			if ch[i].Early && early == nil {
				early = &ch[i]
			}
		}
		for _, c := range pch {
			if c.To == "yellow" && normalYellow < 0 {
				normalYellow = c.T
			}
		}
		if len(cs) != 1 {
			t.Fatalf("%d clusters", len(cs))
		}
		if ms == 12_000 && cs[0].Rate <= 0 {
			t.Errorf("rate %.2f while packing in", cs[0].Rate)
		}
	}
	if early == nil {
		t.Fatal("no early warning while packing in")
	}
	c := early.Cluster
	t.Logf("early yellow at %.2f s (est %.2f, rate %.2f/min, eta %.1f s); plain yellow at %.2f s",
		float64(early.T)/1000, c.Est, c.Rate, c.ETA, float64(normalYellow)/1000)
	if early.From != "calm" || early.To != "yellow" || c.ETA <= 0 || c.ETA > cfg.EarlyWarnS || c.Rate <= 0 || c.Est < cfg.EarlyFloor*cfg.Danger {
		t.Errorf("early change %+v", early)
	}
	if normalYellow < 0 || early.T >= normalYellow {
		t.Errorf("early warning at %d ms, not before plain yellow at %d ms", early.T, normalYellow)
	}

	// Standing still at the same density: no rate, no early warning.
	still := NewTracker(cfg)
	for ms := int64(0); ms <= 30_000; ms += 250 {
		cs, ch := still.Update(ms, ring(20, 2))
		for _, x := range ch {
			if x.Early {
				t.Fatalf("early warning for a group standing still: %+v", x)
			}
		}
		if ms > 6000 && (cs[0].Rate != 0 || cs[0].ETA != 0) {
			t.Fatalf("still group: rate %.2f eta %.2f", cs[0].Rate, cs[0].ETA)
		}
	}
}

// TestSlope: least squares over the window, nothing until it spans 5 s.
func TestSlope(t *testing.T) {
	var h []sample
	for ms := int64(0); ms <= 10_000; ms += 250 {
		h = append(h, sample{t: ms, est: 1 + 0.0002*float64(ms)}) // 0.2 per second
	}
	if s := slope(h, 10_000-RateWindowMs) * 1000; math.Abs(s-0.2) > 1e-9 {
		t.Errorf("slope %.4f/s, want 0.2", s)
	}
	if s := slope(h[:12], 0); s != 0 {
		t.Errorf("slope over 2.75 s: %.4f, want 0", s)
	}
}

// packedInThin is a thin crowd (1.5 people/m² at 60 % participation) over
// a 20 × 14 m floor with a packed 4 × 4 m patch (6 people/m²) in the middle,
// on grids. The patch is about 10 % of the phones.
func packedInThin() []Point {
	const part = 0.6
	var pts []Point
	in := func(x, y float64) bool { return x >= 8 && x < 12 && y >= 5 && y < 9 }
	thin := 1 / math.Sqrt(1.5*part)
	for x := thin / 2; x < 20; x += thin {
		for y := thin / 2; y < 14; y += thin {
			if !in(x, y) {
				pts = append(pts, Point{ID: fmt.Sprintf("t%.2f,%.2f", x, y), X: x, Y: y})
			}
		}
	}
	packed := 1 / math.Sqrt(6*part)
	for x := 8 + packed/2; x < 12; x += packed {
		for y := 5 + packed/2; y < 9; y += packed {
			pts = append(pts, Point{ID: fmt.Sprintf("p%.2f,%.2f", x, y), X: x, Y: y})
		}
	}
	return pts
}

// quantilePeak is the old estimate: the 90th percentile of the local
// densities, for comparison.
func quantilePeak(pts []Point) float64 {
	var ks []int
	for _, p := range pts {
		k := 0
		for _, q := range pts {
			if math.Hypot(p.X-q.X, p.Y-q.Y) <= LocalR {
				k++
			}
		}
		ks = append(ks, k)
	}
	sortInts(ks)
	return float64(ks[min(len(ks)-1, int(PeakQuantile*float64(len(ks))))]) / (math.Pi * LocalR * LocalR)
}

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// TestPackedPatchReadsPacked: a packed patch inside a big thin crowd reads
// as the patch's real density (people/m² at its densest spot), where the
// old 90th-percentile estimate read the thin crowd.
func TestPackedPatchReadsPacked(t *testing.T) {
	pts := packedInThin()
	idx := make([]int, len(pts))
	for i := range idx {
		idx[i] = i
	}
	c := describe(pts, idx, 0.6, AccDisc)
	old := quantilePeak(pts) / 0.6
	t.Logf("%d phones: est %.2f people/m² at (%.1f, %.1f) (old estimate %.2f, disc %.2f); truth 6 in the patch, 1.5 around it",
		len(pts), c.Est, c.PeakX, c.PeakY, old, c.Density/0.6)
	if c.Est < 5 || c.Est > 7 {
		t.Errorf("est %.2f people/m², want about 6", c.Est)
	}
	if c.PeakX < 8 || c.PeakX > 12 || c.PeakY < 5 || c.PeakY > 9 {
		t.Errorf("densest spot (%.1f, %.1f) not in the patch", c.PeakX, c.PeakY)
	}
	// A single phone standing in a lucky spot doesn't make a thin crowd packed.
	lucky := append([]Point{}, pts[:40]...)
	lucky = append(lucky, Point{ID: "x", X: lucky[0].X + 0.1, Y: lucky[0].Y}, Point{ID: "y", X: lucky[0].X, Y: lucky[0].Y + 0.1})
	if p, _, _ := LocalPeak(lucky); p/0.6 > 2.5 {
		t.Errorf("thin crowd with one tight trio reads %.2f people/m²", p/0.6)
	}
	// Neighbours outside the centres count: the edge of an area sees the crowd past it.
	inner := []Point{{ID: "a", X: 0, Y: 0}}
	all := append([]Point{}, inner...)
	for i := 0; i < 9; i++ {
		all = append(all, Point{ID: fmt.Sprint(i), X: 0.5 + 0.1*float64(i), Y: 0})
	}
	if p, _, _ := LocalPeakAmong(all, inner); math.Abs(p-10/(math.Pi*LocalR*LocalR)) > 1e-9 {
		t.Errorf("peak %.3f, want the 10 phones around the centre", p)
	}
}
