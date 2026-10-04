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
{ "type": "state", "node": "ok" | "handling", "zone": "calm" | "yellow" | "red",  // zone = level of the worst zone containing the phone
  "x": 11.2, "y": 7.4, "w": 24, "h": 16,          // always: the phone's position (to 0.1 m) and the venue size, for a mini-map
  "bearing": 30,                                  // only when the venue is GPS-anchored
  "move": { "dx": 0.71, "dy": -0.71, "to": "Left side exit", "reason": "push" | "density" } }  // only while in danger
```
Sent every 500 ms when it changes (by value), else every 3 s. **Guidance (`move`)** is set while the phone is in danger: a zone containing it is red for any reason (detector, area density rule, capacity rule), it is a member of a yellow/red cluster, or its node status is `wave`; it disappears when safe. `dx, dy` is a unit vector in venue coordinates (x right, y down), rounded to 2 decimals. Direction (`crowd.Guide`): down the gradient of a Gaussian KDE (σ 1.5 m) of every non-stale, connected, inside phone; where the gradient is flat or points into a wall/stage edge or out of the venue, the least dense unblocked of 16 directions 2 m away (or the unblocked one closest to the gradient's way). `reason: "push"` when the phone is on a wave edge (direction = sum of its wave edges' travel vectors) or its zone is red from the wave detector (zone direction): then sideways to the push on the less dense side, plus 0.4 × the push direction (diagonal, never against it). Inside a custom area that a staff rule turned red, the way out (toward the nearest point of the area's outline, weight 2) is blended with the density direction and always wins (reason `density`). Then the nearest open exit within ±60° and 25 m is blended in at equal weight and named in `to` (else `to: "less crowded side"`). Smoothed per phone (vector EMA, τ 2 s); the arrow shown only changes when the smoothed direction is > 30° from it. Exits: venue `layout.exits` (walls: `layout.walls` + the stage outline) for live and replay; for the sim pipeline the sim geometry built from the same layout (`crowdsim.LayoutGeometry`), every exit assumed open. Sim phones get guidance too (nothing reads it).

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
                  "trend": "forming|steady|dispersing", "level": "calm|yellow|red", "rate": 6.2, "eta": 12.5 } ],
  "stats": { "phones": 8, "msgPerSec": 79, "medianRtt": 41, "detectMs": 0.84, "snapshotBytes": 5120 },
  "sim": { "bodies": [[x, y, pressure, hasPhone], ...], "t": 42.3, "action": "surge" } }  // mode "sim" only, see below
{ "type": "alert", "id": "a1728000000000-7", "t": ..., "kind": "wave|density|rule", "early": true, "zone": "B", "level": "red", "score": 0.66,
  "brief": "<headline> <action>", "headline": "Zone B: crowd waves travelling left to right.", "action": "Stop entry to Zone B and open relief exits now.",
  "audioUrl": "/audio/123.mp3", "test": false, "status": "open|ack|resolved", "ackAt": ..., "resolvedAt": ..., "escalated": true }
{ "type": "alerts", "alerts": [ <alert>, ... ] }   // sent once on connect: the last 50 alerts, latest state of each
```
**Alerts are incidents; the dashboard upserts by `id`.** An incident = one data source (live/replay/sim) × zone × kind. A zone leaving calm opens one (`status: open`); until staff resolve it, every later change of that zone and kind updates the same `id`: `level` is the worst it reached, `t` when it reached it, `score` the score at that level (a zone going red → yellow → calm keeps the card red; a new push before it's resolved lands on the same card). The briefing arriving, ack, resolve and escalation are updates of the same `id`. A zone returning to calm is also sent as its own notice (new `id`, `level: calm`, `status: resolved`) so the timeline shows it without a card. After resolve, the next change opens a new `id`; resolving never touches detector levels. Briefings: Gemini returns JSON `{headline, action}` (structured output); the template fills both; `brief` = headline + " " + action. The briefing is requested when an incident first goes red (max one per zone per 30 s). **Escalation:** red, still `open`, `-escalate-after` (default 60 s, env `PULSE_ESCALATE_AFTER`, 0 = never) after it went red → `escalated: true`, re-broadcast, voice regenerated as "Still unacknowledged. <brief>" (or the old clip reused), sign and its light forced red for 8 s; once per alert. Test alerts are incidents too (`test: true`) and can escalate.
**Early warning** (`early`, density incidents only): `cluster.rate` = least-squares slope of the cluster's estimated density over the last 8 s (needs ≥ 5 s of samples; people/m² per minute; omitted when 0). A cluster with little history (new ID after clusters merged or split) inherits the older history of the track most of its phones came from. `cluster.eta` = (densityDanger − est) ÷ rate in seconds, only when rate > 0, est < danger and eta ≤ `earlyWarnS` (config, default 30, 0 = off). When eta is set and est ≥ `earlyFloor` × danger (default 0.55 → 2.2 people/m²) for 500 ms, a calm cluster goes yellow at once (it stays yellow while the projection holds) and the density incident is raised with `early: true`; `early` is cleared when the incident goes red. An early incident gets its own briefing straight away, worded as a projection ("Stage front: about 40 people packing in fast; at this rate it reaches a dangerous 4 per m² in about 12 s." / "Open space ahead of them now."; Gemini gets `earlyWarning`, `secondsUntilDangerous`, `densityRisePerMinute`, `dangerDensity`); if it then goes red it is briefed again regardless of the 30 s cooldown, and a late early briefing never overwrites a red one. Thresholds, hold and hysteresis of the normal levels are unchanged.
`stats.detectMs` = mean duration of the active pipeline's step (detector + clusters) over the last 5 s, 2 decimals; `stats.snapshotBytes` = size of the previous encoded snapshot.
`node.zone` = first zone containing the phone ("" if none or outside). `density` = phones per m² of the cluster disc (count / max(π r², 1)); `people` = count / `participation`, and the cluster level uses the estimated density = max(disc density, peak local density) ÷ participation, where the peak local density is phones within 1.5 m of a member ÷ 7.07 m², at the 90th percentile of the members (so a big crowd with a packed front reads as packed).

Zones: with no staff-drawn areas, the venue is split into `zoneCols` × `zoneRows` rectangles (default 2 × 1: A left half, B right half). With areas, zones = the areas (`custom: true`) plus `"rest"` ("Rest of venue", phones in no area). A phone may be in several areas. `sens: "high"` halves that zone's thresholds (`highRiskFactor`) and hold time.

HTTP: `GET /api/config` → `{venueW, venueH, geo, yellow, red, neighbourRadius}`; `GET/PUT /api/areas` (array of `{id, name, sens, poly, light, rules}`; ≥ 3 points, unique ids, names ≤ 40 chars, ≤ 50 areas, points clamped; saved to `data/areas.json`); `GET/PUT /api/venue` (`{w, h, lat, lon, bearing, geo, template, floorplan, layout}`; lat/lon = the map's top-left corner, bearing = degrees clockwise from north of the map's up; saved to `data/venue.json`; initial values from `VENUE_W/H/LAT/LON/BEARING`); `GET /api/node/{id}` (x, y, acc, src, outside, samples …); `GET /api/edge?from=<id>&to=<id>` → `EdgeExplain` (below).

- **Why did it fire?** `GET /api/edge?from=&to=` explains one neighbour pair from the active pipeline's latest detector step (live, replay or sim), in either order (the answer uses the detector's `from`/`to`: smaller x, then y, then id). 400 without both ids, 404 if the pair isn't a neighbour pair in that step. `{from, to, stepMs, a, b, lags, corr, lagMs, peak, second, wave, checks}`: `a`, `b` = the band-passed horizontal traces on the 50 ms grid over the 6 s correlation window (oldest first, m/s², 3 decimals, `null` = no valid sample); `lags` (ms, −maxLag…+maxLag) and `corr` (|r| per lag, `null` = too little overlap; positive lag = b moves after a); `lagMs` = the edge's sub-step lag (or the curve's own peak if the pair was gated out before correlating), `peak`, `second` (best separate peak, ≥ 0), `wave` = the detector's verdict. `checks` (in order): Both phones moving (sway vs `edgeMinSway`), Not handling, Strong correlation (`corrThreshold`), Wave-like lag (120–1200 ms), Unambiguous peak (`peakMargin`), Not vertical (Mexican-wave veto), Part of a chain of ≥ `minChain`; each `{name, pass, detail}`. The detector only keeps the latest step's pair list and per-phone flags; the curve is recomputed on request from the phones' resampled traces, which stay in place until the next step.

- **Venue extras.** `template`: preset name (≤ 40 chars). `floorplan`: read-only, true while an image is stored. `layout`: `{"stage": [[x,y],...], "exits": [{"id","name","x0","y0","x1","y1"}], "walls": [[x0,y0,x1,y1],...]}` in venue metres; points clamped to the venue (to the cm), stage 0 or 3–64 points, ≤ 32 exits (missing/duplicate ids become `exit-N`, names ≤ 40 chars), ≤ 128 walls, zero-length segments dropped. When a simulation starts, a layout with walls or exits replaces the sim's default geometry: the venue outline plus the layout's walls, a gap cut wherever an exit lies along a wall (exits within 0.3 m of the venue edge snap onto it), the stage outline as a wall (people press toward its largest-y edge). `GET /api/sim` shows the same geometry when not running.
- **Floor plan.** `POST /api/venue/floorplan`: raw image body, ≤ 8 MB (413), type sniffed with `http.DetectContentType` and only PNG/JPEG/WebP accepted (415); stored as `data/floorplan` + `data/floorplan.json` (`{"type"}`) → the venue (`floorplan: true`). `GET` serves it with its type and `Cache-Control: no-store` (404 if none); `DELETE` removes it → the venue. `POST /api/venue/floorplan/analyze` → `{w, h, layout, notes, confidence: low|medium|high}`: Gemini vision request (inline image, `responseMimeType: application/json` + response schema, 20 s timeout). Gemini answers on a 0–1000 grid over the whole image plus the metres the image spans; the server converts to venue metres (origin = image top-left), clamps w/h to 2–5000 m and every point into w × h, caps counts, fixes h to the image's aspect ratio (PNG/JPEG) if Gemini's differs by > 10 %, and defaults an unknown confidence to low. 503 `{"error":"Gemini isn't configured (GEMINI_API_KEY)"}` without a key, 404 without a plan, 502 `{"error"}` on Gemini failure. Saves nothing; the dashboard applies it with `PUT /api/venue`.
- **Area rules** (`rules` on an area, all optional): `{"density": 4, "densityHoldS": 5, "push": true, "maxPhones": 200, "message": "≤ 140 chars", "notify": {"sign": true, "light": true, "voice": true}}`. `density` (0 = off): estimated people/m² inside the area = max(phones ÷ area (≥ 1 m²), peak local density) ÷ participation, the clusters' estimate, over phones that aren't stale, outside or disconnected; above `density` for `densityHoldS` (0 = 5 s) → red, above 75 % → yellow, each clearing 10 % (`margin`) below its threshold (the zone/cluster state machine). `maxPhones` (0 = off): more phones inside than this for over 3 s → red; calm as soon as it's back at or under. The area's rule level is the worse of the two, raises `kind: "rule"` alerts (score = estimated people/m², or phones for capacity) and merges into the zone's `level` in snapshots, phone states, signs and lights. `push: false` → the zone's wave score stays 0 and its detector level calm (`detect.ZoneDef.NoPush`). `message` replaces the briefing's `action` verbatim (template and Gemini, which is told "use this action verbatim"; the server overwrites Gemini's action with it), for every alert kind in that area. `notify`: `sign: false` leaves the area out of the worst-zone sign's choice, `light: false` makes its light count it as calm, `voice: false` skips generating audio (the dashboard may still use browser speech for an alert without `audioUrl`).
- **Alerts:** `POST /api/alerts/{id}/ack` (open → ack, sets `ackAt`), `POST /api/alerts/{id}/resolve` (sets `resolvedAt`, closes the incident) → the updated alert, also broadcast to dashboards as an `alert` message; 404 `{"error"}` for an unknown id (or one that fell out of the 50-alert log).
- **Boards:** `GET /api/hardware` entries also carry `x, y` (where staff placed the board, from `data/hardware.json`; absent = not placed), `beacon` (the board's `/pulse` `name`, e.g. `PULSE-A`) and `peers: [{"name","rssi","dist","age","mapDist"}]` passed through from `/pulse` (`mapDist` = the map distance between the two boards, only when both are placed and each hears the other). `PUT /api/hardware/{key}/pos` `{x, y}` (key `sign` or a light letter, case-insensitive; clamped to the venue) → the hardware list; 404 for an unknown key, 400 for a bad body. Zone-light firmware `/pulse`: `{"kind","level","rssi","uptime","ble":{…},"name":"PULSE-B","mac":"C72C","peers":[{"name":"PULSE-A","rssi":-63,"dist":2.4,"age":3}]}`.

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
