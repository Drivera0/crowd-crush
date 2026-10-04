// Package sim generates fake phone motion for the simulator and for tests.
//
// Phones stand in the venue (metres) in a line layout (a rows×cols grid
// 0.6 m apart, the original demo) or a crowd layout (dense groups plus
// stragglers, optionally wandering). Scenarios that travel (shove, wave,
// mexican, walkpast, procession, sway) reach each phone after a delay equal
// to its position along the travel direction (+x) divided by the walking
// speed: 0.6 m per 0.25 s, i.e. 250 ms per person on the line. gather walks
// people into a tight group in front of the stage and back out again.
//
// Besides the true positives (shove, wave) there is a set of false-positive
// scenarios: things real crowds do that look a bit like a travelling wave and
// must not raise the alarm (sway, sway-slow, mexican, walkpast, procession,
// march, pocket, bump, jump-stagger). Amplitudes are physically motivated
// and comparable to the wave push (peak ≈ 1.6 m/s² at full strength).
package sim

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
)

// Scenarios lists the available scenario names.
var Scenarios = []string{
	"calm", "walk", "dance", "handle", "shove", "wave",
	"sway", "sway-slow", "mexican", "walkpast", "procession", "march",
	"pocket", "bump", "jump-stagger", "wave-jump", "gather",
}

// Scenario produces deterministic motion for N phones.
type Scenario struct {
	Name   string
	Rows   int
	Cols   int
	Layout Layout
	LagSec float64 // wave lag per person
	minX   float64 // crowd: x of the first phone along the travel direction
	phones []*phoneSim
	cycle  float64 // event-driven scenarios repeat their schedule every cycle seconds
}

type phoneSim struct {
	row, col int     // line layout cell
	hx, hy   float64 // home position (m)
	k        float64 // position along the travel direction, in people (0.6 m) from the first
	mov      *mover  // wandering (crowd layout with Move)
	g        *gatherPlan
	rng      *rand.Rand
	gain     float64 // per-person amplitude variation
	freq     float64 // walking cadence
	ph       [4]float64
	// handle scenario: when the next handling episode starts and ends
	handleStart, handleEnd float64

	// false-positive scenarios
	delay float64   // reaction delay (s)
	tilt  float64   // how much vertical motion leaks into x (phone not perfectly upright)
	dur   float64   // per-person movement duration (mexican wave)
	jolts []jolt    // brief bumps (walkpast, procession, bump)
	eps   []episode // pocket / drop / fumble episodes
}

// jolt is a brief bump: a damped oscillation starting at t.
type jolt struct {
	t, amp, tau, freq float64
	dx, dy, dz        float64 // direction weights
	rot               float64 // extra rotation rate while it lasts (deg/s)
}

func (j jolt) at(t float64) float64 {
	tau := t - j.t
	if tau < 0 || tau > 6*j.tau {
		return 0
	}
	return j.amp * math.Exp(-tau/j.tau) * math.Sin(2*math.Pi*j.freq*tau)
}

// episode is a stretch of time with the phone off the chest.
type episode struct {
	start, end float64
	kind       int
	rot        float64 // rotation rate (deg/s)
	sd         float64 // accel noise (m/s²)
	amp, freq  float64 // in-pocket sway from the hips/legs
	ph         float64
	dx, dy, dz float64 // impact direction
}

const (
	epHandle = iota // in the hand: rotating, messy accel (sometimes below the handling threshold)
	epPocket        // in a pocket: jostled by hips and legs
	epFall          // free fall: gravity-removed accel reads ~g
	epImpact        // hits the floor
	epStill         // lying on the floor: no breathing, no sway
)

// New creates a scenario for n phones in a line layout on a rows×cols grid
// (filled row by row).
func New(name string, n, rows, cols int, seed int64) (*Scenario, error) {
	return NewLayout(name, n, seed, LineLayout(rows, cols))
}

// NewLayout creates a scenario for n phones placed by lay.
func NewLayout(name string, n int, seed int64, lay Layout) (*Scenario, error) {
	lay = lay.withDefaults()
	rows, cols := lay.Rows, lay.Cols
	if lay.Kind == LayoutLine && cols <= 0 {
		rows = max(rows, 1)
		cols = (n + rows - 1) / rows
		lay.Rows, lay.Cols = rows, cols
	}
	known := false
	for _, s := range Scenarios {
		known = known || s == name
	}
	if !known {
		return nil, fmt.Errorf("unknown scenario %q (want one of %v)", name, Scenarios)
	}
	switch lay.Kind {
	case LayoutLine:
		if rows*cols < n {
			return nil, fmt.Errorf("%d phones don't fit a %dx%d grid", n, rows, cols)
		}
	case LayoutCrowd:
	default:
		return nil, fmt.Errorf("unknown layout %q (want line or crowd)", lay.Kind)
	}
	s := &Scenario{Name: name, Rows: rows, Cols: cols, Layout: lay, LagSec: 0.25}
	master := rand.New(rand.NewSource(seed))
	for i := 0; i < n; i++ {
		p := &phoneSim{
			rng:  rand.New(rand.NewSource(master.Int63())),
			gain: 0.85 + 0.3*master.Float64(),
			freq: 1.6 + 0.5*master.Float64(),
		}
		for k := range p.ph {
			p.ph[k] = 2 * math.Pi * master.Float64()
		}
		p.handleStart = 3 + 15*master.Float64()
		p.handleEnd = p.handleStart + 1.5 + master.Float64()
		s.phones = append(s.phones, p)
	}
	// Positions use their own generator too, so the line layout's motion
	// stays bit-for-bit identical to before positions existed.
	s.place(seed)
	// Scenario-specific schedules use their own generator so the original
	// scenarios stay bit-for-bit identical for a given seed.
	s.setup(rand.New(rand.NewSource(seed ^ 0x5eed5eed)))
	return s, nil
}

func uniform(r *rand.Rand, lo, hi float64) float64 { return lo + (hi-lo)*r.Float64() }

func sign(r *rand.Rand) float64 {
	if r.Intn(2) == 0 {
		return -1
	}
	return 1
}

// setup draws the per-person parameters and event schedules of the
// false-positive scenarios.
func (s *Scenario) setup(r *rand.Rand) {
	for _, p := range s.phones {
		switch s.Name {
		case "sway":
			// Swaying to music at 0.5 Hz. People further from the stage
			// react later (100 ms per person) plus their own 50–200 ms.
			p.delay = p.k*0.10 + uniform(r, 0.05, 0.2)
		case "sway-slow":
			// A slow ballad, one sway every 5 s (0.2 Hz): half a period
			// (2.5 s) no longer fits the ±1.5 s lag search, so the mirror
			// peak that gives 0.5 Hz sway away is out of view.
			p.delay = p.k*0.15 + uniform(r, 0.05, 0.2)
		case "mexican":
			p.delay = uniform(r, -0.05, 0.05)
			p.tilt = uniform(r, -0.2, 0.2)
			p.dur = uniform(r, 1.0, 1.4)
		case "jump-stagger", "wave-jump":
			p.delay = uniform(r, 0, 0.3)
			p.tilt = sign(r) * uniform(r, 0.1, 0.3)
		}
	}
	switch s.Name {
	case "walkpast":
		// One person squeezes along the line at ~1.2 m/s (0.5 s per person,
		// 0.6 m apart), nudging each phone sideways once.
		s.cycle = 90
		for _, p := range s.phones {
			p.jolts = append(p.jolts, s.brush(r, 10+p.k*0.5+r.NormFloat64()*0.04, uniform(r, 1.5, 2.5)))
		}
	case "procession":
		// A procession walks past alongside the line in the same direction at
		// a similar pace, 1.5–4 s apart. Each walker brushes about half the
		// phones in turn, lightly (peak 0.45–0.9 m/s²): they pass by, they
		// don't force their way through.
		s.cycle = 120
		for t := 5.0; t < s.cycle-10; t += uniform(r, 1.5, 4) {
			per := uniform(r, 0.45, 0.55)
			for _, p := range s.phones {
				if r.Float64() < 0.5 {
					p.jolts = append(p.jolts, s.brush(r, t+p.k*per+r.NormFloat64()*0.04, uniform(r, 0.8, 1.6)))
				}
			}
		}
	case "bump":
		// Random neighbours bump into each other: A moves, B is knocked
		// 30–300 ms later, and that's it: nothing travels further.
		s.cycle = 120
		for t := uniform(r, 0.5, 3); t < s.cycle-3; t += 0.5 + r.ExpFloat64()*2 {
			a := s.phones[r.Intn(len(s.phones))]
			var nb []*phoneSim
			for _, q := range s.phones {
				if s.Layout.Kind == LayoutLine {
					if q.row == a.row && (q.col == a.col-1 || q.col == a.col+1) {
						nb = append(nb, q)
					}
				} else if q != a && math.Hypot(q.hx-a.hx, q.hy-a.hy) <= 1.0 {
					nb = append(nb, q)
				}
			}
			if len(nb) == 0 {
				continue
			}
			b := nb[r.Intn(len(nb))]
			amp := sign(r) * uniform(r, 0.8, 2.0)
			a.jolts = append(a.jolts, jolt{t: t, amp: amp, tau: 0.35, freq: uniform(r, 0.8, 1.2), dx: 1, dy: 0.2, dz: 0.3, rot: uniform(r, 30, 90)})
			b.jolts = append(b.jolts, jolt{t: t + uniform(r, 0.03, 0.3), amp: sign(r) * math.Abs(amp) * uniform(r, 0.6, 1.0), tau: 0.35, freq: uniform(r, 0.8, 1.2), dx: 1, dy: 0.2, dz: 0.3, rot: uniform(r, 30, 90)})
		}
	case "pocket":
		s.cycle = 120
		for _, p := range s.phones {
			for t := uniform(r, 1, 8); t < s.cycle-25; {
				switch x := r.Float64(); {
				case x < 0.45: // into a pocket, jostled there, taken out again
					t = p.handling(r, t, uniform(r, 0.8, 1.5))
					end := t + uniform(r, 4, 15)
					p.eps = append(p.eps, episode{start: t, end: end, kind: epPocket, rot: uniform(r, 20, 60),
						sd: uniform(r, 0.2, 0.5), amp: uniform(r, 0.3, 0.8), freq: uniform(r, 0.6, 1.2), ph: uniform(r, 0, 2*math.Pi)})
					t = p.handling(r, end, uniform(r, 0.8, 1.5))
				case x < 0.75: // dropped: free fall, impact, lying still, picked up
					fall := uniform(r, 0.3, 0.45)
					p.eps = append(p.eps, episode{start: t, end: t + fall, kind: epFall, rot: uniform(r, 50, 700)})
					t += fall
					d := [3]float64{r.NormFloat64(), r.NormFloat64(), r.NormFloat64()}
					nd := math.Sqrt(d[0]*d[0]+d[1]*d[1]+d[2]*d[2]) + 1e-9
					p.eps = append(p.eps, episode{start: t, end: t + 0.1, kind: epImpact, rot: uniform(r, 300, 800),
						amp: uniform(r, 15, 40), dx: d[0] / nd, dy: d[1] / nd, dz: d[2] / nd})
					t += 0.1
					still := uniform(r, 2, 5)
					p.eps = append(p.eps, episode{start: t, end: t + still, kind: epStill})
					t = p.handling(r, t+still, uniform(r, 1, 2))
				default: // fumbled / checked in the hand
					t = p.handling(r, t, uniform(r, 1.5, 4))
				}
				t += uniform(r, 5, 20)
			}
		}
	}
}

// brush is a sideways nudge from someone squeezing past: the body gives way
// and swings back, peak ≈ 0.58·amp (amp 2 → 1.2 m/s², a bit under a full
// wave push).
func (s *Scenario) brush(r *rand.Rand, t, amp float64) jolt {
	return jolt{t: t, amp: amp, tau: 0.5, freq: 0.8, dx: 1, dy: 0.3, dz: 0.4, rot: uniform(r, 60, 120)}
}

// handling adds an in-the-hand episode starting at t and returns its end.
// About a third of them rotate slower than the 200°/s handling threshold.
func (p *phoneSim) handling(r *rand.Rand, t, d float64) float64 {
	p.eps = append(p.eps, episode{start: t, end: t + d, kind: epHandle, rot: uniform(r, 120, 400), sd: uniform(r, 1.0, 2.0)})
	return t + d
}

// N is the number of phones.
func (s *Scenario) N() int { return len(s.phones) }

// Cell returns the grid cell of phone i (line layout; 0, 0 in a crowd).
func (s *Scenario) Cell(i int) (row, col int) { return s.phones[i].row, s.phones[i].col }

// Pos returns phone i's starting position in venue metres.
func (s *Scenario) Pos(i int) (x, y float64) { return s.PosAt(i, 0) }

// Summary returns what phone i would send for the 100 ms starting at t
// (seconds, true time): mean acceleration over 6 raw samples and max rotation.
func (s *Scenario) Summary(i int, t float64) (ax, ay, az, rot float64) {
	const raw = 6
	for k := 0; k < raw; k++ {
		x, y, z, r := s.raw(i, t+float64(k)*0.1/raw)
		ax += x / raw
		ay += y / raw
		az += z / raw
		rot = math.Max(rot, r)
	}
	return
}

// raw returns one raw sample: acceleration (m/s², gravity removed) and
// rotation-rate magnitude (deg/s).
func (s *Scenario) raw(i int, t float64) (ax, ay, az, rot float64) {
	p := s.phones[i]
	k := s.kAt(i, t)
	n := func(sd float64) float64 { return p.rng.NormFloat64() * sd }
	tw := 2 * math.Pi * t

	// Everyone breathes and shifts a little, independently.
	ax = 0.04*math.Sin(0.3*tw+p.ph[0]) + n(0.05)
	ay = n(0.05)
	az = 0.04*math.Sin(0.25*tw+p.ph[1]) + n(0.05)
	rot = 3 + math.Abs(n(3))

	switch s.Name {
	case "walk":
		ay += 2.0 * p.gain * math.Sin(p.freq*tw+p.ph[2])
		ax += 0.35 * p.gain * math.Sin(p.freq/2*tw+p.ph[3])
		az += 0.3 * p.gain * math.Sin(p.freq*tw+p.ph[0])
		rot += 25 + math.Abs(n(10))
	case "dance":
		// Everyone on the same beat: jumps at 2 Hz, sways at 0.5 Hz.
		// People are never perfectly in time: ±40 ms each.
		dt := (p.ph[2]/math.Pi - 1) * 0.04
		ay += 3.0 * p.gain * math.Sin(2*2*math.Pi*(t+dt))
		ax += 1.0 * p.gain * math.Sin(0.5*2*math.Pi*(t+dt))
		az += 0.4 * p.gain * math.Sin(2*2*math.Pi*(t+dt))
		rot += 40 + math.Abs(n(15))
	case "handle":
		// Each phone gets picked up now and then: big rotation, messy accel.
		period := 20.0
		local := math.Mod(t, period)
		if local >= p.handleStart && local < p.handleEnd {
			ax += n(1.5)
			ay += n(1.5)
			az += n(1.5)
			rot = 250 + math.Abs(n(80))
		}
	case "shove":
		ax += p.gain * 2.0 * push(t-8-k*s.LagSec)
		rot += 40 * math.Abs(push(t-8-k*s.LagSec))
	case "wave", "wave-jump":
		// A sideways push every 2.5 s travelling down the line, growing
		// from barely noticeable to violent over 60 s.
		const period = 2.5
		amp := 0.3 + 2.2*math.Min(1, t/60)
		local := t - k*s.LagSec
		var x float64
		for k := math.Floor(local / period); k >= 0 && k > math.Floor(local/period)-3; k-- {
			x += push(local - k*period)
		}
		ax += p.gain * amp * x
		rot += 40 * math.Abs(x) * amp / 2.5
		if s.Name == "wave-jump" {
			// The same wave while the crowd jumps to the music: the vertical
			// motion must not hide the push.
			jx, jy, jz := p.jump(t)
			ax, ay, az = ax+jx, ay+jy, az+jz
			rot += 40
		}

	case "sway", "sway-slow":
		// Whole crowd sways side to side to the music. Nobody is a
		// metronome: ±50 ms timing wobble and ±15 % amplitude drift.
		// Amplitude from displacement: a = (2πf)²·x. ±7 cm at 0.5 Hz is
		// 0.7 m/s²; a big ±19 cm ballad sway at 0.2 Hz is only 0.3 m/s².
		f, a := 0.5, 0.7
		if s.Name == "sway-slow" {
			f, a = 0.2, 0.3
		}
		tt := t - p.delay + 0.05*math.Sin(2*math.Pi*0.07*t+p.ph[2])
		a *= p.gain * (0.85 + 0.15*math.Sin(2*math.Pi*0.03*t+p.ph[3]))
		ax += a * math.Sin(2*math.Pi*f*tt)
		az += 0.2 * a * math.Sin(2*math.Pi*f*tt+0.5)
		ay += 0.3 * math.Abs(a) * math.Sin(2*2*math.Pi*f*tt) // a small dip on each side
		rot += 15 + math.Abs(n(5))

	case "mexican":
		// Stadium wave: stand up and throw the arms up, then sit, one
		// person every 250 ms down the line, coming round every 8 s.
		const period = 8.0
		if t < 3 {
			break
		}
		tau := math.Mod(t-3-k*s.LagSec-p.delay, period)
		if tau < 0 {
			tau += period
		}
		if tau < p.dur {
			w := 2 * math.Pi * tau / p.dur
			// Rise 0.4 m and back: a = h·(2π/T)²·cos ≈ 4–5 m/s² peak.
			vy := 4.0 * p.gain * math.Cos(w)
			ay += vy
			ax += 0.25*4.0*p.gain*math.Sin(w) + p.tilt*vy // lean, plus tilt crosstalk
			az += 0.3 * 4.0 * p.gain * math.Sin(w)
			rot += 60 + 40*math.Abs(math.Sin(w))
		}

	case "jump-stagger":
		// Everyone jumps to a 2 Hz beat but reacts 0–300 ms late, with a
		// little timing wobble. Vertical, leaking into x through the tilt.
		jx, jy, jz := p.jump(t)
		ax, ay, az = ax+jx, ay+jy, az+jz
		rot += 40 + math.Abs(n(15))

	case "gather":
		// Walking while moving (to the stage or away), standing otherwise.
		if p.g != nil && p.g.walking(t) {
			ay += 2.0 * p.gain * math.Sin(p.freq*tw+p.ph[2])
			ax += 0.35 * p.gain * math.Sin(p.freq/2*tw+p.ph[3])
			az += 0.3 * p.gain * math.Sin(p.freq*tw+p.ph[0])
			rot += 25 + math.Abs(n(10))
		}

	case "march":
		// The line itself walks off in the same direction with nearly the
		// same cadence (1.8 Hz ± 0.02): steady phase offsets, all periodic.
		f := 1.8 + 0.04*(p.freq-1.85)/0.5
		ay += 2.0 * p.gain * math.Sin(f*tw+p.ph[2])
		ax += 0.45 * p.gain * math.Sin(f/2*tw+p.ph[3])
		az += 0.3 * p.gain * math.Sin(f*tw+p.ph[0])
		rot += 25 + math.Abs(n(10))
	}

	lt := t
	if s.cycle > 0 {
		lt = math.Mod(t, s.cycle)
	}
	for _, j := range p.jolts {
		if v := j.at(lt); v != 0 {
			ax += j.dx * v
			ay += j.dy * v
			az += j.dz * v
			rot += j.rot * math.Abs(v) / math.Abs(j.amp)
		}
	}
	for _, e := range p.eps {
		if lt < e.start || lt >= e.end {
			continue
		}
		switch e.kind {
		case epHandle:
			ax, ay, az = ax+n(e.sd), ay+n(e.sd), az+n(e.sd)
			rot = e.rot + math.Abs(n(30))
		case epPocket:
			v := e.amp * math.Sin(2*math.Pi*e.freq*lt+e.ph)
			ax, ay, az = ax+v+n(e.sd), ay+0.5*v+n(e.sd), az+n(e.sd)
			rot += e.rot
		case epFall:
			ax, ay, az = n(0.3), -9.8+n(0.3), n(0.3)
			rot = e.rot
		case epImpact:
			ax, ay, az = e.amp*e.dx, e.amp*e.dy, e.amp*e.dz
			rot = e.rot
		case epStill:
			ax, ay, az = n(0.01), n(0.01), n(0.01)
			rot = math.Abs(n(1))
		}
	}
	return
}

// jump is one person jumping to a 2 Hz beat, late by their reaction delay.
func (p *phoneSim) jump(t float64) (ax, ay, az float64) {
	tt := t - p.delay - 0.03*math.Sin(2*math.Pi*0.2*t+p.ph[2])
	vy := 3.5 * p.gain * math.Sin(2*math.Pi*2*tt)
	return p.tilt*vy + 0.2*math.Sin(2*math.Pi*0.5*tt), vy, 0.15 * vy
}

// push is one shove: a damped sway that dies out over ~3 s.
func push(t float64) float64 {
	if t < 0 {
		return 0
	}
	return math.Exp(-t/0.9) * math.Sin(2*math.Pi*0.6*t)
}

// Event is one simulated message for offline recording.
type Event struct {
	T          int64 // ms
	Phone      int
	AX, AY, AZ float64
	Rot        float64
}

// Generate produces every phone's 100 ms summaries from 0 to dur seconds,
// sorted by time; t0 is the start time in ms.
func (s *Scenario) Generate(t0 int64, dur float64) []Event {
	var evs []Event
	for i := range s.phones {
		for t := 0.0; t < dur; t += 0.1 {
			ax, ay, az, rot := s.Summary(i, t)
			evs = append(evs, Event{T: t0 + int64(math.Round(t*1000)), Phone: i, AX: ax, AY: ay, AZ: az, Rot: rot})
		}
	}
	sort.SliceStable(evs, func(a, b int) bool { return evs[a].T < evs[b].T })
	return evs
}
