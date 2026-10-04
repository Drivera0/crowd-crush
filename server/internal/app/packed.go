package app

import (
	"math"

	"github.com/Drivera0/crowd-crush/server/internal/crowd"
	"github.com/Drivera0/crowd-crush/server/internal/crowdsim"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// How packed in each phone is (crowd/packed.go): computed after every step
// from the phones the clusters use, shown on the dashboard as the dot's
// fill (snapshot node dens, press, crush) and counted into the phone's own
// state and guidance. It is a display of the density estimate, per person;
// alerts still come from clusters, zones and area rules only.

// packedLocked updates p.packed. Caller holds mu.
func (a *App) packedLocked(p *pipeline, now int64, sim bool) {
	cfg := p.cfg()
	// A replay of a recording made in another venue knows nothing of today's stage.
	layout := a.replay == nil || a.replay.p != p || a.replay.ownVenue
	p.packed = p.pack.Update(now, p.pts, crowd.ConfigFrom(cfg), a.standable(cfg.VenueW, cfg.VenueH, sim, layout))
}

// standable reports where people can stand: inside the venue and off the
// stage (the venue layout's outline; for the simulation, the stage of the
// sim's geometry, which has a default stage pit when the layout has none).
// Caller holds mu.
func (a *App) standable(w, h float64, sim, layout bool) func(x, y float64) bool {
	var stage [][2]float64
	if l := a.venue.Layout; sim {
		stage = crowdsim.LayoutGeometry(w, h, l).StageOutline()
		if s := a.sim; s != nil {
			stage = s.geo.stage
		}
	} else if l != nil && layout {
		for _, pt := range l.Stage {
			stage = append(stage, [2]float64{pt[0], pt[1]})
		}
	}
	return func(x, y float64) bool {
		if x < 0 || y < 0 || x > w || y > h {
			return false
		}
		return len(stage) < 3 || !inPolygon(stage, x, y)
	}
}

// inPolygon is the even-odd point-in-polygon test.
func inPolygon(poly [][2]float64, x, y float64) bool {
	in := false
	for i, j := 0, len(poly)-1; i < len(poly); j, i = i, i+1 {
		xi, yi, xj, yj := poly[i][0], poly[i][1], poly[j][0], poly[j][1]
		if (yi > y) != (yj > y) && x < (xj-xi)*(y-yi)/(yj-yi)+xi {
			in = !in
		}
	}
	return in
}

// packNode fills a snapshot node's dens, press and crush.
func packNode(p *pipeline, id string, n *protocol.Node) {
	pk, ok := p.packed[id]
	if !ok {
		return
	}
	n.Dens = math.Round(pk.Dens*10) / 10
	if pk.Level != protocol.LevelCalm {
		n.Press = pk.Level
	}
	n.Crush = round2(pk.Crush)
}
