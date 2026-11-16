import assert from 'node:assert/strict';
import test from 'node:test';
import { seasonMenuItems, seasonConfirm } from '../static/js/season.js';

const labels = (items) => items.map((i) => i.label || i.note);

function menu(s) {
  const calls = [];
  const items = seasonMenuItems(s, { mark: (p) => calls.push(['mark', p]), shuffle: () => calls.push(['shuffle']) });
  return { items, calls };
}

test('an unwatched season offers mark watched and shuffle', () => {
  const { items, calls } = menu({ title: 'Season 1', childCount: 8, unplayedCount: 8 });
  assert.deepEqual(labels(items), ['Mark season watched', 'Shuffle season']);
  items[0].onClick();
  items[1].onClick();
  assert.deepEqual(calls, [['mark', true], ['shuffle']]);
});

test('a watched season (unplayedCount left out of the JSON) offers mark unwatched', () => {
  const { items, calls } = menu({ title: 'Season 1', childCount: 8 });
  assert.deepEqual(labels(items), ['Mark season unwatched', 'Shuffle season']);
  items[0].onClick();
  assert.deepEqual(calls, [['mark', false]]);
});

test('a partly watched season offers both, a single episode no shuffle', () => {
  assert.deepEqual(labels(menu({ title: 'S', childCount: 8, unplayedCount: 3 }).items), ['Mark season watched', 'Mark season unwatched', 'Shuffle season']);
  assert.deepEqual(labels(menu({ title: 'S', childCount: 1, unplayedCount: 1 }).items), ['Mark season watched']);
});

test('an empty season says so instead of offering actions', () => {
  assert.deepEqual(labels(menu({ title: 'Specials' }).items), ['No episodes in this season yet']);
});

test('the confirmation matches the show-level wording', () => {
  const s = { title: 'Season 2', childCount: 10 };
  assert.deepEqual(seasonConfirm(s, 'Show', true), {
    message: 'Mark all 10 episodes of “Show” Season 2 as watched? This clears resume points.',
    okLabel: 'Mark watched', danger: false, title: 'Mark season watched?',
  });
  const un = seasonConfirm({ title: 'Season 1', childCount: 1 }, 'Show', false);
  assert.equal(un.message, 'Mark all 1 episode of “Show” Season 1 as unwatched? This clears resume points and watch history.');
  assert.equal(un.danger, true);
  assert.equal(un.title, 'Mark season unwatched?');
});
