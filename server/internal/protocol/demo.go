package protocol

// Wire types for the judge demo: phone names, shake, the demo spot, staff
// moving a phone on the map, and the privacy receipt. web/shared/demo.ts
// mirrors this file.

// TypeShake is sent to a phone the moment the server sees it being shaken
// (server → phone), so the phone can acknowledge it.
const TypeShake = "shake"

// Shake is the server's "I saw you shake" message.
type Shake struct {
	Type string `json:"type"`
}

// DemoSpot is GET/PUT /api/demo: while On, phones that join without a
// position are placed in a row starting at (X, Y), Spacing metres apart,
// in join order. No GPS and no tap needed.
type DemoSpot struct {
	On      bool    `json:"on"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Spacing float64 `json:"spacing"` // m between phones; 0 = 0.6
}

// Receipt is GET /api/receipt/{id}: everything the server holds about one
// phone session, for the phone's "what was collected" screen.
type Receipt struct {
	ID       string  `json:"id"` // first 8 characters of the random session id
	Name     string  `json:"name,omitempty"`
	Color    string  `json:"color,omitempty"`
	X        float64 `json:"x"` // position in the room, venue metres
	Y        float64 `json:"y"`
	Src      string  `json:"src"`      // gps | manual: how the position was set; none = never placed
	Messages int64   `json:"messages"` // motion summaries received
	Seconds  int64   `json:"seconds"`  // since the phone joined
	// Kept is how many of the last readings the server holds in memory for
	// the dashboard (at most 30 s); they go when the phone is forgotten,
	// ForgetS seconds after it leaves.
	Kept    int `json:"kept"`
	ForgetS int `json:"forgetS"`
	// Recorded: a labelled run was being recorded during this session, so
	// its motion numbers and positions are in a recording file under the
	// random id. Stored: continuous storage (Tiger Data, or its JSONL
	// fallback) is on, so every reading was stored the same way.
	Recorded bool   `json:"recorded"`
	Stored   bool   `json:"stored"`
	Store    string `json:"store,omitempty"` // "Tiger Data" | "a file on the server"
}

// Tower is GET /api/tower/{key}: a fixed thing on the venue map (the
// laptop, the sign, a zone light) that a phone can check in at by joining
// through its QR code (<join url>?at=<key>).
type Tower struct {
	Key  string  `json:"key"` // laptop | sign | a zone-light letter
	Name string  `json:"name"`
	X    float64 `json:"x"` // venue metres
	Y    float64 `json:"y"`
}

// SrcTower: the phone was placed by checking in at a tower.
const SrcTower = "tower"
