package hub

import (
	"encoding/json"

	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// BeaconHandler is an optional extension of Handler: a handler that
// implements it receives the phones' Bluetooth beacon reports (opt-in,
// Android Chrome only). Reports are validated before they are handed over.
type BeaconHandler interface {
	PhoneBeacons(id string, seen []protocol.BeaconSeen)
}

// beacons decodes, validates and delivers one "beacons" message. A bad
// report is dropped whole.
func (hb *Hub) beacons(id string, b []byte) {
	bh, ok := hb.h.(BeaconHandler)
	if !ok {
		return
	}
	var m protocol.Beacons
	if json.Unmarshal(b, &m) != nil || protocol.ValidBeacons(m.Seen) != nil {
		return
	}
	bh.PhoneBeacons(id, m.Seen)
}
