// TS mirror of server/internal/protocol/protocol.go — keep in sync by hand.

export type NodeStatus = 'connecting' | 'ok' | 'handling' | 'swaying' | 'wave' | 'stale';
export type Level = 'calm' | 'yellow' | 'red';

// ---- Phone → server ----

export interface Hello {
  type: 'hello';
  id: string;
  row: number;
  col: number;
  ua?: string;
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
  row: number;
  col: number;
  status: NodeStatus;
  sway: number;
  rtt: number;
  offset: number;
  age: number;
  ua?: string;
}

export interface Zone {
  id: string;
  level: Level;
  score: number;
  row0: number;
  col0: number;
  row1: number; // inclusive
  col1: number; // inclusive
}

/** A travelling wave edge, always in the direction of travel. */
export interface Wave {
  from: string;
  to: string;
  lagMs: number;
  corr: number;
}

export interface Stats {
  phones: number;
  msgPerSec: number;
  medianRtt: number;
}

export interface Snapshot {
  type: 'snapshot';
  t: number;
  mode: 'live' | 'replay';
  replay?: string;
  progress?: number;
  recording?: string;
  rows: number;
  cols: number;
  nodes: Node[];
  zones: Zone[];
  waves: Wave[];
  stats: Stats;
}

export interface Alert {
  type: 'alert';
  t: number;
  zone: string;
  level: Level;
  score: number;
  brief?: string;
  audioUrl?: string;
  test?: boolean;
}

export interface Alerts {
  type: 'alerts';
  alerts: Alert[];
}

export type ToDash = Snapshot | Alert | Alerts;

/** GET /api/config */
export interface Config {
  rows: number;
  cols: number;
  yellow: number; // zone score thresholds
  red: number;
}

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
  row: number;
  col: number;
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
