# Pulse — to do (StormHacks 2026)

Ordered by value per hour. Judging: technical complexity · design · pitch · originality; 3-minute table demo, something working by 0:90.

## In progress
- [ ] **Free-moving phones** (branch `free-positions`): GPS → venue metres on the server (AirTag-style: the server works out every distance), manual "tap your spot" fallback, nearest-neighbour push detection in any direction, crowd clusters with density alerts (forming / dispersing), staff-drawn areas become real server zones, ant-like simulator with a `gather` scenario.

## Next
1. [ ] **"Move this way" on the attendee's phone** (~2–3 h). When a cluster gets too dense or a push travels through it, the server works out the least crowded direction for each affected phone, and its screen turns red with an arrow. Judging moment: the judge scans the QR, their phone becomes a dot, we push the line, their phone tells them where to go.
2. [ ] **"Why did it fire?" panel + proof numbers** (~2 h). Click a red link: both phones' motion traces overlaid, the cross-correlation curve and its peak lag. Evaluation card: every simulated scenario × 20 random crowds → false alarms and time to detect, including what still fails.
3. [ ] **Google Home Mini speaks the alert** (~1 h). Cast the ElevenLabs briefing to the Home Mini on the same Wi-Fi (no jailbreak). Sign + speaker on the table → Surge Choice, Best Hardware, MLH ElevenLabs.

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

## Done today
- [x] Venue console dashboard: live mesh map, watch areas, attendee drawer with telemetry, light/dark themes
- [x] Detector false-positive guards (sway, Mexican wave, walk-past, pockets, bumps, staggered jumping)
- [x] Gemini, ElevenLabs and Tiger Data connected; Gemini thinking-retry fix
- [x] Arduino sign flashed and wired up (with a retry for dropped connections)
