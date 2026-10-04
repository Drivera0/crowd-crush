package app

// Escalation controls, changeable while the server runs:
//
//   GET  /api/escalation          → {"on": true, "afterS": 60}
//   PUT  /api/escalation          {"on": false} or {"on": true, "afterS": 90}
//   POST /api/alerts/{id}/escalate  re-announce one alert now
//
// The setting starts from -escalate-after / PULSE_ESCALATE_AFTER and, once
// staff change it, is kept in data/escalation.json (which then wins over the
// flag on the next start). Automatic escalation still only touches red,
// unacknowledged, real alerts; "escalate now" is staff asking for it, so it
// also works on a yellow, an acknowledged alert or a drill (to show what an
// escalation looks like) and may be repeated. Only a resolved alert can't.

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/Drivera0/crowd-crush/server/internal/hub"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// EscalationSettings is the wire and file form of the setting.
type EscalationSettings struct {
	On     bool `json:"on"`
	AfterS int  `json:"afterS"` // seconds after going red; kept when off so turning on restores it
}

const (
	minEscalateS = 10
	maxEscalateS = 3600
)

// ErrResolved: a resolved alert can't be escalated.
var ErrResolved = errors.New("that alert is already resolved")

var errEscalation = errors.New("want {on, afterS}: afterS 10–3600 seconds")

// escalationLocked reports the current setting. Caller holds mu.
func (a *App) escalationLocked() EscalationSettings {
	d := a.opt.EscalateAfter
	if d < 0 {
		return EscalationSettings{On: false, AfterS: a.escOffAfterS()}
	}
	return EscalationSettings{On: true, AfterS: int(d / time.Second)}
}

// escOffAfterS is the wait to restore when escalation is turned back on.
func (a *App) escOffAfterS() int {
	if a.escAfterS >= minEscalateS {
		return a.escAfterS
	}
	return int(DefaultEscalateAfter / time.Second)
}

// Escalation returns the current setting.
func (a *App) Escalation() EscalationSettings {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.escalationLocked()
}

// SetEscalation changes the setting and saves it.
func (a *App) SetEscalation(s EscalationSettings) (EscalationSettings, error) {
	if s.AfterS == 0 {
		s.AfterS = a.Escalation().AfterS
	}
	if s.AfterS < minEscalateS || s.AfterS > maxEscalateS {
		return EscalationSettings{}, errEscalation
	}
	a.mu.Lock()
	a.escAfterS = s.AfterS
	if s.On {
		a.opt.EscalateAfter = time.Duration(s.AfterS) * time.Second
	} else {
		a.opt.EscalateAfter = -1
	}
	out := a.escalationLocked()
	a.mu.Unlock()
	if err := a.save("escalation.json", out); err != nil {
		log.Printf("escalation: %v", err)
	}
	log.Printf("escalation: on=%v after %d s", out.On, out.AfterS)
	return out, nil
}

// loadEscalation applies a saved setting, if any. Called from New.
func (a *App) loadEscalation() {
	if a.opt.EscalateAfter > 0 {
		a.escAfterS = int(a.opt.EscalateAfter / time.Second)
	}
	var s EscalationSettings
	if !a.load("escalation.json", &s) || s.AfterS < minEscalateS || s.AfterS > maxEscalateS {
		return
	}
	a.escAfterS = s.AfterS
	if s.On {
		a.opt.EscalateAfter = time.Duration(s.AfterS) * time.Second
	} else {
		a.opt.EscalateAfter = -1
	}
}

// EscalateNow re-announces one alert straight away, as staff asked: the
// sign and its light go red for a few seconds and the voice says "Still
// unacknowledged." (or "Escalated by staff." once acknowledged).
func (a *App) EscalateNow(id string) (protocol.Alert, error) {
	now := hub.Now()
	a.mu.Lock()
	var al protocol.Alert
	found := false
	for _, x := range a.alerts {
		if x.ID == id {
			al, found = x, true
			break
		}
	}
	inc := a.incidents[id]
	if !found || inc == nil {
		a.mu.Unlock()
		return protocol.Alert{}, ErrNoAlert
	}
	if al.Status == protocol.StatusResolved {
		a.mu.Unlock()
		return al, ErrResolved
	}
	inc.escalating = true
	a.signHold = now + signHoldMs
	a.mu.Unlock()
	go a.escalateWith(al, true)
	return al, nil
}

func (a *App) escalationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/escalation", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, a.Escalation())
	})
	mux.HandleFunc("PUT /api/escalation", func(w http.ResponseWriter, r *http.Request) {
		var s EscalationSettings
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&s); err != nil {
			httpError(w, errEscalation, http.StatusBadRequest)
			return
		}
		out, err := a.SetEscalation(s)
		if err != nil {
			httpError(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("POST /api/alerts/{id}/escalate", func(w http.ResponseWriter, r *http.Request) {
		al, err := a.EscalateNow(r.PathValue("id"))
		switch {
		case errors.Is(err, ErrNoAlert):
			httpError(w, err, http.StatusNotFound)
		case errors.Is(err, ErrResolved):
			httpError(w, err, http.StatusConflict)
		case err != nil:
			httpError(w, err, http.StatusBadRequest)
		default:
			writeJSON(w, al)
		}
	})
}
