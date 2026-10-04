// Package detect turns per-phone motion into node statuses, travelling-wave
// edges between physical neighbours and zone alert levels.
//
// Math detects, AI explains: nothing in here calls out to a model.
//
// Phones are free points in the venue (metres, origin top-left, x right,
// y down). Neighbours are whoever is physically near: active phones within
// NeighbourRadius, each keeping its MaxNeighbours nearest; a pair is
// compared if either side keeps the other.
//
// Pipeline per phone (clock-corrected timestamps, ~10 Hz):
//  0. levelling: a phone that sends its gravity vector has every sample
//     split into vertical (along gravity) and a 2-D horizontal vector, so
//     it can be carried at any tilt (level.go). A phone that never sends
//     one is taken to be upright: x, z horizontal, y vertical.
//  1. handling filter: high rotation, or gravity swinging round in the
//     device frame → ignore readings until 1 s of quiet
//  2. band-pass the two horizontal components and the vertical (sway is
//     ~0.2–1 Hz)
//  3. project onto one horizontal axis: x (or z, or the dominant direction)
//     for an upright phone; always the dominant horizontal direction over
//     the correlation window for a levelled phone, whose own horizontal
//     axes point any way round
//  4. sway score = RMS over the last 5 s
//  5. walking gate (levelled phones): rhythmic vertical bounce together
//     with rhythmic horizontal motion is a phone in a trouser pocket
//     swinging with the leg, not sway
//
// Per neighbour pair: cross-correlate the last ~6 s; a strong correlation at a
// lag between ~100 ms and ~1.2 s is a wave travelling between them. Lag ≈ 0 is
// everyone moving together (dancing, jumping), which is not a wave.
//
// False-positive guards on those edges:
//   - vertical veto: if non-rhythmic vertical motion, stronger than the
//     horizontal, travels between the pair too, it is people standing up in
//     sequence (a stadium wave), not a push;
//   - chains: a crowd wave passes person to person to person, so a wave edge
//     only counts as part of a spatial run of ≥ MinChain phones whose hops
//     travel within ChainAngleDeg of each other (the other hops only need to
//     support it, at ChainCorr).
//
// Per zone: score = |Σ unit travel vectors of the wave edges| / number of
// edges touching the zone that could carry the wave (the net fraction of
// edges carrying a wave in one direction; see waveCapable), smoothed;
// yellow/red with hold time and hysteresis.
package detect

import (
	"math"
	"sort"

	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// HistoryMs is how much per-phone history is kept.
const HistoryMs = 30_000

// Sample is one 100 ms motion summary, with T already clock-corrected.
type Sample struct {
	T   int64
	AX  float64
	AY  float64
	AZ  float64
	Rot float64
	// G is the gravity direction in the device frame (see Gravity). The
	// zero value means "not sent with this sample": the phone's last value
	// stays in force, and a phone that never sent one is taken as upright.
	G [3]float64
}

type point struct {
	t      int64
	hx, hz float64
	hy     float64 // band-passed vertical, for the vertical-motion veto
	valid  bool
}

type phone struct {
	id      string
	x, y    float64
	outside bool // outside the venue: counts toward nothing
	// acc is the accuracy radius of the position (m); 0 = the phone stands
	// exactly there (placed by hand). See motion.go.
	acc  float64
	hash uint64 // of the id, for picking motion candidates

	lastT         int64 // corrected time of the newest sample
	handlingUntil int64
	handlingFrom  int64 // when the current handling episode began
	handlingLong  bool  // … and whether it has outlasted HandlingShortMs (filters restart)

	init               bool
	lastValidT         int64
	lpx, lpz, dcx, dcz float64
	lpy, dcy           float64

	lev level // gravity and the levelled frame, for a phone that sends g

	pts []point

	// per-step scratch
	h      []float64
	v      []float64 // band-passed vertical on the same grid
	sx, sz []float64 // the two horizontal components before projection
	valid  []bool
	sway   float64
	wave   bool
	// walking: a levelled phone whose vertical and horizontal motion are
	// both rhythmic (leg swing in a pocket); it joins no pair.
	walking   bool
	walkUntil int64
	// Motion pairs (motion.go): the step in which the phone was last
	// eligible and its index among the eligible then; its motion neighbours.
	eligAt  uint64
	eligIdx int
	stuck   []stuckWith
	// rhythm of the vertical trace, worked out once per step when needed.
	vRhythm   float64
	vRhythmAt uint64
	// blind: being handled, or too few valid readings to say whether it
	// sways. A pair with a blind phone is evidence neither way.
	blind bool
	zones []int
	// RMS of the horizontal and vertical band-passed motion over the whole
	// correlation window.
	hrms, vrms float64
}

type zone struct {
	def ZoneDef

	score     float64
	raw       float64
	state     LevelState
	direction string
	lagMs     int64
}

// PhoneResult is the per-phone output of a step.
type PhoneResult struct {
	ID      string
	X, Y    float64
	Acc     float64 // accuracy radius of the position (m); 0 = exact
	Outside bool
	Status  string
	Sway    float64
	LastT   int64
}

// Edge is a neighbour pair. From is the phone with the smaller x (then y,
// then id). LagMs > 0 means To moves after From, i.e. the motion travels
// From → To. Only edges with Wave set are travelling waves.
type Edge struct {
	From  string
	To    string
	LagMs int64
	Corr  float64
	Wave  bool
	// Motion: the pair was found by motion, not by distance: at least one of
	// the two positions is only roughly known (motion.go). Such pairs are
	// listed only when they look like a wave hop (|corr| ≥ ChainCorr at a
	// wave-like lag, not vertical).
	Motion bool
}

// ZoneResult is the per-zone output of a step.
type ZoneResult struct {
	ID        string
	Name      string
	Poly      [][2]float64
	Custom    bool
	Sens      string
	Level     string
	Score     float64 // smoothed
	Raw       float64 // this step
	Direction string  // dominant wave direction: +x, -x, +y, -y or ""
	LagMs     int64   // mean |lag| of the wave edges (how fast it travels)
	Since     int64   // when the zone entered its current level
}

// Change is a zone level transition.
type Change struct {
	T     int64
	Zone  string
	From  string
	To    string
	Score float64
}

// Result is everything one detector step produces.
type Result struct {
	T       int64
	Phones  []PhoneResult
	Edges   []Edge // all neighbour pairs that were compared
	Zones   []ZoneResult
	Changes []Change
}

// Waves returns only the travelling-wave edges.
func (r Result) Waves() []Edge {
	var out []Edge
	for _, e := range r.Edges {
		if e.Wave {
			out = append(out, e)
		}
	}
	return out
}

// Zone returns the result for one zone ID.
func (r Result) Zone(id string) (ZoneResult, bool) {
	for _, z := range r.Zones {
		if z.ID == id {
			return z, true
		}
	}
	return ZoneResult{}, false
}

// Detector is not safe for concurrent use; callers serialize access.
type Detector struct {
	cfg      Config
	phones   map[string]*phone
	zones    []*zone
	lastStep int64
	last     lastStep // inputs of the latest step, for Explain
	seq      uint64   // step counter
}

// New creates a detector with the default zones for cfg's venue.
func New(cfg Config) *Detector {
	d := &Detector{cfg: cfg, phones: map[string]*phone{}}
	d.SetZones(DefaultZones(cfg))
	return d
}

// Config returns the detector's configuration.
func (d *Detector) Config() Config { return d.cfg }

// SetVenue changes the venue size. Callers usually follow with SetZones.
func (d *Detector) SetVenue(w, h float64) {
	d.cfg.VenueW, d.cfg.VenueH = w, h
}

// SetZones replaces the zones. A zone that keeps its ID keeps its score
// and level.
func (d *Detector) SetZones(defs []ZoneDef) {
	old := map[string]*zone{}
	for _, z := range d.zones {
		old[z.def.ID] = z
	}
	d.zones = nil
	for _, def := range defs {
		if def.Sens == "" {
			def.Sens = SensNormal
		}
		z := old[def.ID]
		if z == nil {
			z = &zone{state: NewLevelState()}
		}
		z.def = def
		d.zones = append(d.zones, z)
	}
}

// Zones returns the current zone definitions.
func (d *Detector) Zones() []ZoneDef {
	out := make([]ZoneDef, len(d.zones))
	for i, z := range d.zones {
		out[i] = z.def
	}
	return out
}

// zoneIdxs lists the zones containing a point: every polygon zone that
// contains it, else the rest zone(s).
func (d *Detector) zoneIdxs(x, y float64) []int {
	var out []int
	for i, z := range d.zones {
		if !z.def.Rest && z.contains(x, y, d.cfg.VenueW, d.cfg.VenueH) {
			out = append(out, i)
		}
	}
	if len(out) == 0 {
		for i, z := range d.zones {
			if z.def.Rest {
				out = append(out, i)
			}
		}
	}
	return out
}

// ZonesOf lists the IDs of every zone containing (x, y).
func (d *Detector) ZonesOf(x, y float64) []string {
	var out []string
	for _, i := range d.zoneIdxs(x, y) {
		out = append(out, d.zones[i].def.ID)
	}
	return out
}

// ZoneOf is the first zone containing (x, y), "" if none.
func (d *Detector) ZoneOf(x, y float64) string {
	if z := d.zoneIdxs(x, y); len(z) > 0 {
		return d.zones[z[0]].def.ID
	}
	return ""
}

// SetPhone adds a phone or moves it.
func (d *Detector) SetPhone(id string, x, y float64) {
	p, ok := d.phones[id]
	if !ok {
		p = &phone{id: id, hash: hashID(id)}
		d.phones[id] = p
	}
	p.x, p.y = x, y
}

// SetAccuracy says how well a phone's position is known: the accuracy
// radius of its GPS fix in metres (the 68 % radius phones report), 0 for a
// phone placed by hand. Unknown phones are ignored.
func (d *Detector) SetAccuracy(id string, acc float64) {
	if p, ok := d.phones[id]; ok {
		if !(acc > 0) || math.IsInf(acc, 0) { // also NaN
			acc = 0
		}
		p.acc = acc
	}
}

// SetOutside marks a phone as outside the venue (its GPS fix put it there):
// it keeps its status but joins no zone or neighbour pair.
func (d *Detector) SetOutside(id string, outside bool) {
	if p, ok := d.phones[id]; ok {
		p.outside = outside
	}
}

// RemovePhone forgets a phone.
func (d *Detector) RemovePhone(id string) { delete(d.phones, id) }

// Add feeds one clock-corrected sample. Unknown phones are ignored.
func (d *Detector) Add(id string, s Sample) {
	p, ok := d.phones[id]
	if !ok || s.T <= p.lastT {
		return // unknown phone, duplicate or out of order
	}
	p.lastT = s.T
	cfg := &d.cfg

	// Gravity swinging round in the device frame: the phone was turned over
	// or pulled out of a pocket.
	g, gOK := unit(s.G)
	turned := gOK && p.lev.set(g, s.T, cfg)
	if s.Rot > cfg.HandlingRot || turned {
		if s.T >= p.handlingUntil { // a new episode
			p.handlingFrom, p.handlingLong = s.T, false
		}
		// A short burst of rotation (a gesture with the phone in the hand)
		// masks its own readings and little more; once it has gone on for
		// HandlingShortMs, or the phone was turned over, it is handling
		// proper: the full quiet time, and the filters start again.
		if turned || cfg.HandlingShortMs <= 0 || s.T-p.handlingFrom >= cfg.HandlingShortMs {
			p.handlingLong = true
		}
		settle := cfg.HandlingSettleMs
		if !p.handlingLong && cfg.HandlingShortSettleMs < settle {
			settle = cfg.HandlingShortSettleMs
		}
		if u := s.T + settle; u > p.handlingUntil {
			p.handlingUntil = u
		}
	}
	ax, ay, az := s.AX, s.AY, s.AZ
	if p.lev.on {
		ax, ay, az = p.lev.split(ax, ay, az)
	}
	if s.T < p.handlingUntil {
		if p.handlingLong {
			p.init = false // restart the filters once the phone settles
		} // else: the filters hold their state across the gap
		p.append(point{t: s.T})
		return
	}

	if !p.init {
		p.lpx, p.dcx = ax, ax
		p.lpz, p.dcz = az, az
		p.lpy, p.dcy = ay, ay
		p.lastValidT = s.T
		p.init = true
	}
	dt := float64(s.T-p.lastValidT) / 1000
	dt = math.Max(0.01, math.Min(dt, 1))
	p.lastValidT = s.T
	aLP := 1 - math.Exp(-dt*2*math.Pi*cfg.LowPassHz)
	aHP := 1 - math.Exp(-dt*2*math.Pi*cfg.HighPassHz)
	p.lpx += aLP * (ax - p.lpx)
	p.lpz += aLP * (az - p.lpz)
	p.lpy += aLP * (ay - p.lpy)
	p.dcx += aHP * (p.lpx - p.dcx)
	p.dcz += aHP * (p.lpz - p.dcz)
	p.dcy += aHP * (p.lpy - p.dcy)
	p.append(point{t: s.T, hx: p.lpx - p.dcx, hz: p.lpz - p.dcz, hy: p.lpy - p.dcy, valid: true})
}

func (p *phone) append(pt point) {
	p.pts = append(p.pts, pt)
	cut := 0
	for cut < len(p.pts) && p.pts[cut].t < pt.t-HistoryMs {
		cut++
	}
	if cut > len(p.pts)/2 { // compact occasionally rather than every sample
		p.pts = append(p.pts[:0], p.pts[cut:]...)
	}
}

// resample fills h/valid on a regular grid start, start+step, … (n points)
// by linear interpolation between valid samples no more than gapMs apart.
func (p *phone) resample(start, step int64, n int, axis string) {
	const gapMs = 400
	p.h = growF(p.h, n)
	p.v = growF(p.v, n)
	p.valid = growB(p.valid, n)
	p.sx, p.sz = growF(p.sx, n), growF(p.sz, n)
	hx, hz := p.sx, p.sz
	if p.lev.on {
		// A levelled phone's horizontal axes point any way round: always
		// use its dominant horizontal direction.
		axis = "xz"
	}
	j := sort.Search(len(p.pts), func(i int) bool { return p.pts[i].t >= start })
	for i := 0; i < n; i++ {
		t := start + int64(i)*step
		for j < len(p.pts) && p.pts[j].t < t {
			j++
		}
		p.valid[i] = false
		hx[i], hz[i] = 0, 0
		switch {
		case j < len(p.pts) && p.pts[j].t == t && p.pts[j].valid:
			hx[i], hz[i], p.v[i], p.valid[i] = p.pts[j].hx, p.pts[j].hz, p.pts[j].hy, true
		case j > 0 && j < len(p.pts):
			a, b := p.pts[j-1], p.pts[j]
			if a.valid && b.valid && b.t-a.t <= gapMs {
				f := float64(t-a.t) / float64(b.t-a.t)
				hx[i] = a.hx + f*(b.hx-a.hx)
				hz[i] = a.hz + f*(b.hz-a.hz)
				p.v[i] = a.hy + f*(b.hy-a.hy)
				p.valid[i] = true
			}
		}
	}
	switch axis {
	case "z":
		copy(p.h, hz)
	case "xz":
		// Dominant horizontal direction: principal axis of (x, z).
		var cxx, czz, cxz float64
		if p.lev.on {
			cxx, czz, cxz = slowCov(hx, hz, int(swayAxisMs/step))
		} else {
			for i := range hx {
				if p.valid[i] {
					cxx += hx[i] * hx[i]
					czz += hz[i] * hz[i]
					cxz += hx[i] * hz[i]
				}
			}
		}
		th := 0.5 * math.Atan2(2*cxz, cxx-czz)
		c, s := math.Cos(th), math.Sin(th)
		for i := range hx {
			p.h[i] = c*hx[i] + s*hz[i]
		}
	default:
		copy(p.h, hx)
	}
	for i := range p.h {
		if !p.valid[i] {
			p.h[i] = 0
			p.v[i] = 0
		}
	}
}

// swayAxisMs is the moving average a levelled phone's horizontal vector is
// smoothed with before its dominant direction is taken. Half a second
// cancels a 2 Hz step or jump beat (and its harmonics) and keeps 85 % of a
// 0.6 Hz push, so the direction is that of the slow sway, not of whatever
// the bouncing leaks into the horizontal plane. Only the direction is taken
// from the smoothed vector; the trace projected onto it is not smoothed.
const swayAxisMs = 500

// slowCov is the covariance (sums) of the horizontal vector (x, z) after a
// moving average over w grid points. Invalid points are zero in x and z.
func slowCov(x, z []float64, w int) (cxx, czz, cxz float64) {
	if w < 1 {
		w = 1
	}
	var sx, sz float64
	for i := range x {
		sx += x[i]
		sz += z[i]
		if i >= w {
			sx -= x[i-w]
			sz -= z[i-w]
		}
		cxx += sx * sx
		czz += sz * sz
		cxz += sx * sz
	}
	return
}

// Step runs one detection pass at server time now (ms).
func (d *Detector) Step(now int64) Result {
	cfg := &d.cfg
	end := now - cfg.LatencyMs
	n := int(cfg.CorrWindowMs/cfg.StepMs) + 1
	start := end - int64(n-1)*cfg.StepMs
	swayN := int(cfg.SwayWindowMs / cfg.StepMs)
	if swayN > n {
		swayN = n
	}

	ids := make([]string, 0, len(d.phones))
	for id := range d.phones {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	active := map[string]bool{}
	var spatial []*phone // active and inside the venue
	for _, id := range ids {
		p := d.phones[id]
		p.wave = false
		p.walking = false
		p.sway = 0
		p.zones = nil
		if now-p.lastT > cfg.StaleMs {
			continue
		}
		active[id] = true
		p.resample(start, cfg.StepMs, n, cfg.Axis)
		var ss float64
		var cnt int
		for i := n - swayN; i < n; i++ {
			if p.valid[i] {
				ss += p.h[i] * p.h[i]
				cnt++
			}
		}
		if cnt >= swayN/2 {
			p.sway = math.Sqrt(ss / float64(cnt))
		}
		p.blind = cnt < swayN/2 || p.lastT < p.handlingUntil
		p.hrms, p.vrms = rms(p.h, p.valid), rms(p.v, p.valid)
		p.walking = d.walking(p, int(cfg.MaxLagMs/cfg.StepMs))
		if !p.outside {
			p.zones = d.zoneIdxs(p.x, p.y)
			spatial = append(spatial, p)
		}
	}

	type tally struct {
		edges  []int     // every edge touching the zone
		speeds []float64 // m/ms along the travel direction, per wave edge
		waves  int
		vx, vy float64
		lagSum int64
		// Phones in the zone by how well their position is known, and the
		// roughly placed ones that are part of a wave found by motion.
		exact, rough, roughWave int
		mwaves                  int     // wave edges found by motion
		mvx, mvy                float64 // their travel vectors, from the rough positions
	}
	tallies := make([]tally, len(d.zones))

	maxLag := int(cfg.MaxLagMs / cfg.StepMs)
	minOverlap := n - maxLag
	if cfg.MinOverlap > 0 && cfg.MinOverlap < 1 {
		minOverlap = int(math.Ceil(float64(minOverlap) * cfg.MinOverlap))
	}
	// Phones that stand exactly where they say are paired by distance. A
	// phone whose position is only roughly known finds its neighbours by
	// motion (motion.go).
	exact, rough := spatial, []*phone(nil)
	if cfg.AccPairScale > 0 {
		for i, p := range spatial {
			if p.acc > 0 {
				exact = append([]*phone(nil), spatial[:i]...)
				for _, q := range spatial[i:] {
					if q.acc > 0 {
						rough = append(rough, q)
					} else {
						exact = append(exact, q)
					}
				}
				break
			}
		}
	}
	d.seq++
	pairs := d.neighbourPairs(exact)
	nExact := len(pairs)
	if len(rough) > 0 {
		pairs = append(pairs, d.motionPairs(spatial)...)
	}
	edges := make([]Edge, 0, len(pairs))
	support := make([]bool, 0, len(pairs)) // hop is wave-like enough to extend a chain
	recs := make([]pairRec, 0, len(pairs))
	// unseen: the pair could not be measured in this step (a phone blind, or
	// too little overlap to correlate): it says nothing about a wave.
	unseen := make([]bool, 0, len(pairs))
	for pi, pr := range pairs {
		a, b := pr[0], pr[1]
		e := Edge{From: a.id, To: b.id, Motion: pi >= nExact}
		sup := false
		dark := a.blind || b.blind
		canCorr := a.lastT >= a.handlingUntil && b.lastT >= b.handlingUntil &&
			a.sway >= cfg.EdgeMinSway && b.sway >= cfg.EdgeMinSway &&
			!a.walking && !b.walking
		if canCorr {
			// |corr|: a phone held upside down, or iOS vs Android sign conventions,
			// flips the axis but not the timing.
			cs := corrCurve(a.h, b.h, a.valid, b.valid, maxLag, minOverlap, true)
			lag, corr, second, ok := peaks(cs, maxLag)
			dark = dark || !ok
			if ok && e.Motion {
				// Found by motion: the lag is all there is to say the motion
				// travels, so it must be resolved (resolvedSecond).
				second = math.Max(second, resolvedSecond(cs, lag, maxLag))
			}
			if ok {
				e.LagMs = int64(math.Round(lag * float64(cfg.StepMs)))
				e.Corr = corr
				al := abs64(e.LagMs)
				waveLag := al >= cfg.MinWaveLagMs && al <= cfg.MaxWaveLagMs
				// A periodic motion (walking cadence) has several equally good
				// lags; only a clear single peak says which way it travels.
				unambiguous := corr-second >= cfg.PeakMargin
				e.Wave = corr >= cfg.CorrThreshold && waveLag && unambiguous
				sup = cfg.ChainCorr > 0 && corr >= cfg.ChainCorr && waveLag && (!e.Motion || unambiguous)
				if (e.Wave || sup) && d.vertical(a, b, lag, maxLag, minOverlap) {
					e.Wave, sup = false, false
				}
			}
		}
		if e.Motion {
			if !e.Wave && !sup {
				continue // candidates that don't move like a wave hop aren't neighbours
			}
			d.stick(a, b)
		}
		edges = append(edges, e)
		unseen = append(unseen, dark)
		support = append(support, sup)
		recs = append(recs, pairRec{motion: e.Motion, a: a, b: b, handA: a.lastT < a.handlingUntil, handB: b.lastT < b.handlingUntil,
			swayA: a.sway, swayB: b.sway, walkA: a.walking, walkB: b.walking, preChain: e.Wave})
	}
	d.keepChains(edges, support)
	d.last = lastStep{pairs: recs, edges: edges, n: n, maxLag: maxLag, minOverlap: minOverlap}

	for i, e := range edges {
		a, b := d.phones[e.From], d.phones[e.To]
		var tx, ty, speed float64
		if e.Wave {
			a.wave, b.wave = true, true
			tx, ty, _ = travel(a, b, e.LagMs)
			speed = math.Hypot(b.x-a.x, b.y-a.y) / float64(max(1, abs64(e.LagMs)))
		}
		for _, zi := range unionIdx(a.zones, b.zones) {
			t := &tallies[zi]
			if e.Motion {
				if e.Wave {
					t.mwaves++
					t.mvx += tx
					t.mvy += ty
				}
				continue
			}
			t.edges = append(t.edges, i)
			if e.Wave {
				t.waves++
				t.vx += tx
				t.vy += ty
				t.lagSum += abs64(e.LagMs)
				t.speeds = append(t.speeds, speed)
			}
		}
	}

	if len(rough) > 0 {
		for _, p := range exact {
			for _, zi := range p.zones {
				tallies[zi].exact++
			}
		}
		for _, p := range rough {
			if p.blind {
				continue // can't be measured: counts neither way
			}
			for _, zi := range p.zones {
				tallies[zi].rough++
				if p.wave {
					tallies[zi].roughWave++
				}
			}
		}
	}

	res := Result{T: now, Edges: edges}
	for _, id := range ids {
		p := d.phones[id]
		st := protocol.StatusOK
		switch {
		case !active[id]:
			st = protocol.StatusStale
		case p.lastT < p.handlingUntil:
			st = protocol.StatusHandling
		case p.wave:
			st = protocol.StatusWave
		case p.sway > cfg.SwayThreshold && !p.walking:
			st = protocol.StatusSwaying
		}
		res.Phones = append(res.Phones, PhoneResult{ID: id, X: p.x, Y: p.y, Acc: p.acc, Outside: p.outside, Status: st, Sway: p.sway, LastT: p.lastT})
	}

	dt := now - d.lastStep
	if d.lastStep == 0 || dt <= 0 || dt > 5000 {
		dt = 250
	}
	d.lastStep = now
	alpha := 1 - math.Exp(-float64(dt)/float64(cfg.ZoneSmoothMs))
	for i, z := range d.zones {
		t := tallies[i]
		raw := 0.0
		if z.def.NoPush {
			t = tally{}
		} else {
			if net := math.Hypot(t.vx, t.vy); net > 0 {
				raw = net / float64(d.waveCapable(edges, unseen, t.edges, t.vx/net, t.vy/net, median(t.speeds)))
			}
			if t.rough > 0 {
				// Roughly placed phones: the share of them that are part of
				// a wave found by motion (roughScore), weighed against the
				// exactly placed phones' edge score by head count.
				raw = (raw*float64(t.exact) + roughScore(t.roughWave, t.rough)*float64(t.rough)) / float64(t.exact+t.rough)
			}
		}
		z.raw = raw
		z.score += alpha * (raw - z.score)
		if z.def.NoPush {
			z.score = 0
		}
		if t.waves > 0 {
			z.direction = dirName(t.vx, t.vy)
			z.lagMs = t.lagSum / int64(t.waves)
		} else if t.mwaves > 0 && math.Hypot(t.mvx, t.mvy) >= roughDirAgree*float64(t.mwaves) {
			// A wave found by motion: its direction comes from the rough
			// positions, and is named only when the edges agree on it. The
			// lag between two such phones is not a per-person lag.
			z.direction, z.lagMs = dirName(t.mvx, t.mvy), 0
		}
		if from, to, ok := z.state.Update(now, z.score, zoneThresholds(cfg, z.def.Sens)); ok {
			if to == protocol.LevelCalm {
				z.direction, z.lagMs = "", 0
			}
			res.Changes = append(res.Changes, Change{T: now, Zone: z.def.ID, From: from, To: to, Score: z.score})
		}
		res.Zones = append(res.Zones, ZoneResult{
			ID: z.def.ID, Name: z.def.Name, Poly: z.def.Poly, Custom: z.def.Custom, Sens: z.def.Sens,
			Level: z.state.Level, Score: z.score, Raw: z.raw,
			Direction: z.direction, LagMs: z.lagMs, Since: z.state.Since,
		})
	}
	return res
}

// waveCapable counts the edges of a zone that could show a wave-like lag
// at all: the wave edges themselves, plus every other edge whose length
// along the wave's direction (ux, uy) is at least speed × MinWaveLagMs
// (speed = median of the wave edges' length / lag). On
// a line every hop qualifies; in a crowd, two people standing side by side
// across the direction of travel are hit at almost the same moment, so
// their near-zero lag is consistent with the wave rather than evidence
// against it, and they are left out. So are pairs that couldn't be measured
// in this step (unseen: a phone being handled, or too many readings missing
// to correlate).
func (d *Detector) waveCapable(edges []Edge, unseen []bool, idx []int, ux, uy, speed float64) int {
	minProj := speed * float64(d.cfg.MinWaveLagMs)
	n := 0
	for _, i := range idx {
		e := edges[i]
		if unseen[i] && !e.Wave {
			continue // couldn't be measured: counts neither way
		}
		a, b := d.phones[e.From], d.phones[e.To]
		if e.Wave || math.Abs((b.x-a.x)*ux+(b.y-a.y)*uy) >= minProj {
			n++
		}
	}
	return max(n, 1)
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[len(s)/2]
}

// before orders phones for edge direction: smaller x, then y, then id.
func before(a, b *phone) bool {
	if a.x != b.x {
		return a.x < b.x
	}
	if a.y != b.y {
		return a.y < b.y
	}
	return a.id < b.id
}

// neighbourPairs picks the pairs to compare: each phone keeps its
// MaxNeighbours nearest within NeighbourRadius, and a pair counts if either
// side keeps the other. Pairs come back ordered (From before To) and sorted,
// so results are deterministic. ps must be sorted by id.
func (d *Detector) neighbourPairs(ps []*phone) [][2]*phone {
	r2 := d.cfg.NeighbourRadius * d.cfg.NeighbourRadius
	type cand struct {
		j  int
		d2 float64
	}
	keep := map[[2]int]bool{}
	var cs []cand
	for i, a := range ps {
		cs = cs[:0]
		for j, b := range ps {
			if i == j {
				continue
			}
			dx, dy := a.x-b.x, a.y-b.y
			if d2 := dx*dx + dy*dy; d2 <= r2 {
				cs = append(cs, cand{j, d2})
			}
		}
		sort.Slice(cs, func(x, y int) bool {
			if cs[x].d2 != cs[y].d2 {
				return cs[x].d2 < cs[y].d2
			}
			return cs[x].j < cs[y].j
		})
		for k := 0; k < len(cs) && k < d.cfg.MaxNeighbours; k++ {
			lo, hi := i, cs[k].j
			if lo > hi {
				lo, hi = hi, lo
			}
			keep[[2]int{lo, hi}] = true
		}
	}
	out := make([][2]*phone, 0, len(keep))
	for k := range keep {
		a, b := ps[k[0]], ps[k[1]]
		if before(b, a) {
			a, b = b, a
		}
		out = append(out, [2]*phone{a, b})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i][0] != out[j][0] {
			return before(out[i][0], out[j][0])
		}
		return before(out[i][1], out[j][1])
	})
	return out
}

// travel is the unit vector of an edge in its direction of travel (a → b
// when lag > 0). ok is false when the phones sit on the same spot.
func travel(a, b *phone, lag int64) (ux, uy float64, ok bool) {
	dx, dy := b.x-a.x, b.y-a.y
	l := math.Hypot(dx, dy)
	if l < 1e-9 {
		return 0, 0, false
	}
	if lag < 0 {
		dx, dy = -dx, -dy
	}
	return dx / l, dy / l, true
}

func unionIdx(a, b []int) []int {
	out := append([]int(nil), a...)
	for _, v := range b {
		dup := false
		for _, w := range a {
			dup = dup || v == w
		}
		if !dup {
			out = append(out, v)
		}
	}
	return out
}

// keepChains clears Wave on edges that are not part of a run of at least
// MinChain phones travelling the same way. A crowd wave passes from person
// to person to person; two neighbours bumping into each other only make an
// isolated edge.
//
// Each wave or support edge is oriented in its direction of travel; a run
// continues from a→b to b→c when the two hops' travel directions are within
// ChainAngleDeg of each other (on a line: the same way along the row). Like
// edge linking with hysteresis, the other hops of a run only need to
// support the wave (support[i]: |corr| ≥ ChainCorr at a wave-like lag), not
// pass every test themselves, so one noisy hop doesn't break a real wave.
func (d *Detector) keepChains(edges []Edge, support []bool) {
	cfg := &d.cfg
	if cfg.MinChain <= 2 {
		return
	}
	type hop struct {
		tail, head string
		ux, uy     float64
		lag        float64 // grid steps, > 0
		motion     bool
	}
	hops := make([]hop, len(edges))
	use := make([]bool, len(edges))
	in, out := map[string][]int{}, map[string][]int{}
	for i, e := range edges {
		if !(e.Wave || support[i]) || e.LagMs == 0 {
			continue
		}
		ux, uy, ok := travel(d.phones[e.From], d.phones[e.To], e.LagMs)
		if !ok && !e.Motion {
			continue
		}
		h := hop{e.From, e.To, ux, uy, float64(abs64(e.LagMs)) / float64(cfg.StepMs), e.Motion}
		if e.LagMs < 0 {
			h.tail, h.head = e.To, e.From
		}
		hops[i], use[i] = h, true
		out[h.tail] = append(out[h.tail], i)
		in[h.head] = append(in[h.head], i)
	}
	cosMax := math.Cos(cfg.ChainAngleDeg * math.Pi / 180)
	// aligned: hop j may follow hop i (fwd) or lead into it (!fwd). Between
	// exactly placed phones: the two hops travel the same way on the map. As
	// soon as a position is only roughly known the map says nothing, and the
	// test is in time instead: the phone before the first hop and the phone
	// after the second must match at the sum of the two lags (closure).
	aligned := func(i, j int, fwd bool) bool {
		if !hops[i].motion && !hops[j].motion {
			return hops[i].ux*hops[j].ux+hops[i].uy*hops[j].uy >= cosMax-1e-9
		}
		first, second := hops[i], hops[j]
		if !fwd {
			first, second = second, first
		}
		return d.closure(d.phones[first.tail], d.phones[second.head], first.lag+second.lag)
	}
	limit := cfg.MinChain
	// run is the longest aligned run of hops starting (fwd) or ending (back)
	// with hop i, counted in hops (depth so far included) and capped at
	// limit. Phones already on the run are never revisited, so cycles end.
	var run func(i int, fwd bool, visited map[string]bool, depth int) int
	run = func(i int, fwd bool, visited map[string]bool, depth int) int {
		if depth >= limit {
			return depth
		}
		next, nb := hops[i].head, out
		if !fwd {
			next, nb = hops[i].tail, in
		}
		best := depth
		for _, j := range nb[next] {
			far := hops[j].head
			if !fwd {
				far = hops[j].tail
			}
			if visited[far] || !aligned(i, j, fwd) {
				continue
			}
			visited[far] = true
			best = max(best, run(j, fwd, visited, depth+1))
			delete(visited, far)
			if best >= limit {
				break
			}
		}
		return best
	}
	var drop []int
	for i, e := range edges {
		if !e.Wave {
			continue
		}
		if !use[i] {
			drop = append(drop, i)
			continue
		}
		visited := map[string]bool{hops[i].tail: true, hops[i].head: true}
		back := run(i, false, visited, 1)
		fwd := run(i, true, visited, 1)
		if back+fwd < cfg.MinChain { // (back+fwd-1) hops = back+fwd phones
			drop = append(drop, i)
		}
	}
	for _, i := range drop {
		edges[i].Wave = false
	}
}

// xcorr finds the lag (in grid steps, sub-step refined) at which b best
// matches a: b[i+lag] ≈ a[i]. Positive lag means b moves after a.
// Correlation is Pearson over the overlapping valid samples. second is the
// height of the next-best separate peak (or range edge), for ambiguity checks.
func xcorr(a, b []float64, va, vb []bool, maxLag, minOverlap int, useAbs bool) (lag, corr, second float64, ok bool) {
	return peaks(corrCurve(a, b, va, vb, maxLag, minOverlap, useAbs), maxLag)
}

// corrCurve is the Pearson correlation of b[i+k] against a[i] for every lag
// k in -maxLag..maxLag (index k+maxLag); NaN where fewer than minOverlap
// samples overlap or a side is flat.
func corrCurve(a, b []float64, va, vb []bool, maxLag, minOverlap int, useAbs bool) []float64 {
	n := len(a)
	cs := make([]float64, 2*maxLag+1)
	for k := -maxLag; k <= maxLag; k++ {
		var sa, sb, saa, sbb, sab float64
		var m int
		for i := 0; i < n; i++ {
			j := i + k
			if j < 0 || j >= n || !va[i] || !vb[j] {
				continue
			}
			x, y := a[i], b[j]
			sa += x
			sb += y
			saa += x * x
			sbb += y * y
			sab += x * y
			m++
		}
		c := math.NaN()
		if m >= minOverlap {
			fm := float64(m)
			cov := sab - sa*sb/fm
			den := math.Sqrt((saa - sa*sa/fm) * (sbb - sb*sb/fm))
			if den > 1e-12 {
				c = cov / den
				if useAbs {
					c = math.Abs(c)
				}
			}
		}
		cs[k+maxLag] = c
	}
	return cs
}

// peaks finds the best lag of a correlation curve (sub-step refined), its
// height and the height of the next-best separate peak.
func peaks(cs []float64, maxLag int) (lag, corr, second float64, ok bool) {
	best := -1
	for i, c := range cs {
		if !math.IsNaN(c) && (best < 0 || c > cs[best]) {
			best = i
		}
	}
	if best < 0 {
		return 0, 0, 0, false
	}
	lag = float64(best - maxLag)
	corr = cs[best]
	second = -1
	val := func(i int) float64 {
		if i < 0 || i >= len(cs) || math.IsNaN(cs[i]) {
			return math.Inf(-1)
		}
		return cs[i]
	}
	for i := range cs {
		// Local maxima away from the best peak. Range edges count too: a
		// value still rising at the edge means a peak just outside it.
		if abs(i-best) < sepSteps(maxLag) || math.IsNaN(cs[i]) {
			continue
		}
		if cs[i] >= val(i-1) && cs[i] >= val(i+1) && cs[i] > second {
			second = cs[i]
		}
	}
	if best > 0 && best < len(cs)-1 && !math.IsNaN(cs[best-1]) && !math.IsNaN(cs[best+1]) {
		ym, y0, yp := cs[best-1], cs[best], cs[best+1]
		if den := ym - 2*y0 + yp; den < 0 {
			lag += 0.5 * (ym - yp) / den
		}
	}
	return lag, corr, second, true
}

// vertical reports whether a candidate wave between a and b is really
// vertical motion travelling down the line (a stadium "Mexican wave", people
// standing or jumping in sequence) leaking into the horizontal axis through
// phone tilt and leaning. It is vetoed when the vertical channel
//   - is stronger than the horizontal one (VerticalRatio),
//   - is not rhythmic on either phone (jumping or walking to a beat repeats
//     within a second; standing up and sitting down once does not), and
//   - travels between the pair by itself: |corr| ≥ CorrThreshold at a
//     wave-like lag in the same direction as the horizontal one.
//
// A genuine push is horizontal. A crowd jumping on the spot while a push
// travels through it is rhythmic, so it never vetoes the push. The vertical
// lag is not required to match the horizontal one exactly: tilt and leaning
// differ per person, which skews the horizontal lag estimate.
func (d *Detector) vertical(a, b *phone, lag float64, maxLag, minOverlap int) bool {
	cfg := &d.cfg
	if cfg.VerticalRatio <= 0 || a.vrms+b.vrms <= cfg.VerticalRatio*(a.hrms+b.hrms) {
		return false
	}
	vr := func(p *phone) float64 {
		if p.vRhythmAt != d.seq {
			p.vRhythm, p.vRhythmAt = rhythm(p.v, p.valid, maxLag, minOverlap), d.seq
		}
		return p.vRhythm
	}
	if vr(a) >= cfg.CorrThreshold || vr(b) >= cfg.CorrThreshold {
		return false
	}
	vlag, corr, _, ok := xcorr(a.v, b.v, a.valid, b.valid, maxLag, minOverlap, true)
	vms := math.Abs(vlag) * float64(cfg.StepMs)
	return ok && corr >= cfg.CorrThreshold && (vlag < 0) == (lag < 0) &&
		vms >= float64(cfg.MinWaveLagMs) && vms <= float64(cfg.MaxWaveLagMs)
}

// resolvedSecond is the highest correlation at any lag at least sepSteps
// away from the best one (lag, in grid steps), local maximum or not. The
// usual ambiguity test only looks at separate peaks; a slow sway (a 5 s
// period, say) has none within the lag range, but its correlation is nearly
// as high 300 ms either side of the best lag as at it, so "which of the two
// moved first, and by how much" is not something the data says. Between
// exactly placed neighbours the map makes up for that (lag per metre,
// direction); for pairs found by motion the lag is the only evidence that
// anything travels, so the best lag must beat every lag ≥ sepSteps away by
// PeakMargin. A push (a jolt of a second or so) passes easily.
func resolvedSecond(cs []float64, lag float64, maxLag int) float64 {
	best := int(math.Round(lag)) + maxLag
	sep := sepSteps(maxLag)
	out := -1.0
	for i, c := range cs {
		if abs(i-best) >= sep && !math.IsNaN(c) && c > out {
			out = c
		}
	}
	return out
}

// rhythm measures how periodic x is: the highest autocorrelation at any lag
// up to maxLag after it first drops below zero. Near 1 for jumping or
// walking to a beat, low for a one-off movement.
func rhythm(x []float64, valid []bool, maxLag, minOverlap int) float64 {
	best, crossed := 0.0, false
	for k := 1; k <= maxLag; k++ {
		c := corrAt(x, x, valid, valid, k, minOverlap)
		if c < 0 {
			crossed = true
		}
		if crossed && c > best {
			best = c
		}
	}
	return best
}

// corrAt is the Pearson correlation of b[i+lag] against a[i].
func corrAt(a, b []float64, va, vb []bool, lag, minOverlap int) float64 {
	var sa, sb, saa, sbb, sab float64
	var m int
	for i := range a {
		j := i + lag
		if j < 0 || j >= len(b) || !va[i] || !vb[j] {
			continue
		}
		x, y := a[i], b[j]
		sa += x
		sb += y
		saa += x * x
		sbb += y * y
		sab += x * y
		m++
	}
	if m < minOverlap {
		return 0
	}
	fm := float64(m)
	den := math.Sqrt((saa - sa*sa/fm) * (sbb - sb*sb/fm))
	if den <= 1e-12 {
		return 0
	}
	return (sab - sa*sb/fm) / den
}

func rms(x []float64, valid []bool) float64 {
	var ss float64
	var n int
	for i, v := range x {
		if valid[i] {
			ss += v * v
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return math.Sqrt(ss / float64(n))
}

// sepSteps is how far (in steps) another peak must be to count as separate:
// a fifth of the lag range (300 ms at the defaults).
func sepSteps(maxLag int) int { return max(2, maxLag/5) }

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func growF(s []float64, n int) []float64 {
	if cap(s) < n {
		return make([]float64, n)
	}
	return s[:n]
}

func growB(s []bool, n int) []bool {
	if cap(s) < n {
		return make([]bool, n)
	}
	return s[:n]
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
