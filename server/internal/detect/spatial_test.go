package detect

import (
	"fmt"
	"math"
	"sort"
	"testing"

	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/sim"
)

// crowdN is the crowd size for the crowd-layout tests: two groups of ~8
// plus stragglers on the 24 × 16 m venue.
const crowdN = 24

// TestCrowdWave: the growing push wave through a crowd that wanders and
// has ±25 ms clock error. Free positions are harder than the line (people
// stand at every angle to the push, and move during the 6 s correlation
// window), so it is slower and not every random crowd gets there; most must
// reach red within a minute, all at least yellow, all travelling +x.
func TestCrowdWave(t *testing.T) {
	reds := 0
	var at []float64
	for seed := int64(1); seed <= 8; seed++ {
		o := runScenarioLayout(t, "wave", crowdN, 70, 25, false, seed, DefaultConfig(), sim.CrowdLayout(true))
		if o.redAt >= 0 && o.redAt <= 60 {
			reds++
			at = append(at, o.redAt)
		}
		if o.maxLevel == protocol.LevelCalm {
			t.Errorf("seed %d: crowd wave stayed calm", seed)
		}
		for d := range o.directions {
			if d != "+x" {
				t.Errorf("seed %d: direction %q, want +x", seed, d)
			}
		}
	}
	sort.Float64s(at)
	t.Logf("red within 60 s in %d/8 crowds, at %v s", reds, at)
	if reds < 5 {
		t.Errorf("only %d/8 crowds reached red within 60 s", reds)
	}
}

// TestCrowdFalsePositives: the look-alikes stay calm in a crowd too (a
// single shove or someone squeezing past may reach yellow, as on the line).
func TestCrowdFalsePositives(t *testing.T) {
	atMost := map[string]string{
		"calm": "calm", "walk": "calm", "dance": "calm", "handle": "calm", "gather": "calm",
		"sway": "calm", "sway-slow": "calm", "mexican": "calm", "march": "calm",
		"pocket": "calm", "bump": "calm", "jump-stagger": "calm",
		"shove": "yellow", "walkpast": "yellow", "procession": "yellow",
	}
	for seed := int64(1); seed <= 3; seed++ {
		for name, want := range atMost {
			dur := 90.0
			if name == "gather" {
				dur = 110
			}
			o := runScenarioLayout(t, name, crowdN, dur, 25, false, seed, DefaultConfig(), sim.CrowdLayout(true))
			if levelRank[o.maxLevel] > levelRank[want] {
				t.Errorf("%s seed %d reached %s, want at most %s", name, seed, o.maxLevel, want)
			}
		}
	}
}

// TestLineDefaultZones: the line demo on the default 2 × 1 zones (the whole
// line lands in zone A) still goes red quickly.
func TestLineDefaultZones(t *testing.T) {
	sc, _ := sim.New("wave", 8, 1, 8, 42)
	cfg := DefaultConfig()
	d := New(cfg)
	for i := 0; i < 8; i++ {
		x, y := sc.Pos(i)
		if lx, ly := cfg.LegacyPos(0, i); lx != x || ly != y {
			t.Fatalf("sim line phone %d at %.2f,%.2f, legacy mapping says %.2f,%.2f", i, x, y, lx, ly)
		}
		d.SetPhone(fmt.Sprint(i), x, y)
	}
	const t0 = 1_700_000_000_000
	evs := sc.Generate(t0, 40)
	j := 0
	for now := int64(t0); now <= t0+40_000; now += 250 {
		for j < len(evs) && evs[j].T <= now {
			e := evs[j]
			d.Add(fmt.Sprint(e.Phone), Sample{T: e.T, AX: e.AX, AY: e.AY, AZ: e.AZ, Rot: e.Rot})
			j++
		}
		r := d.Step(now)
		if z, _ := r.Zone("A"); z.Level == protocol.LevelRed {
			t.Logf("red at %.1f s", float64(now-t0)/1000)
			if now-t0 > 30_000 {
				t.Errorf("red at %.1f s, want within 30 s", float64(now-t0)/1000)
			}
			return
		}
	}
	t.Fatal("line wave on the default zones never reached red")
}

func TestNeighbourSelection(t *testing.T) {
	cfg := DefaultConfig()
	cfg.NeighbourRadius = 1.0
	cfg.MaxNeighbours = 2
	d := New(cfg)
	// A hub with four phones around it at 0.3, 0.4, 0.5, 0.6 m, one phone
	// out of range, and two phones far away together.
	pos := map[string][2]float64{
		"hub": {10, 10}, "n1": {10.3, 10}, "n2": {10, 10.4}, "n3": {9.5, 10}, "n4": {10, 9.4},
		"far": {11.5, 10}, "x1": {20, 5}, "x2": {20.8, 5},
	}
	var ps []*phone
	for id, p := range pos {
		d.SetPhone(id, p[0], p[1])
	}
	ids := make([]string, 0, len(pos))
	for id := range pos {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		ps = append(ps, d.phones[id])
	}
	got := map[string]bool{}
	for _, pr := range d.neighbourPairs(ps) {
		a, b := pr[0], pr[1]
		if !before(a, b) {
			t.Errorf("pair %s-%s not ordered by x, y, id", a.id, b.id)
		}
		got[a.id+"-"+b.id] = true
	}
	// hub keeps n1, n2. n3 and n4 each keep hub among their two nearest, so
	// those pairs count too (either side keeps the other). far is > 1 m from
	// everyone except n1 (1.2 m: out of range).
	for _, want := range []string{"hub-n1", "hub-n2", "n3-hub", "n4-hub", "x1-x2"} {
		if !got[want] {
			t.Errorf("missing pair %s (got %v)", want, got)
		}
	}
	for p := range got {
		if p == "n1-far" || p == "hub-far" {
			t.Errorf("out-of-range pair %s", p)
		}
	}
	// MaxNeighbours trims: with 1, n3–n4 (0.78 m apart, each other's second
	// choice after hub) must not be paired.
	d.cfg.MaxNeighbours = 1
	for _, pr := range d.neighbourPairs(ps) {
		if (pr[0].id == "n3" || pr[0].id == "n4") && (pr[1].id == "n3" || pr[1].id == "n4") {
			t.Errorf("n3-n4 paired with maxNeighbours 1")
		}
	}
}

// chainCase places phones and wave edges by hand and checks which survive.
func chainEdges(t *testing.T, pos map[string][2]float64, edges []Edge) []Edge {
	t.Helper()
	d := New(DefaultConfig())
	for id, p := range pos {
		d.SetPhone(id, p[0], p[1])
	}
	out := append([]Edge(nil), edges...)
	d.keepChains(out, make([]bool, len(out)))
	return out
}

func TestChainOrientation(t *testing.T) {
	wave := func(from, to string, lag int64) Edge {
		return Edge{From: from, To: to, LagMs: lag, Corr: 0.9, Wave: true}
	}

	// a → b → c along x: kept. LagMs < 0 on the second edge, stored the
	// other way round, still travels the same way.
	pos := map[string][2]float64{"a": {0, 0}, "b": {0.6, 0}, "c": {1.2, 0.1}}
	got := chainEdges(t, pos, []Edge{wave("a", "b", 250), {From: "c", To: "b", LagMs: -250, Corr: 0.9, Wave: true}})
	if !got[0].Wave || !got[1].Wave {
		t.Errorf("straight run dropped: %+v", got)
	}

	// The same three phones, but the second hop travels back: not a chain.
	got = chainEdges(t, pos, []Edge{wave("a", "b", 250), wave("c", "b", 250)})
	if got[0].Wave || got[1].Wave {
		t.Errorf("opposite hops kept: %+v", got)
	}

	// A bend: a → b along x, then b → c straight down (90°) is still within
	// the default 90°; at 120° it is not.
	bend := map[string][2]float64{"a": {0, 0}, "b": {0.6, 0}, "c": {0.6, 0.6}, "d": {0.1, 0.3}}
	got = chainEdges(t, bend, []Edge{wave("a", "b", 250), wave("b", "c", 250)})
	if !got[0].Wave {
		t.Errorf("90° bend dropped")
	}
	got = chainEdges(t, bend, []Edge{wave("a", "b", 250), wave("c", "d", 250)})
	if got[0].Wave || got[1].Wave {
		t.Errorf("disconnected hops kept: %+v", got)
	}
	got = chainEdges(t, bend, []Edge{wave("b", "c", 250), {From: "d", To: "c", LagMs: -250, Wave: true}})
	// b→c is down, c→d is down-left at ~121°: dropped.
	if got[0].Wave {
		t.Errorf("sharp bend kept: %+v", got)
	}

	// A cycle (every hop turns 60°) terminates and counts as a run.
	hex := map[string][2]float64{}
	var cyc []Edge
	for i := 0; i < 6; i++ {
		hex[fmt.Sprint(i)] = [2]float64{10 + 0.6*cosDeg(60*float64(i)), 10 + 0.6*sinDeg(60*float64(i))}
	}
	for i := 0; i < 6; i++ {
		cyc = append(cyc, wave(fmt.Sprint(i), fmt.Sprint((i+1)%6), 250))
	}
	got = chainEdges(t, hex, cyc)
	for _, e := range got {
		if !e.Wave {
			t.Errorf("cycle edge dropped: %+v", e)
		}
	}
}

func TestZonesAndSensitivity(t *testing.T) {
	cfg := DefaultConfig()
	d := New(cfg)
	stage := ZoneDef{ID: "stage", Name: "Stage front", Custom: true, Sens: SensHigh, Poly: Rect(2, 2, 10, 8)}
	bar := ZoneDef{ID: "bar", Name: "Bar", Custom: true, Poly: [][2]float64{{8, 6}, {14, 6}, {11, 12}}}
	rest := ZoneDef{ID: RestZone, Name: "Rest of venue", Poly: Rect(0, 0, cfg.VenueW, cfg.VenueH), Rest: true}
	d.SetZones([]ZoneDef{stage, bar, rest})
	cases := []struct {
		x, y float64
		want string
	}{
		{5, 5, "stage"}, {9, 7, "stage,bar"}, {11, 10, "bar"}, {20, 14, "rest"}, {1, 1, "rest"},
	}
	for _, c := range cases {
		got := fmt.Sprint(d.ZonesOf(c.x, c.y))
		if got != "["+replaceComma(c.want)+"]" {
			t.Errorf("ZonesOf(%v, %v) = %s, want %s", c.x, c.y, got, c.want)
		}
	}
	if d.ZoneOf(9, 7) != "stage" {
		t.Errorf("ZoneOf picks the first zone")
	}
	th := zoneThresholds(&cfg, SensHigh)
	if th.Yellow != cfg.YellowScore*cfg.HighRiskFactor || th.Red != cfg.RedScore*cfg.HighRiskFactor || th.Hold != cfg.HoldMs/2 {
		t.Errorf("high-risk thresholds %+v", th)
	}

	// The same single shove: yellow at most in a normal area, red in a
	// high-risk one.
	for _, sens := range []string{SensNormal, SensHigh} {
		sc, _ := sim.New("shove", 8, 1, 8, 42)
		d := New(cfg)
		d.SetZones([]ZoneDef{{ID: "line", Custom: true, Sens: sens, Poly: Rect(3, 6, 10, 10)}, rest})
		for i := 0; i < 8; i++ {
			x, y := sc.Pos(i)
			d.SetPhone(fmt.Sprint(i), x, y)
		}
		const t0 = 1_700_000_000_000
		evs := sc.Generate(t0, 40)
		worst, j := protocol.LevelCalm, 0
		for now := int64(t0); now <= t0+40_000; now += 250 {
			for j < len(evs) && evs[j].T <= now {
				e := evs[j]
				d.Add(fmt.Sprint(e.Phone), Sample{T: e.T, AX: e.AX, AY: e.AY, AZ: e.AZ, Rot: e.Rot})
				j++
			}
			r := d.Step(now)
			z, _ := r.Zone("line")
			if levelRank[z.Level] > levelRank[worst] {
				worst = z.Level
			}
			if rz, _ := r.Zone(RestZone); rz.Level != protocol.LevelCalm {
				t.Errorf("rest zone went %s with nobody in it", rz.Level)
			}
		}
		want := map[string]string{SensNormal: protocol.LevelYellow, SensHigh: protocol.LevelRed}[sens]
		if worst != want {
			t.Errorf("shove in a %s area reached %s, want %s", sens, worst, want)
		}
	}

	// Redrawing keeps the level of a zone that keeps its ID.
	d.zones[0].state.Level = protocol.LevelRed
	d.SetZones([]ZoneDef{{ID: "stage", Poly: Rect(0, 0, 5, 5)}, rest})
	if d.zones[0].state.Level != protocol.LevelRed {
		t.Errorf("stage lost its level on redraw")
	}
}

func replaceComma(s string) string {
	out := []byte(s)
	for i := range out {
		if out[i] == ',' {
			out[i] = ' '
		}
	}
	return string(out)
}

func cosDeg(d float64) float64 { return math.Cos(d * math.Pi / 180) }
func sinDeg(d float64) float64 { return math.Sin(d * math.Pi / 180) }
