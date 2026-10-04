// Package locate estimates where each phone is, for phones nobody placed
// on the map: everyone joins through one shared QR code and walks off.
//
// Every source that needs nothing from the user is fused per phone:
//
//   - the entry spot: the QR code hangs at a known place on the map, so a
//     phone that joins with no position starts there, give or take the
//     queue (Config.Entry);
//   - GPS fixes (venue metres, with their reported accuracy), whose error
//     is mostly a slow bias: the filter carries that bias in its state
//     (kalman.go);
//   - dead reckoning from the motion stream: steps from the vertical
//     bounce, direction from the compass heading and the gait itself
//     (pdr.go), nothing while the person stands still;
//   - absolute fixes of any shape (Fix): a phone placed by hand or at a
//     tower, a Bluetooth beacon fix that is good along one line and poor
//     across it, a position a phone worked out with its mesh neighbours;
//   - proximity: phones whose motion is shared stand within about a metre
//     of each other. Links come from this package's own comparison of the
//     phones' traces (near.go), helped by the phones' mesh reports, and
//     pull the linked phones' estimates together (coop.go);
//   - what a floor allows: nobody stands in a wall, on the stage or outside
//     the venue, no dead-reckoned step crosses a wall (geom.go), and two
//     people are never on the same spot (coop.go).
//
// The per-phone part is a Kalman filter (position and GPS bias); the
// cooperative part is a relaxation over the proximity graph run on top of
// the filters' output every Step, which never feeds back into the filters
// (the same evidence would be counted again every tick).
//
// Everything is venue metres. Latitude and longitude never get here.
package locate

import (
	"math"
	"sort"
)

// Sources that contributed to a position (Position.Src, a bit set).
const (
	SrcEntry  = 1 << iota // started at the entry spot
	SrcGPS                // GPS fixes
	SrcSteps              // dead reckoning
	SrcFix                // placed by hand or at an anchor
	SrcBeacon             // Bluetooth beacon fix
	SrcMesh               // the phone's own mesh estimate
	SrcNear               // pulled toward motion neighbours this step
	SrcMap                // moved by a wall, the stage, the venue edge or the spacing rule
)

// SrcNames names the bits of Position.Src, lowest first.
var SrcNames = []string{"entry", "gps", "steps", "fix", "beacon", "mesh", "near", "map"}

// SrcList is the names of the sources in a bit set.
func SrcList(src int) []string {
	var out []string
	for i, n := range SrcNames {
		if src&(1<<i) != 0 {
			out = append(out, n)
		}
	}
	return out
}

// ExactBelow: an estimate with a 1-σ radius under this (m) is as good as a
// phone placed by hand; the pipeline treats it as exact.
const ExactBelow = 0.5

// AccRadius turns a 1-σ radius into the 68 % radius a GPS reports as its
// accuracy (1.51 σ for a round Gaussian), which is what the detector's and
// the density tracker's accuracy handling expect.
func AccRadius(sigma float64) float64 { return 1.51 * sigma }

// Entry is where the shared QR code is: phones that join without a
// position are there, to within Sigma metres (1 σ per axis).
type Entry struct {
	On    bool
	X, Y  float64
	Sigma float64 // 0 = 1.5 m
}

// Config holds the estimator's settings.
type Config struct {
	VenueW, VenueH float64
	// Bearing: compass direction of the map's up, degrees clockwise from
	// north. Without it (HasBearing false) headings can't be turned into
	// map directions and dead reckoning only knows that a phone walks.
	Bearing    float64
	HasBearing bool
	Entry      Entry
	// Walls ([x0, y0, x1, y1]) and the stage outline: nobody is inside
	// them and no walked step crosses them. Used when MapConstraints is on.
	Walls [][4]float64
	Stage [][2]float64

	// Switches, each on by default, so their effect can be measured.
	StandStill bool // hold the position while no steps are seen
	// BlindWalk: a step rhythm with horizontal motion in step but no
	// usable direction (a phone in a bag) grows the uncertainty by the
	// distance walked. Off by default: a crowd jumping while it is pushed
	// reads the same, and would lose everyone.
	BlindWalk      bool
	DeadReckon     bool    // move with detected steps
	MapConstraints bool    // walls, stage, venue edge
	Spacing        float64 // m, minimum distance between two estimates (0 = off)
	OwnLinks       bool    // find motion neighbours from the phones' traces (near.go)
	Coop           bool    // pull linked phones together
	// CoopFeed: the cooperative position goes back into a linked phone's
	// filter once a second as a fix with this many times its variance
	// (0 = never).
	CoopFeed float64

	// GPS error model.
	BiasTauS    float64 // correlation time of the GPS bias (s)
	GPSMaxAcc   float64 // fixes less accurate than this are ignored (m)
	GPSFloor    float64 // white part of a fix's error is at least this (m)
	GPSWhite    float64 // … and this share of its bias σ
	GPSJumpGate float64 // squared Mahalanobis distance beyond which a fix is taken as a bias jump
	BiasMax     float64 // the bias is held within this many σ of the fix's accuracy; beyond, the position gives way

	// Dead reckoning.
	HandlingRot     float64 // deg/s
	MinStepAmp      float64 // m/s², vertical amplitude at the step frequency
	MinStepPurity   float64 // share of the vertical variance at that frequency
	MinQuad         float64 // |Im(H/V)| needed for a direction
	QuadOverInPhase float64 // … and at least this × the in-phase part
	MinBlindRatio   float64 // |H/V| above which a step rhythm without direction is still walking
	StepLenErr      float64 // 1-σ error of the walked distance, as a share of it
	HeadingErrDeg   float64 // 1-σ heading error (degrees)
	StandDrift      float64 // m per √s a standing person may still shuffle
	StandCap        float64 // … up to this many metres (1 σ) since they last walked or were placed
	DRLenErr        float64 // 1-σ error of a distance the phone itself dead-reckoned, as a share of it
	FreeDrift       float64 // m per √s for a silent phone that had settled
	FollowDrift     float64 // m per √s when StandStill is off: the position follows the fixes
	MaxSigma        float64 // m, cap on the position uncertainty
	// SilentSpeed: a phone that sends nothing (screen locked, in a
	// stall) may be walking; its uncertainty grows by this many m/s.
	SilentSpeed float64
	// Nobody stays at the entry. A phone that started there and has not
	// been seen to walk EntryMinWalk metres within EntryLingerS seconds
	// left without being followed (a bag, a locked screen, a shuffle too
	// soft to count): from then on its uncertainty grows by EntrySpeed m/s
	// until something else places it.
	EntryLingerS, EntryMinWalk, EntrySpeed float64
	// LostSigma: an estimate vaguer than this (m) says nothing about where
	// in the venue the phone is; it is reported as Lost and should count
	// toward nothing, like a phone that was never placed.
	LostSigma float64

	// Proximity.
	LinkRange  float64 // m: linked phones are taken to be within this
	LinkSigma  float64 // m, 1-σ of a link as a constraint on uncertainty
	NearCorr   float64 // rotation-free correlation above which two traces move together
	NearBudget int     // fresh candidates compared per phone and step
	NearMaxR   float64 // m, farthest candidate
	NearMinRMS float64 // m/s², both traces need this much motion
	NearCommon float64 // a phone that matches more than this share of random candidates carries no information (everyone moves to the same beat)
	NearHoldMs int64   // a pair is kept this long after it was last compared
	NearHits   float64 // recent matches needed before a pair is linked
	NearRhythm float64 // a phone whose own trace repeats this well is swaying, not pushed: not compared
	// NearWalking: compare phones that are walking too. A steady walk's
	// sway is the gait's own and fails the rhythm test; people pressing
	// forward in a surge take steps and are pushed at the same time, and
	// leaving them out delays the links by seconds.
	NearWalking bool
	ForgetMs    int64 // a phone gone this long is dropped
}

// DefaultConfig is the configuration for a venue of the given size.
func DefaultConfig(w, h float64) Config {
	return Config{
		VenueW: w, VenueH: h,
		StandStill: true, DeadReckon: true, MapConstraints: true, Spacing: 0.4, OwnLinks: true, Coop: true,
		BiasTauS: 60, GPSMaxAcc: 25, GPSFloor: 1.0, GPSWhite: 0.25, GPSJumpGate: 9, BiasMax: 4,
		HandlingRot: 200, MinStepAmp: 0.15, MinStepPurity: 0.45, MinQuad: 0.3, QuadOverInPhase: 1.0, MinBlindRatio: 0.12,
		StepLenErr: 0.3, HeadingErrDeg: 20, StandDrift: 0.03, StandCap: 0.3, DRLenErr: 0.12, FreeDrift: 0.15, FollowDrift: 0.6, MaxSigma: 30,
		SilentSpeed: 0.4, EntryLingerS: 12, EntryMinWalk: 3, EntrySpeed: 1, LostSigma: 8,
		LinkRange: 0.9, LinkSigma: 0.8, NearCorr: 0.7, NearBudget: 6, NearMaxR: 12, NearMinRMS: 0.12,
		NearCommon: 0.25, NearHoldMs: 6000, NearHits: 2.2, NearRhythm: 0.6, NearWalking: true, ForgetMs: 30_000,
	}
}

// Sample is one motion summary (clock-corrected).
type Sample struct {
	T          int64
	AX, AY, AZ float64
	Rot        float64
	G          [3]float64 // unit gravity, device frame; zero = not sent
	// HD, HB: compass heading (degrees clockwise from north) of the phone's
	// top edge and of its back; NaN = not sent.
	HD, HB float64
}

// NoHeading is the value of Sample.HD / HB when the phone sent none.
var NoHeading = math.NaN()

// Fix is an absolute position measurement with a 1-σ error ellipse:
// Along metres in the direction (AX, AY) and Across metres at right angles
// to it. A round fix sets Along = Across (the direction is ignored).
type Fix struct {
	X, Y          float64
	Along, Across float64
	AX, AY        float64
	Src           int // SrcFix, SrcBeacon or SrcMesh
	// Exact: the phone is put there (placed by hand, tower check-in): the
	// filter starts again at this point instead of weighing it.
	Exact bool
}

// Link says two phones are within about a metre of each other, with a
// weight 0–1 (how sure).
type Link struct {
	A, B string
	W    float64
}

// Position is the estimator's answer for one phone.
type Position struct {
	ID   string
	X, Y float64
	// Acc is the 1-σ radius of the estimate (m): about 39 % of phones are
	// within it, 86 % within twice it, if it is honest.
	Acc float64
	Src int
	// FX, FY: the phone's own filter estimate, before the cooperative step.
	FX, FY  float64
	Walking bool
	Links   int // motion neighbours in this step
	// Lost: the uncertainty is beyond Config.LostSigma. The position is
	// the last one held and means little.
	Lost bool
}

// carryMovedCos: gravity more than 35 degrees from where it has been lately
// means the phone was moved on the body.
var carryMovedCos = math.Cos(35 * math.Pi / 180)

// settleMs: a phone that came in through the entry this long ago has found
// its place.
const settleMs = 60_000

// gaitHoldMs: a walk is carried on this long through a gap in the motion
// stream (handling, lost summaries).
const gaitHoldMs = 2500

const ringN = 48 // samples kept per phone (4.8 s at 10 Hz)

type samp struct {
	t      int64
	v      float64 // vertical, up
	h1, h2 float64 // horizontal, levelled device axes
	b1, b2 float64 // band-passed horizontal, levelled device axes
	rot    float64
}

type phone struct {
	id  string
	idx int // position in the sorted list this step
	lev level

	ring [ringN]samp
	n    int // samples held
	head int // next write

	// band-pass state for the proximity traces
	bpInit      bool
	lp1, lp2    float64
	dc1, dc2    float64
	lastSampleT int64
	// Compass: the heading of e1 as a smoothed unit vector, and when it was
	// last measured.
	hdC, hdS float64
	hdT      int64
	hdSet    bool
	gSlow    vec3 // where gravity has been lately, to notice the phone being moved

	kf       kalman
	placed   bool
	src      int
	lastTick int64 // estimator time the filter was last advanced to
	gpsSigma float64
	blind    float64 // m walked in an unknown direction in the current stretch
	walked   float64 // m dead-reckoned in the current walk
	entryAt  int64   // when it started at the entry spot (0 = it didn't, or has been placed since)
	tracked  float64 // m dead-reckoned since then
	// stillSince: when the current stretch of standing began (0 = walking).
	stillSince int64
	walking    bool
	wasWalking bool // walking when the motion stream last said anything
	gait       pdrOut
	gaitAt     int64 // when the gait was last measured
	// The way the person walks relative to the phone (pdr.go): mean unit
	// vector in the levelled device axes, and how many windows went into it.
	offX, offY float64
	offN       int
	gone       bool
	goneAt     int64

	// cooperative step
	x, y    float64
	acc     float64
	moved   bool
	near    bool
	wasNear bool
	links   int
	tr1     []float64 // resampled traces (near.go)
	tr2     []float64
	trOK    bool
	// rhythmic: the trace repeats (swaying to music): not compared.
	rhythmic bool
	trRMS    float64
	common   float64 // share of random candidates this phone matches (EMA)
	// How far the cooperative step moved the phone last time, so the next
	// one starts where this one ended.
	cdx, cdy float64
	fedAt    int64

	// The phone's own dead reckoning (DR): its last totals, and what has
	// come in since the last Step (map metres, steps).
	drSet    bool
	drSteps  int
	drE, drN float64
	drAt     int64
	drX, drY float64
	drN0     int
	stood    float64 // variance added while standing since the last walk or fix
}

func (p *phone) at(i int) *samp { // i = 0 oldest … n−1 newest
	return &p.ring[(p.head-p.n+i+2*ringN)%ringN]
}

// Estimator tracks every phone. Not safe for concurrent use.
type Estimator struct {
	cfg    Config
	phones map[string]*phone
	geom   geom
	pairs  map[[2]string]*pairEv
	seq    uint64
	order  []*phone
	out    []Position
	stats  Stats

	// scratch
	wt, wv, wx, wy, ww []float64
	lastLinks          []clink // the links used in the latest Step
}

// Stats describes the latest Step.
type Stats struct {
	Phones   int // placed phones
	Located  int // … connected and not Lost
	Walking  int
	Links    int // proximity links used
	OwnLinks int // … of them found by this package
	Compared int // pairs compared
}

// New creates an estimator.
func New(cfg Config) *Estimator {
	e := &Estimator{phones: map[string]*phone{}, pairs: map[[2]string]*pairEv{}}
	e.SetConfig(cfg)
	return e
}

// Config returns the current settings.
func (e *Estimator) Config() Config { return e.cfg }

// SetConfig changes the settings (venue, entry, walls, switches), keeping
// the phones.
func (e *Estimator) SetConfig(cfg Config) {
	if cfg.Entry.Sigma <= 0 {
		cfg.Entry.Sigma = 1.5
	}
	e.cfg = cfg
	e.geom = newGeom(cfg)
}

// Stats is what the latest Step did.
func (e *Estimator) Stats() Stats { return e.stats }

func (e *Estimator) get(id string) *phone {
	p := e.phones[id]
	if p == nil {
		p = &phone{id: id}
		p.lev.init()
		e.phones[id] = p
	}
	return p
}

// Known reports whether the estimator holds a position for this phone.
func (e *Estimator) Known(id string) bool {
	p := e.phones[id]
	return p != nil && p.placed
}

// Join is a hello that carried no position. A phone the estimator already
// holds keeps its estimate (it reconnected), a little less sure for the
// time it was away; a new one starts at the entry spot, if there is one.
// It reports whether the phone now has a position.
func (e *Estimator) Join(id string, now int64) bool {
	p := e.get(id)
	p.gone = false
	if p.placed {
		return true
	}
	if !e.cfg.Entry.On {
		return false
	}
	en := e.cfg.Entry
	// Not all on the one point: each phone starts at a spot of its own
	// drawn (from its id) from where a person who just scanned the code
	// may be standing, so the picture at the entry is a queue as wide as
	// the uncertainty and not a stack that reads as a crush.
	h := mix64(hashID(id))
	u1 := (float64(h&0xffffff) + 1) / float64(1<<24+1)
	u2 := float64((h>>24)&0xffffff) / float64(1<<24)
	r := math.Min(en.Sigma*math.Sqrt(-2*math.Log(u1)), 2.5*en.Sigma)
	x, y := e.geom.inside(en.X+r*math.Cos(2*math.Pi*u2), en.Y+r*math.Sin(2*math.Pi*u2))
	p.kf.initAt(x, y, en.Sigma)
	p.placed, p.src, p.lastTick = true, SrcEntry, now
	p.x, p.y, p.acc = x, y, en.Sigma
	p.entryAt, p.tracked = now, 0
	return true
}

// Gone marks a phone disconnected; it is dropped ForgetMs later. Calling
// it again for a phone already gone changes nothing.
func (e *Estimator) Gone(id string, now int64) {
	if p := e.phones[id]; p != nil && !p.gone {
		p.gone, p.goneAt = true, now
	}
}

// Back marks a phone connected again; its estimate stands.
func (e *Estimator) Back(id string, now int64) {
	if p := e.phones[id]; p != nil {
		p.gone = false
	}
}

// Prune forgets every phone keep doesn't want.
func (e *Estimator) Prune(keep func(id string) bool) {
	for id := range e.phones {
		if !keep(id) {
			delete(e.phones, id)
		}
	}
}

// DR takes a phone's own dead reckoning (protocol "dr"): the steps it has
// counted and how far east and north (m) it has walked since its page
// loaded, all three running totals, so a lost message loses nothing. While
// a phone sends these, the server's own step analysis is not used for it.
// Steps without displacement mean the phone knows it walked but not which
// way. Totals that go backwards mean the page was reloaded.
func (e *Estimator) DR(id string, now int64, steps int, east, north float64) {
	p := e.phones[id]
	if p == nil || !finite(east) || !finite(north) || steps < 0 {
		return
	}
	if p.drSet && steps >= p.drSteps {
		de, dn := east-p.drE, north-p.drN
		n := steps - p.drSteps
		// A step is under 1.5 m: anything bigger is not walking.
		if d := math.Hypot(de, dn); d <= 1.5*float64(n)+0.5 && n <= 50 {
			if e.cfg.HasBearing {
				x, y := toMap(dn, de, e.cfg.Bearing)
				p.drX += x
				p.drY += y
			}
			p.drN0 += n
		}
	}
	p.drSet, p.drSteps, p.drE, p.drN, p.drAt = true, steps, east, north, now
}

// Remove forgets a phone at once.
func (e *Estimator) Remove(id string) { delete(e.phones, id) }

// Fix applies an absolute position measurement.
func (e *Estimator) Fix(id string, now int64, f Fix) {
	if !finite(f.X) || !finite(f.Y) {
		return
	}
	p := e.get(id)
	p.gone = false
	if f.Src == 0 {
		f.Src = SrcFix
	}
	if f.Exact {
		p.kf.initAt(f.X, f.Y, math.Max(f.Along, 0.05))
		p.placed, p.src, p.lastTick, p.blind = true, f.Src, now, 0
		p.entryAt, p.stood, p.walked = 0, 0, 0
		p.gpsSigma = 0 // the bias is unknown again; the next fix measures it here
		p.x, p.y, p.acc = f.X, f.Y, math.Max(f.Along, 0.05)
		return
	}
	al, ac := math.Max(f.Along, 0.05), math.Max(f.Across, 0.05)
	if !finite(al) || !finite(ac) {
		return
	}
	if !p.placed {
		p.kf.initAt(f.X, f.Y, math.Max(al, ac))
		p.placed, p.src, p.lastTick = true, f.Src, now
		p.x, p.y, p.acc = f.X, f.Y, math.Max(al, ac)
	}
	ux, uy := f.AX, f.AY
	if l := math.Hypot(ux, uy); l > 1e-9 && al != ac {
		ux, uy = ux/l, uy/l
	} else {
		ux, uy = 1, 0
		ac = math.Max(al, ac)
		al = ac
	}
	a2, c2 := al*al, ac*ac
	r := [3]float64{a2*ux*ux + c2*uy*uy, (a2 - c2) * ux * uy, a2*uy*uy + c2*ux*ux}
	p.kf.update(f.X, f.Y, r, false)
	p.src |= f.Src
	e.clampState(p)
}

// GPS applies a fix already converted to venue metres (not smoothed, not
// clamped), with its reported accuracy radius (m).
func (e *Estimator) GPS(id string, now int64, x, y, acc float64) {
	cfg := &e.cfg
	if !finite(x) || !finite(y) || !(acc > 0 && acc <= cfg.GPSMaxAcc) {
		return
	}
	p := e.get(id)
	p.gone = false
	// The phone reports the 68 % radius: 1.51 σ per axis.
	sb := acc / 1.51
	if !p.placed {
		// Nothing else known: the fix is all there is. Position and bias
		// can't be told apart yet; the position gets the fix and the
		// bias's uncertainty.
		p.kf.initAt(x, y, sb)
		p.kf.setBiasVar(sb)
		p.kf.p[0][2], p.kf.p[2][0] = -sb*sb, -sb*sb
		p.kf.p[1][3], p.kf.p[3][1] = -sb*sb, -sb*sb
		p.placed, p.src, p.lastTick, p.gpsSigma = true, SrcGPS, now, sb
		e.clampState(p)
		p.x, p.y, p.acc = p.kf.s[0], p.kf.s[1], sb
		return
	}
	if p.gpsSigma == 0 {
		p.kf.setBiasVar(sb) // first fix of a phone that started elsewhere
	}
	p.gpsSigma = sb
	rw := cfg.GPSFloor*cfg.GPSFloor + cfg.GPSWhite*cfg.GPSWhite*sb*sb
	if d2 := p.kf.gpsDist2(x, y, rw); d2 > cfg.GPSJumpGate {
		// Far from where the filter expects it: a reflected signal took
		// over (the bias jumped), not the person. Let the bias take it.
		nx, ny := x-p.kf.s[0]-p.kf.s[2], y-p.kf.s[1]-p.kf.s[3]
		j := (nx*nx + ny*ny) / 2
		p.kf.p[2][2] += j
		p.kf.p[3][3] += j
	}
	p.kf.update(x, y, [3]float64{rw, 0, rw}, true)
	// A bias far beyond what this receiver's accuracy allows is not a
	// bias: the person moved and no steps were seen (a phone in a bag, a
	// shuffle, no compass). The excess goes to the position.
	if lim := cfg.BiasMax * sb; lim > 0 {
		if b := math.Hypot(p.kf.s[2], p.kf.s[3]); b > lim {
			k := 1 - lim/b
			ex, ey := p.kf.s[2]*k, p.kf.s[3]*k
			p.kf.s[0] += ex
			p.kf.s[1] += ey
			p.kf.s[2] -= ex
			p.kf.s[3] -= ey
			v := ex*ex + ey*ey
			p.kf.p[0][0] += v
			p.kf.p[1][1] += v
		}
	}
	p.src |= SrcGPS
	e.clampState(p)
}

// Motion feeds one motion summary.
func (e *Estimator) Motion(id string, s Sample) {
	p := e.phones[id]
	if p == nil || s.T <= p.lastSampleT {
		return
	}
	cfg := &e.cfg
	if g, ok := unit3(s.G); ok {
		p.lev.set(g)
	}
	h1, v, h2 := p.lev.split(vec3{s.AX, s.AY, s.AZ})
	// The phone was moved to another pocket or turned over: what was known
	// about how it sits on the body is void.
	if p.n == 0 {
		p.gSlow = p.lev.g
	} else {
		for k := range p.gSlow {
			p.gSlow[k] += 0.03 * (p.lev.g[k] - p.gSlow[k])
		}
		if gs, ok := unit3(p.gSlow); ok && dot3(gs, p.lev.g) < carryMovedCos {
			p.gSlow = p.lev.g
			p.offX, p.offY, p.offN = 0, 0, 0
		}
	}
	// Compass: the heading of e1, from whichever axis is well defined.
	if cfg.HasBearing {
		a, ok := 0.0, false
		if !math.IsNaN(s.HD) {
			a, ok = p.lev.headingOfE1(s.HD, axisTop)
		}
		if !ok && !math.IsNaN(s.HB) {
			a, ok = p.lev.headingOfE1(s.HB, axisBack)
		}
		if ok {
			sn, cs := math.Sincos(a)
			if !p.hdSet || s.T-p.hdT > 1000 {
				p.hdC, p.hdS = cs, sn
			} else {
				p.hdC += 0.4 * (cs - p.hdC)
				p.hdS += 0.4 * (sn - p.hdS)
			}
			p.hdT, p.hdSet = s.T, true
		}
	}
	sm := samp{t: s.T, v: v, h1: h1, h2: h2, rot: s.Rot}
	// Band-pass for the proximity traces (0.15–1.5 Hz, as the detector's).
	dt := float64(s.T-p.lastSampleT) / 1000
	if !p.bpInit || dt > 1 || s.Rot > cfg.HandlingRot {
		p.lp1, p.lp2, p.dc1, p.dc2, p.bpInit = h1, h2, h1, h2, true
	} else {
		dt = math.Max(dt, 0.01)
		aLP := 1 - math.Exp(-dt*2*math.Pi*1.5)
		aHP := 1 - math.Exp(-dt*2*math.Pi*0.15)
		p.lp1 += aLP * (h1 - p.lp1)
		p.lp2 += aLP * (h2 - p.lp2)
		p.dc1 += aHP * (p.lp1 - p.dc1)
		p.dc2 += aHP * (p.lp2 - p.dc2)
	}
	sm.b1, sm.b2 = p.lp1-p.dc1, p.lp2-p.dc2
	p.lastSampleT = s.T
	p.ring[p.head] = sm
	p.head = (p.head + 1) % ringN
	if p.n < ringN {
		p.n++
	}
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// What the motion stream said about a phone over one step.
type motion struct {
	known  bool    // there is a motion stream for the interval
	walk   bool    // walked dist metres along (ux, uy)
	steps  bool    // stepped dist metres, direction unknown
	sure   bool    // … and the steps were counted by the phone itself
	dist   float64 // m
	ux, uy float64
	lenErr float64 // 1-σ error of dist, as a share of it
}

// predict moves a phone's filter from p.lastTick to now.
//
//   - Walked d metres along u: the position moves (sliding along a wall
//     it would cross), and gains the error of the distance along u and of
//     the heading across it. Both are the same from one step to the next
//     (the same legs, the same compass), so over a walk of length D they
//     add up to σ = k·D, not k·√D.
//   - Stepped, direction unknown (the phone's own count, or the server's
//     with BlindWalk on): held; σ grows by the distance.
//   - Standing: held, with StandDrift up to StandCap (StandStill), else
//     FollowDrift, which lets the fixes carry it.
//   - Silent (no motion stream): someone who was walking when last heard,
//     or who only just came in, may be anywhere SilentSpeed could take
//     them; someone who had settled is most likely still there (FreeDrift).
//   - Lost at the entry: started there and still not seen to walk
//     EntryMinWalk after EntryLingerS: σ grows by EntrySpeed until
//     something places it.
func (e *Estimator) predict(p *phone, now int64, m motion) {
	cfg := &e.cfg
	if now <= p.lastTick {
		return
	}
	dt := float64(now-p.lastTick) / 1000
	p.lastTick = now
	if p.gpsSigma > 0 {
		p.kf.biasStep(dt, cfg.BiasTauS, p.gpsSigma)
	}
	var dx, dy float64
	var Q [3]float64
	// Going somewhere unseen at speed v: the uncertainty grows with the
	// distance (the next stretch goes the same unknown way), not its root.
	unseen := func(d float64) {
		g := (2*p.blind + d) * d / 2
		Q[0] += g
		Q[2] += g
		p.blind += d
	}
	lost := p.entryAt > 0 && float64(now-p.entryAt) > cfg.EntryLingerS*1000 && p.tracked < cfg.EntryMinWalk
	p.walking = m.walk || m.steps
	switch {
	case m.walk:
		d := m.dist
		dx, dy = m.ux*d, m.uy*d
		if cfg.MapConstraints {
			dx, dy = e.geom.slide(p.kf.s[0], p.kf.s[1], dx, dy)
		}
		grow := func(k float64) float64 { return k * k * (2*p.walked + d) * d }
		a2 := grow(m.lenErr)
		c2 := grow(math.Sin(cfg.HeadingErrDeg * math.Pi / 180))
		Q = [3]float64{a2*m.ux*m.ux + c2*m.uy*m.uy, (a2 - c2) * m.ux * m.uy, a2*m.uy*m.uy + c2*m.ux*m.ux}
		p.walked += d
		p.tracked += d
		p.blind, p.stood, p.stillSince = 0, 0, 0
		p.src |= SrcSteps
	case m.steps && (cfg.BlindWalk || m.sure):
		unseen(m.dist)
		p.stillSince = 0
	case !m.known:
		switch {
		case !cfg.StandStill:
			Q[0], Q[2] = cfg.FollowDrift*cfg.FollowDrift*dt, cfg.FollowDrift*cfg.FollowDrift*dt
		case p.wasWalking || lost || (p.entryAt > 0 && now-p.entryAt < settleMs) || (p.stillSince > 0 && now-p.stillSince < settleMs/4):
			unseen(cfg.SilentSpeed * dt)
		default:
			Q[0], Q[2] = cfg.FreeDrift*cfg.FreeDrift*dt, cfg.FreeDrift*cfg.FreeDrift*dt
		}
	default: // standing, or stepping on the spot
		if p.stillSince == 0 {
			p.stillSince = now
		}
		switch {
		case lost:
			unseen(cfg.EntrySpeed * dt)
		case cfg.StandStill:
			q := e.standQ(p, dt)
			Q[0], Q[2] = q, q
			p.blind = 0
		default:
			Q[0], Q[2] = cfg.FollowDrift*cfg.FollowDrift*dt, cfg.FollowDrift*cfg.FollowDrift*dt
			p.blind = 0
		}
		p.walked *= math.Exp(-dt / 30) // a new walk is a new error
	}
	if m.known {
		p.wasWalking = m.walk || m.steps
	}
	p.kf.move(dx, dy, Q)
	if mx := cfg.MaxSigma * cfg.MaxSigma; p.kf.p[0][0] > mx || p.kf.p[1][1] > mx {
		f := mx / math.Max(p.kf.p[0][0], p.kf.p[1][1])
		p.kf.p[0][0] *= f
		p.kf.p[1][1] *= f
		p.kf.p[0][1] *= f
		p.kf.p[1][0] *= f
	}
}

// drFreshMs: a phone that sent its own dead reckoning this lately is
// followed by it.
const drFreshMs = 3000

// drStepLen is the length credited to a step the phone counted but could
// not give a direction for (m).
const drStepLen = 0.7

// drMotion is what the phone's own dead reckoning reported since the last
// Step.
func (e *Estimator) drMotion(p *phone) motion {
	dx, dy, n := p.drX, p.drY, p.drN0
	p.drX, p.drY, p.drN0 = 0, 0, 0
	m := motion{known: true, lenErr: e.cfg.DRLenErr}
	if d := math.Hypot(dx, dy); d > 1e-6 {
		m.walk, m.dist, m.ux, m.uy = true, d, dx/d, dy/d
	} else if n > 0 {
		m.steps, m.sure, m.dist = true, true, drStepLen*float64(n)
	}
	return m
}

// standQ is the variance a standing phone gains in dt seconds: StandDrift,
// up to StandCap in all since it last walked or was placed (someone
// standing still shifts their feet; they don't wander off).
func (e *Estimator) standQ(p *phone, dt float64) float64 {
	cfg := &e.cfg
	q := cfg.StandDrift * cfg.StandDrift * dt
	if c := cfg.StandCap * cfg.StandCap; p.stood+q > c {
		q = math.Max(0, c-p.stood)
	}
	p.stood += q
	return q
}

// clampState keeps the filter's position inside the walkable floor.
func (e *Estimator) clampState(p *phone) {
	if !e.cfg.MapConstraints || p.kf.sigma() < exactSigma {
		return // a phone placed exactly is where it was put
	}
	x, y := e.geom.inside(p.kf.s[0], p.kf.s[1])
	if x != p.kf.s[0] || y != p.kf.s[1] {
		p.kf.s[0], p.kf.s[1] = x, y
		p.src |= SrcMap
	}
}

// Step advances every phone to time now and runs the cooperative step.
// links are proximity links from outside, if the caller has any it trusts
// (the detector's wave edges are not: between roughly placed phones they
// join people riding the same wave, mostly not neighbours); mesh reports
// come in through Near. The result is sorted by id and valid until
// the next Step.
func (e *Estimator) Step(now int64, links []Link) []Position {
	cfg := &e.cfg
	e.seq++
	e.order = e.order[:0]
	for id, p := range e.phones {
		if p.gone && now-p.goneAt > cfg.ForgetMs {
			delete(e.phones, id)
			continue
		}
		if p.placed {
			e.order = append(e.order, p)
		}
	}
	sort.Slice(e.order, func(i, j int) bool { return e.order[i].id < e.order[j].id })
	e.stats = Stats{Phones: len(e.order)}
	for i, p := range e.order {
		p.idx = i
		dt := float64(now-p.lastTick) / 1000
		var m motion
		switch {
		case p.drSet && now-p.drAt <= drFreshMs:
			// The phone counts its own steps.
			m = e.drMotion(p)
		case !p.gone && p.n > 0 && now-p.lastSampleT <= 1500:
			// The motion stream covers the tick.
			m.known, m.lenErr = true, cfg.StepLenErr
			g, ok := e.pdr(p, now)
			if ok {
				p.gaitAt = now
				e.direct(p, &g)
			} else if p.gait.g != gaitStill && now-p.gaitAt <= gaitHoldMs {
				// Nothing to go on for a moment (summaries were lost):
				// someone who was walking is still walking.
				g = p.gait
			}
			p.gait = g
			if cfg.DeadReckon && dt > 0 {
				switch g.g {
				case gaitWalk:
					m.walk, m.dist, m.ux, m.uy = true, g.speed*dt, g.ux, g.uy
				case gaitBlind:
					m.steps, m.dist = true, g.speed*dt
				}
			}
		}
		e.predict(p, now, m)
		e.clampState(p)
		if p.walking {
			e.stats.Walking++
		}
		p.x, p.y = p.kf.s[0], p.kf.s[1]
		p.acc = p.kf.sigma()
		p.wasNear = p.near
		p.moved, p.near, p.links = false, false, 0
	}
	e.cooperate(now, links)
	e.out = e.out[:0]
	for _, p := range e.order {
		src := p.src
		if p.near {
			src |= SrcNear
		}
		if p.moved {
			src |= SrcMap
		}
		e.out = append(e.out, Position{ID: p.id, X: p.x, Y: p.y, Acc: math.Max(0.05, p.acc), Src: src,
			FX: p.kf.s[0], FY: p.kf.s[1], Walking: p.walking, Links: p.links, Lost: p.acc > cfg.LostSigma})
		if p.acc <= cfg.LostSigma && !p.gone {
			e.stats.Located++
		}
	}
	return e.out
}

// Position is a phone's estimate from the latest Step.
func (e *Estimator) Position(id string) (Position, bool) {
	p := e.phones[id]
	if p == nil || !p.placed {
		return Position{}, false
	}
	src := p.src
	if p.near {
		src |= SrcNear
	}
	return Position{ID: id, X: p.x, Y: p.y, Acc: math.Max(0.05, p.acc), Src: src, FX: p.kf.s[0], FY: p.kf.s[1],
		Walking: p.walking, Links: p.links, Lost: p.acc > e.cfg.LostSigma}, true
}
