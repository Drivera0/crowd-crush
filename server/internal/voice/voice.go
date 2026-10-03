// Package voice turns briefings into mp3 files with ElevenLabs.
// Files are served at /audio/<name>.mp3. Every failure is soft: the
// dashboard falls back to the pre-generated clip or the browser's own voice.
package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"
)

// DefaultVoice is used when ELEVENLABS_VOICE_ID is not set.
const DefaultVoice = "21m00Tcm4TlvDq8ikWAM"

// FallbackText is spoken by the clip pre-generated at startup.
const FallbackText = "Zone B, crowd waves building."

const fallbackName = "fallback.mp3"

// ErrNoKey means ELEVENLABS_API_KEY is not set.
var ErrNoKey = errors.New("elevenlabs: no API key")

// Client talks to the ElevenLabs text-to-speech API.
type Client struct {
	key, voiceID, model, dir string
	base                     string
	http                     *http.Client
	seq                      atomic.Int64
}

// New creates a client writing mp3s into dir.
func New(key, voiceID, dir string) *Client {
	if voiceID == "" {
		voiceID = DefaultVoice
	}
	return &Client{key: key, voiceID: voiceID, model: "eleven_flash_v2_5", dir: dir,
		base: "https://api.elevenlabs.io", http: &http.Client{Timeout: 10 * time.Second}}
}

// Enabled reports whether a key is configured.
func (c *Client) Enabled() bool { return c != nil && c.key != "" }

// Dir is where mp3s are written.
func (c *Client) Dir() string { return c.dir }

// Speak synthesizes text and returns the URL path of the mp3.
func (c *Client) Speak(ctx context.Context, text string) (string, error) {
	name := strconv.FormatInt(time.Now().UnixMilli(), 10) + "-" + strconv.FormatInt(c.seq.Add(1), 10) + ".mp3"
	if err := c.synth(ctx, text, name); err != nil {
		return "", err
	}
	return "/audio/" + name, nil
}

// PrepareFallback generates the fallback clip once (kept across restarts).
func (c *Client) PrepareFallback(ctx context.Context) error {
	if _, err := os.Stat(filepath.Join(c.dir, fallbackName)); err == nil {
		return nil
	}
	return c.synth(ctx, FallbackText, fallbackName)
}

// FallbackURL is the pre-generated clip's URL, or "" if there is none.
func (c *Client) FallbackURL() string {
	if _, err := os.Stat(filepath.Join(c.dir, fallbackName)); err != nil {
		return ""
	}
	return "/audio/" + fallbackName
}

func (c *Client) synth(ctx context.Context, text, name string) error {
	if !c.Enabled() {
		return ErrNoKey
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	body, _ := json.Marshal(map[string]any{"text": text, "model_id": c.model})
	u := fmt.Sprintf("%s/v1/text-to-speech/%s?output_format=mp3_44100_128", c.base, c.voiceID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("xi-api-key", c.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "audio/mpeg")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("elevenlabs: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("elevenlabs: %s: %s", resp.Status, msg)
	}
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(c.dir, name+".tmp")
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, io.LimitReader(resp.Body, 10<<20))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("elevenlabs: %w", err)
	}
	return os.Rename(tmp, filepath.Join(c.dir, name))
}
