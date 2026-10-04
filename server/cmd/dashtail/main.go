// Command dashtail prints a one-line summary of dashboard snapshots and every
// alert: a terminal view for debugging without a browser.
//
//	go run ./server/cmd/dashtail -url ws://localhost:8080/ws/dash
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

func main() {
	url := flag.String("url", "ws://localhost:8080/ws/dash", "dashboard WebSocket")
	every := flag.Duration("every", time.Second, "how often to print a snapshot")
	flag.Parse()
	ctx := context.Background()
	ws, _, err := websocket.Dial(ctx, *url, nil)
	if err != nil {
		log.Fatal(err)
	}
	ws.SetReadLimit(1 << 20)
	var last time.Time
	for {
		_, b, err := ws.Read(ctx)
		if err != nil {
			log.Fatal(err)
		}
		typ, _ := protocol.PeekType(b)
		switch typ {
		case protocol.TypeSnapshot:
			if time.Since(last) < *every {
				continue
			}
			last = time.Now()
			var s protocol.Snapshot
			json.Unmarshal(b, &s)
			var nodes, zones []string
			for _, n := range s.Nodes {
				nodes = append(nodes, fmt.Sprintf("%s:%.2f", n.Status[:2], n.Sway))
			}
			for _, z := range s.Zones {
				zones = append(zones, fmt.Sprintf("%s=%s/%.2f", z.ID, z.Level, z.Score))
			}
			fmt.Printf("%s %s phones=%d msg/s=%.0f rtt=%d waves=%d zones[%s] nodes[%s]\n",
				time.UnixMilli(s.T).Format("15:04:05"), s.Mode, s.Stats.Phones, s.Stats.MsgPerSec, s.Stats.MedianRTT,
				len(s.Waves), strings.Join(zones, " "), strings.Join(nodes, " "))
		default:
			fmt.Println(string(b))
		}
	}
}
