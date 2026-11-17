// Settings search: a static index of every settings page, section and field,
// a combobox in the Settings side column that searches it, and reveal(), which
// scrolls to a setting on the shown page, focuses it and briefly highlights it.
// The index is static so a search needn't render (or load) every page. Labels
// must match the text the page shows (field labels, toggle labels and section
// headings): reveal() finds the setting by that text. web/tests checks they do.

import { h } from './ui.js';

// [id, label, icon, adminOnly]
export const PAGES = [
  ['preferences', 'Playback', 'sliders', false],
  ['account', 'Account', 'user', false],
  ['server', 'Server', 'gear', true],
  ['transcoding', 'Transcoding', 'cpu', true],
  ['metadata', 'Metadata', 'film', true],
  ['cache', 'SSD cache', 'drive', true],
  ['backups', 'Backups', 'archive', true],
  ['libraries', 'Libraries', 'library', true],
  ['users', 'Users', 'users', true],
  ['devices', 'Devices', 'devices', true],
  ['logs', 'Tasks & logs', 'log', true],
  ['about', 'About', 'info', true],
];

// Per page: [section heading, section keywords, [[label, keywords], ...]].
// A section listed with no fields is still a result of its own. A section
// whose heading changes (Account's names the user) carries its index name in
// data-search-section.
const INDEX = {
  preferences: [
    ['Quality & method', 'device browser', [
      ['Streaming quality', 'bitrate resolution max quality original 1080p 720p'],
      ['Playback method', 'direct play direct stream remux transcode automatic stutter'],
    ]],
    ['Buffering', 'buffer memory', [
      ['Buffer ahead', 'preload read ahead memory'],
      ['Keep behind playhead', 'back buffer rewind memory'],
      ['Progress update interval', 'resume position heartbeat save'],
    ]],
    ['Audio & subtitles', 'language captions', [
      ['Preferred audio language', 'dub dubbed audio track language'],
      ['Subtitles', 'captions cc forced foreign'],
      ['Subtitle language', 'captions cc'],
      ['Burn in image subtitles', 'pgs blu-ray bitmap burn transcode'],
    ]],
    ['Subtitle style', 'captions appearance look', [
      ['Subtitle size', 'captions font text large small bigger'],
      ['Subtitle position', 'captions height raise lower'],
      ['Font', 'captions typeface serif monospace'],
      ['Font weight', 'captions bold thin'],
      ['Text color', 'captions colour yellow foreground'],
      ['Text opacity', 'captions transparency'],
      ['Edge', 'captions outline shadow raised depressed border'],
      ['Edge color', 'captions outline colour'],
      ['Subtitle background box', 'captions contrast shadow'],
      ['Background color', 'captions colour box'],
      ['Background opacity', 'captions transparency box'],
    ]],
    ['Player', '', [
      ['Autoplay next episode', 'up next binge continue'],
      ['Skip intros automatically', 'intro opening skip intro'],
      ['Skip credits automatically', 'credits ending outro'],
      ['Show stats for nerds by default', 'debug codec bitrate dropped frames overlay info'],
      ['Single-key shortcuts', 'keyboard hotkeys keys speech'],
      ['Autoplay countdown', 'up next timer'],
      ['Skip back', 'rewind seek jump'],
      ['Skip forward', 'fast forward seek jump'],
    ]],
    ['Technical: this browser’s codecs', 'codec support hevc h265 av1 hdr capabilities compatibility', []],
  ],
  account: [
    ['Password', 'account sign in login', [
      ['Current password', 'change password'],
      ['New password', 'change password reset'],
      ['Confirm new password', 'change password'],
    ]],
  ],
  server: [
    ['General', '', [['Server name', 'title name branding']]],
    ['Live TV', 'channels tv broadcast', [
      ['Enable Live TV', 'channels tv broadcast'],
      ['Automatic channels', 'genre channels hide'],
    ]],
    ['Library scanning', 'rescan', [
      ['Rescan every (minutes)', 'scan interval periodic schedule'],
      ['Probe workers', 'ffprobe parallel threads'],
      ['Scan on startup', 'boot start'],
    ]],
    ['Sonarr & Radarr', 'webhook import rescan connect arr', [
      ['Sonarr URL', 'sonarr tv shows webhook'],
      ['Sonarr API key', 'sonarr api key'],
    ]],
    ['Playback history', '', [['Keep history for (days)', 'retention delete history stats']]],
    ['Network & remote access', 'remote internet lan proxy https', [
      ['Remote bitrate limit (kbps)', 'bandwidth internet upload speed remote'],
      ['Stream write buffer (KB)', 'network buffer'],
      ['Local networks (CIDR, comma separated)', 'lan subnet ip range local'],
      ['Trust reverse-proxy headers', 'proxy nginx caddy traefik x-forwarded-for https'],
      ['Trusted proxy peers (CIDR, comma separated)', 'proxy ip nginx caddy'],
    ]],
  ],
  transcoding: [
    ['Playback methods', 'methods', [
      ['Direct Play', 'original untouched'],
      ['Direct Stream (remux)', 'remux repackage mp4'],
      ['Transcode', 'encode convert cpu'],
      ['Max simultaneous video transcodes', 'limit concurrent'],
      ['Max transcode resolution', '4k 1080p 720p 480p height'],
    ]],
    ['Video encoder', 'h264 x264 ffmpeg hardware', [
      ['Encoder', 'hardware h264 v4l2 libx264 software gpu'],
      ['x264 preset', 'speed ultrafast veryfast'],
      ['x264 CRF', 'quality'],
      ['Threads', 'cpu cores'],
      ['Process priority (nice)', 'cpu priority nice'],
      ['Hardware HEVC decoding (Raspberry Pi)', 'hevc h265 hardware decode pi gpu'],
      ['HDR → SDR tone mapping', 'hdr sdr tonemap zscale colour color'],
    ]],
    ['Audio', 'aac surround', [
      ['Transcoded audio channels', 'stereo 5.1 surround'],
      ['AAC bitrate (stereo, kbps)', 'audio quality'],
      ['Convert Dolby and DTS audio in the browser', 'ac3 eac3 e-ac-3 truehd dts dolby'],
    ]],
    ['Streaming', 'fragments keyframes', [
      ['Minimum fragment duration (ms)', 'fragment segment'],
      ['Keyframe interval (s)', 'gop seeking'],
      ['Remux in the browser', 'mkv webm client'],
    ]],
  ],
  metadata: [
    ['Providers', 'tmdb tvmaze radarr api key', [
      ['TMDB API key or read access token', 'tmdb themoviedb api key token v3 v4'],
      ['Language', 'locale metadata language en-us'],
      ['Use TMDB', 'tmdb themoviedb'],
      ['Use TVmaze for shows (no key)', 'tvmaze shows'],
      ['Radarr URL', 'radarr movies'],
      ['Radarr API key', 'radarr api key'],
    ]],
    ['Subtitle downloads', 'opensubtitles subtitles download', [
      ['OpenSubtitles API key', 'opensubtitles api key'],
      ['OpenSubtitles username', 'opensubtitles account login'],
      ['OpenSubtitles password', 'opensubtitles account login'],
    ]],
    ['Seek previews', 'trickplay thumbnails scrubbing', [
      ['Generate seek previews', 'trickplay thumbnails scrubbing hover'],
      ['Interval (seconds)', 'trickplay thumbnails'],
      ['Thumbnail width (px)', 'trickplay thumbnails size'],
    ]],
    ['Intro & credits detection', 'intro credits skip chromaprint fingerprint', [
      ['Detect intros', 'skip intro opening chromaprint'],
      ['Analyse the first (seconds)', 'intro'],
      ['Shortest intro (seconds)', 'intro minimum'],
      ['Longest intro (seconds)', 'intro maximum'],
      ['Detect end credits', 'credits ending outro'],
      ['Analyse the last (seconds)', 'credits'],
      ['Shortest credits (seconds)', 'credits minimum'],
    ]],
    ['Local', 'nfo artwork', [
      ['Use local artwork & hints', 'nfo poster fanart plexmatch artwork'],
      ['Generate missing episode thumbnails', 'stills thumbnails ffmpeg'],
    ]],
    ['Actions', 'refresh metadata retry unmatched rematch detect now', []],
  ],
  cache: [
    ['SSD cache', 'ssd cache disk hard drive', [
      ['Enable SSD cache', 'ssd cache'],
      ['Cache folder', 'path directory'],
      ['Maximum size (GB)', 'size limit'],
      ['Keep free on disk (GB)', 'disk space free'],
      ['Largest file to cache (GB)', 'file size'],
      ['Copy speed limit (MB/s)', 'speed throttle'],
      ['Cache what\'s being played', 'playing copy'],
      ['Prefetch upcoming episodes', 'next episodes prefetch'],
    ]],
    ['Status', 'cache usage', []],
    ['Cached files', 'clear cache remove', []],
  ],
  backups: [
    ['Database backups', 'backup restore database snapshot', [
      ['Back up daily', 'schedule automatic'],
      ['Backup folder', 'path directory disk'],
      ['Backups to keep', 'retention count'],
    ]],
    ['Saved backups', 'download delete restore back up now', []],
  ],
  libraries: [],
  users: [
    ['Users', 'accounts admin role library access reset password delete', []],
    ['Add user', 'create account new user', [
      ['Username', 'new user name'],
      ['Password', 'new user'],
      ['Administrator', 'admin role'],
    ]],
  ],
  devices: [['Signed-in devices', 'sessions sign out logout clients', []]],
  logs: [
    ['Background tasks', 'scan now tasks status', []],
    ['Log', 'logs errors debug server log', []],
  ],
  about: [
    ['AI disclosure', 'llm ai', []],
    ['Lex', 'version database data folder go', []],
    ['ffmpeg', 'ffprobe encoders version', []],
    ['Caches', 'artwork subtitles clear cache images', []],
  ],
};

const PAGE_KEYWORDS = {
  libraries: 'library folders media add library scan movies tv shows path',
};

// Each entry: { page, pageLabel, section, label, keywords, admin }. Pages and
// sections are entries too (label null for a page, section only for a section).
export const ENTRIES = PAGES.flatMap(([page, pageLabel, , admin]) => [
  { page, pageLabel, section: null, label: null, keywords: PAGE_KEYWORDS[page] || '', admin },
  ...INDEX[page].flatMap(([section, skw, fields]) => [
    { page, pageLabel, section, label: null, keywords: skw, admin },
    ...fields.map(([label, keywords]) => ({ page, pageLabel, section, label, keywords, admin })),
  ]),
]);

const norm = (s) => (s || '').toLowerCase().replace(/[’']/g, '\'');

// Lower ranks first: the entry's own name starting with / containing the
// query, then its page or section name, then its keywords. Every word of a
// query must appear somewhere ("tmdb key"). Fields rank before their section.
function rank(e, q) {
  const name = norm(e.label || e.section || e.pageLabel);
  const where = norm(`${e.pageLabel} ${e.section || ''}`);
  const all = `${name} ${where} ${norm(e.keywords)}`;
  if (!q.split(/\s+/).every((w) => all.includes(w))) return -1;
  const kind = e.label ? 0 : e.section ? 0.1 : 0.2;
  if (name.startsWith(q)) return 0 + kind;
  if (name.includes(q)) return 1 + kind;
  if (where.includes(q)) return 2 + kind;
  return 3 + kind;
}

// search returns the entries matching query, best first. Admin-only pages are
// left out for everyone else.
export function searchSettings(query, { isAdmin = false, limit = 12 } = {}) {
  const q = norm(query).trim().replace(/\s+/g, ' ');
  if (!q) return [];
  return ENTRIES
    .filter((e) => isAdmin || !e.admin)
    .map((e, i) => ({ e, r: rank(e, q), i }))
    .filter((x) => x.r >= 0)
    .sort((a, b) => a.r - b.r || a.i - b.i)
    .slice(0, limit)
    .map((x) => x.e);
}

// "Label — Page › Section" (or "Section — Page", or "Page").
export function resultText(e) {
  if (e.label) return [e.label, `${e.pageLabel} › ${e.section}`];
  if (e.section) return [e.section, e.pageLabel];
  return [e.pageLabel, 'Settings page'];
}

const text = (el) => norm(el?.textContent).trim();

// find returns the element to show for entry e within a settings page's
// content: the field or toggle row, or the section, or null.
export function findSetting(root, e) {
  if (!e.section) return null;
  const sections = [...root.querySelectorAll('.form-section')];
  const sec = sections.find((s) => norm(s.dataset.searchSection) === norm(e.section) || [...s.querySelectorAll('h2')].some((h2) => text(h2) === norm(e.section)));
  if (!e.label) return sec || null;
  const label = norm(e.label);
  const rows = [...(sec || root).querySelectorAll('.field, .toggle-row')];
  return rows.find((r) => {
    const name = r.classList.contains('toggle-row') ? r.querySelector('b') : r.firstElementChild;
    return text(name) === label;
  }) || sec || null;
}

// reveal scrolls entry e's setting into view, focuses its control (or, for a
// section, its heading) and highlights it for a moment. It returns the element
// focused, or null when the page has no such setting.
export function reveal(root, e, { smooth = true } = {}) {
  const el = findSetting(root, e);
  if (!el) return null;
  let target = e.label ? [...el.querySelectorAll('input, select, textarea, button')].find((c) => !c.disabled && c.type !== 'hidden' && !c.hidden) : null;
  if (!target) {
    // A section (or a locked setting): its heading takes focus.
    target = el.querySelector('summary') || el.querySelector('h2') || el;
    if (target.tagName !== 'SUMMARY') target.setAttribute('tabindex', '-1');
  }
  el.scrollIntoView?.({ block: 'center', behavior: smooth ? 'smooth' : 'auto' });
  target.focus({ preventScroll: true });
  el.classList.remove('setting-flash');
  void el.offsetWidth; // restart the animation when the same setting is picked again
  el.classList.add('setting-flash');
  setTimeout(() => el.classList.remove('setting-flash'), 2200);
  return target;
}

// searchBox builds the combobox. choose(entry) is called with the picked
// result; the box clears itself.
let boxId = 0;
export function searchBox({ isAdmin, choose }) {
  const id = `settings-search-${++boxId}`;
  let results = [], active = -1;
  const list = h('ul', { class: 'settings-search-list', id: `${id}-list`, role: 'listbox', 'aria-label': 'Matching settings' });
  const empty = h('p', { class: 'settings-search-empty', hidden: true });
  const pop = h('div', { class: 'settings-search-pop', hidden: true }, list, empty);
  const live = h('div', { class: 'sr-only', role: 'status', 'aria-live': 'polite' });
  const input = h('input', {
    type: 'search', id, class: 'settings-search-input', placeholder: 'Search settings', autocomplete: 'off', spellcheck: 'false',
    role: 'combobox', 'aria-autocomplete': 'list', 'aria-expanded': 'false', 'aria-controls': list.id, 'aria-label': 'Search settings',
    dataset: { pageSearch: '' },
  });
  const setActive = (i) => {
    active = i;
    [...list.children].forEach((li, j) => li.setAttribute('aria-selected', String(j === i)));
    const li = list.children[i];
    if (li) { input.setAttribute('aria-activedescendant', li.id); li.scrollIntoView?.({ block: 'nearest' }); }
    else input.removeAttribute('aria-activedescendant');
  };
  const close = () => {
    pop.hidden = true;
    input.setAttribute('aria-expanded', 'false');
    input.removeAttribute('aria-activedescendant');
    active = -1;
  };
  const update = () => {
    const q = input.value.trim();
    results = searchSettings(q, { isAdmin });
    list.replaceChildren(...results.map((e, i) => {
      const [name, where] = resultText(e);
      return h('li', { id: `${id}-opt-${i}`, role: 'option', 'aria-selected': 'false',
        // mousedown would blur the input (and close the list) before the click.
        onmousedown: (ev) => ev.preventDefault(),
        onclick: () => pick(i) },
      h('span', { class: 'name' }, name), h('span', { class: 'where' }, where));
    }));
    if (!q) { close(); live.textContent = ''; return; }
    empty.hidden = results.length > 0;
    list.hidden = !results.length;
    empty.textContent = `No settings match ‘${q}’`;
    pop.hidden = false;
    input.setAttribute('aria-expanded', String(results.length > 0));
    setActive(results.length ? 0 : -1);
    live.textContent = results.length ? `${results.length} setting${results.length === 1 ? '' : 's'} found` : empty.textContent;
  };
  const pick = (i) => {
    const e = results[i];
    if (!e) return;
    input.value = '';
    update();
    choose(e);
  };
  input.addEventListener('input', update);
  input.addEventListener('focus', () => { if (input.value.trim()) update(); });
  input.addEventListener('keydown', (ev) => {
    const n = results.length;
    if (ev.key === 'ArrowDown' || ev.key === 'ArrowUp') {
      if (!n) return;
      ev.preventDefault();
      if (pop.hidden) update();
      setActive(ev.key === 'ArrowDown' ? (active + 1) % n : (active - 1 + n) % n);
    } else if (ev.key === 'Enter') {
      ev.preventDefault();
      if (!pop.hidden) pick(active < 0 ? 0 : active);
    } else if (ev.key === 'Escape') {
      // Esc clears the box (it's also how the app's other search boxes work).
      if (input.value || !pop.hidden) { ev.preventDefault(); ev.stopPropagation(); input.value = ''; update(); }
    }
  });
  const el = h('div', { class: 'settings-search', role: 'search' }, input, pop, live);
  el.addEventListener('focusout', (ev) => { if (!el.contains(ev.relatedTarget)) close(); });
  return { el, input };
}
