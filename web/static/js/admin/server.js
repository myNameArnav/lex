// Server settings pages: General, Network, Transcoding, Metadata, Previews & intros, Sonarr & Radarr.

import { h, toast, confirmDialog, run } from '../ui.js';
import { api } from '../api.js';
import { unmatchedModal } from './dashboard.js';
import { heading, field, select, configForm } from './forms.js';

// The webhook URL carries a secret token: offer a copy button, falling back
// to selecting the text where the clipboard API is unavailable (plain http).
function copyableUrl(url) {
  const code = h('code', { class: 'mono' }, url);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(url);
      toast('Webhook URL copied', 'ok');
    } catch {
      getSelection().selectAllChildren(code);
      toast('Press Ctrl+C (⌘C) to copy the selected URL');
    }
  };
  return h('div', { class: 'webhook-url' }, code, ' ', h('button', { class: 'btn sm', type: 'button', onclick: copy }, 'Copy'));
}

// One Sonarr/Radarr: its connection status and a Connect button. Lex adds
// the rescan webhook through the app's API, trying each of its addresses
// until the app's own Test passes, so nobody has to copy URLs between apps.
// urlKey/keyKey are the config fields holding the app's address and API key;
// the unsaved form values are used, so Connect works before Save.
function arrRow(name, draft, urlKey, keyKey, missing) {
  const app = name.toLowerCase();
  const status = h('span', { class: 'arr-status muted', role: 'status' });
  const btn = h('button', { class: 'btn sm', type: 'button' }, 'Connect');
  const show = (text, cls = 'muted') => { status.className = `arr-status ${cls}`; status.replaceChildren(...[text].flat()); };
  const body = () => ({ url: draft[urlKey] || '', key: draft[keyKey] || '', origin: location.origin });
  const ready = () => !!(draft[urlKey] && draft[keyKey]);
  const connected = (st) => {
    btn.textContent = 'Reconnect';
    show(['Connected', h('span', { class: 'muted' }, ` · ${st.app || name} calls ${st.url}`)], 'good');
  };
  let seq = 0;
  const check = async () => {
    const n = ++seq;
    if (!ready()) { btn.disabled = true; btn.textContent = 'Connect'; show(missing); return; }
    btn.disabled = false;
    show('Checking…');
    try {
      const st = await api(`/api/admin/arr/${app}/status`, { method: 'POST', body: body() });
      if (n !== seq) return;
      if (st.connected) connected(st);
      else { btn.textContent = 'Connect'; show('Not connected'); }
    } catch (e) {
      if (n === seq) show(`Can’t check ${name}: ${e.message}`, 'bad');
    }
  };
  btn.onclick = () => run(btn, async () => {
    ++seq;
    show(`Connecting… ${name} is testing each address lex has`);
    try {
      const st = await api(`/api/admin/arr/${app}/connect`, { method: 'POST', body: body() });
      connected(st);
      toast(`${name} will now tell lex when it imports something`, 'ok');
    } catch (e) {
      show(`${e.message}. Try setting it up by hand below.`, 'bad');
    }
  });
  const row = h('div', { class: 'arr-row' }, h('b', null, name), status, btn);
  row.recheck = check;
  check();
  return row;
}

// The by-hand fallback for apps Lex can't reach (or other tools): one URL
// to copy, plus the LAN addresses for apps in containers.
function webhookManual(path, lanBases) {
  const lan = (lanBases || []).filter((b) => b !== location.origin);
  return h('details', { class: 'arr-manual' },
    h('summary', null, 'Set it up by hand'),
    h('div', { class: 'help' },
      'In Sonarr or Radarr, add Settings › Connect › Webhook with this URL (method POST or PUT):',
      copyableUrl(location.origin + path),
      lan.length ? ['If its Test fails (common when the app runs in Docker or Podman), use a LAN address instead:', ...lan.map((b) => copyableUrl(b + path))] : null));
}

// Both apps' addresses and keys live here: lex uses them for the rescan
// webhook, and Radarr's also as a keyless movie metadata source.
export function arrPage() {
  return configForm(({ text, info, draft }) => {
    const radarr = arrRow('Radarr', draft, 'radarrUrl', 'radarrKey', 'Enter Radarr’s address and API key above');
    const sonarr = arrRow('Sonarr', draft, 'sonarrUrl', 'sonarrKey', 'Enter Sonarr’s address and API key above');
    const later = (row) => { let t = 0; return () => { clearTimeout(t); t = setTimeout(row.recheck, 600); }; };
    return [h('div', { class: 'form-section', dataset: { searchSection: 'Sonarr & Radarr' } }, heading('Sonarr & Radarr', 'Connect them so lex rescans the moment they import, upgrade, rename or delete something, instead of waiting for the next scheduled scan. Radarr is also used as a keyless TMDB source for movies (matched by folder name).'),
      h('div', { class: 'form-grid', oninput: later(radarr) },
        field('Radarr URL', text('radarrUrl', { placeholder: 'http://127.0.0.1:7878' })),
        field('Radarr API key', text('radarrKey', { type: 'password', autocomplete: 'off' }), 'Radarr › Settings › General › API Key')),
      h('div', { class: 'form-grid', oninput: later(sonarr) },
        field('Sonarr URL', text('sonarrUrl', { placeholder: 'http://127.0.0.1:8989' })),
        field('Sonarr API key', text('sonarrKey', { type: 'password', autocomplete: 'off' }), 'Sonarr › Settings › General › API Key')),
      h('div', { class: 'arr-rows' }, radarr, sonarr),
      webhookManual(info.webhookUrl, info.webhookLanUrls))];
  });
}

export function generalSection() {
  return configForm(({ num, text }) => [
    h('div', { class: 'form-section' }, h('h2', null, 'General'),
      field('Server name', text('serverName'))),
    h('div', { class: 'form-section' }, h('h2', null, 'Playback history'),
      field('Keep history for (days)', num('historyRetentionDays', { min: 0, max: 36500 }), '0 keeps it forever. Older plays are deleted hourly, and the Playback dashboard only covers what is kept.')),
  ]);
}

export function networkSection() {
  return configForm(({ num, list, tg }) => [
    h('div', { class: 'form-section' }, h('h2', null, 'Remote access'),
      field('Remote bitrate limit (kbps)', num('remoteMaxBitrate', { min: 0, step: 500 }), '0 = unlimited. Applies to clients outside the local networks below; files above it get transcoded.'),
      field('Local networks (CIDR, comma separated)', list('localNetworks'))),
    h('div', { class: 'form-section' }, h('h2', null, 'Reverse proxy'),
      tg('trustProxy', 'Trust reverse-proxy headers', 'Enable only behind a proxy that replaces client-supplied forwarding headers.'),
      field('Trusted proxy peers (CIDR, comma separated)', list('trustedProxies'), 'Only these peers may supply client IP and HTTPS headers. Defaults to loopback; use exact proxy addresses where possible.')),
    h('div', { class: 'form-section' }, h('h2', null, 'Advanced'),
      field('Stream write buffer (KB)', num('streamBufferKb', { min: 32, max: 4096 }))),
  ]);
}

export function transcodingSection() {
  let first = null;
  // With every method off nothing can play.
  const check = (d) => (!d.enableDirectPlay && !d.enableRemux && !d.enableTranscode ? ['Keep at least one playback method on', first] : null);
  return configForm(({ draft, num, text, tg, bind, info }) => {
    const encs = info.ffmpeg.encoders || [];
    const encOpts = [['libx264', `libx264 (software)${encs.includes('libx264') ? '' : ' (not available)'}`], ['h264_v4l2m2m', `h264_v4l2m2m (Raspberry Pi hardware)${encs.includes('h264_v4l2m2m') ? (info.ffmpeg.v4l2Device ? '' : ', no /dev/video11') : ', not in this ffmpeg'}`]];
    const direct = tg('enableDirectPlay', 'Direct Play', 'Send the original file untouched (range requests). Zero CPU.');
    first = direct.querySelector('input');
    return [
      h('div', { class: 'form-section' }, h('h2', null, 'Playback methods'),
        direct,
        tg('enableRemux', 'Direct Stream (remux)', 'Repackage into fragmented MP4 without re-encoding video. Very low CPU; audio is converted to AAC only if needed.'),
        tg('enableTranscode', 'Transcode', 'Re-encode video to H.264. CPU heavy on low-end boards.'),
        h('div', { class: 'form-grid' },
          field('Max simultaneous video transcodes', num('maxTranscodes', { min: 0, max: 16 }), '0 = unlimited. 1 is sensible on a Raspberry Pi.'),
          field('Max transcode resolution', h('select', { onchange: (e) => { draft.maxTranscodeHeight = +e.target.value; } }, [[480, '480p'], [720, '720p'], [1080, '1080p'], [2160, '4K']].map(([v, l]) => h('option', { value: v, selected: draft.maxTranscodeHeight === v }, l)))))),
      h('div', { class: 'form-section' }, h('h2', null, 'Video encoder'),
        field('Encoder', h('select', { onchange: (e) => { draft.videoEncoder = e.target.value; } }, encOpts.map(([v, l]) => h('option', { value: v, selected: draft.videoEncoder === v, disabled: !encs.includes(v) }, l)))),
        h('div', { class: 'form-grid' },
          field('x264 preset', h('select', { onchange: (e) => { draft.x264Preset = e.target.value; } }, ['ultrafast', 'superfast', 'veryfast', 'faster', 'fast', 'medium'].map((v) => h('option', { value: v, selected: draft.x264Preset === v }, v))), 'Faster presets use less CPU at slightly lower quality.'),
          field('x264 CRF', num('x264Crf', { min: 15, max: 35 }), 'Quality target (capped by the bitrate limit). Lower = better.'),
          field('Threads', num('transcodeThreads', { min: 0, max: 64 }), '0 = automatic.'),
          field('Process priority (nice)', num('ffmpegNice', { min: 0, max: 19 }), 'Higher = yields CPU to other services.')),
        tg('hwDecode', 'Hardware HEVC decoding (Raspberry Pi)', info.ffmpeg.hevcHwDecode ? 'Uses the Pi\'s HEVC decoder (/dev/video19) when transcoding HEVC; falls back to software automatically.' : info.ffmpeg.hevcHwDecodeIssue ? `Unavailable: ${info.ffmpeg.hevcHwDecodeIssue}` : 'Unavailable: needs dtoverlay=vc4-kms-v3d in /boot/firmware/config.txt (then reboot) and an ffmpeg with the drm hwaccel.', { disabled: !info.ffmpeg.hevcHwDecode }),
        info.hwFallback ? h('p', { class: 'help warn' }, `Hardware decoding last fell back to software ${new Date(info.hwFallback.at).toLocaleString()} (${info.hwFallback.source}, ${info.hwFallback.count}× since start): ${info.hwFallback.message}`) : null,
        tg('tonemap', 'HDR → SDR tone mapping', info.ffmpeg.hasZscale ? 'Converts HDR colors when transcoding. Expensive.' : 'Unavailable: this ffmpeg has no zscale filter.', { disabled: !info.ffmpeg.hasZscale })),
      h('div', { class: 'form-section' }, h('h2', null, 'Audio'),
        h('div', { class: 'form-grid' },
          field('Transcoded audio channels', h('select', { onchange: (e) => { draft.audioChannels = +e.target.value; } }, [[2, 'Stereo'], [6, '5.1 surround']].map(([v, l]) => h('option', { value: v, selected: draft.audioChannels === v }, l)))),
          field('AAC bitrate (stereo, kbps)', num('audioBitrate', { min: 64, max: 640, step: 16 }))),
        tg('clientAudio', 'Convert Dolby and DTS audio in the browser', 'Browsers that can’t play AC-3, E-AC-3, TrueHD or DTS (Firefox, Chrome) convert it to AAC themselves, so the server only copies it.')),
      h('div', { class: 'form-section' }, h('h2', null, 'Streaming'),
        h('div', { class: 'form-grid' },
          field('Minimum fragment duration (ms)', num('fragmentMs', { min: 200, max: 10000, step: 100 }), 'Fragments start at video keyframes. Direct stream uses the source file’s keyframes.'),
          field('Keyframe interval (s)', num('keyframeSec', { min: 1, max: 10 }), 'For transcodes. Shorter = more precise seeking.')),
        tg('clientRemux', 'Remux in the browser', 'For Direct Stream of MKV/WebM files, browsers read the original file and repackage it themselves, so the server only sends bytes and runs no ffmpeg.')),
    ];
  }, { check });
}

// The fields need Save; the Actions below the form run at once, with the
// saved settings.
export async function metadataSection() {
  const form = await configForm(({ draft, text, tg }) => [
    h('div', { class: 'form-section' }, heading('Providers', 'TMDB gives the richest data (posters, backdrops, cast, episode stills). Get a free API key at themoviedb.org › Settings › API. Without it, TVmaze is used for shows and your local Radarr (auto-detected, or set under ', h('a', { href: '#/settings/arr' }, 'Sonarr & Radarr'), ') for movies, with no keys needed.'),
      field('TMDB API key or read access token', text('tmdbKey', { type: 'password', placeholder: 'v3 key or v4 token', autocomplete: 'off' }), 'Saving a new key retries unmatched items automatically.'),
      field('Language', text('metadataLanguage', { placeholder: 'en-US' })),
      tg('enableTmdb', 'Use TMDB'),
      tg('enableTvmaze', 'Use TVmaze for shows (no key)')),
    h('div', { class: 'form-section' }, h('h2', null, 'Local'),
      tg('useLocalMetadata', 'Use local artwork & hints', 'poster.jpg / fanart.jpg / *-thumb.jpg, .plexmatch and .nfo ids.'),
      tg('generateThumbs', 'Generate missing episode thumbnails', 'Grabs one frame with ffmpeg when no still is available (cached).')),
    h('div', { class: 'form-section' }, heading('Subtitle downloads', 'Search and download subtitles from the player (Subtitles › Search online). Needs a free API key: create an account at opensubtitles.com, then Profile › API consumers › New consumer. Username and password are optional but raise the daily download limit.'),
      field('OpenSubtitles API key', text('openSubtitlesKey', { type: 'password', autocomplete: 'off' })),
      h('div', { class: 'form-grid' },
        field('OpenSubtitles username', text('openSubtitlesUser', { autocomplete: 'off' })),
        field('OpenSubtitles password', text('openSubtitlesPass', { type: 'password', autocomplete: 'new-password' }))),
      field('Who can download subtitles', select(draft.subtitleDownloads, [['everyone', 'Everyone'], ['admins', 'Admins only']], (v) => { draft.subtitleDownloads = v; }),
        'Downloads use this server’s one OpenSubtitles account and its daily allowance, and are added to the file for everyone. Users can still remove subtitles they downloaded.')),
  ]);
  const actions = h('div', { class: 'form-section' }, h('h2', null, 'Actions'), h('p', { class: 'help', style: { margin: 0 } }, 'These use the saved settings, so save your changes first.'),
    h('div', { class: 'row wrap' },
      h('button', { class: 'btn', type: 'button', onclick: (e) => run(e.currentTarget, () => api('/api/admin/metadata/refresh', { method: 'POST', body: { missingOnly: true } }), 'Retrying unmatched items') }, 'Retry unmatched items'),
      h('button', { class: 'btn', type: 'button', 'aria-haspopup': 'dialog', onclick: () => unmatchedModal() }, 'Show unmatched items'),
      h('button', { class: 'btn', type: 'button', onclick: async (e) => {
        const btn = e.currentTarget;
        if (await confirmDialog('Re-fetch metadata for every item? Manual matches are kept.', 'Refresh', false, 'Refresh all metadata?')) await run(btn, () => api('/api/admin/metadata/refresh', { method: 'POST', body: {} }), 'Refreshing all metadata in the background');
      } }, 'Refresh all metadata')));
  return h('div', { class: 'form' }, form, actions);
}

// Seek previews and intro/credits detection: background work on the media
// files themselves. Detect now runs at once, with the saved settings.
export async function analysisSection() {
  const tasks = await api('/api/admin/tasks').catch(() => null);
  const introOK = tasks?.intro?.available !== false;
  const form = await configForm(({ tg, num }) => [
    h('div', { class: 'form-section' }, heading('Seek previews', 'Thumbnails shown while hovering or dragging the seek bar. Generated in the background from keyframes only, at the lowest priority, and paused during playback (except for files in the SSD cache). About 1–3 MB per movie.'),
      tg('trickplayEnabled', 'Generate seek previews'),
      h('div', { class: 'form-grid' },
        field('Interval (seconds)', num('trickplayInterval', { min: 2, max: 60 }), 'Changes apply to newly generated previews.'),
        field('Thumbnail width (px)', num('trickplayWidth', { min: 120, max: 480, step: 20 })))),
    h('div', { class: 'form-section' }, heading('Intro & credits detection', 'Finds TV intros and end credits by comparing audio fingerprints of neighboring episodes, or from chapters named Intro/Opening and Credits/Ending. Runs in the background at the lowest CPU priority after scans.'),
      tg('introDetect', 'Detect intros'),
      h('div', { class: 'form-grid' },
        field('Analyze the first (seconds)', num('introScanSecs', { min: 120, max: 1800, step: 30 }), 'Intros after cold opens can start a few minutes in.'),
        field('Shortest intro (seconds)', num('introMinSecs', { min: 5, max: 60 })),
        field('Longest intro (seconds)', num('introMaxSecs', { min: 20, max: 300 }))),
      tg('creditsDetect', 'Detect end credits', '"Up next" then starts when the credits do, and films and last episodes get "Skip Credits".'),
      h('div', { class: 'form-grid' },
        field('Analyze the last (seconds)', num('creditsScanSecs', { min: 120, max: 1200, step: 30 }), 'At most 40% of an episode is analyzed.'),
        field('Shortest credits (seconds)', num('creditsMinSecs', { min: 5, max: 120 }))),
      h('div', { class: 'row wrap' },
        h('button', { class: 'btn', type: 'button', disabled: !introOK, 'aria-describedby': introOK ? null : 'intro-unavail', onclick: (e) => run(e.currentTarget, () => api('/api/admin/intro/scan', { method: 'POST' }), 'Intro & credits detection started') }, 'Detect intros & credits now')),
      introOK ? h('p', { class: 'help', style: { margin: 0 } }, 'Detect now uses the saved settings: save your changes first.') : h('div', { class: 'help', id: 'intro-unavail' }, 'Intro & credits detection is unavailable: this ffmpeg has no chromaprint support.')),
  ]);
  return form;
}
