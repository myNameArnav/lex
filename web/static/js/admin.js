import { h, clear, icons, toast, modal, confirmDialog, spinner, toggle, fmtBytes, fmtBitrate, fmtTime, fmtDate, timeAgo, fmtUptime, fmtDuration, LANG_OPTIONS, $, run, staleBanner, motionOK, KIND_LABELS, METHOD_LABEL, reasonLabel, emptyState } from './ui.js';
import { api, img } from './api.js';
import { state, loadLibraries, route, focusAfterRoute, refreshSoft } from './app.js';
import { prefs, DEFAULTS, QUALITIES } from './prefs.js';
import { capsSummary } from './caps.js';
import { lineChart, columnChart, barList } from './charts.js';

// Pollers owned by the current admin view. Each stops at its next tick once
// its view is no longer current (navigated away or re-rendered; a navigation
// cancelled by the leave guard keeps them running), and all stop when the
// session expires.
let pollers = [];
function stopTimers() { pollers.forEach((p) => clearInterval(p.t)); pollers = []; }
// every polls fn every ms while ctx is current. Ticks are skipped while the
// tab is hidden or a dialog is open (a re-render would pull the dialog's
// opener out from under it), and run at once when the tab is shown again.
// fn should throw (or reject) when its request fails: after two failures in
// a row onFail(lastOkAt) is called, and onRecover() after the next success.
function every(ctx, ms, fn, { onFail, onRecover } = {}) {
  let fails = 0, lastOk = Date.now(), busy = false;
  const p = {};
  p.tick = async () => {
    if (!ctx.isCurrent()) { clearInterval(p.t); pollers = pollers.filter((x) => x !== p); return; }
    if (busy || document.hidden || document.querySelector('dialog[open]')) return;
    busy = true;
    try {
      await fn();
      if (fails >= 2) onRecover?.();
      fails = 0;
      lastOk = Date.now();
    } catch {
      if (++fails === 2) onFail?.(lastOk);
    } finally {
      busy = false;
    }
  };
  p.t = setInterval(p.tick, ms);
  pollers.push(p);
  return p.t;
}
window.addEventListener('lex:unauthorized', stopTimers);
document.addEventListener('visibilitychange', () => { if (!document.hidden) pollers.forEach((p) => p.tick()); });

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

let toggleId = 0;
function toggleRow(label, help, checked, onchange) {
  const control = toggle(checked, onchange, label);
  const helpId = `toggle-help-${++toggleId}`;
  if (help) control.firstChild.setAttribute('aria-describedby', helpId);
  return h('label', { class: 'toggle-row' }, h('span', { class: 'lbl' }, h('b', null, label), help ? h('span', { class: 'help', id: helpId }, help) : null), control);
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
  return h('div', { class: 'form cols' },
    h('div', { class: 'form-section' }, h('h2', null, 'Quality & method'),
      h('p', { class: 'help', style: { margin: 0 } }, 'These settings are stored in this browser, so each device can have its own (e.g. lower quality on your phone).'),
      h('div', { class: 'form-grid' },
        field('Streaming quality', select(p.quality, QUALITIES, set('quality', Number)), 'Max bitrate. "Original" direct-plays when the browser supports the file.'),
        field('Playback method', select(p.mode, [['auto', 'Automatic (recommended)'], ['direct', 'Prefer Direct Play'], ['remux', 'Always Direct Stream (remux)'], ['transcode', 'Always Transcode']], set('mode'))))),
    h('div', { class: 'form-section' }, h('h2', null, 'Buffering'),
      h('div', { class: 'form-grid' },
        field('Buffer ahead', select(p.bufferAhead, [[0, 'Automatic (recommended)'], [30, 'At most 30 seconds'], [60, 'At most 1 minute'], [90, 'At most 90 seconds'], [180, 'At most 3 minutes'], [300, 'At most 5 minutes']], set('bufferAhead', Number)), 'For direct stream and transcode (direct play is always buffered by the browser). Automatic fills as much as the browser allows, usually about 150 MB. A limit keeps a transcode from running far ahead of what you watch.'),
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
      toggleRow('Single-key shortcuts', 'p, /, ? and g-then-key. Ctrl/⌘ K and the player keys always work.', p.shortcuts, set('shortcuts')),
      h('div', { class: 'form-grid' },
        field('Autoplay countdown', select(p.countdown, [[5, '5 seconds'], [10, '10 seconds'], [15, '15 seconds'], [30, '30 seconds']], set('countdown', Number))),
        field('Skip back', select(p.skipBack, [[5, '5 seconds'], [10, '10 seconds'], [15, '15 seconds'], [30, '30 seconds']], set('skipBack', Number))),
        field('Skip forward', select(p.skipFwd, [[10, '10 seconds'], [15, '15 seconds'], [30, '30 seconds'], [60, '60 seconds']], set('skipFwd', Number))))),
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
    h('div', null, h('button', { class: 'btn primary', onclick: async (e) => {
      if (n1.value !== n2.value) return toast('Passwords do not match', 'error');
      if (await run(e.currentTarget, () => api('/api/me/password', { method: 'PUT', body: { current: cur.value, new: n1.value } }), 'Password changed; other devices were signed out')) cur.value = n1.value = n2.value = '';
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
  const save = h('button', { class: 'btn primary', onclick: (e) => run(e.currentTarget, async () => {
    cfg = await api('/api/admin/config', { method: 'PUT', body: draft });
    Object.assign(draft, cfg);
    state.caps = { ...state.caps, subtitleSearch: !!cfg.openSubtitlesKey?.trim(), cacheEnabled: !!cfg.cacheEnabled };
  }, 'Settings saved', { busyLabel: 'Saving…' }) }, 'Save changes');
  return h('div', { class: 'form cols' }, build({ draft, num, text, tg, bind, info }), h('div', { class: 'save-bar' }, save));
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
        tg('enableDirectPlay', 'Direct Play', 'Send the original file untouched (range requests). Zero CPU.'),
        tg('enableRemux', 'Direct Stream (remux)', 'Repackage into fragmented MP4 without re-encoding video. Very low CPU; audio is converted to AAC only if needed.'),
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
          field('Minimum fragment duration (ms)', num('fragmentMs', { min: 200, max: 10000, step: 100 }), 'Fragments start at video keyframes. Direct stream uses the source file’s keyframes.'),
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
    h('div', { class: 'form-section' }, h('h2', null, 'Subtitle downloads'),
      h('p', { class: 'help', style: { margin: 0 } }, 'Search and download subtitles from the player (Subtitles → Search online). Needs a free API key: create an account at opensubtitles.com, then Profile → API consumers → New consumer. Username and password are optional but raise the daily download limit.'),
      field('OpenSubtitles API key', text('openSubtitlesKey', { type: 'password', autocomplete: 'off' })),
      h('div', { class: 'form-grid' },
        field('OpenSubtitles username', text('openSubtitlesUser', { autocomplete: 'off' })),
        field('OpenSubtitles password', text('openSubtitlesPass', { type: 'password', autocomplete: 'new-password' })))),
    h('div', { class: 'form-section' }, h('h2', null, 'Seek previews'),
      h('p', { class: 'help', style: { margin: 0 } }, 'Thumbnails shown while hovering or dragging the seek bar. Generated in the background from keyframes only, at the lowest priority, and paused during playback (except for files in the SSD cache). About 1–3 MB per movie.'),
      tg('trickplayEnabled', 'Generate seek previews'),
      h('div', { class: 'form-grid' },
        field('Interval (seconds)', num('trickplayInterval', { min: 2, max: 60 }), 'Changes apply to newly generated previews.'),
        field('Thumbnail width (px)', num('trickplayWidth', { min: 120, max: 480, step: 20 })))),
    h('div', { class: 'form-section' }, h('h2', null, 'Intro detection'),
      h('p', { class: 'help', style: { margin: 0 } }, 'Finds TV intros by comparing audio fingerprints of neighbouring episodes (like Jellyfin\'s Intro Skipper), or from chapters named Intro/Opening. Runs in the background at the lowest CPU priority after scans.'),
      tg('introDetect', 'Detect intros'),
      h('div', { class: 'form-grid' },
        field('Analyse the first (seconds)', num('introScanSecs', { min: 120, max: 1800, step: 30 }), 'Intros after cold opens can start a few minutes in.'),
        field('Shortest intro (seconds)', num('introMinSecs', { min: 5, max: 60 })),
        field('Longest intro (seconds)', num('introMaxSecs', { min: 20, max: 300 }))),
      h('div', null, h('button', { class: 'btn', onclick: (e) => run(e.currentTarget, () => api('/api/admin/intro/scan', { method: 'POST' }), 'Intro detection started') }, 'Detect intros now'))),
    h('div', { class: 'form-section' }, h('h2', null, 'Local'),
      tg('useLocalMetadata', 'Use local artwork & hints', 'poster.jpg / fanart.jpg / *-thumb.jpg, .plexmatch and .nfo ids.'),
      tg('generateThumbs', 'Generate missing episode thumbnails', 'Grabs one frame with ffmpeg when no still is available (cached).')),
    h('div', { class: 'form-section' }, h('h2', null, 'Refresh'),
      h('div', { class: 'row wrap' },
        h('button', { class: 'btn', onclick: (e) => run(e.currentTarget, () => api('/api/admin/metadata/refresh', { method: 'POST', body: { missingOnly: true } }), 'Retrying unmatched items') }, 'Retry unmatched items'),
        h('button', { class: 'btn', onclick: async (e) => {
          const btn = e.currentTarget;
          if (await confirmDialog('Re-fetch metadata for every item? Manual matches are kept.')) await run(btn, () => api('/api/admin/metadata/refresh', { method: 'POST', body: {} }), 'Refreshing all metadata in background');
        } }, 'Refresh all metadata'))),
  ]);
}

// ---------- libraries ----------
async function librariesSection() {
  const libs = await api('/api/admin/libraries');
  const box = h('div', { class: 'form' });
  box.appendChild(h('div', { class: 'row' }, h('div', { class: 'spacer' }),
    h('button', { class: 'btn', onclick: (e) => run(e.currentTarget, () => api('/api/admin/scan', { method: 'POST', body: {} }), 'Scanning all libraries') }, h('span', { html: icons.refresh }), 'Scan all'),
    h('button', { class: 'btn primary', onclick: () => libraryModal() }, h('span', { html: icons.plus }), 'Add library')));
  if (!libs.length) box.appendChild(h('div', { class: 'empty' }, h('h2', null, 'No libraries'), h('p', null, 'Add a folder containing movies or TV shows.')));
  for (const l of libs) {
    box.appendChild(h('div', { class: 'form-section' },
      h('div', { class: 'row' }, h('h2', { class: 'grow' }, l.name), h('span', { class: 'dim' }, `${KIND_LABELS[l.kind] || l.kind} · ${l.itemCount} items · scanned ${timeAgo(l.lastScan)}`)),
      h('div', { class: 'chips' }, l.paths.map((p) => h('span', { class: 'chip mono', style: { paddingRight: '12px' } }, p))),
      h('div', { class: 'row' },
        h('button', { class: 'btn sm', onclick: (e) => run(e.currentTarget, () => api('/api/admin/scan', { method: 'POST', body: { libraryId: l.id } }), `Scanning ${l.name}`) }, 'Scan'),
        h('button', { class: 'btn sm', onclick: () => libraryModal(l) }, 'Edit'),
        h('button', { class: 'btn sm danger', onclick: async (e) => {
          const btn = e.currentTarget;
          if (!(await confirmDialog(`Remove library "${l.name}"? Files on disk are not touched; watch history for its items is removed.`, 'Remove', true))) return;
          if (!(await run(btn, () => api(`/api/admin/libraries/${l.id}`, { method: 'DELETE' })))) return;
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
  const m = modal({ title: lib ? `Edit ${lib.name}` : 'Add library', wide: true, dismissible: false, body: [
    field('Name', name),
    field('Type', select(kind, [['movies', 'Movies'], ['shows', 'TV Shows'], ['mixed', 'Mixed (auto-detect)']], (v) => { kind = v; }), lib ? 'Changing the type rebuilds the library (watch progress for its items resets).' : null),
    field('Folders', chips),
    browser,
  ], actions: [
    h('button', { class: 'btn', onclick: () => m.close() }, 'Cancel'),
    h('button', { class: 'btn primary', onclick: async (e) => {
      const body = { name: name.value || 'Library', kind, paths };
      const ok = await run(e.currentTarget, async () => {
        if (lib) await api(`/api/admin/libraries/${lib.id}`, { method: 'PUT', body });
        else await api('/api/admin/libraries', { method: 'POST', body });
        m.close();
      }, lib ? 'Library updated; rescanning' : 'Library added; scanning now', { busyLabel: lib ? 'Saving…' : 'Adding…' });
      if (ok) { await loadLibraries(); route(); }
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
        h('button', { class: 'btn sm', onclick: (e) => {
          const pw = prompt(`New password for ${u.name}:`);
          if (pw) run(e.currentTarget, () => api(`/api/admin/users/${u.id}`, { method: 'PUT', body: { password: pw } }), 'Password reset');
        } }, 'Reset password'),
        h('button', { class: 'btn sm', onclick: async (e) => {
          if (await run(e.currentTarget, () => api(`/api/admin/users/${u.id}`, { method: 'PUT', body: { isAdmin: !u.isAdmin } }))) route();
        } }, u.isAdmin ? 'Make user' : 'Make admin'),
        u.id !== state.me.id ? h('button', { class: 'btn sm danger', onclick: async (e) => {
          const btn = e.currentTarget;
          if (await confirmDialog(`Delete user ${u.name}?`, 'Delete', true) && await run(btn, () => api(`/api/admin/users/${u.id}`, { method: 'DELETE' }))) route();
        } }, 'Delete') : null)))));
  const name = h('input', { type: 'text', placeholder: 'Username' });
  const pw = h('input', { type: 'password', placeholder: 'Password (12–72 bytes)', autocomplete: 'new-password' });
  let admin = false;
  return h('div', { class: 'form' },
    h('div', { class: 'form-section' }, h('h2', null, 'Users'), h('div', { class: 'table-wrap' }, tbl)),
    h('div', { class: 'form-section' }, h('h2', null, 'Add user'),
      h('div', { class: 'form-grid' }, name, pw),
      toggleRow('Administrator', 'Can change settings, libraries and see stats.', false, (v) => { admin = v; }),
      h('div', null, h('button', { class: 'btn primary', onclick: async (e) => {
        if (await run(e.currentTarget, () => api('/api/admin/users', { method: 'POST', body: { name: name.value, password: pw.value, isAdmin: admin } }), 'User created')) route();
      } }, 'Create user'))));
}

async function devicesSection() {
  const devs = await api('/api/admin/devices');
  return h('div', { class: 'form' }, h('div', { class: 'form-section' }, h('h2', null, 'Signed-in devices'),
    h('div', { class: 'table-wrap' }, h('table', { class: 'tbl' }, h('tr', null, h('th', null, 'User'), h('th', null, 'Client'), h('th', null, 'IP'), h('th', null, 'Signed in'), h('th', null, 'Last seen'), h('th')),
      devs.map((d) => h('tr', null, h('td', null, d.userName), h('td', null, d.client), h('td', { class: 'mono' }, d.ip), h('td', null, fmtDate(d.created)), h('td', null, timeAgo(d.lastSeen)),
        h('td', null, h('button', { class: 'btn sm danger', onclick: async (e) => { if (await run(e.currentTarget, () => api(`/api/admin/devices/${d.prefix}`, { method: 'DELETE' }))) route(); } }, 'Sign out'))))))));
}

function taskRow(label, running, text) {
  return h('div', { class: 'task' }, running ? h('div', { class: 'spinner sm' }) : h('span', { class: 'good', html: icons.check, style: { width: '16px' } }), h('b', null, label), text);
}

// ---------- SSD cache ----------
async function cacheSection(ctx) {
  const statusBox = h('div', { class: 'form-section' });
  const listBox = h('div', { class: 'form-section' });
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
    clear(listBox).append(h('div', { class: 'row' }, h('h2', { class: 'grow' }, 'Cached files'),
      r.entries.length ? h('button', { class: 'btn sm danger', onclick: async (e) => {
        const btn = e.currentTarget;
        if (await confirmDialog('Delete every cached copy? Originals are not touched.', 'Clear', true) && await run(btn, () => api('/api/admin/cache/clear', { method: 'POST', body: { kind: 'media' } }))) refresh();
      } }, 'Clear cache') : null),
    r.entries.length ? h('div', { class: 'table-wrap' }, h('table', { class: 'tbl' },
      h('tr', null, ['Title', 'Size', 'Cached', 'Last used', 'Reason', ''].map((t) => h('th', null, t))),
      r.entries.map((e) => h('tr', null, h('td', null, h('a', { href: `#/item/${e.itemId}` }, e.title), h('div', { class: 'dim small ellipsis', style: { maxWidth: '380px' } }, e.name)), h('td', { class: 'nowrap' }, fmtBytes(e.size)), h('td', { class: 'nowrap' }, timeAgo(e.addedAt)), h('td', { class: 'nowrap' }, timeAgo(e.lastAccess)), h('td', null, e.reason),
        h('td', null, h('button', { class: 'btn sm', onclick: async (ev) => { if (await run(ev.currentTarget, () => api(`/api/admin/cache/items/${e.itemId}`, { method: 'DELETE' }))) refresh(); } }, 'Remove'))))))
      : h('p', { class: 'muted', style: { margin: 0 } }, 'Nothing cached yet. Files are copied when played (and upcoming episodes are prefetched).'));
  };
  const refresh = () => render().catch(() => {});
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
  const page = h('div', { class: 'form' }, form, statusBox, listBox);
  const banner = staleBanner(page);
  every(ctx, 2000, render, banner.hooks());
  return h('div', null, banner.el, page);
}

// ---------- tasks & logs ----------
async function logsSection(ctx) {
  const tasks = h('div', { class: 'form-section' });
  const logBox = h('div', { class: 'logs' });
  let follow = true;
  logBox.addEventListener('scroll', () => { follow = logBox.scrollTop + logBox.clientHeight >= logBox.scrollHeight - 20; });
  const renderTasks = async () => {
    const t = await api('/api/admin/tasks');
    if (!ctx.isCurrent()) return;
    const s = t.scan, m = t.metadata;
    clear(tasks).append(h('h2', null, 'Background tasks'),
      taskRow('Library scan', s.running, s.running ? `${s.phase}${s.library ? ' · ' + s.library : ''}${s.phase === 'probing' ? ` · ${s.probeDone}/${s.probeTotal} ${s.current}` : ''}` : `idle · last finished ${timeAgo(s.finishedAt)} (found ${s.found}, +${s.added}, −${s.removed}, changed ${s.changed})`),
      taskRow('Metadata', m.running, m.running ? `${m.done}/${m.total} · ${m.current}` : `idle · last run ${timeAgo(m.lastRun)} (${m.matched} matched, ${m.missing} not found)`),
      taskRow('Intro detection', t.intro.running, !t.intro.available ? 'unavailable (ffmpeg without chromaprint)' : t.intro.running ? `${t.intro.done}/${t.intro.seasons} seasons · ${t.intro.current} · ${t.intro.found} found` : `idle · last run ${timeAgo(t.intro.lastRun)}`),
      taskRow('SSD cache', !!t.cache.current, !t.cache.enabled ? 'disabled' : t.cache.current ? `copying ${t.cache.current.name} · ${Math.round((t.cache.current.done / t.cache.current.size) * 100)}% · ${fmtBytes(t.cache.current.speed)}/s${t.cache.queued.length ? ` · ${t.cache.queued.length} queued` : ''}` : `idle · ${t.cache.files} files, ${fmtBytes(t.cache.usedBytes)} of ${fmtBytes(t.cache.maxBytes)}`),
      t.trickplay ? taskRow('Seek previews', t.trickplay.running, !t.trickplay.enabled ? 'disabled' : t.trickplay.running ? `generating ${t.trickplay.current} · ${t.trickplay.pending} left` : t.trickplay.paused ? `paused while something is playing · ${t.trickplay.pending} left` : t.trickplay.pending ? `${t.trickplay.pending} waiting` : `idle · all done${t.trickplay.failed ? ` (${t.trickplay.failed} failed)` : ''}`) : null,
      h('div', { class: 'task' }, h('b', null, 'ffmpeg jobs'), `${t.remuxJobs} remux · ${t.transcodeJobs} transcode`),
      h('div', { class: 'row' }, h('button', { class: 'btn sm', onclick: async (e) => { if (await run(e.currentTarget, () => api('/api/admin/scan', { method: 'POST', body: {} }))) renderTasks().catch(() => {}); } }, 'Scan now')));
  };
  const renderLogs = async () => {
    const lines = await api('/api/admin/logs');
    if (!ctx.isCurrent()) return;
    clear(logBox).append(...lines.map((l) => h('div', { class: l.level }, `${new Date(l.t).toLocaleTimeString()} ${l.level.padEnd(5)} ${l.msg}`)));
    if (follow) logBox.scrollTop = logBox.scrollHeight;
  };
  await renderTasks();
  await renderLogs();
  const page = h('div', { class: 'form', style: { maxWidth: 'none' } }, tasks, h('div', { class: 'form-section' }, h('h2', null, 'Log'), logBox));
  const banner = staleBanner(page);
  every(ctx, 2000, renderTasks, banner.hooks('tasks'));
  every(ctx, 3000, renderLogs, banner.hooks('logs'));
  return h('div', null, banner.el, page);
}

async function aboutSection() {
  const i = await api('/api/admin/info');
  const f = i.ffmpeg;
  return h('div', { class: 'form' },
    h('div', { class: 'form-section' }, h('h2', null, 'AI disclosure'),
      h('p', null, 'Lex was built by large language models: LLM coding agents wrote its code, interface, tests and documentation under human direction. LLM-written code can contain mistakes, including security ones, and has not had an independent security review. Lex does not call AI model services at runtime.')),
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
      h('div', { class: 'toggle-row' }, h('div', { class: 'lbl' }, h('b', null, 'Artwork'), h('span', { class: 'help' }, `${fmtBytes(i.imageCache)} — re-downloaded on demand`)), h('button', { class: 'btn sm', onclick: async (e) => { if (await run(e.currentTarget, () => api('/api/admin/cache/clear', { method: 'POST', body: { kind: 'images' } }), 'Artwork cache cleared')) route(); } }, 'Clear')),
      h('div', { class: 'toggle-row' }, h('div', { class: 'lbl' }, h('b', null, 'Subtitles'), h('span', { class: 'help' }, `${fmtBytes(i.subsCache)} — extracted WebVTT`)), h('button', { class: 'btn sm', onclick: async (e) => { if (await run(e.currentTarget, () => api('/api/admin/cache/clear', { method: 'POST', body: { kind: 'subs' } }), 'Subtitle cache cleared')) route(); } }, 'Clear'))));
}

// ======================================================================
// Dashboard
// ======================================================================

const DASH = [['live', 'Live'], ['playback', 'Playback'], ['library', 'Library']];

export async function dashboardView(ctx, tab) {
  if (!state.me.isAdmin) return emptyState({ title: 'Admins only', text: 'Ask the server owner for access.', actions: [h('a', { class: 'btn primary', href: '#/' }, 'Go home')] });
  if (tab === 'history') tab = 'playback'; // merged into Playback
  if (!DASH.some(([id]) => id === tab)) tab = 'live';
  const content = await ({ live: liveTab, playback: playbackTab, library: libraryTab })[tab](ctx);
  return h('div', { class: 'page' },
    h('h1', { class: 'page-title' }, 'Dashboard'),
    // Links, so they open in a new tab; the next view keeps focus on the tab.
    h('nav', { class: 'tabs', 'aria-label': 'Dashboard sections', style: { marginTop: '14px' } }, DASH.map(([id, label]) => h('a', {
      href: `#/dashboard/${id}`, class: id === tab ? 'active' : null, 'aria-current': id === tab ? 'page' : null,
      dataset: { focusKey: `dash-tab-${id}` },
      onclick: (e) => { if (id !== tab && !e.ctrlKey && !e.metaKey && !e.shiftKey) focusAfterRoute(`dash-tab-${id}`); },
    }, label))),
    content);
}

function stat(label, value, sub, extra) {
  return h('div', { class: 'stat' }, h('div', { class: 'l' }, label), h('div', { class: 'v' }, value), sub != null ? h('div', { class: 'sub' }, sub) : null, extra);
}

function meter(pct, hotAt = 85) {
  return h('div', { class: 'meter' }, h('i', { class: pct >= hotAt ? 'hot' : '', style: { width: `${Math.min(100, Math.max(0, pct))}%` } }));
}

const bps = (bytesPerSec) => fmtBitrate(bytesPerSec * 8);
// Polled views update text in place: an unchanged node keeps any selection.
const setText = (el, v) => { if (el.textContent !== v) el.textContent = v; };

// Live: what's playing first, then server health (each card with its own
// last-hour chart), then storage, then one line about the host. Polls update
// the cards in place so focus, selection and open details survive.
async function liveTab(ctx) {
  const sessionsBox = h('div', { class: 'sessions' });
  const nowTitle = h('h2', { class: 'section-title', tabindex: '-1' }, 'Now playing');
  const mkCard = (label) => {
    const c = { label, v: h('span', { class: 'v' }), sub: h('div', { class: 'sub' }), extra: h('div', { class: 'dcard-extra' }), chart: h('div', { class: 'dchart' }) };
    c.el = h('div', { class: 'dcard' }, h('div', { class: 'dcard-head' }, h('span', { class: 'l' }, label), c.v), c.sub, c.extra, c.chart);
    return c;
  };
  const cards = { cpu: mkCard('CPU'), mem: mkCard('Memory'), temp: mkCard('Temperature'), net: mkCard('Network out') };
  const storage = h('div', { class: 'panel' });
  const host = h('div', { class: 'dash-host' });
  // Element.append would print null and stringify arrays; flatten like h().
  const put = (el, ...kids) => clear(el).append(...kids.flat(Infinity).filter((k) => k != null && k !== false));
  const card = (c, value, sub, ...rest) => {
    setText(c.v, value);
    if (typeof sub === 'string') setText(c.sub, sub); else put(c.sub, sub);
    put(c.extra, rest);
  };
  let hist = [], haveSystem = false;

  const renderSystem = async (withHistory) => {
    // Until a first load succeeds, keep asking for the history too.
    withHistory ||= !haveSystem;
    const r = await api(`/api/admin/stats/system${withHistory ? '?history=1' : ''}`);
    if (!ctx.isCurrent()) return;
    haveSystem = true;
    const s = r.snapshot, n = s.now;
    if (withHistory) hist = r.history || [];
    else { hist.push(n); if (hist.length > 1200) hist.shift(); }
    const memPct = s.memTotal ? (s.memUsed / s.memTotal) * 100 : 0;
    const flags = s.throttleFlags?.length ? s.throttleFlags.join(', ') : '';
    card(cards.cpu, `${n.cpu.toFixed(0)}%`,
      `load ${s.load.map((x) => x.toFixed(2)).join(' · ')}${s.freqMhz ? ` · ${Math.round(s.freqMhz)} MHz` : ''}`,
      h('div', { class: 'cores' }, (s.cores || []).map((c, i) => h('div', { class: 'core', title: `Core ${i}: ${c.toFixed(0)}%` }, h('i', { style: { height: `${c}%` } }), h('span', null, `${c.toFixed(0)}`)))));
    card(cards.mem, `${memPct.toFixed(0)}%`,
      `${fmtBytes(s.memUsed)} of ${fmtBytes(s.memTotal)}${s.swapTotal ? ` · swap ${fmtBytes(s.swapUsed)}` : ''}`,
      h('div', { class: 'small dim' }, `Lex uses ${fmtBytes(s.procRss)} (Go heap ${fmtBytes(s.goHeap)}) · ${s.procCpu.toFixed(1)}% CPU`));
    // Hosts without a sensor (VMs, containers on a laptop) get no card.
    const hasTemp = !!n.temp || hist.some((p) => p.temp);
    cards.temp.el.hidden = !hasTemp;
    if (hasTemp) {
      card(cards.temp, n.temp ? `${n.temp.toFixed(0)}°C` : '—',
        flags ? h('span', { class: 'warn' }, flags) : s.throttled ? 'No throttling' : '');
    }
    card(cards.net, bps(n.tx), `in ${bps(n.rx)} · streaming ${bps(n.stream)}`);
    const times = hist.map((p) => p.t);
    if (times.length > 1) {
      const pct = (v) => `${v.toFixed(0)}%`;
      lineChart(cards.cpu.chart, { title: 'CPU over the last hour', times, height: 96, max: 100, ticks: 2, fmt: pct, series: [{ name: 'Host', values: hist.map((p) => p.cpu) }, { name: 'Lex', values: hist.map((p) => p.pcpu / Math.max(1, s.numCpu)) }] });
      lineChart(cards.mem.chart, { title: 'Memory over the last hour', times, height: 96, max: 100, ticks: 2, fmt: pct, series: [{ name: 'Used', values: hist.map((p) => p.mem) }] });
      if (hasTemp) lineChart(cards.temp.chart, { title: 'Temperature over the last hour', times, height: 96, max: 90, fmt: (v) => `${v.toFixed(0)}°`, series: [{ name: 'SoC', values: hist.map((p) => p.temp) }] });
      // In bits per second so the gridlines land on round rates; an idle
      // server still gets a 0–1.5 Mbps scale.
      lineChart(cards.net.chart, { title: 'Network out over the last hour', times, height: 96, floor: 1.5e6, fmt: fmtBitrate, series: [{ name: 'Out', values: hist.map((p) => p.tx * 8) }, { name: 'Streaming', values: hist.map((p) => p.stream * 8) }] });
    }
    // Storage: disks, SSD cache and current disk activity in one place. A
    // long path wraps its note underneath instead of widening the page.
    const row = (name, used, total, hot, note) => h('div', { class: 'srow' },
      h('div', { class: 'row wrap small' }, name, h('span', { class: 'muted' }, note || `${fmtBytes(used)} of ${fmtBytes(total)} · ${fmtBytes(Math.max(0, total - used))} free`)),
      meter(total ? (used / total) * 100 : 0, hot));
    const c = r.cache;
    const pathSpan = (text, mono) => h('span', { class: `grow ellipsis${mono ? ' mono' : ''}`, title: text, style: { minWidth: 0, flex: '1 1 140px' } }, text);
    put(storage,
      h('div', { class: 'row wrap', style: { marginBottom: '10px', rowGap: '2px' } }, h('h3', { class: 'grow', style: { margin: 0 } }, 'Storage'),
        h('span', { class: 'small muted' }, `read ${fmtBytes(n.dr)}/s · write ${fmtBytes(n.dw)}/s`)),
      (s.disks || []).map((d) => row(pathSpan(d.path, true), d.used, d.total, 92)),
      c?.enabled ? row(pathSpan('SSD cache'), c.usedBytes, c.maxBytes, 97,
        `${fmtBytes(c.usedBytes)} of ${fmtBytes(c.maxBytes)} · ${c.files} files${c.current ? ` · copying ${Math.round((c.current.done / c.current.size) * 100)}%` : ''}`) : null);
    host.textContent = [s.model || s.os, s.arch, `${s.numCpu} cores`, `up ${fmtUptime(s.uptime)}`,
      `${r.remuxJobs} remux · ${r.transcodeJobs} transcode jobs`].filter(Boolean).join('  ·  ');
  };

  const cardsById = new Map();
  // A stopped card goes away; focus moves to the list heading, not <body>.
  const refreshAfterStop = async () => {
    await renderSessions().catch(() => {});
    if (!document.activeElement || document.activeElement === document.body) nowTitle.focus({ preventScroll: true });
  };
  const nothing = h('div', { class: 'panel dim' }, 'Nothing is playing right now.');
  const renderSessions = async () => {
    const list = await api('/api/admin/stats/sessions');
    if (!ctx.isCurrent()) return;
    nowTitle.textContent = list.length ? `Now playing · ${list.length}` : 'Now playing';
    list.sort((a, b) => b.startedAt - a.startedAt || (a.id < b.id ? -1 : 1));
    const keys = new Set();
    const want = list.map((s) => {
      const key = `${s.id}:${s.itemId}`;
      keys.add(key);
      let card = cardsById.get(key);
      if (!card) cardsById.set(key, (card = sessionCard(s, refreshAfterStop)));
      card.update(s);
      return card.el;
    });
    for (const k of [...cardsById.keys()]) if (!keys.has(k)) cardsById.delete(k);
    if (!want.length) want.push(nothing);
    // Remove and insert only what changed: moving a card would drop focus
    // inside it.
    for (const el of [...sessionsBox.children]) if (!want.includes(el)) el.remove();
    want.forEach((el, i) => { if (sessionsBox.children[i] !== el) sessionsBox.insertBefore(el, sessionsBox.children[i] || null); });
  };

  // The first load failing leaves labelled placeholders, not empty boxes.
  const unavailable = () => {
    for (const k of ['cpu', 'mem', 'net']) card(cards[k], '—', 'Unavailable');
    cards.temp.el.hidden = true;
    put(storage, h('h3', { style: { margin: 0 } }, 'Storage'), h('div', { class: 'dim small', style: { marginTop: '10px' } }, 'Unavailable'));
  };
  const [sysOk, sessOk] = await Promise.all([renderSystem(true).then(() => true, () => false), renderSessions().then(() => true, () => false)]);
  if (!sysOk) unavailable();
  if (!sessOk) put(sessionsBox, h('div', { class: 'panel dim' }, 'Unavailable'));
  const live = h('div', { class: 'dash-live' },
    h('section', null, h('div', { class: 'dash-head' }, nowTitle), sessionsBox),
    h('section', null, h('div', { class: 'dash-head' }, h('h2', { class: 'section-title' }, 'Server'), h('span', { class: 'muted small' }, 'last hour')),
      h('div', { class: 'dcards' }, cards.cpu.el, cards.mem.el, cards.temp.el, cards.net.el)),
    h('section', null, storage),
    host);
  const banner = staleBanner(live);
  if (!sysOk) banner.fail(null, 'system');
  if (!sessOk) banner.fail(null, 'sessions');
  // Every success clears that poller's failure, including a failed first load.
  const poll = (ms, key, fn) => every(ctx, ms, async () => { await fn(); banner.ok(key); }, { onFail: (t) => banner.fail(t, key) });
  poll(3000, 'system', () => renderSystem(false));
  poll(2000, 'sessions', renderSessions);
  return [banner.el, live];
}

// sessionCard builds one Now playing card; update(s) refreshes it in place.
function sessionCard(s0, refresh) {
  let s = s0;
  const href = `#/item/${s.itemId}`;
  const title = h('a', { href });
  const sub = h('div', { class: 'dim small ellipsis' });
  const method = h('span');
  const status = h('span', { class: 'muted nowrap' });
  const bar = meter(0, 101);
  const reasons = h('div', { class: 'small muted', style: { marginTop: '6px' } });
  const health = ['Buffer', 'Delivery', 'Stalls', 'Read from'].map((k) => [k, h('b')]);
  const facts = ['Video', 'Audio', 'Bitrate', 'Client', 'Picture', 'ffmpeg'].map((k) => {
    const dt = h('dt', null, k), dd = h('dd');
    return { k, dt, dd };
  });
  const summary = h('summary', null, 'Details');
  const stop = h('button', { class: 'btn sm danger', onclick: async (e) => {
    const btn = e.currentTarget;
    const { userName, title: t, id } = s;
    if (!(await confirmDialog(`Stop ${userName}'s stream of ${t}? Their player will show that playback was stopped.`, 'Stop', true, 'Stop stream?'))) return;
    await run(btn, () => api(`/api/admin/sessions/${id}`, { method: 'DELETE' }).catch((err) => {
      throw err.status === 404 ? new Error('That stream had already ended') : err;
    }), `Stopped ${userName}'s stream`);
    refresh();
  } }, 'Stop');
  const el = h('div', { class: 'session' },
    // The title link names the item; the poster is a duplicate pointer target.
    h('a', { class: 'poster', href, tabindex: '-1', 'aria-hidden': 'true' }, h('img', { src: img({ id: s.itemId }, 'poster', 160), alt: '' })),
    h('div', { style: { minWidth: 0 } },
      h('div', { class: 'row session-head' },
        h('div', { class: 'grow', style: { minWidth: 0 } }, h('h3', { class: 'ellipsis' }, title), sub),
        method, stop),
      h('div', { class: 'row small', style: { marginTop: '10px' } }, status, h('div', { class: 'grow' }, bar)),
      reasons,
      h('div', { class: 'health' }, health.map(([k, b]) => h('div', null, h('span', null, k), b))),
      h('details', null, summary, h('dl', { class: 'kv' }, facts.map((f) => [f.dt, f.dd])))));
  const update = (next) => {
    s = next;
    const c = s.clientStats || {};
    const j = s.job;
    setText(title, s.title);
    setText(sub, [s.subtitle, s.userName, s.client, s.remote ? 'remote' : 'local'].filter(Boolean).join(' · '));
    method.className = `method ${s.method}`;
    method.title = s.reasons?.map(reasonLabel).join('; ') || '';
    setText(method, METHOD_LABEL[s.method] || s.method);
    stop.setAttribute('aria-label', `Stop ${s.userName}'s stream of ${s.title}`);
    summary.setAttribute('aria-label', `Details for ${s.title}`);
    setText(status, `${s.paused ? 'Paused' : 'Playing'} · ${fmtTime(s.position)} / ${fmtTime(s.duration)}`);
    bar.firstChild.style.width = `${s.duration ? Math.min(100, (s.position / s.duration) * 100) : 0}%`;
    reasons.hidden = !s.reasons?.length;
    setText(reasons, s.reasons?.length ? `Why ${s.method === 'transcode' ? 'transcoding' : 'converting'}: ${s.reasons.map(reasonLabel).join(' · ')}` : '');
    const buf = c.bufferAhead || 0;
    [
      [`${buf.toFixed(0)}s`, buf < 3 ? 'bad' : buf < 10 ? 'warn' : ''],
      [bps(s.rate), ''],
      [c.bufferEvents ? `${c.bufferEvents} · ${(c.bufferSeconds || 0).toFixed(0)}s` : 'none', c.bufferEvents ? 'warn' : ''],
      [s.cached ? 'SSD cache' : 'Library disk', ''],
    ].forEach(([v, cls], i) => { const b = health[i][1]; setText(b, v); b.className = cls; });
    const vals = {
      Video: `${s.videoIn || '?'} → ${s.videoOut || ''}`,
      Audio: `${s.audioIn || '?'} → ${s.audioOut || ''}`,
      Bitrate: `${fmtBitrate((s.srcBitrate || 0) * 1000)} source${s.method === 'transcode' ? ` → ${fmtBitrate((s.outBitrate || 0) * 1000)}` : ''} · ${fmtBytes(s.bytes)} sent`,
      Client: `${s.client} · ${s.ip}${s.remote ? ' (remote)' : ' (local)'} · ${c.bandwidth ? fmtBitrate(c.bandwidth) : '—'} download`,
      Picture: `${c.resolution || '—'}${c.totalFrames ? ` · ${c.droppedFrames} of ${c.totalFrames} frames dropped` : ''}`,
      ffmpeg: j ? (j.exited ? (j.error ? `exited: ${j.error}` : 'finished') : `${j.throttled ? 'waiting (client buffer full)' : `${(j.speed || 0).toFixed(2)}x · ${Math.round(j.fps || 0)} fps`} · CPU ${Math.round(j.cpu || 0)}% · ${s.restarts} restart${s.restarts === 1 ? '' : 's'}`) : null,
    };
    for (const f of facts) {
      const v = vals[f.k];
      f.dt.hidden = f.dd.hidden = v == null;
      setText(f.dd, v ?? '');
    }
  };
  return { el, update };
}

const RANGES = [7, 30, 90, 365];
const dayKey = (dt) => `${dt.getFullYear()}-${String(dt.getMonth() + 1).padStart(2, '0')}-${String(dt.getDate()).padStart(2, '0')}`;

async function playbackTab(ctx) {
  const days = Math.max(1, Math.min(3650, Math.round(+ctx.query.get('days')) || 30));
  const [st, history] = await Promise.all([
    api(`/api/admin/stats/playback?days=${days}&tz=${-new Date().getTimezoneOffset()}`),
    historySection(ctx, days),
  ]);
  const range = h('div', { class: 'toolbar' },
    h('div', { class: 'row wrap range-group', role: 'group', 'aria-label': 'Range' },
      h('span', { class: 'muted', 'aria-hidden': 'true' }, 'Range'),
      RANGES.map((d) => h('button', {
        class: `btn sm${d === days ? ' primary' : ''}`, 'aria-pressed': String(d === days), dataset: { focusKey: `range-${d}` },
        onclick: () => { if (d === days) return; focusAfterRoute(`range-${d}`); location.hash = `#/dashboard/playback?days=${d}`; },
      }, `${d} days`))),
    h('div', { class: 'spacer' }),
    st.plays ? h('button', { class: 'btn sm ghost', onclick: () => {
      const sec = document.getElementById('history');
      sec?.scrollIntoView({ behavior: motionOK() ? 'smooth' : 'auto' });
      sec?.querySelector('h2')?.focus({ preventScroll: true });
    } }, 'Jump to history') : null);
  // Paging through history keeps you at the table (Back restores its own
  // scroll). Pager links always carry offset, so Newer to the first page too.
  if (ctx.query.has('offset') && !ctx.restore) requestAnimationFrame(() => document.getElementById('history')?.scrollIntoView());
  if (!st.plays) {
    return h('div', null, range,
      emptyState({ level: 'h2', title: `No plays in the last ${days} days`, text: 'Plays are recorded when a session ends.' }),
      history);
  }

  // Fill missing days so the chart has a continuous axis; long ranges are
  // summed into weeks (starting Monday) so the bars stay readable.
  const byDay = Object.fromEntries((st.days || []).map((d) => [d.day, d]));
  const weekly = days > 120;
  const labels = [], full = [], hours = [], plays = [];
  let week = '';
  for (let i = days - 1; i >= 0; i--) {
    const dt = new Date();
    dt.setHours(12, 0, 0, 0);
    dt.setDate(dt.getDate() - i);
    const d = byDay[dayKey(dt)];
    if (weekly) {
      const mon = new Date(dt);
      mon.setDate(dt.getDate() - ((dt.getDay() + 6) % 7));
      if (dayKey(mon) !== week) {
        week = dayKey(mon);
        labels.push(mon.toLocaleDateString(undefined, { month: 'short', day: 'numeric' }));
        full.push(`Week of ${mon.toLocaleDateString(undefined, { dateStyle: 'medium' })}`);
        hours.push(0); plays.push(0);
      }
      hours[hours.length - 1] += d?.hours || 0;
      plays[plays.length - 1] += d?.plays || 0;
    } else {
      labels.push(dt.toLocaleDateString(undefined, { month: 'short', day: 'numeric' }));
      full.push(dt.toLocaleDateString(undefined, { dateStyle: 'medium' }));
      hours.push(d?.hours || 0);
      plays.push(d?.plays || 0);
    }
  }
  const hoursTitle = weekly ? 'Hours watched per week' : 'Hours watched per day';
  const hoursBox = h('div'), hourBox = h('div');
  const hod = Array.from({ length: 24 }, (_, i) => (st.hoursOfDay || []).find((b) => +b.key === i)?.count || 0);
  const hourName = (i) => new Date(2000, 0, 1, i).toLocaleTimeString([], { hour: 'numeric' });
  const fmtH = (v) => (v > 0 && Math.round(v * 60) < 60 ? `${Math.round(v * 60) || '<1'}m` : `${+v.toFixed(1)}h`);
  const page = h('div', null, range,
    h('div', { class: 'stats-grid' },
      stat('Plays', st.plays.toLocaleString(), `${st.uniqueItems} titles · ${Math.round((st.remotePlays / st.plays) * 100)}% remote`),
      stat('Watch time', fmtHours(st.hours), `${fmtHours(st.hours / Math.max(1, days))} a day`),
      stat('Data served', fmtBytes(st.bytes), `${fmtBytes(st.bytes / st.plays)} per play`),
      stat('Buffering', st.hours ? `${((st.bufferSeconds / (st.hours * 3600)) * 100).toFixed(2)}%` : '—', `of watch time · ${st.bufferEvents} stalls, ${st.bufferSeconds.toFixed(0)}s`)),
    h('div', { class: 'dash-2' },
      h('div', { class: 'panel' }, h('h2', { class: 'panel-title' }, hoursTitle), hoursBox,
        dataTable([weekly ? 'Week' : 'Day', 'Hours', 'Plays'], full.map((l, i) => (plays[i] ? [l, fmtHours(hours[i]), plays[i]] : null)), weekly ? 'Weeks without plays are left out.' : 'Days without plays are left out.')),
      h('div', { class: 'panel' }, h('h2', { class: 'panel-title' }, 'Plays by hour of day'), hourBox,
        dataTable(['Hour', 'Plays'], hod.map((v, i) => (v ? [hourName(i), v] : null)), 'Hours without plays are left out.'))),
    h('div', { class: 'dash-3' },
      h('div', { class: 'panel' }, h('h2', { class: 'panel-title' }, 'How streams played'), methodsTable(st),
        st.reasons?.length ? [h('h3', { class: 'panel-sub' }, 'Why streams were converted'),
          h('p', { class: 'small dim', style: { margin: '-4px 0 8px' } }, 'A play can count toward several reasons.'),
          barList(st.reasons.map((m) => ({ label: reasonLabel(m.key), value: m.count })), (v) => `${v}`, null, { wrap: true })] : null),
      h('div', { class: 'panel' }, h('h2', { class: 'panel-title' }, 'Most watched'), barList((st.topItems || []).map((m) => ({ label: m.key, value: m.count })), (v) => `${v} play${v === 1 ? '' : 's'}`)),
      h('div', { class: 'panel' }, h('h2', { class: 'panel-title' }, "Who's watching"), barList((st.users || []).map((m) => ({ label: m.key, value: m.value })), (v) => fmtHours(v)),
        h('h3', { class: 'panel-sub' }, 'Clients'), barList((st.clients || []).map((m) => ({ label: m.key || 'unknown', value: m.count })), (v) => `${v} play${v === 1 ? '' : 's'}`))),
    history);
  requestAnimationFrame(() => {
    const day = (i) => new Date(Date.now() - i * 86400000).toLocaleDateString(undefined, { dateStyle: 'medium' });
    columnChart(hoursBox, { title: `${hoursTitle}, ${day(days - 1)} to ${day(0)}`, labels, fullLabels: full, values: hours, fmt: fmtH, tipLabel: 'watched', floor: 0.25 });
    columnChart(hourBox, { title: 'Plays by hour of day', labels: hod.map((_, i) => `${i}h`), fullLabels: hod.map((_, i) => hourName(i)), values: hod, fmt: (v) => `${Math.round(v)}`, tipLabel: 'plays' });
  });
  return page;
}

// dataTable puts a chart's values in a collapsed table for keyboard, screen
// reader and touch users. Rows that are null are left out.
function dataTable(head, rows, note) {
  rows = rows.filter(Boolean);
  return h('details', { class: 'chart-data' }, h('summary', null, 'Show data'),
    h('p', { class: 'small dim' }, note),
    h('table', { class: 'tbl' },
      h('tr', null, head.map((t) => h('th', { scope: 'col' }, t))),
      rows.map((r) => h('tr', null, r.map((v) => h('td', null, `${v}`))))));
}

// A few seconds read "<1 min", not "0 min"; nothing at all reads "0 h".
const fmtHours = (v) => {
  const min = Math.round(v * 60);
  return !(v > 0) ? '0 h' : min < 1 ? '<1 min' : min < 60 ? `${min} min` : v < 100 ? `${v.toFixed(1)} h` : `${Math.round(v).toLocaleString()} h`;
};

// Plays, share and stall time per playback method, in one table.
function methodsTable(st) {
  const plays = Object.fromEntries((st.methods || []).map((m) => [m.key, m.count]));
  const stall = Object.fromEntries((st.bufferByMethod || []).map((m) => [m.key, m.value]));
  const total = Object.values(plays).reduce((a, b) => a + b, 0);
  const rows = ['direct', 'remux', 'transcode'].filter((k) => plays[k]);
  if (!rows.length) return h('div', { class: 'dim small' }, 'No data yet');
  return h('table', { class: 'tbl mtable' },
    h('tr', null, h('th', { scope: 'col' }, h('span', { class: 'sr-only' }, 'Method')), h('th', { scope: 'col' }, 'Plays'), h('th', { scope: 'col' }, 'Stalled')),
    rows.map((k) => h('tr', null,
      h('td', null, h('i', { class: `dot ${k}`, 'aria-hidden': 'true' }), METHOD_LABEL[k] || k),
      h('td', null, `${plays[k]}`, h('span', { class: 'dim' }, ` · ${Math.round((plays[k] / total) * 100)}%`)),
      h('td', null, stall[k] != null ? `${stall[k].toFixed(2)}%` : '—'))));
}

async function libraryTab() {
  const st = await api('/api/admin/stats/library');
  const bucketList = (dim, b, fmtKey = (k) => k) => barList([...(b || [])].sort((x, y) => y.value - x.value)
    .map((x) => ({ label: `${fmtKey(x.key)} (${x.count})`, value: x.value, onClick: () => libraryTitles(dim, x.key, `${DIM_LABEL[dim]}: ${fmtKey(x.key)}`) })), (v) => fmtBytes(v));
  const panel = (title, list) => h('div', { class: 'panel' }, h('h2', { class: 'panel-title' }, title), list);
  return h('div', null,
    h('div', { class: 'stats-grid' },
      stat('Movies', st.movies.toLocaleString()),
      stat('Shows', st.shows.toLocaleString(), `${st.seasons} seasons · ${st.episodes.toLocaleString()} episodes`),
      stat('Storage', fmtBytes(st.totalBytes), `${st.files.toLocaleString()} files · ${fmtHours(st.totalDuration / 3600)} of video${st.unprobed ? ` · ${st.unprobed} awaiting analysis` : ''}${st.probeErrors ? ` · ${st.probeErrors} unreadable` : ''}`),
      stat('Metadata', `${st.metaMatched} matched`, `${st.metaMissing} not found · ${st.metaPending} pending`)),
    st.files ? h('p', { class: 'small dim', style: { margin: '0 0 10px' } }, 'Bars show storage used; file counts are in brackets. Select a row to see its titles.') : null,
    h('div', { class: 'dash-3' },
      panel('Libraries', bucketList('library', st.libraries)),
      panel('Resolution', bucketList('resolution', st.resolutions)),
      panel('HDR', bucketList('hdr', st.hdr)),
      panel('Video codecs', bucketList('video', st.videoCodecs, (k) => k.toUpperCase())),
      panel('Audio (any track)', bucketList('audio', st.audioCodecs, (k) => k.toUpperCase())),
      panel('Containers', bucketList('container', st.containers, (k) => k.toUpperCase()))));
}

const DIM_LABEL = { library: 'Library', resolution: 'Resolution', hdr: 'HDR', video: 'Video codec', audio: 'Audio track', container: 'Container' };

// libraryTitles shows the movies and shows in one Library-stats category.
async function libraryTitles(dim, key, label) {
  const list = h('div', { class: 'title-list' }, spinner());
  const m = modal({ title: label, body: list, wide: true });
  try {
    const rows = await api(`/api/admin/stats/library/titles?dim=${encodeURIComponent(dim)}&key=${encodeURIComponent(key)}`);
    const total = rows.reduce((a, r) => a + r.bytes, 0);
    clear(list).append(
      h('div', { class: 'small muted' }, `${rows.length} title${rows.length === 1 ? '' : 's'} · ${fmtBytes(total)}`),
      ...rows.map((r) => h('a', { class: 'title-row', href: `#/item/${r.itemId}`, onclick: () => m.close() },
        h('img', { src: img({ id: r.itemId }, 'poster', 80), alt: '', loading: 'lazy' }),
        h('div', { class: 'grow', style: { minWidth: 0 } },
          h('div', { class: 'ellipsis' }, r.title, r.year ? h('span', { class: 'dim' }, ` (${r.year})`) : null),
          h('div', { class: 'small dim' }, r.kind === 'show' ? `${r.files} episode${r.files === 1 ? '' : 's'}` : r.files > 1 ? `${r.files} files` : 'Movie')),
        h('span', { class: 'muted small nowrap' }, fmtBytes(r.bytes)))));
    if (!rows.length) list.append(h('div', { class: 'dim' }, 'Nothing here.'));
  } catch (e) {
    clear(list).append(h('div', { class: 'bad' }, e.message));
  }
}

// Every play, newest first, under the playback statistics. It covers all
// time (not the selected range). Nothing at all renders nothing: the
// statistics' empty state covers it.
async function historySection(ctx, days) {
  const offset = Math.max(0, +ctx.query.get('offset') || 0);
  const rows = await api(`/api/admin/history?limit=100&offset=${offset}`);
  if (!rows.length && !offset) return null;
  const page = (o) => `#/dashboard/playback?days=${days}&offset=${o}`;
  const pager = (label, o) => h('a', { class: 'btn sm', href: page(o), onclick: (e) => { if (!e.ctrlKey && !e.metaKey && !e.shiftKey) focusAfterRoute('history'); } }, label);
  const reasons = (r) => r.reasons ? r.reasons.split('; ').map(reasonLabel).join('; ') : '';
  return h('section', { id: 'history', style: { marginTop: '28px' } },
    h('div', { class: 'toolbar' }, h('h2', { class: 'section-title', tabindex: '-1', dataset: { focusKey: 'history' } }, 'History · all time'),
      rows.length ? h('span', { class: 'muted small' }, `${offset + 1}–${offset + rows.length}`) : null, h('div', { class: 'spacer' }),
      offset > 0 ? pager('Newer', Math.max(0, offset - 100)) : null,
      rows.length === 100 ? pager('Older', offset + 100) : null,
      h('button', { class: 'btn sm danger', onclick: async (e) => {
        const btn = e.currentTarget;
        if (!(await confirmDialog('Delete all playback history? Every total, chart and list on this tab is calculated from it and will reset to zero. Watch progress and watched status are kept.', 'Delete history', true, 'Delete playback history?'))) return;
        if (!(await run(btn, () => api('/api/admin/history', { method: 'DELETE' }), 'Playback history cleared'))) return;
        if (offset) location.hash = page(0); else refreshSoft();
      } }, 'Clear history')),
    rows.length ? h('div', { class: 'panel table-wrap' }, h('table', { class: 'tbl history' },
      h('tr', null, ['When', 'User', 'Title', 'Method', 'Watched', 'Data', 'Stalls', 'Output', 'Client'].map((t) => h('th', { scope: 'col' }, t))),
      rows.map((r) => h('tr', null,
        h('td', { class: 'nowrap h-when' }, fmtDate(r.startedAt)),
        h('td', { class: 'h-user' }, r.userName),
        h('td', { class: 'h-title' }, h('a', { href: `#/item/${r.itemId}` }, r.title), r.subtitle ? h('div', { class: 'dim small' }, r.subtitle) : null),
        h('td', { class: 'h-method', title: reasons(r) || null }, h('span', { class: `method ${r.method}` }, METHOD_LABEL[r.method] || r.method), r.reasons ? h('div', { class: 'dim small h-reasons' }, reasons(r)) : null),
        h('td', { class: 'nowrap h-num', 'data-label': 'Watched' }, r.watched < 60 ? `${Math.round(r.watched)}s` : fmtDuration(r.watched)),
        h('td', { class: 'nowrap h-num', 'data-label': 'Data' }, fmtBytes(r.bytes)),
        h('td', { class: 'nowrap h-num', 'data-label': 'Stalls' }, r.bufferEvents ? `${r.bufferEvents} (${r.bufferSeconds.toFixed(0)}s)` : '0'),
        h('td', { class: 'small h-output' }, [r.videoOut, r.audioOut].filter(Boolean).join(' / ')),
        h('td', { class: 'small h-client' }, `${r.client}`, h('div', { class: 'dim mono' }, `${r.ip}${r.remote ? ' · remote' : ''}`))))))
      : h('div', { class: 'empty' }, h('h3', null, 'No older plays'), h('p', null, pager('Back to the newest plays', 0))));
}
