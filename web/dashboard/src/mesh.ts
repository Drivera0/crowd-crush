// Live crowd map on a canvas, drawn on the venue in metres.
//
// Motion is the truth, smoothed: each phone's dot follows its latest reported
// position (GPS or placed on the map) with a critically damped follow, so it
// never overshoots, and big re-placements (> 3 m) ease over ~400 ms. A phone's
// real sway only adds a few centimetres of idle motion. Simulated people
// arrive at 10 Hz and are interpolated between frames; phone nodes in a
// simulation ride exactly on their simulated body.
//
// Danger is drawn where it is: a smoothed density heat map (kernel density of
// people, coloured only above the watch density and red only above danger),
// a crisp pulsing hull around each yellow/red cluster with a badge, and push
// waves that light up only the links and phones they travel through.
//
// Each dot's fill says how packed in that person is (the server's per-phone
// crush level; for simulated people their true pressure and density): pale
// when standing free, amber as it gets tight, red and then deep red in the
// crushed core, growing a little and glowing as it goes. A crowd that has
// stopped moving is the most dangerous state there is, so the fill never
// depends on motion. Motion is the ring around the dot: green = streaming and
// still, yellow = swaying, red and pulsing = in a push, blue = phone in hand.
//
// Real phones carry a generated name and colour ("Blue Otter"): a ring in
// that colour, the name next to the dot while there are few of them, and a
// burst of ripples while the phone is being shaken. Staff can drag one to
// where it really is.

import type { Cluster, Hardware, Level, Node, NodeStatus, SimFrame, SimFurniture, SimState, VenueLayout, Wave } from '../../shared/protocol';
import { drawNearLabels } from './nearlabels';

type RGB = [number, number, number];

const COLOR: Record<NodeStatus, RGB> = {
  ok: [52, 211, 153],
  handling: [96, 165, 250],
  swaying: [251, 191, 36],
  wave: [244, 63, 94],
  connecting: [148, 163, 184],
  stale: [71, 85, 105],
};
const STATUSES = Object.keys(COLOR) as NodeStatus[];
const WAVE: RGB = COLOR.wave;
const AMBER: RGB = [245, 158, 11];
const ORANGE: RGB = [249, 115, 22];
const RED: RGB = [239, 68, 68];
const CALM_CLUSTER: RGB = [56, 189, 248];
/** Opaque colour per status; alpha goes through globalAlpha so the hot loops build no strings. */
const SOLID = Object.fromEntries(STATUSES.map((s) => [s, `rgb(${COLOR[s].join(',')})`])) as Record<NodeStatus, string>;

// Server thresholds (server/internal/detect/config.go: DensityWatch, DensityDanger), people per m².
const DENSITY_WATCH = 2;
const DENSITY_DANGER = 4;

// The crush ramp: 0 (standing free) → 1 (crushed). Lightness falls all the
// way along it, so it reads without colour vision; the stops sit where the
// server's Crush01 puts the watch (0.35) and danger (0.7) densities.
const CRUSH_STEPS = 32;
const CRUSH_RAMP: Record<'dark' | 'light', [number, RGB][]> = {
  dark: [[0, [226, 232, 240]], [0.18, [254, 240, 138]], [0.35, [250, 204, 21]], [0.52, [249, 115, 22]], [0.7, [239, 68, 68]], [0.85, [220, 38, 38]], [1, [176, 20, 50]]],
  light: [[0, [241, 245, 249]], [0.18, [254, 240, 138]], [0.35, [250, 204, 21]], [0.52, [249, 115, 22]], [0.7, [220, 38, 38]], [0.85, [185, 28, 28]], [1, [127, 29, 29]]],
};

/** Colour of the crush ramp at c (0..1). */
export function crushRGB(c: number, theme: 'dark' | 'light'): RGB {
  const stops = CRUSH_RAMP[theme];
  const v = clamp(c, 0, 1);
  for (let i = 1; i < stops.length; i++) {
    if (v <= stops[i][0]) {
      const [a, ca] = stops[i - 1], [b, cb] = stops[i];
      return mix(ca, cb, (v - a) / (b - a));
    }
  }
  return stops[stops.length - 1][1];
}

/** The server's Crush01 (crowd/packed.go) at the default thresholds: density (people/m²) → 0..1. */
export function crush01(d: number): number {
  const w = DENSITY_WATCH, g = DENSITY_DANGER;
  if (d <= w / 2) return 0;
  if (d <= w) return (0.35 * (d - w / 2)) / (w / 2);
  if (d <= g) return 0.35 + (0.35 * (d - w)) / (g - w);
  return Math.min(1, 0.7 + (0.3 * (d - g)) / (0.5 * g));
}

/**
 * How crushed a simulated person is, from the truth the simulator knows:
 * their packed density, raised by the pressure on their body (200 N/m is a
 * firm squeeze, 1600 N/m is where people get hurt: Helbing et al. 2000).
 */
export function bodyCrush(pressure: number, density: number): number {
  const c = crush01(density);
  return pressure >= 200 ? Math.max(c, 0.55 + 0.45 * Math.min(1, pressure / 1600)) : c;
}
// Density field: kernel density estimate on a coarse grid.
const FIELD_CELL = 0.5; // m (grown for big venues so the grid stays ≤ FIELD_MAX_DIM cells a side)
const FIELD_MAX_DIM = 320;
const FIELD_SIGMA = 1; // m, Gaussian kernel
const FIELD_EVERY = 120; // ms between refreshes (~8 Hz)
const FIELD_TAU = 0.45; // s, temporal smoothing
const FIELD_LUT_MAX = 8; // people/m² at the top of the colour table

const NODE_R = 6.5; // world px
const TELEPORT_M = 3; // a jump bigger than this eases instead of following
const TELEPORT_MS = 400;
const FADE_IN_MS = 350;
const FADE_OUT_MS = 450;
const COLOR_FADE_MS = 200;
const IDLE_MAX_M = 0.04; // sway idle motion, at most 4 cm
const SIM_SNAP_M = 1.5; // a sim body that moved further than this in one frame is a different person: snap
/** Simulated body buffers: x, y, pressure, heading (rad) per person, interpolated between frames. */
const SIM_STRIDE = 4;
/** Body states on the wire (crowdsim/person.go). */
const ST_STANDING = 0, ST_WALKING = 1, ST_SEATED = 2, ST_QUEUEING = 3, ST_PUSHING = 4;
const SIM_MATCH_M = 1.5; // phone node ↔ simulated body matching radius
const NEIGHBOURS = 3; // nearest bodies each node links to when the server sends no links
const MAX_PACKETS = 260;

interface Ripple { t0: number; style: string; max: number; w?: number }

/** Names are drawn on the map while at most this many real phones are connected (otherwise on hover / selection). */
const NAMES_MAX = 8;
const SHAKE_RIPPLE_MS = 260;

interface Body {
  id: string;
  data: Node;
  seed: number;
  /** Latest reported position (venue m). */
  rx: number; ry: number;
  /** Follow target (venue m): the report, or the matched simulated body. */
  tx: number; ty: number;
  /** Displayed position (venue m) and its follow velocity (m/s). */
  px: number; py: number;
  vx: number; vy: number;
  /** Ease of a big jump: start time (−1 = none) and where it started. */
  tw0: number; twx: number; twy: number;
  /** When the report last moved, and the typical gap between moves (ms). */
  movedAt: number; interval: number;
  /** Drawn position in world px (with the idle sway), and the reported spot in world px. */
  x: number; y: number;
  hx: number; hy: number;
  sway: number;
  /** How packed in, 0..1, eased toward the server's value (or the simulated body's truth). */
  crush: number;
  status: NodeStatus;
  prevStatus: NodeStatus;
  statusAt: number;
  stale: number; // 1 → 0.45 when offline
  vis: number; // 0..1 fade in/out
  gone: boolean;
  /** When it left the snapshot (performance.now()). */
  goneAt: number;
  ripples: Ripple[];
  flash: number;
  nextBeat: number;
  simIdx: number;
  nbrs: Body[];
  /** When the last shake ripple was started. */
  shakeAt: number;
}

interface Link { a: Body; b: Body; grid: boolean; wave: boolean }

interface Packet {
  from: Body; to: Body;
  t0: number; dur: number;
  style: string;
  hops: number;
  wave: boolean;
}

function hash(s: string): number {
  let h = 2166136261;
  for (let i = 0; i < s.length; i++) h = Math.imul(h ^ s.charCodeAt(i), 16777619);
  return (h >>> 0) / 4294967296;
}
const rgba = (c: RGB, a: number) => `rgba(${c[0] | 0},${c[1] | 0},${c[2] | 0},${a})`;
const lerp = (a: number, b: number, t: number) => a + (b - a) * t;
const clamp = (v: number, lo: number, hi: number) => (v < lo ? lo : v > hi ? hi : v);
/** Interpolate an angle (rad) the short way round. */
function lerpAngle(a: number, b: number, f: number): number {
  let d = (b - a) % (Math.PI * 2);
  if (d > Math.PI) d -= Math.PI * 2;
  else if (d < -Math.PI) d += Math.PI * 2;
  return a + d * f;
}
const ease = (f: number) => (f < 0.5 ? 4 * f * f * f : 1 - (-2 * f + 2) ** 3 / 2);
const easeOut = (f: number) => 1 - (1 - f) ** 3;
const smooth = (e0: number, e1: number, x: number) => {
  const t = clamp((x - e0) / (e1 - e0), 0, 1);
  return t * t * (3 - 2 * t);
};
const mix = (a: RGB, b: RGB, t: number): RGB => [lerp(a[0], b[0], t), lerp(a[1], b[1], t), lerp(a[2], b[2], t)];
const key = (a: string, b: string) => (a < b ? `${a}|${b}` : `${b}|${a}`);

/** Colour of the density heat map at d people/m²: nothing below watch, amber → orange, red only past danger. */
function fieldColor(d: number, light: boolean): [number, number, number, number] {
  const a = smooth(DENSITY_WATCH, DENSITY_WATCH + 0.6, d) * (0.26 + 0.24 * smooth(DENSITY_DANGER - 0.15, DENSITY_DANGER + 0.15, d));
  let c = mix(AMBER, ORANGE, smooth(DENSITY_WATCH + 0.5, DENSITY_DANGER - 0.4, d));
  c = mix(c, RED, smooth(DENSITY_DANGER - 0.15, DENSITY_DANGER + 0.15, d));
  return [c[0], c[1], c[2], Math.min(1, a * (light ? 1.15 : 1))];
}

export class Mesh {
  private ctx: CanvasRenderingContext2D;
  private w = 0;
  private h = 0;
  private dpr = 1;
  private bg: HTMLCanvasElement | null = null;
  private bodies = new Map<string, Body>();
  private list: Body[] = [];
  private listDirty = false;
  private links: Link[] = [];
  private linkAt = 0;
  private packets: Packet[] = [];
  private waves: Wave[] = [];
  private waveKeys = new Set<string>();
  private waveNodes = new Set<string>();
  private waveSpawn = new Map<string, number>();
  private serverLinks: [string, string][] = [];
  private clusters: Cluster[] = [];
  private clusterLabels: string[] = [];
  /** Estimated people per phone (cluster people ÷ count), for the density field. */
  private perPhone = 1;
  private sim: SimFrame | null = null;
  private simGeo: SimState | null = null;
  // Simulated bodies, stride SIM_STRIDE (x, y, pressure, heading), venue m: interpolated from prev to cur over one frame gap.
  private simPrev = new Float32Array(0);
  private simCur = new Float32Array(0);
  private simDraw = new Float32Array(0);
  private simPhone = new Uint8Array(0);
  /** State of each simulated body (ST_STANDING … ST_PUSHING, latest frame). */
  private simState = new Uint8Array(0);
  /** Packed density of each simulated body (people/m², latest frame; not interpolated). */
  private simDens = new Float32Array(0);
  private simClaim = new Uint8Array(0);
  private simBucket = new Uint8Array(0);
  private simOldP = new Float32Array(0);
  private simOldC = new Float32Array(0);
  private simRemap = new Int32Array(0);
  private simN = 0;
  private simT = NaN;
  private simAt = 0;
  private simGap = 100;
  // Density field.
  private gw = 0;
  private gh = 0;
  private cell = FIELD_CELL;
  private fieldRaw = new Float32Array(0);
  private field = new Float32Array(0);
  private fieldFresh = true;
  private fieldAt = 0;
  private fieldHot = false;
  private fieldCanvas: HTMLCanvasElement = document.createElement('canvas');
  private fieldImg: ImageData | null = null;
  private fieldLut = new Uint32Array(256);
  private kx = new Float32Array(64);
  private ky = new Float32Array(64);
  // Hull scratch buffers (cluster outlines).
  private hp: number[] = [];
  private hIdx: number[] = [];
  private hull: number[] = [];
  /** Badges queued by drawClusters for drawing above the nodes: [cluster index, x, top] triples. */
  private badges: number[] = [];
  // Cached per-theme sprites and styles.
  private glow: Record<string, HTMLCanvasElement> = {};
  private crushStyle: string[] = [];
  private bodyStyle: string[] = [];
  private crushGlow: HTMLCanvasElement | null = null;
  private reduced = false;
  private frameMs = 0;

  private floorplan: HTMLImageElement | null = null;
  private floorplanAlpha = 0.55;
  private layoutGeo: VenueLayout | null = null;
  private boards: Hardware[] = [];
  /** Draw board markers (Hardware page). */
  showBoards = false;
  /** A board being dragged: drawn at this venue position until the server confirms. */
  boardDrag: { key: string; x: number; y: number } | null = null;
  /** A real phone being dragged by staff: drawn at this venue position until the server confirms. */
  nodeDrag: { id: string; x: number; y: number } | null = null;
  /** Where joining phones are lined up (the demo spot), drawn when on. */
  demoSpot: { on: boolean; x: number; y: number; spacing: number } | null = null;
  /** Real phones with a name on the map right now. */
  private named = 0;
  private nameList: Body[] = [];
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
  /** Drawn above the neighbour lines, below the clusters and dots (the phone-to-phone mesh layer, meshnet.ts). */
  overlay: ((g: CanvasRenderingContext2D, now: number) => void) | null = null;
  /** No decorative "readings shared between neighbours" dots (the real mesh links are on screen). */
  quietGossip = false;
  /** Drawn above the background, below everything else (custom areas). */
  underlay: ((g: CanvasRenderingContext2D, now: number) => void) | null = null;
  /** Table demo layer (tablelayer.ts): drawn below and above the dots; it draws two-phone pushes, so drawWaves skips them. */
  table: { draw(g: CanvasRenderingContext2D, now: number, above: boolean): void; hit(wx: number, wy: number): [string, string] | null } | null = null;

  constructor(private canvas: HTMLCanvasElement) {
    this.ctx = canvas.getContext('2d')!;
    const mq = window.matchMedia?.('(prefers-reduced-motion: reduce)');
    if (mq) {
      this.reduced = mq.matches;
      mq.addEventListener?.('change', () => (this.reduced = mq.matches));
    }
    this.buildThemeCache();
    new ResizeObserver(() => this.resize()).observe(canvas);
    this.resize();
    const tick = (now: number) => {
      this.frame(now);
      requestAnimationFrame(tick);
    };
    requestAnimationFrame(tick);
  }

  /** Links currently drawn, gossip hops per second and the mean frame time (ms), for the overlay. */
  stats() {
    const now = performance.now();
    let i = 0;
    while (i < this.hopTimes.length && now - this.hopTimes[i] >= 1000) i++;
    if (i) this.hopTimes.splice(0, i);
    return { links: this.links.length, hops: this.hopTimes.length, frameMs: Math.round(this.frameMs * 100) / 100 };
  }

  update(nodes: Node[], waves: Wave[], links: [string, string][], clusters: Cluster[], venue: { w: number; h: number }) {
    const now = performance.now();
    // A scenario preview (nothing running) is shown in the room it would build, not the live venue.
    if (this.simPreview && !this.sim && this.simPreview.venue) venue = this.simPreview.venue;
    if (venue.w !== this.venue.w || venue.h !== this.venue.h) {
      this.venue = { w: Math.max(1, venue.w), h: Math.max(1, venue.h) };
      this.layout();
    }
    this.serverLinks = links;
    this.clusters = clusters;
    this.clusterLabels = clusters.map((c) => {
      const arrow = c.trend === 'forming' ? ' ↑' : c.trend === 'dispersing' ? ' ↓' : '';
      const eta = c.eta != null && c.level !== 'red' ? ` · danger in ~${Math.max(1, Math.round(c.eta))} s` : '';
      // est = people/m² at the cluster's densest spot (what its level uses); density on older servers.
      return `${c.people ?? c.count} people · up to ${(c.est ?? c.density).toFixed(1)}/m²${arrow}${eta}`;
    });
    // Participation: the server estimates people = phones ÷ participation per cluster.
    let people = 0, count = 0;
    for (const c of clusters) {
      if (c.people != null && c.count > 0) {
        people += c.people;
        count += c.count;
      }
    }
    if (count > 0) this.perPhone = lerp(this.perPhone, clamp(people / count, 1, 50), 0.3);

    const seen = new Set<string>();
    let named = 0;
    for (const n of nodes) {
      seen.add(n.id);
      if (n.name && n.status !== 'stale') named++;
      let b = this.bodies.get(n.id);
      const [rx, ry] = this.nodeDrag?.id === n.id ? [this.nodeDrag.x, this.nodeDrag.y] : this.reported(n);
      if (!b) {
        const seed = hash(n.id);
        b = {
          id: n.id, data: n, seed,
          rx, ry, tx: rx, ty: ry, px: rx, py: ry, vx: 0, vy: 0,
          tw0: -1, twx: 0, twy: 0, movedAt: now, interval: 1000,
          x: 0, y: 0, hx: 0, hy: 0, sway: Math.min(2, n.sway), crush: n.crush ?? 0,
          status: n.status, prevStatus: n.status, statusAt: now - COLOR_FADE_MS,
          stale: n.status === 'stale' ? 0.45 : 1, vis: 0, gone: false, goneAt: 0,
          ripples: [], flash: 0, nextBeat: now + 400 + seed * 900, simIdx: -1, nbrs: [], shakeAt: 0,
        };
        this.bodies.set(n.id, b);
        this.listDirty = true;
        this.place(b, now);
      } else {
        const d = Math.hypot(rx - b.rx, ry - b.ry);
        if (d > 0.001) {
          b.interval = clamp(lerp(b.interval, now - b.movedAt, 0.3), 80, 2000);
          b.movedAt = now;
          if (d > TELEPORT_M && b.simIdx < 0) this.startEase(b, now);
          b.rx = rx;
          b.ry = ry;
        }
        if (b.status !== n.status) {
          const f = (now - b.statusAt) / COLOR_FADE_MS;
          b.prevStatus = f < 0.5 ? b.prevStatus : b.status;
          b.status = n.status;
          b.statusAt = now;
          if (n.status === 'wave') b.ripples.push({ t0: now, style: SOLID.wave, max: 2 * NODE_R });
        }
        if (b.gone) {
          b.gone = false;
          this.listDirty = true;
        }
      }
      b.data = n;
      // "That's me": a phone being shaken sends out ripples in its own colour.
      if (n.shake && now - b.shakeAt > SHAKE_RIPPLE_MS) {
        b.shakeAt = now;
        b.flash = 1;
        b.ripples.push({ t0: now, style: n.color ?? SOLID.ok, max: 9 * NODE_R, w: 4 });
      }
    }
    this.named = named;
    for (const b of this.bodies.values()) {
      if (seen.has(b.id)) continue;
      if (!b.gone) {
        b.gone = true;
        b.goneAt = now;
      } else if (now - b.goneAt > 1500) {
        // Frames may not be running (a background tab): don't keep ghosts until they do.
        this.bodies.delete(b.id);
        this.listDirty = true;
      }
    }
    this.waves = waves;
    this.waveKeys.clear();
    this.waveNodes.clear();
    for (const w of waves) {
      this.waveKeys.add(key(w.from, w.to));
      if (w.pair) continue; // a two-phone push is yellow (tablelayer.ts): no red rings
      this.waveNodes.add(w.from);
      this.waveNodes.add(w.to);
    }
    for (const l of this.links) l.wave = waves.length > 0 && this.waveKeys.has(key(l.a.id, l.b.id));
    if (this.simN) this.matchSimPhones();
  }

  /** A report in venue metres; phones outside the venue sit just beyond its edge. */
  private reported(n: Node): [number, number] {
    if (!n.outside) return [n.x, n.y];
    const m = Math.min(1, 30 / this.fit.s);
    return [clamp(n.x, -m, this.venue.w + m), clamp(n.y, -m, this.venue.h + m)];
  }

  private startEase(b: Body, now: number) {
    b.tw0 = now;
    b.twx = b.px;
    b.twy = b.py;
    b.vx = b.vy = 0;
  }

  /** A scenario's venue shown before it runs (Simulation page, nothing running): its furniture, doors and exits. null = none. */
  private simPreview: SimState | null = null;

  /** Show (or clear) a scenario preview while no simulation runs; the venue size must already be set to the preview's. */
  setSimPreview(geo: SimState | null) {
    this.simPreview = geo;
    if (!this.sim) this.simGeo = geo;
  }

  /** Simulated people (null when not simulating) and the venue's walls and exits. */
  setSim(frame: SimFrame | null, geo: SimState | null) {
    this.sim = frame;
    if (geo) this.simGeo = geo;
    if (!frame) {
      this.simGeo = this.simPreview;
      if (this.simN) {
        this.simN = 0;
        this.simT = NaN;
        for (const b of this.list) b.simIdx = -1;
      }
      return;
    }
    if (frame.t === this.simT) return;
    const now = performance.now();
    const a = this.simN ? clamp((now - this.simAt) / this.simGap, 0, 1) : 1;
    if (this.simN) this.simGap = clamp(lerp(this.simGap, now - this.simAt, 0.25), 40, 1000);
    this.simT = frame.t;
    this.simAt = now;
    const list = frame.bodies;
    const n = list.length;
    if (this.simCur.length < n * SIM_STRIDE) {
      const cap = Math.max(64, n * 2);
      const grow = (old: Float32Array) => {
        const f = new Float32Array(cap * SIM_STRIDE);
        f.set(old);
        return f;
      };
      this.simPrev = grow(this.simPrev);
      this.simCur = grow(this.simCur);
      this.simDraw = grow(this.simDraw);
      this.simPhone = new Uint8Array(cap);
      this.simState = new Uint8Array(cap);
      this.simDens = new Float32Array(cap);
      this.simClaim = new Uint8Array(cap);
      this.simBucket = new Uint8Array(cap);
    }
    const P = this.simPrev, C = this.simCur;
    const old = this.simN;
    // Same length: index i is the same person. Otherwise someone left or arrived: walk both
    // arrays in order and pair each body with the nearest old one a few places ahead.
    const shifted = old > 0 && n !== old;
    if (shifted) {
      if (this.simOldC.length < old * SIM_STRIDE) {
        this.simOldP = new Float32Array(this.simCur.length);
        this.simOldC = new Float32Array(this.simCur.length);
      }
      this.simOldP.set(P.subarray(0, old * SIM_STRIDE));
      this.simOldC.set(C.subarray(0, old * SIM_STRIDE));
    }
    const SP = shifted ? this.simOldP : P, SC = shifted ? this.simOldC : C;
    if (shifted) {
      if (this.simRemap.length < old) this.simRemap = new Int32Array(this.simCur.length / SIM_STRIDE);
      this.simRemap.fill(-1, 0, old);
    }
    const ahead = Math.max(0, old - n) + 1;
    let ptr = 0;
    for (let i = 0; i < n; i++) {
      const [x, y, p, ph] = list[i];
      this.simDens[i] = list[i][4] ?? 0;
      // Heading in radians (older servers send none: face up the map); state code.
      const hd = list[i][5] != null ? (list[i][5]! * Math.PI) / 180 : -Math.PI / 2;
      this.simState[i] = list[i][6] ?? ST_STANDING;
      const j = i * SIM_STRIDE;
      let o = -1;
      if (!shifted) o = i < old ? i : -1;
      else {
        let bd = 0.25;
        for (let q = ptr, end = Math.min(old, ptr + ahead); q < end; q++) {
          const d = (x - SC[q * SIM_STRIDE]) ** 2 + (y - SC[q * SIM_STRIDE + 1]) ** 2;
          if (d < bd) {
            bd = d;
            o = q;
          }
        }
        if (o >= 0) {
          ptr = o + 1;
          this.simRemap[o] = i;
        }
      }
      if (o >= 0) {
        // Start from where the body is drawn right now, so a late frame never jumps.
        const k = o * SIM_STRIDE;
        const dx = lerp(SP[k], SC[k], a), dy = lerp(SP[k + 1], SC[k + 1], a), dp = lerp(SP[k + 2], SC[k + 2], a);
        const far = (x - dx) * (x - dx) + (y - dy) * (y - dy) > SIM_SNAP_M * SIM_SNAP_M;
        P[j] = far ? x : dx;
        P[j + 1] = far ? y : dy;
        P[j + 2] = far ? p : dp;
        P[j + 3] = far ? hd : lerpAngle(SP[k + 3], SC[k + 3], a);
      } else {
        P[j] = x;
        P[j + 1] = y;
        P[j + 2] = p;
        P[j + 3] = hd;
      }
      C[j] = x;
      C[j + 1] = y;
      C[j + 2] = p;
      C[j + 3] = hd;
      this.simPhone[i] = ph ? 1 : 0;
    }
    if (shifted) for (const b of this.list) if (b.simIdx >= 0) b.simIdx = b.simIdx < old ? this.simRemap[b.simIdx] : -1;
    this.simN = n;
    this.interpolateSim(now);
    this.matchSimPhones();
  }

  /** Pin each phone node to the simulated body carrying it: nearest phone-carrying body to its report, sticky. */
  private matchSimPhones() {
    if (this.listDirty) {
      this.list = [...this.bodies.values()];
      this.listDirty = false;
    }
    const n = this.simN, C = this.simCur, claim = this.simClaim, ph = this.simPhone;
    claim.fill(0, 0, n);
    const r2 = SIM_MATCH_M * SIM_MATCH_M;
    // Keep last frame's matches that still fit.
    for (const b of this.list) {
      const i = b.simIdx;
      if (i < 0) continue;
      if (b.data.real) {
        b.simIdx = -1; // a real phone is its own body
        continue;
      }
      const ok = i < n && ph[i] && !claim[i] && (C[i * SIM_STRIDE] - b.rx) ** 2 + (C[i * SIM_STRIDE + 1] - b.ry) ** 2 < r2;
      if (ok) claim[i] = 1;
      else b.simIdx = -1;
    }
    for (const b of this.list) {
      if (b.simIdx >= 0 || b.gone || b.data.real || b.data.name) continue;
      let best = -1, bd = r2;
      for (let i = 0; i < n; i++) {
        if (!ph[i] || claim[i]) continue;
        const d = (C[i * SIM_STRIDE] - b.rx) ** 2 + (C[i * SIM_STRIDE + 1] - b.ry) ** 2;
        if (d < bd) {
          bd = d;
          best = i;
        }
      }
      if (best >= 0) {
        claim[best] = 1;
        b.simIdx = best;
        // Glide onto the body if the dot is visibly elsewhere.
        const j = best * SIM_STRIDE;
        if (b.vis > 0.5 && (this.simDraw[j] - b.px) ** 2 + (this.simDraw[j + 1] - b.py) ** 2 > 0.09) this.startEase(b, performance.now());
      }
    }
  }

  private interpolateSim(now: number) {
    const a = clamp((now - this.simAt) / this.simGap, 0, 1);
    const P = this.simPrev, C = this.simCur, D = this.simDraw;
    for (let j = 0, m = this.simN * SIM_STRIDE; j < m; j += SIM_STRIDE) {
      D[j] = P[j] + (C[j] - P[j]) * a;
      D[j + 1] = P[j + 1] + (C[j + 1] - P[j + 1]) * a;
      D[j + 2] = P[j + 2] + (C[j + 2] - P[j + 2]) * a;
      D[j + 3] = lerpAngle(P[j + 3], C[j + 3], a); // the body turns the short way round
    }
  }

  /** Floor-plan image drawn under everything, stretched to the venue rectangle. */
  setFloorplan(img: HTMLImageElement | null, alpha = this.floorplanAlpha) {
    this.floorplan = img;
    this.floorplanAlpha = alpha;
  }

  /** Fixed venue features (stage, exits, walls) from the venue settings. */
  setLayout(layout: VenueLayout | null) {
    this.layoutGeo = layout;
  }

  setBoards(list: Hardware[]) {
    this.boards = list;
  }

  /** The neighbour link under a screen point (within a few px), wave links first. */
  linkUnder(sx: number, sy: number): [string, string] | null {
    const { x, y } = this.toWorld(sx, sy);
    const tol = 7 / this.view.k;
    const near = (a: Body, b: Body) => {
      const dx = b.x - a.x, dy = b.y - a.y;
      const L = dx * dx + dy * dy || 1;
      const t = Math.max(0, Math.min(1, ((x - a.x) * dx + (y - a.y) * dy) / L));
      return Math.hypot(a.x + t * dx - x, a.y + t * dy - y);
    };
    for (const w of this.waves) {
      const a = this.bodies.get(w.from), b = this.bodies.get(w.to);
      if (a && b && near(a, b) < tol) return [w.from, w.to];
    }
    const band = this.table?.hit(x, y);
    if (band) return band;
    for (const l of this.links) if (near(l.a, l.b) < tol) return [l.a.id, l.b.id];
    return null;
  }

  /** Board key ("sign" or a light letter) under a screen point, if markers are shown. */
  boardAt(sx: number, sy: number): string | null {
    if (!this.showBoards) return null;
    const { x, y } = this.toWorld(sx, sy);
    for (const [i, b] of this.boards.entries()) {
      const p = this.boardPos(b, i);
      if (Math.abs(p.x - x) < 16 && Math.abs(p.y - y) < 16) return b.key ?? (b.zone || 'sign');
    }
    return null;
  }

  /** Where a board is drawn (world px): its saved spot, the drag in progress, or parked along the bottom edge. */
  private boardPos(b: Hardware, i: number) {
    const key = b.key ?? (b.zone || 'sign');
    if (this.boardDrag?.key === key) return this.venueToWorld(this.boardDrag.x, this.boardDrag.y);
    if (b.x != null && b.y != null) return this.venueToWorld(b.x, b.y);
    return this.venueToWorld(1.5 + i * 2.5, this.venue.h - 1);
  }

  setTheme(t: 'dark' | 'light') {
    this.theme = t;
    this.bg = null;
    this.buildThemeCache();
    this.fieldFresh = true;
  }

  /** Glow sprites per status, pressure tints and the density colour table for the current theme. */
  private buildThemeCache() {
    const light = this.theme === 'light';
    this.glow = {};
    for (const s of STATUSES) {
      const c = document.createElement('canvas');
      c.width = c.height = 64;
      const g = c.getContext('2d')!;
      const grad = g.createRadialGradient(32, 32, 0, 32, 32, 32);
      grad.addColorStop(0, rgba(COLOR[s], light ? 0.35 : 0.6));
      grad.addColorStop(0.35, rgba(COLOR[s], light ? 0.12 : 0.22));
      grad.addColorStop(1, rgba(COLOR[s], 0));
      g.fillStyle = grad;
      g.fillRect(0, 0, 64, 64);
      this.glow[s] = c;
    }
    // The crush ramp, for phone dots (opaque) and simulated people (grey and
    // see-through when free, the same ramp as they get packed in).
    const grey: RGB = light ? [100, 116, 139] : [148, 163, 184];
    this.crushStyle = [];
    this.bodyStyle = [];
    for (let i = 0; i <= CRUSH_STEPS; i++) {
      const k = i / CRUSH_STEPS;
      const c = crushRGB(k, this.theme);
      this.crushStyle.push(`rgb(${c[0] | 0},${c[1] | 0},${c[2] | 0})`);
      this.bodyStyle.push(k < 0.12 ? rgba(mix(grey, c, k / 0.12), light ? 0.55 : 0.45) : rgba(c, Math.min(1, 0.7 + 0.5 * k)));
    }
    const gc = document.createElement('canvas');
    gc.width = gc.height = 64;
    const gg = gc.getContext('2d')!;
    const gr = gg.createRadialGradient(32, 32, 0, 32, 32, 32);
    gr.addColorStop(0, rgba(RED, light ? 0.5 : 0.75));
    gr.addColorStop(0.4, rgba(RED, light ? 0.2 : 0.3));
    gr.addColorStop(1, rgba(RED, 0));
    gg.fillStyle = gr;
    gg.fillRect(0, 0, 64, 64);
    this.crushGlow = gc;
    const bytes = new Uint8ClampedArray(this.fieldLut.buffer);
    for (let i = 0; i < 256; i++) {
      const [r, g, b, a] = fieldColor((i / 255) * FIELD_LUT_MAX, light);
      bytes[i * 4] = r;
      bytes[i * 4 + 1] = g;
      bytes[i * 4 + 2] = b;
      bytes[i * 4 + 3] = a * 255;
    }
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

  /** Staff drag a real phone: it follows the pointer at once (venue metres). null = let go. */
  dragNode(id: string | null, x = 0, y = 0) {
    if (!id) {
      this.nodeDrag = null;
      return;
    }
    this.nodeDrag = { id, x, y };
    const b = this.bodies.get(id);
    if (b) {
      b.rx = b.tx = b.px = x;
      b.ry = b.ty = b.py = y;
      b.vx = b.vy = 0;
      b.tw0 = -1;
    }
  }

  /** Is this body a real phone with a generated name (the ones staff may drag)? */
  isNamed(id: string) {
    return !!this.bodies.get(id)?.data.name;
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
    // Bodies live in venue metres; their world px follow on the next frame.
    for (const b of this.list) this.place(b, this.last);
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

  /** World px of a body: drawn spot (displayed position + a few cm of sway) and its reported spot. */
  private place(b: Body, now: number) {
    const { s, ox, oy } = this.fit;
    let ix = 0, iy = 0;
    if (!this.reduced && b.sway > 0.02) {
      const amp = Math.min(IDLE_MAX_M, b.sway * 0.025);
      ix = amp * Math.sin(now * 0.0023 + b.seed * 40);
      iy = amp * 0.8 * Math.sin(now * 0.0017 + b.seed * 71);
    }
    b.x = ox + (b.px + ix) * s;
    b.y = oy + (b.py + iy) * s;
    b.hx = ox + b.rx * s;
    b.hy = oy + b.ry * s;
  }

  private frame(now: number) {
    const t0 = performance.now();
    const dt = Math.min(0.05, Math.max(0, (now - this.last) / 1000));
    this.last = now;
    this.step(now, dt);
    this.draw(now);
    this.frameMs = lerp(this.frameMs, performance.now() - t0, 0.05);
  }

  // -------------------------------------------------------------------------
  // motion
  // -------------------------------------------------------------------------

  private step(now: number, dt: number) {
    if (this.listDirty) {
      this.list = [...this.bodies.values()];
      this.listDirty = false;
    }
    if (this.simN) this.interpolateSim(now);
    const D = this.simDraw;
    const kSway = 1 - Math.exp(-dt / 0.6);
    const kStale = 1 - Math.exp(-dt / 0.25);
    const kCrush = 1 - Math.exp(-dt / 0.35);
    let removed = false;

    for (const b of this.list) {
      // Target: the matched simulated body (already smooth at 10 Hz), else the latest report.
      if (b.simIdx >= 0 && b.simIdx < this.simN) {
        b.tx = D[b.simIdx * SIM_STRIDE];
        b.ty = D[b.simIdx * SIM_STRIDE + 1];
      } else {
        b.tx = b.rx;
        b.ty = b.ry;
      }
      if (b.tw0 >= 0) {
        const f = (now - b.tw0) / TELEPORT_MS;
        if (f >= 1) {
          b.tw0 = -1;
          b.px = b.tx;
          b.py = b.ty;
        } else {
          const e = ease(f);
          b.px = lerp(b.twx, b.tx, e);
          b.py = lerp(b.twy, b.ty, e);
        }
      } else if (b.simIdx >= 0) {
        b.px = b.tx;
        b.py = b.ty;
      } else {
        // Critically damped follow; the time constant tracks how often this phone reports.
        this.follow(b, clamp(b.interval * 0.00035, 0.1, 0.35), dt);
      }
      b.sway = lerp(b.sway, Math.min(2, b.data.sway), kSway);
      // How packed in: Pulse's estimate for this phone; on a simulated body, at least the body's truth.
      let crush = b.status === 'stale' ? 0 : (b.data.crush ?? 0);
      if (b.simIdx >= 0 && b.simIdx < this.simN) crush = Math.max(crush, bodyCrush(D[b.simIdx * SIM_STRIDE + 2], this.simDens[b.simIdx]));
      b.crush = lerp(b.crush, crush, kCrush);
      b.stale = lerp(b.stale, b.status === 'stale' ? 0.45 : 1, kStale);
      b.vis = clamp(b.vis + (b.gone ? -dt * 1000 / FADE_OUT_MS : dt * 1000 / FADE_IN_MS), 0, 1);
      if (b.gone && b.vis <= 0) {
        this.bodies.delete(b.id);
        removed = true;
        continue;
      }
      this.place(b, now);
      let n = 0;
      for (const r of b.ripples) if (now - r.t0 < 900) b.ripples[n++] = r;
      b.ripples.length = n;
      b.flash = Math.max(0, b.flash - dt * 3);
    }
    if (removed) this.listDirty = true;

    if (now - this.linkAt > 180) {
      this.linkAt = now;
      this.relink();
    }
    if (now - this.fieldAt > FIELD_EVERY) {
      const fdt = (now - this.fieldAt) / 1000;
      this.fieldAt = now;
      this.refreshField(fdt);
    }
    if (this.reduced) {
      this.packets.length = 0;
    } else {
      if (!this.quietGossip) this.gossip(now);
      this.spawnWavePackets(now);
    }
    this.deliver(now);
  }

  /** SmoothDamp toward the target: critically damped, never overshoots. */
  private follow(b: Body, st: number, dt: number) {
    if (dt <= 0) return;
    const omega = 2 / st;
    const x = omega * dt;
    const e = 1 / (1 + x + 0.48 * x * x + 0.235 * x * x * x);
    const cx = b.px - b.tx, cy = b.py - b.ty;
    const tx = (b.vx + omega * cx) * dt, ty = (b.vy + omega * cy) * dt;
    b.vx = (b.vx - omega * tx) * e;
    b.vy = (b.vy - omega * ty) * e;
    let nx = b.tx + (cx + tx) * e, ny = b.ty + (cy + ty) * e;
    if ((b.tx - b.px) * (nx - b.tx) + (b.ty - b.py) * (ny - b.ty) > 0) {
      nx = b.tx;
      ny = b.ty;
      b.vx = b.vy = 0;
    }
    b.px = nx;
    b.py = ny;
  }

  /** Links are the neighbour pairs the server's detector compares; without them, nearest bodies. */
  private relink() {
    const live = this.list;
    for (const b of live) b.nbrs.length = 0;
    const seen = new Set<string>();
    const out: Link[] = [];
    const add = (a: Body, b: Body, grid: boolean) => {
      const k = key(a.id, b.id);
      if (seen.has(k)) return;
      seen.add(k);
      out.push({ a, b, grid, wave: this.waveKeys.has(k) });
      a.nbrs.push(b);
      b.nbrs.push(a);
    };
    for (const [ia, ib] of this.serverLinks) {
      const a = this.bodies.get(ia), b = this.bodies.get(ib);
      if (a && b && !a.gone && !b.gone) add(a, b, true);
    }
    if (!out.length) {
      const maxD2 = (this.fit.s * 2) ** 2; // 2 m
      const best: Body[] = [];
      const bestD: number[] = [];
      for (const a of live) {
        if (a.gone) continue;
        best.length = 0;
        bestD.length = 0;
        for (const b of live) {
          if (b === a || b.gone) continue;
          const d = (b.x - a.x) ** 2 + (b.y - a.y) ** 2;
          if (d >= maxD2) continue;
          let i = bestD.length;
          while (i > 0 && bestD[i - 1] > d) i--;
          if (i >= NEIGHBOURS) continue;
          best.splice(i, 0, b);
          bestD.splice(i, 0, d);
          if (best.length > NEIGHBOURS) {
            best.length = NEIGHBOURS;
            bestD.length = NEIGHBOURS;
          }
        }
        for (const b of best) add(a, b, false);
      }
    }
    this.links = out;
  }

  /** Every streaming phone shares its reading with its neighbours, who pass it on. */
  private gossip(now: number) {
    for (const b of this.list) {
      if (now < b.nextBeat) continue;
      const st = b.status;
      b.nextBeat = now + (st === 'wave' ? 350 : st === 'swaying' ? 600 : 1000) + Math.random() * 500;
      if (b.gone || st === 'stale' || st === 'connecting') continue;
      for (const n of b.nbrs) this.send(b, n, SOLID[st], 0, false, now);
    }
  }

  private spawnWavePackets(now: number) {
    for (const w of this.waves) {
      if (w.pair) continue; // two-phone push: tablelayer.ts
      const k = `${w.from}>${w.to}`;
      if (now - (this.waveSpawn.get(k) ?? 0) < 380) continue;
      const a = this.bodies.get(w.from), b = this.bodies.get(w.to);
      if (!a || !b) continue;
      this.waveSpawn.set(k, now);
      this.send(a, b, SOLID.wave, 0, true, now, Math.max(260, Math.min(800, w.lagMs * 1.4)));
    }
    if (this.waveSpawn.size > 500) this.waveSpawn.clear();
  }

  private send(from: Body, to: Body, style: string, hops: number, wave: boolean, now: number, dur?: number) {
    if (this.packets.length >= MAX_PACKETS && !wave) return;
    const d = Math.hypot(to.x - from.x, to.y - from.y);
    this.packets.push({ from, to, t0: now, dur: dur ?? Math.max(220, d * 3.2), style, hops, wave });
    this.hopTimes.push(now);
    if (this.hopTimes.length > 4000) this.hopTimes.splice(0, 2000);
  }

  private deliver(now: number) {
    let n = 0;
    const ps = this.packets;
    const count = ps.length;
    for (let i = 0; i < count; i++) {
      const p = ps[i];
      if (now - p.t0 < p.dur) {
        ps[n++] = p;
        continue;
      }
      const to = p.to;
      if (p.wave) {
        // The push reaches this person: a ring on their node, never a positional kick.
        to.flash = 1;
        to.ripples.push({ t0: now, style: SOLID.wave, max: 3 * NODE_R });
      } else {
        to.flash = Math.min(0.6, to.flash + 0.25);
        if (p.hops < 2 && Math.random() < 0.45 && to.nbrs.length > 1) {
          // Relay: pass the reading on to someone who didn't send it.
          let next = to.nbrs[(Math.random() * to.nbrs.length) | 0];
          if (next === p.from) next = to.nbrs[(to.nbrs.indexOf(next) + 1) % to.nbrs.length];
          if (next !== p.from) this.send(to, next, p.style, p.hops + 1, false, now);
        }
      }
    }
    // Relays pushed during the loop sit after `count`; keep them.
    for (let i = count; i < ps.length; i++) ps[n++] = ps[i];
    ps.length = n;
  }

  // -------------------------------------------------------------------------
  // density field
  // -------------------------------------------------------------------------

  /** Kernel density estimate of people on a coarse grid, smoothed over time, rendered into a small offscreen image. */
  private refreshField(dt: number) {
    const cell = Math.max(FIELD_CELL, Math.max(this.venue.w, this.venue.h) / FIELD_MAX_DIM);
    const gw = Math.ceil(this.venue.w / cell), gh = Math.ceil(this.venue.h / cell);
    if (gw !== this.gw || gh !== this.gh || cell !== this.cell) {
      this.gw = gw;
      this.gh = gh;
      this.cell = cell;
      this.fieldRaw = new Float32Array(gw * gh);
      this.field = new Float32Array(gw * gh);
      this.fieldCanvas.width = gw;
      this.fieldCanvas.height = gh;
      this.fieldImg = this.fieldCanvas.getContext('2d')!.createImageData(gw, gh);
      this.fieldFresh = true;
    }
    const raw = this.fieldRaw;
    raw.fill(0);
    const sig = FIELD_SIGMA;
    const norm = 1 / (2 * Math.PI * sig * sig);
    if (this.simN) {
      // Simulated crowd: everyone is known.
      const D = this.simDraw;
      for (let i = 0; i < this.simN; i++) this.splat(D[i * SIM_STRIDE], D[i * SIM_STRIDE + 1], norm);
    } else {
      // Phones only: each stands for perPhone people (the server's participation estimate).
      const wgt = norm * this.perPhone;
      for (const b of this.list) if (!b.gone && !b.data.outside) this.splat(b.px, b.py, wgt);
    }
    const f = this.field;
    const k = this.fieldFresh ? 1 : 1 - Math.exp(-dt / FIELD_TAU);
    this.fieldFresh = false;
    const img = this.fieldImg!;
    const px = new Uint32Array(img.data.buffer);
    const lut = this.fieldLut;
    const scale = 255 / FIELD_LUT_MAX;
    let hot = false;
    for (let i = 0, m = f.length; i < m; i++) {
      const v = f[i] + (raw[i] - f[i]) * k;
      f[i] = v;
      if (v > DENSITY_WATCH) hot = true;
      px[i] = lut[Math.min(255, (v * scale) | 0)];
    }
    this.fieldHot = hot;
    if (hot) this.fieldCanvas.getContext('2d')!.putImageData(img, 0, 0);
  }

  /** Add one Gaussian kernel (weight w, already normalised) centred on venue (x, y). */
  private splat(x: number, y: number, w: number) {
    const cell = this.cell, gw = this.gw, gh = this.gh;
    const R = 3 * FIELD_SIGMA;
    const i0 = Math.max(0, Math.floor((x - R) / cell)), i1 = Math.min(gw - 1, Math.floor((x + R) / cell));
    const j0 = Math.max(0, Math.floor((y - R) / cell)), j1 = Math.min(gh - 1, Math.floor((y + R) / cell));
    if (i1 < i0 || j1 < j0 || i1 - i0 >= this.kx.length || j1 - j0 >= this.ky.length) return;
    const inv = 1 / (2 * FIELD_SIGMA * FIELD_SIGMA);
    for (let i = i0; i <= i1; i++) {
      const d = (i + 0.5) * cell - x;
      this.kx[i - i0] = Math.exp(-d * d * inv);
    }
    for (let j = j0; j <= j1; j++) {
      const d = (j + 0.5) * cell - y;
      this.ky[j - j0] = w * Math.exp(-d * d * inv);
    }
    const raw = this.fieldRaw;
    for (let j = j0; j <= j1; j++) {
      const wy = this.ky[j - j0];
      const row = j * gw;
      for (let i = i0; i <= i1; i++) raw[row + i] += wy * this.kx[i - i0];
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
    g.globalAlpha = 1;
    g.drawImage(this.background(), 0, 0);
    const { k, x: vx, y: vy } = this.view;
    g.setTransform(this.dpr * k, 0, 0, this.dpr * k, this.dpr * vx, this.dpr * vy);
    const light = this.theme === 'light';
    const bodies = this.list;
    const pulse = this.reduced ? 0.5 : 0.5 + 0.5 * Math.sin(now / 320);

    this.drawVenue(g);
    if (this.simPreview && !this.sim) {
      // A scenario preview while nothing runs: the room alone, not the live phones.
      this.drawSimGeometry(g);
      g.setTransform(this.dpr, 0, 0, this.dpr, 0, 0);
      g.fillStyle = light ? 'rgba(71,85,105,0.8)' : 'rgba(148,163,184,0.6)';
      g.font = '500 14px Inter, system-ui, sans-serif';
      g.textAlign = 'center';
      g.fillText('No simulation running. This is the room the chosen scenario builds; press Start to fill it.', this.w / 2, this.h - 14);
      g.textAlign = 'left';
      return;
    }
    this.drawDemoSpot(g);
    if (!this.sim) this.drawLayout(g);
    this.drawSimGeometry(g);
    this.underlay?.(g, now);
    this.drawField(g);
    this.drawSimBodies(g);

    // GPS accuracy: the true spot is somewhere in this circle.
    g.lineWidth = 1;
    for (const b of bodies) {
      const acc = b.data.acc ?? 0;
      if (acc <= 0 || b.vis <= 0) continue;
      const a = easeOut(b.vis) * b.stale;
      g.fillStyle = g.strokeStyle = SOLID[b.status];
      g.beginPath();
      g.arc(b.hx, b.hy, acc * this.fit.s, 0, Math.PI * 2);
      g.globalAlpha = 0.05 * a;
      g.fill();
      g.globalAlpha = 0.18 * a;
      g.stroke();
    }
    g.globalAlpha = 1;

    // Mesh links: one quiet path per kind. Wave links are drawn separately, on top.
    const base = light ? 'rgba(71,85,105,' : 'rgba(148,163,184,';
    g.lineCap = 'round';
    for (let pass = 0; pass < 2; pass++) {
      const grid = pass === 0;
      g.beginPath();
      let any = false;
      for (const l of this.links) {
        if (l.grid !== grid || l.wave) continue;
        if (l.a.vis < 0.05 || l.b.vis < 0.05) continue;
        g.moveTo(l.a.x, l.a.y);
        g.lineTo(l.b.x, l.b.y);
        any = true;
      }
      if (!any) continue;
      g.strokeStyle = base + (grid ? (light ? '0.28)' : '0.22)') : light ? '0.16)' : '0.13)');
      g.lineWidth = grid ? 1.2 : 0.9;
      g.setLineDash(grid ? [] : [3, 5]);
      g.stroke();
    }
    g.setLineDash([]);
    this.overlay?.(g, now);
    this.table?.draw(g, now, false);

    this.drawClusters(g, pulse);
    this.drawWaves(g, now, pulse);

    // Gossip packets.
    for (const p of this.packets) {
      if (p.wave) continue;
      const f = ease(Math.min(1, (now - p.t0) / p.dur));
      g.globalAlpha = Math.max(0.1, 0.55 - p.hops * 0.17) * Math.min(p.from.vis, p.to.vis);
      g.fillStyle = p.style;
      g.beginPath();
      g.arc(lerp(p.from.x, p.to.x, f), lerp(p.from.y, p.to.y, f), 2.1 - p.hops * 0.4, 0, Math.PI * 2);
      g.fill();
    }
    g.globalAlpha = 1;

    this.drawNodes(g, now, pulse);
    this.drawNames(g);
    drawNearLabels(g, this.list, NODE_R, this.view.k, light); // walked up to a board (nearlabels.ts)
    this.drawBadges(g);
    this.table?.draw(g, now, true);

    // Links of the selected phone: who it shares readings with.
    const sel = this.selected ? this.bodies.get(this.selected) : undefined;
    if (sel) {
      g.strokeStyle = 'rgba(34,211,238,0.55)';
      g.lineWidth = 2;
      g.beginPath();
      for (const n of sel.nbrs) {
        g.moveTo(sel.x, sel.y);
        g.lineTo(n.x, n.y);
      }
      g.stroke();
    }

    if (this.showBoards) this.drawBoards(g, now);

    g.setTransform(this.dpr, 0, 0, this.dpr, 0, 0);

    // A thin tint on the map's border: a glance tells there is an alert somewhere; the map itself stays clean.
    if (this.level !== 'calm') {
      const red = this.level === 'red';
      g.strokeStyle = rgba(red ? WAVE : AMBER, red ? 0.4 + 0.25 * pulse : 0.45);
      g.lineWidth = 3;
      g.strokeRect(1.5, 1.5, this.w - 3, this.h - 3);
    }

    if (bodies.length === 0 && !this.simN) {
      g.fillStyle = light ? 'rgba(71,85,105,0.8)' : 'rgba(148,163,184,0.6)';
      g.font = '500 18px Inter, system-ui, sans-serif';
      g.textAlign = 'center';
      g.fillText('No phones on the map yet. Attendees appear here when they scan the join QR.', this.w / 2, this.h / 2);
      g.textAlign = 'left';
    }
  }

  /** The density heat map: a small image stretched (bilinear) over the venue. */
  private drawField(g: CanvasRenderingContext2D) {
    if (!this.fieldHot || !this.gw) return;
    const { s, ox, oy } = this.fit;
    g.save();
    g.imageSmoothingEnabled = true;
    g.imageSmoothingQuality = 'high';
    g.beginPath();
    g.rect(ox, oy, this.venue.w * s, this.venue.h * s);
    g.clip();
    g.drawImage(this.fieldCanvas, ox, oy, this.gw * this.cell * s, this.gh * this.cell * s);
    g.restore();
  }

  /** Push waves: only the links they travel along, with the direction of travel, and the phones on them. */
  private drawWaves(g: CanvasRenderingContext2D, now: number, pulse: number) {
    g.lineCap = 'round';
    for (const w of this.waves) {
      if (w.pair) continue; // two-phone push: tablelayer.ts
      const a = this.bodies.get(w.from), b = this.bodies.get(w.to);
      if (!a || !b) continue;
      const alpha = Math.min(a.vis, b.vis);
      if (alpha < 0.02) continue;
      g.globalAlpha = alpha;
      g.strokeStyle = rgba(WAVE, 0.18);
      g.lineWidth = 8;
      g.beginPath();
      g.moveTo(a.x, a.y);
      g.lineTo(b.x, b.y);
      g.stroke();
      g.strokeStyle = rgba(WAVE, 0.7 + 0.25 * pulse);
      g.lineWidth = 2.4;
      g.stroke();
      // Arrowhead: which way the push travels.
      const dx = b.x - a.x, dy = b.y - a.y;
      const d = Math.hypot(dx, dy);
      if (d > 4 * NODE_R) {
        const ux = dx / d, uy = dy / d;
        const mx = a.x + dx * 0.58, my = a.y + dy * 0.58;
        const s = 6;
        g.fillStyle = rgba(WAVE, 0.95);
        g.beginPath();
        g.moveTo(mx + ux * s, my + uy * s);
        g.lineTo(mx - ux * s - uy * s * 0.8, my - uy * s + ux * s * 0.8);
        g.lineTo(mx - ux * s + uy * s * 0.8, my - uy * s - ux * s * 0.8);
        g.closePath();
        g.fill();
      }
    }
    // Packets riding the wave links.
    for (const p of this.packets) {
      if (!p.wave) continue;
      const f = ease(Math.min(1, (now - p.t0) / p.dur));
      const x = lerp(p.from.x, p.to.x, f), y = lerp(p.from.y, p.to.y, f);
      const tf = Math.max(0, f - 0.3);
      g.globalAlpha = Math.min(p.from.vis, p.to.vis);
      g.strokeStyle = rgba(WAVE, 0.75);
      g.lineWidth = 3;
      g.beginPath();
      g.moveTo(lerp(p.from.x, p.to.x, tf), lerp(p.from.y, p.to.y, tf));
      g.lineTo(x, y);
      g.stroke();
      g.fillStyle = rgba(WAVE, 0.3);
      g.beginPath();
      g.arc(x, y, 7, 0, Math.PI * 2);
      g.fill();
      g.fillStyle = '#fff1f2';
      g.beginPath();
      g.arc(x, y, 3.5, 0, Math.PI * 2);
      g.fill();
    }
    g.globalAlpha = 1;
  }

  private drawNodes(g: CanvasRenderingContext2D, now: number, pulse: number) {
    const light = this.theme === 'light';
    const outline = light ? '#ffffff' : '#0a0e15';
    const TAU = Math.PI * 2;
    for (const b of this.list) {
      if (b.vis <= 0) continue;
      const st = b.status;
      const ve = easeOut(b.vis);
      const a = ve * b.stale;
      const fade = Math.min(1, (now - b.statusAt) / COLOR_FADE_MS);
      const crush = b.crush;
      const packed = crush >= 0.33; // at or past the watch density
      // A packed-in dot is bigger: the crushed core reads from across a table.
      let r = (this.reduced ? NODE_R : NODE_R * (0.5 + 0.5 * ve)) * (1 + 0.3 * crush);
      if (st === 'wave' && !this.reduced) r *= 1 + 0.07 * Math.sin(now / 320 + b.seed * 10);
      const onWave = this.waveNodes.has(b.id);

      // Ripples: a status change or a push arriving.
      for (const rp of b.ripples) {
        const f = (now - rp.t0) / 900;
        g.globalAlpha = (1 - f) * (rp.w ? 0.9 : 0.7) * a;
        g.strokeStyle = rp.style;
        g.lineWidth = (rp.w ?? 2) * (1 - f) + 0.5;
        g.beginPath();
        g.arc(b.x, b.y, r + (this.reduced ? 4 : f * rp.max), 0, TAU);
        g.stroke();
      }

      // Glow (pre-rendered sprites). Packed in: a red glow that grows with the crush, whatever the
      // phone's motion. Otherwise the motion status's glow; a calm, free phone barely glows.
      if (crush > 0.3 && this.crushGlow && st !== 'stale') {
        const gR = r * (2.6 + 1.6 * crush);
        g.globalAlpha = Math.min(1, (crush - 0.3) / 0.5) * (light ? 0.75 : 0.9) * a;
        g.drawImage(this.crushGlow, b.x - gR, b.y - gR, gR * 2, gR * 2);
      }
      if (st === 'wave' || !packed) {
        const glowR = r * (st === 'wave' ? 4.2 : 3) * (1 + b.flash * 0.3);
        const glowA = (st === 'wave' ? 1 : st === 'ok' ? (light ? 0.2 : 0.45) : light ? 0.4 : 1) * a;
        if (fade < 1) {
          g.globalAlpha = glowA * (1 - fade);
          g.drawImage(this.glow[b.prevStatus], b.x - glowR, b.y - glowR, glowR * 2, glowR * 2);
        }
        g.globalAlpha = glowA * fade;
        g.drawImage(this.glow[st], b.x - glowR, b.y - glowR, glowR * 2, glowR * 2);
      }

      // Sway ring: subtle, only when the phone is actually swaying.
      if (b.sway > 0.15) {
        g.globalAlpha = Math.min(0.35, b.sway * 0.25) * a;
        g.strokeStyle = SOLID[st];
        g.lineWidth = 1.2;
        g.beginPath();
        g.arc(b.x, b.y, r + 5.5 + b.sway * 2, 0, TAU);
        g.stroke();
      }
      // Phones on a push: a red ring, gently pulsing.
      if (onWave) {
        g.globalAlpha = (0.55 + 0.35 * pulse) * a;
        g.strokeStyle = SOLID.wave;
        g.lineWidth = 2;
        g.beginPath();
        g.arc(b.x, b.y, r + 6 + pulse * 1.5, 0, TAU);
        g.stroke();
      }

      if (st === 'connecting' || b.data.outside) {
        g.save();
        g.translate(b.x, b.y);
        if (!this.reduced) g.rotate(now / 400);
        g.setLineDash([4, 4]);
        g.globalAlpha = 0.9 * a;
        g.strokeStyle = SOLID[st];
        g.lineWidth = 2;
        g.beginPath();
        g.arc(0, 0, r + 2, 0, TAU);
        g.stroke();
        g.restore();
      } else {
        // Motion status: a ring round the dot, cross-fading over 200 ms. "ok" (green) only shows on
        // a phone that is not packed in: a still phone in a crush must never look safe.
        const ring = (s: NodeStatus, alpha: number) => {
          if (alpha <= 0.01 || s === 'stale' || s === 'connecting' || (s === 'ok' && packed)) return;
          g.globalAlpha = alpha * (s === 'ok' ? 0.85 : 1);
          g.strokeStyle = SOLID[s];
          g.lineWidth = s === 'ok' ? 1.6 : 2.4;
          g.beginPath();
          g.arc(b.x, b.y, r + (s === 'ok' ? 2.4 : 3), 0, TAU);
          g.stroke();
        };
        if (fade < 1) ring(b.prevStatus, a * (1 - fade));
        ring(st, a * fade);
        // Core: how packed in this person is, on the crush ramp (pale when free); offline is dim grey.
        g.beginPath();
        g.arc(b.x, b.y, r, 0, TAU);
        g.globalAlpha = a;
        g.fillStyle = st === 'stale' ? SOLID.stale : this.crushStyle[Math.round(clamp(crush, 0, 1) * CRUSH_STEPS)];
        g.fill();
        // A thin edge keeps a pale dot visible on a light map and a deep red one on a dark map.
        g.globalAlpha = (light ? 0.9 : 0.8) * a;
        g.strokeStyle = light ? (packed ? '#ffffff' : 'rgba(71,85,105,0.9)') : crush > 0.6 ? 'rgba(254,202,202,0.8)' : outline;
        g.lineWidth = light && !packed ? 1.2 : crush > 0.6 && !light ? 1.1 : 2;
        g.stroke();
        if (b.flash > 0.05) {
          g.globalAlpha = b.flash * 0.5 * a;
          g.fillStyle = '#ffffff';
          g.beginPath();
          g.arc(b.x, b.y, r * 0.45, 0, TAU);
          g.fill();
        }
      }

      // A real phone's own colour: a ring round the status dot (bolder among simulated people).
      if (b.data.color && st !== 'stale') {
        const real = !!b.data.real;
        if (real) {
          g.globalAlpha = 0.9 * a;
          g.strokeStyle = outline;
          g.lineWidth = 6;
          g.beginPath();
          g.arc(b.x, b.y, r + 7.5, 0, TAU);
          g.stroke();
        }
        g.globalAlpha = a;
        g.strokeStyle = b.data.color;
        g.lineWidth = real ? 3.5 : 2.5;
        g.beginPath();
        g.arc(b.x, b.y, r + (real ? 7.5 : 6), 0, TAU);
        g.stroke();
      }

      if (this.hover === b.id || this.selected === b.id) {
        const sel = this.selected === b.id;
        g.globalAlpha = 1;
        g.strokeStyle = sel ? 'rgba(14,165,233,0.95)' : light ? 'rgba(15,23,42,0.6)' : 'rgba(226,232,240,0.9)';
        g.lineWidth = sel ? 2.5 : 1.5;
        g.setLineDash(sel ? [5, 4] : []);
        g.lineDashOffset = this.reduced ? 0 : -now / 40;
        g.beginPath();
        g.arc(b.x, b.y, r + 14, 0, TAU);
        g.stroke();
        g.setLineDash([]);
      }
    }
    g.globalAlpha = 1;
  }

  /**
   * Names of the real phones, in each phone's colour, readable from across a
   * table: all of them while there are few, else only the hovered / selected
   * one. Neighbours stand 0.6 m apart, so labels alternate above and below
   * the row on two levels each, joined to their dot by a thin line.
   */
  private drawNames(g: CanvasRenderingContext2D) {
    const all = this.named > 0 && this.named <= NAMES_MAX;
    const list = this.nameList;
    list.length = 0;
    for (const b of this.list) {
      if (!b.data.name || b.vis <= 0.3 || b.status === 'stale') continue;
      if (all || this.hover === b.id || this.selected === b.id) list.push(b);
    }
    if (!list.length) return;
    list.sort((p, q) => p.x - q.x || p.y - q.y);
    const light = this.theme === 'light';
    const k = this.view.k;
    const fs = (all && list.length <= 6 ? 15 : 13) / k;
    g.font = `700 ${fs}px Inter, system-ui, sans-serif`;
    g.textAlign = 'center';
    g.textBaseline = 'middle';
    g.lineJoin = 'round';
    const tiers = [-1, 1, -2, 2];
    for (let i = 0; i < list.length; i++) {
      const b = list[i];
      const t = list.length > 1 ? tiers[i % 4] : -1;
      const off = NODE_R + 9 + fs * 0.7 + (Math.abs(t) - 1) * (fs * 1.35 + 4 / k);
      const ly = b.y + Math.sign(t) * off;
      const a = easeOut(b.vis);
      const col = b.data.color ?? SOLID.ok;
      // Leader line from the dot to its label.
      g.globalAlpha = 0.55 * a;
      g.strokeStyle = col;
      g.lineWidth = 1.2 / k;
      g.beginPath();
      g.moveTo(b.x, b.y + Math.sign(t) * (NODE_R + 5));
      g.lineTo(b.x, ly - Math.sign(t) * fs * 0.55);
      g.stroke();
      // The name on a pill of the map's background, so it reads over people and links.
      const text = b.data.name!;
      const w = g.measureText(text).width + 10 / k, h = fs * 1.35;
      g.globalAlpha = 0.86 * a;
      g.fillStyle = light ? '#ffffff' : '#0a0e15';
      g.beginPath();
      g.roundRect(b.x - w / 2, ly - h / 2, w, h, h / 2);
      g.fill();
      g.globalAlpha = a;
      g.lineWidth = 1.2 / k;
      g.strokeStyle = col;
      g.stroke();
      g.fillStyle = light ? '#0f172a' : '#f8fafc';
      g.fillText(text, b.x, ly + fs * 0.04);
    }
    g.globalAlpha = 1;
    g.textAlign = 'left';
    g.textBaseline = 'alphabetic';
  }

  /** The demo spot: where the next phones that join will stand. */
  private drawDemoSpot(g: CanvasRenderingContext2D) {
    const d = this.demoSpot;
    if (!d?.on) return;
    const light = this.theme === 'light';
    const sp = d.spacing > 0 ? d.spacing : 0.6;
    g.strokeStyle = light ? 'rgba(15,23,42,0.45)' : 'rgba(226,232,240,0.5)';
    g.fillStyle = light ? 'rgba(15,23,42,0.6)' : 'rgba(226,232,240,0.7)';
    g.lineWidth = 1.2;
    g.setLineDash([3, 3]);
    for (let i = 0; i < 5; i++) {
      const x = d.x + i * sp;
      if (x > this.venue.w) break;
      const p = this.venueToWorld(x, d.y);
      g.globalAlpha = 1 - i * 0.15;
      g.beginPath();
      g.arc(p.x, p.y, NODE_R + 2, 0, Math.PI * 2);
      g.stroke();
    }
    g.setLineDash([]);
    g.globalAlpha = 1;
    const p0 = this.venueToWorld(d.x, d.y);
    g.font = '600 10px Inter, system-ui, sans-serif';
    g.textAlign = 'left';
    g.fillText('DEMO SPOT', p0.x - NODE_R - 2, p0.y + NODE_R + 16);
    // Each lined-up phone's place in the row (node.slot): the number its owner sees on their phone.
    g.font = '700 11px Inter, system-ui, sans-serif';
    g.textAlign = 'center';
    for (const b of this.bodies.values()) {
      if (!b.data.slot) continue;
      const p = this.venueToWorld(b.rx, b.ry);
      g.fillText(`#${b.data.slot}`, p.x, p.y - NODE_R - 7);
    }
    g.textAlign = 'left';
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
    if (this.floorplan?.complete && this.floorplan.naturalWidth) {
      g.save();
      g.globalAlpha = this.floorplanAlpha;
      g.drawImage(this.floorplan, ox, oy, W, H);
      g.restore();
    }
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

  /** The venue's own stage outline and exits (from the venue settings or Gemini's floor-plan reading). */
  private drawLayout(g: CanvasRenderingContext2D) {
    const L = this.layoutGeo;
    if (!L) return;
    const light = this.theme === 'light';
    if (L.stage && L.stage.length >= 3) {
      g.beginPath();
      L.stage.forEach(([x, y], i) => {
        const p = this.venueToWorld(x, y);
        if (i) g.lineTo(p.x, p.y);
        else g.moveTo(p.x, p.y);
      });
      g.closePath();
      g.fillStyle = light ? 'rgba(10,10,10,0.07)' : 'rgba(250,250,250,0.07)';
      g.fill();
      g.strokeStyle = light ? 'rgba(10,10,10,0.35)' : 'rgba(250,250,250,0.3)';
      g.lineWidth = 1.5;
      g.stroke();
      const c = L.stage.reduce((a, [x, y]) => [a[0] + x / L.stage!.length, a[1] + y / L.stage!.length], [0, 0]);
      const p = this.venueToWorld(c[0], c[1]);
      g.fillStyle = light ? 'rgba(10,10,10,0.5)' : 'rgba(250,250,250,0.5)';
      g.font = '600 10px Inter, system-ui, sans-serif';
      g.textAlign = 'center';
      g.fillText('STAGE', p.x, p.y + 3);
      g.textAlign = 'left';
    }
    g.lineCap = 'round';
    for (const [x0, y0, x1, y1] of L.walls ?? []) {
      const a = this.venueToWorld(x0, y0), b = this.venueToWorld(x1, y1);
      g.strokeStyle = light ? 'rgba(10,10,10,0.45)' : 'rgba(250,250,250,0.4)';
      g.lineWidth = 2.5;
      g.beginPath();
      g.moveTo(a.x, a.y);
      g.lineTo(b.x, b.y);
      g.stroke();
    }
    g.font = '600 10px Inter, system-ui, sans-serif';
    g.textAlign = 'center';
    for (const e of L.exits ?? []) {
      const a = this.venueToWorld(e.x0, e.y0), b = this.venueToWorld(e.x1, e.y1);
      g.strokeStyle = 'rgba(22,163,74,0.9)';
      g.lineWidth = 5;
      g.beginPath();
      g.moveTo(a.x, a.y);
      g.lineTo(b.x, b.y);
      g.stroke();
      g.fillStyle = 'rgba(22,163,74,0.95)';
      g.fillText(e.name || 'EXIT', (a.x + b.x) / 2, (a.y + b.y) / 2 - 7);
    }
    g.textAlign = 'left';
  }

  /** Signs and zone lights: draggable markers, with dashed lines to the boards each one hears over Bluetooth. */
  private drawBoards(g: CanvasRenderingContext2D, now: number) {
    const light = this.theme === 'light';
    const pos = new Map<string, { x: number; y: number }>();
    this.boards.forEach((b, i) => {
      pos.set(b.beacon || `PULSE-${b.zone}`, this.boardPos(b, i));
    });
    g.font = '600 10px Inter, system-ui, sans-serif';
    g.textAlign = 'center';
    const drawn = new Set<string>();
    this.boards.forEach((b, i) => {
      const a = this.boardPos(b, i);
      for (const peer of b.peers ?? []) {
        const q = pos.get(peer.name);
        const pair = [b.beacon ?? '', peer.name].sort().join('|');
        if (!q || drawn.has(pair)) continue;
        drawn.add(pair);
        g.setLineDash([5, 5]);
        g.lineDashOffset = -now / 60;
        g.strokeStyle = light ? 'rgba(37,99,235,0.6)' : 'rgba(96,165,250,0.6)';
        g.lineWidth = 1.5;
        g.beginPath();
        g.moveTo(a.x, a.y);
        g.lineTo(q.x, q.y);
        g.stroke();
        g.setLineDash([]);
        const mx = (a.x + q.x) / 2, my = (a.y + q.y) / 2;
        const label = `Bluetooth ≈${peer.dist.toFixed(1)} m${peer.mapDist != null ? ` · map ${peer.mapDist.toFixed(1)} m` : ''} · ${peer.rssi} dBm`;
        const tw = g.measureText(label).width;
        g.fillStyle = light ? 'rgba(255,255,255,0.95)' : 'rgba(20,20,20,0.9)';
        g.fillRect(mx - tw / 2 - 5, my - 9, tw + 10, 16);
        g.fillStyle = light ? 'rgba(37,99,235,1)' : 'rgba(147,197,253,1)';
        g.fillText(label, mx, my + 3);
      }
    });
    this.boards.forEach((b, i) => {
      const p = this.boardPos(b, i);
      const col = !b.online ? '#dc2626' : b.level === 'red' ? '#dc2626' : b.level === 'yellow' ? '#d97706' : '#16a34a';
      // Bluetooth range hint: a faint ring when the board counts devices.
      if (b.ble && b.online) {
        g.strokeStyle = `${col}33`;
        g.lineWidth = 1;
        g.beginPath();
        g.arc(p.x, p.y, 3 * this.fit.s, 0, Math.PI * 2);
        g.stroke();
      }
      g.fillStyle = light ? '#0a0a0a' : '#fafafa';
      g.beginPath();
      g.roundRect(p.x - 13, p.y - 13, 26, 26, 7);
      g.fill();
      g.fillStyle = col;
      g.beginPath();
      g.arc(p.x + 10, p.y - 10, 4.5, 0, Math.PI * 2);
      g.fill();
      g.fillStyle = light ? '#ffffff' : '#0a0a0a';
      g.font = '700 12px Inter, system-ui, sans-serif';
      g.fillText(b.kind === 'laptop' ? 'PC' : b.zone || 'S', p.x, p.y + 4);
      g.font = '600 10px Inter, system-ui, sans-serif';
      g.fillStyle = light ? 'rgba(10,10,10,0.7)' : 'rgba(250,250,250,0.7)';
      g.fillText(b.name + (b.x == null ? ' · drag me' : ''), p.x, p.y + 26);
    });
    g.textAlign = 'left';
  }

  /** Walls, furniture, doors and exits of the simulated venue (also drawn as a preview of a scenario before it runs). */
  private drawSimGeometry(g: CanvasRenderingContext2D) {
    const geo = this.simGeo;
    if (!geo || (!this.sim && !this.simPreview)) return;
    const light = this.theme === 'light';
    const { s } = this.fit;
    this.drawFurniture(g, geo.furniture ?? [], light, s);
    g.lineCap = 'round';
    g.strokeStyle = light ? 'rgba(15,23,42,0.55)' : 'rgba(203,213,225,0.55)';
    g.lineWidth = 3;
    g.beginPath();
    for (const [x0, y0, x1, y1] of geo.walls ?? []) {
      const a = this.venueToWorld(x0, y0), b = this.venueToWorld(x1, y1);
      g.moveTo(a.x, a.y);
      g.lineTo(b.x, b.y);
    }
    g.stroke();
    g.font = '600 10px Inter, system-ui, sans-serif';
    g.textAlign = 'center';
    for (const e of geo.exits ?? []) {
      const a = this.venueToWorld(e.x0, e.y0), b = this.venueToWorld(e.x1, e.y1);
      const mx = (a.x + b.x) / 2, my = (a.y + b.y) / 2;
      const door = e.kind === 'door', stile = e.kind === 'turnstile', em = e.kind === 'emergency';
      // Open: green for a way out, amber-white for an inner door, blue for a turnstile; closed: red, dashed.
      const col = !e.open ? 'rgba(239,68,68,0.95)' : door ? (light ? 'rgba(217,119,6,0.9)' : 'rgba(251,191,36,0.9)') : stile ? 'rgba(56,189,248,0.95)' : 'rgba(34,197,94,0.95)';
      g.strokeStyle = col;
      g.lineWidth = door ? 4 : 5;
      g.setLineDash(e.open ? (em ? [6, 3] : []) : [4, 4]);
      g.beginPath();
      g.moveTo(a.x, a.y);
      g.lineTo(b.x, b.y);
      g.stroke();
      g.setLineDash([]);
      if (door && e.open) {
        // A door leaf, swung open: a short arc from one jamb.
        const len = Math.hypot(b.x - a.x, b.y - a.y);
        const ang = Math.atan2(b.y - a.y, b.x - a.x);
        g.lineWidth = 1.5;
        g.beginPath();
        g.arc(a.x, a.y, len, ang - Math.PI / 2, ang, false);
        g.lineTo(a.x, a.y);
        g.stroke();
      }
      g.fillStyle = col;
      const label = !e.open ? 'CLOSED' : door ? '' : stile ? e.name.replace(/^Turnstile /, '') : em ? 'EMERGENCY' : 'EXIT';
      if (label) {
        // Keep the label inside the map: below an exit along the top wall, above one along the bottom.
        const top = e.y0 <= 0.01 && e.y1 <= 0.01;
        g.fillText(label, mx, top ? my + 14 : my - 6);
      }
    }
    g.textAlign = 'left';
  }

  /** Desks, seat rows, stage, counters, tiered aisles, turnstiles and fences, in venue metres. */
  private drawFurniture(g: CanvasRenderingContext2D, items: SimFurniture[], light: boolean, s: number) {
    if (!items.length) return;
    const ink = light ? 'rgba(15,23,42,' : 'rgba(226,232,240,';
    const wood = light ? 'rgba(180,140,90,' : 'rgba(170,130,80,';
    g.lineCap = 'butt';
    g.lineJoin = 'round';
    g.textAlign = 'center';
    g.font = `600 ${Math.max(9, Math.min(12, 0.4 * s))}px Inter, system-ui, sans-serif`;
    for (const f of items) {
      const a = this.venueToWorld(f.x0, f.y0), b = this.venueToWorld(f.x1, f.y1);
      const x = Math.min(a.x, b.x), y = Math.min(a.y, b.y), w = Math.abs(b.x - a.x), h = Math.abs(b.y - a.y);
      switch (f.kind) {
        case 'desk': {
          g.fillStyle = wood + (light ? '0.35)' : '0.3)');
          g.strokeStyle = wood + '0.8)';
          g.lineWidth = 1;
          g.beginPath();
          g.roundRect(x, y, w, h, Math.min(3, h / 3));
          g.fill();
          g.stroke();
          if (f.label) {
            g.fillStyle = ink + '0.6)';
            g.fillText(f.label, x + w / 2, y + h / 2 + 3.5);
          }
          break;
        }
        case 'chairs':
        case 'seats': {
          // n seats along the row, each a small square with a backrest on the side away from the front (the top).
          const n = f.n ?? 1;
          const pitch = w / n;
          const sw = Math.min(pitch * 0.72, 0.5 * s), sd = Math.min(h * 0.8, 0.45 * s);
          const fixed = f.kind === 'seats';
          g.fillStyle = fixed ? (light ? 'rgba(127,29,29,0.22)' : 'rgba(153,27,27,0.35)') : ink + (light ? '0.1)' : '0.12)');
          g.strokeStyle = fixed ? (light ? 'rgba(127,29,29,0.5)' : 'rgba(248,113,113,0.45)') : ink + '0.35)';
          g.lineWidth = 1;
          g.beginPath();
          for (let i = 0; i < n; i++) {
            const cx = x + (i + 0.5) * pitch, cy = y + h / 2;
            g.roundRect(cx - sw / 2, cy - sd / 2, sw, sd, Math.min(2.5, sw / 4));
          }
          g.fill();
          g.stroke();
          // Backrests.
          g.lineWidth = Math.max(1.5, 0.08 * s);
          g.beginPath();
          for (let i = 0; i < n; i++) {
            const cx = x + (i + 0.5) * pitch, cy = y + h / 2;
            g.moveTo(cx - sw / 2, cy + sd / 2);
            g.lineTo(cx + sw / 2, cy + sd / 2);
          }
          g.stroke();
          break;
        }
        case 'stage': {
          g.fillStyle = light ? 'rgba(10,10,10,0.07)' : 'rgba(250,250,250,0.07)';
          g.fillRect(x, y, w, h);
          g.strokeStyle = light ? 'rgba(10,10,10,0.35)' : 'rgba(250,250,250,0.3)';
          g.lineWidth = 1.5;
          g.strokeRect(x, y, w, h);
          g.fillStyle = ink + '0.5)';
          g.fillText((f.label ?? 'STAGE').toUpperCase(), x + w / 2, y + h / 2 + 3.5);
          break;
        }
        case 'board': {
          g.fillStyle = light ? 'rgba(255,255,255,0.9)' : 'rgba(241,245,249,0.85)';
          g.fillRect(x, y, w, Math.max(h, 3));
          g.strokeStyle = ink + '0.5)';
          g.lineWidth = 1;
          g.strokeRect(x, y, w, Math.max(h, 3));
          g.fillStyle = ink + '0.55)';
          g.fillText(f.label ?? 'Board', x + w / 2, y + Math.max(h, 3) + 11);
          break;
        }
        case 'counter': {
          g.fillStyle = wood + (light ? '0.45)' : '0.4)');
          g.fillRect(x, y, w, h);
          g.strokeStyle = wood + '0.9)';
          g.lineWidth = 1.2;
          g.strokeRect(x, y, w, h);
          g.fillStyle = light ? 'rgba(255,255,255,0.9)' : 'rgba(15,23,42,0.8)';
          g.fillText((f.label ?? 'Bar').toUpperCase(), x + w / 2, y + h / 2 + 3.5);
          break;
        }
        case 'stairs': {
          // Treads: a ladder of thin lines down the aisle.
          g.strokeStyle = ink + (light ? '0.18)' : '0.16)');
          g.lineWidth = 1;
          g.beginPath();
          const step = Math.max(4, 0.3 * s);
          for (let yy = y + step / 2; yy < y + h; yy += step) {
            g.moveTo(x, yy);
            g.lineTo(x + w, yy);
          }
          g.stroke();
          break;
        }
        case 'turnstile': {
          g.fillStyle = 'rgba(56,189,248,0.18)';
          g.fillRect(x, y, w, h);
          g.strokeStyle = 'rgba(56,189,248,0.7)';
          g.lineWidth = 1.2;
          g.strokeRect(x, y, w, h);
          // The arm.
          g.beginPath();
          g.moveTo(x + w / 2, y + h / 2);
          g.lineTo(x + w, y + h / 2);
          g.stroke();
          break;
        }
        case 'gate': {
          g.fillStyle = ink + '0.08)';
          g.fillRect(x, y, w, Math.max(h, 3));
          if (f.label) {
            g.fillStyle = ink + '0.55)';
            g.fillText(f.label, x + w / 2, y + Math.max(h, 3) + 11);
          }
          break;
        }
        case 'fence': {
          g.strokeStyle = ink + '0.6)';
          g.lineWidth = 2;
          g.setLineDash([2, 4]);
          g.beginPath();
          g.moveTo(a.x, a.y);
          g.lineTo(b.x, b.y);
          g.stroke();
          g.setLineDash([]);
          break;
        }
        case 'label': {
          if (!f.label) break;
          g.save();
          g.translate(x + w / 2, y + h / 2);
          if (h > w) g.rotate(-Math.PI / 2);
          g.fillStyle = ink + '0.5)';
          g.fillText(f.label, 0, 3.5);
          g.restore();
          break;
        }
      }
    }
    g.textAlign = 'left';
  }

  /**
   * Simulated people seen from above, interpolated at 10 Hz: a shoulder
   * ellipse turned to face the way the body faces, with a head a little
   * forward of its centre; seated people narrower and sunk into their seat;
   * someone being pushed leans into it (head forward, a short trail
   * behind). The fill is the crush ramp from the simulator's truth (grey
   * when free, amber → red as they are squeezed and the ellipse compresses).
   * Cheap enough for 1,000 bodies at 60 fps: one fill path per tint step for
   * shoulders, one for heads.
   */
  private drawSimBodies(g: CanvasRenderingContext2D) {
    const n = this.simN;
    if (!this.sim || !n) return;
    const { s, ox, oy } = this.fit;
    const light = this.theme === 'light';
    // Shoulder half-width in px: body-sized (0.23 m) when zoomed in, never under 2.6 px.
    const r = Math.max(2.6, Math.min(10, 0.23 * s));
    const tiny = r < 3.2; // zoomed far out: plain dots are all that reads
    const D = this.simDraw, B = this.simBucket, ph = this.simPhone, st = this.simState;
    const Q = 16; // tint steps
    const dens = this.simDens;
    let maxB = 0;
    for (let i = 0; i < n; i++) {
      // The truth: packed density and body pressure → the crush ramp (grey when free).
      const q = Math.round(bodyCrush(D[i * SIM_STRIDE + 2], dens[i]) * Q);
      B[i] = q;
      if (q > maxB) maxB = q;
    }
    // A halo around the people who are really being crushed (danger density and up, or squeezed hard).
    const hot = Math.ceil(0.72 * Q);
    if (maxB >= hot) {
      g.fillStyle = rgba(RED, light ? 0.18 : 0.24);
      g.beginPath();
      for (let i = 0; i < n; i++) {
        if (B[i] < hot) continue;
        const x = ox + D[i * SIM_STRIDE] * s, y = oy + D[i * SIM_STRIDE + 1] * s;
        g.moveTo(x + r * 2.2, y);
        g.arc(x, y, r * 2.2, 0, Math.PI * 2);
      }
      g.fill();
    }
    // Trails of people being pushed: a short smear back along their motion.
    if (!tiny) {
      g.strokeStyle = light ? 'rgba(220,38,38,0.35)' : 'rgba(248,113,113,0.4)';
      g.lineWidth = Math.max(1, r * 0.5);
      g.lineCap = 'round';
      g.beginPath();
      let any = false;
      const P = this.simPrev, C = this.simCur;
      for (let i = 0; i < n; i++) {
        const j = i * SIM_STRIDE;
        if (st[i] !== ST_PUSHING && D[j + 2] < 300) continue;
        const vx = C[j] - P[j], vy = C[j + 1] - P[j + 1];
        const l = Math.hypot(vx, vy);
        if (l < 0.02) continue;
        const k = Math.min(0.6, l * 4) * s / l;
        const x = ox + D[j] * s, y = oy + D[j + 1] * s;
        g.moveTo(x, y);
        g.lineTo(x - vx * k, y - vy * k);
        any = true;
      }
      if (any) g.stroke();
    }
    // Shoulders: one path per tint step.
    const headStyle = light ? 'rgba(30,41,59,0.55)' : 'rgba(15,23,42,0.75)';
    for (let q = 0; q <= maxB; q++) {
      g.beginPath();
      let any = false;
      for (let i = 0; i < n; i++) {
        if (B[i] !== q) continue;
        const j = i * SIM_STRIDE;
        const x = ox + D[j] * s, y = oy + D[j + 1] * s;
        if (tiny) {
          g.moveTo(x + r, y);
          g.arc(x, y, r, 0, Math.PI * 2);
          any = true;
          continue;
        }
        const state = st[i];
        const squeeze = 1 - (0.3 * q) / Q; // compressed as the crush builds
        // Seated: narrower shoulders, sunk into the seat. Standing/walking: a full shoulder line.
        const rw = state === ST_SEATED ? r * 0.8 : r * squeeze;
        const rd = state === ST_SEATED ? r * 0.5 : r * 0.58;
        const hd = D[j + 3];
        // ellipse(): rotation is of the x semi-axis, so the shoulders (wide axis) lie across the heading.
        g.moveTo(x + rw * Math.cos(hd + Math.PI / 2), y + rw * Math.sin(hd + Math.PI / 2));
        g.ellipse(x, y, rw, rd, hd + Math.PI / 2, 0, Math.PI * 2);
        any = true;
      }
      if (!any) continue;
      g.fillStyle = this.bodyStyle[Math.round((q / Q) * CRUSH_STEPS)];
      g.fill();
    }
    if (tiny) return;
    // Heads: a dot forward of the shoulder centre (further forward when leaning into a push).
    g.beginPath();
    for (let i = 0; i < n; i++) {
      const j = i * SIM_STRIDE;
      if (ph[i] && r < 5) continue; // the phone dot sits where the head would be
      const x = ox + D[j] * s, y = oy + D[j + 1] * s;
      const hd = D[j + 3];
      const state = st[i];
      const lean = state === ST_PUSHING ? 0.42 : state === ST_SEATED ? 0.05 : state === ST_WALKING ? 0.22 : state === ST_QUEUEING ? 0.1 : 0.15;
      const hr = r * 0.42;
      const hx = x + Math.cos(hd) * r * lean, hy = y + Math.sin(hd) * r * lean;
      g.moveTo(hx + hr, hy);
      g.arc(hx, hy, hr, 0, Math.PI * 2);
    }
    g.fillStyle = headStyle;
    g.fill();
  }

  /**
   * Clusters, drawn where they are: a hull around the members (phones, or
   * simulated people) padded by ~0.6 m. Yellow and red clusters get a crisp
   * outline (red pulses) and a badge; calm ones a faint dashed outline.
   */
  private drawClusters(g: CanvasRenderingContext2D, pulse: number) {
    const light = this.theme === 'light';
    const s = this.fit.s;
    const pad = Math.max(6, 0.6 * s);
    this.badges.length = 0;
    for (let ci = 0; ci < this.clusters.length; ci++) {
      const c = this.clusters[ci];
      const level = c.level ?? 'calm';
      const col: RGB = level === 'red' ? WAVE : level === 'yellow' ? AMBER : CALM_CLUSTER;
      const n = this.clusterHull(c);
      const p = this.venueToWorld(c.x, c.y);
      this.tracePadded(g, n, pad, p.x, p.y, Math.max(pad, Math.min(c.r, 1.5) * s));
      let top = p.y - pad;
      for (let i = 0; i < n; i++) top = Math.min(top, this.hp[this.hull[i] * 2 + 1] - pad);

      if (level === 'calm') {
        g.setLineDash([4, 5]);
        g.strokeStyle = rgba(col, light ? 0.45 : 0.35);
        g.lineWidth = 1;
        g.stroke();
        g.setLineDash([]);
        // A calm crowd needs no caption (its numbers are in the dot tooltips); the outline alone says "a group".
        continue;
      }

      const red = level === 'red';
      g.fillStyle = rgba(col, red ? 0.1 : 0.07);
      g.fill();
      // Soft halo stroke, then the crisp line.
      g.strokeStyle = rgba(col, red ? 0.12 + 0.14 * pulse : 0.12);
      g.lineWidth = red ? 7 + 3 * pulse : 6;
      g.stroke();
      g.strokeStyle = rgba(col, red ? 0.8 + 0.2 * pulse : 0.85);
      g.lineWidth = red ? 2.2 : 1.8;
      g.setLineDash(red ? [] : [7, 4]);
      g.stroke();
      g.setLineDash([]);
      // The badge is drawn later, above the nodes.
      this.badges.push(ci, p.x, top);
    }
  }

  /** Badges of yellow/red clusters: people, density, trend and the early-warning countdown. */
  private drawBadges(g: CanvasRenderingContext2D) {
    const B = this.badges;
    g.font = '700 11px Inter, system-ui, sans-serif';
    g.textAlign = 'center';
    for (let k = 0; k < B.length; k += 3) {
      const ci = B[k], x = B[k + 1], top = B[k + 2];
      const red = this.clusters[ci]?.level === 'red';
      const col = red ? WAVE : AMBER;
      const label = this.clusterLabels[ci] ?? '';
      const tw = g.measureText(label).width;
      const by = top - 26;
      g.fillStyle = rgba(col, 0.96);
      g.beginPath();
      g.roundRect(x - tw / 2 - 8, by, tw + 16, 19, 9.5);
      g.moveTo(x - 5, by + 18);
      g.lineTo(x + 5, by + 18);
      g.lineTo(x, by + 24);
      g.closePath();
      g.fill();
      g.fillStyle = red ? '#ffffff' : '#1c1917';
      g.fillText(label, x, by + 13.5);
    }
    g.textAlign = 'left';
  }

  /** Members of a cluster in world px (into this.hp), then their convex hull (indices into this.hull). Returns the hull size. */
  private clusterHull(c: Cluster): number {
    const hp = this.hp;
    hp.length = 0;
    const r2 = (c.r + 0.75) ** 2;
    const { s, ox, oy } = this.fit;
    if (this.simN) {
      const D = this.simDraw;
      for (let i = 0; i < this.simN; i++) {
        const x = D[i * SIM_STRIDE], y = D[i * SIM_STRIDE + 1];
        if ((x - c.x) ** 2 + (y - c.y) ** 2 < r2) hp.push(ox + x * s, oy + y * s);
      }
    } else {
      for (const b of this.list) {
        if (b.gone || b.data.outside) continue;
        if ((b.px - c.x) ** 2 + (b.py - c.y) ** 2 < r2) hp.push(b.x, b.y);
      }
    }
    const m = hp.length / 2;
    const idx = this.hIdx;
    idx.length = m;
    for (let i = 0; i < m; i++) idx[i] = i;
    idx.sort((a, b) => hp[a * 2] - hp[b * 2] || hp[a * 2 + 1] - hp[b * 2 + 1]);
    // Andrew's monotone chain, skipping duplicate points.
    const h = this.hull;
    h.length = 0;
    const cross = (o: number, a: number, b: number) =>
      (hp[a * 2] - hp[o * 2]) * (hp[b * 2 + 1] - hp[o * 2 + 1]) - (hp[a * 2 + 1] - hp[o * 2 + 1]) * (hp[b * 2] - hp[o * 2]);
    let u = 0;
    for (let i = 0; i < m; i++) {
      const j = idx[i];
      if (u && hp[idx[u - 1] * 2] === hp[j * 2] && hp[idx[u - 1] * 2 + 1] === hp[j * 2 + 1]) continue;
      idx[u++] = j;
    }
    if (u <= 2) {
      for (let i = 0; i < u; i++) h.push(idx[i]);
      return u;
    }
    for (let i = 0; i < u; i++) {
      while (h.length >= 2 && cross(h[h.length - 2], h[h.length - 1], idx[i]) <= 0) h.pop();
      h.push(idx[i]);
    }
    const lower = h.length + 1;
    for (let i = u - 2; i >= 0; i--) {
      while (h.length >= lower && cross(h[h.length - 2], h[h.length - 1], idx[i]) <= 0) h.pop();
      h.push(idx[i]);
    }
    h.pop();
    return h.length;
  }

  /** Trace the hull grown outward by pad (rounded corners). Falls back to a small disc with no members. */
  private tracePadded(g: CanvasRenderingContext2D, n: number, pad: number, cx: number, cy: number, fallbackR: number) {
    const hp = this.hp, h = this.hull;
    g.beginPath();
    if (n === 0) {
      g.arc(cx, cy, fallbackR, 0, Math.PI * 2);
      return;
    }
    if (n === 1) {
      g.arc(hp[h[0] * 2], hp[h[0] * 2 + 1], pad, 0, Math.PI * 2);
      return;
    }
    // Screen-clockwise order (positive shoelace area with y down), so outward normals are (dy, −dx).
    let area = 0;
    for (let i = 0; i < n; i++) {
      const a = h[i], b = h[(i + 1) % n];
      area += hp[a * 2] * hp[b * 2 + 1] - hp[b * 2] * hp[a * 2 + 1];
    }
    const at = (i: number) => (area >= 0 ? h[((i % n) + n) % n] : h[n - 1 - (((i % n) + n) % n)]);
    for (let i = 0; i < n; i++) {
      const p0 = at(i - 1), p1 = at(i), p2 = at(i + 1);
      const a0 = Math.atan2(-(hp[p1 * 2] - hp[p0 * 2]), hp[p1 * 2 + 1] - hp[p0 * 2 + 1]);
      const a1 = Math.atan2(-(hp[p2 * 2] - hp[p1 * 2]), hp[p2 * 2 + 1] - hp[p1 * 2 + 1]);
      g.arc(hp[p1 * 2], hp[p1 * 2 + 1], pad, a0, a1, false);
    }
    g.closePath();
  }

  /** Where a phone's dot is drawn (world px, the space overlays draw in); null = not on the map. */
  bodyAt(id: string): { x: number; y: number } | null {
    const b = this.bodies.get(id);
    return b && !b.gone && b.vis > 0.05 ? b : null;
  }

  neighbourCount(id: string) {
    const b = this.bodies.get(id);
    return b ? b.nbrs.length : 0;
  }

  /** Screen position of a body, for anchoring the tooltip. */
  position(id: string) {
    const b = this.bodies.get(id);
    return b ? this.toScreen(b.x, b.y) : null;
  }

  /** Screen position of a body's latest reported spot (where the dot is heading). */
  homeOf(id: string) {
    const b = this.bodies.get(id);
    return b ? this.toScreen(b.hx, b.hy) : null;
  }
}
