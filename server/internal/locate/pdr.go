package locate

import (
	"math"
	"math/cmplx"
)

// Dead reckoning from 100 ms motion summaries.
//
// Steps. Walking shows in the levelled vertical acceleration as a bounce
// at the step frequency (1.4–2.5 steps a second). Over the last pdrWindowMs
// the vertical trace is demodulated at each candidate frequency (a
// windowed Fourier sum at the samples' own times, so jitter and lost
// summaries don't matter); the strongest one, V, is the step rhythm if it
// is strong (amplitude ≥ MinStepAmp) and clean (most of the vertical
// variance). 10 Hz summaries are enough for that: the step frequency is
// well under the 5 Hz Nyquist limit and a 100 ms mean only takes 6 % off a
// 2 Hz bounce. What they lose is the heel strike's shape; counting steps by
// rhythm doesn't need it.
//
// Step length is the inverted pendulum's (Zijlstra & Hof, "Assessment of
// spatio-temporal gait parameters from trunk accelerations during human
// walking", Gait & Posture 18, 2003): the trunk rises and falls by h over a
// step, h = 2A/ω² for a bounce of amplitude A at ω = 2π · step frequency,
// and a leg of length l swinging through that arc covers 2·√(2lh − h²),
// times a correction factor. One leg length and one factor for everyone.
//
// Which way, relative to the phone. The body is an inverted pendulum over
// the standing leg, so it speeds up and slows down once per step and the
// forward acceleration leads the vertical one by a quarter of a step. The
// part of the levelled horizontal acceleration at the step frequency a
// quarter cycle ahead of the vertical, d = Im(H/V), therefore points along
// the walk, in the phone's own levelled axes: it says how the phone sits
// on the body (screen forward on the chest, sideways in a pocket). The
// part in phase with the vertical is what a wrong gravity vector leaks
// from the bounce, and is ignored. One window's d is noisy (the body's own
// starts, stops and jostles are in the same band), but how the phone sits
// doesn't change from step to step: d is averaged as a unit vector over
// a few windows (offX, offY; see offAlpha), and forgotten
// when gravity says the phone was moved (carryMovedCos). A phone whose d
// points anywhere (a bag swinging behind the body's motion) never gets a
// steady mean and has no direction.
//
// Which way on the map. The compass heading of the phone's levelled axes
// (level.go), read now, plus that offset. A turn of the body shows in the
// compass at once, so the direction follows it without waiting for the
// gait window.
//
// The outcome for a tick is one of
//
//   - still: no step rhythm (or a bounce with nothing horizontal in step:
//     jumping on the spot). The position is held (zero-velocity update);
//   - walking (ux, uy): steps × length along the direction;
//   - walking somewhere: a step rhythm and horizontal motion in step, but
//     no usable direction (no compass, no steady offset). The position is
//     held and its uncertainty grows by the distance walked.

const (
	pdrWindowMs  = 1700
	pdrMinSpanMs = 1200
	pdrMinN      = 10
	dirWindowMs  = 3200 // window for the horizontal-to-vertical ratio
	dirMinSpanMs = 2400
	stepFMin     = 1.4
	stepFMax     = 2.5
	stepFStep    = 0.1
	legLength    = 0.9 // m
	stepLenK     = 1.6 // correction factor, fitted at normal walking speed
	maxWalkSpeed = 2.2 // m/s
	offMinN      = 4   // windows before the carry offset is used
	offMinR      = 0.6 // … and how well they must agree (mean resultant length)

	headingMaxMs = 2500 // a compass reading older than this is not used (a phone may send its heading once a second)
)

type gait int

const (
	gaitStill gait = iota
	gaitWalk
	gaitBlind // walking, direction unknown
)

type pdrOut struct {
	g      gait
	speed  float64 // m/s
	ux, uy float64 // unit direction in map axes (gaitWalk)
	freq   float64
	amp    float64 // vertical amplitude at the step frequency (m/s²)
	quad   float64 // |Im(H/V)|
	// The walk in the phone's levelled axes from this window, when the
	// quarter-cycle test passed.
	d1, d2 float64
	hasD   bool
}

// pdr analyses the samples in (now − pdrWindowMs, now]. ok is false when
// the window can't be judged (too few samples).
func (e *Estimator) pdr(p *phone, now int64) (out pdrOut, ok bool) {
	cfg := &e.cfg
	// Collect the window (newest first).
	e.wt, e.wv, e.wx, e.wy = e.wt[:0], e.wv[:0], e.wx[:0], e.wy[:0]
	for i := p.n - 1; i >= 0; i-- {
		s := p.at(i)
		if s.t <= now-pdrWindowMs {
			break
		}
		if s.t > now {
			continue
		}
		e.wt = append(e.wt, float64(s.t-now)/1000)
		e.wv = append(e.wv, s.v)
		e.wx = append(e.wx, s.h1)
		e.wy = append(e.wy, s.h2)
	}
	n := len(e.wt)
	// Rotation is no reason to skip a window: a quick turn of the body
	// reads as a burst of it, and every sample is levelled on its own. A
	// phone being handled fails the tests below.
	if n < pdrMinN || (e.wt[0]-e.wt[n-1])*1000 < pdrMinSpanMs {
		return pdrOut{}, false
	}
	// Hann window over the span, means removed.
	t0, t1 := e.wt[n-1], e.wt[0]
	e.ww = e.ww[:0]
	var sw, mv, mx, my float64
	for i := 0; i < n; i++ {
		w := 0.5 - 0.5*math.Cos(2*math.Pi*(e.wt[i]-t0+0.05)/(t1-t0+0.1))
		e.ww = append(e.ww, w)
		sw += w
		mv += w * e.wv[i]
		mx += w * e.wx[i]
		my += w * e.wy[i]
	}
	mv, mx, my = mv/sw, mx/sw, my/sw
	var varV float64
	for i := 0; i < n; i++ {
		d := e.wv[i] - mv
		varV += e.ww[i] * d * d
	}
	varV /= sw
	if varV < 1e-6 {
		return pdrOut{}, true
	}
	best, bestF := 0.0, 0.0
	var bestV complex128
	for f := stepFMin; f <= stepFMax+1e-9; f += stepFStep {
		var v complex128
		om := 2 * math.Pi * f
		for i := 0; i < n; i++ {
			s, c := math.Sincos(-om * e.wt[i])
			v += complex(e.ww[i]*(e.wv[i]-mv)*c, e.ww[i]*(e.wv[i]-mv)*s)
		}
		if a := cmplx.Abs(v); a > best {
			best, bestF, bestV = a, f, v
		}
	}
	amp := 2 * best / sw
	// A pure sinusoid of amplitude A has variance A²/2.
	purity := amp * amp / 2 / varV
	out = pdrOut{freq: bestF, amp: amp}
	if amp < cfg.MinStepAmp || purity < cfg.MinStepPurity {
		return out, true
	}
	out.speed = math.Min(stepLength(amp, bestF)*bestF, maxWalkSpeed)
	// Horizontal against vertical at the step frequency, in the phone's
	// levelled axes, over the longer window.
	cx, cy, ok := e.ratio(p, now, bestF)
	if !ok {
		var hx, hy complex128
		om := 2 * math.Pi * bestF
		for i := 0; i < n; i++ {
			s, c := math.Sincos(-om * e.wt[i])
			k := complex(e.ww[i]*c, e.ww[i]*s)
			hx += k * complex(e.wx[i]-mx, 0)
			hy += k * complex(e.wy[i]-my, 0)
		}
		cx, cy = hx/bestV, hy/bestV
	}
	qx, qy := imag(cx), imag(cy)
	quad := math.Hypot(qx, qy)
	inph := math.Hypot(real(cx), real(cy))
	out.quad = quad
	switch {
	case quad >= cfg.MinQuad && quad >= inph*cfg.QuadOverInPhase:
		out.g = gaitBlind
		out.d1, out.d2, out.hasD = qx/quad, qy/quad, true
	case math.Hypot(quad, inph) >= cfg.MinBlindRatio:
		out.g = gaitBlind
	default:
		// A bounce with nothing horizontal in step: jumping on the spot.
		out.speed = 0
	}
	return out, true
}

// ratio is H/V at frequency f over the last dirWindowMs: long enough to
// tell the step frequency from half of it, where the body sways from foot
// to foot and a dancer sways to the beat (the short window's resolution is
// about 1.2 Hz, so a sway at 1 Hz would leak into a 2 Hz step rhythm and
// read as horizontal motion in step). ok is false when the phone has less
// than dirMinSpanMs of samples.
func (e *Estimator) ratio(p *phone, now int64, f float64) (cx, cy complex128, ok bool) {
	e.wt, e.wv, e.wx, e.wy = e.wt[:0], e.wv[:0], e.wx[:0], e.wy[:0]
	for i := p.n - 1; i >= 0; i-- {
		s := p.at(i)
		if s.t <= now-dirWindowMs {
			break
		}
		if s.t > now {
			continue
		}
		e.wt = append(e.wt, float64(s.t-now)/1000)
		e.wv = append(e.wv, s.v)
		e.wx = append(e.wx, s.h1)
		e.wy = append(e.wy, s.h2)
	}
	n := len(e.wt)
	if n < pdrMinN || (e.wt[0]-e.wt[n-1])*1000 < dirMinSpanMs {
		return 0, 0, false
	}
	t0, t1 := e.wt[n-1], e.wt[0]
	var sw, mv, mx, my float64
	e.ww = e.ww[:0]
	for i := 0; i < n; i++ {
		w := 0.5 - 0.5*math.Cos(2*math.Pi*(e.wt[i]-t0+0.05)/(t1-t0+0.1))
		e.ww = append(e.ww, w)
		sw += w
		mv += w * e.wv[i]
		mx += w * e.wx[i]
		my += w * e.wy[i]
	}
	mv, mx, my = mv/sw, mx/sw, my/sw
	var v, hx, hy complex128
	om := 2 * math.Pi * f
	for i := 0; i < n; i++ {
		s, c := math.Sincos(-om * e.wt[i])
		k := complex(e.ww[i]*c, e.ww[i]*s)
		v += k * complex(e.wv[i]-mv, 0)
		hx += k * complex(e.wx[i]-mx, 0)
		hy += k * complex(e.wy[i]-my, 0)
	}
	if cmplx.Abs(v) < 1e-9 {
		return 0, 0, false
	}
	return hx / v, hy / v, true
}

// direct gives a stepping phone its direction on the map: the compass
// heading of its levelled axes now, plus how it sits on the body.
func (e *Estimator) direct(p *phone, g *pdrOut) {
	if g.g == gaitStill {
		return
	}
	if g.hasD {
		a := math.Max(1/float64(p.offN+1), offAlpha)
		p.offX += a * (g.d1 - p.offX)
		p.offY += a * (g.d2 - p.offY)
		p.offN++
	}
	r := math.Hypot(p.offX, p.offY)
	if p.offN < offMinN || r < offMinR || !p.hdSet || p.lastSampleT-p.hdT > headingMaxMs || !e.cfg.HasBearing {
		return
	}
	l := math.Hypot(p.hdC, p.hdS)
	if l < 0.5 {
		return
	}
	// e1 points (hdC, hdS) as (north, east); the walk is (offX, offY) in
	// (e1, e2), and e1 → e2 is clockwise.
	c, s := p.hdC/l, p.hdS/l
	o1, o2 := p.offX/r, p.offY/r
	north, east := c*o1-s*o2, s*o1+c*o2
	g.ux, g.uy = toMap(north, east, e.cfg.Bearing)
	g.g = gaitWalk
}

// stepLength is the length of one step (m) for a vertical bounce of
// amplitude amp (m/s²) at freq steps per second.
func stepLength(amp, freq float64) float64 {
	om := 2 * math.Pi * freq
	h := 2 * amp / (om * om)
	return stepLenK * 2 * math.Sqrt(math.Max(0, 2*legLength*h-h*h))
}

// offAlpha is the weight of one more window in the carry offset once there
// are several: 0.4 per 250 ms step, about half a second. Longer averaging
// gives a steadier offset but ties the direction to the way the body
// faces, and someone edging through a crowd doesn't walk the way they face.
var offAlpha = 0.4
