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
	"regexp"
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
//	serial:auto                  the sign (an Arduino UNO R4 that says "kind":"sign")
//	A=serial:auto                zone light A (an ESP32 that says zone A, or one with no zone yet)
//	serial:/dev/cu.usbmodem1101  a given port (macOS / Linux)
//	A=serial:COM9                a given port (Windows)
//
// serial:auto tells the boards apart by asking, not by USB IDs (every ESP32
// DevKit has the same CP210x ID): it opens each candidate USB serial port
// once, sends "S" and reads the board's status line (its kind, name and
// zone), keeps the ports some target wants open and hands each to its
// target, and closes the rest. A zone light that has no zone yet (or one no
// target asks for) goes to a zone target nobody else matched, which then
// teaches it its letter ("L calm A"). Unplug a board and its port is
// identified again when it comes back.
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

// SerialSettle is how long to wait after opening a named port before the
// first command (the board may reset or still be booting).
var SerialSettle = 1500 * time.Millisecond

// SerialIdentify is how long serial:auto gives a port to answer "S" (an
// ESP32 may reset when its port opens and needs a moment to boot; the R4
// can be busy for a second while it retries Wi-Fi).
var SerialIdentify = 4 * time.Second

// serialReprobe: a port that didn't answer (or couldn't be opened, e.g.
// another program holds it) is tried again after this long.
var serialReprobe = 15 * time.Second

// usbVIDs are the USB vendor IDs a Pulse board can have.
var usbVIDs = map[string]string{
	"2341": "Arduino",             // UNO R4 WiFi (and other genuine Arduinos)
	"10C4": "Silicon Labs CP210x", // ESP32 DevKit V1
	"1A86": "WCH CH340",           // ESP32 clones
	"0403": "FTDI",
	"303A": "Espressif", // ESP32-S3/C3 native USB
}

// usbNames matches serial device names that are USB adapters, for ports
// whose vendor ID is unknown (the fallback listing).
var usbNames = regexp.MustCompile(`usbmodem|usbserial|SLAB_USBtoUART|wchusbserial|ttyACM|ttyUSB`)

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

// openPort and listPorts are swapped out in tests. quiet opens the port
// with DTR and RTS off (where the OS allows it) instead of on.
var (
	openPort = func(name string, quiet bool) (Port, error) {
		m := &serial.Mode{BaudRate: SerialBaud}
		if quiet {
			m.InitialStatusBits = &serial.ModemOutputBits{}
		}
		return serial.Open(name, m)
	}
	listPorts = systemPorts
)

// quietLines: ports on a USB-UART bridge (an ESP32 DevKit's CP210x or
// CH340) are opened with DTR and RTS left off. Those lines drive the
// ESP32's auto-reset circuit, so asserting them on open rebooted the board
// (seen on Windows: both zone lights restarted every time their port was
// opened). The UNO R4 is the opposite: it needs DTR on to talk at all.
func quietLines(p PortInfo) bool {
	switch p.VID {
	case "10C4", "1A86", "0403", "303A":
		return true
	case "":
		return usbUART.MatchString(p.Name)
	}
	return false
}

var usbUART = regexp.MustCompile(`usbserial|SLAB_USBtoUART|wchusbserial|ttyUSB`)

// portInfo finds a named port's vendor ID (for serial:COM9 and the boards tool).
func portInfo(list func() ([]PortInfo, error), name string) PortInfo {
	if ps, err := list(); err == nil {
		for _, p := range ps {
			if p.Name == name {
				return p
			}
		}
	}
	return PortInfo{Name: name}
}

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
	for _, pat := range []string{"/dev/cu.usbmodem*", "/dev/cu.SLAB_USBtoUART*", "/dev/cu.usbserial*", "/dev/cu.wchusbserial*", "/dev/ttyACM*", "/dev/ttyUSB*"} {
		m, _ := filepath.Glob(pat)
		for _, name := range m {
			out = append(out, PortInfo{Name: name})
		}
	}
	return out
}

// Candidates are the ports that could be a Pulse board, best first: a known
// vendor ID (or, with no IDs known, a USB-serial device name); on macOS the
// /dev/cu.* call-out device, never /dev/tty.* (which blocks on carrier
// detect); R4s (usbmodem / ttyACM) before USB-UART bridges.
func Candidates(ps []PortInfo) []PortInfo {
	seen := map[string]bool{}
	var out []PortInfo
	for _, p := range ps {
		if seen[p.Name] || strings.HasPrefix(p.Name, "/dev/tty.") {
			continue
		}
		if p.VID != "" {
			if _, ok := usbVIDs[p.VID]; !ok {
				continue
			}
		} else if !usbNames.MatchString(p.Name) {
			continue
		}
		seen[p.Name] = true
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := rankPort(out[i]), rankPort(out[j])
		if ri != rj {
			return ri < rj
		}
		return portLess(out[i].Name, out[j].Name)
	})
	return out
}

func rankPort(p PortInfo) int {
	switch {
	case p.VID == "2341", strings.Contains(p.Name, "usbmodem"), strings.Contains(p.Name, "ttyACM"):
		return 0
	case strings.HasPrefix(p.Name, "/dev/cu."):
		return 1
	}
	return 2
}

// portLess orders COM9 before COM11.
func portLess(a, b string) bool {
	if len(a) != len(b) && strings.HasPrefix(a, "COM") && strings.HasPrefix(b, "COM") {
		return len(a) < len(b)
	}
	return a < b
}

// ---- one open port ----

// conn is an open serial port with a goroutine reading its lines.
type conn struct {
	name   string
	port   Port
	wmu    sync.Mutex
	status chan Pulse  // the board's JSON replies (newest only)
	lines  chan string // every non-empty line, for the boards tool (dropped when full)
	done   chan struct{}
	err    error // why reading stopped; set before done closes
}

func dial(open func(string, bool) (Port, error), info PortInfo) (*conn, error) {
	name, quiet := info.Name, quietLines(info)
	p, err := open(name, quiet)
	if err != nil {
		return nil, err
	}
	if !quiet {
		// The UNO R4's USB serial only passes data to the sketch while DTR
		// is asserted (tested on hardware: with DTR off it never answers "S").
		if err := p.SetDTR(true); err != nil {
			log.Printf("sign serial: set DTR on %s: %v", name, err)
		}
		if err := p.SetRTS(true); err != nil {
			log.Printf("sign serial: set RTS on %s: %v", name, err)
		}
	}
	c := &conn{name: name, port: p, status: make(chan Pulse, 1), lines: make(chan string, 64), done: make(chan struct{})}
	go c.read()
	return c, nil
}

func (c *conn) read() {
	sc := bufio.NewScanner(c.port)
	sc.Buffer(make([]byte, 1024), 16<<10)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		select {
		case c.lines <- line:
		default:
		}
		if !strings.HasPrefix(line, "{") {
			continue // boot messages, "level …" echoes, Bluetooth logs
		}
		var st Pulse
		if json.Unmarshal([]byte(line), &st) != nil || st.Kind == "" {
			continue
		}
		select {
		case <-c.status: // keep only the newest
		default:
		}
		select {
		case c.status <- st:
		default:
		}
	}
	err := sc.Err()
	if err == nil {
		err = io.EOF
	}
	c.err = err
	close(c.done)
}

var errNotOpen = errors.New("serial port not open")

func (c *conn) write(s string) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	select {
	case <-c.done:
		return fmt.Errorf("%s closed: %v", c.name, c.err)
	default:
	}
	_, err := io.WriteString(c.port, s)
	return err
}

func (c *conn) close() { c.port.Close() }

func (c *conn) closed() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

var errNoAnswer = errors.New("no answer to S")

// ask sends "S" and waits up to wait for the status line, asking again
// every second.
func (c *conn) ask(ctx context.Context, wait time.Duration) (*Pulse, error) {
	select {
	case <-c.status: // drop a stale reply
	default:
	}
	if err := c.write("S\n"); err != nil {
		return nil, err
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	again := time.NewTicker(min(time.Second, max(wait/4, 20*time.Millisecond)))
	defer again.Stop()
	for {
		select {
		case p := <-c.status:
			return &p, nil
		case <-again.C:
			if err := c.write("S\n"); err != nil {
				return nil, err
			}
		case <-deadline.C:
			return nil, errNoAnswer
		case <-c.done:
			return nil, fmt.Errorf("%s closed: %v", c.name, c.err)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// ---- serial:auto: which board is on which port ----

// usbHub is shared by the serial targets of one Client: it knows which
// ports are taken, which board each identified port holds, and keeps the
// boards some target wants open until that target picks them up.
type usbHub struct {
	open     func(string, bool) (Port, error)
	list     func() ([]PortInfo, error)
	stop     chan struct{} // closed by Client.Close: the targets stop retrying
	mu       sync.Mutex    // held for a whole scan
	links    []*serialLink
	reserved map[string]bool        // named ports (serial:COM7): never opened by auto
	inUse    map[string]*serialLink // ports held by a target
	parked   map[string]*parkedConn // identified, open, waiting for their target
	seen     map[string]seenPort    // identified (or failed) and closed again
}

type parkedConn struct {
	c *conn
	p *Pulse
}

type seenPort struct {
	p   *Pulse // nil = no answer / not openable
	err string
	at  time.Time
}

func newUSBHub() *usbHub {
	return &usbHub{open: openPort, list: listPorts, stop: make(chan struct{}),
		reserved: map[string]bool{}, inUse: map[string]*serialLink{}, parked: map[string]*parkedConn{}, seen: map[string]seenPort{}}
}

func (h *usbHub) add(l *serialLink) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if l.auto() {
		h.links = append(h.links, l)
	} else {
		h.reserved[l.spec] = true
	}
}

// zoneOf is the zone a board says it shows ("" for the sign or a zone
// light that has none yet).
func zoneOf(p *Pulse) string { return strings.ToUpper(strings.TrimSpace(p.Zone)) }

// score says how well a board suits a target: 2 = it is that board (the
// sign; the light that already shows that zone), 1 = it can become it (a
// zone light with no zone, or with a zone no target asks for), 0 = no.
func (h *usbHub) score(l *serialLink, p *Pulse) int {
	if p == nil {
		return 0
	}
	if l.zone == "" {
		if p.Kind == "sign" {
			return 2
		}
		return 0
	}
	if p.Kind != "zone-light" {
		return 0
	}
	z := zoneOf(p)
	if z == l.zone {
		return 2
	}
	if z == "" || !h.wantedZone(z) {
		return 1
	}
	return 0
}

func (h *usbHub) wantedZone(z string) bool {
	for _, o := range h.links {
		if o.zone == z {
			return true
		}
	}
	return false
}

func (h *usbHub) relevant(p *Pulse) bool {
	for _, o := range h.links {
		if h.score(o, p) > 0 {
			return true
		}
	}
	return false
}

// claim finds the board for an auto target: identifies every candidate port
// it doesn't know yet, then hands over the best match.
func (h *usbHub) claim(l *serialLink) (*conn, *Pulse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ps, err := h.list()
	if err != nil {
		return nil, nil, fmt.Errorf("can't list serial ports: %v", err)
	}
	cands := Candidates(ps)
	present := map[string]bool{}
	for _, p := range cands {
		present[p.Name] = true
	}
	for n := range h.seen {
		if !present[n] {
			delete(h.seen, n)
		}
	}
	for n, pc := range h.parked {
		if !present[n] || pc.c.closed() {
			pc.c.close()
			delete(h.parked, n)
		}
	}
	if len(cands) == 0 {
		return nil, nil, errors.New("no Arduino UNO R4 or ESP32 found on USB")
	}
	now := time.Now()
	parkedAny := false
	for _, pi := range cands {
		n := pi.Name
		if h.reserved[n] || h.inUse[n] != nil || h.parked[n] != nil {
			continue
		}
		// A port identified before is skipped while what it holds is of no
		// use to any target (a target may have been added since: reopen it).
		if s, ok := h.seen[n]; ok && ((s.p != nil && !h.relevant(s.p)) || (s.p == nil && now.Sub(s.at) < serialReprobe)) {
			continue
		}
		c, err := dial(h.open, pi)
		if err != nil {
			h.seen[n] = seenPort{err: busyHint(err), at: now}
			continue
		}
		p, err := c.ask(context.Background(), SerialIdentify)
		if p != nil && h.relevant(p) {
			h.parked[n] = &parkedConn{c: c, p: p}
			delete(h.seen, n)
			parkedAny = true
			continue
		}
		c.close()
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		h.seen[n] = seenPort{p: p, err: msg, at: now}
	}
	best, bestScore := "", 0
	for _, pi := range cands {
		pc := h.parked[pi.Name]
		if pc == nil {
			continue
		}
		if s := h.score(l, pc.p); s > bestScore {
			best, bestScore = pi.Name, s
		}
	}
	if parkedAny {
		// Other targets waiting for a board may find theirs parked now.
		for _, o := range h.links {
			if o != l {
				o.nudge()
			}
		}
	}
	if best == "" {
		return nil, nil, fmt.Errorf("no %s on USB (%s)", l.what(), h.describeLocked(cands))
	}
	pc := h.parked[best]
	delete(h.parked, best)
	h.inUse[best] = l
	return pc.c, pc.p, nil
}

// release forgets a port a target lost (unplugged): it is identified again
// when it comes back.
func (h *usbHub) release(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.inUse, name)
	delete(h.seen, name)
}

// describeLocked lists what the candidate ports hold, for error messages.
func (h *usbHub) describeLocked(cands []PortInfo) string {
	var parts []string
	for _, pi := range cands {
		n := pi.Name
		switch {
		case h.inUse[n] != nil:
			parts = append(parts, n+": in use by "+h.inUse[n].what())
		case h.reserved[n]:
			parts = append(parts, n+": named in SIGN_URL")
		case h.parked[n] != nil:
			parts = append(parts, n+": "+boardLabel(h.parked[n].p))
		default:
			s := h.seen[n]
			switch {
			case s.p != nil:
				parts = append(parts, n+": "+boardLabel(s.p))
			case s.err != "":
				parts = append(parts, n+": "+s.err)
			default:
				parts = append(parts, n+": not identified yet")
			}
		}
	}
	return strings.Join(parts, "; ")
}

// boardLabel names a board from its status: "sign", "PULSE-A".
func boardLabel(p *Pulse) string {
	if p == nil {
		return "no answer"
	}
	if p.Kind == "sign" {
		return "sign"
	}
	if p.Name != "" {
		return p.Name
	}
	return p.Kind
}

// busyHint explains the usual reason a port won't open.
func busyHint(err error) string {
	s := err.Error()
	l := strings.ToLower(s)
	if strings.Contains(l, "busy") || strings.Contains(l, "access is denied") || strings.Contains(l, "access denied") || strings.Contains(l, "resource busy") {
		return "busy: another program has it open (Arduino IDE Serial Monitor, a flash script, another Pulse)"
	}
	return s
}

// ---- one serial target ----

// serialLink owns one board's serial port: it keeps it open, writes level
// lines and collects the board's status replies.
type serialLink struct {
	spec   string // "auto" or a port name
	zone   string // the target's zone ("" = the sign): whom auto looks for
	hub    *usbHub
	retry  time.Duration
	settle time.Duration

	mu      sync.Mutex
	c       *conn
	board   *Pulse // what the board said when serial:auto identified it
	want    *state // latest state, re-sent after a reconnect
	lastErr string
	warned  bool // the current failure has been logged

	kick chan struct{}
}

func newSerialLink(spec, zone string, hub *usbHub) *serialLink {
	l := &serialLink{spec: spec, zone: zone, hub: hub, retry: SerialRetry, settle: SerialSettle, kick: make(chan struct{}, 1)}
	hub.add(l) // started by New once every target is known (see run)
	return l
}

func (l *serialLink) auto() bool { return strings.EqualFold(l.spec, "auto") }

// what names the board this target wants, for messages.
func (l *serialLink) what() string {
	if l.zone == "" {
		return "sign"
	}
	return "zone light " + l.zone
}

func (l *serialLink) nudge() {
	select {
	case l.kick <- struct{}{}:
	default:
	}
}

// run keeps trying to open the port while it is closed, until the hub stops.
func (l *serialLink) run() {
	for {
		l.mu.Lock()
		open := l.c != nil
		l.mu.Unlock()
		if !open {
			l.connect()
		}
		select {
		case <-time.After(l.retry):
		case <-l.kick:
		case <-l.hub.stop:
			if c, _ := l.conn(); c != nil {
				c.close()
			}
			return
		}
	}
}

// close stops every serial target and closes their ports.
func (h *usbHub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	select {
	case <-h.stop:
		return
	default:
	}
	close(h.stop)
	for n, pc := range h.parked {
		pc.c.close()
		delete(h.parked, n)
	}
}

func (l *serialLink) connect() {
	var (
		c     *conn
		board *Pulse
		err   error
	)
	if l.auto() {
		c, board, err = l.hub.claim(l)
	} else {
		c, err = dial(l.hub.open, portInfo(l.hub.list, l.spec))
		if err != nil {
			err = fmt.Errorf("%s: %s", l.spec, busyHint(err))
		} else {
			// Let the board settle (it may reset or still be booting) before
			// the first command; until then the port doesn't count as open.
			time.Sleep(l.settle)
			if c.closed() {
				err = fmt.Errorf("%s closed: %v", l.spec, c.err)
				c.close()
			}
		}
	}
	if err != nil {
		l.fail(err.Error())
		return
	}
	l.mu.Lock()
	l.c, l.board, l.lastErr, l.warned = c, board, "", false
	want := l.want
	l.mu.Unlock()
	who := ""
	if board != nil {
		who = " (" + boardLabel(board) + ")"
	}
	log.Printf("sign serial:%s: opened %s%s at %d baud", l.spec, c.name, who, SerialBaud)
	go l.watch(c)
	switch {
	case want != nil:
		if err := l.write(levelLine(*want)); err != nil {
			log.Printf("sign serial:%s: %v", l.spec, err)
		}
	case l.zone != "" && board != nil && zoneOf(board) != l.zone:
		// A light with no zone (or another one) becomes this target's:
		// the zone in an L line is kept in its flash and names its beacon.
		if err := l.write(levelLine(state{"calm", l.zone})); err != nil {
			log.Printf("sign serial:%s: %v", l.spec, err)
		}
	}
}

func (l *serialLink) fail(msg string) {
	l.mu.Lock()
	changed := msg != l.lastErr
	l.lastErr = msg
	first := !l.warned || changed
	l.warned = true
	l.mu.Unlock()
	if first {
		log.Printf("sign serial:%s: %s (retrying every %s)", l.spec, msg, l.retry)
	}
}

// watch closes the target's port when reading stops (unplugged, reset), so
// run reopens it.
func (l *serialLink) watch(c *conn) {
	<-c.done
	l.mu.Lock()
	if l.c != c {
		l.mu.Unlock()
		return
	}
	l.c, l.lastErr = nil, fmt.Sprintf("%s closed: %v", c.name, c.err)
	l.mu.Unlock()
	c.close()
	if l.auto() {
		l.hub.release(c.name)
	}
	log.Printf("sign serial:%s: lost %s (%v), reconnecting", l.spec, c.name, c.err)
	l.nudge()
}

func (l *serialLink) conn() (*conn, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.c, l.lastErr
}

func (l *serialLink) write(line string) error {
	c, lastErr := l.conn()
	if c == nil {
		if lastErr != "" {
			return fmt.Errorf("%w (%s)", errNotOpen, lastErr)
		}
		return errNotOpen
	}
	if err := c.write(line); err != nil {
		c.close() // the reader stops; watch reopens it
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
	c, lastErr := l.conn()
	if c == nil {
		st.Err = lastErr
		if st.Err == "" {
			st.Err = "serial port not open"
		}
		return
	}
	st.Online, st.Port, st.Link = true, c.name, "usb"
	// Older firmware never answers "S": the open port is enough. A single
	// ask: the R4 can be busy for a second retrying Wi-Fi, and the next
	// check comes soon.
	wait := 1500 * time.Millisecond
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) < wait {
		wait = max(time.Until(dl), 10*time.Millisecond)
	}
	select {
	case <-c.status:
	default:
	}
	if err := c.write("S\n"); err != nil {
		c.close()
		st.Online, st.Err = false, err.Error()
		return
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case p := <-c.status:
		st.Pulse = &p
	case <-t.C:
	case <-ctx.Done():
	}
}

// waitOpen waits until the port is open or ctx ends.
func (l *serialLink) waitOpen(ctx context.Context) bool {
	for {
		if c, _ := l.conn(); c != nil {
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
	if l.c != nil && l.c.name != l.spec {
		who := ""
		if l.board != nil {
			who = " (" + boardLabel(l.board) + ")"
		}
		return "serial:" + l.spec + " → " + l.c.name + who
	}
	return "serial:" + l.spec
}
