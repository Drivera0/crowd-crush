// Settings → "Escalation": turn automatic escalation on or off and set how
// long a red alert may go unacknowledged before it is re-announced. The
// setting is the server's, so it applies to every console
// (GET/PUT /api/escalation).

type Escalation = { on: boolean; afterS: number };

export function initEscalation(toast: (text: string, kind?: 'info' | 'ok' | 'error') => void) {
  const on = document.getElementById('escOn') as HTMLInputElement | null;
  const after = document.getElementById('escAfter') as HTMLInputElement | null;
  const note = document.getElementById('escNote');
  if (!on || !after || !note) return;

  const show = (s: Escalation) => {
    on.checked = s.on;
    after.value = String(s.afterS);
    after.disabled = !s.on;
    note.textContent = s.on
      ? `A red alert nobody acknowledges within ${s.afterS} s is announced again and the sign flashes red.`
      : 'Off: red alerts are announced once. You can still press "Escalate now" on an alert card.';
  };

  const load = async () => {
    try {
      const r = await fetch('/api/escalation');
      if (r.ok) show((await r.json()) as Escalation);
    } catch {
      /* server older than this page: leave the controls as they are */
    }
  };

  const save = async () => {
    const afterS = Math.round(Number(after.value));
    if (!Number.isFinite(afterS) || afterS < 10 || afterS > 3600) {
      toast('Escalation wait must be between 10 and 3600 seconds.', 'error');
      void load();
      return;
    }
    try {
      const r = await fetch('/api/escalation', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ on: on.checked, afterS }),
      });
      if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error ?? r.statusText);
      const s = (await r.json()) as Escalation;
      show(s);
      toast(s.on ? `Escalation on: after ${s.afterS} s` : 'Escalation off', 'ok');
    } catch (e) {
      toast(`Couldn't change escalation: ${(e as Error).message}`, 'error');
      void load();
    }
  };

  on.addEventListener('change', () => void save());
  after.addEventListener('change', () => void save());
  void load();
}

/** POST /api/alerts/{id}/escalate: re-announce one alert now. */
export async function escalateNow(id: string): Promise<void> {
  const r = await fetch(`/api/alerts/${encodeURIComponent(id)}/escalate`, { method: 'POST' });
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error ?? r.statusText);
}
