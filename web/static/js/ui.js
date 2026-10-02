// Small DOM + formatting helpers shared by all views.

export function h(tag, attrs, ...children) {
  const el = document.createElement(tag);
  if (attrs) {
    for (const [k, v] of Object.entries(attrs)) {
      if (v == null || v === false) continue;
      if (k === 'class') el.className = v;
      else if (k === 'style' && typeof v === 'object') Object.assign(el.style, v);
      else if (k === 'dataset') Object.assign(el.dataset, v);
      else if (k === 'html') el.innerHTML = v;
      else if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2).toLowerCase(), v);
      else if (k === 'value' || k === 'checked' || k === 'selected' || k === 'disabled') el[k] = v;
      else el.setAttribute(k, v === true ? '' : v);
    }
  }
  append(el, children);
  return el;
}

function append(el, children) {
  for (const c of children) {
    if (c == null || c === false) continue;
    if (Array.isArray(c)) append(el, c);
    else if (c instanceof Node) el.appendChild(c);
    else el.appendChild(document.createTextNode(String(c)));
  }
}

export const $ = (sel, root = document) => root.querySelector(sel);
export const $$ = (sel, root = document) => [...root.querySelectorAll(sel)];

export function clear(el) { while (el.firstChild) el.removeChild(el.firstChild); return el; }

export function fmtTime(sec) {
  if (!isFinite(sec) || sec < 0) sec = 0;
  sec = Math.floor(sec);
  const hh = Math.floor(sec / 3600), mm = Math.floor((sec % 3600) / 60), ss = sec % 60;
  const p = (n) => String(n).padStart(2, '0');
  return hh ? `${hh}:${p(mm)}:${p(ss)}` : `${mm}:${p(ss)}`;
}

export function fmtDuration(sec) {
  if (!sec) return '';
  const m = Math.round(sec / 60);
  if (m < 60) return `${m}m`;
  return `${Math.floor(m / 60)}h ${m % 60}m`;
}

export function fmtBytes(n, digits = 1) {
  if (!n) return '0 B';
  const u = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return `${n.toFixed(i ? digits : 0)} ${u[i]}`;
}

export function fmtBitrate(bps) {
  if (!bps) return '0 kbps';
  if (bps >= 1e6) return `${(bps / 1e6).toFixed(1)} Mbps`;
  return `${Math.round(bps / 1e3)} kbps`;
}

export function fmtDate(ts) {
  if (!ts) return '';
  return new Date(ts * 1000).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
}

export function timeAgo(ts) {
  if (!ts) return 'never';
  const s = Date.now() / 1000 - ts;
  if (s < 60) return 'just now';
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  if (s < 86400 * 30) return `${Math.floor(s / 86400)}d ago`;
  return new Date(ts * 1000).toLocaleDateString(undefined, { dateStyle: 'medium' });
}

export function fmtUptime(sec) {
  const d = Math.floor(sec / 86400), hh = Math.floor((sec % 86400) / 3600), mm = Math.floor((sec % 3600) / 60);
  return d ? `${d}d ${hh}h` : hh ? `${hh}h ${mm}m` : `${mm}m`;
}

const LANGS = { eng: 'English', jpn: 'Japanese', spa: 'Spanish', fre: 'French', ger: 'German', ita: 'Italian', hin: 'Hindi', rus: 'Russian', por: 'Portuguese', chi: 'Chinese', kor: 'Korean', ara: 'Arabic', dut: 'Dutch', swe: 'Swedish', pol: 'Polish', tur: 'Turkish', tam: 'Tamil', tel: 'Telugu', nor: 'Norwegian', dan: 'Danish', fin: 'Finnish', ukr: 'Ukrainian', heb: 'Hebrew', tha: 'Thai', vie: 'Vietnamese', ind: 'Indonesian', may: 'Malay', cze: 'Czech', gre: 'Greek', hun: 'Hungarian', rum: 'Romanian' };
export const langName = (c) => LANGS[c] || (c ? c.toUpperCase() : 'Unknown');
export const LANG_OPTIONS = Object.entries(LANGS);

// Resolution label by width, so 2.39:1 scope films (1920x800) read as 1080p.
export function resLabel(w, h) {
  if (!w && !h) return '';
  if (w >= 3200 || h >= 2000) return '4K';
  if (w >= 1800 || h >= 1000) return '1080p';
  if (w >= 1200 || h >= 700) return '720p';
  if (w >= 900 || h >= 540) return '576p';
  return `${h}p`;
}

export function channelName(ch) {
  return { 1: 'Mono', 2: 'Stereo', 6: '5.1', 8: '7.1' }[ch] || (ch ? `${ch}ch` : '');
}

// Language plus the track title, or just the title when it already names
// the language ("English SDH") or the track has no language tag.
function trackName(s, sep) {
  const lang = langName(s.language);
  const t = s.title && s.title !== lang ? s.title : '';
  if (!t) return lang;
  return !s.language || t.toLowerCase().includes(lang.toLowerCase()) ? t : `${lang}${sep}${t}`;
}

export function streamLabel(s) {
  const codec = (s.codec || '').toUpperCase().replace('HDMV_PGS_SUBTITLE', 'PGS').replace('SUBRIP', 'SRT');
  if (s.type === 'audio') {
    return [trackName(s, ' · '), codec, channelName(s.channels)].filter(Boolean).join(' · ') + (s.default ? ' (default)' : '');
  }
  if (s.type === 'subtitle') {
    const parts = [trackName(s, ' – ')];
    if (s.forced) parts.push('Forced');
    parts.push(codec);
    if (s.external) parts.push('External');
    if (!s.textSub) parts.push('burn-in');
    return parts.filter(Boolean).join(' · ');
  }
  return s.codec;
}

// "S1 E3", or "S1 E3–4" for a multi-episode file.
export function fmtEpisode(it) {
  if (it?.episode == null) return it?.season != null ? `S${it.season}` : '';
  return `S${it.season ?? 0} E${it.episode}${it.episodeEnd ? `–${it.episodeEnd}` : ''}`;
}

export const KIND_LABELS = { movies: 'Movies', shows: 'TV Shows', mixed: 'Mixed' };

// Playback method names, Title Case everywhere in the UI.
export const METHOD_LABEL = { direct: 'Direct Play', remux: 'Direct Stream', transcode: 'Transcode' };

// reasonLabel turns a server conversion reason into UI copy ("forced remux"
// means the user picked the method in the player).
export function reasonLabel(r) {
  const m = /^forced (direct|remux|transcode)$/.exec(r);
  return m ? `${METHOD_LABEL[m[1]]} chosen in player settings` : r === 'forced direct play' ? 'Direct Play chosen in player settings' : r;
}

export const motionOK = () => !matchMedia('(prefers-reduced-motion: reduce)').matches;

// ---------- toasts & modals ----------

let toastBox;
// An open dialog is in the top layer, so toasts go inside the topmost one;
// otherwise into the fullscreen element (only its subtree is drawn) or the
// player (aria-modal hides everything outside it from screen readers).
function toastHost() {
  const fs = document.fullscreenElement;
  return [...document.querySelectorAll('dialog[open]')].at(-1) || (fs && fs.tagName !== 'VIDEO' ? fs : null) || document.querySelector('.player') || document.body;
}
function rehomeToasts() {
  if (toastBox?.childElementCount) { const host = toastHost(); if (toastBox.parentNode !== host) host.appendChild(toastBox); }
}
document.addEventListener('fullscreenchange', rehomeToasts);

function dropToast(t) { t._gone = true; clearTimeout(t._timer); t.remove(); }
function capToasts() { while (toastBox.childElementCount > 3) dropToast(toastBox.firstElementChild); }

// Toasts waiting for the next frame: a live region only announces changes
// made after it is in the page, so a newly attached box gets them a frame late.
let toastQueue = null;

// toast shows a short status message. kind: '' | 'ok' | 'error'.
// A visible toast with the same key (or, without a key, the same text) is
// updated and its timer restarted instead of stacking another one.
// ms defaults to 3s, or 7s for errors and long messages.
export function toast(msg, kind = '', { key, ms } = {}) {
  msg = String(msg ?? '');
  msg = msg.charAt(0).toUpperCase() + msg.slice(1);
  if (!toastBox) toastBox = h('div', { class: 'toasts', role: 'status', 'aria-live': 'polite' });
  const host = toastHost();
  const attached = toastBox.parentNode === host;
  if (!attached) host.appendChild(toastBox);
  const dur = ms ?? (kind === 'error' || msg.length > 60 ? 7000 : 3000);
  let t = [...toastBox.children, ...(toastQueue || [])].find((x) => !x._gone && (key ? x.dataset.key === key : !x.dataset.key && x._msg === msg));
  if (t) {
    clearTimeout(t._timer);
  } else {
    t = h('div', { dataset: key ? { key } : null });
    if (attached && !toastQueue) {
      toastBox.appendChild(t);
      capToasts();
    } else {
      if (!toastQueue) {
        toastQueue = [];
        requestAnimationFrame(() => {
          const q = toastQueue;
          toastQueue = null;
          q.forEach((x) => { if (!x._gone) toastBox.appendChild(x); });
          capToasts();
        });
      }
      toastQueue.push(t);
    }
  }
  t._msg = msg;
  t.className = `toast ${kind}`;
  if (kind === 'error') t.setAttribute('role', 'alert'); else t.removeAttribute('role');
  t.replaceChildren(h('span', null, msg));
  if (kind === 'error') t.appendChild(h('button', { class: 'toast-x', type: 'button', 'aria-label': 'Dismiss', html: icons.close, onclick: () => dropToast(t) }));
  t._timer = setTimeout(() => dropToast(t), dur);
}

// Keep keyboard focus in an overlay while allowing native controls to handle
// their own keys. Hidden and disabled controls do not participate in the loop.
export function containTab(e, root) {
  if (e.key !== 'Tab') return;
  const controls = [...root.querySelectorAll('a[href], button, input, select, textarea, [tabindex]')].filter((el) => el.tabIndex >= 0 && !el.disabled && el.getClientRects().length && getComputedStyle(el).visibility !== 'hidden');
  const first = controls[0], last = controls.at(-1);
  const active = document.activeElement;
  if (first && ((!e.shiftKey && (active === last || active === root)) || (e.shiftKey && (active === first || active === root)))) {
    e.preventDefault();
    (e.shiftKey ? last : first).focus();
  }
}

let modalId = 0;
// modal opens a dialog. It closes on ×, Escape, navigation (hashchange) and,
// when dismissible, a click on the backdrop.
export function modal({ title, body, actions = [], wide = false, onClose, parent = document.body, dismissible = true }) {
  const titleId = `dialog-title-${++modalId}`;
  const bg = h('dialog', { class: 'modal-bg', 'aria-labelledby': titleId, 'aria-modal': 'true' });
  let closed = false;
  const close = () => {
    if (closed) return;
    closed = true;
    window.removeEventListener('hashchange', close);
    bg.close();
    if (toastBox && bg.contains(toastBox)) toastHost().appendChild(toastBox);
    bg.remove();
    onClose && onClose();
  };
  window.addEventListener('hashchange', close);
  bg.addEventListener('cancel', (e) => { e.preventDefault(); close(); });
  bg.addEventListener('close', close);
  bg.addEventListener('keydown', (e) => { if (e.target.closest('dialog') === bg) containTab(e, bg); });
  bg.addEventListener('mousedown', (e) => { if (e.target === bg && dismissible) close(); });
  const foot = actions.length ? h('div', { class: 'modal-foot' }, actions) : null;
  bg.appendChild(h('div', { class: `modal ${wide ? 'wide' : ''}` },
    h('div', { class: 'modal-head' }, h('h2', { id: titleId }, title), h('button', { class: 'btn icon ghost sm', 'aria-label': 'Close dialog', onclick: close, html: icons.close })),
    h('div', { class: 'modal-body' }, body),
    foot));
  // A dialog opened over a popup menu (e.g. the ? key) returns focus to its anchor.
  activeMenu?.close(true);
  parent.appendChild(bg);
  bg.showModal(); // Native focus containment, background inertness and focus restoration.
  return { close, el: bg };
}

// confirmDialog resolves true when the user confirms. Call it positionally,
// confirmDialog(message, okLabel, danger, title), or with an object
// { message, okLabel, danger, title }. danger gives a solid red button.
export function confirmDialog(message, okLabel = 'OK', danger = false, title = 'Confirm') {
  if (message && typeof message === 'object' && !(message instanceof Node)) ({ message, okLabel = 'OK', danger = false, title = 'Confirm' } = message);
  return new Promise((resolve) => {
    let done = false;
    const m = modal({
      title, body: h('p', { style: { margin: 0 } }, message),
      actions: [
        h('button', { class: 'btn', onclick: () => { done = true; m.close(); resolve(false); } }, 'Cancel'),
        h('button', { class: `btn ${danger ? 'danger solid' : 'primary'}`, onclick: () => { done = true; m.close(); resolve(true); } }, okLabel),
      ],
      onClose: () => { if (!done) resolve(false); },
    });
  });
}

// run performs an async action for a button: while fn runs the button is
// disabled and aria-busy (optionally showing busyLabel), so double clicks
// can't submit twice. A failure toasts its message; okMsg (a string, or a
// function of fn's result) toasts on success. Resolves true on success and
// false on failure or when the button was already busy. btn may be null.
export async function run(btn, fn, okMsg, { busyLabel } = {}) {
  if (btn?.disabled) return false;
  let saved = null, hadFocus = false;
  if (btn) {
    hadFocus = document.activeElement === btn;
    btn.disabled = true;
    btn.setAttribute('aria-busy', 'true');
    if (busyLabel) { saved = [...btn.childNodes]; btn.textContent = busyLabel; }
  }
  try {
    const res = await fn();
    const ok = typeof okMsg === 'function' ? okMsg(res) : okMsg;
    if (ok) toast(ok, 'ok');
    return true;
  } catch (e) {
    if (e?.name !== 'AbortError') toast(e?.message || String(e), 'error');
    return false;
  } finally {
    if (btn?.isConnected) {
      if (saved) btn.replaceChildren(...saved);
      btn.disabled = false;
      btn.removeAttribute('aria-busy');
      // Disabling a focused button drops focus to <body>; put it back.
      if (hadFocus && (!document.activeElement || document.activeElement === document.body)) btn.focus({ preventScroll: true });
    }
  }
}

// staleBanner marks a polled view as out of date while the server can't be
// reached. Put .el (an empty live region until needed) above the view and
// pass hooks(key) to every() for each poller: after repeated failures the
// banner says how old the data is and target is dimmed; it clears once every
// failing poller has recovered. fail(t, key) / ok(key) can also be called
// directly; t = null means nothing has loaded yet.
export function staleBanner(target) {
  const el = h('div', { role: 'status' });
  const failing = new Map();
  const show = () => {
    target?.classList.toggle('stale', failing.size > 0);
    if (!failing.size) { el.replaceChildren(); return; }
    const times = [...failing.values()];
    const since = times.includes(null) ? null : Math.min(...times);
    el.replaceChildren(h('div', { class: 'panel stale-banner small' }, since
      ? `Can’t reach the server — showing data from ${new Date(since).toLocaleTimeString()}. Retrying…`
      : 'Can’t reach the server. Retrying…'));
  };
  const b = {
    el,
    fail(t = Date.now(), key = '') { if (!failing.has(key)) { failing.set(key, t); show(); } },
    ok(key = '') { if (failing.delete(key)) show(); },
    hooks: (key = '') => ({ onFail: (t) => b.fail(t, key), onRecover: () => b.ok(key) }),
  };
  return b;
}

// emptyState renders a centred message for empty, error and not-found views.
// actions: buttons or links; alert announces it; level: heading tag.
export function emptyState({ title, text, actions = [], alert = false, level = 'h1' } = {}) {
  return h('div', { class: 'empty', role: alert ? 'alert' : null },
    h(level, null, title),
    text ? h('p', null, text) : null,
    actions.length ? h('div', { class: 'row wrap empty-actions' }, actions) : null);
}

// ---------- popup menus ----------

// The open popup menu: { anchor, close(restoreFocus) }.
let activeMenu = null;
export function closePopupMenu() { activeMenu?.close(false); }

// popupMenu opens a menu for anchor (a button). items: { label, icon?,
// onClick } | '-' (separator) | { note } (dim text). Opening it again from
// the same anchor closes it. Arrow keys, Home/End and Escape work as in a
// native menu; it closes on outside clicks, navigation, resize and when the
// page scroll moves the anchor. onClick may be async: a rejection toasts.
export function popupMenu(anchor, items) {
  if (activeMenu) {
    const same = activeMenu.anchor === anchor;
    activeMenu.close(false);
    if (same) return null;
  }
  const buttons = [];
  const menu = h('div', { class: 'menu popup', role: 'menu', style: { position: 'fixed' } }, items.map((it) => {
    if (it === '-') return h('hr', { role: 'separator' });
    if (it.note) return h('div', { class: 'menu-note', role: 'none' }, it.note);
    const b = h('button', { type: 'button', role: 'menuitem', tabindex: '-1', onclick: () => {
      close(true);
      Promise.resolve().then(it.onClick).catch((e) => { if (e?.name !== 'AbortError') toast(e?.message || String(e), 'error'); });
    } }, it.icon ? h('span', { html: icons[it.icon] }) : null, it.label);
    buttons.push(b);
    return b;
  }));
  // Kept on <body>: the top bar's backdrop-filter would make it the
  // containing block of a fixed-position child.
  document.body.appendChild(menu);
  const r = anchor.getBoundingClientRect();
  const mh = menu.offsetHeight, vh = innerHeight;
  if (r.bottom + 6 + mh <= vh - 8) menu.style.top = `${r.bottom + 6}px`;
  else if (r.top - 6 - mh >= 8) menu.style.bottom = `${vh - r.top + 6}px`;
  else menu.style.top = `${Math.max(8, Math.min(r.bottom + 6, vh - mh - 8))}px`;
  menu.style.right = `${Math.max(8, document.documentElement.clientWidth - r.right)}px`;

  const onDown = (e) => { if (!menu.contains(e.target) && !anchor.contains(e.target)) close(false); };
  const onScroll = (e) => {
    if (menu.contains(e.target)) return;
    const n = anchor.getBoundingClientRect();
    if (Math.abs(n.top - r.top) > 1 || Math.abs(n.left - r.left) > 1) close(false);
  };
  const onAway = () => close(false);
  const onKey = (e) => {
    const i = buttons.indexOf(document.activeElement), n = buttons.length;
    let next = null;
    if (e.key === 'ArrowDown') next = buttons[i < 0 ? 0 : (i + 1) % n];
    else if (e.key === 'ArrowUp') next = buttons[i < 0 ? n - 1 : (i - 1 + n) % n];
    else if (e.key === 'Home') next = buttons[0];
    else if (e.key === 'End') next = buttons[n - 1];
    else if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); close(true); return; }
    // Without preventDefault the browser moves on from the anchor.
    else if (e.key === 'Tab') { close(false); anchor.focus({ preventScroll: true }); return; }
    if (next) { e.preventDefault(); next.focus(); }
  };
  const close = (restore) => {
    if (activeMenu?.menu !== menu) return;
    activeMenu = null;
    menu.remove();
    anchor.setAttribute('aria-expanded', 'false');
    document.removeEventListener('mousedown', onDown, true);
    document.removeEventListener('scroll', onScroll, true);
    window.removeEventListener('hashchange', onAway);
    window.removeEventListener('resize', onAway);
    if (restore && anchor.isConnected) anchor.focus({ preventScroll: true });
  };
  activeMenu = { anchor, menu, close };
  menu.addEventListener('keydown', onKey);
  document.addEventListener('mousedown', onDown, true);
  document.addEventListener('scroll', onScroll, { capture: true, passive: true });
  window.addEventListener('hashchange', onAway);
  window.addEventListener('resize', onAway);
  anchor.setAttribute('aria-haspopup', 'menu');
  anchor.setAttribute('aria-expanded', 'true');
  buttons[0]?.focus({ preventScroll: true });
  return menu;
}

export function spinner() { return h('div', { class: 'loading', role: 'status', 'aria-label': 'Loading' }, h('div', { class: 'spinner' })); }

export function lazyImg(src, alt = '', onFail) {
  const img = h('img', { alt, loading: 'lazy', decoding: 'async' });
  img.addEventListener('load', () => img.classList.add('loaded'));
  img.addEventListener('error', () => { img.remove(); onFail && onFail(); });
  img.src = src;
  return img;
}

export function toggle(checked, onchange, label) {
  const input = h('input', { type: 'checkbox', checked, 'aria-label': label, onchange: (e) => onchange && onchange(e.target.checked) });
  return h('span', { class: 'switch' }, input, h('i'));
}

// ---------- icons (inline SVG, stroke-based) ----------
const svg = (d, fill = false) => `<svg viewBox="0 0 24 24" fill="${fill ? 'currentColor' : 'none'}" stroke="${fill ? 'none' : 'currentColor'}" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">${d}</svg>`;
export const icons = {
  play: svg('<path d="M7 4.5v15a1 1 0 0 0 1.5.86l12.3-7.5a1 1 0 0 0 0-1.72L8.5 3.64A1 1 0 0 0 7 4.5z"/>', true),
  pause: svg('<rect x="6" y="4.5" width="4" height="15" rx="1"/><rect x="14" y="4.5" width="4" height="15" rx="1"/>', true),
  back10: svg('<path d="M3 12a9 9 0 1 0 3-6.7L3 8"/><path d="M3 3v5h5"/><text x="12" y="15.5" font-size="7.5" text-anchor="middle" fill="currentColor" stroke="none" font-weight="700" font-family="system-ui">10</text>'),
  fwd30: svg('<path d="M21 12a9 9 0 1 1-3-6.7L21 8"/><path d="M21 3v5h-5"/><text x="12" y="15.5" font-size="7.5" text-anchor="middle" fill="currentColor" stroke="none" font-weight="700" font-family="system-ui">30</text>'),
  volume: svg('<path d="M11 5 6 9H2v6h4l5 4V5z"/><path d="M15.5 8.5a5 5 0 0 1 0 7"/><path d="M19 5a10 10 0 0 1 0 14"/>'),
  mute: svg('<path d="M11 5 6 9H2v6h4l5 4V5z"/><path d="m22 9-6 6M16 9l6 6"/>'),
  cc: svg('<rect x="2" y="5" width="20" height="14" rx="3"/><path d="M10 10.5a2.5 2.5 0 1 0 0 3M17 10.5a2.5 2.5 0 1 0 0 3"/>'),
  gear: svg('<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 1 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.68 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 1 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.68a1.65 1.65 0 0 0 1-1.51V3a2 2 0 1 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 1 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/>'),
  fullscreen: svg('<path d="M8 3H5a2 2 0 0 0-2 2v3M21 8V5a2 2 0 0 0-2-2h-3M3 16v3a2 2 0 0 0 2 2h3M16 21h3a2 2 0 0 0 2-2v-3"/>'),
  exitfs: svg('<path d="M8 3v3a2 2 0 0 1-2 2H3M21 8h-3a2 2 0 0 1-2-2V3M3 16h3a2 2 0 0 1 2 2v3M16 21v-3a2 2 0 0 1 2-2h3"/>'),
  pip: svg('<rect x="2" y="4" width="20" height="16" rx="2"/><rect x="12" y="11" width="8" height="6" rx="1" fill="currentColor"/>'),
  next: svg('<path d="M5 4.5v15l10-7.5z" fill="currentColor"/><path d="M19 5v14"/>'),
  close: svg('<path d="M18 6 6 18M6 6l12 12"/>'),
  back: svg('<path d="M19 12H5M12 19l-7-7 7-7"/>'),
  check: svg('<path d="M20 6 9 17l-5-5"/>'),
  heart: svg('<path d="M20.8 4.6a5.5 5.5 0 0 0-7.8 0L12 5.7l-1-1.1a5.5 5.5 0 0 0-7.8 7.8l1 1.1L12 21l7.8-7.5 1-1.1a5.5 5.5 0 0 0 0-7.8z"/>'),
  heartFill: svg('<path d="M20.8 4.6a5.5 5.5 0 0 0-7.8 0L12 5.7l-1-1.1a5.5 5.5 0 0 0-7.8 7.8l1 1.1L12 21l7.8-7.5 1-1.1a5.5 5.5 0 0 0 0-7.8z"/>', true),
  search: svg('<circle cx="11" cy="11" r="7"/><path d="m21 21-4.3-4.3"/>'),
  more: svg('<circle cx="12" cy="5" r="1.5" fill="currentColor"/><circle cx="12" cy="12" r="1.5" fill="currentColor"/><circle cx="12" cy="19" r="1.5" fill="currentColor"/>'),
  info: svg('<circle cx="12" cy="12" r="10"/><path d="M12 16v-4M12 8h.01"/>'),
  stats: svg('<path d="M3 3v18h18"/><path d="M7 15l4-4 3 3 5-6"/>'),
  refresh: svg('<path d="M21 12a9 9 0 1 1-2.64-6.36L21 8"/><path d="M21 3v5h-5"/>'),
  folder: svg('<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>'),
  up: svg('<path d="M12 19V5M5 12l7-7 7 7"/>'),
  user: svg('<circle cx="12" cy="8" r="4"/><path d="M4 21a8 8 0 0 1 16 0"/>'),
  keyboard: svg('<rect x="2" y="6" width="20" height="12" rx="2"/><path d="M6 10h.01M10 10h.01M14 10h.01M18 10h.01M8 14h8"/>'),
  logout: svg('<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9"/>'),
  library: svg('<rect x="3" y="3" width="7" height="18" rx="1"/><rect x="14" y="3" width="7" height="18" rx="1"/>'),
  users: svg('<circle cx="9" cy="8" r="4"/><path d="M1 21a8 8 0 0 1 16 0M17 4a4 4 0 0 1 0 8M23 21a8 8 0 0 0-5-7.4"/>'),
  film: svg('<rect x="2" y="3" width="20" height="18" rx="2"/><path d="M7 3v18M17 3v18M2 8h5M17 8h5M2 16h5M17 16h5M2 12h20"/>'),
  sliders: svg('<path d="M4 21v-7M4 10V3M12 21v-9M12 8V3M20 21v-5M20 12V3M1 14h6M9 8h6M17 16h6"/>'),
  devices: svg('<rect x="2" y="4" width="14" height="11" rx="2"/><rect x="17" y="8" width="5" height="12" rx="1"/><path d="M6 19h6"/>'),
  log: svg('<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><path d="M14 2v6h6M8 13h8M8 17h8M8 9h2"/>'),
  cpu: svg('<rect x="5" y="5" width="14" height="14" rx="2"/><rect x="9" y="9" width="6" height="6"/><path d="M9 2v3M15 2v3M9 19v3M15 19v3M2 9h3M2 15h3M19 9h3M19 15h3"/>'),
  broadcast: svg('<circle cx="12" cy="12" r="2"/><path d="M16.2 7.8a6 6 0 0 1 0 8.4M7.8 16.2a6 6 0 0 1 0-8.4M19 5a10 10 0 0 1 0 14M5 19A10 10 0 0 1 5 5"/>'),
  history: svg('<path d="M3 12a9 9 0 1 0 3-6.7L3 8"/><path d="M3 3v5h5M12 7v5l4 2"/>'),
  plus: svg('<path d="M12 5v14M5 12h14"/>'),
  trash: svg('<path d="M3 6h18M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"/>'),
  edit: svg('<path d="M12 20h9M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z"/>'),
  chevL: svg('<path d="m15 18-6-6 6-6"/>'),
  chevR: svg('<path d="m9 18 6-6-6-6"/>'),
  eye: svg('<path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8S1 12 1 12z"/><circle cx="12" cy="12" r="3"/>'),
  link: svg('<path d="M10 13a5 5 0 0 0 7.5.5l3-3a5 5 0 0 0-7-7l-1.7 1.7"/><path d="M14 11a5 5 0 0 0-7.5-.5l-3 3a5 5 0 0 0 7 7l1.7-1.7"/>'),
  drive: svg('<rect x="2" y="13" width="20" height="8" rx="2"/><path d="M5 13 7.5 4h9L19 13"/><path d="M6 17h.01M10 17h.01"/>'),
  shuffle: svg('<path d="M16 3h5v5M4 20 21 3M21 16v5h-5M15 15l6 6M4 4l5 5"/>'),
};
