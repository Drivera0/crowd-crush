package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Drivera0/crowd-crush/server/internal/crowdsim"
	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/hub"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

const simT0 = int64(1_700_000_000_000)

// simRunner drives an App's simulation on a fake clock: physics every
// 50 ms (as the Run loop does), the detector every 250 ms.
type simRunner struct {
	t   testing.TB
	a   *App
	now int64
}

func startSim(t testing.TB, people int, seed int64) *simRunner {
	t.Helper()
	a := New(Options{Detect: detect.DefaultConfig(), RecordingsDir: t.TempDir()})
	if err := a.startSimAt(SimStart{People: people, Participation: 0.6, Seed: seed}, simT0); err != nil {
		t.Fatal(err)
	}
	return &simRunner{t: t, a: a, now: simT0}
}

func (r *simRunner) sec() float64 { return float64(r.now-simT0) / 1000 }

// until runs to sim second end, calling every once per simulated second.
func (r *simRunner) until(end float64, every func(s int)) {
	for r.sec() < end-1e-9 {
		r.now += 50
		if every != nil && (r.now-simT0)%1000 == 0 {
			every(int(r.sec()))
		}
		r.a.simTick(r.now)
		if (r.now-simT0)%int64(DetectEvery/1e6) == 0 {
			r.a.detectTick(r.now)
		}
	}
}

func (r *simRunner) act(ac crowdsim.Action) {
	r.t.Helper()
	if err := r.a.SimAction(ac); err != nil {
		r.t.Fatal(err)
	}
}

// reds lists the incidents that reached red so far (not tests), in the
// order they went red (an alert's T is when it reached its level).
func (r *simRunner) reds() []protocol.Alert {
	r.a.mu.Lock()
	defer r.a.mu.Unlock()
	var out []protocol.Alert
	for _, al := range r.a.alerts {
		if al.Level == protocol.LevelRed && !al.Test {
			out = append(out, al)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].T < out[j].T })
	return out
}

func fp(v float64) *float64 { return &v }

// crushScript: front-of-stage crowding from 5 s, a surge (0.7) at 30 s and
// a shove from behind every 3 s after it.
func crushScript(r *simRunner, rng *rand.Rand) func(s int) {
	return func(s int) {
		if s == 5 {
			r.act(crowdsim.Action{Type: "stage"})
		}
		if s == 30 {
			r.act(crowdsim.Action{Type: "surge", Strength: fp(0.7)})
		}
		if s > 30 && s%3 == 0 {
			dx := []float64{1, -1, 0}[rng.Intn(3)]
			r.act(crowdsim.Action{Type: "shove", X: fp(6 + 12*rng.Float64()), Y: fp(3 + 3*rng.Float64()), DX: fp(dx), DY: fp(-1)})
		}
	}
}

// TestSimCrushRaisesRed: surge plus repeated shoves reach a red alert, and
// the lead time is computed from the two recorded times.
func TestSimCrushRaisesRed(t *testing.T) {
	t.Parallel()
	r := startSim(t, 250, 1)
	r.until(70, crushScript(r, rand.New(rand.NewSource(1))))
	reds := r.reds()
	if len(reds) == 0 {
		t.Fatal("no red alert during a surge with shoves")
	}
	st := r.a.SimStatus()
	tr := st.Truth
	if tr.DangerAt == nil || tr.AlertAt == nil || tr.LeadSeconds == nil {
		t.Fatalf("truth incomplete: %+v", tr)
	}
	first := float64(reds[0].T-simT0) / 1000
	if math.Abs(*tr.AlertAt-first) > 0.051 { // alertAt is rounded to 0.1 s
		t.Errorf("alertAt %.2f, first red alert at %.2f", *tr.AlertAt, first)
	}
	if want := math.Round((*tr.DangerAt-*tr.AlertAt)*10) / 10; math.Abs(*tr.LeadSeconds-want) > 1e-9 {
		t.Errorf("lead %.1f, want dangerAt − alertAt = %.1f", *tr.LeadSeconds, want)
	}
	if *tr.DangerAt < 30 {
		t.Errorf("truth dangerous at %.1f s, before the surge", *tr.DangerAt)
	}
	t.Logf("first red: %s %s at %.1f s; truth dangerous at %.1f s; lead %+.1f s; max density %.1f, pressure %.0f N/m, %d crushing",
		reds[0].Kind, reds[0].Zone, first, *tr.DangerAt, *tr.LeadSeconds, tr.MaxDensity, tr.MaxPressure, tr.Crushing)
}

// TestSimCalmStaysCalm: a minute of concert idling raises no red alert.
func TestSimCalmStaysCalm(t *testing.T) {
	t.Parallel()
	r := startSim(t, 250, 2)
	r.until(60, nil)
	if reds := r.reds(); len(reds) > 0 {
		t.Fatalf("red alert while calm: %+v", reds[0])
	}
	st := r.a.SimStatus()
	if st.Truth.DangerAt != nil || st.Truth.AlertAt != nil || st.Truth.LeadSeconds != nil {
		t.Errorf("calm truth: %+v", st.Truth)
	}
	r.a.mu.Lock()
	for _, z := range r.a.sim.p.last.Zones {
		if z.Level == protocol.LevelRed {
			t.Errorf("zone %s red", z.ID)
		}
	}
	r.a.mu.Unlock()
}

// TestSimAttractNoWave: a group forming (not pushing) raises no red wave alert.
func TestSimAttractNoWave(t *testing.T) {
	t.Parallel()
	r := startSim(t, 250, 3)
	r.until(60, func(s int) {
		if s == 5 {
			r.act(crowdsim.Action{Type: "attract", X: fp(12), Y: fp(10)})
		}
	})
	for _, al := range r.reds() {
		if al.Kind == protocol.KindWave {
			t.Fatalf("red wave alert while a group gathered: %+v", al)
		}
	}
}

// TestSimLeadNoAlert: no red alert means no alertAt and no lead.
func TestSimLeadOnlyWithBoth(t *testing.T) {
	r := startSim(t, 60, 4)
	r.a.mu.Lock()
	r.a.sim.alertAt = 12.34
	r.a.mu.Unlock()
	st := r.a.SimStatus()
	if st.Truth.AlertAt == nil || *st.Truth.AlertAt != 12.3 || st.Truth.LeadSeconds != nil {
		t.Fatalf("alert without danger: %+v", st.Truth)
	}
}

func TestSimAPI(t *testing.T) {
	a, srv := testServer(t, Options{})
	var st protocol.SimStatus
	if code := do(t, "GET", srv.URL+"/api/sim", nil, &st); code != 200 || st.Running || len(st.Exits) != 4 || len(st.Walls) == 0 {
		t.Fatalf("idle status %d %+v", code, st)
	}
	if code := do(t, "POST", srv.URL+"/api/sim/action", `{"type":"calm"}`, nil); code != http.StatusBadRequest {
		t.Errorf("action while stopped: %d", code)
	}
	if code := do(t, "POST", srv.URL+"/api/sim/stop", nil, nil); code != http.StatusConflict {
		t.Errorf("stop while stopped: %d", code)
	}
	for _, body := range []string{`{"people":0.5}`, `{"people":5000}`, `{"participation":2}`, `{"scenario":"rave"}`, `nope`,
		`{"realism":"perfect"}`, `{"imperfections":{"gps":7}}`, `{"realism":"harsh","imperfections":{"carry":-1}}`} {
		if code := do(t, "POST", srv.URL+"/api/sim/start", body, nil); code != http.StatusBadRequest {
			t.Errorf("start %s: %d", body, code)
		}
	}
	var out map[string]string
	if code := do(t, "POST", srv.URL+"/api/sim/start", `{"people":80,"participation":0.5,"scenario":"concert"}`, &out); code != 200 || out["mode"] != "sim" {
		t.Fatalf("start: %d %v", code, out)
	}
	if code := do(t, "POST", srv.URL+"/api/sim/start", `{"people":80}`, nil); code != http.StatusConflict {
		t.Errorf("start twice: %d", code)
	}
	bad := map[string]string{
		"unknown":        `{"type":"moonwalk"}`,
		"no type":        `{}`,
		"attract no xy":  `{"type":"attract"}`,
		"shove no dir":   `{"type":"shove","x":5,"y":5}`,
		"surge strength": `{"type":"surge","strength":3}`,
		"exit unknown":   `{"type":"exit","id":"exit-zz","open":true}`,
		"exit no open":   `{"type":"exit","id":"exit-bl"}`,
		"spawn no n":     `{"type":"spawn","x":5,"y":5}`,
		"outside venue":  `{"type":"spawn","x":50,"y":5,"n":3}`,
		"not json":       `{"type":`,
	}
	for name, body := range bad {
		req, _ := http.NewRequest("POST", srv.URL+"/api/sim/action", strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var e map[string]string
		json.NewDecoder(resp.Body).Decode(&e)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || e["error"] == "" {
			t.Errorf("%s: %d %v", name, resp.StatusCode, e)
		}
	}
	good := []string{
		`{"type":"stage"}`, `{"type":"surge","strength":0.7}`, `{"type":"attract","x":12,"y":10}`,
		`{"type":"shove","x":12,"y":4,"dx":0,"dy":-1}`, `{"type":"exit","id":"exit-bl","open":false}`,
		`{"type":"spawn","x":12,"y":12,"n":10}`, `{"type":"disperse"}`, `{"type":"dance"}`, `{"type":"intermission"}`,
		`{"type":"calm"}`,
	}
	for _, body := range good {
		if code := do(t, "POST", srv.URL+"/api/sim/action", body, nil); code != 200 {
			t.Errorf("%s: %d", body, code)
		}
	}
	st = protocol.SimStatus{}
	do(t, "GET", srv.URL+"/api/sim", nil, &st)
	if !st.Running || st.People != 90 || st.Participation != 0.5 || st.Action != "calm" || st.Truth == nil || st.Exits[0].Open {
		t.Errorf("running status %+v", st)
	}
	// Snapshot shows the sim pipeline with bodies.
	a.simTick(simT0) // no-op time-wise; StartSim used the real clock
	a.mu.Lock()
	snap := a.snapshotLocked(a.sim.startMs + 1000)
	a.mu.Unlock()
	if snap.Mode != "sim" || snap.Sim == nil || len(snap.Sim.Bodies) != 90 {
		t.Fatalf("snapshot mode %s sim %v", snap.Mode, snap.Sim != nil)
	}
	b := snap.Sim.Bodies[0]
	if b[0] != math.Round(b[0]*100)/100 || b[2] != math.Round(b[2]) || (b[3] != 0 && b[3] != 1) {
		t.Errorf("body not rounded: %v", b)
	}
	if code := do(t, "POST", srv.URL+"/api/sim/stop", nil, &out); code != 200 || out["mode"] != "live" {
		t.Fatalf("stop: %d %v", code, out)
	}
	a.mu.Lock()
	snap = a.snapshotLocked(simT0)
	a.mu.Unlock()
	if snap.Mode != "live" || snap.Sim != nil {
		t.Errorf("after stop: mode %s", snap.Mode)
	}
}

// TestSimPhonesThroughPipeline: simulated phones are synced pipeline
// phones, live phones keep their own pipeline, and a recording made during
// the sim holds the sim phones' readings.
func TestSimPhonesThroughPipeline(t *testing.T) {
	r := startSim(t, 60, 5)
	r.a.PhoneHello("live-1", 3, 3, "iPhone")
	name, err := r.a.StartRecording("sim-test")
	if err != nil {
		t.Fatal(err)
	}
	r.until(5, nil)
	r.a.StopRecording()
	r.a.mu.Lock()
	snap := r.a.snapshotLocked(r.now)
	_, liveHasSim := r.a.live.meta["sim-0000"]
	_, simHasLive := r.a.sim.p.meta["live-1"]
	r.a.mu.Unlock()
	if liveHasSim || simHasLive {
		t.Fatal("live and sim pipelines mixed")
	}
	ok := 0
	for _, n := range snap.Nodes {
		if !strings.HasPrefix(n.ID, "sim-") || n.UA != "sim" {
			t.Fatalf("unexpected node %+v", n)
		}
		if n.Status == protocol.StatusConnecting || n.Status == protocol.StatusStale || n.RTT <= 0 {
			t.Fatalf("sim phone not synced/streaming: %+v", n)
		}
		ok++
	}
	if ok == 0 {
		t.Fatal("no sim nodes")
	}
	f, err := os.Open(filepath.Join(r.a.opt.RecordingsDir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	kinds := map[string]int{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var rec struct {
			K, ID string
		}
		json.Unmarshal(sc.Bytes(), &rec)
		if strings.HasPrefix(rec.ID, "sim-") {
			kinds[rec.K]++
		}
	}
	if kinds["hello"] == 0 || kinds["m"] < 40*ok {
		t.Errorf("recording has sim records %v for %d phones", kinds, ok)
	}
}

// TestSimRealism: the realism option of POST /api/sim/start. Ideal (the
// default) phones are placed by hand and exact; realistic ones are GPS
// nodes with an accuracy, some metres off, some silent or outside, and
// their summaries carry a gravity vector; an imperfection can be switched
// on alone.
func TestSimRealism(t *testing.T) {
	type res struct {
		gps, manual, stale, outside int
		medErr                      float64
	}
	run := func(req SimStart) res {
		t.Helper()
		a := New(Options{Detect: detect.DefaultConfig(), RecordingsDir: t.TempDir()})
		req.People, req.Participation, req.Seed = 200, 0.6, 3
		if err := a.startSimAt(req, simT0); err != nil {
			t.Fatal(err)
		}
		r := &simRunner{t: t, a: a, now: simT0}
		name, err := a.StartRecording("realism")
		if err != nil {
			t.Fatal(err)
		}
		r.until(40, nil)
		a.StopRecording()
		a.mu.Lock()
		defer a.mu.Unlock()
		snap := a.snapshotLocked(r.now)
		truth := map[string][2]float64{}
		for _, ag := range a.sim.w.Agents() {
			if id := ag.PhoneID(); id != "" {
				truth[id] = [2]float64{ag.X, ag.Y}
			}
		}
		var out res
		var errs []float64
		for _, n := range snap.Nodes {
			if n.UA != "sim" {
				t.Fatalf("unexpected node %+v", n)
			}
			switch n.Src {
			case protocol.SrcGPS:
				out.gps++
				if n.Acc < 3 {
					t.Fatalf("gps node with accuracy %.1f", n.Acc)
				}
			default:
				out.manual++
			}
			if n.Status == protocol.StatusStale {
				out.stale++
			}
			if n.Outside {
				out.outside++
			}
			if p, ok := truth[n.ID]; ok {
				errs = append(errs, math.Hypot(n.X-p[0], n.Y-p[1]))
			}
		}
		sort.Float64s(errs)
		out.medErr = errs[len(errs)/2]
		b, err := os.ReadFile(filepath.Join(a.opt.RecordingsDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), `"lat"`) || strings.Contains(string(b), `"lon"`) {
			t.Fatal("recording holds coordinates")
		}
		return out
	}
	one := 1.0
	ideal := run(SimStart{})
	if ideal.gps != 0 || ideal.stale != 0 || ideal.outside != 0 || ideal.medErr > 0.05 {
		t.Errorf("ideal (default): %+v", ideal)
	}
	if named := run(SimStart{Realism: "ideal"}); named != ideal {
		t.Errorf(`"ideal" %+v differs from the default %+v`, named, ideal)
	}
	rea := run(SimStart{Realism: "realistic"})
	if rea.gps < rea.manual*5 || rea.medErr < 1.5 || rea.medErr > 9 || rea.stale == 0 {
		t.Errorf("realistic: %+v", rea)
	}
	harsh := run(SimStart{Realism: "harsh"})
	if harsh.medErr <= rea.medErr {
		t.Errorf("harsh position error %.1f m not above realistic %.1f m", harsh.medErr, rea.medErr)
	}
	gpsOnly := run(SimStart{Imperfections: &SimImperfections{GPS: &one}})
	if gpsOnly.gps == 0 || gpsOnly.stale != 0 {
		t.Errorf("gps only: %+v", gpsOnly)
	}
	zero := 0.0
	noGPS := run(SimStart{Realism: "realistic", Imperfections: &SimImperfections{GPS: &zero}})
	if noGPS.gps != 0 || noGPS.medErr > 0.3 || noGPS.stale == 0 {
		t.Errorf("realistic without gps: %+v", noGPS)
	}
	t.Logf("ideal %+v\nrealistic %+v\nharsh %+v\ngps only %+v\nrealistic, gps off %+v", ideal, rea, harsh, gpsOnly, noGPS)
}

// TestSimLeadSweep prints lead times over seeds (env-gated):
// PULSE_SIMSWEEP=1 go test ./server/internal/app -run SimLeadSweep -v
func TestSimLeadSweep(t *testing.T) {
	if os.Getenv("PULSE_SIMSWEEP") == "" {
		t.Skip("set PULSE_SIMSWEEP=1")
	}
	scripts := map[string]func(r *simRunner, rng *rand.Rand) func(int){
		"stage→surge": crushScript,
		"calm→surge": func(r *simRunner, rng *rand.Rand) func(int) {
			return func(s int) {
				if s == 30 {
					r.act(crowdsim.Action{Type: "surge", Strength: fp(0.7)})
				}
				if s > 30 && s%3 == 0 {
					r.act(crowdsim.Action{Type: "shove", X: fp(6 + 12*rng.Float64()), Y: fp(3 + 3*rng.Float64()), DX: fp(0), DY: fp(-1)})
				}
			}
		},
		"stage→surge 0.3": func(r *simRunner, rng *rand.Rand) func(int) {
			return func(s int) {
				if s == 5 {
					r.act(crowdsim.Action{Type: "stage"})
				}
				if s == 30 {
					r.act(crowdsim.Action{Type: "surge", Strength: fp(0.3)})
				}
			}
		},
	}
	for _, name := range []string{"stage→surge", "calm→surge", "stage→surge 0.3"} {
		for seed := int64(1); seed <= 5; seed++ {
			r := startSim(t, 250, seed)
			r.until(80, scripts[name](r, rand.New(rand.NewSource(seed))))
			tr := r.a.SimStatus().Truth
			s := func(p *float64) string {
				if p == nil {
					return "  —  "
				}
				return fmt.Sprintf("%5.1f", *p)
			}
			kind := "—"
			if reds := r.reds(); len(reds) > 0 {
				kind = reds[0].Kind
			}
			t.Logf("%-16s seed %d: danger %s  red %s (%s)  lead %s  maxDensity %.1f  maxPressure %5.0f",
				name, seed, s(tr.DangerAt), s(tr.AlertAt), kind, s(tr.LeadSeconds), tr.MaxDensity, tr.MaxPressure)
		}
	}
}

// TestSimRunLoop runs the real loop (real clock) with HTTP-style calls
// racing it; run with -race.
func TestSimRunLoop(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("real time")
	}
	a := New(Options{Detect: detect.DefaultConfig(), RecordingsDir: t.TempDir()})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)
	if err := a.StartSim(SimStart{People: 150, Seed: 1}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(1500 * time.Millisecond)
	for i := 0; time.Now().Before(deadline); i++ {
		a.SimAction(crowdsim.Action{Type: []string{"stage", "calm"}[i%2]})
		a.SimAction(crowdsim.Action{Type: "shove", X: fp(12), Y: fp(4), DX: fp(1), DY: fp(0)})
		_ = a.SimStatus()
		a.DashWelcome()
		time.Sleep(20 * time.Millisecond)
	}
	st := a.SimStatus()
	if !st.Running || st.T < 1 {
		t.Fatalf("sim did not advance in real time: %+v", st)
	}
	a.mu.Lock()
	snap := a.snapshotLocked(hub.Now())
	a.mu.Unlock()
	if snap.Mode != "sim" || len(snap.Nodes) == 0 || len(snap.Sim.Bodies) != st.People {
		t.Fatalf("snapshot: mode %s, %d nodes", snap.Mode, len(snap.Nodes))
	}
	if err := a.StopSim(); err != nil {
		t.Fatal(err)
	}
}

// simReport runs a script and summarises what Pulse said: red alerts by
// kind, the worst zone level, how many phones were ever "swaying" or in a
// wave at once, and the truth's highest density over the run.
type simReport struct {
	reds                map[string]int
	yellowDensity       int
	maxSwaying, maxWave int
	maxTruthDensity     float64
	maxPressure         float64
	dangerAt            *float64
}

func runReport(r *simRunner, end float64, script func(int)) simReport {
	rep := simReport{reds: map[string]int{}}
	r.until(end, func(s int) {
		if script != nil {
			script(s)
		}
		r.a.mu.Lock()
		sw, wv := 0, 0
		for _, ph := range r.a.sim.p.last.Phones {
			switch ph.Status {
			case protocol.StatusSwaying:
				sw++
			case protocol.StatusWave:
				wv++
			}
		}
		rep.maxSwaying, rep.maxWave = max(rep.maxSwaying, sw), max(rep.maxWave, wv)
		r.a.mu.Unlock()
		tr := r.a.SimStatus().Truth
		rep.maxTruthDensity = math.Max(rep.maxTruthDensity, tr.MaxDensity)
		rep.maxPressure = math.Max(rep.maxPressure, tr.MaxPressure)
	})
	for _, al := range r.reds() {
		rep.reds[al.Kind]++
	}
	r.a.mu.Lock()
	for _, al := range r.a.alerts {
		if al.Kind == protocol.KindDensity && al.Level == protocol.LevelYellow && !al.Test {
			rep.yellowDensity++
		}
	}
	r.a.mu.Unlock()
	rep.dangerAt = r.a.SimStatus().Truth.DangerAt
	return rep
}

// TestSimDance: the whole crowd dancing to one beat, each with their own
// delay, goes through the full pipeline without a red wave alert. Density
// alerts are allowed only if the crowd is genuinely packed.
func TestSimDance(t *testing.T) {
	t.Parallel()
	r := startSim(t, 250, 6)
	rep := runReport(r, 60, func(s int) {
		if s == 5 {
			r.act(crowdsim.Action{Type: "dance"})
		}
	})
	t.Logf("dance, 250 people, 55 s: red alerts %v, yellow density incidents %d; up to %d phones swaying and %d in a wave at once; truth max density %.1f /m², pressure %.0f N/m",
		rep.reds, rep.yellowDensity, rep.maxSwaying, rep.maxWave, rep.maxTruthDensity, rep.maxPressure)
	if rep.reds[protocol.KindWave] > 0 {
		t.Errorf("dancing raised %d red wave alerts", rep.reds[protocol.KindWave])
	}
	if rep.reds[protocol.KindDensity] > 0 && rep.maxTruthDensity < 4 {
		t.Errorf("red density alert while the crowd was at most %.1f /m²", rep.maxTruthDensity)
	}
	if rep.dangerAt != nil {
		t.Errorf("dancing became dangerous at %.1f s", *rep.dangerAt)
	}
}

// TestSimIntermission: the rush to the bar and toilets (and back) raises
// no wave alert; crowding at the POIs may show as density.
func TestSimIntermission(t *testing.T) {
	t.Parallel()
	r := startSim(t, 250, 7)
	rep := runReport(r, 90, func(s int) {
		if s == 5 {
			r.act(crowdsim.Action{Type: "intermission"})
		}
	})
	t.Logf("intermission, 250 people, 85 s: red alerts %v, yellow density incidents %d; up to %d phones swaying, %d in a wave; truth max density %.1f /m², pressure %.0f N/m",
		rep.reds, rep.yellowDensity, rep.maxSwaying, rep.maxWave, rep.maxTruthDensity, rep.maxPressure)
	if rep.reds[protocol.KindWave] > 0 {
		t.Errorf("an intermission raised %d red wave alerts", rep.reds[protocol.KindWave])
	}
	if rep.dangerAt != nil {
		t.Errorf("an intermission became dangerous at %.1f s", *rep.dangerAt)
	}
}

// TestSimPositionCost measures what the sim phones' 10 Hz positions cost
// the pipeline (env-gated): detect step and sim feed time per call at 250
// and 500 people, for positions every 100 ms vs every 500 ms.
// PULSE_POSCOST=1 go test ./server/internal/app -run SimPositionCost -v
func TestSimPositionCost(t *testing.T) {
	if os.Getenv("PULSE_POSCOST") == "" {
		t.Skip("set PULSE_POSCOST=1")
	}
	defer func(v int) { crowdsim.PosEveryTicks = v }(crowdsim.PosEveryTicks)
	for _, people := range []int{250, 500} {
		for _, every := range []int{5, 25} {
			crowdsim.PosEveryTicks = every
			r := startSim(t, people, 1)
			r.until(10, nil)                             // warm up: buffers full
			r.act(crowdsim.Action{Type: "intermission"}) // plenty of walking
			var det, feed []time.Duration
			pos := 0
			for end := r.sec() + 20; r.sec() < end-1e-9; {
				r.now += 50
				r.a.mu.Lock()
				s := r.a.sim
				r.a.mu.Unlock()
				s.mu.Lock()
				s.w.AdvanceTo(r.now)
				ev := s.w.Events()
				s.mu.Unlock()
				for _, e := range ev {
					if e.Kind == crowdsim.EvPos {
						pos++
					}
				}
				t0 := time.Now()
				r.a.mu.Lock()
				r.a.feedSim(s, ev, r.now)
				r.a.mu.Unlock()
				feed = append(feed, time.Since(t0))
				if (r.now-simT0)%int64(DetectEvery/1e6) == 0 {
					t0 := time.Now()
					r.a.detectTick(r.now)
					det = append(det, time.Since(t0))
				}
			}
			q := func(d []time.Duration, p float64) time.Duration {
				sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
				return d[int(p*float64(len(d)-1))]
			}
			mean := func(d []time.Duration) time.Duration {
				var s time.Duration
				for _, v := range d {
					s += v
				}
				return s / time.Duration(len(d))
			}
			t.Logf("%d people (%d phones), positions every %d ms: %.0f pos/s; detect step mean %v p95 %v; feed per 50 ms mean %v p95 %v",
				people, r.a.SimStatus().Phones, every*20, float64(pos)/20, mean(det), q(det, 0.95), mean(feed), q(feed, 0.95))
		}
	}
}

// TestSimBehaviourSweep prints what Pulse says about the harmless
// behaviours over seeds (env-gated):
// PULSE_SIMSWEEP=1 go test ./server/internal/app -run SimBehaviourSweep -v
func TestSimBehaviourSweep(t *testing.T) {
	if os.Getenv("PULSE_SIMSWEEP") == "" {
		t.Skip("set PULSE_SIMSWEEP=1")
	}
	for _, act := range []string{"calm", "dance", "intermission"} {
		for seed := int64(1); seed <= 3; seed++ {
			r := startSim(t, 250, seed)
			rep := runReport(r, 120, func(s int) {
				if s == 5 && act != "calm" {
					r.act(crowdsim.Action{Type: act})
				}
			})
			t.Logf("%-12s seed %d: red %v, yellow density incidents %d, max %d swaying / %d wave phones, truth max density %.1f /m²",
				act, seed, rep.reds, rep.yellowDensity, rep.maxSwaying, rep.maxWave, rep.maxTruthDensity)
		}
	}
}
