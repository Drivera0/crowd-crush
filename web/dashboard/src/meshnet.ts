// The phone-to-phone mesh on the console: the "Mesh" layer of the map (real
// WebRTC links between phones, drawn apart from the detector's neighbour
// lines, with packets travelling along a link while one phone's data is being
// relayed through another), the "reporting · via the mesh" counter, the mesh
// rows of the attendee drawer, and the lost-signal demo (jam one phone, or
// half of them).

import type { MeshFrame, MeshNode } from '../../shared/mesh';
import type { Node } from '../../shared/protocol';
import type { Mesh } from './mesh';

const $ = <T extends HTMLElement = HTMLElement>(id: string) => document.getElementById(id) as T;

export interface MeshNetCtx {
  mesh: Mesh;
  toast: (text: string, kind?: 'info' | 'ok' | 'watch' | 'danger' | 'error') => void;
  /** Name of a phone for people ("Blue Otter"). */
  nameOf: (id: string) => string;
  /** The phone whose drawer is open. */
  selected: () => string | null;
}

const VIOLET = '167,139,250';

async function jam(body: { id?: string; half?: boolean; on: boolean }): Promise<string[]> {
  const r = await fetch('/api/mesh/jam', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
  const j = (await r.json().catch(() => ({}))) as { phones?: string[]; error?: string };
  if (!r.ok) throw new Error(j.error ?? r.statusText);
  return j.phones ?? [];
}

export function initMeshNet(ctx: MeshNetCtx) {
  const { mesh, toast } = ctx;
  let frame: MeshFrame | null = null;
  let ids: string[] = [];
  let byId = new Map<string, MeshNode>();
  const layer = $('meshLayer') as HTMLInputElement;
  try {
    layer.checked = localStorage.getItem('pulse.meshLayer') !== '0';
  } catch {
    /* private mode */
  }
  layer.addEventListener('change', () => {
    try {
      localStorage.setItem('pulse.meshLayer', layer.checked ? '1' : '0');
    } catch {
      /* private mode */
    }
    apply();
  });

  const apply = () => {
    const on = layer.checked && !!frame && (frame.links.length > 0 || frame.nodes.some((n) => n.via));
    // With the real links on screen the decorative "readings shared between neighbours" dots would only confuse.
    mesh.quietGossip = on;
    mesh.overlay = on ? draw : null;
  };

  /** Drawn in the map's world coordinates, above the neighbour lines and below the dots. */
  const draw = (g: CanvasRenderingContext2D, now: number) => {
    const f = frame;
    if (!f) return;
    g.save();
    g.lineCap = 'round';
    // Links between phones.
    g.setLineDash([2, 5]);
    g.lineWidth = 1.3;
    g.strokeStyle = `rgba(${VIOLET},${f.virtual ? 0.3 : 0.6})`;
    g.beginPath();
    for (const [i, j] of f.links) {
      const a = mesh.bodyAt(ids[i]), b = mesh.bodyAt(ids[j]);
      if (!a || !b) continue;
      g.moveTo(a.x, a.y);
      g.lineTo(b.x, b.y);
    }
    g.stroke();
    g.setLineDash([]);
    // Relays: a phone with no connection of its own, the phone that carries its data, and the data on its way.
    for (const n of f.nodes) {
      if (!n.via) continue;
      const a = mesh.bodyAt(n.id), b = mesh.bodyAt(n.via);
      if (!a) continue;
      g.strokeStyle = `rgba(${VIOLET},0.95)`;
      g.lineWidth = 2;
      g.setLineDash([3, 3]);
      g.beginPath();
      g.arc(a.x, a.y, 12, 0, Math.PI * 2);
      g.stroke();
      g.setLineDash([]);
      if (!b) continue;
      g.strokeStyle = `rgba(${VIOLET},0.55)`;
      g.lineWidth = (n.hops ?? 1) > 1 ? 1.5 : 2.5;
      if ((n.hops ?? 1) > 1) g.setLineDash([6, 4]); // passes through other phones on the way
      g.beginPath();
      g.moveTo(a.x, a.y);
      g.lineTo(b.x, b.y);
      g.stroke();
      g.setLineDash([]);
      const d = Math.hypot(b.x - a.x, b.y - a.y);
      const count = Math.max(2, Math.min(5, Math.round(d / 40)));
      g.fillStyle = '#ede9fe';
      for (let k = 0; k < count; k++) {
        const t = (now / 700 + k / count) % 1;
        g.globalAlpha = Math.sin(Math.PI * t);
        g.beginPath();
        g.arc(a.x + (b.x - a.x) * t, a.y + (b.y - a.y) * t, 2.6, 0, Math.PI * 2);
        g.fill();
      }
      g.globalAlpha = 1;
    }
    g.restore();
  };

  /** "direct" or "relayed via Blue Otter · 2 hops", with the link count and mesh round trip. */
  const routeText = (n: MeshNode | undefined): string => {
    if (!n) return 'direct · no links to other phones';
    const route = n.via
      ? `relayed via ${ctx.nameOf(n.via)} · ${n.hops ?? 1} ${(n.hops ?? 1) === 1 ? 'hop' : 'hops'}`
      : n.jam
        ? 'connection dropped, looking for a neighbour'
        : 'direct';
    const links = n.peers ? `${n.peers} ${n.peers === 1 ? 'link' : 'links'}${n.rtt ? ` · ${n.rtt} ms` : ''}` : 'no links';
    return `${route} · ${links}`;
  };

  const posText = (n: MeshNode | undefined): string => {
    const p = n?.pos;
    if (!p) return '–';
    const from = `mesh-corrected from ${p.n ?? 0} ${(p.n ?? 0) === 1 ? 'neighbour' : 'neighbours'}`;
    const anchor = p.hops === undefined ? 'no anchor in reach' : p.hops === 0 ? 'itself an anchor' : `${p.hops} ${p.hops === 1 ? 'hop' : 'hops'} from an anchor`;
    return `${from} · ${anchor} (${p.x.toFixed(1)} m, ${p.y.toFixed(1)} m ±${p.acc.toFixed(1)} m)`;
  };

  const renderDrawer = () => {
    const id = ctx.selected();
    if (!id) return;
    const n = byId.get(id);
    $('dMesh').textContent = routeText(n);
    $('dMeshPos').textContent = posText(n);
    $('dMeshData').textContent = n && (n.tx || n.rx) ? `${((n.tx ?? 0) / 1024).toFixed(1)} kB/s out · ${((n.rx ?? 0) / 1024).toFixed(1)} kB/s in · knows ${n.known ?? 0} phones` : '–';
    const btn = $('dJam') as HTMLButtonElement;
    const off = !!n && (n.jam || !!n.via);
    btn.hidden = !n || frame?.virtual === true && !n.peers && !off;
    btn.textContent = off ? 'Restore connection' : 'Jam connection';
    btn.dataset.on = off ? '0' : '1';
    btn.disabled = !off && !n?.peers;
    btn.title = btn.disabled ? 'This phone has no link to another phone yet, so it would have nothing to fall back on.' : '';
  };

  $('dJam').addEventListener('click', async () => {
    const id = ctx.selected();
    if (!id) return;
    const on = ($('dJam') as HTMLButtonElement).dataset.on === '1';
    try {
      await jam({ id, on });
      toast(on ? `${ctx.nameOf(id)}: connection jammed. Its data now has to come through another phone.` : `${ctx.nameOf(id)}: connection restored.`, on ? 'watch' : 'ok');
    } catch (e) {
      toast(`Couldn't ${on ? 'jam' : 'restore'} that phone: ${(e as Error).message}`, 'error');
    }
  });

  const half = $('jamHalf') as HTMLButtonElement;
  half.addEventListener('click', async () => {
    const on = half.dataset.on !== '1';
    try {
      const phones = await jam({ half: true, on });
      if (on && phones.length === 0) {
        toast('No phone can be jammed yet: phones need a link to another phone first (two or more phones on the same network).', 'info');
        return;
      }
      toast(on ? `Jammed ${phones.length} ${phones.length === 1 ? 'phone' : 'phones'}: their WebSockets are closed. Watch them keep reporting through their neighbours.` : 'Connections restored.', on ? 'watch' : 'ok');
    } catch (e) {
      toast(`Mesh: ${(e as Error).message}`, 'error');
    }
  });

  return {
    /** Every snapshot: the mesh frame and the nodes it indexes. */
    onSnapshot(f: MeshFrame | undefined, nodes: Node[]) {
      frame = f ?? null;
      ids = nodes.map((n) => n.id);
      byId = new Map((f?.nodes ?? []).map((n) => [n.id, n]));
      apply();
      const c = $('meshCount');
      const jammed = (f?.nodes ?? []).filter((n) => n.jam || n.via).length;
      if (!f || f.phones === 0) {
        c.hidden = true;
      } else {
        c.hidden = false;
        c.textContent = `${f.reporting} of ${f.phones} ${f.phones === 1 ? 'phone' : 'phones'} reporting · ${f.viaMesh} via the mesh`;
        c.classList.toggle('relay', f.viaMesh > 0);
      }
      $('meshLayerWrap').hidden = !f || (f.links.length === 0 && jammed === 0);
      half.dataset.on = jammed > 0 ? '1' : '0';
      half.textContent = jammed > 0 ? `Restore connections (${jammed} jammed)` : 'Jam half the phones';
      half.classList.toggle('on', jammed > 0);
      $('jamMsg').textContent = !f || f.phones === 0
        ? 'No phones connected.'
        : f.virtual
          ? 'Simulated phones have no browsers: their links on the map are a stand-in, and only real phones can be jammed.'
          : `${f.links.length} phone-to-phone ${f.links.length === 1 ? 'link' : 'links'} open.`;
      renderDrawer();
    },
    /** One line for the map tooltip. */
    tip(id: string): string {
      const n = byId.get(id);
      if (!n) return '';
      return n.via ? `relayed via ${ctx.nameOf(n.via)} · ${n.hops ?? 1} ${(n.hops ?? 1) === 1 ? 'hop' : 'hops'}` : n.peers ? `direct · linked to ${n.peers} ${n.peers === 1 ? 'phone' : 'phones'}` : '';
    },
    renderDrawer,
  };
}
