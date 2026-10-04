package locate

import "math"

// Levelling and heading: turning a device-frame acceleration into
// vertical, north and east.
//
// The split into vertical and horizontal is the detector's (detect/level.go):
// with the unit gravity vector g (device frame, pointing down),
//
//	vertical   = −a·g           (up positive)
//	horizontal = (a·e1, a·e2)   e1 ⟂ g carried along as g moves, e2 = g × e1
//
// A phone that sends no g is taken as upright (g = (0, −1, 0), e1 = x,
// e2 = z). In a right-handed device frame (x to the right of the screen, y
// to its top, z out of it: every real phone) turning from e1 to e2 is
// clockwise seen from above, the way compass headings count.
//
// The compass heading says which way round the vertical the phone is
// turned. hd is the heading of the top edge (+y), hb of the back (−z); the
// reference axis r is projected on the ground (rh = r − (r·g)g) and its
// angle in the (e1, e2) basis subtracted from the heading: that is the
// heading of e1, and with it every horizontal vector has a compass
// direction. A reference axis within about 25° of the vertical (|rh| <
// minRefLen) has no usable projection; a phone should send the other one.

const minRefLen = 0.42

type vec3 = [3]float64

func dot3(a, b vec3) float64 { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }

func cross3(a, b vec3) vec3 {
	return vec3{a[1]*b[2] - a[2]*b[1], a[2]*b[0] - a[0]*b[2], a[0]*b[1] - a[1]*b[0]}
}

// unit3 normalises v; ok is false for a vector that can't be a direction.
func unit3(v vec3) (vec3, bool) {
	n := math.Sqrt(dot3(v, v))
	if !(n > 0.2) || math.IsInf(n, 0) {
		return v, false
	}
	return vec3{v[0] / n, v[1] / n, v[2] / n}, true
}

type level struct {
	on     bool
	g      vec3
	e1, e2 vec3
}

func (l *level) init() {
	l.g = vec3{0, -1, 0}
	l.e1 = vec3{1, 0, 0}
	l.e2 = vec3{0, 0, 1}
}

// set takes a new gravity direction (unit).
func (l *level) set(g vec3) {
	if l.on && g == l.g {
		return
	}
	if !l.on {
		l.on = true
		l.e1 = vec3{1, 0, 0}
		if math.Abs(g[0]) > 0.9 {
			l.e1 = vec3{0, 0, 1}
		}
	}
	l.g = g
	d := dot3(l.e1, g)
	e := vec3{l.e1[0] - d*g[0], l.e1[1] - d*g[1], l.e1[2] - d*g[2]}
	n := math.Sqrt(dot3(e, e))
	if n < 1e-3 {
		e = cross3(g, vec3{g[1], g[2], g[0]})
		if n = math.Sqrt(dot3(e, e)); n < 1e-3 {
			e = cross3(g, vec3{1, 0, 0})
			n = math.Sqrt(dot3(e, e))
		}
	}
	l.e1 = vec3{e[0] / n, e[1] / n, e[2] / n}
	l.e2 = cross3(g, l.e1)
}

// split is (horizontal 1, vertical up, horizontal 2).
func (l *level) split(a vec3) (h1, v, h2 float64) {
	return dot3(a, l.e1), -dot3(a, l.g), dot3(a, l.e2)
}

// headingOfE1 is the compass heading of e1 (rad, clockwise from north)
// given the heading (deg) of the device axis ref; ok is false when that
// axis is too close to the vertical.
func (l *level) headingOfE1(headingDeg float64, ref vec3) (float64, bool) {
	d := dot3(ref, l.g)
	rh := vec3{ref[0] - d*l.g[0], ref[1] - d*l.g[1], ref[2] - d*l.g[2]}
	if math.Sqrt(dot3(rh, rh)) < minRefLen {
		return 0, false
	}
	ang := math.Atan2(dot3(rh, l.e2), dot3(rh, l.e1))
	return headingDeg*math.Pi/180 - ang, true
}

var (
	axisTop  = vec3{0, 1, 0}
	axisBack = vec3{0, 0, -1}
)

// toMap turns (north, east) into venue axes (x right, y down the map) for a
// venue whose map-up is bearing degrees clockwise from north.
func toMap(north, east, bearingDeg float64) (x, y float64) {
	s, c := math.Sincos(bearingDeg * math.Pi / 180)
	return east*c - north*s, -(east*s + north*c)
}
