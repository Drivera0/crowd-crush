package sign

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBay is several boards on several fake ports, each answering "S" with
// its own identity, like the sign and two zone lights on one USB hub.
type fakeBay struct {
	mu     sync.Mutex
	boards map[string]*bayBoard // port name → board
	opens  []string
	quiet  map[string]bool // port → opened with DTR/RTS off
}

type bayBoard struct {
	kind, zone, name string
	level            string
	lines            chan string
	toHost           *io.PipeWriter
}

func (b *bayBoard) json() string {
	return fmt.Sprintf(`{"kind":%q,"name":%q,"zone":%q,"level":%q,"fw":"abcdef0 2026-10-04","wifi":false,"ssid":"","uptime":5}`,
		b.kind, b.name, b.zone, b.level)
}

type bayPort struct {
	bay    *fakeBay
	b      *bayBoard
	r      *io.PipeReader
	closed chan struct{}
	once   sync.Once
}

func (p *bayPort) SetDTR(bool) error            { return nil }
func (p *bayPort) SetRTS(bool) error            { return nil }
func (p *bayPort) Read(buf []byte) (int, error) { return p.r.Read(buf) }
func (p *bayPort) Close() error {
	p.once.Do(func() { close(p.closed); p.r.Close() })
	return nil
}

func (p *bayPort) Write(buf []byte) (int, error) {
	select {
	case <-p.closed:
		return 0, errors.New("port closed")
	default:
	}
	for _, l := range strings.Split(strings.TrimSpace(string(buf)), "\n") {
		p.bay.mu.Lock()
		b := p.b
		if l == "S" {
			w, msg := b.toHost, b.json()
			p.bay.mu.Unlock()
			go io.WriteString(w, "ble: 3 devices\n"+msg+"\n")
			continue
		}
		if f := strings.Fields(l); len(f) >= 2 && f[0] == "L" {
			b.level = f[1]
			if len(f) == 3 && b.kind == "zone-light" {
				b.zone, b.name = f[2], "PULSE-"+f[2]
			}
		}
		p.bay.mu.Unlock()
		select {
		case b.lines <- l:
		default:
		}
	}
	return len(buf), nil
}

func (bay *fakeBay) open(name string, quiet bool) (Port, error) {
	bay.mu.Lock()
	defer bay.mu.Unlock()
	b := bay.boards[name]
	if b == nil {
		return nil, errors.New("no such port")
	}
	bay.opens = append(bay.opens, name)
	bay.quiet[name] = quiet
	r, w := io.Pipe()
	b.toHost = w
	return &bayPort{bay: bay, b: b, r: r, closed: make(chan struct{})}, nil
}

func newBay() *fakeBay { return &fakeBay{boards: map[string]*bayBoard{}, quiet: map[string]bool{}} }

func (bay *fakeBay) add(port, kind, zone, name string) *bayBoard {
	b := &bayBoard{kind: kind, zone: zone, name: name, level: "calm", lines: make(chan string, 20)}
	bay.boards[port] = b
	return b
}

func (bay *fakeBay) use(t *testing.T, ports func() []PortInfo) {
	t.Helper()
	oldOpen, oldList, oldRetry, oldSettle, oldIdent, oldRe := openPort, listPorts, SerialRetry, SerialSettle, SerialIdentify, serialReprobe
	openPort = bay.open
	listPorts = func() ([]PortInfo, error) { return ports(), nil }
	SerialRetry = 20 * time.Millisecond
	SerialSettle = 10 * time.Millisecond
	SerialIdentify = 300 * time.Millisecond
	serialReprobe = 50 * time.Millisecond
	t.Cleanup(func() {
		openPort, listPorts, SerialRetry, SerialSettle, SerialIdentify, serialReprobe = oldOpen, oldList, oldRetry, oldSettle, oldIdent, oldRe
	})
}

func nextLine(t *testing.T, b *bayBoard) string {
	t.Helper()
	select {
	case l := <-b.lines:
		return l
	case <-time.After(3 * time.Second):
		t.Fatal("no line written to the board")
		return ""
	}
}

// Three boards on one hub: each auto target finds its own by asking, not by
// USB ID (both ESP32s share the CP210x ID), whatever order they enumerate in.
func TestSerialAutoTellsBoardsApart(t *testing.T) {
	bay := newBay()
	lightB := bay.add("COM9", "zone-light", "B", "PULSE-B")
	lightA := bay.add("COM11", "zone-light", "A", "PULSE-A")
	sign := bay.add("COM7", "sign", "", "")
	bay.use(t, func() []PortInfo {
		return []PortInfo{{Name: "COM9", VID: "10C4"}, {Name: "COM11", VID: "10C4"}, {Name: "COM7", VID: "2341"}, {Name: "COM3", VID: "8087"}}
	})
	c := New("serial:auto,A=serial:auto,B=serial:auto")
	t.Cleanup(c.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, tg := range c.targets {
		if !tg.ser.waitOpen(ctx) {
			t.Fatalf("%s never opened: %s", tg.ser.what(), tg.ser.lastErr)
		}
	}
	c.Update(map[string]string{"A": "red", "B": "yellow"}, "red", "A")
	if got := nextLine(t, lightA); got != "L red A" {
		t.Errorf("light A got %q", got)
	}
	if got := nextLine(t, lightB); got != "L yellow B" {
		t.Errorf("light B got %q", got)
	}
	if got := nextLine(t, sign); got != "L red A" {
		t.Errorf("sign got %q", got)
	}
	st := c.Probe(ctx)
	ports := map[string]string{}
	for _, s := range st {
		if !s.Online || s.Link != "usb" || s.Pulse == nil {
			t.Fatalf("status %+v", s)
		}
		ports[s.Zone] = s.Port
	}
	if ports[""] != "COM7" || ports["A"] != "COM11" || ports["B"] != "COM9" {
		t.Fatalf("ports %v", ports)
	}
	// Each port was opened once: identified boards stay open for their target.
	bay.mu.Lock()
	n := len(bay.opens)
	bay.mu.Unlock()
	if n != 3 {
		t.Errorf("opened %d times: %v", n, bay.opens)
	}
	// The ESP32s' CP210x lines stay off (they reset the board); the R4 gets DTR.
	bay.mu.Lock()
	q := bay.quiet
	bay.mu.Unlock()
	if !q["COM9"] || !q["COM11"] || q["COM7"] {
		t.Errorf("quiet opens %v", q)
	}
	if d := c.Describe(); !strings.Contains(d, "COM11 (PULSE-A)") {
		t.Errorf("describe %q", d)
	}
}

// A fresh zone light (no zone yet) goes to the zone target nobody else
// matched and is taught its letter; the light that already says A keeps A.
func TestSerialAutoTeachesZone(t *testing.T) {
	bay := newBay()
	fresh := bay.add("/dev/cu.usbserial-0001", "zone-light", "", "PULSE-7B54")
	lightA := bay.add("/dev/cu.usbserial-0002", "zone-light", "A", "PULSE-A")
	bay.use(t, func() []PortInfo {
		return []PortInfo{{Name: "/dev/cu.usbserial-0001"}, {Name: "/dev/cu.usbserial-0002"}}
	})
	c := New("A=serial:auto,B=serial:auto")
	t.Cleanup(c.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, tg := range c.targets {
		if !tg.ser.waitOpen(ctx) {
			t.Fatalf("%s never opened: %s", tg.ser.what(), tg.ser.lastErr)
		}
	}
	if got := nextLine(t, fresh); got != "L calm B" {
		t.Fatalf("fresh light got %q, want to be taught B", got)
	}
	c.Update(map[string]string{"A": "red", "B": "calm"}, "red", "A")
	if got := nextLine(t, lightA); got != "L red A" {
		t.Errorf("light A got %q", got)
	}
}

// No board for a target: the error says what is on each port.
func TestSerialAutoExplainsMissingBoard(t *testing.T) {
	bay := newBay()
	bay.add("COM9", "zone-light", "A", "PULSE-A")
	bay.use(t, func() []PortInfo { return []PortInfo{{Name: "COM9", VID: "10C4"}} })
	c := New("serial:auto,A=serial:auto")
	t.Cleanup(c.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if !c.targets[1].ser.waitOpen(ctx) {
		t.Fatal("light A never opened")
	}
	time.Sleep(100 * time.Millisecond)
	st := c.Probe(ctx)
	if st[0].Online || !strings.Contains(st[0].Err, "no sign on USB") || !strings.Contains(st[0].Err, "COM9: in use by zone light A") {
		t.Fatalf("sign status %+v", st[0])
	}
}

// Unplug a board and plug it into another port: it is found again there.
func TestSerialAutoReplug(t *testing.T) {
	bay := newBay()
	light := bay.add("COM9", "zone-light", "A", "PULSE-A")
	var mu sync.Mutex
	ports := []PortInfo{{Name: "COM9", VID: "10C4"}}
	bay.use(t, func() []PortInfo { mu.Lock(); defer mu.Unlock(); return append([]PortInfo(nil), ports...) })
	c := New("A=serial:auto")
	t.Cleanup(c.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.targets[0].ser.waitOpen(ctx)
	c.Set("yellow", "A")
	if got := nextLine(t, light); got != "L yellow A" {
		t.Fatalf("got %q", got)
	}
	// Unplug from COM9, replug as COM12.
	mu.Lock()
	ports = []PortInfo{{Name: "COM12", VID: "10C4"}}
	mu.Unlock()
	bay.mu.Lock()
	bay.boards["COM12"] = light
	delete(bay.boards, "COM9")
	w := light.toHost
	bay.mu.Unlock()
	w.CloseWithError(errors.New("device not configured"))
	if got := nextLine(t, light); got != "L yellow A" {
		t.Fatalf("after replug got %q", got)
	}
	st := c.Probe(ctx)
	if st[0].Port != "COM12" {
		t.Fatalf("status %+v", st[0])
	}
}

// Test drives every board, confirms the level came back, then restores.
func TestBoardTest(t *testing.T) {
	bay := newBay()
	light := bay.add("COM9", "zone-light", "A", "PULSE-A")
	bay.use(t, func() []PortInfo { return []PortInfo{{Name: "COM9", VID: "10C4"}} })
	c := New("A=serial:auto")
	t.Cleanup(c.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.targets[0].ser.waitOpen(ctx)
	c.Set("yellow", "A")
	if got := nextLine(t, light); got != "L yellow A" {
		t.Fatalf("got %q", got)
	}
	res := c.Test(ctx, "red", 100*time.Millisecond)
	if len(res) != 1 || !res[0].Sent || !res[0].Confirmed || res[0].Link != "usb" || res[0].Key != "A" || res[0].FW == "" {
		t.Fatalf("result %+v", res)
	}
	if got := nextLine(t, light); got != "L red A" {
		t.Fatalf("test sent %q", got)
	}
	if got := nextLine(t, light); got != "L yellow A" {
		t.Fatalf("restored %q, want the level from before the test", got)
	}
}

func TestSketchHash(t *testing.T) {
	// git hash-object of "hi\n" is 45b983be36b73c0788dc9cbcb76cbb80fc7bb057.
	if h := sketchHash([]byte("hi\r\n")); h != "45b983b" {
		t.Errorf("hash %q", h)
	}
	if FWHash("45b983b 2026-10-04") != "45b983b" || FWHash("dev") != "" || FWHash("") != "" {
		t.Error("FWHash")
	}
}
