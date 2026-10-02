import { h, $, clear, icons, resLabel, fmtTime, fmtDuration, fmtBytes, fmtBitrate, toast, modal, spinner, lazyImg, popupMenu, closePopupMenu, streamLabel, langName, channelName, confirmDialog, run, fmtEpisode, motionOK, emptyState } from './ui.js';
import { api, img, castImg } from './api.js';
import { openPlayer, isPlayerOpen, closePlayer } from './player.js';
import { installShortcuts, showShortcuts, MOD } from './shortcuts.js';

// caps: { subtitleSearch, cacheEnabled } from the server (see /api/me).
export const state = { me: null, caps: {}, serverName: 'Lex', libraries: [], version: '' };
const app = document.getElementById('app');
let mainEl = null, navEl = null, searchInput = null;
let routeToken = 0;

// ---------- boot ----------
installShortcuts({
  focusSearch: () => { if (searchInput?.isConnected) { searchInput.focus(); searchInput.select(); } },
  go: (hash) => { location.hash = hash; },
  libraries: () => state.libraries,
});

// Session expiry (any 401): close whatever is open over the app and ask to
// sign in again; signing in returns to the same route.
window.addEventListener('lex:unauthorized', () => {
  if (!state.me) return;
  state.me = null;
  routeToken++; // invalidates the current view, so its pollers and pending loads stop
  leaveGuard = null;
  document.querySelectorAll('dialog[open]').forEach((d) => d.close());
  closePopupMenu();
  if (isPlayerOpen()) closePlayer();
  renderAuth(false, 'Your session ended. Sign in again to continue.');
});

async function boot() {
  try {
    const info = await api('/api/public/info');
    state.serverName = info.serverName;
    state.version = info.version;
    document.title = info.serverName;
    if (info.setupRequired) return renderAuth(true);
    const me = await api('/api/me');
    state.me = me.user;
    state.caps = me.caps || {};
    await startApp();
  } catch (e) {
    if (e.status === 401) return renderAuth(false);
    const down = e.status === 0 || e.status >= 502;
    clear(app).appendChild(h('main', { id: 'main' }, emptyState({
      title: down ? 'Can’t reach Lex' : 'Something went wrong', text: down ? 'It may be restarting. Try again in a moment.' : e.message, alert: true,
      actions: [h('button', { class: 'btn primary', onclick: () => { clear(app).appendChild(spinner()); boot(); } }, 'Try again')],
    })));
  }
}

const AUTH_ERRORS = {
  'invalid username or password': 'Wrong username or password.',
  'password must be between 12 and 72 bytes': 'Password must be at least 12 characters.',
};

// renderAuth shows the sign-in card, or the first-run admin form when setup
// is set. notice says why the user is here (e.g. their session ended).
function renderAuth(setup, notice = '') {
  clear(app);
  document.title = state.serverName;
  const describe = (...ids) => [notice ? 'auth-notice' : null, ...ids].filter(Boolean).join(' ');
  const name = h('input', { id: 'auth-name', name: 'username', type: 'text', autocomplete: 'username', autocapitalize: 'none', autocorrect: 'off', spellcheck: 'false', required: true, autofocus: true, 'aria-describedby': describe('auth-error') });
  const pass = h('input', { id: 'auth-password', name: 'password', type: 'password', autocomplete: setup ? 'new-password' : 'current-password', required: true, minlength: setup ? 12 : null, maxlength: setup ? 72 : null, 'aria-describedby': setup ? 'auth-password-help auth-error' : 'auth-error' });
  const pass2 = setup ? h('input', { id: 'auth-confirm', name: 'confirm-password', type: 'password', autocomplete: 'new-password', required: true, maxlength: 72, 'aria-describedby': 'auth-error' }) : null;
  const err = h('div', { class: 'err', id: 'auth-error', role: 'alert', 'aria-atomic': 'true' });
  const field = (label, input, help) => h('label', { class: 'field', for: input.id }, h('span', null, label), input, help ? h('div', { class: 'help', id: 'auth-password-help' }, help) : null);
  const invalid = (inputs) => inputs.forEach((input) => input.setAttribute('aria-invalid', 'true'));
  for (const input of [name, pass, pass2].filter(Boolean)) input.addEventListener('input', () => input.removeAttribute('aria-invalid'));
  const btn = h('button', { class: 'btn primary lg', type: 'submit' }, setup ? 'Create admin account' : 'Sign in');
  const form = h('form', { class: 'auth-card', onsubmit: async (e) => {
    e.preventDefault();
    err.textContent = '';
    [name, pass, pass2].filter(Boolean).forEach((input) => input.removeAttribute('aria-invalid'));
    if (setup && pass.value !== pass2.value) { err.textContent = 'Passwords don’t match.'; invalid([pass, pass2]); pass2.focus(); return; }
    btn.disabled = true;
    try {
      const r = await api(setup ? '/api/setup' : '/api/auth/login', { method: 'POST', body: { name: name.value, password: pass.value } });
      state.me = r.user;
      state.caps = r.caps || {};
      await startApp();
      if (setup) location.hash = '#/settings/libraries';
    } catch (ex) {
      // Setup was finished elsewhere (another tab or device).
      if (setup && ex.status === 409) return renderAuth(false, 'Setup is already done. Sign in.');
      err.textContent = AUTH_ERRORS[ex.message] || ex.message;
      if (ex.status === 401) invalid([name, pass]);
      else if (setup && ex.status === 400) invalid([pass]);
      btn.disabled = false;
      pass.focus();
    }
  } },
  h('div', { class: 'logo' }, h('b', null, 'L'), h('span', null, state.serverName)),
  h('h1', null, setup ? 'Welcome! Create the admin account' : 'Sign in'),
  notice ? h('p', { class: 'muted', id: 'auth-notice', role: 'status', style: { margin: 0 } }, notice) : null,
  setup ? h('p', { class: 'muted', style: { margin: 0 } }, 'This account manages libraries, users and settings.') : null,
  field('Username', name), field('Password', pass, setup ? 'At least 12 characters.' : null), pass2 ? field('Confirm password', pass2) : null, err, btn);
  app.appendChild(h('main', { class: 'auth' }, form));
  name.focus();
}

async function startApp() {
  history.scrollRestoration = 'manual';
  await loadLibraries();
  renderShell();
  // The URL being left, for the leave guard to put back (it may differ from
  // the routed hash after in-page replaceState, e.g. the season tabs).
  window.onhashchange = (e) => { if (!guarding) lastHash = new URL(e.oldURL).hash; route(); };
  settled = false;
  route();
}

export async function loadLibraries() {
  try { state.libraries = await api('/api/libraries'); } catch { state.libraries = []; }
  renderNav();
}

// ---------- shell ----------
const narrow = matchMedia('(max-width: 520px)');
const searchPlaceholder = () => { if (searchInput) searchInput.placeholder = narrow.matches ? 'Search' : 'Search movies & shows'; };
narrow.addEventListener('change', searchPlaceholder);

function renderShell() {
  clear(app);
  navLinks = new Map();
  navEl = h('nav', { class: 'nav', 'aria-label': 'Libraries', onscroll: navFade });
  const typed = debounce((e) => {
    const q = e.target.value.trim();
    if (q) goSearch(q);
    else if (onSearch()) leaveSearch();
  }, 300);
  searchInput = h('input', { type: 'search', 'aria-label': 'Search movies & shows', 'aria-keyshortcuts': 'Control+K Meta+K /', autocapitalize: 'off', autocorrect: 'off', spellcheck: 'false', enterkeyhint: 'search', oninput: typed, onkeydown: (e) => {
    if (e.key === 'Escape') {
      typed.cancel();
      e.target.value = '';
      e.target.blur();
      if (onSearch()) leaveSearch();
    }
    if (e.key === 'Enter' && e.target.value.trim()) {
      typed.cancel();
      goSearch(e.target.value.trim());
      // Drop the phone keyboard so the results are visible.
      if (matchMedia('(pointer: coarse)').matches) e.target.blur();
    }
  } });
  searchPlaceholder();
  const avatarBtn = h('button', { class: 'avatar', title: state.me.name, 'aria-label': `Account menu (${state.me.name})`, 'aria-haspopup': 'menu', 'aria-expanded': 'false', onclick: (e) => userMenu(e.currentTarget) }, state.me.name.slice(0, 1).toUpperCase());
  const top = h('header', { class: 'topbar' },
    h('a', { class: 'logo', href: '#/', 'aria-label': `${state.serverName} home` }, h('b', null, 'L'), h('span', null, state.serverName)),
    navEl,
    h('div', { class: 'spacer' }),
    h('div', { class: 'search-box', role: 'search' }, h('span', { html: icons.search }), searchInput, h('kbd', { class: 'search-kbd hide-mobile', title: 'Keyboard shortcuts: press ?' }, `${MOD} K`)),
    state.me.isAdmin ? h('a', { class: 'btn icon ghost hide-mobile', href: '#/dashboard', title: 'Dashboard', html: icons.stats }) : null,
    avatarBtn);
  mainEl = h('main', { id: 'main' });
  // The router owns the hash, so the skip link moves focus itself.
  const skip = h('a', { class: 'skip', href: '#main', onclick: (e) => { e.preventDefault(); focusMain(); } }, 'Skip to content');
  app.append(skip, top, mainEl);
  renderNav();
}

// Nav links are built once per library list and updated in place, so focus
// and the nav's own scroll position survive navigation.
let navLinks = new Map(), navSig = '';
function renderNav() {
  if (!navEl) return;
  const sig = state.libraries.map((l) => `${l.id}\t${l.name}`).join('\n');
  if (!navLinks.size || sig !== navSig) {
    navSig = sig;
    navLinks = new Map([['home', h('a', { href: '#/' }, 'Home')], ...state.libraries.map((l) => [l.id, h('a', { href: `#/library/${l.id}` }, l.name)])]);
    navEl.replaceChildren(...navLinks.values());
  }
  const cur = location.hash;
  let active = null;
  for (const [id, a] of navLinks) {
    const on = id === 'home' ? cur === '' || cur === '#/' || cur.startsWith('#/?') : cur === `#/library/${id}` || cur.startsWith(`#/library/${id}?`);
    a.classList.toggle('active', on);
    if (on) { a.setAttribute('aria-current', 'page'); active = a; } else a.removeAttribute('aria-current');
  }
  // Centre the active link if the row overflows and hides it (scrollIntoView could also scroll the page).
  if (active) {
    const x = active.offsetLeft - navEl.offsetLeft;
    if (x < navEl.scrollLeft || x + active.offsetWidth > navEl.scrollLeft + navEl.clientWidth) navEl.scrollLeft = x - (navEl.clientWidth - active.offsetWidth) / 2;
  }
  navFade();
}

// Fade whichever edge of the nav row hides more links.
function navFade() {
  if (!navEl) return;
  const { scrollLeft: x, scrollWidth: sw, clientWidth: cw } = navEl;
  navEl.classList.toggle('fade-l', sw > cw + 1 && x > 4);
  navEl.classList.toggle('fade-r', sw > cw + 1 && x + cw < sw - 4);
}
window.addEventListener('resize', debounce(navFade, 100));

// ---------- search box ----------
// A search adds one history entry: the first query pushes it (marked, so
// clearing the box can step back to the page the search started from) and
// later queries replace it. Results re-render in place while typing.
const onSearch = () => location.hash.startsWith('#/search');
function goSearch(q) {
  const url = `#/search?q=${encodeURIComponent(q)}`;
  if (!onSearch()) {
    location.hash = url;
    try { history.replaceState({ fromPage: true }, ''); } catch {}
  } else if (parseHash().query.get('q') !== q) {
    history.replaceState(history.state, '', url);
    route();
  }
}
let leavingSearch = false;
function leaveSearch() {
  if (searchInput) searchInput.value = '';
  if (leavingSearch) return;
  leavingSearch = true;
  if (history.state?.fromPage) history.back();
  else location.hash = '#/';
}

function userMenu(anchor) {
  const items = [
    { label: 'Settings', icon: 'gear', onClick: () => { location.hash = '#/settings'; } },
  ];
  if (state.me.isAdmin) items.push({ label: 'Dashboard & stats', icon: 'stats', onClick: () => { location.hash = '#/dashboard'; } });
  // Phones have no keyboard; iPads with one (or a trackpad) keep the entry.
  if (matchMedia('(any-pointer: fine)').matches) items.push({ label: 'Keyboard shortcuts', icon: 'keyboard', onClick: showShortcuts });
  items.push('-', { label: 'Sign out', icon: 'logout', onClick: signOut });
  if (state.version) items.push('-', { note: `Lex ${state.version}` });
  popupMenu(anchor, items);
}

// The session cookie is HttpOnly, so only the server can end it: if the
// request fails, say so rather than reload into the same signed-in state.
async function signOut() {
  if (leaveGuard?.() && !(await confirmDiscard())) return;
  try {
    await api('/api/auth/logout', { method: 'POST' });
  } catch {
    toast('Couldn’t sign out: Lex is unreachable. Try again.', 'error');
    return;
  }
  leaveGuard = null;
  location.hash = '';
  location.reload();
}

function debounce(fn, ms) {
  let t;
  const d = (...a) => { clearTimeout(t); t = setTimeout(() => fn(...a), ms); };
  d.cancel = () => clearTimeout(t);
  return d;
}

// ---------- router ----------
function parseHash() {
  const raw = location.hash.replace(/^#/, '') || '/';
  const [path, qs] = raw.split('?');
  return { parts: path.split('/').filter(Boolean), query: new URLSearchParams(qs || '') };
}

// Leave guard: a view with unsaved edits registers (while rendering) a
// function returning true while it is dirty; navigating away, or reloading,
// asks first. Every route clears it, so a view re-registers on each render.
let leaveGuard = null;
export function setLeaveGuard(fn) { leaveGuard = fn; }
window.addEventListener('beforeunload', (e) => { if (leaveGuard?.()) { e.preventDefault(); e.returnValue = true; } });
const confirmDiscard = () => confirmDialog({ title: 'Discard changes?', message: 'You have unsaved changes. Leave this page and discard them?', okLabel: 'Discard changes', danger: true });

// focusAfterRoute(key): after the next route, focus the element with
// data-focus-key=key (e.g. the library sort select that changed the hash)
// instead of the page heading.
let pendingFocusKey = null;
export function focusAfterRoute(key) { pendingFocusKey = key; }

let lastHash = '', shownHash = null, lastSection, settled = false, guarding = false;

// replaceHash records in-page state in the address (e.g. the season tabs)
// without routing. Use it instead of history.replaceState, so the entry
// keeps its saved scroll and the shown view stays tied to it.
export function replaceHash(hash) {
  const shown = location.hash === shownHash;
  history.replaceState(history.state, '', hash);
  if (shown) shownHash = location.hash;
}

// route renders the view for location.hash. A normal navigation keeps the
// old view for up to 200 ms before showing a spinner, then restores the
// scroll saved in the history entry (Back/Forward) or starts at the top, and
// moves focus to the new page's h1. soft re-renders in place: no spinner,
// same scroll and shelf positions, focus back on the same data-focus-key.
// Views get ctx = { token, query, restore, isCurrent() }; restore is the saved
// { y, shelves, loaded } (or null) so a view can load enough to reach it.
export async function route({ soft = false } = {}) {
  if (!state.me) return;
  closePopupMenu();
  if (!soft && leaveGuard?.()) {
    if (guarding) return;
    const back = lastHash;
    guarding = true;
    const ok = await confirmDiscard();
    guarding = false;
    if (!ok) {
      if (location.hash !== back) history.replaceState(history.state, '', back || location.pathname);
      renderNav();
      return;
    }
  }
  leaveGuard = null;
  const token = ++routeToken;
  leavingSearch = false;
  const { parts, query } = parseHash();
  const [section, id] = parts;
  // Refining a search keeps the old results up until the new ones arrive.
  soft ||= section === 'search' && lastSection === 'search' && shownHash !== null;
  lastHash = location.hash;
  lastSection = section;
  renderNav();
  if (section !== 'search' && searchInput) searchInput.value = '';
  const restore = soft ? captureScroll() : history.state?.scroll || null;
  const active = document.activeElement;
  const focusKey = pendingFocusKey || (soft ? active?.dataset?.focusKey : null);
  const focusWasInMain = mainEl.contains(active) && active !== mainEl;
  pendingFocusKey = null;
  const ctx = { token, query, restore, isCurrent: () => token === routeToken };
  const spin = soft ? 0 : setTimeout(() => { if (ctx.isCurrent()) clear(mainEl).appendChild(spinner()); }, 200);
  let view;
  try {
    switch (section) {
      case undefined: view = await homeView(ctx); break;
      case 'library': view = await libraryView(ctx, +id); break;
      case 'item': view = await itemView(ctx, +id); break;
      case 'search': view = await searchView(ctx, query.get('q') || ''); break;
      case 'settings': { const m = await loadAdmin(); view = await m.settingsView(ctx, id || 'preferences'); break; }
      case 'dashboard': { const m = await loadAdmin(); view = await m.dashboardView(ctx, id || 'live'); break; }
      default: view = notFound();
    }
  } catch (e) {
    view = errorView(e, section);
  }
  if (!ctx.isCurrent()) return;
  clearTimeout(spin);
  mainEl.replaceChildren(view);
  shownHash = location.hash;
  const h1 = mainEl.querySelector('h1');
  document.title = section && h1 ? `${h1.textContent.trim()} · ${state.serverName}` : state.serverName;
  if (restore) restoreScroll(restore);
  else window.scrollTo(0, 0);
  const keyed = focusKey && [...mainEl.querySelectorAll('[data-focus-key]')].find((el) => el.dataset.focusKey === focusKey);
  const busy = isPlayerOpen() || document.querySelector('dialog[open]');
  if (keyed) keyed.focus({ preventScroll: true });
  // Announce the new page by focusing its heading, except after typing in the
  // search box, on first load and on soft refreshes that didn't lose focus.
  else if (!busy && settled && !searchInput?.contains(document.activeElement) && (!soft || (focusWasInMain && !mainEl.contains(document.activeElement)))) {
    if (h1) { h1.setAttribute('tabindex', '-1'); h1.focus({ preventScroll: true }); }
    else focusMain();
  }
  settled = true;
}

// main is focusable only while focused: with a permanent tabindex every click
// on plain content would focus it.
function focusMain() {
  mainEl.setAttribute('tabindex', '-1');
  mainEl.addEventListener('blur', () => mainEl.removeAttribute('tabindex'), { once: true });
  mainEl.focus({ preventScroll: true });
}

// admin.js is loaded on first use. A failed fetch (server down) reads as a
// network error so the route offers "Try again"; browsers remember a failed
// module URL, so the retry asks for a fresh one.
let adminModule = null, adminFails = 0;
function loadAdmin() {
  adminModule ||= import(adminFails ? `./admin.js?retry=${adminFails}` : './admin.js').catch((e) => {
    adminModule = null;
    adminFails++;
    throw e instanceof TypeError ? Object.assign(new Error('Network error: server unreachable'), { status: 0 }) : e;
  });
  return adminModule;
}

function notFound() {
  return emptyState({ title: 'Not found', text: 'This page doesn’t exist.', actions: [h('a', { class: 'btn primary', href: '#/' }, 'Go home')] });
}

function errorView(e, section) {
  if (e.status === 404) return emptyState({ title: 'Not found', text: section === 'item' ? 'This title isn’t in your library any more.' : 'This page doesn’t exist.', actions: [h('a', { class: 'btn primary', href: '#/' }, 'Go home')] });
  const actions = [h('button', { class: 'btn primary', onclick: () => route() }, 'Try again'), section ? h('a', { class: 'btn', href: '#/' }, 'Go home') : null].filter(Boolean);
  // 0: no response; 502–504: a proxy in front of Lex while it restarts.
  if (e.status === 0 || e.status >= 502) return emptyState({ title: 'Can’t reach Lex', text: 'It may be restarting. Try again in a moment.', alert: true, actions });
  return emptyState({ title: 'Something went wrong', text: e.message, alert: true, actions });
}

// ---------- scroll restoration ----------
// Each history entry keeps its page scroll, shelf positions and how many grid
// cards were loaded (saved shortly after scrolling stops), so Back returns to
// the same place.
function captureScroll() {
  return {
    y: Math.round(scrollY),
    shelves: [...mainEl.querySelectorAll('.shelf-track')].map((t) => Math.round(t.scrollLeft)),
    loaded: mainEl.querySelectorAll('.grid > .card').length,
  };
}
function restoreScroll({ y = 0, shelves = [] }) {
  mainEl.querySelectorAll('.shelf-track').forEach((t, i) => { if (shelves[i]) t.scrollLeft = shelves[i]; });
  window.scrollTo(0, y);
}
let scrollTimer = 0;
function saveScroll() {
  clearTimeout(scrollTimer);
  scrollTimer = 0;
  // Only while the shown view belongs to this entry (not mid-navigation).
  if (!state.me || !mainEl?.isConnected || location.hash !== shownHash) return;
  try { history.replaceState({ ...history.state, scroll: captureScroll() }, ''); } catch {} // Safari rate-limits replaceState
}
document.addEventListener('scroll', () => { clearTimeout(scrollTimer); scrollTimer = setTimeout(saveScroll, 200); }, { capture: true, passive: true });
// A click can navigate before the debounce fires: save first.
document.addEventListener('click', () => { if (scrollTimer) saveScroll(); }, true);

export function play(itemId, start = null) {
  openPlayer({ itemId, start, onClose: () => { if (!isPlayerOpen()) refreshSoft(); } });
}

// refreshSoft re-renders the current view in place (after playback or an
// in-page action): no spinner, scroll and shelf positions kept, and focus
// returns to the control with the same data-focus-key.
export function refreshSoft() { return route({ soft: true }); }


// ---------- cards ----------
function subtitleFor(it) {
  if (it.kind === 'show') return it.childCount ? `${it.childCount} episode${it.childCount === 1 ? '' : 's'}` : (it.year || '');
  if (it.kind === 'episode') return fmtEpisode(it);
  if (it.kind === 'season') return it.childCount ? `${it.childCount} episodes` : '';
  return it.year || '';
}

function placeholder(it) {
  return h('div', { class: 'ph' }, it.title);
}

export function posterCard(it) {
  const art = h('div', { class: 'art' });
  art.appendChild(placeholder(it));
  art.appendChild(lazyImg(img(it, 'poster', 320), it.title));
  const ud = it.userData || {};
  if (it.kind === 'show' || it.kind === 'season') {
    if (it.unplayedCount > 0) art.appendChild(h('div', { class: 'badge' }, it.unplayedCount));
    else if (it.childCount > 0) art.appendChild(h('div', { class: 'badge check', html: icons.check }));
  } else if (ud.played) art.appendChild(h('div', { class: 'badge check', html: icons.check }));
  if (ud.position > 0 && it.duration) art.appendChild(h('div', { class: 'progress' }, h('i', { style: { width: `${Math.min(100, (ud.position / it.duration) * 100)}%` } })));
  return h('a', { class: 'card', href: `#/item/${it.id}`, title: it.title },
    art, h('div', { class: 'meta' }, h('div', { class: 't' }, it.title), h('div', { class: 's' }, subtitleFor(it))));
}

export function landCard(it, { playOnClick = true } = {}) {
  const art = h('div', { class: 'art' });
  art.appendChild(placeholder(it));
  art.appendChild(lazyImg(img(it, 'thumb', 480), it.title));
  art.appendChild(h('div', { class: 'play-hover' }, h('span', { html: icons.play })));
  const ud = it.userData || {};
  if (ud.position > 0 && it.duration) art.appendChild(h('div', { class: 'progress' }, h('i', { style: { width: `${Math.min(100, (ud.position / it.duration) * 100)}%` } })));
  const title = it.kind === 'episode' ? (it.showTitle || it.title) : it.title;
  const sub = it.kind === 'episode' ? `${fmtEpisode(it)} — ${it.title}` : [it.year, ud.position && it.duration ? `${fmtDuration(it.duration - ud.position)} left` : ''].filter(Boolean).join(' · ');
  return h('div', { class: 'card land', title: `${title} — ${sub}`, onclick: (e) => {
    if (e.target.closest('.meta a')) return;
    if (playOnClick) play(it.id); else location.hash = `#/item/${it.id}`;
  } }, art, h('div', { class: 'meta' }, h('a', { class: 't', href: `#/item/${it.id}`, style: { display: 'block' } }, title), h('div', { class: 's' }, sub)));
}

function shelf(title, items, kind, moreHref) {
  const track = h('div', { class: 'shelf-track' }, items.map((it) => kind === 'landscape' ? landCard(it) : posterCard(it)));
  const scroll = (dir) => track.scrollBy({ left: dir * track.clientWidth * 0.85, behavior: motionOK() ? 'smooth' : 'auto' });
  return h('section', { class: 'shelf' },
    h('div', { class: 'shelf-head' }, h('h2', { class: 'section-title' }, title), moreHref ? h('a', { class: 'more', href: moreHref, 'aria-label': `See all: ${title}` }, 'See all') : null),
    h('div', { class: 'shelf-scroll' },
      h('button', { class: 'shelf-arrow left hide-mobile', html: icons.chevL, onclick: () => scroll(-1), 'aria-label': 'Scroll left' }),
      track,
      h('button', { class: 'shelf-arrow right hide-mobile', html: icons.chevR, onclick: () => scroll(1), 'aria-label': 'Scroll right' })));
}

// ---------- home ----------
async function homeView() {
  const data = await api('/api/home');
  const wrap = h('div', { style: { paddingTop: '26px', paddingBottom: '40px' } }, h('h1', { class: 'sr-only' }, 'Home'));
  if (!data.libraries.length) {
    wrap.appendChild(h('div', { class: 'empty' },
      h('h2', null, 'No libraries yet'),
      state.me.isAdmin
        ? [h('p', null, 'Add a folder with your movies or shows to get started.'), h('a', { class: 'btn primary', href: '#/settings/libraries' }, 'Add a library')]
        : h('p', null, 'Ask your server admin to add a library.')));
    return wrap;
  }
  if (!data.rows.length) {
    wrap.appendChild(h('div', { class: 'empty' }, h('h2', null, 'Scanning your libraries…'), h('p', null, 'New items will show up here shortly.'), h('button', { class: 'btn', onclick: () => route() }, 'Refresh')));
  }
  for (const row of data.rows) {
    wrap.appendChild(shelf(row.title, row.items, row.kind, row.libraryId ? `#/library/${row.libraryId}?sort=latest&desc=1` : null));
  }
  return wrap;
}

// ---------- library ----------
async function libraryView(ctx, id) {
  const lib = state.libraries.find((l) => l.id === id) || (await loadLibraries(), state.libraries.find((l) => l.id === id));
  if (!lib) return emptyState({ title: 'Library not found', text: 'It may have been removed.', actions: [h('a', { class: 'btn primary', href: '#/' }, 'Go home')] });
  const q = ctx.query;
  const opts = { sort: q.get('sort') || 'title', desc: q.get('desc') === '1', filter: q.get('filter') || '', genre: q.get('genre') || '' };
  // key: the control to focus once the re-rendered view is in.
  const setQ = (patch, key) => {
    const n = { ...opts, ...patch };
    const p = new URLSearchParams();
    if (n.sort !== 'title') p.set('sort', n.sort);
    if (n.desc) p.set('desc', '1');
    if (n.filter) p.set('filter', n.filter);
    if (n.genre) p.set('genre', n.genre);
    focusAfterRoute(key);
    location.hash = `#/library/${id}${p.toString() ? '?' + p : ''}`;
  };
  const genres = await api(`/api/genres?library=${id}`).catch(() => []);
  const sel = (label, key, value, options, on) => h('select', { 'aria-label': label, dataset: { focusKey: key }, onchange: (e) => on(e.target.value) }, options.map(([v, l]) => h('option', { value: v, selected: v === value }, l)));
  const count = h('span', { class: 'count' });
  const grid = h('div', { class: 'grid' });
  const sentinel = h('div', { style: { height: '40px' } });
  const random = h('button', { class: 'btn sm', title: 'Play a random title', disabled: true, onclick: (e) => run(e.currentTarget, async () => {
    const p = new URLSearchParams({ library: id, sort: 'random', limit: 1 });
    if (opts.filter) p.set('filter', opts.filter);
    if (opts.genre) p.set('genre', opts.genre);
    const r = await api(`/api/items?${p}`);
    if (r.items[0]) location.hash = `#/item/${r.items[0].id}`;
  }) }, h('span', { html: icons.shuffle }), 'Random');
  const page = h('div', { class: 'page' },
    h('div', { class: 'row' }, h('h1', { class: 'page-title' }, lib.name), count),
    h('div', { class: 'toolbar' },
      sel('Sort titles', 'sort-select', opts.sort, [['title', 'Title'], ['year', 'Year'], ['added', 'Date added'], ['latest', 'Latest episode/added'], ['rating', 'Rating'], ['played', 'Recently watched'], ['random', 'Random']], (v) => setQ({ sort: v, desc: ['added', 'latest', 'rating', 'played', 'year'].includes(v) }, 'sort-select')),
      h('button', { class: 'btn sm', title: 'Reverse order', dataset: { focusKey: 'sort-dir' }, onclick: () => setQ({ desc: !opts.desc }, 'sort-dir') }, opts.desc ? '↓ Desc' : '↑ Asc'),
      sel('Filter titles', 'filter-select', opts.filter, [['', 'All'], ['unplayed', 'Unwatched'], ['played', 'Watched'], ['inprogress', 'In progress'], ['favorite', 'Favorites']], (v) => setQ({ filter: v }, 'filter-select')),
      genres.length ? sel('Filter by genre', 'genre-select', opts.genre, [['', 'All genres'], ...genres.map((g) => [g, g])], (v) => setQ({ genre: v }, 'genre-select')) : null,
      h('div', { class: 'spacer' }),
      random),
    grid, sentinel);
  let offset = 0, total = Infinity, loading = false;
  const PAGE = 120;
  const load = async () => {
    if (loading || offset >= total || !ctx.isCurrent()) return;
    loading = true;
    const p = new URLSearchParams({ library: id, sort: opts.sort, limit: PAGE, offset });
    if (opts.desc) p.set('desc', '1');
    if (opts.filter) p.set('filter', opts.filter);
    if (opts.genre) p.set('genre', opts.genre);
    try {
      const r = await api(`/api/items?${p}`);
      if (!ctx.isCurrent()) return;
      total = r.total;
      random.disabled = total === 0;
      count.textContent = `${total} item${total === 1 ? '' : 's'}`;
      r.items.forEach((it) => grid.appendChild(posterCard(it)));
      offset += r.items.length;
      if (!r.items.length) total = offset;
      if (total === 0) grid.replaceWith(h('div', { class: 'empty' },
        h('h2', null, opts.filter === 'favorite' && !opts.genre ? 'No favorites yet' : 'Nothing here'),
        h('p', null, opts.filter === 'favorite' && !opts.genre ? 'Open a title and select the heart to add it to your favorites.' : opts.filter || opts.genre ? 'No titles match these filters.' : 'The library is empty or still scanning.'),
        opts.filter || opts.genre ? h('button', { class: 'btn', onclick: () => setQ({ filter: '', genre: '' }, 'filter-select') }, 'Clear filters') : null));
    } finally {
      loading = false;
    }
  };
  // The first page fails the whole view (the router shows the error); later
  // pages leave a retry button, since the observer won't fire again while
  // the sentinel stays in view.
  await load();
  // Back to a scrolled grid: load as many pages as were showing. A failure
  // here just stops early; scrolling down retries through the sentinel.
  try {
    while (ctx.restore && offset < ctx.restore.loaded && offset < total && ctx.isCurrent()) await load();
  } catch {}
  const more = async () => {
    if (loading) return;
    clear(sentinel);
    try { await load(); } catch (e) {
      if (!ctx.isCurrent()) return;
      toast(e.message, 'error');
      sentinel.appendChild(h('div', { class: 'row', style: { justifyContent: 'center' } },
        h('button', { class: 'btn sm', onclick: more }, 'Load more')));
    }
  };
  const io = new IntersectionObserver((ents) => { if (ents[0].isIntersecting) more(); }, { rootMargin: '800px' });
  io.observe(sentinel);
  return page;
}

// ---------- item detail ----------
async function itemView(ctx, id) {
  const d = await api(`/api/items/${id}`);
  const it = d.item;
  if (it.kind === 'season') {
    location.replace(`#/item/${it.showId}?season=${it.id}`);
    return h('div');
  }
  return it.kind === 'show' ? showView(ctx, d) : mediaView(ctx, d);
}

function hero(it, posterNode, info, backdropItem = it) {
  const bgImg = lazyImg(img(backdropItem, 'backdrop', 1280), '', () => {
    // No backdrop: blur the poster instead.
    const alt = lazyImg(img(it, 'poster', 480), '');
    alt.classList.add('blur');
    bg.appendChild(alt);
  });
  const bg = h('div', { class: 'hero-bg' }, bgImg);
  return h('section', { class: 'hero' }, bg, h('div', { class: 'hero-inner' }, posterNode, h('div', { class: 'hero-info' }, info)));
}

function heroPoster(it, kind, land) {
  const box = h('div', { class: `hero-poster ${land ? 'land' : ''}`, style: { position: 'relative' } }, h('div', { class: 'ph', style: { position: 'absolute', inset: 0, display: 'grid', placeItems: 'center', padding: '16px', textAlign: 'center', color: 'var(--text3)', fontWeight: 700, fontSize: '18px' } }, it.title));
  const im = lazyImg(img(it, kind, land ? 640 : 480), it.title);
  im.style.position = 'relative';
  box.appendChild(im);
  return box;
}

function facts(it, extra = []) {
  const f = [];
  if (it.year && it.kind !== 'episode') f.push(h('span', null, it.year));
  if (it.premiere && it.kind === 'episode') f.push(h('span', null, new Date(it.premiere).toLocaleDateString(undefined, { dateStyle: 'medium' })));
  if (it.duration) f.push(h('span', null, fmtDuration(it.duration)));
  else if (it.runtime) f.push(h('span', null, `${it.runtime}m`));
  if (it.contentRating) f.push(h('span', { class: 'pill' }, it.contentRating));
  if (it.rating) f.push(h('span', null, h('span', { class: 'star' }, '★ '), it.rating.toFixed(1)));
  return h('div', { class: 'facts' }, f, extra);
}

function playButtons(it, playId = it.id, label = 'Play') {
  const ud = it.userData || {};
  const btns = [];
  if (ud.position > 0 && !ud.played) {
    btns.push(h('button', { class: 'btn primary lg', dataset: { focusKey: `play-${playId}` }, onclick: () => play(playId, ud.position) }, h('span', { html: icons.play }), `Resume from ${fmtTime(ud.position)}`));
    btns.push(h('button', { class: 'btn lg', dataset: { focusKey: `restart-${playId}` }, onclick: () => play(playId, 0) }, 'Play from start'));
  } else {
    btns.push(h('button', { class: 'btn primary lg', dataset: { focusKey: `play-${playId}` }, onclick: () => play(playId, 0) }, h('span', { html: icons.play }), label));
  }
  return btns;
}

function watchedButton(it, onDone) {
  const played = it.kind === 'show' ? it.unplayedCount === 0 && it.childCount > 0 : it.userData?.played;
  return h('button', { class: 'btn lg icon', title: played ? 'Mark unwatched' : 'Mark watched', style: played ? { color: 'var(--good)' } : null, html: icons.check, onclick: async (e) => {
    if (await run(e.currentTarget, () => api(`/api/items/${it.id}/played`, { method: 'POST', body: { played: !played } }), played ? 'Marked unwatched' : 'Marked watched')) onDone();
  } });
}

function favButton(it) {
  let fav = !!it.userData?.favorite, busy = false;
  const show = () => {
    b.innerHTML = fav ? icons.heartFill : icons.heart;
    b.style.color = fav ? 'var(--bad)' : '';
  };
  const b = h('button', { class: 'btn lg icon', title: 'Favorite', html: fav ? icons.heartFill : icons.heart, style: fav ? { color: 'var(--bad)' } : null, onclick: async () => {
    if (busy) return;
    busy = true;
    fav = !fav;
    show();
    try {
      await api(`/api/items/${it.id}/favorite`, { method: 'POST', body: { favorite: fav } });
    } catch (ex) {
      fav = !fav;
      show();
      toast(ex.message, 'error');
    } finally {
      busy = false;
    }
  } });
  return b;
}

function moreButton(it, d) {
  return h('button', { class: 'btn lg icon', title: 'More', html: icons.more, onclick: (e) => {
    const items = [];
    if (d.files?.length) items.push({ label: 'Media info', icon: 'info', onClick: () => mediaInfoModal(d.files) });
    if (state.me.isAdmin) {
      if (it.kind === 'movie' || it.kind === 'show') items.push({ label: 'Fix match…', icon: 'edit', onClick: () => fixMatch(it) });
      items.push({ label: 'Refresh metadata', icon: 'refresh', onClick: async () => {
        toast('Refreshing metadata…');
        if (await run(null, () => api(`/api/items/${it.id}/refresh`, { method: 'POST' }), 'Metadata refreshed')) route();
      } });
      if (it.metaLocked) items.push({ label: 'Unmatch (auto-match again)', icon: 'refresh', onClick: () => run(null, () => api(`/api/items/${it.id}/unmatch`, { method: 'POST' }), 'Unmatched; re-matching in background') });
      const cached = d.files?.length && d.files.every((f) => f.cached);
      items.push('-');
      if (cached) {
        items.push({ label: 'Remove from SSD cache', icon: 'trash', onClick: async () => { if (await run(null, () => api(`/api/admin/cache/items/${it.id}`, { method: 'DELETE' }), 'Removed from cache')) route(); } });
      } else {
        items.push({ label: it.kind === 'show' ? 'Cache all episodes on SSD' : 'Cache on SSD', icon: 'devices', onClick: async () => {
          await run(null, () => api(`/api/admin/cache/items/${it.id}`, { method: 'POST' }), (r) => `Queued ${r.queued} file${r.queued === 1 ? '' : 's'} for the SSD cache`);
        } });
      }
      if (it.kind === 'show' || it.kind === 'episode') {
        items.push({ label: 'Re-detect intro', icon: 'refresh', onClick: () => run(null, () => api(`/api/items/${it.id}/intro/reset`, { method: 'POST' }), 'Intro detection queued') });
      }
    }
    if (items.length) popupMenu(e.currentTarget, items);
  } });
}

function castRow(it, excludeDirectors = false) {
  const people = (it.cast || []).map((person, index) => ({ person, index })).filter(({ person }) => !excludeDirectors || person.role !== 'Director');
  if (!people.length) return null;
  return h('section', { class: 'shelf' },
    h('div', { class: 'shelf-head' }, h('h2', { class: 'section-title' }, 'Cast & crew')),
    h('div', { class: 'people' }, people.map(({ person: p, index }) => {
      const portrait = h('div', { class: 'ph' }, p.name.slice(0, 1));
      if (p.image) portrait.replaceChildren(lazyImg(castImg(it, index), p.name, () => { portrait.textContent = p.name.slice(0, 1); }));
      return h('div', { class: 'person' }, portrait, h('div', { class: 'n' }, p.name), h('div', { class: 'r' }, p.role || ''));
    })));
}

function mediaView(ctx, d) {
  const it = d.item;
  const isEp = it.kind === 'episode';
  const poster = heroPoster(it, isEp ? 'thumb' : 'poster', isEp);
  const file = d.files?.[0];
  const info = [];
  if (isEp && d.show) {
    info.push(h('a', { class: 'kicker', href: `#/item/${d.show.id}?season=${it.parentId}` }, `${d.show.title} · Season ${it.season}`));
  }
  info.push(h('h1', null, it.title));
  info.push(facts(it, [
    isEp ? h('span', null, fmtEpisode(it)) : null,
    file ? h('span', { class: 'pill' }, [resLabel(file.width, file.height), file.hdr].filter(Boolean).join(' ') || file.container) : null,
    file?.cached ? h('span', { class: 'pill', title: 'Plays from the SSD cache' }, 'SSD') : null,
    d.segments?.find((s) => s.kind === 'intro') ? h('span', { class: 'pill', title: 'Intro detected' }, 'Intro ✓') : null,
  ]));
  if (it.genres?.length) info.push(h('div', { class: 'genres' }, it.genres.map((g) => h('span', null, g))));
  if (!file) info.push(h('p', { class: 'bad' }, 'No media file found for this item.'));
  info.push(h('div', { class: 'actions' },
    file ? playButtons(it) : null,
    watchedButton(it, () => route()),
    it.kind === 'movie' ? favButton(it) : null,
    moreButton(it, d)));
  if (it.tagline) info.push(h('div', { class: 'tagline' }, it.tagline));
  info.push(h('p', { class: 'overview' }, it.overview || (it.metaStatus === 0 ? 'Fetching details…' : 'No description available.')));
  if (file) info.push(fileSummary(file));
  if (isEp) {
    info.push(h('div', { class: 'row', style: { marginTop: '18px' } },
      d.prev ? h('a', { class: 'btn sm', href: `#/item/${d.prev.id}` }, h('span', { html: icons.chevL }), fmtEpisode(d.prev)) : null,
      d.next ? h('a', { class: 'btn sm', href: `#/item/${d.next.id}` }, `Next: ${fmtEpisode(d.next)} ${d.next.title}`, h('span', { html: icons.chevR })) : null));
  }
  const director = (it.cast || []).filter((p) => p.role === 'Director');
  if (director.length) info.push(h('p', { class: 'muted small' }, `Directed by ${director.map((p) => p.name).join(', ')}`));
  if (it.studios?.length) info.push(h('p', { class: 'dim small', style: { marginTop: '4px' } }, it.studios.slice(0, 4).join(' · ')));
  return h('div', null, hero(it, poster, info, isEp && d.show ? d.show : it), castRow(it, true));
}

function fileSummary(f) {
  const s = f.info?.streams || [];
  const v = s.find((x) => x.type === 'video');
  const auds = s.filter((x) => x.type === 'audio');
  const bits = [];
  if (v) bits.push(`${v.codec.toUpperCase()} ${v.width}×${v.height}${v.bitDepth > 8 ? ` ${v.bitDepth}-bit` : ''}`);
  if (auds.length) bits.push(`${auds.length} audio (${[...new Set(auds.map((a) => langName(a.language)))].slice(0, 3).join(', ')})`);
  if (f.subtitles?.length) bits.push(`${f.subtitles.length} subtitle${f.subtitles.length > 1 ? 's' : ''}`);
  bits.push(fmtBytes(f.size));
  if (f.bitrate) bits.push(fmtBitrate(f.bitrate));
  return h('p', { class: 'dim small', style: { marginTop: '10px' } }, bits.join(' · '));
}

function mediaInfoModal(files) {
  const body = files.map((f) => {
    const info = f.info || {};
    const blocks = [h('div', { class: 'panel' }, h('h3', null, 'File'), h('dl', { class: 'kv' },
      h('dt', null, 'Name'), h('dd', null, f.name),
      f.path ? [h('dt', null, 'Path'), h('dd', { class: 'mono' }, f.path)] : null,
      h('dt', null, 'Size'), h('dd', null, fmtBytes(f.size)),
      h('dt', null, 'Container'), h('dd', null, info.format || f.container || '?'),
      h('dt', null, 'Duration'), h('dd', null, fmtTime(info.duration || f.duration)),
      h('dt', null, 'Bitrate'), h('dd', null, fmtBitrate(info.bitrate || f.bitrate)),
      f.probeError ? [h('dt', null, 'Probe error'), h('dd', { class: 'bad' }, f.probeError)] : null))];
    for (const s of info.streams || []) {
      const rows = [['Codec', `${s.codec}${s.profile ? ` (${s.profile})` : ''}${s.codecString ? ` · ${s.codecString}` : ''}`]];
      if (s.type === 'video') rows.push(['Resolution', `${s.width}×${s.height}`], ['Frame rate', s.frameRate ? s.frameRate.toFixed(3) : '?'], ['Bit depth', s.bitDepth], ['Pixel format', s.pixFmt], ['HDR', s.hdr || 'SDR'], s.dvProfile ? ['Dolby Vision', `profile ${s.dvProfile}, compat ${s.dvCompat}`] : null);
      if (s.type === 'audio') rows.push(['Channels', `${channelName(s.channels)} ${s.channelLayout || ''}`], ['Sample rate', s.sampleRate ? `${s.sampleRate} Hz` : '?']);
      if (s.language) rows.push(['Language', langName(s.language)]);
      if (s.title) rows.push(['Title', s.title]);
      if (s.bitrate) rows.push(['Bitrate', fmtBitrate(s.bitrate)]);
      const flags = [s.default && 'default', s.forced && 'forced', s.type === 'subtitle' && (s.textSub ? 'text' : 'image')].filter(Boolean);
      if (flags.length) rows.push(['Flags', flags.join(', ')]);
      blocks.push(h('div', { class: 'panel' }, h('h3', null, `${s.type} #${s.index}`), h('dl', { class: 'kv' }, rows.filter(Boolean).map(([k, v]) => [h('dt', null, k), h('dd', null, String(v ?? ''))]))));
    }
    for (const s of (f.subtitles || []).filter((x) => x.external)) {
      blocks.push(h('div', { class: 'panel' }, h('h3', null, 'external subtitle'), h('p', { class: 'small', style: { margin: 0 } }, streamLabel(s))));
    }
    if (info.chapters?.length) blocks.push(h('div', { class: 'panel' }, h('h3', null, `${info.chapters.length} chapters`), h('dl', { class: 'kv' }, info.chapters.slice(0, 40).map((c) => [h('dt', null, fmtTime(c.start)), h('dd', null, c.title || '')]))));
    return h('div', { class: 'info-grid' }, blocks);
  });
  modal({ title: 'Media info', body, wide: true });
}

async function fixMatch(it) {
  const q = h('input', { type: 'text', value: it.title, style: { flex: 1 } });
  const y = h('input', { type: 'number', value: it.year || '', placeholder: 'Year', style: { width: '90px' } });
  const list = h('div', { class: 'match-list' });
  const search = async () => {
    clear(list).appendChild(spinner());
    try {
      const r = await api(`/api/admin/metadata/search?kind=${it.kind}&q=${encodeURIComponent(q.value)}&year=${y.value || ''}`);
      clear(list);
      if (!r.length) list.appendChild(h('p', { class: 'muted' }, 'No results. Adding a TMDB API key in Settings → Metadata gives the best matches.'));
      for (const c of r) {
        list.appendChild(h('div', { class: 'match', onclick: async () => {
          clear(list).appendChild(spinner());
          try {
            await api(`/api/items/${it.id}/match`, { method: 'POST', body: { provider: c.provider, id: c.id } });
            m.close(); toast('Match updated', 'ok'); route();
          } catch (ex) { toast(ex.message, 'error'); search(); }
        } }, c.poster ? h('img', { src: c.poster, alt: '' }) : h('div', { class: 'noimg' }),
        h('div', null, h('b', null, `${c.title} ${c.year ? `(${c.year})` : ''}`), h('div', { class: 'dim small' }, `${c.provider} #${c.id}`), h('p', null, c.overview || ''))));
      }
    } catch (ex) { clear(list).appendChild(h('p', { class: 'bad' }, ex.message)); }
  };
  const m = modal({ title: `Fix match: ${it.title}`, wide: true, body: [
    h('form', { class: 'row', onsubmit: (e) => { e.preventDefault(); search(); } }, q, y, h('button', { class: 'btn primary' }, 'Search')),
    list] });
  search();
}

// ---------- show ----------
async function showView(ctx, d) {
  const it = d.item;
  const seasons = d.children || [];
  const poster = heroPoster(it, 'poster', false);
  const next = d.nextEpisode;
  const info = [h('h1', null, it.title),
    facts(it, [h('span', null, `${seasons.filter((s) => s.season > 0).length} season${seasons.length === 1 ? '' : 's'}`), it.studios?.length ? h('span', null, it.studios[0]) : null])];
  if (it.genres?.length) info.push(h('div', { class: 'genres' }, it.genres.map((g) => h('span', null, g))));
  const btns = [];
  if (next) {
    const lbl = `${next.userData?.position > 0 ? 'Resume' : 'Play'} ${fmtEpisode(next)}`;
    btns.push(h('button', { class: 'btn primary lg', dataset: { focusKey: `show-play-${it.id}` }, onclick: () => play(next.id) }, h('span', { html: icons.play }), lbl));
  }
  btns.push(watchedButton(it, () => route()), favButton(it), moreButton(it, d));
  info.push(h('div', { class: 'actions' }, btns));
  if (next) info.push(h('p', { class: 'dim small' }, `Up next: ${next.title}`));
  info.push(h('p', { class: 'overview' }, it.overview || (it.metaStatus === 0 ? 'Fetching details…' : 'No description available.')));

  const listId = `episodes-${it.id}`;
  const tabs = h('div', { class: 'tabs', role: 'group', 'aria-label': 'Seasons' });
  const list = h('div', { class: 'episodes', id: listId, role: 'region', 'aria-label': 'Episodes' });
  const wanted = +ctx.query.get('season');
  let active = seasons.find((s) => s.id === wanted) || (next && seasons.find((s) => s.id === next.parentId)) || seasons.find((s) => s.season > 0) || seasons[0];
  const showSeason = async (s) => {
    active = s;
    for (const b of tabs.children) {
      const selected = +b.dataset.id === s.id;
      b.classList.toggle('active', selected);
      b.setAttribute('aria-pressed', String(selected));
    }
    clear(list).appendChild(spinner());
    list.setAttribute('aria-busy', 'true');
    try {
      const eps = await api(`/api/items/${s.id}/children`);
      if (active !== s || !ctx.isCurrent()) return;
      clear(list).append(...eps.map((e) => episodeRow(e)), ...(s.overview ? [h('p', { class: 'muted', style: { maxWidth: '760px' } }, s.overview)] : []));
      replaceHash(`#/item/${it.id}?season=${s.id}`);
    } catch (e) {
      if (active === s && ctx.isCurrent()) clear(list).append(h('p', { class: 'bad', role: 'alert' }, e.message), h('button', { class: 'btn', onclick: () => showSeason(s) }, 'Retry'));
    } finally {
      if (active === s) list.setAttribute('aria-busy', 'false');
    }
  };
  for (const s of seasons) {
    tabs.appendChild(h('button', { dataset: { id: s.id }, 'aria-controls': listId, 'aria-pressed': 'false', 'aria-label': `${s.title}${s.unplayedCount ? `, ${s.unplayedCount} unwatched episode${s.unplayedCount === 1 ? '' : 's'}` : ''}`, onclick: () => showSeason(s) }, s.title, s.unplayedCount ? h('span', { class: 'dim', 'aria-hidden': 'true', style: { marginLeft: '6px', fontWeight: 600 } }, s.unplayedCount) : null));
  }
  const page = h('div', null, hero(it, poster, info),
    h('div', { class: 'page', style: { paddingTop: 0 } }, tabs, list),
    castRow(it));
  if (active) await showSeason(active);
  return page;
}

function episodeRow(e) {
  const ud = e.userData || {};
  let busy = false;
  const art = h('button', { class: 'art', type: 'button', dataset: { focusKey: `episode-play-${e.id}` }, 'aria-label': `${ud.position > 0 && !ud.played ? 'Resume' : 'Play'} episode ${e.episode}: ${e.title}`, onclick: () => play(e.id) }, lazyImg(img(e, 'thumb', 480), ''), h('span', { class: 'ep-play', 'aria-hidden': 'true', html: icons.play }));
  if (ud.position > 0 && e.duration) art.appendChild(h('div', { class: 'progress' }, h('i', { style: { width: `${(ud.position / e.duration) * 100}%` } })));
  const wbtn = h('button', { class: `watched-btn ${ud.played ? 'on' : ''}`, title: ud.played ? 'Mark unwatched' : 'Mark watched', html: icons.check, onclick: async (ev) => {
    ev.stopPropagation();
    if (busy) return;
    busy = true;
    const show = () => {
      wbtn.classList.toggle('on', ud.played);
      wbtn.title = ud.played ? 'Mark unwatched' : 'Mark watched';
    };
    ud.played = !ud.played;
    show();
    try {
      await api(`/api/items/${e.id}/played`, { method: 'POST', body: { played: ud.played } });
    } catch (ex) {
      ud.played = !ud.played;
      show();
      toast(ex.message, 'error');
    } finally {
      busy = false;
    }
  } });
  return h('div', { class: 'episode' },
  art,
  h('a', { class: 'episode-info', href: `#/item/${e.id}`, 'aria-label': `Episode ${e.episode}: ${e.title}` },
    h('h3', null, h('span', { class: 'num' }, `${e.episode}${e.episodeEnd ? '–' + e.episodeEnd : ''}.`), e.title),
    h('div', { class: 'dim small' }, [e.premiere ? new Date(e.premiere).toLocaleDateString(undefined, { dateStyle: 'medium' }) : '', e.duration ? fmtDuration(e.duration) : '', ud.position > 0 && !ud.played ? `${fmtDuration(e.duration - ud.position)} left` : ''].filter(Boolean).join(' · ')),
    e.overview ? h('p', null, e.overview) : null),
  h('div', { class: 'side' }, wbtn));
}

// ---------- search ----------
async function searchView(ctx, q) {
  // Leave the box alone while it holds this query (e.g. with a trailing space being typed).
  if (searchInput && searchInput.value.trim() !== q) searchInput.value = q;
  const r = await api(`/api/search?q=${encodeURIComponent(q)}`);
  const page = h('div', { class: 'page' }, h('h1', { class: 'page-title' }, `Results for “${q}”`));
  const total = r.movies.length + r.shows.length + r.episodes.length;
  if (!total) page.appendChild(h('div', { class: 'empty' }, h('h2', null, 'No matches'), h('p', null, 'Try a different title.')));
  if (r.shows.length) page.append(h('h2', { class: 'section-title', style: { margin: '24px 0 14px' } }, 'Shows'), h('div', { class: 'grid' }, r.shows.map(posterCard)));
  if (r.movies.length) page.append(h('h2', { class: 'section-title', style: { margin: '24px 0 14px' } }, 'Movies'), h('div', { class: 'grid' }, r.movies.map(posterCard)));
  if (r.episodes.length) page.append(h('h2', { class: 'section-title', style: { margin: '24px 0 14px' } }, 'Episodes'), h('div', { class: 'grid land' }, r.episodes.map((e) => landCard(e, { playOnClick: false }))));
  return page;
}

boot();
