package app

import (
	"context"
	"sort"
	"time"

	"github.com/Drivera0/crowd-crush/server/internal/hub"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/sign"
)

// HardwareEvery is how often every sign and zone light is asked for its status.
const HardwareEvery = 5 * time.Second

// watchHardware probes the boards in SIGN_URL until ctx ends, so the
// dashboard can show which are online, their Wi-Fi signal and what their
// Bluetooth counters hear.
func (a *App) watchHardware(ctx context.Context) {
	if !a.opt.Sign.Enabled() {
		return
	}
	t := time.NewTicker(HardwareEvery)
	defer t.Stop()
	for {
		pctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		st := a.opt.Sign.Probe(pctx)
		cancel()
		a.mu.Lock()
		a.hw = hardwareList(st, a.hw, a.areas, hub.Now())
		a.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Hardware returns the latest board status (never nil).
func (a *App) Hardware() []protocol.Hardware {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]protocol.Hardware, len(a.hw))
	for i, h := range a.hw {
		// Area assignments can change between probes.
		h.Areas = lightAreas(a.areas, h.Zone)
		out[i] = h
	}
	return out
}

func hardwareList(st []sign.Status, prev []protocol.Hardware, areas []protocol.Area, now int64) []protocol.Hardware {
	last := map[string]int64{}
	for _, h := range prev {
		last[h.URL] = h.LastSeen
	}
	out := make([]protocol.Hardware, 0, len(st))
	for _, s := range st {
		h := protocol.Hardware{URL: s.URL, Zone: s.Zone, Online: s.Online, Error: s.Err, LastSeen: last[s.URL], Kind: "sign"}
		if s.Zone != "" {
			h.Kind = "zone-light"
		}
		if s.Online {
			h.LastSeen = now
		}
		if p := s.Pulse; p != nil {
			h.Kind, h.RSSI, h.Uptime, h.Level = p.Kind, p.RSSI, p.Uptime, p.Level
			if p.BLE != nil && p.BLE.Devices >= 0 {
				h.BLE = &protocol.BLEScan{Devices: p.BLE.Devices, Near: p.BLE.Near, Scans: p.BLE.Scans, Age: p.BLE.Age}
			}
		}
		h.Name = "Sign"
		if s.Zone != "" {
			h.Name = "Zone light " + s.Zone
		}
		h.Areas = lightAreas(areas, s.Zone)
		out = append(out, h)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Zone < out[j].Zone })
	return out
}

func lightAreas(areas []protocol.Area, zone string) []string {
	if zone == "" {
		return nil
	}
	var out []string
	for _, ar := range areas {
		if ar.Light == zone {
			out = append(out, ar.Name)
		}
	}
	return out
}
