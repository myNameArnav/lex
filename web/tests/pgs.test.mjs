import assert from 'node:assert/strict';
import test from 'node:test';
import { parseSup, decodeObject, paletteRGBA, setAt } from '../static/js/pgs.js';

// The same track internal/stream/pgs_test.go builds, plus a second object
// version, a run-length-coded box and a palette-only update.
const be16 = (v) => [v >> 8, v & 255];
function seg(pts, type, payload) {
  return [0x50, 0x47, (pts >>> 24) & 255, (pts >> 16) & 255, (pts >> 8) & 255, pts & 255, 0, 0, 0, 0, type, ...be16(payload.length), ...payload];
}
const pcs = (pts, state, objs, { palette = 0, update = 0 } = {}) => seg(pts, 0x16, [...be16(1920), ...be16(1080), 0x10, ...be16(1), state, update, palette, objs.length,
  ...objs.flatMap((o) => [...be16(o.id), 0, o.crop ? 0x40 : 0, ...be16(o.x), ...be16(o.y), ...(o.crop ? [...be16(o.crop.x), ...be16(o.crop.y), ...be16(o.crop.w), ...be16(o.crop.h)] : [])])]);
const pds = (pts, id, entries) => seg(pts, 0x14, [id, 0, ...entries.flat()]);
const ods = (pts, id, w, h, rle) => seg(pts, 0x15, [...be16(id), 0, 0xc0, 0, 0, rle.length + 4, ...be16(w), ...be16(h), ...rle]);
const end = (pts) => seg(pts, 0x80, []);
const S = 90000;

function track() {
  return new Uint8Array([
    // 1s: a 4x2 white box (palette entry 1) at (100, 900).
    ...pcs(1 * S, 0x80, [{ id: 0, x: 100, y: 900 }]),
    ...pds(1 * S, 0, [[0, 16, 128, 128, 0], [1, 235, 128, 128, 255]]),
    ...ods(1 * S, 0, 4, 2, [1, 1, 1, 1, 0, 0, 1, 1, 1, 1, 0, 0]),
    ...end(1 * S),
    // 3s: cleared.
    ...pcs(3 * S, 0, []),
    ...end(3 * S),
    // 5s: a new epoch; a 300x1 line as runs: 2 transparent, 296 of color 2
    // (long run), 2 of color 1, cropped to its middle.
    ...pcs(5 * S, 0x80, [{ id: 1, x: 10, y: 20, crop: { x: 2, y: 0, w: 296, h: 1 } }]),
    ...pds(5 * S, 0, [[1, 235, 128, 128, 255], [2, 81, 240, 90, 128]]),
    ...ods(5 * S, 1, 300, 1, [0, 2, 0, 0xc0 | 1, 40, 2, 1, 1, 0, 0]),
    ...end(5 * S),
    // 6s: same object, new palette (palette-only update).
    ...pcs(6 * S, 0, [{ id: 1, x: 10, y: 20 }], { update: 0x80 }),
    ...pds(6 * S, 0, [[2, 235, 128, 128, 255]]),
    ...end(6 * S),
  ]);
}

test('parseSup reads display sets with their times and positions', () => {
  const t = parseSup(track().buffer);
  assert.equal(t.width, 1920);
  assert.equal(t.height, 1080);
  assert.deepEqual(t.sets.map((s) => [s.start, s.end, s.objects.length]), [[1, 3, 1], [3, 5, 0], [5, 6, 1], [6, Infinity, 1]]);
  const [o] = t.sets[0].objects;
  assert.deepEqual([o.x, o.y, o.object.w, o.object.h, o.crop], [100, 900, 4, 2, null]);
  assert.deepEqual(t.sets[2].objects[0].crop, { x: 2, y: 0, w: 296, h: 1 });
  // A palette update keeps the object but not the old palette.
  assert.equal(t.sets[3].objects[0].object, t.sets[2].objects[0].object);
  assert.notEqual(t.sets[3].objects[0].palette, t.sets[2].objects[0].palette);
});

test('setAt finds the display set on screen, and nothing between them', () => {
  const { sets } = parseSup(track().buffer);
  assert.equal(setAt(sets, 0.5), null);
  assert.equal(setAt(sets, 1), sets[0]);
  assert.equal(setAt(sets, 2.99), sets[0]);
  assert.equal(setAt(sets, 3), null); // the clearing set has no objects
  assert.equal(setAt(sets, 5.5), sets[2]);
  assert.equal(setAt(sets, 1e6), sets[3]);
});

test('decodeObject expands short runs, long runs and line ends', () => {
  const { sets } = parseSup(track().buffer);
  assert.deepEqual([...decodeObject(sets[0].objects[0].object)], [1, 1, 1, 1, 1, 1, 1, 1]);
  const line = decodeObject(sets[2].objects[0].object);
  assert.equal(line.length, 300);
  assert.deepEqual([...line.slice(0, 3)], [0, 0, 2]);
  assert.equal(line.slice(2, 298).every((v) => v === 2), true);
  assert.deepEqual([...line.slice(297)], [2, 1, 1]);
});

test('a track cut off mid display set keeps the complete ones', () => {
  const full = track();
  const t = parseSup(full.slice(0, full.length - 20).buffer);
  assert.deepEqual(t.sets.map((s) => s.start), [1, 3, 5]);
  assert.deepEqual(parseSup(new Uint8Array(0).buffer).sets, []);
});

test('paletteRGBA converts limited-range YCbCr', () => {
  const rgba = paletteRGBA({ entries: [[16, 128, 128, 0], [235, 128, 128, 255], [81, 240, 90, 128]] });
  assert.equal(rgba[0], 0); // transparent black
  assert.equal(rgba[1], 0xffffffff); // opaque white
  const red = rgba[2];
  assert.equal(red >>> 24, 128);
  assert.ok((red & 255) > 200 && ((red >> 8) & 255) < 40 && ((red >> 16) & 255) < 40, red.toString(16)); // BT.709 red
});
