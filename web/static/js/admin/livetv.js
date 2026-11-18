// Live TV settings: automatic channels, and editing your own channels.

import { h, icons } from '../ui.js';
import { api } from '../api.js';
import { state } from '../app.js';
import { heading, field, select, configForm } from './forms.js';

// titlePicker edits a list of movies and shows (item ids): chips for the
// picked ones, and a search box that adds more. get() reads the list and
// set(ids) replaces it; the picker tells the form itself.
export const titleInfo = new Map(); // id -> item, from searches and lookups

export let pickerId = 0;

export function titlePicker(label, help, get, set) {
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
    // Enter adds the first result rather than saving the form.
    onkeydown: (e) => {
      if (e.key === 'Enter') { e.preventDefault(); const first = found.find((t) => !get().includes(t.id)); if (first) pick(first); }
      if (e.key === 'Escape' && input.value) { e.preventDefault(); input.value = ''; search(''); }
    } });
  drawChips();
  el.append(h('span', { id }, label), h('div', { class: 'help', id: `${id}-h` }, help), chips, input, results);
  return el;
}

// tvChannelsEditor edits draft.tvChannels, the admin's own Live TV channels.
// Every edit replaces the array (and the edited channel), so the form sees
// the draft change; adding or removing one also tells the form.
export let checksId = 0;

export function tvChannelsEditor(draft, genres) {
  const wrap = h('div', { class: 'tv-channels' });
  const changed = () => wrap.dispatchEvent(new Event('input', { bubbles: true }));
  const edit = (i, patch) => { draft.tvChannels = draft.tvChannels.map((c, j) => (j === i ? { ...c, ...patch } : c)); };
  // A set of checkboxes for list k of channel i; none ticked means no limit.
  const checks = (label, none, i, k, options) => {
    const id = `checks-${++checksId}`;
    return h('div', { class: 'field' }, h('span', { id }, label, ' ', h('span', { class: 'muted' }, none)),
      h('div', { class: 'check-chips', role: 'group', 'aria-labelledby': id },
        options.map(([v, l]) => h('label', null, h('input', { type: 'checkbox', checked: draft.tvChannels[i][k].includes(v),
          onchange: (e) => { const cur = draft.tvChannels[i][k].filter((x) => x !== v); edit(i, { [k]: e.target.checked ? [...cur, v] : cur }); } }), l))));
  };
  // titlePicker's get and set for list k of channel i.
  const list = (i, k) => [() => draft.tvChannels[i][k] || [], (v) => edit(i, { [k]: v })];
  const render = (focus) => {
    wrap.replaceChildren(...(draft.tvChannels || []).map((ch, i) => {
      const name = h('input', { type: 'text', value: ch.name, maxlength: 60, required: true, oninput: (e) => edit(i, { name: e.target.value }) });
      // Genres a channel keeps but the library no longer has stay listed.
      const gs = [...new Set([...genres, ...ch.genres])];
      if (focus === ch.id) requestAnimationFrame(() => { name.focus(); name.select(); });
      // A channel either picks its titles or filters the library (and may
      // leave some titles out); switching keeps the other choices for later.
      const picked = titlePicker('Titles', 'The channel plays only these movies and shows (every episode of a show).', ...list(i, 'titles'));
      const filters = h('div', { class: 'tv-filters' },
        state.libraries.length > 1 ? checks('Libraries', '(none ticked: all)', i, 'libraries', state.libraries.map((l) => [l.id, l.name])) : null,
        gs.length ? checks('Genres', '(none ticked: any)', i, 'genres', gs.map((g) => [g, g])) : null,
        titlePicker('Leave out', 'Movies and shows this channel never plays, even when they match.', ...list(i, 'exclude')));
      const show = (kind) => { picked.hidden = kind !== 'picked'; filters.hidden = kind === 'picked'; };
      show(ch.kind);
      return h('div', { class: 'tv-channel-edit' },
        h('div', { class: 'form-grid' },
          field('Channel name', name),
          field('Plays', select(ch.kind, [['', 'Movies and shows'], ['movie', 'Movies only'], ['show', 'Shows only'], ['picked', 'Only titles I pick']], (v) => { edit(i, { kind: v }); show(v); }))),
        picked, filters,
        h('div', null, h('button', { class: 'btn sm danger', type: 'button', onclick: () => {
          draft.tvChannels = draft.tvChannels.filter((_, j) => j !== i);
          render(); changed();
          (wrap.querySelector('.tv-channel-edit:last-child input') || add).focus();
        } }, h('span', { html: icons.trash }), 'Remove channel')));
    }));
  };
  const add = h('button', { class: 'btn sm', type: 'button', onclick: () => {
    const id = Math.random().toString(16).slice(2, 10);
    draft.tvChannels = [...(draft.tvChannels || []), { id, name: `Channel ${(draft.tvChannels?.length || 0) + 1}`, libraries: [], genres: [], kind: '', titles: [], exclude: [] }];
    render(id); changed();
  } }, h('span', { html: icons.plus }), 'Add channel');
  render();
  return h('div', { class: 'tv-channels-editor' },
    h('h3', null, 'Your channels'),
    h('p', { class: 'help', style: { margin: 0 } }, 'Channels of your own, listed before the automatic ones. Pick the titles a channel plays, or let it play what matches its choices.'),
    wrap, h('div', null, add));
}

// tvAutomaticEditor turns automatic Live TV channels on and off; ticked ones
// air. draft.tvHidden keeps the ids of the ones turned off, including any the
// library no longer fills, so they stay off if they come back.
export function tvAutomaticEditor(draft, auto) {
  if (!auto.length) return null;
  const id = `checks-${++checksId}`;
  return h('div', { class: 'field' }, h('span', { id }, 'Automatic channels'),
    h('div', { class: 'help' }, 'Untick a channel to take it off Live TV.'),
    h('div', { class: 'check-chips', role: 'group', 'aria-labelledby': id },
      auto.map((c) => h('label', null, h('input', { type: 'checkbox', checked: !(draft.tvHidden || []).includes(c.id),
        onchange: (e) => { const rest = (draft.tvHidden || []).filter((x) => x !== c.id); draft.tvHidden = e.target.checked ? rest : [...rest, c.id]; } }), c.name))));
}

export async function liveTvSection() {
  // Genres to offer custom Live TV channels, and the automatic channels.
  const [genres, auto] = await Promise.all([api('/api/genres').catch(() => []), api('/api/admin/tv/automatic').catch(() => [])]);
  const check = (d) => {
    const chs = d.tvChannels || [];
    const cards = document.querySelectorAll('.tv-channel-edit');
    let i = chs.findIndex((c) => !c.name.trim());
    if (i >= 0) return ['Give every Live TV channel a name', cards[i]?.querySelector('input[type=text]')];
    i = chs.findIndex((c) => c.kind === 'picked' && !c.titles?.length);
    if (i >= 0) return [`Pick at least one title for ${chs[i].name.trim()}`, cards[i]?.querySelector('input[type=search]')];
    return null;
  };
  return configForm(({ draft, tg }) => [
    h('div', { class: 'form-section' }, heading('Live TV', 'Channels that play the library at random, like broadcast TV: one for everything, one per library and one per common genre (turn off the ones you don’t want), plus any you make below. Everyone tuned to a channel sees the same program. Live TV saves no resume points or watched marks.'),
      tg('liveTv', 'Enable Live TV', 'Adds Live TV to the menu for every user.'),
      tvAutomaticEditor(draft, auto),
      tvChannelsEditor(draft, genres)),
  ], { check });
}
