# Pulse: the demo video and the plan to demo day

Two parts: what to do between now and judging, in order, and the word-for-word script for the 3-minute Devpost video. The live table script is [PITCH.md](PITCH.md); the clicks and fallbacks are [DEMO.md](DEMO.md); the boards are [TABLE-DEMO.md](TABLE-DEMO.md).

## The plan, in order

### 1. Mac setup (once, at home, about an hour)

- [ ] Install Go, Node, Arduino IDE 2 (or `brew install arduino-cli`) and `cloudflared` ([SETUP.md](SETUP.md)).
- [ ] Clone the repo, `make build`.
- [ ] Copy `~/.cloudflared/` (`cert.pem`, `config.yml`, the tunnel's `.json`) from the PC to the Mac. Treat the `.json` like a password. Then `cloudflared tunnel run pulse` on the Mac: `https://pulsecrowd.tech` now points at the Mac.
- [ ] `.env` on the Mac: the API keys, `PUBLIC_URL=https://pulsecrowd.tech`, and `scripts/boards.sh env --write` for the boards over USB.
- [ ] Boards into the Mac: `scripts/boards.sh flash`, then `scripts/boards.sh status` shows all three "up to date".
- [ ] `./bin/pulse -check` green; `scripts/preflight.sh` says **GO**.

### 2. Real-phone checks (30 minutes, needs two other people)

This is the most important step: almost every number in the pitch comes from the simulator or synthetic phones.

- [ ] An iPhone on mobile data opens `pulsecrowd.tech`, joins, allows motion, gets a number. Lock it for 10 s, unlock: same number.
- [ ] Scan the QR from inside Instagram on an iPhone and an Android: the page tells them how to open it properly.
- [ ] Three people in a row: jump together, **stays calm**. Shove passed down the row every ~3 s, **red in about 8 s**. Note the real time.
- [ ] Two people only: a push shows yellow.
- [ ] **Surge around the real phones**: each phone turns red with an arrow and says it's a drill.
- [ ] Write what actually happened into [QA.md](QA.md) questions 1, 17 and 18. If red took much longer than 8 s, change "about 8 seconds" in PITCH.md and the deck to what you saw.

### 3. Record the video (1–2 hours, see the script below)

- [ ] Rehearse the run twice, then record. Keep the best take of each shot; edit them together.
- [ ] Export 1080p, under 3:00. Upload (YouTube unlisted works for Devpost) and keep a local copy on the Mac: it's the last fallback.

### 4. Devpost submission

- [ ] Fill the checklist in [TRACKS.md](TRACKS.md): description, the video link, the repo link, screenshots, which prize tracks.
- [ ] Only enter tracks you can show. The .Tech track needs `pulsecrowd.tech` live.
- [ ] Fill or cut the remaining placeholders: price and buyer (deck slide 12, QA question 21), your origin story (optional).

### 5. Day of judging

- [ ] 10-minute setup at the table ([DEMO.md](DEMO.md)), escalation off, preflight **GO**.
- [ ] Your two phones joined as #1 and #2, so one judge makes a row of three.
- [ ] Deck open in a second tab ([deck/index.html](deck/index.html)) for a judge who wants slides.
- [ ] Between judges: the 30-second reset in DEMO.md.
- [ ] Something breaks: drop a rung on the fallback ladder in DEMO.md; never debug in front of a judge.

## Recording setup

- **Screen:** on the Mac, QuickTime Player → File → New Screen Recording, or press ⇧⌘5. Dashboard full screen (⛶), light theme reads better on video, browser zoom 110 %.
- **Phones:** record each phone's screen (iPhone Control Centre → Screen Recording; Android quick settings → Screen recorder). Hold one phone up to a second camera for the "look at your phone" shot; it reads better than a screen capture.
- **Table:** a phone on a stand filming the table from the side: laptop, sign, both lights, people in the row.
- **Sound:** record the voice-over separately in a quiet room and lay it over. Keep the dashboard's spoken briefing audible in shot 5 and 8: it's part of the demo.
- **People:** two friends plus you. They agree to be filmed. Phones held in front of the chest.

## The 3-minute screen recording (recommended)

One continuous recording of the dashboard while you talk: 3:00, about 450 spoken words, no camera, no slides, only trimming. The simulator supplies the crowds and your own phone supplies the real-phone moment, so nobody else is needed.

### Before you hit record

- Server and tunnel running; `./bin/pulse -check` green. Dashboard full screen (⛶), light theme, browser zoom 110 %.
- **Settings:** escalation off, spoken alerts on, volume up.
- **Your phone:** your Android joined from `pulsecrowd.tech` (it shows "You're #1 in the row"). Mirror it onto the Mac with **scrcpy** (`brew install scrcpy`, USB debugging on) and put its window at the right edge of the screen. Without scrcpy, skip the phone window and use the fallback line at 1:55.
- **Areas & alerts:** one area drawn over the stage front, named "Stage front", with the rule "more than 4 people per m² for 5 seconds".
- **Simulation page:** scenario **Concert**, 250 people, 60 %, not started.
- **Your phone runs the Pulse Android app** for this recording (not the browser), so the "App mode: Bluetooth on" line and the beacon readings are real. Join with it before recording.
- **Boards:** the sign and both zone lights powered and showing online on the Hardware page, each dragged to its place on the map (or **Set up table demo**).
- **Check the mesh layer in the dry run:** on Live, tick **Phone links** while the simulation runs and confirm links draw between the dots. If nothing draws, stay on the Live map for that line and don't point at links.
- Do one full dry run: you'll learn how long the surge takes to go red and where to keep talking.
- Record: ⇧⌘5 → Record Entire Screen, microphone on.

### Script

| Time | Your clicks | Say (word for word) |
|---|---|---|
| 0:00 | **Live** page, QR showing | "In October 2022, 159 people died in a crowd crush in Itaewon, in Seoul. Nobody inside the crowd could see it building, and nobody outside could either. This is Pulse: early warning for crowd crushes, using the phones already in the crowd." |
| 0:15 | Point at the QR, then your dot, then the mirrored phone | "Attendees scan one code at pulsecrowd.tech. No account, no name. Each phone becomes a dot on the steward's map and streams ten motion readings a second." |
| 0:26 | **Areas & alerts**: the "Stage front" area and its rule | "Staff mark the risky spots and set the rules in plain words, and Gemini can read a floor plan to find the stage and exits." |
| 0:36 | **Simulation** → **Start simulation** → **Dance** | "Now a crowd: 250 simulated people, 60 percent with the app. They're dancing, everyone moving at the same moment. Pulse ignores it." |
| 0:47 | **To the stage**, then **Surge**; wait for amber, then red; the briefing plays | "A crush is different. People pack in, and a push travels from person to person, a fraction of a second apart. Pulse compares every phone with its neighbours, and goes red where the crush is, not everywhere. The steward hears one sentence and one action." *(let the voice play)* |
| 1:17 | Point at **Ground truth vs Pulse**, then the card's **Why did it fire?** | "The simulator knows the true pressure on every body. Pulse only sees the phones, and it warned before the crowd became dangerous. Every alert shows its evidence: the math decides, the AI only writes the sentence." |
| 1:32 | **Stop**, then **🌊 Surge around the real phones**; point at the mirrored phone turning red | "Here's a real phone, mine, placed inside the simulated crush. It turns red, labelled as a drill, and points me out: sideways, never against the push." |
| 1:50 | **Live** → tick **Phone links** in the map legend; point along the links | "Now the part I'm proudest of. The phones don't only talk to the server. They link directly to the phones around them, peer to peer, and pass each other's positions and warnings along. If a phone loses its connection in a jammed network, its data relays through its neighbours, and a warning can still reach it. Phones that move together also confirm they're side by side, which sharpens everyone's position." |
| 2:12 | **Hardware** page: "Board readiness: 3 of 3"; point at the sign and the two zone lights. Then **Alert drill** → **Send drill** and point at the per-board ticks | "And the room itself helps. This Arduino sign and these two ESP32 zone lights are plugged into my laptop right now. When Pulse alerts, the sign flashes stop and each light shows its zone, and this drill just sent that to all three. They also count the Bluetooth devices around them, anonymously, as a second measure of how full a spot is." |
| 2:28 | Stay on the Hardware page; point at each board's beacon name (PULSE-A, PULSE-B) | "What I'm building next: each board also broadcasts a Bluetooth beacon from a known spot, so they can be anchors. GPS is metres off indoors. A Pulse app would measure the signal from each anchor and work out where the phone is, precisely, indoors. I've already built an Android version, and in testing the phone heard the anchors and the anchors heard the phone." |
| 2:44 | Back to **Live** | "Honest status: the crush results are from the simulator, and the mesh and app are tested on a handful of devices, not a real crowd yet. The goal is Pulse inside the ticketing or wallet app people already carry, for venue safety teams. Pulse: pulsecrowd.tech." |
| 3:00 | Stop recording | |

### Notes

- The surge's time to red varies by run. If it's slow, keep talking over the amber stage; trim silence afterwards rather than cutting the moment it turns red.
- Say "simulated" whenever the crowd is simulated, as the script does. It's the first thing a sharp judge checks.
- What's verified, so you can answer if asked: on your Galaxy, the app heard both zone lights and both zone lights heard the app. The mesh relay was tested between browser tabs, and screen-off streaming on an emulator. The script's "tested on a handful of devices" covers this; don't claim more.
- **Boards side by side on the desk:** don't show distances or positions from the boards in the video. Next to each other they can't be told apart by signal strength, so any position would look wrong. Show what works (readiness, the drill reaching all three, the device counts) and describe positioning as the next step, as the script does. Anchors need to be a few metres apart to locate anything.
- The "wow" lines (1:50–2:44) describe what's built plus the plan. Keep "the plan is the app" as a plan: precise beacon positioning hasn't been measured yet.
- Two phones can't show a real push going red (that needs three), which is why this version uses the simulation for the crush. The live table demo is where judges push each other.

## The full 3-minute video script (with table footage)

About 400 spoken words. Read at a calm pace; the shots carry the rest. Times are targets.

| # | Time | On screen | Voice-over (word for word) |
|---|---|---|---|
| 1 | 0:00–0:12 | Black. White text fades in: "159 people. Itaewon, Seoul. 29 October 2022." Then: "Nobody inside could see it building." | "In October 2022, 159 people died in a crowd crush in Itaewon, in Seoul. Nobody inside the crowd could see it building, and nobody outside could either." |
| 2 | 0:12–0:27 | Pulse logo, then the table: laptop, sign, two zone lights, three people scanning the QR. | "This is Pulse: early warning for crowd crushes, using the phones already in the crowd. You scan one code. No app." |
| 3 | 0:27–0:40 | Phone close-up: "You're #3 in the row." Cut to the dashboard: three named dots appear in a row. | "Each phone gets a name and a place in the row, and shows up on the steward's map. It streams ten motion readings a second." |
| 4 | 0:40–0:55 | The three jump together. Dashboard: dots flicker, zone stays calm, no alert. | "First, everyone jumps together. That's a dance: everyone moves at the same moment. Pulse ignores it." |
| 5 | 0:55–1:25 | One person shoves the next, passed down the row, again every few seconds. Dashboard: links light up along the row; yellow, then red. The voice briefing plays. The sign shows STOP; the zone light strobes. | "A crush is different. It's a push that travels from person to person, with a fraction of a second between each. Pulse compares every phone with its neighbours, and when the same shove arrives one after another down a chain of people, it goes red. The steward hears one sentence and one action." *(pause for the spoken briefing)* |
| 6 | 1:25–1:40 | A phone held up to the camera: red screen, "Crowd danger near you", the arrow. | "And the person in the crowd gets a direction: sideways, out of the push, never against it." |
| 7 | 1:40–1:55 | Click a red link: the "Why did it fire?" panel, both traces, the correlation curve, the ticks. | "Every alert shows its evidence. The math decides. Gemini only writes the sentence, and if it's down, a template does and the alarm still fires." |
| 8 | 1:55–2:20 | Simulation page → Surge around the real phones. Simulated bodies pack around the three real dots and turn red; the phones show "Simulated crowd around you — drill". | "Three people can't make a real crush. So we put their real phones inside a simulated one: two hundred and twenty people, packing in. The simulator knows the true pressure on every body. Pulse only sees the phones, and warns them." |
| 9 | 2:20–2:38 | Deck slides 8 and 9, or the evaluation card: "0 of 600", the GPS bars. | "In the simulator, none of six hundred look-alikes, like dancing, swaying or marching, went red. The honest limit: phone GPS is metres off. Pushes still show up through shared motion; a tightly packed crowd doesn't yet." |
| 10 | 2:38–2:50 | Quick cuts: Venue page reading a floor plan, the sign over USB, the Android app, a classroom simulation emptying. | "It reads floor plans, drives signs and lights over USB, and simulates concerts, classrooms, theatres and stadium gates." |
| 11 | 2:50–3:00 | The Pulse logo, `pulsecrowd.tech`, "Early warning for crowd crushes, using the phones already in the crowd." | "Pulse. For venue safety teams and ticketing platforms, through the ticket app people already have." |

### Notes for the edit

- Keep shot 5 as long as it takes to go red for real; speed it up 2× rather than cutting it, and say so on screen ("2× speed") so nobody thinks it's faked.
- Label shot 8 on screen: "Simulated crowd". The script already says it; the label makes it unmissable.
- Use only the numbers above. They match [EVAL.md](EVAL.md) and the deck. If your real-phone test gave a different time in step 2, use that time in shot 5.
- No footage or photos of real disasters or victims. Text on black is enough for shot 1.
