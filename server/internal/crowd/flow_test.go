package crowd

import (
	"fmt"
	"math"
	"testing"

	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// flowRun is what a tracker said about a scene over time.
type flowRun struct {
	firstYellow, firstRed, firstEarly float64 // s; -1 = never
	yellowAfter                       float64 // s at yellow or worse after the warm-up
	motion                            string  // the densest cluster's motion at the end
}

// runScene steps a tracker every 250 ms for dur seconds over the phones
// scene(t) gives, with the flow rule on or off.
func runScene(t *testing.T, scene func(t float64) []Point, dur, warm float64, flow bool) flowRun {
	t.Helper()
	cfg := ConfigFrom(detect.DefaultConfig())
	if !flow {
		cfg.FlowMinOut = 0
	}
	tr := NewTracker(cfg)
	out := flowRun{firstYellow: -1, firstRed: -1, firstEarly: -1}
	const t0 = 1_700_000_000_000
	for ms := int64(0); ms <= int64(dur*1000); ms += 250 {
		sec := float64(ms) / 1000
		cs, ch := tr.Update(t0+ms, scene(sec))
		worst := protocol.LevelCalm
		var top Cluster
		for _, c := range cs {
			if PackedRank(c.Level) > PackedRank(worst) {
				worst = c.Level
			}
			if c.Est > top.Est {
				top = c
			}
		}
		if worst != protocol.LevelCalm && out.firstYellow < 0 {
			out.firstYellow = sec
		}
		if worst == protocol.LevelRed && out.firstRed < 0 {
			out.firstRed = sec
		}
		if worst != protocol.LevelCalm && sec >= warm {
			out.yellowAfter += 0.25
		}
		for _, c := range ch {
			if c.Early && out.firstEarly < 0 {
				out.firstEarly = sec
			}
		}
		out.motion = top.Motion()
	}
	return out
}

// aisle: people walking down a corridor in `lanes` lanes 0.6 m apart, one
// every `gap` m along each lane, at speed m/s (0 = standing), placed to
// ± acc m. The corridor is 30 m long; people re-enter at the top.
func aisle(lanes int, gap, speed, acc float64) func(float64) []Point {
	return func(t float64) []Point {
		var pts []Point
		const length = 30.0
		for l := 0; l < lanes; l++ {
			for k := 0; float64(k)*gap < length; k++ {
				y := math.Mod(float64(k)*gap+speed*t+0.13*float64(l), length)
				pts = append(pts, Point{ID: fmt.Sprintf("a%d-%d", l, k), X: 1 + 0.6*float64(l), Y: y, Acc: acc})
			}
		}
		return pts
	}
}

// stage: a crowd walking up to a barrier at y = 0 and stopping there,
// rows closing up from 1.2 m apart to `pack` m apart, 7 people per row
// 0.5 m apart; they start moving at t = 2 s at 0.5 m/s.
func stage(rows int, pack float64) func(float64) []Point {
	return func(t float64) []Point {
		var pts []Point
		for r := 0; r < rows; r++ {
			start, end := 0.4+1.2*float64(r), 0.4+pack*float64(r)
			y := start
			if t > 2 {
				y = math.Max(end, start-0.5*(t-2))
			}
			for c := 0; c < 7; c++ {
				pts = append(pts, Point{ID: fmt.Sprintf("s%d-%d", r, c), X: 2 + 0.5*float64(c), Y: y})
			}
		}
		return pts
	}
}

func TestFlowRule(t *testing.T) {
	cases := []struct {
		name  string
		scene func(float64) []Point
		dur   float64
		// want with the flow rule on
		wantMotion       string
		wantYellowAfter  bool // any watch after the 12 s warm-up
		sameAsOff        bool // every level and early warning exactly as with the rule off
		wantRed, wantOff bool // (sanity) red with the rule on / any watch with it off
	}{
		// A dense aisle moving at 0.3 m/s (about 2.8 people/m² estimated):
		// a queue. Watch only during the first seconds, before anyone has
		// had time to leave the spot.
		{name: "flowing aisle", scene: aisle(4, 0.6, 0.3, 0), dur: 60, wantMotion: MotionFlowing, wantOff: true},
		// The same aisle standing still: packed and barely moving, the old
		// rule (watch).
		{name: "standing aisle", scene: aisle(4, 0.6, 0, 0), dur: 60, wantMotion: MotionStill, wantYellowAfter: true, sameAsOff: true, wantOff: true},
		// The moving aisle placed to ± 2 m (GPS-like): flow unknown, old rule.
		{name: "rough positions", scene: aisle(4, 0.6, 0.3, 2), dur: 60, wantMotion: MotionUnknown, wantYellowAfter: true, sameAsOff: true, wantOff: true},
		// Packing against a barrier: nobody leaves; levels, early warning and
		// red exactly as before.
		{name: "stage packing", scene: stage(10, 0.45), dur: 40, wantYellowAfter: true, sameAsOff: true, wantRed: true, wantOff: true},
		// An aisle moving at 0.3 m/s but packed past the danger density: red
		// is never masked, flowing or not.
		{name: "flowing past danger", scene: aisle(5, 0.35, 0.3, 0), dur: 40, wantMotion: MotionFlowing, wantYellowAfter: true, sameAsOff: true, wantRed: true, wantOff: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			on := runScene(t, tc.scene, tc.dur, 12, true)
			off := runScene(t, tc.scene, tc.dur, 12, false)
			t.Logf("on %+v / off %+v", on, off)
			if tc.wantMotion != "" || tc.name == "rough positions" {
				if on.motion != tc.wantMotion {
					t.Errorf("motion %q, want %q", on.motion, tc.wantMotion)
				}
			}
			if (on.yellowAfter > 0) != tc.wantYellowAfter {
				t.Errorf("watch after warm-up for %.2f s, want any: %v", on.yellowAfter, tc.wantYellowAfter)
			}
			if (off.firstYellow >= 0) != tc.wantOff {
				t.Errorf("rule off: first watch %.2f, want any: %v", off.firstYellow, tc.wantOff)
			}
			if (on.firstRed >= 0) != tc.wantRed {
				t.Errorf("red at %.2f, want any: %v", on.firstRed, tc.wantRed)
			}
			if on.firstRed != off.firstRed {
				t.Errorf("red at %.2f with the rule, %.2f without: red must not move", on.firstRed, off.firstRed)
			}
			if tc.sameAsOff && (on.firstYellow != off.firstYellow || on.firstEarly != off.firstEarly || on.yellowAfter != off.yellowAfter) {
				t.Errorf("rule on %+v differs from off %+v", on, off)
			}
		})
	}
}

// A flowing aisle raises no early warning even while its density rises: the
// aisle fills from 2 lanes to 4 while moving.
func TestFlowNoEarlyWhileFilling(t *testing.T) {
	two, four := aisle(2, 0.6, 0.3, 0), aisle(4, 0.6, 0.3, 0)
	scene := func(t float64) []Point {
		if t < 20 {
			return two(t)
		}
		// lanes 3 and 4 fill in over 6 s, from the top of the corridor down
		var pts []Point
		for _, p := range four(t) {
			if p.X < 2 || p.Y > 30-5*(t-20) {
				pts = append(pts, p)
			}
		}
		return pts
	}
	on, off := runScene(t, scene, 50, 12, true), runScene(t, scene, 50, 12, false)
	t.Logf("on %+v / off %+v", on, off)
	if off.firstEarly < 0 {
		t.Fatalf("the scene raises no early warning even without the flow rule: test is moot")
	}
	if on.firstEarly >= 0 {
		t.Errorf("early warning at %.2f s in a moving aisle", on.firstEarly)
	}
}

// Someone walking along a dense aisle is not shown as pinned (yellow); the
// same person standing still in it is.
func TestPackedMoving(t *testing.T) {
	cfg := ConfigFrom(detect.DefaultConfig())
	for _, tc := range []struct {
		speed      float64
		wantMoving bool
		wantLevel  string
	}{{0.3, true, protocol.LevelCalm}, {0, false, protocol.LevelYellow}} {
		pt := NewPackedTracker()
		scene := aisle(4, 0.6, tc.speed, 0)
		var got map[string]Packed
		for ms := int64(0); ms <= 20_000; ms += 250 {
			got = pt.Update(1_700_000_000_000+ms, scene(float64(ms)/1000), cfg, nil)
		}
		// a phone in the middle of the aisle
		var mid Packed
		best := math.Inf(1)
		for _, p := range scene(20) {
			if d := math.Abs(p.Y-15) + math.Abs(p.X-1.6); d < best {
				best, mid = d, got[p.ID]
			}
		}
		if mid.Moving != tc.wantMoving || mid.Level != tc.wantLevel {
			t.Errorf("speed %.1f: %+v, want moving %v, level %s", tc.speed, mid, tc.wantMoving, tc.wantLevel)
		}
	}
}
