package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Drivera0/crowd-crush/server/internal/hub"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// Board-side measurements: the boards measuring the phones.
//
// Scanning for adverts in a web page needs a hidden Chrome flag. Two other
// ways turn the measurement around, so that the board measures the phone:
//
//   - Connect mode (src "conn"): any Android Chrome can connect to a zone
//     light over Bluetooth (one pop-up per board) and write its Pulse session
//     id to it. The board measures the signal strength of that connection.
//   - App phones (src "adv"): the Pulse Android app advertises the first 8
//     hex characters of the phone's session id. Every board hears it in the
//     scan it already runs; no connection, no cap.
//
// A zone light reports both in GET /links:
//
//	{"links":[{"id":"<session id>","rssi":-57,"age":1}],
//	 "heard":[{"id":"1a2b3c4d","rssi":-63,"age":1}]}
//
// (firmware with connect mode only answers a bare array of links). The
// server polls every online zone light's /links, about once a second while
// any board reports a phone and every few seconds otherwise, and merges the
// result with what phones scan themselves: the same ranges, the same
// geometry, the same BeaconFix. A board-side reading is of a phone's radio,
// not a board's, so it has its own 1 m reference (connTxPower1m in
// data/beacons.json), shared by both kinds.
//
// An id8 is matched to the connected phone whose session id starts with it;
// if two do, or none, it is ignored.
//
// Everything fails soft: a board that is offline, slow or runs older
// firmware (no /links) is simply left alone for a while.

const (
	linksEvery     = time.Second
	linksIdleMs    = 3000   // between polls of a board while no board reports a phone
	linksRetryMs   = 15_000 // before asking a board again that didn't answer /links
	linksTimeout   = 800 * time.Millisecond
	linksKeepMs    = 10_000 // a reading not refreshed for this long is forgotten
	linksMaxPhones = 256    // ids remembered at once
	linksMaxBody   = 8 << 10
	linksMaxPer    = 8  // links read from one board (the firmware caps at 3)
	heardMaxPer    = 64 // app phones read from one board (the firmware keeps 24)
)

// linkKey names one board-side reading of a phone: which board, which way.
type linkKey struct {
	beacon string
	src    string // protocol.BeaconSrcConn | protocol.BeaconSrcAdv
}

// linkObs is a board's latest reading of one phone.
type linkObs struct {
	rssi float64
	at   int64 // server ms the board measured it
}

// linkBoard is what the poller knows about one board.
type linkBoard struct {
	ok    bool  // answered /links last time
	links int   // phones it reported connected
	heard int   // app phones it reported hearing
	next  int64 // don't poll before this (server ms)
}

// beaconState is the app's beacon bookkeeping (App.bcn).
type beaconState struct {
	cfg    *protocol.BeaconConfig         // loaded on first use
	links  map[string]map[linkKey]linkObs // session id → board-side readings
	boards map[string]*linkBoard          // by hardware key
}

// linkTarget is one board to poll.
type linkTarget struct {
	key, beacon, url string
}

// boardLinks is a board's answer to GET /links.
type boardLinks struct {
	Links []protocol.BeaconLink `json:"links"`
	Heard []protocol.BeaconLink `json:"heard"`
}

// watchBeaconLinks polls the zone lights' /links until ctx ends.
func (a *App) watchBeaconLinks(ctx context.Context) {
	if !a.opt.Sign.Enabled() {
		return
	}
	client := &http.Client{Timeout: linksTimeout}
	t := time.NewTicker(linksEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.pollBeaconLinks(ctx, client)
		}
	}
}

// pollBeaconLinks asks every board that is due for its links, once.
func (a *App) pollBeaconLinks(ctx context.Context, client *http.Client) {
	now := hub.Now()
	a.mu.Lock()
	targets := a.linkTargetsLocked(now)
	a.mu.Unlock()
	if len(targets) == 0 {
		return
	}
	type result struct {
		t   linkTarget
		got boardLinks
		ok  bool
	}
	res := make([]result, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, ok := fetchLinks(ctx, client, t.url)
			res[i] = result{t, got, ok}
		}()
	}
	wg.Wait()
	now = hub.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, r := range res {
		a.beaconLinksLocked(r.t.key, r.t.beacon, r.got, r.ok, now)
	}
	a.linksSettleLocked(now)
}

// linkTargetsLocked lists the online zone lights whose next poll is due.
// Caller holds mu.
func (a *App) linkTargetsLocked(now int64) []linkTarget {
	names := map[string]string{}
	for _, b := range a.beaconBoardsLocked() {
		names[b.Key] = b.Name
	}
	var out []linkTarget
	for _, h := range a.hw {
		if h.Zone == "" || !h.Online || !(strings.HasPrefix(h.URL, "http://") || strings.HasPrefix(h.URL, "https://")) {
			continue // the sign neither scans nor takes connections; serial boards have no HTTP
		}
		if lb := a.bcn.boards[h.Zone]; lb != nil && now < lb.next {
			continue
		}
		out = append(out, linkTarget{key: h.Zone, beacon: names[h.Zone], url: strings.TrimRight(h.URL, "/") + "/links"})
	}
	return out
}

// fetchLinks is GET <board>/links: {"links":[…],"heard":[…]}, or a bare
// array of links. ok is false when the board didn't answer with either
// (offline, or older firmware).
func fetchLinks(ctx context.Context, client *http.Client, url string) (boardLinks, bool) {
	var got boardLinks
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return got, false
	}
	res, err := client.Do(req)
	if err != nil {
		return got, false
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return got, false
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, linksMaxBody))
	if err != nil {
		return got, false
	}
	if json.Unmarshal(b, &got) == nil {
		return got, true
	}
	got = boardLinks{}
	if json.Unmarshal(b, &got.Links) == nil {
		return got, true
	}
	return boardLinks{}, false
}

// usableReading reports whether a board's reading can be a range: a signal
// strength in range (0 = the board hasn't measured it yet) and not stale.
func usableReading(l protocol.BeaconLink) bool {
	return l.RSSI >= protocol.BeaconRSSIMin && l.RSSI <= protocol.BeaconRSSIMax && l.Age >= 0 && l.Age*1000 <= beaconStaleMs
}

// phoneByID8Locked is the connected live phone whose session id starts with
// id8; "" when none does, or more than one. Caller holds mu.
func (a *App) phoneByID8Locked(id8 string) string {
	id8 = strings.ToLower(id8)
	found := ""
	for id, m := range a.live.meta {
		if !m.connected || !strings.HasPrefix(strings.ToLower(id), id8) {
			continue
		}
		if found != "" {
			return "" // ambiguous
		}
		found = id
	}
	return found
}

// beaconLinksLocked takes one board's answer. Readings with an id that
// isn't a plausible session id (or, for app phones, isn't exactly one
// connected phone), a signal strength out of range or a stale age are
// dropped. Caller holds mu.
func (a *App) beaconLinksLocked(key, beacon string, got boardLinks, ok bool, now int64) {
	if a.bcn.boards == nil {
		a.bcn.boards = map[string]*linkBoard{}
	}
	lb := a.bcn.boards[key]
	if lb == nil {
		lb = &linkBoard{}
		a.bcn.boards[key] = lb
	}
	lb.ok, lb.links, lb.heard = ok, 0, 0
	if !ok {
		lb.next = now + linksRetryMs
		return
	}
	lb.next = 0
	keep := func(id, src string, l protocol.BeaconLink) {
		if a.bcn.links == nil {
			a.bcn.links = map[string]map[linkKey]linkObs{}
		}
		byBoard := a.bcn.links[id]
		if byBoard == nil {
			if len(a.bcn.links) >= linksMaxPhones {
				return
			}
			byBoard = map[linkKey]linkObs{}
			a.bcn.links[id] = byBoard
		}
		byBoard[linkKey{beacon, src}] = linkObs{rssi: l.RSSI, at: now - int64(l.Age*1000)}
	}
	for _, l := range got.Links[:min(len(got.Links), linksMaxPer)] {
		if !protocol.ValidLinkID(l.ID) {
			continue
		}
		lb.links++
		if usableReading(l) {
			keep(l.ID, protocol.BeaconSrcConn, l)
		}
	}
	for _, l := range got.Heard[:min(len(got.Heard), heardMaxPer)] {
		if !protocol.ValidID8(l.ID) {
			continue
		}
		lb.heard++
		if id := a.phoneByID8Locked(l.ID); id != "" && usableReading(l) {
			keep(id, protocol.BeaconSrcAdv, l)
		}
	}
}

// linksSettleLocked runs after a round of polls: forgets old readings, sets
// the pace of the next round and refreshes the fix of every live phone that
// has (or just lost) a board-side reading. Caller holds mu.
func (a *App) linksSettleLocked(now int64) {
	any := false
	for _, lb := range a.bcn.boards {
		any = any || (lb.ok && lb.links+lb.heard > 0)
	}
	for _, lb := range a.bcn.boards {
		if lb.ok && !any {
			lb.next = now + linksIdleMs
		}
	}
	for id, byBoard := range a.bcn.links {
		for k, l := range byBoard {
			if now-l.at > linksKeepMs {
				delete(byBoard, k)
			}
		}
		if m := a.live.meta[id]; m != nil && m.connected {
			a.beaconUpdateLocked(id, m, now)
		}
		if len(byBoard) == 0 {
			delete(a.bcn.links, id)
		}
	}
}
