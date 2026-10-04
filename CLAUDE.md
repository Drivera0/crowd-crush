# Pulse: early warning for crowd crushes

StormHacks 2026, solo build (Dan). Phones in a crowd stream their motion to a Go server. When neighbouring phones start swaying together in a wave that travels person to person, the zone goes yellow then red, Gemini writes a briefing, ElevenLabs speaks it, and an Arduino sign flashes.

**Pitch:** Early warning for crowd crushes, using the phones already in the crowd.

## Ground rules for this repo

- **Git attribution: Dan is the only author.** Never add `Co-Authored-By`, `Generated with Claude Code`, session links or any other AI attribution to commit messages, PR titles or PR descriptions. Commit as the repo's configured git user only.
- **Go** for the server, simulator and all backend logic. **TypeScript** (Vite, no framework unless it earns its place) for the phone page and the dashboard.
- One Go binary serves everything: phone page, dashboard, WebSockets, APIs. One process to start in front of judges.
- Every external service (Tiger Data, Gemini, ElevenLabs, Arduino) must fail soft. If it's down or the key is missing, log it and keep detecting. The demo never depends on Wi-Fi to a third party.
- Math detects, AI explains. Gemini never decides whether a zone is in danger.
- Privacy: no names, contacts, location history, audio or photos. Random session ID + venue position + motion numbers only. Positions are venue-relative metres only (no GPS coordinates stored): the current position is used live and stored in recordings (and Tiger readings) so runs can be replayed. GPS is converted to venue-relative metres on arrival; raw coordinates are never stored or sent to the dashboard (nor logged).
- Build order matters more than polish: phones streaming → dashboard showing nodes → simulator → detector → sponsors → hardware.

## Architecture

```
 phones (TS page)  ──ws /ws/phone──►  Go server  ──ws /ws/dash──►  dashboard (TS, laptop)
                                       │  ├─ clock sync per phone
 sim (Go, fake phones) ──same ws──────►│  ├─ ring buffers per phone
                                       │  ├─ detector loop (every 250 ms)
                                       │  ├─ Tiger Data writer (batched)
                                       │  ├─ Gemini briefing on alert
                                       │  ├─ ElevenLabs TTS on alert → dashboard plays audio
                                       │  └─ Arduino sign (HTTP over Wi-Fi)
                                       └─ HTTPS via Cloudflare Tunnel → [name].tech
```

## Repo layout

```
server/
  cmd/pulse/main.go          # wires everything, flags/env, serves web/*/dist
  cmd/sim/main.go            # fake phones: crowd or line layout, wave/gather/false-positive scenarios
  internal/hub/              # phone + dashboard connections, broadcast
  internal/clocksync/        # NTP-style offset per phone
  internal/detect/           # filters, per-phone features, spatial neighbours, wave detection, zones, alert state
  internal/crowd/            # DBSCAN crowd clusters, tracking, trend, density levels
  internal/crowdsim/         # Social Force Model crowd (Helbing 1995/2000): bodies, director, phones from bodies, ground truth
  internal/geo/              # GPS → venue metres (equirectangular around the venue anchor)
  internal/store/            # Tiger Data (pgx) + JSONL fallback recorder
  internal/brief/            # Gemini client
  internal/voice/            # ElevenLabs client
  internal/sign/             # Arduino client
  internal/protocol/         # message structs (source of truth for the wire format)
web/
  shared/protocol.ts         # TS mirror of internal/protocol — keep in sync by hand
  phone/                     # Vite app: permission, grid tap, motion stream
  dashboard/                 # Vite app: node map, zone status, briefing, replay
arduino/sign/sign.ino
recordings/                  # labelled JSONL runs for tuning + replay
```

Suggested Go deps: `github.com/coder/websocket`, `github.com/jackc/pgx/v5`, `google.golang.org/genai`. ElevenLabs is plain HTTP; no SDK needed.

## Wire protocol (JSON over WebSocket)

Positions are **venue metres**: origin at the top-left of the venue map, x to the right, y down. Venue size comes from `GET /api/venue` / `GET /api/config` (default 24 × 16 m).

Phone → server
```jsonc
{ "type": "hello", "id": "<random uuid>", "x": 3.2, "y": 7.5, "ua": "iPhone" }   // x,y optional
// hello may instead carry "lat","lon","acc" (GPS), or legacy "row","col" (old phones/recordings:
// x = legacyX0 + col*0.6, y = venueH/2 + row*0.6)
{ "type": "pos", "x": 3.4, "y": 7.1 }                             // moved by hand (or a simulated phone walked); clamped to the venue
{ "type": "gps", "lat": 49.2781, "lon": -122.9199, "acc": 6.5 }   // ~1 Hz or on >1 m moves; converted to metres on arrival
{ "type": "pong", "t0": 1728000000000, "t1": 1728000000004 }     // reply to ping, t1 = phone clock
{ "type": "m", "t": 1728000000123, "ax": 0.12, "ay": -0.03, "az": 0.01, "rot": 4.2 }
// "m" is a 100 ms summary of ~6 raw samples (mean accel per axis, max rotation rate)
```
GPS needs a venue geo-anchor (`PUT /api/venue` with `geo:true`); without one the server ignores `gps` (logged once per phone) and the phone falls back to manual placement. Fixes with `acc` > `gpsMaxAcc` (25 m) are ignored; accepted fixes are smoothed (EMA, weight 1/(1+acc/10)). Fixes outside the venue are clamped and the node is `outside` (no zones, clusters or neighbours).

Server → phone
```jsonc
{ "type": "ping", "t0": 1728000000000 }
{ "type": "state", "node": "ok" | "handling", "zone": "calm" | "yellow" | "red" }  // zone = level of the worst zone containing the phone
```

Server → dashboard (broadcast ~10 Hz)
```jsonc
{ "type": "snapshot", "t": ..., "mode": "live|replay|sim", "replay": "...", "progress": 0.4, "recording": "...",
  "venue": { "w": 24, "h": 16 },
  "nodes": [ { "id": "...", "x": 3.2, "y": 7.5, "status": "<node status>", "sway": 0.4, "rtt": 38, "offset": -12, "age": 80,
               "ua": "...", "zone": "A", "acc": 6.5, "src": "gps|manual", "outside": false } ],
  "zones": [ { "id": "A", "name": "Zone A", "level": "calm|yellow|red", "score": 0.12, "poly": [[x,y],...], "custom": false, "sens": "normal|high" } ],
  "waves": [ { "from": "<id>", "to": "<id>", "lagMs": 220, "corr": 0.8 } ],     // direction of travel
  "links": [ ["idA", "idB"], ... ],                                              // every neighbour pair the detector compares
  "clusters": [ { "id": "c3", "x": 5.1, "y": 6.0, "r": 1.8, "count": 7, "density": 0.69, "people": 7,
                  "trend": "forming|steady|dispersing", "level": "calm|yellow|red" } ],
  "stats": { "phones": 8, "msgPerSec": 79, "medianRtt": 41 },
  "sim": { "bodies": [[x, y, pressure, hasPhone], ...], "t": 42.3, "action": "surge" } }  // mode "sim" only, see below
{ "type": "alert", "kind": "wave|density", "zone": "B", "level": "red", "brief": "...", "audioUrl": "/audio/123.mp3" }
```
`node.zone` = first zone containing the phone ("" if none or outside). `density` = phones per m² of the cluster disc (count / max(π r², 1)); `people` = count / `participation`, and the cluster level uses the estimated density = max(disc density, peak local density) ÷ participation, where the peak local density is phones within 1.5 m of a member ÷ 7.07 m², at the 90th percentile of the members (so a big crowd with a packed front reads as packed).

Zones: with no staff-drawn areas, the venue is split into `zoneCols` × `zoneRows` rectangles (default 2 × 1: A left half, B right half). With areas, zones = the areas (`custom: true`) plus `"rest"` ("Rest of venue", phones in no area). A phone may be in several areas. `sens: "high"` halves that zone's thresholds (`highRiskFactor`) and hold time.

HTTP: `GET /api/config` → `{venueW, venueH, geo, yellow, red, neighbourRadius}`; `GET/PUT /api/areas` (array of `{id, name, sens, poly}`; ≥ 3 points, unique ids, names ≤ 40 chars, ≤ 50 areas, points clamped; saved to `data/areas.json`); `GET/PUT /api/venue` (`{w, h, lat, lon, bearing, geo}`; lat/lon = the map's top-left corner, bearing = degrees clockwise from north of the map's up; saved to `data/venue.json`; initial values from `VENUE_W/H/LAT/LON/BEARING`); `GET /api/node/{id}` (x, y, acc, src, outside, samples …).

**Crowd simulation** (`internal/crowdsim`, a third data source next to live and replay). While it runs, `mode` is `"sim"`, the dashboard shows the sim pipeline (its phones are ordinary `nodes`, ua `"sim"`), live phones keep streaming into the live pipeline underneath, and `snapshot.sim` carries every simulated person as `[x, y, pressure N/m, hasPhone 0|1]` (x, y to 2 decimals, pressure an integer), plus `t` (s since start) and the last behaviour `action`. Starting a replay stops the sim and vice versa.
- `POST /api/sim/start` `{"people":250,"participation":0.6,"scenario":"concert"}` (all optional, these are the defaults; people 1–1000, participation (0, 1]) → `{"mode":"sim"}`; 400 `{"error"}` on bad input, 409 if already running.
- `POST /api/sim/stop` → `{"mode":"live"}`; 409 if not running.
- `POST /api/sim/action` `{"type": "calm|stage|surge|attract|shove|exit|disperse|spawn", ...}` → 200 `{"ok":true}` or 400 `{"error"}` (also when not running). Fields: `surge` `strength` 0..1 (default 0.7); `attract` `x, y`; `shove` `x, y, dx, dy` (direction, any length; optional `strength` 0..1, default 0.7); `exit` `id, open`; `spawn` `x, y, n` (1–200). x, y must be inside the venue.
- `GET /api/sim` → `{"running", "t", "people", "phones", "participation", "action", "exits": [{"id","name","x0","y0","x1","y1","open"}], "walls": [[x0,y0,x1,y1], ...], "truth": {"maxDensity", "maxPressure", "crushing", "dangerAt", "alertAt", "leadSeconds"}}`. Not running: only `running:false`, `exits`, `walls` (the venue layout). Truth times are s since start, `null` until they happen; `leadSeconds` = `dangerAt − alertAt` (positive = Pulse warned first) once both have happened. `maxDensity` = highest people within 1 m ÷ π m²; `crushing` = people ≥ 1600 N/m or > 6 /m²; `dangerAt` = start of the first ≥ 1 s stretch with ≥ 3 people ≥ 1600 N/m or ≥ 5 people > 6 /m²; `alertAt` = Pulse's first red alert (wave or density) in the sim pipeline.
- Exit ids: `exit-bl`, `exit-br` (bottom corners), `exit-l`, `exit-r` (side walls). `walls` are the static walls including the stage pit (barrier 1.5 m from the top across the middle 60 %); exits are drawn from `exits` (closed = a wall).

## Clock sync

On connect, send 8 pings 100 ms apart. For each: `rtt = now - t0`, `offset = t1 - (t0 + rtt/2)`. Keep the offset from the sample with the smallest RTT. Re-sync every 30 s. Corrected time = `t - offset`. Show RTT per node on the dashboard (judges like seeing real distributed-systems numbers).

## Phone page (web/phone)

1. Big "Join" button → `DeviceMotionEvent.requestPermission()` on iOS (must be inside the tap handler; needs HTTPS).
2. Grid picker: tap your spot (e.g. 1 row × 8 cols for the line demo, configurable from server).
3. Sample `devicemotion` (`acceleration`, fall back to `accelerationIncludingGravity` minus a running mean), summarize every 100 ms, send `m`.
4. Screen shows: connected dot, "hold phone flat against your chest", and colour feedback from `state`.
5. Use Wake Lock API so the screen stays on. Reconnect automatically.

Axis note: held upright, flat against the chest, the phone's **x** is left/right and **z** is forward/back (both horizontal, which is what we want). **y** is up/down (walking, jumping). Detection uses x and z.

## Dashboard (web/dashboard) — the laptop screen

This is the most visible part. Full-screen, dark, readable from across a table.

**Main panel: the network map**
- Each phone is a **node** (circle) placed by its grid row/col. Draw in SVG (or canvas if it gets slow). No force layout: positions are the crowd layout.
- Faint **edges** link grid neighbours. When the detector finds a travelling wave between two phones, animate a **pulse along that edge** in the direction of travel.
- Zones are shaded rectangles behind the nodes, tinted by zone level.
- Node size or ring thickness = current sway strength.
- Hover a node: ID (short), RTT, clock offset, last reading age.

**Node colours**

| Status | Colour | Meaning |
|---|---|---|
| `connecting` | grey outline | joined, clock sync in progress |
| `ok` | green | streaming, calm |
| `handling` | blue | phone being handled (high rotation), readings ignored |
| `swaying` | yellow | this phone shows sustained sideways sway |
| `wave` | red | this phone is part of a detected travelling wave |
| `stale` | dim grey | no data for > 2 s |

Colour changes should fade over ~200 ms; red nodes pulse.

**Side panel**
- Zone list with level and score (sparkline of score over last 60 s).
- Latest Gemini briefing in large text; plays the ElevenLabs audio when it arrives.
- Alert log (timestamped).
- Counters: phones connected, messages/sec, median RTT.
- Controls: **Live / Replay** toggle, recording picker, "Record run" button with a label field, "Test alert" button.

## Detection (internal/detect)

Per phone, on a ring buffer of the last ~30 s at 10 Hz (clock-corrected):
1. **Handling filter:** if rotation rate > threshold, mark `handling`, drop readings until it settles for 1 s.
2. **Low-pass** x and z (sway is slow, roughly 0.2–1 Hz). Simple exponential moving average or a 2-pole Butterworth.
3. **Horizontal only:** `h = projection onto dominant horizontal direction` (or just use x for the line demo; generalize later).
4. **Sway score:** RMS of filtered `h` over the last 5 s. Above threshold → `swaying`.

Per pair of grid neighbours (A, B):
5. Cross-correlate their last 5–8 s of `h`, lags −1.5 s…+1.5 s. Record best lag and correlation.
6. **Travelling wave edge** if correlation > threshold AND |lag| between ~100 ms and ~1.2 s. Lag ≈ 0 with high correlation = everyone moving together = dancing/jumping → not a wave.

Per zone:
7. Zone score = fraction of neighbour edges showing a wave with **consistent direction**, smoothed over ~20 s.
8. Yellow when score > Y for N seconds; red when > R. Hysteresis: red clears only below R − margin, yellow below Y − margin.

All thresholds live in one config struct, loadable from a JSON file, so they can be tuned without recompiling. Write table-driven tests that run each labelled recording through the detector and assert the expected outcome.

## Simulator (server/cmd/sim) — critical when solo

Connects N fake phones over the real WebSocket, with a fake clock offset and jitter each. Scenarios:
- `calm`: small independent noise
- `walk`: vertical bounce at each phone's own cadence
- `dance`: strong vertical + horizontal, all phones in phase
- `handle`: random phones spike rotation
- `shove`: one push travels once, then stops
- `wave`: repeating sideways push travelling down the line with ~250 ms lag per person, growing over 60 s

`go run ./server/cmd/sim -n 8 -scenario wave`. This lets you build the whole dashboard and detector at your desk, then confirm with real phones.

## Sponsors

| Sponsor | Use | Notes |
|---|---|---|
| **Tiger Data** | Hypertable `readings(time, phone_id, zone, ax, ay, az, rot)`, `alerts(time, zone, level, brief)`. Continuous aggregate per zone per second. Replay reads from here. | Batch inserts (e.g. every 500 ms with `CopyFrom`). If DB unreachable, write JSONL to `recordings/` instead. |
| **Gemini** | On yellow→red (rate-limited, max one per zone per 30 s): send zone, duration, score trend, wave direction; get 2-sentence briefing + suggested action. Optional "ask about the last 10 minutes" box on dashboard. | Timeout 5 s; on failure use a template sentence. |
| **ElevenLabs** | Turn the briefing into an mp3, serve at `/audio/<id>.mp3`, dashboard plays it. | Pre-generate one fallback clip ("Zone B, crowd waves building") at startup. |
| **.Tech** | Domain pointing at the Cloudflare Tunnel for the QR code. | Do this early; DNS can be slow. |

Secrets via env vars: `TIGER_DATABASE_URL`, `GEMINI_API_KEY`, `ELEVENLABS_API_KEY`, `ELEVENLABS_VOICE_ID`, `SIGN_URL`. Never commit them; provide `.env.example`.

## Arduino Uno R4 WiFi sign

Sketch runs a tiny HTTP server: `GET /level?v=calm|yellow|red&zone=B`. Calm = blank/heartbeat, yellow = "!" , red = flashing arrow/"STOP". Server calls it on level change with a 1 s timeout. Stretch: force sensor on a "barrier" posting readings back as a second signal.

## Demo script (~60 s)

1. QR on screen → teammates/judges join, stand in a line, phones flat on chest. Nodes appear green on the map.
2. Normal movement: stays green. Someone checks their phone: that node goes blue.
3. Everyone jumps together: nodes may go yellow but no wave edges, zone stays calm. (This is the "won't dancing trigger it?" answer.)
4. Push the end of the line repeatedly: pulses race along the edges, nodes go red, zone goes red, voice briefing plays, Arduino flashes.
5. Safety net: Replay toggle plays a recorded good run through the same pipeline.

## Build order (solo)

1. Go server + phone page streaming over the tunnel; dashboard shows nodes appearing. **Test on a real iPhone first.**
2. Clock sync + RTT shown per node.
3. Simulator with all scenarios.
4. Node colours driven by per-phone status (handling, sway).
5. Pairwise wave detection, edge pulses, zone levels with hysteresis.
6. JSONL recording + replay. Record real labelled runs as soon as possible.
7. Tiger Data storage.
8. Gemini briefing → ElevenLabs audio.
9. Arduino sign.
10. Polish dashboard, tune thresholds on recordings, rehearse the pitch.

## Honest limits (for the pitch)

- A web page only streams while open; a real event would build this into its official app.
- Phone-to-phone relay when the cell network jams needs a native app.
- Locating phones in a real crowd (GPS, ticket section) is the hard real-world problem; the demo uses tap-your-spot.
- A handful of testers proves the method, not the thresholds for 50,000 people.
