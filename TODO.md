# Pulse — to do (StormHacks 2026)

Ordered by value per hour. Judging: technical complexity · design · pitch · originality; 3-minute table demo, something working by 0:90.

## In progress
- [ ] **Free-moving phones** (branch `free-positions`): GPS → venue metres on the server (AirTag-style: the server works out every distance), manual "tap your spot" fallback, nearest-neighbour push detection in any direction, crowd clusters with density alerts (forming / dispersing), staff-drawn areas become real server zones, ant-like simulator with a `gather` scenario.

## Next
1. [ ] **"Move this way" on the attendee's phone** (~2–3 h). When a cluster gets too dense or a push travels through it, the server works out the least crowded direction for each affected phone, and its screen turns red with an arrow. Judging moment: the judge scans the QR, their phone becomes a dot, we push the line, their phone tells them where to go.
2. [ ] **"Why did it fire?" panel + proof numbers** (~2 h). Click a red link: both phones' motion traces overlaid, the cross-correlation curve and its peak lag. Evaluation card: every simulated scenario × 20 random crowds → false alarms and time to detect, including what still fails.
3. [ ] **Sign works anywhere: USB or wireless, no setup at the venue** (~1.5 h).
   - **USB:** plugged into the laptop, the server writes `red B` / `calm` down the serial cable (`SIGN_URL=serial:auto`).
   - **Wireless on a power bank:** the sketch remembers two Wi-Fi networks (home + phone hotspot) and joins whichever is there. It answers `GET /pulse` so the server can find it on the local network by itself (`SIGN_URL=auto`). Discovery works when the server runs natively (the Mac); inside WSL, keep the explicit IP.
   - Power bank: test 10–15 min first; some switch off when the draw is this low.
4. [ ] **Google Home Mini speaks the alert** (~1 h). Cast the ElevenLabs briefing to the Home Mini on the same Wi-Fi (no jailbreak). Sign + speaker on the table → Surge Choice, Best Hardware, MLH ElevenLabs.

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
- [ ] Google Home Mini (if used) on the same network as the Mac.

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
