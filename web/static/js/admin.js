import { h, icons, motionOK, emptyState } from './ui.js';
import { state, focusAfterRoute } from './app.js';
import { GROUPS, PAGES, searchBox, reveal } from './settings-search.js';
import { accountSection } from './admin/account.js';
import { librariesSection } from './admin/libraries.js';
import { liveTvSection } from './admin/livetv.js';
import { cacheSection, backupsSection, logsSection, aboutSection } from './admin/maintenance.js';
import { playbackPrefsSection, subtitlePrefsSection, interfacePrefsSection } from './admin/preferences.js';
import { arrPage, generalSection, networkSection, transcodingSection, metadataSection, analysisSection } from './admin/server.js';
import { usersSection, devicesSection } from './admin/users.js';

// app.js loads this module for both pages.
export { dashboardView } from './admin/dashboard.js';

// ======================================================================
// Settings
// ======================================================================

// The Settings page: a side column (search, then the page list) beside one
// content column of a fixed width, so switching pages never moves the nav or
// changes the column's width. Pages are listed in settings-search.js.
let pendingReveal = null; // a search result to show once its page renders

export async function settingsView(ctx, section) {
  // Following a link keeps focus on it (not on the page heading).
  const link = ([id, label, ic]) => h('a', { href: `#/settings/${id}`, class: id === section ? 'active' : '', 'aria-current': id === section ? 'page' : null,
    dataset: { focusKey: `settings-nav-${id}`, page: id }, onclick: () => { if (id !== section) focusAfterRoute(`settings-nav-${id}`); } }, h('span', { html: icons[ic] }), label);
  const nav = h('nav', { class: 'side-nav', 'aria-label': 'Settings sections' },
    GROUPS.filter((g) => g === 'You' || state.me.isAdmin).map((g) => [h('div', { class: 'grp' }, g), PAGES.filter((s) => s[4] === g).map(link)]));
  const content = h('div', { class: 'settings-content' });
  const search = searchBox({ isAdmin: state.me.isAdmin, choose: (e) => {
    if (e.page === section) { reveal(content, e, { smooth: motionOK() }); return; }
    pendingReveal = e;
    location.hash = `#/settings/${e.page}`;
  } });
  search.input.setAttribute('aria-keyshortcuts', 'Control+K Meta+K /');
  const def = PAGES.find((s) => s[0] === section);
  if (!def) content.appendChild(emptyState({ level: 'h2', title: 'Not found', text: 'There’s no settings page here.', actions: [h('a', { class: 'btn', href: '#/settings/preferences' }, 'Playback settings')] }));
  else if (def[3] && !state.me.isAdmin) {
    content.appendChild(emptyState({ level: 'h2', title: 'Admins only', text: 'Ask an administrator to change server settings.',
      actions: [h('a', { class: 'btn', href: '#/settings/preferences' }, 'Playback settings')] }));
  } else content.appendChild(await ({
    preferences: playbackPrefsSection, subtitles: subtitlePrefsSection, interface: interfacePrefsSection, account: accountSection,
    libraries: librariesSection, metadata: metadataSection, analysis: analysisSection, arr: arrPage, livetv: liveTvSection,
    transcoding: transcodingSection, network: networkSection, cache: cacheSection,
    server: generalSection, users: usersSection, devices: devicesSection, backups: backupsSection, logs: logsSection, about: aboutSection,
  })[section](ctx));
  // A search result on another page: show it once the route has put this
  // page up (and focused its heading).
  const want = pendingReveal;
  pendingReveal = null;
  if (want && want.page === section) setTimeout(() => { if (ctx.isCurrent() && content.isConnected) reveal(content, want, { smooth: false }); });
  // On narrow screens the nav is a horizontal strip: once the view is shown,
  // bring the current section into view (scrollLeft, so the page doesn't scroll).
  requestAnimationFrame(() => {
    const a = nav.querySelector('a.active');
    if (a && nav.scrollWidth > nav.clientWidth) nav.scrollLeft += a.getBoundingClientRect().left - nav.getBoundingClientRect().left - (nav.clientWidth - a.offsetWidth) / 2;
  });
  const view = h('div', { class: 'page settings' }, h('h1', { class: 'page-title' }, 'Settings'),
    h('div', { class: 'settings-layout' }, h('aside', { class: 'settings-side' }, search.el, nav), content));
  // While the next settings page loads, this one stays up: the nav marks the
  // page being opened at once and the content dims (see route() in app.js).
  view.pendingRoute = (next, id) => {
    if (next !== 'settings') return false;
    const to = id || 'preferences';
    nav.querySelectorAll('a').forEach((a) => {
      const on = a.dataset.page === to;
      a.classList.toggle('active', on);
      if (on) a.setAttribute('aria-current', 'page'); else a.removeAttribute('aria-current');
    });
    content.classList.add('pending');
    content.setAttribute('aria-busy', 'true');
    return true;
  };
  return view;
}

