// App-wide keyboard shortcuts and the "?" help sheet. The player handles its
// own keys while it's open; this module only adds the help sheet there.

import { h, modal } from './ui.js';
import { isPlayerOpen } from './player.js';

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
    [['0 – 9'], 'Jump to 0% – 90%'], [['Home', 'End'], 'Start / end'],
    [['f'], 'Fullscreen'], [['c'], 'Subtitles & audio'],
    [['g', 'h'], 'Subtitles earlier / later (0.1 s)'],
    [['n'], 'Next episode'], [['i'], 'Stats for nerds'],
    [['Esc'], 'Close the menu, then the player'],
  ]],
];

let open = null;

export function showShortcuts() {
  if (open) { open.close(); return; }
  const inPlayer = isPlayerOpen();
  // In the player, list its keys first: that's what you're using.
  const sections = inPlayer ? [SECTIONS[3], ...SECTIONS.slice(0, 3)] : SECTIONS;
  // join: '+' held together, 'then' pressed in turn, '/' alternatives.
  const keys = (ks, join = '/') => h('span', { class: 'keys' }, ks.map((k, i) => [i ? h('span', { class: 'join' }, join) : null, h('kbd', null, k)]));
  const body = h('div', { class: 'shortcuts' }, sections.map(([title, rows]) => h('section', null,
    h('h3', null, title),
    h('dl', null, rows.map(([ks, what, join]) => [h('dt', null, keys(ks, join)), h('dd', null, what)])))));
  const m = modal({ title: 'Keyboard shortcuts', body, wide: true, parent: document.querySelector('.player') || document.body, onClose: () => { open = null; } });
  open = m;
}

const typing = (el) => el && (el.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName)) && el.type !== 'range' && el.type !== 'checkbox';

// install wires the global handler. nav: { focusSearch, go(hash), libraries() }.
export function installShortcuts(nav) {
  let gAt = 0;
  document.addEventListener('keydown', (e) => {
    const mod = e.metaKey || e.ctrlKey;
    // Help: "?" (Shift + /) or Ctrl/⌘ + /
    if ((e.key === '?' && !typing(e.target)) || (mod && e.key === '/')) { e.preventDefault(); showShortcuts(); return; }
    if (isPlayerOpen()) return; // the player owns every other key
    if (mod && !e.altKey && e.key.toLowerCase() === 'k') { e.preventDefault(); nav.focusSearch(); return; }
    if (typing(e.target) || mod || e.altKey || document.querySelector('.modal-bg')) return;
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
