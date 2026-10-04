# Setup on your laptop

Do these in order. Nothing past step 1 is required for the demo to work: every service falls back.

## 0. Tools

| Tool | Mac | Windows |
|---|---|---|
| Go 1.25+ | `brew install go` | https://go.dev/dl (installer) |
| Node 20+ | `brew install node` | https://nodejs.org (LTS installer) |
| cloudflared | `brew install cloudflared` | `winget install --id Cloudflare.cloudflared` |
| make | built in (Xcode CLT) | use Git Bash, or run the `go`/`npm` commands below directly |
| Arduino IDE 2 | https://www.arduino.cc/en/software | same |

## 1. Build and run

```sh
git clone https://github.com/Drivera0/crowd-crush && cd crowd-crush
git checkout main-85y2r6
make build           # or: cd web && npm install && npm run build && cd .. && go build -o bin/pulse ./server/cmd/pulse
./bin/pulse          # dashboard: http://localhost:8080/dash/
make sim             # in a second terminal: 8 fake phones (or: go run ./server/cmd/sim -n 8 -scenario wave)
```

## 2. `.env`

Run `make env` (Mac / Git Bash). It explains each value, asks for it, writes `.env`, then runs `./bin/pulse -check` to test every key. Or copy `.env.example` to `.env` and fill it in by hand:

| Variable | Where to get it |
|---|---|
| `TIGER_DATABASE_URL` | https://console.cloud.timescale.com → Create service → copy the connection string (`postgres://tsdbadmin:…@….tsdb.cloud.timescale.com:…/tsdb?sslmode=require`) |
| `GEMINI_API_KEY` | https://aistudio.google.com/apikey → Create API key |
| `ELEVENLABS_API_KEY` | https://elevenlabs.io/app/settings/api-keys → Create key with Text to Speech access |
| `ELEVENLABS_VOICE_ID` | optional: Voice library → ⋯ → Copy voice ID |
| `SIGN_URL` | after step 4: `serial:auto,A=serial:auto,B=serial:auto` for boards plugged into this laptop (`scripts/boards.sh env --write` writes it), or their Wi-Fi URLs |
| `PUBLIC_URL` | after step 3: `https://pulse.yourname.tech` |

`./bin/pulse -check` (or `make doctor`) re-tests at any time. Never commit `.env`.

## 3. The `.tech` domain → Cloudflare Tunnel

Phones need HTTPS for motion sensors; the tunnel gives you that with no port forwarding.

1. Claim the domain (StormHacks/MLH code) at https://get.tech.
2. https://dash.cloudflare.com → **Add a site** → your `.tech` domain → Free plan. Cloudflare shows two nameservers.
3. In the get.tech control panel → your domain → **Name Servers** → replace them with Cloudflare's. **Do this first: it can take hours.** Cloudflare emails you when it's active.
4. On the laptop:
   ```sh
   cloudflared tunnel login                       # browser opens, pick the domain
   cloudflared tunnel create pulse
   cloudflared tunnel route dns pulse pulse.yourname.tech
   cloudflared tunnel run --url http://localhost:8080 pulse
   ```
5. Open `https://pulse.yourname.tech/dash/` and set `PUBLIC_URL=https://pulse.yourname.tech` in `.env`.

Until DNS is ready: `cloudflared tunnel --url http://localhost:8080` gives a random `https://….trycloudflare.com` URL that works the same way.

## 4. Boards (Arduino sign + ESP32 zone lights)

The boards are optional: without them alerts still show on the dashboard and are read aloud. For a table demo (boards next to the laptop) see **[TABLE-DEMO.md](TABLE-DEMO.md)**, the one-page version of this section.

**USB first.** Every board works over its USB cable with no Wi-Fi at all: plug the sign and both zone lights into the laptop that runs Pulse (a USB hub is fine) and set

```
SIGN_URL=serial:auto,A=serial:auto,B=serial:auto
```

`scripts/boards.sh env --write` writes exactly that line into `.env` for the boards it finds (only that line changes; nothing else in `.env` is read out or printed). Wi-Fi stays available as a fallback and for a server that can't see USB (WSL).

### One command per job (Mac / Linux: `scripts/boards.sh`, Windows: `pwsh scripts/boards.ps1`)

| Command | What it does |
|---|---|
| `scripts/boards.sh status` | Every board: USB port or Wi-Fi address, name (`sign`, `PULSE-A`…), zone, firmware **up to date or not**, Wi-Fi network, the boards it hears. If Pulse is running it asks Pulse instead (Pulse holds the ports). |
| `scripts/boards.sh flash` | Compiles and uploads the right sketch to each board on USB, one at a time, with a build id. Zone lights keep the zone stored in their flash; one with no zone gets its letter from `arduino/zone-light/zones.map` (by MAC) or the next free one. Installs the Arduino cores the first time. |
| `scripts/boards.sh wifi` | Asks for a network name and password and saves it on every board over USB (tried before the built-in ones, kept across reboots and reflashes; the password is never printed). |
| `scripts/boards.sh env [--write] [--wifi]` | The `SIGN_URL` line for the boards on USB; `--wifi` uses their Wi-Fi addresses instead. |
| `scripts/boards.sh preflight` | The table-demo go/no-go list (= `scripts/preflight.sh`, needs Pulse running). |

Needs Go (it builds `bin/boards`, the small tool that talks to the boards) and `arduino-cli` for `flash`: on `PATH`, or the copy inside the Arduino IDE 2 app (`/Applications/Arduino IDE.app/Contents/Resources/app/lib/backend/resources/arduino-cli` on a Mac). Stop Pulse (Ctrl-C) before `flash` or `wifi`: only one program can hold a serial port. On Windows the old `pwsh scripts/flash-sign.ps1` and `pwsh scripts/flash-zone-lights.ps1` still work (now with build ids, `-NoEnv`, and zones set over USB).

**USB drivers.** The UNO R4 needs none. The ESP32 DevKits use a Silicon Labs CP210x USB-serial chip: macOS 10.15+ and Windows 11 have a driver built in (ports `/dev/cu.usbserial-…` or `/dev/cu.SLAB_USBtoUART`, `COM…`); on older macOS install the CP210x VCP driver from silabs.com and allow it in System Settings → Privacy & Security. Linux: add yourself to the `dialout` group. A port that never shows up is usually a charge-only cable.

### How `serial:auto` finds each board

Both ESP32s have the same USB ID, so Pulse asks instead of guessing: it opens each candidate USB serial port once (Arduino 0x2341, CP210x 0x10C4, CH340 0x1A86 …), sends `S`, reads the board's status line (`"kind":"sign"`, or `"name":"PULSE-A","zone":"A"`), hands each port to the entry that wants that board and closes the rest. `A=serial:auto` takes the zone light that says zone A; a zone light with no zone yet (or a zone nobody asked for) goes to a zone nobody else matched and is taught its letter (`L calm A`, stored in its flash). Unplug a board and its port is identified again when it comes back, wherever it comes back. Named ports still work: `serial:/dev/cu.usbmodem1101`, `B=serial:COM9`. The R4 gets DTR on (it ignores serial input without it); an ESP32's DTR/RTS are left off, because they drive its reset pin (opening the port with them on rebooted the board every time).

### The serial protocol (115200 baud, one line each way)

| Laptop → board | Board → laptop |
|---|---|
| `L <calm\|yellow\|red> [zone]` | sets the level (as `GET /level?v=…&zone=…`); a log line `level … zone …` |
| `S` | one JSON line shaped like `GET /pulse`: `kind`, `name`, `mac`, `zone`, `level`, `fw`, `wifi`, `ssid`, `ip`, `rssi`, `uptime`, and on zone lights `ble`, `peers`, `links`, `heard` |
| `W <ssid><TAB><password>` (or `W <ssid> <password>`: the last space splits) | `wifi: saved <ssid>; joining it now` (never the password); `W -` forgets it |

Anything else the boards print is a log line, never JSON. The server ignores non-JSON lines.

### Wi-Fi, when you want it

Wi-Fi is optional and never blocks anything: a board boots, answers USB, scans Bluetooth and drives its LEDs at once, and joins Wi-Fi in the background. It tries, in turn: the network saved with `boards.sh wifi`, then `SECRET_SSID`, `SECRET_SSID2`, `SECRET_SSID3` from `arduino_secrets.h` (the 2nd and 3rd are optional: an old secrets file still builds), about 15 s each, and goes round again. Typical use: home Wi-Fi as `SECRET_SSID`, your phone's hotspot as `SECRET_SSID2` (2.4 GHz; iPhone: Maximize Compatibility). Venue Wi-Fi usually blocks device-to-device traffic, so over Wi-Fi put the laptop and the boards on the hotspot. Then `SIGN_URL=http://<sign ip>,A=http://<ip>,B=http://<ip>` (`scripts/boards.sh env --wifi` prints it).

### Firmware build ids

`flash` stamps each build with `<hash> <date>`, where the hash is the git blob hash of the sketch (LF line endings): it changes exactly when the sketch does, committed or not. Boards report it as `fw`; `status`, the dashboard's Board readiness panel and the preflight say **out of date** when it doesn't match the sketch in this checkout (`dev` = built by hand in the IDE).

### What the lights mean

| Board | Pattern | Meaning |
|---|---|---|
| Sign (12×8 matrix) | two dots, double flash | calm, and Pulse is talking to it (USB or Wi-Fi, last 20 s) |
| | one dot, heartbeat | calm, on Wi-Fi, nobody talking to it yet |
| | three dots | still trying Wi-Fi (first 15 s) |
| | `USB` | no Wi-Fi: plug it into the laptop running Pulse |
| | steady `!` / flashing arrow + `STOP` | yellow / red |
| Zone light (blue LED) | 1 / 2 / 3 short blinks every 2 s | calm, Pulse is talking to it; 1 = alone, 2 = hears another Pulse board, 3 = a phone is linked |
| | slow fade | on Wi-Fi, waiting for Pulse |
| | solid | nobody is talking to it and no Wi-Fi (booting, or not plugged into the laptop running Pulse) |
| | slow even blink / fast strobe | yellow / red |

### By hand, if the scripts can't run

Arduino IDE → Boards Manager → **Arduino UNO R4 Boards** (sign, `arduino/sign/sign.ino`) and **esp32 by Espressif** (zone lights, board "ESP32 Dev Module", Tools → Partition Scheme → **Huge APP**, `arduino/zone-light/zone-light.ino`; hold **BOOT** if it sticks at "Connecting…"). The build id is then `dev`. Optional extras on the zone lights: LEDs on GPIO 25/26/27, a buzzer on 14.

- **WSL can't see USB ports** (unless attached with `usbipd`), so a server inside WSL uses the boards' Wi-Fi URLs. USB mode is for the Mac, or a native Windows/Linux build, at the demo.
- The sign's Bluetooth beacon (`PULSE-S`, a third position anchor) is a switch, not a build: `scripts/boards.sh beacon on|off` (Windows: `pwsh scripts/boards.ps1 beacon on|off`). While on, the sign has no Wi-Fi and must be driven over USB, so keep it off for a server in WSL. Details: [BEACONS.md](BEACONS.md#the-sign-as-a-third-anchor). (The old separate beacon build hung an R4. Not the radio firmware: it is 0.6.0, the latest. Most likely Bluetooth was started on a radio module still set up for Wi-Fi. The switch restarts the radio module before starting Bluetooth and falls back to Wi-Fi if it fails.)

## 5. Before judging

Record real runs (dashboard → Record run) with labels such as `wave-push-end` and `dance-jumping`; `go test ./...` checks each run against its label. Rehearse with **Simulation → Saved runs** as the fallback.

## 6. Phones and browsers that can join

How each was checked: **E** = headless Chrome emulation (`cd web && PULSE_URL=http://localhost:8097 PULSE_LAN_URL=http://<lan ip>:8097 npm run test:e2e`, against a running server): that browser's user agent, touch, the motion APIs it has or lacks stubbed in, synthetic `devicemotion` at 60 Hz, then what the page shows and what `/api/join/stats` and `/api/node/{id}` say. **G** = Go test over the real WebSocket (`go test ./server/internal/app -run Join`). **—** = not tested. Emulation proves the page's logic and messages, not that a real phone's browser grants motion: the last column is what still needs a real device.

| Phone / browser | Result | How checked | Still needs a real phone |
|---|---|---|---|
| iPhone, iOS 16.4–18 Safari (incl. camera QR preview) | works: Join → Allow → streaming, number in the row, wake lock held | E | the real Allow prompt; wake lock; motion with the screen dimmed |
| iPhone, iOS 15–16.3 Safari | works; no Screen Wake Lock there, so the page says "Settings → Display & Brightness → Auto-Lock → Never" | E (no `wakeLock`) | screen staying on; that the lowered build target (Safari 14 syntax) parses |
| iPhone, motion denied | message: "aA → Website Settings → Motion & Orientation Access → Allow, then tap Join again"; reported `motion-denied` | E | that iOS shows no second prompt after a deny (expected: the settings route is the fix) |
| iPhone, Chrome / Firefox / Edge (all WebKit) | same as Safari (same prompt, same messages) | E (Chrome iOS UA) | — |
| iOS in-app browsers (Instagram, Facebook, LinkedIn, Gmail, X, TikTok…) | warned before Join ("may block the motion sensors… open in Safari", Copy link); Join is still tried; if motion is refused or never arrives: "Tap ••• → Open in Safari", reported `inapp` | E (Instagram UA) | which apps really pass motion through (unknown: some do) |
| Android Chrome, old (96, Galaxy S10+ / Android 12) and new | works, no prompt; a reload rejoins by itself with the same number | E (Chrome 96 UA) | **the S10+ itself** (it wasn't connected by USB during this work) |
| Samsung Internet (16+) | works; without motion: "⋮ → Settings → Sites and downloads → Site permissions → Motion sensors → Allow" | E | Samsung Internet 16 on the S10+ |
| Firefox Android | works when Firefox delivers motion; if not: "Open the link in Chrome", reported `no-motion` | E (no events) | whether Firefox Android fires `devicemotion` on the S10+ |
| Android in-app browsers / WebViews (LinkedIn, Instagram, Google app) | warned before Join with an **Open in Chrome** button (an `intent://` link); joins if motion arrives; else the same message, reported `inapp` | E (LinkedIn WebView UA) | the intent link opening Chrome |
| Pulse Android app (`dev.pulse.app`) | no in-app warning (recognised by `PulseApp/` in its UA or `window.PulseNative`); `native.ts` untouched | E (UA only) | a build of the app against this page |
| Phone with no gyroscope | streams from `accelerationIncludingGravity` minus a running mean | E | — |
| No motion API at all | "This browser doesn't give pages motion data. Open the link in Chrome/Safari", reported `no-sensor` | E | — |
| Laptop / desktop browser | "this looks like a laptop or desktop. Open the link on a phone", reported `no-motion` | E | — |
| Page opened over plain http (LAN IP) | warned on load ("plain http… scan the QR code: it starts with https://"), Join disabled, reported `insecure` | E | — |
| Very old browser (no ES modules) or a script that fails to load | "This browser is too old / didn't finish loading" instead of a dead button | — | — |
| Reconnect: socket drops, tunnel blips, phone sleeps | same id, same name, same number in the row; on return to the page a silent socket is replaced at once | E (offline 3 s), G (drop + rejoin while another phone joins) | real screen lock / app switch on iOS |
| Clock sync on a slow link (300 ms RTT, phone 4 s ahead) | offset found within ±60 ms; a congested burst no longer replaces a good offset | G | — |

Bundle (gzip): phone page JS ≈ 21 KB + 7 KB of shared chunks, CSS ≈ 3 KB, the debug panel (≈ 1.5 KB) only with `?debug=1`. About 1–2 s on 3G, mostly round trips; lazy-loading the mesh code would save ≈ 5 KB, not worth the risk. The phone build targets Chrome 87 / Safari 14 / Firefox 90 (Vite 7's default is Chrome 107 / Safari 16, newer than the S10+'s Chrome 96 and iOS 15).
