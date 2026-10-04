# Pulse: the 20 hardest questions

Short, honest answers. Numbers come from the README, `docs/EVAL.md` and `docs/loadtest.md`; anything in `[brackets]` is a placeholder to fill before judging.

## How it works

**1. How does the push detection actually work?**
Each phone sends a 100 ms summary of its motion; the server band-passes the left-right axis to 0.15–1.5 Hz, because a crowd sway is slow. For each pair of physical neighbours (within 1.1 m), it cross-correlates the last 6 s of both traces at lags from −1.5 s to +1.5 s: slide one trace over the other and find the shift where they match best. A wave edge needs a strong match (|r| ≥ 0.6) at a lag of 120–1200 ms, a peak that clearly beats any other (by 0.2), and a chain of at least 3 phones carrying it in the same direction.

**2. Why doesn't dancing or jumping set it off?**
When everyone jumps to the beat, neighbours move at the same moment, so the best lag is about zero, and zero lag is never a wave. Swaying to music is periodic, so several lags match equally well and the "clear peak" test rejects it; a stadium Mexican wave is mostly vertical and gets vetoed. In the simulator, dance, sway, Mexican wave, marching, walking past, pocketed phones and random bumps all stay calm `[EVAL: false alarms / look-alike runs]`.

**3. How do you line up timestamps from phones with different clocks?**
NTP-style: on connect the server sends 8 pings, keeps the clock offset from the one with the smallest round trip, and re-syncs every 30 s. Every reading is corrected before detection, and the dashboard shows each phone's RTT and offset. The simulator tests the detector with ±25 ms of clock error.

**4. How does the density alert work?**
Every 250 ms, DBSCAN groups phones within 1.2 m of each other (at least 3), and each cluster gets a density and a trend (forming, steady, dispersing). Density is the larger of the cluster average and the 90th-percentile local density, so a packed front isn't hidden inside a big loose crowd. Yellow above 2 people/m², red above 4/m², each held 2 s, and the early warning raises yellow when the trend projects the cluster reaching danger soon.

**5. Why not machine learning?**
There is no labelled dataset of real crowd crushes from phones, and any model trained on our own simulator would just learn the simulator. A deterministic detector can be explained to a safety officer: every alert opens a panel with both phones' traces, the correlation curve and which checks passed. With real recordings, ML could tune the thresholds later; it shouldn't make the call today.

**6. What does Gemini do, and what happens when it's wrong or down?**
Gemini writes the one-line headline and action for staff, and reads an uploaded floor plan into stage, exits and walls. It never decides whether a zone is dangerous: the detector raises the alert first and Gemini only words it, with a 5 s timeout and a template sentence if it fails; a staff-written area message overrides its action word for word. The floor-plan reading is saved only after staff review and apply it.

## Honest results and limits

**7. How early does it warn? Is it ever late?**
In the Social Force simulator, which knows the true pressure on every body: when a crowd builds at the stage, Pulse went red 17–19 s before injury-level pressure in 3 of 5 runs, because the density passed its 4/m² line first. On a sudden surge into a loose crowd it was about 2 s late: the density has to hold for 2 s and the cluster has to fill. The density-trend early warning is meant to close that gap `[EVAL: surge lead time with early warning]`.

**8. Does the push detector fire in the crowd simulator?**
Almost never, and we say so. Simulated bodies are stiff discs, so a push crosses packed neighbours in tens of milliseconds, under the 120 ms per-hop floor, and in a loose crowd it dies out within about 2 m. Either real people transmit pushes more slowly than stiff discs, or the floor needs lowering for packed crowds; only real recorded pushes can settle it, and the density path covers the simulator case.

**9. How accurate is the location? GPS indoors is terrible.**
Yes: phone GPS is about 5–25 m outdoors and worse indoors, far coarser than the 1.1 m neighbour radius. So GPS fixes worse than 25 m are ignored, and the demo uses tap-your-spot on the venue map. GPS suits open-air venues and coarse clusters; fine positioning would need Bluetooth or UWB ranging between phones, which needs a native app.

**10. What does a web page stop you doing that a native app could?**
A web page only streams while it's open and the screen is on, so it can't run in a pocket all night. It can't do Bluetooth or UWB ranging between phones, background location, or phone-to-phone relay when the cell network jams, and iPhones won't vibrate from a web page. A real deployment would put the same client inside the event's or ticketing platform's app.

**11. Most people won't have it open. Doesn't that break density?**
Density counts phones, so it's divided by a `participation` estimate set per event; with a third of the crowd online, 4 phones in a small area read as 12 people. It's the weakest number in the system: too high and real crushes look half as dense, too low and comfortable groups raise alarms. The push detector depends less on it, because it only needs a few neighbouring phones in a chain.

**12. How did you validate the simulator?**
Against Weidmann's fundamental diagram (1993), the speed-density benchmark that Vadere and JuPedSim are checked against. Mean walking speed is within ±0.10 m/s of Weidmann from 1 to 5 people/m², except at 2.5/m² (−0.14), and we only checked one-way corridor flow. That makes the crowd plausible, not proven: it's a test bench, not evidence about real crushes.

**13. A handful of phones at a hackathon proves what?**
That the method works end to end on real phones, real networks and real hardware. It does not prove the thresholds for 50,000 people; those need recordings from real events, which is why every run can be recorded, labelled and replayed through the tests.

**14. What happens when the detector is wrong?**
A false alarm costs a steward a look: each alert is an incident card that staff acknowledge or resolve, and the evidence panel shows why it fired. Nothing acts on the crowd automatically except a "move this way" arrow toward more space. A miss is the worse failure, so Pulse is one more signal for an existing safety team, never a replacement for stewards and cameras.

## Scale, privacy and business

**15. Does it scale to a stadium?**
Each phone is compared only with up to 6 nearest neighbours within 1.1 m, so the pair count grows linearly with the crowd, not with its square. Each phone sends 10 messages a second; `[LOADTEST: one Go server handled N phones at M msg/s with detector step X ms]`. Beyond one server, the venue splits naturally by zone.

**16. What about privacy and GDPR?**
Attendees opt in; the phone sends a random session ID, its position in venue metres and motion numbers, nothing else: no names, contacts, audio, photos or location history. GPS is converted to venue metres on arrival, and latitude and longitude are never stored, logged or sent to the dashboard. Under GDPR it would still be personal data while a session is live, so a deployment needs a stated purpose (safety), a retention limit on recordings and a consent screen in the ticket app.

**17. Who pays, and how?**
Venue safety teams, promoters and ticketing platforms, priced per event or per attendee. The pitch is no hardware to install: attendees opt in through the ticket app, and the safety team watches one screen. The signs and zone lights are optional extras for places where staff can't watch a screen.

**18. Who else does this?**
Camera analytics count heads and estimate density from CCTV (WaitTime, and the crowd modules in mainstream video-analytics suites); Wi-Fi and Bluetooth analytics count devices in a zone (Cisco Spaces, Crowd Connected for event apps); 3D sensors count people at entrances (Xovis). Academic work used phones too: ETH Zurich ran a crowd-density app at the London Lord Mayor's Show (Wirz et al., 2013). Pulse's difference is measuring how pressure travels between neighbouring people, not just how many are there, and talking back to each attendee's phone; we'd want to combine with cameras, not replace them.

**19. Why would an attendee keep the page open and drain their battery?**
Today they wouldn't for a whole night, which is why it belongs inside the ticket app, where it can run only in high-risk moments (doors, headliner, exits). The page uses motion sensors and a 10 Hz upload, which is light; the screen staying on is the real cost. The arrow on their own phone is the reason to opt in: it's safety information for them, not just for the venue.

**20. What does the hardware add if phones do the sensing?**
Staff and attendees can't all watch a screen, so the Arduino UNO R4 sign flashes STOP in the worst zone and the ESP32 zone lights show each zone's level. The ESP32s also count nearby Bluetooth devices as a second, camera-free crowd estimate (counts only, no addresses), and hear each other to estimate board-to-board distance. Everything still works without them.

## If you don't know

Say "I don't know; here's how I'd find out" and name the test. Don't guess a number.
