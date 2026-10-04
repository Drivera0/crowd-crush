// Package sign drives physical warning lights over HTTP:
// GET <url>/level?v=calm|yellow|red&zone=B, 1 s timeout, fire and forget.
//
// SIGN_URL is a comma-separated list. A plain URL shows the worst zone (the
// Arduino matrix sign); "A=http://…" shows only zone A (an ESP32 zone light):
//
//	SIGN_URL=http://192.168.4.20,A=http://192.168.4.21,B=http://192.168.4.22
package sign

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type state struct{ level, zone string }

// retryDelay is the pause before retrying a failed send.
var retryDelay = 300 * time.Millisecond

type target struct {
	zone string // "" = follow the worst zone
	base string
	http *http.Client

	mu      sync.Mutex
	last    string
	pending chan state
}

// Client sends level changes to every configured sign. A nil client or one
// without URLs is a no-op.
type Client struct {
	targets []*target
}

// New parses a SIGN_URL value.
func New(spec string) *Client {
	c := &Client{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		t := &target{http: &http.Client{Timeout: time.Second}, pending: make(chan state, 1)}
		if z, u, ok := strings.Cut(part, "="); ok && !strings.Contains(z, "/") {
			t.zone, part = strings.ToUpper(strings.TrimSpace(z)), strings.TrimSpace(u)
		}
		if !strings.Contains(part, "://") {
			part = "http://" + part
		}
		t.base = strings.TrimRight(part, "/")
		c.targets = append(c.targets, t)
		go t.loop()
	}
	return c
}

// Enabled reports whether any sign is configured.
func (c *Client) Enabled() bool { return c != nil && len(c.targets) > 0 }

// Describe lists the signs for logs.
func (c *Client) Describe() string {
	if !c.Enabled() {
		return "none"
	}
	var parts []string
	for _, t := range c.targets {
		who := "worst zone"
		if t.zone != "" {
			who = "zone " + t.zone
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", t.base, who))
	}
	return strings.Join(parts, ", ")
}

// Zones lists the zone keys of the per-zone lights (A, B, …), in SIGN_URL order.
func (c *Client) Zones() []string {
	if !c.Enabled() {
		return nil
	}
	var out []string
	for _, t := range c.targets {
		if t.zone != "" {
			out = append(out, t.zone)
		}
	}
	return out
}

// Status is what a board said when probed.
type Status struct {
	URL    string
	Zone   string // "" = follows the worst zone
	Online bool
	Err    string
	// From GET /pulse, when the firmware has it (older sign firmware only
	// answers /level, which still counts as online).
	Pulse *Pulse
}

// Pulse is a board's GET /pulse report.
type Pulse struct {
	Kind   string `json:"kind"`
	Level  string `json:"level"`
	RSSI   int    `json:"rssi"`
	Uptime int64  `json:"uptime"`
	BLE    *struct {
		Devices int   `json:"devices"`
		Near    int   `json:"near"`
		Scans   int64 `json:"scans"`
		Age     int64 `json:"age"`
	} `json:"ble"`
}

// probeClient has no timeout of its own; Probe's context bounds it.
var probeClient = &http.Client{}

// Probe asks every board for GET /pulse, concurrently, within ctx.
func (c *Client) Probe(ctx context.Context) []Status {
	if !c.Enabled() {
		return nil
	}
	out := make([]Status, len(c.targets))
	var wg sync.WaitGroup
	for i, t := range c.targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = t.probe(ctx)
		}()
	}
	wg.Wait()
	return out
}

func (t *target) probe(ctx context.Context) Status {
	st := Status{URL: t.base, Zone: t.zone}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.base+"/pulse", nil)
	if err != nil {
		st.Err = err.Error()
		return st
	}
	// Status checks use the caller's deadline, not the 1 s alert-send timeout:
	// the Uno R4 sign sometimes takes over a second to answer.
	resp, err := probeClient.Do(req)
	if err != nil {
		st.Err = "not reachable"
		return st
	}
	defer resp.Body.Close()
	st.Online = true // any HTTP answer means the board is up
	if resp.StatusCode == http.StatusOK {
		var p Pulse
		if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&p) == nil && p.Kind != "" {
			st.Pulse = &p
		}
	}
	return st
}

// Update shows the current levels: per-zone signs get their zone, the others
// get the worst zone. Repeats are skipped.
func (c *Client) Update(levels map[string]string, worstLevel, worstZone string) {
	if !c.Enabled() {
		return
	}
	for _, t := range c.targets {
		if t.zone == "" {
			t.set(state{worstLevel, worstZone}, false)
		} else if lv, ok := levels[t.zone]; ok {
			t.set(state{lv, t.zone}, false)
		}
	}
}

// Set shows one level on every worst-zone sign (and that zone's own sign).
func (c *Client) Set(level, zone string) {
	c.Update(map[string]string{zone: level}, level, zone)
}

// Force sends a state even if it matches the last one (test alerts).
func (c *Client) Force(level, zone string) {
	if !c.Enabled() {
		return
	}
	for _, t := range c.targets {
		if t.zone == "" || t.zone == zone {
			t.set(state{level, zone}, true)
		}
	}
}

// Check calls every sign once and reports the first failure.
func (c *Client) Check(ctx context.Context) error {
	for _, t := range c.targets {
		if err := t.send(ctx, state{"calm", t.zone}); err != nil {
			return fmt.Errorf("%s: %w", t.base, err)
		}
	}
	return nil
}

func (t *target) set(s state, force bool) {
	key := s.level + "/" + s.zone
	t.mu.Lock()
	if key == t.last && !force {
		t.mu.Unlock()
		return
	}
	t.last = key
	t.mu.Unlock()
	// Only the newest state matters: a slow sign never builds a queue.
	for {
		select {
		case t.pending <- s:
			return
		default:
			select {
			case <-t.pending:
			default:
			}
		}
	}
}

func (t *target) loop() {
	for s := range t.pending {
		var err error
		// The Uno R4 serves one connection at a time and can reset a request
		// that lands while it is still closing the previous one, so a failed
		// send gets one quick retry unless a newer state is already waiting.
		for attempt := 0; attempt < 2; attempt++ {
			if attempt > 0 {
				if len(t.pending) > 0 {
					break
				}
				time.Sleep(retryDelay)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			err = t.send(ctx, s)
			cancel()
			if err == nil {
				break
			}
		}
		if err != nil {
			log.Printf("sign %s: %v", t.base, err)
			t.mu.Lock()
			t.last = "" // retry on the next update
			t.mu.Unlock()
		}
	}
}

func (t *target) send(ctx context.Context, s state) error {
	u := fmt.Sprintf("%s/level?v=%s&zone=%s", t.base, url.QueryEscape(s.level), url.QueryEscape(s.zone))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := t.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sign returned %s", resp.Status)
	}
	return nil
}
