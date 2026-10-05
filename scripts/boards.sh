#!/usr/bin/env bash
# Pulse boards: find, flash and set up the Arduino sign and the ESP32 zone lights (macOS + Linux).
#
#   scripts/boards.sh status              what is plugged in / reachable: port, name, zone, firmware (up to date?), Wi-Fi, peers
#   scripts/boards.sh flash               compile + upload the right sketch to every board on USB, one at a time
#   scripts/boards.sh flash --port P      just that board
#   scripts/boards.sh wifi                save a Wi-Fi network on every board over USB (asks for name + password)
#   scripts/boards.sh beacon on|off       sign as Bluetooth beacon PULSE-S (no Wi-Fi, USB only) / back to Wi-Fi + USB
#   scripts/boards.sh env                 print the SIGN_URL line for the boards on USB
#   scripts/boards.sh env --write         ...and write it into .env (only that line changes; nothing else is printed)
#   scripts/boards.sh env --wifi          use the boards' Wi-Fi addresses instead of USB (for a server that can't see USB)
#   scripts/boards.sh preflight           the table-demo go/no-go list (needs Pulse running)
#
# Needs Go (builds bin/boards, the tool that talks to the boards) and, for flash, arduino-cli:
# on PATH, or the one inside the Arduino IDE 2 app on macOS. Cores are installed when missing.
# Pulse holds the USB ports while it runs: stop it (Ctrl-C) before flash or wifi; status asks it instead.
# Windows: pwsh scripts/boards.ps1 (same commands).

set -euo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo"
export PATH="$PATH:/usr/local/go/bin"

PORT_HTTP=${PULSE_PORT:-8080}
SIGN_FQBN=arduino:renesas_uno:unor4wifi
ZONE_FQBN=esp32:esp32:esp32:PartitionScheme=huge_app
ESP32_URL=https://espressif.github.io/arduino-esp32/package_esp32_index.json

say() { printf '%s\n' "$*"; }
die() { printf 'boards: %s\n' "$*" >&2; exit 1; }

# bin/boards, rebuilt when its sources are newer.
boards() {
  if [ ! -x bin/boards ] || [ -n "$(find server/cmd/boards server/internal/sign -name '*.go' -newer bin/boards 2>/dev/null | head -1)" ]; then
    command -v go >/dev/null || die "Go not found: install it (brew install go) — it builds bin/boards"
    go build -o bin/boards ./server/cmd/boards || die "couldn't build bin/boards"
  fi
  bin/boards "$@"
}

# A running Pulse with serial: boards in SIGN_URL holds their USB ports.
need_ports_free() {
  if curl -s -m 2 "http://localhost:$PORT_HTTP/api/hardware" 2>/dev/null | grep -q '"url":"serial:'; then
    die "Pulse is running on :$PORT_HTTP and holds the boards' USB ports. Stop it (Ctrl-C in its terminal), run this again, then start it."
  fi
}

arduino_cli() {
  local c
  for c in "$(command -v arduino-cli 2>/dev/null || true)" \
    "/Applications/Arduino IDE.app/Contents/Resources/app/lib/backend/resources/arduino-cli" \
    "$HOME/Applications/Arduino IDE.app/Contents/Resources/app/lib/backend/resources/arduino-cli" \
    "$HOME/.local/bin/arduino-cli" "$HOME/bin/arduino-cli"; do
    if [ -n "$c" ] && [ -x "$c" ]; then printf '%s\n' "$c"; return; fi
  done
  die "arduino-cli not found. Install the Arduino IDE 2 (https://www.arduino.cc/en/software) or: brew install arduino-cli"
}

ensure_cores() {
  local cli=$1 want=$2
  case " $want " in *" sign "*)
    "$cli" core list 2>/dev/null | grep -q '^arduino:renesas_uno ' || {
      say "Installing the Arduino UNO R4 core (once)…"
      "$cli" core update-index >/dev/null && "$cli" core install arduino:renesas_uno
    }
    "$cli" lib list 2>/dev/null | grep -q '^ArduinoBLE ' || {
      say "Installing the ArduinoBLE library (once; the sign's beacon mode)…"
      "$cli" lib install ArduinoBLE >/dev/null
    } ;;
  esac
  case " $want " in *" zone-light "*)
    "$cli" core list --additional-urls "$ESP32_URL" 2>/dev/null | grep -q '^esp32:esp32 ' || {
      say "Installing the ESP32 core (once, ~300 MB)…"
      "$cli" core update-index --additional-urls "$ESP32_URL" >/dev/null && "$cli" core install esp32:esp32 --additional-urls "$ESP32_URL"
    } ;;
  esac
}

# Builds one sketch into $tmp/out-<name>, with its secrets (or the example: Wi-Fi is optional)
# and a pulse_build.h carrying the build id.
build_sketch() {
  local cli=$1 name=$2 fqbn=$3 src="arduino/$2" dir="$tmp/$2"
  mkdir -p "$dir"
  cp "$src/$name.ino" "$dir/"
  if [ -f "$src/arduino_secrets.h" ]; then cp "$src/arduino_secrets.h" "$dir/"
  else cp "$src/arduino_secrets.h.example" "$dir/arduino_secrets.h"; say "  ($name: no arduino_secrets.h, building without Wi-Fi; USB works, or use: scripts/boards.sh wifi)"; fi
  local fw; fw=$(boards fwid "$src/$name.ino")
  printf '#define PULSE_FW "%s"\n' "$fw" > "$dir/pulse_build.h"
  say "Compiling $name firmware $fw…"
  "$cli" compile --fqbn "$fqbn" --output-dir "$tmp/out-$name" "$dir" >"$tmp/compile-$name.log" 2>&1 || { tail -20 "$tmp/compile-$name.log"; die "compile of $name failed"; }
}

zone_from_map() { # MAC tag → letter from arduino/zone-light/zones.map
  local mac=$1
  [ -f arduino/zone-light/zones.map ] || return 0
  grep -i "^[[:space:]]*$mac[[:space:]]*=" arduino/zone-light/zones.map | head -1 | sed 's/.*=[[:space:]]*//; s/[[:space:]]*$//' | tr '[:lower:]' '[:upper:]'
}

cmd_flash() {
  local only_port=""
  while [ $# -gt 0 ]; do
    case $1 in --port) only_port=$2; shift 2 ;; *) die "flash: unknown option $1" ;; esac
  done
  need_ports_free
  local cli; cli=$(arduino_cli)
  say "Looking for boards on USB…"
  local plan; plan=$(boards scan -plain)
  [ -n "$plan" ] || die "no boards on USB: use data cables; on a Mac an ESP32 may need the Silicon Labs CP210x driver (docs/TABLE-DEMO.md)"
  if [ -n "$only_port" ]; then plan=$(printf '%s\n' "$plan" | awk -F'\t' -v p="$only_port" '$1 == p'); [ -n "$plan" ] || die "$only_port isn't a board port"; fi
  if printf '%s\n' "$plan" | awk -F'\t' '$6 == "busy"' | grep -q .; then
    printf '%s\n' "$plan" | awk -F'\t' '$6 == "busy" {print "  " $1 ": busy"}'
    die "close what holds those ports (Arduino IDE Serial Monitor, another Pulse) and run this again"
  fi
  local want="" port sketch zone mac fw ok
  while IFS=$'\t' read -r port sketch zone mac fw ok; do
    case " $want " in *" $sketch "*) ;; *) [ "$sketch" != "-" ] && want="$want $sketch" ;; esac
  done <<EOF
$plan
EOF
  [ -n "$want" ] || die "no Arduino UNO R4 or ESP32 among the USB ports"
  ensure_cores "$cli" "$want"
  tmp=$(mktemp -d "${TMPDIR:-/tmp}/pulse-flash.XXXXXX")
  trap 'rm -rf "$tmp"' EXIT
  case " $want " in *" sign "*) build_sketch "$cli" sign "$SIGN_FQBN" ;; esac
  case " $want " in *" zone-light "*) build_sketch "$cli" zone-light "$ZONE_FQBN" ;; esac
  while IFS=$'\t' read -r port sketch zone mac fw ok; do
    [ "$sketch" = "-" ] && { say "Skipping $port (not a Pulse board)"; continue; }
    local fqbn=$SIGN_FQBN; [ "$sketch" = zone-light ] && fqbn=$ZONE_FQBN
    say "Uploading $sketch to $port…"
    if ! "$cli" upload --fqbn "$fqbn" --port "$port" --input-dir "$tmp/out-$sketch" "$tmp/$sketch" >"$tmp/upload.log" 2>&1 </dev/null; then
      tail -15 "$tmp/upload.log"
      if [ "$sketch" = sign ]; then die "upload to the sign failed: close any serial monitor; still stuck, double-tap its RESET button (the LED fades) and run this again"; fi
      die "upload to $port failed: if it stuck at 'Connecting…', hold the ESP32's BOOT button while it connects"
    fi
  done <<EOF
$plan
EOF
  say "Waiting for the boards to start…"
  sleep 4
  # Zone lights keep the zone they know (it's in their flash); one without gets zones.map's letter, else the next free one.
  local after used=" " letter
  after=$(boards scan -plain)
  while IFS=$'\t' read -r port sketch zone mac fw ok; do
    [ "$sketch" = zone-light ] && [ "$zone" != "-" ] && used="$used$zone "
  done <<EOF
$after
EOF
  while IFS=$'\t' read -r port sketch zone mac fw ok; do
    [ "$sketch" = zone-light ] && [ "$ok" = yes ] && [ "$zone" = "-" ] || continue
    letter=$(zone_from_map "$mac")
    if [ -z "$letter" ]; then
      for letter in A B C D E F G H; do case "$used" in *" $letter "*) ;; *) break ;; esac; done
    fi
    used="$used$letter "
    boards zone -port "$port" -zone "$letter" </dev/null
  done <<EOF
$after
EOF
  say
  boards status
  say
  say "Next: scripts/boards.sh env --write (if SIGN_URL isn't set yet), then start Pulse."
}

cmd_wifi() {
  local only_port=""
  while [ $# -gt 0 ]; do
    case $1 in --port) only_port=$2; shift 2 ;; *) die "wifi: unknown option $1" ;; esac
  done
  need_ports_free
  local ssid pass
  say "Saves a Wi-Fi network on each board over USB (kept in its flash, tried before the built-in ones)."
  say "2.4 GHz only. iPhone hotspot: turn on Maximize Compatibility."
  read -r -p "Wi-Fi name: " ssid
  [ -n "$ssid" ] || die "no name given"
  read -r -s -p "Password (not shown): " pass; echo
  local plan port sketch zone mac fw ok n=0
  plan=$(boards scan -plain)
  while IFS=$'\t' read -r port sketch zone mac fw ok; do
    [ -n "$port" ] && [ "$ok" = yes ] || continue
    [ -z "$only_port" ] || [ "$port" = "$only_port" ] || continue
    n=$((n + 1))
    printf '%s\n' "$pass" | boards wifi -port "$port" -ssid "$ssid" || say "  ($port keeps working over USB meanwhile)"
  done <<EOF
$plan
EOF
  [ "$n" -gt 0 ] || die "no board answered on USB (flash them first: scripts/boards.sh flash)"
}

# The sign's Bluetooth beacon mode, a switch kept in its flash (no reflash). On: it advertises
# PULSE-S for the phones and zone lights, with no Wi-Fi (drive it over USB: SIGN_URL=serial:auto).
cmd_beacon() {
  local want="" only_port=""
  while [ $# -gt 0 ]; do
    case $1 in on|off) want=$1; shift ;; --port) only_port=$2; shift 2 ;; *) die "beacon: want on or off [--port P]" ;; esac
  done
  [ -n "$want" ] || die "beacon: want on or off"
  need_ports_free
  local port="$only_port"
  if [ -z "$port" ]; then
    port=$(boards scan -plain | awk -F'\t' '$2 == "sign" && $6 == "yes" {print $1; exit}')
    [ -n "$port" ] || die "no sign answered on USB (plugged in? firmware current? scripts/boards.sh flash)"
  fi
  local flag=""; [ "$want" = on ] && flag=-on
  boards beacon -port "$port" $flag </dev/null
}

cmd_env() {
  local args=()
  while [ $# -gt 0 ]; do
    case $1 in
      --write) args+=(-write .env); shift ;;
      --wifi) args+=(-wifi); shift ;;
      *) die "env: unknown option $1" ;;
    esac
  done
  need_ports_free
  boards env ${args[@]+"${args[@]}"}
}

cmd=${1:-status}
[ $# -gt 0 ] && shift
case $cmd in
  status) boards status -server "http://localhost:$PORT_HTTP" "$@" ;;
  scan) need_ports_free; boards scan "$@" ;;
  flash) cmd_flash "$@" ;;
  wifi) cmd_wifi "$@" ;;
  beacon) cmd_beacon "$@" ;;
  env) cmd_env "$@" ;;
  preflight) exec "$repo/scripts/preflight.sh" "$@" ;;
  -h|--help|help) sed -n '2,18p' "$0" | sed 's/^# \{0,1\}//' ;;
  *) die "unknown command $cmd (status, flash, wifi, beacon, env, preflight)" ;;
esac
