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
//
// Onboard blue LED (GPIO 2), no wiring needed:
//   joining Wi-Fi = quick double flash (stays like this if Wi-Fi is wrong)
//   calm          = slow breathing pulse
//   yellow        = faster, brighter breathing
//   red           = rapid strobe
//   + a quick triple blip each time a Bluetooth scan finishes (~every 6 s)
//   + a short double blip when another Pulse board is heard for the first time
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
// (non-connectable), with manufacturer data FF FF 'P' 'L' 'S' <zone> so boards
// are recognised even if the name is cut off. The zone is learned from
// /level?...&zone=X and kept in flash (Preferences), so it survives reboots;
// until one is known the name is PULSE-<last 4 hex of the MAC>. Every board
// listens for the others in the same scan, keeps a median of the last 5 signal
// readings per board, forgets boards not heard for 30 s, and estimates distance
// with the log-distance model d = 10^((TX_POWER_1M − RSSI) / (10·n)).
// Calibrate TX_POWER_1M: put two boards 1 m apart and read "rssi" in /pulse.
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

void handlePulse() {
  Peer snap[MAX_PEERS];
  int n;
  portENTER_CRITICAL(&peerMux);
  n = peerCount;
  memcpy(snap, peers, n * sizeof(Peer));
  portEXIT_CRITICAL(&peerMux);
  char name[16];
  copyBeaconName(name, sizeof name);

  char buf[1280]; // 8 peers × ~55 bytes + ~300 bytes of status
  unsigned long now = millis();
  int len = snprintf(buf, sizeof buf,
           "{\"kind\":\"zone-light\",\"name\":\"%s\",\"mac\":\"%s\",\"zone\":\"%s\",\"level\":\"%s\",\"ip\":\"%s\","
           "\"rssi\":%d,\"uptime\":%lu,\"ble\":{\"devices\":%d,\"near\":%d,\"scans\":%lu,\"age\":%lu},\"peers\":[",
           name, macTag, zone.c_str(), levelName(level), WiFi.localIP().toString().c_str(), WiFi.RSSI(), now / 1000,
           bleDevices, bleNear, bleScans, bleLastScan ? (now - bleLastScan) / 1000 : 0UL);
  bool first = true;
  for (int i = 0; i < n && len < (int)sizeof buf - 80; i++) {
    unsigned long age = now - snap[i].seen;
    if (age > PEER_TIMEOUT_MS) continue;
    len += snprintf(buf + len, sizeof buf - len, "%s{\"name\":\"%s\",\"rssi\":%d,\"dist\":%.1f,\"age\":%lu}",
                    first ? "" : ",", snap[i].name, snap[i].rssi, estimateDistance(snap[i].rssi), age / 1000);
    first = false;
  }
  snprintf(buf + len, sizeof buf - len, "]}");
  server.sendHeader("Access-Control-Allow-Origin", "*");
  server.send(200, "application/json", buf);
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
  adv->setAdvertisementType(ADV_TYPE_NONCONN_IND); // beacon only: nobody can connect
  adv->setMinInterval(0xA0); // 100–200 ms, in 0.625 ms units
  adv->setMaxInterval(0x140);
  bool advertising = false;

  BLEScan* scan = BLEDevice::getScan();
  scan->setActiveScan(false); // listen only: never ask devices for more data
  scan->setInterval(160);
  scan->setWindow(80); // half duty cycle leaves airtime for Wi-Fi
  for (;;) {
    if (advDirty) {
      advDirty = false;
      updateAdvert(adv, advertising);
      advertising = true;
    }
    BLEScanResults* r = scan->start(SCAN_SECONDS, false);
    unsigned long now = millis();
    int n = r->getCount(), devices = 0, near = 0;
    for (int i = 0; i < n; i++) {
      BLEAdvertisedDevice d = r->getDevice(i);
      int rssi = d.getRSSI();
      char peer[16];
      if (pulseBoardName(d, peer, sizeof peer)) {
        if (notePeer(peer, rssi, now)) {
          peerBlip = millis();
          Serial.printf("ble: found %s (%d dBm)\n", peer, rssi);
        }
        continue; // Pulse boards aren't crowd
      }
      devices++;
      if (rssi >= NEAR_RSSI) near++;
    }
    scan->clearResults();
    expirePeers(now);
    bleDevices = devices;
    bleNear = near;
    bleScans++;
    bleLastScan = millis();
    Serial.printf("ble: %d devices, %d near, %d Pulse boards\n", devices, near, peerCount);
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
    ledcWrite(BLUE, (p < 60 || (p > 150 && p < 210)) ? 255 : 0); // double flash: joining Wi-Fi
    delay(10);
    if ((millis() - t0) % 500 < 10) Serial.print(".");
  }
  Serial.printf("\nZone light ready: http://%s\n", WiFi.localIP().toString().c_str());
  server.on("/level", handleLevel);
  server.on("/pulse", handlePulse);
  server.onNotFound([] { server.send(404, "text/plain", "try /level?v=red or /pulse\n"); });
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

  if (WiFi.status() != WL_CONNECTED) {
    WiFi.reconnect();
    unsigned long p = t % 600;
    ledcWrite(BLUE, (p < 60 || (p > 150 && p < 210)) ? 255 : 0);
    delay(5);
    return;
  }

  int pwm;
  switch (level) {
    case CALM: pwm = breathe(t, 3000, 4, 140); break;
    case WARN: pwm = breathe(t, 1100, 10, 255); break;
    default:   pwm = (t % 140) < 70 ? 255 : 0; break;
  }
  // Triple blip right after each Bluetooth scan (calm/yellow only, so red stays unambiguous).
  unsigned long since = bleLastScan ? t - bleLastScan : 99999;
  if (level != DANGER && since < 420) pwm = (since / 70) % 2 == 0 ? 255 : 0;
  // Short double blip when a new Pulse board is heard (shows after the scan's triple blip).
  unsigned long pb = peerBlip ? t - peerBlip : 99999;
  if (level != DANGER && pb >= 500 && pb < 700) pwm = (pb < 540 || (pb >= 620 && pb < 660)) ? 255 : 0;
  ledcWrite(BLUE, pwm);

  // External LEDs: green → orange → red, speeding up.
  int g = 0, y = 0, r = 0;
  switch (level) {
    case CALM: g = breathe(t, 3000, 10, 255); break;
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
