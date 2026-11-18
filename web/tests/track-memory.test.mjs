import assert from 'node:assert/strict';
import test from 'node:test';

const store = new Map();
globalThis.localStorage = { getItem: (k) => store.get(k) ?? null, setItem: (k, v) => store.set(k, String(v)), removeItem: (k) => store.delete(k) };
const { trackKey, trackPrefs, rememberTracks, subtitleChoice, rememberedCount, forgetTracks } = await import('../static/js/track-memory.js');

const global = { audioLang: '', subLang: 'eng', subMode: 'auto' };
const get = (k) => global[k];

test('episodes share their show’s key; movies use their own; live TV none', () => {
  assert.equal(trackKey({ id: 9, showId: 3 }), '3');
  assert.equal(trackKey({ id: 9 }), '9');
  assert.equal(trackKey({ id: 9 }, true), null);
});

test('choices apply to one show and leave the global preferences alone', () => {
  forgetTracks();
  rememberTracks('3', { audioLang: 'jpn', ...subtitleChoice({ language: 'eng' }, 'auto') });
  assert.deepEqual(trackPrefs('3', get), { audioLang: 'jpn', subLang: 'eng', subMode: 'always', subOffset: 0 });
  assert.deepEqual(trackPrefs('4', get), { audioLang: '', subLang: 'eng', subMode: 'auto', subOffset: 0 });
  rememberTracks('3', { subOffset: -1.5 });
  assert.equal(trackPrefs('3', get).subOffset, -1.5);
  assert.equal(trackPrefs('3', get).audioLang, 'jpn');
  assert.equal(rememberedCount(), 1);
  // Nothing is remembered without a key (live TV).
  rememberTracks(null, { audioLang: 'fre' });
  assert.equal(rememberedCount(), 1);
});

test('subtitle choices', () => {
  assert.deepEqual(subtitleChoice(null, 'always'), { subMode: 'off' });
  assert.deepEqual(subtitleChoice(null, 'auto'), { subMode: 'off' });
  assert.deepEqual(subtitleChoice({ language: 'spa' }, 'off'), { subLang: 'spa', subMode: 'always' });
  assert.deepEqual(subtitleChoice({ forced: true, language: 'eng' }, 'off'), { subMode: 'auto' });
  assert.deepEqual(subtitleChoice({ forced: true, language: 'eng' }, 'always'), {});
});

test('the least recently used titles are dropped past the limit', () => {
  forgetTracks();
  for (let i = 0; i < 305; i++) rememberTracks(String(i), { audioLang: 'eng' });
  assert.equal(rememberedCount(), 300);
});
