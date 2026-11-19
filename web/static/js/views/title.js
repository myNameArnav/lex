// A movie's (or episode's) details page, and the pieces it shares with the
// show page: hero, play / watched / favorite / watchlist buttons, cast, the
// ⋯ menu, versions, playback compatibility, media info, extras and Fix match.

import { h, clear, icons, resLabel, fmtTime, fmtDuration, fmtBytes, fmtBitrate, toast, modal, spinner, lazyImg, popupMenu, streamLabel, langName, channelName, confirmDialog, run, fmtEpisode, datedEpisode, fmtAirDate } from '../ui.js';
import { api, img, castImg } from '../api.js';
import { openPlayer, isPlayerOpen } from '../player-loader.js';
import { versionLabel, orderFiles, chosenFile, rememberFile } from '../versions.js';
import { compatRequest, TIERS, causeLabel, verdictCauses } from '../caps.js';
import { state, takeEarly, play, refreshSoft, refreshLater } from '../app.js';
import { scroller } from './cards.js';
import { showView } from './show.js';

/** @typedef {import('../../../types/api.gen').Item} Item */
/** @typedef {import('../../../types/api.gen').fileView} fileView */
/** @typedef {import('../../../types/api.gen').UserData} UserData */
/** @typedef {import('../../../types/api.gen').MediaInfo} MediaInfo */

// playExtra plays one of a title's local extras (a trailer, a featurette…)
// from the top. Watching one leaves the title's progress alone.
/** @param {Item} it */
function playExtra(it, x) {
  openPlayer({ itemId: it.id, extra: x.id, start: 0, onClose: () => { if (!isPlayerOpen()) refreshSoft(); } });
}

const EXTRA_KINDS = { trailer: 'Trailer', featurette: 'Featurette', behindthescenes: 'Behind the scenes', deleted: 'Deleted scene', interview: 'Interview', scene: 'Scene', short: 'Short', other: 'Extra' };

// trailerButton plays the title's first local trailer, if it has one.
/** @param {Item} it */
export function trailerButton(it, extras) {
  const t = extras?.find((x) => x.type === 'trailer');
  return t ? h('button', { class: 'btn lg', dataset: { focusKey: `trailer-${it.id}` }, onclick: () => playExtra(it, t) }, h('span', { html: icons.play }), 'Trailer') : null;
}

// extrasRow lists a title's local extras, each with a frame from it.
/** @param {Item} it */
export function extrasRow(it, extras) {
  if (!extras?.length) return null;
  return h('section', { class: 'shelf' },
    h('div', { class: 'shelf-head' }, h('h2', { class: 'section-title' }, 'Extras')),
    scroller(h('div', { class: 'shelf-track', tabindex: '-1', role: 'region', 'aria-label': 'Extras' }, extras.map((x) => {
      const kind = EXTRA_KINDS[x.type] || 'Extra';
      const sub = [x.title !== kind ? kind : '', x.duration ? fmtTime(x.duration) : ''].filter(Boolean).join(' · ');
      return h('div', { class: 'card land', title: x.title },
        h('button', { type: 'button', class: 'art', 'aria-label': `Play ${x.title}${sub ? `, ${sub}` : ''}`, dataset: { focusKey: `extra-${x.id}` }, onclick: () => playExtra(it, x) },
          h('div', { class: 'ph' }, kind), lazyImg(`/api/files/${x.id}/thumb?w=480`, ''),
          h('div', { class: 'play-hover', 'aria-hidden': 'true' }, h('span', { html: icons.play }))),
        h('div', { class: 'meta' }, h('div', null, h('div', { class: 't' }, x.title), sub ? h('div', { class: 's' }, sub) : null)));
    }))));
}

export async function itemView(ctx, id) {
  const d = await takeEarly(`/api/items/${id}`);
  const it = d.item;
  if (it.kind === 'season') {
    location.replace(`#/item/${it.showId}?season=${it.id}`);
    return h('div');
  }
  return it.kind === 'show' ? showView(ctx, d) : mediaView(ctx, d);
}

/** @param {Item} it */
export function hero(it, posterNode, info, backdropItem = it) {
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
/** @param {Item} it */
export function heroPoster(it, kind, land) {
  const box = h('div', { class: `hero-poster ${land ? 'land' : ''}`, style: { position: 'relative' } }, h('div', { class: 'ph', 'aria-hidden': 'true', style: { position: 'absolute', inset: 0, display: 'grid', placeItems: 'center', padding: '16px', textAlign: 'center', color: 'var(--text2)', fontWeight: 700, fontSize: '18px' } }, it.title));
  const im = lazyImg(img(it, kind, land ? 640 : 480), '');
  im.style.position = 'relative';
  box.appendChild(im);
  return box;
}

/** @param {Item} it */
export function facts(it, extra = []) {
  const f = [];
  if (it.year && it.kind !== 'episode') f.push(h('span', null, it.year));
  if (it.premiere && it.kind === 'episode') f.push(h('span', null, fmtAirDate(it.premiere)));
  if (it.duration) f.push(h('span', null, fmtDuration(it.duration)));
  else if (it.runtime) f.push(h('span', null, `${it.runtime}m`));
  if (it.contentRating) f.push(h('span', { class: 'pill' }, it.contentRating));
  if (it.rating) f.push(h('span', null, h('span', { class: 'star' }, '★ '), it.rating.toFixed(1)));
  return h('div', { class: 'facts' }, f, extra);
}

/** @param {Item} it */
function playButtons(it, playId = it.id, label = 'Play') {
  /** @type {Partial<UserData>} */
  const ud = it.userData || {};
  const btns = [];
  if (ud.position > 0) {
    btns.push(h('button', { class: 'btn primary lg', dataset: { focusKey: `play-${playId}` }, onclick: () => play(playId, ud.position) }, h('span', { html: icons.play }), `Resume from ${fmtTime(ud.position)}`));
    btns.push(h('button', { class: 'btn lg', dataset: { focusKey: `restart-${playId}` }, onclick: () => play(playId, 0) }, 'Play from start'));
  } else {
    btns.push(h('button', { class: 'btn primary lg', dataset: { focusKey: `play-${playId}` }, onclick: () => play(playId, 0) }, h('span', { html: icons.play }), label));
  }
  return btns;
}

// watchedButton marks a title (a show: every episode) watched or unwatched.
// Shows ask first: that clears every resume point at once.
/** @param {Item} it */
export function watchedButton(it) {
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
/** @param {Item} it */
export function favButton(it) {
  let fav = !!it.userData?.favorite, busy = false;
  const b = h('button', { class: 'btn lg icon', dataset: { focusKey: `fav-${it.id}` }, onclick: async () => {
    if (busy) return;
    busy = true;
    fav = !fav;
    show();
    try {
      await api(`/api/items/${it.id}/favorite`, { method: 'POST', body: { favorite: fav } });
      (it.userData ||= /** @type {UserData} */ ({})).favorite = fav;
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

// watchlistButton toggles "Watch later" in place, like favButton.
/** @param {Item} it */
export function watchlistButton(it) {
  let on = !!it.userData?.watchlist, busy = false;
  const b = h('button', { class: 'btn lg icon', dataset: { focusKey: `watchlist-${it.id}` }, onclick: async () => {
    if (busy) return;
    busy = true;
    on = !on;
    show();
    try {
      await api(`/api/items/${it.id}/watchlist`, { method: 'POST', body: { watchlist: on } });
      (it.userData ||= /** @type {UserData} */ ({})).watchlist = on;
    } catch (ex) {
      on = !on;
      show();
      toast(ex.message, 'error');
    } finally {
      busy = false;
    }
  } });
  const show = () => {
    const label = on ? 'Remove from Watchlist' : 'Add to Watchlist';
    b.innerHTML = on ? icons.bookmarkFill : icons.bookmark;
    b.style.color = on ? 'var(--accent)' : '';
    b.setAttribute('aria-pressed', String(on));
    b.setAttribute('aria-label', label);
    b.title = label;
  };
  show();
  return b;
}

// Genre chips open the title's library filtered by that genre.
/** @param {Item} it */
export function genreLinks(it) {
  return h('div', { class: 'genres' }, it.genres.map((g) => h('a', { href: `#/library/${it.libraryId}?genre=${encodeURIComponent(g)}`, title: `More ${g} in this library` }, g)));
}

const personHref = (name) => `#/person?name=${encodeURIComponent(name)}`;

// moreItems lists the ⋯ menu's actions. They depend only on the item, its
// files and the user, so the button is left out when there are none.
/** @param {Item} it */
function moreItems(it, d) {
  const items = [];
  if (d.files?.length) items.push({ label: 'Media info', icon: 'info', onClick: () => mediaInfoModal(d.files) });
  /** @type {Partial<UserData>} */
  const ud = it.userData || {};
  if (it.kind !== 'show' && ud.position > 0) {
    items.push({ label: 'Remove from Continue Watching', icon: 'close', onClick: async () => {
      if (await run(null, () => api(`/api/items/${it.id}/progress`, { method: 'DELETE' }), 'Removed from Continue Watching')) refreshSoft();
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
    items.push({ label: 'Re-detect intro & credits', icon: 'refresh', onClick: () => run(null, () => api(`/api/items/${it.id}/intro/reset`, { method: 'POST' }), 'Intro & credits detection queued') });
  }
  return items;
}

/** @param {Item} it */
export function moreButton(it, d) {
  const items = moreItems(it, d);
  if (!items.length) return null;
  return h('button', { class: 'btn lg icon', title: 'More actions', 'aria-label': 'More actions', dataset: { focusKey: `more-${it.id}` }, html: icons.more, onclick: (e) => popupMenu(e.currentTarget, items) });
}

/** @param {Item} it */
export function castRow(it, excludeDirectors = false) {
  const people = (it.cast || []).map((person, index) => ({ person, index })).filter(({ person }) => !excludeDirectors || person.role !== 'Director');
  if (!people.length) return null;
  return h('section', { class: 'shelf' },
    h('div', { class: 'shelf-head' }, h('h2', { class: 'section-title' }, 'Cast & crew')),
    scroller(h('div', { class: 'people', tabindex: '-1', role: 'region', 'aria-label': 'Cast & crew' }, people.map(({ person: p, index }) => {
      const portrait = h('div', { class: 'ph' }, p.name.slice(0, 1));
      if (p.image) portrait.replaceChildren(lazyImg(castImg(it, index), p.name, () => { portrait.textContent = p.name.slice(0, 1); }));
      return h('a', { class: 'person', href: personHref(p.name), 'aria-label': p.role ? `${p.name}, ${p.role}` : p.name }, portrait, h('div', { class: 'n' }, p.name), h('div', { class: 'r' }, p.role || ''));
    }))));
}

// Metadata still being fetched: look once more a little later (once per
// title, so a stuck fetch doesn't poll forever). It counts once the re-check
// runs, so an earlier in-page refresh doesn't use it up.
let metaRetried = 0;

export function retryWhileFetching(ctx, it) {
  if (it.metaStatus !== 0 || metaRetried === it.id) return;
  refreshLater(ctx, 8000, () => { metaRetried = it.id; });
}

function mediaView(ctx, d) {
  const it = d.item;
  const isEp = it.kind === 'episode';
  const poster = heroPoster(it, isEp ? 'thumb' : 'poster', isEp);
  // Play plays the version last chosen on this device (see versions.js).
  const files = orderFiles(d.files || []);
  const file = files.length ? chosenFile(it.id, files) : null;
  const compat = file ? compatBadge(it, file) : null;
  const info = [];
  if (isEp && d.show) {
    info.push(h('a', { class: 'kicker', href: `#/item/${d.show.id}?season=${it.parentId}` }, `${d.show.title} · Season ${it.season}`));
  }
  info.push(h('h1', null, it.title));
  info.push(facts(it, [
    isEp && !datedEpisode(it) ? h('span', null, fmtEpisode(it)) : null, // a dated one's code is its air date, already shown
    file ? h('span', { class: 'pill' }, [resLabel(file.width, file.height), file.hdr].filter(Boolean).join(' ') || file.container) : null,
    compat?.pill,
    file?.cached ? h('span', { class: 'pill', title: 'Plays from the SSD cache' }, 'SSD') : null,
    d.segments?.find((s) => s.kind === 'intro') ? h('span', { class: 'pill', title: 'Intro detected' }, 'Intro ✓') : null,
    d.segments?.find((s) => s.kind === 'credits') ? h('span', { class: 'pill', title: 'End credits detected' }, 'Credits ✓') : null,
  ]));
  if (it.genres?.length) info.push(genreLinks(it));
  if (d.collection) info.push(h('p', { class: 'collection-link' }, 'Part of ', h('a', { href: `#/collection/${d.collection.id}` }, d.collection.name)));
  if (!file) info.push(h('p', { class: 'bad' }, 'No media file found for this item.'));
  info.push(h('div', { class: 'actions' },
    file ? playButtons(it) : null,
    trailerButton(it, d.extras),
    watchedButton(it),
    it.kind === 'movie' ? favButton(it) : null,
    it.kind === 'movie' ? watchlistButton(it) : null,
    moreButton(it, d)));
  if (files.length > 1) info.push(versionPicker(it, files, file));
  if (it.tagline) info.push(h('div', { class: 'tagline' }, it.tagline));
  info.push(h('p', { class: 'overview' }, it.overview || (it.metaStatus === 0 ? 'Fetching details…' : 'No description available.')));
  retryWhileFetching(ctx, it);
  if (file) info.push(fileSummary(file), compat.note);
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
  if (director.length) info.push(h('p', { class: 'muted small' }, 'Directed by ', director.map((p, i) => [i ? ', ' : '', h('a', { class: 'plain-link', href: personHref(p.name) }, p.name)])));
  if (it.studios?.length) info.push(h('p', { class: 'muted small', style: { marginTop: '4px' } }, it.studios.slice(0, 4).join(' · ')));
  return h('div', null, hero(it, poster, info, isEp && d.show ? d.show : it), extrasRow(it, d.extras), castRow(it, true));
}

// versionPicker chooses which of a title's files Play plays: its versions
// (cuts, resolutions) or the parts of a split film. The choice is kept on
// this device, and the page redraws for the chosen file.
/** @param {Item} it @param {fileView[]} files @param {fileView} file */
function versionPicker(it, files, file) {
  const parts = files.every((f) => f.part > 0);
  const sel = h('select', { 'aria-label': parts ? 'Part' : 'Version', dataset: { focusKey: `version-${it.id}` },
    onchange: (e) => { rememberFile(it.id, Number(e.target.value), files); refreshSoft(); } },
  files.map((f) => h('option', { value: String(f.id), selected: f.id === file.id }, versionLabel(f, files))));
  return h('label', { class: 'row version-pick' }, h('span', { class: 'muted small' }, parts ? `${files.length} parts` : `${files.length} versions`), sel);
}

// compatBadge says how the file the Play button plays (the chosen one) plays in
// this browser: a chip next to the resolution, and the reasons below the
// file summary when the server has to do the work. Both stay empty until the
// server answers, and on error.
/** @param {Item} it @param {fileView} file */
function compatBadge(it, file) {
  const pill = h('span', { class: 'pill play-tier', hidden: true });
  const note = h('p', { class: 'small compat-note', hidden: true });
  api(`/api/items/${it.id}/compat`, { method: 'POST', body: compatRequest() }).then((r) => {
    const v = r.files.find((f) => f.fileId === file.id);
    if (!v || v.tier === 'unknown') return;
    const tier = TIERS[v.tier];
    const why = verdictCauses(v.tier, v.causes).map((c) => causeLabel(c, r));
    pill.textContent = tier.label;
    pill.dataset.tier = v.tier;
    pill.title = [`In this browser: ${tier.text}`, ...why].join('\n');
    pill.hidden = false;
    if (['remux', 'transcode', 'unplayable'].includes(v.tier) && why.length) {
      note.textContent = `${tier.label} in this browser: ${why.join('; ')}.`;
      note.hidden = false;
    }
  }).catch(() => {});
  return { pill, note };
}

/** @param {fileView} f */
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

/** @param {fileView[]} files */
function mediaInfoModal(files) {
  const body = files.map((f) => {
    /** @type {Partial<MediaInfo>} */
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
      /** @type {[string, string | number][]} */
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

// fixMatch searches the metadata providers for it (a movie or show) and
// applies the chosen match. onDone runs after a match is applied; by default
// the page redraws.
/** @param {any} it @param {{ onDone?: () => any }} [options] */
export async function fixMatch(it, { onDone = refreshSoft } = {}) {
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
    onDone();
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
