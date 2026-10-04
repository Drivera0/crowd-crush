// Guided event setup: eight steps from naming the event to sharing the join
// QR, shown as a bar on every setup page (Back · Skip · Continue) and as the
// checklist on Home. Step completion is worked out from live state (venue,
// areas, hardware, flags), never stored as "done"; only skips are stored.

import { animate } from 'motion';
import { onPage, page, type Page } from './shell';

export type StepId = 'name' | 'size' | 'plan' | 'areas' | 'rules' | 'hardware' | 'drill' | 'qr';
export type StepStatus = 'done' | 'skipped' | 'todo';

/** Everything step completion depends on; main.ts supplies it fresh on each refresh. */
export interface SetupFacts {
  /** Venue, areas and hardware have each been fetched once (no celebrations before that). */
  loaded: boolean;
  named: boolean;
  eventName: string;
  template: boolean;
  /** Staff picked a template or saved a size themselves (the server's default size doesn't count). */
  sizeChosen: boolean;
  floorplan: boolean;
  areas: number;
  rules: boolean;
  hwOnline: number;
  hwTotal: number;
  drill: boolean;
  /** A real phone joined in live mode (simulated or replayed phones don't count). */
  joined: boolean;
  qrShown: boolean;
  /** The console's status sentence when something is wrong right now ("" when calm), e.g. "DANGER · Stage front · crowd push". */
  alarm: string;
  /** The alarm comes from a simulation or replay, not real phones. */
  alarmSimulated: boolean;
}

interface StepDef {
  id: StepId;
  title: string;
  sub: string;
  page: Page;
  optional: boolean;
  /** Controls to ring while this is the current step. */
  focus: string;
  /** What to do, shown in the bar. */
  hint: (f: SetupFacts) => string;
  done: (f: SetupFacts) => boolean;
  /** Status pill text on Home, when "Done" / "To do" isn't the honest word. */
  state?: (f: SetupFacts, st: StepStatus) => string | null;
}

export const STEPS: StepDef[] = [
  {
    id: 'name', title: 'Name your event', sub: 'Shown on the console and in staff briefings.', page: 'venue', optional: false,
    focus: '#flowName, .event-switch',
    hint: () => 'Type the event name here, then press Continue.',
    done: (f) => f.named,
  },
  {
    id: 'size', title: 'Choose the venue size', sub: 'Pick the closest template or type the size in metres.', page: 'venue', optional: false,
    focus: '#tplGrid',
    hint: () => 'Pick the template closest to your venue (you can fine-tune the metres below it and press Save size).',
    done: (f) => f.template && f.sizeChosen,
  },
  {
    id: 'plan', title: 'Upload a floor plan', sub: 'Shown under the map; AI reads the stage and exits from your plan.', page: 'venue', optional: true,
    focus: '#fpDrop',
    hint: () => 'Drop a PNG, JPG or WebP of the venue plan on the box, or skip this step.',
    done: (f) => f.floorplan,
  },
  {
    id: 'areas', title: 'Draw watch areas', sub: 'Barriers, gates, the stage front: spots your team knows get dangerous.', page: 'areas', optional: false,
    focus: '#addArea, #mapWrap .toolbar',
    hint: () => 'Click “+ Draw area”, then drag over a risky spot on the map.',
    done: (f) => f.areas > 0,
  },
  {
    id: 'rules', title: 'Set alert rules', sub: 'Crowding and capacity limits, and the message staff hear.', page: 'areas', optional: true,
    focus: '#areas > li.sel .rules-toggle, #areas:not(:has(> li.sel)) > li:first-child .rules-toggle',
    hint: (f) => (f.areas ? 'Open “Alert when…” on an area and set a limit or a message, or skip.' : 'Draw an area first, or skip this step.'),
    done: (f) => f.rules,
  },
  {
    id: 'hardware', title: 'Connect signs and zone lights', sub: 'Check they are online and place them on the map.', page: 'hardware', optional: true,
    focus: '#hwCard',
    hint: (f) =>
      f.hwTotal === 0
        ? 'No boards are configured. Signs and lights are optional: skip if you are not using any.'
        : f.hwOnline < f.hwTotal
          ? `${f.hwOnline} of ${f.hwTotal} online. Check the offline ones' power and Wi-Fi, or skip for now.`
          : 'All boards are online.',
    // Every configured board must answer: one offline sign is a sign that shows nothing.
    done: (f) => f.hwTotal > 0 && f.hwOnline === f.hwTotal,
    state: (f, st) => (st === 'todo' && f.hwTotal > 0 ? `${f.hwOnline} of ${f.hwTotal} online — fix` : st === 'done' ? `${f.hwTotal} online` : null),
  },
  {
    id: 'drill', title: 'Run a drill', sub: 'Send a test alert through briefing, voice, signs and lights.', page: 'drill', optional: false,
    focus: '#drillSend',
    hint: () => 'Choose where and what to test, then press “Send drill”. Everyone sees it marked DRILL.',
    done: (f) => f.drill,
    state: (_f, st) => (st === 'done' ? 'Drill sent' : null),
  },
  {
    id: 'qr', title: 'Share the join QR', sub: 'Attendees scan it and their phones start sharing crowd movement.', page: 'live', optional: false,
    focus: '#qrBtn',
    hint: (f) =>
      f.qrShown
        ? 'Put the QR on a screen or print it. This step is done when the first phone joins.'
        : 'Show the QR code attendees scan to join.',
    done: (f) => f.joined,
    state: (f, st) => (st === 'done' ? 'Phone joined' : f.qrShown ? 'Waiting for a phone' : null),
  },
];

const SETUP_PAGES: Page[] = ['venue', 'areas', 'hardware', 'drill', 'live'];
const SKIP_KEY = 'pulse.setup.skipped';
const EXIT_KEY = 'pulse.setup.exited';

function load<T>(k: string, d: T): T {
  try {
    const v = localStorage.getItem(k);
    return v ? (JSON.parse(v) as T) : d;
  } catch {
    return d;
  }
}
function store(k: string, v: unknown) {
  try {
    if (v === null) localStorage.removeItem(k);
    else localStorage.setItem(k, JSON.stringify(v));
  } catch {
    /* private mode: skips last for this visit only */
  }
}

const $ = <T extends HTMLElement = HTMLElement>(id: string) => document.getElementById(id) as T;
const esc = (s: string) => s.replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);

export interface SetupHooks {
  facts: () => SetupFacts;
  toast: (text: string, kind?: 'info' | 'ok' | 'error') => void;
  /** The name step's Continue: save this as the event name. */
  onName: (name: string) => void;
  /** The QR step: show the QR overlay. */
  onShowQR: () => void;
  /** Home's "Edit name": focus the sidebar's event field (false when it isn't visible). */
  onEditName: () => boolean;
  /** A step was just opened from the flow (open the rules box, focus a field…). */
  onEnter?: (id: StepId) => void;
}

export class Setup {
  private skipped = new Set<StepId>(load<StepId[]>(SKIP_KEY, []));
  private exited = load<boolean>(EXIT_KEY, false);
  private cursor: StepId | null = null;
  private baseline: Set<StepId> | null = null;
  private sig = '';
  private focused = new Set<Element>();
  private finishedNow = false;
  private f: SetupFacts;

  constructor(private hooks: SetupHooks) {
    this.f = hooks.facts();
    $('flowBack').addEventListener('click', () => this.back());
    $('flowSkip').addEventListener('click', () => this.skip());
    $('flowNext').addEventListener('click', () => this.next());
    $('flowExit').addEventListener('click', () => this.exit());
    $('flowName').addEventListener('keydown', (e) => e.key === 'Enter' && this.next());
    $('setupResume').addEventListener('click', () => (this.complete ? (location.hash = '#live') : this.resume()));
    onPage((p) => this.pageChanged(p));
  }

  status(id: StepId, f = this.f): StepStatus {
    const s = STEPS.find((x) => x.id === id)!;
    if (s.done(f)) return 'done';
    return s.optional && this.skipped.has(id) ? 'skipped' : 'todo';
  }

  get complete() {
    return STEPS.every((s) => this.status(s.id) !== 'todo');
  }

  private idx(id: StepId) {
    return STEPS.findIndex((s) => s.id === id);
  }

  /** First step still to do after `after` (wrapping round), or null when all are done/skipped. */
  private nextTodo(after?: StepId): StepId | null {
    const start = after ? this.idx(after) + 1 : 0;
    for (let k = 0; k < STEPS.length; k++) {
      const s = STEPS[(start + k) % STEPS.length];
      if (this.status(s.id) === 'todo') return s.id;
    }
    return null;
  }

  /** Re-evaluate every step. Cheap: the DOM is only touched when something changed. */
  refresh() {
    this.f = this.hooks.facts();
    const f = this.f;
    const done = new Set(STEPS.filter((s) => s.done(f)).map((s) => s.id));
    if (f.loaded) {
      if (this.baseline) {
        const wasComplete = STEPS.every((s) => this.baseline!.has(s.id) || (s.optional && this.skipped.has(s.id)));
        for (const s of STEPS) if (done.has(s.id) && !this.baseline.has(s.id)) this.celebrate(s);
        this.baseline = done;
        if (!wasComplete && this.complete) this.finish();
      } else {
        this.baseline = done;
        // First time the real state is known: re-pick the current step (it was chosen from defaults).
        if (!this.cursor || this.status(this.cursor) !== 'todo') {
          const p = page();
          this.cursor = STEPS.find((s) => s.page === p && this.status(s.id) === 'todo')?.id ?? this.nextTodo() ?? this.cursor;
        }
      }
    }
    this.cursor ??= this.nextTodo() ?? STEPS[STEPS.length - 1].id;
    this.render();
  }

  // ---------------------------------------------------------------- actions

  /** Open a step: its page, highlighted, as the current step. */
  go(id: StepId) {
    this.cursor = id;
    this.finishedNow = false;
    if (this.exited) {
      this.exited = false;
      store(EXIT_KEY, null);
    }
    const s = STEPS[this.idx(id)];
    if (page() !== s.page) location.hash = `#${s.page}`;
    this.sig = '';
    this.render();
    window.setTimeout(() => this.enter(id), 220);
  }

  /**
   * Start the guided setup again from step 1: skips and the paused state are forgotten (main.ts clears the
   * flags the steps read). Steps that are still true (areas exist, boards online) are done again, without fanfare.
   */
  reset() {
    this.skipped.clear();
    store(SKIP_KEY, null);
    this.exited = false;
    store(EXIT_KEY, null);
    this.cursor = null;
    this.baseline = null;
    this.finishedNow = false;
    this.sig = '';
    this.refresh();
  }

  /** From Home: jump to any step (a skipped one becomes "to do" again). */
  jump(id: StepId) {
    if (this.skipped.delete(id)) store(SKIP_KEY, [...this.skipped]);
    this.go(id);
  }

  resume() {
    this.go(this.nextTodo() ?? STEPS[0].id);
  }

  private enter(id: StepId) {
    if (id === 'name') {
      const input = $<HTMLInputElement>('flowName');
      input.focus();
      input.select();
    }
    this.hooks.onEnter?.(id);
    const target = document.querySelector<HTMLElement>('.setup-focus:not(.event-switch):not(#flowName)');
    target?.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
  }

  private back() {
    const i = this.idx(this.cursor!);
    if (i > 0) this.go(STEPS[i - 1].id);
  }

  private skip() {
    const id = this.cursor!;
    this.skipped.add(id);
    store(SKIP_KEY, [...this.skipped]);
    this.refresh();
    this.advance(id);
  }

  private next() {
    const s = STEPS[this.idx(this.cursor!)];
    if (this.finishedNow || (this.complete && this.status(s.id) !== 'todo')) {
      location.hash = '#live';
      return;
    }
    if (this.status(s.id) === 'todo') {
      if (page() !== s.page) return this.go(s.id);
      if (s.id === 'name') {
        const v = $<HTMLInputElement>('flowName').value.trim();
        if (!v) return this.nudge('Type a name for the event first.');
        this.hooks.onName(v);
      } else if (s.id === 'qr') {
        const again = this.f.qrShown;
        this.hooks.onShowQR();
        return again ? this.nudge('Waiting for the first phone to join. Scan the QR with any phone to test it.') : undefined;
      } else if (s.optional) {
        return this.skip();
      } else {
        return this.nudge();
      }
      this.refresh();
      if (this.status(s.id) === 'todo') return;
    }
    this.advance(s.id);
  }

  private advance(from: StepId) {
    const n = this.nextTodo(from);
    if (n) this.go(n);
    else {
      if (!this.finishedNow) this.finish();
      this.render();
    }
  }

  private exit() {
    this.exited = true;
    store(EXIT_KEY, true);
    this.hooks.toast('Setup paused. Pick it up any time from Home.');
    this.render();
  }

  private pageChanged(p: Page) {
    // The "setup complete" bar stays only while moving between setup pages.
    if (!SETUP_PAGES.includes(p) || p === 'live') this.finishedNow = false;
    // Arriving on a page from the sidebar: if it holds a step still to do, that becomes the current step.
    const cur = this.cursor ? STEPS[this.idx(this.cursor)] : null;
    if (!cur || cur.page !== p) {
      const here = STEPS.find((s) => s.page === p && this.status(s.id) === 'todo');
      if (here) this.cursor = here.id;
    }
    this.sig = '';
    this.render();
  }

  private nudge(text?: string) {
    const s = STEPS[this.idx(this.cursor!)];
    const hint = $('flowHint');
    hint.textContent = text ?? `${s.hint(this.f)} This step is needed before going live.`;
    animate(hint, { x: [0, -6, 6, -4, 4, 0] }, { duration: 0.4 });
    for (const el of this.focused) {
      (el as HTMLElement).scrollIntoView({ block: 'nearest', behavior: 'smooth' });
      animate(el as HTMLElement, { scale: [1, 1.04, 1] }, { duration: 0.35 });
    }
  }

  private celebrate(s: StepDef) {
    this.hooks.toast(`${s.title}: done`, 'ok');
    this.sig = '';
    this.render();
    const tick = $('flowTick');
    if (!$('flowBar').hidden && this.cursor === s.id) {
      animate(tick, { scale: [0.4, 1.25, 1], rotate: [-20, 0] }, { type: 'spring', bounce: 0.55, duration: 0.6 });
      animate($('flowNext'), { scale: [1, 1.08, 1] }, { duration: 0.5, delay: 0.25 });
    }
    const li = document.querySelector<HTMLElement>(`#checklist [data-step="${s.id}"] .tick`);
    if (li && page() === 'home') animate(li, { scale: [0.4, 1.25, 1] }, { type: 'spring', bounce: 0.55, duration: 0.6 });
  }

  private finish() {
    this.hooks.toast('Setup complete: your event is ready to go live.', 'ok');
    this.finishedNow = true;
    this.sig = '';
  }

  // ---------------------------------------------------------------- rendering

  private render() {
    const f = this.f;
    const p = page();
    const statuses = STEPS.map((s) => this.status(s.id)).join(',');
    this.cursor ??= this.nextTodo() ?? STEPS[STEPS.length - 1].id;
    const sig = [statuses, this.cursor, p, this.exited, this.finishedNow, f.qrShown, f.hwTotal, f.hwOnline, f.areas > 0, f.eventName, f.alarm, f.alarmSimulated].join('|');
    this.applyFocus();
    if (sig === this.sig) return;
    this.renderBar(p);
    this.renderHome();
    this.sig = sig; // only after a successful render, so a failure retries next time
    this.applyFocus();
  }

  private barVisible(p: Page) {
    if (this.finishedNow) return SETUP_PAGES.includes(p) && p !== 'live';
    if (this.exited || this.complete || !SETUP_PAGES.includes(p)) return false;
    return p !== 'live' || this.cursor === 'qr';
  }

  private renderBar(p: Page) {
    const f = this.f;
    const bar = $('flowBar');
    const show = this.barVisible(p);
    const wasHidden = bar.hidden;
    bar.hidden = !show;
    document.body.classList.toggle('in-setup', show);
    if (!show) return;
    if (wasHidden) animate(bar, { opacity: [0, 1], y: [-8, 0] }, { duration: 0.25 });

    const settled = STEPS.filter((s) => this.status(s.id) !== 'todo').length;
    $('flowFill').style.width = `${(settled / STEPS.length) * 100}%`;

    const back = $<HTMLButtonElement>('flowBack');
    const skip = $<HTMLButtonElement>('flowSkip');
    const next = $<HTMLButtonElement>('flowNext');
    const name = $<HTMLInputElement>('flowName');
    const tick = $('flowTick');

    if (this.finishedNow) {
      bar.className = 'flow-bar finished';
      $('flowStep').textContent = `All ${STEPS.length} steps`;
      $('flowTitle').textContent = 'Setup complete: your event is ready';
      $('flowHint').textContent = 'Open Live when doors open. Everything stays editable from these pages.';
      tick.textContent = '✓';
      back.hidden = skip.hidden = name.hidden = true;
      next.textContent = 'Go live →';
      next.classList.add('ready');
      $('flowExit').hidden = true;
      return;
    }
    $('flowExit').hidden = false;

    const i = this.idx(this.cursor!);
    const s = STEPS[i];
    const st = this.status(s.id);
    const here = s.page === p;
    bar.className = `flow-bar ${st}`;
    $('flowStep').textContent = `Step ${i + 1} of ${STEPS.length}${s.optional ? ' · optional' : ''}`;
    $('flowTitle').textContent = s.title;
    tick.textContent = st === 'done' ? '✓' : st === 'skipped' ? '–' : String(i + 1);
    back.hidden = false;
    back.disabled = i === 0;
    skip.hidden = !(s.optional && st === 'todo' && here);
    name.hidden = !(s.id === 'name' && st === 'todo');
    if (!name.hidden && document.activeElement !== name) name.value = f.eventName;

    const upcoming = this.nextTodo(s.id);
    const nextTitle = upcoming ? STEPS[this.idx(upcoming)].title : '';
    let label = 'Continue →';
    let hint = s.hint(f);
    if (st === 'done' || st === 'skipped') {
      hint = `${st === 'done' ? 'Done.' : 'Skipped.'} ${upcoming ? `Next: ${nextTitle}.` : 'That was the last step.'}`;
      label = upcoming ? 'Continue →' : 'Finish setup ✓';
    } else if (!here) {
      hint = `${s.hint(f)}`;
      label = `Go to ${pageName(s.page)} →`;
    } else if (s.id === 'name') {
      label = 'Save and continue →';
    } else if (s.id === 'qr') {
      label = f.qrShown ? 'Show the QR again' : 'Show the QR';
    }
    $('flowHint').textContent = hint;
    next.textContent = label;
    next.classList.toggle('ready', st !== 'todo');
    next.classList.toggle('waiting', st === 'todo' && here && !s.optional && s.id !== 'name' && s.id !== 'qr');
  }

  private renderHome() {
    const f = this.f;
    const settled = STEPS.filter((s) => this.status(s.id) !== 'todo').length;
    const anyStarted = STEPS.some((s) => this.status(s.id) !== 'todo');
    const nextId = this.nextTodo();
    const complete = !nextId;
    $('setupCard').classList.toggle('complete', complete);
    // "Ready" must never sit quietly next to a Danger status: say so.
    const alarm = $('setupAlarm');
    alarm.hidden = !f.alarm;
    alarm.className = `setup-alarm${f.alarm.startsWith('DANGER') ? ' red' : ''}`;
    alarm.textContent = f.alarm
      ? `Right now: ${f.alarm}${f.alarmSimulated ? ' (from a simulation or replay, not real phones)' : ''}. Check Live before opening doors.`
      : '';
    $('setupHeadline').textContent = complete ? 'Your event is ready' : 'Event setup';
    $('setupLead').textContent = complete
      ? `All ${STEPS.length} steps are done or skipped. Go live when doors open.`
      : `${settled} of ${STEPS.length} steps done · next: ${STEPS[this.idx(nextId)].title}`;
    $('setupResume').textContent = complete ? 'Go live →' : anyStarted ? 'Continue setup →' : 'Start setup →';
    $('setupBar').style.width = `${(settled / STEPS.length) * 100}%`;
    $('checklist').replaceChildren(
      ...STEPS.map((s, i) => {
        const st = this.status(s.id, f);
        const li = document.createElement('li');
        li.className = `${st}${s.id === nextId ? ' next' : ''}`;
        li.dataset.step = s.id;
        const custom = s.state?.(f, st) ?? null;
        const partial = s.id === 'hardware' && st === 'todo' && f.hwTotal > 0;
        const label =
          st === 'done'
            ? 'Review'
            : st === 'skipped'
              ? 'Do it now'
              : s.id === 'name'
                ? 'Edit name'
                : partial
                  ? 'Fix →'
                  : s.id === nextId
                    ? 'Start →'
                    : 'Go to step';
        const cls = st === 'done' ? 'sm ghost' : s.id === nextId ? 'sm primary' : 'sm';
        li.innerHTML =
          `<span class="tick">${st === 'done' ? '✓' : st === 'skipped' ? '–' : i + 1}</span>` +
          `<span class="ck-text"><span class="ck-title">${esc(s.title)}${s.optional ? '<span class="opt">optional</span>' : ''}</span>` +
          `<span class="ck-sub">${esc(s.sub)}</span></span>` +
          `<span class="ck-state ${partial ? 'warn' : st}">${esc(custom ?? (st === 'done' ? 'Done' : st === 'skipped' ? 'Skipped' : 'To do'))}</span>` +
          `<button class="${cls}">${label}</button>`;
        li.querySelector('button')!.addEventListener('click', () => {
          // The name lives in the sidebar: focus it there when it's visible.
          if (s.id === 'name' && this.hooks.onEditName()) return;
          this.jump(s.id);
        });
        return li;
      }),
    );
  }

  /** Ring the controls for the current step, on its page, while it is still to do. */
  private applyFocus() {
    const want = new Set<Element>();
    const bar = $('flowBar');
    if (!bar.hidden && !this.finishedNow && this.cursor) {
      const s = STEPS[this.idx(this.cursor)];
      if (s.page === page() && this.status(s.id) === 'todo') {
        for (const el of document.querySelectorAll(s.focus)) if (!(el as HTMLElement).hidden) want.add(el);
      }
    }
    for (const el of this.focused) if (!want.has(el)) el.classList.remove('setup-focus');
    for (const el of want) if (!this.focused.has(el)) el.classList.add('setup-focus');
    this.focused = want;
  }
}

function pageName(p: Page) {
  return { home: 'Home', live: 'Live', venue: 'Venue', areas: 'Areas & alerts', hardware: 'Hardware', sim: 'Simulation', drill: 'Alert drill', settings: 'Settings' }[p];
}
