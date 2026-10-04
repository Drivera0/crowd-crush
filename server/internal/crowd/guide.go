package crowd

import (
	"math"

	"github.com/Drivera0/crowd-crush/server/internal/detect"
)

// Personal guidance ("move this way"): for a phone in danger, the direction
// its owner should move.
//
//   - Density: down the gradient of a kernel density estimate of every
//     phone (Gaussian, GuideSigma), i.e. toward fewer people. Where the
//     gradient is flat (the middle of a blob) or points into a wall or out
//     of the venue, the least dense of GuideProbes directions GuideProbeM
//     away that isn't blocked (preferring the gradient's way) wins.
//   - Push: sideways to the push's travel direction, on the less dense
//     side, slightly with the push (diagonal), never against it: the usual
//     crowd-safety advice for a surge.
//   - Exit: the nearest open exit within GuideExitDeg of that direction and
//     GuideExitM is blended in (equal weight) and named.
//   - Stability: the direction is smoothed (EMA, GuideTauMs) and the one
//     shown only changes when the smoothed one turns more than GuideHoldDeg
//     away from it, so the arrow doesn't jitter.
//   - Rough positions (GuideIn.Acc > 0, a GPS fix good to ± Acc metres): the
//     fine gradient at a dot that may be 5 m from its owner is noise. The
//     direction is then the expected one over where the owner may really
//     be: the gradient of the density smoothed by the phone's own position
//     error (and each other phone's), which far from a crowd's centre is
//     simply "away from it". Near the centre that gradient is weak and says
//     little; the direction then leans on the least dense way out at long
//     range (GuideProbeM + 2σ away, inside the venue). A push direction
//     worked out from rough positions is not used. Move.Conf says how much
//     the arrow can be trusted (roughConf).
const (
	GuideSigma   = 1.5  // m
	GuideProbeM  = 2.0  // m
	GuideProbes  = 16   // directions tried when the gradient is no use
	GuideExitM   = 25.0 // m
	GuideExitDeg = 60.0 // degrees either side
	GuideTauMs   = 2000 // ms
	GuideHoldDeg = 30.0 // degrees
	leaveWeight  = 2.0  // the way out of a rule area counts twice the density direction
	pushForward  = 0.4  // how much of the push direction is added to the sideways move
	flatGrad     = 0.05 // |∇ρ|·σ below this fraction of ρ counts as flat
	edgeMargin   = 0.3  // m: a probe this close to the venue edge counts as outside
	// AccToSigma: a reported accuracy radius is the 68 % radius of a
	// two-dimensional error; per axis that is a σ of radius / 1.51.
	AccToSigma = 1.51
	// roughGradFull: a relative gradient |∇ρ|·h/ρ of this much or more (the
	// phone is about a kernel width from the crowd's centre) is trusted
	// fully; below it the long-range probe is mixed in.
	roughGradFull = 0.5
	// GuideConfAcc: Move.Conf = 1 / (1 + (Acc / GuideConfAcc)²): 0.64 at
	// 3 m, 0.5 at 4 m, 0.28 at 6.4 m, 0.14 at 10 m. Set from the evaluation
	// (docs/EVAL.md): against the arrow worked out from everyone's true
	// position, an arrow from a phone that reports ± 3 m is within 45° in
	// 50–66 % of cases; from one that reports ± 6 m in 38 %, with as many
	// pointing more than 90° wrong (a random arrow: 25 % and 50 %).
	GuideConfAcc = 4.0
	// GuideMinConf: below this an arrow should not be shown (the phone gets
	// a plain instruction instead).
	GuideMinConf = 0.5
	lessCrowded  = "less crowded side"
	reasonPush   = "push"
	reasonDense  = "density"
)

// Exit is an open exit: the segment (X0, Y0)–(X1, Y1).
type Exit struct {
	ID, Name       string
	X0, Y0, X1, Y1 float64
}

// Geom is what guidance needs to know about the venue: its size, walls
// (including the stage outline) and open exits, in venue metres.
type Geom struct {
	W, H  float64
	Walls [][4]float64
	Exits []Exit
}

// GuideIn is a phone in danger. Reason is "push" or "density"; PushX/PushY
// is the push's travel direction (any length; zero = unknown, treated as
// density).
type GuideIn struct {
	ID   string
	X, Y float64
	// Acc is the accuracy radius of X, Y (m); 0 = exact.
	Acc          float64
	Reason       string
	PushX, PushY float64
	// Leave, when set, is an area the phone should get out of (an area
	// turned red by a staff rule, e.g. over capacity): the direction leads
	// to the nearest point outside it, blended with lower density.
	Leave [][2]float64
}

// Move is the guidance for one phone: a unit vector (x right, y down), the
// exit it leads to (or "less crowded side") and why.
type Move struct {
	DX, DY float64
	To     string
	Reason string
	// Conf is how far the arrow can be trusted, 0..1: 1 for a phone placed
	// by hand, lower the rougher its position (GuideConfAcc). Below
	// GuideMinConf a plain instruction ("move to where there is more room")
	// is more honest than an arrow.
	Conf float64
}

type guideState struct {
	sx, sy float64 // smoothed direction (unit)
	dx, dy float64 // direction shown
	t      int64
}

// Guide keeps each phone's smoothed direction. Not safe for concurrent use.
type Guide struct {
	st map[string]*guideState
}

// NewGuide creates an empty guide.
func NewGuide() *Guide { return &Guide{st: map[string]*guideState{}} }

// Update works out guidance at time now for the phones in danger, from
// every phone's position pts (people/m² = phones/m² ÷ participation). Phones
// not in the list lose their state (safe again: no move).
func (g *Guide) Update(now int64, pts []Point, participation float64, geom Geom, in []GuideIn) map[string]Move {
	out := make(map[string]Move, len(in))
	keep := make(map[string]bool, len(in))
	for _, p := range in {
		keep[p.ID] = true
		rx, ry, ex, ey, exit := Direction(pts, participation, geom, p)
		reason := reasonDense
		if p.Reason == reasonPush && math.Hypot(p.PushX, p.PushY) > 1e-9 && p.Acc <= 0 {
			reason = reasonPush
		}
		s := g.st[p.ID]
		if s == nil {
			s = &guideState{sx: rx, sy: ry, dx: rx, dy: ry, t: now}
			g.st[p.ID] = s
		} else {
			dt := float64(max(0, now-s.t))
			a := 1 - math.Exp(-dt/GuideTauMs)
			nx, ny := s.sx+a*(rx-s.sx), s.sy+a*(ry-s.sy)
			if l := math.Hypot(nx, ny); l > 1e-3 {
				s.sx, s.sy = nx/l, ny/l
			} else { // turned right round: take the new one
				s.sx, s.sy = rx, ry
			}
			s.t = now
			if s.sx*s.dx+s.sy*s.dy < math.Cos(GuideHoldDeg*math.Pi/180) {
				s.dx, s.dy = s.sx, s.sy
			}
		}
		to := lessCrowded
		if exit != "" && s.dx*ex+s.dy*ey >= math.Cos(GuideExitDeg*math.Pi/180) {
			to = exit
		}
		out[p.ID] = Move{DX: s.dx, DY: s.dy, To: to, Reason: reason, Conf: Conf(p.Acc)}
	}
	for id := range g.st {
		if !keep[id] {
			delete(g.st, id)
		}
	}
	return out
}

// Direction is the unsmoothed guidance for one phone: the unit direction
// (dx, dy) and, when an exit was blended in, the unit vector toward it and
// its name.
func Direction(pts []Point, participation float64, geom Geom, p GuideIn) (dx, dy, ex, ey float64, exit string) {
	if participation <= 0 {
		participation = 1
	}
	dens := func(x, y float64) float64 { return KDE(pts, x, y) / participation }
	blocked := func(ux, uy float64) bool {
		qx, qy := p.X+GuideProbeM*ux, p.Y+GuideProbeM*uy
		if qx < edgeMargin || qy < edgeMargin || qx > geom.W-edgeMargin || qy > geom.H-edgeMargin {
			// Leaving the venue is only blocked if it moves further out.
			if (qx < edgeMargin && ux < 0) || (qx > geom.W-edgeMargin && ux > 0) ||
				(qy < edgeMargin && uy < 0) || (qy > geom.H-edgeMargin && uy > 0) {
				return true
			}
		}
		for _, w := range geom.Walls {
			if segCross(p.X, p.Y, qx, qy, w[0], w[1], w[2], w[3]) {
				return true
			}
		}
		return false
	}
	// best picks the unblocked probe direction closest to (px, py), or the
	// least dense one when (px, py) is zero.
	best := func(px, py float64) (float64, float64) {
		bx, by, bs := px, py, math.Inf(1)
		for k := 0; k < GuideProbes; k++ {
			th := 2 * math.Pi * float64(k) / GuideProbes
			ux, uy := math.Cos(th), math.Sin(th)
			if blocked(ux, uy) {
				continue
			}
			var score float64
			if px == 0 && py == 0 {
				score = dens(p.X+GuideProbeM*ux, p.Y+GuideProbeM*uy)
			} else {
				score = -(ux*px + uy*py)
			}
			if score < bs {
				bx, by, bs = ux, uy, score
			}
		}
		return bx, by
	}

	if p.Acc > 0 {
		dx, dy = roughDirection(pts, geom, p)
	} else if p.Reason == reasonPush && math.Hypot(p.PushX, p.PushY) > 1e-9 {
		l := math.Hypot(p.PushX, p.PushY)
		px, py := p.PushX/l, p.PushY/l
		// Sideways, on the less dense (and open) side, a little with the push.
		lx, ly := -py, px
		rx, ry := py, -px
		dl, dr := dens(p.X+GuideProbeM*lx, p.Y+GuideProbeM*ly), dens(p.X+GuideProbeM*rx, p.Y+GuideProbeM*ry)
		bl, br := blocked(lx, ly), blocked(rx, ry)
		sx, sy := lx, ly
		if (bl && !br) || (bl == br && dr < dl) {
			sx, sy = rx, ry
		}
		dx, dy = norm(sx+pushForward*px, sy+pushForward*py)
		if blocked(dx, dy) {
			dx, dy = norm(sx, sy)
		}
	} else {
		gx, gy, rho := KDEGrad(pts, p.X, p.Y)
		if math.Hypot(gx, gy)*GuideSigma < flatGrad*rho || (gx == 0 && gy == 0) {
			dx, dy = best(0, 0) // flat: the least dense open direction
		} else {
			dx, dy = norm(-gx, -gy)
		}
	}
	lx, ly := leaveDir(p)
	if lx != 0 || ly != 0 {
		dx, dy = norm(leaveWeight*lx+dx, leaveWeight*ly+dy)
	}
	if blocked(dx, dy) {
		dx, dy = best(dx, dy) // slide along the wall
	}

	// Blend in the nearest open exit that lies roughly this way.
	bestD := math.Inf(1)
	cosMax := math.Cos(GuideExitDeg * math.Pi / 180)
	for _, e := range geom.Exits {
		cx, cy := closestOn(e.X0, e.Y0, e.X1, e.Y1, p.X, p.Y)
		d := math.Hypot(cx-p.X, cy-p.Y)
		if d > GuideExitM || d >= bestD {
			continue
		}
		ux, uy := 0.0, 0.0
		if d > 1e-6 {
			ux, uy = (cx-p.X)/d, (cy-p.Y)/d
		} else {
			ux, uy = dx, dy
		}
		if ux*dx+uy*dy < cosMax {
			continue
		}
		bestD, ex, ey, exit = d, ux, uy, e.Name
		if exit == "" {
			exit = e.ID
		}
	}
	if exit != "" {
		dx, dy = norm(dx+ex, dy+ey)
	}
	if (lx != 0 || ly != 0) && dx*lx+dy*ly <= 0.1 {
		dx, dy, exit = lx, ly, "" // whatever else, get out of the area
	}
	return dx, dy, ex, ey, exit
}

// Conf is Move.Conf for a position with accuracy radius acc (m).
func Conf(acc float64) float64 {
	if !(acc > 0) {
		return 1
	}
	return 1 / (1 + (acc/GuideConfAcc)*(acc/GuideConfAcc))
}

// roughDirection is the density direction for a phone whose position is
// only known to ± p.Acc (see the comment at the top): the gradient of the
// density smoothed by the position errors where it is clear, the least
// dense way out at long range where it is not.
func roughDirection(pts []Point, geom Geom, p GuideIn) (dx, dy float64) {
	s := p.Acc / AccToSigma
	own := s * s
	gx, gy, rho := kdeGradVar(pts, p.X, p.Y, own)
	h := math.Sqrt(GuideSigma*GuideSigma + own)
	w := 0.0
	if rho > 0 {
		w = math.Min(1, math.Hypot(gx, gy)*h/rho/roughGradFull)
	}
	gdx, gdy := norm(-gx, -gy)
	// Long range: the least dense direction that stays inside the venue.
	l := GuideProbeM + 2*s
	px, py, best := 0.0, 0.0, math.Inf(1)
	for k := 0; k < GuideProbes; k++ {
		th := 2 * math.Pi * float64(k) / GuideProbes
		ux, uy := math.Cos(th), math.Sin(th)
		qx, qy := p.X+l*ux, p.Y+l*uy
		if qx < edgeMargin || qy < edgeMargin || qx > geom.W-edgeMargin || qy > geom.H-edgeMargin {
			// As far as the venue goes that way; a direction that leaves
			// at once is no way out.
			t := l
			if ux < 0 {
				t = math.Min(t, (p.X-edgeMargin)/-ux)
			} else if ux > 0 {
				t = math.Min(t, (geom.W-edgeMargin-p.X)/ux)
			}
			if uy < 0 {
				t = math.Min(t, (p.Y-edgeMargin)/-uy)
			} else if uy > 0 {
				t = math.Min(t, (geom.H-edgeMargin-p.Y)/uy)
			}
			if t < l/2 {
				continue
			}
			qx, qy = p.X+t*ux, p.Y+t*uy
		}
		_, _, d := kdeGradVar(pts, qx, qy, own)
		if d < best {
			px, py, best = ux, uy, d
		}
	}
	dx, dy = norm(w*gdx+(1-w)*px, w*gdy+(1-w)*py)
	if dx == 0 && dy == 0 {
		if dx, dy = gdx, gdy; dx == 0 && dy == 0 {
			dx, dy = px, py
		}
	}
	return dx, dy
}

// kdeGradVar is KDEGrad with every kernel widened by the position errors:
// each phone's own (its Acc) plus extra (the variance, m², of the point the
// density is asked at).
func kdeGradVar(pts []Point, x, y, extra float64) (gx, gy, rho float64) {
	base := GuideSigma*GuideSigma + extra
	for _, q := range pts {
		s2 := base
		if q.Acc > 0 {
			s := q.Acc / AccToSigma
			s2 += s * s
		}
		dx, dy := q.X-x, q.Y-y
		d2 := dx*dx + dy*dy
		if d2 > 16*s2 {
			continue
		}
		k := math.Exp(-d2/(2*s2)) / (2 * math.Pi * s2)
		rho += k
		gx += dx / s2 * k
		gy += dy / s2 * k
	}
	return gx, gy, rho
}

// leaveDir is the unit vector from the phone to the nearest point of the
// boundary of p.Leave, when the phone is inside it (else zero).
func leaveDir(p GuideIn) (float64, float64) {
	poly := p.Leave
	if len(poly) < 3 || !detect.InPolygon(poly, p.X, p.Y) {
		return 0, 0
	}
	bx, by, bd := 0.0, 0.0, math.Inf(1)
	for i, a := range poly {
		b := poly[(i+1)%len(poly)]
		cx, cy := closestOn(a[0], a[1], b[0], b[1], p.X, p.Y)
		if d := math.Hypot(cx-p.X, cy-p.Y); d < bd {
			bx, by, bd = cx, cy, d
		}
	}
	return norm(bx-p.X, by-p.Y)
}

// KDE is the Gaussian kernel density estimate (phones per m², σ =
// GuideSigma) at (x, y).
func KDE(pts []Point, x, y float64) float64 {
	_, _, rho := KDEGrad(pts, x, y)
	return rho
}

// KDEGrad is the density estimate at (x, y) and its gradient.
func KDEGrad(pts []Point, x, y float64) (gx, gy, rho float64) {
	s2 := GuideSigma * GuideSigma
	cut := 16 * s2 // beyond 4σ the kernel is negligible
	norm := 1 / (2 * math.Pi * s2)
	for _, q := range pts {
		dx, dy := q.X-x, q.Y-y
		d2 := dx*dx + dy*dy
		if d2 > cut {
			continue
		}
		k := norm * math.Exp(-d2/(2*s2))
		rho += k
		gx += dx / s2 * k
		gy += dy / s2 * k
	}
	return gx, gy, rho
}

func norm(x, y float64) (float64, float64) {
	l := math.Hypot(x, y)
	if l < 1e-12 {
		return 0, 0
	}
	return x / l, y / l
}

func closestOn(x0, y0, x1, y1, x, y float64) (float64, float64) {
	dx, dy := x1-x0, y1-y0
	l2 := dx*dx + dy*dy
	t := 0.0
	if l2 > 0 {
		t = math.Max(0, math.Min(1, ((x-x0)*dx+(y-y0)*dy)/l2))
	}
	return x0 + t*dx, y0 + t*dy
}

// segCross reports whether segments p1–p2 and p3–p4 properly intersect.
func segCross(x1, y1, x2, y2, x3, y3, x4, y4 float64) bool {
	d := func(ax, ay, bx, by, cx, cy float64) float64 { return (bx-ax)*(cy-ay) - (by-ay)*(cx-ax) }
	d1 := d(x3, y3, x4, y4, x1, y1)
	d2 := d(x3, y3, x4, y4, x2, y2)
	d3 := d(x1, y1, x2, y2, x3, y3)
	d4 := d(x1, y1, x2, y2, x4, y4)
	return ((d1 > 0 && d2 < 0) || (d1 < 0 && d2 > 0)) && ((d3 > 0 && d4 < 0) || (d3 < 0 && d4 > 0))
}
