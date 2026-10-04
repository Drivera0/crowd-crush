package detect

import (
	"fmt"
	"math"
	"sort"
)

// The table demo profile: two to five judges standing in a row at the demo
// spot, phones in their hands. Off unless the app turns it on (SetTable,
// while the demo spot is on, live phones only), and even then only in zones
// holding at most Table.MaxPhones active phones: a zone with a real crowd in
// it runs exactly as without the profile. Nothing in Config outside Table
// changes.
//
// What it changes in such a zone, and why (measurements: docs/TABLE-DEMO.md):
//
//   - Smoothing and hold. The zone score's 8 s EMA and 2 s hold are tuned to
//     ignore the odd shove in a crowd of hundreds, so even a clean push
//     travelling down a row of three takes ~15 s of repeated pushing to
//     reach red. With a handful of people at a table every hop is visible
//     and nothing is averaged away: Table.SmoothMs / Table.HoldMs (3 s, 1 s)
//     make a push travelling the row yellow within ~2–3 s and red once the
//     pushing keeps coming (a second push) within ~5–7 s. A single push
//     still decays: red needs the waves to repeat, as in a crowd. The wave
//     tests per pair (correlation, 120–1200 ms lag, unambiguous peak, the
//     vertical veto) and the chain of ≥ MinChain phones are unchanged, so
//     jumping or dancing together (zero lag, rhythmic) stays calm.
//   - Two phones. A chain needs three phones, so two judges can never make a
//     wave. A wave edge that is not part of a chain (Pair) still counts, but
//     a zone whose only waves are such edges is capped at Table.PairScore,
//     below redScore: "a push travelled from A to B" is yellow, never red,
//     and its phones aren't marked wave (red dots).
//   - Moving together. People pressed into each other stop moving on their
//     own: they are moved as one, with a small lag from the side the
//     pressure comes from. A pair moving that way for Table.TogetherHoldMs
//     (matching horizontal motion at |lag| ≤ TogetherMaxLagMs over the last
//     TogetherWindowMs, the lag steady, neither trace rhythmic, horizontal
//     stronger than vertical, both phones moving, not handled, not walking)
//     is Together, and lifts its zone to Table.TogetherScore (yellow, never
//     red). Dancing and jumping are moving together too, but to a beat:
//     the rhythm test (autocorrelation over lags up to TogetherRhythmLagMs,
//     long enough to see a 0.2 Hz ballad sway repeat) is what keeps them
//     out. It cannot tell pressure from people choosing to rock together
//     irregularly; that is why it is yellow and worded "moving as one".

// TogetherGroup is a set of phones moving as one (table demo profile).
type TogetherGroup struct {
	Members []string // sorted
	LagMs   int64    // largest |lag| between neighbours in the group
	Corr    float64  // weakest neighbour match in the group
	Since   int64    // when the longest-held pair in it started
}

type tableState struct {
	on  bool
	tog map[[2]string]*togState
	why map[[2]string]togWhy // latest step's "moving together" verdict per profile pair, for Explain
	// pairWhy: why a profile wave edge the chain test dropped was or wasn't
	// kept as a two-phone push, for Explain.
	pairWhy map[[2]string]togWhy
}

type togWhy struct {
	pass   bool
	detail string
}

// togState is one pair's "moving together" streak.
type togState struct {
	since int64   // when the streak began (0 = not moving together)
	lag0  float64 // lag (ms) when it began
	seen  uint64  // step it was last updated
	lag   float64 // latest lag (ms) and |r|
	corr  float64
}

// longTrace is a phone's resampled trace over Table.TogetherWindowMs.
type longTrace struct {
	h, v, sx, sz []float64
	valid        []bool
	hrms, vrms   float64
	rhythm       float64
}

// SetTable turns the table demo profile on or off. Off forgets every
// "moving together" streak.
func (d *Detector) SetTable(on bool) {
	if !on {
		d.tab = tableState{}
	}
	d.tab.on = on && d.cfg.Table.MaxPhones > 0
}

// SetParticipation changes Config.Participation, the share of the people
// assumed to carry a phone (people = phones ÷ participation). Values
// outside (0, 1] are ignored. The table demo uses 1: everyone standing at
// the demo spot is holding a phone.
func (d *Detector) SetParticipation(v float64) {
	if v > 0 && v <= 1 {
		d.cfg.Participation = v
	}
}

// Table reports whether the table demo profile is on.
func (d *Detector) Table() bool { return d.tab.on }

// tableZones marks the zones that run the profile this step (nil when it is
// off): at most Table.MaxPhones active phones inside. A phone runs it when
// all its zones do.
func (d *Detector) tableZones(spatial []*phone) []bool {
	for _, p := range spatial {
		p.table = false
	}
	if !d.tab.on {
		return nil
	}
	count := make([]int, len(d.zones))
	for _, p := range spatial {
		for _, zi := range p.zones {
			count[zi]++
		}
	}
	tz := make([]bool, len(d.zones))
	for i, c := range count {
		tz[i] = c <= d.cfg.Table.MaxPhones
	}
	for _, p := range spatial {
		p.table = len(p.zones) > 0
		for _, zi := range p.zones {
			p.table = p.table && tz[zi]
		}
	}
	return tz
}

// tablePairs keeps, as Pair edges, the wave edges the chain test dropped
// between the two phones of a group of exactly two (no third phone within
// neighbour range, so no chain is possible), when neither phone's motion
// is rhythmic over the long window (a slow sway to music, every person a
// little later than the last, passes the per-pair wave tests too; the
// chain test is what normally stops it). In a group of three or more the
// chain test stands: a push must reach three people.
func (d *Detector) tablePairs(edges []Edge, recs []pairRec, long func(*phone) *longTrace) {
	tc := &d.cfg.Table
	d.tab.pairWhy = map[[2]string]togWhy{}
	if tc.PairScore <= 0 {
		return
	}
	deg := map[*phone]int{}
	for i, e := range edges {
		if !e.Motion {
			deg[recs[i].a]++
			deg[recs[i].b]++
		}
	}
	for i := range edges {
		e := &edges[i]
		a, b := recs[i].a, recs[i].b
		if e.Wave || e.Motion || !recs[i].preChain || !a.table || !b.table {
			continue
		}
		key := [2]string{e.From, e.To}
		if deg[a] != 1 || deg[b] != 1 {
			d.tab.pairWhy[key] = togWhy{detail: "a third phone is in range, so the push has to reach it too (chain of three)"}
			continue
		}
		if r := math.Max(long(a).rhythm, long(b).rhythm); r >= tc.TogetherRhythm {
			d.tab.pairWhy[key] = togWhy{detail: fmt.Sprintf("only two phones, but moving to a beat (rhythm %.2f, need below %.2f): swaying to music, not a push", r, tc.TogetherRhythm)}
			continue
		}
		e.Wave, e.Pair = true, true
		d.tab.pairWhy[key] = togWhy{pass: true, detail: "only these two phones are here, so no chain is possible: shown as a push from one to the other, yellow at most"}
	}
}

// longTraces returns a function giving a phone's trace over the long
// window ending at end, worked out once per step.
func (d *Detector) longTraces(end int64) func(*phone) *longTrace {
	tc := &d.cfg.Table
	step := d.cfg.StepMs
	n := int(tc.TogetherWindowMs/step) + 1
	start := end - int64(n-1)*step
	rLag := int(tc.TogetherRhythmLagMs / step)
	minOverlap := int(math.Ceil(0.7 * float64(n-rLag)))
	done := map[*phone]bool{}
	return func(p *phone) *longTrace {
		if !done[p] {
			done[p] = true
			p.resampleLong(start, step, n, d.cfg.Axis)
			p.long.hrms, p.long.vrms = rms(p.long.h, p.long.valid), rms(p.long.v, p.long.valid)
			p.long.rhythm = rhythm(p.long.h, p.long.valid, rLag, minOverlap)
		}
		return &p.long
	}
}

// tableTogether finds the profile pairs moving as one, updates their
// streaks, marks the held ones Together and groups them. Every profile pair
// gets a verdict for Explain.
func (d *Detector) tableTogether(now int64, edges []Edge, recs []pairRec, long func(*phone) *longTrace) []TogetherGroup {
	tc := &d.cfg.Table
	d.tab.why = map[[2]string]togWhy{}
	if tc.TogetherScore <= 0 {
		return nil
	}
	if d.tab.tog == nil {
		d.tab.tog = map[[2]string]*togState{}
	}
	maxLag := int(tc.TogetherMaxLagMs / d.cfg.StepMs)
	var held []int
	for i := range edges {
		e := &edges[i]
		pr := recs[i]
		a, b := pr.a, pr.b
		if e.Motion || !a.table || !b.table {
			continue
		}
		key := [2]string{e.From, e.To}
		ok, lag, corr, why := false, 0.0, 0.0, ""
		switch {
		case e.Wave:
			why = "carrying a push from one to the other (a push is not moving together)"
		case pr.handA || pr.handB || a.blind || b.blind:
			why = "a phone is being handled or missing readings"
		case pr.walkA || pr.walkB:
			why = "walking (step rhythm)"
		case pr.swayA < d.cfg.EdgeMinSway || pr.swayB < d.cfg.EdgeMinSway:
			why = fmt.Sprintf("not moving enough (sway %.2f and %.2f m/s², need %.2f)", pr.swayA, pr.swayB, d.cfg.EdgeMinSway)
		default:
			ok, lag, corr, why = d.togetherNow(long(a), long(b), maxLag)
		}
		st := d.tab.tog[key]
		if !ok {
			if st != nil {
				st.since = 0
			}
			d.tab.why[key] = togWhy{detail: why}
			continue
		}
		if st == nil {
			st = &togState{}
			d.tab.tog[key] = st
		}
		st.seen, st.lag, st.corr = d.seq, lag, corr
		if st.since == 0 || math.Abs(lag-st.lag0) > float64(tc.TogetherJitterMs) {
			st.since, st.lag0 = now, lag // a new streak (or the lag wandered: start over)
		}
		heldMs := now - st.since
		if heldMs >= tc.TogetherHoldMs {
			e.Together = true
			held = append(held, i)
			d.tab.why[key] = togWhy{pass: true, detail: fmt.Sprintf("%s, for %.0f s: moved as one, as people pressed together are (or rocking together by choice: motion can't tell)", why, float64(heldMs)/1000)}
		} else {
			d.tab.why[key] = togWhy{detail: fmt.Sprintf("%s, but only for %.1f s of the %.0f s needed", why, float64(heldMs)/1000, float64(tc.TogetherHoldMs)/1000)}
		}
	}
	for k, st := range d.tab.tog {
		if st.seen != d.seq {
			delete(d.tab.tog, k)
		}
	}
	return d.togetherGroups(edges, held)
}

// togetherNow tests two long traces: both not rhythmic, horizontal stronger
// than vertical, and a match of at least TogetherCorr at |lag| ≤ maxLag
// steps. Returns the lag (ms) and correlation of the best match, and in
// words what it found.
func (d *Detector) togetherNow(a, b *longTrace, maxLag int) (ok bool, lagMs, corr float64, why string) {
	tc := &d.cfg.Table
	win := float64(tc.TogetherWindowMs) / 1000
	if r := math.Max(a.rhythm, b.rhythm); r >= tc.TogetherRhythm {
		return false, 0, 0, fmt.Sprintf("moving to a beat (rhythm %.2f, need below %.2f): dancing, jumping or swaying to music", r, tc.TogetherRhythm)
	}
	if a.vrms > a.hrms || b.vrms > b.hrms {
		return false, 0, 0, fmt.Sprintf("mostly up and down (vertical %.2f vs horizontal %.2f m/s²)", (a.vrms+b.vrms)/2, (a.hrms+b.hrms)/2)
	}
	n := len(a.h)
	ml := max(maxLag, 1)
	cs := corrCurve(a.h, b.h, a.valid, b.valid, ml, int(math.Ceil(0.7*float64(n-ml))), true)
	lag, c, _, found := peaks(cs, ml)
	if !found {
		return false, 0, 0, "not enough readings over the window"
	}
	lagMs = lag * float64(d.cfg.StepMs)
	if c < tc.TogetherCorr {
		return false, lagMs, c, fmt.Sprintf("|r| %.2f within ±%d ms over the last %.0f s, need %.2f", c, tc.TogetherMaxLagMs, win, tc.TogetherCorr)
	}
	return true, lagMs, c, fmt.Sprintf("|r| %.2f at %.0f ms over the last %.0f s, not to a beat (rhythm %.2f)", c, math.Abs(lagMs), win, math.Max(a.rhythm, b.rhythm))
}

// togetherGroups joins the held pairs into connected groups.
func (d *Detector) togetherGroups(edges []Edge, held []int) []TogetherGroup {
	if len(held) == 0 {
		return nil
	}
	parent := map[string]string{}
	var find func(string) string
	find = func(x string) string {
		if parent[x] == x {
			return x
		}
		parent[x] = find(parent[x])
		return parent[x]
	}
	for _, i := range held {
		for _, id := range []string{edges[i].From, edges[i].To} {
			if _, ok := parent[id]; !ok {
				parent[id] = id
			}
		}
		parent[find(edges[i].From)] = find(edges[i].To)
	}
	byRoot := map[string]*TogetherGroup{}
	var roots []string
	for _, i := range held {
		e := edges[i]
		r := find(e.From)
		g := byRoot[r]
		if g == nil {
			g = &TogetherGroup{Corr: 1, Since: math.MaxInt64}
			byRoot[r] = g
			roots = append(roots, r)
		}
		st := d.tab.tog[[2]string{e.From, e.To}]
		g.LagMs = max(g.LagMs, int64(math.Round(math.Abs(st.lag))))
		g.Since = min(g.Since, st.since)
		g.Corr = math.Min(g.Corr, st.corr)
	}
	for id := range parent {
		g := byRoot[find(id)]
		g.Members = append(g.Members, id)
	}
	out := make([]TogetherGroup, 0, len(roots))
	for _, r := range roots {
		g := byRoot[r]
		sort.Strings(g.Members)
		g.Corr = round3(g.Corr)
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Members[0] < out[j].Members[0] })
	return out
}

// resampleLong fills p.long over a window of n grid points from start,
// leaving the step's own traces (h, v, valid …) as they were.
func (p *phone) resampleLong(start, step int64, n int, axis string) {
	h, v, sx, sz, valid := p.h, p.v, p.sx, p.sz, p.valid
	p.h, p.v, p.sx, p.sz, p.valid = p.long.h, p.long.v, p.long.sx, p.long.sz, p.long.valid
	p.resample(start, step, n, axis)
	p.long.h, p.long.v, p.long.sx, p.long.sz, p.long.valid = p.h, p.v, p.sx, p.sz, p.valid
	p.h, p.v, p.sx, p.sz, p.valid = h, v, sx, sz, valid
}
