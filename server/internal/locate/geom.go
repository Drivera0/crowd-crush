package locate

import "math"

// What the floor allows.
//
// A person is inside the venue rectangle, not on the stage, and not in a
// wall. An estimate that isn't is moved to the nearest point that is
// (inside): a GPS fix 6 m off the edge of the map is someone standing
// near that edge, not someone outside; dropping them loses a third of a
// crowd at 5 m error. A dead-reckoned step that would cross a wall or
// enter the stage slides along it instead, or is dropped if it can't
// (slide).

const (
	wallGap = 0.25 // m, a body's half-width: how close an estimate may sit to a wall or edge
)

type geom struct {
	w, h  float64
	walls [][4]float64
	stage [][2]float64
	on    bool
}

func newGeom(cfg Config) geom {
	g := geom{w: cfg.VenueW, h: cfg.VenueH, on: cfg.MapConstraints}
	for _, s := range cfg.Walls {
		if math.Hypot(s[2]-s[0], s[3]-s[1]) > 1e-6 {
			g.walls = append(g.walls, s)
		}
	}
	if len(cfg.Stage) >= 3 {
		g.stage = cfg.Stage
	}
	return g
}

func inPoly(poly [][2]float64, x, y float64) bool {
	in := false
	for i, j := 0, len(poly)-1; i < len(poly); j, i = i, i+1 {
		xi, yi, xj, yj := poly[i][0], poly[i][1], poly[j][0], poly[j][1]
		if (yi > y) != (yj > y) && x < (xj-xi)*(y-yi)/(yj-yi)+xi {
			in = !in
		}
	}
	return in
}

func closestOnSeg(x0, y0, x1, y1, x, y float64) (float64, float64) {
	dx, dy := x1-x0, y1-y0
	l2 := dx*dx + dy*dy
	t := 0.0
	if l2 > 0 {
		t = math.Max(0, math.Min(1, ((x-x0)*dx+(y-y0)*dy)/l2))
	}
	return x0 + t*dx, y0 + t*dy
}

// inside is the nearest walkable point to (x, y): within the venue, off
// the stage, and not within wallGap of a wall. With the constraints off it
// only keeps the point within the venue rectangle.
func (g *geom) inside(x, y float64) (float64, float64) {
	if g.w <= 0 || g.h <= 0 {
		return x, y
	}
	if !g.on {
		return math.Max(0, math.Min(x, g.w)), math.Max(0, math.Min(y, g.h))
	}
	m := math.Min(wallGap, math.Min(g.w, g.h)/4)
	x = math.Max(m, math.Min(x, g.w-m))
	y = math.Max(m, math.Min(y, g.h-m))
	if len(g.stage) >= 3 && inPoly(g.stage, x, y) {
		// Out through the nearest edge of the stage outline.
		best := math.Inf(1)
		bx, by := x, y
		for i, j := 0, len(g.stage)-1; i < len(g.stage); j, i = i, i+1 {
			cx, cy := closestOnSeg(g.stage[j][0], g.stage[j][1], g.stage[i][0], g.stage[i][1], x, y)
			// An edge that lies on the venue's own outline leads nowhere.
			if cx < m/2 || cy < m/2 || cx > g.w-m/2 || cy > g.h-m/2 {
				continue
			}
			if d := math.Hypot(cx-x, cy-y); d < best {
				best = d
				nx, ny := cx-x, cy-y
				if d > 1e-9 {
					nx, ny = nx/d, ny/d
				}
				bx, by = cx+nx*m, cy+ny*m
			}
		}
		x = math.Max(m, math.Min(bx, g.w-m))
		y = math.Max(m, math.Min(by, g.h-m))
	}
	// Off the walls.
	for _, s := range g.walls {
		cx, cy := closestOnSeg(s[0], s[1], s[2], s[3], x, y)
		d := math.Hypot(x-cx, y-cy)
		if d >= m || d < 1e-9 {
			continue
		}
		nx, ny := (x-cx)/d*m+cx, (y-cy)/d*m+cy
		if len(g.stage) >= 3 && inPoly(g.stage, nx, ny) {
			continue
		}
		x = math.Max(m, math.Min(nx, g.w-m))
		y = math.Max(m, math.Min(ny, g.h-m))
	}
	return x, y
}

func segCross(x1, y1, x2, y2, x3, y3, x4, y4 float64) bool {
	d := (x2-x1)*(y4-y3) - (y2-y1)*(x4-x3)
	if math.Abs(d) < 1e-12 {
		return false
	}
	t := ((x3-x1)*(y4-y3) - (y3-y1)*(x4-x3)) / d
	u := ((x3-x1)*(y2-y1) - (y3-y1)*(x2-x1)) / d
	return t >= 0 && t <= 1 && u >= 0 && u <= 1
}

// blocked reports whether the straight step from (x, y) to (x+dx, y+dy)
// crosses a wall or a stage edge, and that segment.
func (g *geom) blocked(x, y, dx, dy float64) (seg [4]float64, hit bool) {
	for _, s := range g.walls {
		if segCross(x, y, x+dx, y+dy, s[0], s[1], s[2], s[3]) {
			return s, true
		}
	}
	for i, j := 0, len(g.stage)-1; i < len(g.stage) && len(g.stage) >= 3; j, i = i, i+1 {
		s := [4]float64{g.stage[j][0], g.stage[j][1], g.stage[i][0], g.stage[i][1]}
		if segCross(x, y, x+dx, y+dy, s[0], s[1], s[2], s[3]) {
			return s, true
		}
	}
	return seg, false
}

// slide is the part of the step (dx, dy) from (x, y) that the floor
// allows: the whole step, else its component along the wall it would
// cross, else nothing.
func (g *geom) slide(x, y, dx, dy float64) (float64, float64) {
	if !g.on {
		return dx, dy
	}
	s, hit := g.blocked(x, y, dx, dy)
	if !hit {
		return dx, dy
	}
	wx, wy := s[2]-s[0], s[3]-s[1]
	l := math.Hypot(wx, wy)
	wx, wy = wx/l, wy/l
	k := dx*wx + dy*wy
	dx, dy = k*wx, k*wy
	if _, hit := g.blocked(x, y, dx, dy); hit {
		return 0, 0
	}
	return dx, dy
}
