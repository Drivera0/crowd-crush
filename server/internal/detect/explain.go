package detect

import (
	"fmt"
	"math"

	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// lastStep keeps what the latest Step compared, so Explain can rebuild any
// pair's evidence on request instead of Step storing curves for every pair.
// The phones' resampled traces (h, v, valid) and RMS values stay in place
// until the next Step overwrites them; callers serialize access.
type lastStep struct {
	pairs      []pairRec
	edges      []Edge
	n, maxLag  int
	minOverlap int
}

// pairRec is one compared pair as Step saw it.
type pairRec struct {
	a, b         *phone
	handA, handB bool
	swayA, swayB float64
	walkA, walkB bool
	preChain     bool // passed every per-pair test (before the chain test)
	motion       bool // found by motion (a position only roughly known)
}

// Explain says why the latest step did or didn't call the neighbour pair
// (from, to) a travelling wave: both traces, the cross-correlation curve
// and every test. The order of from and to doesn't matter; the answer uses
// the detector's (From before To). ok is false when the pair was not a
// neighbour pair in the latest step.
func (d *Detector) Explain(from, to string) (protocol.EdgeExplain, bool) {
	cfg := &d.cfg
	ls := &d.last
	idx := -1
	for i, e := range ls.edges {
		if (e.From == from && e.To == to) || (e.From == to && e.To == from) {
			idx = i
			break
		}
	}
	if idx < 0 || idx >= len(ls.pairs) {
		return protocol.EdgeExplain{}, false
	}
	pr, e := ls.pairs[idx], ls.edges[idx]
	a, b := pr.a, pr.b
	if len(a.h) != ls.n || len(b.h) != ls.n || len(a.valid) != ls.n || len(b.valid) != ls.n {
		return protocol.EdgeExplain{}, false
	}
	out := protocol.EdgeExplain{From: e.From, To: e.To, StepMs: cfg.StepMs, LagMs: e.LagMs, Wave: e.Wave,
		A: trace(a.h, a.valid), B: trace(b.h, b.valid)}
	cs := corrCurve(a.h, b.h, a.valid, b.valid, ls.maxLag, ls.minOverlap, true)
	out.Lags = make([]int64, len(cs))
	out.Corr = make([]*float64, len(cs))
	for i, c := range cs {
		out.Lags[i] = int64(i-ls.maxLag) * cfg.StepMs
		if !math.IsNaN(c) {
			out.Corr[i] = fp(round3(c))
		}
	}
	lag, corr, second, ok := peaks(cs, ls.maxLag)
	if ok && pr.motion {
		second = math.Max(second, resolvedSecond(cs, lag, ls.maxLag))
	}
	if ok {
		out.Peak, out.Second = round3(corr), round3(math.Max(0, second))
		if e.LagMs == 0 && e.Corr == 0 { // not correlated in Step (gated): report the curve's own peak
			out.LagMs = int64(math.Round(lag * float64(cfg.StepMs)))
		}
	}

	add := func(name string, pass bool, detail string, args ...any) {
		out.Checks = append(out.Checks, protocol.Check{Name: name, Pass: pass, Detail: fmt.Sprintf(detail, args...)})
	}
	add("Both phones moving", pr.swayA >= cfg.EdgeMinSway && pr.swayB >= cfg.EdgeMinSway,
		"sway %.2f and %.2f m/s², need %.2f", pr.swayA, pr.swayB, cfg.EdgeMinSway)
	if pr.motion {
		add("Neighbours by motion", true, "positions known to ±%.0f m and ±%.0f m: compared because they move together, not because of where the map puts them", a.acc, b.acc)
	}
	switch {
	case pr.handA && pr.handB:
		add("Not handling", false, "both phones are being handled (rotation over %.0f°/s)", cfg.HandlingRot)
	case pr.handA || pr.handB:
		id := e.From
		if pr.handB {
			id = e.To
		}
		add("Not handling", false, "%s is being handled (rotation over %.0f°/s)", shortID(id), cfg.HandlingRot)
	default:
		add("Not handling", true, "neither phone is being handled")
	}
	if a.lev.on || b.lev.on { // only phones that send gravity are checked for walking
		switch {
		case pr.walkA && pr.walkB:
			add("Not walking", false, "both phones bounce and swing to a step rhythm (walking, phone in a pocket)")
		case pr.walkA || pr.walkB:
			id := e.From
			if pr.walkB {
				id = e.To
			}
			add("Not walking", false, "%s bounces and swings to a step rhythm (walking, phone in a pocket)", shortID(id))
		default:
			add("Not walking", true, "no step rhythm")
		}
	}
	if !ok {
		add("Strong correlation", false, "not enough overlapping readings to correlate")
		out.Checks = append(out.Checks, chainCheck(cfg, pr, e))
		return out, true
	}
	add("Strong correlation", corr >= cfg.CorrThreshold, "|r| %.2f, need %.2f", corr, cfg.CorrThreshold)
	al := abs64(out.LagMs)
	switch {
	case al < cfg.MinWaveLagMs:
		add("Wave-like lag", false, "%d ms: moving together (dancing, jumping), need %d–%d ms", al, cfg.MinWaveLagMs, cfg.MaxWaveLagMs)
	case al > cfg.MaxWaveLagMs:
		add("Wave-like lag", false, "%d ms: too slow for one person to the next, need %d–%d ms", al, cfg.MinWaveLagMs, cfg.MaxWaveLagMs)
	default:
		add("Wave-like lag", true, "%d ms: one person to the next", al)
	}
	gap := corr - second
	if second < 0 {
		add("Unambiguous peak", gap >= cfg.PeakMargin, "a single peak (|r| %.2f)", corr)
	} else {
		add("Unambiguous peak", gap >= cfg.PeakMargin, "peak %.2f vs next %.2f, need a %.2f gap", corr, second, cfg.PeakMargin)
	}
	vert := d.vertical(a, b, lag, ls.maxLag, ls.minOverlap)
	vr, hr := (a.vrms+b.vrms)/2, (a.hrms+b.hrms)/2
	if vert {
		add("Not vertical (Mexican-wave veto)", false, "vertical %.2f vs horizontal %.2f m/s², and it travels with the pair: people standing up in sequence", vr, hr)
	} else {
		add("Not vertical (Mexican-wave veto)", true, "vertical %.2f vs horizontal %.2f m/s²", vr, hr)
	}
	out.Checks = append(out.Checks, chainCheck(cfg, pr, e))
	return out, true
}

func chainCheck(cfg *Config, pr pairRec, e Edge) protocol.Check {
	name := fmt.Sprintf("Part of a chain of ≥ %d", cfg.MinChain)
	switch {
	case cfg.MinChain <= 2:
		return protocol.Check{Name: name, Pass: true, Detail: "chain test off"}
	case !pr.preChain:
		return protocol.Check{Name: name, Pass: false, Detail: "not a wave edge, so there is no chain to check"}
	case !e.Wave && pr.motion:
		return protocol.Check{Name: name, Pass: false, Detail: fmt.Sprintf("isolated: no run of %d phones hit one after the other (the lags don't add up)", cfg.MinChain)}
	case !e.Wave:
		return protocol.Check{Name: name, Pass: false, Detail: fmt.Sprintf("isolated: no run of %d phones travelling the same way", cfg.MinChain)}
	case pr.motion:
		return protocol.Check{Name: name, Pass: true, Detail: fmt.Sprintf("part of a run of %d or more phones hit one after the other (the lags add up)", cfg.MinChain)}
	}
	return protocol.Check{Name: name, Pass: true, Detail: fmt.Sprintf("part of a run of %d or more phones travelling the same way", cfg.MinChain)}
}

func trace(h []float64, valid []bool) []*float64 {
	out := make([]*float64, len(h))
	for i, v := range h {
		if valid[i] {
			out[i] = fp(round3(v))
		}
	}
	return out
}

func fp(v float64) *float64 { return &v }

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
