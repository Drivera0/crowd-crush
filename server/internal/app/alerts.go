package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Drivera0/crowd-crush/server/internal/brief"
	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/hub"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/store"
	"github.com/Drivera0/crowd-crush/server/internal/voice"
)

// Alerts are incidents with a stable ID.
//
// An incident is one (data source, zone, kind) needing attention: kind is
// wave, density or rule, the source live, replay or sim. A zone leaving
// calm opens one (status open). While it is not resolved, every later level
// change of the same zone and kind updates that incident under the same ID
// instead of raising a new card: yellow → red raises its level (and T, the
// time it reached that level), red → yellow and even → calm keep the worst
// level it reached, and a new push in the same zone after it calmed lands
// on the same card. The briefing arriving, ack, resolve and escalation are
// updates too. Staff resolving it closes the card (detector levels are
// untouched); the next level change in that zone opens a new incident.
//
// A zone returning to calm is also sent as its own short notice (a fresh
// ID, level calm, status resolved), so the timeline shows it without a card.
//
// Escalation: an incident that is red and still open (not acknowledged)
// EscalateAfter after it went red is re-broadcast with escalated:true, its
// briefing is spoken again ("Still unacknowledged. …", or the same audio
// if a new clip can't be made) and the sign and its light are forced red
// for 8 s. Once per incident. Test alerts (drills) never escalate.

// DefaultEscalateAfter is Options.EscalateAfter's default.
const DefaultEscalateAfter = 60 * time.Second

// signHoldMs: a test or escalation keeps the sign red this long.
const signHoldMs = 8000

type incident struct {
	key        string // source|zone|kind; "" = not matched by later changes (tests, calm notices)
	redAt      int64  // when it first went red (0 = never)
	briefing   bool   // a briefing was requested
	earlyBrief bool   // that briefing was an early warning's (the red one still gets its own)
	escalating bool
	notify     notify
}

// newAlertLocked logs a new alert under a fresh ID. Caller holds mu.
func (a *App) newAlertLocked(key string, al protocol.Alert, now int64) protocol.Alert {
	a.alertSeq++
	al.Type = protocol.TypeAlert
	al.ID = fmt.Sprintf("a%d-%d", now, a.alertSeq)
	if al.Status == "" {
		al.Status = protocol.StatusOpen
	}
	inc := &incident{key: key, notify: a.notifyFor(al.Zone)}
	if al.Level == protocol.LevelRed {
		inc.redAt = now
	}
	a.incidents[al.ID] = inc
	if key != "" && al.Status != protocol.StatusResolved {
		a.openInc[key] = al.ID
	}
	a.alerts = append(a.alerts, al)
	if n := len(a.alerts) - alertLogKeep; n > 0 {
		for _, old := range a.alerts[:n] {
			if inc := a.incidents[old.ID]; inc != nil && a.openInc[inc.key] == old.ID {
				delete(a.openInc, inc.key)
			}
			delete(a.incidents, old.ID)
		}
		a.alerts = append(a.alerts[:0:0], a.alerts[n:]...)
	}
	return al
}

// updateAlertLocked changes a logged alert in place and returns the result.
// Caller holds mu.
func (a *App) updateAlertLocked(id string, fn func(al *protocol.Alert, inc *incident)) (protocol.Alert, bool) {
	for i := len(a.alerts) - 1; i >= 0; i-- {
		if a.alerts[i].ID == id {
			inc := a.incidents[id]
			if inc == nil {
				inc = &incident{}
				a.incidents[id] = inc
			}
			fn(&a.alerts[i], inc)
			return a.alerts[i], true
		}
	}
	return protocol.Alert{}, false
}

// briefJob is a briefing to write for an alert.
type briefJob struct {
	id     string
	info   brief.Info
	test   bool
	replay bool
	notify notify
	early  bool // an early warning's projection: dropped if the alert went red meanwhile
}

// raiseLocked turns one level change into an alert: a new incident, an
// update of the open one, or a calm notice. It returns the alert to
// broadcast and, on the way to red, a briefing to write. early marks a
// density pre-warning (the trend, not the density, raised it to yellow):
// the incident gets early:true until it goes red, and a briefing worded as
// a projection right away; the red that may follow still gets its own.
// Caller holds mu.
func (a *App) raiseLocked(source, kind, zone, from, to string, score float64, now int64, replay, early bool, info func() brief.Info) (protocol.Alert, *briefJob) {
	alertSource := "" // live alerts carry no source
	if source != "live" {
		alertSource = source
	}
	if to == protocol.LevelCalm {
		return a.newAlertLocked("", protocol.Alert{T: now, Kind: kind, Zone: zone, Level: to, Score: score,
			Status: protocol.StatusResolved, ResolvedAt: now, Source: alertSource}, now), nil
	}
	key := source + "|" + zone + "|" + kind
	var al protocol.Alert
	ok := false
	if id, open := a.openInc[key]; open {
		al, ok = a.updateAlertLocked(id, func(al *protocol.Alert, inc *incident) {
			// Level, score and T describe the worst the incident got.
			if levelRank[to] > levelRank[al.Level] {
				al.Level, al.T, al.Score = to, now, score
			} else if to == al.Level {
				al.Score = max(al.Score, score)
			}
			if to == protocol.LevelRed {
				al.Early = false
			} else if early && al.Level == protocol.LevelYellow && !al.Early {
				// A yellow incident now projected to turn dangerous soon:
				// it becomes an early warning, timed from now.
				al.Early, al.T = true, now
			}
			if to == protocol.LevelRed && inc.redAt == 0 {
				inc.redAt = now
			}
			inc.notify = a.notifyFor(zone)
		})
	}
	if !ok {
		al = a.newAlertLocked(key, protocol.Alert{T: now, Kind: kind, Zone: zone, Level: to, Score: score,
			Early: early && to == protocol.LevelYellow, Source: alertSource}, now)
	}
	inc := a.incidents[al.ID]
	earlyJob := early && to == protocol.LevelYellow && !inc.briefing && now-a.lastBrief[zone] >= BriefCooldown
	// Red after an early warning's briefing: brief again (the cooldown was
	// spent on the projection).
	redAfterEarly := to == protocol.LevelRed && from != protocol.LevelRed && inc.earlyBrief
	redJob := to == protocol.LevelRed && from != protocol.LevelRed && !inc.briefing && now-a.lastBrief[zone] >= BriefCooldown
	if !earlyJob && !redJob && !redAfterEarly {
		return al, nil
	}
	a.lastBrief[zone] = now
	inc.briefing, inc.earlyBrief = true, earlyJob
	in := info()
	in.Message = a.messageFor(zone)
	return al, &briefJob{id: al.ID, info: in, replay: replay, notify: inc.notify, early: earlyJob}
}

func (a *App) detectTick(now int64) {
	a.mu.Lock()
	res, cch := a.live.step(now)
	rch := a.stepRules(a.live, now)
	a.packedLocked(a.live, now, false)
	a.guideLocked(a.live, now, false)
	forget(a.live, now)
	active, pnow, isReplay, src, source := a.live, now, false, "", "live"
	changes := res.Changes
	var dropped *protocol.Alerts
	ended := false
	if r := a.replay; r != nil {
		pnow = r.now(now)
		a.feedReplay(r, pnow)
		var rres detect.Result
		rres, cch = r.p.step(pnow)
		rch = a.stepRules(r.p, pnow)
		a.packedLocked(r.p, pnow, false)
		a.guideLocked(r.p, pnow, false)
		changes = rres.Changes
		active, isReplay, src, source = r.p, true, " [replay]", "replay"
		if pnow > r.recEnd+3000 {
			log.Printf("replay %s finished, back to live", r.name)
			a.replay = nil
			ended, dropped = true, a.dropSourceLocked("replay")
		}
	}
	if s := a.sim; s != nil {
		var sres detect.Result
		sres, cch = s.p.step(now)
		rch = a.stepRules(s.p, now)
		a.packedLocked(s.p, now, true)
		a.guideLocked(s.p, now, true)
		changes = sres.Changes
		active, isReplay, src, source = s.p, true, " [sim]", "sim"
		forget(s.p, now)
		// Pulse's first red alert of the run (wave, density or rule), for the lead time.
		red := false
		for _, ch := range changes {
			red = red || ch.To == protocol.LevelRed
		}
		for _, ch := range cch {
			red = red || ch.To == protocol.LevelRed
		}
		for _, ch := range rch {
			red = red || ch.to == protocol.LevelRed
		}
		if red && s.alertAt < 0 {
			s.alertAt = s.seconds(now)
		}
	}
	if ended {
		// The replay just ended: its alerts leave with it, and this last
		// tick's changes are not raised (they would outlive the replay).
		a.mu.Unlock()
		a.broadcastDropped(dropped)
		return
	}
	type out struct {
		al    protocol.Alert
		level string // the change's own level (the alert keeps its worst)
	}
	var send []out
	var jobs []*briefJob
	add := func(al protocol.Alert, level string, job *briefJob) {
		send = append(send, out{al, level})
		if job != nil {
			jobs = append(jobs, job)
		}
	}
	for _, ch := range changes {
		al, job := a.raiseLocked(source, protocol.KindWave, ch.Zone, ch.From, ch.To, round2(ch.Score), now, isReplay, false,
			func() brief.Info { return a.waveInfoLocked(active, ch.Zone, protocol.LevelRed, pnow) })
		add(al, ch.To, job)
	}
	for _, ch := range cch {
		c := ch.Cluster
		// Attributed to the drawn area holding the cluster's densest spot
		// (else that spot's zone), not to wherever its centroid falls.
		zone := active.hotspotZone(c.PeakX, c.PeakY)
		al, job := a.raiseLocked(source, protocol.KindDensity, zone, ch.From, ch.To, round2(c.Est), now, isReplay, ch.Early,
			func() brief.Info {
				in := densityInfo(active, c, zone)
				in.Exit = a.nearestExit(active, c.PeakX, c.PeakY)
				return in
			})
		add(al, ch.To, job)
	}
	for _, ch := range rch {
		al, job := a.raiseLocked(source, protocol.KindRule, ch.zone, ch.from, ch.to, ruleScore(ch), now, isReplay, false,
			func() brief.Info {
				in := ruleInfo(active, ch)
				if ch.st.phones > 0 {
					in.Exit = a.nearestExit(active, ch.st.peakX, ch.st.peakY)
				} else if x, y, ok := zoneSpot(active, ch.zone); ok {
					in.Exit = a.nearestExit(active, x, y)
				}
				return in
			})
		add(al, ch.to, job)
	}
	zoneLevels, signLevel, signZone := a.alertLevels(active)
	a.lightLevels(zoneLevels, a.opt.Sign.Zones())
	hold := now < a.signHold
	a.mu.Unlock()

	for _, o := range send {
		al := o.al
		log.Printf("%s %s: %s (score %.2f)%s", al.Kind, al.Zone, o.level, al.Score, src)
		a.Hub.BroadcastJSON(al)
		if !isReplay {
			a.opt.Sink.Alert(store.AlertRow{Time: time.UnixMilli(now), Zone: al.Zone, Level: o.level, Score: al.Score})
		}
	}
	for _, j := range jobs {
		go a.briefAndSpeak(j)
	}
	if !hold {
		a.opt.Sign.Update(zoneLevels, signLevel, signZone)
	}
	a.escalate(now)
}

// alertLevels is every zone's level for the signs: the detector's, raised
// by the clusters in it (a red cluster makes its zone red on the sign) and
// by the area's rules; and the worst zone for the worst-zone sign, leaving
// out areas whose alerts staff kept off the sign. Caller holds mu.
func (a *App) alertLevels(p *pipeline) (zoneLevels map[string]string, level, zone string) {
	zoneLevels = map[string]string{}
	score := map[string]float64{}
	for _, z := range p.last.Zones {
		zoneLevels[z.ID], score[z.ID] = p.zoneLevel(z), z.Score
	}
	for _, c := range p.clusters {
		id := p.hotspotZone(c.PeakX, c.PeakY)
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
		if !a.notifyFor(id).sign {
			continue
		}
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

// waveInfoLocked is briefInfo plus the open exit nearest the push. Caller
// holds mu.
func (a *App) waveInfoLocked(p *pipeline, zone, level string, now int64) brief.Info {
	in := briefInfo(p, zone, level, now)
	if x, y, ok := zoneSpot(p, zone); ok {
		in.Exit = a.nearestExit(p, x, y)
	}
	return in
}

// briefAndSpeak runs Gemini then ElevenLabs and updates the alert with the
// briefing. Both fail soft: template text, then the fallback clip or the
// dashboard's browser voice.
func (a *App) briefAndSpeak(j *briefJob) {
	ctx := context.Background()
	info := j.info
	bf, err := a.opt.Brief.Brief(ctx, info)
	if err != nil && !errors.Is(err, brief.ErrNoKey) {
		log.Printf("brief: %v (using template)", err)
	}
	briefErr := err
	if j.test {
		bf.Headline = drillPrefix + bf.Headline // the voice says so too
	}
	text := bf.Text()
	url := ""
	var voiceErr error
	if j.notify.voice {
		url, err = a.opt.Voice.Speak(ctx, text)
		voiceErr = err
		if err != nil {
			if !errors.Is(err, voice.ErrNoKey) {
				log.Printf("voice: %v", err)
			}
			url = ""
			if info.Zone == "B" && (info.Kind == "" || info.Kind == protocol.KindWave) && info.Message == "" {
				url = a.opt.Voice.FallbackURL() // "Zone B, crowd waves building."
			}
		}
	}
	log.Printf("brief zone %s: %s", info.Zone, text)
	a.mu.Lock()
	stale := false
	al, ok := a.updateAlertLocked(j.id, func(al *protocol.Alert, _ *incident) {
		if j.early && al.Level == protocol.LevelRed {
			stale = true // the red briefing says it better
			return
		}
		al.Brief, al.Headline, al.Action, al.AudioURL = text, bf.Headline, bf.Action, url
	})
	if j.test {
		a.drillBriefedLocked(j.id, text, url, briefErr, voiceErr, j.notify.voice)
	}
	a.mu.Unlock()
	if !ok || stale {
		return // dropped from the log meanwhile, or overtaken
	}
	a.Hub.BroadcastJSON(al)
	if !j.test && !j.replay {
		a.opt.Sink.Alert(store.AlertRow{Time: time.UnixMilli(hub.Now()), Zone: al.Zone, Level: al.Level, Score: al.Score, Brief: text})
	}
}

// ErrNoAlert: no alert with that ID (or it has dropped out of the log).
var ErrNoAlert = errors.New("no such alert")

// Audit trail limits (characters).
const (
	MaxAuditBy   = 60
	MaxAuditNote = 280
)

// ErrAudit: by or note too long.
var ErrAudit = fmt.Errorf("by must be at most %d characters and note at most %d", MaxAuditBy, MaxAuditNote)

// cleanAudit collapses whitespace and checks the lengths.
func cleanAudit(by, note string) (string, string, error) {
	by, note = strings.Join(strings.Fields(by), " "), strings.TrimSpace(note)
	if utf8.RuneCountInString(by) > MaxAuditBy || utf8.RuneCountInString(note) > MaxAuditNote {
		return "", "", ErrAudit
	}
	return by, note, nil
}

// AckAlert marks an alert acknowledged (by whom, optional) and tells every
// dashboard.
func (a *App) AckAlert(id, by string) (protocol.Alert, error) {
	by, _, err := cleanAudit(by, "")
	if err != nil {
		return protocol.Alert{}, err
	}
	now := hub.Now()
	a.mu.Lock()
	al, ok := a.updateAlertLocked(id, func(al *protocol.Alert, _ *incident) {
		if al.Status == protocol.StatusOpen {
			al.Status, al.AckAt, al.AckBy = protocol.StatusAck, now, by
		}
	})
	a.mu.Unlock()
	if !ok {
		return al, ErrNoAlert
	}
	a.Hub.BroadcastJSON(al)
	return al, nil
}

// ResolveAlert closes an alert's card. Detector levels are untouched; the
// next level change in that zone opens a new incident. by (who) and note
// (the outcome, ≤ 280 chars) are optional; a note given for an alert
// already resolved replaces its note.
func (a *App) ResolveAlert(id, by, note string) (protocol.Alert, error) {
	by, note, err := cleanAudit(by, note)
	if err != nil {
		return protocol.Alert{}, err
	}
	now := hub.Now()
	a.mu.Lock()
	al, ok := a.updateAlertLocked(id, func(al *protocol.Alert, inc *incident) {
		if al.Status != protocol.StatusResolved {
			al.Status, al.ResolvedAt, al.ResolvedBy, al.Note = protocol.StatusResolved, now, by, note
		} else if note != "" {
			al.Note = note
			if by != "" {
				al.ResolvedBy = by
			}
		}
		if inc.key != "" && a.openInc[inc.key] == id {
			delete(a.openInc, inc.key)
		}
	})
	a.mu.Unlock()
	if !ok {
		return al, ErrNoAlert
	}
	a.Hub.BroadcastJSON(al)
	return al, nil
}

// dropSourceLocked removes every alert raised from the given data source
// ("sim" or "replay") from the log: a simulation or replay that is over must
// not leave cards behind that read as real. It returns the new log to
// broadcast, nil if nothing was removed. Caller holds mu.
func (a *App) dropSourceLocked(source string) *protocol.Alerts {
	keep := []protocol.Alert{}
	for _, al := range a.alerts {
		if al.Source == source {
			if inc := a.incidents[al.ID]; inc != nil && inc.key != "" && a.openInc[inc.key] == al.ID {
				delete(a.openInc, inc.key)
			}
			delete(a.incidents, al.ID)
			continue
		}
		keep = append(keep, al)
	}
	for key := range a.openInc {
		if strings.HasPrefix(key, source+"|") {
			delete(a.openInc, key)
		}
	}
	if len(keep) == len(a.alerts) {
		return nil
	}
	a.alerts = keep
	return &protocol.Alerts{Type: protocol.TypeAlerts, Alerts: append([]protocol.Alert{}, keep...)}
}

// broadcastDropped sends the log returned by dropSourceLocked, if any.
func (a *App) broadcastDropped(out *protocol.Alerts) {
	if out != nil {
		a.Hub.BroadcastJSON(*out)
	}
}

// ClearAlerts tidies the timeline: resolved alerts (calm notices included)
// and test alerts leave the log; open and acknowledged real incidents stay.
// Every dashboard gets the new log as an alerts message, which is also
// returned.
func (a *App) ClearAlerts() protocol.Alerts {
	a.mu.Lock()
	keep := []protocol.Alert{}
	for _, al := range a.alerts {
		if al.Test || al.Status == protocol.StatusResolved {
			if inc := a.incidents[al.ID]; inc != nil && inc.key != "" && a.openInc[inc.key] == al.ID {
				delete(a.openInc, inc.key)
			}
			delete(a.incidents, al.ID)
			continue
		}
		keep = append(keep, al)
	}
	a.alerts = keep
	out := protocol.Alerts{Type: protocol.TypeAlerts, Alerts: append([]protocol.Alert{}, keep...)}
	a.mu.Unlock()
	a.Hub.BroadcastJSON(out)
	return out
}

// escalate re-announces red incidents nobody acknowledged in time.
func (a *App) escalate(now int64) {
	after := a.opt.EscalateAfter.Milliseconds()
	if after <= 0 {
		return
	}
	var due []protocol.Alert
	a.mu.Lock()
	for _, al := range a.alerts {
		inc := a.incidents[al.ID]
		if inc == nil || al.Test || al.Status != protocol.StatusOpen || al.Level != protocol.LevelRed || al.Escalated ||
			inc.escalating || inc.redAt == 0 || now-inc.redAt < after {
			continue
		}
		inc.escalating = true
		due = append(due, al)
	}
	if len(due) > 0 {
		a.signHold = now + signHoldMs
	}
	a.mu.Unlock()
	for _, al := range due {
		go a.escalateOne(al)
	}
}

func (a *App) escalateOne(al protocol.Alert) {
	a.mu.Lock()
	n := a.incidents[al.ID].notify
	a.mu.Unlock()
	log.Printf("alert %s (%s %s) still unacknowledged: escalating", al.ID, al.Kind, al.Zone)
	a.opt.Sign.ForceAlert(protocol.LevelRed, al.Zone, n.sign, n.light)
	url := al.AudioURL
	if n.voice && al.Brief != "" && a.opt.Voice.Enabled() {
		if u, err := a.opt.Voice.Speak(context.Background(), "Still unacknowledged. "+al.Brief); err == nil {
			url = u
		} else if !errors.Is(err, voice.ErrNoKey) {
			log.Printf("voice: %v (re-using the first clip)", err)
		}
	}
	a.mu.Lock()
	up, ok := a.updateAlertLocked(al.ID, func(x *protocol.Alert, _ *incident) {
		if x.Status == protocol.StatusOpen { // not acknowledged while the clip was made
			x.Escalated = true
			if url != "" {
				x.AudioURL = url
			}
		}
	})
	a.mu.Unlock()
	if ok && up.Escalated {
		a.Hub.BroadcastJSON(up)
	}
}
