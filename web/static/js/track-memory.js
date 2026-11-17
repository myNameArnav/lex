// Track choices made in the player, remembered per show (or per movie) on
// this device: the audio language, subtitle language and mode, and subtitle
// timing. Picking Japanese audio with English subtitles for one anime applies
// to its next episodes, without changing what Settings → Playback says for
// everything else. Kept free of the DOM so it can be tested on its own.

const KEY = 'lex.tracks';
const MAX = 300; // titles remembered; the least recently used go first

function load() {
  try { return JSON.parse(localStorage.getItem(KEY) || '{}') || {}; } catch { return {}; }
}
function save(m) {
  try { localStorage.setItem(KEY, JSON.stringify(m)); } catch {}
}

// trackKey names what a choice applies to: an episode's show, else the item.
// Live TV has none (each programme is different).
export const trackKey = (item, live = false) => (live || !item ? null : `${item.showId || item.id}`);

// trackPrefs is what the player uses for title key: the remembered choices,
// falling back to the global preferences (get reads one, like prefs.get).
export function trackPrefs(key, get) {
  const m = (key && load()[key]) || {};
  return {
    audioLang: m.audioLang ?? get('audioLang'),
    subLang: m.subLang ?? get('subLang'),
    subMode: m.subMode ?? get('subMode'),
    subOffset: m.subOffset ?? 0,
  };
}

// rememberTracks merges patch into what's remembered for key.
export function rememberTracks(key, patch) {
  if (!key) return;
  const m = load();
  m[key] = { ...m[key], ...patch, t: Date.now() };
  const keys = Object.keys(m);
  if (keys.length > MAX) keys.sort((a, b) => (m[a].t || 0) - (m[b].t || 0)).slice(0, keys.length - MAX).forEach((k) => delete m[k]);
  save(m);
}

// rememberedCount is how many titles have choices remembered.
export const rememberedCount = () => Object.keys(load()).length;

// forgetTracks clears every remembered choice.
export function forgetTracks() {
  try { localStorage.removeItem(KEY); } catch {}
}

// subtitleChoice is what to remember for a title after the viewer picks sub
// (a subtitle stream, or null for Off) while mode was in effect: Off keeps
// them off for the title; full subtitles keep that language always on;
// forced subtitles only turn Off back into Auto (which picks them).
export function subtitleChoice(sub, mode) {
  if (!sub) return { subMode: 'off' };
  if (sub.forced) return mode === 'off' ? { subMode: 'auto' } : {};
  return { ...(sub.language ? { subLang: sub.language } : {}), subMode: 'always' };
}
