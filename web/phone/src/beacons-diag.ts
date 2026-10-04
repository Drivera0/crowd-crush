// /beacons.html: can this phone locate itself from the Pulse boards' Bluetooth?
// Works without joining the crowd: it talks to GET /api/beacons and
// POST /api/beacons/locate only, and the server keeps nothing about it.

import type { BeaconFix, BeaconInfo } from '../../shared/beacons';
import { BEACON_FLAG, BeaconScanner, BoardLinks, beaconSupport, toSeen } from './beacons';
import type { BeaconTrack } from './beacons';

const $ = <T extends HTMLElement = HTMLElement>(id: string) => document.getElementById(id) as T;
const esc = (s: string) => s.replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);

const sup = beaconSupport();
const scanner = new BeaconScanner();
// Connect mode needs an id for the boards to report: a random one, for this page only.
const diagId = `diag-${Array.from(crypto.getRandomValues(new Uint8Array(6)), (b) => b.toString(16).padStart(2, '0')).join('')}`;
const links = new BoardLinks(() => diagId);

let info: BeaconInfo | null = null;
let fix: BeaconFix | null = null;
let tracks: BeaconTrack[] = [];
let serverDown = false;

// ---- what this browser can do ----

function renderSupport() {
  const item = (ok: boolean | null, text: string) => `<li class="${ok === null ? 'na' : ok ? 'yes' : ''}">${text}</li>`;
  $('checks').innerHTML = [
    item(sup.secure, sup.secure ? 'Secure page (https)' : 'Not a secure page: open this over https:// (the tunnel address)'),
    item(sup.scan, `Scan mode: hear the boards directly <span class="muted">(Web Bluetooth Scanning, behind a Chrome flag)</span>`),
    item(sup.connect, `Connect mode: connect to a board <span class="muted">(standard Web Bluetooth, no flag)</span>`),
  ].join('');
  const v = $('verdict');
  if (sup.scan) v.innerHTML = '<b>This phone can scan.</b> Tap “Start scanning” and allow it when Chrome asks.';
  else if (sup.connect)
    v.innerHTML = sup.android
      ? '<b>This phone can use connect mode</b> right now: tap “Connect to a Pulse board” and pick one. For scan mode (no pop-up per board), turn the flag on.'
      : '<b>This browser can use connect mode.</b> Scan mode needs Chrome with the flag below.';
  else if (sup.ios) v.innerHTML = '<b>Not possible on an iPhone or iPad:</b> no iOS browser gives a web page Bluetooth. Pulse still works here with GPS or tap-your-spot.';
  else if (!sup.secure) v.innerHTML = '<b>Open this page over https://</b> and look again.';
  else v.innerHTML = '<b>This browser has no Web Bluetooth.</b> Use Chrome on Android.';
  if (sup.watchOnly && !sup.scan) v.innerHTML += ' <span class="muted">(This browser has per-device advert watching only; Pulse doesn’t use that.)</span>';
  $('flag').textContent = BEACON_FLAG;
  $('flagHelp').hidden = sup.scan || sup.ios || !sup.secure;
  $('scanBtn').hidden = !sup.scan;
  $('connBtn').hidden = !sup.connect;
}

function renderMode() {
  const m = $('mode');
  const scanning = scanner.active;
  const n = links.connected;
  m.textContent = scanning ? (n ? `scanning + ${n} connected` : 'scan mode') : n ? `connect mode (${n} board${n > 1 ? 's' : ''})` : links.links.length ? 'connecting…' : 'not started';
  m.className = `tag ${scanning || n ? 'on' : ''}`;
  $('stopBtn').hidden = !scanning && !links.links.length;
  $('scanBtn').textContent = scanning ? 'Scanning…' : scanStopped ? 'Scan paused: tap to resume' : 'Start scanning';
  ($('scanBtn') as HTMLButtonElement).disabled = scanning;
  $('connBtn').textContent = links.links.length ? 'Connect another board' : 'Connect to a Pulse board';
  $('links').innerHTML = links.links.map((l) => `${esc(l.name)}: <b>${l.state}</b>${l.note ? ` <span class="muted">${esc(l.note)}</span>` : ''}`).join(' · ');
}

let scanStopped = false;

$('scanBtn').addEventListener('click', async () => {
  $('err').textContent = '';
  try {
    await scanner.start();
    scanStopped = false;
  } catch (e) {
    $('err').textContent = `Scanning didn’t start: ${String(e)}. Is Bluetooth on, and does Chrome have the Nearby devices (or Location) permission?`;
  }
  renderMode();
});

$('connBtn').addEventListener('click', async () => {
  $('err').textContent = '';
  try {
    await links.add();
  } catch (e) {
    if ((e as { name?: string }).name !== 'NotFoundError') $('err').textContent = `Couldn’t connect: ${String(e)}`;
  }
  renderMode();
});

$('stopBtn').addEventListener('click', () => {
  scanner.stop();
  links.close();
  tracks = [];
  fix = null;
  render();
});

links.onChange = renderMode;

// ---- the server's side: boards, model, the fix ----

async function loadInfo() {
  try {
    const r = await fetch('/api/beacons');
    if (!r.ok) throw new Error(String(r.status));
    info = (await r.json()) as BeaconInfo;
    serverDown = false;
  } catch {
    serverDown = true;
  }
  renderBoards();
}

async function locate() {
  const seen = toSeen(tracks);
  if (!seen.length && !links.links.length) {
    fix = null;
    return;
  }
  try {
    const r = await fetch('/api/beacons/locate', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(links.links.length ? { seen, id: diagId } : { seen }),
    });
    if (!r.ok) throw new Error(String(r.status));
    fix = (await r.json()) as BeaconFix;
    serverDown = false;
  } catch {
    fix = null;
    serverDown = true;
  }
}

function distance(rssi: number, tx: number): number {
  return Math.pow(10, (tx - rssi) / (10 * (info?.model.pathLossN ?? 2.2)));
}

function renderBoards() {
  const tb = $('boards');
  if (!info) {
    tb.innerHTML = `<tr><td class="empty" colspan="5">${serverDown ? 'Can’t reach the Pulse server.' : 'Loading…'}</td></tr>`;
    return;
  }
  tb.innerHTML = info.boards.length
    ? info.boards
        .map(
          (b) =>
            `<tr><td>${esc(b.name)}</td><td>${esc(b.label)}${b.online ? '' : ' <span class="muted">(offline)</span>'}</td>` +
            `<td>${b.x !== undefined && b.y !== undefined ? `${b.x.toFixed(1)}, ${b.y.toFixed(1)} m` : '<span class="tag warn">not placed</span>'}</td>` +
            `<td>${b.txPower1m} dBm${b.calibrated ? ' <span class="muted">(own)</span>' : ''}</td>` +
            `<td>${b.connectable ? `yes${b.links ? `, ${b.links} linked` : ''}${b.heard ? `, hears ${b.heard} app phone${b.heard > 1 ? 's' : ''}` : ''}` : '<span class="muted">no</span>'}</td></tr>`,
        )
        .join('')
    : '<tr><td class="empty" colspan="5">The server knows no boards (SIGN_URL is empty).</td></tr>';
  const what = ['“next to a board” only', 'a position along the line through the boards (1-D)', 'a 2-D position'][info.maxDims] ?? '';
  $('layout').textContent =
    `${info.placed} board${info.placed === 1 ? '' : 's'} on the map: the best this layout can give is ${what}. ` +
    `Model: ${info.model.txPower1m} dBm at 1 m, path-loss exponent ${info.model.pathLossN}; connect mode ${info.connTxPower1m} dBm at 1 m. ` +
    (info.placed < info.boards.length ? 'Boards that aren’t placed can’t be used: drag them onto the map on the dashboard’s Hardware page.' : '');
}

interface Row {
  name: string;
  via: 'scan' | 'conn' | 'adv';
  raw: number | null;
  rssi: number;
  ref: number;
  dist: number;
  age: number | null;
  known: boolean;
}

function rows(): Row[] {
  const now = Date.now();
  const out: Row[] = [];
  const board = (name: string) => info?.boards.find((b) => b.name === name);
  for (const t of tracks) {
    const h = fix?.heard.find((x) => x.name === t.name && x.src === 'scan');
    const ref = board(t.name)?.txPower1m ?? info?.model.txPower1m ?? -64;
    out.push({ name: t.name, via: 'scan', raw: t.raw, rssi: t.rssi, ref, dist: h?.dist ?? distance(t.rssi, ref), age: (now - t.at) / 1000, known: !!board(t.name) || !info });
  }
  for (const h of fix?.heard ?? []) {
    if (h.src === 'scan') continue;
    out.push({ name: h.name, via: h.src, raw: null, rssi: h.rssi, ref: info?.connTxPower1m ?? -64, dist: h.dist, age: null, known: true });
  }
  return out;
}

function renderRows() {
  const rs = rows();
  $('rows').innerHTML = rs.length
    ? rs
        .map(
          (r) =>
            `<tr><td>${esc(r.name)}${r.known ? '' : ' <span class="muted">(not this venue’s)</span>'}</td>` +
            `<td>${r.via === 'scan' ? 'scan' : r.via === 'conn' ? 'connection' : 'app advert'}</td><td>${r.raw === null ? '–' : `${r.raw} dBm`}</td><td>${r.rssi.toFixed(1)} dBm</td>` +
            `<td>${r.ref} dBm</td><td><b>${r.dist.toFixed(1)} m</b></td><td>${r.age === null ? '–' : `${r.age.toFixed(1)} s`}</td>` +
            `<td>${r.known ? `<button class="ghost tiny" data-cal="${esc(r.name)}" data-via="${r.via}" data-rssi="${Math.round(r.rssi)}">1 m</button>` : ''}</td></tr>`,
        )
        .join('')
    : `<tr><td class="empty" colspan="8">${
        scanner.active
          ? 'Scanning: no Pulse board heard yet. Are the boards powered?'
          : links.connected
            ? 'Connected. Waiting for the board to report this connection (a second or two; the server asks the board over Wi-Fi)…'
            : 'Nothing yet.'
      }</td></tr>`;
}

$('rows').addEventListener('click', async (ev) => {
  const b = (ev.target as HTMLElement).closest<HTMLElement>('[data-cal]');
  if (!b) return;
  const name = b.dataset.cal!;
  const tx = Number(b.dataset.rssi);
  const conn = b.dataset.via !== 'scan';
  const what = conn ? 'board-side measurements (connect mode and the app, every board)' : name;
  if (!confirm(`Are you standing 1 m from ${name}? This sets the 1 m reference for ${what} to ${tx} dBm, for everyone.`)) return;
  try {
    const r = await fetch('/api/beacons/model', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(conn ? { conn: true, txPower1m: tx } : { beacon: name, txPower1m: tx }),
    });
    const j = (await r.json()) as BeaconInfo & { error?: string };
    if (!r.ok) throw new Error(j.error ?? String(r.status));
    info = j;
    $('err').textContent = '';
    renderBoards();
  } catch (e) {
    $('err').textContent = `Couldn’t save the calibration: ${String(e)}`;
  }
});

function renderFix() {
  const f = $('fix');
  const n = $('fixNote');
  if (serverDown && (tracks.length || links.links.length)) {
    f.textContent = '–';
    n.textContent = 'Can’t reach the Pulse server, so no position; the distances above use the last known model.';
  } else if (!fix) {
    f.textContent = '–';
    n.textContent = 'Start scanning or connect to a board.';
  } else if (!fix.ok) {
    f.textContent = 'No position';
    n.textContent = fix.note;
  } else {
    f.textContent =
      fix.dims === 2
        ? `${fix.x.toFixed(1)}, ${fix.y.toFixed(1)} m  ± ${fix.acc.toFixed(1)} m`
        : fix.dims === 1
          ? `${fix.x.toFixed(1)}, ${fix.y.toFixed(1)} m  ± ${(fix.along ?? 0).toFixed(1)} m along, ± ${(fix.cross ?? 0).toFixed(1)} m across`
          : `Next to ${fix.near ?? 'a board'}`;
    n.textContent = `${['“Near a board”', '1-D fix', '2-D fix'][fix.dims] ?? ''}. ${fix.note}`;
  }
  drawMap();
}

function drawMap() {
  const c = $('map') as HTMLCanvasElement;
  const g = c.getContext('2d');
  if (!g) return;
  const W = c.width, H = c.height;
  g.clearRect(0, 0, W, H);
  const vw = info?.venueW ?? 24, vh = info?.venueH ?? 16;
  const k = Math.min((W - 60) / vw, (H - 60) / vh);
  const ox = (W - vw * k) / 2, oy = (H - vh * k) / 2;
  const X = (x: number) => ox + x * k, Y = (y: number) => oy + y * k;
  g.save();
  g.strokeStyle = '#3b4a5e';
  g.lineWidth = 2;
  g.strokeRect(ox, oy, vw * k, vh * k);
  g.fillStyle = '#8a97a8';
  g.font = '12px sans-serif';
  g.fillText(`${vw} × ${vh} m`, ox, oy - 8);
  // Everything else is clipped to a margin around the venue (rings can be large).
  g.beginPath();
  g.rect(0, 0, W, H);
  g.clip();
  const heard = new Map((fix?.heard ?? []).map((h) => [h.name, h]));
  for (const b of info?.boards ?? []) {
    if (b.x === undefined || b.y === undefined) continue;
    const h = heard.get(b.name);
    if (h) {
      g.strokeStyle = 'rgba(56, 189, 248, 0.55)';
      g.lineWidth = 1.5;
      g.setLineDash([5, 4]);
      g.beginPath();
      g.arc(X(b.x), Y(b.y), h.dist * k, 0, Math.PI * 2);
      g.stroke();
      g.setLineDash([]);
    }
    g.fillStyle = h ? '#38bdf8' : '#64748b';
    g.fillRect(X(b.x) - 6, Y(b.y) - 6, 12, 12);
    g.fillStyle = '#e8eef5';
    g.fillText(b.name.replace('PULSE-', ''), X(b.x) + 10, Y(b.y) + 4);
  }
  if (fix?.ok) {
    const px = X(fix.x), py = Y(fix.y);
    g.fillStyle = 'rgba(34, 197, 94, 0.18)';
    g.strokeStyle = 'rgba(34, 197, 94, 0.7)';
    g.lineWidth = 1.5;
    g.beginPath();
    if (fix.dims === 1 && fix.axis) g.ellipse(px, py, Math.max(4, (fix.along ?? 1) * k), Math.max(4, (fix.cross ?? 2) * k), Math.atan2(fix.axis[1], fix.axis[0]), 0, Math.PI * 2);
    else g.arc(px, py, Math.max(4, fix.acc * k), 0, Math.PI * 2);
    g.fill();
    g.stroke();
    g.fillStyle = '#22c55e';
    g.beginPath();
    g.arc(px, py, 6, 0, Math.PI * 2);
    g.fill();
    g.strokeStyle = '#fff';
    g.lineWidth = 2;
    g.stroke();
  }
  g.restore();
}

function render() {
  renderMode();
  renderRows();
  renderFix();
}

async function tick() {
  if (scanner.active) tracks = scanner.read();
  else if (tracks.length) {
    // Chrome stops a scan when the page is hidden; starting again needs a tap.
    tracks = [];
    scanStopped = true;
  }
  await locate();
  render();
}

renderSupport();
render();
void loadInfo();
setInterval(() => void tick(), 1000);
setInterval(() => void loadInfo(), 5000);
