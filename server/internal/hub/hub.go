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

// Clock sync schedule (see docs/REFERENCE.md): 8 pings 100 ms apart on connect,
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
	PhoneHello(id string, row, col int, ua string)
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
	sync *clocksync.Sync
}

// Hub tracks connected phones and dashboards.
type Hub struct {
	h Handler

	mu     sync.Mutex
	phones map[string]*phoneConn
	dashes map[*conn]struct{}

	motionMsgs atomic.Int64
}

// New creates a hub delivering events to h.
func New(h Handler) *Hub {
	return &Hub{h: h, phones: map[string]*phoneConn{}, dashes: map[*conn]struct{}{}}
}

// MotionCount is the total number of motion messages received.
func (hb *Hub) MotionCount() int64 { return hb.motionMsgs.Load() }

// PhoneCount is how many phones are connected right now.
func (hb *Hub) PhoneCount() int {
	hb.mu.Lock()
	defer hb.mu.Unlock()
	return len(hb.phones)
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
	var hello protocol.Hello
	if json.Unmarshal(b, &hello) != nil || hello.Type != protocol.TypeHello || hello.ID == "" || len(hello.ID) > 64 {
		ws.Close(websocket.StatusPolicyViolation, "expected hello")
		return
	}
	if len(hello.UA) > 40 {
		hello.UA = hello.UA[:40]
	}

	pc := &phoneConn{conn: newConn(ws, 32), id: hello.ID, sync: &clocksync.Sync{}}
	hb.mu.Lock()
	old := hb.phones[hello.ID]
	hb.phones[hello.ID] = pc
	hb.mu.Unlock()
	if old != nil { // same phone reconnected: the new socket wins
		old.close()
		old.ws.CloseNow()
	}
	defer func() {
		pc.close()
		hb.mu.Lock()
		current := hb.phones[hello.ID] == pc
		if current {
			delete(hb.phones, hello.ID)
		}
		hb.mu.Unlock()
		if current {
			hb.h.PhoneGone(hello.ID)
		}
	}()

	hb.h.PhoneHello(hello.ID, hello.Row, hello.Col, hello.UA)
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
				}
			}
		case protocol.TypeMotion:
			var m protocol.Motion
			if json.Unmarshal(b, &m) != nil {
				continue
			}
			hb.motionMsgs.Add(1)
			if _, _, ok := pc.sync.Result(); !ok {
				continue // no clock yet: can't place this reading in time
			}
			m.T = pc.sync.Correct(m.T)
			hb.h.PhoneMotion(pc.id, m, now)
		case protocol.TypeHello:
			// Phone moved to a new spot without reconnecting.
			var h protocol.Hello
			if json.Unmarshal(b, &h) == nil && h.ID == pc.id {
				hb.h.PhoneHello(h.ID, h.Row, h.Col, hello.UA)
			}
		}
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

// SendPhone sends a message to one phone if it is connected.
func (hb *Hub) SendPhone(id string, v any) {
	hb.mu.Lock()
	p, ok := hb.phones[id]
	hb.mu.Unlock()
	if !ok {
		return
	}
	b, err := json.Marshal(v)
	if err == nil {
		p.trySend(b)
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
