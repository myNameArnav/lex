// Light/dark theme (Settings → Playback → Theme, per device). "System"
// leaves data-theme off so the CSS follows prefers-color-scheme; Dark and
// Light set it. The browser bar's colour (theme-color) follows. The player
// is dark in every theme (see .player in app.css).

import { prefs } from './prefs.js';

const BG = { dark: '#0c0d10', light: '#f5f6f8' };

export function applyTheme(theme = prefs.get('theme')) {
  const root = document.documentElement;
  const fixed = theme === 'dark' || theme === 'light' ? theme : null;
  if (fixed) root.dataset.theme = fixed; else delete root.dataset.theme;
  // index.html has one theme-color per scheme (by media query); a fixed
  // theme sets both to its colour.
  for (const m of document.querySelectorAll('meta[name="theme-color"]')) {
    const scheme = /light/.test(m.media) ? 'light' : 'dark';
    m.content = BG[fixed || scheme];
  }
}

applyTheme();
