package protocol

// Wire types for joining at the table: the QR link test, phones reporting
// why they couldn't join, and a phone's place in the demo spot's row.
// web/shared/join.ts mirrors this file.

// JoinTest is POST /api/join/test: the server fetched the join URL itself.
type JoinTest struct {
	URL string `json:"url"`
	// OK: this server answered through the URL (and the phone page loads).
	OK     bool  `json:"ok"`
	Status int   `json:"status,omitempty"` // HTTP status of the probe, 0 = no answer
	Ms     int64 `json:"ms"`
	Secure bool  `json:"secure"` // https
	// Pulse: what answered: this | other (another Pulse server) | "" (not Pulse, or nothing).
	Pulse string `json:"pulse,omitempty"`
	// Message: one line for staff saying what was found and what to do.
	Message string `json:"message"`
	// Reachable: public | lan | local, as GET /api/join classifies the URL.
	Reachable string `json:"reachable"`
}

// JoinReport is POST /api/join/report: a phone page saying how its join
// went. Only the random session id, a reason code and the browser family.
type JoinReport struct {
	ID      string `json:"id"`
	Reason  string `json:"reason"`  // one of the Join* reasons
	Browser string `json:"browser"` // "iOS Safari 17", "Instagram (iOS)", …
}

// Join report reasons. JoinOK clears an earlier problem.
const (
	JoinOK           = "ok"
	JoinInApp        = "inapp"         // an in-app browser that gave no motion
	JoinInsecure     = "insecure"      // page opened over plain http
	JoinMotionDenied = "motion-denied" // the permission prompt was refused (or blocked)
	JoinNoMotion     = "no-motion"     // no motion events arrived
	JoinNoSensor     = "no-sensor"     // no motion API at all, or a laptop
	JoinPermError    = "perm-error"    // asking for permission threw
	JoinSocket       = "socket"        // the page loaded but the WebSocket can't connect
)

// JoinProblem is one reason in GET /api/join/stats.
type JoinProblem struct {
	Reason   string   `json:"reason"`
	Label    string   `json:"label"`    // short words for the dashboard
	Count    int      `json:"count"`    // phones with this problem now
	Browsers []string `json:"browsers"` // distinct browser families, at most 5
}

// JoinStats is GET /api/join/stats: "3 joined · 1 couldn't get motion: in-app browser".
type JoinStats struct {
	Joined    int           `json:"joined"`    // live phones connected now
	Streaming int           `json:"streaming"` // … of which sent motion in the last 5 s
	Problems  []JoinProblem `json:"problems"`  // reported in the last 30 min, by phones not streaming now
}

// DemoRow is a phone's place in the demo spot's row (PhoneState.Row).
type DemoRow struct {
	N int `json:"n"` // 1-based place in join order
	// Prev: the name of the phone at place N-1 ("" for the first or when
	// nobody stands there now). The person stands to its right.
	Prev string `json:"prev,omitempty"`
	// NewRow: the row wrapped at the venue's edge and this place starts a
	// new row (behind the first one on the map).
	NewRow bool `json:"newRow,omitempty"`
}
