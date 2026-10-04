// The table demo on the live map (docs/TABLE-DEMO.md).
//
// While the demo spot is on, the server runs its table demo profile for the
// live phones and says so in the snapshot (table: true). Two things it finds
// there are yellow at most and would otherwise turn a zone yellow with
// nothing on the map to show why:
//
//   - a push between two phones (a wave with pair: true): drawn here as a
//     yellow pulse travelling from the first phone to the second, labelled
//     "Push: Blue Otter → Red Fox · 240 ms" (the mesh skips these waves, so
//     they are never drawn red);
//   - a group moving as one (snapshot.together): a soft yellow band round its
//     members, dashed, labelled "Moving as one · 12 s", fading out when the
//     group goes.
//
// Plus a "Table demo" badge on the map, and a one-off zoom onto the row of
// phones (and the boards near it) so 3–5 dots 0.6 m apart are big enough to
// read on a venue-sized map. The Fit button still returns to the venue.

import type { Alert, Hardware, Node, Snapshot, Together, Wave } from '../../shared/protocol';
import type { Mesh } from './mesh';

type RGB = [number, number, number];
const rgba = (c: RGB, a: number) => `rgba(${c[0]},${c[1]},${c[2]},${a})`;
const clamp = (v: number, lo: number, hi: number) => (v < lo ? lo : v > hi ? hi : v);

/** Yellow, a little deeper on the light map so it reads on white. */
const YELLOW: Record<'dark' | 'light', RGB> = { dark: [250, 204, 21], light: [202, 138, 4] };
const NODE_R = 6.5; // the mesh's dot radius (world px)
const FADE_IN_MS = 300;
const FADE_OUT_MS = 700;
/** At most this many real phones on the map for the one-off zoom onto the row. */
const FIT_MAX_PHONES = 8;
/** Boards within this distance of the row (m) are kept in the zoomed view. */
const FIT_BOARD_M = 4;
const FIT_PAD_M = 1.2;
/** Dots and the demo spot's marks are drawn in world px and grow with the zoom: past this they crowd the labels. */
const FIT_MAX_K = 4;

/** What a table cause is called on alert cards and in the status line. */
export const CAUSE_LABEL = { pair: 'Push between two people', together: 'Moving as one (people pressed together)' } as const;

const BADGE_TIP =
  'Table demo: with the demo spot on, groups of up to 5 phones get faster push detection, and a push between just two people or a group moving as one shows as yellow, never red.';

interface Faded<T> {
  data: T;
  vis: number;
  seen: boolean;
}

export class TableLayer {
  private on = false;
  private live = false;
  private t = 0;
  private names = new Map<string, Node>();
  private pairs = new Map<string, Faded<Wave>>();
  private groups = new Map<string, Faded<Together>>();
  /** Neighbour pairs the detector compares (for the "Why did it fire?" pair of a band). */
  private links: [string, string][] = [];
  /** Each group's band outline (world px), from the last frame, for clicks. */
  private hulls = new Map<string, number[]>();
  private boards: Hardware[] = [];
  private badge: HTMLElement;
  private last = performance.now();
  /** The zoom this layer set (null = none): re-fitted as phones join while the view is still this one. */
  private fitView: { k: number; x: number; y: number } | null = null;
  private fitCount = 0;
  private fitted = false;

  constructor(private mesh: Mesh, stage: HTMLElement) {
    const css = document.createElement('style');
    css.textContent =
      '.table-badge{position:absolute;top:12px;right:12px;z-index:3;display:flex;align-items:center;gap:7px;' +
      'padding:6px 12px;border-radius:999px;background:var(--surface);border:1px solid color-mix(in srgb,var(--swaying) 55%,transparent);' +
      'box-shadow:var(--shadow-lg);font-size:12.5px;font-weight:700;color:var(--fg);cursor:help}' +
      ".table-badge::before{content:'';width:9px;height:9px;border-radius:50%;background:var(--swaying)}" +
      '.table-badge[hidden]{display:none}';
    document.head.append(css);
    this.badge = document.createElement('div');
    this.badge.className = 'table-badge';
    this.badge.hidden = true;
    this.badge.dataset.tip = BADGE_TIP;
    this.badge.setAttribute('aria-label', BADGE_TIP);
    this.badge.textContent = 'Table demo';
    stage.append(this.badge);
  }

  setBoards(list: Hardware[]) {
    this.boards = list;
  }

  update(s: Snapshot) {
    this.live = s.mode === 'live';
    this.on = !!s.table && this.live;
    this.t = s.t;
    this.badge.hidden = !this.on;
    this.links = s.links ?? [];
    this.names.clear();
    for (const n of s.nodes) this.names.set(n.id, n);
    for (const f of this.pairs.values()) f.seen = false;
    for (const f of this.groups.values()) f.seen = false;
    if (this.on) {
      for (const w of s.waves) {
        if (!w.pair) continue;
        const k = `${w.from}>${w.to}`;
        const f = this.pairs.get(k);
        if (f) {
          f.data = w;
          f.seen = true;
        } else this.pairs.set(k, { data: w, vis: 0, seen: true });
      }
      for (const g of s.together ?? []) {
        const k = [...g.members].sort().join('|');
        let f = this.groups.get(k);
        if (!f) {
          // A member joined or left: the same group, re-keyed (no second band fading out beside it).
          for (const [ok, of] of this.groups) {
            if (of.seen || !of.data.members.some((id) => g.members.includes(id))) continue;
            this.groups.delete(ok);
            this.groups.set(k, of);
            f = of;
            break;
          }
        }
        if (f) {
          f.data = g;
          f.seen = true;
        } else this.groups.set(k, { data: g, vis: 0, seen: true });
      }
    }
    this.autoFit(s);
  }

  /**
   * The table cause behind a yellow push status in zone `zone`, from what is
   * on the map now, else from the open alert that carries one.
   */
  cause(s: Snapshot, zone: string | undefined, alerts: Iterable<Alert>): 'pair' | 'together' | null {
    if (!s.table || s.mode !== 'live') return null;
    const inZone = (id: string) => !zone || this.names.get(id)?.zone === zone;
    const waves = s.waves.filter((w) => inZone(w.from) || inZone(w.to));
    if (waves.length && waves.every((w) => w.pair)) return 'pair';
    if (!waves.length && (s.together ?? []).some((g) => g.members.some(inZone))) return 'together';
    if (waves.some((w) => !w.pair)) return null;
    let best: Alert | null = null;
    for (const a of alerts) {
      if (a.source || a.test || a.status === 'resolved' || (a.kind ?? 'wave') !== 'wave' || !a.cause) continue;
      if (zone && a.zone !== zone) continue;
      if (!best || a.t > best.t) best = a;
    }
    return best?.cause ?? null;
  }

  /** A neighbour pair of the group moving as one (for "Why did it fire?" on a moving-as-one alert). */
  evidence(s: Snapshot, zone?: string): [string, string] | null {
    for (const g of s.together ?? []) {
      if (zone && !g.members.some((id) => this.names.get(id)?.zone === zone)) continue;
      const p = this.groupPair(g.members);
      if (p) return p;
    }
    return null;
  }

  /** The group band under a world point: the member pair nearest to it (a neighbour pair), else null. */
  hit(wx: number, wy: number): [string, string] | null {
    if (!this.pairs.size && !this.groups.size) return null;
    // A dot's core is the phone itself (its drawer), not the push or band around it.
    for (const id of this.names.keys()) {
      const p = this.mesh.bodyAt(id);
      if (p && Math.hypot(p.x - wx, p.y - wy) < NODE_R * 1.15) return null;
    }
    const tol = Math.max(5, 9 / this.mesh.view.k);
    for (const f of this.pairs.values()) {
      const a = this.mesh.bodyAt(f.data.from), b = this.mesh.bodyAt(f.data.to);
      if (a && b && segDist(a, b, wx, wy) < tol) return [f.data.from, f.data.to];
    }
    for (const [k, hull] of this.hulls) {
      if (!inPoly(hull, wx, wy)) continue;
      const g = this.groups.get(k);
      if (g) return this.groupPair(g.data.members, wx, wy);
    }
    return null;
  }

  private groupPair(members: string[], wx?: number, wy?: number): [string, string] | null {
    const set = new Set(members);
    let best: [string, string] | null = null;
    let bd = Infinity;
    for (const [a, b] of this.links) {
      if (!set.has(a) || !set.has(b)) continue;
      if (wx == null || wy == null) return [a, b];
      const pa = this.mesh.bodyAt(a), pb = this.mesh.bodyAt(b);
      if (!pa || !pb) continue;
      const d = Math.hypot((pa.x + pb.x) / 2 - wx, (pa.y + pb.y) / 2 - wy);
      if (d < bd) {
        bd = d;
        best = [a, b];
      }
    }
    return best ?? (members.length >= 2 ? [members[0], members[1]] : null);
  }

  // -------------------------------------------------------------------------
  // drawing (called by the mesh: below the dots, then above them)
  // -------------------------------------------------------------------------

  draw(g: CanvasRenderingContext2D, now: number, above: boolean) {
    if (!above) {
      const dt = Math.min(100, now - this.last);
      this.last = now;
      this.fade(this.pairs, dt);
      this.fade(this.groups, dt);
    }
    if (!this.pairs.size && !this.groups.size) {
      this.hulls.clear();
      return;
    }
    const theme = document.documentElement.dataset.theme === 'light' ? 'light' : 'dark';
    const col = YELLOW[theme];
    const k = this.mesh.view.k;
    if (!above) {
      this.drawBands(g, now, col);
      return;
    }
    // Over the dots: at table scale (0.6 m apart) the dots nearly touch and would hide a line drawn under them.
    this.drawPairLines(g, now, col, theme);
    this.drawPairPulses(g, now, col, theme);
    this.drawLabels(g, col, theme, k);
  }

  private fade<T>(m: Map<string, Faded<T>>, dt: number) {
    for (const [key, f] of m) {
      f.vis = clamp(f.vis + (f.seen ? dt / FADE_IN_MS : -dt / FADE_OUT_MS), 0, 1);
      if (!f.seen && f.vis <= 0) m.delete(key);
    }
  }

  private points(members: string[]) {
    const pts: { x: number; y: number }[] = [];
    for (const id of members) {
      const p = this.mesh.bodyAt(id);
      if (p) pts.push({ x: p.x, y: p.y });
    }
    return pts;
  }

  /** Soft band round each group moving as one: a padded hull, faint fill, dashed outline that drifts slowly. */
  private drawBands(g: CanvasRenderingContext2D, now: number, col: RGB) {
    this.hulls.clear();
    const pad = NODE_R + 11;
    for (const [key, f] of this.groups) {
      const pts = this.points(f.data.members);
      if (pts.length < 2) continue;
      const ring: number[] = [];
      for (const p of pts) for (let i = 0; i < 16; i++) {
        const a = (i / 16) * Math.PI * 2;
        ring.push(p.x + Math.cos(a) * pad, p.y + Math.sin(a) * pad);
      }
      const hull = convexHull(ring);
      this.hulls.set(key, hull);
      const a = f.vis;
      g.beginPath();
      for (let i = 0; i < hull.length; i += 2) (i ? g.lineTo : g.moveTo).call(g, hull[i], hull[i + 1]);
      g.closePath();
      g.fillStyle = rgba(col, 0.13 * a);
      g.fill();
      // About 3 px on screen whatever the zoom; the dashes drift slowly so the band reads as motion.
      const k = this.mesh.view.k;
      g.setLineDash([12 / k, 8 / k]);
      g.lineDashOffset = -now / (60 * k);
      g.lineWidth = 3 / k;
      g.strokeStyle = rgba(col, 0.85 * a);
      g.stroke();
      g.setLineDash([]);
      g.lineDashOffset = 0;
    }
  }

  /** The two-phone push link: a yellow line with an arrow in the direction of travel. */
  private drawPairLines(g: CanvasRenderingContext2D, _now: number, col: RGB, theme: 'dark' | 'light') {
    g.lineCap = 'round';
    const edge = theme === 'light' ? 'rgba(255,255,255,0.95)' : 'rgba(10,14,21,0.9)';
    for (const f of this.pairs.values()) {
      const a = this.mesh.bodyAt(f.data.from), b = this.mesh.bodyAt(f.data.to);
      if (!a || !b) continue;
      const dx = b.x - a.x, dy = b.y - a.y;
      const d = Math.hypot(dx, dy);
      if (d < 1) continue;
      const ux = dx / d, uy = dy / d;
      // From the edge of one dot's core to the other's, so the dots' fill still shows.
      const inset = Math.min(NODE_R * 0.8, d / 4);
      g.globalAlpha = f.vis;
      g.beginPath();
      g.moveTo(a.x + ux * inset, a.y + uy * inset);
      g.lineTo(b.x - ux * inset, b.y - uy * inset);
      g.strokeStyle = edge;
      g.lineWidth = 6;
      g.stroke();
      g.strokeStyle = rgba(col, 1);
      g.lineWidth = 3;
      g.stroke();
      {
        const mx = a.x + dx * 0.5, my = a.y + dy * 0.5;
        const s = 6;
        g.fillStyle = rgba(col, 1);
        g.strokeStyle = edge;
        g.lineWidth = 1.5;
        g.beginPath();
        g.moveTo(mx + ux * s, my + uy * s);
        g.lineTo(mx - ux * s - uy * s * 0.8, my - uy * s + ux * s * 0.8);
        g.lineTo(mx - ux * s + uy * s * 0.8, my - uy * s - ux * s * 0.8);
        g.closePath();
        g.stroke();
        g.fill();
      }
    }
    g.globalAlpha = 1;
  }

  /** A bright dot running from the first phone to the second, and a ring on the second when it lands. */
  private drawPairPulses(g: CanvasRenderingContext2D, now: number, col: RGB, theme: 'dark' | 'light') {
    for (const f of this.pairs.values()) {
      const a = this.mesh.bodyAt(f.data.from), b = this.mesh.bodyAt(f.data.to);
      if (!a || !b) continue;
      const travel = clamp(Math.abs(f.data.lagMs) * 2.2, 450, 1400);
      const period = travel + 650;
      const ph = now % period;
      g.globalAlpha = f.vis;
      if (ph < travel) {
        const e = easeInOut(ph / travel);
        const x = a.x + (b.x - a.x) * e, y = a.y + (b.y - a.y) * e;
        g.fillStyle = rgba(col, 0.35);
        g.beginPath();
        g.arc(x, y, 8, 0, Math.PI * 2);
        g.fill();
        g.fillStyle = theme === 'light' ? '#fffbeb' : '#fefce8';
        g.strokeStyle = rgba(col, 1);
        g.lineWidth = 1.5;
        g.beginPath();
        g.arc(x, y, 4, 0, Math.PI * 2);
        g.fill();
        g.stroke();
      } else {
        const r = (ph - travel) / 650;
        g.strokeStyle = rgba(col, 0.9 * (1 - r));
        g.lineWidth = 3;
        g.beginPath();
        g.arc(b.x, b.y, NODE_R + 3 + r * 3 * NODE_R, 0, Math.PI * 2);
        g.stroke();
      }
    }
    g.globalAlpha = 1;
  }

  /** "Push: A → B · 240 ms" under a push, "Moving as one · 12 s" over a group; clear of the names round the row. */
  private drawLabels(g: CanvasRenderingContext2D, col: RGB, theme: 'dark' | 'light', k: number) {
    const fs = 16 / k;
    const nameFs = 15 / k;
    // The names sit on two tiers above and below the row (mesh.drawNames): go past them.
    // The demo spot's own marks ("#1" above, "DEMO SPOT" below) are world-sized, about 30 px from the dot.
    const clear = Math.max(NODE_R + 9 / k + nameFs * 0.7 + nameFs * 1.35 + 4 / k + nameFs * 0.7 + 12 / k, NODE_R + 32 + fs * 0.8);
    g.font = `700 ${fs}px Inter, system-ui, sans-serif`;
    g.textAlign = 'center';
    g.textBaseline = 'middle';
    const pill = (text: string, x: number, y: number, a: number, solid: boolean) => {
      const w = g.measureText(text).width + 16 / k, h = fs * 1.6;
      g.globalAlpha = a;
      g.fillStyle = solid ? rgba(col, 1) : theme === 'light' ? '#ffffff' : '#0a0e15';
      g.beginPath();
      g.roundRect(x - w / 2, y - h / 2, w, h, h / 2);
      g.fill();
      g.lineWidth = 1.5 / k;
      g.strokeStyle = rgba(col, 1);
      if (!solid) g.setLineDash([4 / k, 3 / k]);
      g.stroke();
      g.setLineDash([]);
      g.fillStyle = solid ? '#1c1917' : theme === 'light' ? '#713f12' : '#fef9c3';
      g.fillText(text, x, y + fs * 0.05);
    };
    let below = 0;
    for (const f of this.pairs.values()) {
      const a = this.mesh.bodyAt(f.data.from), b = this.mesh.bodyAt(f.data.to);
      if (!a || !b) continue;
      const text = `Push: ${this.who(f.data.from)} → ${this.who(f.data.to)} · ${Math.abs(f.data.lagMs)} ms`;
      pill(text, (a.x + b.x) / 2, Math.max(a.y, b.y) + clear + below * fs * 2, f.vis, true);
      below++;
    }
    let over = 0;
    for (const f of this.groups.values()) {
      const pts = this.points(f.data.members);
      if (pts.length < 2) continue;
      const cx = pts.reduce((s, p) => s + p.x, 0) / pts.length;
      const top = Math.min(...pts.map((p) => p.y));
      const secs = Math.max(0, Math.round((this.t - f.data.since) / 1000));
      pill(`Moving as one · ${secs} s`, cx, top - clear - over * fs * 2, f.vis, false);
      over++;
    }
    g.globalAlpha = 1;
    g.textAlign = 'left';
    g.textBaseline = 'alphabetic';
  }

  private who(id: string) {
    return this.names.get(id)?.name ?? id.slice(0, 4);
  }

  // -------------------------------------------------------------------------
  // the one-off zoom onto the row
  // -------------------------------------------------------------------------

  private autoFit(s: Snapshot) {
    const real = this.on ? s.nodes.filter((n) => n.ua !== 'sim' && n.status !== 'stale' && !n.outside) : [];
    if (!this.on) {
      // Demo spot off (or not live): give the venue back if the view is still ours.
      if (this.fitView && sameView(this.mesh.view, this.fitView)) this.mesh.resetView();
      this.fitView = null;
      this.fitted = false;
      this.fitCount = 0;
      return;
    }
    if (document.body.dataset.page !== 'live') {
      // Each page starts with the whole venue (main.ts resets the view): fit again on coming back to Live.
      this.fitted = false;
      this.fitView = null;
      return;
    }
    if (!real.length || real.length > FIT_MAX_PHONES) return;
    const { w, h } = this.mesh.size();
    if (w < 50 || h < 50) return; // the map isn't on screen
    // Once; again only while the view is still the one we set (a phone joined the row).
    if (this.fitted && !(this.fitView && sameView(this.mesh.view, this.fitView) && real.length !== this.fitCount)) return;
    let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
    const add = (x: number, y: number) => {
      x0 = Math.min(x0, x);
      y0 = Math.min(y0, y);
      x1 = Math.max(x1, x);
      y1 = Math.max(y1, y);
    };
    for (const n of real) add(n.x, n.y);
    const spot = this.mesh.demoSpot;
    if (spot?.on) {
      // The whole row of five places, so the view doesn't jump as judges join.
      add(spot.x, spot.y);
      add(Math.min(this.mesh.venueSize.w, spot.x + 4 * (spot.spacing > 0 ? spot.spacing : 0.6)), spot.y);
    }
    const rx0 = x0, ry0 = y0, rx1 = x1, ry1 = y1;
    for (const b of this.boards) {
      if (b.x == null || b.y == null || b.kind === 'laptop') continue;
      const dx = Math.max(rx0 - b.x, 0, b.x - rx1), dy = Math.max(ry0 - b.y, 0, b.y - ry1);
      if (Math.hypot(dx, dy) <= FIT_BOARD_M) add(b.x, b.y);
    }
    const p0 = this.mesh.venueToWorld(x0 - FIT_PAD_M, y0 - FIT_PAD_M);
    const p1 = this.mesh.venueToWorld(x1 + FIT_PAD_M, y1 + FIT_PAD_M);
    const bw = Math.max(1, p1.x - p0.x), bh = Math.max(1, p1.y - p0.y);
    // The row takes about half the map's width, never closer than the zoom limit; labels need room above and below.
    const k = clamp(Math.min((w * 0.5) / bw, (h * 0.4) / bh), 1, FIT_MAX_K);
    const cx = (p0.x + p1.x) / 2, cy = (p0.y + p1.y) / 2;
    this.mesh.view.k = k;
    this.mesh.view.x = w / 2 - cx * k;
    this.mesh.view.y = h / 2 - cy * k;
    this.fitView = { ...this.mesh.view };
    this.fitCount = real.length;
    this.fitted = true;
  }
}

const sameView = (a: { k: number; x: number; y: number }, b: { k: number; x: number; y: number }) =>
  Math.abs(a.k - b.k) < 1e-6 && Math.abs(a.x - b.x) < 0.5 && Math.abs(a.y - b.y) < 0.5;

const easeInOut = (f: number) => (f < 0.5 ? 2 * f * f : 1 - (-2 * f + 2) ** 2 / 2);

/** Convex hull (monotone chain) of a flat [x, y, …] list, as a flat list. */
function convexHull(flat: number[]): number[] {
  const pts: [number, number][] = [];
  for (let i = 0; i < flat.length; i += 2) pts.push([flat[i], flat[i + 1]]);
  pts.sort((a, b) => a[0] - b[0] || a[1] - b[1]);
  if (pts.length < 3) return flat.slice();
  const cross = (o: [number, number], a: [number, number], b: [number, number]) => (a[0] - o[0]) * (b[1] - o[1]) - (a[1] - o[1]) * (b[0] - o[0]);
  const lower: [number, number][] = [];
  for (const p of pts) {
    while (lower.length >= 2 && cross(lower[lower.length - 2], lower[lower.length - 1], p) <= 0) lower.pop();
    lower.push(p);
  }
  const upper: [number, number][] = [];
  for (let i = pts.length - 1; i >= 0; i--) {
    const p = pts[i];
    while (upper.length >= 2 && cross(upper[upper.length - 2], upper[upper.length - 1], p) <= 0) upper.pop();
    upper.push(p);
  }
  return [...lower.slice(0, -1), ...upper.slice(0, -1)].flat();
}

function segDist(a: { x: number; y: number }, b: { x: number; y: number }, x: number, y: number) {
  const dx = b.x - a.x, dy = b.y - a.y;
  const L = dx * dx + dy * dy || 1;
  const t = clamp(((x - a.x) * dx + (y - a.y) * dy) / L, 0, 1);
  return Math.hypot(a.x + t * dx - x, a.y + t * dy - y);
}

function inPoly(flat: number[], x: number, y: number): boolean {
  let inside = false;
  for (let i = 0, j = flat.length - 2; i < flat.length; j = i, i += 2) {
    const xi = flat[i], yi = flat[i + 1], xj = flat[j], yj = flat[j + 1];
    if (yi > y !== yj > y && x < ((xj - xi) * (y - yi)) / (yj - yi) + xi) inside = !inside;
  }
  return inside;
}
