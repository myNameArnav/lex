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
  return new Date(ts * 1000).toLocaleDateString();
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

export function streamLabel(s) {
  if (s.type === 'audio') {
    return [s.title || langName(s.language), s.codec?.toUpperCase(), channelName(s.channels)].filter(Boolean).join(' · ') + (s.default ? ' (default)' : '');
  }
  if (s.type === 'subtitle') {
    const parts = [s.title && s.title !== langName(s.language) ? `${langName(s.language)} – ${s.title}` : langName(s.language)];
    if (s.forced) parts.push('Forced');
    parts.push(s.external ? `${(s.codec || '').toUpperCase()} external` : (s.codec || '').toUpperCase().replace('HDMV_PGS_SUBTITLE', 'PGS').replace('SUBRIP', 'SRT'));
    if (!s.textSub) parts.push('burn-in');
    return parts.join(' · ');
  }
  return s.codec;
}

// ---------- toasts & modals ----------

let toastBox;
export function toast(msg, kind = '') {
  if (!toastBox) { toastBox = h('div', { class: 'toasts' }); document.body.appendChild(toastBox); }
  const t = h('div', { class: `toast ${kind}` }, msg);
  toastBox.appendChild(t);
  setTimeout(() => t.remove(), kind === 'error' ? 6000 : 3000);
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
export function modal({ title, body, actions = [], wide = false, onClose, parent = document.body }) {
  const titleId = `dialog-title-${++modalId}`;
  const bg = h('dialog', { class: 'modal-bg', 'aria-labelledby': titleId, 'aria-modal': 'true' });
  let closed = false;
  const close = () => {
    if (closed) return;
    closed = true;
    bg.close();
    bg.remove();
    onClose && onClose();
  };
  bg.addEventListener('cancel', (e) => { e.preventDefault(); close(); });
  bg.addEventListener('close', close);
  bg.addEventListener('keydown', (e) => { if (e.target.closest('dialog') === bg) containTab(e, bg); });
  bg.addEventListener('mousedown', (e) => { if (e.target === bg) close(); });
  const foot = actions.length ? h('div', { class: 'modal-foot' }, actions) : null;
  bg.appendChild(h('div', { class: `modal ${wide ? 'wide' : ''}` },
    h('div', { class: 'modal-head' }, h('h2', { id: titleId }, title), h('button', { class: 'btn icon ghost sm', 'aria-label': 'Close dialog', onclick: close, html: icons.close })),
    h('div', { class: 'modal-body' }, body),
    foot));
  parent.appendChild(bg);
  bg.showModal(); // Native focus containment, background inertness and focus restoration.
  return { close, el: bg };
}

export function confirmDialog(message, okLabel = 'OK', danger = false) {
  return new Promise((resolve) => {
    let done = false;
    const m = modal({
      title: 'Confirm', body: h('p', { style: { margin: 0 } }, message),
      actions: [
        h('button', { class: 'btn', onclick: () => { done = true; m.close(); resolve(false); } }, 'Cancel'),
        h('button', { class: `btn ${danger ? 'danger' : 'primary'}`, onclick: () => { done = true; m.close(); resolve(true); } }, okLabel),
      ],
      onClose: () => { if (!done) resolve(false); },
    });
  });
}

export function popupMenu(anchor, items) {
  document.querySelectorAll('.menu.popup').forEach((m) => m.remove());
  const r = anchor.getBoundingClientRect();
  const menu = h('div', { class: 'menu popup', style: { position: 'fixed', top: `${r.bottom + 6}px`, right: `${Math.max(8, window.innerWidth - r.right)}px` } },
    items.map((it) => it === '-' ? h('hr') : it.note ? h('div', { class: 'menu-note' }, it.note)
      : h('button', { onclick: () => { menu.remove(); anchor.focus(); it.onClick(); } }, it.icon ? h('span', { html: icons[it.icon] }) : null, it.label)));
  document.body.appendChild(menu);
  const off = (e) => { if (!menu.contains(e.target)) { menu.remove(); document.removeEventListener('mousedown', off, true); } };
  setTimeout(() => document.addEventListener('mousedown', off, true));
  return menu;
}

export function spinner() { return h('div', { class: 'loading' }, h('div', { class: 'spinner' })); }

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

export function icon(name, cls = '') {
  return h('span', { class: `ic ${cls}`, html: icons[name] || '' });
}
