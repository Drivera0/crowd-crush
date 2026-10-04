package crowdsim

import (
	"math"
	"sort"
)

// Behaviour: what each person wants to do, on top of the Social Force
// physics in world.go. See the package doc for the model and its sources.

// purpose is what a group is doing.
type purpose int

const (
	pIdle   purpose = iota // standing at their spots, watching the show
	pToPOI                 // walking to the bar, toilets or merch
	pAtPOI                 // there, queueing or standing around
	pBack                  // returning to roughly their old spots
	pArrive                // just came in, looking for a spot
	pLeave                 // going home through an exit (arrivals and departures)
	pFollow                // attract: heading for the point
	pEvac                  // disperse: leaving through the nearest exit
)

// group is people who came together (Moussaïd et al. 2010). Members share
// a purpose and a target and walk in formation; someone alone is a group
// of one.
type group struct {
	id      int
	members []*Agent
	purpose purpose
	walking bool // in formation toward (tx, ty); false = members settling on their own spots
	tx, ty  float64
	exit    *Exit
	poi     int     // pToPOI, pAtPOI: index into World.pois
	until   float64 // pAtPOI: when to head back; pIdle: next trip to a POI
	since   float64 // when the current leg started
	startAt float64 // intermission: when to set off (0 = nothing pending)
	vg      float64 // formation walking speed
	cx, cy  float64 // centroid (this tick)
	hx, hy  float64 // heading (this tick)
	spread  float64 // largest member distance to the centroid (this tick)
	slow    float64 // s the walking group has been nearly stopped
}

// POI is a point of interest people walk to (bar, toilets, merch).
type POI struct {
	Name     string
	X, Y     float64 // service point, next to a wall
	NX, NY   float64 // unit normal into the venue
	weight   float64 // share of trips
	assigned int     // people with a spot here
}

// Behaviour parameters.
const (
	groupShare = 0.6 // share of people who arrive in a group of 2–4 (Moussaïd et al. 2010: up to 70 %)
	// Group sizes 2, 3, 4 in proportion (pairs dominate; Moussaïd et al. 2010, James 1953).
	tripEvery    = 720.0      // s, mean time between a group's trips to a POI during the show
	dwellMin     = 30.0       // s at a POI
	dwellMax     = 120.0      // s
	interShare   = 0.55       // share of idle groups heading out at an intermission
	interSpread  = 25.0       // s over which they set off
	interDwell   = 150.0      // s, longest intermission dwell (queues)
	churnPerSec  = 1.0 / 2400 // arrivals (and departures) per person already there, per second
	poiDensity   = 2.5        // people/m² around a POI (a busy bar)
	kSlot        = 0.8        // 1/s, formation spring: how hard a member steers to its slot
	settleDone   = 0.35       // m from its spot, a member has arrived
	settleMaxSec = 20.0       // s; after that a member stays wherever it got to
	legMaxSec    = 90.0       // s; a walk taking longer than this ends where it is
	blockedSec   = 6.0        // s nearly stopped before a group stops walking in formation
	accUp        = 1.0        // m/s², how fast desired speed may rise (Teknomo 2002 ~0.7–1.2)
	accDown      = 3.0        // m/s², voluntary slowing (time-gap braking is immediate)
	turnWalk     = 2.0        // rad/s, heading change while walking
	turnSlow     = 5.0        // rad/s, turning on the spot (speed < 0.3 m/s)
	tauStand     = 0.3        // s, how fast someone standing damps a shove's velocity
	standDead    = 150.0      // N, social forces below this don't move someone standing
	squeeze      = 0.3        // m/s, the least a person heading somewhere keeps going (excuse me…)
)

var groupSizeW = []float64{0.6, 0.27, 0.13} // sizes 2, 3, 4

// slots are formation offsets (forward, lateral; m) by group size: abreast
// when it is roomy, bent into a V (middle behind) or a U as it gets
// denser (Moussaïd et al. 2010, Fig. 3).
var slots = [][][2]float64{
	{{0, 0}},
	{{0, -0.375}, {0, 0.375}},
	{{0.1, -0.7}, {-0.2, 0}, {0.1, 0.7}},
	{{0.15, -1.05}, {-0.15, -0.35}, {-0.15, 0.35}, {0.15, 1.05}},
}

// groupSpeed scales the slowest member's desired speed by group size
// (Moussaïd et al. 2010: groups walk slower the bigger they are).
var groupSpeed = []float64{1, 0.9, 0.85, 0.8}

// steering headings for walkers in the venue: wider than the corridor's,
// to get round standing people.
var steerWide = []float64{0, 0.35, -0.35, 0.7, -0.7, 1.05, -1.05}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

// groupSize draws a group size: 1 with probability (1 − groupShare)
// scaled to people, otherwise 2–4.
func (w *World) groupSize() int {
	// Share of *groups* that are 2–4 so that groupShare of *people* are in one.
	mean := 0.0
	for i, p := range groupSizeW {
		mean += p * float64(i+2)
	}
	u := groupShare / (mean*(1-groupShare) + groupShare)
	if w.rng.Float64() >= u {
		return 1
	}
	r := w.rng.Float64()
	for i, p := range groupSizeW {
		if r < p {
			return i + 2
		}
		r -= p
	}
	return 4
}

// addGroup makes a group of these people (idle at their spots).
func (w *World) addGroup(ms []*Agent) *group {
	g := &group{id: w.nextGroup, members: ms, purpose: pIdle}
	w.nextGroup++
	vmin := math.Inf(1)
	for i, a := range ms {
		a.grp, a.slot = g, i
		vmin = math.Min(vmin, a.V0)
	}
	g.vg = vmin * groupSpeed[min(len(ms), 4)-1]
	g.until = w.T + w.rng.ExpFloat64()*tripEvery
	w.groups = append(w.groups, g)
	return g
}

// dropMember removes someone who left the venue from their group.
func (w *World) dropMember(a *Agent) {
	g := a.grp
	if g == nil {
		return
	}
	if a.atPOI >= 0 {
		w.pois[a.atPOI].assigned--
		a.atPOI = -1
	}
	keep := g.members[:0]
	for _, b := range g.members {
		if b != a {
			b.slot = len(keep)
			keep = append(keep, b)
		}
	}
	g.members = keep
	if len(keep) == 0 {
		gs := w.groups[:0]
		for _, h := range w.groups {
			if h != g {
				gs = append(gs, h)
			}
		}
		w.groups = gs
	}
}

// defaultPOIs: a bar on the right wall, toilets on the left, merch at the
// back, all in the back half of the room, pushed inward until clear of the
// stage and walls (a venue layout may put walls there).
func (w *World) defaultPOIs() {
	g := w.G
	cands := []POI{
		{Name: "bar", X: g.W - 0.6, Y: 0.72 * g.H, NX: -1, weight: 0.5},
		{Name: "toilets", X: 0.6, Y: 0.72 * g.H, NX: 1, weight: 0.35},
		{Name: "merch", X: g.W / 2, Y: g.H - 0.6, NY: -1, weight: 0.15},
	}
	for _, p := range cands {
		for k := 0; k < 12; k++ {
			if w.clear(p.X, p.Y, 0.3) {
				w.pois = append(w.pois, p)
				break
			}
			p.X += 0.5 * p.NX
			p.Y += 0.5 * p.NY
		}
	}
}

// POIs lists the points of interest (bar, toilets, merch).
func (w *World) POIs() []POI { return w.pois }

// clear reports whether a body of radius r fits at (x, y) as far as the
// venue goes (walls, stage), ignoring people.
func (w *World) clear(x, y, r float64) bool {
	if x < r+0.1 || y < r+0.1 || x > w.G.W-r-0.1 || y > w.G.H-r-0.1 || w.G.inStage(x, y) {
		return false
	}
	for _, s := range w.solid {
		cx, cy := closest(s, x, y)
		if math.Hypot(cx-x, cy-y) < r+0.05 {
			return false
		}
	}
	if !w.G.custom && y < w.G.BarrierY+r+0.05 && x > w.G.BarrierX0-r-0.05 && x < w.G.BarrierX1+r+0.05 {
		return false
	}
	return true
}

// routine reports whether the concert routine runs (trips, arrivals,
// departures): during the show, dancing and the intermission.
func (w *World) routine() bool {
	switch w.Action {
	case ActCalm, ActDance, ActIntermission, ActAttract:
		return true
	}
	return false
}

// music reports whether there is music to sway to.
func (w *World) music() bool {
	switch w.Action {
	case ActIntermission, ActDisperse, actCorridor:
		return false
	}
	return true
}

// toIdle stops a group where it is: every member's spot (and home) becomes
// where they stand.
func (w *World) toIdle(g *group) {
	g.purpose, g.walking, g.exit, g.startAt = pIdle, false, nil, 0
	for _, a := range g.members {
		if a.atPOI >= 0 {
			w.pois[a.atPOI].assigned--
			a.atPOI = -1
		}
		a.spotX, a.spotY, a.homeX, a.homeY = a.X, a.Y, a.X, a.Y
		a.done = true
	}
	g.until = w.T + w.rng.ExpFloat64()*tripEvery
}

// setOff starts a walk in formation toward (x, y).
func (w *World) setOff(g *group, p purpose, x, y float64) {
	g.purpose, g.walking, g.tx, g.ty, g.since, g.startAt = p, true, x, y, w.T, 0
	for _, a := range g.members {
		a.done = false
	}
}

// pickPOI chooses where a group goes (by the POIs' weights).
func (w *World) pickPOI() int {
	tot := 0.0
	for _, p := range w.pois {
		tot += p.weight
	}
	r := w.rng.Float64() * tot
	for i, p := range w.pois {
		if r < p.weight {
			return i
		}
		r -= p.weight
	}
	return len(w.pois) - 1
}

// goToPOI sends a group to a POI: each member gets a spot in the half-disc
// in front of it, sized for poiDensity, so a busy bar gets a crowd around it.
func (w *World) goToPOI(g *group, i int) {
	if len(w.pois) == 0 {
		return
	}
	p := &w.pois[i]
	for _, a := range g.members {
		if a.atPOI >= 0 {
			w.pois[a.atPOI].assigned--
		}
		p.assigned++
		a.atPOI = i
		r := 0.6 + math.Sqrt(float64(p.assigned)/(math.Pi/2*poiDensity))
		base := math.Atan2(p.NY, p.NX)
		a.spotX, a.spotY = p.X+0.4*p.NX, p.Y+0.4*p.NY
		for k := 0; k < 25; k++ {
			ang := base + (w.rng.Float64()-0.5)*math.Pi
			d := 0.4 + r*math.Sqrt(w.rng.Float64())
			x, y := p.X+d*math.Cos(ang), p.Y+d*math.Sin(ang)
			if w.clear(x, y, a.R) {
				a.spotX, a.spotY = x, y
				break
			}
		}
	}
	g.poi = i
	w.setOff(g, pToPOI, p.X+1.2*p.NX, p.Y+1.2*p.NY)
}

// goBack sends a group from a POI back to (roughly) where they stood.
func (w *World) goBack(g *group) {
	tx, ty := 0.0, 0.0
	for _, a := range g.members {
		if a.atPOI >= 0 {
			w.pois[a.atPOI].assigned--
			a.atPOI = -1
		}
		// Not exactly the same spot: someone else may be standing there.
		a.spotX = clamp(a.homeX+0.3*w.rng.NormFloat64(), 0.5, w.G.W-0.5)
		a.spotY = clamp(a.homeY+0.3*w.rng.NormFloat64(), 0.5, w.G.H-0.5)
		if !w.clear(a.spotX, a.spotY, a.R) {
			a.spotX, a.spotY = a.homeX, a.homeY
		}
		tx += a.spotX
		ty += a.spotY
	}
	n := float64(len(g.members))
	w.setOff(g, pBack, tx/n, ty/n)
}

// audienceSpot is a place in the audience with room to stand (local
// density under 2/m²), nearer the stage more often; ok = false if none found.
func (w *World) audienceSpot() (x, y float64, ok bool) {
	g := w.G
	y0, y1 := g.BarrierY+0.6, g.H-1.0
	for k := 0; k < 40; k++ {
		x = 1 + (g.W-2)*w.rng.Float64()
		y = y0 + (y1-y0)*math.Pow(w.rng.Float64(), 1.25)
		if !w.clear(x, y, 0.3) {
			continue
		}
		n := 0
		for _, a := range w.agents {
			if (a.X-x)*(a.X-x)+(a.Y-y)*(a.Y-y) < 1 {
				n++
			}
		}
		if float64(n) < 2*math.Pi {
			return x, y, true
		}
	}
	return 0, 0, false
}

// arrive brings a group in through a random open exit, heading for a spot.
func (w *World) arrive() {
	var open []*Exit
	for _, e := range w.G.Exits {
		if e.Open {
			open = append(open, e)
		}
	}
	if len(open) == 0 || len(w.agents) >= MaxPeople {
		return
	}
	e := open[w.rng.Intn(len(open))]
	tx, ty, ok := w.audienceSpot()
	if !ok {
		return
	}
	n := min(w.groupSize(), MaxPeople-len(w.agents))
	mx, my := e.mid()
	tlx, tly := -e.ny, e.nx // along the gap
	var ms []*Agent
	for k := 0; len(ms) < n && k < 40; k++ {
		s := (w.rng.Float64() - 0.5) * 0.8 * math.Hypot(e.X1-e.X0, e.Y1-e.Y0)
		in := 0.5 + 0.8*w.rng.Float64()
		x, y := mx-in*e.nx+s*tlx, my-in*e.ny+s*tly
		r := 0.20 + 0.06*w.rng.Float64()
		if !w.free(x, y, r) {
			continue
		}
		a := w.newAgent(x, y)
		a.R = r
		a.hd = math.Atan2(-e.ny, -e.nx)
		a.face = a.hd
		w.agents = append(w.agents, a)
		ms = append(ms, a)
	}
	if len(ms) == 0 {
		return
	}
	g := w.addGroup(ms)
	for i, a := range ms {
		ang := 2 * math.Pi * float64(i) / float64(len(ms))
		d := 0.0
		if len(ms) > 1 {
			d = 0.45
		}
		a.spotX, a.spotY = tx+d*math.Cos(ang), ty+d*math.Sin(ang)
		if !w.clear(a.spotX, a.spotY, a.R) {
			a.spotX, a.spotY = tx, ty
		}
		a.homeX, a.homeY = a.spotX, a.spotY
	}
	w.setOff(g, pArrive, tx, ty)
}

// depart sends a random idle group home through the nearest open exit.
func (w *World) depart() {
	var idle []*group
	for _, g := range w.groups {
		if g.purpose == pIdle && len(g.members) > 0 {
			idle = append(idle, g)
		}
	}
	if len(idle) == 0 {
		return
	}
	g := idle[w.rng.Intn(len(idle))]
	if e := w.nearestExit(g.cx, g.cy); e != nil {
		g.exit = e
		w.setOff(g, pLeave, 0, 0)
	}
}

// pickFollowers sends whole groups to the attraction point, nearest first
// (distance plus a random 0–6 m), until about 40 % of the crowd is going.
// Groups on a trip carry on; the rest stay where they are.
func (w *World) pickFollowers() {
	type cand struct {
		g *group
		k float64
	}
	var cs []cand
	for _, g := range w.groups {
		if g.purpose == pFollow || g.purpose == pEvac {
			w.toIdle(g)
		}
		if g.purpose == pIdle && len(g.members) > 0 {
			cs = append(cs, cand{g, math.Hypot(g.cx-w.attX, g.cy-w.attY) + 6*w.rng.Float64()})
		}
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].k < cs[j].k })
	want := int(math.Round(attractShare * float64(len(w.agents))))
	n := 0
	for _, c := range cs {
		if n >= want {
			break
		}
		w.setOff(c.g, pFollow, w.attX, w.attY)
		n += len(c.g.members)
	}
}

// frame updates every group's centroid, heading and spread.
func (w *World) frame() {
	for _, g := range w.groups {
		if len(g.members) == 0 {
			continue
		}
		cx, cy := 0.0, 0.0
		for _, a := range g.members {
			cx += a.X
			cy += a.Y
		}
		n := float64(len(g.members))
		g.cx, g.cy = cx/n, cy/n
		g.spread = 0
		for _, a := range g.members {
			g.spread = math.Max(g.spread, math.Hypot(a.X-g.cx, a.Y-g.cy))
		}
		tx, ty := g.tx, g.ty
		if g.purpose == pLeave || g.purpose == pEvac {
			if g.exit != nil {
				mx, my := g.exit.mid()
				tx, ty = mx-0.3*g.exit.nx, my-0.3*g.exit.ny
			}
		}
		if dx, dy := tx-g.cx, ty-g.cy; math.Hypot(dx, dy) > 0.2 {
			l := math.Hypot(dx, dy)
			g.hx, g.hy = dx/l, dy/l
		}
	}
}

// behave runs the concert routine and moves groups between purposes.
func (w *World) behave() {
	w.frame()
	if w.routine() && w.periodic == 0 {
		rate := float64(len(w.agents)) * churnPerSec
		if w.Churn && w.rng.Float64() < rate*Dt/1.6 { // ~1.6 people per group
			w.arrive()
		}
		if w.Churn && w.rng.Float64() < rate*Dt/1.6 {
			w.depart()
		}
	}
	for _, g := range w.groups {
		if len(g.members) == 0 {
			continue
		}
		switch g.purpose {
		case pIdle:
			if !w.routine() || !w.Trips || len(w.pois) == 0 {
				break
			}
			if g.startAt > 0 && w.T >= g.startAt {
				w.goToPOI(g, w.pickPOI())
				g.until = w.T + dwellMin + (interDwell-dwellMin)*w.rng.Float64()
				break
			}
			if g.startAt == 0 && w.T >= g.until && w.Action != ActAttract {
				w.goToPOI(g, w.pickPOI())
				g.until = w.T + dwellMin + (dwellMax-dwellMin)*w.rng.Float64()
			}
		case pToPOI, pBack, pArrive:
			w.walkOrSettle(g)
		case pAtPOI:
			if w.T >= g.until {
				w.goBack(g)
			}
		case pFollow:
			if g.walking && math.Hypot(g.cx-g.tx, g.cy-g.ty) < 4 {
				g.walking = false
			}
		case pLeave, pEvac:
			if g.exit == nil || !g.exit.Open {
				g.exit = w.nearestExit(g.cx, g.cy)
			}
			if g.exit != nil {
				mx, my := g.exit.mid()
				g.walking = math.Hypot(g.cx-mx, g.cy-my) > 3.5
			}
		}
	}
}

// walkOrSettle: a group walks in formation until its centroid is near the
// target, then each member walks to its own spot; when all are there (or
// gave up), the group moves on.
func (w *World) walkOrSettle(g *group) {
	near := 1.2 + 0.3*float64(len(g.members))
	if g.purpose == pToPOI {
		p := w.pois[g.poi]
		near = math.Max(near, 0.6+math.Sqrt(float64(p.assigned)/(math.Pi/2*poiDensity)))
	}
	if g.walking {
		// Blocked for a while (a packed crowd in the way): stop walking
		// as a group and make for the spots one by one, or give up there.
		v := 0.0
		for _, a := range g.members {
			v += math.Hypot(a.VX, a.VY)
		}
		if v/float64(len(g.members)) < 0.1 {
			g.slow += Dt
		} else {
			g.slow = 0
		}
		if math.Hypot(g.cx-g.tx, g.cy-g.ty) < near || w.T-g.since > legMaxSec || g.slow > blockedSec {
			g.walking, g.since, g.slow = false, w.T, 0
		}
		return
	}
	all := true
	for _, a := range g.members {
		if !a.done && (math.Hypot(a.X-a.spotX, a.Y-a.spotY) < settleDone || w.T-g.since > settleMaxSec) {
			a.done = true
			a.spotX, a.spotY = a.X, a.Y
		}
		all = all && a.done
	}
	if !all {
		return
	}
	switch g.purpose {
	case pToPOI:
		g.purpose = pAtPOI
	case pBack, pArrive:
		g.purpose = pIdle
		g.until = w.T + w.rng.ExpFloat64()*tripEvery
		for _, a := range g.members {
			a.homeX, a.homeY = a.X, a.Y
		}
	}
}

// how a person moves this tick.
type how int

const (
	howStand how = iota // standing: dead-band, no time gap
	howWalk             // walking: time gap and steering
	howPress            // pressing on regardless (stage, surge)
)

// slotVel is a group member's desired velocity in formation: the group's
// walking velocity plus a spring toward its slot.
func (w *World) slotVel(a *Agent, g *group, vg float64) (speed, ex, ey float64) {
	n := min(len(g.members), 4)
	off := slots[n-1][a.slot%n]
	fwd := off[0] * clamp((a.Density-0.6)/1.0, 0, 1)
	lat := off[1] * clamp(1-0.3*(a.Density-1), 0.4, 1)
	rx, ry := -g.hy, g.hx
	sx, sy := g.cx+g.hx*fwd+rx*lat, g.cy+g.hy*fwd+ry*lat
	vx, vy := vg*g.hx+kSlot*(sx-a.X), vg*g.hy+kSlot*(sy-a.Y)
	speed = math.Hypot(vx, vy)
	if speed < 1e-6 {
		return 0, 0, 0
	}
	ex, ey = vx/speed, vy/speed
	return math.Min(speed, 1.3*a.V0), ex, ey
}

// formationSpeed is the group's walking speed, held back while members
// are spread out so stragglers catch up.
func formationSpeed(g *group) float64 {
	return g.vg * clamp(1-(g.spread-1.6)/3, 0.3, 1)
}

// toward is the unit vector from a to (x, y) and the distance.
func toward(a *Agent, x, y float64) (ex, ey, d float64) {
	ex, ey = x-a.X, y-a.Y
	d = math.Hypot(ex, ey)
	if d < 1e-6 {
		return 0, 0, d
	}
	return ex / d, ey / d, d
}

// intent is what person i wants this tick: speed (before the time gap),
// direction, and how.
func (w *World) intent(a *Agent) (speed, ex, ey float64, h how) {
	g := w.G
	cx := (g.BarrierX0 + g.BarrierX1) / 2
	grp := a.grp
	switch w.Action {
	case actCorridor:
		a.gx, a.gy = a.X+10*a.cdir, a.Y
		ex, ey, _ = toward(a, a.gx, a.gy)
		return a.V0, ex, ey, howWalk
	case ActStage, ActSurge:
		// Head for the barrier just ahead, the group drifting toward the
		// middle together and keeping its members side by side.
		pull := 0.3
		if w.Action == ActSurge {
			pull = 0.3 + 0.3*w.strength
		}
		gcx, gcy := a.X, a.Y
		if grp != nil && len(grp.members) > 1 {
			gcx, gcy = grp.cx, grp.cy
		}
		tx := a.X + (cx-gcx)*pull
		if d := a.X - gcx; math.Abs(d) > 1.0 {
			tx -= 0.5 * (d - math.Copysign(1.0, d))
		}
		a.gx = clamp(tx, g.BarrierX0+0.3, g.BarrierX1-0.3)
		a.gy = g.BarrierY
		speed = a.V0
		if w.Action == ActSurge {
			speed = w.surgeSpeed(a)
		}
		if a.Y < gcy-0.8 { // ahead of the group: let the others catch up
			speed *= 0.8
		}
		ex, ey, _ = toward(a, a.gx, a.gy)
		return speed, ex, ey, howPress
	}
	if grp == nil {
		return 0, 0, 0, howStand
	}
	switch grp.purpose {
	case pFollow:
		if grp.walking {
			a.gx, a.gy = w.attX, w.attY
			speed, ex, ey = w.slotVel(a, grp, 0.8*formationSpeed(grp))
			return speed, ex, ey, howWalk
		}
		// Head for the point; stop pushing once it is crowded around you
		// (about 3.5 people/m² within 1 m) near the group.
		a.gx, a.gy = w.attX, w.attY
		ex, ey, d := toward(a, a.gx, a.gy)
		speed = 0.8 * a.V0 * clamp((d-0.3)/1.0, 0, 1)
		if a.Density >= attractPack && d < w.packR+1.5 {
			speed = 0
		}
		if speed == 0 {
			return 0, ex, ey, howStand
		}
		return speed, ex, ey, howWalk
	case pLeave, pEvac:
		e := grp.exit
		if e == nil {
			return 0, 0, 0, howStand
		}
		mx, my := e.mid()
		v := formationSpeed(grp)
		if grp.walking {
			a.gx, a.gy = mx, my
			speed, ex, ey = w.slotVel(a, grp, v)
			return speed, ex, ey, howWalk
		}
		// Aim for the gap from the inside, then straight out.
		if (mx-a.X)*e.nx+(my-a.Y)*e.ny > 0.6 {
			a.gx, a.gy = mx-0.3*e.nx, my-0.3*e.ny
		} else {
			a.gx, a.gy = mx+2*e.nx, my+2*e.ny
		}
		ex, ey, _ = toward(a, a.gx, a.gy)
		return math.Max(v, 0.8*a.V0), ex, ey, howWalk
	case pToPOI, pBack, pArrive:
		if grp.walking {
			a.gx, a.gy = grp.tx, grp.ty
			speed, ex, ey = w.slotVel(a, grp, formationSpeed(grp))
			return speed, ex, ey, howWalk
		}
		if a.done {
			return 0, 0, 0, howStand
		}
		a.gx, a.gy = a.spotX, a.spotY
		ex, ey, d := toward(a, a.spotX, a.spotY)
		return 0.7 * a.V0 * clamp((d-0.15)/0.8, 0.3, 1), ex, ey, howWalk
	}
	return 0, 0, 0, howStand
}

// desire sets each person's desired velocity for this tick: intent, then
// the time gap and steering (walkers), then smooth speed and heading.
func (w *World) desire() {
	w.behave() // may bring people in
	w.grid.build(w.agents)
	nFollow := 0
	for _, a := range w.agents {
		if a.grp != nil && a.grp.purpose == pFollow {
			nFollow++
		}
	}
	// Followers stop pressing once inside the disc they would fill at ~3.5/m².
	w.packR = math.Sqrt(float64(nFollow) / (attractPack * math.Pi))
	for i, a := range w.agents {
		target, ex, ey, h := w.intent(a)
		// Voluntary speed changes are gradual.
		if target > a.sp {
			a.sp = math.Min(target, a.sp+accUp*Dt)
		} else {
			a.sp = math.Max(target, a.sp-accDown*Dt)
		}
		if ex == 0 && ey == 0 {
			a.sp = 0
		}
		v, dx, dy := a.sp, ex, ey
		if h == howWalk && v > 0 {
			angles := steerWide
			if w.Action == actCorridor && !w.counter {
				angles = steerAngles // as calibrated against Weidmann
			}
			tg := TimeGap
			if a.grp != nil && a.grp.purpose == pEvac {
				tg = EvacTimeGap
			}
			v, dx, dy = w.steer(i, v, ex, ey, angles, tg)
			// Heading somewhere through a standing crowd: keep edging on.
			if w.Action != actCorridor && v < squeeze && a.sp > squeeze {
				v = squeeze
			}
		}
		// Smooth heading: no instant turns.
		if v > 1e-3 {
			wmax := turnWalk
			if a.sp < 0.3 {
				wmax = turnSlow
			}
			d := clamp(angDiff(math.Atan2(dy, dx), a.hd), -wmax*Dt, wmax*Dt)
			a.hd += d
		}
		a.dvx, a.dvy = v*math.Cos(a.hd), v*math.Sin(a.hd)
		a.stand = h == howStand && a.sp < 0.05
		a.ex, a.ey = 0, 0
		if v > 0.05 {
			a.ex, a.ey = math.Cos(a.hd), math.Sin(a.hd)
		}
		// Impatience bookkeeping: how fast this person actually gets on.
		along := a.VX*ex + a.VY*ey
		a.vbar += (Dt / 2.0) * (along - a.vbar)
		w.orient(a, h)
		if !a.stand {
			// Wander: an Ornstein–Uhlenbeck force, so walkers don't move
			// like a lattice.
			const tw = 1.5
			sq := math.Sqrt(2 * Dt / tw)
			a.wx += -a.wx*Dt/tw + sq*w.rng.NormFloat64()
			a.wy += -a.wy*Dt/tw + sq*w.rng.NormFloat64()
		}
	}
}

// orient turns the body: walkers face where they are going, people
// pressing face the way they push, people standing face the stage (or the
// bar they are queueing at), with a little slow fidgeting.
func (w *World) orient(a *Agent, h how) {
	const tj = 4.0
	a.yaw += -a.yaw*Dt/tj + 0.08*math.Sqrt(2*Dt/tj)*w.rng.NormFloat64()
	var target, rate float64
	switch {
	case h != howStand && a.sp > 0.25:
		target, rate = a.hd, turnWalk
	case h == howPress:
		target, rate = math.Atan2(a.gy-a.Y, a.gx-a.X), 1.0
	default:
		g := w.G
		fx, fy := clamp(a.X, g.BarrierX0+0.5, g.BarrierX1-0.5), g.BarrierY
		if a.atPOI >= 0 && a.grp != nil && a.grp.purpose == pAtPOI {
			p := w.pois[a.atPOI]
			fx, fy = p.X, p.Y
		}
		if math.Hypot(fx-a.X, fy-a.Y) < 0.3 {
			target = a.face
		} else {
			target = math.Atan2(fy-a.Y, fx-a.X) + a.yaw
		}
		rate = 1.0
	}
	d := angDiff(target, a.face)
	step := clamp(d*math.Min(1, 3*Dt), -rate*Dt, rate*Dt)
	if h != howStand && a.sp > 0.25 {
		step = clamp(d, -rate*Dt, rate*Dt)
	}
	a.prevFace = a.face
	a.face += step
}
