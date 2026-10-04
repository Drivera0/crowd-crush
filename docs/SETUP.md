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
| `SIGN_URL` | after step 4: the IPs your boards print, or `serial:auto` for the sign plugged in by USB |
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

**Wi-Fi:** venue Wi-Fi usually blocks device-to-device traffic and is often 5 GHz only. Put the laptop and the boards on your **phone's hotspot** (2.4 GHz / "maximize compatibility" on iPhone). Put the SSID and password in each board's `arduino_secrets.h` (copy from `arduino_secrets.h.example`).

**Arduino Uno R4 WiFi, one command on Windows:** fill in `arduino/sign/arduino_secrets.h`, plug the board in, then run `pwsh scripts/flash-sign.ps1`. It finds the board, compiles and uploads with the Arduino IDE's bundled `arduino-cli`, reads the sign's IP from the serial port and writes `SIGN_URL` into `.env`. Already flashed? `pwsh scripts/flash-sign.ps1 -NoUpload` just reads the IP again.

**Arduino Uno R4 WiFi, by hand** (`arduino/sign/sign.ino`): Arduino IDE → Boards Manager → install **Arduino UNO R4 Boards** → board "Arduino UNO R4 WiFi" → pick the port → Upload. Open Serial Monitor at 115200 and copy `SIGN_URL=http://…`. It shows the worst zone: heartbeat / `!` / flashing arrow + STOP.

**ESP32 DevKit V1 ×2** (`arduino/zone-light/zone-light.ino`), one per zone: Boards Manager → install **esp32 by Espressif** → board "ESP32 Dev Module" → Upload (hold **BOOT** if it sticks at "Connecting…"; on Windows you may need the CP210x USB driver). The onboard blue LED works without wiring; optional LEDs go on GPIO 25/26/27 and a buzzer on 14.

Then:
```
SIGN_URL=http://<uno ip>,A=http://<esp32 #1 ip>,B=http://<esp32 #2 ip>
```
`./bin/pulse -check` should list all three. The dashboard's **Test alert** flashes them.

**Sign over USB (no Wi-Fi):** plug the Uno R4 into the laptop running the server and set
```
SIGN_URL=serial:auto                       # or serial:/dev/cu.usbmodem1101 (Mac), serial:COM7 (Windows)
SIGN_URL=serial:auto,A=http://<esp32 #1 ip>  # mixes with Wi-Fi boards; A=serial:… pins a zone
```
`serial:auto` picks the first Arduino (USB vendor 0x2341) or CP210x ESP32 (0x10C4). The server opens the port at 115200 baud with DTR on (the R4 ignores the sketch's serial input without it), waits 1.5 s, then writes one line per level change, `L <calm|yellow|red> <zone>`. Every hardware check it sends `S` and the sign answers with its `/pulse` JSON line. Unplug it and the server reopens the port when it comes back, then re-sends the current level. The sign firmware needs no Wi-Fi for this: if Wi-Fi doesn't connect within 15 s it shows a small **USB** while calm and keeps retrying Wi-Fi in the background. Close the Arduino IDE's Serial Monitor first, because only one program can hold the port.

- **WSL can't see USB ports** unless you attach them with `usbipd`, so the server running inside WSL won't find the board. That's expected: USB mode is for the Mac (or a native Windows/Linux build) at the demo.
- **ESP32 zone lights:** `serial:auto` finds them, but `zone-light.ino` doesn't read serial commands yet. Use their Wi-Fi URLs.

## 5. Before judging

Record real runs (dashboard → Record run) with labels such as `wave-push-end` and `dance-jumping`; `go test ./...` checks each run against its label. Rehearse with **Replay** as the fallback.
