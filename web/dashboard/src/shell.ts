// App shell: sidebar navigation between pages, and the one crowd map that
// moves into whichever page is showing (Live, Venue, Areas, Hardware,
// Simulation). Imported first by main.ts so the map exists in the
// DOM before anything looks it up.

export type Page = 'home' | 'live' | 'venue' | 'areas' | 'hardware' | 'sim' | 'drill' | 'settings';

const PAGES: Record<Page, { title: string; sub: string; map?: { title: string; sub: string; draw?: boolean } }> = {
  home: { title: 'Home', sub: 'Set up your event, then go live.' },
  live: {
    title: 'Live',
    sub: 'What the crowd is doing right now.',
    map: {
      title: 'Live crowd map',
      sub: "Each dot is an attendee's phone at its real spot, coloured by how packed in that person is; the ring shows its motion. Lines join phones close enough to compare readings.",
    },
  },
  venue: {
    title: 'Venue',
    sub: 'Size, floor plan and GPS anchor.',
    map: { title: 'Venue map', sub: 'The space attendees can place themselves in. Upload a floor plan to see it underneath.' },
  },
  areas: {
    title: 'Areas & alerts',
    sub: 'Mark risky spots and decide what raises an alert.',
    map: {
      title: 'Watch areas',
      sub: 'Pick a shape in the toolbar, then drag on the map. Click an area to select it, drag it to move it, press Delete to remove it.',
      draw: true,
    },
  },
  hardware: {
    title: 'Hardware',
    sub: 'Signs and zone lights.',
    map: { title: 'Board placement', sub: "Drag each board's marker to where it is in the venue. Lines show boards that hear each other over Bluetooth." },
  },
  sim: {
    title: 'Simulation',
    sub: 'A virtual crowd you can steer into a crush, and saved runs to play back.',
    map: { title: 'Simulated crowd', sub: 'Each shape is a person seen from above (shoulders and head, facing the way they face); ringed dots are the phones running Pulse. Colour is how crushed each person is: pale = free, amber = tight, red = dangerous, deep red = crushed.' },
  },
  drill: { title: 'Alert drill', sub: 'Send a test alert and see what each output did.' },
  settings: { title: 'Settings', sub: 'Privacy, and technical details for your technician.' },
};

// Put the map into the DOM straight away (in the Live page's slot).
const tpl = document.getElementById('mapHome') as HTMLTemplateElement;
const firstSlot = document.querySelector<HTMLElement>('[data-page="live"] .map-slot')!;
firstSlot.append(tpl.content.cloneNode(true));

let current: Page = 'home';
const listeners: ((p: Page) => void)[] = [];

/** Run cb now and whenever the page changes. */
export function onPage(cb: (p: Page) => void) {
  listeners.push(cb);
  cb(current);
}

export function page() {
  return current;
}

function show(p: Page) {
  current = p;
  for (const s of document.querySelectorAll<HTMLElement>('.page')) s.classList.toggle('on', s.dataset.page === p);
  for (const a of document.querySelectorAll<HTMLElement>('[data-nav]')) a.classList.toggle('on', a.dataset.nav === p);
  const info = PAGES[p];
  document.getElementById('pageTitle')!.textContent = info.title;
  document.getElementById('pageSub')!.textContent = info.sub;
  const map = document.getElementById('mapWrap')!;
  const slot = document.querySelector<HTMLElement>(`[data-page="${p}"] .map-slot`);
  if (slot && info.map) {
    if (map.parentElement !== slot) slot.append(map);
    document.getElementById('mapTitle')!.textContent = info.map.title;
    document.getElementById('mapSub')!.textContent = info.map.sub;
    map.classList.toggle('drawing', !!info.map.draw);
  }
  document.body.dataset.page = p;
  for (const cb of listeners) cb(p);
}

function fromHash(): Page {
  const h = location.hash.replace('#', '');
  // Old links: replays now live on the Simulation page ("Saved runs"), drills on their own page.
  if (h === 'recordings' || h === 'replay' || h === 'replays') {
    const saved = document.getElementById('savedRuns') as HTMLDetailsElement | null;
    if (saved) saved.open = true;
    window.setTimeout(() => saved?.scrollIntoView({ block: 'nearest', behavior: 'smooth' }), 100);
    return 'sim';
  }
  if (h === 'drills') return 'drill';
  return h in PAGES ? (h as Page) : 'home';
}

window.addEventListener('hashchange', () => show(fromHash()));
show(fromHash());

// ---------------------------------------------------------------------------
// Tooltips for every [data-tip]: one floating element, placed below (or above,
// or beside for the map toolbar) and clamped inside the viewport, so tips near
// an edge never get cut off or cause sideways scrolling.
// ---------------------------------------------------------------------------

const tip = document.createElement('div');
tip.className = 'tip';
tip.setAttribute('role', 'tooltip');
tip.hidden = true;
document.body.append(tip);
let tipFor: HTMLElement | null = null;

function placeTip(el: HTMLElement) {
  const text = el.dataset.tip;
  if (!text) return hideTip();
  tipFor = el;
  tip.textContent = text;
  tip.hidden = false;
  const r = el.getBoundingClientRect();
  const t = tip.getBoundingClientRect();
  const pad = 8;
  let x: number, y: number;
  if (el.closest('.toolbar')) {
    x = r.right + 8;
    y = r.top + r.height / 2 - t.height / 2;
  } else {
    x = r.left + r.width / 2 - t.width / 2;
    y = r.bottom + 6;
    if (y + t.height > innerHeight - pad) y = r.top - t.height - 6;
  }
  x = Math.max(pad, Math.min(innerWidth - t.width - pad, x));
  y = Math.max(pad, Math.min(innerHeight - t.height - pad, y));
  tip.style.transform = `translate(${Math.round(x)}px, ${Math.round(y)}px)`;
}
function hideTip() {
  tipFor = null;
  tip.hidden = true;
}
document.addEventListener('pointerover', (e) => {
  const el = (e.target as HTMLElement).closest?.<HTMLElement>('[data-tip]');
  if (el && el !== tipFor) placeTip(el);
  else if (!el && tipFor) hideTip();
});
document.addEventListener('focusin', (e) => {
  const el = (e.target as HTMLElement).closest?.<HTMLElement>('[data-tip]');
  if (el && (e.target as HTMLElement).matches(':focus-visible')) placeTip(el);
});
document.addEventListener('focusout', hideTip);
document.addEventListener('pointerdown', hideTip);
window.addEventListener('scroll', hideTip, true);
