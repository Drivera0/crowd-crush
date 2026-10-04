package detect

import (
	"math"
	"math/rand"
)

// mat3 is a rotation from the body frame (x right, y up, z forward: what an
// upright phone flat against the chest reports) to a phone's device frame.
type mat3 [3][3]float64

func (m mat3) mul(v [3]float64) [3]float64 {
	return [3]float64{
		m[0][0]*v[0] + m[0][1]*v[1] + m[0][2]*v[2],
		m[1][0]*v[0] + m[1][1]*v[1] + m[1][2]*v[2],
		m[2][0]*v[0] + m[2][1]*v[1] + m[2][2]*v[2],
	}
}

func (m mat3) times(o mat3) mat3 {
	var r mat3
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			for k := 0; k < 3; k++ {
				r[i][j] += m[i][k] * o[k][j]
			}
		}
	}
	return r
}

var identity = mat3{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}

// axisAngle is the rotation by ang (rad) about the unit axis u (Rodrigues).
func axisAngle(u [3]float64, ang float64) mat3 {
	c, s := math.Cos(ang), math.Sin(ang)
	x, y, z := u[0], u[1], u[2]
	return mat3{
		{c + x*x*(1-c), x*y*(1-c) - z*s, x*z*(1-c) + y*s},
		{y*x*(1-c) + z*s, c + y*y*(1-c), y*z*(1-c) - x*s},
		{z*x*(1-c) - y*s, z*y*(1-c) + x*s, c + z*z*(1-c)},
	}
}

func randomUnit(r *rand.Rand) [3]float64 {
	for {
		v := [3]float64{r.NormFloat64(), r.NormFloat64(), r.NormFloat64()}
		if n := math.Sqrt(v[0]*v[0] + v[1]*v[1] + v[2]*v[2]); n > 1e-6 {
			return [3]float64{v[0] / n, v[1] / n, v[2] / n}
		}
	}
}

// randomRotation is a uniformly random orientation: any tilt, any yaw (a
// phone in a pocket, a bag or a hand).
func randomRotation(r *rand.Rand) mat3 {
	// A random yaw about vertical, then tip "up" to a random direction.
	yaw := axisAngle([3]float64{0, 1, 0}, r.Float64()*2*math.Pi)
	up := randomUnit(r)
	// Rotation taking (0,1,0) to up.
	ax := [3]float64{up[2], 0, -up[0]} // (0,1,0) × up
	n := math.Hypot(ax[0], ax[2])
	if n < 1e-9 {
		if up[1] > 0 {
			return yaw
		}
		return axisAngle([3]float64{1, 0, 0}, math.Pi).times(yaw)
	}
	ax[0], ax[2] = ax[0]/n, ax[2]/n
	return axisAngle(ax, math.Acos(math.Max(-1, math.Min(1, up[1])))).times(yaw)
}

// tiltBy rotates the unit vector g by deg degrees about a random axis
// perpendicular to it: a gravity estimate that is off by that much.
func tiltBy(r *rand.Rand, g [3]float64, deg float64) [3]float64 {
	u := randomUnit(r)
	// make u perpendicular to g
	d := u[0]*g[0] + u[1]*g[1] + u[2]*g[2]
	u = [3]float64{u[0] - d*g[0], u[1] - d*g[1], u[2] - d*g[2]}
	n := math.Sqrt(u[0]*u[0] + u[1]*u[1] + u[2]*u[2])
	if n < 1e-6 {
		return g
	}
	u = [3]float64{u[0] / n, u[1] / n, u[2] / n}
	return axisAngle(u, deg*math.Pi/180).mul(g)
}
