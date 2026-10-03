// Package protocol is the source of truth for the JSON wire format.
// web/shared/protocol.ts mirrors it by hand; keep the two in sync.
package protocol

import "encoding/json"

// Message types.
const (
	TypeHello    = "hello"
	TypePong     = "pong"
	TypeMotion   = "m"
	TypePing     = "ping"
	TypeState    = "state"
	TypeSnapshot = "snapshot"
	TypeAlert    = "alert"
	TypeAlerts   = "alerts"
)

// Node statuses shown on the dashboard.
const (
	StatusConnecting = "connecting"
	StatusOK         = "ok"
	StatusHandling   = "handling"
	StatusSwaying    = "swaying"
	StatusWave       = "wave"
	StatusStale      = "stale"
)

// Zone levels.
const (
	LevelCalm   = "calm"
	LevelYellow = "yellow"
	LevelRed    = "red"
)

// Envelope is used to peek at the type of an incoming message.
type Envelope struct {
	Type string `json:"type"`
}

// PeekType returns the "type" field of a raw message.
func PeekType(b []byte) (string, error) {
	var e Envelope
	err := json.Unmarshal(b, &e)
	return e.Type, err
}

// ---- Phone → server ----

type Hello struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Row  int    `json:"row"`
	Col  int    `json:"col"`
	UA   string `json:"ua,omitempty"`
}

// Pong replies to Ping. T1 is the phone's clock when it answered.
type Pong struct {
	Type string `json:"type"`
	T0   int64  `json:"t0"`
	T1   int64  `json:"t1"`
}

// Motion is a 100 ms summary of ~6 raw samples: mean acceleration per axis
// (m/s², gravity removed) and max rotation rate (deg/s). T is the phone clock.
type Motion struct {
	Type string  `json:"type"`
	T    int64   `json:"t"`
	AX   float64 `json:"ax"`
	AY   float64 `json:"ay"`
	AZ   float64 `json:"az"`
	Rot  float64 `json:"rot"`
}

// ---- Server → phone ----

type Ping struct {
	Type string `json:"type"`
	T0   int64  `json:"t0"`
}

// PhoneState is optional colour feedback on the phone screen.
type PhoneState struct {
	Type string `json:"type"`
	Node string `json:"node"` // "ok" | "handling" (or any node status)
	Zone string `json:"zone"` // calm | yellow | red
}

// ---- Server → dashboard ----

type Node struct {
	ID     string  `json:"id"`
	Row    int     `json:"row"`
	Col    int     `json:"col"`
	Status string  `json:"status"`
	Sway   float64 `json:"sway"`
	RTT    int64   `json:"rtt"`
	Offset int64   `json:"offset"`
	AgeMs  int64   `json:"age"`
	UA     string  `json:"ua,omitempty"`
}

type Zone struct {
	ID    string  `json:"id"`
	Level string  `json:"level"`
	Score float64 `json:"score"`
	Row0  int     `json:"row0"`
	Col0  int     `json:"col0"`
	Row1  int     `json:"row1"` // inclusive
	Col1  int     `json:"col1"` // inclusive
}

type Wave struct {
	From  string  `json:"from"`
	To    string  `json:"to"`
	LagMs int64   `json:"lagMs"`
	Corr  float64 `json:"corr"`
}

type Stats struct {
	Phones    int     `json:"phones"`
	MsgPerSec float64 `json:"msgPerSec"`
	MedianRTT int64   `json:"medianRtt"`
}

type Snapshot struct {
	Type   string `json:"type"`
	T      int64  `json:"t"`
	Mode   string `json:"mode"`             // live | replay
	Replay string `json:"replay,omitempty"` // name of the recording being replayed
	// Progress of the replay, 0..1.
	Progress  float64 `json:"progress,omitempty"`
	Recording string  `json:"recording,omitempty"` // label of the run being recorded
	Rows      int     `json:"rows"`
	Cols      int     `json:"cols"`
	Nodes     []Node  `json:"nodes"`
	Zones     []Zone  `json:"zones"`
	Waves     []Wave  `json:"waves"`
	Stats     Stats   `json:"stats"`
}

type Alert struct {
	Type     string  `json:"type"`
	T        int64   `json:"t"`
	Zone     string  `json:"zone"`
	Level    string  `json:"level"`
	Score    float64 `json:"score"`
	Brief    string  `json:"brief,omitempty"`
	AudioURL string  `json:"audioUrl,omitempty"`
	Test     bool    `json:"test,omitempty"`
}

// Alerts is sent to a dashboard when it connects: the recent alert log.
type Alerts struct {
	Type   string  `json:"type"`
	Alerts []Alert `json:"alerts"`
}

// Config is served at GET /api/config so the phone page can draw the grid
// and the dashboard can draw the zone thresholds.
type Config struct {
	Rows   int     `json:"rows"`
	Cols   int     `json:"cols"`
	Yellow float64 `json:"yellow"`
	Red    float64 `json:"red"`
}

// Sample is one 100 ms motion summary as the server received it.
type Sample struct {
	T   int64   `json:"t"` // phone clock, ms
	AX  float64 `json:"ax"`
	AY  float64 `json:"ay"`
	AZ  float64 `json:"az"`
	Rot float64 `json:"rot"`
}

// NodeDetail is GET /api/node/{id}: everything the server keeps about one phone.
type NodeDetail struct {
	ID        string   `json:"id"`
	Row       int      `json:"row"`
	Col       int      `json:"col"`
	UA        string   `json:"ua"`
	Zone      string   `json:"zone"`
	Connected bool     `json:"connected"`
	Synced    bool     `json:"synced"`
	RTT       int64    `json:"rtt"`
	Offset    int64    `json:"offset"`
	JoinedAt  int64    `json:"joinedAt"` // server clock, ms; 0 if unknown (replay)
	Messages  int64    `json:"messages"` // motion summaries received
	Samples   []Sample `json:"samples"`  // last ~30 s, oldest first
}
