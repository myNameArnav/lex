// Settings › Live TV: the switch, the lineup (every channel in order, each
// on or off), and a dialog that edits one of your own channels with a
// preview of what it airs. Every change saves on its own.

import { h, icons, toggle, run, modal, confirmDialog, fmtClock, fmtProgram } from '../ui.js';
import { api } from '../api.js';
import { state, renderNav } from '../app.js';
import { heading, field, select, toggleRow, formActions, formErrors, submitInline } from './forms.js';
import { streamDialog, sourceDialog, browseDialog, sourceRow, host } from './livetv-streams.js';

/** @typedef {import('../../../types/api.gen').tvLineup} Lineup */
/** @typedef {import('../../../types/api.gen').tvLineupChannel} LineupChannel */
/** @typedef {import('../../../types/api.gen').TVChannel} TVChannel */

const plural = (n, one, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

// titlePicker edits a list of movies and shows (item ids): chips for the
// picked ones, and a search box that adds more. get() reads the list and
// set(ids) replaces it; the picker tells the form itself.
const titleInfo = new Map(); // id -> item, from searches and lookups

let pickerId = 0;

function titlePicker(label, help, get, set) {
  const id = `picker-${++pickerId}`;
  const chips = h('div', { class: 'chips' });
  const results = h('ul', { class: 'title-results', 'aria-label': `${label}: search results` });
  const el = h('div', { class: 'field title-picker' });
  const changed = () => el.dispatchEvent(new Event('input', { bubbles: true }));
  const name = (t) => (t ? `${t.title}${t.year ? ` (${t.year})` : ''}` : 'Title no longer in the library');
  const drawChips = () => chips.replaceChildren(...get().map((t) => h('span', { class: 'chip' }, titleInfo.has(t) || !loading.has(t) ? name(titleInfo.get(t)) : 'Loading…',
    h('button', { type: 'button', 'aria-label': `Remove ${name(titleInfo.get(t))}`, html: icons.close, onclick: () => {
      set(get().filter((x) => x !== t)); drawChips(); drawResults(); changed(); input.focus();
    } }))));
  // Saved titles are only ids: look up their names once.
  const loading = new Set(get().filter((t) => !titleInfo.has(t)));
  Promise.all([...loading].map((t) => api(`/api/items/${t}`).then((d) => titleInfo.set(t, d.item), () => {}))).then(() => { loading.clear(); drawChips(); });
  let found = [], seq = 0, timer = 0;
  const pick = (t) => { set([...get(), t.id]); drawChips(); drawResults(); changed(); };
  const drawResults = () => results.replaceChildren(...found.filter((t) => !get().includes(t.id)).map((t) =>
    h('li', null, h('button', { type: 'button', onclick: () => { pick(t); input.focus(); } }, h('span', null, name(t)), h('span', { class: 'muted' }, t.kind === 'show' ? 'Show' : 'Movie')))));
  const search = async (q) => {
    const mine = ++seq;
    if (!q) { found = []; drawResults(); return; }
    const r = await api(`/api/search?q=${encodeURIComponent(q)}`).catch(() => null);
    if (mine !== seq || !r) return;
    found = [...r.shows, ...r.movies].sort((x, y) => x.title.localeCompare(y.title));
    for (const t of found) titleInfo.set(t.id, t);
    drawResults();
  };
  const input = h('input', { type: 'search', placeholder: 'Search movies and shows', autocomplete: 'off', 'aria-labelledby': id, 'aria-describedby': `${id}-h`,
    oninput: (e) => { clearTimeout(timer); timer = setTimeout(() => search(e.target.value.trim()), 250); },
    // Enter adds the first result rather than saving the dialog.
    onkeydown: (e) => {
      if (e.key === 'Enter') { e.preventDefault(); const first = found.find((t) => !get().includes(t.id)); if (first) pick(first); }
      if (e.key === 'Escape' && input.value) { e.preventDefault(); e.stopPropagation(); input.value = ''; search(''); }
    } });
  drawChips();
  el.append(h('span', { id }, label), h('div', { class: 'help', id: `${id}-h` }, help), chips, input, results);
  return el;
}

// checks is a set of checkbox chips for list `key` of draft; none ticked
// means no limit.
let checksId = 0;

function checks(label, none, draft, key, options) {
  const id = `checks-${++checksId}`;
  return h('div', { class: 'field' }, h('span', { id }, label, ' ', h('span', { class: 'muted' }, none)),
    h('div', { class: 'check-chips', role: 'group', 'aria-labelledby': id },
      options.map(([v, l]) => h('label', null, h('input', { type: 'checkbox', checked: draft[key].includes(v),
        onchange: (e) => { const cur = draft[key].filter((x) => x !== v); draft[key] = e.target.checked ? [...cur, v] : cur; } }), l))));
}

const KINDS = [['', 'Movies and shows'], ['movie', 'Movies only'], ['show', 'Shows only']];

// summary describes what a channel airs, in a few words.
/** @param {LineupChannel} ch */
function summary(ch) {
  if (ch.source === 'all') return 'Automatic · every library';
  if (ch.source === 'library') return 'Automatic · library';
  if (ch.source === 'genre') return 'Automatic · genre';
  if (ch.source === 'stream') return `Live stream · ${host(ch.stream?.url || '')}`;
  const c = ch.custom;
  if (!c) return '';
  if (c.kind === 'picked') return 'Picked titles';
  const libs = c.libraries.map((id) => state.libraries.find((l) => l.id === id)?.name).filter(Boolean);
  return [KINDS.find(([k]) => k === c.kind)?.[1].replace(' only', ''), libs.join(', '), c.genres.join(', '),
    c.exclude.length ? `leaves out ${c.exclude.length}` : ''].filter(Boolean).join(' · ');
}

// channelDialog adds a channel (ch null) or edits one of yours, with a
// preview of what it would air. onSaved gets the new lineup and the id of
// the channel saved; onRemoved the lineup after removing it.
/**
 * @param {LineupChannel | null} ch
 * @param {string[]} genres
 * @param {{ onSaved: (l: Lineup, id: string) => void, onRemoved: (l: Lineup) => void }} cb
 */
function channelDialog(ch, genres, { onSaved, onRemoved }) {
  const saved = ch?.custom;
  /** @type {TVChannel} */
  const draft = structuredClone(saved || { id: '', name: '', kind: '', libraries: [], genres: [], titles: [], exclude: [] });
  // A channel either picks its titles or filters the library; switching
  // keeps the other's choices for switching back (and the server keeps them).
  let picked = draft.kind === 'picked';
  let kind = picked ? '' : draft.kind;
  const body = () => ({ ...draft, name: draft.name.trim(), kind: picked ? 'picked' : kind });

  const errors = formErrors(`tv-err-${++checksId}`, []);
  const name = h('input', { type: 'text', value: draft.name, maxlength: 60, required: true, placeholder: 'e.g. Crime Night', oninput: (e) => { draft.name = e.target.value; } });
  // Genres a channel keeps but the library no longer has stay listed.
  const gs = [...new Set([...genres, ...draft.genres])];
  const pickedEl = titlePicker('Titles', 'The channel plays only these movies and shows (every episode of a show).', () => draft.titles, (v) => { draft.titles = v; });
  const filters = h('div', { class: 'tv-filters' },
    field('Plays', select(kind, KINDS, (v) => { kind = v; refresh(); })),
    state.libraries.length > 1 ? checks('Libraries', '(none ticked: all)', draft, 'libraries', state.libraries.map((l) => [l.id, l.name])) : null,
    gs.length ? checks('Genres', '(none ticked: any)', draft, 'genres', gs.map((g) => [g, g])) : null,
    titlePicker('Leave out', 'Movies and shows this channel never plays, even when they match.', () => draft.exclude, (v) => { draft.exclude = v; }));
  const modeBtn = (on, label) => h('button', { type: 'button', class: 'seg-btn', 'aria-pressed': String(picked === on), onclick: () => {
    picked = on; mode.querySelectorAll('button').forEach((b, i) => b.setAttribute('aria-pressed', String(picked === (i === 1)))); show(); refresh();
  } }, label);
  const mode = h('div', { class: 'seg', role: 'group', 'aria-label': 'What the channel plays' }, modeBtn(false, 'Match filters'), modeBtn(true, 'Pick titles'));
  const show = () => { pickedEl.hidden = !picked; filters.hidden = picked; };
  show();

  // The preview follows every change, a moment after the last.
  const preview = h('div', { class: 'tv-preview', 'aria-live': 'polite' });
  let seq = 0, timer = 0;
  const refresh = () => {
    clearTimeout(timer);
    timer = setTimeout(async () => {
      const mine = ++seq;
      let r;
      try { r = await api('/api/admin/tv/preview', { method: 'POST', body: body() }); } catch (e) {
        if (mine === seq) preview.replaceChildren(h('p', { class: 'help warn' }, `Couldn’t preview: ${e.message}`));
        return;
      }
      if (mine !== seq || !preview.isConnected) return;
      if (!r.titles) {
        preview.replaceChildren(h('p', { class: 'warn' }, picked ? 'Nothing to air yet: pick some titles.' : 'Nothing to air: no titles match these choices.'));
        return;
      }
      preview.replaceChildren(
        h('p', null, h('b', null, plural(r.titles, 'title')), r.programs.length ? h('span', { class: 'muted' }, ' · coming up:') : h('span', { class: 'muted' }, ' · nothing coming up')),
        h('ol', { class: 'tv-preview-list' }, r.programs.map((p) => h('li', null, h('span', { class: 'tv-time' }, fmtClock(p.start)), h('span', { class: 'tv-title' }, fmtProgram(p.item))))));
    }, 300);
  };
  const form = h('form', { class: 'form tv-channel-form', novalidate: true, oninput: refresh, onchange: refresh, onsubmit: (e) => { e.preventDefault(); submit(); } },
    field('Channel name', name),
    h('div', { class: 'field' }, h('span', null, 'What it plays'), mode),
    pickedEl, filters,
    h('div', { class: 'field' }, h('span', null, 'Preview'), preview));
  refresh();

  const saveBtn = h('button', { class: 'btn primary', type: 'button', onclick: () => submit() }, saved ? 'Save' : 'Add channel');
  const submit = () => {
    errors.reset();
    const b = body();
    if (!b.name) { errors.fail('Give the channel a name', name); return; }
    if (picked && !b.titles.length) { errors.fail('Pick at least one title', pickedEl.querySelector('input')); return; }
    submitInline(saveBtn, errors, async () => {
      const l = await api(saved ? `/api/admin/tv/channels/${encodeURIComponent(saved.id)}` : '/api/admin/tv/channels', { method: saved ? 'PUT' : 'POST', body: b });
      m.close();
      onSaved(l, saved ? ch.id : l.saved);
    }, saved ? `Saved ${b.name}` : `Added ${b.name}`, (msg) => (/name/i.test(msg) ? [name] : []));
  };
  const removeBtn = saved ? h('button', { class: 'btn danger', type: 'button', onclick: async () => {
    if (!await confirmDialog({ message: `Remove ${saved.name} from Live TV? The channels after it move up a number.`, okLabel: 'Remove channel', danger: true, title: 'Remove channel' })) return;
    run(removeBtn, async () => {
      const l = await api(`/api/admin/tv/channels/${encodeURIComponent(saved.id)}`, { method: 'DELETE' });
      m.close();
      onRemoved(l);
    }, `Removed ${saved.name}`);
  } }, h('span', { html: icons.trash }), 'Remove') : null;

  const m = modal({
    title: saved ? `Edit ${saved.name}` : 'New channel', wide: true, dismissible: false, body: form,
    actions: [removeBtn, h('div', { class: 'spacer' }), errors.err, h('button', { class: 'btn', type: 'button', onclick: () => m.close() }, 'Cancel'), saveBtn],
  });
  requestAnimationFrame(() => { name.focus(); if (saved) name.select(); });
}

export async function liveTvSection() {
  /** @type {Lineup} */
  let lineup = await api('/api/admin/tv');
  const genres = await api('/api/genres').catch(() => []);

  // The menu's Live TV link follows the switch (see caps in auth.go).
  const refreshCaps = async () => {
    try { state.caps = (await api('/api/me')).caps || state.caps; } catch {}
    renderNav();
  };
  const enable = toggleRow('Enable Live TV', 'Adds Live TV to the menu for every user. Channels can be set up while it’s off.', lineup.enabled, (on) => {
    run(null, async () => { lineup = await api('/api/admin/tv', { method: 'PUT', body: { enabled: on } }); await refreshCaps(); },
      on ? 'Live TV is on' : 'Live TV is off').then((ok) => { if (!ok) /** @type {HTMLInputElement} */ (enable.querySelector('input')).checked = !on; });
  });

  const list = h('ol', { class: 'tv-lineup', 'aria-label': 'Channels' });
  // draw redraws the list, keeping focus on the control that had it (by its
  // focus key), or on focus (a key) when given.
  const draw = (focus) => {
    const had = focus || /** @type {HTMLElement} */ (document.activeElement)?.dataset?.focusKey;
    const last = lineup.channels.length - 1;
    list.replaceChildren(...lineup.channels.map((ch, i) => {
      const key = ch.id;
      const nothing = !ch.titles && ch.source !== 'stream';
      return h('li', { class: `tv-row${ch.off ? ' off' : ''}` },
        h('span', { class: 'tv-row-num', 'aria-hidden': 'true' }, ch.number),
        h('div', { class: 'tv-row-main' },
          h('div', { class: 'tv-row-name' }, h('span', { class: 'sr-only' }, `Channel ${ch.number}, `), ch.name),
          h('div', { class: 'tv-row-sub' }, summary(ch), ch.source === 'stream' ? null : [' · ', nothing ? h('span', { class: 'warn' }, 'nothing to air') : plural(ch.titles, 'title')])),
        h('div', { class: 'tv-row-actions' },
          h('button', { type: 'button', class: 'btn icon ghost sm', 'aria-label': `Move ${ch.name} up`, title: 'Move up', disabled: i === 0, dataset: { focusKey: `up-${key}` }, html: icons.up, onclick: () => move(i, -1) }),
          h('button', { type: 'button', class: 'btn icon ghost sm tv-down', 'aria-label': `Move ${ch.name} down`, title: 'Move down', disabled: i === last, dataset: { focusKey: `down-${key}` }, html: icons.up, onclick: () => move(i, 1) }),
          ch.custom || ch.stream ? h('button', { type: 'button', class: 'btn sm', 'aria-label': `Edit ${ch.name}`, dataset: { focusKey: `edit-${key}` }, onclick: () => edit(ch) }, 'Edit')
            : h('button', { type: 'button', class: 'btn sm tv-row-remove', 'aria-label': `Remove ${ch.name}`, title: 'Remove from the lineup (it can be restored)', dataset: { focusKey: `remove-${key}` }, onclick: () => removeAuto(ch) }, 'Remove'),
          // A label, so clicking the switch's track toggles it.
          (() => { const t = toggle(!ch.off, (on) => setOff(ch.id, !on), `${ch.name} on Live TV`); /** @type {HTMLElement} */ (t.firstChild).dataset.focusKey = `on-${key}`; return h('label', { class: 'tv-row-switch', title: ch.off ? 'Off: not on the guide' : 'On the guide' }, t); })()));
    }));
    let el = had && /** @type {HTMLElement | null} */ (list.querySelector(`[data-focus-key="${CSS.escape(had)}"]`));
    // A move button that just got disabled (top or bottom): its pair.
    if (el?.matches(':disabled')) el = /** @type {HTMLElement | null} */ (el.parentElement.querySelector('button:not(:disabled)'));
    el?.focus();
  };
  // save sends a lineup change; the list shows it at once and goes back if
  // it fails.
  const save = (next, body, okMsg) => {
    const before = lineup;
    lineup = next;
    draw();
    run(null, async () => { lineup = await api('/api/admin/tv', { method: 'PUT', body }); draw(); }, okMsg).then((ok) => { if (!ok) { lineup = before; draw(); } });
  };
  const move = (i, by) => {
    const chs = [...lineup.channels];
    const [ch] = chs.splice(i, 1);
    chs.splice(i + by, 0, ch);
    save({ ...lineup, channels: chs.map((c, j) => ({ ...c, number: j + 1 })) }, { order: chs.map((c) => c.id) });
  };
  // Automatic channels are removed from the lineup (the rest move up a
  // number) and can be restored; the admin's own are removed in their dialog.
  const removed = h('p', { class: 'muted small tv-removed', role: 'status' });
  const setRemoved = (ids, okMsg) => run(null, async () => {
    lineup = await api('/api/admin/tv', { method: 'PUT', body: { removed: ids } });
    draw(); drawRemoved();
  }, okMsg);
  const removeAuto = (ch) => {
    const i = lineup.channels.indexOf(ch);
    const next = lineup.channels[i + 1] || lineup.channels[i - 1];
    setRemoved([...lineup.removed.map((r) => r.id), ch.id], `Removed ${ch.name}`).then(() => {
      /** @type {HTMLElement | null} */ (next && list.querySelector(`[data-focus-key="remove-${CSS.escape(next.id)}"], [data-focus-key="edit-${CSS.escape(next.id)}"]`))?.focus();
    });
  };
  const drawRemoved = () => {
    const n = lineup.removed.length;
    removed.hidden = !n;
    removed.replaceChildren(n ? `Removed: ${lineup.removed.map((r) => r.name).join(', ')}. ` : '',
      n ? h('button', { type: 'button', class: 'btn sm ghost', onclick: () => setRemoved([], `Restored ${n} channel${n === 1 ? '' : 's'}`) }, n === 1 ? 'Restore it' : `Restore all ${n}`) : '');
  };
  const setOff = (id, off) => {
    const chs = lineup.channels.map((c) => (c.id === id ? { ...c, off } : c));
    save({ ...lineup, channels: chs }, { off: chs.filter((c) => c.off).map((c) => c.id) });
  };
  const done = { onSaved: (l, id) => { lineup = l; draw(`edit-${id}`); drawSources(); }, onRemoved: (l) => { lineup = l; draw('tv-new'); drawSources(); } };
  const edit = (ch) => (ch?.stream ? streamDialog(ch.stream, done) : channelDialog(ch, genres, done));
  const add = h('button', { class: 'btn primary', type: 'button', dataset: { focusKey: 'tv-new' }, onclick: () => edit(null) }, h('span', { html: icons.plus }), 'New channel');
  const addStream = h('button', { class: 'btn', type: 'button', dataset: { focusKey: 'tv-new-stream' }, onclick: () => streamDialog(null, done) }, h('span', { html: icons.plus }), 'New live channel');

  // Playlists (IPTV) to pick live channels from.
  const sources = h('ol', { class: 'tv-lineup', 'aria-label': 'Playlists' });
  const guideNote = h('p', { class: 'help warn', role: 'status' });
  const noSources = h('p', { class: 'muted', style: { margin: 0 } }, 'No playlists. Add one to pick channels from it, or add a single live channel above.');
  const drawSources = () => {
    const have = new Set(lineup.channels.filter((c) => c.stream).map((c) => c.stream.url));
    const saved = (l) => { lineup = l; draw(); drawSources(); };
    sources.replaceChildren(...lineup.sources.map((src) => sourceRow(src, {
      browse: () => browseDialog(src, have, saved),
      edit: () => sourceDialog(lineup.sources, src, saved),
      remove: async (e) => {
        const btn = e.currentTarget;
        if (!await confirmDialog({ message: `Remove the playlist ${src.name}? Channels you added from it stay, without its guide.`, okLabel: 'Remove playlist', danger: true, title: 'Remove playlist' })) return;
        run(btn, async () => saved(await api('/api/admin/tv/sources', { method: 'PUT', body: lineup.sources.filter((s) => s.id !== src.id) })), `Removed ${src.name}`);
      },
    })));
    sources.hidden = !lineup.sources.length;
    noSources.hidden = !!lineup.sources.length;
    guideNote.textContent = lineup.guideError ? `Couldn’t read a guide: ${lineup.guideError}` : '';
    guideNote.hidden = !lineup.guideError;
  };
  const addSource = h('button', { class: 'btn', type: 'button', onclick: () => sourceDialog(lineup.sources, null, (l, added) => {
    lineup = l; drawSources();
    if (added) browseDialog(added, new Set(lineup.channels.filter((c) => c.stream).map((c) => c.stream.url)), (l2) => { lineup = l2; draw(); drawSources(); });
  }) }, h('span', { html: icons.plus }), 'Add playlist');
  draw();
  drawSources();
  drawRemoved();
  const [srcTitle, srcHelp] = heading('Playlists', 'IPTV playlists (M3U) to pick live channels from, with their guides (XMLTV). Live channels play through the server, which converts video the browser can’t play.');

  const [channelsTitle, channelsHelp] = heading('Channels', 'Channel numbers follow this order. Turning a channel off takes it off the guide; it keeps its number. Lex adds a channel for everything, one per library and one per common genre (remove the ones you don’t want; they can be restored); add your own to pick titles or filter by library, genre and type.');
  return h('div', { class: 'form' },
    h('div', { class: 'form-section' },
      heading('Live TV', 'Channels that play the library at random, like broadcast TV. Everyone tuned to a channel sees the same program, and each day’s schedule is fixed at midnight. Live TV saves no resume points or watched marks.'),
      enable),
    h('div', { class: 'form-section' },
      channelsTitle, channelsHelp,
      lineup.channels.length ? null : h('p', { class: 'muted' }, 'No channels yet: add a library, or a channel of your own.'),
      list, removed, h('div', { class: 'row wrap' }, add, addStream)),
    h('div', { class: 'form-section' }, srcTitle, srcHelp,
      noSources, guideNote, sources, h('div', null, addSource)));
}
