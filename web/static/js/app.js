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
  return h('div', { class: 'ph', 'aria-hidden': 'true' }, it.title);
}

const progressBar = (pos, dur) => h('div', { class: 'progress', 'aria-hidden': 'true' }, h('i', { style: { width: `${Math.min(100, (pos / dur) * 100)}%` } }));

export function posterCard(it) {
  const ud = it.userData || {};
  const art = h('div', { class: 'art' }, placeholder(it), lazyImg(img(it, 'poster', 320), ''));
  const parent = it.kind === 'show' || it.kind === 'season';
  const watched = parent ? !it.unplayedCount && it.childCount > 0 : !!ud.played;
  if (parent && it.unplayedCount > 0) art.appendChild(h('div', { class: 'badge', 'aria-hidden': 'true', title: `${it.unplayedCount} unwatched episode${it.unplayedCount === 1 ? '' : 's'}` }, it.unplayedCount));
  else if (watched) art.appendChild(h('div', { class: 'badge check', 'aria-hidden': 'true', title: 'Watched', html: icons.check }));
  const left = ud.position > 0 && it.duration ? `${fmtDuration(it.duration - ud.position)} left` : '';
  if (left) art.appendChild(progressBar(ud.position, it.duration));
  const label = [it.title, subtitleFor(it), parent && it.unplayedCount > 0 ? `${it.unplayedCount} unwatched` : watched ? 'watched' : '', left].filter(Boolean).join(', ');
  return h('a', { class: 'card', href: `#/item/${it.id}`, 'aria-label': label },
    art, h('div', { class: 'meta' }, h('div', { class: 't', title: it.title }, it.title), h('div', { class: 's' }, subtitleFor(it))));
}

// landCard: Continue Watching / Next Up (the artwork plays) and episode
// search results (the card opens the episode).
export function landCard(it, { playOnClick = true } = {}) {
  const ud = it.userData || {};
  const title = it.kind === 'episode' ? (it.showTitle || it.title) : it.title;
  const sub = it.kind === 'episode' ? `${fmtEpisode(it)} — ${it.title}` : [it.year, ud.position && it.duration ? `${fmtDuration(it.duration - ud.position)} left` : ''].filter(Boolean).join(' · ');
  const kids = [placeholder(it), lazyImg(img(it, 'thumb', 480), ''), h('div', { class: 'play-hover', 'aria-hidden': 'true' }, h('span', { html: icons.play })),
    ud.position > 0 && it.duration ? progressBar(ud.position, it.duration) : null];
  // The card's own click handler covers taps beside the art, so the button
  // stops its click there to play only once.
  const art = playOnClick
    ? h('button', { type: 'button', class: 'art', 'aria-label': `${ud.position > 0 ? 'Resume' : 'Play'} ${title}, ${sub}`, dataset: { focusKey: `land-${it.id}` }, onclick: (e) => { e.stopPropagation(); play(it.id); } }, kids)
    : h('div', { class: 'art' }, kids);
  const text = h('div', null, h('a', { class: 't', href: `#/item/${it.id}`, 'aria-label': `${title}, ${sub}`, style: { display: 'block' } }, title), h('div', { class: 's' }, sub));
  const card = h('div', { class: 'card land', title: `${title} — ${sub}`, onclick: (e) => {
    if (e.target.closest('.meta a, .card-more')) return;
    if (playOnClick) play(it.id); else location.hash = `#/item/${it.id}`;
  } }, art, h('div', { class: 'meta' }, text, playOnClick ? landMenuButton(it, title, () => card) : null));
  return card;
}

// The ⋯ on a Continue Watching / Next Up card. Unwatching an in-progress
// title clears its resume point, which takes it off Continue Watching.
function landMenuButton(it, title, card) {
  const ud = it.userData || {};
  const act = async (played, msg) => {
    // The card goes away: keep focus in the row on its neighbour.
    const c = card(), near = c.nextElementSibling || c.previousElementSibling;
    if (!(await run(null, () => api(`/api/items/${it.id}/played`, { method: 'POST', body: { played } }), msg))) return;
    const key = near?.querySelector('[data-focus-key]')?.dataset.focusKey;
    if (key && c.contains(document.activeElement)) focusAfterRoute(key);
    refreshSoft();
  };
  const items = [{ label: 'Mark watched', icon: 'check', onClick: () => act(true, 'Marked watched') }];
  if (ud.position > 0 && !ud.played) items.push({ label: 'Remove from Continue Watching', icon: 'close', onClick: () => act(false, 'Removed from Continue Watching') });
  return h('button', { class: 'btn icon ghost sm card-more', type: 'button', title: 'More', 'aria-label': `More for ${title}`, dataset: { focusKey: `land-more-${it.id}` }, html: icons.more, onclick: (e) => {
    e.stopPropagation();
    popupMenu(e.currentTarget, items);
  } });
}

// scroller wraps a horizontal track with scroll arrows for mouse users. The
// arrows aren't tab stops (focusing a card scrolls it into view) and show
// only while there is more to scroll that way; touch screens swipe instead.
function scroller(track) {
  const go = (dir) => track.scrollBy({ left: dir * track.clientWidth * 0.85, behavior: motionOK() ? 'smooth' : 'auto' });
  const left = h('button', { class: 'shelf-arrow left', type: 'button', tabindex: '-1', hidden: true, html: icons.chevL, onclick: () => go(-1), 'aria-label': 'Scroll left' });
  const right = h('button', { class: 'shelf-arrow right', type: 'button', tabindex: '-1', hidden: true, html: icons.chevR, onclick: () => go(1), 'aria-label': 'Scroll right' });
  const sync = () => {
    left.hidden = track.scrollLeft <= 0;
    right.hidden = track.scrollLeft >= track.scrollWidth - track.clientWidth - 1;
  };
  track.addEventListener('scroll', sync, { passive: true });
  // Window resizes, the view going in (it may be built before it is
  // attached; Firefox reports that first) and the view being swapped out
  // (the observer then lets go).
  let shown = false;
  const ro = new ResizeObserver(() => {
    if (track.isConnected) { shown = true; sync(); } else if (shown) ro.disconnect();
  });
  ro.observe(track);
  requestAnimationFrame(sync);
  return h('div', { class: 'shelf-scroll' }, left, track, right);
}

function shelf(title, items, kind, moreHref) {
  const track = h('div', { class: 'shelf-track' }, items.map((it) => kind === 'landscape' ? landCard(it) : posterCard(it)));
  return h('section', { class: 'shelf' },
    h('div', { class: 'shelf-head' }, h('h2', { class: 'section-title' }, title), moreHref ? h('a', { class: 'more', href: moreHref, 'aria-label': `See all: ${title}` }, 'See all') : null),
    scroller(track));
}

// ---------- home ----------
// refreshLater soft-refreshes the view after ms (a background re-check), but
// only once nothing is open over it: a re-render closes the open menu and
// leaves a dialog's opener detached. onRefresh runs just before.
function refreshLater(ctx, ms, onRefresh) {
  setTimeout(function tick() {
    if (!ctx.isCurrent()) return;
    if (isPlayerOpen() || document.querySelector('dialog[open], .menu.popup')) return setTimeout(tick, 1000);
    onRefresh?.();
    refreshSoft();
  }, ms);
}

async function homeView(ctx) {
  const data = await api('/api/home');
  const wrap = h('div', { style: { paddingTop: '26px', paddingBottom: '40px' } }, h('h1', { class: 'sr-only' }, 'Home'));
  const admin = state.me.isAdmin;
  if (!data.libraries?.length) {
    wrap.appendChild(emptyState({ level: 'h2', title: 'No libraries yet',
      text: admin ? 'Add a folder with your movies or shows to get started.' : 'Ask your server admin to add a library.',
      actions: admin ? [h('a', { class: 'btn primary', href: '#/settings/libraries' }, 'Add a library')] : [] }));
    return wrap;
  }
  if (!data.rows.length) {
    // A library that has never finished a scan may still fill up: check again shortly.
    if (data.libraries.some((l) => !l.lastScan)) {
      wrap.appendChild(emptyState({ level: 'h2', title: 'Scanning your libraries…', text: 'New titles will show up here as they are found.' }));
      refreshLater(ctx, 5000);
    } else {
      wrap.appendChild(emptyState({ level: 'h2', title: 'No media found',
        text: admin ? 'Check that your library folders contain files, e.g. Movies/Title (Year)/Title (Year).mkv.' : 'Ask your server admin to check the library folders.',
        actions: admin ? [h('a', { class: 'btn primary', href: '#/settings/libraries' }, 'Check library folders')] : [] }));
    }
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
  const opts = { sort: q.get('sort') || 'title', desc: q.get('desc') === '1', filter: q.get('filter') || '', genre: q.get('genre') || '', seed: +q.get('seed') || 0 };
  // Movies have no episodes and shows sort by their newest episode, so each
  // offers the one "newest" order that means something for it.
  const sorts = [['title', 'Title'], ['year', 'Year'],
    ...(lib.kind !== 'shows' ? [['added', 'Date added']] : []), ...(lib.kind !== 'movies' ? [['latest', 'Latest episode']] : []),
    ['rating', 'Rating'], ['played', 'Recently watched'], ['random', 'Random']];
  let fixSort = false;
  if (lib.kind === 'movies' && opts.sort === 'latest') { opts.sort = 'added'; fixSort = true; }
  if (lib.kind === 'shows' && opts.sort === 'added') { opts.sort = 'latest'; fixSort = true; }
  // What the direction button reads for each sort (ascending, descending).
  const dirLabel = { title: ['A→Z', 'Z→A'], year: ['Oldest', 'Newest'], added: ['Oldest', 'Newest'], latest: ['Oldest', 'Newest'], rating: ['Lowest', 'Highest'], played: ['Least recent', 'Most recent'] }[opts.sort];
  // Random order is seeded (and the seed kept in the address), so paging and
  // Back show the same shuffle.
  const newSeed = () => 1 + Math.floor(Math.random() * 1e9);
  const hashFor = (n) => {
    const p = new URLSearchParams();
    if (n.sort !== 'title') p.set('sort', n.sort);
    if (n.desc && n.sort !== 'random') p.set('desc', '1');
    if (n.filter) p.set('filter', n.filter);
    if (n.genre) p.set('genre', n.genre);
    if (n.sort === 'random' && n.seed) p.set('seed', n.seed);
    return `#/library/${id}${p.toString() ? '?' + p : ''}`;
  };
  if (opts.sort === 'random' && !opts.seed) {
    opts.seed = newSeed();
    fixSort = true;
  }
  if (fixSort && ctx.isCurrent()) replaceHash(hashFor(opts));
  // key: the control to focus once the re-rendered view is in.
  const setQ = (patch, key) => {
    focusAfterRoute(key);
    location.hash = hashFor({ ...opts, ...patch });
  };
  const genres = await api(`/api/genres?library=${id}`).catch(() => []);
  const sel = (label, key, value, options, on) => h('select', { class: key, 'aria-label': label, dataset: { focusKey: key }, onchange: (e) => on(e.target.value) }, options.map(([v, l]) => h('option', { value: v, selected: v === value }, l)));
  const count = h('span', { class: 'count' });
  const grid = h('div', { class: 'grid' });
  const sentinel = h('div', { style: { height: '40px' } });
  const random = h('button', { class: 'btn sm', title: 'Open a random title', disabled: true, onclick: async (e) => {
    await run(e.currentTarget, async () => {
      const p = new URLSearchParams({ library: id, sort: 'random', limit: 1 });
      if (opts.filter) p.set('filter', opts.filter);
      if (opts.genre) p.set('genre', opts.genre);
      const r = await api(`/api/items?${p}`);
      if (r.items[0]) location.hash = `#/item/${r.items[0].id}`;
      else toast('No titles match these filters');
    });
    // run() re-enables the button.
    random.disabled = total === 0;
  } }, h('span', { html: icons.shuffle }), 'Random');
  const page = h('div', { class: 'page' },
    h('div', { class: 'row' }, h('h1', { class: 'page-title' }, lib.name), count),
    h('div', { class: 'toolbar' },
      sel('Sort titles', 'sort-select', opts.sort, sorts, (v) => setQ({ sort: v, desc: ['added', 'latest', 'rating', 'played', 'year'].includes(v), seed: v === 'random' ? newSeed() : 0 }, 'sort-select')),
      dirLabel ? h('button', { class: 'btn sm', title: 'Reverse order', 'aria-label': `Sort direction: ${dirLabel[+opts.desc]}`, dataset: { focusKey: 'sort-dir' }, onclick: () => setQ({ desc: !opts.desc }, 'sort-dir') }, dirLabel[+opts.desc]) : null,
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
    if (opts.sort === 'random') p.set('seed', opts.seed);
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
      if (total === 0) {
        const favs = opts.filter === 'favorite' && !opts.genre, filtered = opts.filter || opts.genre;
        // The list loaded at startup may predate the first scan finishing.
        let scanned = lib.lastScan;
        if (!filtered && !scanned) { await loadLibraries(); scanned = state.libraries.find((l) => l.id === id)?.lastScan; }
        grid.replaceWith(emptyState({ level: 'h2',
          title: favs ? 'No favorites yet' : filtered ? 'Nothing here' : scanned ? 'No titles found' : 'Still scanning',
          text: favs ? 'Open a title and select the heart to add it to your favorites.' : filtered ? 'No titles match these filters.' : scanned ? 'No titles found in this library.' : 'This library is still being scanned.',
          actions: filtered ? [h('button', { class: 'btn', onclick: () => setQ({ filter: '', genre: '' }, 'filter-select') }, 'Clear filters')] : [] }));
      }
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

// The h1 names the page, so the artwork and its placeholder stay silent.
function heroPoster(it, kind, land) {
  const box = h('div', { class: `hero-poster ${land ? 'land' : ''}`, style: { position: 'relative' } }, h('div', { class: 'ph', 'aria-hidden': 'true', style: { position: 'absolute', inset: 0, display: 'grid', placeItems: 'center', padding: '16px', textAlign: 'center', color: 'var(--text2)', fontWeight: 700, fontSize: '18px' } }, it.title));
  const im = lazyImg(img(it, kind, land ? 640 : 480), '');
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

// watchedButton marks a title (a show: every episode) watched or unwatched.
// Shows ask first: that clears every resume point at once.
function watchedButton(it) {
  const show = it.kind === 'show';
  // unplayedCount is left out of the JSON when it is 0.
  const played = show ? !it.unplayedCount && it.childCount > 0 : !!it.userData?.played;
  const label = played ? 'Mark as unwatched' : 'Mark as watched';
  return h('button', { class: 'btn lg icon', title: label, 'aria-label': label, 'aria-pressed': String(played), dataset: { focusKey: `watched-${it.id}` }, style: played ? { color: 'var(--good)' } : null, html: icons.check, onclick: async (e) => {
    const b = e.currentTarget;
    if (show && !(await confirmDialog(`Mark all ${it.childCount} episodes of “${it.title}” as ${played ? 'unwatched' : 'watched'}? This clears resume points${played ? ' and watch history' : ''}.`,
      played ? 'Mark unwatched' : 'Mark watched', played, played ? 'Mark show unwatched?' : 'Mark show watched?'))) return;
    if (await run(b, () => api(`/api/items/${it.id}/played`, { method: 'POST', body: { played: !played } }), played ? 'Marked unwatched' : 'Marked watched')) refreshSoft();
  } });
}

// favButton toggles in place (no toast: it is undone with the same button).
function favButton(it) {
  let fav = !!it.userData?.favorite, busy = false;
  const b = h('button', { class: 'btn lg icon', dataset: { focusKey: `fav-${it.id}` }, onclick: async () => {
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
  const show = () => {
    const label = fav ? 'Remove from favorites' : 'Add to favorites';
    b.innerHTML = fav ? icons.heartFill : icons.heart;
    b.style.color = fav ? 'var(--bad)' : '';
    b.setAttribute('aria-pressed', String(fav));
    b.setAttribute('aria-label', label);
    b.title = label;
  };
  show();
  return b;
}

// moreItems lists the ⋯ menu's actions. They depend only on the item, its
// files and the user, so the button is left out when there are none.
function moreItems(it, d) {
  const items = [];
  if (d.files?.length) items.push({ label: 'Media info', icon: 'info', onClick: () => mediaInfoModal(d.files) });
  const ud = it.userData || {};
  if (it.kind !== 'show' && ud.position > 0 && !ud.played) {
    items.push({ label: 'Remove from Continue Watching', icon: 'close', onClick: async () => {
      if (await run(null, () => api(`/api/items/${it.id}/played`, { method: 'POST', body: { played: false } }), 'Removed from Continue Watching')) refreshSoft();
    } });
  }
  if (!state.me.isAdmin) return items;
  if (it.kind === 'movie' || it.kind === 'show') items.push({ label: 'Fix match…', icon: 'edit', onClick: () => fixMatch(it) });
  items.push({ label: 'Refresh metadata', icon: 'refresh', onClick: async () => {
    const key = `refresh-${it.id}`;
    toast('Refreshing metadata…', '', { key, ms: 130000 });
    try {
      await api(`/api/items/${it.id}/refresh`, { method: 'POST' });
    } catch (ex) {
      toast(ex.message, 'error', { key });
      return;
    }
    toast('Metadata refreshed', 'ok', { key });
    refreshSoft();
  } });
  if (it.metaLocked) items.push({ label: 'Unmatch (auto-match again)', icon: 'refresh', onClick: async () => { if (await run(null, () => api(`/api/items/${it.id}/unmatch`, { method: 'POST' }), 'Unmatched; re-matching in background')) refreshSoft(); } });
  const cached = d.files?.length && d.files.every((f) => f.cached);
  items.push('-');
  if (cached) {
    items.push({ label: 'Remove from SSD cache', icon: 'trash', onClick: async () => { if (await run(null, () => api(`/api/admin/cache/items/${it.id}`, { method: 'DELETE' }), 'Removed from cache')) refreshSoft(); } });
  } else if (state.caps?.cacheEnabled === false) {
    // Caching would only fail: point at the setting instead.
    items.push({ label: 'Set up SSD cache…', icon: 'drive', onClick: () => { location.hash = '#/settings/cache'; } });
  } else {
    items.push({ label: it.kind === 'show' ? 'Cache all episodes on SSD' : 'Cache on SSD', icon: 'devices', onClick: async () => {
      await run(null, () => api(`/api/admin/cache/items/${it.id}`, { method: 'POST' }), (r) => `Queued ${r.queued} file${r.queued === 1 ? '' : 's'} for the SSD cache`);
    } });
  }
  if (it.kind === 'show' || it.kind === 'episode') {
    items.push({ label: 'Re-detect intro', icon: 'refresh', onClick: () => run(null, () => api(`/api/items/${it.id}/intro/reset`, { method: 'POST' }), 'Intro detection queued') });
  }
  return items;
}

function moreButton(it, d) {
  const items = moreItems(it, d);
  if (!items.length) return null;
  return h('button', { class: 'btn lg icon', title: 'More actions', 'aria-label': 'More actions', dataset: { focusKey: `more-${it.id}` }, html: icons.more, onclick: (e) => popupMenu(e.currentTarget, items) });
}

function castRow(it, excludeDirectors = false) {
  const people = (it.cast || []).map((person, index) => ({ person, index })).filter(({ person }) => !excludeDirectors || person.role !== 'Director');
  if (!people.length) return null;
  return h('section', { class: 'shelf' },
    h('div', { class: 'shelf-head' }, h('h2', { class: 'section-title' }, 'Cast & crew')),
    scroller(h('div', { class: 'people', tabindex: '0', role: 'region', 'aria-label': 'Cast & crew' }, people.map(({ person: p, index }) => {
      const portrait = h('div', { class: 'ph' }, p.name.slice(0, 1));
      if (p.image) portrait.replaceChildren(lazyImg(castImg(it, index), p.name, () => { portrait.textContent = p.name.slice(0, 1); }));
      return h('div', { class: 'person' }, portrait, h('div', { class: 'n' }, p.name), h('div', { class: 'r' }, p.role || ''));
    }))));
}

// Metadata still being fetched: look once more a little later (once per
// title, so a stuck fetch doesn't poll forever). It counts once the re-check
// runs, so an earlier in-page refresh doesn't use it up.
let metaRetried = 0;
function retryWhileFetching(ctx, it) {
  if (it.metaStatus !== 0 || metaRetried === it.id) return;
  refreshLater(ctx, 8000, () => { metaRetried = it.id; });
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
    watchedButton(it),
    it.kind === 'movie' ? favButton(it) : null,
    moreButton(it, d)));
  if (it.tagline) info.push(h('div', { class: 'tagline' }, it.tagline));
  info.push(h('p', { class: 'overview' }, it.overview || (it.metaStatus === 0 ? 'Fetching details…' : 'No description available.')));
  retryWhileFetching(ctx, it);
  if (file) info.push(fileSummary(file));
  if (isEp) {
    // Long titles truncate inside the button rather than widen the page.
    const epLink = (e, dir) => {
      const label = `${dir}: ${fmtEpisode(e)} ${e.title}`;
      const text = h('span', { class: 'ellipsis' }, label);
      return h('a', { class: 'btn sm', href: `#/item/${e.id}`, title: label, style: { maxWidth: '100%', minWidth: 0 } },
        dir === 'Previous' ? [h('span', { html: icons.chevL }), text] : [text, h('span', { html: icons.chevR })]);
    };
    info.push(h('div', { class: 'row wrap', style: { marginTop: '18px' } }, d.prev ? epLink(d.prev, 'Previous') : null, d.next ? epLink(d.next, 'Next') : null));
  }
  const director = (it.cast || []).filter((p) => p.role === 'Director');
  if (director.length) info.push(h('p', { class: 'muted small' }, `Directed by ${director.map((p) => p.name).join(', ')}`));
  if (it.studios?.length) info.push(h('p', { class: 'muted small', style: { marginTop: '4px' } }, it.studios.slice(0, 4).join(' · ')));
  return h('div', null, hero(it, poster, info, isEp && d.show ? d.show : it), castRow(it, true));
}

function fileSummary(f) {
  const s = f.info?.streams || [];
  const v = s.find((x) => x.type === 'video');
  const auds = s.filter((x) => x.type === 'audio');
  const bits = [];
  if (v) bits.push(`${v.codec.toUpperCase()} ${v.width}×${v.height}${v.bitDepth > 8 ? ` ${v.bitDepth}-bit` : ''}`);
  if (auds.length) {
    const langs = [...new Set(auds.filter((a) => a.language && a.language !== 'und').map((a) => langName(a.language)))].slice(0, 3);
    bits.push(`${auds.length} audio${langs.length ? ` (${langs.join(', ')})` : ''}`);
  }
  if (f.subtitles?.length) bits.push(`${f.subtitles.length} subtitle${f.subtitles.length > 1 ? 's' : ''}`);
  bits.push(fmtBytes(f.size));
  if (f.bitrate) bits.push(fmtBitrate(f.bitrate));
  return h('p', { class: 'muted small', style: { marginTop: '10px' } }, bits.join(' · '));
}

const CONTAINERS = { 'matroska,webm': 'MKV', 'mov,mp4,m4a,3gp,3g2,mj2': 'MP4' };

function mediaInfoModal(files) {
  const body = files.map((f) => {
    const info = f.info || {};
    const blocks = [h('div', { class: 'panel' }, h('h3', null, 'File'), h('dl', { class: 'kv' },
      h('dt', null, 'Name'), h('dd', null, f.name),
      f.path ? [h('dt', null, 'Path'), h('dd', { class: 'mono' }, f.path)] : null,
      h('dt', null, 'Size'), h('dd', null, fmtBytes(f.size)),
      h('dt', null, 'Container'), h('dd', null, CONTAINERS[info.format] || info.format || f.container || '?'),
      h('dt', null, 'Duration'), h('dd', null, fmtTime(info.duration || f.duration)),
      h('dt', null, 'Bitrate'), h('dd', null, fmtBitrate(info.bitrate || f.bitrate)),
      f.probeError ? [h('dt', null, 'Probe error'), h('dd', { class: 'bad' }, f.probeError)] : null))];
    for (const s of info.streams || []) {
      // The browser codec string stays (it helps debug playback) unless it
      // only repeats the codec name ("ac3 · ac-3").
      const cs = s.codecString && s.codecString.replace(/[^a-z0-9]/gi, '').toLowerCase() !== (s.codec || '').toLowerCase() ? s.codecString : '';
      const rows = [['Codec', `${s.codec}${s.profile ? ` (${s.profile})` : ''}${cs ? ` · ${cs}` : ''}`]];
      if (s.type === 'video') rows.push(['Resolution', `${s.width}×${s.height}`], ['Frame rate', s.frameRate ? s.frameRate.toFixed(3) : '?'], ['Bit depth', s.bitDepth], ['Pixel format', s.pixFmt], ['HDR', s.hdr || 'SDR'], s.dvProfile ? ['Dolby Vision', `profile ${s.dvProfile}, compat ${s.dvCompat}`] : null);
      if (s.type === 'audio') {
        // The layout only when it adds something: "5.1(side)", not "Mono mono".
        const ch = channelName(s.channels), layout = s.channelLayout && s.channelLayout.replace(/\(.*\)/, '').toLowerCase() !== ch.toLowerCase() ? s.channelLayout : '';
        rows.push(['Channels', [ch, layout].filter(Boolean).join(' · ') || '?'], ['Sample rate', s.sampleRate ? `${s.sampleRate} Hz` : '?']);
      }
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
  const q = h('input', { type: 'text', value: it.title, 'aria-label': 'Title to search', style: { flex: '1 1 120px' } });
  const y = h('input', { type: 'number', value: it.year || '', placeholder: 'Year', 'aria-label': 'Year', style: { width: '84px' } });
  const list = h('div', { class: 'match-list' });
  const apply = async (c) => {
    clear(list).appendChild(h('div', { class: 'row' }, h('div', { class: 'spinner sm' }), h('p', { class: 'muted', role: 'status', style: { margin: 0 } }, `Applying ${c.title}…`)));
    try {
      await api(`/api/items/${it.id}/match`, { method: 'POST', body: { provider: c.provider, id: c.id } });
    } catch (ex) {
      toast(ex.message, 'error');
      await search();
      q.focus();
      return;
    }
    m.close();
    toast('Match updated', 'ok');
    refreshSoft();
  };
  const search = async () => {
    clear(list).appendChild(spinner());
    try {
      const r = await api(`/api/admin/metadata/search?kind=${it.kind}&q=${encodeURIComponent(q.value)}&year=${y.value || ''}`);
      clear(list);
      if (!r.length) list.appendChild(h('p', { class: 'muted' }, 'No results. Adding a TMDB API key in Settings → Metadata gives the best matches.'));
      for (const c of r) {
        // Posters come through the server (the page only loads its own images);
        // a failed one leaves the empty frame.
        const art = h('span', { class: 'noimg' }, c.poster ? lazyImg(c.poster, '') : null);
        list.appendChild(h('button', { type: 'button', class: 'match', 'aria-label': `${c.title}${c.year ? ` (${c.year})` : ''}, ${c.provider}`, onclick: () => apply(c) },
          art, h('span', null, h('b', null, `${c.title} ${c.year ? `(${c.year})` : ''}`), h('span', { class: 'dim small' }, `${c.provider} #${c.id}`), h('span', { class: 'ov' }, c.overview || ''))));
      }
    } catch (ex) { clear(list).appendChild(h('p', { class: 'bad', role: 'alert' }, ex.message)); }
  };
  const m = modal({ title: `Fix match: ${it.title}`, wide: true, body: [
    h('form', { class: 'row wrap', onsubmit: (e) => { e.preventDefault(); search(); } }, q, y, h('button', { class: 'btn primary' }, 'Search')),
    list] });
  search();
}

// ---------- show ----------
// heroActions: the show's play button, watched/favorite/more and the
// "Up next" line, rebuilt in place when an episode is toggled in the list.
function heroActions(d) {
  const it = d.item, next = d.nextEpisode;
  const btns = [];
  // A fully watched show starts over from its first episode.
  const done = !it.unplayedCount && it.childCount > 0;
  if (next) {
    const lbl = done ? `Watch again from ${fmtEpisode(next)}` : `${next.userData?.position > 0 ? 'Resume' : 'Play'} ${fmtEpisode(next)}`;
    btns.push(h('button', { class: 'btn primary lg', dataset: { focusKey: `show-play-${it.id}` }, onclick: () => play(next.id, done ? 0 : null) }, h('span', { html: icons.play }), lbl));
  }
  btns.push(watchedButton(it), favButton(it), moreButton(it, d));
  // A placeholder title ("Episode 1") would only repeat the button.
  const upNext = !done && next?.title && !/^Episode \d+$/.test(next.title);
  return h('div', { class: 'hero-cta' }, h('div', { class: 'actions' }, btns), upNext ? h('p', { class: 'muted small' }, `Up next: ${next.title}`) : null);
}

function seasonTabLabel(tab, s) {
  const n = s.unplayedCount || 0;
  tab.setAttribute('aria-label', `${s.title}${n ? `, ${n} unwatched episode${n === 1 ? '' : 's'}` : ''}`);
  tab.replaceChildren(s.title, n ? h('span', { class: 'dim', 'aria-hidden': 'true', style: { marginLeft: '6px', fontWeight: 600 } }, n) : '');
}

async function showView(ctx, d) {
  const it = d.item;
  const seasons = d.children || [];
  const poster = heroPoster(it, 'poster', false);
  const next = d.nextEpisode;
  let cta = heroActions(d);
  const n = seasons.filter((s) => s.season > 0).length;
  const info = [h('h1', null, it.title),
    facts(it, [n ? h('span', null, `${n} season${n === 1 ? '' : 's'}`) : null, it.studios?.length ? h('span', null, it.studios[0]) : null])];
  if (it.genres?.length) info.push(h('div', { class: 'genres' }, it.genres.map((g) => h('span', null, g))));
  info.push(cta);
  info.push(h('p', { class: 'overview' }, it.overview || (it.metaStatus === 0 ? 'Fetching details…' : 'No description available.')));
  retryWhileFetching(ctx, it);

  const listId = `episodes-${it.id}`;
  const tabs = h('div', { class: 'tabs', role: 'group', 'aria-label': 'Seasons' });
  const list = h('div', { class: 'episodes', id: listId, role: 'region', 'aria-label': 'Episodes' });
  const tabFor = (s) => [...tabs.children].find((b) => +b.dataset.id === s.id);
  // An episode toggled in the list: adjust its season's count now, then
  // refetch the show for the play button, Up next and every season count.
  let refetch = 0;
  const episodeChanged = (played) => {
    active.unplayedCount = Math.max(0, (active.unplayedCount || 0) + (played ? -1 : 1));
    seasonTabLabel(tabFor(active), active);
    clearTimeout(refetch);
    refetch = setTimeout(async () => {
      let nd;
      try { nd = await api(`/api/items/${it.id}`); } catch { return; }
      if (!ctx.isCurrent()) return;
      for (const s of nd.children || []) {
        const old = seasons.find((x) => x.id === s.id), tab = tabFor(s);
        if (old && tab) { old.unplayedCount = s.unplayedCount; seasonTabLabel(tab, old); }
      }
      const key = cta.contains(document.activeElement) ? document.activeElement.dataset.focusKey : null;
      const n = heroActions(nd);
      cta.replaceWith(n);
      cta = n;
      if (key) n.querySelector(`[data-focus-key="${key}"]`)?.focus({ preventScroll: true });
    }, 400);
  };
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
      clear(list).append(...(s.overview ? [h('p', { class: 'muted', style: { maxWidth: '760px', margin: '0 0 8px' } }, s.overview)] : []), ...eps.map((e) => episodeRow(e, episodeChanged)));
      replaceHash(`#/item/${it.id}?season=${s.id}`);
    } catch (e) {
      if (active === s && ctx.isCurrent()) clear(list).append(h('p', { class: 'bad', role: 'alert' }, e.message), h('button', { class: 'btn', onclick: () => showSeason(s) }, 'Retry'));
    } finally {
      if (active === s) list.setAttribute('aria-busy', 'false');
    }
  };
  for (const s of seasons) {
    const tab = h('button', { dataset: { id: s.id }, 'aria-controls': listId, 'aria-pressed': 'false', onclick: () => showSeason(s) });
    seasonTabLabel(tab, s);
    tabs.appendChild(tab);
  }
  const page = h('div', null, hero(it, poster, info),
    h('div', { class: 'page', style: { paddingTop: 0 } }, h('h2', { class: 'sr-only' }, 'Episodes'), tabs, list),
    castRow(it));
  if (active) await showSeason(active);
  // Many seasons: bring the selected tab into view once the page is laid out.
  requestAnimationFrame(() => {
    const tab = active && tabFor(active);
    if (tab && tabs.scrollWidth > tabs.clientWidth) tabs.scrollLeft = tab.offsetLeft - tabs.offsetLeft - (tabs.clientWidth - tab.offsetWidth) / 2;
  });
  return page;
}

// episodeRow: onChange(played) runs after the watched toggle succeeds.
function episodeRow(e, onChange) {
  const ud = e.userData || {};
  let busy = false;
  const art = h('button', { class: 'art', type: 'button', dataset: { focusKey: `episode-play-${e.id}` }, 'aria-label': `${ud.position > 0 && !ud.played ? 'Resume' : 'Play'} episode ${e.episode}: ${e.title}`, onclick: () => play(e.id) }, lazyImg(img(e, 'thumb', 480), ''), h('span', { class: 'ep-play', 'aria-hidden': 'true', html: icons.play }));
  if (ud.position > 0 && e.duration) art.appendChild(progressBar(ud.position, e.duration));
  const badge = h('div', { class: 'badge check', 'aria-hidden': 'true', title: 'Watched', html: icons.check });
  const wbtn = h('button', { class: 'watched-btn', type: 'button', html: icons.check, dataset: { focusKey: `episode-watched-${e.id}` }, onclick: async (ev) => {
    ev.stopPropagation();
    if (busy) return;
    busy = true;
    ud.played = !ud.played;
    show();
    try {
      await api(`/api/items/${e.id}/played`, { method: 'POST', body: { played: ud.played } });
      onChange?.(ud.played);
    } catch (ex) {
      ud.played = !ud.played;
      show();
      toast(ex.message, 'error');
    } finally {
      busy = false;
    }
  } });
  const show = () => {
    const action = ud.played ? 'Mark unwatched' : 'Mark watched';
    wbtn.classList.toggle('on', !!ud.played);
    wbtn.title = action;
    wbtn.setAttribute('aria-label', `${action}: episode ${e.episode}, ${e.title}`);
    if (ud.played) art.appendChild(badge); else badge.remove();
  };
  show();
  // The link is named by its heading; date, progress and synopsis are its description.
  const id = `ep-${e.id}`;
  return h('div', { class: 'episode' },
    art,
    h('a', { class: 'episode-info', href: `#/item/${e.id}`, 'aria-labelledby': `${id}-t`, 'aria-describedby': e.overview ? `${id}-m ${id}-o` : `${id}-m` },
      h('h3', { id: `${id}-t` }, h('span', { class: 'num' }, `${e.episode}${e.episodeEnd ? '–' + e.episodeEnd : ''}.`), ' ', e.title),
      h('div', { class: 'dim small', id: `${id}-m` }, [e.premiere ? new Date(e.premiere).toLocaleDateString(undefined, { dateStyle: 'medium' }) : '', e.duration ? fmtDuration(e.duration) : '', ud.position > 0 && !ud.played ? `${fmtDuration(e.duration - ud.position)} left` : ''].filter(Boolean).join(' · ')),
      e.overview ? h('p', { id: `${id}-o` }, e.overview) : null),
    h('div', { class: 'side' }, wbtn));
}

// ---------- search ----------
async function searchView(ctx, q) {
  // Leave the box alone while it holds this query (e.g. with a trailing space being typed).
  if (searchInput && searchInput.value.trim() !== q) searchInput.value = q;
  if (!q.trim()) {
    return h('div', { class: 'page' }, h('h1', { class: 'page-title' }, 'Search'),
      emptyState({ level: 'h2', title: 'Search your movies and shows', text: 'Type a title in the search box above.' }));
  }
  const r = await api(`/api/search?q=${encodeURIComponent(q)}`);
  const total = r.movies.length + r.shows.length + r.episodes.length;
  // Filled a moment after the page is in, so screen readers announce it.
  const status = h('p', { class: 'sr-only', role: 'status' });
  setTimeout(() => { status.textContent = `${total} result${total === 1 ? '' : 's'}`; }, 150);
  const page = h('div', { class: 'page' }, h('h1', { class: 'page-title' }, `Results for “${q}”`), status);
  if (!total) page.appendChild(h('div', { class: 'empty' }, h('h2', null, 'No matches'), h('p', null, 'Try a different title.')));
  // The server sends at most 40 of each kind.
  const group = (title, items, grid) => page.append(h('h2', { class: 'section-title', style: { margin: '24px 0 14px' } }, title), grid,
    ...(items.length >= 40 ? [h('p', { class: 'dim small' }, 'Showing the first 40. Type more of the title to narrow the results.')] : []));
  if (r.shows.length) group('Shows', r.shows, h('div', { class: 'grid' }, r.shows.map(posterCard)));
  if (r.movies.length) group('Movies', r.movies, h('div', { class: 'grid' }, r.movies.map(posterCard)));
  if (r.episodes.length) group('Episodes', r.episodes, h('div', { class: 'grid land' }, r.episodes.map((e) => landCard(e, { playOnClick: false }))));
  return page;
}

boot();
