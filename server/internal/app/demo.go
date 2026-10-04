package app

import (
	"errors"
	"log"
	"math"
	"sort"

	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/hub"
	"github.com/Drivera0/crowd-crush/server/internal/names"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/store"
)

// The judge demo: things that make a handful of real phones easy to find
// and try on the big screen.
//
//   - Names: every live phone gets a generated name and colour from its
//     random session id (package names), unique among the phones known to
//     the server. Sent to the phone in its state and to the dashboard on
//     its node. Nothing is asked for.
//   - Shake: shaking a phone flags its node for a moment and is answered
//     with a "shake" message, so the person sees their dot ripple. A shaken
//     phone is being handled: its readings never count toward a wave.
//   - Demo spot: phones that join without a position are lined up at a spot
//     staff picked, DemoSpacing apart in join order (GET/PUT /api/demo).
//   - Staff can move a phone on the map (PUT /api/node/{id}/pos).
//   - Receipt: what the server holds about one session (GET /api/receipt/{id}).

// ---- names ----

// maxNames caps the id → name memory (a load test joins thousands).
const maxNames = 5000

// nameLocked is the phone's name: the one it had before, else a new one no
// other known live phone has. Caller holds mu.
func (a *App) nameLocked(id string) names.Name {
	if n, ok := a.names[id]; ok {
		return n
	}
	usedN, usedC := map[string]bool{}, map[string]bool{}
	for other, m := range a.live.meta {
		if other != id && m.name != "" {
			usedN[m.name], usedC[m.color] = true, true
		}
	}
	n := names.Pick(id, func(s string) bool { return usedN[s] }, func(s string) bool { return usedC[s] })
	if len(a.names) >= maxNames {
		a.names = map[string]names.Name{}
	}
	a.names[id] = n
	return n
}

// ---- shake ----

// A motion summary is "hard" when the phone is both accelerating and
// turning fast (a wrist shake), or accelerating very hard. Two hard
// summaries within shakeWindowMs are a shake, shown for shakeShowMs after
// the last one. A push in a crowd is a few m/s² with almost no rotation,
// and jumping barely turns the phone, so neither counts.
const (
	shakeAcc      = 5.0  // m/s², mean over the 100 ms summary
	shakeRot      = 120  // deg/s, with shakeAcc
	shakeHardAcc  = 14.0 // m/s² alone
	shakeWindowMs = 600
	shakeShowMs   = 1500
)

type shakeState struct {
	lastHard int64 // recv time of the last hard summary
	until    int64 // shaking until (recv clock)
	fresh    bool  // a shake just started; cleared by whoever tells the phone
}

func (s *shakeState) add(mo protocol.Motion, recv int64) {
	acc := math.Sqrt(mo.AX*mo.AX + mo.AY*mo.AY + mo.AZ*mo.AZ)
	if !(acc >= shakeHardAcc || (acc >= shakeAcc && mo.Rot >= shakeRot)) {
		return
	}
	if s.lastHard > 0 && recv-s.lastHard <= shakeWindowMs {
		if s.until < recv {
			s.fresh = true
		}
		s.until = recv + shakeShowMs
	}
	s.lastHard = recv
}

func (s *shakeState) active(t int64) bool { return s.until > t }

// detRot is the rotation rate the detector gets for a reading: the real
// one, except that a phone being shaken always reads as handled (above
// handlingRot), whatever its gyroscope said. Shaking never raises an alert.
func detRot(cfg detect.Config, m *nodeMeta, mo protocol.Motion, recv int64) float64 {
	if m.shake.active(recv) && mo.Rot <= cfg.HandlingRot {
		return cfg.HandlingRot + 1
	}
	return mo.Rot
}

// ---- demo spot ----

// DemoSpacing is the default distance between phones lined up at the demo
// spot: within the detector's neighbour radius, like people in a queue.
const DemoSpacing = 0.6

var errDemo = errors.New("want {on, x, y, spacing}: x, y in venue metres, spacing 0.3–2 m (0 = 0.6)")

// Demo is GET /api/demo.
func (a *App) Demo() protocol.DemoSpot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.demo
}

// SetDemo is PUT /api/demo. With arrange, every connected phone is lined up
// at the spot in join order now.
func (a *App) SetDemo(d protocol.DemoSpot, arrange bool) (protocol.DemoSpot, error) {
	if !finite(d.X) || !finite(d.Y) || !finite(d.Spacing) || (d.Spacing != 0 && (d.Spacing < 0.3 || d.Spacing > 2)) {
		return protocol.DemoSpot{}, errDemo
	}
	if d.Spacing == 0 {
		d.Spacing = DemoSpacing
	}
	now := hub.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	d.X, d.Y = a.liveConfig().Clamp(d.X, d.Y)
	d.X, d.Y = r2(d.X), r2(d.Y)
	a.demo = d
	if err := a.save("demo.json", d); err != nil {
		log.Printf("demo spot: %v", err)
	}
	if d.On && arrange {
		ids := make([]string, 0, len(a.live.meta))
		for id, m := range a.live.meta {
			if m.connected {
				ids = append(ids, id)
			}
		}
		sort.Slice(ids, func(i, j int) bool {
			mi, mj := a.live.meta[ids[i]], a.live.meta[ids[j]]
			if mi.joinedAt != mj.joinedAt {
				return mi.joinedAt < mj.joinedAt
			}
			return ids[i] < ids[j]
		})
		for i, id := range ids {
			x, y := a.demoSlotLocked(i)
			a.posIn(a.live, now, id, x, y)
			a.live.meta[id].pinned = true
		}
	}
	return d, nil
}

func (a *App) loadDemo() protocol.DemoSpot {
	var d protocol.DemoSpot
	if !a.load("demo.json", &d) || !finite(d.X) || !finite(d.Y) || d.Spacing < 0.3 || d.Spacing > 2 {
		return protocol.DemoSpot{X: r2(a.venue.W / 2), Y: r2(a.venue.H / 2), Spacing: DemoSpacing}
	}
	d.X, d.Y = a.liveConfig().Clamp(d.X, d.Y)
	return d
}

// demoSlotLocked is where the i-th phone stands: a row from the spot toward
// +x, wrapping into further rows at the venue's edge (toward +y, then
// above the spot).
func (a *App) demoSlotLocked(i int) (x, y float64) {
	cfg := a.liveConfig()
	d := a.demo
	sp := d.Spacing
	if sp <= 0 {
		sp = DemoSpacing
	}
	cols := max(1, int(math.Floor((cfg.VenueW-d.X)/sp+1e-9))+1)
	down := max(1, int(math.Floor((cfg.VenueH-d.Y)/sp+1e-9))+1) // rows that fit below the spot, its own included
	row := i / cols
	y = d.Y + float64(row)*sp
	if row >= down { // no room further down: carry on above the spot
		y = d.Y - float64(row-down+1)*sp
	}
	return cfg.Clamp(d.X+float64(i%cols)*sp, y)
}

// demoGrid is how many slots fit across (cols) and below the spot (down).
func (a *App) demoGrid() (sp float64, cols, down int) {
	cfg := a.liveConfig()
	d := a.demo
	sp = d.Spacing
	if sp <= 0 {
		sp = DemoSpacing
	}
	cols = max(1, int(math.Floor((cfg.VenueW-d.X)/sp+1e-9))+1)
	down = max(1, int(math.Floor((cfg.VenueH-d.Y)/sp+1e-9))+1)
	return sp, cols, down
}

// demoSlotOfLocked is the slot (0-based) whose place is (x, y), -1 if
// (x, y) isn't on one. The inverse of demoSlotLocked. Caller holds mu.
func (a *App) demoSlotOfLocked(x, y float64) int {
	sp, cols, down := a.demoGrid()
	col := int(math.Round((x - a.demo.X) / sp))
	r := int(math.Round((y - a.demo.Y) / sp))
	row := r
	if r < 0 {
		row = down - 1 - r
	}
	if col < 0 || col >= cols || row < 0 {
		return -1
	}
	i := row*cols + col
	sx, sy := a.demoSlotLocked(i)
	if math.Hypot(sx-x, sy-y) >= sp/2 {
		return -1
	}
	return i
}

// demoSlotsLocked is every live phone's place in the demo row (1-based),
// for phones the server lined up and that still stand on a slot, while the
// demo spot is on. Disconnected phones keep their place until forgotten
// (ForgetAfterMs), so a phone that reconnects finds it free. Caller holds mu.
func (a *App) demoSlotsLocked() map[string]int {
	if !a.demo.On {
		return nil
	}
	out := map[string]int{}
	for id, m := range a.live.meta {
		if !m.pinned || m.unplaced {
			continue
		}
		if i := a.demoSlotOfLocked(m.x, m.y); i >= 0 {
			out[id] = i + 1
		}
	}
	return out
}

// demoRowsLocked is the row message for every lined-up phone: its place,
// who stands before it, and whether it starts a new row. Caller holds mu.
func (a *App) demoRowsLocked() map[string]*protocol.DemoRow {
	slots := a.demoSlotsLocked()
	if len(slots) == 0 {
		return nil
	}
	at := make(map[int]string, len(slots))
	for id, n := range slots {
		at[n] = id
	}
	_, cols, _ := a.demoGrid()
	out := make(map[string]*protocol.DemoRow, len(slots))
	for id, n := range slots {
		r := &protocol.DemoRow{N: n, NewRow: n > 1 && (n-1)%cols == 0}
		if prev, ok := at[n-1]; ok && !r.NewRow {
			r.Prev = a.live.meta[prev].name
		}
		out[id] = r
	}
	return out
}

// demoFreeSlotLocked is the first slot nobody known to the server stands on.
func (a *App) demoFreeSlotLocked(skip string) (x, y float64) {
	sp := a.demo.Spacing
	if sp <= 0 {
		sp = DemoSpacing
	}
	for i := 0; ; i++ {
		x, y = a.demoSlotLocked(i)
		taken := false
		for id, m := range a.live.meta {
			if id != skip && !m.unplaced && math.Hypot(m.x-x, m.y-y) < sp/2 {
				taken = true
				break
			}
		}
		if !taken || i > 2000 {
			return x, y
		}
	}
}

// PhoneHelloAuto is a hello that carried no position. A phone the server
// already placed stays where it is (staff may have moved it). Otherwise,
// while the demo spot is on it takes the next free place in the row; with
// the demo spot off it stays unplaced until a position arrives (a GPS fix,
// a tap on the phone's map, staff dragging or lining it up): see
// unplaced.go.
func (a *App) PhoneHelloAuto(id, ua string) {
	now := hub.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	m := a.live.meta[id]
	switch {
	case m != nil && !m.unplaced:
		a.helloLiveLocked(now, id, m.x, m.y, ua)
	case a.demo.On:
		x, y := a.demoFreeSlotLocked(id)
		a.helloLiveLocked(now, id, x, y, ua)
		a.live.meta[id].pinned = true // exact: the estimator leaves it there (locate.go)
	default:
		a.helloUnplacedIn(a.live, now, id, ua)
		a.nameLiveLocked(id)
	}
}

// helloLiveLocked places a live phone and names it. Caller holds mu.
func (a *App) helloLiveLocked(now int64, id string, x, y float64, ua string) {
	a.helloIn(a.live, now, id, x, y, ua)
	a.nameLiveLocked(id)
}

// nameLiveLocked gives a live phone its name if it has none. Caller holds mu.
func (a *App) nameLiveLocked(id string) {
	if m := a.live.meta[id]; m != nil && m.name == "" {
		n := a.nameLocked(id)
		m.name, m.color = n.Name, n.Color
	}
}

// ---- staff move a phone on the map ----

// ErrNoPhone: no live phone with that id.
var ErrNoPhone = errors.New("no such phone")

// MovePhone is PUT /api/node/{id}/pos: staff dragged a real phone's dot.
// The phone hears about it in its next state (x, y).
func (a *App) MovePhone(id string, x, y float64) (protocol.NodeDetail, error) {
	if !finite(x) || !finite(y) {
		return protocol.NodeDetail{}, errors.New("want {x, y} in venue metres")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	m := a.live.meta[id]
	if m == nil {
		return protocol.NodeDetail{}, ErrNoPhone
	}
	a.posIn(a.live, hub.Now(), id, x, y)
	return nodeDetail(a.live, id, m), nil
}

// nodeDetail is GET /api/node/{id}'s answer. Caller holds mu.
func nodeDetail(p *pipeline, id string, m *nodeMeta) protocol.NodeDetail {
	zone := ""
	if !m.outside && !m.unplaced {
		zone = p.det.ZoneOf(m.x, m.y)
	}
	return protocol.NodeDetail{
		ID: id, X: r2(m.x), Y: r2(m.y), Acc: m.acc, Src: m.src(), Outside: m.outside, UA: m.ua, Zone: zone,
		Connected: m.connected, Synced: m.synced, RTT: m.rtt, Offset: m.offset,
		JoinedAt: m.joinedAt, Messages: m.msgs, Samples: m.samples(),
		Beacons: m.bcn.report(m, hub.Now()),
	}
}

// ---- privacy receipt ----

// Receipt is GET /api/receipt/{id}: what the server holds about one live
// phone session. The id is the phone's own random session id, which only
// that phone knows in full.
func (a *App) Receipt(id string) (protocol.Receipt, error) {
	now := hub.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	m := a.live.meta[id]
	if m == nil {
		return protocol.Receipt{}, ErrNoPhone
	}
	r := protocol.Receipt{ID: short(id), Name: m.name, Color: m.color, X: math.Round(m.x*10) / 10, Y: math.Round(m.y*10) / 10,
		Src: m.src(), Messages: m.msgs, Seconds: max(0, (now-m.joinedAt)/1000), Kept: min(len(m.tele), teleKeep),
		ForgetS: ForgetAfterMs / 1000, Recorded: m.recorded}
	if m.unplaced {
		r.Src, r.X, r.Y = "none", 0, 0
	}
	if _, off := a.opt.Sink.(store.Discard); !off {
		r.Stored, r.Store = true, "a file on the server"
		if a.opt.Tiger != nil {
			r.Store = "Tiger Data"
		}
	}
	return r, nil
}
