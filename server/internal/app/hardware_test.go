package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/sign"
)

func TestLightLevels(t *testing.T) {
	a := &App{areas: []protocol.Area{
		{ID: "stage", Name: "Stage front", Light: "A"},
		{ID: "bar", Name: "Bar", Light: "A"},
		{ID: "gate", Name: "Gate"},
	}}
	levels := map[string]string{"stage": "yellow", "bar": "red", "gate": "red", "rest": "calm"}
	a.lightLevels(levels, []string{"A", "B"})
	if levels["A"] != "red" {
		t.Errorf("light A = %q, want the worst of its areas (red)", levels["A"])
	}
	if levels["B"] != "calm" {
		t.Errorf("light B = %q, want calm (no area assigned)", levels["B"])
	}
	if a.lightFor("stage") != "A" || a.lightFor("gate") != "" {
		t.Error("lightFor")
	}
}

func TestAreaLightValidation(t *testing.T) {
	cfg := New(Options{}).liveConfig()
	sq := []protocol.Point{{1, 1}, {3, 1}, {3, 3}}
	got, err := validAreas([]protocol.Area{{ID: "a1", Poly: sq, Light: " b "}}, cfg)
	if err != nil || got[0].Light != "B" {
		t.Fatalf("got %+v, %v; want light B", got, err)
	}
	if _, err := validAreas([]protocol.Area{{ID: "a1", Poly: sq, Light: "not a light!"}}, cfg); err == nil {
		t.Fatal("bad light accepted")
	}
}

// One missed check must not flip a board to offline; a long silence must.
func TestHardwareGrace(t *testing.T) {
	prev := []protocol.Hardware{{URL: "http://sign", Online: true, LastSeen: 1000, Level: "red"}}
	miss := []sign.Status{{URL: "http://sign", Err: "not reachable"}}
	hw := hardwareList(miss, prev, nil, 1000+HardwareGraceMs-1)
	if !hw[0].Online || hw[0].Level != "red" || hw[0].LastSeen != 1000 {
		t.Fatalf("within grace: %+v", hw[0])
	}
	hw = hardwareList(miss, prev, nil, 1000+HardwareGraceMs+1)
	if hw[0].Online || hw[0].Error == "" {
		t.Fatalf("after grace: %+v", hw[0])
	}
}

func TestHardwareProbe(t *testing.T) {
	light := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"kind":"zone-light","level":"calm","rssi":-55,"uptime":42,"ble":{"devices":24,"near":5,"scans":7,"age":1}}`))
	}))
	defer light.Close()
	oldSign := httptest.NewServer(http.NotFoundHandler()) // firmware without /pulse
	defer oldSign.Close()
	c := sign.New(oldSign.URL + ",A=" + light.URL + ",B=http://127.0.0.1:1")
	hw := hardwareList(c.Probe(context.Background()), nil, []protocol.Area{{ID: "s", Name: "Stage front", Light: "A"}}, 1000)
	if len(hw) != 3 {
		t.Fatalf("got %d boards", len(hw))
	}
	byName := map[string]protocol.Hardware{}
	for _, h := range hw {
		byName[h.Name] = h
	}
	if s := byName["Sign"]; !s.Online || s.LastSeen != 1000 {
		t.Errorf("old sign should count as online: %+v", s)
	}
	if l := byName["Zone light A"]; !l.Online || l.RSSI != -55 || l.BLE == nil || l.BLE.Devices != 24 || len(l.Areas) != 1 {
		t.Errorf("zone light A: %+v", l)
	}
	if b := byName["Zone light B"]; b.Online || b.Error == "" {
		t.Errorf("unreachable light should be offline with an error: %+v", b)
	}
}
