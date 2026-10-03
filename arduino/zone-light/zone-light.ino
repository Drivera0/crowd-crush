// Pulse zone light — ESP32 DevKit V1 (one per zone).
//
// Same API as the Arduino sign: GET /level?v=calm|yellow|red&zone=A
// Onboard blue LED (GPIO 2) works with no wiring:
//   calm = short blink every 2 s, yellow = slow blink, red = fast strobe.
// Optional wiring (each LED through a 220 Ω resistor to GND):
//   GPIO 25 green, GPIO 26 yellow, GPIO 27 red, GPIO 14 active buzzer.
//
// Arduino IDE: Boards Manager → install "esp32 by Espressif", board
// "ESP32 Dev Module". Copy arduino_secrets.h.example → arduino_secrets.h.
// Then SIGN_URL=...,A=http://<ip printed on serial monitor>

#include <WiFi.h>
#include <WebServer.h>
#include "arduino_secrets.h"

const int LED_BUILTIN_PIN = 2;
const int GREEN = 25, YELLOW = 26, RED = 27, BUZZER = 14;

WebServer server(80);
enum Level { CALM, WARN, DANGER };
Level level = CALM;

void handleLevel() {
  String v = server.arg("v");
  level = v == "red" ? DANGER : v == "yellow" ? WARN : CALM;
  Serial.printf("level %s zone %s\n", v.c_str(), server.arg("zone").c_str());
  server.send(200, "text/plain", "ok\n");
}

void setup() {
  Serial.begin(115200);
  for (int p : {LED_BUILTIN_PIN, GREEN, YELLOW, RED, BUZZER}) pinMode(p, OUTPUT);
  WiFi.mode(WIFI_STA);
  WiFi.begin(SECRET_SSID, SECRET_PASS);
  Serial.print("Connecting");
  while (WiFi.status() != WL_CONNECTED) {
    digitalWrite(LED_BUILTIN_PIN, !digitalRead(LED_BUILTIN_PIN));
    delay(250);
    Serial.print(".");
  }
  Serial.printf("\nZone light ready: http://%s\n", WiFi.localIP().toString().c_str());
  server.on("/level", handleLevel);
  server.onNotFound([] { server.send(404, "text/plain", "try /level?v=red\n"); });
  server.begin();
}

void loop() {
  server.handleClient();
  if (WiFi.status() != WL_CONNECTED) WiFi.reconnect();
  unsigned long t = millis();
  bool on;
  switch (level) {
    case CALM:   on = (t % 2000) < 80; break;
    case WARN:   on = (t % 1000) < 500; break;
    default:     on = (t % 200) < 100; break;
  }
  digitalWrite(LED_BUILTIN_PIN, on);
  digitalWrite(GREEN, level == CALM);
  digitalWrite(YELLOW, level == WARN);
  digitalWrite(RED, level == DANGER && on);
  digitalWrite(BUZZER, level == DANGER && (t % 1000) < 150);
  delay(5);
}
