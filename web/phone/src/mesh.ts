// The phone-to-phone mesh: WebRTC data channels to a few nearby phones.
//
// The server pairs phones and relays the WebRTC handshake over the phone's
// WebSocket; after that the phones talk directly. On the mesh travel:
//   - beacons (2 Hz): position estimate, zone level, hops to the server and
//     the last 2 s of this phone's sway trace;
//   - gossip (1 Hz): the freshest beacons heard from others, at most 3 hops,
//     de-duplicated by (id, sequence), so every phone knows who is around;
//   - warnings: a phone whose zone is yellow or red tells its neighbourhood,
//     so a phone that can't reach the server still warns its user;
//   - relay: a phone without a working WebSocket sends its ordinary messages
//     to a peer that has one, which hands them to the server; the server's
//     messages for it come back the same way.
// Each phone also correlates its own sway with each direct peer's and tells
// the server ("near"), and corrects its own position from the peers it
// shares motion with (consensus.ts, "mpos").
//
// Nothing here asks for a permission, and everything fails soft: with no
// links the phone simply keeps using its WebSocket.
//
// Privacy: peers see a random handle (not the session id), a position in
// the venue, a zone level and motion numbers. Nothing else.

import type { Level } from '../../shared/protocol';
import type { Beacon, GossipItem, MeshMsg, MeshPeers, MPos, Near, PosSrc, Signal } from '../../shared/mesh';
import { MESH_TTL } from '../../shared/mesh';
import { consensus, type Est, type Fused, type PeerEst } from './consensus';
import { correlate, OwnTrace, Series, GRID_MS } from './trace';

/** This phone's own-source position: where it thinks it is before its neighbours correct it. */
export interface SelfPos extends Est {
  src: PosSrc;
  /** Placed exactly, a moment ago. */
  anchored: boolean;
}

export interface MeshHost {
  /** Send a phone → server message: on the WebSocket when it works, else through the mesh (which calls relayUp). */
  send(msg: object): void;
  /** The WebSocket is open and in use. */
  wsUsable(): boolean;
  /** Write straight to the WebSocket (relay envelopes, jam replies). */
  wsSend(msg: object): void;
  /** Close the WebSocket and stay off it / connect again. */
  closeWs(): void;
  reconnectWs(): void;
  /** A server → phone message that arrived through the mesh. */
  onServerMsg(msg: unknown): void;
  /** The hello to send when going through a relay (no id). */
  hello(): object;
  self(): SelfPos | null;
  /** Zone level the server last gave this phone, '' if stale or unknown. */
  zone(): Level | '';
  /** Phone clock − server clock (ms), 0 until known. */
  clockOffset(): number;
  /** Something the screen shows has changed. */
  onChange(): void;
}

const BEACON_MS = 500;
const SLOW_MS = 1000; // gossip, near, consensus, warnings, housekeeping
const PING_MS = 2000;
const CONNECT_TIMEOUT_MS = 8000;
const SILENT_MS = 6000; // an open link with no beacon this long is dead
const WAIT_OFFER_MS = 16_000; // how long to wait for a peer that is meant to call us (it may first have to notice its old link died)
const RETRY_GAP_MS = 30_000; // a link that died is retried at most this often before the pair is reported as failed
const TRACE_SAMPLES = 20; // 2 s of trace per beacon
const GOSSIP_MAX = 8;
const TABLE_MAX = 64;
const TABLE_KEEP_MS = 10_000;
const WARN_KEEP_MS = 5000;
const WARN_NEAR_M = 8;
const HELLO_EVERY_MS = 5000;
const JAM_FAILSAFE_MS = 10_000;
/** A peer counts as "standing with me" this long after its sway last matched mine. */
const CONFIRM_KEEP_MS = 10_000;
const CONFIRM_CORR = 0.6;

interface PeerInfo {
  at: number;
  fused?: Est;
  raw?: Est;
  ah: number | null;
  z?: Level;
  /** Its hops to the server. */
  s?: number;
}

interface Link {
  h: string;
  pc: RTCPeerConnection;
  u: RTCDataChannel;
  r: RTCDataChannel;
  init: boolean;
  startedAt: number;
  open: boolean;
  openedAt: number;
  remoteSet: boolean;
  ice: RTCIceCandidateInit[];
  rtt: number;
  info: PeerInfo | null;
  series: Series;
  corr: number;
  lagMs: number;
  confAt: number;
  dead: boolean;
}

interface Entry {
  q: number;
  x?: number;
  y?: number;
  a?: number;
  z?: Level;
  hops: number;
  at: number;
  via: string;
  /** Hops it may still travel; 0 = not to be passed on (or already was). */
  ttl: number;
}

export interface MeshStatus {
  /** Open links. */
  links: number;
  /** Phones known (direct or by gossip). */
  known: number;
  /** This phone's messages are going through another phone. */
  relaying: boolean;
  /** No WebSocket and no phone in reach that has one. */
  stranded: boolean;
  jammed: boolean;
  /** A neighbour's warning that applies here. */
  warn: 'yellow' | 'red' | null;
  /** Messages this phone has handed to the server for others. */
  carried: number;
  tx: number;
  rx: number;
  /** Mesh-corrected position, when neighbours went into it. */
  pos: Fused | null;
}

export class Mesh {
  readonly supported = typeof RTCPeerConnection === 'function';
  me = '';
  private iceUrls: string[] = [];
  private links = new Map<string, Link>();
  private wanted = new Map<string, { init: boolean; since: number }>();
  private failed = new Set<string>();
  private gaveUp = new Map<string, number>();
  private retried = new Map<string, number>();
  private hiAt = new Map<string, number>();
  private table = new Map<string, Entry>();
  private warned = new Map<string, { l: 'yellow' | 'red'; at: number; hops: number; x?: number; y?: number }>();
  private seenWarn = new Map<string, number>();
  private routeBack = new Map<string, { h: string; at: number }>();
  private trace = new OwnTrace();
  private q = Math.floor(Date.now() / 100) % 1_000_000_000;
  private wq = 0;
  private timer = 0;
  private lastBeacon = 0;
  private lastSlow = 0;
  private lastPing = 0;
  private txBytes = 0;
  private rxBytes = 0;
  private tx = 0;
  private rx = 0;
  private carried = 0;
  private helloAt = 0;
  private helloVia = '';
  private noRouteSince = 0;
  private relayedAt = 0;
  private fused: Fused | null = null;
  private stopped = false;
  jammed = false;
  /** The jam was this phone's own "Simulate lost signal" switch. */
  jamLocal = false;

  constructor(private host: MeshHost) {}

  /** Call once the phone is live. Tells the server this browser can link up. */
  start() {
    if (!this.supported || this.timer) return;
    this.timer = window.setInterval(() => this.tick(), 250);
    this.announce();
  }

  /** Tell the server (again) that this phone can open links: after every (re)connect. */
  announce() {
    if (this.supported && !this.stopped) this.host.send({ type: 'rtc', on: true });
  }

  stop() {
    this.stopped = true;
    window.clearInterval(this.timer);
    for (const l of [...this.links.values()]) this.drop(l, false);
  }

  // ---- motion ----

  /** One 100 ms motion summary (phone clock), for this phone's sway trace. */
  addMotion(t: number, a: [number, number, number], down: [number, number, number] | null, rot: number) {
    this.trace.add(t - this.host.clockOffset(), a, down, rot);
  }

  // ---- messages from the server (WebSocket or relayed) ----

  /** Handles the mesh message types; false = not one of them. */
  onServer(msg: { type: string }): boolean {
    switch (msg.type) {
      case 'mesh':
        this.onPeers(msg as MeshPeers);
        return true;
      case 'sig':
        void this.onSig(msg as Signal);
        return true;
      case 'jam':
        this.setJam((msg as unknown as { on: boolean }).on, false);
        return true;
      case 'relay': {
        // The server's answer for a phone this one carries messages for.
        const env = msg as unknown as { to?: string; msg?: unknown };
        if (env.to && env.msg) this.down(env.to, env.msg);
        return true;
      }
    }
    return false;
  }

  private onPeers(m: MeshPeers) {
    if (!this.supported || this.stopped) return;
    const now = Date.now();
    this.me = m.me;
    this.iceUrls = m.ice ?? [];
    const next = new Map<string, { init: boolean; since: number }>();
    for (const p of m.peers.slice(0, 8)) next.set(p.id, { init: !!p.init, since: this.wanted.get(p.id)?.since ?? now });
    this.wanted = next;
    for (const l of [...this.links.values()]) if (!next.has(l.h)) this.drop(l, false);
    for (const [h, w] of next) {
      if (this.links.has(h)) continue;
      if (now - (this.gaveUp.get(h) ?? 0) < 20_000) continue;
      if (w.init) void this.offer(h);
      else if (now - (this.hiAt.get(h) ?? 0) > 5000) {
        // The peer is the one that calls. Tell it we are here (and new, if this page was just reloaded):
        // it may still be holding a link to our previous page.
        this.hiAt.set(h, now);
        this.host.send({ type: 'sig', to: h, kind: 'hi', data: {} });
      }
    }
    this.host.onChange();
  }

  // ---- links ----

  private create(h: string, init: boolean): Link {
    const pc = new RTCPeerConnection({ iceServers: this.iceUrls.length ? [{ urls: this.iceUrls }] : [] });
    // Negotiated channels: both sides create them, no in-band handshake.
    const u = pc.createDataChannel('u', { negotiated: true, id: 0, ordered: false, maxRetransmits: 0 });
    const r = pc.createDataChannel('r', { negotiated: true, id: 1 });
    const now = Date.now();
    const l: Link = {
      h, pc, u, r, init, startedAt: now, open: false, openedAt: 0, remoteSet: false, ice: [],
      rtt: 0, info: null, series: new Series(), corr: 0, lagMs: 0, confAt: 0, dead: false,
    };
    this.links.set(h, l);
    pc.onicecandidate = (e) => {
      if (e.candidate) this.host.send({ type: 'sig', to: h, kind: 'ice', data: e.candidate.toJSON() });
    };
    pc.onconnectionstatechange = () => {
      if (pc.connectionState === 'failed' || pc.connectionState === 'closed') this.drop(l, true);
    };
    u.onopen = () => {
      if (l.dead) return;
      l.open = true;
      l.openedAt = Date.now();
      this.beacon(); // say hello on the new link straight away
      this.host.onChange();
    };
    u.onclose = () => this.drop(l, true);
    u.onmessage = r.onmessage = (e) => this.onData(l, e.data as string);
    return l;
  }

  private async offer(h: string) {
    const l = this.create(h, true);
    try {
      await l.pc.setLocalDescription(await l.pc.createOffer());
      if (!l.dead) this.host.send({ type: 'sig', to: h, kind: 'offer', data: l.pc.localDescription!.toJSON() });
    } catch {
      this.drop(l, true);
    }
  }

  private async onSig(s: Signal) {
    if (!this.supported || this.stopped || !s.from) return;
    const h = s.from;
    let l = this.links.get(h);
    if (s.kind === 'hi') {
      // The peer has no link to us and we are the one that calls. Unless our link to it is alive or only
      // just started, start over.
      const w = this.wanted.get(h);
      if (!w?.init) return;
      const now = Date.now();
      if (l && (l.open ? now - (l.info?.at ?? l.openedAt) < 1500 : now - l.startedAt < 3000)) return;
      if (l) this.drop(l, false);
      void this.offer(h);
      return;
    }
    try {
      if (s.kind === 'offer') {
        if (l) this.drop(l, false); // the peer started over (reloaded its page)
        l = this.create(h, false);
        await l.pc.setRemoteDescription(s.data as RTCSessionDescriptionInit);
        l.remoteSet = true;
        for (const c of l.ice.splice(0)) await l.pc.addIceCandidate(c);
        await l.pc.setLocalDescription(await l.pc.createAnswer());
        if (!l.dead) this.host.send({ type: 'sig', to: h, kind: 'answer', data: l.pc.localDescription!.toJSON() });
      } else if (!l) {
        return;
      } else if (s.kind === 'answer') {
        if (!l.init || l.remoteSet) return;
        await l.pc.setRemoteDescription(s.data as RTCSessionDescriptionInit);
        l.remoteSet = true;
        for (const c of l.ice.splice(0)) await l.pc.addIceCandidate(c);
      } else if (s.kind === 'ice') {
        if (l.remoteSet) await l.pc.addIceCandidate(s.data as RTCIceCandidateInit);
        else if (l.ice.length < 32) l.ice.push(s.data as RTCIceCandidateInit);
      }
    } catch {
      if (l) this.drop(l, true);
    }
  }

  /** Close a link. failed = it broke (or never came up): tell the server so it pairs this phone with someone else. */
  private drop(l: Link, failed: boolean) {
    if (l.dead) return;
    l.dead = true;
    if (this.links.get(l.h) === l) this.links.delete(l.h);
    const w = this.wanted.get(l.h);
    if (failed && w && !this.stopped) {
      const now = Date.now();
      if (l.openedAt && now - (this.retried.get(l.h) ?? 0) > RETRY_GAP_MS) {
        // A link that worked and then died: most likely the peer reloaded its page. Call again (or wait to be
        // called) before telling the server the pair is no good.
        this.retried.set(l.h, now);
        w.since = now;
        if (w.init) window.setTimeout(() => !this.stopped && this.wanted.get(l.h)?.init && !this.links.has(l.h) && void this.offer(l.h), 1500);
      } else {
        this.failed.add(l.h);
        this.gaveUp.set(l.h, now);
      }
    }
    l.u.onclose = null;
    l.pc.onconnectionstatechange = null;
    try {
      l.pc.close();
    } catch {
      /* already closed */
    }
    this.host.onChange();
  }

  private openLinks(): Link[] {
    const out: Link[] = [];
    for (const l of this.links.values()) if (l.open && !l.dead) out.push(l);
    return out;
  }

  private sendTo(l: Link, msg: MeshMsg, reliable = false) {
    const ch = reliable ? l.r : l.u;
    if (ch.readyState !== 'open') return false;
    if (!reliable && ch.bufferedAmount > 64_000) return false; // a clogged link: drop rather than queue
    const s = JSON.stringify(msg);
    try {
      ch.send(s);
    } catch {
      return false;
    }
    this.txBytes += s.length;
    return true;
  }

  // ---- data from a peer ----

  private onData(l: Link, data: string) {
    if (typeof data !== 'string' || data.length > 8192 || l.dead) return;
    this.rxBytes += data.length;
    let m: MeshMsg;
    try {
      m = JSON.parse(data) as MeshMsg;
    } catch {
      return;
    }
    const now = Date.now();
    switch (m.k) {
      case 'b':
        this.onBeacon(l, m, now);
        break;
      case 'g':
        if (Array.isArray(m.b)) for (const it of m.b.slice(0, 16)) this.learn(it.id, it, it.hp, it.ttl, l.h, now);
        break;
      case 'w': {
        const key = `${m.id}:${m.q}`;
        if (m.id === this.me || this.seenWarn.has(key) || (m.l !== 'yellow' && m.l !== 'red')) break;
        this.seenWarn.set(key, now);
        this.warned.set(m.id, { l: m.l, at: now, hops: m.hp, x: m.x, y: m.y });
        if (m.ttl > 0 && m.hp < MESH_TTL) {
          for (const o of this.openLinks()) if (o !== l) this.sendTo(o, { ...m, hp: m.hp + 1, ttl: m.ttl - 1 });
        }
        this.host.onChange();
        break;
      }
      case 'p':
        this.sendTo(l, { k: 'q', t: m.t });
        break;
      case 'q':
        if (typeof m.t === 'number') {
          const rtt = Math.max(0, now - m.t);
          l.rtt = l.rtt ? l.rtt + 0.3 * (rtt - l.rtt) : rtt;
        }
        break;
      case 'up':
        this.carry(l, m.f, m.h, m.m, now);
        break;
      case 'dn':
        if (m.to === this.me) this.host.onServerMsg(m.m);
        else this.down(m.to, m.m);
        break;
    }
  }

  private onBeacon(l: Link, b: Beacon, now: number) {
    if (b.id !== l.h) return; // a beacon is only ever its sender's own
    const num = (v: unknown): v is number => typeof v === 'number' && Number.isFinite(v);
    const info: PeerInfo = { at: now, ah: num(b.ah) ? b.ah : null, z: b.z, s: num(b.s) ? b.s : undefined };
    if (num(b.x) && num(b.y) && num(b.a) && b.a > 0) {
      info.fused = { x: b.x, y: b.y, s: b.a };
      info.raw = Array.isArray(b.o) && b.o.length === 3 && b.o.every(num) && b.o[2] > 0 ? { x: b.o[0], y: b.o[1], s: b.o[2] } : info.fused;
    }
    l.info = info;
    if (num(b.t) && Array.isArray(b.h) && b.h.length <= 64) {
      b.h.forEach((v, i) => l.series.set(b.t! + i, num(v) ? v / 100 : NaN));
    }
    this.learn(b.id, b, 1, MESH_TTL - 1, l.h, now);
  }

  /** Put a beacon (direct or passed on) in the table if it is news. */
  private learn(id: string, f: { q: number; x?: number; y?: number; a?: number; z?: Level }, hops: number, ttl: number, via: string, now: number) {
    if (typeof id !== 'string' || id.length !== 12 || id === this.me || !(hops >= 1 && hops <= MESH_TTL) || typeof f.q !== 'number') return;
    const cur = this.table.get(id);
    if (cur && now - cur.at < TABLE_KEEP_MS / 2 && !(f.q > cur.q || (f.q === cur.q && hops < cur.hops))) return;
    this.table.set(id, { q: f.q, x: f.x, y: f.y, a: f.a, z: f.z, hops, at: now, via, ttl: hops < MESH_TTL ? Math.max(0, Math.min(ttl, MESH_TTL - hops)) : 0 });
  }

  // ---- relay ----

  /** The peer to hand a message for the server to: the one nearest the server. */
  private uplink(maxS = MESH_TTL - 1, not?: Link): Link | null {
    let best: Link | null = null;
    for (const l of this.openLinks()) {
      const s = l.info?.s;
      if (l === not || s === undefined || s > maxS || Date.now() - l.info!.at > SILENT_MS) continue;
      if (!best || s < best.info!.s! || (s === best.info!.s && l.rtt < best.rtt)) best = l;
    }
    return best;
  }

  /** This phone's hops to the server: 0 with a working WebSocket, undefined with no route. */
  private serverHops(): number | undefined {
    if (this.host.wsUsable()) return 0;
    const up = this.uplink(MESH_TTL - 2);
    return up ? up.info!.s! + 1 : undefined;
  }

  /** Send one of this phone's own messages to the server through a peer. false = nobody in reach. */
  relayUp(msg: object): boolean {
    if (!this.me || this.stopped) return false;
    let up = this.uplink();
    if (!up) return false;
    const now = Date.now();
    // Stay with the peer already carrying for us while it is as good as any (no flapping between two equals).
    const cur = this.links.get(this.helloVia);
    if (cur && cur !== up && cur.open && !cur.dead && cur.info?.s !== undefined && cur.info.s <= up.info!.s! && now - cur.info.at < SILENT_MS) up = cur;
    // A relayed phone introduces itself first (and again now and then, and on every new route).
    if (up.h !== this.helloVia || now - this.helloAt > HELLO_EVERY_MS) {
      this.helloVia = up.h;
      this.helloAt = now;
      this.sendTo(up, { k: 'up', f: this.me, h: 1, m: this.host.hello() }, true);
    }
    this.relayedAt = now;
    return this.sendTo(up, { k: 'up', f: this.me, h: 1, m: msg }, (msg as { type?: string }).type !== 'm');
  }

  /** A message from phone f on its way to the server arrived on link l. */
  private carry(l: Link, f: string, h: number, m: unknown, now: number) {
    if (typeof f !== 'string' || f.length !== 12 || f === this.me || !(h >= 1 && h <= MESH_TTL) || !m || typeof m !== 'object') return;
    this.routeBack.set(f, { h: l.h, at: now });
    if (this.host.wsUsable()) {
      this.host.wsSend({ type: 'relay', from: f, hops: h, msg: m });
      this.carried++;
      return;
    }
    if (h >= MESH_TTL) return;
    // No server here either: pass it to a peer that is strictly nearer one (never back).
    const mine = this.serverHops();
    const up = mine === undefined ? null : this.uplink(mine - 1, l);
    if (up) this.sendTo(up, { k: 'up', f, h: h + 1, m }, (m as { type?: string }).type !== 'm');
  }

  /** A server message for phone `to`: send it back the way that phone's messages came. */
  private down(to: string, m: unknown) {
    const rb = this.routeBack.get(to);
    const l = rb && this.links.get(rb.h);
    if (l) this.sendTo(l, { k: 'dn', to, m }, true);
  }

  // ---- the lost-signal demo ----

  /**
   * on: drop the WebSocket and send everything through the mesh (refused,
   * returning false, when no peer can reach the server). off: reconnect.
   * local = this phone's own switch (it stays off the WebSocket until
   * switched back); a jam ordered by the server undoes itself when the mesh
   * route is lost for JAM_FAILSAFE_MS.
   */
  setJam(on: boolean, local: boolean): boolean {
    if (on) {
      if (this.jammed) return true;
      if (!this.uplink()) {
        if (!local) this.host.wsSend({ type: 'jam', on: false });
        return false;
      }
      this.host.wsSend({ type: 'jam', on: true }); // "I am leaving this socket; expect me through a neighbour"
      this.jammed = true;
      this.jamLocal = local;
      this.helloAt = 0;
      this.noRouteSince = 0;
      this.host.closeWs();
    } else {
      if (!this.jammed) return true;
      this.jammed = false;
      this.jamLocal = false;
      this.host.reconnectWs();
    }
    this.host.onChange();
    return true;
  }

  // ---- periodic work ----

  private tick() {
    const now = Date.now();
    if (now - this.lastBeacon >= BEACON_MS) {
      this.lastBeacon = now;
      this.beacon();
    }
    if (now - this.lastPing >= PING_MS) {
      this.lastPing = now;
      for (const l of this.openLinks()) this.sendTo(l, { k: 'p', t: now });
    }
    if (now - this.lastSlow >= SLOW_MS) {
      const dt = this.lastSlow ? (now - this.lastSlow) / 1000 : 1;
      this.lastSlow = now;
      this.tx = Math.round(this.txBytes / dt);
      this.rx = Math.round(this.rxBytes / dt);
      this.txBytes = this.rxBytes = 0;
      this.housekeeping(now);
      this.gossip(now);
      this.warn(now);
      this.report(now);
      this.locate(now);
      this.host.onChange();
    }
  }

  private beacon() {
    const links = this.openLinks();
    if (!links.length || !this.me) return;
    const own = this.host.self();
    const b: Beacon = { k: 'b', id: this.me, q: ++this.q };
    const r1 = (v: number) => Math.round(v * 10) / 10;
    const pos = this.fused && this.fused.n > 0 ? this.fused.est : own;
    if (pos) {
      b.x = r1(pos.x);
      b.y = r1(pos.y);
      b.a = r1(Math.max(0.1, pos.s));
      if (own && pos !== own) b.o = [r1(own.x), r1(own.y), r1(Math.max(0.1, own.s))];
      b.src = own?.src ?? 'mesh';
      const ah = this.fused ? this.fused.ah : own?.anchored ? 0 : null;
      if (ah !== null) b.ah = ah;
    }
    const z = this.host.zone();
    if (z) b.z = z;
    const s = this.serverHops();
    if (s !== undefined) b.s = s;
    const [t, h] = this.trace.series.tail(TRACE_SAMPLES);
    if (h.length) {
      b.t = t;
      b.h = h.map((v) => (Number.isNaN(v) ? null : Math.max(-9999, Math.min(9999, Math.round(v * 100)))));
    }
    for (const l of links) this.sendTo(l, b);
  }

  private housekeeping(now: number) {
    for (const l of [...this.links.values()]) {
      if (!l.open && now - l.startedAt > CONNECT_TIMEOUT_MS) this.drop(l, true); // couldn't connect: give up quietly
      else if (l.open && now - Math.max(l.openedAt, l.info?.at ?? 0) > SILENT_MS) this.drop(l, true);
    }
    // A peer that was meant to call us and never did.
    for (const [h, w] of this.wanted) {
      if (!w.init && !this.links.has(h) && now - w.since > WAIT_OFFER_MS && now - (this.gaveUp.get(h) ?? 0) > 20_000) {
        this.failed.add(h);
        this.gaveUp.set(h, now);
      }
    }
    for (const [id, e] of this.table) if (now - e.at > TABLE_KEEP_MS) this.table.delete(id);
    if (this.table.size > TABLE_MAX) {
      const old = [...this.table.entries()].sort((a, b) => a[1].at - b[1].at).slice(0, this.table.size - TABLE_MAX);
      for (const [id] of old) this.table.delete(id);
    }
    for (const [k, at] of this.seenWarn) if (now - at > TABLE_KEEP_MS) this.seenWarn.delete(k);
    for (const [id, w] of this.warned) if (now - w.at > WARN_KEEP_MS) this.warned.delete(id);
    for (const [id, r] of this.routeBack) if (now - r.at > 30_000) this.routeBack.delete(id);
    // Jammed with no way to the server: a jam the server ordered undoes itself.
    if (this.jammed && !this.uplink()) {
      this.noRouteSince ||= now;
      if (!this.jamLocal && now - this.noRouteSince > JAM_FAILSAFE_MS) this.setJam(false, false);
    } else {
      this.noRouteSince = 0;
    }
  }

  /** Pass on the freshest beacons heard from others. */
  private gossip(now: number) {
    const links = this.openLinks();
    if (links.length < 2) return; // nobody to pass anything on to
    const fresh = [...this.table.entries()].filter(([, e]) => e.ttl > 0 && now - e.at < 3000).sort((a, b) => b[1].at - a[1].at).slice(0, GOSSIP_MAX);
    if (!fresh.length) return;
    for (const l of links) {
      const items: GossipItem[] = [];
      for (const [id, e] of fresh) {
        if (e.via === l.h || id === l.h) continue; // not back where it came from, not to its own origin
        items.push({ id, q: e.q, x: e.x, y: e.y, a: e.a, z: e.z, hp: e.hops + 1, ttl: e.ttl - 1 });
      }
      if (items.length) this.sendTo(l, { k: 'g', b: items });
    }
    for (const [, e] of fresh) e.ttl = 0;
  }

  /** While this phone's zone is yellow or red, tell the neighbourhood. */
  private warn(now: number) {
    const z = this.host.zone();
    if (z !== 'yellow' && z !== 'red') return;
    const own = this.host.self();
    const key = `${this.me}:${++this.wq}`;
    this.seenWarn.set(key, now);
    for (const l of this.openLinks()) {
      this.sendTo(l, { k: 'w', id: this.me, q: this.wq, l: z, x: own ? Math.round(own.x * 10) / 10 : undefined, y: own ? Math.round(own.y * 10) / 10 : undefined, hp: 1, ttl: MESH_TTL - 1 });
    }
  }

  /** Correlate with each direct peer and tell the server. */
  private report(now: number) {
    const links = this.openLinks();
    const failed = [...this.failed];
    this.failed.clear();
    if (!links.length && !failed.length) return;
    const near: Near = { type: 'near', peers: [] };
    for (const l of links) {
      const c = correlate(this.trace.series, l.series);
      l.corr = c.corr;
      l.lagMs = c.lagMs;
      if (c.corr >= CONFIRM_CORR) l.confAt = now;
      near.peers.push({ id: l.h, corr: c.corr, lagMs: c.lagMs, hops: 1, rtt: Math.round(l.rtt) });
    }
    if (failed.length) near.failed = failed;
    near.tx = this.tx;
    near.rx = this.rx;
    near.known = this.table.size;
    this.host.send(near);
  }

  /** The consensus step: correct this phone's position from the peers it shares motion with. */
  private locate(now: number) {
    const own = this.host.self();
    const peers: PeerEst[] = [];
    for (const l of this.openLinks()) {
      const i = l.info;
      if (!i?.fused || !i.raw || now - l.confAt > CONFIRM_KEEP_MS) continue;
      peers.push({ fused: i.fused, raw: i.raw, ah: i.ah, age: (now - i.at) / 1000 });
    }
    const f = consensus(own, !!own?.anchored, peers);
    if (!f) {
      this.fused = null;
      return;
    }
    // Ease toward the new estimate so the dot doesn't jitter.
    const prev = this.fused?.est;
    if (prev && f.n > 0 && Math.hypot(prev.x - f.est.x, prev.y - f.est.y) < 5) {
      f.est.x = prev.x + 0.5 * (f.est.x - prev.x);
      f.est.y = prev.y + 0.5 * (f.est.y - prev.y);
    }
    this.fused = f;
    if (f.n === 0) return; // nothing a neighbour added: the server already has this
    const mp: MPos = { type: 'mpos', x: Math.round(f.est.x * 100) / 100, y: Math.round(f.est.y * 100) / 100, acc: Math.round(f.est.s * 10) / 10, n: f.n, src: own?.src ?? 'mesh' };
    if (f.ah !== null) mp.hops = f.ah;
    this.host.send(mp);
  }

  // ---- for the screen ----

  /** The neighbours' warning that applies to this phone: the worst fresh one from close by. */
  private neighbourWarning(now: number): 'yellow' | 'red' | null {
    const own = this.fused?.est ?? this.host.self();
    let worst: 'yellow' | 'red' | null = null;
    for (const w of this.warned.values()) {
      if (now - w.at > WARN_KEEP_MS) continue;
      const near = own && w.x !== undefined && w.y !== undefined ? Math.hypot(own.x - w.x, own.y - w.y) <= WARN_NEAR_M : w.hops <= 2;
      if (near && (w.l === 'red' || !worst)) worst = w.l;
    }
    return worst;
  }

  status(): MeshStatus {
    const now = Date.now();
    const ws = this.host.wsUsable();
    const route = !!this.uplink();
    return {
      links: this.openLinks().length,
      known: this.table.size,
      relaying: !ws && route && now - this.relayedAt < 3000,
      stranded: !ws && !route,
      jammed: this.jammed,
      warn: this.neighbourWarning(now),
      carried: this.carried,
      tx: this.tx,
      rx: this.rx,
      pos: this.fused && this.fused.n > 0 ? this.fused : null,
    };
  }

  /** Everything, for debugging in the console (window.pulseMesh.debug()). */
  debug() {
    return {
      me: this.me,
      status: this.status(),
      serverHops: this.serverHops(),
      links: [...this.links.values()].map((l) => ({
        h: l.h, open: l.open, init: l.init, rtt: Math.round(l.rtt), corr: l.corr, lagMs: l.lagMs,
        confirmed: Date.now() - l.confAt < CONFIRM_KEEP_MS, setupMs: l.openedAt ? l.openedAt - l.startedAt : null, info: l.info,
        state: l.pc.connectionState,
      })),
      table: [...this.table.entries()].map(([id, e]) => ({ id, hops: e.hops, x: e.x, y: e.y, z: e.z, age: Date.now() - e.at })),
      own: this.host.self(),
      fused: this.fused,
      traceLen: this.trace.series.v.length,
      gridNow: Math.floor((Date.now() - this.host.clockOffset()) / GRID_MS),
    };
  }
}
