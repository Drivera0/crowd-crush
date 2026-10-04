# Pulse: presenter's guide

A plain-language walkthrough of what Pulse is, how it works, what it's built with, and what to say about it. The detailed sources are the README, `docs/REFERENCE.md`, `docs/PITCH.md`, `docs/QA.md` and `docs/EVAL.md`. This file is the version to read the night before.

---

## 1. The project in one breath

**Pulse is early warning for crowd crushes, using the phones already in the crowd.**

People scan a QR code and open a web page (no app install). Their phone streams its motion to a server. The server watches for two danger signs:

1. **A push travelling through the crowd** — person A moves, then a fraction of a second later person B, then C, in a chain.
2. **People packing too tightly** — too many phones in too small a space.

When it sees either, a steward's dashboard turns yellow then red, an AI-written one-sentence briefing is spoken aloud, a physical sign on the table flashes STOP, and **each attendee's phone in the danger zone turns red with an arrow telling them which way to move**.

The rule that runs through everything: **math detects, AI explains.** The alarm is decided by deterministic signal processing. Gemini only writes the sentence afterwards.

---

## 2. The problem

- Itaewon, Seoul, 29 October 2022: **159 people died** in a crowd crush, most of them in one narrow alley. (Checked. Say "159 people died", not "159 crushed".)
- Astroworld, Houston, 2021: 10 people died in a crowd surge at the stage. (Checked: the medical examiner ruled all 10 compression asphyxia.)
- Nobody inside a crush can see it building, and nobody outside can either. Cameras count heads from above; they can't tell a person in the middle which way to go.

---

## 3. How it works, end to end

```
 attendee phones (web page)  ──WebSocket──►  Go server  ──WebSocket──►  steward dashboard
                                              │
                                              ├─ clock sync with every phone
                                              ├─ push detector (every 250 ms)
                                              ├─ density / cluster tracker
                                              ├─ saves readings → Tiger Data (TimescaleDB)
                                              ├─ on alert → Gemini writes briefing → ElevenLabs speaks it
                                              ├─ Arduino sign + ESP32 zone lights
                                              └─ "move this way" arrow back to each phone
```

### Step by step

1. **Join.** Attendee scans the QR (points at `pulsecrowd.tech` through a Cloudflare tunnel). Phones only expose motion sensors over HTTPS, which is why the domain and tunnel matter.
2. **Place.** Each phone becomes a dot on the venue map, either dragged to where the person stands, placed by GPS, or (at the table demo) lined up in join order.
3. **Stream.** Each phone sends a motion summary 10 times a second: acceleration, rotation, gravity.
4. **Sync clocks.** The server pings every phone (NTP-style, best of 8 round trips, every 30 s) so it knows each phone's clock offset. This matters because the push detector measures delays of a few hundred milliseconds between phones.
5. **Detect** (every 250 ms) — see section 4.
6. **Alert.** Zone goes yellow → red. An incident card appears on the dashboard. Gemini writes a headline + one action. ElevenLabs speaks it. The sign flashes STOP. Phones in danger turn red with an arrow.
7. **Staff respond.** Acknowledge, resolve, add a note. If nobody acknowledges a red alert in 60 s it escalates and is spoken again.

---

## 4. The detection, explained simply

### A. Push detection (the "wave")

The key insight: **a dance and a crush look different in time.**

- **Jumping or dancing together** → everyone moves at the *same moment*. Lag between neighbours ≈ 0.
- **A push** → it *travels*. Neighbour B moves 120–1200 ms after neighbour A.

What the server does per phone:
1. **Ignore handling.** If the phone is spinning fast (someone checking it), ignore it until it's still. The dot goes blue.
2. **Level it.** Use gravity to work out which way is "horizontal", so it works however the phone is held.
3. **Filter.** Keep only slow horizontal sway (0.15–1.5 Hz). Crowd sway is slow.

Then for each pair of nearby phones:
4. **Cross-correlate.** Slide one phone's last 6 seconds of motion over the other's and find the time shift where they match best.
5. **Call it a wave hop** only if: strong match (|r| ≥ 0.6), a delay of 120–1200 ms, and **one clear best match** (music is rhythmic, so it matches at several delays equally — rejected).
6. **Vertical veto.** If the motion is mostly up-and-down and travels in sequence, it's a stadium Mexican wave, not a push.
7. **Chain of three.** A hop only counts if it's part of a run through **at least 3 phones going the same direction**. Two people bumping into each other doesn't count.

Each zone gets a score (how much of it carries a wave in one direction), smoothed over 8 s. Yellow above 0.3, red above 0.6, each held 2 s. A single shove can reach yellow but not red; red needs the push to keep coming.

**Line to say:** "Jumping together has zero lag between neighbours. A push has a lag that travels. Only the second one alarms."

### B. Density detection (packing)

1. Every 250 ms, group nearby phones into clusters (**DBSCAN**, 1.2 m radius, at least 3 phones).
2. For each cluster, estimate **people per m²** at its densest well-supported spot (phones within 1.5 m), divided by `participation` (the share of the crowd running Pulse — e.g. 0.33 if only a third have it open).
3. Yellow above **2 people/m²**, red above **4 people/m²**, each held 2 s.
4. **Early warning:** if the density is rising fast enough to hit 4/m² within 30 s, go yellow early with a projection ("reaches a dangerous 4 per m² in about 12 s").

### C. "Move this way" arrow

For each phone in danger the server computes a direction toward fewer people (downhill on a density map of all phones), sliding along walls rather than into them. For a push, the arrow points **sideways and slightly with the push, never against it** (standard crowd-safety advice). If an open exit is roughly that way, it names it.

### D. "Why did it fire?"

Click any red link on the dashboard: you see both phones' motion traces, the correlation curve, the peak delay, and every check with pass/fail in plain words. This is the answer to "why not machine learning?" — a safety officer can see exactly why.

---

## 5. Tech stack

| Layer | What | Why it's there |
|---|---|---|
| **Server** | **Go 1.25** | One binary, handles 1000 phones at 10 msgs/s each on one machine. Concurrency for many WebSockets. |
| Real-time transport | `coder/websocket` | `/ws/phone` for phones, `/ws/dash` for dashboards. |
| Detection | Hand-written Go: band-pass filters, cross-correlation, DBSCAN clustering, Kalman filter for positions | Deterministic and explainable. No ML. |
| **Phone page** | **TypeScript + Vite** | Plain web page. Reads `DeviceMotion`/`DeviceOrientation`, shows the red screen and arrow, holds a wake lock. |
| **Dashboard** | **TypeScript + Vite, SVG map** | Live venue map, alert cards, setup wizard, venue/areas/hardware/simulation/recordings pages. Uses the `motion` library for animation. |
| Shared types | `web/shared/protocol.ts` | Mirrors the Go protocol so both sides agree on message shapes. |
| **Database** | **Tiger Data (TimescaleDB)** via `pgx` | Every reading to a `readings` hypertable using Postgres `COPY` in batches; `alerts` and `runs` tables; a `zone_1s` continuous aggregate (per-zone per-second history). Falls back to local JSONL files. |
| **AI briefing + vision** | **Google Gemini** | Structured JSON `{headline, action}` for each alert; reads an uploaded floor-plan image into venue size, stage, exits, walls. 5 s timeout → template sentence. |
| **Voice** | **ElevenLabs** (Flash v2.5) | Speaks each briefing as MP3 on the dashboard. Falls back to the browser's voice. |
| **Public HTTPS** | **Cloudflare Tunnel** + **`.tech` domain** (`pulsecrowd.tech`) | Phones need HTTPS for motion sensors. Gives the laptop a public address with no port forwarding. |
| **Hardware** | **Arduino UNO R4 WiFi** (sign) + **two ESP32s** (zone lights) | Sign flashes heartbeat / `!` / arrow + STOP. Lights show their zone's colour. Driven over USB serial (`go.bug.st/serial`) or Wi-Fi. ESP32s also count nearby Bluetooth devices and hear each other. |
| **Android app** | **Kotlin**, WebView shell (~1,000 lines) | Wraps the same page; adds Bluetooth beacons and keeps streaming with the screen off via a foreground service. Demo only. |
| Phone mesh | **WebRTC** | Phones relay each other's data up to 3 hops if one loses its server connection. |
| **Crowd simulator** | Go, **Social Force Model** (Helbing & Molnár 1995; Helbing, Farkas & Vicsek 2000) | Simulated people as bodies with forces. A crush *emerges*. 60 % carry a simulated phone that goes through the same pipeline. Knows the true pressure, so it can measure Pulse's lead time. |
| QR | `skip2/go-qrcode` | Join QR on the dashboard. |

**Every external service fails soft.** No keys, no internet: Pulse still detects, records to files, speaks with the browser voice, and alerts. `./bin/pulse -check` shows which are connected.

---

## 6. Where things are in the repo

```
server/cmd/pulse       the main server
server/cmd/sim         fake phones for testing
server/cmd/eval        runs every scenario × 20 random crowds → docs/EVAL.md
server/cmd/loadtest    1000 fake phones against a live server
server/internal/
  detect/              push detection, zones, alert levels
  crowd/               DBSCAN clusters, density, trend, early warning
  crowdsim/            Social Force Model crowd simulator
  locate/              position estimator (Kalman filter, step counting)
  clocksync/           per-phone clock offset
  hub/                 WebSocket connections and broadcast
  brief/ voice/ sign/  Gemini, ElevenLabs, Arduino
  store/               Tiger Data / JSONL
  geo/                 GPS → venue metres
  app/                 wires it all together, HTTP API
web/phone              attendee page
web/dashboard          steward console
web/shared             protocol types shared by both
arduino/sign           UNO R4 sign firmware
arduino/zone-light     ESP32 zone light firmware
android/               Kotlin app shell
recordings/            labelled runs used for tests and replay
```

---

## 7. Numbers you can say (and only these)

Each comes with its condition. Say the condition.

| Say | Condition |
|---|---|
| A shove passed down a row of 3–5 goes red in about **8 s** | Table demo, a push every 3 s, synthetic phones calibrated on one real Android |
| Jumping, dancing, walking, handling: **0 of 10** went yellow | Same |
| Two phones: yellow in about 1.2 s, **never red** | Same; red needs a chain of 3 |
| **0 of 600** look-alike runs went red | Simulator, 20 random seeds, ideal and realistic phones |
| Surge around the real phones: every real phone red with an arrow after **9.5–12 s** | 220 simulated people around 1–5 real phones |
| With realistic GPS (~5 m off): **80 of 80** pushes still caught, **0 of 80** packing events red | Simulator |
| **1000 phones, 10,000 messages/s, none dropped**, dashboard at 10 Hz | Load test on one machine |
| Crowd building at the stage: red a median **~18 s before** the simulated danger | Crowd simulator, exact positions |
| Sudden surge into a loose crowd: about **2 s late** | Same; early warning pulls it to on-time on 3 of 5 seeds |

---

## 8. Don't say

- "AI detects crushes." → The math detects; Gemini words it.
- "Saves lives" / "prevents deaths." → "Early warning", "one more signal for the safety team."
- "Tested on real crowds." → "Simulator and synthetic phones; real-phone testing is one Android so far."
- "Validated." → The simulator matches known walking-speed data; it's a test bench, not proof.
- "Works anywhere." → Needs the page open, HTTPS, and positions to about a metre for packing.
- Any number not in section 7.

---

## 9. The 2-minute table demo (summary)

Full run sheet: `docs/PITCH.md` and `docs/DEMO.md`.

| Time | Say | Do |
|---|---|---|
| 0:00 | Itaewon opening line. "Nobody inside a crush can see it building." | Dashboard full screen. |
| 0:10 | "Scan this, please. No app." | Point at QR. |
| 0:20 | "Your phone has a number. Stand in that order." | Dots appear in a row. |
| 0:30 | "First, jump together." | Stays calm. |
| 0:40 | "A crush is different: a push that travels person to person. #1, gently shove #2 and pass it on, every few seconds." | Links light up; yellow ~4 s, red ~8 s. |
| 0:55 | (let the voice play) | Sign flashes STOP. |
| 1:00 | "**Now look at your phone.**" | Their screen is red with an arrow. This is the moment. |
| 1:08 | "Steward gets one sentence. The person in the crowd gets a direction: sideways, never against the push." | Acknowledge the alert. |
| 1:12 | "Math detects, AI explains. Here's why it fired." | Click a red link → evidence panel. |
| 1:25 | "Five people can't make a real crush. So let's put your phones inside one." | Simulation → **Surge around the real phones**. |
| 1:45 | Honest limits: the push was real, the crowd after it is simulated; GPS indoors is metres off. | |
| 1:52 | "Venue safety teams and ticketing platforms pay. Nothing to install." | Stop simulation. |

**Need at least 3 phones for red.** Dan's two phones + one judge is enough.

### 30-second version

"In October 2022, 159 people died in a crowd crush in Itaewon, Seoul. Pulse turns the phones already in a crowd into an early-warning network. Scan this. Jumping together stays calm. A shove passed down the line goes red in about eight seconds: the steward hears what to do, the sign says stop, and your phone points you out of the push. The math decides; Gemini only writes the sentence. Attendees opt in through the ticket app; there's nothing to install in the venue."

---

## 10. Likely questions, short answers

Full list (27 questions): `docs/QA.md`.

- **How do you tell dancing from a crush?** Dancing moves neighbours at the same moment; a push arrives with a delay that travels. Music is rhythmic so it matches at many delays; a push has one clear delay. 0 of 600 look-alike runs went red.
- **Why not machine learning?** There's no labelled dataset of real crushes from phones, and a model trained on our simulator would just learn the simulator. A deterministic detector can show its evidence.
- **GPS indoors is bad. How do you know who's next to whom?** With bad GPS, Pulse stops trusting the dot and lets shared motion decide who's a neighbour. Pushes still get caught (80/80 in sim). Packing doesn't — you can't see a 2 m knot through 5 m of error. Better positioning fixes it.
- **Most people won't have it open.** Density divides by a per-event `participation` estimate. It's the weakest number in the system, and I say so. Push detection depends on it less.
- **What does Gemini do?** Two jobs: writes the briefing as structured JSON, and reads floor plans into walls, stage and exits. It never decides danger. If it's down, a template writes the sentence.
- **Why not cameras?** Cameras count heads from above and can't talk to people in the crowd. Pulse would sit next to them; camera density would actually fix our weakest number.
- **What if it's wrong?** Every alert is a card with its evidence. Staff acknowledge or resolve. Nothing acts on the crowd automatically except an arrow.
- **Does it scale?** One Go server took 1000 phones at 10,000 messages/s with none dropped. Each phone is compared only with nearby neighbours, so work grows with the crowd, not its square.
- **Privacy?** Random session ID, position in venue metres, motion numbers. No names. GPS is converted to venue metres on arrival and never stored or shown.
- **If you don't know:** "I don't know; here's how I'd find out" — and name the test.

---

## 11. Honest limits (say them before you're asked)

- Everything except one Android phone has been measured in a simulator or on synthetic phones. Nobody has recorded a real push yet.
- A web page only streams while it's open and on screen. A real event would build this into the ticket app.
- Indoor GPS is too coarse to see packing; the demo places people by hand.
- Density is only as good as the `participation` estimate.
- In the crowd simulator, the push detector rarely fires (stiff simulated bodies pass a push too fast). Surges there are caught by density. Only real recordings settle which is right.

---

## 12. Still to fill in before judging

From `docs/PITCH.md`, `docs/QA.md` and `docs/TRACKS.md`:

- `[ORIGIN]` — a first-person moment (concert, festival exit, packed station), or cut it.
- `[PRICE]` — per event or per attendee, one number.
- `[BUYER]` — one verified fact about the buyer.
- `[REAL]` — what got tested on judges' phones / a real iPhone.
- `[TRACKS]` — confirm the 2026 StormHacks prize list.
- Is `pulsecrowd.tech` and the named tunnel live?
