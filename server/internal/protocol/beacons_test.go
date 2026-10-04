package protocol

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestValidBeacons(t *testing.T) {
	many := func(n int) []BeaconSeen {
		var out []BeaconSeen
		for i := 0; i < n; i++ {
			out = append(out, BeaconSeen{Name: fmt.Sprintf("PULSE-%d", i), RSSI: -60, N: 3})
		}
		return out
	}
	cases := []struct {
		name string
		seen []BeaconSeen
		ok   bool
	}{
		{"empty report", nil, true},
		{"one board", []BeaconSeen{{Name: "PULSE-A", RSSI: -61, N: 14}}, true},
		{"fractional rssi, no count", []BeaconSeen{{Name: "PULSE-7B54", RSSI: -61.4}}, true},
		{"limits", []BeaconSeen{{Name: "PULSE-A", RSSI: -110}, {Name: "PULSE-B", RSSI: -20}}, true},
		{"16 boards", many(16), true},
		{"17 boards", many(17), false},
		{"name of 32", []BeaconSeen{{Name: "PULSE-" + strings.Repeat("x", 26), RSSI: -60}}, true},
		{"name of 33", []BeaconSeen{{Name: "PULSE-" + strings.Repeat("x", 27), RSSI: -60}}, false},
		{"not a Pulse board", []BeaconSeen{{Name: "Dan's AirPods", RSSI: -60}}, false},
		{"prefix only", []BeaconSeen{{Name: "PULSE-", RSSI: -60}}, false},
		{"lower-case prefix", []BeaconSeen{{Name: "pulse-A", RSSI: -60}}, false},
		{"empty name", []BeaconSeen{{RSSI: -60}}, false},
		{"control character", []BeaconSeen{{Name: "PULSE-A\n", RSSI: -60}}, false},
		{"space", []BeaconSeen{{Name: "PULSE-A B", RSSI: -60}}, false},
		{"too weak", []BeaconSeen{{Name: "PULSE-A", RSSI: -111}}, false},
		{"too strong", []BeaconSeen{{Name: "PULSE-A", RSSI: -19}}, false},
		{"positive", []BeaconSeen{{Name: "PULSE-A", RSSI: 61}}, false},
		{"rssi missing (0)", []BeaconSeen{{Name: "PULSE-A"}}, false},
		{"NaN", []BeaconSeen{{Name: "PULSE-A", RSSI: math.NaN()}}, false},
		{"negative count", []BeaconSeen{{Name: "PULSE-A", RSSI: -60, N: -1}}, false},
		{"absurd count", []BeaconSeen{{Name: "PULSE-A", RSSI: -60, N: 1_000_000}}, false},
		{"duplicate", []BeaconSeen{{Name: "PULSE-A", RSSI: -60}, {Name: "PULSE-A", RSSI: -70}}, false},
		{"one bad entry spoils the report", []BeaconSeen{{Name: "PULSE-A", RSSI: -60}, {Name: "Kitchen TV", RSSI: -70}}, false},
	}
	for _, c := range cases {
		if err := ValidBeacons(c.seen); (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok = %v", c.name, err, c.ok)
		}
	}
}

func TestBeaconsWire(t *testing.T) {
	var m Beacons
	if err := json.Unmarshal([]byte(`{"type":"beacons","seen":[{"name":"PULSE-A","rssi":-61,"n":14}]}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.Type != TypeBeacons || len(m.Seen) != 1 || m.Seen[0] != (BeaconSeen{Name: "PULSE-A", RSSI: -61, N: 14}) {
		t.Fatalf("decoded %+v", m)
	}
}

// The model is the firmware's: TX_POWER_1M −64 dBm, PATH_LOSS_N 2.2.
func TestBeaconDistance(t *testing.T) {
	m := DefaultBeaconModel
	for _, c := range []struct{ rssi, want float64 }{
		{-64, 1},        // the calibration point
		{-86, 10},       // 22 dB down = one decade at n = 2.2
		{-42, 0.1},      // 22 dB up
		{-63, 0.9006},   // the firmware's example peer (rssi −63 → 0.9 m)
		{-70.62, 2.0},   // 10·2.2·log10(2) = 6.62 dB per doubling
		{-77.25, 4.001}, // two doublings
	} {
		if got := m.Distance(c.rssi); math.Abs(got-c.want) > 0.002*c.want+0.001 {
			t.Errorf("Distance(%g) = %.4f, want %.4f", c.rssi, got, c.want)
		}
	}
	// A denser crowd (higher exponent) makes the same signal mean "closer".
	if a, b := m.Distance(-80), (BeaconModel{TxPower1m: -64, PathLossN: 3.5}).Distance(-80); !(b < a) {
		t.Errorf("n = 3.5 gives %.2f m, n = 2.2 gives %.2f m", b, a)
	}
	for _, bad := range []BeaconModel{{}, {TxPower1m: -64, PathLossN: 0.5}, {TxPower1m: 10, PathLossN: 2}, {TxPower1m: math.NaN(), PathLossN: 2}} {
		if bad.Valid() == nil {
			t.Errorf("model %+v accepted", bad)
		}
	}
	if err := m.Valid(); err != nil {
		t.Error(err)
	}
}
