package app

import (
	"encoding/json"
	"errors"
	"log"
	"math"
	"net/http"
	"os"
	"reflect"
	"sort"
	"time"

	"github.com/Drivera0/crowd-crush/server/internal/crowdsim"
	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/geo"
	"github.com/Drivera0/crowd-crush/server/internal/hub"
	"github.com/Drivera0/crowd-crush/server/internal/locate"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// The position estimator in the pipeline (internal/locate).
//
// Everyone joins through one shared QR code and walks off, so where a
// phone is has to be worked out. With the estimator on (the default;
// PULSE_LOCATE=0 or PUT /api/locate turns it off), the live pipeline and
// the crowd simulation's each have one, and its answer is the position the
// pipeline uses: nodeMeta.x/y/acc are the estimate, so zones, clusters,
// density, neighbours, guidance and the phones' own maps all follow it. A
// replay has none: it shows the positions that were recorded.
//
// What goes in:
//
//   - a hello without a position: the phone starts at the entry spot, if
//     staff set one (PUT /api/locate), else it stays unplaced as before;
//   - every position the existing paths set by hand (a tap on the phone's
//     map, staff dragging the dot, the demo spot's row, a tower check-in):
//     an exact fix. The estimator returns exactly that position until the
//     phone is seen to walk, so the line demo is untouched;
//   - GPS fixes, in venue metres as before, raw: the estimator carries the
//     GPS bias itself, so the accuracy-weighted smoothing and the tower's
//     fading correction are not applied on this path;
//   - every motion summary, with its compass heading if it has one, and
//     the phones' own dead reckoning ("dr");
//   - the phones' mesh reports (which peers move with them) and Bluetooth
//     beacon fixes, as measurements with their own uncertainty.
//
// What comes out, every detector step: each phone's position, its
// uncertainty (as the 68 % radius the detector and the density tracker
// take for a GPS accuracy; 0 when it is as good as placed by hand) and
// whether it is lost (too vague to count toward density). The raw source
// position stays in nodeMeta.loc for the dashboard's raw/estimated toggle.
//
// Latitude and longitude never reach the estimator: fixes are converted to
// venue metres first, as before.

// locMeta is what the estimator keeps on a phone's nodeMeta.
type locMeta struct {
	on         bool // the estimator places this phone
	x, y       float64
	src        int
	lost       bool
	rawX, rawY float64 // where the pipeline would have put it without the estimator
	rawAcc     float64
	rawSet     bool
	gps        geo.Smoother // … for which GPS fixes are smoothed as before
	beaconAt   int64        // last beacon fix fed
}

// locState is one pipeline's estimator.
type locState struct {
	a    *App
	est  *locate.Estimator
	cfg  locate.Config
	sim  bool
	now  int64
	busy bool // the estimator is writing positions: place() is its own
	dur  []stepTime
	err  *protocol.LocError
	errT int64
	fed  map[string]int64 // mesh report already given to the estimator, by phone
}

// locInit loads data/locate.json. The estimator is on unless that file,
// Options.NoLocate or PULSE_LOCATE=0 says otherwise.
func (a *App) locInit() {
	c := protocol.LocateConfig{On: !a.opt.NoLocate}
	switch os.Getenv("PULSE_LOCATE") {
	case "0", "off", "false":
		c.On = false
	}
	var saved protocol.LocateConfig
	if a.load("locate.json", &saved) {
		if err := a.validLocate(&saved); err == nil {
			c = saved
		} else {
			log.Printf("locate.json: %v (ignored)", err)
		}
	}
	a.locCfg = c
}

func (a *App) validLocate(c *protocol.LocateConfig) error {
	e := &c.Entry
	if !finite(e.X) || !finite(e.Y) || !finite(e.Sigma) || e.Sigma < 0 || e.Sigma > 20 {
		return errors.New("entry: want {on, x, y, sigma}: x, y in venue metres, sigma 0–20 m (0 = 1.5)")
	}
	e.X, e.Y = a.liveConfig().Clamp(e.X, e.Y)
	e.X, e.Y = r2(e.X), r2(e.Y)
	if c.Bearing != nil {
		if !finite(*c.Bearing) {
			return errors.New("bearing must be a number of degrees")
		}
		b := geo.NormBearing(*c.Bearing)
		c.Bearing = &b
	}
	return nil
}

// locAttach gives a pipeline its estimator (or takes it away when the
// estimator is off). Phones the pipeline already holds are handed over
// where they are. Caller holds mu.
func (a *App) locAttach(p *pipeline, sim bool, now int64) {
	if !a.locCfg.On {
		p.loc = nil
		for _, m := range p.meta {
			m.loc = locMeta{}
		}
		return
	}
	if l := p.loc; l != nil {
		l.cfg = a.locConfigFor(p, sim) // the settings may have changed
		l.est.SetConfig(l.cfg)
		return
	}
	l := &locState{a: a, sim: sim, now: now, fed: map[string]int64{}}
	l.cfg = a.locConfigFor(p, sim)
	l.est = locate.New(l.cfg)
	p.loc = l
	for id, m := range p.meta {
		switch {
		case m.unplaced:
			l.est.Join(id, now)
		case m.acc > 0:
			l.est.GPS(id, now, m.x, m.y, m.acc)
			m.loc.rawX, m.loc.rawY, m.loc.rawAcc, m.loc.rawSet = m.x, m.y, m.acc, true
		default:
			l.est.Fix(id, now, locate.Fix{X: m.x, Y: m.y, Exact: true})
			m.loc.rawX, m.loc.rawY, m.loc.rawAcc, m.loc.rawSet = m.x, m.y, 0, true
		}
	}
}

// locConfigFor is the estimator's configuration for a pipeline: the venue,
// its walls and stage, the entry spot and the bearing. Caller holds mu.
func (a *App) locConfigFor(p *pipeline, sim bool) locate.Config {
	dc := p.cfg()
	c := locate.DefaultConfig(dc.VenueW, dc.VenueH)
	c.GPSMaxAcc, c.HandlingRot = dc.GPSMaxAcc, dc.HandlingRot
	switch {
	case a.venue.Geo:
		c.Bearing, c.HasBearing = a.venue.Bearing, true
	case a.locCfg.Bearing != nil:
		c.Bearing, c.HasBearing = *a.locCfg.Bearing, true
	}
	if sim && !c.HasBearing {
		c.HasBearing = true // the simulated compasses are simulated against bearing 0
	}
	if e := a.locCfg.Entry; e.On {
		c.Entry = locate.Entry{On: true, X: e.X, Y: e.Y, Sigma: e.Sigma}
	}
	// Walls with the exits cut out of them, as the simulation builds them
	// from the venue layout. Without a layout a live venue has no walls;
	// the simulation has its default stage pit.
	lay := a.venue.Layout
	custom := lay != nil && (len(lay.Walls) > 0 || len(lay.Exits) > 0 || len(lay.Stage) >= 3)
	if custom || sim {
		g := crowdsim.LayoutGeometry(dc.VenueW, dc.VenueH, lay)
		_, c.Walls = crowdsim.GeometryJSON(g)
		c.Stage = g.StageOutline()
	}
	return c
}

// locSimSetup is what a simulation takes from the estimator's settings:
// the bearing its phones' compasses are simulated against, and the entry
// spot when everyone is to walk in from it (nil = not asked for, or none
// set). Caller holds mu.
func (a *App) locSimSetup(req SimStart) (bearing float64, entry *crowdsim.Entry) {
	switch {
	case a.venue.Geo:
		bearing = a.venue.Bearing
	case a.locCfg.Bearing != nil:
		bearing = *a.locCfg.Bearing
	}
	if e := a.locCfg.Entry; req.WalkIn && e.On {
		entry = &crowdsim.Entry{X: e.X, Y: e.Y, Over: req.WalkInS}
	}
	return bearing, entry
}

// placed is the hook in pipeline.place: an existing path just put a phone
// somewhere (or left it unplaced). m.lastRecv is the best clock at hand.
func (l *locState) placed(id string, m *nodeMeta) {
	if l.busy {
		return
	}
	now := max(l.now, m.lastRecv)
	switch {
	case m.unplaced:
		l.est.Join(id, now)
	case m.loc.on && m.x == m.loc.x && m.y == m.loc.y:
		// Put back where the estimator had it (the phone said hello again):
		// its estimate stands.
		l.est.Back(id, now)
	case m.bcn.owns(m):
		l.feedBeacon(id, m, now)
		m.loc.rawX, m.loc.rawY, m.loc.rawAcc, m.loc.rawSet = m.x, m.y, m.bcn.fix.Acc, true
	case m.acc > 0:
		// A position with an accuracy that didn't come through locGPS (a
		// recording being replayed into this pipeline): a GPS-like fix.
		l.est.GPS(id, now, m.x, m.y, m.acc)
		m.loc.rawX, m.loc.rawY, m.loc.rawAcc, m.loc.rawSet = m.x, m.y, m.acc, true
	default:
		l.est.Fix(id, now, locate.Fix{X: m.x, Y: m.y, Exact: true})
		m.loc.rawX, m.loc.rawY, m.loc.rawAcc, m.loc.rawSet = m.x, m.y, 0, true
		m.loc.gps.Reset()
	}
}

// feedBeacon gives the estimator a phone's Bluetooth beacon fix: good
// along the line through two boards and poor across it (dims 1), or round.
func (l *locState) feedBeacon(id string, m *nodeMeta, now int64) {
	f := m.bcn.fix
	if !f.OK || f.Dims == 0 {
		return
	}
	fx := locate.Fix{X: f.X, Y: f.Y, Along: f.Acc, Across: f.Acc, Src: locate.SrcBeacon}
	if f.Dims == 1 && f.Axis != nil && f.Along > 0 && f.Cross > 0 {
		fx.Along, fx.Across, fx.AX, fx.AY = f.Along, f.Cross, f.Axis[0], f.Axis[1]
	}
	// A signal-strength fix is off by the same amount for as long as the
	// person stands in the same spot: successive fixes are one measurement,
	// not many. Fed every other second at twice its variance.
	fx.Along *= math.Sqrt2
	fx.Across *= math.Sqrt2
	l.est.Fix(id, now, fx)
	m.loc.beaconAt = now
}

// locGPS takes a GPS fix (venue metres, already gated by accuracy) for the
// estimator. It reports false when the pipeline has none and the caller
// should carry on as before. Caller holds mu.
func (a *App) locGPS(p *pipeline, now int64, id string, m *nodeMeta, x, y, acc float64) bool {
	l := p.loc
	if l == nil {
		return false
	}
	l.est.GPS(id, now, x, y, acc)
	// The raw position, as the pipeline would have computed it.
	rx, ry := m.loc.gps.Add(x, y, acc)
	ra := math.Max(0.1, math.Round(m.loc.gps.Acc*10)/10)
	m.loc.rawX, m.loc.rawY, _ = p.cfg().Place(rx, ry, ra)
	m.loc.rawAcc, m.loc.rawSet = ra, true
	return true
}

// locMotion gives the estimator a motion summary. Caller holds mu.
func (a *App) locMotion(p *pipeline, id string, mo protocol.Motion) {
	if p.loc == nil {
		return
	}
	s := locate.Sample{T: mo.T, AX: mo.AX, AY: mo.AY, AZ: mo.AZ, Rot: mo.Rot, G: detect.Gravity(mo.G),
		HD: locate.NoHeading, HB: locate.NoHeading}
	if mo.HD != nil && finite(*mo.HD) {
		s.HD = *mo.HD
	}
	if mo.HB != nil && finite(*mo.HB) {
		s.HB = *mo.HB
	}
	p.loc.est.Motion(id, s)
}

// PhoneDR is a phone's own dead reckoning (hub.LocateHandler).
func (a *App) PhoneDR(id string, d protocol.DR) {
	now := hub.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if l := a.live.loc; l != nil && a.live.meta[id] != nil {
		l.est.DR(id, now, d.Steps, d.E, d.N)
	}
}

// accOf is the accuracy the pipeline is told for an estimate with 1-σ
// radius sigma: 0 (exact) when it is as good as placed by hand, else the
// 68 % radius, the unit of a GPS accuracy.
func accOf(sigma float64) float64 {
	if sigma < locate.ExactBelow {
		return 0
	}
	return math.Round(locate.AccRadius(sigma)*10) / 10
}

// step runs the estimator and writes its positions into the pipeline.
// Called at the top of pipeline.step; the caller holds App.mu.
func (l *locState) step(p *pipeline, now int64) {
	t0 := time.Now()
	a := l.a
	l.now = now
	if c := a.locConfigFor(p, l.sim); !reflect.DeepEqual(c, l.cfg) {
		l.cfg = c
		l.est.SetConfig(c)
	}
	for id, m := range p.meta {
		if !m.connected {
			l.est.Gone(id, m.goneAt)
		} else {
			l.est.Back(id, now)
		}
		// A beacon fix that isn't what places the phone is one more
		// measurement.
		if !l.sim && m.bcn.at > 0 && now-m.bcn.at <= beaconStaleMs && m.bcn.fix.OK && !m.bcn.owns(m) && now-m.loc.beaconAt >= 2000 {
			l.feedBeacon(id, m, now)
		}
	}
	l.est.Prune(func(id string) bool { return p.meta[id] != nil })
	if !l.sim {
		// What the phones' mesh links report (mesh.go).
		ms := a.ms()
		for id, r := range ms.near {
			if now-r.at > nearFreshMs || p.meta[id] == nil || l.fed[id] == r.at {
				continue
			}
			peers := make([]locate.Peer, 0, len(r.n.Peers))
			for _, pr := range r.n.Peers {
				peers = append(peers, locate.Peer{ID: pr.ID, Corr: pr.Corr, LagMs: pr.LagMs, Hops: pr.Hops})
			}
			l.est.Near(id, now, peers)
			l.fed[id] = r.at
		}
	}
	cfg := p.cfg()
	l.busy = true
	for _, e := range l.est.Step(now, nil) {
		m := p.meta[e.ID]
		if m == nil {
			continue
		}
		x, y := cfg.Clamp(e.X, e.Y)
		m.x, m.y, m.acc, m.outside = x, y, accOf(e.Acc), false
		m.loc.on, m.loc.x, m.loc.y, m.loc.src, m.loc.lost = true, x, y, e.Src, e.Lost
		if !a.placedLocked(p, now, e.ID, m) { // its first position: recorded as its hello
			p.det.SetPhone(e.ID, x, y)
			p.det.SetAccuracy(e.ID, m.acc)
			p.det.SetOutside(e.ID, false)
		}
	}
	l.busy = false
	l.dur = append(l.dur, stepTime{now, time.Since(t0).Nanoseconds()})
	cut := 0
	for cut < len(l.dur) && l.dur[cut].t <= now-statWindowMs {
		cut++
	}
	l.dur = append(l.dur[:0], l.dur[cut:]...)
}

func (l *locState) stepMs() float64 {
	if len(l.dur) == 0 {
		return 0
	}
	var sum int64
	for _, s := range l.dur {
		sum += s.ns
	}
	return round2(float64(sum) / float64(len(l.dur)) / 1e6)
}

// locSrcNames are the wire names of locate's source bits, lowest first.
var locSrcNames = []string{protocol.LocEntry, protocol.LocGPS, protocol.LocSteps, protocol.LocFix,
	protocol.LocBeacon, protocol.LocMesh, protocol.LocNear, protocol.LocMap}

// locNode fills a snapshot node's estimator fields.
func locNode(m *nodeMeta, n *protocol.Node) {
	if !m.loc.on {
		return
	}
	for i, name := range locSrcNames {
		if m.loc.src&(1<<i) != 0 {
			n.Loc = append(n.Loc, name)
		}
	}
	n.Lost = m.loc.lost
	if m.loc.rawSet && (m.loc.rawX != m.x || m.loc.rawY != m.y) {
		n.Raw = &protocol.Point{r2(m.loc.rawX), r2(m.loc.rawY)}
	}
	if m.acc > 0 && m.loc.src&locate.SrcGPS == 0 {
		n.Src = protocol.SrcEst // placed by the estimator without GPS (entry spot, steps, neighbours)
	}
}

// locSimError measures the simulated phones' positions against where
// their owners stand, once a second. Caller holds simRun.mu and App.mu.
func (a *App) locSimError(s *simRun, now int64) *protocol.LocError {
	l := s.p.loc
	if l == nil {
		return nil
	}
	if l.err != nil && now-l.errT < 1000 {
		return l.err
	}
	var errs []float64
	e := &protocol.LocError{}
	var rawSum float64
	for _, ag := range s.w.Agents() {
		m := s.p.meta[ag.PhoneID()]
		if m == nil || !m.connected {
			continue
		}
		if m.loc.rawSet {
			e.RawN++
			rawSum += math.Hypot(m.loc.rawX-ag.X, m.loc.rawY-ag.Y)
		}
		if m.unplaced {
			continue
		}
		if m.loc.lost {
			e.Lost++
			continue
		}
		errs = append(errs, math.Hypot(m.x-ag.X, m.y-ag.Y))
	}
	if e.RawN > 0 {
		e.RawMean = round2(rawSum / float64(e.RawN))
	}
	if e.N = len(errs); e.N > 0 {
		sort.Float64s(errs)
		sum := 0.0
		for _, v := range errs {
			sum += v
		}
		e.Mean, e.Median, e.P95 = round2(sum/float64(e.N)), round2(errs[e.N/2]), round2(errs[min(e.N-1, e.N*95/100)])
	}
	l.err, l.errT = e, now
	return e
}

// Locate is GET /api/locate.
func (a *App) Locate() protocol.LocateStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := protocol.LocateStatus{LocateConfig: a.locCfg}
	if l := a.active().loc; l != nil {
		s := l.est.Stats()
		st.Phones, st.Located, st.Walking, st.Links, st.StepMs = s.Phones, s.Located, s.Walking, s.Links, l.stepMs()
		if a.sim != nil {
			st.Error = l.err
		}
	}
	return st
}

// SetLocate is PUT /api/locate: the switch, the entry spot, the bearing.
func (a *App) SetLocate(c protocol.LocateConfig) (protocol.LocateStatus, error) {
	now := hub.Now()
	a.mu.Lock()
	if err := a.validLocate(&c); err != nil {
		a.mu.Unlock()
		return protocol.LocateStatus{}, err
	}
	a.locCfg = c
	if err := a.save("locate.json", c); err != nil {
		log.Printf("locate: %v", err)
	}
	a.locAttach(a.live, false, now)
	if a.sim != nil {
		a.locAttach(a.sim.p, true, now)
	}
	a.mu.Unlock()
	return a.Locate(), nil
}

func (a *App) locateRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/locate", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, a.Locate())
	})
	mux.HandleFunc("PUT /api/locate", func(w http.ResponseWriter, r *http.Request) {
		var c protocol.LocateConfig
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&c); err != nil {
			httpError(w, errors.New("want {on, entry: {on, x, y, sigma}, bearing}"), http.StatusBadRequest)
			return
		}
		st, err := a.SetLocate(c)
		if err != nil {
			httpError(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, st)
	})
}
