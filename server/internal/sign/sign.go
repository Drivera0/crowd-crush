// Package sign drives physical warning lights over HTTP:
// GET <url>/level?v=calm|yellow|red&zone=B, 1 s timeout, fire and forget.
//
// SIGN_URL is a comma-separated list. A plain URL shows the worst zone (the
// Arduino matrix sign); "A=http://…" shows only zone A (an ESP32 zone light):
//
//	SIGN_URL=http://192.168.4.20,A=http://192.168.4.21,B=http://192.168.4.22
//
// A board plugged into this computer by USB needs no Wi-Fi: "serial:auto"
// (the sign), "A=serial:auto" (zone light A), or a named port
// ("serial:/dev/cu.usbmodem1101", "B=serial:COM9") writes "L <level> <zone>"
// lines down the cable instead (serial.go). serial:auto tells the boards
// apart by asking each port who it is:
//
//	SIGN_URL=serial:auto,A=serial:auto,B=serial:auto
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
	base string // HTTP base URL, or "serial:<port|auto>"
	http *http.Client
	ser  *serialLink // nil for HTTP targets

	mu      sync.Mutex
	last    string
	cur     *state // latest state asked for (restored after a test)
	pending chan state
}

// Client sends level changes to every configured sign. A nil client or one
// without URLs is a no-op.
type Client struct {
	targets []*target
	usb     *usbHub // shared by the serial targets
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
		if port, ok := cutPrefixFold(part, "serial:"); ok {
			port = strings.TrimSpace(port)
			if port == "" {
				port = "auto"
			}
			t.base = "serial:" + port
			if c.usb == nil {
				c.usb = newUSBHub()
			}
			t.ser = newSerialLink(port, t.zone, c.usb)
			c.targets = append(c.targets, t)
			go t.loop()
			continue
		}
		if !strings.Contains(part, "://") {
			part = "http://" + part
		}
		t.base = strings.TrimRight(part, "/")
		c.targets = append(c.targets, t)
		go t.loop()
	}
	// Serial targets start once all are known, so serial:auto knows from the
	// first scan which boards are wanted (and opens each port only once).
	for _, t := range c.targets {
		if t.ser != nil {
			go t.ser.run()
		}
	}
	return c
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return s, false
}

// Close stops the serial targets and closes their ports (HTTP targets need
// nothing). The client must not be used afterwards.
func (c *Client) Close() {
	if c != nil && c.usb != nil {
		c.usb.close()
	}
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
		name := t.base
		if t.ser != nil {
			name = t.ser.describe()
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", name, who))
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

// Keys lists every board's key: "sign" for the worst-zone signs (once),
// then the zone-light keys.
func (c *Client) Keys() []string {
	if !c.Enabled() {
		return nil
	}
	var out []string
	for _, t := range c.targets {
		if t.zone == "" {
			out = append(out, "sign")
			break
		}
	}
	return append(out, c.Zones()...)
}

// Status is what a board said when probed.
type Status struct {
	URL    string
	Zone   string // "" = follows the worst zone
	Online bool
	Err    string
	// Port is the serial port in use (serial targets only, e.g. serial:auto
	// → /dev/cu.usbmodem1101).
	Port string
	// Link is how the server reached the board: "usb" or "wifi" ("" when offline).
	Link string
	// From GET /pulse (or the "S" reply over serial), when the firmware has it (older sign firmware only
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
	// Name is the board's Bluetooth beacon name (PULSE-A); MAC the short
	// address suffix it reports.
	Name  string `json:"name"`
	MAC   string `json:"mac"`
	Peers []Peer `json:"peers"`
	// Zone is the zone a zone light shows (learned from the server, kept in
	// its flash); the sign's is the zone of its last alert.
	Zone string `json:"zone"`
	// FW is the firmware build id ("1a2b3c4 2026-10-04": content hash of the
	// sketch + build date), "dev" for a build made by hand; "" = firmware
	// older than build ids.
	FW string `json:"fw"`
	// WiFi: joined a network; SSID the one it is on or trying; IP its address.
	WiFi *bool  `json:"wifi"`
	SSID string `json:"ssid"`
	IP   string `json:"ip"`
}

// Peer is another Pulse board's beacon as this board hears it.
type Peer struct {
	Name string  `json:"name"`
	RSSI int     `json:"rssi"`
	Dist float64 `json:"dist"` // m, the board's log-distance estimate
	Age  int64   `json:"age"`  // s since last heard
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
	if t.ser != nil {
		t.ser.probe(ctx, &st)
		return st
	}
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
	st.Online, st.Link = true, "wifi" // any HTTP answer means the board is up
	if resp.StatusCode == http.StatusOK {
		var p Pulse
		if json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&p) == nil && p.Kind != "" {
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

// ForceAlert re-sends an alert's level even if it matches the last one:
// to the worst-zone signs (labelled with zone) when toSign, and to the
// zone light keyed light when light is not "".
func (c *Client) ForceAlert(level, zone string, toSign bool, light string) {
	if !c.Enabled() {
		return
	}
	for _, t := range c.targets {
		switch {
		case t.zone == "" && toSign:
			t.set(state{level, zone}, true)
		case t.zone != "" && t.zone == light:
			t.set(state{level, light}, true)
		}
	}
}

// Check calls every sign once and reports the first failure.
func (c *Client) Check(ctx context.Context) error {
	for _, t := range c.targets {
		if t.ser != nil {
			t.ser.waitOpen(ctx) // the port opens in the background
		}
		if err := t.send(ctx, state{"calm", t.zone}); err != nil {
			return fmt.Errorf("%s: %w", t.base, err)
		}
	}
	return nil
}

func (t *target) set(s state, force bool) {
	key := s.level + "/" + s.zone
	t.mu.Lock()
	t.cur = &s
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
	if t.ser != nil {
		return t.ser.send(s)
	}
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
