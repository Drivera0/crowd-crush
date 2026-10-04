package brief

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var info = Info{Zone: "B", Level: "red", Direction: "+x", LagMs: 250, Scores: []float64{0.4, 0.7}}

func TestNoKeyUsesTemplate(t *testing.T) {
	b, err := New("", "").Brief(context.Background(), info)
	text := b.Text()
	if !errors.Is(err, ErrNoKey) || !strings.Contains(text, "Zone B") || !strings.Contains(text, "left to right") {
		t.Fatalf("got %q, %v", text, err)
	}
	if b.Headline == "" || b.Action == "" {
		t.Fatalf("template must fill both fields: %+v", b)
	}
}

// gemini answers every request with body (a candidate's text) and records
// the request.
func gemini(t *testing.T, text string, got *map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if got != nil {
			*got = body
		}
		resp, _ := json.Marshal(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": text}}}}}})
		w.Write(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGeminiRequestAndFallback(t *testing.T) {
	var gotKey, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotPath = r.Header.Get("x-goog-api-key"), r.URL.Path
		var body struct {
			SystemInstruction any            `json:"systemInstruction"`
			GenerationConfig  map[string]any `json:"generationConfig"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.SystemInstruction == nil {
			t.Error("no system instruction")
		}
		if body.GenerationConfig["responseMimeType"] != "application/json" || body.GenerationConfig["responseSchema"] == nil {
			t.Errorf("briefing should ask for structured JSON: %v", body.GenerationConfig)
		}
		w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"{\"headline\":\"Zone B is surging.\",\"action\":\"Hold the gates.\"}"}]}}]}`))
	}))
	defer srv.Close()
	c := New("k", "m1")
	c.base = srv.URL
	b, err := c.Brief(context.Background(), info)
	if err != nil || b.Headline != "Zone B is surging." || b.Action != "Hold the gates." || b.Text() != "Zone B is surging. Hold the gates." {
		t.Fatalf("got %+v, %v", b, err)
	}
	if gotKey != "k" || gotPath != "/v1beta/models/m1:generateContent" {
		t.Fatalf("key %q path %q", gotKey, gotPath)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "quota", http.StatusTooManyRequests)
	}))
	defer bad.Close()
	c.base = bad.URL
	b, err = c.Brief(context.Background(), info)
	if err == nil || b != Template(info) {
		t.Fatalf("want template on error, got %+v, %v", b, err)
	}
}

// A model that ignores the schema and writes plain sentences still works.
func TestPlainTextBriefing(t *testing.T) {
	c := New("k", "")
	c.base = gemini(t, "Zone B is surging.\nHold the gates.", nil).URL
	b, err := c.Brief(context.Background(), info)
	if err != nil || b.Headline != "Zone B is surging." || b.Action != "Hold the gates." {
		t.Fatalf("got %+v, %v", b, err)
	}
	c.base = gemini(t, `{"headline": "cut off`, nil).URL
	if b, err := c.Brief(context.Background(), info); err == nil || b != Template(info) {
		t.Fatalf("broken JSON: want template and an error, got %+v, %v", b, err)
	}
}

// Staff's message is the action, verbatim, from Gemini and the template.
func TestStaffMessage(t *testing.T) {
	in := info
	in.Message = "Open the side gate and slow the barrier queue"
	if b := Template(in); b.Action != in.Message || !strings.Contains(b.Headline, "Zone B") {
		t.Fatalf("template %+v", b)
	}
	var got map[string]any
	c := New("k", "")
	c.base = gemini(t, `{"headline":"Zone B: a push is travelling left to right.","action":"Open the gate."}`, &got).URL
	b, err := c.Brief(context.Background(), in)
	if err != nil || b.Action != in.Message {
		t.Fatalf("got %+v, %v", b, err)
	}
	raw, _ := json.Marshal(got)
	if !strings.Contains(string(raw), "verbatim") || !strings.Contains(string(raw), "slow the barrier queue") {
		t.Errorf("prompt should carry the message as a verbatim action: %s", raw)
	}
}

func TestRetriesWithoutThinking(t *testing.T) {
	// gemini-3.5-flash-lite says only "invalid argument", without naming thinking.
	for _, reject := range []string{"Thinking budget is not supported", "Request contains an invalid argument."} {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			var body struct {
				GenerationConfig map[string]any `json:"generationConfig"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if _, ok := body.GenerationConfig["thinkingConfig"]; ok {
				http.Error(w, `{"error":{"message":"`+reject+`"}}`, http.StatusBadRequest)
				return
			}
			w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"{\"headline\":\"ok\",\"action\":\"go\"}"}]}}]}`))
		}))
		c := New("k", "")
		c.base = srv.URL
		b, err := c.Brief(context.Background(), info)
		srv.Close()
		if err != nil || b.Text() != "ok go" || calls != 2 {
			t.Fatalf("%q: got %+v %v after %d calls", reject, b, err, calls)
		}
	}
}

// A drawn area has a random id; people must hear its name.
func TestTemplateUsesPlaceName(t *testing.T) {
	text := Template(Info{Zone: "n2549bj", Where: "Stage front", Level: "red", Direction: "+x", LagMs: 250}).Text()
	if strings.Contains(text, "n2549bj") || !strings.Contains(text, "Stage front") {
		t.Fatalf("got %q", text)
	}
}

func TestTemplates(t *testing.T) {
	w := Template(Info{Zone: "A", Level: "red", Direction: "+x", LagMs: 250})
	if !strings.Contains(w.Headline, "left to right") || !strings.Contains(w.Action, "Stop entry") {
		t.Errorf("wave template %+v", w)
	}
	if DirectionText("-y") == DirectionText("") || DirectionText("+row") == DirectionText("") {
		t.Error("direction text missing")
	}
	d := Template(Info{Kind: "density", Zone: "A", Level: "red", People: 18, AreaM2: 4.2, X: 12, Y: 4, Trend: "forming"}).Text()
	for _, want := range []string{"Zone A", "18 people", "4 square metres", "denser"} {
		if !strings.Contains(d, want) {
			t.Errorf("density template %q lacks %q", d, want)
		}
	}
	c := Template(Info{Kind: "rule", Rule: "capacity", Zone: "g", Where: "Gate", Level: "red", Phones: 31, Limit: 30})
	if !strings.Contains(c.Headline, "31 phones") || !strings.Contains(c.Headline, "limit of 30") || !strings.Contains(c.Action, "Gate") {
		t.Errorf("capacity template %+v", c)
	}
	r := Template(Info{Kind: "rule", Rule: "density", Zone: "g", Where: "Gate", Level: "red", Density: 4.4, Limit: 3.5})
	if !strings.Contains(r.Headline, "limit of 3.5") {
		t.Errorf("density rule template %+v", r)
	}
}

func pngBytes(w, h int) []byte {
	var buf bytes.Buffer
	png.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h)))
	return buf.Bytes()
}

func TestReadFloorplan(t *testing.T) {
	if _, err := New("", "").ReadFloorplan(context.Background(), pngBytes(4, 2), "image/png"); !errors.Is(err, ErrNoKey) {
		t.Fatalf("no key: %v", err)
	}
	// 40 × 20 m image; Gemini answers on the 0..1000 image grid, with some junk.
	answer := `{"w": 40, "h": 21, "confidence": "Medium",
	  "notes": "Scale from the 10 m scale bar.\nStage at the top.",
	  "stage": [{"x": 250, "y": 0}, {"x": 750, "y": 0}, {"x": 750, "y": 150}, {"x": 250, "y": 150}],
	  "exits": [{"name": "Main doors", "x0": 450, "y0": 1000, "x1": 550, "y1": 1000},
	            {"name": "", "x0": -50, "y0": 400, "x1": 0, "y1": 500},
	            {"name": "dot", "x0": 10, "y0": 10, "x1": 10, "y1": 10}],
	  "walls": [{"x0": 0, "y0": 0, "x1": 1000, "y1": 0}, {"x0": 0, "y0": 0, "x1": 0, "y1": 2000}]}`
	var got map[string]any
	c := New("k", "")
	c.base = gemini(t, answer, &got).URL
	s, err := c.ReadFloorplan(context.Background(), pngBytes(400, 200), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if s.W != 40 || s.H != 21 || s.Confidence != "medium" || !strings.Contains(s.Notes, "scale bar") {
		t.Errorf("size/notes: %+v", s)
	}
	if len(s.Layout.Stage) != 4 || s.Layout.Stage[1] != [2]float64{30, 0} || s.Layout.Stage[2][1] != 3.2 {
		t.Errorf("stage %v", s.Layout.Stage)
	}
	if len(s.Layout.Exits) != 2 || s.Layout.Exits[0].Name != "Main doors" || s.Layout.Exits[0].X0 != 18 || s.Layout.Exits[0].Y0 != 21 ||
		s.Layout.Exits[1].Name != "Exit 2" || s.Layout.Exits[1].X0 != 0 || s.Layout.Exits[0].ID == s.Layout.Exits[1].ID {
		t.Errorf("exits %+v", s.Layout.Exits)
	}
	if len(s.Layout.Walls) != 2 || s.Layout.Walls[1][3] != 21 {
		t.Errorf("walls %v (want clamped to h)", s.Layout.Walls)
	}
	// The request: a vision call with structured output.
	raw, _ := json.Marshal(got)
	for _, want := range []string{`"inlineData"`, `"mimeType":"image/png"`, `"responseMimeType":"application/json"`, `"responseSchema"`, "0.9 m", "scale bar"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("request lacks %s", want)
		}
	}
}

func TestParseFloorplanValidation(t *testing.T) {
	for name, ans := range map[string]string{
		"not json": "I think it is a theatre.",
		"no size":  `{"w": 0, "h": 10, "notes": "", "confidence": "low"}`,
		"nan-ish":  `{"w": -3, "h": 10}`,
	} {
		if _, err := parseFloorplan(ans, 0); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Sizes clamp to 2–5000 m; an h that disagrees with the image is fixed.
	s, err := parseFloorplan(`{"w": 90000, "h": 1, "confidence": "sure", "notes": ""}`, 0)
	if err != nil || s.W != MaxVenueM || s.H != MinVenueM || s.Confidence != "low" || s.Notes == "" {
		t.Errorf("clamp: %+v %v", s, err)
	}
	s, _ = parseFloorplan(`{"w": 30, "h": 30, "confidence": "high", "notes": "ok"}`, 0.5)
	if s.H != 15 || !strings.Contains(s.Notes, "adjusted") {
		t.Errorf("aspect: %+v", s)
	}
	// Caps.
	var walls []string
	for i := 0; i < MaxWalls+20; i++ {
		walls = append(walls, `{"x0":0,"y0":0,"x1":10,"y1":10}`)
	}
	s, _ = parseFloorplan(`{"w": 10, "h": 10, "confidence": "low", "notes": "x", "walls": [`+strings.Join(walls, ",")+`]}`, 0)
	if len(s.Layout.Walls) != MaxWalls {
		t.Errorf("walls not capped: %d", len(s.Layout.Walls))
	}
}
