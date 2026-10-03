import './style.css';
import type { Config, Hello, Motion, Pong, ToPhone } from '../../shared/protocol';
import { wsURL } from '../../shared/protocol';

const $ = <T extends HTMLElement = HTMLElement>(id: string) => document.getElementById(id) as T;

// ---- identity: random per browser session, nothing else ----

function randomId(): string {
  if (crypto.randomUUID) return crypto.randomUUID();
  const b = crypto.getRandomValues(new Uint8Array(16));
  return Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('');
}

function load(key: string): string | null {
  try {
    return sessionStorage.getItem(key);
  } catch {
    return null;
  }
}
function save(key: string, v: string) {
  try {
    sessionStorage.setItem(key, v);
  } catch {
    /* private mode: fine, we just forget */
  }
}

const id = load('pulse-id') ?? randomId();
save('pulse-id', id);

let spot: { row: number; col: number } | null = (() => {
  const s = load('pulse-spot');
  return s ? (JSON.parse(s) as { row: number; col: number }) : null;
})();

function deviceLabel(): string {
  const ua = navigator.userAgent;
  if (/iPhone/.test(ua)) return 'iPhone';
  if (/iPad/.test(ua)) return 'iPad';
  if (/Android/.test(ua)) return 'Android';
  return 'browser';
}

function show(screen: 'join' | 'pick' | 'live') {
  for (const s of ['join', 'pick', 'live']) $(s).hidden = s !== screen;
}

// ---- 1. Join: motion permission must be asked inside the tap handler ----

type PermissionFn = () => Promise<'granted' | 'denied'>;

$('joinBtn').addEventListener('click', async () => {
  const err = $('joinErr');
  err.hidden = true;
  const req = (DeviceMotionEvent as unknown as { requestPermission?: PermissionFn }).requestPermission;
  if (typeof req === 'function') {
    try {
      const r = await req.call(DeviceMotionEvent);
      if (r !== 'granted') {
        err.textContent = 'Motion access was denied. In Safari: aA menu → Website Settings → Motion & Orientation Access, then reload.';
        err.hidden = false;
        return;
      }
    } catch (e) {
      err.textContent = `Couldn't ask for motion access (${e}). This page needs HTTPS.`;
      err.hidden = false;
      return;
    }
  } else if (!('DeviceMotionEvent' in window)) {
    err.textContent = 'This browser has no motion sensors.';
    err.hidden = false;
    return;
  }
  void keepAwake();
  startSensors();
  if (spot) {
    startLive();
  } else {
    await showPicker();
  }
});

// ---- 2. Grid picker ----

async function showPicker() {
  let cfg: Pick<Config, 'rows' | 'cols'> = { rows: 1, cols: 8 };
  try {
    const r = await fetch('/api/config');
    if (r.ok) cfg = (await r.json()) as Config;
  } catch {
    /* use the default line */
  }
  const grid = $('grid');
  grid.innerHTML = '';
  grid.style.gridTemplateColumns = `repeat(${cfg.cols}, minmax(0, 1fr))`;
  $('pickHint').textContent =
    cfg.rows > 1
      ? 'Row A is the front. Count spots from the left.'
      : 'Stand in a line. Spot 1 is the left end as the dashboard sees it.';
  for (let r = 0; r < cfg.rows; r++) {
    for (let c = 0; c < cfg.cols; c++) {
      const b = document.createElement('button');
      b.textContent = cfg.rows > 1 ? `${String.fromCharCode(65 + r)}${c + 1}` : String(c + 1);
      if (spot && spot.row === r && spot.col === c) b.classList.add('mine');
      b.addEventListener('click', () => {
        spot = { row: r, col: c };
        save('pulse-spot', JSON.stringify(spot));
        startLive();
      });
      grid.appendChild(b);
    }
  }
  show('pick');
}

$('moveBtn').addEventListener('click', () => void showPicker());

// ---- 3. Motion: summarise every 100 ms ----

let sum = { x: 0, y: 0, z: 0, n: 0, rot: 0 };
let gravity: { x: number; y: number; z: number } | null = null;
let totalSamples = 0;
let sensorsOn = false;

function onMotion(e: DeviceMotionEvent) {
  let x: number, y: number, z: number;
  const a = e.acceleration;
  if (a && a.x != null && a.y != null && a.z != null) {
    x = a.x;
    y = a.y;
    z = a.z;
  } else {
    // No gyro-fused acceleration: subtract a running mean (≈ gravity).
    const g = e.accelerationIncludingGravity;
    if (!g || g.x == null || g.y == null || g.z == null) return;
    const dt = (e.interval > 1 ? e.interval : e.interval * 1000) || 16; // ms (some browsers report seconds)
    const k = Math.min(1, dt / 1000); // ~1 s time constant
    if (!gravity) gravity = { x: g.x, y: g.y, z: g.z };
    gravity.x += k * (g.x - gravity.x);
    gravity.y += k * (g.y - gravity.y);
    gravity.z += k * (g.z - gravity.z);
    x = g.x - gravity.x;
    y = g.y - gravity.y;
    z = g.z - gravity.z;
  }
  const r = e.rotationRate;
  const rot = r ? Math.hypot(r.alpha ?? 0, r.beta ?? 0, r.gamma ?? 0) : 0;
  sum.x += x;
  sum.y += y;
  sum.z += z;
  sum.n++;
  sum.rot = Math.max(sum.rot, rot);
  totalSamples++;
}

function startSensors() {
  if (sensorsOn) return;
  sensorsOn = true;
  window.addEventListener('devicemotion', onMotion);
  setInterval(flush, 100);
  setTimeout(() => {
    if (totalSamples === 0) {
      const w = $('warn');
      w.textContent = 'No motion data from this phone. Check motion permission, or try Safari / Chrome.';
      w.hidden = false;
    }
  }, 2500);
}

const r3 = (v: number) => Math.round(v * 1000) / 1000;

function flush() {
  if (sum.n === 0) return;
  const m: Motion = {
    type: 'm',
    t: Date.now(),
    ax: r3(sum.x / sum.n),
    ay: r3(sum.y / sum.n),
    az: r3(sum.z / sum.n),
    rot: r3(sum.rot),
  };
  sum = { x: 0, y: 0, z: 0, n: 0, rot: 0 };
  send(m);
}

// ---- WebSocket with automatic reconnect ----

let ws: WebSocket | null = null;
let backoff = 500;
let reconnectTimer = 0;
let sent = 0;

function send(msg: Motion | Pong | Hello) {
  if (ws?.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify(msg));
    if (msg.type === 'm') sent++;
  }
}

function hello(): Hello {
  return { type: 'hello', id, row: spot!.row, col: spot!.col, ua: deviceLabel() };
}

function connect() {
  clearTimeout(reconnectTimer);
  setConn('Connecting…', '');
  const sock = new WebSocket(wsURL('/ws/phone'));
  ws = sock;
  sock.onopen = () => {
    backoff = 500;
    sock.send(JSON.stringify(hello()));
    setConn('Syncing clock…', 'syncing');
  };
  sock.onmessage = (ev) => {
    const msg = JSON.parse(ev.data as string) as ToPhone;
    if (msg.type === 'ping') {
      sock.send(JSON.stringify({ type: 'pong', t0: msg.t0, t1: Date.now() } satisfies Pong));
    } else if (msg.type === 'state') {
      setConn('Connected', 'on');
      applyState(msg.node, msg.zone);
    }
  };
  sock.onclose = () => {
    if (ws !== sock) return;
    ws = null;
    setConn('Reconnecting…', '');
    applyState('connecting', '');
    reconnectTimer = window.setTimeout(connect, backoff);
    backoff = Math.min(backoff * 2, 5000);
  };
  sock.onerror = () => sock.close();
}

function setConn(text: string, cls: '' | 'on' | 'syncing') {
  $('conn').textContent = text;
  $('dot').className = `dot ${cls}`;
}

function startLive() {
  show('live');
  $('spot').textContent = spot ? `Spot ${spot.col + 1}${spot.row > 0 ? ` · row ${spot.row + 1}` : ''}` : '';
  if (ws?.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify(hello())); // moved spot
  } else if (!ws) {
    connect();
  }
}

// ---- feedback from the server ----

function applyState(node: string, zone: string) {
  const b = document.body;
  b.classList.toggle('node-handling', node === 'handling');
  b.classList.toggle('zone-yellow', zone === 'yellow');
  b.classList.toggle('zone-red', zone === 'red');
  const [icon, head, sub] =
    zone === 'red'
      ? ['⚠️', 'Crowd wave detected', 'Stay on your feet. Arms up in front of your chest. Move sideways, not against the push.']
      : node === 'handling'
        ? ['✋', 'Phone is moving around', 'Hold it flat against your chest so it can feel the crowd.']
        : zone === 'yellow'
          ? ['👀', 'Sway building nearby', 'Keep your phone flat against your chest.']
          : node === 'connecting'
            ? ['📡', 'Connecting…', 'Hang on a second.']
            : ['📱', 'Hold your phone flat against your chest', 'Screen facing out, top of the phone up. You are part of the network.'];
  $('icon').textContent = icon;
  $('headline').textContent = head;
  $('sub').textContent = sub;
}

// stats line, so testers can see it's alive
let lastSamples = 0;
setInterval(() => {
  const hz = totalSamples - lastSamples;
  lastSamples = totalSamples;
  $('stats').textContent = `${hz} samples/s · ${sent} sent · id ${id.slice(0, 6)}`;
  if (hz > 0) $('warn').hidden = true;
}, 1000);

// ---- keep the screen on ----

let wakeLock: { release(): Promise<void> } | null = null;
async function keepAwake() {
  try {
    const wl = (navigator as unknown as { wakeLock?: { request(t: 'screen'): Promise<{ release(): Promise<void> }> } }).wakeLock;
    if (wl) wakeLock = await wl.request('screen');
  } catch {
    /* not supported or denied: the phone may dim, that's all */
  }
}
document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'visible' && sensorsOn) {
    void keepAwake();
    if (!ws) connect();
  }
});
void wakeLock;

show('join');
