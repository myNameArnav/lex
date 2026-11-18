// Server upkeep pages: SSD cache, backups, tasks & logs, and About.

import { h, clear, icons, toast, confirmDialog, fmtBytes, fmtDate, timeAgo, run, staleBanner } from '../ui.js';
import { api } from '../api.js';
import { refreshSoft } from '../app.js';
import { meter } from './dashboard.js';
import { heading, field, configForm, actionsTh, keepFocus } from './forms.js';
import { every } from './poll.js';

function taskRow(label, running, text) {
  return h('div', { class: 'task' }, running ? h('div', { class: 'spinner sm' }) : h('span', { class: 'good', html: icons.check, style: { width: '16px' } }), h('b', null, label), text);
}

export async function cacheSection(ctx) {
  const statusBox = h('div', { class: 'form-section' });
  const listBox = h('div', { class: 'form-section' });
  let listSig = null, nextKey = null;
  const render = async () => {
    const r = await api('/api/admin/cache');
    if (!ctx.isCurrent()) return;
    const st = r.status;
    const pct = st.maxBytes ? (st.usedBytes / st.maxBytes) * 100 : 0;
    clear(statusBox).append(h('h2', null, 'Status'),
      h('div', { class: 'row small' }, h('span', { class: 'grow' }, `${fmtBytes(st.usedBytes)} of ${fmtBytes(st.maxBytes)} used · ${st.files} file${st.files === 1 ? '' : 's'}`), h('span', { class: 'muted' }, `disk free ${fmtBytes(st.diskFree)} of ${fmtBytes(st.diskTotal)}`)),
      meter(pct, 97),
      h('dl', { class: 'kv' },
        h('dt', null, 'Folder'), h('dd', { class: 'mono' }, st.dir),
        h('dt', null, 'Reads'), h('dd', null, `${st.hits} from cache · ${st.misses} from library disk (since start)`),
        h('dt', null, 'Copying'), h('dd', null, st.current ? `${st.current.name} — ${Math.round((st.current.done / st.current.size) * 100)}% of ${fmtBytes(st.current.size)} at ${fmtBytes(st.current.speed)}/s (${st.current.reason})` : 'nothing'),
        h('dt', null, 'Queue'), h('dd', null, st.queued.length ? st.queued.map((j) => j.name).join(', ') : 'empty'),
        st.lastError ? [h('dt', null, 'Last error'), h('dd', { class: 'bad' }, st.lastError)] : null));
    // The list (and its buttons) is rebuilt only when it changes, or once a
    // minute for the "… ago" times.
    const sig = `${Math.floor(Date.now() / 60000)}|${r.entries.map((e) => `${e.itemId}:${e.lastAccess}`).join()}`;
    if (sig === listSig) return;
    listSig = sig;
    keepFocus(listBox, () => clear(listBox).append(h('div', { class: 'row' }, h('h2', { class: 'grow' }, 'Cached files'),
      r.entries.length ? h('button', { class: 'btn sm danger', dataset: { focusKey: 'cache-clear' }, onclick: async (e) => {
        const btn = e.currentTarget;
        if (await confirmDialog('Delete every cached copy? Originals are not touched.', 'Clear', true) && await run(btn, () => api('/api/admin/cache/clear', { method: 'POST', body: { kind: 'media' } }))) refresh();
      } }, 'Clear cache') : null),
    r.entries.length ? h('div', { class: 'table-wrap' }, h('table', { class: 'tbl' },
      h('tr', null, ['Title', 'Size', 'Cached', 'Last used', 'Reason', ''].map((t) => h('th', null, t))),
      r.entries.map((e) => h('tr', null, h('td', null, h('a', { href: `#/item/${e.itemId}`, dataset: { focusKey: `cache-item-${e.itemId}` } }, e.title), h('div', { class: 'dim small ellipsis', style: { maxWidth: '380px' } }, e.name)), h('td', { class: 'nowrap' }, fmtBytes(e.size)), h('td', { class: 'nowrap' }, timeAgo(e.addedAt)), h('td', { class: 'nowrap' }, timeAgo(e.lastAccess)), h('td', null, e.reason),
        h('td', null, h('button', { class: 'btn sm', dataset: { focusKey: `cache-remove-${e.itemId}` }, 'aria-label': `Remove ${e.title} from the cache`, onclick: async (ev) => {
          const i = r.entries.indexOf(e), next = r.entries[i + 1] || r.entries[i - 1];
          nextKey = next ? `cache-remove-${next.itemId}` : null;
          if (await run(ev.currentTarget, () => api(`/api/admin/cache/items/${e.itemId}`, { method: 'DELETE' }), `Removed ${e.title} from the cache`)) refresh();
        } }, 'Remove'))))))
      : h('p', { class: 'muted', style: { margin: 0 } }, 'Nothing cached yet. Files are copied when played (and upcoming episodes are prefetched).')), nextKey);
  };
  const refresh = () => render().catch(() => {});
  const form = await configForm(({ draft, num, text, tg }) => [
    h('div', { class: 'form-section' }, heading('SSD cache', 'Keeps copies of media on a fast disk (e.g. the SSD) so playback — and seeking — reads from it instead of the hard drive, which can then stay asleep. Files are copied in the background at a capped speed; the least recently watched are evicted first.'),
      tg('cacheEnabled', 'Enable SSD cache'),
      field('Cache folder', text('cacheDir', { placeholder: 'default: <data folder>/cache' }), 'A "lex-cache" folder is created inside it. Leave empty to use lex\'s data folder (on the SSD here).'),
      h('div', { class: 'form-grid' },
        field('Maximum size (GB)', num('cacheMaxGb', { min: 1 })),
        field('Keep free on disk (GB)', num('cacheMinFreeGb', { min: 0 })),
        field('Largest file to cache (GB)', num('cacheMaxFileGb', { min: 1 })),
        field('Copy speed limit (MB/s)', num('cacheSpeedMbs', { min: 0 }), '0 = unlimited. Keeps the hard drive responsive for playback.')),
      tg('cacheOnPlay', 'Cache what\'s being played', 'The rest of the file is read from the SSD after the copy finishes (after the next seek for remux/transcode).'),
      field('Prefetch upcoming episodes', h('select', { onchange: (e) => { draft.cachePrefetch = +e.target.value; } }, [0, 1, 2, 3, 5].map((v) => h('option', { value: v, selected: draft.cachePrefetch === v }, v ? `Next ${v}` : 'Off'))))),
  ]);
  await render();
  const page = h('div', { class: 'form' }, form, statusBox, listBox);
  const banner = staleBanner(page);
  every(ctx, 2000, render, banner.hooks());
  return h('div', null, banner.el, page);
}

const BACKUP_REASON = { daily: 'Daily', manual: 'Manual', 'pre-migration': 'Before upgrade', 'pre-restore': 'Before restore' };

export async function backupsSection() {
  const listBox = h('div', { class: 'form-section' });
  let nextKey = null;
  const render = async () => {
    const r = await api('/api/admin/backups');
    const now = h('button', { class: 'btn sm', type: 'button', dataset: { focusKey: 'backup-now' }, onclick: async (e) => {
      if (await run(e.currentTarget, () => api('/api/admin/backups', { method: 'POST' }), 'Database backed up', { busyLabel: 'Backing up…' })) refresh();
    } }, 'Back up now');
    const remove = async (e, b) => {
      if (!(await confirmDialog(`Delete the backup from ${fmtDate(b.created)}?`, 'Delete', true, 'Delete backup?'))) return;
      const i = r.backups.indexOf(b), next = r.backups[i + 1] || r.backups[i - 1];
      nextKey = next ? `backup-del-${next.name}` : 'backup-now';
      if (await run(e.currentTarget, () => api(`/api/admin/backups/${encodeURIComponent(b.name)}`, { method: 'DELETE' }), 'Backup deleted')) refresh();
    };
    keepFocus(listBox, () => clear(listBox).append(
      h('div', { class: 'row' }, h('h2', { class: 'grow' }, 'Saved backups'), now),
      h('p', { class: 'help', style: { margin: 0 } }, 'In ', h('span', { class: 'mono' }, r.dir), '. To restore one, stop lex and run ', h('code', { class: 'mono' }, 'lex -restore <file>'), ' with the same data folder (the current database is backed up first), or copy the file over lex.db in the data folder, delete lex.db-wal and lex.db-shm, and start lex.'),
      r.lastError ? h('p', { class: 'bad', role: 'alert', style: { margin: 0 } }, `The last daily backup failed: ${r.lastError}`) : null,
      r.backups.length ? h('div', { class: 'table-wrap' }, h('table', { class: 'tbl stack' },
        h('tr', null, h('th', null, 'Made'), h('th', null, 'Kind'), h('th', null, 'Size'), actionsTh()),
        r.backups.map((b) => h('tr', null,
          h('td', null, h('b', null, fmtDate(b.created)), h('div', { class: 'dim small' }, timeAgo(b.created))),
          h('td', { 'data-label': 'Kind' }, BACKUP_REASON[b.reason] || b.reason),
          h('td', { 'data-label': 'Size', class: 'nowrap' }, fmtBytes(b.size)),
          h('td', null, h('div', { class: 'row wrap tbl-actions' },
            h('a', { class: 'btn sm', href: `/api/admin/backups/${encodeURIComponent(b.name)}`, download: b.name, 'aria-label': `Download the backup from ${fmtDate(b.created)}` }, 'Download'),
            h('button', { class: 'btn sm danger', type: 'button', dataset: { focusKey: `backup-del-${b.name}` }, 'aria-label': `Delete the backup from ${fmtDate(b.created)}`, onclick: (e) => remove(e, b) }, 'Delete')))))))
        : h('p', { class: 'muted', style: { margin: 0 } }, 'No backups yet.')), nextKey);
    nextKey = null;
  };
  const refresh = () => render().catch((e) => toast(e?.message || String(e), 'error'));
  const form = await configForm(({ num, text, tg }) => [
    h('div', { class: 'form-section' }, heading('Database backups', 'Copies of the database: users, watch history, matches, collections and settings. lex also backs it up before every upgrade that changes it. Artwork, subtitles and previews aren’t included; they are rebuilt on demand.'),
      tg('backupDaily', 'Back up daily'),
      field('Backup folder', text('backupDir', { placeholder: 'default: <data folder>/backups', autocapitalize: 'off', spellcheck: 'false' }), 'An existing folder, ideally on another disk than the data folder. A "lex-backups" folder is created inside it.'),
      field('Backups to keep', num('backupKeep', { min: 1, max: 100 }), 'Of each kind (daily, manual, before upgrade, before restore); older ones are deleted.')),
  ], { onSaved: refresh });
  await render();
  return h('div', { class: 'form' }, form, listBox);
}

// Polled every few seconds. Only the status lines are replaced (Scan now is
// built once), and new log lines are appended, so focus and a text selection
// in the log survive the refresh.
export async function logsSection(ctx) {
  const status = h('div', { style: { display: 'grid', gap: '16px' } });
  const tasks = h('div', { class: 'form-section' }, h('h2', null, 'Background tasks'), status,
    h('div', { class: 'row' }, h('button', { class: 'btn sm', dataset: { focusKey: 'scan-now' }, onclick: async (e) => {
      if (await run(e.currentTarget, () => api('/api/admin/scan', { method: 'POST', body: {} }), 'Scanning all libraries')) renderTasks().catch(() => {});
    } }, 'Scan now')));
  const logBox = h('div', { class: 'logs', tabindex: 0, role: 'region', 'aria-label': 'Server log' });
  let follow = true;
  logBox.addEventListener('scroll', () => { follow = logBox.scrollTop + logBox.clientHeight >= logBox.scrollHeight - 20; });
  const renderTasks = async () => {
    const t = await api('/api/admin/tasks');
    if (!ctx.isCurrent()) return;
    const s = t.scan, m = t.metadata;
    status.replaceChildren(
      taskRow('Library scan', s.running, s.running ? `${s.phase}${s.library ? ' · ' + s.library : ''}${s.phase === 'probing' ? ` · ${s.probeDone}/${s.probeTotal} ${s.current}` : ''}` : `idle · last finished ${timeAgo(s.finishedAt)} (found ${s.found}, +${s.added}, −${s.removed}, changed ${s.changed})`),
      taskRow('Metadata', m.running, m.running ? `${m.done}/${m.total} · ${m.current}` : `idle · last run ${timeAgo(m.lastRun)} (${m.matched} matched, ${m.missing} not found)`),
      taskRow('Intro & credits detection', t.intro.running, !t.intro.available ? 'unavailable (ffmpeg without chromaprint)' : t.intro.running ? `${t.intro.done}/${t.intro.seasons} seasons · ${t.intro.current} · ${t.intro.found} intros, ${t.intro.creditsFound || 0} credits found` : `idle · last run ${timeAgo(t.intro.lastRun)}`),
      taskRow('SSD cache', !!t.cache.current, !t.cache.enabled ? 'disabled' : t.cache.current ? `copying ${t.cache.current.name} · ${Math.round((t.cache.current.done / t.cache.current.size) * 100)}% · ${fmtBytes(t.cache.current.speed)}/s${t.cache.queued.length ? ` · ${t.cache.queued.length} queued` : ''}` : `idle · ${t.cache.files} files, ${fmtBytes(t.cache.usedBytes)} of ${fmtBytes(t.cache.maxBytes)}`),
      t.trickplay ? taskRow('Seek previews', t.trickplay.running, !t.trickplay.enabled ? 'disabled' : t.trickplay.running ? `generating ${t.trickplay.current} · ${t.trickplay.pending} left` : t.trickplay.paused ? `paused while something is playing · ${t.trickplay.pending} left` : t.trickplay.pending ? `${t.trickplay.pending} waiting` : `idle · all done${t.trickplay.failed ? ` (${t.trickplay.failed} failed)` : ''}`) : '',
      h('div', { class: 'task' }, h('b', null, 'ffmpeg jobs'), `${t.remuxJobs} remux · ${t.transcodeJobs} transcode`));
  };
  const today = () => new Date().toDateString();
  const stamp = (t) => (new Date(t).toDateString() === today() ? new Date(t).toLocaleTimeString() : new Date(t).toLocaleString());
  const key = (l) => `${l.t}|${l.level}|${l.msg}`;
  const lineEl = (l) => h('div', { class: l.level }, `${stamp(l.t)} ${l.level.padEnd(5)} ${l.msg}`);
  let shown = []; // keys of the rendered lines, oldest first
  let text = [];
  const renderLogs = async () => {
    const lines = await api('/api/admin/logs');
    if (!ctx.isCurrent()) return;
    // Don't move lines under a selection the user is making or copying.
    const sel = getSelection();
    if (sel && !sel.isCollapsed && logBox.contains(sel.anchorNode)) return;
    const keys = lines.map(key);
    const at = shown.length ? keys.lastIndexOf(shown[shown.length - 1]) : -1;
    if (at < 0) {
      logBox.replaceChildren(...lines.map(lineEl));
    } else {
      logBox.append(...lines.slice(at + 1).map(lineEl));
      // The server keeps a fixed number of lines: drop the ones it dropped.
      while (logBox.childElementCount > lines.length) logBox.firstElementChild.remove();
    }
    shown = keys;
    text = lines.map((l) => `${new Date(l.t).toISOString()} ${l.level.padEnd(5)} ${l.msg}`);
    if (follow) logBox.scrollTop = logBox.scrollHeight;
  };
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text.join('\n'));
      toast('Log copied', 'ok');
    } catch {
      getSelection().selectAllChildren(logBox);
      toast('Press Ctrl+C (⌘C) to copy the selected log');
    }
  };
  await renderTasks();
  await renderLogs();
  const page = h('div', { class: 'form' }, tasks,
    h('div', { class: 'form-section' }, h('div', { class: 'row' }, h('h2', { class: 'grow' }, 'Log'),
        h('a', { class: 'btn sm', href: '/api/admin/report', download: '', title: 'System state, load over the last hour, settings and this log, with names, titles, paths and addresses hidden' }, 'Download report'),
        h('button', { class: 'btn sm', type: 'button', onclick: copy }, 'Copy log')), logBox));
  const banner = staleBanner(page);
  every(ctx, 2000, renderTasks, banner.hooks('tasks'));
  every(ctx, 3000, renderLogs, banner.hooks('logs'));
  return h('div', null, banner.el, page);
}

export async function aboutSection() {
  const i = await api('/api/admin/info');
  const f = i.ffmpeg;
  return h('div', { class: 'form' },
    h('div', { class: 'form-section' }, h('h2', null, 'AI disclosure'),
      h('p', null, 'lex was built by large language models: LLM coding agents wrote its code, interface, tests and documentation under human direction. LLM-written code can contain mistakes, including security ones, and has not had an independent security review. lex does not call AI model services at runtime.')),
    h('div', { class: 'form-section' }, h('h2', null, 'lex'), h('dl', { class: 'kv' },
      h('dt', null, 'Version'), h('dd', null, i.version),
      h('dt', null, 'Go runtime'), h('dd', null, i.go),
      h('dt', null, 'Data folder'), h('dd', { class: 'mono' }, i.dataDir),
      h('dt', null, 'Database'), h('dd', null, fmtBytes(i.dbSize)))),
    h('div', { class: 'form-section' }, h('h2', null, 'ffmpeg'), h('dl', { class: 'kv' },
      h('dt', null, 'ffmpeg'), h('dd', { class: 'mono' }, `${f.path} (${f.version || 'not found'})`),
      h('dt', null, 'ffprobe'), h('dd', { class: 'mono' }, f.probe),
      h('dt', null, 'H.264 encoders'), h('dd', null, (f.encoders || []).join(', ') || 'none'),
      h('dt', null, 'V4L2 device'), h('dd', null, f.v4l2Device ? 'present' : 'not present (hardware encoding unavailable)'),
      h('dt', null, 'Tone mapping'), h('dd', null, f.hasZscale && f.hasTonemap ? 'available' : 'unavailable'))),
    h('div', { class: 'form-section' }, h('h2', null, 'Caches'),
      h('div', { class: 'toggle-row' }, h('div', { class: 'lbl' }, h('b', null, 'Artwork'), h('span', { class: 'help' }, `${fmtBytes(i.imageCache)} — re-downloaded on demand`)), h('button', { class: 'btn sm', 'aria-label': 'Clear artwork cache', dataset: { focusKey: 'clear-images' }, onclick: async (e) => { if (await run(e.currentTarget, () => api('/api/admin/cache/clear', { method: 'POST', body: { kind: 'images' } }), 'Artwork cache cleared')) refreshSoft(); } }, 'Clear')),
      h('div', { class: 'toggle-row' }, h('div', { class: 'lbl' }, h('b', null, 'Subtitles'), h('span', { class: 'help' }, `${fmtBytes(i.subsCache)} — extracted WebVTT`)), h('button', { class: 'btn sm', 'aria-label': 'Clear subtitle cache', dataset: { focusKey: 'clear-subs' }, onclick: async (e) => { if (await run(e.currentTarget, () => api('/api/admin/cache/clear', { method: 'POST', body: { kind: 'subs' } }), 'Subtitle cache cleared')) refreshSoft(); } }, 'Clear')),
      h('div', { class: 'toggle-row' }, h('div', { class: 'lbl' }, h('b', null, 'Seek previews'), h('span', { class: 'help' }, `${fmtBytes(i.trickplay)} — trickplay thumbnails, removed with their media`)))));
}
