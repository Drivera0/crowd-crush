// Watch areas drawn by staff on the venue map: rectangles, circles or freehand
// lassos over the parts of the crowd they know are risky.
//
// Areas live on the server (GET/PUT /api/areas) in venue metres and become
// real detector zones: their level comes from the server's math, and they
// drive the briefing, the voice and the sign. "High risk" areas use lower
// thresholds there. This module only draws, edits and syncs them.

import type { Area, Node, Point, Zone } from '../../shared/protocol';
import type { Mesh } from './mesh';

export type Tool = 'select' | 'rect' | 'circle' | 'free';
export type Sensitivity = 'normal' | 'high';
export type AreaLevel = 'calm' | 'watch' | 'danger';

export interface AreaView extends Area {
  color: string;
  // live, from the server's zones and node positions
  level: AreaLevel;
  score: number;
  phones: number;
  wave: number;
  sway: number;
}

type Draft =
  | { kind: 'rect'; x0: number; y0: number; x1: number; y1: number }
  | { kind: 'circle'; cx: number; cy: number; r: number }
  | { kind: 'poly'; pts: Point[] };

const PALETTE = ['#22d3ee', '#a78bfa', '#f472b6', '#34d399', '#fb923c', '#facc15'];
const LEVEL_RGB: Record<AreaLevel, string> = { calm: '', watch: '245,158,11', danger: '239,68,68' };
const toLevel = (l: Zone['level']): AreaLevel => (l === 'red' ? 'danger' : l === 'yellow' ? 'watch' : 'calm');

function hexRgb(hex: string) {
  const n = parseInt(hex.slice(1), 16);
  return `${(n >> 16) & 255},${(n >> 8) & 255},${n & 255}`;
}

export function inPoly(poly: Point[], x: number, y: number): boolean {
  let inside = false;
  for (let i = 0, j = poly.length - 1; i < poly.length; j = i++) {
    const [xi, yi] = poly[i], [xj, yj] = poly[j];
    if (yi > y !== yj > y && x < ((xj - xi) * (y - yi)) / (yj - yi) + xi) inside = !inside;
  }
  return inside;
}

/** A detector zone that staff didn't draw: the default split of the venue (A, B, ...), with its live phone count. */
interface ZoneView extends Zone {
  phones: number;
}

export class Areas {
  list: AreaView[] = [];
  /** The default zones, drawn so it's clear which part of the map is Zone A or B (empty once staff draw areas). */
  private zones: ZoneView[] = [];
  selected: string | null = null;
  private tool: Tool = 'select';
  private draft: Draft | null = null;
  private drag: { id: string; x: number; y: number } | null = null;
  private start = { x: 0, y: 0 };
  private pan: { x: number; y: number } | null = null;
  private press: { x: number; y: number; node: string | null; moved: boolean } | null = null;
  private saveTimer = 0;

  /** Fires when areas are added, removed, renamed, or change level. */
  onChange: () => void = () => {};
  /** Fires once a newly drawn shape is committed (after onChange has rendered its row). */
  onCreated: (a: AreaView) => void = () => {};
  /** Fires when an area escalates (from the server's zone level). */
  onEscalate: (a: AreaView, from: AreaLevel) => void = () => {};
  /** Fires when the tool changes (e.g. Escape, or after finishing a shape). */
  onTool: (t: Tool) => void = () => {};
  /** Fires when a phone is clicked (null: clicked empty space). */
  onNode: (id: string | null) => void = () => {};
  /** Fires when saving to the server fails. */
  onError: (msg: string) => void = () => {};
  /**
   * One-shot map pick for simulation actions: the next press on the map is
   * delivered in venue metres (with the drag vector for direction), instead
   * of selecting, panning or drawing. Cleared after use or on Escape.
   */
  pick: { label: string; arrow: boolean; done: (p: { x: number; y: number; dx: number; dy: number }) => void } | null = null;
  private pickDrag: { x0: number; y0: number; x1: number; y1: number } | null = null;
  /** Areas are not drawn (a replay of a recording made in another venue is on screen). */
  hidden = false;

  /** Give up a pending map pick (the page changed, the simulation stopped). */
  cancelPick() {
    if (!this.pick) return;
    this.pick = null;
    this.pickDrag = null;
    this.setTool('select');
  }
  /** Fires when a neighbour link is clicked (no phone under the pointer). */
  onLink: (from: string, to: string) => void = () => {};
  /** Fires when a board marker is dropped at a new spot (venue metres). */
  onBoardMoved: (key: string, x: number, y: number) => void = () => {};
  /** May staff drag this phone on the map? (Real, named phones, outside a replay.) */
  canDragNode: (id: string) => boolean = () => false;
  /** Fires when a phone's dot is dropped at a new spot (venue metres). */
  onNodeMoved: (id: string, x: number, y: number) => void = () => {};
  /** The phone pressed on, if staff may drag it. */
  private nodePress: string | null = null;

  constructor(private canvas: HTMLCanvasElement, private mesh: Mesh) {
    canvas.addEventListener('pointerdown', (e) => this.down(e));
    canvas.addEventListener('pointermove', (e) => this.move(e));
    canvas.addEventListener('pointerup', (e) => this.up(e));
    canvas.addEventListener('pointercancel', () => {
      this.draft = this.drag = null;
      this.pan = this.press = null;
      this.nodePress = null;
      this.mesh.dragNode(null);
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
      const typing = (e.target as HTMLElement).closest?.('input, textarea, select');
      if (e.key === 'Escape') {
        this.draft = null;
        this.pick = null;
        this.pickDrag = null;
        this.setTool('select');
      } else if (!typing && e.key === 'Delete' && this.selected && this.canvas.offsetParent) {
        // Delete only (not Backspace): a stray Backspace meant for a name field must not delete an area.
        this.remove(this.selected);
      }
    });
    mesh.underlay = (g, now) => this.draw(g, now);
  }

  get drawing() {
    return this.tool !== 'select';
  }

  /** Load the areas stored on the server. */
  async load() {
    try {
      const r = await fetch('/api/areas');
      if (!r.ok) return;
      const areas = (await r.json()) as Area[];
      this.list = areas.map((a, i) => this.view(a, i));
      this.onChange();
    } catch {
      /* server restarting: keep what we have */
    }
  }

  private view(a: Area, i: number): AreaView {
    const prev = this.get(a.id);
    return {
      ...a,
      color: prev?.color ?? PALETTE[i % PALETTE.length],
      level: prev?.level ?? 'calm',
      score: prev?.score ?? 0,
      phones: prev?.phones ?? 0,
      wave: prev?.wave ?? 0,
      sway: prev?.sway ?? 0,
    };
  }

  /** Push the current list to the server (debounced: drags send once at the end). */
  private save() {
    window.clearTimeout(this.saveTimer);
    this.saveTimer = window.setTimeout(async () => {
      // Rules must go too: leaving them out wiped every area's rules on the next save.
      const body: Area[] = this.list.map(({ id, name, sens, poly, light, rules }) => ({
        id, name, sens, poly, ...(light ? { light } : {}), ...(rules ? { rules } : {}),
      }));
      try {
        const r = await fetch('/api/areas', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(body),
        });
        if (!r.ok) {
          const j = (await r.json().catch(() => ({}))) as { error?: string };
          this.onError(`Couldn't save areas: ${j.error ?? r.statusText}`);
        }
      } catch {
        this.onError("Couldn't reach the server to save areas.");
      }
    }, 250);
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
    a.name = name.trim().slice(0, 40) || a.name;
    this.save();
    this.onChange();
  }

  setSens(id: string, sens: Sensitivity) {
    const a = this.get(id);
    if (!a || a.sens === sens) return;
    a.sens = sens;
    this.save();
    this.onChange();
  }

  /** Replace an area's alert rules. */
  setRules(id: string, rules: Area['rules']) {
    const a = this.get(id);
    if (!a) return;
    a.rules = rules;
    this.save();
    this.onChange();
  }

  /** Which zone light shows this area ("" = none). */
  setLight(id: string, light: string) {
    const a = this.get(id);
    if (!a) return;
    a.light = light || undefined;
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

  /** Take levels from the server's zones and count the phones inside each area. */
  sync(zones: Zone[], nodes: Node[]) {
    const byId = new Map(zones.map((z) => [z.id, z]));
    const inside = (poly: Point[]) =>
      nodes.filter((n) => n.status !== 'stale' && !n.outside && inPoly(poly, n.x, n.y)).length;
    this.zones = zones
      .filter((z) => !z.custom && z.id !== 'rest' && z.poly?.length >= 3)
      .map((z) => ({ ...z, phones: inside(z.poly) }));
    let changed = false;
    for (const a of this.list) {
      const z = byId.get(a.id);
      let phones = 0, wave = 0, sway = 0;
      for (const n of nodes) {
        if (n.status === 'stale' || n.outside || !inPoly(a.poly, n.x, n.y)) continue;
        phones++;
        if (n.status === 'wave') wave++;
        else if (n.status === 'swaying') sway++;
      }
      const level = z ? toLevel(z.level) : 'calm';
      const prev = a.level;
      if (level !== prev || phones !== a.phones || wave !== a.wave || sway !== a.sway) changed = true;
      Object.assign(a, { level, score: z?.score ?? 0, phones, wave, sway });
      if (rank(level) > rank(prev)) this.onEscalate(a, prev);
    }
    if (changed) this.onChange();
  }

  /** Worst level across all areas. */
  worst(): AreaLevel {
    return this.list.reduce<AreaLevel>((w, a) => (rank(a.level) > rank(w) ? a.level : w), 'calm');
  }

  // -------------------------------------------------------------------------
  // geometry: pointer → world px → venue metres
  // -------------------------------------------------------------------------

  private screen(e: { clientX: number; clientY: number }) {
    const r = this.canvas.getBoundingClientRect();
    return { x: e.clientX - r.left, y: e.clientY - r.top };
  }

  /** Pointer position in venue metres. */
  private metres(e: PointerEvent) {
    const s = this.screen(e);
    const w = this.mesh.toWorld(s.x, s.y);
    return this.mesh.worldToVenue(w.x, w.y);
  }

  private clampPt([x, y]: Point): Point {
    const v = this.mesh.venueSize;
    const r = (n: number) => Math.round(n * 100) / 100;
    return [r(Math.max(0, Math.min(v.w, x))), r(Math.max(0, Math.min(v.h, y)))];
  }

  private toPoly(d: Draft): Point[] {
    if (d.kind === 'rect') {
      const { x0, y0, x1, y1 } = d;
      return [[x0, y0], [x1, y0], [x1, y1], [x0, y1]].map((p) => this.clampPt(p as Point));
    }
    if (d.kind === 'circle') {
      const out: Point[] = [];
      for (let i = 0; i < 24; i++) {
        const a = (i / 24) * Math.PI * 2;
        out.push(this.clampPt([d.cx + Math.cos(a) * d.r, d.cy + Math.sin(a) * d.r]));
      }
      return out;
    }
    // Thin the freehand line to points at least 25 cm apart.
    const out: Point[] = [];
    for (const p of d.pts) {
      const last = out[out.length - 1];
      if (!last || Math.hypot(p[0] - last[0], p[1] - last[1]) >= 0.25) out.push(this.clampPt(p));
    }
    return out;
  }

  private bigEnough(d: Draft) {
    if (d.kind === 'rect') return Math.abs(d.x1 - d.x0) > 0.5 && Math.abs(d.y1 - d.y0) > 0.5;
    if (d.kind === 'circle') return d.r > 0.4;
    return this.toPoly(d).length >= 4;
  }

  private path(g: CanvasRenderingContext2D, poly: Point[]) {
    g.beginPath();
    poly.forEach(([x, y], i) => {
      const p = this.mesh.venueToWorld(x, y);
      if (i) g.lineTo(p.x, p.y);
      else g.moveTo(p.x, p.y);
    });
    g.closePath();
  }

  // -------------------------------------------------------------------------
  // pointer
  // -------------------------------------------------------------------------

  private down(e: PointerEvent) {
    const scr = this.screen(e);
    const p = this.metres(e);
    if (this.pick) {
      this.canvas.setPointerCapture(e.pointerId);
      this.pickDrag = { x0: p.x, y0: p.y, x1: p.x, y1: p.y };
      return;
    }
    this.start = p;
    this.press = { x: scr.x, y: scr.y, node: null, moved: false };
    this.canvas.setPointerCapture(e.pointerId);
    const board = this.tool === 'select' ? this.mesh.boardAt(scr.x, scr.y) : null;
    if (board) {
      this.mesh.boardDrag = { key: board, x: p.x, y: p.y };
      this.canvas.style.cursor = 'grabbing';
      return;
    }
    if (this.tool === 'select') {
      const node = this.mesh.pick(scr.x, scr.y);
      const hit = node ? undefined : [...this.list].reverse().find((a) => inPoly(a.poly, p.x, p.y));
      this.press.node = node;
      this.nodePress = node && this.canDragNode(node) ? node : null;
      if (this.nodePress) return; // a drag moves the phone; a click opens it
      if (hit) {
        this.select(hit.id);
        this.drag = { id: hit.id, x: p.x, y: p.y };
        this.canvas.style.cursor = 'grabbing';
      } else {
        this.pan = { x: scr.x, y: scr.y };
      }
      return;
    }
    if (this.tool === 'rect') this.draft = { kind: 'rect', x0: p.x, y0: p.y, x1: p.x, y1: p.y };
    else if (this.tool === 'circle') this.draft = { kind: 'circle', cx: p.x, cy: p.y, r: 0 };
    else this.draft = { kind: 'poly', pts: [[p.x, p.y]] };
  }

  private move(e: PointerEvent) {
    const scr = this.screen(e);
    const p = this.metres(e);
    if (this.pickDrag) {
      this.pickDrag.x1 = p.x;
      this.pickDrag.y1 = p.y;
      return;
    }
    if (this.mesh.boardDrag) {
      const [x, y] = this.clampPt([p.x, p.y]);
      this.mesh.boardDrag.x = x;
      this.mesh.boardDrag.y = y;
      return;
    }
    if (this.tool === 'select' && this.mesh.boardAt(scr.x, scr.y)) {
      this.canvas.style.cursor = 'grab';
      return;
    }
    if (this.press && Math.hypot(scr.x - this.press.x, scr.y - this.press.y) > 4) this.press.moved = true;
    if (this.nodePress && this.press) {
      if (this.press.moved) {
        const [x, y] = this.clampPt([p.x, p.y]);
        this.mesh.dragNode(this.nodePress, x, y);
        this.canvas.style.cursor = 'grabbing';
      }
      return;
    }
    if (this.drag) {
      const a = this.get(this.drag.id);
      if (a) {
        const dx = p.x - this.drag.x, dy = p.y - this.drag.y;
        a.poly = a.poly.map(([x, y]) => [x + dx, y + dy]);
      }
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
      const over = this.list.some((a) => inPoly(a.poly, p.x, p.y));
      this.canvas.style.cursor = overNode ? 'pointer' : over ? 'grab' : '';
    }
    const d = this.draft;
    if (!d) return;
    if (d.kind === 'rect') {
      d.x1 = p.x;
      d.y1 = p.y;
    } else if (d.kind === 'circle') {
      d.r = Math.hypot(p.x - this.start.x, p.y - this.start.y);
    } else {
      const [lx, ly] = d.pts[d.pts.length - 1];
      if (Math.hypot(p.x - lx, p.y - ly) > 0.1) d.pts.push([p.x, p.y]);
    }
  }

  private up(e: PointerEvent) {
    if (this.canvas.hasPointerCapture(e.pointerId)) this.canvas.releasePointerCapture(e.pointerId);
    if (this.pick && this.pickDrag) {
      const { x0, y0, x1, y1 } = this.pickDrag;
      const done = this.pick.done;
      this.pick = null;
      this.pickDrag = null;
      done({ x: x0, y: y0, dx: x1 - x0, dy: y1 - y0 });
      return;
    }
    const press = this.press;
    this.press = null;
    this.pan = null;
    const nd = this.nodePress;
    this.nodePress = null;
    if (nd && this.mesh.nodeDrag) {
      this.canvas.style.cursor = '';
      this.onNodeMoved(nd, this.mesh.nodeDrag.x, this.mesh.nodeDrag.y);
      return;
    }
    const bd = this.mesh.boardDrag;
    if (bd) {
      this.canvas.style.cursor = '';
      if (press?.moved) this.onBoardMoved(bd.key, bd.x, bd.y);
      else this.mesh.boardDrag = null;
      return;
    }
    if (this.drag) {
      const a = this.get(this.drag.id);
      if (a) a.poly = a.poly.map((pt) => this.clampPt(pt));
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
        // On a dot: the phone, unless it's off the dot's core onto a table-demo push or band (tablelayer.ts).
        const link = press.node ? (this.mesh.table?.hit(this.mesh.toWorld(press.x, press.y).x, this.mesh.toWorld(press.x, press.y).y) ?? null) : this.mesh.linkUnder(press.x, press.y);
        if (link) {
          this.onLink(link[0], link[1]);
          return;
        }
        this.onNode(press.node);
        if (!press.node) this.select(null);
      }
      return;
    }
    const d = this.draft;
    this.draft = null;
    if (!d || !this.bigEnough(d)) return;
    const a = this.view(
      { id: Math.random().toString(36).slice(2, 9), name: `Area ${nextNumber(this.list)}`, sens: 'normal', poly: this.toPoly(d) },
      this.list.length,
    );
    this.list.push(a);
    this.selected = a.id;
    this.save();
    this.setTool('select');
    this.onChange();
    this.onCreated(a);
  }

  // -------------------------------------------------------------------------
  // drawing (called by the mesh under the nodes)
  // -------------------------------------------------------------------------

  /**
   * The default zones: a dashed outline, a large faint letter in the middle and
   * a label with the phone count (and the level when not calm), tinted when the zone is on watch or
   * in danger. Drawn even when areas are hidden: a simulated classroom or
   * stadium gate has its own zones.
   */
  private drawZones(g: CanvasRenderingContext2D) {
    const light = document.documentElement.dataset.theme === 'light';
    const ink = light ? '15,23,42' : '230,235,242';
    for (const z of this.zones) {
      const level = toLevel(z.level);
      const rgb = level === 'calm' ? ink : LEVEL_RGB[level];
      let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
      for (const [px, py] of z.poly) {
        const w = this.mesh.venueToWorld(px, py);
        x0 = Math.min(x0, w.x); y0 = Math.min(y0, w.y);
        x1 = Math.max(x1, w.x); y1 = Math.max(y1, w.y);
      }
      this.path(g, z.poly);
      if (level !== 'calm') {
        g.fillStyle = `rgba(${rgb},${level === 'danger' ? 0.1 : 0.07})`;
        g.fill();
      }
      g.setLineDash(level === 'calm' ? [10, 8] : []);
      g.lineWidth = level === 'danger' ? 3 : level === 'watch' ? 2.5 : 1.5;
      g.strokeStyle = `rgba(${rgb},${level === 'calm' ? 0.5 : 0.9})`;
      g.stroke();
      g.setLineDash([]);

      // The zone's letter, big and faint, behind the crowd.
      const big = Math.min(x1 - x0, y1 - y0) * 0.45;
      if (big >= 24) {
        g.font = `700 ${Math.min(big, 220)}px Inter, system-ui, sans-serif`;
        g.textAlign = 'center';
        g.textBaseline = 'middle';
        g.fillStyle = `rgba(${rgb},${level === 'calm' ? 0.09 : 0.16})`;
        g.fillText(z.id.length <= 2 ? z.id : z.name, (x0 + x1) / 2, (y0 + y1) / 2);
        g.textAlign = 'start';
        g.textBaseline = 'alphabetic';
      }

      // Label at the bottom middle of the zone: the stage and the crowd badges sit at the top.
      const word = level === 'danger' ? ' · danger' : level === 'watch' ? ' · watch' : '';
      const text = `${z.name}${word} · ${z.phones} phone${z.phones === 1 ? '' : 's'}`;
      g.font = '600 12px Inter, system-ui, sans-serif';
      const tw = g.measureText(text).width;
      const lx = (x0 + x1) / 2 - tw / 2 - 8, ly = y1 - 34;
      g.fillStyle = `rgba(${rgb},${level === 'calm' ? 0.14 : 0.85})`;
      g.beginPath();
      g.roundRect(lx, ly, tw + 16, 22, 11);
      g.fill();
      g.fillStyle = level !== 'calm' ? '#ffffff' : light ? '#0f172a' : '#e6ebf2';
      g.fillText(text, lx + 8, ly + 15);
    }
  }

  private draw(g: CanvasRenderingContext2D, now: number) {
    this.drawZones(g);
    if (this.hidden) return;
    const light = document.documentElement.dataset.theme === 'light';
    for (const a of this.list) {
      if (a.poly.length < 3) continue;
      const sel = a.id === this.selected;
      const rgb = a.level === 'calm' ? hexRgb(a.color) : LEVEL_RGB[a.level];
      // A light, static fill: the map's heat map shows where in the area the crowd is.
      // Danger and watch get a crisp solid outline instead of a pulsing wash.
      const alerting = a.level !== 'calm';
      const fill = a.level === 'danger' ? 0.09 : a.level === 'watch' ? 0.07 : 0.06;
      this.path(g, a.poly);
      g.fillStyle = `rgba(${rgb},${fill})`;
      g.fill();
      g.setLineDash(sel || alerting ? [] : [7, 6]);
      g.lineDashOffset = -now / 50;
      g.lineWidth = a.level === 'danger' ? 3 : sel || alerting ? 2.5 : 1.5;
      g.strokeStyle = `rgba(${rgb},${sel || alerting ? 0.95 : 0.7})`;
      g.stroke();
      g.setLineDash([]);

      // Label at the top-left of the area.
      let x = Infinity, y = Infinity;
      for (const [px, py] of a.poly) {
        const w = this.mesh.venueToWorld(px, py);
        x = Math.min(x, w.x);
        y = Math.min(y, w.y);
      }
      const text = `${a.sens === 'high' ? '⚑ ' : ''}${a.name}${a.phones ? ` · ${a.phones} inside` : ''}`;
      g.font = '600 12px Inter, system-ui, sans-serif';
      const tw = g.measureText(text).width;
      g.fillStyle = `rgba(${rgb},${a.level === 'calm' ? 0.22 : 0.85})`;
      g.beginPath();
      g.roundRect(x + 6, y - 11, tw + 16, 22, 11);
      g.fill();
      g.fillStyle = a.level !== 'calm' ? '#ffffff' : light ? '#0f172a' : '#e6ebf2';
      g.fillText(text, x + 14, y + 4);
    }

    if (this.pickDrag && this.pick?.arrow) {
      const a = this.mesh.venueToWorld(this.pickDrag.x0, this.pickDrag.y0);
      const b = this.mesh.venueToWorld(this.pickDrag.x1, this.pickDrag.y1);
      const ang = Math.atan2(b.y - a.y, b.x - a.x);
      g.strokeStyle = 'rgba(239,68,68,0.9)';
      g.fillStyle = 'rgba(239,68,68,0.9)';
      g.lineWidth = 3;
      g.beginPath();
      g.moveTo(a.x, a.y);
      g.lineTo(b.x, b.y);
      g.stroke();
      g.beginPath();
      g.moveTo(b.x, b.y);
      g.lineTo(b.x - 12 * Math.cos(ang - 0.45), b.y - 12 * Math.sin(ang - 0.45));
      g.lineTo(b.x - 12 * Math.cos(ang + 0.45), b.y - 12 * Math.sin(ang + 0.45));
      g.closePath();
      g.fill();
    }

    if (this.draft) {
      this.path(g, this.toPoly(this.draft).length >= 2 ? this.toPoly(this.draft) : []);
      g.fillStyle = 'rgba(59,130,246,0.08)';
      g.fill();
      g.setLineDash([6, 5]);
      g.strokeStyle = 'rgba(59,130,246,0.9)';
      g.lineWidth = 2;
      g.stroke();
      g.setLineDash([]);
    }
  }
}

function rank(l: AreaLevel) {
  return l === 'danger' ? 2 : l === 'watch' ? 1 : 0;
}

function nextNumber(list: Area[]) {
  let n = list.length + 1;
  while (list.some((a) => a.name === `Area ${n}`)) n++;
  return n;
}
