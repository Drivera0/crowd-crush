# Pulse — to do (StormHacks 2026)

Ordered by value per hour. Judging: technical complexity · design · pitch · originality; 3-minute table demo, something working by 0:90.

## Done this afternoon
- [x] **Free-moving phones** (branch `free-positions`): GPS → venue metres on the server (AirTag-style: the server works out every distance), manual "tap your spot" fallback, nearest-neighbour push detection in any direction, crowd clusters with density alerts (forming / dispersing), staff-drawn areas become real server zones, ant-like simulator with a `gather` scenario.

## Next
1. [ ] **"Move this way" on the attendee's phone** (~2–3 h). When a cluster gets too dense or a push travels through it, the server works out the least crowded direction for each affected phone, and its screen turns red with an arrow. Judging moment: the judge scans the QR, their phone becomes a dot, we push the line, their phone tells them where to go.
2. [ ] **"Why did it fire?" panel + proof numbers** (~2 h). Click a red link: both phones' motion traces overlaid, the cross-correlation curve and its peak lag. Evaluation card: every simulated scenario × 20 random crowds → false alarms and time to detect, including what still fails.
3. [ ] **Sign works anywhere: USB or wireless, no setup at the venue** (~1.5 h).
   - **USB:** plugged into the laptop, the server writes `red B` / `calm` down the serial cable (`SIGN_URL=serial:auto`).
   - **Wireless on a power bank:** the sketch remembers two Wi-Fi networks (home + phone hotspot) and joins whichever is there. It answers `GET /pulse` so the server can find it on the local network by itself (`SIGN_URL=auto`). Discovery works when the server runs natively (the Mac); inside WSL, keep the explicit IP.
   - Power bank: test 10–15 min first; some switch off when the draw is this low.

## Requested next (business-facing product)
- [x] **Setup flow / main menu** (~2 h): first-run wizard and a home screen. 1) Event name and venue, 2) floor plan, 3) watch areas and alert rules, 4) hardware and lights, 5) share the join QR → Go live.
- [x] **ElevenLabs-style redesign** (~2–3 h): clean monochrome, sidebar navigation (Live · Venue · Areas & alerts · Hardware · Simulation · Recordings · Settings), calmer typography, motion on state changes.
- [x] **Venue templates + floor plan import** (~1.5 h): preset sizes (club, theatre floor, arena floor, festival field) that bound where phones can be; upload a floor-plan image as the map background.
- [x] **Gemini reads the floor plan** (~2 h): from an uploaded image, extract the outline, stage, exits and scale as editable geometry (MLH Best Use of Gemini).
- [x] **Custom alert rules per area** (~2 h): e.g. density above X/m² for Y s, any push, more than N phones; who/what gets notified (sign, light, voice, message text).
- [x] **Better AI briefing** (~1.5 h): structured card (what / where / what to do / confidence), acknowledge and resolve, escalation if unacknowledged, briefing history.
- [x] **Boards find each other** (~2–3 h): ESP32s and the sign advertise Bluetooth beacons; each reports the signal strength of the others → estimated distances, board-to-board links on the map, staff drag boards onto their real spots.
- [ ] **Node animation polish** (~1 h).
- [x] **Realistic crowd simulation**: Social Force Model people, steerable from the dashboard (surge, gather, shove, exits), with ground-truth lead time.

## Cheap wins (< 1 h each)
- [ ] **Venue floor plan as the map background**: upload an image, or an iPhone 15 Pro Max LiDAR room scan exported as an image.
- [ ] **`.tech` domain + named Cloudflare tunnel** (MLH Best Use of .Tech; stops the join QR changing). DNS is slow: start early. Steps in `docs/SETUP.md` §3.
- [ ] **Load test**: simulate 500–1000 phones; show "one Go server handles X phones / Y messages per second" on the dashboard.
- [ ] **Startup pitch**: buyers are venue safety managers and promoters. "No hardware to install. Attendees opt in through the ticket app; your team watches one screen." Privacy by design: venue-relative positions only, raw GPS never stored, no names.

## Pitch prep
- [ ] Opening number (verify sources before the slide): Itaewon 2022, 159 deaths; Astroworld 2021, 10 deaths; ~5 people/m² is dangerous.
- [ ] Origin story: a real first-person moment, if there is one.
- [ ] Say the limits unprompted: a web page streams only while open; phone-to-phone ranging (Bluetooth/UWB) needs a native app; GPS is coarse indoors, hence tap-your-spot.
- [ ] Record a labelled real-phone run (backup for Replay) and the ≤ 3 min demo video before the freeze.

## Pre-demo checklist (do it at home first, then again at the venue)
The demo runs on the **MacBook**, on a different network from home. Nothing below is automatic.

**Mac set up (once, at home)**
- [ ] Go 1.25+, Node 20+, cloudflared installed (`brew install go node cloudflared`).
- [ ] Repo cloned, on the right branch, `make build` succeeds.
- [ ] `.env` copied to the Mac by hand (AirDrop / USB). Never through git or chat. `./bin/pulse -check` is all green.
- [ ] Sign: hotspot name/password in `arduino/sign/arduino_secrets.h` (second network), reflashed once.
- [ ] Sign test both ways: plugged into the Mac (USB), then on the power bank over the hotspot; test alert flashes it each time.

**Network**
- [ ] Plan for the demo network: the phone hotspot (iPhone: Maximize Compatibility on) for the Mac and any Wi-Fi devices; attendees' phones can use any network or mobile data.
- [ ] Tunnel running and the join QR opens on a phone that's on **mobile data** (proves it works off the laptop's network).
- [ ] If using the quick tunnel: the address changes on every restart, so re-check the QR. The `.tech` domain fixes this.
- [ ] Voice plays through the Mac's speakers: volume up, dashboard 🔇 clicked once (browsers block audio until a click).

**Phones: test on real devices (at home, over the tunnel)**
| Phone / browser | Join + motion prompt | GPS dot lands right | Tap-your-spot fallback | Red screen on alert | Screen stays on |
|---|---|---|---|---|---|
| iPhone · Safari | [ ] | [ ] | [ ] | [ ] | [ ] |
| iPhone · Chrome (uses Safari's engine) | [ ] | [ ] | [ ] | [ ] | [ ] |
| Android · Chrome | [ ] | [ ] | [ ] | [ ] | [ ] |
| Android · Samsung Internet | [ ] | [ ] | [ ] | [ ] | [ ] |
- Open the QR link with the **camera app** (iPhone → Safari; Android → default browser). In-app browsers (Instagram, Messenger) may block motion.
- Turn **Precise Location** on (iPhone: Settings → Privacy → Location Services → Safari Websites; Android: allow "Precise" when asked).
- Low Power Mode (iPhone) / Battery Saver (Android) can pause sensors or let the screen lock: turn them off for the demo.

**At the venue, before judges arrive**
- [ ] Re-anchor GPS: dashboard → Venue → set the room size → **📍 Centre map on this laptop**. The anchor from home puts every phone in the wrong place.
- [ ] Draw the watch areas for this room (they're saved on the server).
- [ ] `./bin/pulse -check`: Gemini / ElevenLabs / Tiger / Sign. Anything red falls back, but know which.
- [ ] Join with two of your own phones; dots appear in the right place; push → red link → sign flashes → voice plays.
- [ ] Sound on in the dashboard (click 🔇 once; browsers block audio until a click).
- [ ] Replay a good recording once (the backup if live phones misbehave).
- [ ] Laptop on power, sleep disabled, screen brightness up.

## Done today
- [x] Venue console dashboard: live mesh map, watch areas, attendee drawer with telemetry, light/dark themes
- [x] Detector false-positive guards (sway, Mexican wave, walk-past, pockets, bumps, staggered jumping)
- [x] Gemini, ElevenLabs and Tiger Data connected; Gemini thinking-retry fix
- [x] Arduino sign flashed and wired up (with a retry for dropped connections)

## Findings to act on
- Simulation: Pulse catches crowding early (+17–19 s when the crowd builds at the stage) but is ~2 s late on a sudden surge. Idea: early warning from the density trend (cluster forming and densifying fast → yellow before it crosses the limit).
- Simulation: the wave (push) detector never fires on simulated crowds (pushes cross packed neighbours faster than its 120 ms floor; loose crowds damp them). Needs real recorded pushes to tell whether it's the model or the detector.
- Board distances: calibrate the Bluetooth 1 m reference with zone lights A and B 1 m apart.
