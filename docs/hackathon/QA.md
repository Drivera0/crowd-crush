# Pulse: the hardest questions

Short, honest answers. Every number names its condition and comes from [EVAL.md](../EVAL.md), [LOCATE.md](../LOCATE.md), [loadtest.md](../loadtest.md), [APP.md](../APP.md), the README's simulator section, or the table-demo measurement (`go test -run TestTableMeasure -v ./server/internal/detect`, quoted in [DEMO.md](DEMO.md)). Anything in `[brackets]` is a placeholder to fill before judging.

**The answer under every answer:** nearly everything here was measured in a simulator or on synthetic phones. Real-phone checks so far are one Android phone streaming through the server (its noise calibrates the table-demo phones), plus whatever the judges do tonight. Say that first if a judge asks "how do you know?"

## What's real and what's simulated

**1. What have you actually tested on real phones?**
One Android phone, streaming through the server on 3 October (its readings lying on the table and held in the hand set the noise of the simulated table phones). The phone page's behaviour on each browser was checked in headless Chrome emulation, not on the devices themselves (SETUP.md §6). The Android app's screen-off stream was measured on an Android 12 emulator, not the real phone (APP.md). The Bluetooth beacons are compile-checked and unit-tested, not exercised on a radio (BEACONS.md). Nobody has recorded a real push yet. `[REAL: what got tested on judges' phones today, if anything: phones, browsers, push went red y/n]`

**2. So what are your numbers worth?**
They show the method holds up against the cases we could think of, under stated conditions. They don't show it works in a real crowd. Three kinds of source: hand-written scripted signals (a push is a damped sine travelling at 2.4 m/s), a Social Force crowd simulator, and synthetic "messy" phones (GPS error, pockets, dropouts) built on top. The detector's thresholds were tuned on the same simulator, which flatters it. What would count as validation: labelled recordings of real crowds, replayed through the same pipeline with thresholds frozen beforehand. Every run Pulse sees can be recorded, labelled and replayed through the tests for exactly that.

**3. How did you check the simulator?**
Mean walking speed is within ±0.10 m/s of Weidmann's fundamental diagram from 0.5 to 5 people/m², except at 2.5/m² (−0.13), in a corridor. A 1 m door lets 120 people out at 1.6 persons/(m·s) (1.50–1.72 by seed), against ~1.6–1.9 measured by Kretz et al. and Seyfried et al. The furnished venues check door flows against SFPE's 1.32 persons/(m·s) and turnstiles against the Green Guide's 660 per hour. That makes the crowd plausible, not proven. Bodies are discs, and evacuation and panic were not validated.

## How it works

**4. How does the push detection work?**
Each phone sends a 100 ms motion summary. The server levels it with the phone's gravity vector (so it works however the phone is carried), keeps the horizontal motion and band-passes it to 0.15–1.5 Hz, because a crowd sway is slow. For each pair of neighbours it slides one trace over the other (±1.5 s over the last 6 s) and finds the shift where they match best. A wave hop needs a strong match (|r| ≥ 0.6) at a lag of 120–1200 ms, one clear peak, and mostly horizontal motion. It only counts as a push when it chains through at least three phones in a consistent direction. Clocks are synced NTP-style per phone, so a lag of a few hundred ms is real.

**5. Why doesn't dancing or jumping set it off?**
Jumping to a beat moves neighbours at the same moment: lag near zero, never a wave. Swaying to music is periodic, so several lags match equally well and the "one clear peak" test rejects it. A stadium Mexican wave is mostly vertical and gets vetoed. In the simulator, 0 of 600 look-alike runs went red (dance, sway, slow sway, Mexican wave, marching, walking, phones handled, pocketed, people bumping, walking past), with ideal phones, realistic phones and harsh ones (EVAL.md). At the table, jumping and dancing gave 0 of 10 yellows for every row of 2–5 phones (synthetic phones). One false red appeared in 600 runs, only in the 1 m GPS-error condition.

**6. Then what's the difference between a dense, swaying dance floor and a crush?**
Honestly, the hard case. A crush pins people: in the simulator, people above the injury-level pressure (1600 N/m) move about as little as people standing calmly (0.06–0.10 against 0.08 m/s²), so there's no motion signature of a quiet crush (EVAL.md finding 2). Pulse catches it by density, and catches pushes by how they travel. For a small group pressed together and moving as one, the table profile shows a yellow "moving as one", never red, because it can't tell pressure from friends rocking together irregularly. Combining "dense" and "swaying" into a red was rejected: a dense crowd swaying to a slow song would trip it.

**7. How does the density alert work?**
Every 250 ms phones are grouped (DBSCAN, 1.2 m), and each group gets an estimated people/m²: phones within 1.5 m of each member, at the densest well-supported spot, divided by the share of people running Pulse (`participation`). Yellow above 2/m², red above 4/m², each held 2 s. An early warning goes yellow sooner when the trend projects the group reaching 4/m² within 30 s.

**8. Why not machine learning?**
There is no labelled dataset of real crushes recorded from phones, and a model trained on our simulator would learn the simulator. A deterministic detector can be explained to a safety officer: every alert opens a panel with both phones' traces, the correlation curve and which checks passed. With real recordings, learning could tune thresholds later. It shouldn't make the call today.

**9. What does Gemini do? What if it's wrong or down?**
Two jobs. It writes each briefing as structured JSON (a headline and an action), and reads an uploaded floor plan into venue size, stage, exits and walls, which staff review before applying. It never decides whether a zone is dangerous: the detector raises the alert first. 5 s timeout, then a template sentence. A staff-written message for an area replaces its action word for word. ElevenLabs speaks the briefing; without it, the browser's voice does.

## Positions and GPS

**10. GPS indoors is terrible. How do you know who stands next to whom?**
You mostly don't, and Pulse stops pretending to. With realistic phone GPS (median 5 m off, drifting) the detector no longer trusts the dot: everyone within reach of both accuracy radii is a candidate neighbour, and shared motion decides. In the simulator that catches 80 of 80 pushes with realistic phones and 69 of 80 with harsh ones (10 m, most phones in pockets and bags), with 0 of 600 false alarms in both (EVAL.md, seeds 1–20). At the table we skip GPS entirely: phones line up in a row in join order.

**11. And packing, with 5 m of GPS error?**
Not caught red. A patch packed at 6/m², 3 m deep, blurred by 4 m of error reads about 2/m², and so does a comfortable crowd. With realistic GPS, 0 of 80 packing events go red; 59 of 80 raise a yellow, a median 12.7 s before the simulated danger, against 4 of 460 calm runs (EVAL.md). At 2.5 m error, 35 of 80 go red, late. At 1 m it's as good as exact positions. What fixes it: positions good to about a metre (tap your spot, a seat or section, UWB, Wi-Fi RTT, Bluetooth ranging in a native app) or counts that don't need positions (area capacity rules, turnstiles, the boards' Bluetooth device counts).

**12. What does the position estimator add?**
A Kalman filter per phone (position plus its GPS bias), steps from the accelerometer, the entry spot, and above all: phones jostled together are found by their shared motion and pulled together on the map. With realistic phones the median error goes from 4.5 m to 3.6 m, and surges go from 0 of 60 caught to 58 of 60, with no false red in 60 look-alike runs. But the red comes about 5 s *after* the simulated danger starts (2 of 59 before it). Indoors with no GPS at all, walking in from the QR code: about 5 m, 51 of 60 caught, about 6 s late (LOCATE.md, seeds 201–220). Not checked on a real phone.

**13. The arrow on my phone: is it right?**
With exact positions, 79 % of arrows are within 45° of the arrow computed from everyone's true position. At 5 m GPS error it's 38 %, barely better than a random arrow (25 %). So each arrow carries a confidence, and below 0.5 the phone should show words instead: with realistic GPS 24 % of arrows would be shown (EVAL.md). At the table the positions are exact. During a push the arrow points sideways, out of the push and slightly with it, never against it: that's standard crowd-safety advice.

## Small groups, phones and browsers

**14. Can two phones show anything?**
A chain needs three phones, so two can never make a red. With the table profile on (demo spot, five or fewer phones in a zone), a push from one to the other is yellow in a median 1.2 s and never red, with no red dots (10 of 10 runs, synthetic). Three to five phones: a push every 3 s goes red in a median 7.8–8.0 s; one push alone stays yellow.

**15. Doesn't that table profile just make the demo easier?**
It shortens smoothing and hold time in zones with five phones or fewer, and only while the demo spot is on. The per-pair tests and the three-phone chain are unchanged, which is why jumping and dancing still stay calm. Without it the same pushes go red in about 10 s instead of 8. A zone with a real crowd in it runs exactly as without the profile.

**16. What about battery and the screen going off?**
The web page only streams while it's open and on screen; it holds a wake lock so the screen stays on, and the screen is the real battery cost. Screen off, the page's motion stream stops (0 messages/s, measured with the page left alone). The Android app wraps the same page and moves the stream into native code when the screen goes off: 9.9 messages/s for 5 minutes, 3,012 readings, no gap (APP.md). Both were measured on an Android 12 emulator, not a real phone, and battery draw was not measured. A real event would run it inside the ticket app, and only at high-risk moments (doors, headliner, exits).

**17. Does it work on iPhone?**
The phone page does: Join, allow Motion & Orientation, streaming, the red screen and the arrow (checked in emulation; `[REAL: iPhone checked on a real device y/n]`). What an iPhone web page can't do: Web Bluetooth (so no beacon positioning), vibration, or running with the screen off. An iPhone app would need CoreBluetooth and a different advert format, because iOS won't let an app advertise manufacturer data (APP.md). Not built.

**18. What does the phone-to-phone mesh prove?**
That phones can carry each other's data. Phones open WebRTC links to a few nearby phones; when a phone's server connection is closed ("Jam half the phones"), it keeps reporting through its neighbours and still gets its warnings, up to 3 hops. What it doesn't prove: that it works when the cell network is actually jammed. Setting up the links needs the server, and phones on different mobile networks need the internet to reach each other (no TURN server). Real off-grid relay needs Bluetooth or Wi-Fi Direct in a native app. `[MESH: number of real phones the jam demo has run on]`

## Privacy, cameras, business

**19. What about privacy?**
A phone sends a random session ID, its position in venue metres and motion numbers. No names, contacts, audio, photos or location history. GPS is converted to venue metres the moment it arrives; latitude and longitude are never stored, logged or sent to the dashboard. On the mesh, phones see a hashed handle, not each other's IDs, and raw GPS never passes through another phone. The Bluetooth scan only reports devices named `PULSE-…`. A phone's Leave screen shows what the server holds about it and when it is dropped (30 s). Under GDPR a live session is still personal data, so a deployment needs a stated purpose, a retention limit on recordings and a consent screen in the ticket app.

**20. Why not cameras?**
Cameras count heads from above and estimate density; they need line of sight, light, installation and an operator, and they raise their own privacy questions. They can't tell a person in the crowd which way to go. Pulse measures how a push travels between neighbours and talks back to each person's phone. We'd want to sit next to cameras, not replace them: density from cameras would fix our weakest number. Others in the space: camera analytics (CCTV head counts), Wi-Fi and Bluetooth device counting, 3D people counters at entrances, and academic phone-based crowd sensing (ETH Zurich at the Lord Mayor's Show, Wirz et al. 2013). `[VERIFY: check any company you name before naming it]`

**21. Who pays, and how?**
Venue safety teams, promoters and ticketing platforms, `[PRICE: per event or per attendee, one number]`. The pitch is no hardware to install: attendees opt in through the ticket app, and the safety team watches one screen. The sign and zone lights are optional, for places where staff can't watch a screen. `[BUYER: one verified fact about the buyer, e.g. how many large events a promoter runs a year]`

**22. Most people won't have it open. Doesn't that break density?**
Density counts phones, so it's divided by a `participation` estimate set per event: with a third of the crowd online, 4 phones in a small area read as 12 people. It's the weakest number in the system: too high and real crushes look half as dense, too low and comfortable groups raise alarms. The push detector depends on it less: it needs a few neighbouring phones in a chain.

## Scale and engineering

**23. Does it scale to a stadium?**
One Go server took 1000 fake phones at 10 messages/s each: 10,000 messages/s, none dropped, dashboard snapshots steady at 10 Hz (worst gap 111 ms), status endpoint 0.5 ms at the median (loadtest.md). Caveats: the load generator ran on the same machine, and the server was showing a simulation at the time, so the dashboard numbers are the simulation's. Each phone is compared with a handful of neighbours, so the work grows with the crowd, not its square. The detector step for 1000 phones takes 27–90 ms and the estimator 11.8 ms on one core, inside a 250 ms tick (LOCATE.md). Beyond one server, the venue splits by zone.

**24. How early does it warn?**
In the crowd simulator with exact positions: when a crowd builds at the stage, red a median 18.4 s before the simulated danger (20 of 20 seeds), because the density passes 4/m² first. On a sudden surge into a loose crowd it's about 2 s late (median −2.1 s); the early warning moves the first warning to at or before the danger on 3 of 5 seeds (README). With realistic GPS and the estimator, about 5 s late (question 12).

**25. Does the push detector fire inside the crowd simulator?**
Almost never, and we say so. Simulated bodies are stiff discs, so a push crosses packed neighbours in tens of milliseconds, under the 120 ms per-hop floor, and in a loose crowd it dies within about 2 m. Either real people pass a push on more slowly than stiff discs, or the floor needs lowering for packed crowds. Only real recorded pushes settle it. In the simulator, surges are caught by density.

**26. What happens when it's wrong?**
A false alarm costs a steward a look: each alert is an incident card that staff acknowledge or resolve, with the evidence one click away. Unanswered red alerts can re-announce themselves (switchable). Nothing acts on the crowd automatically except an arrow. A miss is the worse failure, so Pulse is one more signal for an existing safety team, never a replacement for stewards and cameras.

**27. What does the hardware add?**
Staff and attendees can't all watch a screen. The Arduino UNO R4 sign flashes STOP for the worst zone; the two ESP32 zone lights show their zone's level. All three run over USB with no Wi-Fi. The ESP32s also count nearby Bluetooth devices (counts only, no addresses) and hear each other. Everything works without them.

## What each sponsor does

The rule across all of them: math detects, the services explain, store and reach people. Every one can be down or missing its key and Pulse still detects and alerts. `./bin/pulse -check` shows which are connected.

**Tiger Data (TimescaleDB): the memory.**
- Every phone's motion summary (10 a second per phone: acceleration, rotation, gravity, venue position, zone) is written to a `readings` hypertable in batches with Postgres `COPY`, so 1,000 phones don't mean 10,000 single inserts a second.
- Every alert goes to an `alerts` hypertable, and labelled recordings ("wave-push-end") to a `runs` table.
- A continuous aggregate, `zone_1s`, rolls readings up per zone per second (phones, horizontal motion, peak rotation) and refreshes every 5 s: the per-second zone history without rescanning raw rows.
- Replays can read a recorded run back out of Tiger and play it through the same detector as live phones.
- If the database is unreachable, the same records go to local JSONL files instead and nothing stops.

**Google Gemini: writes the sentence and reads the floor plan.**
- When an alert fires, Gemini gets the facts (place, level, kind of incident, how dense, which way the push travels, the nearest exit) and returns structured JSON, a headline and one action, for example "Stage front: crowd push travelling left to right." / "Stop entry and open the side exit now." Early warnings are worded as a projection ("reaches a dangerous 4 per m² in about 12 s").
- On the Venue page it reads an uploaded floor-plan image and suggests the venue size, stage, exits and walls, which staff review before applying.
- It never decides whether something is dangerous: the alert exists before Gemini is asked. 5 s timeout, then a template sentence. A message staff wrote for an area replaces the action word for word.

**ElevenLabs: says it out loud.**
- Each briefing is turned into an MP3 (Flash v2.5 model) that the dashboard plays the moment the alert arrives, so a steward who isn't looking at the screen still hears "Stage front: crowd push…".
- An alert nobody acknowledges is re-voiced as "Still unacknowledged. …"; "Escalate now" does the same on demand.
- A fallback clip is generated once at startup, so there's always something to play. Without a key, the browser's own voice reads the text.

**.Tech domain: the address on the QR code.** **pulsecrowd.tech.** A short, memorable HTTPS address for the join QR, pointing at the Cloudflare tunnel. Phones need HTTPS for the motion sensors, so this is what makes "scan and join" work.

**Not sponsors, but they'll ask** (check against this year's track list, `[TRACKS]` in TRACKS.md):
- **Cloudflare Tunnel** gives the laptop a public HTTPS address with no port forwarding.
- **Arduino UNO R4 WiFi and ESP32** are the sign and zone lights (question 27).

## If you don't know

Say "I don't know; here's how I'd find out" and name the test. Don't guess a number.
