package crowdsim

import (
	"errors"
	"fmt"
	"math"
)

// How people behave in a furnished venue (venue.go). Each person has a
// goal (stay, leave, get to their seat, go to the bar, get in) and a phase
// (seated, rising, along the row, waiting to merge into the aisle,
// following a route, settling, sitting down, waiting at a door). Nothing
// switches all at once:
//
//   - pre-movement: when the class ends or an alarm sounds, each person
//     has their own delay (lognormal) before they get up, and the delay
//     runs faster the more of the people around them are already up
//     (herding). Friends sitting together wait for each other for a while;
//   - standing up takes a moment (riseSec), and shows in the phone as a
//     posture change (phone.go);
//   - a row empties into the aisle along the seatway; a seated person
//     cannot be passed, and in the auditorium stands up to let someone
//     through (letPass); at the row's end people give way to the aisle for
//     a few seconds before stepping in (mergePatience);
//   - the way out is chosen among the doors of the room the person is in:
//     distance (nav.go), the queue at each door (people near it ÷ the
//     SFPE flow of 1.32 persons per metre per second) and familiarity
//     (most people leave the way they came in: Sime 1985), reconsidered
//     every few seconds, so a long queue or a closed door makes people
//     re-route;
//   - in a queue, someone held up longer than their patience allows (and
//     only when the situation feels urgent: an alarm, a kick-off rush)
//     starts pushing: they stop keeping a time gap and press on at a
//     higher desired speed (Helbing 2000's impatience). Pressure then
//     comes from the contact physics alone.

type goal int

const (
	gNone  goal = iota // stay where you are
	gLeave             // get out of the venue
	gSeat              // get to your own seat
	gPOI               // go to the bar / toilets and stand there
	gEnter             // from outside (the corridor, the approach) into the room / through the turnstiles
)

type phase int

const (
	phIdle     phase = iota // seated, or standing at a spot
	phRising                // getting up
	phRow                   // along the row toward the aisle
	phMerge                 // at the row's end, waiting for a gap in the aisle
	phNav                   // following a route to a door, exit or point
	phSettle                // walking straight to a spot near the target
	phRowIn                 // along the row toward the seat
	phSitting               // sitting down
	phWaitDoor              // at a door, letting people out first
	phLetPass               // standing at the seat to let someone through
	phDodge                 // stepped into an empty seat to let someone pass in the row
)

// pers is a person's behaviour state in a furnished venue.
type pers struct {
	seat     *seat
	sitting  bool
	goal     goal
	phase    phase
	phaseT   float64
	ready    float64 // pre-movement: accumulates toward delay
	delay    float64
	readyAt  float64 // when ready first passed delay (friends wait from here)
	herd     float64 // share of the people around who are up (cached)
	friend   *Agent
	familiar *Exit
	// Route.
	target *Exit // door or exit aimed at (nil: a point)
	side   float64
	tx, ty float64
	field  []float32
	reeval float64
	region int
	ahead  bool // the friend went ahead: don't wait any longer
	// Queueing and impatience.
	heldT    float64
	patience float64
	push     bool
	freeT    float64
	// Trips.
	poi        int
	dwellUntil float64
	rowEnd     float64 // x of the aisle this person leaves the row by
	sub        int
	teacher    bool
	emergency  bool // may use the emergency exits (alarm)
	letT       float64
	spotted    bool    // has a spot at the POI (counted in its n)
	yieldUntil float64 // standing aside for people coming the other way
	// Head-on in a seat row: time blocked, and the empty seat stepped into.
	rowHeld float64
	dodgeX  float64
	dodgeY  float64
	resume  phase
}

// rowOncoming reports whether someone is walking along a's row toward a
// from the side a is heading to (dir = ±1 along x), within reach.
func (w *World) rowOncoming(a *Agent, dir float64) bool {
	r := w.rowOf(a)
	if r == nil {
		return false
	}
	for _, b := range w.agents {
		if b == a {
			continue
		}
		bdir, ok := w.alongRow(b, r)
		dx := b.X - a.X
		if ok && dx*dir > 0 && math.Abs(dx) < 1.6 && bdir*dir < 0 {
			return true
		}
	}
	return false
}

// rowOf is the row a person is in: their own while they are in it, else
// the row whose seatway they are standing in (pushed there), nil if none.
func (w *World) rowOf(a *Agent) *row {
	if s := a.p.seat; s != nil {
		switch a.p.phase {
		case phIdle, phSitting, phLetPass, phRising, phRow, phRowIn, phDodge, phMerge:
			return s.row
		}
	}
	return w.rowAt(a.X, a.Y)
}

// rowAt is the row whose channel (seatway and seats) contains the point.
func (w *World) rowAt(x, y float64) *row {
	for _, r := range w.vs.rows {
		if x >= r.x0-0.3 && x <= r.x1+0.3 && y >= math.Min(r.y, r.walkY)-0.45 && y <= math.Max(r.y, r.walkY)+0.45 {
			return r
		}
	}
	return nil
}

// alongRow reports whether b is walking along row r and which way (±1 in x):
// to the aisle (phRow), to its seat (phRowIn), or someone pushed into the
// row's channel on their way elsewhere, by their heading.
func (w *World) alongRow(b *Agent, r *row) (dir float64, ok bool) {
	p := b.p
	if p == nil {
		return 0, false
	}
	switch p.phase {
	case phRow:
		if p.seat.row != r {
			return 0, false
		}
		return math.Copysign(1, p.rowEnd-b.X), true
	case phRowIn:
		if p.seat.row != r {
			return 0, false
		}
		return math.Copysign(1, p.seat.x-b.X), true
	case phNav, phSettle:
		if w.rowAt(b.X, b.Y) != r || math.Abs(math.Cos(b.hd)) < 0.5 {
			return 0, false
		}
		return math.Copysign(1, math.Cos(b.hd)), true
	}
	return 0, false
}

// dodge: blocked head-on in a fixed row, step into the nearest empty seat
// and let the other through. ok = false if there is none within reach.
func (w *World) dodge(a *Agent, back phase) bool {
	p := a.p
	r := w.rowOf(a)
	if r == nil {
		return false
	}
	var best *seat
	bd := 0.75
	for _, s := range r.seats {
		if s.occ != nil && s.occ != a {
			continue
		}
		if d := math.Abs(s.x - a.X); d < bd {
			best, bd = s, d
		}
	}
	if best == nil {
		return false
	}
	p.dodgeX, p.dodgeY = best.x, best.y
	if r.fixed {
		p.dodgeY -= letPassShift
	}
	p.resume, p.phase, p.phaseT, p.rowHeld = back, phDodge, w.T, 0
	return true
}

// hurry ranks goals: who gives way to whom when two people meet head on
// (the one with less reason to hurry stands aside).
func hurry(g goal) int {
	switch g {
	case gLeave:
		return 3
	case gPOI:
		return 2
	case gEnter:
		return 1
	}
	return 0
}

// oncoming reports whether someone within 1.5 m ahead of a along (ex, ey)
// is walking the other way with more reason to hurry (or the same, in
// which case the lower id gives way).
func (w *World) oncoming(a *Agent, ex, ey float64) bool {
	found := false
	w.grid.near(w.agents, a.idx, 1.5, func(b *Agent) {
		if found || b.p == nil || b.p.phase != phNav {
			return
		}
		dx, dy := b.X-a.X, b.Y-a.Y
		if dx*ex+dy*ey <= 0 || math.Abs(dx*ey-dy*ex) > 0.6 {
			return
		}
		if b.VX*ex+b.VY*ey < -0.1 || b.ex*ex+b.ey*ey < -0.3 {
			ha, hb := hurry(a.p.goal), hurry(b.p.goal)
			// Equals: the lower id gives way, but never among people all
			// trying to get out or in (a queue is a queue, not a courtesy).
			if hb > ha || hb == ha && ha < hurry(gEnter) && a.ID < b.ID {
				found = true
			}
		}
	})
	return found
}

// Wire states of a simulated person (Bodies).
const (
	StStanding = 0
	StWalking  = 1
	StSeated   = 2
	StQueueing = 3
	StPushing  = 4
)

// Behaviour parameters. "From memory" marks numbers not checked against a
// source while this was written.
const (
	// Pre-movement medians (s) and lognormal sigmas. From memory: Purser &
	// Bensilum 2001 and later drill studies (Gwynne, Kuligowski) report
	// pre-movement times of 30 s to a few minutes for an alarm with nobody
	// instructing, less with staff telling people to go; the end of a class
	// is quicker (people were waiting for it). Compressed to about a third
	// so a run is watchable; the spread is the literature's.
	preDismissMed, preDismissSig = 8.0, 0.6
	preAlarmMed, preAlarmSig     = 10.0, 0.5
	preInterMed, preInterSig     = 12.0, 0.8
	herdGain                     = 3.0 // readiness runs (1 + herdGain × share of neighbours up) times faster
	herdR                        = 1.5 // m: the neighbours that count
	riseSec                      = 1.5 // s to stand up (from memory)
	sitSec                       = 1.2 // s to sit down
	friendShare                  = 0.35
	friendWait                   = 8.0      // s a friend waits for the other to get up
	rowSpeedK                    = 0.6      // × own speed along a row (seatway; from memory ~0.8 m/s)
	stairK                       = 0.55     // × on the tiered aisles (Fruin: stairs 0.5–0.9 m/s vs 1.3 on the level)
	mergeMin, mergeMax           = 2.0, 5.0 // s of giving way at the row's end before stepping in anyway
	doorWaitMax                  = 15.0     // s people coming in let the outflow pass before going against it
	leaveEvery                   = 90.0     // s, mean time between someone slipping out during the show
	tripMin, tripMax             = 40.0, 90.0
	interShareV                  = 0.6 // share of the audience that goes out at the intermission
	interDwellMin, interDwellMax = 60.0, 150.0
	reevalEvery                  = 3.0  // s between exit re-evaluations
	rerouteGain                  = 0.25 // switch doors when another is this much better
	queueR                       = 3.0  // m: the queue in front of a door
	specFlow                     = 1.32 // persons/(m·s) through a door (SFPE Handbook)
	// Patience before pushing (s) at urgency 1, lognormal; scaled by
	// 1/urgency, infinite at urgency 0. From memory.
	patienceMed, patienceSig = 12.0, 0.5
	pushGain                 = 0.6 // desired speed × (1 + pushGain × urgency) when pushing
	letPassR                 = 2.2 // m: a seated person stands when someone comes along the row this close
	letPassShift             = 0.35
	gateCalmRate             = 0.6 // arrivals/s (four turnstiles take 0.73/s)
	gateRushRate             = 2.0
)

// lognormal draws exp(N(ln med, sig)).
func (w *World) lognormal(med, sig float64) float64 {
	return med * math.Exp(sig*w.rng.NormFloat64())
}

// initPers gives a person the venue behaviour state (standing where they are).
func (w *World) initPers(a *Agent) {
	a.p = &pers{phase: phIdle, poi: -1, reeval: w.rng.Float64() * reevalEvery, region: -2,
		patience: w.lognormal(patienceMed, patienceSig)}
	a.transT = -1
}

// sitDown seats a person.
func (w *World) sitDown(a *Agent, s *seat) {
	s.occ = a
	a.p.seat = s
	a.p.sitting = true
	a.seated = true
	a.p.phase = phSitting
	a.p.phaseT = w.T
	a.p.goal = gNone
	a.p.push = false
}

// seatAnchor is where the seat holds this person: the seat, or standing
// against the seatback in front to let someone pass.
func (a *Agent) seatAnchor() (x, y float64) {
	s := a.p.seat
	if a.p.phase == phLetPass {
		return s.x, s.y - letPassShift
	}
	return s.x, s.y
}

// venueApply runs a director action in a furnished venue.
func (w *World) venueApply(act Action) error {
	has := false
	for _, s := range w.scn.Actions {
		if s.Type == act.Type {
			has = true
		}
	}
	if !has {
		if act.Type == "" {
			return errors.New("missing action type")
		}
		names := make([]string, len(w.scn.Actions))
		for i, s := range w.scn.Actions {
			names[i] = s.Type
		}
		return fmt.Errorf("unknown action %q in the %s scenario (want one of %v)", act.Type, w.scn.ID, names)
	}
	str := func(def float64) (float64, error) {
		if act.Strength == nil {
			return def, nil
		}
		if !finite(act.Strength) || *act.Strength < 0 || *act.Strength > 1 {
			return 0, errors.New("strength must be 0..1")
		}
		return *act.Strength, nil
	}
	switch act.Type {
	case ActCalm:
		// Back to normal: whoever has a seat and is not on a trip goes back
		// to it; whoever is out stays out of it.
		w.vs.mode, w.vs.urgency = ActCalm, 0
		w.Action = ActCalm
		if w.scn.ID == "gate" {
			w.vs.arrival = gateCalmRate
			for _, a := range w.agents {
				a.p.push = false
			}
			break
		}
		for _, a := range w.agents {
			p := a.p
			p.emergency, p.push = false, false
			if p.seat != nil && !(p.sitting && p.phase == phIdle) && p.goal != gPOI && p.goal != gSeat {
				w.setGoal(a, gSeat)
				p.delay = w.lognormal(5, 0.6) // the bell: people drift back over a minute or so
			}
			if p.seat == nil && p.goal == gLeave {
				p.goal = gNone // standing people (spawned) stay
			}
		}
	case ActDismiss, ActAlarm:
		urg := 0.0
		if act.Type == ActAlarm {
			var err error
			if urg, err = str(0.6); err != nil {
				return err
			}
		}
		w.vs.mode, w.vs.urgency, w.Action = act.Type, urg, act.Type
		for _, a := range w.agents {
			p := a.p
			p.emergency = act.Type == ActAlarm
			if p.teacher {
				// The teacher sees the class out: leaves last (dismiss) or
				// goes straight away to hold the door (alarm).
				w.setGoal(a, gLeave)
				p.delay = 45
				if act.Type == ActAlarm {
					p.delay = 3
				}
				continue
			}
			if act.Type == ActDismiss {
				w.setGoal(a, gLeave)
				p.delay = w.lognormal(preDismissMed, preDismissSig)
			} else {
				w.setGoal(a, gLeave)
				p.delay = w.lognormal(preAlarmMed, preAlarmSig)
			}
			if p.goal == gLeave && p.phase != phIdle {
				p.ready = p.delay // already up: carry on
			}
		}
	case ActIntermission:
		w.vs.mode, w.vs.urgency, w.Action = ActIntermission, 0, ActIntermission
		for _, a := range w.agents {
			p := a.p
			if p.seat != nil && p.sitting && p.phase == phIdle && w.rng.Float64() < interShareV {
				w.setGoal(a, gPOI)
				p.poi = w.pickVPOI()
				p.delay = w.lognormal(preInterMed, preInterSig)
				p.dwellUntil = w.T + p.delay + interDwellMin + (interDwellMax-interDwellMin)*w.rng.Float64()
			}
		}
	case ActArrive:
		if w.vs.mode == ActCalm {
			// The class is still in: it ends now, that is what makes the two-way flow.
			if err := w.venueApply(Action{Type: ActDismiss}); err != nil {
				return err
			}
		}
		n := 0
		for _, s := range w.vs.seats {
			if s.occ == nil {
				n++
			}
		}
		n = min(max(10, len(w.vs.seats)*2/3), len(w.vs.seats))
		if n == 0 {
			return errors.New("no seats")
		}
		w.classArrives(n)
	case ActRush:
		w.vs.mode, w.vs.urgency, w.Action = ActRush, 0.3, ActRush
		w.vs.arrival = gateRushRate
	case ActSurge:
		s, err := str(0.7)
		if err != nil {
			return err
		}
		w.vs.mode, w.vs.urgency, w.Action = ActSurge, 0.4+0.6*s, ActSurge
		for _, a := range w.agents {
			// The back of the queue can't see why it isn't moving and loses
			// patience fast; whoever is held up now starts pushing soon.
			a.p.patience *= 0.3
		}
	}
	return nil
}

// setGoal gives a person a new goal; their behaviour starts from the
// pre-movement wait if they are seated or standing still.
func (w *World) setGoal(a *Agent, g goal) {
	p := a.p
	if p.goal == g {
		return
	}
	p.goal = g
	p.ready, p.readyAt, p.ahead = 0, 0, false
	p.delay = 0
	p.target, p.field = nil, nil
	p.push, p.heldT = false, 0
	if p.poi >= 0 && g != gPOI {
		if p.spotted {
			w.vs.pois[p.poi].n--
		}
		p.poi, p.spotted = -1, false
	}
	switch p.phase {
	case phIdle, phLetPass, phSitting:
		// wait for the delay, then rise / go
	case phSettle, phNav, phMerge, phRow, phRowIn, phRising, phWaitDoor:
		// Already on their feet: route again from where they are.
		if p.phase != phRising && p.phase != phRow {
			p.phase, p.phaseT = phNav, w.T
			w.route(a)
		}
	}
}

// pickVPOI chooses a point of interest by weight.
func (w *World) pickVPOI() int {
	tot := 0.0
	for _, p := range w.vs.pois {
		tot += p.weight
	}
	r := w.rng.Float64() * tot
	for i, p := range w.vs.pois {
		if r < p.weight {
			return i
		}
		r -= p.weight
	}
	return len(w.vs.pois) - 1
}

// venueTick is the per-tick bookkeeping of a furnished venue: who is up
// around whom (herding), arrivals, turnstiles and the show's comings and goings.
func (w *World) venueTick() {
	if w.ticks%10 == 0 {
		w.grid.build(w.agents)
		for i, a := range w.agents {
			if a.p.phase != phIdle {
				continue
			}
			n, up := 0, 0
			w.grid.near(w.agents, i, herdR, func(b *Agent) {
				n++
				if b.p.phase != phIdle && b.p.phase != phLetPass && b.p.phase != phSitting {
					up++
				}
			})
			a.p.herd = 0
			if n > 0 {
				a.p.herd = float64(up) / float64(n)
			}
		}
	}
	switch w.scn.ID {
	case "gate":
		w.gateTick()
	default:
		if w.vs.mode == ActCalm || w.vs.mode == "" {
			w.showTick()
		}
	}
	// Let-pass: a seated person stands when someone comes along their row.
	if w.ticks%5 == 0 {
		for _, a := range w.agents {
			p := a.p
			if p.seat == nil || !p.seat.row.fixed {
				continue // no seatway to clear (the classroom passage runs behind the chairs)
			}
			if !(p.phase == phIdle && p.sitting) && p.phase != phLetPass {
				continue
			}
			coming := false
			for _, b := range w.agents {
				if b == a {
					continue
				}
				bdir, ok := w.alongRow(b, p.seat.row)
				dx := b.X - a.X
				if ok && math.Abs(dx) < letPassR && math.Abs(dx) > 0.05 && bdir*dx < 0 {
					coming = true // b is heading my way along the row
					break
				}
			}
			switch {
			case coming && p.phase == phIdle:
				p.phase, p.phaseT, p.sitting = phLetPass, w.T, false
				a.transT, a.transDir = w.T, 1
			case coming && p.phase == phLetPass:
				p.letT = w.T
			case !coming && p.phase == phLetPass && w.T-p.letT > 1.5 && w.T-p.phaseT > riseSec:
				p.phase, p.phaseT, p.sitting = phSitting, w.T, true
				a.transT, a.transDir = w.T, -1
			}
		}
	}
}

// showTick: during the show (or the class) someone slips out to the
// toilets now and then and comes back, and in the classroom latecomers
// arrive and take a free seat.
func (w *World) showTick() {
	if w.T >= w.vs.nextLeaver {
		w.vs.nextLeaver = w.T + leaveEvery*w.rng.ExpFloat64()
		if w.scn.ID == "auditorium" {
			w.vs.nextLeaver = w.T + leaveEvery*w.rng.ExpFloat64()/3
		}
		if len(w.vs.pois) > 0 {
			// Someone seated with a clear way to an aisle.
			var cands []*Agent
			for _, a := range w.agents {
				p := a.p
				if p.seat != nil && p.sitting && p.phase == phIdle && p.goal == gNone && p.friend == nil && w.rowClear(p.seat) {
					cands = append(cands, a)
				}
			}
			if len(cands) > 0 {
				a := cands[w.rng.Intn(len(cands))]
				w.setGoal(a, gPOI)
				a.p.poi = w.pickVPOI()
				a.p.delay = 0.5
				a.p.dwellUntil = w.T + tripMin + (tripMax-tripMin)*w.rng.Float64()
			}
		}
	}
	if w.scn.ID == "classroom" && w.vs.latecomersLeft > 0 && w.T >= w.vs.nextLatecomer && len(w.agents) < MaxPeople {
		w.vs.nextLatecomer = w.T + 25 + 50*w.rng.Float64()
		w.vs.latecomersLeft--
		w.classArrives(1)
	}
}

// rowClear reports whether a seat has a free seatway to at least one aisle
// (no one sitting between it and that end).
func (w *World) rowClear(s *seat) bool {
	r := s.row
	if !r.fixed {
		return true // the passage runs behind the chairs
	}
	for _, dir := range []int{-1, 1} {
		ok := true
		for i := s.idx + dir; i >= 0 && i < len(r.seats); i += dir {
			if o := r.seats[i].occ; o != nil && o.p.sitting {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// classArrives brings n people into the corridor at its ends, heading for
// the doors and then a free seat.
func (w *World) classArrives(n int) {
	g := w.G
	for k := 0; k < n && len(w.agents) < MaxPeople; k++ {
		end := w.rng.Intn(2)
		placed := false
		for tries := 0; tries < 40 && !placed; tries++ {
			x := g.W - 2.4 + 0.4 + 1.6*w.rng.Float64()
			y := 0.4 + 3.0*w.rng.Float64()
			if end == 1 {
				y = g.H - y
			}
			r := 0.20 + 0.06*w.rng.Float64()
			if !w.free(x, y, r) {
				continue
			}
			a := w.newAgent(x, y)
			a.R = r
			w.initPers(a)
			a.p.goal, a.p.phase = gEnter, phIdle
			a.p.delay = 0.2 + 3*w.rng.Float64()
			a.hd = math.Pi / 2
			if end == 1 {
				a.hd = -math.Pi / 2
			}
			a.face, a.prevFace = a.hd, a.hd
			w.agents = append(w.agents, a)
			placed = true
		}
	}
}

// gateTick: fans arrive along the bottom edge and the turnstiles let one
// person through at a time.
func (w *World) gateTick() {
	w.vs.arrivalCarry += w.vs.arrival * Dt
	for w.vs.arrivalCarry >= 1 && len(w.agents) < MaxPeople {
		w.vs.arrivalCarry--
		for tries := 0; tries < 20; tries++ {
			x := w.vs.arrX0 + (w.vs.arrX1-w.vs.arrX0)*w.rng.Float64()
			r := 0.20 + 0.06*w.rng.Float64()
			if !w.free(x, w.vs.arrY, r) {
				continue
			}
			a := w.newAgent(x, w.vs.arrY)
			a.R = r
			w.initPers(a)
			a.p.goal, a.p.phase, a.p.ready, a.p.delay = gEnter, phIdle, 1, 0
			if w.vs.mode == ActSurge {
				a.p.patience *= 0.3
			}
			a.hd, a.face, a.prevFace = -math.Pi/2, -math.Pi/2, -math.Pi/2
			w.agents = append(w.agents, a)
			break
		}
	}
	for _, e := range w.vs.gates {
		if !e.Open {
			e.token = nil
			continue
		}
		if t := e.token; t != nil {
			mx, my := e.mid()
			through := (t.X-mx)*e.nx+(t.Y-my)*e.ny > 0.2 || t.out
			if through || w.T-e.tokenAt > 6 {
				e.token = nil
			} else {
				continue
			}
		}
		if w.T < e.nextAt {
			continue
		}
		// The next person: nearest to the gate, on the inside, within reach.
		mx, my := e.mid()
		var best *Agent
		bd := 0.9
		for _, a := range w.agents {
			if a.p.goal != gEnter || a.p.target != e {
				continue
			}
			d := math.Hypot(a.X-mx, a.Y-my)
			if d < bd && (a.X-mx)*e.nx+(a.Y-my)*e.ny < 0 {
				best, bd = a, d
			}
		}
		if best != nil {
			e.token, e.tokenAt, e.nextAt = best, w.T, w.T+1/e.Rate
		}
	}
}

// stairs reports whether a point is on a tiered aisle.
func (w *World) stairs(x, y float64) bool {
	for _, r := range w.vs.stairs {
		if r.has(x, y) {
			return true
		}
	}
	return false
}

// region is the room a person is in (their last known room in a doorway).
func (w *World) region(a *Agent) int {
	if r := w.vs.nav.regionAt(a.X, a.Y); r >= 0 {
		a.p.region = r
	}
	return a.p.region
}

// chooseExit picks the door or exit to head for: the cheapest in walking
// distance plus queue, with a bias toward the familiar one. nil = none open.
func (w *World) chooseExit(a *Agent) *Exit {
	p := a.p
	reg := w.region(a)
	var best *Exit
	bc := math.Inf(1)
	out := w.outField(p.emergency)
	cur := math.Inf(1)
	for _, e := range w.G.Exits {
		if !e.Open || (e.Emergency && !p.emergency && w.scn.ID != "gate") {
			continue
		}
		if reg >= 0 && e.regIn != reg && e.regOut != reg {
			continue // not a door of this room
		}
		if p.goal == gEnter {
			// Into the room (classroom) or through the turnstiles (gate).
			if w.scn.ID == "classroom" && !e.Inner {
				continue
			}
			if w.scn.ID == "gate" && e.Inner {
				continue
			}
		}
		mx, my := e.mid()
		side := math.Copysign(1, (a.X-mx)*e.nx+(a.Y-my)*e.ny)
		d := w.vs.nav.dist(w.portalField(e, -side), a.X, a.Y)
		if math.IsInf(d, 1) {
			continue
		}
		if p.goal == gLeave {
			// Plus the rest of the way out from the far side.
			rest := w.vs.nav.dist(out, mx-side*0.7*e.nx, my-side*0.7*e.ny)
			if math.IsInf(rest, 1) {
				continue
			}
			d += rest
		}
		// The queue in front: people within queueR on this side heading for it.
		q := 0
		for _, b := range w.agents {
			if b != a && b.p.target == e && math.Hypot(b.X-mx, b.Y-my) < queueR {
				q++
			}
		}
		width := math.Hypot(e.X1-e.X0, e.Y1-e.Y0)
		flow := specFlow * width
		if e.Rate > 0 {
			flow = e.Rate
		}
		cost := d + 1.2*float64(q)/flow // s of waiting at walking pace, in metres
		if e == p.familiar || (p.familiar != nil && e.regOut == p.familiar.regIn && e.Inner) {
			cost *= 0.75
		}
		if e == p.target {
			cur = cost
		}
		if cost < bc {
			best, bc = e, cost
		}
	}
	// Hysteresis: only change for a clearly better door.
	if p.target != nil && p.target.Open && best != p.target && !math.IsInf(cur, 1) && bc > cur*(1-rerouteGain) {
		return p.target
	}
	return best
}

// route sets the person's field for their goal from where they are.
func (w *World) route(a *Agent) {
	p := a.p
	p.reeval = w.T + reevalEvery*(0.7+0.6*w.rng.Float64())
	switch p.goal {
	case gLeave, gEnter:
		e := w.chooseExit(a)
		if e == nil {
			p.target, p.field = nil, nil
			return
		}
		if e != p.target {
			p.target = e
			mx, my := e.mid()
			p.side = math.Copysign(1, (a.X-mx)*e.nx+(a.Y-my)*e.ny)
		}
		p.field = w.portalField(e, -p.side)
	case gSeat:
		s := p.seat
		if s == nil {
			p.goal = gNone
			return
		}
		// The nearer aisle end of the row (in walking distance).
		r := s.row
		fl, fr := w.pointField(r.endL, r.walkY), w.pointField(r.endR, r.walkY)
		dl := w.vs.nav.dist(fl, a.X, a.Y) + math.Abs(s.x-r.endL)
		dr := w.vs.nav.dist(fr, a.X, a.Y) + math.Abs(s.x-r.endR)
		p.target = nil
		if dl <= dr {
			p.field, p.tx, p.ty = fl, r.endL, r.walkY
		} else {
			p.field, p.tx, p.ty = fr, r.endR, r.walkY
		}
	case gPOI:
		q := w.vs.pois[p.poi]
		p.target = nil
		p.field, p.tx, p.ty = w.pointField(q.x+1.0*q.nx, q.y+1.0*q.ny), q.x+1.0*q.nx, q.y+1.0*q.ny
	}
}

// poiSpot finds this person a place to stand near their POI (a half disc
// in front of it sized for poiDensity).
func (w *World) poiSpot(a *Agent) {
	p := a.p
	q := &w.vs.pois[p.poi]
	q.n++
	p.spotted = true
	rad := 0.6 + math.Sqrt(float64(q.n)/(math.Pi/2*poiDensity))
	base := math.Atan2(q.ny, q.nx)
	p.tx, p.ty = q.x+0.5*q.nx, q.y+0.5*q.ny
	for k := 0; k < 30; k++ {
		ang := base + (w.rng.Float64()-0.5)*math.Pi
		d := 0.4 + rad*math.Sqrt(w.rng.Float64())
		x, y := q.x+d*math.Cos(ang), q.y+d*math.Sin(ang)
		if w.clear(x, y, a.R) && w.vs.nav.regionAt(x, y) == w.vs.nav.regionAt(q.x+0.5*q.nx, q.y+0.5*q.ny) {
			p.tx, p.ty = x, y
			break
		}
	}
}

// aisleBusy reports whether someone is coming down the aisle toward the row
// end at (x, y) and would get there within about a second.
func (w *World) aisleBusy(a *Agent, x, y float64) bool {
	for _, b := range w.agents {
		if b == a || b.p.phase != phNav {
			continue
		}
		dx, dy := x-b.X, y-b.Y
		d := math.Hypot(dx, dy)
		if d > 2.0 || d < 1e-6 {
			continue
		}
		if (b.VX*dx+b.VY*dy)/d > 0.3 && d/math.Max(0.3, math.Hypot(b.VX, b.VY)) < 1.5 {
			return true
		}
	}
	return false
}

// outflow reports whether people are coming out through door e right now.
func (w *World) outflow(e *Exit) int {
	mx, my := e.mid()
	n := 0
	for _, b := range w.agents {
		if b.p.goal == gLeave && b.p.target == e && math.Hypot(b.X-mx, b.Y-my) < 2.5 {
			n++
		}
	}
	return n
}

// venueIntent is what a person in a furnished venue wants this tick.
func (w *World) venueIntent(a *Agent) (speed, ex, ey float64, h how) {
	p := a.p
	a.seated = p.seat != nil && (p.phase == phIdle && p.sitting || p.phase == phSitting || p.phase == phLetPass || p.phase == phRising && p.sitting)
	switch p.phase {
	case phIdle:
		if p.goal == gNone {
			// Back from the bar / toilets when the trip is over (or the show resumes).
			if p.poi >= 0 && p.seat != nil && w.T >= p.dwellUntil && (w.vs.mode == ActCalm || w.vs.mode == ActIntermission) {
				w.setGoal(a, gSeat)
				p.delay = w.lognormal(3, 0.6)
			}
			return 0, 0, 0, howStand
		}
		// Pre-movement: own delay, hurried along by the people around.
		p.ready += Dt * (1 + herdGain*p.herd)
		if p.ready < p.delay {
			return 0, 0, 0, howStand
		}
		if p.readyAt == 0 {
			p.readyAt = w.T
		}
		// Friends wait for each other (for a while).
		if f := p.friend; f != nil && !p.ahead && f.p.goal == p.goal && f.p.phase == phIdle && f.p.ready < f.p.delay && w.T-p.readyAt < friendWait {
			return 0, 0, 0, howStand
		}
		if f := p.friend; f != nil {
			f.p.ahead = true
		}
		if p.sitting {
			p.phase, p.phaseT, p.sitting = phRising, w.T, false
			a.transT, a.transDir = w.T, 1
			return 0, 0, 0, howStand
		}
		p.phase, p.phaseT = phNav, w.T
		w.route(a)
		return 0, 0, 0, howStand
	case phLetPass:
		switch {
		case p.goal == gSeat:
			w.sitDown(a, p.seat) // standing at the seat anyway
			a.transT, a.transDir = w.T, -1
		case p.goal != gNone && w.T-p.phaseT >= riseSec:
			p.phase, p.phaseT = phRising, w.T-riseSec // already on their feet
		}
		return 0, 0, 0, howStand
	case phRising:
		if w.T-p.phaseT < riseSec {
			return 0, 0, 0, howStand
		}
		a.seated = false
		if p.seat != nil && p.seat.occ == a {
			p.seat.occ = nil // the seat is free while they are away (someone may take it); they still own it
		}
		// Into the seatway, then along the row to the aisle.
		r := p.seat.row
		p.rowEnd = r.endL
		if math.Abs(a.X-r.endR) < math.Abs(a.X-r.endL) {
			p.rowEnd = r.endR
		}
		if p.goal == gLeave && p.target == nil {
			// Toward the end nearer the door they will take.
			if e := w.chooseExit(a); e != nil {
				mx, my := e.mid()
				if math.Abs(mx-r.endR)+math.Abs(my-r.walkY) < math.Abs(mx-r.endL)+math.Abs(my-r.walkY) {
					p.rowEnd = r.endR
				} else {
					p.rowEnd = r.endL
				}
			}
		}
		p.phase, p.phaseT, p.sub = phRow, w.T, 0
		fallthrough
	case phRow:
		r := p.seat.row
		if math.Abs(a.Y-r.walkY) > 0.8 || a.X < math.Min(r.endL, r.x0)-0.6 || a.X > math.Max(r.endR, r.x1)+0.6 {
			// Pushed out of the row (a crowd at the door): find the way from here.
			p.phase, p.phaseT = phNav, w.T
			w.route(a)
			return 0, 0, 0, howStand
		}
		if math.Abs(a.X-p.rowEnd) < 0.3 {
			p.phase, p.phaseT = phMerge, w.T
			return 0, 0, 0, howStand
		}
		if p.goal == gSeat && p.seat != nil && p.seat.occ == nil && math.Abs(a.X-p.seat.x) < 0.3 {
			p.phase, p.phaseT, p.sub = phRowIn, w.T, 1 // sent back to the seat before reaching the aisle
			return 0, 0, 0, howStand
		}
		dir := math.Copysign(1, p.rowEnd-a.X)
		if w.rowBlocked(a, dir, 3.5) && w.dodge(a, phRow) {
			return 0, 0, 0, howStand
		}
		if p.sub == 0 {
			// Step into the seatway first (edging along a little as you do).
			if math.Abs(a.Y-r.walkY) < 0.2 {
				p.sub = 1
			} else {
				ex, ey, _ = toward(a, a.X+0.4*dir, r.walkY)
				return 0.5 * a.V0, ex, ey, howWalk
			}
		}
		ex, ey, _ = toward(a, p.rowEnd, r.walkY)
		return rowSpeedK * a.V0, ex, ey, howWalk
	case phDodge:
		d := math.Hypot(a.X-p.dodgeX, a.Y-p.dodgeY)
		if d > 0.15 && w.T-p.phaseT < 3 {
			ex, ey, _ = toward(a, p.dodgeX, p.dodgeY)
			return 0.5 * a.V0, ex, ey, howWalk
		}
		dir := math.Copysign(1, math.Cos(a.hd))
		if p.resume == phRow {
			dir = math.Copysign(1, p.rowEnd-a.X)
		} else if p.resume == phRowIn && p.seat != nil {
			dir = math.Copysign(1, p.seat.x-a.X)
		}
		if w.T-p.phaseT > 8 || (w.T-p.phaseT > 1.5 && !w.rowOncoming(a, dir) && !w.rowOncoming(a, -dir)) {
			p.phase, p.phaseT, p.sub = p.resume, w.T, 0
		}
		return 0, 0, 0, howStand
	case phMerge:
		r := p.seat.row
		wait := mergeMin + (mergeMax-mergeMin)*float64(a.ID%7)/6
		if p.goal == gLeave && w.vs.urgency > 0.5 {
			wait *= 0.4
		}
		if w.T-p.phaseT < wait && w.aisleBusy(a, p.rowEnd, r.walkY) {
			return 0, 0, 0, howStand
		}
		p.phase, p.phaseT = phNav, w.T
		w.route(a)
		return 0, 0, 0, howStand
	case phNav:
		if p.goal == gNone {
			p.phase = phIdle
			return 0, 0, 0, howStand
		}
		if w.T >= p.reeval && (p.goal == gLeave || p.goal == gEnter || p.field == nil) {
			w.route(a)
		}
		if p.field == nil {
			w.route(a)
			if p.field == nil {
				return 0, 0, 0, howStand // nowhere to go (every door closed): wait
			}
		}
		// Arrived?
		if e := p.target; e != nil {
			mx, my := e.mid()
			along := (a.X-mx)*e.nx + (a.Y-my)*e.ny
			if along*p.side < -0.3 {
				w.passed(a, e)
				return 0, 0, 0, howStand
			}
			// People coming in let the people coming out through first.
			if p.goal == gEnter && e.Inner && math.Abs(along) < 1.6 && w.T-p.phaseT < doorWaitMax && w.outflow(e) > 0 {
				if p.phase != phWaitDoor {
					p.phase, p.phaseT = phWaitDoor, w.T
				}
				return 0, 0, 0, howStand
			}
			if e.Rate > 0 && math.Abs(along) < 0.8 && e.token != a {
				// At a turnstile that isn't ours yet: queue.
				return 0, 0, 0, howStand
			}
		} else if d := math.Hypot(a.X-p.tx, a.Y-p.ty); d < 0.6 || (p.goal == gPOI && d < 2.0 && w.vs.nav.clearLine(a.X, a.Y, w.vs.nav.cell(p.tx, p.ty))) {
			switch p.goal {
			case gSeat:
				p.phase, p.phaseT, p.sub = phRowIn, w.T, 0
			case gPOI:
				w.poiSpot(a)
				p.phase, p.phaseT = phSettle, w.T
			default:
				p.phase, p.goal = phIdle, gNone
			}
			return 0, 0, 0, howStand
		}
		ex, ey, ok := w.vs.nav.dir(p.field, a.X, a.Y)
		if !ok {
			// At the field's end: count it as arrived. Nowhere to go from
			// here (deep in a wall somehow): wait for the next re-route.
			switch {
			case p.target != nil:
				w.passed(a, p.target)
			case math.Hypot(a.X-p.tx, a.Y-p.ty) > 1.2:
				p.reeval = math.Min(p.reeval, w.T+1)
			case p.goal == gSeat:
				p.phase, p.phaseT, p.sub = phRowIn, w.T, 0
			case p.goal == gPOI:
				w.poiSpot(a)
				p.phase, p.phaseT = phSettle, w.T
			default:
				p.phase, p.goal = phIdle, gNone
			}
			return 0, 0, 0, howStand
		}
		// Giving way: someone who meets people coming the other way in a
		// narrow aisle, and has less reason to hurry than they do, stops
		// and lets them past (walkers step round a stander; two walkers
		// pressing on would lock).
		if w.T < p.yieldUntil {
			return 0, 0, 0, howStand
		}
		if r := w.rowAt(a.X, a.Y); r != nil {
			// Pushed into a seat row on the way: out along it, stepping into
			// an empty seat for anyone coming the other way.
			if w.rowBlocked(a, math.Copysign(1, ex), 2.0) && w.dodge(a, phNav) {
				return 0, 0, 0, howStand
			}
		}
		if math.Hypot(a.VX, a.VY) < 0.15 && p.heldT > 1.0 && w.oncoming(a, ex, ey) {
			p.yieldUntil = w.T + 2.5
			p.heldT = 0
			return 0, 0, 0, howStand
		}
		if p.goal == gPOI && p.heldT > 8 {
			// Can't get any nearer the bar: stand where you are (it's that busy).
			p.tx, p.ty = a.X, a.Y
			p.spotted = true
			w.vs.pois[p.poi].n++
			p.phase, p.phaseT = phSettle, w.T
			return 0, 0, 0, howStand
		}
		speed = a.V0
		if w.stairs(a.X, a.Y) {
			speed *= stairK
		}
		if p.goal == gLeave && w.vs.urgency > 0 {
			speed *= 1 + 0.15*w.vs.urgency
		}
		return w.impatience(a, speed, ex, ey)
	case phWaitDoor:
		e := p.target
		if e == nil {
			p.phase = phNav
			return 0, 0, 0, howStand
		}
		if w.T-p.phaseT >= doorWaitMax || w.outflow(e) == 0 {
			p.phase, p.phaseT = phNav, w.T-doorWaitMax // don't wait again at this door
		}
		return 0, 0, 0, howStand
	case phSettle:
		d := math.Hypot(a.X-p.tx, a.Y-p.ty)
		if d < settleDone || w.T-p.phaseT > settleMaxSec {
			p.phase, p.phaseT = phIdle, w.T
			if p.goal == gPOI {
				p.goal = gNone // standing at the bar until dwellUntil
			}
			return 0, 0, 0, howStand
		}
		ex, ey, _ = toward(a, p.tx, p.ty)
		return 0.7 * a.V0 * clamp((d-0.15)/0.8, 0.3, 1), ex, ey, howWalk
	case phRowIn:
		s := p.seat
		r := s.row
		if p.sub == 0 {
			// Along the seatway to the seat's x.
			if math.Abs(a.X-s.x) < 0.15 {
				p.sub = 1
			} else {
				if math.Abs(a.Y-r.walkY) > 0.6 {
					p.phase, p.phaseT = phNav, w.T // pushed out of the row: route back to its end
					w.route(a)
					return 0, 0, 0, howStand
				}
				dir := math.Copysign(1, s.x-a.X)
				if w.rowBlocked(a, dir, 1.5) && w.dodge(a, phRowIn) {
					return 0, 0, 0, howStand
				}
				ex, ey, _ = toward(a, s.x, r.walkY)
				return rowSpeedK * a.V0, ex, ey, howWalk
			}
		}
		if o := s.occ; o != nil && o != a {
			// Someone took the seat: take the nearest free one in the row instead.
			if ns := w.freeSeatNear(s); ns != nil {
				p.seat, s = ns, ns
				p.sub = 0
				return 0, 0, 0, howStand
			}
			p.goal, p.phase = gNone, phIdle
			return 0, 0, 0, howStand
		}
		if math.Hypot(a.X-s.x, a.Y-s.y) < 0.25 {
			w.sitDown(a, s)
			a.transT, a.transDir = w.T, -1
			return 0, 0, 0, howStand
		}
		ex, ey, _ = toward(a, s.x, s.y)
		return 0.5 * a.V0, ex, ey, howWalk
	case phSitting:
		if w.T-p.phaseT >= sitSec {
			p.phase, p.phaseT = phIdle, w.T
			if p.goal == gSeat {
				p.goal = gNone
			}
		}
		return 0, 0, 0, howStand
	}
	return 0, 0, 0, howStand
}

// impatience applies queueing and pushing to a walker's intent: held up
// longer than their patience, with urgency in the air, they press on.
func (w *World) impatience(a *Agent, speed, ex, ey float64) (float64, float64, float64, how) {
	p := a.p
	v := math.Hypot(a.VX, a.VY)
	if v < 0.25*speed {
		p.heldT += Dt
		p.freeT = 0
	} else {
		p.freeT += Dt
		if p.freeT > 1 {
			p.heldT = math.Max(0, p.heldT-Dt)
		}
	}
	urg := w.vs.urgency
	if urg > 0 && !p.push && p.heldT > p.patience/urg {
		p.push = true
	}
	if p.push && p.freeT > 3 {
		p.push = false
		p.heldT = 0
	}
	if p.push {
		return speed * (1 + pushGain*urg), ex, ey, howPress
	}
	return speed, ex, ey, howWalk
}

// rowBlocked reports whether a has been held up in the row for more than
// sec seconds with someone walking the other way ahead.
func (w *World) rowBlocked(a *Agent, dir, sec float64) bool {
	p := a.p
	if math.Hypot(a.VX, a.VY) < 0.1 {
		p.rowHeld += Dt
	} else {
		p.rowHeld = 0
	}
	return p.rowHeld > sec && w.rowOncoming(a, dir)
}

// passed: the person went through door or exit e.
func (w *World) passed(a *Agent, e *Exit) {
	p := a.p
	p.target, p.field = nil, nil
	if !e.Inner {
		return // leave() removes them beyond the edge
	}
	switch p.goal {
	case gEnter:
		// In the room: find a seat (classroom) near this door, or stand at the back.
		if s := w.freeSeat(a); s != nil {
			p.seat = s
			s.occ = a
			p.goal = gSeat
			p.phase, p.phaseT = phNav, w.T
			w.route(a)
			return
		}
		p.goal, p.phase = gNone, phIdle
	default:
		p.phase, p.phaseT = phNav, w.T
		w.route(a)
	}
}

// freeSeat is a free seat for someone who just came in: near the door,
// at the back first (latecomers don't walk to the front row).
func (w *World) freeSeat(a *Agent) *seat {
	var best *seat
	bc := math.Inf(1)
	for _, s := range w.vs.seats {
		if s.occ != nil {
			continue
		}
		c := math.Abs(s.x-a.X) + 0.6*math.Abs(s.y-a.Y) + 0.3*float64(a.ID%5)*w.rng.Float64()
		if c < bc {
			best, bc = s, c
		}
	}
	if best != nil {
		best.occ = a
	}
	return best
}

// freeSeatNear is the nearest free seat in the same row.
func (w *World) freeSeatNear(s *seat) *seat {
	var best *seat
	bd := math.Inf(1)
	for _, o := range s.row.seats {
		if o.occ == nil {
			if d := math.Abs(o.x - s.x); d < bd {
				best, bd = o, d
			}
		}
	}
	return best
}

// wireState is the state code of a person for the dashboard.
func (w *World) wireState(a *Agent) uint8 {
	p := a.p
	if p == nil {
		switch {
		case a.stand:
			return StStanding
		case w.Action == ActStage || w.Action == ActSurge || a.hold:
			return StPushing
		}
		return StWalking
	}
	switch p.phase {
	case phIdle, phSitting:
		if p.sitting {
			return StSeated
		}
		return StStanding
	case phRising, phLetPass, phMerge, phWaitDoor:
		return StStanding
	}
	if p.push {
		return StPushing
	}
	if p.phase == phNav && a.sp > 0.3 && math.Hypot(a.VX, a.VY) < 0.3 {
		return StQueueing
	}
	return StWalking
}

// venueFace is where someone standing still in a furnished venue faces.
func (w *World) venueFace(a *Agent) (x, y float64, ok bool) {
	p := a.p
	switch {
	case p.teacher:
		return a.X, a.Y + 10, true
	case p.goal == gNone && p.poi >= 0 && p.phase == phIdle:
		q := w.vs.pois[p.poi]
		return q.x, q.y, true
	case p.phase == phMerge, p.phase == phWaitDoor:
		if p.target != nil {
			mx, my := p.target.mid()
			return mx, my, true
		}
		return a.X + 10*math.Cos(a.hd), a.Y + 10*math.Sin(a.hd), true
	case p.phase == phNav && p.target != nil:
		mx, my := p.target.mid()
		return mx, my, true
	case p.seat != nil && (p.phase == phIdle || p.phase == phSitting || p.phase == phLetPass || p.phase == phRising):
		return a.X + 10*math.Cos(w.vs.front), a.Y + 10*math.Sin(w.vs.front), true
	}
	return a.X + 10*math.Cos(w.vs.front), a.Y + 10*math.Sin(w.vs.front), true
}
