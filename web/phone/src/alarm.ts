// The danger alarm: a short tone and a red flash of the whole screen. iPhones
// can't vibrate from a web page, so this is how they get the person's
// attention; Android gets it too, on top of the buzz.
//
// Browsers only let a page make sound after a tap: unlockAlarm runs inside
// the Join tap and creates (or resumes) the audio context then. Any audio
// failure is ignored: the flash still shows.

let ctx: AudioContext | null = null;

/** Call inside the Join tap, before any await. */
export function unlockAlarm() {
  try {
    // Safari 17+: play through the mute switch, like a media app.
    const nav = navigator as Navigator & { audioSession?: { type: string } };
    if ('audioSession' in nav && nav.audioSession) nav.audioSession.type = 'playback';
    const AC = window.AudioContext ?? (window as unknown as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext;
    if (!AC) return;
    ctx ??= new AC();
    void ctx.resume().catch(() => {});
    // A silent blip unlocks output on older iOS.
    const g = ctx.createGain();
    g.gain.value = 0;
    const o = ctx.createOscillator();
    o.connect(g).connect(ctx.destination);
    o.start();
    o.stop(ctx.currentTime + 0.01);
  } catch {
    /* no sound: the flash still works */
  }
}

/** Sound the alarm and flash the screen red. */
export function alarm() {
  flash();
  try {
    if (!ctx) return;
    void ctx.resume().catch(() => {});
    const t = ctx.currentTime + 0.02;
    // Three falling two-tone beeps, about 1.3 s in all.
    for (let i = 0; i < 3; i++) {
      for (const [j, f] of [988, 740].entries()) {
        const at = t + i * 0.45 + j * 0.18;
        const o = ctx.createOscillator();
        const g = ctx.createGain();
        o.type = 'square';
        o.frequency.value = f;
        g.gain.setValueAtTime(0.0001, at);
        g.gain.exponentialRampToValueAtTime(0.5, at + 0.02);
        g.gain.exponentialRampToValueAtTime(0.0001, at + 0.16);
        o.connect(g).connect(ctx.destination);
        o.start(at);
        o.stop(at + 0.17);
      }
    }
  } catch {
    /* no sound: the flash still shows */
  }
}

function flash() {
  const el = document.createElement('div');
  el.className = 'alarm-flash';
  el.setAttribute('aria-hidden', 'true');
  document.body.append(el);
  setTimeout(() => el.remove(), 1600);
}
