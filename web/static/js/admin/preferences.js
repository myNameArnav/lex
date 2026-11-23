// The "You" settings pages kept on this device: Playback, Audio & subtitles, Interface.

import { h, toast, confirmDialog, LANG_OPTIONS } from '../ui.js';
import { state, refreshSoft } from '../app.js';
import { prefs, qualityOptions } from '../prefs.js';
import { SUB_SIZES, SUB_POSITIONS, SUB_FONTS, SUB_COLORS, SUB_OPACITIES, SUB_BG_OPACITIES, SUB_EDGES, SUB_WEIGHTS, subStyleVars } from '../substyle.js';
import { capsSummary } from '../caps.js';
import { applyTheme, applyFont } from '../theme.js';
import { rememberedCount, forgetTracks } from '../track-memory.js';
import { infoPair, heading, field, toggleRow, select } from './forms.js';

// Subtitle appearance (substyle.js), with a sample line that follows the
// choices. Also changeable from the player's Subtitles & audio menu.
function subStyleSection(p, set) {
  const sample = h('div', { class: 'p-subs' }, h('div', { class: 'cue' }, 'This is how your subtitles look.'), h('div', { class: 'cue' }, h('i', null, 'And a second line, in italics.')));
  const preview = h('div', { class: 'sub-preview', 'aria-hidden': 'true' }, sample);
  const paint = () => { for (const [k, v] of Object.entries(subStyleVars(prefs.all()))) preview.style.setProperty(k, v); };
  // Changes repaint the preview; the background ones also show or hide their rows.
  const show = (el, on) => { el.style.display = on ? '' : 'none'; };
  const sync = () => { paint(); show(bgRows, prefs.get('subBg')); show(edgeColor, prefs.get('subEdge') !== 'none'); };
  const live = (k, conv) => (v) => { set(k, conv)(v); sync(); };
  const pick = (label, k, list, conv, help) => field(label, select(p[k], list.map(([v, l]) => [v, l]), live(k, conv)), help);
  const bgRows = h('div', { class: 'form-grid' },
    pick('Background color', 'subBgColor', SUB_COLORS),
    pick('Background opacity', 'subBgOpacity', SUB_BG_OPACITIES, Number));
  const edgeColor = pick('Edge color', 'subEdgeColor', SUB_COLORS);
  const section = h('div', { class: 'form-section' }, heading('Subtitle style', 'Applies to text subtitles (SRT, WebVTT) and the dialogue of styled ASS subtitles, which keep their own font. Image subtitles (PGS) can’t be restyled.'),
    preview,
    h('div', { class: 'form-grid' },
      pick('Subtitle size', 'subSize', SUB_SIZES),
      pick('Subtitle position', 'subPos', SUB_POSITIONS),
      pick('Font', 'subFont', SUB_FONTS),
      pick('Font weight', 'subWeight', SUB_WEIGHTS),
      pick('Text color', 'subColor', SUB_COLORS),
      pick('Text opacity', 'subOpacity', SUB_OPACITIES, Number),
      pick('Edge', 'subEdge', SUB_EDGES, undefined, 'Automatic outlines the text when there’s no background box.'),
      edgeColor),
    toggleRow('Subtitle background box', null, p.subBg, live('subBg')),
    bgRows);
  sync();
  return section;
}

// Choices made in the player (audio, subtitles, timing) are kept per show or
// movie (track-memory.js); the languages above are the defaults for the rest.
// A row like the others: name, a short status, and Forget when there's any.
function rememberedTracksRow() {
  const n = rememberedCount();
  const name = 'Per-title choices';
  const [btn, more] = infoPair(name, 'Audio and subtitle tracks you pick in the player are remembered for that show or movie on this device, and used instead of the languages above.', 'span');
  const forget = n ? h('button', { class: 'btn sm', type: 'button', onclick: async (e) => {
    if (!(await confirmDialog(`Forget the audio and subtitle choices for ${n} title${n === 1 ? '' : 's'}? They’ll use the languages above again.`, 'Forget', false, 'Forget per-title choices?'))) return;
    forgetTracks();
    toast('Per-title choices forgotten', 'ok');
    e.target.closest('.remembered-tracks')?.replaceWith(rememberedTracksRow());
  } }, 'Forget') : null;
  if (forget) forget.setAttribute('aria-label', `Forget per-title choices (${n})`);
  return h('div', { class: 'toggle-row remembered-tracks' },
    h('span', { class: 'lbl' }, h('span', { class: 'lbl-name' }, h('b', null, name), btn),
      h('span', { class: 'help' }, n ? `Remembered for ${n} title${n === 1 ? '' : 's'}` : 'None yet'), more),
    forget);
}

// Playback, Audio & subtitles and Interface are stored in this browser
// (prefs.js), save as they change, and share one Reset.
const prefSetter = (k, conv = (x) => x) => (v) => { prefs.set(k, conv(v)); toast('Saved on this device', 'ok'); };

function devicePage(...sections) {
  return h('div', { class: 'form' },
    h('p', { class: 'help', style: { margin: 0 } }, 'Saved in this browser, so each device can have its own.'),
    sections,
    h('div', null, h('button', { class: 'btn', dataset: { focusKey: 'prefs-reset' }, onclick: resetPrefs }, 'Reset this device’s settings')));
}

let capsOpen = false; // stays open across re-renders

export function playbackPrefsSection() {
  const p = prefs.all();
  const set = prefSetter;
  const caps = capsSummary();
  return devicePage(
    h('div', { class: 'form-section' }, h('h2', null, 'Quality & method'),
      h('div', { class: 'form-grid' },
        field('Streaming quality', select(p.quality, qualityOptions({ maxHeight: state.caps.maxTranscodeHeight}), set('quality', Number)), 'Max bitrate. "Original" direct-plays when the browser supports the file. The resolution is the most a transcode delivers on this server.'),
        field('Playback method', select(p.mode, [['auto', 'Automatic (recommended)'], ['direct', 'Prefer Direct Play'], ['remux', 'Always Direct Stream (remux)'], ['transcode', 'Always Transcode']], set('mode')), 'Automatic picks the lightest method that works here. Try “Always Transcode” if a video stutters or won’t play.'))),
    h('div', { class: 'form-section' }, h('h2', null, 'Player'),
      toggleRow('Autoplay next episode', 'Shows an "Up next" card near the end with a countdown.', p.autoplayNext, set('autoplayNext')),
      toggleRow('Skip intros automatically', 'Otherwise a "Skip Intro" button appears while an intro plays.', p.autoSkipIntro, set('autoSkipIntro')),
      toggleRow('Skip credits automatically', 'When the end credits start, play the next episode right away (with autoplay on), or jump to the end of a film or last episode. Otherwise a "Skip Credits" button appears.', p.autoSkipCredits, set('autoSkipCredits')),
      toggleRow('Show stats for nerds by default', 'Overlay with codecs, bitrate, buffer health, dropped frames and server transcode speed (press i in the player).', p.showStats, set('showStats')),
      h('div', { class: 'form-grid' },
        field('Autoplay countdown', select(p.countdown, [[5, '5 seconds'], [10, '10 seconds'], [15, '15 seconds'], [30, '30 seconds']], set('countdown', Number))),
        field('Skip back', select(p.skipBack, [[5, '5 seconds'], [10, '10 seconds'], [15, '15 seconds'], [30, '30 seconds']], set('skipBack', Number))),
        field('Skip forward', select(p.skipFwd, [[10, '10 seconds'], [15, '15 seconds'], [30, '30 seconds'], [60, '60 seconds']], set('skipFwd', Number))))),
    h('div', { class: 'form-section' }, h('h2', null, 'Buffering'),
      h('div', { class: 'form-grid' },
        field('Buffer ahead', select(p.bufferAhead, [[0, 'Automatic (recommended)'], [30, 'At most 30 seconds'], [60, 'At most 1 minute'], [90, 'At most 90 seconds'], [180, 'At most 3 minutes'], [300, 'At most 5 minutes']], set('bufferAhead', Number)), 'For direct stream and transcode (direct play is always buffered by the browser). Automatic fills as much as the browser allows, usually about 150 MB. A limit keeps a transcode from running far ahead of what you watch.'),
        field('Keep behind playhead', select(p.backBuffer, [[10, '10 seconds'], [30, '30 seconds'], [60, '1 minute'], [120, '2 minutes']], set('backBuffer', Number)), 'Lets you rewind instantly; lower saves memory on phones/TVs.'),
        field('Progress update interval', select(p.heartbeat, [[5, '5 seconds'], [10, '10 seconds'], [30, '30 seconds']], set('heartbeat', Number)), 'How often your position is saved, so you can resume on another device.'))),
    h('div', { class: 'form-section' }, h('details', { class: 'caps', open: capsOpen, ontoggle: (e) => { capsOpen = e.target.open; } }, h('summary', null, h('h2', null, 'Technical: this browser’s codecs')),
      state.me.isAdmin ? h('p', { class: 'small', style: { margin: '0 0 12px' } }, h('a', { href: '#/dashboard/library' }, 'See how your library plays in this browser →')) : null,
      h('dl', { class: 'kv' }, Object.entries(caps).map(([k, v]) => [h('dt', null, k), h('dd', null, v)])))));
}

export function subtitlePrefsSection() {
  const p = prefs.all();
  const set = prefSetter;
  const langs = [['', 'File default'], ...LANG_OPTIONS];
  return devicePage(
    h('div', { class: 'form-section' }, h('h2', null, 'Languages'),
      h('div', { class: 'form-grid' },
        field('Preferred audio language', select(p.audioLang, langs, set('audioLang'))),
        field('Subtitles', select(p.subMode, [['off', 'Off'], ['auto', 'Auto (forced / foreign audio)'], ['always', 'Always on']], set('subMode'))),
        field('Subtitle language', select(p.subLang, LANG_OPTIONS, set('subLang')))),
      rememberedTracksRow(),
      toggleRow('Burn in image subtitles', 'Blu-ray (PGS) subtitles are drawn over the video in the browser. Turn this on to have the server burn them into the picture instead, which means transcoding.', p.burnImageSubs, set('burnImageSubs'))),
    subStyleSection(p, set));
}

export function interfacePrefsSection() {
  const p = prefs.all();
  const set = prefSetter;
  return devicePage(
    h('div', { class: 'form-section' }, h('h2', null, 'Appearance'),
      h('div', { class: 'form-grid' },
        field('Theme', select(p.theme, [['system', 'Same as the system'], ['dark', 'Dark'], ['light', 'Light']], (v) => { set('theme')(v); applyTheme(v); }), 'For this device. The player is always dark.'),
        field('Font', select(p.font, [['headings', 'Fredoka for titles'], ['all', 'Fredoka everywhere'], ['system', 'System font']], (v) => { set('font')(v); applyFont(v); }), 'For this device.'))),
    h('div', { class: 'form-section' }, h('h2', null, 'Keyboard'),
      toggleRow('Single-key shortcuts', 'p, /, ? and g-then-key. Ctrl/⌘ K and the player keys always work.', p.shortcuts, set('shortcuts'))));
}

// Volume and the stats panel's compact state and position aren't shown on this page, so a
// reset leaves them as they are.
async function resetPrefs() {
  if (!(await confirmDialog('Reset playback, audio & subtitle and interface settings on this device to their defaults?', 'Reset', true, 'Reset this device’s settings?'))) return;
  const keep = ['volume', 'muted', 'statsCompact', 'statsPos'].map((k) => [k, prefs.get(k)]);
  prefs.reset();
  keep.forEach(([k, v]) => prefs.set(k, v));
  toast('Reset to defaults', 'ok');
  refreshSoft();
}
