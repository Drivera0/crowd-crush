// Package crowdsim is a pedestrian crowd simulation for testing Pulse
// against bodies instead of scripted signals: a Social Force Model in which
// a crowd crush can emerge, people who behave like a concert audience, and
// the phones some of those people carry.
//
// # Physics
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
// Walking people keep a time gap (TimeGap = 0.6 s) to whoever is in their
// path (someone coming the other way counts as closing the gap), and step
// around a slower walker when that gets them on faster: speed adaptation
// as in Tordeux, Chraibi & Seyfried's collision-free speed model (2016)
// and the steering heuristic of Moussaïd, Helbing & Theraulaz (2011). In
// the venue they consider headings up to ±60° (to get round standing
// people) and pass oncoming people on the right, the side preference
// Moussaïd et al. (2009) measured; in the calibration corridor, ±20°.
// People pressing toward the stage (stage, surge) ignore the time gap:
// pushing on while blocked is what builds crush pressure. People leaving
// (disperse) close up to EvacTimeGap = 0.3 s, calibrated on bottleneck
// flow (below).
//
// # Behaviour (behaviour.go)
//
// Groups. 60 % of people arrive in groups of 2–4 (sizes 2:3:4 in
// proportion 0.60:0.27:0.13); Moussaïd, Perozo, Garnier, Helbing &
// Theraulaz, "The walking behaviour of pedestrian social groups and its
// impact on crowd dynamics", PLoS ONE 5, e10047 (2010) found up to 70 % of
// pedestrians walking in groups, mostly pairs. A group shares its purpose
// and target. Walking, its members hold formation slots around the
// centroid (a spring of 0.8 /s toward the slot on top of the group's
// velocity): abreast at low density (pairs 0.75 m apart, fours 2.1 m
// wide), bent into a V (middle behind) or U and narrowed as the local
// density rises, as in that paper's Fig. 3. The group walks at its
// slowest member's speed × 1, 0.9, 0.85, 0.8 for sizes 1–4 (bigger groups
// are slower) and slows down while a member is more than ~1.6 m from the
// centroid, so stragglers catch up. Stage and surge press groups forward
// together (the group drifts as one and keeps its members side by side);
// attract sends whole groups; disperse sends each group to the exit
// nearest its centroid.
//
// The concert routine (calm, dance, intermission):
//   - most people stand still facing the stage. Standing is planted: the
//     driving term damps velocity with τ = 0.3 s, there is no wander force,
//     and social (non-contact) repulsion below 150 N net is ignored, like
//     static friction. Contact forces and shoves always act, so pushes
//     still travel and pressure still builds; walkers squeezing past nudge
//     standing people aside;
//   - a quarter sway gently to the music, everyone sways and most bounce on
//     "dance", and weight shifts and breathing go on the whole time. These
//     move the torso, not the feet: they are in the phone signal (phone.go),
//     not in the bodies' positions;
//   - groups go to a point of interest now and then (mean every 12 min per
//     group): a bar on the right wall, toilets on the left, merch at the
//     back (pushed inward until clear of the stage and walls when a venue
//     layout is used). Each member gets a spot in a half-disc in front of
//     the POI sized for 2.5 people/m², so a busy bar draws a crowd. They
//     stay 30–120 s (intermission: up to 150 s), then go back to within
//     ~0.3 m of where they stood;
//   - people arrive through a random open exit and walk to a spot with
//     room (local density < 2/m²), and idle groups leave through the
//     nearest exit, each at 1/2400 of the crowd per second (about 6 people
//     a minute each way for 250);
//   - intermission: the music stops and 55 % of idle groups set off for the
//     POIs within 25 s, so the first ones come back through those still
//     arriving (bidirectional flow) and the POIs crowd up.
//
// Motion is smooth: desired speed rises at most 1 m/s² (voluntary slowing
// 3 m/s²; braking for the time gap is immediate), the walking heading turns
// at most 2 rad/s (5 rad/s when nearly stopped, turning on the spot), and
// the body turns to face where it is going (2 rad/s) or, standing, toward
// the stage or the POI it is at (≤ 1 rad/s, with slow fidgeting of a few
// degrees). The phone's frame is the body's facing.
//
// # Validation
//
// Mean walking speed against density in a 12 × 2.5 m periodic corridor,
// compared with Weidmann's fundamental diagram (1993; v0 = 1.34 m/s,
// γ = 1.913 m⁻², ρmax = 5.4 /m²), 3 seeds × 20 s each
// (PULSE_WEIDMANN=1 go test ./server/internal/crowdsim -run WeidmannSweep -v):
//
//	ρ /m²    0.5   1.0   1.5   2.0   2.5   3.0   3.5   4.0   4.5   5.0
//	sim m/s  1.23  1.10  0.90  0.60  0.32  0.27  0.19  0.08  0.02  0.00
//	Weidmann 1.30  1.06  0.81  0.61  0.45  0.33  0.23  0.16  0.09  0.04
//
// Within ±0.10 m/s from 0.5 to 5 /m² except 2.5 /m² (−0.13). Free walking
// is a little slow because desired speeds average 1.3 m/s (not 1.34) and
// the wander force costs a little; jam density is reached slightly early.
// TestWeidmann checks 0.5, 2 and 5 /m² on every run. Without the time gap,
// the Helbing 2000 forces alone (B = 0.08 m) let a corridor flow at
// ~1.3 m/s up to 4 /m².
//
// Bottleneck (TestBottleneck; sweep: PULSE_BOTTLE=1 go test
// ./server/internal/crowdsim -run BottleneckSweep -v): 120 people waiting
// in a 10 × 8 m room leave through a door in one wall. Specific flow
// through a 1 m door: 1.6 persons/(m·s) (1.63 over 3 seeds; 0.8 m: 1.32,
// 1.2 m: 2.01). Kretz, Grünebohm & Schreckenberg (2006) and Seyfried et
// al. (2009) measured ~1.6–1.9 /(m·s) for ~1 m. The evacuation time gap
// was chosen for this (0.6 s gives 1.2, 0.2 s gives 2.0). The specific
// flow rises with door width here, while the experiments find it roughly
// constant.
//
// Counterflow (TestCounterflow): a 12 × 4 m periodic corridor, half the
// people walking each way. At 1 /m², lanes form (lane order parameter
// 0.06–0.15 → 0.70–0.82 over two seeds; Rex & Löwen 2007) and people walk
// at 67–87 % of the one-way speed with the same steering. At 2 /m² lanes
// form only partly (→ 0.38–0.51) and speed drops to 49–58 %; whether lanes
// or a gridlock win there depends on the seed, so it is only logged.
// Experiments (Zhang et al. 2012) report a smaller loss at 2 /m², so this
// is a sanity check, not a calibration.
//
// Behaviour checks: a standing crowd of 250 has mean speed < 0.001 m/s and
// moves < 1 mm per person in 20 s (TestStandingNoJitter); walking group
// members stay ~0.7 m from their centroid on average (TestGroups); nobody
// turns or speeds up faster than the limits (TestSmoothMotion); an
// intermission brings 60–75 % of a 150-person crowd to the POIs (local
// density up to ~2.5–3 /m²) and back within 200 s, never dangerous
// (TestIntermission).
//
// Limits. Groups never split (one member can't pop to the toilet alone);
// POIs have no service model (people stand around them, they don't queue
// in a line); standing people never shuffle their feet, so a crowd stays
// exactly where it settled; the side preference is fixed to the right;
// sway and dance exist only in the phone signal; bodies are discs, so
// nobody turns sideways to squeeze through. Only the three benchmarks
// above were checked, not evacuation times or panic behaviour.
//
// Vadere (TU Munich), JuPedSim (Forschungszentrum Jülich) and NetLogo are
// reference tools for this model family. Pulse implements the Social Force
// Model natively in Go because it needs per-person phone accelerometer
// signals and live steering from the dashboard, which those tools don't
// provide.
//
// # Director, waves and ground truth
//
// A director sets the behaviour (calm, stage, surge, attract, disperse,
// dance, intermission) and can shove, spawn and open or close exits.
// Nothing about how a push travels is scripted: a shove is a short force
// on the people near a point and everything after that is contact forces.
// In a packed crowd (bodies touching, k = 1.2·10⁵ kg/s²) a push travels
// fast: in TestShovePropagates a sideways shove peaks ~0.14 s later within
// 1.5 m and ~0.65 s later at 3–5 m; pushed forward, a packed column
// responds as one within ~0.3 s. Neighbouring phones (≤ 1.1 m apart)
// therefore mostly see lags under Pulse's 120 ms wave floor, and the wave
// detector rarely fires here: in this model crushes are caught by density,
// not travelling waves.
//
// Ground truth (truth.go) is per-person pressure (Σ|k·g| ÷ 2πr) and local
// density (people within 1 m ÷ π m²); the crowd is dangerous when ≥ 3
// people are at ≥ 1600 N/m or ≥ 5 are above 6 /m², for ≥ 1 s.
//
// A fraction of the people (participation) carry a phone flat on the
// chest. Each phone reports its body's acceleration (dv/dt, from the
// forces) in the body frame, plus gait, weight shifts, music, breathing
// and noise, as 100 ms means: exactly what the phone page sends; and its
// position every 100 ms. See phone.go.
package crowdsim

import (
	"errors"
	"fmt"
	"math"
	"math/rand"

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
	// ActDance: everyone standing sways and bounces to a shared beat, each
	// with their own delay and amplitude (the false-positive test).
	ActDance = "dance"
	// ActIntermission: the music stops and many groups head for the bar,
	// toilets and merch at once, then come back.
	ActIntermission = "intermission"
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
	homeX, homeY float64 // where this person stands during the show (returns here after a trip)
	spotX, spotY float64 // where this person is settling now
	done         bool    // reached its spot (or gave up)
	grp          *group
	slot         int     // place in the group's formation
	atPOI        int     // POI this person has a spot at, −1 = none
	wx, wy       float64 // wander (Ornstein–Uhlenbeck, unit variance)
	vbar         float64 // smoothed speed toward the goal (impatience)
	sp, hd       float64 // smoothed desired speed (m/s) and heading (rad)
	stand        bool    // standing still this tick (dead-band applies)
	face         float64 // body facing (rad, venue frame: 0 = +x, y down); the phone's frame
	prevFace     float64
	yaw          float64 // slow fidgeting around the facing target
	cdir         float64 // corridor: +1 walks toward +x, −1 toward −x
	style        style   // how this person moves to music
	dvx, dvy     float64 // desired velocity this tick
	ex, ey       float64 // desired direction (0 when standing)
	pushX, pushY float64 // shove force (m/s²) and how long it lasts
	pushT        float64
	fx, fy, comp float64 // per-step accumulators
	sx, sy       float64 // social (non-contact) force this step
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
	// Realism makes the phones messy (GPS error, carry, dropouts; see
	// realism.go). The zero value is the ideal phone.
	Realism Realism
}

// Scenarios lists the start scenarios.
var Scenarios = []string{"concert"}

// World is the simulation. Not safe for concurrent use.
type World struct {
	G             *Geometry
	T             float64 // s since start
	StartMs       int64
	Participation float64
	Action        string // last behaviour action (calm, stage, surge, attract, disperse, dance, intermission)
	strength      float64
	attX, attY    float64
	// Churn: people arrive and leave through the exits at a low rate
	// during the routine. Trips: groups go to the POIs and back. Both on
	// for a concert; tests switch them off for a still crowd.
	Churn, Trips bool
	BeatHz       float64 // the music's beat (Hz)

	agents    []*Agent
	groups    []*group
	nextGroup int
	pois      []POI
	packR     float64
	nextID    int
	rng       *rand.Rand
	solid     []Seg
	grid      grid
	truth     Truth
	pins      []Pin  // real people pinned in place (pinned.go)
	press     *Press // the crowd leaning toward a point (pinned.go)
	events    []Event
	ticks     int64
	periodic  float64 // corridor length if x wraps around (calibration), else 0
	counter   bool    // counterflow corridor: walkers steer as in the venue
	quiet     bool    // phones off (settling before t = 0)
	realism   Realism
	seed      int64
	env       Env        // what the messy phones share (common GPS error)
	envRng    *rand.Rand // nil with ideal phones
}

// Realism is the phones' imperfections.
func (w *World) Realism() Realism { return w.realism }

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
	if err := c.Realism.Validate(); err != nil {
		return nil, err
	}
	w := &World{G: LayoutGeometry(c.W, c.H, c.Layout), StartMs: c.StartMs, Participation: c.Participation,
		Action: ActCalm, rng: rand.New(rand.NewSource(c.Seed)), Churn: true, Trips: true,
		realism: c.Realism, seed: c.Seed}
	if !c.Realism.Ideal() {
		w.envRng = rand.New(rand.NewSource(mixSeed(c.Seed, -7)))
	}
	w.BeatHz = 1.8 + 0.4*w.rng.Float64() // 108–132 BPM
	w.solid = w.G.solid()
	w.truth.Init()
	w.defaultPOIs()
	w.placeConcert(c.People)
	w.settle()
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
	a.homeX, a.homeY, a.spotX, a.spotY, a.done, a.atPOI, a.cdir = x, y, x, y, true, -1, 1
	a.hd = -math.Pi/2 + 0.6*(r.Float64()-0.5) // roughly toward the stage
	a.face, a.prevFace = a.hd, a.hd
	a.wx, a.wy = r.NormFloat64(), r.NormFloat64()
	a.style = newStyle(r)
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
		ms := []*Agent{a}
		// The rest of the group stands next to them.
		size := min(w.groupSize(), n-len(w.agents)+1)
		for k := 0; len(ms) < size && k < 30; k++ {
			ang := 2 * math.Pi * w.rng.Float64()
			d := 0.5 + 0.25*w.rng.Float64()
			px, py := x+d*math.Cos(ang), y+d*math.Sin(ang)
			r := 0.20 + 0.06*w.rng.Float64()
			if !w.free(px, py, r) {
				continue
			}
			b := w.newAgent(px, py)
			b.R = r
			w.agents = append(w.agents, b)
			ms = append(ms, b)
		}
		w.addGroup(ms)
	}
}

// Spawn adds up to n people around (x, y), in groups like everyone else;
// it returns how many fitted.
func (w *World) Spawn(x, y float64, n int) int {
	added := 0
	var ms []*Agent
	size := w.groupSize()
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
		if ms = append(ms, a); len(ms) >= size {
			w.addGroup(ms)
			ms, size = nil, w.groupSize()
		}
	}
	if len(ms) > 0 {
		w.addGroup(ms)
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
	case ActCalm, ActDance, ActIntermission:
		// Back to the concert routine: whoever was pressing, gathering or
		// leaving stops where they are; trips in progress carry on.
		prev := w.Action
		w.Action = act.Type
		w.frame()
		for _, g := range w.groups {
			switch {
			case prev == ActStage || prev == ActSurge || g.purpose == pFollow || g.purpose == pEvac:
				w.toIdle(g)
			case g.purpose == pIdle:
				g.startAt = 0
			}
			if act.Type == ActIntermission && g.purpose == pIdle && w.rng.Float64() < interShare {
				g.startAt = w.T + Dt + interSpread*w.rng.Float64()
			}
		}
	case ActStage, ActSurge:
		s := 0.0
		if act.Type == ActSurge {
			var err error
			if s, err = strength(0.7); err != nil {
				return err
			}
		}
		w.Action, w.strength = act.Type, s
		w.frame()
		for _, g := range w.groups { // everyone drops what they were doing
			w.toIdle(g)
		}
		if act.Type == ActSurge {
			for _, a := range w.agents { // nobody is held up yet
				a.vbar = a.V0 * (1 + s)
			}
		}
	case ActAttract:
		if err := needXY(); err != nil {
			return err
		}
		if w.Action == ActStage || w.Action == ActSurge || w.Action == ActDisperse {
			w.frame()
			for _, g := range w.groups {
				w.toIdle(g)
			}
		}
		w.Action, w.attX, w.attY = ActAttract, *act.X, *act.Y
		w.frame()
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
		w.frame()
		for _, g := range w.groups {
			w.toIdle(g)
			g.exit = w.nearestExit(g.cx, g.cy)
			w.setOff(g, pEvac, 0, 0)
		}
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
		return fmt.Errorf("unknown action %q (want calm, stage, surge, attract, shove, exit, disperse, spawn, dance or intermission)", act.Type)
	}
	return nil
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

// surgeSpeed: desired speed × (1 + strength), plus mild impatience
// (Helbing 2000): v0 drifts toward vmax as the person is held up.
func (w *World) surgeSpeed(a *Agent) float64 {
	base := a.V0 * (1 + w.strength)
	vmax := math.Max(3.0, 1.2*base)
	held := math.Max(0, math.Min(1, 1-a.vbar/base))
	imp := 0.5 * w.strength * held
	return (1-imp)*base + imp*vmax
}

// wanderSigma is the wander force (m/s²) on people walking or pressing.
const wanderSigma = 0.15

// ---- physics ----

// Step advances the world by one 50 Hz tick.
func (w *World) Step() {
	w.desire()
	type v2 struct{ x, y float64 }
	v0 := make([]v2, len(w.agents))
	for i, a := range w.agents {
		v0[i] = v2{a.VX, a.VY}
	}
	h := Dt / SubSteps
	for s := 0; s < SubSteps; s++ {
		w.forces(h)
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
	if !w.quiet {
		w.phones()
	}
}

// settleSec of physics run before t = 0, phones off, so the random
// placement relaxes (nobody starts overlapping a neighbour's personal
// space) before anyone reports anything.
const settleSec = 2.0

func (w *World) settle() {
	churn := w.Churn
	w.quiet, w.Churn = true, false
	for w.T < settleSec-1e-9 {
		w.Step()
	}
	w.quiet, w.Churn = false, churn
	w.T, w.ticks, w.events = 0, 0, nil
	w.truth.Init()
	for _, g := range w.groups {
		g.until -= settleSec
	}
	w.measure()
}

// forces fills fx, fy and comp for every agent.
//
// Someone standing still is planted: the driving term damps their velocity
// to zero (tauStand), there is no wander, and the social (non-contact)
// repulsion only moves them once its net exceeds standDead, like static
// friction. Without this a standing crowd jiggles forever, every body
// nudged by the exponential tails of a dozen neighbours. Body contact and
// shoves always act in full, so a push still travels and pressure still
// builds.
func (w *World) forces(h float64) {
	for _, a := range w.agents {
		if a.stand {
			a.fx, a.fy = -a.M*a.VX/tauStand, -a.M*a.VY/tauStand
		} else {
			a.fx = a.M*(a.dvx-a.VX)/Tau + a.M*wanderSigma*a.wx
			a.fy = a.M*(a.dvy-a.VY)/Tau + a.M*wanderSigma*a.wy
		}
		if a.pushT > 0 {
			a.fx += a.M * a.pushX
			a.fy += a.M * a.pushY
		}
		a.comp, a.sx, a.sy = 0, 0, 0
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
		// Someone standing yields in full to a person on the move (they
		// step aside to let them past); only standers' pushes on each
		// other go through the dead-band.
		if a.stand && !b.stand {
			a.fx += f * nx
			a.fy += f * ny
		} else {
			a.sx += f * nx
			a.sy += f * ny
		}
		if b.stand && !a.stand {
			b.fx -= f * nx
			b.fy -= f * ny
		} else {
			b.sx -= f * nx
			b.sy -= f * ny
		}
		if ov > 0 {
			c := K * ov
			a.comp += c
			b.comp += c
			a.fx += c * nx
			a.fy += c * ny
			b.fx -= c * nx
			b.fy -= c * ny
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
	w.resetPins()
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
			a.sx += f * nx
			a.sy += f * ny
			if ov > 0 {
				c := K * ov
				a.fx += c * nx
				a.fy += c * ny
				a.comp += c
				tx, ty := -ny, nx
				vt := a.VX*tx + a.VY*ty
				kap := math.Min(Kappa*ov, 0.5*a.M/h)
				a.fx -= kap * vt * tx
				a.fy -= kap * vt * ty
			}
		}
		w.pinForce(a, h) // real people standing in the crowd (pinned.go)
		sx, sy := a.sx, a.sy
		if a.stand {
			m := math.Hypot(sx, sy)
			k := 0.0
			if m > standDead {
				k = (m - standDead) / m
			}
			sx, sy = sx*k, sy*k
		}
		a.fx += sx
		a.fy += sy
	}
}

// leave removes people who walked out through an exit: past the venue's
// edge, or (leaving through an inner door) through the door's gap.
func (w *World) leave() {
	if w.periodic > 0 {
		return
	}
	g := w.G
	keep := w.agents[:0]
	var gone []*Agent
	for _, a := range w.agents {
		out := a.X < -0.3 || a.Y < -0.3 || a.X > g.W+0.3 || a.Y > g.H+0.3
		if gr := a.grp; !out && gr != nil && gr.exit != nil && (gr.purpose == pLeave || gr.purpose == pEvac) {
			e := gr.exit
			mx, my := e.mid()
			along := (a.X-mx)*e.nx + (a.Y-my)*e.ny
			side := math.Abs((a.X-mx)*e.ny - (a.Y-my)*e.nx)
			out = along > 0.4 && side < math.Hypot(e.X1-e.X0, e.Y1-e.Y0)/2+0.3
		}
		if out {
			a.out = true
			if a.phone != nil {
				w.events = append(w.events, Event{Kind: EvGone, ID: a.phone.id})
			}
			gone = append(gone, a)
			continue
		}
		keep = append(keep, a)
	}
	for i := len(keep); i < len(w.agents); i++ {
		w.agents[i] = nil
	}
	w.agents = keep
	for _, a := range gone {
		w.dropMember(a)
	}
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

// EvacTimeGap is the time gap of people leaving (disperse, and the
// bottleneck test): queueing for a door, people close up (calibrated
// against bottleneck flows; see the package doc).
var EvacTimeGap = 0.3 // s

const lookAhead = 1.5 // m, centre to centre

// gapAhead is the free distance (m) along (ex, ey) from agent i to the
// nearest person in its path, lookAhead if nobody is, and whether that
// person is coming the other way.
func (w *World) gapAhead(i int, ex, ey float64) (float64, bool) {
	a := w.agents[i]
	best, onc := lookAhead, false
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
		// Distance along the path until the two bodies touch; someone
		// coming the other way closes it (the gap they leave after TimeGap).
		g := along - math.Sqrt(rij*rij-perp*perp)
		bv := b.VX*ex + b.VY*ey
		if bv < 0 {
			g += bv * TimeGap
		}
		if g = math.Max(0, g); g < best {
			best, onc = g, bv < -0.1
		}
	}
	if w.periodic > 0 {
		for j, b := range w.agents {
			if j != i {
				check(b)
			}
		}
		return best, onc
	}
	w.grid.near(w.agents, i, lookAhead, check)
	return best, onc
}

// steerAngles are the headings (rad) a walker considers around the
// direct one, nearest first.
var steerAngles = []float64{0, 0.175, -0.175, 0.35, -0.35}

// steer picks the heading that makes the most progress toward the goal
// given the time gap (Moussaïd, Helbing & Theraulaz 2011: walk where the
// way toward the destination is freest), so a fast walker steps around a
// slow one instead of queueing behind it. Someone coming the other way is
// passed on the right (positive angles; y points down): the side
// preference Moussaïd et al. (2009) measured, which is what makes lanes
// form in counterflow. Returns the capped speed and the chosen direction.
func (w *World) steer(i int, speed, ex, ey float64, angles []float64, tg float64) (float64, float64, float64) {
	bestP, bestV, bx, by := -1.0, 0.0, ex, ey
	keepRight := false
	for _, th := range angles {
		if keepRight && th < 0 {
			continue
		}
		c, s := math.Cos(th), math.Sin(th)
		dx, dy := ex*c-ey*s, ex*s+ey*c
		gap, onc := w.gapAhead(i, dx, dy)
		v := math.Min(speed, gap/tg)
		if p := v * c; p > bestP+1e-9 {
			bestP, bestV, bx, by = p, v, dx, dy
		}
		if th == 0 {
			if v >= speed {
				break // the way ahead is free
			}
			keepRight = onc
		}
	}
	return bestV, bx, by
}
