// Command boards finds the Pulse boards (the Arduino sign and the ESP32
// zone lights) on USB and on the network, and talks to them directly. It
// is the engine behind scripts/boards.sh; run it from the repo root.
//
//	boards status [-server URL] [-json]   what is plugged in / reachable, firmware up to date or not
//	boards scan [-plain|-json]            identify every board on USB (asks each port "S")
//	boards fwid <sketch.ino>              build id for a sketch: "<hash> <date>"
//	boards level -port P -level red [-zone A]
//	boards wifi -port P -ssid NAME        save a Wi-Fi network on a board (password on stdin)
//	boards zone -port P -zone A           teach a zone light its letter
//	boards env [-wifi] [-write FILE]      the SIGN_URL line for the boards found; -write updates only that line
//
// The running server holds the ports it drives: stop it first (Ctrl-C)
// before scan, flash or wifi, or use "status", which asks the server.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/sign"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "status":
		err = status(args)
	case "scan":
		err = scan(args)
	case "fwid":
		err = fwid(args)
	case "level":
		err = level(args)
	case "zone":
		err = zone(args)
	case "wifi":
		err = wifi(args)
	case "beacon":
		err = beacon(args)
	case "env":
		err = env(args)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "boards:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: boards status|scan|fwid|level|zone|wifi|beacon|env [flags]   (see scripts/boards.sh)`)
	os.Exit(2)
}

// ---- helpers ----

// sketchFor says which sketch a USB board runs: from what it said, else
// from its USB vendor ID or device name (old firmware that doesn't answer).
func sketchFor(b sign.USBBoard) string {
	if b.Pulse != nil {
		return b.Pulse.Kind
	}
	n := strings.ToLower(b.Port)
	switch {
	case b.VID == "2341", strings.Contains(n, "usbmodem"), strings.Contains(n, "ttyacm"):
		return "sign"
	case b.VID == "10C4", b.VID == "1A86", b.VID == "303A", strings.Contains(n, "usbserial"), strings.Contains(n, "slab"), strings.Contains(n, "ttyusb"), strings.Contains(n, "wchusbserial"):
		return "zone-light"
	}
	return ""
}

func fwNote(kind, fw string) string {
	want := sign.WantFW(kind)
	switch {
	case fw == "":
		return "old (no build id)"
	case want == "":
		return fw
	case sign.FWHash(fw) == want:
		return fw + " (up to date)"
	case fw == "dev":
		return "dev (built by hand)"
	}
	return fw + " (OUT OF DATE)"
}

func wifiNote(p *sign.Pulse) string {
	if p == nil || p.WiFi == nil {
		return "?"
	}
	if p.Mode == "beacon" {
		return "off: Bluetooth beacon " + dash(p.Name) + " (scripts/boards.sh beacon off)"
	}
	if p.Beacon && p.BLEErr != "" {
		note := "beacon failed (" + p.BLEErr + "), "
		if *p.WiFi {
			return note + fmt.Sprintf("%s %s", p.SSID, p.IP)
		}
		return note + "no Wi-Fi"
	}
	if *p.WiFi {
		return fmt.Sprintf("%s %s", p.SSID, p.IP)
	}
	if p.SSID != "" {
		return "no (trying " + p.SSID + ")"
	}
	return "no (none set)"
}

func peersNote(ps []sign.Peer) string {
	var out []string
	for _, p := range ps {
		out = append(out, fmt.Sprintf("%s %.1f m", p.Name, p.Dist))
	}
	if len(out) == 0 {
		return "-"
	}
	return strings.Join(out, ", ")
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func scanUSB() ([]sign.USBBoard, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return sign.ScanUSB(ctx, 4*time.Second)
}

// ---- scan ----

func scan(args []string) error {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	plain := fs.Bool("plain", false, "tab-separated lines: port, sketch, zone, mac, fw, answered (for scripts)")
	asJSON := fs.Bool("json", false, "JSON")
	fs.Parse(args)
	bs, err := scanUSB()
	if err != nil {
		return err
	}
	switch {
	case *plain:
		for _, b := range bs {
			z, mac, fw, ok := "", "", "", "no"
			if b.Pulse != nil {
				z, mac, fw, ok = b.Pulse.Zone, b.Pulse.MAC, b.Pulse.FW, "yes"
			}
			if b.Err != "" && b.Pulse == nil && strings.HasPrefix(b.Err, "busy") {
				ok = "busy"
			}
			fmt.Printf("%s\t%s\t%s\t%s\t%s\t%s\n", b.Port, dash(sketchFor(b)), dash(z), dash(mac), dash(fw), ok)
		}
	case *asJSON:
		type row struct {
			sign.USBBoard
			Sketch string `json:"sketch"`
		}
		var out []row
		for _, b := range bs {
			out = append(out, row{b, sketchFor(b)})
		}
		return json.NewEncoder(os.Stdout).Encode(out)
	default:
		printUSB(bs)
	}
	return nil
}

func printUSB(bs []sign.USBBoard) {
	if len(bs) == 0 {
		fmt.Println("No boards on USB. Use a data cable (not charge-only); on a Mac, an ESP32 may need the Silicon Labs CP210x driver (docs/TABLE-DEMO.md).")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PORT\tBOARD\tZONE\tFIRMWARE\tWI-FI\tHEARS")
	for _, b := range bs {
		if b.Pulse == nil {
			why := b.Err
			if why == "" || strings.HasPrefix(why, "no answer") {
				why = "no answer: old firmware or not a Pulse board (flash it: scripts/boards.sh flash)"
			}
			fmt.Fprintf(w, "%s\t%s\t-\t-\t-\t%s\n", b.Port, dash(sketchFor(b))+"?", why)
			continue
		}
		p := b.Pulse
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", b.Port, b.Label(), dash(p.Zone), fwNote(p.Kind, p.FW), wifiNote(p), peersNote(p.Peers))
	}
	w.Flush()
}

// ---- status ----

func status(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	server := fs.String("server", "http://localhost:"+portOf(os.Getenv("PULSE_ADDR")), "the running Pulse server")
	asJSON := fs.Bool("json", false, "JSON")
	fs.Parse(args)
	c := &http.Client{Timeout: 3 * time.Second}
	if resp, err := c.Get(strings.TrimRight(*server, "/") + "/api/hardware"); err == nil {
		defer resp.Body.Close()
		var hw []protocol.Hardware
		if resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(&hw) == nil {
			if *asJSON {
				return json.NewEncoder(os.Stdout).Encode(hw)
			}
			fmt.Printf("Pulse is running (%s) and drives the boards; this is what it sees:\n\n", *server)
			printServer(hw)
			return nil
		}
	}
	fmt.Println("Pulse isn't running here; asking the boards directly.")
	fmt.Println()
	bs, err := scanUSB()
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(bs)
	}
	fmt.Println("On USB:")
	printUSB(bs)
	// Wi-Fi boards named in SIGN_URL (environment or .env).
	spec := os.Getenv("SIGN_URL")
	if spec == "" {
		spec = dotEnvValue(".env", "SIGN_URL")
	}
	var urls []string
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if z, u, ok := strings.Cut(part, "="); ok && !strings.Contains(z, "/") {
			part = strings.TrimSpace(u)
		}
		if part != "" && !strings.HasPrefix(strings.ToLower(part), "serial:") {
			urls = append(urls, part)
		}
	}
	if len(urls) > 0 {
		fmt.Println()
		fmt.Println("On the network (from SIGN_URL):")
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		st := sign.New(strings.Join(urls, ",")).Probe(ctx)
		cancel()
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "URL\tBOARD\tZONE\tFIRMWARE\tWI-FI\tHEARS")
		for _, s := range st {
			if s.Pulse == nil {
				fmt.Fprintf(w, "%s\t%s\t-\t-\t-\t-\n", s.URL, map[bool]string{true: "online (old firmware)", false: "not reachable"}[s.Online])
				continue
			}
			p := s.Pulse
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", s.URL, boardName(p), dash(p.Zone), fwNote(p.Kind, p.FW), wifiNote(p), peersNote(p.Peers))
		}
		w.Flush()
	}
	return nil
}

func boardName(p *sign.Pulse) string {
	if p.Kind == "sign" {
		return "sign"
	}
	return dash(p.Name)
}

func printServer(hw []protocol.Hardware) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "BOARD\tLINK\tWHERE\tSHOWS\tFIRMWARE\tWI-FI\tHEARS\tNEXT STEP")
	for _, h := range hw {
		if h.Kind == "laptop" {
			continue
		}
		link, where := "offline", h.URL
		if h.Online {
			link = h.Link
			if link == "" { // a server from before USB/Wi-Fi was reported
				link = map[bool]string{true: "usb", false: "wifi"}[strings.HasPrefix(strings.ToLower(h.URL), "serial:")]
			}
			if h.Port != "" {
				where = h.Port
			}
		}
		fw := "-"
		if h.Online {
			fw = dash(h.FW)
			if h.FWOld {
				fw += " (OUT OF DATE)"
			} else if h.FWWant != "" {
				fw += " (up to date)"
			}
		}
		wifi := "-"
		if h.WiFi != nil {
			if *h.WiFi {
				wifi = h.SSID + " " + h.IP
			} else {
				wifi = "no"
			}
		}
		var peers []string
		for _, p := range h.Peers {
			peers = append(peers, fmt.Sprintf("%s %.1f m", p.Name, p.Dist))
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", h.Name, link, where, dash(h.Level), fw, wifi, dash(strings.Join(peers, ", ")), nextStep(h))
	}
	w.Flush()
}

// nextStep is the one thing to do about a board (the dashboard's readiness
// panel says the same).
func nextStep(h protocol.Hardware) string {
	switch {
	case !h.Online && strings.HasPrefix(strings.ToLower(h.URL), "serial:"):
		if strings.Contains(h.Error, "busy") {
			return "close the program holding the port (Arduino IDE Serial Monitor), then wait 2 s"
		}
		return "plug it in (data cable); Pulse finds it within 2 s"
	case !h.Online:
		return "same Wi-Fi as this laptop? else plug it in by USB and use serial:auto"
	case h.FWOld:
		return "reflash: stop Pulse, scripts/boards.sh flash"
	}
	return "ready"
}

func portOf(addr string) string {
	if addr == "" {
		return "8080"
	}
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i+1:]
	}
	return addr
}

// ---- fwid ----

func fwid(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: boards fwid <sketch.ino>")
	}
	id, err := sign.BuildID(args[0])
	if err != nil {
		return err
	}
	fmt.Println(id)
	return nil
}

// ---- level / zone ----

func open(port string) (*sign.USBConn, *sign.Pulse, error) {
	u, err := sign.OpenUSB(port)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %v", port, err)
	}
	p, err := u.Status(context.Background(), 4*time.Second)
	if err != nil {
		u.Close()
		return nil, nil, fmt.Errorf("%s doesn't answer S (%v): old firmware? flash it with scripts/boards.sh flash", port, err)
	}
	return u, p, nil
}

func level(args []string) error {
	fs := flag.NewFlagSet("level", flag.ExitOnError)
	port := fs.String("port", "", "serial port")
	lv := fs.String("level", "red", "calm | yellow | red")
	z := fs.String("zone", "", "zone label")
	fs.Parse(args)
	u, _, err := open(*port)
	if err != nil {
		return err
	}
	defer u.Close()
	line := "L " + *lv
	if *z != "" {
		line += " " + *z
	}
	if err := u.Send(line); err != nil {
		return err
	}
	time.Sleep(300 * time.Millisecond)
	p, err := u.Status(context.Background(), 3*time.Second)
	if err != nil {
		return err
	}
	fmt.Printf("%s shows %s (zone %s)\n", *port, p.Level, dash(p.Zone))
	want := *lv
	if want != "red" && want != "yellow" {
		want = "calm" // the firmware reads anything else as calm
	}
	if p.Level != want {
		return fmt.Errorf("%s reported %s, not %s", *port, p.Level, want)
	}
	return nil
}

func zone(args []string) error {
	fs := flag.NewFlagSet("zone", flag.ExitOnError)
	port := fs.String("port", "", "serial port of a zone light")
	z := fs.String("zone", "", "zone letter, e.g. A")
	fs.Parse(args)
	if *z == "" {
		return errors.New("want -zone")
	}
	u, p, err := open(*port)
	if err != nil {
		return err
	}
	defer u.Close()
	if p.Kind != "zone-light" {
		return fmt.Errorf("%s is a %s, not a zone light", *port, p.Kind)
	}
	if err := u.Send("L " + p.Level + " " + strings.ToUpper(*z)); err != nil {
		return err
	}
	time.Sleep(300 * time.Millisecond)
	p, err = u.Status(context.Background(), 3*time.Second)
	if err != nil {
		return err
	}
	fmt.Printf("%s is now %s (zone %s)\n", *port, p.Name, p.Zone)
	return nil
}

// ---- beacon ----

// beacon switches the sign's Bluetooth beacon mode (B 1 / B 0). The sign
// reboots and restarts its radio module, so the USB port drops for a few
// seconds; this reopens it and reports the mode it came back in.
func beacon(args []string) error {
	fs := flag.NewFlagSet("beacon", flag.ExitOnError)
	port := fs.String("port", "", "serial port of the sign")
	on := fs.Bool("on", false, "turn beacon mode on (default: off)")
	wait := fs.Duration("wait", 30*time.Second, "how long to wait for the sign to come back")
	fs.Parse(args)
	u, p, err := open(*port)
	if err != nil {
		return err
	}
	if p.Kind != "sign" {
		u.Close()
		return fmt.Errorf("%s is a %s, not the sign", *port, p.Kind)
	}
	if p.Radio == "" && p.Mode == "" {
		u.Close()
		return fmt.Errorf("%s: the sign's firmware predates beacon mode: flash it (scripts/boards.sh flash)", *port)
	}
	u.Drain()
	cmd := "B 0"
	if *on {
		cmd = "B 1"
	}
	if err := u.Send(cmd); err != nil {
		u.Close()
		return err
	}
	reply, _ := u.WaitLine(context.Background(), "beacon:", 3*time.Second)
	u.Close()
	if reply != "" {
		fmt.Printf("%s: %s\n", *port, strings.TrimPrefix(reply, "beacon: "))
	}
	want := "wifi"
	if *on {
		want = "beacon"
	}
	time.Sleep(2 * time.Second)
	deadline := time.Now().Add(*wait)
	var last *sign.Pulse
	for time.Now().Before(deadline) {
		if c, err := sign.OpenUSB(*port); err == nil {
			q, err := c.Status(context.Background(), 3*time.Second)
			c.Close()
			if err == nil {
				last = q
				// Beacon mode comes up only after the radio module restarts: wait for ble or a fallback.
				if q.Mode == want && (want == "wifi" || (q.BLE != nil && q.BLE.Beacon)) {
					adv := ""
					if q.BLE != nil && q.BLE.Beacon {
						adv = ", advertising " + q.Name
					}
					fmt.Printf("%s is in %s mode (radio firmware %s)%s\n", *port, q.Mode, q.Radio, adv)
					return nil
				}
				if *on && q.Mode == "wifi" && q.BLEErr != "" {
					return fmt.Errorf("%s: Bluetooth didn't start (%s); the sign fell back to Wi-Fi + USB", *port, q.BLEErr)
				}
			}
		}
		time.Sleep(time.Second)
	}
	if last != nil {
		return fmt.Errorf("%s answers but is in %q mode, not %s", *port, last.Mode, want)
	}
	return fmt.Errorf("%s didn't come back within %s: unplug and replug it; still nothing, double-tap its RESET button", *port, *wait)
}

// ---- wifi ----

func wifi(args []string) error {
	fs := flag.NewFlagSet("wifi", flag.ExitOnError)
	port := fs.String("port", "", "serial port")
	ssid := fs.String("ssid", "", "network name (2.4 GHz)")
	forget := fs.Bool("forget", false, "forget the saved network instead")
	wait := fs.Duration("wait", 30*time.Second, "how long to wait for the board to join")
	fs.Parse(args)
	if !*forget && (*ssid == "" || len(*ssid) > 32 || strings.ContainsAny(*ssid, "\t\r\n")) {
		return errors.New("want -ssid (up to 32 characters)")
	}
	pass := ""
	if !*forget {
		// The password comes on stdin so it never shows in a process list.
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		pass = strings.TrimRight(line, "\r\n")
		if len(pass) > 63 || strings.ContainsAny(pass, "\t") {
			return errors.New("password: up to 63 characters, no tabs")
		}
	}
	u, p, err := open(*port)
	if err != nil {
		return err
	}
	defer u.Close()
	u.Drain()
	cmd := "W -"
	if !*forget {
		cmd = "W " + *ssid + "\t" + pass
	}
	if err := u.Send(cmd); err != nil {
		return err
	}
	reply, ok := u.WaitLine(context.Background(), "wifi:", 3*time.Second)
	if !ok {
		return fmt.Errorf("%s (%s) didn't confirm: its firmware may predate the W command; flash it", *port, boardLabel(p))
	}
	fmt.Printf("%s (%s): %s\n", *port, boardLabel(p), strings.TrimPrefix(reply, "wifi: "))
	if *forget || strings.Contains(reply, "want W") {
		return nil
	}
	deadline := time.Now().Add(*wait)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		q, err := u.Status(context.Background(), 3*time.Second)
		if err == nil && q.WiFi != nil && *q.WiFi && q.SSID == *ssid {
			fmt.Printf("%s joined %s: %s\n", *port, q.SSID, q.IP)
			return nil
		}
	}
	return fmt.Errorf("%s saved %s but hasn't joined within %s: wrong password, 5 GHz only, or out of range (it keeps trying; USB works meanwhile)", *port, *ssid, *wait)
}

func boardLabel(p *sign.Pulse) string { return sign.USBBoard{Pulse: p}.Label() }

// ---- env ----

func env(args []string) error {
	fs := flag.NewFlagSet("env", flag.ExitOnError)
	useWifi := fs.Bool("wifi", false, "use each board's Wi-Fi address (http://ip) instead of USB, for a server that can't see USB (WSL)")
	write := fs.String("write", "", "update the SIGN_URL line in this file (e.g. .env); other lines are kept and never printed")
	fs.Parse(args)
	bs, err := scanUSB()
	if err != nil {
		return err
	}
	value, notes := signURL(bs, *useWifi)
	for _, n := range notes {
		fmt.Fprintln(os.Stderr, n)
	}
	if value == "" {
		return errors.New("no Pulse boards answered on USB: nothing to write (plug them in; stop Pulse first if it is running)")
	}
	fmt.Println("SIGN_URL=" + value)
	if *write != "" {
		if err := setEnvLine(*write, "SIGN_URL", value); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Updated SIGN_URL in %s (nothing else changed). Restart Pulse to use it.\n", *write)
	}
	return nil
}

// signURL builds SIGN_URL from the boards found: the sign, then the zone
// lights by zone; a light without a zone gets the next free letter (the
// server teaches it on connect).
func signURL(bs []sign.USBBoard, useWifi bool) (string, []string) {
	var parts, notes []string
	addr := func(p *sign.Pulse) string {
		if useWifi {
			if p.WiFi != nil && *p.WiFi && p.IP != "" {
				return "http://" + p.IP
			}
			notes = append(notes, fmt.Sprintf("%s has no Wi-Fi: using USB for it", boardLabel(p)))
		}
		return "serial:auto"
	}
	for _, b := range bs {
		if b.Pulse != nil && b.Pulse.Kind == "sign" {
			parts = append(parts, addr(b.Pulse))
			break
		}
	}
	used := map[string]bool{}
	type light struct {
		zone string
		p    *sign.Pulse
	}
	var lights []light
	var unzoned []*sign.Pulse
	for _, b := range bs {
		if b.Pulse == nil || b.Pulse.Kind != "zone-light" {
			if b.Pulse == nil {
				notes = append(notes, fmt.Sprintf("%s: no answer (old firmware?) — left out", b.Port))
			}
			continue
		}
		z := strings.ToUpper(b.Pulse.Zone)
		if z == "" || used[z] {
			unzoned = append(unzoned, b.Pulse)
			continue
		}
		used[z] = true
		lights = append(lights, light{z, b.Pulse})
	}
	for _, p := range unzoned {
		for c := 'A'; c <= 'Z'; c++ {
			if !used[string(c)] {
				used[string(c)] = true
				lights = append(lights, light{string(c), p})
				notes = append(notes, fmt.Sprintf("%s has no zone yet: it becomes %c", boardLabel(p), c))
				break
			}
		}
	}
	sort.Slice(lights, func(i, j int) bool { return lights[i].zone < lights[j].zone })
	for _, l := range lights {
		parts = append(parts, l.zone+"="+addr(l.p))
	}
	return strings.Join(parts, ","), notes
}

// setEnvLine replaces (or appends) KEY=value in an env file, keeping every
// other line exactly as it was.
func setEnvLine(path, key, value string) error {
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	text := strings.ReplaceAll(string(b), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	found := false
	for i, l := range lines {
		t := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "export "))
		if k, _, ok := strings.Cut(t, "="); ok && strings.TrimSpace(k) == key && !strings.HasPrefix(t, "#") {
			lines[i] = key + "=" + value
			found = true
		}
	}
	out := strings.Join(lines, "\n")
	if !found {
		out = strings.TrimRight(out, "\n")
		if out != "" {
			out += "\n"
		}
		out += key + "=" + value + "\n"
	}
	return os.WriteFile(path, []byte(out), 0o600)
}

// dotEnvValue reads one key from an env file (nothing else is kept).
func dotEnvValue(path, key string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		t := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(sc.Text()), "export "))
		if k, v, ok := strings.Cut(t, "="); ok && strings.TrimSpace(k) == key {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}
