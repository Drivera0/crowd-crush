package crowd

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// Density at table scale: 2–5 judges lined up 0.6 m apart at the demo spot
// must not read as a dangerously packed crowd.

// tableRow is n phones 0.6 m apart from (x0, y0) toward +x.
func tableRow(n int, x0, y0 float64) []Point {
	var pts []Point
	for i := 0; i < n; i++ {
		pts = append(pts, Point{ID: fmt.Sprint(i), X: x0 + 0.6*float64(i), Y: y0})
	}
	return pts
}

type tableDensity struct {
	maxDens  float64 // highest per-phone packed density (people/m²)
	maxLevel string  // worst per-phone packed level
	clEst    float64 // highest cluster estimate
	clLevel  string  // worst cluster level
}

// runTableDensity holds the row still for 30 s (packed and clusters both
// settle) in a 24 × 16 m venue.
func runTableDensity(pts []Point, part float64) tableDensity {
	dc := detect.DefaultConfig()
	dc.Participation = part
	cfg := ConfigFrom(dc)
	open := func(x, y float64) bool { return x >= 0 && y >= 0 && x <= 24 && y <= 16 }
	pk := NewPackedTracker()
	tr := NewTracker(cfg)
	out := tableDensity{maxLevel: protocol.LevelCalm, clLevel: protocol.LevelCalm}
	for now := int64(1_000_000); now <= 1_030_000; now += 250 {
		for _, p := range pk.Update(now, pts, cfg, open) {
			out.maxDens = max(out.maxDens, p.Dens)
			if PackedRank(p.Level) > PackedRank(out.maxLevel) {
				out.maxLevel = p.Level
			}
		}
		cl, _ := tr.Update(now, pts)
		for _, c := range cl {
			out.clEst = max(out.clEst, c.Est)
			if PackedRank(c.Level) > PackedRank(out.clLevel) {
				out.clLevel = c.Level
			}
		}
	}
	return out
}

func TestTableDensityMeasure(t *testing.T) {
	spots := []struct {
		name   string
		x0, y0 float64
	}{{"venue centre", 12, 8}, {"along a wall", 10, 0.2}, {"in a corner", 0.2, 0.2}}
	var b strings.Builder
	b.WriteString("\n| Spot | Phones | Participation | Max per-phone density (/m²) | Per-phone level | Cluster est (/m²) | Cluster level |\n|---|---:|---:|---:|---|---:|---|\n")
	for _, sp := range spots {
		for n := 2; n <= 5; n++ {
			for _, part := range []float64{1, 0.6, 0.3} {
				r := runTableDensity(tableRow(n, sp.x0, sp.y0), part)
				fmt.Fprintf(&b, "| %s | %d | %.1f | %.2f | %s | %.2f | %s |\n", sp.name, n, part, r.maxDens, r.maxLevel, r.clEst, r.clLevel)
			}
		}
	}
	t.Log(b.String())
}
