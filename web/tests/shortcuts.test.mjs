import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';

// Load shortcuts.js against stub modules and capture its keydown handler.
async function fixture({ shortcuts = true, playerOpen = false, playing = false } = {}) {
  const calls = { search: 0, go: [], modals: [], paused: 0, resumed: 0, clicks: 0 };
  let keydown = null;
  const document = {
    addEventListener: (type, fn) => { if (type === 'keydown') keydown = fn; },
    querySelector: () => null,
    querySelectorAll: () => [],
    body: {},
  };
  const context = vm.createContext({ document, navigator: { platform: 'Linux' }, location: { hash: '#/item/2' }, Date });
  // The stub h() keeps tag, attributes and children, so tests can read the sheet.
  const h = (tag, attrs, ...kids) => ({ tag, attrs, kids: kids.flat(Infinity).filter((k) => k != null && k !== false) });
  const deps = {
    './ui.js': {
      h,
      modal: (opts) => { const m = { opts, close: () => opts.onClose?.() }; calls.modals.push(m); return m; },
    },
    './player.js': {
      isPlayerOpen: () => playerOpen,
      pauseForOverlay: () => { if (!playing) return () => {}; calls.paused++; return () => { calls.resumed++; }; },
    },
    './prefs.js': { prefs: { get: (k) => (k === 'shortcuts' ? shortcuts : undefined) } },
  };
  const source = await readFile(new URL('../static/js/shortcuts.js', import.meta.url), 'utf8');
  const module = new vm.SourceTextModule(source, { context });
  await module.link((specifier) => {
    const values = deps[specifier];
    return new vm.SyntheticModule(Object.keys(values), function () {
      for (const [name, value] of Object.entries(values)) this.setExport(name, value);
    }, { context });
  });
  await module.evaluate();
  module.namespace.installShortcuts({ focusSearch: () => calls.search++, go: (hash) => calls.go.push(hash), libraries: () => [{ id: 1 }, { id: 2 }] });
  const press = (key, mods = {}) => {
    const e = { key, target: { tagName: 'BODY' }, metaKey: false, ctrlKey: false, altKey: false, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }, ...mods };
    keydown(e);
    return e;
  };
  // Section titles of the last sheet: modal body is [note?, div.shortcuts > section > h3].
  const sheet = () => {
    const body = calls.modals.at(-1).opts.body.flat(Infinity).filter(Boolean);
    const grid = body.find((n) => n.attrs?.class === 'shortcuts');
    return { note: body.find((n) => n.tag === 'p')?.kids.join('') || '', sections: grid.kids.map((s) => s.kids[0].kids[0]), rows: grid.kids.map((s) => s.kids[1].kids.filter((n) => n.tag === 'dd').map((dd) => dd.kids[0])) };
  };
  return { calls, press, sheet, ns: module.namespace };
}

test('single-key shortcuts work by default', async () => {
  const { calls, press } = await fixture();
  assert.ok(press('/').defaultPrevented);
  assert.equal(calls.search, 1);
  press('g'); press('2');
  assert.deepEqual(calls.go, ['#/library/2']);
  press('?');
  assert.equal(calls.modals.length, 1);
});

test('turning single-key shortcuts off leaves only Ctrl/⌘ shortcuts', async () => {
  const { calls, press, sheet } = await fixture({ shortcuts: false });
  assert.equal(press('/').defaultPrevented, false);
  press('g'); press('h');
  press('p');
  assert.equal(press('?').defaultPrevented, false);
  assert.equal(calls.search, 0);
  assert.deepEqual(calls.go, []);
  assert.equal(calls.modals.length, 0);
  // Ctrl+K and Ctrl+/ still work.
  press('k', { ctrlKey: true });
  assert.equal(calls.search, 1);
  press('/', { ctrlKey: true });
  assert.equal(calls.modals.length, 1);
  assert.match(sheet().note, /Single-key shortcuts are off/);
});

test('the in-player sheet lists only player keys and pauses playback', async () => {
  const { calls, press, sheet } = await fixture({ playerOpen: true, playing: true });
  // The app's own keys don't run over the player.
  press('/'); press('g'); press('h');
  assert.equal(calls.search, 0);
  assert.deepEqual(calls.go, []);
  press('?');
  const s = sheet();
  assert.deepEqual(s.sections, ['Player', 'General']);
  assert.deepEqual(s.rows[1], ['Show keyboard shortcuts']);
  assert.ok(s.rows[0].includes('Skip intro'));
  assert.ok(s.rows[0].includes('Start / near the end'));
  assert.equal(calls.paused, 1);
  assert.equal(calls.resumed, 0);
  calls.modals.at(-1).close();
  assert.equal(calls.resumed, 1);
});
