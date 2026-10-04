// Command sim connects fake phones to the server over the real WebSocket,
// each with its own clock offset and network jitter.
//
//	go run ./server/cmd/sim -scenario wave                 # 24 phones in a crowd
//	go run ./server/cmd/sim -layout line -n 8 -scenario wave
//	go run ./server/cmd/sim -n 40 -scenario gather         # a crowd packs in front of the stage
//	go run ./server/cmd/sim -layout line -n 8 -scenario wave -out recordings/sim-wave.jsonl -duration 70s
//
// Scenarios: calm, walk, dance, handle, shove, wave, wave-jump, gather, and
// the false-positive checks sway, sway-slow, mexican, walkpast, procession,
// march, pocket, bump, jump-stagger (see internal/sim).
//
// Layouts: crowd (default: dense groups plus stragglers, wandering slowly
// with -move, sending pos when they've moved ≥ 0.15 m, at most 2 Hz) or
// line (the original demo: a rows×cols grid 0.6 m apart).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"

	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/sim"
	"github.com/Drivera0/crowd-crush/server/internal/store"
)

func main() {
	n := flag.Int("n", 0, "number of phones (default 24 in a crowd, 8 in a line)")
	scenario := flag.String("scenario", "wave", fmt.Sprint("one of ", sim.Scenarios))
	url := flag.String("url", "ws://localhost:8080/ws/phone", "server phone WebSocket")
	layout := flag.String("layout", sim.LayoutCrowd, "crowd or line")
	move := flag.Bool("move", true, "crowd: phones wander slowly and send pos updates")
	venueW := flag.Float64("venue-w", 24, "venue width (m); match the server")
	venueH := flag.Float64("venue-h", 16, "venue height (m); match the server")
	rows := flag.Int("rows", 1, "line: grid rows (phones fill row by row)")
	cols := flag.Int("cols", 0, "line: grid cols (default: n/rows)")
	dur := flag.Duration("duration", 0, "stop after this long (0 = run until Ctrl-C)")
	seed := flag.Int64("seed", time.Now().UnixNano(), "random seed")
	maxOffset := flag.Duration("offset", 3*time.Second, "max fake clock offset per phone (±)")
	jitter := flag.Duration("jitter", 40*time.Millisecond, "max one-way network jitter")
	out := flag.String("out", "", "write a recording to this JSONL file instead of connecting")
	flag.Parse()

	if *n <= 0 {
		*n = 24
		if *layout == sim.LayoutLine {
			*n = 8
		}
	}
	if *cols == 0 {
		*cols = (*n + *rows - 1) / *rows
	}
	lay := sim.Layout{Kind: *layout, Rows: *rows, Cols: *cols, VenueW: *venueW, VenueH: *venueH, Move: *move}
	sc, err := sim.NewLayout(*scenario, *n, *seed, lay)
	if err != nil {
		log.Fatal(err)
	}

	if *out != "" {
		if *dur == 0 {
			*dur = 70 * time.Second
		}
		if err := writeRecording(sc, *out, *dur); err != nil {
			log.Fatal(err)
		}
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *dur > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *dur)
		defer cancel()
	}
	rng := rand.New(rand.NewSource(*seed))
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < sc.N(); i++ {
		ph := &fakePhone{
			i:      i,
			id:     fmt.Sprintf("sim-%02d-%04x", i, rng.Intn(1<<16)),
			sc:     sc,
			offset: time.Duration(rng.Int63n(int64(2**maxOffset)+1)) - *maxOffset,
			jitter: *jitter,
			start:  start,
			rng:    rand.New(rand.NewSource(rng.Int63())),
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ph.run(ctx, *url)
		}()
	}
	log.Printf("%d phones running %q in a %s layout against %s", sc.N(), *scenario, *layout, *url)
	wg.Wait()
}

type fakePhone struct {
	i      int
	id     string
	sc     *sim.Scenario
	offset time.Duration
	jitter time.Duration
	start  time.Time
	rng    *rand.Rand
	mu     sync.Mutex // guards rng
}

func (p *fakePhone) delay() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.jitter <= 0 {
		return 0
	}
	d := 5*time.Millisecond + time.Duration(p.rng.Int63n(int64(p.jitter)))
	if p.rng.Float64() < 0.03 { // the occasional Wi-Fi hiccup
		d += 150 * time.Millisecond
	}
	return d
}

// phoneNow is the fake phone's clock.
func (p *fakePhone) phoneNow() int64 { return time.Now().Add(p.offset).UnixMilli() }

func (p *fakePhone) run(ctx context.Context, url string) {
	for ctx.Err() == nil {
		if err := p.session(ctx, url); err != nil && ctx.Err() == nil {
			log.Printf("phone %d: %v (reconnecting)", p.i, err)
			select {
			case <-time.After(time.Second):
			case <-ctx.Done():
			}
		}
	}
}

func (p *fakePhone) session(ctx context.Context, url string) error {
	ws, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return err
	}
	defer ws.CloseNow()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// One writer goroutine; messages leave after their simulated delay.
	type outMsg struct {
		at time.Time
		b  []byte
	}
	outq := make(chan outMsg, 256)
	send := func(v any) {
		b, _ := json.Marshal(v)
		select {
		case outq <- outMsg{time.Now().Add(p.delay()), b}:
		default:
		}
	}
	go func() {
		var last time.Time
		for {
			select {
			case m := <-outq:
				if m.at.Before(last) {
					m.at = last // a TCP stream keeps order
				}
				last = m.at
				time.Sleep(time.Until(m.at))
				if err := ws.Write(ctx, websocket.MessageText, m.b); err != nil {
					cancel()
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Where the phone is now (it may have walked while disconnected).
	t := time.Since(p.start).Seconds()
	x, y := p.sc.PosAt(p.i, t)
	x, y = r2(x), r2(y)
	b, _ := json.Marshal(protocol.Hello{Type: protocol.TypeHello, ID: p.id, X: &x, Y: &y, UA: "sim"})
	if err := ws.Write(ctx, websocket.MessageText, b); err != nil {
		return err
	}
	lastX, lastY, lastPos := x, y, time.Now()

	// Reader: answer pings like a phone would, after the downlink delay.
	go func() {
		defer cancel()
		for {
			_, b, err := ws.Read(ctx)
			if err != nil {
				return
			}
			var ping protocol.Ping
			if json.Unmarshal(b, &ping) == nil && ping.Type == protocol.TypePing {
				go func(t0 int64) {
					time.Sleep(p.delay())
					send(protocol.Pong{Type: protocol.TypePong, T0: t0, T1: p.phoneNow()})
				}(ping.T0)
			}
		}
	}()

	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case now := <-tick.C:
			t := now.Sub(p.start).Seconds() - 0.1
			ax, ay, az, rot := p.sc.Summary(p.i, t)
			send(protocol.Motion{Type: protocol.TypeMotion, T: p.phoneNow(), AX: r3(ax), AY: r3(ay), AZ: r3(az), Rot: r3(rot)})
			// Walked ≥ 0.15 m: tell the server, at most twice a second.
			if now.Sub(lastPos) >= 500*time.Millisecond {
				if x, y := p.sc.PosAt(p.i, t); math.Hypot(x-lastX, y-lastY) >= minMove {
					lastX, lastY, lastPos = x, y, now
					send(protocol.Pos{Type: protocol.TypePos, X: r2(x), Y: r2(y)})
				}
			}
		}
	}
}

func r3(v float64) float64 { return float64(int64(v*1000)) / 1000 }

func r2(v float64) float64 { return math.Round(v*100) / 100 }

// minMove is how far (m) a phone walks before it sends a pos update.
const minMove = 0.15

// writeRecording generates a recording offline (perfect clocks, no network).
func writeRecording(sc *sim.Scenario, path string, dur time.Duration) error {
	w, err := store.CreateJSONL(path)
	if err != nil {
		return err
	}
	t0 := time.Now().Truncate(time.Second).UnixMilli()
	w.Write(store.Record{K: store.KindMeta, T: t0, Label: "sim-" + sc.Name, W: sc.Layout.VenueW, H: sc.Layout.VenueH})
	last := make([][2]float64, sc.N())
	for i := 0; i < sc.N(); i++ {
		x, y := sc.Pos(i)
		last[i] = [2]float64{x, y}
		id := fmt.Sprintf("sim-%02d", i)
		w.Write(store.Record{K: store.KindHello, T: t0, ID: id, X: store.F(r2(x)), Y: store.F(r2(y)), UA: "sim"})
		w.Write(store.Record{K: store.KindSync, T: t0, ID: id, RTT: 20 + int64(i)})
	}
	nextPos := t0 + 500
	for _, e := range sc.Generate(t0, dur.Seconds()) {
		for sc.Moves() && e.T >= nextPos {
			for i := range last {
				x, y := sc.PosAt(i, float64(nextPos-t0)/1000)
				if math.Hypot(x-last[i][0], y-last[i][1]) >= minMove {
					last[i] = [2]float64{x, y}
					w.Write(store.Record{K: store.KindPos, T: nextPos, ID: fmt.Sprintf("sim-%02d", i), X: store.F(r2(x)), Y: store.F(r2(y))})
				}
			}
			nextPos += 500
		}
		w.Write(store.Record{K: store.KindM, T: e.T, ID: fmt.Sprintf("sim-%02d", e.Phone), CT: e.T,
			AX: r3(e.AX), AY: r3(e.AY), AZ: r3(e.AZ), Rot: r3(e.Rot)})
	}
	if err := w.Close(); err != nil {
		return err
	}
	log.Printf("wrote %d records to %s", w.Count(), path)
	return nil
}
