import './style.css';
import type { Config, FromPhone, Hello, Motion, PhoneState, Pong, ToPhone } from '../../shared/protocol';
import { wsURL } from '../../shared/protocol';
import { demoShake, demoState, initLeave } from './demo';
import type { Tower } from '../../shared/demo';

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

// Tower check-in: the join link of a tower's QR code carries ?at=<key>. The
// phone is then placed next to that tower (and its GPS, if any, calibrated there).
const atKey = new URLSearchParams(location.search).get('at');
let tower: Tower | null = null;
/** Why the check-in didn't work (unknown code, or the tower isn't on the map yet). */
let towerNote = '';

async function loadTower() {
  if (!atKey) return;
  try {
    const r = await fetch(`/api/tower/${encodeURIComponent(atKey)}`);
    const j = (await r.json()) as Tower & { error?: string };
    if (r.ok) tower = j;
    else towerNote = j.error ? `${j.error[0].toUpperCase()}${j.error.slice(1)}.` : 'That check-in code didn’t work.';
  } catch {
    towerNote = 'Couldn’t check in at that spot.';
  }
}

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
    tower && !manual
      ? `Placed at ${tower.name}${fix && fix.acc <= GPS_MAX_ACC ? ` · GPS ±${Math.round(fix.acc)} m` : ''}`
      : mode === 'gps' && fix
        ? `GPS ±${Math.round(fix.acc)} m`
        : manual
          ? 'Placed on map'
          : cfg.demo
            ? 'Placed by staff'
            : '';
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
  // iOS asks separately for the compass (orientation). Both prompts must start
  // inside this tap, before any await, or Safari refuses them.
  const orientReq = (window.DeviceOrientationEvent as unknown as { requestPermission?: PermissionFn } | undefined)?.requestPermission;
  const orientAsked = typeof orientReq === 'function' ? orientReq.call(DeviceOrientationEvent).catch(() => 'denied' as const) : null;
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
  void (orientAsked ?? Promise.resolve('granted')).then((r) => r === 'granted' && startCompass());
  await loadConfig();
  await loadTower();
  if (tower) {
    // Checked in at a tower: placed next to it. With a GPS venue the fixes
    // follow in the background, corrected by the check-in.
    manual = null;
    mode = 'gps';
    startLive();
    $('moveBtn').textContent = 'I moved: place me on the map';
    if (cfg.geo) void startGps();
  } else if (towerNote && !manual && !cfg.demo && !cfg.geo) {
    showPlace(`${towerNote} For now, tap where you’re standing.`);
  } else if (mode === 'manual' && manual) startLive();
  else if (cfg.demo) {
    // Demo spot: the server lines phones up, no GPS and no tap.
    startLive();
    $('moveBtn').textContent = 'Place me on the map myself';
  }
  else await locate();
});

// ---- motion: summarise every 100 ms ----

let sum = { x: 0, y: 0, z: 0, n: 0, rot: 0, gx: 0, gy: 0, gz: 0, gn: 0 };
let gravity: { x: number; y: number; z: number } | null = null;
let totalSamples = 0;
let sensorsOn = false;

// Which way is down. The server levels every reading with it, so the phone
// can sit in a pocket, a bag or a hand at any tilt. What the sensors give is
// the reaction to gravity, (acceleration including gravity) − (acceleration):
// it points up on Android (the W3C convention: +9.8 on the axis facing the
// sky) and down on iPhones, which report every axis with the opposite sign.
const DOWN_SIGN = isIOS ? 1 : -1;
const G_RESEND_MS = 1000; // repeat g at least this often (a recording or a reconnect may have missed it)
const G_RESEND_COS = Math.cos((3 * Math.PI) / 180); // … and whenever it has turned more than 3°
let sentG: { v: [number, number, number]; t: number } | null = null;

function onMotion(e: DeviceMotionEvent) {
  let x: number, y: number, z: number;
  const a = e.acceleration;
  const g = e.accelerationIncludingGravity;
  const hasG = !!g && g.x != null && g.y != null && g.z != null;
  if (a && a.x != null && a.y != null && a.z != null) {
    x = a.x;
    y = a.y;
    z = a.z;
    if (hasG) {
      // Sensor fusion (gyro) already split gravity off: use its split.
      sum.gx += g!.x! - x;
      sum.gy += g!.y! - y;
      sum.gz += g!.z! - z;
      sum.gn++;
    }
  } else {
    // No gyro-fused acceleration: subtract a running mean (≈ gravity).
    if (!hasG) return;
    const gi = { x: g!.x!, y: g!.y!, z: g!.z! };
    const dt = (e.interval > 1 ? e.interval : e.interval * 1000) || 16; // ms (some browsers report seconds)
    const k = Math.min(1, dt / 1000); // ~1 s time constant
    if (!gravity) gravity = gi;
    gravity.x += k * (gi.x - gravity.x);
    gravity.y += k * (gi.y - gravity.y);
    gravity.z += k * (gi.z - gravity.z);
    x = gi.x - gravity.x;
    y = gi.y - gravity.y;
    z = gi.z - gravity.z;
    sum.gx += gravity.x;
    sum.gy += gravity.y;
    sum.gz += gravity.z;
    sum.gn++;
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
  const g = downVector();
  // The server keeps a phone's last g, so it only goes out when it changed
  // (and once a second, and first thing on every connection).
  if (g && ws?.readyState === WebSocket.OPEN) {
    const dot = sentG ? g[0] * sentG.v[0] + g[1] * sentG.v[1] + g[2] * sentG.v[2] : -1;
    if (!sentG || dot < G_RESEND_COS || m.t - sentG.t >= G_RESEND_MS) {
      m.g = g;
      sentG = { v: g, t: m.t };
    }
  }
  sum = { x: 0, y: 0, z: 0, n: 0, rot: 0, gx: 0, gy: 0, gz: 0, gn: 0 };
  send(m);
}

/** Unit vector pointing down in the phone's own axes over the last 100 ms (2 decimals), or null if the sensors gave no gravity. */
function downVector(): [number, number, number] | null {
  if (sum.gn === 0) return null;
  const n = Math.hypot(sum.gx, sum.gy, sum.gz) / sum.gn;
  // Gravity is 9.8 m/s²; anything far from that is a broken sensor or free fall.
  if (!(n > 4 && n < 16)) return null;
  const k = DOWN_SIGN / (n * sum.gn);
  const r = (v: number) => Math.round(v * k * 100) / 100 + 0; // + 0: no "-0"
  return [r(sum.gx), r(sum.gy), r(sum.gz)];
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
  if (tower && !manual) h.at = tower.key; // until the person places themselves by hand
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
    sentG = null; // a new connection is a new phone to the server: send g again
    setConn('Syncing clock…', 'syncing');
  };
  sock.onmessage = (ev) => {
    const msg = JSON.parse(ev.data as string) as ToPhone;
    if (msg.type === 'ping') {
      sock.send(JSON.stringify({ type: 'pong', t0: msg.t0, t1: Date.now() } satisfies Pong));
    } else if (msg.type === 'state') {
      setConn('Connected', 'on');
      applyState(msg.node, msg.zone);
      applyGuidance(msg);
      demoState(msg);
    } else if (msg.type === 'shake') {
      demoShake();
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
        ? ['✋', 'Phone is moving around', 'Let it rest in your pocket or your hand so it can feel the crowd.']
        : zone === 'yellow'
          ? ['👀', 'Pressure building nearby', 'Keep your phone where it is, with this page open.']
          : node === 'connecting'
            ? ['📡', 'Connecting…', 'Hang on a second.']
            : ['📱', 'You are part of the network', 'Keep this page open. Your pocket or your hand is fine, any way up.'];
  $('icon').textContent = icon;
  $('headline').textContent = head;
  $('sub').textContent = sub;
}

// ---- "Move this way": personal guidance from the server ----

let guidance: PhoneState | null = null;
let heading: number | null = null; // degrees clockwise from north, if the phone has a compass
let lastBuzz = 0;

function startCompass() {
  // iOS gives a compass heading directly; Android gives absolute orientation.
  window.addEventListener('deviceorientation', (e) => {
    const h = (e as DeviceOrientationEvent & { webkitCompassHeading?: number }).webkitCompassHeading;
    if (typeof h === 'number' && !Number.isNaN(h)) {
      heading = h;
      drawGuidance();
    }
  });
  window.addEventListener('deviceorientationabsolute', (e) => {
    const a = (e as DeviceOrientationEvent).alpha;
    if (typeof a === 'number') {
      heading = (360 - a) % 360;
      drawGuidance();
    }
  });
}

function applyGuidance(s: PhoneState) {
  const had = !!guidance?.move;
  guidance = s;
  const g = $('guide');
  g.hidden = !s.move;
  if (!s.move) return;
  // Buzz on Android when guidance starts (iPhones can't vibrate from a web page).
  const now = Date.now();
  if (!had || now - lastBuzz > 15_000) {
    lastBuzz = now;
    navigator.vibrate?.([300, 120, 300, 120, 600]);
  }
  drawGuidance();
}

/** The arrow points the real way when compass + venue bearing are known; otherwise relative to the map (stage at the top). */
function drawGuidance() {
  const s = guidance;
  if (!s?.move) return;
  const mapAngle = (Math.atan2(s.move.dx, -s.move.dy) * 180) / Math.PI; // 0 = up the map, clockwise
  const real = heading !== null && s.bearing !== undefined;
  const screenAngle = real ? s.bearing! + mapAngle - heading! : mapAngle;
  $('arrow').style.transform = `rotate(${screenAngle}deg)`;
  $('guideTo').textContent = s.move.to ? `Toward ${s.move.to}` : s.move.reason === 'push' ? 'Out of the push, to the side' : 'Toward more space';
  $('guideNote').textContent = real
    ? 'The arrow points the real way. Turn until it points forward.'
    : 'The arrow is relative to the venue map below (stage at the top).';
  drawMiniMap(s);
}

function drawMiniMap(s: PhoneState) {
  const c = $('miniMap') as HTMLCanvasElement;
  const g = c.getContext('2d')!;
  const W = c.width, H = c.height;
  g.clearRect(0, 0, W, H);
  const vw = s.w ?? cfg.venueW, vh = s.h ?? cfg.venueH;
  const k = Math.min((W - 16) / vw, (H - 16) / vh);
  const ox = (W - vw * k) / 2, oy = (H - vh * k) / 2;
  g.strokeStyle = 'rgba(255,255,255,0.7)';
  g.lineWidth = 2;
  g.strokeRect(ox, oy, vw * k, vh * k);
  g.fillStyle = 'rgba(255,255,255,0.25)';
  g.fillRect(ox + vw * k * 0.3, oy, vw * k * 0.4, 6); // stage edge
  if (s.x === undefined || s.y === undefined || !s.move) return;
  const px = ox + s.x * k, py = oy + s.y * k;
  const len = Math.min(W, H) * 0.3;
  const ex = px + s.move.dx * len, ey = py + s.move.dy * len;
  g.strokeStyle = '#fff';
  g.lineWidth = 4;
  g.beginPath();
  g.moveTo(px, py);
  g.lineTo(ex, ey);
  g.stroke();
  const a = Math.atan2(ey - py, ex - px);
  g.fillStyle = '#fff';
  g.beginPath();
  g.moveTo(ex, ey);
  g.lineTo(ex - 12 * Math.cos(a - 0.5), ey - 12 * Math.sin(a - 0.5));
  g.lineTo(ex - 12 * Math.cos(a + 0.5), ey - 12 * Math.sin(a + 0.5));
  g.closePath();
  g.fill();
  g.beginPath();
  g.arc(px, py, 7, 0, Math.PI * 2);
  g.fill();
  g.strokeStyle = '#b91c1c';
  g.lineWidth = 3;
  g.stroke();
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
  if (document.visibilityState === 'visible' && sensorsOn && !left) {
    void keepAwake();
    if (!ws) connect();
  }
});
void wakeLock;

// ---- leave: close the connection for good, then show the privacy receipt (demo.ts) ----

let left = false;
initLeave({
  id,
  device: deviceLabel(),
  sent: () => sent,
  leave: () => {
    left = true;
    clearTimeout(reconnectTimer);
    const sock = ws;
    ws = null; // onclose then doesn't reconnect
    sock?.close();
    window.removeEventListener('devicemotion', onMotion);
    stopGps();
    void wakeLock?.release().catch(() => {});
  },
});

show('join');
