package store

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Reading is one motion summary headed for continuous storage.
type Reading struct {
	Time    time.Time // clock-corrected
	PhoneID string
	Zone    string
	X, Y    float64 // venue metres
	AX      float64
	AY      float64
	AZ      float64
	Rot     float64
	G       []float64 // gravity in the device frame ([gx, gy, gz]); nil when the reading carried none
}

// AlertRow is one alert headed for storage.
type AlertRow struct {
	Time  time.Time
	Zone  string
	Level string
	Score float64
	Brief string
}

// Sink is continuous storage. Implementations never block the caller for
// long and never fail loudly: storage must not stop detection.
type Sink interface {
	Reading(Reading)
	Alert(AlertRow)
	Close()
}

// Run is a labelled recording stored in the database.
type Run struct {
	Label string    `json:"label"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// ---- JSONL fallback sink ----

// JSONLSink writes everything to a JSONL file in dir (the auto-recorder).
type JSONLSink struct {
	w      *JSONLWriter
	stop   chan struct{}
	done   chan struct{}
	closed sync.Once
}

// NewJSONLSink starts a fallback recorder at dir/auto-<time>.jsonl.
func NewJSONLSink(dir string) (*JSONLSink, error) {
	w, err := CreateJSONL(filepath.Join(dir, "auto-"+time.Now().Format("20060102-150405")+".jsonl"))
	if err != nil {
		return nil, err
	}
	s := &JSONLSink{w: w, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				s.w.Flush()
			case <-s.stop:
				return
			}
		}
	}()
	return s, nil
}

func (s *JSONLSink) Reading(r Reading) {
	s.w.Write(Record{K: KindM, T: r.Time.UnixMilli(), CT: r.Time.UnixMilli(), ID: r.PhoneID, X: F(r.X), Y: F(r.Y), AX: r.AX, AY: r.AY, AZ: r.AZ, Rot: r.Rot, G: r.G})
}

func (s *JSONLSink) Alert(a AlertRow) {
	s.w.Write(Record{K: "alert", T: a.Time.UnixMilli(), Label: a.Zone + " " + a.Level + ": " + a.Brief})
}

func (s *JSONLSink) Close() {
	s.closed.Do(func() {
		close(s.stop)
		<-s.done
		s.w.Close()
	})
}

// Path is the file being written.
func (s *JSONLSink) Path() string { return s.w.Path() }

// ---- Tiger Data (TimescaleDB) sink ----

// Tiger batches readings into a hypertable with COPY every 500 ms. A failed
// batch goes to the JSONL fallback so nothing is lost.
type Tiger struct {
	pool     *pgxpool.Pool
	fallback Sink

	mu      sync.Mutex
	pending []Reading
	alerts  []AlertRow

	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// FlushEvery is the batch interval.
const FlushEvery = 500 * time.Millisecond

// OpenTiger connects and creates the schema. fallback receives batches the
// database rejects (may be nil).
func OpenTiger(ctx context.Context, url string, fallback Sink) (*Tiger, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	t := &Tiger{pool: pool, fallback: fallback, stop: make(chan struct{}), done: make(chan struct{})}
	if err := t.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	go t.loop()
	return t, nil
}

func (t *Tiger) migrate(ctx context.Context) error {
	must := []string{
		`CREATE TABLE IF NOT EXISTS readings (
			time     TIMESTAMPTZ NOT NULL,
			phone_id TEXT NOT NULL,
			zone     TEXT NOT NULL,
			row      INT NOT NULL DEFAULT 0,
			col      INT NOT NULL DEFAULT 0,
			ax DOUBLE PRECISION, ay DOUBLE PRECISION, az DOUBLE PRECISION, rot DOUBLE PRECISION)`,
		`CREATE TABLE IF NOT EXISTS alerts (
			time  TIMESTAMPTZ NOT NULL,
			zone  TEXT NOT NULL,
			level TEXT NOT NULL,
			score DOUBLE PRECISION,
			brief TEXT)`,
		`CREATE TABLE IF NOT EXISTS runs (
			label TEXT NOT NULL,
			start_time TIMESTAMPTZ NOT NULL,
			end_time   TIMESTAMPTZ NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS readings_time_idx ON readings (time DESC)`,
		// Venue metres (free positions); NULL on rows from the row/col days.
		`ALTER TABLE readings ADD COLUMN IF NOT EXISTS x REAL`,
		`ALTER TABLE readings ADD COLUMN IF NOT EXISTS y REAL`,
		// Gravity in the device frame, NULL when the reading carried none.
		`ALTER TABLE readings ADD COLUMN IF NOT EXISTS gx REAL`,
		`ALTER TABLE readings ADD COLUMN IF NOT EXISTS gy REAL`,
		`ALTER TABLE readings ADD COLUMN IF NOT EXISTS gz REAL`,
	}
	for _, q := range must {
		if _, err := t.pool.Exec(ctx, q); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	// TimescaleDB extras: hypertables and a per-zone per-second continuous
	// aggregate. Plain Postgres works without them.
	optional := []string{
		`CREATE EXTENSION IF NOT EXISTS timescaledb`,
		`SELECT create_hypertable('readings', 'time', if_not_exists => TRUE, migrate_data => TRUE)`,
		`SELECT create_hypertable('alerts', 'time', if_not_exists => TRUE, migrate_data => TRUE)`,
		`CREATE MATERIALIZED VIEW IF NOT EXISTS zone_1s WITH (timescaledb.continuous) AS
			SELECT time_bucket('1 second', time) AS bucket, zone,
			       count(*) AS n,
			       count(DISTINCT phone_id) AS phones,
			       sqrt(avg(ax*ax + az*az)) AS rms_horizontal,
			       max(rot) AS max_rot
			FROM readings GROUP BY bucket, zone WITH NO DATA`,
		`SELECT add_continuous_aggregate_policy('zone_1s',
			start_offset => INTERVAL '10 minutes', end_offset => INTERVAL '1 second',
			schedule_interval => INTERVAL '5 seconds', if_not_exists => TRUE)`,
	}
	for _, q := range optional {
		if _, err := t.pool.Exec(ctx, q); err != nil {
			log.Printf("store: timescale feature skipped (%v)", firstLine(err.Error()))
			break
		}
	}
	return nil
}

func (t *Tiger) Reading(r Reading) {
	t.mu.Lock()
	if len(t.pending) < 200_000 { // ~5 min of 60 phones if the DB stalls
		t.pending = append(t.pending, r)
	}
	t.mu.Unlock()
}

func (t *Tiger) Alert(a AlertRow) {
	t.mu.Lock()
	t.alerts = append(t.alerts, a)
	t.mu.Unlock()
}

func (t *Tiger) loop() {
	defer close(t.done)
	tick := time.NewTicker(FlushEvery)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			t.flush()
		case <-t.stop:
			t.flush()
			return
		}
	}
}

func (t *Tiger) flush() {
	t.mu.Lock()
	batch, alerts := t.pending, t.alerts
	t.pending, t.alerts = nil, nil
	t.mu.Unlock()
	if len(batch) == 0 && len(alerts) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if len(batch) > 0 {
		_, err := t.pool.CopyFrom(ctx, pgx.Identifier{"readings"},
			[]string{"time", "phone_id", "zone", "x", "y", "ax", "ay", "az", "rot", "gx", "gy", "gz"},
			pgx.CopyFromSlice(len(batch), func(i int) ([]any, error) {
				r := batch[i]
				var gx, gy, gz *float32
				if len(r.G) == 3 {
					g := [3]float32{float32(r.G[0]), float32(r.G[1]), float32(r.G[2])}
					gx, gy, gz = &g[0], &g[1], &g[2]
				}
				return []any{r.Time, r.PhoneID, r.Zone, float32(r.X), float32(r.Y), r.AX, r.AY, r.AZ, r.Rot, gx, gy, gz}, nil
			}))
		if err != nil {
			log.Printf("store: COPY %d readings failed, writing to fallback: %v", len(batch), err)
			if t.fallback != nil {
				for _, r := range batch {
					t.fallback.Reading(r)
				}
			}
		}
	}
	for _, a := range alerts {
		if _, err := t.pool.Exec(ctx, `INSERT INTO alerts (time, zone, level, score, brief) VALUES ($1,$2,$3,$4,$5)`,
			a.Time, a.Zone, a.Level, a.Score, a.Brief); err != nil {
			log.Printf("store: alert insert failed: %v", err)
			if t.fallback != nil {
				t.fallback.Alert(a)
			}
		}
	}
}

// Close flushes what is pending and disconnects.
func (t *Tiger) Close() {
	t.once.Do(func() {
		close(t.stop)
		<-t.done
		t.pool.Close()
		if t.fallback != nil {
			t.fallback.Close()
		}
	})
}

// SaveRun stores a labelled run's time range so it can be replayed from here.
func (t *Tiger) SaveRun(ctx context.Context, r Run) error {
	_, err := t.pool.Exec(ctx, `INSERT INTO runs (label, start_time, end_time) VALUES ($1,$2,$3)`, r.Label, r.Start, r.End)
	return err
}

// Runs lists stored runs, newest first.
func (t *Tiger) Runs(ctx context.Context) ([]Run, error) {
	rows, err := t.pool.Query(ctx, `SELECT label, start_time, end_time FROM runs ORDER BY start_time DESC LIMIT 50`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Run, error) {
		var r Run
		err := row.Scan(&r.Label, &r.Start, &r.End)
		return r, err
	})
}

// LoadRun reads a stored run back as replayable records.
func (t *Tiger) LoadRun(ctx context.Context, label string) ([]Record, error) {
	var run Run
	err := t.pool.QueryRow(ctx, `SELECT label, start_time, end_time FROM runs WHERE label = $1 ORDER BY start_time DESC LIMIT 1`, label).
		Scan(&run.Label, &run.Start, &run.End)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("no run labelled %q", label)
	}
	if err != nil {
		return nil, err
	}
	rows, err := t.pool.Query(ctx, `SELECT time, phone_id, row, col, x, y, ax, ay, az, rot, gx, gy, gz FROM readings
		WHERE time BETWEEN $1 AND $2 ORDER BY time`, run.Start, run.End)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	recs := []Record{{K: KindMeta, T: run.Start.UnixMilli(), Label: run.Label}}
	// A hello the first time each phone appears, a pos whenever it moves.
	// Rows from before free positions have no x/y: their hello carries
	// row/col and the replay maps it (legacy layout).
	type where struct {
		row, col int
		x, y     *float32
	}
	same := func(a, b *float32) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
	seen := map[string]where{}
	for rows.Next() {
		var ts time.Time
		var r Record
		var w where
		var gx, gy, gz *float32
		if err := rows.Scan(&ts, &r.ID, &w.row, &w.col, &w.x, &w.y, &r.AX, &r.AY, &r.AZ, &r.Rot, &gx, &gy, &gz); err != nil {
			return nil, err
		}
		if gx != nil && gy != nil && gz != nil {
			r.G = []float64{float64(*gx), float64(*gy), float64(*gz)}
		}
		p, ok := seen[r.ID]
		if !ok || p.row != w.row || p.col != w.col || !same(p.x, w.x) || !same(p.y, w.y) {
			seen[r.ID] = w
			pr := Record{K: KindHello, T: ts.UnixMilli(), ID: r.ID, Row: w.row, Col: w.col}
			if ok {
				pr.K = KindPos
			}
			if w.x != nil && w.y != nil {
				pr.X, pr.Y = F(float64(*w.x)), F(float64(*w.y))
			}
			recs = append(recs, pr)
		}
		r.K, r.T, r.CT = KindM, ts.UnixMilli(), ts.UnixMilli()
		recs = append(recs, r)
	}
	return recs, rows.Err()
}

func firstLine(s string) string {
	for i, c := range s {
		if c == '\n' {
			return s[:i]
		}
	}
	return s
}

// Open picks the best available sink: Tiger Data if url is set and
// reachable, otherwise the JSONL auto-recorder in dir. It never fails; with
// nothing writable it returns a sink that drops everything.
func Open(ctx context.Context, url, dir string) (Sink, *Tiger) {
	fallback := func() Sink {
		s, err := NewJSONLSink(filepath.Join(dir, "auto"))
		if err != nil {
			log.Printf("store: no fallback recorder (%v); readings will not be stored", err)
			return Discard{}
		}
		log.Printf("store: recording to %s", s.Path())
		return s
	}
	if url == "" {
		log.Printf("store: TIGER_DATABASE_URL not set")
		return fallback(), nil
	}
	t, err := OpenTiger(ctx, url, &lazySink{open: fallback})
	if err != nil {
		log.Printf("store: Tiger Data unreachable (%v)", err)
		return fallback(), nil
	}
	log.Printf("store: writing to Tiger Data")
	return t, t
}

// Discard drops everything.
type Discard struct{}

func (Discard) Reading(Reading) {}
func (Discard) Alert(AlertRow)  {}
func (Discard) Close()          {}

// lazySink only creates its file when the database first fails.
type lazySink struct {
	open func() Sink
	once sync.Once
	s    Sink
}

func (l *lazySink) get() Sink {
	l.once.Do(func() { l.s = l.open() })
	return l.s
}

func (l *lazySink) Reading(r Reading) { l.get().Reading(r) }
func (l *lazySink) Alert(a AlertRow)  { l.get().Alert(a) }

// Close only closes a sink that was actually opened.
func (l *lazySink) Close() {
	opened := true
	l.once.Do(func() { opened = false })
	if opened && l.s != nil {
		l.s.Close()
	}
}
