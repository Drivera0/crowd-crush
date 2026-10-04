// TS mirror of server/internal/protocol/protocol.go — keep in sync by hand.
//
// Positions are venue-relative metres: origin at the top-left of the venue map,
// x to the right, y down. Phones may send GPS; the server converts it to venue
// metres on arrival and never stores or forwards the raw coordinates.

export type NodeStatus = 'connecting' | 'ok' | 'handling' | 'swaying' | 'wave' | 'stale';
export type Level = 'calm' | 'yellow' | 'red';
export type Point = [number, number];
import type { DemoRow } from './join';

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
  /** The key of the tower whose check-in QR code the phone joined through (?at=<key>): placed next to it. */
  at?: string;
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
  /**
   * Unit gravity vector in the device frame ([gx, gy, gz], 2 decimals): "down" as the phone sees it,
   * so the server can level the sample however the phone is carried. Optional: the server holds the
   * last value, so the phone sends it only when it changed. Never sent = upright against the chest
   * (x, z horizontal, y vertical).
   */
  g?: [number, number, number];
  /**
   * Compass heading in degrees clockwise from north, for dead reckoning (shared/locate.ts): `hd` of the
   * phone's top edge, or, when the top edge points more up or down than along the ground (|g[1]| > 0.8),
   * `hb` of its back (the way the rear camera looks). Send whichever axis is closer to horizontal. Optional.
   */
  hd?: number;
  hb?: number;
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
  /**
   * Personal guidance when this phone is in danger (red zone, packed cluster or
   * a push passing through): the direction to move, as a unit vector in venue
   * coordinates (x right, y down the map), toward lower density and, when one
   * is close enough in that direction, an open exit.
   */
  move?: {
    dx: number;
    dy: number;
    to?: string;
    reason: 'push' | 'density';
    /** How far the arrow can be trusted, 0..1: 1 for a hand-placed phone, lower the rougher its GPS fix. Below 0.5 show a plain instruction instead of an arrow. */
    conf?: number;
  };
  /** Venue bearing (degrees clockwise from north of the map's "up"), when the venue is GPS-anchored: lets a phone with a compass point the arrow for real. */
  bearing?: number;
  /** This phone's position (venue metres), for the little map on its screen. */
  x?: number;
  y?: number;
  /** Venue size (metres), for the same map. */
  w?: number;
  h?: number;
  /** This phone's generated name ("Blue Otter") and its colour (CSS hex), as on the dashboard map. */
  name?: string;
  color?: string;
  /** This state comes from the crowd simulation running around the phone (a drill), not from the real crowd. */
  sim?: boolean;
  /** Demo spot: this phone's place in the row at the table (shared/join.ts). */
  row?: DemoRow;
  /** Demo spot, while this phone is off the row (tapped a new spot, or beside a board): the place it goes back to with POST /api/demo/back (shared/demomove.ts). */
  spot?: number;
  /** Beside this board ("Zone light A"): the phone was walked up to it (Bluetooth snap). */
  near?: string;
}

export type ToPhone = Ping | PhoneState | import('./demo').Shake;

// ---- Server → dashboard ----

export interface Node {
  id: string;
  x: number;
  y: number;
  /** GPS accuracy radius in metres; 0 = placed by hand. */
  acc?: number;
  /**
   * How the position was set: GPS, by hand, by checking in at a tower (its QR code), Bluetooth beacons, or
   * 'est': worked out by the position estimator without GPS (entry spot, steps, neighbours; shared/locate.ts).
   */
  src?: 'gps' | 'manual' | 'tower' | 'beacon' | 'est';
  /**
   * The position estimator (shared/locate.ts). With it on, x, y are its estimate and acc its uncertainty
   * (68 % radius, m; 0 = as good as placed by hand). raw: where the phone would be shown without it (smoothed
   * GPS fix or hand placement), when that differs. loc: what went into the estimate. lost: too vague to say
   * where in the venue the phone is; it counts toward no cluster or density.
   */
  raw?: [number, number];
  loc?: import('./locate').LocSource[];
  lost?: boolean;
  /** Outside the venue rectangle: counts toward nothing. */
  outside?: boolean;
  /** A real phone's generated name and colour (absent for simulated and replayed phones). */
  name?: string;
  color?: string;
  /** Being shaken right now ("that's me"). */
  shake?: boolean;
  /** Mode "sim": a real phone standing in the simulated crowd. */
  real?: boolean;
  /** Connected but not located yet (no x/y, no accepted GPS fix): x, y mean nothing and it counts toward nothing. Keep it off the map. */
  unplaced?: boolean;
  /** Its place in the demo spot's row (1 = first), while the demo spot is on and it stands where the server lined it up. */
  slot?: number;
  /** Beside this board ("Zone light A"): the phone was walked up to it (Bluetooth snap). */
  near?: string;
  /**
   * How packed in this person is, next to the motion status (a still phone in a crush has status "ok").
   * dens: estimated people/m² around the phone; press: its level with the cluster thresholds, absent = calm;
   * crush: dens on a 0..1 scale for drawing (0.35 at the watch density, 0.7 at danger, 1 at 1.5 × danger).
   * All absent for a phone that counts toward no density (stale, outside, unplaced, lost).
   */
  dens?: number;
  press?: 'yellow' | 'red';
  crush?: number;
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
  /** The pair was found by motion (GPS-placed phones, positions only good to metres), not by distance on the map. */
  motion?: boolean;
  /** Table demo only: a push between two phones with no third in range (no chain): yellow at most. */
  pair?: boolean;
}

/** Table demo only: phones moving as one (matching, irregular sideways motion at a small steady lag). Yellow at most. */
export interface Together {
  members: string[];
  /** Largest neighbour lag in the group (ms). */
  lagMs: number;
  /** Weakest neighbour match. */
  corr: number;
  /** When it started (server ms). */
  since: number;
}

/** A group of phones packed together. */
export interface Cluster {
  id: string;
  /** Median position accuracy of the cluster's phones (m); absent when they were placed by hand. Several metres = `est` is averaged over a disc about that wide: a lower bound on the tightest spot. */
  acc?: number;
  x: number;
  y: number;
  r: number;
  count: number;
  /** Estimated people (count ÷ participation). */
  people?: number;
  /** Phones per m² averaged over the cluster disc (reads thin for big clusters; show `est`). */
  density: number;
  level?: Level;
  trend: 'forming' | 'steady' | 'dispersing';
  /** Estimated people per m² at the cluster's densest spot (scaled by participation): the number alerts and rules use. */
  est?: number;
  /** How fast the estimated density is changing (people/m² per minute). */
  rate?: number;
  /** Projected seconds until it reaches the danger density at the current rate (early warning). */
  eta?: number;
}

export interface Stats {
  phones: number;
  msgPerSec: number;
  medianRtt: number;
  /** Mean detector step time over the last few seconds (ms). */
  detectMs?: number;
  /** Size of the last snapshot sent to dashboards (bytes). */
  snapshotBytes?: number;
}

/** GET /api/edge?from=&to=: why the detector did (or didn't) call this neighbour pair a travelling wave. */
export interface EdgeExplain {
  from: string;
  to: string;
  /** Resampling step of the traces (ms). */
  stepMs: number;
  /** Band-passed horizontal motion of each phone over the correlation window, oldest first (m/s²); null = no valid sample. */
  a: (number | null)[];
  b: (number | null)[];
  /** Cross-correlation |r| at each lag (ms). Positive lag = b moves after a. */
  lags: number[];
  corr: (number | null)[];
  lagMs: number;
  peak: number;
  /** Height of the best separate peak (periodic motion has several). */
  second: number;
  wave: boolean;
  /** Each test the detector applies, and whether it passed. */
  checks: { name: string; pass: boolean; detail: string }[];
}

/** GET /api/eval: the detector run over every scenario and many random crowds (docs/eval.json). */
export interface EvalReport {
  generated: string;
  seeds: number;
  rows: {
    scenario: string;
    layout: 'line' | 'crowd' | 'sim';
    /** True if Pulse should alert. */
    expect: 'red' | 'calm' | 'yellow-ok';
    runs: number;
    red: number;
    yellow: number;
    calm: number;
    /** Median seconds to the first red, when it went red. */
    medianRedS?: number;
    /** Simulation rows: median lead time vs ground truth (s; positive = Pulse first). */
    medianLeadS?: number;
    note?: string;
  }[];
  summary: { falseAlarms: number; lookAlikeRuns: number; missed: number; positiveRuns: number };
}

/** Simulated people (only in mode "sim"): [x, y, pressure N/m, hasPhone 0|1]. */
export interface SimFrame {
  /**
   * Everyone as [x, y, pressure N/m, hasPhone 0|1, density people/m², heading °, state]; density (people
   * within 1 m ÷ the open part of that disc), heading (where the body faces: 0 = +x, clockwise since y
   * points down) and state (0 standing, 1 walking, 2 seated, 3 queueing, 4 pushing) are absent on older servers.
   */
  bodies: [number, number, number, number, number?, number?, number?][];
  t: number;
  action: string;
  /** How far the positions Pulse uses are from where the simulated people stand, once a second (shared/locate.ts). */
  loc?: import('./locate').LocError;
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
  /** The one overall status every part of the console shows: worst of zones, rules and clusters, with where and why. */
  status?: {
    level: Level;
    /** 0..1 crowd risk. */
    score: number;
    /** Zone id and name of the worst place ("" when calm). */
    zone?: string;
    where?: string;
    kind?: 'wave' | 'density' | 'rule' | 'early';
    /** Estimated people/m² at the worst spot, when density is the reason. */
    density?: number;
  };
  clusters: Cluster[];
  stats: Stats;
  /** The phone-to-phone mesh: links between phones and how each reaches the server (absent in a replay). */
  mesh?: import('./mesh').MeshFrame;
  /** The table demo profile is on (demo spot on; live only). */
  table?: boolean;
  /** Table demo only: groups of phones moving as one. */
  together?: Together[];
}

export interface Alert {
  type: 'alert';
  /** Stable id: later messages with the same id update the alert (ack, resolve, briefing). */
  id?: string;
  t: number;
  zone: string;
  level: Level;
  score: number;
  kind?: 'wave' | 'density' | 'rule';
  /** A density pre-warning: not dangerous yet, but projected to be soon. */
  early?: boolean;
  brief?: string;
  /** Structured briefing: what is happening and where … */
  headline?: string;
  /** … and the one thing staff should do. */
  action?: string;
  audioUrl?: string;
  test?: boolean;
  status?: 'open' | 'ack' | 'resolved';
  ackAt?: number;
  resolvedAt?: number;
  /** Who acknowledged / resolved (free text from the console), and the outcome note given on resolve. */
  ackBy?: string;
  resolvedBy?: string;
  note?: string;
  /** Still unacknowledged after the escalation delay: re-announced. */
  escalated?: boolean;
  /**
   * Where the data that raised it came from when that is not the live crowd: the crowd simulation or a saved
   * run. Absent for live alerts and drills. These alerts leave the log when the simulation or run stops.
   */
  source?: 'sim' | 'replay';
  /**
   * Table demo only, on a wave alert that is yellow: what raised it. "pair" = a push between two phones,
   * "together" = people moving as one. Absent otherwise (a crowd push).
   */
  cause?: 'pair' | 'together';
}

// ---- alert drills (POST /api/test-alert, GET /api/drill) ----

/** Body of POST /api/test-alert; {} is the old one-button test alert. */
export interface DrillRequest {
  zone?: string;
  level?: 'yellow' | 'red';
  kind?: 'wave' | 'density' | 'rule';
  outputs?: { briefing?: boolean; voice?: boolean; sign?: boolean; lights?: string[] };
}

/** What one output did in a drill. key: briefing | voice | sign | light:<letter>. */
export interface DrillOutput {
  key: string;
  label: string;
  state: 'ok' | 'failed' | 'skipped' | 'pending';
  note?: string;
}

/** One drill and what came of it (also the response of POST /api/test-alert). */
export interface DrillRecord {
  id: string;
  t: number;
  zone: string;
  where: string;
  level: 'yellow' | 'red';
  kind: string;
  outputs: DrillOutput[];
  brief?: string;
  audioUrl?: string;
}

/** An output's readiness before a drill is sent. */
export interface DrillReady {
  key: string;
  label: string;
  state: 'ready' | 'fallback' | 'offline' | 'none';
  note?: string;
  areas?: string[];
}

/** A place a drill can be sent to; sign / voice false = switched off there by the area's alert rules. */
export interface DrillZone {
  id: string;
  name: string;
  custom: boolean;
  light?: string;
  sign: boolean;
  voice: boolean;
}

/** GET /api/drill */
export interface DrillStatus {
  zones: DrillZone[];
  outputs: DrillReady[];
  history: DrillRecord[];
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
  /** The demo spot is on (GET /api/demo): the server places a phone that joins without a position. */
  demo?: boolean;
  /** The venue size the server starts with when nothing is saved (used when an event's data is cleared). */
  defaultW?: number;
  defaultH?: number;
}

/** Fixed features of the venue, in venue metres. */
export interface VenueLayout {
  /** Stage outline. */
  stage?: Point[];
  exits?: { id: string; name: string; x0: number; y0: number; x1: number; y1: number }[];
  walls?: [number, number, number, number][];
}

/** GET/PUT /api/venue. lat/lon is the map's top-left corner; bearing = degrees clockwise from north of the map's "up". */
export interface Venue {
  w: number;
  h: number;
  lat?: number;
  lon?: number;
  bearing?: number;
  geo: boolean;
  /** Preset the size came from (club, theatre, arena, festival) or "custom". */
  template?: string;
  /** A floor-plan image is stored: GET /api/venue/floorplan. */
  floorplan?: boolean;
  layout?: VenueLayout;
}

/** POST /api/venue/floorplan/analyze: Gemini's reading of the uploaded plan. Nothing is saved until staff accept it. */
export interface FloorplanSuggestion {
  w: number;
  h: number;
  layout: VenueLayout;
  /** What Gemini based the scale on, and anything it was unsure about. */
  notes: string;
  confidence: 'low' | 'medium' | 'high';
}

/** Per-area alert rules, on top of the detector's push and density alerts. */
export interface AlertRules {
  /** Alert when the estimated density inside the area exceeds this (people/m²); 0/absent = off. */
  density?: number;
  /** … for at least this long (s). */
  densityHoldS?: number;
  /** Push (travelling wave) detection for this area. Default on. */
  push?: boolean;
  /** Alert when more people than this are inside (capacity, estimated as phones ÷ participation); 0/absent = off. */
  maxPhones?: number;
  /** Text staff hear and see instead of the generic briefing. */
  message?: string;
  /** Where the alert goes. Defaults: all on. */
  notify?: { sign?: boolean; light?: boolean; voice?: boolean };
}

/** GET/PUT /api/areas: watch areas drawn by staff; each becomes a server zone. */
export interface Area {
  id: string;
  name: string;
  sens: 'normal' | 'high';
  poly: Point[];
  /** Zone light that shows this area (its letter in SIGN_URL); absent = none. */
  light?: string;
  rules?: AlertRules;
}

/** GET /api/hardware: every sign and zone light, probed every 5 s. */
export interface Hardware {
  name: string;
  /** "laptop", "sign" or a zone-light letter: the key in PUT /api/hardware/{key}/pos and in a check-in link (?at=<key>). */
  key?: string;
  /** sign | zone-light | laptop (this computer: always online, no probe). */
  kind: string;
  url: string;
  zone?: string;
  online: boolean;
  lastSeen?: number;
  /** Seconds since the board last answered, on the server's clock. */
  seenAgo?: number;
  error?: string;
  rssi?: number;
  uptime?: number;
  level?: string;
  ble?: { devices: number; near: number; scans: number; age: number };
  areas?: string[];
  /** Where staff placed the board on the venue map (metres); PUT /api/hardware/{key}/pos. */
  x?: number;
  y?: number;
  /** Bluetooth beacon name, e.g. PULSE-A. */
  beacon?: string;
  /** Other Pulse boards this one hears, with an estimated distance (log-distance path loss). */
  peers?: { name: string; rssi: number; dist: number; age: number; mapDist?: number }[];
  /** How the server reaches it now: "usb" (serial cable) or "wifi" (HTTP); absent while offline. */
  link?: 'usb' | 'wifi';
  /** Serial port, for a board on USB. */
  port?: string;
  /** Firmware build id the board reports ("1a2b3c4 2026-10-04"; "dev" = built by hand; absent = older than build ids). */
  fw?: string;
  /** Hash of the sketch in the server's checkout; fwOld = the board runs something else (reflash it). */
  fwWant?: string;
  fwOld?: boolean;
  /** The board's own Wi-Fi: joined or not, the network it is on or trying, its address there. */
  wifi?: boolean;
  ssid?: string;
  ip?: string;
}

/** One board in POST /api/hardware/test: shown red for a second, then asked what it shows. */
export interface BoardTest {
  key: string;
  name: string;
  url: string;
  link?: 'usb' | 'wifi';
  port?: string;
  /** The board took the command. */
  sent: boolean;
  /** It then reported the test level back. */
  confirmed: boolean;
  level?: string;
  fw?: string;
  fwOld?: boolean;
  error?: string;
  ms: number;
}

/** POST /api/hardware/table: boards and laptop in a row, the demo spot beside them, what each light shows. */
export interface TableDemo {
  hardware: Hardware[];
  demo: { on: boolean; x: number; y: number; spacing: number };
  lights: { key: string; shows: string }[];
  notes?: string[];
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
  /** Absent: a way out of the venue. "door": between two rooms. "turnstile": one person at a time. "emergency": used only in an alarm. */
  kind?: 'door' | 'turnstile' | 'emergency';
}

/** A piece of the simulated venue the map draws (venue metres). "fence" is a line x0,y0 → x1,y1; "label" is text only. */
export interface SimFurniture {
  kind: 'desk' | 'chairs' | 'seats' | 'stage' | 'board' | 'counter' | 'stairs' | 'turnstile' | 'gate' | 'fence' | 'label' | string;
  label?: string;
  x0: number;
  y0: number;
  x1: number;
  y1: number;
  /** chairs, seats: how many along the row. */
  n?: number;
}

/** A director action a scenario offers: a plain button, one that sends the strength slider, a click on the map, or a drag. */
export interface SimActionSpec {
  type: string;
  label: string;
  tip: string;
  kind: 'behaviour' | 'strength' | 'point' | 'drag';
}

/** A start scenario for the picker. w, h = the venue it builds (absent: the live venue). */
export interface SimScenario {
  id: string;
  name: string;
  desc: string;
  people: number;
  maxPeople: number;
  participation: number;
  w?: number;
  h?: number;
  actions: SimActionSpec[];
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
  /** The running (or previewed) scenario, the venue it built, and its furniture. */
  scenario?: string;
  venue?: { w: number; h: number };
  furniture?: SimFurniture[];
  /** Every scenario on offer. */
  scenarios?: SimScenario[];
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
  | { type: 'calm' | 'stage' | 'disperse' | 'dance' | 'intermission' | 'dismiss' | 'arrive' | 'rush' }
  | { type: 'surge' | 'alarm'; strength: number }
  | { type: 'attract'; x: number; y: number }
  | { type: 'shove'; x: number; y: number; dx: number; dy: number; strength?: number }
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
  /** PUT /api/node/{id}/pos dropped this phone on another phone's place in the demo row and the two swapped: the other one's name. */
  swapped?: string;
}

/** WebSocket URL on the same host as the page. */
export function wsURL(path: string): string {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${proto}//${location.host}${path}`;
}
