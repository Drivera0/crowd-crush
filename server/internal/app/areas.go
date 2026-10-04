package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/geo"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// Limits on staff-drawn areas.
const (
	MaxAreas       = 50
	MaxAreaPoints  = 200
	MaxAreaName    = 40
	MaxAreaID      = 40
	areasFile      = "areas.json"
	venueFile      = "venue.json"
	maxVenueMetres = 5000
)

// Areas returns the staff-drawn areas (never nil).
func (a *App) Areas() []protocol.Area {
	a.mu.Lock()
	defer a.mu.Unlock()
	return cloneAreas(a.areas)
}

// SetAreas validates and replaces every custom area, clamps their points to
// the venue, saves them and applies them to the live (and replay) zones.
// Zones that keep their ID keep their level.
func (a *App) SetAreas(areas []protocol.Area) ([]protocol.Area, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	clean, err := validAreas(areas, a.liveConfig())
	if err != nil {
		return nil, err
	}
	if err := a.save(areasFile, clean); err != nil {
		return nil, err
	}
	a.areas = clean
	a.applyZones(a.live)
	if a.replay != nil {
		a.applyReplayZones(a.replay)
	}
	if a.sim != nil {
		a.applySimZones(a.sim)
	}
	log.Printf("areas: %d custom area(s)", len(clean))
	return cloneAreas(clean), nil
}

func validAreas(in []protocol.Area, cfg detect.Config) ([]protocol.Area, error) {
	if len(in) > MaxAreas {
		return nil, fmt.Errorf("at most %d areas", MaxAreas)
	}
	out := make([]protocol.Area, 0, len(in))
	seen := map[string]bool{}
	for i, ar := range in {
		ar.ID = strings.TrimSpace(ar.ID)
		ar.Name = strings.TrimSpace(ar.Name)
		switch {
		case ar.ID == "":
			return nil, fmt.Errorf("area %d: id is empty", i)
		case utf8.RuneCountInString(ar.ID) > MaxAreaID:
			return nil, fmt.Errorf("area %q: id longer than %d characters", ar.ID, MaxAreaID)
		case ar.ID == detect.RestZone:
			return nil, fmt.Errorf("area id %q is reserved for the rest of the venue", ar.ID)
		case seen[ar.ID]:
			return nil, fmt.Errorf("area id %q is used twice", ar.ID)
		case utf8.RuneCountInString(ar.Name) > MaxAreaName:
			return nil, fmt.Errorf("area %q: name longer than %d characters", ar.ID, MaxAreaName)
		case len(ar.Poly) < 3:
			return nil, fmt.Errorf("area %q: needs at least 3 points", ar.ID)
		case len(ar.Poly) > MaxAreaPoints:
			return nil, fmt.Errorf("area %q: at most %d points", ar.ID, MaxAreaPoints)
		}
		seen[ar.ID] = true
		switch ar.Sens {
		case "":
			ar.Sens = detect.SensNormal
		case detect.SensNormal, detect.SensHigh:
		default:
			return nil, fmt.Errorf("area %q: sens must be normal or high", ar.ID)
		}
		if ar.Name == "" {
			ar.Name = ar.ID
		}
		ar.Light = strings.ToUpper(strings.TrimSpace(ar.Light))
		if !validLight(ar.Light) {
			return nil, fmt.Errorf("area %q: light must be a short letter/number like A", ar.ID)
		}
		poly := make([]protocol.Point, len(ar.Poly))
		for j, pt := range ar.Poly {
			if !finite(pt[0]) || !finite(pt[1]) {
				return nil, fmt.Errorf("area %q: point %d is not a number", ar.ID, j)
			}
			x, y := cfg.Clamp(pt[0], pt[1])
			poly[j] = protocol.Point{r2(x), r2(y)}
		}
		ar.Poly = poly
		rules, err := validRules(ar.ID, ar.Rules)
		if err != nil {
			return nil, err
		}
		ar.Rules = rules
		out = append(out, ar)
	}
	return out, nil
}

func cloneAreas(in []protocol.Area) []protocol.Area {
	out := make([]protocol.Area, len(in))
	for i, ar := range in {
		ar.Poly = append([]protocol.Point(nil), ar.Poly...)
		ar.Rules = cloneRules(ar.Rules)
		out[i] = ar
	}
	return out
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// validLight accepts "" or a zone-light key as written in SIGN_URL (A, B, ZONE2).
func validLight(s string) bool {
	if len(s) > 8 {
		return false
	}
	for _, r := range s {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// lightLevels adds what each zone light should show to the sign levels:
// the worst level among the areas assigned to it, and calm for lights with
// no area (when drawn areas replace the default zones, nothing else would
// ever reset them). Caller holds mu.
func (a *App) lightLevels(levels map[string]string, lights []string) {
	assigned := map[string]bool{}
	for _, ar := range a.areas {
		if ar.Light == "" {
			continue
		}
		lv, ok := levels[ar.ID]
		if ar.Rules != nil && ar.Rules.Notify != nil && !on(ar.Rules.Notify.Light) {
			// Staff turned this area's light off: it counts as calm there.
			lv, ok = protocol.LevelCalm, true
		}
		if !ok {
			continue
		}
		if !assigned[ar.Light] || levelRank[lv] > levelRank[levels[ar.Light]] {
			levels[ar.Light] = lv
		}
		assigned[ar.Light] = true
	}
	for _, l := range lights {
		if _, ok := levels[l]; !ok {
			levels[l] = protocol.LevelCalm
		}
	}
}

// lightFor is the light assigned to a zone, if any: the area's, or with no
// areas drawn the light keyed like the default zone (A=… shows zone A, as
// the live levels already do). Caller holds mu.
func (a *App) lightFor(zone string) string {
	for _, ar := range a.areas {
		if ar.ID == zone {
			return ar.Light
		}
	}
	if len(a.areas) == 0 {
		for _, l := range a.opt.Sign.Zones() {
			if l == zone {
				return l
			}
		}
	}
	return ""
}

// ---- venue ----

// Venue returns the venue's size and geo-anchor.
func (a *App) Venue() protocol.Venue {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.venue
}

// SetVenue validates, saves and applies a new venue size and anchor. Phones
// placed by hand are clamped into the new size; default zones are rebuilt.
func (a *App) SetVenue(v protocol.Venue) (protocol.Venue, error) {
	if err := validVenue(&v); err != nil {
		return protocol.Venue{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	v.Floorplan = a.plan != nil // the server owns this flag: upload or delete the image to change it
	if err := a.save(venueFile, v); err != nil {
		return protocol.Venue{}, err
	}
	a.venue = v
	a.live.det.SetVenue(v.W, v.H)
	a.applyZones(a.live)
	cfg := a.liveConfig()
	for id, m := range a.live.meta {
		m.x, m.y = cfg.Clamp(m.x, m.y)
		if !v.Geo {
			m.outside = false
		}
		a.live.place(id, m)
	}
	log.Printf("venue: %g x %g m, geo-anchor %v", v.W, v.H, v.Geo) // never log the anchor's coordinates alongside phones
	return v, nil
}

// validVenue checks a venue and normalises it in place.
func validVenue(v *protocol.Venue) error {
	if !(v.W >= 1 && v.H >= 1 && v.W <= maxVenueMetres && v.H <= maxVenueMetres) {
		return fmt.Errorf("venue w and h must be between 1 and %d m", maxVenueMetres)
	}
	v.Template = strings.TrimSpace(v.Template)
	if utf8.RuneCountInString(v.Template) > MaxTemplate {
		return fmt.Errorf("template longer than %d characters", MaxTemplate)
	}
	l, err := validLayout(v.Layout, v.W, v.H)
	if err != nil {
		return err
	}
	v.Layout = l
	if !v.Geo {
		v.Lat, v.Lon, v.Bearing = 0, 0, 0
		return nil
	}
	if !finite(v.Bearing) {
		return errors.New("bearing must be a number")
	}
	v.Bearing = geo.NormBearing(v.Bearing)
	if !(geo.Anchor{Lat: v.Lat, Lon: v.Lon, Bearing: v.Bearing}).Valid() {
		return errors.New("lat must be within ±85 and lon within ±180")
	}
	return nil
}

// ---- persistence ----

func (a *App) save(name string, v any) error {
	if a.opt.DataDir == "" {
		return nil
	}
	if err := os.MkdirAll(a.opt.DataDir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(a.opt.DataDir, name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (a *App) load(name string, v any) bool {
	if a.opt.DataDir == "" {
		return false
	}
	b, err := os.ReadFile(filepath.Join(a.opt.DataDir, name))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("%s: %v", name, err)
		}
		return false
	}
	if err := json.Unmarshal(b, v); err != nil {
		log.Printf("%s: %v (ignored)", name, err)
		return false
	}
	return true
}

func (a *App) loadAreas() []protocol.Area {
	var areas []protocol.Area
	if !a.load(areasFile, &areas) {
		return []protocol.Area{}
	}
	clean, err := validAreas(areas, a.liveConfig())
	if err != nil {
		log.Printf("%s: %v (ignored)", areasFile, err)
		return []protocol.Area{}
	}
	log.Printf("areas: loaded %d custom area(s)", len(clean))
	return clean
}

func (a *App) loadVenue() (protocol.Venue, bool) {
	var v protocol.Venue
	if !a.load(venueFile, &v) {
		return v, false
	}
	return v, true
}
