# Pulse: the table pitch

**One line (sentence test):** A steward sees a crowd crush building from the phones already in the crowd, and each person in it gets told which way to move.

**Track line:** Early warning for crowd crushes, using the phones already in the crowd.

The run sheet (clicks, setup, fallbacks, reset between judges) is [DEMO.md](DEMO.md). The hard questions are [QA.md](QA.md). Slide content is [DECK.md](DECK.md).

## Opening number: `[VERIFY]` before use

None of these are verified. Check each against its source before saying it or putting it on a slide. If it can't be checked in time, drop it and open on the demo. One verified number beats three.

| Claim as drafted | Source to check |
|---|---|
| Itaewon, Seoul, 29 Oct 2022: 159 people died in an alley about 3.2 m wide | Korean government / National Assembly investigation report; Reuters or AP. The toll was reported as 158, then 159: use the final official figure. |
| Astroworld, Houston, 5 Nov 2021: 10 people died in a crowd surge at the stage | Houston Police Department report (2023); Harris County Medical Examiner. |
| Above about 5 people per m², a crowd starts moving as one body and pushes travel through it | G. Keith Still, *Introduction to Crowd Science* (2014) and his density/risk tables; Fruin's level-of-service work. Check whether the threshold for your point is 4, 5 or 6 per m². |

`[VERIFY: opening line]` = the one claim you checked, in one sentence.

## Origin

`[ORIGIN: Dan's own first-person moment, if there is one: a concert, a festival exit, a packed station. Recent and specific, two sentences. If there isn't one, cut this and open on the number.]`

## Before the judge sits down

Boards on, phones lined up, escalation off, sound on, QR on screen (DEMO.md, "10-minute setup"). Two of Dan's phones are already in the row as #1 and #2, so one judge is enough to make a row of three.

## The script (2:00)

The judges' own phones carry the demo. Everything up to 1:25 is real motion from real phones; from 1:25 the crowd around them is simulated, and the script says so.

| Time | Say | Do |
|---|---|---|
| 0:00 | `[VERIFY: opening line]` "Nobody inside a crush can see it building, and nobody outside can either." | Dashboard full screen, Live page. Sign and lights calm on the table. |
| 0:10 | "Pulse is early warning for crowd crushes, using the phones already in the crowd. Scan this, please. No app." | Point at the QR. |
| 0:20 | "Your phone has a name and a number now. Stand in that order, phone in your hand." | Their screens say "You're #3 in the row. Stand to the right of #2 (its name), as you face the big screen." Dots appear with `#n` beside the boards on the map. If the order is off: **Line up phones now**. |
| 0:30 | "First, jump together. A few times." | Dots may flicker yellow. Zone stays calm. |
| 0:40 | "That's a dance: everyone moves at the same moment. A crush is different: a push that travels person to person, with a delay. #1, give #2 a gentle shove on the shoulder and pass it on. Keep it going every few seconds." | Links light up along the row in the direction of the push. Yellow in about 4 s, red in about 8 s. |
| 0:55 | (Let the voice play.) | Spoken briefing. Sign flashes STOP. The zone light strobes red. |
| 1:00 | "Now look at your phone." | **The moment.** Their screen turns red: "Crowd danger near you. Move this way", an arrow, a mini-map, "Move diagonally, not against the push." |
| 1:08 | "Steward gets one sentence and one action. The person in the crowd gets a direction, sideways out of the push, never against it." | Alert card → **Acknowledge**. |
| 1:12 | "Math detects, AI explains. This is why it fired: the same motion on both phones, a fraction of a second apart, in a chain of three. Gemini only writes the sentence. If it's down, a template does, and the alarm doesn't change." | Click a red link → "Why did it fire?": both traces, the correlation curve, the checks. |
| 1:25 | "Five people at a table can't make a real crush. So let's put your phones inside one." | Simulation page → **🌊 Surge around the real phones**. |
| 1:30 | "Two hundred and twenty simulated people pack in around your real phones. The simulator knows the true pressure on every body; Pulse doesn't, it only sees the phones." | Bodies turn amber then red around their dots. Their phones show "Simulated crowd around you — drill" and an arrow within seconds; red in about 10 to 15 s. Voice, sign, light again. |
| 1:45 | "Honest limits: the push you made was real; everything after it was simulated, and real-phone testing so far is one Android plus tonight. And GPS indoors is metres off: pushes still show up through shared motion, packing doesn't yet." | Stay on the simulation. |
| 1:52 | "Who pays: venue safety teams and ticketing platforms. No hardware to install: attendees opt in through the ticket app." | **■ Stop simulation**. Back to Live. |
| 2:00 | Stop. Wait for the question. | |

**Timing notes.** Push to red is about 8 s once the pushes keep coming (one every ~3 s, three to five phones), measured on synthetic phones calibrated on a real Android, not yet on judges. A single push only reaches yellow, by design. If the room is loud, cut 1:12 to one sentence and click the panel on the way past.

**Only one judge?** Dan's two phones plus the judge make three, which is the minimum for red. With only two phones in the row, a push shows yellow ("a push travelled from #1 to #2"), never red. Say so; then go to 1:25.

**Optional, if a judge asks "what about a crowd that isn't pushing but is pressed together?"** Ask the row to lean shoulder to shoulder and sway together, not to a beat. After about 15 s the map draws a yellow "moving as one" band. It is never red: it can't tell pressure from friends rocking together, and the dashboard says so.

## The 30-second version

`[VERIFY: opening line]` "Pulse turns the phones already in a crowd into an early-warning network. Scan this. Jumping together stays calm. A shove passed down the line goes red in about eight seconds: the steward hears what to do, the sign says stop, and your phone points you out of the push. The math decides; Gemini only writes the sentence. Attendees opt in through the ticket app; there's nothing to install in the venue."

## Pocket lines

- **Why it's different:** cameras count heads from above, Wi-Fi and Bluetooth analytics count devices in a zone. Pulse measures how a push moves from one person to the next, and talks back to each person.
- **Dancing:** "Jumping together has zero lag between neighbours. A push has a lag that travels. Only the second one alarms." (No look-alike run went red in 600 simulator runs: see the numbers card.)
- **Two phones:** "Two phones can show a push between them, as a yellow. Red needs it to travel through at least three people."
- **GPS:** "With realistic phone GPS, about 5 m off, pushes are still found by shared motion: 80 of 80 in the simulator. Packing isn't: you can't see a 2-metre knot through 5 metres of error. Positions good to about a metre bring it back."
- **When it's wrong:** "Every alert is a card with its evidence. Staff acknowledge or resolve it. Nothing acts on the crowd except an arrow."
- **Hardware:** "The sign and lights are for staff who aren't watching a screen. They run over USB here; Pulse works without them."
- **Gemini:** "Two jobs: one-sentence briefings with structured output, and reading a floor plan into walls, stage and exits. It never decides danger."

## Numbers card (only these, each with its condition)

| Say | Condition | Source |
|---|---|---|
| A shove passed down a row of 3–5 goes red in a median of about 8 s (7.8–8.0) | table profile on, a push every 3 s, 10 random rows per size, synthetic phones held in the hand, noise calibrated on one real Android | `go test -run TestTableMeasure -v ./server/internal/detect` (DEMO.md quotes the table) |
| Jumping, dancing, walking, handling, standing: 0 of 10 yellow for 2–5 phones | same | same |
| Two phones, a push: yellow in a median 1.2 s, never red | same | same |
| Surge around the phones: every real phone red with an arrow after 9.5–12 s | in-process test, 1–5 phones at five spots, 220 simulated people | `TestHybridSurgeSpots` |
| 0 of 600 look-alike runs went red | scripted simulator signals, 20 seeds, ideal and realistic phones | EVAL.md |
| With realistic GPS (5 m median), 80 of 80 pushes caught, 0 of 80 packing events red (59 of 80 yellow) | simulator, seeds 1–20, no position estimator | EVAL.md, messy phones |
| With the position estimator, 58 of 60 surges go red, but about 5 s after the danger starts | simulator, realistic phones, seeds 201–220 | LOCATE.md |
| With exact positions, red a median 17.8 s before the danger, 40 of 60 surges | crowd simulator, seeds 1–20 | EVAL.md |
| 1000 phones on one Go server, 10,000 messages/s, none dropped, dashboard at 10 Hz | load generator on the same machine; server was in sim mode | loadtest.md |

## Don't say

- "AI detects crushes." The math detects; Gemini words it.
- "Prevents deaths" or "saves lives." Say "early warning" and "one more signal for the safety team."
- "Works anywhere." It needs the page open, HTTPS, and positions good to about a metre to see packing.
- "Tested on real crowds." Nothing has been. Say "simulator and synthetic phones; real-phone testing is one Android so far."
- Any number not on the card above or in EVAL.md, LOCATE.md or loadtest.md. No percentages of anything real.
- "Validated." The crowd simulator matches walking speeds (Weidmann); it is a test bench, not evidence about real crushes.
