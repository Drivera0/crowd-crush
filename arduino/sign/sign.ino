// Pulse warning sign — Arduino Uno R4 WiFi.
//
// Joins Wi-Fi and runs a tiny HTTP server:
//   GET /level?v=calm|yellow|red&zone=B
// calm   = slow heartbeat dot
// yellow = steady "!"
// red    = flashing arrow, alternating with "STOP"
//   GET /pulse → status JSON for the dashboard / server discovery:
//   {"kind":"sign","level":"calm","zone":"B","rssi":-58,"uptime":123,"wifi":true}
//   (rssi = Wi-Fi signal in dBm, uptime in seconds)
//
// The same commands work over the USB cable (Serial, 115200 baud), one per
// line, so the sign needs no Wi-Fi when it's plugged into the laptop
// (SIGN_URL=serial:auto on the server):
//   L <calm|yellow|red> [zone]   set the level, exactly like /level
//   S                            reply with one /pulse-shaped JSON line
//
// Wi-Fi is optional: if it hasn't connected within 15 s the sign carries on
// over USB only (a small "USB" shows while calm) and keeps retrying Wi-Fi in
// the background.
//
// Beacon mode (SIGN_BEACON 1, e.g. scripts/flash-sign.ps1 -Beacon): the sign
// advertises itself over Bluetooth as "PULSE-S", like the zone lights, so
// phones and the other boards can use it as a third position anchor. The
// R4's Wi-Fi and Bluetooth share one radio module and ArduinoBLE can't run
// alongside WiFiS3, so a beacon sign has no Wi-Fi: it is driven over USB only
// (SIGN_URL=serial:auto). Needs the ArduinoBLE library.
//
// Copy arduino_secrets.h.example to arduino_secrets.h and fill in your Wi-Fi.
// The IP is printed on the Serial Monitor (115200 baud); set
// SIGN_URL=http://<that ip> on the server.

#ifndef SIGN_BEACON
#define SIGN_BEACON 0
#endif
#ifndef BEACON_NAME
#define BEACON_NAME "PULSE-S"
#endif

#if SIGN_BEACON
#include <ArduinoBLE.h>
#else
#include <WiFiS3.h>
#endif
#include "Arduino_LED_Matrix.h"
#include "arduino_secrets.h"

// Optional: a buzzer or big LED on this pin turns on while red.
const int ALARM_PIN = 7;

ArduinoLEDMatrix matrix;
#if !SIGN_BEACON
WiFiServer server(80);
#endif

enum Level { CALM, YELLOW, RED };
enum Route { NOT_FOUND, LEVEL, PULSE };
Level level = CALM;
char zone[8] = "";
unsigned long lastChange = 0;

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

#if SIGN_BEACON
bool bleUp = false;

// beginBeacon starts a non-connectable advert carrying only the name.
void beginBeacon() {
  if (!BLE.begin()) {
    Serial.println("ble: failed to start");
    return;
  }
  BLE.setLocalName(BEACON_NAME);
  BLE.setDeviceName(BEACON_NAME);
  BLE.setConnectable(false);
  bleUp = BLE.advertise();
  Serial.print("ble: advertising as ");
  Serial.println(bleUp ? BEACON_NAME : "(failed)");
}

void pollWiFi() {}
#else
void beginWiFi() {
  Serial.print("Connecting to ");
  Serial.println(SECRET_SSID);
  WiFi.begin(SECRET_SSID, SECRET_PASS);
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
    beginWiFi();
  } else if (!connected) {
    bool grace = now - wifiSince < WIFI_GRACE_MS;
    if (now - lastBegin >= (grace ? WIFI_RETRY_FAST_MS : WIFI_RETRY_MS)) beginWiFi();
  }
}

#endif

// usbOnly: Wi-Fi has been down for longer than the grace period (always, in beacon mode).
bool usbOnly() { return SIGN_BEACON || (!wifiUp && millis() - wifiSince >= WIFI_GRACE_MS); }

void setup() {
  Serial.begin(115200);
  pinMode(ALARM_PIN, OUTPUT);
  matrix.begin();
  matrix.renderBitmap(bang, 8, 12);
#if SIGN_BEACON
  beginBeacon();
#else
  // The radio module's firmware version (Bluetooth beacon mode needs 0.2.0 or newer).
  Serial.print("radio firmware ");
  Serial.println(WiFi.firmwareVersion());
  WiFi.setTimeout(1000);
  wifiSince = millis();
  beginWiFi();
#endif
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
#if SIGN_BEACON
  snprintf(body, n, "{\"kind\":\"sign\",\"level\":\"%s\",\"zone\":\"%s\",\"rssi\":0,\"uptime\":%lu,\"wifi\":false,\"name\":\"%s\",\"ble\":%s}",
           levelName(), z, millis() / 1000, BEACON_NAME, bleUp ? "true" : "false");
#else
  snprintf(body, n, "{\"kind\":\"sign\",\"level\":\"%s\",\"zone\":\"%s\",\"rssi\":%d,\"uptime\":%lu,\"wifi\":%s}",
           levelName(), z, wifiUp ? (int)WiFi.RSSI() : 0, millis() / 1000, wifiUp ? "true" : "false");
#endif
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

// USB commands, one per line: "L <level> [zone]" or "S".
char serialBuf[32];
int serialLen = 0;

void handleSerialLine(char* s) {
  while (*s == ' ') s++;
  if (s[0] == 'S' && (s[1] == 0 || s[1] == ' ')) {
    char body[160];
    statusJSON(body, sizeof body);
    Serial.println(body);
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

#if !SIGN_BEACON
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
  if (route == PULSE) {
    char body[160];
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
#endif

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
      if (usbOnly()) show(((t % 1500) < 120) ? blank : usb);
      else if (!wifiUp) show(bang);  // still trying Wi-Fi (first 15 s)
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
#if SIGN_BEACON
  BLE.poll();
#else
  pollWiFi();
  if (wifiUp) serveClient();
#endif
  render();
  delay(10);
}
