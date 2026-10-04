// Package hub owns the WebSocket connections: phones stream motion in,
// dashboards receive broadcasts. It runs clock sync per phone and hands
// clock-corrected readings to a Handler.
package hub

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/Drivera0/crowd-crush/server/internal/clocksync"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// Clock sync schedule (see CLAUDE.md): 8 pings 100 ms apart on connect,
// again every 30 s.
const (
	PingSpacing   = 100 * time.Millisecond
	ResyncEvery   = 30 * time.Second
	burstSettle   = 400 * time.Millisecond
	helloTimeout  = 10 * time.Second
	readTimeout   = ResyncEvery + 15*time.Second // pongs arrive at least this often
	writeTimeout  = 3 * time.Second
	maxMessageLen = 8 << 10
)

// Handler receives phone events. Calls for one phone are sequential.
type Handler interface {
	// PhoneHello places a phone (venue metres) when it joins or re-sends hello.
	PhoneHello(id string, x, y float64, ua string)
	// PhoneHelloAuto is a hello that carried no position at all (no x/y,
	// no row/col): a GPS phone before its first fix, or any phone while
	// the demo spot lines them up. The handler places it or keeps it
	// unplaced; it must not be put on a default spot.
	PhoneHelloAuto(id, ua string)
	// PhoneHelloAt is a hello with at=<tower key>: the phone joined through
	// a tower's QR code and is placed next to that tower. False = unknown
	// or unplaced tower: the hub places the phone the normal way.
	PhoneHelloAt(id, at, ua string) bool
	// PhonePos moves a phone placed by hand (or a walking simulated phone).
	PhonePos(id string, x, y float64)
	// PhoneGPS delivers a raw fix. The handler converts it to venue metres
	// and must never store or log lat/lon.
	PhoneGPS(id string, lat, lon, acc float64)
	// LegacyPos maps an old grid cell (hello with row/col and no x/y) to
	// venue metres.
	LegacyPos(row, col int) (x, y float64)
	PhoneSync(id string, offset, rtt int64)
	// PhoneMotion gets a reading with T already clock-corrected.
	PhoneMotion(id string, m protocol.Motion, recv int64)
	PhoneGone(id string)
	// DashWelcome returns messages for a newly connected dashboard.
	DashWelcome() [][]byte
}

// Now is the server clock in ms.
func Now() int64 { return time.Now().UnixMilli() }

type conn struct {
	ws   *websocket.Conn
	send chan []byte
	done chan struct{}
	once sync.Once
}

func newConn(ws *websocket.Conn, buf int) *conn {
	return &conn{ws: ws, send: make(chan []byte, buf), done: make(chan struct{})}
}

func (c *conn) close() { c.once.Do(func() { close(c.done) }) }

// trySend queues a message; drops it if the client is too slow.
func (c *conn) trySend(b []byte) bool {
	select {
	case c.send <- b:
		return true
	case <-c.done:
		return false
	default:
		return false
	}
}

func (c *conn) writer(ctx context.Context) {
	for {
		select {
		case b := <-c.send:
			wctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := c.ws.Write(wctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				c.close()
				return
			}
		case <-c.done:
			return
		case <-ctx.Done():
			return
		}
	}
}

type phoneConn struct {
	*conn
	id   string
	ua   string
	sync *clocksync.Sync
	// lastRead is when the socket last delivered anything (hub clock, ms).
	lastRead atomic.Int64
	// relayLim limits what this phone may hand over for others (mesh.go).
	relayLim bucket
	// sentClock is the last offset told to the phone (mesh.go: "clock").
	sentClock atomic.Int64
}

// Hub tracks connected phones and dashboards.
type Hub struct {
	h Handler

	mu     sync.Mutex
	phones map[string]*phoneConn
	dashes map[*conn]struct{}
	// The mesh (mesh.go): phones reached through another phone, the public
	// handle of every phone seen, clocks kept for phones that come back
	// relayed, and signalling rate limits.
	relayed map[string]*relayed
	handles map[string]string
	clocks  map[string]savedClock
	sigLim  map[string]*bucket
	mesh    meshCounters

	motionMsgs atomic.Int64
}

// New creates a hub delivering events to h.
func New(h Handler) *Hub {
	return &Hub{h: h, phones: map[string]*phoneConn{}, dashes: map[*conn]struct{}{},
		relayed: map[string]*relayed{}, handles: map[string]string{}, clocks: map[string]savedClock{}, sigLim: map[string]*bucket{}}
}

// MotionCount is the total number of motion messages received.
func (hb *Hub) MotionCount() int64 { return hb.motionMsgs.Load() }

// PhoneCount is how many phones are connected right now (their own socket
// or relayed through the mesh).
func (hb *Hub) PhoneCount() int {
	hb.mu.Lock()
	defer hb.mu.Unlock()
	return len(hb.phones) + len(hb.relayed)
}

// DashCount is how many dashboards are connected right now.
func (hb *Hub) DashCount() int {
	hb.mu.Lock()
	defer hb.mu.Unlock()
	return len(hb.dashes)
}

// LastRTT returns the most recent ping RTT of a phone.
func (hb *Hub) LastRTT(id string) (int64, bool) {
	hb.mu.Lock()
	p, ok := hb.phones[id]
	hb.mu.Unlock()
	if !ok {
		return 0, false
	}
	return p.sync.LastRTT(), true
}

func accept(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	// Phones and dashboards come in through the tunnel's hostname, the LAN
	// address or localhost; origin checks would only get in the way here.
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(maxMessageLen)
	return ws, nil
}

// ServePhone handles /ws/phone.
func (hb *Hub) ServePhone(w http.ResponseWriter, r *http.Request) {
	ws, err := accept(w, r)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	hctx, hcancel := context.WithTimeout(ctx, helloTimeout)
	_, b, err := ws.Read(hctx)
	hcancel()
	if err != nil {
		return
	}
	var hello helloMsg
	if json.Unmarshal(b, &hello) != nil || hello.Type != protocol.TypeHello || hello.ID == "" || len(hello.ID) > 64 {
		ws.Close(websocket.StatusPolicyViolation, "expected hello")
		return
	}
	if len(hello.UA) > 40 {
		hello.UA = hello.UA[:40]
	}

	pc := &phoneConn{conn: newConn(ws, 32), id: hello.ID, ua: hello.UA, sync: &clocksync.Sync{}}
	pc.lastRead.Store(Now())
	pc.sentClock.Store(noClock)
	hb.mu.Lock()
	old := hb.phones[hello.ID]
	hb.phones[hello.ID] = pc
	// Its own socket takes over from any relayed path.
	_, wasRelayed := hb.relayed[hello.ID]
	delete(hb.relayed, hello.ID)
	hb.registerHandle(hello.ID)
	hb.mu.Unlock()
	if mh, ok := hb.h.(MeshHandler); ok && wasRelayed {
		mh.PhoneRoute(hello.ID, "", 0)
	}
	if old != nil { // same phone reconnected: the new socket wins
		old.close()
		old.ws.CloseNow()
	}
	defer func() {
		pc.close()
		hb.mu.Lock()
		current := hb.phones[hello.ID] == pc
		if current {
			hb.saveClock(pc)
			delete(hb.phones, hello.ID)
		}
		hb.mu.Unlock()
		if current {
			hb.h.PhoneGone(hello.ID)
		}
	}()

	hb.hello(hello, hello.UA)
	go pc.writer(ctx)
	go hb.syncLoop(ctx, pc)

	for {
		rctx, rcancel := context.WithTimeout(ctx, readTimeout)
		_, b, err := ws.Read(rctx)
		rcancel()
		if err != nil {
			return
		}
		select {
		case <-pc.done:
			return
		default:
		}
		now := Now()
		pc.lastRead.Store(now)
		typ, err := protocol.PeekType(b)
		if err != nil {
			continue
		}
		switch typ {
		case protocol.TypePong:
			var p protocol.Pong
			if json.Unmarshal(b, &p) == nil {
				pc.sync.Pong(p.T0, p.T1, now)
				if off, _, ok := pc.sync.Result(); ok {
					hb.h.PhoneSync(pc.id, off, pc.sync.LastRTT())
					hb.tellClock(pc, off)
				}
			}
		case protocol.TypeRelay:
			hb.relayIn(pc, b, now)
		case protocol.TypeJam:
			var j protocol.Jam
			if json.Unmarshal(b, &j) != nil {
				continue
			}
			if j.On {
				hb.yield(pc, now) // it will close this socket and go through the mesh
			} else if mh, ok := hb.h.(MeshHandler); ok {
				mh.PhoneJam(pc.id, false)
			}
		default:
			hb.phoneMsg(pc.id, hello.UA, typ, b, now, func(t int64) (int64, bool) {
				if _, _, ok := pc.sync.Result(); !ok {
					return 0, false // no clock yet: can't place this reading in time
				}
				return pc.sync.Correct(t), true
			})
		}
	}
}

// noClock: no offset has been told to the phone yet.
const noClock = int64(-1) << 62

// tellClock sends the phone its clock offset when it changed by more than
// a couple of milliseconds, so the traces phones exchange on the mesh share
// the server's time base.
func (hb *Hub) tellClock(pc *phoneConn, off int64) {
	if last := pc.sentClock.Load(); last != noClock && off-last <= 2 && last-off <= 2 {
		return
	}
	pc.sentClock.Store(off)
	if b, err := json.Marshal(protocol.Clock{Type: protocol.TypeClock, Offset: off}); err == nil {
		pc.trySend(b)
	}
}

// phoneMsg handles one message from phone id, whether it came on the phone's
// own socket or through a relay (mesh.go). correct turns a phone timestamp
// into server time; false = the phone's clock isn't known yet.
func (hb *Hub) phoneMsg(id, ua, typ string, b []byte, now int64, correct func(int64) (int64, bool)) {
	switch typ {
	case protocol.TypeMotion:
		var m protocol.Motion
		if json.Unmarshal(b, &m) != nil {
			return
		}
		hb.motionMsgs.Add(1)
		t, ok := correct(m.T)
		if !ok {
			return
		}
		m.T = t
		hb.h.PhoneMotion(id, m, now)
	case protocol.TypeHello:
		// Phone moved to a new spot without reconnecting.
		var h helloMsg
		if json.Unmarshal(b, &h) == nil && h.ID == id {
			hb.hello(h, ua)
		}
	case protocol.TypePos:
		var p protocol.Pos
		if json.Unmarshal(b, &p) == nil {
			hb.h.PhonePos(id, p.X, p.Y)
		}
	case protocol.TypeGPS:
		var g protocol.GPS
		if json.Unmarshal(b, &g) == nil {
			hb.h.PhoneGPS(id, g.Lat, g.Lon, g.Acc)
		}
	case protocol.TypeBeacons:
		hb.beacons(id, b) // Bluetooth beacon report (beacons.go)
	default:
		if !hb.dr(id, typ, b) { // the phone's own dead reckoning (locate.go)
			hb.meshMsg(id, typ, b, now)
		}
	}
}

// helloMsg is a hello as received: the legacy grid cell as pointers, so a
// phone that sent row/col (an old phone page: cell 0, 0 included) can be
// told from one that sent no position at all.
type helloMsg struct {
	protocol.Hello
	RowP *int `json:"row"`
	ColP *int `json:"col"`
	// At: the key of the tower whose QR code the phone joined through.
	At string `json:"at"`
}

// hello places the phone from x/y, else its legacy row/col; a hello with
// neither is handed over unplaced (it must not sit on a default cell,
// where a few of them would read as a crowd). Then a GPS fix, if the hello
// carried one.
func (hb *Hub) hello(h helloMsg, ua string) {
	switch {
	case h.At != "" && len(h.At) <= 16 && hb.h.PhoneHelloAt(h.ID, h.At, ua):
		// placed next to the tower
	case h.X != nil && h.Y != nil:
		hb.h.PhoneHello(h.ID, *h.X, *h.Y, ua)
	case h.RowP != nil || h.ColP != nil:
		row, col := 0, 0
		if h.RowP != nil {
			row = *h.RowP
		}
		if h.ColP != nil {
			col = *h.ColP
		}
		x, y := hb.h.LegacyPos(row, col)
		hb.h.PhoneHello(h.ID, x, y, ua)
	default:
		hb.h.PhoneHelloAuto(h.ID, ua)
	}
	if h.Lat != nil && h.Lon != nil {
		hb.h.PhoneGPS(h.ID, *h.Lat, *h.Lon, h.Acc)
	}
}

func (hb *Hub) syncLoop(ctx context.Context, pc *phoneConn) {
	for {
		for i := 0; i < clocksync.BurstSize; i++ {
			b, _ := json.Marshal(protocol.Ping{Type: protocol.TypePing, T0: Now()})
			pc.trySend(b)
			if !sleep(ctx, pc.done, PingSpacing) {
				return
			}
		}
		if !sleep(ctx, pc.done, burstSettle) {
			return
		}
		pc.sync.EndBurst()
		if off, rtt, ok := pc.sync.Result(); ok {
			hb.h.PhoneSync(pc.id, off, rtt)
			hb.tellClock(pc, off)
		}
		if !sleep(ctx, pc.done, ResyncEvery) {
			return
		}
	}
}

func sleep(ctx context.Context, done chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-done:
		return false
	case <-ctx.Done():
		return false
	}
}

// SendPhone sends a message to one phone if it is connected: on its own
// socket, or, for a relayed phone, wrapped in a relay envelope to the phone
// that last delivered for it (mesh.go).
func (hb *Hub) SendPhone(id string, v any) {
	hb.mu.Lock()
	p, ok := hb.phones[id]
	var via *phoneConn
	if !ok {
		if r := hb.relayed[id]; r != nil {
			via = hb.phones[r.via]
		}
	}
	hb.mu.Unlock()
	if !ok && via == nil {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	if ok {
		p.trySend(b)
		return
	}
	if env, err := json.Marshal(protocol.Relay{Type: protocol.TypeRelay, To: Handle(id), Msg: b}); err == nil {
		via.trySend(env)
	}
}

// PhoneIDs lists connected phones.
func (hb *Hub) PhoneIDs() []string {
	hb.mu.Lock()
	defer hb.mu.Unlock()
	ids := make([]string, 0, len(hb.phones))
	for id := range hb.phones {
		ids = append(ids, id)
	}
	return ids
}

// ServeDash handles /ws/dash. Dashboards only listen; controls go over HTTP.
func (hb *Hub) ServeDash(w http.ResponseWriter, r *http.Request) {
	ws, err := accept(w, r)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	c := newConn(ws, 64)
	for _, b := range hb.h.DashWelcome() {
		c.trySend(b)
	}
	hb.mu.Lock()
	hb.dashes[c] = struct{}{}
	hb.mu.Unlock()
	defer func() {
		c.close()
		hb.mu.Lock()
		delete(hb.dashes, c)
		hb.mu.Unlock()
	}()
	go c.writer(ctx)
	ctx = ws.CloseRead(ctx)
	select {
	case <-ctx.Done():
	case <-c.done:
	}
}

// Broadcast sends a message to every dashboard. Slow dashboards miss frames
// rather than holding everyone up.
func (hb *Hub) Broadcast(b []byte) {
	hb.mu.Lock()
	defer hb.mu.Unlock()
	for c := range hb.dashes {
		c.trySend(b)
	}
}

// BroadcastJSON marshals v and broadcasts it.
func (hb *Hub) BroadcastJSON(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("hub: marshal: %v", err)
		return
	}
	hb.Broadcast(b)
}
