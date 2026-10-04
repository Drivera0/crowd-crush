// Package crowd finds where people are bunching up: DBSCAN over phone
// positions, clusters tracked across detector ticks with stable IDs, a
// forming / steady / dispersing trend, and a calm / yellow / red level from
// the estimated density (people per m²).
//
// Density is phones per m² of the cluster's disc (centre = centroid,
// radius = farthest member + 0.5 m, area at least 1 m²). Not everyone has
// the page open, so people = phones / Participation and the level uses the
// estimated density Est = max(disc density, peak local density) ÷
// Participation, where the peak local density (LocalPeakAmong) is the
// phones within LocalR (1.5 m) of a member ÷ that disc's area at the
// cluster's densest well-supported spot: a large crowd with a packed front
// reads as packed, not as its thin average. Est is the one density number:
// cluster levels, early warning, area density rules and briefings use it.
//
// A dense cluster that people are still getting out of (flow.go) raises no
// early warning and its yellow is shown calm: an aisle emptying a hall is a
// queue, not a crush building. Red is never masked.
package crowd

import (
	"fmt"
	"math"
	"sort"

	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// Trends.
const (
	Forming    = "forming"
	Steady     = "steady"
	Dispersing = "dispersing"
)

// Trend thresholds, comparing the start of the ClusterTrendMs window with
// now (each side averaged over TrendAvgMs so one jittery tick doesn't
// decide). Head count is the steadier signal, so it goes first:
//   - forming: at least max(2, 10 %) more phones, else estimated density up
//     by ≥ 15 %;
//   - dispersing: at least max(2, 10 %) fewer phones, else density down by
//     ≥ 15 %;
//   - steady otherwise. A cluster younger than half the window is forming
//     (it just appeared).
const (
	TrendRel      = 0.15
	TrendCountAbs = 2
	TrendCountRel = 0.10
	TrendAvgMs    = 2000
)

// MatchDist is how far (m) a cluster's centroid may move between ticks and
// keep its ID; GraceMs is how long a cluster may vanish (dropped below
// minPts for a moment) before it is forgotten.
const (
	MatchDist = 2.0
	GraceMs   = 2000
	padM      = 0.5 // added to the farthest member for the radius
	// LocalR is the radius (m) of the local density around each member.
	LocalR = 1.5
	// PeakQuantile picks the local density that counts for the cluster.
	PeakQuantile = 0.9
)

// Point is one phone position.
type Point struct {
	ID   string
	X, Y float64
	// Acc is the accuracy radius of the position (m): the 68 % radius a
	// phone's GPS reports. 0 = exact (placed by hand).
	Acc float64
}

// Config is the clustering and density-alert part of the detector config.
type Config struct {
	Eps           float64 // m
	MinPts        int
	TrendMs       int64
	Watch, Danger float64 // people/m²
	Participation float64
	Margin        float64 // hysteresis as a fraction of the threshold
	HoldMs        int64
	// Early warning from the density trend: a cluster at or above
	// EarlyFloor × Danger whose estimated density is projected to reach
	// Danger within EarlyWarnS seconds (at its current rate) is at least
	// yellow. EarlyWarnS 0 = off.
	EarlyWarnS float64
	EarlyFloor float64
	// AccDisc: around a phone whose position is only known to ± Acc metres
	// the local density is counted over a disc of radius AccDisc × Acc when
	// that is wider than LocalR, and a cluster of such phones is at least
	// that wide. 0 = positions are taken as exact.
	AccDisc float64
	// Flow (flow.go): a dense crowd that people are still getting out of is
	// held to FlowWatch, not Watch, and raises no early warning.
	// FlowMinOut 0 = off; FlowWatch 0 = Danger.
	FlowMinOut, FlowWatch, FlowMaxAcc float64
	FlowMemoryMs                      int64
}

// Early-warning rate estimate: least-squares slope of the estimated density
// over the last RateWindowMs, once the samples span at least RateMinSpanMs.
// The projection must hold for EarlyHoldMs before it raises the level.
const (
	RateWindowMs  = 8000
	RateMinSpanMs = 5000
	EarlyHoldMs   = 500
)

// ConfigFrom takes the crowd settings from the detector config. The level
// hysteresis reuses Margin as a fraction of the threshold (0.1 → yellow
// clears below 1.8, red below 3.6 people/m² at the defaults) and HoldMs as is.
func ConfigFrom(c detect.Config) Config {
	return Config{Eps: c.ClusterEps, MinPts: c.ClusterMinPts, TrendMs: c.ClusterTrendMs,
		Watch: c.DensityWatch, Danger: c.DensityDanger, Participation: c.Participation,
		Margin: c.Margin, HoldMs: c.HoldMs, EarlyWarnS: c.EarlyWarnS, EarlyFloor: c.EarlyFloor,
		AccDisc: c.DensityAccDisc, FlowMinOut: c.FlowMinOut,
		FlowWatch: c.FlowWatch, FlowMaxAcc: c.FlowMaxAcc, FlowMemoryMs: c.FlowMemoryMs}
}

// Cluster is one group of phones.
type Cluster struct {
	ID      string
	X, Y    float64 // centroid, m
	R       float64 // m
	Count   int     // phones
	Density float64 // phones per m²
	Peak    float64 // phones per m² within LocalR of a member at the densest spot (LocalPeakAmong)
	PeakX   float64 // where that member stands (the cluster's densest spot)
	PeakY   float64
	People  int     // estimated head count (Count / Participation)
	Est     float64 // estimated people per m²
	// Acc is the median position accuracy of the members (m; 0 = placed by
	// hand). When it is several metres, Est is the density averaged over a
	// disc about that wide (AccDisc × Acc), which reads lower than the
	// density at the tightest spot of the crowd: a lower bound.
	Acc     float64
	Trend   string
	Level   string
	Members []string
	// Rate is how fast Est is changing (people/m² per minute; 0 until the
	// cluster has RateMinSpanMs of history). ETA is the projected seconds
	// until Est reaches Danger at that rate, only when Rate > 0, Est is
	// below Danger and the projection is within EarlyWarnS (else 0).
	Rate float64
	ETA  float64
	// Early: the projection holds (ETA set, Est ≥ EarlyFloor × Danger).
	Early bool
	// Motion of the crowd at the densest spot (flow.go): Speed is the net
	// speed of the phones within LocalR of (PeakX, PeakY) (m/s); In and Out
	// are the people per second crossing into and out of that disc; Flow =
	// Out per metre of the disc's width (people per metre per second).
	// FlowKnown: enough phones placed well enough to tell; Flowing: people
	// are getting out (FlowMinOut), now or within FlowMemoryMs, so the
	// cluster is not on watch below FlowWatch and raises no early warning.
	Speed, Flow, In, Out float64
	FlowKnown, Flowing   bool
	inN, outN            int // phones that crossed in / out
}

// Area of the cluster's disc, m² (at least 1).
func (c Cluster) Area() float64 { return math.Max(math.Pi*c.R*c.R, 1) }

// Change is a cluster level transition.
type Change struct {
	T       int64
	Cluster Cluster // the cluster as it is now (Level = To)
	From    string
	To      string
	// Early: the density projection (not the density itself) raised the
	// cluster to yellow, or (From = To = yellow) a cluster already yellow
	// is now projected to reach Danger soon. Once per yellow stretch.
	Early bool
}

type sample struct {
	t     int64
	count int
	est   float64
}

type track struct {
	id         string
	c          Cluster
	born       int64
	lastSeen   int64
	hist       []sample
	state      detect.LevelState
	earlySince int64 // when the projection started holding (0 = not)
	// earlyRaised: an early warning was reported in this yellow stretch.
	earlyRaised bool
	flowAt      int64  // when people were last seen getting out (0 = never)
	flowing     bool   // the held flowing answer (flow.go)
	flowSince   int64  // when the instant answer started to differ from it (0 = it doesn't)
	shown       string // the level last reported (state.Level, unless flow masks a yellow)
}

// Tracker follows clusters over time. Not safe for concurrent use.
type Tracker struct {
	cfg    Config
	tracks []*track
	seq    int
	moves  moves // recent positions per phone (flow.go)
}

// NewTracker creates a tracker.
func NewTracker(cfg Config) *Tracker { return &Tracker{cfg: cfg, moves: moves{}} }

// SetConfig changes the settings, keeping the tracked clusters.
func (t *Tracker) SetConfig(cfg Config) { t.cfg = cfg }

// Update clusters the points at time now and returns the current clusters
// (sorted by ID) and any level changes, including clusters that vanished
// while not calm (→ calm).
func (t *Tracker) Update(now int64, pts []Point) ([]Cluster, []Change) {
	cfg := t.cfg
	groups := DBSCAN(pts, cfg.Eps, cfg.MinPts)
	found := make([]Cluster, len(groups))
	if cfg.flowOn() {
		t.moves.add(now, pts, cfg.FlowMaxAcc)
	}
	for i, g := range groups {
		found[i] = describe(pts, g, cfg.Participation, cfg.AccDisc)
		t.flow(&found[i], pts, now)
	}

	// Greedy nearest matching of new clusters to tracks.
	type pair struct {
		ti, fi int
		d      float64
	}
	var pairs []pair
	for ti, tr := range t.tracks {
		for fi, f := range found {
			if d := math.Hypot(tr.c.X-f.X, tr.c.Y-f.Y); d <= MatchDist {
				pairs = append(pairs, pair{ti, fi, d})
			}
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].d != pairs[j].d {
			return pairs[i].d < pairs[j].d
		}
		if pairs[i].ti != pairs[j].ti {
			return pairs[i].ti < pairs[j].ti
		}
		return pairs[i].fi < pairs[j].fi
	})
	usedT := make([]bool, len(t.tracks))
	usedF := make([]bool, len(found))
	owner := make([]*track, len(found))
	for _, p := range pairs {
		if usedT[p.ti] || usedF[p.fi] {
			continue
		}
		usedT[p.ti], usedF[p.fi] = true, true
		owner[p.fi] = t.tracks[p.ti]
	}
	// Where each phone was a tick ago, for new clusters' history.
	was := map[string]*track{}
	for _, tr := range t.tracks {
		if tr.lastSeen >= now-GraceMs {
			for _, id := range tr.c.Members {
				was[id] = tr
			}
		}
	}
	for fi := range found {
		if owner[fi] == nil {
			t.seq++
			tr := &track{id: fmt.Sprintf("c%d", t.seq), born: now, state: detect.NewLevelState(), shown: protocol.LevelCalm}
			t.tracks = append(t.tracks, tr)
			owner[fi] = tr
		}
		// Clusters merge and split as a crowd moves (a surge pulls small
		// groups into one big one): a cluster with little history takes the
		// older density history of the cluster most of its phones came
		// from, so its rate (early warning) doesn't restart from nothing.
		tr := owner[fi]
		if src := mostOf(found[fi].Members, was); src != nil && src != tr && len(src.hist) > 0 &&
			(len(tr.hist) == 0 || tr.hist[len(tr.hist)-1].t-tr.hist[0].t < RateMinSpanMs) {
			first := now + 1
			if len(tr.hist) > 0 {
				first = tr.hist[0].t
			}
			var older []sample
			for _, s := range src.hist {
				if s.t < first {
					older = append(older, s)
				}
			}
			tr.hist = append(older, tr.hist...)
		}
	}

	th := detect.Thresholds{Yellow: cfg.Watch, Red: cfg.Danger,
		YMargin: cfg.Watch * cfg.Margin, RMargin: cfg.Danger * cfg.Margin, Hold: cfg.HoldMs}
	var out []Cluster
	var changes []Change
	for fi, f := range found {
		tr := owner[fi]
		tr.lastSeen = now
		tr.hist = append(tr.hist, sample{now, f.Count, f.Est})
		cut := 0
		for cut < len(tr.hist) && tr.hist[cut].t < now-cfg.TrendMs {
			cut++
		}
		tr.hist = tr.hist[cut:]
		f.ID = tr.id
		f.Trend = t.trend(tr, now)
		f.Rate, f.ETA, f.Early = t.project(tr, f.Est)
		t.flowState(tr, &f, now)
		if f.Flowing {
			// People are getting out: the rise is a route filling, which
			// drains. No projection.
			f.ETA, f.Early = 0, false
		}
		from, to, changed := tr.state.Update(now, f.Est, th)
		if changed && to == protocol.LevelCalm && f.Early {
			// Still projected to be dangerous soon (a floor below the
			// yellow hysteresis): stay yellow.
			tr.state.Level, changed = from, false
		}
		early := false
		if f.Early {
			if tr.earlySince == 0 {
				tr.earlySince = now
			}
			// Projected to be dangerous soon: at least yellow, without
			// waiting for the density itself to hold above the watch level.
			if tr.state.Level == protocol.LevelCalm && now-tr.earlySince >= EarlyHoldMs {
				tr.state.Level, tr.state.Since = protocol.LevelYellow, now
				from, to, changed, early = protocol.LevelCalm, protocol.LevelYellow, true, true
				tr.earlyRaised = true
			} else if !changed && !tr.earlyRaised && tr.state.Level == protocol.LevelYellow && now-tr.earlySince >= EarlyHoldMs {
				// Already yellow (a packed but steady crowd) and now
				// projected to be dangerous soon: report it as an early
				// warning (yellow → yellow, Early), once per yellow stretch.
				from, to, changed, early = protocol.LevelYellow, protocol.LevelYellow, true, true
				tr.earlyRaised = true
			}
		} else {
			tr.earlySince = 0
		}
		if tr.state.Level != protocol.LevelYellow {
			tr.earlyRaised = false
		}
		// The level shown: the state machine's, except that a flowing crowd
		// below FlowWatch is not on watch (red is never masked, so its
		// timing is the density's alone).
		f.Level = tr.state.Level
		if f.Level == protocol.LevelYellow && f.Flowing && f.Est < cfg.flowWatch() {
			f.Level = protocol.LevelCalm
		}
		tr.c = f
		switch {
		case f.Level != tr.shown:
			changes = append(changes, Change{T: now, Cluster: f, From: tr.shown, To: f.Level, Early: early && f.Level == protocol.LevelYellow})
		case changed && early && from == to:
			changes = append(changes, Change{T: now, Cluster: f, From: from, To: to, Early: true})
		}
		tr.shown = f.Level
		out = append(out, f)
	}
	// Forget clusters gone for longer than the grace period.
	keep := t.tracks[:0]
	for _, tr := range t.tracks {
		if now-tr.lastSeen <= GraceMs {
			keep = append(keep, tr)
			continue
		}
		if tr.shown != protocol.LevelCalm {
			c := tr.c
			c.Level, c.Trend = protocol.LevelCalm, Dispersing
			changes = append(changes, Change{T: now, Cluster: c, From: tr.shown, To: protocol.LevelCalm})
		}
	}
	t.tracks = keep
	sort.Slice(out, func(i, j int) bool { return idLess(out[i].ID, out[j].ID) })
	return out, changes
}

// mostOf is the track most of these phones were in, nil if none.
func mostOf(members []string, was map[string]*track) *track {
	n := map[*track]int{}
	var best *track
	for _, id := range members {
		if tr := was[id]; tr != nil {
			n[tr]++
			if best == nil || n[tr] > n[best] || (n[tr] == n[best] && tr.id < best.id) {
				best = tr
			}
		}
	}
	return best
}

// idLess sorts c2 before c10.
func idLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

func (t *Tracker) trend(tr *track, now int64) string {
	if now-tr.born < t.cfg.TrendMs/2 {
		return Forming
	}
	h := tr.hist
	if len(h) < 2 {
		return Steady
	}
	avg := func(from, to int64) (count, est float64) {
		n := 0
		for _, s := range h {
			if s.t >= from && s.t <= to {
				count += float64(s.count)
				est += s.est
				n++
			}
		}
		if n == 0 {
			return 0, 0
		}
		return count / float64(n), est / float64(n)
	}
	oc, oe := avg(h[0].t, h[0].t+TrendAvgMs)
	nc, ne := avg(now-TrendAvgMs, now)
	rel := (ne - oe) / math.Max(oe, 0.1)
	dc := nc - oc
	need := math.Max(TrendCountAbs, TrendCountRel*oc)
	switch {
	case dc >= need:
		return Forming
	case dc <= -need:
		return Dispersing
	case rel >= TrendRel:
		return Forming
	case rel <= -TrendRel:
		return Dispersing
	}
	return Steady
}

// project estimates the density trend of a track: rate (people/m² per
// minute) from a least-squares slope over the last RateWindowMs of its
// estimated density, the projected seconds until Danger at that rate (when
// rate > 0, est < Danger and it is within EarlyWarnS), and whether that
// projection is an early warning (est ≥ EarlyFloor × Danger).
func (t *Tracker) project(tr *track, est float64) (rate, eta float64, early bool) {
	cfg := t.cfg
	rate = slope(tr.hist, tr.hist[len(tr.hist)-1].t-RateWindowMs) * 60_000
	if cfg.EarlyWarnS <= 0 || rate <= 0 || est >= cfg.Danger {
		return rate, 0, false
	}
	eta = (cfg.Danger - est) / (rate / 60)
	if eta > cfg.EarlyWarnS {
		return rate, 0, false
	}
	return rate, eta, est >= cfg.EarlyFloor*cfg.Danger
}

// slope is the least-squares slope (per ms) of the estimated density over
// the samples at or after from, 0 when they span less than RateMinSpanMs.
func slope(h []sample, from int64) float64 {
	i := sort.Search(len(h), func(i int) bool { return h[i].t >= from })
	h = h[i:]
	if len(h) < 3 || h[len(h)-1].t-h[0].t < RateMinSpanMs {
		return 0
	}
	t0 := h[0].t
	var st, se float64
	for _, s := range h {
		st += float64(s.t - t0)
		se += s.est
	}
	n := float64(len(h))
	mt, me := st/n, se/n
	var stt, ste float64
	for _, s := range h {
		dt := float64(s.t-t0) - mt
		stt += dt * dt
		ste += dt * (s.est - me)
	}
	if stt == 0 {
		return 0
	}
	return ste / stt
}

// describe turns a group of points into a cluster (no ID, trend or level).
// Local densities count every phone (not only the cluster's members) as a
// neighbour.
func describe(pts []Point, idx []int, participation, accDisc float64) Cluster {
	var c Cluster
	accs := make([]float64, 0, len(idx))
	for _, i := range idx {
		c.X += pts[i].X
		c.Y += pts[i].Y
		c.Members = append(c.Members, pts[i].ID)
		accs = append(accs, pts[i].Acc)
	}
	sort.Float64s(accs)
	c.Acc = accs[len(accs)/2]
	n := float64(len(idx))
	c.X /= n
	c.Y /= n
	far := 0.0
	for _, i := range idx {
		far = math.Max(far, math.Hypot(pts[i].X-c.X, pts[i].Y-c.Y))
	}
	// Phones that only know where they are to ± Acc can't show a group
	// tighter than that.
	c.R = math.Max(far, accDisc*c.Acc) + padM
	c.Count = len(idx)
	c.Density = n / c.Area()
	// Peak local density: phones within LocalR of a member, at the
	// PeakQuantile of the members (the max of many noisy counts would be
	// biased high). A big crowd (say 20 m of people pressed against a
	// barrier) is one cluster whose disc is mostly thin crowd, so the disc
	// average hides a packed front; the local peak does not.
	sub := make([]Point, len(idx))
	for k, i := range idx {
		sub[k] = pts[i]
	}
	c.Peak, c.PeakX, c.PeakY = localPeak(pts, sub, accDisc)
	if participation <= 0 {
		participation = 1
	}
	c.People = int(math.Round(n / participation))
	c.Est = math.Max(c.Density, c.Peak) / participation
	sort.Strings(c.Members)
	return c
}

// PeakAgree: the densest spot's reading must be backed by its neighbours.
// A local count of k phones counts as the peak only when at least
// k / PeakAgree phones have k or more within LocalR (one phone that happens
// to stand in a lucky spot can't make a crowd read packed), so for a packed
// patch a few metres across the peak is its real density, however much
// thinner crowd surrounds it.
const PeakAgree = 5

// LocalPeak is LocalPeakAmong(pts, pts).
func LocalPeak(pts []Point) (peak, x, y float64) { return LocalPeakAmong(pts, pts) }

// LocalPeakAmong is the peak local density around the phones in centres,
// counting every phone in all as a neighbour (so a centre near the edge of
// an area or cluster still sees the crowd just past it): for each centre,
// the phones within LocalR (itself included) ÷ that disc's area (7.07 m²).
// The peak is the larger of
//   - the PeakQuantile of the centres' local densities (robust for small
//     groups), and
//   - the densest local count k that at least k / PeakAgree centres reach
//     (a packed patch inside a much larger, thinner crowd: the quantile alone
//     would read the thin crowd once the patch is under 10 % of the phones),
//
// and (x, y) is where it is: the densest centre when the second wins, else
// the quantile's centre. Phones per m²; divide by participation for people
// per m². Zero for no centres. This is the one density estimate behind
// cluster levels, early warning, area density rules and briefings.
//
// A centre whose position is only known to ± Acc metres counts over a disc
// of radius AccDisc × Acc when that is wider than LocalR (its count is then
// scaled to the LocalR disc): the density at the scale its position
// supports. Exactly placed centres (Acc 0) are unaffected.
func LocalPeakAmong(all, centres []Point) (peak, x, y float64) {
	return localPeak(all, centres, AccDisc)
}

// AccDisc is the default of Config.AccDisc (detect.Config.DensityAccDisc),
// used by LocalPeakAmong.
const AccDisc = 0.5

func localPeak(all, centres []Point, accDisc float64) (peak, x, y float64) {
	if len(centres) == 0 {
		return 0, 0, 0
	}
	r2 := LocalR * LocalR
	type local struct {
		k    float64 // phones within LocalR (or the equivalent, from a wider disc)
		x, y float64
	}
	loc := make([]local, 0, len(centres))
	for _, p := range centres {
		rr := r2
		if u := accDisc * p.Acc; u > LocalR {
			rr = u * u
		}
		k := 0
		for _, q := range all {
			dx, dy := p.X-q.X, p.Y-q.Y
			if dx*dx+dy*dy <= rr {
				k++
			}
		}
		loc = append(loc, local{float64(max(k, 1)) * r2 / rr, p.X, p.Y})
	}
	sort.SliceStable(loc, func(a, b int) bool { return loc[a].k < loc[b].k })
	q := loc[min(len(loc)-1, int(PeakQuantile*float64(len(loc))))]
	best, bx, by := q.k, q.x, q.y
	// Agreed peak: walking down from the densest, the i-th densest centre
	// backs a count of at most PeakAgree × i.
	top := loc[len(loc)-1]
	for i := 1; i <= len(loc); i++ {
		k := loc[len(loc)-i].k
		if v := math.Min(k, float64(PeakAgree*i)); v > best {
			best, bx, by = v, top.x, top.y
		}
		if float64(PeakAgree*i) >= k {
			break // further down, k only shrinks
		}
	}
	return best / (math.Pi * r2), bx, by
}

// DBSCAN groups points that have at least minPts points (themselves
// included) within eps, plus the border points they reach. Noise is left
// out. Groups come back ordered by their first point's index.
func DBSCAN(pts []Point, eps float64, minPts int) [][]int {
	n := len(pts)
	e2 := eps * eps
	nb := make([][]int, n)
	for i := range pts {
		for j := range pts {
			dx, dy := pts[i].X-pts[j].X, pts[i].Y-pts[j].Y
			if dx*dx+dy*dy <= e2 {
				nb[i] = append(nb[i], j)
			}
		}
	}
	label := make([]int, n) // 0 unvisited, -1 noise, k>0 cluster k
	var groups [][]int
	for i := range pts {
		if label[i] != 0 {
			continue
		}
		if len(nb[i]) < minPts {
			label[i] = -1
			continue
		}
		k := len(groups) + 1
		group := []int{}
		queue := []int{i}
		label[i] = k
		for len(queue) > 0 {
			p := queue[0]
			queue = queue[1:]
			group = append(group, p)
			if len(nb[p]) < minPts {
				continue // border point: don't expand
			}
			for _, q := range nb[p] {
				if label[q] <= 0 {
					label[q] = k
					queue = append(queue, q)
				}
			}
		}
		sort.Ints(group)
		groups = append(groups, group)
	}
	return groups
}
