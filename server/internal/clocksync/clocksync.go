// Package clocksync estimates each phone's clock offset NTP-style.
//
// For each ping: rtt = now - t0, offset = t1 - (t0 + rtt/2).
// Within a burst we keep the sample with the smallest RTT, since it has the
// least room for asymmetric delay. Corrected time = t - offset.
package clocksync

import "sync"

// BurstSize is how many pings make up one sync burst.
const BurstSize = 8

// Estimate returns rtt and offset for one ping/pong exchange.
// t0 = server send time, t1 = phone clock at reply, now = server receive time.
func Estimate(t0, t1, now int64) (rtt, offset int64) {
	rtt = now - t0
	offset = t1 - (t0 + rtt/2)
	return rtt, offset
}

// Sync tracks the offset of one phone. Safe for concurrent use.
type Sync struct {
	mu sync.Mutex

	// current best estimate
	synced bool
	offset int64
	rtt    int64

	// samples collected in the burst in progress
	burstRTT    int64
	burstOffset int64
	burstN      int

	lastRTT int64
	kept    int // slow bursts in a row that kept the older offset (EndBurst)
}

// Pong records one ping/pong exchange.
func (s *Sync) Pong(t0, t1, now int64) {
	rtt, off := Estimate(t0, t1, now)
	if rtt < 0 || rtt > 10_000 {
		return // nonsense or a reply from a long-gone ping
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastRTT = rtt
	if s.burstN == 0 || rtt < s.burstRTT {
		s.burstRTT, s.burstOffset = rtt, off
	}
	s.burstN++
	// Adopt an estimate right away on the first burst so a phone isn't stuck
	// in "connecting"; later bursts only take effect at EndBurst.
	if !s.synced || rtt < s.rtt {
		s.offset, s.rtt, s.synced = s.burstOffset, s.burstRTT, true
	}
}

// EndBurst commits the best sample of the burst and starts a new one.
func (s *Sync) EndBurst() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.burstN > 0 {
		// On a congested mobile link a whole burst can be slow, and an
		// offset is only good to ± RTT/2: a burst far worse than the one in
		// use (over twice its RTT + 50 ms) keeps the old offset, for up to
		// MaxKeptBursts bursts (5 minutes at one burst per 30 s) so a real
		// clock drift is still followed.
		if s.synced && s.burstRTT > 2*s.rtt+50 && s.kept < MaxKeptBursts {
			s.kept++
		} else {
			s.offset, s.rtt, s.synced = s.burstOffset, s.burstRTT, true
			s.kept = 0
		}
	}
	s.burstN = 0
}

// MaxKeptBursts: how many slow bursts in a row may keep an older, better offset.
const MaxKeptBursts = 10

// Result returns the current offset and RTT. ok is false until the first pong.
func (s *Sync) Result() (offset, rtt int64, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.offset, s.rtt, s.synced
}

// LastRTT is the RTT of the most recent pong (live network health).
func (s *Sync) LastRTT() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastRTT
}

// Correct converts a phone timestamp to server time.
func (s *Sync) Correct(t int64) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return t - s.offset
}
