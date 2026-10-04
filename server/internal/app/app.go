// Package app is the pipeline between the hub and everything else: it feeds
// readings to the detector, turns level changes into alerts (briefing, voice,
// sign, storage), records runs, replays them, and builds dashboard snapshots.
//
// Phones are free points in the venue, in metres (origin top-left of the
// venue map, x right, y down). They are placed by hand (hello x/y, pos) or
// by GPS, converted to metres on arrival with the venue's geo-anchor; raw
// coordinates are never stored, logged or sent to the dashboard.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/Drivera0/crowd-crush/server/internal/brief"
	"github.com/Drivera0/crowd-crush/server/internal/crowd"
	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/geo"
	"github.com/Drivera0/crowd-crush/server/internal/hub"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/sign"
	"github.com/Drivera0/crowd-crush/server/internal/store"
	"github.com/Drivera0/crowd-crush/server/internal/voice"
)

// Timing.
const (
	DetectEvery     = 250 * time.Millisecond
	SnapshotEvery   = 100 * time.Millisecond
	PhoneStateEvery = 500 * time.Millisecond
	ForgetAfterMs   = 30_000 // drop disconnected phones from the map
	BriefCooldown   = 30_000 // max one briefing per zone per 30 s
	histKeep        = 600    // 10 min of 1 Hz zone history
	alertLogKeep    = 50
)

// Options wires in the detector config and the (optional) services.
type Options struct {
	Detect        detect.Config
	RecordingsDir string
	// DataDir holds areas.json and venue.json (staff-drawn areas and the
	// venue's size and geo-anchor). Empty = nothing is persisted.
	DataDir string
	// Venue is the initial venue (size and anchor) when DataDir has no
	// venue.json; zero W/H take the detector config's venue size.
	Venue protocol.Venue
	Sink  store.Sink   // continuous storage; nil = discard
	Tiger *store.Tiger // for labelled runs + DB replay; may be nil
	Brief *brief.Client
	Voice *voice.Client
	Sign  *sign.Client
}

type nodeMeta struct {
	x, y      float64
	acc       float64 // GPS accuracy (m); 0 = placed by hand
	outside   bool    // GPS fix outside the venue
	gps       geo.Smoother
	gpsWarned bool // told the log once that GPS is ignored (no anchor)
	ua        string
	synced    bool
	rtt       int64
	offset    int64
	lastRecv  int64
	connected bool
	goneAt    int64
	joinedAt  int64
	msgs      int64
	tele      []protocol.Sample // last teleKeep samples, for the dashboard's node panel
}

func (m *nodeMeta) src() string {
	if m.acc > 0 {
		return protocol.SrcGPS
	}
	return protocol.SrcManual
}

const teleKeep = 300 // 30 s at 10 Hz

func (m *nodeMeta) addSample(s protocol.Sample) {
	m.msgs++
	m.tele = append(m.tele, s)
	if len(m.tele) > teleKeep*2 {
		m.tele = append(m.tele[:0], m.tele[len(m.tele)-teleKeep:]...)
	}
}

func (m *nodeMeta) samples() []protocol.Sample {
	s := m.tele
	if len(s) > teleKeep {
		s = s[len(s)-teleKeep:]
	}
	return append([]protocol.Sample(nil), s...)
}

type histPoint struct {
	t     int64
	score float64
	level string
}

// pipeline is one detector with the node metadata it needs for snapshots.
// Live phones have one; a replay gets its own.
type pipeline struct {
	det      *detect.Detector
	crowd    *crowd.Tracker
	meta     map[string]*nodeMeta
	last     detect.Result
	clusters []crowd.Cluster
	hist     map[string][]histPoint
	lastHist int64
}

func newPipeline(cfg detect.Config) *pipeline {
	return &pipeline{det: detect.New(cfg), crowd: crowd.NewTracker(crowd.ConfigFrom(cfg)),
		meta: map[string]*nodeMeta{}, hist: map[string][]histPoint{}}
}

func (p *pipeline) cfg() detect.Config { return p.det.Config() }

// step runs the detector and the crowd clusters.
func (p *pipeline) step(now int64) (detect.Result, []crowd.Change) {
	p.last = p.det.Step(now)
	var pts []crowd.Point
	for _, pr := range p.last.Phones {
		m := p.meta[pr.ID]
		if pr.Status == protocol.StatusStale || pr.Outside || (m != nil && !m.connected) {
			continue
		}
		pts = append(pts, crowd.Point{ID: pr.ID, X: pr.X, Y: pr.Y})
	}
	var ch []crowd.Change
	p.clusters, ch = p.crowd.Update(now, pts)
	if now-p.lastHist >= 1000 {
		p.lastHist = now
		for _, z := range p.last.Zones {
			h := append(p.hist[z.ID], histPoint{t: now, score: z.Score, level: z.Level})
			if len(h) > histKeep {
				h = h[len(h)-histKeep:]
			}
			p.hist[z.ID] = h
		}
	}
	return p.last, ch
}

// place moves a phone in this pipeline.
func (p *pipeline) place(id string, m *nodeMeta) {
	p.det.SetPhone(id, m.x, m.y)
	p.det.SetOutside(id, m.outside)
}

type replayState struct {
	name      string
	recs      []store.Record
	idx       int
	recStart  int64
	recEnd    int64
	wallStart int64
	speed     float64
	p         *pipeline
}

func (r *replayState) now(wall int64) int64 {
	return r.recStart + int64(float64(wall-r.wallStart)*r.speed)
}

type recordingState struct {
	label string
	w     *store.JSONLWriter
	start time.Time
}

// App is the server's brain. Create with New, then Run.
type App struct {
	opt Options
	Hub *hub.Hub

	mu        sync.Mutex
	venue     protocol.Venue
	areas     []protocol.Area
	live      *pipeline
	replay    *replayState
	sim       *simRun // in-process crowd simulation, see sim.go
	simMsgs   int64   // simulated motion messages fed so far
	rec       *recordingState
	alerts    []protocol.Alert
	lastBrief map[string]int64
	msgRate   float64
	sentState map[string]protocol.PhoneState
	sentAt    map[string]int64
	signHold  int64               // test alert keeps the sign red until this time
	hw        []protocol.Hardware // latest board status, see hardware.go
}

// New creates the app and its hub, loading saved areas and venue from
// DataDir.
func New(opt Options) *App {
	if opt.Sink == nil {
		opt.Sink = store.Discard{}
	}
	if opt.Brief == nil {
		opt.Brief = brief.New("", "")
	}
	if opt.Voice == nil {
		opt.Voice = voice.New("", "", "audio")
	}
	if opt.Sign == nil {
		opt.Sign = sign.New("")
	}
	a := &App{
		opt:       opt,
		lastBrief: map[string]int64{},
		sentState: map[string]protocol.PhoneState{},
		sentAt:    map[string]int64{},
	}
	v := opt.Venue
	if v.W <= 0 || v.H <= 0 {
		v.W, v.H = opt.Detect.VenueW, opt.Detect.VenueH
	}
	if saved, ok := a.loadVenue(); ok {
		v = saved
	}
	if err := validVenue(&v); err != nil {
		log.Printf("venue: %v; using %gx%g m without a geo-anchor", err, opt.Detect.VenueW, opt.Detect.VenueH)
		v = protocol.Venue{W: opt.Detect.VenueW, H: opt.Detect.VenueH}
	}
	a.venue = v
	a.areas = a.loadAreas()
	a.live = newPipeline(a.liveConfig())
	a.applyZones(a.live)
	a.Hub = hub.New(a)
	return a
}

// liveConfig is the detector config with the current venue size.
func (a *App) liveConfig() detect.Config {
	cfg := a.opt.Detect
	cfg.VenueW, cfg.VenueH = a.venue.W, a.venue.H
	return cfg
}

// Config is the live detector configuration.
func (a *App) Config() detect.Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.liveConfig()
}

// zoneDefs are the zones for a venue: the default split when no areas are
// drawn, else the areas plus "rest" (phones in no area).
func zoneDefs(cfg detect.Config, areas []protocol.Area) []detect.ZoneDef {
	if len(areas) == 0 {
		return detect.DefaultZones(cfg)
	}
	var out []detect.ZoneDef
	for _, ar := range areas {
		poly := make([][2]float64, len(ar.Poly))
		for i, pt := range ar.Poly {
			x, y := cfg.Clamp(pt[0], pt[1])
			poly[i] = [2]float64{x, y}
		}
		out = append(out, detect.ZoneDef{ID: ar.ID, Name: ar.Name, Poly: poly, Custom: true, Sens: ar.Sens})
	}
	return append(out, detect.ZoneDef{ID: detect.RestZone, Name: "Rest of venue", Sens: detect.SensNormal,
		Poly: detect.Rect(0, 0, cfg.VenueW, cfg.VenueH), Rest: true})
}

// applyZones gives a pipeline the current areas. Caller holds mu (or owns p).
func (a *App) applyZones(p *pipeline) {
	p.det.SetZones(zoneDefs(p.cfg(), a.areas))
}

// ---- hub.Handler ----

// LegacyPos maps an old grid cell to venue metres.
func (a *App) LegacyPos(row, col int) (x, y float64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.liveConfig().LegacyPos(row, col)
}

func (a *App) PhoneHello(id string, x, y float64, ua string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.helloIn(a.live, hub.Now(), id, x, y, ua)
}

// helloIn places a phone in pipeline p. Live phones and simulated phones
// (crowdsim) take the same path. Caller holds mu.
func (a *App) helloIn(p *pipeline, now int64, id string, x, y float64, ua string) {
	x, y = p.cfg().Clamp(x, y)
	m := p.meta[id]
	if m == nil {
		m = &nodeMeta{joinedAt: now}
		p.meta[id] = m
		if p == a.live {
			log.Printf("phone %s joined at %.1f, %.1f m (%s)", short(id), x, y, ua)
		}
	}
	m.x, m.y, m.acc, m.outside = x, y, 0, false
	m.gps.Reset()
	m.ua, m.connected, m.goneAt = ua, true, 0
	m.lastRecv = now
	p.place(id, m)
	a.record(store.Record{K: store.KindHello, T: now, ID: id, X: store.F(r2(x)), Y: store.F(r2(y)), UA: ua})
}

func (a *App) PhonePos(id string, x, y float64) {
	if math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.posIn(a.live, hub.Now(), id, x, y)
}

// posIn moves a phone placed by hand in pipeline p. Caller holds mu.
func (a *App) posIn(p *pipeline, now int64, id string, x, y float64) {
	m := p.meta[id]
	if m == nil {
		return
	}
	x, y = p.cfg().Clamp(x, y)
	m.x, m.y, m.acc, m.outside = x, y, 0, false
	m.gps.Reset()
	p.place(id, m)
	a.record(store.Record{K: store.KindPos, T: now, ID: id, X: store.F(r2(x)), Y: store.F(r2(y))})
}

// PhoneGPS converts a fix to venue metres. lat/lon are used here and
// dropped: never logged, stored or forwarded.
func (a *App) PhoneGPS(id string, lat, lon, acc float64) {
	now := hub.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	m := a.live.meta[id]
	if m == nil {
		return
	}
	if !a.venue.Geo {
		if !m.gpsWarned {
			m.gpsWarned = true
			log.Printf("phone %s sent GPS but the venue has no geo-anchor (PUT /api/venue); ignoring it, place it by hand", short(id))
		}
		return
	}
	cfg := a.liveConfig()
	if !(acc > 0 && acc <= cfg.GPSMaxAcc) || !(lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180) {
		return // too inaccurate (or nonsense): keep the last good position
	}
	anchor := geo.Anchor{Lat: a.venue.Lat, Lon: a.venue.Lon, Bearing: a.venue.Bearing}
	x, y := anchor.ToVenue(lat, lon)
	x, y = m.gps.Add(x, y, acc)
	m.outside = x < 0 || y < 0 || x > cfg.VenueW || y > cfg.VenueH
	m.x, m.y = cfg.Clamp(x, y)
	m.acc = math.Max(0.1, math.Round(m.gps.Acc*10)/10)
	a.live.place(id, m)
	a.record(store.Record{K: store.KindPos, T: now, ID: id, X: store.F(r2(m.x)), Y: store.F(r2(m.y)), Acc: m.acc, Out: m.outside})
}

func (a *App) PhoneSync(id string, offset, rtt int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.syncIn(a.live, hub.Now(), id, offset, rtt)
}

// syncIn stores a phone's clock sync in pipeline p. Caller holds mu.
func (a *App) syncIn(p *pipeline, now int64, id string, offset, rtt int64) {
	m := p.meta[id]
	if m == nil {
		return
	}
	m.synced, m.offset, m.rtt = true, offset, rtt
	a.record(store.Record{K: store.KindSync, T: now, ID: id, RTT: rtt, Offset: offset})
}

func (a *App) PhoneMotion(id string, mo protocol.Motion, recv int64) {
	a.mu.Lock()
	zone, x, y, ok := a.motionIn(a.live, id, mo, recv)
	a.mu.Unlock()
	if !ok {
		return
	}
	a.opt.Sink.Reading(store.Reading{Time: time.UnixMilli(mo.T), PhoneID: id, Zone: zone, X: r2(x), Y: r2(y),
		AX: mo.AX, AY: mo.AY, AZ: mo.AZ, Rot: mo.Rot})
}

// motionIn feeds one clock-corrected reading to pipeline p and the labelled
// run being recorded. Caller holds mu.
func (a *App) motionIn(p *pipeline, id string, mo protocol.Motion, recv int64) (zone string, x, y float64, ok bool) {
	m := p.meta[id]
	if m == nil {
		return "", 0, 0, false
	}
	m.lastRecv = recv
	m.addSample(protocol.Sample{T: mo.T, AX: mo.AX, AY: mo.AY, AZ: mo.AZ, Rot: mo.Rot})
	p.det.Add(id, detect.Sample{T: mo.T, AX: mo.AX, AY: mo.AY, AZ: mo.AZ, Rot: mo.Rot})
	a.record(store.Record{K: store.KindM, T: recv, ID: id, CT: mo.T, AX: mo.AX, AY: mo.AY, AZ: mo.AZ, Rot: mo.Rot})
	if !m.outside {
		zone = p.det.ZoneOf(m.x, m.y)
	}
	return zone, m.x, m.y, true
}

func (a *App) PhoneGone(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.goneIn(a.live, hub.Now(), id)
	delete(a.sentState, id)
	delete(a.sentAt, id)
	log.Printf("phone %s left", short(id))
}

// goneIn marks a phone disconnected in pipeline p. Caller holds mu.
func (a *App) goneIn(p *pipeline, now int64, id string) {
	if m := p.meta[id]; m != nil {
		m.connected, m.goneAt = false, now
	}
	a.record(store.Record{K: store.KindBye, T: now, ID: id})
}

func (a *App) DashWelcome() [][]byte {
	a.mu.Lock()
	al := protocol.Alerts{Type: protocol.TypeAlerts, Alerts: append([]protocol.Alert{}, a.alerts...)}
	snap := a.snapshotLocked(hub.Now())
	a.mu.Unlock()
	b1, _ := json.Marshal(al)
	b2, _ := json.Marshal(snap)
	return [][]byte{b1, b2}
}

// record writes to the labelled run if one is being recorded. Caller holds mu.
func (a *App) record(r store.Record) {
	if a.rec != nil {
		if err := a.rec.w.Write(r); err != nil {
			log.Printf("record: %v", err)
		}
	}
}

// ---- loops ----

// Run drives the detector, snapshots and phone feedback until ctx ends.
func (a *App) Run(ctx context.Context) {
	if a.opt.Voice.Enabled() {
		go func() {
			if err := a.opt.Voice.PrepareFallback(ctx); err != nil {
				log.Printf("voice: fallback clip: %v", err)
			}
		}()
	}
	go a.watchHardware(ctx)
	det := time.NewTicker(DetectEvery)
	snap := time.NewTicker(SnapshotEvery)
	st := time.NewTicker(PhoneStateEvery)
	simT := time.NewTicker(SimTickEvery)
	defer simT.Stop()
	defer det.Stop()
	defer snap.Stop()
	defer st.Stop()
	lastCount, lastCountT := a.motionCount(), hub.Now()
	for {
		select {
		case <-ctx.Done():
			a.StopRecording()
			return
		case <-det.C:
			now := hub.Now()
			if now-lastCountT >= 1000 {
				c := a.motionCount()
				a.mu.Lock()
				a.msgRate = float64(c-lastCount) * 1000 / float64(now-lastCountT)
				if a.rec != nil {
					a.rec.w.Flush()
				}
				a.mu.Unlock()
				lastCount, lastCountT = c, now
			}
			a.detectTick(now)
		case <-snap.C:
			a.mu.Lock()
			s := a.snapshotLocked(hub.Now())
			a.mu.Unlock()
			a.Hub.BroadcastJSON(s)
		case <-st.C:
			a.phoneStates(hub.Now())
		case <-simT.C:
			// Physics runs off the loop so a slow tick never delays detection.
			a.mu.Lock()
			s := a.sim
			a.mu.Unlock()
			if s != nil && s.busy.CompareAndSwap(false, true) {
				go func() {
					defer s.busy.Store(false)
					a.simTick(hub.Now())
				}()
			}
		}
	}
}

type pendingAlert struct {
	al     protocol.Alert
	info   brief.Info
	brief  bool
	replay bool
}

func (a *App) detectTick(now int64) {
	a.mu.Lock()
	res, cch := a.live.step(now)
	forget(a.live, now)
	active, pnow, isReplay, src := a.live, now, false, ""
	changes := res.Changes
	if r := a.replay; r != nil {
		pnow = r.now(now)
		a.feedReplay(r, pnow)
		var rres detect.Result
		rres, cch = r.p.step(pnow)
		changes = rres.Changes
		active, isReplay, src = r.p, true, " [replay]"
		if pnow > r.recEnd+3000 {
			log.Printf("replay %s finished, back to live", r.name)
			a.replay = nil
		}
	}
	if s := a.sim; s != nil {
		var sres detect.Result
		sres, cch = s.p.step(now)
		changes = sres.Changes
		active, isReplay, src = s.p, true, " [sim]"
		forget(s.p, now)
		// Pulse's first red alert of the run (wave or density), for the lead time.
		for _, ch := range changes {
			if ch.To == protocol.LevelRed && s.alertAt < 0 {
				s.alertAt = s.seconds(now)
			}
		}
		for _, ch := range cch {
			if ch.To == protocol.LevelRed && s.alertAt < 0 {
				s.alertAt = s.seconds(now)
			}
		}
	}
	var pend []pendingAlert
	for _, ch := range changes {
		pa := pendingAlert{replay: isReplay, al: protocol.Alert{Type: protocol.TypeAlert, T: now, Kind: protocol.KindWave,
			Zone: ch.Zone, Level: ch.To, Score: round2(ch.Score)}}
		if ch.To == protocol.LevelRed && ch.From == protocol.LevelYellow && now-a.lastBrief[ch.Zone] >= BriefCooldown {
			a.lastBrief[ch.Zone] = now
			pa.brief = true
			pa.info = briefInfo(active, ch.Zone, protocol.LevelRed, pnow)
		}
		pend = append(pend, pa)
	}
	for _, ch := range cch {
		c := ch.Cluster
		zone := active.det.ZoneOf(c.X, c.Y)
		pa := pendingAlert{replay: isReplay, al: protocol.Alert{Type: protocol.TypeAlert, T: now, Kind: protocol.KindDensity,
			Zone: zone, Level: ch.To, Score: round2(c.Est)}}
		if ch.To == protocol.LevelRed && ch.From == protocol.LevelYellow && now-a.lastBrief[zone] >= BriefCooldown {
			a.lastBrief[zone] = now
			pa.brief = true
			pa.info = densityInfo(active, c, zone)
		}
		pend = append(pend, pa)
	}
	zoneLevels, signLevel, signZone := alertLevels(active)
	a.lightLevels(zoneLevels, a.opt.Sign.Zones())
	hold := now < a.signHold
	a.mu.Unlock()

	for _, pa := range pend {
		log.Printf("%s %s: %s (score %.2f)%s", pa.al.Kind, pa.al.Zone, pa.al.Level, pa.al.Score, src)
		a.pushAlert(pa.al)
		if !pa.replay {
			a.opt.Sink.Alert(store.AlertRow{Time: time.UnixMilli(now), Zone: pa.al.Zone, Level: pa.al.Level, Score: pa.al.Score})
		}
		if pa.brief {
			go a.briefAndSpeak(pa.info, false, pa.replay)
		}
	}
	if !hold {
		a.opt.Sign.Update(zoneLevels, signLevel, signZone)
	}
}

// alertLevels merges zone levels with the density levels of the clusters
// in them (a red cluster makes its zone red on the sign), and picks the
// worst zone.
func alertLevels(p *pipeline) (zoneLevels map[string]string, level, zone string) {
	zoneLevels = map[string]string{}
	score := map[string]float64{}
	for _, z := range p.last.Zones {
		zoneLevels[z.ID], score[z.ID] = z.Level, z.Score
	}
	for _, c := range p.clusters {
		id := p.det.ZoneOf(c.X, c.Y)
		if id != "" && levelRank[c.Level] > levelRank[zoneLevels[id]] {
			zoneLevels[id] = c.Level
		}
	}
	level = protocol.LevelCalm
	best := -1.0
	ids := make([]string, 0, len(zoneLevels))
	for id := range zoneLevels {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		l := zoneLevels[id]
		if levelRank[l] > levelRank[level] || (l == level && score[id] > best) {
			level, zone, best = l, id, score[id]
		}
	}
	if level == protocol.LevelCalm {
		zone = ""
	}
	return zoneLevels, level, zone
}

func (a *App) pushAlert(al protocol.Alert) {
	a.mu.Lock()
	a.alerts = append(a.alerts, al)
	if len(a.alerts) > alertLogKeep {
		a.alerts = a.alerts[len(a.alerts)-alertLogKeep:]
	}
	a.mu.Unlock()
	a.Hub.BroadcastJSON(al)
}

// briefAndSpeak runs Gemini then ElevenLabs and broadcasts the result. Both
// fail soft: template text, then fallback clip or the browser's own voice.
func (a *App) briefAndSpeak(info brief.Info, test, replay bool) {
	ctx := context.Background()
	text, err := a.opt.Brief.Brief(ctx, info)
	if err != nil && !errors.Is(err, brief.ErrNoKey) {
		log.Printf("brief: %v (using template)", err)
	}
	url, err := a.opt.Voice.Speak(ctx, text)
	if err != nil {
		if !errors.Is(err, voice.ErrNoKey) {
			log.Printf("voice: %v", err)
		}
		url = ""
		if info.Zone == "B" && info.Kind != protocol.KindDensity {
			url = a.opt.Voice.FallbackURL() // "Zone B, crowd waves building."
		}
	}
	kind := info.Kind
	if kind == "" {
		kind = protocol.KindWave
	}
	score := lastScore(info)
	if kind == protocol.KindDensity {
		score = round2(info.Density)
	}
	al := protocol.Alert{Type: protocol.TypeAlert, T: hub.Now(), Kind: kind, Zone: info.Zone, Level: info.Level,
		Score: score, Brief: text, AudioURL: url, Test: test}
	log.Printf("brief zone %s: %s", info.Zone, text)
	a.pushAlert(al)
	if !test && !replay {
		a.opt.Sink.Alert(store.AlertRow{Time: time.UnixMilli(al.T), Zone: al.Zone, Level: al.Level, Score: al.Score, Brief: text})
	}
}

// TestAlert runs the whole alert chain (briefing, voice, sign) for the zone
// with the highest score, without touching detector state.
func (a *App) TestAlert() string {
	now := hub.Now()
	a.mu.Lock()
	p := a.active()
	// The zone that looks worst, else B (the fallback clip's zone), else the first.
	zone, best := "", 0.1
	for _, z := range p.last.Zones {
		if z.Score > best {
			zone, best = z.ID, z.Score
		}
	}
	if zone == "" {
		if _, ok := p.last.Zone("B"); ok || len(p.last.Zones) == 0 {
			zone = "B"
		} else {
			zone = p.last.Zones[0].ID
		}
	}
	info := briefInfo(p, zone, protocol.LevelRed, a.pnowLocked(now))
	if info.Direction == "" {
		info.Direction, info.LagMs = "+x", 250
	}
	a.signHold = now + 8000
	light := a.lightFor(zone)
	a.mu.Unlock()

	a.pushAlert(protocol.Alert{Type: protocol.TypeAlert, T: now, Kind: protocol.KindWave, Zone: zone, Level: protocol.LevelRed, Score: lastScore(info), Test: true})
	a.opt.Sign.Force(protocol.LevelRed, zone)
	if light != "" {
		a.opt.Sign.Force(protocol.LevelRed, light)
	}
	go a.briefAndSpeak(info, true, false)
	return zone
}

func (a *App) active() *pipeline {
	if a.sim != nil {
		return a.sim.p
	}
	if a.replay != nil {
		return a.replay.p
	}
	return a.live
}

func (a *App) pnowLocked(now int64) int64 {
	if a.replay != nil && a.sim == nil {
		return a.replay.now(now)
	}
	return now
}

// inZone reports whether a phone result is in a zone.
func inZone(p *pipeline, ph detect.PhoneResult, zone string) bool {
	if ph.Outside {
		return false
	}
	for _, z := range p.det.ZonesOf(ph.X, ph.Y) {
		if z == zone {
			return true
		}
	}
	return false
}

func briefInfo(p *pipeline, zone, level string, now int64) brief.Info {
	in := brief.Info{Kind: protocol.KindWave, Zone: zone, Where: zoneName(p, zone), Level: level}
	if z, ok := p.last.Zone(zone); ok {
		in.Direction, in.LagMs = z.Direction, z.LagMs
	}
	h := p.hist[zone]
	for i := len(h) - 1; i >= 0 && h[i].level != protocol.LevelCalm; i-- {
		in.SecondsHigh = float64(now-h[i].t) / 1000
	}
	in.SecondsHigh = math.Round(in.SecondsHigh)
	for i := max(0, len(h)-20); i < len(h); i++ {
		in.Scores = append(in.Scores, round2(h[i].score))
	}
	for _, ph := range p.last.Phones {
		if ph.Status == protocol.StatusStale || !inZone(p, ph, zone) {
			continue
		}
		in.Phones++
		if ph.Status == protocol.StatusSwaying || ph.Status == protocol.StatusWave {
			in.Swaying++
		}
	}
	return in
}

// zoneName is the zone's name for people ("Zone A", a drawn area's name).
func zoneName(p *pipeline, zone string) string {
	if z, ok := p.last.Zone(zone); ok && z.Name != "" {
		return z.Name
	}
	return ""
}

// densityInfo describes a cluster that packed past the danger density.
func densityInfo(p *pipeline, c crowd.Cluster, zone string) brief.Info {
	in := brief.Info{Kind: protocol.KindDensity, Zone: zone, Where: zoneName(p, zone), Level: c.Level, Density: round2(c.Est),
		People: c.People, AreaM2: math.Round(c.Area()*10) / 10, Trend: c.Trend, X: math.Round(c.X), Y: math.Round(c.Y)}
	if c.Peak > c.Density {
		// The level comes from the packed spot, not the cluster as a whole:
		// describe that spot.
		area := math.Pi * crowd.LocalR * crowd.LocalR
		part := p.cfg().Participation
		if part <= 0 {
			part = 1
		}
		in.People = int(math.Round(c.Peak * area / part))
		in.AreaM2 = math.Round(area*10) / 10
		in.X, in.Y = math.Round(c.PeakX), math.Round(c.PeakY)
	}
	for _, ph := range p.last.Phones {
		if ph.Status != protocol.StatusStale && inZone(p, ph, zone) {
			in.Phones++
		}
	}
	return in
}

func lastScore(in brief.Info) float64 {
	if len(in.Scores) == 0 {
		return 0
	}
	return in.Scores[len(in.Scores)-1]
}

var levelRank = map[string]int{protocol.LevelCalm: 0, protocol.LevelYellow: 1, protocol.LevelRed: 2}

// ---- snapshots ----

func (a *App) snapshotLocked(now int64) protocol.Snapshot {
	p := a.active()
	cfg := p.cfg()
	pnow := a.pnowLocked(now)
	s := protocol.Snapshot{Type: protocol.TypeSnapshot, T: now, Mode: "live",
		Venue: protocol.VenueSize{W: cfg.VenueW, H: cfg.VenueH},
		Nodes: []protocol.Node{}, Zones: []protocol.Zone{}, Waves: []protocol.Wave{},
		Links: [][2]string{}, Clusters: []protocol.Cluster{}}
	if a.replay != nil {
		s.Mode, s.Replay = "replay", a.replay.name
		if d := a.replay.recEnd - a.replay.recStart; d > 0 {
			s.Progress = round2(math.Min(1, float64(pnow-a.replay.recStart)/float64(d)))
		}
	}
	if a.sim != nil {
		s.Mode, s.Replay, s.Progress = "sim", "", 0
		f := a.sim.frame
		s.Sim = &f
	}
	if a.rec != nil {
		s.Recording = a.rec.label
	}
	status := map[string]detect.PhoneResult{}
	for _, pr := range p.last.Phones {
		status[pr.ID] = pr
	}
	var rtts []int64
	for id, m := range p.meta {
		n := protocol.Node{ID: id, X: r2(m.x), Y: r2(m.y), RTT: m.rtt, Offset: m.offset, AgeMs: max(0, pnow-m.lastRecv),
			UA: m.ua, Acc: m.acc, Src: m.src(), Outside: m.outside}
		if !m.outside {
			n.Zone = p.det.ZoneOf(m.x, m.y)
		}
		pr, ok := status[id]
		switch {
		case !m.connected:
			n.Status = protocol.StatusStale
		case !m.synced:
			n.Status = protocol.StatusConnecting
		case !ok:
			n.Status = protocol.StatusConnecting
		default:
			n.Status = pr.Status
			n.Sway = round2(pr.Sway)
		}
		if m.connected {
			s.Stats.Phones++
			if m.synced {
				rtts = append(rtts, m.rtt)
			}
		}
		s.Nodes = append(s.Nodes, n)
	}
	sort.Slice(s.Nodes, func(i, j int) bool { return s.Nodes[i].ID < s.Nodes[j].ID })
	if len(rtts) > 0 {
		sort.Slice(rtts, func(i, j int) bool { return rtts[i] < rtts[j] })
		s.Stats.MedianRTT = rtts[len(rtts)/2]
	}
	s.Stats.MsgPerSec = math.Round(a.msgRate)
	for _, z := range p.last.Zones {
		poly := make([]protocol.Point, len(z.Poly))
		for i, pt := range z.Poly {
			poly[i] = protocol.Point{r2(pt[0]), r2(pt[1])}
		}
		s.Zones = append(s.Zones, protocol.Zone{ID: z.ID, Name: z.Name, Level: z.Level, Score: round2(z.Score),
			Poly: poly, Custom: z.Custom, Sens: z.Sens})
	}
	for _, e := range p.last.Edges {
		s.Links = append(s.Links, [2]string{e.From, e.To})
		if !e.Wave {
			continue
		}
		w := protocol.Wave{From: e.From, To: e.To, LagMs: e.LagMs, Corr: round2(e.Corr)}
		if w.LagMs < 0 { // always report in the direction of travel
			w.From, w.To, w.LagMs = w.To, w.From, -w.LagMs
		}
		s.Waves = append(s.Waves, w)
	}
	for _, c := range p.clusters {
		s.Clusters = append(s.Clusters, protocol.Cluster{ID: c.ID, X: r2(c.X), Y: r2(c.Y), R: r2(c.R), Count: c.Count,
			Density: round2(c.Density), People: c.People, Trend: c.Trend, Level: c.Level})
	}
	return s
}

// phoneStates sends colour feedback to phones when it changes (and every few
// seconds as a heartbeat). zone = level of the worst zone containing the phone.
func (a *App) phoneStates(now int64) {
	type out struct {
		id string
		st protocol.PhoneState
	}
	var send []out
	a.mu.Lock()
	zoneLevel := map[string]string{}
	for _, z := range a.live.last.Zones {
		zoneLevel[z.ID] = z.Level
	}
	for _, pr := range a.live.last.Phones {
		m := a.live.meta[pr.ID]
		if m == nil || !m.connected {
			continue
		}
		node := pr.Status
		if !m.synced {
			node = protocol.StatusConnecting
		}
		level := protocol.LevelCalm
		if !m.outside {
			for _, z := range a.live.det.ZonesOf(m.x, m.y) {
				if levelRank[zoneLevel[z]] > levelRank[level] {
					level = zoneLevel[z]
				}
			}
		}
		st := protocol.PhoneState{Type: protocol.TypeState, Node: node, Zone: level}
		if st != a.sentState[pr.ID] || now-a.sentAt[pr.ID] > 3000 {
			a.sentState[pr.ID], a.sentAt[pr.ID] = st, now
			send = append(send, out{pr.ID, st})
		}
	}
	a.mu.Unlock()
	for _, o := range send {
		a.Hub.SendPhone(o.id, o.st)
	}
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// r2 rounds a position to the centimetre.
func r2(v float64) float64 { return round2(v) }

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// forget drops phones that left more than ForgetAfterMs ago. Caller holds mu.
func forget(p *pipeline, now int64) {
	for id, m := range p.meta {
		if !m.connected && now-m.goneAt > ForgetAfterMs {
			delete(p.meta, id)
			p.det.RemovePhone(id)
		}
	}
}

// motionCount is every motion message received: real phones plus the simulation's.
func (a *App) motionCount() int64 {
	a.mu.Lock()
	n := a.simMsgs
	a.mu.Unlock()
	return a.Hub.MotionCount() + n
}

// modeLocked is the data source on screen: live, replay or sim. Caller holds mu.
func (a *App) modeLocked() string {
	switch {
	case a.sim != nil:
		return "sim"
	case a.replay != nil:
		return "replay"
	}
	return "live"
}
