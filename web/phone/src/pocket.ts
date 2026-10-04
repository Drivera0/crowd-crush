// Pocket mode: the page only streams with the screen on, so a phone in a
// pocket gets tapped by accident. Pocket mode covers the page with a pure
// black sheet (cheap on OLED screens) that swallows every touch; the only
// way out is a deliberate two-second press. The zone colour shows as a thin
// border, and danger breaks through: when the phone goes red or is told
// which way to move, the sheet shows the warning and the arrow instead of
// staying black, and the phone buzzes where it can (Android).
//
// It only covers the screen: sensors, the WebSocket, the mesh, the wake lock
// and shake detection carry on underneath.

const $ = <T extends HTMLElement = HTMLElement>(id: string) => document.getElementById(id) as T;

const HOLD_MS = 2000;
const HINT_AFTER_MS = 6000;

export interface PocketView {
  zone: string; // '' | calm | yellow | red
  /** A neighbour's warning rather than the server's. */
  byNeighbour: boolean;
  /** Guidance: the arrow's rotation on screen (deg) and where it leads. */
  move: { angle: number; to: string } | null;
}

let on = false;
let view: PocketView = { zone: '', byNeighbour: false, move: null };
let holdTimer = 0;
let hintTimer = 0;
let hintShown = false;
let wasDanger = false;

const danger = () => view.zone === 'red' || !!view.move;

function render() {
  const el = $('pocket');
  el.classList.toggle('zone-yellow', view.zone === 'yellow');
  el.classList.toggle('danger', danger());
  $('pocketArrowWrap').hidden = !view.move;
  if (view.move) {
    $('pocketArrow').style.transform = `rotate(${view.move.angle}deg)`;
    $('pocketTo').textContent = view.move.to;
  } else {
    $('pocketTo').textContent = 'Stay on your feet. Arms up in front of your chest.';
  }
  $('pocketHead').textContent = view.move ? 'Move this way' : view.byNeighbour ? 'Warned by a neighbour' : 'Crowd danger near you';
  $('pocketLine').textContent = danger()
    ? 'Hold 2 s to unlock'
    : view.zone === 'yellow'
      ? 'Pulse is running · pressure building nearby · hold 2 s to unlock'
      : 'Pulse is running · hold 2 s to unlock';
}

/** Every change of zone or guidance (main.ts). */
export function pocketUpdate(v: PocketView) {
  view = v;
  if (!on) return;
  const d = danger();
  if (d && !wasDanger) {
    try {
      navigator.vibrate?.([400, 150, 400, 150, 800]);
    } catch {
      /* not supported (iPhone) */
    }
  }
  wasDanger = d;
  render();
}

/** The server saw this phone being shaken: a short flash of the border, so the person still gets their answer. */
export function pocketShake() {
  if (!on) return;
  const el = $('pocket');
  el.classList.remove('shake');
  void el.offsetWidth;
  el.classList.add('shake');
}

export function pocketOn() {
  return on;
}

function enter() {
  if (on) return;
  on = true;
  wasDanger = danger();
  dismissHint();
  $('pocket').hidden = false;
  render();
  // Android: full screen hides the address bar and the system bars, so there is less to hit. iPhones have no full screen for pages.
  try {
    void document.documentElement.requestFullscreen?.({ navigationUI: 'hide' })?.catch(() => {});
  } catch {
    /* not allowed: the sheet alone still works */
  }
}

/** Leave pocket mode (the hold completed, or the person is leaving Pulse). */
export function pocketExit() {
  if (!on) return;
  on = false;
  cancelHold();
  $('pocket').hidden = true;
  try {
    if (document.fullscreenElement) void document.exitFullscreen?.()?.catch(() => {});
  } catch {
    /* fine */
  }
}

function startHold() {
  if (holdTimer) return;
  $('pocket').classList.add('holding');
  holdTimer = window.setTimeout(() => {
    holdTimer = 0;
    $('pocket').classList.remove('holding');
    pocketExit();
  }, HOLD_MS);
}

function cancelHold() {
  window.clearTimeout(holdTimer);
  holdTimer = 0;
  $('pocket').classList.remove('holding');
}

function dismissHint() {
  window.clearTimeout(hintTimer);
  hintShown = true;
  $('pocketHint').hidden = true;
}

/** Call when the phone goes live: shows the button and, a few seconds later, the hint. */
export function pocketReady() {
  $('pocketBtn').hidden = false;
  $('pocketHelp').hidden = false;
  if (hintShown || hintTimer) return;
  hintTimer = window.setTimeout(() => {
    if (!on && !hintShown && !$('live').hidden) $('pocketHint').hidden = false;
    hintShown = true;
  }, HINT_AFTER_MS);
}

export function initPocket(isIOS: boolean) {
  $('pocketHelp').textContent = isIOS
    ? 'Want a hard lock? Turn on Guided Access (Settings → Accessibility), then triple-click the side button on this page.'
    : 'Want a hard lock? Pin this app: Settings → Security → App pinning, then pin the browser from the recent-apps screen.';
  $('pocketBtn').addEventListener('click', enter);
  $('pocketHintGo').addEventListener('click', enter);
  $('pocketHintNo').addEventListener('click', dismissHint);
  const el = $('pocket');
  // One finger (or the mouse) held down for two seconds; anything else is swallowed.
  el.addEventListener('pointerdown', (e) => {
    e.preventDefault();
    if (e.isPrimary) startHold();
    else cancelHold(); // several fingers: that is a pocket, not a person
  });
  for (const t of ['pointerup', 'pointercancel', 'pointerleave'] as const) el.addEventListener(t, cancelHold);
  for (const t of ['click', 'dblclick', 'contextmenu', 'touchstart', 'touchmove', 'touchend', 'wheel'] as const) {
    el.addEventListener(
      t,
      (e) => {
        e.preventDefault();
        e.stopPropagation();
      },
      { passive: false },
    );
  }
}
