// Package crowd finds where people are bunching up: DBSCAN over phone
// positions, clusters tracked across detector ticks with stable IDs, a
// forming / steady / dispersing trend, and a calm / yellow / red level from
// the estimated density (people per m²).
//
// Density is phones per m² of the cluster's disc (centre = centroid,
// radius = farthest member + 0.5 m, area at least 1 m²). Not everyone has
// the page open, so people = phones / Participation and the level uses the
// estimated density = max(disc density, peak local density) ÷ Participation,
// where the peak local density is the phones within LocalR (1.5 m) of a
// member ÷ that disc's area, at the 90th percentile of the members: a
// large crowd with a packed front reads as packed, not as its thin average.
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
}

// ConfigFrom takes the crowd settings from the detector config. The level
// hysteresis reuses Margin as a fraction of the threshold (0.1 → yellow
// clears below 1.8, red below 3.6 people/m² at the defaults) and HoldMs as is.
func ConfigFrom(c detect.Config) Config {
	return Config{Eps: c.ClusterEps, MinPts: c.ClusterMinPts, TrendMs: c.ClusterTrendMs,
		Watch: c.DensityWatch, Danger: c.DensityDanger, Participation: c.Participation,
		Margin: c.Margin, HoldMs: c.HoldMs}
}

// Cluster is one group of phones.
type Cluster struct {
	ID      string
	X, Y    float64 // centroid, m
	R       float64 // m
	Count   int     // phones
	Density float64 // phones per m²
	Peak    float64 // phones per m² within LocalR of a member, 90th percentile over members
	PeakX   float64 // where that member stands
	PeakY   float64
	People  int     // estimated head count (Count / Participation)
	Est     float64 // estimated people per m²
	Trend   string
	Level   string
	Members []string
}

// Area of the cluster's disc, m² (at least 1).
func (c Cluster) Area() float64 { return math.Max(math.Pi*c.R*c.R, 1) }

// Change is a cluster level transition.
type Change struct {
	T       int64
	Cluster Cluster // the cluster as it is now (Level = To)
	From    string
	To      string
}

type sample struct {
	t     int64
	count int
	est   float64
}

type track struct {
	id       string
	c        Cluster
	born     int64
	lastSeen int64
	hist     []sample
	state    detect.LevelState
}

// Tracker follows clusters over time. Not safe for concurrent use.
type Tracker struct {
	cfg    Config
	tracks []*track
	seq    int
}

// NewTracker creates a tracker.
func NewTracker(cfg Config) *Tracker { return &Tracker{cfg: cfg} }

// SetConfig changes the settings, keeping the tracked clusters.
func (t *Tracker) SetConfig(cfg Config) { t.cfg = cfg }

// Update clusters the points at time now and returns the current clusters
// (sorted by ID) and any level changes, including clusters that vanished
// while not calm (→ calm).
func (t *Tracker) Update(now int64, pts []Point) ([]Cluster, []Change) {
	cfg := t.cfg
	groups := DBSCAN(pts, cfg.Eps, cfg.MinPts)
	found := make([]Cluster, len(groups))
	for i, g := range groups {
		found[i] = describe(pts, g, cfg.Participation)
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
	for fi := range found {
		if owner[fi] == nil {
			t.seq++
			tr := &track{id: fmt.Sprintf("c%d", t.seq), born: now, state: detect.NewLevelState()}
			t.tracks = append(t.tracks, tr)
			owner[fi] = tr
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
		from, to, changed := tr.state.Update(now, f.Est, th)
		f.Level = tr.state.Level
		tr.c = f
		if changed {
			changes = append(changes, Change{T: now, Cluster: f, From: from, To: to})
		}
		out = append(out, f)
	}
	// Forget clusters gone for longer than the grace period.
	keep := t.tracks[:0]
	for _, tr := range t.tracks {
		if now-tr.lastSeen <= GraceMs {
			keep = append(keep, tr)
			continue
		}
		if tr.state.Level != protocol.LevelCalm {
			c := tr.c
			c.Level, c.Trend = protocol.LevelCalm, Dispersing
			changes = append(changes, Change{T: now, Cluster: c, From: tr.state.Level, To: protocol.LevelCalm})
		}
	}
	t.tracks = keep
	sort.Slice(out, func(i, j int) bool { return idLess(out[i].ID, out[j].ID) })
	return out, changes
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

// describe turns a group of points into a cluster (no ID, trend or level).
func describe(pts []Point, idx []int, participation float64) Cluster {
	var c Cluster
	for _, i := range idx {
		c.X += pts[i].X
		c.Y += pts[i].Y
		c.Members = append(c.Members, pts[i].ID)
	}
	n := float64(len(idx))
	c.X /= n
	c.Y /= n
	far := 0.0
	for _, i := range idx {
		far = math.Max(far, math.Hypot(pts[i].X-c.X, pts[i].Y-c.Y))
	}
	c.R = far + padM
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
	c.Peak, c.PeakX, c.PeakY = LocalPeak(sub)
	if participation <= 0 {
		participation = 1
	}
	c.People = int(math.Round(n / participation))
	c.Est = math.Max(c.Density, c.Peak) / participation
	sort.Strings(c.Members)
	return c
}

// LocalPeak is the peak local density of a set of phones: for each one,
// the phones within LocalR (itself included) ÷ that disc's area, taken at
// the PeakQuantile of the set, and where that phone stands. Phones per m²;
// divide by participation for people per m². Zero for no points.
func LocalPeak(pts []Point) (peak, x, y float64) {
	if len(pts) == 0 {
		return 0, 0, 0
	}
	r2 := LocalR * LocalR
	type local struct {
		k    int
		x, y float64
	}
	loc := make([]local, 0, len(pts))
	for _, p := range pts {
		k := 0
		for _, q := range pts {
			dx, dy := p.X-q.X, p.Y-q.Y
			if dx*dx+dy*dy <= r2 {
				k++
			}
		}
		loc = append(loc, local{k, p.X, p.Y})
	}
	sort.SliceStable(loc, func(a, b int) bool { return loc[a].k < loc[b].k })
	q := loc[min(len(loc)-1, int(PeakQuantile*float64(len(loc))))]
	return float64(q.k) / (math.Pi * r2), q.x, q.y
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
