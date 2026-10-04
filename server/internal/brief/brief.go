// Package brief asks Gemini to explain an alert in two sentences.
//
// Gemini never decides whether a zone is in danger: the detector already has.
// Every call has a 5 s timeout and falls back to a template sentence.
package brief

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Timeout bounds every Gemini call.
const Timeout = 5 * time.Second

// Info is what the detector knows about an alert.
//
// Kind "wave" (the default) is a push travelling through the crowd; kind
// "density" is a cluster of people packing too tightly, described by the
// density fields.
type Info struct {
	Kind        string    `json:"kind,omitempty"` // wave | density
	Zone        string    `json:"zoneId"`
	Where       string    `json:"where,omitempty"` // the zone's name for people: "Zone A", "Stage front"
	Level       string    `json:"level"`
	SecondsHigh float64   `json:"secondsElevated,omitempty"` // time since the zone left calm
	Scores      []float64 `json:"scoreTrendLast20s,omitempty"`
	Direction   string    `json:"waveDirection,omitempty"` // +x, -x, +y, -y (legacy +col, -col, +row, -row)
	LagMs       int64     `json:"lagPerPersonMs,omitempty"`
	Phones      int       `json:"phonesInZone"`
	Swaying     int       `json:"phonesSwaying,omitempty"`

	// Density alerts.
	Density float64 `json:"peoplePerSquareMetre,omitempty"` // estimated
	People  int     `json:"estimatedPeople,omitempty"`
	AreaM2  float64 `json:"areaSquareMetres,omitempty"`
	Trend   string  `json:"trend,omitempty"` // forming | steady | dispersing
	X       float64 `json:"x,omitempty"`     // where on the venue map (m)
	Y       float64 `json:"y,omitempty"`
}

// DirectionText describes a wave direction for people. +x is left to
// right on the venue map, +y top to bottom.
func DirectionText(d string) string {
	switch d {
	case "+x":
		return "across the venue, left to right"
	case "-x":
		return "across the venue, right to left"
	case "+y":
		return "from the top of the map to the bottom"
	case "-y":
		return "from the bottom of the map to the top"
	case "+col":
		return "along the line, left to right"
	case "-col":
		return "along the line, right to left"
	case "+row":
		return "from front to back"
	case "-row":
		return "from back to front"
	}
	return "through the crowd"
}

// TrendText describes a cluster trend for people.
func TrendText(t string) string {
	switch t {
	case "forming":
		return "and getting denser"
	case "dispersing":
		return "though it is starting to thin out"
	}
	return "and holding"
}

// Place is how people should hear the zone: its name ("Stage front",
// "Zone A"), never an internal id like a drawn area's random one.
func Place(in Info) string {
	if in.Where != "" {
		return in.Where
	}
	return "Zone " + in.Zone
}

// Template is the fallback briefing when Gemini is unavailable.
func Template(in Info) string {
	at := Place(in)
	if in.Kind == "density" {
		if in.Level != "red" {
			return fmt.Sprintf("%s: people bunching up near %.0f, %.0f, watch closely.", at, in.X, in.Y)
		}
		return fmt.Sprintf("%s: about %d people packed into %.0f square metres near %.0f, %.0f, %s. Stop entry to %s and open space around them now.",
			at, in.People, math.Max(1, math.Round(in.AreaM2)), in.X, in.Y, TrendText(in.Trend), at)
	}
	if in.Level != "red" {
		return fmt.Sprintf("%s: crowd sway building, watch closely.", at)
	}
	speed := ""
	if in.LagMs > 0 {
		speed = fmt.Sprintf(", about one person every %d milliseconds", in.LagMs)
	}
	return fmt.Sprintf("%s: crowd waves travelling %s%s. Stop entry to %s and open relief exits now.",
		at, DirectionText(in.Direction), speed, at)
}

// Client talks to the Gemini REST API.
type Client struct {
	key   string
	model string
	base  string
	http  *http.Client
}

// ErrNoKey means GEMINI_API_KEY is not set.
var ErrNoKey = errors.New("gemini: no API key")

// New creates a client. An empty key makes every call return ErrNoKey.
func New(key, model string) *Client {
	if model == "" {
		model = "gemini-flash-latest"
	}
	return &Client{key: key, model: model, base: "https://generativelanguage.googleapis.com", http: &http.Client{Timeout: Timeout}}
}

// Enabled reports whether a key is configured.
func (c *Client) Enabled() bool { return c != nil && c.key != "" }

const system = `You are the voice of a crowd-safety early-warning system used by event stewards.
The detector (not you) has already decided the alert level from phone motion sensors.
Write exactly two short sentences to be read aloud over a radio:
1) what is happening and where, in plain words;
2) one concrete action for stewards.
Name the place exactly as the "where" field says; never read out zoneId.
No preamble, no markdown, no numbers with decimals, under 40 words total.`

// Brief returns a two-sentence briefing. On any failure it returns the
// template text together with the error, so callers can always use the text.
func (c *Client) Brief(ctx context.Context, in Info) (string, error) {
	if !c.Enabled() {
		return Template(in), ErrNoKey
	}
	b, _ := json.Marshal(in)
	meaning := "Alert type: a push wave travelling through the crowd. Direction meaning: " + DirectionText(in.Direction)
	if in.Kind == "density" {
		meaning = "Alert type: crowding. People are packed too tightly in one spot (positions are metres on the venue map, origin top-left); this is a density alert, not a push wave. Trend: " + TrendText(in.Trend)
	}
	out, err := c.generate(ctx, system, "Alert data:\n"+string(b)+"\n"+meaning, 120)
	if err != nil {
		return Template(in), err
	}
	return out, nil
}

// Ask answers a free-form question about recent activity.
func (c *Client) Ask(ctx context.Context, question, history string) (string, error) {
	if !c.Enabled() {
		return "", ErrNoKey
	}
	sys := `You answer questions from event stewards about the last minutes of a crowd-safety monitor.
Use only the log provided. Be brief: at most three sentences. If the log doesn't say, say so.`
	return c.generate(ctx, sys, "Log:\n"+history+"\n\nQuestion: "+question, 250)
}

// errThinking means the model rejected the thinking budget.
var errThinking = errors.New("gemini: thinking config rejected")

func (c *Client) generate(ctx context.Context, sys, user string, maxTokens int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	// Thinking off keeps briefings fast; models that refuse that get a retry
	// without it (with room for the thinking tokens).
	out, err := c.call(ctx, sys, user, maxTokens, true)
	if errors.Is(err, errThinking) {
		out, err = c.call(ctx, sys, user, maxTokens+1024, false)
	}
	return out, err
}

func (c *Client) call(ctx context.Context, sys, user string, maxTokens int, noThinking bool) (string, error) {
	gen := map[string]any{"temperature": 0.4, "maxOutputTokens": maxTokens}
	if noThinking {
		gen["thinkingConfig"] = map[string]any{"thinkingBudget": 0}
	}
	body := map[string]any{
		"systemInstruction": map[string]any{"parts": []map[string]string{{"text": sys}}},
		"contents":          []map[string]any{{"role": "user", "parts": []map[string]string{{"text": user}}}},
		"generationConfig":  gen,
	}
	b, _ := json.Marshal(body)
	u := fmt.Sprintf("%s/v1beta/models/%s:generateContent", c.base, url.PathEscape(c.model))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", c.key)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("gemini: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	// Some models reject thinkingBudget 0 with a generic "invalid argument",
	// so any 400 on the no-thinking call earns one retry without it.
	if resp.StatusCode == http.StatusBadRequest && noThinking {
		return "", errThinking
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
			return "", fmt.Errorf("gemini: %s: %s", resp.Status, truncate(e.Error.Message, 200))
		}
		return "", fmt.Errorf("gemini: %s: %s", resp.Status, truncate(string(raw), 200))
	}
	var r struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", fmt.Errorf("gemini: decode: %w", err)
	}
	var sb strings.Builder
	if len(r.Candidates) > 0 {
		for _, p := range r.Candidates[0].Content.Parts {
			if !p.Thought {
				sb.WriteString(p.Text)
			}
		}
	}
	text := strings.Join(strings.Fields(sb.String()), " ")
	if text == "" {
		return "", errors.New("gemini: empty response")
	}
	return text, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
