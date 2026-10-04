// Hardware page extras for a table demo: one click to lay the boards out
// (POST /api/hardware/table), a board test (POST /api/hardware/test), and a
// "Board readiness" panel that says for each board how Pulse reaches it
// (USB / Wi-Fi / offline), whether its firmware matches this checkout, what
// its light should be doing right now, and the one next step when something
// is wrong. Self-contained: its own cards, styles and polling; main.ts only
// calls initHwSetup.

import type { BoardTest, Hardware, TableDemo } from '../../shared/protocol';

export interface HwSetupCtx {
  toast: (text: string, kind?: 'info' | 'ok' | 'watch' | 'danger' | 'error') => void;
  /** Reload the Hardware list and map markers now. */
  refresh: () => void;
}

const CSS = `
.hws-row { display: grid; grid-template-columns: 10px 1fr; gap: 4px 10px; padding: 10px 0; border-top: 1px solid var(--line); }
.hws-row:first-child { border-top: 0; }
.hws-dot { width: 10px; height: 10px; border-radius: 50%; margin-top: 5px; background: var(--stale); }
.hws-dot.ok { background: var(--ok); }
.hws-dot.warn { background: var(--swaying); }
.hws-dot.bad { background: var(--wave); }
.hws-main { min-width: 0; }
.hws-top { display: flex; justify-content: space-between; gap: 8px; align-items: baseline; }
.hws-facts { display: flex; flex-wrap: wrap; gap: 4px 12px; font-size: 12.5px; color: var(--fg-2); margin-top: 2px; }
.hws-led { font-size: 12.5px; color: var(--muted); margin-top: 4px; }
.hws-next { font-size: 12.5px; margin-top: 4px; }
.hws-next b { font-weight: 600; }
.hws-actions { display: flex; flex-wrap: wrap; gap: 8px; margin: 8px 0 4px; }
.hws-out { font-size: 12.5px; margin-top: 6px; }
.hws-out li { margin: 2px 0; }
.hws-out code, .hws-row code { font-size: 12px; }
`;

const esc = (s: string) => s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]!);

async function post<T>(path: string, body: unknown = {}): Promise<T> {
  const r = await fetch(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
  const j = (await r.json().catch(() => ({}))) as T & { error?: string };
  if (!r.ok) throw new Error(j.error ?? r.statusText);
  return j;
}

const isSerial = (h: Hardware) => h.url.toLowerCase().startsWith('serial:');

/** How Pulse reaches the board, in a few words. */
function connection(h: Hardware): string {
  if (!h.online) return 'Offline';
  const link = h.link ?? (isSerial(h) ? 'usb' : 'wifi');
  if (link === 'usb') return `USB${h.port ? ` · ${h.port}` : ''}`;
  return `Wi-Fi · ${h.url.replace(/^https?:\/\//, '')}`;
}

function firmware(h: Hardware): { text: string; old: boolean } {
  if (!h.online) return { text: '', old: false };
  if (h.fwOld) return { text: h.fw ? `firmware ${h.fw.split(' ')[0]} is out of date` : 'firmware older than build ids', old: true };
  if (h.fw && h.fwWant) return { text: 'firmware up to date', old: false };
  if (h.fw) return { text: `firmware ${h.fw}`, old: false };
  return { text: 'firmware: unknown', old: false };
}

/** What the board's light should be doing right now, so staff can check it at a glance. */
function ledNow(h: Hardware): string {
  if (!h.online) {
    return h.kind === 'sign'
      ? 'Its screen shows “USB” (no Wi-Fi, waiting for the laptop), three dots (still trying Wi-Fi) or a single heartbeat dot (on Wi-Fi, nobody talking to it).'
      : 'Its blue LED is solid (nobody is talking to it and no Wi-Fi) or slowly fading (on Wi-Fi, waiting for Pulse).';
  }
  if (h.kind === 'sign') {
    if (h.level === 'red') return 'Should be flashing an arrow, then STOP.';
    if (h.level === 'yellow') return 'Should show a steady “!”.';
    return 'Should double-flash two dots every 1.5 s: Pulse is talking to it.';
  }
  if (h.level === 'red') return 'Blue LED should strobe fast.';
  if (h.level === 'yellow') return 'Blue LED should blink slowly and evenly.';
  const n = h.peers?.length ? 2 : 1;
  return `Blue LED should blink ${n === 2 ? 'twice' : 'once'} every 2 s (3 times when a phone is linked): Pulse is talking to it.${
    n === 2 ? ` It hears ${h.peers!.map((p) => p.name).join(', ')}.` : ''
  } Solid instead = it isn’t getting Pulse’s messages.`;
}

/** The one thing to do about a board, or "" when it's ready. */
function nextStep(h: Hardware): string {
  if (!h.online) {
    if (isSerial(h)) {
      if (/busy/i.test(h.error ?? '')) return 'Close the program holding its USB port (Arduino IDE Serial Monitor, a flash script); Pulse retries every 2 s.';
      if (/no .* on USB/.test(h.error ?? '') && /: (sign|PULSE-)/.test(h.error ?? '')) return `Another board answered instead: ${h.error}`;
      return 'Plug it into this laptop with a data cable (a hub is fine). Pulse finds it within a few seconds.';
    }
    return 'Plug it into this laptop by USB and switch SIGN_URL to serial:auto (<code>scripts/boards.sh env --write</code>, restart Pulse), or put it on the same Wi-Fi as this laptop.';
  }
  if (h.fwOld) return 'Reflash: stop Pulse, run <code>scripts/boards.sh flash</code>, start Pulse.';
  return '';
}

function boardRow(h: Hardware): string {
  const fw = firmware(h);
  const next = nextStep(h);
  const state = !h.online ? 'bad' : next ? 'warn' : 'ok';
  const shows = h.online && h.level ? `<span class="muted small">shows ${esc(h.level)}</span>` : '';
  const wifi = h.online && h.wifi !== undefined ? (h.wifi ? `own Wi-Fi: ${esc(h.ssid ?? '')}` : 'own Wi-Fi: not joined') : '';
  const facts = [esc(connection(h)), fw.text ? esc(fw.text) : '', h.link === 'usb' ? wifi : ''].filter(Boolean).map((f) => `<span>${f}</span>`).join('');
  return (
    `<div class="hws-row"><span class="hws-dot ${state}"></span><div class="hws-main">` +
    `<div class="hws-top"><b>${esc(h.name)}</b>${shows}</div>` +
    `<div class="hws-facts">${facts}</div>` +
    `<div class="hws-led">${esc(ledNow(h))}</div>` +
    `<div class="hws-next">${next ? `<b>Next:</b> ${next}` : '<span class="ok-text">Ready</span>'}</div>` +
    `</div></div>`
  );
}

export function initHwSetup(ctx: HwSetupCtx) {
  const anchor = document.getElementById('hwCard');
  if (!anchor?.parentElement) return;
  const style = document.createElement('style');
  style.textContent = CSS;
  document.head.append(style);

  const table = document.createElement('div');
  table.className = 'card';
  table.id = 'hwsTable';
  table.innerHTML =
    `<div class="card-head"><h3>Table demo</h3></div>` +
    `<p class="muted small">Boards side by side next to this laptop? One click puts the sign, the zone lights and this laptop in a row on the map, lines up phones that join right beside them, and gives each zone light a zone to show, so a test alert lights the right one.</p>` +
    `<div class="hws-actions"><button class="primary" id="hwsSetUp">Set up table demo</button>` +
    `<button id="hwsTest" title="Shows red on every board for 1 s, then asks each what it shows">Test boards</button></div>` +
    `<div class="hws-out" id="hwsOut"></div>`;

  const ready = document.createElement('div');
  ready.className = 'card';
  ready.id = 'hwsReady';
  ready.innerHTML = `<div class="card-head"><h3>Board readiness</h3><span class="meta" id="hwsSum"></span></div><div id="hwsList"></div>`;

  anchor.parentElement.insertBefore(table, anchor);
  anchor.parentElement.insertBefore(ready, anchor);

  const out = table.querySelector<HTMLElement>('#hwsOut')!;
  const list = ready.querySelector<HTMLElement>('#hwsList')!;
  const sum = ready.querySelector<HTMLElement>('#hwsSum')!;

  const render = (hw: Hardware[]) => {
    const boards = hw.filter((h) => h.kind !== 'laptop');
    if (!boards.length) {
      sum.textContent = '';
      list.innerHTML =
        '<p class="muted small">No boards configured. Plug the sign and zone lights into this laptop, run <code>scripts/boards.sh env --write</code> (it writes <code>SIGN_URL=serial:auto,A=serial:auto,B=serial:auto</code>), then restart Pulse.</p>';
      return;
    }
    const ok = boards.filter((h) => h.online && !nextStep(h)).length;
    sum.textContent = `${ok} of ${boards.length} ready`;
    list.innerHTML = boards.map(boardRow).join('');
  };

  const load = async () => {
    try {
      const r = await fetch('/api/hardware');
      if (r.ok) render((await r.json()) as Hardware[]);
    } catch {
      /* server restarting: keep the last view */
    }
  };
  void load();
  setInterval(() => void load(), 5000);

  const setUp = table.querySelector<HTMLButtonElement>('#hwsSetUp')!;
  setUp.addEventListener('click', async () => {
    setUp.disabled = true;
    try {
      const res = await post<TableDemo>('/api/hardware/table');
      const lights = res.lights.map((l) => `<li>Zone light ${esc(l.key)} shows ${l.shows ? `<b>${esc(l.shows)}</b>` : '<i>nothing</i>'}</li>`).join('');
      const notes = (res.notes ?? []).map((n) => `<li class="muted">${esc(n)}</li>`).join('');
      out.innerHTML =
        `<ul><li>Boards and this laptop placed in a row; phones line up from ${res.demo.x.toFixed(1)} m, ${res.demo.y.toFixed(1)} m.</li>${lights}${notes}</ul>`;
      render(res.hardware);
      ctx.refresh();
      ctx.toast('Table demo set up: boards placed, demo spot on', 'ok');
    } catch (e) {
      ctx.toast(`Couldn't set up the table demo: ${(e as Error).message}`, 'error');
    } finally {
      setUp.disabled = false;
    }
  });

  const test = table.querySelector<HTMLButtonElement>('#hwsTest')!;
  test.addEventListener('click', async () => {
    test.disabled = true;
    out.innerHTML = '<span class="muted">Showing red on every board for a second…</span>';
    try {
      const res = await post<BoardTest[]>('/api/hardware/test');
      if (!res.length) {
        out.innerHTML = '<span class="muted">No boards configured.</span>';
        return;
      }
      out.innerHTML =
        '<ul>' +
        res
          .map((r) => {
            const via = r.link === 'usb' ? `USB${r.port ? ` ${esc(r.port)}` : ''}` : r.link === 'wifi' ? 'Wi-Fi' : '';
            if (!r.sent) return `<li><b>${esc(r.name)}</b>: not reached (${esc(r.error ?? 'no answer')})</li>`;
            if (!r.confirmed) return `<li><b>${esc(r.name)}</b>: took the command over ${via} but didn’t report it back${r.level ? ` (says ${esc(r.level)})` : ''}. Old firmware?</li>`;
            return `<li><b>${esc(r.name)}</b>: ✓ showed red and said so over ${via} (${r.ms} ms)${r.fwOld ? ' · firmware out of date' : ''}</li>`;
          })
          .join('') +
        '</ul>';
      const bad = res.filter((r) => !r.confirmed).length;
      ctx.toast(bad ? `${bad} board(s) didn’t confirm` : 'Every board showed red and confirmed it', bad ? 'watch' : 'ok');
      void load();
    } catch (e) {
      out.innerHTML = '';
      ctx.toast(`Board test failed: ${(e as Error).message}`, 'error');
    } finally {
      test.disabled = false;
    }
  });
}
