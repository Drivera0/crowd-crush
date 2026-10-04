package app

import (
	"errors"
	"log"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Drivera0/crowd-crush/server/internal/crowdsim"
	"github.com/Drivera0/crowd-crush/server/internal/hub"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/store"
)

// The in-process crowd simulation (package crowdsim) is a third data source
// next to live and replay. Like a replay it gets its own pipeline, shown on
// the dashboard while it runs; live phones keep streaming into the live
// pipeline underneath, so stopping is instant. Its phones go through the
// same helloIn / syncIn / posIn / motionIn / goneIn path as real phones
// (ua "sim"), so they are recorded into a labelled run like live ones, but
// they are not written to continuous storage (Tiger Data) and their alerts
// are not stored as real alerts.

// SimTickEvery is how often the Run loop advances the simulation to the
// server clock (it steps at 50 Hz internally).
const SimTickEvery = 50 * time.Millisecond

// SimStart is POST /api/sim/start.
//
// Realism makes the simulated phones as messy as real ones (see
// crowdsim/realism.go): "ideal" (the default: upright on the chest, exact
// position, nothing lost), "realistic" (every imperfection at strength 1)
// or "harsh" (strength 2). Imperfections overrides single strengths
// (0–3, 0 = off) on top of the preset, e.g. {"realism": "ideal",
// "imperfections": {"gps": 1}} is GPS error alone.
type SimStart struct {
	People        int               `json:"people"`
	Participation float64           `json:"participation"`
	Scenario      string            `json:"scenario"`
	Seed          int64             `json:"seed,omitempty"` // 0 = random
	Realism       string            `json:"realism,omitempty"`
	Imperfections *SimImperfections `json:"imperfections,omitempty"`
	// WalkIn: the venue starts empty and everyone comes in at the entry
	// spot of the position estimator (PUT /api/locate) over WalkInS
	// seconds (0 = 60), as with one shared QR code at the door.
	WalkIn  bool    `json:"walkIn,omitempty"`
	WalkInS float64 `json:"walkInS,omitempty"`
}

// SimImperfections are per-imperfection strengths; nil = the preset's.
type SimImperfections struct {
	GPS     *float64 `json:"gps,omitempty"`
	Carry   *float64 `json:"carry,omitempty"`
	Dropout *float64 `json:"dropout,omitempty"`
	// For the position estimator (crowdsim/heading.go, dr.go): Heading =
	// the phones report a compass heading (strength of its error); NoPos =
	// they never say where they are; PhoneDR = they report their own step
	// count and displacement (a model).
	Heading *float64 `json:"heading,omitempty"`
	PhoneDR *float64 `json:"phoneDR,omitempty"`
	NoPos   *bool    `json:"noPos,omitempty"`
}

// realism resolves the preset and the overrides.
func (r SimStart) realism() (crowdsim.Realism, error) {
	rl, err := crowdsim.RealismPreset(r.Realism)
	if err != nil {
		return rl, err
	}
	if o := r.Imperfections; o != nil {
		for _, f := range []struct {
			v   *float64
			dst *float64
		}{{o.GPS, &rl.GPS}, {o.Carry, &rl.Carry}, {o.Dropout, &rl.Dropout}, {o.Heading, &rl.Heading}, {o.PhoneDR, &rl.PhoneDR}} {
			if f.v != nil {
				*f.dst = *f.v
			}
		}
	}
	if o := r.Imperfections; o != nil && o.NoPos != nil {
		rl.NoPos = *o.NoPos
	}
	return rl, rl.Validate()
}

type simRun struct {
	mu   sync.Mutex // guards w; lock order: simRun.mu, then App.mu
	w    *crowdsim.World
	busy atomic.Bool

	messy bool // phones with imperfections: messages arrive when they arrive

	// Guarded by App.mu.
	p       *pipeline
	startMs int64 // server ms at sim t = 0 (moves forward if the sim skips a stall)
	simT    float64
	alertAt float64 // s since start of Pulse's first red alert; < 0 = none
	frame   protocol.SimFrame
}

var errSimRunning = errors.New("the simulation is already running (POST /api/sim/stop first)")
var errSimStopped = errors.New("the simulation is not running (POST /api/sim/start first)")

// StartSim starts the crowd simulation at the current server time.
func (a *App) StartSim(req SimStart) error { return a.startSimAt(req, hub.Now()) }

func (a *App) startSimAt(req SimStart, now int64) error {
	if req.People == 0 {
		req.People = 250
	}
	if req.Scenario == "" {
		req.Scenario = "concert"
	}
	if req.Participation == 0 {
		req.Participation = 0.6
	}
	if req.Seed == 0 {
		req.Seed = time.Now().UnixNano()
	}
	rl, err := req.realism()
	if err != nil {
		return err
	}
	a.mu.Lock()
	running := a.sim != nil
	cfg := a.liveConfig()
	layout := a.venue.Layout
	bearing, entry := a.locSimSetup(req)
	a.mu.Unlock()
	if running {
		return errSimRunning
	}
	if req.WalkIn && entry == nil {
		return errors.New("walkIn needs an entry spot: PUT /api/locate {on: true, entry: {on: true, x, y}}")
	}
	w, err := crowdsim.New(crowdsim.Config{W: cfg.VenueW, H: cfg.VenueH, People: req.People,
		Participation: req.Participation, Scenario: req.Scenario, Seed: req.Seed, StartMs: now, Layout: layout, Realism: rl, Bearing: bearing, Entry: entry})
	if err != nil {
		return err
	}
	// The operator knows roughly what share of the crowd runs Pulse; the
	// density alerts need it to turn phones/m² into people/m².
	cfg.Participation = req.Participation
	s := &simRun{w: w, p: newPipeline(cfg), startMs: now, alertAt: -1, messy: !rl.Ideal()}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sim != nil {
		return errSimRunning
	}
	a.applyZones(s.p)
	a.locAttach(s.p, true, now)
	a.replay = nil
	a.sim = s
	a.feedSim(s, w.Events(), now)
	s.frame = protocol.SimFrame{Bodies: w.Bodies(), Action: w.Action}
	log.Printf("sim: %d people, %d phones (participation %.2f), scenario %s, phones: gps %.1f carry %.1f dropout %.1f",
		len(w.Agents()), w.Phones(), req.Participation, req.Scenario, rl.GPS, rl.Carry, rl.Dropout)
	return nil
}

// StopSim returns to live data.
func (a *App) StopSim() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sim == nil {
		return errSimStopped
	}
	a.sim = nil
	log.Printf("sim stopped, back to live")
	return nil
}

// SimAction runs a director action on the running simulation
// (POST /api/sim/action). Behaviours: calm (the concert routine: standing,
// some swaying, trips to the bar/toilets/merch, people arriving and
// leaving), stage, surge, attract, disperse, dance (everyone sways to one
// beat with their own delay: the false-positive test) and intermission
// (the music stops, many groups head for the POIs at once and come back).
// Events: shove, exit, spawn. See crowdsim.World.Apply for the fields.
func (a *App) SimAction(act crowdsim.Action) error {
	a.mu.Lock()
	s := a.sim
	a.mu.Unlock()
	if s == nil {
		return errSimStopped
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.w.Apply(act); err != nil {
		return err
	}
	if act.Type == crowdsim.ActDance || act.Type == crowdsim.ActIntermission {
		log.Printf("sim: %s at %.1f s", act.Type, s.w.T)
	}
	return nil
}

// SimStatus is GET /api/sim.
func (a *App) SimStatus() protocol.SimStatus {
	a.mu.Lock()
	s := a.sim
	cfg := a.liveConfig()
	layout := a.venue.Layout
	a.mu.Unlock()
	if s == nil {
		st := protocol.SimStatus{}
		st.Exits, st.Walls = crowdsim.GeometryJSON(crowdsim.LayoutGeometry(cfg.VenueW, cfg.VenueH, layout))
		return st
	}
	s.mu.Lock()
	st := s.w.Status()
	s.mu.Unlock()
	a.mu.Lock()
	alertAt := s.alertAt
	a.mu.Unlock()
	if alertAt >= 0 {
		v := math.Round(alertAt*10) / 10
		st.Truth.AlertAt = &v
		if st.Truth.DangerAt != nil {
			lead := math.Round((*st.Truth.DangerAt-v)*10) / 10
			st.Truth.LeadSeconds = &lead
		}
	}
	return st
}

// simTick advances the simulation to server time now and feeds its phones'
// messages into the sim pipeline.
func (a *App) simTick(now int64) {
	a.mu.Lock()
	s := a.sim
	a.mu.Unlock()
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.w.AdvanceTo(now)
	ev := s.w.Events()
	bodies := s.w.Bodies()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sim != s {
		return
	}
	s.startMs, s.simT = s.w.StartMs, s.w.T
	a.feedSim(s, ev, now)
	s.frame = protocol.SimFrame{Bodies: bodies, T: math.Round(s.w.T*10) / 10, Action: s.w.Action, Loc: a.locSimError(s, now)}
}

// feedSim delivers simulated phone messages exactly as the hub delivers
// real ones. Caller holds mu.
func (a *App) feedSim(s *simRun, ev []crowdsim.Event, now int64) {
	for _, e := range ev {
		switch e.Kind {
		case crowdsim.EvHello:
			if e.Auto {
				// A GPS phone's hello has no position: it stays unplaced
				// (counting toward nothing) until its first usable fix,
				// exactly as a live one does (unplaced.go).
				a.helloUnplacedIn(s.p, now, e.ID, "sim")
				break
			}
			a.helloIn(s.p, now, e.ID, e.X, e.Y, "sim")
		case crowdsim.EvSync:
			a.syncIn(s.p, now, e.ID, e.Offset, e.RTT)
		case crowdsim.EvPos:
			a.posIn(s.p, now, e.ID, e.X, e.Y)
		case crowdsim.EvGPS:
			a.simGPSIn(s.p, now, e.ID, e.X, e.Y, e.Acc)
		case crowdsim.EvMotion:
			// recv = when the summary would have arrived: its own time
			// for an ideal phone; a messy one's arrive late and in clumps.
			recv := e.M.T
			if s.messy {
				recv = now
			}
			if _, _, _, ok := a.motionIn(s.p, e.ID, e.M, recv); ok {
				a.simMsgs++
			}
		case crowdsim.EvDR:
			if l := s.p.loc; l != nil {
				l.est.DR(e.ID, now, e.Steps, e.East, e.North)
			}
		case crowdsim.EvGone:
			a.goneIn(s.p, now, e.ID)
		}
	}
}

// simGPSIn is a simulated phone's GPS-like fix, already in venue metres
// (the sim has no latitude or longitude). From there it is treated exactly
// as PhoneGPS treats a live fix: dropped when its accuracy is worse than
// gpsMaxAcc, smoothed by accuracy (geo.Smoother), flagged outside and
// clamped when it falls off the venue, so the node shows src "gps" and its
// acc. Caller holds mu.
func (a *App) simGPSIn(p *pipeline, now int64, id string, x, y, acc float64) {
	m := p.meta[id]
	if m == nil {
		return
	}
	cfg := p.cfg()
	if !(acc > 0 && acc <= cfg.GPSMaxAcc) {
		return
	}
	if a.locGPS(p, now, id, m, x, y, acc) {
		return // the position estimator takes the fix as it is (locate.go)
	}
	x, y = m.gps.Add(x, y, acc)
	m.acc = math.Max(0.1, math.Round(m.gps.Acc*10)/10)
	m.x, m.y, m.outside = cfg.Place(x, y, m.acc) // as gpsLocked
	if a.placedLocked(p, now, id, m) {
		return // its first fix: recorded as its hello
	}
	p.place(id, m)
	a.record(store.Record{K: store.KindPos, T: now, ID: id, X: store.F(r2(m.x)), Y: store.F(r2(m.y)), Acc: m.acc, Out: m.outside})
}

// simSeconds converts server time to sim time. Caller holds mu.
func (s *simRun) seconds(now int64) float64 { return float64(now-s.startMs) / 1000 }
