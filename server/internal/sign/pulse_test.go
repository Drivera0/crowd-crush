package sign

import (
	"encoding/json"
	"testing"
)

// The sign reports "ble" as a boolean (beacon advertising), the zone lights as
// their crowd counter object; both must decode, or the sign looks offline.
func TestPulseBLEBoolOrObject(t *testing.T) {
	var s Pulse
	line := `{"kind":"sign","level":"red","zone":"B","rssi":0,"uptime":46,"wifi":false,"fw":"b2afbd6 2026-10-04","ssid":"","ip":"","radio":"0.6.0","mode":"beacon","beacon":true,"ble":true,"name":"PULSE-S","bleErr":""}`
	if err := json.Unmarshal([]byte(line), &s); err != nil {
		t.Fatal(err)
	}
	if s.BLE == nil || !s.BLE.Beacon || s.BLE.Devices != -1 || s.Mode != "beacon" || !s.Beacon || s.Radio != "0.6.0" || s.Name != "PULSE-S" {
		t.Fatalf("sign: %+v ble %+v", s, s.BLE)
	}
	var z Pulse
	if err := json.Unmarshal([]byte(`{"kind":"zone-light","ble":{"devices":24,"near":3,"scans":9,"age":1}}`), &z); err != nil {
		t.Fatal(err)
	}
	if z.BLE == nil || z.BLE.Devices != 24 || z.BLE.Near != 3 || z.BLE.Beacon {
		t.Fatalf("zone light ble %+v", z.BLE)
	}
	var w Pulse
	if err := json.Unmarshal([]byte(`{"kind":"sign","ble":false,"mode":"wifi","beacon":true,"bleErr":"BLE.begin failed"}`), &w); err != nil {
		t.Fatal(err)
	}
	if w.BLE == nil || w.BLE.Beacon || w.BLEErr != "BLE.begin failed" {
		t.Fatalf("fallback %+v", w)
	}
}
