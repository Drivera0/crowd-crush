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
	KindRule    = "rule" // a staff-set area rule (density or capacity)
)

// Alert statuses.
const (
	StatusOpen     = "open"
	StatusAck      = "ack"
	StatusResolved = "resolved"
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
	// G is the unit gravity vector in the device frame ([gx, gy, gz], 2
	// decimals): the direction of "down" as the phone sees it, so the server
	// can level the sample whatever way the phone is carried. Optional; the
	// server holds a phone's last value, so a phone may send it only when it
	// changed. A phone that never sends it is taken to be upright against
	// the chest (x, z horizontal, y vertical).
	G []float64 `json:"g,omitempty"`
	// HD and HB are compass headings in degrees clockwise from north (see
	// locate.go in this package): HD of the phone's top edge, HB of the
	// phone's back. Optional; used only for dead reckoning.
	HD *float64 `json:"hd,omitempty"`
	HB *float64 `json:"hb,omitempty"`
}

// ---- Server → phone ----

type Ping struct {
	Type string `json:"type"`
	T0   int64  `json:"t0"`
}

// PhoneState is colour feedback and personal guidance on the phone screen.
type PhoneState struct {
	Type string `json:"type"`
	Node string `json:"node"` // "ok" | "handling" (or any node status)
	Zone string `json:"zone"` // level of the worst zone containing the phone: calm | yellow | red
	// Move is where this person should go, only while the phone is in
	// danger (red zone, yellow/red cluster or a push passing through).
	Move *Move `json:"move,omitempty"`
	// Bearing is the venue's bearing (degrees clockwise from north of the
	// map's up), only when the venue is GPS-anchored.
	Bearing *float64 `json:"bearing,omitempty"`
	// The phone's position and the venue size (m), for the map on its screen.
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
	// Name and Color: the phone's generated name ("Blue Otter") and its
	// colour (CSS hex), the same as on the dashboard map.
	Name  string `json:"name,omitempty"`
	Color string `json:"color,omitempty"`
	// Sim: this state comes from the crowd simulation running around the
	// phone (a drill), not from the real crowd.
	Sim bool `json:"sim,omitempty"`
}

// Move is personal guidance: a unit vector in venue coordinates (x right,
// y down the map) toward lower density and, when one lies that way, an
// open exit (To = its name; else "less crowded side").
type Move struct {
	DX     float64 `json:"dx"`
	DY     float64 `json:"dy"`
	To     string  `json:"to,omitempty"`
	Reason string  `json:"reason"` // push | density
	// Conf: how far the arrow can be trusted, 0..1 (to 1 decimal): 1 for a
	// phone placed by hand, lower the rougher its GPS fix (crowd.Conf). Below
	// 0.5 (crowd.GuideMinConf) show a plain instruction instead of an arrow.
	Conf float64 `json:"conf"`
}

// Guidance reasons.
const (
	ReasonPush    = "push"
	ReasonDensity = "density"
)

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
	// Name and Color: a real phone's generated name and colour (absent for
	// simulated and replayed phones).
	Name  string `json:"name,omitempty"`
	Color string `json:"color,omitempty"`
	Shake bool   `json:"shake,omitempty"` // being shaken right now ("that's me")
	Real  bool   `json:"real,omitempty"`  // mode "sim": a real phone standing in the simulated crowd
	// Unplaced: connected, but with no position yet (no x/y, no accepted
	// GPS fix, not lined up at the demo spot). x, y are 0 and mean nothing;
	// like an outside phone it counts toward no zone, cluster or neighbour.
	Unplaced bool `json:"unplaced,omitempty"`
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
	Light string      `json:"light,omitempty"`
	Rules *AlertRules `json:"rules,omitempty"`
}

// AlertRules are per-area alert rules on top of the detector's push and
// density alerts.
type AlertRules struct {
	// Density: alert when the estimated people/m² inside the area stays
	// above this for DensityHoldS (red; yellow at 75 %). 0 = off.
	Density      float64 `json:"density,omitempty"`
	DensityHoldS int     `json:"densityHoldS,omitempty"` // 0 = 5 s
	// Push: travelling-wave detection for this area. nil = on.
	Push *bool `json:"push,omitempty"`
	// MaxPhones is a capacity in PEOPLE (the name is kept for compatibility):
	// red when the estimated people inside (phones ÷ participation) stay
	// above it for over 3 s. 0 = off.
	MaxPhones int `json:"maxPhones,omitempty"`
	// Message replaces the generic briefing's action (≤ 140 chars).
	Message string  `json:"message,omitempty"`
	Notify  *Notify `json:"notify,omitempty"`
}

// Notify says where an area's alerts go. nil fields = on.
type Notify struct {
	Sign  *bool `json:"sign,omitempty"`
	Light *bool `json:"light,omitempty"`
	Voice *bool `json:"voice,omitempty"`
}

// Hardware is GET /api/hardware: one entry per sign or zone light in SIGN_URL,
// checked every few seconds.
type Hardware struct {
	Name string `json:"name"` // "Sign", "Zone light A", "This laptop"
	// Key names the entry in PUT /api/hardware/{key}/pos and in a check-in
	// link (?at=<key>): "laptop", "sign" or a zone-light letter.
	Key      string   `json:"key,omitempty"`
	Kind     string   `json:"kind"` // sign | zone-light (as the board reports it)
	URL      string   `json:"url"`
	Zone     string   `json:"zone,omitempty"` // light letter; "" = follows the worst zone
	Online   bool     `json:"online"`
	LastSeen int64    `json:"lastSeen,omitempty"` // server ms
	SeenAgo  int64    `json:"seenAgo,omitempty"`  // seconds since last answer, on the server's clock
	Error    string   `json:"error,omitempty"`
	RSSI     int      `json:"rssi,omitempty"`   // Wi-Fi signal, dBm
	Uptime   int64    `json:"uptime,omitempty"` // seconds since the board booted
	Level    string   `json:"level,omitempty"`  // what the board is showing
	BLE      *BLEScan `json:"ble,omitempty"`    // Bluetooth crowd counter, if the board has one
	Areas    []string `json:"areas,omitempty"`  // names of the areas this light shows
	// Where staff placed the board on the venue map (m); PUT /api/hardware/{key}/pos.
	X      *float64 `json:"x,omitempty"`
	Y      *float64 `json:"y,omitempty"`
	Beacon string   `json:"beacon,omitempty"` // Bluetooth beacon name, e.g. PULSE-A
	Peers  []Peer   `json:"peers,omitempty"`  // other Pulse boards this one hears
}

// Peer is another Pulse board a board hears over Bluetooth.
type Peer struct {
	Name string  `json:"name"`
	RSSI int     `json:"rssi"`
	Dist float64 `json:"dist"` // m, estimated from the signal (log-distance path loss)
	Age  int64   `json:"age"`  // s since last heard
	// MapDist is the distance between the two boards as placed on the map
	// (m), when both are placed and hear each other: a check on Dist.
	MapDist *float64 `json:"mapDist,omitempty"`
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
	Level   string  `json:"level"`   // calm | yellow | red, from Est
	// Est: estimated people per m² at the cluster's densest spot (peak local
	// density ÷ participation, or the disc density if higher). The one
	// density number: cluster levels, early warning, area rules and
	// briefings all use it.
	Est float64 `json:"est"`
	// Rate: how fast the estimated density is changing (people/m² per minute).
	Rate float64 `json:"rate,omitempty"`
	// ETA: projected seconds until the danger density at that rate (early
	// warning); only when it is rising and within earlyWarnS.
	ETA float64 `json:"eta,omitempty"`
	// Acc: median position accuracy of the cluster's phones (m); omitted
	// when they were placed by hand. Several metres = est is the density
	// averaged over a disc about that wide: a lower bound on the tightest spot.
	Acc float64 `json:"acc,omitempty"`
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
	// Template is the preset the size came from (club, theatre, …) or "custom".
	Template string `json:"template,omitempty"`
	// Floorplan: a floor-plan image is stored (GET /api/venue/floorplan).
	Floorplan bool         `json:"floorplan"`
	Layout    *VenueLayout `json:"layout,omitempty"`
}

// VenueLayout is the venue's fixed features, in venue metres.
type VenueLayout struct {
	Stage []Point      `json:"stage,omitempty"` // stage outline
	Exits []LayoutExit `json:"exits,omitempty"`
	Walls [][4]float64 `json:"walls,omitempty"` // [x0, y0, x1, y1]
}

// LayoutExit is an exit or door: the segment (x0, y0)–(x1, y1).
type LayoutExit struct {
	ID   string  `json:"id"`
	Name string  `json:"name"`
	X0   float64 `json:"x0"`
	Y0   float64 `json:"y0"`
	X1   float64 `json:"x1"`
	Y1   float64 `json:"y1"`
}

// FloorplanSuggestion is POST /api/venue/floorplan/analyze: Gemini's reading
// of the stored plan. Nothing is saved until staff PUT /api/venue.
type FloorplanSuggestion struct {
	W          float64     `json:"w"`
	H          float64     `json:"h"`
	Layout     VenueLayout `json:"layout"`
	Notes      string      `json:"notes"`
	Confidence string      `json:"confidence"` // low | medium | high
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
	// Motion: the pair was found by motion (GPS-placed phones, positions
	// only good to metres), not by distance on the map.
	Motion bool `json:"motion,omitempty"`
}

type Stats struct {
	Phones    int     `json:"phones"`
	MsgPerSec float64 `json:"msgPerSec"`
	MedianRTT int64   `json:"medianRtt"`
	// DetectMs: mean detector step time over the last ~5 s (ms, 2 decimals).
	DetectMs float64 `json:"detectMs,omitempty"`
	// SnapshotBytes: size of the last snapshot sent to dashboards.
	SnapshotBytes int `json:"snapshotBytes,omitempty"`
}

// EdgeExplain is GET /api/edge?from=&to=: why the detector did (or didn't)
// call a neighbour pair a travelling wave, from its latest step.
type EdgeExplain struct {
	From   string     `json:"from"`
	To     string     `json:"to"`
	StepMs int64      `json:"stepMs"`
	A      []*float64 `json:"a"` // band-passed horizontal motion, oldest first (m/s²); null = no valid sample
	B      []*float64 `json:"b"`
	Lags   []int64    `json:"lags"` // ms; positive = b moves after a
	Corr   []*float64 `json:"corr"` // |r| per lag; null = too little overlap
	LagMs  int64      `json:"lagMs"`
	Peak   float64    `json:"peak"`
	Second float64    `json:"second"` // best separate peak
	Wave   bool       `json:"wave"`
	Checks []Check    `json:"checks"`
}

// Check is one test the detector applies to a neighbour pair.
type Check struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail"`
}

type Snapshot struct {
	Type   string `json:"type"`
	T      int64  `json:"t"`
	Mode   string `json:"mode"`             // live | replay | sim
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
	Sim       *SimFrame   `json:"sim,omitempty"` // mode "sim" only
	// Status is the one overall status the console shows: the worst of the
	// zones (wave detector), area rules and clusters.
	Status *Status `json:"status,omitempty"`
	// Mesh: the phone-to-phone links and how each phone reaches the server
	// (mesh.go); absent in a replay.
	Mesh *MeshFrame `json:"mesh,omitempty"`
}

// Status kinds: what makes the worst place the worst.
const (
	StatusKindWave    = "wave"
	StatusKindDensity = "density"
	StatusKindRule    = "rule"
	StatusKindEarly   = "early"
)

// Status is the snapshot's overall status. Score is the single crowd-risk
// number, 0..1, banded by level: calm < 0.5 ≤ yellow < 0.8 ≤ red. Zone is
// the worst place's zone id and Where its name for people (a drawn area's
// name when the hotspot is inside one, else the zone's name; never an id);
// Zone, Where and Kind are empty when calm. Density is the estimated
// people/m² at the worst spot when density is the reason (density, early,
// a density rule).
type Status struct {
	Level   string  `json:"level"`
	Score   float64 `json:"score"`
	Zone    string  `json:"zone,omitempty"`
	Where   string  `json:"where,omitempty"`
	Kind    string  `json:"kind,omitempty"` // wave | density | rule | early
	Density float64 `json:"density,omitempty"`
}

// Alert is one incident. Later messages with the same ID update it (level
// rising, briefing arriving, ack, resolve, escalation); the dashboard
// upserts by ID.
type Alert struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	T    int64  `json:"t"`              // when the incident opened (server ms)
	Kind string `json:"kind,omitempty"` // wave (default) | density | rule
	// Early: a density pre-warning, not dangerous yet but projected to be
	// soon (cleared when the incident goes red).
	Early    bool    `json:"early,omitempty"`
	Zone     string  `json:"zone"`
	Level    string  `json:"level"` // the worst level the incident reached
	Score    float64 `json:"score"`
	Brief    string  `json:"brief,omitempty"`    // headline + " " + action, for voice and timeline
	Headline string  `json:"headline,omitempty"` // what is happening and where
	Action   string  `json:"action,omitempty"`   // the one thing staff should do
	AudioURL string  `json:"audioUrl,omitempty"`
	Test     bool    `json:"test,omitempty"`
	Status   string  `json:"status"` // open | ack | resolved
	AckAt    int64   `json:"ackAt,omitempty"`
	// ResolvedAt is when staff closed it (server ms).
	ResolvedAt int64 `json:"resolvedAt,omitempty"`
	// Audit trail: who acknowledged / resolved it (free text from the
	// console) and the outcome note given on resolve (≤ 280 chars).
	AckBy      string `json:"ackBy,omitempty"`
	ResolvedBy string `json:"resolvedBy,omitempty"`
	Note       string `json:"note,omitempty"`
	Escalated  bool   `json:"escalated,omitempty"` // red and unacknowledged past the escalation delay
}

// JoinInfo is GET /api/join: the URL phones should open (the QR code) and
// how far it reaches: public (PUBLIC_URL set, or a public hostname), lan
// (a private IP) or local (localhost / loopback).
type JoinInfo struct {
	URL       string `json:"url"`
	Reachable string `json:"reachable"` // public | lan | local
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
	// Demo: the demo spot is on (GET /api/demo): a phone that joins is
	// placed by the server, so the phone page skips GPS and the map.
	Demo bool `json:"demo,omitempty"`
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
	// Beacons: the Pulse boards this phone hears over Bluetooth and the fix
	// from them (beacons.go); absent unless the phone reports beacons.
	Beacons *BeaconFix `json:"beacons,omitempty"`
}

// ---- Crowd simulation (mode "sim") ----

// SimFrame is the snapshot's sim field while mode is "sim": every simulated
// person as [x, y, pressure N/m, hasPhone 0|1] (x, y to the centimetre,
// pressure in whole N/m), the sim time (s since start) and the last
// behaviour action.
type SimFrame struct {
	Bodies [][4]float64 `json:"bodies"`
	T      float64      `json:"t"`
	Action string       `json:"action"`
}

// SimExit is an exit gap in the simulated venue's wall.
type SimExit struct {
	ID   string  `json:"id"`
	Name string  `json:"name"`
	X0   float64 `json:"x0"`
	Y0   float64 `json:"y0"`
	X1   float64 `json:"x1"`
	Y1   float64 `json:"y1"`
	Open bool    `json:"open"`
}

// SimTruth is the simulation's ground truth and Pulse's lead time. Times
// are seconds since the sim started; null = has not happened.
type SimTruth struct {
	MaxDensity  float64  `json:"maxDensity"`  // people/m², densest 3 × 3 m square
	MaxPressure float64  `json:"maxPressure"` // N/m, highest per-person pressure
	Crushing    int      `json:"crushing"`    // people ≥ 1600 N/m or > 6 people/m² within 1 m
	DangerAt    *float64 `json:"dangerAt"`    // truth first dangerous (start of a ≥ 1 s stretch)
	AlertAt     *float64 `json:"alertAt"`     // Pulse's first red alert (wave or density)
	LeadSeconds *float64 `json:"leadSeconds"` // dangerAt − alertAt: positive = Pulse warned first
}

// SimStatus is GET /api/sim. When not running only Running, Exits and
// Walls (the venue's layout) are set.
type SimStatus struct {
	Running       bool         `json:"running"`
	T             float64      `json:"t,omitempty"`
	People        int          `json:"people,omitempty"`
	Phones        int          `json:"phones,omitempty"`
	Participation float64      `json:"participation,omitempty"`
	Action        string       `json:"action,omitempty"`
	Exits         []SimExit    `json:"exits"`
	Walls         [][4]float64 `json:"walls"`
	Truth         *SimTruth    `json:"truth,omitempty"`
}
