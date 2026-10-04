package locate

import (
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Drivera0/crowd-crush/server/internal/crowdsim"
)

func nowNs() int64 { return time.Now().UnixNano() }

// seedsFromEnv reads LOCATE_SEEDS ("101-105" or "7"), default def.
func seedsFromEnv(def string) []int64 {
	s := os.Getenv("LOCATE_SEEDS")
	if s == "" {
		s = def
	}
	lo, hi, ok := strings.Cut(s, "-")
	a, _ := strconv.ParseInt(lo, 10, 64)
	b := a
	if ok {
		b, _ = strconv.ParseInt(hi, 10, 64)
	}
	var out []int64
	for v := a; v <= b; v++ {
		out = append(out, v)
	}
	return out
}

var (
	condIdeal     = condition{name: "ideal"}
	condRealistic = condition{name: "realistic", rl: crowdsim.Realism{GPS: 1, Carry: 1, Dropout: 1, Heading: 1, NoPos: true}}
	condGPS       = condition{name: "gps only", rl: crowdsim.Realism{GPS: 1, Heading: 1, NoPos: true}}
	condGPSHalf   = condition{name: "gps ×0.5 only", rl: crowdsim.Realism{GPS: 0.5, Heading: 1, NoPos: true}}
	condIndoor    = condition{name: "indoor, no GPS, walk in", rl: crowdsim.Realism{Carry: 1, Dropout: 1, Heading: 1, NoPos: true}, walkIn: true}
	condIndoorDR  = condition{name: "indoor, phone-side steps (model)", rl: crowdsim.Realism{Carry: 1, Dropout: 1, Heading: 1, NoPos: true, PhoneDR: 1}, walkIn: true}
	condWalkGPS   = condition{name: "realistic, walk in", rl: crowdsim.Realism{GPS: 1, Carry: 1, Dropout: 1, Heading: 1, NoPos: true}, walkIn: true}
)

var (
	varRaw = variant{name: "raw", raw: true}
	varEst = variant{name: "estimator"}
)

func bare(c *Config) {
	c.StandStill, c.MapConstraints, c.Spacing, c.Coop, c.OwnLinks = false, false, 0, false, false
}

// variants by name, for LOCATE_VARIANTS.
var variants = map[string]variant{
	"raw":       varRaw,
	"estimator": varEst,
	// The filter alone (GPS bias, entry spot, dead reckoning), then one
	// constraint at a time.
	"bare":           {name: "bare", mod: bare},
	"bare+still":     {name: "bare+still", mod: func(c *Config) { bare(c); c.StandStill = true }},
	"bare+map":       {name: "bare+map", mod: func(c *Config) { bare(c); c.MapConstraints = true }},
	"bare+spacing":   {name: "bare+spacing", mod: func(c *Config) { bare(c); c.Spacing = 0.4 }},
	"bare+steps-off": {name: "bare+steps-off", mod: func(c *Config) { bare(c); c.DeadReckon = false }},
	"lost-out":       {name: "lost-out"},
	"blind":          {name: "blind", mod: func(c *Config) { c.BlindWalk = true }},
	"no-map":         {name: "no-map", mod: func(c *Config) { c.MapConstraints = false }},
	"no-spacing":     {name: "no-spacing", mod: func(c *Config) { c.Spacing = 0 }},
	"no-still":       {name: "no-still", mod: func(c *Config) { c.StandStill = false }},
	"no-links":       {name: "no-links", mod: func(c *Config) { c.OwnLinks = false }},
	"no-coop":        {name: "no-coop", mod: func(c *Config) { c.Coop = false }},
	"no-steps":       {name: "no-steps", mod: func(c *Config) { c.DeadReckon = false }},
}

// applySet applies LOCATE_SET ("NearCorr=0.75,NearHits=3") to a config.
func applySet(c *Config) {
	s := os.Getenv("LOCATE_SET")
	if s == "" {
		return
	}
	v := reflect.ValueOf(c).Elem()
	for _, kv := range strings.Split(s, ",") {
		k, val, _ := strings.Cut(kv, "=")
		f := v.FieldByName(strings.TrimSpace(k))
		if !f.IsValid() {
			panic("no config field " + k)
		}
		x, err := strconv.ParseFloat(val, 64)
		if err != nil {
			panic(err)
		}
		switch f.Kind() {
		case reflect.Float64:
			f.SetFloat(x)
		case reflect.Int, reflect.Int64:
			f.SetInt(int64(x))
		case reflect.Bool:
			f.SetBool(x != 0)
		}
	}
}

func variantsFromEnv(def ...variant) []variant {
	s := os.Getenv("LOCATE_VARIANTS")
	if s == "" {
		return def
	}
	var out []variant
	for _, n := range strings.Split(s, ",") {
		v, ok := variants[strings.TrimSpace(n)]
		if !ok {
			panic("unknown variant " + n)
		}
		out = append(out, v)
	}
	return out
}

type simJob struct {
	c    condition
	sc   simScript
	seed int64
}

// parallel runs fn(0..n-1) on all CPUs.
func parallel(n int, fn func(i int)) {
	var wg sync.WaitGroup
	ch := make(chan int)
	for k := 0; k < runtime.NumCPU(); k++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range ch {
				fn(i)
			}
		}()
	}
	for i := 0; i < n; i++ {
		ch <- i
	}
	close(ch)
	wg.Wait()
}

// runJobs runs every job for the variants on all CPUs; out[j][v].
func runJobs(jobs []simJob, vs []variant, people int) [][]outcome {
	out := make([][]outcome, len(jobs))
	parallel(len(jobs), func(i int) { out[i] = runSim(jobs[i].sc, jobs[i].seed, jobs[i].c, vs, people) })
	return out
}

// summarise prints, per condition and variant, the pooled ground-truth
// comparison and the detection outcome.
func summarise(t *testing.T, jobs []simJob, vs []variant, outs [][]outcome, conds []condition) {
	for _, c := range conds {
		for vi, v := range vs {
			var walk, show metrics
			caught, pos, falseRed, look, before, danger, calmYellow := 0, 0, 0, 0, 0, 0, 0
			var leads []float64
			for ji, j := range jobs {
				if j.c.name != c.name {
					continue
				}
				o := outs[ji][vi]
				walk.add(&o.walk)
				show.add(&o.show)
				lead := 0.0
				if c.walkIn {
					lead = walkInS
				}
				switch j.sc.expect {
				case "red":
					pos++
					if o.redAt >= lead {
						caught++
					}
					if o.dangerAt >= 0 {
						danger++
						if o.redAt >= 0 && o.redAt < o.dangerAt {
							before++
						}
						if o.hasLead {
							leads = append(leads, o.lead)
						}
					}
				default:
					look++
					if o.redAt >= 0 {
						falseRed++
						if os.Getenv("LOCATE_WHY") != "" {
							t.Logf("   false red: %s / %s seed %d at %.1f s (script starts at %.0f) wave %v density %v", c.name, j.sc.name, j.seed, o.redAt, lead, o.waveRed, o.densRed)
						}
					}
					if j.sc.expect == "calm" && o.yellowAt >= 0 {
						calmYellow++
					}
				}
			}
			t.Logf("%-26s %-22s show: %s", c.name, v.name, show.row())
			if os.Getenv("LOCATE_TIMES") != "" {
				all := map[int][]float64{}
				for k, e := range walk.errT {
					all[k] = append(all[k], e...)
				}
				for k, e := range show.errT {
					all[k] = append(all[k], e...)
				}
				var ks []int
				for k := range all {
					ks = append(ks, k)
				}
				sort.Ints(ks)
				line := ""
				for _, k := range ks {
					line += fmt.Sprintf(" %ds:%.1f/%.1f/%.1f", k, meanOf(all[k]), quant(all[k], 0.5), quant(all[k], 0.95))
				}
				t.Logf("%-26s %-22s error by time (mean/median/p95 m):%s", "", "", line)
			}
			if c.walkIn {
				t.Logf("%-26s %-22s walk: %s", "", "", walk.row())
			}
			t.Logf("%-26s %-22s packing caught %d/%d, red before danger %d/%d (median lead %.1f s), false red %d/%d, calm→yellow %d",
				"", "", caught, pos, before, danger, quant(leads, 0.5), falseRed, look, calmYellow)
		}
	}
}

// TestEvalSim is the measurement behind docs/LOCATE.md (crowd simulation).
// LOCATE_EVAL=sim go test ./server/internal/locate -run EvalSim -v
func TestEvalSim(t *testing.T) {
	if os.Getenv("LOCATE_EVAL") != "sim" {
		t.Skip("set LOCATE_EVAL=sim")
	}
	conds := []condition{condIdeal, condRealistic, condGPS, condGPSHalf, condIndoor, condIndoorDR, condWalkGPS}
	if only := os.Getenv("LOCATE_COND"); only != "" {
		var cs []condition
		for _, c := range conds {
			if strings.Contains(c.name, only) {
				cs = append(cs, c)
			}
		}
		conds = cs
	}
	vs := variantsFromEnv(varRaw, varEst)
	scripts := simScripts
	if only := os.Getenv("LOCATE_SCRIPT"); only != "" {
		scripts = []simScript{scriptByName(only)}
	}
	var jobs []simJob
	for _, c := range conds {
		for _, sc := range scripts {
			for _, s := range seedsFromEnv("101-104") {
				jobs = append(jobs, simJob{c, sc, s})
			}
		}
	}
	start := time.Now()
	outs := runJobs(jobs, vs, 250)
	t.Logf("%d runs in %s", len(jobs), time.Since(start).Round(time.Second))
	summarise(t, jobs, vs, outs, conds)
	_ = fmt.Sprint
}

// TestEvalLong: a calm crowd standing for ten minutes with GPS. Does
// holding still let the fixes average down?
// LOCATE_EVAL=long go test ./server/internal/locate -run EvalLong -v
func TestEvalLong(t *testing.T) {
	if os.Getenv("LOCATE_EVAL") != "long" {
		t.Skip("set LOCATE_EVAL=long")
	}
	sc := simScript{name: "calm, 10 min", expect: "calm", dur: 600, act: func(*crowdsim.World, int, *rand.Rand) {}}
	conds := []condition{condGPS, condRealistic}
	vs := variantsFromEnv(varRaw, varEst, variants["no-still"], variants["no-links"])
	var jobs []simJob
	for _, c := range conds {
		for _, s := range seedsFromEnv("201-205") {
			jobs = append(jobs, simJob{c, sc, s})
		}
	}
	outs := runJobs(jobs, vs, 250)
	for _, c := range conds {
		for vi, v := range vs {
			all := map[int][]float64{}
			falseRed := 0
			for ji, j := range jobs {
				if j.c.name != c.name {
					continue
				}
				for k, e := range outs[ji][vi].show.errT {
					all[k/60*60] = append(all[k/60*60], e...)
				}
				if outs[ji][vi].redAt >= 0 {
					falseRed++
				}
			}
			line := ""
			for k := 0; k < 600; k += 60 {
				line += fmt.Sprintf(" %d–%d s: %.2f/%.2f", k, k+60, meanOf(all[k]), quant(all[k], 0.5))
			}
			t.Logf("%-12s %-10s false red %d | error by minute (mean/median m):%s", c.name, v.name, falseRed, line)
		}
	}
}
