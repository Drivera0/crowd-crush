package crowdsim

import (
	"math"
	"os"
	"strconv"
	"testing"
)

// corridorSpeed is the mean walking speed in a 12 × 2.5 m periodic
// corridor at the given density: warm up, then average over measure s.
func corridorSpeed(density float64, seed int64, warm, measure float64) float64 {
	w := NewCorridor(12, 2.5, density, seed)
	for w.T < warm {
		w.Step()
	}
	sum, n := 0.0, 0
	for w.T < warm+measure {
		w.Step()
		sum += w.MeanSpeedX()
		n++
	}
	return sum / float64(n)
}

// TestWeidmann checks the walking speed against Weidmann's fundamental
// diagram at three densities: speed falls as density rises, and the
// curve is followed within a tolerance (see the package doc for the full
// table: PULSE_WEIDMANN=1 go test ./server/internal/crowdsim -run Weidmann -v).
func TestWeidmann(t *testing.T) {
	cases := []struct{ rho, tol float64 }{
		{0.5, 0.2},
		{2, 0.15},
		{5, 0.1},
	}
	prev := math.Inf(1)
	for _, c := range cases {
		v := corridorSpeed(c.rho, 1, 8, 8)
		want := Weidmann(c.rho)
		t.Logf("ρ = %.1f /m²: %.2f m/s (Weidmann %.2f)", c.rho, v, want)
		if math.Abs(v-want) > c.tol {
			t.Errorf("ρ = %.1f: speed %.2f m/s, Weidmann %.2f ± %.2f", c.rho, v, want, c.tol)
		}
		if v >= prev {
			t.Errorf("speed did not fall with density: %.2f at ρ = %.1f", v, c.rho)
		}
		prev = v
	}
}

// TestWeidmannSweep prints the full fundamental diagram (env-gated).
func TestWeidmannSweep(t *testing.T) {
	if os.Getenv("PULSE_WEIDMANN") == "" {
		t.Skip("set PULSE_WEIDMANN=1 to print the fundamental diagram")
	}
	if v, err := strconv.ParseFloat(os.Getenv("PULSE_TIMEGAP"), 64); err == nil {
		TimeGap = v
	}
	if v, err := strconv.ParseFloat(os.Getenv("PULSE_STEER"), 64); err == nil {
		steerAngles = []float64{0, v / 2, -v / 2, v, -v}
	}
	t.Logf("time gap %.2f s, steering up to %.2f rad", TimeGap, steerAngles[len(steerAngles)-1]*-1)
	t.Logf("%6s %10s %10s %8s", "ρ /m²", "sim m/s", "Weidmann", "diff")
	for _, rho := range []float64{0.5, 1, 1.5, 2, 2.5, 3, 3.5, 4, 4.5, 5} {
		v := 0.0
		for seed := int64(1); seed <= 3; seed++ {
			v += corridorSpeed(rho, seed, 10, 20) / 3
		}
		want := Weidmann(rho)
		t.Logf("%6.1f %10.2f %10.2f %+8.2f", rho, v, want, v-want)
	}
}
