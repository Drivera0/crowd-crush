// The phone's own sway trace, and how it correlates with a neighbour's.
//
// Same idea as the server's detector, done on the phone: level each motion
// summary with gravity, keep the horizontal part, band-pass it to the sway
// band (0.15–1.5 Hz), project it on its dominant direction and resample it on
// a 100 ms grid in server time. Two phones' traces are then compared by lagged
// cross-correlation over the last few seconds. Pure functions and plain
// classes: no DOM, no network.

export const GRID_MS = 100;
/** Samples kept: 9 s. */
const KEEP = 90;
/** Correlation window (samples) and the lags searched (± samples). */
export const CORR_WINDOW = 60;
export const MAX_LAG = 15;
const MIN_OVERLAP = 30;
/** Both phones need at least this much sway (m/s² RMS) for a correlation to mean anything. */
export const MIN_RMS = 0.05;
/** Rotation rate above which the phone is being handled, and how long after it readings are skipped. */
const HANDLING_ROT = 200;
const HANDLING_SETTLE_MS = 1000;

/** A series on the 100 ms grid: values by absolute grid index; NaN = no valid sample. */
export class Series {
  /** Grid index of v[0]. */
  first = 0;
  v: number[] = [];

  get last() {
    return this.first + this.v.length - 1;
  }

  at(i: number): number {
    const k = i - this.first;
    return k >= 0 && k < this.v.length ? this.v[k] : NaN;
  }

  set(i: number, val: number) {
    if (this.v.length === 0 || i > this.last + KEEP || i < this.first - KEEP) {
      // empty, or a jump (clock re-sync): start over
      this.first = i;
      this.v = [val];
      return;
    }
    if (i < this.first) return; // too old to matter
    while (this.last < i) this.v.push(NaN);
    this.v[i - this.first] = val;
    if (this.v.length > KEEP) {
      const drop = this.v.length - KEEP;
      this.v.splice(0, drop);
      this.first += drop;
    }
  }

  /** The newest n samples as [first index, values]. */
  tail(n: number): [number, number[]] {
    const k = Math.max(0, this.v.length - n);
    return [this.first + k, this.v.slice(k)];
  }
}

/** Unit vector, or null for a zero vector. */
function unit(v: [number, number, number]): [number, number, number] | null {
  const n = Math.hypot(v[0], v[1], v[2]);
  return n > 1e-9 ? [v[0] / n, v[1] / n, v[2] / n] : null;
}

/**
 * Builds this phone's trace from its 100 ms motion summaries.
 * add(t, a, down, rot): t in server time (ms), a = mean acceleration in the
 * device frame, down = unit gravity direction in the device frame (null =
 * unknown: the phone is taken to be upright, x and z horizontal).
 */
export class OwnTrace {
  readonly series = new Series();
  private e1: [number, number, number] = [1, 0, 0];
  private e2: [number, number, number] = [0, 0, 1];
  private down: [number, number, number] | null = null;
  // band-pass state per horizontal component
  private lp = [0, 0];
  private hpIn = [0, 0];
  private hp = [0, 0];
  private primed = false;
  // running covariance of the band-passed pair, and the axis it gives
  private cxx = 0;
  private cyy = 0;
  private cxy = 0;
  private axis: [number, number] = [1, 0];
  private prevT = 0;
  private prevV = NaN;
  private quietUntil = 0;

  private basis(down: [number, number, number] | null) {
    if (!down) {
      this.e1 = [1, 0, 0];
      this.e2 = [0, 0, 1];
      this.down = null;
      return;
    }
    // Keep the basis while gravity has barely turned, so the trace doesn't jump.
    if (this.down && this.down[0] * down[0] + this.down[1] * down[1] + this.down[2] * down[2] > 0.985) return;
    this.down = down;
    // e1: the previous e1 with its vertical part removed (continuity), else the device axis least aligned with gravity.
    const proj = (r: [number, number, number]) => {
      const d = r[0] * down[0] + r[1] * down[1] + r[2] * down[2];
      return unit([r[0] - d * down[0], r[1] - d * down[1], r[2] - d * down[2]]);
    };
    let e1 = proj(this.e1);
    if (!e1) {
      const ax = Math.abs(down[0]), ay = Math.abs(down[1]), az = Math.abs(down[2]);
      e1 = proj(ax <= ay && ax <= az ? [1, 0, 0] : az <= ay ? [0, 0, 1] : [0, 1, 0]);
    }
    if (!e1) return;
    this.e1 = e1;
    this.e2 = [down[1] * e1[2] - down[2] * e1[1], down[2] * e1[0] - down[0] * e1[2], down[0] * e1[1] - down[1] * e1[0]];
  }

  add(t: number, a: [number, number, number], down: [number, number, number] | null, rot: number) {
    const dt = this.prevT ? (t - this.prevT) / 1000 : 0.1;
    if (dt <= 0) return;
    if (dt > 1) this.primed = false; // a gap: restart the filters
    this.basis(down ? unit(down) : null);
    const u = [
      a[0] * this.e1[0] + a[1] * this.e1[1] + a[2] * this.e1[2],
      a[0] * this.e2[0] + a[1] * this.e2[1] + a[2] * this.e2[2],
    ];
    const step = Math.min(dt, 0.3);
    const kl = 1 - Math.exp(-2 * Math.PI * 1.5 * step); // low-pass 1.5 Hz
    const kh = Math.exp(-2 * Math.PI * 0.15 * step); // high-pass 0.15 Hz
    const f = [0, 0];
    for (let i = 0; i < 2; i++) {
      if (!this.primed) {
        this.lp[i] = u[i];
        this.hpIn[i] = u[i];
        this.hp[i] = 0;
      }
      this.lp[i] += kl * (u[i] - this.lp[i]);
      this.hp[i] = kh * (this.hp[i] + this.lp[i] - this.hpIn[i]);
      this.hpIn[i] = this.lp[i];
      f[i] = this.hp[i];
    }
    this.primed = true;
    // Dominant horizontal direction: principal axis of the last ~5 s.
    const kc = 1 - Math.exp(-step / 5);
    this.cxx += kc * (f[0] * f[0] - this.cxx);
    this.cyy += kc * (f[1] * f[1] - this.cyy);
    this.cxy += kc * (f[0] * f[1] - this.cxy);
    const th = 0.5 * Math.atan2(2 * this.cxy, this.cxx - this.cyy);
    let ax: [number, number] = [Math.cos(th), Math.sin(th)];
    if (ax[0] * this.axis[0] + ax[1] * this.axis[1] < 0) ax = [-ax[0], -ax[1]]; // an axis has no sign: keep the one we had
    this.axis = ax;
    if (rot > HANDLING_ROT) this.quietUntil = t + HANDLING_SETTLE_MS;
    const v = t < this.quietUntil ? NaN : f[0] * ax[0] + f[1] * ax[1];
    // Resample on the grid: every grid point between the previous summary and this one.
    if (this.prevT && dt <= 1) {
      for (let g = Math.floor(this.prevT / GRID_MS) + 1; g * GRID_MS <= t; g++) {
        const w = (g * GRID_MS - this.prevT) / (t - this.prevT);
        this.series.set(g, Number.isNaN(v) || Number.isNaN(this.prevV) ? NaN : this.prevV + w * (v - this.prevV));
      }
    }
    this.prevT = t;
    this.prevV = v;
  }
}

export interface Corr {
  /** Peak |r|, 0 when there is too little motion or overlap. */
  corr: number;
  /** Lag of the peak in ms; positive = b moves after a. */
  lagMs: number;
}

/**
 * Lagged cross-correlation of two traces over the newest CORR_WINDOW samples
 * both have. For each lag L (−MAX_LAG…+MAX_LAG) the Pearson r of a[i] against
 * b[i + L]; the answer is the largest |r|, its lag refined by a parabola
 * through the neighbouring lags.
 */
export function correlate(a: Series, b: Series): Corr {
  const none = { corr: 0, lagMs: 0 };
  if (!a.v.length || !b.v.length) return none;
  const end = Math.min(a.last, b.last);
  const start = end - CORR_WINDOW + 1;
  const rs: number[] = [];
  let best = -1, bestL = 0;
  for (let L = -MAX_LAG; L <= MAX_LAG; L++) {
    let n = 0, sa = 0, sb = 0, saa = 0, sbb = 0, sab = 0;
    for (let i = start; i <= end; i++) {
      const x = a.at(i), y = b.at(i + L);
      if (Number.isNaN(x) || Number.isNaN(y)) continue;
      n++;
      sa += x;
      sb += y;
      saa += x * x;
      sbb += y * y;
      sab += x * y;
    }
    let r = 0;
    if (n >= MIN_OVERLAP) {
      const va = saa / n - (sa / n) ** 2, vb = sbb / n - (sb / n) ** 2;
      if (va > MIN_RMS * MIN_RMS && vb > MIN_RMS * MIN_RMS) r = Math.abs((sab / n - (sa / n) * (sb / n)) / Math.sqrt(va * vb));
    }
    rs.push(r);
    if (r > best) {
      best = r;
      bestL = L;
    }
  }
  if (best <= 0) return none;
  const k = bestL + MAX_LAG;
  let frac = 0;
  if (k > 0 && k < rs.length - 1) {
    const d = rs[k - 1] - 2 * rs[k] + rs[k + 1];
    if (d < 0) frac = Math.max(-0.5, Math.min(0.5, (0.5 * (rs[k - 1] - rs[k + 1])) / d));
  }
  return { corr: Math.min(1, Math.round(best * 100) / 100), lagMs: Math.round((bestL + frac) * GRID_MS) };
}
