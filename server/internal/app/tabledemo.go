package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/sign"
)

// Table demo: the sign, the zone lights and this laptop sit side by side on
// one table, a few phones in front of them. Bluetooth distances between
// boards 30 cm apart mean nothing, so the map positions come from one
// action instead (POST /api/hardware/table):
//
//   - the boards and the laptop go in a tidy row, tableGap apart, at the
//     demo spot if it is on, else the map centre (or a spot given as {x, y});
//     zone light A on the left, B on the right, the sign and the laptop
//     between them, so with the default left/right zones each light sits
//     in its own half;
//   - the demo spot goes on just in front of the row, so phones that join
//     line up beside the boards (and phones already connected move there);
//   - each zone light gets something to show: with no drawn areas the
//     default zones (A shows zone A); with areas, lights nobody assigned go
//     to the areas that have none, in order.
//
// Run it again and nothing moves: with the demo spot on, the row is built
// around the phones' spot.
//
// POST /api/hardware/test shows red on every board for a second, asks each
// what it shows, and restores the live level (preflight uses it).

const (
	tableGap      = 0.8 // m between markers in the board row
	tableRowAhead = 1.0 // m from the board row to the phone row
	tablePhones   = 4   // phones the row is centred for
)

// TableDemo places the boards for a table demo. x, y (both or neither) put
// the table there instead of the demo spot / map centre.
func (a *App) TableDemo(x, y *float64) (protocol.TableDemo, error) {
	if (x == nil) != (y == nil) || (x != nil && (!finite(*x) || !finite(*y))) {
		return protocol.TableDemo{}, errors.New("want {} or {x, y} in venue metres")
	}
	lights := append([]string(nil), a.opt.Sign.Zones()...)
	sort.Strings(lights)
	hasSign := false
	for _, k := range a.opt.Sign.Keys() {
		if k == "sign" {
			hasSign = true
		}
	}

	a.mu.Lock()
	cfg := a.liveConfig()
	d := a.demo
	sp := d.Spacing
	if sp <= 0 {
		sp = DemoSpacing
	}
	half := float64(tablePhones-1) * sp / 2 // phone row: centred on the table
	var ax, ay float64
	switch {
	case x != nil:
		ax, ay = *x, *y
	case d.On:
		ax, ay = d.X+half, d.Y-tableRowAhead/2
	default:
		ax, ay = cfg.VenueW/2, cfg.VenueH/2
	}
	// The row: A, sign, laptop, B, then any further lights.
	var row []string
	if len(lights) > 0 {
		row = append(row, lights[0])
	}
	if hasSign {
		row = append(row, "sign")
	}
	row = append(row, towerLaptop)
	if len(lights) > 1 {
		row = append(row, lights[1:]...)
	}
	// Keep the whole row and the phones inside the venue.
	span := float64(len(row)-1) * tableGap / 2
	ax = clampTo(ax, max(span, half)+0.3, cfg.VenueW-max(span, half)-0.3)
	ay = clampTo(ay, tableRowAhead/2+0.3, cfg.VenueH-tableRowAhead/2-0.3)
	pos := map[string]protocol.Point{}
	for k, v := range a.hwPos {
		pos[k] = v
	}
	for i, k := range row {
		bx, by := cfg.Clamp(ax-span+float64(i)*tableGap, ay-tableRowAhead/2)
		pos[k] = protocol.Point{r2(bx), r2(by)}
	}
	err := a.save(hardwareFile, pos)
	if err == nil {
		a.hwPos = pos
	}
	areas := cloneAreas(a.areas)
	a.mu.Unlock()
	if err != nil {
		return protocol.TableDemo{}, err
	}

	// The demo spot just in front, phones lined up from there.
	demo, err := a.SetDemo(protocol.DemoSpot{On: true, X: ax - half, Y: ay + tableRowAhead/2, Spacing: sp}, true)
	if err != nil {
		return protocol.TableDemo{}, err
	}

	out := protocol.TableDemo{Demo: demo}
	known := map[string]bool{}
	for _, l := range lights {
		known[l] = true
	}
	if len(areas) == 0 {
		// The default zones: light A shows zone A, and so on.
		names := map[string]string{}
		for _, z := range detect.DefaultZones(cfg) {
			names[z.ID] = z.Name
		}
		for _, l := range lights {
			tl := protocol.TableLight{Key: l, Shows: names[l]}
			if tl.Shows == "" {
				out.Notes = append(out.Notes, "Zone light "+l+" has no zone of its own: draw an area on Areas & alerts and choose this light for it.")
			}
			out.Lights = append(out.Lights, tl)
		}
	} else {
		used := map[string]bool{}
		for _, ar := range areas {
			if known[ar.Light] {
				used[ar.Light] = true
			}
		}
		var free []string
		for _, l := range lights {
			if !used[l] {
				free = append(free, l)
			}
		}
		changed := false
		for i := range areas {
			if len(free) > 0 && !known[areas[i].Light] {
				areas[i].Light, free = free[0], free[1:]
				changed = true
			}
		}
		if changed {
			if _, err := a.SetAreas(areas); err != nil {
				return protocol.TableDemo{}, err
			}
		}
		for _, l := range lights {
			var shows []string
			for _, ar := range areas {
				if ar.Light == l {
					shows = append(shows, ar.Name)
				}
			}
			out.Lights = append(out.Lights, protocol.TableLight{Key: l, Shows: strings.Join(shows, ", ")})
			if len(shows) == 0 {
				out.Notes = append(out.Notes, "Zone light "+l+" shows nothing: there are fewer areas than lights. Draw another area, or delete the areas to use the left and right halves.")
			}
		}
	}
	if len(lights) == 0 && !hasSign {
		out.Notes = append(out.Notes, "No boards in SIGN_URL: only this laptop was placed. For boards on USB set SIGN_URL=serial:auto,A=serial:auto,B=serial:auto (scripts/boards.sh env) and restart Pulse.")
	}
	out.Hardware = a.Hardware()
	return out, nil
}

func clampTo(v, lo, hi float64) float64 {
	if lo > hi {
		return (lo + hi) / 2
	}
	return min(max(v, lo), hi)
}

// BoardTestLevel and BoardTestHold: what POST /api/hardware/test shows.
const (
	BoardTestLevel = protocol.LevelRed
	BoardTestHold  = time.Second
)

// TestBoards shows red on every board for a second, checks each reports it,
// then restores the live level.
func (a *App) TestBoards(ctx context.Context) []protocol.BoardTest {
	res := a.opt.Sign.Test(ctx, BoardTestLevel, BoardTestHold)
	out := make([]protocol.BoardTest, 0, len(res))
	for _, r := range res {
		bt := protocol.BoardTest{Key: r.Key, Name: "Sign", URL: r.URL, Link: r.Link, Port: r.Port, Sent: r.Sent,
			Confirmed: r.Confirmed, Level: r.Level, FW: r.FW, Error: r.Err, Ms: r.Ms}
		kind := "sign"
		if r.Key != "sign" {
			bt.Name, kind = "Zone light "+r.Key, "zone-light"
		}
		if r.Sent && r.Level != "" {
			bt.FWOld = sign.FWOutOfDate(kind, r.FW)
		}
		out = append(out, bt)
	}
	return out
}

func (a *App) tableRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/hardware/table", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			X *float64 `json:"x"`
			Y *float64 `json:"y"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			httpError(w, errors.New("want {} or {x, y} in venue metres"), http.StatusBadRequest)
			return
		}
		res, err := a.TableDemo(req.X, req.Y)
		if err != nil {
			httpError(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, res)
	})
	mux.HandleFunc("POST /api/hardware/test", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		res := a.TestBoards(ctx)
		if res == nil {
			res = []protocol.BoardTest{}
		}
		writeJSON(w, res)
	})
}
