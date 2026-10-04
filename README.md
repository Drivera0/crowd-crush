# Pulse

**Early warning for crowd crushes, using the phones already in the crowd.**

Phones in a crowd stream their motion to one Go server. Each phone is a dot on the venue map (dragged to where its owner stands, or placed by GPS). When physically neighbouring phones start swaying together in a wave that travels person to person, the zone goes yellow then red, Gemini writes a briefing, ElevenLabs speaks it, and an Arduino sign flashes. Dancing and jumping (everyone moving *together*) don't trigger it; a push travelling *through the crowd* does. Pulse also watches where people bunch up: a group packing past a safe density raises its own alert.

Math detects, AI explains. Every external service fails soft: with no keys and no internet, Pulse still detects, records, speaks (browser voice) and alerts.

## Quick start

```sh
make build          # npm install + build the two web apps + Go binaries
./bin/pulse         # http://localhost:8080/ (phone)  http://localhost:8080/dash/ (dashboard)
make sim            # 24 fake phones in a crowd, "wave" scenario, in another terminal
```

**Laptop setup (keys, .tech domain, flashing boards): [docs/SETUP.md](docs/SETUP.md).** Needs Go 1.25+ and Node 20+. `make test` runs the Go tests (detector scenarios, recordings, clock sync, sponsor clients).

### Real phones need HTTPS

Motion sensors only work on HTTPS. Easiest is a Cloudflare quick tunnel:

```sh
make tunnel         # cloudflared tunnel --url http://localhost:8080 → https://<random>.trycloudflare.com
```

For the demo, point the `.tech` domain at a named tunnel and set `PUBLIC_URL=https://<name>.tech` so the dashboard QR code shows it (otherwise the QR uses whatever host the dashboard was opened on — open the dashboard through the tunnel URL and it just works).

**Test on a real iPhone first**: tap Join → allow Motion & Orientation → drag your dot to where you stand → hold the phone flat on your chest. The dashboard node should go green within a second or two.

### Secrets

Copy `.env.example` to `.env` (git-ignored); the server reads it on start. All optional:

| Variable | Without it |
|---|---|
| `TIGER_DATABASE_URL` | readings recorded to `recordings/auto/*.jsonl` |
| `GEMINI_API_KEY` (`GEMINI_MODEL`) | template briefing sentence |
| `ELEVENLABS_API_KEY`, `ELEVENLABS_VOICE_ID` | dashboard uses the browser's speech synthesis |
| `SIGN_URL` | no sign (boards on USB: `serial:auto,A=serial:auto,B=serial:auto`; or Wi-Fi URLs: see docs/SETUP.md, docs/TABLE-DEMO.md) |
| `PUBLIC_URL` | QR code uses a running quick tunnel if one is found, else the dashboard's own host (a link pasted in the dashboard's QR window wins over both, and over `PUBLIC_URL`) |
| `VENUE_W`, `VENUE_H` | 24 × 16 m venue |
| `VENUE_LAT`, `VENUE_LON`, `VENUE_BEARING` | no geo-anchor: GPS fixes are ignored and phones are placed by hand (set it later with `PUT /api/venue`; it's saved in `data/venue.json`, which wins over the env on restart) |

The dashboard header shows which services are live.

## Demo script (~60 s)

1. Open `/dash/` full-screen (⛶). The QR shows while nobody has joined. Click **Enable sound** once (browsers block audio until a click).
2. People join, drag their dot to where they stand (a line works well), phones flat on chest. Nodes appear green; hover one for RTT and clock offset.
3. Someone checks their phone → that node goes **blue** (handling: readings ignored).
4. Everyone jumps together → nodes may go **yellow** (swaying) but no wave edges, zone stays calm.
5. Push the end of the line repeatedly → pulses race along the edges, nodes go **red**, zone goes red, the briefing plays, the sign flashes.
6. Safety net: **Simulation → Saved runs** → `sim-wave` (or a recorded real run) plays through the same pipeline. **Alert drill → Send drill** fires the alert chain on demand and reports what each output did.

Record real runs early with **Record run** and a label that says what happened (`wave-push-end`, `dance-jumping`, `calm-standing`): they land in `recordings/`, show up in the replay picker, and `go test ./server/internal/app` checks each one reaches the level its label promises.

## How it works

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

Every threshold lives in one struct: `./bin/pulse -dump-config > detect.json`, edit, `./bin/pulse -config detect.json` (`detect.example.json` holds the defaults).

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

### Crowd simulator: simulated people instead of scripted signals

`server/internal/crowdsim` runs simulated people in the server process: a Social Force Model (Helbing & Molnár 1995; Helbing, Farkas & Vicsek 2000), with bodies as discs (social repulsion, body compression, sliding friction, walls), a stage barrier and four exits. A crowd crush *emerges* from it. The dashboard steers it (`calm`, `stage`, `surge`, `attract`, `shove`, `exit`, `disperse`, `spawn`, `dance`, `intermission`; API in docs/REFERENCE.md). 60 % of the simulated people carry a phone. They go through the same pipeline as real phones. The rest are invisible to Pulse but still push.

**People behave like a concert audience.** 60 % arrive in groups of 2–4 (Moussaïd et al. 2010, PLoS ONE). A group shares its plans and walks together: side by side when there's room, bent into a V or U as it gets denser, at the pace of its slowest member. During the show (`calm`) most people stand still facing the stage. Standing people are planted: their velocity is damped and the small social forces from their neighbours are ignored below 150 N, so a standing crowd doesn't jiggle. Body contact and shoves always act. A quarter of the people sway gently to the music. Now and then a group walks to the bar, toilets or merch stand (defaults on the side and back walls), stays 30–120 s and comes back to about where it stood. A few people arrive and leave through the exits all the time (about 6 a minute each way for 250 people). `dance`: everyone standing sways to one beat and most bounce, each with their own delay (0–300 ms) and amplitude. This is the false-positive test. `intermission`: the music stops, and 55 % of the groups head for the POIs within 25 s and come back later. That gives two-way flow and crowding around the bar. `stage`/`surge` move groups forward together, `attract` sends whole groups, and `disperse` sends each group to its nearest exit. Motion is smooth. Desired speed rises at most 1 m/s², headings turn at most 2 rad/s while walking, and bodies turn to face where they're going (or the stage).

**Phones from bodies.** Each phone reads its body's acceleration from the forces, rotated into the frame the body faces. On top of that come gait while walking; breathing and occasional weight shifts while standing; beat sway (and bounce on `dance`); sensor noise; and occasional handling. A crush shows up as compression. Sim phones report their position every 100 ms (10 Hz) whenever it changed by at least 1 cm. There's no movement threshold, so the dashboard shows the bodies' true motion. This costs the pipeline nothing measurable: the detect step takes ~0.6 ms at 250 people and ~2.4 ms at 500, the same at 2 Hz positions (`PULSE_POSCOST=1 go test ./server/internal/app -run SimPositionCost -v`).

**Validation** (method and tables in the package doc):
- *Weidmann's fundamental diagram* (1993), the benchmark Vadere and JuPedSim are checked against. In a periodic corridor, mean walking speed is within ±0.10 m/s of Weidmann from 0.5 to 5 people/m², except at 2.5/m² (−0.13). The calibration needed a time gap for walkers (0.6 s). The Helbing 2000 forces alone don't slow a walking crowd until ~4/m².
- *Bottleneck:* 120 people leave a room through a 1 m door at **1.6 persons/(m·s)** (1.50–1.72 by seed). Kretz et al. (2006) and Seyfried et al. (2009) measured ~1.6–1.9. People leaving use a 0.3 s time gap, chosen for this. The specific flow grows with door width here (0.8 m: 1.3, 1.2 m: 2.0), while the experiments find it roughly constant.
- *Counterflow:* in a corridor with half the people walking each way at 1/m², lanes form (lane order 0.06–0.15 → 0.70–0.82). People walk at 67–87 % of the one-way speed. At 2/m² lanes form only partly (→ 0.38–0.51) and speed falls to 49–58 %. That's a bigger loss than experiments show, so this counts as a sanity check, not a calibration.
- *Behaviour:* a standing crowd's mean speed is < 0.001 m/s. Walking group members stay ~0.7 m from their group's centre. `dance` through the full pipeline (250 people, 55 s) gives no red alert. Many phones read "swaying", 2–9 briefly read as part of a wave, and there are one or two yellow density incidents from the packed front (truth ≤ 2.5/m²). `intermission` behaves the same, with the POIs at up to ~3/m².
- *Limits:* groups never split up. POIs have no queue, so people just stand around them. Standing people never shuffle their feet. People always pass each other on the right. Sway and dance exist only in the phone signal, not in body positions. Bodies are discs. Evacuation times and panic were not validated.

**Ground truth and lead time.** The simulation knows what Pulse has to guess: per-person pressure (Helbing's injury level is 1600 N/m) and local density. `GET /api/sim` reports when the truth first became dangerous, when Pulse first went red, and the difference (`leadSeconds`, positive = Pulse warned first). Results over 5 seeds (250 people, `PULSE_SIMSWEEP=1 go test ./server/internal/app -run SimLeadSweep -v`):

| Script | Truth dangerous | Pulse red (all density alerts) | Lead |
|---|---|---|---|
| calm 30 s → `surge` 0.7 + a shove every 3 s | 33.0–33.7 s | 35.0–35.8 s | −1.3 to −2.6 s (Pulse ~2 s *late*) |
| `stage` at 5 s → `surge` 0.7 + shoves at 30 s | 30.6–31.1 s | 11.0–12.8 s (all seeds, during `stage`) | +18.3 to +19.7 s |
| `stage` → `surge` 0.3 | 30.7–36.2 s | same | +18.7 to +23.4 s |

**With the early warning** (`calm → surge 0.7 + shoves`, 5 seeds, first density alert after the surge vs the truth turning dangerous; `PULSE_SIMSWEEP=1 go test ./server/internal/app -run SimEarlySweep -v`):

| | Seed 1 | 2 | 3 | 4 | 5 |
|---|---|---|---|---|---|
| truth dangerous | 34.2 s | 33.3 s | 33.1 s | 33.4 s | 33.5 s |
| first alert, before (`earlyWarnS: 0`) | 35.25 (−1.05) | 35.75 (−2.45) | 33.75 (−0.65) | 34.25 (−0.85) | 34.0 (−0.5) |
| first alert, with early warning | **33.0 (+1.2)** | **33.5 (−0.2)** | **33.0 (+0.1)** | **33.25 (+0.15)** | 34.0 (−0.5, no early) |
| red (unchanged) | 35.5 | 35.75 | 35.25 | 35.5 | 35.5 |

The first warning moves 0.75–2.25 s earlier on 4 of 5 seeds and lands at or before the truth turning dangerous on 3. The surge goes from calm to dangerous in about 3 s, which an 8 s trend can only partly anticipate. `calm` (120 s × 5 seeds): no early warning (the yellow density alerts some calm seeds already showed are unchanged). `attract`: yellow 0.25–1.25 s earlier on 4 seeds, never red. (Measured with the crowd simulator as of this change; its realism is still being worked on.)

Read it honestly. A sudden surge into a loose crowd is caught ~2 s after the crowd turns dangerous: the density has to persist for 2 s, and the cluster has to fill. The large positive leads happen when `stage` crowding already passes Pulse's 4/m² danger threshold (true density ~5/m², pressure still below 1600 N/m), so Pulse is red before the surge starts. That's an early warning by Pulse's own threshold, not a prediction of the surge. In `calm`, `dance` and `intermission` there is no red alert (seeds 1–3, 120 s; `PULSE_SIMSWEEP=1 go test ./server/internal/app -run SimBehaviourSweep -v`). Each run shows one or two yellow density incidents. Friends standing together at the front reach a true ~2.5/m² (2.9/m² around the bar at intermission), above Pulse's 2/m² watch level. In `attract` (a group forming around a point) there is yellow density only.

**What the simulator says about wave detection.** In this model the travelling-wave detector almost never fires. Bodies in a packed crowd are stiff (k = 1.2·10⁵ kg/s²), so a push crosses neighbours in tens of milliseconds, under the detector's 120 ms per-hop floor, and reads as moving together. In a loose crowd a push dies out within ~2 m. Either real crowds transmit pushes more slowly than stiff discs do (people are compliant and step), or the wave detector needs a lower lag floor for packed crowds. Recordings of real pushes would settle it.

### Alerts, rules, floor plans and boards

**Alerts are incidents with a stable `id`.** A zone leaving calm opens one (`status: open`) per data source, zone and kind (`wave`, `density`, `rule`). Until staff resolve it, later changes of that zone and kind update the same alert: yellow → red raises its level, and it keeps the worst level it reached (and the time it got there) when the zone calms down. The briefing (`headline` + `action`, with `brief` = both, for voice and timeline), acknowledge, resolve and escalation arrive as updates with the same `id`; the dashboard upserts by `id`. A zone returning to calm is also sent as a separate short notice (new `id`, `level: calm`, `status: resolved`) for the timeline. Resolving only closes the card; the next level change opens a new incident. A red alert nobody acknowledged `-escalate-after` (60 s) after it went red is re-broadcast with `escalated: true`, spoken again ("Still unacknowledged. …") and the sign and its light are forced red; once per alert.

**Area rules** (per drawn area, `rules` in `PUT /api/areas`): `density` (people/m², the clusters' estimate: the peak local density around the phones inside the area, counting phones just past the outline as neighbours, ÷ participation; never the area-wide average, so a packed group in a corner of a big area reads packed; an area smaller than 7 m² also uses its own phones ÷ area if higher) held for `densityHoldS` (0 = 5 s) is red, 75 % of it yellow, each clearing 10 % lower; `maxPhones` is a capacity in **people** (the field name is kept for compatibility): more estimated people inside (phones ÷ participation) than this for over 3 s is red, clearing at once when back under; `push: false` turns travelling-wave detection off for that area. These raise `kind: "rule"` alerts and merge into the area's zone level (worst of detector and rules). `message` (≤ 140 chars) replaces the briefing's action, word for word (Gemini is told to use it verbatim, and the server enforces it). `notify.sign`, `notify.light`, `notify.voice` (default on) keep the area off the worst-zone sign, off its light, or skip generating voice audio.

**One density number, one status.** Every density decision (cluster levels, early warning, area density rules, briefings) uses one estimate: people/m² at the densest well-supported spot, i.e. phones within 1.5 m ÷ 7.07 m² ÷ participation, at the larger of the 90th percentile and the densest count k that at least k/5 phones reach (so a packed patch inside a big thin crowd is not averaged away). Clusters carry it as `est`; `density` stays the thin disc average. On a test floor (20 × 14 m at 1.5 people/m², a 4 × 4 m patch at 6 people/m², 60 % participation) the disc average reads 1.1, the old 90th-percentile estimate 4.7 and `est` 6.1. `snapshot.status` is the console's single answer: the worst of zones, rules and clusters, with a 0..1 `score` (calm < 0.5 ≤ yellow < 0.8 ≤ red), the place (`where`: the drawn area holding the hotspot, else the zone's name), `kind` and `density`. Density alerts are filed under the drawn area holding the cluster's densest spot, and briefings name it and the nearest exit from the venue layout.

**Drills and "Ask the AI".** Test alerts never escalate. The history Gemini answers from starts with the live situation (overall status, active incidents), names places (never area ids) and leaves drills out except for a count.

**Floor plans.** Upload a PNG/JPEG/WebP (≤ 8 MB, type sniffed from the bytes) and it becomes the map background. **Gemini reads it**: a vision request with structured JSON output returns the venue size, stage outline, exits and walls in venue metres (origin = the image's top-left), with notes on how it judged the scale (labels, scale bars, ~0.9 m doors, typical stage sizes) and a confidence. Nothing is saved until staff apply it with `PUT /api/venue`. A venue layout with walls or exits replaces the simulator's default walls and exits when a simulation starts.

**Boards.** Staff drag signs and zone lights onto the map (`data/hardware.json`). Zone lights report the other Pulse boards they hear over Bluetooth (`peers`: beacon name, RSSI, estimated distance); when two placed boards hear each other, each peer also gets `mapDist`, the distance on the map, so a bad estimate is visible.

### HTTP API

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
| `GET /api/sim`, `POST /api/sim/start`, `/stop`, `/action` | crowd simulation (docs/REFERENCE.md) |
| `GET /api/recordings`, `POST /api/record/start`, `/record/stop`, `/replay`, `/live` | recordings and replay |
| `POST /api/test-alert`, `POST /api/ask`, `GET /api/status`, `GET /api/qr.png`, `GET /api/phone-url` | drill, questions to Gemini, service status, join QR (`phone-url` is plain text) |

## Layout

```
server/cmd/pulse      the server (flags: -venue-w -venue-h -venue-lat -venue-lon -venue-bearing -zone-cols -zone-rows -config -data -escalate-after -addr …)
server/cmd/sim        fake phones: -scenario wave [-layout crowd|line] [-n 24] [-move=false] [-rows -cols for the line]
                      [-out file.jsonl for offline recordings, with hello x/y and pos records]
server/cmd/dashtail   dashboard snapshots in a terminal
server/cmd/eval       evaluation report: every scenario × N random crowds → docs/eval.json, docs/EVAL.md (-seeds 20)
server/cmd/loadtest   N fake phones against a running server → docs/loadtest.md (-n 1000 -duration 60s)
server/internal/      hub, clocksync, detect, crowd (clusters), geo (GPS → metres), store, brief, voice, sign, protocol, app, sim,
                      crowdsim (Social Force Model crowd, in-process: POST /api/sim/start)
data/                 saved venue (size, anchor, layout, floor plan), staff-drawn areas, board positions (git-ignored)
web/phone             phone page (Vite + TS)       web/dashboard   dashboard (Vite + TS, SVG)
web/shared            protocol.ts — mirror of server/internal/protocol
arduino/sign          Uno R4 WiFi sign sketch
recordings/           labelled runs for tuning, tests and replay
```

Web dev with hot reload: run `./bin/pulse`, then `cd web && npm run dev:dash` (or `dev:phone`); Vite proxies the API and sockets to :8080.

## Arduino sign

`arduino/sign/sign.ino` for the Uno R4 WiFi: copy `arduino_secrets.h.example` to `arduino_secrets.h`, add Wi-Fi, flash, read the IP off the serial monitor, set `SIGN_URL=http://<ip>`. It serves `GET /level?v=calm|yellow|red&zone=B`: calm = heartbeat, yellow = `!`, red = flashing arrow + STOP (and pin 7 high for a buzzer/LED).

**Over USB, no Wi-Fi:** `SIGN_URL=serial:auto,A=serial:auto,B=serial:auto` drives the sign and both zone lights through their cables at 115200 baud: one `L <calm|yellow|red> <zone>` line per level change, `S` for a `/pulse`-shaped status line, `W <ssid> <password>` to save a Wi-Fi network. `serial:auto` asks each USB port which board it is (the two ESP32s share a USB ID), reconnects if a cable comes out, and re-sends the current level. Wi-Fi is optional on every board and retried in the background (several networks, e.g. home + hotspot). A server inside WSL can't see USB ports without `usbipd`, so USB mode is for the Mac at the demo. `scripts/boards.sh status|flash|wifi|env` does the rest. Details: [docs/SETUP.md](docs/SETUP.md), [docs/TABLE-DEMO.md](docs/TABLE-DEMO.md).

## Evaluation and load test

`make eval` (`go run ./server/cmd/eval -seeds 20`) runs every simulator scenario in the line and crowd layouts over 20 random crowds, plus five crowd-simulation scripts with ground-truth lead times, through the real detector and density tracker in parallel (about 20 s on 32 threads). It writes [docs/EVAL.md](docs/EVAL.md) (table, method, limits) and `docs/eval.json` (the `EvalReport` shape in `web/shared/protocol.ts`). Latest: **0 false alarms in 600 look-alike runs; 22 of 160 true-positive runs missed** (19 of them `wave-jump` in a crowd, 3 `wave` in a crowd). These are simulated signals, scored with thresholds that were tuned on the same simulator, so they are not real-world validation.

`make loadtest N=1000` (`go run ./server/cmd/loadtest -n 1000 -url ws://localhost:8080/ws/phone -duration 60s`; `-n 500,1000` runs both) connects fake phones to a running server over the real WebSocket: hello, clock-sync replies, 10 Hz calm motion, joining over 10 s. One dashboard client times the snapshots and `/api/status` is polled. Results and machine details go to [docs/loadtest.md](docs/loadtest.md). On a Ryzen 9 9950X (WSL2), 1000 phones all connected and none dropped: 10,000 messages/s went in, snapshots stayed at 10 Hz (worst gap 111 ms), and `/api/status` answered in 0.5 ms at the median (1.3 ms worst). The phones are real to the server, so in a small venue they are dense enough to raise density alerts.

## Privacy

Random session ID + position in the venue + motion numbers. No names, contacts, location history, audio or photos. Positions are venue-relative metres only: the current one is used live and stored in recordings (and Tiger readings) so runs can be replayed. GPS is converted to venue metres the moment it arrives; latitude and longitude are never stored, logged or sent to the dashboard.

## Honest limits

- A web page only streams while open; a real event would build this into its official app.
- Phone-to-phone relay when the cell network jams needs a native app.
- Locating phones in a real crowd is the hard real-world problem. Phone GPS is good to 5–25 m outdoors and worse indoors, far coarser than the 1.1 m neighbour radius, so the demo places people by hand (drag your dot); GPS suits open-air venues and coarse clusters.
- Density counts phones, so its people/m² is only as good as the `participation` estimate.
- A handful of testers proves the method, not the thresholds for 50,000 people.
