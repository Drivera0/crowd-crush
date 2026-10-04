# Pulse: table demo run sheet

The words are in [PITCH.md](PITCH.md). This file is the clicks, the phones, the reset between judges and what to do when something breaks. Boards on the table (USB, cables, what each LED pattern means, recovery): [TABLE-DEMO.md](TABLE-DEMO.md). Don't duplicate it here; go there when a board misbehaves.

## Pack

Everything in [TABLE-DEMO.md → Pack](TABLE-DEMO.md#pack), plus:

- [ ] Dan's two phones, charged, joined as #1 and #2 (one is also the hotspot)
- [ ] Printed QR card (the join link) for judges who can't see the screen
- [ ] Phone charger cable for the hotspot phone
- [ ] The Devpost video saved locally (last rung of the fallback ladder)
- [ ] Small sign for the table: "Scan to join. No app." `[optional]`

## 10-minute setup at the table

At home first, once: everything in TABLE-DEMO.md's "Before leaving home", `./bin/pulse -check` green, a phone opening the join link on mobile data.

| # | Do | Check |
|---|---|---|
| 1 | Laptop on power, sleep off, brightness up. Hub in, three boards in. | Each board lights up (TABLE-DEMO.md, step 1). |
| 2 | Start the tunnel (`cloudflared tunnel run pulse`, or `make tunnel`), then `./bin/pulse`. | Log shows the sign, `PULSE-A`, `PULSE-B` opened. |
| 3 | Dashboard → **Hardware** → **Set up table demo**. | Board readiness: 3 of 3. Boards and laptop in a row on the map, demo spot on in front of them. |
| 4 | `scripts/preflight.sh` | **GO**. Fix each ✗ as it says. |
| 5 | **Settings** → Escalation → untick **Re-announce unanswered alerts**. Type your name under **Your name**. Click **Turn on** for spoken alerts. Volume up. | Escalation note confirms it's off. |
| 6 | Join Dan's two phones with the QR (camera app → browser). Join → allow motion. | They say #1 and #2. Dots with `#1`, `#2` beside the boards. |
| 7 | Rehearse once with your two phones plus a third (a friend's or a spare): jump (stays calm), push down the row (red), Acknowledge, Resolve. | Voice, sign STOP, a zone light strobes. |
| 8 | **Simulation** → **🌊 Surge around the real phones** once. Wait for red on your phones, then **■ Stop**. | Phones show "Simulated crowd around you — drill" and an arrow, then go back to calm. |
| 9 | **Simulation** → **Saved runs**: pick the best run, **▶ Play** to the end, stop. Leave it selected. | It's your fallback. |
| 10 | **Reset** (below). Live page, full screen (⛶), QR showing. | Ready. |

If the phone row straddles the middle of the map, the push can light both zones and both lights. That's fine; mention it if asked ("the row is across two zones").

## The run (clicks only)

| Time | Click / action | What should happen |
|---|---|---|
| 0:10 | Point at the QR | Judges scan with the camera app. iPhone asks for Motion: Allow. |
| 0:20 | Ask them to stand in number order to the right of #2. If the numbers are off: Live → **Demo & diagnostics** → **Line up phones now** | Each phone: "You're #n in the row. Stand to the right of #n−1". Map: `#n` on each dot. |
| 0:30 | "Jump together" | Dots may go yellow. Zone stays calm, no alert. |
| 0:40 | "#1 shoves #2, pass it on, every few seconds" | Links light along the row. Yellow ~4 s, red ~8 s (medians, synthetic). |
| 0:55 | Nothing | Briefing card, voice, sign STOP, zone light strobes. |
| 1:00 | "Look at your phone" | Red screen, "Move this way", arrow, mini-map. Android vibrates. |
| 1:08 | Alert card → **Acknowledge** | Card shows acknowledged, "by Dan". |
| 1:12 | Click a red link on the map (or the card's **Why did it fire?**) | Traces, correlation curve, the checks with pass/fail. |
| 1:25 | **Simulation** → **🌊 Surge around the real phones** | 220 simulated people pack around their dots; "What Pulse raised" shows a SIMULATION card. |
| 1:30 | Point at their phones | "Simulated crowd around you — drill", arrow within ~5 s, red within ~10–15 s. Voice, sign, light again. "Ground truth vs Pulse" shows the true pressure. |
| 1:52 | **■ Stop** | Phones go back to the live pipeline (calm). |
| 2:00 | Live page | Map for questions. |

**Only one or two judges.** Dan's phones make up the row. Two phones alone: the push is yellow ("a push travelled from #1 to #2"), never red. Red needs three.

**"Moving as one" (optional, if asked about a crowd pressed together).** The row leans shoulder to shoulder and sways together, not to a beat. A yellow "moving as one" band appears after a median 14–17 s (synthetic). Never red.

### What the table measurements say

From `go test -count=1 -run TestTableMeasure -v ./server/internal/detect`: 10 random rows per cell, phones held in the hand at a tilt, 0.6 m apart, noise calibrated on one real Android's readings. Synthetic, not judges. Table profile on (the demo spot is on).

| Case | Phones | Yellow | Median yellow | Red | Median red |
|---|---|---:|---:|---:|---:|
| still, jump, dance, walk, handle | 2–5 | 0/10 | — | 0/10 | — |
| push (one every 3 s) | 2 | 10/10 | 1.2 s | 0/10 | — |
| push (one every 3 s) | 3 / 4 / 5 | 10/10 | 3.8 / 4.0 / 4.2 s | 10/10 | 7.8 / 7.8 / 8.0 s |
| one push only | 3–5 | 10/10 | 3.8–4.2 s | 0/10 | — |
| moving as one | 2 / 3 / 4 / 5 | 10, 9, 10, 10 of 10 | 15.2 / 16.8 / 16.5 / 14.2 s | 0/10 | — |

Profile off (demo spot off): the same pushes go red in a median 10.2–10.5 s, two phones show nothing, moving as one shows nothing.

Surge around the phones (`go test -run TestHybridSurge -v ./server/internal/app`, in-process, synthetic phones): every real phone red with an arrow after 9.5–12.0 s at five spots with 1–5 phones; in the 4-phone run the arrow appeared at 4.5 s and red at 14.75 s.

## Reset between judges (30 seconds)

1. If a simulation is running: **■ Stop** (Simulation) or **■ Stop simulation** (Live → Demo & diagnostics).
2. Resolve every open card (**Resolve…**). Then Live → Incident timeline → **Clear**, click twice within 4 s. Clear keeps open and acknowledged real incidents, so resolve first.
3. Ask the leaving judges to close the page or tap Leave. Their spots are kept 30 s in case they reconnect.
4. Live → Demo & diagnostics → **Line up phones now**, so Dan's phones are #1 and #2 again.
5. Check escalation is still off (Settings). Sign and lights back to calm (TABLE-DEMO.md, "What the lights mean").

## The 60-second version (rushed judge)

| Time | Do | Say |
|---|---|---|
| 0:00 | Dan's two phones are in the row; QR on screen | "Early warning for crowd crushes, from the phones already in the crowd. Scan this." |
| 0:10 | Judge is #3 | "Stand to the right of #2." |
| 0:15 | Dan shoves #2's holder, who passes it on; repeat every 3 s | "A crush is a push that travels person to person, with a delay. Dancing has no delay." |
| 0:25 | Red: voice, sign, light | "Steward hears what to do." |
| 0:30 | Point at the judge's phone | "You get a direction: sideways, out of the push." |
| 0:40 | Click a red link | "Math decides; Gemini only writes the sentence." |
| 0:50 | — | "Simulated and synthetic so far; real-crowd recordings are next. Venues pay; attendees opt in through the ticket app." |

If time runs out before red, skip to the simulation: **🌊 Surge around the real phones** is the fastest thing that turns their own phone red without anyone pushing.

## Fallback ladder

Drop one rung the moment the current one stalls for more than 10 s. Don't debug in front of a judge.

| Rung | When | How | Say |
|---|---|---|---|
| 1. Judges' phones | default | as above | — |
| 2. Dan's phones only | judges' phones won't join | Push between Dan's two phones (yellow), then Surge around the real phones (red on Dan's phones) | "Here are two phones I've already joined." |
| 3. Simulation | no phones join, tunnel down | **Simulation** → **▶ Start simulation** → **To the stage** → **Surge** | "250 simulated people, 60 % with the app. Same detector, same alerts." |
| 4. Saved run | simulation fails or the laptop struggles | **Simulation** → **Saved runs** → the good run → **▶ Play** | "A recorded run through the same pipeline." |
| 5. Video | the server won't start | the local copy of the Devpost video | "Here's the three-minute video; happy to show code." |

**Alert drill → Send drill** fires briefing, voice, sign and lights on demand at any rung and reports what each output did. It says "This is a drill."

## When something fails

| Failure | What you see | Do this |
|---|---|---|
| Venue Wi-Fi bad | Gemini / ElevenLabs slow; tunnel drops | Laptop onto Dan's phone hotspot (Maximize Compatibility on). Boards are on USB and don't care. Judges' phones use mobile data. |
| Tunnel down | QR page won't load on mobile data | Restart it. A quick tunnel changes address: the dashboard picks up the new one, or paste it in the QR window, then test it there. Not back in 30 s: rung 2 if Dan's phones are already joined, else rung 3. |
| No internet at all | Phones can't open the QR | Phones can't join without the HTTPS tunnel. Rung 3. Detection, template briefings, browser voice and the USB boards all still run locally. |
| Boards not on USB / not found | Readiness says Offline | TABLE-DEMO.md → Recovery. Don't power-cycle mid-pitch; boards are optional. |
| Boards over Wi-Fi instead | only if USB fails | Laptop and boards on the hotspot; `scripts/boards.sh env --wifi`. Needs a Pulse restart: do it between judges, not during. |
| Gemini down | Briefing reads like a template | Nothing. "Gemini's down, so this is the template. The alarm doesn't depend on it." |
| ElevenLabs down | Browser voice | Nothing. It falls back by itself. |
| Judge stuck on the motion screen | In-app browser (Instagram, LinkedIn…) or motion denied | The page says what to do ("Open in Safari / Chrome"). Still stuck: hand them a spare phone. |
| Push doesn't go red | Yellow, no red | Keep pushing every 2–3 s, firmer; a single push only reaches yellow. Need three phones. Then go to Surge around the real phones. |
| Phones in the wrong order | `#` numbers don't match people | **Line up phones now**, or ask people to swap places to match their screens. |
| Escalation re-announces mid-pitch | "Still unacknowledged…" | Acknowledge the card. Settings → turn escalation off. |

## Devpost video: 3-minute shot list

Record before the freeze. Screen capture at 1080p plus phone footage. Keep a local copy for rung 5.

| # | Time | Shot | Voice-over |
|---|---|---|---|
| 1 | 0:00–0:10 | One line with the opening number (only if verified) | The problem. |
| 2 | 0:10–0:25 | The table: laptop, sign, two lights, phones scanning the QR | "Pulse turns the phones in a crowd into an early-warning network. No app." |
| 3 | 0:25–0:40 | Phones show "#3 in the row"; dots line up on the map | Joining, numbered row. |
| 4 | 0:40–0:55 | Everyone jumps; zone stays calm | Why dancing doesn't trigger it. |
| 5 | 0:55–1:25 | Shove passed down the row; links light; red; voice; sign STOP; light strobes | The push, in one sentence of cross-correlation. |
| 6 | 1:25–1:40 | Close-up of a phone: red, arrow, "Move diagonally, not against the push" | "The person in the crowd gets a direction." |
| 7 | 1:40–1:55 | "Why did it fire?" panel | "Math detects, AI explains." |
| 8 | 1:55–2:20 | Surge around the real phones; bodies turn red around real dots; ground truth panel | Real phones inside a simulated crush; honest about simulation. |
| 9 | 2:20–2:35 | Evaluation card (0 of 600 look-alikes red; GPS rows) | What holds, what breaks. |
| 10 | 2:35–2:45 | Venue page: floor plan → **✦ Find stage and exits** | Gemini's second job. |
| 11 | 2:45–3:00 | Limits on one card, the one line, the URL | Limits and buyer. |
