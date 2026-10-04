package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// fakeHandler records what the hub delivers.
type fakeHandler struct {
	mu     sync.Mutex
	events []string
	motion map[string][]protocol.Motion
	near   map[string]protocol.Near
	pairs  map[[2]string]bool
}

func newFake() *fakeHandler {
	return &fakeHandler{motion: map[string][]protocol.Motion{}, near: map[string]protocol.Near{}, pairs: map[[2]string]bool{}}
}

func (f *fakeHandler) ev(format string, a ...any) {
	f.mu.Lock()
	f.events = append(f.events, fmt.Sprintf(format, a...))
	f.mu.Unlock()
}

func (f *fakeHandler) has(e string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, x := range f.events {
		if x == e {
			return true
		}
	}
	return false
}

func (f *fakeHandler) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, x := range f.events {
		if strings.HasPrefix(x, prefix) {
			n++
		}
	}
	return n
}

func (f *fakeHandler) motions(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.motion[id])
}

func (f *fakeHandler) PhoneHello(id string, x, y float64, ua string) {
	f.ev("hello %s %.1f %.1f", id, x, y)
}
func (f *fakeHandler) PhoneHelloAuto(id, ua string)              { f.ev("helloauto %s", id) }
func (f *fakeHandler) PhoneHelloAt(id, at, ua string) bool       { return false }
func (f *fakeHandler) PhonePos(id string, x, y float64)          { f.ev("pos %s %.1f %.1f", id, x, y) }
func (f *fakeHandler) PhoneGPS(id string, lat, lon, acc float64) {}
func (f *fakeHandler) LegacyPos(row, col int) (x, y float64)     { return 0, 0 }
func (f *fakeHandler) PhoneSync(id string, offset, rtt int64)    { f.ev("sync %s", id) }
func (f *fakeHandler) PhoneGone(id string)                       { f.ev("gone %s", id) }
func (f *fakeHandler) DashWelcome() [][]byte                     { return nil }
func (f *fakeHandler) PhoneRTC(id string, on bool)               { f.ev("rtc %s %v", id, on) }
func (f *fakeHandler) PhoneMPos(id string, p protocol.MPos)      { f.ev("mpos %s %.1f %.1f", id, p.X, p.Y) }
func (f *fakeHandler) PhoneRoute(id, via string, hops int) {
	f.ev("route %s via %s hops %d", id, via, hops)
}
func (f *fakeHandler) PhoneJam(id string, on bool) { f.ev("jam %s %v", id, on) }
func (f *fakeHandler) PhoneMotion(id string, m protocol.Motion, recv int64) {
	f.mu.Lock()
	f.motion[id] = append(f.motion[id], m)
	f.mu.Unlock()
}
func (f *fakeHandler) PhoneNear(id string, n protocol.Near) {
	f.mu.Lock()
	f.near[id] = n
	f.mu.Unlock()
	f.ev("near %s", id)
}
func (f *fakeHandler) MeshPair(a, b string) bool {
	if a > b {
		a, b = b, a
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pairs[[2]string{a, b}]
}

type testPhone struct {
	t  *testing.T
	id string
	ws *websocket.Conn
	// every non-ping message received, in order
	mu   sync.Mutex
	msgs []map[string]any
}

type rig struct {
	t   *testing.T
	f   *fakeHandler
	hb  *Hub
	srv *httptest.Server
	ctx context.Context
}

func newRig(t *testing.T) *rig {
	t.Helper()
	f := newFake()
	hb := New(f)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws/phone", hb.ServePhone)
	srv := httptest.NewServer(mux)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(func() { cancel(); srv.Close() })
	return &rig{t: t, f: f, hb: hb, srv: srv, ctx: ctx}
}

// join connects a phone, answers its pings (so its clock syncs) and
// collects everything else it is sent.
func (r *rig) join(id string) *testPhone {
	r.t.Helper()
	ws, _, err := websocket.Dial(r.ctx, "ws"+strings.TrimPrefix(r.srv.URL, "http")+"/ws/phone", nil)
	if err != nil {
		r.t.Fatal(err)
	}
	p := &testPhone{t: r.t, id: id, ws: ws}
	p.send(map[string]any{"type": "hello", "id": id, "x": 1, "y": 1, "ua": "test"})
	go func() {
		for {
			_, b, err := ws.Read(r.ctx)
			if err != nil {
				return
			}
			var m map[string]any
			if json.Unmarshal(b, &m) != nil {
				continue
			}
			if m["type"] == "ping" {
				p.send(map[string]any{"type": "pong", "t0": m["t0"], "t1": time.Now().UnixMilli()})
				continue
			}
			p.mu.Lock()
			p.msgs = append(p.msgs, m)
			p.mu.Unlock()
		}
	}()
	r.wait("joined "+id, func() bool { return r.f.count("sync "+id) > 0 })
	return p
}

func (p *testPhone) send(v any) {
	b, _ := json.Marshal(v)
	if err := p.ws.Write(context.Background(), websocket.MessageText, b); err != nil {
		p.t.Logf("write %s: %v", p.id, err)
	}
}

// got returns the first received message of a type, nil if none.
func (p *testPhone) got(typ string) map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, m := range p.msgs {
		if m["type"] == typ {
			return m
		}
	}
	return nil
}

func (r *rig) wait(what string, cond func() bool) {
	r.t.Helper()
	for i := 0; i < 400; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.t.Fatalf("timed out waiting for: %s", what)
}

func (r *rig) pair(a, b string) {
	if a > b {
		a, b = b, a
	}
	r.f.mu.Lock()
	r.f.pairs[[2]string{a, b}] = true
	r.f.mu.Unlock()
}

func relayEnv(from string, hops int, msg any) map[string]any {
	return map[string]any{"type": "relay", "from": Handle(from), "hops": hops, "msg": msg}
}

func motion() map[string]any {
	return map[string]any{"type": "m", "t": time.Now().UnixMilli(), "ax": 0.1, "ay": 0, "az": 0, "rot": 1}
}

func TestHandle(t *testing.T) {
	a, b := Handle("phone-a"), Handle("phone-b")
	if len(a) != 12 || a == b || a != Handle("phone-a") || strings.Contains(a, "phone") {
		t.Fatalf("handles %q %q", a, b)
	}
}

func TestSignalRelayedOnlyBetweenPairedPhones(t *testing.T) {
	r := newRig(t)
	a, b, c := r.join("A"), r.join("B"), r.join("C")
	r.pair("A", "B")
	offer := map[string]any{"type": "sig", "to": Handle("B"), "kind": "offer", "data": map[string]any{"sdp": "v=0"}}
	a.send(offer)
	r.wait("B gets the offer", func() bool { return b.got("sig") != nil })
	m := b.got("sig")
	if m["from"] != Handle("A") || m["kind"] != "offer" || m["data"].(map[string]any)["sdp"] != "v=0" {
		t.Fatalf("offer as delivered: %v", m)
	}
	if _, leaked := m["to"]; leaked {
		t.Fatalf("delivered signal still names its target: %v", m)
	}
	// Not paired, unknown target, bad kind, itself: all dropped.
	a.send(map[string]any{"type": "sig", "to": Handle("C"), "kind": "offer", "data": map[string]any{}})
	a.send(map[string]any{"type": "sig", "to": "ffffffffffff", "kind": "offer", "data": map[string]any{}})
	a.send(map[string]any{"type": "sig", "to": Handle("B"), "kind": "steal", "data": map[string]any{}})
	a.send(map[string]any{"type": "sig", "to": Handle("A"), "kind": "ice", "data": map[string]any{}})
	// A marker that does get through, to know the others were handled.
	a.send(map[string]any{"type": "sig", "to": Handle("B"), "kind": "ice", "data": map[string]any{"n": 1}})
	r.wait("marker", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return len(b.msgs) >= 2
	})
	time.Sleep(50 * time.Millisecond)
	if c.got("sig") != nil || a.got("sig") != nil {
		t.Fatal("a signal reached a phone it was not for")
	}
	b.mu.Lock()
	n := 0
	for _, m := range b.msgs {
		if m["type"] == "sig" {
			n++
		}
	}
	b.mu.Unlock()
	if n != 2 {
		t.Fatalf("B got %d signals, want 2", n)
	}
}

func TestSignalRateLimited(t *testing.T) {
	r := newRig(t)
	a, b := r.join("A"), r.join("B")
	r.pair("A", "B")
	for i := 0; i < 400; i++ {
		a.send(map[string]any{"type": "sig", "to": Handle("B"), "kind": "ice", "data": map[string]any{"i": i}})
	}
	a.send(map[string]any{"type": "rtc", "on": true})
	r.wait("all handled", func() bool { return r.f.has("rtc A true") })
	b.mu.Lock()
	n := len(b.msgs)
	b.mu.Unlock()
	// (B's send queue may drop more: a slow client misses messages.)
	if n < 10 || n > sigBurst+40 {
		t.Fatalf("B got %d of 400 signals; the limit is a burst of %v", n, sigBurst)
	}
	if got := r.hb.mesh.sigs.Load(); got < sigBurst || got > sigBurst+40 {
		t.Fatalf("hub passed on %d of 400 signals; the limit is a burst of %v", got, sigBurst)
	}
	// The bucket itself: a burst, then the steady rate.
	var bk bucket
	ok := 0
	for i := 0; i < 1000; i++ {
		if bk.take(1000, sigRate, sigBurst) {
			ok++
		}
	}
	if ok != int(sigBurst) || bk.take(1001, sigRate, sigBurst) || !bk.take(2000, sigRate, sigBurst) {
		t.Fatalf("bucket let %d of 1000 through at once (burst %v)", ok, sigBurst)
	}
}

func TestNearHandlesResolved(t *testing.T) {
	r := newRig(t)
	a := r.join("A")
	r.join("B")
	a.send(map[string]any{"type": "near", "tx": 900, "peers": []map[string]any{
		{"id": Handle("B"), "corr": 0.8, "lagMs": 200, "hops": 1},
		{"id": "000000000000", "corr": 0.9, "lagMs": 0, "hops": 1}, // nobody
		{"id": Handle("A"), "corr": 1, "lagMs": 0, "hops": 1},      // itself
	}})
	r.wait("near", func() bool { return r.f.has("near A") })
	r.f.mu.Lock()
	n := r.f.near["A"]
	r.f.mu.Unlock()
	if len(n.Peers) != 1 || n.Peers[0].ID != "B" || n.Peers[0].Corr != 0.8 || n.Tx != 900 {
		t.Fatalf("near as delivered: %+v", n)
	}
}

// A phone announces it is dropping its socket (jam), then everything from
// it arrives through a neighbour and everything for it goes back that way.
func TestJamThenRelayBothWays(t *testing.T) {
	r := newRig(t)
	a, b := r.join("A"), r.join("B")
	a.send(map[string]any{"type": "jam", "on": true})
	r.wait("jam ack", func() bool { return r.f.has("jam A true") })
	a.ws.Close(websocket.StatusNormalClosure, "")
	// B carries A's readings.
	for i := 0; i < 5; i++ {
		b.send(relayEnv("A", 1, motion()))
	}
	r.wait("relayed motion", func() bool { return r.f.motions("A") == 5 })
	if r.f.has("gone A") {
		t.Fatal("a phone that announced the switch must not read as gone")
	}
	if !r.f.has("route A via B hops 1") {
		t.Fatalf("no route event: %v", r.f.events)
	}
	if via, hops, ok := r.hb.Route("A"); !ok || via != "B" || hops != 1 {
		t.Fatalf("route: %q %d %v", via, hops, ok)
	}
	// Clock: A's own socket measured its offset; relayed readings use it.
	r.f.mu.Lock()
	mt := r.f.motion["A"][0].T
	r.f.mu.Unlock()
	if d := time.Now().UnixMilli() - mt; d < -500 || d > 2000 {
		t.Fatalf("relayed reading is %d ms from server time", d)
	}
	// Down: state for A is wrapped for B to pass on.
	r.hb.SendPhone("A", protocol.PhoneState{Type: protocol.TypeState, Node: "ok", Zone: "red"})
	r.wait("relay down", func() bool { return b.got("relay") != nil })
	env := b.got("relay")
	if env["to"] != Handle("A") || env["msg"].(map[string]any)["zone"] != "red" {
		t.Fatalf("downward envelope: %v", env)
	}
	// Other relayed message kinds go through the normal dispatch.
	b.send(relayEnv("A", 1, map[string]any{"type": "pos", "x": 3, "y": 4}))
	b.send(relayEnv("A", 1, map[string]any{"type": "mpos", "x": 5, "y": 6, "acc": 2}))
	r.wait("relayed pos", func() bool { return r.f.has("pos A 3.0 4.0") && r.f.has("mpos A 5.0 6.0") })

	// A's own socket comes back and takes over.
	a2 := r.join("A")
	r.wait("direct again", func() bool { return r.f.has("route A via  hops 0") })
	if via, _, ok := r.hb.Route("A"); !ok || via != "" {
		t.Fatalf("route after reconnect: %q %v", via, ok)
	}
	before := r.f.motions("A")
	b.send(relayEnv("A", 1, motion())) // a late relayed copy: its own socket wins
	a2.send(motion())
	r.wait("direct motion", func() bool { return r.f.motions("A") > before })
	time.Sleep(50 * time.Millisecond)
	if got := r.f.motions("A"); got != before+1 {
		t.Fatalf("%d readings after reconnect, want %d (the relayed copy must be dropped)", got, before+1)
	}
}

func TestRelayValidation(t *testing.T) {
	r := newRig(t)
	a, b := r.join("A"), r.join("B")
	a.send(map[string]any{"type": "jam", "on": true})
	r.wait("jam ack", func() bool { return r.f.has("jam A true") })
	_, dropped0 := r.hb.RelayCounts()
	bad := []any{
		relayEnv("B", 1, motion()),                                                            // loop: relaying for itself
		relayEnv("A", 0, motion()),                                                            // hops out of range
		relayEnv("A", protocol.MaxRelayHops+1, motion()),                                      // hop limit
		relayEnv("nobody", 1, motion()),                                                       // unknown handle
		relayEnv("A", 1, relayEnv("A", 1, motion())),                                          // nested relay
		relayEnv("A", 1, map[string]any{"type": "pong", "t0": 1}),                             // not relayable
		relayEnv("A", 1, map[string]any{"type": "jam", "on": false}),                          // not relayable
		relayEnv("A", 1, map[string]any{"type": "gps", "lat": 49.2, "lon": -122.9, "acc": 5}), // raw GPS never goes through a neighbour
		map[string]any{"type": "relay", "from": Handle("A"), "hops": 1},
		relayEnv("A", 1, map[string]any{"type": "m", "pad": strings.Repeat("x", maxRelayMsg)}), // too big
	}
	for _, m := range bad {
		b.send(m)
	}
	b.send(relayEnv("A", 2, motion()))
	r.wait("the good one", func() bool { return r.f.motions("A") == 1 })
	if _, dropped := r.hb.RelayCounts(); dropped-dropped0 != int64(len(bad)) {
		t.Fatalf("dropped %d of %d bad envelopes", dropped-dropped0, len(bad))
	}
	if !r.f.has("route A via B hops 2") {
		t.Fatalf("route: %v", r.f.events)
	}
}

// A phone whose own socket is alive is never overridden by a relayed copy;
// a phone the server never saw can't be introduced through a relay.
func TestRelayNeverOverridesLiveSocket(t *testing.T) {
	r := newRig(t)
	a, b := r.join("A"), r.join("B")
	a.send(motion())
	r.wait("direct", func() bool { return r.f.motions("A") == 1 })
	b.send(relayEnv("A", 1, motion()))
	b.send(relayEnv("A", 1, map[string]any{"type": "pos", "x": 9, "y": 9}))
	b.send(relayEnv("Z", 1, map[string]any{"type": "hello", "ua": "x"}))
	b.send(map[string]any{"type": "rtc", "on": true})
	r.wait("handled", func() bool { return r.f.has("rtc B true") })
	if r.f.motions("A") != 1 || r.f.has("pos A 9.0 9.0") || r.f.count("helloauto Z") > 0 {
		t.Fatalf("relayed copies got through: %v", r.f.events)
	}
	if via, _, _ := r.hb.Route("A"); via != "" {
		t.Fatalf("A rerouted via %q", via)
	}
}

// A phone that really lost its connection (no announcement): once its
// socket is gone it says hello through a neighbour and carries on.
func TestRelayAfterRealLoss(t *testing.T) {
	r := newRig(t)
	a, b := r.join("A"), r.join("B")
	a.ws.CloseNow()
	r.wait("gone", func() bool { return r.f.has("gone A") })
	b.send(relayEnv("A", 1, motion())) // before a hello: dropped
	b.send(relayEnv("A", 1, map[string]any{"type": "hello", "x": 2, "y": 3, "ua": "test"}))
	b.send(relayEnv("A", 1, motion()))
	r.wait("back through B", func() bool { return r.f.motions("A") == 1 })
	if !r.f.has("hello A 2.0 3.0") {
		t.Fatalf("relayed hello not delivered under the phone's id: %v", r.f.events)
	}
	// Nobody delivers for it any more: the sweep reports it gone.
	gone := r.f.count("gone A")
	r.hb.SweepRelayed(Now() + relayGoneMs + 1)
	if r.f.count("gone A") != gone+1 {
		t.Fatal("sweep did not drop the silent relayed phone")
	}
	if _, _, ok := r.hb.Route("A"); ok {
		t.Fatal("still routed after the sweep")
	}
}

func TestRelayRateLimited(t *testing.T) {
	r := newRig(t)
	a, b := r.join("A"), r.join("B")
	a.send(map[string]any{"type": "jam", "on": true})
	r.wait("jam ack", func() bool { return r.f.has("jam A true") })
	for i := 0; i < 500; i++ {
		b.send(relayEnv("A", 1, motion()))
	}
	b.send(map[string]any{"type": "rtc", "on": true})
	r.wait("handled", func() bool { return r.f.has("rtc B true") })
	if n := r.f.motions("A"); n < int(relayBurst)-1 || n > int(relayBurst)+60 {
		t.Fatalf("%d of 500 relayed readings accepted; the limit is a burst of %v", n, relayBurst)
	}
}

func TestClockToldToPhone(t *testing.T) {
	r := newRig(t)
	a := r.join("A")
	r.wait("clock", func() bool { return a.got("clock") != nil })
	if off, ok := a.got("clock")["offset"].(float64); !ok || off < -1000 || off > 1000 {
		t.Fatalf("clock message: %v", a.got("clock"))
	}
}
