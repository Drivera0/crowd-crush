import './style.css';
import type { Alert, Config, Level, Node, NodeDetail, Snapshot, ToDash } from '../../shared/protocol';
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
// watch areas: drawn by the operator, evaluated from the detector's node status
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
        `<button class="del sm ghost" data-tip="Delete area" aria-label="Delete area">✕</button>` +
        `<div class="meta"></div>`;
      const id = a.id;
      const name = li.querySelector<HTMLInputElement>('.name')!;
      name.value = a.name;
      name.addEventListener('change', () => areas.rename(id, name.value));
      name.addEventListener('keydown', (e) => e.key === 'Enter' && name.blur());
      li.querySelector('.sens')!.addEventListener('click', () => areas.toggleSens(id));
      li.querySelector('.del')!.addEventListener('click', () => areas.remove(id));
      li.addEventListener('click', (e) => {
        if (!(e.target as HTMLElement).closest('button')) areas.select(id);
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

areas.onEscalate = (a) => {
  const danger = a.level === 'danger';
  const text = danger
    ? `${a.name}: ${a.wave} of ${a.phones} phones caught in a travelling push.`
    : `${a.name}: ${a.wave + a.sway} of ${a.phones} phones swaying.`;
  logEntry({ t: Date.now(), level: danger ? 'red' : 'yellow', text, area: a.name });
  toast(text, danger ? 'danger' : 'watch');
  if (danger) {
    beep();
    if (soundOn) speak(`${a.name}. Crowd push detected.`);
  }
};

// ---------------------------------------------------------------------------
// header status: one sentence anyone can read from across the room
// ---------------------------------------------------------------------------

function renderStatus(zone: Level, phones: number) {
  const danger = areas.list.filter((a) => a.level === 'danger');
  const watch = areas.list.filter((a) => a.level === 'watch');
  let cls: 'calm' | 'yellow' | 'red' = 'calm';
  let text = phones === 0 ? 'Waiting for attendees' : 'All clear';
  const active = danger.length + watch.length + (zone !== 'calm' && !danger.length && !watch.length ? 1 : 0);
  $('kAlerts').textContent = String(active);
  $('kAlerts').parentElement!.classList.toggle('hot', active > 0);
  $('kAlertsSub').textContent =
    danger.length || watch.length
      ? `${danger.length} danger · ${watch.length} watch area${watch.length === 1 ? '' : 's'}`
      : active
        ? `crowd-wide ${zone === 'red' ? 'danger' : 'warning'}`
        : 'nothing needs attention';
  $('kAreas').textContent = String(areas.list.length);
  if (zone === 'yellow' || watch.length) {
    cls = 'yellow';
    text = watch.length ? `Pressure building in ${names(watch)}` : 'Pressure building in the crowd';
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
    `<div class="tt-ua">${esc(deviceName(n.ua))} · spot ${n.col + 1}${n.row ? `, row ${n.row + 1}` : ''}</div>` +
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
  $('dSpot').textContent = n ? `${n.col + 1}${n.row ? `, row ${n.row + 1}` : ''}` : '–';
  $('dZone').textContent = d?.zone ?? '–';
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

  mesh.update(s.nodes, s.waves, s.rows, s.cols);
  areas.evaluate(s.nodes);
  const worstArea = areas.worst();
  mesh.level = worstArea === 'danger' ? 'red' : risk.level === 'red' ? 'red' : worstArea === 'watch' ? 'yellow' : risk.level;
  renderRisk(risk.level, risk.score, s.waves.length);
  renderStatus(risk.level, s.stats.phones);
  renderTooltip();

  setCounter('cPhones', s.stats.phones);
  setCounter('cRate', Math.round(s.stats.msgPerSec));
  setCounter('cRtt', s.stats.medianRtt || null);
  setCounter('cWaves', s.waves.length);

  const replay = s.mode === 'replay';
  const badge = $('mode');
  badge.textContent = replay ? 'REPLAY' : 'LIVE';
  badge.className = `badge ${replay ? 'replay' : 'live'}`;
  $('replayBanner').hidden = !replay;
  if (replay) $('replayInfo').textContent = `${s.replay ?? ''} · ${Math.round((s.progress ?? 0) * 100)}%`;
  if (replay !== wasReplay) {
    wasReplay = replay;
    $('liveBtn').classList.toggle('on', !replay);
    $('replayBtn').classList.toggle('on', replay);
    $('replayOpts').hidden = !replay;
  }

  $('recBadge').hidden = !s.recording;
  const recBtn = $('recBtn');
  recBtn.textContent = s.recording ? `■ Stop and save "${s.recording}"` : '● Start recording';
  recBtn.classList.toggle('on', !!s.recording);
  if (drawerId && !nodesById.has(drawerId)) openDrawer(null);

  updateQR();
}

function onAlert(a: Alert, fresh: boolean) {
  logEntry({ t: a.t, level: a.level, text: a.brief ?? `Crowd risk ${Math.round(a.score * 100)}.`, test: a.test }, fresh);
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

let wasReplay = false;

async function goLive() {
  try {
    await post('/api/live');
    $('replayOpts').hidden = true;
    $('liveBtn').classList.add('on');
    $('replayBtn').classList.remove('on');
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
$('qrBtn').addEventListener('click', () => {
  qrMode = $('qr').hidden ? 'shown' : 'hidden';
  updateQR();
});
$('qr').addEventListener('click', () => {
  qrMode = 'hidden';
  updateQR();
});
function updateQR() {
  const show = qrMode === 'shown' || (qrMode === 'auto' && snap?.mode === 'live' && snap.stats.phones === 0);
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
  connect();
}

void init();
