// Choosing audio and subtitle tracks, styling ASS subtitles to the viewer's
// preferences, and fetching subtitle tracks.

import { state } from '../app.js';
import { prefs } from '../prefs.js';
import { assStyleOverrides } from '../substyle.js';

/** @typedef {import('../../../types/api.gen').fileView} fileView */

// The preferred audio track: the language asked for, else the file's default, else the first.
/** @param {fileView} file */
export function pickAudio(file, lang) {
  const auds = (file.info?.streams || []).filter((s) => s.type === 'audio');
  if (!auds.length) return -1;
  return (lang && auds.find((a) => a.language === lang)) || auds.find((a) => a.default) || auds[0];
}

// tp: the subtitle mode and language in effect for this title (see
// track-memory.js).
/** @param {fileView} file */
export function pickSubtitle(file, audio, tp) {
  const mode = tp.subMode;
  const want = tp.subLang;
  const subs = file.subtitles || [];
  if (mode === 'off' || !subs.length) return -1;
  const byLang = (l) => subs.filter((s) => s.language === l || (!s.language && l === want));
  const prefer = (list) => list.find((s) => s.textSub && !s.forced) || list.find((s) => !s.forced) || list[0];
  if (mode === 'always') {
    const s = prefer(byLang(want));
    return s ? s.index : -1;
  }
  // auto: forced subs for the audio language, or full subs if the audio is foreign.
  const alang = audio && audio !== -1 ? audio.language : '';
  const forced = subs.find((s) => s.forced && (s.language === alang || s.language === want));
  if (forced) return forced.index;
  if (want && alang && alang !== want) {
    const s = prefer(byLang(want));
    if (s) return s.index;
  }
  return -1;
}

// Style names used for signs, songs and effects rather than dialogue.
const ASS_SIGN_STYLE = /sign|^ts\b|title|kara|romaji|kanji|song|lyric|^op\b|^ed\d*\b|opening|ending|screen|insert|note|logo/i;

// Applies the viewer's subtitle preferences (substyle.js) to an ASS
// script's dialogue styles (signs and karaoke keep the release's look,
// since they're positioned over the picture on purpose).
export function styleASS(text) {
  const p = prefs.all();
  const end = text.indexOf('[Events]');
  if (end < 0) return text;
  const head = text.slice(0, end);
  const resY = +(head.match(/^PlayResY:\s*(\d+)/m) || [])[1] || 288;
  let format = null;
  const out = head.split('\n').map((line) => {
    const m = line.match(/^(Format|Style):\s*(.*)$/);
    if (!m) return line;
    const fields = m[2].split(',').map((f) => f.trim());
    if (m[1] === 'Format') { format = fields.map((f) => f.toLowerCase()); return line; }
    if (!format || fields.length < format.length) return line;
    const at = (k) => format.indexOf(k);
    const align = +fields[at('alignment')];
    // Only bottom-aligned (numpad 1-3) dialogue styles.
    if (ASS_SIGN_STYLE.test(fields[at('name')]) || !(align >= 1 && align <= 3)) return line;
    const cur = Object.fromEntries(format.map((k, i) => [k, fields[i]]));
    for (const [k, v] of Object.entries(assStyleOverrides(p, cur, resY))) if (at(k) >= 0) fields[at(k)] = String(v);
    return `Style: ${fields.join(',')}`;
  });
  return out.join('\n') + text.slice(end);
}

// Downloaded subtitles are shared: only whoever fetched them, or an admin, may remove them.
export const canRemoveSub = (d) => !!state.me && (state.me.isAdmin || (!!d.downloadedBy && d.downloadedBy === state.me.id));

// Fetches a subtitle track; partial means the server is still extracting it.
export async function fetchSub(url) {
  const res = await fetch(url, { credentials: 'same-origin' });
  if (!res.ok) throw new Error((await res.json().catch(() => ({}))).error || `HTTP ${res.status}`);
  return { text: await res.text(), partial: res.headers.get('X-Lex-Partial') === '1' };
}

// The same, as bytes (image subtitles).
export async function fetchSubBytes(url) {
  const res = await fetch(url, { credentials: 'same-origin' });
  if (!res.ok) throw new Error((await res.json().catch(() => ({}))).error || `HTTP ${res.status}`);
  return { data: await res.arrayBuffer(), partial: res.headers.get('X-Lex-Partial') === '1' };
}
