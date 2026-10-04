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
| `SIGN_URL` | no sign (Arduino + ESP32 zone lights: see docs/SETUP.md) |
| `PUBLIC_URL` | QR code uses the dashboard's own host |
| `VENUE_W`, `VENUE_H` | 24 × 16 m venue |
| `VENUE_LAT`, `VENUE_LON`, `VENUE_BEARING` | no geo-anchor: GPS fixes are ignored and phones are placed by hand (set it later with `PUT /api/venue`; it's saved in `data/venue.json`, which wins over the env on restart) |

The dashboard header shows which services are live.

## Demo script (~60 s)

1. Open `/dash/` full-screen (⛶). The QR shows while nobody has joined. Click **Enable sound** once (browsers block audio until a click).
2. People join, drag their dot to where they stand (a line works well), phones flat on chest. Nodes appear green; hover one for RTT and clock offset.
3. Someone checks their phone → that node goes **blue** (handling: readings ignored).
4. Everyone jumps together → nodes may go **yellow** (swaying) but no wave edges, zone stays calm.
5. Push the end of the line repeatedly → pulses race along the edges, nodes go **red**, zone goes red, the briefing plays, the sign flashes.
6. Safety net: **Replay** → `sim-wave.jsonl` (or a recorded real run) plays through the same pipeline. **Test alert** fires the whole alert chain on demand.

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

`server/internal/crowdsim` runs simulated people in the server process: a Social Force Model (Helbing & Molnár 1995; Helbing, Farkas & Vicsek 2000), with bodies as discs (social repulsion, body compression, sliding friction, walls), a stage barrier and four exits. A crowd crush *emerges* from it. The dashboard steers it (`calm`, `stage`, `surge`, `attract`, `shove`, `exit`, `disperse`, `spawn`; API in docs/REFERENCE.md). 60 % of the simulated people carry a phone. Each phone's readings are its body's acceleration from the forces, rotated into the chest frame, plus gait, breathing, sensor noise and occasional handling. They go through the same pipeline as real phones. The rest are invisible to Pulse but still push.

**Calibrated against Weidmann's fundamental diagram** (1993), the standard benchmark that Vadere and JuPedSim are checked against. In a periodic corridor, mean walking speed is within ±0.10 m/s of Weidmann from 1 to 5 people/m², except 2.5/m² (−0.14). Free walking at 0.5/m² is 0.15 m/s slow (desired speeds average 1.3 m/s). Only unidirectional corridor flow was checked; table and method are in the package doc. The calibration needed a time gap for walkers (0.6 s; the Helbing 2000 forces alone don't slow a walking crowd until ~4/m²).

**Ground truth and lead time.** The simulation knows what Pulse has to guess: per-person pressure (Helbing's injury level is 1600 N/m) and local density. `GET /api/sim` reports when the truth first became dangerous, when Pulse first went red, and the difference (`leadSeconds`, positive = Pulse warned first). Results over 5 seeds (250 people, `PULSE_SIMSWEEP=1 go test ./server/internal/app -run SimLeadSweep -v`):

| Script | Truth dangerous | Pulse red (all density alerts) | Lead |
|---|---|---|---|
| calm 30 s → `surge` 0.7 + a shove every 3 s | 32–33 s | 34–35 s | −2.1 to −2.7 s (Pulse 2 s *late*) |
| `stage` at 5 s → `surge` 0.7 + shoves at 30 s | 30 s | 3 seeds: 12–13 s (during `stage`); 2 seeds: 31–32 s | +17 to +19 s, or −0.8 to −1.9 s |
| `stage` → `surge` 0.3 | 30–32 s | same pattern | +17 to +19 s, or −0.4 to −0.8 s |

Read it honestly. A sudden surge into a loose crowd is caught ~2 s after the crowd turns dangerous: the density has to persist for 2 s, and the cluster has to fill. The large positive leads happen when `stage` crowding already passes Pulse's 4/m² danger threshold (true density ~5/m², pressure still below 1600 N/m), so Pulse is red before the surge starts. That's an early warning by Pulse's own threshold, not a prediction of the surge. In `calm` there is no red alert (and on seeds 1–3 over 120 s, no density alert at all). In `attract` (a group forming around a point) there is yellow density only.

**What the simulator says about wave detection.** In this model the travelling-wave detector almost never fires. Bodies in a packed crowd are stiff (k = 1.2·10⁵ kg/s²), so a push crosses neighbours in tens of milliseconds, under the detector's 120 ms per-hop floor, and reads as moving together. In a loose crowd a push dies out within ~2 m. Either real crowds transmit pushes more slowly than stiff discs do (people are compliant and step), or the wave detector needs a lower lag floor for packed crowds. Recordings of real pushes would settle it.

## Layout

```
server/cmd/pulse      the server (flags: -venue-w -venue-h -venue-lat -venue-lon -venue-bearing -zone-cols -zone-rows -config -data -addr …)
server/cmd/sim        fake phones: -scenario wave [-layout crowd|line] [-n 24] [-move=false] [-rows -cols for the line]
                      [-out file.jsonl for offline recordings, with hello x/y and pos records]
server/cmd/dashtail   dashboard snapshots in a terminal
server/internal/      hub, clocksync, detect, crowd (clusters), geo (GPS → metres), store, brief, voice, sign, protocol, app, sim,
                      crowdsim (Social Force Model crowd, in-process: POST /api/sim/start)
data/                 saved venue anchor and staff-drawn areas (git-ignored)
web/phone             phone page (Vite + TS)       web/dashboard   dashboard (Vite + TS, SVG)
web/shared            protocol.ts — mirror of server/internal/protocol
arduino/sign          Uno R4 WiFi sign sketch
recordings/           labelled runs for tuning, tests and replay
```

Web dev with hot reload: run `./bin/pulse`, then `cd web && npm run dev:dash` (or `dev:phone`); Vite proxies the API and sockets to :8080.

## Arduino sign

`arduino/sign/sign.ino` for the Uno R4 WiFi: copy `arduino_secrets.h.example` to `arduino_secrets.h`, add Wi-Fi, flash, read the IP off the serial monitor, set `SIGN_URL=http://<ip>`. It serves `GET /level?v=calm|yellow|red&zone=B`: calm = heartbeat, yellow = `!`, red = flashing arrow + STOP (and pin 7 high for a buzzer/LED).

## Privacy

Random session ID + position in the venue + motion numbers. No names, contacts, location history, audio or photos. Positions are venue-relative metres only: the current one is used live and stored in recordings (and Tiger readings) so runs can be replayed. GPS is converted to venue metres the moment it arrives; latitude and longitude are never stored, logged or sent to the dashboard.

## Honest limits

- A web page only streams while open; a real event would build this into its official app.
- Phone-to-phone relay when the cell network jams needs a native app.
- Locating phones in a real crowd is the hard real-world problem. Phone GPS is good to 5–25 m outdoors and worse indoors, far coarser than the 1.1 m neighbour radius, so the demo places people by hand (drag your dot); GPS suits open-air venues and coarse clusters.
- Density counts phones, so its people/m² is only as good as the `participation` estimate.
- A handful of testers proves the method, not the thresholds for 50,000 people.
