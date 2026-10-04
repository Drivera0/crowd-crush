package crowdsim

import (
	"math"
	"testing"

	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// onWall reports whether (x, y) lies on any wall.
func onWall(g *Geometry, x, y float64) bool {
	for _, s := range g.Walls {
		cx, cy := closest(s, x, y)
		if math.Hypot(cx-x, cy-y) < 0.01 {
			return true
		}
	}
	return false
}

func testLayout() *protocol.VenueLayout {
	return &protocol.VenueLayout{
		Stage: []protocol.Point{{8, 0}, {16, 0}, {16, 3}, {8, 3}},
		Exits: []protocol.LayoutExit{
			{ID: "main", Name: "Main doors", X0: 11, Y0: 15.9, X1: 13, Y1: 15.9}, // snaps onto the bottom edge
			{ID: "bar", Name: "Bar door", X0: 20, Y0: 8, X1: 20, Y1: 9},          // a door in an inner wall
		},
		// The outline drawn again over the main doors, and the bar's wall.
		Walls: [][4]float64{{0, 16, 24, 16}, {20, 4, 20, 12}},
	}
}

func TestLayoutGeometry(t *testing.T) {
	if g := LayoutGeometry(24, 16, nil); g.custom || len(g.Exits) != 4 {
		t.Fatal("no layout should give the default geometry")
	}
	g := LayoutGeometry(24, 16, testLayout())
	if len(g.Exits) != 2 || g.Exits[0].Y0 != 16 || g.Exits[0].ny != 1 {
		t.Fatalf("exits %+v", g.Exits[0])
	}
	if onWall(g, 12, 16) {
		t.Error("the main doors are walled over (outline or the layout's own wall)")
	}
	if !onWall(g, 5, 16) || !onWall(g, 20, 6) || !onWall(g, 0, 8) {
		t.Error("walls missing around the gaps")
	}
	if onWall(g, 20, 8.5) {
		t.Error("the bar door is walled over")
	}
	if b := g.Exits[1]; math.Abs(b.nx) != 1 || b.ny != 0 {
		t.Errorf("inner door normal %v, %v", b.nx, b.ny)
	}
	if !g.inStage(12, 1) || g.inStage(12, 5) || g.BarrierY != 3 {
		t.Errorf("stage: barrier %v", g.BarrierY)
	}
	exits, _ := GeometryJSON(g)
	if exits[0].ID != "main" || exits[0].Name != "Main doors" {
		t.Errorf("exits JSON %+v", exits)
	}
}

// People in a layout venue stay out of the stage and walls, and leave
// through its exits.
func TestLayoutWorld(t *testing.T) {
	w, err := New(Config{W: 24, H: 16, People: 80, Participation: 0.6, Seed: 3, StartMs: 1_000_000, Layout: testLayout()})
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Agents()) != 80 {
		t.Fatalf("placed %d of 80", len(w.Agents()))
	}
	for _, a := range w.Agents() {
		if w.G.inStage(a.X, a.Y) {
			t.Fatalf("agent placed on the stage at %.1f, %.1f", a.X, a.Y)
		}
	}
	if err := w.Apply(Action{Type: ActDisperse}); err != nil {
		t.Fatal(err)
	}
	run(w, 40)
	if n := len(w.Agents()); n > 40 {
		t.Errorf("%d of 80 people still inside after 40 s of dispersing", n)
	}
}
