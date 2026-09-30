import { h, $, clear, icons, resLabel, fmtTime, fmtDuration, fmtBytes, fmtBitrate, toast, modal, spinner, lazyImg, popupMenu, streamLabel, langName, channelName, confirmDialog } from './ui.js';
import { api, img, castImg } from './api.js';
import { openPlayer, isPlayerOpen } from './player.js';
import { installShortcuts, showShortcuts, MOD } from './shortcuts.js';

export const state = { me: null, serverName: 'Lex', libraries: [], version: '' };
const app = document.getElementById('app');
let mainEl = null;
let routeToken = 0;

// ---------- boot ----------
let shortcutsInstalled = false;
async function boot() {
  if (!shortcutsInstalled) {
    shortcutsInstalled = true;
    installShortcuts({
      focusSearch: () => { if (searchInput?.isConnected) { searchInput.focus(); searchInput.select(); } },
      go: (hash) => { location.hash = hash; },
      libraries: () => state.libraries,
    });
  }
  window.addEventListener('lex:unauthorized', () => { if (state.me) { state.me = null; renderAuth(false); } });
  try {
    const info = await api('/api/public/info');
    state.serverName = info.serverName;
    state.version = info.version;
    document.title = info.serverName;
    if (info.setupRequired) return renderAuth(true);
    const me = await api('/api/me');
    state.me = me.user;
    await startApp();
  } catch (e) {
    if (e.status === 401) renderAuth(false);
    else clear(app).appendChild(h('div', { class: 'empty' }, h('h2', null, 'Server unreachable'), h('p', null, e.message), h('button', { class: 'btn', onclick: () => location.reload() }, 'Retry')));
  }
}

function renderAuth(setup) {
  clear(app);
  const name = h('input', { id: 'auth-name', name: 'username', type: 'text', autocomplete: 'username', required: true, autofocus: true, 'aria-describedby': 'auth-error' });
  const pass = h('input', { id: 'auth-password', name: 'password', type: 'password', autocomplete: setup ? 'new-password' : 'current-password', required: true, 'aria-describedby': setup ? 'auth-password-help auth-error' : 'auth-error' });
  const pass2 = setup ? h('input', { id: 'auth-confirm', name: 'confirm-password', type: 'password', autocomplete: 'new-password', required: true, 'aria-describedby': 'auth-error' }) : null;
  const err = h('div', { class: 'err', id: 'auth-error', role: 'alert', 'aria-atomic': 'true' });
  const field = (label, input, help) => h('label', { class: 'field', for: input.id }, h('span', null, label), input, help ? h('div', { class: 'help', id: 'auth-password-help' }, help) : null);
  const invalid = (inputs) => inputs.forEach((input) => input.setAttribute('aria-invalid', 'true'));
  for (const input of [name, pass, pass2].filter(Boolean)) input.addEventListener('input', () => input.removeAttribute('aria-invalid'));
  const btn = h('button', { class: 'btn primary lg', type: 'submit' }, setup ? 'Create admin account' : 'Sign in');
  const form = h('form', { class: 'auth-card', onsubmit: async (e) => {
    e.preventDefault();
    err.textContent = '';
    [name, pass, pass2].filter(Boolean).forEach((input) => input.removeAttribute('aria-invalid'));
    if (setup && pass.value !== pass2.value) { err.textContent = 'Passwords do not match'; invalid([pass, pass2]); pass2.focus(); return; }
    btn.disabled = true;
    try {
      const r = await api(setup ? '/api/setup' : '/api/auth/login', { method: 'POST', body: { name: name.value, password: pass.value } });
      state.me = r.user;
      await startApp();
      if (setup) location.hash = '#/settings/libraries';
    } catch (ex) {
      err.textContent = ex.message;
      if (ex.status === 401) invalid([name, pass]);
      btn.disabled = false;
      pass.focus();
    }
  } },
  h('div', { class: 'logo' }, h('b', null, 'L'), h('span', null, state.serverName)),
  h('h1', null, setup ? 'Welcome! Create the admin account' : 'Sign in'),
  setup ? h('p', { class: 'muted', style: { margin: 0 } }, 'This account manages libraries, users and settings.') : null,
  field('Username', name), field('Password', pass, setup ? 'Use 12–72 bytes.' : null), pass2 ? field('Confirm password', pass2) : null, err, btn);
  app.appendChild(h('div', { class: 'auth' }, form));
  name.focus();
}

async function startApp() {
  await loadLibraries();
  renderShell();
  window.onhashchange = route;
  route();
}

export async function loadLibraries() {
  try { state.libraries = await api('/api/libraries'); } catch { state.libraries = []; }
  renderNav();
}

// ---------- shell ----------
let navEl, searchInput;
function renderShell() {
  clear(app);
  navEl = h('nav', { class: 'nav desktop' });
  searchInput = h('input', { type: 'search', placeholder: 'Search movies & shows', 'aria-label': 'Search movies & shows', 'aria-keyshortcuts': 'Control+K Meta+K /', oninput: debounce((e) => {
    const q = e.target.value.trim();
    if (q) location.hash = `#/search?q=${encodeURIComponent(q)}`;
  }, 300), onkeydown: (e) => {
    if (e.key === 'Escape') { e.target.value = ''; e.target.blur(); }
    if (e.key === 'Enter' && e.target.value.trim()) location.hash = `#/search?q=${encodeURIComponent(e.target.value.trim())}`;
  } });
  const avatarBtn = h('button', { class: 'avatar', title: state.me.name, onclick: (e) => userMenu(e.currentTarget) }, state.me.name.slice(0, 1).toUpperCase());
  const top = h('header', { class: 'topbar' },
    h('a', { class: 'logo', href: '#/' }, h('b', null, 'L'), h('span', null, state.serverName)),
    navEl,
    h('div', { class: 'spacer' }),
    h('div', { class: 'search-box' }, h('span', { html: icons.search }), searchInput, h('kbd', { class: 'search-kbd hide-mobile', title: 'Keyboard shortcuts: press ?' }, `${MOD} K`)),
    state.me.isAdmin ? h('a', { class: 'btn icon ghost hide-mobile', href: '#/dashboard', title: 'Dashboard', html: icons.stats }) : null,
    avatarBtn);
  mainEl = h('main');
  app.append(top, mainEl);
  renderNav();
}

function renderNav() {
  if (!navEl) return;
  const cur = location.hash;
  clear(navEl).append(
    h('a', { href: '#/', class: cur === '' || cur === '#/' ? 'active' : '' }, 'Home'),
    ...state.libraries.map((l) => h('a', { href: `#/library/${l.id}`, class: cur.startsWith(`#/library/${l.id}`) ? 'active' : '' }, l.name)));
}

function userMenu(anchor) {
  const items = [
    { label: 'Settings', icon: 'gear', onClick: () => { location.hash = '#/settings'; } },
  ];
  if (state.me.isAdmin) items.push({ label: 'Dashboard & stats', icon: 'stats', onClick: () => { location.hash = '#/dashboard'; } });
  items.push({ label: 'Keyboard shortcuts', icon: 'keyboard', onClick: showShortcuts });
  items.push('-', { label: 'Sign out', icon: 'logout', onClick: async () => { await api('/api/auth/logout', { method: 'POST' }).catch(() => {}); location.hash = ''; location.reload(); } });
  if (state.version) items.push('-', { note: `Lex ${state.version}` });
  popupMenu(anchor, items);
}

function debounce(fn, ms) {
  let t;
  return (...a) => { clearTimeout(t); t = setTimeout(() => fn(...a), ms); };
}

// ---------- router ----------
function parseHash() {
  const raw = location.hash.replace(/^#/, '') || '/';
  const [path, qs] = raw.split('?');
  return { parts: path.split('/').filter(Boolean), query: new URLSearchParams(qs || '') };
}

export async function route() {
  if (!state.me) return;
  const token = ++routeToken;
  const { parts, query } = parseHash();
  renderNav();
  window.scrollTo(0, 0);
  clear(mainEl).appendChild(spinner());
  const [section, id, sub] = parts;
  if (section !== 'search' && searchInput) searchInput.value = '';
  const ctx = { token, query, isCurrent: () => token === routeToken };
  try {
    let view;
    switch (section) {
      case undefined: view = await homeView(ctx); break;
      case 'library': view = await libraryView(ctx, +id); break;
      case 'item': view = await itemView(ctx, +id); break;
      case 'search': view = await searchView(ctx, query.get('q') || ''); break;
      case 'settings': { const m = await import('./admin.js'); view = await m.settingsView(ctx, id || 'preferences'); break; }
      case 'dashboard': { const m = await import('./admin.js'); view = await m.dashboardView(ctx, id || 'live'); break; }
      default: view = h('div', { class: 'empty' }, h('h2', null, 'Not found'));
    }
    if (!ctx.isCurrent()) return;
    clear(mainEl).appendChild(view);
  } catch (e) {
    if (!ctx.isCurrent()) return;
    clear(mainEl).appendChild(h('div', { class: 'empty' }, h('h2', null, 'Something went wrong'), h('p', null, e.message)));
  }
}

export function play(itemId, start = null) {
  openPlayer({ itemId, start, onClose: () => { if (!isPlayerOpen()) refreshSoft(); } });
}

// Re-render the current view quietly (progress bars etc.) after playback.
async function refreshSoft() {
  const y = window.scrollY;
  const focusKey = document.activeElement?.dataset.focusKey;
  await route();
  window.scrollTo(0, y);
  if (focusKey) [...mainEl.querySelectorAll('[data-focus-key]')].find((el) => el.dataset.focusKey === focusKey)?.focus({ preventScroll: true });
}

// ---------- cards ----------
function subtitleFor(it) {
  if (it.kind === 'show') return it.childCount ? `${it.childCount} episode${it.childCount === 1 ? '' : 's'}` : (it.year || '');
  if (it.kind === 'episode') return `S${it.season} · E${it.episode}`;
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
  const sub = it.kind === 'episode' ? `S${it.season} · E${it.episode} — ${it.title}` : [it.year, ud.position && it.duration ? `${fmtDuration(it.duration - ud.position)} left` : ''].filter(Boolean).join(' · ');
  return h('div', { class: 'card land', title: `${title} — ${sub}`, onclick: (e) => {
    if (e.target.closest('.meta a')) return;
    if (playOnClick) play(it.id); else location.hash = `#/item/${it.id}`;
  } }, art, h('div', { class: 'meta' }, h('a', { class: 't', href: `#/item/${it.id}`, style: { display: 'block' } }, title), h('div', { class: 's' }, sub)));
}

function shelf(title, items, kind, moreHref) {
  const track = h('div', { class: 'shelf-track' }, items.map((it) => kind === 'landscape' ? landCard(it) : posterCard(it)));
  const scroll = (dir) => track.scrollBy({ left: dir * track.clientWidth * 0.85, behavior: 'smooth' });
  return h('section', { class: 'shelf' },
    h('div', { class: 'shelf-head' }, h('h2', { class: 'section-title' }, title), moreHref ? h('a', { class: 'more', href: moreHref }, 'See all') : null),
    h('div', { class: 'shelf-scroll' },
      h('button', { class: 'shelf-arrow left hide-mobile', html: icons.chevL, onclick: () => scroll(-1), 'aria-label': 'Scroll left' }),
      track,
      h('button', { class: 'shelf-arrow right hide-mobile', html: icons.chevR, onclick: () => scroll(1), 'aria-label': 'Scroll right' })));
}

// ---------- home ----------
async function homeView() {
  const data = await api('/api/home');
  const wrap = h('div', { style: { paddingTop: '26px', paddingBottom: '40px' } });
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
  if (!lib) return h('div', { class: 'empty' }, h('h2', null, 'Library not found'));
  const q = ctx.query;
  const opts = { sort: q.get('sort') || 'title', desc: q.get('desc') === '1', filter: q.get('filter') || '', genre: q.get('genre') || '' };
  const setQ = (patch) => {
    const n = { ...opts, ...patch };
    const p = new URLSearchParams();
    if (n.sort !== 'title') p.set('sort', n.sort);
    if (n.desc) p.set('desc', '1');
    if (n.filter) p.set('filter', n.filter);
    if (n.genre) p.set('genre', n.genre);
    location.hash = `#/library/${id}${p.toString() ? '?' + p : ''}`;
  };
  const genres = await api(`/api/genres?library=${id}`).catch(() => []);
  const sel = (label, value, options, on) => h('select', { 'aria-label': label, onchange: (e) => on(e.target.value) }, options.map(([v, l]) => h('option', { value: v, selected: v === value }, l)));
  const count = h('span', { class: 'count' });
  const grid = h('div', { class: 'grid' });
  const sentinel = h('div', { style: { height: '40px' } });
  const random = h('button', { class: 'btn sm', title: 'Play a random title', disabled: true, onclick: async () => {
    const p = new URLSearchParams({ library: id, sort: 'random', limit: 1 });
    if (opts.filter) p.set('filter', opts.filter);
    if (opts.genre) p.set('genre', opts.genre);
    const r = await api(`/api/items?${p}`);
    if (r.items[0]) location.hash = `#/item/${r.items[0].id}`;
  } }, h('span', { html: icons.shuffle }), 'Random');
  const page = h('div', { class: 'page' },
    h('div', { class: 'row' }, h('h1', { class: 'page-title' }, lib.name), count),
    h('div', { class: 'toolbar' },
      sel('Sort titles', opts.sort, [['title', 'Title'], ['year', 'Year'], ['added', 'Date added'], ['latest', 'Latest episode/added'], ['rating', 'Rating'], ['played', 'Recently watched'], ['random', 'Random']], (v) => setQ({ sort: v, desc: ['added', 'latest', 'rating', 'played', 'year'].includes(v) })),
      h('button', { class: 'btn sm', title: 'Reverse order', onclick: () => setQ({ desc: !opts.desc }) }, opts.desc ? '↓ Desc' : '↑ Asc'),
      sel('Filter titles', opts.filter, [['', 'All'], ['unplayed', 'Unwatched'], ['played', 'Watched'], ['inprogress', 'In progress'], ['favorite', 'Favorites']], (v) => setQ({ filter: v })),
      genres.length ? sel('Filter by genre', opts.genre, [['', 'All genres'], ...genres.map((g) => [g, g])], (v) => setQ({ genre: v })) : null,
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
    const r = await api(`/api/items?${p}`);
    total = r.total;
    random.disabled = total === 0;
    count.textContent = `${total} item${total === 1 ? '' : 's'}`;
    r.items.forEach((it) => grid.appendChild(posterCard(it)));
    offset += r.items.length;
    if (!r.items.length) total = offset;
    if (total === 0) grid.replaceWith(h('div', { class: 'empty' },
      h('h2', null, opts.filter === 'favorite' && !opts.genre ? 'No favorites yet' : 'Nothing here'),
      h('p', null, opts.filter === 'favorite' && !opts.genre ? 'Open a title and select the heart to add it to your favorites.' : opts.filter || opts.genre ? 'No titles match these filters.' : 'The library is empty or still scanning.'),
      opts.filter || opts.genre ? h('button', { class: 'btn', onclick: () => setQ({ filter: '', genre: '' }) }, 'Clear filters') : null));
    loading = false;
  };
  await load();
  const io = new IntersectionObserver((ents) => { if (ents[0].isIntersecting) load(); }, { rootMargin: '800px' });
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
  const box = h('div', { class: `hero-poster card-ph ${land ? 'land' : ''}`, style: { position: 'relative' } }, h('div', { class: 'ph', style: { position: 'absolute', inset: 0, display: 'grid', placeItems: 'center', padding: '16px', textAlign: 'center', color: 'var(--text3)', fontWeight: 700, fontSize: '18px' } }, it.title));
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
  return h('button', { class: 'btn lg icon', title: played ? 'Mark unwatched' : 'Mark watched', style: played ? { color: 'var(--good)' } : null, html: icons.check, onclick: async () => {
    await api(`/api/items/${it.id}/played`, { method: 'POST', body: { played: !played } });
    toast(played ? 'Marked unwatched' : 'Marked watched', 'ok');
    onDone();
  } });
}

function favButton(it) {
  let fav = !!it.userData?.favorite;
  const b = h('button', { class: 'btn lg icon', title: 'Favorite', html: fav ? icons.heartFill : icons.heart, style: fav ? { color: 'var(--bad)' } : null, onclick: async () => {
    fav = !fav;
    await api(`/api/items/${it.id}/favorite`, { method: 'POST', body: { favorite: fav } });
    b.innerHTML = fav ? icons.heartFill : icons.heart;
    b.style.color = fav ? 'var(--bad)' : '';
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
        try { await api(`/api/items/${it.id}/refresh`, { method: 'POST' }); toast('Metadata refreshed', 'ok'); route(); } catch (ex) { toast(ex.message, 'error'); }
      } });
      if (it.metaLocked) items.push({ label: 'Unmatch (auto-match again)', icon: 'refresh', onClick: async () => { await api(`/api/items/${it.id}/unmatch`, { method: 'POST' }); toast('Unmatched; re-matching in background'); } });
      const cached = d.files?.length && d.files.every((f) => f.cached);
      items.push('-');
      if (cached) {
        items.push({ label: 'Remove from SSD cache', icon: 'trash', onClick: async () => { await api(`/api/admin/cache/items/${it.id}`, { method: 'DELETE' }); toast('Removed from cache', 'ok'); route(); } });
      } else {
        items.push({ label: it.kind === 'show' ? 'Cache all episodes on SSD' : 'Cache on SSD', icon: 'devices', onClick: async () => {
          try { const r = await api(`/api/admin/cache/items/${it.id}`, { method: 'POST' }); toast(`Queued ${r.queued} file${r.queued === 1 ? '' : 's'} for the SSD cache`, 'ok'); } catch (ex) { toast(ex.message, 'error'); }
        } });
      }
      if (it.kind === 'show' || it.kind === 'episode') {
        items.push({ label: 'Re-detect intro', icon: 'refresh', onClick: async () => { try { await api(`/api/items/${it.id}/intro/reset`, { method: 'POST' }); toast('Intro detection queued', 'ok'); } catch (ex) { toast(ex.message, 'error'); } } });
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
    isEp ? h('span', null, `S${it.season} E${it.episode}${it.episodeEnd ? '–' + it.episodeEnd : ''}`) : null,
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
      d.prev ? h('a', { class: 'btn sm', href: `#/item/${d.prev.id}` }, h('span', { html: icons.chevL }), `S${d.prev.season}E${d.prev.episode}`) : null,
      d.next ? h('a', { class: 'btn sm', href: `#/item/${d.next.id}` }, `Next: S${d.next.season}E${d.next.episode} ${d.next.title}`, h('span', { html: icons.chevR })) : null));
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
    const lbl = `${next.userData?.position > 0 ? 'Resume' : 'Play'} S${next.season} E${next.episode}`;
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
      history.replaceState(null, '', `#/item/${it.id}?season=${s.id}`);
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
  const art = h('button', { class: 'art', type: 'button', dataset: { focusKey: `episode-play-${e.id}` }, 'aria-label': `${ud.position > 0 && !ud.played ? 'Resume' : 'Play'} episode ${e.episode}: ${e.title}`, onclick: () => play(e.id) }, lazyImg(img(e, 'thumb', 480), ''), h('span', { class: 'ep-play', 'aria-hidden': 'true', html: icons.play }));
  if (ud.position > 0 && e.duration) art.appendChild(h('div', { class: 'progress' }, h('i', { style: { width: `${(ud.position / e.duration) * 100}%` } })));
  const wbtn = h('button', { class: `watched-btn ${ud.played ? 'on' : ''}`, title: ud.played ? 'Mark unwatched' : 'Mark watched', html: icons.check, onclick: async (ev) => {
    ev.stopPropagation();
    ud.played = !ud.played;
    await api(`/api/items/${e.id}/played`, { method: 'POST', body: { played: ud.played } });
    wbtn.classList.toggle('on', ud.played);
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
  if (searchInput && searchInput.value !== q) searchInput.value = q;
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
