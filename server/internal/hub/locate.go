package hub

import (
	"encoding/json"

	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// LocateHandler is an optional extension of Handler: a handler that
// implements it receives the phones' own dead-reckoning reports ("dr").
type LocateHandler interface {
	PhoneDR(id string, d protocol.DR)
}

// dr decodes and delivers one "dr" message. It reports whether typ was one.
func (hb *Hub) dr(id, typ string, b []byte) bool {
	if typ != protocol.TypeDR {
		return false
	}
	lh, ok := hb.h.(LocateHandler)
	if !ok {
		return true
	}
	var d protocol.DR
	if json.Unmarshal(b, &d) == nil && d.Steps >= 0 {
		lh.PhoneDR(id, d)
	}
	return true
}
