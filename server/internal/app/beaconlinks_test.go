package app

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/hub"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/sign"
)

func TestValidLinkID(t *testing.T) {
	for id, want := range map[string]bool{
		"3f2b8c1e-9a4d-4e0b-8f6a-1c2d3e4f5a6b":  true, // a UUID: 36 characters
		"0123456789abcdef0123456789abcdef":      true,
		"diag-7Kq2":                             true,
		"":                                      false,
		"3f2b8c1e-9a4d-4e0b-8f6a-1c2d3e4f5a6bX": false, // 37
		"has space":                             false,
		"semi;colon":                            false,
		`quote"`:                                false,
		"<script>":                              false,
		"új":                                    false,
		"a/b":                                   false,
		"under_score":                           false,
	} {
		if got := protocol.ValidLinkID(id); got != want {
			t.Errorf("ValidLinkID(%q) = %v, want %v", id, got, want)
		}
	}
}

// link is what a board reports for a phone at (px, py) connected to it.
func link(id string, px, py float64, b board) protocol.BeaconLink {
	return protocol.BeaconLink{ID: id, RSSI: math.Round(rssiAt(px, py, b.x, b.y, 0)), Age: 1}
}

// TestBeaconLinksMerge: connections reported by the boards become ranges
// for the phone whose id was written, through the same geometry as scans.
func TestBeaconLinksMerge(t *testing.T) {
	a := beaconApp(t)
	now := hub.Now()
	feed := func(key string, b board, links ...protocol.BeaconLink) {
		a.mu.Lock()
		a.beaconLinksLocked(key, b.name, boardLinks{Links: links}, true, hub.Now())
		a.linksSettleLocked(hub.Now())
		a.mu.Unlock()
	}
	a.PhoneHelloAuto("phone-1", "Android")

	// Connected to board A only, far away: a ring, the phone stays unplaced.
	feed("A", boardA, link("phone-1", 9, 8, boardA))
	if m := towerMeta(t, a, "phone-1"); !m.unplaced {
		t.Fatal("one connection, 5 m away, placed the phone")
	}
	if d := towerMeta(t, a, "phone-1").bcn.fix; d.OK || len(d.Heard) != 1 || d.Heard[0].Src != protocol.BeaconSrcConn || math.Abs(d.Heard[0].Dist-5) > 0.4 {
		t.Errorf("fix from one connection: %+v", d)
	}
	// Connected to B too: a 1-D fix, which places a phone with no position.
	feed("B", boardB, link("phone-1", 9, 8, boardB))
	m := towerMeta(t, a, "phone-1")
	if m.unplaced || math.Abs(m.x-9) > 0.4 || m.y != 8 || m.src() != protocol.SrcBeacon {
		t.Errorf("two connections: %.2f, %.2f unplaced %v src %s", m.x, m.y, m.unplaced, m.src())
	}
	if _, _, _, dims, ok := a.BeaconFix("phone-1"); !ok || dims != 1 {
		t.Errorf("BeaconFix: dims %d ok %v", dims, ok)
	}
	// A third board on the map and connected: 2-D.
	if _, err := a.SetHardwarePos("C", boardC.x, boardC.y); err != nil {
		t.Fatal(err)
	}
	feed("C", boardC, link("phone-1", 9, 10, boardC))
	feed("A", boardA, link("phone-1", 9, 10, boardA))
	feed("B", boardB, link("phone-1", 9, 10, boardB))
	x, y, _, dims, ok := a.BeaconFix("phone-1")
	if !ok || dims != 2 || math.Hypot(x-9, y-10) > 0.6 {
		t.Errorf("three connections: %.2f, %.2f dims %d ok %v; want about 9, 10", x, y, dims, ok)
	}
	// A scan report for a board that is also connected: both are listed, the
	// board is one anchor (the two distances averaged), the fix stays put.
	a.PhoneBeacons("phone-1", seenAt(9, 10, boardA))
	d, _ := a.BeaconFixDetail("phone-1")
	srcs := map[string]int{}
	for _, h := range d.Heard {
		srcs[h.Name+"/"+h.Src]++
	}
	if len(d.Heard) != 4 || srcs["PULSE-A/scan"] != 1 || srcs["PULSE-A/conn"] != 1 || srcs["PULSE-B/conn"] != 1 || srcs["PULSE-C/conn"] != 1 {
		t.Errorf("scan + connections: %+v", d.Heard)
	}
	if d.Dims != 2 || math.Hypot(d.X-9, d.Y-10) > 0.6 {
		t.Errorf("scan + connections: fix %.2f, %.2f dims %d", d.X, d.Y, d.Dims)
	}
	// A phone-side and a board-side range that disagree meet in between.
	both, _ := a.LocateBeacons([]protocol.BeaconSeen{{Name: "PULSE-A", RSSI: -64}, {Name: "PULSE-B", RSSI: -64}}, "")
	a.mu.Lock()
	a.bcn.links["mix"] = map[linkKey]linkObs{{"PULSE-A", protocol.BeaconSrcAdv}: {rssi: -86, at: hub.Now()}}
	a.mu.Unlock()
	mixed, _ := a.LocateBeacons([]protocol.BeaconSeen{{Name: "PULSE-A", RSSI: -64}, {Name: "PULSE-B", RSSI: -64}}, "mix")
	if !(mixed.X > both.X+0.05) || len(mixed.Heard) != 3 {
		t.Errorf("A at 1 m by the phone and 10 m by the board: x %.2f, want further from A than %.2f; heard %+v", mixed.X, both.X, mixed.Heard)
	}

	// Bad links are dropped: a hostile id, an id that isn't a phone, a signal
	// the board hasn't measured yet, a stale reading.
	feed("A", boardA,
		protocol.BeaconLink{ID: `x"><script>`, RSSI: -50, Age: 0},
		protocol.BeaconLink{ID: "nobody-here", RSSI: -50, Age: 0},
		protocol.BeaconLink{ID: "phone-2", RSSI: 0, Age: 0},
		protocol.BeaconLink{ID: "phone-3", RSSI: -50, Age: 60},
		protocol.BeaconLink{ID: "phone-4", RSSI: math.NaN(), Age: 0},
	)
	a.mu.Lock()
	_, hostile := a.bcn.links[`x"><script>`]
	_, unmeasured := a.bcn.links["phone-2"]
	_, stale := a.bcn.links["phone-3"]
	_, nan := a.bcn.links["phone-4"]
	lb := *a.bcn.boards["A"]
	a.mu.Unlock()
	if hostile || unmeasured || stale || nan {
		t.Errorf("bad links kept: hostile %v unmeasured %v stale %v nan %v", hostile, unmeasured, stale, nan)
	}
	if !lb.ok || lb.links != 4 {
		t.Errorf("board A: %+v, want 4 links with a valid id", lb)
	}
	// The diagnostic page (no phone session) reads its own links by id.
	fix, err := a.LocateBeacons(nil, "nobody-here")
	if err != nil || len(fix.Heard) != 1 || fix.Heard[0].Name != "PULSE-A" || fix.Heard[0].Src != protocol.BeaconSrcConn {
		t.Errorf("locate by id: %+v, %v", fix, err)
	}
	if _, err := a.LocateBeacons(nil, "bad id!"); err == nil {
		t.Error("bad id accepted")
	}
	// Connect mode has its own 1 m reference.
	if err := a.SetBeaconConnTx(-58); err != nil {
		t.Fatal(err)
	}
	fix, _ = a.LocateBeacons([]protocol.BeaconSeen{{Name: "PULSE-B", RSSI: -58}}, "nobody-here")
	for _, h := range fix.Heard {
		want := map[string]float64{"PULSE-A": bcnModel.Distance(-50 - 6), "PULSE-B": bcnModel.Distance(-58)}[h.Name]
		if math.Abs(h.Dist-want) > 0.02 {
			t.Errorf("%s (%s) at %.1f dBm: %.2f m, want %.2f", h.Name, h.Src, h.RSSI, h.Dist, want)
		}
	}
	if info := a.BeaconInfo(); info.ConnTxPower1m != -58 || info.Model.TxPower1m != -64 || info.Service == "" || !info.Boards[0].Connectable || info.Boards[0].Links != 4 {
		t.Errorf("info = %+v", info)
	}
	if err := a.SetBeaconConnTx(-5); err == nil {
		t.Error("connTxPower1m -5 accepted")
	}

	// Links that stop being reported go stale, then are forgotten.
	a.mu.Lock()
	for _, byBoard := range a.bcn.links {
		for k, l := range byBoard {
			l.at = now - linksKeepMs - 1000
			byBoard[k] = l
		}
	}
	a.live.meta["phone-1"].bcn.scanAt = 0
	a.linksSettleLocked(hub.Now())
	left := len(a.bcn.links)
	a.mu.Unlock()
	if left != 0 {
		t.Errorf("%d phones still have links", left)
	}
	if _, _, _, _, ok := a.BeaconFix("phone-1"); ok {
		t.Error("fix survived its links")
	}
}

// TestBeaconLinksPoll: the poller asks online zone lights for /links, backs
// off from boards that don't have it, and slows down while nobody is connected.
func TestBeaconLinksPoll(t *testing.T) {
	var hitsA, hitsB atomic.Int64
	var body atomic.Value
	body.Store(`[]`)
	lightA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/links" {
			http.NotFound(w, r)
			return
		}
		hitsA.Add(1)
		fmt.Fprint(w, body.Load().(string))
	}))
	defer lightA.Close()
	// Board B runs older firmware: no /links.
	lightB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsB.Add(1)
		http.NotFound(w, r)
	}))
	defer lightB.Close()

	a := New(Options{Detect: detect.DefaultConfig(), RecordingsDir: t.TempDir(), DataDir: t.TempDir(),
		Sign: sign.New("A=" + lightA.URL + ",B=" + lightB.URL + ",C=http://127.0.0.1:9")})
	for k, p := range map[string][2]float64{"A": {4, 8}, "B": {12, 8}} {
		if _, err := a.SetHardwarePos(k, p[0], p[1]); err != nil {
			t.Fatal(err)
		}
	}
	a.mu.Lock()
	a.hw = []protocol.Hardware{
		{URL: lightA.URL, Zone: "A", Online: true, Kind: "zone-light", Beacon: "PULSE-A"},
		{URL: lightB.URL, Zone: "B", Online: true, Kind: "zone-light"},
		{URL: "http://127.0.0.1:9", Zone: "C", Online: false, Kind: "zone-light"},
		{URL: "serial:auto", Online: true, Kind: "sign"},
	}
	a.mu.Unlock()
	a.PhoneHelloAuto("phone-1", "Android")
	ctx := context.Background()
	client := &http.Client{Timeout: linksTimeout}

	// Nobody connected: both online lights are asked once, then left alone
	// (A for the idle interval, B for the retry interval).
	a.pollBeaconLinks(ctx, client)
	a.pollBeaconLinks(ctx, client)
	if hitsA.Load() != 1 || hitsB.Load() != 1 {
		t.Fatalf("idle: A asked %d times, B %d; want 1 and 1", hitsA.Load(), hitsB.Load())
	}
	info := a.BeaconInfo()
	if !info.Boards[0].Connectable || info.Boards[1].Connectable || info.Boards[2].Connectable {
		t.Errorf("connectable: %+v", info.Boards)
	}
	// A phone connects to A, 0.9 m away: polled every round from now on,
	// and the phone is placed next to the board.
	body.Store(`[{"id":"phone-1","rssi":-63,"age":0},{"id":"","rssi":-70,"age":2}]`)
	a.mu.Lock()
	a.bcn.boards["A"].next = 0 // the idle interval has passed
	a.mu.Unlock()
	a.pollBeaconLinks(ctx, client)
	a.pollBeaconLinks(ctx, client)
	a.pollBeaconLinks(ctx, client)
	if hitsA.Load() != 4 || hitsB.Load() != 1 {
		t.Errorf("connected: A asked %d times, B %d; want 4 and 1", hitsA.Load(), hitsB.Load())
	}
	m := towerMeta(t, a, "phone-1")
	if d := math.Hypot(m.x-4, m.y-8); m.unplaced || d > towerSpreadMax+0.01 || m.src() != protocol.SrcBeacon {
		t.Errorf("phone connected 0.9 m from A: %.2f, %.2f unplaced %v src %s", m.x, m.y, m.unplaced, m.src())
	}
	if fix, ok := a.BeaconFixDetail("phone-1"); !ok || fix.Near != "PULSE-A" || fix.Dims != 0 || !strings.Contains(fix.Note, "next to") {
		t.Errorf("fix = %+v, %v", fix, ok)
	}
	// The board answers nonsense: treated like no /links, nothing breaks.
	body.Store(`<html>404</html>`)
	a.pollBeaconLinks(ctx, client)
	a.pollBeaconLinks(ctx, client)
	if hitsA.Load() != 5 {
		t.Errorf("after a bad answer A was asked %d times, want 5 (then backed off)", hitsA.Load())
	}
	if a.BeaconInfo().Boards[0].Connectable {
		t.Error("board A still connectable after a bad answer")
	}
}

// TestBeaconHeardPhones: a board hears the Android app's advert (8 hex
// characters of the session id). It becomes a range for the one connected
// phone whose id starts with them; ambiguous or unknown ids are ignored.
func TestBeaconHeardPhones(t *testing.T) {
	for id, want := range map[string]bool{"1a2b3c4d": true, "1A2B3C4D": true, "1a2b3c4": false, "1a2b3c4d5": false, "1a2b3c4g": false, "": false, "1a2b-c4d": false} {
		if got := protocol.ValidID8(id); got != want {
			t.Errorf("ValidID8(%q) = %v, want %v", id, got, want)
		}
	}
	a := beaconApp(t)
	const app1 = "1a2b3c4d-0000-4000-8000-000000000001"
	const twinA, twinB = "deadbeef-0000-4000-8000-00000000000a", "deadbeef-0000-4000-8000-00000000000b"
	for _, id := range []string{app1, twinA, twinB} {
		a.PhoneHelloAuto(id, "Android app")
	}
	a.PhoneHelloAuto("99999999-gone", "Android app")
	a.PhoneGone("99999999-gone")
	heard := func(id string, px, py float64, b board) protocol.BeaconLink {
		return protocol.BeaconLink{ID: id, RSSI: math.Round(rssiAt(px, py, b.x, b.y, 0)), Age: 0}
	}
	feed := func(key string, b board, got boardLinks) {
		a.mu.Lock()
		a.beaconLinksLocked(key, b.name, got, true, hub.Now())
		a.linksSettleLocked(hub.Now())
		a.mu.Unlock()
	}
	feed("A", boardA, boardLinks{Heard: []protocol.BeaconLink{
		heard("1A2B3C4D", 7, 8, boardA), // upper case is the same phone
		heard("deadbeef", 7, 8, boardA), // two phones start with this: ignored
		heard("99999999", 7, 8, boardA), // that phone has left
		heard("0badf00d", 7, 8, boardA), // nobody
		{ID: "not-hex!", RSSI: -60},
	}})
	feed("B", boardB, boardLinks{Heard: []protocol.BeaconLink{heard("1a2b3c4d", 7, 8, boardB)}})
	a.mu.Lock()
	n := len(a.bcn.links)
	lb := *a.bcn.boards["A"]
	a.mu.Unlock()
	if n != 1 {
		t.Errorf("%d phones have readings, want only %s", n, app1)
	}
	if lb.heard != 4 || lb.links != 0 {
		t.Errorf("board A: %+v, want 4 heard", lb)
	}
	m := towerMeta(t, a, app1)
	if m.unplaced || math.Abs(m.x-7) > 0.4 || m.y != 8 || m.src() != protocol.SrcBeacon {
		t.Errorf("app phone heard by A and B: %.2f, %.2f unplaced %v src %s", m.x, m.y, m.unplaced, m.src())
	}
	d, ok := a.BeaconFixDetail(app1)
	if !ok || d.Dims != 1 || len(d.Heard) != 2 || d.Heard[0].Src != protocol.BeaconSrcAdv {
		t.Errorf("fix = %+v, %v", d, ok)
	}
	for _, id := range []string{twinA, twinB} {
		if m := towerMeta(t, a, id); !m.unplaced {
			t.Errorf("ambiguous id8 placed %s", id)
		}
	}
	// The board-side reference constant applies to adverts as to connections.
	if err := a.SetBeaconConnTx(-70); err != nil {
		t.Fatal(err)
	}
	fix, _ := a.LocateBeacons(nil, app1)
	if want := (protocol.BeaconModel{TxPower1m: -70, PathLossN: 2.2}).Distance(math.Round(rssiAt(7, 8, 4, 8, 0))); len(fix.Heard) != 2 || math.Abs(fix.Heard[0].Dist-want) > 0.02 {
		t.Errorf("heard = %+v, want A at %.2f m", fix.Heard, want)
	}
	// Connected to A as well, measured later: per board the fresher reading counts, once.
	a.mu.Lock()
	a.bcn.links[app1][linkKey{"PULSE-A", protocol.BeaconSrcConn}] = linkObs{rssi: -50, at: hub.Now() + 50}
	a.mu.Unlock()
	fix, _ = a.LocateBeacons(nil, app1)
	if len(fix.Heard) != 2 || fix.Heard[0].Name != "PULSE-A" || fix.Heard[0].Src != protocol.BeaconSrcConn || fix.Heard[0].RSSI != -50 {
		t.Errorf("fresher connection reading: %+v", fix.Heard)
	}
	if info := a.BeaconInfo(); info.Boards[0].Heard != 4 {
		t.Errorf("info board A = %+v", info.Boards[0])
	}
}

// The poller reads both answer shapes: {"links","heard"} and a bare array.
func TestFetchLinksShapes(t *testing.T) {
	var body atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body.Load().(string))
	}))
	defer srv.Close()
	client := &http.Client{Timeout: linksTimeout}
	body.Store(`{"links":[{"id":"abc","rssi":-57,"age":1}],"heard":[{"id":"1a2b3c4d","rssi":-63,"age":0},{"id":"0badf00d","rssi":-80,"age":2}]}`)
	got, ok := fetchLinks(context.Background(), client, srv.URL)
	if !ok || len(got.Links) != 1 || len(got.Heard) != 2 || got.Heard[0].ID != "1a2b3c4d" || got.Heard[0].RSSI != -63 {
		t.Errorf("object: %+v, %v", got, ok)
	}
	body.Store(`[{"id":"abc","rssi":-57,"age":1}]`)
	if got, ok = fetchLinks(context.Background(), client, srv.URL); !ok || len(got.Links) != 1 || len(got.Heard) != 0 {
		t.Errorf("array: %+v, %v", got, ok)
	}
	body.Store(`try /level?v=red or /pulse`)
	if _, ok = fetchLinks(context.Background(), client, srv.URL); ok {
		t.Error("plain text accepted")
	}
}
