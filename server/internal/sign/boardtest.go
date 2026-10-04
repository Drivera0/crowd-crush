package sign

import (
	"context"
	"sync"
	"time"
)

// TestResult is what one board did when Test drove it.
type TestResult struct {
	Key  string // "sign" or the zone-light letter
	URL  string // as in SIGN_URL
	Link string // "usb" or "wifi" when it answered
	Port string // serial port, for USB boards
	// Sent: the level was delivered (serial write or HTTP 200).
	Sent bool
	// Confirmed: the board then reported that level in its status (S or
	// /pulse). False with Sent = it took the command but couldn't confirm
	// (old firmware, or the status check timed out).
	Confirmed bool
	Level     string // what the board reported
	FW        string // the board's firmware build id, if it reported one
	Err       string
	Ms        int64 // send → confirmation
}

// Test shows level on every board for hold, checks each one reports it
// back, then puts back what the server last asked for (calm if nothing).
// It runs the boards concurrently and returns once all are restored.
func (c *Client) Test(ctx context.Context, level string, hold time.Duration) []TestResult {
	if !c.Enabled() {
		return nil
	}
	out := make([]TestResult, len(c.targets))
	var wg sync.WaitGroup
	for i, t := range c.targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = t.test(ctx, level, hold)
		}()
	}
	wg.Wait()
	return out
}

func (t *target) key() string {
	if t.zone == "" {
		return "sign"
	}
	return t.zone
}

func (t *target) test(ctx context.Context, level string, hold time.Duration) TestResult {
	r := TestResult{Key: t.key(), URL: t.base}
	start := time.Now()
	sctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	err := t.send(sctx, state{level, t.zone})
	cancel()
	if err != nil {
		r.Err = err.Error()
		return r
	}
	r.Sent = true
	defer t.restore()
	if t.ser == nil {
		// The Uno R4 serves one connection at a time: let it close the last.
		time.Sleep(300 * time.Millisecond)
	}
	for attempt := 0; attempt < 2 && !r.Confirmed; attempt++ {
		pctx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
		st := t.probe(pctx)
		cancel()
		r.Link, r.Port = st.Link, st.Port
		if st.Pulse != nil {
			r.Level, r.FW = st.Pulse.Level, st.Pulse.FW
			r.Confirmed = st.Pulse.Level == level
			r.Err = ""
		} else if st.Err != "" {
			r.Err = st.Err
		}
	}
	r.Ms = time.Since(start).Milliseconds()
	if rest := hold - time.Since(start); rest > 0 {
		select {
		case <-time.After(rest):
		case <-ctx.Done():
		}
	}
	return r
}

// restore re-sends the state the server last asked this board for.
func (t *target) restore() {
	t.mu.Lock()
	s := state{"calm", t.zone}
	if t.cur != nil {
		s = *t.cur
	}
	t.mu.Unlock()
	t.set(s, true)
}
