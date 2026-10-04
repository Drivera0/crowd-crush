package app

import (
	"github.com/Drivera0/crowd-crush/server/internal/crowd"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// The table demo profile (detect/table.go, docs/TABLE-DEMO.md) follows the
// demo spot: on while the demo spot is on, for the live pipeline only
// (replays and the simulation never use it). While it is on:
//
//   - the detector runs the profile in zones holding at most
//     Table.MaxPhones phones (faster smoothing and hold, a push between two
//     phones shown as yellow, "moving as one" as yellow);
//   - participation is taken as 1: everyone standing at the demo spot holds
//     a phone, so a row of judges 0.6 m apart is counted as the handful of
//     people it is, not as phones ÷ participation (at 0.6, four or five
//     judges along a wall read as "packed", yellow; at 0.3, red).
//
// Turning the demo spot off restores the configured participation.

// tableSyncLocked switches the live pipeline's profile to match the demo
// spot. Cheap when nothing changed. Caller holds mu.
func (a *App) tableSyncLocked() {
	p := a.live
	on := a.demo.On
	part := a.opt.Detect.Participation
	if on {
		part = 1
	}
	if p.det.Table() == on && p.cfg().Participation == part {
		return
	}
	p.det.SetTable(on)
	p.det.SetParticipation(part)
	p.crowd.SetConfig(crowd.ConfigFrom(p.cfg()))
}

// tableSnap adds the profile's output to a snapshot of pipeline p: which
// waves are two-phone pushes, and the groups moving as one.
func tableSnap(p *pipeline, s *protocol.Snapshot) {
	if !p.det.Table() {
		return
	}
	s.Table = true
	pair := map[[2]string]bool{}
	for _, e := range p.last.Edges {
		if e.Wave && e.Pair {
			pair[[2]string{e.From, e.To}], pair[[2]string{e.To, e.From}] = true, true
		}
	}
	for i, w := range s.Waves {
		s.Waves[i].Pair = pair[[2]string{w.From, w.To}]
	}
	for _, g := range p.last.Together {
		s.Together = append(s.Together, protocol.Together{Members: g.Members, LagMs: g.LagMs, Corr: round2(g.Corr), Since: g.Since})
	}
}

// tableCause says what is lifting zone zone of pipeline p under the table
// demo profile, for the wave alert's cause: "pair" when every wave touching
// the zone is a two-phone push, "together" when no wave touches it and a
// group moving as one does, "" otherwise (or with the profile off).
func tableCause(p *pipeline, zone string) string {
	if !p.det.Table() {
		return ""
	}
	pos := map[string][2]float64{}
	for _, ph := range p.last.Phones {
		pos[ph.ID] = [2]float64{ph.X, ph.Y}
	}
	in := func(id string) bool {
		q, ok := pos[id]
		if !ok {
			return false
		}
		for _, z := range p.det.ZonesOf(q[0], q[1]) {
			if z == zone {
				return true
			}
		}
		return false
	}
	waves, pairs, together := 0, 0, false
	for _, e := range p.last.Edges {
		if !in(e.From) && !in(e.To) {
			continue
		}
		if e.Wave {
			waves++
			if e.Pair {
				pairs++
			}
		}
		together = together || e.Together
	}
	switch {
	case waves > 0 && waves == pairs:
		return "pair"
	case waves == 0 && together:
		return "together"
	}
	return ""
}

// tableCauseLocked records on a live wave alert what raised it under the
// table demo profile (tableCause). A red incident is a crowd push, so it
// carries no cause; a change with no table cause keeps the one it had (the
// zone stays yellow a moment after the push or the group is gone). Caller
// holds mu.
func (a *App) tableCauseLocked(p *pipeline, al protocol.Alert, to string) protocol.Alert {
	if al.ID == "" || al.Kind != protocol.KindWave || to == protocol.LevelCalm {
		return al
	}
	c := tableCause(p, al.Zone)
	if al.Level == protocol.LevelRed {
		c = ""
	} else if c == "" {
		return al
	}
	if c == al.Cause {
		return al
	}
	out, ok := a.updateAlertLocked(al.ID, func(x *protocol.Alert, _ *incident) { x.Cause = c })
	if !ok {
		return al
	}
	return out
}
