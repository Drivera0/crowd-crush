// "Near Zone light A" beside a phone's dot while the server has it beside a
// board it was walked up to (node.near: the Bluetooth snap, server
// app/beaconsnap.go). Drawn by mesh.ts on top of the dots, in world px,
// at a fixed size on screen (view zoom k).

export interface NearBody {
  /** Where the dot is drawn now (world px). */
  x: number;
  y: number;
  vis: number;
  data: { near?: string };
}

export function drawNearLabels(g: CanvasRenderingContext2D, bodies: Iterable<NearBody>, nodeR: number, k: number, light: boolean) {
  g.save();
  const fs = 13 / k;
  g.font = `600 ${fs}px Inter, system-ui, sans-serif`;
  g.textAlign = 'left';
  g.textBaseline = 'middle';
  g.lineJoin = 'round';
  for (const b of bodies) {
    if (!b.data.near || b.vis <= 0.3) continue;
    const text = `📍 Near ${b.data.near}`;
    const x = b.x + nodeR + 5 / k;
    g.globalAlpha = Math.min(1, b.vis);
    g.lineWidth = 3 / k;
    g.strokeStyle = light ? 'rgba(255,255,255,0.95)' : 'rgba(15,23,42,0.95)';
    g.strokeText(text, x, b.y);
    g.fillStyle = light ? '#0369a1' : '#7dd3fc';
    g.fillText(text, x, b.y);
  }
  g.restore();
}
