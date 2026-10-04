package protocol

// Wire types of the position estimator (internal/locate; app/locate.go).
// web/shared/locate.ts mirrors this file.
//
// Phones need to send nothing new for the estimator to work. Two optional
// additions make its dead reckoning possible:
//
//   - Motion.HD / Motion.HB (protocol.go): the compass heading, in degrees
//     clockwise from north, of the phone's top edge (hd) or, when the top
//     edge points more up or down than along the ground, of its back, the
//     way the rear camera looks (hb). A phone sends whichever of the two
//     axes is closer to horizontal; the server works out the rest from the
//     gravity vector g. Without a heading the server knows that a phone
//     walks but not where to.
//   - "dr" (below): the phone's own step count and displacement.

// TypeDR is the phone → server dead-reckoning report.
const TypeDR = "dr"

// DR is a phone's own dead reckoning, about once a second while it has
// anything to report: the steps it has counted since the page loaded and
// how far east (E) and north (N) it has walked in metres, all running
// totals (so a lost message loses nothing; totals that go backwards mean
// the page was reloaded). A phone that counts steps but can't tell the
// direction leaves E and N where they are.
// {"type":"dr","steps":412,"e":-3.2,"n":18.6}
type DR struct {
	Type  string  `json:"type"`
	Steps int     `json:"steps"`
	E     float64 `json:"e"`
	N     float64 `json:"n"`
}

// Sources of an estimated position (Node.Loc).
const (
	LocEntry  = "entry"  // started at the entry spot
	LocGPS    = "gps"    // GPS fixes
	LocSteps  = "steps"  // dead reckoning
	LocFix    = "fix"    // placed by hand, at a tower or at the demo spot
	LocBeacon = "beacon" // Bluetooth beacon fix
	LocMesh   = "mesh"   // the phone's own mesh estimate
	LocNear   = "near"   // pulled toward motion neighbours
	LocMap    = "map"    // moved by a wall, the stage, the venue edge or the spacing rule
)

// EntrySpot is where the one shared QR code hangs: a phone that joins
// without a position starts there, give or take Sigma metres.
type EntrySpot struct {
	On    bool    `json:"on"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Sigma float64 `json:"sigma"` // m, 1 σ per axis; 0 = 1.5
}

// LocateConfig is GET/PUT /api/locate: the estimator's switch, the entry
// spot, and the compass direction of the map's up for venues without a GPS
// anchor (degrees clockwise from north; nil = unknown, so compass headings
// can't be used; a GPS-anchored venue uses its own bearing).
type LocateConfig struct {
	On      bool      `json:"on"`
	Entry   EntrySpot `json:"entry"`
	Bearing *float64  `json:"bearing,omitempty"`
}

// LocateStatus is GET /api/locate: the settings and what the estimator of
// the pipeline on screen is doing.
type LocateStatus struct {
	LocateConfig
	Phones  int `json:"phones"`  // phones it holds a position for
	Located int `json:"located"` // … not lost
	Walking int `json:"walking"` // … dead-reckoned in the latest step
	Links   int `json:"links"`   // motion-neighbour links in the latest step
	// StepMs: mean duration of the estimator's step over the last 5 s.
	StepMs float64 `json:"stepMs"`
	// Error against the simulation's ground truth (mode "sim" only).
	Error *LocError `json:"error,omitempty"`
}

// LocError is the position error of the simulated phones against where
// their owners really stand (SimFrame.Loc, once a second): the mean,
// median and 95th percentile in metres over the N phones that have a
// position, for the estimate and for the raw source position (GPS smoothed
// and clamped, or the hand placement) of the RawN phones that have one.
type LocError struct {
	N       int     `json:"n"`
	Mean    float64 `json:"mean"`
	Median  float64 `json:"median"`
	P95     float64 `json:"p95"`
	RawN    int     `json:"rawN"`
	RawMean float64 `json:"rawMean"`
	// Lost: phones whose estimate is too vague to count (Node.Lost).
	Lost int `json:"lost"`
}
