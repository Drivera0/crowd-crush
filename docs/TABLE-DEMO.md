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
   [ zone light A ]   [ sign ]   [ MacBook ]   [ zone light B ]
                (USB hub behind the laptop, cables to each)
        [ phone ]   [ phone ]   [ phone ]   [ judge's phone ]
```

Sign facing the judge. A on the left, B on the right: that matches the map (zone A = left half, B = right half) after **Set up table demo**.

## 5-minute setup at the venue (on the Mac)

1. Plug the hub into the Mac, the three boards into the hub. Each board lights up on its own (sign: three dots, then `USB` or a dot; zone lights: blue LED solid or fading).
2. `.env` has `SIGN_URL=serial:auto,A=serial:auto,B=serial:auto`. Not yet? `scripts/boards.sh env --write` (changes only that line).
3. Start the tunnel and the server: `cloudflared tunnel run pulse` (or `make tunnel`), then `./bin/pulse`. The log says `opened /dev/cu.usbmodem… (sign)`, `… (PULSE-A)`, `… (PULSE-B)` within a few seconds. The boards switch to "in touch": the sign double-flashes two dots, the zone lights blink 1–2 times every 2 s.
4. Dashboard → **Hardware** → **Set up table demo**. Boards and laptop go in a row in the middle of the map, the demo spot turns on right beside them (joining phones line up there), and each zone light gets a zone. **Board readiness** should say 3 of 3 ready.
5. `scripts/preflight.sh` (in a third terminal). It flashes every board red for 1 s and checks each reports it back, fetches the public URL through the tunnel, checks the demo spot, keys and disk. **GO** = done. Each ✗ line says what to do.
6. Join the phones with the QR code; they line up beside the boards. Alert drill or a test alert: the sign shows STOP and the right zone light strobes.

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
| ESP32 upload sticks at "Connecting…" | `flash` stops on a zone light | Hold its **BOOT** button while it says Connecting, release when it starts writing. |
| Boards fine but alerts don't show | Preflight ✓ for boards, nothing lights in the drill | Areas drawn without lights? Press **Set up table demo** again: it assigns lights to areas that have none. |
| Nothing works and judges are coming | — | Boards are optional. Dashboard alerts and voice work without them; the Simulation page needs no phones either. |
