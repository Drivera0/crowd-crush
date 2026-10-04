// Which phone and browser this page is running in, what that browser can
// do, and the exact words to fix each way joining can fail. Also reports a
// failed join to the server (POST /api/join/report) so the dashboard can say
// "3 joined · 1 couldn't get motion: in-app browser". Only the random
// session id, a reason code and the browser family are sent.

const ua = navigator.userAgent;

export const isIOS = /iPhone|iPad|iPod/.test(ua) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1);
export const isAndroid = /Android/.test(ua);
export const isMobile = isIOS || isAndroid || /Mobi/.test(ua);
/** Inside the Pulse Android app (a WebView with window.PulseNative): not an "in-app browser" to escape from. */
export const isPulseApp = /PulseApp\//.test(ua) || !!(window as unknown as { PulseNative?: unknown }).PulseNative;

export type Browser =
  | 'safari'
  | 'chrome'
  | 'samsung'
  | 'firefox'
  | 'edge'
  | 'opera'
  | 'inapp'
  | 'webview'
  | 'pulseapp'
  | 'other';

/** In-app browsers by their user-agent marks: [pattern, name]. */
const IN_APP: [RegExp, string][] = [
  [/Instagram/, 'Instagram'],
  [/FBAN|FBAV|FB_IAB|FBIOS/, 'Facebook'],
  [/Messenger|MessengerForiOS/, 'Messenger'],
  [/LinkedInApp/, 'LinkedIn'],
  [/Twitter|TwitterAndroid/, 'X'],
  [/Snapchat/, 'Snapchat'],
  [/musical_ly|BytedanceWebview|TikTok/, 'TikTok'],
  [/MicroMessenger/, 'WeChat'],
  [/\bLine\//, 'LINE'],
  [/Discord/, 'Discord'],
  [/Slack/, 'Slack'],
  [/GSA\//, 'the Google app'],
  [/Gmail|GoogleApp/, 'Gmail'],
  [/Pinterest/, 'Pinterest'],
  [/KAKAOTALK/, 'KakaoTalk'],
];

function detect(): { kind: Browser; app: string; version: number } {
  const v = (re: RegExp) => Number(ua.match(re)?.[1] ?? 0);
  if (isPulseApp) return { kind: 'pulseapp', app: 'Pulse app', version: 0 };
  for (const [re, name] of IN_APP) if (re.test(ua)) return { kind: 'inapp', app: name, version: 0 };
  if (/SamsungBrowser/.test(ua)) return { kind: 'samsung', app: 'Samsung Internet', version: v(/SamsungBrowser\/(\d+)/) };
  if (/EdgA|EdgiOS|Edg\//.test(ua)) return { kind: 'edge', app: 'Edge', version: v(/Edg(?:A|iOS)?\/(\d+)/) };
  if (/OPR\/|OPiOS|OPT\//.test(ua)) return { kind: 'opera', app: 'Opera', version: 0 };
  if (/FxiOS|Firefox\//.test(ua)) return { kind: 'firefox', app: 'Firefox', version: v(/(?:FxiOS|Firefox)\/(\d+)/) };
  if (/CriOS/.test(ua)) return { kind: 'chrome', app: 'Chrome', version: v(/CriOS\/(\d+)/) };
  // Android WebView ("; wv)") or an iOS WKWebView without "Safari/" in it: some app's built-in browser.
  if (isAndroid && /; wv\)/.test(ua)) return { kind: 'webview', app: 'an app’s built-in browser', version: 0 };
  if (/Chrome\//.test(ua)) return { kind: 'chrome', app: 'Chrome', version: v(/Chrome\/(\d+)/) };
  if (isIOS && !/Safari\//.test(ua)) return { kind: 'webview', app: 'an app’s built-in browser', version: 0 };
  if (/Safari\//.test(ua)) return { kind: 'safari', app: 'Safari', version: v(/Version\/(\d+)/) };
  return { kind: 'other', app: 'this browser', version: 0 };
}

export const browser = detect();
/** iOS version (e.g. 17.4), 0 if not an iPhone/iPad or unknown. Every iOS browser is Safari's engine. */
export const iosVersion = (() => {
  const m = ua.match(/OS (\d+)[_.](\d+)/);
  return isIOS && m ? Number(m[1]) + Number(m[2]) / 10 : 0;
})();
/** An in-app or unknown embedded browser: motion may be refused, and the fix is opening a real browser. */
export const embedded = browser.kind === 'inapp' || browser.kind === 'webview';

/** "iOS Safari 17", "Instagram (Android)": the browser family for reports, nothing more. */
export function browserLabel(): string {
  const os = isIOS ? 'iOS' : isAndroid ? 'Android' : 'desktop';
  if (browser.kind === 'inapp' || browser.kind === 'webview') return `${browser.kind === 'inapp' ? browser.app : 'in-app'} (${os})`.slice(0, 40);
  if (browser.kind === 'pulseapp') return 'Pulse app';
  const ver = isIOS && iosVersion ? ` ${Math.floor(iosVersion)}` : browser.version ? ` ${browser.version}` : '';
  return `${os} ${browser.app}${ver}`.slice(0, 40);
}

/** Where a real browser is on this phone, for "open this in …". */
export const realBrowser = isIOS ? 'Safari' : isAndroid ? 'Chrome' : 'a browser on your phone';

/** How to get from this in-app browser to a real one. */
export function openOutHelp(): string {
  const app = browser.kind === 'inapp' ? browser.app : 'this app';
  if (isIOS) {
    return `You opened this inside ${app}, which may not share the motion sensors. Tap ••• or the compass/share icon and choose “Open in Safari”, or copy the link and paste it into Safari.`;
  }
  return `You opened this inside ${app}, which may not share the motion sensors. Tap ⋮ and choose “Open in Chrome” (or “Open in browser”), or copy the link into Chrome.`;
}

/** Android: an intent link that opens this page in Chrome from inside an in-app browser. */
export function chromeIntent(href: string): string | null {
  if (!isAndroid) return null;
  try {
    const u = new URL(href);
    return `intent://${u.host}${u.pathname}${u.search}#Intent;scheme=${u.protocol.replace(':', '')};package=com.android.chrome;S.browser_fallback_url=${encodeURIComponent(href)};end`;
  } catch {
    return null;
  }
}

/** The per-browser words for each failure. */
export const help = {
  insecure(): string {
    const h = location.hostname;
    const local = h === 'localhost' || /^(127\.|10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.)/.test(h) || h.endsWith('.local');
    return local
      ? `This address (${location.host}) is plain http, so the phone won’t share its motion sensors. Scan the QR code on the screen instead: it starts with https://.`
      : `This page was opened over http://, so the phone won’t share its motion sensors. Open https://${location.host}${location.pathname} instead, or scan the QR code on the screen.`;
  },
  motionDenied(): string {
    if (embedded) return openOutHelp();
    if (isIOS)
      return 'Motion access was refused. Tap aA in the address bar → Website Settings → Motion & Orientation Access → Allow, then tap Join again. (Older iPhones: close this tab, open the link again and tap Allow.)';
    if (browser.kind === 'samsung') return 'Motion sensors are blocked. ⋮ → Settings → Sites and downloads → Site permissions → Motion sensors → Allow, then reload.';
    return 'Motion sensors are blocked for this site. Tap the icon left of the address → Permissions → Motion sensors → Allow, then reload.';
  },
  permError(e: unknown): string {
    const msg = e instanceof Error ? e.message : String(e);
    if (/gesture|activation/i.test(msg)) return 'The phone wants the Join button tapped directly. Tap Join again.';
    if (embedded) return openOutHelp();
    return `The phone wouldn’t ask for motion access (${msg.slice(0, 80)}). Open the link in ${realBrowser} and tap Join again.`;
  },
  noSensorApi(): string {
    if (!isMobile) return 'This device has no motion sensor. Open the link on a phone: scan the QR code on the screen.';
    if (embedded) return openOutHelp();
    return `This browser doesn’t give pages motion data. Open the link in ${realBrowser}.`;
  },
  noMotion(): string {
    if (!isMobile) return 'No motion data: this looks like a laptop or desktop. Open the link on a phone.';
    if (embedded) return openOutHelp();
    if (isIOS) return 'No motion data yet. Tap aA → Website Settings → allow Motion & Orientation Access, then reload. Low Power Mode can also pause the sensors.';
    if (browser.kind === 'samsung') return 'No motion data. ⋮ → Settings → Sites and downloads → Site permissions → Motion sensors → Allow, then reload.';
    if (browser.kind === 'firefox') return 'Firefox isn’t giving this page motion data. Open the link in Chrome instead.';
    return 'No motion data. Tap the icon left of the address → Permissions → Motion sensors → Allow, then reload.';
  },
  socket(): string {
    return 'The page loaded but can’t hold a live connection to the server. Switch between Wi-Fi and mobile data, then reload.';
  },
  screen(): string {
    if (isIOS && iosVersion > 0 && iosVersion < 16.4) {
      return 'Keep this page open with the screen on: this iPhone can’t keep it awake by itself. Settings → Display & Brightness → Auto-Lock → Never helps while you stand here.';
    }
    return isIOS ? 'Keep this page open with the screen on. iPhones pause web pages when locked (Low Power Mode makes it lock sooner).' : 'Keep this page open with the screen on.';
  },
};

export type { JoinReason } from '../../shared/join';
import type { JoinReason } from '../../shared/join';

const sentReasons = new Set<string>();

/** Tell the server how joining went (once per reason). Never throws, never blocks. */
export function reportJoin(id: string, reason: JoinReason) {
  // An in-app browser's own failure is reported as "inapp": the cause staff can act on.
  const r: JoinReason = embedded && (reason === 'motion-denied' || reason === 'no-motion' || reason === 'perm-error' || reason === 'no-sensor') ? 'inapp' : reason;
  if (sentReasons.has(r)) return;
  sentReasons.add(r);
  if (r === 'ok' && sentReasons.size === 1) return; // nothing went wrong: nothing to clear
  try {
    void fetch('/api/join/report', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id, reason: r, browser: browserLabel() }),
      keepalive: true,
    }).catch(() => {});
  } catch {
    /* offline: the dashboard just won't know */
  }
}
