package app

import (
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"strings"

	"github.com/Drivera0/crowd-crush/server/internal/hub"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// Tower check-in: fixed things staff have placed on the venue map (this
// laptop, the sign, the zone-light boards) are position anchors for phones.
// A web page can't range against Bluetooth, so the anchor is a QR code per
// tower: the join link with ?at=<key>. A phone that joins through it is
// standing next to that tower, so:
//
//   - it is placed at the tower's map position, a little to one side
//     (0.5–1 m, a different side per phone, so phones don't stack), with
//     src "tower";
//   - if it also has GPS, the check-in calibrates it: the difference
//     between where the tower is and where the phone's (smoothed) GPS says
//     it is at check-in is that phone's GPS bias right now. It is added to
//     its later fixes and fades out (time constant towerBiasTauS), since
//     the bias drifts as the satellites and the phone move. Everything is
//     in venue metres; latitude and longitude are never kept.
//
// A tower nobody has placed on the map is no anchor: the phone is told so
// (GET /api/tower/{key} → 409) and joins the normal way.

const (
	towerLaptop = "laptop"
	// towerSpreadMin, towerSpreadMax: how far from the tower a phone is put (m).
	towerSpreadMin, towerSpreadMax = 0.5, 1.0
	// towerBiasTauS: the GPS correction decays with this time constant (s):
	// 37 % left after 2 minutes, 8 % after 5, dropped below 2 %.
	towerBiasTauS = 120.0
	// towerBiasGraceMs: a first GPS fix this long after the check-in still
	// counts as taken at the tower (the person hasn't walked off yet).
	towerBiasGraceMs = 30_000
	// towerBiasMax: a bigger offset than this (m) isn't a GPS bias; the
	// phone wasn't at the tower (someone opened a photo of the code).
	towerBiasMax = 60.0
)

// gpsBias is a phone's GPS correction from a tower check-in.
type gpsBias struct {
	cx, cy  float64 // where the phone was put at check-in
	at      int64   // when it checked in
	pending bool    // waiting for the first fix to measure the offset
	dx, dy  float64 // offset (m) measured at t0
	t0      int64
	on      bool
}

// apply corrects a smoothed fix (venue metres) at time now.
func (b *gpsBias) apply(now int64, x, y float64) (float64, float64) {
	if b.pending {
		b.pending = false
		if dx, dy := b.cx-x, b.cy-y; now-b.at <= towerBiasGraceMs && math.Hypot(dx, dy) <= towerBiasMax {
			b.dx, b.dy, b.t0, b.on = dx, dy, now, true
		}
	}
	if !b.on {
		return x, y
	}
	k := math.Exp(-float64(now-b.t0) / 1000 / towerBiasTauS)
	if k < 0.02 {
		*b = gpsBias{}
		return x, y
	}
	return x + b.dx*k, y + b.dy*k
}

// ErrTowerUnplaced: the tower exists but staff haven't put it on the map.
var ErrTowerUnplaced = errors.New("that spot isn't on the venue map yet")

// towerKey normalises a tower key: "laptop", "sign" or a zone-light letter
// from SIGN_URL.
func (a *App) towerKey(key string) (string, bool) {
	key = strings.TrimSpace(key)
	switch {
	case strings.EqualFold(key, towerLaptop):
		return towerLaptop, true
	case strings.EqualFold(key, "sign"):
		key = "sign"
	default:
		key = strings.ToUpper(key)
	}
	for _, k := range a.opt.Sign.Keys() {
		if k == key {
			return key, true
		}
	}
	return "", false
}

// towerName is what people call a tower.
func towerName(key string) string {
	switch key {
	case towerLaptop:
		return "This laptop"
	case "sign":
		return "Sign"
	}
	return "Zone light " + key
}

// Tower is GET /api/tower/{key}: where a tower is, for a phone about to
// check in there. ErrNoBoard for an unknown key, ErrTowerUnplaced (wrapped
// with the tower's name) for one that isn't on the map.
func (a *App) Tower(key string) (protocol.Tower, error) {
	k, ok := a.towerKey(key)
	if !ok {
		return protocol.Tower{}, ErrNoBoard
	}
	name := towerName(k)
	if k == towerLaptop {
		name = "the control laptop"
	}
	a.mu.Lock()
	p, placed := a.hwPos[k]
	a.mu.Unlock()
	if !placed {
		return protocol.Tower{}, fmt.Errorf("%w: %s can't place you until staff drag it onto the map (Hardware page)", ErrTowerUnplaced, name)
	}
	return protocol.Tower{Key: k, Name: name, X: p[0], Y: p[1]}, nil
}

// towerSpot is where phone id stands when it checks in at a tower at
// (tx, ty): towerSpreadMin–towerSpreadMax away, on a side picked from its
// id, kept inside the venue.
func (a *App) towerSpot(id, key string, tx, ty float64) (x, y float64) {
	h := fnv.New64a()
	h.Write([]byte(id + "@" + key))
	v := h.Sum64()
	ang := 2 * math.Pi * float64(v&0xffff) / 65536
	r := towerSpreadMin + (towerSpreadMax-towerSpreadMin)*float64((v>>16)&0xffff)/65535
	cfg := a.liveConfig()
	// Next to a wall or in a corner, turn until the spot is inside the venue.
	for i := 0; i < 16; i++ {
		x, y = tx+r*math.Cos(ang), ty+r*math.Sin(ang)
		if x >= 0 && y >= 0 && x <= cfg.VenueW && y <= cfg.VenueH {
			return x, y
		}
		ang += math.Pi / 8
	}
	return cfg.Clamp(x, y)
}

// PhoneHelloAt is a hello with at=<tower key>: the phone joined through a
// tower's QR code. It reports false when the tower is unknown or not on
// the map (the hub then places the phone the normal way). A phone that
// reconnects after checking in at the same tower keeps its position.
func (a *App) PhoneHelloAt(id, at, ua string) bool {
	key, ok := a.towerKey(at)
	if !ok {
		return false
	}
	now := hub.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	p, placed := a.hwPos[key]
	if !placed {
		return false
	}
	m := a.live.meta[id]
	if m != nil && !m.unplaced && m.tower == key {
		a.helloLiveLocked(now, id, m.x, m.y, ua)
		return true
	}
	x, y := a.towerSpot(id, key, p[0], p[1])
	bias := gpsBias{cx: x, cy: y, at: now, pending: true}
	if m != nil && !m.unplaced && m.acc > 0 {
		// Already tracked by GPS: its smoothed fix now, against the tower.
		bias.apply(now, m.gps.X, m.gps.Y)
	}
	a.helloLiveLocked(now, id, x, y, ua)
	m = a.live.meta[id]
	m.tower, m.bias = key, bias
	return true
}

// towersLocked adds the laptop to the hardware list and gives every entry
// its key. Caller holds mu.
func (a *App) towersLocked(list []protocol.Hardware, now int64) []protocol.Hardware {
	for i := range list {
		list[i].Key = hwKey(list[i])
	}
	lap := protocol.Hardware{Name: towerName(towerLaptop), Kind: towerLaptop, Key: towerLaptop, Online: true, LastSeen: now}
	if p, ok := a.hwPos[towerLaptop]; ok {
		x, y := p[0], p[1]
		lap.X, lap.Y = &x, &y
	}
	return append([]protocol.Hardware{lap}, list...)
}
