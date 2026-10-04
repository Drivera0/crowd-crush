package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/sign"
)

func TestTableDemo(t *testing.T) {
	s := sign.New("http://127.0.0.1:1,A=http://127.0.0.1:1,B=http://127.0.0.1:1")
	a := New(Options{Detect: detect.DefaultConfig(), RecordingsDir: t.TempDir(), DataDir: t.TempDir(), Sign: s})
	res, err := a.TableDemo(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Default 24 × 16 m venue: the row is centred on the map, A left, B right.
	want := map[string]protocol.Point{"A": {10.8, 7.5}, "sign": {11.6, 7.5}, "laptop": {12.4, 7.5}, "B": {13.2, 7.5}}
	a.mu.Lock()
	got := a.hwPos
	a.mu.Unlock()
	for k, p := range want {
		if got[k] != p {
			t.Errorf("%s at %v, want %v", k, got[k], p)
		}
	}
	if d := res.Demo; !d.On || d.X != 11.1 || d.Y != 8.5 || d.Spacing != DemoSpacing {
		t.Errorf("demo spot %+v", d)
	}
	if len(res.Lights) != 2 || res.Lights[0] != (protocol.TableLight{Key: "A", Shows: "Zone A"}) || res.Lights[1].Shows != "Zone B" {
		t.Errorf("lights %+v", res.Lights)
	}
	// A drill on zone A lights A (default zones, no areas drawn).
	a.mu.Lock()
	l := a.lightFor("A")
	a.mu.Unlock()
	if l != "A" {
		t.Errorf("lightFor(A) = %q", l)
	}
	// Again: nothing moves (the row is built around the demo spot).
	if _, err := a.TableDemo(nil, nil); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	again := a.hwPos
	a.mu.Unlock()
	for k, p := range want {
		if again[k] != p {
			t.Errorf("second run moved %s to %v", k, again[k])
		}
	}
}

func TestTableDemoAssignsAreaLights(t *testing.T) {
	s := sign.New("A=http://127.0.0.1:1,B=http://127.0.0.1:1")
	a := New(Options{Detect: detect.DefaultConfig(), RecordingsDir: t.TempDir(), DataDir: t.TempDir(), Sign: s})
	sq := func(x float64) []protocol.Point { return []protocol.Point{{x, 1}, {x + 3, 1}, {x + 3, 4}, {x, 4}} }
	if _, err := a.SetAreas([]protocol.Area{
		{ID: "stage", Name: "Stage front", Poly: sq(1)},
		{ID: "bar", Name: "Bar", Poly: sq(10), Light: "B"},
		{ID: "gate", Name: "Gate", Poly: sq(15)},
	}); err != nil {
		t.Fatal(err)
	}
	x, y := 5.0, 5.0
	res, err := a.TableDemo(&x, &y)
	if err != nil {
		t.Fatal(err)
	}
	ar := a.Areas()
	if ar[0].Light != "A" || ar[1].Light != "B" || ar[2].Light != "" {
		t.Errorf("lights on areas: %q %q %q", ar[0].Light, ar[1].Light, ar[2].Light)
	}
	if res.Lights[0].Shows != "Stage front" || res.Lights[1].Shows != "Bar" {
		t.Errorf("lights %+v", res.Lights)
	}
	if !res.Demo.On || res.Demo.X != 5-1.5*DemoSpacing {
		t.Errorf("demo %+v", res.Demo)
	}
}

// POST /api/hardware/test: a Wi-Fi board shows red, says so, and gets its
// level back afterwards.
func TestBoardTestOverHTTP(t *testing.T) {
	var mu sync.Mutex
	level, calls := "calm", []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/level":
			level = r.URL.Query().Get("v")
			calls = append(calls, level)
			w.Write([]byte("ok\n"))
		case "/pulse":
			w.Write([]byte(`{"kind":"zone-light","name":"PULSE-A","zone":"A","level":"` + level + `","fw":"0000000 2026-10-04"}`))
		}
	}))
	defer srv.Close()
	a := New(Options{Detect: detect.DefaultConfig(), RecordingsDir: t.TempDir(), DataDir: t.TempDir(), Sign: sign.New("A=" + srv.URL)})
	res := a.TestBoards(context.Background())
	if len(res) != 1 || !res[0].Sent || !res[0].Confirmed || res[0].Link != "wifi" || res[0].Key != "A" || res[0].Name != "Zone light A" {
		t.Fatalf("result %+v", res)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(calls)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) < 2 || calls[0] != "red" || calls[len(calls)-1] != "calm" {
		t.Fatalf("level calls %v: want red, then calm restored", calls)
	}
}
