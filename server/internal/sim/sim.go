// Package sim generates fake phone motion for the simulator and for tests.
//
// Phones stand on a grid; scenarios that travel (shove, wave) move along the
// columns of each row with a fixed lag per person.
package sim

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
)

// Scenarios lists the available scenario names.
var Scenarios = []string{"calm", "walk", "dance", "handle", "shove", "wave"}

// Scenario produces deterministic motion for N phones.
type Scenario struct {
	Name   string
	Rows   int
	Cols   int
	LagSec float64 // wave lag per person
	phones []*phoneSim
}

type phoneSim struct {
	row, col int
	rng      *rand.Rand
	gain     float64 // per-person amplitude variation
	freq     float64 // walking cadence
	ph       [4]float64
	// handle scenario: when the next handling episode starts and ends
	handleStart, handleEnd float64
}

// New creates a scenario for n phones on a rows×cols grid (filled row by row).
func New(name string, n, rows, cols int, seed int64) (*Scenario, error) {
	known := false
	for _, s := range Scenarios {
		known = known || s == name
	}
	if !known {
		return nil, fmt.Errorf("unknown scenario %q (want one of %v)", name, Scenarios)
	}
	if rows*cols < n {
		return nil, fmt.Errorf("%d phones don't fit a %dx%d grid", n, rows, cols)
	}
	s := &Scenario{Name: name, Rows: rows, Cols: cols, LagSec: 0.25}
	master := rand.New(rand.NewSource(seed))
	for i := 0; i < n; i++ {
		p := &phoneSim{
			row:  i / cols,
			col:  i % cols,
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
	return s, nil
}

// N is the number of phones.
func (s *Scenario) N() int { return len(s.phones) }

// Pos returns the grid cell of phone i.
func (s *Scenario) Pos(i int) (row, col int) { return s.phones[i].row, s.phones[i].col }

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
		ax += p.gain * 2.0 * push(t-8-float64(p.col)*s.LagSec)
		rot += 40 * math.Abs(push(t-8-float64(p.col)*s.LagSec))
	case "wave":
		// A sideways push every 2.5 s travelling down the line, growing
		// from barely noticeable to violent over 60 s.
		const period = 2.5
		amp := 0.3 + 2.2*math.Min(1, t/60)
		local := t - float64(p.col)*s.LagSec
		var x float64
		for k := math.Floor(local / period); k >= 0 && k > math.Floor(local/period)-3; k-- {
			x += push(local - k*period)
		}
		ax += p.gain * amp * x
		rot += 40 * math.Abs(x) * amp / 2.5
	}
	return
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
