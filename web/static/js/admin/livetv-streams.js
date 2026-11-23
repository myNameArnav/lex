// Settings › Live TV, channels from the internet (IPTV): a stream's dialog
// (with a test), a playlist's dialog, and picking channels from a playlist.

import { h, icons, run, modal, confirmDialog } from '../ui.js';
import { api } from '../api.js';
import { field, formErrors, submitInline } from './forms.js';

/** @typedef {import('../../../types/api.gen').tvLineup} Lineup */
/** @typedef {import('../../../types/api.gen').TVStream} TVStream */
/** @typedef {import('../../../types/api.gen').TVSource} TVSource */

let uid = 0;
const host = (u) => { try { return new URL(u).host; } catch { return u; } };
const webURL = (u) => /^https?:\/\/[^/\s]+/i.test(u.trim());

// codecLine describes a probed stream, e.g. "1920×1080 H.264, AAC".
function codecLine(p) {
  const name = (c) => ({ h264: 'H.264', hevc: 'HEVC', mpeg2video: 'MPEG-2', aac: 'AAC', mp2: 'MP2', ac3: 'AC-3', eac3: 'E-AC-3', mp3: 'MP3' })[c] || c.toUpperCase();
  return [p.width && p.height ? `${p.width}×${p.height}` : '', p.video ? name(p.video) : 'no video', p.audio ? name(p.audio) : 'no audio'].filter(Boolean).join(' · ');
}

// headersEditor edits d.headers: a name and a value per row, rows added
// and removed; onChange runs after every edit.
function headersEditor(d, help, onChange = () => {}) {
  d.headers ||= [];
  const id = `headers-${++uid}`;
  const rows = h('div', { class: 'tv-headers' });
  const draw = (focus) => {
    rows.replaceChildren(...d.headers.map((hd, i) => h('div', { class: 'tv-header-row' },
      h('input', { type: 'text', value: hd.name, placeholder: 'Name, e.g. Cookie', 'aria-label': `Header ${i + 1} name`, autocapitalize: 'off', autocorrect: 'off', spellcheck: 'false',
        oninput: (e) => { hd.name = e.target.value; onChange(); } }),
      h('input', { type: 'text', value: hd.value, placeholder: 'Value', 'aria-label': `Header ${i + 1} value`, autocapitalize: 'off', autocorrect: 'off', spellcheck: 'false',
        oninput: (e) => { hd.value = e.target.value; onChange(); } }),
      h('button', { type: 'button', class: 'btn icon ghost sm', 'aria-label': `Remove header ${hd.name || i + 1}`, title: 'Remove', html: icons.close, onclick: () => {
        d.headers.splice(i, 1); draw(Math.min(i, d.headers.length - 1)); onChange();
      } }))));
    if (focus != null) (/** @type {HTMLElement | null} */ (rows.children[focus]?.querySelector('input')) || addBtn).focus();
  };
  const addBtn = h('button', { type: 'button', class: 'btn sm', disabled: false, onclick: () => {
    if (d.headers.length >= 20) return;
    d.headers.push({ name: '', value: '' }); draw(d.headers.length - 1); onChange();
  } }, h('span', { html: icons.plus }), 'Add header');
  draw();
  return h('div', { class: 'field', role: 'group', 'aria-labelledby': id }, h('span', { id }, 'Other headers'), h('div', { class: 'help' }, help), rows, h('div', null, addBtn));
}

// streamDialog adds a live channel (st null) or edits one: its address and
// the headers its provider wants, with a test that opens it.
/**
 * @param {TVStream | null} st
 * @param {{ onSaved: (l: Lineup, id: string) => void, onRemoved: (l: Lineup) => void }} cb
 */
export function streamDialog(st, { onSaved, onRemoved }) {
  const d = structuredClone(st || { id: '', name: '', url: '', userAgent: '', referrer: '', logo: '', guideId: '', source: '', headers: [] });
  const errors = formErrors(`stream-err-${++uid}`, []);
  const input = (k, attrs = {}) => h('input', { type: 'text', value: d[k], autocapitalize: 'off', autocorrect: 'off', spellcheck: 'false', ...attrs, oninput: (e) => { d[k] = e.target.value; result.replaceChildren(); } });
  const name = input('name', { maxlength: 60, placeholder: 'e.g. News 24', autocapitalize: 'sentences' });
  const url = input('url', { inputmode: 'url', placeholder: 'https://…/master.m3u8' });
  const result = h('div', { class: 'tv-probe', role: 'status' });
  const test = h('button', { class: 'btn', type: 'button', onclick: () => {
    if (!webURL(d.url)) { errors.fail('Enter the stream’s http(s) address', url); return; }
    errors.reset();
    result.replaceChildren(h('span', { class: 'muted' }, 'Opening the stream…'));
    run(test, async () => {
      try {
        const p = await api('/api/admin/tv/streams/probe', { method: 'POST', body: d });
        result.replaceChildren(h('span', null, `It plays: ${codecLine(p)}`),
          p.convert ? h('span', { class: 'warn' }, ' · the server converts this video while it plays, which takes CPU') : null);
      } catch (e) { result.replaceChildren(h('span', { class: 'bad-ink' }, e.message)); }
    }, null, { busyLabel: 'Testing…' });
  } }, 'Test stream');
  const form = h('form', { class: 'form tv-channel-form', novalidate: true, onsubmit: (e) => { e.preventDefault(); submit(); } },
    field('Channel name', name),
    field('Stream address', url, 'An HLS playlist (.m3u8) or an MPEG-TS stream. The server fetches it, so the address never reaches the browser.'),
    h('div', { class: 'form-grid' },
      field('User-Agent', input('userAgent', { placeholder: 'Only if the provider wants one' }), 'Sent with every request for the stream, its segments and its key. Some providers only answer their own app’s.'),
      field('Referer', input('referrer', { placeholder: 'Optional' }))),
    headersEditor(d, 'Any other headers the provider wants with every request, such as a Cookie, Origin or token.', () => result.replaceChildren()),
    h('div', { class: 'form-grid' },
      field('Logo address', input('logo', { inputmode: 'url', placeholder: 'Optional' })),
      field('Guide ID', input('guideId', { placeholder: 'tvg-id, optional' }), 'The channel’s id in a playlist’s guide (XMLTV), for what’s on.')),
    h('div', { class: 'row wrap' }, test, result));
  const saveBtn = h('button', { class: 'btn primary', type: 'button', onclick: () => submit() }, st ? 'Save' : 'Add channel');
  const submit = () => {
    errors.reset();
    d.headers = d.headers.filter((x) => x.name.trim());
    if (!d.name.trim()) { errors.fail('Give the channel a name', name); return; }
    if (!webURL(d.url)) { errors.fail('Enter the stream’s http(s) address', url); return; }
    submitInline(saveBtn, errors, async () => {
      const l = st ? await api(`/api/admin/tv/streams/${encodeURIComponent(st.id)}`, { method: 'PUT', body: d })
        : await api('/api/admin/tv/streams', { method: 'POST', body: [d] });
      m.close();
      onSaved(l, st ? `stream-${st.id}` : l.saved);
    }, st ? `Saved ${d.name.trim()}` : `Added ${d.name.trim()}`, (msg) => (/name/i.test(msg) ? [name] : /address/i.test(msg) ? [url] : []));
  };
  const removeBtn = st ? h('button', { class: 'btn danger', type: 'button', onclick: async () => {
    if (!await confirmDialog({ message: `Remove ${st.name} from Live TV? The channels after it move up a number.`, okLabel: 'Remove channel', danger: true, title: 'Remove channel?' })) return;
    run(removeBtn, async () => { const l = await api(`/api/admin/tv/streams/${encodeURIComponent(st.id)}`, { method: 'DELETE' }); m.close(); onRemoved(l); }, `Removed ${st.name}`);
  } }, h('span', { html: icons.trash }), 'Remove') : null;
  const m = modal({ title: st ? `Edit ${st.name}` : 'New live channel', wide: true, dismissible: false, body: form,
    actions: [removeBtn, h('div', { class: 'spacer' }), errors.err, h('button', { class: 'btn', type: 'button', onclick: () => m.close() }, 'Cancel'), saveBtn] });
  requestAnimationFrame(() => (st ? name : name).focus());
}

// sourceDialog adds a playlist (src null) or edits one; sources is the
// list saved now, replaced as a whole.
/**
 * @param {TVSource[]} sources
 * @param {TVSource | null} src
 * @param {(l: Lineup, added?: TVSource | null) => void} onSaved
 */
export function sourceDialog(sources, src, onSaved) {
  const d = structuredClone(src || { id: '', name: '', url: '', guideUrl: '', userAgent: '', headers: [], playlistFile: '', guideFile: '' });
  const errors = formErrors(`source-err-${++uid}`, []);
  const input = (k, attrs = {}) => h('input', { type: 'text', value: d[k], autocapitalize: 'off', autocorrect: 'off', spellcheck: 'false', ...attrs, oninput: (e) => { d[k] = e.target.value; } });
  const name = input('name', { maxlength: 60, placeholder: 'e.g. Free channels', autocapitalize: 'sentences' });
  const url = input('url', { inputmode: 'url', placeholder: 'https://…/playlist.m3u, or a channel API address' });
  const guide = input('guideUrl', { inputmode: 'url', placeholder: 'Optional: https://…/guide.xml(.gz)' });
  // Each of the playlist and the guide is an address or an uploaded file.
  const pick = (kind, input, fileKey, urlKey, accept, label) => {
    const status = h('span', { class: 'muted small', role: 'status' });
    const fileIn = h('input', { type: 'file', accept, hidden: true, onchange: async () => {
      const f = fileIn.files?.[0];
      fileIn.value = '';
      if (!f) return;
      errors.reset();
      status.textContent = `Uploading ${f.name}…`;
      await run(upBtn, async () => {
        const res = await fetch(`/api/admin/tv/files?kind=${kind}`, { method: 'POST', body: f, credentials: 'same-origin', headers: { 'Content-Type': 'application/octet-stream' } });
        const r = await res.json().catch(() => ({}));
        if (!res.ok) { status.textContent = ''; throw new Error(r.error || `Upload failed (${res.status})`); }
        d[fileKey] = r.file; d[urlKey] = '';
        if (!d.name.trim()) { d.name = f.name.replace(/\.[^.]+$/, '').replaceAll('_', ' '); name.value = d.name; }
        show(`${f.name}${r.channels != null ? ` · ${r.channels.toLocaleString()} channel${r.channels === 1 ? '' : 's'}` : ''}`);
      }, null, { busyLabel: 'Uploading…' });
    } });
    const upBtn = h('button', { type: 'button', class: 'btn sm', onclick: () => fileIn.click() }, 'Upload file');
    const clearBtn = h('button', { type: 'button', class: 'btn sm ghost', onclick: () => { d[fileKey] = ''; show(); input.focus(); } }, 'Use an address');
    const show = (text) => {
      const file = !!d[fileKey];
      input.hidden = file; clearBtn.hidden = !file;
      status.textContent = file ? text || 'Uploaded file' : '';
    };
    show();
    return h('div', { class: 'tv-src-pick' }, input, h('div', { class: 'row wrap' }, upBtn, clearBtn, status), fileIn);
  };
  const playlistPick = pick('playlist', url, 'playlistFile', 'url', '.m3u,.m3u8,.txt,audio/x-mpegurl,application/vnd.apple.mpegurl', 'playlist');
  const guidePick = pick('guide', guide, 'guideFile', 'guideUrl', '.xml,.xmltv,.gz,text/xml,application/xml,application/gzip', 'guide');
  const saveBtn = h('button', { class: 'btn primary', type: 'button', onclick: () => submit() }, src ? 'Save' : 'Add playlist');
  const submit = () => {
    errors.reset();
    d.headers = d.headers.filter((x) => x.name.trim());
    if (!d.playlistFile && !webURL(d.url)) { errors.fail('Enter the playlist’s http(s) address, or upload the file', url); return; }
    if (!d.guideFile && d.guideUrl.trim() && !webURL(d.guideUrl)) { errors.fail('Enter the guide’s http(s) address, or leave it empty', guide); return; }
    const next = src ? sources.map((s) => (s.id === src.id ? d : s)) : [...sources, d];
    submitInline(saveBtn, errors, async () => {
      const l = await api('/api/admin/tv/sources', { method: 'PUT', body: next });
      m.close();
      // A new playlist: go straight to picking its channels.
      onSaved(l, src ? null : l.sources.at(-1));
    }, src ? 'Saved the playlist' : 'Added the playlist', (msg) => (/playlist/i.test(msg) ? [url] : /guide/i.test(msg) ? [guide] : []));
  };
  const form = h('form', { class: 'form tv-channel-form', novalidate: true, onsubmit: (e) => { e.preventDefault(); submit(); } },
    field('Name', name),
    h('div', { class: 'field' }, h('span', null, 'Playlist'), h('div', { class: 'help' }, 'A list of channels (M3U), by its address or as a file. Or your provider’s channel API (JSON, as OnnetPlayer apps read), which brings its own guide: enter its address, and the User-Agent and headers its app sends below. You then pick which of its channels to add.'), playlistPick),
    h('div', { class: 'field' }, h('span', null, 'Guide ', h('span', { class: 'muted' }, '(optional)')), h('div', { class: 'help' }, 'What’s on (XMLTV, may be gzipped), matched to channels by their tvg-id. Not needed for a channel API. Without one, channels show as Live.'), guidePick),
    field('User-Agent', input('userAgent', { placeholder: 'Optional' }), 'Sent when reading the playlist and guide, and for its channels unless the playlist names one.'),
    headersEditor(d, 'Sent when reading the playlist and guide, and with its channels unless the playlist gives them their own. For a channel API, the app’s own data, e.g. Content-APP-DATA: {"groupId":1,…}, which is kept on the server and sent only to the API.'));
  const m = modal({ title: src ? `Edit ${src.name}` : 'New playlist', wide: true, dismissible: false, body: form,
    actions: [h('div', { class: 'spacer' }), errors.err, h('button', { class: 'btn', type: 'button', onclick: () => m.close() }, 'Cancel'), saveBtn] });
  requestAnimationFrame(() => name.focus());
}

// browseDialog picks channels from a playlist: search by name, narrow by
// group, tick and add. have holds the stream addresses already added.
/**
 * @param {TVSource} src
 * @param {Set<string>} have
 * @param {(l: Lineup) => void} onAdded
 */
export function browseDialog(src, have, onAdded) {
  const picked = new Map(); // url -> entry
  const list = h('ul', { class: 'tv-browse', 'aria-label': 'Channels in the playlist' });
  const status = h('p', { class: 'muted small', role: 'status', style: { margin: 0 } }, 'Reading the playlist…');
  const groupSel = h('select', { 'aria-label': 'Group', onchange: () => load() }, h('option', { value: '' }, 'All groups'));
  let timer = 0, seq = 0, groupsShown = false;
  const q = h('input', { type: 'search', placeholder: 'Search channels', 'aria-label': 'Search channels', autocomplete: 'off', oninput: () => { clearTimeout(timer); timer = setTimeout(load, 250); } });
  const addBtn = h('button', { class: 'btn primary', type: 'button', disabled: true, onclick: () => {
    run(addBtn, async () => {
      const body = [...picked.values()].map((e) => ({ id: '', name: e.name, url: e.url, logo: e.logo, guideId: e.guideId, userAgent: e.userAgent, referrer: e.referrer, headers: e.headers || [], source: src.id }));
      const l = await api('/api/admin/tv/streams', { method: 'POST', body });
      m.close();
      onAdded(l);
    }, `Added ${picked.size} channel${picked.size === 1 ? '' : 's'}`);
  } }, 'Add channels');
  const count = () => { addBtn.disabled = !picked.size; addBtn.textContent = picked.size ? `Add ${picked.size.toLocaleString()} channel${picked.size === 1 ? '' : 's'}` : 'Add channels'; };
  // Every channel matching the search and group, not only those shown.
  let matched = 0;
  const allBtn = h('button', { type: 'button', class: 'btn sm', hidden: true, onclick: () => run(allBtn, async () => {
    const r = await api(`/api/admin/tv/sources/${encodeURIComponent(src.id)}/channels?all=1&q=${encodeURIComponent(q.value.trim())}&group=${encodeURIComponent(groupSel.value)}`);
    for (const e of r.channels) if (!have.has(e.url)) picked.set(e.url, e);
    list.querySelectorAll('input[type=checkbox]:not(:disabled)').forEach((b) => { /** @type {HTMLInputElement} */ (b).checked = true; });
    count();
  }) }, 'Select all');
  const noneBtn = h('button', { type: 'button', class: 'btn sm ghost', onclick: () => {
    picked.clear();
    list.querySelectorAll('input[type=checkbox]:not(:disabled)').forEach((b) => { /** @type {HTMLInputElement} */ (b).checked = false; });
    count();
  } }, 'Clear');
  async function load() {
    const mine = ++seq;
    let r;
    try { r = await api(`/api/admin/tv/sources/${encodeURIComponent(src.id)}/channels?q=${encodeURIComponent(q.value.trim())}&group=${encodeURIComponent(groupSel.value)}`); } catch (e) {
      if (mine === seq) status.replaceChildren(h('span', { class: 'bad-ink' }, e.message));
      return;
    }
    if (mine !== seq || !list.isConnected) return;
    if (!groupsShown) { groupsShown = true; groupSel.append(...r.groups.map((g) => h('option', { value: g }, g))); }
    matched = r.matched;
    allBtn.hidden = !matched;
    allBtn.textContent = `Select all ${matched.toLocaleString()}`;
    status.textContent = r.matched > r.channels.length ? `Showing ${r.channels.length} of ${r.matched.toLocaleString()} matching (${r.total.toLocaleString()} in the playlist): search to narrow them down.`
      : `${r.matched.toLocaleString()} matching, ${r.total.toLocaleString()} in the playlist.`;
    list.replaceChildren(...r.channels.map((e) => {
      const added = have.has(e.url);
      return h('li', null, h('label', { class: added ? 'added' : '' },
        h('input', { type: 'checkbox', disabled: added, checked: added || picked.has(e.url), onchange: (ev) => { if (ev.target.checked) picked.set(e.url, e); else picked.delete(e.url); count(); } }),
        e.logo ? h('img', { src: e.logo, alt: '', loading: 'lazy', referrerpolicy: 'no-referrer', onerror: (ev) => ev.target.remove() }) : h('span', { class: 'tv-browse-nologo' }),
        h('span', { class: 'tv-browse-name' }, e.name, h('small', null, [e.group.replaceAll(';', ', '), added ? 'added' : ''].filter(Boolean).join(' · ')))));
    }));
  }
  const m = modal({ title: `Add channels from ${src.name}`, wide: true,
    body: h('div', { class: 'tv-browse-wrap' }, h('div', { class: 'row wrap' }, q, groupSel), h('div', { class: 'row wrap' }, status, h('div', { class: 'spacer' }), allBtn, noneBtn), list),
    actions: [h('div', { class: 'spacer' }), h('button', { class: 'btn', type: 'button', onclick: () => m.close() }, 'Cancel'), addBtn] });
  q.focus();
  load();
}

// sourceRow is a playlist in the settings list.
export function sourceRow(src, { browse, edit, remove }) {
  return h('li', { class: 'tv-row tv-src' },
    h('span', { class: 'tv-row-num', 'aria-hidden': 'true', html: icons.broadcast }),
    h('div', { class: 'tv-row-main' }, h('div', { class: 'tv-row-name' }, src.name),
      h('div', { class: 'tv-row-sub' }, [src.playlistFile ? 'uploaded playlist' : host(src.url), src.guideFile ? 'uploaded guide' : src.guideUrl ? `guide from ${host(src.guideUrl)}` : 'no guide file'].join(' · '))),
    h('div', { class: 'tv-row-actions' },
      h('button', { type: 'button', class: 'btn sm primary', 'aria-label': `Add channels from ${src.name}`, dataset: { focusKey: `browse-${src.id}` }, onclick: browse }, 'Add channels'),
      h('button', { type: 'button', class: 'btn sm', 'aria-label': `Edit ${src.name}`, dataset: { focusKey: `src-edit-${src.id}` }, onclick: edit }, 'Edit'),
      h('button', { type: 'button', class: 'btn icon ghost sm danger', 'aria-label': `Remove ${src.name}`, title: 'Remove', html: icons.trash, onclick: remove })));
}

export { host };
