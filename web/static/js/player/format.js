// Display helpers and constants for the player's controls, menus and stats panel.

import { fmtTime, langName } from '../ui.js';

// "Ends at" clock.
export const END_FMT = new Intl.DateTimeFormat(undefined, { hour: 'numeric', minute: '2-digit' });

// Playback speed choices.
export const SPEEDS = [0.5, 0.75, 1, 1.25, 1.5, 1.75, 2];

// Audio and subtitle language choices, by name.
export const LANG_CHOICES = ['eng', 'spa', 'fre', 'ger', 'ita', 'por', 'hin', 'jpn', 'kor', 'chi', 'ara', 'rus', 'dut', 'swe', 'nor', 'dan', 'fin', 'pol', 'tur', 'tam', 'tel', 'ukr', 'heb', 'gre', 'cze', 'hun', 'rum', 'tha', 'vie', 'ind', 'may']
  .map((k) => [k, langName(k)]).sort((a, b) => a[1].localeCompare(b[1]));

// Back/forward icons carry the configured number of seconds.
export const skipIcon = (dir, n) => (n = Math.round(+n) || 0, `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">${dir < 0 ? '<path d="M3 12a9 9 0 1 0 3-6.7L3 8"/><path d="M3 3v5h5"/>' : '<path d="M21 12a9 9 0 1 1-3-6.7L21 8"/><path d="M21 3v5h-5"/>'}<text x="12" y="15.5" font-size="${n >= 100 ? 6 : 7.5}" text-anchor="middle" fill="currentColor" stroke="none" font-weight="700" font-family="system-ui">${n}</text></svg>`);

// Stats panel formatting: durations in whole seconds up to ten minutes, then
// m:ss; positions are always m:ss.
export const secs = (s) => (s < 600 ? `${Math.round(s)}s` : fmtTime(s));
export const plural = (n, w) => `${n} ${w}${n === 1 ? '' : 's'}`;
// The server says "copy"; the UI says "copied".
export const copied = (s) => (s || '').replace(/ \(copy\)$/, ' · copied');
