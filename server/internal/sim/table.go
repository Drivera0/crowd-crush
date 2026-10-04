package sim

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
)

// Table demo motion: what 2–5 phones send when the people holding them
// stand in a row at a table, 0.6 m apart (the demo spot), and do one of the
// things judges try. Unlike the line scenarios (an ideal phone upright on
// the chest, no gravity sent) these phones are held in the hand at a tilt,
// each turned its own way, and send their gravity vector like the phone
// page does, so the detector levels them.
//
// Noise is calibrated on a real Android phone recorded through the server
// (recordings/auto, 2026-10-03): lying on the table its 100 ms summaries
// scatter by 0.003–0.005 m/s² per axis at ~0.1°/s; held still in the hand
// by 0.05–0.15 m/s² per axis, rotation ~10°/s with spikes to 60–90°/s.
//
// Cases (everyone stands still for the first TableLeadIn seconds, so each
// case's timings are measured from the same start):
//
//	still     holding the phones still, chatting
//	jump      everyone jumping on the spot to one beat (2 Hz, ±50 ms)
//	dance     everyone swaying side to side to one beat (0.5 Hz) with a bounce
//	push      the person at one end is shoved toward the others every
//	          TablePushEvery seconds; each person shoves the next on,
//	          150–400 ms per hop, a little weaker at each hop
//	push1     the same, one push only
//	together  pressed shoulder to shoulder and swayed as one: shared,
//	          irregular low-frequency horizontal motion with a small,
//	          constant lag per person (40–100 ms) from one end
//	walk      walking around the table, each at their own pace
//	handle    turning the phones over, picking them up, shaking them
//
// Positions are not part of it: the demo spot puts phone i at
// x0 + i·0.6 m.
type Table struct {
	Case   string
	phones []*tablePhone
	lagMs  []float64 // push / together: lag of each person after the first (ms)
	pushes []float64 // push: when the first person is shoved (s)
	// push: when each push reaches each person (s) and how hard (m/s²)
	hitAt, hitAmp [][]float64
	// together: the shared sway, sampled at TableRawHz
	shared [2][]float64
}

// TableCases lists the table-demo cases.
var TableCases = []string{"still", "jump", "dance", "push", "push1", "together", "walk", "handle"}

// Table timing.
const (
	TableLeadIn    = 5.0  // s of standing still before the case starts
	TablePushEvery = 3.0  // s between deliberate pushes
	TableRawHz     = 60.0 // raw sensor rate; summaries average 6 samples
)

type tablePhone struct {
	rng *rand.Rand
	// Orientation of the phone in the hand: pitch back, yaw round the
	// vertical, roll (rad), plus slow wobble.
	pitch, yaw, roll float64
	gain             float64
	wob              [3][]float64 // orientation wobble (rad), sampled at TableRawHz
	post             [2][]float64 // postural sway, horizontal body x/z (m/s²)
	hand             [3][]float64 // hand drift (m/s², device frame)
	beat             float64      // jump/dance timing offset (s)
	cadence          float64      // walk: steps per second
	ph               [4]float64
	eps              []tableEpisode
}

// tableEpisode is a stretch with the phone handled: turned over, shaken.
type tableEpisode struct {
	start, end float64
	turn       float64 // rad the phone turns over the episode (gravity swings round)
	axis       int     // about which device axis
	sd         float64 // accel noise (m/s²)
	rot        float64 // rotation rate (deg/s)
	shake      bool
}

// NewTable creates a table-demo case for n phones lasting up to dur seconds.
func NewTable(name string, n int, seed int64, dur float64) (*Table, error) {
	ok := false
	for _, c := range TableCases {
		ok = ok || c == name
	}
	if !ok {
		return nil, fmt.Errorf("unknown table case %q (want one of %v)", name, TableCases)
	}
	if n < 1 {
		return nil, fmt.Errorf("need at least one phone")
	}
	r := rand.New(rand.NewSource(seed))
	nRaw := int(dur*TableRawHz) + 16
	t := &Table{Case: name}
	for i := 0; i < n; i++ {
		p := &tablePhone{
			rng:     rand.New(rand.NewSource(r.Int63())),
			pitch:   uniform(r, 25, 60) * math.Pi / 180,
			yaw:     uniform(r, -35, 35) * math.Pi / 180,
			roll:    uniform(r, -12, 12) * math.Pi / 180,
			gain:    uniform(r, 0.85, 1.15),
			beat:    uniform(r, -0.05, 0.05),
			cadence: uniform(r, 1.6, 2.0),
		}
		for k := range p.ph {
			p.ph[k] = uniform(r, 0, 2*math.Pi)
		}
		for k := range p.wob {
			p.wob[k] = bandNoise(r, nRaw, 0.05, 0.6, 1.5*math.Pi/180)
		}
		// Standing still: weight shifts and breathing, ~0.04 m/s².
		p.post[0] = bandNoise(r, nRaw, 0.1, 0.8, 0.04)
		p.post[1] = bandNoise(r, nRaw, 0.1, 0.8, 0.04)
		// The hand holding the phone wanders more than the body.
		for k := range p.hand {
			p.hand[k] = bandNoise(r, nRaw, 0.3, 3, 0.05)
		}
		t.phones = append(t.phones, p)
	}
	switch name {
	case "push", "push1":
		for s := TableLeadIn; s < dur-2; s += TablePushEvery + uniform(r, -0.3, 0.3) {
			t.pushes = append(t.pushes, s)
			// Each person shoves the next on: 150–400 ms per hop, a little
			// weaker each time.
			hit, amp := make([]float64, n), make([]float64, n)
			hit[0], amp[0] = s, 4.0
			for j := 1; j < n; j++ {
				hit[j] = hit[j-1] + uniform(r, 0.15, 0.4)
				amp[j] = amp[j-1] * uniform(r, 0.75, 0.95)
			}
			t.hitAt, t.hitAmp = append(t.hitAt, hit), append(t.hitAmp, amp)
			if name == "push1" {
				break
			}
		}
	case "together":
		lag := uniform(r, 40, 100)
		for i := 0; i < n; i++ {
			t.lagMs = append(t.lagMs, float64(i)*lag)
		}
		// One shared, irregular sway of the pressed group: ~0.4 m/s² RMS
		// across the row, half that front to back, 0.15–0.7 Hz.
		t.shared[0] = bandNoise(r, nRaw+int(TableRawHz), 0.15, 0.7, 0.4)
		t.shared[1] = bandNoise(r, nRaw+int(TableRawHz), 0.15, 0.7, 0.2)
	case "handle":
		for _, p := range t.phones {
			for s := TableLeadIn + uniform(r, 0, 3); s < dur-1; s += uniform(r, 2, 6) {
				d := uniform(r, 0.8, 2.5)
				p.eps = append(p.eps, tableEpisode{start: s, end: s + d, turn: uniform(r, 1.0, 3.0) * sign(r),
					axis: r.Intn(3), sd: uniform(r, 1, 4), rot: uniform(r, 150, 500), shake: r.Float64() < 0.4})
				s += d
			}
		}
	}
	return t, nil
}

// bandNoise is n samples (at TableRawHz) of noise band-limited to lo–hi Hz
// with the given RMS: white noise through a two-pole low-pass and a
// one-pole high-pass, normalised.
func bandNoise(r *rand.Rand, n int, lo, hi, rms float64) []float64 {
	out := make([]float64, n)
	dt := 1 / TableRawHz
	aL := 1 - math.Exp(-dt*2*math.Pi*hi)
	aH := 1 - math.Exp(-dt*2*math.Pi*lo)
	var l1, l2, dc float64
	// Warm the filters up so the start is like the rest.
	warm := int(3 / lo * TableRawHz)
	for i := -warm; i < n; i++ {
		w := r.NormFloat64()
		l1 += aL * (w - l1)
		l2 += aL * (l1 - l2)
		dc += aH * (l2 - dc)
		if i >= 0 {
			out[i] = l2 - dc
		}
	}
	var ss float64
	for _, v := range out {
		ss += v * v
	}
	if s := math.Sqrt(ss / float64(max(n, 1))); s > 0 {
		for i := range out {
			out[i] *= rms / s
		}
	}
	return out
}

func at(v []float64, t float64) float64 {
	i := int(t * TableRawHz)
	if i < 0 {
		i = 0
	}
	if i >= len(v) {
		i = len(v) - 1
	}
	return v[i]
}

// TableEvent is one 100 ms summary: device-frame acceleration (gravity
// removed), peak rotation rate and the gravity direction (device frame,
// unit, pointing down) as the phone page sends them.
type TableEvent struct {
	T          int64 // ms
	Phone      int
	AX, AY, AZ float64
	Rot        float64
	G          [3]float64
}

// N is the number of phones.
func (t *Table) N() int { return len(t.phones) }

// Generate produces every phone's summaries from 0 to dur seconds, sorted
// by time; t0 is the start time in ms.
func (t *Table) Generate(t0 int64, dur float64) []TableEvent {
	var evs []TableEvent
	for i := range t.phones {
		for s := 0.0; s < dur-1e-9; s += 0.1 {
			var e TableEvent
			const raw = 6
			for k := 0; k < raw; k++ {
				a, rot, g := t.raw(i, s+float64(k)/TableRawHz)
				e.AX += a[0] / raw
				e.AY += a[1] / raw
				e.AZ += a[2] / raw
				e.Rot = math.Max(e.Rot, rot)
				e.G[0] += g[0] / raw
				e.G[1] += g[1] / raw
				e.G[2] += g[2] / raw
			}
			e.T, e.Phone = t0+int64(math.Round(s*1000)), i
			evs = append(evs, e)
		}
	}
	sort.SliceStable(evs, func(a, b int) bool { return evs[a].T < evs[b].T })
	return evs
}

// tablePush is the body's response to one shove: pushed ~15 cm over a third
// of a second and caught again, a damped 1 Hz swing (peak ≈ 0.75·amp).
func tablePush(t float64) float64 {
	if t < 0 || t > 4 {
		return 0
	}
	return math.Exp(-t/0.5) * math.Sin(2*math.Pi*1.0*t)
}

// raw is one raw sample of phone i at time s: device-frame acceleration,
// rotation rate (deg/s) and gravity direction.
func (t *Table) raw(i int, s float64) (a [3]float64, rot float64, g [3]float64) {
	p := t.phones[i]
	n := func(sd float64) float64 { return p.rng.NormFloat64() * sd }
	// Body frame: x along the row (toward the far end), y up, z forward.
	bx := at(p.post[0], s)
	bz := at(p.post[1], s)
	by := 0.0
	rot = 4 + math.Abs(n(4))
	pitch := p.pitch + at(p.wob[0], s)
	yaw := p.yaw + at(p.wob[1], s)
	roll := p.roll + at(p.wob[2], s)
	act := s >= TableLeadIn
	tw := 2 * math.Pi * s

	switch t.Case {
	case "jump":
		if act {
			// Jumping on the spot: a hard landing every half second. The
			// arm holding the phone flexes, so some leaks sideways.
			tt := s + p.beat
			v := 3.5 * p.gain * (math.Sin(2*math.Pi*2*tt) + 0.35*math.Sin(2*math.Pi*4*tt+0.6))
			by += v
			bx += 0.15*v*math.Sin(p.ph[0]) + 0.2*math.Sin(2*math.Pi*0.5*tt+p.ph[1])
			bz += 0.2 * v * math.Cos(p.ph[0])
			rot += 40 + 40*math.Abs(math.Sin(2*math.Pi*2*tt)) + math.Abs(n(15))
			pitch += 4 * math.Pi / 180 * math.Sin(2*math.Pi*2*tt+p.ph[2])
		}
	case "dance":
		if act {
			tt := s + p.beat
			bx += 1.0 * p.gain * math.Sin(2*math.Pi*0.5*tt)
			by += 1.5 * p.gain * math.Sin(2*math.Pi*2*tt)
			bz += 0.3 * math.Sin(2*math.Pi*2*tt+p.ph[1])
			rot += 25 + 20*math.Abs(math.Sin(2*math.Pi*0.5*tt)) + math.Abs(n(10))
			roll += 5 * math.Pi / 180 * math.Sin(2*math.Pi*0.5*tt)
		}
	case "push", "push1":
		for k := range t.pushes {
			v := p.gain * t.hitAmp[k][i] * tablePush(s-t.hitAt[k][i])
			bx += v
			bz += 0.25 * v * math.Sin(p.ph[3])
			by += 0.15 * math.Abs(v)
			rot += 35 * math.Abs(v)
			pitch += 0.02 * v
		}
	case "together":
		if act {
			d := t.lagMs[i] / 1000
			bx += p.gain * at(t.shared[0], s-d+1)
			bz += p.gain * at(t.shared[1], s-d+1)
			rot += 6 * math.Abs(at(t.shared[0], s-d+1))
		}
	case "walk":
		if act {
			f := p.cadence
			by += 2.0 * p.gain * math.Sin(f*tw+p.ph[0])
			bx += 0.4 * p.gain * math.Sin(f/2*tw+p.ph[1])
			bz += 0.4 * p.gain * math.Sin(f*tw+p.ph[2])
			rot += 20 + 15*math.Abs(math.Sin(f/2*tw)) + math.Abs(n(10))
			// Turning round the table: the heading drifts.
			yaw += 0.6 * math.Sin(2*math.Pi*0.05*s+p.ph[3])
			pitch += 3 * math.Pi / 180 * math.Sin(f*tw)
		}
	}

	// Device orientation: columns are the device axes in body coordinates.
	m := orient(pitch, yaw, roll)
	for _, e := range p.eps {
		if s < e.start || s >= e.end {
			continue
		}
		f := (s - e.start) / (e.end - e.start)
		ang := e.turn * (0.5 - 0.5*math.Cos(math.Pi*f))
		m = mulM(m, axisRot(e.axis, ang))
		bx, by, bz = bx+n(e.sd), by+n(e.sd), bz+n(e.sd)
		rot = e.rot + math.Abs(n(40))
		if e.shake {
			v := 12 * math.Sin(2*math.Pi*5*s)
			bx += v
			rot = math.Max(rot, 300)
		}
	}
	body := [3]float64{bx, by, bz}
	for k := 0; k < 3; k++ {
		col := [3]float64{m[0][k], m[1][k], m[2][k]}
		a[k] = dot3(col, body) + at(p.hand[k], s) + n(0.03)
		g[k] = -col[1] // gravity (0, −1, 0) in body coordinates
	}
	return
}

// orient is the phone's orientation in the hand: held upright facing the
// person, pitched back, turned (yaw) and rolled.
func orient(pitch, yaw, roll float64) [3][3]float64 {
	return mulM(mulM(axisRot(1, yaw), axisRot(0, -pitch)), axisRot(2, roll))
}

func axisRot(axis int, a float64) [3][3]float64 {
	c, s := math.Cos(a), math.Sin(a)
	switch axis {
	case 0:
		return [3][3]float64{{1, 0, 0}, {0, c, -s}, {0, s, c}}
	case 1:
		return [3][3]float64{{c, 0, s}, {0, 1, 0}, {-s, 0, c}}
	}
	return [3][3]float64{{c, -s, 0}, {s, c, 0}, {0, 0, 1}}
}

func mulM(a, b [3][3]float64) [3][3]float64 {
	var o [3][3]float64
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			for k := 0; k < 3; k++ {
				o[i][j] += a[i][k] * b[k][j]
			}
		}
	}
	return o
}

func dot3(a, b [3]float64) float64 { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }
