package clocksync

import "testing"

func TestEstimate(t *testing.T) {
	tests := []struct {
		name           string
		t0, t1, now    int64
		wantRTT, wantO int64
	}{
		{"same clock", 1000, 1020, 1040, 40, 0},
		{"phone ahead 5s", 1000, 6020, 1040, 40, 5000},
		{"phone behind", 1000, 520, 1040, 40, -500},
	}
	for _, tt := range tests {
		rtt, off := Estimate(tt.t0, tt.t1, tt.now)
		if rtt != tt.wantRTT || off != tt.wantO {
			t.Errorf("%s: got rtt=%d off=%d, want %d %d", tt.name, rtt, off, tt.wantRTT, tt.wantO)
		}
	}
}

func TestBurstKeepsSmallestRTT(t *testing.T) {
	var s Sync
	if _, _, ok := s.Result(); ok {
		t.Fatal("synced before any pong")
	}
	// True offset 3000. Asymmetric delays distort high-RTT samples.
	s.Pong(0, 3000+80, 100)    // rtt 100, off 3030
	s.Pong(200, 3200+12, 230)  // rtt 30, off 2997
	s.Pong(400, 3400+150, 600) // rtt 200, off 3050
	s.EndBurst()
	off, rtt, ok := s.Result()
	if !ok || rtt != 30 || off != 2997 {
		t.Fatalf("got off=%d rtt=%d ok=%v", off, rtt, ok)
	}
	if got := s.Correct(10_000); got != 10_000-2997 {
		t.Fatalf("Correct = %d", got)
	}
	// A new burst with worse samples replaces the estimate (network changed).
	s.Pong(1000, 4000+60, 1100)
	s.EndBurst()
	if _, rtt, _ := s.Result(); rtt != 100 {
		t.Fatalf("new burst rtt = %d", rtt)
	}
}
