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
// The JSON matches EvalReport in web/shared/protocol.ts (served at GET /api/eval).
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
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/sim"
)

// Report is EvalReport in web/shared/protocol.ts.
type Report struct {
	Generated string  `json:"generated"`
	Seeds     int     `json:"seeds"`
	Rows      []Row   `json:"rows"`
	Summary   Summary `json:"summary"`
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
	Note        string   `json:"note,omitempty"`
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
	flag.Parse()
	if *seeds < 1 {
		log.Fatal("-seeds must be ≥ 1")
	}
	if *simSeeds <= 0 {
		*simSeeds = *seeds
	}

	type rowSpec struct {
		scenario, layout, expect string
		n                        int
		dur                      float64
		runs                     int
	}
	var specs []rowSpec
	var jobs []job
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
			sp := rowSpec{name, layout, expectOf(name), n, dur, *seeds}
			specs = append(specs, sp)
			key := name + "/" + layout
			for s := 1; s <= *seeds; s++ {
				seed := int64(s)
				jobs = append(jobs, job{key: key, cost: dur * float64(n), fn: func() run {
					return scripted(name, layout, n, dur, seed)
				}})
			}
		}
	}
	if *withSim {
		for _, sc := range simScripts {
			specs = append(specs, rowSpec{sc.name, "sim", sc.expect, 250, sc.dur, *simSeeds})
			for s := 1; s <= *simSeeds; s++ {
				seed := int64(s)
				jobs = append(jobs, job{key: sc.name + "/sim", cost: 1e9, fn: func() run {
					return simulated(sc, seed)
				}})
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
	for _, sp := range specs {
		rs := byKey[sp.scenario+"/"+sp.layout]
		row := Row{Scenario: sp.scenario, Layout: sp.layout, Expect: sp.expect, Runs: len(rs)}
		var reds, leads []float64
		densReds, waveReds, densAny, noDanger := 0, 0, 0, 0
		var maxDens, maxPress []float64
		for _, r := range rs {
			switch r.maxLevel {
			case protocol.LevelRed:
				row.Red++
				reds = append(reds, r.redAt)
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
			rep.Summary.PositiveRuns += row.Runs
			missed := row.Runs - row.Red
			rep.Summary.Missed += missed
			if missed > 0 {
				notes = append(notes, fmt.Sprintf("missed %d/%d", missed, row.Runs))
			}
		default:
			rep.Summary.LookAlikeRuns += row.Runs
			rep.Summary.FalseAlarms += row.Red
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
		rep.Rows = append(rep.Rows, row)
	}

	b, _ := json.MarshalIndent(rep, "", "  ")
	if err := writeFile(*out, append(b, '\n')); err != nil {
		log.Fatal(err)
	}
	table := markdownTable(rep)
	fmt.Println(table)
	fmt.Printf("\nfalse alarms %d/%d look-alike runs · missed %d/%d true positives\n",
		rep.Summary.FalseAlarms, rep.Summary.LookAlikeRuns, rep.Summary.Missed, rep.Summary.PositiveRuns)
	if *md != "" {
		if err := writeFile(*md, []byte(markdownDoc(rep, table, *simSeeds, *withSim, time.Since(start)))); err != nil {
			log.Fatal(err)
		}
	}
	log.Printf("wrote %s%s", *out, map[bool]string{true: " and " + *md, false: ""}[*md != ""])
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
}

func newStages(cfg detect.Config, t0 int64) *stages {
	return &stages{det: detect.New(cfg), crowd: crowd.NewTracker(crowd.ConfigFrom(cfg)), gone: map[string]int64{}, t0: t0,
		out: run{maxLevel: protocol.LevelCalm, redAt: -1, dangerAt: -1}}
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
		pts = append(pts, crowd.Point{ID: p.ID, X: p.X, Y: p.Y})
	}
	clusters, cch := s.crowd.Update(now, pts)
	for id, at := range s.gone {
		if now-at > forgetAfterMs {
			s.det.RemovePhone(id)
			delete(s.gone, id)
		}
	}
	level := protocol.LevelCalm
	waveRed, densRed := false, false
	for _, z := range res.Zones {
		if levelRank[z.Level] > levelRank[level] {
			level = z.Level
		}
		waveRed = waveRed || z.Level == protocol.LevelRed
	}
	for _, c := range clusters {
		if levelRank[c.Level] > levelRank[level] {
			level = c.Level
		}
		densRed = densRed || c.Level == protocol.LevelRed
		s.out.densAny = s.out.densAny || c.Level != protocol.LevelCalm
	}
	for _, ch := range cch {
		densRed = densRed || ch.To == protocol.LevelRed
	}
	if levelRank[level] > levelRank[s.out.maxLevel] {
		s.out.maxLevel = level
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

// scripted runs one internal/sim scenario the way the detector tests do:
// moving phones report their position every 500 ms, ±25 ms clock error,
// the line split into zones of four phones.
func scripted(name, layout string, n int, dur float64, seed int64) run {
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

// simulated runs a crowd-simulation script: 250 people, 60 % with phones,
// physics every 50 ms, detection every 250 ms, phone messages fed in as
// the server's sim pipeline does (already on the server clock).
func simulated(sc simScript, seed int64) run {
	const t0 = 1_700_000_000_000
	cfg := detect.DefaultConfig()
	cfg.Participation = 0.6
	w, err := crowdsim.New(crowdsim.Config{W: cfg.VenueW, H: cfg.VenueH, People: 250, Participation: 0.6, Seed: seed, StartMs: t0})
	if err != nil {
		log.Fatal(err)
	}
	st := newStages(cfg, t0)
	known := map[string]bool{}
	feed := func(now int64) {
		for _, e := range w.Events() {
			switch e.Kind {
			case crowdsim.EvHello:
				x, y := cfg.Clamp(e.X, e.Y)
				st.det.SetPhone(e.ID, x, y)
				known[e.ID] = true
				delete(st.gone, e.ID)
			case crowdsim.EvPos:
				if known[e.ID] {
					x, y := cfg.Clamp(e.X, e.Y)
					st.det.SetPhone(e.ID, x, y)
				}
			case crowdsim.EvMotion:
				if known[e.ID] {
					st.det.Add(e.ID, detect.Sample{T: e.M.T, AX: e.M.AX, AY: e.M.AY, AZ: e.M.AZ, Rot: e.M.Rot})
				}
			case crowdsim.EvGone:
				if known[e.ID] {
					st.gone[e.ID] = now
				}
			}
		}
	}
	feed(t0)
	rng := rand.New(rand.NewSource(seed))
	for now := int64(t0); now < t0+int64(sc.dur*1000); {
		now += 50
		if (now-t0)%1000 == 0 {
			sc.act(w, int((now-t0)/1000), rng)
		}
		w.AdvanceTo(now)
		feed(now)
		if (now-t0)%250 == 0 {
			st.step(now)
		}
	}
	tr := w.Truth()
	st.out.dangerAt = tr.DangerAt
	st.out.maxDens, st.out.maxPressN = tr.MaxDensity, tr.MaxPressure
	if tr.DangerAt >= 0 && st.out.redAt >= 0 {
		st.out.lead = math.Round((tr.DangerAt-st.out.redAt)*10) / 10
		st.out.hasLead = true
	}
	return st.out
}

func fmtOpt(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%.1f", *v)
}

func markdownTable(rep Report) string {
	var b strings.Builder
	b.WriteString("| Scenario | Layout | Expect | Runs | Red | Yellow | Calm | Median red (s) | Median lead (s) | Note |\n")
	b.WriteString("|---|---|---|---:|---:|---:|---:|---:|---:|---|\n")
	for _, r := range rep.Rows {
		fmt.Fprintf(&b, "| `%s` | %s | %s | %d | %d | %d | %d | %s | %s | %s |\n",
			r.Scenario, r.Layout, r.Expect, r.Runs, r.Red, r.Yellow, r.Calm, fmtOpt(r.MedianRedS), fmtOpt(r.MedianLeadS), r.Note)
	}
	return b.String()
}

func markdownDoc(rep Report, table string, simSeeds int, withSim bool, took time.Duration) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Pulse evaluation\n\n")
	fmt.Fprintf(&b, "Generated %s by `go run ./server/cmd/eval -seeds %d` (`make eval`) in %s on %d CPU threads. Machine-readable copy: [`eval.json`](eval.json) (served at `GET /api/eval`).\n\n",
		rep.Generated, rep.Seeds, took.Round(time.Second), runtime.NumCPU())
	s := rep.Summary
	fmt.Fprintf(&b, "**False alarms: %d of %d look-alike runs went red. Missed: %d of %d true-positive runs never went red.**\n\n",
		s.FalseAlarms, s.LookAlikeRuns, s.Missed, s.PositiveRuns)
	b.WriteString(table)
	b.WriteString(`
## Method

- **Scripted scenarios** (` + "`internal/sim`" + `): every scenario in the line layout (8 phones 0.6 m apart, split into zones of four) and the crowd layout (24 phones, ~70 % in one or two dense groups, the rest scattered, all wandering slowly and reporting their position every 500 ms; ` + "`gather`" + ` uses 40 phones and has no line version). Each phone gets a fixed clock-sync error of up to ±25 ms. Each run is one random crowd (seed 1…N): positions, timings, amplitudes and tilts all change with the seed. Runs last 90 s (` + "`wave`" + ` 70 s, ` + "`gather`" + ` 110 s).
- **Pipeline**: the 100 ms motion summaries go into the real detector (` + "`internal/detect`" + `, default config) and its result into the real crowd-density tracker (` + "`internal/crowd`" + `), stepped every 250 ms, as the server does. Staff-drawn area rules are not part of this evaluation. A run's level is the worst any zone or cluster reached; "red" counts a run that went red at any moment.
- **Expectations**: ` + "`wave`" + ` and ` + "`wave-jump`" + ` (a growing push travelling through the crowd) and ` + "`gather`" + ` (people packing in at ~10/m²) must go red. A single ` + "`shove`" + `, one person squeezing past (` + "`walkpast`" + `) and a ` + "`procession`" + ` brushing past may reach yellow (` + "`yellow-ok`" + `) but not red. Everything else is a look-alike that must stay calm. A **false alarm** is a look-alike or yellow-ok run that went red; **missed** is a true-positive run that never went red. Yellow on a calm row is visible in the table but not counted as a false alarm.
- **Density yellow on calm crowd rows**: the crowd layout packs most phones into one or two tight groups (σ ≈ 0.7 m) and the default participation is 1.0, so in some random crowds the density tracker reads a group as crowded (yellow) whatever the phones are doing. The same seeds do it in every scenario, because the positions depend only on the seed. That is the density alert reacting to where people stand, not to the motion.
`)
	if withSim {
		fmt.Fprintf(&b, `- **Crowd simulation** (`+"`internal/crowdsim`"+`, layout `+"`sim`"+`): %d seeds per script, 250 simulated people (Social Force Model) on the default 24 × 16 m venue, 60 %% carrying a phone, participation set to 0.6. Physics every 50 ms, detection every 250 ms. The phones' messages go through the same detector and density tracker; this reproduces the server's sim pipeline (minus area rules) without the app package. Scripts: `+"`calm`"+` (nothing happens), `+"`attract`"+` (a group forms around a point at 5 s), `+"`calm→surge`"+` (surge 0.7 at 30 s plus a shove every 3 s), `+"`stage→surge`"+` (front-of-stage crowding from 5 s, surge 0.7 and shoves from 30 s) and `+"`stage→surge 0.3`"+`. **Lead** = when the simulated truth first became dangerous (≥ 3 people at ≥ 1600 N/m or ≥ 5 people above 6/m², held 1 s) minus Pulse's first red; positive means Pulse warned first.
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
