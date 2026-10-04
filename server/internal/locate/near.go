package locate

import (
	"math"
	"sort"
)

// Motion neighbours.
//
// People standing shoulder to shoulder are moved by the same pushes: when
// a crowd is packed, what one body does its neighbours do a moment later,
// through contact. Two phones whose horizontal motion over the last few
// seconds is the same motion are therefore probably close, and that is a
// statement about distance that needs no map.
//
// Each Step, every phone that is moving at all (and not walking) has its
// band-passed horizontal acceleration (0.15–1.5 Hz, the detector's sway
// band, in its own levelled axes) put on a 100 ms grid. Two traces are
// compared by their rotation-free correlation: phone axes point any way
// round the vertical, so the 2 × 2 cross-covariance S of the two traces is
// reduced to the best it can be under a turn (or a turn and a flip, for a
// phone the wrong way up that sent no gravity):
//
//	r = max(√((Sxx+Syy)² + (Sxy−Syx)²), √((Sxx−Syy)² + (Sxy+Syx)²)) / √(Na·Nb)
//
// at lags of 0, ±100 and ±200 ms (contact carries a push a metre in about
// a tenth of a second). r ≥ NearCorr is a match.
//
// One match proves little: in a simulated surge 25–37 % of the pairs truly
// within 1.1 m match in any one 4 s window, but so do 1.5–3 % of the pairs
// more than 5 m apart, and there are many more of those. A pair is
// therefore compared again about once a second after its first match, and
// is a link only once the matches add up (NearHits recent ones, and at
// least half of the comparisons). Each comparison counts 15 % less than the
// one after it, so a link lapses a few seconds after the two traces stop
// agreeing. A pair that can't be compared any more (both standing still
// again) keeps its link for NearHoldMs, unless one of them walks off.
//
// Whom to compare: besides the pairs that matched before, each phone is
// compared with NearBudget others drawn afresh each step among the phones
// whose estimates are close enough to be neighbours given both
// uncertainties (LinkRange + 2 (σa + σb), at most NearMaxR). Within a few
// seconds it has tried everyone in reach.
//
// What it must not do: a hall swaying to one beat has everyone moving
// alike, near and far (97 % of all pairs match while a simulated crowd
// dances). Two guards. A phone whose own trace repeats (rhythm ≥
// NearRhythm: its best rotation-free correlation with itself 0.4–1.2 s
// later) is swaying to something, not being pushed, and is not compared.
// And each phone keeps the share of its random candidates that matched
// (common); when that passes NearCommon its matches say nothing about
// distance and are not used.

const (
	trN       = 40  // grid points per trace (4 s)
	trStepMs  = 100 //
	trLagMs   = 300 // newest grid point is this far behind now (late packets)
	trMaxGap  = 350 // ms between samples that may be interpolated across
	nearLags  = 2   // ± grid steps
	commonEMA = 0.08
)

type pairEv struct {
	hits, tests float64 // decayed counts
	lastOK      int64
	lastTest    int64
	lastMesh    int64 // last mesh report counted
	r           float64
	// How far each phone had been dead-reckoned when the pair last matched:
	// a phone that has walked on since is no longer next to the other.
	walkA, walkB float64
}

const (
	pairDecay   = 0.85 // per comparison
	pairEveryMs = 1000 // a matched pair is compared again this often
	nearWalkOff = 2.0  // m dead-reckoned since the last match that ends a pair
)

// Peer is one entry of a phone's mesh report: a peer it holds a link to
// and how its own motion correlates with that peer's.
type Peer struct {
	ID    string
	Corr  float64
	LagMs int64
	Hops  int
}

// Near takes a phone's mesh report: the peers it holds a direct link to
// and how its own motion correlates with each. A peer whose motion matches
// (|corr| ≥ NearCorr within 300 ms) is put forward as a pair: the server
// compares the two traces itself from then on, about once a second. Only
// when it can't (the summaries of one of the two aren't reaching it) does
// the report itself count as a match toward the link, at most one a
// second, and not for a phone last seen swaying to a rhythm.
func (e *Estimator) Near(id string, now int64, peers []Peer) {
	a := e.phones[id]
	if a == nil {
		return
	}
	for _, pr := range peers {
		b := e.phones[pr.ID]
		if b == nil || b == a || pr.Hops > 1 || math.Abs(pr.Corr) < e.cfg.NearCorr || pr.LagMs > 300 || pr.LagMs < -300 {
			continue
		}
		k := pkey(id, pr.ID)
		ev := e.pairs[k]
		if ev == nil {
			ev = &pairEv{lastOK: now, lastTest: now, hits: 0.5, tests: 1, walkA: e.phones[k[0]].tracked, walkB: e.phones[k[1]].tracked}
			e.pairs[k] = ev
			continue
		}
		// While the server has both phones' motion, its own comparison
		// speaks for the pair. The report counts when it hasn't (a phone
		// whose summaries aren't arriving).
		if now-ev.lastMesh < pairEveryMs || (now-a.lastSampleT <= 2000 && now-b.lastSampleT <= 2000) || a.rhythmic || b.rhythmic {
			continue
		}
		ev.lastMesh = now
		ev.hits, ev.tests = ev.hits*pairDecay+1, ev.tests*pairDecay+1
		ev.lastOK = now
	}
}

func pkey(a, b string) [2]string {
	if a > b {
		a, b = b, a
	}
	return [2]string{a, b}
}

// trace resamples a phone's band-passed horizontal motion onto the grid
// ending at end. ok is false when too much of it is missing.
func (p *phone) trace(end int64, handlingRot float64) bool {
	if cap(p.tr1) < trN {
		p.tr1, p.tr2 = make([]float64, trN), make([]float64, trN)
	}
	p.tr1, p.tr2 = p.tr1[:trN], p.tr2[:trN]
	start := end - int64(trN-1)*trStepMs
	j := 0
	var ss float64
	for k := 0; k < trN; k++ {
		t := start + int64(k)*trStepMs
		for j < p.n && p.at(j).t < t {
			j++
		}
		if j == 0 || j >= p.n {
			if j < p.n && p.at(j).t == t {
				s := p.at(j)
				p.tr1[k], p.tr2[k] = s.b1, s.b2
				ss += s.b1*s.b1 + s.b2*s.b2
				continue
			}
			return false
		}
		a, b := p.at(j-1), p.at(j)
		if b.t-a.t > trMaxGap || a.rot > handlingRot || b.rot > handlingRot {
			return false
		}
		f := float64(t-a.t) / float64(b.t-a.t)
		p.tr1[k] = a.b1 + f*(b.b1-a.b1)
		p.tr2[k] = a.b2 + f*(b.b2-a.b2)
		ss += p.tr1[k]*p.tr1[k] + p.tr2[k]*p.tr2[k]
	}
	p.trRMS = math.Sqrt(ss / trN)
	return true
}

// rotCorr is the rotation-free correlation of two phones' traces, the
// best over the lags.
func rotCorr(a, b *phone) float64 {
	best := 0.0
	for lag := -nearLags; lag <= nearLags; lag++ {
		var sxx, sxy, syx, syy, na, nb float64
		for k := 0; k < trN; k++ {
			m := k + lag
			if m < 0 || m >= trN {
				continue
			}
			a1, a2, b1, b2 := a.tr1[k], a.tr2[k], b.tr1[m], b.tr2[m]
			sxx += a1 * b1
			sxy += a1 * b2
			syx += a2 * b1
			syy += a2 * b2
			na += a1*a1 + a2*a2
			nb += b1*b1 + b2*b2
		}
		if na <= 0 || nb <= 0 {
			continue
		}
		r := math.Max(math.Hypot(sxx+syy, sxy-syx), math.Hypot(sxx-syy, sxy+syx)) / math.Sqrt(na*nb)
		if r > best {
			best = r
		}
	}
	return best
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

func mix64(z uint64) uint64 {
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// findNear compares traces and returns the links found by motion.
func (e *Estimator) findNear(now int64) []clink {
	cfg := &e.cfg
	end := now - trLagMs
	var el []*phone
	for _, p := range e.order {
		p.trOK = !p.gone && (!p.walking || cfg.NearWalking) && p.n >= trN/2 && p.trace(end, cfg.HandlingRot) && p.trRMS >= cfg.NearMinRMS
		p.rhythmic = false
		if p.trOK && p.rhythm() >= cfg.NearRhythm {
			p.trOK, p.rhythmic = false, true
		}
		if p.trOK {
			el = append(el, p)
		}
	}
	compare := func(a, b *phone, ev *pairEv) {
		r := rotCorr(a, b)
		e.stats.Compared++
		hit := r >= cfg.NearCorr
		if ev == nil {
			// A fresh candidate: it also says how common matching is.
			v := 0.0
			if hit {
				v = 1
			}
			a.common += commonEMA * (v - a.common)
			b.common += commonEMA * (v - b.common)
			if !hit {
				return
			}
			ev = &pairEv{}
			e.pairs[pkey(a.id, b.id)] = ev
		}
		ev.hits, ev.tests = ev.hits*pairDecay, ev.tests*pairDecay+1
		ev.lastTest, ev.r = now, r
		if hit {
			ev.hits++
			ev.lastOK = now
			x, y := e.phones[pkey(a.id, b.id)[0]], e.phones[pkey(a.id, b.id)[1]]
			ev.walkA, ev.walkB = x.tracked, y.tracked
		}
	}
	// Pairs that matched before, about once a second. A pair that can't be
	// compared for a while (both standing still again, or one swaying)
	// keeps what it has for NearHoldMs: people who were shoulder to
	// shoulder a moment ago still are, unless one of them walked off.
	for k, ev := range e.pairs {
		a, b := e.phones[k[0]], e.phones[k[1]]
		if a == nil || b == nil || ev.hits < 0.3 || now-ev.lastTest > cfg.NearHoldMs ||
			a.tracked-ev.walkA > nearWalkOff || b.tracked-ev.walkB > nearWalkOff {
			delete(e.pairs, k)
			continue
		}
		if a.trOK && b.trOK && a.placed && b.placed && now-ev.lastTest >= pairEveryMs {
			compare(a, b, ev)
		}
	}
	if len(el) >= 2 {
		cell := cfg.NearMaxR
		grid := make(map[[2]int][]*phone, len(el))
		cof := func(p *phone) [2]int { return [2]int{int(math.Floor(p.x / cell)), int(math.Floor(p.y / cell))} }
		for _, p := range el {
			grid[cof(p)] = append(grid[cof(p)], p)
		}
		type cand struct {
			q    *phone
			rank uint64
		}
		var cs []cand
		for _, a := range el {
			cs = cs[:0]
			c := cof(a)
			for gx := c[0] - 1; gx <= c[0]+1; gx++ {
				for gy := c[1] - 1; gy <= c[1]+1; gy++ {
					for _, b := range grid[[2]int{gx, gy}] {
						if b == a {
							continue
						}
						r := math.Min(cfg.LinkRange+2*(a.acc+b.acc), cfg.NearMaxR)
						if dx, dy := a.x-b.x, a.y-b.y; dx*dx+dy*dy > r*r {
							continue
						}
						cs = append(cs, cand{b, mix64(uint64(a.idx)*0x9E3779B97F4A7C15 ^ uint64(b.idx)<<20 ^ e.seq*0xD1B54A32D192ED03)})
					}
				}
			}
			if len(cs) > cfg.NearBudget {
				sort.Slice(cs, func(i, j int) bool { return cs[i].rank < cs[j].rank })
				cs = cs[:cfg.NearBudget]
			}
			for _, cd := range cs {
				if _, ok := e.pairs[pkey(a.id, cd.q.id)]; !ok {
					compare(a, cd.q, nil)
				}
			}
		}
	}
	var out []clink
	for k, ev := range e.pairs {
		if ev.hits < cfg.NearHits || ev.hits < ev.tests/2 {
			continue
		}
		a, b := e.phones[k[0]], e.phones[k[1]]
		if a == nil || b == nil || !a.placed || !b.placed || a.gone || b.gone {
			continue
		}
		if a.common > cfg.NearCommon || b.common > cfg.NearCommon {
			continue // everyone moves alike: no information about distance
		}
		out = append(out, clink{a, b, math.Min(1, ev.hits/ev.tests), true})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].a.idx != out[j].a.idx {
			return out[i].a.idx < out[j].a.idx
		}
		return out[i].b.idx < out[j].b.idx
	})
	return out
}

// rhythm is how periodic a phone's own trace is: its best rotation-free
// correlation with itself 0.4–1.2 s later. Swaying to music scores high; a
// push or a jostle doesn't repeat.
func (p *phone) rhythm() float64 {
	best := 0.0
	for lag := 4; lag <= 12; lag++ {
		var sxx, sxy, syx, syy, na, nb float64
		for k := 0; k+lag < trN; k++ {
			a1, a2, b1, b2 := p.tr1[k], p.tr2[k], p.tr1[k+lag], p.tr2[k+lag]
			sxx += a1 * b1
			sxy += a1 * b2
			syx += a2 * b1
			syy += a2 * b2
			na += a1*a1 + a2*a2
			nb += b1*b1 + b2*b2
		}
		if na <= 0 || nb <= 0 {
			continue
		}
		if r := math.Max(math.Hypot(sxx+syy, sxy-syx), math.Hypot(sxx-syy, sxy+syx)) / math.Sqrt(na*nb); r > best {
			best = r
		}
	}
	return best
}
