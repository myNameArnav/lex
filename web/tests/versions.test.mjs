import assert from 'node:assert/strict';
import test from 'node:test';

const store = new Map();
// ui.js listens for fullscreen changes when it loads.
globalThis.document ??= { addEventListener() {} };
globalThis.localStorage = { getItem: (k) => store.get(k) ?? null, setItem: (k, v) => store.set(k, String(v)), removeItem: (k) => store.delete(k) };
const { versionLabel, orderFiles, chosenFile, rememberFile } = await import('../static/js/versions.js');

const GB = 1024 ** 3;
const f = (id, extra) => ({ id, width: 1920, height: 1080, vcodec: 'h264', size: 12.3 * GB, name: `file${id}.mkv`, ...extra });

test('editions tell versions apart', () => {
  const files = [f(1, { edition: 'Final Cut' }), f(2, { edition: 'Theatrical', size: 11 * GB })];
  assert.equal(versionLabel(files[0], files), 'Final Cut · 1080p H.264 · 12.3 GB');
  assert.equal(versionLabel(files[1], files, { size: false }), 'Theatrical · 1080p H.264');
});

test('identical labels fall back to the file name', () => {
  const files = [f(1), f(2)];
  assert.equal(versionLabel(files[0], files), 'file1.mkv');
  assert.equal(versionLabel(files[1], files, { size: false }), 'file2.mkv');
});

test('parts are labelled and ordered', () => {
  const files = [f(7, { part: 2, size: 1 * GB }), f(3, { part: 1, size: 2 * GB })];
  assert.deepEqual(orderFiles(files).map((x) => x.id), [3, 7]);
  assert.equal(versionLabel(files[0], files, { size: false }), 'Part 2 · 1080p H.264');
  // Mixed parts and versions keep the server's order.
  assert.deepEqual(orderFiles([f(1), f(2, { part: 1 })]).map((x) => x.id), [1, 2]);
});

test('the chosen version is remembered per item', () => {
  const files = [f(1), f(2)];
  assert.equal(chosenFile(10, files).id, 1);
  rememberFile(10, 2, files);
  assert.equal(chosenFile(10, files).id, 2);
  assert.equal(chosenFile(11, files).id, 1);
  // A version that's gone falls back to the first.
  assert.equal(chosenFile(10, [f(1), f(3)]).id, 1);
  rememberFile(10, 1, files);
  assert.equal(JSON.parse(store.get('lex.versions'))[10], undefined);
});
