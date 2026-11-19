// Pages that are a plain grid of titles: Watchlist, Favorites, a collection, a person.

import { h, lazyImg, emptyState } from '../ui.js';
import { api, castImg } from '../api.js';
import { notFound } from '../app.js';
import { posterCard } from './cards.js';

export const WATCHLIST_HINT = 'Open a movie or show and select the bookmark, or use a poster’s ⋯ menu, to save it for later.';

export const FAVORITES_HINT = 'Open a movie or show and select the heart, or use a poster’s ⋯ menu, to add it to your favorites.';

// titlesPage: a heading block over a grid of posters (or an empty state).
/** @param {{ kicker?: string, title: string, count?: boolean, head?: HTMLElement, items: any[], sub?: (it: any) => string, empty: object }} page */
function titlesPage({ kicker, title, count, head, items, sub, empty }) {
  const n = items.length;
  const text = h('div', { style: { minWidth: 0 } },
    kicker ? h('div', { class: 'kicker' }, kicker) : null,
    h('h1', { class: 'page-title' }, title),
    count !== false ? h('span', { class: 'count' }, `${n} title${n === 1 ? '' : 's'}`) : null);
  return h('div', { class: 'page' },
    h('div', { class: 'browse-head' }, head || null, text),
    n ? h('div', { class: 'grid' }, items.map((it) => posterCard(it, { sub: sub?.(it) }))) : emptyState({ level: 'h2', ...empty }));
}

export async function watchlistView() {
  const r = await api('/api/watchlist');
  return titlesPage({ title: 'Watchlist', items: r.items, empty: { title: 'Your Watchlist is empty', text: WATCHLIST_HINT, actions: [h('a', { class: 'btn primary', href: '#/' }, 'Browse titles')] } });
}

export async function favoritesView() {
  const r = await api('/api/favorites');
  return titlesPage({ title: 'Favorites', items: r.items, empty: { title: 'No favorites yet', text: FAVORITES_HINT, actions: [h('a', { class: 'btn primary', href: '#/' }, 'Browse titles')] } });
}

export async function collectionView(ctx, id) {
  const r = await api(`/api/collections/${id}`);
  return titlesPage({ kicker: 'Collection', title: r.collection.name, items: r.items,
    empty: { title: 'Nothing here', text: 'None of this collection’s movies are in your library any more.' } });
}

export async function personView(ctx, name) {
  if (!name.trim()) return notFound();
  const r = await api(`/api/person?name=${encodeURIComponent(name)}`);
  const portrait = h('div', { class: 'ph', 'aria-hidden': 'true' }, r.name.slice(0, 1));
  if (r.image) portrait.replaceChildren(lazyImg(castImg({ id: r.image.itemId, updatedAt: r.image.updatedAt }, r.image.index, 240), '', () => { portrait.textContent = r.name.slice(0, 1); }));
  return titlesPage({ kicker: 'Cast & crew', title: r.name, items: r.items, head: h('div', { class: 'person-portrait' }, portrait),
    sub: (it) => [r.roles[it.id], it.year].filter(Boolean).join(' · '),
    empty: { title: 'No titles found', text: `Nothing in your library lists ${r.name}.` } });
}
