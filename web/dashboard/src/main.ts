import './style.css';
import { onPage, page } from './shell';
import type { Alert, AlertRules, Cluster, Config, DrillReady, DrillRecord, DrillStatus, DrillZone, EdgeExplain, EvalReport, FloorplanSuggestion, Hardware, Level, Node, NodeDetail, SimAction, SimActionSpec, SimScenario, SimState, Snapshot, ToDash, Venue } from '../../shared/protocol';
import { wsURL } from '../../shared/protocol';
import { animate } from 'motion';
import { Areas, inPoly, type Tool } from './areas';
import { initDemo } from './demo';
import { initMeshNet } from './meshnet';
import { Mesh, crushRGB } from './mesh';
import { CAUSE_LABEL, TableLayer } from './tablelayer';
import { Setup } from './setup';
import { escalateNow, initEscalation } from './escalation';
import { initJoinQR } from './joinqr';
import { initHwSetup } from './hwsetup';

const $ = <T extends HTMLElement = HTMLElement>(id: string) => document.getElementById(id) as T;

// ---------------------------------------------------------------------------
// state
// ---------------------------------------------------------------------------

let snap: Snapshot | null = null;
let thresholds = { yellow: 0.3, red: 0.6 };
const nodesById = new Map<string, Node>();
const riskHist: number[] = []; // worst zone score, 1 Hz, last 60 s
let lastHistAt = 0;
/** Timeline: server alerts plus watch-area escalations, oldest first. */
interface LogItem {
  t: number;
  level: Level;
  text: string;
  area?: string;
  test?: boolean;
  /** The alert this entry belongs to (for "Clear"). */
  id?: string;
  /** Raised by a simulation or a saved run, not the live crowd. */
  src?: 'sim' | 'replay';
}
let timeline: LogItem[] = [];
let soundOn = false;
let lastBrief: Alert | null = null;
/** Guided event setup; created at the end of the file, once everything it reads exists. */
let setup: Setup | null = null;
/** Re-check the setup steps (cheap; safe to call before setup exists). */
function refreshSetup() {
  setup?.refresh();
}

// ---------------------------------------------------------------------------
// mesh view
// ---------------------------------------------------------------------------

const mesh = new Mesh($('mesh') as HTMLCanvasElement);
(window as unknown as { pulseMap: Mesh }).pulseMap = mesh; // for headless checks (web/phone/e2e/move.e2e.mjs)
const table = new TableLayer(mesh, $('mesh').parentElement!);
mesh.table = table;

setInterval(() => {
  const s = mesh.stats();
  $('mLinks').textContent = String(s.links);
  $('mHops').textContent = String(s.hops);
}, 250);

// ---------------------------------------------------------------------------
// watch areas: drawn by staff, stored on the server, levels from the detector
// ---------------------------------------------------------------------------

const areas = new Areas($('mesh') as HTMLCanvasElement, mesh);
const toolBtns = [...document.querySelectorAll<HTMLButtonElement>('.toolbar [data-tool]')];
const hints: Record<Tool, string> = {
  select: '',
  rect: 'Drag to draw a rectangle · Esc to cancel',
  circle: 'Drag out from the centre to draw a circle · Esc to cancel',
  free: 'Hold and draw around the area · Esc to cancel',
};

for (const b of toolBtns) b.addEventListener('click', () => areas.setTool(b.dataset.tool as Tool));
$('addArea').addEventListener('click', () => areas.setTool('rect'));

areas.onTool = (t) => {
  for (const b of toolBtns) b.classList.toggle('on', b.dataset.tool === t);
  const hint = $('hint');
  hint.textContent = hints[t];
  if (t !== 'select') animate(hint, { opacity: [0, 1], y: [-6, 0] }, { type: 'spring', bounce: 0.35, duration: 0.4 });
  else animate(hint, { opacity: 0 }, { duration: 0.15 });
};

const areaEls = new Map<string, HTMLLIElement>();
const areaLevelText = { calm: 'Calm', watch: 'Pressure building', danger: 'Danger' } as const;

/** Rules that actually change something (the form always fills in push + notify). */
function rulesCount(r?: AlertRules): number {
  if (!r) return 0;
  const n = r.notify ?? {};
  return [
    !!r.density,
    !!r.maxPhones,
    r.push === false,
    !!r.message,
    n.sign === false || n.light === false || n.voice === false,
  ].filter(Boolean).length;
}
/** An area's rules were set by staff (any non-empty rules object). */
const hasRules = (r?: AlertRules) => !!r && Object.values(r).some((v) => v !== undefined && v !== null);

/** Open (or toggle) the "Alert when…" box of one area. */
function toggleRules(id: string, open?: boolean) {
  const li = areaEls.get(id);
  if (!li) return;
  const box = li.querySelector<HTMLElement>('.rules')!;
  const show = open ?? box.hidden;
  box.hidden = !show;
  li.querySelector('.rules-toggle')!.setAttribute('aria-expanded', String(show));
  li.classList.toggle('rules-open', show);
  if (show && !box.childElementCount) buildRules(box, id);
  if (show) animate(box, { opacity: [0, 1], y: [-4, 0] }, { duration: 0.2 });
}

areas.onChange = () => {
  const ul = $('areas');
  $('areasEmpty').hidden = areas.list.length > 0;
  const live = new Set(areas.list.map((a) => a.id));
  for (const [id, li] of areaEls) {
    if (live.has(id)) continue;
    areaEls.delete(id);
    animate(li, { opacity: 0, x: 24 }, { duration: 0.2 }).then(() => li.remove());
  }
  for (const a of areas.list) {
    let li = areaEls.get(a.id);
    if (!li) {
      li = document.createElement('li');
      // Two lines that fit any rail width: name + delete, then live status,
      // then the controls; rules fold out below.
      li.innerHTML =
        `<div class="a-top"><span class="swatch"></span>` +
        `<input class="name" maxlength="40" aria-label="Area name" />` +
        `<button class="del icon-btn sm-icon" data-tip="Delete area" aria-label="Delete area">✕</button></div>` +
        `<div class="a-status"><span class="lvl"></span><span class="a-live"></span></div>` +
        `<div class="a-controls">` +
        `<div class="a-field"><span>Priority</span><div class="seg2 sens" role="radiogroup" aria-label="Priority">` +
        `<button type="button" role="radio" data-sens="normal" data-tip="Standard thresholds, for open floor">Standard</button>` +
        `<button type="button" role="radio" data-sens="high" data-tip="Lower thresholds: alerts sooner at spots where trouble starts fast">High risk (alerts sooner)</button>` +
        `</div></div>` +
        `<label class="a-field light-field"><span>Zone light</span><select class="light" aria-label="Zone light for this area"></select></label>` +
        `</div>` +
        `<button class="rules-toggle sm" aria-expanded="false"><span class="rt-label">Alert when…</span><span class="rt-sum"></span><span class="rt-chev">▾</span></button>` +
        `<div class="rules" hidden></div>`;
      const id = a.id;
      const name = li.querySelector<HTMLInputElement>('.name')!;
      name.value = a.name;
      name.addEventListener('change', () => areas.rename(id, name.value));
      name.addEventListener('keydown', (e) => e.key === 'Enter' && name.blur());
      name.addEventListener('focus', () => areas.selected !== id && areas.select(id));
      for (const b of li.querySelectorAll<HTMLButtonElement>('[data-sens]')) {
        b.addEventListener('click', () => areas.setSens(id, b.dataset.sens as 'normal' | 'high'));
      }
      li.querySelector<HTMLSelectElement>('.light')!.addEventListener('change', (e) =>
        areas.setLight(id, (e.target as HTMLSelectElement).value),
      );
      li.querySelector('.del')!.addEventListener('click', () => areas.remove(id));
      li.querySelector('.rules-toggle')!.addEventListener('click', (e) => {
        e.stopPropagation();
        toggleRules(id);
      });
      li.addEventListener('click', (e) => {
        if (!(e.target as HTMLElement).closest('button, input, select, label, .rules')) areas.select(id);
      });
      ul.append(li);
      areaEls.set(a.id, li);
      animate(li, { opacity: [0, 1], y: [12, 0], scale: [0.96, 1] }, { type: 'spring', bounce: 0.4, duration: 0.5 });
    }
    const prev = li.dataset.level;
    li.className = `area ${a.level}${areas.selected === a.id ? ' sel' : ''}${li.querySelector<HTMLElement>('.rules')!.hidden ? '' : ' rules-open'}`;
    li.dataset.level = a.level;
    li.querySelector<HTMLElement>('.swatch')!.style.background = a.color;
    const name = li.querySelector<HTMLInputElement>('.name')!;
    if (document.activeElement !== name && name.value !== a.name) name.value = a.name;
    name.title = a.name;
    li.querySelector('.lvl')!.textContent = areaLevelText[a.level];
    const liveEl = li.querySelector<HTMLElement>('.a-live')!;
    liveEl.textContent =
      (a.phones === 0 ? 'nobody inside yet' : `${a.phones} ${a.phones === 1 ? 'person' : 'people'} inside`) +
      (a.wave ? ` · ${a.wave} in a push` : '') +
      (a.sway ? ` · ${a.sway} swaying` : '');
    liveEl.title = 'Counted from phones running Pulse inside this area';
    for (const b of li.querySelectorAll<HTMLButtonElement>('[data-sens]')) {
      const on = b.dataset.sens === a.sens;
      b.classList.toggle('on', on);
      b.setAttribute('aria-checked', String(on));
    }
    fillLightPicker(li.querySelector<HTMLSelectElement>('.light')!, a.light ?? '');
    li.querySelector<HTMLElement>('.light-field')!.hidden = lightKeys.length === 0 && !a.light;
    const n = rulesCount(a.rules);
    li.querySelector('.rt-sum')!.textContent = n ? `${n} set` : 'standard alerts';
    if (prev && prev !== a.level) animate(li, { scale: [1.04, 1] }, { type: 'spring', bounce: 0.5, duration: 0.5 });
  }
  refreshSetup();
};
areas.onChange();
areas.onError = (m) => toast(m, 'error');

// A freshly drawn area: put the cursor in its name so typing names it straight away.
// The pointerup that finished the shape can still move focus, so retry until it sticks.
areas.onCreated = (a) => {
  const focusName = (tries: number) => {
    const name = areaEls.get(a.id)?.querySelector<HTMLInputElement>('.name');
    if (!name) return;
    if (document.activeElement !== name) {
      name.focus({ preventScroll: true });
      name.select();
      name.closest('li')?.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
    }
    if (tries > 0) window.setTimeout(() => document.activeElement !== name && focusName(tries - 1), 80);
  };
  // Straight away (the row exists: onChange ran first), then re-check on timers;
  // not requestAnimationFrame, which never fires while the window is in the background.
  focusName(4);
};

// The server raises the alert (timeline, briefing, voice, sign); this is the
// operator's heads-up toast for their own area.
areas.onEscalate = (a) => {
  const danger = a.level === 'danger';
  const text = danger
    ? `${a.name} is in danger${a.wave ? `: ${a.wave} of ${a.phones} phones caught in a push` : ''}.`
    : `${a.name}: pressure building${a.phones ? ` (${a.wave + a.sway} of ${a.phones} phones moving)` : ''}.`;
  toast(text, danger ? 'danger' : 'watch');
  if (danger) {
    beep();
    if (soundOn) speak(`${a.name}. Crowd push detected.`);
  }
};

// ---------------------------------------------------------------------------
// header status: one sentence anyone can read from across the room
// ---------------------------------------------------------------------------

/**
 * What is happening, worked out once per snapshot. The sidebar status, the
 * risk panel, the KPIs and Home all read this, so they can never disagree.
 */
interface Situation {
  cls: Level;
  /** "Stage front", "Stage front +1", or "" when crowd-wide / calm. */
  place: string;
  /** "crowd push" | "crowding" | "over the limit" | "" */
  kind: string;
  /** The one sentence for the sidebar. */
  text: string;
  /** For the risk panel. */
  detail: string;
}
let situation: Situation = { cls: 'calm', place: '', kind: '', text: 'Waiting for attendees', detail: 'No pushes travelling through the crowd.' };

const kindText = (k?: Alert['kind']) => (k === 'density' ? 'crowding' : k === 'rule' ? 'over the limit' : 'crowd push');
/** Headline of a table demo alert with no briefing (yellow ones get none). */
const causeHead = (c: NonNullable<Alert['cause']>, where: string) =>
  c === 'pair' ? `${where}: push between two people` : `${where}: moving as one (people pressed together)`;

/** Name of the area or zone a point lies in (areas first: they are what staff drew). */
function placeAt(s: Snapshot, x: number, y: number): string {
  const a = [...areas.list].reverse().find((a) => inPoly(a.poly, x, y));
  if (a) return a.name;
  const z = s.zones.find((z) => z.poly?.length >= 3 && inPoly(z.poly, x, y));
  return z?.name ?? '';
}

function computeSituation(s: Snapshot): Situation {
  // The server's one overall status, when it sends it (newer servers).
  const st = s.status;
  if (st) {
    const waves = s.waves.length;
    if (st.level === 'calm') {
      return {
        cls: 'calm', place: '', kind: '',
        text: s.stats.phones === 0 ? 'Waiting for attendees' : 'All clear',
        detail: waves
          ? `Small movements between ${waves} pair${waves === 1 ? '' : 's'} of neighbours; not enough to raise an alert.`
          : 'No pushes travelling through the crowd.',
      };
    }
    const place = st.where || (st.zone ? (areas.get(st.zone)?.name ?? zoneNames.get(st.zone) ?? '') : '');
    // Table demo: a yellow "push" may be a push between just two people, or people moving as one.
    const cause = (st.kind ?? 'wave') === 'wave' && st.level !== 'red' ? table.cause(s, st.zone, alertsById.values()) : null;
    const kind = cause ? CAUSE_LABEL[cause].toLowerCase() : st.kind === 'density' ? 'crowding' : st.kind === 'rule' ? 'over the limit' : st.kind === 'early' ? 'crowding fast' : 'crowd push';
    const where = place ? ` in ${place}` : ' in the crowd';
    const dens = st.density != null ? ` (about ${st.density.toFixed(1)} people/m²)` : '';
    const detail = cause === 'pair'
      ? `A push passed from one person to the next${where}. With only two phones it can't be a wave through a crowd, so it stays yellow.`
      : cause === 'together'
        ? `People${where} are moving as one, the way people pressed together are (or rocking together by choice). Shown as yellow, never red.`
        : kind === 'crowd push'
        ? `${st.level === 'red' ? 'A crowd push is travelling' : 'Pressure is building'}${where}${waves ? `, passing between ${waves} pair${waves === 1 ? '' : 's'} of neighbours` : ''}.`
        : kind === 'over the limit'
          ? `An alert rule was crossed${where}${dens}.`
          : kind === 'crowding fast'
            ? `Getting crowded fast${where}${dens}: dangerous soon at this rate.`
            : `${st.level === 'red' ? 'Dangerously crowded' : 'Getting crowded'}${where}${dens}.`;
    return { cls: st.level, place, kind, text: [st.level === 'red' ? 'DANGER' : 'WARNING', place || 'whole crowd', kind].join(' · '), detail };
  }
  // Older server: work it out from zones and clusters.
  const clusters = s.clusters ?? [];
  const openAlerts = [...alertsById.values()].filter((a) => a.status !== 'resolved' && a.level !== 'calm' && !a.test);
  type Hit = { level: Level; place: string; kind: string };
  const hits: Hit[] = [];
  for (const z of s.zones) {
    if (z.level === 'calm') continue;
    const area = areas.get(z.id);
    const al = openAlerts.filter((a) => a.zone === z.id).sort((x, y) => y.t - x.t)[0];
    const kind = al ? kindText(al.kind) : area && area.wave === 0 && rulesCount(area.rules) ? 'over the limit' : 'crowd push';
    hits.push({ level: z.level, place: area?.name ?? z.name, kind });
  }
  for (const c of clusters) {
    if (c.level !== 'red' && c.level !== 'yellow') continue;
    hits.push({ level: c.level, place: placeAt(s, c.x, c.y), kind: 'crowding' });
  }
  const worst: Level = hits.some((h) => h.level === 'red') ? 'red' : hits.length ? 'yellow' : 'calm';
  if (worst === 'calm') {
    const waves = s.waves.length;
    return {
      cls: 'calm', place: '', kind: '',
      text: s.stats.phones === 0 ? 'Waiting for attendees' : 'All clear',
      detail: waves
        ? `Small movements between ${waves} pair${waves === 1 ? '' : 's'} of neighbours; not enough to raise an alert.`
        : 'No pushes travelling through the crowd.',
    };
  }
  const top = hits.filter((h) => h.level === worst);
  // A push outranks crowding in the headline; name the places.
  const lead = top.find((h) => h.kind === 'crowd push') ?? top[0];
  const places = [...new Set(top.map((h) => h.place).filter(Boolean))];
  const place = places.length ? places[0] + (places.length > 1 ? ` +${places.length - 1}` : '') : '';
  const word = worst === 'red' ? 'DANGER' : 'WARNING';
  const text = [word, place || 'whole crowd', lead.kind].join(' · ');
  const where = place ? ` in ${place}` : ' in the crowd';
  const detail =
    lead.kind === 'crowd push'
      ? `${worst === 'red' ? 'A crowd push is travelling' : 'Pressure is building'}${where}${s.waves.length ? `, passing between ${s.waves.length} pair${s.waves.length === 1 ? '' : 's'} of neighbours` : ''}.`
      : lead.kind === 'crowding'
        ? `${worst === 'red' ? 'Dangerously crowded' : 'Getting crowded'}${where}.`
        : `An alert rule was crossed${where}.`;
  return { cls: worst, place, kind: lead.kind, text, detail };
}

function renderStatus(clusters: Cluster[]) {
  const sit = situation;
  const cls = sit.cls;
  const text = sit.text;
  renderAlertSummary();
  const packed = clusters.filter((c) => c.level === 'red');
  // est = people/m² at the densest spot (what alerts use); density on older servers.
  const densest = clusters.reduce((m, c) => Math.max(m, c.est ?? c.density), 0);
  const top = clusters.reduce<Cluster | null>((m, c) => ((c.est ?? c.density) >= (m ? (m.est ?? m.density) : -1) ? c : m), null);
  const topPlace = top && snap ? placeAt(snap, top.x, top.y) : '';
  $('kDense').textContent = clusters.length ? densest.toFixed(1) : '–';
  $('kDenseSub').textContent = clusters.length
    ? `people/m²${topPlace ? ` · ${topPlace}` : ''} · ${clusters.length} crowd${clusters.length === 1 ? '' : 's'}${clusters.some((c) => c.trend === 'forming') ? ', one forming' : ''}`
    : 'no crowds packed together';
  $('kDense').parentElement!.classList.toggle('hot', packed.length > 0);
  const el = $('status');
  const statusCls = cls === 'calm' ? 'calm' : cls;
  if (!el.classList.contains(statusCls)) {
    el.className = `status ${statusCls}`;
    animate(el, { scale: [1.08, 1] }, { type: 'spring', bounce: 0.5, duration: 0.6 });
  }
  $('statusText').textContent = text;
  el.title = text;
}

/** Active alerts KPI and its summary line: open alerts, so ack/resolve shows straight away. */
function renderAlertSummary() {
  const open = [...alertsById.values()].filter((a) => a.status !== 'resolved' && a.level !== 'calm');
  const real = open.filter((a) => !a.test);
  const unacked = real.filter((a) => a.status !== 'ack').length;
  const drills = open.length - real.length;
  // Something is wrong but no alert has arrived yet: still count it.
  const active = real.length || (situation.cls !== 'calm' ? 1 : 0);
  $('kAlerts').textContent = String(active);
  $('kAlerts').parentElement!.classList.toggle('hot', active > 0);
  $('kAlertsSub').textContent = real.length
    ? unacked
      ? `${unacked} need${unacked === 1 ? 's' : ''} acknowledging`
      : 'all acknowledged'
    : situation.cls !== 'calm'
      ? `${situation.place || 'crowd-wide'} · ${situation.kind}`
      : drills
        ? `${drills} drill${drills === 1 ? '' : 's'} open, nothing real`
        : 'nothing needs attention';
}

// ---------------------------------------------------------------------------
// toasts
// ---------------------------------------------------------------------------

function toast(text: string, kind: 'info' | 'ok' | 'watch' | 'danger' | 'error' = 'info') {
  const el = document.createElement('div');
  el.className = `toast ${kind}`;
  if (kind === 'ok') {
    // A tick that draws itself: the "step done" confirmation.
    el.innerHTML = `<svg class="toast-tick" viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="10" /><path d="M7 12.5l3.2 3.2L17 9" /></svg><span></span>`;
    el.querySelector('span')!.textContent = text;
    const path = el.querySelector('path')!;
    animate(path, { pathLength: [0, 1] }, { duration: 0.45, delay: 0.15, ease: 'easeOut' });
  } else {
    el.textContent = text;
  }
  $('toasts').append(el);
  animate(el, { opacity: [0, 1], y: [20, 0], scale: [0.95, 1] }, { type: 'spring', bounce: 0.35, duration: 0.5 });
  setTimeout(() => {
    animate(el, { opacity: 0, y: 10 }, { duration: 0.25 }).then(() => el.remove());
  }, kind === 'danger' ? 6000 : 3500);
}

// ---------------------------------------------------------------------------
// tooltip
// ---------------------------------------------------------------------------

$('mesh').addEventListener('mousemove', (e) => {
  const r = $('mesh').getBoundingClientRect();
  mesh.hover = mesh.pick(e.clientX - r.left, e.clientY - r.top);
  renderTooltip();
});
$('mesh').addEventListener('mouseleave', () => {
  mesh.hover = null;
  renderTooltip();
});

function renderTooltip() {
  const tip = $('tooltip');
  const id = mesh.hover;
  const n = id && !areas.drawing && id !== drawerId ? nodesById.get(id) : undefined;
  const p = id ? mesh.position(id) : null;
  if (!n || !p) {
    tip.hidden = true;
    return;
  }
  tip.innerHTML =
    `<div class="tt-head">${n.name ? whoHTML(n.id) : `<b>${esc(n.id.slice(0, 8))}</b>`}<span class="st ${n.status}">${statusText[n.status]}</span></div>` +
    (n.press ? `<div class="tt-packed"><span class="st packed-${n.press}">${packedText(n)}</span></div>` : '') +
    `<div class="tt-ua">${esc(deviceName(n.ua))} · ${where(n)}${n.real ? ' · real phone in the simulated crowd' : ''}</div>` +
    (meshNet.tip(n.id) ? `<div class="tt-ua tt-mesh">${esc(meshNet.tip(n.id))}</div>` : '') +
    `<div class="tt-foot">${n.name && lastMode !== 'replay' ? 'Click for details · drag to move' : 'Click for details'}</div>`;
  tip.hidden = false;
  const w = $('mesh').clientWidth;
  tip.style.left = `${Math.min(p.x + 22, w - 230)}px`;
  tip.style.top = `${Math.max(8, p.y - 20)}px`;
}

/** "Packed in: about 5 people per m²" for a phone at or past the watch density, else "". */
function packedText(n?: Node): string {
  if (!n?.press || n.dens == null) return '';
  return `${n.press === 'red' ? 'Dangerously packed' : 'Getting tight'}: about ${n.dens.toFixed(n.dens < 10 ? 1 : 0)} people per m²`;
}

/** The dot's fill as a CSS colour: where this phone is on the crush ramp. */
function crushCss(n?: Node): string {
  const theme = document.documentElement.dataset.theme === 'light' ? 'light' : 'dark';
  const c = crushRGB(n?.crush ?? 0, theme);
  return `rgb(${c[0] | 0},${c[1] | 0},${c[2] | 0})`;
}

// Motion status. "ok" is a phone that is streaming and not moving much: "Still", not "Calm":
// a still phone can be in the middle of a crush (see packedText).
const statusText: Record<Node['status'], string> = {
  ok: 'Still',
  handling: 'In hand',
  swaying: 'Swaying',
  wave: 'In a push',
  connecting: 'Connecting',
  stale: 'Offline',
};

/** What staff call a phone: its generated name ("Blue Otter"), else the start of its random id. */
function who(id: string): string {
  return nodesById.get(id)?.name ?? id.slice(0, 6);
}

/** The name with a dot in the phone's colour. */
function whoHTML(id: string): string {
  const n = nodesById.get(id);
  const c = n?.color && /^#[0-9a-f]{3,8}$/i.test(n.color) ? n.color : '';
  return `<b class="who"${c ? ` style="--c:${c}"` : ''}>${c ? '<i></i>' : ''}${esc(who(id))}</b>`;
}

/** How the phone's position is known. */
function where(n: Node) {
  if (n.outside) return 'outside the venue';
  return n.src === 'gps' || (n.acc ?? 0) > 0 ? `GPS ±${Math.round(n.acc ?? 0)} m` : n.src === 'tower' ? 'checked in at a tower' : n.src === 'beacon' ? 'Bluetooth beacons' : 'placed on map';
}

/** Zone ids → names, from the latest snapshot (custom areas have random ids). */
const zoneNames = new Map<string, string>();

/** "iPhone · Safari" from a user agent string. */
function deviceName(ua?: string): string {
  if (!ua) return 'Unknown device';
  if (/^sim/i.test(ua)) return 'Simulated phone';
  const os = /iPhone/.test(ua) ? 'iPhone' : /iPad/.test(ua) ? 'iPad' : /Android/.test(ua) ? 'Android' : /Mac OS X/.test(ua) ? 'Mac' : /Windows/.test(ua) ? 'Windows' : '';
  const br = /CriOS|Chrome/.test(ua) ? 'Chrome' : /FxiOS|Firefox/.test(ua) ? 'Firefox' : /Safari/.test(ua) ? 'Safari' : '';
  return [os, br].filter(Boolean).join(' · ') || ua.slice(0, 40);
}

// ---------------------------------------------------------------------------
// attendee drawer: click a phone to see its device and live telemetry
// ---------------------------------------------------------------------------

let drawerId: string | null = null;
let drawerTimer = 0;

areas.onNode = (id) => openDrawer(id);
$('dClose').addEventListener('click', () => openDrawer(null));
window.addEventListener('keydown', (e) => e.key === 'Escape' && drawerId && openDrawer(null));

function openDrawer(id: string | null) {
  const el = $('drawer');
  window.clearInterval(drawerTimer);
  drawerId = id;
  mesh.selected = id;
  if (!id) {
    if (!el.hidden) animate(el, { opacity: 0, x: 40 }, { duration: 0.18 }).then(() => (el.hidden = drawerId !== null ? el.hidden : true));
    return;
  }
  const wasHidden = el.hidden;
  el.hidden = false;
  if (wasHidden) animate(el, { opacity: [0, 1], x: [40, 0] }, { type: 'spring', bounce: 0.25, duration: 0.45 });
  void refreshDrawer();
  drawerTimer = window.setInterval(() => void refreshDrawer(), 500);
}

async function refreshDrawer() {
  const id = drawerId;
  if (!id) return;
  const n = nodesById.get(id);
  let d: NodeDetail | null = null;
  try {
    const r = await fetch(`/api/node/${encodeURIComponent(id)}`);
    if (r.ok) d = (await r.json()) as NodeDetail;
  } catch {
    /* server busy: keep last values */
  }
  if (drawerId !== id) return;
  const color = getComputedStyle(document.documentElement).getPropertyValue(`--${n?.status ?? 'stale'}`).trim();
  // Like its dot on the map: the fill is how packed in it is, the ring its motion status.
  $('dAvatar').style.background = n && n.status !== 'stale' ? crushCss(n) : color;
  $('dAvatar').style.boxShadow = `0 0 0 3px ${n?.color ?? color}`;
  const dens = n?.dens;
  const crush = n?.crush ?? 0;
  $('dCrushBar').style.width = `${Math.round(crush * 100)}%`;
  $('dCrushBar').style.background = crushCss(n);
  $('dCrush').textContent =
    dens == null
      ? 'not counted (offline, outside or not located)'
      : `Packed in: about ${dens.toFixed(1)} people per m² · ${n?.press === 'red' ? 'dangerous' : n?.press === 'yellow' ? 'tight' : 'room to move'}`;
  $('dName').textContent = n?.name ?? 'Attendee';
  $('dId').textContent = id.slice(0, 8);
  $('dDevice').textContent = deviceName(d?.ua ?? n?.ua);
  const st = $('dStatus');
  st.className = `st ${n?.status ?? 'stale'}`;
  st.textContent = statusText[n?.status ?? 'stale'];
  $('dExplain').textContent = explain(n);
  const sway = n?.sway ?? 0;
  $('dSwayBar').style.width = `${Math.min(100, (sway / 1) * 100)}%`;
  $('dSwayBar').className = `meter-fill ${sway >= 0.25 ? 'over' : ''}`;
  $('dSway').textContent = `${sway.toFixed(2)} m/s²`;
  $('dSpot').textContent = n ? `${n.x.toFixed(1)} m, ${n.y.toFixed(1)} m` : '–';
  $('dSrc').textContent = n ? where(n) : '–';
  $('dZone').textContent = zoneNames.get(d?.zone ?? '') ?? d?.zone ?? '–';
  $('dJoined').textContent = d?.joinedAt ? `${fmtTime(d.joinedAt)} (${ago(Date.now() - d.joinedAt)})` : '–';
  $('dMsgs').textContent = d ? d.messages.toLocaleString() : '–';
  $('dRtt').textContent = n ? `${n.rtt} ms` : '–';
  $('dOffset').textContent = n ? `${n.offset >= 0 ? '+' : ''}${n.offset} ms` : '–';
  $('dAge').textContent = n ? `${(n.age / 1000).toFixed(1)} s ago` : '–';
  $('dNeigh').textContent = String(mesh.neighbourCount(id));
  meshNet.renderDrawer();
  const s = d?.samples ?? [];
  chart('chX', s.map((p) => p.ax), color, 'vX', 'm/s²');
  chart('chZ', s.map((p) => p.az), color, 'vZ', 'm/s²');
  chart('chY', s.map((p) => p.ay), color, 'vY', 'm/s²');
  chart('chR', s.map((p) => p.rot), color, 'vR', '°/s');
}

function explain(n?: Node): string {
  const packed = n?.press
    ? `${packedText(n)} around this phone${n.press === 'red' ? ', past the danger level' : ''}. `
    : '';
  if (packed && n?.status === 'ok') return `${packed}It is hardly moving: in a packed crowd that is not a good sign, people may have no room to move.`;
  return packed + explainMotion(n);
}

function explainMotion(n?: Node): string {
  switch (n?.status) {
    case 'ok': return 'Streaming normally. Movement is within everyday levels.';
    case 'handling': return 'The phone is being handled (turned or picked up), so its readings are ignored until it settles.';
    case 'swaying': return 'Sustained side-to-side sway, above the alert threshold. On its own this is not an incident.';
    case 'wave': return 'This person is moving in step with a push that is travelling from neighbour to neighbour.';
    case 'connecting': return 'Joined; syncing clocks with the server before readings count.';
    default: return 'No readings for over 2 seconds. The phone may be locked or out of signal.';
  }
}

function ago(ms: number) {
  const s = Math.round(ms / 1000);
  return s < 60 ? `${s}s ago` : `${Math.round(s / 60)} min ago`;
}

/** Small line chart of the last 30 s, symmetric around zero (rotation from zero). */
function chart(id: string, vals: number[], color: string, valId: string, unit: string) {
  const c = $(id) as HTMLCanvasElement;
  const dpr = Math.min(2, window.devicePixelRatio || 1);
  const w = c.clientWidth, h = c.clientHeight;
  if (c.width !== Math.round(w * dpr)) {
    c.width = Math.round(w * dpr);
    c.height = Math.round(h * dpr);
  }
  const g = c.getContext('2d')!;
  g.setTransform(dpr, 0, 0, dpr, 0, 0);
  g.clearRect(0, 0, w, h);
  const css = getComputedStyle(document.documentElement);
  g.strokeStyle = css.getPropertyValue('--line').trim();
  g.lineWidth = 1;
  const signed = unit !== '°/s';
  const zeroY = signed ? h / 2 : h - 1;
  g.beginPath();
  g.moveTo(0, zeroY);
  g.lineTo(w, zeroY);
  g.stroke();
  $(valId).textContent = vals.length ? `${vals[vals.length - 1].toFixed(unit === '°/s' ? 0 : 2)} ${unit}` : '–';
  if (vals.length < 2) return;
  const span = Math.max(signed ? 0.5 : 30, ...vals.map(Math.abs));
  g.strokeStyle = color;
  g.lineWidth = 1.5;
  g.beginPath();
  const n = 300;
  vals.forEach((v, i) => {
    const x = ((i + n - vals.length) / (n - 1)) * w;
    const y = signed ? h / 2 - (v / span) * (h / 2 - 2) : h - 1 - (v / span) * (h - 3);
    if (i) g.lineTo(x, y);
    else g.moveTo(x, y);
  });
  g.stroke();
}

// ---------------------------------------------------------------------------
// zoom
// ---------------------------------------------------------------------------

function zoomBy(f: number) {
  const c = $('mesh');
  mesh.zoomAt(c.clientWidth / 2, c.clientHeight / 2, f);
}
$('zoomIn').addEventListener('click', () => zoomBy(1.25));
$('zoomOut').addEventListener('click', () => zoomBy(0.8));
$('zoomFit').addEventListener('click', () => mesh.resetView());
setInterval(() => ($('zoomPct').textContent = `${Math.round(mesh.view.k * 100)}%`), 150);

// ---------------------------------------------------------------------------
// header: theme, clock, event name
// ---------------------------------------------------------------------------

function applyTheme(t: 'dark' | 'light') {
  document.documentElement.dataset.theme = t;
  mesh.setTheme(t);
  $('setTheme').textContent = t === 'light' ? 'Switch to dark' : 'Switch to light';
  try {
    localStorage.setItem('pulse.theme', t);
  } catch {
    /* fine */
  }
}
applyTheme(document.documentElement.dataset.theme === 'light' ? 'light' : 'dark');
const flipTheme = () => applyTheme(document.documentElement.dataset.theme === 'light' ? 'dark' : 'light');
$('themeBtn').addEventListener('click', flipTheme);
$('setTheme').addEventListener('click', flipTheme);

const tickClock = () => ($('clock').textContent = new Date().toLocaleTimeString([], { hour12: false }));
tickClock();
setInterval(tickClock, 1000);

const eventInput = $('eventName') as HTMLInputElement;
try {
  eventInput.value = localStorage.getItem('pulse.event') ?? eventInput.value;
} catch {
  /* fine */
}
eventInput.addEventListener('change', () => {
  try {
    localStorage.setItem('pulse.event', eventInput.value);
  } catch {
    /* fine */
  }
});
eventInput.addEventListener('keydown', (e) => e.key === 'Enter' && eventInput.blur());

function esc(s: string): string {
  return s.replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);
}

// ---------------------------------------------------------------------------
// crowd risk: the worst zone, shown as one number for the whole crowd
// ---------------------------------------------------------------------------

const ARC = 2 * Math.PI * 50;
const levelRank: Record<Level, number> = { calm: 0, yellow: 1, red: 2 };
const levelText: Record<Level, string> = { calm: 'Calm', yellow: 'Building', red: 'Danger' };
let shownScore = 0;
let targetScore = 0;

function crowdRisk(s: Snapshot): { level: Level; score: number } {
  let level: Level = 'calm';
  let score = 0;
  for (const z of s.zones) {
    score = Math.max(score, z.score);
    if (levelRank[z.level] > levelRank[level]) level = z.level;
  }
  // Packed clusters count too: density alerts work alongside push detection.
  for (const c of s.clusters ?? []) {
    if (c.level && levelRank[c.level] > levelRank[level]) level = c.level;
    if (c.level === 'red') score = Math.max(score, 0.8);
    else if (c.level === 'yellow') score = Math.max(score, 0.45);
  }
  return { level, score };
}

function renderRisk(level: Level, score: number) {
  // One number, one level, one sentence: the tile and the panel show the same
  // value (the gauge arc eases, the digits don't), and the sentence comes from
  // the same situation as the sidebar.
  if (levelRank[situation.cls] > levelRank[level]) level = situation.cls;
  const n = Math.round(score * 100);
  targetScore = score;
  $('riskPanel').className = `card risk ${level}`;
  $('kRisk').textContent = String(n);
  $('riskNum').textContent = String(n);
  // The thresholds give the number its meaning ("28 of 100" alone says nothing).
  $('kRiskSub').textContent =
    level === 'calm'
      ? `of 100 · calm · warning from ${Math.round(thresholds.yellow * 100)}, danger from ${Math.round(thresholds.red * 100)}`
      : `of 100 · ${levelText[level].toLowerCase()}`;
  $('riskLevel').textContent = levelText[level];
  $('riskSub').textContent = situation.detail;
  const pts = riskHist.map((v, i) => `${i + 60 - riskHist.length - 1},${1 - Math.min(1, Math.max(0, v))}`);
  $('riskLine').setAttribute('points', pts.join(' '));
  if (pts.length) {
    const x0 = 60 - riskHist.length - 1;
    $('riskArea').setAttribute('points', `${x0},1 ${pts.join(' ')} 58,1`);
  }
}

function setThresholdLines() {
  for (const [id, v] of [['thY', thresholds.yellow], ['thR', thresholds.red]] as const) {
    $(id).setAttribute('y1', String(1 - v));
    $(id).setAttribute('y2', String(1 - v));
  }
}

// Gauge and number ease toward the latest score.
function tickGauge() {
  shownScore += (targetScore - shownScore) * 0.12;
  $('gaugeArc').setAttribute('stroke-dasharray', `${ARC * Math.min(1, shownScore)} ${ARC}`);
  requestAnimationFrame(tickGauge);
}
requestAnimationFrame(tickGauge);
setThresholdLines();

// Header counters roll toward their new values.
const counterVals = new Map<string, number>();
function setCounter(id: string, v: number | null) {
  const el = $(id);
  if (v === null) {
    el.textContent = '–';
    counterVals.delete(id);
    return;
  }
  const from = counterVals.get(id) ?? v;
  counterVals.set(id, v);
  if (from === v) {
    el.textContent = String(v);
    return;
  }
  const t0 = performance.now();
  const step = (now: number) => {
    if (counterVals.get(id) !== v) return;
    const f = Math.min(1, (now - t0) / 300);
    el.textContent = String(Math.round(from + (v - from) * f));
    if (f < 1) requestAnimationFrame(step);
  };
  requestAnimationFrame(step);
  if (Math.abs(v - from) > Math.max(1, from * 0.1)) {
    el.classList.remove('bump');
    void el.offsetWidth;
    el.classList.add('bump');
  }
}

// ---------------------------------------------------------------------------
// side panel
// ---------------------------------------------------------------------------

function fmtTime(t: number) {
  return new Date(t).toLocaleTimeString([], { hour12: false });
}

function logEntry(e: LogItem, fresh = true) {
  timeline.push(e);
  if (timeline.length > 200) timeline.shift();
  renderLog(fresh);
}

/** Zone-light letters reported by the server (A, B, …), for the area light pickers. */
let lightKeys: string[] = [];

function fillLightPicker(sel: HTMLSelectElement, current: string) {
  const keys = [...new Set([...lightKeys, ...(current ? [current] : [])])].sort();
  const want = ['', ...keys].join('|');
  if (sel.dataset.keys !== want) {
    sel.dataset.keys = want;
    sel.replaceChildren(new Option('No zone light', ''), ...keys.map((k) => new Option(`Zone light ${k}`, k)));
  }
  sel.value = current;
  sel.hidden = keys.length === 0;
}

/**
 * The timeline only shows the current event: entries before `timelineFrom`
 * (set by "Clear", or when the event is renamed) stay on the server but are
 * hidden here. Drills can be filtered out.
 */
let timelineFrom = Number(lsGet('pulse.timelineFrom') ?? 0) || 0;
let hideDrills = lsGet('pulse.hideDrills') === '1';
function lsGet(k: string) {
  try {
    return localStorage.getItem(k);
  } catch {
    return null;
  }
}
function lsSet(k: string, v: string) {
  try {
    localStorage.setItem(k, v);
  } catch {
    /* fine */
  }
}
function startTimelineAt(t: number) {
  timelineFrom = t;
  lsSet('pulse.timelineFrom', String(t));
  renderLog();
}

function renderLog(fresh = false) {
  const ol = $('log');
  const shown = timeline.filter((e) => e.t >= timelineFrom && !(hideDrills && e.test)).sort((x, y) => x.t - y.t);
  const hiddenDrills = hideDrills ? timeline.filter((e) => e.t >= timelineFrom && e.test).length : 0;
  $('logEmpty').hidden = shown.length > 0;
  $('logEmpty').textContent = hiddenDrills
    ? `No incidents for this event (${hiddenDrills} drill${hiddenDrills === 1 ? '' : 's'} hidden).`
    : 'No incidents for this event yet.';
  ol.replaceChildren(
    ...shown
      .slice()
      .reverse()
      .map((e, i) => {
        const li = document.createElement('li');
        li.className = `lv-${e.level}${e.test ? ' drill' : ''}`;
        if (fresh && i === 0) li.classList.add('new');
        li.innerHTML =
          `<time>${fmtTime(e.t)}</time><span class="lv ${e.level}">${levelText[e.level]}</span>` +
          `<span class="txt">${e.test ? '<span class="tag">DRILL</span>' : ''}${e.src ? `<span class="tag src">${e.src === 'sim' ? 'SIMULATION' : 'SAVED RUN'}</span>` : ''}${e.area ? '<span class="tag area">AREA</span>' : ''}${esc(e.text)}</span>`;
        return li;
      }),
  );
}
$('logClear').addEventListener('click', async () => {
  // It clears for every console, so ask once: the first click arms the button for 4 s.
  const btn = $('logClear') as HTMLButtonElement;
  if (btn.dataset.armed !== '1') {
    btn.dataset.armed = '1';
    btn.textContent = 'Clear for everyone?';
    btn.classList.add('warn');
    window.setTimeout(() => {
      btn.dataset.armed = '0';
      btn.textContent = 'Clear';
      btn.classList.remove('warn');
    }, 4000);
    return;
  }
  btn.dataset.armed = '0';
  btn.textContent = 'Clear';
  btn.classList.remove('warn');
  // The server drops resolved and drill alerts; open real alerts stay (they still need handling).
  try {
    const r = await fetch('/api/alerts/clear', { method: 'POST' });
    if (!r.ok) throw new Error(String(r.status));
    for (const [id, a] of alertsById) if (a.status === 'resolved' || a.test) alertsById.delete(id);
    timeline = timeline.filter((e) => e.id && alertsById.has(e.id));
    renderAlertCards();
    renderLog();
    toast('Timeline cleared. Open alerts are kept until they are resolved.');
  } catch {
    // Older server: hide everything before now, on this console only.
    startTimelineAt(Date.now());
    toast('Timeline cleared on this console.');
  }
});
($('logHideDrills') as HTMLInputElement).checked = hideDrills;
$('logHideDrills').addEventListener('change', (e) => {
  hideDrills = (e.target as HTMLInputElement).checked;
  lsSet('pulse.hideDrills', hideDrills ? '1' : '0');
  renderLog();
});

function showBrief(a: Alert, fresh = false) {
  lastBrief = a;
  const el = $('brief');
  el.classList.remove('muted');
  $('briefMeta').textContent = `· ${fmtTime(a.t)}${a.test ? ' · drill' : ''}`;
  $('briefPanel').classList.toggle('red', a.level === 'red' && !a.test);
  $('briefPanel').classList.toggle('drill', !!a.test);
  ($('replayAudioBtn') as HTMLButtonElement).disabled = false;
  syncBriefVisibility();
  if (!fresh) {
    el.textContent = a.brief ?? '';
    return;
  }
  // Type the briefing out, word by word.
  $('briefPanel').classList.remove('flash');
  void $('briefPanel').offsetWidth;
  $('briefPanel').classList.add('flash');
  const words = (a.brief ?? '').split(' ');
  let i = 0;
  el.textContent = '';
  const id = window.setInterval(() => {
    if (lastBrief !== a || i >= words.length) return window.clearInterval(id);
    el.textContent += (i ? ' ' : '') + words[i++];
  }, 55);
}

// ---------------------------------------------------------------------------
// sound: browsers only allow it after a click, hence the button
// ---------------------------------------------------------------------------

let audioCtx: AudioContext | null = null;

function setSound(on: boolean) {
  soundOn = on;
  $('soundBtn').dataset.tip = soundOn ? 'Spoken alerts on (click to mute)' : 'Turn on spoken alerts';
  $('soundBtn').classList.toggle('on', soundOn);
  $('setSound').textContent = soundOn ? 'Turn off' : 'Turn on';
  $('setSound').classList.toggle('on', soundOn);
  toast(soundOn ? 'Spoken alerts on' : 'Spoken alerts off');
  if (soundOn) {
    audioCtx ??= new AudioContext();
    void audioCtx.resume();
    speechSynthesis?.speak(new SpeechSynthesisUtterance(''));
  } else {
    speechSynthesis?.cancel();
  }
}
$('soundBtn').addEventListener('click', () => setSound(!soundOn));
$('setSound').addEventListener('click', () => setSound(!soundOn));

$('replayAudioBtn').addEventListener('click', () => {
  if (lastBrief) playBrief(lastBrief, true);
});

function beep() {
  if (!soundOn || !audioCtx) return;
  const t = audioCtx.currentTime;
  for (const [i, f] of [880, 660, 880].entries()) {
    const o = audioCtx.createOscillator();
    const g = audioCtx.createGain();
    o.frequency.value = f;
    o.type = 'square';
    g.gain.setValueAtTime(0.0001, t + i * 0.18);
    g.gain.exponentialRampToValueAtTime(0.12, t + i * 0.18 + 0.02);
    g.gain.exponentialRampToValueAtTime(0.0001, t + i * 0.18 + 0.16);
    o.connect(g).connect(audioCtx.destination);
    o.start(t + i * 0.18);
    o.stop(t + i * 0.18 + 0.17);
  }
}

let currentAudio: HTMLAudioElement | null = null;

function speak(text: string) {
  if (!('speechSynthesis' in window)) return;
  speechSynthesis.cancel();
  const u = new SpeechSynthesisUtterance(text);
  u.rate = 1.02;
  speechSynthesis.speak(u);
}

function playBrief(a: Alert, force = false) {
  if (!soundOn && !force) return;
  // An area set to "no voice" stays silent (the server skips its audio, so
  // don't fall back to the browser's voice either), unless staff press Play.
  if (!force && areas.get(a.zone)?.rules?.notify?.voice === false) return;
  currentAudio?.pause();
  if (!a.brief) return;
  if (a.audioUrl) {
    currentAudio = new Audio(a.audioUrl);
    currentAudio.play().catch(() => speak(a.brief!));
  } else {
    speak(a.brief);
  }
}

// ---------------------------------------------------------------------------
// snapshots and alerts
// ---------------------------------------------------------------------------

function onSnapshot(s: Snapshot) {
  // Phones with no position yet (a GPS phone before its first usable fix) are connected but nowhere:
  // they stay off the map and out of every pair and area, and are counted as "locating".
  const locating = s.nodes.filter((n) => n.unplaced && n.status !== 'stale').length;
  if (locating || s.nodes.some((n) => n.unplaced)) s = { ...s, nodes: s.nodes.filter((n) => !n.unplaced) };
  const loc = $('locating');
  loc.hidden = locating === 0;
  if (locating) loc.textContent = `${locating} ${locating === 1 ? 'phone' : 'phones'} locating…`;
  snap = s;
  nodesById.clear();
  for (const n of s.nodes) nodesById.set(n.id, n);

  // The server's overall status is the one source for level and score; older servers: worked out here.
  const risk = s.status ? { level: s.status.level, score: s.status.score } : crowdRisk(s);
  if (s.t - lastHistAt >= 1000) {
    lastHistAt = s.t;
    riskHist.push(risk.score);
    if (riskHist.length > 60) riskHist.shift();
  }

  zoneNames.clear();
  for (const z of s.zones) zoneNames.set(z.id, z.name);
  const clusters = s.clusters ?? [];
  mesh.update(s.nodes, s.waves, s.links ?? [], clusters, s.venue ?? { w: 24, h: 16 });
  table.update(s);
  areas.sync(s.zones, s.nodes);
  meshNet.onSnapshot(s.mesh, s.nodes);
  // People each phone stands for (for the "Max people" hint).
  const ppl = clusters.reduce((n, c) => n + (c.people ?? 0), 0);
  const cnt = clusters.reduce((n, c) => n + (c.people != null ? c.count : 0), 0);
  const ratio = cnt > 0 ? ppl / cnt : null;
  if (ratio && Math.round(ratio) !== Math.round(peoplePerPhone ?? 0)) {
    peoplePerPhone = ratio;
    renderShareHint();
  }
  situation = computeSituation(s);
  const worstArea = areas.worst();
  mesh.level = worstArea === 'danger' ? 'red' : risk.level === 'red' ? 'red' : worstArea === 'watch' ? 'yellow' : risk.level;
  renderRisk(risk.level, risk.score);
  renderStatus(clusters);
  renderTooltip();

  setCounter('cPhones', s.stats.phones);
  setCounter('cRate', Math.round(s.stats.msgPerSec));
  // Connection quality in words; the milliseconds stay in the small print.
  const rtt = s.stats.medianRtt;
  $('cRtt').textContent = !rtt ? '–' : rtt < 150 ? 'Good' : rtt < 400 ? 'Fair' : 'Poor';
  $('cRttSub').textContent = rtt ? `phones answer in about ${Math.round(rtt)} ms` : 'no phones connected';
  $('cRtt').parentElement!.classList.toggle('hot', rtt >= 400);
  setCounter('cWaves', s.waves.length);
  $('cDetect').textContent = s.stats.detectMs != null ? s.stats.detectMs.toFixed(2) : '–';
  $('cSnap').textContent = s.stats.snapshotBytes != null ? (s.stats.snapshotBytes / 1024).toFixed(1) : '–';

  const replay = s.mode === 'replay';
  const simulating = s.mode === 'sim';
  const badge = $('mode');
  badge.textContent = replay ? 'SAVED RUN' : simulating ? 'SIMULATION' : 'LIVE';
  badge.className = `badge ${replay ? 'replay' : simulating ? 'sim' : 'live'}`;
  $('replayBanner').hidden = !replay && !simulating;
  $('replayBanner').classList.toggle('sim', simulating);
  // A recording made in another venue plays in that venue, and a classroom or auditorium simulation builds its own
  // room: today's floor plan, layout and areas don't belong on them.
  const foreign = (replay || simulating) && !!s.venue && (s.venue.w !== venue.w || s.venue.h !== venue.h);
  if (foreign !== foreignVenue) {
    foreignVenue = foreign;
    areas.hidden = foreign;
    mesh.setLayout(foreign ? null : (venue.layout ?? null));
    mesh.setFloorplan(foreign ? null : planImg);
  }
  if (replay) {
    $('rbTag').textContent = 'SAVED RUN';
    $('replayInfo').textContent = `${s.replay ?? ''} · ${Math.round((s.progress ?? 0) * 100)}%`;
    $('rbNote').textContent = foreign
      ? `Not live: recorded in a ${s.venue.w} × ${s.venue.h} m room, shown in that room without today's areas.`
      : 'Not live: a saved run is playing through the detector.';
  } else if (simulating) {
    $('rbTag').textContent = 'SIMULATION';
    const where = scenarioOf(simState?.scenario ?? '')?.name;
    $('replayInfo').textContent = `${where ? `${where} · ` : ''}${simState?.people ?? s.sim?.bodies.length ?? 0} people · ${actionText[s.sim?.action ?? ''] ?? s.sim?.action ?? ''}`;
    $('rbNote').textContent = foreign
      ? `Not live: a virtual ${where?.toLowerCase() ?? 'venue'} (${s.venue.w} × ${s.venue.h} m) is feeding the detector, shown without today's areas.`
      : 'Not live: a virtual crowd is feeding the detector.';
  }
  mesh.setSim(simulating ? (s.sim ?? null) : null, simulating ? simState : null);
  const mode = s.mode;
  if (mode !== lastMode) {
    const was = lastMode;
    lastMode = mode;
    renderSimRunning(mode === 'sim');
    void showSimPreview(); // the room preview gives way to the running crowd, and comes back after it
    renderReplayRunning(mode === 'replay');
    if (mode !== 'sim') {
      if (was === 'sim') simEnded();
      areas.cancelPick();
    }
    renderAlertCards();
  }
  renderSimStatus(s);

  $('recBadge').hidden = !s.recording;
  const recBtn = $('recBtn');
  recBtn.textContent = s.recording ? `■ Stop and save "${s.recording}"` : '● Start recording';
  recBtn.classList.toggle('on', !!s.recording);
  if (drawerId && !nodesById.has(drawerId)) openDrawer(null);
  demo.onMode(mode);

  updateQR();
  // Setup's last step: a real phone joined (simulated and replayed ones don't count).
  if (s.mode === 'live' && s.stats.phones > 0 && !flag('pulse.joined')) setFlag('pulse.joined');
  refreshSetup();
}

/** A zone id as staff know it: the area's name, the zone's name, never a raw id. */
function placeName(zone: string): string {
  return areas.get(zone)?.name ?? zoneNames.get(zone) ?? (/^[A-Z]$/.test(zone) ? `Zone ${zone}` : 'the venue');
}

function onAlert(a: Alert, fresh: boolean) {
  if (a.id && alertsById.has(a.id)) {
    // An update to an alert we already have (briefing arrived, acknowledged, resolved, escalated).
    const prev = alertsById.get(a.id)!;
    // The server always sends the whole alert, and leaves out what is false or empty: take it as it
    // is (merging would keep "early warning" on an incident that has since gone red).
    const next = { ...a };
    alertsById.set(a.id, next);
    // The audit trail goes into the timeline too.
    if (a.status === 'ack' && prev.status !== 'ack') {
      logEntry({ id: a.id, t: a.ackAt ?? Date.now(), level: 'calm', test: next.test, text: `${placeName(next.zone)}: acknowledged${next.ackBy ? ` by ${next.ackBy}` : ''}.` }, fresh);
    }
    if (a.status === 'resolved' && prev.status !== 'resolved') {
      logEntry({ id: a.id, t: a.resolvedAt ?? Date.now(), level: 'calm', test: next.test, text: `${placeName(next.zone)}: resolved${next.resolvedBy ? ` by ${next.resolvedBy}` : ''}${next.note ? `: “${next.note}”` : '.'}` }, fresh);
    }
    renderAlertCards();
    if (fresh && a.escalated && !prev.escalated) {
      toast(`Escalated: ${placeName(a.zone)} still unacknowledged`, 'danger');
      if (a.brief) playBrief(a);
    }
    if (fresh && a.brief && !prev.brief) {
      showBrief(a, fresh);
      playBrief(a);
    }
    return;
  }
  if (a.id) {
    alertsById.set(a.id, a);
    renderAlertCards();
  }
  const where = placeName(a.zone);
  const fallback = a.test
    ? `Drill at ${where}: not a real incident.`
    : a.level === 'calm' ? `${where}: back to calm.` // the notice of a zone returning to calm
    : a.kind === 'density' ? `${where}: crowd too dense.` : a.kind === 'rule' ? `${where}: alert rule crossed.` : a.cause && a.level !== 'red' ? `${causeHead(a.cause, where)}.` : `${where}: crowd push detected.`;
  logEntry({ id: a.id, t: a.t, level: a.level, text: a.brief ?? fallback, test: a.test, src: a.source, area: a.kind === 'density' ? 'DENSITY' : undefined }, fresh);
  // History from the server: its acknowledgement and resolution too.
  if (a.ackAt && (a.status === 'ack' || a.status === 'resolved')) {
    logEntry({ id: a.id, t: a.ackAt, level: 'calm', test: a.test, text: `${where}: acknowledged${a.ackBy ? ` by ${a.ackBy}` : ''}.` }, false);
  }
  if (a.status === 'resolved') {
    logEntry({ id: a.id, t: a.resolvedAt ?? a.t, level: 'calm', test: a.test, text: `${where}: resolved${a.resolvedBy ? ` by ${a.resolvedBy}` : ''}${a.note ? `: “${a.note}”` : '.'}` }, false);
  }
  if (a.brief) {
    showBrief(a, fresh);
    if (fresh) playBrief(a);
  } else if (fresh && a.level === 'red') {
    beep();
  }
}

// ---------------------------------------------------------------------------
// WebSocket
// ---------------------------------------------------------------------------

let backoff = 500;
function connect() {
  const ws = new WebSocket(wsURL('/ws/dash'));
  ws.onopen = () => {
    backoff = 500;
    $('wsDot').classList.add('on');
  };
  ws.onmessage = (ev) => {
    const msg = JSON.parse(ev.data as string) as ToDash;
    if (msg.type === 'snapshot') onSnapshot(msg);
    else if (msg.type === 'alert') onAlert(msg, true);
    else if (msg.type === 'alerts') {
      timeline = [];
      alertsById.clear(); // a full history: rebuild, so every alert gets its timeline entry
      for (const a of msg.alerts) onAlert(a, false);
      renderAlertCards();
      timeline.sort((x, y) => x.t - y.t);
      renderLog();
    }
  };
  ws.onclose = () => {
    $('wsDot').classList.remove('on');
    setTimeout(connect, backoff);
    backoff = Math.min(backoff * 2, 5000);
  };
  ws.onerror = () => ws.close();
}

// ---------------------------------------------------------------------------
// controls
// ---------------------------------------------------------------------------

/** A short status message for an action the operator just took. */
function msg(text: string, isErr = false) {
  toast(text, isErr ? 'error' : 'info');
}

/** An HTTP error with its status code (409 = already running, and so on). */
class HttpError extends Error {
  constructor(message: string, readonly status: number) {
    super(message);
  }
}

async function send<T = Record<string, unknown>>(method: 'POST' | 'PUT' | 'DELETE', path: string, body?: unknown): Promise<T> {
  const r = await fetch(path, {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const j = (await r.json().catch(() => ({}))) as T & { error?: string };
  if (!r.ok) throw new HttpError(j.error ?? r.statusText, r.status);
  return j;
}
const post = <T = Record<string, unknown>>(path: string, body?: unknown) => send<T>('POST', path, body);

// ---- saved runs: play a recording through the detector, record a new one ----

/** One saved run as GET /api/recordings describes it (older servers send only the name). */
interface RecFile { name: string; label?: string; seconds?: number; phones?: number; w?: number; h?: number; legacy?: boolean }
let recFiles = new Map<string, RecFile>();
/** A recording made in another venue is on screen: today's floor plan, layout and areas are hidden. */
let foreignVenue = false;

function fmtDur(sec: number) {
  const s = Math.round(sec);
  return s < 90 ? `${s} s` : `${Math.floor(s / 60)} min ${s % 60 ? `${s % 60} s` : ''}`.trim();
}

/** What the selected run is, in one line, so nobody plays it blind. */
function renderRecInfo() {
  const name = ($('recSelect') as HTMLSelectElement).value;
  const f = recFiles.get(name);
  const el = $('recInfo');
  if (!name) {
    el.textContent = 'No saved runs yet. Record one below, or run the simulator.';
    return;
  }
  if (!f || f.seconds == null) {
    el.textContent = name.startsWith('db:') ? 'Stored in the database.' : '';
    return;
  }
  const expect = /wave|push/.test(name) ? 'should reach Danger' : /shove/.test(name) ? 'may reach Warning, never Danger' : /calm|walk|dance|jump|handle/.test(name) ? 'should stay calm' : '';
  const auto = name.startsWith('auto/') ? 'kept automatically while the database was unreachable · ' : '';
  const where = f.legacy
    ? `an early demo: ${f.phones} phones in a row, recorded before phones had map positions. It plays in the ${f.w} × ${f.h} m room it was made in, without today's areas`
    : f.w && (f.w !== venue.w || f.h !== venue.h)
      ? `recorded in a ${f.w} × ${f.h} m venue: it plays in that venue, without today's areas`
      : 'recorded in this venue';
  el.textContent = `${auto}${fmtDur(f.seconds)} · ${f.phones} phone${f.phones === 1 ? '' : 's'}${expect ? ` · ${expect}` : ''} · ${where}.`;
}

async function loadRecordings(select?: string) {
  try {
    const r = await fetch('/api/recordings');
    const j = (await r.json()) as { files: RecFile[]; runs: { label: string }[] };
    const sel = $('recSelect') as HTMLSelectElement;
    const prev = select ?? sel.value;
    sel.replaceChildren();
    recFiles = new Map(j.files.map((f) => [f.name, f]));
    const opts = [...j.files.map((f) => f.name), ...j.runs.map((r) => `db:${r.label}`)];
    if (!opts.length) {
      const o = new Option('no saved runs yet', '');
      o.disabled = true;
      sel.append(o);
    }
    // Labelled runs first; the automatic fallback recordings after them.
    const named = opts.filter((o) => !o.startsWith('auto/'));
    const auto = opts.filter((o) => o.startsWith('auto/'));
    for (const name of named) sel.append(new Option(name.replace(/\.jsonl$/, ''), name));
    if (auto.length) {
      const g = document.createElement('optgroup');
      g.label = 'Kept automatically';
      for (const name of auto) g.append(new Option(name.replace(/^auto\//, '').replace(/\.jsonl$/, ''), name));
      sel.append(g);
    }
    const pick = opts.includes(prev) ? prev : (named.find((o) => /wave/.test(o)) ?? named[0] ?? opts[0]);
    if (pick) sel.value = pick;
    ($('replayPlayBtn') as HTMLButtonElement).disabled = !opts.length;
    $('savedRunsMeta').textContent = opts.length ? `${named.length} saved${auto.length ? ` · ${auto.length} automatic` : ''}` : 'none yet';
    renderRecInfo();
  } catch {
    /* server restarting */
  }
}
$('recSelect').addEventListener('change', renderRecInfo);

let lastMode: Snapshot['mode'] | '' = '';

/** Back to the live phones: stops a saved run or a simulation, whichever is on screen. */
async function goLive() {
  try {
    await post('/api/live');
    await fetch('/api/sim/stop', { method: 'POST' }).catch(() => {}); // 409 when none runs: fine
    msg('Showing live phones');
  } catch (e) {
    msg((e as Error).message, true);
  }
}
$('backLiveBtn').addEventListener('click', () => void goLive());
$('replayStopBtn').addEventListener('click', () => void goLive());

function renderReplayRunning(running: boolean) {
  $('replayStopBtn').hidden = !running;
  $('replayPlayBtn').textContent = running ? '↻ Play from the start' : '▶ Play';
  if (running) ($('savedRuns') as HTMLDetailsElement).open = true;
}

$('replayPlayBtn').addEventListener('click', async () => {
  const name = ($('recSelect') as HTMLSelectElement).value;
  if (!name) return msg('Pick a saved run first', true);
  const speed = Number(($('speedSelect') as HTMLSelectElement).value);
  try {
    await post('/api/replay', { name, speed });
    msg(lastMode === 'sim' ? `The simulation was stopped to play ${name}` : `Playing ${name}`);
  } catch (e) {
    msg((e as Error).message, true);
  }
});
$('recBtn').addEventListener('click', async () => {
  try {
    if (snap?.recording) {
      const r = await post<{ name: string; records: number }>('/api/record/stop');
      msg(`Saved ${r.name} (${r.records} records)`);
      await loadRecordings(r.name);
    } else {
      if (lastMode === 'replay') return msg('A saved run is playing: go back to live (or start a simulation) before recording.', true);
      const label = ($('recLabel') as HTMLInputElement).value.trim() || 'run';
      await post('/api/record/start', { label });
      msg(`Recording "${label}"`);
    }
  } catch (e) {
    msg((e as Error).message, true);
  }
});

// ---- alert drill: choose where, how bad, what kind and which outputs; send; see what each output did ----

let drillStatus: DrillStatus | null = null;
/** The server has no GET /api/drill (it is older than this console). */
let drillLegacy = false;
let drillLevel: 'yellow' | 'red' = 'red';
let drillKind: 'wave' | 'density' | 'rule' = 'wave';
/** Outputs the operator switched off by hand (by key); everything else follows readiness and the area's rules. */
const drillOff = new Set<string>();
const drillOn = new Set<string>();
/** The drill whose result is on screen. */
let drillShown: string | null = null;
let drillPoll = 0;

const kindLabel = { wave: 'Crowd push', density: 'Crowding', rule: 'Over capacity' } as const;
const readyText = { ready: 'Ready', fallback: 'Fallback', offline: 'Offline', none: 'Not connected' } as const;

function segPick(id: string, attr: string, value: string) {
  for (const b of $(id).querySelectorAll<HTMLButtonElement>('button')) {
    const on = b.dataset[attr] === value;
    b.classList.toggle('on', on);
    b.setAttribute('aria-checked', String(on));
  }
}
for (const b of $('drillLevel').querySelectorAll<HTMLButtonElement>('button')) {
  b.addEventListener('click', () => {
    drillLevel = b.dataset.level as 'yellow' | 'red';
    renderDrillForm();
  });
}
for (const b of $('drillKind').querySelectorAll<HTMLButtonElement>('button')) {
  b.addEventListener('click', () => {
    drillKind = b.dataset.kind as typeof drillKind;
    renderDrillForm();
  });
}
$('drillZone').addEventListener('change', () => {
  // A different place: back to that area's own choices.
  drillOff.clear();
  drillOn.clear();
  renderDrillForm();
});

/** Is this output going to be exercised, given readiness, the area's rules and the operator's toggles? */
function drillWants(key: string, zone: DrillZone | undefined, ready: DrillReady): boolean {
  if (drillOff.has(key)) return false;
  if (drillOn.has(key)) return true;
  if (ready.state === 'none') return false;
  if (key === 'voice') return zone?.voice !== false;
  if (key === 'sign') return zone?.sign !== false;
  if (key.startsWith('light:')) return zone?.light === key.slice(6);
  return true;
}

function renderDrillForm() {
  const st = drillStatus;
  segPick('drillLevel', 'level', drillLevel);
  segPick('drillKind', 'kind', drillKind);
  const sel = $('drillZone') as HTMLSelectElement;
  const btn = $('drillSend') as HTMLButtonElement;
  if (!st) {
    // A server from before the drill page: the one-button test alert still works.
    btn.disabled = !drillLegacy;
    btn.textContent = 'Send drill';
    $('drillSendNote').textContent = drillLegacy
      ? 'This Pulse server is older than the console: restart it to choose the place, level and outputs. Until then this sends its standard test alert.'
      : 'Waiting for the server…';
    return;
  }
  const want = st.zones.map((z) => `${z.id}=${z.name}`).join('|');
  if (sel.dataset.zones !== want) {
    const prev = sel.value;
    sel.dataset.zones = want;
    sel.replaceChildren(...st.zones.map((z) => new Option(z.custom ? z.name : `${z.name} (whole ${z.id === 'rest' ? 'rest of the venue' : 'zone'})`, z.id)));
    sel.value = st.zones.some((z) => z.id === prev) ? prev : (st.zones[0]?.id ?? '');
  }
  const zone = st.zones.find((z) => z.id === sel.value);
  const ul = $('drillOutputs');
  ul.replaceChildren(
    ...st.outputs.map((o) => {
      const li = document.createElement('li');
      const wants = drillWants(o.key, zone, o);
      const needsBrief = o.key === 'voice' && !drillWants('briefing', zone, st.outputs[0]);
      li.className = `drill-out ${o.state}${wants ? ' on' : ''}`;
      const areasNote = o.areas?.length ? `shows ${o.areas.join(', ')}` : o.key.startsWith('light:') ? 'no area assigned to it' : '';
      const ruleNote =
        (o.key === 'voice' && zone?.voice === false) || (o.key === 'sign' && zone?.sign === false)
          ? 'switched off for this area in its alert rules'
          : '';
      li.innerHTML =
        `<label><input type="checkbox" ${wants ? 'checked' : ''} ${o.state === 'none' ? 'disabled' : ''} /><b></b></label>` +
        `<span class="ready ${o.state}"></span><span class="do-note"></span>`;
      li.querySelector('b')!.textContent = o.label;
      li.querySelector('.ready')!.textContent = readyText[o.state];
      li.querySelector('.do-note')!.textContent = [needsBrief ? 'needs the briefing: there is nothing to read out without it' : o.note, areasNote, ruleNote].filter(Boolean).join(' · ');
      li.querySelector('input')!.addEventListener('change', (e) => {
        const on = (e.target as HTMLInputElement).checked;
        drillOff.delete(o.key);
        drillOn.delete(o.key);
        (on ? drillOn : drillOff).add(o.key);
        renderDrillForm();
      });
      return li;
    }),
  );
  const chosen = st.outputs.filter((o) => drillWants(o.key, zone, o));
  const offline = chosen.filter((o) => o.state === 'offline');
  $('drillOutputsHint').textContent = offline.length
    ? `${offline.map((o) => o.label).join(', ')} ${offline.length === 1 ? 'is' : 'are'} offline: the drill will still be sent, and will report ${offline.length === 1 ? 'it' : 'them'} as failed.`
    : 'Ticked outputs are exercised. “Fallback” still works: the text comes from a template, the voice from this browser.';
  btn.disabled = !zone;
  btn.textContent = `Send drill: ${drillLevel === 'red' ? 'Danger' : 'Warning'} · ${kindLabel[drillKind]}${zone ? ` · ${zone.name}` : ''}`;
  $('drillSendNote').textContent = zone ? '' : 'No area or zone to send it to yet.';
}

const drillIcon = { ok: '✓', failed: '✗', skipped: '–', pending: '…' } as const;

function drillOutputsHTML(d: DrillRecord, compact = false) {
  return d.outputs
    .map(
      (o) =>
        `<li class="${o.state}"><span class="ic">${drillIcon[o.state]}</span><span class="lb">${esc(o.label)}</span>` +
        (compact || !o.note ? '' : `<span class="nt">${esc(o.note)}</span>`) +
        `</li>`,
    )
    .join('');
}

function renderDrillResult() {
  const st = drillStatus;
  const d = st?.history.find((h) => h.id === drillShown) ?? st?.history[0];
  const box = $('drillResult');
  if (!d) {
    box.innerHTML = '<p class="muted small">Nothing sent yet. After you send a drill, each output reports here what it did.</p>';
    $('drillResultMeta').textContent = '';
  } else {
    const al = alertsById.get(d.id);
    const open = al && al.status !== 'resolved';
    $('drillResultMeta').textContent = `${fmtTime(d.t)} · ${d.level === 'red' ? 'Danger' : 'Warning'} · ${kindLabel[d.kind as keyof typeof kindLabel] ?? d.kind}`;
    box.innerHTML =
      `<p class="dr-where"><span class="tag">DRILL</span> ${esc(d.where)}</p>` +
      `<ul class="dr-outs">${drillOutputsHTML(d)}</ul>` +
      (d.brief ? `<p class="dr-brief">${esc(d.brief)}</p>` : '') +
      `<div class="row">` +
      (d.brief ? `<button class="sm" data-play>▶ Play voice</button>` : '') +
      (open ? `<button class="sm ghost" data-end>End drill</button>` : al ? '<span class="muted small">Drill ended.</span>' : '') +
      `</div>` +
      (d.brief && !d.audioUrl ? '<p class="muted small">No clip was made: “Play voice” uses this browser’s voice.</p>' : '');
    box.querySelector('[data-play]')?.addEventListener('click', () => playBrief({ ...(al ?? ({} as Alert)), brief: d.brief, audioUrl: d.audioUrl, zone: d.zone } as Alert, true));
    box.querySelector('[data-end]')?.addEventListener('click', () => al && void alertAction(al, 'resolve', 'Drill ended'));
  }
  const hist = st?.history ?? [];
  $('drillHistoryEmpty').hidden = hist.length > 0;
  $('drillHistory').replaceChildren(
    ...hist.map((h) => {
      const li = document.createElement('li');
      li.className = h.id === d?.id ? 'sel' : '';
      li.innerHTML =
        `<button class="dh-row"><time>${fmtTime(h.t).slice(0, 5)}</time><span class="lv ${h.level}">${h.level === 'red' ? 'Danger' : 'Warning'}</span>` +
        `<span class="dh-what">${esc(kindLabel[h.kind as keyof typeof kindLabel] ?? h.kind)} · ${esc(h.where)}</span></button>` +
        `<ul class="dr-outs compact">${drillOutputsHTML(h, true)}</ul>`;
      li.querySelector('button')!.addEventListener('click', () => {
        drillShown = h.id;
        renderDrillResult();
      });
      return li;
    }),
  );
}

async function loadDrill() {
  try {
    const r = await fetch('/api/drill');
    drillLegacy = r.status === 404;
    if (!r.ok) throw new Error(String(r.status));
    drillStatus = (await r.json()) as DrillStatus;
  } catch {
    if (drillLegacy) renderDrillForm();
    return; // server restarting, or older than this console
  }
  renderDrillForm();
  renderDrillResult();
}

$('drillSend').addEventListener('click', async () => {
  const st = drillStatus;
  if (!st && drillLegacy) {
    try {
      const r = await post<{ zone: string }>('/api/test-alert');
      toast(`Drill sent to ${placeName(r.zone)}`, 'ok');
      setFlag('pulse.drill');
      refreshSetup();
    } catch (e) {
      toast(`Couldn't send the drill: ${(e as Error).message}`, 'error');
    }
    return;
  }
  const zoneId = ($('drillZone') as HTMLSelectElement).value;
  const zone = st?.zones.find((z) => z.id === zoneId);
  if (!st || !zone) return;
  const wants = (key: string) => {
    const o = st.outputs.find((x) => x.key === key);
    return !!o && drillWants(key, zone, o);
  };
  const touched = (key: string) => drillOn.has(key) || drillOff.has(key);
  const btn = $('drillSend') as HTMLButtonElement;
  btn.disabled = true;
  try {
    const d = await post<DrillRecord>('/api/test-alert', {
      zone: zoneId,
      level: drillLevel,
      kind: drillKind,
      // Only what the operator switched by hand is sent as a choice; the rest follows the area's own
      // alert rules on the server, which then says so in the result ("switched off for this area").
      outputs: {
        briefing: touched('briefing') ? wants('briefing') : undefined,
        voice: touched('voice') ? wants('voice') : undefined,
        sign: touched('sign') ? wants('sign') : undefined,
        lights: st.outputs.some((o) => o.key.startsWith('light:') && touched(o.key))
          ? st.outputs.filter((o) => o.key.startsWith('light:') && wants(o.key)).map((o) => o.key.slice(6))
          : undefined,
      },
    });
    drillShown = d.id;
    st.history = [d, ...st.history.filter((h) => h.id !== d.id)];
    renderDrillResult();
    toast(`Drill sent to ${d.where}`, 'ok');
    setFlag('pulse.drill');
    refreshSetup();
    // The briefing and the voice report back within a few seconds.
    window.clearInterval(drillPoll);
    let n = 0;
    drillPoll = window.setInterval(async () => {
      await loadDrill();
      const cur = drillStatus?.history.find((h) => h.id === d.id);
      if (++n > 30 || (cur && !cur.outputs.some((o) => o.state === 'pending'))) window.clearInterval(drillPoll);
    }, 1000);
  } catch (e) {
    toast(`Couldn't send the drill: ${(e as Error).message}`, 'error');
  }
  btn.disabled = false;
  renderDrillForm();
});
$('drillBannerResolve').addEventListener('click', () => {
  for (const a of alertsById.values()) if (a.test && a.status !== 'resolved') void alertAction(a, 'resolve', 'Drill ended');
});
onPage((p) => {
  if (p === 'drill') void loadDrill();
});
setInterval(() => {
  if (page() === 'drill') void loadDrill();
}, 4000);

$('askForm').addEventListener('submit', async (e) => {
  e.preventDefault();
  const q = ($('askInput') as HTMLInputElement).value.trim();
  if (!q) return;
  const out = $('askAnswer');
  out.hidden = false;
  out.textContent = 'Thinking…';
  try {
    const r = await post<{ answer: string }>('/api/ask', { question: q });
    out.textContent = r.answer;
  } catch (err) {
    out.textContent = `Error: ${(err as Error).message}`;
  }
});
$('fsBtn').addEventListener('click', () => {
  if (document.fullscreenElement) void document.exitFullscreen();
  else void document.documentElement.requestFullscreen();
});

// QR overlay: shown automatically while nobody has joined, or on demand.
// "shown" = the modal from the top-bar button (any page); "auto" = inside the
// Live map while nobody has joined; "hidden" = dismissed.
let qrMode: 'auto' | 'shown' | 'hidden' = 'auto';
/** The tower whose check-in code the modal shows; null = the ordinary join code. */
let qrTower: Hardware | null = null;
let joinUrl = '';
let qrVer = 0; // bumped when the join link changes, so the code image reloads
const qrDefault = { title: $('qrTitle').textContent ?? '', caption: document.querySelector('#qr .qr-caption')!.textContent ?? '' };
let onLivePage = false;
onPage((p) => {
  onLivePage = p === 'live';
  updateQR();
});
$('qrBtn').addEventListener('click', () => {
  qrMode = qrMode === 'shown' && !qrTower ? 'hidden' : 'shown';
  qrTower = null;
  updateQR();
});
const closeQR = () => {
  qrMode = 'hidden';
  updateQR();
};

// A tower's check-in code reuses the join modal: same link with ?at=<key>.

function renderQRContent() {
  const key = qrTower?.key;
  const img = $('qrImg') as HTMLImageElement;
  const src = key ? `/api/qr.png?at=${encodeURIComponent(key)}&v=${qrVer}` : `/api/qr.png?v=${qrVer}`;
  if (!img.src.endsWith(src)) img.src = src;
  $('qrTitle').textContent = qrTower ? `Check in at ${qrTower.name === 'This laptop' ? 'the control laptop' : qrTower.name}` : qrDefault.title;
  document.querySelector('#qr .qr-caption')!.textContent = qrTower
    ? 'Standing right here? Scan to join: Pulse puts you at this spot on the crowd map, no GPS or tapping needed.'
    : qrDefault.caption;
  $('qrUrl').textContent = key && joinUrl ? `${joinUrl}?at=${encodeURIComponent(key)}` : joinUrl;
}

function openTowerQR(h: Hardware) {
  qrTower = h;
  qrMode = 'shown';
  updateQR();
}
$('qrClose').addEventListener('click', closeQR);
// Click on the backdrop (not the card) closes; Esc closes.
$('qr').addEventListener('click', (e) => e.target === $('qr') && closeQR());
window.addEventListener('keydown', (e) => e.key === 'Escape' && !$('qr').hidden && closeQR());
$('qrCopy').addEventListener('click', async () => {
  try {
    await navigator.clipboard.writeText($('qrUrl').textContent ?? '');
    toast('Join link copied');
  } catch {
    toast('Copy failed: select the link and copy it by hand', 'error');
  }
});
$('qrPrint').addEventListener('click', () => {
  // A clean page with just the code and the caption.
  const w = window.open('', '_blank', 'width=720,height=900');
  if (!w) return toast('The browser blocked the print window', 'error');
  const img = ($('qrImg') as HTMLImageElement).src;
  w.document.write(
    `<!doctype html><title>Join Pulse</title><body style="font-family:Inter,system-ui,sans-serif;text-align:center;padding:40px">` +
      `<h1 style="font-size:44px;margin:0 0 12px">${esc($('qrTitle').textContent ?? 'Scan to join')}</h1>` +
      `<img src="${esc(img)}" style="width:420px;image-rendering:pixelated" onload="setTimeout(()=>print(),200)"/>` +
      `<p style="font-size:22px;max-width:520px;margin:16px auto">${esc($('qrUrl').closest('.qr-text')!.querySelector('.qr-caption')!.textContent ?? '')}</p>` +
      `<p style="font-size:16px;color:#555">${esc($('qrUrl').textContent ?? '')}</p></body>`,
  );
  w.document.close();
});

function updateQR() {
  if (qrMode !== 'shown') qrTower = null;
  renderQRContent();
  const auto = qrMode === 'auto' && onLivePage && snap?.mode === 'live' && snap.stats.phones === 0;
  const show = qrMode === 'shown' || auto;
  const qr = $('qr');
  // Modal on top of everything, or tucked inside the Live map.
  const stage = document.querySelector('#mapWrap .stage');
  if (qrMode === 'shown') {
    if (qr.parentElement !== document.body) document.body.append(qr);
  } else if (auto && stage && qr.parentElement !== stage) {
    stage.append(qr);
  }
  qr.classList.toggle('modal', qrMode === 'shown');
  const wasHidden = qr.hidden;
  qr.hidden = !show;
  $('qrBtn').classList.toggle('on', qrMode === 'shown');
  if (show && wasHidden && qrMode === 'shown') {
    animate(qr.querySelector('.qr-card')!, { opacity: [0, 1], scale: [0.96, 1] }, { duration: 0.2 });
    $('qrClose').focus();
  }
  if (show && !flag('pulse.qrShown')) {
    setFlag('pulse.qrShown');
    refreshSetup();
  }
}

// The join link changed (joinqr.ts: staff pasted a tunnel URL): a fresh code and address.
window.addEventListener('pulse:join', () => {
  qrVer++;
  void loadJoinInfo();
});

/** The join link and whether phones can reach it (GET /api/join; /api/phone-url on older servers). */
async function loadJoinInfo() {
  let url = '';
  let reach: 'public' | 'lan' | 'local' | '' = '';
  try {
    const r = await fetch('/api/join');
    if (!r.ok) throw new Error();
    const j = (await r.json()) as { url: string; reachable?: 'public' | 'lan' | 'local' };
    url = j.url;
    reach = j.reachable ?? '';
  } catch {
    try {
      url = (await (await fetch('/api/phone-url')).text()).trim();
    } catch {
      return;
    }
  }
  if (!reach) {
    // Work it out from the address ourselves.
    const host = (() => {
      try {
        return new URL(url).hostname;
      } catch {
        return '';
      }
    })();
    reach = /^(localhost|127\.|\[?::1\]?$)/.test(host)
      ? 'local'
      : /^(10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.)|\.local$/.test(host)
        ? 'lan'
        : 'public';
  }
  joinUrl = url;
  renderQRContent();
  const warn = $('qrWarn');
  warn.hidden = reach === 'public';
  warn.textContent =
    reach === 'local'
      ? 'Phones can’t open this address: it only works on this computer. Start the tunnel or set PUBLIC_URL.'
      : 'This address only works for phones on the same Wi-Fi as this computer. For attendees on mobile data, start the tunnel or set PUBLIC_URL.';
  warn.classList.toggle('bad', reach === 'local');
}

async function init() {
  try {
    const cfg = (await (await fetch('/api/config')).json()) as Config;
    if (cfg.yellow && cfg.red) thresholds = { yellow: cfg.yellow, red: cfg.red };
    if (cfg.defaultW && cfg.defaultH) defaultVenue = { w: cfg.defaultW, h: cfg.defaultH };
    setThresholdLines();
  } catch {
    /* defaults */
  }
  try {
    const st = (await (await fetch('/api/status')).json()) as Record<string, boolean>;
    const names: Record<string, string> = {
      tiger: 'Incident history · Tiger Data',
      gemini: 'AI briefings · Gemini',
      elevenlabs: 'Voice · ElevenLabs',
      sign: 'Signs & lights',
    };
    $('services').replaceChildren(
      ...Object.entries(names).map(([k, label]) => {
        const s = document.createElement('span');
        // Signs follow the boards' real online state (loadHardware), not just "configured".
        const on = k === 'sign' ? (hwLoaded ? hwOnline > 0 : !!st[k]) : !!st[k];
        s.className = `svc ${on ? 'on' : ''}`;
        s.dataset.svc = k;
        s.textContent = label;
        s.title = k === 'sign' && hwLoaded ? `${hwOnline} of ${hwTotal} boards online` : on ? 'connected' : 'not configured: falling back';
        return s;
      }),
    );
  } catch {
    /* ignore */
  }
  ($('qrImg') as HTMLImageElement).src = '/api/qr.png';
  void loadJoinInfo();
  initJoinQR(); // the QR window's link test, link setting and join counts (joinqr.ts)
  await loadRecordings();
  await areas.load();
  await loadVenue();
  areasLoaded = venueLoaded = true;
  refreshSetup();
  connect();
}
/** Setup only celebrates steps once the state they depend on has loaded. */
let areasLoaded = false;
let venueLoaded = false;

void init();

// ---------------------------------------------------------------------------
// venue: size and GPS anchor (phones' GPS is turned into metres on this map)
// ---------------------------------------------------------------------------

let venue: Venue = { w: 24, h: 16, geo: false };

function renderVenue() {
  ($('vW') as HTMLInputElement).value = String(venue.w);
  ($('vH') as HTMLInputElement).value = String(venue.h);
  ($('vBearing') as HTMLInputElement).value = String(venue.bearing ?? 0);
  $('vGeo').textContent = venue.geo
    ? `GPS on: map anchored at ${venue.lat?.toFixed(5)}, ${venue.lon?.toFixed(5)}`
    : 'GPS off: attendees place themselves on the map';
  $('vGeo').className = `small ${venue.geo ? 'ok-text' : 'muted'}`;
  ($('vClearGeo') as HTMLButtonElement).disabled = !venue.geo;
  renderTemplates();
  loadPlanImage();
  mesh.setLayout(venue.layout ?? null);
  refreshSetup();
}

async function loadVenue() {
  try {
    const r = await fetch('/api/venue');
    if (r.ok) venue = (await r.json()) as Venue;
  } catch {
    /* defaults */
  }
  renderVenue();
}

/**
 * Save the venue. Only explicit actions call this (Save size, a template, Apply,
 * GPS buttons); `sized` marks the size as chosen by staff for the setup checklist.
 */
async function saveVenue(next: Venue, opts: { sized?: boolean; quiet?: boolean } = {}) {
  const resized = next.w !== venue.w || next.h !== venue.h;
  if (resized && lastMode === 'sim') {
    toast('Stop the simulation to change the venue size.', 'error');
    renderVenue();
    return;
  }
  try {
    const r = await fetch('/api/venue', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(next),
    });
    const j = (await r.json().catch(() => ({}))) as Venue & { error?: string };
    if (!r.ok) throw new Error(j.error ?? r.statusText);
    venue = j;
    if (opts.sized) setFlag('pulse.sizeChosen');
    renderVenue();
    if (!opts.quiet) toast(resized ? `Venue size saved: ${venue.w} × ${venue.h} m` : 'Venue saved');
    if (resized) {
      // Areas drawn for the old size may now hang off the edge.
      const out = areas.list.filter((a) => a.poly.some(([x, y]) => x < 0 || y < 0 || x > venue.w + 0.01 || y > venue.h + 0.01));
      if (out.length) {
        toast(`${out.length === 1 ? `“${out[0].name}” lies` : `${out.length} areas lie`} partly outside the new venue size. Move or redraw ${out.length === 1 ? 'it' : 'them'} on Areas & alerts.`, 'watch');
      }
    }
  } catch (e) {
    toast(`Couldn't save venue: ${(e as Error).message}`, 'error');
  }
}

function venueForm(): Venue {
  const num = (id: string, d: number) => {
    const v = Number(($(id) as HTMLInputElement).value);
    return Number.isFinite(v) && v > 0 ? v : d;
  };
  return { ...venue, w: num('vW', venue.w), h: num('vH', venue.h), bearing: Number(($('vBearing') as HTMLInputElement).value) || 0 };
}

$('vSave').addEventListener('click', () => {
  const f = venueForm();
  const tpl = TEMPLATES.find((t) => t.w === f.w && t.h === f.h);
  void saveVenue({ ...f, template: tpl?.id ?? 'custom' }, { sized: true });
});

// Centre the map on this laptop: its GPS fix becomes the middle of the venue,
// and the top-left corner (the map origin) is worked out from the size.
$('vAnchor').addEventListener('click', () => {
  if (!('geolocation' in navigator)) return toast('This browser has no location access.', 'error');
  toast('Getting this laptop’s location…');
  navigator.geolocation.getCurrentPosition(
    (p) => {
      const v = venueForm();
      const { latitude: lat, longitude: lon, accuracy } = p.coords;
      const b = ((v.bearing ?? 0) * Math.PI) / 180;
      // Map vector from the centre to the top-left corner, rotated into east/north.
      const dx = -v.w / 2, dyDown = -v.h / 2;
      const east = dx * Math.cos(b) - dyDown * Math.sin(b);
      const north = -(dx * Math.sin(b) + dyDown * Math.cos(b));
      const lat0 = lat + north / 110540;
      const lon0 = lon + east / (111320 * Math.cos((lat * Math.PI) / 180));
      void saveVenue({ ...v, lat: lat0, lon: lon0, geo: true }, { quiet: true }).then(() =>
        toast(`Anchored (laptop GPS ±${Math.round(accuracy)} m)`),
      );
    },
    (err) => toast(`Couldn't get location: ${err.message}`, 'error'),
    { enableHighAccuracy: true, timeout: 15000 },
  );
});

$('vClearGeo').addEventListener('click', () => void saveVenue({ ...venueForm(), lat: undefined, lon: undefined, geo: false }));

// ---------------------------------------------------------------------------
// hardware: every sign and zone light, as the server last found it
// ---------------------------------------------------------------------------

function ago2(ms: number) {
  const sec = Math.round(ms / 1000);
  return sec < 60 ? `${sec}s` : sec < 3600 ? `${Math.round(sec / 60)} min` : `${Math.round(sec / 3600)} h`;
}

function bars(rssi?: number) {
  if (!rssi) return '';
  const n = rssi >= -55 ? 4 : rssi >= -65 ? 3 : rssi >= -75 ? 2 : 1;
  return `<span class="bars b${n}" title="Wi-Fi ${rssi} dBm"><i></i><i></i><i></i><i></i></span>`;
}

async function loadHardware() {
  let list: Hardware[] = [];
  try {
    const r = await fetch('/api/hardware');
    if (r.ok) list = (await r.json()) as Hardware[];
  } catch {
    return;
  }
  lightKeys = list.filter((h) => h.zone).map((h) => h.zone!);
  // The laptop is a tower (a check-in point), not a board to keep online.
  const boards = list.filter((h) => h.kind !== 'laptop');
  hwPlaced = list.filter((h) => h.x != null).length;
  hwOnline = boards.filter((h) => h.online).length;
  hwTotal = boards.length;
  hwLoaded = true;
  mesh.setBoards(list);
  table.setBoards(list);
  areas.onChange();
  // Settings' "Sign" chip follows what Hardware sees, not just "configured".
  const signChip = document.querySelector<HTMLElement>('.svc[data-svc="sign"]');
  if (signChip) {
    signChip.classList.toggle('on', hwOnline > 0);
    signChip.classList.toggle('warn', hwOnline > 0 && hwOnline < hwTotal);
    signChip.title = !hwTotal ? 'no boards configured' : `${hwOnline} of ${hwTotal} boards online`;
  }
  $('hwEmpty').hidden = boards.length > 0;
  const on = hwOnline;
  $('hwSummary').textContent = boards.length ? `${on} of ${boards.length} online` : '';
  const heard = list.reduce((n, h) => n + (h.ble?.devices ?? 0), 0);
  const openDetails = new Set([...$('hw').querySelectorAll<HTMLDetailsElement>('details[open]')].map((d) => d.dataset.url));
  $('hw').replaceChildren(
    ...list.map((h) => {
      const li = document.createElement('li');
      li.className = `hw-row ${h.online ? 'on' : 'off'}`;
      const host = h.url.replace(/^https?:\/\//, '');
      const lastAt = h.seenAgo != null ? fmtTime(Date.now() - h.seenAgo * 1000).slice(0, 5) : '';
      const laptop = h.kind === 'laptop';
      const status = laptop
        ? '<span class="muted">This computer</span>'
        : h.online
        ? `<span class="ok-text">Online</span>${bars(h.rssi)}${h.uptime ? `<span class="muted">on for ${ago2(h.uptime * 1000)}</span>` : ''}`
        : `<span class="off-text">Offline${lastAt ? ` · last answered ${lastAt}` : ' · has not answered yet'}</span>`;
      const fix = h.online || laptop
        ? ''
        : `<div class="hw-fix">Check its power and that it’s on the venue Wi-Fi (or plugged in by USB).${lastAt ? ` Last answered ${lastAt}.` : ''}</div>`;
      const details =
        `<details class="hw-details" data-url="${esc(h.url)}"${openDetails.has(h.url) ? ' open' : ''}><summary>Details</summary>` +
        `<div class="mono small">${esc(host)}</div>${h.error && !h.online ? `<div class="small muted">${esc(h.error)}</div>` : ''}</details>`;
      const lvl = h.online && h.level ? `<span class="st ${h.level === 'red' ? 'wave' : h.level === 'yellow' ? 'swaying' : 'ok'}">${esc(h.level)}</span>` : '';
      const ble = h.ble
        ? `<div class="hw-ble">📶 Bluetooth: <b>${h.ble.devices}</b> devices nearby · ${h.ble.near} close</div>`
        : '';
      const peers = h.peers?.length
        ? `<div class="hw-peers">Hears ${h.peers
            .map((p) => `<b>${esc(p.name)}</b> ≈${p.dist.toFixed(1)} m${p.mapDist != null ? ` (${p.mapDist.toFixed(1)} m on the map)` : ''}`)
            .join(', ')}</div>`
        : '';
      // Check-in point: a QR code that places whoever scans it next to this tower.
      const checkin =
        h.x != null && h.key
          ? `<div class="hw-checkin"><button class="sm" data-checkin>Check-in QR</button><span class="muted small">Phones that scan this code are placed here (${h.x.toFixed(1)} m, ${h.y!.toFixed(1)} m), no GPS or tapping</span></div>`
          : `<div class="hw-areas">Drag its marker onto the map to get a check-in QR: phones that scan it are placed at that spot</div>`;
      const shows = laptop
        ? ''
        : h.zone
        ? `<div class="hw-areas">${h.areas?.length ? `Shows ${h.areas.map(esc).join(', ')}` : 'No area assigned yet: choose “Zone light ' + esc(h.zone) + '” on a watch area'}</div>`
        : '<div class="hw-areas">Shows the worst alert anywhere</div>';
      li.innerHTML =
        `<span class="hw-dot"></span><div class="hw-main"><div class="hw-top"><b>${esc(h.name)}</b>${lvl}</div>` +
        `<div class="hw-sub">${status}</div>${fix}${ble}${peers}${shows}${checkin}${laptop ? '' : details}</div>`;
      li.querySelector('[data-checkin]')?.addEventListener('click', () => openTowerQR(h));
      return li;
    }),
  );
  if (heard && snap) {
    $('hwSummary').textContent += ` · ${heard} Bluetooth devices heard vs ${snap.stats.phones} on Pulse`;
  }
}
void loadHardware();
setInterval(() => void loadHardware(), 5000);

// ---------------------------------------------------------------------------
// simulation: a virtual crowd (Social Force Model) staff can steer
// ---------------------------------------------------------------------------

let simState: SimState | null = null;
const actionText: Record<string, string> = {
  calm: 'calm',
  stage: 'pressing to the stage',
  surge: 'surging',
  attract: 'gathering',
  shove: 'shoved',
  spawn: 'arriving',
  disperse: 'evacuating',
  exit: 'exits changed',
  dance: 'dancing',
  intermission: 'intermission',
  dismiss: 'leaving',
  alarm: 'alarm: evacuating',
  arrive: 'next class arriving',
  rush: 'kick-off rush',
};

/** The scenarios the server offers (GET /api/sim), the one chosen in the picker, and its preview geometry. */
let simScenarios: SimScenario[] = [];
let simScenario = 'concert';
let simPreviewFor = '';
/** Staff picked a scenario themselves (so a late scenario list or a running crowd must not change it under them). */
let simScenarioPicked = false;

function scenarioOf(id: string): SimScenario | undefined {
  return simScenarios.find((s) => s.id === id);
}

/** The picker: one button per scenario with a one-line description; choosing one sets the sliders' defaults. */
function renderScenarios() {
  const box = $('simScenarios');
  if (box.childElementCount !== simScenarios.length) {
    box.replaceChildren(
      ...simScenarios.map((s) => {
        const b = document.createElement('button');
        b.dataset.scn = s.id;
        b.setAttribute('role', 'radio');
        b.innerHTML = `<b>${esc(s.name)}</b><span>${esc(s.desc)}</span>`;
        b.addEventListener('click', () => chooseScenario(s.id, true));
        return b;
      }),
    );
  }
  for (const b of box.querySelectorAll<HTMLElement>('button')) {
    const on = b.dataset.scn === simScenario;
    b.classList.toggle('on', on);
    b.setAttribute('aria-checked', String(on));
  }
}

/** Pick a scenario: sliders take its defaults (unless staff moved them), the actions and the map preview follow. */
function chooseScenario(id: string, byUser: boolean) {
  const s = scenarioOf(id);
  if (!s) return;
  simScenario = id;
  if (byUser) simScenarioPicked = true;
  const people = $('simPeople') as HTMLInputElement;
  people.max = String(Math.min(1000, s.maxPeople));
  people.step = s.maxPeople > 200 ? '10' : '1';
  if (byUser) {
    simSlidersTouched = true;
    setSimSliders(s.people, Math.round(s.participation * 100));
  }
  renderScenarios();
  renderSimActions(s);
  renderSimApply();
  void showSimPreview();
}

/** The action buttons of a scenario (what the director can do there). */
function renderSimActions(s: SimScenario | undefined) {
  const box = $('simActions');
  const acts = s?.actions ?? [];
  const icon: Record<string, string> = {
    calm: '😌', dance: '💃', intermission: '🍺', stage: '🎤', surge: '🌊', attract: '📍', shove: '👉', spawn: '➕', disperse: '🚪',
    dismiss: '🔔', alarm: '🚨', arrive: '🎒', rush: '🏃',
  };
  box.replaceChildren(
    ...acts.map((a) => {
      const b = document.createElement('button');
      b.dataset.sim = a.type;
      b.dataset.tip = a.tip;
      b.textContent = `${icon[a.type] ?? '•'} ${a.label}`;
      b.addEventListener('click', () => runSimAction(a));
      return b;
    }),
  );
  const strength = acts.find((a) => a.kind === 'strength');
  ($('simStrength').parentElement as HTMLElement).hidden = !strength && !acts.some((a) => a.type === 'shove');
  $('simStrengthLabel').textContent = strength ? `${strength.label} strength` : 'Shove strength';
}

/** Run an action from its spec: a plain button, one with the strength slider, a click on the map, or a drag. */
function runSimAction(a: SimActionSpec) {
  const strength = Number(($('simStrength') as HTMLInputElement).value) / 100;
  areas.cancelPick(); // a new choice replaces a pick still waiting for a click
  switch (a.kind) {
    case 'behaviour':
      void simAction({ type: a.type } as SimAction);
      break;
    case 'strength':
      void simAction({ type: a.type, strength } as SimAction);
      break;
    case 'point':
      pickOnMap(a.type === 'spawn' ? 'Click where 20 people arrive' : 'Click where the group should gather', false, (p) =>
        void simAction(a.type === 'spawn' ? { type: 'spawn', x: p.x, y: p.y, n: 20 } : ({ type: a.type, x: p.x, y: p.y } as SimAction)),
      );
      break;
    case 'drag':
      pickOnMap('Drag on the map: where the push starts, and which way', true, (p) => {
        const len = Math.hypot(p.dx, p.dy);
        if (len < 0.3) return msg('Drag a little further to give the push a direction', true);
        void simAction({ type: 'shove', x: p.x, y: p.y, dx: p.dx / len, dy: p.dy / len, strength });
      });
      break;
  }
}

/** While nothing runs and the Simulation page is showing, the map shows the room the chosen scenario builds. */
async function showSimPreview() {
  const want = page() === 'sim' && lastMode !== 'sim' ? simScenario : '';
  if (!want) {
    if (simPreviewFor) {
      simPreviewFor = '';
      mesh.setSimPreview(null);
    }
    return;
  }
  if (want === simPreviewFor) return;
  simPreviewFor = want;
  try {
    const r = await fetch(`/api/sim?scenario=${encodeURIComponent(want)}`);
    if (!r.ok) return;
    const st = (await r.json()) as SimState;
    if (simPreviewFor !== want) return; // the choice moved on meanwhile
    if (st.scenarios?.length && !simScenarios.length) {
      simScenarios = st.scenarios;
      renderScenarios();
    }
    mesh.setSimPreview(st);
  } catch {
    /* server busy */
  }
}
onPage(() => void showSimPreview());

/** The operator moved a slider since the running crowd was started (so the sliders are a wish, not a mirror). */
let simSlidersTouched = false;
/** A start, stop or restart request is in flight. */
let simBusy = false;

for (const [id, out, fmt] of [
  ['simPeople', 'simPeopleV', (v: string) => v],
  ['simPart', 'simPartV', (v: string) => `${v}%`],
] as const) {
  const input = $(id) as HTMLInputElement;
  const show = () => {
    $(out).textContent = fmt(input.value);
    renderSimApply();
  };
  input.addEventListener('input', () => {
    simSlidersTouched = true;
    show();
  });
  show();
}

function setSimSliders(people: number, partPct: number) {
  const p = $('simPeople') as HTMLInputElement, q = $('simPart') as HTMLInputElement;
  p.value = String(people);
  q.value = String(partPct);
  $('simPeopleV').textContent = p.value;
  $('simPartV').textContent = `${q.value}%`;
}

/** The running simulation's real numbers vs the sliders: "Restart to apply" when the operator asked for something else. */
function renderSimApply() {
  const st = simState;
  const running = !!st?.running && lastMode === 'sim';
  const now = $('simNow');
  now.hidden = !running;
  const restart = $('simRestart') as HTMLButtonElement;
  if (!running || !st) {
    restart.hidden = true;
    return;
  }
  // People walk in and out during a show, so the head count drifts from what was asked for.
  const people = st.people ?? 0;
  const part = Math.round((st.participation ?? 0) * 100);
  now.textContent = `Running now: ${people} people · ${part}% carry Pulse (${st.phones ?? Math.round((people * part) / 100)} phones)`;
  const wantPeople = Number(($('simPeople') as HTMLInputElement).value);
  const wantPart = Number(($('simPart') as HTMLInputElement).value);
  const started = simStarted ?? { people, part, scenario: st.scenario ?? 'concert' };
  // A crowd started elsewhere (surge around the phones, another console): the sliders follow it until touched.
  if (!simSlidersTouched && (wantPeople !== started.people || wantPart !== started.part)) {
    const slider = $('simPeople') as HTMLInputElement;
    setSimSliders(Math.min(Number(slider.max), Math.max(Number(slider.min), started.people)), started.part);
  }
  const otherPlace = simScenario !== started.scenario;
  restart.hidden = !otherPlace && (!simSlidersTouched || (wantPeople === started.people && Math.abs(wantPart - started.part) < 1));
  restart.disabled = simBusy;
  if (!restart.hidden) {
    const where = scenarioOf(simScenario)?.name.toLowerCase() ?? simScenario;
    restart.dataset.tip = `Stop this crowd and start a ${where} with ${wantPeople} people, ${wantPart}% with the app`;
  }
}
/** What the running crowd was started with (people drift as they arrive and leave). */
let simStarted: { people: number; part: number; scenario: string } | null = null;

function renderSimRunning(running: boolean) {
  $('simStart').hidden = running;
  $('simStop').hidden = !running;
  $('simControls').hidden = !running;
  ($('simStart') as HTMLButtonElement).disabled = simBusy;
  ($('simStop') as HTMLButtonElement).disabled = simBusy;
  for (const b of document.querySelectorAll<HTMLButtonElement>('#simControls button')) b.disabled = simBusy;
  if (running && !simState?.running) void pollSim();
  renderSimApply();
}

/** The simulation ended (stopped, replaced by a saved run): nothing of it may linger as if current. */
function simEnded() {
  simState = null;
  simStarted = null;
  simSlidersTouched = false;
  exitsSig = '';
  $('simExits').replaceChildren();
  $('simTruth').innerHTML = '<p class="muted small">Start a simulation to compare what really happens in the crowd with what Pulse detects.</p>';
  $('simClock').textContent = '';
}

/** Start a crowd with the sliders' values; with `restart`, stop the running one first. */
async function startSim(restart: boolean) {
  if (simBusy) return;
  const people = Number(($('simPeople') as HTMLInputElement).value);
  const participation = Number(($('simPart') as HTMLInputElement).value) / 100;
  simBusy = true;
  renderSimRunning(lastMode === 'sim');
  try {
    for (let attempt = 0; ; attempt++) {
      if (restart || attempt > 0) {
        // 409 here only means it had already stopped.
        await send('POST', '/api/sim/stop').catch((e) => {
          if (!(e instanceof HttpError) || e.status !== 409) throw e;
        });
      }
      try {
        await post('/api/sim/start', { people, participation, scenario: simScenario });
        break;
      } catch (e) {
        // Already running (started from another console, or by "surge around the phones" a moment ago).
        if (e instanceof HttpError && e.status === 409 && attempt === 0) {
          if (!restart) {
            msg('A simulation is already running: showing it. Use “Restart to apply” for a new crowd.');
            break;
          }
          continue;
        }
        throw e;
      }
    }
    // A fresh run: nothing from the previous one stays on screen.
    simEnded();
    simStarted = { people, part: Math.round(participation * 100), scenario: simScenario };
    const where = scenarioOf(simScenario)?.name.toLowerCase() ?? simScenario;
    msg(restart ? `Restarted: ${where}, ${people} people, ${Math.round(participation * 100)}% with the app` : `Simulating a ${where} with ${people} people`);
    await pollSim();
  } catch (e) {
    msg((e as Error).message, true);
  }
  simBusy = false;
  renderSimRunning(lastMode === 'sim' || !!simState?.running);
}

$('simRestart').addEventListener('click', () => void startSim(true));
$('simStart').addEventListener('click', () => void startSim(false));
$('simStop').addEventListener('click', async () => {
  if (simBusy) return;
  simBusy = true;
  renderSimRunning(true);
  await goLive();
  simBusy = false;
  renderSimRunning(lastMode === 'sim');
});

async function simAction(a: SimAction) {
  try {
    await post('/api/sim/action', a);
    void pollSim();
  } catch (e) {
    msg((e as Error).message, true);
  }
}

function pickOnMap(label: string, arrow: boolean, done: (p: { x: number; y: number; dx: number; dy: number }) => void) {
  areas.pick = { label, arrow, done };
  const hint = $('hint');
  hint.textContent = `${label} · Esc to cancel`;
  animate(hint, { opacity: [0, 1], y: [-6, 0] }, { type: 'spring', bounce: 0.35, duration: 0.4 });
  const clear = () => animate(hint, { opacity: 0 }, { duration: 0.15 });
  const orig = done;
  areas.pick.done = (p) => {
    clear();
    orig(p);
  };
}

// The strength slider applies to a surge (or alarm) that is already running, too.
$('simStrength').addEventListener('change', () => {
  const act = simState?.action;
  const spec = scenarioOf(simState?.scenario ?? simScenario)?.actions.find((a) => a.type === act && a.kind === 'strength');
  if (lastMode === 'sim' && spec) void simAction({ type: spec.type, strength: Number(($('simStrength') as HTMLInputElement).value) / 100 } as SimAction);
});

const mmss = (sec: number) => `${Math.floor(sec / 60)}:${String(Math.floor(sec % 60)).padStart(2, '0')}`;
let exitsSig = '';

function renderSimState(st: SimState) {
  // Exits: rebuilt only when one changes, so a click is never lost to the 1 s refresh.
  const sig = (st.exits ?? []).map((e) => `${e.id}:${e.open}:${e.name}`).join('|');
  if (sig !== exitsSig) {
    exitsSig = sig;
    $('simExits').replaceChildren(
      ...(st.exits ?? []).map((e) => {
        const btn = document.createElement('button');
        const what = e.kind === 'door' ? 'door' : e.kind === 'turnstile' ? 'turnstile' : 'exit';
        btn.className = `sm exit ${e.open ? 'open' : 'closed'} ${e.kind ?? ''}`;
        btn.textContent = `${e.open ? '🟢' : '🔴'} ${e.name}`;
        btn.dataset.tip = e.open ? `Open. Click to close this ${what}` : `Closed. Click to open this ${what}`;
        btn.addEventListener('click', () => void simAction({ type: 'exit', id: e.id, open: !e.open }));
        return btn;
      }),
    );
  }
  $('simClock').textContent = st.running && st.t != null ? `running ${mmss(st.t)}` : '';
  const t = st.truth;
  if (!st.running || !t) return;
  // When it became dangerous vs when Pulse alerted, and the lead time between them.
  const danger = t.dangerAt != null ? `at ${mmss(t.dangerAt)}` : 'not yet';
  const alerted = t.alertAt != null ? `at ${mmss(t.alertAt)}` : 'not yet';
  let verdict = '<div class="lead idle">Nothing dangerous has happened in this run, and Pulse has raised no red alert.</div>';
  if (t.leadSeconds != null) {
    verdict =
      t.leadSeconds >= 0
        ? `<div class="lead good">Pulse warned <b>${t.leadSeconds.toFixed(0)} s before</b> the crowd became dangerous.</div>`
        : `<div class="lead bad">Pulse warned <b>${(-t.leadSeconds).toFixed(0)} s after</b> the crowd became dangerous.</div>`;
  } else if (t.alertAt != null) {
    verdict = `<div class="lead good">Pulse raised a red alert at ${mmss(t.alertAt)}; the crowd has not reached crush levels.</div>`;
  } else if (t.dangerAt != null) {
    verdict = `<div class="lead bad">The crowd became dangerous at ${mmss(t.dangerAt)} and Pulse has not raised a red alert yet.</div>`;
  }
  $('simTruth').innerHTML =
    `<h4>Ground truth (only the simulator knows this)</h4>` +
    `<div class="truth-grid"><div><b>${t.maxDensity.toFixed(1)}</b><span>people/m² at the densest spot</span></div>` +
    `<div><b>${Math.round(t.maxPressure)}</b><span>peak crush pressure (N/m)</span></div>` +
    `<div><b>${t.crushing}</b><span>people at crush level</span></div></div>` +
    `<dl class="truth-times"><dt>Crowd became dangerous</dt><dd>${danger}</dd><dt>Pulse’s first red alert</dt><dd>${alerted}</dd>` +
    `<dt>Lead time</dt><dd>${t.leadSeconds != null ? `${t.leadSeconds >= 0 ? '+' : '−'}${Math.abs(t.leadSeconds).toFixed(0)} s` : '–'}</dd></dl>${verdict}`;
}

async function pollSim() {
  try {
    const r = await fetch('/api/sim');
    if (!r.ok) return;
    const st = (await r.json()) as SimState;
    if (!st.running) {
      // Not running (any more): keep nothing of the last run.
      if (simState) simEnded();
      return;
    }
    simState = st;
    if (st.scenarios?.length) {
      simScenarios = st.scenarios;
      renderScenarios();
    }
    if (!simStarted || (st.scenario && simStarted.scenario !== st.scenario)) {
      // Started elsewhere (another console, "surge around the phones", the API): the picker follows the running scenario.
      simStarted = { people: st.people ?? 0, part: Math.round((st.participation ?? 0) * 100), scenario: st.scenario ?? 'concert' };
      if (!simScenarioPicked && st.scenario && st.scenario !== simScenario) chooseScenario(st.scenario, false);
    }
    // The action buttons are the running scenario's (not the picker's) while it runs.
    const running = scenarioOf(st.scenario ?? 'concert');
    if (running && $('simActions').childElementCount !== running.actions.length) renderSimActions(running);
    renderSimState(st);
    renderSimApply();
  } catch {
    /* server busy */
  }
}

/** The scenario list, once, so the picker is there before anything runs. */
void (async () => {
  try {
    const r = await fetch('/api/sim');
    if (!r.ok) return;
    const st = (await r.json()) as SimState;
    if (st.scenarios?.length) {
      simScenarios = st.scenarios;
      if (st.running && st.scenario && !simScenarioPicked) simScenario = st.scenario;
      if (!simScenarioPicked) chooseScenario(simScenario, false);
      else renderScenarios();
    }
  } catch {
    /* server busy */
  }
})();
setInterval(() => {
  if (lastMode === 'sim') void pollSim();
}, 1000);

/** The Simulation page's status line: the same sentence as the sidebar, marked as simulated. */
function renderSimStatus(s: Snapshot) {
  const sourced = s.mode === 'sim' || s.mode === 'replay';
  const el = $('simStatus');
  const cls = sourced ? situation.cls : 'calm';
  if (!el.classList.contains(cls)) el.className = `status sim-status ${cls}`;
  $('simStatusText').textContent = !sourced ? 'Not running' : s.mode === 'sim' ? `Simulated crowd: ${situation.text}` : `Saved run: ${situation.text}`;
  const tag = $('simAlertsTag');
  tag.hidden = !sourced;
  tag.textContent = s.mode === 'replay' ? 'SAVED RUN' : 'SIMULATION';
  tag.className = `src-tag ${s.mode === 'replay' ? 'replay' : 'sim'}`;
}

// ---------------------------------------------------------------------------
// alert cards: what / where / what to do, acknowledge and resolve
// ---------------------------------------------------------------------------

const alertsById = new Map<string, Alert>();

/** Split a two-sentence briefing into headline + action when the server didn't. */
function splitBrief(a: Alert): { head: string; action: string } {
  if (a.headline || a.action) return { head: a.headline ?? a.brief ?? '', action: a.action ?? '' };
  const parts = (a.brief ?? '').split(/(?<=[.!?])\s+/);
  return { head: parts[0] ?? '', action: parts.slice(1).join(' ') };
}

/** The alert whose "Resolve" form is open, and what has been typed in it (kept across re-renders). */
let resolving: { id: string; note: string } | null = null;

/** The operator's name for the audit trail: asked once, remembered on this console. */
function operator(): string {
  return lsGet('pulse.operator') ?? '';
}

/** Is the place this alert is about still alerting right now? */
function stillAlerting(a: Alert) {
  const z = snap?.zones.find((z) => z.id === a.zone);
  return (z && z.level !== 'calm') || areas.get(a.zone)?.level === 'danger' || (snap?.status?.zone === a.zone && snap.status.level !== 'calm');
}

/** "SIMULATION" / "SAVED RUN" for an alert that did not come from the live crowd. */
const sourceTag = (a: Alert) => (a.source === 'sim' ? 'SIMULATION' : a.source === 'replay' ? 'SAVED RUN' : '');

function renderAlertCards() {
  const all = [...alertsById.values()].filter((a) => a.status !== 'resolved' && a.level !== 'calm');
  const open = all.sort((x, y) => y.t - x.t).slice(0, 4);
  const badge = $('navAlert');
  const reds = open.filter((a) => a.level === 'red' && a.status !== 'ack' && !a.test).length;
  badge.hidden = reds === 0;
  badge.textContent = String(reds);
  // Drill banner while any drill is still open.
  const drill = all.find((a) => a.test);
  $('drillBanner').hidden = !drill;
  if (drill) $('drillWhere').textContent = placeName(drill.zone);
  // Live shows everything that is open; the Simulation page shows what the simulation (or saved run) raised.
  const sourced = all.filter((a) => a.source === 'sim' || a.source === 'replay').slice(0, 4);
  for (const [box, list] of [['alertCards', open], ['simAlertCards', sourced]] as const) {
    $(box).replaceChildren(...list.map((a) => alertCardEl(a, box)));
  }
  $('simAlertsEmpty').hidden = sourced.length > 0 || lastMode === 'sim' || lastMode === 'replay';
  syncBriefVisibility();
  renderSimBrief();
  renderAlertSummary();
  renderBriefAfterChange();
  if (page() === 'drill') renderDrillResult();
}

/** The briefing paragraph repeats the card's text: show it only when its alert has no card on screen. */
function syncBriefVisibility() {
  const id = lastBrief?.id;
  const onCard = !!id && !!$('alertCards').querySelector(`[data-id="${CSS.escape(id)}"]`);
  $('brief').hidden = onCard;
}

/** The briefing of the latest simulated alert, on the Simulation page. */
function renderSimBrief() {
  const a = [...alertsById.values()].filter((x) => (x.source === 'sim' || x.source === 'replay') && x.brief && x.status !== 'resolved').sort((x, y) => y.t - x.t)[0];
  $('simBriefBox').hidden = !a;
  if (!a) return;
  if ($('simBrief').textContent !== a.brief) $('simBrief').textContent = a.brief ?? '';
  $('simBriefBox').dataset.id = a.id ?? '';
}
$('simBriefPlay').addEventListener('click', () => {
  const a = alertsById.get($('simBriefBox').dataset.id ?? '');
  if (a) playBrief(a, true);
});

/** One alert card (what, where, what to do; acknowledge, resolve, why). `box` is the container it goes into. */
function alertCardEl(a: Alert, box: string): HTMLElement {
      const { head, action } = splitBrief(a);
      const el = document.createElement('div');
      el.className = `alert-card ${a.level} ${a.status === 'ack' ? 'ack' : ''} ${a.test ? 'drill' : ''} ${a.source ? 'sourced' : ''}`;
      if (a.id) el.dataset.id = a.id;
      const where = placeName(a.zone);
      const kind = a.test ? 'Drill' : a.early ? 'Early warning' : a.kind === 'density' ? 'Crowding' : a.kind === 'rule' ? 'Alert rule' : a.cause && a.level !== 'red' ? CAUSE_LABEL[a.cause] : 'Crowd push';
      const label = a.test ? 'DRILL · not a real incident' : `${sourceTag(a) ? `${sourceTag(a)} · ` : ''}${a.level === 'red' ? 'Danger' : 'Watch'}`;
      const acked = a.status === 'ack'
        ? `<span class="muted small">Acknowledged ${a.ackAt ? fmtTime(a.ackAt) : ''}${a.ackBy ? ` by ${esc(a.ackBy)}` : ''}</span>`
        : '<button class="sm primary" data-ack>Acknowledge</button>';
      const form = resolving?.id === a.id;
      const danger = form && !a.test && !a.source && stillAlerting(a);
      el.innerHTML =
        `<div class="ac-top"><b>${label}</b><span>${esc(kind)} · ${esc(where)} · ${fmtTime(a.t)}</span>` +
        `${a.escalated ? '<span class="esc">escalated</span>' : ''}</div>` +
        `<div class="ac-head">${esc(head || (a.test ? `Drill at ${where}` : a.cause && a.level !== 'red' ? causeHead(a.cause, where) : `${where}: ${kind.toLowerCase()} detected`))}</div>` +
        (action ? `<div class="ac-action">${esc(action)}</div>` : '') +
        (form
          ? `<form class="ac-resolve">` +
            (danger ? `<p class="ac-warn">The crowd here is still in danger. Resolve anyway?</p>` : '') +
            `<label>Outcome <span class="muted">(optional)</span><input name="note" maxlength="200" placeholder="e.g. Opened side gate, crowd eased" /></label>` +
            `<label>Your name <span class="muted">(for the log)</span><input name="by" maxlength="40" placeholder="e.g. Maya, safety lead" /></label>` +
            `<div class="ac-btns"><button class="sm ${danger ? 'danger-btn' : 'primary'}" type="submit">${danger ? 'Resolve anyway' : 'Resolve'}</button><button class="sm ghost" type="button" data-cancel>Cancel</button></div></form>`
          : `<div class="ac-btns">${acked}<button class="sm ghost" data-resolve>Resolve…</button>` +
            `<button class="sm ghost" data-escalate title="Announce this alert again now: voice and a red sign">Escalate now</button>` +
            `${!a.test && (a.kind ?? 'wave') === 'wave' ? '<button class="ac-why" data-why>Why did it fire?</button>' : ''}</div>`);
      el.querySelector('[data-why]')?.addEventListener('click', () => {
        const pair = evidenceFor(a);
        if (pair) void openExplain(pair);
        else toast('No two neighbouring phones in that area right now to show the evidence for.', 'info');
      });
      el.querySelector('[data-ack]')?.addEventListener('click', () => void alertAction(a, 'ack'));
      el.querySelector('[data-escalate]')?.addEventListener('click', () => {
        if (!a.id) return;
        escalateNow(a.id)
          .then(() => toast(`Escalated: ${placeName(a.zone)}`, 'danger'))
          .catch((e: Error) => toast(`Couldn't escalate: ${e.message}`, 'error'));
      });
      el.querySelector('[data-resolve]')?.addEventListener('click', () => {
        resolving = { id: a.id!, note: '' };
        renderAlertCards();
        el.isConnected || $(box).querySelector<HTMLInputElement>('.ac-resolve [name=note]')?.focus();
      });
      const f = el.querySelector<HTMLFormElement>('.ac-resolve');
      if (f) {
        const note = f.querySelector<HTMLInputElement>('[name=note]')!;
        const by = f.querySelector<HTMLInputElement>('[name=by]')!;
        note.value = resolving!.note;
        by.value = operator();
        note.addEventListener('input', () => resolving && (resolving.note = note.value));
        f.querySelector('[data-cancel]')!.addEventListener('click', () => {
          resolving = null;
          renderAlertCards();
        });
        f.addEventListener('submit', (e) => {
          e.preventDefault();
          if (by.value.trim()) lsSet('pulse.operator', by.value.trim());
          resolving = null;
          void alertAction(a, 'resolve', note.value.trim());
        });
      }
      return el;
}

/** After ack/resolve: the briefing paragraph follows what is still open. */
function renderBriefAfterChange() {
  if (!lastBrief?.id) return;
  const cur = alertsById.get(lastBrief.id);
  if (cur && cur.status !== 'resolved') return;
  const next = [...alertsById.values()].filter((a) => a.status !== 'resolved' && a.brief).sort((x, y) => y.t - x.t)[0];
  if (next) return showBrief(next);
  lastBrief = null;
  const el = $('brief');
  el.classList.add('muted');
  el.textContent = cur?.resolvedAt
    ? `Resolved at ${fmtTime(cur.resolvedAt)}${cur.resolvedBy ? ` by ${cur.resolvedBy}` : ''}. Nothing else is open.`
    : 'Nothing to report. When the detector raises a red alert, a short briefing for staff appears here and is read aloud.';
  $('briefMeta').textContent = '';
  $('briefPanel').classList.remove('red', 'drill');
  ($('replayAudioBtn') as HTMLButtonElement).disabled = true;
  el.hidden = false;
}

async function alertAction(a: Alert, what: 'ack' | 'resolve', note = '') {
  if (!a.id) return;
  const by = operator();
  if (what === 'ack' && !by && !flag('pulse.nameHint')) {
    // Never block an acknowledgement with a prompt: say once where the name goes.
    setFlag('pulse.nameHint');
    toast('Acknowledged. Add your name in Settings and the log will say who did.', 'info');
  }
  // Optimistic: the server's update arrives over the socket too.
  const now = Date.now();
  alertsById.set(a.id, {
    ...a,
    status: what === 'ack' ? 'ack' : 'resolved',
    ackAt: what === 'ack' ? now : a.ackAt,
    ackBy: what === 'ack' ? by || undefined : a.ackBy,
    resolvedAt: what === 'resolve' ? now : a.resolvedAt,
    resolvedBy: what === 'resolve' ? by || undefined : a.resolvedBy,
    note: what === 'resolve' ? note || undefined : a.note,
  });
  if (what === 'resolve') {
    logEntry({ id: a.id, t: now, level: 'calm', test: a.test, text: `${placeName(a.zone)}: resolved${by ? ` by ${by}` : ''}${note ? `: “${note}”` : '.'}` });
  } else {
    logEntry({ id: a.id, t: now, level: 'calm', test: a.test, text: `${placeName(a.zone)}: acknowledged${by ? ` by ${by}` : ''}.` });
  }
  renderAlertCards();
  try {
    await post(`/api/alerts/${encodeURIComponent(a.id)}/${what}`, what === 'ack' ? { by } : { by, note });
  } catch (e) {
    toast(`Couldn't ${what === 'ack' ? 'acknowledge' : 'resolve'}: ${(e as Error).message}`, 'error');
  }
}

// ---------------------------------------------------------------------------
// area alert rules
// ---------------------------------------------------------------------------

function buildRules(box: HTMLElement, id: string) {
  const a = areas.get(id);
  if (!a) return;
  const r: AlertRules = a.rules ?? {};
  const n = r.notify ?? {};
  // Plain sentences an operator can read aloud: "Alert when more than 4 people per m² for 5 s".
  // Empty boxes mean the rule is off; the server's limits are mirrored in min/max.
  box.innerHTML =
    `<div class="rules-form">` +
    `<p class="rf-note muted">Three ways this area can raise an alert. Leave a box empty to turn that rule off; changes save as you go.</p>` +
    `<div class="rf-rule"><span class="rf-name">Crowding</span>` +
    `<label class="rf-line"><span>more than</span><input type="number" name="density" min="0.5" max="20" step="0.5" placeholder="off" value="${r.density ?? ''}" aria-label="People per square metre" /><span>people per m²</span></label>` +
    `<label class="rf-line"><span>for at least</span><input type="number" name="densityHoldS" min="1" max="600" step="1" placeholder="5" value="${r.densityHoldS ?? ''}" aria-label="Seconds" /><span>seconds</span></label>` +
    `<p class="rf-hint">About 2 per m² is comfortable, 4 is tight, 5 and up is dangerous. Warning at 75 % of your limit.</p></div>` +
    `<div class="rf-rule"><span class="rf-name">Capacity</span>` +
    `<label class="rf-line"><span>more than</span><input type="number" name="maxPhones" min="1" max="100000" step="1" placeholder="off" value="${r.maxPhones ?? ''}" aria-label="Maximum people inside" /><span>people inside</span></label>` +
    `<p class="rf-hint" data-share></p></div>` +
    `<div class="rf-rule"><span class="rf-name">Crowd push</span>` +
    `<label class="rf-line rf-switch"><input type="checkbox" name="push" ${r.push !== false ? 'checked' : ''} role="switch" /><span>Detect pushes travelling through this area</span></label></div>` +
    `<label class="rf-block"><span>Message staff will hear</span><input type="text" name="message" maxlength="140" placeholder="e.g. Open the side gate and slow the barrier queue" value="${esc(r.message ?? '')}" />` +
    `<span class="rf-hint">Replaces the suggested action in every briefing for this area.</span></label>` +
    `<div class="rf-block"><span>Send to</span><div class="rf-chips">` +
    `<label class="chip"><input type="checkbox" name="sign" ${n.sign !== false ? 'checked' : ''}/> Sign</label>` +
    `<label class="chip"><input type="checkbox" name="light" ${n.light !== false ? 'checked' : ''}/> Zone light</label>` +
    `<label class="chip"><input type="checkbox" name="voice" ${n.voice !== false ? 'checked' : ''}/> Voice</label></div></div>` +
    `<span class="rf-saved" aria-live="polite"></span>` +
    `</div>`;
  renderShareHint(box);
  const read = (): AlertRules => {
    const v = (name: string) => (box.querySelector(`[name=${name}]`) as HTMLInputElement).value.trim();
    const num = (name: string, max: number) => {
      const x = Number(v(name));
      return v(name) && Number.isFinite(x) && x > 0 ? Math.min(max, x) : undefined;
    };
    const chk = (name: string) => (box.querySelector(`[name=${name}]`) as HTMLInputElement).checked;
    return {
      density: num('density', 20),
      densityHoldS: num('densityHoldS', 600),
      maxPhones: num('maxPhones', 100000),
      push: chk('push'),
      message: v('message') || undefined,
      notify: { sign: chk('sign'), light: chk('light'), voice: chk('voice') },
    };
  };
  const saved = box.querySelector<HTMLElement>('.rf-saved')!;
  box.addEventListener('change', () => {
    areas.setRules(id, read());
    // Inline confirmation next to the form, instead of a toast per keystroke.
    saved.textContent = 'Saved ✓';
    animate(saved, { opacity: [0, 1], y: [4, 0] }, { duration: 0.2 });
    window.clearTimeout(Number(saved.dataset.t));
    saved.dataset.t = String(window.setTimeout(() => animate(saved, { opacity: 0 }, { duration: 0.4 }), 2500));
  });
  box.addEventListener('click', (e) => e.stopPropagation());
}

/** How many attendees each Pulse phone stands for, from the clusters' estimate (people ÷ phones). */
let peoplePerPhone: number | null = null;
function renderShareHint(root: ParentNode = document) {
  const text =
    peoplePerPhone && peoplePerPhone > 1.05
      ? `Counted from the phones inside: right now about 1 in ${Math.round(peoplePerPhone)} attendees runs Pulse, so the count is scaled up.`
      : 'Counted from the phones running Pulse inside the area, scaled up by how many attendees run it.';
  for (const el of root.querySelectorAll<HTMLElement>('[data-share]')) el.textContent = text;
}

// ---------------------------------------------------------------------------
// venue templates and floor plan
// ---------------------------------------------------------------------------

const TEMPLATES = [
  { id: 'demo', name: 'Demo room', w: 8, h: 6, sub: 'A classroom or table demo' },
  { id: 'club', name: 'Club', w: 16, h: 12, sub: '≈ 300–500 people' },
  { id: 'theatre', name: 'Theatre floor', w: 30, h: 20, sub: '≈ 1,500 standing' },
  { id: 'arena', name: 'Arena floor', w: 60, h: 40, sub: '≈ 6,000 standing' },
  { id: 'festival', name: 'Festival field', w: 120, h: 80, sub: '≈ 20,000+' },
  // The rooms the simulator's classroom, auditorium and stadium-gate scenarios build (same sizes, so areas drawn here fit them).
  { id: 'classroom', name: 'Classroom', w: 11.6, h: 16, sub: 'Lecture room + corridor, 63 desks' },
  { id: 'auditorium', name: 'Auditorium', w: 30, h: 25, sub: '504 seats, lobby, 4 exits' },
  { id: 'gate', name: 'Stadium gate', w: 30, h: 22, sub: 'Turnstile bank inside fences' },
  { id: 'custom', name: 'Custom', w: 0, h: 0, sub: 'Type the size below' },
];

function renderTemplates() {
  const grid = $('tplGrid');
  if (!grid.childElementCount) {
    for (const t of TEMPLATES) {
      const b = document.createElement('button');
      b.className = 'tpl';
      b.dataset.tpl = t.id;
      b.innerHTML = `<b>${t.name}</b><span>${t.w ? `${t.w} × ${t.h} m · ` : ''}${t.sub}</span>`;
      b.addEventListener('click', () => {
        if (t.id === 'custom') {
          // Custom: type the metres, then "Save size" (that is what marks the step done).
          for (const b of grid.querySelectorAll<HTMLElement>('.tpl')) b.classList.toggle('on', b === b.parentElement!.querySelector('[data-tpl="custom"]'));
          ($('vW') as HTMLInputElement).focus();
          ($('vW') as HTMLInputElement).select();
          toast('Type the width and depth in metres, then press Save size.');
          return;
        }
        void saveVenue({ ...venue, w: t.w, h: t.h, template: t.id }, { sized: true });
      });
      grid.append(b);
    }
  }
  for (const b of grid.querySelectorAll<HTMLElement>('.tpl')) b.classList.toggle('on', b.dataset.tpl === (venue.template ?? ''));
  const t = TEMPLATES.find((x) => x.id === venue.template);
  $('vTplMeta').textContent = `${venue.w} × ${venue.h} m${t && t.id !== 'custom' ? ` · ${t.name}` : ''}`;
}

let planImg: HTMLImageElement | null = null;
const planAlpha = () => Number(($('fpOpacity') as HTMLInputElement).value) / 100;

function loadPlanImage() {
  $('fpTools').hidden = !venue.floorplan;
  if (!venue.floorplan) {
    planImg = null;
    mesh.setFloorplan(null);
    return;
  }
  const img = new Image();
  img.onload = () => {
    planImg = img;
    mesh.setFloorplan(img, planAlpha());
  };
  img.src = `/api/venue/floorplan?v=${Date.now()}`;
}

async function uploadPlan(file: File) {
  if (!/^image\/(png|jpeg|webp)$/.test(file.type)) return toast('Use a PNG, JPG or WebP image', 'error');
  if (file.size > 8 * 1024 * 1024) return toast('That image is over 8 MB', 'error');
  toast('Uploading floor plan…');
  try {
    const r = await fetch('/api/venue/floorplan', { method: 'POST', headers: { 'Content-Type': file.type }, body: file });
    const j = (await r.json().catch(() => ({}))) as Venue & { error?: string };
    if (!r.ok) throw new Error(j.error ?? (r.status === 404 ? 'the server needs the floor-plan update' : r.statusText));
    venue = j;
    renderVenue();
    toast('Floor plan uploaded. Press “Find stage and exits” to have AI read it.');
  } catch (e) {
    toast(`Upload failed: ${(e as Error).message}`, 'error');
  }
}

$('fpFile').addEventListener('change', (e) => {
  const f = (e.target as HTMLInputElement).files?.[0];
  if (f) void uploadPlan(f);
});
const drop = $('fpDrop');
drop.addEventListener('dragover', (e) => {
  e.preventDefault();
  drop.classList.add('over');
});
drop.addEventListener('dragleave', () => drop.classList.remove('over'));
drop.addEventListener('drop', (e) => {
  e.preventDefault();
  drop.classList.remove('over');
  const f = e.dataTransfer?.files?.[0];
  if (f) void uploadPlan(f);
});
$('fpOpacity').addEventListener('input', () => {
  if (planImg) mesh.setFloorplan(planImg, planAlpha());
  try {
    localStorage.setItem('pulse.planAlpha', ($('fpOpacity') as HTMLInputElement).value);
  } catch {
    /* fine */
  }
});
try {
  const a = localStorage.getItem('pulse.planAlpha');
  if (a) ($('fpOpacity') as HTMLInputElement).value = a;
} catch {
  /* fine */
}
$('fpRemove').addEventListener('click', async () => {
  try {
    const r = await fetch('/api/venue/floorplan', { method: 'DELETE' });
    if (r.ok) venue = (await r.json()) as Venue;
    else throw new Error(r.statusText);
    renderVenue();
    $('fpSuggest').hidden = true;
  } catch (e) {
    toast(`Couldn't remove: ${(e as Error).message}`, 'error');
  }
});

$('fpAnalyze').addEventListener('click', async () => {
  const box = $('fpSuggest');
  box.hidden = false;
  box.innerHTML = '<b>AI is reading the stage and exits from your plan…</b>';
  try {
    const r = await fetch('/api/venue/floorplan/analyze', { method: 'POST' });
    const j = (await r.json().catch(() => ({}))) as FloorplanSuggestion & { error?: string };
    if (!r.ok) throw new Error(j.error ?? r.statusText);
    const exits = j.layout.exits ?? [];
    box.innerHTML =
      `<b>AI suggests</b> <span class="muted">(${esc(j.confidence)} confidence)</span>` +
      `<ul><li>Size: <b>${j.w} × ${j.h} m</b></li><li>Stage: ${j.layout.stage ? 'found' : 'not found'}</li>` +
      `<li>Exits: ${exits.length ? exits.map((e) => esc(e.name)).join(', ') : 'none found'}</li></ul>` +
      `<p class="muted small">${esc(j.notes)}</p>` +
      `<div class="row"><button class="sm primary" data-apply>Apply to venue</button><button class="sm ghost" data-dismiss>Dismiss</button></div>`;
    // Preview it on the map straight away.
    mesh.setLayout(j.layout);
    box.querySelector('[data-apply]')!.addEventListener('click', () => {
      void saveVenue({ ...venue, w: j.w, h: j.h, layout: j.layout, template: 'custom' }, { sized: true });
      box.hidden = true;
    });
    box.querySelector('[data-dismiss]')!.addEventListener('click', () => {
      mesh.setLayout(venue.layout ?? null);
      box.hidden = true;
    });
  } catch (e) {
    box.innerHTML = `<b>Couldn't read the plan.</b> <span class="muted">${esc((e as Error).message)}</span>`;
  }
});

// ---------------------------------------------------------------------------
// hardware placement on the map
// ---------------------------------------------------------------------------

areas.onBoardMoved = async (key, x, y) => {
  try {
    const r = await fetch(`/api/hardware/${encodeURIComponent(key)}/pos`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ x: Math.round(x * 10) / 10, y: Math.round(y * 10) / 10 }),
    });
    if (!r.ok) throw new Error(r.status === 404 ? 'the server needs the board-placement update' : r.statusText);
    toast('Board placed');
    await loadHardware();
  } catch (e) {
    toast(`Couldn't save the position: ${(e as Error).message}`, 'error');
  }
  mesh.boardDrag = null;
};


// ---------------------------------------------------------------------------
// home: greeting and the event setup checklist
// ---------------------------------------------------------------------------

let hwOnline = 0;
let hwTotal = 0;
/** Boards (and the laptop) staff placed on the map. */
let hwPlaced = 0;
let hwLoaded = false;
// Function declarations (hoisted): updateQR and onSnapshot use these before this point in the file.
function flag(k: string) {
  try {
    return localStorage.getItem(k) === '1';
  } catch {
    return false;
  }
}
function setFlag(k: string) {
  try {
    localStorage.setItem(k, '1');
  } catch {
    /* fine */
  }
}

function renderGreeting() {
  const h = new Date().getHours();
  const part = h < 12 ? 'Good morning' : h < 18 ? 'Good afternoon' : 'Good evening';
  $('homeGreeting').textContent = `${part} · ${($('eventName') as HTMLInputElement).value || 'your event'}`;
}

/** A new event name starts a new event: the timeline shows only what happens from now. */
let eventNameBefore = eventInput.value;
function eventRenamed() {
  if (eventInput.value.trim() && eventInput.value !== eventNameBefore) startTimelineAt(Date.now());
  eventNameBefore = eventInput.value;
}

function setEventName(name: string) {
  const changed = name !== eventInput.value;
  eventInput.value = name;
  if (changed) eventRenamed();
  try {
    localStorage.setItem('pulse.event', name);
  } catch {
    /* fine */
  }
  setFlag('pulse.eventNamed');
  renderGreeting();
}

$('eventName').addEventListener('change', () => {
  eventRenamed();
  setFlag('pulse.eventNamed');
  renderGreeting();
  refreshSetup();
});

setup = new Setup({
  facts: () => ({
    loaded: areasLoaded && venueLoaded && hwLoaded,
    named: flag('pulse.eventNamed'),
    eventName: eventInput.value,
    template: !!venue.template,
    sizeChosen: flag('pulse.sizeChosen'),
    floorplan: !!venue.floorplan,
    areas: areas.list.length,
    rules: areas.list.some((a) => hasRules(a.rules)),
    hwOnline,
    hwTotal,
    drill: flag('pulse.drill'),
    joined: flag('pulse.joined'),
    qrShown: flag('pulse.qrShown'),
    alarm: situation.cls === 'calm' ? '' : situation.text,
    alarmSimulated: situation.cls !== 'calm' && !!snap && snap.mode !== 'live',
  }),
  toast: (text, kind) => toast(text, kind),
  onName: (name) => setEventName(name),
  onShowQR: () => {
    qrMode = 'shown';
    updateQR();
  },
  onEditName: () => {
    const box = document.querySelector<HTMLElement>('.event-switch');
    if (!box || !box.offsetParent) return false; // collapsed sidebar: use the setup bar's field instead
    eventInput.focus();
    eventInput.select();
    box.classList.add('setup-focus');
    window.setTimeout(() => box.classList.remove('setup-focus'), 4000);
    toast('Type your event name in the sidebar, then press Enter.');
    return true;
  },
  onEnter: (id) => {
    // Rules step: fold out "Alert when…" on the selected (or first) area.
    if (id === 'rules') {
      const a = areas.get(areas.selected ?? '') ?? areas.list[0];
      if (a) toggleRules(a.id, true);
    }
  },
});
setup.refresh();
// Fallback for changes made elsewhere (another tab, a flag set by the server's state).
setInterval(() => {
  renderGreeting();
  refreshSetup();
}, 5000);

// ---------------------------------------------------------------------------
// home: start over (reset the setup checklist, or also clear this event's data)
// ---------------------------------------------------------------------------

/** This console's setup progress (see setup.ts and the flags above). */
const SETUP_KEYS = ['pulse.setup.skipped', 'pulse.setup.exited', 'pulse.eventNamed', 'pulse.sizeChosen', 'pulse.drill', 'pulse.joined', 'pulse.qrShown'];
const DEFAULT_EVENT = eventInput.defaultValue || 'Main Floor';
/** The venue size the server starts with (GET /api/config defaultW / defaultH; 24 × 16 on older servers). */
let defaultVenue = { w: 24, h: 16 };

function lsDel(k: string) {
  try {
    localStorage.removeItem(k);
  } catch {
    /* fine */
  }
}

function resetChoice(): 'checklist' | 'data' {
  return (document.querySelector<HTMLInputElement>('#resetForm [name=resetWhat]:checked')?.value ?? 'checklist') as 'checklist' | 'data';
}

/** Exactly what "also clear this event's data" removes right now. */
function resetItems(): string[] {
  const boards = hwPlaced;
  return [
    `The event name “${eventInput.value || DEFAULT_EVENT}” (back to “${DEFAULT_EVENT}”)`,
    areas.list.length ? `${areas.list.length} watch area${areas.list.length === 1 ? '' : 's'} and ${areas.list.length === 1 ? 'its' : 'their'} alert rules: ${areas.list.map((a) => a.name).join(', ')}` : 'Watch areas (none drawn)',
    `The venue: ${venue.w} × ${venue.h} m${venue.floorplan ? ', its floor plan' : ''}${venue.layout ? ', the stage and exits' : ''}${venue.geo ? ', the GPS anchor' : ''} (back to ${defaultVenue.w} × ${defaultVenue.h} m, nothing else)`,
    boards ? `Where ${boards} board${boards === 1 ? ' is' : 's are'} placed on the map (the boards stay connected)` : 'Board positions on the map (none placed)',
    'The demo spot (turned off)',
    'The incident timeline, drills included (alerts that are still open and real stay until resolved)',
    'A running simulation or saved run (stopped)',
  ];
}

function renderResetDlg() {
  const data = resetChoice() === 'data';
  $('resetConfirmRow').hidden = !data;
  const go = $('resetGo') as HTMLButtonElement;
  go.textContent = data ? 'Clear this event’s data' : 'Reset checklist';
  go.className = data ? 'danger-btn' : 'primary';
  go.disabled = data && ($('resetConfirm') as HTMLInputElement).value.trim().toUpperCase() !== 'CLEAR';
  $('resetList').replaceChildren(
    ...resetItems().map((t) => {
      const li = document.createElement('li');
      li.textContent = t;
      return li;
    }),
  );
}

function openResetDlg(open: boolean) {
  const dlg = $('resetDlg');
  dlg.hidden = !open;
  if (!open) return;
  (document.querySelector<HTMLInputElement>('#resetForm [name=resetWhat][value=checklist]')!).checked = true;
  ($('resetConfirm') as HTMLInputElement).value = '';
  $('resetMsg').textContent = '';
  renderResetDlg();
  animate(dlg.querySelector('.modal-card')!, { opacity: [0, 1], scale: [0.96, 1] }, { duration: 0.2 });
  $('resetCancel').focus();
}

$('startOver').addEventListener('click', () => openResetDlg(true));
$('resetCancel').addEventListener('click', () => openResetDlg(false));
$('resetDlg').addEventListener('click', (e) => e.target === $('resetDlg') && openResetDlg(false));
window.addEventListener('keydown', (e) => e.key === 'Escape' && !$('resetDlg').hidden && openResetDlg(false));
$('resetForm').addEventListener('change', renderResetDlg);
$('resetConfirm').addEventListener('input', renderResetDlg);

/** Forget this console's setup progress; the steps are then worked out again from what really exists. */
function resetChecklist() {
  for (const k of SETUP_KEYS) lsDel(k);
  setup?.reset();
}

/** Clear the event's data on the server, step by step; returns what could not be cleared. */
async function clearEventData(): Promise<string[]> {
  const failed: string[] = [];
  const step = async (what: string, fn: () => Promise<unknown>) => {
    try {
      await fn();
    } catch (e) {
      failed.push(`${what} (${(e as Error).message})`);
    }
  };
  // A simulation or saved run would block the venue change, and its alerts are not this event's.
  await step('stopping the simulation', async () => {
    await send('POST', '/api/live');
    await send('POST', '/api/sim/stop').catch((e) => {
      if (!(e instanceof HttpError) || e.status !== 409) throw e;
    });
  });
  await step('watch areas', () => send('PUT', '/api/areas', []));
  await step('floor plan', async () => {
    const r = await fetch('/api/venue/floorplan', { method: 'DELETE' });
    if (!r.ok) throw new Error(r.statusText);
  });
  await step('venue', () => send('PUT', '/api/venue', { w: defaultVenue.w, h: defaultVenue.h, geo: false, template: '' }));
  await step('board positions', () => send('DELETE', '/api/hardware/pos'));
  await step('demo spot', () => send('PUT', '/api/demo', { ...(mesh.demoSpot ?? { x: defaultVenue.w / 2, y: defaultVenue.h / 2, spacing: 0.6 }), on: false }));
  await step('timeline', () => send('POST', '/api/alerts/clear'));
  return failed;
}

$('resetForm').addEventListener('submit', async (e) => {
  e.preventDefault();
  const data = resetChoice() === 'data';
  const go = $('resetGo') as HTMLButtonElement;
  if (go.disabled) return;
  go.disabled = true;
  let failed: string[] = [];
  if (data) {
    $('resetMsg').textContent = 'Clearing…';
    failed = await clearEventData();
    // The event name and the timeline cut-off live on this console.
    eventInput.value = DEFAULT_EVENT;
    eventNameBefore = DEFAULT_EVENT;
    lsDel('pulse.event');
    lsDel('pulse.timelineFrom');
    timelineFrom = 0;
    for (const [id, a] of alertsById) if (a.status === 'resolved' || a.test) alertsById.delete(id);
    timeline = timeline.filter((x) => x.id && alertsById.has(x.id));
    // Show the server's state as it is now.
    await areas.load();
    await loadVenue();
    await loadHardware();
    demo.reload();
    renderAlertCards();
    renderLog();
  }
  resetChecklist();
  renderGreeting();
  refreshSetup();
  if (failed.length) {
    $('resetMsg').textContent = `Could not clear: ${failed.join('; ')}. Everything else was reset.`;
    go.disabled = false;
    toast('Reset finished with problems: see the dialog.', 'error');
    return;
  }
  openResetDlg(false);
  toast(
    data
      ? 'Event cleared: name, watch areas, venue, floor plan, board positions, demo spot and timeline. Setup starts again from step 1.'
      : 'Setup checklist reset: it starts again from step 1. Venue, areas and hardware were not touched.',
    'ok',
  );
});

// ---------------------------------------------------------------------------
// settings: this console's event name and operator name (the top-bar buttons
// for sound and theme have twins here, wired where they are defined)
// ---------------------------------------------------------------------------

const setEvent = $('setEvent') as HTMLInputElement;
const setOperator = $('setOperator') as HTMLInputElement;
function flashSaved(text = 'Saved ✓') {
  const el = $('setSaved');
  el.textContent = text;
  animate(el, { opacity: [0, 1] }, { duration: 0.2 });
  window.clearTimeout(Number(el.dataset.t));
  el.dataset.t = String(window.setTimeout(() => animate(el, { opacity: 0 }, { duration: 0.4 }), 2500));
}
setEvent.addEventListener('change', () => {
  const v = setEvent.value.trim();
  if (!v) return (setEvent.value = eventInput.value);
  setEventName(v);
  refreshSetup();
  flashSaved();
});
setEvent.addEventListener('keydown', (e) => e.key === 'Enter' && setEvent.blur());
setOperator.addEventListener('change', () => {
  const v = setOperator.value.trim();
  if (v) lsSet('pulse.operator', v);
  else lsDel('pulse.operator');
  flashSaved(v ? `Saved ✓ The log will say “by ${v}”.` : 'Saved ✓ Acknowledgements stay anonymous.');
});
setOperator.addEventListener('keydown', (e) => e.key === 'Enter' && setOperator.blur());
onPage((p) => {
  if (p !== 'settings') return;
  setEvent.value = eventInput.value;
  setOperator.value = operator();
});

// Last: runs immediately, so everything it touches must already exist.
let lastPage = '';
onPage((p) => {
  mesh.showBoards = p === 'hardware';
  areas.cancelPick(); // a "click the map" request does not follow you to another page
  // Each page starts with the whole venue in view (a pan on one page cut off the stage on the next).
  if (p !== lastPage && lastPage) mesh.resetView();
  lastPage = p;
  if (p === 'home') renderGreeting();
  if (p === 'venue') renderTemplates();
  refreshSetup();
});

// ---------------------------------------------------------------------------
// why did it fire? — the evidence behind a red link
// ---------------------------------------------------------------------------

let explainPair: [string, string] | null = null;
let explainTimer = 0;

areas.onLink = (from, to) => void openExplain([from, to]);
$('exClose').addEventListener('click', () => closeExplain());
window.addEventListener('keydown', (e) => e.key === 'Escape' && explainPair && closeExplain());

function closeExplain() {
  window.clearInterval(explainTimer);
  explainPair = null;
  $('explain').hidden = true;
}

/**
 * The pair of phones that best shows why a push alert fired: the strongest
 * push travelling right now in (or into) the alert's zone, between real
 * named phones if there is one; with no push at the moment, a neighbour
 * pair in that zone (the panel then says which check it fails now).
 */
function evidenceFor(a: Alert): [string, string] | null {
  if (!snap) return null;
  const moving = a.cause === 'together' ? table.evidence(snap, a.zone) : null; // table demo: a pair in the group
  if (moving) return moving;
  const zone = (id: string) => nodesById.get(id)?.zone;
  const named = (p: [string, string]) => (nodesById.get(p[0])?.name ? 1 : 0) + (nodesById.get(p[1])?.name ? 1 : 0);
  const inZone = (p: [string, string]) => (zone(p[0]) === a.zone ? 1 : 0) + (zone(p[1]) === a.zone ? 1 : 0);
  const waves = [...snap.waves].sort(
    (x, y) => inZone([y.from, y.to]) - inZone([x.from, x.to]) || named([y.from, y.to]) - named([x.from, x.to]) || y.corr - x.corr,
  );
  if (waves.length && inZone([waves[0].from, waves[0].to]) > 0) return [waves[0].from, waves[0].to];
  const links = (snap.links ?? []).filter((l) => inZone(l) === 2).sort((x, y) => named(y) - named(x));
  if (links.length) return links[0];
  return waves.length ? [waves[0].from, waves[0].to] : null;
}

async function openExplain(pair: [string, string]) {
  openDrawer(null);
  explainPair = pair;
  const el = $('explain');
  if (el.hidden) {
    el.hidden = false;
    animate(el, { opacity: [0, 1], x: [40, 0] }, { type: 'spring', bounce: 0.25, duration: 0.45 });
  }
  window.clearInterval(explainTimer);
  await refreshExplain();
  explainTimer = window.setInterval(() => void refreshExplain(), 1000);
}

async function refreshExplain() {
  const pair = explainPair;
  if (!pair) return;
  let e: EdgeExplain;
  try {
    const r = await fetch(`/api/edge?from=${encodeURIComponent(pair[0])}&to=${encodeURIComponent(pair[1])}`);
    if (r.status === 404) {
      $('exSummary').textContent = 'These two phones are no longer neighbours (one moved or went offline).';
      return;
    }
    if (!r.ok) throw new Error(r.statusText);
    e = (await r.json()) as EdgeExplain;
  } catch (err) {
    $('exSummary').textContent = `Couldn't load the evidence: ${(err as Error).message}`;
    return;
  }
  if (explainPair !== pair) return;
  $('exPair').innerHTML = `${whoHTML(e.from)} → ${whoHTML(e.to)}`;
  $('exA').textContent = who(e.from);
  $('exB').textContent = who(e.to);
  const v = $('exVerdict');
  v.className = `st ${e.wave ? 'wave' : 'ok'}`;
  v.textContent = e.wave ? 'Push detected' : 'Not a push';
  const failed = e.checks.filter((c) => !c.pass);
  // Table demo rows: a two-phone push (yellow at most) and moving as one (yellow, not a push).
  const pairPush = e.wave && e.checks.some((c) => c.pass && c.name.startsWith('Two-phone push'));
  const asOne = !e.wave && e.checks.some((c) => c.pass && c.name.startsWith('Moving as one'));
  if (pairPush || asOne) {
    v.className = 'st swaying';
    v.textContent = pairPush ? 'Push between two people' : 'Moving as one';
  }
  $('exSummary').textContent = asOne
    ? `${who(e.from)} and ${who(e.to)} move as one, at most a few hundred ms apart, not to a beat: what people pressed together feel like (or people rocking together by choice). Shown as yellow, never red.`
    : pairPush
      ? `${who(e.to)} repeats ${who(e.from)}'s motion ${Math.abs(e.lagMs)} ms later (similarity ${e.peak.toFixed(2)}): a push from one to the other. With only two phones there is no chain through a crowd, so it shows as yellow, never red.`
      : e.wave
        ? `${who(e.to)} repeats ${who(e.from)}'s motion ${Math.abs(e.lagMs)} ms later (similarity ${e.peak.toFixed(2)}): a push passing from one person to the next.`
        : `Not counted as a push: ${failed.map((c) => c.name.toLowerCase()).join(', ') || 'below the thresholds'}.`;
  $('exWin').textContent = `${((e.a.length * e.stepMs) / 1000).toFixed(0)} s`;
  $('exPeak').textContent = `peak ${e.peak.toFixed(2)} at ${e.lagMs} ms`;
  drawTraces(e);
  drawCorr(e);
  $('exChecks').replaceChildren(
    ...e.checks.map((c) => {
      const li = document.createElement('li');
      li.innerHTML = `<span class="${c.pass ? 'ok' : 'no'}">${c.pass ? '✓' : '✗'}</span><span>${esc(c.name)}<small>${esc(c.detail)}</small></span>`;
      return li;
    }),
  );
}

function setupCanvas(id: string) {
  const c = $(id) as HTMLCanvasElement;
  const dpr = Math.min(2, window.devicePixelRatio || 1);
  const w = c.clientWidth, h = c.clientHeight;
  if (c.width !== Math.round(w * dpr)) {
    c.width = Math.round(w * dpr);
    c.height = Math.round(h * dpr);
  }
  const g = c.getContext('2d')!;
  g.setTransform(dpr, 0, 0, dpr, 0, 0);
  g.clearRect(0, 0, w, h);
  return { g, w, h, css: getComputedStyle(document.documentElement) };
}

function drawTraces(e: EdgeExplain) {
  const { g, w, h, css } = setupCanvas('exTrace');
  const all = [...e.a, ...e.b].filter((v): v is number => v != null);
  const span = Math.max(0.2, ...all.map(Math.abs));
  g.strokeStyle = css.getPropertyValue('--line').trim();
  g.beginPath();
  g.moveTo(0, h / 2);
  g.lineTo(w, h / 2);
  g.stroke();
  const line = (vals: (number | null)[], color: string) => {
    g.strokeStyle = color;
    g.lineWidth = 1.6;
    g.beginPath();
    let pen = false;
    vals.forEach((v, i) => {
      if (v == null) {
        pen = false;
        return;
      }
      const x = (i / Math.max(1, vals.length - 1)) * w;
      const y = h / 2 - (v / span) * (h / 2 - 3);
      if (pen) g.lineTo(x, y);
      else g.moveTo(x, y);
      pen = true;
    });
    g.stroke();
  };
  line(e.a, css.getPropertyValue('--ok').trim());
  line(e.b, css.getPropertyValue('--handling').trim());
}

function drawCorr(e: EdgeExplain) {
  const { g, w, h, css } = setupCanvas('exCorr');
  const lo = e.lags[0] ?? -1500, hi = e.lags[e.lags.length - 1] ?? 1500;
  const X = (ms: number) => ((ms - lo) / (hi - lo || 1)) * w;
  const Y = (r: number) => h - 14 - r * (h - 22);
  // The "wave" window: 120–1200 ms either way; inside ±120 ms = moving together.
  g.fillStyle = css.getPropertyValue('--warn-soft').trim();
  g.fillRect(X(-120), 0, X(120) - X(-120), h - 14);
  g.fillStyle = css.getPropertyValue('--muted').trim();
  g.font = '10px Inter, system-ui, sans-serif';
  g.textAlign = 'center';
  g.fillText('together', X(0), 10);
  for (const ms of [-1000, -500, 0, 500, 1000]) if (ms >= lo && ms <= hi) g.fillText(String(ms), X(ms), h - 2);
  g.strokeStyle = css.getPropertyValue('--line').trim();
  g.setLineDash([3, 4]);
  g.beginPath();
  g.moveTo(0, Y(0.6));
  g.lineTo(w, Y(0.6));
  g.stroke();
  g.setLineDash([]);
  g.strokeStyle = css.getPropertyValue(e.wave ? '--wave' : '--fg-2').trim();
  g.lineWidth = 1.8;
  g.beginPath();
  let pen = false;
  e.lags.forEach((ms, i) => {
    const r = e.corr[i];
    if (r == null) {
      pen = false;
      return;
    }
    if (pen) g.lineTo(X(ms), Y(r));
    else g.moveTo(X(ms), Y(r));
    pen = true;
  });
  g.stroke();
  g.fillStyle = g.strokeStyle;
  g.beginPath();
  g.arc(X(e.lagMs), Y(e.peak), 4, 0, Math.PI * 2);
  g.fill();
  g.textAlign = 'left';
}

// ---------------------------------------------------------------------------
// detector evaluation (docs/eval.json via /api/eval)
// ---------------------------------------------------------------------------

async function loadEval() {
  let rep: EvalReport;
  try {
    const r = await fetch('/api/eval');
    if (!r.ok) return;
    rep = (await r.json()) as EvalReport;
  } catch {
    return;
  }
  const s = rep.summary;
  $('evalMeta').textContent = `${rep.seeds} random crowds per scenario · ${new Date(rep.generated).toLocaleDateString()}`;
  const red = rep.rows.filter((r) => r.expect === 'red' && r.medianRedS != null).map((r) => r.medianRedS!);
  const med = red.length ? red.sort((a, b) => a - b)[Math.floor(red.length / 2)] : null;
  $('evalSummary').innerHTML =
    `<div><b>${s.falseAlarms}</b><span>false alarms in ${s.lookAlikeRuns} look-alike runs</span></div>` +
    `<div><b>${s.positiveRuns - s.missed}/${s.positiveRuns}</b><span>real pushes caught</span></div>` +
    `<div><b>${med != null ? `${med.toFixed(0)} s` : '–'}</b><span>median time to red</span></div>`;
  const tb = $('evalTable').querySelector('tbody')!;
  tb.replaceChildren(
    ...rep.rows.map((r) => {
      const tr = document.createElement('tr');
      const bad = (r.expect === 'calm' && r.red > 0) || (r.expect === 'red' && r.red < r.runs);
      tr.className = bad ? 'bad' : '';
      tr.innerHTML =
        `<td>${esc(r.scenario)}</td><td>${r.layout}</td><td>${r.expect === 'yellow-ok' ? 'calm/yellow' : r.expect}</td>` +
        `<td>${r.red}</td><td>${r.yellow}</td><td>${r.calm}</td><td>${r.medianRedS != null ? `${r.medianRedS.toFixed(0)} s` : r.medianLeadS != null ? `lead ${r.medianLeadS.toFixed(0)} s` : '–'}</td>`;
      if (r.note) tr.title = r.note;
      return tr;
    }),
  );
  $('evalTable').hidden = false;
}
void loadEval();

// ---------------------------------------------------------------------------
// the judge demo: demo spot, dragging real phones, surge around the phones
// ---------------------------------------------------------------------------

const meshNet = initMeshNet({ mesh, toast, nameOf: who, selected: () => drawerId });

const demo = initDemo({
  mesh,
  areas,
  mode: () => lastMode || 'live',
  toast,
  pickOnMap: (label, done) => pickOnMap(label, false, (p) => done({ x: p.x, y: p.y })),
  nameOf: who,
  goLive,
});

initEscalation((text, kind) => toast(text, kind));
initHwSetup({ toast, refresh: () => void loadHardware() }); // Hardware page: table demo + board readiness (hwsetup.ts)
