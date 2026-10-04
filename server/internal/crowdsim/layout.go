package crowdsim

import (
	"fmt"
	"math"

	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// snapM: an exit within this distance of the venue's edge is on that edge.
const snapM = 0.3

// HasLayout reports whether a venue layout gives the simulation anything
// to use (walls or exits); without one the default geometry applies.
func HasLayout(l *protocol.VenueLayout) bool {
	return l != nil && (len(l.Walls) > 0 || len(l.Exits) > 0)
}

// LayoutGeometry builds the simulated venue from a staff-drawn (or
// Gemini-read) layout: the venue's outline plus the layout's walls, with a
// gap cut wherever an exit lies along a wall, and the stage outline as a
// wall (people press toward its front, the edge with the largest y). It
// falls back to NewGeometry when the layout has no walls or exits.
func LayoutGeometry(w, h float64, l *protocol.VenueLayout) *Geometry {
	if !HasLayout(l) {
		return NewGeometry(w, h)
	}
	g := &Geometry{W: w, H: h, custom: true}
	clampPt := func(x, y float64) (float64, float64) {
		return math.Max(0, math.Min(w, x)), math.Max(0, math.Min(h, y))
	}
	walls := []Seg{{0, 0, w, 0}, {w, 0, w, h}, {w, h, 0, h}, {0, h, 0, 0}}
	for _, s := range l.Walls {
		x0, y0 := clampPt(s[0], s[1])
		x1, y1 := clampPt(s[2], s[3])
		walls = append(walls, Seg{x0, y0, x1, y1})
	}
	if len(l.Stage) >= 3 {
		minX, maxX, maxY := math.Inf(1), math.Inf(-1), math.Inf(-1)
		for i, p := range l.Stage {
			x, y := clampPt(p[0], p[1])
			g.Stage = append(g.Stage, [2]float64{x, y})
			q := l.Stage[(i+1)%len(l.Stage)]
			qx, qy := clampPt(q[0], q[1])
			walls = append(walls, Seg{x, y, qx, qy})
			minX, maxX, maxY = math.Min(minX, x), math.Max(maxX, x), math.Max(maxY, y)
		}
		g.BarrierY, g.BarrierX0, g.BarrierX1 = maxY, minX, maxX
	} else {
		// No stage: "stage" and "surge" press toward the top wall.
		g.BarrierY, g.BarrierX0, g.BarrierX1 = 0, 0, w
	}
	cx, cy := w/2, h/2
	for i, le := range l.Exits {
		x0, y0 := clampPt(le.X0, le.Y0)
		x1, y1 := clampPt(le.X1, le.Y1)
		if math.Hypot(x1-x0, y1-y0) < 0.05 {
			continue
		}
		e := &Exit{ID: le.ID, Name: le.Name, X0: x0, Y0: y0, X1: x1, Y1: y1, Open: true}
		if e.ID == "" {
			e.ID = fmt.Sprintf("exit-%d", i+1)
		}
		switch {
		case y0 <= snapM && y1 <= snapM:
			e.Y0, e.Y1, e.ny = 0, 0, -1
		case y0 >= h-snapM && y1 >= h-snapM:
			e.Y0, e.Y1, e.ny = h, h, 1
		case x0 <= snapM && x1 <= snapM:
			e.X0, e.X1, e.nx = 0, 0, -1
		case x0 >= w-snapM && x1 >= w-snapM:
			e.X0, e.X1, e.nx = w, w, 1
		default:
			// An inner door: its normal points away from the venue's centre.
			l := math.Hypot(x1-x0, y1-y0)
			nx, ny := -(y1-y0)/l, (x1-x0)/l
			mx, my := e.mid()
			if nx*(mx-cx)+ny*(my-cy) < 0 {
				nx, ny = -nx, -ny
			}
			e.nx, e.ny = nx, ny
		}
		g.Exits = append(g.Exits, e)
		walls = cutGap(walls, e)
	}
	g.Walls = walls
	return g
}

// cutGap removes the part of every wall that an exit lies along.
func cutGap(walls []Seg, e *Exit) []Seg {
	var out []Seg
	for _, s := range walls {
		dx, dy := s.X1-s.X0, s.Y1-s.Y0
		l2 := dx*dx + dy*dy
		if l2 < 1e-9 {
			continue
		}
		l := math.Sqrt(l2)
		// Both exit ends must lie on the wall's line.
		off := func(x, y float64) float64 { return math.Abs((x-s.X0)*dy-(y-s.Y0)*dx) / l }
		if off(e.X0, e.Y0) > snapM || off(e.X1, e.Y1) > snapM {
			out = append(out, s)
			continue
		}
		t0 := ((e.X0-s.X0)*dx + (e.Y0-s.Y0)*dy) / l2
		t1 := ((e.X1-s.X0)*dx + (e.Y1-s.Y0)*dy) / l2
		if t0 > t1 {
			t0, t1 = t1, t0
		}
		if t1 <= 0 || t0 >= 1 {
			out = append(out, s)
			continue
		}
		const minLen = 0.05
		if t0 > 0 && t0*l >= minLen {
			out = append(out, Seg{s.X0, s.Y0, s.X0 + t0*dx, s.Y0 + t0*dy})
		}
		if t1 < 1 && (1-t1)*l >= minLen {
			out = append(out, Seg{s.X0 + t1*dx, s.Y0 + t1*dy, s.X1, s.Y1})
		}
	}
	return out
}

// inPoly is the even-odd point-in-polygon test.
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
