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

var info = Info{Zone: "B", Level: "red", Direction: "+col", LagMs: 250, Scores: []float64{0.4, 0.7}}

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
