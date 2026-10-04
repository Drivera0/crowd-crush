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
// hello may carry "at": "<tower key>" (joined through a tower's check-in QR code: placed next to that tower, see Tower check-in)
// hello may instead carry "lat","lon","acc" (GPS), or legacy "row","col" (old phones/recordings:
// x = legacyX0 + col*0.6, y = venueH/2 + row*0.6)
{ "type": "pos", "x": 3.4, "y": 7.1 }                             // moved by hand (or a simulated phone walked); clamped to the venue
{ "type": "gps", "lat": 49.2781, "lon": -122.9199, "acc": 6.5 }   // ~1 Hz or on >1 m moves; converted to metres on arrival
{ "type": "pong", "t0": 1728000000000, "t1": 1728000000004 }     // reply to ping, t1 = phone clock
{ "type": "m", "t": 1728000000123, "ax": 0.12, "ay": -0.03, "az": 0.01, "rot": 4.2, "g": [0.05, -0.83, 0.56] }
// "m" is a 100 ms summary of ~6 raw samples (mean accel per axis, max rotation rate)
// "g" (optional): unit gravity vector in the device frame, 2 decimals: which way is down as the phone sees it
```
**Gravity (`g`).** The server levels every reading with it (below, Detection), so the phone can be carried at any tilt. The server holds a phone's last `g`: the phone page sends it when it has turned more than 3° since the last one sent, at least once a second, and with the first `m` of every connection. A phone that never sends `g` (old pages, old recordings, `cmd/sim`, `cmd/loadtest`) is taken to be upright against the chest (x, z horizontal, y vertical) and goes through the detector exactly as before. Only the axis of `g` is used: `−g` gives the same result, so the iOS/Android sign difference cannot hurt (the page still corrects it so `g` points down on both). A `g` that isn't three finite numbers with length > 0.2 is ignored. Recordings (`"g"` on `m` records) and Tiger readings (`gx, gy, gz`, NULL when absent) store it when the message carried it, so replays level the same way.
GPS needs a venue geo-anchor (`PUT /api/venue` with `geo:true`); without one the server ignores `gps` (logged once per phone) and the phone falls back to manual placement. Fixes with `acc` > `gpsMaxAcc` (25 m) are ignored; accepted fixes are smoothed (EMA, weight 1/(1+acc/10)). A fix off the venue by more than `outsideAccFactor` (1) × its accuracy radius is clamped and the node is `outside` (no zones, clusters or neighbours); nearer than that it is someone inside whose GPS is off, and the fix is mirrored back in at the edge it crossed (`detect.Config.Place`). A GPS-placed phone keeps its accuracy radius (`node.acc`), which the detector and the density tracker use: see **Rough positions** under Detection.
**Unplaced phones.** A hello with no position at all (no `x`/`y`, no `row`/`col`: a GPS phone before its first usable fix) is never put on a default spot. With the demo spot on (below) it is lined up there; otherwise it is **unplaced**: connected and streaming, but treated like `outside` (no zone, cluster, neighbour pair, density or guidance), sent to the dashboard with `unplaced: true` and `x, y` 0 (the dashboard keeps it off the map and shows "N phones locating…"), and nothing about it is written to recordings or continuous storage. Its first position places it (an accepted `gps` fix, a `pos`, a hello with `x`/`y`, staff dragging or lining it up) and is recorded as its hello. A hello that does carry `row`/`col` (old phone pages, cell 0,0 included) still lands on its legacy cell, and old recordings replay as before. Simulated phones with GPS realism start unplaced the same way.

Server → phone
```jsonc
{ "type": "ping", "t0": 1728000000000 }
{ "type": "state", "node": "ok" | "handling", "zone": "calm" | "yellow" | "red",  // zone = level of the worst zone containing the phone
  "x": 11.2, "y": 7.4, "w": 24, "h": 16,          // always: the phone's position (to 0.1 m) and the venue size, for a mini-map
  "bearing": 30,                                  // only when the venue is GPS-anchored
  "name": "Blue Otter", "color": "#3b82f6",       // the phone's generated name and colour, as on the dashboard map
  "sim": true,                                    // only while this state comes from the crowd simulation (a drill)
  "move": { "dx": 0.71, "dy": -0.71, "to": "Left side exit", "reason": "push" | "density", "conf": 1 } }  // only while in danger
{ "type": "shake" }                               // the server just saw this phone being shaken (sent once, when a shake starts)
```
`zone` is the worst of the zones containing the phone and of the yellow/red cluster it is a member of (standing in a packed cluster turns the phone yellow/red even when its zone as a whole is calm).
**Names.** Every live phone gets a generated name and colour from its random session id (`internal/names`: colour + animal, FNV hash of the id; stepped to a free colour, then a free animal, when another phone known to the server already has it, so a handful of phones each get their own colour). The server remembers id → name while it runs, so a reconnect keeps it. Nothing is asked for; simulated and replayed phones have none.
**Shake.** Two "hard" motion summaries within 600 ms (|a| ≥ 5 m/s² with rot ≥ 120 °/s, or |a| ≥ 14 m/s²) are a shake: the node carries `shake: true` until 1.5 s after the last one, the phone is sent `{"type":"shake"}` once, and while it lasts the detector is given a rotation above `handlingRot`, so a shaken phone is `handling` and never part of a wave or an alert.
Sent every 500 ms when it changes (by value), else every 3 s. **Guidance (`move`)** is set while the phone is in danger: a zone containing it is red for any reason (detector, area density rule, capacity rule), it is a member of a yellow/red cluster, or its node status is `wave`; it disappears when safe. `dx, dy` is a unit vector in venue coordinates (x right, y down), rounded to 2 decimals. Direction (`crowd.Guide`): down the gradient of a Gaussian KDE (σ 1.5 m) of every non-stale, connected, inside phone; where the gradient is flat or points into a wall/stage edge or out of the venue, the least dense unblocked of 16 directions 2 m away (or the unblocked one closest to the gradient's way). `reason: "push"` when the phone is on a wave edge (direction = sum of its wave edges' travel vectors) or its zone is red from the wave detector (zone direction): then sideways to the push on the less dense side, plus 0.4 × the push direction (diagonal, never against it). Inside a custom area that a staff rule turned red, the way out (toward the nearest point of the area's outline, weight 2) is blended with the density direction and always wins (reason `density`). Then the nearest open exit within ±60° and 25 m is blended in at equal weight and named in `to` (else `to: "less crowded side"`). Smoothed per phone (vector EMA, τ 2 s); the arrow shown only changes when the smoothed direction is > 30° from it. Exits: venue `layout.exits` (walls: `layout.walls` + the stage outline) for live and replay; for the sim pipeline the sim geometry built from the same layout (`crowdsim.LayoutGeometry`), every exit assumed open. Sim phones get guidance too (nothing reads it).

Server → dashboard (broadcast ~10 Hz)
```jsonc
{ "type": "snapshot", "t": ..., "mode": "live|replay|sim", "replay": "...", "progress": 0.4, "recording": "...",
  "venue": { "w": 24, "h": 16 },
  "nodes": [ { "id": "...", "x": 3.2, "y": 7.5, "status": "<node status>", "sway": 0.4, "rtt": 38, "offset": -12, "age": 80,
               "ua": "...", "zone": "A", "acc": 6.5, "src": "gps|manual|tower", "outside": false,
               "name": "Blue Otter", "color": "#3b82f6", "shake": true, "real": true, "unplaced": true } ],  // name/color: real phones only; shake, real (mode sim: a real phone in the simulated crowd), unplaced: only when true
  "zones": [ { "id": "A", "name": "Zone A", "level": "calm|yellow|red", "score": 0.12, "poly": [[x,y],...], "custom": false, "sens": "normal|high" } ],
  "waves": [ { "from": "<id>", "to": "<id>", "lagMs": 220, "corr": 0.8 } ],     // direction of travel; "motion": true when the pair was found by motion (GPS-placed phones)
  "links": [ ["idA", "idB"], ... ],                                              // every neighbour pair the detector compares
  "clusters": [ { "id": "c3", "x": 5.1, "y": 6.0, "r": 1.8, "count": 7, "density": 0.69, "people": 7, "est": 4.6,
                  "trend": "forming|steady|dispersing", "level": "calm|yellow|red", "rate": 6.2, "eta": 12.5 } ],
  "status": { "level": "red", "score": 0.89, "zone": "n2549bj", "where": "Stage front", "kind": "wave|density|rule|early", "density": 5.9 },
  "stats": { "phones": 8, "msgPerSec": 79, "medianRtt": 41, "detectMs": 0.84, "snapshotBytes": 5120 },
  "sim": { "bodies": [[x, y, pressure, hasPhone], ...], "t": 42.3, "action": "surge" } }  // mode "sim" only, see below
{ "type": "alert", "id": "a1728000000000-7", "t": ..., "kind": "wave|density|rule", "early": true, "zone": "B", "level": "red", "score": 0.66,
  "brief": "<headline> <action>", "headline": "Zone B: crowd waves travelling left to right.", "action": "Stop entry to Zone B and open relief exits now.",
  "audioUrl": "/audio/123.mp3", "test": false, "status": "open|ack|resolved", "ackAt": ..., "resolvedAt": ..., "escalated": true,
  "ackBy": "Sam", "resolvedBy": "Ana", "note": "False alarm: encore." }   // audit trail, from the ack/resolve bodies
{ "type": "alerts", "alerts": [ <alert>, ... ] }   // sent once on connect: the last 50 alerts, latest state of each
```
**Alerts are incidents; the dashboard upserts by `id`.** An incident = one data source (live/replay/sim) × zone × kind. A zone leaving calm opens one (`status: open`); until staff resolve it, every later change of that zone and kind updates the same `id`: `level` is the worst it reached, `t` when it reached it, `score` the score at that level (a zone going red → yellow → calm keeps the card red; a new push before it's resolved lands on the same card). The briefing arriving, ack, resolve and escalation are updates of the same `id`. A zone returning to calm is also sent as its own notice (new `id`, `level: calm`, `status: resolved`) so the timeline shows it without a card. After resolve, the next change opens a new `id`; resolving never touches detector levels. Briefings: Gemini returns JSON `{headline, action}` (structured output); the template fills both; `brief` = headline + " " + action. The briefing is requested when an incident first goes red (max one per zone per 30 s). **Escalation:** red, still `open`, `-escalate-after` (default 60 s, env `PULSE_ESCALATE_AFTER`, 0 = never) after it went red → `escalated: true`, re-broadcast, voice regenerated as "Still unacknowledged. <brief>" (or the old clip reused), sign and its light forced red for 8 s; once per alert. Test alerts are incidents too (`test: true`) but never escalate, and `POST /api/ask` leaves them out of the history it gives Gemini (only a drill count). That history starts with the live situation (`status` and the active incidents) and names places, never area ids.
**Overall status** (`snapshot.status`, `app/status.go`): the one status the console shows. Candidates: each zone's wave detector (score vs the zone's thresholds), each area rule (density: `est` vs its limit; capacity: estimated people vs `maxPhones`), each cluster (`est` vs densityWatch/densityDanger; `kind: "early"` while an early warning holds it yellow). The worst by level, then risk: risk maps a value v with yellow/red thresholds Y < R to 0.5·v/Y below Y, 0.5 + 0.3·(v−Y)/(R−Y) up to R, 0.8 + 0.2·min(1, (v−R)/R) above, clamped into the band of the level its state machine is at (calm < 0.5 ≤ yellow < 0.8 ≤ red). `score` = that risk (2 decimals; when calm, the highest risk anywhere). `zone`/`where`/`kind` only when not calm; `where` = the drawn area holding the hotspot (a cluster's densest spot) if any, else the zone's name, never an id; `density` = est for density, early and density-rule reasons.
**Attribution:** cluster (density) alerts, sign levels and the status use the zone of the cluster's densest spot (`PeakX, PeakY`), preferring a drawn area over "rest"/default zones. Briefings get `nearestExit`: the exit nearest the hotspot (density: densest spot; rule: the area's densest spot; wave: the wave phones' centre, else the zone's), from the venue layout (sim: the sim geometry, every exit assumed open), named in the template's action and given to Gemini; an area's staff `message` still replaces the action.
**Early warning** (`early`, density incidents only): `cluster.rate` = least-squares slope of the cluster's estimated density over the last 8 s (needs ≥ 5 s of samples; people/m² per minute; omitted when 0). A cluster with little history (new ID after clusters merged or split) inherits the older history of the track most of its phones came from. `cluster.eta` = (densityDanger − est) ÷ rate in seconds, only when rate > 0, est < danger and eta ≤ `earlyWarnS` (config, default 30, 0 = off). When eta is set and est ≥ `earlyFloor` × danger (default 0.55 → 2.2 people/m²) for 500 ms, a calm cluster goes yellow at once (it stays yellow while the projection holds) and the density incident is raised with `early: true`; a cluster that is already yellow (a packed but steady crowd, e.g. friend groups at the front) whose projection starts holding raises the same early warning once per yellow stretch (its open yellow incident gets `early: true` and `t` = now, and the early briefing); `early` is cleared when the incident goes red. An early incident gets its own briefing straight away, worded as a projection ("Stage front: about 40 people packing in fast; at this rate it reaches a dangerous 4 per m² in about 12 s." / "Open space ahead of them now."; Gemini gets `earlyWarning`, `secondsUntilDangerous`, `densityRisePerMinute`, `dangerDensity`); if it then goes red it is briefed again regardless of the 30 s cooldown, and a late early briefing never overwrites a red one. Thresholds, hold and hysteresis of the normal levels are unchanged.
`stats.detectMs` = mean duration of the active pipeline's step (detector + clusters) over the last 5 s, 2 decimals; `stats.snapshotBytes` = size of the previous encoded snapshot.
`node.zone` = first zone containing the phone ("" if none or outside). `density` = phones per m² of the cluster disc (count / max(π r², 1)); `people` = count / `participation`, and `est` = max(disc density, peak local density) ÷ participation is THE density estimate (cluster levels, early warning, area density rules, briefings and `status` all use it; the dashboard shows it, not `density`). Peak local density (`crowd.LocalPeakAmong`): for each member, phones within 1.5 m (every counting phone, not just members) ÷ 7.07 m²; the larger of the 90th percentile over members and the largest count k that at least k/5 members reach (`PeakAgree`), so a packed patch inside a big thin crowd reads at its real density (test floor 20 × 14 m at 1.5/m² with a 4 × 4 m patch at 6/m², participation 0.6: disc 1.1, old 90th percentile 4.7, est 6.1).

Zones: with no staff-drawn areas, the venue is split into `zoneCols` × `zoneRows` rectangles (default 2 × 1: A left half, B right half). With areas, zones = the areas (`custom: true`) plus `"rest"` ("Rest of venue", phones in no area). A phone may be in several areas. `sens: "high"` halves that zone's thresholds (`highRiskFactor`) and hold time.

HTTP: `GET /api/config` → `{venueW, venueH, geo, yellow, red, neighbourRadius, demo}` (`demo: true` while the demo spot is on: the phone page then skips GPS and the map); `GET/PUT /api/areas` (array of `{id, name, sens, poly, light, rules}`; ≥ 3 points, unique ids, names ≤ 40 chars, ≤ 50 areas, points clamped; saved to `data/areas.json`); `GET/PUT /api/venue` (`{w, h, lat, lon, bearing, geo, template, floorplan, layout}`; lat/lon = the map's top-left corner, bearing = degrees clockwise from north of the map's up; saved to `data/venue.json`; initial values from `VENUE_W/H/LAT/LON/BEARING`); `GET /api/node/{id}` (x, y, acc, src, outside, samples …); `GET /api/edge?from=<id>&to=<id>` → `EdgeExplain` (below).

- **Why did it fire?** `GET /api/edge?from=&to=` explains one neighbour pair from the active pipeline's latest detector step (live, replay or sim), in either order (the answer uses the detector's `from`/`to`: smaller x, then y, then id). 400 without both ids, 404 if the pair isn't a neighbour pair in that step. `{from, to, stepMs, a, b, lags, corr, lagMs, peak, second, wave, checks}`: `a`, `b` = the band-passed horizontal traces on the 50 ms grid over the 6 s correlation window (oldest first, m/s², 3 decimals, `null` = no valid sample); `lags` (ms, −maxLag…+maxLag) and `corr` (|r| per lag, `null` = too little overlap; positive lag = b moves after a); `lagMs` = the edge's sub-step lag (or the curve's own peak if the pair was gated out before correlating), `peak`, `second` (best separate peak, ≥ 0), `wave` = the detector's verdict. `checks` (in order): Both phones moving (sway vs `edgeMinSway`), Not handling, Not walking (only when one of the two phones sends `g`), Strong correlation (`corrThreshold`), Wave-like lag (120–1200 ms), Unambiguous peak (`peakMargin`), Not vertical (Mexican-wave veto), Part of a chain of ≥ `minChain`; each `{name, pass, detail}`. The detector only keeps the latest step's pair list and per-phone flags; the curve is recomputed on request from the phones' resampled traces, which stay in place until the next step.

- **Venue extras.** `template`: preset name (≤ 40 chars). `floorplan`: read-only, true while an image is stored. `layout`: `{"stage": [[x,y],...], "exits": [{"id","name","x0","y0","x1","y1"}], "walls": [[x0,y0,x1,y1],...]}` in venue metres; points clamped to the venue (to the cm), stage 0 or 3–64 points, ≤ 32 exits (missing/duplicate ids become `exit-N`, names ≤ 40 chars), ≤ 128 walls, zero-length segments dropped. When a simulation starts, a layout with walls or exits replaces the sim's default geometry: the venue outline plus the layout's walls, a gap cut wherever an exit lies along a wall (exits within 0.3 m of the venue edge snap onto it), the stage outline as a wall (people press toward its largest-y edge). `GET /api/sim` shows the same geometry when not running.
- **Floor plan.** `POST /api/venue/floorplan`: raw image body, ≤ 8 MB (413), type sniffed with `http.DetectContentType` and only PNG/JPEG/WebP accepted (415); stored as `data/floorplan` + `data/floorplan.json` (`{"type"}`) → the venue (`floorplan: true`). `GET` serves it with its type and `Cache-Control: no-store` (404 if none); `DELETE` removes it → the venue. `POST /api/venue/floorplan/analyze` → `{w, h, layout, notes, confidence: low|medium|high}`: Gemini vision request (inline image, `responseMimeType: application/json` + response schema, 20 s timeout). Gemini answers on a 0–1000 grid over the whole image plus the metres the image spans; the server converts to venue metres (origin = image top-left), clamps w/h to 2–5000 m and every point into w × h, caps counts, fixes h to the image's aspect ratio (PNG/JPEG) if Gemini's differs by > 10 %, and defaults an unknown confidence to low. 503 `{"error":"Gemini isn't configured (GEMINI_API_KEY)"}` without a key, 404 without a plan, 502 `{"error"}` on Gemini failure. Saves nothing; the dashboard applies it with `PUT /api/venue`.
- **Area rules** (`rules` on an area, all optional): `{"density": 4, "densityHoldS": 5, "push": true, "maxPhones": 200, "message": "≤ 140 chars", "notify": {"sign": true, "light": true, "voice": true}}`. `density` (0 = off): estimated people/m² inside the area = the clusters' estimate: peak local density (`crowd.LocalPeakAmong`) centred on the counting phones (not stale, outside or disconnected) inside the area, with every counting phone (inside or just past the outline) as a neighbour, ÷ participation; never the area-wide phones ÷ polygon area, except that an area smaller than the 7.07 m² local disc takes the larger of the two; above `density` for `densityHoldS` (0 = 5 s) → red, above 75 % → yellow, each clearing 10 % (`margin`) below its threshold (the zone/cluster state machine). `maxPhones` (0 = off) is a capacity in **people** (name kept for compatibility): more estimated people inside (counting phones ÷ participation) than this for over 3 s → red; calm as soon as it's back at or under. The area's rule level is the worse of the two, raises `kind: "rule"` alerts (score = estimated people/m², or estimated people for capacity) and merges into the zone's `level` in snapshots, phone states, signs and lights. `push: false` → the zone's wave score stays 0 and its detector level calm (`detect.ZoneDef.NoPush`). `message` replaces the briefing's `action` verbatim (template and Gemini, which is told "use this action verbatim"; the server overwrites Gemini's action with it), for every alert kind in that area. `notify`: `sign: false` leaves the area out of the worst-zone sign's choice, `light: false` makes its light count it as calm, `voice: false` skips generating audio (the dashboard may still use browser speech for an alert without `audioUrl`).
- **Alerts:** `POST /api/alerts/{id}/ack` (open → ack, sets `ackAt`; optional body `{"by"}` → `ackBy`), `POST /api/alerts/{id}/resolve` (sets `resolvedAt`, closes the incident; optional body `{"by", "note"}` → `resolvedBy`, `note`; a note sent for an already resolved alert replaces its note) → the updated alert, also broadcast to dashboards as an `alert` message and kept in the log; empty bodies work; `by` ≤ 60 chars (whitespace collapsed), `note` ≤ 280, else 400 `{"error"}` (also for bad JSON); 404 `{"error"}` for an unknown id (or one that fell out of the 50-alert log). `POST /api/alerts/clear` drops resolved alerts (calm notices included) and test alerts from the log, keeps open and acknowledged real incidents (they keep their ids and updates), → and broadcasts `{"type":"alerts","alerts":[…]}`.
- **Join URL:** `GET /api/phone-url` (plain text) and `GET /api/qr.png` as before; `GET /api/join` → `{"url", "reachable"}`: `url` = `PUBLIC_URL` (flag `-public-url`, `app.Options.PublicURL`) + "/" if set, else the request's own scheme/host (X-Forwarded-Proto/-Host honoured); `reachable` = `public` (PUBLIC_URL set, a public IP or a dotted hostname), `lan` (private, link-local or 100.64/10 IP, a bare machine name, `.local`/`.lan`/`.home`/`.internal`), `local` (localhost, `*.localhost`, loopback). The dashboard warns when it isn't `public`.
- **Judge demo** (`app/demo.go`). `GET/PUT /api/demo` `{on, x, y, spacing}` (the demo spot; saved to `data/demo.json`; x, y clamped to the venue, spacing 0.3–2 m, 0 = 0.6; 400 `{"error"}` otherwise). While `on`, a phone whose hello has no position is placed at the first free place of a row starting at (x, y) and running toward +x, `spacing` apart, in join order (wrapping to further rows at the venue edge); a phone the server already placed keeps its position. PUT also takes `"arrange": true`: every connected phone is lined up now, in join order. `PUT /api/node/{id}/pos` `{x, y}`: staff moved a live phone's dot (clamped to the venue; also while a simulation runs) → its `NodeDetail`; 404 unknown phone, 400 bad body; the phone sees it in its next `state`. `GET /api/receipt/{id}` (the phone's full session id) → `{id (first 8 chars), name, color, x, y, src: gps|manual|none, messages, seconds, kept, forgetS, recorded, stored, store}`: what the server holds about that session, for the phone's Leave screen. `kept` = readings in memory (≤ 30 s, dropped `forgetS` = 30 s after it leaves); `stored` = continuous storage is on (`store`: "Tiger Data" or "a file on the server", the JSONL auto-recorder), `recorded` = a labelled recording was running; `src: none` = never placed, nothing stored. 404 for an unknown id.
- **Tower check-in** (`app/tower.go`). Fixed things staff placed on the map are position anchors for phones: this laptop (hardware entry `{"name":"This laptop","key":"laptop","kind":"laptop","online":true}`, always first in `GET /api/hardware`, never probed, placed with `PUT /api/hardware/laptop/pos`), the sign (`sign`) and the zone lights (their letter). Every hardware entry carries its `key`. A web page can't range against Bluetooth, so the anchor is a QR code: the join link with `?at=<key>` (`GET /api/qr.png?at=<key>`; 404 for an unknown or unplaced tower). The phone page asks `GET /api/tower/{key}` → `{key, name, x, y}` (404 `{"error"}` unknown key, 409 `{"error"}` not on the map yet; it then shows the message and joins the normal way) and sends `"at": "<key>"` in its hello. The server puts the phone 0.5–1 m from the tower, on a side picked from its id (so phones don't stack; turned to stay inside the venue), node `src: "tower"`; the page shows "Placed at Zone light A". A reconnect with the same `at` keeps the position; a `pos`, a hello with `x`/`y` or a staff drag ends it (`src: manual`). An unknown or unplaced tower in a hello is ignored (normal placement: x/y, row/col, demo spot, unplaced). **GPS correction:** if the phone also sends GPS, the check-in calibrates it: offset = check-in position − the phone's smoothed fix (its current one if already tracked, else its first accepted fix within 30 s of the check-in; ignored if > 60 m), added to every later smoothed fix × e^(−t/120 s) (37 % left after 2 min, dropped below 2 %, ~8 min). All in venue metres; latitude and longitude are never kept. The dashboard's Hardware page shows a "Check-in QR" button (the join modal with that link; Print works) on every placed tower.
- **Boards:** `GET /api/hardware` entries also carry `x, y` (where staff placed the board, from `data/hardware.json`; absent = not placed), `beacon` (the board's `/pulse` `name`, e.g. `PULSE-A`) and `peers: [{"name","rssi","dist","age","mapDist"}]` passed through from `/pulse` (`mapDist` = the map distance between the two boards, only when both are placed and each hears the other). `PUT /api/hardware/{key}/pos` `{x, y}` (key `sign` or a light letter, case-insensitive; clamped to the venue) → the hardware list; 404 for an unknown key, 400 for a bad body. Zone-light firmware `/pulse`: `{"kind","level","rssi","uptime","ble":{…},"name":"PULSE-B","mac":"C72C","peers":[{"name":"PULSE-A","rssi":-63,"dist":2.4,"age":3}]}`.
- **Bluetooth beacon positioning** (opt-in, Android only; `docs/BEACONS.md`; `internal/protocol/beacons.go`, `app/beacons.go`, `app/beaconlinks.go`, `hub/beacons.go`, `web/phone/src/beacons.ts`, `web/shared/beacons.ts`). The boards are anchors: zone lights advertise `PULSE-<letter>`, the sign's beacon build `PULSE-S` (the default name for the `sign` key even when it is offline or never reported a name); only boards placed on the map count. Three sources of a range between a phone and a board, all through the log-distance model `d = 10^((tx1m − rssi) / (10·n))` (defaults −64 dBm, 2.2):
  - `scan`: phone → server `{"type":"beacons","seen":[{"name":"PULSE-A","rssi":-61,"n":14}]}` about once a second (Chrome's Web Bluetooth Scanning API behind `chrome://flags/#enable-experimental-web-platform-features`, or the Android app). ≤ 16 entries, distinct names `PULSE-<tag>` ≤ 32 printable ASCII characters, rssi −110…−20, `n` = adverts in the 2 s window; one bad entry drops the report; names that aren't this venue's boards are dropped; an empty list = "I hear none". Handled by the optional `hub.BeaconHandler` (`PhoneBeacons`).
  - `conn` (connect mode, any Android Chrome, no flag): the page connects to a zone light (GATT service `7b1e0001-52c4-4f6a-9d6b-50554c534500`) and writes its session id (≤ 36 chars of letters, digits, `-`) to characteristic `7b1e0002-…`; the board reads that connection's RSSI about once a second. At most 3 phones per board; a connection that writes no id within 5 s is dropped.
  - `adv` (the Pulse Android app): the phone advertises manufacturer data `FF FF 'P' 'L' 'S' '1'` + the first 8 hex characters of its session id; every zone light hears it in its scan (smoothed RSSI, up to 24 phones, forgotten after 10 s). The server matches the 8 characters to the one connected phone whose id starts with them (none or several = ignored).
  - Zone-light firmware: `GET /links` → `{"links":[{"id":"<session id>","rssi":-57,"age":1}],"heard":[{"id":"1a2b3c4d","rssi":-63,"age":1}]}` (also `"links"` and `"heard"` in `/pulse`; a bare array of links is accepted too). The server polls each online zone light's `/links` (800 ms timeout) every second while any board reports a phone, every 3 s otherwise, and leaves a board that didn't answer alone for 15 s. Readings with rssi outside −110…−20 (0 = not measured yet) or `age` > 5 s are dropped.
  - Per phone and board: the fresher of `conn` and `adv`; a board measured both by the phone (`scan`) and by itself is one anchor, the two distances averaged by certainty. Board-side readings use `connTxPower1m` (default = the model's).
  - Geometry (`BeaconFix.dims`): one board = a distance ring, no position, unless closer than 1.5 m (`dims: 0`, "near board X", placed beside it); two boards, or all in a line = the position along the line only (`dims: 1`, `along`, `cross` ≥ 2 m, `axis`); three or more not in a line = 2-D weighted least squares (`dims: 2`). Reports older than 5 s are no fix.
  - The fix becomes the phone's position (`src: "beacon"`) only when it is 2-D (then a GPS fix doesn't move the phone while it is fresh) or the phone has no other position. `App.BeaconFix(phoneID) (x, y, acc, dims, ok)` and `BeaconFixDetail` are the accessors for a position estimator.
  - HTTP: `GET /api/beacons` → `{boards: [{name, key, label, x, y, online, txPower1m, calibrated, connectable, links, heard}], model: {txPower1m, pathLossN}, connTxPower1m, service, idChar, venueW, venueH, nearM, staleS, prefix, placed, maxDims}`; `POST /api/beacons/locate` `{"seen":[…],"id":"<id written to boards>"}` (both optional) → a `BeaconFix`, nothing kept (400 on an invalid report or id); `PUT /api/beacons/model` `{txPower1m, pathLossN}` | `{"beacon":"PULSE-S","txPower1m":-58}` (per-beacon 1 m reference; 0 removes it; 404 unknown beacon) | `{"conn":true,"txPower1m":-60}` → like GET, saved in `data/beacons.json`; `GET /api/node/{id}` gains `beacons` (the phone's latest `BeaconFix` with `heard: [{name, rssi, n, dist, placed, src}]`, `ageMs`, `used`).
  - `/beacons.html` (second entry of the phone Vite app): the diagnostic page; works without joining.

**Crowd simulation** (`internal/crowdsim`, a third data source next to live and replay). While it runs, `mode` is `"sim"`, the dashboard shows the sim pipeline (its phones are ordinary `nodes`, ua `"sim"`), live phones keep streaming into the live pipeline underneath (and also stand in the simulated crowd, see Hybrid below), and `snapshot.sim` carries every simulated person as `[x, y, pressure N/m, hasPhone 0|1]` (x, y to 2 decimals, pressure an integer), plus `t` (s since start) and the last behaviour `action`. Starting a replay stops the sim and vice versa.
- `POST /api/sim/start` `{"people":250,"participation":0.6,"scenario":"concert","realism":"ideal","imperfections":{"gps":1,"carry":1,"dropout":1}}` (all optional; people 1–1000, participation (0, 1]) → `{"mode":"sim"}`; 400 `{"error"}` on bad input, 409 if already running. `realism` makes the simulated phones as messy as real ones (`crowdsim/realism.go`): `"ideal"` (default: exact position, upright on the chest, nothing lost), `"realistic"` (every imperfection at strength 1) or `"harsh"` (strength 2). `imperfections` overrides single strengths on top of the preset (0–3, 0 = off): `gps` (phones locate by GPS-like fixes with error and an accuracy radius, treated exactly like live fixes: dropped above `gpsMaxAcc`, smoothed, `outside` when off the venue; such a phone starts unplaced until its first usable fix), `carry` (carried at any orientation, sending `g`), `dropout` (messages late, in clumps, or lost). E.g. `{"realism":"ideal","imperfections":{"gps":1}}` is GPS error alone.
- **Hybrid: real phones in the simulated crowd** (`app/hybrid.go`). While a simulation runs, every connected, placed live phone is also (a) a pinned body in the simulated world at its venue position (`crowdsim.Pin`, radius 0.23 m, fading in over 1 s): simulated people feel the same repulsion, compression and friction as against a person, walk round it and press against it; and (b) a node of the sim pipeline with `real: true`, its name and colour, fed its own motion readings, so it is in the sim's zones, clusters, pairs and guidance. Its `state` then comes from the sim pipeline with `sim: true` (the phone page shows "Simulated crowd around you — drill"); the live pipeline keeps running on the same readings and its state takes over when the simulation stops. Moving the phone (`pos`, staff drag) moves the pin; disconnecting removes it. With no live phone connected the simulation is bit-for-bit what it is without this.
- `POST /api/sim/surge-phones` → `{"mode":"sim","x","y","phones","started"}`: "surge around the phones". Starts a simulation if none runs (220 people, participation 0.6), sends the crowd to the centroid of the connected live phones (`attract`; the demo spot if nobody is connected but it is on; else 409 `{"error"}`), and from 5 s later has everyone within 5 m lean toward it (`crowdsim.Press`: a force of mass × up to 8 m/s² per person, ramping over 10 s and swelling ±15 % every 2.5 s) for up to 75 s, or until another behaviour action (calm, disperse, …) is applied. The crush, the alerts (typically an early-warning yellow within ~5 s and a density red within 10–15 s), briefing, voice and signs then follow from the normal pipeline.
- `POST /api/sim/stop` → `{"mode":"live"}`; 409 if not running.
- `POST /api/sim/action` `{"type": "calm|stage|surge|attract|shove|exit|disperse|spawn", ...}` → 200 `{"ok":true}` or 400 `{"error"}` (also when not running). Fields: `surge` `strength` 0..1 (default 0.7); `attract` `x, y`; `shove` `x, y, dx, dy` (direction, any length; optional `strength` 0..1, default 0.7); `exit` `id, open`; `spawn` `x, y, n` (1–200). x, y must be inside the venue.
- `GET /api/sim` → `{"running", "t", "people", "phones", "participation", "action", "exits": [{"id","name","x0","y0","x1","y1","open"}], "walls": [[x0,y0,x1,y1], ...], "truth": {"maxDensity", "maxPressure", "crushing", "dangerAt", "alertAt", "leadSeconds"}}`. Not running: only `running:false`, `exits`, `walls` (the venue layout). Truth times are s since start, `null` until they happen; `leadSeconds` = `dangerAt − alertAt` (positive = Pulse warned first) once both have happened. `maxDensity` = highest people within 1 m ÷ π m²; `crushing` = people ≥ 1600 N/m or > 6 /m²; `dangerAt` = start of the first ≥ 1 s stretch with ≥ 3 people ≥ 1600 N/m or ≥ 5 people > 6 /m²; `alertAt` = Pulse's first red alert (wave or density) in the sim pipeline.
- Exit ids: `exit-bl`, `exit-br` (bottom corners), `exit-l`, `exit-r` (side walls). `walls` are the static walls including the stage pit (barrier 1.5 m from the top across the middle 60 %); exits are drawn from `exits` (closed = a wall).

### Phone-to-phone mesh (WebRTC data channels)

Phones link to a few nearby phones and pass each other's data on, so a phone that loses its connection still reports and is still warned. No permission prompt, no extra step after Join; with no links a phone just uses its WebSocket. Types: `server/internal/protocol/mesh.go` ↔ `web/shared/mesh.ts`; code: `hub/mesh.go` (signalling, relay), `app/mesh.go` (pairing, evidence, jam), `web/phone/src/mesh.ts` (+ `trace.ts`, `consensus.ts`), `web/dashboard/src/meshnet.ts`.

**Handles.** Phones never see each other's session ids. On the mesh, in signalling and in relay envelopes a phone is named by its handle: the first 12 hex characters of SHA-256(session id). The hub maps handles back to ids (a phone must have connected directly once since the server started).

Phone ↔ server (on the phone WebSocket, or wrapped in a relay)
```jsonc
{ "type": "rtc", "on": true }                                   // phone → server after every (re)connect: this browser can open links
{ "type": "mesh", "me": "<handle>", "peers": [ { "id": "<handle>", "init": true } ], "ice": ["stun:…"] }
// server → phone: the FULL peer list (links to anyone else are closed); init = this phone sends the offer
{ "type": "sig", "to": "<handle>", "kind": "offer|answer|ice|hi", "data": { … } }   // phone → server; delivered as {"type":"sig","from":"<handle>",…}
{ "type": "near", "peers": [ { "id": "<handle>", "corr": 0.82, "lagMs": 240, "hops": 1, "rtt": 14 } ],
  "failed": ["<handle>"], "tx": 1900, "rx": 1700, "known": 7 }   // phone → server, ~1 Hz
{ "type": "mpos", "x": 8.1, "y": 7.9, "acc": 1.5, "hops": 1, "n": 3, "src": "gps" }   // phone → server, ~1 Hz while neighbours correct its position
{ "type": "relay", "from": "<handle>", "hops": 1, "msg": { …a phone → server message… } }   // phone → server, for a phone with no socket
{ "type": "relay", "to": "<handle>", "msg": { …a server → phone message… } }                 // server → the phone that last delivered for it
{ "type": "jam", "on": true }     // server → phone: drop the WebSocket, go through the mesh (off = reconnect).
                                  // phone → server: on = "I am leaving this socket now", off = "can't: no mesh route"
{ "type": "clock", "offset": -12 } // server → phone: its clock offset (phone − server, ms), after each sync burst when it changed by > 2 ms
```
- **Pairing** (`app.selectPeers`, every 1 s): each phone that sent `rtc` gets up to `MeshPeers` (4) links, at most `MeshMaxPeers` (6): the nearest placed phones by the server's current position; a phone with no position gets pseudo-random peers (a hash of the pair, stable from tick to tick). Hysteresis: a link is only dropped when it is older than 10 s and neither end has the other among its 8 best candidates. A pair a phone reports in `near.failed` is not tried again for 60 s (cleared when either phone sends `rtc` again: a fresh page). A phone whose socket closed stays paired for 10 s so it can come back through a neighbour. The list is re-sent when it changes and every 10 s.
- **Signalling:** the hub passes `sig` only between phones that are currently paired, at most a burst of 120 then 30/s per sender. `hi` = "I have no link to you: call me" from the side that does not offer (a reloaded page). ICE servers: env `PULSE_STUN` (comma-separated URLs; default `stun:stun.l.google.com:19302`; set but empty = host candidates only, one network). No TURN. A pair that isn't connected in 8 s is given up quietly and reported as failed; a link that worked and then died is retried once before that.
- **`near`:** every peer with an open data channel. `corr` = peak |r| of the lagged cross-correlation (±1.5 s, 6 s window, 100 ms grid in server time) of the two phones' sway traces (levelled with gravity, band-passed 0.15–1.5 Hz, dominant horizontal direction); 0 = too little motion or overlap. `lagMs` > 0 = the peer moves after this phone. `tx`/`rx` = payload bytes/s on its data channels. The app keeps the latest report per phone for 3 s: `App.NearEvidence()` (ids resolved, `map[id][]NearPeer`). `App.MeshPositions()` gives the phones' `mpos` (kept 5 s); `mpos` never replaces the server's position.
- **Relay:** a phone without a usable WebSocket sends its ordinary messages (`hello` without `id`, `m`, `pos`, `near`, `mpos`, `sig`, `rtc`, `beacons`; never `gps`, and a relayed hello's lat/lon are ignored: raw GPS does not pass through other phones) to the peer nearest the server; a phone with a socket wraps them in `relay`. The hub checks shape, `hops` 1–`MaxRelayHops` (3), inner size ≤ 4 KiB, known handle, `from` ≠ the sender (loop), no nested relays, 40 msg/s per relayed phone and 300/s per relayer, then dispatches the inner message exactly like a direct one. A phone's own live socket (anything received in the last 2 s) always wins: relayed copies are dropped. A phone the hub doesn't hold must say `hello` first. Timestamps use the offset its own socket last measured (else estimated from the messages). Server → phone messages for a relayed phone (state, guidance, `mesh`, `sig`, `jam`) go to the phone that last delivered for it, as `relay` with `to`. Silent for 6 s → gone. A direct `hello` takes over again.
- **Jam (the lost-signal demo):** `POST /api/mesh/jam` `{"id":"<session id>","on":true}` or `{"half":true,"on":true|false}` → `{"phones":[ids],"on"}`; 404 unknown phone, 409 `{"error"}` when the phone has no open link to fall back on. "Half" jams ⌊n/2⌋ of the phones that have links, never one whose jammed neighbours would be left without a connected neighbour; `on:false` restores everyone. The phone answers `jam on`, **closes its WebSocket for real** and relays everything; the order to restore reaches it through the mesh. A server-ordered jam undoes itself after 10 s without a mesh route. The phone's own "Simulate lost signal" switch does the same locally. `GET /api/mesh` → `{ice, capable, links, pairs, jammed, relayed, txBytes, relayIn, relayDropped}`.

Server → dashboard: `snapshot.mesh` (absent in a replay)
```jsonc
"mesh": { "links": [[0, 3], [1, 3]],            // pairs of indexes into snapshot.nodes: open links as the phones report them
          "nodes": [ { "id": "…", "via": "<session id of the relaying phone>", "hops": 1, "peers": 3, "rtt": 12, "jam": true,
                       "tx": 1900, "rx": 1700, "known": 7, "pos": { "x": 8.1, "y": 7.9, "acc": 1.5, "hops": 1, "n": 3, "src": "gps" } } ],
          "phones": 9, "reporting": 9, "viaMesh": 4,   // connected; sent something in the last 2 s; of those, relayed
          "virtual": false }
```
`via` absent = the phone's own socket. In mode `sim` the simulated phones (no browsers) get stand-in links picked by the same `selectPeers` (refreshed every 2 s, `virtual: true`), and `NearEvidence()` gives them the detector's own pair correlation (`corr`, `lagMs`) on those links; nothing else is simulated (no relay, no gossip).

Phone ↔ phone (compact JSON; two negotiated channels per link: `u` id 0 unordered, no retransmits; `r` id 1 reliable)
```jsonc
{ "k": "b", "id": "<handle>", "q": 1234, "x": 8.1, "y": 7.9, "a": 1.5,      // beacon, 2 Hz on u: position estimate (mesh-corrected) ± a (1 sigma, m)
  "o": [13, 11, 8], "src": "gps", "ah": 1,                                // own-source estimate [x, y, sigma] before correction; its source; hops from an anchor
  "z": "calm", "s": 0,                                                    // zone level; hops to the server (0 = own WebSocket; absent = no route)
  "t": 17910919961, "h": [12, -3, null, …] }                              // sway trace: grid index (server ms ÷ 100) of the first of ≤ 20 samples, 0.01 m/s²
{ "k": "g", "b": [ { "id", "q", "x", "y", "a", "z", "hp": 2, "ttl": 1 } ] } // gossip, 1 Hz on u: ≤ 8 freshest beacons heard (no trace), ≤ 3 hops, de-duplicated by (id, q)
{ "k": "w", "id", "q", "l": "yellow|red", "x", "y", "hp": 1, "ttl": 2 }    // warning, 1 Hz while the origin's zone is yellow/red; flooded ≤ 3 hops
{ "k": "p", "t": … } / { "k": "q", "t": … }                                 // link round-trip probe and echo, every 2 s
{ "k": "up", "f": "<origin handle>", "h": 1, "m": { … } }                  // on its way to the server (motion on u, the rest on r); h ≤ 3
{ "k": "dn", "to": "<handle>", "m": { … } }                                // a server message on its way back along the same path (r)
```
A phone shows "Warned by a neighbour" when a warning ≤ 5 s old from within 8 m (or ≤ 2 hops when positions are unknown) is worse than what the server last told it. A relaying phone forwards `up` only to a peer strictly nearer the server (by `s`), so relays never loop.

**Phone-side position consensus** (`consensus.ts`, ~1 Hz, also with no server): for direct peers whose sway matched this phone's (`corr` ≥ 0.6 in the last 10 s) the phone fuses its own-source estimate with theirs by inverse variance. A neighbour's estimate counts with variance σ² + 0.7² (they are pressed together, not in the same spot) and 0.5 m/s of age; the result is never more certain than the best neighbour + 0.7 m (unless its own source already is). Sources: `manual`, `tower`, `demo` (σ 0.5 m, growing 1 cm/s; an **anchor** for 120 s after the placement), `gps` (σ = the fix's accuracy, ≥ 3 m), `beacon` (reserved). Feedback guard: corrected estimates only flow away from anchors (`ah` = hops from an anchor): a neighbour's corrected estimate is used only if its `ah` is strictly smaller than this phone's; otherwise only its own-source estimate `o`. Anchored phones weigh neighbours × 0.1. The result goes into the beacon, to the server as `mpos`, and onto the phone's mini-map. Test aid: `/?acc=8` makes a phone treat its own placement as ±8 m.

The phone also sends `"hd": <0–359>` on `m` (compass heading of its top edge, degrees clockwise from north) when it has a compass: when it changed by ≥ 5° or once a second.

Privacy on the mesh: a handle, a venue-relative position, a zone level and motion numbers. No session id, no name, no raw GPS.

## Clock sync

On connect, send 8 pings 100 ms apart. For each: `rtt = now - t0`, `offset = t1 - (t0 + rtt/2)`. Keep the offset from the sample with the smallest RTT. Re-sync every 30 s. Corrected time = `t - offset`. Show RTT per node on the dashboard (judges like seeing real distributed-systems numbers).

## Phone page (web/phone)

1. Big "Join" button → `DeviceMotionEvent.requestPermission()` on iOS (must be inside the tap handler; needs HTTPS).
2. Grid picker: tap your spot (e.g. 1 row × 8 cols for the line demo, configurable from server).
3. Sample `devicemotion` (`acceleration`, fall back to `accelerationIncludingGravity` minus a running mean), summarize every 100 ms, send `m`.
4. Screen shows: connected dot, "keep this page open, your pocket or your hand is fine, any way up", and colour feedback from `state`.
5. Use Wake Lock API so the screen stays on. Reconnect automatically.

Orientation: the phone can be carried any way round. Gravity = `accelerationIncludingGravity − acceleration` when the browser gives both (sensor fusion), else the running mean of `accelerationIncludingGravity` (the same estimate that is subtracted to get the acceleration); averaged over the 100 ms, normalised, and sent as `g` (see the wire protocol) unless its length is outside 4–16 m/s². That vector points up on Android (W3C convention) and down on iPhones (every axis has the opposite sign), so the page negates it on Android: `g` is "down" on both. `ax, ay, az` are sent as the browser gives them (device axes, platform sign); the detector is sign-blind.

Axis note for phones that send no `g`: held upright, flat against the chest, the phone's **x** is left/right and **z** is forward/back (both horizontal). **y** is up/down (walking, jumping).

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
0. **Levelling** (`level.go`, phones that send `g`): each reading is split into vertical = −a·g and a 2-D horizontal vector (a·e1, a·e2), before any filtering. e1, e2 are the phone's own horizontal axes: device x, z for g = (0, −1, 0), so an upright phone that sends `g` gives the same numbers as one that doesn't; as g moves, e1 is carried along (projected onto the new horizontal plane), so the basis never jumps. Everything below then reads "x, z" as the two horizontal components and "y" as the vertical: the sway score, the vertical (Mexican-wave) veto, the walking gate.
1. **Handling filter:** if rotation rate > threshold, mark `handling`, drop readings until it settles for 1 s. For levelled phones also when `g` turns more than `handlingTiltDeg` (45°) within `handlingTiltMs` (500 ms): the phone was turned over or pulled out of a pocket.
2. **Low-pass** x and z (sway is slow, roughly 0.2–1 Hz). Simple exponential moving average or a 2-pole Butterworth.
3. **Horizontal only:** unlevelled phones: `h` = x (`axis` config: x, z or xz = dominant direction). Levelled phones: always the **dominant horizontal direction** over the 6 s correlation window (principal axis of the horizontal vector after a 0.5 s moving average, which cancels a 2 Hz step/jump beat so the axis is that of the slow sway; the trace projected onto it is not smoothed).
4. **Sway score:** RMS of filtered `h` over the last 5 s. Above threshold → `swaying`.
4a. **Walking gate** (levelled phones only): vertical RMS ≥ `walkMinVert` and both the vertical and the horizontal trace periodic (autocorrelation ≥ `walkRhythm` 0.75 at some lag ≤ 1.5 s after its first zero crossing) = walking with the phone in a pocket (step bounce + leg swing). Held for `walkHoldMs` (3 s). Such a phone joins no neighbour pair and is shown `ok`, not `swaying`. A push through a jumping crowd keeps a non-periodic horizontal trace and is not gated.

**Shared horizontal frame.** Levelling fixes down but not heading: each phone's e1 points an arbitrary way round the vertical. Two neighbours hit by the same push move along the same physical line, so each one's dominant horizontal direction is that line, and the pair is compared by |correlation|, which ignores which way along the line each phone counts as positive. No compass is used (unreliable indoors and next to steel barriers, different APIs on iOS and Android, and it still wouldn't say which axis the push is on). The direction a wave travels comes from positions and lag (who moved first), never from device axes. Mixed pairs (one phone upright without `g`, one levelled) work as long as the push is along the upright phone's x, as before.

Measured (`PULSE_ORIENT=1 go test ./server/internal/detect -run TestOrientationReport -v`; 8 phones in a line, 20 crowds, `g` with 3° error): push wave red in 20/20 upright, 5/20 at random orientation without `g`, 20/20 with `g` (median 24 s, same as upright); wave through a jumping crowd 20/20 → 0/20 → 19/20; stadium wave false yellow 0/20 → 4/20 → 0/20; pocket walking shown as swaying 95 % of the time without `g`, 1 % with. Step cost at 1000 phones, every pair correlated: 27 ms unlevelled (unchanged), 32 ms all levelled (`go test ./server/internal/detect -bench Step`).

Per pair of grid neighbours (A, B):
5. Cross-correlate their last 5–8 s of `h`, lags −1.5 s…+1.5 s. Record best lag and correlation.
6. **Travelling wave edge** if correlation > threshold AND |lag| between ~100 ms and ~1.2 s. Lag ≈ 0 with high correlation = everyone moving together = dancing/jumping → not a wave.

Per zone:
7. Zone score = fraction of neighbour edges showing a wave with **consistent direction**, smoothed over ~20 s.
8. Yellow when score > Y for N seconds; red when > R. Hysteresis: red clears only below R − margin, yellow below Y − margin.

All thresholds live in one config struct, loadable from a JSON file, so they can be tuned without recompiling. Write table-driven tests that run each labelled recording through the detector and assert the expected outcome.

### Rough positions (GPS-placed phones)

A phone placed by hand stands exactly where it says (accuracy 0) and everything above applies unchanged. A phone placed by GPS reports an accuracy radius of several metres (`detect.SetAccuracy`, from `node.acc`); with a 5 m error the "neighbours" within 1.1 m of its dot are strangers. For such phones the map only says who *could* be a neighbour, the motion says who is (`detect/motion.go`):

- **Candidates:** every other phone within `neighbourRadius + accPairScale × (accA + accB)` (capped at `maxPairRadius`, 15 m) that is moving at all (sway ≥ `edgeMinSway`, not handled, not walking: the walking gate applies to these phones with or without a gravity vector). Each phone is compared with at most `motionPairs` (10) candidates per step: pairs that looked like a wave hop in the last 2 s first, the rest drawn afresh each step.
- **Wave edge:** the same per-pair tests (correlation, wave-like lag, one clear peak, vertical veto). Only pairs that pass at least the chain-support level are listed in `links`/`waves` (`"motion": true`).
- **Chains without a map:** hops a→b and b→c make a run only if a and c match (|corr| ≥ `chainCorr`) at lag(a,b) + lag(b,c) ± `lagClosureMs` (150): three phones hit one after the other by the same disturbance.
- **A resolved lag:** for these pairs the best lag must beat every lag ≥ 300 ms away by `peakMargin`, separate peak or not: a slow sway (0.2 Hz) passed from row to row correlates almost as well half a second either side of its best lag, and without a map nothing else says it travels.
- **Zone score:** of all pairs of the zone's GPS-placed phones (not handled, with enough readings), the share whose two phones are both on such a wave (w(w−1) / n(n−1), needing w ≥ 2); same yellow/red thresholds (red ≈ 4 phones in 5 on the wave; a procession brushing every other person scores a quarter). In a zone with both kinds of phone the two scores are weighted by head count. Direction comes from the rough positions and is set only when the wave edges' travel vectors agree (|Σ| ≥ 0.5 × edges); `lagMs` is 0 (the lag between two such phones is not a per-person lag).
- **Evidence neither way:** a pair with a phone that is being handled, or with too few valid readings to correlate, no longer counts against a wave (exact pairs too). `minOverlap` (0.7): a lag needs that share of the readings a gap-free pair would have; gap-free pairs are unaffected.
- **Handling:** a burst of rotation shorter than `handlingShortMs` (1500) is a gesture: readings are masked for the burst plus `handlingShortSettleMs` (300) and the filters hold their state; longer, or the phone turned over, is handling as before (1 s settle, filters restart).
- **Density:** around a phone known to ± acc the local density is counted over a disc of radius `densityAccDisc` (0.5) × acc when that is wider than 1.5 m, and a cluster of such phones is drawn at least that wide; `cluster.acc` = the members' median accuracy radius (omitted for hand-placed). Twenty phones all claiming one spot to ± 10 m are not a crush. With accuracy of several metres `est` is the density averaged over that disc, a lower bound on the tightest spot: at 5 m error a 4/m² patch cannot be told from 2/m² by position (docs/EVAL.md), so packing shows as yellow at best and red needs better positions (tap-your-spot, staff-drawn areas with capacity rules, zone boards).
- **Guidance:** for a GPS-placed phone the direction is the gradient of the density smoothed by the position errors (its own and each other phone's), blended toward the least dense way out at long range (2 m + 2σ) where that gradient is weak; a push direction worked out from rough positions is not used (reason `density`). `move.conf` = 1 / (1 + (acc / 4 m)²), 1 for hand-placed, to 1 decimal: below 0.5 (acc > 4 m) the phone page should show a plain instruction instead of an arrow.

Config (detect.Config JSON): `accPairScale` (0 = ignore accuracy, the old behaviour), `maxPairRadius`, `motionPairs`, `lagClosureMs`, `outsideAccFactor`, `densityAccDisc`, `minOverlap`, `handlingShortMs`, `handlingShortSettleMs`. Step cost at 1000 phones: about 30 ms with exact positions (unchanged), about 95 ms with every phone GPS-placed, pushed and in range of every other (`go test ./server/internal/detect -bench Step`).

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
