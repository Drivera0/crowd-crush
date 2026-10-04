# Pulse load test

Generated 2026-10-03T20:11:18-07:00 by `go run ./server/cmd/loadtest` (`make loadtest N=…`) against `ws://localhost:8080/ws/phone`.

**Machine:** AMD Ryzen 9 9950X 16-Core Processor, 32 hardware threads, linux 6.18.33.2-microsoft-standard-WSL2, go1.25.0. The load generator ran on the same machine as the server, so they shared the CPU.

Each fake phone sends a hello with a position (spread evenly over the venue), answers the server's clock-sync pings at once, and streams a 100 ms motion summary of calm standing (10 messages/s), like the phone page. Phones join evenly over the ramp; "steady state" starts 2 s after the ramp ends. One dashboard client times snapshots and `GET /api/status` is polled every second.

- **Ping delivery** is the time from the server stamping a clock-sync ping to the phone receiving it (one clock, one machine): how long the server's send path takes under load. The server stamps pings in whole milliseconds, so 0 means under 1 ms.
- **Round trip, server-measured** is the server's own `rtt` per phone (the latest ping → pong), read from snapshot `nodes[].rtt` once a second.

**Note:** the server was in `sim` mode during the test, so the dashboard snapshots showed that pipeline, not these phones: the snapshot size and node count are the sim's, the server-measured round trip of the load-test phones is not visible, and the server's `msgPerSec` includes the sim's messages. The load-test phones still went through the hub and the live detector underneath.

| | 500 phones, 1m0s | 1000 phones, 1m0s |
|---|---|---|
| Phones connected / failed / dropped | 500 / 0 / 0 | 1000 / 0 / 0 |
| Motion messages sent (total) | 274784 | 549530 |
| Messages/s sent, steady state (ideal 10 × N) | 5000.1 (ideal 5000) | 10000.0 (ideal 10000) |
| Server-reported msgPerSec, median | 6540.0 | 11540.0 |
| Ping delivery, server → phone (ms) p50 / p95 / p99 | 0.0 / 1.0 / 1.0 (n=8000) | 0.0 / 1.0 / 1.0 (n=16000) |
| Ping/pong round trip, server-measured (ms) p50 / p95 / p99 | — / — / — (n=0) | — / — / — (n=0) |
| Dashboard snapshots: rate (Hz) / gap p95 / gap max (ms) | 10.0 / 102.4 / 106.8 | 10.0 / 106.0 / 110.9 |
| Snapshot size (kB) median / max | 39.6 / 40.0 | 39.9 / 39.9 |
| Dashboard mode; nodes in last snapshot (ours) | sim; 154 (0) | sim; 154 (0) |
| stats.detectMs | not reported by this server build | not reported by this server build |
| GET /api/status (ms) p50 / p95 / max | 0.5 / 0.7 / 0.8 (errors 0) | 0.5 / 0.7 / 1.3 (errors 0) |

<details><summary>Timeline, 500 phones</summary>

```
t= 5s phones=251 sent=1234/s snapshots=51
t=10s phones=500 sent=3735/s snapshots=101
t=15s phones=500 sent=5000/s snapshots=151 lastSnapshot=40 kB
t=20s phones=500 sent=5000/s snapshots=201 lastSnapshot=40 kB
t=25s phones=500 sent=5000/s snapshots=251 lastSnapshot=40 kB
t=30s phones=500 sent=5000/s snapshots=301 lastSnapshot=40 kB
t=35s phones=500 sent=5000/s snapshots=351 lastSnapshot=40 kB
t=40s phones=500 sent=5000/s snapshots=401 lastSnapshot=40 kB
t=45s phones=500 sent=5000/s snapshots=451 lastSnapshot=40 kB
t=50s phones=500 sent=5000/s snapshots=501 lastSnapshot=40 kB
t=55s phones=500 sent=5000/s snapshots=551 lastSnapshot=40 kB
```
</details>

<details><summary>Timeline, 1000 phones</summary>

```
t= 5s phones=501 sent=2459/s snapshots=51
t=10s phones=1000 sent=7460/s snapshots=101
t=15s phones=1000 sent=10000/s snapshots=151 lastSnapshot=40 kB
t=20s phones=1000 sent=10000/s snapshots=201 lastSnapshot=40 kB
t=25s phones=1000 sent=10000/s snapshots=251 lastSnapshot=40 kB
t=30s phones=1000 sent=10000/s snapshots=301 lastSnapshot=40 kB
t=35s phones=1000 sent=10000/s snapshots=351 lastSnapshot=40 kB
t=40s phones=1000 sent=10000/s snapshots=401 lastSnapshot=40 kB
t=45s phones=1000 sent=10000/s snapshots=451 lastSnapshot=40 kB
t=50s phones=1000 sent=10000/s snapshots=501 lastSnapshot=40 kB
t=55s phones=1000 sent=10000/s snapshots=551 lastSnapshot=40 kB
```
</details>

