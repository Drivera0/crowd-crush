package sign

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// USB access for the boards tool (server/cmd/boards): find every Pulse
// board plugged into this computer and talk to one directly. The server
// itself uses serial targets (serial.go); a port the running server holds
// is busy for everyone else.

// USBBoard is one candidate port and what is on it.
type USBBoard struct {
	Port  string
	VID   string
	Pulse *Pulse // its status line; nil = no answer (not a Pulse board, old firmware, busy)
	Err   string
}

// Label names the board: "sign", "PULSE-A", or "no answer".
func (b USBBoard) Label() string { return boardLabel(b.Pulse) }

// ScanUSB opens every candidate port in turn, asks "S" and closes it again.
func ScanUSB(ctx context.Context, wait time.Duration) ([]USBBoard, error) {
	ps, err := listPorts()
	if err != nil {
		return nil, err
	}
	cands := Candidates(ps)
	out := make([]USBBoard, len(cands))
	var wg sync.WaitGroup
	for i, pi := range cands {
		out[i] = USBBoard{Port: pi.Name, VID: pi.VID}
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := dial(openPort, pi)
			if err != nil {
				out[i].Err = busyHint(err)
				return
			}
			defer c.close()
			p, err := c.ask(ctx, wait)
			out[i].Pulse = p
			if err != nil {
				out[i].Err = err.Error()
			}
		}()
	}
	wg.Wait()
	return out, nil
}

// USBConn is one open board port.
type USBConn struct{ c *conn }

// OpenUSB opens a board's port (DTR and RTS on, like the server).
func OpenUSB(name string) (*USBConn, error) {
	c, err := dial(openPort, portInfo(listPorts, name))
	if err != nil {
		return nil, errors.New(busyHint(err))
	}
	return &USBConn{c}, nil
}

// Close closes the port.
func (u *USBConn) Close() { u.c.close() }

// Status asks "S" (again every second) until the board answers or wait passes.
func (u *USBConn) Status(ctx context.Context, wait time.Duration) (*Pulse, error) {
	return u.c.ask(ctx, wait)
}

// Send writes one command line (a newline is added).
func (u *USBConn) Send(line string) error { return u.c.write(strings.TrimRight(line, "\r\n") + "\n") }

// Drain throws away the lines read so far.
func (u *USBConn) Drain() {
	for {
		select {
		case <-u.c.lines:
		default:
			return
		}
	}
}

// WaitLine waits for a line starting with prefix and returns it.
func (u *USBConn) WaitLine(ctx context.Context, prefix string, wait time.Duration) (string, bool) {
	t := time.NewTimer(wait)
	defer t.Stop()
	for {
		select {
		case l := <-u.c.lines:
			if strings.HasPrefix(l, prefix) {
				return l, true
			}
		case <-t.C:
			return "", false
		case <-u.c.done:
			return "", false
		case <-ctx.Done():
			return "", false
		}
	}
}
