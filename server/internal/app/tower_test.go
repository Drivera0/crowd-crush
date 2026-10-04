package app

import (
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/Drivera0/crowd-crush/server/internal/geo"
	"github.com/Drivera0/crowd-crush/server/internal/hub"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

func towerMeta(t *testing.T, a *App, id string) nodeMeta {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	m := a.live.meta[id]
	if m == nil {
		t.Fatalf("no phone %s", id)
	}
	return *m
}

// TestTowerCheckIn: the laptop is a tower staff can place; a phone joining
// with at=laptop stands 0.5–1 m from it (each phone on its own side, never
// stacked), is shown with src "tower" and keeps its place on reconnect; an
// unknown key or an unplaced tower places nobody and says why.
func TestTowerCheckIn(t *testing.T) {
	a := demoApp(t)
	hw := a.Hardware()
	if len(hw) != 1 || hw[0].Key != "laptop" || !hw[0].Online || hw[0].X != nil {
		t.Fatalf("hardware = %+v, want the unplaced laptop", hw)
	}
	// Not on the map yet: no anchor.
	if _, err := a.Tower("laptop"); !errors.Is(err, ErrTowerUnplaced) {
		t.Errorf("unplaced tower: %v", err)
	}
	if a.PhoneHelloAt("early", "laptop", "iPhone") {
		t.Error("checked in at a tower that isn't on the map")
	}
	// Unknown keys (no SIGN_URL here, so no sign and no lights).
	for _, key := range []string{"nope", "sign", "A", ""} {
		if _, err := a.Tower(key); !errors.Is(err, ErrNoBoard) {
			t.Errorf("Tower(%q): %v", key, err)
		}
		if a.PhoneHelloAt("p", key, "iPhone") {
			t.Errorf("checked in at unknown tower %q", key)
		}
		if _, err := a.SetHardwarePos(key, 1, 1); !errors.Is(err, ErrNoBoard) {
			t.Errorf("SetHardwarePos(%q): %v", key, err)
		}
	}
	hw, err := a.SetHardwarePos("Laptop", 12, 8)
	if err != nil || hw[0].X == nil || *hw[0].X != 12 || *hw[0].Y != 8 {
		t.Fatalf("placing the laptop: %+v, %v", hw, err)
	}
	if tw, err := a.Tower("LAPTOP"); err != nil || tw.Key != "laptop" || tw.X != 12 || tw.Y != 8 || tw.Name == "" {
		t.Errorf("Tower = %+v, %v", tw, err)
	}
	type pt struct{ x, y float64 }
	var spots []pt
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("guest-%d", i)
		if !a.PhoneHelloAt(id, "laptop", "Android") {
			t.Fatal("not checked in")
		}
		m := towerMeta(t, a, id)
		d := math.Hypot(m.x-12, m.y-8)
		if d < towerSpreadMin-1e-9 || d > towerSpreadMax+1e-9 {
			t.Errorf("%s is %.2f m from the tower, want %.1f–%.1f", id, d, towerSpreadMin, towerSpreadMax)
		}
		if m.src() != protocol.SrcTower || m.unplaced || m.name == "" {
			t.Errorf("%s: src %s, unplaced %v, name %q", id, m.src(), m.unplaced, m.name)
		}
		for _, o := range spots {
			if math.Hypot(o.x-m.x, o.y-m.y) < 0.02 {
				t.Errorf("%s stacked on another phone at %.2f, %.2f", id, m.x, m.y)
			}
		}
		spots = append(spots, pt{m.x, m.y})
	}
	// They don't all stand on one side.
	var sx, sy float64
	for _, p := range spots {
		sx, sy = sx+p.x-12, sy+p.y-8
	}
	if math.Hypot(sx, sy)/float64(len(spots)) > 0.6 {
		t.Errorf("phones bunch on one side of the tower: mean offset %.2f, %.2f", sx/12, sy/12)
	}
	a.mu.Lock()
	snap := a.snapshotLocked(demoT0)
	a.mu.Unlock()
	for _, n := range snap.Nodes {
		if n.ID != "early" && n.ID != "p" && n.Src != protocol.SrcTower {
			t.Errorf("node %s src %q", n.ID, n.Src)
		}
	}
	// Reconnecting through the same link keeps the place; staff moving the
	// phone (or its own tap) ends "at the tower".
	before := towerMeta(t, a, "guest-0")
	a.PhoneGone("guest-0")
	a.PhoneHelloAt("guest-0", "laptop", "Android")
	if m := towerMeta(t, a, "guest-0"); m.x != before.x || m.y != before.y || m.src() != protocol.SrcTower {
		t.Errorf("reconnect moved the phone: %+v", m)
	}
	a.MovePhone("guest-0", 3, 3)
	if m := towerMeta(t, a, "guest-0"); m.src() != protocol.SrcManual || m.x != 3 {
		t.Errorf("after staff moved it: src %s at %.1f", m.src(), m.x)
	}
	// A tower in the corner still puts phones inside the venue.
	a.SetHardwarePos("laptop", 0, 0)
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("corner-%d", i)
		a.PhoneHelloAt(id, "laptop", "iPhone")
		m := towerMeta(t, a, id)
		if d := math.Hypot(m.x, m.y); m.x < 0 || m.y < 0 || d < towerSpreadMin-1e-9 || d > towerSpreadMax+1e-9 {
			t.Errorf("%s at %.2f, %.2f (%.2f m from the corner tower)", id, m.x, m.y, d)
		}
	}
}

// TestTowerGPSBias: a check-in calibrates the phone's GPS. Its fixes are
// off by a steady 9 m; right after checking in, the corrected position is
// where the phone really is (next to the tower), it follows the phone when
// it walks, and the correction fades over minutes. A phone that never
// checked in shows the raw error, and a fix that arrives long after the
// check-in calibrates nothing.
func TestTowerGPSBias(t *testing.T) {
	a := demoApp(t)
	anchor := geo.Anchor{Lat: 49.2781, Lon: -122.9199, Bearing: 30}
	if _, err := a.SetVenue(protocol.Venue{W: 24, H: 16, Lat: anchor.Lat, Lon: anchor.Lon, Bearing: anchor.Bearing, Geo: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetHardwarePos("laptop", 12, 8); err != nil {
		t.Fatal(err)
	}
	const bx, by = 7.0, -5.5 // this phone's GPS error (m): about 9 m
	// fix feeds a GPS fix for true position (x, y) at server time now.
	fix := func(id string, now int64, x, y float64) {
		lat, lon := anchor.ToLatLon(x+bx, y+by)
		a.mu.Lock()
		a.gpsLocked(now, id, lat, lon, 6)
		a.mu.Unlock()
	}
	now := hub.Now()
	if !a.PhoneHelloAt("cal", "laptop", "iPhone") {
		t.Fatal("not checked in")
	}
	a.PhoneHello("raw", 12, 8, "iPhone") // same spot, no check-in
	spot := towerMeta(t, a, "cal")
	tx, ty := spot.x, spot.y

	// Standing at the tower: corrected fixes sit on the check-in spot.
	for i := 0; i < 5; i++ {
		now += 1000
		fix("cal", now, tx, ty)
		fix("raw", now, 12, 8)
	}
	cal, raw := towerMeta(t, a, "cal"), towerMeta(t, a, "raw")
	if d := math.Hypot(cal.x-tx, cal.y-ty); d > 0.3 {
		t.Errorf("after check-in the corrected fix is %.2f m off (at %.2f, %.2f; want %.2f, %.2f)", d, cal.x, cal.y, tx, ty)
	}
	if cal.src() != protocol.SrcGPS || cal.acc <= 0 {
		t.Errorf("checked-in phone with GPS: src %s acc %.1f", cal.src(), cal.acc)
	}
	if d := math.Hypot(raw.x-12, raw.y-8); d < 8 {
		t.Errorf("the phone without a check-in is only %.2f m off: the test's GPS error isn't applied", d)
	}
	// It walks 6 m to the left over 12 s: the corrected position follows.
	for i := 1; i <= 12; i++ {
		now += 1000
		fix("cal", now, tx-0.5*float64(i), ty)
	}
	for i := 0; i < 8; i++ { // let the smoother settle
		now += 1000
		fix("cal", now, tx-6, ty)
	}
	cal = towerMeta(t, a, "cal")
	if d := math.Hypot(cal.x-(tx-6), cal.y-ty); d > 2.0 {
		t.Errorf("25 s after check-in the phone is shown %.2f m from where it is", d)
	}
	// The correction fades: after 2 minutes roughly e⁻¹ of it is left, and
	// after 10 minutes none (the position is the phone's own GPS again).
	elapsed := func() float64 {
		a.mu.Lock()
		defer a.mu.Unlock()
		return float64(now-a.live.meta["cal"].bias.t0) / 1000
	}
	for elapsed() < towerBiasTauS {
		now += 1000
		fix("cal", now, tx-6, ty)
	}
	cal = towerMeta(t, a, "cal")
	left := math.Hypot(cal.x-(tx-6), cal.y-ty) / math.Hypot(bx, by) // share of the raw error showing
	if want := 1 - math.Exp(-1); math.Abs(left-want) > 0.1 {
		t.Errorf("after %.0f s %.0f %% of the GPS error shows, want about %.0f %%", elapsed(), left*100, want*100)
	}
	for i := 0; i < 600; i++ {
		now += 1000
		fix("cal", now, tx-6, ty)
	}
	cal = towerMeta(t, a, "cal")
	if d := math.Hypot(cal.x-(tx-6+bx), cal.y-(ty+by)); d > 0.3 || cal.bias.on {
		t.Errorf("after 12 minutes the correction is still there: %.2f m from the raw GPS position, bias on %v", d, cal.bias.on)
	}

	// A first fix long after the check-in calibrates nothing.
	late := hub.Now()
	a.PhoneHelloAt("late", "laptop", "iPhone")
	lm := towerMeta(t, a, "late")
	late += towerBiasGraceMs + 5000
	fix("late", late, lm.x, lm.y)
	if m := towerMeta(t, a, "late"); m.bias.on || math.Hypot(m.x-(lm.x+bx), m.y-(lm.y+by)) > 0.3 {
		t.Errorf("a fix %d s after check-in was used as calibration: %+v", (towerBiasGraceMs+5000)/1000, m.bias)
	}
}
