package app

import (
	"math"

	"github.com/Drivera0/crowd-crush/server/internal/crowd"
	"github.com/Drivera0/crowd-crush/server/internal/crowdsim"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// Personal guidance ("move this way", crowd.Guide): after each step, every
// phone in danger gets a direction to move, sent in its state message.
// Danger = a red zone (detector or area rules) containing it, membership of
// a yellow or red cluster, or its node status "wave". The reason is "push"
// when the phone is on a wave edge, or its zone is red from the wave
// detector; the push direction is the travel direction of its wave edges,
// else the zone's wave direction. Otherwise the reason is "density"
// (including danger from a staff rule). Inside an area that a rule turned
// red (capacity, density), the direction leads out of the area: toward the
// nearest point of its outline, blended with lower density.
//
// The venue geometry (walls, stage, open exits) comes from the venue layout
// for live and replay; for the simulation it is the sim's geometry as built
// from the same layout (crowdsim.LayoutGeometry), with every exit open.

// guideLocked updates p.moves. Caller holds mu.
func (a *App) guideLocked(p *pipeline, now int64, sim bool) {
	zoneLevel := map[string]string{}
	zoneRes := map[string]string{} // detector level and direction, for pushes
	zoneDir := map[string]string{}
	leave := map[string][][2]float64{} // areas red because of a staff rule
	for _, z := range p.last.Zones {
		zoneLevel[z.ID] = p.zoneLevel(z)
		zoneRes[z.ID], zoneDir[z.ID] = z.Level, z.Direction
		if z.Custom && p.ruleLevel(z.ID) == protocol.LevelRed {
			leave[z.ID] = z.Poly
		}
	}
	inCluster := map[string]bool{}
	for _, c := range p.clusters {
		if c.Level == protocol.LevelYellow || c.Level == protocol.LevelRed {
			for _, id := range c.Members {
				inCluster[id] = true
			}
		}
	}
	// Travel direction of the wave edges touching each phone.
	push := map[string][2]float64{}
	for _, e := range p.last.Edges {
		if !e.Wave {
			continue
		}
		ma, mb := p.meta[e.From], p.meta[e.To]
		if ma == nil || mb == nil {
			continue
		}
		dx, dy := mb.x-ma.x, mb.y-ma.y
		if e.LagMs < 0 {
			dx, dy = -dx, -dy
		}
		l := math.Hypot(dx, dy)
		if l < 1e-9 {
			continue
		}
		for _, id := range []string{e.From, e.To} {
			v := push[id]
			push[id] = [2]float64{v[0] + dx/l, v[1] + dy/l}
		}
	}
	var in []crowd.GuideIn
	for _, pr := range p.last.Phones {
		m := p.meta[pr.ID]
		if m == nil || !m.connected || pr.Outside || pr.Status == protocol.StatusStale {
			continue
		}
		red, waveRed, dir := false, false, ""
		var out [][2]float64
		for _, z := range p.det.ZonesOf(m.x, m.y) {
			if zoneLevel[z] == protocol.LevelRed {
				red = true
			}
			if leave[z] != nil && out == nil {
				out = leave[z]
			}
			if zoneRes[z] == protocol.LevelRed && zoneDir[z] != "" {
				waveRed, dir = true, zoneDir[z]
			}
		}
		wave := pr.Status == protocol.StatusWave
		// Packed in (packed.go) is danger too, whatever its cluster says.
		if !red && !wave && !inCluster[pr.ID] && p.packed[pr.ID].Level != protocol.LevelRed {
			continue
		}
		g := crowd.GuideIn{ID: pr.ID, X: m.x, Y: m.y, Acc: m.acc, Reason: protocol.ReasonDensity, Leave: out}
		if v := push[pr.ID]; wave && (v[0] != 0 || v[1] != 0) {
			g.Reason, g.PushX, g.PushY = protocol.ReasonPush, v[0], v[1]
		} else if waveRed || wave {
			if dx, dy := dirVector(dir); dx != 0 || dy != 0 {
				g.Reason, g.PushX, g.PushY = protocol.ReasonPush, dx, dy
			}
		}
		in = append(in, g)
	}
	cfg := p.cfg()
	p.moves = p.guide.Update(now, p.pts, cfg.Participation, a.guideGeom(cfg.VenueW, cfg.VenueH, sim), in)
}

// guideGeom is the venue for guidance. Caller holds mu.
func (a *App) guideGeom(w, h float64, sim bool) crowd.Geom {
	g := crowd.Geom{W: w, H: h}
	l := a.venue.Layout
	if sim {
		exits, walls := crowdsim.GeometryJSON(crowdsim.LayoutGeometry(w, h, l))
		g.Walls = walls
		for _, e := range exits {
			if e.Open {
				g.Exits = append(g.Exits, crowd.Exit{ID: e.ID, Name: e.Name, X0: e.X0, Y0: e.Y0, X1: e.X1, Y1: e.Y1})
			}
		}
		return g
	}
	if l == nil {
		return g
	}
	g.Walls = append(g.Walls, l.Walls...)
	for i, pt := range l.Stage {
		q := l.Stage[(i+1)%len(l.Stage)]
		g.Walls = append(g.Walls, [4]float64{pt[0], pt[1], q[0], q[1]})
	}
	for _, e := range l.Exits {
		g.Exits = append(g.Exits, crowd.Exit{ID: e.ID, Name: e.Name, X0: e.X0, Y0: e.Y0, X1: e.X1, Y1: e.Y1})
	}
	return g
}

// dirVector turns a zone's wave direction (+x, -y, …) into a unit vector.
func dirVector(d string) (float64, float64) {
	switch d {
	case "+x", "+col":
		return 1, 0
	case "-x", "-col":
		return -1, 0
	case "+y", "+row":
		return 0, 1
	case "-y", "-row":
		return 0, -1
	}
	return 0, 0
}

// moveFor is the wire form of a phone's guidance, nil when it is safe.
func moveFor(p *pipeline, id string) *protocol.Move {
	mv, ok := p.moves[id]
	if !ok {
		return nil
	}
	return &protocol.Move{DX: round2(mv.DX), DY: round2(mv.DY), To: mv.To, Reason: mv.Reason, Conf: math.Round(mv.Conf*10) / 10}
}

// sameState compares two phone states by value.
func sameState(a, b protocol.PhoneState) bool {
	if a.Type != b.Type || a.Node != b.Node || a.Zone != b.Zone || a.X != b.X || a.Y != b.Y || a.W != b.W || a.H != b.H {
		return false
	}
	if a.Name != b.Name || a.Color != b.Color || a.Sim != b.Sim {
		return false
	}
	if (a.Bearing == nil) != (b.Bearing == nil) || (a.Bearing != nil && *a.Bearing != *b.Bearing) {
		return false
	}
	if (a.Move == nil) != (b.Move == nil) {
		return false
	}
	return a.Move == nil || *a.Move == *b.Move
}
