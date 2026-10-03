// Package app is the pipeline between the hub and everything else: it feeds
// readings to the detector, turns level changes into alerts (briefing, voice,
// sign, storage), records runs, replays them, and builds dashboard snapshots.
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
	"github.com/Drivera0/crowd-crush/server/internal/detect"
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
	Sink          store.Sink   // continuous storage; nil = discard
	Tiger         *store.Tiger // for labelled runs + DB replay; may be nil
	Brief         *brief.Client
	Voice         *voice.Client
	Sign          *sign.Client
}

type nodeMeta struct {
	row, col  int
	ua        string
	synced    bool
	rtt       int64
	offset    int64
	lastRecv  int64
	connected bool
	goneAt    int64
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
	meta     map[string]*nodeMeta
	last     detect.Result
	hist     map[string][]histPoint
	lastHist int64
}

func newPipeline(cfg detect.Config) *pipeline {
	return &pipeline{det: detect.New(cfg), meta: map[string]*nodeMeta{}, hist: map[string][]histPoint{}}
}

func (p *pipeline) step(now int64) detect.Result {
	p.last = p.det.Step(now)
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
	return p.last
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
	live      *pipeline
	replay    *replayState
	rec       *recordingState
	alerts    []protocol.Alert
	lastBrief map[string]int64
	msgRate   float64
	sentState map[string]protocol.PhoneState
	sentAt    map[string]int64
	signHold  int64 // test alert keeps the sign red until this time
}

// New creates the app and its hub.
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
		live:      newPipeline(opt.Detect),
		lastBrief: map[string]int64{},
		sentState: map[string]protocol.PhoneState{},
		sentAt:    map[string]int64{},
	}
	a.Hub = hub.New(a)
	return a
}

// Config is the live detector configuration.
func (a *App) Config() detect.Config { return a.opt.Detect }

// ---- hub.Handler ----

func (a *App) PhoneHello(id string, row, col int, ua string) {
	cfg := a.opt.Detect
	row = clamp(row, 0, cfg.Rows-1)
	col = clamp(col, 0, cfg.Cols-1)
	now := hub.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	m := a.live.meta[id]
	if m == nil {
		m = &nodeMeta{}
		a.live.meta[id] = m
		log.Printf("phone %s joined at row %d col %d (%s)", short(id), row, col, ua)
	}
	m.row, m.col, m.ua, m.connected, m.goneAt = row, col, ua, true, 0
	m.lastRecv = now
	a.live.det.SetPhone(id, row, col)
	a.record(store.Record{K: store.KindHello, T: now, ID: id, Row: row, Col: col, UA: ua})
}

func (a *App) PhoneSync(id string, offset, rtt int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	m := a.live.meta[id]
	if m == nil {
		return
	}
	m.synced, m.offset, m.rtt = true, offset, rtt
	a.record(store.Record{K: store.KindSync, T: hub.Now(), ID: id, RTT: rtt, Offset: offset})
}

func (a *App) PhoneMotion(id string, mo protocol.Motion, recv int64) {
	a.mu.Lock()
	m := a.live.meta[id]
	if m == nil {
		a.mu.Unlock()
		return
	}
	m.lastRecv = recv
	a.live.det.Add(id, detect.Sample{T: mo.T, AX: mo.AX, AY: mo.AY, AZ: mo.AZ, Rot: mo.Rot})
	a.record(store.Record{K: store.KindM, T: recv, ID: id, CT: mo.T, AX: mo.AX, AY: mo.AY, AZ: mo.AZ, Rot: mo.Rot})
	zone := a.live.det.ZoneOf(m.row, m.col)
	row, col := m.row, m.col
	a.mu.Unlock()
	a.opt.Sink.Reading(store.Reading{Time: time.UnixMilli(mo.T), PhoneID: id, Zone: zone, Row: row, Col: col,
		AX: mo.AX, AY: mo.AY, AZ: mo.AZ, Rot: mo.Rot})
}

func (a *App) PhoneGone(id string) {
	now := hub.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if m := a.live.meta[id]; m != nil {
		m.connected, m.goneAt = false, now
	}
	delete(a.sentState, id)
	delete(a.sentAt, id)
	a.record(store.Record{K: store.KindBye, T: now, ID: id})
	log.Printf("phone %s left", short(id))
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
	det := time.NewTicker(DetectEvery)
	snap := time.NewTicker(SnapshotEvery)
	st := time.NewTicker(PhoneStateEvery)
	defer det.Stop()
	defer snap.Stop()
	defer st.Stop()
	lastCount, lastCountT := a.Hub.MotionCount(), hub.Now()
	for {
		select {
		case <-ctx.Done():
			a.StopRecording()
			return
		case <-det.C:
			now := hub.Now()
			if now-lastCountT >= 1000 {
				c := a.Hub.MotionCount()
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
		}
	}
}

type pendingChange struct {
	ch     detect.Change
	info   brief.Info
	brief  bool
	replay bool
}

func (a *App) detectTick(now int64) {
	a.mu.Lock()
	res := a.live.step(now)
	for id, m := range a.live.meta {
		if !m.connected && now-m.goneAt > ForgetAfterMs {
			delete(a.live.meta, id)
			a.live.det.RemovePhone(id)
		}
	}
	active, pnow, isReplay := a.live, now, false
	changes := res.Changes
	if r := a.replay; r != nil {
		pnow = r.now(now)
		a.feedReplay(r, pnow)
		changes = r.p.step(pnow).Changes
		active, isReplay = r.p, true
		if pnow > r.recEnd+3000 {
			log.Printf("replay %s finished, back to live", r.name)
			a.replay = nil
		}
	}
	var pend []pendingChange
	for _, ch := range changes {
		pc := pendingChange{ch: ch, replay: isReplay}
		if ch.To == protocol.LevelRed && ch.From == protocol.LevelYellow && now-a.lastBrief[ch.Zone] >= BriefCooldown {
			a.lastBrief[ch.Zone] = now
			pc.brief = true
			pc.info = briefInfo(active, ch.Zone, protocol.LevelRed, pnow)
		}
		pend = append(pend, pc)
	}
	signLevel, signZone := worstZone(active.last)
	hold := now < a.signHold
	a.mu.Unlock()

	for _, pc := range pend {
		log.Printf("zone %s: %s → %s (score %.2f)%s", pc.ch.Zone, pc.ch.From, pc.ch.To, pc.ch.Score, map[bool]string{true: " [replay]"}[pc.replay])
		al := protocol.Alert{Type: protocol.TypeAlert, T: now, Zone: pc.ch.Zone, Level: pc.ch.To, Score: round2(pc.ch.Score)}
		a.pushAlert(al)
		if !pc.replay {
			a.opt.Sink.Alert(store.AlertRow{Time: time.UnixMilli(now), Zone: al.Zone, Level: al.Level, Score: al.Score})
		}
		if pc.brief {
			go a.briefAndSpeak(pc.info, false, pc.replay)
		}
	}
	if !hold {
		a.opt.Sign.Set(signLevel, signZone)
	}
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
		if info.Zone == "B" {
			url = a.opt.Voice.FallbackURL() // "Zone B, crowd waves building."
		}
	}
	al := protocol.Alert{Type: protocol.TypeAlert, T: hub.Now(), Zone: info.Zone, Level: info.Level,
		Score: lastScore(info), Brief: text, AudioURL: url, Test: test}
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
	zone := "B"
	if _, ok := p.last.Zone(zone); !ok && len(p.last.Zones) > 0 {
		zone = p.last.Zones[0].ID
	}
	best := -1.0
	for _, z := range p.last.Zones {
		if z.Score > best+0.05 {
			best, zone = z.Score, z.ID
		}
	}
	info := briefInfo(p, zone, protocol.LevelRed, a.pnowLocked(now))
	if info.Direction == "" {
		info.Direction, info.LagMs = "+col", 250
	}
	a.signHold = now + 8000
	a.mu.Unlock()

	a.pushAlert(protocol.Alert{Type: protocol.TypeAlert, T: now, Zone: zone, Level: protocol.LevelRed, Score: lastScore(info), Test: true})
	a.opt.Sign.Force(protocol.LevelRed, zone)
	go a.briefAndSpeak(info, true, false)
	return zone
}

func (a *App) active() *pipeline {
	if a.replay != nil {
		return a.replay.p
	}
	return a.live
}

func (a *App) pnowLocked(now int64) int64 {
	if a.replay != nil {
		return a.replay.now(now)
	}
	return now
}

func briefInfo(p *pipeline, zone, level string, now int64) brief.Info {
	in := brief.Info{Zone: zone, Level: level}
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
		if p.det.ZoneOf(ph.Row, ph.Col) != zone || ph.Status == protocol.StatusStale {
			continue
		}
		in.Phones++
		if ph.Status == protocol.StatusSwaying || ph.Status == protocol.StatusWave {
			in.Swaying++
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

func worstZone(r detect.Result) (level, zone string) {
	level = protocol.LevelCalm
	best := -1.0
	for _, z := range r.Zones {
		if levelRank[z.Level] > levelRank[level] || (z.Level == level && z.Score > best) {
			level, zone, best = z.Level, z.ID, z.Score
		}
	}
	if level == protocol.LevelCalm {
		zone = ""
	}
	return level, zone
}

// ---- snapshots ----

func (a *App) snapshotLocked(now int64) protocol.Snapshot {
	p := a.active()
	cfg := p.det.Config()
	pnow := a.pnowLocked(now)
	s := protocol.Snapshot{Type: protocol.TypeSnapshot, T: now, Mode: "live", Rows: cfg.Rows, Cols: cfg.Cols,
		Nodes: []protocol.Node{}, Zones: []protocol.Zone{}, Waves: []protocol.Wave{}}
	if a.replay != nil {
		s.Mode, s.Replay = "replay", a.replay.name
		if d := a.replay.recEnd - a.replay.recStart; d > 0 {
			s.Progress = round2(math.Min(1, float64(pnow-a.replay.recStart)/float64(d)))
		}
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
		n := protocol.Node{ID: id, Row: m.row, Col: m.col, RTT: m.rtt, Offset: m.offset, AgeMs: max(0, pnow-m.lastRecv), UA: m.ua}
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
	sort.Slice(s.Nodes, func(i, j int) bool {
		if s.Nodes[i].Row != s.Nodes[j].Row {
			return s.Nodes[i].Row < s.Nodes[j].Row
		}
		if s.Nodes[i].Col != s.Nodes[j].Col {
			return s.Nodes[i].Col < s.Nodes[j].Col
		}
		return s.Nodes[i].ID < s.Nodes[j].ID
	})
	if len(rtts) > 0 {
		sort.Slice(rtts, func(i, j int) bool { return rtts[i] < rtts[j] })
		s.Stats.MedianRTT = rtts[len(rtts)/2]
	}
	s.Stats.MsgPerSec = math.Round(a.msgRate)
	for _, z := range p.last.Zones {
		s.Zones = append(s.Zones, protocol.Zone{ID: z.ID, Level: z.Level, Score: round2(z.Score),
			Row0: z.Row0, Col0: z.Col0, Row1: z.Row1, Col1: z.Col1})
	}
	for _, e := range p.last.Waves() {
		w := protocol.Wave{From: e.From, To: e.To, LagMs: e.LagMs, Corr: round2(e.Corr)}
		if w.LagMs < 0 { // always report in the direction of travel
			w.From, w.To, w.LagMs = w.To, w.From, -w.LagMs
		}
		s.Waves = append(s.Waves, w)
	}
	return s
}

// phoneStates sends colour feedback to phones when it changes (and every few
// seconds as a heartbeat).
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
		st := protocol.PhoneState{Type: protocol.TypeState, Node: node, Zone: zoneLevel[a.live.det.ZoneOf(m.row, m.col)]}
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

func clamp(v, lo, hi int) int {
	return max(lo, min(v, hi))
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
