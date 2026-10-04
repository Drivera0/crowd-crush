// Bluetooth beacon positioning on the phone: an opt-in extra, Android Chrome only.
//
// Two ways for a web page to range against the fixed Pulse boards:
//
//   scan     navigator.bluetooth.requestLEScan: the page hears the boards'
//            adverts with their signal strength. Needs the Chrome flag
//            chrome://flags/#enable-experimental-web-platform-features.
//   connect  navigator.bluetooth.requestDevice + gatt.connect(): shipped in
//            every Android Chrome. The page connects to a board (one chooser
//            pop-up per board) and writes its session id; the board measures
//            the connection's signal strength and the server reads it there.
//
// iPhone browsers have neither, and nothing is shown there. Only devices
// whose name starts with "PULSE-" are ever asked for, heard or reported.

import { BEACON_ID_CHAR, BEACON_PREFIX, BEACON_SERVICE } from '../../shared/beacons';
import type { BeaconSeen, BeaconsMsg } from '../../shared/beacons';

// ---- the parts of Web Bluetooth used here (not in TypeScript's DOM lib) ----

interface BtDevice extends EventTarget {
  id: string;
  name?: string;
  gatt?: BtGattServer;
}
interface BtGattServer {
  connected: boolean;
  connect(): Promise<BtGattServer>;
  disconnect(): void;
  getPrimaryService(uuid: string): Promise<{ getCharacteristic(uuid: string): Promise<{ writeValue(v: BufferSource): Promise<void> }> }>;
}
interface BtScan {
  active: boolean;
  stop(): void;
}
interface BtAdvertEvent extends Event {
  device: BtDevice;
  name?: string;
  rssi?: number;
}
interface Bt extends EventTarget {
  requestLEScan?(opts: { filters: { namePrefix: string }[]; keepRepeatedDevices: boolean }): Promise<BtScan>;
  requestDevice?(opts: { filters: { namePrefix: string }[]; optionalServices: string[] }): Promise<BtDevice>;
}

function bt(): Bt | undefined {
  return (navigator as unknown as { bluetooth?: Bt }).bluetooth;
}

export interface BeaconSupport {
  /** https (or localhost): Web Bluetooth exists only there. */
  secure: boolean;
  /** The scanning API is there (the flag is on). */
  scan: boolean;
  /** Standard Web Bluetooth is there (connect mode). */
  connect: boolean;
  /** Only per-device watchAdvertisements: not used (it would need a chooser per board and still no scan). */
  watchOnly: boolean;
  android: boolean;
  ios: boolean;
}

export function beaconSupport(): BeaconSupport {
  const b = bt();
  const ua = navigator.userAgent;
  const scan = typeof b?.requestLEScan === 'function';
  const BtDeviceCtor = (window as unknown as { BluetoothDevice?: { prototype: object } }).BluetoothDevice;
  return {
    secure: window.isSecureContext,
    scan,
    connect: typeof b?.requestDevice === 'function',
    watchOnly: !scan && !!BtDeviceCtor && 'watchAdvertisements' in BtDeviceCtor.prototype,
    android: /Android/.test(ua),
    ios: /iPhone|iPad|iPod/.test(ua) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1),
  };
}

export const BEACON_FLAG = 'chrome://flags/#enable-experimental-web-platform-features';

// ---- scan mode ----

const WINDOW_MS = 2000; // median over the adverts of the last 2 s …
const EMA = 0.35; // … then an exponential moving average of the medians
const FORGET_MS = 5000; // a board not heard for this long is dropped

function median(v: number[]): number {
  const s = [...v].sort((a, b) => a - b);
  const m = s.length >> 1;
  return s.length % 2 ? s[m] : (s[m - 1] + s[m]) / 2;
}

/** One board being heard. */
export interface BeaconTrack {
  name: string;
  /** Latest raw reading, dBm. */
  raw: number;
  /** Median of the window, then smoothed. */
  rssi: number;
  /** Adverts in the window. */
  n: number;
  /** When it was last heard (ms, Date.now()). */
  at: number;
}

export class BeaconScanner {
  private tracks = new Map<string, { samples: { t: number; rssi: number }[]; ema: number | null; raw: number; at: number }>();
  private scan: BtScan | null = null;
  private listening = false;
  /** Adverts received since the scan started. */
  adverts = 0;

  get active(): boolean {
    return !!this.scan?.active;
  }

  /** Must be called from a tap: Chrome shows its permission prompt. Rejects if refused or unsupported. */
  async start(): Promise<void> {
    const b = bt();
    if (!b?.requestLEScan) throw new Error('This browser can’t scan for Bluetooth beacons.');
    if (!this.listening) {
      this.listening = true;
      b.addEventListener('advertisementreceived', (e) => this.onAdvert(e as BtAdvertEvent));
    }
    this.scan?.stop();
    this.scan = await b.requestLEScan({ filters: [{ namePrefix: BEACON_PREFIX }], keepRepeatedDevices: true });
  }

  stop() {
    this.scan?.stop();
    this.scan = null;
    this.tracks.clear();
  }

  private onAdvert(e: BtAdvertEvent) {
    const name = (e.name || e.device?.name || '').trim();
    const rssi = e.rssi;
    // The scan is filtered by name already; check again so nothing else can ever be reported.
    if (!name.startsWith(BEACON_PREFIX) || name.length > 32 || typeof rssi !== 'number' || rssi > -20 || rssi < -110) return;
    const now = Date.now();
    let t = this.tracks.get(name);
    if (!t) {
      if (this.tracks.size >= 16) return;
      t = { samples: [], ema: null, raw: rssi, at: now };
      this.tracks.set(name, t);
    }
    t.samples.push({ t: now, rssi });
    t.raw = rssi;
    t.at = now;
    this.adverts++;
  }

  /** The boards heard in the last 5 s, strongest first. Call about once a second: each call advances the smoothing. */
  read(now = Date.now()): BeaconTrack[] {
    const out: BeaconTrack[] = [];
    for (const [name, t] of this.tracks) {
      if (now - t.at > FORGET_MS) {
        this.tracks.delete(name);
        continue;
      }
      t.samples = t.samples.filter((s) => now - s.t <= WINDOW_MS);
      if (t.samples.length) {
        const med = median(t.samples.map((s) => s.rssi));
        t.ema = t.ema === null ? med : t.ema + EMA * (med - t.ema);
      }
      if (t.ema === null) continue;
      out.push({ name, raw: t.raw, rssi: Math.round(t.ema * 10) / 10, n: t.samples.length, at: t.at });
    }
    return out.sort((a, b) => b.rssi - a.rssi);
  }
}

export function toSeen(tracks: BeaconTrack[]): BeaconSeen[] {
  return tracks.map((t) => ({ name: t.name, rssi: t.rssi, n: t.n }));
}

// ---- connect mode ----

export interface BoardLink {
  name: string;
  state: 'connecting' | 'connected' | 'reconnecting' | 'failed';
  note: string;
}

export class BoardLinks {
  private entries: { dev: BtDevice; link: BoardLink; tries: number; timer: number }[] = [];
  /** Called whenever a link changes state. */
  onChange: () => void = () => {};

  /** id = what is written to each board: this page's session id (letters, digits, dashes; ≤ 36). */
  constructor(private id: () => string) {}

  get links(): BoardLink[] {
    return this.entries.map((e) => e.link);
  }

  get connected(): number {
    return this.entries.filter((e) => e.link.state === 'connected').length;
  }

  /** Must be called from a tap: opens Chrome's chooser for one board. Rejects if the chooser is cancelled. */
  async add(): Promise<void> {
    const b = bt();
    if (!b?.requestDevice) throw new Error('This browser has no Web Bluetooth.');
    const dev = await b.requestDevice({ filters: [{ namePrefix: BEACON_PREFIX }], optionalServices: [BEACON_SERVICE] });
    let e = this.entries.find((x) => x.dev.id === dev.id);
    if (!e) {
      e = { dev, link: { name: dev.name || 'Pulse board', state: 'connecting', note: '' }, tries: 0, timer: 0 };
      this.entries.push(e);
      const entry = e;
      dev.addEventListener('gattserverdisconnected', () => this.lost(entry));
    }
    e.tries = 0;
    await this.open(e);
  }

  private async open(e: BoardLinks['entries'][number]) {
    clearTimeout(e.timer);
    if (e.link.state !== 'reconnecting') e.link.state = 'connecting';
    this.onChange();
    try {
      if (!e.dev.gatt) throw new Error('no GATT');
      const server = await e.dev.gatt.connect();
      let ch;
      try {
        ch = await (await server.getPrimaryService(BEACON_SERVICE)).getCharacteristic(BEACON_ID_CHAR);
      } catch {
        // Connected, but it has no Pulse service: the sign, or a light with older firmware.
        e.link.state = 'failed';
        e.link.note = 'This board doesn’t take connections (the sign, or older firmware).';
        e.dev.gatt.disconnect();
        this.onChange();
        return;
      }
      await ch.writeValue(new TextEncoder().encode(this.id().slice(0, 36)));
      e.link.state = 'connected';
      e.link.note = '';
      e.tries = 0;
    } catch (err) {
      this.retry(e, err);
    }
    this.onChange();
  }

  private lost(e: BoardLinks['entries'][number]) {
    if (e.link.state === 'failed') return;
    this.retry(e, null);
    this.onChange();
  }

  /** An already chosen device can be reconnected without another tap. */
  private retry(e: BoardLinks['entries'][number], err: unknown) {
    e.tries++;
    e.link.state = 'reconnecting';
    e.link.note = e.tries > 3 ? `Out of range, or the board is full${err ? ` (${String(err).slice(0, 60)})` : ''}.` : '';
    clearTimeout(e.timer);
    e.timer = window.setTimeout(() => void this.open(e), Math.min(1000 * e.tries, 5000));
  }

  /** Drops every link. */
  close() {
    for (const e of this.entries) {
      clearTimeout(e.timer);
      e.link.state = 'failed';
      e.dev.gatt?.disconnect();
    }
    this.entries = [];
    this.onChange();
  }
}

// ---- the phone page: one small button on the live screen ----

/**
 * Adds the opt-in Bluetooth button to the live screen. Does nothing on phones
 * without Web Bluetooth (every iPhone). Scanning is preferred when the flag is
 * on; otherwise connect mode. `send` puts a message on the phone's WebSocket.
 */
export function initBeacons(id: string, send: (msg: BeaconsMsg) => void) {
  try {
    addBeaconButton(id, send);
  } catch {
    /* an extra: it must never break the phone page */
  }
}

function addBeaconButton(id: string, send: (msg: BeaconsMsg) => void) {
  const sup = beaconSupport();
  const host = document.querySelector('#live .bottom');
  if (!host || !sup.secure || !(sup.scan || sup.connect)) return;

  const btn = document.createElement('button');
  btn.className = 'small';
  const note = document.createElement('p');
  note.className = 'fine';
  note.hidden = true;
  // Above the first button that is a direct child of the row (":scope >": a nested one can't be an insertion point).
  host.insertBefore(btn, host.querySelector(':scope > #moveBtn') ?? host.querySelector(':scope > button'));
  btn.after(note);
  const say = (t: string) => {
    note.textContent = t;
    note.hidden = !t;
  };

  if (sup.scan) {
    const scanner = new BeaconScanner();
    let timer = 0;
    btn.textContent = 'Use Bluetooth beacons for a better position';
    btn.addEventListener('click', async () => {
      try {
        await scanner.start();
      } catch (e) {
        say(`Bluetooth scanning didn’t start (${String(e).slice(0, 80)}).`);
        return;
      }
      btn.hidden = true;
      say('Listening for Pulse boards…');
      clearInterval(timer);
      timer = window.setInterval(() => {
        if (!scanner.active) {
          // Chrome stops a scan when the page is hidden; starting again needs a tap.
          clearInterval(timer);
          btn.textContent = 'Bluetooth paused: tap to resume';
          btn.hidden = false;
          say('');
          return;
        }
        const tracks = scanner.read();
        send({ type: 'beacons', seen: toSeen(tracks) });
        say(tracks.length ? `Bluetooth: ${tracks.map((t) => `${t.name} ${Math.round(t.rssi)} dBm`).join(' · ')}` : 'Bluetooth: no Pulse board in range.');
      }, 1000);
    });
    return;
  }

  const links = new BoardLinks(() => id);
  btn.textContent = 'Connect to a Pulse board for a better position';
  links.onChange = () => {
    const l = links.links;
    btn.textContent = l.length ? 'Connect another Pulse board' : 'Connect to a Pulse board for a better position';
    say(l.map((x) => `${x.name}: ${x.state}${x.note ? ` (${x.note})` : ''}`).join(' · '));
  };
  btn.addEventListener('click', async () => {
    try {
      await links.add();
    } catch (e) {
      if ((e as { name?: string }).name !== 'NotFoundError') say(`Couldn’t connect (${String(e).slice(0, 80)}).`); // NotFoundError = chooser cancelled
    }
  });
}
