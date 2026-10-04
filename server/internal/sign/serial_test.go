package sign

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBoard is a board on a fake serial port: it records the lines the
// server writes and answers "S" with a status line, like the firmware.
type fakeBoard struct {
	mu     sync.Mutex
	opens  []string // port names opened, in order
	lines  chan string
	toHost *io.PipeWriter // what the board prints
	silent bool           // old firmware: never answers "S"
	dtr    bool
	rts    bool
}

type fakePort struct {
	b      *fakeBoard
	r      *io.PipeReader
	closed chan struct{}
	once   sync.Once
}

// SetDTR and SetRTS record the line state; the R4 only talks with DTR on.
func (p *fakePort) SetDTR(on bool) error { p.b.mu.Lock(); p.b.dtr = on; p.b.mu.Unlock(); return nil }
func (p *fakePort) SetRTS(on bool) error { p.b.mu.Lock(); p.b.rts = on; p.b.mu.Unlock(); return nil }

func (p *fakePort) Read(buf []byte) (int, error) { return p.r.Read(buf) }

func (p *fakePort) Write(buf []byte) (int, error) {
	select {
	case <-p.closed:
		return 0, errors.New("port closed")
	default:
	}
	for _, l := range strings.SplitAfter(string(buf), "\n") {
		if l == "" {
			continue
		}
		l = strings.TrimSuffix(l, "\n")
		if l == "S" {
			p.b.mu.Lock()
			w, silent := p.b.toHost, p.b.silent
			p.b.mu.Unlock()
			if !silent {
				go io.WriteString(w, "level 2 zone B\n{\"kind\":\"sign\",\"level\":\"red\",\"zone\":\"B\",\"rssi\":0,\"uptime\":42}\n")
			}
			continue
		}
		p.b.lines <- l
	}
	return len(buf), nil
}

func (p *fakePort) Close() error {
	p.once.Do(func() { close(p.closed); p.r.Close() })
	return nil
}

func newFakeBoard() *fakeBoard { return &fakeBoard{lines: make(chan string, 20)} }

func (b *fakeBoard) open(name string) (Port, error) {
	r, w := io.Pipe()
	b.mu.Lock()
	b.opens = append(b.opens, name)
	b.toHost = w
	b.mu.Unlock()
	return &fakePort{b: b, r: r, closed: make(chan struct{})}, nil
}

// unplug makes the host side see the port die (read error).
func (b *fakeBoard) unplug() {
	b.mu.Lock()
	w := b.toHost
	b.mu.Unlock()
	w.CloseWithError(errors.New("device not configured"))
}

func (b *fakeBoard) next(t *testing.T) string {
	t.Helper()
	select {
	case l := <-b.lines:
		return l
	case <-time.After(2 * time.Second):
		t.Fatal("no line written to the serial port")
		return ""
	}
}

func useFakes(t *testing.T, b *fakeBoard, ports []PortInfo) {
	t.Helper()
	oldOpen, oldList, oldRetry, oldSettle := openPort, listPorts, SerialRetry, SerialSettle
	openPort = b.open
	listPorts = func() ([]PortInfo, error) { return ports, nil }
	SerialRetry = 20 * time.Millisecond
	SerialSettle = 10 * time.Millisecond
	t.Cleanup(func() { openPort, listPorts, SerialRetry, SerialSettle = oldOpen, oldList, oldRetry, oldSettle })
}

func TestSerialAutoWritesLevels(t *testing.T) {
	b := newFakeBoard()
	useFakes(t, b, []PortInfo{
		{Name: "/dev/cu.Bluetooth-Incoming-Port"},
		{Name: "/dev/tty.usbmodem1101", VID: "2341"},
		{Name: "/dev/cu.usbmodem1101", VID: "2341"},
	})
	c := New("serial:auto")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if got := b.next(t); got != "L calm" {
		t.Fatalf("check wrote %q", got)
	}
	c.Set("red", "B")
	c.Set("red", "B") // repeat: skipped
	if got := b.next(t); got != "L red B" {
		t.Fatalf("got %q", got)
	}
	select {
	case l := <-b.lines:
		t.Fatalf("repeat was sent: %q", l)
	case <-time.After(100 * time.Millisecond):
	}
	b.mu.Lock()
	dtr, rts := b.dtr, b.rts
	b.mu.Unlock()
	if !dtr || !rts {
		t.Fatalf("DTR %v RTS %v after open; the UNO R4 needs DTR on to talk", dtr, rts)
	}
	if b.opens[0] != "/dev/cu.usbmodem1101" {
		t.Fatalf("opened %v, want the cu.usbmodem port", b.opens)
	}
	if d := c.Describe(); !strings.Contains(d, "serial:auto → /dev/cu.usbmodem1101") {
		t.Errorf("describe %q", d)
	}
}

func TestSerialProbe(t *testing.T) {
	b := newFakeBoard()
	useFakes(t, b, nil)
	c := New("A=serial:COM7")
	if z := c.Zones(); len(z) != 1 || z[0] != "A" {
		t.Fatalf("zones %v", z)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c.targets[0].ser.waitOpen(ctx)
	st := c.Probe(ctx)
	if len(st) != 1 || !st[0].Online || st[0].Port != "COM7" || st[0].URL != "serial:COM7" || st[0].Zone != "A" {
		t.Fatalf("status %+v", st)
	}
	if st[0].Pulse == nil || st[0].Pulse.Kind != "sign" || st[0].Pulse.Level != "red" || st[0].Pulse.Uptime != 42 {
		t.Fatalf("pulse %+v", st[0].Pulse)
	}

	// Old firmware that doesn't answer "S" is still online.
	b.mu.Lock()
	b.silent = true
	b.mu.Unlock()
	st = c.Probe(ctx)
	if !st[0].Online || st[0].Pulse != nil {
		t.Fatalf("silent board: %+v", st[0])
	}
}

func TestSerialReconnectResends(t *testing.T) {
	b := newFakeBoard()
	useFakes(t, b, nil)
	c := New("serial:/dev/ttyACM0")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c.targets[0].ser.waitOpen(ctx)
	c.Set("yellow", "A")
	if got := b.next(t); got != "L yellow A" {
		t.Fatalf("got %q", got)
	}
	b.unplug()
	// Reopened and the current state re-sent without a new update.
	if got := b.next(t); got != "L yellow A" {
		t.Fatalf("after reconnect got %q", got)
	}
	b.mu.Lock()
	n := len(b.opens)
	b.mu.Unlock()
	if n < 2 {
		t.Fatalf("port opened %d times", n)
	}
}

func TestSerialNoBoard(t *testing.T) {
	b := newFakeBoard()
	useFakes(t, b, []PortInfo{{Name: "COM3", VID: "8087"}}) // some other USB device
	c := New("serial:auto")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := c.Check(ctx); err == nil || !strings.Contains(err.Error(), "no Arduino") {
		t.Fatalf("check with no board: %v", err)
	}
	st := c.Probe(context.Background())
	if st[0].Online || !strings.Contains(st[0].Err, "no Arduino") {
		t.Fatalf("status %+v", st[0])
	}
	if len(b.opens) != 0 {
		t.Fatalf("opened %v", b.opens)
	}
}

func TestPickAuto(t *testing.T) {
	cases := []struct {
		ports []PortInfo
		want  string
	}{
		{[]PortInfo{{Name: "COM3", VID: "8087"}, {Name: "COM9", VID: "10C4"}}, "COM9"},
		{[]PortInfo{{Name: "/dev/ttyUSB0", VID: "10C4"}, {Name: "/dev/ttyACM0", VID: "2341"}}, "/dev/ttyACM0"},
		{[]PortInfo{{Name: "/dev/tty.usbmodem1"}, {Name: "/dev/cu.usbmodem1"}}, "/dev/cu.usbmodem1"}, // fallback listing, no VIDs
		{[]PortInfo{{Name: "COM3", VID: "8087"}}, ""},
	}
	for _, c := range cases {
		got, _ := pickAuto(c.ports)
		if got != c.want {
			t.Errorf("pickAuto(%v) = %q, want %q", c.ports, got, c.want)
		}
	}
}

// The firmware's line parser is mirrored here only by format: one line per
// state, zone last and optional.
func TestLevelLine(t *testing.T) {
	if l := levelLine(state{"red", "B"}); l != "L red B\n" {
		t.Errorf("%q", l)
	}
	if l := levelLine(state{"calm", ""}); l != "L calm\n" {
		t.Errorf("%q", l)
	}
	sc := bufio.NewScanner(strings.NewReader(levelLine(state{"yellow", "rest"})))
	sc.Scan()
	if f := strings.Fields(sc.Text()); len(f) != 3 || f[0] != "L" || f[2] != "rest" {
		t.Errorf("fields %v", f)
	}
}
