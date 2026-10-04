// Moving about at the table demo. Mirrors server/internal/protocol/demomove.go;
// keep in sync by hand.

/** GET /api/demo/row: the row, the boards and the phones, for the phone's zoomed "I moved" map. */
export interface DemoView {
  on: boolean;
  /** The demo spot: the first place in the row (venue metres). */
  x: number;
  y: number;
  /** Metres between places. */
  spacing: number;
  /** Places in one row before it wraps. */
  cols: number;
  /** Every connected, placed phone (to the decimetre); no session ids. */
  phones: DemoViewPhone[];
  /** Boards and this laptop, where staff put them on the map. */
  boards: DemoViewBoard[];
}

export interface DemoViewPhone {
  /** Its place in the row (1 = first); absent = off the row. */
  n?: number;
  x: number;
  y: number;
  name?: string;
  color?: string;
  /** Beside this board (Bluetooth snap). */
  near?: string;
}

export interface DemoViewBoard {
  key: string;
  /** "Zone light A", "Sign", "This laptop". */
  label: string;
  x: number;
  y: number;
}

/** POST /api/demo/back {id} → the place in the row the phone is back on (404 unknown phone, 409 demo spot off). */
export interface DemoBack {
  n: number;
  x: number;
  y: number;
}
