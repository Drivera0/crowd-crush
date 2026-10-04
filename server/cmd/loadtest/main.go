// Command loadtest connects many fake phones to a running Pulse server over
// the real WebSocket and measures how it copes:
//
//	go run ./server/cmd/loadtest -n 1000 -url ws://localhost:8080/ws/phone -duration 60s
//	go run ./server/cmd/loadtest -n 500,1000        # one run per size, 15 s apart
//
// Each phone does what the phone page does: hello with a position, answers
// the server's clock-sync pings, and streams a 100 ms motion summary (10 Hz)
// of calm standing (internal/sim "calm"). Phones join evenly over -ramp.
// One dashboard client on /ws/dash times the snapshots, and GET /api/status
// is polled once a second.
//
// Measured: connects and failures, messages sent per second, the ping →
// phone delivery delay (load generator and server share a clock on one
// machine), the server's own ping/pong round trip per phone (snapshot
// nodes[].rtt), snapshot rate and size, stats.detectMs if the server
// reports it, and /api/status latency. Results print as a summary and go to
// -out (docs/loadtest.md) with the machine's details.
//
// Note: these phones are real to the server. They appear on the dashboard,
// in recordings and storage, and if the venue is small they are dense
// enough to raise density alerts.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/coder/websocket"

	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/sim"
)

const ua = "loadtest"

func main() {
	ns := flag.String("n", "1000", "phones; a comma-separated list runs each size in turn")
	wsURL := flag.String("url", "ws://localhost:8080/ws/phone", "server phone WebSocket")
	dur := flag.Duration("duration", 60*time.Second, "length of each run, ramp included")
	ramp := flag.Duration("ramp", 10*time.Second, "time over which phones join")
	pause := flag.Duration("pause", 15*time.Second, "pause between runs (list of sizes)")
	out := flag.String("out", "docs/loadtest.md", "Markdown report (\"\" = don't write)")
	seed := flag.Int64("seed", 1, "random seed (positions, motion)")
	flag.Parse()

	var sizes []int
	for _, s := range strings.Split(*ns, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || n < 1 || n > 20000 {
			log.Fatalf("bad -n %q", s)
		}
		sizes = append(sizes, n)
	}
	base, err := url.Parse(*wsURL)
	if err != nil || (base.Scheme != "ws" && base.Scheme != "wss") {
		log.Fatalf("bad -url %q", *wsURL)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var results []*result
	for i, n := range sizes {
		if i > 0 {
			log.Printf("pausing %s before the next run", *pause)
			select {
			case <-time.After(*pause):
			case <-ctx.Done():
			}
		}
		if ctx.Err() != nil {
			break
		}
		r := runOnce(ctx, base, n, *dur, *ramp, *seed)
		r.print()
		results = append(results, r)
		if r.serverDown {
			log.Printf("server stopped answering; not running further sizes")
			break
		}
	}
	if *out != "" && len(results) > 0 {
		if err := writeReport(*out, base.String(), results); err != nil {
			log.Fatal(err)
		}
		log.Printf("wrote %s", *out)
	}
}

// result is one run's measurements.
type result struct {
	n             int
	dur, ramp     time.Duration
	venueW        float64
	venueH        float64
	connected     int64
	failed        int64
	dropped       int64 // sessions that ended before the run did
	firstErr      string
	sent          int64
	steadySent    int64
	steadySecs    float64
	pings         []float64 // ms, ping sent → phone received
	serverRTT     []float64 // ms, server-measured ping/pong round trips of our phones
	snapN         int
	snapBytes     []float64
	snapGap       []float64 // ms between snapshots (steady state)
	snapNodes     int       // nodes in the last snapshot
	snapOurs      int       // of them ours
	mode          string    // snapshot mode (live | replay | sim)
	detectMs      []float64
	serverMsgRate []float64 // stats.msgPerSec while steady
	statusMs      []float64
	statusErr     int
	dashErr       string
	serverDown    bool
	timeline      []string
}

func runOnce(parent context.Context, base *url.URL, n int, dur, ramp time.Duration, seed int64) *result {
	r := &result{n: n, dur: dur, ramp: ramp, venueW: 24, venueH: 16}
	httpBase := "http://" + base.Host
	if base.Scheme == "wss" {
		httpBase = "https://" + base.Host
	}
	if w, h, err := venueSize(httpBase); err == nil {
		r.venueW, r.venueH = w, h
	} else {
		log.Printf("GET /api/config: %v (assuming 24 × 16 m)", err)
	}
	ctx, cancel := context.WithTimeout(parent, dur)
	defer cancel()
	start := time.Now()
	steadyFrom := start.Add(ramp + 2*time.Second)

	sc, err := sim.NewLayout("calm", n, seed, sim.CrowdLayout(false))
	if err != nil {
		log.Fatal(err)
	}
	pos := spread(n, r.venueW, r.venueH, seed)

	var mu sync.Mutex // guards r's slices and counters below
	var sent, steadySent, live atomic.Int64
	var wg sync.WaitGroup

	// Dashboard client.
	dashURL := *base
	dashURL.Path = strings.TrimSuffix(base.Path, "/phone") + "/dash"
	wg.Add(1)
	go func() {
		defer wg.Done()
		dash(ctx, dashURL.String(), steadyFrom, r, &mu)
	}()
	// Status poller.
	wg.Add(1)
	go func() {
		defer wg.Done()
		pollStatus(ctx, httpBase+"/api/status", r, &mu)
	}()
	// Timeline every 5 s.
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		var last int64
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s := sent.Load()
				mu.Lock()
				line := fmt.Sprintf("t=%2.0fs phones=%d sent=%d/s snapshots=%d", time.Since(start).Seconds(), live.Load(), (s-last)/5, r.snapN)
				if len(r.snapBytes) > 0 {
					line += fmt.Sprintf(" lastSnapshot=%.0f kB", r.snapBytes[len(r.snapBytes)-1]/1024)
				}
				r.timeline = append(r.timeline, line)
				mu.Unlock()
				last = s
				log.Print(line)
			}
		}
	}()

	rng := rand.New(rand.NewSource(seed))
	log.Printf("run: %d phones over %s, %s total, venue %.0f × %.0f m", n, ramp, dur, r.venueW, r.venueH)
	for i := 0; i < n; i++ {
		at := start.Add(time.Duration(float64(ramp) * float64(i) / float64(n)))
		select {
		case <-time.After(time.Until(at)):
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		ph := &phone{i: i, id: fmt.Sprintf("load-%04d-%04x", i, rng.Intn(1<<16)), x: pos[i][0], y: pos[i][1], sc: sc, start: start}
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := ph.run(ctx, base.String(), func() { live.Add(1) }, func(ms float64) {
				mu.Lock()
				r.pings = append(r.pings, ms)
				mu.Unlock()
			}, func() {
				sent.Add(1)
				if time.Now().After(steadyFrom) {
					steadySent.Add(1)
				}
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == errDial:
				r.failed++
				if r.firstErr == "" {
					r.firstErr = ph.err
				}
			case ph.connected && ctx.Err() == nil:
				r.dropped++
				if r.firstErr == "" {
					r.firstErr = ph.err
				}
			}
			if ph.connected {
				r.connected++
				live.Add(-1)
			}
		}()
	}
	<-ctx.Done()
	wg.Wait()
	r.sent = sent.Load()
	r.steadySent = steadySent.Load()
	r.steadySecs = time.Since(steadyFrom).Seconds()
	if r.steadySecs < 0 {
		r.steadySecs = 0
	}
	// Did the server survive? One status call after the phones left.
	cl := &http.Client{Timeout: 5 * time.Second}
	if resp, err := cl.Get(httpBase + "/api/status"); err != nil {
		r.serverDown = true
		log.Printf("after the run, /api/status failed: %v", err)
	} else {
		resp.Body.Close()
	}
	return r
}

func venueSize(httpBase string) (w, h float64, err error) {
	cl := &http.Client{Timeout: 3 * time.Second}
	resp, err := cl.Get(httpBase + "/api/config")
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	var c struct {
		VenueW float64 `json:"venueW"`
		VenueH float64 `json:"venueH"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		return 0, 0, err
	}
	if c.VenueW <= 0 || c.VenueH <= 0 {
		return 0, 0, fmt.Errorf("venue %v × %v", c.VenueW, c.VenueH)
	}
	return c.VenueW, c.VenueH, nil
}

// spread places n phones on a jittered grid over the whole venue, as evenly
// as possible, to keep crowd density down.
func spread(n int, w, h float64, seed int64) [][2]float64 {
	rng := rand.New(rand.NewSource(seed ^ 0x10ad))
	cols := int(math.Ceil(math.Sqrt(float64(n) * w / h)))
	rows := int(math.Ceil(float64(n) / float64(cols)))
	dx, dy := w/float64(cols), h/float64(rows)
	out := make([][2]float64, n)
	for i := range out {
		c, rr := i%cols, i/cols
		x := (float64(c) + 0.5 + 0.3*(rng.Float64()-0.5)) * dx
		y := (float64(rr) + 0.5 + 0.3*(rng.Float64()-0.5)) * dy
		out[i] = [2]float64{math.Round(x*100) / 100, math.Round(y*100) / 100}
	}
	return out
}

type phone struct {
	i         int
	id        string
	x, y      float64
	sc        *sim.Scenario
	start     time.Time
	connected bool
	err       string
}

var errDial = fmt.Errorf("dial failed")

// run is one phone's session. It returns errDial if it never connected.
func (p *phone) run(ctx context.Context, wsURL string, joined func(), ping func(ms float64), sent func()) error {
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	ws, _, err := websocket.Dial(dctx, wsURL, nil)
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		p.err = err.Error()
		return errDial
	}
	defer ws.CloseNow()
	p.connected = true
	joined()
	x, y := p.x, p.y
	b, _ := json.Marshal(protocol.Hello{Type: protocol.TypeHello, ID: p.id, X: &x, Y: &y, UA: ua})
	if err := ws.Write(ctx, websocket.MessageText, b); err != nil {
		p.err = err.Error()
		return err
	}
	sctx, scancel := context.WithCancel(ctx)
	defer scancel()
	// Reader: answer pings at once, like the phone page.
	go func() {
		defer scancel()
		for {
			_, b, err := ws.Read(sctx)
			if err != nil {
				if sctx.Err() == nil {
					p.err = "read: " + err.Error()
				}
				return
			}
			var pg protocol.Ping
			if json.Unmarshal(b, &pg) == nil && pg.Type == protocol.TypePing {
				now := time.Now().UnixMilli()
				ping(float64(now - pg.T0))
				out, _ := json.Marshal(protocol.Pong{Type: protocol.TypePong, T0: pg.T0, T1: now})
				if err := ws.Write(sctx, websocket.MessageText, out); err != nil {
					return
				}
			}
		}
	}()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-sctx.Done():
			if ctx.Err() == nil && p.err == "" {
				p.err = "session ended"
			}
			if ctx.Err() == nil {
				return fmt.Errorf("%s", p.err)
			}
			ws.Close(websocket.StatusNormalClosure, "load test over")
			return nil
		case now := <-tick.C:
			ax, ay, az, rot := p.sc.Summary(p.i, now.Sub(p.start).Seconds())
			m := protocol.Motion{Type: protocol.TypeMotion, T: now.UnixMilli(), AX: r3(ax), AY: r3(ay), AZ: r3(az), Rot: r3(rot)}
			b, _ := json.Marshal(m)
			if err := ws.Write(sctx, websocket.MessageText, b); err != nil {
				if ctx.Err() == nil {
					p.err = "write: " + err.Error()
				}
				continue // the select sees sctx end
			}
			sent()
		}
	}
}

func r3(v float64) float64 { return math.Round(v*1000) / 1000 }

// snapshot is the part of a dashboard snapshot the load test reads.
type snapshot struct {
	Type  string `json:"type"`
	Mode  string `json:"mode"`
	Nodes []struct {
		ID  string `json:"id"`
		UA  string `json:"ua"`
		RTT int64  `json:"rtt"`
	} `json:"nodes"`
	Stats map[string]any `json:"stats"`
}

func dash(ctx context.Context, u string, steadyFrom time.Time, r *result, mu *sync.Mutex) {
	ws, _, err := websocket.Dial(ctx, u, nil)
	if err != nil {
		mu.Lock()
		r.dashErr = err.Error()
		mu.Unlock()
		return
	}
	defer ws.CloseNow()
	ws.SetReadLimit(256 << 20)
	var last time.Time
	var lastRTT time.Time
	for {
		_, b, err := ws.Read(ctx)
		if err != nil {
			if ctx.Err() == nil {
				mu.Lock()
				r.dashErr = err.Error()
				mu.Unlock()
			}
			return
		}
		now := time.Now()
		var s snapshot
		if json.Unmarshal(b, &s) != nil || s.Type != protocol.TypeSnapshot {
			continue
		}
		steady := now.After(steadyFrom)
		mu.Lock()
		r.snapN++
		if steady {
			r.snapBytes = append(r.snapBytes, float64(len(b)))
			if !last.IsZero() {
				r.snapGap = append(r.snapGap, float64(now.Sub(last).Microseconds())/1000)
			}
		}
		last = now
		ours := 0
		sampleRTT := steady && now.Sub(lastRTT) >= time.Second
		for _, nd := range s.Nodes {
			if nd.UA == ua || strings.HasPrefix(nd.ID, "load-") {
				ours++
				if sampleRTT && nd.RTT > 0 {
					r.serverRTT = append(r.serverRTT, float64(nd.RTT))
				}
			}
		}
		if sampleRTT {
			lastRTT = now
		}
		r.snapNodes, r.snapOurs, r.mode = len(s.Nodes), ours, s.Mode
		if steady {
			if v, ok := s.Stats["detectMs"].(float64); ok {
				r.detectMs = append(r.detectMs, v)
			}
			if v, ok := s.Stats["msgPerSec"].(float64); ok {
				r.serverMsgRate = append(r.serverMsgRate, v)
			}
		}
		mu.Unlock()
	}
}

func pollStatus(ctx context.Context, u string, r *result, mu *sync.Mutex) {
	cl := &http.Client{Timeout: 5 * time.Second}
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		t0 := time.Now()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		resp, err := cl.Do(req)
		ms := float64(time.Since(t0).Microseconds()) / 1000
		if err != nil {
			if ctx.Err() == nil {
				mu.Lock()
				r.statusErr++
				mu.Unlock()
			}
			continue
		}
		resp.Body.Close()
		mu.Lock()
		r.statusMs = append(r.statusMs, ms)
		mu.Unlock()
	}
}

// pct is the p-th percentile (0–100) of v, nearest rank.
func pct(v []float64, p float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	i := int(math.Ceil(p/100*float64(len(s)))) - 1
	return s[max(0, min(i, len(s)-1))]
}

func mean(v []float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	t := 0.0
	for _, x := range v {
		t += x
	}
	return t / float64(len(v))
}

func f1(v float64) string {
	if math.IsNaN(v) {
		return "—"
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
}

func (r *result) rows() [][2]string {
	snapHz := math.NaN()
	if g := mean(r.snapGap); !math.IsNaN(g) && g > 0 {
		snapHz = 1000 / g
	}
	steadyRate := math.NaN()
	if r.steadySecs > 0 {
		steadyRate = float64(r.steadySent) / r.steadySecs
	}
	rows := [][2]string{
		{"Phones connected / failed / dropped", fmt.Sprintf("%d / %d / %d", r.connected, r.failed, r.dropped)},
		{"Motion messages sent (total)", fmt.Sprint(r.sent)},
		{"Messages/s sent, steady state (ideal 10 × N)", fmt.Sprintf("%s (ideal %d)", f1(steadyRate), 10*r.n)},
		{"Server-reported msgPerSec, median", f1(pct(r.serverMsgRate, 50))},
		{"Ping delivery, server → phone (ms) p50 / p95 / p99", fmt.Sprintf("%s / %s / %s (n=%d)", f1(pct(r.pings, 50)), f1(pct(r.pings, 95)), f1(pct(r.pings, 99)), len(r.pings))},
		{"Ping/pong round trip, server-measured (ms) p50 / p95 / p99", fmt.Sprintf("%s / %s / %s (n=%d)", f1(pct(r.serverRTT, 50)), f1(pct(r.serverRTT, 95)), f1(pct(r.serverRTT, 99)), len(r.serverRTT))},
		{"Dashboard snapshots: rate (Hz) / gap p95 / gap max (ms)", fmt.Sprintf("%s / %s / %s", f1(snapHz), f1(pct(r.snapGap, 95)), f1(pct(r.snapGap, 100)))},
		{"Snapshot size (kB) median / max", fmt.Sprintf("%s / %s", f1(pct(r.snapBytes, 50)/1024), f1(pct(r.snapBytes, 100)/1024))},
		{"Dashboard mode; nodes in last snapshot (ours)", fmt.Sprintf("%s; %d (%d)", r.mode, r.snapNodes, r.snapOurs)},
	}
	if len(r.detectMs) > 0 {
		rows = append(rows, [2]string{"stats.detectMs p50 / p95 / max", fmt.Sprintf("%s / %s / %s", f1(pct(r.detectMs, 50)), f1(pct(r.detectMs, 95)), f1(pct(r.detectMs, 100)))})
	} else {
		rows = append(rows, [2]string{"stats.detectMs", "not reported by this server build"})
	}
	rows = append(rows, [2]string{"GET /api/status (ms) p50 / p95 / max", fmt.Sprintf("%s / %s / %s (errors %d)", f1(pct(r.statusMs, 50)), f1(pct(r.statusMs, 95)), f1(pct(r.statusMs, 100)), r.statusErr)})
	if r.firstErr != "" {
		rows = append(rows, [2]string{"First phone error", r.firstErr})
	}
	if r.dashErr != "" {
		rows = append(rows, [2]string{"Dashboard client error", r.dashErr})
	}
	if r.serverDown {
		rows = append(rows, [2]string{"Server after the run", "NOT answering /api/status"})
	}
	return rows
}

func (r *result) print() {
	fmt.Printf("\n== %d phones, %s (ramp %s) ==\n", r.n, r.dur, r.ramp)
	for _, row := range r.rows() {
		fmt.Printf("  %-62s %s\n", row[0], row[1])
	}
	fmt.Println()
}

func cpuModel() string {
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return runtime.GOARCH
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), ":"); ok && strings.TrimSpace(k) == "model name" {
			return strings.TrimSpace(v)
		}
	}
	return runtime.GOARCH
}

func kernel() string {
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return runtime.GOOS
	}
	return runtime.GOOS + " " + strings.TrimSpace(string(b))
}

func writeReport(path, target string, rs []*result) error {
	var b strings.Builder
	b.WriteString("# Pulse load test\n\n")
	fmt.Fprintf(&b, "Generated %s by `go run ./server/cmd/loadtest` (`make loadtest N=…`) against `%s`.\n\n", time.Now().Format(time.RFC3339), target)
	fmt.Fprintf(&b, "**Machine:** %s, %d hardware threads, %s, %s. The load generator ran on the same machine as the server, so they shared the CPU.\n\n", cpuModel(), runtime.NumCPU(), kernel(), runtime.Version())
	b.WriteString("Each fake phone sends a hello with a position (spread evenly over the venue), answers the server's clock-sync pings at once, and streams a 100 ms motion summary of calm standing (10 messages/s), like the phone page. Phones join evenly over the ramp; \"steady state\" starts 2 s after the ramp ends. One dashboard client times snapshots and `GET /api/status` is polled every second.\n\n")
	b.WriteString("- **Ping delivery** is the time from the server stamping a clock-sync ping to the phone receiving it (one clock, one machine): how long the server's send path takes under load. The server stamps pings in whole milliseconds, so 0 means under 1 ms.\n")
	b.WriteString("- **Round trip, server-measured** is the server's own `rtt` per phone (the latest ping → pong), read from snapshot `nodes[].rtt` once a second.\n\n")
	for _, r := range rs {
		if r.mode == "sim" || r.mode == "replay" {
			fmt.Fprintf(&b, "**Note:** the server was in `%s` mode during the test, so the dashboard snapshots showed that pipeline, not these phones: the snapshot size and node count are the %s's, the server-measured round trip of the load-test phones is not visible, and the server's `msgPerSec` includes the %s's messages. The load-test phones still went through the hub and the live detector underneath.\n\n", r.mode, r.mode, r.mode)
			break
		}
	}
	b.WriteString("| | " + func() string {
		var h []string
		for _, r := range rs {
			h = append(h, fmt.Sprintf("%d phones, %s", r.n, r.dur))
		}
		return strings.Join(h, " | ")
	}() + " |\n|---|" + strings.Repeat("---|", len(rs)) + "\n")
	rows := make([][][2]string, len(rs))
	for i, r := range rs {
		rows[i] = r.rows()
	}
	// Union of row labels, in first-seen order.
	var labels []string
	seen := map[string]bool{}
	for _, rr := range rows {
		for _, row := range rr {
			if !seen[row[0]] {
				seen[row[0]] = true
				labels = append(labels, row[0])
			}
		}
	}
	for _, l := range labels {
		b.WriteString("| " + l + " |")
		for _, rr := range rows {
			v := ""
			for _, row := range rr {
				if row[0] == l {
					v = row[1]
				}
			}
			b.WriteString(" " + strings.ReplaceAll(v, "|", "/") + " |")
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
	for _, r := range rs {
		fmt.Fprintf(&b, "<details><summary>Timeline, %d phones</summary>\n\n```\n%s\n```\n</details>\n\n", r.n, strings.Join(r.timeline, "\n"))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}
