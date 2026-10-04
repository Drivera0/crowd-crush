package detect

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
)

// Config holds every detection threshold so they can be tuned from a JSON
// file without recompiling. Times are in milliseconds, accelerations in m/s².
type Config struct {
	// Venue (metres; origin top-left of the map, x right, y down) and its
	// default split into ZoneCols × ZoneRows rectangles.
	VenueW   float64 `json:"venueW"`
	VenueH   float64 `json:"venueH"`
	ZoneCols int     `json:"zoneCols"`
	ZoneRows int     `json:"zoneRows"`

	// Legacy row/col phones (old recordings, old phone pages) are placed at
	// x = LegacyX0 + col·LegacySpacing, y = VenueH/2 + row·LegacySpacing.
	LegacySpacing float64 `json:"legacySpacing"`
	LegacyX0      float64 `json:"legacyX0"`

	// Spatial neighbour graph.
	NeighbourRadius float64 `json:"neighbourRadius"` // m: phones closer than this are compared
	MaxNeighbours   int     `json:"maxNeighbours"`   // each phone keeps its nearest this many
	ChainAngleDeg   float64 `json:"chainAngleDeg"`   // a chain may bend this much per hop

	// Staff-drawn "high risk" areas: thresholds × this, hold time halved.
	HighRiskFactor float64 `json:"highRiskFactor"`

	// Crowd clusters (DBSCAN over phone positions) and density alerts.
	ClusterEps     float64 `json:"clusterEps"`     // m
	ClusterMinPts  int     `json:"clusterMinPts"`  // phones
	ClusterTrendMs int64   `json:"clusterTrendMs"` // window for forming / dispersing
	DensityWatch   float64 `json:"densityWatch"`   // people/m² → yellow
	DensityDanger  float64 `json:"densityDanger"`  // people/m² → red
	Participation  float64 `json:"participation"`  // fraction of attendees with the page open (people = phones / participation)

	// GPS fixes less accurate than this (m) are ignored.
	GPSMaxAcc float64 `json:"gpsMaxAcc"`

	// Per-phone.
	HandlingRot      float64 `json:"handlingRot"`      // deg/s above which the phone is being handled
	HandlingSettleMs int64   `json:"handlingSettleMs"` // quiet time before readings count again
	StaleMs          int64   `json:"staleMs"`          // no data for this long → stale
	LowPassHz        float64 `json:"lowPassHz"`        // sway is slow: drop everything above
	HighPassHz       float64 `json:"highPassHz"`       // and remove bias / drift below
	Axis             string  `json:"axis"`             // "x", "z" or "xz" (dominant horizontal direction)
	SwayWindowMs     int64   `json:"swayWindowMs"`     // RMS window for the sway score
	SwayThreshold    float64 `json:"swayThreshold"`    // RMS above this → swaying

	// Per neighbour pair.
	CorrWindowMs  int64   `json:"corrWindowMs"`  // how much history to cross-correlate
	MaxLagMs      int64   `json:"maxLagMs"`      // search lags -MaxLag..+MaxLag
	StepMs        int64   `json:"stepMs"`        // resampling grid for correlation
	CorrThreshold float64 `json:"corrThreshold"` // min correlation for a wave edge
	MinWaveLagMs  int64   `json:"minWaveLagMs"`  // |lag| below this = moving together (dancing)
	MaxWaveLagMs  int64   `json:"maxWaveLagMs"`  // |lag| above this = unrelated
	PeakMargin    float64 `json:"peakMargin"`    // best peak must beat any other peak by this (periodic motion is ambiguous)
	EdgeMinSway   float64 `json:"edgeMinSway"`   // both phones need at least this RMS
	LatencyMs     int64   `json:"latencyMs"`     // analyse up to now-LatencyMs so late packets have arrived

	// False-positive guards.
	VerticalRatio float64 `json:"verticalRatio"` // veto a wave edge when non-rhythmic vertical motion this many times stronger than the horizontal travels down the line with it (Mexican wave); 0 = off
	MinChain      int     `json:"minChain"`      // a wave edge must be part of a spatial run of at least this many phones travelling the same way (each hop within chainAngleDeg of the last); ≤ 2 = off
	ChainCorr     float64 `json:"chainCorr"`     // the other hops of a chain need only this |corr| at a wave-like lag in the same direction; 0 = they must be full wave edges

	// Per zone.
	ZoneSmoothMs int64   `json:"zoneSmoothMs"` // EMA time constant for the zone score
	YellowScore  float64 `json:"yellowScore"`
	RedScore     float64 `json:"redScore"`
	Margin       float64 `json:"margin"` // hysteresis: clear below threshold-margin
	HoldMs       int64   `json:"holdMs"` // score must stay above a threshold this long
}

// DefaultConfig is tuned on the simulator scenarios; retune on real recordings.
func DefaultConfig() Config {
	return Config{
		VenueW: 24, VenueH: 16, ZoneCols: 2, ZoneRows: 1,
		LegacySpacing: 0.6, LegacyX0: 4.0,
		NeighbourRadius: 1.1, MaxNeighbours: 6, ChainAngleDeg: 90,
		HighRiskFactor: 0.5,
		ClusterEps:     1.2, ClusterMinPts: 3, ClusterTrendMs: 10000,
		DensityWatch: 2.0, DensityDanger: 4.0, Participation: 1.0,
		GPSMaxAcc: 25,

		HandlingRot:      200,
		HandlingSettleMs: 1000,
		StaleMs:          2000,
		LowPassHz:        1.5,
		HighPassHz:       0.15,
		Axis:             "x",
		SwayWindowMs:     5000,
		SwayThreshold:    0.25,

		CorrWindowMs:  6000,
		MaxLagMs:      1500,
		StepMs:        50,
		CorrThreshold: 0.6,
		MinWaveLagMs:  120,
		MaxWaveLagMs:  1200,
		PeakMargin:    0.2,
		EdgeMinSway:   0.15,
		LatencyMs:     300,

		VerticalRatio: 1.0,
		MinChain:      3,
		ChainCorr:     0.4,

		ZoneSmoothMs: 8000,
		YellowScore:  0.3,
		RedScore:     0.6,
		Margin:       0.1,
		HoldMs:       2000,
	}
}

// LoadConfig reads a JSON file over the defaults (missing fields keep their
// default value).
func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, cfg.Validate()
}

// Validate catches settings that would break the detector.
func (c Config) Validate() error {
	switch {
	case !(c.VenueW >= 1 && c.VenueH >= 1 && c.VenueW <= 5000 && c.VenueH <= 5000):
		return fmt.Errorf("venue must be between 1 m and 5 km on each side")
	case c.ZoneRows < 1 || c.ZoneCols < 1 || c.ZoneRows*c.ZoneCols > 100:
		return fmt.Errorf("zoneCols × zoneRows must be 1..100")
	case !(c.LegacySpacing > 0):
		return fmt.Errorf("legacySpacing must be > 0")
	case !(c.NeighbourRadius > 0) || c.MaxNeighbours < 1:
		return fmt.Errorf("neighbourRadius must be > 0 and maxNeighbours ≥ 1")
	case !(c.ChainAngleDeg > 0 && c.ChainAngleDeg <= 180):
		return fmt.Errorf("chainAngleDeg must be in (0, 180]")
	case !(c.HighRiskFactor > 0 && c.HighRiskFactor <= 1):
		return fmt.Errorf("highRiskFactor must be in (0, 1]")
	case !(c.ClusterEps > 0) || c.ClusterMinPts < 2 || c.ClusterTrendMs <= 0:
		return fmt.Errorf("clusterEps > 0, clusterMinPts ≥ 2, clusterTrendMs > 0")
	case !(c.DensityWatch > 0 && c.DensityDanger > c.DensityWatch):
		return fmt.Errorf("need 0 < densityWatch < densityDanger")
	case !(c.Participation > 0 && c.Participation <= 1):
		return fmt.Errorf("participation must be in (0, 1]")
	case !(c.GPSMaxAcc > 0):
		return fmt.Errorf("gpsMaxAcc must be > 0")
	case c.StepMs <= 0 || c.CorrWindowMs <= 2*c.MaxLagMs:
		return fmt.Errorf("corrWindowMs must exceed 2*maxLagMs and stepMs must be > 0")
	case c.Axis != "x" && c.Axis != "z" && c.Axis != "xz":
		return fmt.Errorf("axis must be x, z or xz")
	}
	return nil
}

// LegacyPos maps an old grid cell to venue metres.
func (c Config) LegacyPos(row, col int) (x, y float64) {
	return c.LegacyX0 + float64(col)*c.LegacySpacing, c.VenueH/2 + float64(row)*c.LegacySpacing
}

// Clamp keeps a point inside the venue.
func (c Config) Clamp(x, y float64) (float64, float64) {
	return math.Max(0, math.Min(x, c.VenueW)), math.Max(0, math.Min(y, c.VenueH))
}
