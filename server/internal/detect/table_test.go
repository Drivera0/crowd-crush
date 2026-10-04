package detect

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"

	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/sim"
)

// Table demo measurements: 2–5 phones held in the hand by people in a row
// 0.6 m apart (the demo spot), for each thing judges try (sim/table.go).

// tableOutcome is one run. Times are seconds after the case starts
// (sim.TableLeadIn), -1 = never.
type tableOutcome struct {
	maxLevel           string
	yellowAt, redAt    float64
	waveSteps          int
	pairSteps          int // steps with a two-phone push edge (table profile)
	togetherAt         float64
	handlingSeen       bool
	maxScore           float64
	levelBeforeStart   string // worst level during the lead-in (must be calm)
	finalLevel         string
	nodeWaveSteps      int // steps with some node in status wave (red dot)
	togetherBeforeCase bool
}

const tableT0 = 1_700_000_000_000

// runTable feeds one table case through a detector. table turns the demo
// profile on (SetTable).
func runTable(t *testing.T, name string, n int, seed int64, dur float64, cfg Config, table bool) tableOutcome {
	t.Helper()
	sc, err := sim.NewTable(name, n, seed, dur)
	if err != nil {
		t.Fatal(err)
	}
	d := New(cfg)
	d.SetTable(table)
	for i := 0; i < n; i++ {
		d.SetPhone(fmt.Sprint(i), 12.0+0.6*float64(i), 8)
	}
	// Clock sync leaves each phone off by up to ±25 ms.
	rng := rand.New(rand.NewSource(seed))
	errs := make([]int64, n)
	for i := range errs {
		errs[i] = rng.Int63n(51) - 25
	}
	evs := sc.Generate(tableT0, dur)
	o := tableOutcome{maxLevel: protocol.LevelCalm, yellowAt: -1, redAt: -1, togetherAt: -1, levelBeforeStart: protocol.LevelCalm}
	start := tableT0 + int64(sim.TableLeadIn*1000)
	j := 0
	for now := int64(tableT0); now <= tableT0+int64(dur*1000); now += 250 {
		for j < len(evs) && evs[j].T <= now {
			e := evs[j]
			d.Add(fmt.Sprint(e.Phone), Sample{T: e.T + errs[e.Phone], AX: e.AX, AY: e.AY, AZ: e.AZ, Rot: e.Rot, G: e.G})
			j++
		}
		r := d.Step(now)
		since := float64(now-start) / 1000
		pair := false
		for _, e := range r.Waves() {
			if e.Pair {
				pair = true
			}
		}
		if pair {
			o.pairSteps++
		}
		if len(r.Waves()) > 0 && !pair {
			o.waveSteps++
		}
		for _, p := range r.Phones {
			o.handlingSeen = o.handlingSeen || p.Status == protocol.StatusHandling
		}
		for _, p := range r.Phones {
			if p.Status == protocol.StatusWave {
				o.nodeWaveSteps++
				break
			}
		}
		if len(r.Together) > 0 {
			if now < start {
				o.togetherBeforeCase = true
			} else if o.togetherAt < 0 {
				o.togetherAt = since
			}
		}
		for _, z := range r.Zones {
			if now < start {
				if levelRank[z.Level] > levelRank[o.levelBeforeStart] {
					o.levelBeforeStart = z.Level
				}
				continue
			}
			o.maxScore = max(o.maxScore, z.Score)
			if levelRank[z.Level] > levelRank[o.maxLevel] {
				o.maxLevel = z.Level
			}
			if z.Level != protocol.LevelCalm && o.yellowAt < 0 {
				o.yellowAt = since
			}
			if z.Level == protocol.LevelRed && o.redAt < 0 {
				o.redAt = since
			}
		}
		o.finalLevel = protocol.LevelCalm
		for _, z := range r.Zones {
			if levelRank[z.Level] > levelRank[o.finalLevel] {
				o.finalLevel = z.Level
			}
		}
	}
	return o
}

// tableSeeds is how many random rows each cell of the measurement runs.
const tableSeeds = 10

// tableDur is how long each case runs after the lead-in.
const tableDur = 40

type tableCell struct {
	yellow, red, together int
	yAt, rAt, tAt         []float64
	leadIn                int // runs not calm during the lead-in
}

func medianOf(v []float64) string {
	if len(v) == 0 {
		return "—"
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return fmt.Sprintf("%.1f", s[len(s)/2])
}

func measureTable(t *testing.T, name string, n int, table bool) tableCell {
	var c tableCell
	for seed := int64(1); seed <= tableSeeds; seed++ {
		o := runTable(t, name, n, seed, sim.TableLeadIn+tableDur, DefaultConfig(), table)
		if o.levelBeforeStart != protocol.LevelCalm {
			c.leadIn++
		}
		if o.yellowAt >= 0 {
			c.yellow++
			c.yAt = append(c.yAt, o.yellowAt)
		}
		if o.redAt >= 0 {
			c.red++
			c.rAt = append(c.rAt, o.redAt)
		}
		if o.togetherAt >= 0 {
			c.together++
			c.tAt = append(c.tAt, o.togetherAt)
		}
	}
	return c
}

// TestTableMeasure prints the measurement table (go test -run
// TestTableMeasure -v ./server/internal/detect). It asserts nothing: the
// outcomes are pinned by TestTableDemo.
func TestTableMeasure(t *testing.T) {
	if testing.Short() {
		t.Skip("measurement")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n| Case | Phones | Profile | Yellow (runs) | Median yellow (s) | Red (runs) | Median red (s) | Moving together (runs) | Median (s) |\n|---|---:|---|---:|---:|---:|---:|---:|---:|\n")
	for _, name := range sim.TableCases {
		for n := 2; n <= 5; n++ {
			for _, table := range []bool{false, true} {
				c := measureTable(t, name, n, table)
				prof := "off"
				if table {
					prof = "on"
				}
				fmt.Fprintf(&b, "| %s | %d | %s | %d/%d | %s | %d/%d | %s | %d/%d | %s |\n", name, n, prof,
					c.yellow, tableSeeds, medianOf(c.yAt), c.red, tableSeeds, medianOf(c.rAt), c.together, tableSeeds, medianOf(c.tAt))
				if c.leadIn > 0 {
					fmt.Fprintf(&b, "|  ↳ not calm while standing still before the case in %d runs |\n", c.leadIn)
				}
			}
		}
	}
	t.Log(b.String())
}
