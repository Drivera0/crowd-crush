package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Drivera0/crowd-crush/server/internal/sign"
)

func TestSetEnvLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(p, []byte("GEMINI_API_KEY=secret\r\n# SIGN_URL=old comment\r\nSIGN_URL=http://192.168.1.88\r\nPUBLIC_URL=https://x.tech\r\n"), 0o600)
	if err := setEnvLine(p, "SIGN_URL", "serial:auto,A=serial:auto"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	want := "GEMINI_API_KEY=secret\n# SIGN_URL=old comment\nSIGN_URL=serial:auto,A=serial:auto\nPUBLIC_URL=https://x.tech\n"
	if string(b) != want {
		t.Fatalf("got %q", b)
	}
	q := filepath.Join(t.TempDir(), ".env")
	if err := setEnvLine(q, "SIGN_URL", "serial:auto"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(q); string(b) != "SIGN_URL=serial:auto\n" {
		t.Fatalf("new file %q", b)
	}
}

func TestSignURL(t *testing.T) {
	yes, no := true, false
	bs := []sign.USBBoard{
		{Port: "COM9", Pulse: &sign.Pulse{Kind: "zone-light", Name: "PULSE-B", Zone: "B", WiFi: &yes, IP: "192.168.1.90"}},
		{Port: "COM12", Pulse: &sign.Pulse{Kind: "zone-light", Name: "PULSE-1A2B", WiFi: &no}},
		{Port: "COM7", Pulse: &sign.Pulse{Kind: "sign", WiFi: &yes, IP: "192.168.1.88"}},
		{Port: "COM3"},
	}
	if got, _ := signURL(bs, false); got != "serial:auto,A=serial:auto,B=serial:auto" {
		t.Errorf("usb: %q", got)
	}
	if got, _ := signURL(bs, true); got != "http://192.168.1.88,A=serial:auto,B=http://192.168.1.90" {
		t.Errorf("wifi: %q", got)
	}
}
