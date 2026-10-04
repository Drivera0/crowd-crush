// The judge demo on the console: the demo spot (phones line up where staff
// say), dragging a real phone's dot to where it really is, and the one
// click that packs a simulated crowd around the real phones.

import type { DemoSpot, SurgeResult } from '../../shared/demo';
import type { Areas } from './areas';
import type { Mesh } from './mesh';

const $ = <T extends HTMLElement = HTMLElement>(id: string) => document.getElementById(id) as T;

export interface DemoCtx {
  mesh: Mesh;
  areas: Areas;
  /** live | replay | sim, from the latest snapshot. */
  mode: () => string;
  toast: (text: string, kind?: 'info' | 'ok' | 'watch' | 'danger' | 'error') => void;
  /** One-shot map pick (venue metres). */
  pickOnMap: (label: string, done: (p: { x: number; y: number }) => void) => void;
  /** Name of a phone, for messages. */
  nameOf: (id: string) => string;
  /** Back to live phones (stops a simulation or replay). */
  goLive: () => Promise<void>;
}

async function send<T>(method: 'PUT' | 'POST', path: string, body?: unknown): Promise<T> {
  const r = await fetch(path, {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const j = (await r.json().catch(() => ({}))) as T & { error?: string };
  if (!r.ok) throw new Error(j.error ?? r.statusText);
  return j;
}

export function initDemo(ctx: DemoCtx) {
  const { mesh, areas, toast } = ctx;
  let spot: DemoSpot = { on: false, x: 12, y: 8, spacing: 0.6 };
  const onBox = $('demoOn') as HTMLInputElement;
  const note = $('demoMsg');

  const render = () => {
    onBox.checked = spot.on;
    mesh.demoSpot = spot;
    $('demoWhere').textContent = spot.on ? `Row starts at ${spot.x.toFixed(1)} m, ${spot.y.toFixed(1)} m` : 'Off: phones use GPS or tap their spot';
    ($('demoArrange') as HTMLButtonElement).disabled = !spot.on;
  };

  const save = async (next: Partial<DemoSpot>, arrange = false) => {
    try {
      spot = await send<DemoSpot>('PUT', '/api/demo', { ...spot, ...next, arrange });
      render();
      return true;
    } catch (e) {
      toast(`Couldn't save the demo spot: ${(e as Error).message}`, 'error');
      render();
      return false;
    }
  };

  void (async () => {
    try {
      const r = await fetch('/api/demo');
      if (r.ok) spot = (await r.json()) as DemoSpot;
    } catch {
      /* older server: the card stays off */
    }
    render();
  })();

  onBox.addEventListener('change', async () => {
    if (await save({ on: onBox.checked })) toast(onBox.checked ? 'Demo spot on: phones that join line up in a row' : 'Demo spot off', 'ok');
  });
  $('demoPick').addEventListener('click', () => {
    ctx.pickOnMap('Click where the first phone should stand; the row runs to the right', async (p) => {
      if (await save({ on: true, x: p.x, y: p.y }, true)) toast('Demo spot placed', 'ok');
    });
  });
  $('demoArrange').addEventListener('click', async () => {
    if (await save({}, true)) toast('Phones lined up in join order', 'ok');
  });

  // Drag a real phone's dot to where the person really is.
  areas.canDragNode = (id) => ctx.mode() !== 'replay' && mesh.isNamed(id);
  areas.onNodeMoved = async (id, x, y) => {
    try {
      const d = await send('PUT', `/api/node/${encodeURIComponent(id)}/pos`, { x: Math.round(x * 100) / 100, y: Math.round(y * 100) / 100 });
      // Dropped on another phone's place in the demo row: the two swapped (server demomove.go).
      const swapped = (d as { swapped?: string } | undefined)?.swapped;
      toast(swapped ? `${ctx.nameOf(id)} and ${swapped} swapped places` : `${ctx.nameOf(id)} moved`, 'ok');
    } catch (e) {
      toast(`Couldn't move ${ctx.nameOf(id)}: ${(e as Error).message}`, 'error');
    }
    // Keep the dot where it was dropped until the next snapshots carry the new spot.
    setTimeout(() => mesh.nodeDrag?.id === id && mesh.dragNode(null), 400);
  };

  // One click: a simulated crowd closes in on the real phones.
  for (const b of document.querySelectorAll<HTMLButtonElement>('.surge-btn')) {
    b.addEventListener('click', async () => {
      b.disabled = true;
      try {
        const r = await send<SurgeResult>('POST', '/api/sim/surge-phones');
        const who = r.phones === 1 ? '1 phone' : `${r.phones} phones`;
        const text = r.phones > 0 ? `A simulated crowd is closing in on ${who}` : 'A simulated crowd is closing in on the demo spot';
        note.textContent = `${text}. Their screens say it is a drill.`;
        toast(text, 'watch');
      } catch (e) {
        note.textContent = (e as Error).message;
        toast((e as Error).message, 'error');
      }
      b.disabled = false;
    });
  }
  $('surgeStop').addEventListener('click', () => void ctx.goLive());

  return {
    /** Fetch the demo spot again (after something else changed it). */
    reload() {
      void (async () => {
        try {
          const r = await fetch('/api/demo');
          if (r.ok) spot = (await r.json()) as DemoSpot;
        } catch {
          /* keep what we have */
        }
        render();
      })();
    },
    /** Called on every snapshot. */
    onMode(mode: string) {
      $('surgeStop').hidden = mode !== 'sim';
      if (mode !== 'sim' && note.textContent?.includes('closing in')) note.textContent = '';
    },
  };
}
