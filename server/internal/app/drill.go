package app

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/Drivera0/crowd-crush/server/internal/brief"
	"github.com/Drivera0/crowd-crush/server/internal/hub"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/voice"
)

// Alert drills: the test alert, with the place, level, kind of incident and
// outputs chosen by staff (protocol/drill.go). A drill
//
//   - is an incident with test:true on every dashboard (card, timeline,
//     banner), and its briefing starts "This is a drill.", so the voice says
//     so too;
//   - never escalates (alerts.go) and stays out of the history given to
//     Gemini;
//   - does not touch detector state, so attendee phones are not told: their
//     screens follow the detector's levels only;
//   - shows on the signs and zone lights chosen for it as a real alert of
//     that level would, for signHoldMs (8 s), after which they go back to
//     what the detector says. The boards cannot say "drill".
//
// What each output did is kept with the drill (the last DrillKeep) and
// served by GET /api/drill together with every output's readiness.

// drillPrefix starts a drill's briefing headline.
const drillPrefix = "This is a drill. "

// ErrDrill: a drill request that makes no sense.
var ErrDrill = errors.New("drill")

func drillErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrDrill, fmt.Sprintf(format, args...))
}

// drillZonesLocked lists where a drill can go: the active pipeline's zones
// (drawn areas first, then the default zones or "rest"). Caller holds mu.
func (a *App) drillZonesLocked(p *pipeline) []protocol.DrillZone {
	var out []protocol.DrillZone
	for _, z := range p.last.Zones {
		n := a.notifyFor(z.ID)
		rules := a.rulesFor(z.ID)
		lightOn := rules == nil || rules.Notify == nil || on(rules.Notify.Light)
		dz := protocol.DrillZone{ID: z.ID, Name: zoneLabel(p, z.ID), Custom: z.Custom, Sign: n.sign, Voice: n.voice}
		if lightOn {
			dz.Light = a.lightFor(z.ID)
		}
		out = append(out, dz)
	}
	if len(out) == 0 {
		// No detector step yet: the saved areas.
		for _, ar := range a.areas {
			n := a.notifyFor(ar.ID)
			out = append(out, protocol.DrillZone{ID: ar.ID, Name: ar.Name, Custom: true, Light: n.light, Sign: n.sign, Voice: n.voice})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Custom && !out[j].Custom })
	return out
}

// drillReadyLocked is every output's readiness right now. Caller holds mu.
func (a *App) drillReadyLocked() []protocol.DrillReady {
	out := []protocol.DrillReady{{Key: "briefing", Label: "Briefing", State: protocol.ReadyOK, Note: "written by Gemini"}}
	if !a.opt.Brief.Enabled() {
		out[0].State, out[0].Note = protocol.ReadyFallback, "Gemini is not configured: a template sentence is used"
	}
	v := protocol.DrillReady{Key: "voice", Label: "Voice", State: protocol.ReadyOK, Note: "spoken by ElevenLabs"}
	if !a.opt.Voice.Enabled() {
		v.State, v.Note = protocol.ReadyFallback, "ElevenLabs is not configured: this console reads it in the browser's voice when spoken alerts are on"
	}
	out = append(out, v)
	signs, online, lastErr := 0, 0, ""
	var lights []protocol.DrillReady
	for _, h := range a.hw {
		state, note := protocol.ReadyOK, "online"
		if !h.Online {
			state, note = protocol.ReadyOffline, "offline"
			if h.Error != "" {
				note = "offline: " + h.Error
			}
		}
		if h.Zone == "" {
			signs++
			if h.Online {
				online++
			} else {
				lastErr = note
			}
			continue
		}
		lights = append(lights, protocol.DrillReady{Key: "light:" + h.Zone, Label: "Zone light " + h.Zone, State: state, Note: note,
			Areas: lightAreas(a.areas, h.Zone)})
	}
	// Boards in SIGN_URL that have not been probed yet still count.
	if len(a.hw) == 0 && a.opt.Sign.Enabled() {
		for _, k := range a.opt.Sign.Keys() {
			if k == "sign" {
				signs++
				lastErr = "not checked yet"
			} else {
				lights = append(lights, protocol.DrillReady{Key: "light:" + k, Label: "Zone light " + k, State: protocol.ReadyOffline,
					Note: "not checked yet", Areas: lightAreas(a.areas, k)})
			}
		}
	}
	s := protocol.DrillReady{Key: "sign", Label: "Sign"}
	switch {
	case signs == 0:
		s.State, s.Note = protocol.ReadyNone, "no sign is connected (SIGN_URL)"
	case online == signs:
		s.State, s.Note = protocol.ReadyOK, "online"
		if signs > 1 {
			s.Note = fmt.Sprintf("%d signs online", signs)
		}
	case online > 0:
		s.State, s.Note = protocol.ReadyOK, fmt.Sprintf("%d of %d signs online", online, signs)
	default:
		s.State, s.Note = protocol.ReadyOffline, lastErr
	}
	out = append(out, s)
	return append(out, lights...)
}

// DrillStatus is GET /api/drill.
func (a *App) DrillStatus() protocol.DrillStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := protocol.DrillStatus{Zones: a.drillZonesLocked(a.active()), Outputs: a.drillReadyLocked(), History: []protocol.DrillRecord{}}
	if st.Zones == nil {
		st.Zones = []protocol.DrillZone{}
	}
	for i := len(a.drills) - 1; i >= 0; i-- {
		d := a.drills[i]
		d.Outputs = append([]protocol.DrillOutput(nil), d.Outputs...)
		st.History = append(st.History, d)
	}
	return st
}

// TestAlert is the one-button drill (an empty DrillRequest); it returns the
// zone it went to.
func (a *App) TestAlert() string {
	d, _ := a.Drill(protocol.DrillRequest{})
	return d.Zone
}

// Drill runs the alert chain for a test alert: a new incident (test:true)
// that can be acknowledged and resolved like a real one, the briefing and
// voice (if asked for), and the sign and zone lights (if asked for), without
// touching detector state. See the top of this file for what a drill is.
func (a *App) Drill(req protocol.DrillRequest) (protocol.DrillRecord, error) {
	level := req.Level
	if level == "" {
		level = protocol.LevelRed
	}
	if level != protocol.LevelYellow && level != protocol.LevelRed {
		return protocol.DrillRecord{}, drillErr("level must be yellow or red")
	}
	kind := req.Kind
	if kind == "" {
		kind = protocol.KindWave
	}
	if kind != protocol.KindWave && kind != protocol.KindDensity && kind != protocol.KindRule {
		return protocol.DrillRecord{}, drillErr("kind must be wave, density or rule")
	}
	now := hub.Now()
	a.mu.Lock()
	p := a.active()
	zone := req.Zone
	if zone == "" {
		// The zone that looks worst, else B (the fallback clip's zone), else the first.
		best := 0.1
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
	} else if !a.knownZoneLocked(p, zone) {
		a.mu.Unlock()
		return protocol.DrillRecord{}, drillErr("no area or zone %q", zone)
	}
	n := a.notifyFor(zone)
	// What to exercise: the request's choices, else what the area's rules allow.
	wantBrief, wantVoice, wantSign := true, n.voice, n.sign
	lights := []string{}
	if n.light != "" {
		lights = append(lights, n.light)
	}
	voiceNote, signNote := "", ""
	if !n.voice {
		voiceNote = "switched off for this area"
	}
	if !n.sign {
		signNote = "switched off for this area"
	}
	if o := req.Outputs; o != nil {
		if o.Briefing != nil {
			wantBrief = *o.Briefing
		}
		if o.Voice != nil {
			wantVoice, voiceNote = *o.Voice, "not chosen for this drill"
		}
		if o.Sign != nil {
			wantSign, signNote = *o.Sign, "not chosen for this drill"
		}
		if o.Lights != nil {
			lights = lights[:0]
			seen := map[string]bool{}
			for _, l := range *o.Lights {
				l = strings.ToUpper(strings.TrimSpace(l))
				if l != "" && !seen[l] {
					seen[l] = true
					lights = append(lights, l)
				}
			}
		}
	}
	info := a.drillInfoLocked(p, zone, level, kind, now)
	score := lastScore(info)
	switch kind {
	case protocol.KindDensity:
		score = info.Density
	case protocol.KindRule:
		score = float64(info.People)
	}
	a.signHold = now + signHoldMs
	al := a.newAlertLocked("", protocol.Alert{T: now, Kind: kind, Zone: zone, Level: level, Score: score, Test: true}, now)
	inc := a.incidents[al.ID]
	inc.briefing = true
	inc.notify = notify{sign: wantSign, voice: wantVoice && wantBrief}

	rec := protocol.DrillRecord{ID: al.ID, T: now, Zone: zone, Where: zoneLabel(p, zone), Level: level, Kind: kind}
	add := func(key, label, state, note string) {
		rec.Outputs = append(rec.Outputs, protocol.DrillOutput{Key: key, Label: label, State: state, Note: note})
	}
	ready := map[string]protocol.DrillReady{}
	for _, r := range a.drillReadyLocked() {
		ready[r.Key] = r
	}
	if wantBrief {
		add("briefing", "Briefing", protocol.DrillPending, "")
	} else {
		add("briefing", "Briefing", protocol.DrillSkipped, "not chosen for this drill")
	}
	switch {
	case wantVoice && wantBrief:
		add("voice", "Voice", protocol.DrillPending, "")
	case wantVoice:
		add("voice", "Voice", protocol.DrillSkipped, "nothing to read out: the briefing is off")
	default:
		add("voice", "Voice", protocol.DrillSkipped, voiceNote)
	}
	board := func(key, label string) {
		switch r := ready[key]; r.State {
		case protocol.ReadyOK:
			add(key, label, protocol.DrillOK, "sent: shows "+level+" for 8 s")
		case protocol.ReadyOffline:
			add(key, label, protocol.DrillFailed, "sent, but the board is "+r.Note)
		default:
			add(key, label, protocol.DrillSkipped, "none connected")
		}
	}
	if wantSign {
		board("sign", "Sign")
	} else {
		add("sign", "Sign", protocol.DrillSkipped, signNote)
	}
	for _, l := range lights {
		board("light:"+l, "Zone light "+l)
	}
	if len(lights) == 0 && req.Outputs == nil && a.lightFor(zone) != "" {
		add("light:"+a.lightFor(zone), "Zone light "+a.lightFor(zone), protocol.DrillSkipped, "switched off for this area")
	}
	a.drills = append(a.drills, rec)
	if n := len(a.drills) - protocol.DrillKeep; n > 0 {
		a.drills = append(a.drills[:0:0], a.drills[n:]...)
	}
	rec.Outputs = append([]protocol.DrillOutput(nil), rec.Outputs...)
	a.mu.Unlock()

	a.Hub.BroadcastJSON(al)
	if wantSign {
		a.opt.Sign.ForceAlert(level, zone, true, "")
	}
	for _, l := range lights {
		a.opt.Sign.ForceAlert(level, zone, false, l)
	}
	if wantBrief {
		go a.briefAndSpeak(&briefJob{id: al.ID, info: info, test: true, notify: notify{voice: wantVoice}})
	}
	return rec, nil
}

// knownZoneLocked reports whether zone is a zone of pipeline p or a saved
// area. Caller holds mu.
func (a *App) knownZoneLocked(p *pipeline, zone string) bool {
	if _, ok := p.last.Zone(zone); ok {
		return true
	}
	for _, ar := range a.areas {
		if ar.ID == zone {
			return true
		}
	}
	return false
}

// drillInfoLocked is what the drill's briefing is written from: the zone's
// real numbers where it has any, and plausible ones for the kind of
// incident being rehearsed. Caller holds mu.
func (a *App) drillInfoLocked(p *pipeline, zone, level, kind string, now int64) brief.Info {
	info := a.waveInfoLocked(p, zone, level, a.pnowLocked(now))
	info.Message = a.messageFor(zone)
	cfg := p.cfg()
	switch kind {
	case protocol.KindDensity:
		info.Kind = protocol.KindDensity
		info.Density = cfg.DensityWatch + 0.5
		if level == protocol.LevelRed {
			info.Density = cfg.DensityDanger + 0.5
		}
		info.AreaM2 = math.Round(localDiscM2)
		info.People = int(math.Round(info.Density * info.AreaM2))
		info.Trend, info.Danger = "forming", cfg.DensityDanger
		if x, y, ok := zoneSpot(p, zone); ok {
			info.X, info.Y = math.Round(x), math.Round(y)
		}
	case protocol.KindRule:
		info.Kind, info.Rule, info.Limit = protocol.KindRule, "capacity", 100
		if r := a.rulesFor(zone); r != nil && r.MaxPhones > 0 {
			info.Limit = float64(r.MaxPhones)
		}
		info.People = int(math.Round(info.Limit * 1.2))
	default:
		if info.Direction == "" {
			info.Direction, info.LagMs = "+x", 250
		}
	}
	return info
}

// drillBriefedLocked records how a drill's briefing and voice went. Caller
// holds mu.
func (a *App) drillBriefedLocked(id, text, audioURL string, briefErr, voiceErr error, wantVoice bool) {
	for i := range a.drills {
		d := &a.drills[i]
		if d.ID != id {
			continue
		}
		d.Brief, d.AudioURL = text, audioURL
		for k := range d.Outputs {
			o := &d.Outputs[k]
			if o.State != protocol.DrillPending {
				continue
			}
			switch o.Key {
			case "briefing":
				o.State = protocol.DrillOK
				switch {
				case briefErr == nil:
					o.Note = "written by Gemini"
				case errors.Is(briefErr, brief.ErrNoKey):
					o.Note = "template text (Gemini is not configured)"
				default:
					o.Note = "template text: Gemini failed (" + shortErr(briefErr) + ")"
				}
			case "voice":
				switch {
				case !wantVoice:
					o.State, o.Note = protocol.DrillSkipped, "not chosen for this drill"
				case voiceErr == nil && audioURL != "":
					o.State, o.Note = protocol.DrillOK, "clip made by ElevenLabs"
				case errors.Is(voiceErr, voice.ErrNoKey) && audioURL != "":
					o.State, o.Note = protocol.DrillOK, "the pre-made fallback clip (ElevenLabs is not configured)"
				case errors.Is(voiceErr, voice.ErrNoKey):
					o.State, o.Note = protocol.DrillSkipped, "ElevenLabs is not configured: this console reads it in the browser's voice when spoken alerts are on"
				default:
					o.State, o.Note = protocol.DrillFailed, "ElevenLabs failed ("+shortErr(voiceErr)+"); the browser's voice reads it instead"
				}
			}
		}
		return
	}
}

// shortErr is an error for a status line.
func shortErr(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}
