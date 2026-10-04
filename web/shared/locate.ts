// The position estimator: mirrors server/internal/protocol/locate.go.
//
// Everyone joins through one shared QR code and walks off; the server works
// out where each phone is (server/internal/locate). Nothing new is needed
// from a phone for that. Two optional additions make dead reckoning possible:
// a compass heading on each motion summary (`hd` / `hb` on Motion, see
// protocol.ts) and the phone's own step count and displacement ("dr").

/**
 * Phone → server, about once a second while there is something to report: the
 * steps counted since the page loaded and the metres walked east and north,
 * all running totals (a lost message loses nothing; totals that go backwards
 * mean the page was reloaded). A phone that counts steps but cannot tell the
 * direction leaves `e` and `n` where they are.
 */
export interface DRMsg {
  type: 'dr';
  steps: number;
  e: number;
  n: number;
}

/** What went into an estimated position (Node.loc). */
export type LocSource = 'entry' | 'gps' | 'steps' | 'fix' | 'beacon' | 'mesh' | 'near' | 'map';

/** Where the one shared QR code hangs: phones that join without a position start here ± sigma. */
export interface EntrySpot {
  on: boolean;
  x: number;
  y: number;
  /** Metres, 1 σ per axis; 0 = 1.5. */
  sigma: number;
}

/** PUT /api/locate. `bearing`: compass direction of the map's up (degrees clockwise from north) for a venue without a GPS anchor. */
export interface LocateConfig {
  on: boolean;
  entry: EntrySpot;
  bearing?: number;
}

/** Position error of the simulated phones against where their owners stand (SimFrame.loc), metres. */
export interface LocError {
  /** Phones with a position that isn't lost. */
  n: number;
  mean: number;
  median: number;
  p95: number;
  /** Phones with a raw source position (GPS or hand placement) and their mean error. */
  rawN: number;
  rawMean: number;
  /** Phones whose estimate is too vague to count. */
  lost: number;
}

/** GET /api/locate. */
export interface LocateStatus extends LocateConfig {
  phones: number;
  located: number;
  walking: number;
  links: number;
  /** Mean duration of the estimator's step over the last 5 s (ms). */
  stepMs: number;
  /** Mode "sim" only. */
  error?: LocError;
}
