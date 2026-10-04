package protocol

import "encoding/json"

// Wire types for the phone-to-phone mesh (WebRTC data channels between
// phones, signalled over the phone WebSocket). web/shared/mesh.ts mirrors
// this file.
//
// Phones never learn each other's session ids: on the mesh, in signalling
// and in relay envelopes a phone is named by its handle, the first 12 hex
// characters of SHA-256(session id) (hub.Handle). The server maps handles
// back to ids.

// Message types.
const (
	TypeRTC    = "rtc"   // phone → server: this browser can open WebRTC links
	TypeMesh   = "mesh"  // server → phone: who to link with
	TypeSignal = "sig"   // both ways: offer / answer / ICE candidate, relayed by the server
	TypeNear   = "near"  // phone → server: its direct links and proximity evidence
	TypeRelay  = "relay" // both ways: a message carried for a phone that has no socket of its own
	TypeJam    = "jam"   // both ways: stop / resume using the WebSocket (the lost-signal demo)
	TypeClock  = "clock" // server → phone: the phone's clock offset, so mesh traces share one time base
)

// Signal kinds.
const (
	SigOffer  = "offer"
	SigAnswer = "answer"
	SigICE    = "ice"
	// SigHi: "I have no link to you yet: call me", from the side that does
	// not offer (a page that was just reloaded, say).
	SigHi = "hi"
)

// MaxRelayHops is how many phones a relayed message may pass through.
const MaxRelayHops = 3

// RTC says the phone can (On) or can no longer open WebRTC links.
type RTC struct {
	Type string `json:"type"`
	On   bool   `json:"on"`
}

// MeshPeers tells a phone who to link with: its own handle, the full list of
// its peers (links to anyone else are closed) and the ICE servers to use
// (empty = host candidates only: same network).
type MeshPeers struct {
	Type  string     `json:"type"`
	Me    string     `json:"me"`
	Peers []MeshPeer `json:"peers"`
	ICE   []string   `json:"ice"`
}

// MeshPeer is one peer; Init = this phone sends the offer.
type MeshPeer struct {
	ID   string `json:"id"`
	Init bool   `json:"init,omitempty"`
}

// Signal carries WebRTC signalling between two phones the server paired.
// Phone → server: To is the peer's handle. Server → phone: From is.
type Signal struct {
	Type string          `json:"type"`
	To   string          `json:"to,omitempty"`
	From string          `json:"from,omitempty"`
	Kind string          `json:"kind"` // offer | answer | ice
	Data json.RawMessage `json:"data"`
}

// Near is a phone's report, about once a second: every peer it has an open
// data channel with, and how its own motion correlates with that peer's.
type Near struct {
	Type  string     `json:"type"`
	Peers []NearPeer `json:"peers"`
	// Failed: peers the phone gave up on (no link within the timeout); the
	// server pairs it with someone else.
	Failed []string `json:"failed,omitempty"`
	// Tx, Rx: bytes per second on the phone's data channels (payload only).
	Tx int `json:"tx,omitempty"`
	Rx int `json:"rx,omitempty"`
	// Known: phones in its gossip table (direct peers included).
	Known int `json:"known,omitempty"`
}

// NearPeer is one direct link. ID is the peer's handle on the wire and its
// session id once the hub has resolved it. Corr is the peak |r| of the
// lagged cross-correlation of the two phones' horizontal sway traces over
// the last few seconds (0 = not enough motion to say); LagMs is where the
// peak is (positive = the peer moves after this phone). Hops is 1 (a direct
// link); RTT is the data channel's round trip (ms).
type NearPeer struct {
	ID    string  `json:"id"`
	Corr  float64 `json:"corr"`
	LagMs int64   `json:"lagMs"`
	Hops  int     `json:"hops"`
	RTT   int64   `json:"rtt,omitempty"`
}

// Relay wraps a message for a phone that has no working socket. Phone →
// server: From is the origin's handle, Hops how many phones it passed
// through (1 = the sender heard it directly), Msg an ordinary phone → server
// message. Server → phone: To is the handle of the phone the sender should
// pass Msg (an ordinary server → phone message) on to.
type Relay struct {
	Type string          `json:"type"`
	From string          `json:"from,omitempty"`
	To   string          `json:"to,omitempty"`
	Hops int             `json:"hops,omitempty"`
	Msg  json.RawMessage `json:"msg"`
}

// Jam: server → phone, On = close the WebSocket and send everything through
// the mesh, Off = reconnect. Phone → server: On = "I am about to do that"
// (also sent for the phone's own "Simulate lost signal" switch), Off = "I
// can't: no mesh link reaches the server".
type Jam struct {
	Type string `json:"type"`
	On   bool   `json:"on"`
}

// Clock tells a phone its clock offset (phone clock − server clock, ms).
type Clock struct {
	Type   string `json:"type"`
	Offset int64  `json:"offset"`
}

// MeshFrame is the snapshot's mesh field: the phone-to-phone links as the
// phones report them, and how each phone reaches the server.
type MeshFrame struct {
	// Links are pairs of indexes into the snapshot's nodes.
	Links [][2]int   `json:"links"`
	Nodes []MeshNode `json:"nodes"`
	// Phones connected (their own socket or relayed), how many of them sent
	// a reading in the last 2 s, and how many of those arrive through the mesh.
	Phones    int `json:"phones"`
	Reporting int `json:"reporting"`
	ViaMesh   int `json:"viaMesh"`
	// Virtual: the links are the server's stand-in for simulated phones
	// (which have no browsers), not real WebRTC links.
	Virtual bool `json:"virtual,omitempty"`
}

// MeshNode is one phone's mesh state. Via is the session id of the phone
// that hands its messages to the server ("" = its own socket) and Hops how
// many phones they pass through.
type MeshNode struct {
	ID    string `json:"id"`
	Via   string `json:"via,omitempty"`
	Hops  int    `json:"hops,omitempty"`
	Peers int    `json:"peers"`
	RTT   int64  `json:"rtt,omitempty"` // mean data-channel round trip, ms
	Jam   bool   `json:"jam,omitempty"` // told (or chose) to drop its WebSocket
	Tx    int    `json:"tx,omitempty"`  // bytes/s on its data channels
	Rx    int    `json:"rx,omitempty"`
	Known int    `json:"known,omitempty"`
	// Pos: the phone's own mesh-corrected position estimate, if it sent one lately.
	Pos *MPos `json:"pos,omitempty"`
}

// MeshStatus is GET /api/mesh.
type MeshStatus struct {
	ICE     []string `json:"ice"`
	Capable int      `json:"capable"` // phones that can open WebRTC links
	Links   int      `json:"links"`   // open links (as reported by the phones)
	Pairs   int      `json:"pairs"`   // pairs the server has assigned
	Jammed  []string `json:"jammed"`  // session ids
	Relayed int      `json:"relayed"`
	TxBytes int      `json:"txBytes"` // sum of the phones' data-channel bytes/s
	// Relay counters since the server started.
	RelayIn      int64 `json:"relayIn"`
	RelayDropped int64 `json:"relayDropped"`
}

// TypeMPos is a phone's own mesh-corrected position estimate (phone → server).
const TypeMPos = "mpos"

// MPos is the position a phone worked out with its mesh neighbours: its own
// estimate pulled toward the estimates of the direct peers it shares motion
// with. X, Y in venue metres, Acc its 1-sigma accuracy (m), Hops how many
// phones away the nearest anchored source is (0 = this phone is anchored;
// absent = no anchor in reach), N how many neighbours went into it. The
// server stores it as one more input for its own estimator; it never
// replaces the server's position.
type MPos struct {
	Type string  `json:"type,omitempty"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Acc  float64 `json:"acc"`
	Hops *int    `json:"hops,omitempty"`
	N    int     `json:"n,omitempty"`
	Src  string  `json:"src,omitempty"` // where the phone's own starting estimate came from
}
