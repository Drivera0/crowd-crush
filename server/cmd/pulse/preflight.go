package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// runPreflight is the go/no-go list for the table demo (./bin/pulse
// -preflight, scripts/preflight.sh). It talks to the server that is already
// running on addr, so it checks what judges will meet: the public URL, the
// boards as that server drives them, the demo spot. Every line says what
// was actually verified; nothing is assumed.
//
// ✗ = no-go (fix it before judges arrive), ! = works but degraded, ✓ = verified.
func runPreflight(addr, publicURL string) int {
	base := "http://localhost:" + portOfAddr(addr)
	c := &http.Client{Timeout: 5 * time.Second}
	nogo, warn := 0, 0
	line := func(mark, name, msg, fix string) {
		switch mark {
		case "✗":
			nogo++
		case "!":
			warn++
		}
		fmt.Printf("  %s  %-14s %s\n", mark, name, msg)
		if fix != "" {
			fmt.Printf("     %-14s → %s\n", "", fix)
		}
	}
	fmt.Println("Table demo preflight")

	// 1. The server.
	localCfg, err := get(c, base+"/api/config")
	serverUp := err == nil
	if serverUp {
		line("✓", "Server", "answers on "+base, "")
	} else {
		line("✗", "Server", fmt.Sprintf("nothing answers on %s (%v)", base, err), "start it: ./bin/pulse (in another terminal), then run this again")
	}

	// 2. The public URL (what the QR code opens).
	switch u := strings.TrimRight(publicURL, "/"); {
	case u == "":
		line("✗", "Public URL", "PUBLIC_URL is not set: the QR code points at this laptop's own address, which phones can't open over HTTPS",
			"start the tunnel (cloudflared tunnel run pulse, or make tunnel) and set PUBLIC_URL in .env, then restart Pulse")
	default:
		body, err := get(c, u+"/api/config")
		switch {
		case err != nil:
			line("✗", "Public URL", fmt.Sprintf("%s doesn't answer (%v)", u, err), "is cloudflared running? Quick tunnel: the address changes each start, update PUBLIC_URL")
		case serverUp && !bytes.Equal(body, localCfg):
			line("!", "Public URL", u+" answers, but with a different config than this server: is the tunnel pointing at another Pulse?", "point the tunnel at "+base)
		default:
			line("✓", "Public URL", u+" reaches this server (fetched /api/config through it)", "")
		}
	}

	// 3. The boards, driven by the running server.
	if !serverUp {
		line("!", "Boards", "not tested: the server isn't running", "")
	} else if body, err := post(c, base+"/api/hardware/test"); err != nil {
		line("!", "Boards", fmt.Sprintf("couldn't test (%v)", err), "rebuild and restart Pulse (make build) so it has the board test")
	} else {
		var res []protocol.BoardTest
		if json.Unmarshal(body, &res) != nil {
			line("!", "Boards", "the server's answer made no sense", "")
		} else if len(res) == 0 {
			line("!", "Boards", "none configured (SIGN_URL is empty): alerts show on screen and are read aloud only",
				"plug the boards into this laptop, run scripts/boards.sh env --write, restart Pulse")
		}
		for _, r := range res {
			via := r.Link
			if r.Port != "" {
				via += " " + r.Port
			} else if r.Link == "wifi" {
				via += " " + r.URL
			}
			switch {
			case !r.Sent:
				fix := "plug it in with a data cable; Pulse finds it within a few seconds"
				if strings.Contains(r.Error, "busy") {
					fix = "close whatever holds the port (Arduino IDE Serial Monitor, a flash script)"
				} else if strings.HasPrefix(strings.ToLower(r.URL), "http") {
					fix = "on the same Wi-Fi as this laptop? On a table, plug it in by USB and use serial:auto (scripts/boards.sh env --write)"
				}
				line("✗", r.Name, "not driven: "+r.Error, fix)
			case !r.Confirmed:
				msg := fmt.Sprintf("took the test level over %s but didn't report it back", via)
				if r.Level != "" {
					msg = fmt.Sprintf("over %s: sent %s, it reports %s", via, protocol.LevelRed, r.Level)
				}
				line("!", r.Name, msg, "old firmware? scripts/boards.sh status, then flash if out of date")
			case r.FWOld:
				line("!", r.Name, fmt.Sprintf("over %s: showed red for 1 s and reported it back, but its firmware (%s) is not this checkout's", via, dashIfEmpty(r.FW)),
					"stop Pulse, scripts/boards.sh flash, start Pulse")
			default:
				line("✓", r.Name, fmt.Sprintf("over %s: showed red for 1 s and reported it back (%d ms)", via, r.Ms), "")
			}
		}
	}

	// 4. The demo spot (phones that join line up beside the boards).
	if serverUp {
		var d protocol.DemoSpot
		if body, err := get(c, base+"/api/demo"); err != nil || json.Unmarshal(body, &d) != nil {
			line("!", "Demo spot", "couldn't read it", "")
		} else if !d.On {
			line("!", "Demo spot", "off: phones that join must place themselves", "dashboard → Hardware → Set up table demo")
		} else {
			line("✓", "Demo spot", fmt.Sprintf("on at %.1f, %.1f m: phones that join line up there", d.X, d.Y), "")
		}
	}

	// 5. Keys (presence only: -check calls the services).
	for _, k := range []struct{ name, env, without string }{
		{"Gemini key", "GEMINI_API_KEY", "briefings use the template sentence"},
		{"ElevenLabs key", "ELEVENLABS_API_KEY", "the dashboard reads alerts with the browser's voice"},
	} {
		if os.Getenv(k.env) == "" {
			line("!", k.name, k.env+" is not set: "+k.without, "fine for the demo; set it in .env to use the service")
		} else {
			line("✓", k.name, "set (present only; ./bin/pulse -check calls the service)", "")
		}
	}

	// 6. Disk space (recordings, audio, Tiger fallback).
	if free, err := diskFree("."); err != nil {
		line("!", "Disk", fmt.Sprintf("couldn't read free space (%v)", err), "")
	} else {
		gb := float64(free) / (1 << 30)
		switch {
		case free < 200<<20:
			line("✗", "Disk", fmt.Sprintf("%.2f GB free: recordings and audio may fail", gb), "free some space")
		case free < 2<<30:
			line("!", "Disk", fmt.Sprintf("%.1f GB free", gb), "enough for a demo; free space before recording long runs")
		default:
			line("✓", "Disk", fmt.Sprintf("%.0f GB free", gb), "")
		}
	}

	fmt.Println()
	switch {
	case nogo > 0:
		fmt.Printf("NO-GO: %d thing(s) to fix (✗), %d warning(s). Fallback if time runs out: Simulation page (no phones, no boards needed).\n", nogo, warn)
		return 1
	case warn > 0:
		fmt.Printf("GO, with %d warning(s) (!): the demo works, those parts fall back.\n", warn)
	default:
		fmt.Println("GO: everything above was checked live.")
	}
	return 0
}

func portOfAddr(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 && i < len(addr)-1 {
		return addr[i+1:]
	}
	return "8080"
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "none reported"
	}
	return s
}

func get(c *http.Client, u string) ([]byte, error) {
	resp, err := c.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", resp.Status)
	}
	return b, nil
}

func post(c *http.Client, u string) ([]byte, error) {
	cl := *c
	cl.Timeout = 15 * time.Second // the board test holds each level for a second
	resp, err := cl.Post(u, "application/json", strings.NewReader("{}"))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", resp.Status)
	}
	return b, nil
}
