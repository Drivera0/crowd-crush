// Package detect turns per-phone motion into node statuses, travelling-wave
// edges between grid neighbours and zone alert levels.
//
// Math detects, AI explains: nothing in here calls out to a model.
//
// Pipeline per phone (clock-corrected timestamps, ~10 Hz):
//  1. handling filter: high rotation → ignore readings until 1 s of quiet
//  2. band-pass x and z (sway is ~0.2–1 Hz)
//  3. project onto the horizontal axis (x, z or the dominant direction)
//  4. sway score = RMS over the last 5 s
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
//     only counts as part of a run of ≥ MinChain phones in one direction
//     (the other hops only need to support it, at ChainCorr).
//
// Per zone: score = net fraction of edges carrying a wave in one direction,
// smoothed; yellow/red with hold time and hysteresis.
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
}

type point struct {
	t      int64
	hx, hz float64
	hy     float64 // band-passed vertical, for the vertical-motion veto
	valid  bool
}

type phone struct {
	id       string
	row, col int

	lastT         int64 // corrected time of the newest sample
	handlingUntil int64

	init               bool
	lastValidT         int64
	lpx, lpz, dcx, dcz float64
	lpy, dcy           float64

	pts []point

	// per-step scratch
	h     []float64
	v     []float64 // band-passed vertical on the same grid
	valid []bool
	sway  float64
	wave  bool
	// RMS of the horizontal and vertical band-passed motion over the whole
	// correlation window.
	hrms, vrms float64
}

type zone struct {
	id                     string
	idx                    int
	row0, col0, row1, col1 int

	score       float64
	raw         float64
	level       string
	yellowSince int64 // time score went above yellow (0 = not above)
	redSince    int64
	levelSince  int64
	direction   string
	lagMs       int64
}

// PhoneResult is the per-phone output of a step.
type PhoneResult struct {
	ID     string
	Row    int
	Col    int
	Status string
	Sway   float64
	LastT  int64
}

// Edge is a neighbour pair. LagMs > 0 means To moves after From, i.e. the
// motion travels From → To. Only edges with Wave set are travelling waves.
type Edge struct {
	From  string
	To    string
	LagMs int64
	Corr  float64
	Wave  bool
}

// ZoneResult is the per-zone output of a step.
type ZoneResult struct {
	ID         string
	Level      string
	Score      float64 // smoothed
	Raw        float64 // this step
	Row0, Col0 int
	Row1, Col1 int    // inclusive
	Direction  string // dominant wave direction: +col, -col, +row, -row or ""
	LagMs      int64  // mean |lag| of the wave edges (how fast it travels)
	Since      int64  // when the zone entered its current level
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
	Edges   []Edge // all neighbour pairs that could be correlated
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
}

// New creates a detector for the grid described in cfg.
func New(cfg Config) *Detector {
	d := &Detector{cfg: cfg, phones: map[string]*phone{}}
	nzr := (cfg.Rows + cfg.ZoneRows - 1) / cfg.ZoneRows
	nzc := (cfg.Cols + cfg.ZoneCols - 1) / cfg.ZoneCols
	for zr := 0; zr < nzr; zr++ {
		for zc := 0; zc < nzc; zc++ {
			i := zr*nzc + zc
			d.zones = append(d.zones, &zone{
				id:    ZoneName(i),
				idx:   i,
				row0:  zr * cfg.ZoneRows,
				col0:  zc * cfg.ZoneCols,
				row1:  min(cfg.Rows, (zr+1)*cfg.ZoneRows) - 1,
				col1:  min(cfg.Cols, (zc+1)*cfg.ZoneCols) - 1,
				level: protocol.LevelCalm,
			})
		}
	}
	return d
}

// Config returns the detector's configuration.
func (d *Detector) Config() Config { return d.cfg }

// ZoneName turns a zone index into a letter: A, B, … Z, AA, AB …
func ZoneName(i int) string {
	s := ""
	for {
		s = string(rune('A'+i%26)) + s
		i = i/26 - 1
		if i < 0 {
			return s
		}
	}
}

// ZoneOf returns the zone ID for a grid cell.
func (d *Detector) ZoneOf(row, col int) string {
	return d.zones[d.zoneIdx(row, col)].id
}

func (d *Detector) zoneIdx(row, col int) int {
	row = clamp(row, 0, d.cfg.Rows-1)
	col = clamp(col, 0, d.cfg.Cols-1)
	nzc := (d.cfg.Cols + d.cfg.ZoneCols - 1) / d.cfg.ZoneCols
	return (row/d.cfg.ZoneRows)*nzc + col/d.cfg.ZoneCols
}

// SetPhone adds a phone or moves it to a new grid cell.
func (d *Detector) SetPhone(id string, row, col int) {
	p, ok := d.phones[id]
	if !ok {
		p = &phone{id: id}
		d.phones[id] = p
	}
	p.row, p.col = row, col
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

	if s.Rot > cfg.HandlingRot {
		p.handlingUntil = s.T + cfg.HandlingSettleMs
	}
	if s.T < p.handlingUntil {
		p.init = false // restart the filters once the phone settles
		p.append(point{t: s.T})
		return
	}

	if !p.init {
		p.lpx, p.dcx = s.AX, s.AX
		p.lpz, p.dcz = s.AZ, s.AZ
		p.lpy, p.dcy = s.AY, s.AY
		p.lastValidT = s.T
		p.init = true
	}
	dt := float64(s.T-p.lastValidT) / 1000
	dt = math.Max(0.01, math.Min(dt, 1))
	p.lastValidT = s.T
	aLP := 1 - math.Exp(-dt*2*math.Pi*cfg.LowPassHz)
	aHP := 1 - math.Exp(-dt*2*math.Pi*cfg.HighPassHz)
	p.lpx += aLP * (s.AX - p.lpx)
	p.lpz += aLP * (s.AZ - p.lpz)
	p.lpy += aLP * (s.AY - p.lpy)
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
	hx := make([]float64, n)
	hz := make([]float64, n)
	j := sort.Search(len(p.pts), func(i int) bool { return p.pts[i].t >= start })
	for i := 0; i < n; i++ {
		t := start + int64(i)*step
		for j < len(p.pts) && p.pts[j].t < t {
			j++
		}
		p.valid[i] = false
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
		for i := range hx {
			if p.valid[i] {
				cxx += hx[i] * hx[i]
				czz += hz[i] * hz[i]
				cxz += hx[i] * hz[i]
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
	for _, id := range ids {
		p := d.phones[id]
		p.wave = false
		p.sway = 0
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
		p.hrms, p.vrms = rms(p.h, p.valid), rms(p.v, p.valid)
	}

	// Neighbour pairs: right and down, so each pair is visited once with
	// From = the phone nearer the grid origin.
	byCell := map[[2]int][]string{}
	for _, id := range ids {
		if active[id] {
			p := d.phones[id]
			byCell[[2]int{p.row, p.col}] = append(byCell[[2]int{p.row, p.col}], id)
		}
	}
	type tally struct {
		hTot, hNet, vTot, vNet, waves int
		lagSum                        int64
	}
	tallies := make([]tally, len(d.zones))

	maxLag := int(cfg.MaxLagMs / cfg.StepMs)
	minOverlap := n - maxLag
	var edges []Edge
	var axes []int     // 0 = along a row (+col), 1 = along a column (+row)
	var support []bool // hop is wave-like enough to extend a chain
	for _, id := range ids {
		if !active[id] {
			continue
		}
		a := d.phones[id]
		for ax, dir := range [][2]int{{0, 1}, {1, 0}} {
			for _, bid := range byCell[[2]int{a.row + dir[0], a.col + dir[1]}] {
				b := d.phones[bid]
				e := Edge{From: a.id, To: b.id}
				sup := false
				canCorr := a.lastT >= a.handlingUntil && b.lastT >= b.handlingUntil &&
					a.sway >= cfg.EdgeMinSway && b.sway >= cfg.EdgeMinSway
				if canCorr {
					// |corr|: a phone held upside down, or iOS vs Android sign conventions,
					// flips the axis but not the timing.
					lag, corr, second, ok := xcorr(a.h, b.h, a.valid, b.valid, maxLag, minOverlap, true)
					if ok {
						e.LagMs = int64(math.Round(lag * float64(cfg.StepMs)))
						e.Corr = corr
						al := abs64(e.LagMs)
						waveLag := al >= cfg.MinWaveLagMs && al <= cfg.MaxWaveLagMs
						// A periodic motion (walking cadence) has several equally good
						// lags; only a clear single peak says which way it travels.
						unambiguous := corr-second >= cfg.PeakMargin
						e.Wave = corr >= cfg.CorrThreshold && waveLag && unambiguous
						sup = cfg.ChainCorr > 0 && corr >= cfg.ChainCorr && waveLag
						if (e.Wave || sup) && d.vertical(a, b, lag, maxLag, minOverlap) {
							e.Wave, sup = false, false
						}
					}
				}
				edges = append(edges, e)
				axes = append(axes, ax)
				support = append(support, sup)
			}
		}
	}
	d.keepChains(edges, axes, support)

	for i, e := range edges {
		a, b := d.phones[e.From], d.phones[e.To]
		if e.Wave {
			a.wave, b.wave = true, true
		}
		sign := 0
		if e.Wave {
			sign = 1
			if e.LagMs < 0 {
				sign = -1
			}
		}
		za, zb := d.zoneIdx(a.row, a.col), d.zoneIdx(b.row, b.col)
		for _, zi := range uniq(za, zb) {
			t := &tallies[zi]
			if axes[i] == 0 {
				t.hTot++
				t.hNet += sign
			} else {
				t.vTot++
				t.vNet += sign
			}
			if e.Wave {
				t.waves++
				t.lagSum += abs64(e.LagMs)
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
		case p.sway > cfg.SwayThreshold:
			st = protocol.StatusSwaying
		}
		res.Phones = append(res.Phones, PhoneResult{ID: id, Row: p.row, Col: p.col, Status: st, Sway: p.sway, LastT: p.lastT})
	}

	dt := now - d.lastStep
	if d.lastStep == 0 || dt <= 0 || dt > 5000 {
		dt = 250
	}
	d.lastStep = now
	alpha := 1 - math.Exp(-float64(dt)/float64(cfg.ZoneSmoothMs))
	for i, z := range d.zones {
		t := tallies[i]
		raw, dirn := 0.0, ""
		if t.hTot > 0 {
			raw = math.Abs(float64(t.hNet)) / float64(t.hTot)
			dirn = dirName(t.hNet, "col")
		}
		if t.vTot > 0 {
			if v := math.Abs(float64(t.vNet)) / float64(t.vTot); v > raw {
				raw, dirn = v, dirName(t.vNet, "row")
			}
		}
		z.raw = raw
		z.score += alpha * (raw - z.score)
		if t.waves > 0 {
			z.direction = dirn
			z.lagMs = t.lagSum / int64(t.waves)
		}
		if ch, ok := z.update(now, cfg); ok {
			res.Changes = append(res.Changes, ch)
		}
		res.Zones = append(res.Zones, ZoneResult{
			ID: z.id, Level: z.level, Score: z.score, Raw: z.raw,
			Row0: z.row0, Col0: z.col0, Row1: z.row1, Col1: z.col1,
			Direction: z.direction, LagMs: z.lagMs, Since: z.levelSince,
		})
	}
	return res
}

// update runs the zone's level state machine.
func (z *zone) update(now int64, cfg *Config) (Change, bool) {
	since := func(p *int64, above bool) int64 {
		if !above {
			*p = 0
			return 0
		}
		if *p == 0 {
			*p = now
		}
		return now - *p
	}
	yHeld := since(&z.yellowSince, z.score > cfg.YellowScore)
	rHeld := since(&z.redSince, z.score > cfg.RedScore)

	from, to := z.level, z.level
	switch z.level {
	case protocol.LevelCalm:
		if z.yellowSince != 0 && yHeld >= cfg.HoldMs {
			to = protocol.LevelYellow
		}
	case protocol.LevelYellow:
		if z.redSince != 0 && rHeld >= cfg.HoldMs {
			to = protocol.LevelRed
		} else if z.score < cfg.YellowScore-cfg.Margin {
			to = protocol.LevelCalm
		}
	case protocol.LevelRed:
		if z.score < cfg.RedScore-cfg.Margin {
			to = protocol.LevelYellow
		}
	}
	if to == from {
		return Change{}, false
	}
	z.level, z.levelSince = to, now
	if to == protocol.LevelCalm {
		z.direction, z.lagMs = "", 0
	}
	return Change{T: now, Zone: z.id, From: from, To: to, Score: z.score}, true
}

// xcorr finds the lag (in grid steps, sub-step refined) at which b best
// matches a: b[i+lag] ≈ a[i]. Positive lag means b moves after a.
// Correlation is Pearson over the overlapping valid samples. second is the
// height of the next-best separate peak (or range edge), for ambiguity checks.
func xcorr(a, b []float64, va, vb []bool, maxLag, minOverlap int, useAbs bool) (lag, corr, second float64, ok bool) {
	n := len(a)
	cs := make([]float64, 2*maxLag+1)
	best := -1
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
		if !math.IsNaN(c) && (best < 0 || c > cs[best]) {
			best = k + maxLag
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
	if rhythm(a.v, a.valid, maxLag, minOverlap) >= cfg.CorrThreshold ||
		rhythm(b.v, b.valid, maxLag, minOverlap) >= cfg.CorrThreshold {
		return false
	}
	vlag, corr, _, ok := xcorr(a.v, b.v, a.valid, b.valid, maxLag, minOverlap, true)
	vms := math.Abs(vlag) * float64(cfg.StepMs)
	return ok && corr >= cfg.CorrThreshold && (vlag < 0) == (lag < 0) &&
		vms >= float64(cfg.MinWaveLagMs) && vms <= float64(cfg.MaxWaveLagMs)
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

// keepChains clears Wave on edges that are not part of a run of at least
// MinChain phones along one row or column, all travelling the same way. A
// crowd wave passes from person to person to person; two neighbours bumping
// into each other only make an isolated edge.
//
// Like edge linking with hysteresis, the other hops of a run only need to
// support the wave (support[i]: |corr| ≥ ChainCorr at a wave-like lag),
// not pass every test themselves, so one noisy hop doesn't break a real
// wave. axes[i] is 0 for an edge along a row, 1 along a column.
func (d *Detector) keepChains(edges []Edge, axes []int, support []bool) {
	cfg := &d.cfg
	if cfg.MinChain <= 2 {
		return
	}
	type key struct {
		id   string
		axis int
	}
	in, out := map[key][]int{}, map[key][]int{}
	for i, e := range edges {
		if e.Wave || support[i] {
			in[key{e.To, axes[i]}] = append(in[key{e.To, axes[i]}], i)
			out[key{e.From, axes[i]}] = append(out[key{e.From, axes[i]}], i)
		}
	}
	consistent := func(i, j int) bool {
		return (edges[i].LagMs < 0) == (edges[j].LagMs < 0)
	}
	// Longest consistent run of edges ending at / starting from edge i.
	// Edges only point right or down, so the recursion always terminates.
	back, fwd := make([]int, len(edges)), make([]int, len(edges))
	var runBack, runFwd func(i int) int
	runBack = func(i int) int {
		if back[i] == 0 {
			best := 0
			for _, j := range in[key{edges[i].From, axes[i]}] {
				if consistent(i, j) {
					best = max(best, runBack(j))
				}
			}
			back[i] = best + 1
		}
		return back[i]
	}
	runFwd = func(i int) int {
		if fwd[i] == 0 {
			best := 0
			for _, j := range out[key{edges[i].To, axes[i]}] {
				if consistent(i, j) {
					best = max(best, runFwd(j))
				}
			}
			fwd[i] = best + 1
		}
		return fwd[i]
	}
	var drop []int
	for i, e := range edges {
		if e.Wave && runBack(i)+runFwd(i) < cfg.MinChain { // (back+fwd-1) edges = back+fwd phones
			drop = append(drop, i)
		}
	}
	for _, i := range drop {
		edges[i].Wave = false
	}
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

func dirName(net int, axis string) string {
	switch {
	case net > 0:
		return "+" + axis
	case net < 0:
		return "-" + axis
	}
	return ""
}

func uniq(a, b int) []int {
	if a == b {
		return []int{a}
	}
	return []int{a, b}
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
