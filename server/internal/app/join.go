package app

import (
	"net"
	"net/http"
	"strings"

	"github.com/Drivera0/crowd-crush/server/internal/protocol"
)

// Reachability of the join URL (GET /api/join).
const (
	ReachPublic = "public" // PUBLIC_URL, or a public hostname / IP: any phone can open it
	ReachLAN    = "lan"    // a private IP or LAN-only name: phones on the same network only
	ReachLocal  = "local"  // localhost / loopback: no phone can open it
)

// JoinURL is the URL phones should open, as GET /api/phone-url and the QR
// code give it (public, if set, else the dashboard's own host), and how far
// it reaches.
func JoinURL(public string, r *http.Request) protocol.JoinInfo {
	if u := strings.TrimRight(strings.TrimSpace(public), "/"); u != "" {
		return protocol.JoinInfo{URL: u + "/", Reachable: ReachPublic}
	}
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
	return protocol.JoinInfo{URL: scheme + "://" + host + "/", Reachable: hostReach(host)}
}

// hostReach classifies a Host header value (with or without a port).
func hostReach(host string) string {
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	h = strings.ToLower(strings.Trim(h, "[]"))
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
