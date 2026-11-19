// A library's page: the title grid with its sorting, filters, letter rail and paging.

import { h, clear, icons, RAIL_LETTERS, letterOffsets, toast, run, emptyState } from '../ui.js';
import { api } from '../api.js';
import { state, loadLibraries, focusAfterRoute, replaceHash } from '../app.js';
import { posterCard } from './cards.js';
import { WATCHLIST_HINT, FAVORITES_HINT } from './lists.js';

export async function libraryView(ctx, id) {
  const lib = state.libraries.find((l) => l.id === id) || (await loadLibraries(), state.libraries.find((l) => l.id === id));
  if (!lib) return emptyState({ title: 'Library not found', text: 'It may have been removed.', actions: [h('a', { class: 'btn primary', href: '#/' }, 'Go home')] });
  const q = ctx.query;
  const mixed = lib.kind === 'mixed';
  const opts = { sort: q.get('sort') || 'title', desc: q.get('desc') === '1', filter: q.get('filter') || '', genre: q.get('genre') || '',
    kind: mixed && ['movie', 'show'].includes(q.get('kind')) ? q.get('kind') : '', letter: q.get('letter') || '', seed: 0 };
  // A seed the server can't read as an integer is replaced below.
  if (Number.isSafeInteger(+q.get('seed'))) opts.seed = +q.get('seed');
  // Movies have no episodes and shows sort by their newest episode, so each
  // offers the one "newest" order that means something for it (a Mixed
  // library filtered to one kind too).
  const onlyMovies = lib.kind === 'movies' || opts.kind === 'movie', onlyShows = lib.kind === 'shows' || opts.kind === 'show';
  const sorts = [['title', 'Title'], ['year', 'Year'],
    ...(!onlyShows ? [['added', 'Date added']] : []), ...(!onlyMovies ? [['latest', 'Latest episode']] : []),
    ['rating', 'Rating'], ['played', 'Recently watched'], ['random', 'Random']];
  let fixSort = false;
  if (onlyMovies && opts.sort === 'latest') { opts.sort = 'added'; fixSort = true; }
  if (onlyShows && opts.sort === 'added') { opts.sort = 'latest'; fixSort = true; }
  // The A–Z rail is for title order; a letter elsewhere is dropped.
  const rail = opts.sort === 'title';
  if (opts.letter && (!rail || !RAIL_LETTERS.includes(opts.letter))) { opts.letter = ''; fixSort = true; }
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
    if (n.kind) p.set('kind', n.kind);
    if (n.sort === 'random' && n.seed) p.set('seed', n.seed);
    if (n.sort === 'title' && n.letter) p.set('letter', n.letter);
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
  // The filters the grid, Random and the letter counts all go by.
  const filterParams = (p) => {
    if (opts.filter) p.set('filter', opts.filter);
    if (opts.genre) p.set('genre', opts.genre);
    if (opts.kind) p.set('kind', opts.kind);
    return p;
  };
  // Letter counts come with the view, so the rail is complete when it
  // first shows; without them it is left out rather than shown wrong.
  const [genres, counts] = await Promise.all([
    api(`/api/genres?library=${id}`).catch(() => []),
    rail ? api(`/api/letters?${filterParams(new URLSearchParams({ library: id }))}`).catch(() => null) : null,
  ]);
  const offsets = counts && letterOffsets(counts, opts.desc);
  // Where the grid starts: the chosen letter's first title, or the next one.
  // With none at or after it (the filters changed), from the top.
  let start = (opts.letter && offsets?.[opts.letter]) || 0;
  if (start && start >= Object.values(counts).reduce((a, b) => a + b, 0)) {
    start = 0;
    opts.letter = '';
    if (ctx.isCurrent()) replaceHash(hashFor(opts));
  }
  const sel = (label, key, value, options, on) => h('select', { class: key, 'aria-label': label, dataset: { focusKey: key }, onchange: (e) => on(e.target.value) }, options.map(([v, l]) => h('option', { value: v, selected: v === value }, l)));
  const count = h('span', { class: 'count' });
  const grid = h('div', { class: 'grid' });
  const sentinel = h('div', { style: { height: '40px' } });
  const random = h('button', { class: 'btn sm', title: 'Open a random title', disabled: true, onclick: async (e) => {
    await run(e.currentTarget, async () => {
      const p = filterParams(new URLSearchParams({ library: id, sort: 'random', limit: '1' }));
      const r = await api(`/api/items?${p}`);
      if (r.items[0]) location.hash = `#/item/${r.items[0].id}`;
      else toast('No titles match these filters');
    });
    // run() re-enables the button.
    random.disabled = total === 0;
  } }, h('span', { html: icons.shuffle }), 'Random');
  // Mixed libraries: All / Movies / Shows, as pressed buttons so all three
  // are always in view.
  const kindSeg = mixed ? h('div', { class: 'seg', role: 'group', 'aria-label': 'Movies or shows' },
    [['', 'All'], ['movie', 'Movies'], ['show', 'Shows']].map(([v, l]) => h('button', { type: 'button', class: 'seg-btn', 'aria-pressed': String(opts.kind === v),
      dataset: { focusKey: `kind-${v || 'all'}` }, onclick: () => { if (opts.kind !== v) setQ({ kind: v }, `kind-${v || 'all'}`); } }, l))) : null;
  // The A–Z rail: links, so each letter is an address Back returns to.
  // Letters with no titles under the current filters are dimmed.
  const azRail = offsets ? h('nav', { class: 'az-rail', 'aria-label': 'Jump to letter' },
    RAIL_LETTERS.map((L) => {
      const label = L === '#' ? '#' : L.toUpperCase();
      if (!counts[L]) return h('span', { class: 'az off', 'aria-hidden': 'true' }, label);
      return h('a', { class: 'az', href: hashFor({ ...opts, letter: L }), 'aria-label': L === '#' ? 'Numbers and symbols' : label,
        'aria-current': opts.letter === L ? 'true' : null, dataset: { focusKey: `az-${L}` },
        onclick: (e) => {
          // The letter shown already: back to its first title.
          if (opts.letter === L) { e.preventDefault(); window.scrollTo(0, 0); } else focusAfterRoute(`az-${L}`);
        } }, label);
    })) : null;
  const page = h('div', { class: 'page' },
    h('div', { class: 'row' }, h('h1', { class: 'page-title' }, lib.name), count),
    h('div', { class: 'toolbar' },
      sel('Sort titles', 'sort-select', opts.sort, sorts, (v) => setQ({ sort: v, desc: ['added', 'latest', 'rating', 'played', 'year'].includes(v), seed: v === 'random' ? newSeed() : 0 }, 'sort-select')),
      dirLabel ? h('button', { class: 'btn sm', title: 'Reverse order', 'aria-label': `Sort direction: ${dirLabel[+opts.desc]}`, dataset: { focusKey: 'sort-dir' }, onclick: () => setQ({ desc: !opts.desc }, 'sort-dir') }, dirLabel[+opts.desc]) : null,
      sel('Filter titles', 'filter-select', opts.filter, [['', 'All'], ['unplayed', 'Unwatched'], ['played', 'Watched'], ['inprogress', 'In progress'], ['favorite', 'Favorites'], ['watchlist', 'Watchlist']], (v) => setQ({ filter: v }, 'filter-select')),
      genres.length ? sel('Filter by genre', 'genre-select', opts.genre, [['', 'All genres'], ...genres.map((g) => [g, g])], (v) => setQ({ genre: v }, 'genre-select')) : null,
      kindSeg,
      h('div', { class: 'spacer' }),
      random),
    azRail, grid, sentinel);
  let offset = start, total = Infinity, loading = false;
  const PAGE = 120;
  const load = async () => {
    if (loading || offset >= total || !ctx.isCurrent()) return;
    loading = true;
    const p = filterParams(new URLSearchParams({ library: id, sort: opts.sort, limit: String(PAGE), offset: String(offset) }));
    if (opts.desc) p.set('desc', '1');
    if (opts.sort === 'random') p.set('seed', opts.seed);
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
        const favs = opts.filter === 'favorite' && !opts.genre && !opts.kind, wl = opts.filter === 'watchlist' && !opts.genre && !opts.kind, filtered = opts.filter || opts.genre || opts.kind;
        azRail?.remove();
        // The list loaded at startup may predate the first scan finishing.
        let scanned = lib.lastScan;
        if (!filtered && !scanned) { await loadLibraries(); scanned = state.libraries.find((l) => l.id === id)?.lastScan; }
        grid.replaceWith(emptyState({ level: 'h2',
          title: favs ? 'No favorites yet' : wl ? 'Your Watchlist is empty' : filtered ? 'Nothing here' : scanned ? 'No titles found' : 'Still scanning',
          text: favs ? FAVORITES_HINT : wl ? WATCHLIST_HINT : filtered ? 'No titles match these filters.' : scanned ? 'No titles found in this library.' : 'This library is still being scanned.',
          actions: filtered ? [h('button', { class: 'btn', onclick: () => setQ({ filter: '', genre: '', kind: '' }, 'filter-select') }, 'Clear filters')] : [] }));
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
    while (ctx.restore && offset - start < ctx.restore.loaded && offset < total && ctx.isCurrent()) await load();
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
  // A horizontal rail (phones) may have the chosen letter out of view:
  // centre it once the view is in.
  const cur = azRail?.querySelector('[aria-current]');
  if (cur) requestAnimationFrame(() => {
    if (azRail.scrollWidth <= azRail.clientWidth) return;
    const r = azRail.getBoundingClientRect(), c = cur.getBoundingClientRect();
    azRail.scrollLeft += c.left + c.width / 2 - (r.left + r.width / 2);
  });
  return page;
}
