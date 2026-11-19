// The player's two menus: Subtitles & audio, and Settings (quality,
// playback method, speed, chapters, versions, toggles), with their
// sub-pages and arrow-key navigation.
//
// Player methods (installed on the class in player.js). State they use:
// this.menu / menuName / menuPage / menuOpener; choices call back into
// replan(), chooseAudio(), chooseSubtitle() and friends.

import { state } from '../app.js';
import { QUALITIES, prefs, qualityOptions } from '../prefs.js';
import { SUB_STYLE_DEFAULTS, SUB_COLORS, SUB_SIZES, SUB_POSITIONS, subLabel, SUB_FONTS, SUB_OPACITIES, SUB_BG_OPACITIES, SUB_EDGES } from '../substyle.js';
import { h, clear, icons, streamLabel, trackName, channelName, METHOD_LABEL, fmtBitrate, fmtTime, fmtBytes } from '../ui.js';
import { versionLabel, rememberFile } from '../versions.js';
import { SPEEDS } from './format.js';
import { canRemoveSub } from './tracks.js';

export const menus = {
  // Two-level menus: a main list of "Label  value ›" rows and toggles; a row
  // opens a sub-page of choices with a back button.
  // restore: focus goes back to the button that opened the menu, if it was
  // inside the menu.
  closeMenu(restore = true) {
    if (!this.menu) return;
    const hadFocus = this.menu.contains(document.activeElement);
    this.menu.remove();
    this.menu = null; this.menuName = null; this.menuPage = null;
    const opener = this.menuOpener;
    this.menuOpener = null;
    opener?.setAttribute('aria-expanded', 'false');
    if (restore && hadFocus) opener?.focus();
  },

  toggleMenu(name) {
    if (this.menuName === name) { this.closeMenu(); return; }
    this.closeMenu(false);
    this.menuName = name;
    this.menuPage = null;
    this.menuOpener = name === 'tracks' ? this.ccBtn : this.gearBtn;
    this.menuOpener.setAttribute('aria-expanded', 'true');
    this.menu = h('div', { class: 'p-menu', role: 'dialog', 'aria-label': name === 'tracks' ? 'Subtitles and audio' : 'Settings', onclick: (e) => e.stopPropagation() });
    this.root.appendChild(this.menu);
    this.renderMenu();
    // Subtitles are what people change most: start on the chosen one.
    (name === 'tracks' && this.menu.querySelector('.pm-col:last-child .pm-opt.on') || this.menu.querySelector('.pm-row, .pm-opt'))?.focus();
    this.showUI(true);
  },

  // The chapter list opens as a Settings page, so Back leads to Settings.
  toggleChapters() {
    if (this.menuName === 'settings' && this.menuPage === 'chapters') { this.closeMenu(); return; }
    if (this.menuName !== 'settings') this.toggleMenu('settings');
    this.openPage('chapters');
  },

  openPage(page) {
    const from = this.menuPage;
    this.menuPage = page;
    this.renderMenu({ from });
  },

  // Up/Down (and Home/End) move between the menu's controls.
  menuKey(e) {
    const els = [...this.menu.querySelectorAll('button:not(:disabled), input:not(:disabled)')];
    if (!els.length) return;
    const i = els.indexOf(document.activeElement);
    const n = els.length;
    const next = { ArrowDown: i < 0 ? 0 : (i + 1) % n, ArrowUp: i < 0 ? n - 1 : (i - 1 + n) % n, Home: 0, End: n - 1 }[e.key];
    els[next].focus();
  },

  qualityLabel(k) {
    const q = QUALITIES.find(([v]) => v === k);
    return q ? q[1] : 'Original';
  },

  // Re-renders the open menu. Focus follows: to Back when a sub-page opens,
  // to the row it came from when going back (opts.from), otherwise it stays
  // on the same control (e.g. a timing step).
  renderMenu(opts) {
    const m = this.menu;
    if (!m) return;
    const active = m.contains(document.activeElement) ? /** @type {HTMLElement} */ (document.activeElement) : null;
    const key = active?.dataset.key;
    clear(m);
    m.classList.remove('pm-wide');
    this.fillMenu(m);
    let target = null;
    if (opts && 'from' in opts) target = this.menuPage ? m.querySelector('.pm-back') : m.querySelector(`[data-key="${opts.from}"]`) || m.querySelector('.pm-row');
    else if (key) target = m.querySelector(`[data-key="${key}"]`);
    if (target || active) (target || m.querySelector('.pm-row, .pm-opt'))?.focus();
  },

  fillMenu(m) {
    const row = (label, value, page) => h('button', { class: 'pm-row', 'data-key': page, onclick: () => this.openPage(page) },
      h('span', { class: 'pm-label' }, label), h('span', { class: 'pm-val' }, value), h('span', { class: 'pm-chev', html: icons.chevR }));
    const sw = (label, on, set, hint) => h('label', { class: 'pm-row pm-toggle' },
      h('span', { class: 'pm-label' }, label, hint ? h('small', null, hint) : null),
      h('span', { class: 'switch' }, h('input', { type: 'checkbox', 'data-key': `sw-${label}`, checked: on, onchange: (e) => set(e.target.checked) }), h('i')));
    // Choices are a radio group; actions (e.g. 'Search online…') follow it.
    // back: the page Back returns to (the main list by default).
    const head = (title, back = null) => m.append(h('div', { class: 'pm-head' },
      h('button', { class: 'pm-back', 'data-key': 'back', html: icons.chevL, 'aria-label': 'Back', onclick: () => this.openPage(back) }), h('b', null, title)));
    const opt = (it, role) => h('button', { class: `pm-opt ${it.active ? 'on' : ''}`, role, 'aria-checked': role ? String(!!it.active) : null, 'data-key': it.key || null, onclick: it.on },
      h('span', { class: 'ck', html: it.active ? icons.check : '' }),
      h('span', { class: 'pm-opt-l' }, it.label, it.sub ? h('small', null, it.sub) : null),
      it.right ? h('span', { class: 'pm-val' }, it.right) : null);
    const page = (title, items, back = null) => {
      head(title, back);
      m.append(h('div', { class: 'pm-list', role: 'radiogroup', 'aria-label': title }, items.filter((it) => !it.action).map((it) => opt(it, 'radio'))));
      for (const it of items.filter((x) => x.action)) m.append(opt(it, null));
    };
    const auds = (this.file.info?.streams || []).filter((s) => s.type === 'audio');
    const styleCustom = () => Object.keys(SUB_STYLE_DEFAULTS).some((k) => k !== 'subSize' && k !== 'subPos' && String(prefs.get(k)) !== String(SUB_STYLE_DEFAULTS[k]));
    // Subtitle timing: nudges either way, and the offset itself resets it.
    const timing = () => {
      const off = this.subOffset || 0;
      const fmt = `${off > 0 ? '+' : ''}${off.toFixed(1)}s`;
      const step = (d) => h('button', { class: 'pm-step', 'data-key': `step-${d}`, 'aria-label': `Subtitles ${d > 0 ? 'later' : 'earlier'} by ${Math.abs(d)} seconds`, onclick: () => this.setSubOffset(Math.round((off + d) * 10) / 10) }, d > 0 ? `+${d}` : `${d}`);
      return h('div', { class: 'pm-row pm-stepper pm-timing', role: 'group', 'aria-label': 'Subtitle timing' },
        h('span', { class: 'pm-label' }, 'Timing', h('small', null, 'Keys G / H')),
        h('div', { class: 'pm-steps' }, step(-0.5), step(-0.1),
          h('button', { class: 'pm-step pm-zero', 'data-key': 'step-zero', title: 'Reset', 'aria-label': `Reset subtitle timing (now ${fmt})`, onclick: () => this.setSubOffset(0) }, fmt),
          step(0.1), step(0.5)));
    };
    const subs = this.file.subtitles || [];
    // Below 900px the menu spans the subtitles: close it so the change shows.
    const styled = (back = null) => (matchMedia('(max-width: 900px)').matches ? this.closeMenu() : this.openPage(back));
    // A subtitle style choice: saved, applied at once, then back to Style.
    const choose = (title, key, list) => page(title, list.map(([k, l, sub]) => ({
      label: l, sub: typeof sub === 'string' ? sub : null, active: String(prefs.get(key)) === String(k),
      right: list === colors ? swatch(k) : null,
      on: () => { prefs.set(key, k); this.applySubStyle(); styled('style'); },
    })), 'style');
    const swatch = (k) => h('i', { class: 'pm-swatch', style: { background: (SUB_COLORS.find((c) => c[0] === k) || [])[2] } });
    const colors = SUB_COLORS.map(([k, l]) => [k, l]);

    if (this.menuName === 'tracks') {
      switch (this.menuPage) {
        case 'subs':
          return page('Subtitles', [
            { label: 'Off', active: this.subtitle < 0, on: () => { this.chooseSubtitle(-1); this.closeMenu(); } },
            ...subs.map((s) => ({ label: streamLabel(s), active: this.subtitle === s.index, on: () => { this.chooseSubtitle(s.index); this.closeMenu(); } })),
            // Hidden when search isn't set up, unless there are downloads this user may remove.
            state.caps?.subtitleSearch !== false || subs.some((s) => s.downloaded && canRemoveSub(s))
              ? { label: 'Search online…', sub: 'OpenSubtitles.com', action: true, on: () => { this.closeMenu(); this.searchSubtitles(); } } : null,
          ].filter(Boolean));
        case 'audio':
          return page('Audio', auds.map((a) => ({ label: streamLabel(a), active: this.audio === a.index, on: () => { if (a.index !== this.audio) this.chooseAudio(a.index); this.closeMenu(); } })));
        case 'subopts': {
          // Everything about how subtitles look and line up, on one page.
          head('Subtitle settings');
          const cur = this.subById(this.subtitle);
          if (cur && (cur.textSub || this.pgs)) m.append(timing());
          const sizes = SUB_SIZES.map(([k]) => k), si = Math.max(0, sizes.indexOf(prefs.get('subSize')));
          const setSize = (i) => { prefs.set('subSize', sizes[i]); this.applySubStyle(); this.renderMenu(); };
          m.append(h('div', { class: 'pm-row pm-stepper', role: 'group', 'aria-label': 'Subtitle size' },
            h('span', { class: 'pm-label' }, 'Size'),
            h('div', { class: 'pm-seg pm-seg-step' },
              h('button', { class: 'pm-step', 'data-key': 'size-down', 'aria-label': 'Smaller subtitles', disabled: si === 0, onclick: () => setSize(si - 1) }, '−'),
              h('span', { class: 'pm-seg-val', 'aria-live': 'polite' }, SUB_SIZES[si][1]),
              h('button', { class: 'pm-step', 'data-key': 'size-up', 'aria-label': 'Larger subtitles', disabled: si === sizes.length - 1, onclick: () => setSize(si + 1) }, '+'))));
          m.append(h('div', { class: 'pm-row pm-stepper', role: 'group', 'aria-label': 'Subtitle position' },
            h('span', { class: 'pm-label' }, 'Position'),
            h('div', { class: 'pm-seg', role: 'radiogroup', 'aria-label': 'Subtitle position' }, SUB_POSITIONS.map(([k, l]) => h('button', {
              class: `pm-step${prefs.get('subPos') === k ? ' on' : ''}`, role: 'radio', 'aria-checked': String(prefs.get('subPos') === k), 'data-key': `pos-${k}`,
              onclick: () => { prefs.set('subPos', k); this.applySubStyle(); this.renderMenu(); },
            }, l)))));
          m.append(h('div', { class: 'pm-sep' }));
          m.append(row('Style', styleCustom() ? 'Custom' : 'Default', 'style'));
          return;
        }
        case 'style': {
          const srow = (label, value, pg, sw2) => {
            const r = row(label, value, pg);
            if (sw2) r.querySelector('.pm-val').prepend(swatch(sw2));
            return r;
          };
          head('Subtitle style', 'subopts');
          m.append(srow('Font', subLabel(SUB_FONTS, prefs.get('subFont')) || 'Sans-serif', 'sFont'));
          m.append(srow('Text color', subLabel(SUB_COLORS, prefs.get('subColor')) || 'White', 'sColor', prefs.get('subColor')));
          m.append(srow('Text opacity', subLabel(SUB_OPACITIES, prefs.get('subOpacity')) || '100%', 'sOpacity'));
          m.append(sw('Bold', prefs.get('subWeight') !== 'normal', (v) => { prefs.set('subWeight', v ? 'bold' : 'normal'); this.applySubStyle(); }));
          m.append(h('div', { class: 'pm-sep' }));
          m.append(sw('Background box', prefs.get('subBg'), (v) => { prefs.set('subBg', v); this.applySubStyle(); this.renderMenu(); }));
          if (prefs.get('subBg')) {
            m.append(srow('Background color', subLabel(SUB_COLORS, prefs.get('subBgColor')) || 'Black', 'sBgColor', prefs.get('subBgColor')));
            m.append(srow('Background opacity', subLabel(SUB_BG_OPACITIES, prefs.get('subBgOpacity')) || `${Math.round(prefs.get('subBgOpacity') * 100)}%`, 'sBgOpacity'));
          }
          m.append(srow('Edge', subLabel(SUB_EDGES, prefs.get('subEdge')) || 'Automatic', 'sEdge'));
          if (prefs.get('subEdge') !== 'none') m.append(srow('Edge color', subLabel(SUB_COLORS, prefs.get('subEdgeColor')) || 'Black', 'sEdgeColor', prefs.get('subEdgeColor')));
          m.append(h('div', { class: 'pm-sep' }));
          m.append(h('button', { class: 'pm-opt', 'data-key': 'sReset', onclick: () => {
            for (const k of Object.keys(SUB_STYLE_DEFAULTS)) if (k !== 'subSize' && k !== 'subPos') prefs.set(k, SUB_STYLE_DEFAULTS[k]);
            this.applySubStyle(); this.renderMenu();
          } }, h('span', { class: 'ck' }), h('span', { class: 'pm-opt-l' }, 'Reset style', h('small', null, 'Size and position stay'))));
          if (this.jassub) m.append(h('p', { class: 'pm-note' }, 'Styled (ASS) subtitles keep their own font.'));
          else if (this.pgs) m.append(h('p', { class: 'pm-note' }, 'Image (PGS) subtitles can’t be restyled.'));
          return;
        }
        case 'sFont': return choose('Font', 'subFont', SUB_FONTS.map(([k, l]) => [k, l]));
        case 'sColor': return choose('Text color', 'subColor', colors);
        case 'sOpacity': return choose('Text opacity', 'subOpacity', SUB_OPACITIES);
        case 'sBgColor': return choose('Background color', 'subBgColor', colors);
        case 'sBgOpacity': return choose('Background opacity', 'subBgOpacity', SUB_BG_OPACITIES);
        case 'sEdge': return choose('Edge', 'subEdge', SUB_EDGES);
        case 'sEdgeColor': return choose('Edge color', 'subEdgeColor', colors);
      }
      // Main page: the two track lists side by side, so a pick is one click.
      // Sound effects sit under the audio list; how subtitles look and their
      // timing are one row away.
      const pick = (title, key, items, extra) => h('div', { class: 'pm-col' },
        h('div', { class: 'pm-title' }, title),
        h('div', { class: 'pm-list', role: 'radiogroup', 'aria-label': title }, items.map((it) => opt(it, 'radio'))),
        extra);
      const audLabel = (a) => ({ label: trackName(a), sub: [(a.codec || '').toUpperCase(), channelName(a.channels)].filter(Boolean).join(' '), key: `a-${a.index}` });
      const subLabel2 = (s) => ({ label: trackName(s) + (s.forced ? ' (Forced)' : '') + (s.hearingImpaired ? ' (SDH)' : ''),
        sub: [s.external ? 'External' : '', s.downloaded ? 'Downloaded' : '', !s.textSub && s.codec !== 'hdmv_pgs_subtitle' ? 'Burned in' : ''].filter(Boolean).join(' · '), key: `s-${s.index}` });
      const fx = auds.length && this.fxAvailable() ? h('div', { class: 'pm-chips', role: 'group', 'aria-label': 'Sound' },
        /** @type {[string, string, string, () => void][]} */ ([['nightMode', 'Night mode', 'Quiet parts louder, loud parts quieter', () => this.applyFx()],
          ['voiceBoost', 'Boost voices', 'Clearer dialogue', () => { this.applyFx(); if (this.voiceNeedsReplan()) this.replan(); }]]).map(([k, l, tip, apply]) => h('button', {
          class: `pm-chip${prefs.get(k) ? ' on' : ''}`, 'aria-pressed': String(!!prefs.get(k)), title: tip, 'data-key': `fx-${k}`,
          onclick: () => { prefs.set(k, !prefs.get(k)); apply(); this.renderMenu(); },
        }, h('span', { class: 'ck', html: prefs.get(k) ? icons.check : '' }), l))) : null;
      const search = state.caps?.subtitleSearch !== false || subs.some((s) => s.downloaded && canRemoveSub(s))
        ? h('button', { class: 'pm-opt pm-action', 'data-key': 'search', onclick: () => { this.closeMenu(); this.searchSubtitles(); } },
          h('span', { class: 'ck', html: icons.search || '' }), h('span', { class: 'pm-opt-l' }, 'Search online…')) : null;
      m.classList.add('pm-wide');
      m.append(h('div', { class: 'pm-cols' },
        auds.length ? pick('Audio', 'a', auds.map((a) => ({ ...audLabel(a), active: this.audio === a.index, on: () => { if (a.index !== this.audio) this.chooseAudio(a.index); this.closeMenu(); } })), fx) : null,
        pick('Subtitles', 's', [
          { label: 'Off', key: 's-off', active: this.subtitle < 0, on: () => { this.chooseSubtitle(-1); this.closeMenu(); } },
          ...subs.map((s) => ({ ...subLabel2(s), active: this.subtitle === s.index, on: () => { this.chooseSubtitle(s.index); this.closeMenu(); } })),
        ], search)));
      m.append(h('div', { class: 'pm-sep' }));
      const off = this.subOffset || 0;
      const summary = [off ? `${off > 0 ? '+' : ''}${off.toFixed(1)}s` : '', subLabel(SUB_SIZES, prefs.get('subSize')) || 'Medium', styleCustom() ? 'custom style' : ''].filter(Boolean).join(' · ');
      m.append(row('Subtitle settings', summary, 'subopts'));
      return;
    }

    // settings
    const src = this.file?.info?.bitrate ? Math.round(this.file.info.bitrate / 1000) : 0;
    const modes = [
      ['auto', 'Automatic', `${METHOD_LABEL.direct}, then ${METHOD_LABEL.remux}, then ${METHOD_LABEL.transcode}`],
      ['direct', METHOD_LABEL.direct, 'Original file'],
      ['remux', METHOD_LABEL.remux, 'New container; video untouched'],
      ['transcode', METHOD_LABEL.transcode, 'Re-encode on the server'],
      ['hls', 'HLS', 'For AirPlay and older devices'],
    // HLS needs native support (Safari, iOS); elsewhere it only falls back.
    ].filter(([k]) => k !== 'hls' || this.mode === 'hls' || this.nativeHls());
    const upper = (t) => t.charAt(0).toUpperCase() + t.slice(1);
    // [pref, label, short name for the summary, apply]
    /** @type {() => [string, string, string, ((on: boolean) => void)?][]} */
    const autoOpts = () => [
      ['autoSkipIntro', 'Skip intros', 'intros'],
      ['autoSkipCredits', 'Skip credits', 'credits'],
      ['autoplayNext', 'Autoplay next episode', 'next episode', (v) => { if (!v) this.cancelUpNext(false); }],
    ];
    switch (this.menuPage) {
      case 'quality':
        return page('Quality', qualityOptions({ maxHeight: state.caps?.maxTranscodeHeight, srcHeight: this.file?.height, srcKbps: src})
          .filter(([k]) => !k || !src || k <= src * 1.5 || k === this.quality).map(([k, l]) => ({
          label: l, active: this.quality === k, right: k === 0 && src ? fmtBitrate(src * 1000) : '',
          on: () => { this.quality = k; prefs.set('quality', k); this.closeMenu(); this.replan(); },
        })));
      case 'advanced':
        // The playback method is a fix for a file that won't play, so it
        // sits with the diagnostics rather than with the everyday controls.
        page('Advanced', modes.map(([k, l, d]) => ({ label: l, sub: d, key: `mode-${k}`, active: this.mode === k, on: () => { this.mode = k; prefs.set('mode', k); this.fallbacks = 0; this.swEncode = false; this.noClientAudio = false; this.noClientRemux = false; this.closeMenu(); this.replan(); } })));
        m.querySelector('.pm-list').before(sw('Stats for nerds', !!this.statsEl, () => this.toggleStats(), 'Shortcut: I'), h('div', { class: 'pm-sep' }), h('div', { class: 'pm-title pm-subtitle' }, 'Playback method'));
        return;
      case 'auto':
        head('Autoplay & skipping');
        for (const [k, l, , apply] of autoOpts()) m.append(sw(l, prefs.get(k), (v) => { prefs.set(k, v); apply?.(v); }));
        return;
      case 'speed':
        // The default rate survives load(), so the speed outlasts track,
        // quality and method changes, fallbacks and the next episode.
        return page('Speed', SPEEDS.map((sp) => ({ label: sp === 1 ? 'Normal' : `${sp}x`, active: this.video.playbackRate === sp, on: () => { this.video.defaultPlaybackRate = this.video.playbackRate = sp; this.openPage(null); } })));
      case 'chapters': {
        const list = this.chapters(), cur = this.chapterAt(this.video.currentTime, list);
        return page('Chapters', list.map((c, i) => ({ label: c.label, right: fmtTime(c.start), active: i === cur, on: () => { this.seekTo(c.start); this.closeMenu(); } })));
      }
      case 'version':
        return page('Version', this.files.map((f) => ({ label: versionLabel(f, this.files, { size: false }), right: fmtBytes(f.size), active: this.file.id === f.id, on: () => { this.file = { ...f, subtitles: f.subtitles }; rememberFile(this.item.id, f.id, this.files); this.chaptersDrawn = false; this.closeMenu(); this.replan(); } })));
    }
    m.append(h('div', { class: 'pm-title' }, 'Settings'));
    // Everyday controls first; set-and-forget preferences and the
    // diagnostics are one level down.
    m.append(row('Speed', this.video.playbackRate === 1 ? 'Normal' : `${this.video.playbackRate}x`, 'speed'));
    m.append(row('Quality', this.qualityLabel(this.quality), 'quality'));
    const chaps = this.chapters();
    if (chaps.length > 1) m.append(row('Chapters', chaps[this.chapterAt(this.video.currentTime, chaps)]?.label || chaps[0].label, 'chapters'));
    if (this.files.length > 1) m.append(row('Version', versionLabel(this.files.find((f) => f.id === this.file.id) || this.file, this.files, { size: false }), 'version'));
    m.append(h('div', { class: 'pm-sep' }));
    const auto = autoOpts().filter(([k]) => prefs.get(k)).map(([, , short]) => short);
    m.append(row('Autoplay & skipping', auto.length ? upper(auto.join(', ')) : 'Off', 'auto'));
    m.append(row('Advanced', this.statsEl ? 'Stats on' : this.mode === 'auto' ? '' : (modes.find(([k]) => k === this.mode) || modes[0])[1], 'advanced'));
  },
};
