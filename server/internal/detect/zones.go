package detect

import (
	"math"

	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// Sensitivities of a zone.
const (
	SensNormal = "normal"
	SensHigh   = "high"
)

// RestZone is the ID of the zone holding phones in no custom area.
const RestZone = "rest"

// ZoneDef describes one zone: a polygon in venue metres.
type ZoneDef struct {
	ID     string
	Name   string
	Poly   [][2]float64
	Custom bool   // drawn by staff
	Sens   string // normal | high
	// Rest zones contain every phone that is in no other zone (Poly is only
	// for drawing).
	Rest bool
}

// DefaultZones splits the venue into ZoneCols × ZoneRows rectangles, named
// A, B, … row by row.
func DefaultZones(cfg Config) []ZoneDef {
	var out []ZoneDef
	w := cfg.VenueW / float64(cfg.ZoneCols)
	h := cfg.VenueH / float64(cfg.ZoneRows)
	for zr := 0; zr < cfg.ZoneRows; zr++ {
		for zc := 0; zc < cfg.ZoneCols; zc++ {
			id := ZoneName(zr*cfg.ZoneCols + zc)
			x0, y0 := float64(zc)*w, float64(zr)*h
			out = append(out, ZoneDef{ID: id, Name: "Zone " + id, Sens: SensNormal,
				Poly: Rect(x0, y0, x0+w, y0+h)})
		}
	}
	return out
}

// Rect is an axis-aligned rectangle as a polygon (clockwise on the map).
func Rect(x0, y0, x1, y1 float64) [][2]float64 {
	return [][2]float64{{x0, y0}, {x1, y0}, {x1, y1}, {x0, y1}}
}

// ZoneName turns a zone index into a letter: A, B, … Z, AA, AB …
func ZoneName(i int) string {
	s := ""
	for {
		s = string(rune('A'+i%26)) + s
		i = i/26 - 1
		if i < 0 {
			return s
		}
	}
}

// InPolygon reports whether (x, y) is inside poly (even-odd rule). Points
// on the left/top edges count as inside, so rectangles that share an edge
// split the points between them, and the venue's right and bottom borders
// are handled by the caller.
func InPolygon(poly [][2]float64, x, y float64) bool {
	in := false
	n := len(poly)
	for i, j := 0, n-1; i < n; j, i = i, i+1 {
		xi, yi := poly[i][0], poly[i][1]
		xj, yj := poly[j][0], poly[j][1]
		if (yi > y) != (yj > y) {
			if x < (xj-xi)*(y-yi)/(yj-yi)+xi {
				in = !in
			}
		}
	}
	return in
}

// contains is InPolygon with the venue's far edges included, so a phone
// clamped onto the right or bottom border still lands in a default zone.
func (z *zone) contains(x, y, w, h float64) bool {
	if InPolygon(z.def.Poly, x, y) {
		return true
	}
	const eps = 1e-6
	if x >= w-eps {
		x = w - 1e-3
	}
	if y >= h-eps {
		y = h - 1e-3
	}
	return InPolygon(z.def.Poly, x, y)
}

// Thresholds for one level machine. A value above Yellow (Red) for Hold
// ms raises the level; it clears below Yellow−YMargin (Red−RMargin).
type Thresholds struct {
	Yellow, Red      float64
	YMargin, RMargin float64
	Hold             int64
}

// LevelState is the calm → yellow → red state machine with hold time and
// hysteresis used by zones (wave score) and clusters (density).
type LevelState struct {
	Level       string
	yellowSince int64 // when the value went above yellow (0 = not above)
	redSince    int64
	Since       int64 // when Level was entered
}

// NewLevelState starts calm.
func NewLevelState() LevelState { return LevelState{Level: protocol.LevelCalm} }

// Update feeds one value at time now and reports a level change.
func (s *LevelState) Update(now int64, v float64, th Thresholds) (from, to string, changed bool) {
	if s.Level == "" {
		s.Level = protocol.LevelCalm
	}
	since := func(p *int64, above bool) int64 {
		if !above {
			*p = 0
			return 0
		}
		if *p == 0 {
			*p = now
		}
		return now - *p
	}
	yHeld := since(&s.yellowSince, v > th.Yellow)
	rHeld := since(&s.redSince, v > th.Red)
	from, to = s.Level, s.Level
	switch s.Level {
	case protocol.LevelCalm:
		if s.yellowSince != 0 && yHeld >= th.Hold {
			to = protocol.LevelYellow
		}
	case protocol.LevelYellow:
		if s.redSince != 0 && rHeld >= th.Hold {
			to = protocol.LevelRed
		} else if v < th.Yellow-th.YMargin {
			to = protocol.LevelCalm
		}
	case protocol.LevelRed:
		if v < th.Red-th.RMargin {
			to = protocol.LevelYellow
		}
	}
	if to == from {
		return from, to, false
	}
	s.Level, s.Since = to, now
	return from, to, true
}

// zoneThresholds applies the zone's sensitivity to the wave-score
// thresholds.
func zoneThresholds(cfg *Config, sens string) Thresholds {
	th := Thresholds{Yellow: cfg.YellowScore, Red: cfg.RedScore, YMargin: cfg.Margin, RMargin: cfg.Margin, Hold: cfg.HoldMs}
	if sens == SensHigh {
		f := cfg.HighRiskFactor
		th.Yellow, th.Red, th.YMargin, th.RMargin = th.Yellow*f, th.Red*f, th.YMargin*f, th.RMargin*f
		th.Hold /= 2
	}
	return th
}

// dirName names the dominant axis of a net travel vector: +x is left to
// right on the map, +y top to bottom.
func dirName(vx, vy float64) string {
	switch {
	case vx == 0 && vy == 0:
		return ""
	case math.Abs(vx) >= math.Abs(vy):
		if vx > 0 {
			return "+x"
		}
		return "-x"
	case vy > 0:
		return "+y"
	default:
		return "-y"
	}
}
