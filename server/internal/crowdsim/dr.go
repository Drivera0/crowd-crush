package crowdsim

import (
	"math"
	"math/rand"
)

// Phone-side dead reckoning (Realism.PhoneDR): A MODEL, NOT A SIMULATION.
//
// The server can only dead-reckon from 100 ms summaries (internal/locate,
// pdr.go). A phone page could do it itself at the sensor's full rate, with
// the gyroscope, and send the result: steps counted and metres walked east
// and north, as running totals once a second (protocol "dr"). Nothing here
// simulates such a page's signal processing. This is a model of what it
// could deliver, so that the server side can be exercised and the possible
// gain put next to the server's own result. Every number below is an
// assumption, taken from what the pedestrian dead-reckoning literature
// reports for phones (Harle, "A survey of indoor inertial positioning
// systems for pedestrians", IEEE Comm. Surveys & Tutorials 15(3), 2013:
// step counts within a few per cent, distance within 5–10 % after
// calibration and heading as the dominant error; Brajdic & Harle, "Walk
// detection and step counting on unconstrained smartphones", UbiComp 2013:
// about 1–3 % step miscount for a phone in the hand, worse in a pocket or
// bag):
//
//   - a step is counted whenever the person moves at 0.25 m/s or more (a
//     real detector working on the heel strike catches short steps; a
//     shuffle at less than that is still invisible);
//   - each person's step length is off by a fixed factor, σ 8 %, plus 5 %
//     noise per report;
//   - the direction is the true direction of travel plus the compass's
//     fixed offset, plus half of its slow disturbance (the gyroscope
//     carries the heading through a disturbance; it cannot remove a
//     constant one), plus a fixed error per phone in how it believes it is
//     carried, σ 8°;
//   - a phone in a bag counts steps but cannot tell the direction: its
//     east and north stay where they are;
//   - nothing is counted while the phone is being handled.
//
// At strength s the three σ are × s.

type phoneDR struct {
	rng         *rand.Rand
	scale       float64
	carryErr    float64
	steps       float64
	east, north float64
	next        float64
	strength    float64
}

const (
	drMinSpeed = 0.25 // m/s
	drEvery    = 1.0  // s
)

func (p *phoneDR) init(strength float64, r *rand.Rand) {
	p.rng, p.strength = r, strength
	p.scale = 1 + 0.08*strength*r.NormFloat64()
	p.carryErr = 8 * deg * strength * r.NormFloat64()
	p.next = drEvery * r.Float64()
}

// tick advances the phone's dead reckoning by one tick; ok when a report
// is due.
func (p *phoneDR) tick(d *Device, in Raw, env Env, dt float64) (Event, bool) {
	handled := in.T < d.burstEnd || in.T < d.gestEnd
	if in.Gait > 0 && in.Speed >= drMinSpeed && !handled {
		// Steps: the gait phase advances 2π per step.
		p.steps += math.Max(1.6, math.Min(2.2, 1.6+0.5*(in.Speed-0.6))) * dt
		if d.carry != CarryBag {
			dist := in.Speed * dt * p.scale * (1 + 0.05*p.strength*p.rng.NormFloat64())
			az := Azimuth(math.Cos(in.Dir), math.Sin(in.Dir), env.Bearing)*deg + d.cmp.hard + 0.5*d.cmp.ou + p.carryErr
			p.north += dist * math.Cos(az)
			p.east += dist * math.Sin(az)
		}
	}
	if in.T < p.next {
		return Event{}, false
	}
	p.next = in.T + drEvery
	return Event{Kind: EvDR, ID: d.ID, Steps: int(p.steps), East: r2c(p.east), North: r2c(p.north)}, true
}
