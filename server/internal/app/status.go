package app

import (
	"math"

	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// The overall status (Snapshot.status) is the one answer to "how bad is it
// and where": the worst of
//   - each zone's wave detector (score vs the zone's thresholds),
//   - each area's rules (estimated people/m² vs its density limit,
//     estimated people vs its capacity),
//   - each cluster (Est vs densityWatch / densityDanger; an early warning
//     is kind "early"),
//
// by level first, then by risk. Risk maps each signal onto one 0..1 scale
// banded by level: below the yellow threshold 0 … 0.5, yellow → red
// threshold 0.5 … 0.8, above red 0.8 … 1 (reaching 1 at twice the red
// threshold), then clamped into the band of the level the signal's state
// machine is actually at (hold times and hysteresis decide levels, not the
// raw value): calm < 0.5 ≤ yellow < 0.8 ≤ red. Score is the worst risk.

// risk maps value v with yellow and red thresholds y < r onto 0..1, inside
// the band of level.
func risk(v, y, r float64, level string) float64 {
	var s float64
	switch {
	case !(y > 0 && r > y) || v <= 0:
		s = 0
	case v < y:
		s = 0.5 * v / y
	case v < r:
		s = 0.5 + 0.3*(v-y)/(r-y)
	default:
		s = 0.8 + 0.2*math.Min(1, (v-r)/r)
	}
	switch level {
	case protocol.LevelRed:
		s = math.Max(s, 0.8)
	case protocol.LevelYellow:
		s = math.Min(math.Max(s, 0.5), 0.79)
	default:
		s = math.Min(s, 0.49)
	}
	return s
}

// statusLocked is pipeline p's overall status. Caller holds mu.
func (a *App) statusLocked(p *pipeline) protocol.Status {
	cfg := p.cfg()
	best := protocol.Status{Level: protocol.LevelCalm}
	consider := func(c protocol.Status) {
		if levelRank[c.Level] > levelRank[best.Level] || (c.Level == best.Level && c.Score > best.Score) {
			best = c
		}
	}
	for _, z := range p.last.Zones {
		th := cfg.ZoneThresholds(z.Sens)
		consider(protocol.Status{Level: z.Level, Score: risk(z.Score, th.Yellow, th.Red, z.Level),
			Zone: z.ID, Where: zoneLabel(p, z.ID), Kind: protocol.StatusKindWave})
		st, r := p.rules[z.ID], a.rulesFor(z.ID)
		if st == nil || r == nil {
			continue
		}
		if r.Density > 0 {
			lv := st.dens.Level
			consider(protocol.Status{Level: lv, Score: risk(st.est, r.Density*ruleYellowFraction, r.Density, lv),
				Zone: z.ID, Where: zoneLabel(p, z.ID), Kind: protocol.StatusKindRule, Density: round2(st.est)})
		}
		if r.MaxPhones > 0 {
			lv := protocol.LevelCalm
			if st.capRed {
				lv = protocol.LevelRed
			}
			limit := float64(r.MaxPhones)
			consider(protocol.Status{Level: lv, Score: risk(st.people, limit*ruleYellowFraction, limit, lv),
				Zone: z.ID, Where: zoneLabel(p, z.ID), Kind: protocol.StatusKindRule})
		}
	}
	for _, c := range p.clusters {
		zone := p.hotspotZone(c.PeakX, c.PeakY)
		kind := protocol.StatusKindDensity
		if c.Early && c.Level == protocol.LevelYellow {
			kind = protocol.StatusKindEarly
		}
		consider(protocol.Status{Level: c.Level, Score: risk(c.Est, cfg.DensityWatch, cfg.DensityDanger, c.Level),
			Zone: zone, Where: zoneLabel(p, zone), Kind: kind, Density: round2(c.Est)})
	}
	best.Score = round2(best.Score)
	if best.Level == protocol.LevelCalm {
		best = protocol.Status{Level: protocol.LevelCalm, Score: best.Score}
	}
	return best
}

// hotspotZone is the zone a spot belongs to for alerts and briefings: the
// drawn area containing it if there is one, else its zone (a default zone
// or "rest"); "" outside every zone.
func (p *pipeline) hotspotZone(x, y float64) string {
	for _, id := range p.det.ZonesOf(x, y) {
		if z, ok := p.last.Zone(id); ok && z.Custom {
			return id
		}
	}
	return p.det.ZoneOf(x, y)
}

// zoneLabel is a zone's name for people, never its id.
func zoneLabel(p *pipeline, zone string) string {
	if n := zoneName(p, zone); n != "" {
		return n
	}
	if z, ok := p.last.Zone(zone); ok && z.Custom {
		return "an unnamed area"
	}
	if zone == "" {
		return ""
	}
	return "Zone " + zone
}

// zoneSpot is where a wave alert is happening: the middle of the zone's
// phones in a wave (else swaying, else the zone outline's mean point).
func zoneSpot(p *pipeline, zone string) (x, y float64, ok bool) {
	for _, want := range []string{protocol.StatusWave, protocol.StatusSwaying} {
		n := 0
		x, y = 0, 0
		for _, ph := range p.last.Phones {
			if ph.Status == want && inZone(p, ph, zone) {
				x, y, n = x+ph.X, y+ph.Y, n+1
			}
		}
		if n > 0 {
			return x / float64(n), y / float64(n), true
		}
	}
	z, found := p.last.Zone(zone)
	if !found || len(z.Poly) == 0 {
		return 0, 0, false
	}
	x, y = 0, 0
	for _, pt := range z.Poly {
		x, y = x+pt[0], y+pt[1]
	}
	return x / float64(len(z.Poly)), y / float64(len(z.Poly)), true
}

// nearestExit is the name of the open exit closest to (x, y) in pipeline
// p's venue: the venue layout's exits (live, replay), or the simulation's
// geometry built from it (every exit assumed open, as for guidance). ""
// when the venue has none. Caller holds mu.
func (a *App) nearestExit(p *pipeline, x, y float64) string {
	cfg := p.cfg()
	sim := a.sim != nil && a.sim.p == p
	g := a.guideGeom(cfg.VenueW, cfg.VenueH, sim)
	name, best := "", math.Inf(1)
	for _, e := range g.Exits {
		if d := segDist(x, y, e.X0, e.Y0, e.X1, e.Y1); d < best {
			name, best = e.Name, d
			if name == "" {
				name = "the nearest exit"
			}
		}
	}
	return name
}

// segDist is the distance from (px, py) to the segment (x0, y0)–(x1, y1).
func segDist(px, py, x0, y0, x1, y1 float64) float64 {
	dx, dy := x1-x0, y1-y0
	l2 := dx*dx + dy*dy
	t := 0.0
	if l2 > 0 {
		t = math.Max(0, math.Min(1, ((px-x0)*dx+(py-y0)*dy)/l2))
	}
	return math.Hypot(px-(x0+t*dx), py-(y0+t*dy))
}
