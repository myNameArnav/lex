// App-wide keyboard shortcuts and the "?" help sheet. The player handles its
// own keys while it's open; this module only adds the help sheet there.
// Single-key shortcuts can be turned off (Settings > Playback) for speech
// input and accidental presses; Ctrl/⌘ shortcuts and the player keys stay.

import { h, modal } from './ui.js';
import { isPlayerOpen, pauseForOverlay } from './player.js';
import { prefs } from './prefs.js';

export const MOD = /Mac|iPhone|iPad/.test(navigator.platform) ? '⌘' : 'Ctrl';

const SECTIONS = [
  ['General', [
    [[MOD, 'K'], 'Search', '+'],
    [['/'], 'Search'],
    [['?'], 'Show keyboard shortcuts'],
    [['Esc'], 'Close a dialog or menu, clear the search box'],
  ]],
  ['Go to', [
    [['g', 'h'], 'Home', 'then'],
    [['g', '1 – 9'], 'Library 1 to 9 (in menu order)', 'then'],
    [['g', 's'], 'Settings', 'then'],
    [['g', 'd'], 'Dashboard (admins)', 'then'],
  ]],
  ['Titles', [
    [['p'], 'Play or resume the title you are viewing'],
  ]],
  ['Player', [
    [['Space'], 'Play / pause'], [['k'], 'Play / pause'],
    [['←', '→'], 'Skip back / forward'], [['j', 'l'], 'Skip back / forward'],
    [['↑', '↓'], 'Volume'], [['m'], 'Mute'],
    [['0 – 9'], 'Jump to 0% – 90%'], [['Home', 'End'], 'Start / near the end'],
    [['f'], 'Fullscreen'], [['c'], 'Subtitles & audio'],
    [['g', 'h'], 'Subtitles earlier / later (0.1 s)'],
    [['n'], 'Next episode'], [['s'], 'Skip intro'], [['i'], 'Stats for nerds'],
    [['Esc'], 'Close the menu, then the player'],
  ]],
];

let open = null;

export function showShortcuts() {
  if (open) { open.close(); return; }
  const inPlayer = isPlayerOpen();
  const single = prefs.get('shortcuts');
  const help = single ? [['?'], 'Show keyboard shortcuts'] : [[MOD, '/'], 'Show keyboard shortcuts', '+'];
  // The app's own keys don't work over the player, so it lists only its keys.
  const sections = inPlayer ? [SECTIONS[3], ['General', [help]]] : SECTIONS;
  // join: '+' held together, 'then' pressed in turn, '/' alternatives.
  const keys = (ks, join = '/') => h('span', { class: 'keys' }, ks.map((k, i) => [i ? h('span', { class: 'join' }, join) : null, h('kbd', null, k)]));
  const body = [
    !single && !inPlayer ? h('p', { class: 'help', style: { margin: 0 } }, 'Single-key shortcuts are off (Settings › Playback), so only Ctrl/⌘ shortcuts work outside the player.') : null,
    h('div', { class: 'shortcuts' }, sections.map(([title, rows]) => h('section', null,
      h('h3', null, title),
      h('dl', null, rows.map(([ks, what, join]) => [h('dt', null, keys(ks, join)), h('dd', null, what)]))))),
  ];
  const resume = pauseForOverlay();
  const m = modal({ title: 'Keyboard shortcuts', body, wide: true, parent: document.querySelector('.player') || document.body, onClose: () => { open = null; resume(); } });
  open = m;
}

const typing = (el) => el && (el.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName)) && el.type !== 'range' && el.type !== 'checkbox';

// install wires the global handler. nav: { focusSearch, go(hash), libraries() }.
export function installShortcuts(nav) {
  let gAt = 0;
  document.addEventListener('keydown', (e) => {
    const mod = e.metaKey || e.ctrlKey;
    const single = prefs.get('shortcuts');
    // Help: "?" (Shift + /) or Ctrl/⌘ + /
    if ((e.key === '?' && single && !typing(e.target)) || (mod && e.key === '/')) { e.preventDefault(); showShortcuts(); return; }
    if (isPlayerOpen()) return; // the player owns every other key
    if (mod && !e.altKey && e.key.toLowerCase() === 'k') { e.preventDefault(); nav.focusSearch(); return; }
    if (!single || typing(e.target) || mod || e.altKey || document.querySelector('.modal-bg')) return;
    const k = e.key;
    if (k === '/') { e.preventDefault(); nav.focusSearch(); return; }
    if (Date.now() - gAt < 1200) {
      gAt = 0;
      const libs = nav.libraries();
      const target = k === 'h' ? '#/' : k === 's' ? '#/settings' : k === 'd' ? '#/dashboard'
        : /^[1-9]$/.test(k) && libs[+k - 1] ? `#/library/${libs[+k - 1].id}` : null;
      if (target) { e.preventDefault(); nav.go(target); }
      return;
    }
    if (k === 'g') { gAt = Date.now(); return; }
    if (k === 'p' && location.hash.startsWith('#/item/')) {
      const btn = [...document.querySelectorAll('main button')].find((b) => /^\s*(Play|Resume)\b/.test(b.textContent));
      if (btn) { e.preventDefault(); btn.click(); }
    }
  });
}
