package brief

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var info = Info{Zone: "B", Level: "red", Direction: "+x", LagMs: 250, Scores: []float64{0.4, 0.7}}

func TestNoKeyUsesTemplate(t *testing.T) {
	text, err := New("", "").Brief(context.Background(), info)
	if !errors.Is(err, ErrNoKey) || !strings.Contains(text, "Zone B") || !strings.Contains(text, "left to right") {
		t.Fatalf("got %q, %v", text, err)
	}
}

func TestGeminiRequestAndFallback(t *testing.T) {
	var gotKey, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotPath = r.Header.Get("x-goog-api-key"), r.URL.Path
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["systemInstruction"] == nil {
			t.Error("no system instruction")
		}
		w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"Zone B is surging.\nHold the gates."}]}}]}`))
	}))
	defer srv.Close()
	c := New("k", "m1")
	c.base = srv.URL
	text, err := c.Brief(context.Background(), info)
	if err != nil || text != "Zone B is surging. Hold the gates." {
		t.Fatalf("got %q, %v", text, err)
	}
	if gotKey != "k" || gotPath != "/v1beta/models/m1:generateContent" {
		t.Fatalf("key %q path %q", gotKey, gotPath)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "quota", http.StatusTooManyRequests)
	}))
	defer bad.Close()
	c.base = bad.URL
	text, err = c.Brief(context.Background(), info)
	if err == nil || text != Template(info) {
		t.Fatalf("want template on error, got %q, %v", text, err)
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
			w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`))
		}))
		c := New("k", "")
		c.base = srv.URL
		text, err := c.Brief(context.Background(), info)
		srv.Close()
		if err != nil || text != "ok" || calls != 2 {
			t.Fatalf("%q: got %q %v after %d calls", reject, text, err, calls)
		}
	}
}

// A drawn area has a random id; people must hear its name.
func TestTemplateUsesPlaceName(t *testing.T) {
	text := Template(Info{Zone: "n2549bj", Where: "Stage front", Level: "red", Direction: "+x", LagMs: 250})
	if strings.Contains(text, "n2549bj") || !strings.Contains(text, "Stage front") {
		t.Fatalf("got %q", text)
	}
}

func TestTemplates(t *testing.T) {
	w := Template(Info{Zone: "A", Level: "red", Direction: "+x", LagMs: 250})
	if !strings.Contains(w, "left to right") {
		t.Errorf("wave template %q", w)
	}
	if DirectionText("-y") == DirectionText("") || DirectionText("+row") == DirectionText("") {
		t.Error("direction text missing")
	}
	d := Template(Info{Kind: "density", Zone: "A", Level: "red", People: 18, AreaM2: 4.2, X: 12, Y: 4, Trend: "forming"})
	for _, want := range []string{"Zone A", "18 people", "4 square metres", "denser"} {
		if !strings.Contains(d, want) {
			t.Errorf("density template %q lacks %q", d, want)
		}
	}
}
