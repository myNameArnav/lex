import assert from 'node:assert/strict';
import test from 'node:test';

const { matcher, commonWords, toggleWord, wordState, words } = await import('../static/js/channel-filter.js');

const chs = [
  { number: 2, name: 'STAR PLUS HD' },
  { number: 3, name: 'ZEE TV HD' },
  { number: 8, name: '&TV HD' },
  { number: 9, name: 'BIG MAGIC' },
  { number: 11, name: 'SONY SAB' },
  { number: 12, name: 'Star Bharat' },
  { number: 20, name: 'News18 India' },
];
const names = (q) => chs.filter(matcher(q)).map((c) => c.name);

test('words split at spaces and punctuation', () => {
  assert.deepEqual(words('&TV HD'), ['&tv', 'tv', 'hd']);
});

test('a query keeps channels with every word, minus the left-out ones', () => {
  assert.deepEqual(names(''), chs.map((c) => c.name));
  assert.deepEqual(names('hd'), ['STAR PLUS HD', 'ZEE TV HD', '&TV HD']);
  assert.deepEqual(names('-hd'), ['BIG MAGIC', 'SONY SAB', 'Star Bharat', 'News18 India']);
  assert.deepEqual(names('star -hd'), ['Star Bharat']);
  // Word starts, not anywhere in the name: "tv" isn't in "STAR".
  assert.deepEqual(names('tv'), ['ZEE TV HD', '&TV HD']);
  assert.deepEqual(names('news'), ['News18 India']);
  // A number is the channel's number as well.
  assert.deepEqual(names('12'), ['Star Bharat']);
  // A lone minus is no word.
  assert.deepEqual(names('- hd'), names('hd'));
});

test('common words are the ones shared by several channels, but not all', () => {
  assert.deepEqual(commonWords(chs, 5, 2).map((w) => [w.label, w.count]), [['HD', 3], ['STAR', 2], ['TV', 2]]);
  assert.deepEqual(commonWords([{ name: 'Ab HD' }, { name: 'Cd HD' }], 5, 1).map((w) => w.label), ['Ab', 'Cd']);
});

test('a word cycles through wanted, left out and gone', () => {
  let q = 'star';
  q = toggleWord(q, 'HD'); assert.equal(q, 'star HD'); assert.equal(wordState(q, 'HD'), 'want');
  q = toggleWord(q, 'HD'); assert.equal(q, 'star -HD'); assert.equal(wordState(q, 'hd'), 'not');
  q = toggleWord(q, 'HD'); assert.equal(q, 'star'); assert.equal(wordState(q, 'HD'), '');
});
