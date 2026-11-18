// Library settings: the library list, adding or editing a library, and the folder browser.

import { h, icons, modal, confirmDialog, timeAgo, run, staleBanner, KIND_LABELS, emptyState } from '../ui.js';
import { api } from '../api.js';
import { state, loadLibraries, refreshSoft, focusAfterRoute } from '../app.js';
import { field, select, configForm, upper } from './forms.js';
import { every } from './poll.js';

// Why a library shows less than its folders hold: files another library
// already has (nested folders), or nothing found at all.
function libraryNote(l) {
  const els = l.elsewhere || [];
  if (els.length) {
    const n = els.reduce((a, e) => a + e.files, 0);
    const where = els.map((e) => `“${e.library}”`).join(', ');
    return h('div', { class: `help${l.itemCount ? '' : ' warn'}` },
      `${n} file${n === 1 ? ' in these folders is' : 's in these folders are'} already in librar${els.length === 1 ? 'y' : 'ies'} ${where} and ${n === 1 ? 'isn’t' : 'aren’t'} shown here: a file can only be in one library.`);
  }
  return l.lastScan && !l.itemCount ? h('div', { class: 'help warn' }, 'No media found — check the folder and the type.') : null;
}

// The list shows each library's scan progress while the scanner works on it
// (from /api/admin/tasks) and reloads when a scan finishes. The scanning
// settings sit below it.
export async function librariesSection(ctx) {
  const tasks = await api('/api/admin/tasks').catch(() => null);
  const libs = await api('/api/admin/libraries');
  const scanForm = await configForm(({ num, tg }) => [
    h('div', { class: 'form-section' }, h('h2', null, 'Library scanning'),
      h('div', { class: 'form-grid' },
        field('Rescan every (minutes)', num('scanIntervalMin', { min: 0 }), '0 disables periodic scans. Scans only stat files, so they are cheap.'),
        field('Probe workers', num('probeWorkers', { min: 1, max: 8 }), 'Parallel ffprobe processes for new files. 1 is best on a Pi.')),
      tg('scanOnStartup', 'Scan on startup')),
  ]);
  const box = h('div', { class: 'form' });
  box.appendChild(h('div', { class: 'row' }, h('div', { class: 'spacer' }),
    h('button', { class: 'btn', disabled: !libs.length, dataset: { focusKey: 'lib-scan-all' }, onclick: (e) => run(e.currentTarget, () => api('/api/admin/scan', { method: 'POST', body: {} }), 'Scanning all libraries') }, h('span', { html: icons.refresh }), 'Scan all'),
    h('button', { class: 'btn primary', dataset: { focusKey: 'lib-add' }, onclick: () => libraryModal() }, h('span', { html: icons.plus }), 'Add library')));
  if (!libs.length) {
    box.appendChild(emptyState({ level: 'h2', title: 'Add your first library', text: 'Point lex at the folder with your movies or TV shows, e.g. /media/movies.',
      actions: [h('button', { class: 'btn primary', onclick: () => libraryModal() }, 'Add library')] }));
  }
  const status = new Map();
  for (const l of libs) {
    const meta = h('span', { class: 'dim' }, `${KIND_LABELS[l.kind] || l.kind} · ${l.itemCount} item${l.itemCount === 1 ? '' : 's'} · scanned ${timeAgo(l.lastScan)}`);
    const live = h('span', { class: 'row small muted', hidden: true });
    status.set(l.name, { meta, live });
    box.appendChild(h('div', { class: 'form-section' },
      // The name keeps its line; the meta wraps below it on phones.
      h('div', { class: 'row wrap' }, h('h2', { style: { flex: '1 1 auto' } }, l.name), meta, live),
      libraryNote(l),
      h('div', { class: 'chips' }, l.paths.map((p) => h('span', { class: 'chip mono', style: { paddingRight: '12px' } }, p))),
      h('div', { class: 'row' },
        h('button', { class: 'btn sm', 'aria-label': `Scan ${l.name}`, dataset: { focusKey: `lib-scan-${l.id}` }, onclick: (e) => run(e.currentTarget, () => api('/api/admin/scan', { method: 'POST', body: { libraryId: l.id } }), `Scanning ${l.name}`) }, 'Scan'),
        h('button', { class: 'btn sm', 'aria-label': `Edit ${l.name}`, dataset: { focusKey: `lib-edit-${l.id}` }, onclick: () => libraryModal(l) }, 'Edit'),
        h('button', { class: 'btn sm danger', 'aria-label': `Remove ${l.name}`, onclick: async (e) => {
          const btn = e.currentTarget;
          if (!(await confirmDialog(`Remove library “${l.name}”? Files on disk are not touched; watch history for its items is removed.`, 'Remove', true, 'Remove library?'))) return;
          if (!(await run(btn, () => api(`/api/admin/libraries/${l.id}`, { method: 'DELETE' }), `Removed ${l.name}`))) return;
          focusAfterRoute('lib-add'); // the card goes away
          await loadLibraries();
          refreshSoft();
        } }, 'Remove'))));
  }
  box.appendChild(h('div', { class: 'help' }, 'Types: Movies (one movie per folder or file), TV Shows (Show/Season N/episodes), Mixed (auto-detects episodes by S01E01 or Season folders — good for anime folders with both series and films).'));
  box.appendChild(scanForm);
  const show = (s) => {
    for (const [name, { meta, live }] of status) {
      const on = !!s?.running && s.library === name;
      meta.hidden = on;
      live.hidden = !on;
      if (on) live.replaceChildren(h('div', { class: 'spinner sm' }), s.phase === 'probing' ? `Probing · ${s.probeDone}/${s.probeTotal}` : `${upper(s.phase || 'scanning')}…`);
    }
  };
  show(tasks?.scan);
  // A scan that finished since this render (or one seen running that has
  // stopped) changes counts and "scanned" times: re-render in place, once
  // the scanning settings have no unsaved edits.
  let seen = tasks?.scan || null;
  const banner = staleBanner(box);
  every(ctx, 2000, async () => {
    const { scan: s } = await api('/api/admin/tasks');
    if (!ctx.isCurrent()) return;
    if (seen && !s.running && (seen.running || s.finishedAt !== seen.finishedAt)) { if (!scanForm.dirty()) refreshSoft(); return; }
    seen = s;
    show(s);
  }, banner.hooks());
  return h('div', null, banner.el, box);
}

const within = (p, base) => p === base || base === '/' || p.startsWith(`${base}/`);

let libModalId = 0;

function libraryModal(lib) {
  const uid = ++libModalId;
  const err = h('div', { class: 'err', role: 'alert', id: `lib-err-${uid}` });
  const name = h('input', { type: 'text', value: lib?.name || '', 'aria-describedby': err.id, oninput: () => name.removeAttribute('aria-invalid') });
  let kind = lib?.kind || 'movies';
  const paths = [...(lib?.paths || [])];
  let autoName = null;
  const live = h('div', { class: 'sr-only', 'aria-live': 'polite' });
  const folderNote = h('div', { class: 'help warn', role: 'status' });
  const chips = h('div', { class: 'chips' });
  const renderChips = () => chips.replaceChildren(...(paths.length
    ? paths.map((p, i) => h('span', { class: 'chip mono' }, p, h('button', { type: 'button', 'aria-label': `Remove folder ${p}`, title: 'Remove folder', html: icons.close, onclick: () => removeAt(i) })))
    : [h('span', { class: 'dim small' }, 'No folders added yet')]));
  const removeAt = (i) => {
    const [p] = paths.splice(i, 1);
    renderChips();
    folderNote.textContent = '';
    live.textContent = `Removed ${p}`;
    const btns = chips.querySelectorAll('button');
    (btns[i] || btns[i - 1] || browser.addBtn).focus();
  };
  const onPick = async (p) => {
    folderNote.textContent = '';
    err.textContent = '';
    if (paths.includes(p)) { folderNote.textContent = `${p} is already added.`; return; }
    const clash = paths.find((q) => within(p, q) || within(q, p));
    if (clash) {
      folderNote.textContent = within(p, clash) ? `${p} is inside ${clash}, which is already added.` : `${p} contains ${clash}, which is already added. Remove it first to use the larger folder.`;
      return;
    }
    if (p === '/' && !(await confirmDialog('Use the whole filesystem as a library folder?', 'Use /', false, 'Add / as a folder?'))) return;
    paths.push(p);
    renderChips();
    live.textContent = `Added ${p}`;
    // A new library is usually named after its folder (renamed again if that
    // folder is swapped for another, unless the name was typed).
    if (!lib && paths.length === 1 && p !== '/' && (!name.value.trim() || name.value === autoName)) {
      name.value = autoName = upper(p.split('/').pop());
      name.removeAttribute('aria-invalid');
    }
  };
  renderChips();
  const parent = (p) => p?.replace(/\/[^/]+$/, '');
  const browser = folderBrowser(onPick, [parent(lib?.paths?.[0]), parent(state.libraries[0]?.paths?.[0]), '/media', '/mnt']);
  const retypeNote = lib ? h('span', null, ` Changing the type rebuilds the library: watch progress, watched status and favorites for its ${lib.itemCount} items are reset for everyone.`) : null;
  const typeHelp = h('div', { class: 'help' }, 'Movies: one film per folder or file. TV Shows: Show/Season/episodes. Mixed: both, detected per folder.', retypeNote);
  const typeSel = select(kind, Object.entries(KIND_LABELS), (v) => { kind = v; retypeNote?.classList.toggle('warn', v !== lib.kind); });
  const submit = async (e) => {
    const btn = e.currentTarget;
    if (btn.disabled) return;
    err.textContent = '';
    name.removeAttribute('aria-invalid');
    const nm = name.value.trim();
    if (!nm) { err.textContent = 'Enter a name'; name.setAttribute('aria-invalid', 'true'); name.focus(); return; }
    if (!paths.length) {
      err.textContent = browser.current() ? 'Press “Add this folder” to use the folder you’re viewing' : 'Choose a folder';
      (browser.current() ? browser.addBtn : browser.input).focus();
      return;
    }
    if (lib && (kind !== lib.kind || lib.paths.some((p) => !paths.includes(p)))) {
      const retype = kind !== lib.kind;
      const msg = retype
        ? `Changing “${lib.name}” to ${KIND_LABELS[kind]} rebuilds it. Watch progress, watched status and favorites for its ${lib.itemCount} items are reset for everyone.`
        : 'Titles in the removed folders, and everyone’s watch history for them, are removed from the library.';
      if (!(await confirmDialog(msg, 'Save changes', true, retype ? 'Change library type?' : 'Remove folders?'))) return;
    }
    let failed = null;
    const ok = await run(btn, async () => {
      try {
        const body = { name: nm, kind, paths };
        if (lib) await api(`/api/admin/libraries/${lib.id}`, { method: 'PUT', body });
        else await api('/api/admin/libraries', { method: 'POST', body });
      } catch (ex) {
        // Shown in the dialog rather than as a toast; AbortError keeps run() quiet.
        failed = ex;
        throw Object.assign(new Error(ex.message), { name: 'AbortError' });
      }
      m.close();
    }, lib ? 'Library updated; rescanning' : 'Library added; scanning now', { busyLabel: lib ? 'Saving…' : 'Adding…' });
    if (!ok) { if (failed) err.textContent = upper(failed.message); return; }
    await loadLibraries();
    refreshSoft();
  };
  const m = modal({ title: lib ? `Edit ${lib.name}` : 'Add library', wide: true, dismissible: false, body: [
    field('Name', name),
    field('Type', typeSel, typeHelp),
    h('div', { class: 'field', role: 'group', 'aria-label': 'Folders' }, h('span', null, 'Folders'), chips, folderNote),
    browser.el,
    live,
    err,
  ], actions: [
    h('button', { class: 'btn', type: 'button', onclick: () => m.close() }, 'Cancel'),
    h('button', { class: 'btn primary', type: 'button', onclick: submit }, lib ? 'Save' : 'Add library')] });
}

// folderBrowser lists server folders for the library editor. It opens at the
// first of starts that exists (falling back to /). current() is the folder
// last listed successfully; "Add this folder" adds that one, not whatever is
// typed in the box.
function folderBrowser(onPick, starts) {
  const input = h('input', { type: 'text', 'aria-label': 'Folder path', autocapitalize: 'off', autocorrect: 'off', spellcheck: 'false', style: { flex: '1 1 200px', minWidth: 0 } });
  const list = h('div', { class: 'fs-list' });
  let current = null;
  const addBtn = h('button', { class: 'btn sm primary', type: 'button', disabled: true, onclick: () => current && onPick(current) }, h('span', { html: icons.plus }), 'Add this folder');
  const row = (icon, label, onclick) => h('button', { type: 'button', onclick }, h('span', { html: icons[icon] }), label);
  // Listings can be slow (a sleeping disk): only the latest request counts.
  // A superseded one resolves null.
  let seq = 0;
  const go = async (p, { quiet = false } = {}) => {
    const fromList = list.contains(document.activeElement);
    const mine = ++seq;
    try {
      const r = await api(`/api/admin/fs?path=${encodeURIComponent(p)}`);
      if (mine !== seq) return null;
      current = r.path;
      input.value = r.path;
      addBtn.disabled = false;
      list.replaceChildren(...[
        r.path !== '/' ? row('up', '..', () => go(r.parent)) : null,
        ...r.dirs.map((d) => row('folder', d, () => go(`${r.path === '/' ? '' : r.path}/${d}`))),
        r.dirs.length ? null : h('div', { class: 'dim small', style: { padding: '8px' } }, 'No sub-folders'),
      ].filter(Boolean));
      if (fromList) list.querySelector('button')?.focus();
      return true;
    } catch (e) {
      if (mine !== seq) return null;
      if (quiet) return false;
      // Keep listing the last good folder, with the error above it.
      input.value = current ?? '';
      list.querySelector('.fs-err')?.remove();
      const msg = h('div', { class: 'fs-err small', role: 'alert' }, e.message);
      if (current) list.prepend(msg);
      else list.replaceChildren(msg, row('up', 'Open /', () => go('/')));
      if (fromList) list.querySelector('button')?.focus();
      return false;
    }
  };
  (async () => {
    // Stops as soon as one lists, or the user opens a folder first.
    for (const p of [...new Set(starts.filter(Boolean))]) if ((await go(p, { quiet: true })) !== false) return;
    await go('/');
  })();
  const el = h('div', { style: { display: 'grid', gap: '8px' } },
    h('form', { class: 'row wrap', onsubmit: (e) => { e.preventDefault(); go(input.value.trim() || '/'); } }, input, h('button', { class: 'btn sm', type: 'submit' }, 'Go'), addBtn),
    list);
  return { el, addBtn, input, current: () => current };
}
