package crowd

import (
	"fmt"
	"math"
	"math/rand"
	"testing"

	"github.com/Drivera0/crowd-crush/server/internal/detect"
)

// blob is n phones scattered (Gaussian, sd) around (cx, cy).
func blob(n int, cx, cy, sd float64, seed int64) []Point {
	rng := rand.New(rand.NewSource(seed))
	pts := make([]Point, n)
	for i := range pts {
		pts[i] = Point{fmt.Sprint("p", i), cx + sd*rng.NormFloat64(), cy + sd*rng.NormFloat64()}
	}
	return pts
}

func angle(ax, ay, bx, by float64) float64 {
	c := (ax*bx + ay*by) / (math.Hypot(ax, ay) * math.Hypot(bx, by))
	return math.Acos(math.Max(-1, math.Min(1, c))) * 180 / math.Pi
}

var open = Geom{W: 40, H: 40}

// TestGuideGradient: in a blob, a phone off-centre is sent straight away
// from the centre (down the density gradient).
func TestGuideGradient(t *testing.T) {
	pts := blob(80, 20, 20, 1.5, 1)
	for _, p := range []GuideIn{{ID: "x", X: 21.5, Y: 20}, {ID: "x", X: 20, Y: 18.5}, {ID: "x", X: 19, Y: 21}} {
		p.Reason = "density"
		dx, dy, _, _, exit := Direction(pts, 1, open, p)
		if a := angle(dx, dy, p.X-20, p.Y-20); a > 25 || exit != "" {
			t.Errorf("at %.1f,%.1f: direction (%.2f, %.2f) is %.0f° from straight out (exit %q)", p.X, p.Y, dx, dy, a, exit)
		}
		if l := math.Hypot(dx, dy); math.Abs(l-1) > 1e-9 {
			t.Errorf("not a unit vector: %.3f", l)
		}
	}
	// KDE: the gradient points up the density.
	gx, gy, rho := KDEGrad(pts, 22, 20)
	if gx >= 0 || rho <= 0 || math.Abs(gy) > math.Abs(gx) {
		t.Errorf("gradient at the blob's right edge (%.2f, %.2f), density %.2f", gx, gy, rho)
	}
}

// TestGuideWall: a phone pressed against a wall with the crowd beside it
// is not sent into the wall.
func TestGuideWall(t *testing.T) {
	g := Geom{W: 40, H: 40, Walls: [][4]float64{{10, 15, 30, 15}}}
	pts := blob(80, 20, 17.5, 1.2, 2) // crowd just below the wall at y = 15
	dx, dy, _, _, _ := Direction(pts, 1, g, GuideIn{ID: "x", X: 20, Y: 15.4, Reason: "density"})
	if dy < 0 && segCross(20, 15.4, 20+2*dx, 15.4+2*dy, 10, 15, 30, 15) {
		t.Errorf("sent through the wall: (%.2f, %.2f)", dx, dy)
	}
}

// TestGuideExit: an exit roughly ahead is blended in and named; one behind
// or too far is not.
func TestGuideExit(t *testing.T) {
	pts := blob(80, 20, 20, 1.5, 3)
	p := GuideIn{ID: "x", X: 21.5, Y: 20, Reason: "density"} // sent toward +x
	ahead := Geom{W: 40, H: 40, Exits: []Exit{{ID: "e", Name: "East door", X0: 40, Y0: 24, X1: 40, Y1: 28}}}
	dx, dy, ex, ey, exit := Direction(pts, 1, ahead, p)
	if exit != "East door" {
		t.Fatalf("exit %q, want East door", exit)
	}
	if dy <= 0 || angle(dx, dy, 1, 0) > 30 || angle(dx, dy, ex, ey) > angle(1, 0, ex, ey) {
		t.Errorf("not blended toward the exit: (%.2f, %.2f), exit (%.2f, %.2f)", dx, dy, ex, ey)
	}
	behind := Geom{W: 40, H: 40, Exits: []Exit{{ID: "w", Name: "West door", X0: 0, Y0: 18, X1: 0, Y1: 22}}}
	if _, _, _, _, exit := Direction(pts, 1, behind, p); exit != "" {
		t.Errorf("exit behind used: %q", exit)
	}
	far := Geom{W: 80, H: 40, Exits: []Exit{{ID: "f", Name: "Far door", X0: 80, Y0: 18, X1: 80, Y1: 22}}}
	if _, _, _, _, exit := Direction(pts, 1, far, p); exit != "" {
		t.Errorf("exit 58 m away used: %q", exit)
	}
	// Through the guide: To is the exit's name, else "less crowded side".
	g := NewGuide()
	if mv := g.Update(0, pts, 1, ahead, []GuideIn{p})["x"]; mv.To != "East door" || mv.Reason != "density" {
		t.Errorf("move %+v", mv)
	}
	if mv := g.Update(250, pts, 1, behind, []GuideIn{p})["x"]; mv.To != lessCrowded {
		t.Errorf("move %+v", mv)
	}
}

// TestGuidePush: a push sends people sideways (on the emptier side),
// slightly with the push, never against it.
func TestGuidePush(t *testing.T) {
	// Crowd along x = 20; more people on the left (x < 20) than the right.
	var pts []Point
	rng := rand.New(rand.NewSource(4))
	for i := 0; i < 60; i++ {
		pts = append(pts, Point{fmt.Sprint("l", i), 17 + 3*rng.Float64(), 10 + 20*rng.Float64()})
	}
	for i := 0; i < 15; i++ {
		pts = append(pts, Point{fmt.Sprint("r", i), 20 + 3*rng.Float64(), 10 + 20*rng.Float64()})
	}
	p := GuideIn{ID: "x", X: 20, Y: 20, Reason: "push", PushX: 0, PushY: 2} // push travelling down the map
	dx, dy, _, _, _ := Direction(pts, 1, open, p)
	if dx <= 0 {
		t.Errorf("sent to the crowded side: (%.2f, %.2f)", dx, dy)
	}
	if dy < 0 {
		t.Errorf("sent against the push: (%.2f, %.2f)", dx, dy)
	}
	if a := angle(dx, dy, 1, 0); a > 30 {
		t.Errorf("%.0f° from sideways", a)
	}
	mv := NewGuide().Update(0, pts, 1, open, []GuideIn{p})["x"]
	if mv.Reason != "push" {
		t.Errorf("reason %q", mv.Reason)
	}
}

// TestGuideSmoothing: small changes leave the arrow alone; a lasting turn
// moves it after the EMA catches up, not at once.
func TestGuideSmoothing(t *testing.T) {
	g := NewGuide()
	at := func(x, y float64) []Point { return blob(80, x, y, 1.5, 5) }
	p := GuideIn{ID: "x", X: 21.5, Y: 20, Reason: "density"}
	first := g.Update(0, at(20, 20), 1, open, []GuideIn{p})["x"]
	// Crowd centre wobbles: raw direction turns ~20°, the arrow stays.
	for i := 1; i <= 8; i++ {
		cy := 20 + 0.5*float64(i%2*2-1)
		mv := g.Update(int64(i)*250, at(20, cy), 1, open, []GuideIn{p})["x"]
		if mv.DX != first.DX || mv.DY != first.DY {
			t.Fatalf("arrow jittered at step %d: (%.2f, %.2f) → (%.2f, %.2f)", i, first.DX, first.DY, mv.DX, mv.DY)
		}
	}
	// The crowd moves to the right of the phone: the raw direction flips to
	// −x. One tick later the arrow hasn't flipped; a few seconds later it has.
	now := int64(2000)
	mv := g.Update(now+250, at(23, 20), 1, open, []GuideIn{p})["x"]
	if mv.DX < 0 {
		t.Errorf("arrow flipped within one tick: (%.2f, %.2f)", mv.DX, mv.DY)
	}
	for i := 2; i <= 24; i++ {
		mv = g.Update(now+int64(i)*250, at(23, 20), 1, open, []GuideIn{p})["x"]
	}
	rx, ry, _, _, _ := Direction(at(23, 20), 1, open, p)
	if rx >= 0 || angle(mv.DX, mv.DY, rx, ry) > GuideHoldDeg+1 {
		t.Errorf("after 6 s the arrow is (%.2f, %.2f), raw direction (%.2f, %.2f)", mv.DX, mv.DY, rx, ry)
	}
	// Safe again: state cleared, no move.
	if out := g.Update(now+7000, at(23, 20), 1, open, nil); len(out) != 0 || len(g.st) != 0 {
		t.Errorf("state kept for a safe phone: %v", out)
	}
}

// TestGuideLeaveArea: inside an area a rule turned red, the way out of the
// area wins even against the density gradient.
func TestGuideLeaveArea(t *testing.T) {
	area := [][2]float64{{10, 10}, {14, 10}, {14, 14}, {10, 14}}
	// Crowd outside, just right of the area: density says go left (−x),
	// the nearest way out of the area is right (+x, 0.5 m away).
	pts := blob(60, 16, 12, 1, 6)
	p := GuideIn{ID: "x", X: 13.5, Y: 12, Reason: "density", Leave: area}
	dx, dy, _, _, _ := Direction(pts, 1, open, p)
	if dx <= 0.1 {
		t.Errorf("not led out of the area: (%.2f, %.2f)", dx, dy)
	}
	if !detect.InPolygon(area, p.X, p.Y) {
		t.Fatal("test phone outside its area")
	}
}
