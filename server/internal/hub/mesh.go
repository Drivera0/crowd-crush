package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync/atomic"

	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// The phone-to-phone mesh, as far as the hub is concerned:
//
//   - Signalling: a phone's "sig" (offer / answer / ICE candidate) is passed
//     to the peer it names, if the handler paired the two.
//   - Relay: a phone with a socket hands over messages it carried for a phone
//     without one ({"type":"relay","from":<handle>,"hops":n,"msg":{…}}).
//     The inner message goes through the same dispatch as a direct one, as
//     coming from that phone. Messages for a relayed phone go back down
//     through the phone that last delivered for it.
//   - Jam (the lost-signal demo): a phone announces it is about to drop its
//     socket ({"type":"jam","on":true}); it becomes a relayed phone at once,
//     so it never reads as gone in between.
//
// Phones are named to each other by handle (Handle), never by session id.
// Nothing is authenticated (nothing is on the direct path either): the hub
// checks shape, size, rate and loops, and a phone's own live socket always
// wins over a relayed copy.

const (
	handleLen = 12
	// maxRelayMsg caps the inner message of a relay envelope.
	maxRelayMsg = 4 << 10
	// A relayed phone may deliver this many messages a second (10 Hz motion,
	// 1 Hz near, positions, signalling), with a burst on top.
	relayRate  = 40.0
	relayBurst = 80.0
	// A relayer may hand over this many a second in total.
	relayerRate  = 300.0
	relayerBurst = 600.0
	// sigRate limits signalling messages per sender.
	sigRate  = 30.0
	sigBurst = 120.0
	// directFreshMs: relayed copies are dropped while the phone's own socket
	// delivered something this recently.
	directFreshMs = 2000
	// relayGoneMs: a relayed phone nobody delivered for in this long is gone.
	relayGoneMs = 6000
	// maxHandles caps the handle → id memory.
	maxHandles = 20000
)

// Handle is a phone's public name on the mesh: the first 12 hex characters
// of SHA-256(session id). Peers and relays see only this.
func Handle(id string) string {
	s := sha256.Sum256([]byte(id))
	return hex.EncodeToString(s[:])[:handleLen]
}

// MeshHandler is the optional part of a Handler that deals with the mesh.
// A Handler without it gets no mesh: signalling and relays are dropped.
type MeshHandler interface {
	// PhoneRTC: the phone can (or no longer can) open WebRTC links.
	PhoneRTC(id string, on bool)
	// PhoneNear: the phone's direct links, peer handles already resolved to
	// session ids (unknown ones dropped).
	PhoneNear(id string, n protocol.Near)
	// PhoneMPos: the phone's mesh-corrected position estimate.
	PhoneMPos(id string, p protocol.MPos)
	// MeshPair reports whether a and b are paired (may exchange signalling).
	MeshPair(a, b string) bool
	// PhoneRoute: how the phone's messages reach the server from now on.
	// via "" = its own socket; else the session id of the delivering phone.
	PhoneRoute(id, via string, hops int)
	// PhoneJam: the phone dropped its socket on purpose (on), or said it
	// can't (off).
	PhoneJam(id string, on bool)
}

// bucket is a token bucket on the hub's clock (ms).
type bucket struct {
	tokens float64
	at     int64
}

func (b *bucket) take(now int64, rate, burst float64) bool {
	if b.at == 0 {
		b.tokens = burst
	} else if now > b.at {
		b.tokens = min(burst, b.tokens+rate*float64(now-b.at)/1000)
	}
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// relayClock turns a relayed phone's timestamps into server time: with the
// offset its own socket measured, or, for a phone the server never synced,
// an estimate from the messages themselves (t − arrival is offset − delay,
// so the largest seen is the offset, give or take the shortest delay).
type relayClock struct {
	off   int64
	exact bool
	have  bool
}

func (c *relayClock) correct(t, now int64) int64 {
	if !c.exact {
		if d := t - now; !c.have || d > c.off {
			c.off, c.have = d, true
		}
	}
	return t - c.off
}

// relayed is a phone reached through another phone.
type relayed struct {
	via    string // session id of the phone that last delivered for it; "" = announced, nothing delivered yet
	hops   int
	last   int64
	clock  relayClock
	rtt    int64
	synced bool // PhoneSync has been called for it
	lim    bucket
	ua     string
}

type savedClock struct {
	off, rtt int64
}

// meshCounters are totals since start.
type meshCounters struct {
	relayIn, relayDropped, sigs atomic.Int64
}

// RelayCounts is how many relayed messages were accepted and dropped.
func (hb *Hub) RelayCounts() (in, dropped int64) {
	return hb.mesh.relayIn.Load(), hb.mesh.relayDropped.Load()
}

// Route reports how a phone reaches the server: ok false = not connected;
// via "" = its own socket.
func (hb *Hub) Route(id string) (via string, hops int, ok bool) {
	hb.mu.Lock()
	defer hb.mu.Unlock()
	if _, direct := hb.phones[id]; direct {
		return "", 0, true
	}
	if r := hb.relayed[id]; r != nil {
		return r.via, r.hops, true
	}
	return "", 0, false
}

// registerHandle remembers a phone's handle. Caller holds mu.
func (hb *Hub) registerHandle(id string) {
	if len(hb.handles) >= maxHandles {
		hb.handles = map[string]string{}
		for pid := range hb.phones {
			hb.handles[Handle(pid)] = pid
		}
		for pid := range hb.relayed {
			hb.handles[Handle(pid)] = pid
		}
	}
	hb.handles[Handle(id)] = id
}

func (hb *Hub) resolve(handle string) (string, bool) {
	if len(handle) != handleLen {
		return "", false
	}
	hb.mu.Lock()
	id, ok := hb.handles[handle]
	hb.mu.Unlock()
	return id, ok
}

// saveClock keeps a phone's measured offset for when it comes back relayed.
// Caller holds mu.
func (hb *Hub) saveClock(pc *phoneConn) {
	if off, rtt, ok := pc.sync.Result(); ok {
		if len(hb.clocks) >= maxHandles {
			hb.clocks = map[string]savedClock{}
		}
		hb.clocks[pc.id] = savedClock{off, rtt}
	}
}

// newRelayed makes the relayed entry for a phone. Caller holds mu.
func (hb *Hub) newRelayed(id, via string, hops int, now int64) *relayed {
	r := &relayed{via: via, hops: hops, last: now}
	if c, ok := hb.clocks[id]; ok {
		r.clock = relayClock{off: c.off, exact: true, have: true}
		r.rtt = c.rtt
	}
	hb.relayed[id] = r
	return r
}

// yield is a phone announcing it will drop its socket and go through the
// mesh: it turns into a relayed phone now, so closing the socket is not
// "gone". The sweep drops it if nothing ever arrives for it.
func (hb *Hub) yield(pc *phoneConn, now int64) {
	hb.mu.Lock()
	if hb.phones[pc.id] != pc {
		hb.mu.Unlock()
		return
	}
	hb.saveClock(pc)
	delete(hb.phones, pc.id)
	r := hb.newRelayed(pc.id, "", 0, now)
	r.synced = true // the handler has its clock from the socket
	r.ua = pc.ua
	hb.mu.Unlock()
	if mh, ok := hb.h.(MeshHandler); ok {
		mh.PhoneJam(pc.id, true)
	}
}

var relayable = map[string]bool{
	protocol.TypeHello: true, protocol.TypeMotion: true, protocol.TypePos: true, // never gps: raw coordinates do not pass through other phones
	protocol.TypeNear: true, protocol.TypeSignal: true, protocol.TypeRTC: true, protocol.TypeMPos: true, protocol.TypeBeacons: true, protocol.TypeDR: true,
}

// relayIn handles a relay envelope delivered by pc.
func (hb *Hub) relayIn(pc *phoneConn, b []byte, now int64) {
	mh, ok := hb.h.(MeshHandler)
	if !ok {
		return
	}
	drop := func() { hb.mesh.relayDropped.Add(1) }
	var env protocol.Relay
	if json.Unmarshal(b, &env) != nil || env.Hops < 1 || env.Hops > protocol.MaxRelayHops ||
		len(env.Msg) < 2 || len(env.Msg) > maxRelayMsg {
		drop()
		return
	}
	id, known := hb.resolve(env.From)
	if !known || id == pc.id { // a phone never relays for itself: that is a loop
		drop()
		return
	}
	typ, err := protocol.PeekType(env.Msg)
	if err != nil || !relayable[typ] { // no nested relays, no pongs
		drop()
		return
	}

	hb.mu.Lock()
	if !pc.relayLim.take(now, relayerRate, relayerBurst) {
		hb.mu.Unlock()
		drop()
		return
	}
	var old *phoneConn
	if d := hb.phones[id]; d != nil {
		if now-d.lastRead.Load() < directFreshMs {
			hb.mu.Unlock()
			drop() // its own socket is alive: that copy wins
			return
		}
		// Its socket has gone quiet and it is asking through a neighbour:
		// the relayed path takes over.
		hb.saveClock(d)
		delete(hb.phones, id)
		old = d
	}
	r := hb.relayed[id]
	created := r == nil
	if created {
		if typ != protocol.TypeHello && old == nil {
			hb.mu.Unlock()
			drop() // a phone the server doesn't hold: it must say hello first
			return
		}
		r = hb.newRelayed(id, "", 0, now)
		if old != nil {
			r.synced, r.ua = true, old.ua
		}
	}
	if !r.lim.take(now, relayRate, relayBurst) {
		hb.mu.Unlock()
		drop()
		return
	}
	routeChanged := r.via != pc.id || r.hops != env.Hops
	r.via, r.hops, r.last = pc.id, env.Hops, now
	clock := r.clock
	needSync := !r.synced && clock.exact
	if needSync {
		r.synced = true
	}
	ua := r.ua
	hb.mu.Unlock()

	if old != nil {
		old.close()
		old.ws.CloseNow()
	}
	hb.mesh.relayIn.Add(1)
	if typ == protocol.TypeHello {
		// The envelope names the phone; the inner hello carries no id (the
		// relaying phones must not learn it).
		var h helloMsg
		if json.Unmarshal(env.Msg, &h) != nil {
			return
		}
		h.ID = id
		h.Lat, h.Lon = nil, nil // raw GPS never comes through another phone
		if len(h.UA) > 40 {
			h.UA = h.UA[:40]
		}
		if h.UA == "" {
			h.UA = ua
		}
		hb.mu.Lock()
		if cur := hb.relayed[id]; cur != nil {
			cur.ua = h.UA
		}
		hb.mu.Unlock()
		hb.hello(h, h.UA)
	}
	if needSync {
		hb.h.PhoneSync(id, clock.off, r.rtt)
	}
	if routeChanged {
		mh.PhoneRoute(id, pc.id, env.Hops)
	}
	if typ == protocol.TypeHello {
		return
	}
	correct := func(t int64) (int64, bool) {
		hb.mu.Lock()
		cur := hb.relayed[id]
		if cur == nil {
			hb.mu.Unlock()
			return 0, false
		}
		ct := cur.clock.correct(t, now)
		first := !cur.synced
		cur.synced = true
		off := cur.clock.off
		hb.mu.Unlock()
		if first {
			hb.h.PhoneSync(id, off, 0)
		}
		return ct, true
	}
	hb.phoneMsg(id, ua, typ, env.Msg, now, correct)
}

// meshMsg handles the mesh message types that a phone may send on its own
// socket or through a relay. It reports whether typ was one of them.
func (hb *Hub) meshMsg(id, typ string, b []byte, now int64) bool {
	switch typ {
	case protocol.TypeRTC, protocol.TypeNear, protocol.TypeSignal, protocol.TypeMPos:
	default:
		return false
	}
	mh, ok := hb.h.(MeshHandler)
	if !ok {
		return true
	}
	switch typ {
	case protocol.TypeRTC:
		var m protocol.RTC
		if json.Unmarshal(b, &m) == nil {
			mh.PhoneRTC(id, m.On)
		}
	case protocol.TypeMPos:
		var m protocol.MPos
		if json.Unmarshal(b, &m) == nil {
			mh.PhoneMPos(id, m)
		}
	case protocol.TypeNear:
		var n protocol.Near
		if json.Unmarshal(b, &n) != nil || len(n.Peers) > 16 || len(n.Failed) > 16 {
			return true
		}
		peers := n.Peers[:0]
		for _, p := range n.Peers {
			if pid, ok := hb.resolve(p.ID); ok && pid != id {
				p.ID = pid
				peers = append(peers, p)
			}
		}
		n.Peers = peers
		failed := n.Failed[:0]
		for _, f := range n.Failed {
			if pid, ok := hb.resolve(f); ok && pid != id {
				failed = append(failed, pid)
			}
		}
		n.Failed = failed
		mh.PhoneNear(id, n)
	case protocol.TypeSignal:
		var s protocol.Signal
		if json.Unmarshal(b, &s) != nil || len(s.Data) == 0 {
			return true
		}
		if s.Kind != protocol.SigOffer && s.Kind != protocol.SigAnswer && s.Kind != protocol.SigICE && s.Kind != protocol.SigHi {
			return true
		}
		to, ok := hb.resolve(s.To)
		if !ok || to == id || !mh.MeshPair(id, to) {
			return true
		}
		hb.mu.Lock()
		lim := hb.sigLim[id]
		if lim == nil {
			if len(hb.sigLim) >= maxHandles {
				hb.sigLim = map[string]*bucket{}
			}
			lim = &bucket{}
			hb.sigLim[id] = lim
		}
		allowed := lim.take(now, sigRate, sigBurst)
		hb.mu.Unlock()
		if !allowed {
			return true
		}
		hb.mesh.sigs.Add(1)
		hb.SendPhone(to, protocol.Signal{Type: protocol.TypeSignal, From: Handle(id), Kind: s.Kind, Data: s.Data})
	}
	return true
}

// SweepRelayed drops relayed phones nobody has delivered for lately. Call
// it about once a second.
func (hb *Hub) SweepRelayed(now int64) {
	var gone []string
	hb.mu.Lock()
	for id, r := range hb.relayed {
		if now-r.last > relayGoneMs {
			delete(hb.relayed, id)
			gone = append(gone, id)
		}
	}
	hb.mu.Unlock()
	for _, id := range gone {
		hb.h.PhoneGone(id)
	}
}
