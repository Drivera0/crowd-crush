// Pulse warning sign — Arduino Uno R4 WiFi.
//
// Joins Wi-Fi and runs a tiny HTTP server:
//   GET /level?v=calm|yellow|red&zone=B
// calm   = slow heartbeat dot
// yellow = steady "!"
// red    = flashing arrow, alternating with "STOP"
//   GET /pulse → status JSON for the dashboard / server discovery:
//   {"kind":"sign","level":"calm","zone":"B","rssi":-58,"uptime":123}
//   (rssi = Wi-Fi signal in dBm, uptime in seconds)
//
// No Bluetooth beacon here: the R4's Wi-Fi and Bluetooth share one radio
// module and ArduinoBLE can't run alongside WiFiS3, so the sign doesn't show
// up in the zone lights' "peers"; the dashboard places it from Wi-Fi only.
//
// Copy arduino_secrets.h.example to arduino_secrets.h and fill in your Wi-Fi.
// The IP is printed on the Serial Monitor (115200 baud); set
// SIGN_URL=http://<that ip> on the server.

#include <WiFiS3.h>
#include "Arduino_LED_Matrix.h"
#include "arduino_secrets.h"

// Optional: a buzzer or big LED on this pin turns on while red.
const int ALARM_PIN = 7;

ArduinoLEDMatrix matrix;
WiFiServer server(80);

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

void connectWiFi() {
  matrix.renderBitmap(bang, 8, 12);
  while (WiFi.status() != WL_CONNECTED) {
    Serial.print("Connecting to ");
    Serial.println(SECRET_SSID);
    WiFi.begin(SECRET_SSID, SECRET_PASS);
    for (int i = 0; i < 20 && WiFi.status() != WL_CONNECTED; i++) delay(500);
  }
  // The R4 sometimes reports 0.0.0.0 for a moment after connecting.
  while (WiFi.localIP() == IPAddress(0, 0, 0, 0)) delay(200);
  Serial.print("Sign ready: SIGN_URL=http://");
  Serial.println(WiFi.localIP());
  server.begin();
}

void setup() {
  Serial.begin(115200);
  pinMode(ALARM_PIN, OUTPUT);
  matrix.begin();
  connectWiFi();
}

const char* levelName() { return level == RED ? "red" : level == YELLOW ? "yellow" : "calm"; }

// Parses "GET /level?v=red&zone=B HTTP/1.1" or "GET /pulse HTTP/1.1".
Route handleRequestLine(const String& line) {
  if (line.startsWith("GET /pulse ") || line.startsWith("GET /pulse?")) return PULSE;
  if (!line.startsWith("GET /level")) return NOT_FOUND;
  int v = line.indexOf("v=");
  if (v < 0) return NOT_FOUND;
  String rest = line.substring(v + 2);
  if (rest.startsWith("red")) level = RED;
  else if (rest.startsWith("yellow")) level = YELLOW;
  else level = CALM;
  int z = line.indexOf("zone=");
  zone[0] = 0;
  if (z >= 0) {
    int i = 0;
    for (int p = z + 5; p < (int)line.length() && i < 7; p++) {
      char c = line[p];
      if (c == ' ' || c == '&') break;
      zone[i++] = c;
    }
    zone[i] = 0;
  }
  lastChange = millis();
  Serial.print("level ");
  Serial.print(level);
  Serial.print(" zone ");
  Serial.println(zone);
  return LEVEL;
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
  if (route == PULSE) {
    // zone holds only what came after "zone=" up to a space or '&'; strip quotes/backslashes for JSON.
    char z[8];
    int j = 0;
    for (int i = 0; zone[i] && j < 7; i++)
      if (zone[i] != '"' && zone[i] != '\\') z[j++] = zone[i];
    z[j] = 0;
    char body[128];
    snprintf(body, sizeof body, "{\"kind\":\"sign\",\"level\":\"%s\",\"zone\":\"%s\",\"rssi\":%d,\"uptime\":%lu}",
             levelName(), z, (int)WiFi.RSSI(), millis() / 1000);
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
      // Heartbeat: a short blink every 1.5 s.
      show(((t % 1500) < 120) ? heart : blank);
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
  if (WiFi.status() != WL_CONNECTED) connectWiFi();
  serveClient();
  render();
  delay(10);
}
