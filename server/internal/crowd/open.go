package crowd

import "math"

// Boundary correction for a local density. A count of people within r of
// someone standing against a wall or a stage barrier covers a disc that is
// partly where nobody can stand, so the plain count ÷ π r² reads about half
// the real density exactly where a crush is worst. Dividing by the part of
// the disc people can stand on instead is the usual edge correction of a
// density estimate.

// openPattern is 37 points spread evenly (by area) over the unit disc.
var openPattern = func() [][2]float64 {
	pts := [][2]float64{{0, 0}}
	for ring, n := range []int{6, 12, 18} {
		r := (float64(ring) + 1) / 3 * 0.92
		for k := 0; k < n; k++ {
			a := 2 * math.Pi * (float64(k) + 0.5*float64(ring%2)) / float64(n)
			pts = append(pts, [2]float64{r * math.Cos(a), r * math.Sin(a)})
		}
	}
	return pts
}()

// MinOpen is the least open fraction OpenFraction reports: a corner or a
// narrow passage never multiplies a count by more than 1 / MinOpen.
const MinOpen = 0.4

// OpenFraction is the share (MinOpen..1) of the disc of radius r around
// (x, y) that people can stand on, sampled at 37 points.
func OpenFraction(x, y, r float64, open func(x, y float64) bool) float64 {
	n := 0
	for _, p := range openPattern {
		if open(x+r*p[0], y+r*p[1]) {
			n++
		}
	}
	return math.Max(MinOpen, float64(n)/float64(len(openPattern)))
}
