package protocol

// Alert drills (POST /api/test-alert, GET /api/drill): a test alert staff
// send on purpose to exercise the alert chain. A drill is an incident with
// test:true; it never touches detector state, never escalates, stays out of
// the history given to Gemini, and attendee phones are not told about it
// (their screens follow the detector, which a drill does not change).

// DrillRequest is the optional body of POST /api/test-alert. An empty body
// (or {}) is the old one-button test alert: the zone that looks worst (else
// zone B, else the first zone), red, a crowd push, every output the area's
// rules allow.
type DrillRequest struct {
	Zone  string `json:"zone,omitempty"`  // an area or zone id; "" = pick as above
	Level string `json:"level,omitempty"` // yellow | red (default red)
	Kind  string `json:"kind,omitempty"`  // wave (crowd push, default) | density (crowding) | rule (over capacity)
	// Outputs to exercise; absent = what the area's rules allow.
	Outputs *DrillOutputs `json:"outputs,omitempty"`
}

// DrillOutputs chooses the outputs of a drill. A nil field keeps the
// default: briefing on, voice and sign as the area's rules say, the area's
// own zone light.
type DrillOutputs struct {
	Briefing *bool `json:"briefing,omitempty"`
	Voice    *bool `json:"voice,omitempty"` // needs the briefing: it is what gets read out
	Sign     *bool `json:"sign,omitempty"`
	// Lights: the zone-light letters to flash ([] = none).
	Lights *[]string `json:"lights,omitempty"`
}

// Drill output states.
const (
	DrillOK      = "ok"
	DrillFailed  = "failed"
	DrillSkipped = "skipped"
	DrillPending = "pending"
)

// DrillOutput is what one output did in a drill. Key: briefing | voice |
// sign | light:<letter>.
type DrillOutput struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	State string `json:"state"` // ok | failed | skipped | pending
	Note  string `json:"note,omitempty"`
}

// DrillRecord is one drill and what came of it. Outputs still "pending"
// are filled in when the briefing and voice finish (poll GET /api/drill).
type DrillRecord struct {
	ID       string        `json:"id"` // the alert's id
	T        int64         `json:"t"`
	Zone     string        `json:"zone"`
	Where    string        `json:"where"` // the zone's name for people
	Level    string        `json:"level"`
	Kind     string        `json:"kind"`
	Outputs  []DrillOutput `json:"outputs"`
	Brief    string        `json:"brief,omitempty"`
	AudioURL string        `json:"audioUrl,omitempty"`
}

// Readiness of an output before a drill is sent.
const (
	ReadyOK       = "ready"
	ReadyFallback = "fallback" // works, through its fallback (template text, browser voice)
	ReadyOffline  = "offline"
	ReadyNone     = "none" // not configured
)

// DrillReady is one output's readiness.
type DrillReady struct {
	Key   string   `json:"key"`
	Label string   `json:"label"`
	State string   `json:"state"` // ready | fallback | offline | none
	Note  string   `json:"note,omitempty"`
	Areas []string `json:"areas,omitempty"` // a zone light: the areas assigned to it
}

// DrillZone is a place a drill can be sent to.
type DrillZone struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Custom bool   `json:"custom"`          // an area staff drew
	Light  string `json:"light,omitempty"` // its zone light
	// What the area's rules allow (false = staff switched it off there).
	Sign  bool `json:"sign"`
	Voice bool `json:"voice"`
}

// DrillStatus is GET /api/drill.
type DrillStatus struct {
	Zones   []DrillZone   `json:"zones"`
	Outputs []DrillReady  `json:"outputs"`
	History []DrillRecord `json:"history"` // newest first, the last DrillKeep
}

// DrillKeep is how many drills the history keeps.
const DrillKeep = 8
