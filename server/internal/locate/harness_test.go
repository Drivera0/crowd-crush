package locate

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"

	"github.com/Drivera0/crowd-crush/server/internal/crowd"
	"github.com/Drivera0/crowd-crush/server/internal/crowdsim"
	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/geo"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// The measurement harness: the crowd simulation's phone messages go through
// the detector and the density tracker the way cmd/eval feeds them (which is
// the way the server's pipeline does), once with the positions the pipeline
// has today ("raw": hand placement, or GPS smoothed and clamped) and once
// per estimator variant, all from the same simulated run. Everything is
// compared with where the simulated people really stand.

const (
	t0Ms          = 1_700_000_000_000
	simBearing    = 30.0
	neighbourDist = 1.1
	densFrom      = 2.0
)

var levelRank = map[string]int{protocol.LevelCalm: 0, protocol.LevelYellow: 1, protocol.LevelRed: 2}

// accFor turns the estimator's 1-σ radius into the accuracy radius the
// detector and the density tracker expect (the 68 % radius a GPS reports),
// 0 (exact) for a phone that is as good as placed by hand.
func accFor(sigma float64) float64 {
	if sigma < ExactBelow {
		return 0
	}
	return math.Round(AccRadius(sigma)*10) / 10
}

// lostOut: variants named "…lost-out" leave lost phones out of everything,
// like unplaced ones.
func outsideArm(a *arm, p Position) bool {
	return p.Lost && strings.Contains(a.v.name, "lost-out")
}

// variant is one way of positioning the phones.
type variant struct {
	name string
	// mod changes the estimator's default config; nil with raw = no estimator.
	raw bool
	mod func(*Config)
}

type metrics struct {
	errs               []float64 // every phone's position error, every second
	errT               map[int][]float64
	counted, phones    int
	truePairs, tpPairs int
	estPairs           int
	farPairs           int // estimated neighbours truly more than 2.2 m apart
	densN              int
	densErr, densAbs   float64
	linkN, linkNear    int // estimator links, and those truly within 1.5 m
	accN               int
	accIn1, accIn2     int // phones whose error is within 1 σ, 2 σ
}

type outcome struct {
	maxLevel           string
	redAt, yellowAt    float64
	waveRed, densRed   bool
	dangerAt           float64
	lead               float64
	hasLead            bool
	walk, show         metrics // while people come in; after
	stepNs             int64
	steps              int
	locNs              int64
	phonesAtEnd, links int
}

type arm struct {
	v     variant
	cfg   detect.Config
	det   *detect.Detector
	trk   *crowd.Tracker
	loc   *Estimator
	gone  map[string]int64
	known map[string]bool
	gps   map[string]*geo.Smoother
	pos   map[string][2]float64
	acc   map[string]float64
	in    map[string]bool // placed and inside
	lost  map[string]bool // estimator: too vague to count toward density
	out   outcome
	pts   []crowd.Point
	cl    []crowd.Cluster
	edges []detect.Edge
	lpos  []Position
}

func newArm(v variant, cfg detect.Config, lc Config) *arm {
	a := &arm{v: v, cfg: cfg, det: detect.New(cfg), trk: crowd.NewTracker(crowd.ConfigFrom(cfg)),
		gone: map[string]int64{}, known: map[string]bool{}, gps: map[string]*geo.Smoother{},
		pos: map[string][2]float64{}, acc: map[string]float64{}, in: map[string]bool{}, lost: map[string]bool{},
		out: outcome{maxLevel: protocol.LevelCalm, redAt: -1, yellowAt: -1, dangerAt: -1}}
	a.out.walk.errT, a.out.show.errT = map[int][]float64{}, map[int][]float64{}
	if !v.raw {
		if v.mod != nil {
			v.mod(&lc)
		}
		applySet(&lc)
		a.loc = New(lc)
	}
	return a
}

func (a *arm) place(id string, x, y, acc float64, outside bool) {
	if acc > 0 {
		x, y = a.cfg.Fold(x, y)
	}
	x, y = a.cfg.Clamp(x, y)
	a.det.SetPhone(id, x, y)
	a.det.SetAccuracy(id, acc)
	a.det.SetOutside(id, outside)
	a.pos[id] = [2]float64{x, y}
	a.acc[id] = acc
	a.in[id] = !outside
}

func hd(p *float64) float64 {
	if p == nil {
		return NoHeading
	}
	return *p
}

func (a *arm) feed(now int64, evs []crowdsim.Event) {
	for _, e := range evs {
		if e.Kind != crowdsim.EvHello && !a.known[e.ID] {
			continue
		}
		switch e.Kind {
		case crowdsim.EvHello:
			a.known[e.ID] = true
			delete(a.gone, e.ID)
			if a.loc != nil {
				a.det.SetPhone(e.ID, 0, 0)
				if e.Auto {
					ok := a.loc.Join(e.ID, now)
					if !a.in[e.ID] {
						a.det.SetOutside(e.ID, true)
					}
					_ = ok
				} else {
					a.loc.Fix(e.ID, now, Fix{X: e.X, Y: e.Y, Exact: true})
					a.place(e.ID, e.X, e.Y, 0, false)
				}
				continue
			}
			a.gps[e.ID] = &geo.Smoother{}
			x, y := e.X, e.Y
			if e.Auto {
				x, y = a.cfg.LegacyPos(0, 0)
			}
			a.place(e.ID, x, y, 0, e.Auto)
		case crowdsim.EvPos:
			if a.loc != nil {
				a.loc.Fix(e.ID, now, Fix{X: e.X, Y: e.Y, Exact: true})
				a.place(e.ID, e.X, e.Y, 0, false)
				continue
			}
			a.gps[e.ID].Reset()
			a.place(e.ID, e.X, e.Y, 0, false)
		case crowdsim.EvGPS:
			if a.loc != nil {
				a.loc.GPS(e.ID, now, e.X, e.Y, e.Acc)
				continue
			}
			if !(e.Acc > 0 && e.Acc <= a.cfg.GPSMaxAcc) {
				continue
			}
			s := a.gps[e.ID]
			x, y := s.Add(e.X, e.Y, e.Acc)
			acc := math.Max(0.1, math.Round(s.Acc*10)/10)
			a.place(e.ID, x, y, acc, a.cfg.Outside(x, y, acc))
		case crowdsim.EvMotion:
			a.det.Add(e.ID, detect.Sample{T: e.M.T, AX: e.M.AX, AY: e.M.AY, AZ: e.M.AZ, Rot: e.M.Rot, G: detect.Gravity(e.M.G)})
			if a.loc != nil {
				a.loc.Motion(e.ID, Sample{T: e.M.T, AX: e.M.AX, AY: e.M.AY, AZ: e.M.AZ, Rot: e.M.Rot,
					G: detect.Gravity(e.M.G), HD: hd(e.M.HD), HB: hd(e.M.HB)})
			}
		case crowdsim.EvDR:
			if a.loc != nil {
				a.loc.DR(e.ID, now, e.Steps, e.East, e.North)
			}
		case crowdsim.EvGone:
			a.gone[e.ID] = now
			if a.loc != nil {
				a.loc.Gone(e.ID, now)
			}
		}
	}
}

func (a *arm) step(now int64) {
	if a.loc != nil {
		// The detector's wave edges are not passed on as links: between
		// roughly placed phones they join people riding the same wave,
		// who are mostly not neighbours (18 % within 1.5 m when measured).
		var links []Link
		t := nowNs()
		a.lpos = a.loc.Step(now, links)
		a.out.locNs += nowNs() - t
		// A lost phone stays in the detector, which finds its neighbours
		// by motion whatever its position, and is left out of the density
		// clusters, like a phone that was never placed.
		for _, p := range a.lpos {
			a.place(p.ID, p.X, p.Y, accFor(p.Acc), outsideArm(a, p))
			a.lost[p.ID] = p.Lost && !strings.Contains(a.v.name, "lost-in")
		}
	}
	t := nowNs()
	res := a.det.Step(now)
	a.edges = res.Edges
	var pts []crowd.Point
	for _, p := range res.Phones {
		if _, gone := a.gone[p.ID]; gone || p.Status == protocol.StatusStale || p.Outside || a.lost[p.ID] {
			continue
		}
		pts = append(pts, crowd.Point{ID: p.ID, X: p.X, Y: p.Y, Acc: p.Acc})
	}
	clusters, cch := a.trk.Update(now, pts)
	a.out.stepNs += nowNs() - t
	a.out.steps++
	a.pts, a.cl = pts, clusters
	for id, at := range a.gone {
		if now-at > 30_000 {
			a.det.RemovePhone(id)
			delete(a.gone, id)
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
	}
	for _, ch := range cch {
		if ch.To == protocol.LevelRed {
			densRed, level = true, protocol.LevelRed
		}
	}
	if levelRank[level] > levelRank[a.out.maxLevel] {
		a.out.maxLevel = level
	}
	ts := float64(now-t0Ms) / 1000
	if level != protocol.LevelCalm && a.out.yellowAt < 0 {
		a.out.yellowAt = ts
	}
	if (waveRed || densRed) && a.out.redAt < 0 {
		a.out.redAt = ts
	}
	a.out.waveRed = a.out.waveRed || waveRed
	a.out.densRed = a.out.densRed || densRed
}

// compare measures the arm's picture against the truth at second sec.
func (a *arm) compare(m *metrics, agents []*crowdsim.Agent, truth []crowd.Point, truePeak float64, sec int) {
	counted := map[string]bool{}
	for _, p := range a.pts {
		counted[p.ID] = true
	}
	type ph struct {
		id             string
		tx, ty, ex, ey float64
	}
	var ps []ph
	for _, ag := range agents {
		id := ag.PhoneID()
		if id == "" {
			continue
		}
		m.phones++
		if !counted[id] {
			continue
		}
		m.counted++
		p := a.pos[id]
		ps = append(ps, ph{id, ag.X, ag.Y, p[0], p[1]})
		e := math.Hypot(p[0]-ag.X, p[1]-ag.Y)
		m.errs = append(m.errs, e)
		m.errT[sec/10*10] = append(m.errT[sec/10*10], e)
	}
	// Neighbour pairs among the counted phones.
	for i := range ps {
		for j := i + 1; j < len(ps); j++ {
			td := math.Hypot(ps[i].tx-ps[j].tx, ps[i].ty-ps[j].ty)
			ed := math.Hypot(ps[i].ex-ps[j].ex, ps[i].ey-ps[j].ey)
			tn, en := td <= neighbourDist, ed <= neighbourDist
			if tn {
				m.truePairs++
			}
			if en {
				m.estPairs++
				if td > 2*neighbourDist {
					m.farPairs++
				}
			}
			if tn && en {
				m.tpPairs++
			}
		}
	}
	// True neighbour pairs lost because a phone isn't counted at all.
	var all []ph
	for _, ag := range agents {
		if ag.PhoneID() != "" && !counted[ag.PhoneID()] {
			all = append(all, ph{tx: ag.X, ty: ag.Y})
		}
	}
	for i := range all {
		for j := i + 1; j < len(all); j++ {
			if math.Hypot(all[i].tx-all[j].tx, all[i].ty-all[j].ty) <= neighbourDist {
				m.truePairs++
			}
		}
		for _, q := range ps {
			if math.Hypot(all[i].tx-q.tx, all[i].ty-q.ty) <= neighbourDist {
				m.truePairs++
			}
		}
	}
	if truePeak >= densFrom {
		est := 0.0
		for _, c := range a.cl {
			est = math.Max(est, c.Est)
		}
		m.densN++
		m.densErr += est - truePeak
		m.densAbs += math.Abs(est - truePeak)
	}
	if a.loc != nil {
		tp := map[string][2]float64{}
		for _, q := range ps {
			tp[q.id] = [2]float64{q.tx, q.ty}
		}
		for _, l := range a.loc.lastLinks {
			pa, oka := tp[l.a.id]
			pb, okb := tp[l.b.id]
			if !oka || !okb {
				continue
			}
			m.linkN++
			if math.Hypot(pa[0]-pb[0], pa[1]-pb[1]) <= 1.5 {
				m.linkNear++
			}
		}
		for _, p := range a.lpos {
			t, ok := tp[p.ID]
			if !ok {
				continue
			}
			e := math.Hypot(p.X-t[0], p.Y-t[1])
			m.accN++
			if e <= p.Acc {
				m.accIn1++
			}
			if e <= 2*p.Acc {
				m.accIn2++
			}
		}
	}
}

type simScript struct {
	name   string
	expect string // red | calm | yellow-ok
	dur    float64
	act    func(w *crowdsim.World, s int, rng *rand.Rand)
}

func fp(v float64) *float64 { return &v }

func apply(w *crowdsim.World, a crowdsim.Action) {
	if err := w.Apply(a); err != nil {
		panic(err)
	}
}

// The crowd-simulation scripts of cmd/eval, plus everyone dancing.
var simScripts = []simScript{
	{name: "calm", expect: "calm", dur: 80, act: func(*crowdsim.World, int, *rand.Rand) {}},
	{name: "dance", expect: "calm", dur: 80, act: func(w *crowdsim.World, s int, _ *rand.Rand) {
		if s == 5 {
			apply(w, crowdsim.Action{Type: "dance"})
		}
	}},
	{name: "attract", expect: "yellow-ok", dur: 80, act: func(w *crowdsim.World, s int, _ *rand.Rand) {
		if s == 5 {
			apply(w, crowdsim.Action{Type: "attract", X: fp(12), Y: fp(9)})
		}
	}},
	{name: "calm→surge", expect: "red", dur: 80, act: func(w *crowdsim.World, s int, rng *rand.Rand) {
		if s == 30 {
			apply(w, crowdsim.Action{Type: "surge", Strength: fp(0.7)})
		}
		if s > 30 && s%3 == 0 {
			apply(w, crowdsim.Action{Type: "shove", X: fp(6 + 12*rng.Float64()), Y: fp(3 + 3*rng.Float64()), DX: fp(0), DY: fp(-1)})
		}
	}},
	{name: "stage→surge", expect: "red", dur: 80, act: func(w *crowdsim.World, s int, rng *rand.Rand) {
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
	{name: "stage→surge 0.3", expect: "red", dur: 80, act: func(w *crowdsim.World, s int, _ *rand.Rand) {
		if s == 5 {
			apply(w, crowdsim.Action{Type: "stage"})
		}
		if s == 30 {
			apply(w, crowdsim.Action{Type: "surge", Strength: fp(0.3)})
		}
	}},
}

func scriptByName(name string) simScript {
	for _, s := range simScripts {
		if s.name == name {
			return s
		}
	}
	panic("no script " + name)
}

// condition is how the phones are.
type condition struct {
	name string
	rl   crowdsim.Realism
	// walkIn: the venue starts empty and everyone comes in at the entry
	// spot over walkInOver seconds; the script starts walkInS later.
	walkIn bool
}

const (
	walkInOver = 150.0
	walkInS    = 190
	entryX     = 4.0
	entryY     = 14.5
)

// simGeom is the default sim venue's walls and stage for the estimator.
func simGeom(w *crowdsim.World) ([][4]float64, [][2]float64) {
	_, walls := crowdsim.GeometryJSON(w.G)
	g := w.G
	stage := [][2]float64{{g.BarrierX0, 0}, {g.BarrierX1, 0}, {g.BarrierX1, g.BarrierY}, {g.BarrierX0, g.BarrierY}}
	return walls, stage
}

// runSim runs one script and seed under one condition for every variant.
func runSim(sc simScript, seed int64, c condition, vs []variant, people int) []outcome {
	cfg := detect.DefaultConfig()
	cfg.Participation = 0.6
	wc := crowdsim.Config{W: cfg.VenueW, H: cfg.VenueH, People: people, Participation: 0.6, Seed: seed, StartMs: t0Ms,
		Realism: c.rl, Bearing: simBearing}
	if c.walkIn {
		wc.Entry = &crowdsim.Entry{X: entryX, Y: entryY, Over: walkInOver}
	}
	w, err := crowdsim.New(wc)
	if err != nil {
		panic(err)
	}
	lc := DefaultConfig(cfg.VenueW, cfg.VenueH)
	lc.Bearing, lc.HasBearing = simBearing, true
	lc.Walls, lc.Stage = simGeom(w)
	if c.walkIn {
		lc.Entry = Entry{On: true, X: entryX, Y: entryY, Sigma: 1.5}
	}
	arms := make([]*arm, len(vs))
	for i, v := range vs {
		arms[i] = newArm(v, cfg, lc)
	}
	ev := w.Events()
	for _, a := range arms {
		a.feed(t0Ms, ev)
	}
	rng := rand.New(rand.NewSource(seed))
	lead := 0
	if c.walkIn {
		lead = walkInS
	}
	end := int64(t0Ms) + int64((float64(lead)+sc.dur)*1000)
	for now := int64(t0Ms); now < end; {
		now += 50
		sec := int((now - t0Ms) / 1000)
		if (now-t0Ms)%1000 == 0 && sec >= lead {
			sc.act(w, sec-lead, rng)
		}
		w.AdvanceTo(now)
		ev := w.Events()
		for _, a := range arms {
			a.feed(now, ev)
		}
		if (now-t0Ms)%250 == 0 {
			for _, a := range arms {
				a.step(now)
			}
		}
		if (now-t0Ms)%1000 == 0 {
			agents := w.Agents()
			truth := make([]crowd.Point, len(agents))
			for i, ag := range agents {
				truth[i] = crowd.Point{ID: fmt.Sprint(ag.ID), X: ag.X, Y: ag.Y}
			}
			truePeak, _, _ := crowd.LocalPeakAmong(truth, truth)
			for _, a := range arms {
				m := &a.out.show
				if sec < lead {
					m = &a.out.walk
				}
				a.compare(m, agents, truth, truePeak, sec)
			}
		}
	}
	tr := w.Truth()
	outs := make([]outcome, len(arms))
	for i, a := range arms {
		a.out.dangerAt = tr.DangerAt
		if tr.DangerAt >= 0 && a.out.redAt >= 0 {
			a.out.lead, a.out.hasLead = tr.DangerAt-a.out.redAt, true
		}
		if a.loc != nil {
			st := a.loc.Stats()
			a.out.phonesAtEnd, a.out.links = st.Phones, st.Links
		}
		outs[i] = a.out
	}
	return outs
}

func quant(v []float64, q float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[int(math.Min(float64(len(s)-1), q*float64(len(s))))]
}

func meanOf(v []float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func (m *metrics) add(o *metrics) {
	m.errs = append(m.errs, o.errs...)
	if m.errT == nil {
		m.errT = map[int][]float64{}
	}
	for k, v := range o.errT {
		m.errT[k] = append(m.errT[k], v...)
	}
	m.counted += o.counted
	m.phones += o.phones
	m.truePairs += o.truePairs
	m.tpPairs += o.tpPairs
	m.estPairs += o.estPairs
	m.farPairs += o.farPairs
	m.densN += o.densN
	m.densErr += o.densErr
	m.densAbs += o.densAbs
	m.linkN += o.linkN
	m.linkNear += o.linkNear
	m.accN += o.accN
	m.accIn1 += o.accIn1
	m.accIn2 += o.accIn2
}

func ratio(a, b int) float64 {
	if b == 0 {
		return math.NaN()
	}
	return float64(a) / float64(b)
}

func (m *metrics) row() string {
	return fmt.Sprintf("err mean %.2f med %.2f p95 %.2f | counted %3.0f%% | pairs found %3.0f%% false %3.0f%% (far %3.0f%%) | dens bias %+.2f abs %.2f | links %d near %3.0f%% | within σ %2.0f%% 2σ %2.0f%%",
		meanOf(m.errs), quant(m.errs, 0.5), quant(m.errs, 0.95), 100*ratio(m.counted, m.phones),
		100*ratio(m.tpPairs, m.truePairs), 100*ratio(m.estPairs-m.tpPairs, m.estPairs), 100*ratio(m.farPairs, m.estPairs),
		m.densErr/math.Max(1, float64(m.densN)), m.densAbs/math.Max(1, float64(m.densN)),
		m.linkN, 100*ratio(m.linkNear, m.linkN), 100*ratio(m.accIn1, m.accN), 100*ratio(m.accIn2, m.accN))
}
