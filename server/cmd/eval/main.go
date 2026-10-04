// Command eval runs Pulse's detection offline over every simulator scenario
// and many random crowds, and writes the evaluation report:
//
//	go run ./server/cmd/eval                 # 20 seeds, writes docs/eval.json and docs/EVAL.md
//	go run ./server/cmd/eval -seeds 5 -sim=false
//
// Scripted scenarios (internal/sim) run in the line layout (8 phones) and the
// crowd layout (24 wandering phones; gather uses 40), with ±25 ms clock
// error, through the detector and the crowd-density tracker exactly as the
// detector tests drive them. Crowd-simulation scripts (internal/crowdsim:
// Social Force Model people with ground truth) run through the same two
// stages the server's sim pipeline uses (no area rules), so each run also
// has a lead time against the simulated truth.
//
// Everything is run once per phone-realism condition (crowdsim/realism.go):
// ideal phones (the report's rows and summary, as before), the realistic
// and harsh presets, and realistic with each imperfection on alone (GPS
// error, carry, dropouts). The scripted scenarios' signals go through the
// same messy-phone model (crowdsim.Device) as the crowd simulation's.
//
// The JSON matches EvalReport in web/shared/protocol.ts (served at GET
// /api/eval), plus "realism": one entry per condition.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Drivera0/crowd-crush/server/internal/crowd"
	"github.com/Drivera0/crowd-crush/server/internal/crowdsim"
	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/geo"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/sim"
)

// Report is EvalReport in web/shared/protocol.ts.
type Report struct {
	Generated string  `json:"generated"`
	Seeds     int     `json:"seeds"`
	Rows      []Row   `json:"rows"`
	Summary   Summary `json:"summary"`
	// Realism: the same evaluation per phone-realism condition. The first
	// entry is "ideal" and equals Rows/Summary above.
	Realism []Condition `json:"realism,omitempty"`
}

// Condition is one phone-realism setting: the strengths of the three
// imperfections (0 = off, 1 = realistic, 2 = harsh) and what came out.
type Condition struct {
	Name    string  `json:"name"`
	GPS     float64 `json:"gps"`
	Carry   float64 `json:"carry"`
	Dropout float64 `json:"dropout"`
	Summary Summary `json:"summary"`
	// Pushes: wave and wave-jump runs (line and crowd) that went red.
	PushesCaught int `json:"pushesCaught"`
	PushRuns     int `json:"pushRuns"`
	// Packing: gather and the crowd-simulation surges that went red.
	PackingCaught int `json:"packingCaught"`
	PackingRuns   int `json:"packingRuns"`
	// PackingWarned: packing runs that reached at least yellow (a warning,
	// not an alarm).
	PackingWarned int `json:"packingWarned"`
	// CalmYellow: runs expected calm that reached yellow (never counted as
	// false alarms).
	CalmYellow int `json:"calmYellow"`
	CalmRuns   int `json:"calmRuns"`
	// StackReds: runs whose only red was the stack of GPS phones without a
	// usable fix on the default grid cell (false alarms on look-alikes; not
	// counted as caught on true positives).
	StackReds int `json:"stackReds"`
	// Diagnostic conditions are left out of the per-scenario matrix.
	Diag       bool        `json:"diagnostic,omitempty"`
	Sim        *SimMetrics `json:"sim,omitempty"`
	Rows       []Row       `json:"rows"`
	forceCarry int
	noG        bool
}

// SimMetrics compares Pulse with the crowd simulation's ground truth, over
// every crowd-simulation run of a condition.
type SimMetrics struct {
	// Density (people/m²): Pulse's highest cluster estimate minus the true
	// peak (the same statistic over every body, phone or not), sampled once
	// a second while the true peak is ≥ 2/m². Bias = mean signed error,
	// MAE = mean absolute error; per run, then the median over runs.
	DensityBias float64 `json:"densityBias"`
	DensityMAE  float64 `json:"densityMAE"`
	// Lead (s): truth dangerous − Pulse's first red, median over the runs
	// where both happened. DangerRuns = runs where the truth became
	// dangerous; WarnedFirst = those where Pulse was red first. Yellow
	// lead: the same against the first time anything left calm.
	MedianLeadS  *float64 `json:"medianLeadS,omitempty"`
	WarnedFirst  int      `json:"warnedFirst"`
	DangerRuns   int      `json:"dangerRuns"`
	MedianYLeadS *float64 `json:"medianYellowLeadS,omitempty"`
	// PosErrM: median distance between where Pulse thinks a phone is and
	// where its owner stands. CountedShare: phones Pulse counts (not stale,
	// outside or disconnected) ÷ phones truly in the venue.
	PosErrM      float64 `json:"posErrM"`
	CountedShare float64 `json:"countedShare"`
	// Guidance: for phones whose owner truly stands at ≥ 4/m², the angle
	// between the direction Pulse would show and the direction computed
	// from every body's true position.
	GuideSamples  int     `json:"guideSamples"`
	GuideWithin45 float64 `json:"guideWithin45"` // share within 45°
	GuideOpposite float64 `json:"guideOpposite"` // share more than 90° off
	// The same over the arrows Pulse would show: those with crowd.Conf ≥
	// crowd.GuideMinConf (the rest get a plain instruction, no arrow).
	GuideShown    float64 `json:"guideShown"` // share of the samples with an arrow
	GuideShown45  float64 `json:"guideShownWithin45"`
	GuideShownOpp float64 `json:"guideShownOpposite"`
}

type Row struct {
	Scenario    string   `json:"scenario"`
	Layout      string   `json:"layout"` // line | crowd | sim
	Expect      string   `json:"expect"` // red | calm | yellow-ok
	Runs        int      `json:"runs"`
	Red         int      `json:"red"`
	Yellow      int      `json:"yellow"`
	Calm        int      `json:"calm"`
	MedianRedS  *float64 `json:"medianRedS,omitempty"`
	MedianLeadS *float64 `json:"medianLeadS,omitempty"`
	// StackRed: runs (counted in Red) whose only red was the stack of GPS
	// phones without a usable fix on the default grid cell.
	StackRed int    `json:"stackRed,omitempty"`
	Note     string `json:"note,omitempty"`
}

type Summary struct {
	FalseAlarms   int `json:"falseAlarms"`
	LookAlikeRuns int `json:"lookAlikeRuns"`
	Missed        int `json:"missed"`
	PositiveRuns  int `json:"positiveRuns"`
}

const (
	expRed    = "red"
	expCalm   = "calm"
	expYellow = "yellow-ok"
)

// expectOf: true positives must go red; a single shove, one person
// squeezing past and a procession brushing past may reach yellow (README);
// everything else is a look-alike that must stay calm.
func expectOf(scenario string) string {
	switch scenario {
	case "wave", "wave-jump", "gather":
		return expRed
	case "shove", "walkpast", "procession":
		return expYellow
	}
	return expCalm
}

// run is one simulated run's outcome.
type run struct {
	key       string
	maxLevel  string
	redAt     float64 // s, -1 = never
	densRed   bool    // the first red came from a density cluster
	waveRed   bool    // a detector zone went red
	densAny   bool    // a cluster left calm at some point
	lead      float64 // sim: truth danger − first red (s)
	hasLead   bool
	dangerAt  float64 // sim: -1 = truth never dangerous
	maxDens   float64 // sim: truth max density
	maxPressN float64 // sim: truth max pressure
	yellowAt  float64 // first time anything left calm (s), -1 = never
	// GPS phones without a usable fix sit stacked on the default grid cell
	// and read as a packed cluster. ghostRed: that stack went red.
	// realRedAt: the first red that wasn't the stack (a wave zone or any
	// other cluster), -1 = never; lead times and "caught" use it.
	ghostRed  bool
	realRedAt float64
	// sim, sampled once a second
	densN            int // samples with the true peak ≥ densFrom
	densErr, densAbs float64
	posErr           []float64 // per-sample median position error (m)
	counted          []float64 // per-sample share of phones counted
	guideN           int
	guide45, guide90 int
	shownN           int // arrows confident enough to show
	shown45, shown90 int
}

type job struct {
	key  string
	fn   func() run
	cost float64 // rough relative cost, biggest first
}

var levelRank = map[string]int{protocol.LevelCalm: 0, protocol.LevelYellow: 1, protocol.LevelRed: 2}

func main() {
	seeds := flag.Int("seeds", 20, "random crowds per scripted scenario and layout")
	simSeeds := flag.Int("sim-seeds", 0, "crowd-simulation runs per script (default: same as -seeds)")
	withSim := flag.Bool("sim", true, "include the crowd-simulation (Social Force Model) scripts")
	out := flag.String("out", "docs/eval.json", "JSON report path")
	md := flag.String("md", "docs/EVAL.md", "Markdown report path (\"\" = don't write)")
	workers := flag.Int("workers", runtime.NumCPU(), "parallel runs")
	only := flag.String("realism", "", "comma-separated conditions to run besides ideal (default: all)")
	seed0 := flag.Int64("seed0", 1, "first seed (runs use seed0 … seed0+seeds-1): develop on one range, report on another")
	flag.Parse()
	if *seeds < 1 {
		log.Fatal("-seeds must be ≥ 1")
	}
	if *simSeeds <= 0 {
		*simSeeds = *seeds
	}

	conds := allConditions
	if *only != "" {
		conds = nil
		for _, name := range strings.Split(*only, ",") {
			found := false
			for _, c := range allConditions {
				if c.Name == strings.TrimSpace(name) {
					conds, found = append(conds, c), true
				}
			}
			if !found {
				log.Fatalf("unknown condition %q", name)
			}
		}
		if conds[0].Name != "ideal" {
			conds = append([]Condition{allConditions[0]}, conds...)
		}
	}

	var specs []rowSpec
	var jobs []job
	for ci, c := range conds {
		rl := crowdsim.Realism{GPS: c.GPS, Carry: c.Carry, Dropout: c.Dropout, ForceCarry: c.forceCarry, NoGravity: c.noG}
		for _, name := range sim.Scenarios {
			for _, layout := range []string{sim.LayoutLine, sim.LayoutCrowd} {
				if name == "gather" && layout == sim.LayoutLine {
					continue // walking to the stage needs free positions
				}
				n, dur := 8, 90.0
				if layout == sim.LayoutCrowd {
					n = 24
				}
				switch name {
				case "wave":
					dur = 70
				case "gather":
					n, dur = 40, 110
				}
				if ci == 0 {
					specs = append(specs, rowSpec{name, layout, expectOf(name), n, dur})
				}
				key := c.Name + "|" + name + "/" + layout
				for s := 0; s < *seeds; s++ {
					seed := *seed0 + int64(s)
					jobs = append(jobs, job{key: key, cost: dur * float64(n), fn: func() run {
						return scripted(name, layout, n, dur, seed, rl)
					}})
				}
			}
		}
		if *withSim {
			for _, sc := range simScripts {
				if ci == 0 {
					specs = append(specs, rowSpec{sc.name, "sim", sc.expect, 250, sc.dur})
				}
				for s := 0; s < *simSeeds; s++ {
					seed := *seed0 + int64(s)
					jobs = append(jobs, job{key: c.Name + "|" + sc.name + "/sim", cost: 1e9, fn: func() run {
						return simulated(sc, seed, rl)
					}})
				}
			}
		}
	}

	start := time.Now()
	log.Printf("%d runs on %d workers", len(jobs), *workers)
	results := runAll(jobs, *workers)
	log.Printf("done in %s", time.Since(start).Round(time.Second))

	byKey := map[string][]run{}
	for _, r := range results {
		byKey[r.key] = append(byKey[r.key], r)
	}
	rep := Report{Generated: time.Now().UTC().Format(time.RFC3339), Seeds: *seeds}
	for _, c := range conds {
		c.Rows, c.Summary = nil, Summary{}
		var simRuns []run
		for _, sp := range specs {
			rs := byKey[c.Name+"|"+sp.scenario+"/"+sp.layout]
			row := buildRow(sp, rs, &c.Summary)
			c.Rows = append(c.Rows, row)
			switch {
			case sp.scenario == "wave" || sp.scenario == "wave-jump":
				c.PushRuns += row.Runs
				c.PushesCaught += row.Red - row.StackRed
			case sp.expect == expRed:
				c.PackingRuns += row.Runs
				c.PackingCaught += row.Red - row.StackRed
				c.PackingWarned += row.Red - row.StackRed + row.Yellow
			case sp.expect == expCalm:
				c.CalmRuns += row.Runs
				c.CalmYellow += row.Yellow
			}
			c.StackReds += row.StackRed
			if sp.layout == "sim" {
				simRuns = append(simRuns, rs...)
			}
		}
		c.Sim = simMetrics(simRuns)
		rep.Realism = append(rep.Realism, c)
	}
	rep.Rows, rep.Summary = rep.Realism[0].Rows, rep.Realism[0].Summary

	b, _ := json.MarshalIndent(rep, "", "  ")
	if err := writeFile(*out, append(b, '\n')); err != nil {
		log.Fatal(err)
	}
	table := markdownTable(rep.Rows)
	fmt.Println(table)
	fmt.Printf("\nfalse alarms %d/%d look-alike runs · missed %d/%d true positives\n\n",
		rep.Summary.FalseAlarms, rep.Summary.LookAlikeRuns, rep.Summary.Missed, rep.Summary.PositiveRuns)
	fmt.Println(realismTables(rep))
	if *md != "" {
		old, _ := os.ReadFile(*md)
		if err := writeFile(*md, []byte(markdownDoc(rep, table, *simSeeds, *withSim, time.Since(start), findings(string(old))))); err != nil {
			log.Fatal(err)
		}
	}
	log.Printf("wrote %s%s", *out, map[bool]string{true: " and " + *md, false: ""}[*md != ""])
}

type rowSpec struct {
	scenario, layout, expect string
	n                        int
	dur                      float64
}

// allConditions: ideal, the two presets, and realistic with each
// imperfection on alone.
var allConditions = []Condition{
	{Name: "ideal"},
	{Name: "realistic", GPS: 1, Carry: 1, Dropout: 1},
	{Name: "harsh", GPS: 2, Carry: 2, Dropout: 2},
	{Name: "gps only", GPS: 1},
	{Name: "gps ×0.5 only", GPS: 0.5}, // 2.5 m median: the best a phone does under open sky
	{Name: "gps ×0.2 only", GPS: 0.2}, // 1 m median: better than phone GPS (UWB / BLE ranging territory)
	{Name: "carry only", Carry: 1},
	{Name: "dropouts only", Dropout: 1},
	// Diagnostics: carry only, every phone carried the same way.
	{Name: "carry: all chest (tilted)", Carry: 1, forceCarry: crowdsim.CarryChest + 1, Diag: true},
	{Name: "carry: all in the hand", Carry: 1, forceCarry: crowdsim.CarryHand + 1, Diag: true},
	{Name: "carry: all in a pocket", Carry: 1, forceCarry: crowdsim.CarryPocket + 1, Diag: true},
	{Name: "carry: all in a bag", Carry: 1, forceCarry: crowdsim.CarryBag + 1, Diag: true},
	// What levelling buys: the same phones without the gravity vector.
	{Name: "carry only, no g sent", Carry: 1, noG: true, Diag: true},
	{Name: "carry: all in a pocket, no g sent", Carry: 1, forceCarry: crowdsim.CarryPocket + 1, noG: true, Diag: true},
}

// buildRow summarises one scenario × layout and adds it to the summary.
func buildRow(sp rowSpec, rs []run, sum *Summary) Row {
	row := Row{Scenario: sp.scenario, Layout: sp.layout, Expect: sp.expect, Runs: len(rs)}
	var reds, leads []float64
	densReds, waveReds, densAny, noDanger := 0, 0, 0, 0
	var maxDens, maxPress []float64
	for _, r := range rs {
		switch r.maxLevel {
		case protocol.LevelRed:
			row.Red++
			if r.realRedAt >= 0 {
				reds = append(reds, r.realRedAt)
			} else if r.ghostRed {
				row.StackRed++
			} else {
				reds = append(reds, r.redAt)
			}
		case protocol.LevelYellow:
			row.Yellow++
		default:
			row.Calm++
		}
		if r.densRed {
			densReds++
		}
		if r.waveRed {
			waveReds++
		}
		if r.densAny {
			densAny++
		}
		if r.hasLead {
			leads = append(leads, r.lead)
		}
		if sp.layout == "sim" {
			if r.dangerAt < 0 {
				noDanger++
			}
			maxDens = append(maxDens, r.maxDens)
			maxPress = append(maxPress, r.maxPressN)
		}
	}
	if m, ok := median(reds); ok {
		row.MedianRedS = &m
	}
	if m, ok := median(leads); ok {
		row.MedianLeadS = &m
	}
	var notes []string
	switch sp.expect {
	case expRed:
		sum.PositiveRuns += row.Runs
		missed := row.Runs - (row.Red - row.StackRed) // red only from the stack is not a catch
		sum.Missed += missed
		if missed > 0 {
			notes = append(notes, fmt.Sprintf("missed %d/%d", missed, row.Runs))
		}
	default:
		sum.LookAlikeRuns += row.Runs
		sum.FalseAlarms += row.Red
		if row.Red > 0 {
			notes = append(notes, fmt.Sprintf("false alarm (red) %d/%d", row.Red, row.Runs))
		}
	}
	if row.Red > 0 && densReds > 0 {
		if densReds == row.Red && waveReds == 0 {
			notes = append(notes, "red from crowd density, not waves")
		} else {
			notes = append(notes, fmt.Sprintf("first red from density in %d", densReds))
		}
	} else if densAny > 0 && sp.layout != "sim" {
		notes = append(notes, fmt.Sprintf("density yellow in %d", densAny))
	}
	if row.StackRed > 0 {
		notes = append(notes, fmt.Sprintf("only red was phones without a usable fix stacked on the default spot in %d", row.StackRed))
	}
	if sp.layout == sim.LayoutCrowd && sp.n != 24 {
		notes = append(notes, fmt.Sprintf("%d phones", sp.n))
	}
	if sp.layout == "sim" {
		if noDanger > 0 {
			notes = append(notes, fmt.Sprintf("truth never dangerous in %d/%d", noDanger, row.Runs))
		}
		if d, ok := median(maxDens); ok {
			p, _ := median(maxPress)
			notes = append(notes, fmt.Sprintf("median peak %.1f/m², %.0f N/m", d, p))
		}
	}
	row.Note = strings.Join(notes, "; ")
	return row
}

func mean(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func med2(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	m := s[len(s)/2]
	if len(s)%2 == 0 {
		m = (s[len(s)/2-1] + s[len(s)/2]) / 2
	}
	return math.Round(m*100) / 100
}

// simMetrics pools the crowd-simulation runs of one condition.
func simMetrics(rs []run) *SimMetrics {
	if len(rs) == 0 {
		return nil
	}
	m := &SimMetrics{}
	var bias, mae, leads, yleads, pos, counted []float64
	g45, g90 := 0, 0
	sn, s45, s90 := 0, 0, 0
	for _, r := range rs {
		if r.densN > 0 {
			bias = append(bias, r.densErr/float64(r.densN))
			mae = append(mae, r.densAbs/float64(r.densN))
		}
		if r.dangerAt >= 0 {
			m.DangerRuns++
			if r.hasLead {
				leads = append(leads, r.lead)
				if r.lead > 0 {
					m.WarnedFirst++
				}
			}
			if r.yellowAt >= 0 {
				yleads = append(yleads, r.dangerAt-r.yellowAt)
			}
		}
		pos = append(pos, mean(r.posErr))
		counted = append(counted, mean(r.counted))
		m.GuideSamples += r.guideN
		g45 += r.guide45
		g90 += r.guide90
		sn += r.shownN
		s45 += r.shown45
		s90 += r.shown90
	}
	m.DensityBias, m.DensityMAE = med2(bias), med2(mae)
	if v, ok := median(leads); ok {
		m.MedianLeadS = &v
	}
	if v, ok := median(yleads); ok {
		m.MedianYLeadS = &v
	}
	m.PosErrM, m.CountedShare = med2(pos), med2(counted)
	if m.GuideSamples > 0 {
		m.GuideWithin45 = math.Round(100*float64(g45)/float64(m.GuideSamples)) / 100
		m.GuideOpposite = math.Round(100*float64(g90)/float64(m.GuideSamples)) / 100
		m.GuideShown = math.Round(100*float64(sn)/float64(m.GuideSamples)) / 100
	}
	if sn > 0 {
		m.GuideShown45 = math.Round(100*float64(s45)/float64(sn)) / 100
		m.GuideShownOpp = math.Round(100*float64(s90)/float64(sn)) / 100
	}
	return m
}

func writeFile(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// runAll runs the jobs on n workers, the expensive ones first.
func runAll(jobs []job, n int) []run {
	sort.SliceStable(jobs, func(i, j int) bool { return jobs[i].cost > jobs[j].cost })
	ch := make(chan job)
	out := make([]run, 0, len(jobs))
	var mu sync.Mutex
	var wg sync.WaitGroup
	done := 0
	for range max(n, 1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range ch {
				r := j.fn()
				r.key = j.key
				mu.Lock()
				out = append(out, r)
				done++
				if done%100 == 0 {
					log.Printf("%d/%d runs", done, len(jobs))
				}
				mu.Unlock()
			}
		}()
	}
	for _, j := range jobs {
		ch <- j
	}
	close(ch)
	wg.Wait()
	return out
}

func median(v []float64) (float64, bool) {
	if len(v) == 0 {
		return 0, false
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	m := s[len(s)/2]
	if len(s)%2 == 0 {
		m = (s[len(s)/2-1] + s[len(s)/2]) / 2
	}
	return math.Round(m*10) / 10, true
}

// stages is the detector plus the crowd-density tracker, stepped every
// 250 ms like the server's pipeline (without area rules).
type stages struct {
	det   *detect.Detector
	crowd *crowd.Tracker
	gone  map[string]int64 // disconnected phones (sim), by when
	t0    int64
	out   run
	ghost bool // GPS phones: some may sit stacked on the default grid cell
	// the latest step, for the ground-truth comparison
	pts      []crowd.Point
	clusters []crowd.Cluster
}

func newStages(cfg detect.Config, t0 int64) *stages {
	return &stages{det: detect.New(cfg), crowd: crowd.NewTracker(crowd.ConfigFrom(cfg)), gone: map[string]int64{}, t0: t0,
		out: run{maxLevel: protocol.LevelCalm, redAt: -1, dangerAt: -1, yellowAt: -1, realRedAt: -1}}
}

// forgetAfterMs matches the server: disconnected phones leave the map after 30 s.
const forgetAfterMs = 30_000

func (s *stages) step(now int64) {
	res := s.det.Step(now)
	var pts []crowd.Point
	for _, p := range res.Phones {
		if _, gone := s.gone[p.ID]; gone || p.Status == protocol.StatusStale || p.Outside {
			continue
		}
		pts = append(pts, crowd.Point{ID: p.ID, X: p.X, Y: p.Y, Acc: p.Acc})
	}
	clusters, cch := s.crowd.Update(now, pts)
	s.pts, s.clusters = pts, clusters
	for id, at := range s.gone {
		if now-at > forgetAfterMs {
			s.det.RemovePhone(id)
			delete(s.gone, id)
		}
	}
	level, real := protocol.LevelCalm, protocol.LevelCalm // real: without the default-spot stack
	waveRed, densRed := false, false
	for _, z := range res.Zones {
		if levelRank[z.Level] > levelRank[level] {
			level, real = z.Level, z.Level
		}
		waveRed = waveRed || z.Level == protocol.LevelRed
	}
	gx, gy := s.det.Config().LegacyPos(0, 0)
	stack := func(c crowd.Cluster) bool { return s.ghost && math.Hypot(c.PeakX-gx, c.PeakY-gy) < 1 }
	for _, c := range clusters {
		if levelRank[c.Level] > levelRank[level] {
			level = c.Level
		}
		switch {
		case !stack(c):
			if levelRank[c.Level] > levelRank[real] {
				real = c.Level
			}
		case c.Level == protocol.LevelRed:
			s.out.ghostRed = true
		}
		densRed = densRed || c.Level == protocol.LevelRed
		s.out.densAny = s.out.densAny || c.Level != protocol.LevelCalm
	}
	for _, ch := range cch {
		densRed = densRed || ch.To == protocol.LevelRed
		if ch.To == protocol.LevelRed && !stack(ch.Cluster) {
			real = protocol.LevelRed
		}
	}
	if levelRank[level] > levelRank[s.out.maxLevel] {
		s.out.maxLevel = level
	}
	if real != protocol.LevelCalm && s.out.yellowAt < 0 {
		s.out.yellowAt = float64(now-s.t0) / 1000
	}
	if real == protocol.LevelRed && s.out.realRedAt < 0 {
		s.out.realRedAt = float64(now-s.t0) / 1000
	}
	s.out.waveRed = s.out.waveRed || waveRed
	if (waveRed || densRed) && s.out.redAt < 0 {
		s.out.redAt = float64(now-s.t0) / 1000
		s.out.densRed = densRed && !waveRed
		if densRed {
			s.out.maxLevel = protocol.LevelRed
		}
	}
}

// feeder delivers phone messages (crowdsim events) to the stages the way
// the server's pipeline does: hello and pos place the phone; a GPS-like fix
// is dropped when its accuracy is worse than gpsMaxAcc, smoothed by
// accuracy (geo.Smoother) and clamped, the phone marked outside when the
// smoothed fix is off the venue (App.PhoneGPS); a motion summary carries its
// gravity vector.
type feeder struct {
	st    *stages
	cfg   detect.Config
	known map[string]bool
	gps   map[string]*geo.Smoother
	pos   map[string][2]float64 // where Pulse thinks each phone is
	acc   map[string]float64    // … and how well it says it knows (m; 0 = exact)
	shift map[string]int64      // extra clock error per phone (ms)
}

func newFeeder(st *stages, cfg detect.Config) *feeder {
	return &feeder{st: st, cfg: cfg, known: map[string]bool{}, gps: map[string]*geo.Smoother{}, pos: map[string][2]float64{}, acc: map[string]float64{}}
}

func (f *feeder) place(id string, x, y, acc float64, outside bool) {
	x, y = f.cfg.Clamp(x, y)
	f.st.det.SetPhone(id, x, y)
	f.st.det.SetAccuracy(id, acc)
	f.st.det.SetOutside(id, outside)
	f.pos[id] = [2]float64{x, y}
	f.acc[id] = acc
}

func (f *feeder) feed(now int64, evs []crowdsim.Event) {
	for _, e := range evs {
		if e.Kind != crowdsim.EvHello && !f.known[e.ID] {
			continue
		}
		switch e.Kind {
		case crowdsim.EvHello:
			f.known[e.ID] = true
			f.gps[e.ID] = &geo.Smoother{}
			delete(f.st.gone, e.ID)
			x, y := e.X, e.Y
			if e.Auto {
				// A GPS phone's hello has no position: it is nowhere
				// (unplaced: on the default grid cell, counted toward
				// nothing) until its first usable fix, as in the app.
				x, y = f.cfg.LegacyPos(0, 0)
				f.st.ghost = true
			}
			f.place(e.ID, x, y, 0, e.Auto)
		case crowdsim.EvPos:
			f.gps[e.ID].Reset()
			f.place(e.ID, e.X, e.Y, 0, false)
		case crowdsim.EvGPS:
			if !(e.Acc > 0 && e.Acc <= f.cfg.GPSMaxAcc) {
				continue
			}
			s := f.gps[e.ID]
			x, y := s.Add(e.X, e.Y, e.Acc)
			acc := math.Max(0.1, math.Round(s.Acc*10)/10)
			x, y, out := f.cfg.Place(x, y, acc) // a fix just off the venue is mirrored back in
			f.place(e.ID, x, y, acc, out)
		case crowdsim.EvMotion:
			f.st.det.Add(e.ID, detect.Sample{T: e.M.T + f.shift[e.ID], AX: e.M.AX, AY: e.M.AY, AZ: e.M.AZ, Rot: e.M.Rot, G: detect.Gravity(e.M.G)})
		case crowdsim.EvGone:
			f.st.gone[e.ID] = now
		}
	}
}

// scripted runs one internal/sim scenario the way the detector tests do:
// moving phones report their position every 500 ms, ±25 ms clock error,
// the line split into zones of four phones. With messy phones (rl not
// ideal) the same signals and positions go through crowdsim.Device first.
func scripted(name, layout string, n int, dur float64, seed int64, rl crowdsim.Realism) run {
	lay := sim.LineLayout(1, n)
	if layout == sim.LayoutCrowd {
		lay = sim.CrowdLayout(true)
	}
	sc, err := sim.NewLayout(name, n, seed, lay)
	if err != nil {
		log.Fatal(err)
	}
	cfg := detect.DefaultConfig()
	const t0 = 1_700_000_000_000
	st := newStages(cfg, t0)
	if lay.Kind == sim.LayoutLine {
		st.det.SetZones(lineZones(cfg, sc.Cols))
	}
	const clockErrMs = 25
	rng := rand.New(rand.NewSource(7))
	errs := make([]int64, n)
	if !rl.Ideal() {
		for i := range errs {
			errs[i] = rng.Int63n(2*clockErrMs+1) - clockErrMs
		}
		return scriptedMessy(sc, st, cfg, name, n, dur, seed, rl, errs)
	}
	for i := 0; i < n; i++ {
		x, y := sc.Pos(i)
		st.det.SetPhone(fmt.Sprint(i), x, y)
		errs[i] = rng.Int63n(2*clockErrMs+1) - clockErrMs
	}
	evs := sc.Generate(t0, dur)
	moves := sc.Moves()
	j := 0
	for now := int64(t0); now <= t0+int64(dur*1000); now += 250 {
		if moves && (now-t0)%500 == 0 {
			for i := 0; i < n; i++ {
				x, y := sc.PosAt(i, float64(now-t0)/1000)
				st.det.SetPhone(fmt.Sprint(i), x, y)
			}
		}
		for j < len(evs) && evs[j].T <= now {
			e := evs[j]
			st.det.Add(fmt.Sprint(e.Phone), detect.Sample{T: e.T + errs[e.Phone], AX: e.AX, AY: e.AY, AZ: e.AZ, Rot: e.Rot})
			j++
		}
		st.step(now)
	}
	return st.out
}

// scriptedMessy is scripted with messy phones. Each scripted 100 ms summary
// is the body's motion for those 100 ms (held over the five 20 ms ticks),
// the scripted position is where the person truly stands; the device
// decides what the phone sends. The ±25 ms clock error stays on top. In
// "walk" and "march" every person is walking (a pocket phone gets its leg
// swing); the other scenarios' walkers aren't marked as such.
func scriptedMessy(sc *sim.Scenario, st *stages, cfg detect.Config, name string, n int, dur float64, seed int64, rl crowdsim.Realism, errs []int64) run {
	const t0 = 1_700_000_000_000
	f := newFeeder(st, cfg)
	f.shift = map[string]int64{}
	evs := sc.Generate(t0, dur)
	sums := make([][]sim.Event, n)
	for _, e := range evs {
		sums[e.Phone] = append(sums[e.Phone], e)
	}
	rng := rand.New(rand.NewSource(seed*7919 + 13))
	devs := make([]*crowdsim.Device, n)
	stepHz := make([]float64, n)
	phase := make([]float64, n)
	for i := range devs {
		id := fmt.Sprint(i)
		devs[i] = crowdsim.NewDevice(id, rl, seed*100_003+int64(i)*7+1)
		f.shift[id] = errs[i]
		stepHz[i] = 1.6 + 0.4*rng.Float64()
	}
	walk := name == "walk" || name == "march"
	// A summary's time is the start of its window, as sim.Generate stamps it.
	env := crowdsim.Env{StartMs: t0 - 100}
	var out []crowdsim.Event
	st.step(t0)
	for k := 1; k <= int(math.Round(dur*50)); k++ {
		t := float64(k) * crowdsim.Dt
		env.Common.Step(crowdsim.Dt, rl.GPS, rng)
		out = out[:0]
		w := (k - 1) / 5
		for i, d := range devs {
			if w >= len(sums[i]) {
				continue
			}
			e := sums[i][w]
			x, y := sc.PosAt(i, t)
			raw := crowdsim.Raw{T: t, X: x, Y: y, BX: e.AX, BY: e.AY, BZ: e.AZ, Rot: e.Rot}
			if walk {
				phase[i] += 2 * math.Pi * stepHz[i] * crowdsim.Dt
				raw.Gait, raw.Step = 1, phase[i]
			}
			out = d.Tick(raw, env, out)
		}
		now := t0 + int64(k)*20
		f.feed(now, out)
		if (now-t0)%250 == 0 {
			st.step(now)
		}
	}
	return st.out
}

// lineZones splits a line of cols phones into zones of four, as the
// detector tests do.
func lineZones(cfg detect.Config, cols int) []detect.ZoneDef {
	var out []detect.ZoneDef
	for c0, i := 0, 0; c0 < cols; c0, i = c0+4, i+1 {
		x0, _ := cfg.LegacyPos(0, c0)
		x0 -= cfg.LegacySpacing / 2
		x1 := x0 + 4*cfg.LegacySpacing
		if c0 == 0 {
			x0 = 0
		}
		if c0+4 >= cols {
			x1 = cfg.VenueW
		}
		out = append(out, detect.ZoneDef{ID: detect.ZoneName(i), Name: "Zone " + detect.ZoneName(i), Poly: detect.Rect(x0, 0, x1, cfg.VenueH)})
	}
	return out
}

// simScript is a crowd-simulation director script: act is called once per
// simulated second.
type simScript struct {
	name, expect string
	dur          float64
	act          func(w *crowdsim.World, s int, rng *rand.Rand)
}

func fp(v float64) *float64 { return &v }

func apply(w *crowdsim.World, a crowdsim.Action) {
	if err := w.Apply(a); err != nil {
		log.Fatalf("sim action %s: %v", a.Type, err)
	}
}

// The scripts of the server's lead-time sweep (internal/app sim_test.go),
// plus a calm crowd and a group gathering around a point.
var simScripts = []simScript{
	{name: "calm", expect: expCalm, dur: 80, act: func(*crowdsim.World, int, *rand.Rand) {}},
	{name: "attract", expect: expYellow, dur: 80, act: func(w *crowdsim.World, s int, _ *rand.Rand) {
		if s == 5 {
			apply(w, crowdsim.Action{Type: "attract", X: fp(12), Y: fp(9)})
		}
	}},
	{name: "calm→surge", expect: expRed, dur: 80, act: func(w *crowdsim.World, s int, rng *rand.Rand) {
		if s == 30 {
			apply(w, crowdsim.Action{Type: "surge", Strength: fp(0.7)})
		}
		if s > 30 && s%3 == 0 {
			apply(w, crowdsim.Action{Type: "shove", X: fp(6 + 12*rng.Float64()), Y: fp(3 + 3*rng.Float64()), DX: fp(0), DY: fp(-1)})
		}
	}},
	{name: "stage→surge", expect: expRed, dur: 80, act: func(w *crowdsim.World, s int, rng *rand.Rand) {
		if s == 5 {
			apply(w, crowdsim.Action{Type: "stage"})
		}
		if s == 30 {
			apply(w, crowdsim.Action{Type: "surge", Strength: fp(0.7)})
		}
		if s > 30 && s%3 == 0 {
			dx := []float64{1, -1, 0}[rng.Intn(3)]
			apply(w, crowdsim.Action{Type: "shove", X: fp(6 + 12*rng.Float64()), Y: fp(3 + 3*rng.Float64()), DX: fp(dx), DY: fp(-1)})
		}
	}},
	{name: "stage→surge 0.3", expect: expRed, dur: 80, act: func(w *crowdsim.World, s int, _ *rand.Rand) {
		if s == 5 {
			apply(w, crowdsim.Action{Type: "stage"})
		}
		if s == 30 {
			apply(w, crowdsim.Action{Type: "surge", Strength: fp(0.3)})
		}
	}},
}

// densFrom: the density comparison only counts moments when the true peak
// is at least this (people/m²): where an error matters.
const densFrom = 2.0

// simulated runs a crowd-simulation script: 250 people, 60 % with phones,
// physics every 50 ms, detection every 250 ms, phone messages fed in as
// the server's sim pipeline does (already on the server clock). Once a
// second Pulse's picture is compared with the simulation's ground truth.
func simulated(sc simScript, seed int64, rl crowdsim.Realism) run {
	const t0 = 1_700_000_000_000
	cfg := detect.DefaultConfig()
	cfg.Participation = 0.6
	w, err := crowdsim.New(crowdsim.Config{W: cfg.VenueW, H: cfg.VenueH, People: 250, Participation: 0.6, Seed: seed, StartMs: t0, Realism: rl})
	if err != nil {
		log.Fatal(err)
	}
	st := newStages(cfg, t0)
	f := newFeeder(st, cfg)
	f.feed(t0, w.Events())
	geom := crowd.Geom{W: cfg.VenueW, H: cfg.VenueH}
	exits, walls := crowdsim.GeometryJSON(w.G)
	geom.Walls = walls
	for _, e := range exits {
		if e.Open {
			geom.Exits = append(geom.Exits, crowd.Exit{ID: e.ID, Name: e.Name, X0: e.X0, Y0: e.Y0, X1: e.X1, Y1: e.Y1})
		}
	}
	rng := rand.New(rand.NewSource(seed))
	for now := int64(t0); now < t0+int64(sc.dur*1000); {
		now += 50
		if (now-t0)%1000 == 0 {
			sc.act(w, int((now-t0)/1000), rng)
		}
		w.AdvanceTo(now)
		f.feed(now, w.Events())
		if (now-t0)%250 == 0 {
			st.step(now)
		}
		if (now-t0)%1000 == 0 {
			compare(w, st, f, cfg, geom)
		}
	}
	tr := w.Truth()
	st.out.dangerAt = tr.DangerAt
	st.out.maxDens, st.out.maxPressN = tr.MaxDensity, tr.MaxPressure
	if tr.DangerAt >= 0 && st.out.realRedAt >= 0 {
		st.out.lead = math.Round((tr.DangerAt-st.out.realRedAt)*10) / 10
		st.out.hasLead = true
	}
	return st.out
}

// compare measures Pulse's latest step against the simulation's truth.
func compare(w *crowdsim.World, st *stages, f *feeder, cfg detect.Config, geom crowd.Geom) {
	agents := w.Agents()
	truth := make([]crowd.Point, len(agents))
	phones := 0
	for i, a := range agents {
		truth[i] = crowd.Point{ID: fmt.Sprint(a.ID), X: a.X, Y: a.Y}
		if a.PhoneID() != "" {
			phones++
		}
	}
	counted := map[string]bool{}
	for _, p := range st.pts {
		counted[p.ID] = true
	}
	// Position error and how many phones count.
	var errs []float64
	for _, a := range agents {
		if id := a.PhoneID(); counted[id] {
			p := f.pos[id]
			errs = append(errs, math.Hypot(p[0]-a.X, p[1]-a.Y))
		}
	}
	if phones > 0 {
		st.out.counted = append(st.out.counted, float64(len(errs))/float64(phones))
	}
	if len(errs) > 0 {
		sort.Float64s(errs)
		st.out.posErr = append(st.out.posErr, errs[len(errs)/2])
	}
	// Density: the same peak statistic over every body.
	truePeak, _, _ := crowd.LocalPeakAmong(truth, truth)
	if truePeak >= densFrom {
		est := 0.0
		for _, c := range st.clusters {
			est = math.Max(est, c.Est)
		}
		st.out.densN++
		st.out.densErr += est - truePeak
		st.out.densAbs += math.Abs(est - truePeak)
	}
	// Guidance, for the phones whose owner truly stands in a dangerous
	// density: Pulse's direction (its positions, its participation) against
	// the direction from everyone's true position.
	if truePeak < cfg.DensityDanger {
		return
	}
	r2 := crowd.LocalR * crowd.LocalR
	area := math.Pi * r2
	for _, a := range agents {
		id := a.PhoneID()
		if !counted[id] {
			continue
		}
		k := 0
		for _, b := range agents {
			if dx, dy := a.X-b.X, a.Y-b.Y; dx*dx+dy*dy <= r2 {
				k++
			}
		}
		if float64(k)/area < cfg.DensityDanger {
			continue
		}
		tx, ty, _, _, _ := crowd.Direction(truth, 1, geom, crowd.GuideIn{ID: id, X: a.X, Y: a.Y, Reason: "density"})
		p := f.pos[id]
		px, py, _, _, _ := crowd.Direction(st.pts, cfg.Participation, geom, crowd.GuideIn{ID: id, X: p[0], Y: p[1], Acc: f.acc[id], Reason: "density"})
		cos := tx*px + ty*py
		shown := crowd.Conf(f.acc[id]) >= crowd.GuideMinConf
		st.out.guideN++
		if shown {
			st.out.shownN++
		}
		if cos >= math.Cos(math.Pi/4) {
			st.out.guide45++
			if shown {
				st.out.shown45++
			}
		}
		if cos < 0 {
			st.out.guide90++
			if shown {
				st.out.shown90++
			}
		}
	}
}

func fmtOpt(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%.1f", *v)
}

func markdownTable(rows []Row) string {
	var b strings.Builder
	b.WriteString("| Scenario | Layout | Expect | Runs | Red | Yellow | Calm | Median red (s) | Median lead (s) | Note |\n")
	b.WriteString("|---|---|---|---:|---:|---:|---:|---:|---:|---|\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| `%s` | %s | %s | %d | %d | %d | %d | %s | %s | %s |\n",
			r.Scenario, r.Layout, r.Expect, r.Runs, r.Red, r.Yellow, r.Calm, fmtOpt(r.MedianRedS), fmtOpt(r.MedianLeadS), r.Note)
	}
	return b.String()
}

// The hand-written findings in EVAL.md sit between these markers and are
// kept when the report is regenerated.
const (
	findStart = "<!-- findings:start -->"
	findEnd   = "<!-- findings:end -->"
)

func findings(old string) string {
	a, b := strings.Index(old, findStart), strings.Index(old, findEnd)
	if a < 0 || b < a {
		return findStart + "\n" + findEnd
	}
	return old[a : b+len(findEnd)]
}

func pct(v float64) string { return fmt.Sprintf("%.0f %%", 100*v) }

// realismTables: the per-condition summary, the ground-truth comparison
// and the red-run matrix.
func realismTables(rep Report) string {
	var b strings.Builder
	cs := rep.Realism
	b.WriteString("| Phones | GPS | Carry | Dropouts | False alarms (red) | Pushes caught | Packing caught | Packing at least yellow | Calm runs at yellow | Red only from the default-spot stack |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	var main []Condition
	for _, c := range cs {
		fmt.Fprintf(&b, "| %s | %g | %g | %g | %d / %d | %d / %d | %d / %d | %d / %d | %d / %d | %d |\n", c.Name, c.GPS, c.Carry, c.Dropout,
			c.Summary.FalseAlarms, c.Summary.LookAlikeRuns, c.PushesCaught, c.PushRuns, c.PackingCaught, c.PackingRuns, c.PackingWarned, c.PackingRuns, c.CalmYellow, c.CalmRuns, c.StackReds)
		if !c.Diag {
			main = append(main, c)
		}
	}
	if cs[0].Sim != nil {
		b.WriteString("\nCrowd simulation against its ground truth:\n\n")
		b.WriteString("| Phones | Position error (m) | Phones counted | Density bias (/m²) | Density abs. error (/m²) | Red before danger | Median lead, red (s) | Median lead, first yellow (s) | Guidance within 45° | Guidance > 90° off | Arrow shown | … within 45° | … > 90° off |\n")
		b.WriteString("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
		for _, c := range cs {
			m := c.Sim
			g45, g90, gs, s45, s90 := "—", "—", "—", "—", "—"
			if m.GuideSamples > 0 {
				g45, g90, gs = pct(m.GuideWithin45), pct(m.GuideOpposite), pct(m.GuideShown)
				if m.GuideShown > 0 {
					s45, s90 = pct(m.GuideShown45), pct(m.GuideShownOpp)
				}
			}
			fmt.Fprintf(&b, "| %s | %.2f | %s | %+.2f | %.2f | %d / %d | %s | %s | %s | %s | %s | %s | %s |\n", c.Name, m.PosErrM, pct(m.CountedShare),
				m.DensityBias, m.DensityMAE, m.WarnedFirst, m.DangerRuns, fmtOpt(m.MedianLeadS), fmtOpt(m.MedianYLeadS), g45, g90, gs, s45, s90)
		}
	}
	b.WriteString("\nRuns that went red, per scenario (**bold** = wrong: a look-alike that went red, or a true positive missed in at least one run; `s` = runs whose only red was the default-spot stack, counted as red on look-alikes and as missed on true positives; `y` = calm runs that reached yellow):\n\n")
	cs = main
	b.WriteString("| Scenario | Layout | Expect |")
	for _, c := range cs {
		b.WriteString(" " + c.Name + " |")
	}
	b.WriteString("\n|---|---|---|" + strings.Repeat("---:|", len(cs)) + "\n")
	for i, r := range cs[0].Rows {
		fmt.Fprintf(&b, "| `%s` | %s | %s |", r.Scenario, r.Layout, r.Expect)
		for _, c := range cs {
			x := c.Rows[i]
			red := x.Red
			if x.Expect == expRed {
				red -= x.StackRed
			}
			cell := fmt.Sprintf("%d / %d", red, x.Runs)
			if (x.Expect == expRed && red < x.Runs) || (x.Expect != expRed && red > 0) {
				cell = "**" + cell + "**"
			}
			if x.StackRed > 0 {
				cell += fmt.Sprintf(" (%d s)", x.StackRed)
			}
			if x.Expect == expCalm && x.Yellow > 0 {
				cell += fmt.Sprintf(" (%d y)", x.Yellow)
			}
			b.WriteString(" " + cell + " |")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func markdownDoc(rep Report, table string, simSeeds int, withSim bool, took time.Duration, found string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Pulse evaluation\n\n")
	fmt.Fprintf(&b, "Generated %s by `go run ./server/cmd/eval -seeds %d` (`make eval`) in %s on %d CPU threads. Machine-readable copy: [`eval.json`](eval.json) (served at `GET /api/eval`).\n\n",
		rep.Generated, rep.Seeds, took.Round(time.Second), runtime.NumCPU())
	s := rep.Summary
	fmt.Fprintf(&b, "**False alarms: %d of %d look-alike runs went red. Missed: %d of %d true-positive runs never went red.**\n\n",
		s.FalseAlarms, s.LookAlikeRuns, s.Missed, s.PositiveRuns)
	b.WriteString("The table below is with **ideal phones** (upright on the chest, exact position, nothing lost): the conditions the detector was tuned in. [Messy phones](#messy-phones) repeats everything with phones as they really are.\n\n")
	b.WriteString(table)
	b.WriteString("\n## Messy phones\n\n")
	b.WriteString("The same runs with the phones made as messy as real ones (`server/internal/crowdsim/realism.go`, where the parameters and their sources are): **GPS** error instead of the true position (slowly drifting, median 5 m per phone at strength 1, 10 m at strength 2, with jumps and a reported accuracy), **carry** (25 % chest, 25 % in the hand, 35 % trouser pocket, 15 % bag at strength 1; the phone's axes are no longer the body's; it sends its gravity vector), **dropouts** (screen locks of seconds to minutes, stalls that deliver messages in clumps, lost summaries, clock error, slow sensors). Strength 0 = off, 1 = realistic, 2 = harsh. The \"only\" rows have one imperfection on alone, at strength 1 unless it says otherwise (GPS ×0.5 = 2.5 m median error, ×0.2 = 1 m). Detector and thresholds are the same in every row. *Packing at least yellow* counts the packing runs that raised a warning (yellow) or an alarm (red).\n\n")
	b.WriteString(realismTables(rep))
	b.WriteString("\n" + found + "\n")
	b.WriteString(`
## Method

- **Scripted scenarios** (` + "`internal/sim`" + `): every scenario in the line layout (8 phones 0.6 m apart, split into zones of four) and the crowd layout (24 phones, ~70 % in one or two dense groups, the rest scattered, all wandering slowly and reporting their position every 500 ms; ` + "`gather`" + ` uses 40 phones and has no line version). Each phone gets a fixed clock-sync error of up to ±25 ms. Each run is one random crowd (seed 1…N): positions, timings, amplitudes and tilts all change with the seed. Runs last 90 s (` + "`wave`" + ` 70 s, ` + "`gather`" + ` 110 s).
- **Pipeline**: the 100 ms motion summaries go into the real detector (` + "`internal/detect`" + `, default config) and its result into the real crowd-density tracker (` + "`internal/crowd`" + `), stepped every 250 ms, as the server does. Staff-drawn area rules are not part of this evaluation. A run's level is the worst any zone or cluster reached; "red" counts a run that went red at any moment.
- **Expectations**: ` + "`wave`" + ` and ` + "`wave-jump`" + ` (a growing push travelling through the crowd) and ` + "`gather`" + ` (people packing in at ~10/m²) must go red. A single ` + "`shove`" + `, one person squeezing past (` + "`walkpast`" + `) and a ` + "`procession`" + ` brushing past may reach yellow (` + "`yellow-ok`" + `) but not red. Everything else is a look-alike that must stay calm. A **false alarm** is a look-alike or yellow-ok run that went red; **missed** is a true-positive run that never went red. Yellow on a calm row is visible in the table but not counted as a false alarm.
- **Density yellow on calm crowd rows**: the crowd layout packs most phones into one or two tight groups (σ ≈ 0.7 m) and the default participation is 1.0, so in some random crowds the density tracker reads a group as crowded (yellow) whatever the phones are doing. The same seeds do it in every scenario, because the positions depend only on the seed. That is the density alert reacting to where people stand, not to the motion.
`)
	if withSim {
		fmt.Fprintf(&b, `- **Messy phones**: each condition reruns every scenario and seed. The crowd simulation's phones are built messy (`+"`crowdsim.Config.Realism`"+`). The scripted scenarios' 100 ms signals are taken as the body's motion and their positions as where people truly stand, and go through the same phone model (`+"`crowdsim.Device`"+`); only in `+"`walk`"+` and `+"`march`"+` are people marked as walking (a pocket phone gets its leg swing). GPS fixes are gated and smoothed as the server does for live phones (`+"`gpsMaxAcc`"+`, `+"`geo.Smoother`"+`), and the detector is told each phone's accuracy radius: such phones find their neighbours by motion and their density is measured at the scale the fix supports. A smoothed fix off the venue by less than its accuracy radius is mirrored back in; further off, the phone counts as outside. A GPS phone with no usable fix yet is nowhere and counts toward nothing. Note that the line layout models the tap-your-spot demo, where real phones don't use GPS: its GPS columns say what would happen if they did.
- **Ground-truth comparison** (crowd simulation only, once a second): *position error* = median distance between where Pulse places a phone and where its owner stands; *phones counted* = phones Pulse uses ÷ phones truly in the venue; *density* = Pulse's highest cluster estimate minus the true peak (the same 1.5 m statistic over every body, with or without a phone), while the true peak is ≥ 2/m², as mean signed error (bias) and mean absolute error per run, median over runs (with ideal phones the remaining error is the 60 %% sample and the estimator itself); *red before danger* = surge runs where Pulse was red before the truth turned dangerous, of the runs where it did; *guidance* = for each counted phone whose owner truly stands at ≥ 4/m², the unsmoothed direction Pulse would show (`+"`crowd.Direction`"+` from Pulse's positions) against the same function over every body's true position; *arrow shown* = the share of those arrows Pulse would show rather than replace by a plain instruction (confidence ≥ 0.5, i.e. an accuracy radius of at most 4 m), with the same two shares over the shown ones.
- **Crowd simulation** (`+"`internal/crowdsim`"+`, layout `+"`sim`"+`): %d seeds per script, 250 simulated people (Social Force Model) on the default 24 × 16 m venue, 60 %% carrying a phone, participation set to 0.6. Physics every 50 ms, detection every 250 ms. The phones' messages go through the same detector and density tracker; this reproduces the server's sim pipeline (minus area rules) without the app package. Scripts: `+"`calm`"+` (nothing happens), `+"`attract`"+` (a group forms around a point at 5 s), `+"`calm→surge`"+` (surge 0.7 at 30 s plus a shove every 3 s), `+"`stage→surge`"+` (front-of-stage crowding from 5 s, surge 0.7 and shoves from 30 s) and `+"`stage→surge 0.3`"+`. **Lead** = when the simulated truth first became dangerous (≥ 3 people at ≥ 1600 N/m or ≥ 5 people above 6/m², held 1 s) minus Pulse's first red; positive means Pulse warned first.
`, simSeeds)
	} else {
		b.WriteString("- Crowd-simulation scripts were skipped (`-sim=false`).\n")
	}
	b.WriteString(`
## Limits: this is not real-world validation

- **Simulated motion.** Every number above comes from motion that Pulse's own simulators generate. The scripted scenarios are hand-written signals (a push is a damped sine travelling at 2.4 m/s); the crowd simulation is a physics model calibrated only against walking speeds (Weidmann's fundamental diagram), not against recorded crowd crushes or real phone sensors.
- **Tuned on the same simulator.** The detector's thresholds and guards (lag window, chain rule, vertical veto, hold times) were tuned while watching these same scenarios. Performance on scenarios it was tuned on overstates performance on motion it has never seen.
- **One venue, small crowds.** A 24 × 16 m floor, 8–40 phones (250 simulated people in the crowd simulation). Nothing here says how thresholds behave for thousands of phones, other venue shapes, or real phone placement (pockets, hands, bags).
- **Density depends on participation.** The density alerts count phones; the simulation knows the true share of people with a phone, a real event has to estimate it.
- **Wave detection in the crowd simulation.** In the Social Force Model a push crosses packed neighbours faster than the detector's 120 ms per-hop floor, so the simulated surges are caught by crowd density, not by the travelling-wave detector (see the README). Real pushes recorded with phones would settle which model is right.
- **What would count as validation:** labelled recordings of real crowds (including real pushes and real look-alikes like concerts and stadium waves), replayed through the same pipeline, with thresholds frozen beforehand.
`)
	return b.String()
}
