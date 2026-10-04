package sign

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Firmware build ids. scripts/boards.sh (and the Windows flash scripts)
// compile each sketch with a generated pulse_build.h:
//
//	#define PULSE_FW "1a2b3c4 2026-10-04"
//
// The first word is the git blob hash (7 hex digits) of the sketch with
// line endings normalised to LF, so it changes exactly when the sketch
// does, committed or not. A board reports it as "fw" in /pulse and in its
// "S" reply; "dev" means it was built by hand without the script.

// FirmwareDir is where the sketches live, relative to the working
// directory (the repo root, where the server runs).
var FirmwareDir = "arduino"

// SketchPath is the sketch a board kind runs ("sign", "zone-light").
func SketchPath(kind string) string {
	switch kind {
	case "sign":
		return filepath.Join(FirmwareDir, "sign", "sign.ino")
	case "zone-light":
		return filepath.Join(FirmwareDir, "zone-light", "zone-light.ino")
	}
	return ""
}

// SketchID is the build id hash of a sketch file.
func SketchID(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return sketchHash(b), nil
}

func sketchHash(b []byte) string {
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(b))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))[:7]
}

// BuildID is the full id for a build of the sketch at path made now.
func BuildID(path string) (string, error) {
	id, err := SketchID(path)
	if err != nil {
		return "", err
	}
	return id + " " + time.Now().Format("2006-01-02"), nil
}

// FWHash is the hash part of a build id ("" for none or "dev").
func FWHash(fw string) string {
	f := strings.Fields(fw)
	if len(f) == 0 || len(f[0]) != 7 {
		return ""
	}
	return f[0]
}

type fwCache struct {
	mu   sync.Mutex
	at   map[string]time.Time
	hash map[string]string
}

var wantCache = fwCache{at: map[string]time.Time{}, hash: map[string]string{}}

// WantFW is the hash a board of this kind should report: the sketch in
// this checkout ("" if the sketch isn't there, e.g. the server runs from
// another directory). Re-read when the file changes.
func WantFW(kind string) string {
	p := SketchPath(kind)
	if p == "" {
		return ""
	}
	fi, err := os.Stat(p)
	if err != nil {
		return ""
	}
	wantCache.mu.Lock()
	defer wantCache.mu.Unlock()
	if t, ok := wantCache.at[p]; ok && t.Equal(fi.ModTime()) {
		return wantCache.hash[p]
	}
	id, err := SketchID(p)
	if err != nil {
		return ""
	}
	wantCache.at[p], wantCache.hash[p] = fi.ModTime(), id
	return id
}

// FWOutOfDate reports whether a board's build id differs from the sketch
// in this checkout (false when that isn't known).
func FWOutOfDate(kind, fw string) bool {
	want := WantFW(kind)
	return want != "" && FWHash(fw) != want
}
