# Table demo: boards next to the MacBook

The small setup for judges: the sign and the two zone lights sit on the table beside the MacBook, plugged into it by USB, with a few phones. No board needs Wi-Fi; the venue network only matters for the laptop (tunnel, Gemini, ElevenLabs). Full details: [SETUP.md §4](SETUP.md#4-boards-arduino-sign--esp32-zone-lights). Run sheet for the pitch itself: [DEMO.md](DEMO.md).

## Pack

- MacBook + charger; **USB hub** (USB-C, 4 ports) — or enough ports/adapters for 3 boards
- Arduino UNO R4 WiFi (sign) + its **USB-C data cable**
- 2 × ESP32 DevKit (zone lights A and B, labelled) + **2 micro-USB data cables** (not charge-only: a charge-only cable powers the board but no port appears)
- Your phone (hotspot fallback) + 2 more phones; a printed QR card
- Spare data cable; power bank (boards can also run from it, but then only over Wi-Fi)

Before leaving home, once: `scripts/boards.sh flash` with all three plugged into the Mac, then `scripts/boards.sh status` says **up to date** for each. If the Mac has never seen an ESP32: macOS 10.15+ has a CP210x driver built in (`/dev/cu.usbserial-…` or `/dev/cu.SLAB_USBtoUART`); on an older macOS install the Silicon Labs CP210x VCP driver and allow it in System Settings → Privacy & Security.

## Lay out the table

```
   [ zone light A ] ←1.5 m→ [ sign ][ MacBook ] ←1.5 m→ [ zone light B ]
                (USB hub behind the laptop, cables to each)
            [ #1 ]    [ #2 ]    [ #3 ]    [ #4 ]   ← judges' phones
```

Sign facing the judge. A at the left end of the table, B at the right end, the sign in the middle by the MacBook: **at least 1.5 m between boards** (a 3 m table, or two tables end to end). Boards side by side (~0.3 m) can't be told apart by a phone walked up to one (see "Walk to a board" below). This matches the map after **Set up table demo**: the whole table, boards and phones, is placed inside zone A (the left half), so the row is one zone (the table profile and the three-phone wave chain work per zone). A push along the row lights zone light A; zone light B shows zone B, where nobody stands: use it for an alert drill on zone B, or drag a phone's dot into zone B on the dashboard.

## 5-minute setup at the venue (on the Mac)

1. Plug the hub into the Mac, the three boards into the hub. Each board lights up on its own (sign: three dots, then `USB` or a dot; zone lights: blue LED solid or fading).
2. `.env` has `SIGN_URL=serial:auto,A=serial:auto,B=serial:auto`. Not yet? `scripts/boards.sh env --write` (changes only that line).
3. Start the tunnel and the server: `cloudflared tunnel run pulse` (or `make tunnel`), then `./bin/pulse`. The log says `opened /dev/cu.usbmodem… (sign)`, `… (PULSE-A)`, `… (PULSE-B)` within a few seconds. The boards switch to "in touch": the sign double-flashes two dots, the zone lights blink 1–2 times every 2 s.
4. Dashboard → **Hardware** → **Set up table demo**. Boards and laptop go along the table in the middle of zone A (A and B 1.5 m either side of the sign), the demo spot turns on right beside them (joining phones line up there), and each zone light gets a zone. **Board readiness** should say 3 of 3 ready.
5. `scripts/preflight.sh` (in a third terminal). It flashes every board red for 1 s and checks each reports it back, fetches the public URL through the tunnel, checks the demo spot, keys and disk. **GO** = done. Each ✗ line says what to do.
6. Join the phones with the QR code; they line up beside the boards. Alert drill or a test alert: the sign shows STOP and the right zone light strobes.

### Optional: the sign as a third Bluetooth anchor

For a 2-D Bluetooth fix (A, B and the sign; see [BEACONS.md](BEACONS.md#the-sign-as-a-third-anchor)), switch the sign to beacon mode **before step 3** (Pulse must be stopped: it holds the port):

- On: `scripts/boards.sh beacon on`. The sign reboots, its USB port drops for about 3 s, then it advertises `PULSE-S`. `scripts/boards.sh status` shows the zone lights hearing `PULSE-S` and the sign's Wi-Fi as "off: Bluetooth beacon PULSE-S".
- Off: `scripts/boards.sh beacon off` (back to Wi-Fi + USB).
- Cost: **no Wi-Fi on the sign** while it's a beacon. On the table it is on USB anyway (`SIGN_URL=serial:auto`), so nothing changes there; alerts, the matrix and preflight work the same. Don't use it where the server reaches the sign over Wi-Fi (a server in WSL).
- The switch is kept in the sign's flash (survives power and reflashing). No reflash is needed either way.

## What the lights mean

| Board | Pattern | Meaning |
|---|---|---|
| Sign | two dots, double flash every 1.5 s | calm, Pulse is talking to it ✓ |
| | one dot, heartbeat | calm, on Wi-Fi, but Pulse isn't talking to it |
| | three dots | booting / trying Wi-Fi (first 15 s) |
| | `USB` | no Wi-Fi and Pulse isn't talking to it: is it plugged into the Mac and is Pulse running? |
| | steady `!` | yellow |
| | flashing arrow, then `STOP` | red |
| Zone light (blue LED) | 1 short blink every 2 s | calm, Pulse is talking to it ✓ (hears no other board) |
| | 2 short blinks | … and hears another Pulse board ✓ (normal on a table) |
| | 3 short blinks | … and a phone is linked over Bluetooth |
| | slow fade | on Wi-Fi, Pulse isn't talking to it |
| | solid on | Pulse isn't talking to it and no Wi-Fi |
| | slow even blink | yellow |
| | fast strobe | red |

"Talking to it" = a command from Pulse in the last 20 s, over USB or Wi-Fi. Pulse asks every 5 s, so a board that is plugged in and identified always shows the ✓ pattern.

## Recovery

| Problem | What you see | Do this |
|---|---|---|
| Board not found | Readiness: Offline, "plug it in"; LED solid / sign shows `USB` | Reseat the cable at both ends; try another hub port or the spare cable (charge-only cables are the usual culprit). Pulse looks again every 2 s, no restart needed. `scripts/boards.sh status` (with Pulse stopped) lists every port and what answered. |
| No port at all for an ESP32 on the Mac | `ls /dev/cu.*` shows no `usbserial`/`SLAB` | CP210x driver (older macOS, see Pack), or the cable. |
| Port busy | "busy: another program has it open" | Quit the Arduino IDE (its Serial Monitor holds the port), or a second Pulse. `flash` and `wifi` need Pulse stopped too. |
| Wrong board on a zone | Light B reacts to zone A | Zone lights keep their zone in flash. `scripts/boards.sh status` shows each one's zone; to swap, stop Pulse and run `bin/boards zone -port <port> -zone B`. |
| Firmware out of date | Readiness / status / preflight say so | Stop Pulse, `scripts/boards.sh flash`, start Pulse. |
| Wrong Wi-Fi (only if you use Wi-Fi) | `status` shows `no (trying <name>)` | Stop Pulse, `scripts/boards.sh wifi`, enter the hotspot's name and password (2.4 GHz, Maximize Compatibility). USB keeps working the whole time. |
| Sign frozen (matrix stuck, no answer to `S`, upload fails) | Readiness: sign offline or not confirming | Unplug and replug it. Still stuck: **double-tap its RESET button** (the onboard LED fades in and out: bootloader mode), then `scripts/boards.sh flash --port /dev/cu.usbmodem…`. |
| Sign's beacon didn't start | `status` shows "beacon failed (…)" for the sign; zone lights don't hear `PULSE-S` | The sign fell back to Wi-Fi + USB by itself and works as a normal sign. Retry with `scripts/boards.sh beacon on`, or carry on without the third anchor. |
| ESP32 upload sticks at "Connecting…" | `flash` stops on a zone light | Hold its **BOOT** button while it says Connecting, release when it starts writing. |
| Boards fine but alerts don't show | Preflight ✓ for boards, nothing lights in the drill | Areas drawn without lights? Press **Set up table demo** again: it assigns lights to areas that have none. |
| Nothing works and judges are coming | — | Boards are optional. Dashboard alerts and voice work without them; the Simulation page needs no phones either. |

The detector's table-profile measurements (thresholds, timings, what a push between two phones looks like) are in [DEMO.md](DEMO.md), not here.

## Moving about: judges see their dots move

A web page can't measure where a phone is to the decimetre, so movement at the table shows up in three honest ways. None of them touches detection: a lined-up, tapped or snapped phone is an exact placement (the position estimator leaves it exactly there), and the row stays inside one zone.

**Tap to move (any phone).** On the live screen, under "You're #3 in the row": **I moved: show where I am now**. The map that opens is zoomed to the table: the row's places (dashed circles), the boards (squares, labelled) and the other phones in their colours with their numbers, so "I'm now next to #2" is one tap. **That's me** sends the spot; the dot slides there on the big screen, the number goes (the place is kept free for this phone: nobody new is lined up on it), and **Back to my place in the row (#3)** puts it back (`POST /api/demo/back`; if someone was dragged onto that place meanwhile, the first free one).

**Swap places (staff, dashboard Live page).** Drag one phone's dot onto another phone's place in the row: the two swap, both phones' "You're #n" update within half a second, and the toast says "Blue Otter and Red Fox swapped places". Dropped on a free place, the phone is lined up there; dropped anywhere else it goes there as before.

**Walk to a board (Android).** With the Pulse Android app, or Chrome with the Bluetooth scan flag or connect mode ([BEACONS.md](BEACONS.md)), a phone carried right up to a board (a hand's width, < ~0.5 m) is put beside that board on the map after ~2 s: src `beacon`, "Near Zone light A" under the dot and on the phone. Walk away and 5 s later it goes back where it was (its place in the row, or the spot it tapped). The rule (`server/internal/app/beaconsnap.go`):

| | Value | Why |
|---|---|---|
| Snap in | nearest board < 0.5 m (≈ −57 dBm at the default model) | right next to it, not "somewhere near the table" |
| … and clearly that one | every other board ≥ 2.3 × farther (8 dB weaker) | more than the few dB a hand or a turn changes per second |
| Agreement | 3 reports in a row (~1/s) spanning ≥ 2 s, gaps ≤ 2.5 s | one lucky reading doesn't move a dot |
| Stay | that board < 1.2 m (≈ −66 dBm) and not 2.3 × farther than another | hysteresis: 8 dB between in and out |
| Back | no board near for 5 s, or no reports | |

Two boards closer than about 1.2 m (2.3 × 0.5 m) can't be told apart, which is why the boards go at the ends of the table, 1.5 m apart. Per-board calibration on the Beacons page (`/beacons.html`) makes the 0.5 m honest for each board; uncalibrated, it is the firmware's model. iPhones can't scan Bluetooth from a web page: they use tap to move.
