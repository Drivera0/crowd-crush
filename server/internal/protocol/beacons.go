package protocol

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// Bluetooth beacon positioning (opt-in, Android Chrome with the Web
// Bluetooth Scanning flag): a phone reports how strongly it hears the fixed
// Pulse boards, and the server turns that into a position in venue metres.
// The phone only ever reports boards whose name starts with "PULSE-", never
// any other Bluetooth device. web/shared/beacons.ts mirrors this file.

// TypeBeacons is the phone → server beacon report.
const TypeBeacons = "beacons"

// SrcBeacon: the phone's position comes from the boards' Bluetooth beacons.
const SrcBeacon = "beacon"

// Limits of a beacon report.
const (
	BeaconPrefix    = "PULSE-"
	MaxBeaconsSeen  = 16
	MaxBeaconName   = 32
	BeaconRSSIMin   = -110.0
	BeaconRSSIMax   = -20.0
	maxBeaconWindow = 10_000 // samples in one smoothing window; more is nonsense
)

// BeaconSeen is one board as the phone hears it: its beacon name, the
// smoothed signal strength (dBm) and how many adverts went into it.
type BeaconSeen struct {
	Name string  `json:"name"`
	RSSI float64 `json:"rssi"`
	N    int     `json:"n,omitempty"`
}

// Beacons is the report, about once a second:
// {"type":"beacons","seen":[{"name":"PULSE-A","rssi":-61,"n":14}]}.
// An empty list means "I hear no board any more".
type Beacons struct {
	Type string       `json:"type"`
	Seen []BeaconSeen `json:"seen"`
}

// ValidBeacons checks a report: at most MaxBeaconsSeen entries, each a
// distinct "PULSE-…" name of at most MaxBeaconName printable ASCII
// characters with an RSSI between BeaconRSSIMin and BeaconRSSIMax. One bad
// entry rejects the whole report. Whether a name is one of this venue's
// boards is the server's business (unknown names are dropped there).
func ValidBeacons(seen []BeaconSeen) error {
	if len(seen) > MaxBeaconsSeen {
		return fmt.Errorf("%d beacons in one report (max %d)", len(seen), MaxBeaconsSeen)
	}
	names := map[string]bool{}
	for _, s := range seen {
		switch {
		case len(s.Name) > MaxBeaconName:
			return fmt.Errorf("beacon name over %d characters", MaxBeaconName)
		case !strings.HasPrefix(s.Name, BeaconPrefix) || len(s.Name) == len(BeaconPrefix):
			return errors.New("beacon name must be " + BeaconPrefix + "<tag>")
		case !printableASCII(s.Name):
			return errors.New("beacon name has unprintable characters")
		case names[s.Name]:
			return fmt.Errorf("beacon %s listed twice", s.Name)
		case math.IsNaN(s.RSSI) || s.RSSI < BeaconRSSIMin || s.RSSI > BeaconRSSIMax:
			return fmt.Errorf("beacon %s: rssi must be %g…%g dBm", s.Name, BeaconRSSIMin, BeaconRSSIMax)
		case s.N < 0 || s.N > maxBeaconWindow:
			return fmt.Errorf("beacon %s: bad sample count", s.Name)
		}
		names[s.Name] = true
	}
	return nil
}

func printableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] <= ' ' || s[i] > '~' {
			return false
		}
	}
	return true
}

// BeaconModel is the log-distance path-loss model, the same one the
// zone-light firmware uses between boards:
// d = 10^((TxPower1m − RSSI) / (10 · PathLossN)).
type BeaconModel struct {
	TxPower1m float64 `json:"txPower1m"` // dBm heard 1 m from a board
	PathLossN float64 `json:"pathLossN"` // 2 = open air, ~2.2 indoors, 3–4 in a packed crowd
}

// DefaultBeaconModel matches arduino/zone-light (TX_POWER_1M, PATH_LOSS_N).
var DefaultBeaconModel = BeaconModel{TxPower1m: -64, PathLossN: 2.2}

// Distance is the estimated distance (m) to a board heard at rssi dBm.
func (m BeaconModel) Distance(rssi float64) float64 {
	return math.Pow(10, (m.TxPower1m-rssi)/(10*m.PathLossN))
}

// Valid reports whether the constants are usable.
func (m BeaconModel) Valid() error {
	if !(m.TxPower1m >= -100 && m.TxPower1m <= -30) {
		return errors.New("txPower1m must be -100…-30 dBm")
	}
	if !(m.PathLossN >= 1.5 && m.PathLossN <= 5) {
		return errors.New("pathLossN must be 1.5…5")
	}
	return nil
}

// BeaconConfig is data/beacons.json: the model, plus a 1 m reference RSSI
// per beacon name for boards whose radio differs from the rest (the Uno R4
// sign transmits at a different power than the ESP32 zone lights).
type BeaconConfig struct {
	BeaconModel
	Boards map[string]float64 `json:"boards,omitempty"` // beacon name → dBm heard 1 m away
	// ConnTxPower1m is the 1 m reference for connect mode, where the board
	// measures the phone's signal over a Bluetooth connection (a phone's
	// radio is not a board's). 0 = the same as TxPower1m.
	ConnTxPower1m float64 `json:"connTxPower1m,omitempty"`
}

// Conn is the model for a board measuring a connected phone.
func (c BeaconConfig) Conn() BeaconModel {
	m := c.BeaconModel
	if c.ConnTxPower1m != 0 {
		m.TxPower1m = c.ConnTxPower1m
	}
	return m
}

// Where a range came from.
const (
	BeaconSrcScan = "scan" // the phone heard the board's advert (scanning, flag needed)
	BeaconSrcConn = "conn" // the board measured the phone over a connection (connect mode)
	BeaconSrcAdv  = "adv"  // the board heard the phone's own advert (the Pulse Android app)
)

// Connect mode: a zone light's GATT service, and the characteristic the
// phone page writes its Pulse session id to (arduino/zone-light).
const (
	BeaconService = "7b1e0001-52c4-4f6a-9d6b-50554c534500"
	BeaconIDChar  = "7b1e0002-52c4-4f6a-9d6b-50554c534500"
	MaxLinkID     = 36
)

// BeaconLink is one phone as a board measures it, from GET /links (and
// /pulse). In "links" (connect mode): the session id the phone wrote, the
// RSSI of that connection and its age in seconds. In "heard" (the Pulse
// Android app's advert): the first 8 hex characters of the session id, the
// smoothed RSSI of its adverts and the seconds since the last one.
type BeaconLink struct {
	ID   string  `json:"id"`
	RSSI float64 `json:"rssi"`
	Age  float64 `json:"age"`
}

// ValidID8 reports whether s is what the Pulse Android app advertises: the
// first 8 hex characters of a session id.
func ValidID8(s string) bool {
	if len(s) != 8 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// ValidLinkID reports whether id could be a session id written to a board:
// 1…MaxLinkID characters, letters, digits and dashes only.
func ValidLinkID(id string) bool {
	if id == "" || len(id) > MaxLinkID {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-') {
			return false
		}
	}
	return true
}

// For is the model for one beacon: its own 1 m reference if it has one.
func (c BeaconConfig) For(name string) BeaconModel {
	m := c.BeaconModel
	if tx, ok := c.Boards[name]; ok {
		m.TxPower1m = tx
	}
	return m
}

// Valid checks the model and every per-beacon reference.
func (c BeaconConfig) Valid() error {
	if err := c.BeaconModel.Valid(); err != nil {
		return err
	}
	for name := range c.Boards {
		if err := c.For(name).Valid(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if err := c.Conn().Valid(); err != nil {
		return fmt.Errorf("connTxPower1m: %w", err)
	}
	return nil
}

// BeaconSign is the beacon name of the sign's beacon build (SIGN_BEACON in
// arduino/sign), used for the "sign" hardware key unless it reports another.
const BeaconSign = "PULSE-S"

// BeaconBoard is a fixed board that advertises a beacon.
type BeaconBoard struct {
	Name   string   `json:"name"`        // beacon name, e.g. PULSE-A
	Key    string   `json:"key"`         // hardware key (zone-light letter)
	Label  string   `json:"label"`       // "Zone light A"
	X      *float64 `json:"x,omitempty"` // where staff placed it (venue m); absent = not placed
	Y      *float64 `json:"y,omitempty"`
	Online bool     `json:"online"` // answering over Wi-Fi or serial (it may advertise either way)
	// TxPower1m: this beacon's 1 m reference RSSI (dBm); Calibrated when it
	// has its own value rather than the model's.
	TxPower1m  float64 `json:"txPower1m"`
	Calibrated bool    `json:"calibrated,omitempty"`
	// Connect mode: the board's firmware answers GET /links, and how many
	// phones it reports connected right now.
	Connectable bool `json:"connectable,omitempty"`
	Links       int  `json:"links,omitempty"`
	// Heard: phones running the Pulse app that the board hears advertising.
	Heard int `json:"heard,omitempty"`
}

// BeaconInfo is GET /api/beacons: what a phone needs to range against the
// boards (for the diagnostic page).
type BeaconInfo struct {
	Boards []BeaconBoard `json:"boards"`
	Model  BeaconModel   `json:"model"`
	// ConnTxPower1m: the 1 m reference used for connect-mode ranges.
	ConnTxPower1m float64 `json:"connTxPower1m"`
	Service       string  `json:"service"` // connect mode GATT service
	IDChar        string  `json:"idChar"`  // … and the session-id characteristic
	VenueW        float64 `json:"venueW"`
	VenueH        float64 `json:"venueH"`
	NearM         float64 `json:"nearM"`   // "near board X" below this distance
	StaleS        float64 `json:"staleS"`  // a report older than this is no fix
	Prefix        string  `json:"prefix"`  // name prefix the phone filters on
	Placed        int     `json:"placed"`  // boards on the map
	MaxDim        int     `json:"maxDims"` // best fix this layout can give: 0, 1 or 2
}

// BeaconHeard is one board of a report with its estimated distance.
type BeaconHeard struct {
	Name   string  `json:"name"`
	RSSI   float64 `json:"rssi"`
	N      int     `json:"n,omitempty"`
	Dist   float64 `json:"dist"`   // m, from the model
	Placed bool    `json:"placed"` // on the map, so it counted toward the fix
	Src    string  `json:"src"`    // scan | conn | adv
}

// BeaconFix is a position estimated from one beacon report.
//
// Dims says what the geometry allows: 2 = a 2-D fix (three or more boards
// not in a line); 1 = only the position along the line through the boards
// (two boards, or all in a line): X, Y is the point on that line, Along is
// the uncertainty along it and Cross the much larger one across it (which
// side of the line the phone is on cannot be known); 0 = just "next to board
// Near" (one board heard, closer than nearM). With one board further away
// there is only a distance ring and OK is false.
type BeaconFix struct {
	OK    bool    `json:"ok"`
	Dims  int     `json:"dims"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Acc   float64 `json:"acc"`             // overall uncertainty (m, about 1 σ)
	Along float64 `json:"along,omitempty"` // 1-D: uncertainty along the line (m)
	Cross float64 `json:"cross,omitempty"` // 1-D: uncertainty across the line (m)
	// Axis is the unit vector of the line the boards lie on (1-D only).
	Axis *Point `json:"axis,omitempty"`
	// Near is the beacon the phone is right next to (closer than nearM), if any.
	Near  string        `json:"near,omitempty"`
	Note  string        `json:"note"` // what this fix is, in words
	Heard []BeaconHeard `json:"heard"`
	// In GET /api/node/{id} only: how old the report is and whether it is
	// where the phone is shown right now (src "beacon").
	AgeMs int64 `json:"ageMs,omitempty"`
	Used  bool  `json:"used,omitempty"`
}
