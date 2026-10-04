import './style.css';
import { onPage, page } from './shell';
import type { Alert, AlertRules, Cluster, Config, EdgeExplain, EvalReport, FloorplanSuggestion, Hardware, Level, Node, NodeDetail, SimAction, SimState, Snapshot, ToDash, Venue } from '../../shared/protocol';
import { wsURL } from '../../shared/protocol';
import { animate } from 'motion';
import { Areas, type Tool } from './areas';
import { Mesh } from './mesh';

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
}
let timeline: LogItem[] = [];
let soundOn = false;
let lastBrief: Alert | null = null;

// ---------------------------------------------------------------------------
// mesh view
// ---------------------------------------------------------------------------

const mesh = new Mesh($('mesh') as HTMLCanvasElement);

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
const areaLevelText = { calm: 'calm', watch: 'watch', danger: 'danger' } as const;

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
      li.innerHTML =
        `<span class="swatch"></span>` +
        `<input class="name" maxlength="32" aria-label="Area name" />` +
        `<span class="lvl"></span>` +
        `<button class="sens sm" data-tip="High-risk areas alert on the first push"></button>` +
        `<select class="light sm" aria-label="Zone light for this area" data-tip="Which zone light shows this area"></select>` +
        `<button class="del sm ghost" data-tip="Delete area" aria-label="Delete area">✕</button>` +
        `<div class="meta"></div>` +
        `<div class="rules-wrap" style="grid-area: rules"><button class="rules-toggle">Alert rules ▾</button><div class="rules" hidden></div></div>`;
      const id = a.id;
      const name = li.querySelector<HTMLInputElement>('.name')!;
      name.value = a.name;
      name.addEventListener('change', () => areas.rename(id, name.value));
      name.addEventListener('keydown', (e) => e.key === 'Enter' && name.blur());
      li.querySelector('.sens')!.addEventListener('click', () => areas.toggleSens(id));
      li.querySelector<HTMLSelectElement>('.light')!.addEventListener('change', (e) =>
        areas.setLight(id, (e.target as HTMLSelectElement).value),
      );
      li.querySelector('.del')!.addEventListener('click', () => areas.remove(id));
      const rulesBox = li.querySelector<HTMLElement>('.rules')!;
      li.querySelector('.rules-toggle')!.addEventListener('click', (e) => {
        e.stopPropagation();
        rulesBox.hidden = !rulesBox.hidden;
        (e.target as HTMLElement).textContent = rulesBox.hidden ? 'Alert rules ▾' : 'Alert rules ▴';
        if (!rulesBox.hidden && !rulesBox.childElementCount) buildRules(rulesBox, id);
      });
      li.addEventListener('click', (e) => {
        if (!(e.target as HTMLElement).closest('button, input, select, .rules')) areas.select(id);
      });
      ul.append(li);
      areaEls.set(a.id, li);
      animate(li, { opacity: [0, 1], y: [12, 0], scale: [0.96, 1] }, { type: 'spring', bounce: 0.4, duration: 0.5 });
      if (areas.selected === a.id) setTimeout(() => name.select(), 50);
    }
    const prev = li.dataset.level;
    li.className = `area ${a.level}${areas.selected === a.id ? ' sel' : ''}`;
    li.dataset.level = a.level;
    li.querySelector<HTMLElement>('.swatch')!.style.background = a.color;
    li.querySelector('.lvl')!.textContent = areaLevelText[a.level];
    const sens = li.querySelector<HTMLButtonElement>('.sens')!;
    sens.textContent = a.sens === 'high' ? '⚑ High risk' : 'Normal';
    sens.classList.toggle('hi', a.sens === 'high');
    fillLightPicker(li.querySelector<HTMLSelectElement>('.light')!, a.light ?? '');
    li.querySelector('.meta')!.textContent =
      a.phones === 0
        ? 'No phones inside yet'
        : `${a.phones} phone${a.phones === 1 ? '' : 's'}` +
          (a.wave ? ` · ${a.wave} in a push` : '') +
          (a.sway ? ` · ${a.sway} swaying` : '');
    if (prev && prev !== a.level) animate(li, { scale: [1.04, 1] }, { type: 'spring', bounce: 0.5, duration: 0.5 });
  }
};
areas.onChange();
areas.onError = (m) => toast(m, 'error');

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

function renderStatus(zone: Level, phones: number, clusters: Cluster[]) {
  const danger = areas.list.filter((a) => a.level === 'danger');
  const watch = areas.list.filter((a) => a.level === 'watch');
  const packed = clusters.filter((c) => c.level === 'red');
  const tight = clusters.filter((c) => c.level === 'yellow');
  let cls: 'calm' | 'yellow' | 'red' = 'calm';
  let text = phones === 0 ? 'Waiting for attendees' : 'All clear';
  const active =
    danger.length + watch.length + packed.length + tight.length +
    (zone !== 'calm' && !danger.length && !watch.length ? 1 : 0);
  $('kAlerts').textContent = String(active);
  $('kAlerts').parentElement!.classList.toggle('hot', active > 0);
  $('kAlertsSub').textContent =
    danger.length || watch.length
      ? `${danger.length} danger · ${watch.length} watch area${watch.length === 1 ? '' : 's'}`
      : active
        ? `crowd-wide ${zone === 'red' ? 'danger' : 'warning'}`
        : 'nothing needs attention';
  const densest = clusters.reduce((m, c) => Math.max(m, c.density), 0);
  $('kDense').textContent = clusters.length ? densest.toFixed(1) : '–';
  $('kDenseSub').textContent = clusters.length
    ? `people/m² · ${clusters.length} crowd${clusters.length === 1 ? '' : 's'}${clusters.some((c) => c.trend === 'forming') ? ', one forming' : ''}`
    : 'no crowds packed together';
  $('kDense').parentElement!.classList.toggle('hot', packed.length > 0);
  if (tight.length) {
    cls = 'yellow';
    const soon = tight.filter((c) => c.eta != null).sort((a, b) => a.eta! - b.eta!)[0];
    text = soon
      ? `Crowd packing fast: dangerous in ~${Math.round(soon.eta!)} s`
      : `Crowd packing tighter (${tight[0].density.toFixed(1)} people/m²)`;
  }
  if (zone === 'yellow' || watch.length) {
    cls = 'yellow';
    text = watch.length ? `Pressure building in ${names(watch)}` : 'Pressure building in the crowd';
  }
  if (packed.length) {
    cls = 'red';
    text = `Danger: crowd too dense (${packed[0].density.toFixed(1)} people/m²)`;
  }
  if (zone === 'red' || danger.length) {
    cls = 'red';
    text = danger.length ? `Danger: push travelling through ${names(danger)}` : 'Danger: push travelling through the crowd';
  }
  const el = $('status');
  if (!el.classList.contains(cls)) {
    el.className = `status ${cls}`;
    animate(el, { scale: [1.08, 1] }, { type: 'spring', bounce: 0.5, duration: 0.6 });
  }
  $('statusText').textContent = text;
}

function names(list: { name: string }[]) {
  const n = list.map((a) => a.name);
  return n.length <= 2 ? n.join(' and ') : `${n.slice(0, 2).join(', ')} +${n.length - 2}`;
}

// ---------------------------------------------------------------------------
// toasts
// ---------------------------------------------------------------------------

function toast(text: string, kind: 'info' | 'watch' | 'danger' | 'error' = 'info') {
  const el = document.createElement('div');
  el.className = `toast ${kind}`;
  el.textContent = text;
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
    `<div class="tt-head"><b>${esc(n.id.slice(0, 8))}</b><span class="st ${n.status}">${statusText[n.status]}</span></div>` +
    `<div class="tt-ua">${esc(deviceName(n.ua))} · ${where(n)}</div>` +
    `<div class="tt-foot">Click for live telemetry</div>`;
  tip.hidden = false;
  const w = $('mesh').clientWidth;
  tip.style.left = `${Math.min(p.x + 22, w - 230)}px`;
  tip.style.top = `${Math.max(8, p.y - 20)}px`;
}

const statusText: Record<Node['status'], string> = {
  ok: 'Calm',
  handling: 'In hand',
  swaying: 'Swaying',
  wave: 'In a push',
  connecting: 'Connecting',
  stale: 'Offline',
};

/** How the phone's position is known. */
function where(n: Node) {
  if (n.outside) return 'outside the venue';
  return n.src === 'gps' || (n.acc ?? 0) > 0 ? `GPS ±${Math.round(n.acc ?? 0)} m` : 'placed on map';
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
  $('dAvatar').style.background = color;
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
  const s = d?.samples ?? [];
  chart('chX', s.map((p) => p.ax), color, 'vX', 'm/s²');
  chart('chZ', s.map((p) => p.az), color, 'vZ', 'm/s²');
  chart('chY', s.map((p) => p.ay), color, 'vY', 'm/s²');
  chart('chR', s.map((p) => p.rot), color, 'vR', '°/s');
}

function explain(n?: Node): string {
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
  try {
    localStorage.setItem('pulse.theme', t);
  } catch {
    /* fine */
  }
}
applyTheme(document.documentElement.dataset.theme === 'light' ? 'light' : 'dark');
$('themeBtn').addEventListener('click', () =>
  applyTheme(document.documentElement.dataset.theme === 'light' ? 'dark' : 'light'),
);

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

function renderRisk(level: Level, score: number, waves: number) {
  targetScore = score;
  $('riskPanel').className = `card risk ${level}`;
  $('kRisk').textContent = String(Math.round(score * 100));
  $('kRiskSub').textContent = `of 100 · ${levelText[level].toLowerCase()}`;
  $('riskLevel').textContent = levelText[level];
  $('riskSub').textContent =
    waves === 0
      ? 'No pushes travelling through the crowd.'
      : `A push is passing between ${waves} pair${waves === 1 ? '' : 's'} of neighbours.`;
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
  $('riskNum').textContent = String(Math.round(shownScore * 100));
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
  if (timeline.length > 60) timeline.shift();
  renderLog(fresh);
}

/** Zone-light letters reported by the server (A, B, …), for the area light pickers. */
let lightKeys: string[] = [];

function fillLightPicker(sel: HTMLSelectElement, current: string) {
  const keys = [...new Set([...lightKeys, ...(current ? [current] : [])])].sort();
  const want = ['', ...keys].join('|');
  if (sel.dataset.keys !== want) {
    sel.dataset.keys = want;
    sel.replaceChildren(new Option('No light', ''), ...keys.map((k) => new Option(`Light ${k}`, k)));
  }
  sel.value = current;
  sel.hidden = keys.length === 0;
}

function renderLog(fresh = false) {
  const ol = $('log');
  $('logEmpty').hidden = timeline.length > 0;
  ol.replaceChildren(
    ...timeline
      .slice()
      .reverse()
      .map((e, i) => {
        const li = document.createElement('li');
        li.className = `lv-${e.level}`;
        if (fresh && i === 0) li.classList.add('new');
        li.innerHTML =
          `<time>${fmtTime(e.t)}</time><span class="lv ${e.level}">${levelText[e.level]}</span>` +
          `<span class="txt">${e.test ? '<span class="tag">TEST</span>' : ''}${e.area ? '<span class="tag area">AREA</span>' : ''}${esc(e.text)}</span>`;
        return li;
      }),
  );
}

function showBrief(a: Alert, fresh = false) {
  lastBrief = a;
  const el = $('brief');
  el.classList.remove('muted');
  $('briefMeta').textContent = `· ${fmtTime(a.t)}${a.test ? ' · test' : ''}`;
  $('briefPanel').classList.toggle('red', a.level === 'red');
  ($('replayAudioBtn') as HTMLButtonElement).disabled = false;
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

$('soundBtn').addEventListener('click', () => {
  soundOn = !soundOn;
  $('soundBtn').dataset.tip = soundOn ? 'Spoken alerts on' : 'Turn on spoken alerts';
  $('soundBtn').classList.toggle('on', soundOn);
  toast(soundOn ? 'Spoken alerts on' : 'Spoken alerts off');
  if (soundOn) {
    audioCtx ??= new AudioContext();
    void audioCtx.resume();
    speechSynthesis?.speak(new SpeechSynthesisUtterance(''));
  } else {
    speechSynthesis?.cancel();
  }
});

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
  snap = s;
  nodesById.clear();
  for (const n of s.nodes) nodesById.set(n.id, n);

  const risk = crowdRisk(s);
  if (s.t - lastHistAt >= 1000) {
    lastHistAt = s.t;
    riskHist.push(risk.score);
    if (riskHist.length > 60) riskHist.shift();
  }

  zoneNames.clear();
  for (const z of s.zones) zoneNames.set(z.id, z.name);
  const clusters = s.clusters ?? [];
  mesh.update(s.nodes, s.waves, s.links ?? [], clusters, s.venue ?? { w: 24, h: 16 });
  areas.sync(s.zones, s.nodes);
  const worstArea = areas.worst();
  mesh.level = worstArea === 'danger' ? 'red' : risk.level === 'red' ? 'red' : worstArea === 'watch' ? 'yellow' : risk.level;
  renderRisk(risk.level, risk.score, s.waves.length);
  renderStatus(risk.level, s.stats.phones, clusters);
  renderTooltip();

  setCounter('cPhones', s.stats.phones);
  setCounter('cRate', Math.round(s.stats.msgPerSec));
  setCounter('cRtt', s.stats.medianRtt || null);
  setCounter('cWaves', s.waves.length);
  $('cDetect').textContent = s.stats.detectMs != null ? s.stats.detectMs.toFixed(2) : '–';
  $('cSnap').textContent = s.stats.snapshotBytes != null ? (s.stats.snapshotBytes / 1024).toFixed(1) : '–';

  const replay = s.mode === 'replay';
  const simulating = s.mode === 'sim';
  const badge = $('mode');
  badge.textContent = replay ? 'REPLAY' : simulating ? 'SIMULATION' : 'LIVE';
  badge.className = `badge ${replay ? 'replay' : simulating ? 'sim' : 'live'}`;
  $('replayBanner').hidden = !replay && !simulating;
  $('replayBanner').classList.toggle('sim', simulating);
  if (replay) {
    $('rbTag').textContent = 'REPLAY';
    $('replayInfo').textContent = `${s.replay ?? ''} · ${Math.round((s.progress ?? 0) * 100)}%`;
    $('rbNote').textContent = 'Not live: a saved run is playing through the detector.';
  } else if (simulating) {
    $('rbTag').textContent = 'SIMULATION';
    $('replayInfo').textContent = `${simState?.people ?? s.sim?.bodies.length ?? 0} people · ${actionText[s.sim?.action ?? ''] ?? s.sim?.action ?? ''}`;
    $('rbNote').textContent = 'Not live: a virtual crowd is feeding the detector.';
  }
  mesh.setSim(simulating ? (s.sim ?? null) : null, simulating ? simState : null);
  const mode = s.mode;
  if (mode !== lastMode) {
    lastMode = mode;
    $('liveBtn').classList.toggle('on', mode === 'live');
    $('replayBtn').classList.toggle('on', mode === 'replay');
    $('simBtn').classList.toggle('on', mode === 'sim');
    $('replayOpts').hidden = mode !== 'replay';
    renderSimRunning(mode === 'sim');
  }

  $('recBadge').hidden = !s.recording;
  const recBtn = $('recBtn');
  recBtn.textContent = s.recording ? `■ Stop and save "${s.recording}"` : '● Start recording';
  recBtn.classList.toggle('on', !!s.recording);
  if (drawerId && !nodesById.has(drawerId)) openDrawer(null);

  updateQR();
}

function onAlert(a: Alert, fresh: boolean) {
  if (a.id && alertsById.has(a.id)) {
    // An update to an alert we already have (briefing arrived, acknowledged, resolved, escalated).
    const prev = alertsById.get(a.id)!;
    alertsById.set(a.id, { ...prev, ...a });
    renderAlertCards();
    if (fresh && a.escalated && !prev.escalated) {
      toast(`Escalated: ${zoneNames.get(a.zone) ?? a.zone} still unacknowledged`, 'danger');
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
  const where = zoneNames.get(a.zone) ?? a.zone;
  const fallback = a.kind === 'density' ? `${where}: crowd too dense.` : `${where}: crowd risk ${Math.round(a.score * 100)}.`;
  logEntry({ t: a.t, level: a.level, text: a.brief ?? fallback, test: a.test, area: a.kind === 'density' ? 'DENSITY' : undefined }, fresh);
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
      timeline = timeline.filter((e) => e.area);
      for (const a of msg.alerts) onAlert(a, false);
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

function msg(text: string, isErr = false) {
  const el = $('ctlMsg');
  el.textContent = text;
  el.style.color = isErr ? 'var(--wave)' : '';
  toast(text, isErr ? 'error' : 'info');
  window.clearTimeout((msg as unknown as { t?: number }).t);
  (msg as unknown as { t?: number }).t = window.setTimeout(() => (el.textContent = ''), 5000);
}

async function post<T = Record<string, unknown>>(path: string, body?: unknown): Promise<T> {
  const r = await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const j = (await r.json().catch(() => ({}))) as T & { error?: string };
  if (!r.ok) throw new Error(j.error ?? r.statusText);
  return j;
}

async function loadRecordings(select?: string) {
  try {
    const r = await fetch('/api/recordings');
    const j = (await r.json()) as { files: { name: string }[]; runs: { label: string }[] };
    const sel = $('recSelect') as HTMLSelectElement;
    const prev = select ?? sel.value;
    sel.replaceChildren();
    const opts = [...j.files.map((f) => f.name), ...j.runs.map((r) => `db:${r.label}`)];
    if (!opts.length) {
      const o = new Option('no recordings yet', '');
      o.disabled = true;
      sel.append(o);
    }
    for (const name of opts) sel.append(new Option(name, name));
    const pick = opts.includes(prev) ? prev : (opts.find((o) => /wave/.test(o) && !o.startsWith('auto/')) ?? opts[0]);
    if (pick) sel.value = pick;
  } catch {
    /* server restarting */
  }
}

let lastMode: Snapshot['mode'] | '' = '';

async function goLive() {
  try {
    await post('/api/live');
    if (lastMode === 'sim') await fetch('/api/sim/stop', { method: 'POST' }).catch(() => {});
    $('replayOpts').hidden = true;
    $('liveBtn').classList.add('on');
    $('replayBtn').classList.remove('on');
    $('simBtn').classList.remove('on');
    msg('Showing live phones');
  } catch (e) {
    msg((e as Error).message, true);
  }
}
$('liveBtn').addEventListener('click', () => void goLive());
$('backLiveBtn').addEventListener('click', () => void goLive());
// Choosing Replay only opens the picker; nothing changes until Play.
$('replayBtn').addEventListener('click', () => {
  $('replayOpts').hidden = false;
  $('liveBtn').classList.remove('on');
  $('simBtn').classList.remove('on');
  $('replayBtn').classList.add('on');
  animate($('replayOpts'), { opacity: [0, 1], y: [-6, 0] }, { duration: 0.25 });
});
$('replayPlayBtn').addEventListener('click', async () => {
  const name = ($('recSelect') as HTMLSelectElement).value;
  if (!name) return msg('Pick a recording first', true);
  const speed = Number(($('speedSelect') as HTMLSelectElement).value);
  try {
    await post('/api/replay', { name, speed });
    msg(`Replaying ${name}`);
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
      const label = ($('recLabel') as HTMLInputElement).value.trim() || 'run';
      await post('/api/record/start', { label });
      msg(`Recording "${label}"`);
    }
  } catch (e) {
    msg((e as Error).message, true);
  }
});
$('testBtn').addEventListener('click', async () => {
  try {
    const r = await post<{ zone: string }>('/api/test-alert');
    msg(`Test alert sent (${r.zone})`);
  } catch (e) {
    msg((e as Error).message, true);
  }
});
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
let qrMode: 'auto' | 'shown' | 'hidden' = 'auto';
let onLivePage = false;
onPage((p) => {
  onLivePage = p === 'live';
  if (p !== 'live' && qrMode === 'shown') qrMode = 'auto';
  updateQR();
});
$('qrBtn').addEventListener('click', () => {
  qrMode = $('qr').hidden ? 'shown' : 'hidden';
  updateQR();
});
$('qr').addEventListener('click', () => {
  qrMode = 'hidden';
  updateQR();
});
function updateQR() {
  const show = qrMode === 'shown' || (qrMode === 'auto' && onLivePage && snap?.mode === 'live' && snap.stats.phones === 0);
  $('qr').hidden = !show;
}

async function init() {
  try {
    const cfg = (await (await fetch('/api/config')).json()) as Config;
    if (cfg.yellow && cfg.red) thresholds = { yellow: cfg.yellow, red: cfg.red };
    setThresholdLines();
  } catch {
    /* defaults */
  }
  try {
    const st = (await (await fetch('/api/status')).json()) as Record<string, boolean>;
    const names: Record<string, string> = { tiger: 'Tiger Data', gemini: 'Gemini', elevenlabs: 'ElevenLabs', sign: 'Sign' };
    $('services').replaceChildren(
      ...Object.entries(names).map(([k, label]) => {
        const s = document.createElement('span');
        s.className = `svc ${st[k] ? 'on' : ''}`;
        s.textContent = label;
        s.title = st[k] ? 'connected' : 'not configured: falling back';
        return s;
      }),
    );
  } catch {
    /* ignore */
  }
  ($('qrImg') as HTMLImageElement).src = '/api/qr.png';
  fetch('/api/phone-url')
    .then((r) => r.text())
    .then((u) => ($('qrUrl').textContent = u))
    .catch(() => {});
  await loadRecordings();
  await areas.load();
  await loadVenue();
  connect();
}

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
  renderTemplates();
  loadPlanImage();
  mesh.setLayout(venue.layout ?? null);
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

async function saveVenue(next: Venue) {
  try {
    const r = await fetch('/api/venue', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(next),
    });
    const j = (await r.json().catch(() => ({}))) as Venue & { error?: string };
    if (!r.ok) throw new Error(j.error ?? r.statusText);
    venue = j;
    renderVenue();
    toast('Venue saved');
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

$('vSave').addEventListener('click', () => void saveVenue(venueForm()));

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
      void saveVenue({ ...v, lat: lat0, lon: lon0, geo: true }).then(() =>
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
  hwOnline = list.filter((h) => h.online).length;
  mesh.setBoards(list);
  areas.onChange();
  $('hwEmpty').hidden = list.length > 0;
  const on = list.filter((h) => h.online).length;
  $('hwSummary').textContent = list.length ? `${on} of ${list.length} online` : '';
  const heard = list.reduce((n, h) => n + (h.ble?.devices ?? 0), 0);
  $('hw').replaceChildren(
    ...list.map((h) => {
      const li = document.createElement('li');
      li.className = `hw-row ${h.online ? 'on' : 'off'}`;
      const host = h.url.replace(/^https?:\/\//, '');
      const status = h.online
        ? `${bars(h.rssi)}${h.uptime ? `<span class="muted">up ${ago2(h.uptime * 1000)}</span>` : ''}`
        : `<span class="off-text">offline${h.seenAgo != null ? ` · last answered ${ago2(h.seenAgo * 1000)} ago` : ' · not answering'}</span>`;
      const lvl = h.online && h.level ? `<span class="st ${h.level === 'red' ? 'wave' : h.level === 'yellow' ? 'swaying' : 'ok'}">${esc(h.level)}</span>` : '';
      const ble = h.ble
        ? `<div class="hw-ble">📶 Bluetooth: <b>${h.ble.devices}</b> devices nearby · ${h.ble.near} close</div>`
        : '';
      const peers = h.peers?.length
        ? `<div class="hw-peers">Hears ${h.peers
            .map((p) => `<b>${esc(p.name)}</b> ≈${p.dist.toFixed(1)} m${p.mapDist != null ? ` (${p.mapDist.toFixed(1)} m on the map)` : ''}`)
            .join(', ')}</div>`
        : '';
      const shows = h.zone
        ? `<div class="hw-areas">${h.areas?.length ? `Shows ${h.areas.map(esc).join(', ')}` : 'No area assigned yet: pick “Light ' + esc(h.zone) + '” on a watch area'}</div>`
        : '<div class="hw-areas">Shows the worst alert anywhere</div>';
      li.innerHTML =
        `<span class="hw-dot"></span><div class="hw-main"><div class="hw-top"><b>${esc(h.name)}</b>${lvl}</div>` +
        `<div class="hw-sub"><span class="mono">${esc(host)}</span>${status}</div>${ble}${peers}${shows}</div>`;
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
};

for (const [id, out, fmt] of [
  ['simPeople', 'simPeopleV', (v: string) => v],
  ['simPart', 'simPartV', (v: string) => `${v}%`],
] as const) {
  const input = $(id) as HTMLInputElement;
  const show = () => ($(out).textContent = fmt(input.value));
  input.addEventListener('input', show);
  show();
}


function renderSimRunning(running: boolean) {
  $('simStart').hidden = running;
  $('simStop').hidden = !running;
  $('simControls').hidden = !running;
}

$('simStart').addEventListener('click', async () => {
  const people = Number(($('simPeople') as HTMLInputElement).value);
  const participation = Number(($('simPart') as HTMLInputElement).value) / 100;
  try {
    await post('/api/sim/start', { people, participation, scenario: 'concert' });
    msg(`Simulating ${people} people`);
    renderSimRunning(true);
    void pollSim();
  } catch (e) {
    msg((e as Error).message, true);
  }
});

$('simStop').addEventListener('click', () => void goLive());

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

for (const b of document.querySelectorAll<HTMLButtonElement>('[data-sim]')) {
  b.addEventListener('click', () => {
    const type = b.dataset.sim!;
    const strength = Number(($('simStrength') as HTMLInputElement).value) / 100;
    switch (type) {
      case 'calm':
      case 'stage':
      case 'disperse':
      case 'dance':
      case 'intermission':
        void simAction({ type });
        break;
      case 'surge':
        void simAction({ type: 'surge', strength });
        break;
      case 'attract':
        pickOnMap('Click where the group should gather', false, (p) => void simAction({ type: 'attract', x: p.x, y: p.y }));
        break;
      case 'spawn':
        pickOnMap('Click where 20 people arrive', false, (p) => void simAction({ type: 'spawn', x: p.x, y: p.y, n: 20 }));
        break;
      case 'shove':
        pickOnMap('Drag on the map: where the push starts, and which way', true, (p) => {
          const len = Math.hypot(p.dx, p.dy);
          if (len < 0.3) return msg('Drag a little further to give the push a direction', true);
          void simAction({ type: 'shove', x: p.x, y: p.y, dx: p.dx / len, dy: p.dy / len });
        });
        break;
    }
  });
}

function renderSimState(st: SimState) {
  $('simExits').replaceChildren(
    ...(st.exits ?? []).map((e) => {
      const btn = document.createElement('button');
      btn.className = `sm exit ${e.open ? 'open' : 'closed'}`;
      btn.textContent = `${e.open ? '🟢' : '🔴'} ${e.name}`;
      btn.dataset.tip = e.open ? 'Close this exit' : 'Open this exit';
      btn.addEventListener('click', () => void simAction({ type: 'exit', id: e.id, open: !e.open }));
      return btn;
    }),
  );
  const t = st.truth;
  if (!t) {
    $('simTruth').textContent = '';
    return;
  }
  let verdict = '';
  if (t.leadSeconds != null) {
    verdict =
      t.leadSeconds >= 0
        ? `<div class="lead good">Pulse warned <b>${t.leadSeconds.toFixed(0)} s before</b> the crowd became dangerous.</div>`
        : `<div class="lead bad">Pulse warned <b>${(-t.leadSeconds).toFixed(0)} s after</b> the crowd became dangerous.</div>`;
  } else if (t.alertAt != null) {
    verdict = `<div class="lead good">Pulse raised the alarm at ${t.alertAt.toFixed(0)} s; the crowd hasn't reached crush levels.</div>`;
  } else if (t.dangerAt != null) {
    verdict = `<div class="lead bad">The crowd became dangerous at ${t.dangerAt.toFixed(0)} s and Pulse hasn't alerted.</div>`;
  }
  $('simTruth').innerHTML =
    `<h4>Ground truth (only the simulator knows this)</h4>` +
    `<div class="truth-grid"><div><b>${t.maxDensity.toFixed(1)}</b><span>people/m² at the densest spot</span></div>` +
    `<div><b>${Math.round(t.maxPressure)}</b><span>N/m peak body pressure</span></div>` +
    `<div><b>${t.crushing}</b><span>people at crush level</span></div></div>${verdict}`;
}

async function pollSim() {
  try {
    const r = await fetch('/api/sim');
    if (!r.ok) return;
    simState = (await r.json()) as SimState;
    renderSimState(simState);
  } catch {
    /* server busy */
  }
}
setInterval(() => {
  if (lastMode === 'sim') void pollSim();
}, 1000);

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

function renderAlertCards() {
  const open = [...alertsById.values()]
    .filter((a) => a.status !== 'resolved' && a.level !== 'calm')
    .sort((x, y) => y.t - x.t)
    .slice(0, 4);
  const badge = $('navAlert');
  const reds = open.filter((a) => a.level === 'red' && a.status !== 'ack').length;
  badge.hidden = reds === 0;
  badge.textContent = String(reds);
  $('alertCards').replaceChildren(
    ...open.map((a) => {
      const { head, action } = splitBrief(a);
      const el = document.createElement('div');
      el.className = `alert-card ${a.level} ${a.status === 'ack' ? 'ack' : ''}`;
      const where = zoneNames.get(a.zone) ?? a.zone;
      const kind = a.early ? 'Early warning' : a.kind === 'density' ? 'Crowding' : a.kind === 'rule' ? 'Rule' : 'Crowd push';
      el.innerHTML =
        `<div class="ac-top"><b>${a.level === 'red' ? 'Danger' : 'Watch'}</b><span>${esc(kind)} · ${esc(where)} · ${fmtTime(a.t)}</span>` +
        `${a.test ? '<span class="tag">TEST</span>' : ''}${a.escalated ? '<span class="esc">escalated</span>' : ''}</div>` +
        `<div class="ac-head">${esc(head || `${where}: ${kind.toLowerCase()} detected`)}</div>` +
        (action ? `<div class="ac-action">${esc(action)}</div>` : '') +
        `<div class="ac-btns">${a.status === 'ack' ? `<span class="muted small">Acknowledged ${a.ackAt ? fmtTime(a.ackAt) : ''}</span>` : '<button class="sm primary" data-ack>Acknowledge</button>'}` +
        `<button class="sm ghost" data-resolve>Resolve</button></div>`;
      el.querySelector('[data-ack]')?.addEventListener('click', () => void alertAction(a, 'ack'));
      el.querySelector('[data-resolve]')!.addEventListener('click', () => void alertAction(a, 'resolve'));
      return el;
    }),
  );
}

async function alertAction(a: Alert, what: 'ack' | 'resolve') {
  if (!a.id) return;
  // Optimistic: the server's update arrives over the socket too.
  alertsById.set(a.id, { ...a, status: what === 'ack' ? 'ack' : 'resolved', ackAt: what === 'ack' ? Date.now() : a.ackAt });
  renderAlertCards();
  try {
    await post(`/api/alerts/${encodeURIComponent(a.id)}/${what}`);
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
  box.innerHTML =
    `<div class="rules-grid">` +
    `<label>Density alert above<input type="number" name="density" min="0.5" max="10" step="0.5" placeholder="off" value="${r.density ?? ''}" /><span class="muted">people per m²</span></label>` +
    `<label>…for at least<input type="number" name="densityHoldS" min="1" max="120" step="1" placeholder="5" value="${r.densityHoldS ?? ''}" /><span class="muted">seconds</span></label>` +
    `<label>Capacity<input type="number" name="maxPhones" min="1" max="10000" step="1" placeholder="off" value="${r.maxPhones ?? ''}" /><span class="muted">phones inside</span></label>` +
    `<label>Push detection<select name="push"><option value="on">On</option><option value="off">Off</option></select><span class="muted">travelling waves</span></label>` +
    `<label class="wide">Message for staff<input type="text" name="message" maxlength="140" placeholder="e.g. Open the side gate and slow the barrier queue" value="${esc(r.message ?? '')}" /></label>` +
    `<div class="checks"><label><input type="checkbox" name="sign" ${n.sign !== false ? 'checked' : ''}/> Sign</label>` +
    `<label><input type="checkbox" name="light" ${n.light !== false ? 'checked' : ''}/> Zone light</label>` +
    `<label><input type="checkbox" name="voice" ${n.voice !== false ? 'checked' : ''}/> Voice</label></div>` +
    `</div>`;
  (box.querySelector('[name=push]') as HTMLSelectElement).value = r.push === false ? 'off' : 'on';
  const read = (): AlertRules => {
    const v = (name: string) => (box.querySelector(`[name=${name}]`) as HTMLInputElement).value.trim();
    const num = (name: string) => (v(name) ? Number(v(name)) : undefined);
    const chk = (name: string) => (box.querySelector(`[name=${name}]`) as HTMLInputElement).checked;
    return {
      density: num('density'),
      densityHoldS: num('densityHoldS'),
      maxPhones: num('maxPhones'),
      push: v('push') !== 'off',
      message: v('message') || undefined,
      notify: { sign: chk('sign'), light: chk('light'), voice: chk('voice') },
    };
  };
  box.addEventListener('change', () => {
    areas.setRules(id, read());
    toast(`Rules saved for ${areas.get(id)?.name ?? 'area'}`);
  });
  box.addEventListener('click', (e) => e.stopPropagation());
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
          ($('vW') as HTMLInputElement).focus();
          void saveVenue({ ...venueForm(), template: 'custom' });
          return;
        }
        void saveVenue({ ...venue, w: t.w, h: t.h, template: t.id });
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
    toast('Floor plan uploaded. Try “Read the layout with Gemini”.');
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
  box.innerHTML = '<b>Gemini is reading the floor plan…</b>';
  try {
    const r = await fetch('/api/venue/floorplan/analyze', { method: 'POST' });
    const j = (await r.json().catch(() => ({}))) as FloorplanSuggestion & { error?: string };
    if (!r.ok) throw new Error(j.error ?? r.statusText);
    const exits = j.layout.exits ?? [];
    box.innerHTML =
      `<b>Gemini suggests</b> <span class="muted">(${esc(j.confidence)} confidence)</span>` +
      `<ul><li>Size: <b>${j.w} × ${j.h} m</b></li><li>Stage: ${j.layout.stage ? 'found' : 'not found'}</li>` +
      `<li>Exits: ${exits.length ? exits.map((e) => esc(e.name)).join(', ') : 'none found'}</li></ul>` +
      `<p class="muted small">${esc(j.notes)}</p>` +
      `<div class="row"><button class="sm primary" data-apply>Apply to venue</button><button class="sm ghost" data-dismiss>Dismiss</button></div>`;
    // Preview it on the map straight away.
    mesh.setLayout(j.layout);
    box.querySelector('[data-apply]')!.addEventListener('click', () => {
      void saveVenue({ ...venue, w: j.w, h: j.h, layout: j.layout, template: 'custom' });
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
const flag = (k: string) => {
  try {
    return localStorage.getItem(k) === '1';
  } catch {
    return false;
  }
};
const setFlag = (k: string) => {
  try {
    localStorage.setItem(k, '1');
  } catch {
    /* fine */
  }
};

$('testBtn').addEventListener('click', () => setFlag('pulse.drill'));

function renderChecklist() {
  const h = new Date().getHours();
  const part = h < 12 ? 'Good morning' : h < 18 ? 'Good afternoon' : 'Good evening';
  $('homeGreeting').textContent = `${part} · ${($('eventName') as HTMLInputElement).value || 'your event'}`;
  if ((snap?.stats.phones ?? 0) > 0) setFlag('pulse.joined');
  const items = [
    { done: flag('pulse.eventNamed'), title: 'Name your event', sub: 'Shown on the console and in briefings.', go: '#home', optional: false },
    { done: !!venue.template, title: 'Choose the venue size', sub: 'Pick a template or type the size in metres.', go: '#venue', optional: false },
    { done: !!venue.floorplan, title: 'Upload a floor plan', sub: 'Gemini reads the stage and exits from it.', go: '#venue', optional: true },
    { done: areas.list.length > 0, title: 'Draw watch areas', sub: 'Barriers, gates, the stage front.', go: '#areas', optional: false },
    { done: areas.list.some((a) => a.rules && Object.values(a.rules).some((v) => v !== undefined)), title: 'Set alert rules', sub: 'Density, capacity and your own message per area.', go: '#areas', optional: true },
    { done: hwOnline > 0, title: 'Connect signs and lights', sub: 'Check they are online and place them on the map.', go: '#hardware', optional: true },
    { done: flag('pulse.drill'), title: 'Run a drill', sub: 'Fire a test alert through briefing, voice and signs.', go: '#recordings', optional: false },
    { done: flag('pulse.joined'), title: 'Share the join QR', sub: 'Attendees scan it; their phones join the mesh.', go: '#live', optional: false },
  ];
  const req = items.filter((i) => !i.optional);
  const doneReq = req.filter((i) => i.done).length;
  $('setupProgress').textContent = `${doneReq} of ${req.length} required steps`;
  $('setupBar').style.width = `${(doneReq / req.length) * 100}%`;
  $('checklist').replaceChildren(
    ...items.map((i) => {
      const li = document.createElement('li');
      li.className = i.done ? 'done' : '';
      li.innerHTML =
        `<span class="tick">${i.done ? '✓' : ''}</span><span class="ck-text"><span class="ck-title">${esc(i.title)}${i.optional ? '<span class="opt">optional</span>' : ''}</span>` +
        `<span class="ck-sub">${esc(i.sub)}</span></span>` +
        (i.done ? '' : `<a class="sm-link" href="${i.go}"><button class="sm">${i.go === '#home' ? 'Edit name' : 'Open'}</button></a>`);
      if (i.go === '#home') li.querySelector('button')?.addEventListener('click', (e) => {
        e.preventDefault();
        ($('eventName') as HTMLInputElement).focus();
        ($('eventName') as HTMLInputElement).select();
      });
      return li;
    }),
  );
}
$('eventName').addEventListener('change', () => {
  setFlag('pulse.eventNamed');
  renderChecklist();
});
setInterval(() => {
  if (page() === 'home') renderChecklist();
}, 2000);

// Last: runs immediately, so everything it touches must already exist.
onPage((p) => {
  mesh.showBoards = p === 'hardware';
  if (p === 'home') renderChecklist();
  if (p === 'venue') renderTemplates();
});

// ---------------------------------------------------------------------------
// why did it fire? — the evidence behind a red link
// ---------------------------------------------------------------------------

let explainPair: [string, string] | null = null;
let explainTimer = 0;

areas.onLink = (from, to) => void openExplain([from, to]);
$('exClose').addEventListener('click', () => closeExplain());

function closeExplain() {
  window.clearInterval(explainTimer);
  explainPair = null;
  $('explain').hidden = true;
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
  $('exPair').textContent = `${e.from.slice(0, 6)} → ${e.to.slice(0, 6)}`;
  $('exA').textContent = e.from.slice(0, 6);
  $('exB').textContent = e.to.slice(0, 6);
  const v = $('exVerdict');
  v.className = `st ${e.wave ? 'wave' : 'ok'}`;
  v.textContent = e.wave ? 'Push detected' : 'Not a push';
  const failed = e.checks.filter((c) => !c.pass);
  $('exSummary').textContent = e.wave
    ? `${e.to.slice(0, 6)} repeats ${e.from.slice(0, 6)}'s motion ${Math.abs(e.lagMs)} ms later (similarity ${e.peak.toFixed(2)}): a push passing from one person to the next.`
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
