package voice

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSpeakWritesMP3(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("xi-api-key") != "k" || !strings.Contains(r.URL.Path, "/v1/text-to-speech/v1") {
			http.Error(w, "bad", http.StatusUnauthorized)
			return
		}
		w.Write([]byte("ID3fake"))
	}))
	defer srv.Close()
	dir := t.TempDir()
	c := New("k", "v1", dir)
	c.base = srv.URL
	url, err := c.Speak(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, strings.TrimPrefix(url, "/audio/")))
	if err != nil || string(b) != "ID3fake" {
		t.Fatalf("file: %q %v", b, err)
	}
	if c.FallbackURL() != "" {
		t.Fatal("fallback exists before it was made")
	}
	if err := c.PrepareFallback(context.Background()); err != nil || c.FallbackURL() == "" {
		t.Fatalf("fallback: %v", err)
	}
}

func TestNoKey(t *testing.T) {
	if _, err := New("", "", t.TempDir()).Speak(context.Background(), "x"); !errors.Is(err, ErrNoKey) {
		t.Fatal(err)
	}
}
