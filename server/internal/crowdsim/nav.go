package crowdsim

import (
	"container/heap"
	"fmt"
	"math"
)

// Navigation for venues with furniture (venue.go: classroom, auditorium,
// stadium gate). The concert has no obstacles between a person and where
// they are going, so a straight line and local steering suffice. Rows of
// desks or seats, inner walls and doors need a route: every person follows
// the gradient of a distance field over a grid of navCell cells, computed
// by Dijkstra from the target (a door, an exit, a point) with 8-connected
// moves and no corner cutting (the usual "floor field" of pedestrian
// models, e.g. Burstedde et al. 2001; Vadere and JuPedSim do the same).
// Fields are cached per target and thrown away when a door opens or
// closes, so people re-route when the geometry changes.
//
// Regions: the free cells with every portal (door and exit) blocked fall
// into rooms (hall, lobby, classroom, corridor, outside). A person's exit
// choice considers the portals of the room they are in.

const (
	navCell   = 0.25 // m
	navMargin = 1.0  // m the grid extends beyond the venue (cells beyond an exit)
	navClear  = 0.27 // m: a cell closer than this to a wall is blocked (body radius ≤ 0.26)
	navAhead  = 4    // cells of descent a walker looks ahead (1 m)
)

type nav struct {
	x0, y0  float64
	nx, ny  int
	blocked []bool
	wall    []bool  // blocked by a wall or furniture (as opposed to a seat row, which a body can walk along)
	hug     []bool  // free cell next to a blocked one (paths prefer the middle of an aisle)
	region  []int32 // room id per cell, −1 for blocked and portal cells
	regions int
	fields  map[string][]float32
	// Dijkstra and search scratch.
	pq    navHeap
	seen  []int
	visit []uint32
	mark  uint32
}

type navItem struct {
	c int
	d float32
}
type navHeap []navItem

func (h navHeap) Len() int           { return len(h) }
func (h navHeap) Less(i, j int) bool { return h[i].d < h[j].d }
func (h navHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *navHeap) Push(x any)        { *h = append(*h, x.(navItem)) }
func (h *navHeap) Pop() any          { o := *h; it := o[len(o)-1]; *h = o[:len(o)-1]; return it }
func (n *nav) cell(x, y float64) int { return n.cellXY(n.ix(x), n.iy(y)) }
func (n *nav) ix(x float64) int      { return int(math.Floor((x - n.x0) / navCell)) }
func (n *nav) iy(y float64) int      { return int(math.Floor((y - n.y0) / navCell)) }
func (n *nav) cellXY(ix, iy int) int {
	if ix < 0 || iy < 0 || ix >= n.nx || iy >= n.ny {
		return -1
	}
	return iy*n.nx + ix
}
func (n *nav) centre(c int) (x, y float64) {
	return n.x0 + (float64(c%n.nx)+0.5)*navCell, n.y0 + (float64(c/n.nx)+0.5)*navCell
}

// buildNav rasterises the solid segments (walls, furniture, closed doors)
// and the regions. Called on creation and whenever a door changes.
func (w *World) buildNav() {
	g := w.G
	n := w.vs.nav
	if n == nil {
		n = &nav{x0: -navMargin, y0: -navMargin}
		n.nx = int(math.Ceil((g.W+2*navMargin)/navCell)) + 1
		n.ny = int(math.Ceil((g.H+2*navMargin)/navCell)) + 1
		n.blocked = make([]bool, n.nx*n.ny)
		n.wall = make([]bool, n.nx*n.ny)
		n.hug = make([]bool, n.nx*n.ny)
		n.region = make([]int32, n.nx*n.ny)
		w.vs.nav = n
	}
	n.fields = map[string][]float32{}
	for i := range n.blocked {
		n.blocked[i] = false
	}
	// Walls. Each segment only touches the cells within navClear of it.
	mark := func(s Seg, clear float64) {
		minX, maxX := math.Min(s.X0, s.X1)-clear, math.Max(s.X0, s.X1)+clear
		minY, maxY := math.Min(s.Y0, s.Y1)-clear, math.Max(s.Y0, s.Y1)+clear
		for iy := max(0, n.iy(minY)); iy <= min(n.ny-1, n.iy(maxY)); iy++ {
			for ix := max(0, n.ix(minX)); ix <= min(n.nx-1, n.ix(maxX)); ix++ {
				c := iy*n.nx + ix
				x, y := n.centre(c)
				cx, cy := closest(s, x, y)
				if math.Hypot(cx-x, cy-y) < clear {
					n.blocked[c] = true
				}
			}
		}
	}
	for _, s := range w.solid {
		mark(s, navClear)
	}
	copy(n.wall, n.blocked)
	// Seat rows are not routes: people in a row walk along it to the aisle
	// (person.go, phRow); nobody plans a path through other people's seats.
	for _, r := range w.vs.rows {
		y0, y1 := math.Min(r.y, r.walkY)-0.4, math.Max(r.y, r.walkY)+0.4
		pitch := 0.6
		if len(r.seats) > 1 {
			pitch = r.seats[1].x - r.seats[0].x
		}
		x0, x1 := r.x0-pitch/2, r.x1+pitch/2
		for iy := max(0, n.iy(y0)); iy <= min(n.ny-1, n.iy(y1)); iy++ {
			for ix := max(0, n.ix(x0)); ix <= min(n.nx-1, n.ix(x1)); ix++ {
				n.blocked[iy*n.nx+ix] = true
			}
		}
	}
	// Open doors: the cells across the gap are free whatever the jambs say,
	// so a narrow door never pinches the grid shut (bodies squeeze through
	// in the physics anyway).
	for _, e := range g.Exits {
		if !e.Open {
			continue
		}
		mx, my := e.mid()
		half := math.Hypot(e.X1-e.X0, e.Y1-e.Y0)/2 - 0.12
		tx, ty := -e.ny, e.nx
		for iy := max(0, n.iy(my-half-0.8)); iy <= min(n.ny-1, n.iy(my+half+0.8)); iy++ {
			for ix := max(0, n.ix(mx-half-0.8)); ix <= min(n.nx-1, n.ix(mx+half+0.8)); ix++ {
				c := iy*n.nx + ix
				x, y := n.centre(c)
				lat := math.Abs((x-mx)*tx + (y-my)*ty)
				along := math.Abs((x-mx)*e.nx + (y-my)*e.ny)
				if lat <= math.Max(half, 0.13) && along <= 0.6 {
					n.blocked[c] = false
				}
			}
		}
	}
	for c := range n.hug {
		n.hug[c] = false
		if n.blocked[c] {
			continue
		}
		ix, iy := c%n.nx, c/n.nx
		for oy := -1; oy <= 1 && !n.hug[c]; oy++ {
			for ox := -1; ox <= 1; ox++ {
				if cc := n.cellXY(ix+ox, iy+oy); cc < 0 || n.blocked[cc] {
					n.hug[c] = true
					break
				}
			}
		}
	}
	w.buildRegions()
}

// buildRegions labels rooms: flood fill over free cells with every portal
// (open or closed) blocked.
func (w *World) buildRegions() {
	n := w.vs.nav
	for i := range n.region {
		n.region[i] = -1
	}
	portal := make([]bool, len(n.blocked))
	for _, e := range w.G.Exits {
		s := Seg{e.X0, e.Y0, e.X1, e.Y1}
		minX, maxX := math.Min(s.X0, s.X1)-0.4, math.Max(s.X0, s.X1)+0.4
		minY, maxY := math.Min(s.Y0, s.Y1)-0.4, math.Max(s.Y0, s.Y1)+0.4
		for iy := max(0, n.iy(minY)); iy <= min(n.ny-1, n.iy(maxY)); iy++ {
			for ix := max(0, n.ix(minX)); ix <= min(n.nx-1, n.ix(maxX)); ix++ {
				c := iy*n.nx + ix
				x, y := n.centre(c)
				cx, cy := closest(s, x, y)
				if math.Hypot(cx-x, cy-y) < 0.4 {
					portal[c] = true
				}
			}
		}
	}
	n.regions = 0
	var stack []int
	for c0 := range n.blocked {
		if n.blocked[c0] || portal[c0] || n.region[c0] >= 0 {
			continue
		}
		id := int32(n.regions)
		n.regions++
		n.region[c0] = id
		stack = append(stack[:0], c0)
		for len(stack) > 0 {
			c := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			ix, iy := c%n.nx, c/n.nx
			for _, d := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
				cc := n.cellXY(ix+d[0], iy+d[1])
				if cc < 0 || n.blocked[cc] || portal[cc] || n.region[cc] >= 0 {
					continue
				}
				n.region[cc] = id
				stack = append(stack, cc)
			}
		}
	}
	// Which rooms each portal joins: the cells a little way along its normal.
	for _, e := range w.G.Exits {
		mx, my := e.mid()
		e.regIn, e.regOut = -1, -1
		for _, d := range []float64{0.6, 0.9, 1.3} {
			if e.regIn < 0 {
				e.regIn = n.regionAt(mx-d*e.nx, my-d*e.ny)
			}
			if e.regOut < 0 {
				e.regOut = n.regionAt(mx+d*e.nx, my+d*e.ny)
			}
		}
	}
}

// blockedAt reports whether a point is in a blocked cell (a wall, furniture,
// a seat row) or off the grid.
func (n *nav) blockedAt(x, y float64) bool {
	c := n.cell(x, y)
	return c < 0 || n.blocked[c]
}

// regionAt is the room id at a point, −1 in a wall or a doorway.
func (n *nav) regionAt(x, y float64) int {
	c := n.cell(x, y)
	if c < 0 {
		return -1
	}
	return int(n.region[c])
}

// field is the distance (in cells) to the target from every cell, by key
// (cached). seeds are the target cells.
func (n *nav) field(key string, seeds []int) []float32 {
	if f, ok := n.fields[key]; ok {
		return f
	}
	f := make([]float32, len(n.blocked))
	for i := range f {
		f[i] = float32(math.Inf(1))
	}
	n.pq = n.pq[:0]
	for _, c := range seeds {
		if c >= 0 && c < len(f) {
			f[c] = 0
			heap.Push(&n.pq, navItem{c, 0})
		}
	}
	for n.pq.Len() > 0 {
		it := heap.Pop(&n.pq).(navItem)
		if it.d > f[it.c] {
			continue
		}
		ix, iy := it.c%n.nx, it.c/n.nx
		for oy := -1; oy <= 1; oy++ {
			for ox := -1; ox <= 1; ox++ {
				if ox == 0 && oy == 0 {
					continue
				}
				cc := n.cellXY(ix+ox, iy+oy)
				if cc < 0 || n.blocked[cc] {
					continue
				}
				step := float32(1)
				if ox != 0 && oy != 0 {
					// No cutting corners through a wall.
					a, b := n.cellXY(ix+ox, iy), n.cellXY(ix, iy+oy)
					if a < 0 || b < 0 || n.blocked[a] || n.blocked[b] {
						continue
					}
					step = math.Sqrt2
				}
				if n.hug[cc] {
					step *= 1.6
				}
				if d := it.d + step; d < f[cc] {
					f[cc] = d
					heap.Push(&n.pq, navItem{cc, d})
				}
			}
		}
	}
	n.fields[key] = f
	return f
}

// nearestFree is the closest routable cell to c (c itself if it is one),
// −1 if none within reach: someone standing in a seat row (rows are not
// routes) or pushed against a wall heads for it first. The search walks
// through row cells but never through walls, so from inside a row it
// finds the row's end in the aisle, not the aisle behind the seatback.
func (n *nav) nearestFree(c int, f []float32) int {
	if c < 0 {
		return -1
	}
	usable := func(cc int) bool { return !n.blocked[cc] && (f == nil || !math.IsInf(float64(f[cc]), 1)) }
	if usable(c) {
		return c
	}
	const reach = 40 // cells (10 m): the longest row
	if n.wall[c] {
		// In a wall: the nearest usable cell, whatever is between.
		ix, iy := c%n.nx, c/n.nx
		best, bd := -1, math.Inf(1)
		for oy := -6; oy <= 6; oy++ {
			for ox := -6; ox <= 6; ox++ {
				cc := n.cellXY(ix+ox, iy+oy)
				if cc < 0 || !usable(cc) {
					continue
				}
				if d := float64(ox*ox + oy*oy); d < bd {
					best, bd = cc, d
				}
			}
		}
		return best
	}
	// Breadth-first through non-wall cells.
	n.seen = append(n.seen[:0], c)
	n.mark++
	if n.visit == nil || len(n.visit) != len(n.blocked) {
		n.visit = make([]uint32, len(n.blocked))
	}
	n.visit[c] = n.mark
	for head := 0; head < len(n.seen) && head < reach*reach; head++ {
		cur := n.seen[head]
		ix, iy := cur%n.nx, cur/n.nx
		for _, d := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
			cc := n.cellXY(ix+d[0], iy+d[1])
			if cc < 0 || n.wall[cc] || n.visit[cc] == n.mark {
				continue
			}
			if usable(cc) {
				return cc
			}
			n.visit[cc] = n.mark
			n.seen = append(n.seen, cc)
		}
	}
	return -1
}

// dist is the field's distance (m) from a point to its target, +Inf if unreachable.
func (n *nav) dist(f []float32, x, y float64) float64 {
	c := n.nearestFree(n.cell(x, y), f)
	if c < 0 {
		return math.Inf(1)
	}
	return float64(f[c]) * navCell
}

// dir is the way to walk from (x, y) along field f: toward the cell a few
// steps down the field, as far as the straight line to it stays clear.
// ok = false at the target or when it is unreachable.
func (n *nav) dir(f []float32, x, y float64) (ex, ey float64, ok bool) {
	c0 := n.cell(x, y)
	c := n.nearestFree(c0, f)
	if c < 0 {
		return 0, 0, false
	}
	if c != c0 {
		// Pushed into a wall or a doorway cell: head for the free cell.
		tx, ty := n.centre(c)
		return unit(tx-x, ty-y)
	}
	if f[c] == 0 {
		return 0, 0, false
	}
	aim := c
	cur := c
	for k := 0; k < navAhead; k++ {
		ix, iy := cur%n.nx, cur/n.nx
		best, bv := -1, f[cur]
		for oy := -1; oy <= 1; oy++ {
			for ox := -1; ox <= 1; ox++ {
				cc := n.cellXY(ix+ox, iy+oy)
				if cc < 0 || n.blocked[cc] || f[cc] >= bv {
					continue
				}
				if ox != 0 && oy != 0 {
					a, b := n.cellXY(ix+ox, iy), n.cellXY(ix, iy+oy)
					if a < 0 || b < 0 || n.blocked[a] || n.blocked[b] {
						continue
					}
				}
				best, bv = cc, f[cc]
			}
		}
		if best < 0 {
			break
		}
		if k > 0 && !n.clearLine(x, y, best) {
			break
		}
		aim, cur = best, best
		if f[best] == 0 {
			break
		}
	}
	tx, ty := n.centre(aim)
	return unit(tx-x, ty-y)
}

// clearLine reports whether the straight line from (x, y) to cell c's
// centre crosses no blocked cell.
func (n *nav) clearLine(x, y float64, c int) bool {
	tx, ty := n.centre(c)
	steps := int(math.Ceil(math.Hypot(tx-x, ty-y)/(navCell*0.5))) + 1
	for i := 1; i < steps; i++ {
		t := float64(i) / float64(steps)
		cc := n.cell(x+(tx-x)*t, y+(ty-y)*t)
		if cc < 0 || n.blocked[cc] {
			return false
		}
	}
	return true
}

func unit(dx, dy float64) (float64, float64, bool) {
	l := math.Hypot(dx, dy)
	if l < 1e-6 {
		return 0, 0, false
	}
	return dx / l, dy / l, true
}

// portalField is the field toward the side of a door or exit that lies
// along dir × its normal (+1: the outward side; −1: back in).
func (w *World) portalField(e *Exit, dir float64) []float32 {
	n := w.vs.nav
	key := "p:" + e.ID
	if dir < 0 {
		key += ":in"
	}
	if f, ok := n.fields[key]; ok {
		return f
	}
	mx, my := e.mid()
	var seeds []int
	for _, d := range []float64{0.5, 0.75, 1.0} {
		if c := n.nearestFree(n.cell(mx+dir*d*e.nx, my+dir*d*e.ny), nil); c >= 0 {
			seeds = append(seeds, c)
		}
	}
	return n.field(key, seeds)
}

// pointField is the field toward a point.
func (w *World) pointField(x, y float64) []float32 {
	n := w.vs.nav
	c := n.nearestFree(n.cell(x, y), nil)
	key := fmt.Sprintf("pt:%d", c)
	return n.field(key, []int{c})
}

// outField is the field toward the nearest open outer exit (all of them).
func (w *World) outField(emergency bool) []float32 {
	n := w.vs.nav
	key := "out"
	if emergency {
		key = "out+"
	}
	if f, ok := n.fields[key]; ok {
		return f
	}
	var seeds []int
	for _, e := range w.G.Exits {
		if !e.Open || e.Inner || (e.Emergency && !emergency) {
			continue
		}
		mx, my := e.mid()
		if c := n.nearestFree(n.cell(mx+0.6*e.nx, my+0.6*e.ny), nil); c >= 0 {
			seeds = append(seeds, c)
		}
	}
	return n.field(key, seeds)
}
