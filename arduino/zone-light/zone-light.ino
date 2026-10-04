// Pulse zone light — ESP32 DevKit V1 (one per zone).
//
// Same API as the Arduino sign: GET /level?v=calm|yellow|red&zone=A
// Plus GET /pulse → JSON status for the dashboard's hardware panel and for the
// server to find boards on the network (kind, zone, level, Wi-Fi signal,
// uptime, Bluetooth devices heard).
//
// Onboard blue LED (GPIO 2), no wiring needed:
//   joining Wi-Fi = quick double flash (stays like this if Wi-Fi is wrong)
//   calm          = slow breathing pulse
//   yellow        = faster, brighter breathing
//   red           = rapid strobe
//   + a quick triple blip each time a Bluetooth scan finishes (~every 6 s)
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
// how many devices are around, not a list of who.
//
// Arduino IDE: install "esp32 by Espressif", board "ESP32 Dev Module",
// Tools → Partition Scheme → "Huge APP" (Wi-Fi + Bluetooth don't fit the default).
// Copy arduino_secrets.h.example → arduino_secrets.h. Or: pwsh scripts/flash-zone-lights.ps1

#include <WiFi.h>
#include <WebServer.h>
#include <BLEDevice.h>
#include <BLEScan.h>
#include "arduino_secrets.h"

const bool RGB_LED = false; // true: one RGB LED on 27 (R) / 25 (G) instead of three LEDs
const int BLUE = 2, GREEN = 25, YELLOW = 26, RED = 27, BUZZER = 14;
const int SCAN_SECONDS = 5;
const int NEAR_RSSI = -70; // dBm: roughly within a few metres

WebServer server(80);
enum Level { CALM, WARN, DANGER };
Level level = CALM;
String zone = "";

// Bluetooth counter, written by the scan task, read by the web handler.
volatile int bleDevices = -1, bleNear = -1;
volatile unsigned long bleScans = 0, bleLastScan = 0;

const char* levelName(Level l) { return l == DANGER ? "red" : l == WARN ? "yellow" : "calm"; }

void handleLevel() {
  String v = server.arg("v");
  level = v == "red" ? DANGER : v == "yellow" ? WARN : CALM;
  if (server.hasArg("zone")) zone = server.arg("zone");
  Serial.printf("level %s zone %s\n", v.c_str(), zone.c_str());
  server.send(200, "text/plain", "ok\n");
}

void handlePulse() {
  char buf[320];
  snprintf(buf, sizeof buf,
           "{\"kind\":\"zone-light\",\"zone\":\"%s\",\"level\":\"%s\",\"ip\":\"%s\",\"rssi\":%d,\"uptime\":%lu,"
           "\"ble\":{\"devices\":%d,\"near\":%d,\"scans\":%lu,\"age\":%lu}}",
           zone.c_str(), levelName(level), WiFi.localIP().toString().c_str(), WiFi.RSSI(), millis() / 1000,
           bleDevices, bleNear, bleScans, bleLastScan ? (millis() - bleLastScan) / 1000 : 0UL);
  server.sendHeader("Access-Control-Allow-Origin", "*");
  server.send(200, "application/json", buf);
}

// Scans run on the other core so the LED and web server never stall.
void bleTask(void*) {
  BLEDevice::init("");
  BLEScan* scan = BLEDevice::getScan();
  scan->setActiveScan(false); // listen only: never ask devices for more data
  scan->setInterval(160);
  scan->setWindow(80); // half duty cycle leaves airtime for Wi-Fi
  for (;;) {
    BLEScanResults* r = scan->start(SCAN_SECONDS, false);
    int n = r->getCount(), near = 0;
    for (int i = 0; i < n; i++)
      if (r->getDevice(i).getRSSI() >= NEAR_RSSI) near++;
    scan->clearResults();
    bleDevices = n;
    bleNear = near;
    bleScans++;
    bleLastScan = millis();
    Serial.printf("ble: %d devices, %d near\n", n, near);
    vTaskDelay(pdMS_TO_TICKS(1000));
  }
}

void setup() {
  Serial.begin(115200);
  pinMode(BUZZER, OUTPUT);
  for (int p : {BLUE, GREEN, YELLOW, RED}) ledcAttach(p, 5000, 8); // PWM so LEDs can breathe
  WiFi.mode(WIFI_STA); // modem sleep stays on: required when Wi-Fi and Bluetooth share the radio
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
