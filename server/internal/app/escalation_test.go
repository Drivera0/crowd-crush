package app

import (
	"testing"
	"time"

	"github.com/Drivera0/crowd-crush/server/internal/brief"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// TestEscalationSwitch: staff can turn automatic escalation off and on, the
// wait is kept while off, and the setting survives a restart.
func TestEscalationSwitch(t *testing.T) {
	dir := t.TempDir()
	a, srv := testServer(t, Options{DataDir: dir, EscalateAfter: 2 * time.Second})
	var got EscalationSettings
	if code := do(t, "GET", srv.URL+"/api/escalation", nil, &got); code != 200 || !got.On || got.AfterS != 2 {
		t.Fatalf("initial: %d %+v", code, got)
	}
	if code := do(t, "PUT", srv.URL+"/api/escalation", map[string]any{"on": true, "afterS": 5}, nil); code != 400 {
		t.Errorf("afterS under 10 s should be refused: %d", code)
	}
	if code := do(t, "PUT", srv.URL+"/api/escalation", map[string]any{"on": true, "afterS": 90}, &got); code != 200 || !got.On || got.AfterS != 90 {
		t.Fatalf("set 90 s: %d %+v", code, got)
	}
	if code := do(t, "PUT", srv.URL+"/api/escalation", map[string]any{"on": false}, &got); code != 200 || got.On || got.AfterS != 90 {
		t.Fatalf("off keeps the wait: %d %+v", code, got)
	}

	// Off: a red alert nobody acknowledged does not escalate.
	info := func() brief.Info { return brief.Info{Zone: "A", Level: "red"} }
	a.mu.Lock()
	r, _ := a.raiseLocked("live", protocol.KindDensity, "A", "calm", "red", 4.5, ft0, false, false, info)
	a.mu.Unlock()
	a.escalate(ft0 + 10*60_000)
	time.Sleep(100 * time.Millisecond)
	a.mu.Lock()
	al, _ := a.updateAlertLocked(r.ID, func(*protocol.Alert, *incident) {})
	a.mu.Unlock()
	if al.Escalated {
		t.Errorf("escalated while escalation is off")
	}

	// The setting is saved and wins over the flag on the next start.
	b := New(Options{DataDir: dir, EscalateAfter: 2 * time.Second})
	if s := b.Escalation(); s.On || s.AfterS != 90 {
		t.Errorf("after restart: %+v", s)
	}
}

// TestEscalateNow: staff can escalate any unresolved alert by hand, including
// an acknowledged one and a drill; a resolved or unknown one can't be.
func TestEscalateNow(t *testing.T) {
	a, srv := testServer(t, Options{EscalateAfter: -1})
	info := func() brief.Info { return brief.Info{Zone: "A", Level: "yellow"} }
	a.mu.Lock()
	y, _ := a.raiseLocked("live", protocol.KindWave, "A", "calm", "yellow", 0.35, ft0, false, false, info)
	a.mu.Unlock()
	if code := do(t, "POST", srv.URL+"/api/alerts/"+y.ID+"/ack", nil, nil); code != 200 {
		t.Fatalf("ack: %d", code)
	}
	if code := do(t, "POST", srv.URL+"/api/alerts/"+y.ID+"/escalate", nil, nil); code != 200 {
		t.Fatalf("escalate now: %d", code)
	}
	waitFor(t, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		al, _ := a.updateAlertLocked(y.ID, func(*protocol.Alert, *incident) {})
		return al.Escalated && al.Status == protocol.StatusAck
	})
	if code := do(t, "POST", srv.URL+"/api/alerts/"+y.ID+"/escalate", nil, nil); code != 200 {
		t.Errorf("a second escalation by hand should be allowed: %d", code)
	}
	if code := do(t, "POST", srv.URL+"/api/alerts/nope/escalate", nil, nil); code != 404 {
		t.Errorf("unknown id: %d", code)
	}
	if code := do(t, "POST", srv.URL+"/api/alerts/"+y.ID+"/resolve", nil, nil); code != 200 {
		t.Fatalf("resolve: %d", code)
	}
	if code := do(t, "POST", srv.URL+"/api/alerts/"+y.ID+"/escalate", nil, nil); code != 409 {
		t.Errorf("resolved alert: %d", code)
	}
}
