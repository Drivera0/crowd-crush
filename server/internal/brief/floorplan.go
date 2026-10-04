package brief

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // DecodeConfig for the plan's aspect ratio
	_ "image/png"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// FloorplanTimeout bounds a floor-plan reading: a vision request on a
// large image takes longer than a briefing.
const FloorplanTimeout = 20 * time.Second

// Limits on what a floor-plan reading may return (the venue API's limits).
const (
	MinVenueM     = 2
	MaxVenueM     = 5000
	MaxStagePts   = 64
	MaxExits      = 32
	MaxWalls      = 128
	MaxExitName   = 40
	maxNotesRunes = 600
	normScale     = 1000 // Gemini's coordinates: 0..1000 across the image
)

const floorplanSystem = `You read venue floor plans for a crowd-safety system used by event staff.
You get one image: a floor plan, site map, sketch or photo of a plan. Find:
- the venue outline (outer walls or fences),
- the stage (or main attraction) outline,
- every exit, door or gate people can leave through,
- walls and barriers inside the venue that people cannot cross (including the outline's walls).
Coordinates: give every point as x, y on a 0–1000 grid over the WHOLE IMAGE:
x = 0 at the image's left edge, 1000 at its right edge; y = 0 at the top edge, 1000 at the bottom edge.
Scale: estimate how many metres the whole image spans across (w) and down (h). Use, in this order:
dimension labels or a scale bar; door widths (a single door is about 0.9 m, double doors about 1.8 m);
typical sizes (a concert stage is about 10–15 m wide, a parking space 2.5 m, a seat row 0.9 m deep).
The image's own width/height ratio should match w/h.
Exits are short segments along the wall where the opening is. Walls are straight segments; approximate curves with a few segments.
Leave a list empty when the plan doesn't show that feature; never invent features.
In "notes" (two or three short sentences), say what you based the scale on, what you found,
and what you were unsure about. Set "confidence" to low, medium or high for the scale and layout overall.`

var ptSchema = map[string]any{
	"type":       "OBJECT",
	"properties": map[string]any{"x": map[string]any{"type": "NUMBER"}, "y": map[string]any{"type": "NUMBER"}},
	"required":   []string{"x", "y"},
}

func segSchema(named bool) map[string]any {
	props := map[string]any{}
	req := []string{}
	if named {
		props["name"] = map[string]any{"type": "STRING"}
		req = append(req, "name")
	}
	for _, k := range []string{"x0", "y0", "x1", "y1"} {
		props[k] = map[string]any{"type": "NUMBER"}
		req = append(req, k)
	}
	return map[string]any{"type": "OBJECT", "properties": props, "required": req}
}

var floorplanSchema = map[string]any{
	"type": "OBJECT",
	"properties": map[string]any{
		"w":          map[string]any{"type": "NUMBER", "description": "metres the whole image spans left to right"},
		"h":          map[string]any{"type": "NUMBER", "description": "metres the whole image spans top to bottom"},
		"stage":      map[string]any{"type": "ARRAY", "items": ptSchema, "description": "stage outline polygon, 0-1000 image grid"},
		"exits":      map[string]any{"type": "ARRAY", "items": segSchema(true)},
		"walls":      map[string]any{"type": "ARRAY", "items": segSchema(false)},
		"notes":      map[string]any{"type": "STRING"},
		"confidence": map[string]any{"type": "STRING", "enum": []string{"low", "medium", "high"}},
	},
	"required":         []string{"w", "h", "stage", "exits", "walls", "notes", "confidence"},
	"propertyOrdering": []string{"w", "h", "stage", "exits", "walls", "notes", "confidence"},
}

// rawPlan is Gemini's answer, coordinates on the 0..1000 image grid.
type rawPlan struct {
	W     float64 `json:"w"`
	H     float64 `json:"h"`
	Stage []struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	} `json:"stage"`
	Exits []struct {
		Name           string `json:"name"`
		X0, Y0, X1, Y1 float64
	} `json:"exits"`
	Walls []struct {
		X0, Y0, X1, Y1 float64
	} `json:"walls"`
	Notes      string `json:"notes"`
	Confidence string `json:"confidence"`
}

// ReadFloorplan asks Gemini to read a floor-plan image (PNG, JPEG or WebP)
// and returns the venue it shows in venue metres: origin at the image's
// top-left, x right, y down, the whole image spanning w × h. Gemini's
// numbers are validated and clamped. Nothing is saved.
func (c *Client) ReadFloorplan(ctx context.Context, img []byte, mime string) (protocol.FloorplanSuggestion, error) {
	if !c.Enabled() {
		return protocol.FloorplanSuggestion{}, ErrNoKey
	}
	user := "Read this floor plan. Reply with the JSON described."
	aspect := imageAspect(img)
	if aspect > 0 {
		user += fmt.Sprintf(" The image's height is %.3f times its width.", aspect)
	}
	out, err := c.generate(ctx, request{sys: floorplanSystem, user: user, image: img, mime: mime,
		maxTokens: 8192, schema: floorplanSchema, timeout: FloorplanTimeout, temp: 0.2})
	if err != nil {
		return protocol.FloorplanSuggestion{}, err
	}
	return parseFloorplan(out, aspect)
}

// imageAspect is h/w of a PNG or JPEG, 0 if unknown (WebP: no decoder in
// the standard library).
func imageAspect(img []byte) float64 {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(img))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return 0
	}
	return float64(cfg.Height) / float64(cfg.Width)
}

// parseFloorplan validates Gemini's answer and converts it to metres.
// aspect (h/w of the image, 0 = unknown) corrects an h that disagrees with
// the image by more than 10 %: the dashboard stretches the image over the
// venue, so the venue must have the image's shape.
func parseFloorplan(s string, aspect float64) (protocol.FloorplanSuggestion, error) {
	var r rawPlan
	if err := json.Unmarshal([]byte(stripFence(s)), &r); err != nil {
		return protocol.FloorplanSuggestion{}, fmt.Errorf("gemini: floor plan is not valid JSON: %w", err)
	}
	if !(r.W > 0) || !(r.H > 0) || math.IsInf(r.W, 0) || math.IsInf(r.H, 0) {
		return protocol.FloorplanSuggestion{}, errors.New("gemini: no usable venue size in the floor-plan reading")
	}
	notes := oneLine(r.Notes)
	w, h := clampF(r.W, MinVenueM, MaxVenueM), clampF(r.H, MinVenueM, MaxVenueM)
	if aspect > 0 {
		if want := w * aspect; math.Abs(h-want) > 0.1*want {
			h = clampF(want, MinVenueM, MaxVenueM)
			notes = strings.TrimSpace(notes + fmt.Sprintf(" Height adjusted to %.0f m to match the image's shape.", h))
		}
	}
	w, h = round1(w), round1(h)
	sx, sy := w/normScale, h/normScale
	px := func(v float64) float64 { return round1(clampF(v, 0, normScale) * sx) }
	py := func(v float64) float64 { return round1(clampF(v, 0, normScale) * sy) }

	out := protocol.FloorplanSuggestion{W: w, H: h, Notes: truncRunes(notes, maxNotesRunes), Confidence: strings.ToLower(strings.TrimSpace(r.Confidence))}
	switch out.Confidence {
	case "low", "medium", "high":
	default:
		out.Confidence = "low"
	}
	if out.Notes == "" {
		out.Notes = "Gemini gave no notes on how it read the plan."
	}
	if len(r.Stage) >= 3 {
		for _, p := range r.Stage {
			if len(out.Layout.Stage) == MaxStagePts {
				break
			}
			if finite(p.X) && finite(p.Y) {
				out.Layout.Stage = append(out.Layout.Stage, protocol.Point{px(p.X), py(p.Y)})
			}
		}
		if len(out.Layout.Stage) < 3 {
			out.Layout.Stage = nil
		}
	}
	for _, e := range r.Exits {
		if len(out.Layout.Exits) == MaxExits {
			break
		}
		if !finite(e.X0) || !finite(e.Y0) || !finite(e.X1) || !finite(e.Y1) {
			continue
		}
		ex := protocol.LayoutExit{ID: fmt.Sprintf("exit-%d", len(out.Layout.Exits)+1), Name: truncRunes(oneLine(e.Name), MaxExitName),
			X0: px(e.X0), Y0: py(e.Y0), X1: px(e.X1), Y1: py(e.Y1)}
		if ex.Name == "" {
			ex.Name = fmt.Sprintf("Exit %d", len(out.Layout.Exits)+1)
		}
		if ex.X0 == ex.X1 && ex.Y0 == ex.Y1 {
			continue
		}
		out.Layout.Exits = append(out.Layout.Exits, ex)
	}
	for _, s := range r.Walls {
		if len(out.Layout.Walls) == MaxWalls {
			break
		}
		if !finite(s.X0) || !finite(s.Y0) || !finite(s.X1) || !finite(s.Y1) {
			continue
		}
		wl := [4]float64{px(s.X0), py(s.Y0), px(s.X1), py(s.Y1)}
		if wl[0] == wl[2] && wl[1] == wl[3] {
			continue
		}
		out.Layout.Walls = append(out.Layout.Walls, wl)
	}
	return out, nil
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func clampF(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func truncRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
