# Pulse evaluation

Generated 2026-10-04T16:36:51Z by `go run ./server/cmd/eval -seeds 20` (`make eval`) in 4m43s on 32 CPU threads. Machine-readable copy: [`eval.json`](eval.json) (served at `GET /api/eval`).

**False alarms: 0 of 600 look-alike runs went red. Missed: 22 of 160 true-positive runs never went red.**

The table below is with **ideal phones** (upright on the chest, exact position, nothing lost): the conditions the detector was tuned in. [Messy phones](#messy-phones) repeats everything with phones as they really are.

| Scenario | Layout | Expect | Runs | Red | Yellow | Calm | Median red (s) | Median lead (s) | Note |
|---|---|---|---:|---:|---:|---:|---:|---:|---|
| `calm` | line | calm | 20 | 0 | 0 | 20 | — | — |  |
| `calm` | crowd | calm | 20 | 0 | 9 | 11 | — | — | density yellow in 9 |
| `walk` | line | calm | 20 | 0 | 0 | 20 | — | — |  |
| `walk` | crowd | calm | 20 | 0 | 9 | 11 | — | — | density yellow in 9 |
| `dance` | line | calm | 20 | 0 | 0 | 20 | — | — |  |
| `dance` | crowd | calm | 20 | 0 | 9 | 11 | — | — | density yellow in 9 |
| `handle` | line | calm | 20 | 0 | 0 | 20 | — | — |  |
| `handle` | crowd | calm | 20 | 0 | 9 | 11 | — | — | density yellow in 9 |
| `shove` | line | yellow-ok | 20 | 0 | 20 | 0 | — | — |  |
| `shove` | crowd | yellow-ok | 20 | 0 | 13 | 7 | — | — | density yellow in 9 |
| `wave` | line | red | 20 | 20 | 0 | 0 | 24.3 | — |  |
| `wave` | crowd | red | 20 | 17 | 3 | 0 | 34.5 | — | missed 3/20; density yellow in 9 |
| `sway` | line | calm | 20 | 0 | 0 | 20 | — | — |  |
| `sway` | crowd | calm | 20 | 0 | 9 | 11 | — | — | density yellow in 9 |
| `sway-slow` | line | calm | 20 | 0 | 0 | 20 | — | — |  |
| `sway-slow` | crowd | calm | 20 | 0 | 9 | 11 | — | — | density yellow in 9 |
| `mexican` | line | calm | 20 | 0 | 0 | 20 | — | — |  |
| `mexican` | crowd | calm | 20 | 0 | 9 | 11 | — | — | density yellow in 9 |
| `walkpast` | line | yellow-ok | 20 | 0 | 20 | 0 | — | — |  |
| `walkpast` | crowd | yellow-ok | 20 | 0 | 10 | 10 | — | — | density yellow in 9 |
| `procession` | line | yellow-ok | 20 | 0 | 0 | 20 | — | — |  |
| `procession` | crowd | yellow-ok | 20 | 0 | 9 | 11 | — | — | density yellow in 9 |
| `march` | line | calm | 20 | 0 | 0 | 20 | — | — |  |
| `march` | crowd | calm | 20 | 0 | 9 | 11 | — | — | density yellow in 9 |
| `pocket` | line | calm | 20 | 0 | 0 | 20 | — | — |  |
| `pocket` | crowd | calm | 20 | 0 | 9 | 11 | — | — | density yellow in 9 |
| `bump` | line | calm | 20 | 0 | 0 | 20 | — | — |  |
| `bump` | crowd | calm | 20 | 0 | 9 | 11 | — | — | density yellow in 9 |
| `jump-stagger` | line | calm | 20 | 0 | 0 | 20 | — | — |  |
| `jump-stagger` | crowd | calm | 20 | 0 | 9 | 11 | — | — | density yellow in 9 |
| `wave-jump` | line | red | 20 | 20 | 0 | 0 | 50.1 | — |  |
| `wave-jump` | crowd | red | 20 | 1 | 19 | 0 | 66.8 | — | missed 19/20; density yellow in 9 |
| `gather` | crowd | red | 20 | 20 | 0 | 0 | 31.5 | — | red from crowd density, not waves; 40 phones |
| `calm` | sim | calm | 20 | 0 | 19 | 1 | — | — | truth never dangerous in 20/20; median peak 2.2/m², 0 N/m |
| `attract` | sim | yellow-ok | 20 | 0 | 20 | 0 | — | — | truth never dangerous in 20/20; median peak 2.2/m², 0 N/m |
| `calm→surge` | sim | red | 20 | 20 | 0 | 0 | 35.4 | -2.1 | red from crowd density, not waves; median peak 6.0/m², 5364 N/m |
| `stage→surge` | sim | red | 20 | 20 | 0 | 0 | 12.5 | 18.3 | red from crowd density, not waves; median peak 6.0/m², 4650 N/m |
| `stage→surge 0.3` | sim | red | 20 | 20 | 0 | 0 | 12.5 | 19.0 | red from crowd density, not waves; median peak 5.4/m², 2526 N/m |

## Messy phones

The same runs with the phones made as messy as real ones (`server/internal/crowdsim/realism.go`, where the parameters and their sources are): **GPS** error instead of the true position (slowly drifting, median 5 m per phone at strength 1, 10 m at strength 2, with jumps and a reported accuracy), **carry** (25 % chest, 25 % in the hand, 35 % trouser pocket, 15 % bag at strength 1; the phone's axes are no longer the body's; it sends its gravity vector), **dropouts** (screen locks of seconds to minutes, stalls that deliver messages in clumps, lost summaries, clock error, slow sensors). Strength 0 = off, 1 = realistic, 2 = harsh. The "only" rows have one imperfection on alone, at strength 1 unless it says otherwise (GPS ×0.5 = 2.5 m median error, ×0.2 = 1 m). Detector and thresholds are the same in every row. *Packing at least yellow* counts the packing runs that raised a warning (yellow) or an alarm (red).

| Phones | GPS | Carry | Dropouts | False alarms (red) | Pushes caught | Packing caught | Packing at least yellow | Calm runs at yellow | Red only from the default-spot stack |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| ideal | 0 | 0 | 0 | 0 / 600 | 58 / 80 | 80 / 80 | 80 / 80 | 118 / 460 | 0 |
| realistic | 1 | 1 | 1 | 0 / 600 | 80 / 80 | 0 / 80 | 57 / 80 | 4 / 460 | 0 |
| harsh | 2 | 2 | 2 | 0 / 600 | 69 / 80 | 0 / 80 | 9 / 80 | 0 / 460 | 0 |
| gps only | 1 | 0 | 0 | 0 / 600 | 80 / 80 | 0 / 80 | 60 / 80 | 4 / 460 | 0 |
| gps ×0.5 only | 0.5 | 0 | 0 | 0 / 600 | 80 / 80 | 34 / 80 | 60 / 80 | 18 / 460 | 0 |
| gps ×0.2 only | 0.2 | 0 | 0 | 1 / 600 | 80 / 80 | 60 / 80 | 80 / 80 | 31 / 460 | 0 |
| carry only | 0 | 1 | 0 | 0 / 600 | 51 / 80 | 80 / 80 | 80 / 80 | 119 / 460 | 0 |
| dropouts only | 0 | 0 | 1 | 0 / 600 | 58 / 80 | 80 / 80 | 80 / 80 | 130 / 460 | 0 |
| carry: all chest (tilted) | 0 | 1 | 0 | 0 / 600 | 57 / 80 | 80 / 80 | 80 / 80 | 119 / 460 | 0 |
| carry: all in the hand | 0 | 1 | 0 | 0 / 600 | 60 / 80 | 80 / 80 | 80 / 80 | 118 / 460 | 0 |
| carry: all in a pocket | 0 | 1 | 0 | 0 / 600 | 58 / 80 | 80 / 80 | 80 / 80 | 121 / 460 | 0 |
| carry: all in a bag | 0 | 1 | 0 | 0 / 600 | 72 / 80 | 80 / 80 | 80 / 80 | 118 / 460 | 0 |
| carry only, no g sent | 0 | 1 | 0 | 0 / 600 | 33 / 80 | 80 / 80 | 80 / 80 | 119 / 460 | 0 |
| carry: all in a pocket, no g sent | 0 | 1 | 0 | 0 / 600 | 28 / 80 | 80 / 80 | 80 / 80 | 118 / 460 | 0 |

Crowd simulation against its ground truth:

| Phones | Position error (m) | Phones counted | Density bias (/m²) | Density abs. error (/m²) | Red before danger | Median lead, red (s) | Median lead, first yellow (s) | Guidance within 45° | Guidance > 90° off | Arrow shown | … within 45° | … > 90° off |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| ideal | 0.00 | 100 % | +0.39 | 0.39 | 40 / 60 | 17.8 | 28.6 | 78 % | 15 % | 100 % | 78 % | 15 % |
| realistic | 4.48 | 87 % | -2.42 | 2.42 | 0 / 60 | — | 15.1 | 38 % | 38 % | 24 % | 45 % | 33 % |
| harsh | 6.78 | 63 % | -3.56 | 3.56 | 0 / 60 | — | -7.3 | 32 % | 45 % | 2 % | 31 % | 43 % |
| gps only | 4.52 | 93 % | -2.27 | 2.27 | 0 / 60 | — | 17.5 | 38 % | 39 % | 20 % | 50 % | 29 % |
| gps ×0.5 only | 2.46 | 99 % | -1.01 | 1.01 | 8 / 60 | -7.0 | 22.3 | 50 % | 27 % | 64 % | 54 % | 25 % |
| gps ×0.2 only | 1.02 | 100 % | +0.16 | 0.32 | 38 / 60 | 16.6 | 26.4 | 66 % | 16 % | 98 % | 66 % | 15 % |
| carry only | 0.00 | 100 % | +0.39 | 0.39 | 40 / 60 | 17.8 | 28.6 | 78 % | 15 % | 100 % | 78 % | 15 % |
| dropouts only | 0.01 | 94 % | +0.14 | 0.25 | 40 / 60 | 17.5 | 28.5 | 75 % | 17 % | 100 % | 75 % | 17 % |
| carry: all chest (tilted) | 0.00 | 100 % | +0.39 | 0.39 | 40 / 60 | 17.8 | 28.6 | 78 % | 15 % | 100 % | 78 % | 15 % |
| carry: all in the hand | 0.00 | 100 % | +0.39 | 0.39 | 40 / 60 | 17.8 | 28.6 | 78 % | 15 % | 100 % | 78 % | 15 % |
| carry: all in a pocket | 0.00 | 100 % | +0.39 | 0.39 | 40 / 60 | 17.8 | 28.6 | 78 % | 15 % | 100 % | 78 % | 15 % |
| carry: all in a bag | 0.00 | 100 % | +0.39 | 0.39 | 40 / 60 | 17.8 | 28.6 | 78 % | 15 % | 100 % | 78 % | 15 % |
| carry only, no g sent | 0.00 | 100 % | +0.39 | 0.39 | 40 / 60 | 17.8 | 28.6 | 78 % | 15 % | 100 % | 78 % | 15 % |
| carry: all in a pocket, no g sent | 0.00 | 100 % | +0.39 | 0.39 | 40 / 60 | 17.8 | 28.6 | 78 % | 15 % | 100 % | 78 % | 15 % |

Runs that went red, per scenario (**bold** = wrong: a look-alike that went red, or a true positive missed in at least one run; `s` = runs whose only red was the default-spot stack, counted as red on look-alikes and as missed on true positives; `y` = calm runs that reached yellow):

| Scenario | Layout | Expect | ideal | realistic | harsh | gps only | gps ×0.5 only | gps ×0.2 only | carry only | dropouts only |
|---|---|---|---:|---:|---:|---:|---:|---:|---:|---:|
| `calm` | line | calm | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 |
| `calm` | crowd | calm | 0 / 20 (9 y) | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 (1 y) | 0 / 20 (9 y) | 0 / 20 (10 y) |
| `walk` | line | calm | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 |
| `walk` | crowd | calm | 0 / 20 (9 y) | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 (1 y) | 0 / 20 (9 y) | 0 / 20 (10 y) |
| `dance` | line | calm | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 |
| `dance` | crowd | calm | 0 / 20 (9 y) | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 (1 y) | 0 / 20 (9 y) | 0 / 20 (10 y) |
| `handle` | line | calm | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 |
| `handle` | crowd | calm | 0 / 20 (9 y) | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 (1 y) | 0 / 20 (9 y) | 0 / 20 (10 y) |
| `shove` | line | yellow-ok | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 |
| `shove` | crowd | yellow-ok | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 |
| `wave` | line | red | 20 / 20 | 20 / 20 | **18 / 20** | 20 / 20 | 20 / 20 | 20 / 20 | 20 / 20 | 20 / 20 |
| `wave` | crowd | red | **17 / 20** | 20 / 20 | 20 / 20 | 20 / 20 | 20 / 20 | 20 / 20 | **12 / 20** | **17 / 20** |
| `sway` | line | calm | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 |
| `sway` | crowd | calm | 0 / 20 (9 y) | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 (1 y) | 0 / 20 (9 y) | 0 / 20 (10 y) |
| `sway-slow` | line | calm | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 (1 y) | 0 / 20 (1 y) |
| `sway-slow` | crowd | calm | 0 / 20 (9 y) | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 (1 y) | 0 / 20 (9 y) | 0 / 20 (10 y) |
| `mexican` | line | calm | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 |
| `mexican` | crowd | calm | 0 / 20 (9 y) | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 (1 y) | 0 / 20 (9 y) | 0 / 20 (10 y) |
| `walkpast` | line | yellow-ok | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 |
| `walkpast` | crowd | yellow-ok | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 |
| `procession` | line | yellow-ok | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 |
| `procession` | crowd | yellow-ok | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 |
| `march` | line | calm | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 |
| `march` | crowd | calm | 0 / 20 (9 y) | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 (1 y) | 0 / 20 (9 y) | 0 / 20 (10 y) |
| `pocket` | line | calm | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 |
| `pocket` | crowd | calm | 0 / 20 (9 y) | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 (1 y) | 0 / 20 (9 y) | 0 / 20 (10 y) |
| `bump` | line | calm | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 |
| `bump` | crowd | calm | 0 / 20 (9 y) | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 (1 y) | 0 / 20 (9 y) | 0 / 20 (10 y) |
| `jump-stagger` | line | calm | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 |
| `jump-stagger` | crowd | calm | 0 / 20 (9 y) | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 (1 y) | 0 / 20 (9 y) | 0 / 20 (10 y) |
| `wave-jump` | line | red | 20 / 20 | 20 / 20 | **11 / 20** | 20 / 20 | 20 / 20 | 20 / 20 | **19 / 20** | 20 / 20 |
| `wave-jump` | crowd | red | **1 / 20** | 20 / 20 | 20 / 20 | 20 / 20 | 20 / 20 | 20 / 20 | **0 / 20** | **1 / 20** |
| `gather` | crowd | red | 20 / 20 | **0 / 20** | **0 / 20** | **0 / 20** | **0 / 20** | **0 / 20** | 20 / 20 | 20 / 20 |
| `calm` | sim | calm | 0 / 20 (19 y) | 0 / 20 (4 y) | 0 / 20 | 0 / 20 (4 y) | 0 / 20 (18 y) | 0 / 20 (20 y) | 0 / 20 (19 y) | 0 / 20 (19 y) |
| `attract` | sim | yellow-ok | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | 0 / 20 | **1 / 20** | 0 / 20 | 0 / 20 |
| `calm→surge` | sim | red | 20 / 20 | **0 / 20** | **0 / 20** | **0 / 20** | **18 / 20** | 20 / 20 | 20 / 20 | 20 / 20 |
| `stage→surge` | sim | red | 20 / 20 | **0 / 20** | **0 / 20** | **0 / 20** | **8 / 20** | 20 / 20 | 20 / 20 | 20 / 20 |
| `stage→surge 0.3` | sim | red | 20 / 20 | **0 / 20** | **0 / 20** | **0 / 20** | **8 / 20** | 20 / 20 | 20 / 20 | 20 / 20 |

<!-- findings:start -->
### What changed, and what still breaks

Written by hand from two runs of 2026-10-04 (20 seeds each, seeds 1–20): **before** is the detector that paired phones by map distance only; **after** is the tables above. This section is kept when the report is regenerated, so check it against the tables if they have changed.

| Phones | | False alarms (red) | Pushes caught | Packing caught | Packing at least yellow | Phones counted | Density bias (/m²) | Guidance within 45° / > 90° off |
|---|---|---:|---:|---:|---:|---:|---:|---:|
| ideal | before | 0 / 600 | 58 / 80 | 80 / 80 | 80 / 80 | 100 % | +0.33 | 79 % / 14 % |
| | after | 0 / 600 | 58 / 80 | 80 / 80 | 80 / 80 | 100 % | +0.33 | 79 % / 14 % |
| realistic | before | 3 / 600 | 0 / 80 | 0 / 80 | not recorded | 62 % | −2.25 | 34 % / 42 % |
| | after | 0 / 600 | 80 / 80 | 0 / 80 | 59 / 80 | 87 % | −2.37 | 38 % / 38 % |
| harsh | before | 54 / 600 | 0 / 80 | 38 / 80 (see point 4 below) | not recorded | 42 % | +14.70 | 27 % / 49 % |
| | after | 0 / 600 | 69 / 80 | 0 / 80 | 7 / 80 | 63 % | −3.58 | 32 % / 45 % |
| gps only | before | 6 / 600 | 0 / 80 | 0 / 80 | not recorded | 67 % | −2.11 | 34 % / 42 % |
| | after | 0 / 600 | 80 / 80 | 0 / 80 | 60 / 80 | 93 % | −2.25 | 37 % / 39 % |
| gps ×0.5 only | before | 0 / 600 | 0 / 80 | 36 / 80 | not recorded | 88 % | −1.02 | 45 % / 32 % |
| | after | 0 / 600 | 80 / 80 | 35 / 80 | 60 / 80 | 98 % | −1.01 | 49 % / 27 % |
| gps ×0.2 only | before | 2 / 600 | 6 / 80 | 60 / 80 | not recorded | 98 % | +0.17 | 58 % / 23 % |
| | after | 1 / 600 | 80 / 80 | 60 / 80 | 80 / 80 | 100 % | +0.13 | 66 % / 16 % |
| carry only | before | 0 / 600 | 45 / 80 | 80 / 80 | 80 / 80 | 100 % | +0.33 | 79 % / 14 % |
| | after | 0 / 600 | 51 / 80 | 80 / 80 | 80 / 80 | 100 % | +0.33 | 79 % / 14 % |
| carry: all in the hand | before | 0 / 600 | 31 / 80 | 80 / 80 | 80 / 80 | 100 % | +0.33 | 79 % / 14 % |
| | after | 0 / 600 | 60 / 80 | 80 / 80 | 80 / 80 | 100 % | +0.33 | 79 % / 14 % |
| dropouts only | before | 0 / 600 | 56 / 80 | 80 / 80 | 80 / 80 | 94 % | +0.09 | 77 % / 16 % |
| | after | 0 / 600 | 58 / 80 | 80 / 80 | 80 / 80 | 94 % | +0.09 | 77 % / 16 % |

The ideal rows are the same run for run (same reds, yellows, red times and lead times). How the seeds were used: the changes were developed on seeds 101–110 and unit-tested on 31–34. The first run on seeds 1–20 then showed one false alarm (a slow sway on the line, GPS only) and a scripted run on 201–220 showed another (a procession in the crowd, GPS only); each led to one change (the resolved-lag test and the pair-share score below), so seeds 1–20 are not untouched. A run on seeds 301–320, not looked at before, agrees with the tables: realistic 0 / 600 false alarms, 80 / 80 pushes, 0 / 80 packing (57 / 80 at least yellow); harsh 0 / 600, 65 / 80, 0 / 80; GPS only 0 / 600, 80 / 80, 0 / 80; GPS ×0.5 0 / 600, 80 / 80, 34 / 80; GPS ×0.2 1 / 600, 80 / 80, 60 / 80; ideal 0 / 600, 62 / 80, 80 / 80.

#### What now works

1. **Pushes are found without knowing where anyone stands.** A phone placed by GPS reports its accuracy radius, and the detector no longer believes its dot: everyone within reach of the two radii is a *candidate* neighbour, and the motion decides (`detect/motion.go`). A pair is a wave hop under the same tests as before (strong correlation at a 120–1200 ms lag, one clear peak, not vertical); three phones make a chain when their lags add up (a→b plus b→c equals a→c), which replaces "travelling the same way on the map"; the zone score is the share of pairs of the zone's phones that are both on such a wave. Realistic GPS: 80 of 80 pushes (0 before); harsh (10 m median error, most phones in pockets, hands and bags): 69 of 80; no false alarm in 600 look-alike runs in either.
2. **It beats the map on the hardest push.** `wave-jump` in the crowd layout (a push through a jumping crowd) is caught 1 time in 20 with exact positions and 20 in 20 with rough ones: a phone found by motion is compared with everyone its push could reach, not only with the six nearest dots. The exact-position detector was deliberately left as it was (its tests and recordings pin its outcomes); giving it the same evidence is the obvious next step (19 of the 22 ideal misses are this row).
3. **The phone in the hand.** A gesture (a burst of rotation under 1.5 s) now masks its own readings plus 0.3 s and the filters hold their state, instead of 1 s of quiet and a restart; a pair that can't be measured (a phone being handled, too few readings to correlate) no longer counts as evidence *against* a wave; and a lag needs 70 % of a gap-free pair's readings rather than all of them. All-hand: 60 of 80 pushes (31 before, 58 ideal). The carry mix: 51 (45 before).
4. **The false alarms from GPS are gone.** Before: 3 (realistic), 6 (GPS only) and 54 (harsh) of 600, from phones without a usable fix stacked on the default spot; with that stack harsh read 14.7 /m² too dense, and its 38 "caught" packing events came late (a median 5.7 s after the danger) out of that estimate. Now a GPS phone without a usable fix is nowhere and counts toward nothing (the server's `unplaced`), a fix just off the venue is mirrored back in instead of being dropped or stacked on the edge (phones counted: 62 % → 87 %), and density around a phone known to ± acc is counted over a disc at least half that wide, so twenty phones that all claim one spot to ± 10 m read as 0.25 /m², not 20.

#### What still breaks, worst first

1. **Packing cannot be seen through 5 m of position error, and nothing here pretends otherwise.** Realistic GPS: 0 of 80 packing events go red (0 before). 59 of 80 raise a yellow warning, a median 12.7 s before the simulated crowd turns dangerous, against 4 of 460 calm runs at yellow; `gather` (40 phones packing to 10/m²) raises nothing at all. The reason is not the estimator. A patch packed at 6/m², 3 m deep, blurred by a 4 m error reads about 2/m² at any scale the positions support, and so does a comfortable crowd at 2/m²: the estimate is a lower bound (each cluster now carries its accuracy radius so the dashboard can say so), and no threshold on it separates the two. Subtracting the known error variance from the spread of the dots was worked through and not built: with 150 phones the depth of a 3 m strip comes out as 0.75 ± 2 m² at 5 m error, and the correction divides by it. At 2.5 m error: 35 of 80 red, a median 7.8 s *after* the danger, as before. At 1 m: as ideal (60 of 80, 17.2 s lead), also as before.
2. **There is no position-free crush signal in a quiet crush.** Looked for, against the simulator's pressure: people at ≥ 1600 N/m read 0.06–0.10 m/s² of horizontal motion, the same as people standing in a calm crowd (0.08) and less than the looser crowd around them (0.2–0.6): they are pinned. A surge with shoving puts 30–45 % of the phones on waves found by motion (a zone score of 0.1–0.2, below yellow); the surge without shoves (`stage→surge 0.3`, 2000 N/m) shows nothing on the accelerometers. "Density × velocity variance" (Helbing's crowd pressure) therefore has no second factor to offer here, and making red out of "density yellow and waves yellow" was rejected: a dense crowd swaying to a slow song would trip it. Whether really crushed people are that still is a property of this simulator's phone model (the body's acceleration under contact forces) that only recordings can settle.
3. **Guidance is barely better than a coin at 5 m.** Within 45° of the arrow computed from everyone's true position: 38 % (34 before); more than 90° off: 38 % (42 before); a random arrow scores 25 % and 50 %. Three ways to pick the direction were measured (the gradient of the density smoothed by the position errors, the least dense way out at long range, the nearest exit) and all land at 35–39 % / 38–40 % ("straight back from the stage" is worse: 22 %): which way is right depends on which side of the crowd you are on, and that is what a 5 m error loses. So each arrow now carries a confidence (1 for a hand-placed phone, 0.5 at ± 4 m, 0.28 at ± 6.4 m) and below 0.5 the phone should show words instead of an arrow: with realistic GPS 24 % of the arrows are shown (45 % within 45°, 33 % the wrong way). At 2.5 m: 65 % shown, 54 % / 24 %; at 1 m: 98 %, 66 % / 16 % (58 % / 23 % before).
4. **Harsh is still harsh.** 69 of 80 pushes (`wave-jump` on the line: 11 of 20), no packing, and its few yellows come a median 7 s after the danger. 37 % of its phones are not counted at any moment.
5. **The carry mix costs pushes with exact positions.** 51 of 80 (58 ideal), though no single way of carrying does worse than 57: probably because different carries delay the push differently (a bag's padding by about 0.2 s), which between two neighbours is as much as the lag being measured. Found by motion the same mix scores 80 of 80 (the realistic row): every phone has many partners.
6. **Levelling with the gravity vector is still worth a lot.** Carried phones without `g`: 33 of 80 pushes for the mix (51 with), 28 for all-pocket (58 with).
7. **Dropouts are the least of it.** 58 of 80 pushes (58 ideal), packing 80 of 80, lead 17.7 s; about 6 % of phones missing at any moment.
8. **What position-free detection gives up.** A slow sway handed from row to row (`sway-slow`, 0.2 Hz) correlates almost as well half a second either side of its best lag, and without a map nothing else says it travels: pairs found by motion must have a lag resolved to 300 ms, so a real disturbance slower than about 0.3 Hz would be missed by them. And red needs about four phones in five of a zone on the wave: a push through one corner of a large zone is diluted (as it is with exact positions).

What a venue would have to add to see packing: positions good to about 1 m (tap-your-spot or a seat or section, UWB, Wi-Fi RTT or Bluetooth ranging in a native app), or counts that don't need positions: staff-drawn areas with capacity rules where the area is much larger than the error, turnstile counts, or the zone boards' Bluetooth device counts as a density proxy per board.

What this does not say: the line demo places phones by hand (tap your spot), so the GPS rows do not apply to it; carry and dropouts do. And the 24 × 16 m venue is about as large as the GPS error itself. On a festival field a crowd tens of metres across would still show up as a dense region through GPS, only not at the 1.5 m scale the thresholds are written for. That has not been measured here.

**Flow rule (2026-10-04, `crowd/flow.go`, CLAUDE.md → Detection → Flow):** a dense cluster that people are still getting out of (≥ 1 phone leaving its densest 1.5 m spot in 6 s) raises no early warning and its yellow is shown calm; red is untouched. Every number in the tables above is identical with and without it (ideal and realistic re-run on seeds 1–20: same reds, yellows, lead times, first-yellow leads). In the furnished crowdsim venues (5 seeds each, ideal phones): an auditorium show end (300 people) went from 54–83 s of yellow to 5–11 s, left only at the moment the first aisle fills (people in, nobody out yet: indistinguishable from packing); stage→surge and gate rush+surge keep their first warning, early warning and red to the 0.25 s; the classroom fire alarm with one door (2.2–2.5 /m², a moving door queue, truth never dangerous) now raises a watch in 1 of 5 runs instead of 4 of 5. Speed, front/back compression and Helbing's ρ·Var(v) from positions did not separate an aisle filling from a crowd walking up to a stage; whether anyone leaves the spot did. With GPS-placed phones the flow is unknown and nothing changes.
<!-- findings:end -->

## Method

- **Scripted scenarios** (`internal/sim`): every scenario in the line layout (8 phones 0.6 m apart, split into zones of four) and the crowd layout (24 phones, ~70 % in one or two dense groups, the rest scattered, all wandering slowly and reporting their position every 500 ms; `gather` uses 40 phones and has no line version). Each phone gets a fixed clock-sync error of up to ±25 ms. Each run is one random crowd (seed 1…N): positions, timings, amplitudes and tilts all change with the seed. Runs last 90 s (`wave` 70 s, `gather` 110 s).
- **Pipeline**: the 100 ms motion summaries go into the real detector (`internal/detect`, default config) and its result into the real crowd-density tracker (`internal/crowd`), stepped every 250 ms, as the server does. Staff-drawn area rules are not part of this evaluation. A run's level is the worst any zone or cluster reached; "red" counts a run that went red at any moment.
- **Expectations**: `wave` and `wave-jump` (a growing push travelling through the crowd) and `gather` (people packing in at ~10/m²) must go red. A single `shove`, one person squeezing past (`walkpast`) and a `procession` brushing past may reach yellow (`yellow-ok`) but not red. Everything else is a look-alike that must stay calm. A **false alarm** is a look-alike or yellow-ok run that went red; **missed** is a true-positive run that never went red. Yellow on a calm row is visible in the table but not counted as a false alarm.
- **Density yellow on calm crowd rows**: the crowd layout packs most phones into one or two tight groups (σ ≈ 0.7 m) and the default participation is 1.0, so in some random crowds the density tracker reads a group as crowded (yellow) whatever the phones are doing. The same seeds do it in every scenario, because the positions depend only on the seed. That is the density alert reacting to where people stand, not to the motion.
- **Messy phones**: each condition reruns every scenario and seed. The crowd simulation's phones are built messy (`crowdsim.Config.Realism`). The scripted scenarios' 100 ms signals are taken as the body's motion and their positions as where people truly stand, and go through the same phone model (`crowdsim.Device`); only in `walk` and `march` are people marked as walking (a pocket phone gets its leg swing). GPS fixes are gated and smoothed as the server does for live phones (`gpsMaxAcc`, `geo.Smoother`), and the detector is told each phone's accuracy radius: such phones find their neighbours by motion and their density is measured at the scale the fix supports. A smoothed fix off the venue by less than its accuracy radius is mirrored back in; further off, the phone counts as outside. A GPS phone with no usable fix yet is nowhere and counts toward nothing. Note that the line layout models the tap-your-spot demo, where real phones don't use GPS: its GPS columns say what would happen if they did.
- **Ground-truth comparison** (crowd simulation only, once a second): *position error* = median distance between where Pulse places a phone and where its owner stands; *phones counted* = phones Pulse uses ÷ phones truly in the venue; *density* = Pulse's highest cluster estimate minus the true peak (the same 1.5 m statistic over every body, with or without a phone), while the true peak is ≥ 2/m², as mean signed error (bias) and mean absolute error per run, median over runs (with ideal phones the remaining error is the 60 % sample and the estimator itself); *red before danger* = surge runs where Pulse was red before the truth turned dangerous, of the runs where it did; *guidance* = for each counted phone whose owner truly stands at ≥ 4/m², the unsmoothed direction Pulse would show (`crowd.Direction` from Pulse's positions) against the same function over every body's true position; *arrow shown* = the share of those arrows Pulse would show rather than replace by a plain instruction (confidence ≥ 0.5, i.e. an accuracy radius of at most 4 m), with the same two shares over the shown ones.
- **Crowd simulation** (`internal/crowdsim`, layout `sim`): 20 seeds per script, 250 simulated people (Social Force Model) on the default 24 × 16 m venue, 60 % carrying a phone, participation set to 0.6. Physics every 50 ms, detection every 250 ms. The phones' messages go through the same detector and density tracker; this reproduces the server's sim pipeline (minus area rules) without the app package. Scripts: `calm` (nothing happens), `attract` (a group forms around a point at 5 s), `calm→surge` (surge 0.7 at 30 s plus a shove every 3 s), `stage→surge` (front-of-stage crowding from 5 s, surge 0.7 and shoves from 30 s) and `stage→surge 0.3`. **Lead** = when the simulated truth first became dangerous (≥ 3 people at ≥ 1600 N/m or ≥ 5 people above 6/m², held 1 s) minus Pulse's first red; positive means Pulse warned first.

## Limits: this is not real-world validation

- **Simulated motion.** Every number above comes from motion that Pulse's own simulators generate. The scripted scenarios are hand-written signals (a push is a damped sine travelling at 2.4 m/s); the crowd simulation is a physics model calibrated only against walking speeds (Weidmann's fundamental diagram), not against recorded crowd crushes or real phone sensors.
- **Tuned on the same simulator.** The detector's thresholds and guards (lag window, chain rule, vertical veto, hold times) were tuned while watching these same scenarios. Performance on scenarios it was tuned on overstates performance on motion it has never seen.
- **One venue, small crowds.** A 24 × 16 m floor, 8–40 phones (250 simulated people in the crowd simulation). Nothing here says how thresholds behave for thousands of phones, other venue shapes, or real phone placement (pockets, hands, bags).
- **Density depends on participation.** The density alerts count phones; the simulation knows the true share of people with a phone, a real event has to estimate it.
- **Wave detection in the crowd simulation.** In the Social Force Model a push crosses packed neighbours faster than the detector's 120 ms per-hop floor, so the simulated surges are caught by crowd density, not by the travelling-wave detector (see the README). Real pushes recorded with phones would settle which model is right.
- **What would count as validation:** labelled recordings of real crowds (including real pushes and real look-alikes like concerts and stadium waves), replayed through the same pipeline, with thresholds frozen beforehand.
