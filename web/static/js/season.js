/** @typedef {import('../../types/api.gen').Item} Item */

// Season actions on a show page: the ⋯ menu beside the season tabs. Kept
// free of the DOM so the menu's contents can be tested on their own.

// seasonMenuItems lists the actions for season s (a season item with
// childCount and unplayedCount, both left out of the JSON when 0) as
// popupMenu items. mark(played) and shuffle() run the actions. A partly
// watched season offers both mark actions.
/** @param {Item} s */
export function seasonMenuItems(s, { mark, shuffle }) {
  const total = s.childCount || 0;
  if (!total) return [{ note: 'No episodes in this season yet' }];
  const unplayed = Math.min(s.unplayedCount || 0, total);
  const items = [];
  if (unplayed > 0) items.push({ label: 'Mark season watched', icon: 'check', onClick: () => mark(true) });
  if (unplayed < total) items.push({ label: 'Mark season unwatched', icon: 'check', onClick: () => mark(false) });
  if (total > 1) items.push({ label: 'Shuffle season', icon: 'shuffle', onClick: shuffle });
  return items;
}

// seasonConfirm is the confirmation for marking every episode of season s
// of show showTitle, worded like the show-level watched button's.
/** @param {Item} s */
export function seasonConfirm(s, showTitle, played) {
  const n = s.childCount || 0;
  return {
    message: `Mark all ${n} episode${n === 1 ? '' : 's'} of “${showTitle}” ${s.title} as ${played ? 'watched' : 'unwatched'}? This clears resume points${played ? '' : ' and watch history'}.`,
    okLabel: played ? 'Mark watched' : 'Mark unwatched',
    danger: !played,
    title: played ? 'Mark season watched?' : 'Mark season unwatched?',
  };
}
