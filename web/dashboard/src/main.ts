import './style.css';
import type { Alert, Config, Node, Snapshot, ToDash, Wave, Zone } from '../../shared/protocol';
import { wsURL } from '../../shared/protocol';

const $ = <T extends HTMLElement = HTMLElement>(id: string) => document.getElementById(id) as T;
const SVGNS = 'http://www.w3.org/2000/svg';
const svgEl = <K extends keyof SVGElementTagNameMap>(tag: K, attrs: Record<string, string | number> = {}) => {
  const e = document.createElementNS(SVGNS, tag);
  for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, String(v));
  return e;
};

// ---------------------------------------------------------------------------
// state
// ---------------------------------------------------------------------------

let snap: Snapshot | null = null;
let thresholds = { yellow: 0.3, red: 0.6 };
const nodesById = new Map<string, Node>();
const zoneHist = new Map<string, number[]>(); // 1 Hz, last 60 s
let lastHistAt = 0;
const alerts: Alert[] = [];
let soundOn = false;
let lastBrief: Alert | null = null;

// ---------------------------------------------------------------------------
// network map
// ---------------------------------------------------------------------------

const CELL = 120;
const PAD = 40;
const map = $('map') as unknown as SVGSVGElement;
const gZones = svgEl('g');
const gEdges = svgEl('g');
const gPulses = svgEl('g');
const gNodes = svgEl('g');
map.append(gZones, gEdges, gPulses, gNodes);

let gridKey = '';
const HEADER = 40; // label strip above each band of zone rows
let bandStarts: number[] = [0]; // first row of each zone band

/** y of the top of grid row r, leaving a header above every zone band. */
function rowY(r: number): number {
  let band = 0;
  for (let i = 0; i < bandStarts.length; i++) if (bandStarts[i] <= r) band = i;
  return r * CELL + (band + 1) * HEADER;
}
const zoneEls = new Map<string, { rect: SVGRectElement; label: SVGTextElement; score: SVGTextElement }>();
const edgeEls = new Map<string, SVGLineElement>();
const nodeEls = new Map<string, { g: SVGGElement; core: SVGCircleElement; ring: SVGCircleElement; text: SVGTextElement }>();
const nodePos = new Map<string, { x: number; y: number }>();

function layoutGrid(rows: number, cols: number, zones: Zone[]) {
  const starts = [...new Set(zones.map((z) => z.row0))].sort((a, b) => a - b);
  const key = `${rows}x${cols}:${starts.join(',')}`;
  if (key === gridKey) return;
  gridKey = key;
  bandStarts = starts.length ? starts : [0];
  const w = cols * CELL;
  const h = rowY(rows);
  map.setAttribute('viewBox', `${-PAD} ${-PAD} ${w + 2 * PAD} ${h + 2 * PAD}`);
  gZones.replaceChildren();
  zoneEls.clear();
}

function renderZones(zones: Zone[]) {
  const seen = new Set<string>();
  for (const z of zones) {
    seen.add(z.id);
    let el = zoneEls.get(z.id);
    if (!el) {
      const rect = svgEl('rect', { rx: 18, class: 'zone-rect' });
      const label = svgEl('text', { class: 'zone-label' });
      const score = svgEl('text', { class: 'zone-score' });
      label.textContent = z.id;
      gZones.append(rect, label, score);
      el = { rect, label, score };
      zoneEls.set(z.id, el);
    }
    const x = z.col0 * CELL + 6;
    const top = rowY(z.row0) - HEADER + 6;
    el.rect.setAttribute('x', String(x));
    el.rect.setAttribute('y', String(top));
    el.rect.setAttribute('width', String((z.col1 - z.col0 + 1) * CELL - 12));
    el.rect.setAttribute('height', String(rowY(z.row1) + CELL - 6 - top));
    el.label.setAttribute('x', String(x + 14));
    el.label.setAttribute('y', String(top + 36));
    el.score.setAttribute('x', String(x + 50));
    el.score.setAttribute('y', String(top + 30));
    el.score.textContent = `${z.level} · ${z.score.toFixed(2)}`;
    el.rect.setAttribute('class', `zone-rect ${z.level}`);
    el.label.setAttribute('class', `zone-label ${z.level}`);
  }
  for (const [id, el] of zoneEls) {
    if (!seen.has(id)) {
      el.rect.remove();
      el.label.remove();
      el.score.remove();
      zoneEls.delete(id);
    }
  }
}

function placeNodes(nodes: Node[]) {
  // Several phones may share a cell: fan them out around the centre.
  const byCell = new Map<string, Node[]>();
  for (const n of nodes) {
    const k = `${n.row},${n.col}`;
    if (!byCell.has(k)) byCell.set(k, []);
    byCell.get(k)!.push(n);
  }
  nodePos.clear();
  for (const list of byCell.values()) {
    list.forEach((n, i) => {
      const cx = n.col * CELL + CELL / 2;
      const cy = rowY(n.row) + CELL / 2;
      if (list.length === 1) {
        nodePos.set(n.id, { x: cx, y: cy });
      } else {
        const a = (2 * Math.PI * i) / list.length - Math.PI / 2;
        nodePos.set(n.id, { x: cx + 26 * Math.cos(a), y: cy + 26 * Math.sin(a) });
      }
    });
  }
}

function renderNodes(nodes: Node[]) {
  const seen = new Set<string>();
  for (const n of nodes) {
    seen.add(n.id);
    let el = nodeEls.get(n.id);
    if (!el) {
      const g = svgEl('g', { class: 'node' });
      const ring = svgEl('circle', { class: 'ring' });
      const core = svgEl('circle', { class: 'core', r: 20 });
      const text = svgEl('text');
      g.append(ring, core, text);
      g.addEventListener('mouseenter', () => (hoverId = n.id));
      g.addEventListener('mouseleave', () => {
        hoverId = null;
        $('tooltip').hidden = true;
      });
      gNodes.append(g);
      el = { g, core, ring, text };
      nodeEls.set(n.id, el);
    }
    const p = nodePos.get(n.id)!;
    const sway = Math.min(n.sway, 2);
    el.g.setAttribute('class', `node ${n.status}`);
    el.core.setAttribute('cx', String(p.x));
    el.core.setAttribute('cy', String(p.y));
    el.ring.setAttribute('cx', String(p.x));
    el.ring.setAttribute('cy', String(p.y));
    el.ring.setAttribute('r', String(26 + sway * 10));
    el.ring.setAttribute('stroke-width', String(2 + sway * 6));
    el.text.setAttribute('x', String(p.x));
    el.text.setAttribute('y', String(p.y));
    el.text.textContent = String(n.col + 1);
  }
  for (const [id, el] of nodeEls) {
    if (!seen.has(id)) {
      el.g.remove();
      nodeEls.delete(id);
    }
  }
}

const edgeKey = (a: string, b: string) => (a < b ? `${a}|${b}` : `${b}|${a}`);

function renderEdges(nodes: Node[], waves: Wave[]) {
  const waveKeys = new Set(waves.map((w) => edgeKey(w.from, w.to)));
  const seen = new Set<string>();
  for (let i = 0; i < nodes.length; i++) {
    for (let j = i + 1; j < nodes.length; j++) {
      const a = nodes[i];
      const b = nodes[j];
      if (Math.abs(a.row - b.row) + Math.abs(a.col - b.col) !== 1) continue;
      const k = edgeKey(a.id, b.id);
      seen.add(k);
      let line = edgeEls.get(k);
      if (!line) {
        line = svgEl('line', { class: 'edge' });
        gEdges.append(line);
        edgeEls.set(k, line);
      }
      const pa = nodePos.get(a.id)!;
      const pb = nodePos.get(b.id)!;
      line.setAttribute('x1', String(pa.x));
      line.setAttribute('y1', String(pa.y));
      line.setAttribute('x2', String(pb.x));
      line.setAttribute('y2', String(pb.y));
      line.setAttribute('class', waveKeys.has(k) ? 'edge wave' : 'edge');
    }
  }
  for (const [k, line] of edgeEls) {
    if (!seen.has(k)) {
      line.remove();
      edgeEls.delete(k);
    }
  }
}

// Pulses race along wave edges in the direction of travel.
interface Pulse {
  el: SVGCircleElement;
  from: { x: number; y: number };
  to: { x: number; y: number };
  start: number;
  dur: number;
}
let pulses: Pulse[] = [];
const lastSpawn = new Map<string, number>();
let activeWaves: Wave[] = [];

function spawnPulses(now: number) {
  for (const w of activeWaves) {
    const k = `${w.from}>${w.to}`;
    if (now - (lastSpawn.get(k) ?? 0) < 650) continue;
    const from = nodePos.get(w.from);
    const to = nodePos.get(w.to);
    if (!from || !to) continue;
    lastSpawn.set(k, now);
    const el = svgEl('circle', { r: 7, class: 'pulse' });
    gPulses.append(el);
    // Travel time on screen ~ the measured lag, stretched to be visible.
    pulses.push({ el, from, to, start: now, dur: Math.max(350, Math.min(900, w.lagMs * 1.6)) });
  }
}

function animate(now: number) {
  spawnPulses(now);
  pulses = pulses.filter((p) => {
    const f = (now - p.start) / p.dur;
    if (f >= 1) {
      p.el.remove();
      return false;
    }
    const e = f < 0.5 ? 2 * f * f : 1 - Math.pow(-2 * f + 2, 2) / 2;
    p.el.setAttribute('cx', String(p.from.x + (p.to.x - p.from.x) * e));
    p.el.setAttribute('cy', String(p.from.y + (p.to.y - p.from.y) * e));
    p.el.setAttribute('opacity', String(f < 0.85 ? 1 : (1 - f) / 0.15));
    return true;
  });
  requestAnimationFrame(animate);
}
requestAnimationFrame(animate);

// ---------------------------------------------------------------------------
// tooltip
// ---------------------------------------------------------------------------

let hoverId: string | null = null;
let mouse = { x: 0, y: 0 };
$('mapWrap').addEventListener('mousemove', (e) => {
  const r = $('mapWrap').getBoundingClientRect();
  mouse = { x: e.clientX - r.left, y: e.clientY - r.top };
  renderTooltip();
});

function renderTooltip() {
  const tip = $('tooltip');
  const n = hoverId ? nodesById.get(hoverId) : undefined;
  if (!n) {
    tip.hidden = true;
    return;
  }
  const offs = n.offset >= 0 ? `+${n.offset}` : String(n.offset);
  tip.innerHTML =
    `<b>${esc(n.id.slice(0, 8))}</b> · ${esc(n.ua ?? '')}<br>` +
    `spot ${n.col + 1}${n.row ? `, row ${n.row + 1}` : ''} · <b>${n.status}</b><br>` +
    `sway ${n.sway.toFixed(2)} m/s²<br>` +
    `RTT ${n.rtt} ms<br>clock offset ${offs} ms<br>last reading ${(n.age / 1000).toFixed(1)} s ago`;
  tip.hidden = false;
  const w = $('mapWrap').clientWidth;
  tip.style.left = `${Math.min(mouse.x + 16, w - 200)}px`;
  tip.style.top = `${mouse.y + 16}px`;
}

function esc(s: string): string {
  return s.replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);
}

// ---------------------------------------------------------------------------
// side panel
// ---------------------------------------------------------------------------

const zoneRows = new Map<string, { row: HTMLElement; level: HTMLElement; score: HTMLElement; line: SVGPolylineElement }>();

function renderZonePanel(zones: Zone[]) {
  const box = $('zones');
  for (const z of zones) {
    let r = zoneRows.get(z.id);
    if (!r) {
      const row = document.createElement('div');
      row.className = 'zone-row';
      const id = document.createElement('div');
      id.className = 'zone-id';
      id.textContent = z.id;
      const level = document.createElement('div');
      level.className = 'zone-level';
      const spark = svgEl('svg', { class: 'spark', viewBox: '0 0 59 1', preserveAspectRatio: 'none' });
      for (const [v, color] of [
        [thresholds.yellow, 'rgba(250,204,21,.45)'],
        [thresholds.red, 'rgba(239,68,68,.55)'],
      ] as const) {
        spark.append(svgEl('line', { x1: 0, x2: 59, y1: 1 - v, y2: 1 - v, stroke: color }));
      }
      const line = svgEl('polyline');
      spark.append(line);
      const score = document.createElement('div');
      score.className = 'zone-score-num';
      row.append(id, level, spark as unknown as HTMLElement, score);
      box.append(row);
      r = { row, level, score, line };
      zoneRows.set(z.id, r);
    }
    r.row.className = `zone-row ${z.level}`;
    r.level.textContent = z.level;
    r.score.textContent = z.score.toFixed(2);
    const h = zoneHist.get(z.id) ?? [];
    const off = 60 - h.length;
    r.line.setAttribute('points', h.map((v, i) => `${i + off - 1},${1 - Math.min(1, Math.max(0, v))}`).join(' '));
  }
  for (const [id, r] of zoneRows) {
    if (!zones.some((z) => z.id === id)) {
      r.row.remove();
      zoneRows.delete(id);
    }
  }
}

function fmtTime(t: number) {
  return new Date(t).toLocaleTimeString([], { hour12: false });
}

function renderLog() {
  const ol = $('log');
  ol.replaceChildren(
    ...alerts
      .slice()
      .reverse()
      .map((a) => {
        const li = document.createElement('li');
        li.innerHTML =
          `<time>${fmtTime(a.t)}</time><b>${esc(a.zone)}</b><span class="lv ${a.level}">${a.level}</span>` +
          `<span class="txt">${a.test ? '<span class="test">TEST</span>' : ''}${a.brief ? esc(a.brief) : `score ${a.score.toFixed(2)}`}</span>`;
        return li;
      }),
  );
}

function showBrief(a: Alert) {
  lastBrief = a;
  $('brief').textContent = a.brief ?? '';
  $('brief').classList.remove('muted');
  $('briefMeta').textContent = `· zone ${a.zone} · ${fmtTime(a.t)}${a.test ? ' · test' : ''}`;
  $('briefPanel').classList.toggle('red', a.level === 'red');
  ($('replayAudioBtn') as HTMLButtonElement).disabled = false;
}

// ---------------------------------------------------------------------------
// sound: browsers only allow it after a click, hence the button
// ---------------------------------------------------------------------------

let audioCtx: AudioContext | null = null;

$('soundBtn').addEventListener('click', () => {
  soundOn = !soundOn;
  $('soundBtn').textContent = soundOn ? '🔊 Sound on' : '🔇 Enable sound';
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

  if (s.t - lastHistAt >= 1000) {
    lastHistAt = s.t;
    for (const z of s.zones) {
      const h = zoneHist.get(z.id) ?? [];
      h.push(z.score);
      if (h.length > 60) h.shift();
      zoneHist.set(z.id, h);
    }
  }

  layoutGrid(s.rows, s.cols, s.zones);
  renderZones(s.zones);
  placeNodes(s.nodes);
  renderEdges(s.nodes, s.waves);
  renderNodes(s.nodes);
  activeWaves = s.waves;
  renderZonePanel(s.zones);
  renderTooltip();

  $('cPhones').textContent = String(s.stats.phones);
  $('cRate').textContent = String(Math.round(s.stats.msgPerSec));
  $('cRtt').textContent = s.stats.medianRtt ? String(s.stats.medianRtt) : '–';
  $('cWaves').textContent = String(s.waves.length);

  const replay = s.mode === 'replay';
  const badge = $('mode');
  badge.textContent = replay ? 'REPLAY' : 'LIVE';
  badge.className = `badge ${replay ? 'replay' : 'live'}`;
  $('replayInfo').hidden = !replay;
  if (replay) $('replayInfo').textContent = `${s.replay ?? ''} · ${Math.round((s.progress ?? 0) * 100)}%`;
  $('liveBtn').classList.toggle('on', !replay);
  $('replayBtn').classList.toggle('on', replay);

  $('recBadge').hidden = !s.recording;
  const recBtn = $('recBtn');
  recBtn.textContent = s.recording ? `■ Stop "${s.recording}"` : '● Record run';
  recBtn.classList.toggle('on', !!s.recording);

  updateQR();
}

function onAlert(a: Alert, fresh: boolean) {
  alerts.push(a);
  if (alerts.length > 50) alerts.shift();
  renderLog();
  if (a.brief) {
    showBrief(a);
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
      alerts.length = 0;
      for (const a of msg.alerts) onAlert(a, false);
      if (!msg.alerts.length) renderLog();
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

$('liveBtn').addEventListener('click', () => post('/api/live').catch((e: Error) => msg(e.message, true)));
$('replayBtn').addEventListener('click', async () => {
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
    msg(`Test alert sent for zone ${r.zone}`);
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
