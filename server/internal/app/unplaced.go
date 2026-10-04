package app

import (
	"log"

	"github.com/Drivera0/crowd-crush/server/internal/store"
)

// Unplaced phones.
//
// A hello may carry no position at all: a GPS phone says hello before its
// first fix, and the fix may never be good enough (accuracy worse than
// gpsMaxAcc is ignored). Such a phone is connected and streaming but it is
// nowhere: it must not be drawn or counted at some default spot, where a
// handful of them stack up and read as a crush.
//
// So it is unplaced (nodeMeta.unplaced, Node.unplaced on the wire): the
// detector holds it as outside the venue (no zone, no cluster, no
// neighbours, no density, no guidance), the dashboard keeps it off the map
// ("locating…"), and nothing about it goes into a recording or continuous
// storage. The first position that arrives places it: an accepted GPS fix,
// a pos from the phone's own map, a hello with x/y, staff dragging its dot
// or lining phones up at the demo spot. That moment is recorded as its
// hello, so a replay sees the phone appear where it really was.
//
// Legacy hellos (row/col, old phone pages) and old recordings still land
// on their grid cell: only a hello with neither x/y nor row/col is unplaced.

// helloUnplacedIn is a hello without a position in pipeline p. A phone the
// pipeline already knows keeps its position (or stays unplaced). Caller
// holds mu.
func (a *App) helloUnplacedIn(p *pipeline, now int64, id, ua string) {
	m := p.meta[id]
	if m == nil {
		m = &nodeMeta{joinedAt: now, unplaced: true}
		p.meta[id] = m
		if p == a.live {
			log.Printf("phone %s joined, not located yet (%s)", short(id), ua)
		}
	}
	m.ua, m.connected, m.goneAt = ua, true, 0
	m.lastRecv = now
	p.place(id, m)
}

// placedLocked marks a phone that just got its first position (m.x, m.y,
// m.acc and m.outside already set) as placed, and records it as a hello.
// It reports whether the phone was unplaced. Caller holds mu.
func (a *App) placedLocked(p *pipeline, now int64, id string, m *nodeMeta) bool {
	if !m.unplaced {
		return false
	}
	m.unplaced = false
	p.place(id, m)
	a.record(store.Record{K: store.KindHello, T: now, ID: id, X: store.F(r2(m.x)), Y: store.F(r2(m.y)),
		Acc: m.acc, Out: m.outside, UA: m.ua})
	if m.synced {
		a.record(store.Record{K: store.KindSync, T: now, ID: id, RTT: m.rtt, Offset: m.offset})
	}
	return true
}
