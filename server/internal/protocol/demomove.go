package protocol

// Moving about at the table demo (app/demomove.go, app/beaconsnap.go).

// DemoView is GET /api/demo/row: what a phone's "I moved" map shows while
// the demo spot is on, so a tap at table scale lands where the person is:
// the row, the boards near it and the other phones with their numbers.
// Positions are venue metres (to the decimetre); no session ids.
type DemoView struct {
	On      bool            `json:"on"`
	X       float64         `json:"x"`       // the demo spot (first place in the row)
	Y       float64         `json:"y"`       //
	Spacing float64         `json:"spacing"` // m between places
	Cols    int             `json:"cols"`    // places in one row before it wraps
	Phones  []DemoViewPhone `json:"phones"`
	Boards  []DemoViewBoard `json:"boards"`
}

// DemoViewPhone is one connected, placed phone on the view.
type DemoViewPhone struct {
	N     int     `json:"n,omitempty"` // its place in the row (1 = first), 0 = off the row
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Name  string  `json:"name,omitempty"`
	Color string  `json:"color,omitempty"`
	Near  string  `json:"near,omitempty"` // beside this board (Bluetooth snap)
}

// DemoViewBoard is a board (or this laptop) staff put on the map.
type DemoViewBoard struct {
	Key   string  `json:"key"`
	Label string  `json:"label"` // "Zone light A", "Sign", "This laptop"
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
}

// DemoBack is POST /api/demo/back's answer: the place in the row the
// phone is back on (1-based).
type DemoBack struct {
	N int     `json:"n"`
	X float64 `json:"x"`
	Y float64 `json:"y"`
}
