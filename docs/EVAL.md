# Pulse evaluation

Generated 2026-10-04T03:06:06Z by `go run ./server/cmd/eval -seeds 20` (`make eval`) in 16s on 32 CPU threads. Machine-readable copy: [`eval.json`](eval.json) (served at `GET /api/eval`).

**False alarms: 0 of 600 look-alike runs went red. Missed: 22 of 160 true-positive runs never went red.**

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
| `calm` | sim | calm | 20 | 0 | 4 | 16 | — | — | truth never dangerous in 20/20; median peak 2.2/m², 0 N/m |
| `attract` | sim | yellow-ok | 20 | 0 | 20 | 0 | — | — | truth never dangerous in 20/20; median peak 2.5/m², 0 N/m |
| `calm→surge` | sim | red | 20 | 20 | 0 | 0 | 34.8 | -2.3 | red from crowd density, not waves; median peak 6.0/m², 4523 N/m |
| `stage→surge` | sim | red | 20 | 20 | 0 | 0 | 14.3 | 16.0 | red from crowd density, not waves; median peak 6.0/m², 4794 N/m |
| `stage→surge 0.3` | sim | red | 20 | 20 | 0 | 0 | 14.3 | 16.4 | red from crowd density, not waves; median peak 5.4/m², 2364 N/m |

## Method

- **Scripted scenarios** (`internal/sim`): every scenario in the line layout (8 phones 0.6 m apart, split into zones of four) and the crowd layout (24 phones, ~70 % in one or two dense groups, the rest scattered, all wandering slowly and reporting their position every 500 ms; `gather` uses 40 phones and has no line version). Each phone gets a fixed clock-sync error of up to ±25 ms. Each run is one random crowd (seed 1…N): positions, timings, amplitudes and tilts all change with the seed. Runs last 90 s (`wave` 70 s, `gather` 110 s).
- **Pipeline**: the 100 ms motion summaries go into the real detector (`internal/detect`, default config) and its result into the real crowd-density tracker (`internal/crowd`), stepped every 250 ms, as the server does. Staff-drawn area rules are not part of this evaluation. A run's level is the worst any zone or cluster reached; "red" counts a run that went red at any moment.
- **Expectations**: `wave` and `wave-jump` (a growing push travelling through the crowd) and `gather` (people packing in at ~10/m²) must go red. A single `shove`, one person squeezing past (`walkpast`) and a `procession` brushing past may reach yellow (`yellow-ok`) but not red. Everything else is a look-alike that must stay calm. A **false alarm** is a look-alike or yellow-ok run that went red; **missed** is a true-positive run that never went red. Yellow on a calm row is visible in the table but not counted as a false alarm.
- **Density yellow on calm crowd rows**: the crowd layout packs most phones into one or two tight groups (σ ≈ 0.7 m) and the default participation is 1.0, so in some random crowds the density tracker reads a group as crowded (yellow) whatever the phones are doing. The same seeds do it in every scenario, because the positions depend only on the seed. That is the density alert reacting to where people stand, not to the motion.
- **Crowd simulation** (`internal/crowdsim`, layout `sim`): 20 seeds per script, 250 simulated people (Social Force Model) on the default 24 × 16 m venue, 60 % carrying a phone, participation set to 0.6. Physics every 50 ms, detection every 250 ms. The phones' messages go through the same detector and density tracker; this reproduces the server's sim pipeline (minus area rules) without the app package. Scripts: `calm` (nothing happens), `attract` (a group forms around a point at 5 s), `calm→surge` (surge 0.7 at 30 s plus a shove every 3 s), `stage→surge` (front-of-stage crowding from 5 s, surge 0.7 and shoves from 30 s) and `stage→surge 0.3`. **Lead** = when the simulated truth first became dangerous (≥ 3 people at ≥ 1600 N/m or ≥ 5 people above 6/m², held 1 s) minus Pulse's first red; positive means Pulse warned first.

## Limits: this is not real-world validation

- **Simulated motion.** Every number above comes from motion that Pulse's own simulators generate. The scripted scenarios are hand-written signals (a push is a damped sine travelling at 2.4 m/s); the crowd simulation is a physics model calibrated only against walking speeds (Weidmann's fundamental diagram), not against recorded crowd crushes or real phone sensors.
- **Tuned on the same simulator.** The detector's thresholds and guards (lag window, chain rule, vertical veto, hold times) were tuned while watching these same scenarios. Performance on scenarios it was tuned on overstates performance on motion it has never seen.
- **One venue, small crowds.** A 24 × 16 m floor, 8–40 phones (250 simulated people in the crowd simulation). Nothing here says how thresholds behave for thousands of phones, other venue shapes, or real phone placement (pockets, hands, bags).
- **Density depends on participation.** The density alerts count phones; the simulation knows the true share of people with a phone, a real event has to estimate it.
- **Wave detection in the crowd simulation.** In the Social Force Model a push crosses packed neighbours faster than the detector's 120 ms per-hop floor, so the simulated surges are caught by crowd density, not by the travelling-wave detector (see the README). Real pushes recorded with phones would settle which model is right.
- **What would count as validation:** labelled recordings of real crowds (including real pushes and real look-alikes like concerts and stadium waves), replayed through the same pipeline, with thresholds frozen beforehand.
