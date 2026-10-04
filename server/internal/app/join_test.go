package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/locate"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// fakePhone is a phone page over the real WebSocket: it answers pings,
// streams calm motion and keeps the last state the server sent.
type fakePhone struct {
	id     string
	ws     *websocket.Conn
	cancel context.CancelFunc
	mu     sync.Mutex
	state  protocol.PhoneState
	states int
}

func joinPhone(t *testing.T, ctx context.Context, base, id string) *fakePhone {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+"/ws/phone", nil)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakePhone{id: id, ws: ws, cancel: cancel}
	// A demo-spot phone sends no position: the server lines it up.
	ws.Write(ctx, websocket.MessageText, []byte(fmt.Sprintf(`{"type":"hello","id":%q,"ua":"iPhone"}`, id)))
	go func() {
		for {
			_, b, err := ws.Read(ctx)
			if err != nil {
				return
			}
			typ, _ := protocol.PeekType(b)
			switch typ {
			case protocol.TypePing:
				var pg protocol.Ping
				json.Unmarshal(b, &pg)
				// A slow mobile link: about 150 ms each way, and a phone clock 4 s ahead.
				go func(t0 int64) {
					time.Sleep(150 * time.Millisecond)
					t1 := time.Now().UnixMilli() + 4000
					time.Sleep(150 * time.Millisecond)
					pong, _ := json.Marshal(protocol.Pong{Type: "pong", T0: t0, T1: t1})
					ws.Write(ctx, websocket.MessageText, pong)
				}(pg.T0)
			case protocol.TypeState:
				var st protocol.PhoneState
				if json.Unmarshal(b, &st) == nil {
					p.mu.Lock()
					p.state, p.states = st, p.states+1
					p.mu.Unlock()
				}
			}
		}
	}()
	go func() {
		tk := time.NewTicker(100 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				m, _ := json.Marshal(protocol.Motion{Type: "m", T: time.Now().UnixMilli() + 4000, AX: 0.02, AZ: -0.01, Rot: 3})
				ws.Write(ctx, websocket.MessageText, m)
			}
		}
	}()
	return p
}

func (p *fakePhone) leave() {
	p.cancel()
	p.ws.CloseNow()
}

// waitRow waits for a state whose row place is n.
func (p *fakePhone) waitRow(t *testing.T, n int) protocol.PhoneState {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		st := p.state
		p.mu.Unlock()
		if st.Row != nil && st.Row.N == n {
			return st
		}
		time.Sleep(50 * time.Millisecond)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	t.Fatalf("%s: never told it is #%d in the row (last state %+v, row %+v)", p.id, n, p.state, p.state.Row)
	return protocol.PhoneState{}
}

// TestJoinAtTheTable drives the whole table demo over the real WebSocket:
// the demo spot on, phones join with no position and are told their place
// in the row and who stands before them; a phone that drops (the screen
// slept, the tunnel hiccuped) comes back to the same place, name and
// slot, while a phone that joins meanwhile takes the next place, not its
// gap; the dashboard snapshot carries the slots; the join counts add up.
func TestJoinAtTheTable(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	a := New(Options{Detect: detect.DefaultConfig(), RecordingsDir: t.TempDir(), DataDir: t.TempDir()})
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	go a.Run(ctx)
	mux := http.NewServeMux()
	a.Routes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	if code := do(t, "PUT", srv.URL+"/api/demo", map[string]any{"on": true, "x": 4, "y": 6}, nil); code != 200 {
		t.Fatalf("PUT /api/demo: %d", code)
	}

	p1 := joinPhone(t, ctx, srv.URL, "phone-one")
	s1 := p1.waitRow(t, 1)
	p2 := joinPhone(t, ctx, srv.URL, "phone-two")
	s2 := p2.waitRow(t, 2)
	p3 := joinPhone(t, ctx, srv.URL, "phone-three")
	s3 := p3.waitRow(t, 3)
	defer p1.leave()
	defer p3.leave()
	if s1.Row.Prev != "" || s2.Row.Prev != s1.Name || s3.Row.Prev != s2.Name || s1.Name == "" {
		t.Errorf("prev names: #1 %q (%q), #2 after %q (want %q), #3 after %q (want %q)", s1.Row.Prev, s1.Name, s2.Row.Prev, s1.Name, s3.Row.Prev, s2.Name)
	}
	if s1.X != 4 || s2.X != 4.6 || s3.X != 5.2 || s1.Y != 6 {
		t.Errorf("row positions %.1f %.1f %.1f (y %.1f)", s1.X, s2.X, s3.X, s1.Y)
	}

	// Phone two drops; phone four joins meanwhile and takes place 4, not 2.
	p2.leave()
	time.Sleep(300 * time.Millisecond)
	p4 := joinPhone(t, ctx, srv.URL, "phone-four")
	defer p4.leave()
	p4.waitRow(t, 4)
	// Phone two comes back with the same id: same place, same name.
	p2b := joinPhone(t, ctx, srv.URL, "phone-two")
	defer p2b.leave()
	back := p2b.waitRow(t, 2)
	if back.Name != s2.Name || back.X != s2.X {
		t.Errorf("reconnected as %q at %.1f, was %q at %.1f", back.Name, back.X, s2.Name, s2.X)
	}

	// Clock sync over a slow link: the offset (phone 4 s ahead) is found.
	time.Sleep(1500 * time.Millisecond)
	a.mu.Lock()
	m := a.live.meta["phone-one"]
	synced, off := m.synced, m.offset
	snap := a.snapshotLocked(time.Now().UnixMilli())
	a.mu.Unlock()
	if !synced || math.Abs(float64(off-4000)) > 60 {
		t.Errorf("clock: synced %v offset %d, want about 4000", synced, off)
	}
	slots := map[string]int{}
	for _, n := range snap.Nodes {
		slots[n.ID] = n.Slot
	}
	if slots["phone-one"] != 1 || slots["phone-two"] != 2 || slots["phone-three"] != 3 || slots["phone-four"] != 4 {
		t.Errorf("snapshot slots: %v", slots)
	}

	var st protocol.JoinStats
	if code := do(t, "GET", srv.URL+"/api/join/stats", nil, &st); code != 200 || st.Joined != 4 || st.Streaming != 4 {
		t.Errorf("join stats: %d %+v", code, st)
	}
}

// TestDemoPhonesStayPut: the position estimator never moves a phone lined
// up at the demo spot, whatever it is told; a phone placed by a tap is
// still the estimator's to move.
func TestDemoPhonesStayPut(t *testing.T) {
	a := demoApp(t)
	demoOn(t, a, 6, 8)
	a.PhoneHelloAuto("lined", "test")
	a.PhoneHello("tapped", 6, 10, "test")
	a.PhoneSync("lined", 0, 20)
	a.PhoneSync("tapped", 0, 20)
	a.mu.Lock()
	if a.live.loc == nil {
		a.mu.Unlock()
		t.Skip("position estimator off")
	}
	a.mu.Unlock()
	now := demoT0
	for i := 0; i < 40; i++ {
		a.mu.Lock()
		// The estimator is made to put both 5 m away (as a walk it thinks it saw would).
		a.live.loc.est.Fix("lined", now, locate.Fix{X: 11, Y: 8, Exact: true})
		a.live.loc.est.Fix("tapped", now, locate.Fix{X: 11, Y: 10, Exact: true})
		a.mu.Unlock()
		a.detectTick(now)
		now += 250
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if m := a.live.meta["lined"]; m.x != 6 || m.y != 8 || m.acc != 0 {
		t.Errorf("lined-up phone drifted to %.2f, %.2f (acc %.1f)", m.x, m.y, m.acc)
	}
	if m := a.live.meta["tapped"]; math.Abs(m.x-11) > 0.01 {
		t.Errorf("tapped phone at %.2f: the estimator should still place it (11)", m.x)
	}
	if s := a.demoSlotsLocked(); s["lined"] != 1 || s["tapped"] != 0 {
		t.Errorf("slots %v", s)
	}
}

// TestJoinLinkOrder: settings, then PUBLIC_URL, then a running quick
// tunnel, then the dashboard's own address; settings persist.
func TestJoinLinkOrder(t *testing.T) {
	metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/quicktunnel" {
			fmt.Fprint(w, `{"hostname":"brave-otter-lamp.trycloudflare.com"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer metrics.Close()
	named := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"hostname":""}`) // a named tunnel
	}))
	defer named.Close()
	t.Setenv("CLOUDFLARED_METRICS", strings.TrimPrefix(named.URL, "http://")+","+strings.TrimPrefix(metrics.URL, "http://"))
	checkTunnels = false // the fake hostname must not be fetched for real
	defer func() { checkTunnels = true }()

	dir := t.TempDir()
	a, srv := testServer(t, Options{DataDir: dir})
	a.opt.PublicURL = ""
	var j protocol.JoinInfo
	do(t, "GET", srv.URL+"/api/join", nil, &j)
	if j.Source != JoinFromTunnel || j.URL != "https://brave-otter-lamp.trycloudflare.com/" || !j.Secure || j.Reachable != ReachPublic || j.Problem != "" {
		t.Fatalf("tunnel: %+v", j)
	}
	if j.Display != "brave-otter-lamp.trycloudflare.com" {
		t.Errorf("display %q", j.Display)
	}
	a.opt.PublicURL = "https://pulse.example.tech/"
	do(t, "GET", srv.URL+"/api/join", nil, &j)
	if j.Source != JoinFromEnv || j.URL != "https://pulse.example.tech/" || j.Tunnel == "" {
		t.Fatalf("env: %+v", j)
	}
	if code := do(t, "PUT", srv.URL+"/api/join", map[string]string{"url": "  Pulse-Two.Example.tech/ "}, &j); code != 200 ||
		j.Source != JoinFromSettings || j.URL != "https://pulse-two.example.tech/" {
		t.Fatalf("settings: %d %+v", code, j)
	}
	for _, bad := range []string{"ftp://x.y", "https://x.y/path", "https://x.y/?a=1", "https://"} {
		if code := do(t, "PUT", srv.URL+"/api/join", map[string]string{"url": bad}, nil); code != 400 {
			t.Errorf("PUT %q: %d, want 400", bad, code)
		}
	}
	// Saved: a restarted server uses it.
	b := New(Options{Detect: detect.DefaultConfig(), RecordingsDir: t.TempDir(), DataDir: dir})
	if got := b.JoinInfo(httptest.NewRequest("GET", "/", nil)); got.Source != JoinFromSettings || got.URL != "https://pulse-two.example.tech/" {
		t.Errorf("after restart: %+v", got)
	}
	// Cleared: back to PUBLIC_URL.
	do(t, "PUT", srv.URL+"/api/join", map[string]string{"url": ""}, &j)
	if j.Source != JoinFromEnv {
		t.Errorf("cleared: %+v", j)
	}
	// Plain http on a LAN address: a problem, said in words.
	got := joinInfo("http://192.168.1.20:8080", JoinFromRequest)
	if got.Secure || got.Reachable != ReachLAN || !strings.Contains(got.Problem, "https") {
		t.Errorf("LAN http: %+v", got)
	}
}

// TestJoinLinkCheck: "Test this QR" tells this server from another one,
// from something else, and from nothing at all.
func TestJoinLinkCheck(t *testing.T) {
	a, srv := testServer(t, Options{})
	ctx := context.Background()
	// Itself, over plain http on 127.0.0.1: answers, but phones can't use it.
	got := a.TestJoinURL(ctx, srv.URL)
	if got.Pulse != "this" || got.OK || got.Secure || got.Reachable != ReachLocal {
		t.Errorf("self: %+v", got)
	}
	// Another Pulse server.
	_, other := testServer(t, Options{})
	if got := a.TestJoinURL(ctx, other.URL); got.Pulse != "other" || got.OK {
		t.Errorf("other: %+v", got)
	}
	// A tunnel whose origin is down (Cloudflare answers 530).
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(530)
	}))
	defer down.Close()
	if got := a.TestJoinURL(ctx, down.URL); got.OK || got.Status != 530 || !strings.Contains(got.Message, "port") {
		t.Errorf("530: %+v", got)
	}
	// Nothing listening.
	dead := httptest.NewServer(http.NotFoundHandler())
	u := dead.URL
	dead.Close()
	if got := a.TestJoinURL(ctx, u); got.OK || !strings.Contains(got.Message, "listening") {
		t.Errorf("closed port: %+v", got)
	}
	if got := a.TestJoinURL(ctx, "not a url at all/x"); got.OK {
		t.Errorf("garbage: %+v", got)
	}
	// Over HTTP, with no body: the current join URL.
	var jt protocol.JoinTest
	if code := do(t, "POST", srv.URL+"/api/join/test", "", &jt); code != 200 || jt.Pulse != "this" {
		t.Errorf("POST /api/join/test: %d %+v", code, jt)
	}
}

// TestJoinReports: phones that couldn't get motion are counted by reason
// until they report ok or start streaming.
func TestJoinReports(t *testing.T) {
	a, srv := testServer(t, Options{})
	rep := func(id, reason, browser string) int {
		return do(t, "POST", srv.URL+"/api/join/report", protocol.JoinReport{ID: id, Reason: reason, Browser: browser}, nil)
	}
	if c := rep("a", protocol.JoinInApp, "Instagram (iOS)"); c != 204 {
		t.Fatalf("report: %d", c)
	}
	rep("b", protocol.JoinInApp, "LinkedIn (Android)")
	rep("c", protocol.JoinMotionDenied, "iOS Safari 17")
	rep("d", protocol.JoinNoMotion, "<script>")
	if c := rep("e", "bogus", ""); c != 400 {
		t.Errorf("bogus reason: %d", c)
	}
	rep("c", protocol.JoinOK, "iOS Safari 17") // allowed it on the second try
	// d got going after all: streaming phones aren't counted.
	a.PhoneHello("d", 3, 3, "Android")
	a.PhoneMotion("d", protocol.Motion{T: time.Now().UnixMilli(), AX: 0.1}, time.Now().UnixMilli())
	var st protocol.JoinStats
	do(t, "GET", srv.URL+"/api/join/stats", nil, &st)
	if st.Joined != 1 || st.Streaming != 1 || len(st.Problems) != 1 {
		t.Fatalf("stats %+v", st)
	}
	p := st.Problems[0]
	if p.Reason != protocol.JoinInApp || p.Count != 2 || p.Label != "in-app browser" || len(p.Browsers) != 2 {
		t.Errorf("problem %+v", p)
	}
}
