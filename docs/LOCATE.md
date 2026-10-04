# Where is each phone? The position estimator

Everyone joins through one shared QR code and taps Join. Nobody taps a map and there are no per-location codes; afterwards people walk where they like. `server/internal/locate` works out where each phone is from everything that needs nothing from the user, and `server/internal/app/locate.go` puts its answer into the pipeline (zones, clusters, density, neighbours, guidance, the phones' own maps).

It is on by default. `PULSE_LOCATE=0` or `PUT /api/locate {"on": false}` turns it off; the old paths are untouched underneath.

**The short version.** In the simulator, with realistic phone GPS, the estimator takes the median position error from 4.5 m to 3.6 m and brings packing detection back from 0 of 60 surge runs to 58 of 60, with no false alarm in 60 look-alike runs or in 560 scripted ones. Almost all of the detection gain comes from one thing: phones that are jostled together are found by their shared motion and pulled together on the map. The position error itself improves only by about a fifth. Indoors with no GPS at all, walking in from the QR code, positions are good to about 5 m, which says which part of the room a phone is in and not who its neighbours are; 51 of 60 surges are still caught, about 6 s after the simulated danger begins rather than before it. Nothing here has been checked on a real phone.

## What it does

Per phone, a Kalman filter holds the position and the phone's GPS bias (`kalman.go`). On top of the filters, once per detector step, a cooperative step adjusts the positions together (`coop.go`). It never feeds back into the filters.

| Source | What the estimator does with it |
|---|---|
| **Entry spot** | Staff set where the QR code hangs (`PUT /api/locate`). A phone whose hello carries no position starts there, ± 1.5 m by default, on a spot of its own drawn from its id so the picture at the door is a queue and not a stack. Without an entry spot such a phone stays unplaced, as before. |
| **GPS** | Fixes arrive in venue metres with their reported accuracy. A phone's GPS error is mostly a slow bias, so the bias is a state of the filter (correlation time 60 s, size from the reported accuracy). A fix far from where the filter expects it is taken as the bias jumping, not the person. A bias beyond 4 σ of the receiver's accuracy is not believed: the excess moves the position, so someone who walked off unseen is followed in the end. |
| **Standing still** | No steps seen: the position is held (0.03 m/√s, at most 0.3 m in all) and everything the fixes do is put down to the bias. |
| **Steps** (`pdr.go`) | From the 100 ms summaries: the step rhythm in the levelled vertical acceleration, step length from the bounce (inverted pendulum), direction from the compass heading plus how the phone sits on the body, which comes from the gait itself (the forward acceleration leads the vertical one by a quarter of a step). Needs `hd`/`hb` on the motion message and the map's bearing. |
| **Phone's own step count** (`dr`) | If the phone sends its own dead reckoning, that is used instead of the server's. |
| **Exact placements** | A tap on the phone's map, staff dragging the dot, the demo spot's row, a tower check-in: the filter restarts at that point. A check-in therefore calibrates the GPS bias (the next fix measures it there). |
| **Any other fix** | `Fix` takes a position with an error ellipse: a Bluetooth beacon fix that is good along the line between two boards and poor across it, or a round one. Wired to `App`'s beacon fixes. |
| **Motion neighbours** (`near.go`) | Phones whose band-passed horizontal motion over the last 4 s is the same motion (rotation-free correlation ≥ 0.7, confirmed three times over a few seconds) are linked. Phones swaying to a rhythm are not compared, so a hall dancing to one beat links nobody. Mesh reports (`near`) put pairs forward for the same test. |
| **Cooperative step** | Linked phones are pulled to within 0.9 m of each other, each moving in proportion to its own variance. No two estimates end closer than about 0.4 m. Nobody ends in a wall, on the stage or outside the venue. A phone placed exactly is never moved. |
| **Walls** (`geom.go`) | A dead-reckoned step that would cross a wall slides along it. A fix off the edge of the map is someone at the edge, kept and counted. |

Each position comes with an uncertainty (1 σ), the sources that went into it and a `lost` flag when it is vaguer than 8 m. The pipeline is told the uncertainty as the 68 % radius a GPS reports, or 0 when it is under 0.5 m (as good as placed by hand). A lost phone stays in the wave detector, which finds neighbours by motion wherever they are, and is left out of the density clusters.

Honesty rules that turned out to matter:

- **Nobody stays at the door.** A phone that started at the entry and has not been seen to walk 3 m within 12 s left without being followed (a bag, a locked screen). Its uncertainty then grows by 1 m/s until something places it. Without this rule about twenty phones sat on the entry spot in every run and read as a crush.
- **A silent phone** that was walking, or had only just come in, may be anywhere 0.4 m/s could take it. One that had settled is assumed to be still there.
- **A step rhythm without a direction** moves nothing and, by default, widens nothing. A crowd jumping while it is pushed looks the same as someone walking with the phone in a bag, and widening for it lost every phone in the `wave-jump` scenario.

## Measurements

All from the simulator (`internal/crowdsim`, 250 people, 60 % with a phone, 24 × 16 m), which knows where everyone stands. Parameters were chosen on seeds 101–110; every number below is from seeds 201–220, which were not looked at before. Run on 2026-10-03 against the detector and density tracker as they were that evening.

    LOCATE_EVAL=sim      LOCATE_SEEDS=201-220 go test ./server/internal/locate -run EvalSim -v
    LOCATE_EVAL=scripted LOCATE_SEEDS=201-220 go test ./server/internal/locate -run EvalScripted -v

"Raw" is what the pipeline uses with the estimator off: GPS smoothed and clamped, or the hand placement. Conditions with GPS, carry and dropouts also have the compass heading on (`Realism.Heading`), with an indoor compass's error.

### Position, neighbours, density (crowd simulation, 6 scripts × 20 seeds per condition)

| Phones | | Error mean / median / p95 (m) | Counted | True ≤ 1.1 m pairs found | Estimated pairs that are wrong | Density bias / abs. error (/m²) |
|---|---|---:|---:|---:|---:|---:|
| ideal (exact positions) | raw | 0.00 / 0.00 / 0.01 | 100 % | 100 % | 0 % | +0.40 / 0.44 |
| | estimator | 0.00 / 0.00 / 0.01 | 100 % | 100 % | 0 % | +0.40 / 0.44 |
| realistic (GPS 5 m, carry, dropouts) | raw | 5.24 / 4.49 / 11.92 | 87 % | 2 % | 95 % | −2.24 / 2.24 |
| | estimator | 4.17 / 3.57 / 9.57 | 86 % | 6 % | 88 % | −0.51 / 1.65 |
| GPS only | raw | 5.26 / 4.46 / 11.99 | 93 % | 2 % | 95 % | −2.12 / 2.12 |
| | estimator | 3.95 / 3.35 / 9.10 | 90 % | 10 % | 86 % | +0.51 / 2.10 |
| GPS × 0.5 only (2.5 m) | raw | 3.04 / 2.47 / 7.41 | 99 % | 6 % | 89 % | −1.01 / 1.02 |
| | estimator | 2.47 / 2.04 / 5.86 | 99 % | 16 % | 84 % | +1.25 / 1.74 |
| realistic, everyone walks in from the entry | raw | 5.69 / 4.97 / 12.60 | 82 % | 1 % | 95 % | −2.09 / 2.09 |
| | estimator | 4.28 / 3.54 / 10.59 | 84 % | 6 % | 89 % | −0.19 / 1.28 |
| indoor: no GPS, everyone walks in | raw | no position at all | 0 % | 0 % | — | −3.83 / 3.83 |
| | estimator | 5.82 / 4.84 / 14.21 | 65 % | 4 % | 89 % | −1.35 / 1.92 |
| indoor, phone counts its own steps (a model, see below) | estimator | 4.53 / 3.72 / 11.45 | 73 % | 5 % | 89 % | −0.72 / 1.55 |

"Counted" is the share of phones that count toward density (not lost, stale or outside). The error is over the counted phones. During the walk-in itself (first 190 s) the indoor error is 4.98 / 4.05 / 12.39 m.

The estimator's own uncertainty is roughly honest with GPS (46–54 % of phones within 1 σ, 86–90 % within 2 σ; a round Gaussian gives 39 % and 86 %) and too confident after a walk-in (34 % and 72 %).

Neighbours by map distance stay poor in every condition: at best 16 % of true neighbour pairs, and most estimated pairs are wrong. The links found by motion are better evidence than the map (54–58 % of linked pairs are truly within 1.5 m), which is why they are used to move the map and not the other way round.

Over time (a calm crowd standing for ten minutes, `LOCATE_EVAL=long`, 5 seeds, mean / median error in m):

| | first minute | minute 3 | minute 5 | minute 10 |
|---|---:|---:|---:|---:|
| GPS only, raw | 5.06 / 4.29 | 5.55 / 4.63 | 5.51 / 4.64 | 5.72 / 4.93 |
| GPS only, estimator | 4.25 / 3.68 | 3.87 / 3.23 | 3.67 / 3.05 | 3.76 / 3.05 |
| GPS only, estimator without standing still | 4.25 / 3.67 | 4.15 / 3.58 | 4.04 / 3.49 | 4.21 / 3.61 |
| realistic, raw | 4.84 / 4.17 | 5.51 / 4.74 | 5.45 / 4.72 | 5.61 / 4.71 |
| realistic, estimator | 4.11 / 3.51 | 3.97 / 3.35 | 4.01 / 3.39 | 4.20 / 3.52 |

Holding still lets the fixes average down to about 3 m in four minutes and no further: a phone's GPS bias wanders about as fast as it can be averaged. With dropouts (realistic) phones come and go and the gain stays near 1.2 m. No false red in any of these runs.

### Detection (same runs; 60 surge runs and 60 look-alike runs per condition)

| Phones | | Surges caught (red) | Red before the truth turned dangerous | Median lead (s) | False red on calm / dance / attract |
|---|---|---:|---:|---:|---:|
| ideal | raw = estimator | 60 / 60 | 38 / 59 | +18.5 | 0 / 60 |
| realistic | raw | 0 / 60 | 0 / 59 | — | 0 / 60 |
| | estimator | 58 / 60 | 2 / 59 | −5.5 | 0 / 60 |
| GPS only | raw | 0 / 60 | 0 / 59 | — | 0 / 60 |
| | estimator | 59 / 60 | 6 / 59 | −5.2 | 0 / 60 |
| GPS × 0.5 only | raw | 29 / 60 | 6 / 59 | −21.7 | 0 / 60 |
| | estimator | 60 / 60 | 33 / 59 | +18.8 | 0 / 60 |
| realistic, walk in | raw | 0 / 60 | 0 / 59 | — | 0 / 60 |
| | estimator | 58 / 60 | 2 / 59 | −5.4 | 0 / 60 |
| indoor, walk in | raw | 0 / 60 | 0 / 59 | — | 0 / 60 |
| | estimator | 51 / 60 | 0 / 59 | −6.2 | 0 / 60 |
| indoor, phone counts steps (model) | estimator | 55 / 60 | 2 / 59 | −5.9 | 0 / 60 |

A negative lead means the alarm came after the simulated danger began. With realistic GPS the estimator turns "never" into "about 5 s late". Only at 2.5 m GPS error does it warn ahead again.

Scripted scenarios (`internal/sim`, line and crowd layouts, 20 seeds, estimator against raw):

| Phones | False alarms | Pushes caught | `gather` caught | Calm runs at yellow |
|---|---:|---:|---:|---:|
| ideal | 0 / 560 → 0 / 560 | 60 / 80 → 60 / 80 | 20 / 20 → 20 / 20 | 101 → 101 of 440 |
| realistic | 0 / 560 → 0 / 560 | 80 / 80 → 80 / 80 | 0 / 20 → 0 / 20 | 0 → 11 |
| GPS only | 0 / 560 → 0 / 560 | 80 / 80 → 80 / 80 | 0 / 20 → 0 / 20 | 1 → 15 |
| GPS × 0.5 only | 0 / 560 → 0 / 560 | 80 / 80 → 80 / 80 | 0 / 20 → 0 / 20 | 1 → 18 |

Pushes are already caught without positions (the detector finds neighbours by motion). `gather` stays missed: its people walk into a tight group without touching, so there is no shared motion to link them, and GPS alone cannot show a 2 m group. The estimator costs a few more yellow warnings on calm runs (links pull some phones together) and no red.

### What each part is worth (leave one out; seeds 201–210, 30 surge and 30 look-alike runs)

Median position error (m) and density bias (/m²), then surges caught:

| | realistic | GPS only | realistic, walk in | indoor, walk in |
|---|---|---|---|---|
| everything on | 3.55, −0.56, 28 / 30 | 3.28, +0.42, 29 / 30 | 3.50, −0.27, 29 / 30 | 4.91, −1.41, 25 / 30 |
| without map constraints | 3.68, −0.77, 28 | 3.40, +0.09, 28 | 3.62, −0.51, 28 | 5.03, −1.51, 24 |
| without spacing | 3.59, −0.46, 29 | 3.31, +0.54, 29 | 3.54, −0.26, 28 | 4.95, −1.45, 25 |
| without standing still | 3.60, −0.58, 29 | 3.39, +0.39, 28 | 3.64, −0.52, 29 | 4.88, −2.64, 15 (31 % counted) |
| without steps | 3.83, −0.82, 29 | 3.68, +0.09, 30 | 4.51, −2.70, 10 (6 false red) | 13.65, −3.60, 2 (6 false red) |
| without motion links | 3.74, −1.95, 0 | 3.54, −1.74, 0 | 3.65, −1.51, 0 | 4.94, −2.57, 0 |
| filter alone | 3.89, −2.18, 0 | 3.72, −2.07, 0 | 3.92, −1.91, 0 | 5.03, −3.34, 0 |

- **Map constraints**: about 0.1 m off the error and 0.1–0.3 /m² off the density bias, and the uncertainty becomes honest (37 % → 46 % of phones within 1 σ in the realistic condition). Their real value is that nobody is dropped as `outside`.
- **Spacing**: no measurable effect on the position error; density error 0.1–0.2 /m² lower. It is there to stop stacks, and the entry spread does most of that.
- **Standing still**: 0.05–0.15 m with GPS in 80 s runs, 0.5 m over ten minutes (table above). Indoors it is what keeps a phone located at all (65 % counted against 31 %).
- **Steps**: 0.3 m with GPS when people are already in place; essential when they walk in (without them everyone stays at the door).
- **Motion links**: nothing for the position error, everything for packing detection.

Tried and left off: feeding the cooperative positions back into the filters (`CoopFeed`). It took another 0.2–0.3 m off the error and over-read density by 1.5 /m², with 2 false reds in 12 look-alike runs. Using the detector's wave edges as links: only 18 % of them join phones within 1.5 m (they join people riding the same wave).

### Cost

`BenchmarkStep1000`: one estimator step for 1000 phones, all streaming, all with GPS, two thirds being jostled: 11.8 ms on one core of a Ryzen 9 9950X (about 3500 pairs compared, 4100 links held), against 27–90 ms for the detector's own step on the same machine. It runs inside the pipeline step, so `stats.detectMs` includes it; `GET /api/locate` reports it alone (`stepMs`).

## Dead reckoning: how good, and what the phone should send

Followed alone from a known start through a walk of about 19 m into the simulated crowd (`LOCATE_EVAL=split`, `LOCATE_EVAL=pdr`):

- distance: within 2 m at the end (median), once slow steps are counted;
- direction with a perfect compass: 3 m at the end; with an indoor compass (fixed offset σ 8°, slow disturbance σ 10–30°): 4–5 m. That is about a quarter of the distance walked;
- by carry, with the realistic compass and dropouts: chest 3.4 m, trouser pocket 3.7 m, hand 5.2 m, bag: not followed at all (its swing lags the body, so the gait gives no direction; 15 % of phones);
- two fifths of the way in is walked at under 0.6 m/s, edging through people. Below about 0.3 m/s the bounce is too small to count.

So from 10 Hz summaries, in a crowd, the server's dead reckoning is no better than GPS. It is still what makes the entry spot usable.

**Would the phone do better?** `crowdsim/dr.go` is a labelled model of a phone that counts steps at full sensor rate with its gyroscope (steps counted down to 0.25 m/s, length off by 8 % per person, heading = compass offset + half the slow disturbance + 8° carry error). With it the indoor median goes from 4.84 m to 3.72 m and far fewer phones are lost (73 % counted against 65 %). The compass still limits it. The server side is in place:

    { "type": "dr", "steps": 412, "e": -3.2, "n": 18.6 }   // running totals since the page loaded: steps, metres east, metres north

The phone page would count steps on the heel strike at the full sensor rate, hold the heading with the gyroscope between compass readings, and send the totals once a second. Steps without displacement mean "walking, direction unknown".

## What to promise

- **Tap-your-spot, demo spot, tower check-in**: exact, unchanged.
- **Outdoors, one QR code, GPS**: about 3.5 m typical, 9–10 m for one phone in twenty. Enough for which zone, not for who stands next to whom.
- **Indoors, one QR code, no GPS**: about 5 m typical after walking in, a third of the phones lost (bags, locked screens). Enough for which part of the room.
- **Detection** does not wait for good positions. Pushes are caught from motion alone. Packing is caught when people are pressed together and move together, a few seconds after it becomes dangerous in the simulator, not before. A group that packs without touching is not caught with GPS-grade positions.

## Not done

- **Bluetooth device counts from the boards** (`/pulse` → `ble`) as a constraint on where the crowd mass is: not implemented. The simulator has no model of them, so there was nothing to measure it against, and the error shared by a whole group is small here (1 m of 5 m).
- **The phones' own mesh estimate** (`mpos`, `App.MeshPositions()`): not used. It starts from the position the server sent the phone, so feeding it back would count the same information twice. `Fix` with `SrcMesh` is the hook.
- **Mesh `near` reports** are wired for live phones (`Estimator.Near`) but untested against real ones. For simulated phones the app's stand-in evidence is the detector's own correlation, so it is not fed.
- **Recordings** store the first position of an estimated phone and its GPS path as before, not the estimate over time; a replay shows the recorded positions, with no estimator.
- Four existing tests pin the GPS path the estimator replaces (EMA smoothing, the `outside` flag, the tower's fading correction, beacon-over-GPS precedence). They now run with the estimator off; `app/locate_test.go` covers the same ground with it on.

## What the dashboard should draw

- An uncertainty halo per node, radius `acc` (68 % radius in metres); none when `acc` is 0.
- A raw / estimated toggle: when a node has `raw`, a ghost dot there and a thin line to the estimate.
- `lost` nodes faint or hollow, with a count ("12 phones not located").
- In the node panel: the sources (`loc`: entry, gps, steps, fix, beacon, mesh, near, map). `src: "est"` should read "estimated", not "GPS ± x m" as it does today.
- In sim mode, live: `snapshot.sim.loc` → "position error 3.4 m (raw 5.0 m), 9 lost" (mean, median, p95, rawMean, lost).
- The entry spot as a draggable marker with its radius (`GET/PUT /api/locate`), the estimator's on/off switch, and for a venue without a GPS anchor a control for the map's bearing.
- From `GET /api/locate`: phones, located, walking, links, stepMs.

## To check on real phones

1. **Sign conventions.** The estimator assumes a right-handed device frame, `g` pointing down and acceleration being the true acceleration. iOS and Android report opposite signs; a flipped `g` turns every walking direction round by 180°.
2. **Heading.** What `webkitCompassHeading` (iOS) and absolute `alpha` (Android) give for a phone upright in a pocket. The page sends `hd` (top edge) today; upright phones need `hb` (the back of the phone), or their heading is ignored and they are not dead-reckoned.
3. **Magnetic against true north**: the map's bearing must be set with the same kind of compass the phones use.
4. **Step detection thresholds** (0.15 m/s² at the step frequency) and the step-length constant against recorded walks, per carry.
5. **The quarter-step lead** of forward over vertical acceleration on real gait, which is what gives the direction for a pocketed phone.
6. **Motion neighbours**: the 0.7 correlation and the rhythm test, on people really standing shoulder to shoulder, and on a real dancing crowd.
7. **GPS**: the 60 s bias time constant and how honest the reported accuracy is.
