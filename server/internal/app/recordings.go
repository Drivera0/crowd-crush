package app

import (
	"sync"

	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/store"
)

// Saved runs, described: what GET /api/recordings lists next to each file's
// name, so the console can say what a recording is before it is played.

// RecordingInfo is one saved run.
type RecordingInfo struct {
	store.RecordingInfo
	Label   string  `json:"label,omitempty"` // the label it was recorded under
	Seconds float64 `json:"seconds"`         // first reading to last
	Phones  int     `json:"phones"`          // phones that sent readings
	// The venue it plays in: the size recorded with it, or for a recording
	// from before free positions (Legacy: a row of phones on a grid) the
	// default venue it was made in.
	W      float64 `json:"w"`
	H      float64 `json:"h"`
	Legacy bool    `json:"legacy,omitempty"`
}

type recCacheKey struct {
	name        string
	size, mtime int64
}

var (
	recCacheMu sync.Mutex
	recCache   = map[recCacheKey]RecordingInfo{}
)

// Recordings lists the JSONL recordings with what is in each. A file is
// read once and remembered until it changes.
func (a *App) Recordings() []RecordingInfo {
	files, _ := store.ListJSONL(a.opt.RecordingsDir)
	base := a.Config()
	out := make([]RecordingInfo, 0, len(files))
	for _, f := range files {
		key := recCacheKey{f.Name, f.Size, f.MTime}
		recCacheMu.Lock()
		info, ok := recCache[key]
		recCacheMu.Unlock()
		if !ok {
			info = RecordingInfo{RecordingInfo: f}
			if path, err := store.SafeJoin(a.opt.RecordingsDir, f.Name); err == nil {
				if recs, err := store.ReadJSONL(path); err == nil {
					describeRecording(&info, recs)
				}
			}
			recCacheMu.Lock()
			if len(recCache) > 500 {
				recCache = map[recCacheKey]RecordingInfo{}
			}
			recCache[key] = info
			recCacheMu.Unlock()
		}
		if info.W == 0 { // recorded positions but no size: it plays in today's venue
			info.W, info.H = base.VenueW, base.VenueH
		}
		out = append(out, info)
	}
	return out
}

func describeRecording(info *RecordingInfo, recs []store.Record) {
	phones := map[string]bool{}
	var first, last int64
	for _, r := range recs {
		switch r.K {
		case store.KindMeta:
			if info.Label == "" {
				info.Label = r.Label
			}
		case store.KindM:
			if first == 0 {
				first = r.T
			}
			last = r.T
			phones[r.ID] = true
		}
	}
	info.Phones = len(phones)
	info.Seconds = float64(last-first) / 1000
	if legacyRecording(recs) {
		def := detect.DefaultConfig()
		cfg := ReplayConfig(def, recs)
		info.Legacy, info.W, info.H = true, cfg.VenueW, cfg.VenueH
		return
	}
	for _, r := range recs {
		if r.K == store.KindMeta && r.W >= 1 && r.H >= 1 {
			info.W, info.H = r.W, r.H
			return
		}
	}
}

// legacyRecording reports whether a recording is from before free
// positions: no venue size in its meta record and no x/y on any record
// (phones are placed from their grid row and column).
func legacyRecording(recs []store.Record) bool {
	for _, r := range recs {
		if r.HasPos() || (r.K == store.KindMeta && r.W >= 1 && r.H >= 1) {
			return false
		}
	}
	return len(recs) > 0
}
