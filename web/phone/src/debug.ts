// ?debug=1 on the phone page: a panel with everything needed to work out
// why a phone isn't streaming, readable at the table without a laptop.
// Loaded only with ?debug=1 (a dynamic import from main.ts).

type Info = Record<string, unknown> & {
  fields: { acc: boolean; accG: boolean; rot: boolean; interval: number };
  state: unknown;
};

const esc = (s: string) => s.replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);

export function startDebug(read: () => Info) {
  const box = document.createElement('div');
  box.id = 'debugPanel';
  box.setAttribute('role', 'status');
  box.style.cssText =
    'position:fixed;left:0;right:0;bottom:0;z-index:2000;max-height:45vh;overflow:auto;background:rgba(0,0,0,.88);color:#cbd5e1;' +
    'font:11px/1.35 ui-monospace,Menlo,Consolas,monospace;padding:8px 10px calc(8px + env(safe-area-inset-bottom));border-top:1px solid #334155;text-align:left';
  const head = document.createElement('div');
  head.style.cssText = 'display:flex;justify-content:space-between;align-items:center;margin-bottom:4px;color:#fff;font-weight:700';
  head.innerHTML = '<span>Pulse debug</span>';
  const btns = document.createElement('span');
  const copy = document.createElement('button');
  copy.textContent = 'Copy';
  const hide = document.createElement('button');
  hide.textContent = 'Hide';
  for (const b of [copy, hide]) b.style.cssText = 'font:inherit;padding:2px 8px;margin-left:6px;border-radius:6px;background:#1e293b;color:#fff;border:0';
  btns.append(copy, hide);
  head.append(btns);
  const body = document.createElement('div');
  box.append(head, body);
  document.body.append(box);
  document.getElementById('stats')?.removeAttribute('hidden');

  let text = '';
  const render = () => {
    const d = read();
    const f = d.fields;
    const st = d.state as { node?: string; zone?: string; x?: number; y?: number; row?: { n: number }; name?: string } | null;
    const rows: [string, string, boolean?][] = [
      ['id · browser', `${d.id} · ${d.browser}`],
      ['secure context', String(d.secure), !d.secure],
      ['motion permission', String(d.motionPerm), /denied|error|no API/.test(String(d.motionPerm))],
      ['orientation permission', String(d.orientPerm)],
      ['sensor rate', `${d.hz} Hz (${d.samples} samples, ${d.sent} sent)`, d.hz === 0],
      ['sensor fields', `acc ${f.acc ? 'yes' : 'NO'} · accG ${f.accG ? 'yes' : 'NO'} · rotation ${f.rot ? 'yes' : 'NO'} · interval ${f.interval}`],
      ['compass', String(d.compass)],
      ['wake lock', String(d.wakeLock), String(d.wakeLock) !== 'held'],
      ['socket', `${d.socket} · last rx ${d.lastRx === -1 ? 'never' : `${d.lastRx} s ago`} · opened ${d.opens}× · failed ${d.fails}×`, d.socket !== 'open'],
      ['last close', String(d.lastClose || '—')],
      ['clock offset', `${d.clockOffset} ms (phone − server)`],
      ['server state', st ? `${st.node}/${st.zone} at ${st.x}, ${st.y}${st.row ? ` · row #${st.row.n}` : ''}${st.name ? ` · ${st.name}` : ''}` : 'none yet', !st],
      ['mesh links', String(d.mesh)],
      ['config · mode', `${JSON.stringify(d.cfg)} · ${d.mode}`],
      ['last error', String(d.lastError || '—'), !!d.lastError],
    ];
    text = rows.map(([k, v]) => `${k}: ${v}`).join('\n');
    body.innerHTML = rows
      .map(([k, v, bad]) => `<div><span style="color:#64748b">${esc(k)}</span> <span style="color:${bad ? '#fca5a5' : '#e2e8f0'}">${esc(v)}</span></div>`)
      .join('');
  };
  copy.addEventListener('click', async () => {
    try {
      await navigator.clipboard.writeText(`${text}\nua: ${navigator.userAgent}`);
      copy.textContent = 'Copied';
    } catch {
      copy.textContent = 'Copy failed';
    }
  });
  hide.addEventListener('click', () => {
    body.hidden = !body.hidden;
    hide.textContent = body.hidden ? 'Show' : 'Hide';
  });
  render();
  setInterval(render, 500);
}
