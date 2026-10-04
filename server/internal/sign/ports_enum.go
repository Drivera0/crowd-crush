//go:build !darwin || cgo

package sign

import (
	"strings"

	"go.bug.st/serial/enumerator"
)

// enumeratePorts lists USB serial ports with their vendor IDs (on macOS the
// enumerator needs cgo, which a normal Mac build has).
func enumeratePorts() ([]PortInfo, error) {
	ps, err := enumerator.GetDetailedPortsList()
	if err != nil {
		return nil, err
	}
	var out []PortInfo
	for _, p := range ps {
		if p.IsUSB {
			out = append(out, PortInfo{Name: p.Name, VID: strings.ToUpper(p.VID)})
		}
	}
	return out, nil
}
