// Search results: titles, people and episodes.

import { h, emptyState } from '../ui.js';
import { api } from '../api.js';
import { setSearchBox } from '../app.js';
import { posterCard, landCard } from './cards.js';

export async function searchView(ctx, q) {
  // Leave the box alone while it holds this query (e.g. with a trailing space being typed).
  setSearchBox(q);
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
  if (r.shows.length) group('Shows', r.shows, h('div', { class: 'grid' }, r.shows.map((it) => posterCard(it))));
  if (r.movies.length) group('Movies', r.movies, h('div', { class: 'grid' }, r.movies.map((it) => posterCard(it))));
  if (r.episodes.length) group('Episodes', r.episodes, h('div', { class: 'grid land' }, r.episodes.map((e) => landCard(e, { playOnClick: false }))));
  return page;
}
