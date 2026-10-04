package locate

import "math"

// The cooperative step.
//
// After every phone's own filter has been advanced, the estimates are
// adjusted together, by the least movement that satisfies what is known
// about pairs of phones (a projection, in the manner of position-based
// dynamics, repeated coopIters times):
//
//   - linked phones (motion neighbours, near.go; the detector's wave edges;
//     mesh reports) are within LinkRange of each other: a pair farther
//     apart is pulled together until it is. Each moves in proportion to
//     its own variance, so a phone that knows where it is (an anchor, one
//     that just left the entry) hardly moves and pulls its vaguer
//     neighbours to it. Pairs already within range are left alone: the
//     graph says "near", not "on top of each other", and pulling further
//     would read as a crush that isn't there;
//   - no two estimates are closer than Spacing: bodies don't overlap.
//     Phones that all start on the entry spot, or were pulled together,
//     spread into a packing a crowd could have;
//   - nobody ends up in a wall, on the stage or outside (geom.go).
//
// A phone placed exactly (σ under exactSigma) is never moved.
//
// A linked phone's uncertainty shrinks as if each neighbour were one more
// measurement of its position, as good as that neighbour's own estimate
// plus LinkSigma: 1/σ'² = 1/σ² + Σ w / (σ_j² + LinkSigma²). The adjusted
// positions do not go back into the filters; the next step starts from
// where this one moved each linked phone to.

const (
	coopIters  = 6
	exactSigma = 0.31 // StandCap (0.3) on top of a 5 cm placement stays exact
	coopKeep   = 0.8  // share of the last step's adjustment an unlinked phone starts from
)

type clink struct {
	a, b *phone
	w    float64
	own  bool
}

func (e *Estimator) cooperate(now int64, ext []Link) {
	cfg := &e.cfg
	var links []clink
	if cfg.Coop {
		if cfg.OwnLinks {
			links = e.findNear(now)
			e.stats.OwnLinks = len(links)
		}
		have := map[[2]string]bool{}
		for _, l := range links {
			have[pkey(l.a.id, l.b.id)] = true
		}
		add := func(ida, idb string, w float64) {
			k := pkey(ida, idb)
			a, b := e.phones[ida], e.phones[idb]
			if have[k] || a == nil || b == nil || a == b || !a.placed || !b.placed || !(w > 0) {
				return
			}
			have[k] = true
			links = append(links, clink{a, b, math.Min(w, 1), false})
		}
		for _, l := range ext {
			add(l.A, l.B, l.W)
		}
		// A link between estimates too far apart to be the same place,
		// given both uncertainties, is not believed.
		keep := links[:0]
		for _, l := range links {
			d := math.Hypot(l.a.x-l.b.x, l.a.y-l.b.y)
			if d > cfg.LinkRange+3*(l.a.acc+l.b.acc)+2 {
				continue
			}
			keep = append(keep, l)
		}
		links = keep
	}
	e.stats.Links = len(links)
	e.lastLinks = links
	if len(links) == 0 && cfg.Spacing <= 0 {
		return
	}
	mob := func(p *phone) float64 {
		if p.acc < exactSigma {
			return 0
		}
		return p.acc * p.acc
	}
	// Uncertainty first, from the filters' own σ.
	if len(links) > 0 {
		prec := make([]float64, len(e.order))
		for _, l := range links {
			ls := cfg.LinkSigma * cfg.LinkSigma
			prec[l.a.idx] += l.w / (l.b.acc*l.b.acc + ls)
			prec[l.b.idx] += l.w / (l.a.acc*l.a.acc + ls)
			l.a.links++
			l.b.links++
		}
		for i, p := range e.order {
			if prec[i] > 0 {
				p.acc = math.Sqrt(1 / (1/(p.acc*p.acc) + prec[i]))
				p.near = true
			}
		}
	}
	// Start where the last step ended: a big group of linked phones takes
	// more than one step's iterations to settle.
	// A phone that is no longer held anywhere drifts back to its own
	// estimate (a fifth of the way per step).
	for _, p := range e.order {
		if mob(p) == 0 {
			continue
		}
		k := coopKeep
		if (p.near && p.wasNear) || math.Hypot(p.cdx, p.cdy) <= 2*cfg.Spacing {
			k = 1 // held by its links, or only nudged aside
		}
		p.x += k * p.cdx
		p.y += k * p.cdy
	}
	var cells map[[2]int][]*phone
	for it := 0; it < coopIters; it++ {
		for _, l := range links {
			a, b := l.a, l.b
			dx, dy := b.x-a.x, b.y-a.y
			d := math.Hypot(dx, dy)
			if d <= cfg.LinkRange {
				continue
			}
			ma, mb := mob(a), mob(b)
			if ma+mb == 0 {
				continue
			}
			pull := (d - cfg.LinkRange) * l.w / d
			fa, fb := ma/(ma+mb), mb/(ma+mb)
			a.x += dx * pull * fa
			a.y += dy * pull * fa
			b.x -= dx * pull * fb
			b.y -= dy * pull * fb
		}
		if s := cfg.Spacing; s > 0 {
			if cells == nil {
				cells = make(map[[2]int][]*phone, len(e.order))
			} else {
				for k := range cells {
					delete(cells, k)
				}
			}
			cof := func(p *phone) [2]int { return [2]int{int(math.Floor(p.x / s)), int(math.Floor(p.y / s))} }
			for _, p := range e.order {
				if !p.gone {
					c := cof(p)
					cells[c] = append(cells[c], p)
				}
			}
			for _, a := range e.order {
				if a.gone {
					continue
				}
				c := cof(a)
				for gx := c[0] - 1; gx <= c[0]+1; gx++ {
					for gy := c[1] - 1; gy <= c[1]+1; gy++ {
						for _, b := range cells[[2]int{gx, gy}] {
							if b.idx <= a.idx {
								continue
							}
							ma, mb := mob(a), mob(b)
							if ma+mb == 0 {
								continue
							}
							dx, dy := b.x-a.x, b.y-a.y
							d := math.Hypot(dx, dy)
							if d >= s {
								continue
							}
							if d < 1e-6 {
								// On the same point: part along a direction
								// fixed by who they are.
								ang := float64(mix64(uint64(a.idx)<<32|uint64(b.idx))%6283) / 1000
								dx, dy, d = math.Cos(ang)*1e-3, math.Sin(ang)*1e-3, 1e-3
							}
							push := (s - d) / d
							// Half each unless one of them is fixed: here
							// the uncertainty says nothing about who is
							// in whose place.
							fa, fb := 0.5, 0.5
							if ma == 0 {
								fa, fb = 0, 1
							} else if mb == 0 {
								fa, fb = 1, 0
							}
							a.x -= dx * push * fa
							a.y -= dy * push * fa
							b.x += dx * push * fb
							b.y += dy * push * fb
						}
					}
				}
			}
		}
		if cfg.MapConstraints {
			for _, p := range e.order {
				if mob(p) > 0 {
					p.x, p.y = e.geom.inside(p.x, p.y)
				}
			}
		}
	}
	for _, p := range e.order {
		p.cdx, p.cdy = p.x-p.kf.s[0], p.y-p.kf.s[1]
		if math.Hypot(p.cdx, p.cdy) > 0.05 && !p.near {
			p.moved = true
		}
		// What the neighbours taught a phone outlives the contact: once a
		// second the adjusted position goes into its filter as a weak fix
		// (CoopFeed × its variance), so its GPS bias is learnt while the
		// group holds together and the position doesn't spring back when
		// the links lapse.
		if cfg.CoopFeed > 0 && p.near && mob(p) > 0 && now-p.fedAt >= 1000 {
			p.fedAt = now
			r := cfg.CoopFeed * math.Max(p.acc*p.acc, cfg.LinkSigma*cfg.LinkSigma)
			x, y := p.x, p.y
			p.kf.update(x, y, [3]float64{r, 0, r}, false)
			p.cdx, p.cdy = x-p.kf.s[0], y-p.kf.s[1]
		}
	}
}
