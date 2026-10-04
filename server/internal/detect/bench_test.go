package detect

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
)

// benchPush is a damped shove repeating every 2.5 s (not periodic within the
// lag range, so no phone is gated as walking and every pair is correlated).
func benchPush(t float64) float64 {
	t = math.Mod(t+100, 2.5)
	return math.Exp(-t/0.9) * math.Sin(2*math.Pi*0.6*t)
}

// benchCrowd builds a detector with n phones on a 0.6 m grid, every one of
// them pushed hard enough to be correlated with all its neighbours while it
// bounces to a beat (so the walking gate has to measure both rhythms: the
// detector's worst case), and feeds 8 s of samples. orient gives each phone
// a random orientation and sends the gravity vector with every sample.
func benchCrowd(n int, orient bool) (*Detector, int64) {
	cfg := DefaultConfig()
	cols := int(math.Ceil(math.Sqrt(float64(n) * 1.6)))
	cfg.VenueW = math.Max(24, float64(cols)*0.6+1)
	cfg.VenueH = math.Max(16, float64(n/cols+1)*0.6+1)
	d := New(cfg)
	rng := rand.New(rand.NewSource(1))
	rots := make([]mat3, n)
	for i := 0; i < n; i++ {
		d.SetPhone(fmt.Sprint(i), 0.5+float64(i%cols)*0.6, 0.5+float64(i/cols)*0.6)
		rots[i] = randomRotation(rng)
	}
	const t0 = int64(1_700_000_000_000)
	for ms := int64(0); ms <= 8000; ms += 100 {
		t := float64(ms) / 1000
		for i := 0; i < n; i++ {
			k := float64(i % cols)
			a := [3]float64{
				1.5*benchPush(t-0.25*k) + rng.NormFloat64()*0.05,
				0.8*math.Sin(2*math.Pi*1.9*t+float64(i)) + rng.NormFloat64()*0.05,
				rng.NormFloat64() * 0.05,
			}
			s := Sample{T: t0 + ms, Rot: 20}
			if orient {
				a = rots[i].mul(a)
				s.G = rots[i].mul([3]float64{0, -1, 0})
			}
			s.AX, s.AY, s.AZ = a[0], a[1], a[2]
			d.Add(fmt.Sprint(i), s)
		}
	}
	return d, t0 + 8000
}

func benchStep(b *testing.B, n int, orient bool) {
	d, now := benchCrowd(n, orient)
	r := d.Step(now)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d.Step(now)
	}
	b.ReportMetric(float64(len(r.Edges)), "pairs")
}

func BenchmarkStep1000Upright(b *testing.B)  { benchStep(b, 1000, false) }
func BenchmarkStep1000Levelled(b *testing.B) { benchStep(b, 1000, true) }

// BenchmarkAdd is the per-sample cost (filters, and levelling when g is sent).
func benchAdd(b *testing.B, withG bool) {
	d := New(DefaultConfig())
	d.SetPhone("a", 1, 1)
	s := Sample{AX: 0.3, AY: 0.2, AZ: 0.1, Rot: 10}
	if withG {
		s.G = [3]float64{0.6, -0.64, 0.48}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.T = 1_000_000 + int64(i)*100
		d.Add("a", s)
	}
}

func BenchmarkAddUpright(b *testing.B)  { benchAdd(b, false) }
func BenchmarkAddLevelled(b *testing.B) { benchAdd(b, true) }
