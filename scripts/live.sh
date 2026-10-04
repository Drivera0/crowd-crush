#!/usr/bin/env bash
# Start Pulse and the Cloudflare tunnel together; Ctrl-C stops both.
# Uses the named tunnel (~/.cloudflared, e.g. pulsecrowd.tech) when it's set up
# on this machine, else a quick tunnel with a random trycloudflare.com URL
# (the dashboard QR finds that one by itself). Keeps the Mac awake while running.
#
#   scripts/live.sh                 # tunnel "pulse", server on :8080
#   TUNNEL=other scripts/live.sh    # another named tunnel
#   QUICK=1 scripts/live.sh         # force a quick tunnel
#   scripts/live.sh -layout line    # anything else goes to bin/pulse
set -euo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo"

tunnel=${TUNNEL:-pulse}
port=8080

command -v cloudflared >/dev/null || { echo "cloudflared not found: brew install cloudflared"; exit 1; }
if [ ! -x bin/pulse ]; then
  echo "Building bin/pulse…"
  make build
fi
if curl -s -o /dev/null -m 1 "http://localhost:$port/"; then
  echo "Something is already listening on :$port (an old bin/pulse?). Stop it first."
  exit 1
fi

pids=()
cleanup() {
  trap - INT TERM EXIT
  echo
  echo "Stopping…"
  for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done
  wait 2>/dev/null || true
}
trap 'cleanup; exit 0' INT TERM
trap cleanup EXIT

bin/pulse "$@" > >(sed -u 's/^/[pulse]  /') 2>&1 &
pids+=($!)

for _ in $(seq 1 40); do
  curl -s -o /dev/null -m 1 "http://localhost:$port/" && break
  sleep 0.25
done
curl -s -o /dev/null -m 1 "http://localhost:$port/" || { echo "Pulse didn't start on :$port"; exit 1; }

if [ -z "${QUICK:-}" ] && [ -f "$HOME/.cloudflared/config.yml" ] && cloudflared tunnel info "$tunnel" >/dev/null 2>&1; then
  echo "Tunnel: named tunnel \"$tunnel\""
  cloudflared tunnel run "$tunnel" > >(sed -u 's/^/[tunnel] /') 2>&1 &
else
  echo "Tunnel: quick tunnel (random URL; the dashboard QR picks it up)"
  cloudflared tunnel --protocol http2 --url "http://localhost:$port" > >(sed -u 's/^/[tunnel] /') 2>&1 &
fi
pids+=($!)

if command -v caffeinate >/dev/null; then
  caffeinate -dimsu &
  pids+=($!)
fi

url=$(curl -s -m 2 "http://localhost:$port/api/join" | sed -n 's/.*"url":"\([^"]*\)".*/\1/p')
echo "Dashboard: http://localhost:$port/dash/   Phones: ${url:-see the dashboard QR}   (Ctrl-C stops everything)"

# Stop everything if either one dies.
while :; do
  for p in "${pids[@]}"; do
    kill -0 "$p" 2>/dev/null || { echo "A process exited."; exit 1; }
  done
  sleep 1
done
