package locate

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Drivera0/crowd-crush/server/internal/crowdsim"
	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/sim"
)

// The scripted scenarios of cmd/eval (internal/sim: line and crowd layouts,
// pushes and their look-alikes), run the way cmd/eval runs them, with the
// positions going through each variant. These give the headline numbers:
// false alarms, pushes caught, packing caught.

func expectOf(scenario string) string {
	switch scenario {
	case "wave", "wave-jump", "gather":
		return "red"
	case "shove", "walkpast", "procession":
		return "yellow-ok"
	}
	return "calm"
}

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

// scriptedPosErr collects the position error of every phone once a second.
func scriptedPosErr(a *arm, sc *sim.Scenario, n int, t float64) {
	for i := 0; i < n; i++ {
		id := fmt.Sprint(i)
		if !a.in[id] {
			continue
		}
		x, y := sc.PosAt(i, t)
		p := a.pos[id]
		a.out.show.errs = append(a.out.show.errs, math.Hypot(p[0]-x, p[1]-y))
	}
}

// runScripted runs one scripted scenario for every variant.
func runScripted(name, layout string, n int, dur float64, seed int64, rl crowdsim.Realism, vs []variant) []outcome {
	lay := sim.LineLayout(1, n)
	if layout == sim.LayoutCrowd {
		lay = sim.CrowdLayout(true)
	}
	sc, err := sim.NewLayout(name, n, seed, lay)
	if err != nil {
		panic(err)
	}
	cfg := detect.DefaultConfig()
	lc := DefaultConfig(cfg.VenueW, cfg.VenueH)
	arms := make([]*arm, len(vs))
	for i, v := range vs {
		arms[i] = newArm(v, cfg, lc)
		if lay.Kind == sim.LayoutLine {
			arms[i].det.SetZones(lineZones(cfg, sc.Cols))
		}
	}
	const clockErrMs = 25
	rng := rand.New(rand.NewSource(7))
	errs := make([]int64, n)
	for i := range errs {
		errs[i] = rng.Int63n(2*clockErrMs+1) - clockErrMs
	}
	evs := sc.Generate(t0Ms, dur)
	if rl.Ideal() {
		// Exact positions, re-sent every 500 ms when people move.
		var hello []crowdsim.Event
		for i := 0; i < n; i++ {
			x, y := sc.Pos(i)
			hello = append(hello, crowdsim.Event{Kind: crowdsim.EvHello, ID: fmt.Sprint(i), X: x, Y: y})
		}
		for _, a := range arms {
			a.feed(t0Ms, hello)
		}
		moves := sc.Moves()
		j := 0
		for now := int64(t0Ms); now <= t0Ms+int64(dur*1000); now += 250 {
			var out []crowdsim.Event
			if moves && (now-t0Ms)%500 == 0 {
				for i := 0; i < n; i++ {
					x, y := sc.PosAt(i, float64(now-t0Ms)/1000)
					out = append(out, crowdsim.Event{Kind: crowdsim.EvPos, ID: fmt.Sprint(i), X: x, Y: y})
				}
			}
			for j < len(evs) && evs[j].T <= now {
				e := evs[j]
				m := crowdsim.Event{Kind: crowdsim.EvMotion, ID: fmt.Sprint(e.Phone)}
				m.M.T, m.M.AX, m.M.AY, m.M.AZ, m.M.Rot = e.T+errs[e.Phone], e.AX, e.AY, e.AZ, e.Rot
				out = append(out, m)
				j++
			}
			for _, a := range arms {
				a.feed(now, out)
				a.step(now)
				if (now-t0Ms)%1000 == 0 {
					scriptedPosErr(a, sc, n, float64(now-t0Ms)/1000)
				}
			}
		}
	} else {
		sums := make([][]sim.Event, n)
		for _, e := range evs {
			sums[e.Phone] = append(sums[e.Phone], e)
		}
		rng := rand.New(rand.NewSource(seed*7919 + 13))
		devs := make([]*crowdsim.Device, n)
		stepHz := make([]float64, n)
		phase := make([]float64, n)
		shift := map[string]int64{}
		for i := range devs {
			id := fmt.Sprint(i)
			devs[i] = crowdsim.NewDevice(id, rl, seed*100_003+int64(i)*7+1)
			shift[id] = errs[i]
			stepHz[i] = 1.6 + 0.4*rng.Float64()
		}
		walk := name == "walk" || name == "march"
		env := crowdsim.Env{StartMs: t0Ms - 100, Bearing: simBearing}
		var out []crowdsim.Event
		for _, a := range arms {
			a.step(t0Ms)
		}
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
				raw := crowdsim.Raw{T: t, X: x, Y: y, BX: e.AX, BY: e.AY, BZ: e.AZ, Rot: e.Rot, Face: -math.Pi / 2, Dir: -math.Pi / 2}
				if walk {
					phase[i] += 2 * math.Pi * stepHz[i] * crowdsim.Dt
					raw.Gait, raw.Step = 1, phase[i]
				}
				out = d.Tick(raw, env, out)
			}
			for i := range out {
				if out[i].Kind == crowdsim.EvMotion {
					out[i].M.T += shift[out[i].ID]
				}
			}
			now := t0Ms + int64(k)*20
			for _, a := range arms {
				a.feed(now, out)
				if (now-t0Ms)%250 == 0 {
					a.step(now)
				}
				if (now-t0Ms)%1000 == 0 {
					scriptedPosErr(a, sc, n, t)
				}
			}
		}
	}
	outs := make([]outcome, len(arms))
	for i, a := range arms {
		outs[i] = a.out
	}
	return outs
}

type scriptedJob struct {
	cond             string
	rl               crowdsim.Realism
	name, layout     string
	n                int
	dur              float64
	seed             int64
	out              []outcome
	expect, scenario string
}

// TestEvalScripted is the measurement behind docs/LOCATE.md (scripted
// scenarios). LOCATE_EVAL=scripted go test ./server/internal/locate -run EvalScripted -v
func TestEvalScripted(t *testing.T) {
	if os.Getenv("LOCATE_EVAL") != "scripted" {
		t.Skip("set LOCATE_EVAL=scripted")
	}
	conds := []condition{
		{name: "ideal"},
		{name: "realistic", rl: crowdsim.Realism{GPS: 1, Carry: 1, Dropout: 1}},
		{name: "gps only", rl: crowdsim.Realism{GPS: 1}},
		{name: "gps ×0.5 only", rl: crowdsim.Realism{GPS: 0.5}},
	}
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
	var jobs []*scriptedJob
	for _, c := range conds {
		for _, name := range sim.Scenarios {
			if only := os.Getenv("LOCATE_SCRIPT"); only != "" && only != name {
				continue
			}
			for _, layout := range []string{sim.LayoutLine, sim.LayoutCrowd} {
				if name == "gather" && layout == sim.LayoutLine {
					continue
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
				for _, s := range seedsFromEnv("101-104") {
					jobs = append(jobs, &scriptedJob{cond: c.name, rl: c.rl, name: name, layout: layout, n: n, dur: dur, seed: s, expect: expectOf(name)})
				}
			}
		}
	}
	start := time.Now()
	parallel(len(jobs), func(i int) {
		j := jobs[i]
		j.out = runScripted(j.name, j.layout, j.n, j.dur, j.seed, j.rl, vs)
	})
	t.Logf("%d runs in %s", len(jobs), time.Since(start).Round(time.Second))
	for _, c := range conds {
		for vi, v := range vs {
			var errs []float64
			falseRed, look, push, pushN, pack, packN, calmY, calmN := 0, 0, 0, 0, 0, 0, 0, 0
			wrong := map[string]int{}
			for _, j := range jobs {
				if j.cond != c.name {
					continue
				}
				o := j.out[vi]
				errs = append(errs, o.show.errs...)
				red := o.redAt >= 0
				switch {
				case j.name == "wave" || j.name == "wave-jump":
					pushN++
					if red {
						push++
					}
				case j.expect == "red":
					packN++
					if red {
						pack++
					}
				default:
					look++
					if red {
						falseRed++
						wrong[j.name+"/"+j.layout]++
					}
					if j.expect == "calm" {
						calmN++
						if o.yellowAt >= 0 {
							calmY++
						}
					}
				}
			}
			t.Logf("%-16s %-12s false alarms %d/%d %v | pushes caught %d/%d | packing (gather) caught %d/%d | calm runs at yellow %d/%d | position error mean %.2f med %.2f p95 %.2f",
				c.name, v.name, falseRed, look, wrong, push, pushN, pack, packN, calmY, calmN, meanOf(errs), quant(errs, 0.5), quant(errs, 0.95))
		}
	}
}
