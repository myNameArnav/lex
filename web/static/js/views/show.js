// A show's page: seasons, episodes, and the show's actions (shuffle, resume).

import { h, clear, icons, fmtDuration, toast, spinner, lazyImg, popupMenu, confirmDialog, run, fmtEpisode, datedEpisode, fmtAirDate } from '../ui.js';
import { api, img } from '../api.js';
import { openPlayer, isPlayerOpen } from '../player-loader.js';
import { seasonMenuItems, seasonConfirm } from '../season.js';
import { replaceHash, play, refreshSoft } from '../app.js';
import { progressBar } from './cards.js';
import { trailerButton, extrasRow, hero, heroPoster, facts, watchedButton, favButton, watchlistButton, genreLinks, moreButton, castRow, retryWhileFetching } from './title.js';

// playShuffle plays a show's (or a season's) episodes in random order.
// The button stays busy while the order loads.
async function playShuffle(it, btn) {
  let eps = null;
  if (!(await run(btn, async () => { eps = await api(`/api/items/${it.id}/shuffle`); }))) return;
  if (!eps.length) { toast('Nothing to shuffle yet'); return; }
  openPlayer({ shuffle: eps, onClose: () => { if (!isPlayerOpen()) refreshSoft(); } });
}

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
    if (it.childCount > 1) btns.push(h('button', { class: 'btn lg', title: 'Play episodes in random order', dataset: { focusKey: `show-shuffle-${it.id}` }, onclick: (e) => playShuffle(it, e.currentTarget) }, h('span', { html: icons.shuffle }), 'Shuffle'));
  }
  btns.push(trailerButton(it, d.extras), watchedButton(it), favButton(it), watchlistButton(it), moreButton(it, d));
  // A placeholder title ("Episode 1") would only repeat the button.
  const upNext = !done && next?.title && !/^Episode \d+$/.test(next.title);
  return h('div', { class: 'hero-cta' }, h('div', { class: 'actions' }, btns), upNext ? h('p', { class: 'muted small' }, `Up next: ${next.title}`) : null);
}

function seasonTabLabel(tab, s) {
  const n = s.unplayedCount || 0;
  tab.setAttribute('aria-label', `${s.title}${n ? `, ${n} unwatched episode${n === 1 ? '' : 's'}` : ''}`);
  tab.replaceChildren(s.title, n ? h('span', { class: 'dim', 'aria-hidden': 'true', style: { marginLeft: '6px', fontWeight: 600 } }, n) : '');
}

export async function showView(ctx, d) {
  const it = d.item;
  const seasons = d.children || [];
  const poster = heroPoster(it, 'poster', false);
  const next = d.nextEpisode;
  let cta = heroActions(d);
  const n = seasons.filter((s) => s.season > 0).length;
  const info = [h('h1', null, it.title),
    facts(it, [n ? h('span', null, `${n} season${n === 1 ? '' : 's'}`) : null, it.studios?.length ? h('span', null, it.studios[0]) : null])];
  if (it.genres?.length) info.push(genreLinks(it));
  info.push(cta);
  info.push(h('p', { class: 'overview' }, it.overview || (it.metaStatus === 0 ? 'Fetching details…' : 'No description available.')));
  retryWhileFetching(ctx, it);

  const listId = `episodes-${it.id}`;
  const tabs = h('div', { class: 'tabs', role: 'group', 'aria-label': 'Seasons' });
  const list = h('div', { class: 'episodes', id: listId, role: 'region', 'aria-label': 'Episodes' });
  const tabFor = (s) => [...tabs.children].find((b) => +b.dataset.id === s.id);
  // An episode toggled in the list: adjust its season's count now, then
  // refetch the show for the play button, Up next and every season count.
  // s is the episode's season (the user may have switched tabs since).
  let refetch = 0, seq = 0;
  const episodeChanged = (s, played) => {
    s.unplayedCount = Math.max(0, (s.unplayedCount || 0) + (played ? -1 : 1));
    seasonTabLabel(tabFor(s), s);
    refetchShow(400);
  };
  // refetchShow updates the play button, Up next and every season count
  // after a change in the list (debounced: toggles come in bursts).
  const refetchShow = (delay) => {
    clearTimeout(refetch);
    const mine = ++seq;
    refetch = setTimeout(async () => {
      let nd;
      try { nd = await api(`/api/items/${it.id}`); } catch { return; }
      // A later toggle's refetch has the newer state.
      if (!ctx.isCurrent() || mine !== seq) return;
      for (const s of nd.children || []) {
        const old = seasons.find((x) => x.id === s.id), tab = tabFor(s);
        if (old && tab) { old.unplayedCount = s.unplayedCount; seasonTabLabel(tab, old); }
      }
      const key = cta.contains(document.activeElement) ? document.activeElement.dataset.focusKey : null;
      const n = heroActions(nd);
      cta.replaceWith(n);
      cta = n;
      if (key) n.querySelector(`[data-focus-key="${key}"]`)?.focus({ preventScroll: true });
    }, delay);
  };
  const wanted = +ctx.query.get('season');
  let active = seasons.find((s) => s.id === wanted) || (next && seasons.find((s) => s.id === next.parentId)) || seasons.find((s) => s.season > 0) || seasons[0];
  const renderEpisodes = (s, eps) => clear(list).append(...(s.overview ? [h('p', { class: 'muted', style: { maxWidth: '760px', margin: '0 0 8px' } }, s.overview)] : []), ...eps.map((e) => episodeRow(e, (played) => episodeChanged(s, played))));
  // The selected season's ⋯ menu: mark it watched/unwatched, shuffle it.
  // Always shown at a fixed size beside the tabs, so switching seasons or
  // acting on one never moves the layout; the menu is built on open.
  const markSeason = async (s, played, btn) => {
    if (!(await confirmDialog(seasonConfirm(s, it.title, played)))) return;
    if (!(await run(btn, () => api(`/api/items/${s.id}/played`, { method: 'POST', body: { played } }), played ? 'Marked watched' : 'Marked unwatched'))) return;
    s.unplayedCount = played ? 0 : s.childCount || 0;
    seasonTabLabel(tabFor(s), s);
    refetchShow(0);
    // Redraw the checks and progress bars in place (no spinner, no jump).
    if (active !== s) return;
    let eps;
    try { eps = await api(`/api/items/${s.id}/children`); } catch { return; }
    if (active !== s || !ctx.isCurrent()) return;
    const key = list.contains(document.activeElement) ? document.activeElement.dataset.focusKey : null;
    renderEpisodes(s, eps);
    if (key) list.querySelector(`[data-focus-key="${key}"]`)?.focus({ preventScroll: true });
  };
  const more = seasons.length ? h('button', { class: 'btn icon ghost sm season-more', type: 'button', title: 'Season actions', 'aria-label': 'Season actions', 'aria-haspopup': 'menu', dataset: { focusKey: `season-more-${it.id}` }, html: icons.more, onclick: (e) => {
    const btn = e.currentTarget, s = active;
    if (!s) return;
    popupMenu(btn, seasonMenuItems(s, { mark: (played) => markSeason(s, played, btn), shuffle: () => playShuffle(s, btn) }));
  } }) : null;
  const showSeason = async (s) => {
    active = s;
    more?.setAttribute('aria-label', `Actions for ${s.title}`);
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
      renderEpisodes(s, eps);
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
    h('div', { class: 'page', style: { paddingTop: 0 } }, h('h2', { class: 'sr-only' }, 'Episodes'), h('div', { class: 'season-bar' }, tabs, more), list),
    extrasRow(it, d.extras), castRow(it));
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
  // Unplaced daily-show episodes go by air date, which the line below the title shows.
  const dated = datedEpisode(e);
  const epName = dated ? fmtAirDate(e.premiere) : `episode ${e.episode}`;
  const art = h('button', { class: 'art', type: 'button', dataset: { focusKey: `episode-play-${e.id}` }, 'aria-label': `${ud.position > 0 ? 'Resume' : 'Play'} ${epName}: ${e.title}`, onclick: () => play(e.id) }, lazyImg(img(e, 'thumb', 480), ''), h('span', { class: 'ep-play', 'aria-hidden': 'true', html: icons.play }));
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
    wbtn.setAttribute('aria-label', `${action}: ${epName}, ${e.title}`);
    if (ud.played) art.appendChild(badge); else badge.remove();
  };
  show();
  // The link is named by its heading; date, progress and synopsis are its description.
  const id = `ep-${e.id}`;
  return h('div', { class: 'episode' },
    art,
    h('a', { class: 'episode-info', href: `#/item/${e.id}`, 'aria-labelledby': `${id}-t`, 'aria-describedby': e.overview ? `${id}-m ${id}-o` : `${id}-m` },
      h('h3', { id: `${id}-t` }, dated ? null : h('span', { class: 'num' }, `${e.episode}${e.episodeEnd ? '–' + e.episodeEnd : ''}.`), dated ? null : ' ', e.title),
      h('div', { class: 'dim small', id: `${id}-m` }, [e.premiere ? fmtAirDate(e.premiere) : '', e.duration ? fmtDuration(e.duration) : '', ud.position > 0 ? `${fmtDuration(e.duration - ud.position)} left` : ''].filter(Boolean).join(' · ')),
      e.overview ? h('p', { id: `${id}-o` }, e.overview) : null),
    h('div', { class: 'side' }, wbtn));
}
