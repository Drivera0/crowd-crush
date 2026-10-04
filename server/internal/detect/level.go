package detect

import "math"

// Levelling: making the sway signal independent of how the phone is carried.
//
// A phone that sends its gravity vector g (unit, device frame, pointing
// down) has every acceleration sample split into
//
//	vertical   = −a·g                    (up positive)
//	horizontal = (a·e1, a·e2)            with e1, e2 ⟂ g
//
// before any filtering, so a walking bounce stays out of the sway channel
// and a sideways push stays in it whatever the tilt. Only the axis of g
// matters: with −g the vertical flips sign and the horizontal plane is the
// same, and everything downstream is sign-blind (|correlation|, RMS).
//
// e1, e2 are the phone's own horizontal axes. For g = (0, −1, 0), the
// upright phone the detector assumed before, they are device x and z, so
// a phone that reports "upright" gives exactly the unlevelled numbers. As
// g moves, e1 is carried along with it (projected onto the new horizontal
// plane) rather than recomputed, so the basis has no jump anywhere on the
// sphere and a small error in g only moves it a little.
//
// What levelling cannot fix is the heading: e1 points an arbitrary way
// round the vertical, different on every phone. The detector therefore
// reduces a levelled phone's horizontal vector to its dominant direction
// over the correlation window (principal axis; resample), which for two
// neighbours hit by the same push is the same physical line, and compares
// neighbours by |correlation|, which ignores which way along that line
// each one counts as positive. The direction a wave travels still comes
// from where the phones stand and who moved first, never from device axes.
type level struct {
	on     bool
	g      [3]float64 // down, unit
	e1, e2 [3]float64 // horizontal basis
	ref    [3]float64 // g from HandlingTiltMs to 2 × HandlingTiltMs ago, for the tilt-handling test
	nxt    [3]float64 // the next ref
	nxtT   int64
}

// Gravity turns a wire value (protocol.Motion.G) into a Sample.G: the zero
// vector ("not sent") unless g is three finite numbers that aren't all ~0.
func Gravity(g []float64) [3]float64 {
	if len(g) != 3 {
		return [3]float64{}
	}
	v, ok := unit([3]float64{g[0], g[1], g[2]})
	if !ok {
		return [3]float64{}
	}
	return v
}

// unit normalises g; ok is false for the zero vector (no gravity sent) or
// anything that can't be a direction.
func unit(g [3]float64) ([3]float64, bool) {
	n := math.Sqrt(g[0]*g[0] + g[1]*g[1] + g[2]*g[2])
	if !(n > 0.2) || math.IsInf(n, 0) { // !(…) is also true for NaN
		return g, false
	}
	return [3]float64{g[0] / n, g[1] / n, g[2] / n}, true
}

func dot(a, b [3]float64) float64 { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }

func cross(a, b [3]float64) [3]float64 {
	return [3]float64{a[1]*b[2] - a[2]*b[1], a[2]*b[0] - a[0]*b[2], a[0]*b[1] - a[1]*b[0]}
}

// set takes a new gravity direction (unit) at time t. It reports whether
// gravity turned more than HandlingTiltDeg within about HandlingTiltMs: the
// phone is being handled.
func (l *level) set(g [3]float64, t int64, cfg *Config) (handled bool) {
	first := !l.on
	if first {
		// Start from device x (device z if x is vertical), so an upright
		// phone gets e1 = x, e2 = z.
		l.on = true
		l.e1 = [3]float64{1, 0, 0}
		if math.Abs(g[0]) > 0.9 {
			l.e1 = [3]float64{0, 0, 1}
		}
		l.ref, l.nxt, l.nxtT = g, g, t
	}
	if first || g != l.g {
		l.g = g
		// Carry e1 along: drop its component along the new g.
		d := dot(l.e1, g)
		e := [3]float64{l.e1[0] - d*g[0], l.e1[1] - d*g[1], l.e1[2] - d*g[2]}
		n := math.Sqrt(dot(e, e))
		if n < 1e-3 { // g jumped onto e1 (a 90° flip between two samples): any horizontal axis will do
			e = cross(g, [3]float64{g[1], g[2], g[0]})
			if n = math.Sqrt(dot(e, e)); n < 1e-3 {
				e = cross(g, [3]float64{1, 0, 0})
				n = math.Sqrt(dot(e, e))
			}
		}
		l.e1 = [3]float64{e[0] / n, e[1] / n, e[2] / n}
		l.e2 = cross(g, l.e1)
	}
	if cfg.HandlingTiltDeg <= 0 {
		return false
	}
	if t-l.nxtT >= cfg.HandlingTiltMs {
		l.ref = l.nxt
		l.nxt, l.nxtT = g, t
	}
	if dot(g, l.ref) < math.Cos(cfg.HandlingTiltDeg*math.Pi/180) {
		l.ref, l.nxt, l.nxtT = g, g, t
		return true
	}
	return false
}

// split turns a device-frame acceleration into (horizontal 1, vertical,
// horizontal 2), the slots the detector uses for an upright phone's x, y, z.
func (l *level) split(ax, ay, az float64) (h1, v, h2 float64) {
	a := [3]float64{ax, ay, az}
	return dot(a, l.e1), -dot(a, l.g), dot(a, l.e2)
}

// walking reports whether a levelled phone is being walked with: the
// vertical channel bounces to a rhythm (steps) and the horizontal one swings
// to a rhythm too (the leg carrying a pocket, or the body's side-to-side
// roll). People packed tightly enough to be crushed are not walking, and a
// push is not periodic within the lag range, so such a phone joins no pair
// and is not shown as swaying. A crowd jumping on the spot while a push
// passes through keeps a non-rhythmic horizontal trace and is not gated.
// Phones that don't send gravity are never gated (their outcome is unchanged).
//
// Walking goes on for a while, so a positive holds for WalkHoldMs: a
// window broken up by a handling episode (too few readings to measure the
// rhythm) doesn't let the phone through in between.
func (d *Detector) walking(p *phone, maxLag int) bool {
	cfg := &d.cfg
	// A roughly placed phone (motion.go) is gated whether or not it sends
	// gravity: its pairs are picked by motion alone, among everyone within
	// metres, and people walking to the same beat or setting off together
	// would otherwise pair up. Without gravity it is taken as upright.
	if !(p.lev.on || (p.acc > 0 && cfg.AccPairScale > 0)) || cfg.WalkRhythm <= 0 {
		return false
	}
	if p.sway >= cfg.EdgeMinSway && p.vrms >= cfg.WalkMinVert {
		// Half the usual overlap is enough to see a step rhythm.
		minOverlap := (len(p.v) - maxLag) / 2
		if rhythm(p.v, p.valid, maxLag, minOverlap) >= cfg.WalkRhythm &&
			rhythm(p.h, p.valid, maxLag, minOverlap) >= cfg.WalkRhythm {
			p.walkUntil = p.lastT + cfg.WalkHoldMs
		}
	}
	return p.lastT < p.walkUntil
}
