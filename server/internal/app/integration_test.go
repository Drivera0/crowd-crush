package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// TestEndToEnd connects phones with skewed clocks over real WebSockets and
// checks the dashboard sees them synced and streaming.
func TestEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	a := New(Options{Detect: detect.DefaultConfig(), RecordingsDir: t.TempDir()})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)
	mux := http.NewServeMux()
	a.Routes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")

	const n = 3
	offsets := []int64{5000, -3000, 120}
	for i := 0; i < n; i++ {
		ws, _, err := websocket.Dial(ctx, wsBase+"/ws/phone", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer ws.CloseNow()
		hello, _ := json.Marshal(protocol.Hello{Type: "hello", ID: fmt.Sprintf("p%d", i), Row: 0, Col: i, UA: "test"})
		ws.Write(ctx, websocket.MessageText, hello)
		off := offsets[i]
		go func() { // answer pings
			for {
				_, b, err := ws.Read(ctx)
				if err != nil {
					return
				}
				var p protocol.Ping
				if json.Unmarshal(b, &p) == nil && p.Type == "ping" {
					pong, _ := json.Marshal(protocol.Pong{Type: "pong", T0: p.T0, T1: time.Now().UnixMilli() + off})
					ws.Write(ctx, websocket.MessageText, pong)
				}
			}
		}()
		go func() { // stream calm motion
			tk := time.NewTicker(100 * time.Millisecond)
			defer tk.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tk.C:
					m, _ := json.Marshal(protocol.Motion{Type: "m", T: time.Now().UnixMilli() + off, AX: 0.01, Rot: 2})
					ws.Write(ctx, websocket.MessageText, m)
				}
			}
		}()
	}

	dash, _, err := websocket.Dial(ctx, wsBase+"/ws/dash", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dash.CloseNow()
	dash.SetReadLimit(1 << 20)
	deadline := time.Now().Add(5 * time.Second)
	var last protocol.Snapshot
	for time.Now().Before(deadline) {
		_, b, err := dash.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if typ, _ := protocol.PeekType(b); typ != protocol.TypeSnapshot {
			continue
		}
		json.Unmarshal(b, &last)
		ok := 0
		for _, nd := range last.Nodes {
			// Offsets are recovered within the (local) RTT.
			want := offsets[nd.Col]
			if nd.Status == protocol.StatusOK && nd.Offset > want-20 && nd.Offset < want+20 {
				ok++
			}
		}
		if ok == n && last.Stats.Phones == n {
			return
		}
	}
	t.Fatalf("phones never all ok and synced: %+v", last.Nodes)
}
