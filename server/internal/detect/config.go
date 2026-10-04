package detect

import (
	"encoding/json"
	"fmt"
	"os"
)

// Config holds every detection threshold so they can be tuned from a JSON
// file without recompiling. Times are in milliseconds, accelerations in m/s².
type Config struct {
	// Grid layout and how it is split into zones.
	Rows     int `json:"rows"`
	Cols     int `json:"cols"`
	ZoneRows int `json:"zoneRows"` // rows per zone
	ZoneCols int `json:"zoneCols"` // cols per zone

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
	MinChain      int     `json:"minChain"`      // a wave edge must be part of a run of at least this many phones along a row/column, same direction; ≤ 2 = off
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
		Rows: 1, Cols: 8, ZoneRows: 1, ZoneCols: 4,

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
	case c.Rows < 1 || c.Cols < 1:
		return fmt.Errorf("grid must be at least 1x1")
	case c.ZoneRows < 1 || c.ZoneCols < 1:
		return fmt.Errorf("zones must be at least 1x1")
	case c.StepMs <= 0 || c.CorrWindowMs <= 2*c.MaxLagMs:
		return fmt.Errorf("corrWindowMs must exceed 2*maxLagMs and stepMs must be > 0")
	case c.Axis != "x" && c.Axis != "z" && c.Axis != "xz":
		return fmt.Errorf("axis must be x, z or xz")
	}
	return nil
}
