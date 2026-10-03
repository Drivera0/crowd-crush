#!/usr/bin/env bash
# Interactive .env writer. Press Enter to keep the current value (or skip).
# Every value is optional: anything left blank falls back and the demo works.
set -euo pipefail
cd "$(dirname "$0")/.."

# (macOS ships bash 3.2: no associative arrays, so values live in cur_<NAME>.)
get() { local n="cur_$1"; printf '%s' "${!n:-}"; }
put() { printf -v "cur_$1" '%s' "$2"; }
if [[ -f .env ]]; then
  while IFS='=' read -r k v || [[ -n "$k" ]]; do
    if [[ "$k" =~ ^[A-Z_]+$ ]]; then put "$k" "$v"; fi
  done < .env
fi

ask() { # name, secret(0/1), help…
  local name=$1 secret=$2; shift 2
  echo
  echo "── $name"
  for line in "$@"; do echo "   $line"; done
  local have shown=""
  have="$(get "$name")"
  if [[ -n "$have" ]]; then
    if [[ $secret == 1 ]]; then shown=" [set: …${have: -4}]"; else shown=" [$have]"; fi
  fi
  local val
  if [[ $secret == 1 ]]; then read -rsp "   value$shown: " val; echo; else read -rp "   value$shown: " val; fi
  if [[ "$val" == "-" ]]; then val=""; elif [[ -z "$val" ]]; then val="$have"; fi
  put "$name" "$val"
}

echo "Pulse .env setup. Enter keeps the current value, '-' clears it."

ask TIGER_DATABASE_URL 1 \
  "Tiger Data: https://console.cloud.timescale.com → Create service (free trial)" \
  "→ copy the connection string: postgres://tsdbadmin:PASSWORD@xxxx.tsdb.cloud.timescale.com:PORT/tsdb?sslmode=require"
ask GEMINI_API_KEY 1 \
  "Gemini: https://aistudio.google.com/apikey → Create API key (starts with AIza…)"
ask GEMINI_MODEL 0 \
  "Optional. Leave blank for gemini-flash-latest."
ask ELEVENLABS_API_KEY 1 \
  "ElevenLabs: https://elevenlabs.io/app/settings/api-keys → Create key (enable Text to Speech)"
ask ELEVENLABS_VOICE_ID 0 \
  "Optional. https://elevenlabs.io/app/voice-library → pick a voice → ⋯ → Copy voice ID." \
  "Blank uses a default voice."
ask SIGN_URL 0 \
  "IP(s) printed on the Arduino / ESP32 serial monitor (115200 baud) after flashing." \
  "Arduino matrix sign (shows the worst zone):  http://192.168.x.y" \
  "Plus ESP32 zone lights:  http://192.168.x.y,A=http://192.168.x.z,B=http://192.168.x.w"
ask PUBLIC_URL 0 \
  "Your .tech address once the tunnel is up, e.g. https://pulse.yourname.tech (for the QR code)."

umask 077
{
  echo "# Written by scripts/setup-env.sh — never commit this file."
  for k in TIGER_DATABASE_URL GEMINI_API_KEY GEMINI_MODEL ELEVENLABS_API_KEY ELEVENLABS_VOICE_ID SIGN_URL PUBLIC_URL; do
    echo "$k=$(get "$k")"
  done
} > .env
echo
echo "Wrote .env. Testing it…"
echo
if [[ -x bin/pulse ]]; then ./bin/pulse -check; else go run ./server/cmd/pulse -check; fi
