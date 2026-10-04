package locate

import "math"

// kalman is one phone's filter: position p and GPS bias b, each (x, y).
//
//	state  s = [px, py, bx, by]
//	GPS    z = p + b + noise        (the bias is slow, the noise white)
//	fix    z = p + noise            (anchor, hand placement, beacon …)
//
// The bias is a first-order Gauss–Markov process (time constant BiasTauS,
// size from the fix's reported accuracy), the standard model of a
// receiver's multipath and atmospheric error. Keeping it in the state is
// what lets everything else work: a phone that is known to stand still
// (no steps) has all the change in its fixes put down to the bias, a phone
// that just left a known spot has its bias measured there, and dead
// reckoning carries the position while the bias wanders.
type kalman struct {
	s [4]float64
	p [4][4]float64
}

// initAt starts the filter at (x, y) ± sigma with an unknown bias.
func (k *kalman) initAt(x, y, sigma float64) {
	*k = kalman{}
	k.s[0], k.s[1] = x, y
	k.p[0][0], k.p[1][1] = sigma*sigma, sigma*sigma
}

// move shifts the position by (dx, dy) and adds the 2 × 2 covariance q
// ([xx, xy, yy]) to it.
func (k *kalman) move(dx, dy float64, q [3]float64) {
	k.s[0] += dx
	k.s[1] += dy
	k.p[0][0] += q[0]
	k.p[0][1] += q[1]
	k.p[1][0] += q[1]
	k.p[1][1] += q[2]
}

// biasStep lets the bias decay and wander for dt seconds: time constant tau,
// stationary σ sigma per axis.
func (k *kalman) biasStep(dt, tau, sigma float64) {
	if dt <= 0 {
		return
	}
	a := math.Exp(-dt / tau)
	k.s[2] *= a
	k.s[3] *= a
	for i := 0; i < 4; i++ {
		for j := 2; j < 4; j++ {
			k.p[i][j] *= a
			k.p[j][i] *= a
		}
	}
	// The 2 × 2 bias block was scaled twice on its diagonal entries' rows
	// and columns: p[2..3][2..3] got a² as it should; the cross terms got a.
	q := sigma * sigma * (1 - a*a)
	k.p[2][2] += q
	k.p[3][3] += q
}

// setBiasVar makes the bias at least this uncertain (σ per axis): the
// first fix, or a fix whose accuracy got worse.
func (k *kalman) setBiasVar(sigma float64) {
	v := sigma * sigma
	if k.p[2][2] < v {
		k.p[2][2] = v
	}
	if k.p[3][3] < v {
		k.p[3][3] = v
	}
}

// update applies a 2-D measurement z = H s + noise with covariance r
// ([xx, xy, yy]). withBias: H = [I I] (a GPS fix), else H = [I 0]. It
// returns the squared Mahalanobis distance of the innovation.
func (k *kalman) update(zx, zy float64, r [3]float64, withBias bool) float64 {
	// HP rows: h[m][j] = Σ_i H[m][i] P[i][j]
	var hp [2][4]float64
	for j := 0; j < 4; j++ {
		hp[0][j] = k.p[0][j]
		hp[1][j] = k.p[1][j]
		if withBias {
			hp[0][j] += k.p[2][j]
			hp[1][j] += k.p[3][j]
		}
	}
	col := func(m, n int) float64 { // (HPHᵀ)[m][n]
		v := hp[m][n]
		if withBias {
			v += hp[m][n+2]
		}
		return v
	}
	sxx, sxy, syy := col(0, 0)+r[0], col(0, 1)+r[1], col(1, 1)+r[2]
	det := sxx*syy - sxy*sxy
	if !(det > 1e-12) {
		return 0
	}
	ixx, ixy, iyy := syy/det, -sxy/det, sxx/det
	nx, ny := zx-k.s[0], zy-k.s[1]
	if withBias {
		nx -= k.s[2]
		ny -= k.s[3]
	}
	d2 := nx*nx*ixx + 2*nx*ny*ixy + ny*ny*iyy
	// K = P Hᵀ S⁻¹ ; P Hᵀ = hpᵀ
	var g [4][2]float64
	for i := 0; i < 4; i++ {
		g[i][0] = hp[0][i]*ixx + hp[1][i]*ixy
		g[i][1] = hp[0][i]*ixy + hp[1][i]*iyy
	}
	for i := 0; i < 4; i++ {
		k.s[i] += g[i][0]*nx + g[i][1]*ny
	}
	var np [4][4]float64
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			np[i][j] = k.p[i][j] - g[i][0]*hp[0][j] - g[i][1]*hp[1][j]
		}
	}
	// Keep it symmetric and positive on the diagonal.
	for i := 0; i < 4; i++ {
		for j := i + 1; j < 4; j++ {
			m := (np[i][j] + np[j][i]) / 2
			np[i][j], np[j][i] = m, m
		}
		if np[i][i] < 1e-6 {
			np[i][i] = 1e-6
		}
	}
	k.p = np
	return d2
}

// innovation is the squared Mahalanobis distance a GPS fix would have,
// without applying it.
func (k *kalman) gpsDist2(zx, zy float64, r float64) float64 {
	sxx := k.p[0][0] + k.p[2][2] + 2*k.p[0][2] + r
	syy := k.p[1][1] + k.p[3][3] + 2*k.p[1][3] + r
	sxy := k.p[0][1] + k.p[2][3] + k.p[0][3] + k.p[1][2]
	det := sxx*syy - sxy*sxy
	if !(det > 1e-12) {
		return 0
	}
	nx, ny := zx-k.s[0]-k.s[2], zy-k.s[1]-k.s[3]
	return (nx*nx*syy - 2*nx*ny*sxy + ny*ny*sxx) / det
}

// sigma is the position's 1-σ radius: the root of the mean axis variance.
func (k *kalman) sigma() float64 {
	return math.Sqrt(math.Max(0, (k.p[0][0]+k.p[1][1])/2))
}
