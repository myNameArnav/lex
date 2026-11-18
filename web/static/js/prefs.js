// Per-device playback preferences (quality depends on the device & network,
// so these live in localStorage rather than on the server).

const KEY = 'lex.prefs.v1';

// Browser language codes (ISO 639-1, with region) to the ISO 639-2 codes
// media files tag their tracks with.
export const LANG1 = { en: 'eng', es: 'spa', fr: 'fre', de: 'ger', it: 'ita', pt: 'por', 'pt-pt': 'por', 'pt-br': 'por', hi: 'hin', ja: 'jpn', ko: 'kor', zh: 'chi', 'zh-cn': 'chi', 'zh-tw': 'chi', ar: 'ara', ru: 'rus', nl: 'dut', sv: 'swe', no: 'nor', nb: 'nor', nn: 'nor', da: 'dan', fi: 'fin', pl: 'pol', tr: 'tur', ta: 'tam', te: 'tel', uk: 'ukr', he: 'heb', el: 'gre', cs: 'cze', hu: 'hun', ro: 'rum', th: 'tha', vi: 'vie', id: 'ind', ms: 'may' };
export const lang1to3 = (l) => { const k = (l || '').toLowerCase(); return LANG1[k] || LANG1[k.split('-')[0]] || l; };

// The browser's language, for the default subtitle language; English when
// it isn't one the settings list.
function browserLang() {
  try { return LANG1[(navigator.language || '').toLowerCase()] || LANG1[(navigator.language || '').toLowerCase().split('-')[0]] || 'eng'; }
  catch { return 'eng'; }
}

export const DEFAULTS = {
  quality: 0,          // max kbps, 0 = original
  mode: 'auto',        // auto | direct | remux | transcode
  bufferAhead: 0,      // seconds to buffer ahead (remux/transcode); 0 = as much as the browser allows
  backBuffer: 30,      // seconds kept behind the playhead
  audioLang: '',       // preferred audio language (ISO 639-2), '' = file default
  subLang: browserLang(), // preferred subtitle language: the browser's, else English
  subMode: 'auto',     // off | auto (forced / foreign audio) | always
  // Subtitle appearance; the choices are in substyle.js.
  subSize: 'medium',   // xsmall | small | medium | large | xlarge | huge
  subPos: 'normal',    // low | normal | high
  subFont: 'sans',
  subWeight: 'bold',   // normal | bold
  subColor: 'white',
  subOpacity: 1,
  subBg: false,        // background box behind each line (off: outlined text, as most players do)
  subBgColor: 'black',
  subBgOpacity: 0.6,
  subEdge: 'auto',     // auto | none | outline | shadow | raised | depressed
  subEdgeColor: 'black',
  burnImageSubs: false, // burn PGS in on the server instead of drawing it (pgs.js)
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
  timeLeft: false,     // the player's clock shows time left (−32:17) instead of the duration
  skipBack: 10,
  skipFwd: 30,
  heartbeat: 10,
  shortcuts: true,     // single-key app shortcuts (p, /, ?, g then a key)
  theme: 'system',     // system | dark | light (theme.js)
  font: 'headings',    // headings (Fredoka) | all (Fredoka) | system (theme.js)
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
