package crowdsim

import (
	"math"
	"math/rand"
)

// Heading: what a phone needs to send for dead reckoning (Realism.Heading,
// used by internal/locate). Off (0) in every preset.
//
// # Compass
//
// Every 100 ms summary carries the phone's compass heading in degrees
// clockwise from north: HD, the heading of its top edge (the device's +y
// axis projected on the ground), or, when the top edge points more up or
// down than along the ground (a phone upright in a pocket or on the chest),
// HB, the heading of its back (the way the rear camera looks, −z). One of
// the two is always well defined. It is computed from where the phone
// really points (carry orientation, the pocket's swing, the body's facing,
// the venue's bearing) plus an indoor compass's error at strength 1:
//
//   - a fixed offset per phone, σ 8° (soft/hard-iron left uncalibrated, a
//     magnetic case clasp);
//   - a slowly varying disturbance (Ornstein–Uhlenbeck, correlation time
//     15–40 s): σ 10° for 70 % of the phones and 30° for the rest (standing
//     next to steel, a speaker stack, a barrier). Indoor heading errors of
//     tens of degrees are the normal finding: Afzal, Renaudin & Lachapelle,
//     "Magnetic field based heading estimation for pedestrian navigation
//     environments", IPIN 2011; Li et al., "How feasible is the use of
//     magnetic field alone for indoor positioning?", IPIN 2012;
//   - noise σ 3° per summary, rounded to the degree.
//
// # Gait
//
// The walking signal of phone.go is a vertical bounce at the step frequency
// and a small lateral sway at half of it. Real gait also speeds up and
// slows down along the walking direction once per step: the body is an
// inverted pendulum over the standing leg, slowest at the top of the arc
// (mid-stance) and fastest at the bottom (double support), so the forward
// acceleration leads the vertical one by a quarter of a step, with
// amplitude ≈ g/(v·ω) of it (≈ 0.6 at 1.3 m/s and 2 steps/s; Cavagna &
// Margaria 1966; Kuo 2007). That component is what lets a server tell which
// way a pocketed phone is walking (Kourogi & Kurata 2003), so a phone with
// Heading on gets it: 0.85·v m/s² ± 30 % per person, phase ± 17°, along the
// direction the person is moving (not the way the body faces: someone
// edging through a crowd moves partly sideways).
//
// # Handedness
//
// The body frame of phone.go (x right, y up, z forward) is left-handed, and
// so is every device frame rotated out of it. Nothing cared, because the
// detector is blind to the sign of an axis. A heading is not: turning from
// one axis to another clockwise or anticlockwise is the whole point. A
// phone with Heading on therefore reports the mirror image, device x
// negated (acceleration and gravity vector): for a chest phone x is then
// the body's left, y up and z forward (out of the screen), a real phone's
// right-handed frame.

type compass struct {
	on         bool
	rng        *rand.Rand
	hard       float64 // rad
	sigma, tau float64
	ou         float64
	faAmp      float64
	faPh       float64
}

const (
	compassHardSD  = 8 * deg
	compassCalm    = 10 * deg
	compassBad     = 30 * deg
	compassBadPart = 0.3
	compassNoise   = 3 * deg
	// headingFlat: the top edge's projection on the ground must be at least
	// this long (of 1) for HD; otherwise the phone sends HB.
	headingFlat = 0.6
	foreAftAmp  = 0.85
)

func (c *compass) init(strength float64, r *rand.Rand) {
	c.on, c.rng = true, r
	c.hard = compassHardSD * strength * r.NormFloat64()
	c.sigma = compassCalm * strength
	if r.Float64() < compassBadPart {
		c.sigma = compassBad * strength
	}
	c.tau = 15 + 25*r.Float64()
	c.ou = c.sigma * r.NormFloat64()
	c.faAmp = foreAftAmp * (0.7 + 0.6*r.Float64())
	c.faPh = 0.3 * r.NormFloat64()
}

func (c *compass) step(dt float64) {
	c.ou += -c.ou*dt/c.tau + c.sigma*math.Sqrt(2*dt/c.tau)*c.rng.NormFloat64()
}

// foreAft is the gait's acceleration along the direction of travel this tick.
func (c *compass) foreAft(in Raw) float64 {
	if in.Gait <= 0 {
		return 0
	}
	return in.Gait * c.faAmp * math.Min(in.Speed, 1.8) * math.Cos(in.Step+c.faPh)
}

// Azimuth is the compass direction (degrees clockwise from north, 0–360)
// of the venue-frame vector (vx, vy), y pointing down the map, for a venue
// whose map-up is bearing degrees clockwise from north.
func Azimuth(vx, vy, bearing float64) float64 {
	s, c := math.Sincos(bearing * deg)
	up := -vy
	east := vx*c + up*s
	north := -vx*s + up*c
	a := math.Atan2(east, north) / deg
	if a < 0 {
		a += 360
	}
	return a
}

// report is what the phone sends: hd (top edge) or hb (back), whichever is
// better defined, with the compass error.
func (c *compass) report(q m3, face, bearing float64) (hd, hb *float64) {
	fx, fy := math.Cos(face), math.Sin(face)
	rx, ry := -fy, fx
	// Device axes in the body frame are the columns of q.
	az := func(right, fwd float64) *float64 {
		vx, vy := right*rx+fwd*fx, right*ry+fwd*fy
		a := Azimuth(vx, vy, bearing) + (c.hard+c.ou+compassNoise*c.rng.NormFloat64())/deg
		a = math.Mod(math.Round(a), 360)
		if a < 0 {
			a += 360
		}
		return &a
	}
	if math.Hypot(q[1], q[7]) >= headingFlat {
		return az(q[1], q[7]), nil
	}
	return nil, az(-q[2], -q[8])
}

// Facing is the way this person's body faces (rad, venue frame: 0 = +x,
// y down): where they are walking to, or the stage while standing.
func (a *Agent) Facing() float64 { return a.face }
