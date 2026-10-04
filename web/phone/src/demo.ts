// The judge demo on the phone: "You are Blue Otter", the shake
// acknowledgement, the drill label while a simulated crowd drives this
// phone's state, and the Leave button with its privacy receipt.

import './demo.css';
import type { PhoneState } from '../../shared/protocol';
import type { Receipt } from '../../shared/demo';

const $ = <T extends HTMLElement = HTMLElement>(id: string) => document.getElementById(id) as T;

const esc = (s: string) => s.replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);
/** Only a plain hex colour from the server is ever put into a style. */
const safeColor = (c?: string) => (c && /^#[0-9a-f]{3,8}$/i.test(c) ? c : '#38bdf8');

let name = '';
let color = '';

/** Every state message: the phone's name and colour, and whether a simulation is driving it. */
export function demoState(s: PhoneState) {
  if (s.name && (s.name !== name || s.color !== color)) {
    name = s.name;
    color = safeColor(s.color);
    $('iamName').textContent = name;
    $('iam').style.setProperty('--me', color);
    $('iam').hidden = false;
  }
  const sim = !!s.sim;
  $('drill').hidden = !sim;
  $('guideDrill').hidden = !sim;
  document.body.classList.toggle('drill', sim);
}

let shakeTimer = 0;

/** The server saw this phone being shaken: a short buzz where the browser can (Android), a flash everywhere. */
export function demoShake() {
  try {
    navigator.vibrate?.(60);
  } catch {
    /* not allowed before a tap, or not supported (iPhone): the flash is enough */
  }
  const fx = $('shakeFx');
  fx.style.setProperty('--me', color || '#38bdf8');
  fx.hidden = false;
  fx.classList.remove('go');
  void fx.offsetWidth; // restart the animation
  fx.classList.add('go');
  window.clearTimeout(shakeTimer);
  shakeTimer = window.setTimeout(() => (fx.hidden = true), 1400);
}

function duration(s: number) {
  const m = Math.floor(s / 60);
  return m > 0 ? `${m} min ${s % 60} s` : `${s} s`;
}

export interface LeaveCtx {
  /** The full random session id (only this phone knows it). */
  id: string;
  /** "iPhone", "Android", … as sent to the server. */
  device: string;
  /** Motion summaries this page sent. */
  sent: () => number;
  /** Close the connection and stop the sensors, for good. */
  leave: () => void;
}

/** Leave: fetch what the server holds, close the connection, show the receipt. */
export function initLeave(ctx: LeaveCtx) {
  $('leaveBtn').addEventListener('click', async () => {
    const btn = $('leaveBtn') as HTMLButtonElement;
    btn.disabled = true;
    let r: Receipt | null = null;
    try {
      const res = await fetch(`/api/receipt/${encodeURIComponent(ctx.id)}`, { cache: 'no-store' });
      if (res.ok) r = (await res.json()) as Receipt;
    } catch {
      /* offline: the receipt is written from what this page knows */
    }
    ctx.leave();
    showReceipt(ctx, r);
  });
  $('rejoinBtn').addEventListener('click', () => location.reload());
}

function showReceipt(ctx: LeaveCtx, r: Receipt | null) {
  for (const s of ['join', 'locate', 'place', 'live']) $(s).hidden = true;
  $('guide').hidden = true;
  document.body.className = '';
  const rows: [string, string][] = [];
  rows.push(['Random session ID', `${esc(r?.id ?? ctx.id.slice(0, 8))}… <small>made up by this page when you joined; not linked to you or your phone number</small>`]);
  if (r?.name ?? name) {
    rows.push(['Generated name', `<b style="color:${safeColor(r?.color ?? color)}">${esc(r?.name ?? name)}</b> <small>worked out from that ID so you could find your dot</small>`]);
  }
  rows.push(['Device type', `${esc(ctx.device)} <small>just that one word</small>`]);
  if (r) {
    rows.push([
      'Position in the room',
      r.src === 'none'
        ? 'None <small>you were never placed on the venue map, so your motion numbers counted toward nothing and were not stored</small>'
        : `${r.x.toFixed(1)} m, ${r.y.toFixed(1)} m on the venue map <small>${r.src === 'gps' ? 'from GPS, turned into metres in the room on arrival' : r.src === 'tower' ? 'next to the spot whose check-in code you scanned' : 'placed on the map (by you or by staff)'}</small>`,
    ]);
    rows.push([
      'Motion numbers',
      `${r.messages.toLocaleString()} summaries <small>ten a second: average acceleration on three axes, how fast the phone turned, and which way is down</small>`,
    ]);
    rows.push(['Time connected', duration(r.seconds)]);
  } else {
    rows.push(['Motion numbers', `${ctx.sent().toLocaleString()} summaries sent <small>the server couldn't be reached for its own count</small>`]);
  }
  $('rcCollected').innerHTML = rows.map(([k, v]) => `<dt>${k}</dt><dd>${v}</dd>`).join('');

  const kept: string[] = [];
  if (r) {
    kept.push(`The server's memory holds your last ${r.kept.toLocaleString()} readings for the live map. They are dropped ${r.forgetS} seconds after you leave.`);
    if (r.stored && r.src !== 'none') {
      kept.push(`Your motion numbers and room position were also saved to ${esc(r.store ?? 'storage on the server')}, under the random ID above, so a run can be replayed and the detector tuned.`);
    } else {
      kept.push('Nothing was written to long-term storage.');
    }
    if (r.recorded) kept.push('A labelled recording was running while you were connected: the same numbers are in that recording file.');
  } else {
    kept.push('The server could not be reached, so this page cannot say what it stored. While it runs it keeps motion numbers and room positions under the random ID, never anything else.');
  }
  $('rcKept').innerHTML = kept.map((t) => `<li>${t}</li>`).join('');
  $('receipt').hidden = false;
  window.scrollTo(0, 0);
}
