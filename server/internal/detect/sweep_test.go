package detect

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/Drivera0/crowd-crush/server/internal/sim"
)

// TestSweep is a tuning aid, not a regression test: PULSE_SWEEP=1 runs every
// scenario over many simulator seeds and prints how often each level is reached.
func TestSweep(t *testing.T) {
	if os.Getenv("PULSE_SWEEP") == "" {
		t.Skip("set PULSE_SWEEP=1")
	}
	seeds := 20
	cfg := DefaultConfig()
	if s := os.Getenv("PULSE_CFG"); s != "" {
		if err := json.Unmarshal([]byte(s), &cfg); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range sim.Scenarios {
		dur := 90.0
		if name == "wave" {
			dur = 70
		}
		counts := map[string]int{}
		var reds, ylw []float64
		notCalmEnd, steps := 0, 0
		for s := 1; s <= seeds; s++ {
			o := runScenarioCfg(t, name, 8, dur, 25, false, int64(s), cfg)
			counts[o.maxLevel]++
			steps += o.waveSteps
			if o.yellowAt >= 0 {
				ylw = append(ylw, o.yellowAt)
			}
			if o.redAt >= 0 {
				reds = append(reds, o.redAt)
			}
			for _, l := range o.finalLevels {
				if l != "calm" {
					notCalmEnd++
					break
				}
			}
		}
		sort.Float64s(reds)
		sort.Float64s(ylw)
		med := func(x []float64) float64 {
			if len(x) == 0 {
				return -1
			}
			return x[len(x)/2]
		}
		var rs []string
		for _, r := range reds {
			rs = append(rs, fmt.Sprintf("%.0f", r))
		}
		fmt.Printf("%-13s calm=%2d yellow=%2d red=%2d notCalmAtEnd=%2d medYellow=%.0f medRed=%.0f waveSteps/run=%d redAt=[%s]\n", name,
			counts["calm"], counts["yellow"], counts["red"], notCalmEnd, med(ylw), med(reds), steps/seeds, strings.Join(rs, " "))
	}
}
