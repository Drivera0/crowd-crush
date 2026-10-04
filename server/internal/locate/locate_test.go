package locate

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
)

const t0 = int64(1_700_000_000_000)

func testCfg() Config {
	c := DefaultConfig(24, 16)
	c.Bearing, c.HasBearing = 0, true
	return c
}

// mat is a rotation whose columns are the device's x, y, z axes in world
// east-north-up coordinates: a right-handed frame, as a real phone's.
type mat [3]vec3

func rotAxis(axis vec3, ang float64) mat {
	c, s := math.Cos(ang), math.Sin(ang)
	x, y, z := axis[0], axis[1], axis[2]
	// Rows of the rotation matrix.
	r := [3]vec3{
		{c + x*x*(1-c), x*y*(1-c) - z*s, x*z*(1-c) + y*s},
		{y*x*(1-c) + z*s, c + y*y*(1-c), y*z*(1-c) - x*s},
		{z*x*(1-c) - y*s, z*y*(1-c) + x*s, c + z*z*(1-c)},
	}
	return mat{{r[0][0], r[1][0], r[2][0]}, {r[0][1], r[1][1], r[2][1]}, {r[0][2], r[1][2], r[2][2]}}
}

func (m mat) mul(n mat) mat { // columns of m·n
	var o mat
	for j := 0; j < 3; j++ {
		for i := 0; i < 3; i++ {
			o[j][i] = m[0][i]*n[j][0] + m[1][i]*n[j][1] + m[2][i]*n[j][2]
		}
	}
	return o
}

// toDevice expresses a world vector in device axes.
func (m mat) toDevice(w vec3) vec3 { return vec3{dot3(w, m[0]), dot3(w, m[1]), dot3(w, m[2])} }

// azimuthOf is the compass heading (degrees) of a world vector's
// projection on the ground, and how long that projection is.
func azimuthOf(w vec3) (float64, float64) {
	a := math.Atan2(w[0], w[1]) * 180 / math.Pi
	if a < 0 {
		a += 360
	}
	return a, math.Hypot(w[0], w[1])
}

// walker feeds the estimator the summaries of a phone with attitude att
// walking at compass heading walkDeg for secs seconds: a bounce of 1.8 m/s²
// at 1.9 steps/s and a forward acceleration of 1.1 m/s² a quarter step
// ahead of it (the inverted pendulum), in a right-handed device frame.
func walker(e *Estimator, id string, att mat, walkDeg, secs float64, from int64, sendG bool) int64 {
	const f, av, af = 1.9, 1.8, 1.1
	wd := walkDeg * math.Pi / 180
	fwd := vec3{math.Sin(wd), math.Cos(wd), 0}
	g := att.toDevice(vec3{0, 0, -1})
	top, back := att[1], vec3{-att[2][0], -att[2][1], -att[2][2]}
	now := from
	for k := 0; k < int(secs*10); k++ {
		now += 100
		// Mean over the 100 ms, like a summary.
		var a vec3
		for j := 0; j < 5; j++ {
			ph := 2 * math.Pi * f * (float64(now-from)/1000 - 0.1 + 0.02*float64(j))
			w := vec3{af * math.Cos(ph) * fwd[0], af * math.Cos(ph) * fwd[1], av * math.Sin(ph)}
			d := att.toDevice(w)
			a[0], a[1], a[2] = a[0]+d[0]/5, a[1]+d[1]/5, a[2]+d[2]/5
		}
		s := Sample{T: now, AX: a[0], AY: a[1], AZ: a[2], HD: NoHeading, HB: NoHeading}
		if sendG {
			s.G = g
		}
		if az, l := azimuthOf(top); l >= 0.6 {
			s.HD = az
		} else {
			s.HB, _ = azimuthOf(back)
		}
		e.Motion(id, s)
		if (now-from)%250 == 0 {
			e.Step(now, nil)
		}
	}
	return now
}

// The direction of a walk comes out right for a real phone's right-handed
// axes, however it is carried, on a map turned any way.
func TestWalkDirection(t *testing.T) {
	up := vec3{0, 0, 1}
	// Chest, screen out, facing north: x = west (the body's left), y = up,
	// z = north.
	chest := mat{{-1, 0, 0}, {0, 0, 1}, {0, 1, 0}}
	carries := map[string]mat{
		"chest, facing the walk":   rotAxis(up, -40*math.Pi/180).mul(chest),
		"chest, turned 90°":        rotAxis(up, 50*math.Pi/180).mul(chest),
		"flat in the hand":         rotAxis(up, -40*math.Pi/180).mul(mat{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}),
		"tilted back 50°":          rotAxis(up, -40*math.Pi/180).mul(rotAxis(vec3{1, 0, 0}, -50*math.Pi/180).mul(mat{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}})),
		"pocket, top down, turned": rotAxis(up, 130*math.Pi/180).mul(rotAxis(vec3{0, 1, 0}, math.Pi).mul(rotAxis(vec3{1, 0, 0}, 0.3).mul(chest))),
		"on its side":              rotAxis(up, 70*math.Pi/180).mul(rotAxis(vec3{0, 1, 0}, math.Pi/2).mul(chest)),
	}
	for name, att := range carries {
		for _, bearing := range []float64{0, 30, 200} {
			cfg := testCfg()
			cfg.Bearing = bearing
			cfg.Entry = Entry{On: true, X: 12, Y: 8, Sigma: 0.5}
			cfg.MapConstraints, cfg.Spacing = false, 0
			e := New(cfg)
			e.Join("p", t0)
			p0, _ := e.Position("p")
			const walkDeg, secs = 40.0, 6.0
			walker(e, "p", att, walkDeg, secs, t0, true)
			p1, _ := e.Position("p")
			dx, dy := p1.X-p0.X, p1.Y-p0.Y
			// Expected: heading 40° on a map whose up is bearing.
			rel := (walkDeg - bearing) * math.Pi / 180
			ex, ey := math.Sin(rel), -math.Cos(rel)
			d := math.Hypot(dx, dy)
			if d < 3 || d > 9 {
				t.Errorf("%s, bearing %g: walked %.1f m in %g s", name, bearing, d, secs)
				continue
			}
			if ang := math.Acos((dx*ex+dy*ey)/d) * 180 / math.Pi; ang > 8 {
				t.Errorf("%s, bearing %g: direction %.0f° off (moved %.1f, %.1f; want along %.2f, %.2f)", name, bearing, ang, dx, dy, ex, ey)
			}
			if p1.Src&SrcSteps == 0 || !p1.Walking {
				t.Errorf("%s: sources %v, walking %v", name, SrcList(p1.Src), p1.Walking)
			}
		}
	}
}

// Bouncing on the spot is not walking; neither is a phone with no compass
// going anywhere on the map.
func TestNoInventedMotion(t *testing.T) {
	cfg := testCfg()
	cfg.Entry = Entry{On: true, X: 12, Y: 8, Sigma: 0.5}
	e := New(cfg)
	e.Join("jump", t0)
	e.Join("nocompass", t0)
	a, _ := e.Position("jump")
	b, _ := e.Position("nocompass")
	now := t0
	for k := 0; k < 200; k++ {
		now += 100
		ph := 2 * math.Pi * 2.0 * float64(k) / 10
		// Jumping to a beat, swaying at half of it.
		e.Motion("jump", Sample{T: now, AX: 1.5 * math.Sin(ph/2), AY: 3 * math.Sin(ph), HD: 90, HB: NoHeading})
		// Walking, but the phone sends no heading.
		e.Motion("nocompass", Sample{T: now, AY: 1.8 * math.Sin(ph), AZ: 1.1 * math.Cos(ph), HD: NoHeading, HB: NoHeading})
		if k%5 == 4 {
			e.Step(now, nil)
		}
	}
	for id, was := range map[string]Position{"jump": a, "nocompass": b} {
		p, _ := e.Position(id)
		if d := math.Hypot(p.FX-was.FX, p.FY-was.FY); d > 0.05 {
			t.Errorf("%s moved %.2f m", id, d)
		}
	}
}

// A phone that stands still is held where it is while its GPS wanders,
// and a phone placed exactly stays exact.
func TestStandingStill(t *testing.T) {
	e := New(testCfg())
	e.Fix("p", t0, Fix{X: 10, Y: 8, Exact: true})
	r := rand.New(rand.NewSource(1))
	now := t0
	bx, by := 5.0, -3.0
	for s := 0; s < 300; s++ {
		for k := 0; k < 20; k++ {
			now += 50
			if k%2 == 1 {
				e.Motion("p", Sample{T: now, AX: 0.02 * r.NormFloat64(), HD: NoHeading, HB: NoHeading})
			}
			if k%5 == 4 {
				e.Step(now, nil)
			}
			if k == 12 { // fixes arrive between steps
				bx += 0.2 * r.NormFloat64() // the bias wanders
				by += 0.2 * r.NormFloat64()
				e.GPS("p", now, 10+bx+r.NormFloat64(), 8+by+r.NormFloat64(), 6)
			}
		}
	}
	p, _ := e.Position("p")
	if d := math.Hypot(p.X-10, p.Y-8); d > 0.5 || p.Acc >= ExactBelow {
		t.Errorf("after 5 minutes of GPS 6 m off: %.2f m from where it was placed, σ %.2f", d, p.Acc)
	}
	// Nothing but GPS: the estimate is the fixes' centre, with their accuracy.
	e.GPS("g", t0, 15, 5, 6)
	q, ok := e.Position("g")
	if !ok || math.Hypot(q.X-15, q.Y-5) > 0.01 || math.Abs(AccRadius(q.Acc)-6) > 0.1 {
		t.Errorf("first fix: %+v ok %v", q, ok)
	}
}

// Fixes that keep saying the phone is elsewhere win in the end: a bias
// can't be many times the receiver's accuracy.
func TestMovedUnseen(t *testing.T) {
	cfg := testCfg()
	cfg.VenueW, cfg.VenueH = 100, 100
	e := New(cfg)
	e.Fix("p", t0, Fix{X: 10, Y: 10, Exact: true})
	now := t0
	for s := 0; s < 60; s++ {
		now += 1000
		e.Motion("p", Sample{T: now, HD: NoHeading, HB: NoHeading})
		e.GPS("p", now, 60, 10, 5) // 50 m away, every second
		e.Step(now, nil)
	}
	p, _ := e.Position("p")
	if p.X < 40 {
		t.Errorf("a minute of fixes 50 m away left the phone at %.1f (σ %.1f)", p.X, p.Acc)
	}
}

// A fix that is good along a line and poor across it (two beacons) moves
// the estimate along the line only.
func TestAnisotropicFix(t *testing.T) {
	e := New(testCfg())
	e.GPS("p", t0, 10, 8, 9) // σ ≈ 6 m
	e.Fix("p", t0+1, Fix{X: 14, Y: 3, Along: 0.7, Across: 12, AX: 1, AY: 0, Src: SrcBeacon})
	p, _ := e.Position("p")
	if math.Abs(p.FX-14) > 0.5 {
		t.Errorf("along the line: x %.2f, want 14", p.FX)
	}
	if math.Abs(p.FY-8) > 1.5 {
		t.Errorf("across the line: y %.2f, want it left near 8", p.FY)
	}
	ph := e.phones["p"]
	if sx, sy := math.Sqrt(ph.kf.p[0][0]), math.Sqrt(ph.kf.p[1][1]); sx > 0.8 || sy < 3 {
		t.Errorf("σx %.2f, σy %.2f: want small along the line, wide across", sx, sy)
	}
	if p.Src&SrcBeacon == 0 {
		t.Errorf("sources %v", SrcList(p.Src))
	}
	// A turned line.
	e.GPS("q", t0, 10, 8, 9)
	u := 1 / math.Sqrt2
	e.Fix("q", t0+1, Fix{X: 13, Y: 11, Along: 0.5, Across: 12, AX: u, AY: u})
	q, _ := e.Position("q")
	if along := (q.FX-13)*u + (q.FY-11)*u; math.Abs(along) > 0.5 {
		t.Errorf("turned line: %.2f m from the fix along it", along)
	}
}

// Nobody is in a wall, on the stage or outside, and a walk doesn't cross a
// wall.
func TestMapConstraints(t *testing.T) {
	cfg := testCfg()
	cfg.Walls = [][4]float64{{12, 4, 12, 16}}
	cfg.Stage = [][2]float64{{6, 0}, {18, 0}, {18, 2}, {6, 2}}
	g := newGeom(cfg)
	for _, c := range [][2]float64{{-5, 3}, {30, 20}, {10, 1}, {12.05, 8}, {11, 0.5}} {
		x, y := g.inside(c[0], c[1])
		if x < 0.2 || y < 0.2 || x > 23.8 || y > 15.8 || inPoly(cfg.Stage, x, y) || (math.Abs(x-12) < 0.2 && y > 4) {
			t.Errorf("inside(%g, %g) = %.2f, %.2f", c[0], c[1], x, y)
		}
		if d := math.Hypot(x-c[0], y-c[1]); c[0] > 0 && c[0] < 24 && d > 2 {
			t.Errorf("inside(%g, %g) moved it %.2f m", c[0], c[1], d)
		}
	}
	if dx, dy := g.slide(11, 8, 2, 1); dx != 0 || dy != 1 {
		t.Errorf("a step through the wall slid to %.2f, %.2f; want 0, 1", dx, dy)
	}
	if dx, dy := g.slide(11, 8, -2, 1); dx != -2 || dy != 1 {
		t.Errorf("a free step was changed to %.2f, %.2f", dx, dy)
	}
	// A GPS phone far off the map is at its edge, and counts.
	e := New(cfg)
	e.GPS("p", t0, -7, 8, 6)
	e.Step(t0+250, nil)
	if p, _ := e.Position("p"); p.X < 0.2 || p.X > 1 || p.Lost {
		t.Errorf("a fix 7 m off the edge: %+v", p)
	}
	cfg.MapConstraints = false
	e = New(cfg)
	e.GPS("p", t0, -7, 8, 6)
	e.Step(t0+250, nil)
	if p, _ := e.Position("p"); p.FX > -6 {
		t.Errorf("constraints off: %.2f", p.FX)
	}
}

// Estimates don't stack: phones that start on the entry spot spread out.
func TestSpacing(t *testing.T) {
	cfg := testCfg()
	cfg.Entry = Entry{On: true, X: 12, Y: 8, Sigma: 0.2}
	e := New(cfg)
	for i := 0; i < 40; i++ {
		e.Join(fmt.Sprint("p", i), t0)
	}
	var ps []Position
	for k := 1; k <= 20; k++ {
		ps = append(ps[:0], e.Step(t0+int64(k)*250, nil)...)
	}
	for i := range ps {
		for j := i + 1; j < len(ps); j++ {
			if d := math.Hypot(ps[i].X-ps[j].X, ps[i].Y-ps[j].Y); d < cfg.Spacing-0.06 { // a soft minimum: 0.34 m and up
				t.Fatalf("%s and %s are %.2f m apart", ps[i].ID, ps[j].ID, d)
			}
		}
	}
	// An exact phone is not pushed aside.
	e.Fix("x", t0+6000, Fix{X: 12, Y: 8, Exact: true})
	e.Step(t0+6250, nil)
	if p, _ := e.Position("x"); p.X != 12 || p.Y != 8 {
		t.Errorf("exact phone moved to %.3f, %.3f", p.X, p.Y)
	}
}

// push is an aperiodic shove: a few seconds of irregular horizontal
// acceleration, the same for everyone who is given the same seed.
func push(seed int64, n int) [][2]float64 {
	r := rand.New(rand.NewSource(seed))
	out := make([][2]float64, n)
	var x, y float64
	for i := range out {
		x += 0.35 * (r.NormFloat64() - 0.25*x)
		y += 0.35 * (r.NormFloat64() - 0.25*y)
		out[i] = [2]float64{x, y}
	}
	return out
}

// Phones that share an irregular motion are linked and pulled together;
// phones swaying to the same beat are not.
func TestMotionNeighbours(t *testing.T) {
	cfg := testCfg()
	e := New(cfg)
	// a and b are jostled together; c is jostled by something else; d and
	// e sway to one beat. All start 5–6 m apart with σ ≈ 4 m.
	at := map[string][2]float64{"a": {8, 8}, "b": {13, 9}, "c": {10, 4}, "d": {6, 12}, "e": {12, 13}}
	for id, p := range at {
		e.GPS(id, t0, p[0], p[1], 6)
	}
	shared, other := push(1, 400), push(2, 400)
	now := t0
	var pos map[string]Position
	for k := 0; k < 300; k++ {
		now += 100
		s := shared[k]
		// b's phone is turned 110° about the vertical relative to a's.
		c110, s110 := math.Cos(1.92), math.Sin(1.92)
		e.Motion("a", Sample{T: now, AX: s[0], AZ: s[1], HD: NoHeading, HB: NoHeading})
		e.Motion("b", Sample{T: now, AX: c110*s[0] - s110*s[1], AZ: s110*s[0] + c110*s[1], HD: NoHeading, HB: NoHeading})
		e.Motion("c", Sample{T: now, AX: other[k][0], AZ: other[k][1], HD: NoHeading, HB: NoHeading})
		beat := math.Sin(2 * math.Pi * 1.0 * float64(k) / 10)
		e.Motion("d", Sample{T: now, AX: 1.2 * beat, HD: NoHeading, HB: NoHeading})
		e.Motion("e", Sample{T: now, AZ: 0.9 * beat, HD: NoHeading, HB: NoHeading})
		if k%10 == 9 {
			for _, id := range []string{"a", "b", "c", "d", "e"} {
				e.GPS(id, now, at[id][0], at[id][1], 6)
			}
		}
		if (now-t0)%250 == 0 {
			pos = map[string]Position{}
			for _, p := range e.Step(now, nil) {
				pos[p.ID] = p
			}
		}
	}
	d := func(x, y string) float64 { return math.Hypot(pos[x].X-pos[y].X, pos[x].Y-pos[y].Y) }
	if d("a", "b") > cfg.LinkRange+0.05 || pos["a"].Links != 1 || pos["a"].Src&SrcNear == 0 {
		t.Errorf("a and b share their motion: %.2f m apart, links %d, sources %v", d("a", "b"), pos["a"].Links, SrcList(pos["a"].Src))
	}
	if pos["c"].Links != 0 || pos["d"].Links != 0 || pos["e"].Links != 0 {
		t.Errorf("links: c %d, d %d, e %d; want none", pos["c"].Links, pos["d"].Links, pos["e"].Links)
	}
	if d("d", "e") < 5 {
		t.Errorf("two phones swaying to one beat were pulled to %.2f m", d("d", "e"))
	}
	if pos["a"].Acc >= pos["c"].Acc {
		t.Errorf("a linked phone (σ %.2f) should be surer than an unlinked one (σ %.2f)", pos["a"].Acc, pos["c"].Acc)
	}
	// A mesh report puts a pair forward; it doesn't link two phones whose
	// traces the server can compare and finds different.
	for i := 0; i < 10; i++ {
		e.Near("a", now, []Peer{{ID: "c", Corr: 0.95, Hops: 1}})
		now += 1000
		for _, id := range []string{"a", "b", "c"} {
			e.Motion(id, Sample{T: now, HD: NoHeading, HB: NoHeading})
		}
		e.Step(now, nil)
	}
	if p, _ := e.Position("c"); p.Links != 0 {
		t.Errorf("a mesh report alone linked c (%d links)", p.Links)
	}
}

// A phone that started at the entry and was never seen to walk is not
// believed to be there for long; a silent phone that had settled is.
func TestLost(t *testing.T) {
	cfg := testCfg()
	cfg.Entry = Entry{On: true, X: 4, Y: 14, Sigma: 1.5}
	e := New(cfg)
	e.Join("bag", t0)
	now := t0
	var p Position
	for k := 0; k < 300; k++ { // 30 s of standing-like motion
		now += 100
		e.Motion("bag", Sample{T: now, HD: NoHeading, HB: NoHeading})
		if k%5 == 4 {
			p = e.Step(now, nil)[0]
		}
	}
	if !p.Lost || p.Acc < cfg.LostSigma {
		t.Errorf("30 s at the entry without a step: σ %.1f lost %v", p.Acc, p.Lost)
	}
	e.Fix("settled", now, Fix{X: 12, Y: 8, Exact: true})
	for k := 0; k < 240; k++ { // a minute of silence
		now += 250
		e.Step(now, nil)
	}
	if q, _ := e.Position("settled"); q.Lost || q.Acc > 2 {
		t.Errorf("a minute of silence: σ %.2f lost %v", q.Acc, q.Lost)
	}
	e.Gone("settled", now)
	e.Gone("settled", now+20_000) // told again: the first time counts
	e.Step(now+cfg.ForgetMs+1, nil)
	if e.Known("settled") {
		t.Error("a phone gone for ForgetMs is still held")
	}
}

// The phone's own dead reckoning is running totals: a lost report loses
// nothing, a page reload doesn't jump.
func TestDR(t *testing.T) {
	cfg := testCfg()
	cfg.Bearing = 0
	cfg.Entry = Entry{On: true, X: 12, Y: 12, Sigma: 0.5}
	cfg.Spacing = 0
	e := New(cfg)
	e.Join("p", t0)
	p0, _ := e.Position("p")
	e.DR("p", t0, 100, 3, 4)
	e.DR("p", t0+1000, 104, 3, 6.5)
	// (a report at +2 s is lost)
	e.DR("p", t0+3000, 112, 3, 11.5)
	e.Step(t0+3250, nil)
	p, _ := e.Position("p")
	if math.Abs(p.X-p0.X) > 0.01 || math.Abs((p0.Y-p.Y)-7.5) > 0.01 {
		t.Errorf("7.5 m north: moved %.2f, %.2f", p.X-p0.X, p.Y-p0.Y)
	}
	e.DR("p", t0+4000, 2, 0.5, 0.5) // the page was reloaded
	e.DR("p", t0+5000, 4, 0.5, 2)
	e.Step(t0+5250, nil)
	q, _ := e.Position("p")
	if math.Abs((p.Y-q.Y)-1.5) > 0.01 {
		t.Errorf("after a reload: moved %.2f north, want 1.5", p.Y-q.Y)
	}
	// Steps without displacement: the phone walks, nobody knows where to.
	before := q.Acc
	e.DR("p", t0+6000, 24, 0.5, 2)
	e.Step(t0+6250, nil)
	r, _ := e.Position("p")
	if r.X != q.X || r.Y != q.Y || r.Acc <= before+1 {
		t.Errorf("20 steps without a direction: moved %.2f, σ %.2f → %.2f", math.Hypot(r.X-q.X, r.Y-q.Y), before, r.Acc)
	}
}

// BenchmarkStep1000 is one estimator step with 1000 phones in a 60 × 40 m
// venue: all streaming motion, all with GPS, two thirds being jostled (the
// motion-neighbour search at full load).
func BenchmarkStep1000(b *testing.B) {
	cfg := DefaultConfig(60, 40)
	cfg.Bearing, cfg.HasBearing = 0, true
	e := New(cfg)
	r := rand.New(rand.NewSource(1))
	const n = 1000
	ids := make([]string, n)
	traces := make([][][2]float64, 40)
	for i := range traces {
		traces[i] = push(int64(i+1), 2000)
	}
	for i := range ids {
		ids[i] = fmt.Sprintf("p%04d", i)
		e.GPS(ids[i], t0, 60*r.Float64(), 40*r.Float64(), 5+3*r.Float64())
	}
	now := t0
	feed := func(k int) {
		now += 100
		for i, id := range ids {
			s := Sample{T: now, HD: float64((i * 7) % 360), HB: NoHeading, AY: 0.3 * r.NormFloat64()}
			if i%3 != 0 {
				tr := traces[i%len(traces)][k%2000]
				s.AX, s.AZ = tr[0]+0.3*r.NormFloat64(), tr[1]+0.3*r.NormFloat64()
			}
			e.Motion(id, s)
		}
	}
	k := 0
	for ; k < 60; k++ { // fill the traces
		feed(k)
		if k%5 == 4 {
			e.Step(now, nil)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		for j := 0; j < 3; j++ { // 250 ms of summaries, more or less
			feed(k)
			k++
		}
		if i%4 == 0 {
			for _, id := range ids {
				e.GPS(id, now, 60*r.Float64(), 40*r.Float64(), 6)
			}
		}
		b.StartTimer()
		e.Step(now, nil)
	}
	b.StopTimer()
	st := e.Stats()
	b.ReportMetric(float64(st.Compared), "pairs-compared")
	b.ReportMetric(float64(st.Links), "links")
}
