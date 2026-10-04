// Headless check of moving about at the table demo (server app/demomove.go,
// app/beaconsnap.go; phone src/demomove.ts): three phones line up after
// "Set up table demo", one taps a new spot on the zoomed "I moved" map and
// goes back to its place, staff drag-swap two on the dashboard, and a fake
// Android phone walks up to zone light A over Bluetooth and away again.
//
//   PULSE_URL=http://localhost:8101 SHOTS=/tmp/shots node phone/e2e/move.e2e.mjs
//
// It changes the server's state (boards, demo spot): run it against a
// throwaway server with its own -data directory, never the real one.

import { chromium } from 'playwright-core';
import { execSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';

const BASE = (process.env.PULSE_URL ?? 'http://localhost:8101').replace(/\/$/, '');
const SHOTS = process.env.SHOTS ?? '';
const chromePath = process.env.CHROME_PATH ?? execSync('command -v google-chrome || command -v chromium', { shell: '/bin/sh' }).toString().trim();
const ANDROID = 'Mozilla/5.0 (Linux; Android 14; SM-S918B) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Mobile Safari/537.36';

const results = [];
function check(name, ok, detail = '') {
  results.push({ name, ok });
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${name}${detail ? ` — ${detail}` : ''}`);
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function api(path, init) {
  const r = await fetch(BASE + path, init);
  return r.json();
}
const json = (method, body) => ({ method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
async function shot(page, name) {
  if (!SHOTS) return;
  mkdirSync(SHOTS, { recursive: true });
  await page.screenshot({ path: `${SHOTS}/${name}.png` });
}

/** Android Chrome: no permission prompt, devicemotion at 60 Hz. */
function shape() {
  let t = 0;
  setInterval(() => {
    t += 16;
    const s = Math.sin(t / 300);
    window.dispatchEvent(new DeviceMotionEvent('devicemotion', { acceleration: { x: 0.2 * s, y: 0.05, z: -0.1 * s }, accelerationIncludingGravity: { x: 0.2 * s, y: 9.81, z: -0.1 * s }, rotationRate: { alpha: 2, beta: 1, gamma: 0.5 }, interval: 16 }));
  }, 16);
}

async function main() {
  const browser = await chromium.launch({ executablePath: chromePath, headless: true });
  const table = await api('/api/hardware/table', json('POST', {}));
  const hw = Object.fromEntries(table.hardware.filter((h) => h.x != null).map((h) => [h.key, [h.x, h.y]]));
  check('table demo: boards 1.5 m apart', Math.abs(hw.B[0] - hw.A[0] - 3) < 0.01, JSON.stringify(hw));

  const phones = [];
  for (let i = 0; i < 3; i++) {
    const ctx = await browser.newContext({ userAgent: ANDROID, viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 2 });
    await ctx.addInitScript(shape);
    const page = await ctx.newPage();
    const errors = [];
    page.on('pageerror', (e) => errors.push(String(e)));
    await page.goto(BASE + '/');
    await page.click('#joinBtn');
    await page.waitForSelector('#rowCard:not([hidden])', { timeout: 10_000 });
    const id = await page.evaluate(() => sessionStorage.getItem('pulse-id'));
    phones.push({ page, id, errors });
    await sleep(300); // join order
  }
  const rowN = (p) => p.page.textContent('#rowN');
  check('three phones lined up #1 #2 #3', (await Promise.all(phones.map(rowN))).join(' ') === '#1 #2 #3');
  const [p1, p2, p3] = phones;
  await shot(p2.page, '1-phone2-live');

  const dash = await (await browser.newContext({ viewport: { width: 1400, height: 900 } })).newPage();
  await dash.goto(BASE + '/dash/#live');
  await sleep(2500);
  await shot(dash, '2-dash-row');

  // ---- tap to move ----
  await p2.page.click('#iMoved');
  await p2.page.waitForSelector('#place:not([hidden])');
  await sleep(500);
  const vb = (await p2.page.getAttribute('#venueSvg', 'viewBox')).split(' ').map(Number);
  check('placement map zoomed to the row', vb[2] < 10 && vb[3] < 8, `viewBox ${vb.join(' ')}`);
  const others = await p2.page.$$eval('#venueSvg text', (ts) => ts.map((t) => t.textContent));
  check('map shows #1, #3 and the boards', others.includes('#1') && others.includes('#3') && others.includes('Zone light A'), others.join(', '));
  await shot(p2.page, '3-phone2-zoomed-map');
  const n1 = await api(`/api/node/${p1.id}`);
  const target = { x: n1.x, y: n1.y + 0.5 }; // right behind #1
  const box = await p2.page.locator('#venue').boundingBox();
  await p2.page.mouse.click(box.x + ((target.x - vb[0]) / vb[2]) * box.width, box.y + ((target.y - vb[1]) / vb[3]) * box.height);
  await shot(p2.page, '4-phone2-tapped');
  await p2.page.click('#placeDone');
  await sleep(1200);
  let n2 = await api(`/api/node/${p2.id}`);
  check('tap moved the dot behind #1', Math.hypot(n2.x - target.x, n2.y - target.y) < 0.1, `${n2.x}, ${n2.y} vs ${target.x}, ${target.y}`);
  check('phone 2 lost its number, can go back to #2', (await p2.page.isHidden('#rowCard')) && (await p2.page.textContent('#backRow')).includes('#2'));
  await shot(p2.page, '5-phone2-after-tap');
  await sleep(600);
  await shot(dash, '6-dash-after-tap');

  await p2.page.click('#backRow');
  await sleep(1200);
  n2 = await api(`/api/node/${p2.id}`);
  check('back to the row: #2 again', (await rowN(p2)) === '#2' && (await p2.page.isHidden('#backRow')), `${n2.x}, ${n2.y}`);

  // ---- staff swap on the dashboard: drag #1's dot onto #3 ----
  const n3 = await api(`/api/node/${p3.id}`);
  const nn1 = await api(`/api/node/${p1.id}`);
  const c = await dash.locator('#mesh').boundingBox();
  // Venue metres → page px through the map's own transform (it zooms to fit the phones).
  const scr = async (n) => {
    const p = await dash.evaluate(([x, y]) => {
      const m = window.pulseMap;
      const w = m.venueToWorld(x, y);
      return m.toScreen(w.x, w.y);
    }, [n.x, n.y]);
    return { x: c.x + p.x, y: c.y + p.y };
  };
  const a = await scr(nn1), b = await scr(n3);
  await dash.mouse.move(a.x, a.y);
  await dash.mouse.down();
  for (let i = 1; i <= 10; i++) await dash.mouse.move(a.x + ((b.x - a.x) * i) / 10, a.y + ((b.y - a.y) * i) / 10);
  await dash.mouse.up();
  await sleep(1500);
  const rows = await Promise.all(phones.map(rowN));
  check('drag-swap #1 onto #3: phone 1 is #3, phone 3 is #1', rows[0] === '#3' && rows[2] === '#1', rows.join(' '));
  await shot(dash, '7-dash-after-swap');
  await shot(p1.page, '8-phone1-after-swap');

  // ---- walk to a board: a fake Android-app phone reporting beacons ----
  const fid = 'a1b2c3d4-0000-4000-8000-00000000beef';
  const ws = new WebSocket(BASE.replace(/^http/, 'ws') + '/ws/phone');
  const states = [];
  ws.onmessage = (ev) => {
    const m = JSON.parse(ev.data);
    if (m.type === 'ping') ws.send(JSON.stringify({ type: 'pong', t0: m.t0, t1: Date.now() }));
    if (m.type === 'state') states.push(m);
  };
  await new Promise((r) => (ws.onopen = r));
  ws.send(JSON.stringify({ type: 'hello', id: fid, ua: 'Android' }));
  await sleep(1500);
  const home = await api(`/api/node/${fid}`);
  const rssi = (px, py, [bx, by]) => Math.round(-64 - 22 * Math.log10(Math.max(0.1, Math.hypot(px - bx, py - by))));
  const report = (px, py) => ws.send(JSON.stringify({ type: 'beacons', seen: [{ name: 'PULSE-A', rssi: rssi(px, py, hw.A), n: 12 }, { name: 'PULSE-B', rssi: rssi(px, py, hw.B), n: 12 }] }));
  const motion = setInterval(() => ws.send(JSON.stringify({ type: 'm', t: Date.now(), ax: 0.01, ay: 0, az: 0, rot: 1 })), 100);
  for (let i = 0; i < 4; i++) {
    report(hw.A[0] + 0.1, hw.A[1] + 0.1); // a hand's width from A
    await sleep(1000);
  }
  await sleep(800);
  const near = states.at(-1);
  const nd = await api(`/api/node/${fid}`);
  check('walked up to A: snapped beside it', near?.near === 'Zone light A' && nd.src === 'beacon' && Math.hypot(nd.x - hw.A[0], nd.y - hw.A[1]) < 0.5, `state near ${near?.near}, ${nd.x}, ${nd.y} src ${nd.src}`);
  await shot(dash, '9-dash-near-A');
  for (let i = 0; i < 7; i++) {
    report(hw.A[0], hw.A[1] + 3); // 3 m away
    await sleep(1000);
  }
  await sleep(800);
  const back = await api(`/api/node/${fid}`);
  check('walked away: back on its place in the row', !states.at(-1)?.near && back.src !== 'beacon' && Math.hypot(back.x - home.x, back.y - home.y) < 0.01 && states.at(-1)?.row?.n === 4, `${back.x}, ${back.y} (was ${home.x}, ${home.y}) row ${JSON.stringify(states.at(-1)?.row)}`);
  clearInterval(motion);
  ws.close();

  for (const p of phones) check(`no page errors (${p.id.slice(0, 6)})`, p.errors.length === 0, p.errors.join('; '));
  await browser.close();
  const failed = results.filter((r) => !r.ok).length;
  console.log(`${results.length - failed}/${results.length} passed`);
  process.exit(failed ? 1 : 0);
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
