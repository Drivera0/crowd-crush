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

Needs Go 1.25+ and Node 20+. `make test` runs the Go tests (detector scenarios, recordings, clock sync, sponsor clients).

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
| `SIGN_URL` | no sign |
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

Per pair of grid neighbours, cross-correlate the last 6 s at lags ±1.5 s. A **wave edge** needs |correlation| ≥ 0.6 at a lag of 120–1200 ms, and that peak must be *unambiguous*: periodic motion like walking has several equally good lags, so the best peak must beat any other by 0.2. Lag ≈ 0 means moving together (dancing) and is never a wave.

Per zone: score = net fraction of edges carrying a wave in one direction, smoothed (8 s). Yellow above 0.3 for 2 s, red above 0.6 for 2 s, clearing 0.1 lower (hysteresis).

Every threshold lives in one struct: `./bin/pulse -dump-config > detect.json`, edit, `./bin/pulse -config detect.json`.

| Simulator scenario | Detector outcome (tested) |
|---|---|
| `calm`, `walk`, `handle` | calm; handling nodes blue |
| `dance` | nodes swaying, no wave edges, calm |
| `shove` (one push) | yellow at most, decays back to calm |
| `wave` (growing push every 2.5 s, 250 ms/person) | red in ~25 s, direction left → right |

Also tested: ±25 ms clock-sync error, and every other phone held upside down.

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
