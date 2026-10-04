# Pulse

**Early warning for crowd crushes, using the phones already in the crowd.**

Phones in a crowd stream their motion to one Go server. When neighbouring phones start swaying together in a wave that travels person to person, the zone goes yellow then red, Gemini writes a briefing, ElevenLabs speaks it, and an Arduino sign flashes. Dancing and jumping (everyone moving *together*) don't trigger it; a push travelling *down the line* does.

Math detects, AI explains. Every external service fails soft: with no keys and no internet, Pulse still detects, records, speaks (browser voice) and alerts.

## Quick start

```sh
make build          # npm install + build the two web apps + Go binaries
./bin/pulse         # http://localhost:8080/ (phone)  http://localhost:8080/dash/ (dashboard)
make sim            # 8 fake phones, "wave" scenario, in another terminal
```

**Laptop setup (keys, .tech domain, flashing boards): [docs/SETUP.md](docs/SETUP.md).** Needs Go 1.25+ and Node 20+. `make test` runs the Go tests (detector scenarios, recordings, clock sync, sponsor clients).

### Real phones need HTTPS

Motion sensors only work on HTTPS. Easiest is a Cloudflare quick tunnel:

```sh
make tunnel         # cloudflared tunnel --url http://localhost:8080 → https://<random>.trycloudflare.com
```

For the demo, point the `.tech` domain at a named tunnel and set `PUBLIC_URL=https://<name>.tech` so the dashboard QR code shows it (otherwise the QR uses whatever host the dashboard was opened on — open the dashboard through the tunnel URL and it just works).

**Test on a real iPhone first**: tap Join → allow Motion & Orientation → tap your spot → hold the phone flat on your chest. The dashboard node should go green within a second or two.

### Secrets

Copy `.env.example` to `.env` (git-ignored); the server reads it on start. All optional:

| Variable | Without it |
|---|---|
| `TIGER_DATABASE_URL` | readings recorded to `recordings/auto/*.jsonl` |
| `GEMINI_API_KEY` (`GEMINI_MODEL`) | template briefing sentence |
| `ELEVENLABS_API_KEY`, `ELEVENLABS_VOICE_ID` | dashboard uses the browser's speech synthesis |
| `SIGN_URL` | no sign (Arduino + ESP32 zone lights: see docs/SETUP.md) |
| `PUBLIC_URL` | QR code uses the dashboard's own host |

The dashboard header shows which services are live.

## Demo script (~60 s)

1. Open `/dash/` full-screen (⛶). The QR shows while nobody has joined. Click **Enable sound** once (browsers block audio until a click).
2. People join and stand in a line, phones flat on chest. Nodes appear green; hover one for RTT and clock offset.
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

**Detection** (`server/internal/detect`), per phone on clock-corrected 10 Hz summaries:

1. **Handling**: rotation > 200°/s → readings ignored until 1 s of quiet.
2. **Band-pass** x (left/right with the phone upright on the chest) to 0.15–1.5 Hz: sway is slow.
3. **Sway** = RMS over 5 s; above 0.25 m/s² → *swaying*.

Per pair of grid neighbours, cross-correlate the last 6 s at lags ±1.5 s. A **wave edge** needs |correlation| ≥ 0.6 at a lag of 120–1200 ms, and that peak must be *unambiguous*: periodic motion like walking or swaying to music has several equally good lags, so the best peak must beat any other by 0.2. Lag ≈ 0 means moving together (dancing) and is never a wave.

Two guards keep look-alikes out:

- **Vertical veto** (`verticalRatio`): a push is horizontal. If the phones' vertical (y) motion is stronger than the horizontal, isn't rhythmic (its autocorrelation doesn't come back above 0.6 within 1.5 s, unlike jumping or walking to a beat), and travels between the pair by itself (|corr| ≥ 0.6 at a 120–1200 ms lag, same direction), it's people standing up in sequence: a stadium Mexican wave whose lean and tilt leak into x. A crowd jumping on the spot while a push goes through is rhythmic, so it never vetoes the push.
- **Chains** (`minChain`, `chainCorr`): a crowd wave passes person to person to person. A wave edge only counts if it's part of a run of ≥ 3 phones along a row or column travelling the same way. The other hops only need to *support* it (|corr| ≥ 0.4 at a wave-like lag, same direction), like hysteresis in edge linking, so one noisy hop doesn't break a real wave. Two neighbours bumping into each other make an isolated edge and are dropped.

Per zone: score = net fraction of edges carrying a wave in one direction, smoothed (8 s). Yellow above 0.3 for 2 s, red above 0.6 for 2 s, clearing 0.1 lower (hysteresis). Red already needs persistence: a single travelling event shows in the 6 s correlation window for at most ~6 s, which takes the 8 s-smoothed score to ~0.5 at most, so one shove or one person squeezing past can reach yellow but never red.

Every threshold lives in one struct: `./bin/pulse -dump-config > detect.json`, edit, `./bin/pulse -config detect.json`.

| Simulator scenario | Detector outcome (tested) |
|---|---|
| `calm`, `walk`, `handle` | calm; handling nodes blue |
| `dance` | nodes swaying, no wave edges, calm |
| `shove` (one push) | yellow at most, decays back to calm |
| `wave` (growing push every 2.5 s, 250 ms/person) | red in ~25 s (23–26 s over 20 crowds), direction left → right |
| `wave-jump` (the same wave while everyone jumps to a beat) | red, slower: ~50 s (39–84 s over 20 crowds) |
| `sway` (whole crowd sways to music at 0.5 Hz, 100 ms/person lag gradient + 50–200 ms each) | calm: periodic, lag ambiguous |
| `sway-slow` (0.2 Hz ballad sway, ±19 cm) | calm: small, and the few edges don't chain |
| `mexican` (stand up + arms up, 250 ms/person, every 8 s) | calm: vertical veto (red in 8/20 crowds without it) |
| `walkpast` (one person squeezes along the line, one nudge per phone) | brief yellow, decays to calm; a single travelling jolt is a shove |
| `procession` (people walk past alongside, lightly brushing about half the phones) | calm (yellow allowed; 4/20 crowds went yellow without chains) |
| `march` (the line walks off together, near-identical cadence) | calm: periodic |
| `pocket` (phones pocketed, jostled, dropped, picked up; some slower than the handling threshold) | calm |
| `bump` (random neighbour pairs bump, 30–300 ms apart) | calm, no wave edges: isolated pairs don't chain |
| `jump-stagger` (jumping to a 2 Hz beat, 0–300 ms reaction delays) | calm: periodic and vertical |

Also tested: ±25 ms clock-sync error, every other phone held upside down, and the false-positive scenarios over 8 different random crowds each. `PULSE_SWEEP=1 go test ./server/internal/detect -run Sweep -v` prints outcomes over 20 crowds per scenario, and `PULSE_CFG='{"minChain":0}'` overrides config fields for comparisons.

## Layout

```
server/cmd/pulse      the server (flags: -rows -cols -zone-cols -config -addr …)
server/cmd/sim        fake phones: -n 8 -scenario wave [-out file.jsonl for offline recordings]
server/cmd/dashtail   dashboard snapshots in a terminal
server/internal/      hub, clocksync, detect, store, brief, voice, sign, protocol, app, sim
web/phone             phone page (Vite + TS)       web/dashboard   dashboard (Vite + TS, SVG)
web/shared            protocol.ts — mirror of server/internal/protocol
arduino/sign          Uno R4 WiFi sign sketch
recordings/           labelled runs for tuning, tests and replay
```

Web dev with hot reload: run `./bin/pulse`, then `cd web && npm run dev:dash` (or `dev:phone`); Vite proxies the API and sockets to :8080.

## Arduino sign

`arduino/sign/sign.ino` for the Uno R4 WiFi: copy `arduino_secrets.h.example` to `arduino_secrets.h`, add Wi-Fi, flash, read the IP off the serial monitor, set `SIGN_URL=http://<ip>`. It serves `GET /level?v=calm|yellow|red&zone=B`: calm = heartbeat, yellow = `!`, red = flashing arrow + STOP (and pin 7 high for a buzzer/LED).

## Privacy

Random session ID + tapped grid spot + motion numbers. No names, contacts, location, audio or photos.

## Honest limits

- A web page only streams while open; a real event would build this into its official app.
- Phone-to-phone relay when the cell network jams needs a native app.
- Locating phones in a real crowd (GPS, ticket section) is the hard real-world problem; the demo uses tap-your-spot.
- A handful of testers proves the method, not the thresholds for 50,000 people.
