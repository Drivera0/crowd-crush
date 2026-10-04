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
//
// Onboard blue LED (GPIO 2), no wiring needed:
//   joining Wi-Fi = solid on (stays on if Wi-Fi is wrong)
//   calm, alone   = 1 blink every 2 s
//   calm, linked  = 2 blinks every 2 s (another Pulse board is heard)
//   calm, phone   = 3 blinks every 2 s (a phone is connected or its app is heard)
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
// Copy arduino_secrets.h.example → arduino_secrets.h. Or: pwsh scripts/flash-zone-lights.ps1

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

void handleLevel() {
  String v = server.arg("v");
  level = v == "red" ? DANGER : v == "yellow" ? WARN : CALM;
  if (server.hasArg("zone")) {
    String z = cleanTag(server.arg("zone"));
    if (z.length() && z != zone) {
      zone = z;
      prefs.putString("zone", zone); // only on change: spares the flash
      setBeaconName();
    }
  }
  Serial.printf("level %s zone %s\n", v.c_str(), zone.c_str());
  server.send(200, "text/plain", "ok\n");
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
  server.sendHeader("Access-Control-Allow-Origin", "*");
  server.send(200, "application/json", buf);
}

void handlePulse() {
  Peer snap[MAX_PEERS];
  int n;
  portENTER_CRITICAL(&peerMux);
  n = peerCount;
  memcpy(snap, peers, n * sizeof(Peer));
  portEXIT_CRITICAL(&peerMux);
  char name[16];
  copyBeaconName(name, sizeof name);

  static char buf[2900]; // 8 peers × ~55 bytes + ~300 bytes of status + 3 links × ~70 bytes + 24 heard × ~45 bytes
  unsigned long now = millis();
  int len = snprintf(buf, sizeof buf,
           "{\"kind\":\"zone-light\",\"name\":\"%s\",\"mac\":\"%s\",\"zone\":\"%s\",\"level\":\"%s\",\"ip\":\"%s\","
           "\"rssi\":%d,\"uptime\":%lu,\"ble\":{\"devices\":%d,\"near\":%d,\"scans\":%lu,\"age\":%lu},\"peers\":[",
           name, macTag, zone.c_str(), levelName(level), WiFi.localIP().toString().c_str(), WiFi.RSSI(), now / 1000,
           bleDevices, bleNear, bleScans, bleLastScan ? (now - bleLastScan) / 1000 : 0UL);
  bool first = true;
  for (int i = 0; i < n && len < (int)sizeof buf - 1700; i++) {
    unsigned long age = now - snap[i].seen;
    if (age > PEER_TIMEOUT_MS) continue;
    len += snprintf(buf + len, sizeof buf - len, "%s{\"name\":\"%s\",\"rssi\":%d,\"dist\":%.1f,\"age\":%lu}",
                    first ? "" : ",", snap[i].name, snap[i].rssi, estimateDistance(snap[i].rssi), age / 1000);
    first = false;
  }
  len += snprintf(buf + len, sizeof buf - len, "],\"links\":");
  len += linksJson(buf + len, 320);
  len += snprintf(buf + len, sizeof buf - len, ",\"heard\":");
  len += heardJson(buf + len, sizeof buf - len - 2);
  snprintf(buf + len, sizeof buf - len, "}");
  server.sendHeader("Access-Control-Allow-Origin", "*");
  server.send(200, "application/json", buf);
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
  setBeaconName();
  WiFi.begin(SECRET_SSID, SECRET_PASS);
  Serial.print("Connecting");
  unsigned long t0 = millis();
  while (WiFi.status() != WL_CONNECTED) {
    unsigned long p = (millis() - t0) % 600;
    (void)p;
    ledcWrite(BLUE, 255); // solid: joining Wi-Fi
    delay(10);
    if ((millis() - t0) % 500 < 10) Serial.print(".");
  }
  Serial.printf("\nZone light ready: http://%s\n", WiFi.localIP().toString().c_str());
  server.on("/level", handleLevel);
  server.on("/pulse", handlePulse);
  server.on("/links", handleLinks);
  server.onNotFound([] { server.send(404, "text/plain", "try /level?v=red, /pulse or /links\n"); });
  server.begin();
  xTaskCreatePinnedToCore(bleTask, "ble", 8192, nullptr, 1, nullptr, 0);
}

// 0..255 breathing curve with the given period.
int breathe(unsigned long t, unsigned long period, int lo, int hi) {
  float ph = (t % period) / (float)period;
  float s = 0.5f - 0.5f * cosf(ph * 2 * PI);
  return lo + (int)((hi - lo) * s * s); // squared: lingers dim, swells bright
}

void loop() {
  server.handleClient();
  unsigned long t = millis();
  serviceLinks(t);

  if (WiFi.status() != WL_CONNECTED) {
    WiFi.reconnect();
    ledcWrite(BLUE, 255);
    delay(5);
    return;
  }

  // Onboard blue LED: count the blinks. 1 = alone, 2 = linked to another
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
    case CALM: pwm = linkFlash ? 255 : 0; break;
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
