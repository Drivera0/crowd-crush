//go:build darwin && !cgo

package sign

import "errors"

// enumeratePorts needs cgo on macOS; without it serial:auto falls back to
// the usual device names (/dev/cu.usbmodem*, /dev/cu.SLAB_USBtoUART*, …).
func enumeratePorts() ([]PortInfo, error) {
	return nil, errors.New("USB port enumeration needs cgo on macOS")
}
