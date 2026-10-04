package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Drivera0/crowd-crush/server/internal/brief"
	"github.com/Drivera0/crowd-crush/server/internal/crowdsim"
	"github.com/Drivera0/crowd-crush/server/internal/hub"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/store"
)

// Routes registers the WebSocket endpoints and the JSON API.
func (a *App) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /ws/phone", a.Hub.ServePhone)
	mux.HandleFunc("GET /ws/dash", a.Hub.ServeDash)
	a.meshRoutes(mux)     // GET /api/mesh, POST /api/mesh/jam (mesh.go)
	a.beaconRoutes(mux)   // Bluetooth beacon positioning (beacons.go)
	a.locateRoutes(mux)   // GET/PUT /api/locate: the position estimator (locate.go)
	a.joinRoutes(mux)     // /api/join…: the QR link, its test, phones' join reports (join.go)
	a.demoMoveRoutes(mux) // GET /api/demo/row, POST /api/demo/back: moving about at the table demo (demomove.go)

	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		d, geo, demo := a.liveConfig(), a.venue.Geo, a.demo.On
		a.mu.Unlock()
		writeJSON(w, protocol.Config{VenueW: d.VenueW, VenueH: d.VenueH, Geo: geo,
			Yellow: d.YellowScore, Red: d.RedScore, NeighbourRadius: d.NeighbourRadius, Demo: demo,
			DefaultW: a.opt.Detect.VenueW, DefaultH: a.opt.Detect.VenueH})
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
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&v); err != nil {
			httpError(w, errors.New("want {w, h, lat, lon, bearing, geo, template, layout}"), http.StatusBadRequest)
			return
		}
		out, err := a.SetVenue(v)
		if err != nil {
			httpError(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("POST /api/venue/floorplan", func(w http.ResponseWriter, r *http.Request) {
		img, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxFloorplanSize))
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				httpError(w, fmt.Errorf("the image is over %d MB", MaxFloorplanSize>>20), http.StatusRequestEntityTooLarge)
				return
			}
			httpError(w, err, http.StatusBadRequest)
			return
		}
		v, err := a.SetFloorplan(img)
		if err != nil {
			httpError(w, err, http.StatusUnsupportedMediaType)
			return
		}
		writeJSON(w, v)
	})
	mux.HandleFunc("GET /api/venue/floorplan", func(w http.ResponseWriter, r *http.Request) {
		img, mime := a.Floorplan()
		if img == nil {
			httpError(w, errors.New("no floor plan uploaded"), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", mime)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Write(img)
	})
	mux.HandleFunc("DELETE /api/venue/floorplan", func(w http.ResponseWriter, r *http.Request) {
		v, err := a.DeleteFloorplan()
		if err != nil {
			httpError(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, v)
	})
	mux.HandleFunc("POST /api/venue/floorplan/analyze", func(w http.ResponseWriter, r *http.Request) {
		s, err := a.AnalyzeFloorplan(r.Context())
		switch {
		case errors.Is(err, brief.ErrNoKey):
			httpError(w, errors.New("Gemini isn't configured (GEMINI_API_KEY)"), http.StatusServiceUnavailable)
		case errors.Is(err, errNoFloorplan):
			httpError(w, err, http.StatusNotFound)
		case err != nil:
			log.Printf("floor plan: %v", err)
			httpError(w, err, http.StatusBadGateway)
		default:
			writeJSON(w, s)
		}
	})
	// Ack and resolve take an optional body: {"by"} and {"by", "note"}.
	// An empty body works as before.
	auditBody := func(w http.ResponseWriter, r *http.Request) (by, note string, ok bool) {
		var req struct {
			By   string `json:"by"`
			Note string `json:"note"`
		}
		err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req)
		if err != nil && !errors.Is(err, io.EOF) {
			httpError(w, errors.New(`want an empty body or {"by": "…", "note": "…"}`), http.StatusBadRequest)
			return "", "", false
		}
		return req.By, req.Note, true
	}
	alertErr := func(w http.ResponseWriter, err error) {
		if errors.Is(err, ErrNoAlert) {
			httpError(w, err, http.StatusNotFound)
			return
		}
		httpError(w, err, http.StatusBadRequest)
	}
	mux.HandleFunc("POST /api/alerts/{id}/ack", func(w http.ResponseWriter, r *http.Request) {
		by, _, ok := auditBody(w, r)
		if !ok {
			return
		}
		al, err := a.AckAlert(r.PathValue("id"), by)
		if err != nil {
			alertErr(w, err)
			return
		}
		writeJSON(w, al)
	})
	mux.HandleFunc("POST /api/alerts/{id}/resolve", func(w http.ResponseWriter, r *http.Request) {
		by, note, ok := auditBody(w, r)
		if !ok {
			return
		}
		al, err := a.ResolveAlert(r.PathValue("id"), by, note)
		if err != nil {
			alertErr(w, err)
			return
		}
		writeJSON(w, al)
	})
	a.escalationRoutes(mux)
	a.tableRoutes(mux) // table demo + board test (tabledemo.go)
	mux.HandleFunc("POST /api/alerts/clear", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, a.ClearAlerts())
	})
	mux.HandleFunc("GET /api/hardware", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, a.Hardware())
	})
	mux.HandleFunc("PUT /api/hardware/{key}/pos", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			X *float64 `json:"x"`
			Y *float64 `json:"y"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req); err != nil || req.X == nil || req.Y == nil {
			httpError(w, errors.New("want {x, y} in venue metres"), http.StatusBadRequest)
			return
		}
		hw, err := a.SetHardwarePos(r.PathValue("key"), *req.X, *req.Y)
		switch {
		case errors.Is(err, ErrNoBoard):
			httpError(w, err, http.StatusNotFound)
		case err != nil:
			httpError(w, err, http.StatusBadRequest)
		default:
			writeJSON(w, hw)
		}
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
		d := nodeDetail(p, id, m)
		a.mu.Unlock()
		writeJSON(w, d)
	})
	// The detector evaluation written by `make eval` (cmd/eval), for the
	// dashboard's evaluation card. Served from the working directory.
	mux.HandleFunc("GET /api/eval", func(w http.ResponseWriter, r *http.Request) {
		b, err := os.ReadFile(filepath.Join("docs", "eval.json"))
		if err != nil {
			httpError(w, errors.New("no evaluation yet: run make eval"), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(b)
	})
	mux.HandleFunc("GET /api/edge", func(w http.ResponseWriter, r *http.Request) {
		from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
		if from == "" || to == "" {
			httpError(w, errors.New("want ?from=<id>&to=<id>"), http.StatusBadRequest)
			return
		}
		ex, ok := a.ExplainEdge(from, to)
		if !ok {
			httpError(w, errors.New("not a neighbour pair in the latest detector step"), http.StatusNotFound)
			return
		}
		writeJSON(w, ex)
	})
	mux.HandleFunc("GET /api/recordings", func(w http.ResponseWriter, r *http.Request) {
		out := map[string]any{"files": a.Recordings(), "runs": []store.Run{}}
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
	mux.HandleFunc("GET /api/sim", func(w http.ResponseWriter, r *http.Request) {
		// ?scenario=<id>: that scenario's venue (size, furniture, doors,
		// exits) before it runs, for the picker's preview.
		if scn := r.URL.Query().Get("scenario"); scn != "" {
			st, err := a.simPreview(scn)
			if err != nil {
				httpError(w, err, http.StatusBadRequest)
				return
			}
			writeJSON(w, st)
			return
		}
		writeJSON(w, a.SimStatus())
	})
	mux.HandleFunc("POST /api/sim/start", func(w http.ResponseWriter, r *http.Request) {
		var req SimStart
		if r.ContentLength != 0 {
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
				httpError(w, errors.New(`want {people, participation, scenario, realism, imperfections}: realism "ideal", "realistic" or "harsh"; imperfections {gps, carry, dropout} strengths 0–3`), http.StatusBadRequest)
				return
			}
		}
		if err := a.StartSim(req); err != nil {
			code := http.StatusBadRequest
			if errors.Is(err, errSimRunning) {
				code = http.StatusConflict
			}
			httpError(w, err, code)
			return
		}
		writeJSON(w, map[string]string{"mode": "sim"})
	})
	mux.HandleFunc("POST /api/sim/stop", func(w http.ResponseWriter, r *http.Request) {
		if err := a.StopSim(); err != nil {
			httpError(w, err, http.StatusConflict)
			return
		}
		writeJSON(w, map[string]string{"mode": "live"})
	})
	mux.HandleFunc("POST /api/sim/action", func(w http.ResponseWriter, r *http.Request) {
		var act crowdsim.Action
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&act); err != nil {
			httpError(w, errors.New("want {type, ...}"), http.StatusBadRequest)
			return
		}
		if err := a.SimAction(act); err != nil {
			httpError(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
	// ---- the judge demo (demo.go, hybrid.go) ----
	mux.HandleFunc("PUT /api/node/{id}/pos", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			X *float64 `json:"x"`
			Y *float64 `json:"y"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req); err != nil || req.X == nil || req.Y == nil {
			httpError(w, errors.New("want {x, y} in venue metres"), http.StatusBadRequest)
			return
		}
		d, err := a.MovePhone(r.PathValue("id"), *req.X, *req.Y)
		switch {
		case errors.Is(err, ErrNoPhone):
			httpError(w, err, http.StatusNotFound)
		case err != nil:
			httpError(w, err, http.StatusBadRequest)
		default:
			writeJSON(w, d)
		}
	})
	mux.HandleFunc("GET /api/demo", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, a.Demo())
	})
	mux.HandleFunc("PUT /api/demo", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			protocol.DemoSpot
			Arrange bool `json:"arrange"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req); err != nil {
			httpError(w, errDemo, http.StatusBadRequest)
			return
		}
		d, err := a.SetDemo(req.DemoSpot, req.Arrange)
		if err != nil {
			httpError(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, d)
	})
	mux.HandleFunc("GET /api/tower/{key}", func(w http.ResponseWriter, r *http.Request) {
		t, err := a.Tower(r.PathValue("key"))
		switch {
		case errors.Is(err, ErrTowerUnplaced):
			httpError(w, err, http.StatusConflict)
		case err != nil:
			httpError(w, errors.New("no such tower: this check-in code isn't for anything at this venue"), http.StatusNotFound)
		default:
			writeJSON(w, t)
		}
	})
	mux.HandleFunc("GET /api/receipt/{id}", func(w http.ResponseWriter, r *http.Request) {
		rc, err := a.Receipt(r.PathValue("id"))
		if err != nil {
			httpError(w, err, http.StatusNotFound)
			return
		}
		writeJSON(w, rc)
	})
	mux.HandleFunc("POST /api/sim/surge-phones", func(w http.ResponseWriter, r *http.Request) {
		res, err := a.SurgePhones()
		switch {
		case errors.Is(err, ErrNoPhones):
			httpError(w, err, http.StatusConflict)
		case err != nil:
			httpError(w, err, http.StatusBadRequest)
		default:
			writeJSON(w, res)
		}
	})
	// The alert drill (drill.go). An empty body is the old one-button test
	// alert; the response keeps its "zone" and adds the drill's record.
	mux.HandleFunc("POST /api/test-alert", func(w http.ResponseWriter, r *http.Request) {
		var req protocol.DrillRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			httpError(w, errors.New(`want an empty body or {zone, level: "yellow"|"red", kind: "wave"|"density"|"rule", outputs: {briefing, voice, sign, lights: ["A"]}}`), http.StatusBadRequest)
			return
		}
		d, err := a.Drill(req)
		if err != nil {
			httpError(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, d)
	})
	mux.HandleFunc("GET /api/drill", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, a.DrillStatus())
	})
	// Take every board off the map (Home → Start over).
	mux.HandleFunc("DELETE /api/hardware/pos", func(w http.ResponseWriter, r *http.Request) {
		hw, err := a.ClearHardwarePos()
		if err != nil {
			httpError(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, hw)
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

// History summarises the situation for the "ask" box: the live situation
// first (overall status, active incidents), then the last 10 minutes of
// real alerts and each zone's peak score and level per minute. Places are
// named, never given as ids. Drills (test alerts) are left out, only
// counted, so the answer never mistakes one for an incident.
func (a *App) History() string {
	now := hub.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.active()
	var sb strings.Builder
	mode := a.modeLocked()
	switch mode {
	case "sim":
		mode = "sim (a crowd simulation on screen, not real phones)"
	case "replay":
		mode = "replay (a recorded run on screen, not the live crowd)"
	}
	fmt.Fprintf(&sb, "Now: %s. Mode: %s.\n", time.UnixMilli(now).Format("15:04:05"), mode)
	st := a.statusLocked(p)
	if st.Level == protocol.LevelCalm {
		fmt.Fprintf(&sb, "Current situation: calm everywhere (crowd risk %.2f of 1).\n", st.Score)
	} else {
		what := map[string]string{protocol.StatusKindWave: "crowd waves (pushes) travelling through the crowd",
			protocol.StatusKindDensity: "people packed too tightly", protocol.StatusKindRule: "a staff area rule broken",
			protocol.StatusKindEarly: "crowding building fast (early warning)"}[st.Kind]
		fmt.Fprintf(&sb, "Current situation: %s at %s: %s (crowd risk %.2f of 1)", strings.ToUpper(st.Level), st.Where, what, st.Score)
		if st.Density > 0 {
			fmt.Fprintf(&sb, ", about %.1f people per square metre at the worst spot", st.Density)
		}
		sb.WriteString(".\n")
	}
	line := func(al protocol.Alert) {
		kind := al.Kind
		if kind == "" {
			kind = protocol.KindWave
		}
		fmt.Fprintf(&sb, "- %s %s: %s %s (score %.2f)", time.UnixMilli(al.T).Format("15:04:05"), a.placeLocked(p, al.Zone), kind, al.Level, al.Score)
		if al.Early {
			sb.WriteString(" [early warning]")
		}
		if al.Status != "" && al.Level != protocol.LevelCalm {
			fmt.Fprintf(&sb, " [%s]", al.Status)
		}
		if al.Escalated {
			sb.WriteString(" [escalated]")
		}
		if al.Note != "" {
			fmt.Fprintf(&sb, " [outcome: %q]", al.Note)
		}
		if al.Brief != "" {
			fmt.Fprintf(&sb, ": %q", al.Brief)
		}
		sb.WriteString("\n")
	}
	sb.WriteString("Active incidents (not resolved yet):\n")
	n := 0
	for _, al := range a.alerts {
		if !al.Test && al.Status != protocol.StatusResolved && al.Level != protocol.LevelCalm {
			n++
			line(al)
		}
	}
	if n == 0 {
		sb.WriteString("- none\n")
	}
	sb.WriteString("Real alerts in the last 10 minutes (oldest first):\n")
	n, drills := 0, 0
	for _, al := range a.alerts {
		if now-al.T > 10*60_000 {
			continue
		}
		if al.Test {
			drills++
			continue
		}
		n++
		line(al)
	}
	if n == 0 {
		sb.WriteString("- none\n")
	}
	if drills > 0 {
		fmt.Fprintf(&sb, "Drills: %d test alert(s) in the last 10 minutes; these were tests of the alert chain, not incidents.\n", drills)
	}
	sb.WriteString("Each zone's level now and its peak push-wave score per minute (oldest first, 0..1):\n")
	zoneLevels, _, _ := a.alertLevels(p) // detector, rules and the clusters in each zone
	for _, z := range p.last.Zones {
		fmt.Fprintf(&sb, "- %s now %s:", zoneLabel(p, z.ID), zoneLevels[z.ID])
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

// placeLocked names an alert's zone for people: the zone's name in pipeline
// p, else the saved area's name, else a description (never the id of a
// drawn area). Caller holds mu.
func (a *App) placeLocked(p *pipeline, zone string) string {
	if _, ok := p.last.Zone(zone); ok {
		return zoneLabel(p, zone)
	}
	for _, ar := range a.areas {
		if ar.ID == zone && ar.Name != "" {
			return ar.Name
		}
	}
	if zone == "" {
		return "somewhere in the venue"
	}
	if len(zone) <= 2 { // a default zone letter
		return "Zone " + zone
	}
	return "an area that has since been removed"
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
