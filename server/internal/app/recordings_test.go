package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/store"
)

// expectations by label: a recording named "<label>-….jsonl" (or
// "sim-<label>.jsonl") must reach exactly / at most this level.
var expectations = []struct {
	label string
	level string
	exact bool // false: "at most"
}{
	{"wave", protocol.LevelRed, true},
	{"push", protocol.LevelRed, true},
	{"calm", protocol.LevelCalm, true},
	{"walk", protocol.LevelCalm, true},
	{"dance", protocol.LevelCalm, true},
	{"jump", protocol.LevelCalm, true},
	{"handle", protocol.LevelCalm, true},
	{"shove", protocol.LevelYellow, false},
}

// TestRecordings replays every labelled run in recordings/ through the same
// pipeline the Replay button uses and checks the outcome its label promises.
// Record real runs with the dashboard's "Record run" button and name them
// after what happened (e.g. "wave-push-end", "dance-jumping") to add cases.
func TestRecordings(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "recordings")
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if len(files) == 0 {
		t.Skip("no recordings")
	}
	for _, f := range files {
		name := strings.TrimPrefix(filepath.Base(f), "sim-")
		var want *struct {
			label string
			level string
			exact bool
		}
		for i, e := range expectations {
			if strings.HasPrefix(name, e.label) {
				want = &expectations[i]
				break
			}
		}
		if want == nil {
			continue // unlabelled run: nothing to assert
		}
		t.Run(filepath.Base(f), func(t *testing.T) {
			recs, err := store.ReadJSONL(f)
			if err != nil {
				t.Fatal(err)
			}
			got := replayMaxLevel(t, recs)
			if want.exact && got != want.level {
				t.Errorf("reached %s, label %q expects %s", got, want.label, want.level)
			}
			if !want.exact && levelRank[got] > levelRank[want.level] {
				t.Errorf("reached %s, label %q allows at most %s", got, want.label, want.level)
			}
		})
	}
}

func replayMaxLevel(t *testing.T, recs []store.Record) string {
	t.Helper()
	if len(recs) == 0 {
		t.Fatal("empty recording")
	}
	a := New(Options{Detect: detect.DefaultConfig()})
	rs := &replayState{recs: recs, recStart: recs[0].T, recEnd: recs[len(recs)-1].T,
		p: newPipeline(ReplayConfig(detect.DefaultConfig(), recs))}
	worst := protocol.LevelCalm
	for now := rs.recStart; now <= rs.recEnd+1000; now += 250 {
		a.feedReplay(rs, now)
		for _, z := range rs.p.step(now).Zones {
			if levelRank[z.Level] > levelRank[worst] {
				worst = z.Level
			}
		}
	}
	return worst
}

