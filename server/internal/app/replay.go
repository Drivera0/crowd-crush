package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/hub"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
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

// ReplayConfig sizes the venue for a recording: the size its meta record
// gives, else base's, grown if needed so every recorded position (or
// legacy grid cell) fits.
func ReplayConfig(base detect.Config, recs []store.Record) detect.Config {
	cfg := base
	for _, r := range recs {
		if r.K == store.KindMeta && r.W >= 1 && r.H >= 1 {
			cfg.VenueW, cfg.VenueH = r.W, r.H
			break
		}
	}
	w, h := cfg.VenueW, cfg.VenueH
	for _, r := range recs {
		var x, y float64
		switch {
		case r.HasPos():
			x, y = *r.X, *r.Y
		case r.K == store.KindHello || r.K == store.KindPos || r.K == store.KindM:
			x, y = cfg.LegacyPos(r.Row, r.Col)
		default:
			continue
		}
		w, h = math.Max(w, math.Ceil(x+1)), math.Max(h, math.Ceil(y+1))
	}
	cfg.VenueW, cfg.VenueH = w, h
	return cfg
}

// recPos is where a record puts its phone: its x/y, or the legacy mapping
// of its row/col (recordings from before free positions).
func recPos(cfg detect.Config, r store.Record) (x, y float64) {
	if r.HasPos() {
		x, y = *r.X, *r.Y
	} else {
		x, y = cfg.LegacyPos(r.Row, r.Col)
	}
	return cfg.Clamp(x, y)
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
	}
	a.mu.Lock()
	rs.p = newPipeline(ReplayConfig(a.liveConfig(), recs))
	a.applyZones(rs.p)
	a.mu.Unlock()
	// Feed the metadata (hellos, syncs) that precedes the first reading.
	rs.recStart = first - 1
	a.mu.Lock()
	a.sim = nil // a replay replaces a running simulation
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
	cfg := p.cfg()
	ensure := func(rec store.Record) *nodeMeta {
		m := p.meta[rec.ID]
		if m == nil {
			m = &nodeMeta{connected: true, synced: true}
			m.x, m.y = recPos(cfg, rec)
			p.meta[rec.ID] = m
			p.place(rec.ID, m)
		}
		return m
	}
	for r.idx < len(r.recs) && r.recs[r.idx].T <= pnow {
		rec := r.recs[r.idx]
		r.idx++
		switch rec.K {
		case store.KindHello:
			m := ensure(rec)
			m.x, m.y = recPos(cfg, rec)
			m.acc, m.outside = rec.Acc, rec.Out
			m.ua, m.connected, m.lastRecv = rec.UA, true, rec.T
			p.place(rec.ID, m)
		case store.KindPos:
			m := ensure(rec)
			m.x, m.y = recPos(cfg, rec)
			m.acc, m.outside = rec.Acc, rec.Out
			p.place(rec.ID, m)
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
			m.addSample(protocol.Sample{T: ct, AX: rec.AX, AY: rec.AY, AZ: rec.AZ, Rot: rec.Rot})
			p.det.Add(rec.ID, detect.Sample{T: ct, AX: rec.AX, AY: rec.AY, AZ: rec.AZ, Rot: rec.Rot, G: detect.Gravity(rec.G)})
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
	cfg := a.liveConfig()
	w.Write(store.Record{K: store.KindMeta, T: now, Label: label, W: cfg.VenueW, H: cfg.VenueH})
	// Phones already connected need a hello so the replay knows where they
	// are (venue metres only, never GPS coordinates). Simulated phones too.
	pipes := []*pipeline{a.live}
	if a.sim != nil {
		pipes = append(pipes, a.sim.p)
	}
	for _, p := range pipes {
		for id, m := range p.meta {
			if m.connected && !m.unplaced {
				w.Write(store.Record{K: store.KindHello, T: now, ID: id, X: store.F(r2(m.x)), Y: store.F(r2(m.y)),
					Acc: m.acc, Out: m.outside, UA: m.ua})
				if m.synced {
					w.Write(store.Record{K: store.KindSync, T: now, ID: id, RTT: m.rtt, Offset: m.offset})
				}
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
