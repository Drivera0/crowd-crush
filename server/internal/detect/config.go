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

	// Phones whose position is only roughly known (a GPS fix with an
	// accuracy radius; see motion.go). Neighbours are then found by motion:
	// two such phones are candidates when their reported positions are within
	// neighbourRadius + accPairScale × (accA + accB), at most maxPairRadius,
	// and each phone is compared with at most motionPairs candidates per step.
	AccPairScale  float64 `json:"accPairScale"`  // 0 = ignore accuracy (every phone is taken to stand exactly where it says)
	MaxPairRadius float64 `json:"maxPairRadius"` // m
	MotionPairs   int     `json:"motionPairs"`   // candidates compared per phone and step
	LagClosureMs  int64   `json:"lagClosureMs"`  // a chain a→b→c found by motion needs a and c to match at lag(a,b) + lag(b,c), give or take this
	// A GPS phone counts as outside the venue only when its fix is more than
	// outsideAccFactor × its accuracy beyond the edge (0 = any distance, as
	// before); nearer than that it is taken to stand at the edge.
	OutsideAccFactor float64 `json:"outsideAccFactor"`

	// Staff-drawn "high risk" areas: thresholds × this, hold time halved.
	HighRiskFactor float64 `json:"highRiskFactor"`

	// Crowd clusters (DBSCAN over phone positions) and density alerts.
	ClusterEps     float64 `json:"clusterEps"`     // m
	ClusterMinPts  int     `json:"clusterMinPts"`  // phones
	ClusterTrendMs int64   `json:"clusterTrendMs"` // window for forming / dispersing
	DensityWatch   float64 `json:"densityWatch"`   // people/m² → yellow
	DensityDanger  float64 `json:"densityDanger"`  // people/m² → red
	Participation  float64 `json:"participation"`  // fraction of attendees with the page open (people = phones / participation)
	EarlyWarnS     float64 `json:"earlyWarnS"`     // early warning: a cluster projected to reach densityDanger within this many seconds (at its current rate) is at least yellow; 0 = off
	EarlyFloor     float64 `json:"earlyFloor"`     // … once its estimated density is at least this fraction of densityDanger
	// Density around a phone whose position is only known to ± acc metres is
	// counted over a disc of radius densityAccDisc × acc when that is wider
	// than the usual 1.5 m: phones that all claim the same spot to ± 10 m are
	// not a crush. 0 = every position is taken as exact.
	DensityAccDisc float64 `json:"densityAccDisc"`

	// GPS fixes less accurate than this (m) are ignored.
	GPSMaxAcc float64 `json:"gpsMaxAcc"`

	// Per-phone.
	HandlingRot      float64 `json:"handlingRot"`      // deg/s above which the phone is being handled
	HandlingSettleMs int64   `json:"handlingSettleMs"` // quiet time before readings count again
	// A short burst of rotation (a gesture with the phone in the hand) only
	// masks its own readings: while a handling episode is younger than
	// handlingShortMs the quiet time is handlingShortSettleMs and the filters
	// are held, not restarted. 0 = every episode is a long one.
	HandlingShortMs       int64   `json:"handlingShortMs"`
	HandlingShortSettleMs int64   `json:"handlingShortSettleMs"`
	StaleMs               int64   `json:"staleMs"`       // no data for this long → stale
	LowPassHz             float64 `json:"lowPassHz"`     // sway is slow: drop everything above
	HighPassHz            float64 `json:"highPassHz"`    // and remove bias / drift below
	Axis                  string  `json:"axis"`          // "x", "z" or "xz" (dominant horizontal direction)
	SwayWindowMs          int64   `json:"swayWindowMs"`  // RMS window for the sway score
	SwayThreshold         float64 `json:"swayThreshold"` // RMS above this → swaying

	// Phones that send their gravity vector (levelled; see level.go).
	HandlingTiltDeg float64 `json:"handlingTiltDeg"` // gravity turning this far in the device frame within handlingTiltMs = being handled (out of a pocket, turned over); 0 = off
	HandlingTiltMs  int64   `json:"handlingTiltMs"`
	WalkRhythm      float64 `json:"walkRhythm"`  // walking gate: vertical and horizontal motion both this periodic (autocorrelation) → walking, not sway; 0 = off
	WalkMinVert     float64 `json:"walkMinVert"` // … with at least this much vertical RMS (m/s²)
	WalkHoldMs      int64   `json:"walkHoldMs"`  // … and the phone stays "walking" this long after the last time it was seen

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
	// MinOverlap: a pair is correlated at a lag when at least this share of
	// the readings a gap-free pair would have there are present on both
	// phones (gaps: handling, dropouts). 1 = any gap rules out the far lags,
	// as before. Gap-free pairs are unaffected by it.
	MinOverlap float64 `json:"minOverlap"`

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

	// Table demo profile (table.go): used only while the app turns it on
	// (the demo spot is on, live phones only), and only in zones holding at
	// most Table.MaxPhones phones. It never changes anything above.
	Table TableConfig `json:"table"`
}

// TableConfig is the table demo profile: a handful of judges in a row at
// the demo spot. See table.go for what each rule means and why.
type TableConfig struct {
	MaxPhones int   `json:"maxPhones"` // the profile applies to zones with at most this many active phones; 0 = never
	SmoothMs  int64 `json:"smoothMs"`  // zone score EMA time constant in such a zone (instead of zoneSmoothMs)
	HoldMs    int64 `json:"holdMs"`    // … and hold time (instead of holdMs)
	// PairScore: a wave edge that is not part of a chain (two phones, or a
	// push that reached only two people) still counts, but the zone score
	// it gives is capped here, below redScore: yellow at most. 0 = off.
	PairScore float64 `json:"pairScore"`
	// Moving together: neighbours whose horizontal motion matches (|r| ≥
	// TogetherCorr) at a small lag (≤ TogetherMaxLagMs) over the last
	// TogetherWindowMs, with neither trace rhythmic (autocorrelation at
	// lags up to TogetherRhythmLagMs below TogetherRhythm: dancing, jumping
	// and walking are), horizontal stronger than vertical, sustained for
	// TogetherHoldMs with the lag steady (within TogetherJitterMs of where
	// it started). Raises the zone score to TogetherScore (below redScore:
	// yellow at most) while it holds. TogetherScore 0 = off.
	TogetherCorr        float64 `json:"togetherCorr"`
	TogetherMaxLagMs    int64   `json:"togetherMaxLagMs"`
	TogetherWindowMs    int64   `json:"togetherWindowMs"`
	TogetherRhythm      float64 `json:"togetherRhythm"`
	TogetherRhythmLagMs int64   `json:"togetherRhythmLagMs"`
	TogetherHoldMs      int64   `json:"togetherHoldMs"`
	TogetherJitterMs    int64   `json:"togetherJitterMs"`
	TogetherScore       float64 `json:"togetherScore"`
}

// DefaultTableConfig is the table demo profile, tuned on the table-demo
// cases (sim/table.go; numbers in docs/TABLE-DEMO.md).
func DefaultTableConfig() TableConfig {
	return TableConfig{
		MaxPhones: 5, SmoothMs: 7000, HoldMs: 500,
		PairScore:    0.45,
		TogetherCorr: 0.7, TogetherMaxLagMs: 300, TogetherWindowMs: 15000,
		TogetherRhythm: 0.5, TogetherRhythmLagMs: 5000,
		TogetherHoldMs: 8000, TogetherJitterMs: 120, TogetherScore: 0.45,
	}
}

// DefaultConfig is tuned on the simulator scenarios; retune on real recordings.
func DefaultConfig() Config {
	return Config{
		VenueW: 24, VenueH: 16, ZoneCols: 2, ZoneRows: 1,
		LegacySpacing: 0.6, LegacyX0: 4.0,
		NeighbourRadius: 1.1, MaxNeighbours: 6, ChainAngleDeg: 90,
		AccPairScale: 1.0, MaxPairRadius: 15, MotionPairs: 10, LagClosureMs: 150,
		OutsideAccFactor: 1.0,
		HighRiskFactor:   0.5,
		ClusterEps:       1.2, ClusterMinPts: 3, ClusterTrendMs: 10000,
		DensityWatch: 2.0, DensityDanger: 4.0, Participation: 1.0,
		EarlyWarnS: 30, EarlyFloor: 0.55, DensityAccDisc: 0.5,
		GPSMaxAcc: 25,

		HandlingRot:      200,
		HandlingSettleMs: 1000,
		HandlingShortMs:  1500, HandlingShortSettleMs: 300,
		StaleMs:       2000,
		LowPassHz:     1.5,
		HighPassHz:    0.15,
		Axis:          "x",
		SwayWindowMs:  5000,
		SwayThreshold: 0.25,

		HandlingTiltDeg: 45,
		HandlingTiltMs:  500,
		WalkRhythm:      0.75,
		WalkMinVert:     0.3,
		WalkHoldMs:      3000,

		CorrWindowMs:  6000,
		MaxLagMs:      1500,
		StepMs:        50,
		CorrThreshold: 0.6,
		MinWaveLagMs:  120,
		MaxWaveLagMs:  1200,
		PeakMargin:    0.2,
		EdgeMinSway:   0.15,
		LatencyMs:     300,
		MinOverlap:    0.7,

		VerticalRatio: 1.0,
		MinChain:      3,
		ChainCorr:     0.4,

		ZoneSmoothMs: 8000,
		YellowScore:  0.3,
		RedScore:     0.6,
		Margin:       0.1,
		HoldMs:       2000,

		Table: DefaultTableConfig(),
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
	case !(c.AccPairScale >= 0) || (c.AccPairScale > 0 && (!(c.MaxPairRadius >= c.NeighbourRadius) || c.MotionPairs < 1)) || c.LagClosureMs < 0 || !(c.OutsideAccFactor >= 0):
		return fmt.Errorf("accPairScale ≥ 0 (with it on: maxPairRadius ≥ neighbourRadius, motionPairs ≥ 1), lagClosureMs ≥ 0, outsideAccFactor ≥ 0")
	case !(c.HighRiskFactor > 0 && c.HighRiskFactor <= 1):
		return fmt.Errorf("highRiskFactor must be in (0, 1]")
	case !(c.ClusterEps > 0) || c.ClusterMinPts < 2 || c.ClusterTrendMs <= 0:
		return fmt.Errorf("clusterEps > 0, clusterMinPts ≥ 2, clusterTrendMs > 0")
	case !(c.DensityWatch > 0 && c.DensityDanger > c.DensityWatch):
		return fmt.Errorf("need 0 < densityWatch < densityDanger")
	case !(c.Participation > 0 && c.Participation <= 1):
		return fmt.Errorf("participation must be in (0, 1]")
	case !(c.EarlyWarnS >= 0 && c.EarlyWarnS <= 600) || !(c.EarlyFloor >= 0 && c.EarlyFloor < 1):
		return fmt.Errorf("earlyWarnS must be 0..600 s and earlyFloor in [0, 1)")
	case !(c.DensityAccDisc >= 0 && c.DensityAccDisc <= 10):
		return fmt.Errorf("densityAccDisc must be 0..10")
	case !(c.GPSMaxAcc > 0):
		return fmt.Errorf("gpsMaxAcc must be > 0")
	case c.StepMs <= 0 || c.CorrWindowMs <= 2*c.MaxLagMs:
		return fmt.Errorf("corrWindowMs must exceed 2*maxLagMs and stepMs must be > 0")
	case !(c.MinOverlap > 0 && c.MinOverlap <= 1):
		return fmt.Errorf("minOverlap must be in (0, 1]")
	case c.Axis != "x" && c.Axis != "z" && c.Axis != "xz":
		return fmt.Errorf("axis must be x, z or xz")
	}
	if t := c.Table; t.MaxPhones > 0 {
		switch {
		case t.SmoothMs <= 0 || t.HoldMs < 0:
			return fmt.Errorf("table: smoothMs > 0 and holdMs ≥ 0")
		case !(t.PairScore >= 0 && t.PairScore < c.RedScore) || !(t.TogetherScore >= 0 && t.TogetherScore < c.RedScore):
			return fmt.Errorf("table: pairScore and togetherScore must be ≥ 0 and below redScore (they are yellow at most)")
		case t.TogetherScore > 0 && (!(t.TogetherCorr > 0 && t.TogetherCorr <= 1) || t.TogetherMaxLagMs < 0 ||
			t.TogetherWindowMs < c.CorrWindowMs || t.TogetherRhythmLagMs <= 0 || 2*t.TogetherRhythmLagMs >= t.TogetherWindowMs ||
			t.TogetherWindowMs > HistoryMs || t.TogetherHoldMs < 0 || t.TogetherJitterMs < 0):
			return fmt.Errorf("table: togetherCorr in (0, 1], togetherWindowMs between corrWindowMs and %d ms and more than twice togetherRhythmLagMs", HistoryMs)
		}
	}
	return nil
}

// LegacyPos maps an old grid cell to venue metres.
func (c Config) LegacyPos(row, col int) (x, y float64) {
	return c.LegacyX0 + float64(col)*c.LegacySpacing, c.VenueH/2 + float64(row)*c.LegacySpacing
}

// Outside reports whether a position fix (x, y) with accuracy radius acc
// (m; 0 = exact) is outside the venue: beyond the edge by more than
// OutsideAccFactor × acc. A fix nearer than that is taken to be someone
// inside whose GPS is off, and Clamp puts them at the edge.
func (c Config) Outside(x, y, acc float64) bool {
	m := c.OutsideAccFactor * math.Max(acc, 0)
	if c.OutsideAccFactor <= 0 {
		m = 0
	}
	return x < -m || y < -m || x > c.VenueW+m || y > c.VenueH+m
}

// Fold brings a roughly known position that fell off the venue back in by
// mirroring it at the edge it crossed (then clamping, should it still be
// off). Clamping alone would stack every such phone on the edge line, and
// the corners would read as crushes; mirroring is the usual boundary
// correction for a density estimate and keeps the crowd near a wall as
// dense as it was.
func (c Config) Fold(x, y float64) (float64, float64) {
	f := func(v, hi float64) float64 {
		if v < 0 {
			v = -v
		} else if v > hi {
			v = 2*hi - v
		}
		return math.Max(0, math.Min(v, hi))
	}
	return f(x, c.VenueW), f(y, c.VenueH)
}

// Place turns a position fix (x, y) with accuracy radius acc (m; 0 = exact)
// into where the phone is put and whether it counts as outside the venue:
// beyond the edge by more than OutsideAccFactor × acc it is outside and
// clamped onto the edge (as every fix off the venue used to be); nearer, it
// is someone inside whose GPS is off, and the fix is mirrored back in (Fold).
func (c Config) Place(x, y, acc float64) (px, py float64, outside bool) {
	if c.Outside(x, y, acc) {
		px, py = c.Clamp(x, y)
		return px, py, true
	}
	px, py = c.Fold(x, y)
	return px, py, false
}

// Clamp keeps a point inside the venue.
func (c Config) Clamp(x, y float64) (float64, float64) {
	return math.Max(0, math.Min(x, c.VenueW)), math.Max(0, math.Min(y, c.VenueH))
}
