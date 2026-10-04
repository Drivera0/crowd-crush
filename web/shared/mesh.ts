// Wire types for the phone-to-phone mesh. Mirrors server/internal/protocol/mesh.go
// (server ↔ phone, server → dashboard) and documents the messages phones send
// each other over WebRTC data channels (web/phone/src/mesh.ts); keep in sync by hand.
//
// Phones never see each other's session ids: everywhere below a phone is named
// by its handle, the first 12 hex characters of SHA-256(session id).

import type { Level } from './protocol';

// ---- phone ↔ server (over the phone WebSocket, or wrapped in a relay) ----

/** Phone → server: this browser can open WebRTC links. */
export interface RTC {
  type: 'rtc';
  on: boolean;
}

/** Server → phone: the full list of peers to link with (links to anyone else are closed). */
export interface MeshPeers {
  type: 'mesh';
  /** This phone's own handle. */
  me: string;
  /** init = this phone sends the offer. */
  peers: { id: string; init?: boolean }[];
  /** ICE server URLs; empty = host candidates only (one network). */
  ice: string[];
}

/** WebRTC signalling, relayed by the server between two phones it paired. Phone → server carries `to`, server → phone `from`. */
export interface Signal {
  type: 'sig';
  to?: string;
  from?: string;
  /** hi = "I have no link to you yet: call me" (sent by the side that does not offer, e.g. after a page reload). */
  kind: 'offer' | 'answer' | 'ice' | 'hi';
  data: unknown;
}

/** One direct link, as the phone sees it. */
export interface NearPeer {
  id: string;
  /** Peak |r| of the lagged cross-correlation of the two phones' sway traces (0 = not enough motion). */
  corr: number;
  /** Where the peak is; positive = the peer moves after this phone. */
  lagMs: number;
  hops: 1;
  /** Data-channel round trip, ms. */
  rtt?: number;
}

/** Phone → server, about once a second: its open links and proximity evidence. */
export interface Near {
  type: 'near';
  peers: NearPeer[];
  /** Peers it gave up on (no link within 8 s): the server pairs it with someone else. */
  failed?: string[];
  /** Bytes per second on its data channels (payload). */
  tx?: number;
  rx?: number;
  /** Phones in its gossip table. */
  known?: number;
}

/**
 * A message carried for a phone that has no socket of its own. Phone → server:
 * `from` (the origin's handle), `hops` (phones it passed through, 1–3) and an
 * ordinary phone → server message (a relayed hello carries no id). Server →
 * phone: `to` (the handle to pass `msg`, an ordinary server → phone message, on to).
 */
export interface Relay {
  type: 'relay';
  from?: string;
  to?: string;
  hops?: number;
  msg: unknown;
}

/**
 * The lost-signal demo. Server → phone: on = close the WebSocket and send
 * everything through the mesh, off = reconnect. Phone → server: on = "I am
 * about to do that", off = "I can't: no mesh link reaches the server".
 */
export interface Jam {
  type: 'jam';
  on: boolean;
}

/** Server → phone: the phone's clock offset (phone clock − server clock, ms), so traces on the mesh share one time base. */
export interface Clock {
  type: 'clock';
  offset: number;
}

/** Where a phone's own position estimate comes from. */
export type PosSrc = 'gps' | 'tower' | 'beacon' | 'demo' | 'manual' | 'server' | 'mesh';

/** Phone → server, about once a second: the position the phone worked out with its mesh neighbours. Never replaces the server's. */
export interface MPos {
  type?: 'mpos';
  x: number;
  y: number;
  /** 1-sigma accuracy, m. */
  acc: number;
  /** Phones between this one and the nearest anchored source (0 = anchored itself; absent = none in reach). */
  hops?: number;
  /** Neighbours that went into it. */
  n?: number;
  /** Where the phone's own starting estimate came from. */
  src?: string;
}

// ---- server → dashboard ----

export interface MeshNode {
  id: string;
  /** Session id of the phone that hands this phone's messages to the server ("" / absent = its own socket). */
  via?: string;
  hops?: number;
  /** Open links. */
  peers: number;
  /** Mean data-channel round trip, ms. */
  rtt?: number;
  /** Told (or chose) to drop its WebSocket. */
  jam?: boolean;
  tx?: number;
  rx?: number;
  known?: number;
  pos?: MPos;
}

/** The snapshot's `mesh` field. */
export interface MeshFrame {
  /** Pairs of indexes into the snapshot's nodes. */
  links: [number, number][];
  nodes: MeshNode[];
  phones: number;
  /** Phones that sent a reading in the last 2 s … */
  reporting: number;
  /** … and how many of those arrive through another phone. */
  viaMesh: number;
  /** The links are the server's stand-in for simulated phones, not real WebRTC links. */
  virtual?: boolean;
}

/** GET /api/mesh */
export interface MeshStatus {
  ice: string[];
  capable: number;
  links: number;
  pairs: number;
  jammed: string[];
  relayed: number;
  txBytes: number;
  relayIn: number;
  relayDropped: number;
}

// ---- phone ↔ phone (WebRTC data channels; compact JSON) ----
//
// Two negotiated channels per link: "u" (id 0, unordered, no retransmits) for
// beacons, gossip, warnings, pings and relayed motion; "r" (id 1, reliable) for
// every other relayed message.

/** A phone's own beacon, twice a second. */
export interface Beacon {
  k: 'b';
  id: string;
  /** Sequence number. */
  q: number;
  /** Position estimate (venue metres, mesh-corrected) and its 1-sigma accuracy; absent = no position. */
  x?: number;
  y?: number;
  a?: number;
  /** The phone's own estimate before its neighbours corrected it: [x, y, sigma]; absent = same as x, y, a. */
  o?: [number, number, number];
  /** Where that own estimate comes from. */
  src?: PosSrc;
  /** Hops from an anchored source (0 = anchored itself). */
  ah?: number;
  /** Zone level the server last told it. */
  z?: Level;
  /** Hops to the server: 0 = its own WebSocket works; absent = no route. */
  s?: number;
  /** Sway trace: grid index (server time ÷ 100 ms) of the first sample, then samples in 0.01 m/s² (null = no valid sample). */
  t?: number;
  h?: (number | null)[];
}

/** A beacon heard from someone else, passed on (no trace). hp = hops it has travelled, ttl = hops it may still travel. */
export interface GossipItem {
  id: string;
  q: number;
  x?: number;
  y?: number;
  a?: number;
  z?: Level;
  hp: number;
  ttl: number;
}

export interface Gossip {
  k: 'g';
  b: GossipItem[];
}

/** A warning: the origin's zone is yellow or red. Flooded, de-duplicated by (id, q). */
export interface Warning {
  k: 'w';
  id: string;
  q: number;
  l: 'yellow' | 'red';
  x?: number;
  y?: number;
  hp: number;
  ttl: number;
}

/** Link round-trip probe and its echo. */
export interface LinkPing {
  k: 'p' | 'q';
  t: number;
}

/** A message on its way to the server for phone f (h = hops so far), and one on its way back to phone `to`. */
export interface Up {
  k: 'up';
  f: string;
  h: number;
  m: unknown;
}
export interface Down {
  k: 'dn';
  to: string;
  m: unknown;
}

export type MeshMsg = Beacon | Gossip | Warning | LinkPing | Up | Down;

/** Beacons travel at most this many hops; relayed messages pass through at most this many phones. */
export const MESH_TTL = 3;
