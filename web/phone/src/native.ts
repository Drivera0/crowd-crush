// App mode: the same page inside the Pulse Android app (android/, docs/APP.md).
//
// The app is a thin shell around this page that adds what a browser can't
// give it: a Bluetooth scan without a Chrome flag or chooser pop-ups, a
// Bluetooth advert of this phone's session so the fixed boards can hear it,
// and a foreground service that keeps going with the screen off. It exposes
// `window.PulseNative`; in a browser that object doesn't exist and this file
// does nothing.
//
// After Join (the live screen appearing) scanning and advertising start by
// themselves. Beacon reports take the same road as the web scanner's: a
// {"type":"beacons","seen":[…]} message on the phone's WebSocket.

import type { BeaconSeen, BeaconsMsg } from '../../shared/beacons';

/** The object the app injects. Every call answers with JSON text. */
interface PulseNative {
  /** Start/stop hearing PULSE- boards; results come as `pulsenative` events about once a second. */
  scanBeacons(on: boolean): string;
  /** Advertise this session id ("" stops): manufacturer data 0xFFFF, "PLS1" + its first 8 hex characters. */
  advertise(id: string): string;
  status(): string;
  /** This page's hello message, so the app can carry the session while the screen is off ("" = left). */
  setSession(hello: string): void;
  /** Ask for the Bluetooth permission again. */
  requestPermissions(): void;
}

export interface NativeStatus {
  app: true;
  version: string;
  sdk: number;
  bluetooth: { supported: boolean; on: boolean; canScan: boolean; canAdvertise: boolean };
  permissions: { bluetooth: boolean; location: boolean; notifications: boolean };
  scanning: boolean;
  scanError: string;
  adverts: number;
  advertising: boolean;
  advertId: string;
  advertError: string;
  service: boolean;
  joined: boolean;
  screenOffMode: string;
  native: { active: boolean; connected: boolean; sent: number };
  /** While the page was away (screen off): how often, for how long, how many motion readings the app sent itself. */
  screenOff: { times: number; seconds: number; sent: number };
}

type NativeEvent = (BeaconsMsg & { type: 'beacons' }) | (NativeStatus & { type: 'status' });

function bridge(): PulseNative | undefined {
  const n = (window as unknown as { PulseNative?: PulseNative }).PulseNative;
  return n && typeof n.status === 'function' && typeof n.scanBeacons === 'function' ? n : undefined;
}

/** True inside the Pulse app. */
export function nativeApp(): boolean {
  return !!bridge();
}

/**
 * Hooks the page up to the app. `send` puts a message on the phone's WebSocket
 * (the one initBeacons uses); `hello` is the page's current hello message.
 * Does nothing in a browser.
 */
export function initNative(id: string, send: (msg: BeaconsMsg) => void, hello: () => unknown) {
  try {
    const n = bridge();
    if (n) start(n, id, send, hello);
  } catch {
    /* an extra: it must never break the phone page */
  }
}

function start(n: PulseNative, id: string, send: (msg: BeaconsMsg) => void, hello: () => unknown) {
  const live = document.getElementById('live');
  const receipt = document.getElementById('receipt');
  const host = document.querySelector('#live .bottom');
  if (!live || !host) return;

  document.body.classList.add('app-mode');
  // The app keeps the screen on and keeps running with it off: the browser's "keep the screen on" tip doesn't apply.
  const style = document.createElement('style');
  style.textContent = '.app-mode #screenTip{display:none}';
  document.head.append(style);
  // The web page's own opt-in Bluetooth button (beacons.ts; it has no id) has nothing to add in here.
  for (const b of host.querySelectorAll(':scope > button:not([id])')) {
    if (/Bluetooth beacons|Pulse board/.test(b.textContent ?? '')) (b as HTMLElement).hidden = true;
  }

  const line = document.createElement('p');
  line.id = 'appLine';
  line.className = 'fine';
  host.prepend(line);

  let status: NativeStatus | null = null;
  let seen: BeaconSeen[] = [];
  let running = false;
  let timer = 0;

  const readStatus = (text: string) => {
    try {
      status = JSON.parse(text) as NativeStatus;
    } catch {
      /* keep the last one */
    }
  };

  const render = () => {
    const s = status;
    if (!s) return;
    let bt: string;
    let fix = false;
    if (!s.bluetooth.supported) bt = 'no Bluetooth on this phone';
    else if (!s.permissions.bluetooth) {
      bt = 'Bluetooth not allowed — tap here to allow it';
      fix = true;
    } else if (!s.bluetooth.on) bt = 'Bluetooth is off — turn it on to be placed by the venue’s beacons';
    else {
      bt = 'Bluetooth on';
      if (s.scanning) bt += seen.length ? ` · hearing ${seen.map((b) => `${b.name} ${Math.round(b.rssi)} dBm`).join(', ')}` : ' · no Pulse board in range';
      else if (s.scanError) bt += ` · can’t listen (${s.scanError})`;
      if (s.advertising) bt += ' · visible to the boards';
      else if (s.advertError) bt += ` · can’t advertise (${s.advertError})`;
    }
    let text = `App mode: ${bt}`;
    if (s.screenOff.sent > 0) text += ` · ${s.screenOff.sent} readings sent with the screen off`;
    line.textContent = text;
    line.style.textDecoration = fix ? 'underline' : '';
  };

  line.addEventListener('click', () => {
    if (status && !status.permissions.bluetooth) n.requestPermissions();
  });

  window.addEventListener('pulsenative', (e) => {
    const d = (e as CustomEvent<NativeEvent>).detail;
    if (!d || typeof d !== 'object') return;
    if (d.type === 'beacons' && Array.isArray(d.seen)) {
      seen = d.seen;
      if (running) send({ type: 'beacons', seen });
    } else if (d.type === 'status') {
      status = d;
      // Bluetooth was just allowed or switched on: start what couldn't start before.
      if (running && d.permissions.bluetooth && d.bluetooth.on && !d.scanning && !d.scanError) n.scanBeacons(true);
    }
    render();
  });

  const session = () => {
    try {
      n.setSession(JSON.stringify(hello()));
    } catch {
      /* the next round will do */
    }
  };

  const begin = () => {
    if (running) return;
    running = true;
    session();
    n.scanBeacons(true);
    readStatus(n.advertise(id));
    render();
    // Keep the app's copy of the hello fresh (the position in it changes when the person moves).
    timer = window.setInterval(session, 5000);
  };

  const end = () => {
    if (!running) return;
    running = false;
    clearInterval(timer);
    seen = [];
    n.setSession('');
    n.scanBeacons(false);
    readStatus(n.advertise(''));
    render();
  };

  // Join = the live screen appearing; Leave = the receipt appearing. Watching the screens keeps main.ts untouched.
  const check = () => {
    if (receipt && !receipt.hidden) end();
    else if (!live.hidden) begin();
  };
  const obs = new MutationObserver(check);
  obs.observe(live, { attributes: true, attributeFilter: ['hidden'] });
  if (receipt) obs.observe(receipt, { attributes: true, attributeFilter: ['hidden'] });

  // Back from a stretch with the screen off: show what the app did meanwhile.
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState !== 'visible') return;
    readStatus(n.status());
    render();
  });

  readStatus(n.status());
  render();
  check();
}
