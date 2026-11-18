// The Live TV guide: what's on each channel now and next.

import { h, icons, lazyImg, fmtEpisode, fmtClock, emptyState } from '../ui.js';
import { api, img } from '../api.js';
import { openPlayer, isPlayerOpen } from '../player-loader.js';
import { state, renderNav, refreshSoft, refreshLater } from '../app.js';
import { placeholder, progressBar } from './cards.js';

// playChannel tunes into a Live TV channel; channels are what Next and
// Page Up/Down switch through.
export function playChannel(id, channels) {
  openPlayer({ channel: id, channels, onClose: () => { if (!isPlayerOpen()) refreshSoft(); } });
}

// The guide's two lines for a program: the show or movie, then the episode.
export function programHead(it) {
  return it.kind === 'episode' ? it.showTitle || it.title : it.title;
}

export function programSub(it) {
  return it.kind === 'episode' ? `${fmtEpisode(it)} — ${it.title}` : String(it.year || '');
}

// The guide: each channel's program now (the art tunes in) and what's next.
// It refreshes every minute; the time left is relative to the server clock.
export async function tvView(ctx) {
  const off = () => emptyState({ title: 'Live TV is off', text: state.me.isAdmin ? 'Turn it on in Settings › Live TV.' : 'Ask your server admin to turn it on.',
      actions: [state.me.isAdmin ? h('a', { class: 'btn primary', href: '#/settings/livetv' }, 'Open settings') : h('a', { class: 'btn primary', href: '#/' }, 'Go home')] });
  if (!state.caps.liveTv) return off();
  let data;
  try { data = await api('/api/tv'); } catch (e) {
    if (e.status !== 404) throw e;
    // Turned off since this page loaded: drop it from the menu too.
    state.caps = { ...state.caps, liveTv: false };
    renderNav();
    return off();
  }
  const skew = Date.now() - data.serverTime;
  const wrap = h('div', { class: 'page' }, h('h1', { class: 'page-title' }, 'Live TV'),
    h('p', { class: 'muted', style: { margin: 0 } }, 'Channels that play your library at random, like TV. Tune in to join whatever is on.'));
  if (!data.channels.length) {
    wrap.appendChild(emptyState({ level: 'h2', title: 'Nothing to air yet', text: 'Live TV plays movies and episodes once a library scan has read their running times.' }));
    return wrap;
  }
  const channels = data.channels.map(({ id, number, name }) => ({ id, number, name }));
  const now = Date.now() - skew;
  const list = h('ol', { class: 'tv-guide' }, data.channels.map((ch) => {
    const [cur, ...later] = ch.programs;
    const it = cur.item;
    const left = Math.max(1, Math.round((cur.end - now) / 60000));
    const label = `Channel ${ch.number}, ${ch.name}: ${programHead(it)}${programSub(it) ? `, ${programSub(it)}` : ''}, ${left} min left`;
    return h('li', { class: 'tv-channel' },
      h('div', { class: 'tv-num', 'aria-hidden': 'true' }, h('b', null, ch.number), h('span', null, ch.name)),
      h('button', { type: 'button', class: 'card tv-now', 'aria-label': `Tune in to ${label}`, dataset: { focusKey: `tv-${ch.id}` }, onclick: () => playChannel(ch.id, channels) },
        h('div', { class: 'art' }, placeholder(it), lazyImg(img(it, 'thumb', 480), ''),
          h('div', { class: 'play-hover', 'aria-hidden': 'true' }, h('span', { html: icons.play })),
          progressBar(now - cur.start, cur.end - cur.start)),
        h('div', { class: 'meta' },
          h('div', { class: 'small tv-on' }, h('span', { class: 'tv-dot', 'aria-hidden': 'true' }), `On now · ${left} min left`),
          h('div', { class: 't' }, programHead(it)), h('div', { class: 's' }, programSub(it)))),
      h('ul', { class: 'tv-later', 'aria-label': `Later on ${ch.name}` }, later.map((p) => h('li', null,
        h('span', { class: 'tv-time' }, fmtClock(p.start)), h('span', { class: 'tv-title' }, programHead(p.item), p.item.kind === 'episode' ? h('span', { class: 'muted' }, ` · ${fmtEpisode(p.item)}`) : null)))));
  }));
  wrap.appendChild(list);
  refreshLater(ctx, 60000);
  return wrap;
}
