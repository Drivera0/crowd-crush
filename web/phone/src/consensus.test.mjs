// Unit tests for the phone-side maths (no browser needed):
//   cd web && npm test
// Node runs the TypeScript sources directly (type stripping).

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { consensus, NEAR_M } from './consensus.ts';
import { correlate, OwnTrace, Series } from './trace.ts';

/** A little crowd: each phone has a true spot, an own-source estimate and what it currently broadcasts. */
function crowd(defs) {
  return defs.map((d) => ({ ...d, fused: { ...d.own }, ah: d.anchored ? 0 : null, n: 0 }));
}

/** One round: every phone runs the consensus step on its neighbours' last broadcast. */
function round(phones, links, age = 0.5) {
  const next = phones.map((p, i) => {
    const peers = links
      .filter(([a, b]) => a === i || b === i)
      .map(([a, b]) => phones[a === i ? b : a])
      .map((q) => ({ fused: q.fused, raw: q.own, ah: q.ah, age }));
    return consensus(p.own, p.anchored, peers);
  });
  next.forEach((f, i) => {
    phones[i].fused = f.est;
    phones[i].ah = f.ah;
    phones[i].n = f.n;
  });
}

const err = (p) => Math.hypot(p.fused.x - p.true[0], p.fused.y - p.true[1]);

test('an anchored phone improves the phones pressed against it, and they improve the next', () => {
  // A row of four people 0.6 m apart. Only the first is placed exactly; the others have GPS that is 5–7 m off.
  const phones = crowd([
    { true: [10, 8], own: { x: 10, y: 8, s: 0.5 }, anchored: true },
    { true: [10.6, 8], own: { x: 16, y: 11, s: 8 }, anchored: false },
    { true: [11.2, 8], own: { x: 6, y: 3, s: 8 }, anchored: false },
    { true: [11.8, 8], own: { x: 14, y: 14, s: 8 }, anchored: false },
  ]);
  const before = phones.map(err);
  const links = [[0, 1], [1, 2], [2, 3]];
  for (let i = 0; i < 12; i++) round(phones, links);
  const after = phones.map(err);
  assert.ok(after[0] < 0.3, `the anchor moved ${after[0].toFixed(2)} m`);
  for (let i = 1; i < 4; i++) {
    assert.ok(after[i] < before[i] / 2, `phone ${i}: ${before[i].toFixed(1)} m → ${after[i].toFixed(1)} m`);
    assert.ok(after[i] < 2.5, `phone ${i} still ${after[i].toFixed(1)} m off`);
  }
  assert.deepEqual(phones.map((p) => p.ah), [0, 1, 2, 3]);
  // Accuracy degrades with every hop from the anchor, and never beats the neighbour it came from plus NEAR_M.
  for (let i = 1; i < 4; i++) {
    assert.ok(phones[i].fused.s >= phones[i - 1].fused.s + NEAR_M - 1e-9, `phone ${i} claims ±${phones[i].fused.s.toFixed(2)} m`);
    assert.ok(phones[i].fused.s < 8);
  }
});

test('no feedback loop: phones with no anchor never talk each other into certainty', () => {
  const phones = crowd([
    { true: [5, 5], own: { x: 9, y: 5, s: 8 }, anchored: false },
    { true: [5.5, 5], own: { x: 2, y: 6, s: 8 }, anchored: false },
    { true: [6, 5], own: { x: 5, y: 9, s: 8 }, anchored: false },
  ]);
  const links = [[0, 1], [1, 2], [0, 2]];
  for (let i = 0; i < 200; i++) round(phones, links);
  for (const p of phones) {
    assert.equal(p.ah, null);
    // Three independent ±8 m fixes can't be better than 8/√3 ≈ 4.6 m, however long they talk.
    assert.ok(p.fused.s > 4.5, `claims ±${p.fused.s.toFixed(2)} m after 200 rounds`);
    assert.ok(Number.isFinite(p.fused.x) && Math.abs(p.fused.x) < 50);
  }
  // … and they do agree on the average of what they each measured.
  assert.ok(Math.hypot(phones[0].fused.x - phones[1].fused.x, phones[0].fused.y - phones[1].fused.y) < 0.5);
});

test('floor: never more certain than the best neighbour plus NEAR_M', () => {
  const own = { x: 0, y: 0, s: 6 };
  const peer = (s) => ({ fused: { x: 1, y: 0, s }, raw: { x: 1, y: 0, s }, ah: 0, age: 0 });
  const f = consensus(own, false, [peer(1), peer(1), peer(1), peer(1), peer(1)]);
  assert.ok(f.est.s >= 1 + NEAR_M - 1e-9, `±${f.est.s}`);
  assert.equal(f.n, 5);
  // A phone whose own source is already better keeps its own accuracy.
  const g = consensus({ x: 0, y: 0, s: 0.5 }, true, [peer(3)]);
  assert.ok(g.est.s <= 0.5 + 1e-9);
});

test('anchored phones barely move; stale neighbours are ignored; no input, no answer', () => {
  const f = consensus({ x: 0, y: 0, s: 0.5 }, true, [{ fused: { x: 4, y: 0, s: 0.5 }, raw: { x: 4, y: 0, s: 0.5 }, ah: 0, age: 0 }]);
  assert.ok(f.est.x < 0.4, `anchor pulled ${f.est.x.toFixed(2)} m`);
  assert.equal(f.ah, 0);
  const stale = consensus({ x: 0, y: 0, s: 8 }, false, [{ fused: { x: 4, y: 0, s: 0.5 }, raw: { x: 4, y: 0, s: 0.5 }, ah: 0, age: 30 }]);
  assert.deepEqual([stale.est.x, stale.n, stale.ah], [0, 0, null]);
  // Older news counts for less.
  const at = (age) => consensus({ x: 0, y: 0, s: 3 }, false, [{ fused: { x: 4, y: 0, s: 1 }, raw: { x: 4, y: 0, s: 1 }, ah: 0, age }]).est.x;
  assert.ok(at(0) > at(3));
  assert.equal(consensus(null, false, []), null);
  // No own source at all (server unreachable, never placed): the neighbours place it.
  const alone = consensus(null, false, [{ fused: { x: 4, y: 2, s: 1 }, raw: { x: 4, y: 2, s: 1 }, ah: 0, age: 0 }]);
  assert.deepEqual([alone.est.x, alone.est.y, alone.ah], [4, 2, 1]);
  assert.ok(alone.est.s >= 1 + NEAR_M - 1e-9);
});

test('correlate finds the lag between two sway traces', () => {
  const a = new Series(), b = new Series();
  const f = (t) => Math.sin(t * 0.9) + 0.5 * Math.sin(t * 2.3 + 1); // not periodic within the window
  for (let i = 0; i < 90; i++) {
    a.set(1000 + i, f(i * 0.1));
    b.set(1000 + i, 0.6 * f(i * 0.1 - 0.3)); // b does what a did 300 ms ago, more gently
  }
  const c = correlate(a, b);
  assert.ok(c.corr > 0.9, `corr ${c.corr}`);
  assert.ok(Math.abs(c.lagMs - 300) <= 50, `lag ${c.lagMs} ms`);
  const back = correlate(b, a);
  assert.ok(Math.abs(back.lagMs + 300) <= 50, `reverse lag ${back.lagMs} ms`);
  // Two still phones say nothing.
  const s1 = new Series(), s2 = new Series();
  for (let i = 0; i < 90; i++) {
    s1.set(i, 0.001 * Math.sin(i));
    s2.set(i, 0.001 * Math.cos(i));
  }
  assert.equal(correlate(s1, s2).corr, 0);
});

test('OwnTrace levels, filters and resamples a sway however the phone is held', () => {
  const run = (down, accel) => {
    const tr = new OwnTrace();
    for (let i = 0; i < 120; i++) {
      const t = 1_700_000_000_000 + i * 100 + 37; // off the grid on purpose
      tr.add(t, accel(0.9 * (Math.sin(i * 0.31) + 0.5 * Math.sin(i * 0.73 + 1))), down, 5);
    }
    return tr.series;
  };
  // Upright (y down in the device frame... gravity along −y), swaying along x.
  const upright = run([0, -1, 0], (s) => [s, 0.3, 0]);
  // Flat on its back (gravity along −z), swaying along y, with a constant vertical offset.
  const flat = run([0, 0, -1], (s) => [0, s, 2]);
  for (const s of [upright, flat]) {
    const [, v] = s.tail(40);
    const rms = Math.sqrt(v.reduce((n, x) => n + x * x, 0) / v.length);
    assert.ok(rms > 0.35 && rms < 1, `sway rms ${rms.toFixed(2)}`);
  }
  const c = correlate(upright, flat);
  assert.ok(c.corr > 0.95 && Math.abs(c.lagMs) <= 50, `same sway, two carries: corr ${c.corr}, lag ${c.lagMs}`);
  // A handled phone (fast rotation) contributes no samples for a second.
  const tr = new OwnTrace();
  for (let i = 0; i < 30; i++) tr.add(1000 + i * 100, [Math.sin(i), 0, 0], null, i === 20 ? 400 : 5);
  assert.ok(Number.isNaN(tr.series.at(Math.floor(3500 / 100))));
  assert.ok(!Number.isNaN(tr.series.at(15)));
});
