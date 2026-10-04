// The "Where are you?" map on the phone: the venue as staff drew it (watch
// areas with their names, the stage, exits and walls), so tapping your spot
// isn't a guess. Read from the public GET /api/venue and GET /api/areas;
// without them the map stays a plain grid with "STAGE" at the top.

import type { Area, Venue } from '../../shared/protocol';
import type { DemoView } from '../../shared/demomove';
import type { ViewBox } from './demomove';

const SVG = 'http://www.w3.org/2000/svg';

let drawn = '';

function el<K extends keyof SVGElementTagNameMap>(name: K, attrs: Record<string, string | number>, text?: string) {
  const e = document.createElementNS(SVG, name);
  for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, String(v));
  if (text) e.textContent = text;
  return e;
}

/**
 * Fetch the venue layout and areas and draw them under the "you" dot. Never throws.
 * view: the part of the venue shown (default all of it). row: at the demo spot, the
 * row, the boards and the other phones on top (demomove.ts); me = this phone's name, left out.
 */
export async function drawVenueMap(w: number, h: number, view?: ViewBox, row?: DemoView | null, me?: string) {
  const svg = document.getElementById('venueSvg') as SVGSVGElement | null;
  if (!svg) return;
  let venue: Venue | null = null;
  let areas: Area[] = [];
  try {
    const [rv, ra] = await Promise.all([fetch('/api/venue'), fetch('/api/areas')]);
    if (rv.ok) venue = (await rv.json()) as Venue;
    if (ra.ok) areas = (await ra.json()) as Area[];
  } catch {
    /* offline: the plain map is still usable */
  }
  if (venue && venue.w > 0 && venue.h > 0) {
    w = venue.w;
    h = venue.h;
  }
  const vb = view ?? { x: 0, y: 0, w, h };
  const key = JSON.stringify([w, h, vb, row, me, venue?.layout, areas.map((a) => [a.name, a.poly, a.sens])]);
  if (key === drawn) return;
  drawn = key;
  svg.setAttribute('viewBox', `${vb.x} ${vb.y} ${vb.w} ${vb.h}`);
  svg.setAttribute('preserveAspectRatio', 'none');
  svg.replaceChildren();
  // Text in metres would be tiny or huge depending on the venue: size it to the map.
  const fs = Math.max(vb.w, vb.h) / 30;
  const pts = (p: [number, number][]) => p.map(([x, y]) => `${x},${y}`).join(' ');

  for (const a of areas) {
    if (!Array.isArray(a.poly) || a.poly.length < 3) continue;
    const high = a.sens === 'high';
    svg.append(
      el('polygon', {
        points: pts(a.poly as [number, number][]),
        fill: high ? 'rgba(239,68,68,0.14)' : 'rgba(56,189,248,0.12)',
        stroke: high ? 'rgba(239,68,68,0.6)' : 'rgba(56,189,248,0.55)',
        'stroke-width': fs / 8,
        'vector-effect': 'non-scaling-stroke',
      }),
    );
    const cx = a.poly.reduce((s, p) => s + p[0], 0) / a.poly.length;
    const cy = a.poly.reduce((s, p) => s + p[1], 0) / a.poly.length;
    svg.append(el('text', { x: cx, y: cy, 'font-size': fs, 'text-anchor': 'middle', 'dominant-baseline': 'middle', fill: 'rgba(226,232,240,0.85)' }, a.name));
  }

  const lay = venue?.layout;
  const stage = lay?.stage && lay.stage.length >= 3 ? (lay.stage as [number, number][]) : null;
  if (stage) {
    svg.append(el('polygon', { points: pts(stage), fill: '#1e293b', stroke: '#475569', 'stroke-width': 1, 'vector-effect': 'non-scaling-stroke' }));
    const cx = stage.reduce((s, p) => s + p[0], 0) / stage.length;
    const cy = stage.reduce((s, p) => s + p[1], 0) / stage.length;
    svg.append(el('text', { x: cx, y: cy, 'font-size': fs, 'text-anchor': 'middle', 'dominant-baseline': 'middle', fill: '#94a3b8', 'letter-spacing': fs / 4 }, 'STAGE'));
  }
  // The HTML "STAGE" tab only stands in when no stage outline is known.
  const tag = document.getElementById('stageTag');
  if (tag) tag.hidden = !!stage || !!view; // zoomed in, the top of the map isn't the stage end

  for (const wl of lay?.walls ?? []) {
    svg.append(el('line', { x1: wl[0], y1: wl[1], x2: wl[2], y2: wl[3], stroke: '#94a3b8', 'stroke-width': 3, 'vector-effect': 'non-scaling-stroke' }));
  }
  for (const ex of lay?.exits ?? []) {
    svg.append(el('line', { x1: ex.x0, y1: ex.y0, x2: ex.x1, y2: ex.y1, stroke: '#22c55e', 'stroke-width': 5, 'stroke-linecap': 'round', 'vector-effect': 'non-scaling-stroke' }));
    const mx = (ex.x0 + ex.x1) / 2;
    const my = (ex.y0 + ex.y1) / 2;
    // Keep the label inside the map.
    const tx = Math.min(w - fs * 2, Math.max(fs * 2, mx));
    const ty = Math.min(h - fs * 0.6, Math.max(fs, my));
    svg.append(el('text', { x: tx, y: ty, 'font-size': fs * 0.8, 'text-anchor': 'middle', fill: '#86efac' }, ex.name || 'Exit'));
  }
  if (row) drawRow(svg, row, fs, me);
}

/** The demo row on top: dashed places, the boards, the other phones in their colours with their numbers. */
function drawRow(svg: SVGSVGElement, v: DemoView, fs: number, me?: string) {
  const sp = v.spacing > 0 ? v.spacing : 0.6;
  const cols = Math.max(1, v.cols || 1);
  const places = Math.max(4, ...v.phones.map((p) => (p.n ?? 0) + 1));
  const r = Math.min(sp * 0.35, fs * 1.2);
  for (let i = 0; i < places; i++) {
    const x = v.x + (i % cols) * sp;
    const y = v.y + Math.floor(i / cols) * sp;
    svg.append(el('circle', { cx: x, cy: y, r, fill: 'none', stroke: 'rgba(226,232,240,0.35)', 'stroke-width': 1, 'stroke-dasharray': '3 3', 'vector-effect': 'non-scaling-stroke' }));
  }
  for (const b of v.boards) {
    const s = fs * 1.1;
    svg.append(el('rect', { x: b.x - s / 2, y: b.y - s / 2, width: s, height: s, rx: s / 5, fill: b.key === 'laptop' ? '#334155' : '#0ea5e9', stroke: '#e2e8f0', 'stroke-width': 1, 'vector-effect': 'non-scaling-stroke' }));
    svg.append(el('text', { x: b.x, y: b.y - s * 0.9, 'font-size': fs * 0.75, 'text-anchor': 'middle', fill: '#7dd3fc' }, b.label));
  }
  for (const p of v.phones) {
    if (me && p.name === me) continue;
    svg.append(el('circle', { cx: p.x, cy: p.y, r: r * 0.8, fill: p.color || '#94a3b8', stroke: '#0f172a', 'stroke-width': 1.5, 'vector-effect': 'non-scaling-stroke' }));
    const label = p.n ? `#${p.n}` : (p.name ?? '').split(' ').pop() ?? '';
    if (label) svg.append(el('text', { x: p.x, y: p.y + r * 0.8 + fs, 'font-size': fs * 0.9, 'text-anchor': 'middle', fill: '#f8fafc' }, label));
  }
}
