// Pulse warning sign — Arduino Uno R4 WiFi.
//
// Joins Wi-Fi and runs a tiny HTTP server:
//   GET /level?v=calm|yellow|red&zone=B
// calm   = slow heartbeat dot
// yellow = steady "!"
// red    = flashing arrow, alternating with "STOP"
//   GET /pulse → status JSON for the dashboard / server discovery:
//   {"kind":"sign","level":"calm","zone":"B","rssi":-58,"uptime":123,"wifi":true,
//    "fw":"1a2b3c4 2026-10-04","ssid":"HomeWiFi","ip":"192.168.1.88"}
//   (rssi = Wi-Fi signal in dBm, uptime in seconds, fw = firmware build id:
//   content hash of this sketch + build date, written by scripts/boards.sh;
//   also "radio":"0.6.0" (radio module firmware), "mode":"wifi"|"beacon",
//   "beacon" (asked for with B 1), "ble" (advertising), "name":"PULSE-S" while
//   advertising, "bleErr" (why beacon mode fell back to Wi-Fi))
//
// The same commands work over the USB cable (Serial, 115200 baud), one per
// line, so the sign needs no Wi-Fi when it's plugged into the laptop
// (SIGN_URL=serial:auto on the server):
//   L <calm|yellow|red> [zone]   set the level, exactly like /level
//   S                            reply with one /pulse-shaped JSON line
//   W <ssid><TAB><password>      save a Wi-Fi network in flash and join it (tried before the
//   W <ssid> <password>          built-in ones; the password is never printed; without a tab
//   W -                          the last space splits them). "W -" forgets it.
//   B 1 | B 0 | B                Bluetooth beacon mode on / off / report (below)
//
// Wi-Fi is optional: if it hasn't connected within 15 s the sign carries on
// over USB only (a small "USB" shows while calm) and keeps retrying Wi-Fi in
// the background, going through its networks in turn: the one saved over
// USB, then SECRET_SSID, SECRET_SSID2, SECRET_SSID3 (the last two optional).
//
// While calm the matrix shows who is talking to it:
//   two dots, double flash   = the server is in touch (USB or Wi-Fi, last 20 s)
//   one dot, heartbeat       = on Wi-Fi, waiting for the server
//   three dots               = still trying Wi-Fi (first 15 s)
//   "USB"                    = no Wi-Fi: plug it into the laptop running the server
//
// Beacon mode (a runtime switch kept in EEPROM, no reflash): "B 1" over USB
// reboots the sign into a Bluetooth beacon advertising "PULSE-S" with
// manufacturer data FF FF 'P' 'L' 'S' 'S' (the zone lights' format), so the
// zone lights and phones can use it as a third position anchor. "B 0" reboots
// it back to Wi-Fi. The R4's Wi-Fi and Bluetooth share one radio module (the
// ESP32-S3, which is also the USB bridge) and run one at a time here, so a
// beacon sign has no Wi-Fi: drive it over USB (SIGN_URL=serial:auto). Level
// commands, the matrix and S keep working. Switching restarts the radio
// module, so the USB port drops for a few seconds and comes back. Needs radio
// firmware 0.2.0 or newer (S reports it as "radio"). If Bluetooth fails to
// start or hangs (a 5 s watchdog catches a hang), the sign falls back to
// Wi-Fi + USB, says why in S ("bleErr") and stays there until the next "B 1".
//   B 1 | B 0 | B                beacon on / off (each reboots) / report the mode
// Needs the ArduinoBLE library (arduino-cli lib install ArduinoBLE).
//
// Flash it with scripts/boards.sh flash (Mac / Linux) or pwsh scripts/flash-sign.ps1
// (Windows): both stamp the build id. Wi-Fi is optional: copy
// arduino_secrets.h.example to arduino_secrets.h and fill it in, or set a
// network later over USB (scripts/boards.sh wifi). On a table, plug it into
// the laptop and use SIGN_URL=serial:auto; over Wi-Fi the IP is printed on the
// Serial Monitor (115200 baud): SIGN_URL=http://<that ip>.

#ifndef BEACON_NAME
#define BEACON_NAME "PULSE-S"
#endif

#include <WiFiS3.h>
#include <EEPROM.h>
#include <ArduinoBLE.h>
#include <WDT.h>
#include "WiFiCommands.h"
#include "Arduino_LED_Matrix.h"
#include "arduino_secrets.h"
// pulse_build.h is written by scripts/boards.sh / the flash scripts: #define PULSE_FW "<hash> <date>".
#if __has_include("pulse_build.h")
#include "pulse_build.h"
#endif
#ifndef PULSE_FW
#define PULSE_FW "dev"
#endif

// Wi-Fi networks, tried in turn after the one saved over USB (W command).
// SECRET_SSID2/3 are optional: a secrets file without them still builds.
#ifndef SECRET_SSID
#define SECRET_SSID ""
#define SECRET_PASS ""
#endif
#if defined(SECRET_SSID2) && !defined(SECRET_PASS2)
#define SECRET_PASS2 ""
#endif
#if defined(SECRET_SSID3) && !defined(SECRET_PASS3)
#define SECRET_PASS3 ""
#endif
struct WifiNet { const char* ssid; const char* pass; };
const WifiNet BUILTIN_NETS[] = {
  {SECRET_SSID, SECRET_PASS},
#ifdef SECRET_SSID2
  {SECRET_SSID2, SECRET_PASS2},
#endif
#ifdef SECRET_SSID3
  {SECRET_SSID3, SECRET_PASS3},
#endif
};
const int BUILTIN_NET_COUNT = sizeof BUILTIN_NETS / sizeof BUILTIN_NETS[0];

// The network saved over USB, in the R4's EEPROM (emulated in data flash).
struct SavedNet {
  uint32_t magic;
  char ssid[33];
  char pass[64];
};
const uint32_t SAVED_MAGIC = 0x50554C53; // "PULS"
SavedNet saved = {0, "", ""};

// Optional: a buzzer or big LED on this pin turns on while red.
const int ALARM_PIN = 7;

ArduinoLEDMatrix matrix;
WiFiServer server(80);

// Beacon mode, kept in EEPROM after the saved network.
struct ModeRec {
  uint32_t magic;
  uint8_t beacon;   // 1 = run as a Bluetooth beacon (B 1)
  uint8_t failed;   // Bluetooth didn't start: stay on Wi-Fi until the next B 1
  uint8_t trying;   // set while Bluetooth starts; still set at boot = it hung (watchdog reset)
  uint8_t espDirty; // the radio module may still run Bluetooth: restart it before using it
  char err[40];     // why it failed
};
const int MODE_ADDR = 128;
const uint32_t MODE_MAGIC = 0x504C4243; // "PLBC"
ModeRec mode = {MODE_MAGIC, 0, 0, 0, 0, ""};
bool beaconMode = false; // running as a beacon now
bool bleUp = false;      // advertising
char radioFw[16] = "";
const unsigned long BLE_SETTLED_MS = 20000; // healthy this long: a later reset isn't a start failure
bool bleSettled = false;
unsigned long lastBlePoll = 0;

enum Level { CALM, YELLOW, RED };
enum Route { NOT_FOUND, LEVEL, PULSE };
Level level = CALM;
char zone[8] = "";
unsigned long lastChange = 0;
// Last time the server talked to the sign (HTTP request or USB command).
unsigned long lastContact = 0;
const unsigned long CONTACT_FRESH_MS = 20000;
bool inTouch() { return lastContact && millis() - lastContact < CONTACT_FRESH_MS; }

// 8 rows x 12 cols frames.
uint8_t blank[8][12] = {0};

uint8_t heart[8][12] = {
  {0,0,0,0,0,0,0,0,0,0,0,0},
  {0,0,0,0,0,0,0,0,0,0,0,0},
  {0,0,0,0,0,0,0,0,0,0,0,0},
  {0,0,0,0,0,1,1,0,0,0,0,0},
  {0,0,0,0,0,1,1,0,0,0,0,0},
  {0,0,0,0,0,0,0,0,0,0,0,0},
  {0,0,0,0,0,0,0,0,0,0,0,0},
  {0,0,0,0,0,0,0,0,0,0,0,0},
};

uint8_t bang[8][12] = {
  {0,0,0,0,0,1,1,0,0,0,0,0},
  {0,0,0,0,0,1,1,0,0,0,0,0},
  {0,0,0,0,0,1,1,0,0,0,0,0},
  {0,0,0,0,0,1,1,0,0,0,0,0},
  {0,0,0,0,0,1,1,0,0,0,0,0},
  {0,0,0,0,0,0,0,0,0,0,0,0},
  {0,0,0,0,0,1,1,0,0,0,0,0},
  {0,0,0,0,0,1,1,0,0,0,0,0},
};

uint8_t arrow[8][12] = {
  {0,0,0,0,0,0,1,0,0,0,0,0},
  {0,0,0,0,0,0,1,1,0,0,0,0},
  {1,1,1,1,1,1,1,1,1,0,0,0},
  {1,1,1,1,1,1,1,1,1,1,0,0},
  {1,1,1,1,1,1,1,1,1,1,0,0},
  {1,1,1,1,1,1,1,1,1,0,0,0},
  {0,0,0,0,0,0,1,1,0,0,0,0},
  {0,0,0,0,0,0,1,0,0,0,0,0},
};

// "STOP" squeezed into 12 columns (3 px per letter).
uint8_t stop_[8][12] = {
  {0,0,0,0,0,0,0,0,0,0,0,0},
  {0,0,0,0,0,0,0,0,0,0,0,0},
  {1,1,1,1,1,1,1,1,1,1,1,1},
  {1,0,0,0,1,0,1,0,1,1,0,1},
  {1,1,1,0,1,0,1,0,1,1,1,1},
  {0,0,1,0,1,0,1,0,1,1,0,0},
  {1,1,1,0,1,0,1,1,1,1,0,0},
  {0,0,0,0,0,0,0,0,0,0,0,0},
};

// Two dots: the calm heartbeat while the server is in touch with the sign.
uint8_t linked[8][12] = {
  {0,0,0,0,0,0,0,0,0,0,0,0},
  {0,0,0,0,0,0,0,0,0,0,0,0},
  {0,0,0,0,0,0,0,0,0,0,0,0},
  {0,0,1,1,0,0,0,0,1,1,0,0},
  {0,0,1,1,0,0,0,0,1,1,0,0},
  {0,0,0,0,0,0,0,0,0,0,0,0},
  {0,0,0,0,0,0,0,0,0,0,0,0},
  {0,0,0,0,0,0,0,0,0,0,0,0},
};

// Three dots: still trying Wi-Fi (first 15 s after boot or a lost connection).
uint8_t waiting[8][12] = {
  {0,0,0,0,0,0,0,0,0,0,0,0},
  {0,0,0,0,0,0,0,0,0,0,0,0},
  {0,0,0,0,0,0,0,0,0,0,0,0},
  {0,0,0,0,0,0,0,0,0,0,0,0},
  {0,0,0,0,0,0,0,0,0,0,0,0},
  {0,1,1,0,0,1,1,0,0,1,1,0},
  {0,1,1,0,0,1,1,0,0,1,1,0},
  {0,0,0,0,0,0,0,0,0,0,0,0},
};

// "USB" in 3-px letters: shown while calm and Wi-Fi is down.
uint8_t usb[8][12] = {
  {0,0,0,0,0,0,0,0,0,0,0,0},
  {1,0,1,0,1,1,1,0,1,1,0,0},
  {1,0,1,0,1,0,0,0,1,0,1,0},
  {1,0,1,0,1,1,1,0,1,1,0,0},
  {1,0,1,0,0,0,1,0,1,0,1,0},
  {1,1,1,0,1,1,1,0,1,1,0,0},
  {0,0,0,0,0,0,0,0,0,0,0,0},
  {0,0,0,0,0,0,0,0,0,0,0,0},
};

// Wi-Fi state. WiFi.begin blocks until it connects or times out, so it gets
// a short timeout and the connection is polled from loop(): the sign keeps
// answering USB commands while Wi-Fi comes and goes.
const unsigned long WIFI_GRACE_MS = 15000;     // after this, show "USB" and stop waiting
const unsigned long WIFI_RETRY_FAST_MS = 5000; // re-begin while in the grace window
const unsigned long WIFI_RETRY_MS = 30000;     // re-begin after that
bool wifiUp = false;
unsigned long wifiSince = 0;  // start of the current attempt (boot or loss)
unsigned long lastBegin = 0;
unsigned long lastPoll = 0;

void saveMode() { EEPROM.put(MODE_ADDR, mode); }

// restartRadio restarts the ESP32-S3 radio module (it is also the USB bridge,
// so the USB port drops and comes back) and gives it time to boot. Clears
// espDirty first, so a board that gets reset along with it can't loop.
void restartRadio() {
  mode.espDirty = 0;
  saveMode();
  Serial.println("radio: restarting the radio module (USB drops for a moment)");
  Serial.flush();
  delay(50);
  std::string res = "";
  modem.begin();
  modem.write_nowait(std::string(PROMPT(_RESET)), res, "%s", CMD(_RESET));
  delay(3000);
}

// versionAtLeast compares "0.4.1"-style versions.
bool versionAtLeast(const char* v, int a, int b, int c) {
  int x = 0, y = 0, z = 0;
  if (sscanf(v, "%d.%d.%d", &x, &y, &z) < 2) return false;
  if (x != a) return x > a;
  if (y != b) return y > b;
  return z >= c;
}

// bleFail records why Bluetooth didn't start and reboots into Wi-Fi + USB mode.
void bleFail(const char* why) {
  mode.trying = 0;
  mode.failed = 1;
  strncpy(mode.err, why, sizeof mode.err - 1);
  mode.err[sizeof mode.err - 1] = 0;
  saveMode();
  Serial.print("ble: ");
  Serial.print(why);
  Serial.println("; rebooting into Wi-Fi + USB mode (B 1 tries again)");
  Serial.flush();
  delay(100);
  NVIC_SystemReset();
}

// beginBeacon starts a non-connectable advert: the name plus the Pulse
// manufacturer marker. A 5 s watchdog turns a hang into a reset; the
// "trying" flag in EEPROM then sends the next boot to Wi-Fi mode.
void beginBeacon() {
  if (!versionAtLeast(radioFw, 0, 2, 0)) {
    char why[40];
    snprintf(why, sizeof why, "radio firmware %s too old (need 0.2.0)", radioFw[0] ? radioFw : "?");
    bleFail(why);
  }
  mode.trying = 1;
  mode.espDirty = 1;
  saveMode();
  WDT.begin(5000);
  if (!BLE.begin()) bleFail("BLE.begin failed");
  WDT.refresh();
  static const uint8_t marker[] = {0xFF, 0xFF, 'P', 'L', 'S', 'S'};
  BLE.setLocalName(BEACON_NAME);
  BLE.setDeviceName(BEACON_NAME);
  BLE.setManufacturerData(marker, sizeof marker);
  BLE.setConnectable(false);
  BLE.setAdvertisingInterval(160); // 100 ms
  bleUp = BLE.advertise();
  WDT.refresh();
  if (!bleUp) bleFail("advertise failed");
  Serial.print("ble: advertising as ");
  Serial.println(BEACON_NAME);
}

// pollBeacon feeds the watchdog (it can't be stopped once started) and lets ArduinoBLE handle events.
void pollBeacon() {
  WDT.refresh();
  unsigned long now = millis();
  if (!bleSettled && now > BLE_SETTLED_MS) {
    bleSettled = true;
    mode.trying = 0;
    saveMode();
    WDT.refresh();
  }
  if (now - lastBlePoll >= 200) {
    lastBlePoll = now;
    BLE.poll();
  }
}

int netIdx = -1;          // network being tried: 0 = the saved one (if any), then the built-in ones
const char* curSsid = ""; // name of the network being tried or joined

int netCount() { return (saved.magic == SAVED_MAGIC && saved.ssid[0] ? 1 : 0) + BUILTIN_NET_COUNT; }

// The i-th network to try; false = none there.
bool netAt(int i, const char*& ssid, const char*& pass) {
  if (saved.magic == SAVED_MAGIC && saved.ssid[0]) {
    if (i == 0) { ssid = saved.ssid; pass = saved.pass; return true; }
    i--;
  }
  if (i < 0 || i >= BUILTIN_NET_COUNT) return false;
  ssid = BUILTIN_NETS[i].ssid;
  pass = BUILTIN_NETS[i].pass;
  return true;
}

const unsigned long WIFI_NET_DWELL_MS = 12000; // keep re-trying one network this long before moving on
unsigned long netStart = 0;                    // when the current network was first tried

// beginWiFi (re)starts joining: the same network again, or after WIFI_NET_DWELL_MS
// the next usable one (skipping empty and placeholder names).
void beginWiFi() {
  unsigned long now = millis();
  lastBegin = now;
  int n = netCount();
  const char *ssid, *pass;
  bool same = netIdx >= 0 && now - netStart < WIFI_NET_DWELL_MS && netAt(netIdx, ssid, pass) && ssid[0];
  for (int k = 0; !same && k < n; k++) {
    netIdx = (netIdx + 1) % n;
    if (!netAt(netIdx, ssid, pass) || !ssid[0] || strcmp(ssid, "your-wifi") == 0) continue;
    netStart = now;
    same = true;
  }
  if (!same) {
    curSsid = "";
    return;
  }
  curSsid = ssid;
  Serial.print("Connecting to ");
  Serial.println(ssid);
  WiFi.begin(ssid, pass);
  lastBegin = millis();
}

// pollWiFi notices Wi-Fi coming up or dropping and retries while it's down.
void pollWiFi() {
  unsigned long now = millis();
  if (now - lastPoll < 500) return;
  lastPoll = now;
  bool connected = WiFi.status() == WL_CONNECTED;
  // The R4 sometimes reports 0.0.0.0 for a moment after connecting.
  if (connected && WiFi.localIP() == IPAddress(0, 0, 0, 0)) connected = false;
  if (connected && !wifiUp) {
    wifiUp = true;
    server.begin();
    Serial.print("Sign ready: SIGN_URL=http://");
    Serial.println(WiFi.localIP());
  } else if (!connected && wifiUp) {
    wifiUp = false;
    wifiSince = now;
    Serial.println("Wi-Fi lost; still listening on USB");
    netStart = now; // the network that just dropped gets another WIFI_NET_DWELL_MS first
    beginWiFi();
  } else if (!connected) {
    bool grace = now - wifiSince < WIFI_GRACE_MS;
    if (now - lastBegin >= (grace ? WIFI_RETRY_FAST_MS : WIFI_RETRY_MS)) beginWiFi();
  }
}

// usbOnly: Wi-Fi has been down for longer than the grace period (always, in beacon mode).
bool usbOnly() { return beaconMode || (!wifiUp && millis() - wifiSince >= WIFI_GRACE_MS); }

void setup() {
  Serial.begin(115200);
  pinMode(ALARM_PIN, OUTPUT);
  matrix.begin();
  matrix.renderBitmap(waiting, 8, 12);
  Serial.print("Pulse sign, firmware ");
  Serial.println(PULSE_FW);
  EEPROM.get(MODE_ADDR, mode);
  if (mode.magic != MODE_MAGIC) {
    memset(&mode, 0, sizeof mode);
    mode.magic = MODE_MAGIC;
  }
  mode.err[sizeof mode.err - 1] = 0;
  if (mode.beacon && mode.trying) {
    // The last start of Bluetooth never finished: the watchdog reset the board.
    mode.trying = 0;
    mode.failed = 1;
    strcpy(mode.err, "Bluetooth start hung (watchdog)");
    saveMode();
  }
  beaconMode = mode.beacon && !mode.failed;
  // Bluetooth may still run on the radio module (beacon before, or a reset in beacon mode): start it clean.
  if (mode.espDirty) restartRadio();
  // The radio module's firmware version (Bluetooth beacon mode needs 0.2.0 or newer).
  strncpy(radioFw, WiFi.firmwareVersion(), sizeof radioFw - 1);
  Serial.print("radio firmware ");
  Serial.println(radioFw);
  if (beaconMode) {
    beginBeacon();
    return;
  }
  if (mode.beacon && mode.failed) {
    Serial.print("ble: beacon mode is on but Bluetooth failed (");
    Serial.print(mode.err);
    Serial.println("); running Wi-Fi + USB. B 1 tries again, B 0 turns it off.");
  }
  EEPROM.get(0, saved);
  if (saved.magic != SAVED_MAGIC) saved.ssid[0] = 0;
  saved.ssid[sizeof saved.ssid - 1] = 0;
  saved.pass[sizeof saved.pass - 1] = 0;
  WiFi.setTimeout(1000);
  wifiSince = millis();
  beginWiFi();
}

const char* levelName() { return level == RED ? "red" : level == YELLOW ? "yellow" : "calm"; }

// setLevel applies a level word ("red", "yellow", anything else = calm) and
// a zone label (up to 7 chars, stops at a space or '&'), from HTTP or USB.
void setLevel(const char* lv, const char* z) {
  if (strncmp(lv, "red", 3) == 0) level = RED;
  else if (strncmp(lv, "yellow", 6) == 0) level = YELLOW;
  else level = CALM;
  int i = 0;
  if (z) {
    for (; z[i] && i < 7; i++) {
      if (z[i] == ' ' || z[i] == '&' || z[i] == '\r' || z[i] == '\n') break;
      zone[i] = z[i];
    }
  }
  zone[i] = 0;
  if (strcmp(zone, "-") == 0) zone[0] = 0;
  lastChange = millis();
  Serial.print("level ");
  Serial.print(level);
  Serial.print(" zone ");
  Serial.println(zone);
}

// statusJSON writes the /pulse body (also the reply to "S" over USB).
void statusJSON(char* body, size_t n) {
  // zone holds only what came after "zone=" up to a space or '&'; strip quotes/backslashes for JSON.
  char z[8];
  int j = 0;
  for (int i = 0; zone[i] && j < 7; i++)
    if (zone[i] != '"' && zone[i] != '\\') z[j++] = zone[i];
  z[j] = 0;
  char ssid[34];
  j = 0;
  for (int i = 0; curSsid[i] && j < 33; i++)
    if (curSsid[i] != '"' && curSsid[i] != '\\' && (unsigned char)curSsid[i] >= 0x20) ssid[j++] = curSsid[i];
  ssid[j] = 0;
  char ip[16] = "";
  if (wifiUp) {
    IPAddress a = WiFi.localIP();
    snprintf(ip, sizeof ip, "%u.%u.%u.%u", a[0], a[1], a[2], a[3]);
  }
  // radio: the radio module's firmware; mode: what runs now ("beacon" = Bluetooth, no Wi-Fi);
  // beacon: what was asked for (B 1 / B 0); ble: advertising as name; bleErr: why beacon mode fell back.
  char err[40];
  j = 0;
  if (mode.beacon && mode.failed)
    for (int i = 0; mode.err[i] && j < 39; i++)
      if (mode.err[i] != '"' && mode.err[i] != '\\') err[j++] = mode.err[i];
  err[j] = 0;
  snprintf(body, n,
           "{\"kind\":\"sign\",\"level\":\"%s\",\"zone\":\"%s\",\"rssi\":%d,\"uptime\":%lu,\"wifi\":%s,\"fw\":\"%s\",\"ssid\":\"%s\",\"ip\":\"%s\","
           "\"radio\":\"%s\",\"mode\":\"%s\",\"beacon\":%s,\"ble\":%s,\"name\":\"%s\",\"bleErr\":\"%s\"}",
           levelName(), z, wifiUp ? (int)WiFi.RSSI() : 0, millis() / 1000, wifiUp ? "true" : "false", PULSE_FW, ssid, ip,
           radioFw, beaconMode ? "beacon" : "wifi", mode.beacon ? "true" : "false", bleUp ? "true" : "false",
           bleUp ? BEACON_NAME : "", err);
}

// Parses "GET /level?v=red&zone=B HTTP/1.1" or "GET /pulse HTTP/1.1".
Route handleRequestLine(const String& line) {
  if (line.startsWith("GET /pulse ") || line.startsWith("GET /pulse?")) return PULSE;
  if (!line.startsWith("GET /level")) return NOT_FOUND;
  int v = line.indexOf("v=");
  if (v < 0) return NOT_FOUND;
  int z = line.indexOf("zone=");
  setLevel(line.c_str() + v + 2, z >= 0 ? line.c_str() + z + 5 : nullptr);
  return LEVEL;
}

// USB commands, one per line: "L <level> [zone]", "S" or "W <ssid> <password>".
char serialBuf[128];
int serialLen = 0;

// handleBeaconCommand: "B 1" / "B 0" saves the mode and reboots into it; "B" only reports it.
void handleBeaconCommand(char* arg) {
  while (*arg == ' ') arg++;
  if (*arg != '0' && *arg != '1') {
    Serial.print("beacon: ");
    if (beaconMode) Serial.println("on (Bluetooth " BEACON_NAME ", no Wi-Fi)");
    else if (mode.beacon) { Serial.print("on but fell back to Wi-Fi: "); Serial.println(mode.err); }
    else Serial.println("off (Wi-Fi + USB)");
    return;
  }
  bool on = *arg == '1';
  if (on == beaconMode && (on || !mode.beacon)) {
    Serial.println(on ? "beacon: already on" : "beacon: already off");
    return;
  }
  mode.beacon = on;
  mode.failed = 0;
  mode.trying = 0;
  mode.err[0] = 0;
  // Restart the radio module at boot either way: Bluetooth starts on a fresh module, Wi-Fi without Bluetooth left on.
  if (on || beaconMode) mode.espDirty = 1;
  saveMode();
  if (!on && !beaconMode) { // "B 0" after a fallback: already on Wi-Fi, just forget the request
    Serial.println("beacon: off (Wi-Fi + USB)");
    return;
  }
  Serial.println(on ? "beacon: on; rebooting into Bluetooth beacon mode (USB only, no Wi-Fi)" : "beacon: off; rebooting into Wi-Fi + USB mode");
  Serial.flush();
  if (wifiUp) WiFi.disconnect();
  delay(200);
  NVIC_SystemReset();
}

// handleWifiCommand saves (or with "-" forgets) the network from a W line and starts joining it.
void handleWifiCommand(char* arg) {
  while (*arg == ' ') arg++;
  if (strcmp(arg, "-") == 0) {
    saved.magic = 0;
    saved.ssid[0] = saved.pass[0] = 0;
    EEPROM.put(0, saved);
    Serial.println("wifi: forgot the saved network");
  } else {
    char* sep = strchr(arg, '\t');
    if (!sep) sep = strrchr(arg, ' ');
    if (sep) *sep++ = 0;
    if (!*arg || strlen(arg) > 32 || (sep && strlen(sep) > 63)) {
      Serial.println("wifi: want W <ssid><TAB><password> (ssid up to 32, password up to 63 characters)");
      return;
    }
    saved.magic = SAVED_MAGIC;
    strncpy(saved.ssid, arg, sizeof saved.ssid - 1);
    saved.ssid[sizeof saved.ssid - 1] = 0;
    strncpy(saved.pass, sep ? sep : "", sizeof saved.pass - 1);
    saved.pass[sizeof saved.pass - 1] = 0;
    EEPROM.put(0, saved);
    Serial.print("wifi: saved ");
    Serial.print(saved.ssid); // never the password
    Serial.println("; joining it now");
  }
  if (wifiUp) WiFi.disconnect();
  wifiUp = false;
  wifiSince = millis();
  netIdx = -1; // start over from the saved network
  beginWiFi();
}

void handleSerialLine(char* s) {
  while (*s == ' ') s++;
  if (s[0] == 'S' || s[0] == 'L' || s[0] == 'W' || s[0] == 'B') lastContact = millis();
  if (s[0] == 'S' && (s[1] == 0 || s[1] == ' ')) {
    char body[400];
    statusJSON(body, sizeof body);
    Serial.println(body);
  } else if (s[0] == 'W' && (s[1] == ' ' || s[1] == '\t')) {
    if (beaconMode) Serial.println("wifi: not in beacon mode (no Wi-Fi): B 0 first");
    else handleWifiCommand(s + 2);
  } else if (s[0] == 'B' && (s[1] == 0 || s[1] == ' ')) {
    handleBeaconCommand(s + 1);
  } else if (s[0] == 'L' && s[1] == ' ') {
    char* lv = s + 2;
    while (*lv == ' ') lv++;
    char* z = strchr(lv, ' ');
    if (z) {
      while (*z == ' ') z++;
    }
    setLevel(lv, z);
  }
}

void pollSerial() {
  while (Serial.available() > 0) {
    char c = Serial.read();
    if (c == '\n') {
      serialBuf[serialLen] = 0;
      handleSerialLine(serialBuf);
      serialLen = 0;
    } else if (c != '\r' && serialLen < (int)sizeof serialBuf - 1) {
      serialBuf[serialLen++] = c;
    }
  }
}

void serveClient() {
  WiFiClient client = server.available();
  if (!client) return;
  String line = "";
  unsigned long start = millis();
  Route route = NOT_FOUND;
  bool firstLine = true;
  while (client.connected() && millis() - start < 500) {
    if (!client.available()) continue;
    char c = client.read();
    if (c == '\n') {
      if (firstLine) {
        route = handleRequestLine(line);
        firstLine = false;
      }
      if (line.length() <= 1) break; // blank line: end of headers
      line = "";
    } else {
      line += c;
    }
  }
  if (route != NOT_FOUND) lastContact = millis();
  if (route == PULSE) {
    char body[400];
    statusJSON(body, sizeof body);
    client.println("HTTP/1.1 200 OK");
    client.println("Content-Type: application/json");
    client.println("Access-Control-Allow-Origin: *");
    client.println("Connection: close");
    client.println();
    client.println(body);
  } else if (route == LEVEL) {
    client.println("HTTP/1.1 200 OK");
    client.println("Content-Type: text/plain");
    client.println("Connection: close");
    client.println();
    client.println("ok");
  } else {
    client.println("HTTP/1.1 404 Not Found");
    client.println("Connection: close");
    client.println();
  }
  delay(1);
  client.stop();
}

// renderBitmap is a macro that takes the frame's address, so it can't be
// handed a ?: expression directly; route frames through a named parameter.
void show(uint8_t frame[8][12]) {
  matrix.renderBitmap(frame, 8, 12);
}

void render() {
  unsigned long t = millis();
  switch (level) {
    case CALM:
      // Heartbeat: a short blink every 1.5 s. USB-only: "USB" instead, with
      // the same blink (the frames alternate).
      // In touch with the server: a double flash of two dots every 1.5 s.
      if (inTouch()) {
        unsigned long lp = t % 1500;
        show((lp < 120 || (lp >= 260 && lp < 380)) ? linked : blank);
      } else if (usbOnly()) show(((t % 1500) < 120) ? blank : usb);
      else if (!wifiUp) show(waiting);  // still trying Wi-Fi (first 15 s)
      else show(((t % 1500) < 120) ? heart : blank);
      digitalWrite(ALARM_PIN, LOW);
      break;
    case YELLOW:
      matrix.renderBitmap(bang, 8, 12);
      digitalWrite(ALARM_PIN, LOW);
      break;
    case RED: {
      // Arrow flashes, then STOP, repeating every 1.2 s.
      unsigned long p = t % 1200;
      if (p < 600) show(((p / 150) % 2) ? blank : arrow);
      else matrix.renderBitmap(stop_, 8, 12);
      digitalWrite(ALARM_PIN, ((t / 250) % 2) ? HIGH : LOW);
      break;
    }
  }
}

void loop() {
  pollSerial();
  if (beaconMode) {
    pollBeacon();
  } else {
    pollWiFi();
    if (wifiUp) serveClient();
  }
  render();
  delay(10);
}
