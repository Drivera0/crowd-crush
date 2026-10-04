package crowdsim

import (
	"math"

	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// Venues with furniture: a classroom, an auditorium and a stadium gate,
// next to the concert (world.go, behaviour.go). Each is a Scenario: its
// own room, furniture, seats, doors and exits, the people in it, and the
// director actions that make sense there. The people's behaviour in these
// places is in person.go; routes are found over a navigation grid (nav.go).
//
// # Classroom (about 30–60 people)
//
// A 9.2 × 16 m lecture room with a corridor 2.4 m wide along its right
// side (the venue is 11.6 × 16 m). Three columns of three-seat desks in
// seven rows (63 seats), a whiteboard and the teacher's desk at the front
// (the top of the map), two 0.9 m doors into the corridor (front and
// back), and the corridor's two ends as the ways out. Row pitch is 1.8 m:
// a desk 0.5 m deep, the seated person behind it and a passage behind the
// chairs that a disc body can walk along (real rooms are tighter, ~1.5 m,
// but a real person turns sideways; bodies here can't). Students sit facing
// the front; now and then one leaves for the toilets (in the corridor) and
// comes back; latecomers arrive from the corridor and take a free seat.
// Actions: class ends (dismiss), fire alarm (alarm), next class arrives
// (arrive), class resumes (calm).
//
// # Auditorium (about 150–500 people)
//
// A 30 × 25 m hall: stage along the top, a cross-aisle, 12 rows of three
// blocks of 14 fixed seats (504 seats; seat pitch 0.58 m, row pitch
// 1.3 m), aisles on both sides and between the blocks, a rear cross-aisle,
// a wall with four doors into a lobby 4.8 m deep (bar and toilets), two
// main exits from the lobby and two emergency exits beside the stage. A
// seat row is a channel between seatbacks: nobody can pass a seated
// neighbour; whoever sees someone coming along the row stands up against
// the seatback in front to let them squeeze past (letPass, person.go), as
// in any theatre, so a row empties from the aisle inward unless the people
// inside are quicker. The aisles beside the blocks are tiered: walking
// there is slower, as on stairs.
// Actions: intermission, show resumes (calm), show ends (dismiss),
// evacuate (alarm), open/close any door or exit.
//
// # Stadium gate (up to 1000 people)
//
// A 30 × 22 m concourse in front of a turnstile bank in the top wall: four
// turnstiles 0.6 m wide and a 2 m relief gate (closed to start with),
// inside a funnel of crowd fences. Fans arrive along the bottom edge at a
// rate the director sets and queue for the turnstiles, each of which lets
// one person through at a time at 660 an hour (the Green Guide figure for
// a turnstile; the number is from memory). Late arrivals who can't see the
// front push (surge), which is how a queue turns into a crush: the
// pattern of Hillsborough (1989), Ellis Park (2001) and Kanjuruhan (2022).
// Opening the relief gate relieves it. Actions: steady arrivals (calm),
// kick-off rush (rush), push from the back (surge), open/close gates.
//
// # Numbers used, and where they are from
//
// Checked against published sources while this was written: the SFPE
// Handbook's maximum specific flow through doors and corridors,
// 1.32 persons/(m·s) at 1.9 /m² (the hydraulic model); Weidmann's free
// walking speed, 1.34 m/s (calib.go); the Green Guide's (SGSA, Guide to
// Safety at Sports Grounds) 660 persons per turnstile per hour; Fruin's
// stair speeds, 0.5–0.9 m/s free-flow, i.e. about half to two thirds of
// level walking. From memory and so approximate: the pre-movement time
// distributions (Purser & Bensilum 2001 and Gwynne, Kuligowski et al. put
// the time before people start to move at tens of seconds to minutes,
// lognormally spread, shorter when staff tell them to go; the medians
// here are compressed to roughly a third of that so a run is watchable,
// and say so at their constants), walking speed along a seat row, the
// share of people who leave the way they came in (Sime 1985, "movement
// toward the familiar": most), and how long someone in a queue puts up
// with not moving before pushing.

// Scenario is a start scenario: a place, its people and its actions.
type Scenario struct {
	ID, Name, Desc string
	// People is the default crowd; MaxPeople the most the place holds (the
	// seats, or the venue's cap). Participation is the default share with
	// the app (students nearly all have their phone out; a stadium crowd less so).
	People, MaxPeople int
	Participation     float64
	// W, H is the venue the scenario builds (0 = the live venue, concert).
	W, H    float64
	Actions []protocol.SimActionSpec
	build   func(w *World, people int)
}

// scenarios is the registry; the first is the default.
var scenarios = []*Scenario{
	{ID: "concert", Name: "Concert", Desc: "A standing crowd in front of a stage: surges, dancing, trips to the bar.",
		People: 250, MaxPeople: MaxPeople, Participation: 0.6,
		Actions: []protocol.SimActionSpec{
			{Type: ActCalm, Label: "Calm", Tip: "People stand and watch the show", Kind: "behaviour"},
			{Type: ActDance, Label: "Dance", Tip: "Everyone dances to the beat: must NOT raise a push alarm", Kind: "behaviour"},
			{Type: ActIntermission, Label: "Intermission", Tip: "Many head for the bar and toilets at once", Kind: "behaviour"},
			{Type: ActStage, Label: "To the stage", Tip: "Everyone presses toward the stage", Kind: "behaviour"},
			{Type: ActSurge, Label: "Surge", Tip: "A surge toward the stage: the classic crush", Kind: "strength"},
			{Type: ActAttract, Label: "Gather here…", Tip: "Click the map: a group forms there", Kind: "point"},
			{Type: ActShove, Label: "Shove…", Tip: "Drag on the map: a push in that direction", Kind: "drag"},
			{Type: ActSpawn, Label: "Add people…", Tip: "Click the map: 20 more people arrive", Kind: "point"},
			{Type: ActDisperse, Label: "Evacuate", Tip: "Everyone heads for the open exits", Kind: "behaviour"},
		}},
	{ID: "classroom", Name: "Classroom", Desc: "A lecture room of 63 desks with a corridor outside: class ends, a fire alarm, the next class arriving.",
		People: 45, MaxPeople: 63, Participation: 0.9, W: 11.6, H: 16,
		Actions: []protocol.SimActionSpec{
			{Type: ActCalm, Label: "Class in session", Tip: "Everyone sits (or goes back to their seat); someone slips out now and then", Kind: "behaviour"},
			{Type: ActDismiss, Label: "Class ends", Tip: "People pack up at their own pace and file out through the doors", Kind: "behaviour"},
			{Type: ActAlarm, Label: "Fire alarm", Tip: "Everyone leaves at once: the doors become bottlenecks. Strength = how urgent people feel", Kind: "strength"},
			{Type: ActArrive, Label: "Next class arrives", Tip: "A new class walks up the corridor and comes in while the last one leaves", Kind: "behaviour"},
			{Type: ActShove, Label: "Shove…", Tip: "Drag on the map: a push in that direction", Kind: "drag"},
		}, build: buildClassroom},
	{ID: "auditorium", Name: "Auditorium", Desc: "A 504-seat lecture theatre with a lobby: intermission, the show ending, an evacuation with an exit blocked.",
		People: 300, MaxPeople: 504, Participation: 0.6, W: 30, H: 25,
		Actions: []protocol.SimActionSpec{
			{Type: ActCalm, Label: "Show on", Tip: "The audience sits; whoever is out in the lobby comes back to their seat", Kind: "behaviour"},
			{Type: ActIntermission, Label: "Intermission", Tip: "More than half the audience heads for the bar and toilets in the lobby, then comes back", Kind: "behaviour"},
			{Type: ActDismiss, Label: "Show ends", Tip: "Everyone leaves through the lobby, row by row, at their own pace", Kind: "behaviour"},
			{Type: ActAlarm, Label: "Evacuate", Tip: "Fire alarm: every exit counts, including the emergency exits by the stage. Strength = urgency", Kind: "strength"},
			{Type: ActShove, Label: "Shove…", Tip: "Drag on the map: a push in that direction", Kind: "drag"},
		}, build: buildAuditorium},
	{ID: "gate", Name: "Stadium gate", Desc: "Fans arriving at a bank of turnstiles inside crowd fences: the queue that becomes a crush when the back pushes.",
		People: 150, MaxPeople: MaxPeople, Participation: 0.5, W: 30, H: 22,
		Actions: []protocol.SimActionSpec{
			{Type: ActCalm, Label: "Steady arrivals", Tip: "Fans arrive about as fast as the turnstiles let them through", Kind: "behaviour"},
			{Type: ActRush, Label: "Kick-off rush", Tip: "Fans arrive three times faster than the turnstiles can take them; the queue grows", Kind: "behaviour"},
			{Type: ActSurge, Label: "Push from the back", Tip: "The people at the back, who can't see the front, push forward. Strength = how hard", Kind: "strength"},
			{Type: ActShove, Label: "Shove…", Tip: "Drag on the map: a push in that direction", Kind: "drag"},
			{Type: ActSpawn, Label: "Add people…", Tip: "Click the map: 20 more people arrive", Kind: "point"},
		}, build: buildGate},
}

// Venue actions (the concert's are in world.go).
const (
	ActDismiss = "dismiss" // class ends / show ends: everyone leaves normally
	ActAlarm   = "alarm"   // fire alarm: everyone leaves now; strength = urgency
	ActArrive  = "arrive"  // classroom: the next class comes in
	ActRush    = "rush"    // gate: arrivals faster than the turnstiles
)

// ScenarioInfos describes the scenarios for GET /api/sim.
func ScenarioInfos() []protocol.SimScenario {
	out := make([]protocol.SimScenario, len(scenarios))
	for i, s := range scenarios {
		out[i] = protocol.SimScenario{ID: s.ID, Name: s.Name, Desc: s.Desc, People: s.People, MaxPeople: s.MaxPeople,
			Participation: s.Participation, W: s.W, H: s.H, Actions: s.Actions}
	}
	return out
}

// FindScenario looks a scenario up by id ("" = concert).
func FindScenario(id string) *Scenario {
	if id == "" {
		id = "concert"
	}
	for _, s := range scenarios {
		if s.ID == id {
			return s
		}
	}
	return nil
}

// ---- furniture, seats, rows ----

// seat is a place to sit; a person sitting in it is held there.
type seat struct {
	x, y float64
	row  *row
	idx  int
	occ  *Agent
}

// row is a line of seats that empties into an aisle at each end: people
// leave along walkY (the seatway, or the passage behind the chairs) to
// endL or endR, the aisle centre lines.
type row struct {
	y, walkY   float64
	x0, x1     float64 // seat span
	endL, endR float64
	seats      []*seat
	stairs     bool
	// fixed: theatre seats, where the seatway runs through the row itself
	// and a seated person has to stand to let anyone past (letPass). False
	// for desks with a passage behind the chairs.
	fixed bool
}

// vpoi is somewhere people go and stand about (the bar, the toilets).
type vpoi struct {
	name   string
	x, y   float64
	nx, ny float64 // into the room
	weight float64
	n      int // people with a spot here
}

type rect struct{ x0, y0, x1, y1 float64 }

func (r rect) has(x, y float64) bool { return x >= r.x0 && x <= r.x1 && y >= r.y0 && y <= r.y1 }

// venueState is what a furnished venue adds to the world.
type venueState struct {
	nav       *nav
	seats     []*seat
	rows      []*row
	pois      []vpoi
	stairs    []rect // tiered aisles: slower walking
	furniture []protocol.SimFurniture
	mode      string  // the behaviour in force (calm, dismiss, alarm, intermission, rush, surge)
	urgency   float64 // 0 (nobody minds waiting) … 1 (everyone pushes when held up)
	front     float64 // facing when seated (rad; −π/2 = toward the top)
	// Gate: arrivals per second along the bottom edge.
	arrival        float64
	arrX0, arrX1   float64
	arrY           float64
	arrivalCarry   float64
	gates          []*Exit // turnstiles (Rate > 0)
	outsideRegion  int     // classroom: the corridor; auditorium: the lobby
	roomRegion     int     // where the seats are
	nextLeaver     float64 // classroom/auditorium: next time someone slips out during the show
	nextLatecomer  float64
	latecomersLeft int
	arrivals       []*Agent // classroom: the next class, waiting in the corridor
}

// addRect adds a solid rectangle of furniture (its four sides are walls)
// and lists it for the dashboard.
func (w *World) addRect(kind, label string, x0, y0, x1, y1 float64, walls *[]Seg) {
	*walls = append(*walls, Seg{x0, y0, x1, y0}, Seg{x1, y0, x1, y1}, Seg{x1, y1, x0, y1}, Seg{x0, y1, x0, y0})
	w.vs.furniture = append(w.vs.furniture, protocol.SimFurniture{Kind: kind, Label: label, X0: round2(x0), Y0: round2(y0), X1: round2(x1), Y1: round2(y1)})
}

// addRow adds n seats from x0 at pitch along y, leaving along walkY to the
// aisles at endL and endR, and lists the row for the dashboard.
func (w *World) addRow(kind string, x0, pitch float64, n int, y, walkY, endL, endR float64, fixed bool) *row {
	r := &row{y: y, walkY: walkY, x0: x0, x1: x0 + float64(n-1)*pitch, endL: endL, endR: endR, fixed: fixed, stairs: fixed}
	for i := 0; i < n; i++ {
		s := &seat{x: x0 + float64(i)*pitch, y: y, row: r, idx: i}
		r.seats = append(r.seats, s)
		w.vs.seats = append(w.vs.seats, s)
	}
	w.vs.rows = append(w.vs.rows, r)
	w.vs.furniture = append(w.vs.furniture, protocol.SimFurniture{Kind: kind, X0: round2(x0 - pitch/2), Y0: round2(y - 0.25), X1: round2(r.x1 + pitch/2), Y1: round2(y + 0.25), N: n})
	return r
}

// outerWalls are the venue's four sides.
func outerWalls(wd, h float64) []Seg {
	return []Seg{{0, 0, wd, 0}, {wd, 0, wd, h}, {wd, h, 0, h}, {0, h, 0, 0}}
}

// addExit adds a door or exit along the given segment with normal (nx, ny)
// and cuts its gap out of the walls.
func addExit(g *Geometry, walls []Seg, id, name string, x0, y0, x1, y1, nx, ny float64, inner, emergency, open bool) ([]Seg, *Exit) {
	e := &Exit{ID: id, Name: name, X0: x0, Y0: y0, X1: x1, Y1: y1, Open: open, nx: nx, ny: ny, Inner: inner, Emergency: emergency}
	g.Exits = append(g.Exits, e)
	return cutGap(walls, e), e
}

// ---- the classroom ----

func buildClassroom(w *World, people int) {
	const (
		roomW   = 9.2
		corrW   = 2.4
		H       = 16.0
		deskW   = 1.8
		deskD   = 0.5
		pitch   = 1.8
		rows    = 7
		firstY  = 2.5
		seatOff = 0.8  // seat centre behind the desk's front edge
		walkOff = 1.42 // the passage behind the chairs
	)
	g := &Geometry{W: roomW + corrW, H: H, custom: true}
	walls := outerWalls(g.W, H)
	walls = append(walls, Seg{roomW, 0, roomW, H}) // the corridor wall
	var e *Exit
	walls, e = addExit(g, walls, "door-front", "Front door", roomW, 1.0, roomW, 1.9, 1, 0, true, false, true)
	_ = e
	walls, _ = addExit(g, walls, "door-back", "Back door", roomW, 13.6, roomW, 14.5, 1, 0, true, false, true)
	walls, _ = addExit(g, walls, "corr-top", "Corridor, front end", roomW+0.2, 0, g.W-0.2, 0, 0, -1, false, false, true)
	walls, _ = addExit(g, walls, "corr-bottom", "Corridor, back end", roomW+0.2, H, g.W-0.2, H, 0, 1, false, false, true)
	// The whiteboard is on the wall (drawn, not an obstacle); the teacher's desk
	// leaves a metre in front of it and a metre to the first row, like the aisles.
	w.vs.furniture = append(w.vs.furniture, protocol.SimFurniture{Kind: "board", Label: "Whiteboard", X0: 1.6, Y0: 0, X1: 7.6, Y1: 0.08})
	w.addRect("desk", "Teacher's desk", 3.7, 1.0, 5.5, 1.5, &walls)
	blocks := []float64{0.9, 3.7, 6.5} // desk x0 per column (aisles 1.0 m, margins 0.9 m)
	aisles := []float64{0.45, 3.2, 6.0, 8.75}
	for i := 0; i < rows; i++ {
		dy := firstY + float64(i)*pitch
		for b, bx := range blocks {
			w.addRect("desk", "", bx, dy, bx+deskW, dy+deskD, &walls)
			w.addRow("chairs", bx+0.3, 0.6, 3, dy+seatOff, dy+walkOff, aisles[b], aisles[b+1], false)
		}
	}
	g.Walls = walls
	g.BarrierY, g.BarrierX0, g.BarrierX1 = -1, 0, g.W // no stage: nothing is ever "on the stage"
	w.G = g
	w.solid = g.solid()
	w.vs.front = -math.Pi / 2
	w.vs.pois = []vpoi{{name: "toilets", x: g.W - 0.5, y: H - 1.2, nx: -1, weight: 1}}
	w.buildNav()
	w.vs.roomRegion = w.vs.nav.regionAt(4.6, 8)
	w.vs.outsideRegion = w.vs.nav.regionAt(roomW+corrW/2, 8)
	// The teacher, standing in front of the desk facing the class.
	t := w.newAgent(4.6, 2.0)
	t.R = 0.23
	w.initPers(t)
	t.p.teacher = true
	t.p.familiar = g.Exit("door-front")
	t.face, t.prevFace, t.hd = math.Pi/2, math.Pi/2, math.Pi/2
	w.agents = append(w.agents, t)
	w.seatPeople(people, func(s *seat) float64 { return 1 }) // any seat
	// Most came in by the front door (the teacher's end); the rest by the back door.
	for _, a := range w.agents {
		if a.p.teacher {
			continue
		}
		a.p.familiar = g.Exit("door-front")
		if w.rng.Float64() < 0.4 {
			a.p.familiar = g.Exit("door-back")
		}
	}
	w.vs.nextLeaver = w.T + leaveEvery*w.rng.ExpFloat64()
	w.vs.nextLatecomer = w.T + 20 + 40*w.rng.Float64()
	w.vs.latecomersLeft = min(3, len(w.vs.seats)-people)
}

// seatPeople puts n people in seats chosen with the given weight (0 = never).
func (w *World) seatPeople(n int, weight func(*seat) float64) {
	free := make([]*seat, 0, len(w.vs.seats))
	ws := make([]float64, 0, len(w.vs.seats))
	for _, s := range w.vs.seats {
		if s.occ == nil {
			if wt := weight(s); wt > 0 {
				free = append(free, s)
				ws = append(ws, wt)
			}
		}
	}
	for k := 0; k < n && len(free) > 0; k++ {
		tot := 0.0
		for _, v := range ws {
			tot += v
		}
		r := w.rng.Float64() * tot
		i := len(free) - 1
		for j, v := range ws {
			if r < v {
				i = j
				break
			}
			r -= v
		}
		s := free[i]
		free[i], ws[i] = free[len(free)-1], ws[len(ws)-1]
		free, ws = free[:len(free)-1], ws[:len(ws)-1]
		a := w.newAgent(s.x, s.y)
		w.initPers(a)
		w.sitDown(a, s)
		a.p.phase = phIdle
		a.face, a.prevFace, a.hd = w.vs.front, w.vs.front, w.vs.front
		w.agents = append(w.agents, a)
	}
	// Friends: pairs sitting next to each other who wait for one another.
	for _, r := range w.vs.rows {
		for i := 0; i+1 < len(r.seats); i++ {
			a, b := r.seats[i].occ, r.seats[i+1].occ
			if a != nil && b != nil && a.p.friend == nil && b.p.friend == nil && w.rng.Float64() < friendShare {
				a.p.friend, b.p.friend = b, a
			}
		}
	}
}

// ---- the auditorium ----

func buildAuditorium(w *World, people int) {
	const (
		W, H     = 30.0, 25.0
		stageY   = 1.5
		firstY   = 4.0 // first row's seat centres
		rows     = 12
		rowPitch = 1.3 // seatbacks 1.3 m apart: room for a disc body to pass someone standing at their seat
		seatW    = 0.58
		perBlock = 14
		lobbyY   = 20.2
	)
	g := &Geometry{W: W, H: H, custom: true}
	walls := outerWalls(W, H)
	// Stage.
	g.Stage = [][2]float64{{6, 0}, {24, 0}, {24, stageY}, {6, stageY}}
	walls = append(walls, Seg{6, 0, 6, stageY}, Seg{6, stageY, 24, stageY}, Seg{24, stageY, 24, 0})
	w.vs.furniture = append(w.vs.furniture, protocol.SimFurniture{Kind: "stage", Label: "Stage", X0: 6, Y0: 0, X1: 24, Y1: stageY})
	g.BarrierY, g.BarrierX0, g.BarrierX1 = stageY, 6, 24
	// Lobby wall with four doors.
	walls = append(walls, Seg{0, lobbyY, W, lobbyY})
	blockW := float64(perBlock) * seatW
	bx := []float64{1.2, 1.2 + blockW + 1.5, 1.2 + 2*(blockW+1.5)} // block x0 (first seat centre − seatW/2)
	aisles := []float64{0.6, bx[1] - 0.75, bx[2] - 0.75, (bx[2] + blockW + W) / 2}
	walls, _ = addExit(g, walls, "door-l", "Lobby door, left", 0.3, lobbyY, 1.5, lobbyY, 0, 1, true, false, true)
	walls, _ = addExit(g, walls, "door-cl", "Lobby door, centre left", aisles[1]-0.9, lobbyY, aisles[1]+0.9, lobbyY, 0, 1, true, false, true)
	walls, _ = addExit(g, walls, "door-cr", "Lobby door, centre right", aisles[2]-0.9, lobbyY, aisles[2]+0.9, lobbyY, 0, 1, true, false, true)
	walls, _ = addExit(g, walls, "door-r", "Lobby door, right", W-1.5, lobbyY, W-0.3, lobbyY, 0, 1, true, false, true)
	walls, _ = addExit(g, walls, "exit-main-l", "Main exit, left", 6, H, 8.4, H, 0, 1, false, false, true)
	walls, _ = addExit(g, walls, "exit-main-r", "Main exit, right", W-8.4, H, W-6, H, 0, 1, false, false, true)
	walls, _ = addExit(g, walls, "exit-em-l", "Emergency exit, left", 0, 2.0, 0, 3.2, -1, 0, false, true, true)
	walls, _ = addExit(g, walls, "exit-em-r", "Emergency exit, right", W, 2.0, W, 3.2, 1, 0, false, true, true)
	// Seat rows between seatbacks; the aisles beside them are stepped.
	for b, x0 := range bx {
		for i := 0; i < rows; i++ {
			y := firstY + float64(i)*rowPitch
			walls = append(walls, Seg{x0, y - rowPitch/2, x0 + blockW, y - rowPitch/2})
			if i == rows-1 {
				walls = append(walls, Seg{x0, y + rowPitch/2, x0 + blockW, y + rowPitch/2})
			}
			// The seatway runs just behind the seated people's centres;
			// whoever is passed stands against the seatback in front (letPass).
			w.addRow("seats", x0+seatW/2, seatW, perBlock, y, y+seatwayOff, aisles[b], aisles[b+1], true)
		}
		w.vs.furniture = append(w.vs.furniture, protocol.SimFurniture{Kind: "stairs", X0: round2(aisles[b] - 0.5), Y0: firstY - rowPitch/2, X1: round2(aisles[b] + 0.5), Y1: round2(firstY + (float64(rows)-0.5)*rowPitch)})
		w.vs.stairs = append(w.vs.stairs, rect{aisles[b] - 0.75, firstY - rowPitch/2, aisles[b] + 0.75, firstY + (float64(rows)-0.5)*rowPitch})
	}
	w.vs.furniture = append(w.vs.furniture, protocol.SimFurniture{Kind: "stairs", X0: round2(aisles[3] - 0.5), Y0: firstY - rowPitch/2, X1: round2(aisles[3] + 0.5), Y1: round2(firstY + (float64(rows)-0.5)*rowPitch)})
	w.vs.stairs = append(w.vs.stairs, rect{aisles[3] - 0.75, firstY - rowPitch/2, aisles[3] + 0.75, firstY + (float64(rows)-0.5)*rowPitch})
	// Lobby: bar along the back wall, toilets at the sides.
	w.addRect("counter", "Bar", 11, H-0.6, 19, H, &walls)
	w.vs.furniture = append(w.vs.furniture, protocol.SimFurniture{Kind: "label", Label: "Toilets", X0: 0, Y0: 21.5, X1: 0.2, Y1: 23.5},
		protocol.SimFurniture{Kind: "label", Label: "Toilets", X0: W - 0.2, Y0: 21.5, X1: W, Y1: 23.5})
	g.Walls = walls
	w.G = g
	w.solid = g.solid()
	w.vs.front = -math.Pi / 2
	w.vs.pois = []vpoi{
		{name: "bar", x: 15, y: H - 0.7, nx: 0, ny: -1, weight: 0.5},
		{name: "toilets", x: 0.5, y: 22.5, nx: 1, ny: 0, weight: 0.25},
		{name: "toilets", x: W - 0.5, y: 22.5, nx: -1, ny: 0, weight: 0.25},
	}
	w.buildNav()
	w.vs.roomRegion = w.vs.nav.regionAt(W/2, 2.5)
	w.vs.outsideRegion = w.vs.nav.regionAt(W/2, 22.5)
	// Fuller toward the front and the middle, as a half-full house sits.
	w.seatPeople(people, func(s *seat) float64 {
		return 0.3 + 1.0*(1-math.Abs(s.x-W/2)/(W/2)) + 0.7*(1-(s.y-firstY)/(rowPitch*rows))
	})
	for _, a := range w.agents {
		// Everyone came in through the lobby, by the main exit on their side.
		a.p.familiar = g.Exit("exit-main-l")
		if a.X > W/2 {
			a.p.familiar = g.Exit("exit-main-r")
		}
	}
	w.vs.nextLeaver = w.T + leaveEvery*w.rng.ExpFloat64()/3 // a bigger audience: more comings and goings
}

// seatwayOff is how far behind the seat centres the seatway of a fixed
// row runs: with someone standing letPassShift in front of their seat and
// the seatback rowPitch/2 behind it, a body passes along here with the
// same clearance on both sides.
const seatwayOff = 0.28

// ---- the stadium gate ----

// stileLane is the length (m) of the railings in front of each turnstile.
const stileLane = 1.6

func buildGate(w *World, people int) {
	const (
		W, H     = 30.0, 22.0
		stileW   = 0.9 // a real turnstile is ~0.55 m; a disc body of 0.26 m needs 0.9 to get through the social force of the jambs
		stileX0  = 12.5
		nStiles  = 4
		throatX0 = 10.0
		throatX1 = 20.0
	)
	g := &Geometry{W: W, H: H, custom: true}
	walls := outerWalls(W, H)
	for i := 0; i < nStiles; i++ {
		x := stileX0 + float64(i)*(stileW+0.5)
		var e *Exit
		walls, e = addExit(g, walls, "stile-"+string(rune('a'+i)), "Turnstile "+string(rune('A'+i)), x, 0, x+stileW, 0, 0, -1, false, false, true)
		e.Rate = 660.0 / 3600 // Green Guide: 660 persons per turnstile per hour (from memory)
		w.vs.gates = append(w.vs.gates, e)
		// Railings either side make a single-file lane in front of each turnstile.
		for _, lx := range []float64{x, x + stileW} {
			walls = append(walls, Seg{lx, 0, lx, stileLane})
			w.vs.furniture = append(w.vs.furniture, protocol.SimFurniture{Kind: "fence", X0: round2(lx), Y0: 0, X1: round2(lx), Y1: stileLane})
		}
		w.vs.furniture = append(w.vs.furniture, protocol.SimFurniture{Kind: "turnstile", Label: e.Name, X0: round2(x), Y0: 0, X1: round2(x + stileW), Y1: 0.6})
	}
	walls, _ = addExit(g, walls, "gate-relief", "Relief gate", 18.2, 0, 19.9, 0, 0, -1, false, false, false)
	w.vs.furniture = append(w.vs.furniture, protocol.SimFurniture{Kind: "gate", Label: "Relief gate", X0: 18.2, Y0: 0, X1: 19.9, Y1: 0.3})
	// Crowd fences: a funnel from the approach to the throat.
	fence := []Seg{{6, H, 6, 12}, {6, 12, throatX0, 0}, {W - 6, H, W - 6, 12}, {W - 6, 12, throatX1, 0}}
	walls = append(walls, fence...)
	for _, s := range fence {
		w.vs.furniture = append(w.vs.furniture, protocol.SimFurniture{Kind: "fence", X0: round2(s.X0), Y0: round2(s.Y0), X1: round2(s.X1), Y1: round2(s.Y1)})
	}
	g.Walls = walls
	g.BarrierY, g.BarrierX0, g.BarrierX1 = -1, throatX0, throatX1
	w.G = g
	w.solid = g.solid()
	w.vs.front = -math.Pi / 2
	w.vs.arrX0, w.vs.arrX1, w.vs.arrY = 7, W-7, H-0.6
	w.vs.arrival = gateCalmRate
	w.vs.mode = ActCalm
	w.buildNav()
	// The first fans are already queueing: placed in the funnel, denser toward the front.
	for tries := 0; len(w.agents) < people && tries < people*300; tries++ {
		u := w.rng.Float64()
		y := 1.0 + 14*math.Pow(u, 1.4)
		// The funnel narrows toward the throat.
		x0, x1 := 6.0, W-6
		if y < 12 {
			k := (12 - y) / 12
			x0, x1 = 6+(throatX0-6)*k, W-6-(W-6-throatX1)*k
		}
		x := x0 + 0.6 + (x1-x0-1.2)*w.rng.Float64()
		r := 0.20 + 0.06*w.rng.Float64()
		if !w.free(x, y, r) {
			continue
		}
		a := w.newAgent(x, y)
		a.R = r
		w.initPers(a)
		a.p.goal = gEnter
		a.p.phase = phIdle
		a.p.ready, a.p.delay = 1, 0 // already on their way
		w.agents = append(w.agents, a)
	}
}
