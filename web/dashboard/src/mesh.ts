// Living mesh view of the crowd on a canvas, drawn on the venue in metres.
//
// Each phone is a body that follows its real position (GPS or placed on the
// map) and jitters around it like an ant: jitter grows with the phone's real
// sway, and a detected wave shoves the receiving phone in the direction of
// travel. Bodies link to the neighbours the server's detector compares, and
// small packets gossip along those links, hop by hop. Wave edges carry fast
// red packets in the direction the push travels. Clusters (where the crowd is
// packing together) are drawn underneath with their density and trend.

import type { Cluster, Level, Node, NodeStatus, SimFrame, SimState, Wave } from '../../shared/protocol';

type RGB = [number, number, number];

const COLOR: Record<NodeStatus, RGB> = {
  ok: [52, 211, 153],
  handling: [96, 165, 250],
  swaying: [251, 191, 36],
  wave: [244, 63, 94],
  connecting: [148, 163, 184],
  stale: [71, 85, 105],
};
const WAVE: RGB = COLOR.wave;

const TRAIL_LEN = 26;
const TRAIL_EVERY = 45; // ms between trail points
const NEIGHBOURS = 3; // nearest bodies each node links to, beyond its grid neighbours
const MAX_PACKETS = 260;

interface Ripple { t0: number; color: RGB; max: number }

interface Body {
  id: string;
  data: Node;
  x: number; y: number;
  vx: number; vy: number;
  hx: number; hy: number; // home: the tapped spot, spread organically over the canvas
  heading: number;
  seed: number;
  color: RGB;
  alpha: number;
  gone: boolean;
  trail: { x: number; y: number }[];
  lastTrail: number;
  nextBeat: number;
  ripples: Ripple[];
  flash: number;
}

interface Link { a: Body; b: Body; grid: boolean }

interface Packet {
  from: Body; to: Body;
  t0: number; dur: number;
  color: RGB;
  hops: number;
  wave: boolean;
}

function hash(s: string): number {
  let h = 2166136261;
  for (let i = 0; i < s.length; i++) h = Math.imul(h ^ s.charCodeAt(i), 16777619);
  return (h >>> 0) / 4294967296;
}
const rand1 = (seed: number, k: number) => {
  const x = Math.sin(seed * 9301 + k * 49297) * 233280;
  return x - Math.floor(x);
};
const rgba = (c: RGB, a: number) => `rgba(${c[0] | 0},${c[1] | 0},${c[2] | 0},${a})`;
const lerp = (a: number, b: number, t: number) => a + (b - a) * t;
const ease = (f: number) => (f < 0.5 ? 2 * f * f : 1 - (-2 * f + 2) ** 2 / 2);
const key = (a: string, b: string) => (a < b ? `${a}|${b}` : `${b}|${a}`);

export class Mesh {
  private ctx: CanvasRenderingContext2D;
  private w = 0;
  private h = 0;
  private dpr = 1;
  private bg: HTMLCanvasElement | null = null;
  private bodies = new Map<string, Body>();
  private links: Link[] = [];
  private linkAt = 0;
  private packets: Packet[] = [];
  private waves: Wave[] = [];
  private waveSpawn = new Map<string, number>();
  private serverLinks: [string, string][] = [];
  private clusters: Cluster[] = [];
  private sim: SimFrame | null = null;
  private simGeo: SimState | null = null;
  private venue = { w: 24, h: 16 };
  /** World px per metre, and where the venue's top-left corner sits in world px. */
  private fit = { s: 30, ox: 0, oy: 0 };
  private last = performance.now();
  private hopTimes: number[] = [];
  level: Level = 'calm';
  hover: string | null = null;
  selected: string | null = null;
  private theme: 'dark' | 'light' = 'dark';
  /** Zoom and pan: screen = world * k + (x, y). */
  view = { k: 1, x: 0, y: 0 };
  /** Drawn above the background, below everything else (custom areas). */
  underlay: ((g: CanvasRenderingContext2D, now: number) => void) | null = null;

  constructor(private canvas: HTMLCanvasElement) {
    this.ctx = canvas.getContext('2d')!;
    new ResizeObserver(() => this.resize()).observe(canvas);
    this.resize();
    const tick = (now: number) => {
      this.frame(now);
      requestAnimationFrame(tick);
    };
    requestAnimationFrame(tick);
  }

  /** Links currently drawn and gossip hops per second, for the overlay. */
  stats() {
    const now = performance.now();
    this.hopTimes = this.hopTimes.filter((t) => now - t < 1000);
    return { links: this.links.length, hops: this.hopTimes.length };
  }

  update(nodes: Node[], waves: Wave[], links: [string, string][], clusters: Cluster[], venue: { w: number; h: number }) {
    const now = performance.now();
    const layoutChanged = venue.w !== this.venue.w || venue.h !== this.venue.h;
    if (layoutChanged) {
      this.venue = { w: Math.max(1, venue.w), h: Math.max(1, venue.h) };
      this.layout();
    }
    this.serverLinks = links;
    this.clusters = clusters;
    const seen = new Set<string>();
    for (const n of nodes) {
      seen.add(n.id);
      let b = this.bodies.get(n.id);
      if (!b) {
        const seed = hash(n.id);
        b = {
          id: n.id, data: n, x: 0, y: 0, vx: 0, vy: 0, hx: 0, hy: 0,
          heading: seed * Math.PI * 2, seed,
          color: [...COLOR[n.status]] as RGB, alpha: 0, gone: false,
          trail: [], lastTrail: 0, nextBeat: now + 400 + seed * 900,
          ripples: [{ t0: now, color: COLOR.ok, max: 70 }], flash: 0,
        };
        this.bodies.set(n.id, b);
        this.placeHome(b, n);
        b.x = b.hx;
        b.y = b.hy;
      } else if (b.data.status !== n.status) {
        b.ripples.push({ t0: now, color: COLOR[n.status], max: n.status === 'wave' ? 90 : 50 });
      }
      b.data = n;
      b.gone = false;
      this.placeHome(b, n);
    }
    for (const b of this.bodies.values()) if (!seen.has(b.id)) b.gone = true;
    this.waves = waves;
  }

  /** Simulated people (null when not simulating) and the venue's walls and exits. */
  setSim(frame: SimFrame | null, geo: SimState | null) {
    this.sim = frame;
    if (geo) this.simGeo = geo;
    if (!frame) this.simGeo = null;
  }

  setTheme(t: 'dark' | 'light') {
    this.theme = t;
    this.bg = null;
  }

  toWorld(sx: number, sy: number) {
    return { x: (sx - this.view.x) / this.view.k, y: (sy - this.view.y) / this.view.k };
  }

  toScreen(wx: number, wy: number) {
    return { x: wx * this.view.k + this.view.x, y: wy * this.view.k + this.view.y };
  }

  /** Zoom by factor around a screen point, clamped to 0.5×–5×. */
  zoomAt(sx: number, sy: number, factor: number) {
    const k = Math.max(0.5, Math.min(5, this.view.k * factor));
    const w = this.toWorld(sx, sy);
    this.view.k = k;
    this.view.x = sx - w.x * k;
    this.view.y = sy - w.y * k;
  }

  panBy(dx: number, dy: number) {
    this.view.x += dx;
    this.view.y += dy;
  }

  resetView() {
    this.view = { k: 1, x: 0, y: 0 };
  }

  /** Body under a screen point, if any. */
  pick(sx: number, sy: number): string | null {
    const { x, y } = this.toWorld(sx, sy);
    let best: string | null = null;
    let bd = (22 / Math.min(1, this.view.k)) ** 2;
    for (const b of this.bodies.values()) {
      const d = (b.x - x) ** 2 + (b.y - y) ** 2;
      if (d < bd && !b.gone) {
        bd = d;
        best = b.id;
      }
    }
    return best;
  }

  private resize() {
    const r = this.canvas.getBoundingClientRect();
    this.dpr = Math.min(2, window.devicePixelRatio || 1);
    this.w = Math.max(1, r.width);
    this.h = Math.max(1, r.height);
    this.canvas.width = Math.round(this.w * this.dpr);
    this.canvas.height = Math.round(this.h * this.dpr);
    this.bg = null;
    this.layout();
  }

  /** Fit the venue rectangle into the canvas, leaving room for the toolbars. */
  private layout() {
    const mx = 70, my = 50;
    const s = Math.max(4, Math.min((this.w - 2 * mx) / this.venue.w, (this.h - 2 * my) / this.venue.h));
    this.fit = { s, ox: (this.w - this.venue.w * s) / 2, oy: (this.h - this.venue.h * s) / 2 };
    for (const b of this.bodies.values()) {
      const hx = b.hx, hy = b.hy;
      this.placeHome(b, b.data);
      // Keep bodies where they are relative to their home on a resize.
      b.x += b.hx - hx;
      b.y += b.hy - hy;
    }
  }

  /** Venue metres → world px. */
  venueToWorld(x: number, y: number) {
    return { x: this.fit.ox + x * this.fit.s, y: this.fit.oy + y * this.fit.s };
  }

  /** World px → venue metres. */
  worldToVenue(x: number, y: number) {
    return { x: (x - this.fit.ox) / this.fit.s, y: (y - this.fit.oy) / this.fit.s };
  }

  /** World px per metre. */
  get scale() {
    return this.fit.s;
  }

  get venueSize() {
    return { ...this.venue };
  }

  /** Home = the phone's real position on the venue map. */
  private placeHome(b: Body, n: Node) {
    const p = this.venueToWorld(n.x, n.y);
    b.hx = p.x;
    b.hy = p.y;
  }

  private frame(now: number) {
    const dt = Math.min(0.05, (now - this.last) / 1000);
    this.last = now;
    this.step(now, dt);
    this.draw(now);
  }

  // -------------------------------------------------------------------------
  // simulation
  // -------------------------------------------------------------------------

  private step(now: number, dt: number) {
    const list = [...this.bodies.values()];
    const roam = Math.max(4, this.fit.s * 0.3); // ants wander ~30 cm around their real spot
    const space = Math.max(6, Math.min(18, this.fit.s * 0.3)); // personal space, px

    for (const b of list) {
      const st = b.data.status;
      const sway = Math.min(2, b.data.sway);
      // Ants: a heading that wanders, a little faster when the phone sways.
      b.heading += (rand1(b.seed, now * 0.0007) - 0.5) * 5 * dt + Math.sin(now * 0.0011 + b.seed * 40) * 0.9 * dt;
      const still = st === 'stale' || st === 'connecting' ? 0.15 : 1;
      const speed = (6 + sway * 22) * still;
      const tx = Math.cos(b.heading) * speed;
      const ty = Math.sin(b.heading) * speed;
      // Steer toward the wander velocity, and stay near home.
      const dx = b.hx - b.x;
      const dy = b.hy - b.y;
      const dist = Math.hypot(dx, dy);
      const pull = dist > roam ? 2.2 : 0.35;
      b.vx += ((tx - b.vx) * 1.4 + dx * pull) * dt;
      b.vy += ((ty - b.vy) * 1.4 + dy * pull) * dt;
      if (st === 'handling') {
        b.vx += (rand1(b.seed, now) - 0.5) * 900 * dt;
        b.vy += (rand1(b.seed, now + 1) - 0.5) * 900 * dt;
      }
    }

    // Personal space.
    for (let i = 0; i < list.length; i++) {
      for (let j = i + 1; j < list.length; j++) {
        const a = list[i], b = list[j];
        const dx = b.x - a.x, dy = b.y - a.y;
        const d = Math.hypot(dx, dy) || 0.01;
        if (d < space) {
          const f = ((space - d) / space) * 160 * dt;
          a.vx -= (dx / d) * f; a.vy -= (dy / d) * f;
          b.vx += (dx / d) * f; b.vy += (dy / d) * f;
        }
      }
    }

    for (const b of list) {
      const damp = Math.exp(-1.6 * dt);
      b.vx *= damp;
      b.vy *= damp;
      b.x += b.vx * dt;
      b.y += b.vy * dt;
      // Stay inside the venue (outside phones sit just beyond its edge).
      const pad = 4;
      const x0 = this.fit.ox - (b.data.outside ? 30 : -pad), x1 = this.fit.ox + this.venue.w * this.fit.s + (b.data.outside ? 30 : -pad);
      const y0 = this.fit.oy - (b.data.outside ? 30 : -pad), y1 = this.fit.oy + this.venue.h * this.fit.s + (b.data.outside ? 30 : -pad);
      b.x = Math.max(x0, Math.min(x1, b.x));
      b.y = Math.max(y0, Math.min(y1, b.y));

      const k = 1 - Math.exp(-dt / 0.2); // ~200 ms colour fade
      const target = COLOR[b.data.status];
      for (let c = 0; c < 3; c++) b.color[c] = lerp(b.color[c], target[c], k);
      const ta = b.gone ? 0 : b.data.status === 'stale' ? 0.45 : 1;
      b.alpha = lerp(b.alpha, ta, 1 - Math.exp(-dt / 0.3));
      if (b.gone && b.alpha < 0.02) this.bodies.delete(b.id);

      if (now - b.lastTrail > TRAIL_EVERY) {
        b.lastTrail = now;
        b.trail.push({ x: b.x, y: b.y });
        if (b.trail.length > TRAIL_LEN) b.trail.shift();
      }
      b.ripples = b.ripples.filter((r) => now - r.t0 < 900);
      b.flash = Math.max(0, b.flash - dt * 3);
    }

    if (now - this.linkAt > 180) {
      this.linkAt = now;
      this.relink();
    }
    this.gossip(now);
    this.spawnWavePackets(now);
    this.deliver(now);
  }

  /** Links are the neighbour pairs the server's detector compares; without them, nearest bodies. */
  private relink() {
    const live = [...this.bodies.values()].filter((b) => !b.gone);
    const maxD = this.fit.s * 2; // 2 m
    const seen = new Set<string>();
    const out: Link[] = [];
    const add = (a: Body, b: Body, grid: boolean) => {
      const k = key(a.id, b.id);
      if (seen.has(k)) return;
      seen.add(k);
      out.push({ a, b, grid });
    };
    for (const [ia, ib] of this.serverLinks) {
      const a = this.bodies.get(ia), b = this.bodies.get(ib);
      if (a && b && !a.gone && !b.gone) add(a, b, true);
    }
    if (out.length) {
      this.links = out;
      return;
    }
    for (const a of live) {
      const near = live
        .filter((b) => b !== a)
        .map((b) => ({ b, d: Math.hypot(b.x - a.x, b.y - a.y) }))
        .filter((e) => e.d < maxD)
        .sort((p, q) => p.d - q.d)
        .slice(0, NEIGHBOURS);
      for (const { b } of near) add(a, b, false);
    }
    this.links = out;
  }

  private neighbours(b: Body): Body[] {
    const out: Body[] = [];
    for (const l of this.links) {
      if (l.a === b) out.push(l.b);
      else if (l.b === b) out.push(l.a);
    }
    return out;
  }

  /** Every streaming phone shares its reading with its neighbours, who pass it on. */
  private gossip(now: number) {
    for (const b of this.bodies.values()) {
      if (now < b.nextBeat) continue;
      const st = b.data.status;
      b.nextBeat = now + (st === 'wave' ? 350 : st === 'swaying' ? 600 : 1000) + rand1(b.seed, now) * 500;
      if (b.gone || st === 'stale' || st === 'connecting') continue;
      for (const n of this.neighbours(b)) this.send(b, n, [...b.color] as RGB, 0, false, now);
    }
  }

  private spawnWavePackets(now: number) {
    for (const w of this.waves) {
      const k = `${w.from}>${w.to}`;
      if (now - (this.waveSpawn.get(k) ?? 0) < 380) continue;
      const a = this.bodies.get(w.from), b = this.bodies.get(w.to);
      if (!a || !b) continue;
      this.waveSpawn.set(k, now);
      this.send(a, b, WAVE, 0, true, now, Math.max(260, Math.min(800, w.lagMs * 1.4)));
    }
  }

  private send(from: Body, to: Body, color: RGB, hops: number, wave: boolean, now: number, dur?: number) {
    if (this.packets.length >= MAX_PACKETS && !wave) return;
    const d = Math.hypot(to.x - from.x, to.y - from.y);
    this.packets.push({ from, to, t0: now, dur: dur ?? Math.max(220, d * 3.2), color, hops, wave });
    this.hopTimes.push(now);
  }

  private deliver(now: number) {
    const arrived: Packet[] = [];
    this.packets = this.packets.filter((p) => {
      if (now - p.t0 < p.dur) return true;
      arrived.push(p);
      return false;
    });
    for (const p of arrived) {
      const to = p.to;
      to.flash = Math.min(1, to.flash + (p.wave ? 1 : 0.35));
      if (p.wave) {
        // The push reaches this person: they get shoved along the direction of travel.
        const dx = to.x - p.from.x, dy = to.y - p.from.y;
        const d = Math.hypot(dx, dy) || 1;
        to.vx += (dx / d) * 150;
        to.vy += (dy / d) * 150;
        to.ripples.push({ t0: now, color: WAVE, max: 60 });
      } else if (p.hops < 2 && Math.random() < 0.45) {
        // Relay: pass the reading on to someone who didn't send it.
        const next = this.neighbours(to).filter((n) => n !== p.from);
        if (next.length) this.send(to, next[(Math.random() * next.length) | 0], p.color, p.hops + 1, false, now);
      }
    }
  }

  // -------------------------------------------------------------------------
  // drawing
  // -------------------------------------------------------------------------

  private background() {
    if (this.bg) return this.bg;
    const c = document.createElement('canvas');
    c.width = this.canvas.width;
    c.height = this.canvas.height;
    const g = c.getContext('2d')!;
    g.scale(this.dpr, this.dpr);
    const grad = g.createRadialGradient(this.w / 2, this.h / 2, 0, this.w / 2, this.h / 2, Math.max(this.w, this.h) * 0.7);
    const light = this.theme === 'light';
    grad.addColorStop(0, light ? '#ffffff' : '#0f1520');
    grad.addColorStop(1, light ? '#eef1f5' : '#0a0e15');
    g.fillStyle = grad;
    g.fillRect(0, 0, this.w, this.h);
    g.fillStyle = light ? 'rgba(15,23,42,0.09)' : 'rgba(148,163,184,0.07)';
    for (let x = 12; x < this.w; x += 28) for (let y = 12; y < this.h; y += 28) g.fillRect(x, y, 1.4, 1.4);
    this.bg = c;
    return c;
  }

  private draw(now: number) {
    const g = this.ctx;
    g.setTransform(1, 0, 0, 1, 0, 0);
    g.drawImage(this.background(), 0, 0);
    const { k, x: vx, y: vy } = this.view;
    g.setTransform(this.dpr * k, 0, 0, this.dpr * k, this.dpr * vx, this.dpr * vy);
    const bodies = [...this.bodies.values()];
    this.drawVenue(g);
    this.drawSimGeometry(g);
    this.underlay?.(g, now);
    this.drawClusters(g, now);
    this.drawSimBodies(g);

    // GPS accuracy: the true spot is somewhere in this circle.
    for (const b of bodies) {
      const acc = b.data.acc ?? 0;
      if (acc <= 0 || b.gone) continue;
      g.fillStyle = rgba(b.color, 0.05 * b.alpha);
      g.strokeStyle = rgba(b.color, 0.18 * b.alpha);
      g.lineWidth = 1;
      g.beginPath();
      g.arc(b.hx, b.hy, acc * this.fit.s, 0, Math.PI * 2);
      g.fill();
      g.stroke();
    }

    // Heat under swaying and wave phones: where the crowd is moving.
    g.globalCompositeOperation = this.theme === 'light' ? 'source-over' : 'lighter';
    for (const b of bodies) {
      const st = b.data.status;
      if (st !== 'swaying' && st !== 'wave') continue;
      const r = this.fit.s * (st === 'wave' ? 2.4 + Math.sin(now / 180 + b.seed * 9) * 0.3 : 1.6);
      const grad = g.createRadialGradient(b.x, b.y, 0, b.x, b.y, r);
      const heat = this.theme === 'light' ? 0.45 : 1;
      grad.addColorStop(0, rgba(b.color, (st === 'wave' ? 0.2 : 0.1) * b.alpha * heat));
      grad.addColorStop(1, rgba(b.color, 0));
      g.fillStyle = grad;
      g.fillRect(b.x - r, b.y - r, r * 2, r * 2);
    }
    g.globalCompositeOperation = 'source-over';

    // Trails.
    g.lineCap = 'round';
    for (const b of bodies) {
      const t = b.trail;
      for (let i = 1; i < t.length; i++) {
        g.strokeStyle = rgba(b.color, (i / t.length) * 0.35 * b.alpha);
        g.lineWidth = 1 + (i / t.length) * 1.5;
        g.beginPath();
        g.moveTo(t[i - 1].x, t[i - 1].y);
        g.lineTo(t[i].x, t[i].y);
        g.stroke();
      }
    }

    // Mesh links.
    const waveKeys = new Set(this.waves.map((w) => key(w.from, w.to)));
    const maxD = this.fit.s * 2;
    for (const l of this.links) {
      const { a, b } = l;
      const d = Math.hypot(b.x - a.x, b.y - a.y);
      const alpha = Math.min(a.alpha, b.alpha);
      if (waveKeys.has(key(a.id, b.id))) {
        g.save();
        g.shadowColor = rgba(WAVE, 0.9);
        g.shadowBlur = 14;
        g.strokeStyle = rgba(WAVE, (0.55 + Math.sin(now / 120) * 0.2) * alpha);
        g.lineWidth = 2.6;
        g.beginPath();
        g.moveTo(a.x, a.y);
        g.lineTo(b.x, b.y);
        g.stroke();
        g.restore();
        continue;
      }
      const strength = Math.max(0.05, 1 - d / (maxD * 1.3));
      const grad = g.createLinearGradient(a.x, a.y, b.x, b.y);
      grad.addColorStop(0, rgba(a.color, (l.grid ? 0.32 : 0.16) * strength * alpha));
      grad.addColorStop(1, rgba(b.color, (l.grid ? 0.32 : 0.16) * strength * alpha));
      g.strokeStyle = grad;
      g.lineWidth = l.grid ? 1.4 : 0.9;
      g.setLineDash(l.grid ? [] : [3, 5]);
      g.lineDashOffset = -now / 60;
      g.beginPath();
      g.moveTo(a.x, a.y);
      g.lineTo(b.x, b.y);
      g.stroke();
    }
    g.setLineDash([]);

    // Packets.
    for (const p of this.packets) {
      const f = ease(Math.min(1, (now - p.t0) / p.dur));
      const x = lerp(p.from.x, p.to.x, f), y = lerp(p.from.y, p.to.y, f);
      if (p.wave) {
        const tf = Math.max(0, f - 0.3);
        g.strokeStyle = rgba(WAVE, 0.7);
        g.lineWidth = 3;
        g.beginPath();
        g.moveTo(lerp(p.from.x, p.to.x, tf), lerp(p.from.y, p.to.y, tf));
        g.lineTo(x, y);
        g.stroke();
        g.save();
        g.shadowColor = rgba(WAVE, 1);
        g.shadowBlur = 18;
        g.fillStyle = '#fff1f2';
        g.beginPath();
        g.arc(x, y, 4.5, 0, Math.PI * 2);
        g.fill();
        g.restore();
      } else {
        g.fillStyle = rgba(p.color, 0.6 - p.hops * 0.18);
        g.beginPath();
        g.arc(x, y, 2.2 - p.hops * 0.4, 0, Math.PI * 2);
        g.fill();
      }
    }

    // Nodes.
    for (const b of bodies) {
      const st = b.data.status;
      const sway = Math.min(2, b.data.sway);
      const a = b.alpha;
      let r = 7 + sway * 2.5;
      if (st === 'wave') r *= 1 + Math.sin(now / 130 + b.seed * 10) * 0.15;

      for (const rp of b.ripples) {
        const f = (now - rp.t0) / 900;
        g.strokeStyle = rgba(rp.color, (1 - f) * 0.7 * a);
        g.lineWidth = 2 * (1 - f) + 0.5;
        g.beginPath();
        g.arc(b.x, b.y, r + f * rp.max, 0, Math.PI * 2);
        g.stroke();
      }

      // Sway ring.
      g.strokeStyle = rgba(b.color, 0.35 * a);
      g.lineWidth = 1 + sway * 2.5;
      g.beginPath();
      g.arc(b.x, b.y, r + 6 + sway * 9, 0, Math.PI * 2);
      g.stroke();

      if (st === 'connecting' || b.data.outside) {
        g.save();
        g.translate(b.x, b.y);
        g.rotate(now / 400);
        g.setLineDash([4, 4]);
        g.strokeStyle = rgba(b.color, 0.9 * a);
        g.lineWidth = 2;
        g.beginPath();
        g.arc(0, 0, r + 2, 0, Math.PI * 2);
        g.stroke();
        g.restore();
        continue;
      }

      g.save();
      g.shadowColor = rgba(b.color, 0.9 * a);
      g.shadowBlur = (this.theme === 'light' ? 0 : 6) + b.flash * 8 + (st === 'wave' ? 10 : 0);
      g.fillStyle = rgba(b.color, a);
      g.beginPath();
      g.arc(b.x, b.y, r, 0, Math.PI * 2);
      g.fill();
      g.restore();
      g.strokeStyle = this.theme === 'light' ? `rgba(255,255,255,${0.9 * a})` : `rgba(10,14,21,${0.8 * a})`;
      g.lineWidth = 2;
      g.stroke();
      if (b.flash > 0.05) {
        g.fillStyle = `rgba(255,255,255,${b.flash * 0.5 * a})`;
        g.beginPath();
        g.arc(b.x, b.y, r * 0.45, 0, Math.PI * 2);
        g.fill();
      }

      if (this.hover === b.id || this.selected === b.id) {
        const sel = this.selected === b.id;
        g.strokeStyle = sel ? 'rgba(14,165,233,0.95)' : this.theme === 'light' ? 'rgba(15,23,42,0.6)' : 'rgba(226,232,240,0.9)';
        g.lineWidth = sel ? 2.5 : 1.5;
        g.setLineDash(sel ? [5, 4] : []);
        g.lineDashOffset = -now / 40;
        g.beginPath();
        g.arc(b.x, b.y, r + 16, 0, Math.PI * 2);
        g.stroke();
        g.setLineDash([]);
      }
    }

    // Links of the selected phone: who it shares readings with.
    const sel = this.selected ? this.bodies.get(this.selected) : undefined;
    if (sel) {
      g.strokeStyle = 'rgba(34,211,238,0.55)';
      g.lineWidth = 2;
      for (const n of this.neighbours(sel)) {
        g.beginPath();
        g.moveTo(sel.x, sel.y);
        g.lineTo(n.x, n.y);
        g.stroke();
      }
    }

    g.setTransform(this.dpr, 0, 0, this.dpr, 0, 0);

    // Alert vignette around the whole view.
    if (this.level !== 'calm') {
      const c = this.level === 'red' ? WAVE : COLOR.swaying;
      const pulse = (this.level === 'red' ? 0.32 + Math.sin(now / 220) * 0.14 : 0.16) * (this.theme === 'light' ? 0.45 : 1);
      const grad = g.createRadialGradient(this.w / 2, this.h / 2, Math.min(this.w, this.h) * 0.35, this.w / 2, this.h / 2, Math.max(this.w, this.h) * 0.75);
      grad.addColorStop(0, rgba(c, 0));
      grad.addColorStop(1, rgba(c, pulse));
      g.fillStyle = grad;
      g.fillRect(0, 0, this.w, this.h);
    }

    if (bodies.length === 0) {
      g.fillStyle = this.theme === 'light' ? 'rgba(71,85,105,0.8)' : 'rgba(148,163,184,0.6)';
      g.font = '500 18px Inter, system-ui, sans-serif';
      g.textAlign = 'center';
      g.fillText('Waiting for phones to join the mesh…', this.w / 2, this.h / 2);
    }
  }

  size() {
    return { w: this.w, h: this.h };
  }

  /** The venue floor: outline, 1 m grid, stage edge and a scale bar. */
  private drawVenue(g: CanvasRenderingContext2D) {
    const { s, ox, oy } = this.fit;
    const W = this.venue.w * s, H = this.venue.h * s;
    const light = this.theme === 'light';
    g.fillStyle = light ? 'rgba(255,255,255,0.7)' : 'rgba(17,24,36,0.65)';
    g.fillRect(ox, oy, W, H);
    g.strokeStyle = light ? 'rgba(15,23,42,0.06)' : 'rgba(148,163,184,0.06)';
    g.lineWidth = 1;
    const step = s < 12 ? 5 : 1; // metres between grid lines
    g.beginPath();
    for (let m = step; m < this.venue.w; m += step) {
      g.moveTo(ox + m * s, oy);
      g.lineTo(ox + m * s, oy + H);
    }
    for (let m = step; m < this.venue.h; m += step) {
      g.moveTo(ox, oy + m * s);
      g.lineTo(ox + W, oy + m * s);
    }
    g.stroke();
    g.strokeStyle = light ? 'rgba(15,23,42,0.25)' : 'rgba(148,163,184,0.28)';
    g.lineWidth = 1.5;
    g.strokeRect(ox, oy, W, H);
    // Stage along the top edge.
    g.fillStyle = light ? 'rgba(15,23,42,0.08)' : 'rgba(148,163,184,0.12)';
    g.fillRect(ox + W * 0.3, oy - 14, W * 0.4, 14);
    g.fillStyle = light ? 'rgba(15,23,42,0.5)' : 'rgba(148,163,184,0.6)';
    g.font = '600 10px Inter, system-ui, sans-serif';
    g.textAlign = 'center';
    g.fillText('STAGE', ox + W / 2, oy - 4);
    // Scale bar: 5 m.
    g.textAlign = 'left';
    g.strokeStyle = g.fillStyle;
    g.lineWidth = 2;
    g.beginPath();
    g.moveTo(ox, oy + H + 12);
    g.lineTo(ox + 5 * s, oy + H + 12);
    g.stroke();
    g.fillText(`5 m · venue ${this.venue.w} × ${this.venue.h} m`, ox + 5 * s + 8, oy + H + 16);
  }

  /** Walls, the stage barrier and exits of the simulated venue. */
  private drawSimGeometry(g: CanvasRenderingContext2D) {
    const geo = this.simGeo;
    if (!geo || !this.sim) return;
    const light = this.theme === 'light';
    g.lineCap = 'round';
    g.strokeStyle = light ? 'rgba(15,23,42,0.55)' : 'rgba(203,213,225,0.55)';
    g.lineWidth = 3;
    for (const [x0, y0, x1, y1] of geo.walls ?? []) {
      const a = this.venueToWorld(x0, y0), b = this.venueToWorld(x1, y1);
      g.beginPath();
      g.moveTo(a.x, a.y);
      g.lineTo(b.x, b.y);
      g.stroke();
    }
    g.font = '600 10px Inter, system-ui, sans-serif';
    g.textAlign = 'center';
    for (const e of geo.exits ?? []) {
      const a = this.venueToWorld(e.x0, e.y0), b = this.venueToWorld(e.x1, e.y1);
      g.strokeStyle = e.open ? 'rgba(34,197,94,0.95)' : 'rgba(239,68,68,0.95)';
      g.lineWidth = 5;
      g.setLineDash(e.open ? [] : [4, 4]);
      g.beginPath();
      g.moveTo(a.x, a.y);
      g.lineTo(b.x, b.y);
      g.stroke();
      g.setLineDash([]);
      g.fillStyle = g.strokeStyle;
      g.fillText(e.open ? 'EXIT' : 'CLOSED', (a.x + b.x) / 2, (a.y + b.y) / 2 - 6);
    }
    g.textAlign = 'left';
  }

  /** Simulated people: phone carriers are drawn as nodes; the rest are grey, tinted by how hard they're squeezed. */
  private drawSimBodies(g: CanvasRenderingContext2D) {
    if (!this.sim) return;
    const r = Math.max(2, 0.22 * this.fit.s);
    const light = this.theme === 'light';
    for (const [x, y, pressure, phone] of this.sim.bodies) {
      const p = this.venueToWorld(x, y);
      // 0 → grey; ~1500 N/m and up → red (crowd-crush pressure).
      const k = Math.min(1, pressure / 1500);
      if (k > 0.05) {
        g.fillStyle = `rgba(239,${Math.round(140 * (1 - k))},${Math.round(60 * (1 - k))},${0.25 + 0.5 * k})`;
        g.beginPath();
        g.arc(p.x, p.y, r * (1.6 + k), 0, Math.PI * 2);
        g.fill();
      }
      if (phone) continue;
      g.fillStyle = light ? 'rgba(100,116,139,0.55)' : 'rgba(148,163,184,0.45)';
      g.beginPath();
      g.arc(p.x, p.y, r * 0.8, 0, Math.PI * 2);
      g.fill();
    }
  }

  /** Where the crowd packs together: a soft disc per cluster with its density and trend. */
  private drawClusters(g: CanvasRenderingContext2D, now: number) {
    const light = this.theme === 'light';
    for (const c of this.clusters) {
      const p = this.venueToWorld(c.x, c.y);
      const r = Math.max(10, c.r * this.fit.s);
      const col: RGB = c.level === 'red' ? WAVE : c.level === 'yellow' ? COLOR.swaying : [56, 189, 248];
      const grad = g.createRadialGradient(p.x, p.y, 0, p.x, p.y, r);
      grad.addColorStop(0, rgba(col, (c.level === 'red' ? 0.22 : 0.12) * (light ? 0.7 : 1)));
      grad.addColorStop(1, rgba(col, 0));
      g.fillStyle = grad;
      g.beginPath();
      g.arc(p.x, p.y, r, 0, Math.PI * 2);
      g.fill();
      // Forming: rings converge inward. Dispersing: rings spread outward.
      if (c.trend !== 'steady') {
        const f = ((now / 1600) % 1);
        const rr = c.trend === 'forming' ? r * (1.25 - 0.45 * f) : r * (0.8 + 0.45 * f);
        g.strokeStyle = rgba(col, 0.35 * (c.trend === 'forming' ? f : 1 - f));
        g.lineWidth = 1.5;
        g.beginPath();
        g.arc(p.x, p.y, rr, 0, Math.PI * 2);
        g.stroke();
      }
      g.setLineDash([4, 4]);
      g.strokeStyle = rgba(col, 0.45);
      g.lineWidth = 1;
      g.beginPath();
      g.arc(p.x, p.y, r, 0, Math.PI * 2);
      g.stroke();
      g.setLineDash([]);
      const arrow = c.trend === 'forming' ? ' ↑' : c.trend === 'dispersing' ? ' ↓' : '';
      const label = `${c.people ?? c.count} people · ${c.density.toFixed(1)}/m²${arrow}`;
      g.font = '600 11px Inter, system-ui, sans-serif';
      const tw = g.measureText(label).width;
      g.fillStyle = light ? 'rgba(255,255,255,0.9)' : 'rgba(10,14,21,0.8)';
      g.beginPath();
      g.roundRect(p.x - tw / 2 - 7, p.y - r - 22, tw + 14, 18, 9);
      g.fill();
      g.fillStyle = rgba(col, 1);
      g.textAlign = 'center';
      g.fillText(label, p.x, p.y - r - 9);
      g.textAlign = 'left';
    }
  }

  neighbourCount(id: string) {
    const b = this.bodies.get(id);
    return b ? this.neighbours(b).length : 0;
  }

  /** Screen position of a body, for anchoring the tooltip. */
  position(id: string) {
    const b = this.bodies.get(id);
    return b ? this.toScreen(b.x, b.y) : null;
  }
}
