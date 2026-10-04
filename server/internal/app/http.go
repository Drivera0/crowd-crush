package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Drivera0/crowd-crush/server/internal/brief"
	"github.com/Drivera0/crowd-crush/server/internal/hub"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/store"
)

// Routes registers the WebSocket endpoints and the JSON API.
func (a *App) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /ws/phone", a.Hub.ServePhone)
	mux.HandleFunc("GET /ws/dash", a.Hub.ServeDash)

	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		d, geo := a.liveConfig(), a.venue.Geo
		a.mu.Unlock()
		writeJSON(w, protocol.Config{VenueW: d.VenueW, VenueH: d.VenueH, Geo: geo,
			Yellow: d.YellowScore, Red: d.RedScore, NeighbourRadius: d.NeighbourRadius})
	})
	mux.HandleFunc("GET /api/areas", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, a.Areas())
	})
	mux.HandleFunc("PUT /api/areas", func(w http.ResponseWriter, r *http.Request) {
		var areas []protocol.Area
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&areas); err != nil {
			httpError(w, errors.New("want a JSON array of {id, name, sens, poly}"), http.StatusBadRequest)
			return
		}
		out, err := a.SetAreas(areas)
		if err != nil {
			httpError(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("GET /api/venue", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, a.Venue())
	})
	mux.HandleFunc("PUT /api/venue", func(w http.ResponseWriter, r *http.Request) {
		var v protocol.Venue
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&v); err != nil {
			httpError(w, errors.New("want {w, h, lat, lon, bearing, geo}"), http.StatusBadRequest)
			return
		}
		out, err := a.SetVenue(v)
		if err != nil {
			httpError(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]bool{
			"gemini":     a.opt.Brief.Enabled(),
			"elevenlabs": a.opt.Voice.Enabled(),
			"tiger":      a.opt.Tiger != nil,
			"sign":       a.opt.Sign.Enabled(),
		})
	})
	mux.HandleFunc("GET /api/node/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		a.mu.Lock()
		p := a.active()
		m := p.meta[id]
		if m == nil {
			a.mu.Unlock()
			httpError(w, errors.New("no such phone"), http.StatusNotFound)
			return
		}
		zone := ""
		if !m.outside {
			zone = p.det.ZoneOf(m.x, m.y)
		}
		d := protocol.NodeDetail{
			ID: id, X: r2(m.x), Y: r2(m.y), Acc: m.acc, Src: m.src(), Outside: m.outside, UA: m.ua, Zone: zone,
			Connected: m.connected, Synced: m.synced, RTT: m.rtt, Offset: m.offset,
			JoinedAt: m.joinedAt, Messages: m.msgs, Samples: m.samples(),
		}
		a.mu.Unlock()
		writeJSON(w, d)
	})
	mux.HandleFunc("GET /api/recordings", func(w http.ResponseWriter, r *http.Request) {
		files, _ := store.ListJSONL(a.opt.RecordingsDir)
		out := map[string]any{"files": files, "runs": []store.Run{}}
		if a.opt.Tiger != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if runs, err := a.opt.Tiger.Runs(ctx); err == nil {
				out["runs"] = runs
			}
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("POST /api/record/start", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Label string `json:"label"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		name, err := a.StartRecording(req.Label)
		if err != nil {
			httpError(w, err, http.StatusConflict)
			return
		}
		writeJSON(w, map[string]string{"name": name})
	})
	mux.HandleFunc("POST /api/record/stop", func(w http.ResponseWriter, r *http.Request) {
		name, n, err := a.StopRecording()
		if err != nil {
			httpError(w, err, http.StatusConflict)
			return
		}
		writeJSON(w, map[string]any{"name": name, "records": n})
	})
	mux.HandleFunc("POST /api/replay", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name  string  `json:"name"`
			Speed float64 `json:"speed"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
			httpError(w, errors.New("want {name, speed}"), http.StatusBadRequest)
			return
		}
		if err := a.StartReplay(r.Context(), req.Name, req.Speed); err != nil {
			httpError(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]string{"mode": "replay"})
	})
	mux.HandleFunc("POST /api/live", func(w http.ResponseWriter, r *http.Request) {
		a.StopReplay()
		writeJSON(w, map[string]string{"mode": "live"})
	})
	mux.HandleFunc("POST /api/test-alert", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"zone": a.TestAlert()})
	})
	mux.HandleFunc("POST /api/ask", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Question string `json:"question"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		q := strings.TrimSpace(req.Question)
		if q == "" || len(q) > 500 {
			httpError(w, errors.New("ask a question (max 500 chars)"), http.StatusBadRequest)
			return
		}
		ans, err := a.opt.Brief.Ask(r.Context(), q, a.History())
		if err != nil {
			if errors.Is(err, brief.ErrNoKey) {
				ans = "Gemini isn't configured (GEMINI_API_KEY). Here is the raw log:\n" + a.History()
			} else {
				httpError(w, err, http.StatusBadGateway)
				return
			}
		}
		writeJSON(w, map[string]string{"answer": ans})
	})
}

// History summarises the last 10 minutes for the "ask" box: alert log plus
// each zone's peak score and level per minute.
func (a *App) History() string {
	now := hub.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	var sb strings.Builder
	fmt.Fprintf(&sb, "Now: %s. Mode: %s.\n", time.UnixMilli(now).Format("15:04:05"), map[bool]string{true: "replay", false: "live"}[a.replay != nil])
	sb.WriteString("Alerts (oldest first):\n")
	n := 0
	for _, al := range a.alerts {
		if now-al.T > 10*60_000 {
			continue
		}
		n++
		fmt.Fprintf(&sb, "- %s zone %s → %s (score %.2f)", time.UnixMilli(al.T).Format("15:04:05"), al.Zone, al.Level, al.Score)
		if al.Test {
			sb.WriteString(" [test]")
		}
		if al.Brief != "" {
			fmt.Fprintf(&sb, ": %q", al.Brief)
		}
		sb.WriteString("\n")
	}
	if n == 0 {
		sb.WriteString("- none\n")
	}
	p := a.active()
	sb.WriteString("Zone peak score per minute (oldest first, 0..1):\n")
	for _, z := range p.last.Zones {
		fmt.Fprintf(&sb, "- zone %s now %s:", z.ID, z.Level)
		h := p.hist[z.ID]
		for i := 0; i < len(h); i += 60 {
			peak, lvl := 0.0, "calm"
			for _, pt := range h[i:min(i+60, len(h))] {
				peak = max(peak, pt.score)
				if levelRank[pt.level] > levelRank[lvl] {
					lvl = pt.level
				}
			}
			fmt.Fprintf(&sb, " %.2f(%s)", peak, lvl)
		}
		sb.WriteString("\n")
	}
	phones := 0
	for _, m := range a.live.meta {
		if m.connected {
			phones++
		}
	}
	fmt.Fprintf(&sb, "Phones connected: %d.\n", phones)
	return sb.String()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, err error, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
