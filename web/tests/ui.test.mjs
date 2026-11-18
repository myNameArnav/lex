import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';

// A minimal DOM: enough for h(), toast() and run() to work on.
function fakeDom() {
  class Node {}
  class Text extends Node {
    constructor(text) { super(); this.textContent = text; this.parentNode = null; }
    remove() { this.parentNode?._drop(this); }
  }
  class Element extends Node {
    constructor(tag) {
      super();
      this.tagName = tag.toUpperCase();
      this.childNodes = []; this.attrs = {}; this.dataset = {}; this.style = {};
      this.parentNode = null; this.className = ''; this.disabled = false; this.isConnected = true;
    }
    get children() { return this.childNodes.filter((c) => c instanceof Element); }
    get childElementCount() { return this.children.length; }
    get firstElementChild() { return this.children[0] || null; }
    get textContent() { return this.childNodes.map((c) => c.textContent).join(''); }
    set textContent(v) { this.replaceChildren(new Text(String(v))); }
    set innerHTML(v) { this.html = v; }
    setAttribute(k, v) { this.attrs[k] = String(v); }
    getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
    removeAttribute(k) { delete this.attrs[k]; }
    addEventListener(type, fn) { (this.listeners ||= {})[type] = fn; }
    appendChild(c) { c.parentNode?._drop(c); c.parentNode = this; this.childNodes.push(c); return c; }
    replaceChildren(...cs) { this.childNodes.forEach((c) => { c.parentNode = null; }); this.childNodes = []; cs.forEach((c) => this.appendChild(c)); }
    remove() { this.parentNode?._drop(this); }
    contains(n) { for (let x = n; x; x = x.parentNode) if (x === this) return true; return false; }
    _drop(c) { this.childNodes = this.childNodes.filter((x) => x !== c); c.parentNode = null; }
    focus() { document.activeElement = this; }
  }
  const body = new Element('body');
  const document = {
    body, activeElement: body, fullscreenElement: null,
    createElement: (t) => new Element(t), createTextNode: (t) => new Text(t),
    querySelector: () => null, querySelectorAll: () => [], addEventListener() {},
  };
  return { Node, Element, document, body };
}

async function loadUI() {
  const dom = fakeDom();
  const timers = [];
  const frames = [];
  const context = vm.createContext({
    Node: dom.Node, document: dom.document, window: { addEventListener() {} },
    setTimeout: (fn, ms) => { timers.push({ fn, ms }); return timers.length; }, clearTimeout() {},
    requestAnimationFrame: (fn) => frames.push(fn), matchMedia: () => ({ matches: false }),
    Date, Intl, String, Math, Promise,
  });
  const source = await readFile(new URL('../static/js/ui.js', import.meta.url), 'utf8');
  const module = new vm.SourceTextModule(source, { context });
  await module.link(() => { throw new Error('ui.js has no imports'); });
  await module.evaluate();
  const flushFrames = () => frames.splice(0).forEach((f) => f());
  return { ui: module.namespace, dom, timers, flushFrames };
}

const toastTexts = (dom) => {
  const box = dom.body.children.find((c) => c.className === 'toasts');
  return box ? box.children.map((t) => t.children[0].textContent) : [];
};

test('fmtEpisode formats single and multi-episode codes', async () => {
  const { ui } = await loadUI();
  assert.equal(ui.fmtEpisode({ season: 1, episode: 3 }), 'S1 E3');
  assert.equal(ui.fmtEpisode({ season: 2, episode: 5, episodeEnd: 6 }), 'S2 E5–6');
  assert.equal(ui.fmtEpisode({ season: 1 }), 'S1');
  assert.equal(ui.fmtEpisode({}), '');
});

test('fmtEpisode shows the air date of an unplaced daily-show episode', async () => {
  const { ui } = await loadUI();
  const it = { season: 2024, episode: 305, premiere: '2024-03-05' };
  assert.equal(ui.datedEpisode(it), true);
  assert.equal(ui.fmtEpisode(it), ui.fmtAirDate('2024-03-05'));
  assert.notEqual(ui.fmtEpisode(it), 'S2024 E305');
  // Placed in its provider's season, it is numbered like any other.
  assert.equal(ui.datedEpisode({ season: 29, episode: 45, premiere: '2024-03-05' }), false);
  assert.equal(ui.fmtEpisode({ season: 29, episode: 45, premiere: '2024-03-05' }), 'S29 E45');
  // A provider that numbers seasons by year isn't mistaken for one.
  assert.equal(ui.fmtEpisode({ season: 2024, episode: 12, premiere: '2024-03-05' }), 'S2024 E12');
  assert.equal(ui.datedEpisode({}), false);
});

test('streamLabel normalises codecs and names tracks once', async () => {
  const { ui } = await loadUI();
  assert.equal(ui.streamLabel({ type: 'subtitle', language: 'eng', codec: 'srt', forced: true, external: true, textSub: true }), 'English · Forced · SRT · External');
  assert.equal(ui.streamLabel({ type: 'subtitle', language: 'spa', codec: 'subrip', textSub: true }), 'Spanish · SRT');
  // PGS is drawn by the player; other image subtitles are burned in.
  assert.equal(ui.streamLabel({ type: 'subtitle', language: 'eng', codec: 'hdmv_pgs_subtitle', textSub: false }), 'English · PGS');
  assert.equal(ui.streamLabel({ type: 'subtitle', language: 'eng', codec: 'dvd_subtitle', textSub: false }), 'English · DVD_SUBTITLE · burn-in');
  assert.equal(ui.streamLabel({ type: 'subtitle', language: 'eng', title: 'SDH', codec: 'ass', textSub: true }), 'English – SDH · ASS');
  assert.equal(ui.streamLabel({ type: 'audio', language: 'eng', title: 'Commentary', codec: 'aac', channels: 2 }), 'English · Commentary · AAC · Stereo');
  assert.equal(ui.streamLabel({ type: 'audio', language: 'eng', title: 'English Stereo', codec: 'aac', channels: 2 }), 'English Stereo · AAC · Stereo');
  assert.equal(ui.streamLabel({ type: 'audio', title: 'Director', codec: 'ac3', channels: 6, default: true }), 'Director · AC3 · 5.1 (default)');
});

test('method labels and reasons use the Title Case names', async () => {
  const { ui } = await loadUI();
  assert.equal(ui.METHOD_LABEL.remux, 'Direct Stream');
  assert.equal(ui.reasonLabel('forced remux'), 'Direct Stream chosen in player settings');
  assert.equal(ui.reasonLabel('forced direct play'), 'Direct Play chosen in player settings');
  assert.equal(ui.reasonLabel('audio codec not supported'), 'audio codec not supported');
  assert.equal(ui.KIND_LABELS.shows, 'TV Shows');
});

test('fmtDateShort gives the time today, the day this year, and the year before', async () => {
  const { ui } = await loadUI();
  const now = new Date(2026, 10, 18, 21, 30);
  const at = (...a) => new Date(...a).getTime() / 1000;
  const today = ui.fmtDateShort(at(2026, 10, 18, 21, 27), now);
  assert.match(today, /9:27/);
  assert.doesNotMatch(today, /Nov/);
  const thisYear = ui.fmtDateShort(at(2026, 8, 30, 19, 24), now);
  assert.match(thisYear, /Sep/);
  assert.doesNotMatch(thisYear, /2026|7:24/);
  assert.match(ui.fmtDateShort(at(2025, 8, 30, 19, 24), now), /2025/);
  assert.equal(ui.fmtDateShort(0, now), '');
});

test('timeAgo falls back to a medium date after 30 days', async () => {
  const { ui } = await loadUI();
  const ts = Date.now() / 1000 - 40 * 86400;
  assert.equal(ui.timeAgo(ts), new Date(ts * 1000).toLocaleDateString(undefined, { dateStyle: 'medium' }));
  assert.equal(ui.timeAgo(Date.now() / 1000 - 7200), '2h ago');
});

test('fmtAirDate keeps date-only air dates on their day west of UTC', async () => {
  const { ui } = await loadUI();
  const tz = process.env.TZ;
  process.env.TZ = 'America/New_York';
  try {
    const want = new Date(2024, 2, 5).toLocaleDateString(undefined, { dateStyle: 'medium' });
    assert.equal(ui.fmtAirDate('2024-03-05'), want);
    assert.equal(ui.fmtAirDate(''), '');
    assert.equal(ui.fmtAirDate('not a date'), '');
  } finally {
    if (tz === undefined) delete process.env.TZ; else process.env.TZ = tz;
  }
});

test('toasts are a live region, capitalised, deduplicated, keyed and capped', async () => {
  const { ui, dom, timers, flushFrames } = await loadUI();
  ui.toast('saved on this device', 'ok');
  flushFrames(); // the first toast waits a frame so the new live region announces it
  const box = dom.body.children[0];
  assert.equal(box.getAttribute('role'), 'status');
  assert.equal(box.getAttribute('aria-live'), 'polite');
  assert.deepEqual(toastTexts(dom), ['Saved on this device']);
  ui.toast('saved on this device', 'ok');
  assert.deepEqual(toastTexts(dom), ['Saved on this device'], 'same text restarts instead of stacking');
  assert.equal(timers.at(-1).ms, 3000);
  ui.toast('Subtitle timing +0.1s', '', { key: 'subOffset' });
  ui.toast('Subtitle timing +0.2s', '', { key: 'subOffset' });
  assert.deepEqual(toastTexts(dom), ['Saved on this device', 'Subtitle timing +0.2s'], 'a key replaces the text');
  ui.toast('database is locked', 'error');
  const err = box.children.at(-1);
  assert.equal(err.getAttribute('role'), 'alert');
  assert.ok(err.children.some((c) => c.getAttribute('aria-label') === 'Dismiss'), 'errors can be dismissed');
  assert.equal(timers.at(-1).ms, 7000);
  ui.toast('fourth');
  assert.deepEqual(toastTexts(dom), ['Subtitle timing +0.2s', 'Database is locked', 'Fourth'], 'at most three, oldest dropped');
});

test('later status toasts never push out an error', async () => {
  const { ui, dom, flushFrames } = await loadUI();
  ui.toast('database is locked', 'error');
  flushFrames();
  for (const m of ['One', 'Two', 'Three']) ui.toast(m);
  assert.deepEqual(toastTexts(dom), ['Database is locked', 'Two', 'Three']);
});

test('toasts fired before the live region settles keep their order', async () => {
  const { ui, dom, flushFrames } = await loadUI();
  ui.toast('first');
  ui.toast('second', 'ok');
  ui.toast('first');
  assert.deepEqual(toastTexts(dom), [], 'nothing is inserted until the next frame');
  flushFrames();
  assert.deepEqual(toastTexts(dom), ['First', 'Second']);
  ui.toast('third');
  assert.deepEqual(toastTexts(dom), ['First', 'Second', 'Third'], 'later toasts go in at once');
});

test('toasts shown in a dialog or the player outlive it', async () => {
  const { ui, dom, flushFrames } = await loadUI();
  const player = dom.document.createElement('div');
  dom.body.appendChild(player);
  dom.document.querySelector = (sel) => (sel === '.player' && player.parentNode ? player : null);
  ui.toast('stats copied');
  flushFrames();
  const box = player.children[0];
  assert.equal(box.className, 'toasts', 'the player hosts the stack while it is open');
  player.remove();
  ui.releaseToasts(player);
  assert.equal(box.parentNode, dom.body);
  assert.deepEqual(toastTexts(dom), ['Stats copied']);
  ui.releaseToasts(dom.document.createElement('dialog'));
  assert.equal(box.parentNode, dom.body, 'other elements leave the stack alone');
});

test('run disables the button while busy, toasts and reports the outcome', async () => {
  const { ui, dom, flushFrames } = await loadUI();
  const btn = dom.document.createElement('button');
  btn.textContent = 'Save changes';
  btn.focus();
  let release;
  const pending = ui.run(btn, () => new Promise((r) => { release = r; }), 'Settings saved', { busyLabel: 'Saving…' });
  assert.equal(btn.disabled, true);
  assert.equal(btn.getAttribute('aria-busy'), 'true');
  assert.equal(btn.textContent, 'Saving…');
  assert.equal(await ui.run(btn, async () => assert.fail('a busy button must not run again')), false);
  dom.document.activeElement = dom.body; // browsers drop focus from a disabled button
  release();
  assert.equal(await pending, true);
  assert.equal(btn.disabled, false);
  assert.equal(btn.getAttribute('aria-busy'), null);
  assert.equal(btn.textContent, 'Save changes');
  assert.equal(dom.document.activeElement, btn, 'focus returns to the button');
  flushFrames();
  assert.deepEqual(toastTexts(dom), ['Settings saved']);

  assert.equal(await ui.run(btn, async () => { throw new Error('database is locked'); }), false);
  assert.ok(toastTexts(dom).includes('Database is locked'));
  assert.equal(btn.disabled, false);

  const before = toastTexts(dom).length;
  assert.equal(await ui.run(null, async () => { throw Object.assign(new Error('aborted'), { name: 'AbortError' }); }), false);
  assert.equal(toastTexts(dom).length, before, 'aborts are silent');

  assert.equal(await ui.run(null, async () => ({ queued: 2 }), (r) => `Queued ${r.queued} files`), true);
  assert.ok(toastTexts(dom).includes('Queued 2 files'));
});

test('staleBanner shows the oldest failure until every poller recovers', async () => {
  const { ui, dom } = await loadUI();
  const target = dom.document.createElement('div');
  target.classList = { on: false, toggle(_, v) { this.on = v; } };
  const b = ui.staleBanner(target);
  assert.equal(b.el.getAttribute('role'), 'status');
  assert.equal(b.el.childElementCount, 0);
  const t = new Date(2026, 9, 2, 17, 0, 0).getTime();
  b.hooks('a').onFail(t);
  b.hooks('b').onFail(t + 60000);
  assert.ok(b.el.textContent.includes(new Date(t).toLocaleTimeString()));
  assert.equal(target.classList.on, true);
  b.hooks('a').onRecover();
  assert.equal(b.el.childElementCount, 1, 'still failing while b is down');
  b.ok('b');
  assert.equal(b.el.childElementCount, 0);
  assert.equal(target.classList.on, false);
  b.fail(null, 'start');
  assert.equal(b.el.textContent, 'Can’t reach the server. Retrying…');
});

test('letterOffsets gives where each rail letter starts in either direction', async () => {
  const { ui: { letterOffsets, RAIL_LETTERS } } = await loadUI();
  const counts = { '#': 2, a: 3, c: 1, z: 1, '~': 4 };
  const asc = letterOffsets(counts);
  assert.deepEqual(Object.keys(asc).sort(), [...RAIL_LETTERS].sort());
  assert.equal(asc['#'], 0);
  assert.equal(asc.a, 2);
  assert.equal(asc.b, 5); // no "b": the first title after it, a "c"
  assert.equal(asc.c, 5);
  assert.equal(asc.z, 6);
  const desc = letterOffsets(counts, true);
  assert.equal(desc.z, 4); // after the four past "z"
  assert.equal(desc.c, 5);
  assert.equal(desc.a, 6);
  assert.equal(desc['#'], 9);
});

test('fmtLeft shows the time left in whole seconds, adding up to the duration shown', async () => {
  const { ui } = await loadUI();
  assert.equal(ui.fmtLeft(763.6, 2700.4), '−32:17'); // 12:43 + 32:17 = 45:00
  assert.equal(ui.fmtLeft(0, 5400), '−1:30:00');
  assert.equal(ui.fmtLeft(3000, 2700), '−0:00');
  assert.equal(ui.fmtLeft(10, 0), '−0:00');
});
