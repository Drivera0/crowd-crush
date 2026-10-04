// Moving about at the table demo (server: app/demomove.go, app/beaconsnap.go).
//
// The judges stand in a numbered row. A web page can't measure where a phone
// is to the decimetre, so moving shows up in the honest ways:
//   - "I moved": the placement map zooms to the row (the boards near it and the
//     other phones with their numbers), so a tap at table scale lands next to
//     "#2" and not two metres off. The tap is an ordinary `pos`; the server
//     drops the number and keeps the place in the row free for this phone.
//   - "Back to my place in the row": POST /api/demo/back.
//   - Walked up to a board (Android app, or Chrome's Bluetooth modes): the
//     server puts the phone beside it and says so in state.near.

import type { PhoneState } from '../../shared/protocol';
import type { DemoBack, DemoView } from '../../shared/demomove';

/** A part of the venue in metres: what the placement map shows. */
export interface ViewBox {
  x: number;
  y: number;
  w: number;
  h: number;
}

const $ = (id: string) => document.getElementById(id) as HTMLElement;

/** GET /api/demo/row; null when the demo spot is off or the server can't be reached. */
export async function loadDemoView(): Promise<DemoView | null> {
  try {
    const r = await fetch('/api/demo/row');
    if (!r.ok) return null;
    const v = (await r.json()) as DemoView;
    return v.on ? v : null;
  } catch {
    return null;
  }
}

const NEAR_M = 6; // boards and phones this close to the row are shown
const PAD_M = 1.2;

/**
 * The part of the venue to show: the row (its first places, at least four),
 * the boards and phones near it and this phone, padded, at least 5 × 3.5 m,
 * not much wider or taller than a phone screen's map, kept inside the venue.
 */
export function demoViewBox(v: DemoView, venueW: number, venueH: number, me?: { x: number; y: number }): ViewBox {
  const sp = v.spacing > 0 ? v.spacing : 0.6;
  const cols = Math.max(1, v.cols || 1);
  const pts: [number, number][] = [];
  const places = Math.max(4, ...v.phones.map((p) => (p.n ?? 0) + 1));
  for (let i = 0; i < places; i++) pts.push([v.x + (i % cols) * sp, v.y + Math.floor(i / cols) * sp]);
  const near = (x: number, y: number) => Math.hypot(x - v.x, y - v.y) < NEAR_M + places * sp;
  for (const b of v.boards) if (near(b.x, b.y)) pts.push([b.x, b.y]);
  for (const p of v.phones) if (near(p.x, p.y)) pts.push([p.x, p.y]);
  if (me && near(me.x, me.y)) pts.push([me.x, me.y]);
  let x0 = Math.min(...pts.map((p) => p[0])) - PAD_M;
  let x1 = Math.max(...pts.map((p) => p[0])) + PAD_M;
  let y0 = Math.min(...pts.map((p) => p[1])) - PAD_M;
  let y1 = Math.max(...pts.map((p) => p[1])) + PAD_M;
  const grow = (lo: number, hi: number, want: number): [number, number] => (hi - lo >= want ? [lo, hi] : [(lo + hi) / 2 - want / 2, (lo + hi) / 2 + want / 2]);
  [x0, x1] = grow(x0, x1, 5);
  [y0, y1] = grow(y0, y1, 3.5);
  // A phone's map is about 3:2 landscape: keep the box between 1.2 and 1.8.
  [y0, y1] = grow(y0, y1, (x1 - x0) / 1.8);
  [x0, x1] = grow(x0, x1, (y1 - y0) * 1.2);
  const fit = (lo: number, hi: number, max: number): [number, number] => {
    const w = Math.min(hi - lo, max);
    const l = Math.min(Math.max(0, lo), max - w);
    return [l, l + w];
  };
  [x0, x1] = fit(x0, x1, venueW);
  [y0, y1] = fit(y0, y1, venueH);
  return { x: x0, y: y0, w: x1 - x0, h: y1 - y0 };
}

/** POST /api/demo/back. Throws with the server's reason. */
export async function backToRow(id: string): Promise<DemoBack> {
  const r = await fetch('/api/demo/back', { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ id }) });
  const j = (await r.json().catch(() => ({}))) as DemoBack & { error?: string };
  if (!r.ok) throw new Error(j.error || `HTTP ${r.status}`);
  return j;
}

/** The live screen's demo controls from the latest state: "I moved", "Back to my place", "Near Zone light A". */
export function renderDemoMove(s: PhoneState, demo: boolean) {
  const box = $('demoMove');
  const on = demo || !!s.row || !!s.spot;
  box.hidden = !on;
  $('moveBtn').hidden = on; // "I moved" above does its job at the table
  if (!on) return;
  const near = $('nearLine');
  near.hidden = !s.near;
  near.textContent = s.near ? `📍 Near ${s.near}` : '';
  const back = $('backRow') as HTMLButtonElement;
  back.hidden = !s.spot;
  if (s.spot) back.textContent = `Back to my place in the row (#${s.spot})`;
  $('iMoved').textContent = s.row ? 'I moved: show where I am now' : 'I moved again';
}
