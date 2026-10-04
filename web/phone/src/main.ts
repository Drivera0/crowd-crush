import './style.css';
import type { Config, FromPhone, Hello, Motion, Pong, ToPhone } from '../../shared/protocol';
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

function deviceLabel(): string {
  const ua = navigator.userAgent;
  if (/iPhone/.test(ua)) return 'iPhone';
  if (/iPad/.test(ua)) return 'iPad';
  if (/Android/.test(ua)) return 'Android';
  return 'browser';
}

// iPhone and Android hide the same switches in different places, so every
// "how do I fix this" message is per platform. iPadOS reports itself as a Mac.
const isIOS = /iPhone|iPad|iPod/.test(navigator.userAgent) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1);
const isSamsung = /SamsungBrowser/.test(navigator.userAgent);

const help = {
  motionDenied: isIOS
    ? 'Motion access was denied. Tap aA in the address bar → Website Settings → Motion & Orientation Access → Allow, then reload.'
    : 'Motion sensors are blocked for this site. Tap the icon left of the address → Permissions → Motion sensors → Allow, then reload.',
  noMotion: isIOS
    ? 'No motion data. Tap aA → Website Settings → allow Motion & Orientation Access, then reload. Low Power Mode can also pause sensors.'
    : isSamsung
      ? 'No motion data. In Samsung Internet: ⋮ → Settings → Sites and downloads → Site permissions → Motion sensors → Allow, then reload.'
      : 'No motion data. Tap the icon left of the address → Permissions → Motion sensors → Allow, then reload.',
  locationDenied: isIOS
    ? 'Location is off for this site. Settings → Privacy & Security → Location Services → Safari Websites → While Using, then try again.'
    : 'Location is blocked for this site. Tap the icon left of the address → Permissions → Location → Allow, then try again.',
  imprecise: isIOS
    ? 'Your phone is only sharing an approximate location. Settings → Privacy & Security → Location Services → Safari Websites → turn on Precise Location.'
    : 'Your phone is only sharing an approximate location. When the browser asks, choose “Precise”, or turn on Settings → Location → Use precise location.',
  screen: isIOS
    ? 'Keep this page open with the screen on. iPhones pause web pages when locked.'
    : 'Keep this page open with the screen on.',
};

type Screen = 'join' | 'locate' | 'place' | 'live';
function show(screen: Screen) {
  for (const s of ['join', 'locate', 'place', 'live']) $(s).hidden = s !== screen;
}

// ---- where the phone is ----
// GPS when the venue has a geo-anchor and the fix is good enough; otherwise
// the attendee taps their spot on the venue map (metres).

const GPS_MAX_ACC = 25; // m: worse fixes can't tell one part of the venue from another
const GPS_TIMEOUT_MS = 12_000;

let cfg: Config = { venueW: 24, venueH: 16, geo: false, yellow: 0.3, red: 0.6, neighbourRadius: 1 };
let manual: { x: number; y: number } | null = (() => {
  const s = load('pulse-spot-m');
  return s ? (JSON.parse(s) as { x: number; y: number }) : null;
})();
let fix: { lat: number; lon: number; acc: number } | null = null;
let mode: 'gps' | 'manual' = manual ? 'manual' : 'gps';
let watchId: number | null = null;
let lastSentFix: { lat: number; lon: number; t: number } | null = null;

async function loadConfig() {
  try {
    const r = await fetch('/api/config');
    if (r.ok) cfg = { ...cfg, ...((await r.json()) as Partial<Config>) };
  } catch {
    /* defaults */
  }
}

/** Metres between two fixes (equirectangular; fine at venue scale). */
function metres(a: { lat: number; lon: number }, b: { lat: number; lon: number }) {
  const k = Math.PI / 180;
  const x = (b.lon - a.lon) * k * Math.cos(((a.lat + b.lat) / 2) * k);
  const y = (b.lat - a.lat) * k;
  return Math.hypot(x, y) * 6_371_000;
}

function startGps(): Promise<boolean> {
  return new Promise((resolve) => {
    if (!cfg.geo) return resolve(false);
    if (!('geolocation' in navigator)) return resolve(false);
    let settled = false;
    const done = (ok: boolean) => {
      if (!settled) {
        settled = true;
        resolve(ok);
      }
    };
    const timer = setTimeout(() => {
      locateNote = !fix
        ? 'No GPS signal yet.'
        : fix.acc > 500
          ? help.imprecise
          : `GPS is only accurate to ±${Math.round(fix.acc)} m here, too rough to place you.`;
      done(false);
    }, GPS_TIMEOUT_MS);
    stopGps();
    watchId = navigator.geolocation.watchPosition(
      (p) => {
        fix = { lat: p.coords.latitude, lon: p.coords.longitude, acc: p.coords.accuracy };
        $('locateMsg').textContent = `Got a fix, ±${Math.round(fix.acc)} m…`;
        if (fix.acc <= GPS_MAX_ACC) {
          clearTimeout(timer);
          done(true);
        }
        sendFix();
        updateWhere();
      },
      (err) => {
        clearTimeout(timer);
        locateNote = err.code === err.PERMISSION_DENIED ? help.locationDenied : 'Couldn’t get a GPS fix.';
        done(false);
      },
      { enableHighAccuracy: true, maximumAge: 1000, timeout: GPS_TIMEOUT_MS },
    );
  });
}

function stopGps() {
  if (watchId !== null) navigator.geolocation.clearWatch(watchId);
  watchId = null;
}

/** Send a fix at most ~1/s, or straight away after moving more than a metre. */
function sendFix() {
  if (mode !== 'gps' || !fix || fix.acc > GPS_MAX_ACC) return;
  const now = Date.now();
  if (lastSentFix && now - lastSentFix.t < 1000 && metres(lastSentFix, fix) < 1) return;
  lastSentFix = { lat: fix.lat, lon: fix.lon, t: now };
  send({ type: 'gps', lat: fix.lat, lon: fix.lon, acc: Math.round(fix.acc * 10) / 10 });
}

/** Why GPS wasn't used, shown above the map so the attendee can fix it or just tap. */
let locateNote = '';

async function locate() {
  show('locate');
  locateNote = '';
  const ok = await startGps();
  if (ok) {
    mode = 'gps';
    startLive();
  } else {
    stopGps();
    showPlace(cfg.geo ? `${locateNote || 'GPS can’t place you precisely here.'} For now, tap where you’re standing.` : undefined);
  }
}

$('skipGps').addEventListener('click', () => {
  stopGps();
  showPlace();
});
$('tryGps').addEventListener('click', () => void locate());
$('moveBtn').addEventListener('click', () => showPlace());

// ---- the venue map: tap or drag your dot ----

let draft: { x: number; y: number } | null = null;

function showPlace(hint?: string) {
  const v = $('venue');
  v.style.aspectRatio = `${cfg.venueW} / ${cfg.venueH}`;
  $('placeHint').textContent = hint ?? 'Tap where you’re standing. Drag to adjust.';
  $('tryGps').hidden = !cfg.geo;
  draft = manual;
  drawMe();
  show('place');
}

function drawMe() {
  const me = $('me');
  ($('placeDone') as HTMLButtonElement).disabled = !draft;
  if (!draft) {
    me.hidden = true;
    return;
  }
  me.hidden = false;
  me.style.left = `${(draft.x / cfg.venueW) * 100}%`;
  me.style.top = `${(draft.y / cfg.venueH) * 100}%`;
}

function pointAt(e: PointerEvent) {
  const r = $('venue').getBoundingClientRect();
  const fx = Math.min(1, Math.max(0, (e.clientX - r.left) / r.width));
  const fy = Math.min(1, Math.max(0, (e.clientY - r.top) / r.height));
  return { x: Math.round(fx * cfg.venueW * 10) / 10, y: Math.round(fy * cfg.venueH * 10) / 10 };
}

let dragging = false;
$('venue').addEventListener('pointerdown', (e) => {
  dragging = true;
  $('venue').setPointerCapture(e.pointerId);
  draft = pointAt(e);
  drawMe();
});
$('venue').addEventListener('pointermove', (e) => {
  if (!dragging) return;
  draft = pointAt(e);
  drawMe();
});
$('venue').addEventListener('pointerup', () => (dragging = false));

$('placeDone').addEventListener('click', () => {
  if (!draft) return;
  manual = draft;
  save('pulse-spot-m', JSON.stringify(manual));
  mode = 'manual';
  stopGps();
  startLive();
  send({ type: 'pos', x: manual.x, y: manual.y });
});

function updateWhere() {
  $('where').textContent =
    mode === 'gps' && fix ? `GPS ±${Math.round(fix.acc)} m` : manual ? 'Placed on map' : '';
}

// ---- 1. Join: motion permission must be asked inside the tap handler ----

type PermissionFn = () => Promise<'granted' | 'denied'>;

$('joinBtn').addEventListener('click', async () => {
  const err = $('joinErr');
  err.hidden = true;
  // Both platforms only expose motion and GPS to HTTPS pages.
  if (!window.isSecureContext) {
    err.textContent = 'This page has to be opened over https:// (scan the QR code on the screen) for the sensors to work.';
    err.hidden = false;
    return;
  }
  const req = (DeviceMotionEvent as unknown as { requestPermission?: PermissionFn }).requestPermission;
  if (typeof req === 'function') {
    try {
      const r = await req.call(DeviceMotionEvent);
      if (r !== 'granted') {
        err.textContent = help.motionDenied;
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
  await loadConfig();
  if (mode === 'manual' && manual) startLive();
  else await locate();
});

// ---- motion: summarise every 100 ms ----

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
      w.textContent = help.noMotion;
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

function send(msg: FromPhone) {
  if (ws?.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify(msg));
    if (msg.type === 'm') sent++;
  }
}

function hello(): Hello {
  const h: Hello = { type: 'hello', id, ua: deviceLabel() };
  if (mode === 'gps' && fix) Object.assign(h, { lat: fix.lat, lon: fix.lon, acc: Math.round(fix.acc * 10) / 10 });
  else if (manual) Object.assign(h, { x: manual.x, y: manual.y });
  return h;
}

function connect() {
  clearTimeout(reconnectTimer);
  setConn('Connecting…', '');
  const sock = new WebSocket(wsURL('/ws/phone'));
  ws = sock;
  sock.onopen = () => {
    backoff = 500;
    sock.send(JSON.stringify(hello()));
    lastSentFix = null;
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
  updateWhere();
  $('moveBtn').textContent = mode === 'gps' ? 'GPS is off? Place me on the map' : 'I moved: place me again';
  if (!ws) connect();
  else if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(hello()));
}

// ---- feedback from the server ----

function applyState(node: string, zone: string) {
  const b = document.body;
  b.classList.toggle('node-handling', node === 'handling');
  b.classList.toggle('zone-yellow', zone === 'yellow');
  b.classList.toggle('zone-red', zone === 'red');
  const [icon, head, sub] =
    zone === 'red'
      ? ['⚠️', 'Crowd danger near you', 'Stay on your feet. Arms up in front of your chest. Move sideways, not against the push.']
      : node === 'handling'
        ? ['✋', 'Phone is moving around', 'Hold it flat against your chest so it can feel the crowd.']
        : zone === 'yellow'
          ? ['👀', 'Pressure building nearby', 'Keep your phone flat against your chest.']
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
    /* denied (e.g. Low Power Mode): fall through to the tip */
  }
  // Without a wake lock (older iPhones, Low Power Mode) the screen can lock and
  // the page stops streaming, so say so.
  $('screenTip').hidden = wakeLock !== null;
  $('screenTip').textContent = help.screen;
}
document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'visible' && sensorsOn) {
    void keepAwake();
    if (!ws) connect();
  }
});
void wakeLock;

show('join');
