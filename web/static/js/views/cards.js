// Poster and landscape cards, their ⋯ menus, and the scrolling shelves
// they sit in. Shared by every page that lists titles.

import { h, icons, fmtDuration, lazyImg, popupMenu, run, fmtEpisode, motionOK } from '../ui.js';
import { api, img } from '../api.js';
import { focusAfterRoute, play, refreshSoft } from '../app.js';

/** @typedef {import('../../../types/api.gen').Item} Item */
/** @typedef {import('../../../types/api.gen').UserData} UserData */

// List responses (/api/items, /api/home, /api/search) carry only what cards
// use: ids, kind, title, year, season/episode, counts, userData, duration,
// showTitle and updatedAt (store.cardCols). /api/items/:id/children adds
// overview and premiere for episode rows and season tabs. Anything else
// (overview, cast, genres, studios, ratings…) comes from /api/items/:id.
/** @param {Item} it */
function subtitleFor(it) {
  if (it.kind === 'show') return it.childCount ? `${it.childCount} episode${it.childCount === 1 ? '' : 's'}` : (it.year || '');
  if (it.kind === 'episode') return fmtEpisode(it);
  if (it.kind === 'season') return it.childCount ? `${it.childCount} episodes` : '';
  return it.year || '';
}

/** @param {Item} it */
export function placeholder(it) {
  return h('div', { class: 'ph', 'aria-hidden': 'true' }, it.title);
}

export const progressBar = (pos, dur) => h('div', { class: 'progress', 'aria-hidden': 'true' }, h('i', { style: { width: `${Math.min(100, (pos / dur) * 100)}%` } }));

// posterCard: sub replaces the line under the title (e.g. a person's role).
/** @param {any} it @param {{ sub?: string }} [options] */
export function posterCard(it, { sub } = {}) {
  /** @type {Partial<UserData>} */
  const ud = it.userData || {};
  const art = h('div', { class: 'art' }, placeholder(it), lazyImg(img(it, 'poster', 320), ''));
  const parent = it.kind === 'show' || it.kind === 'season';
  const watched = parent ? !it.unplayedCount && it.childCount > 0 : !!ud.played;
  if (parent && it.unplayedCount > 0) art.appendChild(h('div', { class: 'badge', 'aria-hidden': 'true', title: `${it.unplayedCount} unwatched episode${it.unplayedCount === 1 ? '' : 's'}` }, it.unplayedCount));
  else if (watched) art.appendChild(h('div', { class: 'badge check', 'aria-hidden': 'true', title: 'Watched', html: icons.check }));
  const left = ud.position > 0 && it.duration ? `${fmtDuration(it.duration - ud.position)} left` : '';
  if (left) art.appendChild(progressBar(ud.position, it.duration));
  const line = sub ?? subtitleFor(it);
  const label = [it.title, line, parent && it.unplayedCount > 0 ? `${it.unplayedCount} unwatched` : watched ? 'watched' : '', ud.watchlist ? 'on your watchlist' : '', ud.favorite ? 'a favorite' : '', left].filter(Boolean).join(', ');
  const link = h('a', { class: 'card-link', href: `#/item/${it.id}`, 'aria-label': label, dataset: { focusKey: `card-${it.id}` } },
    art, h('div', { class: 'meta' }, h('div', { class: 't', title: it.title }, it.title), h('div', { class: 's' }, line)));
  const more = it.kind === 'movie' || it.kind === 'show' ? posterMenuButton(it) : null;
  return h('div', { class: `card poster${more ? ' has-more' : ''}` }, link, more);
}

// setWatchlist adds a movie or show to the watchlist or takes it off, and
// says so (the toast is the undo hint). Resolves to whether it worked.
/** @param {Item} it */
async function setWatchlist(it, on) {
  const ok = await run(null, () => api(`/api/items/${it.id}/watchlist`, { method: 'POST', body: { watchlist: on } }), on ? 'Added to Watchlist' : 'Removed from Watchlist');
  if (ok) (it.userData ||= /** @type {UserData} */ ({})).watchlist = on;
  return !!ok;
}

// setFavorite marks a title as a favorite or not, like setWatchlist.
/** @param {Item} it */
async function setFavorite(it, on) {
  const ok = await run(null, () => api(`/api/items/${it.id}/favorite`, { method: 'POST', body: { favorite: on } }), on ? 'Added to Favorites' : 'Removed from Favorites');
  if (ok) (it.userData ||= /** @type {UserData} */ ({})).favorite = on;
  return !!ok;
}

// The ⋯ on a movie or show poster: Watchlist, Favorites and watched
// toggles. The view re-renders in place afterwards (Watchlist and Favorites
// rows and filters change).
/** @param {Item} it */
function posterMenuButton(it) {
  /** @type {Partial<UserData>} */
  const ud = it.userData || {};
  const items = [ud.watchlist
    ? { label: 'Remove from Watchlist', icon: 'bookmarkFill', onClick: async () => { if (await setWatchlist(it, false)) refreshSoft(); } }
    : { label: 'Add to Watchlist', icon: 'bookmark', onClick: async () => { if (await setWatchlist(it, true)) refreshSoft(); } },
  ud.favorite
    ? { label: 'Remove from Favorites', icon: 'heartFill', onClick: async () => { if (await setFavorite(it, false)) refreshSoft(); } }
    : { label: 'Add to Favorites', icon: 'heart', onClick: async () => { if (await setFavorite(it, true)) refreshSoft(); } }];
  if (it.kind === 'movie') {
    items.push(ud.played
      ? { label: 'Mark unwatched', icon: 'check', onClick: async () => { if (await run(null, () => api(`/api/items/${it.id}/played`, { method: 'POST', body: { played: false } }), 'Marked unwatched')) refreshSoft(); } }
      : { label: 'Mark watched', icon: 'check', onClick: async () => { if (await run(null, () => api(`/api/items/${it.id}/played`, { method: 'POST', body: { played: true } }), 'Marked watched')) refreshSoft(); } });
  }
  return h('button', { class: 'btn icon ghost sm card-more', type: 'button', title: 'More', 'aria-label': `More for ${it.title}`, dataset: { focusKey: `card-more-${it.id}` }, html: icons.more, onclick: (e) => popupMenu(e.currentTarget, items) });
}

// landCard: Continue Watching / Next Up (the artwork plays) and episode
// search results (the card opens the episode).
/** @param {Item} it @param {{ playOnClick?: boolean }} [options] */
export function landCard(it, { playOnClick = true } = {}) {
  /** @type {Partial<UserData>} */
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

// The ⋯ on a Continue Watching / Next Up card. Removing a title from
// Continue Watching clears its resume point and leaves its watched state be.
/** @param {Item} it */
function landMenuButton(it, title, card) {
  /** @type {Partial<UserData>} */
  const ud = it.userData || {};
  const act = async (req, msg) => {
    // The card goes away: keep focus in the row on its neighbour.
    const c = card(), near = c.nextElementSibling || c.previousElementSibling;
    if (!(await run(null, req, msg))) return;
    const key = near?.querySelector('[data-focus-key]')?.dataset.focusKey;
    if (key && c.contains(document.activeElement)) focusAfterRoute(key);
    refreshSoft();
  };
  const items = [{ label: 'Mark watched', icon: 'check', onClick: () => act(() => api(`/api/items/${it.id}/played`, { method: 'POST', body: { played: true } }), 'Marked watched') }];
  if (ud.position > 0) items.push({ label: 'Remove from Continue Watching', icon: 'close', onClick: () => act(() => api(`/api/items/${it.id}/progress`, { method: 'DELETE' }), 'Removed from Continue Watching') });
  return h('button', { class: 'btn icon ghost sm card-more', type: 'button', title: 'More', 'aria-label': `More for ${title}`, dataset: { focusKey: `land-more-${it.id}` }, html: icons.more, onclick: (e) => {
    e.stopPropagation();
    popupMenu(e.currentTarget, items);
  } });
}

// scroller wraps a horizontal track with scroll arrows for mouse users. The
// arrows aren't tab stops (focusing a card scrolls it into view) and show
// only while there is more to scroll that way; touch screens swipe instead.
export function scroller(track) {
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

/** @param {string} title @param {Item[]} items */
export function shelf(title, items, kind, moreHref) {
  const track = h('div', { class: 'shelf-track', tabindex: '-1' }, items.map((it) => kind === 'landscape' ? landCard(it) : posterCard(it)));
  return h('section', { class: 'shelf' },
    h('div', { class: 'shelf-head' }, h('h2', { class: 'section-title' }, title), moreHref ? h('a', { class: 'more', href: moreHref, 'aria-label': `See all: ${title}` }, 'See all') : null),
    scroller(track));
}
