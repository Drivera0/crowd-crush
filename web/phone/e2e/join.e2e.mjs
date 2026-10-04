// Headless join test for the phone page: every browser profile a judge might
// scan the QR code with, emulated in desktop Chrome (user agent, touch, the
// motion APIs each browser has or lacks, synthetic devicemotion), against a
// running Pulse server. Checks what the person sees and what the dashboard
// is told (/api/join/stats, /api/node/{id}).
//
//   PULSE_URL=http://localhost:8097 PULSE_LAN_URL=http://192.168.1.20:8097 npm run test:e2e
//
// PULSE_URL must be a secure context (localhost counts); PULSE_LAN_URL (a
// LAN IP, plain http: not secure) is for the "opened over http" case and is
// skipped when unset. Chrome: CHROME_PATH, else google-chrome on the PATH.
// The demo spot is turned on for the run (and back off at the end, if it was).
// Emulation can't prove a real iPhone grants motion or keeps the screen on:
// docs/SETUP.md lists what still needs a real phone.

import { chromium } from 'playwright-core';
import { execSync } from 'node:child_process';

const BASE = (process.env.PULSE_URL ?? 'http://localhost:8097').replace(/\/$/, '');
const LAN = process.env.PULSE_LAN_URL?.replace(/\/$/, '');
const chromePath =
  process.env.CHROME_PATH ??
  (() => {
    try {
      return execSync('command -v google-chrome || command -v chromium || command -v chromium-browser', { shell: '/bin/sh' }).toString().trim();
    } catch {
      return undefined;
    }
  })();

const UA = {
  ios17: 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Mobile/15E148 Safari/604.1',
  ios15: 'Mozilla/5.0 (iPhone; CPU iPhone OS 15_2 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/15.2 Mobile/15E148 Safari/604.1',
  iosChrome: 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/124.0.6367.88 Mobile/15E148 Safari/604.1',
  instagram: 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 Instagram 330.0.0.0.0 (iPhone14,5; iOS 17_4; en_US)',
  linkedinAndroid: 'Mozilla/5.0 (Linux; Android 13; Pixel 7 Build/TQ3A; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/120.0.0.0 Mobile Safari/537.36 LinkedInApp',
  chrome96: 'Mozilla/5.0 (Linux; Android 12; SM-G975F) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/96.0.4664.104 Mobile Safari/537.36',
  samsung16: 'Mozilla/5.0 (Linux; Android 12; SAMSUNG SM-G975F) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/16.0 Chrome/92.0.4515.166 Mobile Safari/537.36',
  firefox: 'Mozilla/5.0 (Android 12; Mobile; rv:120.0) Gecko/120.0 Firefox/120.0',
  pulseApp: 'Mozilla/5.0 (Linux; Android 12; SM-G975F Build/SP1A; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/120.0.0.0 Mobile Safari/537.36 PulseApp/0.3',
  desktop: 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36',
};

/**
 * Runs in the page before its scripts: shapes the motion APIs like the
 * emulated browser and, with motion on, fires devicemotion at 60 Hz.
 * o.perm: 'granted' | 'denied' | 'throw' | undefined (no requestPermission: Android).
 * o.motion: 'full' | 'gravity' (no gyroscope) | 'none' (no events) . o.noApi: no DeviceMotionEvent.
 * o.noWakeLock: no navigator.wakeLock (iOS < 16.4).
 */
function shapeBrowser(o) {
  if (o.noWakeLock) {
    try {
      Object.defineProperty(Navigator.prototype, 'wakeLock', { get: () => undefined, configurable: true });
    } catch {}
  }
  if (!window.DeviceMotionEvent) return; // about:blank before the page loads
  if (o.noApi) {
    delete window.DeviceMotionEvent;
    return;
  }
  if (!o.perm) {
    // Android browsers (Chrome 96, Samsung 16, Firefox) have no permission prompt. Newer desktop Chrome may: hide it.
    try {
      delete window.DeviceMotionEvent.requestPermission;
      if (window.DeviceOrientationEvent) delete window.DeviceOrientationEvent.requestPermission;
    } catch {}
  }
  if (o.perm) {
    const answer = () => (o.perm === 'throw' ? Promise.reject(new DOMException('Requesting device orientation or motion access requires a user gesture to prompt', 'NotAllowedError')) : Promise.resolve(o.perm));
    window.DeviceMotionEvent.requestPermission = answer;
    if (window.DeviceOrientationEvent) window.DeviceOrientationEvent.requestPermission = answer;
  }
  if (o.motion === 'none') return;
  let t = 0;
  const fire = () => {
    t += 16;
    const s = Math.sin(t / 300);
    const acc = o.motion === 'gravity' ? null : { x: 0.3 * s, y: 0.05, z: -0.1 * s };
    const g = { x: 0.3 * s, y: 9.81, z: -0.1 * s };
    const rot = o.motion === 'gravity' ? null : { alpha: 2, beta: 1, gamma: 0.5 };
    window.dispatchEvent(new DeviceMotionEvent('devicemotion', { acceleration: acc, accelerationIncludingGravity: g, rotationRate: rot, interval: 16 }));
  };
  setInterval(fire, 16);
}

async function api(path, init) {
  const r = await fetch(BASE + path, init);
  return r.status === 204 ? null : r.json();
}

const results = [];
function check(name, ok, detail = '') {
  results.push({ name, ok, detail });
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${name}${detail ? ` — ${detail}` : ''}`);
}

async function main() {
  const browser = await chromium.launch({ executablePath: chromePath, headless: true });
  const demoBefore = await api('/api/demo');
  await api('/api/demo', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ...demoBefore, on: true }) });

  const open = async (ua, shape, { url = BASE + '/', mobile = true } = {}) => {
    const ctx = await browser.newContext({ userAgent: ua, viewport: { width: 390, height: 844 }, isMobile: mobile, hasTouch: mobile, deviceScaleFactor: 2 });
    await ctx.addInitScript(shapeBrowser, shape);
    const page = await ctx.newPage();
    const errors = [];
    page.on('pageerror', (e) => errors.push(String(e)));
    await page.goto(url);
    return { ctx, page, errors };
  };
  const text = (page, sel) => page.locator(sel).innerText().catch(() => '');
  const visible = (page, sel) => page.locator(sel).isVisible().catch(() => false);
  const sessionId = (page) => page.evaluate(() => sessionStorage.getItem('pulse-id'));

  // 1. iPhone Safari 17: Allow → live, streaming, told its place in the row.
  {
    const { ctx, page, errors } = await open(UA.ios17, { perm: 'granted', motion: 'full' });
    await page.click('#joinBtn');
    await page.waitForSelector('#live:not([hidden])', { timeout: 8000 });
    await page.waitForSelector('#rowCard:not([hidden])', { timeout: 8000 }).catch(() => {});
    const row = await text(page, '#rowCard');
    await page.waitForFunction(() => document.getElementById('conn')?.textContent === 'Connected', null, { timeout: 8000 }).catch(() => {});
    const id = await sessionId(page);
    await page.waitForTimeout(1500);
    const node = await api(`/api/node/${id}`);
    check('iOS 17 Safari: joins, streams, row place shown', /#\d+ in the row/.test(row) && node.messages > 10 && errors.length === 0, `${row.replace(/\n/g, ' ')} · ${node.messages} msgs · src ${node.src} · errors ${errors.join('; ')}`);
    check('iOS 17 Safari: no diagnostics line for attendees', !(await visible(page, '#stats')));
    // The tunnel hiccups: offline 3 s, back online → same id, same place.
    const before = await text(page, '#rowN');
    await ctx.setOffline(true);
    await page.waitForTimeout(3000);
    await ctx.setOffline(false);
    await page.waitForFunction(() => document.getElementById('conn')?.textContent === 'Connected', null, { timeout: 15000 }).catch(() => {});
    await page.waitForTimeout(1000);
    const after = await text(page, '#rowN');
    check('reconnect after going offline keeps id and row place', before === after && (await sessionId(page)) === id && (await text(page, '#conn')) === 'Connected', `${before} → ${after}`);
    // Reload (a discarded tab): iPhone must tap again, keeps its place.
    await page.reload();
    const btn = await text(page, '#joinBtn');
    await page.click('#joinBtn');
    await page.waitForSelector('#rowCard:not([hidden])', { timeout: 8000 }).catch(() => {});
    check('iOS reload: "Carry on" tap, same place', btn === 'Carry on' && (await text(page, '#rowN')) === before, `${btn}, ${await text(page, '#rowN')}`);
    await ctx.close();
  }

  // 2. Android Chrome 96 (Galaxy S10+): no prompt; joins; a reload carries on by itself.
  {
    const { ctx, page, errors } = await open(UA.chrome96, { motion: 'full' });
    await page.click('#joinBtn');
    await page.waitForSelector('#rowCard:not([hidden])', { timeout: 8000 }).catch(() => {});
    const n = await text(page, '#rowN');
    const where = await text(page, '#rowWhere');
    check('Android Chrome 96: joins with no prompt, told where to stand', /#\d+/.test(n) && /right of #|left end/.test(where) && errors.length === 0, `${n}: ${where}`);
    await page.reload();
    await page.waitForSelector('#live:not([hidden])', { timeout: 8000 }).catch(() => {});
    await page.waitForSelector('#rowCard:not([hidden])', { timeout: 8000 }).catch(() => {});
    check('Android reload rejoins by itself, same place', (await visible(page, '#live')) && (await text(page, '#rowN')) === n, await text(page, '#rowN'));
    await ctx.close();
  }

  // 3. Gravity only (no gyroscope): still streams.
  {
    const { ctx, page } = await open(UA.samsung16, { motion: 'gravity' });
    await page.click('#joinBtn');
    await page.waitForSelector('#live:not([hidden])', { timeout: 8000 });
    await page.waitForTimeout(2500);
    const node = await api(`/api/node/${await sessionId(page)}`);
    check('Samsung Internet 16, no gyroscope: streams from gravity fallback', node.messages > 10 && !(await visible(page, '#warn')), `${node.messages} msgs`);
    await ctx.close();
  }

  // 4. Failure paths: what the person reads, what the dashboard is told.
  const failures = [
    ['iOS Safari: motion denied', UA.ios17, { perm: 'denied', motion: 'full' }, '#joinErr', /aA.*Website Settings.*Motion/, 'motion-denied'],
    ['iOS: prompt throws (not a direct tap)', UA.ios17, { perm: 'throw', motion: 'full' }, '#joinErr', /tapped directly/, 'perm-error'],
    ['iOS Chrome (WebKit): motion denied', UA.iosChrome, { perm: 'denied', motion: 'full' }, '#joinErr', /aA.*Motion/, 'motion-denied'],
    ['Instagram in-app (iOS): denied → open in Safari', UA.instagram, { perm: 'denied', motion: 'full' }, '#joinErr', /Open in Safari/, 'inapp'],
    ['LinkedIn in-app (Android): no events → open in Chrome', UA.linkedinAndroid, { motion: 'none' }, '#warn', /Open in Chrome/, 'inapp'],
    ['Firefox Android: no events', UA.firefox, { motion: 'none' }, '#warn', /Firefox.*Chrome/, 'no-motion'],
    ['Samsung Internet: no events', UA.samsung16, { motion: 'none' }, '#warn', /Samsung|Site permissions/, 'no-motion'],
    ['Chrome Android: no events', UA.chrome96, { motion: 'none' }, '#warn', /Motion sensors → Allow/, 'no-motion'],
    ['No DeviceMotionEvent at all', UA.chrome96, { noApi: true }, '#joinErr', /doesn’t give pages motion/, 'no-sensor'],
    ['Laptop browser', UA.desktop, { motion: 'none' }, '#warn', /laptop or desktop/, 'no-motion'],
  ];
  for (const [name, ua, shape, sel, re, reason] of failures) {
    const { ctx, page, errors } = await open(ua, shape, { mobile: ua !== UA.desktop });
    await page.click('#joinBtn');
    await page.waitForFunction((s) => !document.querySelector(s)?.hidden, sel, { timeout: 6000 }).catch(() => {});
    const msg = await text(page, sel);
    await page.waitForTimeout(400);
    const stats = await api('/api/join/stats');
    const counted = stats.problems.some((p) => p.reason === reason);
    check(name, re.test(msg) && counted && errors.length === 0, `“${msg.slice(0, 110)}” · reported ${reason}: ${counted}`);
    await ctx.close();
  }

  // 5. In-app browsers get the way out before Join; the Android one an intent link to Chrome.
  {
    const { ctx, page } = await open(UA.linkedinAndroid, { motion: 'full' });
    const note = await text(page, '#envText');
    const href = await page.locator('#envOpen').getAttribute('href');
    check('in-app (Android): warned before Join, Open-in-Chrome intent', /built-in browser/.test(note) && /^intent:\/\/.*package=com\.android\.chrome/.test(href ?? ''), href ?? '');
    await page.click('#joinBtn');
    await page.waitForSelector('#live:not([hidden])', { timeout: 8000 }).catch(() => {});
    check('in-app (Android) that does pass motion still joins', await visible(page, '#live'));
    await ctx.close();
  }
  {
    const { ctx, page } = await open(UA.pulseApp, { motion: 'full' });
    check('Pulse Android app WebView: no in-app warning', !(await visible(page, '#envNote')));
    await ctx.close();
  }

  // 6. iOS 15 (no Screen Wake Lock): joins, says how to keep the screen on.
  {
    const { ctx, page } = await open(UA.ios15, { perm: 'granted', motion: 'full', noWakeLock: true });
    await page.click('#joinBtn');
    await page.waitForSelector('#live:not([hidden])', { timeout: 8000 });
    await page.waitForTimeout(500);
    check('iOS 15 without wake lock: Auto-Lock tip', /Auto-Lock → Never/.test(await text(page, '#screenTip')), await text(page, '#screenTip'));
    await ctx.close();
  }

  // 7. Plain http on a LAN address: told what to do before tapping anything.
  if (LAN) {
    const { ctx, page } = await open(UA.ios17, { perm: 'granted', motion: 'full' }, { url: LAN + '/' });
    const note = await text(page, '#envText');
    const disabled = await page.locator('#joinBtn').isDisabled();
    await page.waitForTimeout(500);
    const stats = await api('/api/join/stats');
    check('plain http (LAN): warned, Join disabled, reported', /plain http/.test(note) && disabled && stats.problems.some((p) => p.reason === 'insecure'), note.slice(0, 120));
    await ctx.close();
  } else {
    console.log('skip plain-http case (set PULSE_LAN_URL)');
  }

  // 8. ?debug=1: the troubleshooting panel.
  {
    const { ctx, page } = await open(UA.chrome96, { motion: 'full' }, { url: BASE + '/?debug=1' });
    await page.click('#joinBtn');
    await page.waitForTimeout(2500);
    const panel = await text(page, '#debugPanel');
    check('?debug=1 panel: sensor rate, permissions, socket, clock', /sensor rate.*\d+ Hz/s.test(panel) && /socket.*open/s.test(panel) && /clock offset/.test(panel) && /wake lock/.test(panel), panel.split('\n').slice(0, 3).join(' | '));
    await ctx.close();
  }

  // 9. The placement map shows the venue's areas and exits (demo spot off for this one).
  {
    await api('/api/demo', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ...demoBefore, on: false }) });
    const { ctx, page } = await open(UA.chrome96, { motion: 'full' });
    await page.click('#joinBtn');
    await page.waitForSelector('#place:not([hidden])', { timeout: 15000 }).catch(() => {});
    await page.waitForTimeout(800);
    const shapes = await page.locator('#venueSvg > *').count();
    const areas = await api('/api/areas');
    const venue = await api('/api/venue');
    const want = areas.length + (venue.layout?.exits?.length ?? 0) + (venue.layout?.walls?.length ?? 0);
    check('placement map draws areas, exits and walls', want === 0 ? shapes === 0 : shapes >= want, `${shapes} shapes for ${areas.length} areas, ${venue.layout?.exits?.length ?? 0} exits`);
    await ctx.close();
    await api('/api/demo', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ...demoBefore, on: true }) });
  }

  await api('/api/demo', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(demoBefore) });
  await browser.close();
  const failed = results.filter((r) => !r.ok);
  console.log(`\n${results.length - failed.length}/${results.length} passed`);
  process.exit(failed.length ? 1 : 0);
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
