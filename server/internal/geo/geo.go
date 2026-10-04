// Package geo converts GPS fixes to venue metres.
//
// The venue map's origin is its top-left corner (Lat, Lon), x to the right
// and y down the map. Bearing is the compass direction of the map's "up"
// (-y), in degrees clockwise from north: 0 means north is up, 90 means east
// is up. Over a venue-sized area a local equirectangular projection around
// the anchor is accurate to well under a GPS fix.
//
// Raw coordinates go in and metres come out; nothing here keeps them.
package geo

import "math"

// Metres per degree of latitude, and of longitude at the equator.
const (
	MetresPerDegLat = 110540.0
	MetresPerDegLon = 111320.0
)

// Anchor ties the venue map to the globe.
type Anchor struct {
	Lat, Lon float64 // the map's top-left corner
	Bearing  float64 // degrees clockwise from north of the map's up
}

// Valid reports whether the anchor is a usable point on Earth.
func (a Anchor) Valid() bool {
	return a.Lat >= -85 && a.Lat <= 85 && a.Lon >= -180 && a.Lon <= 180 &&
		!math.IsNaN(a.Bearing) && !math.IsInf(a.Bearing, 0)
}

// ToVenue converts a fix to venue metres (x right, y down the map). The
// result can lie outside the venue; the caller clamps it.
func (a Anchor) ToVenue(lat, lon float64) (x, y float64) {
	dlon := lon - a.Lon
	// Shortest way round across the antimeridian.
	if dlon > 180 {
		dlon -= 360
	} else if dlon < -180 {
		dlon += 360
	}
	east := dlon * math.Cos(a.Lat*math.Pi/180) * MetresPerDegLon
	north := (lat - a.Lat) * MetresPerDegLat
	s, c := math.Sincos(a.Bearing * math.Pi / 180)
	// Map right is the up direction turned 90° clockwise.
	x = east*c - north*s
	up := east*s + north*c
	return x, -up
}

// ToLatLon is the inverse of ToVenue (for tests and tools).
func (a Anchor) ToLatLon(x, y float64) (lat, lon float64) {
	s, c := math.Sincos(a.Bearing * math.Pi / 180)
	up := -y
	east := x*c + up*s
	north := -x*s + up*c
	return a.Lat + north/MetresPerDegLat, a.Lon + east/(math.Cos(a.Lat*math.Pi/180)*MetresPerDegLon)
}

// NormBearing folds a bearing into [0, 360).
func NormBearing(b float64) float64 {
	b = math.Mod(b, 360)
	if b < 0 {
		b += 360
	}
	return b
}

// Smoother lightly smooths a stream of fixes (already in venue metres) so
// dots don't jump: an exponential moving average whose weight falls with
// the fix's accuracy radius, w = 1 / (1 + acc/10 m). A 5 m fix moves the
// dot two thirds of the way, a 25 m fix less than a third. The first fix
// is taken as is.
type Smoother struct {
	X, Y float64
	Acc  float64 // smoothed accuracy (m)
	ok   bool
}

// Weight is the EMA weight given to a fix with accuracy acc (m).
func Weight(acc float64) float64 { return 1 / (1 + math.Max(acc, 0)/10) }

// Add feeds one fix and returns the smoothed position.
func (s *Smoother) Add(x, y, acc float64) (float64, float64) {
	if !s.ok {
		s.X, s.Y, s.Acc, s.ok = x, y, acc, true
		return x, y
	}
	w := Weight(acc)
	s.X += w * (x - s.X)
	s.Y += w * (y - s.Y)
	s.Acc += w * (acc - s.Acc)
	return s.X, s.Y
}

// Reset forgets the history (e.g. after the phone was placed by hand).
func (s *Smoother) Reset() { *s = Smoother{} }
