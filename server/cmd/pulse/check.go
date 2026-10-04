package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Drivera0/crowd-crush/server/internal/brief"
	"github.com/Drivera0/crowd-crush/server/internal/sign"
	"github.com/Drivera0/crowd-crush/server/internal/store"
	"github.com/Drivera0/crowd-crush/server/internal/voice"
)

// runCheck tests every service configured in the environment (.env) and
// prints what works. Nothing here is required: it just says what falls back.
func runCheck(audioDir string) int {
	ctx := context.Background()
	failed := 0
	report := func(name, env string, err error, ok string) {
		switch {
		case errors.Is(err, errUnset):
			fmt.Printf("  –  %-11s %s not set (falls back, demo still works)\n", name, env)
		case err != nil:
			failed++
			fmt.Printf("  ✗  %-11s %v\n", name, err)
		default:
			fmt.Printf("  ✓  %-11s %s\n", name, ok)
		}
	}
	fmt.Println("Checking services from the environment / .env …")

	// Tiger Data
	if u := os.Getenv("TIGER_DATABASE_URL"); u == "" {
		report("Tiger Data", "TIGER_DATABASE_URL", errUnset, "")
	} else {
		t, err := store.OpenTiger(ctx, u, nil)
		if err == nil {
			t.Close()
		}
		report("Tiger Data", "", err, "connected, tables ready")
	}

	// Gemini
	b := brief.New(os.Getenv("GEMINI_API_KEY"), os.Getenv("GEMINI_MODEL"))
	if !b.Enabled() {
		report("Gemini", "GEMINI_API_KEY", errUnset, "")
	} else {
		bf, err := b.Brief(ctx, brief.Info{Zone: "B", Level: "red", Direction: "+x", LagMs: 250,
			SecondsHigh: 12, Scores: []float64{0.35, 0.5, 0.66}, Phones: 4, Swaying: 4})
		report("Gemini", "", err, fmt.Sprintf("%q", bf.Text()))
	}

	// ElevenLabs
	v := voice.New(os.Getenv("ELEVENLABS_API_KEY"), os.Getenv("ELEVENLABS_VOICE_ID"), audioDir)
	if !v.Enabled() {
		report("ElevenLabs", "ELEVENLABS_API_KEY", errUnset, "")
	} else {
		u, err := v.Speak(ctx, "Pulse check. Zone B, crowd waves building.")
		report("ElevenLabs", "", err, "wrote "+strings.TrimPrefix(u, "/audio/")+" in "+audioDir+"/")
	}

	// Signs
	s := sign.New(os.Getenv("SIGN_URL"))
	if !s.Enabled() {
		report("Sign", "SIGN_URL", errUnset, "")
	} else {
		// serial:auto asks each USB port who it is: give it time.
		wait := 3 * time.Second
		usb := strings.Contains(strings.ToLower(os.Getenv("SIGN_URL")), "serial:")
		if usb {
			wait = 15 * time.Second
		}
		cctx, cancel := context.WithTimeout(ctx, wait)
		err := s.Check(cctx)
		cancel()
		desc := s.Describe()
		s.Close()
		switch {
		case err != nil && usb:
			err = fmt.Errorf("%v (plugged in with a data cable? If Pulse is running it holds the ports: use ./bin/pulse -preflight instead)", err)
		case err != nil:
			err = fmt.Errorf("%v (same Wi-Fi as this laptop? venue Wi-Fi often blocks device-to-device: use a phone hotspot, or plug the boards in and use serial:auto)", err)
		}
		report("Sign", "", err, desc)
	}

	// Public URL (only answers while pulse and the tunnel are running)
	if u := strings.TrimRight(os.Getenv("PUBLIC_URL"), "/"); u == "" {
		report("Public URL", "PUBLIC_URL", errUnset, "")
	} else {
		c := &http.Client{Timeout: 5 * time.Second}
		resp, err := c.Get(u + "/api/config")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				err = fmt.Errorf("%s answered %s", u, resp.Status)
			}
		}
		if err != nil {
			err = fmt.Errorf("%v (fine if pulse + cloudflared aren't running yet)", err)
		}
		report("Public URL", "", err, u+" reaches this server")
	}

	if failed > 0 {
		fmt.Printf("%d check(s) failed. Fix the value in .env, or blank it to use the fallback.\n", failed)
		return 1
	}
	fmt.Println("All set.")
	return 0
}

var errUnset = errors.New("unset")
