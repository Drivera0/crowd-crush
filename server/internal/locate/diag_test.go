package locate

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"strings"
	"testing"

	"github.com/Drivera0/crowd-crush/server/internal/crowdsim"
	"github.com/Drivera0/crowd-crush/server/internal/detect"
)

// TestDiagPDR: how well does dead reckoning follow simulated walkers, per
// carry state? LOCATE_EVAL=pdr
func TestDiagPDR(t *testing.T) {
	if os.Getenv("LOCATE_EVAL") != "pdr" {
		t.Skip("set LOCATE_EVAL=pdr")
	}
	for _, rl := range []crowdsim.Realism{
		{Heading: 1e-9, NoPos: true},
		{Heading: 1, NoPos: true},
		{Carry: 1, Heading: 1e-9, NoPos: true},
		{Carry: 1, Heading: 1, NoPos: true},
		{Carry: 1, Dropout: 1, Heading: 1, NoPos: true},
	} {
		type acc struct {
			n, walkTicks, seen, blind, falseWalk, stillTicks int
			angErr                                           []float64
			spdRatio                                         []float64
			endErr                                           []float64
			pathTrue, pathSlow, pathPDR, alongErr, crossErr  float64
			along, cross                                     []float64
		}
		by := map[int]*acc{}
		for _, seed := range seedsFromEnv("101-102") {
			cfg := detect.DefaultConfig()
			w, err := crowdsim.New(crowdsim.Config{W: cfg.VenueW, H: cfg.VenueH, People: 250, Participation: 0.6, Seed: seed,
				StartMs: t0Ms, Realism: rl, Bearing: simBearing, Entry: &crowdsim.Entry{X: entryX, Y: entryY, Over: walkInOver}})
			if err != nil {
				t.Fatal(err)
			}
			lc := DefaultConfig(cfg.VenueW, cfg.VenueH)
			lc.Bearing, lc.HasBearing = simBearing, true
			lc.Walls, lc.Stage = simGeom(w)
			lc.Entry = Entry{On: true, X: entryX, Y: entryY}
			lc.Coop, lc.Spacing = false, 0
			a := newArm(variant{name: "est"}, cfg, lc)
			for now := int64(t0Ms); now < t0Ms+110_000; {
				now += 50
				w.AdvanceTo(now)
				a.feed(now, w.Events())
				if (now-t0Ms)%250 != 0 {
					continue
				}
				a.lpos = a.loc.Step(now, nil)
				for _, ag := range w.Agents() {
					p := a.loc.phones[ag.PhoneID()]
					if p == nil || !p.placed {
						continue
					}
					c := by[ag.Carry()]
					if c == nil {
						c = &acc{}
						by[ag.Carry()] = c
					}
					sp := math.Hypot(ag.VX, ag.VY)
					c.pathTrue += sp * 0.25
					if sp <= 0.6 {
						c.pathSlow += sp * 0.25
					}
					if p.gait.g == gaitWalk {
						c.pathPDR += p.gait.speed * 0.25
					}
					if sp > 0.6 {
						c.walkTicks++
						switch p.gait.g {
						case gaitWalk:
							c.seen++
							ang := math.Acos(math.Max(-1, math.Min(1, (p.gait.ux*ag.VX+p.gait.uy*ag.VY)/sp)))
							c.angErr = append(c.angErr, ang*180/math.Pi)
							c.spdRatio = append(c.spdRatio, p.gait.speed/sp)
						case gaitBlind:
							c.blind++
						}
					} else if sp < 0.1 {
						c.stillTicks++
						if p.gait.g != gaitStill {
							c.falseWalk++
						}
					}
				}
			}
			for _, ag := range w.Agents() {
				p := a.loc.phones[ag.PhoneID()]
				if p == nil || !p.placed {
					continue
				}
				by[ag.Carry()].endErr = append(by[ag.Carry()].endErr, math.Hypot(p.kf.s[0]-ag.X, p.kf.s[1]-ag.Y))
				tx, ty := ag.X-entryX, ag.Y-entryY
				tl := math.Hypot(tx, ty)
				ex, ey := p.kf.s[0]-entryX, p.kf.s[1]-entryY
				by[ag.Carry()].along = append(by[ag.Carry()].along, (ex*tx+ey*ty)/tl/tl)
				by[ag.Carry()].cross = append(by[ag.Carry()].cross, math.Abs(ex*ty-ey*tx)/tl/tl)
			}
		}
		t.Logf("carry %.0f heading %.0g dropout %.0f", rl.Carry, rl.Heading, rl.Dropout)
		for c := 0; c < 4; c++ {
			a := by[c]
			if a == nil {
				continue
			}
			t.Logf("  %-7s walking ticks seen %3.0f%% blind %3.0f%% | dir err med %4.0f° p90 %4.0f° | speed ratio med %.2f | false walk %4.1f%% of still | end err med %.2f p90 %.2f (n=%d) | path: slow share %.2f, pdr/true %.2f | end along ratio med %.2f, cross ratio med %.2f",
				crowdsim.CarryNames[c], 100*ratio(a.seen, a.walkTicks), 100*ratio(a.blind, a.walkTicks),
				quant(a.angErr, 0.5), quant(a.angErr, 0.9), quant(a.spdRatio, 0.5), 100*ratio(a.falseWalk, a.stillTicks),
				quant(a.endErr, 0.5), quant(a.endErr, 0.9), len(a.endErr), a.pathSlow/a.pathTrue, a.pathPDR/a.pathTrue, quant(a.along, 0.5), quant(a.cross, 0.5))
		}
	}
}

// TestDiagTrace prints one walker's gait analysis over time. LOCATE_EVAL=trace
func TestDiagTrace(t *testing.T) {
	if os.Getenv("LOCATE_EVAL") != "trace" {
		t.Skip("set LOCATE_EVAL=trace")
	}
	cfg := detect.DefaultConfig()
	rl := crowdsim.Realism{Heading: 1e-9, NoPos: true}
	w, _ := crowdsim.New(crowdsim.Config{W: cfg.VenueW, H: cfg.VenueH, People: 250, Participation: 0.6, Seed: 101,
		StartMs: t0Ms, Realism: rl, Bearing: simBearing, Entry: &crowdsim.Entry{X: entryX, Y: entryY, Over: walkInOver}})
	lc := DefaultConfig(cfg.VenueW, cfg.VenueH)
	lc.Bearing, lc.HasBearing = simBearing, true
	lc.Walls, lc.Stage = simGeom(w)
	lc.Entry = Entry{On: true, X: entryX, Y: entryY}
	lc.Coop, lc.Spacing = false, 0
	a := newArm(variant{name: "est"}, cfg, lc)
	watch := map[string]int{}
	for now := int64(t0Ms); now < t0Ms+60_000; {
		now += 50
		w.AdvanceTo(now)
		a.feed(now, w.Events())
		if (now-t0Ms)%250 != 0 {
			continue
		}
		a.loc.Step(now, nil)
		for _, ag := range w.Agents() {
			id := ag.PhoneID()
			p := a.loc.phones[id]
			if p == nil {
				continue
			}
			if _, ok := watch[id]; !ok && len(watch) < 3 {
				watch[id] = 0
			}
			if _, ok := watch[id]; !ok || watch[id] > 120 {
				continue
			}
			watch[id]++
			sp := math.Hypot(ag.VX, ag.VY)
			t.Logf("%s t=%5.2f sp=%.2f true(%.1f,%.1f) est(%.1f,%.1f) gait=%d f=%.1f amp=%.2f quad=%.2f spd=%.2f dir(%.2f,%.2f) truedir(%.2f,%.2f)",
				id, float64(now-t0Ms)/1000, sp, ag.X, ag.Y, p.kf.s[0], p.kf.s[1], p.gait.g, p.gait.freq, p.gait.amp, p.gait.quad, p.gait.speed,
				p.gait.ux, p.gait.uy, ag.VX/math.Max(sp, 1e-3), ag.VY/math.Max(sp, 1e-3))
		}
	}
}

// TestDiagSplit: where does the dead-reckoning error come from? Integrates
// from the true start with (a) PDR speed and true direction, (b) true speed
// and PDR direction, (c) both PDR. LOCATE_EVAL=split
func TestDiagSplit(t *testing.T) {
	if os.Getenv("LOCATE_EVAL") != "split" {
		t.Skip("set LOCATE_EVAL=split")
	}
	cfg := detect.DefaultConfig()
	for _, rl := range []crowdsim.Realism{{Heading: 1e-9, NoPos: true}, {Heading: 1, NoPos: true}} {
		w, _ := crowdsim.New(crowdsim.Config{W: cfg.VenueW, H: cfg.VenueH, People: 250, Participation: 0.6, Seed: 101,
			StartMs: t0Ms, Realism: rl, Bearing: simBearing, Entry: &crowdsim.Entry{X: entryX, Y: entryY, Over: walkInOver}})
		lc := DefaultConfig(cfg.VenueW, cfg.VenueH)
		lc.Bearing, lc.HasBearing = simBearing, true
		lc.Entry = Entry{On: true, X: entryX, Y: entryY}
		lc.Coop, lc.Spacing, lc.MapConstraints = false, 0, false
		a := newArm(variant{name: "est"}, cfg, lc)
		type tr struct{ ax, ay, bx, by, cx, cy, sx, sy, path, fx, fy float64 }
		ts := map[string]*tr{}
		var fv, pf, pfs []float64
		for now := int64(t0Ms); now < t0Ms+110_000; {
			now += 50
			w.AdvanceTo(now)
			a.feed(now, w.Events())
			if (now-t0Ms)%250 != 0 {
				continue
			}
			a.loc.Step(now, nil)
			for _, ag := range w.Agents() {
				p := a.loc.phones[ag.PhoneID()]
				if p == nil {
					continue
				}
				q := ts[p.id]
				if q == nil {
					q = &tr{sx: ag.X, sy: ag.Y}
					ts[p.id] = q
				}
				sp := math.Hypot(ag.VX, ag.VY)
				q.path += sp * 0.25
				if p.gait.g == gaitWalk && sp > 0.6 {
					fx, fy := math.Cos(ag.Facing()), math.Sin(ag.Facing())
					ang := func(ax, ay, bx, by float64) float64 {
						return math.Atan2(ax*by-ay*bx, ax*bx+ay*by) * 180 / math.Pi
					}
					fv = append(fv, math.Abs(ang(fx, fy, ag.VX, ag.VY)))
					pf = append(pf, math.Abs(ang(p.gait.ux, p.gait.uy, fx, fy)))
					pfs = append(pfs, ang(p.gait.ux, p.gait.uy, fx, fy))
				}
				if p.gait.g == gaitWalk {
					if sp > 1e-3 {
						q.ax += p.gait.speed * 0.25 * ag.VX / sp
						q.ay += p.gait.speed * 0.25 * ag.VY / sp
					}
					q.fx += sp * 0.25 * math.Cos(ag.Facing())
					q.fy += sp * 0.25 * math.Sin(ag.Facing())
					q.bx += sp * 0.25 * p.gait.ux
					q.by += sp * 0.25 * p.gait.uy
					q.cx += p.gait.speed * 0.25 * p.gait.ux
					q.cy += p.gait.speed * 0.25 * p.gait.uy
				}
			}
		}
		t.Logf("|facing−velocity| med %.0f° p90 %.0f° | |pdr−facing| med %.0f° p90 %.0f° | signed pdr−facing mean %.1f° med %.1f°",
			quant(fv, 0.5), quant(fv, 0.9), quant(pf, 0.5), quant(pf, 0.9), meanOf(pfs), quant(pfs, 0.5))
		var ea, eb, ec, ef, path, eo []float64
		for _, ag := range w.Agents() {
			q := ts[ag.PhoneID()]
			if q == nil {
				continue
			}
			dx, dy := ag.X-q.sx, ag.Y-q.sy
			ea = append(ea, math.Hypot(q.ax-dx, q.ay-dy))
			eo = append(eo, math.Hypot(q.fx-dx, q.fy-dy))
			eb = append(eb, math.Hypot(q.bx-dx, q.by-dy))
			ec = append(ec, math.Hypot(q.cx-dx, q.cy-dy))
			p := a.loc.phones[ag.PhoneID()]
			ef = append(ef, math.Hypot(p.kf.s[0]-ag.X, p.kf.s[1]-ag.Y))
			path = append(path, q.path)
		}
		t.Logf("true speed along true facing: %.2f/%.2f", quant(eo, 0.5), quant(eo, 0.9))
		t.Logf("heading %.0g: path med %.1f m | end error med/p90: PDR speed+true dir %.2f/%.2f | true speed+PDR dir %.2f/%.2f | PDR both %.2f/%.2f | filter (from entry spot) %.2f/%.2f",
			rl.Heading, quant(path, 0.5), quant(ea, 0.5), quant(ea, 0.9), quant(eb, 0.5), quant(eb, 0.9), quant(ec, 0.5), quant(ec, 0.9), quant(ef, 0.5), quant(ef, 0.9))
	}
}

var rhythmMax = 9.0

func init() {
	if v := os.Getenv("LOCATE_RHYTHM"); v != "" {
		fmt.Sscan(v, &rhythmMax)
	}
}

func init() {
	if v := os.Getenv("LOCATE_OFFALPHA"); v != "" {
		fmt.Sscan(v, &offAlpha)
	}
}

// TestDiagEntry: who is the estimator holding at the entry spot? LOCATE_EVAL=entry
func TestDiagEntry(t *testing.T) {
	if os.Getenv("LOCATE_EVAL") != "entry" {
		t.Skip("set LOCATE_EVAL=entry")
	}
	cfg := detect.DefaultConfig()
	cfg.Participation = 0.6
	c := condIndoor
	if strings.Contains(os.Getenv("LOCATE_COND"), "phone") {
		c = condIndoorDR
	}
	w, _ := crowdsim.New(crowdsim.Config{W: cfg.VenueW, H: cfg.VenueH, People: 250, Participation: 0.6, Seed: seedsFromEnv("101")[0],
		StartMs: t0Ms, Realism: c.rl, Bearing: simBearing, Entry: &crowdsim.Entry{X: entryX, Y: entryY, Over: walkInOver}})
	lc := DefaultConfig(cfg.VenueW, cfg.VenueH)
	lc.Bearing, lc.HasBearing = simBearing, true
	lc.Walls, lc.Stage = simGeom(w)
	lc.Entry = Entry{On: true, X: entryX, Y: entryY}
	a := newArm(variantsFromEnv(varEst)[0], cfg, lc)
	for now := int64(t0Ms); now < t0Ms+270_000; {
		now += 50
		w.AdvanceTo(now)
		a.feed(now, w.Events())
		if (now-t0Ms)%250 != 0 {
			continue
		}
		a.step(now)
		if (now-t0Ms)%10_000 != 0 {
			continue
		}
		nEst, nTrue, nTrue3, people := 0, 0, 0, 0
		gs := map[gait]int{}
		carry := map[int]int{}
		var sig, terr []float64
		for _, ag := range w.Agents() {
			if math.Hypot(ag.X-entryX, ag.Y-entryY) <= 1.5 {
				people++
			}
			p := a.loc.phones[ag.PhoneID()]
			if p == nil || !p.placed {
				continue
			}
			td := math.Hypot(ag.X-entryX, ag.Y-entryY)
			if td <= 1.5 {
				nTrue++
			}
			if td <= 3 {
				nTrue3++
			}
			if math.Hypot(p.x-entryX, p.y-entryY) <= 1.5 && p.acc <= lc.LostSigma {
				nEst++
				gs[p.gait.g]++
				carry[ag.Carry()]++
				sig = append(sig, p.acc)
				terr = append(terr, td)
			}
		}
		est := 0.0
		for _, cl := range a.cl {
			if cl.Est > est {
				est = cl.Est
				nt, np := 0, 0
				for _, ag := range w.Agents() {
					if math.Hypot(ag.X-cl.PeakX, ag.Y-cl.PeakY) <= 1.5 {
						nt++
					}
				}
				var accs []float64
				for _, pt := range a.pts {
					if math.Hypot(pt.X-cl.PeakX, pt.Y-cl.PeakY) <= 1.5 {
						np++
						accs = append(accs, pt.Acc)
					}
				}
				nl := 0
				for _, l := range a.loc.lastLinks {
					if math.Hypot(l.a.x-cl.PeakX, l.a.y-cl.PeakY) <= 1.5 || math.Hypot(l.b.x-cl.PeakX, l.b.y-cl.PeakY) <= 1.5 {
						nl++
					}
				}
				t.Logf("   densest: at (%.1f, %.1f) est %.1f/m² level %s, %d phones within 1.5 m (acc med %.1f max %.1f), truly %d people = %.1f/m², cluster acc %.1f count %d, links there %d of %d",
					cl.PeakX, cl.PeakY, cl.Est, cl.Level, np, quant(accs, 0.5), quant(accs, 0.99), nt, float64(nt)/7.07, cl.Acc, cl.Count, nl, len(a.loc.lastLinks))
			}
		}
		t.Logf("t=%3d: phones estimated within 1.5 m of the entry %d (truly %d, within 3 m %d; people truly within 1.5 m: %d) gait %v carry %v σ med %.1f true dist med %.1f p90 %.1f | max cluster est %.1f level %s",
			(now-t0Ms)/1000, nEst, nTrue, nTrue3, people, gs, carry, quant(sig, 0.5), quant(terr, 0.5), quant(terr, 0.9), est, a.out.maxLevel)
	}
}

// TestDiagNear: does shared motion say "near"? Rotation-free correlation of
// every eligible pair against the true distance, by script phase.
// LOCATE_EVAL=near LOCATE_SCRIPT="stage→surge"
func TestDiagNear(t *testing.T) {
	if os.Getenv("LOCATE_EVAL") != "near" {
		t.Skip("set LOCATE_EVAL=near")
	}
	name := os.Getenv("LOCATE_SCRIPT")
	if name == "" {
		name = "stage→surge"
	}
	sc := scriptByName(name)
	for _, c := range []condition{condGPS, condRealistic} {
		cfg := detect.DefaultConfig()
		cfg.Participation = 0.6
		w, _ := crowdsim.New(crowdsim.Config{W: cfg.VenueW, H: cfg.VenueH, People: 250, Participation: 0.6, Seed: 101,
			StartMs: t0Ms, Realism: c.rl, Bearing: simBearing})
		lc := DefaultConfig(cfg.VenueW, cfg.VenueH)
		lc.Bearing, lc.HasBearing = simBearing, true
		lc.Walls, lc.Stage = simGeom(w)
		lc.OwnLinks = false
		a := newArm(variant{name: "est"}, cfg, lc)
		a.feed(t0Ms, w.Events())
		rng := rand.New(rand.NewSource(101))
		bins := []float64{1.1, 2.2, 5, 1e9}
		thr := []float64{0.5, 0.6, 0.7, 0.8, 0.9}
		type cell struct {
			n   int
			hit [5]int
		}
		stats := map[string]*[4]cell{}
		elig := map[string][2]int{}
		for now := int64(t0Ms); now < t0Ms+int64(sc.dur*1000); {
			now += 50
			sec := int((now - t0Ms) / 1000)
			if (now-t0Ms)%1000 == 0 {
				sc.act(w, sec, rng)
			}
			w.AdvanceTo(now)
			a.feed(now, w.Events())
			if (now-t0Ms)%250 != 0 {
				continue
			}
			a.step(now)
			if (now-t0Ms)%1000 != 0 || sec < 6 {
				continue
			}
			phase := "5–30 s"
			if sec >= 30 {
				phase = "30–55 s"
			}
			if sec >= 55 {
				phase = "55–80 s"
			}
			st := stats[phase]
			if st == nil {
				st = &[4]cell{}
				stats[phase] = st
			}
			var el []*phone
			tp := map[string][2]float64{}
			for _, ag := range w.Agents() {
				p := a.loc.phones[ag.PhoneID()]
				if p == nil || !p.placed {
					continue
				}
				tp[p.id] = [2]float64{ag.X, ag.Y}
				ok := !p.gone && !p.walking && p.n >= trN/2 && p.trace(now-trLagMs, lc.HandlingRot) && p.trRMS >= lc.NearMinRMS && p.rhythm() < rhythmMax
				e := elig[phase]
				e[1]++
				if ok {
					e[0]++
					el = append(el, p)
				}
				elig[phase] = e
			}
			for i := range el {
				for j := i + 1; j < len(el); j++ {
					pa, pb := tp[el[i].id], tp[el[j].id]
					d := math.Hypot(pa[0]-pb[0], pa[1]-pb[1])
					b := 0
					for d > bins[b] {
						b++
					}
					r := rotCorr(el[i], el[j])
					st[b].n++
					for k, th := range thr {
						if r >= th {
							st[b].hit[k]++
						}
					}
				}
			}
		}
		t.Logf("%s, %s", c.name, name)
		for _, ph := range []string{"5–30 s", "30–55 s", "55–80 s"} {
			st := stats[ph]
			if st == nil {
				continue
			}
			t.Logf("  %s: eligible phones %.0f%%", ph, 100*ratio(elig[ph][0], elig[ph][1]))
			for b, name := range []string{"≤ 1.1 m", "1.1–2.2 m", "2.2–5 m", "> 5 m"} {
				c := st[b]
				s := ""
				for k, th := range thr {
					s += fmt.Sprintf("  r≥%.1f: %5.1f%%", th, 100*ratio(c.hit[k], c.n))
				}
				t.Logf("    %-10s pairs %7d %s", name, c.n, s)
			}
		}
	}
}

// TestDiagIdealCount: which ideal phones does the estimator arm not count?
func TestDiagIdealCount(t *testing.T) {
	if os.Getenv("LOCATE_EVAL") != "idealcount" {
		t.Skip()
	}
	cfg := detect.DefaultConfig()
	cfg.Participation = 0.6
	w, _ := crowdsim.New(crowdsim.Config{W: cfg.VenueW, H: cfg.VenueH, People: 250, Participation: 0.6, Seed: 101, StartMs: t0Ms, Bearing: simBearing})
	lc := DefaultConfig(cfg.VenueW, cfg.VenueH)
	lc.Walls, lc.Stage = simGeom(w)
	raw := newArm(varRaw, cfg, lc)
	est := newArm(varEst, cfg, lc)
	ev := w.Events()
	raw.feed(t0Ms, ev)
	est.feed(t0Ms, ev)
	seen := map[string]bool{}
	sc := scriptByName(os.Getenv("LOCATE_SCRIPT"))
	rng := rand.New(rand.NewSource(101))
	for now := int64(t0Ms); now < t0Ms+80_000; {
		now += 50
		if (now-t0Ms)%1000 == 0 {
			sc.act(w, int((now-t0Ms)/1000), rng)
		}
		w.AdvanceTo(now)
		ev := w.Events()
		raw.feed(now, ev)
		est.feed(now, ev)
		if (now-t0Ms)%250 != 0 {
			continue
		}
		raw.step(now)
		est.step(now)
		in := map[string]bool{}
		for _, p := range est.pts {
			in[p.ID] = true
		}
		for _, p := range raw.pts {
			if !in[p.ID] && !seen[p.ID] {
				seen[p.ID] = true
				q := est.loc.phones[p.ID]
				t.Logf("t=%.2f %s missing: phone %v placed %v gone %v acc %.2f inDet %v", float64(now-t0Ms)/1000, p.ID, q != nil, q != nil && q.placed, q != nil && q.gone, est.acc[p.ID], est.in[p.ID])
			}
		}
	}
}
