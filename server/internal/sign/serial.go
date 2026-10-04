package sign

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"go.bug.st/serial"
)

// A serial target drives a board plugged into this computer over USB, with
// no Wi-Fi involved. SIGN_URL entries:
//
//	serial:auto                  the first Arduino UNO R4 (USB VID 0x2341) or CP210x ESP32 (0x10C4)
//	serial:/dev/cu.usbmodem1101  a given port (macOS / Linux)
//	serial:COM7                  a given port (Windows)
//	A=serial:auto                zone A's light, as with HTTP targets
//
// At 115200 baud the server writes one line per state change,
//
//	L <calm|yellow|red> <zone>\n
//
// (zone omitted when there is none), and "S\n" when it wants a status line;
// the board answers with one JSON line shaped like its GET /pulse. Anything
// else the board prints is ignored. If the port goes away (unplugged, board
// reset), it is reopened every SerialRetry and the current state re-sent.

// SerialBaud is the line speed the firmware expects.
const SerialBaud = 115200

// SerialRetry is how often a closed serial target tries to (re)open its port.
var SerialRetry = 2 * time.Second

// SerialSettle is how long to wait after opening a port before the first
// command (the board may reset or still be booting).
var SerialSettle = 1500 * time.Millisecond

// USB vendor IDs that serial:auto accepts.
var autoVIDs = map[string]string{
	"2341": "Arduino",             // UNO R4 WiFi (and other genuine Arduinos)
	"10C4": "Silicon Labs CP210x", // ESP32 DevKit V1
}

// Port is the part of a serial port the sign uses. Tests fake it.
type Port interface {
	io.ReadWriteCloser
	SetDTR(dtr bool) error
	SetRTS(rts bool) error
}

// PortInfo is one serial port found on this computer.
type PortInfo struct {
	Name string
	VID  string // uppercase hex without 0x, "" if unknown
}

// openPort and listPorts are swapped out in tests.
var (
	openPort = func(name string) (Port, error) {
		return serial.Open(name, &serial.Mode{BaudRate: SerialBaud})
	}
	listPorts = systemPorts
)

// systemPorts lists USB serial ports with their vendor IDs; if the
// enumerator isn't available (macOS without cgo) or fails, it falls back to
// the usual device names (VID unknown).
func systemPorts() ([]PortInfo, error) {
	out, err := enumeratePorts()
	if err == nil {
		return out, nil
	}
	if runtime.GOOS == "windows" {
		return nil, err
	}
	return globPorts(), nil
}

func globPorts() []PortInfo {
	var out []PortInfo
	for _, pat := range []string{"/dev/cu.usbmodem*", "/dev/cu.SLAB_USBtoUART*", "/dev/cu.usbserial*", "/dev/ttyACM*", "/dev/ttyUSB*"} {
		m, _ := filepath.Glob(pat)
		for _, name := range m {
			out = append(out, PortInfo{Name: name})
		}
	}
	return out
}

// pickAuto chooses the port for serial:auto: a known vendor ID first; on
// macOS the /dev/cu.* call-out device rather than /dev/tty.* (which blocks
// on carrier detect). With no vendor IDs known (fallback listing), the
// first usbmodem/ACM port, then any.
func pickAuto(ps []PortInfo) (string, bool) {
	sort.SliceStable(ps, func(i, j int) bool { return rankPort(ps[i]) < rankPort(ps[j]) })
	for _, p := range ps {
		if _, ok := autoVIDs[p.VID]; ok && !strings.HasPrefix(p.Name, "/dev/tty.") {
			return p.Name, true
		}
	}
	for _, p := range ps {
		if p.VID == "" && !strings.HasPrefix(p.Name, "/dev/tty.") {
			return p.Name, true
		}
	}
	return "", false
}

func rankPort(p PortInfo) int {
	switch {
	case strings.Contains(p.Name, "usbmodem"), strings.Contains(p.Name, "ttyACM"):
		return 0
	case strings.HasPrefix(p.Name, "/dev/cu."):
		return 1
	}
	return 2
}

// serialLink owns one serial port: it keeps it open, writes level lines and
// collects the board's status replies.
type serialLink struct {
	spec   string // "auto" or a port name
	open   func(string) (Port, error)
	list   func() ([]PortInfo, error)
	retry  time.Duration
	settle time.Duration

	mu      sync.Mutex
	port    Port
	name    string // port actually open
	want    *state // latest state, re-sent after a reconnect
	lastErr string
	warned  bool // "no board found" logged since the last success

	status chan Pulse // the board's JSON replies (newest only)
	kick   chan struct{}
	gen    int // bumps on every (re)open, so a stale reader can't close a newer port
}

func newSerialLink(spec string) *serialLink {
	l := &serialLink{spec: spec, open: openPort, list: listPorts, retry: SerialRetry, settle: SerialSettle, status: make(chan Pulse, 1), kick: make(chan struct{}, 1)}
	go l.run()
	return l
}

// run keeps trying to open the port while it is closed.
func (l *serialLink) run() {
	for {
		l.mu.Lock()
		open := l.port != nil
		l.mu.Unlock()
		if !open {
			l.connect()
		}
		select {
		case <-time.After(l.retry):
		case <-l.kick:
		}
	}
}

func (l *serialLink) connect() {
	name := l.spec
	if strings.EqualFold(name, "auto") {
		ps, err := l.list()
		if err != nil {
			l.fail(fmt.Sprintf("can't list serial ports: %v", err))
			return
		}
		var ok bool
		if name, ok = pickAuto(ps); !ok {
			l.fail("no Arduino UNO R4 or ESP32 found on USB")
			return
		}
	}
	p, err := l.open(name)
	if err != nil {
		l.fail(fmt.Sprintf("%s: %v", name, err))
		return
	}
	// The UNO R4's USB serial only passes data to the sketch while DTR is
	// asserted (tested on hardware: with DTR off it never answers "S").
	if err := p.SetDTR(true); err != nil {
		log.Printf("sign serial:%s: set DTR on %s: %v", l.spec, name, err)
	}
	if err := p.SetRTS(true); err != nil {
		log.Printf("sign serial:%s: set RTS on %s: %v", l.spec, name, err)
	}
	l.mu.Lock()
	l.gen++
	gen := l.gen
	l.mu.Unlock()
	go l.read(p, gen)
	// Let the board settle (it may reset or still be booting) before the
	// first command; until then the port doesn't count as open.
	time.Sleep(l.settle)
	l.mu.Lock()
	if l.gen != gen {
		l.mu.Unlock()
		p.Close()
		return
	}
	l.port, l.name, l.lastErr, l.warned = p, name, "", false
	want := l.want
	l.mu.Unlock()
	log.Printf("sign serial:%s: opened %s at %d baud", l.spec, name, SerialBaud)
	if want != nil {
		if err := l.write(levelLine(*want)); err != nil {
			log.Printf("sign serial:%s: %v", l.spec, err)
		}
	}
}

func (l *serialLink) fail(msg string) {
	l.mu.Lock()
	l.lastErr = msg
	first := !l.warned
	l.warned = true
	l.mu.Unlock()
	if first {
		log.Printf("sign serial:%s: %s (retrying every %s)", l.spec, msg, l.retry)
	}
}

// read collects lines until the port fails, then closes it so run reopens it.
func (l *serialLink) read(p Port, gen int) {
	sc := bufio.NewScanner(p)
	sc.Buffer(make([]byte, 1024), 16<<10)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "{") {
			continue // boot messages, "level …" echoes
		}
		var st Pulse
		if json.Unmarshal([]byte(line), &st) != nil || st.Kind == "" {
			continue
		}
		select {
		case <-l.status: // keep only the newest
		default:
		}
		l.status <- st
	}
	err := sc.Err()
	if err == nil {
		err = io.EOF
	}
	l.drop(gen, err)
}

// drop closes the port opened as generation gen (if it is still the open one).
func (l *serialLink) drop(gen int, err error) {
	l.mu.Lock()
	if l.gen != gen {
		l.mu.Unlock()
		return
	}
	if l.port == nil { // died while settling: connect gives up on it
		l.gen++
		l.mu.Unlock()
		return
	}
	p, name := l.port, l.name
	l.port, l.lastErr = nil, fmt.Sprintf("%s closed: %v", name, err)
	l.mu.Unlock()
	p.Close()
	log.Printf("sign serial:%s: lost %s (%v), reconnecting", l.spec, name, err)
	select {
	case l.kick <- struct{}{}:
	default:
	}
}

var errNotOpen = errors.New("serial port not open")

func (l *serialLink) write(line string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.port == nil {
		if l.lastErr != "" {
			return fmt.Errorf("%w (%s)", errNotOpen, l.lastErr)
		}
		return errNotOpen
	}
	if _, err := io.WriteString(l.port, line); err != nil {
		p, gen := l.port, l.gen
		l.port, l.lastErr = nil, err.Error()
		l.gen = gen + 1
		go p.Close()
		select {
		case l.kick <- struct{}{}:
		default:
		}
		return err
	}
	return nil
}

func levelLine(s state) string {
	if s.zone == "" {
		return "L " + s.level + "\n"
	}
	return "L " + s.level + " " + s.zone + "\n"
}

// send remembers s (it is re-sent when the port comes back) and writes it.
func (l *serialLink) send(s state) error {
	l.mu.Lock()
	l.want = &s
	l.mu.Unlock()
	return l.write(levelLine(s))
}

// probe reports the port open (online) and, if the board answers "S" in
// time, its status line.
func (l *serialLink) probe(ctx context.Context, st *Status) {
	l.mu.Lock()
	open, name, lastErr := l.port != nil, l.name, l.lastErr
	l.mu.Unlock()
	if !open {
		st.Err = lastErr
		if st.Err == "" {
			st.Err = "serial port not open"
		}
		return
	}
	st.Online, st.Port = true, name
	select {
	case <-l.status: // drop a stale reply
	default:
	}
	if err := l.write("S\n"); err != nil {
		st.Online, st.Err = false, err.Error()
		return
	}
	// Older firmware never answers "S": the open port is enough.
	wait := time.NewTimer(time.Second)
	defer wait.Stop()
	select {
	case p := <-l.status:
		st.Pulse = &p
	case <-wait.C:
	case <-ctx.Done():
	}
}

// waitOpen waits until the port is open or ctx ends.
func (l *serialLink) waitOpen(ctx context.Context) bool {
	for {
		l.mu.Lock()
		open := l.port != nil
		l.mu.Unlock()
		if open {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// describe names the port for logs.
func (l *serialLink) describe() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.port != nil && l.name != l.spec {
		return "serial:" + l.spec + " → " + l.name
	}
	return "serial:" + l.spec
}
