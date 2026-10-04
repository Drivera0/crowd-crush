package app

import (
	"context"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/Drivera0/crowd-crush/server/internal/hub"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/sign"
)

// HardwareEvery is how often every sign and zone light is asked for its status.
const HardwareEvery = 5 * time.Second

// HardwareGraceMs: a board only counts as offline after missing every check
// for this long. Single misses are normal for the Uno R4 sign, which handles
// one connection at a time and is sometimes slow to answer.
const HardwareGraceMs = 15_000

// hardwareTimeout bounds one round of status checks.
const hardwareTimeout = 2500 * time.Millisecond

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
		pctx, cancel := context.WithTimeout(ctx, hardwareTimeout)
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
	now := hub.Now()
	for i, h := range a.hw {
		// Area assignments can change between probes.
		h.Areas = lightAreas(a.areas, h.Zone)
		h.X, h.Y = nil, nil
		if p, ok := a.hwPos[hwKey(h)]; ok {
			x, y := p[0], p[1]
			h.X, h.Y = &x, &y
		}
		h.Peers = append([]protocol.Peer(nil), h.Peers...)
		// Age on the server's clock: the browser's clock may differ (WSL drifts).
		if h.LastSeen > 0 {
			h.SeenAgo = max(0, (now-h.LastSeen)/1000)
		}
		out[i] = h
	}
	mapDistances(out)
	return a.towersLocked(out, now)
}

func hardwareList(st []sign.Status, prev []protocol.Hardware, areas []protocol.Area, now int64) []protocol.Hardware {
	last := map[string]protocol.Hardware{}
	for _, h := range prev {
		last[h.URL] = h
	}
	out := make([]protocol.Hardware, 0, len(st))
	for _, s := range st {
		was := last[s.URL]
		h := protocol.Hardware{URL: s.URL, Zone: s.Zone, Online: s.Online, Error: s.Err, LastSeen: was.LastSeen, Kind: "sign"}
		if s.Zone != "" {
			h.Kind = "zone-light"
		}
		if s.Online {
			h.LastSeen = now
		} else if was.LastSeen > 0 && now-was.LastSeen < HardwareGraceMs {
			// A missed check or two: keep showing what it last said.
			h.Online, h.Error = true, ""
			h.Kind, h.RSSI, h.Uptime, h.Level, h.BLE = was.Kind, was.RSSI, was.Uptime, was.Level, was.BLE
			h.Beacon, h.Peers = was.Beacon, was.Peers
		}
		if p := s.Pulse; p != nil {
			h.Kind, h.RSSI, h.Uptime, h.Level = p.Kind, p.RSSI, p.Uptime, p.Level
			h.Beacon = p.Name
			h.Peers = nil
			for _, pe := range p.Peers {
				if pe.Name == "" || len(h.Peers) == maxPeers {
					continue
				}
				h.Peers = append(h.Peers, protocol.Peer{Name: pe.Name, RSSI: pe.RSSI, Dist: round2(pe.Dist), Age: pe.Age})
			}
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

// maxPeers bounds the peers kept per board.
const maxPeers = 32

const hardwareFile = "hardware.json"

// hwKey is a board's key in PUT /api/hardware/{key}/pos: "sign" for the
// worst-zone sign, else its light letter.
func hwKey(h protocol.Hardware) string {
	if h.Zone == "" {
		return "sign"
	}
	return h.Zone
}

// ErrNoBoard: no board with that key in SIGN_URL.
var ErrNoBoard = errors.New("no such board (want \"laptop\", \"sign\" or a zone-light letter from SIGN_URL)")

// SetHardwarePos places a board (or this laptop) on the venue map (clamped to the venue)
// and saves it in data/hardware.json.
func (a *App) SetHardwarePos(key string, x, y float64) ([]protocol.Hardware, error) {
	key, known := a.towerKey(key)
	if !known {
		return nil, ErrNoBoard
	}
	if !finite(x) || !finite(y) {
		return nil, errors.New("want {x, y} in venue metres")
	}
	a.mu.Lock()
	cx, cy := a.liveConfig().Clamp(x, y)
	pos := map[string]protocol.Point{}
	for k, v := range a.hwPos {
		pos[k] = v
	}
	pos[key] = protocol.Point{r2(cx), r2(cy)}
	err := a.save(hardwareFile, pos)
	if err == nil {
		a.hwPos = pos
	}
	a.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return a.Hardware(), nil
}

// loadHardwarePos reads data/hardware.json (never nil).
func (a *App) loadHardwarePos() map[string]protocol.Point {
	pos := map[string]protocol.Point{}
	if !a.load(hardwareFile, &pos) {
		return map[string]protocol.Point{}
	}
	for k, p := range pos {
		if !finite(p[0]) || !finite(p[1]) {
			delete(pos, k)
		}
	}
	return pos
}

// mapDistances adds the map distance to each peer that is also placed and
// hears this board back, so a Bluetooth estimate can be checked against
// where staff put the boards.
func mapDistances(hw []protocol.Hardware) {
	byBeacon := map[string]*protocol.Hardware{}
	for i := range hw {
		if hw[i].Beacon != "" {
			byBeacon[hw[i].Beacon] = &hw[i]
		}
	}
	hears := func(h *protocol.Hardware, name string) bool {
		for _, p := range h.Peers {
			if p.Name == name {
				return true
			}
		}
		return false
	}
	for i := range hw {
		b := &hw[i]
		for j := range b.Peers {
			p := &b.Peers[j]
			p.MapDist = nil
			o := byBeacon[p.Name]
			if b.X == nil || o == nil || o == b || o.X == nil || !hears(o, b.Beacon) {
				continue
			}
			d := round2(math.Hypot(*o.X-*b.X, *o.Y-*b.Y))
			p.MapDist = &d
		}
	}
}
