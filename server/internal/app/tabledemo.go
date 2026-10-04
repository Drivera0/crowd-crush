package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
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
//   - the boards and the laptop go in a row along the table at the demo
//     spot if it is on, else the middle of zone A (or a spot given as
//     {x, y}): zone light A at the left end, the sign in the middle with
//     the laptop beside it, zone light B at the right end, the Bluetooth
//     boards tableBeaconGap apart so a phone walked up to one can tell it
//     from the next (beaconsnap.go);
//   - with no areas drawn the whole table, phones included, is kept inside
//     one default zone (tableZoneLocked): the table profile and the wave
//     chain work per zone. That zone's light (A) shows the table; light B
//     shows zone B, where nobody stands (drills, or a phone moved there);
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
	// tableBeaconGap: m between the Bluetooth boards along the table. A
	// phone walked up to one board must hear it far louder than the next
	// (snapRatio × snapEnterM ≈ 1.2 m, beaconsnap.go); 1.5 m leaves a margin.
	tableBeaconGap = 1.5
	tableLaptopOff = 0.5 // m from the sign to this laptop (not a beacon)
	tableRowAhead  = 1.0 // m from the board row to the phone row
	tablePhones    = 4   // phones the row is centred for
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
	// Where each marker goes along the table, from its centre: zone light A
	// at the left end, the sign in the middle with this laptop beside it,
	// zone light B at the right end, any further lights beyond, every
	// Bluetooth board tableBeaconGap from the next (beaconsnap.go).
	offs := map[string]float64{towerLaptop: 0}
	if hasSign {
		offs["sign"], offs[towerLaptop] = 0, tableLaptopOff
	}
	for i, l := range lights {
		switch i {
		case 0:
			offs[l] = -tableBeaconGap
		default:
			offs[l] = float64(i) * tableBeaconGap
		}
	}
	lo, hi := -half, half
	for _, o := range offs {
		lo, hi = min(lo, o), max(hi, o)
	}
	var ax, ay float64
	switch {
	case x != nil:
		ax, ay = *x, *y
	case d.On:
		ax, ay = d.X+half, d.Y-tableRowAhead/2
	default:
		ax, ay = cfg.VenueW/2, cfg.VenueH/2
		if len(a.areas) == 0 {
			// The middle of zone A, not the middle of the map (the line
			// between zones A and B): see tableZoneLocked.
			if z := detect.DefaultZones(cfg); len(z) > 0 {
				ax, ay = polyCentre(z[0].Poly)
			}
		}
	}
	ax, ay = a.tableZoneLocked(ax, ay, lo, hi)
	// Keep the whole row and the phones inside the venue.
	ax = clampTo(ax, -lo+0.3, cfg.VenueW-hi-0.3)
	ay = clampTo(ay, tableRowAhead/2+0.3, cfg.VenueH-tableRowAhead/2-0.3)
	pos := map[string]protocol.Point{}
	for k, v := range a.hwPos {
		pos[k] = v
	}
	for k, o := range offs {
		bx, by := cfg.Clamp(ax+o, ay-tableRowAhead/2)
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
		table := ""
		for _, z := range detect.DefaultZones(cfg) {
			names[z.ID] = z.Name
			if x0, y0, x1, y1 := polyBox(z.Poly); table == "" && demo.X >= x0 && demo.X <= x1 && demo.Y >= y0 && demo.Y <= y1 {
				table = z.ID
			}
		}
		for _, l := range lights {
			tl := protocol.TableLight{Key: l, Shows: names[l]}
			switch {
			case tl.Shows == "":
				out.Notes = append(out.Notes, "Zone light "+l+" has no zone of its own: draw an area on Areas & alerts and choose this light for it.")
			case table != "" && l != table:
				out.Notes = append(out.Notes, "The phones at the table are all in "+names[table]+", so a push lights zone light "+table+". Zone light "+l+" shows "+tl.Shows+" (an alert drill there, or a phone dragged into it).")
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

// tableZoneLocked moves the table centre (ax, ay) so that the whole table
// (markers from lo to hi along it, the phone row in front, and the spots
// beside the boards) lies inside one of the default zones: the one holding
// (ax, ay). The table-demo profile and the wave chain work per zone (at most
// Table.MaxPhones phones, three in a chain), so a row split between zones A
// and B could only ever show yellow halves. With areas drawn the table goes
// where it was asked: staff chose the zones. Caller holds mu.
func (a *App) tableZoneLocked(ax, ay, lo, hi float64) (float64, float64) {
	if len(a.areas) > 0 {
		return ax, ay
	}
	const m = snapSpotM + 0.2 // m of zone left around the table
	for _, z := range detect.DefaultZones(a.liveConfig()) {
		x0, y0, x1, y1 := polyBox(z.Poly)
		if ax < x0 || ax > x1 || ay < y0 || ay > y1 {
			continue
		}
		ax = clampTo(ax, x0-lo+m, x1-hi-m)
		ay = clampTo(ay, y0+tableRowAhead/2+m, y1-tableRowAhead/2-m)
		return ax, ay
	}
	return ax, ay
}

func polyBox(p [][2]float64) (x0, y0, x1, y1 float64) {
	x0, y0, x1, y1 = math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, q := range p {
		x0, y0, x1, y1 = min(x0, q[0]), min(y0, q[1]), max(x1, q[0]), max(y1, q[1])
	}
	return
}

func polyCentre(p [][2]float64) (float64, float64) {
	x0, y0, x1, y1 := polyBox(p)
	return (x0 + x1) / 2, (y0 + y1) / 2
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
