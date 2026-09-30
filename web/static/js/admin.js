import { h, clear, icons, toast, modal, confirmDialog, spinner, toggle, fmtBytes, fmtBitrate, fmtTime, fmtDate, timeAgo, fmtUptime, fmtDuration, LANG_OPTIONS, $ } from './ui.js';
import { api, img } from './api.js';
import { state, loadLibraries, route } from './app.js';
import { prefs, DEFAULTS, QUALITIES } from './prefs.js';
import { capsSummary } from './caps.js';
import { lineChart, columnChart, sparkline, barList, SERIES } from './charts.js';

// Timers owned by the current admin view; cleared on navigation.
let timers = [];
function every(ms, fn) { const t = setInterval(fn, ms); timers.push(t); return t; }
window.addEventListener('hashchange', () => { timers.forEach(clearInterval); timers = []; });

// ======================================================================
// Settings
// ======================================================================

const SECTIONS = [
  ['preferences', 'Playback', 'sliders', false],
  ['account', 'Account', 'user', false],
  ['server', 'Server', 'gear', true],
  ['transcoding', 'Transcoding', 'cpu', true],
  ['metadata', 'Metadata', 'film', true],
  ['cache', 'SSD cache', 'drive', true],
  ['libraries', 'Libraries', 'library', true],
  ['users', 'Users', 'users', true],
  ['devices', 'Devices', 'devices', true],
  ['logs', 'Tasks & logs', 'log', true],
  ['about', 'About', 'info', true],
];

export async function settingsView(ctx, section) {
  const nav = h('nav', { class: 'side-nav' },
    h('div', { class: 'grp' }, 'You'),
    SECTIONS.filter((s) => !s[3]).map(([id, label, ic]) => h('a', { href: `#/settings/${id}`, class: id === section ? 'active' : '' }, h('span', { html: icons[ic] }), label)),
    state.me.isAdmin ? [h('div', { class: 'grp' }, 'Server'), SECTIONS.filter((s) => s[3]).map(([id, label, ic]) => h('a', { href: `#/settings/${id}`, class: id === section ? 'active' : '' }, h('span', { html: icons[ic] }), label))] : null);
  const content = h('div');
  const def = SECTIONS.find((s) => s[0] === section);
  if (!def || (def[3] && !state.me.isAdmin)) content.appendChild(h('div', { class: 'empty' }, 'Not available'));
  else content.appendChild(await ({
    preferences: prefsSection, account: accountSection, server: serverSection, transcoding: transcodingSection,
    metadata: metadataSection, cache: cacheSection, libraries: librariesSection, users: usersSection, devices: devicesSection, logs: logsSection, about: aboutSection,
  })[section](ctx));
  return h('div', { class: 'page' }, h('h1', { class: 'page-title', style: { marginBottom: '22px' } }, 'Settings'), h('div', { class: 'side-layout' }, nav, content));
}

function field(label, input, help) {
  return h('label', { class: 'field' }, h('span', null, label), input, help ? h('div', { class: 'help' }, help) : null);
}

function toggleRow(label, help, checked, onchange) {
  return h('div', { class: 'toggle-row' }, h('div', { class: 'lbl' }, h('b', null, label), help ? h('span', { class: 'help' }, help) : null), toggle(checked, onchange));
}

function select(value, options, onchange) {
  return h('select', { onchange: (e) => onchange(e.target.value) }, options.map(([v, l]) => h('option', { value: String(v), selected: String(v) === String(value) }, l)));
}

// ---------- per-device playback prefs ----------
function prefsSection() {
  const p = prefs.all();
  const set = (k, conv = (x) => x) => (v) => { prefs.set(k, conv(v)); toast('Saved on this device', 'ok'); };
  const langs = [['', 'File default'], ...LANG_OPTIONS];
  const caps = capsSummary();
  return h('div', { class: 'form' },
    h('div', { class: 'form-section' }, h('h2', null, 'Quality & method'),
      h('p', { class: 'help', style: { margin: 0 } }, 'These settings are stored in this browser, so each device can have its own (e.g. lower quality on your phone).'),
      h('div', { class: 'form-grid' },
        field('Streaming quality', select(p.quality, QUALITIES, set('quality', Number)), 'Max bitrate. "Original" direct-plays when the browser supports the file.'),
        field('Playback method', select(p.mode, [['auto', 'Automatic (recommended)'], ['direct', 'Prefer direct play'], ['remux', 'Always direct stream (remux)'], ['transcode', 'Always transcode']], set('mode'))))),
    h('div', { class: 'form-section' }, h('h2', null, 'Buffering'),
      h('div', { class: 'form-grid' },
        field('Buffer ahead', select(p.forwardBuffer, [[30, '30 seconds'], [60, '1 minute'], [90, '90 seconds'], [180, '3 minutes'], [300, '5 minutes'], [600, '10 minutes']], set('forwardBuffer', Number)), 'For remux/transcode. More = smoother on flaky networks; the server pauses work once this is full.'),
        field('Keep behind playhead', select(p.backBuffer, [[10, '10 seconds'], [30, '30 seconds'], [60, '1 minute'], [120, '2 minutes']], set('backBuffer', Number)), 'Lets you rewind instantly; lower saves memory on phones/TVs.'),
        field('Progress update interval', select(p.heartbeat, [[5, '5 seconds'], [10, '10 seconds'], [30, '30 seconds']], set('heartbeat', Number))))),
    h('div', { class: 'form-section' }, h('h2', null, 'Audio & subtitles'),
      h('div', { class: 'form-grid' },
        field('Preferred audio language', select(p.audioLang, langs, set('audioLang'))),
        field('Subtitles', select(p.subMode, [['off', 'Off'], ['auto', 'Auto (forced / foreign audio)'], ['always', 'Always on']], set('subMode'))),
        field('Subtitle language', select(p.subLang, LANG_OPTIONS, set('subLang'))),
        field('Subtitle size', select(p.subSize, [['small', 'Small'], ['medium', 'Medium'], ['large', 'Large']], set('subSize'))),
        field('Subtitle position', select(p.subPos, [['low', 'Low'], ['normal', 'Normal'], ['high', 'High']], set('subPos')))),
      toggleRow('Subtitle background box', null, p.subBg, set('subBg'))),
    h('div', { class: 'form-section' }, h('h2', null, 'Player'),
      toggleRow('Autoplay next episode', 'Shows an "Up next" card near the end with a countdown.', p.autoplayNext, set('autoplayNext')),
      toggleRow('Skip intros automatically', 'Otherwise a "Skip Intro" button appears while an intro plays.', p.autoSkipIntro, set('autoSkipIntro')),
      toggleRow('Show stats for nerds by default', 'Overlay with codecs, bitrate, buffer health, dropped frames and server transcode speed (press i in the player).', p.showStats, set('showStats')),
      h('div', { class: 'form-grid' },
        field('Autoplay countdown', select(p.countdown, [[5, '5 seconds'], [10, '10 seconds'], [15, '15 seconds'], [30, '30 seconds']], set('countdown', Number))),
        field('Skip back', select(p.skipBack, [[5, '5s'], [10, '10s'], [15, '15s'], [30, '30s']], set('skipBack', Number))),
        field('Skip forward', select(p.skipFwd, [[10, '10s'], [15, '15s'], [30, '30s'], [60, '60s']], set('skipFwd', Number))))),
    h('div', { class: 'form-section' }, h('h2', null, "This browser's playback capabilities"),
      h('dl', { class: 'kv' }, Object.entries(caps).map(([k, v]) => [h('dt', null, k), h('dd', null, v)]))),
    h('div', null, h('button', { class: 'btn', onclick: () => { prefs.reset(); toast('Reset to defaults', 'ok'); route(); } }, 'Reset to defaults')));
}

function accountSection() {
  const cur = h('input', { type: 'password', autocomplete: 'current-password' });
  const n1 = h('input', { type: 'password', autocomplete: 'new-password' });
  const n2 = h('input', { type: 'password', autocomplete: 'new-password' });
  return h('div', { class: 'form' }, h('div', { class: 'form-section' }, h('h2', null, `Signed in as ${state.me.name}`),
    h('p', { class: 'muted', style: { margin: 0 } }, state.me.isAdmin ? 'Administrator' : 'User'),
    field('Current password', cur), field('New password', n1, 'Use 12–72 bytes.'), field('Confirm new password', n2),
    h('div', null, h('button', { class: 'btn primary', onclick: async () => {
      if (n1.value !== n2.value) return toast('Passwords do not match', 'error');
      try { await api('/api/me/password', { method: 'PUT', body: { current: cur.value, new: n1.value } }); toast('Password changed; other devices were signed out', 'ok'); cur.value = n1.value = n2.value = ''; }
      catch (e) { toast(e.message, 'error'); }
    } }, 'Change password'))));
}

// ---------- server config (shared by several sections) ----------
async function configForm(build) {
  let cfg = await api('/api/admin/config');
  const info = await api('/api/admin/info');
  const draft = { ...cfg };
  const bind = (k, conv = (x) => x) => (v) => { draft[k] = conv(v); };
  const num = (k, attrs = {}) => h('input', { type: 'number', value: draft[k], ...attrs, oninput: (e) => { draft[k] = Number(e.target.value); } });
  const text = (k, attrs = {}) => h('input', { type: 'text', value: draft[k] ?? '', ...attrs, oninput: (e) => { draft[k] = e.target.value; } });
  const tg = (k, label, help) => toggleRow(label, help, !!draft[k], bind(k));
  const save = h('button', { class: 'btn primary', onclick: async () => {
    try { cfg = await api('/api/admin/config', { method: 'PUT', body: draft }); Object.assign(draft, cfg); toast('Settings saved', 'ok'); }
    catch (e) { toast(e.message, 'error'); }
  } }, 'Save changes');
  return h('div', { class: 'form' }, build({ draft, num, text, tg, bind, info }), h('div', { class: 'save-bar' }, save));
}

function serverSection() {
  return configForm(({ draft, num, text, tg, info }) => [
    h('div', { class: 'form-section' }, h('h2', null, 'General'),
      field('Server name', text('serverName'))),
    h('div', { class: 'form-section' }, h('h2', null, 'Library scanning'),
      h('div', { class: 'form-grid' },
        field('Rescan every (minutes)', num('scanIntervalMin', { min: 0 }), '0 disables periodic scans. Scans only stat files, so they are cheap.'),
        field('Probe workers', num('probeWorkers', { min: 1, max: 8 }), 'Parallel ffprobe processes for new files. 1 is best on a Pi.')),
      tg('scanOnStartup', 'Scan on startup'),
      h('div', { class: 'help' }, 'Sonarr/Radarr: add a Webhook connection pointing at ', h('code', { class: 'mono' }, `${location.origin}${info.webhookUrl}`), ' to rescan instantly after imports.')),
    h('div', { class: 'form-section' }, h('h2', null, 'Network & remote access'),
      h('div', { class: 'form-grid' },
        field('Remote bitrate limit (kbps)', num('remoteMaxBitrate', { min: 0, step: 500 }), '0 = unlimited. Applies to clients outside the local networks below; files above it get transcoded.'),
        field('Stream write buffer (KB)', num('streamBufferKb', { min: 32, max: 4096 }))),
      field('Local networks (CIDR, comma separated)', text('localNetworks')),
      tg('trustProxy', 'Trust reverse-proxy headers', 'Enable only behind a proxy that replaces client-supplied forwarding headers.'),
      field('Trusted proxy peers (CIDR, comma separated)', text('trustedProxies'), 'Only these peers may supply client IP and HTTPS headers. Defaults to loopback; use exact proxy addresses where possible.')),
  ]);
}

function transcodingSection() {
  return configForm(({ draft, num, text, tg, bind, info }) => {
    const encs = info.ffmpeg.encoders || [];
    const encOpts = [['libx264', `libx264 (software)${encs.includes('libx264') ? '' : ' — not available'}`], ['h264_v4l2m2m', `h264_v4l2m2m (Raspberry Pi hardware)${encs.includes('h264_v4l2m2m') ? (info.ffmpeg.v4l2Device ? '' : ' — no /dev/video11') : ' — not in this ffmpeg'}`]];
    return [
      h('div', { class: 'form-section' }, h('h2', null, 'Playback methods'),
        tg('enableDirectPlay', 'Direct play', 'Send the original file untouched (range requests). Zero CPU.'),
        tg('enableRemux', 'Direct stream (remux)', 'Repackage into fragmented MP4 without re-encoding video. Very low CPU; audio is converted to AAC only if needed.'),
        tg('enableTranscode', 'Transcode', 'Re-encode video to H.264. CPU heavy on low-end boards.'),
        h('div', { class: 'form-grid' },
          field('Max simultaneous video transcodes', num('maxTranscodes', { min: 0, max: 16 }), '0 = unlimited. 1 is sensible on a Raspberry Pi.'),
          field('Max transcode resolution', h('select', { onchange: (e) => { draft.maxTranscodeHeight = +e.target.value; } }, [[480, '480p'], [720, '720p'], [1080, '1080p'], [2160, '4K']].map(([v, l]) => h('option', { value: v, selected: draft.maxTranscodeHeight === v }, l)))))),
      h('div', { class: 'form-section' }, h('h2', null, 'Video encoder'),
        field('Encoder', h('select', { onchange: (e) => { draft.videoEncoder = e.target.value; } }, encOpts.map(([v, l]) => h('option', { value: v, selected: draft.videoEncoder === v }, l)))),
        h('div', { class: 'form-grid' },
          field('x264 preset', h('select', { onchange: (e) => { draft.x264Preset = e.target.value; } }, ['ultrafast', 'superfast', 'veryfast', 'faster', 'fast', 'medium'].map((v) => h('option', { value: v, selected: draft.x264Preset === v }, v))), 'Faster presets use less CPU at slightly lower quality.'),
          field('x264 CRF', num('x264Crf', { min: 15, max: 35 }), 'Quality target (capped by the bitrate limit). Lower = better.'),
          field('Threads', num('transcodeThreads', { min: 0, max: 64 }), '0 = automatic.'),
          field('Process priority (nice)', num('ffmpegNice', { min: 0, max: 19 }), 'Higher = yields CPU to other services.')),
        tg('hwDecode', 'Hardware HEVC decoding (Raspberry Pi)', info.ffmpeg.hevcHwDecode ? 'Uses the Pi\'s HEVC decoder (/dev/video19) when transcoding HEVC; falls back to software automatically.' : 'Unavailable: needs dtoverlay=vc4-kms-v3d in /boot/firmware/config.txt (then reboot) and an ffmpeg with the drm hwaccel.'),
        tg('tonemap', 'HDR → SDR tone mapping', info.ffmpeg.hasZscale ? 'Converts HDR colours when transcoding. Expensive.' : 'Unavailable: this ffmpeg has no zscale filter.')),
      h('div', { class: 'form-section' }, h('h2', null, 'Audio'),
        h('div', { class: 'form-grid' },
          field('Transcoded audio channels', h('select', { onchange: (e) => { draft.audioChannels = +e.target.value; } }, [[2, 'Stereo'], [6, '5.1 surround']].map(([v, l]) => h('option', { value: v, selected: draft.audioChannels === v }, l)))),
          field('AAC bitrate (stereo, kbps)', num('audioBitrate', { min: 64, max: 640, step: 16 })))),
      h('div', { class: 'form-section' }, h('h2', null, 'Streaming'),
        h('div', { class: 'form-grid' },
          field('Fragment duration (ms)', num('fragmentMs', { min: 200, max: 10000, step: 100 }), 'Shorter = faster start & seeking, slightly more overhead.'),
          field('Keyframe interval (s)', num('keyframeSec', { min: 1, max: 10 }), 'For transcodes. Shorter = more precise seeking.'))),
    ];
  });
}

function metadataSection() {
  return configForm(({ draft, text, tg, num }) => [
    h('div', { class: 'form-section' }, h('h2', null, 'Providers'),
      h('p', { class: 'help', style: { margin: 0 } }, 'TMDB gives the richest data (posters, backdrops, cast, episode stills). Get a free API key at themoviedb.org → Settings → API. Without it, TVmaze is used for shows and your local Radarr (auto-detected) for movies — no keys needed.'),
      field('TMDB API key or read access token', text('tmdbKey', { type: 'password', placeholder: 'v3 key or v4 token', autocomplete: 'off' })),
      field('Language', text('metadataLanguage', { placeholder: 'en-US' })),
      tg('enableTmdb', 'Use TMDB'),
      tg('enableTvmaze', 'Use TVmaze for shows (no key)'),
      h('div', { class: 'form-grid' },
        field('Radarr URL', text('radarrUrl', { placeholder: 'http://127.0.0.1:7878' }), 'Used as a keyless TMDB source for movies (matched by folder name).'),
        field('Radarr API key', text('radarrKey', { type: 'password', autocomplete: 'off' }), 'Radarr → Settings → General → API Key'))),
    h('div', { class: 'form-section' }, h('h2', null, 'Intro detection'),
      h('p', { class: 'help', style: { margin: 0 } }, 'Finds TV intros by comparing audio fingerprints of neighbouring episodes (like Jellyfin\'s Intro Skipper), or from chapters named Intro/Opening. Runs in the background at the lowest CPU priority after scans.'),
      tg('introDetect', 'Detect intros'),
      h('div', { class: 'form-grid' },
        field('Analyse the first (seconds)', num('introScanSecs', { min: 120, max: 1800, step: 30 }), 'Intros after cold opens can start a few minutes in.'),
        field('Shortest intro (seconds)', num('introMinSecs', { min: 5, max: 60 })),
        field('Longest intro (seconds)', num('introMaxSecs', { min: 20, max: 300 }))),
      h('div', null, h('button', { class: 'btn', onclick: async () => { try { await api('/api/admin/intro/scan', { method: 'POST' }); toast('Intro detection started', 'ok'); } catch (e) { toast(e.message, 'error'); } } }, 'Detect intros now'))),
    h('div', { class: 'form-section' }, h('h2', null, 'Local'),
      tg('useLocalMetadata', 'Use local artwork & hints', 'poster.jpg / fanart.jpg / *-thumb.jpg, .plexmatch and .nfo ids.'),
      tg('generateThumbs', 'Generate missing episode thumbnails', 'Grabs one frame with ffmpeg when no still is available (cached).')),
    h('div', { class: 'form-section' }, h('h2', null, 'Refresh'),
      h('div', { class: 'row wrap' },
        h('button', { class: 'btn', onclick: async () => { await api('/api/admin/metadata/refresh', { method: 'POST', body: { missingOnly: true } }); toast('Retrying unmatched items', 'ok'); } }, 'Retry unmatched items'),
        h('button', { class: 'btn', onclick: async () => { if (await confirmDialog('Re-fetch metadata for every item? Manual matches are kept.')) { await api('/api/admin/metadata/refresh', { method: 'POST', body: {} }); toast('Refreshing all metadata in background', 'ok'); } } }, 'Refresh all metadata'))),
  ]);
}

// ---------- libraries ----------
async function librariesSection() {
  const libs = await api('/api/admin/libraries');
  const box = h('div', { class: 'form' });
  box.appendChild(h('div', { class: 'row' }, h('div', { class: 'spacer' }),
    h('button', { class: 'btn', onclick: async () => { await api('/api/admin/scan', { method: 'POST', body: {} }); toast('Scanning all libraries', 'ok'); } }, h('span', { html: icons.refresh }), 'Scan all'),
    h('button', { class: 'btn primary', onclick: () => libraryModal() }, h('span', { html: icons.plus }), 'Add library')));
  if (!libs.length) box.appendChild(h('div', { class: 'empty' }, h('h2', null, 'No libraries'), h('p', null, 'Add a folder containing movies or TV shows.')));
  for (const l of libs) {
    box.appendChild(h('div', { class: 'form-section' },
      h('div', { class: 'row' }, h('h2', { class: 'grow' }, l.name), h('span', { class: 'dim' }, `${l.kind} · ${l.itemCount} items · scanned ${timeAgo(l.lastScan)}`)),
      h('div', { class: 'chips' }, l.paths.map((p) => h('span', { class: 'chip mono', style: { paddingRight: '12px' } }, p))),
      h('div', { class: 'row' },
        h('button', { class: 'btn sm', onclick: async () => { await api('/api/admin/scan', { method: 'POST', body: { libraryId: l.id } }); toast(`Scanning ${l.name}`, 'ok'); } }, 'Scan'),
        h('button', { class: 'btn sm', onclick: () => libraryModal(l) }, 'Edit'),
        h('button', { class: 'btn sm danger', onclick: async () => {
          if (!(await confirmDialog(`Remove library "${l.name}"? Files on disk are not touched; watch history for its items is removed.`, 'Remove', true))) return;
          await api(`/api/admin/libraries/${l.id}`, { method: 'DELETE' });
          await loadLibraries(); route();
        } }, 'Remove'))));
  }
  box.appendChild(h('div', { class: 'help' }, 'Types: Movies (one movie per folder or file), TV Shows (Show/Season N/episodes), Mixed (auto-detects episodes by S01E01 or Season folders — good for anime folders with both series and films).'));
  return box;
}

function libraryModal(lib) {
  const name = h('input', { type: 'text', value: lib?.name || '' });
  let kind = lib?.kind || 'movies';
  const paths = [...(lib?.paths || [])];
  const chips = h('div', { class: 'chips' });
  const renderChips = () => clear(chips).append(...paths.map((p, i) => h('span', { class: 'chip mono' }, p, h('button', { html: icons.close, onclick: () => { paths.splice(i, 1); renderChips(); } }))));
  renderChips();
  const browser = folderBrowser((p) => { if (!paths.includes(p)) { paths.push(p); renderChips(); } });
  const m = modal({ title: lib ? `Edit ${lib.name}` : 'Add library', wide: true, body: [
    field('Name', name),
    field('Type', select(kind, [['movies', 'Movies'], ['shows', 'TV Shows'], ['mixed', 'Mixed (auto-detect)']], (v) => { kind = v; }), lib ? 'Changing the type rebuilds the library (watch progress for its items resets).' : null),
    field('Folders', chips),
    browser,
  ], actions: [
    h('button', { class: 'btn', onclick: () => m.close() }, 'Cancel'),
    h('button', { class: 'btn primary', onclick: async () => {
      const body = { name: name.value || 'Library', kind, paths };
      try {
        if (lib) await api(`/api/admin/libraries/${lib.id}`, { method: 'PUT', body });
        else await api('/api/admin/libraries', { method: 'POST', body });
        m.close(); toast(lib ? 'Library updated; rescanning' : 'Library added; scanning now', 'ok');
        await loadLibraries(); route();
      } catch (e) { toast(e.message, 'error'); }
    } }, lib ? 'Save' : 'Add library')] });
}

function folderBrowser(onPick) {
  const pathIn = h('input', { type: 'text', value: '/', style: { flex: 1 } });
  const list = h('div', { class: 'fs-list' });
  const go = async (p) => {
    try {
      const r = await api(`/api/admin/fs?path=${encodeURIComponent(p)}`);
      pathIn.value = r.path;
      clear(list);
      if (r.path !== '/') list.appendChild(h('button', { onclick: () => go(r.parent) }, h('span', { html: icons.up }), '..'));
      for (const d of r.dirs) list.appendChild(h('button', { onclick: () => go((r.path === '/' ? '' : r.path) + '/' + d) }, h('span', { html: icons.folder }), d));
      if (!r.dirs.length) list.appendChild(h('div', { class: 'dim small', style: { padding: '8px' } }, 'No sub-folders'));
    } catch (e) { toast(e.message, 'error'); }
  };
  go(state.libraries[0]?.paths?.[0]?.replace(/\/[^/]+$/, '') || '/mnt');
  return h('div', { style: { display: 'grid', gap: '8px' } },
    h('form', { class: 'row', onsubmit: (e) => { e.preventDefault(); go(pathIn.value); } }, pathIn, h('button', { class: 'btn sm', type: 'submit' }, 'Go'), h('button', { class: 'btn sm primary', type: 'button', onclick: () => onPick(pathIn.value) }, h('span', { html: icons.plus }), 'Add this folder')),
    list);
}

// ---------- users & devices ----------
async function usersSection() {
  const users = await api('/api/admin/users');
  const tbl = h('table', { class: 'tbl' }, h('tr', null, h('th', null, 'Name'), h('th', null, 'Role'), h('th', null, 'Created'), h('th')),
    users.map((u) => h('tr', null, h('td', null, h('b', null, u.name)), h('td', null, u.isAdmin ? 'Admin' : 'User'), h('td', null, fmtDate(u.createdAt)),
      h('td', { style: { textAlign: 'right' } }, h('div', { class: 'row', style: { justifyContent: 'flex-end' } },
        h('button', { class: 'btn sm', onclick: async () => {
          const pw = prompt(`New password for ${u.name}:`);
          if (!pw) return;
          try { await api(`/api/admin/users/${u.id}`, { method: 'PUT', body: { password: pw } }); toast('Password reset', 'ok'); } catch (e) { toast(e.message, 'error'); }
        } }, 'Reset password'),
        h('button', { class: 'btn sm', onclick: async () => {
          try { await api(`/api/admin/users/${u.id}`, { method: 'PUT', body: { isAdmin: !u.isAdmin } }); route(); } catch (e) { toast(e.message, 'error'); }
        } }, u.isAdmin ? 'Make user' : 'Make admin'),
        u.id !== state.me.id ? h('button', { class: 'btn sm danger', onclick: async () => {
          if (await confirmDialog(`Delete user ${u.name}?`, 'Delete', true)) { await api(`/api/admin/users/${u.id}`, { method: 'DELETE' }); route(); }
        } }, 'Delete') : null)))));
  const name = h('input', { type: 'text', placeholder: 'Username' });
  const pw = h('input', { type: 'password', placeholder: 'Password (12–72 bytes)', autocomplete: 'new-password' });
  let admin = false;
  return h('div', { class: 'form' },
    h('div', { class: 'form-section' }, h('h2', null, 'Users'), h('div', { class: 'table-wrap' }, tbl)),
    h('div', { class: 'form-section' }, h('h2', null, 'Add user'),
      h('div', { class: 'form-grid' }, name, pw),
      toggleRow('Administrator', 'Can change settings, libraries and see stats.', false, (v) => { admin = v; }),
      h('div', null, h('button', { class: 'btn primary', onclick: async () => {
        try { await api('/api/admin/users', { method: 'POST', body: { name: name.value, password: pw.value, isAdmin: admin } }); toast('User created', 'ok'); route(); } catch (e) { toast(e.message, 'error'); }
      } }, 'Create user'))));
}

async function devicesSection() {
  const devs = await api('/api/admin/devices');
  return h('div', { class: 'form' }, h('div', { class: 'form-section' }, h('h2', null, 'Signed-in devices'),
    h('div', { class: 'table-wrap' }, h('table', { class: 'tbl' }, h('tr', null, h('th', null, 'User'), h('th', null, 'Client'), h('th', null, 'IP'), h('th', null, 'Signed in'), h('th', null, 'Last seen'), h('th')),
      devs.map((d) => h('tr', null, h('td', null, d.userName), h('td', null, d.client), h('td', { class: 'mono' }, d.ip), h('td', null, fmtDate(d.created)), h('td', null, timeAgo(d.lastSeen)),
        h('td', null, h('button', { class: 'btn sm danger', onclick: async () => { await api(`/api/admin/devices/${d.prefix}`, { method: 'DELETE' }); route(); } }, 'Sign out'))))))));
}

function taskRow(label, running, text) {
  return h('div', { class: 'task' }, running ? h('div', { class: 'spinner', style: { width: '16px', height: '16px', borderWidth: '2px' } }) : h('span', { class: 'good', html: icons.check, style: { width: '16px' } }), h('b', null, label), text);
}

// ---------- SSD cache ----------
async function cacheSection(ctx) {
  const statusBox = h('div', { class: 'form-section' });
  const listBox = h('div', { class: 'form-section' });
  const render = async () => {
    const r = await api('/api/admin/cache').catch(() => null);
    if (!r || !ctx.isCurrent()) return;
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
    clear(listBox).append(h('div', { class: 'row' }, h('h2', { class: 'grow' }, 'Cached files'),
      r.entries.length ? h('button', { class: 'btn sm danger', onclick: async () => { if (await confirmDialog('Delete every cached copy? Originals are not touched.', 'Clear', true)) { await api('/api/admin/cache/clear', { method: 'POST', body: { kind: 'media' } }); render(); } } }, 'Clear cache') : null),
    r.entries.length ? h('div', { class: 'table-wrap' }, h('table', { class: 'tbl' },
      h('tr', null, ['Title', 'Size', 'Cached', 'Last used', 'Reason', ''].map((t) => h('th', null, t))),
      r.entries.map((e) => h('tr', null, h('td', null, h('a', { href: `#/item/${e.itemId}` }, e.title), h('div', { class: 'dim small ellipsis', style: { maxWidth: '380px' } }, e.name)), h('td', { class: 'nowrap' }, fmtBytes(e.size)), h('td', { class: 'nowrap' }, timeAgo(e.addedAt)), h('td', { class: 'nowrap' }, timeAgo(e.lastAccess)), h('td', null, e.reason),
        h('td', null, h('button', { class: 'btn sm', onclick: async () => { await api(`/api/admin/cache/items/${e.itemId}`, { method: 'DELETE' }); render(); } }, 'Remove'))))))
      : h('p', { class: 'muted', style: { margin: 0 } }, 'Nothing cached yet. Files are copied when played (and upcoming episodes are prefetched).'));
  };
  const form = await configForm(({ draft, num, text, tg }) => [
    h('div', { class: 'form-section' }, h('h2', null, 'SSD cache'),
      h('p', { class: 'help', style: { margin: 0 } }, 'Keeps copies of media on a fast disk (e.g. the SSD) so playback — and seeking — reads from it instead of the hard drive, which can then stay asleep. Files are copied in the background at a capped speed; the least recently watched are evicted first.'),
      tg('cacheEnabled', 'Enable SSD cache'),
      field('Cache folder', text('cacheDir', { placeholder: 'default: <data folder>/cache' }), 'A "lex-cache" folder is created inside it. Leave empty to use Lex\'s data folder (on the SSD here).'),
      h('div', { class: 'form-grid' },
        field('Maximum size (GB)', num('cacheMaxGb', { min: 1 })),
        field('Keep free on disk (GB)', num('cacheMinFreeGb', { min: 0 })),
        field('Largest file to cache (GB)', num('cacheMaxFileGb', { min: 1 })),
        field('Copy speed limit (MB/s)', num('cacheSpeedMbs', { min: 0 }), '0 = unlimited. Keeps the hard drive responsive for playback.')),
      tg('cacheOnPlay', 'Cache what\'s being played', 'The rest of the file is read from the SSD after the copy finishes (after the next seek for remux/transcode).'),
      field('Prefetch upcoming episodes', h('select', { onchange: (e) => { draft.cachePrefetch = +e.target.value; } }, [0, 1, 2, 3, 5].map((v) => h('option', { value: v, selected: draft.cachePrefetch === v }, v ? `Next ${v}` : 'Off'))))),
  ]);
  await render();
  every(2000, render);
  return h('div', { class: 'form' }, form, statusBox, listBox);
}

// ---------- tasks & logs ----------
async function logsSection() {
  const tasks = h('div', { class: 'form-section' });
  const logBox = h('div', { class: 'logs' });
  let follow = true;
  logBox.addEventListener('scroll', () => { follow = logBox.scrollTop + logBox.clientHeight >= logBox.scrollHeight - 20; });
  const renderTasks = async () => {
    const t = await api('/api/admin/tasks').catch(() => null);
    if (!t) return;
    const s = t.scan, m = t.metadata;
    clear(tasks).append(h('h2', null, 'Background tasks'),
      h('div', { class: 'task' }, s.running ? h('div', { class: 'spinner', style: { width: '16px', height: '16px', borderWidth: '2px' } }) : h('span', { class: 'good', html: icons.check, style: { width: '16px' } }),
        h('b', null, 'Library scan'), s.running ? `${s.phase}${s.library ? ' · ' + s.library : ''}${s.phase === 'probing' ? ` · ${s.probeDone}/${s.probeTotal} ${s.current}` : ''}` : `idle · last finished ${timeAgo(s.finishedAt)} (found ${s.found}, +${s.added}, −${s.removed}, changed ${s.changed})`),
      h('div', { class: 'task' }, m.running ? h('div', { class: 'spinner', style: { width: '16px', height: '16px', borderWidth: '2px' } }) : h('span', { class: 'good', html: icons.check, style: { width: '16px' } }),
        h('b', null, 'Metadata'), m.running ? `${m.done}/${m.total} · ${m.current}` : `idle · last run ${timeAgo(m.lastRun)} (${m.matched} matched, ${m.missing} not found)`),
      taskRow('Intro detection', t.intro.running, !t.intro.available ? 'unavailable (ffmpeg without chromaprint)' : t.intro.running ? `${t.intro.done}/${t.intro.seasons} seasons · ${t.intro.current} · ${t.intro.found} found` : `idle · last run ${timeAgo(t.intro.lastRun)}`),
      taskRow('SSD cache', !!t.cache.current, !t.cache.enabled ? 'disabled' : t.cache.current ? `copying ${t.cache.current.name} · ${Math.round((t.cache.current.done / t.cache.current.size) * 100)}% · ${fmtBytes(t.cache.current.speed)}/s${t.cache.queued.length ? ` · ${t.cache.queued.length} queued` : ''}` : `idle · ${t.cache.files} files, ${fmtBytes(t.cache.usedBytes)} of ${fmtBytes(t.cache.maxBytes)}`),
      h('div', { class: 'task' }, h('b', null, 'ffmpeg jobs'), `${t.remuxJobs} remux · ${t.transcodeJobs} transcode`),
      h('div', { class: 'row' }, h('button', { class: 'btn sm', onclick: async () => { await api('/api/admin/scan', { method: 'POST', body: {} }); renderTasks(); } }, 'Scan now')));
  };
  const renderLogs = async () => {
    const lines = await api('/api/admin/logs').catch(() => []);
    clear(logBox).append(...lines.map((l) => h('div', { class: l.level }, `${new Date(l.t).toLocaleTimeString()} ${l.level.padEnd(5)} ${l.msg}`)));
    if (follow) logBox.scrollTop = logBox.scrollHeight;
  };
  await renderTasks();
  await renderLogs();
  every(2000, renderTasks);
  every(3000, renderLogs);
  return h('div', { class: 'form', style: { maxWidth: 'none' } }, tasks, h('div', { class: 'form-section' }, h('h2', null, 'Log'), logBox));
}

async function aboutSection() {
  const i = await api('/api/admin/info');
  const f = i.ffmpeg;
  return h('div', { class: 'form' },
    h('div', { class: 'form-section' }, h('h2', null, 'AI disclosure'),
      h('p', null, 'Lex was developed with generative AI assistance in code, documentation and review. AI-assisted output can contain errors; maintainers are responsible for validation. Lex does not call AI model services at runtime.')),
    h('div', { class: 'form-section' }, h('h2', null, 'Lex'), h('dl', { class: 'kv' },
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
      h('div', { class: 'toggle-row' }, h('div', { class: 'lbl' }, h('b', null, 'Artwork'), h('span', { class: 'help' }, `${fmtBytes(i.imageCache)} — re-downloaded on demand`)), h('button', { class: 'btn sm', onclick: async () => { await api('/api/admin/cache/clear', { method: 'POST', body: { kind: 'images' } }); toast('Artwork cache cleared', 'ok'); route(); } }, 'Clear')),
      h('div', { class: 'toggle-row' }, h('div', { class: 'lbl' }, h('b', null, 'Subtitles'), h('span', { class: 'help' }, `${fmtBytes(i.subsCache)} — extracted WebVTT`)), h('button', { class: 'btn sm', onclick: async () => { await api('/api/admin/cache/clear', { method: 'POST', body: { kind: 'subs' } }); toast('Subtitle cache cleared', 'ok'); route(); } }, 'Clear'))));
}

// ======================================================================
// Dashboard
// ======================================================================

const DASH = [['live', 'Live', 'broadcast'], ['playback', 'Playback', 'stats'], ['library', 'Library', 'library'], ['history', 'History', 'history']];

export async function dashboardView(ctx, tab) {
  if (!state.me.isAdmin) return h('div', { class: 'empty' }, 'Admins only');
  const content = await ({ live: liveTab, playback: playbackTab, library: libraryTab, history: historyTab }[tab] || liveTab)(ctx);
  return h('div', { class: 'page' },
    h('h1', { class: 'page-title' }, 'Dashboard'),
    h('div', { class: 'tabs', style: { marginTop: '14px' } }, DASH.map(([id, label]) => h('button', { class: id === tab ? 'active' : '', onclick: () => { location.hash = `#/dashboard/${id}`; } }, label))),
    content);
}

function stat(label, value, sub, extra) {
  return h('div', { class: 'stat' }, h('div', { class: 'l' }, label), h('div', { class: 'v' }, value), sub != null ? h('div', { class: 'sub' }, sub) : null, extra);
}

function meter(pct, hotAt = 85) {
  return h('div', { class: 'meter' }, h('i', { class: pct >= hotAt ? 'hot' : '', style: { width: `${Math.min(100, Math.max(0, pct))}%` } }));
}

const bps = (bytesPerSec) => fmtBitrate(bytesPerSec * 8);

async function liveTab(ctx) {
  const tiles = h('div', { class: 'stats-grid' });
  const cores = h('div');
  const sessionsBox = h('div', { class: 'sessions' });
  const charts = { cpu: h('div'), net: h('div'), temp: h('div'), stream: h('div') };
  const extra = h('div', { class: 'info-grid', style: { marginTop: '14px' } });
  const sparkCanvases = {};
  const spark = (k) => (sparkCanvases[k] ||= h('canvas'));

  const renderSystem = async (withHistory) => {
    const r = await api(`/api/admin/stats/system${withHistory ? '?history=1' : ''}`).catch(() => null);
    if (!r || !ctx.isCurrent()) return;
    const s = r.snapshot, n = s.now;
    if (withHistory || !renderSystem.hist) renderSystem.hist = r.history || [];
    else { renderSystem.hist.push(n); if (renderSystem.hist.length > 1200) renderSystem.hist.shift(); }
    const hist = renderSystem.hist;
    const last = (k, count = 40) => hist.slice(-count).map((p) => p[k]);
    const memPct = s.memTotal ? (s.memUsed / s.memTotal) * 100 : 0;
    clear(tiles).append(
      stat('CPU', `${n.cpu.toFixed(0)}%`, `load ${s.load.map((x) => x.toFixed(2)).join(' ')}${s.freqMhz ? ` · ${Math.round(s.freqMhz)} MHz` : ''}`, [meter(n.cpu), spark('cpu')]),
      stat('Memory', `${memPct.toFixed(0)}%`, `${fmtBytes(s.memUsed)} of ${fmtBytes(s.memTotal)}${s.swapTotal ? ` · swap ${fmtBytes(s.swapUsed)}` : ''}`, meter(memPct, 90)),
      stat('Temperature', n.temp ? `${n.temp.toFixed(1)}°C` : '—', s.throttleFlags?.length ? s.throttleFlags.join(', ') : (s.throttled ? 'no throttling' : ''), [n.temp ? meter((n.temp / 85) * 100, 88) : null, spark('temp')]),
      stat('Network out', bps(n.tx), `in ${bps(n.rx)}`, spark('tx')),
      stat('Streaming', bps(n.stream), `${n.sessions} session${n.sessions === 1 ? '' : 's'} · ${r.remuxJobs} remux · ${r.transcodeJobs} transcode`, spark('stream')),
      stat('Disk I/O', `${fmtBytes(n.dr)}/s`, `write ${fmtBytes(n.dw)}/s`, spark('dr')),
      ...(r.cache?.enabled ? [stat('SSD cache', fmtBytes(r.cache.usedBytes), `of ${fmtBytes(r.cache.maxBytes)} · ${r.cache.files} files${r.cache.current ? ` · copying ${Math.round((r.cache.current.done / r.cache.current.size) * 100)}%` : ''}`, meter((r.cache.usedBytes / r.cache.maxBytes) * 100, 97))] : []),
      stat('Lex process', fmtBytes(s.procRss), `CPU ${s.procCpu.toFixed(1)}% · heap ${fmtBytes(s.goHeap)} · ${s.goroutines} goroutines`),
      stat('Uptime', fmtUptime(s.uptime), `${s.model || s.os} · ${s.arch} · ${s.numCpu} cores`));
    sparkline(spark('cpu'), last('cpu'), 100);
    sparkline(spark('temp'), last('temp'));
    sparkline(spark('tx'), last('tx'));
    sparkline(spark('stream'), last('stream'));
    sparkline(spark('dr'), last('dr'));
    clear(cores).append(h('div', { class: 'cores' }, (s.cores || []).map((c, i) => h('div', { class: 'core', title: `Core ${i}: ${c.toFixed(0)}%` }, h('i', { style: { height: `${c}%` } }), h('span', null, `${c.toFixed(0)}%`)))));
    clear(extra).append(
      h('div', { class: 'panel' }, h('h3', null, 'Storage'), (s.disks || []).map((d) => h('div', { style: { marginBottom: '10px' } },
        h('div', { class: 'row small' }, h('span', { class: 'mono grow ellipsis' }, d.path), h('span', { class: 'muted' }, `${fmtBytes(d.used)} / ${fmtBytes(d.total)}`)), meter((d.used / d.total) * 100, 92)))),
      h('div', { class: 'panel' }, h('h3', null, 'Interfaces'), h('dl', { class: 'kv' }, (s.ifaces || []).map((i) => [h('dt', null, i.name), h('dd', null, `↑ ${bps(i.tx)} · ↓ ${bps(i.rx)} (total ↑ ${fmtBytes(i.txTotal)})`)]))));
    const times = hist.map((p) => p.t);
    if (times.length > 1) {
      lineChart(charts.cpu, { times, series: [{ name: 'CPU', values: hist.map((p) => p.cpu) }, { name: 'Lex process', values: hist.map((p) => p.pcpu / Math.max(1, s.numCpu)) }], fmt: (v) => `${v.toFixed(0)}%`, max: 100 });
      lineChart(charts.net, { times, series: [{ name: 'Out', values: hist.map((p) => p.tx) }, { name: 'In', values: hist.map((p) => p.rx) }], fmt: (v) => bps(v) });
      lineChart(charts.temp, { times, series: [{ name: 'SoC temp', values: hist.map((p) => p.temp) }], fmt: (v) => `${v.toFixed(0)}°`, max: 90 });
      lineChart(charts.stream, { times, series: [{ name: 'Streaming', values: hist.map((p) => p.stream) }], fmt: (v) => bps(v) });
    }
  };

  const renderSessions = async () => {
    const list = await api('/api/admin/stats/sessions').catch(() => null);
    if (!list || !ctx.isCurrent()) return;
    clear(sessionsBox);
    if (!list.length) { sessionsBox.appendChild(h('div', { class: 'panel dim' }, 'Nothing is playing right now.')); return; }
    list.sort((a, b) => b.startedAt - a.startedAt);
    for (const s of list) {
      const c = s.clientStats || {};
      const j = s.job;
      const pct = s.duration ? (s.position / s.duration) * 100 : 0;
      const kv = [
        ['Video', `${s.videoIn || '?'} → ${s.videoOut || ''}`],
        ['Audio', `${s.audioIn || '?'} → ${s.audioOut || ''}`],
        ['Bitrate', `${fmtBitrate((s.srcBitrate || 0) * 1000)} source${s.method === 'transcode' ? ` → ${fmtBitrate((s.outBitrate || 0) * 1000)}` : ''}`],
        ['Delivery', `${bps(s.rate)} · ${fmtBytes(s.bytes)} sent`],
        ['Client buffer', `${(c.bufferAhead || 0).toFixed(0)}s ahead${c.buffering ? ' · buffering!' : ''}`],
        ['Client speed', c.bandwidth ? fmtBitrate(c.bandwidth) : '—'],
        ['Stalls', `${c.bufferEvents || 0} (${(c.bufferSeconds || 0).toFixed(1)}s)`],
        ['Frames', c.totalFrames ? `${c.droppedFrames} dropped of ${c.totalFrames}` : '—'],
        ['Client', `${s.client} · ${s.ip}${s.remote ? ' (remote)' : ' (local)'}`],
        ['Source', s.cached ? 'SSD cache' : 'library disk'],
        ['Rendered', c.resolution || '—'],
      ];
      if (j) kv.push(['ffmpeg', j.exited ? (j.error ? `exited: ${j.error}` : 'finished') : `${j.throttled ? 'throttled (client buffer full)' : `${(j.speed || 0).toFixed(2)}x · ${Math.round(j.fps || 0)} fps`} · CPU ${Math.round(j.cpu || 0)}%`], ['Processed', `to ${fmtTime(j.outTime)} · restarts ${s.restarts}`]);
      sessionsBox.appendChild(h('div', { class: 'session' },
        h('a', { class: 'poster', href: `#/item/${s.itemId}` }, h('img', { src: img({ id: s.itemId }, 'poster', 160), alt: '' })),
        h('div', { style: { minWidth: 0 } },
          h('div', { class: 'row' },
            h('div', { class: 'grow', style: { minWidth: 0 } }, h('h3', { class: 'ellipsis' }, s.title), h('div', { class: 'dim small ellipsis' }, `${s.subtitle || ''} — ${s.userName}`)),
            h('span', { class: `method ${s.method}` }, s.method === 'remux' ? 'direct stream' : s.method === 'direct' ? 'direct play' : 'transcode'),
            h('button', { class: 'btn sm danger', title: 'Stop this stream', onclick: async () => { if (await confirmDialog(`Stop ${s.userName}'s stream?`, 'Stop', true)) { await api(`/api/admin/sessions/${s.id}`, { method: 'DELETE' }); renderSessions(); } } }, 'Stop')),
          s.reasons?.length ? h('div', { class: 'small warn', style: { marginTop: '4px' } }, s.reasons.join(' · ')) : null,
          h('div', { class: 'row small', style: { marginTop: '8px' } }, h('span', { class: 'muted nowrap' }, `${s.paused ? '⏸' : '▶'} ${fmtTime(s.position)} / ${fmtTime(s.duration)}`), h('div', { class: 'grow' }, meter(pct, 101))),
          h('dl', { class: 'kv' }, kv.map(([k, v]) => [h('dt', null, k), h('dd', null, v)])))));
    }
  };

  await Promise.all([renderSystem(true), renderSessions()]);
  every(3000, () => renderSystem(false));
  every(2000, renderSessions);
  const chartPanel = (title, box) => h('div', { class: 'panel' }, h('h3', null, title), box);
  return h('div', null,
    tiles,
    h('div', { class: 'panel', style: { marginBottom: '14px' } }, h('h3', null, 'CPU cores'), cores),
    h('h2', { class: 'section-title', style: { margin: '22px 0 12px' } }, 'Now playing'),
    sessionsBox,
    h('h2', { class: 'section-title', style: { margin: '26px 0 12px' } }, 'Last hour'),
    h('div', { class: 'info-grid' }, chartPanel('CPU', charts.cpu), chartPanel('Network', charts.net), chartPanel('Temperature', charts.temp), chartPanel('Streaming throughput', charts.stream)),
    extra);
}

async function playbackTab(ctx) {
  const days = +(ctx.query.get('days') || 30);
  const st = await api(`/api/admin/stats/playback?days=${days}`);
  // Fill missing days so the chart has a continuous axis.
  const byDay = Object.fromEntries((st.days || []).map((d) => [d.day, d]));
  const labels = [], full = [], hours = [], plays = [];
  const nDays = Math.min(days, 120);
  for (let i = nDays - 1; i >= 0; i--) {
    const dt = new Date(Date.now() - i * 86400000);
    const key = `${dt.getFullYear()}-${String(dt.getMonth() + 1).padStart(2, '0')}-${String(dt.getDate()).padStart(2, '0')}`;
    labels.push(dt.toLocaleDateString(undefined, { month: 'short', day: 'numeric' }));
    full.push(dt.toLocaleDateString(undefined, { dateStyle: 'medium' }));
    hours.push(byDay[key]?.hours || 0);
    plays.push(byDay[key]?.plays || 0);
  }
  const hoursBox = h('div'), hourBox = h('div');
  const hod = Array.from({ length: 24 }, (_, i) => (st.hoursOfDay || []).find((b) => +b.key === i)?.count || 0);
  const methodColor = (it) => ({ direct: SERIES[0], remux: SERIES[1], transcode: SERIES[2] })[it.key] || SERIES[0];
  const mlabel = (k) => ({ direct: 'Direct play', remux: 'Direct stream', transcode: 'Transcode' })[k] || k;
  const page = h('div', null,
    h('div', { class: 'toolbar' }, h('span', { class: 'muted' }, 'Range'), [7, 30, 90, 365].map((d) => h('button', { class: `btn sm ${d === days ? 'primary' : ''}`, onclick: () => { location.hash = `#/dashboard/playback?days=${d}`; } }, `${d} days`))),
    h('div', { class: 'stats-grid' },
      stat('Plays', st.plays.toLocaleString(), `${st.uniqueItems} unique titles`),
      stat('Watch time', `${st.hours.toFixed(1)} h`, `${(st.hours / Math.max(1, days)).toFixed(1)} h/day average`),
      stat('Data served', fmtBytes(st.bytes), st.plays ? `${fmtBytes(st.bytes / st.plays)} per play` : ''),
      stat('Remote plays', st.plays ? `${Math.round((st.remotePlays / st.plays) * 100)}%` : '—', `${st.remotePlays} of ${st.plays}`),
      stat('Buffering', `${st.bufferEvents} stalls`, st.hours ? `${((st.bufferSeconds / (st.hours * 3600)) * 100).toFixed(2)}% of watch time (${st.bufferSeconds.toFixed(0)}s)` : '')),
    h('div', { class: 'panel', style: { marginBottom: '14px' } }, h('h3', null, 'Hours watched per day'), hoursBox),
    h('div', { class: 'info-grid' },
      h('div', { class: 'panel' }, h('h3', null, 'Playback methods (plays)'), barList((st.methods || []).map((m) => ({ key: m.key, label: mlabel(m.key), value: m.count })), (v) => `${v}`, methodColor),
        h('div', { class: 'legend', style: { marginTop: '10px' } }, ['direct', 'remux', 'transcode'].map((k, i) => h('span', null, h('i', { style: { background: SERIES[i] } }), mlabel(k))))),
      h('div', { class: 'panel' }, h('h3', null, 'Stall time by method (% of watch time)'), barList((st.bufferByMethod || []).map((m) => ({ key: m.key, label: mlabel(m.key), value: m.value })), (v) => `${v.toFixed(2)}%`, methodColor)),
      h('div', { class: 'panel' }, h('h3', null, 'Most watched'), barList((st.topItems || []).map((m) => ({ label: m.key, value: m.count })), (v) => `${v} play${v === 1 ? '' : 's'}`)),
      h('div', { class: 'panel' }, h('h3', null, 'Users (hours)'), barList((st.users || []).map((m) => ({ label: m.key, value: m.value })), (v) => `${v.toFixed(1)} h`)),
      h('div', { class: 'panel' }, h('h3', null, 'Clients (plays)'), barList((st.clients || []).map((m) => ({ label: m.key || 'unknown', value: m.count })), (v) => `${v}`)),
      h('div', { class: 'panel' }, h('h3', null, 'Why streams were converted'), barList((st.reasons || []).map((m) => ({ label: m.key, value: m.count })), (v) => `${v}`)),
      h('div', { class: 'panel' }, h('h3', null, 'Time of day (plays started)'), hourBox)));
  requestAnimationFrame(() => {
    columnChart(hoursBox, { labels, fullLabels: full, values: hours, fmt: (v) => `${v.toFixed(1)}h`, tipLabel: 'watched' });
    columnChart(hourBox, { labels: hod.map((_, i) => `${i}h`), values: hod, fmt: (v) => `${Math.round(v)}`, tipLabel: 'plays', height: 140 });
  });
  return page;
}

async function libraryTab() {
  const st = await api('/api/admin/stats/library');
  const bucketList = (b, fmtKey = (k) => k) => barList((b || []).map((x) => ({ label: `${fmtKey(x.key)} (${x.count})`, value: x.value })), (v) => fmtBytes(v));
  return h('div', null,
    h('div', { class: 'stats-grid' },
      stat('Movies', st.movies.toLocaleString()),
      stat('Shows', st.shows.toLocaleString(), `${st.seasons} seasons · ${st.episodes.toLocaleString()} episodes`),
      stat('Files', st.files.toLocaleString(), `${st.unprobed} awaiting analysis${st.probeErrors ? ` · ${st.probeErrors} unreadable` : ''}`),
      stat('Total size', fmtBytes(st.totalBytes)),
      stat('Total runtime', `${Math.round(st.totalDuration / 3600).toLocaleString()} h`, `${(st.totalDuration / 86400).toFixed(1)} days of video`),
      stat('Metadata', `${st.metaMatched} matched`, `${st.metaMissing} not found · ${st.metaPending} pending`)),
    h('div', { class: 'info-grid' },
      h('div', { class: 'panel' }, h('h3', null, 'Libraries (size)'), bucketList(st.libraries)),
      h('div', { class: 'panel' }, h('h3', null, 'Video codecs (size)'), bucketList(st.videoCodecs, (k) => k.toUpperCase())),
      h('div', { class: 'panel' }, h('h3', null, 'Resolution (size)'), bucketList(st.resolutions)),
      h('div', { class: 'panel' }, h('h3', null, 'HDR (size)'), bucketList(st.hdr)),
      h('div', { class: 'panel' }, h('h3', null, 'Audio codecs (size)'), bucketList(st.audioCodecs, (k) => k.toUpperCase())),
      h('div', { class: 'panel' }, h('h3', null, 'Containers (size)'), bucketList(st.containers, (k) => k.toUpperCase()))));
}

async function historyTab(ctx) {
  const offset = +(ctx.query.get('offset') || 0);
  const rows = await api(`/api/admin/history?limit=100&offset=${offset}`);
  return h('div', null,
    h('div', { class: 'toolbar' }, h('span', { class: 'muted' }, `Showing ${offset + 1}–${offset + rows.length}`), h('div', { class: 'spacer' }),
      offset > 0 ? h('a', { class: 'btn sm', href: `#/dashboard/history?offset=${Math.max(0, offset - 100)}` }, 'Newer') : null,
      rows.length === 100 ? h('a', { class: 'btn sm', href: `#/dashboard/history?offset=${offset + 100}` }, 'Older') : null,
      h('button', { class: 'btn sm danger', onclick: async () => { if (await confirmDialog('Delete all playback history? Watch progress is kept.', 'Delete', true)) { await api('/api/admin/history', { method: 'DELETE' }); route(); } } }, 'Clear history')),
    rows.length ? h('div', { class: 'panel table-wrap' }, h('table', { class: 'tbl' },
      h('tr', null, ['When', 'User', 'Title', 'Method', 'Watched', 'Data', 'Stalls', 'Output', 'Client'].map((t) => h('th', null, t))),
      rows.map((r) => h('tr', null,
        h('td', { class: 'nowrap' }, fmtDate(r.startedAt)),
        h('td', null, r.userName),
        h('td', null, h('a', { href: `#/item/${r.itemId}` }, r.title)),
        h('td', { title: r.reasons }, h('span', { class: `method ${r.method}` }, r.method), r.reasons ? h('div', { class: 'dim small' }, r.reasons) : null),
        h('td', { class: 'nowrap' }, fmtDuration(r.watched) || `${Math.round(r.watched)}s`),
        h('td', { class: 'nowrap' }, fmtBytes(r.bytes)),
        h('td', null, r.bufferEvents ? `${r.bufferEvents} (${r.bufferSeconds.toFixed(0)}s)` : '0'),
        h('td', { class: 'small' }, [r.videoOut, r.audioOut].filter(Boolean).join(' / ')),
        h('td', { class: 'small' }, `${r.client}`, h('div', { class: 'dim mono' }, `${r.ip}${r.remote ? ' · remote' : ''}`))))))
      : h('div', { class: 'empty' }, h('h2', null, 'No playback history yet'), h('p', null, 'Plays are recorded when a session ends.')));
}
