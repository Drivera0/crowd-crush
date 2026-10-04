package crowdsim

import (
	"fmt"
	"math"
	"math/rand"

	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// Phones from bodies. A phone is held upright, flat on the chest, screen
// out. Its axes (as the phone page reports them, gravity removed):
//
//   - x: left/right relative to where the person faces;
//   - z: forward/back;
//   - y: vertical.
//
// Horizontal acceleration is the body's own dv/dt from the forces, rotated
// into the frame of the body's facing (Agent.face: walkers face where they
// are going, people standing face the stage or the bar they queue at; see
// orient in behaviour.go). On top of that, what a body does that the disc
// model can't show:
//
//   - gait while walking: vertical bounce at the step frequency (1.6–2.2 Hz,
//     rising with speed) and a smaller lateral sway at half of it;
//   - standing: breathing (0.2–0.33 Hz, ±0.02 m/s²) and now and then a shift
//     of weight from one foot to the other (2–5 cm over 0.8–1.6 s, every
//     4–16 s, ≲ 0.2 m/s²);
//   - music (not during the intermission or an evacuation): a quarter of the
//     people sway gently (0.1–0.4 m/s² lateral at half the beat); on
//     "dance" everyone standing sways harder (0.5–2.5 m/s²) and most bounce
//     on the beat (1–4 m/s² vertical), each with their own reaction delay
//     (0–300 ms) and starting side, fading in and out over ~1.5 s;
//   - sensor noise (σ 0.04 m/s² per raw sample), and a rotation rate from
//     turning plus a few °/s of tremor, with rare handling episodes (about
//     one per 5 minutes per phone, 2–4 s at 250–500 °/s).
//
// Crush compression needs nothing extra: it is the body's acceleration
// under contact forces.
//
// Raw samples are taken at 50 Hz (each world tick) and summarised every
// 100 ms like the phone page: mean acceleration per axis, max rotation
// rate. T is the server clock (the phones are already synced). The
// position is sent every PosEveryTicks ticks (100 ms) whenever it changed
// at centimetre resolution: unlike a real phone there is no movement
// threshold, so the dashboard shows the bodies' true, smooth motion.

// Event kinds: what a phone would have sent, for the app to feed through
// its normal phone path.
type EventKind int

const (
	EvHello EventKind = iota
	EvSync
	EvPos
	EvMotion
	EvGone
	// EvGPS is a GPS-like fix (messy phones only, see realism.go): X, Y in
	// venue metres, not clamped, with the accuracy radius Acc (m). The
	// receiver smooths and gates it as the server does a live fix.
	EvGPS
)

// Event is one phone message.
type Event struct {
	Kind EventKind
	ID   string
	X, Y float64 // hello, pos, gps
	Acc  float64 // gps: reported accuracy radius (m)
	// Auto: a hello without a position, as a GPS phone's is (its hello
	// carries only the fix, which follows as EvGPS). The receiver places
	// the phone where the server places such a phone (the default grid
	// cell) until a usable fix arrives.
	Auto        bool
	Offset, RTT int64           // sync
	M           protocol.Motion // motion
}

const (
	summaryTicks = 5    // 100 ms at 50 Hz
	noiseSD      = 0.04 // m/s² per raw sample
	handleRate   = 1.0 / 300
)

// PosEveryTicks is how often a phone reports its position, in 20 ms ticks
// (5 = every 100 ms, 10 Hz).
var PosEveryTicks = 5

type phone struct {
	id      string
	rng     *rand.Rand
	step    float64 // gait phase
	breathF float64
	breathP float64
	sx, sy  float64 // last sent position (cm)
	ticks   int
	hello   bool
	// current 100 ms window
	n          int
	ax, ay, az float64
	rot        float64
	handleEnd  float64
	handleRot  float64
	offset     int64
	rtt        int64
	dev        *Device // messy phone (realism.go); nil = ideal
}

func newPhone(w *World, a *Agent) *phone {
	r := rand.New(rand.NewSource(w.rng.Int63()))
	p := &phone{
		id: fmt.Sprintf("sim-%04d", a.ID), rng: r,
		breathF: 0.2 + 0.13*r.Float64(), breathP: 2 * math.Pi * r.Float64(),
		offset: int64(r.NormFloat64() * 800), rtt: int64(25 + r.ExpFloat64()*40),
	}
	if !w.realism.Ideal() {
		// Its own random stream, derived from the world seed and the
		// person, so the world's and the ideal phone's draws are untouched.
		p.dev = NewDevice(p.id, w.realism, mixSeed(w.seed, int64(a.ID)))
	}
	return p
}

// mixSeed derives an independent seed (splitmix64).
func mixSeed(seed, k int64) int64 {
	z := uint64(seed) + 0x9E3779B97F4A7C15*uint64(k+1)
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return int64(z ^ (z >> 31))
}

// PhoneID is the id of the phone this person carries ("" = none).
func (a *Agent) PhoneID() string {
	if a.phone == nil {
		return ""
	}
	return a.phone.id
}

// Carry is how this person carries their phone (CarryChest…; −1 = no phone).
func (a *Agent) Carry() int {
	switch {
	case a.phone == nil:
		return -1
	case a.phone.dev == nil:
		return CarryChest
	}
	return a.phone.dev.Carry()
}

// Events returns (and clears) the phone messages produced since the last call.
func (w *World) Events() []Event {
	ev := w.events
	w.events = nil
	return ev
}

func angDiff(a, b float64) float64 {
	d := math.Mod(a-b+math.Pi, 2*math.Pi)
	if d < 0 {
		d += 2 * math.Pi
	}
	return d - math.Pi
}

// style is how a person moves while standing: weight shifts and music.
type style struct {
	swayAmp  float64 // m/s², gentle swaying during the show (0 = doesn't)
	danceAmp float64 // m/s², lateral sway on "dance"
	bounce   float64 // m/s², vertical bounce on "dance"
	delay    float64 // s behind the beat
	side     float64 // ±1: which way the first sway goes
	amp, bnc float64 // current (faded) sway and bounce
	wsNext   float64 // next weight shift
	wsStart  float64
	wsDur    float64
	wsD      float64 // m
	wsX, wsZ float64 // direction in the body frame
}

func newStyle(r *rand.Rand) style {
	// A fixed number of draws per person, so tuning a share here doesn't
	// reshuffle every later random number of a seeded run.
	u := [7]float64{}
	for i := range u {
		u[i] = r.Float64()
	}
	s := style{delay: 0.3 * u[0], side: 1, danceAmp: clamp(1.0+1.5*u[1], 0.5, 2.5), wsNext: 16 * u[2]}
	if u[3] < 0.5 {
		s.side = -1
	}
	if u[4] < 0.25 {
		s.swayAmp = 0.1 + 0.3*u[5]
	}
	if u[6] < 0.6 {
		s.bounce = 1 + 3*u[6]/0.6
	} else {
		s.bounce = 0.2 + 0.6*(u[6]-0.6)/0.4
	}
	return s
}

// bodyMotion is what the body does on top of the disc's acceleration, in
// the body frame (x right, y up, z forward; m/s²), plus rotation (°/s).
func (w *World) bodyMotion(a *Agent, speed float64, r *rand.Rand) (bx, by, bz, rot float64) {
	st := &a.style
	idle := a.stand || speed < 0.1
	// Music: fade toward this person's sway and bounce.
	var ta, tb float64
	if idle && w.music() {
		ta = st.swayAmp
		if w.Action == ActDance {
			ta, tb = math.Max(st.danceAmp, st.swayAmp), st.bounce
		}
	}
	k := Dt / 1.5
	st.amp += (ta - st.amp) * k
	st.bnc += (tb - st.bnc) * k
	if st.amp > 1e-3 || st.bnc > 1e-3 {
		t := w.T - st.delay
		ph := 2 * math.Pi * w.BeatHz / 2 * t // sway: one side per beat
		bx += st.side * st.amp * math.Sin(ph)
		bz += 0.15 * st.amp * math.Sin(ph+0.5)
		by += st.bnc * math.Sin(2*math.Pi*w.BeatHz*t)
		rot += 12 * st.amp * math.Abs(math.Cos(ph))
	}
	// Weight shifts while standing.
	if idle && w.T >= st.wsNext && w.T >= st.wsStart+st.wsDur {
		st.wsStart, st.wsDur = w.T, 0.8+0.8*r.Float64()
		st.wsD = 0.02 + 0.03*r.Float64()
		th := 0.3 * r.NormFloat64()
		if r.Float64() < 0.5 {
			th += math.Pi
		}
		st.wsX, st.wsZ = math.Cos(th), math.Sin(th)
		st.wsNext = w.T + 4 + 12*r.Float64()
	}
	if t := w.T - st.wsStart; st.wsDur > 0 && t >= 0 && t < st.wsDur {
		// d(t) = D·(1 − cos(πt/T))/2  →  a(t) = D·(π/T)²/2 · cos(πt/T)
		acc := st.wsD * (math.Pi / st.wsDur) * (math.Pi / st.wsDur) / 2 * math.Cos(math.Pi*t/st.wsDur)
		bx += acc * st.wsX
		bz += acc * st.wsZ
	}
	return
}

// phones advances every phone by one tick and queues its messages.
func (w *World) phones() {
	if w.envRng != nil {
		w.env.Common.Step(Dt, w.realism.GPS, w.envRng)
	}
	w.env.StartMs = w.StartMs
	for _, a := range w.agents {
		p := a.phone
		if p == nil {
			continue
		}
		if !p.hello && p.dev == nil {
			p.hello = true
			p.sx, p.sy = r2c(a.X), r2c(a.Y)
			w.events = append(w.events, Event{Kind: EvHello, ID: p.id, X: a.X, Y: a.Y},
				Event{Kind: EvSync, ID: p.id, Offset: p.offset, RTT: p.rtt})
		}
		speed := math.Hypot(a.VX, a.VY)
		// The phone's frame is the body's facing.
		turn := angDiff(a.face, a.prevFace)
		fx, fy := math.Cos(a.face), math.Sin(a.face)
		rx, ry := -fy, fx // right of the facing direction (y down)
		bx := a.AX*rx + a.AY*ry
		bz := a.AX*fx + a.AY*fy
		by := 0.0
		// Gait.
		g := math.Max(0, math.Min(1, (speed-0.15)/0.4))
		if g > 0 {
			fs := math.Max(1.6, math.Min(2.2, 1.6+0.5*(speed-0.6)))
			p.step += 2 * math.Pi * fs * Dt
			v := math.Min(speed, 1.8)
			by += g * 1.4 * v * math.Sin(p.step)
			bx += g * 0.3 * v * math.Sin(p.step/2)
		}
		mx, my, mz, mrot := w.bodyMotion(a, speed, p.rng)
		bx += mx
		by += my
		bz += mz
		// Breathing, sensor noise.
		br := 0.02 * math.Sin(2*math.Pi*p.breathF*w.T+p.breathP)
		bz += br
		by += 0.5 * br
		bx += noiseSD * p.rng.NormFloat64()
		by += noiseSD * p.rng.NormFloat64()
		bz += noiseSD * p.rng.NormFloat64()
		rot := math.Abs(turn)/Dt*180/math.Pi + mrot + math.Abs(3*p.rng.NormFloat64())
		// Handling.
		if w.T >= p.handleEnd && p.rng.Float64() < handleRate*Dt {
			p.handleEnd = w.T + 2 + 2*p.rng.Float64()
			p.handleRot = 250 + 250*p.rng.Float64()
		}
		if w.T < p.handleEnd {
			rot = p.handleRot * (0.7 + 0.3*p.rng.Float64())
			bx += 0.6 * p.rng.NormFloat64()
			by += 0.6 * p.rng.NormFloat64()
			bz += 0.6 * p.rng.NormFloat64()
		}
		if p.dev != nil {
			// A messy phone: the device decides what is sent, and when.
			w.events = p.dev.Tick(Raw{T: w.T, X: a.X, Y: a.Y, BX: bx, BY: by, BZ: bz, Rot: rot, Gait: g, Step: p.step}, w.env, w.events)
			continue
		}
		p.ax += bx
		p.ay += by
		p.az += bz
		p.rot = math.Max(p.rot, rot)
		p.n++
		if p.n >= summaryTicks {
			n := float64(p.n)
			w.events = append(w.events, Event{Kind: EvMotion, ID: p.id, M: protocol.Motion{
				Type: protocol.TypeMotion, T: w.StartMs + int64(math.Round(w.T*1000)),
				AX: r3(p.ax / n), AY: r3(p.ay / n), AZ: r3(p.az / n), Rot: math.Round(p.rot*10) / 10}})
			p.n, p.ax, p.ay, p.az, p.rot = 0, 0, 0, 0, 0
		}
		if p.ticks++; p.ticks >= PosEveryTicks {
			p.ticks = 0
			if x, y := r2c(a.X), r2c(a.Y); x != p.sx || y != p.sy {
				p.sx, p.sy = x, y
				w.events = append(w.events, Event{Kind: EvPos, ID: p.id, X: x, Y: y})
			}
		}
	}
}

func r3(v float64) float64  { return math.Round(v*1000) / 1000 }
func r2c(v float64) float64 { return math.Round(v*100) / 100 }
