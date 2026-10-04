package app

import (
	"errors"
	"log"
	"math"

	"github.com/Drivera0/crowd-crush/server/internal/crowdsim"
	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/hub"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// Hybrid: real phones inside the simulated crowd.
//
// While a simulation runs, every connected real phone also exists in the
// simulated world at its real venue position:
//
//   - as a pinned body (crowdsim.Pin) the simulated people walk around and
//     press against;
//   - as a node of the sim pipeline (real: true on the dashboard), fed the
//     phone's own motion next to the simulated phones', so it is part of
//     the sim's zones, clusters, neighbour pairs and guidance.
//
// Its state message then comes from the sim pipeline (sim: true): when the
// simulated crowd surges around the dot, the phone goes red and shows the
// way out. The live pipeline keeps running underneath on the same
// readings, and takes over again the moment the simulation stops.
//
// hybridSync runs before every simulation tick of the Run loop. With no
// real phone connected it does nothing, and the simulation is exactly what
// it was without it.

// hybridSync copies the connected live phones into the running simulation
// and runs the surge director. No-op when no simulation runs.
func (a *App) hybridSync(now int64) {
	a.mu.Lock()
	s := a.sim
	if s == nil {
		a.surge = nil
		a.mu.Unlock()
		return
	}
	pins := a.hybridSyncLocked(s, now)
	press, end := a.surgeStepLocked(s, now)
	a.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(pins) > 0 || len(s.w.Pins()) > 0 {
		s.w.SetPins(pins)
	}
	switch {
	case press != nil && s.w.Action != crowdsim.ActAttract:
		// Staff gave the crowd another behaviour: the surge is over.
		s.w.SetPress(nil)
		a.mu.Lock()
		a.surge = nil
		a.mu.Unlock()
	case press != nil:
		s.w.SetPress(press)
	case end:
		s.w.SetPress(nil)
	}
}

// hybridSyncLocked mirrors the live phones into the sim pipeline and
// returns them as pinned bodies. Caller holds mu.
func (a *App) hybridSyncLocked(s *simRun, now int64) []crowdsim.Pin {
	var pins []crowdsim.Pin
	for id, m := range a.live.meta {
		sm := s.p.meta[id]
		if sm != nil && !sm.real {
			continue // a simulated phone with the same id: leave it alone
		}
		if !m.connected {
			if sm != nil && sm.connected {
				sm.connected, sm.goneAt = false, now
			}
			continue
		}
		fresh := sm == nil
		if fresh {
			sm = &nodeMeta{real: true, joinedAt: m.joinedAt, lastRecv: m.lastRecv}
			s.p.meta[id] = sm
		}
		sm.connected, sm.goneAt = true, 0
		sm.ua, sm.name, sm.color = m.ua, m.name, m.color
		sm.synced, sm.rtt, sm.offset = m.synced, m.rtt, m.offset
		sm.shake.until = m.shake.until
		if fresh || sm.x != m.x || sm.y != m.y || sm.outside != m.outside || sm.acc != m.acc || sm.unplaced != m.unplaced {
			sm.x, sm.y, sm.acc, sm.outside, sm.unplaced = m.x, m.y, m.acc, m.outside, m.unplaced
			s.p.place(id, sm)
		}
		if !m.outside && !m.unplaced {
			pins = append(pins, crowdsim.Pin{ID: id, X: m.x, Y: m.y})
		}
	}
	return pins
}

// hybridMotionLocked gives a real phone's reading to the sim pipeline too.
// Caller holds mu.
func (a *App) hybridMotionLocked(id string, mo protocol.Motion, recv int64, rot float64) {
	if a.sim == nil {
		return
	}
	p := a.sim.p
	sm := p.meta[id]
	if sm == nil || !sm.real {
		return
	}
	sm.lastRecv = recv
	sm.addSample(protocol.Sample{T: mo.T, AX: mo.AX, AY: mo.AY, AZ: mo.AZ, Rot: mo.Rot})
	p.det.Add(id, detect.Sample{T: mo.T, AX: mo.AX, AY: mo.AY, AZ: mo.AZ, Rot: rot, G: detect.Gravity(mo.G)})
}

// ---- "Surge around the phones" ----

// The demo's one click: a simulated crowd closes in on the real phones.
// It starts the simulation if none runs, sends the crowd to the phones'
// centroid (attract) and then has everyone around it lean in harder and
// harder (crowdsim.Press), swelling and easing like a crowd pushing in
// waves, so a crush forms where the judges' dots are. Nothing is faked
// downstream: the detector, alerts, briefing, voice and sign react to it
// as they would to any simulation. It ends after surgeForS, or as soon as
// staff give the simulated crowd another behaviour (calm, disperse, …).

const (
	surgePeople     = 220
	surgePressFromS = 5.0  // s after the click: the crowd that gathered starts leaning in
	surgeRampS      = 10.0 // s for the press to reach full strength
	surgePressAcc   = 8.0  // m/s² per person at full strength (about 560 N for 70 kg: a hard, sustained push)
	surgePressR     = 6.0  // m around the phones
	surgeSwell      = 0.15 // the press swells and eases by this fraction…
	surgeSwellS     = 2.5  // …every this many seconds
	surgeForS       = 75.0 // the director lets go after this long
)

type surgeDirector struct {
	s       *simRun
	x, y    float64
	start   int64
	pressed bool // the press was on at the last step
}

// SurgeResult is POST /api/sim/surge-phones.
type SurgeResult struct {
	Mode    string  `json:"mode"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Phones  int     `json:"phones"`  // real phones the crowd closes in on
	Started bool    `json:"started"` // the simulation was started by this call
}

// ErrNoPhones: nothing to surge around.
var ErrNoPhones = errors.New("no phones are connected: ask someone to join first (or turn on the demo spot to surge there)")

// SurgePhones is POST /api/sim/surge-phones.
func (a *App) SurgePhones() (SurgeResult, error) { return a.surgePhonesAt(hub.Now(), 0) }

func (a *App) surgePhonesAt(now, seed int64) (SurgeResult, error) {
	a.mu.Lock()
	var x, y float64
	n := 0
	for _, m := range a.live.meta {
		if m.connected && !m.outside && !m.unplaced {
			x, y, n = x+m.x, y+m.y, n+1
		}
	}
	switch {
	case n > 0:
		x, y = x/float64(n), y/float64(n)
	case a.demo.On:
		x, y = a.demo.X, a.demo.Y
	default:
		a.mu.Unlock()
		return SurgeResult{}, ErrNoPhones
	}
	running := a.sim != nil
	a.mu.Unlock()
	res := SurgeResult{Mode: "sim", X: r2(x), Y: r2(y), Phones: n}
	if !running {
		err := a.startSimAt(SimStart{People: surgePeople, Seed: seed}, now)
		if err != nil && !errors.Is(err, errSimRunning) {
			return SurgeResult{}, err
		}
		res.Started = err == nil
	}
	a.hybridSync(now)
	if err := a.SimAction(crowdsim.Action{Type: crowdsim.ActAttract, X: &x, Y: &y}); err != nil {
		return SurgeResult{}, err
	}
	a.mu.Lock()
	if a.sim != nil {
		a.surge = &surgeDirector{s: a.sim, x: x, y: y, start: now}
	}
	a.mu.Unlock()
	log.Printf("sim: surge around %d phone(s) at %.1f, %.1f m", n, x, y)
	return res, nil
}

// surgeStepLocked is the press the director wants now: nil = none, and
// end = it just finished (take the press off). Caller holds mu.
func (a *App) surgeStepLocked(s *simRun, now int64) (press *crowdsim.Press, end bool) {
	d := a.surge
	if d == nil {
		return nil, false
	}
	t := float64(now-d.start) / 1000
	if d.s != s || t > surgeForS {
		a.surge = nil
		return nil, d.s == s
	}
	if t < surgePressFromS {
		return nil, false
	}
	t -= surgePressFromS
	acc := surgePressAcc * math.Min(1, t/surgeRampS) * (1 + surgeSwell*math.Sin(2*math.Pi*t/surgeSwellS))
	d.pressed = true
	return &crowdsim.Press{X: d.x, Y: d.y, R: surgePressR, Acc: acc}, false
}

// ---- phone states ----

// clusterLevels is the level of the yellow or red cluster each phone is a
// member of.
func clusterLevels(p *pipeline) map[string]string {
	out := map[string]string{}
	for _, c := range p.clusters {
		if c.Level != protocol.LevelYellow && c.Level != protocol.LevelRed {
			continue
		}
		for _, id := range c.Members {
			if levelRank[c.Level] > levelRank[out[id]] {
				out[id] = c.Level
			}
		}
	}
	return out
}

// statesFrom builds the state of every connected live phone that pipeline p
// has a result for. sim marks states that come from the simulation. Caller
// holds mu.
func (a *App) statesFrom(p *pipeline, sim bool, out map[string]protocol.PhoneState) {
	cfg := a.liveConfig()
	var bearing *float64
	if a.venue.Geo {
		b := a.venue.Bearing
		bearing = &b
	}
	zoneLevel := map[string]string{}
	for _, z := range p.last.Zones {
		zoneLevel[z.ID] = p.zoneLevel(z)
	}
	inCluster := clusterLevels(p)
	for _, pr := range p.last.Phones {
		m := a.live.meta[pr.ID]
		if m == nil || !m.connected {
			continue
		}
		if pm := p.meta[pr.ID]; sim && (pm == nil || !pm.real) {
			continue
		}
		node := pr.Status
		if !m.synced {
			node = protocol.StatusConnecting
		}
		level := protocol.LevelCalm
		if !m.outside && !m.unplaced {
			for _, z := range p.det.ZonesOf(m.x, m.y) {
				if levelRank[zoneLevel[z]] > levelRank[level] {
					level = zoneLevel[z]
				}
			}
			// Standing in a packed cluster is danger too, whatever the
			// zone as a whole says.
			if l := inCluster[pr.ID]; levelRank[l] > levelRank[level] {
				level = l
			}
		}
		out[pr.ID] = protocol.PhoneState{Type: protocol.TypeState, Node: node, Zone: level, Move: moveFor(p, pr.ID),
			Bearing: bearing, X: math.Round(m.x*10) / 10, Y: math.Round(m.y*10) / 10, W: cfg.VenueW, H: cfg.VenueH,
			Name: m.name, Color: m.color, Sim: sim}
	}
}
