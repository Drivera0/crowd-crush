# Pulse: the 2-minute table pitch

**One line (sentence test):** A steward sees a crowd crush building from the phones already in the crowd.

**Track line:** Early warning for crowd crushes, using the phones already in the crowd.

## Opening numbers: VERIFY BEFORE USE

None of these are verified. Check each against the source before saying it or putting it on screen.

| Claim as drafted | VERIFY BEFORE USE: source to check |
|---|---|
| Itaewon, Seoul, Oct 29 2022: 159 people died in an alley about 3.2 m wide | Korean government / National Assembly investigation report; Reuters or AP coverage. Death toll was reported as 158, then 159; use the final official figure. |
| Astroworld, Houston, Nov 5 2021: 10 people died in a crowd surge at the stage | Houston Police Department report (2023); Harris County Medical Examiner. |
| Above about 5 people per m², a crowd starts moving as one body and pushes travel through it | G. Keith Still, *Introduction to Crowd Science* (2014) and his density/risk tables; Fruin's level-of-service work. Check whether the threshold he uses is 4, 5 or 6 per m² for the point you make. |

If a number can't be checked in time, drop it. One verified number beats three.

## Origin story

`[ORIGIN: Dan's own first-person moment, if there is one. A concert, a festival exit, a packed station. Recent and specific. If there isn't one, cut this and open on the number.]`

## The script (2:00)

The QR is on the dashboard before the judge sits down. Phones A, B, C are already joined (see DEMO.md).

| Time | Say | Do |
|---|---|---|
| 0:00 | "In 2022, 159 people died in one alley in Seoul. `[VERIFY]` Nobody inside could see it building, and nobody outside could either." | Dashboard full screen, Live page. |
| 0:10 | "Pulse is early warning for crowd crushes, using the phones already in the crowd. Scan this, please." | Point at the QR. Hand the judge the card with the QR if the screen is far. |
| 0:20 | "Each phone is a dot on the venue map. The server measures the distance between every pair, so it knows who stands next to whom." | Judge's dot appears. Ask them to drag it onto "Stage front". |
| 0:35 | "Two things are dangerous. A crowd packing past about 4 people per square metre, and a push travelling person to person. Pulse watches both." | Point at the cluster ring and its density number. |
| 0:45 | "You just walked into the stage front. Watch." | The area crosses its rule. Zone goes yellow, then red. |
| 0:55 | (Let the voice play: headline + action.) | Sign on the table flashes STOP; the ESP32 light for that zone goes red. |
| 1:00 | "Now look at your phone." | **The moment.** Judge's screen is red with an arrow: "Toward more space". |
| 1:10 | "The steward gets one sentence and one action. The person in the crowd gets a direction." | Click the alert card: Acknowledge. |
| 1:15 | "Math detects, AI explains. The alarm is plain signal processing: cross-correlation between neighbours' motion, density from clustering. Gemini only writes the sentence and reads the floor plan. If Gemini is down, the alarm still fires with a template sentence." | Click a red link: the "Why did it fire?" panel with both traces, the correlation curve and its peak lag. |
| 1:28 | "We test against a crowd simulator calibrated to Weidmann's walking-speed data, which knows the true pressure on every body. When a crowd builds at the stage, Pulse went red 17 to 19 seconds before it got dangerous, in 3 of 5 runs. On a sudden surge it was about 2 seconds late. `[EVAL: surge lead time with early warning on]`" | Simulation page, lead-time readout. |
| 1:42 | "Limits: a web page only streams while it's open, GPS is too coarse indoors so people tap their spot, and density counts phones, not people." | |
| 1:50 | "Who pays: venue safety teams, promoters and ticketing platforms. No hardware to install: attendees opt in through the ticket app. No names, no GPS stored." | Back to Live. |
| 2:00 | Stop. Wait for the question. | |

If friends or a teammate are at the table with phones, swap 0:35 to 1:00 for the push demo: three people in a line, push the end repeatedly, red links race along the line. It takes about 20 to 25 s to go red, so start pushing at 0:30.

## The 30-second version

"In 2022, 159 people died in an alley in Seoul `[VERIFY]`. Pulse turns the phones already in a crowd into an early-warning network. Scan this. Your phone is now a dot on the map. When a group packs too tight, or a push travels person to person, the zone goes red, the steward hears what to do, and your phone shows you which way to move. The math decides; Gemini only writes the sentence. No hardware to install: attendees opt in through the ticket app."

## Lines to keep in your pocket

- **Why it's different:** cameras count heads from above; Wi-Fi and Bluetooth analytics count devices in a zone. Pulse measures how the crowd moves, phone against neighbour, and talks back to each person.
- **Dancing:** "Everyone jumping together has zero lag between neighbours. A push has a lag that travels. Pulse only alarms on the second."
- **When it's wrong:** "Every alert opens a card with the evidence. Staff acknowledge or resolve it; nothing happens automatically to the crowd except an arrow."
- **Don't say:** "AI detects crushes", "prevents deaths", "works anywhere", or any number not in the table above or in EVAL.md.
