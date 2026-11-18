// The home page: Continue watching, Next up, recently added and the other rows.

import { h, emptyState } from '../ui.js';
import { state, takeEarly, refreshLater } from '../app.js';
import { shelf } from './cards.js';

export async function homeView(ctx) {
  const data = await takeEarly('/api/home');
  const wrap = h('div', { style: { paddingTop: '26px', paddingBottom: '40px' } }, h('h1', { class: 'sr-only' }, 'Home'));
  const admin = state.me.isAdmin;
  if (!data.libraries?.length) {
    wrap.appendChild(emptyState({ level: 'h2', title: 'No libraries yet',
      text: admin ? 'Add a folder with your movies or shows to get started.' : 'Ask your server admin to add a library.',
      actions: admin ? [h('a', { class: 'btn primary', href: '#/settings/libraries' }, 'Add a library')] : [] }));
    return wrap;
  }
  if (!data.rows.length) {
    // A library that has never finished a scan may still fill up: check again shortly.
    if (data.libraries.some((l) => !l.lastScan)) {
      wrap.appendChild(emptyState({ level: 'h2', title: 'Scanning your libraries…', text: 'New titles will show up here as they are found.' }));
      refreshLater(ctx, 5000);
    } else {
      wrap.appendChild(emptyState({ level: 'h2', title: 'No media found',
        text: admin ? 'Check that your library folders contain files, e.g. Movies/Title (Year)/Title (Year).mkv.' : 'Ask your server admin to check the library folders.',
        actions: admin ? [h('a', { class: 'btn primary', href: '#/settings/libraries' }, 'Check library folders')] : [] }));
    }
  }
  for (const row of data.rows) {
    wrap.appendChild(shelf(row.title, row.items, row.kind, row.libraryId ? `#/library/${row.libraryId}?sort=latest&desc=1` : HOME_ROW_PAGES[row.id] || null));
  }
  return wrap;
}

// The Home rows with a page of their own behind "See all".
const HOME_ROW_PAGES = { watchlist: '#/watchlist', favorites: '#/favorites' };
