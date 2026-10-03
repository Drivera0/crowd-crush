// Custom watch areas drawn by the operator on top of the mesh: rectangles,
// circles or freehand lassos over the parts of the crowd they know are risky.
//
// An area doesn't detect anything itself. It counts the phones whose spot lies
// inside it and reads their status from the detector: how many are part of a
// travelling wave, how many are swaying. "High risk" areas escalate on the
// first sign of a push; normal areas need a share of their phones involved.

import type { Node } from '../../shared/protocol';
import type { Mesh } from './mesh';

export type Tool = 'select' | 'rect' | 'circle' | 'free';
export type Sensitivity = 'normal' | 'high';
export type AreaLevel = 'calm' | 'watch' | 'danger';

// Coordinates are fractions of the canvas size, so areas survive a resize.
// A circle's radius is a fraction of the smaller side.
export type Shape =
  | { kind: 'rect'; x: number; y: number; w: number; h: number }
  | { kind: 'circle'; cx: number; cy: number; r: number }
  | { kind: 'poly'; pts: [number, number][] };

export interface Area {
  id: string;
  name: string;
  color: string;
  sens: Sensitivity;
  shape: Shape;
  // live, not saved
  level: AreaLevel;
  phones: number;
  wave: number;
  sway: number;
  calmSince: number;
}

const PALETTE = ['#22d3ee', '#a78bfa', '#f472b6', '#34d399', '#fb923c', '#facc15'];
const STORE = 'pulse.areas.v1';
const HOLD_MS = 2000; // an area stays escalated this long after things settle

const LEVEL_RGB: Record<AreaLevel, string> = { calm: '', watch: '251,191,36', danger: '244,63,94' };

function hexRgb(hex: string) {
  const n = parseInt(hex.slice(1), 16);
  return `${(n >> 16) & 255},${(n >> 8) & 255},${n & 255}`;
}

export class Areas {
  list: Area[] = [];
  selected: string | null = null;
  private tool: Tool = 'select';
  private draft: Shape | null = null;
  private drag: { id: string; x: number; y: number } | null = null;
  private start = { x: 0, y: 0 };

  /** Fires when areas are added, removed, renamed, or change level. */
  onChange: () => void = () => {};
  /** Fires when an area escalates. */
  onEscalate: (a: Area, from: AreaLevel) => void = () => {};
  /** Fires when the tool changes (e.g. Escape, or after finishing a shape). */
  onTool: (t: Tool) => void = () => {};
  /** Fires when a phone is clicked (null: clicked empty space). */
  onNode: (id: string | null) => void = () => {};
  private pan: { x: number; y: number } | null = null;
  private press: { x: number; y: number; node: string | null; moved: boolean } | null = null;

  constructor(private canvas: HTMLCanvasElement, private mesh: Mesh) {
    this.load();
    canvas.addEventListener('pointerdown', (e) => this.down(e));
    canvas.addEventListener('pointermove', (e) => this.move(e));
    canvas.addEventListener('pointerup', (e) => this.up(e));
    canvas.addEventListener('pointercancel', () => {
      this.draft = this.drag = null;
      this.pan = this.press = null;
    });
    canvas.addEventListener(
      'wheel',
      (e) => {
        e.preventDefault();
        const s = this.screen(e);
        mesh.zoomAt(s.x, s.y, Math.exp(-e.deltaY * 0.0015));
      },
      { passive: false },
    );
    window.addEventListener('keydown', (e) => {
      const typing = (e.target as HTMLElement).closest('input, textarea, select');
      if (e.key === 'Escape') {
        this.draft = null;
        this.setTool('select');
      } else if (!typing && (e.key === 'Delete' || e.key === 'Backspace') && this.selected) {
        this.remove(this.selected);
      }
    });
    mesh.underlay = (g, now) => this.draw(g, now);
  }

  get drawing() {
    return this.tool !== 'select';
  }

  setTool(t: Tool) {
    this.tool = t;
    this.canvas.style.cursor = t === 'select' ? '' : 'crosshair';
    this.onTool(t);
  }

  select(id: string | null) {
    this.selected = id;
    this.onChange();
  }

  rename(id: string, name: string) {
    const a = this.get(id);
    if (!a) return;
    a.name = name.trim() || a.name;
    this.save();
  }

  toggleSens(id: string) {
    const a = this.get(id);
    if (!a) return;
    a.sens = a.sens === 'high' ? 'normal' : 'high';
    this.save();
    this.onChange();
  }

  remove(id: string) {
    this.list = this.list.filter((a) => a.id !== id);
    if (this.selected === id) this.selected = null;
    this.save();
    this.onChange();
  }

  get(id: string) {
    return this.list.find((a) => a.id === id);
  }

  /** Recount phones in each area and update its level. */
  evaluate(nodes: Node[]) {
    const now = performance.now();
    let changed = false;
    for (const a of this.list) {
      let phones = 0, wave = 0, sway = 0;
      for (const n of nodes) {
        if (n.status === 'stale') continue;
        const p = this.mesh.homeOf(n.id);
        if (!p || !this.contains(a.shape, p.x, p.y)) continue;
        phones++;
        if (n.status === 'wave') wave++;
        else if (n.status === 'swaying') sway++;
      }
      const want = levelFor(a.sens, phones, wave, sway);
      const prev = a.level;
      let next = prev;
      if (rank(want) > rank(prev)) next = want;
      else if (rank(want) < rank(prev)) {
        if (!a.calmSince) a.calmSince = now;
        if (now - a.calmSince > HOLD_MS) next = want;
      }
      if (rank(want) >= rank(prev)) a.calmSince = 0;
      if (next !== prev || phones !== a.phones || wave !== a.wave || sway !== a.sway) changed = true;
      a.phones = phones;
      a.wave = wave;
      a.sway = sway;
      if (next !== prev) {
        a.level = next;
        a.calmSince = 0;
        if (rank(next) > rank(prev)) this.onEscalate(a, prev);
      }
    }
    if (changed) this.onChange();
  }

  /** Worst level across all areas. */
  worst(): AreaLevel {
    return this.list.reduce<AreaLevel>((w, a) => (rank(a.level) > rank(w) ? a.level : w), 'calm');
  }

  // -------------------------------------------------------------------------
  // geometry
  // -------------------------------------------------------------------------

  private screen(e: { clientX: number; clientY: number }) {
    const r = this.canvas.getBoundingClientRect();
    return { x: e.clientX - r.left, y: e.clientY - r.top };
  }

  /** Pointer position in world (unzoomed layout) coordinates. */
  private px(e: PointerEvent) {
    const s = this.screen(e);
    return this.mesh.toWorld(s.x, s.y);
  }

  private size() {
    return this.mesh.size();
  }

  private contains(s: Shape, x: number, y: number): boolean {
    const { w, h } = this.size();
    if (s.kind === 'rect') {
      const x0 = Math.min(s.x, s.x + s.w) * w, x1 = Math.max(s.x, s.x + s.w) * w;
      const y0 = Math.min(s.y, s.y + s.h) * h, y1 = Math.max(s.y, s.y + s.h) * h;
      return x >= x0 && x <= x1 && y >= y0 && y <= y1;
    }
    if (s.kind === 'circle') {
      return Math.hypot(x - s.cx * w, y - s.cy * h) <= s.r * Math.min(w, h);
    }
    let inside = false;
    const p = s.pts;
    for (let i = 0, j = p.length - 1; i < p.length; j = i++) {
      const xi = p[i][0] * w, yi = p[i][1] * h, xj = p[j][0] * w, yj = p[j][1] * h;
      if (yi > y !== yj > y && x < ((xj - xi) * (y - yi)) / (yj - yi) + xi) inside = !inside;
    }
    return inside;
  }

  private path(g: CanvasRenderingContext2D, s: Shape) {
    const { w, h } = this.size();
    g.beginPath();
    if (s.kind === 'rect') {
      g.roundRect(Math.min(s.x, s.x + s.w) * w, Math.min(s.y, s.y + s.h) * h, Math.abs(s.w) * w, Math.abs(s.h) * h, 14);
    } else if (s.kind === 'circle') {
      g.arc(s.cx * w, s.cy * h, s.r * Math.min(w, h), 0, Math.PI * 2);
    } else {
      s.pts.forEach(([x, y], i) => (i ? g.lineTo(x * w, y * h) : g.moveTo(x * w, y * h)));
      g.closePath();
    }
  }

  /** Top-left anchor for the label. */
  private anchor(s: Shape) {
    const { w, h } = this.size();
    if (s.kind === 'rect') return { x: Math.min(s.x, s.x + s.w) * w, y: Math.min(s.y, s.y + s.h) * h };
    if (s.kind === 'circle') {
      const r = s.r * Math.min(w, h);
      return { x: s.cx * w - r * 0.7, y: s.cy * h - r * 0.95 };
    }
    let x = Infinity, y = Infinity;
    for (const [px, py] of s.pts) {
      x = Math.min(x, px * w);
      y = Math.min(y, py * h);
    }
    return { x, y };
  }

  private translate(s: Shape, dx: number, dy: number) {
    if (s.kind === 'rect') {
      s.x += dx;
      s.y += dy;
    } else if (s.kind === 'circle') {
      s.cx += dx;
      s.cy += dy;
    } else {
      for (const p of s.pts) {
        p[0] += dx;
        p[1] += dy;
      }
    }
  }

  // -------------------------------------------------------------------------
  // pointer
  // -------------------------------------------------------------------------

  private down(e: PointerEvent) {
    const scr = this.screen(e);
    const p = this.px(e);
    const { w, h } = this.size();
    this.start = p;
    this.press = { x: scr.x, y: scr.y, node: null, moved: false };
    this.canvas.setPointerCapture(e.pointerId);
    if (this.tool === 'select') {
      const node = this.mesh.pick(scr.x, scr.y);
      const hit = node ? undefined : [...this.list].reverse().find((a) => this.contains(a.shape, p.x, p.y));
      this.press.node = node;
      if (hit) {
        this.select(hit.id);
        this.drag = { id: hit.id, x: p.x, y: p.y };
        this.canvas.style.cursor = 'grabbing';
      } else {
        this.pan = { x: scr.x, y: scr.y };
      }
      return;
    }
    const nx = p.x / w, ny = p.y / h;
    if (this.tool === 'rect') this.draft = { kind: 'rect', x: nx, y: ny, w: 0, h: 0 };
    else if (this.tool === 'circle') this.draft = { kind: 'circle', cx: nx, cy: ny, r: 0 };
    else this.draft = { kind: 'poly', pts: [[nx, ny]] };
  }

  private move(e: PointerEvent) {
    const scr = this.screen(e);
    const p = this.px(e);
    const { w, h } = this.size();
    if (this.press && Math.hypot(scr.x - this.press.x, scr.y - this.press.y) > 4) this.press.moved = true;
    if (this.drag) {
      const a = this.get(this.drag.id);
      if (a) this.translate(a.shape, (p.x - this.drag.x) / w, (p.y - this.drag.y) / h);
      this.drag.x = p.x;
      this.drag.y = p.y;
      return;
    }
    if (this.pan) {
      if (this.press?.moved) this.canvas.style.cursor = 'grabbing';
      this.mesh.panBy(scr.x - this.pan.x, scr.y - this.pan.y);
      this.pan = { x: scr.x, y: scr.y };
      return;
    }
    if (this.tool === 'select') {
      const overNode = this.mesh.pick(scr.x, scr.y);
      const over = this.list.some((a) => this.contains(a.shape, p.x, p.y));
      this.canvas.style.cursor = overNode ? 'pointer' : over ? 'grab' : '';
    }
    const d = this.draft;
    if (!d) return;
    if (d.kind === 'rect') {
      d.w = p.x / w - d.x;
      d.h = p.y / h - d.y;
    } else if (d.kind === 'circle') {
      d.r = Math.hypot(p.x - this.start.x, p.y - this.start.y) / Math.min(w, h);
    } else {
      const [lx, ly] = d.pts[d.pts.length - 1];
      if (Math.hypot(p.x - lx * w, p.y - ly * h) > 5 / this.mesh.view.k) d.pts.push([p.x / w, p.y / h]);
    }
  }

  private up(e: PointerEvent) {
    if (this.canvas.hasPointerCapture(e.pointerId)) this.canvas.releasePointerCapture(e.pointerId);
    const press = this.press;
    this.press = null;
    this.pan = null;
    if (this.drag) {
      this.drag = null;
      this.canvas.style.cursor = '';
      this.save();
      this.onChange();
      return;
    }
    if (this.tool === 'select') {
      this.canvas.style.cursor = '';
      // A click (not a drag) picks a phone, or clears the selection.
      if (press && !press.moved) {
        this.onNode(press.node);
        if (!press.node) this.select(null);
      }
      return;
    }
    const d = this.draft;
    this.draft = null;
    if (!d || !bigEnough(d, this.size())) return;
    const n = this.list.length;
    const a: Area = {
      id: Math.random().toString(36).slice(2, 9),
      name: `Area ${nextNumber(this.list)}`,
      color: PALETTE[n % PALETTE.length],
      sens: 'high',
      shape: d,
      level: 'calm', phones: 0, wave: 0, sway: 0, calmSince: 0,
    };
    this.list.push(a);
    this.selected = a.id;
    this.save();
    this.setTool('select');
    this.onChange();
  }

  // -------------------------------------------------------------------------
  // drawing (called by the mesh under the nodes)
  // -------------------------------------------------------------------------

  private draw(g: CanvasRenderingContext2D, now: number) {
    for (const a of this.list) {
      const sel = a.id === this.selected;
      const rgb = a.level === 'calm' ? hexRgb(a.color) : LEVEL_RGB[a.level];
      const pulse = a.level === 'danger' ? 0.16 + Math.sin(now / 200) * 0.08 : a.level === 'watch' ? 0.12 : 0.06;
      this.path(g, a.shape);
      g.fillStyle = `rgba(${rgb},${pulse})`;
      g.fill();
      g.setLineDash(sel ? [] : [7, 6]);
      g.lineDashOffset = -now / 50;
      g.lineWidth = sel ? 2.5 : a.level === 'calm' ? 1.5 : 2;
      g.strokeStyle = `rgba(${rgb},${sel ? 0.95 : 0.7})`;
      g.stroke();
      g.setLineDash([]);

      const { x, y } = this.anchor(a.shape);
      const text = `${a.sens === 'high' ? '⚑ ' : ''}${a.name} · ${a.phones}`;
      g.font = '600 12px Inter, system-ui, sans-serif';
      const tw = g.measureText(text).width;
      g.fillStyle = `rgba(${rgb},${a.level === 'calm' ? 0.22 : 0.85})`;
      g.beginPath();
      g.roundRect(x + 6, y - 11, tw + 16, 22, 11);
      g.fill();
      const light = document.documentElement.dataset.theme === 'light';
      g.fillStyle = a.level !== 'calm' ? '#ffffff' : light ? '#0f172a' : '#e6ebf2';
      g.fillText(text, x + 14, y + 4);
    }

    if (this.draft) {
      this.path(g, this.draft);
      g.fillStyle = 'rgba(34,211,238,0.08)';
      g.fill();
      g.setLineDash([6, 5]);
      g.strokeStyle = 'rgba(34,211,238,0.9)';
      g.lineWidth = 2;
      g.stroke();
      g.setLineDash([]);
    }
  }

  // -------------------------------------------------------------------------
  // persistence: this browser only
  // -------------------------------------------------------------------------

  private save() {
    try {
      const out = this.list.map(({ id, name, color, sens, shape }) => ({ id, name, color, sens, shape }));
      localStorage.setItem(STORE, JSON.stringify(out));
    } catch {
      /* storage blocked: areas last until reload */
    }
  }

  private load() {
    try {
      const raw = JSON.parse(localStorage.getItem(STORE) ?? '[]') as Pick<Area, 'id' | 'name' | 'color' | 'sens' | 'shape'>[];
      this.list = raw.map((a) => ({ ...a, level: 'calm', phones: 0, wave: 0, sway: 0, calmSince: 0 }));
    } catch {
      this.list = [];
    }
  }
}

function rank(l: AreaLevel) {
  return l === 'danger' ? 2 : l === 'watch' ? 1 : 0;
}

/** High risk: any wave phone is danger, any swaying phone is watch. Normal: a share of the area's phones. */
export function levelFor(sens: Sensitivity, phones: number, wave: number, sway: number): AreaLevel {
  if (phones === 0) return 'calm';
  if (sens === 'high') return wave > 0 ? 'danger' : sway > 0 ? 'watch' : 'calm';
  if (wave >= Math.max(1, Math.ceil(phones * 0.4))) return 'danger';
  if (wave + sway >= Math.max(1, Math.ceil(phones * 0.3))) return 'watch';
  return 'calm';
}

function bigEnough(s: Shape, { w, h }: { w: number; h: number }) {
  if (s.kind === 'rect') return Math.abs(s.w * w) > 20 && Math.abs(s.h * h) > 20;
  if (s.kind === 'circle') return s.r * Math.min(w, h) > 15;
  return s.pts.length >= 4;
}

function nextNumber(list: Area[]) {
  let n = list.length + 1;
  while (list.some((a) => a.name === `Area ${n}`)) n++;
  return n;
}
