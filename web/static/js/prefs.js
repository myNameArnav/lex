// Per-device playback preferences (quality depends on the device & network,
// so these live in localStorage rather than on the server).

const KEY = 'lex.prefs.v1';

export const DEFAULTS = {
  quality: 0,          // max kbps, 0 = original
  mode: 'auto',        // auto | direct | remux | transcode
  bufferAhead: 0,      // seconds to buffer ahead (remux/transcode); 0 = as much as the browser allows
  backBuffer: 30,      // seconds kept behind the playhead
  audioLang: '',       // preferred audio language (ISO 639-2), '' = file default
  subLang: 'eng',      // preferred subtitle language
  subMode: 'auto',     // off | auto (forced / foreign audio) | always
  subSize: 'medium',
  subBg: true,
  subPos: 'normal',    // low | normal | high
  autoSkipIntro: false,
  autoSkipCredits: false,
  autoplayNext: true,
  countdown: 10,
  showStats: false,
  statsCompact: false, // stats panel collapsed to its summary and graph
  volume: 1,
  muted: false,
  nightMode: false,    // compress loud and quiet parts (audio-fx.js)
  voiceBoost: false,   // lift dialogue
  skipBack: 10,
  skipFwd: 30,
  heartbeat: 10,
  shortcuts: true,     // single-key app shortcuts (p, /, ?, g then a key)
};

let cache = null;

function load() {
  if (cache) return cache;
  try { cache = { ...DEFAULTS, ...JSON.parse(localStorage.getItem(KEY) || '{}') }; }
  catch { cache = { ...DEFAULTS }; }
  return cache;
}

export const prefs = {
  get(k) { return load()[k]; },
  all() { return { ...load() }; },
  set(k, v) {
    load()[k] = v;
    try { localStorage.setItem(KEY, JSON.stringify(cache)); } catch {}
  },
  reset() { cache = { ...DEFAULTS }; localStorage.removeItem(KEY); },
};

// Max-bitrate choices. The labels name only the bitrate: the resolution a
// transcode comes out at depends on the server and the file, so
// qualityOptions() adds it.
export const QUALITIES = [
  [0, 'Original'],
  [40000, '40 Mbps'],
  [20000, '20 Mbps'],
  [12000, '12 Mbps'],
  [8000, '8 Mbps'],
  [4000, '4 Mbps'],
  [3000, '3 Mbps'],
  [1500, '1.5 Mbps'],
  [720, '720 kbps'],
];

// The height the server picks for a transcode at this bitrate; mirrors
// heightForBitrate in internal/stream/decide.go.
export function heightForBitrate(kbps) {
  if (kbps >= 8000) return 1080;
  if (kbps >= 3000) return 720;
  if (kbps >= 1500) return 480;
  return 360;
}

// The height a transcode at kbps is delivered at: the bitrate's height,
// capped by the server's Max transcode height and by the source, rounded
// down to even as the server does. 0 maxHeight / srcHeight means unknown.
export function transcodeHeight(kbps, maxHeight = 0, srcHeight = 0) {
  let h = heightForBitrate(kbps);
  if (maxHeight > 0) h = Math.min(h, maxHeight);
  if (srcHeight > 0) h = Math.min(h, srcHeight);
  return h - (h % 2);
}

// The quality menu: QUALITIES labelled with the resolution each one will
// actually be delivered at. maxHeight is the server's Max transcode height
// (caps.maxTranscodeHeight from /api/me); in the player srcHeight and
// srcKbps describe the file. A limit at or above the source's bitrate
// leaves the video untouched when the browser can play it, so it names no
// resolution. Entries that share a resolution stay separate: they still
// differ in bitrate, which is what the choice limits.
export function qualityOptions({ maxHeight = 0, srcHeight = 0, srcKbps = 0 } = {}) {
  return QUALITIES.map(([k, base]) => [k,
    !k || (srcKbps > 0 && k >= srcKbps) ? base : `${base} · ${transcodeHeight(k, maxHeight, srcHeight)}p`]);
}
