# How Pulse works

Detection, zones, density, guidance, alerts and the HTTP API at a glance. The full wire protocol and every endpoint are in [REFERENCE.md](REFERENCE.md); the crowd simulator is in [SIMULATOR.md](SIMULATOR.md).

```
 phones (TS page)  ──ws /ws/phone──►  Go server  ──ws /ws/dash──►  dashboard (TS)
 sim (Go)          ──same ws───────►   ├─ clock sync per phone (8 pings, best RTT, every 30 s)
                                       ├─ detector every 250 ms
                                       ├─ Tiger Data (COPY every 500 ms) or JSONL
                                       ├─ Gemini → ElevenLabs on yellow→red
                                       └─ Arduino sign on level change
```

**Positions.** Everything is in venue metres: origin at the top-left of the venue map, x to the right, y down (default 24 × 16 m). A phone is placed by hand (`hello` x/y, then `pos` when it's dragged) or by GPS (`gps` lat/lon/acc, converted on arrival with the venue's geo-anchor; fixes worse than 25 m are ignored and the rest lightly smoothed; fixes outside the venue are clamped and that phone counts toward nothing). Old phone pages and recordings that send a grid `row`/`col` land on a line at x = 4 + 0.6·col, y = venue height / 2.

**Detection** (`server/internal/detect`), per phone on clock-corrected 10 Hz summaries:

1. **Handling**: rotation > 200°/s → readings ignored until 1 s of quiet.
2. **Band-pass** x (left/right with the phone upright on the chest) to 0.15–1.5 Hz: sway is slow.
3. **Sway** = RMS over 5 s; above 0.25 m/s² → *swaying*.

**Neighbours** are whoever is physically near: each active phone keeps its 6 nearest within `neighbourRadius` (1.1 m), and a pair is compared if either side keeps the other. Per pair, cross-correlate the last 6 s at lags ±1.5 s. A **wave edge** needs |correlation| ≥ 0.6 at a lag of 120–1200 ms, and that peak must be *unambiguous*: periodic motion like walking or swaying to music has several equally good lags, so the best peak must beat any other by 0.2. Lag ≈ 0 means moving together (dancing) and is never a wave.

Two guards keep look-alikes out:

- **Vertical veto** (`verticalRatio`): a push is horizontal. If the phones' vertical (y) motion is stronger than the horizontal, isn't rhythmic (its autocorrelation doesn't come back above 0.6 within 1.5 s, unlike jumping or walking to a beat), and travels between the pair by itself (|corr| ≥ 0.6 at a 120–1200 ms lag, same direction), it's people standing up in sequence: a stadium Mexican wave whose lean and tilt leak into x. A crowd jumping on the spot while a push goes through is rhythmic, so it never vetoes the push.
- **Chains** (`minChain`, `chainCorr`, `chainAngleDeg`): a crowd wave passes person to person to person. Each wave edge is oriented in its direction of travel, and it only counts as part of a run of ≥ 3 phones whose hops keep going the same way (each hop within 90° of the one before; on a line that is simply "the same way along the line"). The other hops only need to *support* it (|corr| ≥ 0.4 at a wave-like lag), like hysteresis in edge linking, so one noisy hop doesn't break a real wave. Two neighbours bumping into each other make an isolated edge and are dropped.

**Zones.** With no staff-drawn areas the venue splits into `zoneCols` × `zoneRows` rectangles (2 × 1: A is the left half, B the right). Staff can draw watch areas on the dashboard (`PUT /api/areas`, saved in `data/areas.json`); the zones are then those areas plus "rest" (everyone in no area). A phone may be in several areas, and a **high-risk** area alerts at half the thresholds (`highRiskFactor`) with half the hold time.

Per zone: score = |Σ unit travel vectors of the wave edges| ÷ the zone's edges that could show a wave, smoothed over 8 s: the net fraction of edges carrying a wave in one direction. "Could show a wave" leaves out pairs standing side by side across the direction of travel: at the wave's own speed (median length ÷ lag of its wave edges) they are hit less than 120 ms apart, so their near-zero lag fits the wave rather than counting against it. On a line every hop counts. Yellow above 0.3 for 2 s, red above 0.6 for 2 s, clearing 0.1 lower (hysteresis). Red already needs persistence: a single travelling event shows in the 6 s correlation window for at most ~6 s, which takes the 8 s-smoothed score to ~0.5 at most, so one shove or one person squeezing past can reach yellow but not red (in a normal-sensitivity zone). The briefing's direction is the dominant axis of the net travel vector: `+x` is left to right on the map, `+y` top to bottom.

**Crowd clusters** (`server/internal/crowd`): every detector tick, DBSCAN (eps 1.2 m, ≥ 3 phones) over the phones that aren't stale or outside. Each cluster has a centroid, a radius (farthest member + 0.5 m) and a density = phones ÷ max(π r², 1 m²). Clusters keep their ID across ticks (nearest centroid within 2 m). The **trend** compares now with 10 s ago (each end averaged over 2 s): *forming* if there are ≥ max(2, 10 %) more phones, or else the density rose ≥ 15 %; *dispersing* is the mirror image; *steady* otherwise. A cluster younger than 5 s is *forming*.

**Density alerts.** A cluster's level comes from its estimated density = phones/m² ÷ `participation`, where phones/m² is the larger of the cluster-disc density and the *peak local density* (phones within 1.5 m of a member ÷ 7.07 m², at the 90th percentile of the members). Without the local peak, a 20 m crowd pressed against a barrier is one big cluster whose disc average hides the packed front (the crowd simulator showed 1.2/m² for a front at 6/m²). Levels: yellow above `densityWatch` (2 people/m²) for 2 s, red above `densityDanger` (4 people/m²) for 2 s, each clearing 10 % below its threshold. Yellow → red raises an alert through the same chain as a wave (timeline, a Gemini briefing worded as crowding, voice, sign) for the zone the cluster's centre is in, with the same one-briefing-per-zone-per-30-s limit. **The participation caveat:** density counts *phones*. The default `participation` of 1.0 assumes everyone in the cluster has the page open. If only a third do, set 0.33, so that 4 phones in a few m² read as 12 people. Set it too high and real crushes read as half as dense; too low and comfortable groups raise alarms. It's the weakest number in the system, so tune it per event.

**Early warning from the density trend.** Waiting for the density to pass 4/m² for 2 s made Pulse ~2 s late on a sudden surge. Each cluster now also has a `rate` (least-squares slope of its estimated density over the last 8 s, people/m² per minute) and, when it is rising, an `eta`: seconds until it would reach `densityDanger` at that rate, if within `earlyWarnS` (30 s). A cluster whose `eta` is set while its density is already at least `earlyFloor` × danger (0.55 → 2.2 people/m²) goes yellow after 500 ms instead of waiting for the 2 s hold, with a density alert marked `early: true` and a briefing worded as a projection: *"Stage front: about 40 people packing in fast; at this rate it reaches a dangerous 4 per m² in about 12 s. Open space ahead of them now."* Red still needs the danger density itself, unchanged. When small groups merge into one big cluster (what a surge does), the new cluster inherits the density history of the group most of its phones came from, so its rate doesn't restart from zero.

**"Move this way" on the attendee's phone.** While a phone is in danger (a red zone, including an area turned red by a staff rule such as capacity; a yellow or red cluster it belongs to; or a push passing through it), its `state` message carries `move: {dx, dy, to, reason}`, a unit vector on the venue map, plus its position and the venue size (and the venue's bearing when GPS-anchored) so the phone can draw an arrow on a little map. The direction is down the gradient of a kernel density estimate of all phones (Gaussian, σ 1.5 m): toward fewer people, sliding along walls and the stage instead of into them. For a push it is sideways to the push's travel direction, on the emptier side and slightly with the push: crowd-safety advice is to move diagonally out of a surge, never against it. Inside an area a rule turned red, it leads out of the area. An open exit within ±60° of that direction and 25 m is blended in and named (`to`), else `to` is "less crowded side". The arrow is smoothed (EMA, 2 s) and only turns when the new direction is more than 30° from the one shown, so it doesn't jitter. Simulated phones get guidance too (nothing reads it yet).

**"Why did it fire?"** `GET /api/edge?from=<id>&to=<id>` explains one neighbour pair from the latest detector step: both phones' band-passed traces, the full cross-correlation curve with its peak, second peak and lag, the verdict, and every test with pass/fail and a plain-words detail (`"sway 0.41 and 0.38 m/s², need 0.15"`, `"250 ms: one person to the next"`, `"23 ms: moving together (dancing, jumping)"`, …). It is computed on request from inputs the detector keeps, so the 250 ms step doesn't pay for it. The dashboard's stats also show `detectMs` (mean step time over 5 s) and `snapshotBytes`.

Every threshold lives in one struct: `./bin/pulse -dump-config > detect.json`, edit, `./bin/pulse -config detect.json` (`detect.example.json` (repo root) holds the defaults).

The line demo (`-layout line`, 8 phones 0.6 m apart) and the crowd layout (`-layout crowd`, the default: about 70 % of phones in one or two dense groups, the rest scattered, all wandering slowly) run through the same detector. Outcomes over 20 random crowds each, ±25 ms clock error:

| Simulator scenario | Line (8 phones) | Crowd (24 phones, wandering) |
|---|---|---|
| `calm`, `walk`, `handle` | calm; handling nodes blue | calm |
| `dance` | nodes swaying, no wave edges, calm | calm |
| `shove` (one push) | yellow, decays back to calm | calm (yellow in 6/20) |
| `wave` (growing push every 2.5 s at 2.4 m/s, i.e. 250 ms per person on the line) | red in ~24 s (23–26 s), direction +x | red in 17/20 crowds at ~34 s (30–47 s), yellow in the rest |
| `wave-jump` (the same wave while everyone jumps to a beat) | red, slower: ~52 s (39–84 s) | yellow (red in 1/20): jumping noise plus free positions |
| `sway` (whole crowd sways to music at 0.5 Hz, 100 ms/person lag gradient + 50–200 ms each) | calm: periodic, lag ambiguous | calm |
| `sway-slow` (0.2 Hz ballad sway, ±19 cm) | calm: small, and the few edges don't chain | calm |
| `mexican` (stand up + arms up, 250 ms/person, every 8 s) | calm: vertical veto | calm |
| `walkpast` (one person squeezes through, one nudge per phone) | brief yellow, decays to calm | calm (yellow in 1/20) |
| `procession` (people walk past, lightly brushing about half the phones) | calm (yellow allowed) | calm |
| `march` (everyone walks off together, near-identical cadence) | calm: periodic | calm |
| `pocket` (phones pocketed, jostled, dropped, picked up) | calm | calm |
| `bump` (random neighbour pairs bump, 30–300 ms apart) | calm: isolated pairs don't chain | calm |
| `jump-stagger` (jumping to a 2 Hz beat, 0–300 ms reaction delays) | calm: periodic and vertical | calm |
| `gather` (scattered people walk to the stage over ~40 s, pack in at ~10/m², hold, leave from 75 s) | n/a | no wave; with 40 phones the cluster is *forming*, goes red on density at ~35–40 s, turns *dispersing* as people leave, then clears |

Also tested: every other phone held upside down, the false-positive scenarios over 8 random lines and 3 random crowds, high-risk areas (a single shove that stays yellow in a normal area goes red in a high-risk one), GPS conversion and privacy, and old row/col recordings replaying. `PULSE_SWEEP=1 go test ./server/internal/detect -run Sweep -v` prints outcomes over 20 crowds per scenario (`PULSE_LAYOUT=crowd PULSE_N=24` for the crowd layout, `PULSE_ONLY=wave,gather` to pick scenarios), and `PULSE_CFG='{"minChain":0}'` overrides config fields for comparisons.

## Alerts, rules, floor plans and boards

**Alerts are incidents with a stable `id`.** A zone leaving calm opens one (`status: open`) per data source, zone and kind (`wave`, `density`, `rule`). Until staff resolve it, later changes of that zone and kind update the same alert: yellow → red raises its level, and it keeps the worst level it reached (and the time it got there) when the zone calms down. The briefing (`headline` + `action`, with `brief` = both, for voice and timeline), acknowledge, resolve and escalation arrive as updates with the same `id`; the dashboard upserts by `id`. A zone returning to calm is also sent as a separate short notice (new `id`, `level: calm`, `status: resolved`) for the timeline. Resolving only closes the card; the next level change opens a new incident. A red alert nobody acknowledged `-escalate-after` (60 s) after it went red is re-broadcast with `escalated: true`, spoken again ("Still unacknowledged. …") and the sign and its light are forced red; once per alert.

**Area rules** (per drawn area, `rules` in `PUT /api/areas`): `density` (people/m², the clusters' estimate: the peak local density around the phones inside the area, counting phones just past the outline as neighbours, ÷ participation; never the area-wide average, so a packed group in a corner of a big area reads packed; an area smaller than 7 m² also uses its own phones ÷ area if higher) held for `densityHoldS` (0 = 5 s) is red, 75 % of it yellow, each clearing 10 % lower; `maxPhones` is a capacity in **people** (the field name is kept for compatibility): more estimated people inside (phones ÷ participation) than this for over 3 s is red, clearing at once when back under; `push: false` turns travelling-wave detection off for that area. These raise `kind: "rule"` alerts and merge into the area's zone level (worst of detector and rules). `message` (≤ 140 chars) replaces the briefing's action, word for word (Gemini is told to use it verbatim, and the server enforces it). `notify.sign`, `notify.light`, `notify.voice` (default on) keep the area off the worst-zone sign, off its light, or skip generating voice audio.

**One density number, one status.** Every density decision (cluster levels, early warning, area density rules, briefings) uses one estimate: people/m² at the densest well-supported spot, i.e. phones within 1.5 m ÷ 7.07 m² ÷ participation, at the larger of the 90th percentile and the densest count k that at least k/5 phones reach (so a packed patch inside a big thin crowd is not averaged away). Clusters carry it as `est`; `density` stays the thin disc average. On a test floor (20 × 14 m at 1.5 people/m², a 4 × 4 m patch at 6 people/m², 60 % participation) the disc average reads 1.1, the old 90th-percentile estimate 4.7 and `est` 6.1. `snapshot.status` is the console's single answer: the worst of zones, rules and clusters, with a 0..1 `score` (calm < 0.5 ≤ yellow < 0.8 ≤ red), the place (`where`: the drawn area holding the hotspot, else the zone's name), `kind` and `density`. Density alerts are filed under the drawn area holding the cluster's densest spot, and briefings name it and the nearest exit from the venue layout.

**Drills and "Ask the AI".** Test alerts never escalate. The history Gemini answers from starts with the live situation (overall status, active incidents), names places (never area ids) and leaves drills out except for a count.

**Floor plans.** Upload a PNG/JPEG/WebP (≤ 8 MB, type sniffed from the bytes) and it becomes the map background. **Gemini reads it**: a vision request with structured JSON output returns the venue size, stage outline, exits and walls in venue metres (origin = the image's top-left), with notes on how it judged the scale (labels, scale bars, ~0.9 m doors, typical stage sizes) and a confidence. Nothing is saved until staff apply it with `PUT /api/venue`. A venue layout with walls or exits replaces the simulator's default walls and exits when a simulation starts.

**Boards.** Staff drag signs and zone lights onto the map (`data/hardware.json`). Zone lights report the other Pulse boards they hear over Bluetooth (`peers`: beacon name, RSSI, estimated distance); when two placed boards hear each other, each peer also gets `mapDist`, the distance on the map, so a bad estimate is visible.

## HTTP API at a glance

| Endpoint | |
|---|---|
| `GET /api/config` | venue size, geo flag, zone thresholds, neighbour radius |
| `GET/PUT /api/venue` | `{w, h, lat, lon, bearing, geo, template, floorplan, layout: {stage, exits, walls}}`; layout points clamped to the venue, ≤ 64 stage points, ≤ 32 exits, ≤ 128 walls, names ≤ 40 chars; `floorplan` is read-only |
| `POST /api/venue/floorplan` | raw image body (PNG/JPEG/WebP, ≤ 8 MB) → venue; 415 for anything else, 413 if too big |
| `GET/DELETE /api/venue/floorplan` | the image (`Cache-Control: no-store`) / remove it → venue |
| `POST /api/venue/floorplan/analyze` | Gemini's reading `{w, h, layout, notes, confidence}`; 503 without `GEMINI_API_KEY`, 404 without a plan, 502 if Gemini fails; saves nothing |
| `GET/PUT /api/areas` | watch areas `{id, name, sens, poly, light, rules}` (`data/areas.json`) |
| `POST /api/alerts/{id}/ack`, `/resolve` | optional body `{"by"}` (ack) / `{"by", "note"}` (resolve; `by` ≤ 60, `note` ≤ 280 chars, else 400; an empty body still works) → the updated alert with `ackBy` / `resolvedBy` / `note` (also broadcast to dashboards, kept in the log); 404 for an unknown id |
| `POST /api/alerts/clear` | drops resolved and test alerts from the log, keeps open and acknowledged real incidents → `{"type":"alerts","alerts":[…]}`, also broadcast |
| `GET/PUT /api/join` | the URL the QR code points at: `{url, reachable: public\|lan\|local, secure, source: settings\|env\|tunnel\|request, display, problem}`. Order: a link set in the dashboard's QR window (`PUT {"url"}`, saved in `data/join.json`, `""` clears), `PUBLIC_URL`, a running Cloudflare quick tunnel (auto-detected), the dashboard's own address |
| `POST /api/join/test` | `{url?}` → the server fetches the join link itself: `{ok, status, ms, secure, pulse: this\|other, message}` |
| `POST /api/join/report`, `GET /api/join/stats` | phones report why joining failed (`inapp`, `insecure`, `motion-denied`, `no-motion`, …); stats: `{joined, streaming, problems}` |
| `GET /api/hardware` | signs and zone lights: online, Wi-Fi, BLE counts, `x, y`, `beacon`, `peers` |
| `PUT /api/hardware/{key}/pos` | `{x, y}` (key `sign` or a light letter) → hardware list; 404 for an unknown board |
| `GET /api/node/{id}` | one phone's details and last 30 s of readings |
| `GET /api/edge?from=&to=` | why the detector did or didn't call a neighbour pair a wave: traces, cross-correlation curve, every check; 404 if not a neighbour pair |
| `GET /api/sim`, `POST /api/sim/start`, `/stop`, `/action` | crowd simulation ([docs/REFERENCE.md](REFERENCE.md)) |
| `GET /api/recordings`, `POST /api/record/start`, `/record/stop`, `/replay`, `/live` | recordings and replay |
| `POST /api/test-alert`, `POST /api/ask`, `GET /api/status`, `GET /api/qr.png`, `GET /api/phone-url` | drill, questions to Gemini, service status, join QR (`phone-url` is plain text) |

## Arduino sign

`arduino/sign/sign.ino` for the Uno R4 WiFi: copy `arduino_secrets.h.example` to `arduino_secrets.h`, add Wi-Fi, flash, read the IP off the serial monitor, set `SIGN_URL=http://<ip>`. It serves `GET /level?v=calm|yellow|red&zone=B`: calm = heartbeat, yellow = `!`, red = flashing arrow + STOP (and pin 7 high for a buzzer/LED).

**Over USB, no Wi-Fi:** `SIGN_URL=serial:auto,A=serial:auto,B=serial:auto` drives the sign and both zone lights through their cables at 115200 baud: one `L <calm|yellow|red> <zone>` line per level change, `S` for a `/pulse`-shaped status line, `W <ssid> <password>` to save a Wi-Fi network. `serial:auto` asks each USB port which board it is (the two ESP32s share a USB ID), reconnects if a cable comes out, and re-sends the current level. Wi-Fi is optional on every board and retried in the background (several networks, e.g. home + hotspot). A server inside WSL can't see USB ports without `usbipd`, so USB mode is for the Mac at the demo. `scripts/boards.sh status|flash|wifi|env` does the rest. Details: [docs/SETUP.md](SETUP.md), [docs/TABLE-DEMO.md](TABLE-DEMO.md).

## Evaluation and load test

`make eval` (`go run ./server/cmd/eval -seeds 20`) runs every simulator scenario in the line and crowd layouts over 20 random crowds, plus five crowd-simulation scripts with ground-truth lead times, through the real detector and density tracker in parallel (about 20 s on 32 threads). It writes [docs/EVAL.md](EVAL.md) (table, method, limits) and `docs/eval.json` (the `EvalReport` shape in `web/shared/protocol.ts`). Latest: **0 false alarms in 600 look-alike runs; 22 of 160 true-positive runs missed** (19 of them `wave-jump` in a crowd, 3 `wave` in a crowd). These are simulated signals, scored with thresholds that were tuned on the same simulator, so they are not real-world validation.

`make loadtest N=1000` (`go run ./server/cmd/loadtest -n 1000 -url ws://localhost:8080/ws/phone -duration 60s`; `-n 500,1000` runs both) connects fake phones to a running server over the real WebSocket: hello, clock-sync replies, 10 Hz calm motion, joining over 10 s. One dashboard client times the snapshots and `/api/status` is polled. Results and machine details go to [docs/loadtest.md](loadtest.md). On a Ryzen 9 9950X (WSL2), 1000 phones all connected and none dropped: 10,000 messages/s went in, snapshots stayed at 10 Hz (worst gap 111 ms), and `/api/status` answered in 0.5 ms at the median (1.3 ms worst). The phones are real to the server, so in a small venue they are dense enough to raise density alerts.
