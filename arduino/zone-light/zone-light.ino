// Pulse zone light — ESP32 DevKit V1 (one per zone).
//
// Same API as the Arduino sign: GET /level?v=calm|yellow|red&zone=A
// Plus GET /pulse → JSON status for the dashboard's hardware panel and for the
// server to find boards on the network (kind, beacon name, zone, level, Wi-Fi
// signal, uptime, Bluetooth devices heard, nearby Pulse boards):
//   {"kind":"zone-light","name":"PULSE-A","mac":"7B54","zone":"A","level":"calm",
//    "ip":"192.168.1.89","rssi":-53,"uptime":719,
//    "ble":{"devices":23,"near":5,"scans":119,"age":0},
//    "peers":[{"name":"PULSE-B","rssi":-63,"dist":2.4,"age":3}]}
//   mac  = last 4 hex digits of the Wi-Fi MAC
//   dist = rough distance estimate in metres (0.3–30), age = seconds since last heard
// and, while phones are connected over Bluetooth (connect mode, below),
//   "links":[{"id":"<session id>","rssi":-57,"age":1}]
// and the phones running the Pulse Android app that this board hears advertising (below),
//   "heard":[{"id":"1a2b3c4d","rssi":-63,"age":1}]
// GET /links → {"links":[...],"heard":[...]}: just those two, cheap enough for the server to poll every second.
// /pulse also carries "fw" (firmware build id: content hash of this sketch + build date),
// "wifi" (joined or not) and "ssid" (the network it is on or trying).
//
// USB serial (115200 baud), one command per line, so the board needs no Wi-Fi
// when it is plugged into the laptop running the server (A=serial:auto):
//   L <calm|yellow|red> [zone]  set the level (and learn the zone), exactly like /level
//   S                           reply with one /pulse-shaped JSON line
//   W <ssid><TAB><password>     save a Wi-Fi network in flash and join it now (tried before
//   W <ssid> <password>         the built-in ones); the password is never printed. Without a
//   W -                         tab the last space splits them. "W -" forgets the saved one.
// Anything else the board prints ("ble: …", "level …") is a log line, never JSON.
//
// Wi-Fi is optional: the board boots, scans, advertises and answers USB at once,
// and keeps trying its networks in the background (the saved one, then
// SECRET_SSID, SECRET_SSID2, SECRET_SSID3 from arduino_secrets.h; 15 s each).
//
// Onboard blue LED (GPIO 2), no wiring needed:
//   solid on      = nobody is talking to it and no Wi-Fi yet (booting, or Wi-Fi wrong and no USB server)
//   slow fade     = on Wi-Fi, waiting for the server to talk to it
//   calm, in touch with the server (over USB or Wi-Fi, in the last 20 s), count the blinks:
//     1 blink every 2 s  = alone
//     2 blinks every 2 s = another Pulse board is heard
//     3 blinks every 2 s = a phone is connected or its app is heard
//   yellow        = slow even blink, half a second on, half a second off
//   red           = rapid strobe
//
// Optional wiring, each LED through a 220 Ω resistor to GND:
//   separate LEDs: GPIO 25 green, GPIO 26 yellow/orange, GPIO 27 red
//   or one common-cathode RGB LED: R → 27, G → 25 (leave 26 unused, set RGB_LED true)
//   GPIO 14: active buzzer (beeps while red)
// The colour goes green → orange → red and the pulse speeds up with the level.
//
// Bluetooth crowd counter: passively counts the Bluetooth devices advertising
// nearby (phones, watches, earbuds). Only counts are kept: no addresses, no
// names. Phones rotate their Bluetooth addresses, so this is an estimate of
// how many devices are around, not a list of who. Pulse boards are not counted.
//
// Pulse beacon: each zone light also advertises itself as "PULSE-<zone>"
// (connectable, see connect mode), with manufacturer data FF FF 'P' 'L' 'S' <zone> so boards
// are recognised even if the name is cut off. The zone is learned from
// /level?...&zone=X and kept in flash (Preferences), so it survives reboots;
// until one is known the name is PULSE-<last 4 hex of the MAC>. Every board
// listens for the others in the same scan, keeps a median of the last 5 signal
// readings per board, forgets boards not heard for 30 s, and estimates distance
// with the log-distance model d = 10^((TX_POWER_1M − RSSI) / (10·n)).
// Calibrate TX_POWER_1M: put two boards 1 m apart and read "rssi" in /pulse.
//
// Connect mode: a phone's browser can't measure Bluetooth signal strength
// without a hidden flag, but any Android Chrome can connect. So the board
// also runs a tiny GATT server: one service with one writable characteristic.
// The Pulse phone page connects and writes its random session id (≤ 36
// characters; only letters, digits and '-' are kept). The board reads the
// signal strength of each connection about once a second and reports it in
// "links", so the server knows how far that phone is from this board. At
// most MAX_LINKS phones at a time (the Bluetooth stack's limit); a further
// one is disconnected at once, and so is a connection that hasn't written an
// id within 5 s. The beacon keeps advertising while phones are connected.
// Nothing else about the phone is read or kept.
//
// App phones: the Pulse Android app advertises the phone's identity itself, so
// no connection is needed and there is no cap of three. Its advert is a legacy
// non-connectable one with manufacturer data FF FF 'P' 'L' 'S' '1' followed by
// the first 8 hex characters of the phone's session id, and no name. The scan
// that already runs for the crowd counter picks these up as they arrive (every
// advert, not once per scan), keeps a smoothed signal strength for up to
// MAX_HEARD phones and reports them in "heard"; a phone not heard for 10 s is
// dropped. They still count as ordinary devices in the crowd counter.
// Scanning: passive, 50 ms window every 100 ms (half the airtime, the rest is
// for Wi-Fi), 5 s on and 1 s off, so a phone advertising every 100–250 ms is
// heard many times a second and the longest gap is about a second.
//
// Arduino IDE: install "esp32 by Espressif", board "ESP32 Dev Module",
// Tools → Partition Scheme → "Huge APP" (Wi-Fi + Bluetooth don't fit the default).
// Copy arduino_secrets.h.example → arduino_secrets.h (optional: Wi-Fi only). Or simply:
// scripts/boards.sh flash (Mac / Linux), pwsh scripts/flash-zone-lights.ps1 (Windows),
// which also stamp the build id ("fw"). On a table, plug it into the laptop: A=serial:auto.

#include <WiFi.h>
#include <WebServer.h>
#include <Preferences.h>
#include <esp_mac.h>
#include <BLEDevice.h>
#include <BLEScan.h>
#include <BLEAdvertising.h>
#include <BLEServer.h>
#include <esp_gap_ble_api.h>
#include "arduino_secrets.h"
// pulse_build.h is written by scripts/boards.sh / the flash scripts: #define PULSE_FW "<hash> <date>".
#if __has_include("pulse_build.h")
#include "pulse_build.h"
#endif
#ifndef PULSE_FW
#define PULSE_FW "dev"
#endif

// Wi-Fi networks, tried in order after the one saved over USB (W command).
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
const unsigned long WIFI_TRY_MS = 15000;    // per network before moving on to the next
const unsigned long CONTACT_FRESH_MS = 20000; // "in touch with the server" if it talked to us this recently

#ifndef TX_POWER_1M
#define TX_POWER_1M -64 // dBm one Pulse board hears from another 1 m away (measured: A↔B at 1 m, median of 26 scans, Oct 3 2026)
#endif
#define PATH_LOSS_N 2.2f // 2 = open air, ~2.2 indoors, up to 3–4 in a packed crowd

const bool RGB_LED = false; // true: one RGB LED on 27 (R) / 25 (G) instead of three LEDs
const int BLUE = 2, GREEN = 25, YELLOW = 26, RED = 27, BUZZER = 14;
const int SCAN_SECONDS = 5;
const int NEAR_RSSI = -70; // dBm: roughly within a few metres
const unsigned long PEER_TIMEOUT_MS = 30000;
const int MAX_PEERS = 8, RSSI_SAMPLES = 5, TAG_LEN = 6;
// Manufacturer data marker: company ID 0xFFFF (reserved for testing) + "PLS", then the zone tag.
const uint8_t PULSE_MAGIC[5] = {0xFF, 0xFF, 'P', 'L', 'S'};

// Connect mode (the same UUIDs are in server/internal/protocol/beacons.go and web/phone/src/beacons.ts).
#define PULSE_SERVICE_UUID "7b1e0001-52c4-4f6a-9d6b-50554c534500"
#define PULSE_ID_CHAR_UUID "7b1e0002-52c4-4f6a-9d6b-50554c534500"
const int MAX_LINKS = 3, LINK_ID_LEN = 36;
const unsigned long LINK_ID_TIMEOUT_MS = 5000, LINK_RSSI_MS = 1000;
// App phones heard advertising: company ID 0xFFFF + "PLS1" + 8 hex characters of the session id.
const uint8_t APP_MAGIC[6] = {0xFF, 0xFF, 'P', 'L', 'S', '1'};
const int MAX_HEARD = 24, ID8_LEN = 8, MAX_SCAN_DEVICES = 400;
const unsigned long HEARD_TIMEOUT_MS = 10000;
const float HEARD_EMA = 0.3f; // weight of a new reading in the smoothed signal strength

WebServer server(80);
Preferences prefs;
enum Level { CALM, WARN, DANGER };
Level level = CALM;
String zone = "";    // learned from /level, kept in flash
char macTag[5] = ""; // last 4 hex digits of the Wi-Fi MAC

// Wi-Fi and server contact (loop task only).
String savedSsid = "", savedPass = ""; // set over USB (W), kept in flash
int netIdx = -1;                       // network being tried: -1 = none yet
String curSsid = "";
unsigned long netSince = 0;
bool wifiUp = false, httpStarted = false;
unsigned long lastContact = 0; // last command from the server, over HTTP or USB
bool inTouch() { return lastContact && millis() - lastContact < CONTACT_FRESH_MS; }

// Beacon name, written by the web handler, read by the Bluetooth task.
char beaconName[16] = "PULSE-";
portMUX_TYPE nameMux = portMUX_INITIALIZER_UNLOCKED;
volatile bool advDirty = true;

// Bluetooth counter, written by the scan task, read by the web handler.
volatile int bleDevices = -1, bleNear = -1;
volatile unsigned long bleScans = 0, bleLastScan = 0;

// Pulse boards heard nearby, written by the scan task, read by the web handler.
struct Peer {
  char name[16];
  int8_t samples[RSSI_SAMPLES];
  uint8_t count, next;
  int8_t rssi; // median of the samples
  unsigned long seen;
};
Peer peers[MAX_PEERS];
int peerCount = 0;
portMUX_TYPE peerMux = portMUX_INITIALIZER_UNLOCKED;
volatile unsigned long peerBlip = 0; // when a new peer was first heard

// Phones connected over Bluetooth. Written by the Bluetooth stack's callbacks, read by the web handler and loop().
struct Link {
  bool used;
  uint16_t conn;
  esp_bd_addr_t addr;
  char id[LINK_ID_LEN + 1]; // the session id the phone wrote; "" until it does
  int8_t rssi;              // 0 = not measured yet
  unsigned long since, rssiAt;
};
Link links[MAX_LINKS];
portMUX_TYPE linkMux = portMUX_INITIALIZER_UNLOCKED;
BLEServer* gatt = nullptr;

// App phones heard advertising. Written by the scan callback, read by the web handler.
struct Heard {
  char id[ID8_LEN + 1];
  float rssi; // smoothed
  unsigned long seen;
};
Heard heard[MAX_HEARD];
int heardCount = 0;
portMUX_TYPE heardMux = portMUX_INITIALIZER_UNLOCKED;

// The devices of the scan in progress (addresses only, forgotten when it ends), so each counts once.
uint8_t scanSeen[MAX_SCAN_DEVICES][6];
int scanSeenCount = 0, scanDevices = 0, scanNear = 0;
volatile bool advRestart = false; // a phone connected or left: the controller stopped advertising

const char* levelName(Level l) { return l == DANGER ? "red" : l == WARN ? "yellow" : "calm"; }

// Keeps letters, digits, '-' and '_' (safe in JSON and in a Bluetooth name), at most TAG_LEN.
String cleanTag(const String& s) {
  String out;
  for (unsigned i = 0; i < s.length() && out.length() < TAG_LEN; i++) {
    char c = s[i];
    if (isalnum((unsigned char)c) || c == '-' || c == '_') out += c;
  }
  return out;
}

void setBeaconName() {
  String tag = zone.length() ? zone : String(macTag);
  portENTER_CRITICAL(&nameMux);
  snprintf(beaconName, sizeof beaconName, "PULSE-%s", tag.c_str());
  portEXIT_CRITICAL(&nameMux);
  advDirty = true;
}

void copyBeaconName(char* out, size_t len) {
  portENTER_CRITICAL(&nameMux);
  strlcpy(out, beaconName, len);
  portEXIT_CRITICAL(&nameMux);
}

// Applies a level word ("red", "yellow", anything else = calm) and, if given, the zone (from HTTP or USB).
void applyLevel(const String& v, const String* zoneArg) {
  level = v == "red" ? DANGER : v == "yellow" ? WARN : CALM;
  if (zoneArg) {
    String z = cleanTag(*zoneArg);
    if (z.length() && z != zone) {
      zone = z;
      prefs.putString("zone", zone); // only on change: spares the flash
      setBeaconName();
    }
  }
  lastContact = millis();
  Serial.printf("level %s zone %s\n", v.c_str(), zone.c_str());
}

void handleLevel() {
  String z = server.arg("zone");
  applyLevel(server.arg("v"), server.hasArg("zone") ? &z : nullptr);
  server.send(200, "text/plain", "ok\n");
}

// Copies s into out for a JSON string: drops quotes, backslashes and control characters.
void jsonSafe(const String& s, char* out, size_t len) {
  size_t j = 0;
  for (unsigned i = 0; i < s.length() && j + 1 < len; i++) {
    char c = s[i];
    if (c == '"' || c == '\\' || (unsigned char)c < 0x20) continue;
    out[j++] = c;
  }
  out[j] = 0;
}

float estimateDistance(int rssi) {
  float d = powf(10.0f, (TX_POWER_1M - rssi) / (10.0f * PATH_LOSS_N));
  return d < 0.3f ? 0.3f : d > 30.0f ? 30.0f : d;
}

// Writes the links that have an id as a JSON array; returns the length.
int linksJson(char* buf, size_t size) {
  Link snap[MAX_LINKS];
  portENTER_CRITICAL(&linkMux);
  memcpy(snap, links, sizeof links);
  portEXIT_CRITICAL(&linkMux);
  unsigned long now = millis();
  int len = snprintf(buf, size, "[");
  bool first = true;
  for (int i = 0; i < MAX_LINKS && len < (int)size - 80; i++) {
    if (!snap[i].used || !snap[i].id[0]) continue;
    len += snprintf(buf + len, size - len, "%s{\"id\":\"%s\",\"rssi\":%d,\"age\":%lu}", first ? "" : ",", snap[i].id, snap[i].rssi,
                    snap[i].rssiAt ? (now - snap[i].rssiAt) / 1000 : 0UL);
    first = false;
  }
  len += snprintf(buf + len, size - len, "]");
  return len;
}

// Writes the app phones heard in the last HEARD_TIMEOUT_MS as a JSON array; returns the length.
int heardJson(char* buf, size_t size) {
  static Heard snap[MAX_HEARD]; // only the web handler calls this
  int n;
  portENTER_CRITICAL(&heardMux);
  n = heardCount;
  memcpy(snap, heard, n * sizeof(Heard));
  portEXIT_CRITICAL(&heardMux);
  unsigned long now = millis();
  int len = snprintf(buf, size, "[");
  bool first = true;
  for (int i = 0; i < n && len < (int)size - 60; i++) {
    unsigned long age = now - snap[i].seen;
    if (age > HEARD_TIMEOUT_MS) continue;
    len += snprintf(buf + len, size - len, "%s{\"id\":\"%s\",\"rssi\":%d,\"age\":%lu}", first ? "" : ",", snap[i].id, (int)lroundf(snap[i].rssi), age / 1000);
    first = false;
  }
  len += snprintf(buf + len, size - len, "]");
  return len;
}

void handleLinks() {
  static char buf[1600]; // 3 links × ~70 bytes + 24 heard × ~45 bytes
  int len = snprintf(buf, sizeof buf, "{\"links\":");
  len += linksJson(buf + len, 320);
  len += snprintf(buf + len, sizeof buf - len, ",\"heard\":");
  len += heardJson(buf + len, sizeof buf - len - 2);
  snprintf(buf + len, sizeof buf - len, "}");
  lastContact = millis();
  server.sendHeader("Access-Control-Allow-Origin", "*");
  server.send(200, "application/json", buf);
}

// Writes the /pulse JSON (also the reply to "S" over USB); returns the length.
int pulseJson(char* buf, size_t size) {
  Peer snap[MAX_PEERS];
  int n;
  portENTER_CRITICAL(&peerMux);
  n = peerCount;
  memcpy(snap, peers, n * sizeof(Peer));
  portEXIT_CRITICAL(&peerMux);
  char name[16];
  copyBeaconName(name, sizeof name);

  unsigned long now = millis();
  char ssid[40];
  jsonSafe(curSsid, ssid, sizeof ssid);
  int len = snprintf(buf, size,
           "{\"kind\":\"zone-light\",\"name\":\"%s\",\"mac\":\"%s\",\"zone\":\"%s\",\"level\":\"%s\",\"ip\":\"%s\","
           "\"rssi\":%d,\"uptime\":%lu,\"fw\":\"%s\",\"wifi\":%s,\"ssid\":\"%s\","
           "\"ble\":{\"devices\":%d,\"near\":%d,\"scans\":%lu,\"age\":%lu},\"peers\":[",
           name, macTag, zone.c_str(), levelName(level), wifiUp ? WiFi.localIP().toString().c_str() : "",
           wifiUp ? (int)WiFi.RSSI() : 0, now / 1000, PULSE_FW, wifiUp ? "true" : "false", ssid,
           bleDevices, bleNear, bleScans, bleLastScan ? (now - bleLastScan) / 1000 : 0UL);
  bool first = true;
  for (int i = 0; i < n && len < (int)size - 1700; i++) {
    unsigned long age = now - snap[i].seen;
    if (age > PEER_TIMEOUT_MS) continue;
    len += snprintf(buf + len, size - len, "%s{\"name\":\"%s\",\"rssi\":%d,\"dist\":%.1f,\"age\":%lu}",
                    first ? "" : ",", snap[i].name, snap[i].rssi, estimateDistance(snap[i].rssi), age / 1000);
    first = false;
  }
  len += snprintf(buf + len, size - len, "],\"links\":");
  len += linksJson(buf + len, 320);
  len += snprintf(buf + len, size - len, ",\"heard\":");
  len += heardJson(buf + len, size - len - 2);
  len += snprintf(buf + len, size - len, "}");
  return len;
}

static char pulseBuf[3000]; // 8 peers × ~55 bytes + ~400 bytes of status + 3 links × ~70 bytes + 24 heard × ~45 bytes

void handlePulse() {
  pulseJson(pulseBuf, sizeof pulseBuf);
  lastContact = millis();
  server.sendHeader("Access-Control-Allow-Origin", "*");
  server.send(200, "application/json", pulseBuf);
}

// ---- Wi-Fi: optional, tried in the background ----

// The i-th network to try: the one saved over USB first, then the built-in ones. False = none there.
bool netAt(int i, String& ssid, String& pass) {
  if (savedSsid.length()) {
    if (i == 0) { ssid = savedSsid; pass = savedPass; return true; }
    i--;
  }
  if (i < 0 || i >= BUILTIN_NET_COUNT) return false;
  ssid = BUILTIN_NETS[i].ssid;
  pass = BUILTIN_NETS[i].pass;
  return true;
}

int netCount() { return (savedSsid.length() ? 1 : 0) + BUILTIN_NET_COUNT; }

// Starts joining the next usable network (skipping empty and placeholder names).
void tryNextNet() {
  int n = netCount();
  for (int k = 0; k < n; k++) {
    netIdx = (netIdx + 1) % n;
    String ssid, pass;
    if (!netAt(netIdx, ssid, pass) || !ssid.length() || ssid == "your-wifi") continue;
    curSsid = ssid;
    netSince = millis();
    WiFi.disconnect();
    WiFi.begin(ssid.c_str(), pass.c_str());
    Serial.printf("wifi: trying %s\n", ssid.c_str());
    return;
  }
  curSsid = "";
  netSince = millis(); // nothing configured: look again later (a W command restarts at once)
}

// Notices Wi-Fi coming up or dropping and moves on to the next network while it's down. Never blocks.
void pollWiFi(unsigned long now) {
  static unsigned long lastPoll = 0;
  if (now - lastPoll < 250) return;
  lastPoll = now;
  bool connected = WiFi.status() == WL_CONNECTED;
  if (connected && !wifiUp) {
    wifiUp = true;
    if (!httpStarted) {
      server.begin();
      httpStarted = true;
    }
    Serial.printf("Zone light ready: http://%s (%s)\n", WiFi.localIP().toString().c_str(), curSsid.c_str());
  } else if (!connected && wifiUp) {
    wifiUp = false;
    netSince = now; // the stack reconnects by itself; after WIFI_TRY_MS move on
    Serial.println("wifi: lost; retrying in the background (USB still works)");
  } else if (!connected && now - netSince >= WIFI_TRY_MS) {
    tryNextNet();
  }
}

// ---- USB serial commands ----

char serialBuf[160];
int serialLen = 0;

void handleSerialLine(char* s) {
  while (*s == ' ') s++;
  if (s[0] == 'S' && (s[1] == 0 || s[1] == ' ')) {
    lastContact = millis();
    int len = pulseJson(pulseBuf, sizeof pulseBuf - 2);
    pulseBuf[len++] = '\n';
    Serial.write((const uint8_t*)pulseBuf, len); // one write: never interleaved with the Bluetooth task's log lines
  } else if (s[0] == 'L' && s[1] == ' ') {
    char* lv = s + 2;
    while (*lv == ' ') lv++;
    char* z = strchr(lv, ' ');
    if (z) {
      *z++ = 0;
      while (*z == ' ') z++;
    }
    String zs = z ? String(z) : String();
    applyLevel(String(lv), z && *z ? &zs : nullptr);
  } else if (s[0] == 'W' && (s[1] == ' ' || s[1] == '\t')) {
    char* arg = s + 2;
    while (*arg == ' ') arg++;
    if (strcmp(arg, "-") == 0) {
      savedSsid = savedPass = "";
      prefs.remove("wssid");
      prefs.remove("wpass");
      Serial.println("wifi: forgot the saved network");
    } else {
      char* sep = strchr(arg, '\t');
      if (!sep) sep = strrchr(arg, ' ');
      if (sep) *sep++ = 0;
      if (!*arg || strlen(arg) > 32 || (sep && strlen(sep) > 63)) {
        Serial.println("wifi: want W <ssid><TAB><password> (ssid up to 32, password up to 63 characters)");
        return;
      }
      savedSsid = arg;
      savedPass = sep ? sep : "";
      prefs.putString("wssid", savedSsid);
      prefs.putString("wpass", savedPass);
      Serial.printf("wifi: saved %s; joining it now\n", savedSsid.c_str()); // never the password
    }
    lastContact = millis();
    wifiUp = false;
    netIdx = -1; // start over from the saved network
    tryNextNet();
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

// If the advert is from the Pulse Android app, writes the 8 hex characters of its id (lower case) to out and returns true.
bool appPhoneId(BLEAdvertisedDevice& d, char* out) {
  if (!d.haveManufacturerData()) return false;
  String m = d.getManufacturerData();
  if (m.length() != sizeof APP_MAGIC + ID8_LEN || memcmp(m.c_str(), APP_MAGIC, sizeof APP_MAGIC) != 0) return false;
  for (int i = 0; i < ID8_LEN; i++) {
    char c = m[sizeof APP_MAGIC + i];
    if (!isxdigit((unsigned char)c)) return false;
    out[i] = tolower((unsigned char)c);
  }
  out[ID8_LEN] = 0;
  return true;
}

// Records one advert of an app phone: a moving average of its signal strength.
void noteHeard(const char* id, int rssi, unsigned long now) {
  portENTER_CRITICAL(&heardMux);
  int i = 0;
  while (i < heardCount && strcmp(heard[i].id, id) != 0) i++;
  if (i == heardCount) {
    if (heardCount == MAX_HEARD) { // full: replace the stalest
      i = 0;
      for (int j = 1; j < heardCount; j++)
        if (heard[j].seen < heard[i].seen) i = j;
    } else {
      heardCount++;
    }
    strlcpy(heard[i].id, id, sizeof heard[i].id);
    heard[i].rssi = rssi;
  } else if (now - heard[i].seen > 3000) {
    heard[i].rssi = rssi; // back after a gap: don't drag the old value along
  } else {
    heard[i].rssi += HEARD_EMA * (rssi - heard[i].rssi);
  }
  heard[i].seen = now;
  portEXIT_CRITICAL(&heardMux);
}

void expireHeard(unsigned long now) {
  portENTER_CRITICAL(&heardMux);
  for (int i = 0; i < heardCount;)
    if (now - heard[i].seen > HEARD_TIMEOUT_MS) heard[i] = heard[--heardCount];
    else i++;
  portEXIT_CRITICAL(&heardMux);
}

// If the advert is from a Pulse board, writes its name ("PULSE-B") to out and returns true.
bool pulseBoardName(BLEAdvertisedDevice& d, char* out, size_t len) {
  if (d.haveManufacturerData()) {
    String m = d.getManufacturerData();
    if (m.length() > sizeof PULSE_MAGIC && memcmp(m.c_str(), PULSE_MAGIC, sizeof PULSE_MAGIC) == 0) {
      snprintf(out, len, "PULSE-%s", cleanTag(m.substring(sizeof PULSE_MAGIC)).c_str());
      return true;
    }
  }
  if (d.haveName()) {
    String nm = d.getName();
    if (nm.startsWith("PULSE-")) {
      snprintf(out, len, "PULSE-%s", cleanTag(nm.substring(6)).c_str());
      return true;
    }
  }
  return false;
}

// Records one sighting of a Pulse board; returns true the first time it's heard.
bool notePeer(const char* name, int rssi, unsigned long now) {
  bool isNew = false;
  portENTER_CRITICAL(&peerMux);
  int i = 0;
  while (i < peerCount && strcmp(peers[i].name, name) != 0) i++;
  if (i == peerCount) {
    if (peerCount == MAX_PEERS) { // full: replace the stalest
      i = 0;
      for (int j = 1; j < peerCount; j++)
        if (peers[j].seen < peers[i].seen) i = j;
    } else {
      peerCount++;
    }
    strlcpy(peers[i].name, name, sizeof peers[i].name);
    peers[i].count = peers[i].next = 0;
    isNew = true;
  }
  Peer& p = peers[i];
  p.samples[p.next] = (int8_t)constrain(rssi, -127, 20);
  p.next = (p.next + 1) % RSSI_SAMPLES;
  if (p.count < RSSI_SAMPLES) p.count++;
  int8_t s[RSSI_SAMPLES]; // median of the last few readings shrugs off single bad ones
  memcpy(s, p.samples, p.count);
  for (int a = 1; a < p.count; a++)
    for (int b = a; b > 0 && s[b - 1] > s[b]; b--) { int8_t t = s[b]; s[b] = s[b - 1]; s[b - 1] = t; }
  p.rssi = s[p.count / 2];
  p.seen = now;
  portEXIT_CRITICAL(&peerMux);
  return isNew;
}

void expirePeers(unsigned long now) {
  portENTER_CRITICAL(&peerMux);
  for (int i = 0; i < peerCount;)
    if (now - peers[i].seen > PEER_TIMEOUT_MS) peers[i] = peers[--peerCount];
    else i++;
  portEXIT_CRITICAL(&peerMux);
}

// ---- connect mode: these run on the Bluetooth stack's task ----

class LinkServerCallbacks : public BLEServerCallbacks {
  void onConnect(BLEServer* s, esp_ble_gatts_cb_param_t* p) override {
    bool kept = false;
    portENTER_CRITICAL(&linkMux);
    for (int i = 0; i < MAX_LINKS && !kept; i++) {
      if (links[i].used) continue;
      memset(&links[i], 0, sizeof(Link));
      links[i].used = true;
      links[i].conn = p->connect.conn_id;
      memcpy(links[i].addr, p->connect.remote_bda, sizeof(esp_bd_addr_t));
      links[i].since = millis();
      kept = true;
    }
    portEXIT_CRITICAL(&linkMux);
    if (!kept) s->disconnect(p->connect.conn_id); // full
    advRestart = true;                            // connecting stops the advert: keep the beacon on air
  }
  void onDisconnect(BLEServer*, esp_ble_gatts_cb_param_t* p) override {
    portENTER_CRITICAL(&linkMux);
    for (int i = 0; i < MAX_LINKS; i++)
      if (links[i].used && links[i].conn == p->disconnect.conn_id) links[i].used = false;
    portEXIT_CRITICAL(&linkMux);
    advRestart = true;
  }
};

class LinkIdCallbacks : public BLECharacteristicCallbacks {
  void onWrite(BLECharacteristic* c, esp_ble_gatts_cb_param_t* p) override {
    String v = c->getValue().c_str();
    char id[LINK_ID_LEN + 1];
    int n = 0;
    for (unsigned i = 0; i < v.length() && n < LINK_ID_LEN; i++) {
      char ch = v[i];
      if (isalnum((unsigned char)ch) || ch == '-') id[n++] = ch; // nothing that could break JSON
    }
    id[n] = 0;
    portENTER_CRITICAL(&linkMux);
    for (int i = 0; i < MAX_LINKS; i++)
      if (links[i].used && links[i].conn == p->write.conn_id) strlcpy(links[i].id, id, sizeof links[i].id);
    portEXIT_CRITICAL(&linkMux);
    c->setValue(""); // the id is nobody else's business
  }
};

// Signal strength of a connection, asked for in loop() with esp_ble_gap_read_rssi.
void linkGapEvent(esp_gap_ble_cb_event_t event, esp_ble_gap_cb_param_t* p) {
  if (event != ESP_GAP_BLE_READ_RSSI_COMPLETE_EVT || p->read_rssi_cmpl.status != ESP_BT_STATUS_SUCCESS) return;
  unsigned long now = millis();
  portENTER_CRITICAL(&linkMux);
  for (int i = 0; i < MAX_LINKS; i++)
    if (links[i].used && memcmp(links[i].addr, p->read_rssi_cmpl.remote_addr, sizeof(esp_bd_addr_t)) == 0) {
      links[i].rssi = p->read_rssi_cmpl.rssi;
      links[i].rssiAt = now;
    }
  portEXIT_CRITICAL(&linkMux);
}

// Called from loop(): asks for each connection's signal strength about once a second, drops
// connections that never said who they are, and restarts the advert after a connect or disconnect.
void serviceLinks(unsigned long now) {
  static unsigned long last = 0;
  if (!gatt) return;
  if (advRestart) {
    advRestart = false;
    BLEDevice::startAdvertising();
  }
  if (now - last < LINK_RSSI_MS) return;
  last = now;
  Link snap[MAX_LINKS];
  portENTER_CRITICAL(&linkMux);
  memcpy(snap, links, sizeof links);
  portEXIT_CRITICAL(&linkMux);
  for (int i = 0; i < MAX_LINKS; i++) {
    if (!snap[i].used) continue;
    if (!snap[i].id[0] && now - snap[i].since > LINK_ID_TIMEOUT_MS) gatt->disconnect(snap[i].conn);
    else esp_ble_gap_read_rssi(snap[i].addr);
  }
}

// Every advert the scan hears lands here (on the Bluetooth stack's task). App phones are noted each
// time; for the crowd counter and the peers each device counts once per scan, as before.
class ScanCallbacks : public BLEAdvertisedDeviceCallbacks {
  void onResult(BLEAdvertisedDevice d) override {
    int rssi = d.getRSSI();
    char id[ID8_LEN + 1];
    bool app = appPhoneId(d, id);
    if (app) noteHeard(id, rssi, millis());
    uint8_t addr[6];
    memcpy(addr, d.getAddress().getNative(), sizeof addr);
    for (int i = 0; i < scanSeenCount; i++)
      if (memcmp(scanSeen[i], addr, sizeof addr) == 0) return; // already counted in this scan
    if (scanSeenCount == MAX_SCAN_DEVICES) return;             // more than the table holds: not counted
    memcpy(scanSeen[scanSeenCount++], addr, sizeof addr);
    char peer[16];
    if (!app && pulseBoardName(d, peer, sizeof peer)) {
      if (notePeer(peer, rssi, millis())) {
        peerBlip = millis();
        Serial.printf("ble: found %s (%d dBm)\n", peer, rssi);
      }
      return; // Pulse boards aren't crowd
    }
    scanDevices++; // an app phone is a phone like any other here
    if (rssi >= NEAR_RSSI) scanNear++;
  }
};

// (Re)publishes the beacon: flags, manufacturer marker + zone, and the full name (≤ 30 of 31 bytes).
void updateAdvert(BLEAdvertising* adv, bool running) {
  char name[16];
  copyBeaconName(name, sizeof name);
  BLEAdvertisementData data;
  data.setFlags(ESP_BLE_ADV_FLAG_GEN_DISC | ESP_BLE_ADV_FLAG_BREDR_NOT_SPT);
  String marker(PULSE_MAGIC, sizeof PULSE_MAGIC);
  marker += name + 6; // the tag after "PULSE-"
  data.setManufacturerData(marker);
  data.setName(name);
  if (running) adv->stop();
  adv->setAdvertisementData(data); // advertising (re)starts once the controller has the data
  Serial.printf("ble: advertising as %s\n", name);
}

// Scans run on the other core so the LED and web server never stall.
void bleTask(void*) {
  char name[16];
  copyBeaconName(name, sizeof name);
  BLEDevice::init(name);
  BLEAdvertising* adv = BLEDevice::getAdvertising();
  adv->setScanResponse(false); // everything fits in the advert itself
  adv->setAdvertisementType(ADV_TYPE_IND); // connectable: a phone page can connect and say who it is (connect mode)
  adv->setMinInterval(0xA0); // 100–200 ms, in 0.625 ms units
  adv->setMaxInterval(0x140);
  bool advertising = false;

  // Connect mode: one service, one writable characteristic (the phone's session id).
  BLEDevice::setCustomGapHandler(linkGapEvent);
  BLEServer* srv = BLEDevice::createServer();
  srv->setCallbacks(new LinkServerCallbacks());
  BLEService* svc = srv->createService(PULSE_SERVICE_UUID);
  BLECharacteristic* idChar = svc->createCharacteristic(PULSE_ID_CHAR_UUID, BLECharacteristic::PROPERTY_WRITE | BLECharacteristic::PROPERTY_WRITE_NR);
  idChar->setCallbacks(new LinkIdCallbacks());
  svc->start();
  gatt = srv;

  BLEScan* scan = BLEDevice::getScan();
  scan->setActiveScan(false); // listen only: never ask devices for more data
  scan->setInterval(160);
  scan->setWindow(80); // half duty cycle leaves airtime for Wi-Fi
  scan->setAdvertisedDeviceCallbacks(new ScanCallbacks(), true); // true: every advert, so app phones are heard as they come
  for (;;) {
    if (advDirty) {
      advDirty = false;
      updateAdvert(adv, advertising);
      advertising = true;
    }
    scanSeenCount = scanDevices = scanNear = 0;
    scan->start(SCAN_SECONDS, false); // ScanCallbacks does the counting
    unsigned long now = millis();
    int devices = scanDevices, near = scanNear;
    scan->clearResults();
    expirePeers(now);
    expireHeard(now);
    bleDevices = devices;
    bleNear = near;
    bleScans++;
    bleLastScan = millis();
    Serial.printf("ble: %d devices, %d near, %d Pulse boards, %d app phones\n", devices, near, peerCount, heardCount);
    vTaskDelay(pdMS_TO_TICKS(1000));
  }
}

void setup() {
  Serial.begin(115200);
  pinMode(BUZZER, OUTPUT);
  for (int p : {BLUE, GREEN, YELLOW, RED}) ledcAttach(p, 5000, 8); // PWM so LEDs can breathe
  WiFi.mode(WIFI_STA); // modem sleep stays on: required when Wi-Fi and Bluetooth share the radio
  uint8_t mac[6];
  esp_read_mac(mac, ESP_MAC_WIFI_STA); // from eFuse: valid before the Wi-Fi driver is up
  snprintf(macTag, sizeof macTag, "%02X%02X", mac[4], mac[5]);
  prefs.begin("pulse", false);
  zone = cleanTag(prefs.getString("zone", ""));
  savedSsid = prefs.getString("wssid", "");
  savedPass = prefs.getString("wpass", "");
  setBeaconName();
  Serial.printf("\nPulse zone light %s, firmware %s. USB commands: L <level> [zone], S, W <ssid> <password>\n", macTag, PULSE_FW);
  server.on("/level", handleLevel);
  server.on("/pulse", handlePulse);
  server.on("/links", handleLinks);
  server.onNotFound([] { server.send(404, "text/plain", "try /level?v=red, /pulse or /links\n"); });
  // Wi-Fi joins in the background (pollWiFi); Bluetooth, the LEDs and USB work from now on.
  tryNextNet();
  xTaskCreatePinnedToCore(bleTask, "ble", 8192, nullptr, 1, nullptr, 0);
}

// 0..255 breathing curve with the given period.
int breathe(unsigned long t, unsigned long period, int lo, int hi) {
  float ph = (t % period) / (float)period;
  float s = 0.5f - 0.5f * cosf(ph * 2 * PI);
  return lo + (int)((hi - lo) * s * s); // squared: lingers dim, swells bright
}

void loop() {
  pollSerial();
  if (httpStarted) server.handleClient();
  unsigned long t = millis();
  serviceLinks(t);
  pollWiFi(t);

  // Onboard blue LED. While calm: solid = nobody is talking to the board and
  // no Wi-Fi; slow fade = on Wi-Fi, waiting for the server; counted blinks =
  // the server is in touch (USB or Wi-Fi): 1 = alone, 2 = linked to another
  // Pulse board, 3 = a phone is connected or heard. Yellow and red override.
  bool linked = peerCount > 0;
  int phones = heardCount;
  for (int i = 0; i < MAX_LINKS; i++)
    if (links[i].used) phones++;
  int blinks = phones > 0 ? 3 : linked ? 2 : 1;
  unsigned long lp = t % 2000;
  bool linkFlash = lp < (unsigned long)blinks * 300 && (lp % 300) < 120;
  int pwm;
  switch (level) {
    case CALM: pwm = inTouch() ? (linkFlash ? 255 : 0) : wifiUp ? breathe(t, 3000, 0, 200) : 255; break;
    case WARN: pwm = (t % 1000) < 500 ? 255 : 0; break;
    default:   pwm = (t % 140) < 70 ? 255 : 0; break;
  }
  ledcWrite(BLUE, pwm);

  // External LEDs: green → orange → red, speeding up.
  int g = 0, y = 0, r = 0;
  switch (level) {
    case CALM: g = linked ? (linkFlash ? 255 : 12) : breathe(t, 3000, 10, 255); break;
    case WARN:
      if (RGB_LED) { r = breathe(t, 1100, 20, 255); g = r * 2 / 5; } // orange = red + some green
      else y = breathe(t, 1100, 20, 255);
      break;
    default: r = (t % 140) < 70 ? 255 : 0; break;
  }
  ledcWrite(GREEN, g);
  ledcWrite(YELLOW, RGB_LED ? 0 : y);
  ledcWrite(RED, r);
  digitalWrite(BUZZER, level == DANGER && (t % 1000) < 150);
  delay(5);
}
