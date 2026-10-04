// Package crowdsim is a pedestrian crowd simulation for testing Pulse
// against bodies instead of scripted signals: a Social Force Model in which
// a crowd crush can emerge, and the phones some of those people carry.
//
// Physics follows
//
//   - D. Helbing and P. Molnár, "Social force model for pedestrian
//     dynamics", Phys. Rev. E 51, 4282 (1995): each person is driven toward
//     a goal with relaxation time τ, m dv/dt = m (v0·e − v)/τ + Σ f_ij + Σ f_iW;
//   - D. Helbing, I. Farkas and T. Vicsek, "Simulating dynamical features
//     of escape panic", Nature 407, 487–490 (2000): bodies as discs with
//     social repulsion A·exp((r_ij − d_ij)/B), body compression k·g(r_ij − d_ij)
//     and sliding friction κ·g(r_ij − d_ij)·Δv_t, the same terms against
//     walls, and impatience (desired speed grows when people are held up).
//     That paper also gives the injury level used for ground truth: people
//     are hurt when the radial compression reaches 1600 N/m.
//
// Parameters are the papers' (A = 2000 N, B = 0.08 m, k = 1.2·10⁵ kg/s²,
// κ = 2.4·10⁵ kg/(m·s), τ = 0.5 s), with bodies of radius 0.20–0.26 m,
// 55–95 kg, desired speed N(1.3, 0.25) m/s clamped to 0.6–2.0.
//
// Integration is semi-implicit Euler. The world ticks at 50 Hz (behaviour,
// phones, ground truth); each tick is SubSteps physics steps of 5 ms,
// because body contact with k = 1.2·10⁵ kg/s² rings at up to ~20 Hz in a
// packed crowd and a single 20 ms step is unstable there. Speeds are
// clamped to MaxSpeed and accelerations to MaxAccel so a bad overlap can
// never explode.
//
// Walking people also keep a time gap (TimeGap = 0.6 s) to whoever is in
// their path, and step around a slower walker when that gets them on faster
// (headings 0, ±10°, ±20°): speed adaptation as in Tordeux, Chraibi &
// Seyfried's collision-free speed model (2016) and the steering heuristic
// of Moussaïd, Helbing & Theraulaz (2011). People pressing toward the stage
// (stage, surge) ignore the time gap: pushing on while blocked is what
// builds crush pressure.
//
// Calibration. Mean walking speed against density in a 12 × 2.5 m periodic
// corridor, compared with Weidmann's fundamental diagram (1993;
// v0 = 1.34 m/s, γ = 1.913 m⁻², ρmax = 5.4 /m²), 3 seeds × 20 s each
// (PULSE_WEIDMANN=1 go test ./server/internal/crowdsim -run WeidmannSweep -v):
//
//	ρ /m²    0.5   1.0   1.5   2.0   2.5   3.0   3.5   4.0   4.5   5.0
//	sim m/s  1.15  1.08  0.90  0.59  0.31  0.28  0.19  0.08  0.02  0.00
//	Weidmann 1.30  1.06  0.81  0.61  0.45  0.33  0.23  0.16  0.09  0.04
//
// Within ±0.10 m/s from 1 to 5 /m² except 2.5 /m² (−0.14). Free walking
// at 0.5 /m² is 0.15 m/s slow, because desired speeds average 1.3 m/s (not
// Weidmann's 1.34) and the wander force costs a little. Jam density is
// reached slightly early (≤ 0.02 m/s at 4.5 /m², Weidmann 0.09). TestWeidmann
// checks 0.5, 2 and 5 /m² on every run. Only this one benchmark was checked:
// unidirectional flow in a corridor. Bottlenecks, counterflow and
// evacuation times were not. Without the time gap, the Helbing 2000 forces
// alone (B = 0.08 m) let a corridor flow at ~1.3 m/s up to 4 /m².
//
// Vadere (TU Munich), JuPedSim (Forschungszentrum Jülich) and NetLogo are
// reference tools for this model family. Pulse implements the Social Force
// Model natively in Go because it needs per-person phone accelerometer
// signals and live steering from the dashboard, which those tools don't
// provide.
//
// A director sets everyone's goals (calm, stage, surge, attract, disperse)
// and can shove, spawn and open or close exits. Nothing about how a push
// travels is scripted: a shove is a short force on the people near a point
// and everything after that is contact forces. In a packed crowd (bodies
// touching, k = 1.2·10⁵ kg/s²) a push travels fast: in TestShovePropagates
// a sideways shove peaks ~0.13 s later within 1.5 m and ~0.7 s later at
// 3–5 m; pushed forward, a packed column responds as one within ~0.3 s.
// Neighbouring phones (≤ 1.1 m apart) therefore mostly see lags under
// Pulse's 120 ms wave floor, and the wave detector rarely fires here: in
// this model crushes are caught by density, not travelling waves.
//
// Ground truth (truth.go) is per-person pressure (Σ|k·g| ÷ 2πr) and local
// density (people within 1 m ÷ π m²); the crowd is dangerous when ≥ 3
// people are at ≥ 1600 N/m or ≥ 5 are above 6 /m², for ≥ 1 s.
//
// A fraction of the people (participation) carry a phone flat on the
// chest. Each phone reports its body's acceleration (dv/dt, from the
// forces) in the body frame, plus gait, breathing and noise, as 100 ms
// means: exactly what the phone page sends. See phone.go.
package crowdsim

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sort"

	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// Physics constants.
const (
	A        = 2000.0 // N, social repulsion strength
	B        = 0.08   // m, social repulsion range
	K        = 1.2e5  // kg/s², body compression
	Kappa    = 2.4e5  // kg/(m·s), sliding friction
	Tau      = 0.5    // s, relaxation time
	Dt       = 0.02   // s, world tick (50 Hz)
	SubSteps = 4      // physics steps per tick (5 ms each)
	MaxSpeed = 5.0    // m/s
	MaxAccel = 100.0  // m/s² (10 g)
	// Social forces are ignored beyond r_ij + cutExtra (2000·e^(−0.6/0.08) ≈ 1 N).
	cutExtra = 0.6
	cellSize = 1.6 // spatial hash cell, m (≥ max r_ij + cutExtra, ≥ lookAhead)
)

// Ground-truth thresholds.
const (
	// CrushPressure (N/m) is where Helbing et al. (2000) put injuries: the
	// sum of radial compression forces on a body divided by its
	// circumference.
	CrushPressure = 1600.0
	// CrushDensity (people/m²): above 6/m² a crowd is in the danger range
	// (Fruin level of service F; Still, "Crowd dynamics").
	CrushDensity = 6.0
	// DangerHoldSec: the truth must stay dangerous this long to count (a
	// single shove can spike pressure for one tick).
	DangerHoldSec = 1.0
	// dangerPressureCount people over CrushPressure make the crowd dangerous.
	dangerPressureCount = 3
	// dangerDensityCount people above CrushDensity make it dangerous too.
	dangerDensityCount = 5
	densityRadius      = 1.0 // m, per-person local density
	windowM            = 3.0 // m, ground-truth density window
	windowCell         = 0.5 // m, window grid step
)

// Actions.
const (
	ActCalm     = "calm"
	ActStage    = "stage"
	ActSurge    = "surge"
	ActAttract  = "attract"
	ActShove    = "shove"
	ActExit     = "exit"
	ActDisperse = "disperse"
	ActSpawn    = "spawn"
)

// Limits.
const (
	MaxPeople    = 1000
	MaxSpawn     = 200
	ShoveRadius  = 1.5 // m
	shoveSec     = 0.3 // a shove is a force over this long
	shoveDV      = 2.0 // m/s velocity change at the centre at strength 1
	attractShare = 0.4
	attractPack  = 3.5 // people/m² at which a follower is close enough
)

// Agent is one simulated person.
type Agent struct {
	ID       int
	X, Y     float64 // m
	VX, VY   float64 // m/s
	AX, AY   float64 // m/s², dv/dt over the last tick
	R, M     float64 // radius (m), mass (kg)
	V0       float64 // own desired speed (m/s)
	Pressure float64 // N/m: Σ|body compression| / (2πr)
	Density  float64 // people/m² within 1 m (incl. self)

	gx, gy       float64 // goal
	homeX, homeY float64 // calm: the spot this person mills around
	nextHome     float64 // calm: when to pick a new spot
	follow       bool    // attract: heading for the point
	wx, wy       float64 // wander (Ornstein–Uhlenbeck, unit variance)
	vbar         float64 // smoothed speed toward the goal (impatience)
	dvx, dvy     float64 // desired velocity this tick
	ex, ey       float64 // desired direction (0 when standing)
	pushX, pushY float64 // shove force (m/s²) and how long it lasts
	pushT        float64
	fx, fy, comp float64 // per-step accumulators
	phone        *phone
	out          bool // left through an exit
}

// Config starts a world.
type Config struct {
	W, H          float64
	People        int
	Participation float64 // fraction carrying a phone, (0, 1]
	Scenario      string  // "concert" (the only one for now)
	Seed          int64
	StartMs       int64 // server clock at t = 0, for phone timestamps
	// Layout, when it has walls or exits, replaces the default geometry
	// (see LayoutGeometry).
	Layout *protocol.VenueLayout
}

// Scenarios lists the start scenarios.
var Scenarios = []string{"concert"}

// World is the simulation. Not safe for concurrent use.
type World struct {
	G             *Geometry
	T             float64 // s since start
	StartMs       int64
	Participation float64
	Action        string // last behaviour action (calm, stage, surge, attract, disperse)
	strength      float64
	attX, attY    float64

	agents   []*Agent
	nextID   int
	rng      *rand.Rand
	solid    []Seg
	grid     grid
	truth    Truth
	events   []Event
	ticks    int64
	periodic float64 // corridor length if x wraps around (calibration), else 0
}

// New creates a world in the given scenario.
func New(c Config) (*World, error) {
	if c.Scenario == "" {
		c.Scenario = "concert"
	}
	if c.Scenario != "concert" {
		return nil, fmt.Errorf("unknown scenario %q (want concert)", c.Scenario)
	}
	if !(c.W >= 6 && c.H >= 6 && c.W <= 500 && c.H <= 500) {
		return nil, errors.New("venue must be 6–500 m on each side")
	}
	if c.People < 1 || c.People > MaxPeople {
		return nil, fmt.Errorf("people must be 1–%d", MaxPeople)
	}
	if !(c.Participation > 0 && c.Participation <= 1) {
		return nil, errors.New("participation must be in (0, 1]")
	}
	w := &World{G: LayoutGeometry(c.W, c.H, c.Layout), StartMs: c.StartMs, Participation: c.Participation,
		Action: ActCalm, rng: rand.New(rand.NewSource(c.Seed))}
	w.solid = w.G.solid()
	w.truth.Init()
	w.placeConcert(c.People)
	w.measure()
	return w, nil
}

// Agents are the people still in the venue (read-only use).
func (w *World) Agents() []*Agent { return w.agents }

// Phones is how many people carry a phone.
func (w *World) Phones() int {
	n := 0
	for _, a := range w.agents {
		if a.phone != nil {
			n++
		}
	}
	return n
}

// newAgent draws a person's body and desired speed.
func (w *World) newAgent(x, y float64) *Agent {
	r := w.rng
	a := &Agent{ID: w.nextID, X: x, Y: y, R: 0.20 + 0.06*r.Float64(), M: 55 + 40*r.Float64(),
		V0: math.Max(0.6, math.Min(2.0, 1.3+0.25*r.NormFloat64()))}
	w.nextID++
	a.homeX, a.homeY = x, y
	a.nextHome = w.T + 10 + 30*r.Float64()
	a.wx, a.wy = r.NormFloat64(), r.NormFloat64()
	if r.Float64() < w.Participation {
		a.phone = newPhone(w, a)
	}
	return a
}

// free reports whether a body of radius r fits at (x, y).
func (w *World) free(x, y, r float64) bool {
	if x < r+0.1 || y < r+0.1 || x > w.G.W-r-0.1 || y > w.G.H-r-0.1 {
		return false
	}
	if w.G.custom {
		// A venue layout: keep clear of the stage and every wall.
		if w.G.inStage(x, y) || w.G.inStage(x-r, y) || w.G.inStage(x+r, y) || w.G.inStage(x, y-r) || w.G.inStage(x, y+r) {
			return false
		}
		for _, s := range w.solid {
			cx, cy := closest(s, x, y)
			if math.Hypot(cx-x, cy-y) < r+0.05 {
				return false
			}
		}
	} else if w.G.inStage(x, y) || (y < w.G.BarrierY+r+0.05 && x > w.G.BarrierX0-r-0.05 && x < w.G.BarrierX1+r+0.05) {
		return false
	}
	for _, b := range w.agents {
		if math.Hypot(b.X-x, b.Y-y) < r+b.R+0.05 {
			return false
		}
	}
	return true
}

// placeConcert spreads people in front of the stage, denser toward it.
func (w *World) placeConcert(n int) {
	g := w.G
	x0, x1 := 1.0, g.W-1.0
	y0, y1 := g.BarrierY+0.4, g.BarrierY+0.4+math.Max(4, (g.H-g.BarrierY)*0.8)
	for tries := 0; len(w.agents) < n && tries < n*400; tries++ {
		x := x0 + (x1-x0)*w.rng.Float64()
		// Denser near the stage: y skewed toward y0.
		u := w.rng.Float64()
		y := y0 + (y1-y0)*math.Pow(u, 1.25)
		r := 0.20 + 0.06*w.rng.Float64()
		if !w.free(x, y, r) {
			continue
		}
		a := w.newAgent(x, y)
		a.R = r
		w.agents = append(w.agents, a)
	}
}

// Spawn adds up to n people around (x, y); it returns how many fitted.
func (w *World) Spawn(x, y float64, n int) int {
	added := 0
	for tries := 0; added < n && tries < n*300 && len(w.agents) < MaxPeople; tries++ {
		// Grow the disc as it fills.
		rad := 0.4 + 0.35*math.Sqrt(float64(added+1)) + 0.002*float64(tries)
		ang := 2 * math.Pi * w.rng.Float64()
		d := rad * math.Sqrt(w.rng.Float64())
		px, py := x+d*math.Cos(ang), y+d*math.Sin(ang)
		r := 0.20 + 0.06*w.rng.Float64()
		if !w.free(px, py, r) {
			continue
		}
		a := w.newAgent(px, py)
		a.R = r
		w.agents = append(w.agents, a)
		added++
	}
	return added
}

// Action is a director command (POST /api/sim/action). Pointers mark the
// fields that were given.
type Action struct {
	Type     string   `json:"type"`
	X        *float64 `json:"x,omitempty"`
	Y        *float64 `json:"y,omitempty"`
	DX       *float64 `json:"dx,omitempty"`
	DY       *float64 `json:"dy,omitempty"`
	Strength *float64 `json:"strength,omitempty"`
	ID       string   `json:"id,omitempty"`
	Open     *bool    `json:"open,omitempty"`
	N        *int     `json:"n,omitempty"`
}

func finite(v *float64) bool { return v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) }

// Apply runs a director action.
func (w *World) Apply(act Action) error {
	needXY := func() error {
		if !finite(act.X) || !finite(act.Y) {
			return fmt.Errorf("%s needs x and y (venue metres)", act.Type)
		}
		if *act.X < 0 || *act.Y < 0 || *act.X > w.G.W || *act.Y > w.G.H {
			return fmt.Errorf("x, y must be inside the %gx%g m venue", w.G.W, w.G.H)
		}
		return nil
	}
	strength := func(def float64) (float64, error) {
		if act.Strength == nil {
			return def, nil
		}
		if !finite(act.Strength) || *act.Strength < 0 || *act.Strength > 1 {
			return 0, errors.New("strength must be 0..1")
		}
		return *act.Strength, nil
	}
	switch act.Type {
	case ActCalm:
		w.Action = ActCalm
		for _, a := range w.agents {
			a.follow = false
			a.homeX, a.homeY = a.X, a.Y
			a.nextHome = w.T + 10 + 30*w.rng.Float64()
		}
	case ActStage:
		w.Action, w.strength = ActStage, 0
		w.unfollow()
	case ActSurge:
		s, err := strength(0.7)
		if err != nil {
			return err
		}
		w.Action, w.strength = ActSurge, s
		w.unfollow()
		for _, a := range w.agents { // nobody is held up yet
			a.vbar = a.V0 * (1 + s)
		}
	case ActAttract:
		if err := needXY(); err != nil {
			return err
		}
		w.Action, w.attX, w.attY = ActAttract, *act.X, *act.Y
		w.pickFollowers()
	case ActShove:
		if err := needXY(); err != nil {
			return err
		}
		if !finite(act.DX) || !finite(act.DY) || math.Hypot(*act.DX, *act.DY) < 1e-6 {
			return errors.New("shove needs a direction dx, dy (not both 0)")
		}
		s, err := strength(0.7)
		if err != nil {
			return err
		}
		w.Shove(*act.X, *act.Y, *act.DX, *act.DY, s)
	case ActExit:
		e := w.G.Exit(act.ID)
		if e == nil {
			ids := make([]string, len(w.G.Exits))
			for i, x := range w.G.Exits {
				ids[i] = x.ID
			}
			return fmt.Errorf("unknown exit %q (want one of %v)", act.ID, ids)
		}
		if act.Open == nil {
			return errors.New("exit needs open: true or false")
		}
		e.Open = *act.Open
		w.solid = w.G.solid()
	case ActDisperse:
		if w.nearestExit(w.G.W/2, w.G.H/2) == nil {
			return errors.New("every exit is closed: open one first")
		}
		w.Action = ActDisperse
		w.unfollow()
	case ActSpawn:
		if err := needXY(); err != nil {
			return err
		}
		if act.N == nil || *act.N < 1 || *act.N > MaxSpawn {
			return fmt.Errorf("spawn needs n (1–%d)", MaxSpawn)
		}
		if len(w.agents) >= MaxPeople {
			return fmt.Errorf("the venue already holds the maximum of %d people", MaxPeople)
		}
		if w.Spawn(*act.X, *act.Y, *act.N) == 0 {
			return errors.New("no room for anyone there")
		}
	case "":
		return errors.New("missing action type")
	default:
		return fmt.Errorf("unknown action %q (want calm, stage, surge, attract, shove, exit, disperse or spawn)", act.Type)
	}
	return nil
}

func (w *World) unfollow() {
	for _, a := range w.agents {
		a.follow = false
	}
}

// pickFollowers sends about 40 % of the crowd to the attraction point,
// mostly those nearer to it (distance plus a random 0–6 m).
func (w *World) pickFollowers() {
	type cand struct {
		a *Agent
		k float64
	}
	cs := make([]cand, len(w.agents))
	for i, a := range w.agents {
		a.follow = false
		cs[i] = cand{a, math.Hypot(a.X-w.attX, a.Y-w.attY) + 6*w.rng.Float64()}
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].k < cs[j].k })
	n := int(math.Round(attractShare * float64(len(cs))))
	for i := 0; i < n; i++ {
		cs[i].a.follow = true
	}
	for _, a := range w.agents { // the rest stay where they are
		if !a.follow {
			a.homeX, a.homeY = a.X, a.Y
		}
	}
}

// Shove pushes the people within ShoveRadius of (x, y) toward (dx, dy):
// a force over shoveSec giving a velocity change of up to 2 m/s × strength
// at the centre, falling off linearly to the edge. What happens next is up
// to the contact forces.
func (w *World) Shove(x, y, dx, dy, strength float64) {
	l := math.Hypot(dx, dy)
	ux, uy := dx/l, dy/l
	for _, a := range w.agents {
		d := math.Hypot(a.X-x, a.Y-y)
		if d > ShoveRadius {
			continue
		}
		acc := shoveDV * strength * (1 - 0.7*d/ShoveRadius) / shoveSec
		a.pushX, a.pushY, a.pushT = ux*acc, uy*acc, shoveSec
	}
}

// nearestExit is the closest open exit to (x, y), nil if all are closed.
func (w *World) nearestExit(x, y float64) *Exit {
	var best *Exit
	bd := math.Inf(1)
	for _, e := range w.G.Exits {
		if !e.Open {
			continue
		}
		mx, my := e.mid()
		if d := math.Hypot(mx-x, my-y); d < bd {
			best, bd = e, d
		}
	}
	return best
}

// ---- behaviour ----

// desire sets each person's goal and desired velocity for this tick.
func (w *World) desire() {
	g := w.G
	cx := (g.BarrierX0 + g.BarrierX1) / 2
	nFollow := 0
	for _, a := range w.agents {
		if a.follow {
			nFollow++
		}
	}
	// Followers stop pressing once inside the disc they would fill at ~3.5/m².
	packR := math.Sqrt(float64(nFollow) / (3.5 * math.Pi))
	w.grid.build(w.agents)
	for i, a := range w.agents {
		var speed float64
		mode := w.Action
		if mode == ActAttract && !a.follow {
			mode = ActCalm
		}
		switch mode {
		case ActCalm:
			if w.T >= a.nextHome {
				// Mill about: a new spot within ~1.5 m every 10–40 s.
				for i := 0; i < 10; i++ {
					hx := a.homeX + 1.5*(2*w.rng.Float64()-1)
					hy := a.homeY + 1.5*(2*w.rng.Float64()-1)
					if hx > 0.5 && hy > g.BarrierY+0.5 && hx < g.W-0.5 && hy < g.H-0.5 {
						a.homeX, a.homeY = hx, hy
						break
					}
				}
				a.nextHome = w.T + 10 + 30*w.rng.Float64()
			}
			a.gx, a.gy = a.homeX, a.homeY
			d := math.Hypot(a.gx-a.X, a.gy-a.Y)
			speed = 0.5 * a.V0 * math.Min(1, math.Max(0, d-0.15)/1.0)
		case ActStage, ActSurge:
			// Head for the barrier just ahead, drifting toward the middle.
			pull := 0.3
			if mode == ActSurge {
				pull = 0.3 + 0.3*w.strength
			}
			a.gx = math.Max(g.BarrierX0+0.3, math.Min(g.BarrierX1-0.3, a.X+(cx-a.X)*pull))
			a.gy = g.BarrierY
			speed = a.V0
			if mode == ActSurge {
				speed = w.surgeSpeed(a)
			}
		case ActAttract:
			// Head for the point; stop pushing once it is crowded around you
			// (about 3.5 people/m² within 1 m) near the group.
			a.gx, a.gy = w.attX, w.attY
			d := math.Hypot(a.gx-a.X, a.gy-a.Y)
			speed = 0.8 * a.V0 * math.Max(0, math.Min(1, (d-0.3)/1.0))
			if a.Density >= attractPack && d < packR+1.5 {
				speed = 0
			}
		case actCorridor:
			a.gx, a.gy = a.X+10, a.Y
			speed = a.V0
		case ActDisperse:
			e := w.nearestExit(a.X, a.Y)
			if e == nil {
				a.gx, a.gy = a.X, a.Y
				break
			}
			mx, my := e.mid()
			// Aim for the gap from the inside, then straight out.
			if (mx-a.X)*e.nx+(my-a.Y)*e.ny > 0.6 {
				a.gx, a.gy = mx-0.3*e.nx, my-0.3*e.ny
			} else {
				a.gx, a.gy = mx+2*e.nx, my+2*e.ny
			}
			speed = a.V0
		}
		ex, ey := a.gx-a.X, a.gy-a.Y
		if l := math.Hypot(ex, ey); l > 1e-6 {
			ex, ey = ex/l, ey/l
		} else {
			ex, ey = 0, 0
		}
		// Walking (not pressing): no faster than the gap ahead allows.
		if mode != ActStage && mode != ActSurge && speed > 0 {
			speed, ex, ey = w.steer(i, speed, ex, ey)
		}
		a.dvx, a.dvy = speed*ex, speed*ey
		a.ex, a.ey = 0, 0
		if speed > 0.05 {
			a.ex, a.ey = ex, ey
		}
		// Impatience bookkeeping: how fast this person actually gets on.
		along := a.VX*ex + a.VY*ey
		a.vbar += (Dt / 2.0) * (along - a.vbar)
		// Wander: an Ornstein–Uhlenbeck force, so nobody freezes into a lattice.
		const tw = 1.5
		sq := math.Sqrt(2 * Dt / tw)
		a.wx += -a.wx*Dt/tw + sq*w.rng.NormFloat64()
		a.wy += -a.wy*Dt/tw + sq*w.rng.NormFloat64()
	}
}

// surgeSpeed: desired speed × (1 + strength), plus mild impatience
// (Helbing 2000): v0 drifts toward vmax as the person is held up.
func (w *World) surgeSpeed(a *Agent) float64 {
	base := a.V0 * (1 + w.strength)
	vmax := math.Max(3.0, 1.2*base)
	held := math.Max(0, math.Min(1, 1-a.vbar/base))
	imp := 0.5 * w.strength * held
	return (1-imp)*base + imp*vmax
}

func (w *World) wanderSigma() float64 {
	switch w.Action {
	case ActCalm, ActAttract:
		return 0.25
	}
	return 0.15
}

// ---- physics ----

// Step advances the world by one 50 Hz tick.
func (w *World) Step() {
	w.desire()
	sig := w.wanderSigma()
	type v2 struct{ x, y float64 }
	v0 := make([]v2, len(w.agents))
	for i, a := range w.agents {
		v0[i] = v2{a.VX, a.VY}
	}
	h := Dt / SubSteps
	for s := 0; s < SubSteps; s++ {
		w.forces(h, sig)
		for _, a := range w.agents {
			ax, ay := a.fx/a.M, a.fy/a.M
			if m := math.Hypot(ax, ay); m > MaxAccel {
				ax, ay = ax*MaxAccel/m, ay*MaxAccel/m
			}
			a.VX += ax * h
			a.VY += ay * h
			if v := math.Hypot(a.VX, a.VY); v > MaxSpeed {
				a.VX, a.VY = a.VX*MaxSpeed/v, a.VY*MaxSpeed/v
			}
			a.X += a.VX * h
			a.Y += a.VY * h
			if w.periodic > 0 {
				a.X -= w.periodic * math.Floor(a.X/w.periodic)
			}
			if a.pushT > 0 {
				a.pushT -= h
			}
		}
	}
	for i, a := range w.agents {
		a.AX, a.AY = (a.VX-v0[i].x)/Dt, (a.VY-v0[i].y)/Dt
		a.Pressure = a.comp / (2 * math.Pi * a.R)
	}
	w.T += Dt
	w.ticks++
	w.leave()
	w.measure()
	w.phones()
}

// forces fills fx, fy and comp for every agent.
func (w *World) forces(h, sig float64) {
	for _, a := range w.agents {
		a.fx = a.M*(a.dvx-a.VX)/Tau + a.M*sig*a.wx
		a.fy = a.M*(a.dvy-a.VY)/Tau + a.M*sig*a.wy
		if a.pushT > 0 {
			a.fx += a.M * a.pushX
			a.fy += a.M * a.pushY
		}
		a.comp = 0
	}
	pair := func(a, b *Agent) {
		dx, dy := a.X-b.X, a.Y-b.Y
		if w.periodic > 0 {
			dx -= w.periodic * math.Round(dx/w.periodic)
		}
		d2 := dx*dx + dy*dy
		rij := a.R + b.R
		if d2 > (rij+cutExtra)*(rij+cutExtra) {
			return
		}
		d := math.Sqrt(d2)
		if d < 1e-6 { // exactly on top of each other: pick a direction
			dx, dy, d = 1e-3*(float64(a.ID%7)-3), 1e-3, 1e-3
		}
		nx, ny := dx/d, dy/d
		ov := rij - d
		f := A * math.Exp(math.Min(ov, 0.4)/B)
		if ov > 0 {
			c := K * ov
			f += c
			a.comp += c
			b.comp += c
			// Sliding friction κ·g·Δv_t·t, capped so one explicit step can
			// at most halve the relative tangential velocity (stability).
			tx, ty := -ny, nx
			dvt := (b.VX-a.VX)*tx + (b.VY-a.VY)*ty
			mred := a.M * b.M / (a.M + b.M)
			kap := math.Min(Kappa*ov, 0.5*mred/h)
			a.fx += kap * dvt * tx
			a.fy += kap * dvt * ty
			b.fx -= kap * dvt * tx
			b.fy -= kap * dvt * ty
		}
		a.fx += f * nx
		a.fy += f * ny
		b.fx -= f * nx
		b.fy -= f * ny
	}
	if w.periodic > 0 {
		for i, a := range w.agents {
			for _, b := range w.agents[i+1:] {
				pair(a, b)
			}
		}
	} else {
		w.grid.build(w.agents)
		w.grid.pairs(w.agents, pair)
	}
	for _, a := range w.agents {
		for _, s := range w.solid {
			cx, cy := closest(s, a.X, a.Y)
			dx, dy := a.X-cx, a.Y-cy
			d2 := dx*dx + dy*dy
			if d2 > (a.R+cutExtra)*(a.R+cutExtra) {
				continue
			}
			d := math.Sqrt(d2)
			if d < 1e-6 {
				continue
			}
			nx, ny := dx/d, dy/d
			ov := a.R - d
			f := A * math.Exp(math.Min(ov, 0.4)/B)
			if ov > 0 {
				c := K * ov
				f += c
				a.comp += c
				tx, ty := -ny, nx
				vt := a.VX*tx + a.VY*ty
				kap := math.Min(Kappa*ov, 0.5*a.M/h)
				a.fx -= kap * vt * tx
				a.fy -= kap * vt * ty
			}
			a.fx += f * nx
			a.fy += f * ny
		}
	}
}

// leave removes people who walked out through an exit.
func (w *World) leave() {
	if w.periodic > 0 {
		return
	}
	g := w.G
	keep := w.agents[:0]
	for _, a := range w.agents {
		if a.X < -0.3 || a.Y < -0.3 || a.X > g.W+0.3 || a.Y > g.H+0.3 {
			a.out = true
			if a.phone != nil {
				w.events = append(w.events, Event{Kind: EvGone, ID: a.phone.id})
			}
			continue
		}
		keep = append(keep, a)
	}
	for i := len(keep); i < len(w.agents); i++ {
		w.agents[i] = nil
	}
	w.agents = keep
}

// ---- spatial hash ----

type grid struct {
	x0, y0     float64
	nx, ny     int
	start, idx []int
	cell       []int
}

func (g *grid) build(as []*Agent) {
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, a := range as {
		minX, minY = math.Min(minX, a.X), math.Min(minY, a.Y)
		maxX, maxY = math.Max(maxX, a.X), math.Max(maxY, a.Y)
	}
	if len(as) == 0 {
		minX, minY, maxX, maxY = 0, 0, 1, 1
	}
	g.x0, g.y0 = minX-0.01, minY-0.01
	g.nx = int((maxX-g.x0)/cellSize) + 1
	g.ny = int((maxY-g.y0)/cellSize) + 1
	nc := g.nx * g.ny
	if cap(g.start) < nc+1 {
		g.start = make([]int, nc+1)
	}
	g.start = g.start[:nc+1]
	for i := range g.start {
		g.start[i] = 0
	}
	if cap(g.cell) < len(as) {
		g.cell = make([]int, len(as))
		g.idx = make([]int, len(as))
	}
	g.cell, g.idx = g.cell[:len(as)], g.idx[:len(as)]
	for i, a := range as {
		c := int((a.Y-g.y0)/cellSize)*g.nx + int((a.X-g.x0)/cellSize)
		g.cell[i] = c
		g.start[c+1]++
	}
	for i := 1; i <= nc; i++ {
		g.start[i] += g.start[i-1]
	}
	fill := append([]int(nil), g.start[:nc]...)
	for i := range as {
		c := g.cell[i]
		g.idx[fill[c]] = i
		fill[c]++
	}
}

// pairs calls fn once for every pair of agents in the same or adjacent cells.
func (g *grid) pairs(as []*Agent, fn func(a, b *Agent)) {
	for i, a := range as {
		c := g.cell[i]
		cx, cy := c%g.nx, c/g.nx
		for oy := -1; oy <= 1; oy++ {
			y := cy + oy
			if y < 0 || y >= g.ny {
				continue
			}
			for ox := -1; ox <= 1; ox++ {
				x := cx + ox
				if x < 0 || x >= g.nx {
					continue
				}
				cc := y*g.nx + x
				for k := g.start[cc]; k < g.start[cc+1]; k++ {
					if j := g.idx[k]; j > i {
						fn(a, as[j])
					}
				}
			}
		}
	}
}

// near calls fn for every agent within r of agent i (r ≤ cellSize).
func (g *grid) near(as []*Agent, i int, r float64, fn func(b *Agent)) {
	a := as[i]
	c := g.cell[i]
	cx, cy := c%g.nx, c/g.nx
	for oy := -1; oy <= 1; oy++ {
		y := cy + oy
		if y < 0 || y >= g.ny {
			continue
		}
		for ox := -1; ox <= 1; ox++ {
			x := cx + ox
			if x < 0 || x >= g.nx {
				continue
			}
			cc := y*g.nx + x
			for k := g.start[cc]; k < g.start[cc+1]; k++ {
				j := g.idx[k]
				if j == i {
					continue
				}
				b := as[j]
				if (b.X-a.X)*(b.X-a.X)+(b.Y-a.Y)*(b.Y-a.Y) <= r*r {
					fn(b)
				}
			}
		}
	}
}

// Speed adaptation. People who are walking (not pressing forward) keep a
// time gap to whoever is in their path: desired speed ≤ gap / TimeGap,
// where gap is the free space to the nearest person ahead whose body
// overlaps the walker's path (as in the collision-free speed model of
// Tordeux, Chraibi & Seyfried 2016). It is what makes a walking crowd slow
// down with density the way Weidmann's fundamental diagram says (see
// calib.go). Pressing (stage, surge) ignores it: pushing on despite being
// blocked is what builds the pressure of a crush (Helbing et al. 2000).
// TimeGap is a var only so the calibration sweep can try values.
var TimeGap = 0.6 // s

const lookAhead = 1.5 // m, centre to centre

// gapAhead is the free distance (m) along (ex, ey) from agent i to the
// nearest person in its path, lookAhead if nobody is.
func (w *World) gapAhead(i int, ex, ey float64) float64 {
	a := w.agents[i]
	best := lookAhead
	check := func(b *Agent) {
		dx, dy := b.X-a.X, b.Y-a.Y
		if w.periodic > 0 {
			dx -= w.periodic * math.Round(dx/w.periodic)
		}
		along := dx*ex + dy*ey
		if along <= 0 || along > lookAhead {
			return
		}
		rij := a.R + b.R
		perp := math.Abs(dx*ey - dy*ex)
		if perp >= rij {
			return
		}
		// Distance along the path until the two bodies touch.
		g := along - math.Sqrt(rij*rij-perp*perp)
		best = math.Min(best, math.Max(0, g))
	}
	if w.periodic > 0 {
		for j, b := range w.agents {
			if j != i {
				check(b)
			}
		}
		return best
	}
	w.grid.near(w.agents, i, lookAhead, check)
	return best
}

// steerAngles are the headings (rad) a walker considers around the
// direct one, nearest first.
var steerAngles = []float64{0, 0.175, -0.175, 0.35, -0.35}

// steer picks the heading that makes the most progress toward the goal
// given the time gap (Moussaïd, Helbing & Theraulaz 2011: walk where the
// way toward the destination is freest), so a fast walker steps around a
// slow one instead of queueing behind it. Returns the capped speed and
// the chosen direction.
func (w *World) steer(i int, speed, ex, ey float64) (float64, float64, float64) {
	bestP, bestV, bx, by := -1.0, 0.0, ex, ey
	for _, th := range steerAngles {
		c, s := math.Cos(th), math.Sin(th)
		dx, dy := ex*c-ey*s, ex*s+ey*c
		v := math.Min(speed, w.gapAhead(i, dx, dy)/TimeGap)
		if p := v * c; p > bestP+1e-9 {
			bestP, bestV, bx, by = p, v, dx, dy
		}
		if v >= speed && th == 0 {
			break // the way ahead is free
		}
	}
	return bestV, bx, by
}
