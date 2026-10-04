package detect

import (
	"math"
	"sort"
)

// Neighbours from motion, for phones whose position is only roughly known.
//
// A phone placed by hand stands exactly where it says, and its neighbours
// are whoever is within NeighbourRadius (detect.go). A phone placed by GPS
// reports an accuracy radius of several metres: the people within 1.1 m of
// its dot on the map are strangers, and its real neighbours are somewhere
// within that radius. For such a phone (acc > 0, SetAccuracy) the map only
// says who *could* be a neighbour; the motion says who is:
//
//   - Candidates: every other phone whose reported position is within
//     NeighbourRadius + AccPairScale × (accA + accB), at most MaxPairRadius.
//     Only phones that are moving at all (sway ≥ EdgeMinSway, not handled,
//     not walking) are candidates, so a calm crowd costs nothing.
//   - Cost: each roughly placed phone is compared with at most MotionPairs
//     candidates per step. Pairs that looked like a wave hop in the last
//     stickySteps steps come first (the "motion neighbour graph"), the rest
//     of the budget goes to candidates drawn afresh every step, so within a
//     second or two a phone has tried everyone around it.
//   - A candidate pair is kept (listed as an edge, drawn as a link) only
//     when it looks like a wave hop: |corr| ≥ ChainCorr at a wave-like lag,
//     not vertical. It is a wave edge under exactly the tests of an exactly
//     placed pair: strong correlation at a wave-like lag, one clear peak,
//     not vertical.
//   - A resolved lag: the best lag must beat every lag at least 300 ms away
//     by PeakMargin, separate peak or not (resolvedSecond). A slow sway
//     passed from row to row correlates almost as well half a second either
//     side of its best lag; without a map nothing else says it travels.
//   - Chains without a map: a wave edge still needs to be part of a run of
//     MinChain phones, but "travelling the same way" can't be read off rough
//     positions. Instead the lags must add up: for hops a→b and b→c, a and c
//     must match (|corr| ≥ ChainCorr) at lag(a,b) + lag(b,c) ± LagClosureMs.
//     Three phones hit in sequence by the same disturbance pass; two pairs
//     that each happen to correlate don't.
//   - Zone score: of all pairs of the zone's roughly placed phones, the
//     share whose two phones are both on such a wave (roughScore), instead
//     of the net fraction of edges travelling one way: an edge between two
//     neighbours counts when the wave reaches both. A push that moves
//     everyone scores near 1; a procession brushing every other person
//     scores a quarter. The same yellow/red thresholds apply (red at 0.6 is
//     about 4 phones in 5 on the wave). The zone a phone counts toward is
//     the one its rough position falls in.
//   - Direction: the sum of the wave edges' travel vectors between the rough
//     positions, named only when at least roughDirAgree of it survives the
//     sum (many phones over many metres); otherwise the zone has no
//     direction.
//
// With every phone exactly placed nothing in this file runs.

const (
	// stickySteps: how long a motion pair that looked like a wave hop keeps
	// its place in the next steps' comparisons (8 steps = 2 s).
	stickySteps = 8
	// roughDirAgree: |Σ unit travel vectors| ÷ edges needed to name a
	// direction from rough positions.
	roughDirAgree = 0.5
	// roughMinWave: fewer roughly placed wave phones than this in a zone
	// score nothing (one phone whose partners were put elsewhere by their
	// GPS error is not a wave in this zone).
	roughMinWave = 2
)

// roughScore is the wave score of a zone's roughly placed phones: of the
// pairs among the n of them, the share with both phones on a wave found by
// motion (wave phones).
func roughScore(wave, n int) float64 {
	if n <= 0 || wave < roughMinWave {
		return 0
	}
	if n < 2 {
		return 1
	}
	return float64(wave) * float64(wave-1) / (float64(n) * float64(n-1))
}

// hashID is FNV-1a over the id.
func hashID(id string) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(id); i++ {
		h ^= uint64(id[i])
		h *= 1099511628211
	}
	return h
}

// mix64 is the splitmix64 finaliser.
func mix64(z uint64) uint64 {
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// stuckWith is one entry of a phone's motion-neighbour list: a partner it
// looked like a wave hop with, and the step it last did.
type stuckWith struct {
	q   *phone
	seq uint64
}

// stick remembers that a and b looked like a wave hop in this step.
func (d *Detector) stick(a, b *phone) {
	add := func(p, q *phone) {
		for i := range p.stuck {
			if p.stuck[i].q == q {
				p.stuck[i].seq = d.seq
				return
			}
		}
		p.stuck = append(p.stuck, stuckWith{q, d.seq})
	}
	add(a, b)
	add(b, a)
}

// motionPairs picks the pairs to compare that involve at least one roughly
// placed phone (see the comment at the top). spatial is every active phone
// inside the venue, sorted by id. Pairs come back ordered and sorted like
// neighbourPairs'.
func (d *Detector) motionPairs(spatial []*phone) [][2]*phone {
	cfg := &d.cfg
	var el []*phone
	rough := false
	for _, p := range spatial {
		if p.lastT >= p.handlingUntil && p.sway >= cfg.EdgeMinSway && !p.walking {
			p.eligAt, p.eligIdx = d.seq, len(el)
			el = append(el, p)
			rough = rough || p.acc > 0
		}
	}
	if !rough || len(el) < 2 {
		return nil
	}
	cell := cfg.MaxPairRadius
	cellOf := func(p *phone) [2]int {
		return [2]int{int(math.Floor(p.x / cell)), int(math.Floor(p.y / cell))}
	}
	grid := make(map[[2]int][]int, len(el))
	for i, p := range el {
		k := cellOf(p)
		grid[k] = append(grid[k], i)
	}
	near := func(a, b *phone) bool {
		r := math.Min(cfg.NeighbourRadius+cfg.AccPairScale*(a.acc+b.acc), cfg.MaxPairRadius)
		dx, dy := a.x-b.x, a.y-b.y
		return dx*dx+dy*dy <= r*r
	}
	type cand struct {
		j    int
		rank uint64
	}
	budget := cfg.MotionPairs
	maxSticky := budget - budget/4 // always leave room to try someone new
	keep := map[[2]int]bool{}
	take := func(i, j int) {
		if i > j {
			i, j = j, i
		}
		keep[[2]int{i, j}] = true
	}
	top := make([]cand, 0, budget+1) // the best-ranked fresh candidates so far, ascending
	salt := d.seq * 0x9E3779B97F4A7C15
	for i, a := range el {
		if a.acc <= 0 {
			continue // an exactly placed phone is picked by its rough partners
		}
		// The motion-neighbour list first, most recent first, dropping what
		// has gone stale.
		n := 0
		live := a.stuck[:0]
		for _, s := range a.stuck {
			if d.seq-s.seq <= stickySteps {
				live = append(live, s)
			}
		}
		a.stuck = live
		sort.Slice(a.stuck, func(x, y int) bool {
			if a.stuck[x].seq != a.stuck[y].seq {
				return a.stuck[x].seq > a.stuck[y].seq
			}
			return a.stuck[x].q.id < a.stuck[y].q.id
		})
		for _, s := range a.stuck {
			if n >= maxSticky {
				break
			}
			if b := s.q; b.eligAt == d.seq && el[b.eligIdx] == b && near(a, b) {
				take(i, b.eligIdx)
				n++
			}
		}
		// The rest of the budget: candidates in an order that changes every
		// step. (One of them may already be on the list above; then one
		// fewer new pair is tried this step.)
		need := budget - n
		top = top[:0]
		c := cellOf(a)
		for gx := c[0] - 1; gx <= c[0]+1; gx++ {
			for gy := c[1] - 1; gy <= c[1]+1; gy++ {
				for _, j := range grid[[2]int{gx, gy}] {
					if j == i || !near(a, el[j]) {
						continue
					}
					rank := mix64(a.hash ^ (el[j].hash * 0x9E3779B97F4A7C15) ^ salt)
					if len(top) == need && rank >= top[need-1].rank {
						continue
					}
					k := len(top)
					if k < need {
						top = append(top, cand{})
					} else {
						k--
					}
					for k > 0 && top[k-1].rank > rank {
						top[k] = top[k-1]
						k--
					}
					top[k] = cand{j, rank}
				}
			}
		}
		for _, cd := range top {
			take(i, cd.j)
		}
	}
	out := make([][2]*phone, 0, len(keep))
	for k := range keep {
		a, b := el[k[0]], el[k[1]]
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

// closure reports whether c repeats a's motion lag grid steps later (give
// or take LagClosureMs): |corr| ≥ ChainCorr (CorrThreshold when ChainCorr is
// off) at one of those lags. It is the chain test for hops found by motion:
// a→b and b→c only make a run when a→c closes at the sum of their lags.
func (d *Detector) closure(a, c *phone, lag float64) bool {
	cfg := &d.cfg
	need := cfg.ChainCorr
	if need <= 0 {
		need = cfg.CorrThreshold
	}
	n := len(a.h)
	if n == 0 || len(c.h) != n {
		return false
	}
	tol := int(cfg.LagClosureMs / cfg.StepMs)
	k0 := int(math.Round(lag))
	minOverlap := n / 2
	for k := k0 - tol; k <= k0+tol; k++ {
		if k <= 0 || n-k < minOverlap {
			continue
		}
		if math.Abs(corrAt(a.h, c.h, a.valid, c.valid, k, minOverlap)) >= need {
			return true
		}
	}
	return false
}
