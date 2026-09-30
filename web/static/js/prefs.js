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
  autoplayNext: true,
  countdown: 10,
  showStats: false,
  volume: 1,
  muted: false,
  skipBack: 10,
  skipFwd: 30,
  heartbeat: 10,
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

export const QUALITIES = [
  [0, 'Original'],
  [40000, '40 Mbps'],
  [20000, '20 Mbps · 1080p'],
  [12000, '12 Mbps · 1080p'],
  [8000, '8 Mbps · 1080p'],
  [4000, '4 Mbps · 720p'],
  [3000, '3 Mbps · 720p'],
  [1500, '1.5 Mbps · 480p'],
  [720, '720 kbps · 360p'],
];
