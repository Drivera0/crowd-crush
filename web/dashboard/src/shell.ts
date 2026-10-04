// App shell: sidebar navigation between pages, and the one crowd map that
// moves into whichever page is showing (Live, Venue, Areas, Hardware,
// Simulation, Replays). Imported first by main.ts so the map exists in the
// DOM before anything looks it up.

export type Page = 'home' | 'live' | 'venue' | 'areas' | 'hardware' | 'sim' | 'recordings' | 'settings';

const PAGES: Record<Page, { title: string; sub: string; map?: { title: string; sub: string; draw?: boolean } }> = {
  home: { title: 'Home', sub: 'Set up your event, then go live.' },
  live: {
    title: 'Live',
    sub: 'What the crowd is doing right now.',
    map: {
      title: 'Live crowd map',
      sub: "Each dot is an attendee's phone at its real spot. Lines join phones close enough to compare readings; shaded circles are crowds packing together.",
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
    map: { title: 'Watch areas', sub: 'Draw with the tools on the left. Click an area to select it, drag to move it.', draw: true },
  },
  hardware: {
    title: 'Hardware',
    sub: 'Signs and zone lights.',
    map: { title: 'Board placement', sub: "Drag each board's marker to where it is in the venue. Lines show boards that hear each other over Bluetooth." },
  },
  sim: {
    title: 'Simulation',
    sub: 'A virtual crowd you can steer into a crush.',
    map: { title: 'Simulated crowd', sub: 'Grey dots are people without the app; coloured dots carry Pulse. Red glow = body pressure.' },
  },
  recordings: {
    title: 'Replays & drills',
    sub: 'Rehearse with saved runs and test the alert chain.',
    map: { title: 'Crowd map', sub: 'Replays play through the same detector as live phones.' },
  },
  settings: { title: 'Settings', sub: 'Services, system health and privacy.' },
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
  const h = location.hash.replace('#', '') as Page;
  return h in PAGES ? h : 'home';
}

window.addEventListener('hashchange', () => show(fromHash()));
show(fromHash());
