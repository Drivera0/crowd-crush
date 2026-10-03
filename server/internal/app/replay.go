package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/hub"
	"github.com/Drivera0/crowd-crush/server/internal/store"
)

// LoadRecording reads a recording by name: a JSONL file under the
// recordings dir, or "db:<label>" for a run stored in Tiger Data.
func (a *App) LoadRecording(ctx context.Context, name string) ([]store.Record, error) {
	if label, ok := strings.CutPrefix(name, "db:"); ok {
		if a.opt.Tiger == nil {
			return nil, errors.New("Tiger Data is not connected")
		}
		return a.opt.Tiger.LoadRun(ctx, label)
	}
	path, err := store.SafeJoin(a.opt.RecordingsDir, name)
	if err != nil {
		return nil, err
	}
	return store.ReadJSONL(path)
}

// ReplayConfig sizes the detector grid to fit a recording.
func ReplayConfig(base detect.Config, recs []store.Record) detect.Config {
	cfg := base
	for _, r := range recs {
		switch r.K {
		case store.KindMeta:
			if r.Rows > 0 && r.Cols > 0 {
				cfg.Rows, cfg.Cols = r.Rows, r.Cols
			}
		case store.KindHello, store.KindM:
			cfg.Rows = max(cfg.Rows, r.Row+1)
			cfg.Cols = max(cfg.Cols, r.Col+1)
		}
	}
	return cfg
}

// StartReplay plays a recording through a fresh detector at the given speed.
// The live pipeline keeps running underneath, so Live is instant.
func (a *App) StartReplay(ctx context.Context, name string, speed float64) error {
	recs, err := a.LoadRecording(ctx, name)
	if err != nil {
		return err
	}
	var first, last int64
	n := 0
	for _, r := range recs {
		if r.K != store.KindM {
			continue
		}
		if n == 0 {
			first = r.T
		}
		last = r.T
		n++
	}
	if n == 0 {
		return fmt.Errorf("%s has no readings", name)
	}
	if speed <= 0 || speed > 10 {
		speed = 1
	}
	rs := &replayState{
		name:      filepath.Base(name),
		recs:      recs,
		recStart:  first,
		recEnd:    last,
		wallStart: hub.Now(),
		speed:     speed,
		p:         newPipeline(ReplayConfig(a.opt.Detect, recs)),
	}
	// Feed the metadata (hellos, syncs) that precedes the first reading.
	rs.recStart = first - 1
	a.mu.Lock()
	a.replay = rs
	a.mu.Unlock()
	log.Printf("replaying %s: %d readings, %s", name, n, time.Duration(last-first)*time.Millisecond)
	return nil
}

// StopReplay returns to live data.
func (a *App) StopReplay() {
	a.mu.Lock()
	a.replay = nil
	a.mu.Unlock()
}

// feedReplay pushes every record up to pnow into the replay pipeline.
// Caller holds mu.
func (a *App) feedReplay(r *replayState, pnow int64) {
	p := r.p
	ensure := func(rec store.Record) *nodeMeta {
		m := p.meta[rec.ID]
		if m == nil {
			m = &nodeMeta{row: rec.Row, col: rec.Col, connected: true, synced: true}
			p.meta[rec.ID] = m
			p.det.SetPhone(rec.ID, rec.Row, rec.Col)
		}
		return m
	}
	for r.idx < len(r.recs) && r.recs[r.idx].T <= pnow {
		rec := r.recs[r.idx]
		r.idx++
		switch rec.K {
		case store.KindHello:
			m := ensure(rec)
			m.row, m.col, m.ua, m.connected, m.lastRecv = rec.Row, rec.Col, rec.UA, true, rec.T
			p.det.SetPhone(rec.ID, rec.Row, rec.Col)
		case store.KindSync:
			if m := p.meta[rec.ID]; m != nil {
				m.rtt, m.offset = rec.RTT, rec.Offset
			}
		case store.KindM:
			m := ensure(rec)
			m.lastRecv, m.connected = rec.T, true
			ct := rec.CT
			if ct == 0 {
				ct = rec.T
			}
			p.det.Add(rec.ID, detect.Sample{T: ct, AX: rec.AX, AY: rec.AY, AZ: rec.AZ, Rot: rec.Rot})
		case store.KindBye:
			if m := p.meta[rec.ID]; m != nil {
				m.connected, m.goneAt = false, rec.T
			}
		}
	}
}

// ---- labelled runs ----

// StartRecording writes every live event to recordings/<label>-<time>.jsonl.
func (a *App) StartRecording(label string) (string, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		label = "run"
	}
	start := time.Now()
	w, err := store.CreateJSONL(filepath.Join(a.opt.RecordingsDir, store.RunFileName(label, start)))
	if err != nil {
		return "", err
	}
	now := start.UnixMilli()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.rec != nil {
		w.Close()
		return "", fmt.Errorf("already recording %q", a.rec.label)
	}
	cfg := a.opt.Detect
	w.Write(store.Record{K: store.KindMeta, T: now, Label: label, Rows: cfg.Rows, Cols: cfg.Cols})
	// Phones already connected need a hello so the replay knows where they are.
	for id, m := range a.live.meta {
		if m.connected {
			w.Write(store.Record{K: store.KindHello, T: now, ID: id, Row: m.row, Col: m.col, UA: m.ua})
			if m.synced {
				w.Write(store.Record{K: store.KindSync, T: now, ID: id, RTT: m.rtt, Offset: m.offset})
			}
		}
	}
	a.rec = &recordingState{label: label, w: w, start: start}
	log.Printf("recording %q to %s", label, w.Path())
	return filepath.Base(w.Path()), nil
}

// StopRecording closes the run and stores its time range in Tiger Data.
func (a *App) StopRecording() (name string, records int, err error) {
	a.mu.Lock()
	rec := a.rec
	a.rec = nil
	a.mu.Unlock()
	if rec == nil {
		return "", 0, errors.New("not recording")
	}
	records = rec.w.Count()
	if err := rec.w.Close(); err != nil {
		return "", records, err
	}
	if a.opt.Tiger != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		// Readings are batched; give the last batch time to land.
		err := a.opt.Tiger.SaveRun(ctx, store.Run{Label: rec.label, Start: rec.start, End: time.Now().Add(store.FlushEvery)})
		if err != nil {
			log.Printf("store: save run: %v", err)
		}
	}
	log.Printf("recorded %q: %d records", rec.label, records)
	return filepath.Base(rec.w.Path()), records, nil
}
