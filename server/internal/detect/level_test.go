package detect

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/sim"
)

// How the phones of a test crowd are carried.
type carry int

const (
	carryUpright   carry = iota // flat against the chest, no g sent: the detector's old assumption
	carryRandom                 // any orientation (pocket, bag, hand), g sent with ~3° error
	carryRandomNoG              // any orientation, no g: what the detector made of it before levelling
	carryMixed                  // even phones upright without g, odd phones any orientation with g
	carryUprightG               // upright, but sending g like a new phone page
)

func (c carry) String() string {
	return [...]string{"upright", "random+g", "random, no g", "mixed", "upright+g"}[c]
}

var gErrDeg = 3.0 // degrees: error of the gravity estimate a phone sends

// carrier turns body-frame readings into what each phone would send.
type carrier struct {
	mode  carry
	rot   []mat3       // body → device, per phone
	gBias [][3]float64 // each phone's gravity estimate: off by a fixed gErrDeg …
	rng   *rand.Rand   // … plus a little jitter per reading
	// pocket: a walking scenario seen from a trouser pocket (legSwing)
	pocket bool
	gyro   bool
	legs   []leg
}

func newCarrier(mode carry, n int, seed int64) *carrier {
	rng := rand.New(rand.NewSource(seed*7919 + 13))
	c := &carrier{mode: mode, rng: rng}
	for i := 0; i < n; i++ {
		r := identity
		if c.oriented(i) {
			r = randomRotation(rng)
		}
		c.rot = append(c.rot, r)
		c.gBias = append(c.gBias, tiltBy(rng, r.mul([3]float64{0, -1, 0}), gErrDeg))
		c.legs = append(c.legs, leg{f: 1.6 + 0.5*rng.Float64(), ph: rng.Float64() * 2 * math.Pi, gain: 0.8 + 0.4*rng.Float64()})
	}
	return c
}

func (c *carrier) oriented(i int) bool {
	switch c.mode {
	case carryRandom, carryRandomNoG:
		return true
	case carryMixed:
		return i%2 == 1
	}
	return false
}

func (c *carrier) sendsG(i int) bool {
	switch c.mode {
	case carryRandom, carryUprightG:
		return true
	case carryMixed:
		return i%2 == 1
	}
	return false
}

func r2(v float64) float64 { return math.Round(v*100) / 100 }

// via is the hook for runScenarioVia.
func (c *carrier) via(e sim.Event, s Sample) Sample {
	i := e.Phone
	a := [3]float64{s.AX, s.AY, s.AZ}
	rot := c.rot[i]
	g := c.gBias[i]
	if c.pocket {
		var extra [3]float64
		var pitch, rate float64
		extra, pitch, rate = c.legs[i].at(float64(e.T%1_000_000) / 1000)
		a = [3]float64{a[0] + extra[0], a[1] + extra[1], a[2] + extra[2]}
		s.Rot += rate
		// The phone rides on the thigh: it pitches about the body's
		// left–right axis with every stride.
		swing := axisAngle([3]float64{1, 0, 0}, pitch)
		still := rot.mul([3]float64{0, -1, 0})
		rot = rot.times(swing)
		now := rot.mul([3]float64{0, -1, 0})
		if c.gyro {
			// Sensor fusion follows gravity as the leg swings.
			g = tiltBy(c.rng, now, gErrDeg)
		} else {
			// No gyro: the phone page only has a slow average of gravity, and
			// what gravity does meanwhile is left in the "acceleration".
			a = rot.mul(a)
			a = [3]float64{a[0] + 9.81*(still[0]-now[0]), a[1] + 9.81*(still[1]-now[1]), a[2] + 9.81*(still[2]-now[2])}
			s.AX, s.AY, s.AZ = a[0], a[1], a[2]
			if c.sendsG(i) {
				s.G = [3]float64{r2(g[0]), r2(g[1]), r2(g[2])}
			}
			return s
		}
	}
	a = rot.mul(a)
	s.AX, s.AY, s.AZ = a[0], a[1], a[2]
	if c.sendsG(i) {
		g = tiltBy(c.rng, g, 1)
		s.G = [3]float64{r2(g[0]), r2(g[1]), r2(g[2])} // 2 decimals on the wire
	}
	return s
}

// leg is one person's walk as a trouser pocket feels it: the bounce of each
// step, the thigh swinging forward and back once per stride (two steps) and
// the hips rolling sideways with it.
type leg struct{ f, ph, gain float64 }

// at returns the extra acceleration in the body frame (m/s²), the thigh's
// pitch (rad) and how fast it is turning (deg/s) at time t.
func (l leg) at(t float64) (a [3]float64, pitch, rate float64) {
	w := 2 * math.Pi * l.f * t
	const swingDeg = 20
	a = [3]float64{
		0.6 * l.gain * math.Sin(w/2+l.ph+1),
		2.5*l.gain*math.Sin(w+l.ph) + 1.0*l.gain*math.Sin(w/2+l.ph+2),
		2.5 * l.gain * math.Sin(w/2+l.ph),
	}
	pitch = swingDeg * math.Pi / 180 * math.Sin(w/2+l.ph)
	rate = math.Abs(swingDeg * math.Pi * l.f * math.Cos(w/2+l.ph))
	return
}

// orientCase is one body motion and what the detector must make of it,
// however the phones are carried.
type orientCase struct {
	name     string // label
	scenario string // sim scenario
	pocket   bool   // walking, seen from a trouser pocket (leg swing added to the scenario)
	gyro     bool   // … on a phone with sensor fusion (gravity follows the leg)
	dur      float64
	wantRed  bool    // a real travelling push: must reach red
	redBy    float64 // … within this many seconds
	atMost   string  // otherwise the worst level allowed
	// maxWave is the most steps with a wave edge allowed (−1 = any).
	maxWave int
	// noSway: no phone may even be shown as swaying (walking must not read as sway).
	noSway bool
}

var orientCases = []orientCase{
	{name: "wave", scenario: "wave", dur: 70, wantRed: true, redBy: 40, maxWave: -1},
	{name: "wave-jump", scenario: "wave-jump", dur: 90, wantRed: true, redBy: 85, maxWave: -1},
	{name: "calm", scenario: "calm", dur: 60, atMost: "calm", maxWave: 0},
	{name: "dance (in-phase jumping + sway)", scenario: "dance", dur: 60, atMost: "calm", maxWave: 8},
	{name: "jump-stagger", scenario: "jump-stagger", dur: 90, atMost: "calm", maxWave: 12},
	{name: "walk (phone on chest)", scenario: "walk", dur: 60, atMost: "calm", maxWave: 8},
	{name: "walk (trouser pocket)", scenario: "calm", pocket: true, gyro: true, dur: 60, atMost: "calm", maxWave: 8, noSway: true},
	{name: "walk (pocket, no gyro)", scenario: "calm", pocket: true, dur: 60, atMost: "calm", maxWave: 8, noSway: true},
	{name: "march", scenario: "march", dur: 90, atMost: "calm", maxWave: 8},
	{name: "mexican", scenario: "mexican", dur: 90, atMost: "calm", maxWave: 12},
	{name: "sway", scenario: "sway", dur: 90, atMost: "calm", maxWave: 12},
}

func runCarried(t *testing.T, oc orientCase, mode carry, n int, seed int64, lay sim.Layout) outcome {
	t.Helper()
	return runCarriedCfg(t, oc, mode, n, seed, lay, DefaultConfig())
}

func runCarriedCfg(t *testing.T, oc orientCase, mode carry, n int, seed int64, lay sim.Layout, cfg Config) outcome {
	t.Helper()
	c := newCarrier(mode, n, seed)
	c.pocket, c.gyro = oc.pocket, oc.gyro
	return runScenarioVia(t, oc.scenario, n, oc.dur, 25, false, seed, cfg, lay, c.via)
}

// TestAnyOrientation: the same body motion gives the same outcome whether
// the phones are upright on the chest (no g, the old assumption), carried
// any way round and sending gravity with ~3° error, or a mix of the two.
//
// A real push must reach red in time in as many crowds as it does with
// upright phones, give or take one (the growing wave through a jumping
// crowd has a crowd or two that only just make it either way), and the
// look-alikes must stay as calm as they do upright.
func TestAnyOrientation(t *testing.T) {
	const n, seeds = 8, 8
	lay := sim.LineLayout(1, n)
	for _, oc := range orientCases {
		upright := 0
		if oc.wantRed {
			for seed := int64(1); seed <= seeds; seed++ {
				if o := runCarried(t, oc, carryUpright, n, seed, lay); o.redAt >= 0 && o.redAt <= oc.redBy {
					upright++
				}
			}
			if upright < seeds-1 {
				t.Errorf("%s: only %d/%d upright crowds reached red within %.0f s", oc.name, upright, seeds, oc.redBy)
			}
		}
		for _, mode := range []carry{carryUpright, carryUprightG, carryRandom, carryMixed} {
			if oc.pocket && mode != carryRandom && mode != carryMixed {
				continue // a pocket phone is not upright
			}
			if oc.wantRed && mode == carryUpright {
				continue // counted above
			}
			t.Run(oc.name+"/"+mode.String(), func(t *testing.T) {
				reds := 0
				for seed := int64(1); seed <= seeds; seed++ {
					o := runCarried(t, oc, mode, n, seed, lay)
					switch {
					case oc.wantRed:
						if o.redAt >= 0 && o.redAt <= oc.redBy {
							reds++
						}
						if o.maxLevel == protocol.LevelCalm {
							t.Errorf("seed %d: stayed calm", seed)
						}
						for d := range o.directions {
							if d != "+x" {
								t.Errorf("seed %d: wave direction %q, want +x (from positions, not device axes)", seed, d)
							}
						}
					case levelRank[o.maxLevel] > levelRank[oc.atMost]:
						t.Errorf("seed %d: reached %s, want at most %s", seed, o.maxLevel, oc.atMost)
					}
					if oc.maxWave >= 0 && o.waveSteps > oc.maxWave {
						t.Errorf("seed %d: %d steps with wave edges, want ≤ %d", seed, o.waveSteps, oc.maxWave)
					}
					// (A phone can show as swaying for the second or so between
					// its first sway score and enough history to see the rhythm.)
					if oc.noSway && mode == carryRandom && o.swayFrac > 0.03 {
						t.Errorf("seed %d: walking phones were shown as swaying %.0f %% of the time", seed, 100*o.swayFrac)
					}
				}
				if oc.wantRed {
					t.Logf("red within %.0f s: %d/%d (upright %d/%d)", oc.redBy, reds, seeds, upright, seeds)
					if reds < upright-1 {
						t.Errorf("red within %.0f s in %d/%d crowds, upright %d/%d", oc.redBy, reds, seeds, upright, seeds)
					}
				}
			})
		}
	}
}

// TestCrowdAnyOrientation: the push wave through a wandering crowd of
// phones carried any way round is found about as often as with upright ones.
func TestCrowdAnyOrientation(t *testing.T) {
	wave := orientCases[0]
	reds := map[carry]int{}
	for _, mode := range []carry{carryUpright, carryRandom, carryMixed} {
		for seed := int64(1); seed <= 8; seed++ {
			o := runCarried(t, wave, mode, crowdN, seed, sim.CrowdLayout(true))
			if o.redAt >= 0 && o.redAt <= 60 {
				reds[mode]++
			}
			if o.maxLevel == protocol.LevelCalm {
				t.Errorf("%s seed %d: crowd wave stayed calm", mode, seed)
			}
			for d := range o.directions {
				if d != "+x" {
					t.Errorf("%s seed %d: direction %q, want +x", mode, seed, d)
				}
			}
		}
	}
	t.Logf("red within 60 s: upright %d/8, random+g %d/8, mixed %d/8", reds[carryUpright], reds[carryRandom], reds[carryMixed])
	for _, mode := range []carry{carryRandom, carryMixed} {
		if reds[mode] < reds[carryUpright]-1 || reds[mode] < 5 {
			t.Errorf("%s: %d/8 crowds reached red within 60 s, upright %d/8", mode, reds[mode], reds[carryUpright])
		}
	}
}

// TestLevellingIsNeeded keeps the orientation tests honest: without g, the
// same randomly carried phones must do clearly worse on the push wave.
func TestLevellingIsNeeded(t *testing.T) {
	const n, seeds = 8, 8
	wave := orientCases[0]
	var with, without int
	for seed := int64(1); seed <= seeds; seed++ {
		if o := runCarried(t, wave, carryRandom, n, seed, sim.LineLayout(1, n)); o.redAt >= 0 && o.redAt <= wave.redBy {
			with++
		}
		if o := runCarried(t, wave, carryRandomNoG, n, seed, sim.LineLayout(1, n)); o.redAt >= 0 && o.redAt <= wave.redBy {
			without++
		}
	}
	t.Logf("wave red within %.0f s: %d/%d with g, %d/%d without", wave.redBy, with, seeds, without, seeds)
	if with != seeds || without > seeds/2 {
		t.Errorf("with g %d/%d, without %d/%d: expected all with and at most half without", with, seeds, without, seeds)
	}
}

// TestLevelUprightIsIdentity: a phone that reports g = (0, −1, 0) is split
// into exactly the numbers an unlevelled phone gives, and the basis follows
// g without jumps wherever it goes.
func TestLevelUprightIsIdentity(t *testing.T) {
	cfg := DefaultConfig()
	var l level
	l.set([3]float64{0, -1, 0}, 0, &cfg)
	if h1, v, h2 := l.split(1, 2, 3); h1 != 1 || v != 2 || h2 != 3 {
		t.Fatalf("upright split = %v %v %v, want 1 2 3", h1, v, h2)
	}
	// Walk g all the way round a great circle through "upside down" in 2°
	// steps: the basis must stay orthonormal and never turn more than g did.
	rng := rand.New(rand.NewSource(3))
	axis := randomUnit(rng)
	g := [3]float64{0, -1, 0}
	prev := l.e1
	for i := 0; i < 400; i++ {
		g = axisAngle(axis, 2*math.Pi/180).mul(g)
		u, _ := unit(g)
		l.set(u, int64(i)*100, &cfg)
		if math.Abs(dot(l.e1, l.g)) > 1e-9 || math.Abs(dot(l.e2, l.g)) > 1e-9 || math.Abs(dot(l.e1, l.e2)) > 1e-9 ||
			math.Abs(dot(l.e1, l.e1)-1) > 1e-9 || math.Abs(dot(l.e2, l.e2)-1) > 1e-9 {
			t.Fatalf("step %d: basis not orthonormal: g %v e1 %v e2 %v", i, l.g, l.e1, l.e2)
		}
		if c := dot(prev, l.e1); c < math.Cos(2.01*math.Pi/180) {
			t.Fatalf("step %d: e1 jumped %.1f° for a 2° change of g", i, math.Acos(c)*180/math.Pi)
		}
		prev = l.e1
	}
}

// TestGravityParsing: only a usable three-vector switches levelling on.
func TestGravityParsing(t *testing.T) {
	for _, bad := range [][]float64{nil, {}, {0, 0, 0}, {0, -1}, {0, -1, 0, 0}, {math.NaN(), 0, 1}, {math.Inf(1), 0, 0}} {
		if g := Gravity(bad); g != ([3]float64{}) {
			t.Errorf("Gravity(%v) = %v, want zero", bad, g)
		}
	}
	if g := Gravity([]float64{0, -9.81, 0}); g != ([3]float64{0, -1, 0}) {
		t.Errorf("Gravity normalises: got %v", g)
	}
	d := New(DefaultConfig())
	d.SetPhone("a", 1, 1)
	d.Add("a", Sample{T: 1000, AX: 1})
	if d.phones["a"].lev.on {
		t.Fatal("a sample without g switched levelling on")
	}
	d.Add("a", Sample{T: 1100, AX: 1, G: Gravity([]float64{0.6, -0.8, 0})})
	d.Add("a", Sample{T: 1200, AX: 1}) // g is held
	if l := d.phones["a"].lev; !l.on || l.g != ([3]float64{0.6, -0.8, 0}) {
		t.Fatalf("held gravity %+v", l)
	}
}

// TestTiltIsHandling: gravity swinging round in the device frame (the phone
// pulled out of a pocket) marks the phone as handled even when the rotation
// rate it reported stayed under the threshold; the slow lean of someone
// shifting their weight does not.
func TestTiltIsHandling(t *testing.T) {
	cfg := DefaultConfig()
	run := func(degPerSec float64) bool {
		d := New(cfg)
		d.SetPhone("a", 1, 1)
		seen := false
		for ms := int64(0); ms <= 4000; ms += 100 {
			ang := 0.0
			if ms >= 2000 {
				ang = math.Min(90, degPerSec*float64(ms-2000)/1000)
			}
			g := axisAngle([3]float64{0, 0, 1}, ang*math.Pi/180).mul([3]float64{0, -1, 0})
			d.Add("a", Sample{T: 1_000_000 + ms, AX: 0.01, Rot: 20, G: g})
			r := d.Step(1_000_000 + ms)
			seen = seen || r.Phones[0].Status == protocol.StatusHandling
		}
		return seen
	}
	if !run(150) {
		t.Error("90° in 0.6 s was not marked as handling")
	}
	if run(20) {
		t.Error("a slow 20°/s lean was marked as handling")
	}
}

// TestExplainWalking: the explanation of a pair of levelled phones says
// whether walking ruled it out; unlevelled pairs keep their old check list.
func TestExplainWalking(t *testing.T) {
	const n = 8
	oc := orientCase{scenario: "calm", pocket: true, gyro: true, dur: 20}
	c := newCarrier(carryRandom, n, 1)
	c.pocket, c.gyro = true, true
	sc, err := sim.NewLayout(oc.scenario, n, 1, sim.LineLayout(1, n))
	if err != nil {
		t.Fatal(err)
	}
	d := New(DefaultConfig())
	for i := 0; i < n; i++ {
		x, y := sc.Pos(i)
		d.SetPhone(fmt.Sprint(i), x, y)
	}
	const t0 = 1_700_000_000_000
	for _, e := range sc.Generate(t0, oc.dur) {
		d.Add(fmt.Sprint(e.Phone), c.via(e, Sample{T: e.T, AX: e.AX, AY: e.AY, AZ: e.AZ, Rot: e.Rot}))
	}
	r := d.Step(t0 + int64(oc.dur*1000))
	ex, ok := d.Explain(r.Edges[0].From, r.Edges[0].To)
	if !ok {
		t.Fatal("no explanation")
	}
	found := false
	for _, ch := range ex.Checks {
		if ch.Name == "Not walking" {
			found = true
			if ch.Pass {
				t.Errorf("walking pair passed the walking check: %s", ch.Detail)
			}
		}
	}
	if !found {
		t.Errorf("no walking check in %+v", ex.Checks)
	}
}

// TestOrientationReport is a measuring aid, not a regression test:
// PULSE_ORIENT=1 prints, for every body motion, how the detector does with
// upright phones, with phones carried any way round without g (the old code
// path) and with g. PULSE_SEEDS sets the number of crowds, PULSE_LAYOUT=crowd
// uses the wandering crowd instead of the line.
func TestOrientationReport(t *testing.T) {
	if os.Getenv("PULSE_ORIENT") == "" {
		t.Skip("set PULSE_ORIENT=1")
	}
	seeds := 20
	if v, err := strconv.Atoi(os.Getenv("PULSE_SEEDS")); err == nil && v > 0 {
		seeds = v
	}
	n, lay := 8, sim.LineLayout(1, 8)
	if os.Getenv("PULSE_LAYOUT") == "crowd" {
		n, lay = crowdN, sim.CrowdLayout(true)
	}
	modes := []carry{carryUpright, carryRandomNoG, carryRandom, carryMixed}
	cfg := DefaultConfig()
	if s := os.Getenv("PULSE_CFG"); s != "" {
		if err := json.Unmarshal([]byte(s), &cfg); err != nil {
			t.Fatal(err)
		}
	}
	fmt.Printf("%d phones, %s layout, %d seeds; g error %.0f°\n", n, lay.Kind, seeds, gErrDeg)
	fmt.Printf("%-32s %-13s %5s %6s %7s %10s %9s\n", "motion", "carried", "red", "yellow", "medRed", "waveSteps", "swaying")
	for _, oc := range orientCases {
		for _, mode := range modes {
			if oc.pocket && mode == carryUpright {
				continue
			}
			var red, yellow, steps int
			var sway float64
			var at []float64
			for seed := int64(1); seed <= int64(seeds); seed++ {
				o := runCarriedCfg(t, oc, mode, n, seed, lay, cfg)
				switch o.maxLevel {
				case protocol.LevelRed:
					red++
					at = append(at, o.redAt)
				case protocol.LevelYellow:
					yellow++
				}
				steps += o.waveSteps
				sway += o.swayFrac
			}
			sort.Float64s(at)
			med := "-"
			if len(at) > 0 {
				med = fmt.Sprintf("%.0f s", at[len(at)/2])
			}
			fmt.Printf("%-32s %-13s %2d/%-2d %3d/%-2d %7s %10d %8.0f%%\n", strings.TrimSpace(oc.name), mode,
				red, seeds, yellow, seeds, med, steps/seeds, 100*sway/float64(seeds))
		}
	}
}
