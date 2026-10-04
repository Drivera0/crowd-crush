# Pulse: prize tracks and evidence

The 2026 track list wasn't published before the event; the names below follow StormHacks 2025. **Check the Devpost prize list and tick only the tracks that exist.** `[TRACKS: confirm the 2026 list]`. Judging criteria: technical complexity, design, pitch, originality.

Stacking won every big 2025 prize (MapD: Finalist + Best Design + UN SDG). Pulse is eligible for about ten tracks at once; the strongest bets are in bold.

Every number below has its condition next to it. Most evidence is from the simulator or synthetic phones; real-phone checks so far are one Android. Say so on Devpost.

## Summary

| Track | Fit | Strongest evidence | Still missing |
|---|---|---|---|
| **Finalist** | strong | judge's own phone turns red at the table; deterministic core with an evidence panel; evaluation that publishes its failures | verified opening number; one recorded real push |
| **Best Hardware** | strong | UNO R4 sign + two ESP32 zone lights over USB, one-click table setup, preflight that flashes and reads back every board | power-bank run if not on USB; real Bluetooth test of the beacons |
| **Surge Choice** | strong | judges shove each other and the sign on the table says STOP | sign visible from the aisle |
| **Most Likely to Become a Startup** | strong | named buyer, nothing to install, a console with setup, incidents, drills, escalation | `[PRICE]`, `[BUYER]` |
| MLH Best Use of Gemini | good | structured briefings + floor-plan vision with a response schema | floor-plan read on camera |
| MLH Best Use of ElevenLabs | good | every red alert spoken; escalation re-speaks | ElevenLabs voice (not browser fallback) audible at the table |
| MLH Best Use of .Tech | only if done | QR points at the `.tech` domain | pulsecrowd.tech claimed; named tunnel `[live? y/n]` |
| Best Design | medium | two surfaces: console + one-arrow phone screen; table layer for small groups | clean screenshots |
| Best Solo | good | one person: server, detector, estimator, simulator, two web apps, Android app, three boards | confirm the track exists |
| Social Good / UN SDG | good | crowd safety; SDG 11, SDG 3 | `[SDG: name the target on Devpost]` |
| Software Systems | good | clock sync, 1000-phone load test, WebRTC relay mesh, position estimator | — |
| IEEE SFU | possible | sensors + embedded boards | — |

## Evidence per track

### Finalist
- **Working by 1:00, on the judge's own phone.** They scan one QR (no app), get a name and a place in the row, jump together (stays calm), pass a shove down the row (red in a median ~8 s, synthetic phones calibrated on a real Android), and their own screen turns red with an arrow while the sign says STOP.
- **Then a crush they can't make at a table:** "Surge around the real phones" puts their real phones inside 220 simulated people. Every real phone went red with an arrow after 9.5–12 s in tests.
- **Deterministic core, LLM on the side:** cross-correlation between neighbours, density from clustering, area rules. "Why did it fire?" shows both traces, the correlation curve and every check.
- **An evaluation that says what breaks** (EVAL.md, LOCATE.md): 0 of 600 look-alike runs red with ideal, realistic and harsh phones; with realistic GPS 80 of 80 pushes still caught, 0 of 80 packing events red; the estimator brings surges back (58 of 60) but about 5 s late.
- Missing: verified opening statistic (PITCH.md), a labelled real-phone push recording.

### Best Hardware
- Arduino UNO R4 WiFi sign: two-dot "in touch" heartbeat / `!` / flashing arrow + STOP; forced red on escalation.
- Two ESP32 zone lights: their zone's colour; a blue LED that counts blinks (alone / hears another board / phone linked); Bluetooth device counts (counts only, no addresses); board-to-board distance.
- All three over USB with no Wi-Fi: `serial:auto` asks each port which board it is (the two ESP32s share a USB ID), reconnects on unplug. `scripts/boards.sh flash|status|wifi`, firmware build ids, **Set up table demo**, and a preflight that flashes every board red and checks each reports it back.
- Optional Bluetooth beacon positioning and an Android app that advertises to the boards (unit- and compile-tested; not yet on a real radio: say so).
- Missing: `[HW: anything that failed in the venue rehearsal]`.

### Surge Choice
- The object on the table that reacts to the judges: they shove each other, the sign says STOP and a zone light strobes.
- Missing: a printed QR so anyone walking past can join.

### Most Likely to Become a Startup
- **Buyer:** venue safety teams, promoters, ticketing platforms. `[BUYER: one verified fact]`
- **Price:** `[PRICE: per event or per attendee]`
- **Why they'd pay:** no hardware to install; attendees opt in through the ticket app; one screen for the safety team; a direction on each attendee's phone.
- **Product, not prototype:** setup checklist, venue templates, floor-plan import, watch areas with rules (density, capacity, custom message, which outputs), incidents with acknowledge / resolve / notes, escalation with an on/off switch, alert drills that report what each output did, saved runs.
- **Privacy by design:** random session ID, venue metres only, raw GPS never stored or shown, no names; the phone's Leave screen shows what the server holds.
- **Honest roadmap:** recordings from real events, then an app inside a ticketing platform (the Android shell shows what that adds: screen-off streaming, Bluetooth).

### MLH Best Use of Gemini
- Structured output for every briefing: JSON `{headline, action}`; staff messages enforced word for word; early-warning briefings worded as a projection.
- Vision with a response schema: an uploaded floor plan becomes venue size, stage, exits and walls in metres, with notes on how it judged scale and a confidence. Staff review before applying; the simulator then uses that geometry.
- Fails soft: template briefing, so Gemini is never on the critical path.
- Missing: show **✦ Find stage and exits** live or in the video.

### MLH Best Use of ElevenLabs
- Every red alert's briefing is spoken. Escalation (when on) re-speaks "Still unacknowledged. …". Drills say "This is a drill."
- Per-area voice switch; browser speech as fallback.
- Missing: confirm with `./bin/pulse -check` that the ElevenLabs voice, not the fallback, plays at the table.

### MLH Best Use of .Tech
- The join QR points at `https://<name>.tech` through a named Cloudflare tunnel, so it doesn't change on restart; the dashboard tests the link itself.
- Domain: `pulsecrowd.tech` (GitHub Student Pack). Confirm the named tunnel is live before claiming this track.

### Best Design
- Console: one status sentence, alert cards with their evidence, a map whose vocabulary is people ("packed in", "moving as one", never "cluster").
- Phone: one colour, one arrow, one sentence; "#3 in the row"; drill marking; plain fixes for every browser that blocks motion.
- Table layer: small groups drawn as a row with push arrows and a "moving as one" band.
- Missing: `[SCREENSHOTS: Devpost images]`.

### Software Systems
- One Go binary: NTP-style clock sync per phone, 10 Hz streams, a 250 ms detector step, incidents with stable IDs.
- Load test: 1000 phones, 10,000 messages/s, none dropped, dashboard at 10 Hz (loadtest.md; generator on the same machine).
- Position estimator: Kalman filter per phone plus a cooperative step from shared motion; 11.8 ms per step for 1000 phones on one core (LOCATE.md).
- Phone-to-phone WebRTC mesh: phones whose server connection is cut keep reporting and get warnings through neighbours (up to 3 hops).
- Evaluation harness: `make eval` runs every scenario × 20 seeds under each phone condition, from ideal to harsh.

### Others that fit
- **Best Solo:** one person built all of the above.
- **Social Good / UN SDG:** SDG 11 (safe, inclusive public spaces), SDG 3 (health). `[SDG]`
- **IEEE SFU:** embedded boards and sensor processing.
- **Tiger Data** (if a 2026 sponsor): readings and alerts in a hypertable, replay from it. `[TIGER: sponsor this year? y/n]`

## Devpost checklist
- [ ] Tick every eligible track.
- [ ] Video ≤ 3:00 (DEMO.md shot list), uploaded and linked.
- [ ] Repo link; README "Honest limits" section stays in.
- [ ] One paragraph per sponsor track saying exactly how it's used.
- [ ] A "What's simulated" paragraph: simulator and synthetic phones; real-phone checks so far are one Android.
- [ ] No unverified number anywhere on Devpost.
