// TS mirror of server/internal/protocol/join.go (and JoinInfo in
// protocol.go) — keep in sync by hand. Joining at the table: the QR link,
// testing it, phones' join reports and a phone's place in the demo row.

/** GET/PUT /api/join: the URL in the QR code and what's wrong with it. */
export interface JoinInfo {
  url: string;
  reachable: 'public' | 'lan' | 'local';
  /** https: phones only give motion sensors to https pages. */
  secure?: boolean;
  /** settings (PUT /api/join) | env (PUBLIC_URL) | tunnel (a running Cloudflare quick tunnel) | request (the dashboard's own address). */
  source?: 'settings' | 'env' | 'tunnel' | 'request';
  /** The URL without https:// and the trailing slash, to print under the code. */
  display?: string;
  /** Why phones can't use it, in words ("" = nothing known). */
  problem?: string;
  override?: string;
  env?: string;
  tunnel?: string;
}

/** POST /api/join/test {url?}: the server fetched the URL itself. */
export interface JoinTest {
  url: string;
  ok: boolean;
  status?: number;
  ms: number;
  secure: boolean;
  /** this | other (another Pulse server answered) | absent (not Pulse, or nothing). */
  pulse?: 'this' | 'other';
  message: string;
  reachable: 'public' | 'lan' | 'local';
}

export type JoinReason = 'ok' | 'inapp' | 'insecure' | 'motion-denied' | 'no-motion' | 'no-sensor' | 'perm-error' | 'socket';

/** POST /api/join/report. */
export interface JoinReport {
  id: string;
  reason: JoinReason;
  browser: string;
}

/** GET /api/join/stats: "3 joined · 1 couldn't get motion: in-app browser". */
export interface JoinStats {
  joined: number;
  streaming: number;
  problems: { reason: JoinReason; label: string; count: number; browsers: string[] }[];
}

/** PhoneState.row: this phone's place in the demo spot's row. */
export interface DemoRow {
  /** 1-based, in join order. */
  n: number;
  /** Name of the phone at place n-1, when someone stands there. */
  prev?: string;
  /** The row wrapped at the venue edge and this place starts a new one. */
  newRow?: boolean;
}
