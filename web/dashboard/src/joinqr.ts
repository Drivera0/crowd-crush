// The join QR window, made hard to get wrong at the table:
//
//   - a big warning on the code itself when phones can't use it (localhost,
//     a LAN address, plain http), with the fix;
//   - the short address in large type under the code, and where it came from
//     (settings / PUBLIC_URL / a running quick tunnel / this page's address);
//   - "Test this QR": the server fetches the link itself and says what answered;
//   - "Change link": paste the tunnel URL (saved in data/join.json, no restart);
//   - how joining is going: "3 joined · 1 couldn't get motion: in-app browser";
//   - "Line up phones at the table" when the demo spot is off.
//
// It only adds a panel to the existing #qr card (index.html) and needs one
// hook in main.ts: the "pulse:join" event, sent here after the link changes,
// makes main.ts reload the code and the address (loadJoinInfo).

import type { JoinInfo, JoinStats, JoinTest } from '../../shared/join';
import type { DemoSpot } from '../../shared/demo';

const $ = <T extends HTMLElement = HTMLElement>(id: string) => document.getElementById(id) as T;
const esc = (s: string) => s.replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);

const SOURCE: Record<string, string> = {
  settings: 'set here',
  env: 'from PUBLIC_URL',
  tunnel: 'running Cloudflare quick tunnel, found automatically',
  request: 'the address this dashboard is open on',
};

const css = `
#qr .jq { margin-top: 10px; display: flex; flex-direction: column; gap: 8px; text-align: left; }
#qr.jq-on #qrWarn { display: none; }
#qr .jq-alarm { background: #b91c1c; color: #fff; border-radius: 12px; padding: 10px 14px; font-weight: 800; font-size: 18px; line-height: 1.25; text-align: center; }
#qr .jq-alarm small { display: block; font-weight: 500; font-size: 13px; margin-top: 4px; opacity: .95; }
#qr .jq-alarm[hidden], #qr .jq [hidden] { display: none; }
#qr .jq-short { font-size: 26px; font-weight: 800; letter-spacing: .2px; word-break: break-all; margin: 0; }
#qr .jq-src { font-size: 12px; opacity: .7; margin: 0; }
#qr .jq-row { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; }
#qr .jq-res { font-size: 13px; margin: 0; padding: 6px 10px; border-radius: 8px; background: rgba(148,163,184,.12); }
#qr .jq-res.ok { background: rgba(34,197,94,.16); }
#qr .jq-res.bad { background: rgba(239,68,68,.18); }
#qr .jq-edit { display: flex; gap: 6px; flex-wrap: wrap; }
#qr .jq-edit input { flex: 1 1 220px; min-width: 0; font: inherit; padding: 6px 8px; border-radius: 8px; border: 1px solid rgba(148,163,184,.4); background: transparent; color: inherit; }
#qr .jq-stats { font-size: 14px; margin: 0; }
#qr .jq-stats b.bad { color: #f87171; }
#qr .jq-demo { font-size: 13px; margin: 0; opacity: .85; }
`;

let info: JoinInfo | null = null;
let timer = 0;

async function json<T>(method: string, url: string, body?: unknown): Promise<T> {
  const r = await fetch(url, {
    method,
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
    cache: 'no-store',
  });
  const j = r.status === 204 ? ({} as T) : ((await r.json()) as T & { error?: string });
  if (!r.ok) throw new Error((j as { error?: string }).error ?? `HTTP ${r.status}`);
  return j;
}

function build() {
  const qr = document.getElementById('qr');
  const text = qr?.querySelector('.qr-text');
  const img = document.getElementById('qrImg');
  if (!qr || !text || !img || document.getElementById('jqPanel')) return false;
  const style = document.createElement('style');
  style.textContent = css;
  document.head.append(style);
  qr.classList.add('jq-on');

  const alarm = document.createElement('div');
  alarm.id = 'jqAlarm';
  alarm.className = 'jq-alarm';
  alarm.hidden = true;
  alarm.setAttribute('role', 'alert');
  img.before(alarm);

  const panel = document.createElement('div');
  panel.id = 'jqPanel';
  panel.className = 'jq';
  panel.innerHTML = `
    <p class="jq-short" id="jqShort"></p>
    <p class="jq-src" id="jqSrc"></p>
    <div class="jq-row">
      <button class="sm" id="jqTest" type="button">Test this QR</button>
      <button class="sm" id="jqEditBtn" type="button">Change link</button>
      <button class="sm" id="jqDemo" type="button" hidden>Line up phones at the table</button>
    </div>
    <p class="jq-res" id="jqRes" hidden></p>
    <form class="jq-edit" id="jqEdit" hidden>
      <input id="jqInput" type="text" inputmode="url" autocomplete="off" spellcheck="false" placeholder="https://something.trycloudflare.com" aria-label="Join link" />
      <button class="sm" type="submit">Save</button>
      <button class="sm" type="button" id="jqTunnel" hidden>Use the tunnel</button>
      <button class="sm" type="button" id="jqClear">Back to default</button>
    </form>
    <p class="jq-stats" id="jqStats" aria-live="polite"></p>
    <p class="jq-demo" id="jqDemoLine" hidden></p>`;
  text.querySelector('#qrWarn')?.after(panel);

  $('jqTest').addEventListener('click', () => void test());
  $('jqEditBtn').addEventListener('click', () => {
    const f = $('jqEdit');
    f.hidden = !f.hidden;
    if (!f.hidden) {
      ($('jqInput') as HTMLInputElement).value = info?.override ?? '';
      $('jqInput').focus();
    }
  });
  $('jqEdit').addEventListener('submit', (e) => {
    e.preventDefault();
    void setLink(($('jqInput') as HTMLInputElement).value);
  });
  $('jqClear').addEventListener('click', () => void setLink(''));
  $('jqTunnel').addEventListener('click', () => info?.tunnel && void setLink(info.tunnel));
  $('jqDemo').addEventListener('click', () => void lineUp());
  return true;
}

function result(text: string, cls: '' | 'ok' | 'bad') {
  const r = $('jqRes');
  r.textContent = text;
  r.className = `jq-res ${cls}`;
  r.hidden = !text;
}

function renderInfo() {
  const j = info;
  if (!j) return;
  $('jqShort').textContent = j.display || j.url;
  $('jqSrc').textContent = `Address phones get: ${SOURCE[j.source ?? ''] ?? j.source ?? ''}${j.source !== 'tunnel' && j.tunnel ? ` · tunnel running at ${j.tunnel.replace('https://', '')}` : ''}`;
  const alarm = $('jqAlarm');
  const bad = !!j.problem;
  alarm.hidden = !bad;
  if (bad) {
    const head =
      j.reachable === 'local' ? 'Phones can’t open this code: it points at this laptop only' : j.secure === false ? 'Phones won’t get motion from this code: it is plain http' : 'Only phones on this Wi-Fi can open this code';
    alarm.innerHTML = `${esc(head)}<small>${esc(j.problem ?? '')}</small>`;
  }
  $('jqTunnel').hidden = !j.tunnel || j.override === j.tunnel;
}

async function loadInfo() {
  try {
    info = await json<JoinInfo>('GET', '/api/join');
    renderInfo();
  } catch {
    /* an old server: main.ts's own warning stays */
  }
}

async function setLink(url: string) {
  try {
    info = await json<JoinInfo>('PUT', '/api/join', { url });
    renderInfo();
    $('jqEdit').hidden = true;
    window.dispatchEvent(new Event('pulse:join'));
    result(url ? 'Saved. The QR code now points there; testing it…' : 'Back to the default link.', '');
    if (url) void test();
  } catch (e) {
    result(`Not saved: ${e instanceof Error ? e.message : e}`, 'bad');
  }
}

async function test() {
  result('Testing: the server is fetching the link the way a phone would…', '');
  ($('jqTest') as HTMLButtonElement).disabled = true;
  try {
    const t = await json<JoinTest>('POST', '/api/join/test', {});
    result(`${t.ok ? '✓' : '✗'} ${t.message}`, t.ok ? 'ok' : 'bad');
  } catch (e) {
    result(`Couldn’t run the test: ${e instanceof Error ? e.message : e}`, 'bad');
  } finally {
    ($('jqTest') as HTMLButtonElement).disabled = false;
  }
}

async function loadStats() {
  try {
    const s = await json<JoinStats>('GET', '/api/join/stats');
    const parts = [`<b>${s.joined}</b> joined`];
    if (s.joined > s.streaming) parts.push(`${s.joined - s.streaming} not sending motion yet`);
    const failed = s.problems.reduce((n, p) => n + p.count, 0);
    if (failed > 0) {
      const why = s.problems.map((p) => `${p.count} ${esc(p.label)}${p.browsers.length ? ` <span class="jq-src">(${esc(p.browsers.join(', '))})</span>` : ''}`);
      parts.push(`<b class="bad">${failed}</b> couldn’t get motion: ${why.join('; ')}`);
    }
    $('jqStats').innerHTML = parts.join(' · ');
  } catch {
    $('jqStats').textContent = '';
  }
  try {
    const d = await json<DemoSpot>('GET', '/api/demo');
    $('jqDemo').hidden = d.on;
    const line = $('jqDemoLine');
    line.hidden = false;
    line.textContent = d.on
      ? 'Phones that join are lined up 0.6 m apart in join order; each phone shows its number. Stand in that order, #1 on the left.'
      : 'Phones place themselves (GPS or a tap on their map). At a table, line them up instead.';
  } catch {
    /* fine */
  }
}

async function lineUp() {
  try {
    const d = await json<DemoSpot>('GET', '/api/demo');
    await json<DemoSpot>('PUT', '/api/demo', { ...d, on: true, arrange: true });
    result('Demo spot on: phones line up in join order. Move the spot on the Live map if needed.', 'ok');
    void loadStats();
    window.dispatchEvent(new Event('pulse:demo'));
  } catch (e) {
    result(`Couldn’t turn the demo spot on: ${e instanceof Error ? e.message : e}`, 'bad');
  }
}

/** While the QR window is open: keep the counts and the link fresh. */
function watch() {
  const qr = document.getElementById('qr');
  if (!qr) return;
  const tick = () => {
    if (qr.hidden) return;
    void loadStats();
  };
  new MutationObserver(() => {
    if (!qr.hidden) {
      void loadInfo();
      tick();
    }
  }).observe(qr, { attributes: true, attributeFilter: ['hidden'] });
  window.clearInterval(timer);
  timer = window.setInterval(tick, 3000);
  // The link can change under us (a tunnel started or stopped): re-check now and then.
  window.setInterval(() => !qr.hidden && void loadInfo(), 15_000);
}

export function initJoinQR() {
  try {
    if (!build()) return;
    void loadInfo();
    void loadStats();
    watch();
  } catch (e) {
    console.warn('join QR panel:', e);
  }
}
