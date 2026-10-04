package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/hub"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/sign"
)

var bcnModel = protocol.DefaultBeaconModel

// rssiAt is the signal a phone at (px, py) hears from a board at (bx, by)
// under the model, plus noise dB.
func rssiAt(px, py, bx, by, noise float64) float64 {
	d := math.Max(0.1, math.Hypot(px-bx, py-by))
	return bcnModel.TxPower1m - 10*bcnModel.PathLossN*math.Log10(d) + noise
}

type board struct {
	name string
	x, y float64
}

// anchorsAt is what a phone at (px, py) measures to each board, with
// Gaussian RSSI noise of sigma dB.
func anchorsAt(boards []board, px, py, sigma float64, rng *rand.Rand) []beaconAnchor {
	var an []beaconAnchor
	for _, b := range boards {
		noise := 0.0
		if sigma > 0 {
			noise = rng.NormFloat64() * sigma
		}
		d := bcnModel.Distance(rssiAt(px, py, b.x, b.y, noise))
		an = append(an, beaconAnchor{name: b.name, key: strings.TrimPrefix(b.name, "PULSE-"), x: b.x, y: b.y, d: d, sigma: beaconSigma(bcnModel, d)})
	}
	return an
}

func TestBeaconOneBoard(t *testing.T) {
	bs := []board{{"PULSE-A", 6, 4}}
	// Far: a ring, not a position.
	fix := solveBeacons(anchorsAt(bs, 10, 4, 0, nil), 24, 16)
	if fix.OK || fix.Near != "" || !strings.Contains(fix.Note, "ring") {
		t.Errorf("4 m from the only board: %+v, want no position", fix)
	}
	// Close: "near board A", at the board.
	fix = solveBeacons(anchorsAt(bs, 6.8, 4, 0, nil), 24, 16)
	if !fix.OK || fix.Dims != 0 || fix.Near != "PULSE-A" || fix.X != 6 || fix.Y != 4 || fix.Acc != beaconNearM {
		t.Errorf("0.8 m from the only board: %+v, want near PULSE-A at the board", fix)
	}
	// Nothing heard.
	if fix = solveBeacons(nil, 24, 16); fix.OK {
		t.Errorf("no board: %+v", fix)
	}
	// Two boards on the same spot are one board.
	same := []board{{"PULSE-A", 6, 4}, {"PULSE-B", 6.1, 4}}
	if fix = solveBeacons(anchorsAt(same, 12, 4, 0, nil), 24, 16); fix.OK {
		t.Errorf("two boards on one spot, phone 6 m away: %+v, want no position", fix)
	}
}

func TestBeaconTwoBoards(t *testing.T) {
	bs := []board{{"PULSE-A", 4, 8}, {"PULSE-B", 12, 8}}
	cases := []struct {
		name     string
		px, py   float64
		wantX    float64
		minCross float64
	}{
		{"between, on the line", 7, 8, 7, beaconCrossMin},
		{"midpoint", 8, 8, 8, beaconCrossMin},
		{"between, 3 m to one side", 7, 11, 7, 3},
		{"between, 3 m to the other side", 7, 5, 7, 3},
		{"beyond A", 2.5, 8, 2.5, beaconCrossMin},
		{"beyond B", 14, 8, 14, beaconCrossMin},
		{"beyond B and off the line", 15, 10, 15, 2},
	}
	for _, c := range cases {
		fix := solveBeacons(anchorsAt(bs, c.px, c.py, 0, nil), 24, 16)
		if !fix.OK || fix.Dims != 1 {
			t.Errorf("%s: %+v, want a 1-D fix", c.name, fix)
			continue
		}
		// Always on the line through the boards: the side is unknowable.
		if math.Abs(fix.X-c.wantX) > 0.05 || math.Abs(fix.Y-8) > 0.01 {
			t.Errorf("%s: at %.2f, %.2f; want %.2f, 8", c.name, fix.X, fix.Y, c.wantX)
		}
		if fix.Cross < c.minCross || fix.Cross <= fix.Along || fix.Acc < fix.Cross {
			t.Errorf("%s: along %.2f cross %.2f acc %.2f; want cross ≥ %.1f and the larger of the two", c.name, fix.Along, fix.Cross, fix.Acc, c.minCross)
		}
		if fix.Axis == nil || math.Abs(math.Abs(fix.Axis[0])-1) > 0.01 {
			t.Errorf("%s: axis %v, want along x", c.name, fix.Axis)
		}
	}
	// The mirror image gives the very same fix.
	up := solveBeacons(anchorsAt(bs, 7, 11, 0, nil), 24, 16)
	down := solveBeacons(anchorsAt(bs, 7, 5, 0, nil), 24, 16)
	if up.X != down.X || up.Y != down.Y || up.Cross != down.Cross {
		t.Errorf("mirror positions differ: %+v vs %+v", up, down)
	}
	// Ranges that don't meet (both read short): between the boards, nearer
	// the board with the shorter (more certain) range.
	an := []beaconAnchor{
		{name: "PULSE-A", x: 4, y: 8, d: 1, sigma: beaconSigma(bcnModel, 1)},
		{name: "PULSE-B", x: 12, y: 8, d: 3, sigma: beaconSigma(bcnModel, 3)},
	}
	fix := solveBeacons(an, 24, 16)
	if !fix.OK || fix.Dims != 1 || !(fix.X > 5 && fix.X < 7) || fix.Y != 8 || fix.Near != "PULSE-A" {
		t.Errorf("gap between the ranges: %+v, want x in 5…7 on the line, near A", fix)
	}
	// One range far too long: stays outside neither circle's reach of the near board.
	an[0].d, an[1].d = 1, 20
	an[1].sigma = beaconSigma(bcnModel, 20)
	if fix = solveBeacons(an, 24, 16); !fix.OK || math.Abs(fix.X-4) > 2.5 {
		t.Errorf("B far too long: %+v, want within 2.5 m of A", fix)
	}
	// Clamped to the venue.
	if fix = solveBeacons(anchorsAt(bs, -3, 8, 0, nil), 24, 16); fix.X != 0 {
		t.Errorf("outside the venue: x = %.2f, want clamped to 0", fix.X)
	}
	// With noise: the along-line coordinate holds up, on average.
	rng := rand.New(rand.NewSource(1))
	var sum float64
	const trials = 400
	for i := 0; i < trials; i++ {
		px, py := 4+8*rng.Float64(), 8+2*(rng.Float64()-0.5)
		fix := solveBeacons(anchorsAt(bs, px, py, 3, rng), 24, 16)
		if !fix.OK || fix.Dims != 1 {
			t.Fatalf("noisy fix %+v", fix)
		}
		sum += math.Abs(fix.X - px)
	}
	if mean := sum / trials; mean > 1.5 {
		t.Errorf("mean along-line error with 3 dB noise = %.2f m, want ≤ 1.5", mean)
	} else {
		t.Logf("two boards 8 m apart, 3 dB noise: mean along-line error %.2f m", mean)
	}
}

func TestBeaconThreeAndFourBoards(t *testing.T) {
	tri := []board{{"PULSE-A", 2, 2}, {"PULSE-B", 20, 3}, {"PULSE-C", 11, 14}}
	quad := []board{{"PULSE-A", 2, 2}, {"PULSE-B", 22, 2}, {"PULSE-C", 22, 14}, {"PULSE-D", 2, 14}}
	for _, bs := range [][]board{tri, quad} {
		// Exact ranges: the position comes back.
		for _, p := range [][2]float64{{12, 8}, {5, 5}, {18, 11}, {3, 12}, {20.5, 3.5}, {11, 1}} {
			fix := solveBeacons(anchorsAt(bs, p[0], p[1], 0, nil), 24, 16)
			if !fix.OK || fix.Dims != 2 || math.Hypot(fix.X-p[0], fix.Y-p[1]) > 0.05 {
				t.Errorf("%d boards, phone at %v: %+v", len(bs), p, fix)
			}
			if fix.Acc < beaconSigmaMin || fix.Acc > 8 || fix.Axis != nil || fix.Cross != 0 {
				t.Errorf("%d boards, phone at %v: acc %.2f axis %v cross %.2f", len(bs), p, fix.Acc, fix.Axis, fix.Cross)
			}
		}
		// Noisy ranges: metres, not centimetres, and the stated uncertainty is honest.
		rng := rand.New(rand.NewSource(7))
		var sum, sumAcc float64
		within := 0
		const trials = 500
		for i := 0; i < trials; i++ {
			px, py := 3+18*rng.Float64(), 3+10*rng.Float64()
			fix := solveBeacons(anchorsAt(bs, px, py, 3, rng), 24, 16)
			if !fix.OK || fix.Dims != 2 || math.IsNaN(fix.X) || math.IsNaN(fix.Y) || math.IsNaN(fix.Acc) {
				t.Fatalf("noisy fix %+v", fix)
			}
			e := math.Hypot(fix.X-px, fix.Y-py)
			sum += e
			sumAcc += fix.Acc
			if e <= 2*fix.Acc {
				within++
			}
		}
		mean := sum / trials
		t.Logf("%d boards, 3 dB noise: mean error %.2f m, mean stated acc %.2f m, %d%% within 2 × acc", len(bs), mean, sumAcc/trials, within*100/trials)
		if mean > 4 {
			t.Errorf("%d boards: mean error %.2f m with 3 dB noise, want ≤ 4", len(bs), mean)
		}
		if within*100/trials < 85 {
			t.Errorf("%d boards: only %d%% of fixes within 2 × their stated acc", len(bs), within*100/trials)
		}
	}
	// Three boards in a line are still only a 1-D fix.
	line := []board{{"PULSE-A", 4, 8}, {"PULSE-B", 12, 8}, {"PULSE-C", 20, 8.2}}
	for _, p := range [][2]float64{{9, 8}, {15, 11}, {15, 5}} {
		fix := solveBeacons(anchorsAt(line, p[0], p[1], 0, nil), 24, 16)
		if !fix.OK || fix.Dims != 1 || math.Abs(fix.X-p[0]) > 0.3 || math.Abs(fix.Y-8.1) > 0.2 || fix.Cross < math.Abs(p[1]-8.1) {
			t.Errorf("three boards in a line, phone at %v: %+v", p, fix)
		}
	}
	if d := beaconDims(anchorsAt(line, 0, 0, 0, nil)); d != 1 {
		t.Errorf("line layout dims = %d", d)
	}
	if d := beaconDims(anchorsAt(tri, 0, 0, 0, nil)); d != 2 {
		t.Errorf("triangle layout dims = %d", d)
	}
}

// beaconApp is an app with zone lights A, B and C in SIGN_URL (nothing
// listens there) and A, B placed 8 m apart.
func beaconApp(t *testing.T) *App {
	t.Helper()
	a := New(Options{Detect: detect.DefaultConfig(), RecordingsDir: t.TempDir(), DataDir: t.TempDir(),
		Sign: sign.New("A=http://127.0.0.1:9,B=http://127.0.0.1:9,C=http://127.0.0.1:9")})
	for k, p := range map[string][2]float64{"A": {4, 8}, "B": {12, 8}} {
		if _, err := a.SetHardwarePos(k, p[0], p[1]); err != nil {
			t.Fatal(err)
		}
	}
	return a
}

func seenAt(px, py float64, boards ...board) []protocol.BeaconSeen {
	var out []protocol.BeaconSeen
	for _, b := range boards {
		out = append(out, protocol.BeaconSeen{Name: b.name, RSSI: rssiAt(px, py, b.x, b.y, 0), N: 10})
	}
	return out
}

var (
	boardA = board{"PULSE-A", 4, 8}
	boardB = board{"PULSE-B", 12, 8}
	boardC = board{"PULSE-C", 8, 14}
)

func TestBeaconInfoAndUnknownNames(t *testing.T) {
	a := beaconApp(t)
	info := a.BeaconInfo()
	if len(info.Boards) != 3 || info.Placed != 2 || info.MaxDim != 1 || info.Model != protocol.DefaultBeaconModel || info.VenueW != 24 {
		t.Fatalf("info = %+v", info)
	}
	if b := info.Boards[0]; b.Name != "PULSE-A" || b.Key != "A" || b.X == nil || *b.X != 4 || *b.Y != 8 {
		t.Errorf("board A = %+v", b)
	}
	if b := info.Boards[2]; b.Name != "PULSE-C" || b.X != nil {
		t.Errorf("board C = %+v, want unplaced", b)
	}
	// A board of another venue, and one that is ours but not on the map:
	// the first is dropped, the second listed but not used.
	seen := append(seenAt(7, 8, boardA, boardB), protocol.BeaconSeen{Name: "PULSE-ZZ", RSSI: -40}, protocol.BeaconSeen{Name: "PULSE-C", RSSI: -70})
	fix, err := a.LocateBeacons(seen, "")
	if err != nil || !fix.OK || fix.Dims != 1 || math.Abs(fix.X-7) > 0.05 || fix.Y != 8 {
		t.Fatalf("fix = %+v, %v", fix, err)
	}
	if len(fix.Heard) != 3 {
		t.Fatalf("heard = %+v, want A, B and (unplaced) C", fix.Heard)
	}
	for _, h := range fix.Heard {
		if h.Name == "PULSE-ZZ" || (h.Name == "PULSE-C") == h.Placed || h.Dist <= 0 {
			t.Errorf("heard %+v", h)
		}
	}
	// Only unknown names: nothing.
	fix, err = a.LocateBeacons([]protocol.BeaconSeen{{Name: "PULSE-ZZ", RSSI: -40}}, "")
	if err != nil || fix.OK || len(fix.Heard) != 0 {
		t.Errorf("unknown board: %+v, %v", fix, err)
	}
	// Only an unplaced board: no position, and the note says why.
	fix, _ = a.LocateBeacons([]protocol.BeaconSeen{{Name: "PULSE-C", RSSI: -50}}, "")
	if fix.OK || !strings.Contains(fix.Note, "map") {
		t.Errorf("unplaced board: %+v", fix)
	}
	// Invalid reports are refused.
	if _, err := a.LocateBeacons([]protocol.BeaconSeen{{Name: "Someone's headphones", RSSI: -40}}, ""); err == nil {
		t.Error("a non-Pulse name was accepted")
	}
	// Placing the third board makes 2-D fixes possible.
	if _, err := a.SetHardwarePos("C", 8, 14); err != nil {
		t.Fatal(err)
	}
	if info = a.BeaconInfo(); info.Placed != 3 || info.MaxDim != 2 {
		t.Errorf("after placing C: %+v", info)
	}
	// The model can be recalibrated; nonsense is refused.
	if err := a.SetBeaconModel(protocol.BeaconModel{TxPower1m: -20, PathLossN: 9}); err == nil {
		t.Error("bad model accepted")
	}
	m := protocol.BeaconModel{TxPower1m: -70, PathLossN: 2.5}
	if err := a.SetBeaconModel(m); err != nil || a.BeaconInfo().Model != m {
		t.Errorf("SetBeaconModel: %v", err)
	}
	fix, _ = a.LocateBeacons([]protocol.BeaconSeen{{Name: "PULSE-A", RSSI: -70}}, "")
	if len(fix.Heard) != 1 || fix.Heard[0].Dist != 1 {
		t.Errorf("after recalibration −70 dBm should be 1 m: %+v", fix.Heard)
	}
}

// The sign is a beacon too (PULSE-S) whenever it is on the map, online or
// not, and each beacon can have its own 1 m reference.
func TestBeaconSignAndCalibration(t *testing.T) {
	dir := t.TempDir()
	opt := Options{Detect: detect.DefaultConfig(), RecordingsDir: t.TempDir(), DataDir: dir,
		Sign: sign.New("http://127.0.0.1:9,A=http://127.0.0.1:9,B=http://127.0.0.1:9")}
	a := New(opt)
	boardS := board{"PULSE-S", 8, 14}
	for k, p := range map[string][2]float64{"A": {4, 8}, "B": {12, 8}, "sign": {8, 14}} {
		if _, err := a.SetHardwarePos(k, p[0], p[1]); err != nil {
			t.Fatal(err)
		}
	}
	info := a.BeaconInfo()
	if len(info.Boards) != 3 || info.Placed != 3 || info.MaxDim != 2 {
		t.Fatalf("info = %+v", info)
	}
	s := info.Boards[0]
	if s.Name != "PULSE-S" || s.Key != "sign" || s.Online || s.X == nil || s.TxPower1m != -64 || s.Calibrated {
		t.Fatalf("sign board = %+v", s)
	}
	// Three anchors: a 2-D fix.
	fix, err := a.LocateBeacons(seenAt(9, 11, boardA, boardB, boardS), "")
	if err != nil || !fix.OK || fix.Dims != 2 || math.Hypot(fix.X-9, fix.Y-11) > 0.1 {
		t.Fatalf("fix with the sign = %+v, %v", fix, err)
	}
	// The sign's radio is 6 dB quieter: uncalibrated, it reads twice as far
	// and the fix is off; with its own 1 m reference it is right again.
	quiet := seenAt(9, 11, boardA, boardB, boardS)
	quiet[2].RSSI -= 6
	off, _ := a.LocateBeacons(quiet, "")
	if err := a.SetBeaconTx("PULSE-S", -70); err != nil {
		t.Fatal(err)
	}
	fix, _ = a.LocateBeacons(quiet, "")
	if math.Hypot(fix.X-9, fix.Y-11) > 0.1 || math.Hypot(off.X-9, off.Y-11) < 0.5 {
		t.Errorf("calibrated fix %.2f, %.2f; uncalibrated %.2f, %.2f; phone at 9, 11", fix.X, fix.Y, off.X, off.Y)
	}
	info = a.BeaconInfo()
	if s = info.Boards[0]; s.TxPower1m != -70 || !s.Calibrated || info.Boards[1].TxPower1m != -64 || info.Boards[1].Calibrated || info.Model.TxPower1m != -64 {
		t.Errorf("after calibrating the sign: %+v", info)
	}
	// Saved, and loaded by the next server.
	b := New(opt)
	if s = b.BeaconInfo().Boards[0]; s.TxPower1m != -70 || !s.Calibrated {
		t.Errorf("after a restart: %+v", s)
	}
	// Unknown beacon, nonsense value, and undoing it.
	if err := a.SetBeaconTx("PULSE-ZZ", -60); !errors.Is(err, ErrNoBeacon) {
		t.Errorf("unknown beacon: %v", err)
	}
	if err := a.SetBeaconTx("PULSE-S", -5); err == nil {
		t.Error("-5 dBm at 1 m accepted")
	}
	if err := a.SetBeaconTx("PULSE-S", 0); err != nil || a.BeaconInfo().Boards[0].Calibrated {
		t.Errorf("removing the sign's reference: %v", err)
	}
}

func TestPhoneBeacons(t *testing.T) {
	a := beaconApp(t)
	// A phone placed by hand keeps its place on a 1-D fix; the fix is there
	// for whoever wants it.
	a.PhoneHello("manual", 20, 3, "Android")
	a.PhoneBeacons("manual", seenAt(7, 8, boardA, boardB))
	if m := towerMeta(t, a, "manual"); m.x != 20 || m.y != 3 || m.src() != protocol.SrcManual {
		t.Errorf("1-D fix moved a placed phone: %.2f, %.2f src %s", m.x, m.y, m.src())
	}
	x, y, acc, dims, ok := a.BeaconFix("manual")
	if !ok || dims != 1 || math.Abs(x-7) > 0.05 || y != 8 || acc < beaconCrossMin {
		t.Errorf("BeaconFix = %.2f, %.2f ± %.2f dims %d ok %v", x, y, acc, dims, ok)
	}
	if d, ok := a.BeaconFixDetail("manual"); !ok || d.Axis == nil || len(d.Heard) != 2 {
		t.Errorf("BeaconFixDetail = %+v, %v", d, ok)
	}
	// A phone with no position takes even the 1-D fix.
	a.PhoneHelloAuto("lost", "Android")
	if m := towerMeta(t, a, "lost"); !m.unplaced {
		t.Fatal("phone without a position isn't unplaced")
	}
	a.PhoneBeacons("lost", seenAt(9, 8, boardA, boardB))
	m := towerMeta(t, a, "lost")
	if m.unplaced || math.Abs(m.x-9) > 0.05 || m.y != 8 || m.src() != protocol.SrcBeacon {
		t.Errorf("unplaced phone after a 1-D fix: %+v src %s", [2]float64{m.x, m.y}, m.src())
	}
	// … and follows later fixes (smoothed).
	for i := 0; i < 8; i++ {
		a.PhoneBeacons("lost", seenAt(6, 8, boardA, boardB))
	}
	if m = towerMeta(t, a, "lost"); math.Abs(m.x-6) > 0.1 || m.src() != protocol.SrcBeacon {
		t.Errorf("after walking to x = 6: %.2f src %s", m.x, m.src())
	}
	// One board, far away: a ring. An unplaced phone stays unplaced.
	a.PhoneHelloAuto("ring", "Android")
	a.PhoneBeacons("ring", seenAt(9, 8, boardA))
	if m = towerMeta(t, a, "ring"); !m.unplaced {
		t.Error("a distance ring placed a phone")
	}
	if _, _, _, _, ok := a.BeaconFix("ring"); ok {
		t.Error("a distance ring is a fix")
	}
	// One board, right next to it: placed beside the board.
	a.PhoneBeacons("ring", seenAt(4.5, 8, boardA))
	m = towerMeta(t, a, "ring")
	if d := math.Hypot(m.x-4, m.y-8); m.unplaced || d < towerSpreadMin-0.01 || d > towerSpreadMax+0.01 || m.src() != protocol.SrcBeacon {
		t.Errorf("next to board A: %.2f, %.2f (%.2f m from it) src %s", m.x, m.y, d, m.src())
	}
	if _, _, _, dims, ok := a.BeaconFix("ring"); !ok || dims != 0 {
		t.Errorf("near fix: dims %d ok %v", dims, ok)
	}
	// Placing itself by hand afterwards wins, and stays.
	a.PhonePos("lost", 15, 15)
	a.PhoneBeacons("lost", seenAt(6, 8, boardA, boardB))
	if m = towerMeta(t, a, "lost"); m.x != 15 || m.y != 15 || m.src() != protocol.SrcManual {
		t.Errorf("after placing by hand: %.2f, %.2f src %s", m.x, m.y, m.src())
	}
	// An invalid report (or one from a phone the server doesn't know) is ignored.
	a.PhoneBeacons("lost", []protocol.BeaconSeen{{Name: "Kitchen TV", RSSI: -50}})
	a.PhoneBeacons("nobody", seenAt(6, 8, boardA, boardB))
	if d, ok := a.BeaconFixDetail("lost"); !ok || len(d.Heard) != 2 {
		t.Errorf("an invalid report replaced the fix: %+v", d)
	}
	// An empty report ("I hear nothing now") clears the fix.
	a.PhoneBeacons("lost", nil)
	if _, _, _, _, ok := a.BeaconFix("lost"); ok {
		t.Error("fix survived an empty report")
	}
	// A stale report is no fix.
	a.PhoneBeacons("manual", seenAt(7, 8, boardA, boardB))
	a.mu.Lock()
	a.live.meta["manual"].bcn.at -= beaconStaleMs + 1
	a.mu.Unlock()
	if _, _, _, _, ok := a.BeaconFix("manual"); ok {
		t.Error("stale fix returned")
	}
}

// With three boards on the map the fix is 2-D: it moves a phone placed by
// hand or by GPS, and GPS doesn't pull it back while the fix is fresh.
func TestPhoneBeacons2D(t *testing.T) {
	a := beaconApp(t)
	a.SetLocate(protocol.LocateConfig{}) // the position estimator off: this test pins the GPS path it replaces (locate_test.go covers that path with it on)
	if _, err := a.SetHardwarePos("C", boardC.x, boardC.y); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetVenue(protocol.Venue{W: 24, H: 16, Lat: 49.2781, Lon: -122.9199, Geo: true}); err != nil {
		t.Fatal(err)
	}
	a.PhoneHello("p", 20, 3, "Android")
	a.PhoneBeacons("p", seenAt(8, 10, boardA, boardB, boardC))
	m := towerMeta(t, a, "p")
	if math.Hypot(m.x-8, m.y-10) > 0.1 || m.src() != protocol.SrcBeacon {
		t.Fatalf("2-D fix: %.2f, %.2f src %s; want 8, 10 beacon", m.x, m.y, m.src())
	}
	x, y, acc, dims, ok := a.BeaconFix("p")
	if !ok || dims != 2 || math.Hypot(x-8, y-10) > 0.1 || acc <= 0 || acc > 5 {
		t.Errorf("BeaconFix = %.2f, %.2f ± %.2f dims %d ok %v", x, y, acc, dims, ok)
	}
	// A GPS fix somewhere else in the venue: ignored while the beacons hold.
	a.PhoneGPS("p", 49.2781-0.00003, -122.9199+0.00020, 8)
	if m = towerMeta(t, a, "p"); math.Hypot(m.x-8, m.y-10) > 0.1 || m.src() != protocol.SrcBeacon {
		t.Errorf("GPS moved a phone with a fresh 2-D beacon fix: %.2f, %.2f src %s", m.x, m.y, m.src())
	}
	// Beacons gone stale: GPS takes over again.
	a.mu.Lock()
	a.live.meta["p"].bcn.at -= beaconStaleMs + 1
	a.mu.Unlock()
	a.PhoneGPS("p", 49.2781-0.00003, -122.9199+0.00020, 8)
	if m = towerMeta(t, a, "p"); m.src() != protocol.SrcGPS || math.Hypot(m.x-8, m.y-10) < 1 {
		t.Errorf("after the beacons went stale: %.2f, %.2f src %s; want GPS", m.x, m.y, m.src())
	}
	// The node panel shows what the phone hears.
	a.PhoneBeacons("p", seenAt(8, 10, boardA, boardB, boardC))
	a.mu.Lock()
	d := nodeDetail(a.live, "p", a.live.meta["p"])
	a.mu.Unlock()
	if d.Beacons == nil || !d.Beacons.OK || !d.Beacons.Used || d.Beacons.Dims != 2 || len(d.Beacons.Heard) != 3 || d.Src != protocol.SrcBeacon {
		t.Errorf("node detail beacons = %+v src %s", d.Beacons, d.Src)
	}
	// A phone that never reported has none.
	a.PhoneHello("q", 1, 1, "iPhone")
	a.mu.Lock()
	d = nodeDetail(a.live, "q", a.live.meta["q"])
	a.mu.Unlock()
	if d.Beacons != nil || d.Src != protocol.SrcManual {
		t.Errorf("phone without beacons: %+v src %s", d.Beacons, d.Src)
	}
}

// The API and the wire: GET /api/beacons, POST /api/beacons/locate, and a
// "beacons" message on the phone socket.
func TestBeaconAPI(t *testing.T) {
	a := beaconApp(t)
	mux := http.NewServeMux()
	a.Routes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	res, err := http.Get(srv.URL + "/api/beacons")
	if err != nil {
		t.Fatal(err)
	}
	var info protocol.BeaconInfo
	json.NewDecoder(res.Body).Decode(&info)
	res.Body.Close()
	if res.StatusCode != 200 || len(info.Boards) != 3 || info.Model.TxPower1m != -64 || info.Model.PathLossN != 2.2 || info.Prefix != "PULSE-" {
		t.Fatalf("GET /api/beacons: %d %+v", res.StatusCode, info)
	}

	post := func(body string) (int, protocol.BeaconFix) {
		res, err := http.Post(srv.URL+"/api/beacons/locate", "application/json", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var f protocol.BeaconFix
		json.NewDecoder(res.Body).Decode(&f)
		return res.StatusCode, f
	}
	if code, f := post(`{"seen":[{"name":"PULSE-A","rssi":-64,"n":9}]}`); code != 200 || !f.OK || f.Near != "PULSE-A" || f.Heard[0].Dist != 1 {
		t.Errorf("locate at 1 m from A: %d %+v", code, f)
	}
	for _, bad := range []string{`{"seen":[{"name":"AirPods","rssi":-50}]}`, `{"seen":[{"name":"PULSE-A","rssi":5}]}`, `not json`} {
		if code, _ := post(bad); code != 400 {
			t.Errorf("locate %s: %d, want 400", bad, code)
		}
	}

	// Over the phone socket: an unplaced phone reports beacons and gets a place.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/phone", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	go func() { // drain pings
		for {
			if _, _, err := ws.Read(ctx); err != nil {
				return
			}
		}
	}()
	ws.Write(ctx, websocket.MessageText, []byte(`{"type":"hello","id":"droid","ua":"Android"}`))
	// A bad report first (dropped whole), then a good one.
	ws.Write(ctx, websocket.MessageText, []byte(`{"type":"beacons","seen":[{"name":"PULSE-A","rssi":-60},{"name":"Fitbit","rssi":-60}]}`))
	msg, _ := json.Marshal(protocol.Beacons{Type: protocol.TypeBeacons, Seen: seenAt(10, 8, boardA, boardB)})
	ws.Write(ctx, websocket.MessageText, msg)
	deadline := time.Now().Add(3 * time.Second)
	for {
		x, y, _, dims, ok := a.BeaconFix("droid")
		if ok {
			if dims != 1 || math.Abs(x-10) > 0.05 || y != 8 {
				t.Fatalf("fix over the socket: %.2f, %.2f dims %d", x, y, dims)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no beacon fix arrived over the phone socket")
		}
		time.Sleep(20 * time.Millisecond)
	}
	res, err = http.Get(srv.URL + "/api/node/droid")
	if err != nil {
		t.Fatal(err)
	}
	var d protocol.NodeDetail
	json.NewDecoder(res.Body).Decode(&d)
	res.Body.Close()
	if d.Src != protocol.SrcBeacon || d.Beacons == nil || len(d.Beacons.Heard) != 2 || math.Abs(d.X-10) > 0.05 {
		t.Errorf("GET /api/node: src %s x %.2f beacons %+v", d.Src, d.X, d.Beacons)
	}
	_ = hub.Now
}
