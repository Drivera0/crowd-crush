package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/Drivera0/crowd-crush/server/internal/brief"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// Venue layout and floor plan.
const (
	MaxStagePoints   = brief.MaxStagePts // 64
	MaxExits         = brief.MaxExits    // 32
	MaxWalls         = brief.MaxWalls    // 128
	MaxLayoutName    = 40
	MaxTemplate      = 40
	MaxFloorplanSize = 8 << 20
	floorplanFile    = "floorplan"
	floorplanMeta    = "floorplan.json"
)

// floorplanTypes are the image types a floor plan may be, as sniffed.
var floorplanTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/webp": true}

// validLayout checks a venue layout against a w × h venue: points clamped
// into it (to the centimetre), counts capped, names trimmed. An empty
// layout becomes nil.
func validLayout(l *protocol.VenueLayout, w, h float64) (*protocol.VenueLayout, error) {
	if l == nil {
		return nil, nil
	}
	clamp := func(x, y float64) (float64, float64) {
		return r2(math.Max(0, math.Min(w, x))), r2(math.Max(0, math.Min(h, y)))
	}
	out := &protocol.VenueLayout{}
	switch n := len(l.Stage); {
	case n > MaxStagePoints:
		return nil, fmt.Errorf("stage: at most %d points", MaxStagePoints)
	case n > 0 && n < 3:
		return nil, errors.New("stage: needs at least 3 points")
	}
	for i, p := range l.Stage {
		if !finite(p[0]) || !finite(p[1]) {
			return nil, fmt.Errorf("stage: point %d is not a number", i)
		}
		x, y := clamp(p[0], p[1])
		out.Stage = append(out.Stage, protocol.Point{x, y})
	}
	if len(l.Exits) > MaxExits {
		return nil, fmt.Errorf("at most %d exits", MaxExits)
	}
	seen := map[string]bool{}
	for i, e := range l.Exits {
		if !finite(e.X0) || !finite(e.Y0) || !finite(e.X1) || !finite(e.Y1) {
			return nil, fmt.Errorf("exit %d: not a number", i)
		}
		e.ID, e.Name = strings.TrimSpace(e.ID), strings.Join(strings.Fields(e.Name), " ")
		if utf8.RuneCountInString(e.Name) > MaxLayoutName {
			return nil, fmt.Errorf("exit %d: name longer than %d characters", i, MaxLayoutName)
		}
		if e.ID == "" || utf8.RuneCountInString(e.ID) > MaxAreaID || seen[e.ID] {
			e.ID = fmt.Sprintf("exit-%d", i+1)
			for k := i + 2; seen[e.ID]; k++ {
				e.ID = fmt.Sprintf("exit-%d", k)
			}
		}
		if e.Name == "" {
			e.Name = fmt.Sprintf("Exit %d", i+1)
		}
		e.X0, e.Y0 = clamp(e.X0, e.Y0)
		e.X1, e.Y1 = clamp(e.X1, e.Y1)
		if e.X0 == e.X1 && e.Y0 == e.Y1 {
			continue // nothing left of it inside the venue
		}
		seen[e.ID] = true
		out.Exits = append(out.Exits, e)
	}
	if len(l.Walls) > MaxWalls {
		return nil, fmt.Errorf("at most %d walls", MaxWalls)
	}
	for i, s := range l.Walls {
		for _, v := range s {
			if !finite(v) {
				return nil, fmt.Errorf("wall %d: not a number", i)
			}
		}
		x0, y0 := clamp(s[0], s[1])
		x1, y1 := clamp(s[2], s[3])
		if x0 == x1 && y0 == y1 {
			continue
		}
		out.Walls = append(out.Walls, [4]float64{x0, y0, x1, y1})
	}
	if len(out.Stage) == 0 && len(out.Exits) == 0 && len(out.Walls) == 0 {
		return nil, nil
	}
	return out, nil
}

// ---- floor plan ----

type floorplan struct {
	data []byte
	mime string
}

// SetFloorplan stores a floor-plan image (PNG, JPEG or WebP, sniffed from
// the bytes) and returns the venue with floorplan:true.
func (a *App) SetFloorplan(img []byte) (protocol.Venue, error) {
	mime := http.DetectContentType(img)
	if !floorplanTypes[mime] {
		return protocol.Venue{}, fmt.Errorf("want a PNG, JPEG or WebP image (got %s)", mime)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.opt.DataDir != "" {
		if err := os.MkdirAll(a.opt.DataDir, 0o755); err != nil {
			return protocol.Venue{}, err
		}
		path := filepath.Join(a.opt.DataDir, floorplanFile)
		if err := os.WriteFile(path+".tmp", img, 0o644); err != nil {
			return protocol.Venue{}, err
		}
		if err := os.Rename(path+".tmp", path); err != nil {
			return protocol.Venue{}, err
		}
		if err := a.save(floorplanMeta, map[string]string{"type": mime}); err != nil {
			return protocol.Venue{}, err
		}
	}
	a.plan = &floorplan{data: img, mime: mime}
	a.venue.Floorplan = true
	if err := a.save(venueFile, a.venue); err != nil {
		log.Printf("venue: %v", err)
	}
	log.Printf("venue: floor plan stored (%s, %d KB)", mime, len(img)>>10)
	return a.venue, nil
}

// Floorplan returns the stored image and its type (nil if none).
func (a *App) Floorplan() ([]byte, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.plan == nil {
		return nil, ""
	}
	return a.plan.data, a.plan.mime
}

// DeleteFloorplan removes the stored image.
func (a *App) DeleteFloorplan() (protocol.Venue, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.opt.DataDir != "" {
		for _, f := range []string{floorplanFile, floorplanMeta} {
			if err := os.Remove(filepath.Join(a.opt.DataDir, f)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return protocol.Venue{}, err
			}
		}
	}
	a.plan = nil
	a.venue.Floorplan = false
	if err := a.save(venueFile, a.venue); err != nil {
		log.Printf("venue: %v", err)
	}
	return a.venue, nil
}

// errNoFloorplan: analyze before uploading.
var errNoFloorplan = errors.New("no floor plan uploaded (POST /api/venue/floorplan first)")

// AnalyzeFloorplan asks Gemini to read the stored plan. Saves nothing.
func (a *App) AnalyzeFloorplan(ctx context.Context) (protocol.FloorplanSuggestion, error) {
	if !a.opt.Brief.Enabled() {
		return protocol.FloorplanSuggestion{}, brief.ErrNoKey
	}
	img, mime := a.Floorplan()
	if img == nil {
		return protocol.FloorplanSuggestion{}, errNoFloorplan
	}
	s, err := a.opt.Brief.ReadFloorplan(ctx, img, mime)
	if err != nil {
		return s, err
	}
	// The same limits as PUT /api/venue, so the dashboard can apply it as is.
	l, err := validLayout(&s.Layout, s.W, s.H)
	if err != nil {
		return protocol.FloorplanSuggestion{}, fmt.Errorf("gemini: %w", err)
	}
	s.Layout = protocol.VenueLayout{}
	if l != nil {
		s.Layout = *l
	}
	log.Printf("venue: Gemini read the floor plan: %g x %g m, %d exits, %d walls, stage %v (%s confidence)",
		s.W, s.H, len(s.Layout.Exits), len(s.Layout.Walls), len(s.Layout.Stage) > 0, s.Confidence)
	return s, nil
}

// loadFloorplan reads the stored plan at startup. Caller owns a.
func (a *App) loadFloorplan() {
	if a.opt.DataDir == "" {
		return
	}
	img, err := os.ReadFile(filepath.Join(a.opt.DataDir, floorplanFile))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("%s: %v", floorplanFile, err)
		}
		return
	}
	var meta struct {
		Type string `json:"type"`
	}
	a.load(floorplanMeta, &meta)
	if !floorplanTypes[meta.Type] {
		meta.Type = http.DetectContentType(img)
	}
	if !floorplanTypes[meta.Type] {
		log.Printf("%s: not a PNG, JPEG or WebP image (ignored)", floorplanFile)
		return
	}
	a.plan = &floorplan{data: img, mime: meta.Type}
}
