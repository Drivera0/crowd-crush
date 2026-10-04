package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"

	"github.com/Drivera0/crowd-crush/server/internal/hub"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/store"
)

// Bluetooth beacon positioning.
//
// The zone-light boards advertise as PULSE-<letter> and staff place them on
// the venue map. A phone that can scan for Bluetooth adverts (Android Chrome
// with the experimental web platform features flag; no iPhone browser can)
// reports once a second how strongly it hears each board. Here that becomes
// a distance per board (the firmware's log-distance model) and, as far as
// the geometry honestly allows, a position in venue metres:
//
//   - one board: a distance ring, which is no position. Only when the phone
//     is right next to the board (< beaconNearM) is it "near board X";
//   - two boards (or more, all in a line): the position along the line
//     through them. Which side of the line the phone is on cannot be known,
//     so the fix is flagged 1-D with a large cross-line uncertainty;
//   - three or more boards not in a line: a 2-D weighted least-squares fix.
//
// It is opt-in and nothing depends on it: a phone that never sends a
// "beacons" message is handled exactly as before. Only signal strengths of
// Pulse boards are reported (the phone filters on the name prefix) and
// names that aren't one of this venue's boards are dropped on arrival.
//
// Until a position estimator fuses this with GPS and the other sources, a
// fix is used as the phone's position (src "beacon") when it is 2-D, or
// when the phone has no position from anywhere else. BeaconFix is the
// accessor an estimator should read instead.
//
// The same ranges also come from the other direction, the boards measuring
// the phone (connect mode, and the Android app's own advert): beaconlinks.go.

const (
	// beaconNearM: closer than this to a board, the phone is "near" it.
	beaconNearM = 1.5
	// beaconStaleMs: a report older than this is no fix (the phone stopped
	// scanning, or walked out of range).
	beaconStaleMs = 5000
	// beaconKeepMs: the node panel keeps showing the last report this long.
	beaconKeepMs = 30_000
	// beaconSigmaDB: RSSI noise left after the phone's smoothing (dB). In the
	// log-distance model a dB error is a relative distance error, so far
	// boards are trusted less than near ones.
	beaconSigmaDB = 4.0
	// beaconSigmaMin: no range is trusted better than this (m).
	beaconSigmaMin = 0.5
	// beaconCrossMin: the least cross-line uncertainty of a 1-D fix (m).
	beaconCrossMin = 2.0
	// beaconLineM: boards whose spread across their main axis is under this
	// (or under a tenth of their spread along it) are "in a line".
	beaconLineM = 0.5
	// beaconSmooth: weight of a new fix against the position it replaces.
	beaconSmooth = 0.5

	beaconsFile = "beacons.json"
)

// beaconAnchor is a placed board with the phone's estimated distance to it.
type beaconAnchor struct {
	name, key string
	x, y      float64
	d         float64 // estimated distance (m)
	sigma     float64 // its uncertainty (m)
}

// beaconSigma is the uncertainty of a range d under model m.
func beaconSigma(m protocol.BeaconModel, d float64) float64 {
	return math.Max(beaconSigmaMin, d*math.Ln10/(10*m.PathLossN)*beaconSigmaDB)
}

// beaconAxis is the main axis of a set of points: their centroid, the unit
// vector along the axis and the RMS spread along and across it.
func beaconAxis(an []beaconAnchor) (cx, cy, ux, uy, along, across float64) {
	n := float64(len(an))
	for _, a := range an {
		cx += a.x / n
		cy += a.y / n
	}
	var sxx, syy, sxy float64
	for _, a := range an {
		sxx += (a.x - cx) * (a.x - cx) / n
		syy += (a.y - cy) * (a.y - cy) / n
		sxy += (a.x - cx) * (a.y - cy) / n
	}
	th := 0.5 * math.Atan2(2*sxy, sxx-syy)
	ux, uy = math.Cos(th), math.Sin(th)
	root := math.Sqrt((sxx-syy)*(sxx-syy) + 4*sxy*sxy)
	along = math.Sqrt(math.Max(0, (sxx+syy+root)/2))
	across = math.Sqrt(math.Max(0, (sxx+syy-root)/2))
	return
}

// beaconDims is the best fix a set of placed boards can give: 0 (one spot),
// 1 (a line) or 2.
func beaconDims(an []beaconAnchor) int {
	if len(an) < 2 {
		return 0
	}
	_, _, _, _, along, across := beaconAxis(an)
	switch {
	case along < beaconLineM/2:
		return 0 // all on the same spot
	case across < math.Max(beaconLineM, along/10):
		return 1
	}
	return 2
}

// solveBeacons turns ranges to placed boards into a fix in a w × h venue.
func solveBeacons(an []beaconAnchor, w, h float64) protocol.BeaconFix {
	var fix protocol.BeaconFix
	if len(an) == 0 {
		fix.Note = "No Pulse board that is on the map was heard."
		return fix
	}
	an = append([]beaconAnchor(nil), an...)
	sort.SliceStable(an, func(i, j int) bool { return an[i].d < an[j].d })
	near := an[0]
	if near.d < beaconNearM {
		fix.Near = near.name
	}
	switch beaconDims(an) {
	case 0:
		if near.d >= beaconNearM {
			fix.Note = fmt.Sprintf("Only %s heard, about %.1f m away: that is a ring around the board, not a position.", near.name, near.d)
			return fix
		}
		fix.OK, fix.X, fix.Y = true, near.x, near.y
		fix.Acc = beaconNearM
		fix.Note = fmt.Sprintf("Right next to %s (about %.1f m).", near.name, near.d)
	case 1:
		solveBeaconLine(an, &fix)
	default:
		solveBeacon2D(an, &fix)
	}
	if w > 0 && h > 0 {
		fix.X, fix.Y = math.Min(math.Max(fix.X, 0), w), math.Min(math.Max(fix.Y, 0), h)
	}
	fix.X, fix.Y, fix.Acc = round2(fix.X), round2(fix.Y), round2(fix.Acc)
	fix.Along, fix.Cross = round2(fix.Along), round2(fix.Cross)
	return fix
}

// solveBeaconLine is the 1-D fix: the boards lie on a line, so only the
// coordinate s along it (and how far off the line the phone is, but not on
// which side) can be found.
func solveBeaconLine(an []beaconAnchor, fix *protocol.BeaconFix) {
	cx, cy, ux, uy, _, _ := beaconAxis(an)
	t := make([]float64, len(an)) // each board's coordinate along the line
	var wsum float64
	for i, a := range an {
		t[i] = (a.x-cx)*ux + (a.y-cy)*uy
		wsum += 1 / (a.sigma * a.sigma)
	}
	var s, off float64 // along the line; off it
	if len(an) == 2 {
		s, off = lineTwo(t[0], an[0].d, an[0].sigma, t[1], an[1].d, an[1].sigma)
	} else {
		// d² = (s − t)² + off²  ⇒  d² − t² = −2t·s + q with q = s² + off²:
		// linear in (s, q). Weighted by the variance of d².
		var a11, a12, a22, b1, b2 float64
		for i, a := range an {
			wt := 1 / math.Pow(2*math.Max(a.d, beaconSigmaMin)*a.sigma, 2)
			r, rhs := -2*t[i], a.d*a.d-t[i]*t[i]
			a11 += wt * r * r
			a12 += wt * r
			a22 += wt
			b1 += wt * r * rhs
			b2 += wt * rhs
		}
		det := a11*a22 - a12*a12
		if math.Abs(det) < 1e-12 {
			s, off = lineTwo(t[0], an[0].d, an[0].sigma, t[1], an[1].d, an[1].sigma)
		} else {
			s = (b1*a22 - b2*a12) / det
			q := (a11*b2 - a12*b1) / det
			off = math.Sqrt(math.Max(0, q-s*s))
		}
	}
	fix.OK, fix.Dims = true, 1
	fix.X, fix.Y = cx+ux*s, cy+uy*s
	fix.Along = math.Max(beaconSigmaMin, math.Sqrt(1/wsum))
	fix.Cross = math.Max(beaconCrossMin, off+fix.Along)
	fix.Acc = math.Hypot(fix.Along, fix.Cross)
	fix.Axis = &protocol.Point{round2(ux), round2(uy)}
	fix.Note = fmt.Sprintf("%d boards in a line: this is the position along the line through them only. Which side of the line the phone is on cannot be known.", len(an))
}

// lineTwo places a phone on the line through two boards at coordinates ta
// and tb, given its ranges da, db (± sa, sb). When the two range circles
// intersect, s is where their chord crosses the line and off is half the
// chord (the phone is that far to one side or the other). When they don't
// (noise), the phone is put on the line where the two ranges, weighted by
// their certainty, agree best.
func lineTwo(ta, da, sa, tb, db, sb float64) (s, off float64) {
	if tb < ta {
		ta, da, sa, tb, db, sb = tb, db, sb, ta, da, sa
	}
	l := tb - ta
	wa, wb := 1/(sa*sa), 1/(sb*sb)
	switch {
	case da+db < l: // a gap between the circles: somewhere between the boards
		s = (wa*da + wb*(l-db)) / (wa + wb)
	case da-db > l: // beyond b
		s = (wa*da + wb*(l+db)) / (wa + wb)
	case db-da > l: // beyond a
		s = (wa*(-da) + wb*(l-db)) / (wa + wb)
	default:
		s = (da*da - db*db + l*l) / (2 * l)
		off = math.Sqrt(math.Max(0, da*da-s*s))
	}
	return ta + s, off
}

// solveBeacon2D is the 2-D fix: weighted non-linear least squares on the
// ranges (Gauss-Newton with Levenberg-Marquardt damping), started from the
// centroid of the boards weighted by 1/d².
func solveBeacon2D(an []beaconAnchor, fix *protocol.BeaconFix) {
	var x, y, ws float64
	for _, a := range an {
		wt := 1 / math.Max(a.d*a.d, 0.01)
		x, y, ws = x+wt*a.x, y+wt*a.y, ws+wt
	}
	x, y = x/ws, y/ws
	// normal builds JᵀWJ, JᵀWr and the cost at (px, py).
	normal := func(px, py float64) (a11, a12, a22, g1, g2, cost float64) {
		for _, a := range an {
			dx, dy := px-a.x, py-a.y
			r := math.Max(math.Hypot(dx, dy), 1e-6)
			jx, jy := dx/r, dy/r
			wt := 1 / (a.sigma * a.sigma)
			res := r - a.d
			a11 += wt * jx * jx
			a12 += wt * jx * jy
			a22 += wt * jy * jy
			g1 += wt * jx * res
			g2 += wt * jy * res
			cost += wt * res * res
		}
		return
	}
	lambda := 1e-3
	a11, a12, a22, g1, g2, cost := normal(x, y)
	for it := 0; it < 60; it++ {
		d11, d22 := a11*(1+lambda)+1e-9, a22*(1+lambda)+1e-9
		det := d11*d22 - a12*a12
		if math.Abs(det) < 1e-15 {
			break
		}
		sx, sy := -(g1*d22-g2*a12)/det, -(d11*g2-a12*g1)/det
		n11, n12, n22, ng1, ng2, ncost := normal(x+sx, y+sy)
		if ncost > cost {
			lambda *= 10
			if lambda > 1e8 {
				break
			}
			continue
		}
		x, y = x+sx, y+sy
		a11, a12, a22, g1, g2, cost = n11, n12, n22, ng1, ng2, ncost
		lambda = math.Max(lambda*0.3, 1e-9)
		if math.Hypot(sx, sy) < 1e-4 {
			break
		}
	}
	// Uncertainty: the covariance of the solution, inflated when the ranges
	// disagree with each other more than their own uncertainty explains.
	acc := 0.0
	if det := a11*a22 - a12*a12; det > 1e-12 {
		acc = math.Sqrt((a11 + a22) / det)
	} else {
		acc = an[len(an)-1].d
	}
	if chi := math.Sqrt(cost / float64(len(an)-2)); chi > 1 {
		acc *= chi
	}
	fix.OK, fix.Dims = true, 2
	fix.X, fix.Y, fix.Acc = x, y, math.Max(beaconSigmaMin, acc)
	fix.Note = fmt.Sprintf("2-D fix from %d boards.", len(an))
}

// ---- per phone ----

// beaconPos is what a phone's latest beacon report said (nodeMeta.bcn).
type beaconPos struct {
	fix protocol.BeaconFix
	at  int64 // server ms of the latest fix attempt; 0 = never
	// The phone's own latest scan report and when it came.
	scan   []protocol.BeaconSeen
	scanAt int64
	// The position this phone was given from a beacon fix. It is where the
	// phone is (src "beacon") for as long as nothing else has moved it.
	x, y   float64
	placed bool
}

// owns reports whether m's position is the one a beacon fix gave it.
func (b *beaconPos) owns(m *nodeMeta) bool {
	return b.placed && !m.unplaced && m.x == b.x && m.y == b.y
}

// holds reports whether a fresh 2-D beacon fix is placing the phone, so a
// GPS fix (far less accurate indoors) should not move it.
func (b *beaconPos) holds(m *nodeMeta, now int64) bool {
	return b.owns(m) && b.fix.OK && b.fix.Dims == 2 && now-b.at <= beaconStaleMs
}

// report is the node panel's view of the latest report; nil when the phone
// never sent one or it is long gone.
func (b *beaconPos) report(m *nodeMeta, now int64) *protocol.BeaconFix {
	if b.at == 0 || now-b.at > beaconKeepMs {
		return nil
	}
	f := b.fix
	f.AgeMs = max(1, now-b.at)
	f.Used = b.owns(m)
	if now-b.at > beaconStaleMs {
		f.OK = false
		f.Note = "Stale: the phone has stopped reporting beacons."
	}
	return &f
}

// ---- the venue's boards and the model ----

// beaconBoardsLocked lists the boards that advertise a beacon: every zone
// light in SIGN_URL (PULSE-<letter>, or the name the board itself reports),
// and the sign (PULSE-S). Only boards staff have put on the map count toward
// a fix. Caller holds mu.
func (a *App) beaconBoardsLocked() []protocol.BeaconBoard {
	reported := map[string]protocol.Hardware{}
	for _, h := range a.hw {
		reported[hwKey(h)] = h
	}
	cfg := a.beaconConfigLocked()
	var out []protocol.BeaconBoard
	for _, key := range a.opt.Sign.Keys() {
		h := reported[key]
		name := h.Beacon
		if name == "" {
			// A board advertises whether or not the server can reach it (the
			// beacon build of the sign has no Wi-Fi at all), so each key has a
			// default name: PULSE-S for the sign, PULSE-<letter> for a light.
			name = protocol.BeaconPrefix + key
			if key == "sign" {
				name = protocol.BeaconSign
			}
		}
		_, own := cfg.Boards[name]
		b := protocol.BeaconBoard{Name: name, Key: key, Label: towerName(key), Online: h.Online,
			TxPower1m: cfg.For(name).TxPower1m, Calibrated: own}
		if lb := a.bcn.boards[key]; lb != nil {
			b.Connectable, b.Links, b.Heard = lb.ok, lb.links, lb.heard
		}
		if p, ok := a.hwPos[key]; ok {
			x, y := p[0], p[1]
			b.X, b.Y = &x, &y
		}
		out = append(out, b)
	}
	return out
}

// beaconConfigLocked is the path-loss model and the per-beacon references:
// data/beacons.json, else the firmware's constants. Caller holds mu.
func (a *App) beaconConfigLocked() protocol.BeaconConfig {
	if a.bcn.cfg == nil {
		c := protocol.BeaconConfig{BeaconModel: protocol.DefaultBeaconModel}
		var saved protocol.BeaconConfig
		if a.load(beaconsFile, &saved) && saved.Valid() == nil {
			c = saved
		}
		a.bcn.cfg = &c
	}
	return *a.bcn.cfg
}

// SetBeaconModel changes the path-loss constants for every beacon without
// its own reference, and saves them in data/beacons.json.
func (a *App) SetBeaconModel(m protocol.BeaconModel) error {
	if err := m.Valid(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.beaconConfigLocked()
	c.BeaconModel = m
	return a.saveBeaconConfigLocked(c)
}

// SetBeaconConnTx sets the 1 m reference of connect mode: the RSSI (dBm) a
// board measures from a connected phone 1 m away. 0 = the model's.
func (a *App) SetBeaconConnTx(tx float64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.beaconConfigLocked()
	c.ConnTxPower1m = tx
	if err := c.Valid(); err != nil {
		return err
	}
	return a.saveBeaconConfigLocked(c)
}

// ErrNoBeacon: that name isn't one of this venue's boards.
var ErrNoBeacon = errors.New("no board with that beacon name at this venue")

// SetBeaconTx calibrates one beacon: tx is the RSSI (dBm) a phone reads
// 1 m from that board. tx = 0 removes its own reference again.
func (a *App) SetBeaconTx(name string, tx float64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	known := false
	for _, b := range a.beaconBoardsLocked() {
		known = known || b.Name == name
	}
	if !known {
		return ErrNoBeacon
	}
	c := a.beaconConfigLocked()
	boards := map[string]float64{}
	for k, v := range c.Boards {
		boards[k] = v
	}
	if tx == 0 {
		delete(boards, name)
	} else {
		boards[name] = tx
	}
	c.Boards = boards
	if err := c.Valid(); err != nil {
		return err
	}
	return a.saveBeaconConfigLocked(c)
}

func (a *App) saveBeaconConfigLocked(c protocol.BeaconConfig) error {
	if err := a.save(beaconsFile, c); err != nil {
		return err
	}
	a.bcn.cfg = &c
	return nil
}

// BeaconInfo is GET /api/beacons.
func (a *App) BeaconInfo() protocol.BeaconInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	cfg := a.liveConfig()
	bc := a.beaconConfigLocked()
	info := protocol.BeaconInfo{Boards: a.beaconBoardsLocked(), Model: bc.BeaconModel, ConnTxPower1m: bc.Conn().TxPower1m,
		Service: protocol.BeaconService, IDChar: protocol.BeaconIDChar,
		VenueW: cfg.VenueW, VenueH: cfg.VenueH, NearM: beaconNearM, StaleS: beaconStaleMs / 1000, Prefix: protocol.BeaconPrefix}
	if info.Boards == nil {
		info.Boards = []protocol.BeaconBoard{}
	}
	var an []beaconAnchor
	for _, b := range info.Boards {
		if b.X != nil {
			an = append(an, beaconAnchor{x: *b.X, y: *b.Y})
		}
	}
	info.Placed, info.MaxDim = len(an), beaconDims(an)
	return info
}

// beaconObs is one board as one phone "sees" it: heard by the phone's own
// scan, or measured by the board over a connection.
type beaconObs struct {
	name string
	rssi float64
	n    int
	src  string
}

// beaconObsLocked gathers what is known about one phone: its own scan
// report (the phone hears the boards) and the boards' readings of it
// (beaconlinks.go). Per board, the fresher of its connection and its hearing
// of the app's advert is taken. Caller holds mu.
func (a *App) beaconObsLocked(id string, scan []protocol.BeaconSeen, now int64) []beaconObs {
	var obs []beaconObs
	for _, s := range scan {
		obs = append(obs, beaconObs{name: s.Name, rssi: s.RSSI, n: s.N, src: protocol.BeaconSrcScan})
	}
	best := map[string]beaconObs{}
	bestAt := map[string]int64{}
	for k, l := range a.bcn.links[id] {
		if now-l.at > beaconStaleMs {
			continue
		}
		if at, ok := bestAt[k.beacon]; ok && (at > l.at || (at == l.at && k.src == protocol.BeaconSrcAdv)) {
			continue
		}
		best[k.beacon], bestAt[k.beacon] = beaconObs{name: k.beacon, rssi: l.rssi, src: k.src}, l.at
	}
	for _, name := range sortedKeys(best) {
		obs = append(obs, best[name])
	}
	return obs
}

// beaconFixLocked turns observations into a fix. Names that aren't one of
// this venue's boards are dropped. A board measured both ways (the phone
// hears it and it hears the phone) is one anchor: the two distances are
// averaged, weighted by their certainty. nearKey is the hardware key of the board
// in fix.Near. Caller holds mu.
func (a *App) beaconFixLocked(obs []beaconObs) (fix protocol.BeaconFix, nearKey string) {
	cfg := a.beaconConfigLocked()
	boards := map[string]protocol.BeaconBoard{}
	for _, b := range a.beaconBoardsLocked() {
		boards[b.Name] = b
	}
	var an []beaconAnchor
	anchorOf := map[string]int{}
	heard := []protocol.BeaconHeard{}
	for _, s := range obs {
		b, ok := boards[s.name]
		if !ok {
			continue
		}
		model := cfg.For(s.name)
		if s.src != protocol.BeaconSrcScan {
			model = cfg.Conn() // the board measured the phone
		}
		d := model.Distance(s.rssi)
		heard = append(heard, protocol.BeaconHeard{Name: s.name, RSSI: math.Round(s.rssi*10) / 10, N: s.n, Dist: round2(d), Placed: b.X != nil, Src: s.src})
		if b.X == nil {
			continue
		}
		sg := beaconSigma(model, d)
		if i, dup := anchorOf[s.name]; dup {
			p := &an[i]
			w1, w2 := 1/(p.sigma*p.sigma), 1/(sg*sg)
			p.d = (w1*p.d + w2*d) / (w1 + w2)
			p.sigma = math.Max(beaconSigmaMin, math.Sqrt(1/(w1+w2)))
			continue
		}
		anchorOf[s.name] = len(an)
		an = append(an, beaconAnchor{name: b.Name, key: b.Key, x: *b.X, y: *b.Y, d: d, sigma: sg})
	}
	venue := a.liveConfig()
	fix = solveBeacons(an, venue.VenueW, venue.VenueH)
	fix.Heard = heard
	if len(an) == 0 && len(heard) > 0 {
		fix.Note = "The boards heard aren't on the venue map yet: drag them onto it on the Hardware page."
	}
	for _, x := range an {
		if x.name == fix.Near {
			nearKey = x.key
		}
	}
	return fix, nearKey
}

// LocateBeacons is POST /api/beacons/locate: the fix for a scan report
// and/or the connections the boards report for id, without a phone session
// (the diagnostic page). Nothing is kept.
func (a *App) LocateBeacons(seen []protocol.BeaconSeen, id string) (protocol.BeaconFix, error) {
	if err := protocol.ValidBeacons(seen); err != nil {
		return protocol.BeaconFix{}, err
	}
	if id != "" && !protocol.ValidLinkID(id) {
		return protocol.BeaconFix{}, errors.New("id: letters, digits and dashes only, at most 36")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	fix, _ := a.beaconFixLocked(a.beaconObsLocked(id, seen, hub.Now()))
	return fix, nil
}

// PhoneBeacons (hub.BeaconHandler) takes a live phone's scan report.
func (a *App) PhoneBeacons(id string, seen []protocol.BeaconSeen) {
	if protocol.ValidBeacons(seen) != nil {
		return
	}
	now := hub.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	m := a.live.meta[id]
	if m == nil {
		return
	}
	m.bcn.scan, m.bcn.scanAt = append([]protocol.BeaconSeen(nil), seen...), now
	a.beaconUpdateLocked(id, m, now)
}

// beaconUpdateLocked recomputes a live phone's fix from its scan report (if
// fresh) and its board connections. The fix is kept for BeaconFix and the
// node panel, and becomes the phone's position when it is 2-D or the phone
// has no other position. Caller holds mu.
func (a *App) beaconUpdateLocked(id string, m *nodeMeta, now int64) {
	var scan []protocol.BeaconSeen
	if m.bcn.scanAt != 0 && now-m.bcn.scanAt <= beaconStaleMs {
		scan = m.bcn.scan
	}
	fix, nearKey := a.beaconFixLocked(a.beaconObsLocked(id, scan, now))
	mine := m.bcn.owns(m)
	m.bcn.fix, m.bcn.at = fix, now
	if !fix.OK || (fix.Dims < 2 && !m.unplaced && !mine) {
		return // no position, or a weaker one than the phone already has
	}
	x, y := fix.X, fix.Y
	if fix.Dims == 0 && nearKey != "" {
		// Next to a board: beside it, each phone on its own side (as a tower check-in).
		x, y = a.towerSpot(id, nearKey, x, y)
	}
	if mine {
		x, y = m.x+beaconSmooth*(x-m.x), m.y+beaconSmooth*(y-m.y)
	}
	m.x, m.y = a.liveConfig().Clamp(x, y)
	m.acc, m.outside = 0, false
	m.bcn.x, m.bcn.y, m.bcn.placed = m.x, m.y, true
	if a.placedLocked(a.live, now, id, m) {
		return // its first position: recorded as its hello
	}
	a.live.place(id, m)
	a.record(store.Record{K: store.KindPos, T: now, ID: id, X: store.F(r2(m.x)), Y: store.F(r2(m.y))})
}

// BeaconFix is a live phone's current beacon fix, for a position estimator
// to use as one more input: the position and its uncertainty (about 1 σ) in
// venue metres, and dims, which says what the fix constrains (2 = a 2-D
// position; 1 = only the coordinate along the line through the boards, with
// acc dominated by the unknown cross-line offset; 0 = "next to a board").
// ok is false when the phone doesn't report beacons, its last report is
// older than beaconStaleMs, or the report gave no position.
func (a *App) BeaconFix(phoneID string) (x, y, acc float64, dims int, ok bool) {
	now := hub.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	m := a.live.meta[phoneID]
	if m == nil || m.bcn.at == 0 || now-m.bcn.at > beaconStaleMs || !m.bcn.fix.OK {
		return 0, 0, 0, 0, false
	}
	f := m.bcn.fix
	return f.X, f.Y, f.Acc, f.Dims, true
}

// BeaconFixDetail is BeaconFix with everything the solver knows (the line's
// axis and the along/cross split of a 1-D fix, the boards heard).
func (a *App) BeaconFixDetail(phoneID string) (protocol.BeaconFix, bool) {
	now := hub.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	m := a.live.meta[phoneID]
	if m == nil || m.bcn.at == 0 || now-m.bcn.at > beaconStaleMs || !m.bcn.fix.OK {
		return protocol.BeaconFix{}, false
	}
	return m.bcn.fix, true
}

// beaconRoutes registers the beacon API.
func (a *App) beaconRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/beacons", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, a.BeaconInfo())
	})
	// {txPower1m, pathLossN} sets the model; {beacon, txPower1m} calibrates
	// one board and {conn: true, txPower1m} connect mode (txPower1m 0 = back
	// to the model's). Answers like GET.
	mux.HandleFunc("PUT /api/beacons/model", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			protocol.BeaconModel
			Beacon string `json:"beacon"`
			Conn   bool   `json:"conn"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req); err != nil {
			httpError(w, errors.New("want {txPower1m, pathLossN}, {beacon, txPower1m} or {conn: true, txPower1m}"), http.StatusBadRequest)
			return
		}
		var err error
		switch {
		case req.Conn:
			err = a.SetBeaconConnTx(req.TxPower1m)
		case req.Beacon != "":
			err = a.SetBeaconTx(req.Beacon, req.TxPower1m)
		default:
			err = a.SetBeaconModel(req.BeaconModel)
		}
		switch {
		case errors.Is(err, ErrNoBeacon):
			httpError(w, err, http.StatusNotFound)
		case err != nil:
			httpError(w, err, http.StatusBadRequest)
		default:
			writeJSON(w, a.BeaconInfo())
		}
	})
	mux.HandleFunc("POST /api/beacons/locate", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			protocol.Beacons
			ID string `json:"id"` // connect mode: the id this page wrote to the boards
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<13)).Decode(&req); err != nil {
			httpError(w, errors.New(`want {"seen": [{"name": "PULSE-A", "rssi": -61, "n": 14}], "id": "<id written to the boards, if any>"}`), http.StatusBadRequest)
			return
		}
		fix, err := a.LocateBeacons(req.Seen, req.ID)
		if err != nil {
			httpError(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, fix)
	})
}
