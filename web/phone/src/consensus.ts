// Phones correcting each other's positions, on the phone.
//
// About once a second a phone pulls its own position estimate toward the
// estimates of the direct mesh peers it is confirmed to be standing with
// (their sway correlates with its own), weighted by inverse variance. A
// phone pressed against a well-placed one inherits most of its accuracy, and
// passes it on to the next.
//
// Rules that keep this honest:
//   - A neighbour's position is evidence of mine only to within the distance
//     between us: its variance is the neighbour's plus NEAR_M².
//   - Floor: the result is never more certain than the best neighbour used
//     plus NEAR_M (unless the phone's own source is already better).
//   - No feedback loops: corrected ("fused") estimates only flow away from
//     anchored phones. A neighbour's fused estimate is used only when it is
//     strictly closer to an anchor (in hops) than this phone; from any other
//     neighbour only its raw own-source estimate is used, which contains
//     nothing this phone told it.
//   - Age: a neighbour's estimate loses AGE_M_PER_S of accuracy per second
//     since it was heard, and is ignored after MAX_AGE_S.
//   - Anchored phones (placed exactly a moment ago) barely move: neighbours
//     count ANCHOR_PULL as much.
//
// Pure maths: no DOM, no network. Tested in consensus.test.ts.

/** A position estimate: venue metres and 1-sigma accuracy (m). */
export interface Est {
  x: number;
  y: number;
  s: number;
}

export interface PeerEst {
  /** The neighbour's mesh-corrected estimate, as it broadcasts it. */
  fused: Est;
  /** Its own-source estimate before correction. */
  raw: Est;
  /** Its hops from an anchored source: 0 = anchored itself, null = none in reach. */
  ah: number | null;
  /** Seconds since this was heard. */
  age: number;
}

export interface Fused {
  est: Est;
  /** Hops from an anchored source, null = none in reach. */
  ah: number | null;
  /** Neighbours that went into it. */
  n: number;
}

/** How far apart two phones that share motion can be (m, 1 sigma). */
export const NEAR_M = 0.7;
export const AGE_M_PER_S = 0.5;
export const MAX_AGE_S = 4;
export const ANCHOR_PULL = 0.1;
const MAX_AH = 16;

/**
 * One consensus step. own = this phone's own-source estimate (null = it has
 * none: the result is built from the neighbours alone); anchored = that
 * source is an exact placement; peers = the motion-confirmed direct peers.
 * Returns null when there is nothing to go on.
 */
export function consensus(own: Est | null, anchored: boolean, peers: PeerEst[]): Fused | null {
  const fresh = peers.filter((p) => p.age <= MAX_AGE_S);
  let ah: number | null = anchored && own ? 0 : null;
  if (ah === null) {
    for (const p of fresh) if (p.ah !== null && p.ah + 1 <= MAX_AH && (ah === null || p.ah + 1 < ah)) ah = p.ah + 1;
  }
  let w = 0, x = 0, y = 0, n = 0;
  let best = Infinity;
  if (own) {
    const w0 = 1 / (own.s * own.s);
    w += w0;
    x += w0 * own.x;
    y += w0 * own.y;
  }
  for (const p of fresh) {
    // Fused only from phones strictly nearer an anchor; otherwise their raw estimate.
    const e = p.ah !== null && ah !== null && p.ah < ah ? p.fused : p.raw;
    if (!(e.s > 0) || !Number.isFinite(e.x) || !Number.isFinite(e.y)) continue;
    const s = Math.sqrt(e.s * e.s + NEAR_M * NEAR_M) + AGE_M_PER_S * p.age;
    const wj = (anchored ? ANCHOR_PULL : 1) / (s * s);
    w += wj;
    x += wj * e.x;
    y += wj * e.y;
    best = Math.min(best, e.s + NEAR_M);
    n++;
  }
  if (w <= 0) return null;
  const fusedS = 1 / Math.sqrt(w);
  // Never more certain than the best neighbour plus NEAR_M, unless the own source already is.
  const floor = Math.min(own ? own.s : Infinity, best);
  return { est: { x: x / w, y: y / w, s: Math.max(fusedS, floor) }, ah, n };
}
