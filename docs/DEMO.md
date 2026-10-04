# Pulse: live demo run sheet

The words to say are in [PITCH.md](PITCH.md). This file covers the clicks, the phones and what to do when something breaks. The full pre-demo checklist is in [TODO.md](../TODO.md) under "Pre-demo checklist". Do it at home first, then the short version below at the venue.

## What's on the table

| Item | Where | Job |
|---|---|---|
| MacBook, on power, sleep off, brightness up | centre, screen toward the judge | dashboard full screen (⛶) |
| Phone A (Dan's iPhone) | flat on the table, left | joined, dot inside "Stage front"; also the hotspot if venue Wi-Fi is bad |
| Phone B (second phone) | flat on the table, middle | joined, dot inside "Stage front" |
| Phone C (spare or borrowed) | flat on the table, right | joined, dot inside "Stage front" |
| Judge's own phone | in the judge's hand | joins by QR at 0:10; this is the "oh" moment |
| Arduino UNO R4 WiFi sign | front of the table, facing the judge | flashes STOP on red |
| ESP32 zone lights A and B | either side of the laptop, labelled | light the zone colour; count nearby Bluetooth devices |
| Power bank | behind the laptop | boards, if not on USB |
| Printed QR card | in front of the sign | for judges who can't see the screen QR |

Solo build, so Dan presents and drives. If a friend helps, they hold Phone C and do the push demo with Dan (see "Optional: the push").

## 15 minutes before judges arrive

Run the "At the venue, before judges arrive" block of the TODO checklist. Then these demo-specific steps:

1. `./bin/pulse -check`. Note which services are red; each one has a fallback below.
2. Start the tunnel (`cloudflared tunnel run ... pulse`, or `make tunnel`). Open the QR on a phone on **mobile data**. If it's a quick tunnel, open the dashboard through the tunnel URL so the QR matches.
3. **Areas & alerts** → draw a small area at the front of the map, name it `Stage front`, mark it **High risk**, set **Capacity** to `3` phones and a message such as "Hold entry at the stage front and open the side exit." Save.
4. Join Phones A, B, C. On each: Join → allow Motion & Orientation → tap/drag the dot inside `Stage front`. Lay them flat. The area is at its limit, not over it.
5. Check the moment works: join a fourth phone (or your laptop browser through the tunnel), drag it into `Stage front`. Within about 3 s: zone red, briefing card, voice, sign flashes, and **the fourth phone shows the red "move this way" screen with an arrow**. Acknowledge and Resolve the card. Drag the fourth dot out again.
   - If the arrow does not appear for a capacity rule, the guidance only follows density or push alerts. Switch to plan B for the moment: remove the Capacity rule and restart the server with participation about `0.25` (`./bin/pulse -dump-config > detect.json`, edit `participation`, `./bin/pulse -config detect.json`), so four phones close together read as a red cluster. Re-test. Say so if asked: it's the same knob a real event would tune.
6. Click the sound button once (spoken alerts on). Volume up.
7. **Recordings** → play one good recording to the end, then **Back to live**. Leave the picker on that recording.
8. **Simulation** → start once and stop, so you know it works on this machine. Leave the people count at 250.
9. Live page, full screen, QR visible. Phones A to C on the table, screens on (Low Power Mode off).

## The run (clicks only)

| Time | Click / action | What should happen |
|---|---|---|
| 0:10 | Point at the QR | Judge opens it with the camera app. iPhone asks for Motion: they tap Allow. |
| 0:20 | Ask the judge to drag their dot onto `Stage front` | Their dot appears in the area. Four phones, capacity 3. |
| 0:45 | Wait about 3 s | Zone yellow then red; alert card opens; briefing appears. |
| 0:55 | Nothing | Voice plays. Sign flashes STOP. ESP32 light goes red. |
| 1:00 | "Look at your phone" | Judge's screen: red, arrow, "Toward more space". It points the real way only with a compass and a GPS-anchored venue; otherwise it's relative to the mini map (stage at the top). Android also vibrates. |
| 1:10 | Alert card → **Acknowledge** | Card shows "Acknowledged". |
| 1:15 | Click any red link on the map, or open a recording with waves and click a red link | "Why did it fire?" panel: both traces, correlation curve, peak lag, checks passed/failed. If there is no red link live, show it on the replay (step 7). |
| 1:28 | **Simulation** → **▶ Start simulation** → **🎤 To the stage**, then **🌊 Surge** | Bodies coloured by pressure; "Ground truth vs Pulse" shows danger time, alert time and lead. |
| 1:50 | **Back to live** or the Live nav link | Map again for questions. |

After the judge leaves: Resolve the alert, ask the judge's dot to leave (or wait for it to go stale), check Phones A to C are still green.

### Optional: the push (needs 3 people)

Three people stand in a line about 0.6 m apart, phones flat on the chest, dots placed in a line. Push the end person's shoulder, repeat every 2 to 3 s. Red links race along the line; red takes about 20 to 25 s in the simulator, so start pushing early. Then everyone jumps together: links may go yellow, zone stays calm. That answers "won't dancing set it off?"

## The fallback ladder

Drop one rung the moment the current one stalls for more than 10 s. Don't debug in front of a judge.

| Rung | When to use | How | What to say |
|---|---|---|---|
| 1. Live phones | default | as above | — |
| 2. Simulation | judge's phone won't join, tunnel down, or phones misbehave | **Simulation** → Start → To the stage → Surge | "Here are 250 simulated people, 60 % with the app. Same detector, same alerts." |
| 3. Replay | simulation fails or the laptop is slow | **Simulation** → **Saved runs** → pick the good run → **▶ Play** | "This is a recorded run going through the same pipeline." |
| 4. Recorded video | the server won't start | the Devpost video, downloaded locally (not streamed) | "Here's the three-minute video; happy to show code." |

**Send drill** (Alert drill page) fires briefing, voice, sign and lights on demand at any rung, and reports what each one did.

## When something fails

| Failure | What you see | Do this |
|---|---|---|
| Venue Wi-Fi | dashboard can't reach Gemini/ElevenLabs; boards offline | Laptop and boards on Phone A's hotspot (Maximize Compatibility on). Attendee phones use mobile data. |
| No internet at all | phones can't open the QR | Phones can't join without the HTTPS tunnel. Go to rung 2. Detection, template briefings, browser voice and the boards on the hotspot all still run locally. |
| Tunnel down | QR page doesn't load on mobile data | Restart the tunnel. With a quick tunnel the address changes, so reopen the dashboard through the new URL to refresh the QR. If not back in 30 s, rung 2. |
| Gemini down or slow | briefing reads like a template sentence | Nothing. Say: "Gemini is down, so this is the template. The alarm doesn't depend on it." Floor-plan reading is the only feature that needs Gemini: show the layout that's already applied. |
| ElevenLabs down | browser voice instead of the ElevenLabs voice | Nothing. It falls back on its own. |
| Sign or ESP32 offline | header shows it red | Point at the dashboard instead. Don't power-cycle it during a pitch. |
| Judge's phone blocks motion | stuck on the permission screen | Ask them to open the link in Safari/Chrome, not an in-app browser. If still stuck, hand them Phone C and drag Phone C's dot instead. |
| Judge's dot lands in the wrong place | GPS fix inside a building | Ask them to drag the dot. Say: "GPS is 5 to 25 m indoors, which is why tap-your-spot exists." |

## Devpost video: 3-minute shot list

Record before the freeze. Screen capture at 1080p plus phone footage. Voice-over from the pitch, slowed down.

| # | Time | Shot | Voice-over |
|---|---|---|---|
| 1 | 0:00–0:12 | Black screen, one line of text with the opening number (only if verified) | The problem, one number. |
| 2 | 0:12–0:25 | Dashboard Home with the setup checklist, then Live with the QR | "Pulse turns the phones in a crowd into an early-warning network." |
| 3 | 0:25–0:45 | Over-the-shoulder: a phone scans the QR, allows motion, drags its dot | How joining works; no install. |
| 4 | 0:45–1:15 | Split screen: phone + dashboard. Area goes red, card, voice audible, sign flashing on the table | Density and capacity alerts. |
| 5 | 1:15–1:30 | Close-up of the phone: red screen, arrow, vibration | "The person in the crowd gets a direction." |
| 6 | 1:30–1:55 | Three people in a line, push, red links travel; then everyone jumps and it stays calm | Cross-correlation in one sentence; why dancing doesn't trigger it. |
| 7 | 1:55–2:15 | "Why did it fire?" panel; zoom on the correlation curve and the checks | "Math detects, AI explains." |
| 8 | 2:15–2:35 | Simulation: To the stage → Surge, ground-truth panel with lead time | The honest lead-time result. Detector evaluation card `[EVAL: false alarms]`. |
| 9 | 2:35–2:45 | Venue page: upload a floor plan → "Read the layout with Gemini" → stage and exits appear | Gemini's two jobs. |
| 10 | 2:45–2:55 | Hardware page: boards on the map with peer distances; ESP32 Bluetooth counts | Hardware. |
| 11 | 2:55–3:00 | Limits on one card, then the one-line pitch and the `.tech` URL | Limits and buyer. |

Keep it under 3:00 exactly. Upload as unlisted YouTube and keep a local copy for rung 4.
