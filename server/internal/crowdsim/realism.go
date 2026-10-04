package crowdsim

import (
	"fmt"
	"math"
	"math/rand"

	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// Messy phones. The ideal sim phone (phone.go) is upright on the chest,
// knows its position to the centimetre, never drops a message and is
// perfectly synced. Real phones are none of that. Realism switches three
// independent imperfections on, each with a strength (0 = off, 1 = the
// "realistic" preset, 2 = "harsh"; up to MaxRealism), so the effect of each
// can be measured alone (cmd/eval).
//
// # 1. GPS (Realism.GPS)
//
// The phone reports a GPS-like fix about once a second instead of its true
// position (Event EvGPS: venue metres + an accuracy radius, which the
// server smooths and gates exactly like a live fix: internal/geo.Smoother,
// gpsMaxAcc). As with a live GPS phone, its hello carries no position: the
// server puts it on the default grid cell until the first usable fix, and a
// phone whose fixes are all too inaccurate stays there. The error of one
// phone is
//
//   - a slow first-order Gauss–Markov (Ornstein–Uhlenbeck) bias per axis,
//     correlation time 30–120 s, the standard receiver-error model (Rankin
//     1994, "GPS and differential GPS: an error model for sensor
//     simulation", IEEE PLANS; Brown & Hwang, "Introduction to Random
//     Signals and Applied Kalman Filtering", ch. on GPS error models):
//     multipath and atmospheric errors are correlated over tens of seconds
//     to minutes, so averaging a few fixes doesn't remove them;
//   - its size differs per phone: median horizontal error drawn log-normal
//     around 5 m × strength (σ_ln 0.5, clamped to 2–20 m × strength).
//     Sources: GPS.gov / van Diggelen & Enge 2015 (ION GNSS+): phones are
//     typically within 4.9 m under open sky; Zandbergen & Barbeau 2011 (J.
//     Navigation 64(3)): median 5.0–8.5 m for A-GPS phones outdoors; Merry
//     & Bettinger 2019 (PLoS ONE 14(7)): 7–13 m mean for an iPhone 6 in an
//     urban setting; Zandbergen 2009 (Trans. GIS 13): Wi-Fi positioning
//     (what a phone falls back to indoors) median 74 m. Strength 1 is
//     therefore an optimistic outdoor crowd; strength 2 (10 m median) is a
//     street between buildings or under a stadium roof;
//   - white noise per fix (20 % of the phone's σ);
//   - a venue-wide common error (σ 1 m × strength per axis, correlation
//     time 120 s): the part of the atmospheric error every phone shares;
//   - jumps: about once per 3 minutes per phone (÷ strength) the bias is
//     kicked 2–5 σ in a random direction (a reflected signal taking over)
//     and decays back with the correlation time;
//   - a reported accuracy that is roughly right: the 68 % radius of the
//     phone's own error (1.51 σ per axis), times a per-phone log-normal
//     factor (σ_ln 0.3), never below 3 m. It does not grow during a jump.
//
// # 2. Carry (Realism.Carry)
//
// Where the phone is. The share still upright on the chest (the ideal
// assumption) is 0.25^strength; the rest split hand : pocket : bag =
// 25 : 35 : 15. At strength 1: chest 25 %, hand 25 %, pocket 35 %, bag
// 15 %. (Cui, Chipchase & Ichikawa 2007, "A cross culture study on phone
// carrying and physical personalization": about 60 % of men carry the
// phone in a trouser pocket, about 60 % of women in a bag; Wiese, Saponas
// & Brush 2013, "Phoneprioception", CHI. Skewed toward the hand here
// because a web page only streams while it is open, and people who just
// opened it are holding the phone.)
//
//   - chest: upright, a few degrees off (tilt ≤ 10°, yaw σ 10°);
//   - hand, looking at it: tilted back 30–70°, yaw σ 20°; the arm passes
//     85 % of the body's motion below ~3 Hz, plus tremor (σ 0.12 m/s²) and
//     a gesture every ~10 s (0.3–1.2 s at 80–300 °/s: some pass the
//     server's 200 °/s handling threshold, some don't);
//   - trouser pocket: flat on the thigh, top up or down, screen in or out,
//     sometimes sideways, tilted 10–20°, turned about the vertical: σ 35°
//     for a front pocket (60 %), any angle for a back, side or jacket
//     pocket (40 %);
//     while walking the thigh swings ±14–26° at the stride frequency: the
//     phone's orientation (and gravity vector) swings with it, it reads the
//     swing's rotation rate (80–150 °/s) and tangential acceleration
//     (~2 m/s² fore-aft), and heel strikes come through 30 % harder;
//   - bag: any orientation; horizontal motion low-passed (two poles at
//     1.5 Hz, gain 0.8) and a pendulum swing while walking.
//
// The device-frame acceleration is the body-frame motion (phone.go)
// rotated by the carry orientation, so a pocket phone's x is no longer
// left/right. Every summary carries g, the unit gravity vector in the
// device frame (protocol.Motion.G), as a phone would estimate it: the true
// direction plus the low-passed linear acceleration leaking in (τ 1 s),
// 0.5 s behind, plus noise (σ 0.01), to 2 decimals. People change how they
// carry the phone about once per 3 minutes (÷ strength): a 1.5–3 s burst
// at 150–500 °/s with messy acceleration, then the new orientation.
//
// # 3. Dropouts (Realism.Dropout)
//
//   - Screen lock / page in the background: each phone goes silent about
//     once per 4 minutes (÷ strength), for a log-normal time (median 15 s,
//     σ_ln 1.0, 2 s–5 min); about 10 % of phones are silent at any moment
//     at strength 1. Silent for more than 8 s: the socket closes after
//     2–8 s (EvGone) and the phone says hello again when it returns.
//   - Stalls: about once per 45 s (÷ strength) the connection stalls for
//     100 ms–2 s (log-uniform); everything sent meanwhile arrives in one
//     clump at the end. Nothing is reordered (it is TCP).
//   - Lost summaries: 1 % × strength of the 100 ms summaries are never
//     sent (a throttled timer).
//   - Latency: every message arrives rtt/2 late; rtt = 30 ms + Exp(60 ms ×
//     strength) per phone (a loaded cell).
//   - Clock: the sync error left after the NTP-style exchange, σ 12 ms ×
//     strength, capped at rtt/2, redrawn at every 30 s re-sync, plus
//     per-message timestamp jitter σ 4 ms × strength.
//   - Low sensor rate: 10 % × strength of the phones (at most 40 %) get
//     motion events at 10 Hz instead of 50–60 Hz, so a summary is one raw
//     sample; half of those also send only every 200 ms.
//
// With every strength at 0 nothing in this file runs and the phones are
// exactly the ideal ones (same random numbers, same messages).

// Realism is the strength of each imperfection: 0 = off, 1 = realistic,
// 2 = harsh.
type Realism struct {
	GPS     float64 `json:"gps"`
	Carry   float64 `json:"carry"`
	Dropout float64 `json:"dropout"`
	// ForceCarry, for measuring one carry state alone: 0 = the mix above,
	// CarryChest+1 … CarryBag+1 = every phone carried that way (still tilted
	// and handled as that state is). Needs Carry > 0.
	ForceCarry int `json:"forceCarry,omitempty"`
	// NoGravity: carried phones don't send their gravity vector g (an old
	// phone page), so the server can't level them. For measuring what
	// levelling buys.
	NoGravity bool `json:"noGravity,omitempty"`
}

// MaxRealism is the largest strength accepted.
const MaxRealism = 3.0

// Realism presets.
const (
	RealismIdeal     = "ideal"
	RealismRealistic = "realistic"
	RealismHarsh     = "harsh"
)

// RealismPresets lists the preset names.
var RealismPresets = []string{RealismIdeal, RealismRealistic, RealismHarsh}

// RealismPreset returns a preset by name ("" = ideal).
func RealismPreset(name string) (Realism, error) {
	switch name {
	case "", RealismIdeal:
		return Realism{}, nil
	case RealismRealistic:
		return Realism{GPS: 1, Carry: 1, Dropout: 1}, nil
	case RealismHarsh:
		return Realism{GPS: 2, Carry: 2, Dropout: 2}, nil
	}
	return Realism{}, fmt.Errorf("unknown realism %q (want ideal, realistic or harsh)", name)
}

// Ideal reports whether every imperfection is off.
func (r Realism) Ideal() bool { return r.GPS == 0 && r.Carry == 0 && r.Dropout == 0 }

// Validate checks the strengths.
func (r Realism) Validate() error {
	for _, v := range []float64{r.GPS, r.Carry, r.Dropout} {
		if math.IsNaN(v) || v < 0 || v > MaxRealism {
			return fmt.Errorf("realism strengths must be 0–%g", MaxRealism)
		}
	}
	if r.ForceCarry < 0 || r.ForceCarry > CarryBag+1 {
		return fmt.Errorf("forceCarry must be 0–%d", CarryBag+1)
	}
	return nil
}

// Carry states.
const (
	CarryChest = iota
	CarryHand
	CarryPocket
	CarryBag
)

// CarryNames names the carry states.
var CarryNames = []string{"chest", "hand", "pocket", "bag"}

// GPS model parameters at strength 1 (see the comment above).
const (
	gpsMedianErr  = 5.0  // m, median horizontal error of the median phone
	gpsSpreadLn   = 0.5  // log-normal spread between phones
	gpsMinErr     = 2.0  // m
	gpsMaxErr     = 20.0 // m
	gpsTauMin     = 30.0 // s
	gpsTauMax     = 120.0
	gpsWhite      = 0.2 // of σ
	gpsJumpEvery  = 180.0
	gpsAccFloor   = 3.0
	gpsAccSpread  = 0.3
	gpsCommonSD   = 1.0 // m per axis
	gpsCommonTau  = 120.0
	gpsFixEvery   = 1.0 // s
	medianToSigma = 1.1774
	r68ToSigma    = 1.5096
)

// Dropout model parameters at strength 1.
const (
	silentEvery    = 240.0 // s between silences per phone
	silentMedian   = 15.0  // s
	silentSpreadLn = 1.0
	silentMin      = 2.0
	silentMax      = 300.0
	silentCloseS   = 8.0  // silences longer than this close the socket
	stallEvery     = 45.0 // s
	stallMin       = 0.1
	stallMax       = 2.0
	lossRate       = 0.01
	clockErrMs     = 12.0
	clockJitterMs  = 4.0
	resyncS        = 30.0
	lowRateShare   = 0.10
)

// Carry model parameters at strength 1.
const (
	carryChangeEvery = 180.0 // s
	gestureEvery     = 10.0  // s, phone in hand
)

// GPSCommon is the GPS error every phone in the venue shares.
type GPSCommon struct{ X, Y float64 }

// Step advances the common error by dt seconds at the given strength.
func (c *GPSCommon) Step(dt, strength float64, r *rand.Rand) {
	if strength <= 0 {
		return
	}
	sd := gpsCommonSD * strength
	k := math.Sqrt(2 * dt / gpsCommonTau)
	c.X += -c.X*dt/gpsCommonTau + sd*k*r.NormFloat64()
	c.Y += -c.Y*dt/gpsCommonTau + sd*k*r.NormFloat64()
}

// Raw is one 20 ms tick of what a phone's owner does: where they truly
// are and their body-frame motion (x right, y up, z forward; m/s²; gravity
// removed) with the rotation rate (°/s), as phone.go computes them.
type Raw struct {
	T          float64 // s since start
	X, Y       float64 // true position, venue metres
	BX, BY, BZ float64
	Rot        float64
	Gait       float64 // 0 standing … 1 walking
	Step       float64 // gait phase (rad), advancing 2π per step
}

// Env is what the device needs to know about the run.
type Env struct {
	StartMs int64 // server clock at T = 0
	Common  GPSCommon
}

type queued struct {
	at float64
	ev Event
}

type m3 [9]float64

func (m m3) mul(n m3) m3 {
	var o m3
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			o[3*i+j] = m[3*i]*n[j] + m[3*i+1]*n[3+j] + m[3*i+2]*n[6+j]
		}
	}
	return o
}

func (m m3) t() m3 { return m3{m[0], m[3], m[6], m[1], m[4], m[7], m[2], m[5], m[8]} }

func (m m3) apply(x, y, z float64) (float64, float64, float64) {
	return m[0]*x + m[1]*y + m[2]*z, m[3]*x + m[4]*y + m[5]*z, m[6]*x + m[7]*y + m[8]*z
}

var ident = m3{1, 0, 0, 0, 1, 0, 0, 0, 1}

func rotX(a float64) m3 {
	c, s := math.Cos(a), math.Sin(a)
	return m3{1, 0, 0, 0, c, -s, 0, s, c}
}

func rotY(a float64) m3 {
	c, s := math.Cos(a), math.Sin(a)
	return m3{c, 0, s, 0, 1, 0, -s, 0, c}
}

func rotZ(a float64) m3 {
	c, s := math.Cos(a), math.Sin(a)
	return m3{c, -s, 0, s, c, 0, 0, 0, 1}
}

// randRot is a uniformly random rotation (from a random unit quaternion).
func randRot(r *rand.Rand) m3 {
	var q [4]float64
	n := 0.0
	for n < 1e-6 {
		n = 0
		for i := range q {
			q[i] = r.NormFloat64()
			n += q[i] * q[i]
		}
	}
	n = math.Sqrt(n)
	w, x, y, z := q[0]/n, q[1]/n, q[2]/n, q[3]/n
	return m3{
		1 - 2*(y*y+z*z), 2 * (x*y - z*w), 2 * (x*z + y*w),
		2 * (x*y + z*w), 1 - 2*(x*x+z*z), 2 * (y*z - x*w),
		2 * (x*z - y*w), 2 * (y*z + x*w), 1 - 2*(x*x+y*y),
	}
}

const deg = math.Pi / 180

// Device is one messy phone: it turns its owner's true position and body
// motion into the messages a real phone in that pocket would send.
type Device struct {
	ID  string
	rl  Realism
	rng *rand.Rand

	hello       bool
	offset, rtt int64

	// 100 ms summary window
	n          int
	ax, ay, az float64
	rot        float64
	tick       int
	lowRate    int // 0 = no, 1 = 10 Hz samples, 2 = 10 Hz samples and 5 Hz summaries
	win        int // ticks per summary

	// position
	posTick int
	sx, sy  float64
	sent    bool

	// GPS
	sigma, tau float64
	ex, ey     float64
	accFactor  float64
	nextFix    float64

	// carry
	carry      int
	q0         m3 // device attitude in the body frame (columns = device axes)
	fix        m3 // pocket: the fixed part (which way round it went in)
	yaw, tilt  float64
	swingA     float64 // rad
	swingP     float64
	prevStep   float64
	lp1, lp2   [3]float64 // hand / bag low-pass state (body frame)
	glp        [3]float64 // linear acceleration leaking into the gravity estimate
	gEst       [3]float64
	gOK        bool
	gestEnd    float64
	gestRot    float64
	burstEnd   float64
	burstMid   float64
	burstRot   float64
	nextCarry  int
	bagSwing   float64
	lastT      float64
	started    bool
	clockBias  float64
	nextResync float64

	// dropouts
	silentUntil float64
	goneAt      float64 // > 0: the socket closes then
	wasSilent   bool
	stallUntil  float64
	q           []queued
	lastRel     float64
}

// NewDevice creates a phone with the given imperfections; seed fixes
// everything random about it.
func NewDevice(id string, rl Realism, seed int64) *Device {
	r := rand.New(rand.NewSource(seed))
	d := &Device{ID: id, rl: rl, rng: r, win: summaryTicks, q0: ident,
		offset: int64(r.NormFloat64() * 800), rtt: int64(25 + r.ExpFloat64()*40)}
	if rl.Dropout > 0 {
		d.rtt = int64(30 + r.ExpFloat64()*60*rl.Dropout)
		if u := r.Float64(); u < math.Min(0.4, lowRateShare*rl.Dropout) {
			d.lowRate = 1
			if r.Float64() < 0.5 {
				d.lowRate, d.win = 2, 2*summaryTicks
			}
		}
		d.resync()
	}
	if rl.GPS > 0 {
		m := gpsMedianErr * math.Exp(gpsSpreadLn*r.NormFloat64())
		m = clamp(m, gpsMinErr, gpsMaxErr) * rl.GPS
		d.sigma = m / medianToSigma
		d.tau = gpsTauMin + (gpsTauMax-gpsTauMin)*r.Float64()
		d.ex, d.ey = d.sigma*r.NormFloat64(), d.sigma*r.NormFloat64()
		d.accFactor = math.Exp(gpsAccSpread * r.NormFloat64())
		d.nextFix = gpsFixEvery * r.Float64()
	}
	if rl.Carry > 0 {
		d.setCarry(d.drawCarry(-1))
		d.swingA = 0.25 + 0.2*r.Float64()
		d.swingP = 2 * math.Pi * r.Float64()
		d.bagSwing = 0.2 + 0.4*r.Float64()
	}
	return d
}

// Carry is the phone's current carry state (CarryChest…).
func (d *Device) Carry() int { return d.carry }

// HorizSigma is the per-axis σ of the phone's slow GPS error (m); 0 with GPS off.
func (d *Device) HorizSigma() float64 { return d.sigma }

func (d *Device) resync() {
	d.clockBias = clamp(clockErrMs*d.rl.Dropout*d.rng.NormFloat64(), -float64(d.rtt)/2, float64(d.rtt)/2)
}

// drawCarry picks a carry state other than not.
func (d *Device) drawCarry(not int) int {
	if d.rl.ForceCarry > 0 {
		return d.rl.ForceCarry - 1
	}
	chest := math.Pow(0.25, d.rl.Carry)
	for {
		u := d.rng.Float64()
		c := CarryBag
		switch {
		case u < chest:
			c = CarryChest
		case u < chest+(1-chest)*25/75:
			c = CarryHand
		case u < chest+(1-chest)*60/75:
			c = CarryPocket
		}
		if c != not {
			return c
		}
	}
}

// setCarry draws the orientation that goes with a carry state.
func (d *Device) setCarry(c int) {
	r := d.rng
	d.carry = c
	d.lp1, d.lp2 = [3]float64{}, [3]float64{}
	switch c {
	case CarryChest:
		d.q0 = rotY(10 * deg * r.NormFloat64()).mul(rotX(10 * deg * (2*r.Float64() - 1))).mul(rotZ(5 * deg * r.NormFloat64()))
	case CarryHand:
		d.q0 = rotY(20 * deg * r.NormFloat64()).mul(rotX(-(30 + 40*r.Float64()) * deg)).mul(rotZ(10 * deg * r.NormFloat64()))
	case CarryPocket:
		d.fix = ident
		if r.Float64() < 0.5 { // top down
			d.fix = d.fix.mul(rotZ(math.Pi))
		}
		if r.Float64() < 0.5 { // screen toward the leg
			d.fix = d.fix.mul(rotY(math.Pi))
		}
		if r.Float64() < 0.15 { // sideways
			d.fix = d.fix.mul(rotZ(math.Pi / 2))
		}
		d.yaw = 35 * deg * r.NormFloat64()
		if r.Float64() < 0.4 { // back, side or jacket pocket
			d.yaw = 2 * math.Pi * r.Float64()
		}
		d.tilt = (10 + 10*r.Float64()) * deg
		d.q0 = rotY(d.yaw).mul(rotX(d.tilt)).mul(d.fix)
	case CarryBag:
		d.q0 = randRot(r)
	}
}

// send queues a message: it arrives rtt/2 later, after any stall, never
// before an earlier one. With dropouts off it is delivered at once.
func (d *Device) send(t float64, ev Event) {
	at := t
	if d.rl.Dropout > 0 {
		at = math.Max(t+float64(d.rtt)/2000, d.stallUntil)
		at = math.Max(at, d.lastRel)
		d.lastRel = at
	}
	d.q = append(d.q, queued{at, ev})
}

func (d *Device) flush(t float64, out []Event) []Event {
	i := 0
	for ; i < len(d.q) && d.q[i].at <= t+1e-9; i++ {
		out = append(out, d.q[i].ev)
	}
	if i > 0 {
		d.q = append(d.q[:0], d.q[i:]...)
	}
	return out
}

// Tick advances the phone by one 20 ms tick and appends the messages that
// arrive at the server during it to out.
func (d *Device) Tick(in Raw, env Env, out []Event) []Event {
	r := d.rng
	t := in.T
	dt := Dt
	if d.started {
		dt = math.Max(1e-3, t-d.lastT)
	}
	d.lastT, d.started = t, true

	// GPS error drifts whether or not anyone is listening.
	if d.rl.GPS > 0 {
		k := math.Sqrt(2 * dt / d.tau)
		d.ex += -d.ex*dt/d.tau + d.sigma*k*r.NormFloat64()
		d.ey += -d.ey*dt/d.tau + d.sigma*k*r.NormFloat64()
		if r.Float64() < d.rl.GPS*dt/gpsJumpEvery {
			a, m := 2*math.Pi*r.Float64(), d.sigma*(2+3*r.Float64())
			d.ex += m * math.Cos(a)
			d.ey += m * math.Sin(a)
		}
	}

	if dr := d.rl.Dropout; dr > 0 {
		if d.goneAt > 0 && t >= d.goneAt {
			d.goneAt = 0
			d.hello = false
			out = append(out, Event{Kind: EvGone, ID: d.ID}) // the server sees the close itself
		}
		if t >= d.silentUntil && d.hello && r.Float64() < dr*dt/silentEvery {
			dur := clamp(silentMedian*math.Exp(silentSpreadLn*r.NormFloat64()), silentMin, silentMax)
			d.silentUntil = t + dur
			if dur > silentCloseS {
				d.goneAt = t + 2 + 6*r.Float64()
			}
		}
		if t < d.silentUntil {
			d.wasSilent = true
			return d.flush(t, out)
		}
		if d.wasSilent { // back: start a fresh window, fix and sync
			d.wasSilent = false
			d.n, d.ax, d.ay, d.az, d.rot, d.tick = 0, 0, 0, 0, 0, 0
			d.nextFix = t
			d.goneAt = 0
		}
		if t >= d.stallUntil && r.Float64() < dr*dt/stallEvery {
			d.stallUntil = t + stallMin*math.Pow(stallMax/stallMin, r.Float64())
		}
		if t >= d.nextResync {
			d.nextResync = t + resyncS
			d.resync()
			if d.hello {
				d.send(t, Event{Kind: EvSync, ID: d.ID, Offset: d.offset + int64(d.clockBias), RTT: d.rtt})
			}
		}
	}

	x, y := in.X, in.Y
	if d.rl.GPS > 0 {
		x, y = d.fixPos(in, env)
	}
	if !d.hello {
		d.hello = true
		d.sx, d.sy, d.sent = r2c(in.X), r2c(in.Y), false
		d.send(t, Event{Kind: EvHello, ID: d.ID, X: x, Y: y, Auto: d.rl.GPS > 0})
		d.send(t, Event{Kind: EvSync, ID: d.ID, Offset: d.offset + int64(d.clockBias), RTT: d.rtt})
		d.nextFix = t
	}

	ax, ay, az, rot := in.BX, in.BY, in.BZ, in.Rot
	var g []float64
	if d.rl.Carry > 0 {
		ax, ay, az, rot = d.carried(in, dt)
	}

	// Sampling: a slow sensor only sees every fifth tick.
	d.tick++
	if d.lowRate == 0 || d.tick%summaryTicks == 0 {
		d.ax += ax
		d.ay += ay
		d.az += az
		d.n++
	}
	d.rot = math.Max(d.rot, rot)
	if d.tick >= d.win {
		lost := d.rl.Dropout > 0 && r.Float64() < lossRate*d.rl.Dropout
		if d.n > 0 && !lost {
			n := float64(d.n)
			ts := env.StartMs + int64(math.Round(t*1000))
			if d.rl.Dropout > 0 {
				ts += int64(math.Round(d.clockBias + clockJitterMs*d.rl.Dropout*r.NormFloat64()))
			}
			if d.rl.Carry > 0 && !d.rl.NoGravity {
				g = d.gravity()
			}
			d.send(t, Event{Kind: EvMotion, ID: d.ID, M: protocol.Motion{Type: protocol.TypeMotion, T: ts,
				AX: r3(d.ax / n), AY: r3(d.ay / n), AZ: r3(d.az / n), Rot: math.Round(d.rot*10) / 10, G: g}})
		}
		d.n, d.ax, d.ay, d.az, d.rot, d.tick = 0, 0, 0, 0, 0, 0
	}

	// Position: a GPS fix about once a second, else the true position at
	// 10 Hz whenever it changed (as the ideal phone).
	if d.rl.GPS > 0 {
		if t >= d.nextFix {
			d.nextFix = t + gpsFixEvery*(0.9+0.2*r.Float64())
			acc := math.Max(gpsAccFloor, r68ToSigma*d.sigma*d.accFactor)
			d.send(t, Event{Kind: EvGPS, ID: d.ID, X: r2c(x), Y: r2c(y), Acc: math.Round(acc*10) / 10})
		}
	} else if d.posTick++; d.posTick >= PosEveryTicks {
		d.posTick = 0
		if px, py := r2c(in.X), r2c(in.Y); px != d.sx || py != d.sy {
			d.sx, d.sy = px, py
			d.send(t, Event{Kind: EvPos, ID: d.ID, X: px, Y: py})
		}
	}
	return d.flush(t, out)
}

// fixPos is where a GPS fix would put the phone now.
func (d *Device) fixPos(in Raw, env Env) (float64, float64) {
	w := gpsWhite * d.sigma
	return in.X + d.ex + env.Common.X + w*d.rng.NormFloat64(), in.Y + d.ey + env.Common.Y + w*d.rng.NormFloat64()
}

func lowpass(s *[3]float64, x, y, z, a float64) (float64, float64, float64) {
	s[0] += (x - s[0]) * a
	s[1] += (y - s[1]) * a
	s[2] += (z - s[2]) * a
	return s[0], s[1], s[2]
}

// carried turns the body-frame motion into what the phone reads where it
// is carried: device-frame acceleration and rotation rate.
func (d *Device) carried(in Raw, dt float64) (ax, ay, az, rot float64) {
	r := d.rng
	t := in.T
	bx, by, bz, rot := in.BX, in.BY, in.BZ, in.Rot
	q := d.q0
	stepRate := 0.0 // rad/s of gait phase
	if in.Gait > 0 {
		stepRate = (in.Step - d.prevStep) / dt
		if stepRate < 0 || stepRate > 40 {
			stepRate = 0
		}
	}
	d.prevStep = in.Step
	switch d.carry {
	case CarryHand:
		a := 1 - math.Exp(-2*math.Pi*3*dt)
		bx, by, bz = lowpass(&d.lp1, bx, by, bz, a)
		bx, by, bz = 0.85*bx+0.12*r.NormFloat64(), 0.85*by+0.12*r.NormFloat64(), 0.85*bz+0.12*r.NormFloat64()
		rot += math.Abs(8 * r.NormFloat64())
		if t >= d.gestEnd && r.Float64() < dt/gestureEvery {
			d.gestEnd = t + 0.3 + 0.9*r.Float64()
			d.gestRot = 80 + 220*r.Float64()
		}
		if t < d.gestEnd {
			rot = math.Max(rot, d.gestRot*(0.6+0.4*r.Float64()))
			bx += 0.8 * r.NormFloat64()
			by += 0.8 * r.NormFloat64()
			bz += 0.8 * r.NormFloat64()
		}
	case CarryPocket:
		if in.Gait > 0 && stepRate > 0 {
			// The thigh swings at the stride frequency (half the step
			// frequency): θ = A sin(φ/2).
			w := stepRate / 2
			ph := in.Step/2 + d.swingP
			A := d.swingA * in.Gait
			th := A * math.Sin(ph)
			bz += -0.2 * A * w * w * math.Sin(ph) // tangential, 0.2 m below the hip
			by *= 1.3                             // heel strikes
			rot = math.Max(rot, math.Abs(A*w*math.Cos(ph))/deg)
			q = rotY(d.yaw).mul(rotX(d.tilt + th)).mul(d.fix)
		}
		bx += 0.05 * r.NormFloat64()
		bz += 0.05 * r.NormFloat64()
	case CarryBag:
		a := 1 - math.Exp(-2*math.Pi*1.5*dt)
		hx, _, hz := lowpass(&d.lp1, bx, 0, bz, a)
		hx, _, hz = lowpass(&d.lp2, hx, 0, hz, a)
		bx, bz = 0.8*hx, 0.8*hz
		if in.Gait > 0 {
			bx += d.bagSwing * in.Gait * math.Sin(in.Step/2+d.swingP)
			rot += (20 + 40*d.bagSwing) * in.Gait * math.Abs(math.Cos(in.Step/2+d.swingP))
		}
	}
	// Changing how the phone is carried: a handling burst, then the new
	// orientation.
	if t >= d.burstEnd && d.burstMid == 0 && r.Float64() < d.rl.Carry*dt/carryChangeEvery {
		dur := 1.5 + 1.5*r.Float64()
		d.burstMid, d.burstEnd = t+dur/2, t+dur
		d.burstRot = 150 + 350*r.Float64()
		d.nextCarry = d.drawCarry(d.carry)
	}
	if d.burstMid > 0 {
		if t >= d.burstMid && d.nextCarry >= 0 {
			d.setCarry(d.nextCarry)
			d.nextCarry = -1
			q = d.q0
		}
		if t < d.burstEnd {
			rot = math.Max(rot, d.burstRot*(0.7+0.3*r.Float64()))
			bx += 1.5 * r.NormFloat64()
			by += 1.5 * r.NormFloat64()
			bz += 1.5 * r.NormFloat64()
		} else {
			d.burstMid = 0
		}
	}
	// Into the device frame: device = Qᵀ · body.
	qt := q.t()
	ax, ay, az = qt.apply(bx, by, bz)
	// Gravity as the phone would estimate it: down (body −y) in the device
	// frame, plus the slow part of the linear acceleration, lagging 0.5 s.
	gx, gy, gz := qt.apply(0, -1, 0)
	lx, ly, lz := lowpass(&d.glp, ax, ay, az, 1-math.Exp(-dt/1.0))
	tx, ty, tz := gx+lx/9.81, gy+ly/9.81, gz+lz/9.81
	if !d.gOK {
		d.gEst, d.gOK = [3]float64{tx, ty, tz}, true
	} else {
		lowpass(&d.gEst, tx, ty, tz, 1-math.Exp(-dt/0.5))
	}
	return
}

// gravity is the unit gravity vector the phone reports (2 decimals).
func (d *Device) gravity() []float64 {
	g := [3]float64{}
	n := 0.0
	for i := range g {
		g[i] = d.gEst[i] + 0.01*d.rng.NormFloat64()
		n += g[i] * g[i]
	}
	n = math.Sqrt(n)
	if n < 1e-6 {
		return []float64{0, -1, 0}
	}
	return []float64{math.Round(g[0]/n*100) / 100, math.Round(g[1]/n*100) / 100, math.Round(g[2]/n*100) / 100}
}
