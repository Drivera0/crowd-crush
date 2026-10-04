#!/usr/bin/env bash
# Table-demo go/no-go list, against the Pulse server that is already running:
# public URL through the tunnel, every board driven (red for 1 s, read back),
# demo spot, keys present, disk space. Each line says what was actually checked.
#
#   scripts/preflight.sh            # server on :8080
#   PULSE_ADDR=:8096 scripts/preflight.sh
set -euo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo"
export PATH="$PATH:/usr/local/go/bin"
if [ ! -x bin/pulse ]; then
  echo "Building bin/pulse…"
  go build -o bin/pulse ./server/cmd/pulse
fi
exec bin/pulse -preflight -addr "${PULSE_ADDR:-:8080}" "$@"
