# Pulse: prize tracks and evidence

The 2026 track list wasn't published before the event; the names below follow StormHacks 2025. **Check the Devpost prize list and tick only the tracks that exist.** Judging criteria: technical complexity, design, pitch, originality.

Stacking won every big 2025 prize (MapD: Finalist + Best Design + UN SDG). Pulse is built to be eligible for about ten tracks at once; the strongest bets are marked.

## Summary

| Track | Fit | Strongest evidence | Still missing |
|---|---|---|---|
| **Finalist** | strong | only crowd-physics project in the room; deterministic core with evidence panel; honest eval | verified opening number; eval card numbers |
| **Best Hardware** | strong | UNO R4 sign + 2 ESP32 zone lights, Bluetooth counts, boards hear each other | sign reliability test on power bank |
| **Surge Choice** | strong | physical sign flashing STOP when the judge's own phone joins | sign visible and loud from the aisle |
| **Most Likely to Become a Startup** | strong | named buyer, no hardware to install, setup checklist console | one-sentence price and buyer number |
| MLH Best Use of Gemini | good | two jobs: structured briefings + floor-plan vision | live floor-plan read in the video |
| MLH Best Use of ElevenLabs | good | spoken briefings + escalation re-speak | voice audible at the table |
| MLH Best Use of .Tech | good if done | QR points at the `.tech` domain | domain + named tunnel (TODO) |
| Best Design | medium | two surfaces: monochrome console + phone arrow screen | node animation polish; screenshot pass |
| Best Solo | good | solo build | confirm the track exists |
| Social Good / UN SDG | good | crowd safety; SDG 11 (safe public spaces), SDG 3 (health) | name the SDG target on Devpost |
| IEEE SFU | possible | sensors + embedded boards | — |
| Software Systems | possible | Go server, clock sync, 10 Hz streams, load test | load-test numbers |

## Evidence per track

### Finalist (Microsoft + Hootsuite tours)
- **Only one of its kind.** Every 2025 finalist was; nobody else will bring crowd physics from phones. It is not an "AI watches you" project, which won tracks but never finals in 2025.
- **Working by 0:60**: the judge's own phone joins at 0:10 and turns red with an arrow at about 1:00.
- **Deterministic core, LLM on the side**: cross-correlation, DBSCAN density, rules. "Math detects, AI explains."
- **Honesty about limits**: ~2 s late on a sudden surge; push detector doesn't fire in the simulator; GPS too coarse indoors; participation is a guess.
- **Technical depth on demand**: "Why did it fire?" panel, Weidmann-calibrated Social Force simulator with ground truth, evaluation card.
- Missing: verified opening statistic (PITCH.md), `[EVAL: false alarms]` and `[EVAL: missed]` on the eval card, a recorded real-phone push.

### Best Hardware
- Arduino UNO R4 WiFi sign: heartbeat / `!` / flashing arrow + STOP, driven on every level change and forced red on escalation.
- Two ESP32 zone lights: show their zone's level, count nearby Bluetooth devices (counts only, no addresses), advertise Pulse beacons and estimate distance to each other; the map shows estimated vs placed distance.
- 2025 winner pattern: "a working sensor + a working screen, not a robot. Reliability beat ambition."
- Missing: 10–15 min power-bank test; calibrate the 1 m Bluetooth reference with lights A and B 1 m apart (TODO findings); USB serial sign (TODO item 3) if time.

### Surge Choice (Microsoft tour; "most creative and inspiring")
- 2025 winner was "the physical object that does something on the table". The sign flashing STOP the moment the judge's phone tips the area over capacity is that object.
- Missing: put the sign where passing organizers can see it; a printed QR so anyone walking by can trigger it.

### Most Likely to Become a Startup (Vercel Pro + Inworld credits)
- **Buyer**: venue safety teams, promoters, ticketing platforms.
- **Why they'd pay**: no hardware to install; attendees opt in through the ticket app; one screen for the safety team.
- **Product, not prototype**: home screen with an 8-step event setup checklist, venue templates, floor-plan import, watch areas with rules and custom messages, incidents with acknowledge / resolve / escalation, drills (test alert).
- **Privacy by design**: venue metres only, raw GPS never stored, no names.
- Missing: one number about the buyer (e.g. how many large events a promoter runs per year) `[VERIFY]`; a one-line price model; one line on competitors (QA.md #18).

### MLH Best Use of Gemini
- Structured output for every briefing: JSON `{headline, action}`, with staff messages enforced verbatim.
- Vision with structured output: an uploaded floor plan becomes venue size, stage, exits and walls in metres, with notes on how it judged scale and a confidence; staff review before applying. The simulator then uses that geometry.
- "Ask" box: questions to Gemini about the event.
- Fails soft: template briefing, so Gemini is never on the critical path (judges ask this).
- Missing: show the floor-plan read live or in the video (shot 9 in DEMO.md).

### MLH Best Use of ElevenLabs
- Every red alert's briefing is spoken; unacknowledged alerts are re-spoken after 60 s as "Still unacknowledged. …".
- Per-area `notify.voice` switch; browser speech as fallback.
- Missing: confirm the ElevenLabs voice (not the browser fallback) is what plays at the table; `-check` green.

### MLH Best Use of .Tech
- The join QR points at `https://<name>.tech` through a named Cloudflare tunnel, so the QR doesn't change on restart.
- Missing: everything. Claim the domain, move nameservers to Cloudflare (slow), named tunnel, `PUBLIC_URL` (SETUP.md §3). If it isn't done, drop this track.

### Best Design
- Two surfaces, each with one job: the monochrome console (sidebar: Home, Live, Venue, Areas & alerts, Hardware, Simulation, Recordings, Settings) and the attendee screen (one colour, one arrow, one sentence).
- Readable from across a table; light and dark themes; motion on state changes.
- Missing: node animation polish (TODO); check the phone screen on a small Android; clean screenshots for Devpost.

### Others that fit
- **Best Solo**: one person built the server, detector, simulator, two web apps and three boards.
- **Social Good / UN SDG**: SDG 11 (safe, inclusive public spaces) and SDG 3 (health). Missing: name the target on Devpost.
- **IEEE SFU**: embedded boards and sensor processing.
- **Software Systems**: one Go binary, NTP-style clock sync per phone, 10 Hz streams, incidents with stable IDs. Missing: `[LOADTEST: phones / msg/s]`.
- **Tiger Data** (if it's a 2026 sponsor): readings and alerts in a hypertable, replay from it.
- **Huawei spec challenge**: not this project. If one is announced, enter it separately.

## Devpost checklist
- [ ] Tick every eligible track on the submission form.
- [ ] Video ≤ 3:00 (DEMO.md shot list), uploaded and linked.
- [ ] Repo link; README "Honest limits" section stays in.
- [ ] One paragraph per sponsor track saying exactly how the sponsor is used.
- [ ] No unverified number anywhere on Devpost.
