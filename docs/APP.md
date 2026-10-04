# The Pulse Android app (demo)

A thin native shell around the Pulse phone page, for showing what a native app adds over the web page. Not for the Play Store: debug-signed, installed over USB.

The web page stays the only UI. The app loads it from the Pulse server in a WebView and adds three things a browser can't give a page:

| | Web page | App |
|---|---|---|
| Hear the venue's Bluetooth boards | needs a hidden Chrome flag (scan) or one chooser pop-up per board (connect) | starts by itself after Join, one Android prompt on first launch |
| Be heard by the boards | impossible (a page can't advertise) | advertises the session as a BLE beacon |
| Screen off | the page stops | a foreground service keeps Bluetooth and the motion stream going |

Code: `android/` (Kotlin, one activity, one service, ~1,000 lines), `web/phone/src/native.ts` (the page's side), two lines in `web/phone/src/main.ts`.

## What it does

1. **First launch.** One screen: the server address (default `http://localhost:8080/`), what Android is about to ask and why, Continue. Then Android's own prompt, then the page.
2. **The page** runs as in a browser: Join, motion stream, WebRTC mesh, guidance arrow, vibration. The screen stays on while the app is in front, and the app shows over the lock screen (a warning must be readable without unlocking).
3. **After Join** the page finds `window.PulseNative` and, with no button and no pop-up, starts the Bluetooth scan and the Bluetooth advert. The live screen gets one line: `App mode: Bluetooth on · hearing PULSE-B -61 dBm · visible to the boards`.
4. **A notification**, "Pulse is running for this event", is there the whole time anything runs, with a Stop button.
5. **Screen off** (or the app in the background): the page is paused and native code takes over the session: same session id, same `/ws/phone` protocol, motion sampled from the sensors and sent as the same `m` messages, beacon reports sent as the same `beacons` messages. If the server says the phone is in danger, the phone buzzes and shows a "Crowd danger near you" notification. Screen on again: native stops, the page reconnects and carries on. The line then also says how many readings were sent with the screen off.

To change the server later: long-press the app icon → **Change server**. A page that fails to load also brings the first screen back with the reason.

## Permissions

| Android asks | When | Why | Without it |
|---|---|---|---|
| "Allow Pulse to find, connect to, and determine the relative position of nearby devices?" (Android 12+) | first launch, after Continue | Bluetooth scan + advert | everything else works; the line says "Bluetooth not allowed — tap here to allow it" |
| Notifications (Android 13+ only) | first launch | the running notification, danger warnings | the service still runs; Android hides the notification |
| Location | only if the venue uses GPS and the page asks | the page's GPS position | the page falls back to tap-your-spot |

Scanning is declared `neverForLocation`, so on Android 12+ Bluetooth needs no location permission. On Android 8–11 a Bluetooth scan requires the location permission, so there it is asked at first launch. No body sensors, camera, microphone, contacts or background location. Cleartext http is allowed to `localhost`/`127.0.0.1` only; any other server must be https.

## The bridge: `window.PulseNative`

Injected into the page with `addJavascriptInterface`; the page feature-detects it (`web/phone/src/native.ts`), so the same page still works in a browser. Calls answer with JSON text.

| Call | Does |
|---|---|
| `scanBeacons(on: boolean): string` | Start/stop the BLE scan for adverts whose local name starts with `PULSE-`. Returns `status()`. |
| `advertise(id: string): string` | Advertise the Pulse session id (`""` stops). Returns `status()`; `advertising` turns true a moment later, with a `status` event. |
| `status(): string` | See below. |
| `setSession(hello: string)` | The page's `hello` message as JSON (session id + position), kept in memory only, so native code can carry the session while the screen is off. `""` = left. The page refreshes it every 5 s. |
| `requestPermissions()` | Show the Bluetooth prompt again (or the app's Android settings page once Android won't ask any more). |

Events arrive as `window.dispatchEvent(new CustomEvent('pulsenative', {detail}))`:

```jsonc
{ "type": "beacons", "seen": [ { "name": "PULSE-B", "rssi": -61.3, "n": 14 } ] }   // about once a second while scanning
{ "type": "status", ...status }                                                    // whenever something changed
```

The `beacons` event is exactly the message the web scanner builds (`web/shared/beacons.ts`); the page puts it on its WebSocket unchanged. Smoothing is the web scanner's: median of the adverts of the last 2 s, then an exponential moving average (0.35); a board not heard for 5 s is dropped; at most 16 boards; RSSI outside −110…−20 ignored.

```jsonc
// status()
{ "app": true, "version": "0.1", "sdk": 31,
  "bluetooth": { "supported": true, "on": true, "canScan": true, "canAdvertise": true },
  "permissions": { "bluetooth": true, "location": false, "notifications": true },
  "scanning": true, "scanError": "", "adverts": 412,
  "advertising": true, "advertId": "2e5b80d0", "advertError": "",
  "service": true, "joined": true, "screenOffMode": "native",
  "native": { "active": false, "connected": false, "sent": 0 },          // the native stream right now
  "screenOff": { "times": 2, "seconds": 331, "sent": 3277 } }            // all stretches with the page away
```

## The advert (phone → boards)

Legacy advertising, non-connectable, no device name, low-latency mode (about 100 ms interval), high TX power. One AD structure:

```
manufacturer-specific data, company id 0xFFFF
payload (12 bytes, ASCII): "PLS1" + the first 8 hex characters of the Pulse session id
e.g. session 2e5b80d0-45df-…  →  FF FF 50 4C 53 31 32 65 35 62 38 30 64 30
```

16 bytes on air, well inside the 31-byte limit. `0xFFFF` is the Bluetooth test company id, the same one the zone lights use for their own `PLS` + zone-tag marker. The boards match `FF FF "PLS1"` and report the 8 characters with the RSSI they measured; the server matches them to the session whose id starts with them. Eight hex characters are 32 bits of a random id: enough to tell a crowd's phones apart, and nothing a bystander can use.

## The scan (boards → phone)

Low-latency scan with filters: manufacturer data `0xFFFF "PLS…"`, or a name `PULSE-A` … `PULSE-H`, `PULSE-S`. Filters are what lets Android keep the scan running with the screen off (an unfiltered scan is stopped). Android can't filter a name by prefix, hence the list; the callback checks the `PULSE-` prefix again, so nothing else is ever reported. A board with another tag is still heard if it carries the `PLS` manufacturer marker (the zone lights do). The scan is restarted every 20 minutes because Android downgrades a scan that runs longer than 30.

## Screen off: what was measured

Android 12 emulator (API 31, x86_64), the app joined to the local server through `adb reverse`, motion messages per second counted at `GET /api/node/<id>`:

| Mode | Screen on | Screen off |
|---|---|---|
| Page left alone (`screenOff=page`) | 10.0 /s | **0 /s**: the socket stays open, the motion stream stops |
| Native takeover (`screenOff=native`, the default) | 10.0 /s | **9.9 /s** for 5 minutes, 3,012 readings, one connection, no gap; the page took the session back within a few seconds of screen-on |
| WebView told it is still visible (`screenOff=keepvisible`, experimental) | 10.0 /s | 10.0 /s for 20 s (not tested longer) |

So: a WebView's sensors stop when it isn't visible, even with a foreground service holding the process. That is why motion moves into native code for the screen-off stretch.

**Not yet measured on the real phone** (it wasn't connected while this was built): the same table on the Galaxy S10+, boards actually heard, the advert actually on air, and a longer screen-off run. The emulator has no real Bluetooth radio: scan and advert started without error there, and that is all it can show. See "Checking on the phone" below.

What works with the screen off, and what doesn't:

- **Works:** the motion stream (native), beacon reports (native), the advert, danger buzz + notification.
- **Paused:** the page itself, so the WebRTC mesh (this phone drops out of its neighbours' links until the screen is back on), the compass heading in `m` messages, and GPS updates (the last position stands; background GPS would need another permission and is not asked for).
- **Hand-over gaps:** about 1 s at screen-off (new connection + clock sync), a few seconds at screen-on (the page reconnects on its own back-off timer).
- **`keepvisible`** keeps the whole page running, mesh included, by never telling the WebView its window went away. It worked on the emulator; real phones may throttle it, and it relies on behaviour Chromium doesn't promise. Worth a try on the S10+; not the default.

Native motion matches the page's numbers: Chrome's `devicemotion` on Android is built from the same sensors (`acceleration` = linear acceleration, `accelerationIncludingGravity` − `acceleration` = the gravity sensor, `rotationRate` = the gyroscope in deg/s). Every 100 ms: mean acceleration per axis (3 decimals), largest rotation rate, and `g` (unit down vector, 2 decimals) first thing on each connection, when it turned more than 3°, and once a second.

## Build and install

Gradle can't build reliably from the `\\wsl.localhost` path, so build from a copy on the Windows disk.

Toolchain (all in the user profile, nothing system-wide; set per shell):

```powershell
$env:JAVA_HOME    = "C:\Users\drive\android-toolchain\jdk\jdk-17.0.20.1+1"   # Temurin 17
$env:ANDROID_HOME = "C:\Users\drive\android-toolchain\sdk"                    # platform 34, build-tools 34.0.0
$env:Path = "$env:JAVA_HOME\bin;$env:Path"
```

```powershell
robocopy \\wsl.localhost\ubuntu\home\driver\projects\crowd-crush\android C:\Users\drive\pulse-android /E /XD .gradle build
cd C:\Users\drive\pulse-android
Set-Content local.properties "sdk.dir=C\:\\Users\\drive\\android-toolchain\\sdk" -Encoding ascii
.\gradlew.bat assembleDebug          # → app\build\outputs\apk\debug\app-debug.apk (about 1.4 MB)

adb -s R38M700Y4CH install -r app\build\outputs\apk\debug\app-debug.apk
adb -s R38M700Y4CH shell am start -n dev.pulse.app/.MainActivity
adb -s R38M700Y4CH logcat -s PulseApp:*
```

On another machine: any JDK 17, the Android SDK with `platforms;android-34` and `build-tools;34.0.0`, and `local.properties` pointing at it. Gradle 8.7 comes through the wrapper; AGP 8.5.2, Kotlin 1.9.24, one dependency (OkHttp 4.12, the native WebSocket).

Dev setup: the phone's `localhost:8080` reaches the PC through `adb reverse tcp:8080 tcp:8081` (the mapping is lost whenever the cable is unplugged; run it again). For the demo use the https tunnel address instead.

Dev aids (intent extras):

```powershell
adb shell am start -n dev.pulse.app/.MainActivity --es url https://pulse.example/     # set the server, skip the first screen
adb shell am start -n dev.pulse.app/.MainActivity --es screenOff page                 # or native (default), keepvisible
```

### Checking on the phone

1. Install and start (above). Tap **Continue**, then **Allow** on "Allow Pulse to find, connect to, and determine the relative position of nearby devices?". Tap **Join**.
2. `logcat -s PulseApp:*` should show `joined: session <id>`, `scan started`, `advertising started: 0xFFFF "PLS1…"`, and `scan: first advert from PULSE-B, -xx dBm` for each board in range. The live screen's "App mode" line names the boards.
3. `GET /api/node/<id>` on the server: `messages` rises by about 10 per second; `beacons` appears once boards are heard.
4. Press the power button. `logcat`: `screen off: native motion stream started`, `native link open`. `messages` keeps rising by about 10 per second. Leave it a few minutes.
5. Wake the phone: `screen on: native stream stopped after N readings`; the page shows "Connected" again and the count.

## Limits

- A demo build: debug-signed, no release signing, no update path, not minified.
- Android 8+ (minSdk 26). Verified on an Android 12 emulator only so far; see above.
- The bridge is offered to whatever page the configured server serves; links to other hosts open in the browser, not in the app. Point it only at a Pulse server.
- Battery: screen-off running holds a partial wake lock (released after 6 hours at most), samples three sensors at 50 Hz, keeps a WebSocket open and a low-latency BLE scan and advert going. That is the cost of a few hours of an event, roughly what a fitness tracker app draws during a workout; not measured. An event build would use a balanced scan mode and a slower advert.
- Samsung's "sleeping apps" and battery optimisation can still stop a foreground service after long idle periods. The app doesn't ask to be exempted (that is the owner's call in Settings).
- The scan hears boards named `PULSE-A`…`PULSE-H` and `PULSE-S`, or any board carrying the `PLS` manufacturer marker.
- With the screen off there is no mesh, no compass heading and no GPS update (see above).
- Phones differ: some can't advertise at all (`advertError` says so), and RSSI varies by model by several dB, so distances from RSSI need the per-board calibration in `docs/BEACONS.md`.

## What an iPhone version would need

- A Swift app with `WKWebView` and a `WKScriptMessageHandler` bridge (same `PulseNative` shape, calls answered asynchronously), CoreBluetooth for scan and advert, CoreMotion (`CMDeviceMotion`: `userAcceleration` × 9.81, `gravity`, `rotationRate`) and `URLSessionWebSocketTask` for the screen-off stream.
- **The advert format can't be the same.** iOS doesn't let an app advertise manufacturer data: only a local name and service UUIDs, and in the background not even those in the clear (they move to an Apple-specific overflow area only other iPhones can read). The id would go in the local name (foreground) or a GATT characteristic the boards read after connecting, which is today's connect mode. The boards' scanner would need a second format.
- Background: the `bluetooth-central`/`bluetooth-peripheral` background modes keep scanning and advertising alive, with scan results coalesced and slowed. Motion in the background has no mode of its own; it would ride on the Bluetooth or location mode keeping the app awake, which App Review looks at closely.
- In return, iPhones give RSSI for any advert without a permission tied to location, and newer ones have UWB (Nearby Interaction) for real ranging against a UWB anchor.
- An Apple developer account ($99/yr), a Mac to build, TestFlight or a registered device to install.
