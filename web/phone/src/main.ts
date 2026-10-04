import './style.css';
import type { Config, FromPhone, Hello, Motion, PhoneState, Pong, ToPhone } from '../../shared/protocol';
import { wsURL } from '../../shared/protocol';
import { demoShake, demoState, initLeave } from './demo';
import type { Tower } from '../../shared/demo';
import type { Clock, Jam, MeshPeers, PosSrc, Relay, Signal } from '../../shared/mesh';
import { Mesh, type SelfPos } from './mesh';
import { initPocket, pocketExit, pocketReady, pocketShake, pocketUpdate } from './pocket';
import { initBeacons } from './beacons';
import { initNative } from './native';
import { browserLabel, chromeIntent, embedded, help as envHelp, isIOS, isMobile, reportJoin, type JoinReason } from './env';
import { drawVenueMap } from './placemap';
import { backToRow, demoViewBox, loadDemoView, renderDemoMove, type ViewBox } from './demomove';

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
// "how do I fix this" message is per platform and per browser (env.ts).
// iPadOS reports itself as a Mac.

const help = {
  locationDenied: isIOS
    ? 'Location is off for this site. Settings → Privacy & Security → Location Services → Safari Websites → While Using, then try again.'
    : 'Location is blocked for this site. Tap the icon left of the address → Permissions → Location → Allow, then try again.',
  imprecise: isIOS
    ? 'Your phone is only sharing an approximate location. Settings → Privacy & Security → Location Services → Safari Websites → turn on Precise Location.'
    : 'Your phone is only sharing an approximate location. When the browser asks, choose “Precise”, or turn on Settings → Location → Use precise location.',
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
$('moveBtn').addEventListener('click', () => void (atDemo() ? showDemoPlace() : showPlace()));
$('iMoved').addEventListener('click', () => void showDemoPlace());
$('backRow').addEventListener('click', async () => {
  const b = $('backRow') as HTMLButtonElement;
  b.disabled = true;
  try {
    const res = await backToRow(id);
    // The server lines it up again: no spot of its own from now on (a reconnect says hello without one).
    manual = null;
    save('pulse-spot-m', '');
    placedAt = Date.now();
    updateWhere();
    b.textContent = `Back on #${res.n}`;
  } catch (e) {
    b.textContent = `Couldn’t go back: ${(e as Error).message}`;
  } finally {
    b.disabled = false;
  }
});

/** The demo spot is on (as the server said at Join, or as this phone's state shows). */
function atDemo(): boolean {
  return !!cfg.demo || !!lastState?.row || !!lastState?.spot;
}

// ---- the venue map: tap or drag your dot ----

let draft: { x: number; y: number } | null = null;
/** The part of the venue the map shows: all of it, or zoomed to the demo row (demomove.ts). */
let view: ViewBox | null = null;
const fullView = (): ViewBox => ({ x: 0, y: 0, w: cfg.venueW, h: cfg.venueH });

function showPlace(hint?: string, zoom?: ViewBox, row?: import('../../shared/demomove').DemoView | null) {
  const v = $('venue');
  view = zoom ?? null;
  const vb = view ?? fullView();
  v.style.aspectRatio = `${vb.w} / ${vb.h}`;
  $('placeHint').textContent = hint ?? 'Tap where you’re standing. Drag to adjust.';
  $('tryGps').hidden = !cfg.geo || !!zoom;
  // Areas, stage, exits and walls to orient by; at the demo spot also the row, the boards and the other phones (placemap.ts).
  void drawVenueMap(cfg.venueW, cfg.venueH, zoom, row, lastState?.name);
  draft = manual ?? (zoom && lastState?.x !== undefined && lastState.y !== undefined ? { x: lastState.x, y: lastState.y } : null);
  drawMe();
  show('place');
}

/** "I moved" at the demo spot: the map zoomed to the row so a tap at table scale lands next to the right person. */
async function showDemoPlace() {
  const row = await loadDemoView();
  if (!row) return showPlace();
  const me = lastState?.x !== undefined && lastState.y !== undefined ? { x: lastState.x, y: lastState.y } : undefined;
  showPlace('Tap where you’re standing now. Dots are the others in the row (#numbers), squares are the boards.', demoViewBox(row, cfg.venueW, cfg.venueH, me), row);
}

function drawMe() {
  const me = $('me');
  ($('placeDone') as HTMLButtonElement).disabled = !draft;
  if (!draft) {
    me.hidden = true;
    return;
  }
  const vb = view ?? fullView();
  me.hidden = false;
  me.style.left = `${((draft.x - vb.x) / vb.w) * 100}%`;
  me.style.top = `${((draft.y - vb.y) / vb.h) * 100}%`;
}

function pointAt(e: PointerEvent) {
  const r = $('venue').getBoundingClientRect();
  const vb = view ?? fullView();
  const fx = Math.min(1, Math.max(0, (e.clientX - r.left) / r.width));
  const fy = Math.min(1, Math.max(0, (e.clientY - r.top) / r.height));
  // Zoomed in, a tap is worth centimetres: keep them.
  const k = view ? 100 : 10;
  return { x: Math.round((vb.x + fx * vb.w) * k) / k, y: Math.round((vb.y + fy * vb.h) * k) / k };
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
  placedAt = Date.now();
  save('pulse-spot-m', JSON.stringify(manual));
  mode = 'manual';
  stopGps();
  startLive();
  send({ type: 'pos', x: manual.x, y: manual.y });
});

function updateWhere() {
  if (lastState?.near) {
    $('where').textContent = `Near ${lastState.near}`; // walked up to a board (Bluetooth)
    return;
  }
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

/** What the ?debug=1 panel shows (debug.ts). */
const dbg = {
  motionPerm: 'not asked' as string,
  orientPerm: 'not asked' as string,
  wakeLock: 'not asked' as string,
  wsOpens: 0,
  wsFails: 0,
  lastClose: '',
  lastError: '',
  fields: { acc: false, accG: false, rot: false, interval: 0 },
};

/** A failure on the Join screen (or the live screen): the fix in words, the server told why. */
function fail(where: 'join' | 'live', reason: JoinReason, text: string) {
  dbg.lastError = `${reason}: ${text}`;
  const el = $(where === 'join' ? 'joinErr' : 'warn');
  el.textContent = text;
  el.hidden = false;
  if (where === 'live') showFixButtons('warnFix', 'warnOpen', reason === 'no-motion' || reason === 'socket' ? embedded : true);
  reportJoin(id, reason);
}

/** Copy-link and (Android) open-in-Chrome buttons under a message. */
function showFixButtons(box: string, open: string, on: boolean) {
  $(box).hidden = !on;
  const intent = chromeIntent(location.href);
  const a = $(open) as HTMLAnchorElement;
  a.hidden = !intent;
  if (intent) a.href = intent;
}

async function copyLink(btn: HTMLElement) {
  const link = `${location.origin}${location.pathname}${location.search}`;
  try {
    await navigator.clipboard.writeText(link);
    btn.textContent = 'Copied: paste it in your browser';
  } catch {
    // Older browsers and some in-app ones: show the link to copy by hand.
    btn.textContent = link;
  }
}
$('envCopy').addEventListener('click', () => void copyLink($('envCopy')));
$('warnCopy').addEventListener('click', () => void copyLink($('warnCopy')));

/** Before Join: say straight away when this browser can't work (http, an in-app browser). */
function checkEnv() {
  let text = '';
  if (!window.isSecureContext) {
    text = envHelp.insecure();
    reportJoin(id, 'insecure');
    ($('joinBtn') as HTMLButtonElement).disabled = true;
    $('envCopy').hidden = true;
  } else if (embedded) {
    // Try anyway (some in-app browsers do pass motion on); this is the way out if not.
    text = `${browserLabel().split(' (')[0]}’s built-in browser may block the motion sensors. If Join doesn’t work, open this page in ${isIOS ? 'Safari' : 'Chrome'}.`;
  }
  $('envNote').hidden = !text;
  $('envText').textContent = text;
  if (text) showFixButtons('envNote', 'envOpen', true);
}

$('joinBtn').addEventListener('click', async () => {
  $('joinErr').hidden = true;
  // Both platforms only expose motion (and GPS) to HTTPS pages.
  if (!window.isSecureContext) return fail('join', 'insecure', envHelp.insecure());
  const req = (window.DeviceMotionEvent as unknown as { requestPermission?: PermissionFn } | undefined)?.requestPermission;
  // iOS 13+ asks separately for the compass (orientation). Both prompts must
  // start inside this tap, before any await, or Safari refuses them.
  const orientReq = (window.DeviceOrientationEvent as unknown as { requestPermission?: PermissionFn } | undefined)?.requestPermission;
  let orientAsked: Promise<string> | null = null;
  try {
    orientAsked = typeof orientReq === 'function' ? orientReq.call(DeviceOrientationEvent).catch(() => 'denied') : null;
  } catch {
    orientAsked = Promise.resolve('denied');
  }
  if (!('DeviceMotionEvent' in window)) {
    dbg.motionPerm = 'no API';
    return fail('join', 'no-sensor', envHelp.noSensorApi());
  }
  if (typeof req === 'function') {
    dbg.motionPerm = 'asking';
    try {
      const r = await req.call(DeviceMotionEvent);
      dbg.motionPerm = r;
      if (r !== 'granted') return fail('join', 'motion-denied', envHelp.motionDenied());
    } catch (e) {
      dbg.motionPerm = `error: ${e}`;
      return fail('join', 'perm-error', envHelp.permError(e));
    }
  } else {
    dbg.motionPerm = 'no prompt needed';
  }
  if (!isMobile) {
    // A laptop has the API but no sensor: say so now rather than after a silent wait.
    $('joinErr').textContent = envHelp.noMotion();
    $('joinErr').hidden = false;
  }
  save('pulse-joined', '1'); // a reload (the phone slept, the tab was discarded) carries on by itself where it can
  void keepAwake();
  startSensors();
  void (orientAsked ?? Promise.resolve('granted')).then((r) => {
    dbg.orientPerm = orientAsked ? r : 'no prompt needed';
    if (r === 'granted') startCompass();
  });
  await loadConfig();
  await loadTower();
  if (tower) {
    // Checked in at a tower: placed next to it. With a GPS venue the fixes
    // follow in the background, corrected by the check-in.
    manual = null;
    mode = 'gps';
    placedAt = Date.now();
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
let sentHd: { v: number; t: number } | null = null;

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
  if (totalSamples % 64 === 0) {
    // Which fields this phone's sensors fill in (no gyroscope = no acceleration/rotationRate), for ?debug=1.
    dbg.fields = { acc: !!a && a.x != null, accG: hasG, rot: !!r && r.alpha != null, interval: e.interval };
  }
  if (totalSamples === 0) {
    reportJoin(id, 'ok'); // clears a "no motion" this phone reported earlier
    $('warnFix').hidden = true;
  }
  totalSamples++;
}

function startSensors() {
  if (sensorsOn) return;
  sensorsOn = true;
  window.addEventListener('devicemotion', onMotion);
  setInterval(flush, 100);
  // Some phones take a moment to start the sensors (and an in-app browser may never): wait, then say why.
  setTimeout(() => {
    if (totalSamples === 0 && !left) fail('live', 'no-motion', envHelp.noMotion());
  }, 3000);
}

const r3 = (v: number) => Math.round(v * 1000) / 1000;

function flush() {
  if (sum.n === 0) return;
  const m: Motion & { hd?: number } = {
    type: 'm',
    t: Date.now(),
    ax: r3(sum.x / sum.n),
    ay: r3(sum.y / sum.n),
    az: r3(sum.z / sum.n),
    rot: r3(sum.rot),
  };
  const g = downVector();
  // This phone's own sway trace, for its mesh neighbours (mesh.ts).
  mesh.addMotion(m.t, [m.ax, m.ay, m.az], g, m.rot);
  // Compass heading of the phone's top edge, when it has one: only when it
  // changed by 5° or more, or once a second.
  if (heading !== null && canSend()) {
    const hd = ((Math.round(heading) % 360) + 360) % 360;
    if (!sentHd || Math.abs(((hd - sentHd.v + 540) % 360) - 180) >= 5 || m.t - sentHd.t >= 1000) {
      m.hd = hd;
      sentHd = { v: hd, t: m.t };
    }
  }
  // The server keeps a phone's last g, so it only goes out when it changed
  // (and once a second, and first thing on every connection).
  if (g && canSend()) {
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
let wsFailsInARow = 0;
let backoff = 500;
let reconnectTimer = 0;
let sent = 0;

/** The WebSocket is open and in use (not dropped for the lost-signal demo). */
function wsUsable(): boolean {
  return ws?.readyState === WebSocket.OPEN && !mesh.jammed;
}

/** There is some way to the server: the WebSocket, or a mesh neighbour. */
function canSend(): boolean {
  return wsUsable() || mesh.status().links > 0;
}

/**
 * Send to the server: on the WebSocket when it works, else through a mesh
 * neighbour (mesh.ts). Raw GPS never goes through another phone.
 */
function send(msg: FromPhone | { type: 'near' | 'mpos' | 'sig' | 'rtc' }) {
  if (wsUsable()) {
    ws!.send(JSON.stringify(msg));
    if (msg.type === 'm') sent++;
  } else if (msg.type !== 'gps' && mesh.relayUp(msg)) {
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
  if (mesh.jammed || left) return; // off the WebSocket on purpose
  setConn('Connecting…', '');
  let sock: WebSocket;
  try {
    sock = new WebSocket(wsURL('/ws/phone'));
  } catch (e) {
    dbg.lastError = `WebSocket: ${e}`;
    reconnectTimer = window.setTimeout(connect, backoff);
    return;
  }
  ws = sock;
  // A tunnel hiccup can leave a socket "connecting" for a long time: give up on it and retry.
  const openTimer = window.setTimeout(() => {
    if (sock.readyState === WebSocket.CONNECTING) sock.close();
  }, 8000);
  sock.onopen = () => {
    clearTimeout(openTimer);
    dbg.wsOpens++;
    wsFailsInARow = 0;
    if (!$('warn').hidden && $('warn').textContent === envHelp.socket()) $('warn').hidden = true;
    backoff = 500;
    sock.send(JSON.stringify(hello()));
    lastSentFix = null;
    sentG = null; // a new connection is a new phone to the server: send g again
    lastWsRx = Date.now();
    gotState = false;
    renderLive();
    mesh.announce();
  };
  sock.onmessage = (ev) => {
    const msg = JSON.parse(ev.data as string) as ServerMsg;
    lastWsRx = Date.now();
    if (msg.type === 'ping') {
      sock.send(JSON.stringify({ type: 'pong', t0: msg.t0, t1: Date.now() } satisfies Pong));
    } else {
      if (msg.type === 'state') gotState = true;
      onServer(msg);
    }
  };
  sock.onclose = (ev) => {
    clearTimeout(openTimer);
    dbg.lastClose = `${ev.code}${ev.reason ? ` ${ev.reason}` : ''} at ${new Date().toLocaleTimeString()}`;
    if (ws !== sock) return;
    ws = null;
    if (dbg.wsOpens === 0 || ev.code === 1006) {
      dbg.wsFails++;
      // Never connected, many times over: the page loads but the socket can't get through.
      if (++wsFailsInARow >= 4 && dbg.wsOpens === 0) fail('live', 'socket', envHelp.socket());
    }
    renderLive();
    reconnectTimer = window.setTimeout(connect, backoff);
    backoff = Math.min(backoff * 2, 5000);
  };
  sock.onerror = () => sock.close();
}

type ServerMsg = ToPhone | MeshPeers | Signal | Jam | Clock | Relay;

/** A server → phone message, whether it came on the WebSocket or through a mesh neighbour. */
function onServer(msg: ServerMsg) {
  if (msg.type === 'state') {
    // Staff (or the demo spot) moved this phone: that is an exact placement.
    if (lastState?.x !== undefined && msg.x !== undefined && mode !== 'gps' && Math.hypot(msg.x - lastState.x, (msg.y ?? 0) - (lastState.y ?? 0)) > 0.3) placedAt = Date.now();
    lastState = msg;
    lastStateAt = Date.now();
    renderLive();
    applyGuidance(msg);
    demoState(msg);
    renderRow(msg);
    renderDemoMove(msg, !!cfg.demo);
    updateWhere();
  } else if (msg.type === 'shake') {
    demoShake();
    pocketShake();
  } else if (msg.type === 'clock') {
    if (Number.isFinite(msg.offset)) clockOffset = msg.offset;
  } else if (msg.type !== 'ping') {
    mesh.onServer(msg);
  }
}

/**
 * The demo spot lines phones up in join order (state.row): tell the person
 * their number and where to stand, so the row at the table matches the row
 * on the big screen (which runs left to right, #1 on the left).
 */
function renderRow(s: PhoneState) {
  const r = s.row;
  $('rowCard').hidden = !r;
  if (!r) return;
  $('rowN').textContent = `#${r.n}`;
  $('rowWhere').textContent =
    r.n === 1
      ? 'Stand at the left end of the row, as you face the big screen.'
      : r.newRow
        ? 'The first row is full: start a second row at its left end. Your dot on the big screen shows where.'
        : `Stand to the right of #${r.n - 1}${r.prev ? ` (${r.prev})` : ''}, as you face the big screen.`;
}

// ---- the phone-to-phone mesh (mesh.ts) ----

let lastState: PhoneState | null = null;
let lastStateAt = 0;
let lastWsRx = 0;
let gotState = false;
let clockOffset = 0;
/** When this phone was last placed exactly (a tap on the map, a tower check-in, the demo spot, staff). */
let placedAt = Date.now();
/** An exact placement counts as an anchor for this long; people move. */
const ANCHOR_S = 120;
/** Test aid: ?acc=8 makes this phone treat its own position as only good to ±8 m (like a GPS fix), so the mesh correction can be seen without GPS. */
const fuzzyAcc = Number(new URLSearchParams(location.search).get('acc')) || 0;

/** Where this phone thinks it is before its neighbours correct it, and how sure it is. */
function selfPos(): SelfPos | null {
  const st = lastState;
  const age = (Date.now() - placedAt) / 1000;
  const exact = (src: PosSrc, x: number, y: number): SelfPos =>
    fuzzyAcc > 0 ? { x, y, s: fuzzyAcc, src: 'gps', anchored: false } : { x, y, s: 0.5 + 0.01 * age, src, anchored: age < ANCHOR_S };
  if (manual) return exact('manual', manual.x, manual.y);
  if (st?.x === undefined || st.y === undefined) return tower ? exact('tower', tower.x, tower.y) : null;
  if (mode === 'gps' && cfg.geo && fix && fix.acc <= GPS_MAX_ACC) return { x: st.x, y: st.y, s: Math.max(3, fix.acc), src: 'gps', anchored: false };
  if (tower) return exact('tower', st.x, st.y);
  if (cfg.demo) return exact('demo', st.x, st.y);
  return null; // not placed yet
}

const mesh: Mesh = new Mesh({
  send,
  wsUsable,
  wsSend: (msg) => {
    if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify(msg));
  },
  closeWs: () => {
    clearTimeout(reconnectTimer);
    const sock = ws;
    ws = null; // onclose then doesn't reconnect
    sock?.close();
    renderLive();
  },
  reconnectWs: () => {
    if (!ws && !left && sensorsOn) connect();
  },
  onServerMsg: (msg) => {
    if (msg && typeof msg === 'object' && typeof (msg as { type?: unknown }).type === 'string') onServer(msg as ServerMsg);
  },
  // Through a relay the hello carries no id (the envelope names the phone by its handle) and no GPS.
  hello: () => {
    const h: Partial<Hello> = hello();
    delete h.id;
    delete h.lat;
    delete h.lon;
    delete h.acc;
    return h;
  },
  self: selfPos,
  zone: () => (lastState && Date.now() - lastStateAt < 5000 ? lastState.zone : ''),
  clockOffset: () => clockOffset,
  onChange: () => renderLive(),
});
(window as unknown as { pulseMesh: Mesh }).pulseMesh = mesh;

$('lostSig').addEventListener('change', () => {
  const box = $('lostSig') as HTMLInputElement;
  if (!mesh.setJam(box.checked, true)) {
    box.checked = false;
    $('meshLine').textContent = 'No phone linked yet, so there is nothing to relay through.';
    $('meshLine').hidden = false;
  }
});

const zoneRank = { '': 0, calm: 0, yellow: 1, red: 2 } as const;

/** Everything the live screen says about the connection and the crowd: from the server's last state, the WebSocket and the mesh. */
function renderLive() {
  const ms = mesh.status();
  const up = ws?.readyState === WebSocket.OPEN && !mesh.jammed;
  const fresh = !!lastState && Date.now() - lastStateAt < 6000;
  if (up) setConn(fresh ? 'Connected' : 'Syncing clock…', fresh ? 'on' : 'syncing');
  else if (ms.relaying) setConn('No connection — relaying through nearby phones', 'syncing');
  else if (mesh.jammed) setConn('No connection — no phone in reach', '');
  else setConn(ws ? 'Connecting…' : 'Reconnecting…', '');
  const zone = fresh ? lastState!.zone : '';
  const node = fresh ? lastState!.node : 'connecting';
  // A neighbour's warning counts when it is worse than what the server last said (or the server can't be heard).
  const byNeighbour = !!ms.warn && zoneRank[ms.warn] > zoneRank[zone];
  applyState(byNeighbour && node === 'connecting' ? 'ok' : node, byNeighbour ? ms.warn! : zone, byNeighbour);
  const line = $('meshLine');
  const text = ms.links > 0 ? `Linked to ${ms.links} ${ms.links === 1 ? 'phone' : 'phones'} nearby` : '';
  if (line.textContent !== text && !(text === '' && line.textContent?.startsWith('No phone linked'))) line.textContent = text;
  line.hidden = !line.textContent;
  $('lostWrap').hidden = !mesh.supported || (ms.links === 0 && !mesh.jammed);
  ($('lostSig') as HTMLInputElement).checked = mesh.jammed;
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
  mesh.start(); // links to nearby phones; needs no permission
  pocketReady();
}

/**
 * Back on screen (woken up, back from another app, restored from the
 * back/forward cache) or back online: a socket that has heard nothing for a
 * few seconds is dead even when the browser hasn't noticed, so start a new
 * one now instead of waiting for the backoff. Same id, so the server gives
 * back the same place, name and slot.
 */
function revive(why: string) {
  if (!sensorsOn || left || mesh.jammed) return;
  void keepAwake();
  backoff = 500;
  if (ws && ws.readyState === WebSocket.OPEN && Date.now() - lastWsRx < 4000) return;
  if (ws && ws.readyState === WebSocket.CONNECTING) return; // already on it
  if (ws) {
    const sock = ws;
    ws = null; // its onclose then doesn't schedule another
    try {
      sock.close();
    } catch {
      /* already gone */
    }
  }
  console.info(`pulse: reconnecting (${why})`);
  connect();
}

// ---- feedback from the server ----

/** What the screen shows right now, for pocket mode (pocket.ts). */
let shown = { zone: '', byNeighbour: false };
let guideAngle: number | null = null;

function syncPocket() {
  const move = guidance?.move && guideAngle !== null ? { angle: guideAngle, to: $('guideTo').textContent ?? '' } : null;
  pocketUpdate({ zone: shown.zone, byNeighbour: shown.byNeighbour, move });
}

function applyState(node: string, zone: string, byNeighbour = false) {
  shown = { zone, byNeighbour };
  const b = document.body;
  b.classList.toggle('node-handling', node === 'handling');
  b.classList.toggle('zone-yellow', zone === 'yellow');
  b.classList.toggle('zone-red', zone === 'red');
  const [icon, head, sub] = byNeighbour
    ? zone === 'red'
      ? ['⚠️', 'Warned by a neighbour', 'Stay on your feet, arms up in front of your chest.']
      : ['👀', 'Warned by a neighbour', 'Pressure is building close to you. Keep this page open.']
    : zone === 'red'
      ? ['⚠️', 'Crowd danger near you', 'Stay on your feet, arms up in front of your chest.']
      : node === 'handling'
        ? ['✋', 'Phone is moving around', 'Let it rest in your pocket or your hand so it can feel the crowd.']
        : zone === 'yellow'
          ? ['👀', 'Pressure building nearby', 'Keep this page open.']
          : node === 'connecting'
            ? ['📡', 'Connecting…', 'Hang on a second.']
            : ['📱', 'You are part of the network', 'Keep this page open. Your pocket or your hand is fine, any way up.'];
  $('icon').textContent = icon;
  $('headline').textContent = head;
  $('sub').textContent = sub;
  syncPocket();
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
  if (!s.move) {
    guideAngle = null;
    syncPocket();
    return;
  }
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
  guideAngle = screenAngle;
  $('guideTo').textContent = s.move.to ? `Toward ${s.move.to}` : s.move.reason === 'push' ? 'Out of the push, to the side' : 'Toward more space';
  $('guideNote').textContent = real
    ? 'The arrow points the real way. Turn until it points forward.'
    : 'The arrow is relative to the venue map below (stage at the top).';
  drawMiniMap(s);
  syncPocket();
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
  // Where the phone is: its mesh-corrected position when its neighbours have one to offer, else the server's.
  const mp = mesh.status().pos?.est;
  const px = ox + Math.min(vw, Math.max(0, mp?.x ?? s.x)) * k, py = oy + Math.min(vh, Math.max(0, mp?.y ?? s.y)) * k;
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

// Once a second: is it alive? (The numbers themselves are in the ?debug=1 panel.)
let lastSamples = 0;
let hz = 0;
setInterval(() => {
  hz = totalSamples - lastSamples;
  lastSamples = totalSamples;
  $('stats').textContent = `${hz} samples/s · ${sent} sent · id ${id.slice(0, 6)}`;
  if (hz > 0 && $('warn').textContent === envHelp.noMotion()) {
    $('warn').hidden = true;
    $('warnFix').hidden = true;
  }
  // A socket that has gone silent (the server sends a state every 3 s) is dead even if the browser hasn't noticed.
  if (ws?.readyState === WebSocket.OPEN && gotState && Date.now() - lastWsRx > 12_000) {
    const sock = ws;
    ws = null;
    sock.close();
    connect();
  }
  if (sensorsOn && !left) renderLive();
}, 1000);

// ---- keep the screen on ----
// The Screen Wake Lock API: Chrome 84+, Samsung Internet 14+, Safari 16.4+.
// The browser drops the lock whenever the page is hidden, so it is asked for
// again on every return. Without it (iOS before 16.4, Low Power Mode, some
// in-app browsers) the tip says how to keep the screen on by hand.

type Sentinel = { released?: boolean; release(): Promise<void>; addEventListener?(t: 'release', f: () => void): void };
let wakeLock: Sentinel | null = null;
async function keepAwake() {
  const wl = (navigator as unknown as { wakeLock?: { request(t: 'screen'): Promise<Sentinel> } }).wakeLock;
  if (!wl) dbg.wakeLock = 'not supported';
  else if (!wakeLock || wakeLock.released) {
    try {
      const s = await wl.request('screen');
      wakeLock = s;
      dbg.wakeLock = 'held';
      s.addEventListener?.('release', () => {
        dbg.wakeLock = 'released (page hidden)';
      });
    } catch (e) {
      wakeLock = null;
      dbg.wakeLock = `refused: ${e instanceof Error ? e.name : e}`; // Low Power Mode, or not visible
    }
  }
  $('screenTip').hidden = !!wakeLock && !wakeLock.released;
  $('screenTip').textContent = envHelp.screen();
}
document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'visible') revive('visible again');
});
// Back from the back/forward cache (iOS Safari does this after a switch to another app).
window.addEventListener('pageshow', (e) => {
  if ((e as PageTransitionEvent).persisted) revive('restored from cache');
});
window.addEventListener('online', () => revive('back online'));
window.addEventListener('error', (e) => {
  dbg.lastError = `${e.message} (${(e.filename ?? '').split('/').pop()}:${e.lineno})`;
});
window.addEventListener('unhandledrejection', (e) => {
  dbg.lastError = `promise: ${String((e as PromiseRejectionEvent).reason).slice(0, 160)}`;
});

// ---- leave: close the connection for good, then show the privacy receipt (demo.ts) ----

let left = false;
initLeave({
  id,
  device: deviceLabel(),
  sent: () => sent,
  linked: () => mesh.status().links,
  leave: () => {
    left = true;
    save('pulse-joined', ''); // "Join again" starts from the Join screen
    mesh.stop();
    pocketExit();
    clearTimeout(reconnectTimer);
    const sock = ws;
    ws = null; // onclose then doesn't reconnect
    sock?.close();
    window.removeEventListener('devicemotion', onMotion);
    stopGps();
    void wakeLock?.release().catch(() => {});
  },
});

initPocket(isIOS);

// Opt-in Bluetooth beacon positioning (Android Chrome only; nothing shows elsewhere): beacons.ts.
initBeacons(id, (m) => ws?.readyState === WebSocket.OPEN && ws.send(JSON.stringify(m)));
// Inside the Pulse Android app only (window.PulseNative): Bluetooth scan + advert after Join, screen-off running: native.ts.
initNative(id, (m) => ws?.readyState === WebSocket.OPEN && ws.send(JSON.stringify(m)), hello);

show('join');
checkEnv();
(window as unknown as { __pulseBooted: boolean }).__pulseBooted = true;

// The page was reloaded after this person joined (the phone slept and the
// browser threw the tab away, or a tunnel hiccup reloaded it). Where no
// permission prompt is needed (Android, the app) carry straight on with the
// same id, so the server gives back the same place in the row. An iPhone
// must tap again: Safari only asks for motion inside a tap.
if (load('pulse-joined') === '1' && window.isSecureContext) {
  const needsTap = typeof (window.DeviceMotionEvent as unknown as { requestPermission?: unknown } | undefined)?.requestPermission === 'function';
  if (needsTap) {
    $('joinBtn').textContent = 'Carry on';
    document.querySelector('#join .lead')!.textContent = 'You were already in. Tap to carry on where you were.';
  } else {
    $('joinBtn').click();
  }
}

// ?debug=1: a panel with the sensor rate, permissions, socket and clock, for troubleshooting at the table (debug.ts, loaded only then).
if (new URLSearchParams(location.search).has('debug')) {
  void import('./debug').then((d) =>
    d.startDebug(() => ({
      id: id.slice(0, 8),
      browser: browserLabel(),
      secure: window.isSecureContext,
      hz,
      samples: totalSamples,
      sent,
      fields: dbg.fields,
      motionPerm: dbg.motionPerm,
      orientPerm: dbg.orientPerm,
      compass: heading === null ? 'none' : `${Math.round(heading)}°`,
      wakeLock: dbg.wakeLock,
      socket: ws ? ['connecting', 'open', 'closing', 'closed'][ws.readyState] : mesh.jammed ? 'off (lost-signal demo)' : 'none',
      lastRx: lastWsRx ? Math.round((Date.now() - lastWsRx) / 100) / 10 : -1,
      opens: dbg.wsOpens,
      fails: dbg.wsFails,
      lastClose: dbg.lastClose,
      clockOffset,
      state: lastState,
      mesh: mesh.status().links,
      cfg: { demo: !!cfg.demo, geo: cfg.geo, w: cfg.venueW, h: cfg.venueH },
      mode,
      lastError: dbg.lastError,
    })),
  );
}
