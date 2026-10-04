// Wire types for the judge demo. Mirrors server/internal/protocol/demo.go
// (and SurgeResult in server/internal/app/hybrid.go); keep in sync by hand.

/** Server → phone: "I saw you shake" (sent the moment a shake starts). */
export interface Shake {
  type: 'shake';
}

/**
 * GET/PUT /api/demo. While on, phones that join without a position are
 * lined up from (x, y) toward +x, spacing metres apart, in join order.
 * PUT also takes `arrange: true` to line up everyone connected right now.
 */
export interface DemoSpot {
  on: boolean;
  x: number;
  y: number;
  /** Metres between phones (0.3–2; 0 = 0.6). */
  spacing: number;
}

/** GET /api/receipt/{id}: everything the server holds about one phone session. */
export interface Receipt {
  /** First 8 characters of the random session id. */
  id: string;
  name?: string;
  color?: string;
  /** Position in the room, venue metres. */
  x: number;
  y: number;
  /** How the position was set; "none" = the phone was never placed (x, y mean nothing). */
  src: 'gps' | 'manual' | 'tower' | 'none';
  /** Motion summaries received. */
  messages: number;
  /** Seconds since the phone joined. */
  seconds: number;
  /** Readings held in memory for the dashboard (the last 30 s at most)… */
  kept: number;
  /** …until this many seconds after the phone leaves. */
  forgetS: number;
  /** A labelled run was being recorded during the session: its readings are in a recording file. */
  recorded: boolean;
  /** Continuous storage is on: every reading was stored under the random id. */
  stored: boolean;
  /** Where: "Tiger Data" or "a file on the server". */
  store?: string;
}

/** POST /api/sim/surge-phones: the simulated crowd closes in on the real phones. */
export interface SurgeResult {
  mode: 'sim';
  x: number;
  y: number;
  /** Real phones it closes in on. */
  phones: number;
  /** The simulation was started by this call. */
  started: boolean;
}

/** GET /api/tower/{key}: a tower a phone can check in at (404 unknown key, 409 not on the map yet; both `{error}`). */
export interface Tower {
  key: string;
  name: string;
  x: number;
  y: number;
}
