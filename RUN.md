# Running Pulse

The short version. Full details are in [README.md](README.md) and [docs/SETUP.md](docs/SETUP.md).

## Start it

```sh
cd ~/Projects/Personal/crowd-crush
scripts/live.sh        # or: make live
```

This starts Pulse and the Cloudflare tunnel together and keeps the Mac awake. Leave the terminal open. **Ctrl-C stops everything.**

Wait for these lines before opening anything:

```
Dashboard: http://localhost:8080/dash/   Phones: https://pulsecrowd.tech/
[tunnel] ... Registered tunnel connection ...
```

## Open it

| What | Address |
|---|---|
| **Dashboard** (laptop screen) | https://pulsecrowd.tech/dash/ (or http://localhost:8080/dash/ on this laptop) |
| **Phone page** (what the QR code opens) | https://pulsecrowd.tech/ |

On the dashboard: click ⛶ for full screen, then **Enable sound** once so the voice briefing can play.

## Check everything works

1. **Keys and boards:** with Pulse stopped, run `make doctor`. Every line should be ✓:

   ```
   ✓  Tiger Data  connected, tables ready
   ✓  Gemini      "<a test briefing>"
   ✓  ElevenLabs  wrote ....mp3
   ✓  Sign        http://... (zone A), ...
   ✗  Public URL  answered 530   ← fine: it only passes while scripts/live.sh is running
   ```

2. **Website:** run `scripts/live.sh`, then open https://pulsecrowd.tech/dash/. The dashboard header shows which services are live.

3. **Detection, without phones:** in a second terminal run `make sim`. Fake phones appear on the dashboard, and within ~30 s a zone goes yellow, then red, a briefing shows and plays, and the sign flashes. Ctrl-C the sim when done.

4. **A real phone:** scan the QR on the dashboard, tap **Join**, allow motion, hold the phone flat on your chest. Your dot appears and goes green within a few seconds.

5. **Before judging:** `make preflight` (with `scripts/live.sh` running) runs the full go/no-go list: public URL, each board, keys.

## If something's wrong

| You see | Means | Fix |
|---|---|---|
| Cloudflare **Error 1033** or **530** on pulsecrowd.tech | the tunnel isn't running | start `scripts/live.sh`, wait a few seconds |
| Cloudflare **502** | tunnel is up but Pulse isn't | restart `scripts/live.sh` |
| `Something is already listening on :8080` | an old Pulse is still running | `pkill -f bin/pulse`, then start again |
| Tunnel won't connect on campus Wi-Fi | network blocks it | put the laptop on your phone's hotspot; or `QUICK=1 scripts/live.sh` for a temporary `trycloudflare.com` address (the QR follows it) |
| Gemini / ElevenLabs / Tiger Data show as off | key missing or wrong in `.env` | `make env` re-enters them; `make doctor` re-tests |
| Sign or zone lights A/B don't light at campus | `SIGN_URL` in `.env` holds all three boards' **home** Wi-Fi IPs | plug all three boards into the laptop by USB (data cables; a hub is fine), stop Pulse, run `scripts/boards.sh env --write` (sets `SIGN_URL=serial:auto,A=serial:auto,B=serial:auto`), check with `scripts/boards.sh status` |

Nothing external is required: with any key missing, Pulse still detects and falls back (template briefing, browser voice, local recordings).

## On a new Mac

Needs Go, Node and cloudflared (`brew install go node cloudflared`), then:

```sh
make build
make env                    # enter the keys; writes .env (never commit it)
cloudflared tunnel login    # browser opens; pick pulsecrowd.tech
cloudflared tunnel token --cred-file ~/.cloudflared/cedca08e-40ca-424e-9d06-8ea49e636b06.json pulse
```

and create `~/.cloudflared/config.yml`:

```yaml
tunnel: cedca08e-40ca-424e-9d06-8ea49e636b06
credentials-file: /Users/<you>/.cloudflared/cedca08e-40ca-424e-9d06-8ea49e636b06.json
protocol: http2
ingress:
  - hostname: pulsecrowd.tech
    service: http://localhost:8080
  - service: http_status:404
```

Make sure `.env` has `PUBLIC_URL=https://pulsecrowd.tech`.
