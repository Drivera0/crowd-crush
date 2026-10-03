// Package sign drives the Arduino Uno R4 WiFi sign over HTTP:
// GET <SIGN_URL>/level?v=calm|yellow|red&zone=B, 1 s timeout, fire and forget.
package sign

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Client sends level changes to the sign. A nil or URL-less client is a no-op.
type Client struct {
	base string
	http *http.Client

	mu      sync.Mutex
	last    string
	pending chan [2]string
}

// New creates a client; base is e.g. http://192.168.1.50.
func New(base string) *Client {
	c := &Client{base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: time.Second}, pending: make(chan [2]string, 1)}
	if c.base != "" {
		go c.loop()
	}
	return c
}

// Enabled reports whether a sign URL is configured.
func (c *Client) Enabled() bool { return c != nil && c.base != "" }

// Set shows a level for a zone. Repeats of the current state are skipped and
// only the newest pending state is sent, so a slow sign never builds a queue.
func (c *Client) Set(level, zone string) {
	if !c.Enabled() {
		return
	}
	c.mu.Lock()
	key := level + "/" + zone
	if key == c.last {
		c.mu.Unlock()
		return
	}
	c.last = key
	c.mu.Unlock()
	for {
		select {
		case c.pending <- [2]string{level, zone}:
			return
		default:
			select {
			case <-c.pending: // drop the stale one
			default:
			}
		}
	}
}

// Force sends a state even if it matches the last one (test alerts).
func (c *Client) Force(level, zone string) {
	if !c.Enabled() {
		return
	}
	c.mu.Lock()
	c.last = ""
	c.mu.Unlock()
	c.Set(level, zone)
}

func (c *Client) loop() {
	for st := range c.pending {
		if err := c.send(st[0], st[1]); err != nil {
			log.Printf("sign: %v", err)
			c.mu.Lock()
			c.last = "" // retry on the next Set
			c.mu.Unlock()
		}
	}
}

func (c *Client) send(level, zone string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	u := fmt.Sprintf("%s/level?v=%s&zone=%s", c.base, url.QueryEscape(level), url.QueryEscape(zone))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sign returned %s", resp.Status)
	}
	return nil
}
