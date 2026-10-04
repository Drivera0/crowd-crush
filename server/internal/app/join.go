package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Drivera0/crowd-crush/server/internal/hub"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// Joining at the table: the URL in the QR code, testing it, and phones
// reporting why they couldn't join.
//
// The QR code's URL, first match wins:
//
//  1. settings: staff pasted a URL in the dashboard's QR window (PUT
//     /api/join, saved to data/join.json, no restart). Empty clears it.
//  2. env: PUBLIC_URL (or -public-url).
//  3. tunnel: a Cloudflare quick tunnel running on this machine
//     (`cloudflared tunnel --url http://localhost:8080`): its metrics
//     server answers GET /quicktunnel with {"hostname":"….trycloudflare.com"}
//     on localhost:20241–20245 (cloudflared's default metrics ports).
//     CLOUDFLARED_METRICS=host:port[,host:port] probes other addresses;
//     "off" turns detection off. A tunnel that leads to a different Pulse
//     server (seen once it has been checked) is not used.
//  4. request: the address the dashboard was opened on.

// Reachability of the join URL (GET /api/join).
const (
	ReachPublic = "public" // PUBLIC_URL, or a public hostname / IP: any phone can open it
	ReachLAN    = "lan"    // a private IP or LAN-only name: phones on the same network only
	ReachLocal  = "local"  // localhost / loopback: no phone can open it
)

// Where the join URL came from (JoinInfo.Source).
const (
	JoinFromSettings = "settings"
	JoinFromEnv      = "env"
	JoinFromTunnel   = "tunnel"
	JoinFromRequest  = "request"
)

const joinFile = "join.json"

// JoinURL is the URL phones should open given PUBLIC_URL (public; "" =
// none) and the dashboard's request, and how far it reaches. It doesn't
// look at staff settings or tunnels: App.JoinInfo does.
func JoinURL(public string, r *http.Request) protocol.JoinInfo {
	if u := strings.TrimRight(strings.TrimSpace(public), "/"); u != "" {
		return joinInfo(u, JoinFromEnv)
	}
	return joinInfo(requestBase(r), JoinFromRequest)
}

// requestBase is scheme://host of the request, as the tunnel forwarded it.
func requestBase(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = p
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = h
	}
	return scheme + "://" + host
}

// joinInfo describes base (scheme://host, no trailing slash).
func joinInfo(base, source string) protocol.JoinInfo {
	j := protocol.JoinInfo{URL: base + "/", Source: source}
	host := base
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		host = u.Host
		j.Secure = u.Scheme == "https"
	}
	j.Reachable = hostReach(host)
	j.Display = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://"), "/")
	var p []string
	switch j.Reachable {
	case ReachLocal:
		p = append(p, "This address only works on this computer: no phone can open it.")
	case ReachLAN:
		p = append(p, "Only phones on the same Wi-Fi as this computer can open this address.")
	}
	if !j.Secure {
		p = append(p, "It is plain http://, so phones won't give the page their motion sensors (they need https).")
	}
	if len(p) > 0 {
		p = append(p, "Start a tunnel (cloudflared tunnel --url http://localhost:8080) or paste the https link below.")
	}
	j.Problem = strings.Join(p, " ")
	return j
}

func splitHost(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

// hostReach classifies a Host header value (with or without a port).
func hostReach(host string) string {
	h := strings.ToLower(strings.Trim(splitHost(host), "[]"))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return ReachLocal
	}
	if ip := net.ParseIP(h); ip != nil {
		switch {
		case ip.IsLoopback() || ip.IsUnspecified():
			return ReachLocal
		case ip.IsPrivate() || ip.IsLinkLocalUnicast() || isCGNAT(ip):
			return ReachLAN
		}
		return ReachPublic
	}
	// A bare machine name or a LAN-only suffix only resolves on the local network.
	if !strings.Contains(h, ".") || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".lan") ||
		strings.HasSuffix(h, ".home") || strings.HasSuffix(h, ".internal") {
		return ReachLAN
	}
	return ReachPublic
}

// isCGNAT: 100.64.0.0/10 (carrier-grade NAT, Tailscale): not reachable from the internet.
func isCGNAT(ip net.IP) bool {
	v4 := ip.To4()
	return v4 != nil && v4[0] == 100 && v4[1]&0xc0 == 64
}

// errJoinURL: PUT /api/join with something that isn't a plain address.
var errJoinURL = errors.New("want {\"url\": \"https://name.example\"}: http(s), a host, no path or query (\"\" clears it)")

// normJoinURL turns what staff pasted into scheme://host ("" stays "").
// A bare host gets https://.
func normJoinURL(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || len(s) > 200 {
		return "", errJoinURL
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host), nil
}

// ---- state ----

type joinState struct {
	once     sync.Once
	mu       sync.Mutex
	instance string // random per process: tells this server from another one behind a URL
	override string // staff-set URL (scheme://host), "" = none

	tunnel      string    // detected quick tunnel (https://host), "" = none
	tunnelAt    time.Time // last metrics probe
	tunnelOther string    // a tunnel found to lead to another server (not used)
	checking    bool      // a tunnel check is running

	issues map[string]joinIssue // phones' join reports, by session id
}

type joinIssue struct {
	reason, browser string
	at              int64
}

const (
	tunnelProbeEvery = 10 * time.Second
	issueKeepMs      = 30 * 60 * 1000
	maxIssues        = 1000
)

func (a *App) joinInit() *joinState {
	j := &a.join
	j.once.Do(func() {
		b := make([]byte, 8)
		rand.Read(b)
		j.instance = hex.EncodeToString(b)
		j.issues = map[string]joinIssue{}
		var saved struct {
			URL string `json:"url"`
		}
		if a.load(joinFile, &saved) {
			if u, err := normJoinURL(saved.URL); err == nil {
				j.override = u
			}
		}
	})
	return j
}

// JoinInfo is GET /api/join: the URL the QR code points at (see the top
// of this file for the order) and what's wrong with it.
func (a *App) JoinInfo(r *http.Request) protocol.JoinInfo {
	j := a.joinInit()
	tunnel := a.quickTunnel()
	j.mu.Lock()
	override := j.override
	j.mu.Unlock()
	env := strings.TrimRight(strings.TrimSpace(a.opt.PublicURL), "/")
	var out protocol.JoinInfo
	switch {
	case override != "":
		out = joinInfo(override, JoinFromSettings)
	case env != "":
		out = joinInfo(env, JoinFromEnv)
	case tunnel != "":
		out = joinInfo(tunnel, JoinFromTunnel)
	default:
		out = joinInfo(requestBase(r), JoinFromRequest)
	}
	out.Override, out.Env, out.Tunnel = override, env, tunnel
	if _, port, err := net.SplitHostPort(r.Host); err == nil && port != "8080" {
		out.Problem = strings.Replace(out.Problem, "localhost:8080", "localhost:"+port, 1) // the tunnel goes to this server's port
	}
	return out
}

// SetJoinURL is PUT /api/join: staff's URL for the QR code ("" clears it).
func (a *App) SetJoinURL(raw string) error {
	u, err := normJoinURL(raw)
	if err != nil {
		return err
	}
	j := a.joinInit()
	j.mu.Lock()
	j.override = u
	j.mu.Unlock()
	if u == "" {
		log.Printf("join link: cleared, back to the default")
	} else {
		log.Printf("join link: set to %s", u)
	}
	return a.save(joinFile, map[string]string{"url": u})
}

// metricsAddrs are where a cloudflared metrics server may listen.
func metricsAddrs() []string {
	if v := strings.TrimSpace(os.Getenv("CLOUDFLARED_METRICS")); v != "" {
		if v == "off" || v == "0" {
			return nil
		}
		var out []string
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return []string{"localhost:20241", "localhost:20242", "localhost:20243", "localhost:20244", "localhost:20245"}
}

var tunnelHost = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// quickTunnel is the https URL of a running Cloudflare quick tunnel, ""
// if none. The metrics servers are asked at most every 10 s.
func (a *App) quickTunnel() string {
	j := a.joinInit()
	j.mu.Lock()
	if time.Since(j.tunnelAt) < tunnelProbeEvery {
		t := j.tunnel
		j.mu.Unlock()
		return t
	}
	j.tunnelAt = time.Now()
	j.mu.Unlock()

	found := probeQuickTunnel(metricsAddrs())
	j.mu.Lock()
	defer j.mu.Unlock()
	if found == j.tunnelOther && found != "" {
		found = "" // checked: it leads to another server
	}
	if found != "" && found != j.tunnel {
		log.Printf("join link: found a Cloudflare quick tunnel, %s", found)
		if !j.checking && checkTunnels {
			j.checking = true
			go a.checkTunnel(found)
		}
	}
	j.tunnel = found
	return found
}

// probeQuickTunnel asks every metrics address at once; the first hostname wins.
func probeQuickTunnel(addrs []string) string {
	if len(addrs) == 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	res := make(chan string, len(addrs))
	for _, ad := range addrs {
		go func(ad string) {
			req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+ad+"/quicktunnel", nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				res <- ""
				return
			}
			defer resp.Body.Close()
			var q struct {
				Hostname string `json:"hostname"`
			}
			if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&q) != nil {
				res <- ""
				return
			}
			h := strings.ToLower(strings.TrimSpace(q.Hostname))
			if !tunnelHost.MatchString(h) { // a named tunnel answers {"hostname":""}
				res <- ""
				return
			}
			res <- "https://" + h
		}(ad)
	}
	for range addrs {
		if h := <-res; h != "" {
			return h
		}
	}
	return ""
}

// checkTunnels: check a found tunnel in the background (off in tests: no network).
var checkTunnels = true

// checkTunnel fetches a newly found tunnel once: if another Pulse server
// answers, the tunnel isn't for this one (two servers on one laptop).
func (a *App) checkTunnel(u string) {
	t := a.TestJoinURL(context.Background(), u)
	j := a.joinInit()
	j.mu.Lock()
	defer j.mu.Unlock()
	j.checking = false
	if t.Pulse == "other" {
		log.Printf("join link: %s leads to another Pulse server; not using it", u)
		j.tunnelOther = u
		if j.tunnel == u {
			j.tunnel = ""
		}
	}
}

// ---- "Test this QR" ----

// joinClient fetches join URLs: a short timeout, the system's certificates.
var joinClient = &http.Client{Timeout: 6 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
	if len(via) >= 3 {
		return http.ErrUseLastResponse
	}
	return nil
}}

// TestJoinURL fetches <u>/api/join/echo from this server, as a phone
// would reach it, and says what answered. u = "" tests the current join URL.
func (a *App) TestJoinURL(ctx context.Context, u string) protocol.JoinTest {
	j := a.joinInit()
	base, err := normJoinURL(u)
	out := protocol.JoinTest{URL: base + "/"}
	if err != nil || base == "" {
		out.URL = u
		out.Message = "That isn't a web address. Paste something like https://pulse.example.tech."
		return out
	}
	info := joinInfo(base, JoinFromSettings)
	out.Secure, out.Reachable = info.Secure, info.Reachable
	t0 := time.Now()
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/api/join/echo", nil)
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := joinClient.Do(req)
	out.Ms = time.Since(t0).Milliseconds()
	if err != nil {
		out.Message = describeFetchErr(err, base)
		return out
	}
	defer resp.Body.Close()
	out.Status = resp.StatusCode
	var echo struct {
		Pulse string `json:"pulse"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&echo)
	switch {
	case echo.Pulse == j.instance:
		out.Pulse = "this"
	case echo.Pulse != "":
		out.Pulse = "other"
		out.Message = "Another Pulse server answered there (a different laptop, or an old tunnel pointing at another port). Point the tunnel at this server's port."
		return out
	default:
		out.Message = fmt.Sprintf("Something answered with HTTP %d, but not this Pulse server. %s", resp.StatusCode, statusHint(resp.StatusCode))
		return out
	}
	// The phone page itself.
	preq, _ := http.NewRequestWithContext(ctx, "GET", base+"/", nil)
	if presp, err := joinClient.Do(preq); err == nil {
		b, _ := io.ReadAll(io.LimitReader(presp.Body, 64<<10))
		presp.Body.Close()
		if presp.StatusCode != 200 || !strings.Contains(string(b), "<title>Pulse") {
			out.Message = fmt.Sprintf("The server answers, but the phone page doesn't load (HTTP %d). Build it: cd web && npm run build.", presp.StatusCode)
			return out
		}
	}
	out.OK = true
	switch {
	case !out.Secure:
		out.OK = false
		out.Message = "This server answers, but over plain http://: phones will load the page and get no motion sensors. Use an https tunnel."
	case out.Reachable == ReachLocal:
		out.OK = false
		out.Message = "This server answers, but only on this computer. Phones need the tunnel's https address."
	case out.Reachable == ReachLAN:
		out.Message = fmt.Sprintf("This server answers in %d ms, from this computer. Phones must be on the same Wi-Fi, and LAN addresses rarely have https: prefer the tunnel.", out.Ms)
	default:
		out.Message = fmt.Sprintf("Works: this server answered through %s in %d ms. Phones on mobile data can join.", info.Display, out.Ms)
	}
	return out
}

func statusHint(code int) string {
	switch {
	case code == 530 || code == 502 || code == 503 || code == 1033:
		return "The tunnel is up but can't reach this server: check the port in --url (and that pulse is running)."
	case code == 404:
		return "It's some other website, or an old server without this check."
	}
	return "Check the address."
}

func describeFetchErr(err error, base string) string {
	var dns *net.DNSError
	var op *net.OpError
	switch {
	case errors.As(err, &dns):
		return "That name doesn't resolve (yet). A new .tech domain can take hours; a quick tunnel's name works within a minute. Check for typos."
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "Client.Timeout"):
		return "No answer within 6 s. Is the tunnel running? Is this laptop online?"
	case strings.Contains(err.Error(), "certificate") || strings.Contains(err.Error(), "x509"):
		return "The https certificate isn't valid for this name; phones will show a warning and refuse sensors."
	case errors.As(err, &op):
		return "Nothing is listening there (connection refused). Is the tunnel or the server running?"
	}
	return "Couldn't fetch " + base + ": " + err.Error()
}

// ---- phones' join reports ----

var joinReasonLabel = map[string]string{
	protocol.JoinInApp:        "in-app browser",
	protocol.JoinInsecure:     "opened over http",
	protocol.JoinMotionDenied: "motion access denied",
	protocol.JoinNoMotion:     "no motion data",
	protocol.JoinNoSensor:     "no motion sensor",
	protocol.JoinPermError:    "permission prompt failed",
	protocol.JoinSocket:       "can't connect",
}

var browserOK = regexp.MustCompile(`^[A-Za-z0-9 ._()+/-]{0,40}$`)

// JoinReport is POST /api/join/report.
func (a *App) JoinReport(r protocol.JoinReport) error {
	if r.ID == "" || len(r.ID) > 64 {
		return errors.New("want {id, reason, browser}")
	}
	if _, ok := joinReasonLabel[r.Reason]; !ok && r.Reason != protocol.JoinOK {
		return errors.New("unknown reason")
	}
	if !browserOK.MatchString(r.Browser) {
		r.Browser = "other"
	}
	j := a.joinInit()
	now := hub.Now()
	j.mu.Lock()
	defer j.mu.Unlock()
	if r.Reason == protocol.JoinOK {
		delete(j.issues, r.ID)
		return nil
	}
	if _, had := j.issues[r.ID]; !had && len(j.issues) >= maxIssues {
		pruneIssues(j.issues, now, true)
	}
	j.issues[r.ID] = joinIssue{reason: r.Reason, browser: r.Browser, at: now}
	return nil
}

// pruneIssues drops reports older than issueKeepMs (and, if full, the oldest half).
func pruneIssues(m map[string]joinIssue, now int64, full bool) {
	for id, is := range m {
		if now-is.at > issueKeepMs {
			delete(m, id)
		}
	}
	if full && len(m) >= maxIssues {
		ids := make([]string, 0, len(m))
		for id := range m {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(x, y int) bool { return m[ids[x]].at < m[ids[y]].at })
		for _, id := range ids[:len(ids)/2] {
			delete(m, id)
		}
	}
}

// JoinStats is GET /api/join/stats.
func (a *App) JoinStats() protocol.JoinStats {
	now := hub.Now()
	out := protocol.JoinStats{Problems: []protocol.JoinProblem{}}
	streaming := map[string]bool{}
	a.mu.Lock()
	for id, m := range a.live.meta {
		if !m.connected {
			continue
		}
		out.Joined++
		if m.msgs > 0 && now-m.lastRecv < 5000 {
			out.Streaming++
			streaming[id] = true
		}
	}
	a.mu.Unlock()
	j := a.joinInit()
	j.mu.Lock()
	defer j.mu.Unlock()
	pruneIssues(j.issues, now, false)
	by := map[string]*protocol.JoinProblem{}
	for id, is := range j.issues {
		if streaming[id] {
			continue // it got going after all
		}
		p := by[is.reason]
		if p == nil {
			p = &protocol.JoinProblem{Reason: is.reason, Label: joinReasonLabel[is.reason], Browsers: []string{}}
			by[is.reason] = p
		}
		p.Count++
		if is.browser != "" && len(p.Browsers) < 5 && !contains(p.Browsers, is.browser) {
			p.Browsers = append(p.Browsers, is.browser)
		}
	}
	for _, p := range by {
		sort.Strings(p.Browsers)
		out.Problems = append(out.Problems, *p)
	}
	sort.Slice(out.Problems, func(x, y int) bool {
		if out.Problems[x].Count != out.Problems[y].Count {
			return out.Problems[x].Count > out.Problems[y].Count
		}
		return out.Problems[x].Reason < out.Problems[y].Reason
	})
	return out
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// ---- routes ----

func (a *App) joinRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/join", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, a.JoinInfo(r))
	})
	mux.HandleFunc("PUT /api/join", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			URL *string `json:"url"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req); err != nil || req.URL == nil {
			httpError(w, errJoinURL, http.StatusBadRequest)
			return
		}
		if err := a.SetJoinURL(*req.URL); err != nil {
			code := http.StatusBadRequest
			if !errors.Is(err, errJoinURL) {
				code = http.StatusInternalServerError
			}
			httpError(w, err, code)
			return
		}
		writeJSON(w, a.JoinInfo(r))
	})
	mux.HandleFunc("POST /api/join/test", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			URL string `json:"url"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req)
		u := req.URL
		if strings.TrimSpace(u) == "" {
			u = a.JoinInfo(r).URL
		}
		writeJSON(w, a.TestJoinURL(r.Context(), u))
	})
	// What TestJoinURL fetches: which server this is.
	mux.HandleFunc("GET /api/join/echo", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"pulse": a.joinInit().instance})
	})
	mux.HandleFunc("POST /api/join/report", func(w http.ResponseWriter, r *http.Request) {
		var rep protocol.JoinReport
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&rep); err != nil {
			httpError(w, errors.New("want {id, reason, browser}"), http.StatusBadRequest)
			return
		}
		if err := a.JoinReport(rep); err != nil {
			httpError(w, err, http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/join/stats", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, a.JoinStats())
	})
}
