# Pulse: pitch deck content

Thirteen slides (the last is a backup). Each one: a title, the few words that go on it (a statement or one big number, not bullets), what to show, and speaker notes. Only numbers from EVAL.md, LOCATE.md, loadtest.md, the README and the table measurement (DEMO.md) appear, each with its condition in the notes. Placeholders are `[…]`. The live script is [PITCH.md](PITCH.md); the deck is for a judge who wants slides, the Devpost gallery, or a projector.

Style: dark background, one colour per level (calm / yellow / red, as on the dashboard), large type, the ripple logo.

---

### 1. Title

**On the slide:** Pulse. Early warning for crowd crushes, using the phones already in the crowd.

**Show:** the ripple logo; under it the join QR and the `[TECH: .tech URL]`.

**Notes:** "Scan this while I talk. No app." Let the first phones join while slide 2 is up.

---

### 2. The problem

**On the slide:** 159: lives lost in the Itaewon crowd crush, Seoul, 29 October 2022 (checked; see PITCH.md). Nobody inside could see it building.

**Show:** black slide, the number large, the place and year small. No photo of victims.

**Notes:** Only a number checked against its source (PITCH.md lists the sources). If none is verified, use the sentence alone: "In a crush, nobody inside can see it building, and nobody outside can either." `[ORIGIN: Dan's own moment, two sentences, if there is one]`

---

### 3. The insight

**On the slide:** A crush is a push that travels from person to person. A dance is everyone moving at once.

**Show:** two strips of three phone traces. Left (dance): peaks lined up vertically. Right (push): the same peak shifted along each trace, an arrow showing the delay.

**Notes:** "Pulse compares each phone with its neighbours. Same motion, a fraction of a second later, passed through three people in a row: that's a push. Same motion at the same moment: that's a dance, and it's ignored."

---

### 4. How it works

**On the slide:** Phones → one server → steward, sign, and each person's phone.

**Show:** the architecture as a left-to-right diagram: phones (web page, 10 readings/s) → Go server (clock sync, detector every 250 ms, density, estimator) → dashboard + voice, sign and zone lights, and a red arrow back to each phone. Small label on Gemini: "writes the sentence".

**Notes:** "Math detects, AI explains. The alarm is signal processing: cross-correlation between neighbours, density from clustering. Gemini writes one sentence and reads floor plans; ElevenLabs speaks. Every outside service can fail and the alarm still fires."

---

### 5. What the person in the crowd sees

**On the slide:** Move this way.

**Show:** a phone screenshot: red, "Crowd danger near you", arrow, mini-map, "Move diagonally, not against the push." Next to it, the "#3 in the row" join screen.

**Notes:** "The steward gets one sentence and one action. The person in the crowd gets a direction: sideways out of a push, toward space, toward an open exit when there is one. Never against the push."

---

### 6. At this table

**On the slide:** About 8 s from the first shove to red.

**Show:** photo of the table (laptop, sign, two lights, phones in a row) and the dashboard map with red links along the row.

**Notes:** "Three to five people in a row, a shove passed on every 3 s: red in a median 7.8 to 8.0 s; jumping, dancing, walking or handling the phones: no warning at all. Two phones can show a push as yellow, never red. Those are synthetic phones held in the hand, 10 random rows each, with noise from a real Android: today's judges are the first real test." (DEMO.md table)

---

### 7. Real phones in a simulated crush

**On the slide:** Your phone, inside 220 simulated people.

**Show:** screenshot of the Simulation page mid-surge: bodies coloured by pressure, the real phones as named dots in the middle, the "Ground truth vs Pulse" panel.

**Notes:** "You can't make a crush at a table, so the real phones stand in a simulated one. The simulator is a Social Force model whose walking speeds match Weidmann's data within about 0.1 m/s from 0.5 to 5 people per m², and it knows the true pressure on every body; Pulse only sees the phones. In tests every real phone went red with an arrow after 9.5 to 12 s." Say clearly that this part is simulated.

---

### 8. What holds

**On the slide:** 0 of 600 look-alikes went red.

**Show:** a grid of look-alike scenarios (dance, sway, Mexican wave, march, pockets, bumps, walk-past…) all green; one line under it: "ideal, realistic and harsh phones".

**Notes:** "In the simulator, over 20 random crowds each, none of the things that look like a push raised a red, with exact phones, realistic ones (5 m GPS error, pockets, dropouts) or harsh ones. With realistic GPS, pushes are still found by shared motion: 80 of 80. With exact positions, a crowd building at the stage goes red a median 17.8 s before the simulated danger." (EVAL.md, seeds 1–20; thresholds tuned on the same simulator)

---

### 9. What breaks

**On the slide:** You can't see a 2-metre knot through 5 metres of GPS error.

**Show:** two bars: "pushes caught with realistic GPS: 80/80" and "packing caught red with realistic GPS: 0/80"; a third, smaller: "with the position estimator: 58/60 surges, ~5 s late".

**Notes:** "Packing needs positions good to about a metre. With realistic phone GPS, no packing event goes red; 59 of 80 get a yellow. The position estimator, which pulls jostled phones together by their shared motion, brings 58 of 60 surges back, but about 5 s after the danger starts. And a pinned crowd barely moves, so there's no motion signature of a quiet crush. Fixes: tap your spot, seats or sections, UWB or Bluetooth ranging in a native app, capacity rules." (EVAL.md, LOCATE.md)

---

### 10. Built to run an event

**On the slide:** One Go server. 1000 phones. Nothing dropped.

**Show:** a strip of console screenshots: setup checklist, watch areas with rules, alert card with Acknowledge / Resolve, Alert drill, Hardware with board readiness. Small print: "10,000 messages/s, dashboard at 10 Hz".

**Notes:** "Load test: 1000 fake phones at 10 readings a second each, none dropped, dashboard steady at 10 Hz (generator on the same machine). Phones relay for each other over WebRTC when a connection drops. Sign and lights run over USB with no Wi-Fi. Every outside service falls back. Privacy: a random ID, venue metres, motion numbers; raw GPS never stored." (loadtest.md)

---

### 11. What each sponsor does

**On the slide:** four cards: Tiger Data (motion readings and alerts in TimescaleDB hypertables, a per-zone per-second continuous aggregate, replays), Google Gemini (one headline and one action as structured JSON; reads floor plans; never decides), ElevenLabs (speaks every briefing; re-voices unanswered alerts), .Tech domain (the HTTPS address behind the QR).

**Notes:** Details in QA.md, "What each sponsor does". Every service can be down and Pulse still alerts.

---

### 12. Who pays, and what's next

**On the slide:** Venue safety teams and ticketing platforms. Nothing to install.

**Show:** three steps left to right: "Attendees opt in through the ticket app" → "Safety team watches one screen" → "Recordings from real events tune it". Price line: `[PRICE]`.

**Notes:** "`[BUYER: one verified fact]`. Next: record real crowds with consent and replay them through the same tests with thresholds frozen beforehand, and put the client inside a ticketing app, which is what the Android shell already shows: screen-off streaming and Bluetooth. Honest limits: a web page only streams while it's open; real-phone testing so far is one Android plus today; a handful of testers proves the method, not the thresholds for 50,000 people."

---

### 13 (backup). Why did it fire?

**On the slide:** Every alert shows its evidence.

**Show:** the "Why did it fire?" panel: two traces, the correlation curve with its peak and lag, the list of checks with ticks.

**Notes:** Use as the backup slide when a judge asks how the detector decides, or if the live panel can't be shown.

---

## Placeholders in this file

| Placeholder | Slide | Fill with |
|---|---|---|
| `[TECH: .tech URL]` | 1 | the join URL, or drop it |
| `[ORIGIN: …]` | 2 | Dan's moment, or cut |
| `[PRICE]` | 11 | per event or per attendee |
| `[BUYER: one verified fact]` | 11 | a checked fact about the buyer, or cut |
