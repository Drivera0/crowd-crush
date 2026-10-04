package crowdsim

import "math"

// Seg is a wall segment in venue metres.
type Seg struct{ X0, Y0, X1, Y1 float64 }

// Exit is a gap in the venue's outer wall. Closed, it is a wall like any
// other; open, people can walk through it and leave.
type Exit struct {
	ID, Name       string
	X0, Y0, X1, Y1 float64
	Open           bool
	nx, ny         float64 // outward normal
	// Furnished venues (venue.go). Inner: a door between two rooms, not a
	// way out of the venue. Emergency: used only when the alarm sounds.
	// Rate > 0: a turnstile that lets one person through every 1/Rate s.
	Inner, Emergency bool
	Rate             float64
	token            *Agent // turnstile: the person going through
	tokenAt, nextAt  float64
	regIn, regOut    int // rooms on the −normal and +normal sides (nav.go)
}

// mid is the middle of the gap.
func (e *Exit) mid() (x, y float64) { return (e.X0 + e.X1) / 2, (e.Y0 + e.Y1) / 2 }

// Geometry of a venue: outer walls with four exit gaps, and the stage pit
// (barrier plus two short side walls) along the top.
//
//	┌──────┬──────────────┬──────┐ y = 0
//	│      │    stage     │      │
//	│      └──────────────┘      │ y = StageDepth (barrier)
//	╎ exit-l                exit-r╎
//	│                            │
//	└─╎exit-bl╎────────────╎exit-br╎─┘ y = H
type Geometry struct {
	W, H                 float64
	Walls                []Seg // static walls (exit gaps left open)
	Exits                []*Exit
	BarrierY             float64 // y of the stage barrier's front
	BarrierX0, BarrierX1 float64
	// Stage is the stage outline from a venue layout (LayoutGeometry); empty
	// = the default stage pit above the barrier.
	Stage  [][2]float64
	custom bool // built from a venue layout
}

// Geometry constants (m).
const (
	StageDepth   = 1.5  // barrier distance from the top wall
	StageFrac    = 0.6  // barrier spans the middle 60 % of the width
	ExitWidth    = 2.0  // every exit gap
	cornerInset  = 1.0  // corner exits start this far from the corner
	sideExitFrac = 0.45 // side exits are centred at this fraction of the height
)

// NewGeometry lays out a w × h venue.
func NewGeometry(w, h float64) *Geometry {
	g := &Geometry{W: w, H: h, BarrierY: StageDepth}
	g.BarrierX0 = w * (1 - StageFrac) / 2
	g.BarrierX1 = w - g.BarrierX0
	ew := math.Min(ExitWidth, math.Min(w, h)/4)
	inset := math.Min(cornerInset, w/8)
	sy := h * sideExitFrac
	g.Exits = []*Exit{
		{ID: "exit-bl", Name: "Bottom-left exit", X0: inset, Y0: h, X1: inset + ew, Y1: h, Open: true, ny: 1},
		{ID: "exit-br", Name: "Bottom-right exit", X0: w - inset - ew, Y0: h, X1: w - inset, Y1: h, Open: true, ny: 1},
		{ID: "exit-l", Name: "Left side exit", X0: 0, Y0: sy - ew/2, X1: 0, Y1: sy + ew/2, Open: true, nx: -1},
		{ID: "exit-r", Name: "Right side exit", X0: w, Y0: sy - ew/2, X1: w, Y1: sy + ew/2, Open: true, nx: 1},
	}
	bl, br, el, er := g.Exits[0], g.Exits[1], g.Exits[2], g.Exits[3]
	g.Walls = []Seg{
		{0, 0, w, 0},                       // top
		{0, 0, 0, el.Y0}, {0, el.Y1, 0, h}, // left, around exit-l
		{w, 0, w, er.Y0}, {w, er.Y1, w, h}, // right, around exit-r
		{0, h, bl.X0, h}, {bl.X1, h, br.X0, h}, {br.X1, h, w, h}, // bottom
		// Stage pit: barrier and its two side walls.
		{g.BarrierX0, g.BarrierY, g.BarrierX1, g.BarrierY},
		{g.BarrierX0, 0, g.BarrierX0, g.BarrierY},
		{g.BarrierX1, 0, g.BarrierX1, g.BarrierY},
	}
	return g
}

// Exit returns the exit with this id.
func (g *Geometry) Exit(id string) *Exit {
	for _, e := range g.Exits {
		if e.ID == id {
			return e
		}
	}
	return nil
}

// solid is every segment people collide with: the walls plus closed exits.
func (g *Geometry) solid() []Seg {
	s := append([]Seg(nil), g.Walls...)
	for _, e := range g.Exits {
		if !e.Open {
			s = append(s, Seg{e.X0, e.Y0, e.X1, e.Y1})
		}
	}
	return s
}

// inStage reports whether a point is inside the stage pit (no one may stand there).
func (g *Geometry) inStage(x, y float64) bool {
	if len(g.Stage) >= 3 {
		return inPoly(g.Stage, x, y)
	}
	return y < g.BarrierY && x > g.BarrierX0 && x < g.BarrierX1
}

// StageOutline is the area nobody may stand in: the layout's stage
// outline, or the default stage pit.
func (g *Geometry) StageOutline() [][2]float64 {
	if len(g.Stage) >= 3 {
		return g.Stage
	}
	if g.custom || g.BarrierY <= 0 {
		return nil
	}
	return [][2]float64{{g.BarrierX0, 0}, {g.BarrierX1, 0}, {g.BarrierX1, g.BarrierY}, {g.BarrierX0, g.BarrierY}}
}

// closest is the closest point of segment s to (x, y).
func closest(s Seg, x, y float64) (cx, cy float64) {
	dx, dy := s.X1-s.X0, s.Y1-s.Y0
	l2 := dx*dx + dy*dy
	t := 0.0
	if l2 > 0 {
		t = math.Max(0, math.Min(1, ((x-s.X0)*dx+(y-s.Y0)*dy)/l2))
	}
	return s.X0 + t*dx, s.Y0 + t*dy
}
