package app

import (
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/Drivera0/crowd-crush/server/internal/brief"
	"github.com/Drivera0/crowd-crush/server/internal/crowd"
	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// Per-area alert rules (protocol.AlertRules), on top of the detector.
//
//   - density: the estimated people/m² inside the area, the same estimate
//     clusters use (max(phones ÷ area, peak local density) ÷ participation),
//     above the limit for DensityHoldS (default 5 s) is red; above 75 % of
//     it for as long is yellow. Each clears 10 % (Margin) below its
//     threshold, like the zone and cluster levels.
//   - maxPhones: more non-stale phones inside than the limit for over
//     CapacityHoldMs is red; it clears as soon as the count is back at or
//     under the limit.
//   - push false: the zone never escalates from wave edges (detect.ZoneDef.NoPush).
//
// The area's rule level is the worst of the two; it merges into the zone
// level shown in snapshots, on phones, signs and lights.
const (
	MaxRuleMessage     = 140
	MaxRuleDensity     = 20.0 // people/m²
	MaxRuleHoldS       = 600
	MaxRulePhones      = 100_000
	DefaultRuleHoldS   = 5
	CapacityHoldMs     = 3000
	ruleYellowFraction = 0.75
)

func validRules(id string, r *protocol.AlertRules) (*protocol.AlertRules, error) {
	if r == nil {
		return nil, nil
	}
	out := *r
	switch {
	case !finite(out.Density) || out.Density < 0 || out.Density > MaxRuleDensity:
		return nil, fmt.Errorf("area %q: density rule must be between 0 (off) and %g people/m²", id, MaxRuleDensity)
	case out.DensityHoldS < 0 || out.DensityHoldS > MaxRuleHoldS:
		return nil, fmt.Errorf("area %q: densityHoldS must be between 0 and %d s", id, MaxRuleHoldS)
	case out.MaxPhones < 0 || out.MaxPhones > MaxRulePhones:
		return nil, fmt.Errorf("area %q: maxPhones must be between 0 (off) and %d", id, MaxRulePhones)
	}
	out.Message = strings.Join(strings.Fields(out.Message), " ")
	if utf8.RuneCountInString(out.Message) > MaxRuleMessage {
		return nil, fmt.Errorf("area %q: message longer than %d characters", id, MaxRuleMessage)
	}
	if out.Notify != nil {
		n := *out.Notify
		out.Notify = &n
	}
	return &out, nil
}

func cloneRules(r *protocol.AlertRules) *protocol.AlertRules {
	if r == nil {
		return nil
	}
	out := *r
	if r.Push != nil {
		v := *r.Push
		out.Push = &v
	}
	if r.Notify != nil {
		n := *r.Notify
		for _, p := range []**bool{&n.Sign, &n.Light, &n.Voice} {
			if *p != nil {
				v := **p
				*p = &v
			}
		}
		out.Notify = &n
	}
	return &out
}

func on(b *bool) bool { return b == nil || *b }

// pushOff reports whether an area turned push detection off.
func pushOff(r *protocol.AlertRules) bool { return r != nil && !on(r.Push) }

// notify is where one zone's alerts go.
type notify struct {
	sign, voice bool
	light       string // the zone light to show it on, "" = none (or turned off)
}

// rulesFor is a zone's rules (nil for default zones and "rest"). Caller holds mu.
func (a *App) rulesFor(zone string) *protocol.AlertRules {
	for _, ar := range a.areas {
		if ar.ID == zone {
			return ar.Rules
		}
	}
	return nil
}

// notifyFor says where a zone's alerts go. Caller holds mu.
func (a *App) notifyFor(zone string) notify {
	n := notify{sign: true, voice: true, light: a.lightFor(zone)}
	if r := a.rulesFor(zone); r != nil && r.Notify != nil {
		n.sign, n.voice = on(r.Notify.Sign), on(r.Notify.Voice)
		if !on(r.Notify.Light) {
			n.light = ""
		}
	}
	return n
}

// messageFor is the staff-set action for a zone's alerts, if any. Caller holds mu.
func (a *App) messageFor(zone string) string {
	if r := a.rulesFor(zone); r != nil {
		return r.Message
	}
	return ""
}

// ruleState is one area's rule machine in one pipeline.
type ruleState struct {
	dens      detect.LevelState
	overSince int64 // phones above maxPhones since (0 = not above)
	capRed    bool
	level     string
	rule      string  // what drove the level last: density | capacity
	est       float64 // estimated people/m² inside, last step
	phones    int
	areaM2    float64
	peakX     float64
	peakY     float64
}

// ruleChange is an area's rule level changing.
type ruleChange struct {
	zone, from, to string
	rule           string // density | capacity: what drives the new level (or drove the old one)
	st             ruleState
	limit          float64
}

// stepRules evaluates every area's rules on pipeline p's latest step.
// Caller holds mu.
func (a *App) stepRules(p *pipeline, now int64) []ruleChange {
	if p.rules == nil {
		p.rules = map[string]*ruleState{}
	}
	cfg := p.cfg()
	part := cfg.Participation
	if part <= 0 {
		part = 1
	}
	seen := map[string]bool{}
	var out []ruleChange
	for _, ar := range a.areas {
		seen[ar.ID] = true
		st := p.rules[ar.ID]
		if st == nil {
			st = &ruleState{dens: detect.NewLevelState(), level: protocol.LevelCalm}
			p.rules[ar.ID] = st
		}
		r := ar.Rules
		var pts []crowd.Point
		for _, ph := range p.last.Phones {
			m := p.meta[ph.ID]
			if ph.Status == protocol.StatusStale || (m != nil && !m.connected) || !inZone(p, ph, ar.ID) {
				continue
			}
			pts = append(pts, crowd.Point{ID: ph.ID, X: ph.X, Y: ph.Y})
		}
		st.phones = len(pts)
		st.areaM2 = math.Max(1, polyArea(ar.Poly, cfg))
		peak, px, py := crowd.LocalPeak(pts)
		st.est = math.Max(float64(len(pts))/st.areaM2, peak) / part
		st.peakX, st.peakY = px, py

		denLevel := protocol.LevelCalm
		if r != nil && r.Density > 0 {
			hold := r.DensityHoldS
			if hold <= 0 {
				hold = DefaultRuleHoldS
			}
			y := r.Density * ruleYellowFraction
			th := detect.Thresholds{Yellow: y, Red: r.Density, YMargin: y * cfg.Margin, RMargin: r.Density * cfg.Margin, Hold: int64(hold) * 1000}
			st.dens.Update(now, st.est, th)
			denLevel = st.dens.Level
		} else {
			st.dens = detect.NewLevelState()
		}
		if r != nil && r.MaxPhones > 0 && st.phones > r.MaxPhones {
			if st.overSince == 0 {
				st.overSince = now
			}
			st.capRed = now-st.overSince > CapacityHoldMs
		} else {
			st.overSince, st.capRed = 0, false
		}

		level, rule := denLevel, "density"
		if st.capRed {
			level, rule = protocol.LevelRed, "capacity"
		}
		if level != st.level {
			if level == protocol.LevelCalm {
				rule = st.rule // say what cleared
			}
			ch := ruleChange{zone: ar.ID, from: st.level, to: level, rule: rule, st: *st}
			st.rule = rule
			if r != nil {
				if ch.rule == "capacity" {
					ch.limit = float64(r.MaxPhones)
				} else {
					ch.limit = r.Density
				}
			}
			st.level = level
			ch.st.level = level
			out = append(out, ch)
		}
	}
	for id := range p.rules {
		if !seen[id] {
			delete(p.rules, id)
		}
	}
	return out
}

// ruleLevel is an area's rule level in pipeline p ("" = no rules state).
func (p *pipeline) ruleLevel(zone string) string {
	if st := p.rules[zone]; st != nil {
		return st.level
	}
	return ""
}

// zoneLevel is a zone's level as shown everywhere: the worst of the
// detector's and the area's rules.
func (p *pipeline) zoneLevel(z detect.ZoneResult) string {
	lv := z.Level
	if rl := p.ruleLevel(z.ID); levelRank[rl] > levelRank[lv] {
		lv = rl
	}
	return lv
}

// ruleInfo describes a rule alert for the briefing.
func ruleInfo(p *pipeline, ch ruleChange) brief.Info {
	in := brief.Info{Kind: protocol.KindRule, Rule: ch.rule, Limit: ch.limit, Zone: ch.zone, Where: zoneName(p, ch.zone),
		Level: ch.to, Phones: ch.st.phones, Density: round2(ch.st.est), AreaM2: math.Round(ch.st.areaM2*10) / 10,
		X: math.Round(ch.st.peakX), Y: math.Round(ch.st.peakY)}
	part := p.cfg().Participation
	if part <= 0 {
		part = 1
	}
	in.People = int(math.Round(float64(ch.st.phones) / part))
	return in
}

// ruleScore is what a rule alert reports as its score: phones for
// capacity, estimated people/m² for density.
func ruleScore(ch ruleChange) float64 {
	if ch.rule == "capacity" {
		return float64(ch.st.phones)
	}
	return round2(ch.st.est)
}

// polyArea is a polygon's area (m²) after clamping to the venue.
func polyArea(poly []protocol.Point, cfg detect.Config) float64 {
	var s float64
	for i := range poly {
		x0, y0 := cfg.Clamp(poly[i][0], poly[i][1])
		x1, y1 := cfg.Clamp(poly[(i+1)%len(poly)][0], poly[(i+1)%len(poly)][1])
		s += x0*y1 - x1*y0
	}
	return math.Abs(s) / 2
}
