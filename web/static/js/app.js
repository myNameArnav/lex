import './theme.js'; // first: sets the theme before anything renders
import { h, clear, icons, toast, spinner, popupMenu, closePopupMenu, confirmDialog, emptyState } from './ui.js';
import { api } from './api.js';
import { openPlayer, isPlayerOpen, closePlayer, warmPlayer } from './player-loader.js';
import { installShortcuts, showShortcuts, MOD } from './shortcuts.js';
import { homeView } from './views/home.js';
import { libraryView } from './views/library.js';
import { watchlistView, favoritesView, collectionView, personView } from './views/lists.js';
import { searchView } from './views/search.js';
import { itemView } from './views/title.js';
import { tvView } from './views/tv.js';
// admin.js imports this from here.
export { fixMatch } from './views/title.js';

/** @typedef {import('../../types/api.gen').User} User */
/** @typedef {import('../../types/api.gen').Library} Library */
/** @typedef {import('../../types/api.gen').serverCaps} ServerCaps */

// caps: the optional features this user can use, from the server (see /api/me).
// subsRemaining: OpenSubtitles downloads left today, once a search has said.
/** @type {{ me: User | null, caps: Partial<ServerCaps>, serverName: string, libraries: Library[], version: string, subsRemaining?: number | null }} */
export const state = { me: null, caps: {}, serverName: 'lex', libraries: [], version: '' };
const app = document.getElementById('app');
let mainEl = null, navEl = null, searchInput = null;
let routeToken = 0;

// ---------- boot ----------
installShortcuts({
  // A page with its own search box (Settings) takes the shortcut first; pressed
  // again from there, it goes to the app's search.
  focusSearch: () => {
    const local = mainEl?.querySelector('[data-page-search]');
    const box = local && document.activeElement !== local ? local : searchInput;
    if (box?.isConnected) { box.focus(); box.select(); }
  },
  go: (hash) => { location.hash = hash; },
  libraries: () => state.libraries,
  liveTv: () => !!state.caps.liveTv,
});

// Session expiry (any 401): close whatever is open over the app and ask to
// sign in again; signing in returns to the same route.
window.addEventListener('lex:unauthorized', () => {
  if (!state.me) return;
  state.me = null;
  routeToken++; // invalidates the current view, so its pollers and pending loads stop
  leaveGuard = null;
  document.querySelectorAll('dialog').forEach((d) => d.open && d.close());
  closePopupMenu();
  if (isPlayerOpen()) closePlayer();
  renderAuth(false, 'Your session ended. Sign in again to continue.');
});

// The service worker (sw.js) shows a "Can't reach your Lex server" page
// instead of a browser error when the installed app opens offline. Browsers
// only allow it in a secure context (HTTPS or localhost).
if ('serviceWorker' in navigator && window.isSecureContext) {
  window.addEventListener('load', () => { navigator.serviceWorker.register('/sw.js').catch(() => {}); }, { once: true });
}

// What boot needs comes in one round trip instead of one after another: the
// requests don't depend on each other, and on a remote link each costs as
// much as the server takes to answer all of them. `early` holds the ones a
// later step picks up (takeEarly) until the first view has asked for its own.
const early = new Map();
function fetchEarly(path) {
  const p = api(path);
  p.catch(() => {}); // whoever takes it handles the error
  early.set(path, p);
}
export function takeEarly(path) {
  const p = early.get(path);
  early.delete(path);
  return p || api(path);
}
// earlyViewPath is the request the view at the current hash starts with.
function earlyViewPath() {
  const { parts } = parseHash();
  if (!parts.length) return '/api/home';
  if (parts[0] === 'item' && /^\d+$/.test(parts[1] || '')) return `/api/items/${+parts[1]}`;
  return null;
}

async function boot() {
  try {
    const infoReq = api('/api/public/info');
    const meReq = api('/api/me');
    meReq.catch(() => {});
    fetchEarly('/api/libraries');
    const viewPath = earlyViewPath();
    if (viewPath) fetchEarly(viewPath);
    const info = await infoReq;
    state.serverName = info.serverName;
    state.version = info.version;
    document.title = info.serverName;
    if (info.setupRequired) { early.clear(); return renderAuth(true); }
    const me = await meReq;
    state.me = me.user;
    state.caps = me.caps || {};
    await startApp();
  } catch (e) {
    early.clear();
    if (e.status === 401) return renderAuth(false);
    const down = e.status === 0 || e.status >= 502;
    clear(app).appendChild(h('main', { id: 'main' }, emptyState({
      title: down ? 'Can’t reach lex' : 'Couldn’t start lex', text: down ? 'It may be restarting. Try again in a moment.' : e.message, alert: true,
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
  // aria-label: the plain label is the name. Chrome would read the label's
  // CSS uppercase and the help (already the description) into it.
  const field = (label, input, help) => {
    input.setAttribute('aria-label', label);
    return h('label', { class: 'field', for: input.id }, h('span', null, label), input, help ? h('div', { class: 'help', id: 'auth-password-help' }, help) : null);
  };
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
  h('div', { class: 'logo' }, h('img', { src: '/favicon.svg', alt: '', width: 28, height: 28 }), h('span', null, state.serverName)),
  h('h1', null, setup ? 'Create the admin account' : 'Sign in'),
  notice ? h('p', { class: 'muted', id: 'auth-notice', role: 'status', style: { margin: 0 } }, notice) : null,
  setup ? h('p', { class: 'muted', style: { margin: 0 } }, 'This account manages libraries, users and settings.') : null,
  field('Username', name), field('Password', pass, setup ? 'At least 12 characters.' : null), pass2 ? field('Confirm password', pass2) : null, err, btn);
  app.appendChild(h('main', { class: 'auth' }, form));
  name.focus();
}

async function startApp() {
  history.scrollRestoration = 'manual';
  // Reloaded with the player open: its entry is now a plain page entry.
  if (history.state?.lexPlayer) try { history.replaceState({ ...history.state, lexPlayer: false }, ''); } catch {}
  await loadLibraries();
  renderShell();
  // The URL being left, for the leave guard to put back (it may differ from
  // the routed hash after in-page replaceState, e.g. the season tabs).
  window.onhashchange = (e) => {
    // The leave guard stepping back to the page it kept: that view is still shown.
    if (reverting != null) {
      const done = location.hash === reverting;
      reverting = null;
      if (done) { lastHash = location.hash; renderNav(); return; }
    }
    if (!guarding) lastHash = new URL(e.oldURL).hash;
    route();
  };
  settled = false;
  route();
  // The first view has made its request by now; later ones fetch afresh.
  early.clear();
  // Then fetch the player in the background, after what's on screen.
  const warm = () => warmPlayer().catch(() => {});
  if ('requestIdleCallback' in window) requestIdleCallback(warm, { timeout: 3000 }); else setTimeout(warm, 1000);
}

export async function loadLibraries() {
  try { state.libraries = await takeEarly('/api/libraries'); } catch { state.libraries = []; }
  renderNav();
}

// ---------- shell ----------
const narrow = matchMedia('(max-width: 520px)');
// Shows q in the header's search box (the search page keeps the two in step).
export function setSearchBox(q) { if (searchInput && searchInput.value.trim() !== q) searchInput.value = q; }
const searchPlaceholder = () => { if (searchInput) searchInput.placeholder = narrow.matches ? 'Search' : 'Search movies & shows'; };
narrow.addEventListener('change', searchPlaceholder);

function renderShell() {
  clear(app);
  navLinks = new Map();
  // tabindex -1: Firefox makes an overflowing scroller a Tab stop of its
  // own, hidden under the fade; its links already are.
  navEl = h('nav', { class: 'nav', 'aria-label': 'Libraries', tabindex: '-1', onscroll: navFade, onfocusin: navReveal });
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
    h('a', { class: 'logo', href: '#/', 'aria-label': `${state.serverName} home` }, h('img', { src: '/favicon.svg', alt: '', width: 28, height: 28 }), h('span', null, state.serverName)),
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

// Nav links are built once per library list (and Live TV setting) and
// updated in place, so focus and the nav's own scroll position survive
// navigation. Settings calls it when Live TV is turned on or off.
let navLinks = new Map(), navSig = '';
export function renderNav() {
  if (!navEl) return;
  const sig = [state.caps.liveTv ? 'tv' : '', ...state.libraries.map((l) => `${l.id}\t${l.name}`)].join('\n');
  if (!navLinks.size || sig !== navSig) {
    navSig = sig;
    navLinks = new Map(/** @type {[string, HTMLAnchorElement][]} */ ([['home', h('a', { href: '#/' }, 'Home')], ...(state.caps.liveTv ? [['tv', h('a', { href: '#/tv' }, 'Live TV')]] : []), ...state.libraries.map((l) => [l.id, h('a', { href: `#/library/${l.id}` }, l.name)])]));
    navEl.replaceChildren(...navLinks.values());
  }
  const cur = location.hash;
  let active = null;
  for (const [id, a] of navLinks) {
    const on = id === 'home' ? cur === '' || cur === '#/' || cur.startsWith('#/?') : id === 'tv' ? cur === '#/tv' : cur === `#/library/${id}` || cur.startsWith(`#/library/${id}?`);
    a.classList.toggle('active', on);
    if (on) { a.setAttribute('aria-current', 'page'); active = a; } else a.removeAttribute('aria-current');
  }
  if (active) centreNavLink(active, 0);
  navFade();
}

// Centre a link the overflowing row hides, or (margin) leaves under a fade
// edge. scrollIntoView could also scroll the page.
function centreNavLink(a, margin) {
  const x = a.offsetLeft - navEl.offsetLeft;
  if (x < navEl.scrollLeft + margin || x + a.offsetWidth > navEl.scrollLeft + navEl.clientWidth - margin) navEl.scrollLeft = x - (navEl.clientWidth - a.offsetWidth) / 2;
}

// A link tabbed to partly in view still counts as visible to the browser,
// so it doesn't scroll: bring it clear of the 32px fades.
function navReveal(e) {
  const a = e.target.closest('a');
  if (a && navEl.scrollWidth > navEl.clientWidth + 1) centreNavLink(a, 32);
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
  /** @type {import('./ui.js').PopupItem[]} */
  const items = [
    { label: 'Watchlist', icon: 'bookmark', onClick: () => { location.hash = '#/watchlist'; } },
    { label: 'Favorites', icon: 'heart', onClick: () => { location.hash = '#/favorites'; } },
    '-',
    { label: 'Settings', icon: 'gear', onClick: () => { location.hash = '#/settings'; } },
  ];
  if (state.me.isAdmin) items.push({ label: 'Dashboard & stats', icon: 'stats', onClick: () => { location.hash = '#/dashboard'; } });
  // Phones have no keyboard; iPads with one (or a trackpad) keep the entry.
  if (matchMedia('(any-pointer: fine)').matches) items.push({ label: 'Keyboard shortcuts', icon: 'keyboard', onClick: showShortcuts });
  items.push('-', { label: 'Sign out', icon: 'logout', onClick: signOut });
  if (state.version) items.push('-', { note: `lex ${state.version}` });
  popupMenu(anchor, items);
}

// The session cookie is HttpOnly, so only the server can end it: if the
// request fails, say so rather than reload into the same signed-in state.
async function signOut() {
  if (leaveGuard?.() && !(await confirmDiscard())) return;
  try {
    await api('/api/auth/logout', { method: 'POST' });
  } catch {
    toast('Couldn’t sign out: lex is unreachable. Try again.', 'error');
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
// Forward onto the entry of a player that has since closed (Escape pops it,
// leaving it ahead): step back over it, so the next Play replaces it and a
// later Back can't land on it.
window.addEventListener('popstate', () => { if (history.state?.lexPlayer && !isPlayerOpen()) history.back(); });
const confirmDiscard = () => confirmDialog({ title: 'Discard changes?', message: 'You have unsaved changes. Leave this page and discard them?', okLabel: 'Discard changes', danger: true });

// focusAfterRoute(key): after the next route, focus the element with
// data-focus-key=key (e.g. the library sort select that changed the hash)
// instead of the page heading.
let pendingFocusKey = null;
export function focusAfterRoute(key) { pendingFocusKey = key; }

let lastHash = '', shownHash = null, lastSection, settled = false, guarding = false, reverting = null;
// Every routed history entry carries its position (state.idx), so a
// cancelled leave guard can return to the entry it left instead of
// rewriting the one it moved to.
let histIdx = -1;

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
      const idx = history.state?.idx;
      if (location.hash !== back) {
        reverting = back;
        // Back/Forward: return to the entry that was left. A link or a typed
        // URL pushed a new entry (no idx yet): drop it.
        if (typeof idx !== 'number') history.back();
        else if (idx !== histIdx) history.go(histIdx - idx);
        else { reverting = null; history.replaceState(history.state, '', back || location.pathname); }
        // history.go can't move if the entry indexes aren't contiguous (e.g.
        // entries from an earlier visit): rewrite the address instead.
        if (reverting != null) {
          const want = reverting;
          setTimeout(() => {
            if (reverting !== want || location.hash === want) return;
            reverting = null;
            history.replaceState(history.state, '', want || location.pathname);
          }, 500);
        }
      }
      // Typing in the search box started this navigation: the page stays.
      if (!(back || '').startsWith('#/search') && searchInput) searchInput.value = '';
      renderNav();
      return;
    }
  }
  leaveGuard = null;
  if (!soft) {
    const st = history.state;
    if (typeof st?.idx === 'number') histIdx = st.idx;
    else try { history.replaceState({ ...st, idx: ++histIdx }, ''); } catch {}
  }
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
  const active = /** @type {HTMLElement | null} */ (document.activeElement);
  const focusKey = pendingFocusKey || (soft ? active?.dataset?.focusKey : null);
  const focusWasInMain = mainEl.contains(active) && active !== mainEl;
  pendingFocusKey = null;
  const ctx = { token, query, restore, isCurrent: () => token === routeToken };
  // A view can stay up while the next one loads (Settings keeps its side
  // column and dims its page) instead of being swapped for a spinner.
  const shown = mainEl.firstElementChild;
  const kept = !soft && typeof shown?.pendingRoute === 'function' && shown.pendingRoute(section, id);
  const spin = soft || kept ? 0 : setTimeout(() => { if (ctx.isCurrent()) clear(mainEl).appendChild(spinner()); }, 200);
  let view;
  try {
    switch (section) {
      case undefined: view = await homeView(ctx); break;
      case 'library': view = await libraryView(ctx, +id); break;
      case 'item': view = await itemView(ctx, +id); break;
      case 'search': view = await searchView(ctx, query.get('q') || ''); break;
      case 'watchlist': view = await watchlistView(); break;
      case 'favorites': view = await favoritesView(); break;
      case 'collection': view = await collectionView(ctx, +id); break;
      case 'person': view = await personView(ctx, query.get('name') || ''); break;
      case 'tv': view = await tvView(ctx); break;
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

export function notFound() {
  return emptyState({ title: 'Not found', text: 'This page doesn’t exist.', actions: [h('a', { class: 'btn primary', href: '#/' }, 'Go home')] });
}

function errorView(e, section) {
  if (e.status === 404) return emptyState({ title: 'Not found', text: section === 'item' ? 'This title isn’t in your library any more.' : 'This page doesn’t exist.', actions: [h('a', { class: 'btn primary', href: '#/' }, 'Go home')] });
  const actions = [h('button', { class: 'btn primary', onclick: () => route() }, 'Try again'), section ? h('a', { class: 'btn', href: '#/' }, 'Go home') : null].filter(Boolean);
  // 0: no response; 502–504: a proxy in front of Lex while it restarts.
  if (e.status === 0 || e.status >= 502) return emptyState({ title: 'Can’t reach lex', text: 'It may be restarting. Try again in a moment.', alert: true, actions });
  return emptyState({ title: 'Couldn’t load this page', text: e.message, alert: true, actions });
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

// refreshLater soft-refreshes the view after ms (a background re-check), but
// only once nothing is open over it: a re-render closes the open menu and
// leaves a dialog's opener detached. onRefresh runs just before.
export function refreshLater(ctx, ms, onRefresh) {
  setTimeout(function tick() {
    if (!ctx.isCurrent()) return;
    if (isPlayerOpen() || document.querySelector('dialog[open], .menu.popup')) return setTimeout(tick, 1000);
    onRefresh?.();
    refreshSoft();
  }, ms);
}

boot();
