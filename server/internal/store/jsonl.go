// Package store records readings and alerts: JSONL files for labelled runs
// (tuning, tests, replay) and Tiger Data for continuous storage, falling back
// to JSONL when the database is unreachable.
package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Record kinds.
const (
	KindMeta  = "meta"  // first line: label, venue size
	KindHello = "hello" // phone joined at x/y (old recordings: row/col)
	KindPos   = "pos"   // phone moved to x/y (dragged, walked, or a GPS fix converted to metres)
	KindSync  = "sync"  // clock sync result
	KindM     = "m"     // one motion summary
	KindBye   = "bye"   // phone disconnected
)

// Record is one line of a JSONL recording. Zero values are omitted, which is
// lossless for numbers; positions are pointers so 0 m stays distinguishable
// from "absent" (old row/col recordings).
//
// Privacy: positions are venue metres only. GPS fixes are converted on
// arrival; latitude and longitude are never recorded.
type Record struct {
	K      string    `json:"k"`
	T      int64     `json:"t"` // server time (ms) the event happened
	ID     string    `json:"id,omitempty"`
	X      *float64  `json:"x,omitempty"` // venue metres
	Y      *float64  `json:"y,omitempty"`
	Acc    float64   `json:"acc,omitempty"` // GPS accuracy (m); 0 = placed by hand
	Out    bool      `json:"out,omitempty"` // GPS fix outside the venue (clamped)
	Row    int       `json:"row,omitempty"` // legacy grid cell
	Col    int       `json:"col,omitempty"`
	UA     string    `json:"ua,omitempty"`
	CT     int64     `json:"ct,omitempty"` // clock-corrected phone time of a reading
	AX     float64   `json:"ax,omitempty"`
	AY     float64   `json:"ay,omitempty"`
	AZ     float64   `json:"az,omitempty"`
	Rot    float64   `json:"rot,omitempty"`
	G      []float64 `json:"g,omitempty"` // gravity in the device frame, when the reading carried one (protocol.Motion.G)
	RTT    int64     `json:"rtt,omitempty"`
	Offset int64     `json:"off,omitempty"`
	Label  string    `json:"label,omitempty"`
	Rows   int       `json:"rows,omitempty"` // legacy grid size
	Cols   int       `json:"cols,omitempty"`
	W      float64   `json:"w,omitempty"` // meta: venue size (m)
	H      float64   `json:"h,omitempty"`
}

// F returns a pointer to v, for Record positions.
func F(v float64) *float64 { return &v }

// HasPos reports whether the record carries an x/y position.
func (r Record) HasPos() bool { return r.X != nil && r.Y != nil }

// JSONLWriter appends records to a file. Safe for concurrent use.
type JSONLWriter struct {
	mu   sync.Mutex
	f    *os.File
	w    *bufio.Writer
	enc  *json.Encoder
	path string
	n    int
}

// CreateJSONL creates (or truncates) a recording file, making parent dirs.
func CreateJSONL(path string) (*JSONLWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	w := bufio.NewWriterSize(f, 64<<10)
	return &JSONLWriter{f: f, w: w, enc: json.NewEncoder(w), path: path}, nil
}

// Path is the file being written.
func (j *JSONLWriter) Path() string { return j.path }

// Write appends one record.
func (j *JSONLWriter) Write(r Record) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.n++
	return j.enc.Encode(r)
}

// Count is how many records were written.
func (j *JSONLWriter) Count() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.n
}

// Flush pushes buffered records to disk.
func (j *JSONLWriter) Flush() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.w.Flush()
}

// Close flushes and closes the file.
func (j *JSONLWriter) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.w.Flush(); err != nil {
		j.f.Close()
		return err
	}
	return j.f.Close()
}

// ReadJSONL loads a whole recording, sorted by time (stable).
func ReadJSONL(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var recs []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		b := sc.Bytes()
		if len(strings.TrimSpace(string(b))) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		recs = append(recs, r)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(recs, func(a, b int) bool { return recs[a].T < recs[b].T })
	return recs, nil
}

var unsafeChars = regexp.MustCompile(`[^a-z0-9]+`)

// Slug makes a label safe for a filename.
func Slug(label string) string {
	s := strings.Trim(unsafeChars.ReplaceAllString(strings.ToLower(label), "-"), "-")
	if s == "" {
		s = "run"
	}
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

// RunFileName is the file name for a labelled run started at t.
func RunFileName(label string, t time.Time) string {
	return Slug(label) + "-" + t.Format("20060102-150405") + ".jsonl"
}

// RecordingInfo describes a recording on disk.
type RecordingInfo struct {
	Name  string `json:"name"` // path relative to the recordings dir
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"`
}

// ListJSONL lists recordings in dir and its auto/ subdirectory, newest first.
func ListJSONL(dir string) ([]RecordingInfo, error) {
	var out []RecordingInfo
	for _, sub := range []string{"", "auto"} {
		ents, err := os.ReadDir(filepath.Join(dir, sub))
		if err != nil {
			continue
		}
		for _, e := range ents {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			out = append(out, RecordingInfo{Name: filepath.ToSlash(filepath.Join(sub, e.Name())), Size: info.Size(), MTime: info.ModTime().UnixMilli()})
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].MTime > out[b].MTime })
	return out, nil
}

// SafeJoin resolves a recording name inside dir, refusing path traversal.
func SafeJoin(dir, name string) (string, error) {
	clean := filepath.Clean("/" + filepath.FromSlash(name))
	if !strings.HasSuffix(clean, ".jsonl") {
		return "", fmt.Errorf("not a recording: %q", name)
	}
	return filepath.Join(dir, clean), nil
}
