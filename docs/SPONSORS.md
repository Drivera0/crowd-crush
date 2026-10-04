# Pulse: what each sponsor does

The rule across all of them: **math detects; the sponsors explain, store and reach people.** None of them decides whether a crowd is in danger, and every one can be down or missing its key while Pulse still detects and alerts. `./bin/pulse -check` shows which are connected, and the dashboard header shows which are live.

| Sponsor | Its job in Pulse | Without it |
|---|---|---|
| Google Gemini | Writes each alert briefing; reads floor plans | A template sentence; floor plans drawn by hand |
| ElevenLabs | Speaks each briefing aloud | The browser's own voice |
| Tiger Data (TimescaleDB) | Stores every reading, alert and recorded run | Local JSONL files |
| .Tech domain | The HTTPS address on the join QR (`pulsecrowd.tech`) | A random Cloudflare quick-tunnel address |

---

## Google Gemini: writes the sentence and reads the floor plan

Code: `server/internal/brief/brief.go`, `server/internal/brief/floorplan.go`

**1. Alert briefings (structured output).**
When an alert fires, Gemini gets the facts: the place, the level, the kind of incident (push, packing or an area rule), how dense, which way the push is travelling, and the nearest exit. It returns structured JSON with a headline and one action, for example:

> "Stage front: crowd push travelling left to right." / "Stop entry and open the side exit now."

- Early warnings are worded as a projection: "at this rate it reaches a dangerous 4 per m² in about 12 s."
- If staff wrote a message for an area, Gemini is told to use it word for word, and the server enforces it.
- The **Ask the AI** panel answers staff questions from the live situation and the alert history.

**2. Floor-plan vision (response schema).**
On the Venue page, staff upload a floor-plan image. Gemini reads it and returns the venue size, stage outline, exits and walls in metres, with notes on how it judged the scale (labels, scale bars, ~0.9 m doors, typical stage sizes) and a confidence. Nothing is saved until staff review and apply it, and the crowd simulator then uses that geometry.

**Fails soft.** Gemini is asked only after the detector has already raised the alert. After a 5 s timeout, a template sentence is used instead, so the alarm never waits on it.

---

## ElevenLabs: says it out loud

Code: `server/internal/voice/voice.go`

- Each briefing becomes an MP3 (Flash v2.5 model) that the dashboard plays the moment the alert arrives, so a steward who isn't watching the screen still hears it.
- A red alert nobody acknowledges within 60 s is re-voiced as "Still unacknowledged. …", and **Escalate now** does the same on demand.
- Drills are spoken as "This is a drill."
- Each watch area can turn voice on or off.
- A fallback clip is generated once at startup, so there's always something to play.

**Fails soft.** Without a key, the browser's built-in speech reads the same text.

---

## Tiger Data (TimescaleDB): the memory

Code: `server/internal/store/tiger.go` (fallback: `store/jsonl.go`)

- **`readings` hypertable.** Every phone's motion summary is written here: 10 a second per phone, with acceleration, rotation, gravity, venue position and zone. Rows are batched with Postgres `COPY` every 500 ms, so 1,000 phones don't mean 10,000 single inserts a second.
- **`alerts` hypertable.** Every alert and its updates (acknowledged, resolved, escalated).
- **`runs` table.** Labelled recordings, such as `wave-push-end` or `dance-jumping`.
- **`zone_1s` continuous aggregate.** Rolls readings up per zone per second (phones, horizontal motion, peak rotation) and refreshes every 5 s, giving per-second zone history without rescanning raw rows.
- **Replay.** A recorded run can be read back out of Tiger and played through the same detector as live phones.

**Fails soft.** If the database is unreachable, the same records go to `recordings/auto/*.jsonl` and nothing stops.

`[TIGER: confirm Tiger Data is a 2026 sponsor before entering its track]`

---

## .Tech domain: the address on the QR code

Domain: **`pulsecrowd.tech`** (from the GitHub Student Pack)

- Phones only give a web page their motion sensors over **HTTPS**, so "scan and join" needs a public HTTPS address.
- `pulsecrowd.tech` points at a named Cloudflare tunnel to the laptop. The join QR stays the same across restarts, and it's short enough to type.
- The dashboard tests the join link itself (`POST /api/join/test`) and shows whether it's reachable.
- The deck is also served at `https://pulsecrowd.tech/deck` for the iPad.

`[.TECH: confirm the named tunnel is live before entering this track]`

---

## Not sponsors, but used

- **Cloudflare Tunnel** gives the laptop a public HTTPS address with no port forwarding.
- **Arduino UNO R4 WiFi** (the STOP sign) and **two ESP32s** (zone lights) are for staff who aren't watching a screen. They run over USB and Pulse works without them.

---

## One-line versions for the pitch

- **Gemini:** "Two jobs: one-sentence briefings with structured output, and reading a floor plan into walls, stage and exits. It never decides danger."
- **ElevenLabs:** "Every red alert is spoken, so a steward who isn't looking at the screen still hears what to do."
- **Tiger Data:** "Every reading goes into a hypertable in batches, so any run can be replayed through the same detector."
- **.Tech:** "pulsecrowd.tech is what makes 'scan and join' work, because phones need HTTPS for their motion sensors."
