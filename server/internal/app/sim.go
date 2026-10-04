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
type SimStart struct {
	People        int     `json:"people"`
	Participation float64 `json:"participation"`
	Scenario      string  `json:"scenario"`
	Seed          int64   `json:"seed,omitempty"` // 0 = random
}

type simRun struct {
	mu   sync.Mutex // guards w; lock order: simRun.mu, then App.mu
	w    *crowdsim.World
	busy atomic.Bool

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
	a.mu.Lock()
	running := a.sim != nil
	cfg := a.liveConfig()
	layout := a.venue.Layout
	a.mu.Unlock()
	if running {
		return errSimRunning
	}
	w, err := crowdsim.New(crowdsim.Config{W: cfg.VenueW, H: cfg.VenueH, People: req.People,
		Participation: req.Participation, Scenario: req.Scenario, Seed: req.Seed, StartMs: now, Layout: layout})
	if err != nil {
		return err
	}
	// The operator knows roughly what share of the crowd runs Pulse; the
	// density alerts need it to turn phones/m² into people/m².
	cfg.Participation = req.Participation
	s := &simRun{w: w, p: newPipeline(cfg), startMs: now, alertAt: -1}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sim != nil {
		return errSimRunning
	}
	a.applyZones(s.p)
	a.replay = nil
	a.sim = s
	a.feedSim(s, w.Events(), now)
	s.frame = protocol.SimFrame{Bodies: w.Bodies(), Action: w.Action}
	log.Printf("sim: %d people, %d phones (participation %.2f), scenario %s", len(w.Agents()), w.Phones(), req.Participation, req.Scenario)
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
	s.frame = protocol.SimFrame{Bodies: bodies, T: math.Round(s.w.T*10) / 10, Action: s.w.Action}
}

// feedSim delivers simulated phone messages exactly as the hub delivers
// real ones. Caller holds mu.
func (a *App) feedSim(s *simRun, ev []crowdsim.Event, now int64) {
	for _, e := range ev {
		switch e.Kind {
		case crowdsim.EvHello:
			a.helloIn(s.p, now, e.ID, e.X, e.Y, "sim")
		case crowdsim.EvSync:
			a.syncIn(s.p, now, e.ID, e.Offset, e.RTT)
		case crowdsim.EvPos:
			a.posIn(s.p, now, e.ID, e.X, e.Y)
		case crowdsim.EvMotion:
			// recv = when the summary would have arrived: its own time.
			if _, _, _, ok := a.motionIn(s.p, e.ID, e.M, e.M.T); ok {
				a.simMsgs++
			}
		case crowdsim.EvGone:
			a.goneIn(s.p, now, e.ID)
		}
	}
}

// simSeconds converts server time to sim time. Caller holds mu.
func (s *simRun) seconds(now int64) float64 { return float64(now-s.startMs) / 1000 }
