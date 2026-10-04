// Bluetooth beacon positioning: mirrors server/internal/protocol/beacons.go.
//
// Scan mode (Android Chrome with chrome://flags/#enable-experimental-web-platform-features):
// the phone hears the boards' adverts and reports their signal strength.
// Connect mode (any Android Chrome): the phone connects to a board and writes
// its session id; the board measures the connection's signal strength.
// (A third way needs no web page: the Pulse Android app advertises the phone's
// id and the boards hear it; the server reports those ranges as src "adv".)
// Only Pulse boards (name prefix "PULSE-") are ever involved.

export const BEACON_PREFIX = 'PULSE-';
/** Connect mode: the zone lights' GATT service and its session-id characteristic. */
export const BEACON_SERVICE = '7b1e0001-52c4-4f6a-9d6b-50554c534500';
export const BEACON_ID_CHAR = '7b1e0002-52c4-4f6a-9d6b-50554c534500';

/** One board as the phone hears it: smoothed RSSI (dBm) and the samples behind it. */
export interface BeaconSeen {
  name: string;
  rssi: number;
  n?: number;
}

/** Phone → server, about once a second (at most 16 entries; an empty list = "I hear none"). */
export interface BeaconsMsg {
  type: 'beacons';
  seen: BeaconSeen[];
}

/** d = 10^((txPower1m − rssi) / (10 · pathLossN)), as in the zone-light firmware. */
export interface BeaconModel {
  txPower1m: number;
  pathLossN: number;
}

export interface BeaconBoard {
  /** Beacon name, e.g. PULSE-A. */
  name: string;
  key: string;
  label: string;
  /** Where staff placed it (venue m); absent = not on the map, so it can't be used. */
  x?: number;
  y?: number;
  online: boolean;
  /** This beacon's 1 m reference RSSI; calibrated = its own value, not the model's. */
  txPower1m: number;
  calibrated?: boolean;
  /** Connect mode: the board answers GET /links, and how many phones it reports. */
  connectable?: boolean;
  links?: number;
  /** Phones running the Pulse Android app that the board hears advertising. */
  heard?: number;
}

/** GET /api/beacons (PUT /api/beacons/model answers the same). */
export interface BeaconInfo {
  boards: BeaconBoard[];
  model: BeaconModel;
  /** 1 m reference for connect-mode ranges (the board measures the phone). */
  connTxPower1m: number;
  service: string;
  idChar: string;
  venueW: number;
  venueH: number;
  nearM: number;
  staleS: number;
  prefix: string;
  placed: number;
  /** Best fix this layout can give: 0 (near a board), 1 (along a line) or 2. */
  maxDims: number;
}

export interface BeaconHeard {
  name: string;
  rssi: number;
  n?: number;
  /** Estimated distance, m. */
  dist: number;
  placed: boolean;
  /** scan = the phone heard the board; conn / adv = the board measured the phone (over a connection / from the Android app's advert). */
  src: 'scan' | 'conn' | 'adv';
}

/** POST /api/beacons/locate, and "beacons" in GET /api/node/{id}. */
export interface BeaconFix {
  ok: boolean;
  /** 2 = 2-D; 1 = along the line through the boards only; 0 = next to board `near`. */
  dims: number;
  x: number;
  y: number;
  /** Overall uncertainty, m. */
  acc: number;
  along?: number;
  cross?: number;
  /** 1-D: unit vector of the line the boards lie on. */
  axis?: [number, number];
  near?: string;
  note: string;
  heard: BeaconHeard[];
  ageMs?: number;
  used?: boolean;
}
