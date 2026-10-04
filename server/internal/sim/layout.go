package sim

import (
	"math"
	"math/rand"
	"sort"
)

// Layouts.
const (
	// LayoutLine stands phones on a rows×cols grid, 0.6 m apart, like the
	// original line demo (x = X0 + col·0.6, y = Y0 + row·0.6).
	LayoutLine = "line"
	// LayoutCrowd scatters phones over the venue: ~70 % in one or two dense
	// groups (σ ≈ 0.7 m around attractor points), the rest anywhere.
	LayoutCrowd = "crowd"
)

// WalkSpeed is how fast travelling scenarios move through the crowd:
// 0.6 m per 0.25 s, the line demo's 250 ms per person.
const WalkSpeed = 0.6 / 0.25

// Layout says where the phones stand.
type Layout struct {
	Kind           string
	Rows, Cols     int     // line
	VenueW, VenueH float64 // m
	Spacing        float64 // line: m between neighbours
	X0, Y0         float64 // line: position of row 0, col 0
	Move           bool    // crowd: phones wander slowly around their spot
}

// LineLayout is the line demo on the default 24 × 16 m venue.
func LineLayout(rows, cols int) Layout {
	return Layout{Kind: LayoutLine, Rows: rows, Cols: cols}.withDefaults()
}

// CrowdLayout is a crowd on the default 24 × 16 m venue.
func CrowdLayout(move bool) Layout {
	return Layout{Kind: LayoutCrowd, Move: move}.withDefaults()
}

func (l Layout) withDefaults() Layout {
	if l.Kind == "" {
		l.Kind = LayoutLine
	}
	if l.VenueW <= 0 {
		l.VenueW = 24
	}
	if l.VenueH <= 0 {
		l.VenueH = 16
	}
	if l.Spacing <= 0 {
		l.Spacing = 0.6
	}
	if l.X0 == 0 && l.Y0 == 0 {
		l.X0, l.Y0 = 4.0, l.VenueH/2
	}
	if l.Rows <= 0 {
		l.Rows = 1
	}
	return l
}

// place puts every phone at its home position and sets its distance along
// the travel direction (+x).
func (s *Scenario) place(seed int64) {
	lay := s.Layout
	if lay.Kind == LayoutLine {
		for i, p := range s.phones {
			p.row, p.col = i/lay.Cols, i%lay.Cols
			p.hx = lay.X0 + float64(p.col)*lay.Spacing
			p.hy = lay.Y0 + float64(p.row)*lay.Spacing
			p.k = float64(p.col)
		}
		if s.Name == "gather" {
			s.planGather(rand.New(rand.NewSource(seed ^ 0x6a7e)))
		}
		return
	}
	r := rand.New(rand.NewSource(seed ^ 0x51ed2701))
	w, h := lay.VenueW, lay.VenueH
	scatter := func() (float64, float64) { return uniform(r, 1, w-1), uniform(r, 1, h-1) }
	if s.Name == "gather" {
		for _, p := range s.phones {
			p.hx, p.hy = scatter()
		}
	} else {
		ng := 1 + r.Intn(2)
		var att [][2]float64
		for g := 0; g < ng; g++ {
			att = append(att, [2]float64{uniform(r, 4, w-4), uniform(r, 3, h-3)})
		}
		for _, p := range s.phones {
			if r.Float64() < 0.7 {
				a := att[r.Intn(ng)]
				p.hx = clampF(a[0]+0.7*r.NormFloat64(), 0.3, w-0.3)
				p.hy = clampF(a[1]+0.7*r.NormFloat64(), 0.3, h-0.3)
			} else {
				p.hx, p.hy = scatter()
			}
		}
	}
	minX := math.Inf(1)
	for _, p := range s.phones {
		minX = math.Min(minX, p.hx)
	}
	s.minX = minX
	for _, p := range s.phones {
		p.k = (p.hx - minX) / lay.Spacing
		if lay.Move && s.Name != "gather" {
			p.mov = &mover{rng: rand.New(rand.NewSource(r.Int63()))}
		}
	}
	if s.Name == "gather" {
		s.planGather(r)
	}
}

// PosAt is phone i's position (m) at t seconds into the scenario.
func (s *Scenario) PosAt(i int, t float64) (x, y float64) {
	p := s.phones[i]
	x, y = p.hx, p.hy
	switch {
	case p.g != nil:
		x, y = p.g.pos(t, x, y)
	case p.mov != nil:
		dx, dy := p.mov.at(t)
		x, y = x+dx, y+dy
	}
	return clampF(x, 0, s.Layout.VenueW), clampF(y, 0, s.Layout.VenueH)
}

// kAt is phone i's position along the travel direction at time t, in
// people (0.6 m): fixed on the line, following the phone as it wanders in a
// crowd (a push reaches you where you stand now).
func (s *Scenario) kAt(i int, t float64) float64 {
	p := s.phones[i]
	if p.mov == nil {
		return p.k
	}
	x, _ := s.PosAt(i, t)
	return (x - s.minX) / s.Layout.Spacing
}

// Moves reports whether phones change position during the scenario.
func (s *Scenario) Moves() bool {
	for _, p := range s.phones {
		if p.g != nil || p.mov != nil {
			return true
		}
	}
	return false
}

// mover is a slow random walk (≤ 0.4 m/s) sprung back toward home, so
// groups persist. The trajectory is generated lazily in 0.1 s steps; one
// goroutine per phone may call at, never two for the same phone.
type mover struct {
	rng    *rand.Rand
	traj   [][2]float64 // offset from home at 0, 0.1, 0.2 … s
	vx, vy float64
}

func (m *mover) at(t float64) (dx, dy float64) {
	if t <= 0 {
		return 0, 0
	}
	const dt, vmax = 0.1, 0.4
	i := int(t / dt)
	if len(m.traj) == 0 {
		m.traj = append(m.traj, [2]float64{})
	}
	for len(m.traj) <= i+1 {
		o := m.traj[len(m.traj)-1]
		// Ornstein–Uhlenbeck velocity with a weak spring to home.
		m.vx += (-0.1*o[0]-1.0*m.vx)*dt + 0.1*math.Sqrt(dt)*m.rng.NormFloat64()
		m.vy += (-0.1*o[1]-1.0*m.vy)*dt + 0.1*math.Sqrt(dt)*m.rng.NormFloat64()
		if v := math.Hypot(m.vx, m.vy); v > vmax {
			m.vx, m.vy = m.vx*vmax/v, m.vy*vmax/v
		}
		m.traj = append(m.traj, [2]float64{o[0] + m.vx*dt, o[1] + m.vy*dt})
	}
	f := t/dt - float64(i)
	a, b := m.traj[i], m.traj[i+1]
	return a[0] + f*(b[0]-a[0]), a[1] + f*(b[1]-a[1])
}

// gatherPlan walks one phone from home to a slot in front of the stage and
// back: still until t0, walking until t1, packed in until t2, walking home
// until t3.
type gatherPlan struct {
	sx, sy         float64
	t0, t1, t2, t3 float64
}

// Gather timing (s): people set off between GatherStart and GatherStart+25,
// the group is complete by ~45 s, holds, and leaves from GatherLeave.
const (
	GatherStart = 3.0
	GatherLeave = 75.0
	// GatherDensity is how tightly the group packs (people/m²): crush
	// territory, past the default density danger level.
	GatherDensity = 10.0
)

// GatherCentre is where the group forms: in front of a stage at the top
// of the map, 2 m left of the middle so it sits clearly inside zone A.
func (s *Scenario) GatherCentre() (x, y float64) { return s.Layout.VenueW/2 - 2, 4.0 }

func (s *Scenario) planGather(r *rand.Rand) {
	cx, cy := s.GatherCentre()
	type cand struct {
		p      *phoneSim
		start  float64
		speed  float64
		arrive float64
	}
	var cs []cand
	for _, p := range s.phones {
		if r.Float64() >= 0.85 { // some people stay where they are
			continue
		}
		c := cand{p: p, start: GatherStart + uniform(r, 0, 25), speed: uniform(r, 1.0, 1.4)}
		c.arrive = c.start + math.Hypot(p.hx-cx, p.hy-cy)/c.speed
		cs = append(cs, c)
	}
	// First to arrive take the middle: a Vogel spiral at GatherDensity.
	sort.Slice(cs, func(i, j int) bool { return cs[i].arrive < cs[j].arrive })
	spacing := math.Sqrt(1 / (math.Pi * GatherDensity))
	golden := math.Pi * (3 - math.Sqrt(5))
	for k, c := range cs {
		rad := spacing * math.Sqrt(float64(k)+0.5)
		th := float64(k) * golden
		g := &gatherPlan{sx: cx + rad*math.Cos(th), sy: cy + rad*math.Sin(th), t0: c.start}
		d := math.Hypot(c.p.hx-g.sx, c.p.hy-g.sy)
		g.t1 = g.t0 + d/c.speed
		g.t2 = GatherLeave + uniform(r, 0, 10)
		g.t3 = g.t2 + d/c.speed
		c.p.g = g
	}
}

func (g *gatherPlan) walking(t float64) bool {
	return (t >= g.t0 && t < g.t1) || (t >= g.t2 && t < g.t3)
}

func (g *gatherPlan) pos(t, hx, hy float64) (x, y float64) {
	lerp := func(f float64, ax, ay, bx, by float64) (float64, float64) {
		return ax + f*(bx-ax), ay + f*(by-ay)
	}
	switch {
	case t < g.t0 || t >= g.t3:
		return hx, hy
	case t < g.t1:
		return lerp((t-g.t0)/(g.t1-g.t0), hx, hy, g.sx, g.sy)
	case t < g.t2:
		return g.sx, g.sy
	default:
		return lerp((t-g.t2)/(g.t3-g.t2), g.sx, g.sy, hx, hy)
	}
}

func clampF(v, lo, hi float64) float64 { return math.Max(lo, math.Min(v, hi)) }
