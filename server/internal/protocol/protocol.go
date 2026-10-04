// Package protocol is the source of truth for the JSON wire format.
// web/shared/protocol.ts mirrors it by hand; keep the two in sync.
package protocol

import "encoding/json"

// Message types.
const (
	TypeHello    = "hello"
	TypePos      = "pos"
	TypeGPS      = "gps"
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

// Alert kinds.
const (
	KindWave    = "wave"
	KindDensity = "density"
)

// Position sources.
const (
	SrcManual = "manual"
	SrcGPS    = "gps"
)

// ---- Phone → server ----
//
// Positions are venue metres: origin at the top-left of the venue map, x to
// the right, y down.

// Hello starts a phone session. X/Y place the phone (pointers, so 0 is a
// position, not "absent"). Without them, legacy phones and recordings give
// a grid Row/Col, or the phone sends Lat/Lon/Acc (converted to metres on
// arrival and never stored).
type Hello struct {
	Type string   `json:"type"`
	ID   string   `json:"id"`
	X    *float64 `json:"x,omitempty"`
	Y    *float64 `json:"y,omitempty"`
	Row  int      `json:"row,omitempty"`
	Col  int      `json:"col,omitempty"`
	Lat  *float64 `json:"lat,omitempty"`
	Lon  *float64 `json:"lon,omitempty"`
	Acc  float64  `json:"acc,omitempty"`
	UA   string   `json:"ua,omitempty"`
}

// Pos moves a phone placed by hand (or a simulated phone that walked).
type Pos struct {
	Type string  `json:"type"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
}

// GPS is a Geolocation API fix; Acc is the accuracy radius in metres.
type GPS struct {
	Type string  `json:"type"`
	Lat  float64 `json:"lat"`
	Lon  float64 `json:"lon"`
	Acc  float64 `json:"acc"`
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
	Zone string `json:"zone"` // level of the worst zone containing the phone: calm | yellow | red
}

// ---- Server → dashboard ----

type Node struct {
	ID      string  `json:"id"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Status  string  `json:"status"`
	Sway    float64 `json:"sway"`
	RTT     int64   `json:"rtt"`
	Offset  int64   `json:"offset"`
	AgeMs   int64   `json:"age"`
	UA      string  `json:"ua,omitempty"`
	Zone    string  `json:"zone"`    // first zone containing the phone, "" if none
	Acc     float64 `json:"acc"`     // GPS accuracy (m); 0 = placed by hand
	Src     string  `json:"src"`     // gps | manual
	Outside bool    `json:"outside"` // GPS put it outside the venue (clamped; counts toward nothing)
}

// Point is [x, y] in venue metres.
type Point [2]float64

type Zone struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Level  string  `json:"level"`
	Score  float64 `json:"score"`
	Poly   []Point `json:"poly"`
	Custom bool    `json:"custom"`
	Sens   string  `json:"sens"` // normal | high
}

// Area is a staff-drawn watch area (GET/PUT /api/areas).
type Area struct {
	ID   string  `json:"id"`
	Name string  `json:"name"`
	Sens string  `json:"sens"` // normal | high
	Poly []Point `json:"poly"`
	// Light is the zone light that shows this area's level: the letter it has
	// in SIGN_URL ("A" for A=http://…). Empty = no light.
	Light string `json:"light,omitempty"`
}

// Hardware is GET /api/hardware: one entry per sign or zone light in SIGN_URL,
// checked every few seconds.
type Hardware struct {
	Name     string   `json:"name"` // "Sign", "Zone light A"
	Kind     string   `json:"kind"` // sign | zone-light (as the board reports it)
	URL      string   `json:"url"`
	Zone     string   `json:"zone,omitempty"` // light letter; "" = follows the worst zone
	Online   bool     `json:"online"`
	LastSeen int64    `json:"lastSeen,omitempty"` // server ms
	Error    string   `json:"error,omitempty"`
	RSSI     int      `json:"rssi,omitempty"`   // Wi-Fi signal, dBm
	Uptime   int64    `json:"uptime,omitempty"` // seconds since the board booted
	Level    string   `json:"level,omitempty"`  // what the board is showing
	BLE      *BLEScan `json:"ble,omitempty"`    // Bluetooth crowd counter, if the board has one
	Areas    []string `json:"areas,omitempty"`  // names of the areas this light shows
}

// BLEScan is a zone light's latest Bluetooth count (counts only, no addresses).
type BLEScan struct {
	Devices int   `json:"devices"`
	Near    int   `json:"near"` // strong signal: roughly within a few metres
	Scans   int64 `json:"scans"`
	Age     int64 `json:"age"` // seconds since the last scan finished
}

// Cluster is a group of phones standing close together.
type Cluster struct {
	ID      string  `json:"id"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	R       float64 `json:"r"`
	Count   int     `json:"count"`   // phones
	Density float64 `json:"density"` // phones per m²
	People  int     `json:"people"`  // estimated head count (count / participation)
	Trend   string  `json:"trend"`   // forming | steady | dispersing
	Level   string  `json:"level"`   // calm | yellow | red, from the estimated density
}

// Venue is the venue's size and geo-anchor (GET/PUT /api/venue). Lat/Lon
// is the map's top-left corner; Bearing is degrees clockwise from north of
// the map's up. Geo is false when no anchor is set.
type Venue struct {
	W       float64 `json:"w"`
	H       float64 `json:"h"`
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
	Bearing float64 `json:"bearing"`
	Geo     bool    `json:"geo"`
}

// VenueSize is the snapshot's venue field.
type VenueSize struct {
	W float64 `json:"w"`
	H float64 `json:"h"`
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
	Progress  float64     `json:"progress,omitempty"`
	Recording string      `json:"recording,omitempty"` // label of the run being recorded
	Venue     VenueSize   `json:"venue"`
	Nodes     []Node      `json:"nodes"`
	Zones     []Zone      `json:"zones"`
	Waves     []Wave      `json:"waves"` // direction of travel
	Links     [][2]string `json:"links"` // every neighbour pair the detector compares
	Clusters  []Cluster   `json:"clusters"`
	Stats     Stats       `json:"stats"`
}

type Alert struct {
	Type     string  `json:"type"`
	T        int64   `json:"t"`
	Kind     string  `json:"kind,omitempty"` // wave (default) | density
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

// Config is served at GET /api/config so the phone page can draw the
// venue and the dashboard can draw the zone thresholds.
type Config struct {
	VenueW          float64 `json:"venueW"`
	VenueH          float64 `json:"venueH"`
	Geo             bool    `json:"geo"`
	Yellow          float64 `json:"yellow"`
	Red             float64 `json:"red"`
	NeighbourRadius float64 `json:"neighbourRadius"`
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
	X         float64  `json:"x"`
	Y         float64  `json:"y"`
	Acc       float64  `json:"acc"`
	Src       string   `json:"src"`
	Outside   bool     `json:"outside"`
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
