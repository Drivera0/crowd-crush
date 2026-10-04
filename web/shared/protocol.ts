// TS mirror of server/internal/protocol/protocol.go — keep in sync by hand.
//
// Positions are venue-relative metres: origin at the top-left of the venue map,
// x to the right, y down. Phones may send GPS; the server converts it to venue
// metres on arrival and never stores or forwards the raw coordinates.

export type NodeStatus = 'connecting' | 'ok' | 'handling' | 'swaying' | 'wave' | 'stale';
export type Level = 'calm' | 'yellow' | 'red';
export type Point = [number, number];

// ---- Phone → server ----

/** Join. Give a position as x/y (placed on the map) or lat/lon/acc (GPS). row/col is legacy. */
export interface Hello {
  type: 'hello';
  id: string;
  x?: number;
  y?: number;
  lat?: number;
  lon?: number;
  acc?: number;
  row?: number;
  col?: number;
  ua?: string;
}

/** The phone was placed or moved on the venue map (metres). */
export interface Pos {
  type: 'pos';
  x: number;
  y: number;
}

/** A GPS fix; acc is the accuracy radius in metres. Sent ~1/s or after moving > 1 m. */
export interface Gps {
  type: 'gps';
  lat: number;
  lon: number;
  acc: number;
}

/** Reply to a ping. t1 = phone clock when it answered. */
export interface Pong {
  type: 'pong';
  t0: number;
  t1: number;
}

/** 100 ms summary of ~6 raw samples: mean accel per axis (m/s², no gravity), max rotation rate (deg/s). */
export interface Motion {
  type: 'm';
  t: number;
  ax: number;
  ay: number;
  az: number;
  rot: number;
}

export type FromPhone = Hello | Pos | Gps | Pong | Motion;

// ---- Server → phone ----

export interface Ping {
  type: 'ping';
  t0: number;
}

export interface PhoneState {
  type: 'state';
  node: NodeStatus;
  zone: Level | '';
}

export type ToPhone = Ping | PhoneState;

// ---- Server → dashboard ----

export interface Node {
  id: string;
  x: number;
  y: number;
  /** GPS accuracy radius in metres; 0 = placed by hand. */
  acc?: number;
  src?: 'gps' | 'manual';
  /** Outside the venue rectangle: counts toward nothing. */
  outside?: boolean;
  status: NodeStatus;
  sway: number;
  rtt: number;
  offset: number;
  age: number;
  ua?: string;
  zone?: string;
}

export interface Zone {
  id: string;
  name: string;
  level: Level;
  score: number;
  poly: Point[];
  /** Drawn by staff on the dashboard (false = default split or "rest of venue"). */
  custom: boolean;
  sens: 'normal' | 'high';
}

/** A travelling wave edge, always in the direction of travel. */
export interface Wave {
  from: string;
  to: string;
  lagMs: number;
  corr: number;
}

/** A group of phones packed together. */
export interface Cluster {
  id: string;
  x: number;
  y: number;
  r: number;
  count: number;
  /** Estimated people (count ÷ participation). */
  people?: number;
  /** Estimated people per m². */
  density: number;
  level?: Level;
  trend: 'forming' | 'steady' | 'dispersing';
}

export interface Stats {
  phones: number;
  msgPerSec: number;
  medianRtt: number;
}

/** Simulated people (only in mode "sim"): [x, y, pressure N/m, hasPhone 0|1]. */
export interface SimFrame {
  bodies: [number, number, number, number][];
  t: number;
  action: string;
}

export interface Snapshot {
  type: 'snapshot';
  t: number;
  mode: 'live' | 'replay' | 'sim';
  sim?: SimFrame;
  replay?: string;
  progress?: number;
  recording?: string;
  venue: { w: number; h: number };
  nodes: Node[];
  zones: Zone[];
  waves: Wave[];
  /** Every neighbour pair the detector compares. */
  links: [string, string][];
  clusters: Cluster[];
  stats: Stats;
}

export interface Alert {
  type: 'alert';
  t: number;
  zone: string;
  level: Level;
  score: number;
  kind?: 'wave' | 'density';
  brief?: string;
  audioUrl?: string;
  test?: boolean;
}

export interface Alerts {
  type: 'alerts';
  alerts: Alert[];
}

export type ToDash = Snapshot | Alert | Alerts;

// ---- HTTP ----

/** GET /api/config */
export interface Config {
  venueW: number;
  venueH: number;
  geo: boolean;
  yellow: number; // zone score thresholds
  red: number;
  neighbourRadius: number;
}

/** GET/PUT /api/venue. lat/lon is the map's top-left corner; bearing = degrees clockwise from north of the map's "up". */
export interface Venue {
  w: number;
  h: number;
  lat?: number;
  lon?: number;
  bearing?: number;
  geo: boolean;
}

/** GET/PUT /api/areas: watch areas drawn by staff; each becomes a server zone. */
export interface Area {
  id: string;
  name: string;
  sens: 'normal' | 'high';
  poly: Point[];
  /** Zone light that shows this area (its letter in SIGN_URL); absent = none. */
  light?: string;
}

/** GET /api/hardware: every sign and zone light, probed every 5 s. */
export interface Hardware {
  name: string;
  kind: string;
  url: string;
  zone?: string;
  online: boolean;
  lastSeen?: number;
  error?: string;
  rssi?: number;
  uptime?: number;
  level?: string;
  ble?: { devices: number; near: number; scans: number; age: number };
  areas?: string[];
}

/** GET /api/sim: the in-process crowd simulation (Social Force Model). */
export interface SimExit {
  id: string;
  name: string;
  x0: number;
  y0: number;
  x1: number;
  y1: number;
  open: boolean;
}

export interface SimState {
  running: boolean;
  t?: number;
  people?: number;
  phones?: number;
  participation?: number;
  action?: string;
  exits?: SimExit[];
  walls?: [number, number, number, number][];
  truth?: {
    maxDensity: number;
    maxPressure: number;
    crushing: number;
    /** Seconds since start when the simulated crowd first became dangerous. */
    dangerAt?: number | null;
    /** Seconds since start of Pulse's first red alert. */
    alertAt?: number | null;
    /** dangerAt − alertAt: positive = Pulse warned first. */
    leadSeconds?: number | null;
  };
}

export type SimAction =
  | { type: 'calm' | 'stage' | 'disperse' }
  | { type: 'surge'; strength: number }
  | { type: 'attract'; x: number; y: number }
  | { type: 'shove'; x: number; y: number; dx: number; dy: number }
  | { type: 'exit'; id: string; open: boolean }
  | { type: 'spawn'; x: number; y: number; n: number };

/** One 100 ms motion summary as the server received it (phone clock). */
export interface Sample {
  t: number;
  ax: number;
  ay: number;
  az: number;
  rot: number;
}

/** GET /api/node/{id} */
export interface NodeDetail {
  id: string;
  x: number;
  y: number;
  ua: string;
  zone: string;
  connected: boolean;
  synced: boolean;
  rtt: number;
  offset: number;
  joinedAt: number;
  messages: number;
  samples: Sample[];
}

/** WebSocket URL on the same host as the page. */
export function wsURL(path: string): string {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${proto}//${location.host}${path}`;
}
