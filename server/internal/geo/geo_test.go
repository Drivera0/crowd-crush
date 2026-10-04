package geo

import (
	"math"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	for _, a := range []Anchor{
		{Lat: 49.2781, Lon: -122.9199, Bearing: 0},
		{Lat: 49.2781, Lon: -122.9199, Bearing: 37},
		{Lat: -33.86, Lon: 151.21, Bearing: 270},
		{Lat: 0, Lon: 179.9999, Bearing: 180}, // across the antimeridian
	} {
		for _, p := range [][2]float64{{0, 0}, {24, 16}, {3.2, 7.5}, {-5, 30}} {
			lat, lon := a.ToLatLon(p[0], p[1])
			if lon > 180 {
				lon -= 360
			}
			x, y := a.ToVenue(lat, lon)
			if math.Abs(x-p[0]) > 1e-6 || math.Abs(y-p[1]) > 1e-6 {
				t.Errorf("anchor %+v: %v → (%.6f, %.6f) → (%.6f, %.6f)", a, p, lat, lon, x, y)
			}
		}
	}
}

func TestBearing(t *testing.T) {
	const lat0, lon0 = 49.0, -123.0
	east := func(m float64) (float64, float64) {
		return lat0, lon0 + m/(math.Cos(lat0*math.Pi/180)*MetresPerDegLon)
	}
	south := func(m float64) (float64, float64) { return lat0 - m/MetresPerDegLat, lon0 }
	near := func(t *testing.T, name string, gx, gy, wx, wy float64) {
		t.Helper()
		if math.Abs(gx-wx) > 1e-6 || math.Abs(gy-wy) > 1e-6 {
			t.Errorf("%s: got (%.4f, %.4f), want (%.4f, %.4f)", name, gx, gy, wx, wy)
		}
	}
	// North up: east is +x, south is +y (down the map).
	a := Anchor{Lat: lat0, Lon: lon0}
	x, y := a.ToVenue(east(10))
	near(t, "north-up east", x, y, 10, 0)
	x, y = a.ToVenue(south(10))
	near(t, "north-up south", x, y, 0, 10)
	// East up (bearing 90): east is up the map (-y), south is +x.
	a.Bearing = 90
	x, y = a.ToVenue(east(10))
	near(t, "east-up east", x, y, 0, -10)
	x, y = a.ToVenue(south(10))
	near(t, "east-up south", x, y, 10, 0)
	// South up (180): east is -x.
	a.Bearing = 180
	x, y = a.ToVenue(east(10))
	near(t, "south-up east", x, y, -10, 0)
}

func TestSmoother(t *testing.T) {
	var s Smoother
	if x, y := s.Add(10, 10, 5); x != 10 || y != 10 {
		t.Fatal("first fix should be taken as is")
	}
	x, _ := s.Add(13, 10, 5)
	if want := 10 + 3*Weight(5); math.Abs(x-want) > 1e-9 {
		t.Fatalf("x %.3f, want %.3f", x, want)
	}
	if Weight(5) <= Weight(25) {
		t.Fatal("accurate fixes must weigh more")
	}
	if NormBearing(-90) != 270 || NormBearing(720) != 0 {
		t.Fatal("NormBearing")
	}
}
