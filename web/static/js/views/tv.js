// The Live TV guide. On wide screens a grid: channels down the side, time
// across, a line at now; the program on now tunes in, a later one opens its
// details. On phones a list of each channel's program now and what's next.

import { h, icons, lazyImg, fmtEpisode, fmtClock, fmtProgram, emptyState, modal } from '../ui.js';
import { api, img } from '../api.js';
import { openPlayer, isPlayerOpen } from '../player-loader.js';
import { state, renderNav, refreshSoft, refreshLater } from '../app.js';
import { placeholder, progressBar } from './cards.js';
import { matcher } from '../channel-filter.js';

/** @typedef {import('../../../types/api.gen').Item} Item */
/** @typedef {{ id: string, number: number, name: string }} ChannelRef */

const MIN = 60000;
const SLOT = 30 * MIN; // the grid's columns
const HOURS = 12; // how far the grid looks ahead
const PX_PER_MIN = 7; // 30 minutes = 210px
const CH_WIDTH = 168; // the channel column

// playChannel tunes into a Live TV channel; channels are what Next and
// Page Up/Down switch through.
function playChannel(id, channels) {
  openPlayer({ channel: id, channels, onClose: () => { if (!isPlayerOpen()) refreshSoft(); } });
}

// The guide's two lines for a program: the show or movie, then the episode.
/** @param {Item} it */
function programHead(it) {
  return it.kind === 'episode' ? it.showTitle || it.title : it.title;
}

/** @param {Item} it */
function programSub(it) {
  if (it.kind === 'live') return it.originalTitle || 'Live';
  return it.kind === 'episode' ? `${fmtEpisode(it)} — ${it.title}` : String(it.year || '');
}

// art is a program's picture: a live channel's logo, else the title's.
const art = (it, logo, w) => (it.kind === 'live' ? (logo ? lazyImg(logo, '') : null) : lazyImg(img(it, 'thumb', w), ''));

const titleHref = (/** @type {Item} */ it) => `#/item/${it.kind === 'episode' && it.showId ? it.showId : it.id}`;

const off = () => emptyState({ title: 'Live TV is off', text: state.me.isAdmin ? 'Turn it on in Settings › Live TV.' : 'Ask your server admin to turn it on.',
  actions: [state.me.isAdmin ? h('a', { class: 'btn primary', href: '#/settings/livetv' }, 'Open settings') : h('a', { class: 'btn primary', href: '#/' }, 'Go home')] });

// guide loads /api/tv (path with its query); null when Live TV was turned
// off since the page loaded (and the menu link is dropped).
async function guide(path) {
  try { return await api(path); } catch (e) {
    if (e.status !== 404) throw e;
    state.caps = { ...state.caps, liveTv: false };
    renderNav();
    return null;
  }
}

const nothing = () => emptyState({ level: 'h2', title: 'Nothing to air yet', text: 'Live TV plays movies and episodes once a library scan has read their running times.' });

const intro = () => [h('h1', { class: 'page-title' }, 'Live TV'),
  h('p', { class: 'muted', style: { margin: 0 } }, 'Channels that play your library at random, like TV, and live channels from the internet.')];

// The search: kept while the guide refreshes, and for coming back to it.
let query = '';

// searchBox filters the channels by name or number (see channel-filter.js);
// onChange redraws. Next channel then steps through the matches only.
function searchBox(total, onChange) {
  const input = h('input', { type: 'search', class: 'tv-search', value: query, placeholder: 'Search channels', 'aria-label': 'Search channels by name or number',
    title: 'Words in the channel name, or its number. Put a minus in front of a word to leave those out: -HD', autocomplete: 'off', spellcheck: 'false',
    dataset: { focusKey: 'tv-search' },
    // A refresh focuses it again: keep typing at the end.
    onfocus: (e) => { const n = e.target.value.length; e.target.setSelectionRange(n, n); },
    oninput: (e) => { query = e.target.value; onChange(); },
    onkeydown: (e) => { if (e.key === 'Escape' && query) { e.preventDefault(); e.stopPropagation(); input.value = query = ''; onChange(); } } });
  return total > 8 ? h('div', { class: 'tv-search-row' }, input) : null;
}

const noMatch = () => h('p', { class: 'muted tv-no-match' }, `No channels match “${query.trim()}”.`);

export async function tvView(ctx) {
  if (!state.caps.liveTv) return off();
  // The grid needs room; a phone gets the list.
  return matchMedia('(min-width: 760px)').matches ? gridView(ctx) : listView(ctx);
}

// listView: each channel's program now (the art tunes in) and what's next.
// It refreshes every minute; the time left is relative to the server clock.
async function listView(ctx) {
  const data = await guide('/api/tv');
  if (!data) return off();
  const skew = Date.now() - data.serverTime;
  const wrap = h('div', { class: 'page' }, ...intro());
  if (!data.channels.length) { wrap.appendChild(nothing()); return wrap; }
  const list = h('div');
  const draw = () => {
    const m = matcher(query);
    const shown = data.channels.filter(m);
    const channels = shown.map(({ id, number, name }) => ({ id, number, name }));
    list.replaceChildren(shown.length ? guideList(shown, channels, skew) : noMatch());
  };
  wrap.append(searchBox(data.channels.length, draw) || '', list);
  draw();
  refreshLater(ctx, 60000);
  return wrap;
}

function guideList(shown, channels, skew) {
  const now = Date.now() - skew;
  return h('ol', { class: 'tv-guide' }, shown.map((ch) => {
    const [cur, ...later] = ch.programs;
    const it = cur.item;
    const left = Math.max(1, Math.round((cur.end - now) / MIN));
    const label = `Channel ${ch.number}, ${ch.name}: ${programHead(it)}${programSub(it) ? `, ${programSub(it)}` : ''}, ${left} min left`;
    return h('li', { class: 'tv-channel' },
      h('div', { class: 'tv-num', 'aria-hidden': 'true' }, h('b', null, ch.number), h('span', null, ch.name)),
      h('button', { type: 'button', class: 'card tv-now', 'aria-label': `Tune in to ${label}`, dataset: { focusKey: `tv-${ch.id}` }, onclick: () => playChannel(ch.id, channels) },
        h('div', { class: `art${ch.live ? ' logo' : ''}` }, placeholder(it), art(it, ch.logo, 480),
          h('div', { class: 'play-hover', 'aria-hidden': 'true' }, h('span', { html: icons.play })),
          progressBar(now - cur.start, cur.end - cur.start)),
        h('div', { class: 'meta' },
          h('div', { class: 'small tv-on' }, h('b', null, 'On now'), ` · ${left} min left`),
          h('div', { class: 't' }, programHead(it)), h('div', { class: 's' }, programSub(it)))),
      h('ul', { class: 'tv-later', 'aria-label': `Later on ${ch.name}` }, later.map((p) => h('li', null,
        h('span', { class: 'tv-time' }, fmtClock(p.start)), h('span', { class: 'tv-title' }, programHead(p.item), p.item.kind === 'episode' ? h('span', { class: 'muted' }, ` · ${fmtEpisode(p.item)}`) : null)))));
  }));
}

// programDialog: a program's details, from the grid. One on now tunes in;
// any can be played on its own from the start.
function programDialog(p, ch, channels, now) {
  const it = p.item;
  const on = p.start <= now && now < p.end;
  const when = `${fmtClock(p.start)} – ${fmtClock(p.end)}`;
  const m = modal({
    title: programHead(it),
    body: h('div', { class: 'tv-details' },
      h('div', { class: `art${ch.live ? ' logo' : ''}` }, placeholder(it), art(it, ch.logo, 640)),
      h('p', { class: 'muted', style: { margin: 0 } }, `${ch.number} · ${ch.name} · ${on ? 'on now' : when}`),
      programSub(it) ? h('p', { style: { margin: 0, fontWeight: 600 } }, programSub(it)) : null,
      it.overview ? h('p', { class: 'tv-overview' }, it.overview) : null),
    actions: it.kind === 'live' ? [
      h('div', { class: 'spacer' }),
      on ? h('button', { class: 'btn primary', type: 'button', onclick: () => { m.close(); playChannel(ch.id, channels); } }, h('span', { html: icons.play }), 'Tune in')
        : h('button', { class: 'btn', type: 'button', onclick: () => m.close() }, 'Close'),
    ] : [
      h('a', { class: 'btn', href: titleHref(it), onclick: () => m.close() }, it.kind === 'episode' ? 'Show details' : 'Movie details'),
      h('div', { class: 'spacer' }),
      h('button', { class: `btn${on ? '' : ' primary'}`, type: 'button', onclick: () => { m.close(); openPlayer({ itemId: it.id, start: 0 }); } }, 'Play from the start'),
      on ? h('button', { class: 'btn primary', type: 'button', onclick: () => { m.close(); playChannel(ch.id, channels); } }, h('span', { html: icons.play }), 'Tune in') : null,
    ],
  });
}

// gridView: the grid, from the start of this half hour to HOURS on. Every
// half minute the now line and what's on move along; at the next half hour
// the grid reloads, keeping where it's scrolled to in time.
async function gridView(ctx) {
  const wrap = h('div', { class: 'page tvg-page' }, ...intro());
  const holder = h('div');
  wrap.appendChild(holder);
  let skew = 0, from = 0, data = null, channels = /** @type {ChannelRef[]} */ ([]);
  const x = (t) => ((t - from) / MIN) * PX_PER_MIN;

  const load = async () => {
    const start = Math.floor((Date.now() - skew) / SLOT) * SLOT;
    const d = await guide(`/api/tv?from=${start}&to=${start + HOURS * 3600000}`);
    if (!d) return false;
    data = d; from = d.from; skew = Date.now() - d.serverTime;
    return true;
  };
  if (!await load()) return off();
  if (!data.channels.length) { wrap.appendChild(nothing()); return wrap; }
  // Searching keeps the time in view.
  const search = searchBox(data.channels.length, () => {
    const at = scroller?.scrollLeft;
    draw();
    if (scroller) scroller.scrollLeft = at ?? Math.max(0, x(Date.now() - skew) - 3 * PX_PER_MIN * 10);
  });
  if (search) holder.before(search);

  /** @type {HTMLElement} */
  let scroller, nowLine;
  const draw = () => {
    const now = Date.now() - skew;
    const width = x(data.to);
    const shown = data.channels.filter(matcher(query));
    channels = shown.map(({ id, number, name }) => ({ id, number, name }));
    if (!shown.length) { scroller = null; holder.replaceChildren(noMatch()); return; }
    const head = h('div', { class: 'tvg-head', role: 'row' },
      h('div', { class: 'tvg-corner', role: 'columnheader' }, new Date(from).toLocaleDateString([], { weekday: 'long' })),
      h('div', { class: 'tvg-times', style: { width: `${width}px` } },
        Array.from({ length: (data.to - from) / SLOT }, (_, i) => h('span', { class: 'tvg-tick', role: 'columnheader', style: { left: `${x(from + i * SLOT)}px` } }, fmtClock(from + i * SLOT)))));
    const rows = shown.map((ch) => h('div', { class: 'tvg-row', role: 'row' },
      h('button', { type: 'button', class: 'tvg-ch', role: 'rowheader', tabindex: '-1', 'aria-label': `Tune in to channel ${ch.number}, ${ch.name}`, dataset: { focusKey: `tvg-ch-${ch.id}` }, onclick: () => playChannel(ch.id, channels) },
        h('b', null, ch.number), ch.logo ? lazyImg(ch.logo, '') : null, h('span', null, ch.name)),
      h('div', { class: 'tvg-track', style: { width: `${width}px` } }, ch.programs.map((p) => {
        const l = Math.max(0, x(p.start)), r = Math.min(width, x(p.end));
        const it = p.item;
        const on = p.start <= now && now < p.end;
        // A live channel without a guide: one block, always on.
        const whole = it.kind === 'live' && p.start <= from && p.end >= data.to;
        const label = whole ? `${ch.name}, live: tune in` : `${fmtProgram(it)}, ${fmtClock(p.start)} to ${fmtClock(p.end)} on ${ch.name}${on ? ', on now: tune in' : ''}`;
        return h('button', { type: 'button', class: `tvg-prog${on ? ' now' : p.end <= now ? ' past' : ''}`, tabindex: '-1', 'aria-label': label, title: fmtProgram(it),
          style: { left: `${l}px`, width: `${Math.max(r - l - 2, 4)}px` }, dataset: { start: String(p.start), end: String(p.end), focusKey: `tvg-${ch.id}-${p.start}`, whole: whole ? '1' : '' },
          onclick: () => (p.start <= Date.now() - skew && Date.now() - skew < p.end ? playChannel(ch.id, channels) : programDialog(p, ch, channels, Date.now() - skew)) },
          h('span', { class: 'tvg-t' }, programHead(it)),
          h('span', { class: 'tvg-s' }, whole ? 'Live' : it.kind === 'episode' ? fmtEpisode(it) : it.kind === 'live' && it.originalTitle ? it.originalTitle : fmtClock(p.start)),
          on && !whole ? h('i', { class: 'tvg-bar', 'aria-hidden': 'true', style: { width: `${Math.min(100, ((now - p.start) / (p.end - p.start)) * 100)}%` } }) : null);
      }))));
    nowLine = h('div', { class: 'tvg-now', 'aria-hidden': 'true', style: { left: `${CH_WIDTH + x(now)}px` } });
    scroller = h('div', { class: 'tvg', role: 'grid', 'aria-label': 'Live TV guide', 'aria-rowcount': String(rows.length + 1), onkeydown: onKey, onfocusin: (e) => roveTo(e.target) },
      h('div', { class: 'tvg-inner', style: { width: `${CH_WIDTH + width}px` } }, head, ...rows, nowLine));
    holder.replaceChildren(scroller);
    // One tab stop: the program on now on the first channel.
    const first = /** @type {HTMLElement} */ (scroller.querySelector('.tvg-prog.now') || scroller.querySelector('.tvg-prog'));
    if (first) first.tabIndex = 0;
  };

  // roveTo makes el the grid's one tab stop.
  const roveTo = (el) => {
    if (!(el instanceof HTMLElement) || !el.matches('.tvg-prog, .tvg-ch')) return;
    scroller.querySelectorAll('[tabindex="0"]').forEach((b) => { if (b !== el) b.tabIndex = -1; });
    el.tabIndex = 0;
  };
  const go = (el) => {
    if (!el) return;
    roveTo(el);
    el.focus({ preventScroll: true });
    // Keep it clear of the sticky channel column and time header.
    const box = scroller.getBoundingClientRect(), r = el.getBoundingClientRect();
    if (el.matches('.tvg-prog')) {
      if (r.left < box.left + CH_WIDTH) scroller.scrollLeft -= box.left + CH_WIDTH - r.left + 24;
      else if (r.left > box.right - 120) scroller.scrollLeft += r.left - box.left - CH_WIDTH - 24;
    }
    if (r.top < box.top + 40) scroller.scrollTop -= box.top + 40 - r.top;
    else if (r.bottom > box.bottom) scroller.scrollTop += r.bottom - box.bottom;
  };
  // At time t (the middle of the program that has focus), in another row.
  const atTime = (row, t) => {
    const progs = /** @type {HTMLElement[]} */ ([...row.querySelectorAll('.tvg-prog')]);
    return progs.find((b) => +b.dataset.start <= t && t < +b.dataset.end) || progs.at(t < from ? 0 : -1);
  };
  function onKey(e) {
    const el = /** @type {HTMLElement} */ (e.target);
    if (!el.matches('.tvg-prog, .tvg-ch') || e.altKey || e.ctrlKey || e.metaKey) return;
    const row = /** @type {Element} */ (el.closest('.tvg-row'));
    const progs = /** @type {HTMLElement[]} */ ([...row.querySelectorAll('.tvg-prog')]);
    const i = progs.indexOf(el);
    const t = el.matches('.tvg-ch') ? Date.now() - skew : Math.max(+el.dataset.start, from) + Math.min(SLOT, (+el.dataset.end - Math.max(+el.dataset.start, from)) / 2);
    const rowTo = (dir) => {
      let r = row;
      do r = dir > 0 ? r.nextElementSibling : r.previousElementSibling; while (r && !r.matches('.tvg-row'));
      return r;
    };
    let to = null;
    switch (e.key) {
      case 'ArrowRight': to = el.matches('.tvg-ch') ? progs[0] : progs[i + 1]; break;
      case 'ArrowLeft': to = i > 0 ? progs[i - 1] : row.querySelector('.tvg-ch'); break;
      case 'ArrowDown': case 'ArrowUp': {
        const r = rowTo(e.key === 'ArrowDown' ? 1 : -1);
        to = r && (el.matches('.tvg-ch') ? r.querySelector('.tvg-ch') : atTime(r, t));
        break;
      }
      case 'Home': to = e.ctrlKey ? null : row.querySelector('.tvg-ch'); break;
      case 'End': to = progs.at(-1); break;
      default: return;
    }
    e.preventDefault();
    go(to);
  }

  draw();
  // Start at now, a little after the channel column.
  requestAnimationFrame(() => { if (scroller) scroller.scrollLeft = Math.max(0, x(Date.now() - skew) - 3 * PX_PER_MIN * 10); });

  const tick = setInterval(async () => {
    if (!ctx.isCurrent() || !wrap.isConnected) { clearInterval(tick); return; }
    if (isPlayerOpen() || document.querySelector('dialog[open]')) return;
    const now = Date.now() - skew;
    if (now >= from + SLOT) {
      // A new half hour: reload, keeping the time in view and the focus.
      const shift = scroller?.scrollLeft || 0, top = scroller?.scrollTop || 0, old = from;
      const had = /** @type {HTMLElement} */ (document.activeElement)?.dataset?.focusKey;
      if (!await load()) { refreshSoft(); return; }
      if (!wrap.isConnected) return;
      draw();
      if (!scroller) return;
      // The grid now starts later: the same time sits further left.
      scroller.scrollLeft = Math.max(0, shift - ((from - old) / MIN) * PX_PER_MIN);
      scroller.scrollTop = top;
      if (had && had !== 'tv-search') go(/** @type {HTMLElement} */ (scroller.querySelector(`[data-focus-key="${CSS.escape(had)}"]`)));
      return;
    }
    if (!scroller) return;
    nowLine.style.left = `${CH_WIDTH + x(now)}px`;
    for (const b of /** @type {NodeListOf<HTMLElement>} */ (scroller.querySelectorAll('.tvg-prog'))) {
      const s = +b.dataset.start, e = +b.dataset.end;
      const on = s <= now && now < e;
      b.classList.toggle('now', on);
      b.classList.toggle('past', e <= now);
      let bar = /** @type {HTMLElement} */ (b.querySelector('.tvg-bar'));
      if (on && !bar && !b.dataset.whole) b.appendChild(bar = h('i', { class: 'tvg-bar', 'aria-hidden': 'true' }));
      if (bar) { if (on) bar.style.width = `${Math.min(100, ((now - s) / (e - s)) * 100)}%`; else bar.remove(); }
    }
  }, 30000);
  return wrap;
}
