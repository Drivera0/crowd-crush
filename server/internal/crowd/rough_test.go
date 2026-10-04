package crowd

import (
	"fmt"
	"math"
	"math/rand"
	"testing"

	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// TestStackOfRoughPhonesIsNotACrush: twenty phones that all report the same
// spot to ± 10 m (Wi-Fi positioning snapping to one access point, or fixes
// clamped onto a corner) are not twenty people on one square metre. The
// same dots placed by hand are.
func TestStackOfRoughPhonesIsNotACrush(t *testing.T) {
	cfg := ConfigFrom(detect.DefaultConfig())
	stack := func(acc float64) []Point {
		var pts []Point
		for i := 0; i < 20; i++ {
			pts = append(pts, Point{ID: fmt.Sprint(i), X: 12 + 0.01*float64(i), Y: 8, Acc: acc})
		}
		return pts
	}
	run := func(acc float64) Cluster {
		tr := NewTracker(cfg)
		var cs []Cluster
		for now := int64(0); now <= 5000; now += 250 {
			cs, _ = tr.Update(now, stack(acc))
		}
		if len(cs) != 1 {
			t.Fatalf("acc %.0f: %d clusters, want 1", acc, len(cs))
		}
		return cs[0]
	}
	exact := run(0)
	if exact.Level != protocol.LevelRed || exact.Acc != 0 {
		t.Errorf("20 hand-placed phones on one spot: level %s est %.1f acc %.1f, want red", exact.Level, exact.Est, exact.Acc)
	}
	rough := run(10)
	if rough.Level != protocol.LevelCalm || rough.Est > 0.5 || rough.Acc != 10 {
		t.Errorf("20 phones on one spot to ± 10 m: level %s est %.2f acc %.1f, want calm and well under 1/m²", rough.Level, rough.Est, rough.Acc)
	}
	if rough.R < 5 {
		t.Errorf("cluster of ± 10 m phones drawn %.1f m wide; it can't be known tighter than its positions", rough.R)
	}
	// A small accuracy radius leaves the local density alone (the usual
	// 1.5 m disc is wider than 0.5 × 2 m); only the point-mass reading of
	// the cluster's own tiny disc goes.
	if fine := run(2); fine.Peak != exact.Peak || math.Abs(fine.Est-fine.Peak) > 1e-9 {
		t.Errorf("± 2 m phones: peak %.2f est %.2f, exact peak %.2f; want the same peak, and est = peak", fine.Peak, fine.Est, exact.Peak)
	}
	// AccDisc 0 = positions taken as exact, as before.
	cfg.AccDisc = 0
	if c := describe(stack(10), seq(20), 1, 0); c.Est < 4 {
		t.Errorf("accDisc 0: est %.2f, want the point-mass reading", c.Est)
	}
}

func seq(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// TestRoughGuidance: a phone that knows where it is to ± 6 m, standing
// well outside a crowd, is still sent away from it (the big picture
// survives the blur); the push direction worked out from rough positions
// is ignored; and the confidence falls with the accuracy radius.
func TestRoughGuidance(t *testing.T) {
	rng := rand.New(rand.NewSource(4))
	var pts []Point
	for i := 0; i < 120; i++ {
		pts = append(pts, Point{ID: fmt.Sprint(i), X: 20 + 3*rng.NormFloat64(), Y: 20 + 3*rng.NormFloat64(), Acc: 6})
	}
	for _, p := range []GuideIn{{X: 28, Y: 20}, {X: 20, Y: 11}, {X: 13, Y: 26}} {
		p.ID, p.Acc, p.Reason = "x", 6, "density"
		dx, dy, _, _, _ := Direction(pts, 1, open, p)
		if a := angle(dx, dy, p.X-20, p.Y-20); a > 30 {
			t.Errorf("at %.0f,%.0f (± 6 m): direction (%.2f, %.2f) is %.0f° from straight away from the crowd", p.X, p.Y, dx, dy, a)
		}
		if l := math.Hypot(dx, dy); math.Abs(l-1) > 1e-9 {
			t.Errorf("not a unit vector: %.3f", l)
		}
	}
	// In the middle of the crowd the gradient says nothing; whatever comes
	// out must still be a direction that stays in the venue.
	g := Geom{W: 40, H: 22}
	dx, dy, _, _, _ := Direction(pts, 1, g, GuideIn{ID: "x", X: 20, Y: 20, Acc: 6, Reason: "density"})
	if math.Abs(math.Hypot(dx, dy)-1) > 1e-9 || dy > 0.5 {
		t.Errorf("mid-crowd near the bottom edge: (%.2f, %.2f), want a unit vector not into the edge", dx, dy)
	}
	// A push direction from rough positions is not followed.
	a, b, _, _, _ := Direction(pts, 1, open, GuideIn{ID: "x", X: 28, Y: 20, Acc: 6, Reason: "push", PushX: -1})
	c, d, _, _, _ := Direction(pts, 1, open, GuideIn{ID: "x", X: 28, Y: 20, Acc: 6, Reason: "density"})
	if a != c || b != d {
		t.Errorf("rough phone followed a push direction: (%.2f, %.2f) vs (%.2f, %.2f)", a, b, c, d)
	}
	gd := NewGuide()
	mv := gd.Update(0, pts, 1, open, []GuideIn{
		{ID: "hand", X: 28, Y: 20, Reason: "density"},
		{ID: "gps3", X: 28, Y: 20, Acc: 3, Reason: "density"},
		{ID: "gps8", X: 28, Y: 20, Acc: 8, Reason: "push", PushX: 1},
	})
	if mv["hand"].Conf != 1 || !(mv["gps3"].Conf >= GuideMinConf && mv["gps3"].Conf < 1) || mv["gps8"].Conf >= GuideMinConf {
		t.Errorf("confidence: hand %.2f, ± 3 m %.2f, ± 8 m %.2f; want 1, shown, not shown", mv["hand"].Conf, mv["gps3"].Conf, mv["gps8"].Conf)
	}
	if mv["gps8"].Reason != "density" {
		t.Errorf("rough phone's reason %q, want density (the push direction can't be trusted)", mv["gps8"].Reason)
	}
}
