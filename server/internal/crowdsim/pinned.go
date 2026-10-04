package crowdsim

import "math"

// Pinned bodies: real people standing in the simulated crowd. While a
// simulation runs, every real phone connected to the server is a body
// fixed at the phone's venue position (the app sets them before each tick,
// see app/hybrid.go). Simulated people can't walk through one: they feel
// the same social repulsion, body compression and sliding friction as
// against another person (Helbing 2000), so they go around it and, in a
// crush, press against it. A pinned body never moves and carries no
// simulated phone; the real phone is its phone. A body that has just been
// pinned fades in over pinFadeSec, so a simulated person who happens to be
// standing on that spot is eased aside instead of thrown.
//
// Press: the people within a radius of a point lean toward it, a steady
// force on each of them on top of whatever they were doing (SetPress). The
// app's "surge around the phones" uses it to pack the crowd around the
// pinned bodies; the contact forces, pressure and ground truth then follow
// from the physics as in any crush.

// PinR is a pinned body's radius (m): an average adult.
const PinR = 0.23

// Pin is one pinned body.
type Pin struct {
	ID   string
	X, Y float64

	comp float64 // Σ|body compression| on it over the last physics step (N)
	gain float64 // 0 → 1 while it fades in
}

// pinFadeSec is how long a new pinned body takes to become solid.
const pinFadeSec = 1.0

// Press is a crowd leaning toward a point: everyone within R of (X, Y)
// pushes toward it with Acc m/s² (their mass × Acc newtons), fading to
// nothing over the outer third of R and within 0.5 m of the point.
type Press struct {
	X, Y, R float64
	Acc     float64
}

// SetPress starts, changes or (nil) ends the press.
func (w *World) SetPress(p *Press) { w.press = p }

// Press is the current press, nil if none.
func (w *World) Press() *Press { return w.press }

// Pressure is the crowd's pressure on the pinned body (N/m), like
// Agent.Pressure.
func (p Pin) Pressure() float64 { return p.comp / (2 * math.Pi * PinR) }

// SetPins replaces the pinned bodies. An empty list leaves the world
// exactly as it is without them.
func (w *World) SetPins(pins []Pin) {
	old := map[string]Pin{}
	for _, p := range w.pins {
		old[p.ID] = p
	}
	w.pins = append(w.pins[:0], pins...)
	for i := range w.pins {
		o := old[w.pins[i].ID]
		w.pins[i].comp, w.pins[i].gain = o.comp, o.gain
	}
}

// Pins are the pinned bodies (read-only use).
func (w *World) Pins() []Pin { return w.pins }

// resetPins starts a physics step: clears the per-step compression and
// fades new pinned bodies in.
func (w *World) resetPins() {
	for i := range w.pins {
		p := &w.pins[i]
		p.comp = 0
		p.gain = math.Min(1, p.gain+Dt/SubSteps/pinFadeSec)
	}
}

// pinForce adds the pinned bodies' forces on agent a: social repulsion into
// a.sx, a.sy (so a stander's dead-band applies), contact into a.fx, a.fy.
func (w *World) pinForce(a *Agent, h float64) {
	if pr := w.press; pr != nil && pr.R > 0 {
		dx, dy := pr.X-a.X, pr.Y-a.Y
		if d := math.Hypot(dx, dy); d < pr.R && d > 1e-6 {
			k := math.Min(1, (pr.R-d)/(pr.R/3)) * math.Min(1, d/0.5)
			a.fx += a.M * pr.Acc * k * dx / d
			a.fy += a.M * pr.Acc * k * dy / d
		}
	}
	for i := range w.pins {
		p := &w.pins[i]
		dx, dy := a.X-p.X, a.Y-p.Y
		rij := a.R + PinR
		d2 := dx*dx + dy*dy
		if d2 > (rij+cutExtra)*(rij+cutExtra) {
			continue
		}
		d := math.Sqrt(d2)
		if d < 1e-6 { // exactly on top of it: pick a direction
			dx, dy, d = 1e-3*(float64(a.ID%7)-3), 1e-3, 1e-3
		}
		nx, ny := dx/d, dy/d
		ov := rij - d
		f := p.gain * A * math.Exp(math.Min(ov, 0.4)/B)
		a.sx += f * nx
		a.sy += f * ny
		if ov > 0 {
			c := p.gain * K * ov
			a.comp += c
			p.comp += c
			a.fx += c * nx
			a.fy += c * ny
			tx, ty := -ny, nx
			vt := a.VX*tx + a.VY*ty
			kap := math.Min(p.gain*Kappa*ov, 0.5*a.M/h)
			a.fx -= kap * vt * tx
			a.fy -= kap * vt * ty
		}
	}
}
