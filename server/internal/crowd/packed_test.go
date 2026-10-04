package crowd

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

func packedCfg() Config {
	return Config{Watch: 2, Danger: 4, Participation: 1, Margin: 0.1, HoldMs: 2000, AccDisc: 0.5}
}

// lattice is a square lattice of phones at rho per m² over [x0, x0+w] × [y0, y0+h].
func lattice(rho, x0, y0, w, h float64, prefix string) []Point {
	d := 1 / math.Sqrt(rho)
	var pts []Point
	for y := y0 + d/2; y < y0+h; y += d {
		for x := x0 + d/2; x < x0+w; x += d {
			pts = append(pts, Point{ID: fmt.Sprintf("%s%d", prefix, len(pts)), X: x, Y: y})
		}
	}
	return pts
}

// TestPackedGradient: a packed block against a wall with a loose crowd
// behind it. The block is red right up to the wall (the edge correction),
// the loose crowd is calm, and nothing flickers on the way.
func TestPackedGradient(t *testing.T) {
	open := func(x, y float64) bool { return x >= 0 && y >= 0 && x <= 24 && y <= 16 }
	pts := append(lattice(5, 8, 0, 8, 3, "core"), lattice(1, 0, 6, 24, 8, "loose")...)
	tr := NewPackedTracker()
	var got map[string]Packed
	for now := int64(0); now <= 4000; now += 250 {
		got = tr.Update(now, pts, packedCfg(), open)
		if now < 2000 {
			for id, p := range got {
				if p.Level != protocol.LevelCalm {
					t.Fatalf("%s is %s after %d ms: the level must hold first", id, p.Level, now)
				}
			}
		}
	}
	var wall, mid float64
	nw, nm := 0, 0
	for _, p := range pts {
		g := got[p.ID]
		switch {
		case strings.HasPrefix(p.ID, "loose"):
			if g.Level != protocol.LevelCalm || g.Crush > 0.1 {
				t.Errorf("loose phone %s at (%.1f, %.1f): %s, %.1f /m², crush %.2f", p.ID, p.X, p.Y, g.Level, g.Dens, g.Crush)
			}
		case p.X > 9.5 && p.X < 14.5 && p.Y < 0.5:
			wall, nw = wall+g.Dens, nw+1
			if g.Level != protocol.LevelRed {
				t.Errorf("phone against the wall at (%.1f, %.1f): %s, %.1f /m²", p.X, p.Y, g.Level, g.Dens)
			}
		case p.X > 9.5 && p.X < 14.5 && p.Y > 1.2 && p.Y < 1.6:
			mid, nm = mid+g.Dens, nm+1
			if g.Level != protocol.LevelRed || g.Crush < 0.7 {
				t.Errorf("phone in the core at (%.1f, %.1f): %s, %.1f /m², crush %.2f", p.X, p.Y, g.Level, g.Dens, g.Crush)
			}
		}
	}
	wall, mid = wall/float64(nw), mid/float64(nm)
	t.Logf("against the wall %.1f /m², in the core %.1f /m² (true 5)", wall, mid)
	if math.Abs(wall-5) > 1 || math.Abs(mid-5) > 0.8 {
		t.Errorf("densities %.1f (wall), %.1f (core), want about 5", wall, mid)
	}
	// Without the correction the corner phone reads far less.
	plain := NewPackedTracker().Update(0, pts, packedCfg(), nil)
	corr := NewPackedTracker().Update(0, pts, packedCfg(), open)
	if plain["core0"].Dens > 0.75*corr["core0"].Dens {
		t.Errorf("corner phone uncorrected %.1f vs corrected %.1f: no edge correction", plain["core0"].Dens, corr["core0"].Dens)
	}
	// The crowd thins out: red clears only below danger − margin, and a
	// phone that is gone has no level.
	thin := lattice(3.8, 8, 0, 8, 3, "core")
	for now := int64(4250); now <= 12000; now += 250 {
		got = tr.Update(now, thin, packedCfg(), open)
	}
	if g := got["core20"]; g.Level != protocol.LevelRed {
		t.Errorf("at 3.8 /m² (above danger − margin) a red phone went %s (%.1f /m²)", g.Level, g.Dens)
	}
	if _, ok := got["loose3"]; ok {
		t.Error("a phone that no longer counts still has a crush level")
	}
	for now := int64(12250); now <= 20000; now += 250 {
		got = tr.Update(now, lattice(1, 8, 0, 8, 3, "core"), packedCfg(), open)
	}
	if g := got["core3"]; g.Level != protocol.LevelCalm || g.Crush > 0.1 {
		t.Errorf("at 1 /m²: %s, crush %.2f", g.Level, g.Crush)
	}
}

func TestCrush01(t *testing.T) {
	for _, c := range []struct{ d, want float64 }{{0, 0}, {1, 0}, {2, 0.35}, {3, 0.525}, {4, 0.7}, {5, 0.85}, {6, 1}, {9, 1}} {
		if got := Crush01(c.d, 2, 4); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("Crush01(%g) = %g, want %g", c.d, got, c.want)
		}
	}
}
