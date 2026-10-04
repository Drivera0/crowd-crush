// Package brief asks Gemini to explain an alert as a structured briefing
// (a headline and one action), and to read a venue's floor plan.
//
// Gemini never decides whether a zone is in danger: the detector already has.
// Every briefing call has a 5 s timeout and falls back to a template.
package brief

import (
	"bytes"
	"context"
	"encoding/base64"
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

// Timeout bounds every briefing and "ask" call.
const Timeout = 5 * time.Second

// Info is what the detector knows about an alert.
//
// Kind "wave" (the default) is a push travelling through the crowd; kind
// "density" is a cluster of people packing too tightly, described by the
// density fields; kind "rule" is an area rule staff set (Rule says which).
type Info struct {
	Kind        string    `json:"kind,omitempty"` // wave | density | rule
	Zone        string    `json:"zoneId"`
	Where       string    `json:"where,omitempty"` // the zone's name for people: "Zone A", "Stage front"
	Level       string    `json:"level"`
	SecondsHigh float64   `json:"secondsElevated,omitempty"` // time since the zone left calm
	Scores      []float64 `json:"scoreTrendLast20s,omitempty"`
	Direction   string    `json:"waveDirection,omitempty"` // +x, -x, +y, -y (legacy +col, -col, +row, -row)
	LagMs       int64     `json:"lagPerPersonMs,omitempty"`
	Phones      int       `json:"phonesInZone"`
	Swaying     int       `json:"phonesSwaying,omitempty"`

	// Density alerts (and density rules).
	Density float64 `json:"peoplePerSquareMetre,omitempty"` // estimated
	People  int     `json:"estimatedPeople,omitempty"`
	AreaM2  float64 `json:"areaSquareMetres,omitempty"`
	Trend   string  `json:"trend,omitempty"` // forming | steady | dispersing
	X       float64 `json:"x,omitempty"`     // where on the venue map (m)
	Y       float64 `json:"y,omitempty"`
	// Early warning: not dangerous yet, but at the current rate the density
	// reaches Danger (people/m²) in about ETA seconds.
	Early  bool    `json:"earlyWarning,omitempty"`
	ETA    float64 `json:"secondsUntilDangerous,omitempty"`
	Rate   float64 `json:"densityRisePerMinute,omitempty"`
	Danger float64 `json:"dangerDensity,omitempty"`
	// Motion: what the crowd at the densest spot is doing, in words
	// ("packed and barely moving", "packing in: people arriving and nobody
	// getting out", "dense but moving: people are getting out"); "" when
	// positions are too rough to tell. Leaving: people per metre per second
	// getting out of that spot.
	Motion  string  `json:"crowdMotion,omitempty"`
	Leaving float64 `json:"peopleLeavingPerMetrePerSecond,omitempty"`

	// Exit is the name of the open exit nearest the problem (from the venue
	// layout), "" if the venue has none.
	Exit string `json:"nearestExit,omitempty"`

	// Rule alerts: which rule ("density" or "capacity") and its limit
	// (people/m², or people for capacity).
	Rule  string  `json:"rule,omitempty"`
	Limit float64 `json:"ruleLimit,omitempty"`

	// Message is the action staff set for this area: used verbatim.
	Message string `json:"staffAction,omitempty"`
}

// Briefing is a structured briefing: what is happening and where, and the
// one thing staff should do.
type Briefing struct {
	Headline string `json:"headline"`
	Action   string `json:"action"`
}

// Text is the briefing as one string, for the voice and the timeline.
func (b Briefing) Text() string {
	return strings.TrimSpace(b.Headline + " " + b.Action)
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

// Template is the fallback briefing when Gemini is unavailable. A staff
// message replaces the action.
func Template(in Info) Briefing {
	b := template(in)
	if m := strings.TrimSpace(in.Message); m != "" {
		b.Action = m
	}
	return b
}

func template(in Info) Briefing {
	at := Place(in)
	// out is the way out, when the venue layout has exits.
	out := func(without, with string) string {
		if in.Exit == "" {
			return without
		}
		return fmt.Sprintf(with, in.Exit)
	}
	switch in.Kind {
	case "density":
		if in.Early && in.Level != "red" {
			return Briefing{
				fmt.Sprintf("%s: about %d people packing in fast; at this rate it reaches a dangerous %s per m² in about %.0f s.",
					at, in.People, trimNum(math.Max(1, in.Danger)), math.Max(1, in.ETA)),
				out("Open space ahead of them now.", "Open space ahead of them now, toward %s.")}
		}
		if in.Level != "red" {
			if in.Motion != "" {
				return Briefing{fmt.Sprintf("%s: people bunching up near %.0f, %.0f, %s.", at, in.X, in.Y, in.Motion), "Watch closely."}
			}
			return Briefing{fmt.Sprintf("%s: people bunching up near %.0f, %.0f.", at, in.X, in.Y), "Watch closely."}
		}
		return Briefing{
			fmt.Sprintf("%s: about %d people packed into %.0f square metres near %.0f, %.0f, %s.",
				at, in.People, math.Max(1, math.Round(in.AreaM2)), in.X, in.Y, TrendText(in.Trend)),
			fmt.Sprintf("Stop entry to %s and %s.", at, out("open space around them now", "move people out toward %s now"))}
	case "rule":
		if in.Rule == "capacity" {
			return Briefing{
				fmt.Sprintf("%s: about %d people inside, over this area's limit of %.0f people.", at, in.People, in.Limit),
				fmt.Sprintf("Stop entry to %s %s.", at, out("until it clears", "and send people on toward %s"))}
		}
		if in.Level != "red" {
			return Briefing{fmt.Sprintf("%s: crowd density nearing this area's limit.", at), "Watch closely."}
		}
		return Briefing{
			fmt.Sprintf("%s: about %.0f people per square metre, above this area's limit of %s.", at, math.Max(1, in.Density), trimNum(in.Limit)),
			fmt.Sprintf("Stop entry to %s and %s.", at, out("open space now", "move people out toward %s now"))}
	}
	if in.Level != "red" {
		return Briefing{fmt.Sprintf("%s: crowd sway building.", at), "Watch closely."}
	}
	speed := ""
	if in.LagMs > 0 {
		speed = fmt.Sprintf(", about one person every %d milliseconds", in.LagMs)
	}
	return Briefing{
		fmt.Sprintf("%s: crowd waves travelling %s%s.", at, DirectionText(in.Direction), speed),
		fmt.Sprintf("Stop entry to %s and open relief exits now%s.", at, out("", ", starting with %s"))}
}

// trimNum prints 4 as "4" and 2.5 as "2.5".
func trimNum(v float64) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.1f", v), "0"), ".")
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
	// No client-level timeout: each call's context bounds it (5 s for
	// briefings, longer for floor plans).
	return &Client{key: key, model: model, base: "https://generativelanguage.googleapis.com", http: &http.Client{}}
}

// WithBase points the client at another API host (a fake one in tests).
func (c *Client) WithBase(u string) *Client {
	c.base = strings.TrimRight(u, "/")
	return c
}

// Enabled reports whether a key is configured.
func (c *Client) Enabled() bool { return c != nil && c.key != "" }

const system = `You are the voice of a crowd-safety early-warning system used by event stewards.
The detector (not you) has already decided the alert level from phone motion sensors.
Reply as JSON with two fields, to be read aloud over a radio:
- "headline": one short sentence saying what is happening and where, in plain words;
- "action": one short sentence with one concrete instruction for stewards.
Name the place exactly as the "where" field says; never read out zoneId.
If the data has a "staffAction", that is the action staff chose for this area: use it verbatim as "action".
Otherwise, if the data has a "nearestExit", name that exit in the action as the way out.
If the data has a "crowdMotion", say it in the headline: it is why the crowding matters (packed and still, or packing in).
No markdown, no numbers with decimals, under 40 words in total.`

// briefSchema is the structured-output schema for a briefing.
var briefSchema = map[string]any{
	"type": "OBJECT",
	"properties": map[string]any{
		"headline": map[string]any{"type": "STRING"},
		"action":   map[string]any{"type": "STRING"},
	},
	"required":         []string{"headline", "action"},
	"propertyOrdering": []string{"headline", "action"},
}

// Brief returns a structured briefing. On any failure it returns the
// template together with the error, so callers can always use it.
func (c *Client) Brief(ctx context.Context, in Info) (Briefing, error) {
	if !c.Enabled() {
		return Template(in), ErrNoKey
	}
	b, _ := json.Marshal(in)
	meaning := "Alert type: a push wave travelling through the crowd. Direction meaning: " + DirectionText(in.Direction)
	switch in.Kind {
	case "density":
		if in.Early && in.Level != "red" {
			meaning = "Alert type: early warning of crowding. It is not dangerous yet: the density is rising fast and, at the current rate, is projected to reach the danger density (dangerDensity, people per square metre) in about secondsUntilDangerous seconds. Say clearly that this is a projection, e.g. 'at this rate it reaches a dangerous level in about 12 seconds'."
			break
		}
		meaning = "Alert type: crowding. People are packed too tightly in one spot (positions are metres on the venue map, origin top-left); this is a density alert, not a push wave. Trend: " + TrendText(in.Trend)
	case "rule":
		if in.Rule == "capacity" {
			meaning = "Alert type: an area rule set by staff. More people are estimated inside the area (estimatedPeople) than its capacity (ruleLimit, people); this is about capacity, not a push wave."
		} else {
			meaning = "Alert type: an area rule set by staff. The estimated crowd density inside the area (peoplePerSquareMetre) has stayed above its limit (ruleLimit, people per square metre); this is crowding, not a push wave."
		}
	}
	user := "Alert data:\n" + string(b) + "\n" + meaning
	if m := strings.TrimSpace(in.Message); m != "" {
		user += fmt.Sprintf("\nUse this action verbatim: %q", m)
	}
	out, err := c.generate(ctx, request{sys: system, user: user, maxTokens: 200, schema: briefSchema, timeout: Timeout})
	if err != nil {
		return Template(in), err
	}
	bf, err := parseBriefing(out)
	if err != nil {
		return Template(in), err
	}
	if m := strings.TrimSpace(in.Message); m != "" {
		bf.Action = m // staff's words, whatever the model did with them
	}
	return bf, nil
}

// parseBriefing reads the model's JSON. A model that ignored the schema and
// wrote plain sentences still works: first sentence headline, rest action.
func parseBriefing(s string) (Briefing, error) {
	var b Briefing
	if err := json.Unmarshal([]byte(stripFence(s)), &b); err == nil {
		b.Headline, b.Action = oneLine(b.Headline), oneLine(b.Action)
		if b.Headline == "" && b.Action == "" {
			return b, errors.New("gemini: empty briefing")
		}
		return b, nil
	}
	s = oneLine(s)
	if strings.HasPrefix(s, "{") {
		return b, errors.New("gemini: briefing is not valid JSON")
	}
	if i := strings.IndexAny(s, ".!?"); i >= 0 && i < len(s)-1 {
		return Briefing{strings.TrimSpace(s[:i+1]), strings.TrimSpace(s[i+1:])}, nil
	}
	return Briefing{Headline: s}, nil
}

// stripFence removes a ```json … ``` fence some models add anyway.
func stripFence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimPrefix(s, "json")
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	return strings.TrimSpace(s)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// Ask answers a free-form question about recent activity.
func (c *Client) Ask(ctx context.Context, question, history string) (string, error) {
	if !c.Enabled() {
		return "", ErrNoKey
	}
	sys := `You answer questions from event stewards about a crowd-safety monitor.
Use only the log provided. The "Current situation" and "Active incidents" lines are the state right now: never call it calm while they say otherwise.
Drills (test alerts) are not incidents; mention them only if asked.
Name places exactly as the log names them; never use internal ids or codes.
Be brief: at most three sentences. If the log doesn't say, say so.`
	out, err := c.generate(ctx, request{sys: sys, user: "Log:\n" + history + "\n\nQuestion: " + question, maxTokens: 250, timeout: Timeout})
	return oneLine(out), err
}

// errThinking means the model rejected the thinking budget.
var errThinking = errors.New("gemini: thinking config rejected")

// request is one generateContent call.
type request struct {
	sys, user string
	image     []byte // optional inline image
	mime      string
	maxTokens int
	schema    map[string]any // structured JSON output when set
	timeout   time.Duration
	temp      float64 // 0 = 0.4
}

func (c *Client) generate(ctx context.Context, r request) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	// Thinking off keeps briefings fast; models that refuse that get a retry
	// without it (with room for the thinking tokens).
	out, err := c.call(ctx, r, true)
	if errors.Is(err, errThinking) {
		r.maxTokens += 1024
		out, err = c.call(ctx, r, false)
	}
	return out, err
}

func (c *Client) call(ctx context.Context, r request, noThinking bool) (string, error) {
	temp := r.temp
	if temp == 0 {
		temp = 0.4
	}
	gen := map[string]any{"temperature": temp, "maxOutputTokens": r.maxTokens}
	if noThinking {
		gen["thinkingConfig"] = map[string]any{"thinkingBudget": 0}
	}
	if r.schema != nil {
		gen["responseMimeType"] = "application/json"
		gen["responseSchema"] = r.schema
	}
	parts := []map[string]any{}
	if len(r.image) > 0 {
		parts = append(parts, map[string]any{"inlineData": map[string]string{
			"mimeType": r.mime, "data": base64.StdEncoding.EncodeToString(r.image)}})
	}
	parts = append(parts, map[string]any{"text": r.user})
	body := map[string]any{
		"systemInstruction": map[string]any{"parts": []map[string]string{{"text": r.sys}}},
		"contents":          []map[string]any{{"role": "user", "parts": parts}},
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
	var res struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", fmt.Errorf("gemini: decode: %w", err)
	}
	var sb strings.Builder
	if len(res.Candidates) > 0 {
		for _, p := range res.Candidates[0].Content.Parts {
			if !p.Thought {
				sb.WriteString(p.Text)
			}
		}
	}
	text := strings.TrimSpace(sb.String())
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
