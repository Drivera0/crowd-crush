# Bluetooth beacons: a phone locating itself from the Pulse boards

An opt-in extra. Nothing in Pulse depends on it, and a phone that doesn't use it behaves exactly as before.

## What it is

The fixed boards advertise over Bluetooth: the two ESP32 zone lights as `PULSE-A` and `PULSE-B`, and the sign as `PULSE-S` when it runs its beacon build (`SIGN_BEACON=1`). Staff place the boards on the dashboard map (Hardware page). A phone that can measure how strongly it hears each board gets a distance per board, and from the distances a position in venue metres.

Distance uses the same model as the firmware: `d = 10^((tx1m − RSSI) / (10 · n))`, with `tx1m = −64 dBm` (signal at 1 m) and `n = 2.2`. Every 6.6 dB weaker is twice as far.

What the geometry allows, and what the server reports (`dims`):

| Boards heard (on the map) | Result |
|---|---|
| 1, further than 1.5 m | A distance ring. No position. |
| 1, closer than 1.5 m | "Next to board X" (`dims: 0`). |
| 2 (or more, all in a line) | The position **along the line through the boards** only (`dims: 1`). Which side of the line the phone is on cannot be known, so the cross-line uncertainty is large (≥ 2 m) and shown as a stretched ellipse. |
| 3 or more, not in a line | A 2-D fix (`dims: 2`), weighted least squares. |

With A, B and the sign all on the map and not in a line, a 2-D fix is possible. Expect metres, not centimetres: in the tests, 3 dB of signal noise gives a mean error of about 3 m in a 24 × 16 m venue with three or four boards, and about 0.8 m along the line between two boards 8 m apart. Bodies absorb 2.4 GHz, so a packed crowd makes it worse.

A beacon fix becomes the phone's position on the map (`src: "beacon"`) only when it is 2-D, or when the phone has no position from anywhere else. Otherwise it is kept as an input for the position estimator (`App.BeaconFix`).

## Three ways to get a range, all Android only

No iPhone browser gives a web page Bluetooth. On an iPhone nothing is shown.

Scan and connect mode are for the web page. The third, the app's own advert, needs the Pulse Android app.

**Scan mode** (needs a hidden Chrome flag). The page hears the boards' adverts and their signal strength directly (`navigator.bluetooth.requestLEScan`). One permission prompt, every board at once.

**Connect mode** (no flag; works on any Android Chrome). The page connects to a board with standard Web Bluetooth and writes its random session id to it. The board measures the signal strength of that connection and the server reads it from the board (`GET /links`, about once a second). One Chrome pop-up per board: tap "Connect to a Pulse board", pick `PULSE-A`, then "Connect another" for `PULSE-B`.

- Needs the zone-light firmware with connect mode (flash it with `pwsh scripts/flash-zone-lights.ps1`). The sign does not take connections.
- **At most 3 phones per board at a time** (the ESP32 Bluetooth stack's connection limit). A fourth is disconnected at once. So this is a proof for a few phones, not for a crowd.
- A connected phone gives one distance per board it is connected to: one board = a ring, two = the line, three = 2-D. The sign can't be one of them, so connect mode alone tops out at the line between A and B until there is a third ESP32.
- The board measures the phone here (not the phone the board), and a phone's radio is not an ESP32's, so connect mode has its own 1 m reference (`connTxPower1m`).

The page prefers scan mode when the flag is on, otherwise offers connect mode.

**App advert** (the Pulse Android app, no web page). The app advertises the first 8 hex characters of the phone's session id (manufacturer data `FF FF` + `PLS1` + the 8 characters, no name). Every zone light already scans for the crowd counter, so it hears the phone, keeps a smoothed signal strength (up to 24 phones per board, forgotten after 10 s) and reports it in `/links` as `heard`. No connection, no cap of three. The server matches the 8 characters to the one connected phone whose id starts with them; if two phones share them, or none does, the reading is ignored. The sign only broadcasts; it can't hear phones.

How they combine: per phone and board, the fresher of the connection reading and the advert reading is used. When the phone also scans and hears that board itself, the board is still one anchor: the two distances are averaged, weighted by their certainty. Both board-side kinds share one 1 m reference (`connTxPower1m`).

Scanning on the boards: passive, a 50 ms window every 100 ms (half the airtime; the rest is Wi-Fi's), 5 s on and 1 s off. A phone advertising every 100–250 ms is heard several times a second; the longest gap is about a second.

## Test it tonight (Dan's Android phone)

Before you start: server running, tunnel up, boards powered, and **A, B (and the sign) dragged onto the map** on the dashboard's Hardware page. A board that isn't on the map can't be used.

### A. Connect mode (no flag): try this first

1. On the phone, in Chrome, open `https://<tunnel address>/beacons.html`.
2. "This browser" should show ✓ Secure page and ✓ Connect mode.
3. Tap **Connect to a Pulse board**. Chrome lists nearby `PULSE-…` devices. Pick `PULSE-A`, tap Pair.
4. Within a few seconds "Boards heard" shows `PULSE-A`, via "connection", with an RSSI and a distance. With one board you get "No position … a ring" unless you are within 1.5 m, then "Next to PULSE-A".
5. Tap **Connect another board**, pick `PULSE-B`. Now there is a 1-D fix: a dot on the line between A and B on the mini-map.
6. Stand 1 m from A: expect about −64 dBm and about 1 m, give or take a factor of two before calibration. Then calibrate (below).
7. Walk from A to B: the dot should slide along the line, the distance to A growing and to B shrinking.

If the chooser is empty: is the phone's Bluetooth on, does Chrome have the "Nearby devices" permission (Android 12+) or Location (older), and has the board been flashed with the new firmware? If connecting fails on a board that already has three phones, that is the cap.

### B. Scan mode (flag)

1. In Chrome on the phone open `chrome://flags/#enable-experimental-web-platform-features`, set it to **Enabled**, tap **Relaunch**.
2. Open `https://<tunnel address>/beacons.html`. "This browser" should now show ✓ Scan mode.
3. Tap **Start scanning** and allow it. `PULSE-A`, `PULSE-B` and `PULSE-S` appear in the table with raw and smoothed RSSI, distance and age (age should stay under about a second).
4. Stand 1 m from board A: about −64 dBm, about 1 m. Then B. Then the sign (its radio is different: calibrate it).
5. Walk between the boards and watch the dot. With A, B and the sign on the map the page says "2-D fix"; with only two, "1-D fix".
6. Chrome stops a scan when the page is hidden or the screen locks; the button then says "Scan paused: tap to resume".

### C. In the crowd flow

Join as usual at `https://<tunnel address>/`. On the live screen there is one extra button: "Use Bluetooth beacons for a better position" (flag on) or "Connect to a Pulse board for a better position" (flag off). The dashboard's node panel (`GET /api/node/{id}`) then lists the boards that phone hears, and the node says "Bluetooth beacons" when the fix is what places it.

### Numbers to expect

| Distance | RSSI (uncalibrated) |
|---|---|
| 0.5 m | about −57 dBm |
| 1 m | about −64 dBm |
| 2 m | about −71 dBm |
| 4 m | about −77 dBm |
| 8 m | about −84 dBm |

Readings jump by ±5 dB from one second to the next; that is normal for Bluetooth, and the smoothed column (median of 2 s, then a moving average) is what the fix uses. How you hold the phone and whether your body is between it and the board easily costs 10 dB.

## Calibration

The −64 dBm reference was measured board to board. A phone's antenna differs, and the sign (Uno R4) transmits at a different power than the ESP32s. So each beacon can have its own 1 m reference:

1. Stand 1 m from the board, phone held as you'd normally hold it.
2. Wait for the smoothed RSSI to settle.
3. Tap **1 m** on that board's row and confirm. That reading becomes the board's reference (shown in "Boards at this venue" as "(own)").

On a connection or app-advert row the same button sets the board-side reference (`connTxPower1m`), for every board.

By hand: `PUT /api/beacons/model` with `{"beacon":"PULSE-S","txPower1m":-58}` (one board; `0` removes it), `{"conn":true,"txPower1m":-60}` (connect mode) or `{"txPower1m":-64,"pathLossN":2.2}` (the model). Saved in `data/beacons.json`.

## Privacy

The phone only asks Chrome for devices whose name starts with `PULSE-`, checks the name again before reporting, and the server drops any name that isn't one of this venue's boards. No other Bluetooth device is seen, reported or stored. In connect mode the phone writes its random session id to a board it chose; the board keeps it only while connected and reports the id with a signal strength, nothing else.

## Limits

- Android Chrome only. No iPhone.
- Two ESP32s give a line, not a position. The sign's beacon makes a third anchor for scan mode only.
- Scan mode needs a flag no attendee will turn on; connect mode needs a pop-up per board and holds 3 phones per board.
- A web page only scans while it is open and on screen.
- RSSI ranging is rough, and worse through a crowd.

## More anchors

- **Sign**: has a beacon build (`SIGN_BEACON=1` in `arduino/sign/sign.ino`): it advertises `PULSE-S`, has no Wi-Fi in that build and is driven over USB serial. The server treats `PULSE-S` as the sign's beacon whenever the sign is on the map, online or not. Calibrate it separately.
- **Laptop**: a browser can't advertise. It would take a small native helper, or simply a third ESP32 on the laptop's USB port flashed as a zone light.
- **A third ESP32** is the cheapest way to a 2-D fix in both modes.

## What a native or instant app would add

- **Every phone**: iPhones included (CoreBluetooth / iBeacon ranging), no flag, no pop-up per board.
- **Background scanning**: with the screen off and the phone in a pocket, which is where phones are in a crowd.
- **The phone advertising its own id**: the boards (already scanning for the crowd counter) hear each phone and measure it, with no connection and no 3-phone cap. The boards and the server already do their half of this (see "App advert"); the Android app is being built.
- Access to the raw advert rate and TX power, and newer ranging (Bluetooth channel sounding, UWB) on phones that have it.

## Wire format

Phone → server, about once a second, on the phone WebSocket (scan mode):

```json
{"type":"beacons","seen":[{"name":"PULSE-A","rssi":-61,"n":14}]}
```

At most 16 entries; names `PULSE-<tag>`, ≤ 32 printable characters, no duplicates; RSSI −110…−20; `n` = adverts in the 2 s window. One bad entry drops the whole report. An empty list means "I hear none now".

Board → server: `GET http://<board>/links` →

```json
{"links":[{"id":"<session id>","rssi":-57,"age":1}],
 "heard":[{"id":"1a2b3c4d","rssi":-63,"age":1}]}
```

`links` = phones connected (connect mode; ids are letters, digits and dashes, ≤ 36), `heard` = app phones heard advertising (8 hex characters). Both are also in `/pulse`. The server polls `/links` every second while any board reports a phone, every 3 s otherwise.

HTTP:

- `GET /api/beacons` → boards (beacon name, key, position if placed, 1 m reference, connectable, links), the model, `connTxPower1m`, the GATT UUIDs, venue size, `placed`, `maxDims`.
- `POST /api/beacons/locate` `{"seen":[…], "id":"<id written to boards, optional>"}` → a fix; keeps nothing (the diagnostic page).
- `PUT /api/beacons/model` → see Calibration.
- `GET /api/node/{id}` → `beacons`: the phone's latest fix and the boards it hears.

A fix:

```json
{"ok":true,"dims":1,"x":7.02,"y":8,"acc":2.6,"along":0.9,"cross":2.4,"axis":[1,0],"near":"",
 "note":"…","heard":[{"name":"PULSE-A","rssi":-74.5,"n":12,"dist":3.0,"placed":true,"src":"scan"}]}
```

`src` is `scan` (the phone heard the board), `conn` (the board measured the connection) or `adv` (the board heard the app's advert).

Code: `server/internal/protocol/beacons.go`, `server/internal/app/beacons.go` (geometry, per-phone state, API), `server/internal/app/beaconlinks.go` (connect mode polling), `server/internal/hub/beacons.go`, `web/phone/src/beacons.ts`, `web/phone/beacons.html` + `src/beacons-diag.ts`, `arduino/zone-light/zone-light.ino`.

## Verified and not

Verified here: the Go tests (validation, distance model, 1/2/3/4-board geometry with noise, unknown names, calibration, link polling and merging, id sanitising, app-advert matching), the web type-check and build, the diagnostic page rendering and feature-detecting in desktop Chrome, and the zone-light firmware compiling (1.70 MB, 53 % of the Huge APP partition; 71 KB of RAM for globals, 21 %; no partition change).

**Not tested: any real Bluetooth.** No scan, no connection, no app advert and no flashed board has been exercised. The firmware's connect mode and app-phone hearing are compile-checked only. In particular, unverified on hardware: that the ESP32 keeps advertising and scanning with phones connected, that three connections hold next to Wi-Fi, and that reporting every advert to the scan callback (instead of once per scan) leaves the web server responsive in a room full of Bluetooth devices.
