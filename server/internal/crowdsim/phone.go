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
// into that frame. The facing follows the intended walking direction when
// the person walks (turning at most 120°/s) and otherwise slowly turns toward
// where they are trying to go (the stage, the attraction, the exit). On top
// of that: gait while walking (vertical bounce at the step frequency,
// 1.6–2.2 Hz rising with speed; a smaller lateral sway at half of it),
// breathing (0.2–0.33 Hz, ±0.02 m/s²), sensor noise (σ 0.04 m/s² per raw
// sample), and a rotation rate from turning plus a few °/s of tremor, with
// rare handling episodes (about one per 5 minutes per phone, 2–4 s at
// 250–500 °/s, when the owner looks at the screen).
//
// Raw samples are taken at 50 Hz (each world tick) and summarised every
// 100 ms like the phone page: mean acceleration per axis, max rotation
// rate. T is the server clock (the phones are already synced).

// Event kinds: what a phone would have sent, for the app to feed through
// its normal phone path.
type EventKind int

const (
	EvHello EventKind = iota
	EvSync
	EvPos
	EvMotion
	EvGone
)

// Event is one phone message.
type Event struct {
	Kind        EventKind
	ID          string
	X, Y        float64         // hello, pos
	Offset, RTT int64           // sync
	M           protocol.Motion // motion
}

const (
	summaryTicks = 5    // 100 ms at 50 Hz
	posMinMove   = 0.15 // m
	posMinGap    = 0.5  // s (at most 2 Hz)
	noiseSD      = 0.04 // m/s² per raw sample
	handleRate   = 1.0 / 300
)

type phone struct {
	id      string
	rng     *rand.Rand
	facing  float64 // rad, venue frame (0 = +x, y down)
	step    float64 // gait phase
	breathF float64
	breathP float64
	sx, sy  float64 // last sent position
	sentAt  float64
	hello   bool
	// current 100 ms window
	n          int
	ax, ay, az float64
	rot        float64
	handleEnd  float64
	handleRot  float64
	offset     int64
	rtt        int64
}

func newPhone(w *World, a *Agent) *phone {
	r := rand.New(rand.NewSource(w.rng.Int63()))
	return &phone{
		id: fmt.Sprintf("sim-%04d", a.ID), rng: r,
		facing:  -math.Pi/2 + 0.6*(r.Float64()-0.5), // roughly toward the stage
		breathF: 0.2 + 0.13*r.Float64(), breathP: 2 * math.Pi * r.Float64(),
		offset: int64(r.NormFloat64() * 800), rtt: int64(25 + r.ExpFloat64()*40),
	}
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

// phones advances every phone by one tick and queues its messages.
func (w *World) phones() {
	for _, a := range w.agents {
		p := a.phone
		if p == nil {
			continue
		}
		if !p.hello {
			p.hello = true
			p.sx, p.sy, p.sentAt = a.X, a.Y, w.T
			w.events = append(w.events, Event{Kind: EvHello, ID: p.id, X: a.X, Y: a.Y},
				Event{Kind: EvSync, ID: p.id, Offset: p.offset, RTT: p.rtt})
		}
		speed := math.Hypot(a.VX, a.VY)
		// Facing.
		target, rate := p.facing, 0.0
		switch {
		case math.Hypot(a.dvx, a.dvy) > 0.3 && speed > 0.3:
			// Walking: face where you are going (not where a shove sends you).
			target, rate = math.Atan2(a.dvy, a.dvx), 1/0.5
		case math.Hypot(a.gx-a.X, a.gy-a.Y) > 0.3:
			target, rate = math.Atan2(a.gy-a.Y, a.gx-a.X), 1/3.0
		}
		turn := angDiff(target, p.facing) * math.Min(1, rate*Dt)
		maxTurn := 120 * math.Pi / 180 * Dt
		turn = math.Max(-maxTurn, math.Min(maxTurn, turn))
		p.facing += turn
		fx, fy := math.Cos(p.facing), math.Sin(p.facing)
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
		// Breathing and heartbeat, sensor noise.
		br := 0.02 * math.Sin(2*math.Pi*p.breathF*w.T+p.breathP)
		bz += br
		by += 0.5 * br
		bx += noiseSD * p.rng.NormFloat64()
		by += noiseSD * p.rng.NormFloat64()
		bz += noiseSD * p.rng.NormFloat64()
		rot := math.Abs(turn)/Dt*180/math.Pi + math.Abs(3*p.rng.NormFloat64())
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
		if math.Hypot(a.X-p.sx, a.Y-p.sy) >= posMinMove && w.T-p.sentAt >= posMinGap {
			p.sx, p.sy, p.sentAt = a.X, a.Y, w.T
			w.events = append(w.events, Event{Kind: EvPos, ID: p.id, X: a.X, Y: a.Y})
		}
	}
}

func r3(v float64) float64 { return math.Round(v*1000) / 1000 }
