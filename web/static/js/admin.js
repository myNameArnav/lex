import { h, clear, icons, toast, modal, confirmDialog, spinner, toggle, fmtBytes, fmtBitrate, fmtTime, fmtDate, timeAgo, fmtUptime, fmtDuration, LANG_OPTIONS, $, run, staleBanner, motionOK, KIND_LABELS, METHOD_LABEL, reasonLabel, emptyState } from './ui.js';
import { api, img } from './api.js';
import { state, loadLibraries, route, refreshSoft, setLeaveGuard, focusAfterRoute } from './app.js';
import { prefs, DEFAULTS, QUALITIES } from './prefs.js';
import { capsSummary } from './caps.js';
import { lineChart, columnChart, sparkline, barList, SERIES } from './charts.js';

// Pollers owned by the current admin view. Each stops at its next tick once
// its view is no longer current (navigated away or re-rendered; a navigation
// cancelled by the leave guard keeps them running), and all stop when the
// session expires.
let pollers = [];
function stopTimers() { pollers.forEach((p) => clearInterval(p.t)); pollers = []; }
// every polls fn every ms while ctx is current. Ticks are skipped while the
// tab is hidden or a dialog is open (a re-render would pull the dialog's
// opener out from under it), and run at once when the tab is shown again.
// fn should throw (or reject) when its request fails: after two failures in
// a row onFail(lastOkAt) is called, and onRecover() after the next success.
function every(ctx, ms, fn, { onFail, onRecover } = {}) {
  let fails = 0, lastOk = Date.now(), busy = false;
  const p = {};
  p.tick = async () => {
    if (!ctx.isCurrent()) { clearInterval(p.t); pollers = pollers.filter((x) => x !== p); return; }
    if (busy || document.hidden || document.querySelector('dialog[open]')) return;
    busy = true;
    try {
      await fn();
      if (fails >= 2) onRecover?.();
      fails = 0;
      lastOk = Date.now();
    } catch {
      if (++fails === 2) onFail?.(lastOk);
    } finally {
      busy = false;
    }
  };
  p.t = setInterval(p.tick, ms);
  pollers.push(p);
  return p.t;
}
window.addEventListener('lex:unauthorized', stopTimers);
document.addEventListener('visibilitychange', () => { if (!document.hidden) pollers.forEach((p) => p.tick()); });

// ======================================================================
// Settings
// ======================================================================

const SECTIONS = [
  ['preferences', 'Playback', 'sliders', false],
  ['account', 'Account', 'user', false],
  ['server', 'Server', 'gear', true],
  ['transcoding', 'Transcoding', 'cpu', true],
  ['metadata', 'Metadata', 'film', true],
  ['cache', 'SSD cache', 'drive', true],
  ['libraries', 'Libraries', 'library', true],
  ['users', 'Users', 'users', true],
  ['devices', 'Devices', 'devices', true],
  ['logs', 'Tasks & logs', 'log', true],
  ['about', 'About', 'info', true],
];

export async function settingsView(ctx, section) {
  // Following a link keeps focus on it (not on the page heading).
  const link = ([id, label, ic]) => h('a', { href: `#/settings/${id}`, class: id === section ? 'active' : '', 'aria-current': id === section ? 'page' : null,
    dataset: { focusKey: `settings-nav-${id}` }, onclick: () => { if (id !== section) focusAfterRoute(`settings-nav-${id}`); } }, h('span', { html: icons[ic] }), label);
  const nav = h('nav', { class: 'side-nav', 'aria-label': 'Settings sections' },
    h('div', { class: 'grp' }, 'You'),
    SECTIONS.filter((s) => !s[3]).map(link),
    state.me.isAdmin ? [h('div', { class: 'grp' }, 'Server'), SECTIONS.filter((s) => s[3]).map(link)] : null);
  const content = h('div');
  const def = SECTIONS.find((s) => s[0] === section);
  if (!def) content.appendChild(emptyState({ level: 'h2', title: 'Not found', text: 'There’s no settings page here.', actions: [h('a', { class: 'btn', href: '#/settings/preferences' }, 'Playback settings')] }));
  else if (def[3] && !state.me.isAdmin) {
    content.appendChild(emptyState({ level: 'h2', title: 'Admins only', text: 'Ask an administrator to change server settings.',
      actions: [h('a', { class: 'btn', href: '#/settings/preferences' }, 'Playback settings')] }));
  } else content.appendChild(await ({
    preferences: prefsSection, account: accountSection, server: serverSection, transcoding: transcodingSection,
    metadata: metadataSection, cache: cacheSection, libraries: librariesSection, users: usersSection, devices: devicesSection, logs: logsSection, about: aboutSection,
  })[section](ctx));
  // On narrow screens the nav is a horizontal strip: once the view is shown,
  // bring the current section into view (scrollLeft, so the page doesn't scroll).
  requestAnimationFrame(() => {
    const a = nav.querySelector('a.active');
    if (a && nav.scrollWidth > nav.clientWidth) nav.scrollLeft += a.getBoundingClientRect().left - nav.getBoundingClientRect().left - (nav.clientWidth - a.offsetWidth) / 2;
  });
  return h('div', { class: 'page' }, h('h1', { class: 'page-title', style: { marginBottom: '22px' } }, 'Settings'), h('div', { class: 'side-layout' }, nav, content));
}

// field wraps a control with its label and help. The label alone names the
// control; the help describes it (so it isn't read as part of the name).
// help may be text or a ready .help element.
let fieldId = 0;
function field(label, input, help) {
  const id = `field-${++fieldId}`;
  const helpEl = help == null ? null : help instanceof Node ? help : h('div', { class: 'help' }, help);
  if (helpEl) helpEl.id = `${id}-h`;
  if (input.matches?.('input, select, textarea')) {
    // aria-label for plain text: Chrome would read the label's CSS uppercase into the name.
    if (typeof label === 'string') input.setAttribute('aria-label', label);
    else input.setAttribute('aria-labelledby', `${id}-l`);
    if (helpEl) input.setAttribute('aria-describedby', [helpEl.id, input.getAttribute('aria-describedby')].filter(Boolean).join(' '));
  }
  return h('label', { class: 'field' }, h('span', { id: `${id}-l` }, label), input, helpEl);
}

let toggleId = 0;
function toggleRow(label, help, checked, onchange, { disabled = false } = {}) {
  const control = toggle(checked, onchange, label);
  const helpId = `toggle-help-${++toggleId}`;
  if (help) control.firstChild.setAttribute('aria-describedby', helpId);
  control.firstChild.disabled = disabled;
  return h('label', { class: 'toggle-row' }, h('span', { class: 'lbl' }, h('b', null, label), help ? h('span', { class: 'help', id: helpId }, help) : null), control);
}

function select(value, options, onchange) {
  return h('select', { onchange: (e) => onchange(e.target.value) }, options.map(([v, l]) => h('option', { value: String(v), selected: String(v) === String(value) }, l)));
}

// ---------- per-device playback prefs ----------
let capsOpen = false; // stays open across re-renders
function prefsSection() {
  const p = prefs.all();
  const set = (k, conv = (x) => x) => (v) => { prefs.set(k, conv(v)); toast('Saved on this device', 'ok'); };
  const langs = [['', 'File default'], ...LANG_OPTIONS];
  const caps = capsSummary();
  return h('div', { class: 'form cols' },
    h('div', { class: 'form-section' }, h('h2', null, 'Quality & method'),
      h('p', { class: 'help', style: { margin: 0 } }, 'These settings are stored in this browser, so each device can have its own (e.g. lower quality on your phone).'),
      h('div', { class: 'form-grid' },
        field('Streaming quality', select(p.quality, QUALITIES, set('quality', Number)), 'Max bitrate. "Original" direct-plays when the browser supports the file.'),
        field('Playback method', select(p.mode, [['auto', 'Automatic (recommended)'], ['direct', 'Prefer Direct Play'], ['remux', 'Always Direct Stream (remux)'], ['transcode', 'Always Transcode']], set('mode')), 'Automatic picks the lightest method that works here. Try “Always Transcode” if a video stutters or won’t play.'))),
    h('div', { class: 'form-section' }, h('h2', null, 'Buffering'),
      h('div', { class: 'form-grid' },
        field('Buffer ahead', select(p.bufferAhead, [[0, 'Automatic (recommended)'], [30, 'At most 30 seconds'], [60, 'At most 1 minute'], [90, 'At most 90 seconds'], [180, 'At most 3 minutes'], [300, 'At most 5 minutes']], set('bufferAhead', Number)), 'For direct stream and transcode (direct play is always buffered by the browser). Automatic fills as much as the browser allows, usually about 150 MB. A limit keeps a transcode from running far ahead of what you watch.'),
        field('Keep behind playhead', select(p.backBuffer, [[10, '10 seconds'], [30, '30 seconds'], [60, '1 minute'], [120, '2 minutes']], set('backBuffer', Number)), 'Lets you rewind instantly; lower saves memory on phones/TVs.'),
        field('Progress update interval', select(p.heartbeat, [[5, '5 seconds'], [10, '10 seconds'], [30, '30 seconds']], set('heartbeat', Number)), 'How often your position is saved, so you can resume on another device.'))),
    h('div', { class: 'form-section' }, h('h2', null, 'Audio & subtitles'),
      h('div', { class: 'form-grid' },
        field('Preferred audio language', select(p.audioLang, langs, set('audioLang'))),
        field('Subtitles', select(p.subMode, [['off', 'Off'], ['auto', 'Auto (forced / foreign audio)'], ['always', 'Always on']], set('subMode'))),
        field('Subtitle language', select(p.subLang, LANG_OPTIONS, set('subLang'))),
        field('Subtitle size', select(p.subSize, [['small', 'Small'], ['medium', 'Medium'], ['large', 'Large']], set('subSize'))),
        field('Subtitle position', select(p.subPos, [['low', 'Low'], ['normal', 'Normal'], ['high', 'High']], set('subPos')))),
      toggleRow('Subtitle background box', null, p.subBg, set('subBg'))),
    h('div', { class: 'form-section' }, h('h2', null, 'Player'),
      toggleRow('Autoplay next episode', 'Shows an "Up next" card near the end with a countdown.', p.autoplayNext, set('autoplayNext')),
      toggleRow('Skip intros automatically', 'Otherwise a "Skip Intro" button appears while an intro plays.', p.autoSkipIntro, set('autoSkipIntro')),
      toggleRow('Show stats for nerds by default', 'Overlay with codecs, bitrate, buffer health, dropped frames and server transcode speed (press i in the player).', p.showStats, set('showStats')),
      toggleRow('Single-key shortcuts', 'p, /, ? and g-then-key. Ctrl/⌘ K and the player keys always work.', p.shortcuts, set('shortcuts')),
      h('div', { class: 'form-grid' },
        field('Autoplay countdown', select(p.countdown, [[5, '5 seconds'], [10, '10 seconds'], [15, '15 seconds'], [30, '30 seconds']], set('countdown', Number))),
        field('Skip back', select(p.skipBack, [[5, '5 seconds'], [10, '10 seconds'], [15, '15 seconds'], [30, '30 seconds']], set('skipBack', Number))),
        field('Skip forward', select(p.skipFwd, [[10, '10 seconds'], [15, '15 seconds'], [30, '30 seconds'], [60, '60 seconds']], set('skipFwd', Number))))),
    h('div', { class: 'form-section' }, h('details', { class: 'caps', open: capsOpen, ontoggle: (e) => { capsOpen = e.target.open; } }, h('summary', null, h('h2', null, 'Technical: this browser’s codecs')),
      h('dl', { class: 'kv' }, Object.entries(caps).map(([k, v]) => [h('dt', null, k), h('dd', null, v)])))),
    h('div', null, h('button', { class: 'btn', dataset: { focusKey: 'prefs-reset' }, onclick: resetPrefs }, 'Reset to defaults')));
}

// Volume and the stats panel's compact state aren't shown on this page, so a
// reset leaves them as they are.
async function resetPrefs() {
  if (!(await confirmDialog('Reset playback settings on this device to their defaults?', 'Reset', true, 'Reset playback settings?'))) return;
  const keep = ['volume', 'muted', 'statsCompact'].map((k) => [k, prefs.get(k)]);
  prefs.reset();
  keep.forEach(([k, v]) => prefs.set(k, v));
  toast('Reset to defaults', 'ok');
  refreshSoft();
}

// formErrors gives a form an inline error line (role=alert) and helpers to
// flag fields: fail(msg, ...inputs) marks them invalid and focuses the first.
function formErrors(id, inputs) {
  const err = h('div', { class: 'err', role: 'alert', id });
  for (const i of inputs) {
    i.setAttribute('aria-describedby', [i.getAttribute('aria-describedby'), id].filter(Boolean).join(' '));
    i.addEventListener('input', () => i.removeAttribute('aria-invalid'));
  }
  const reset = () => { err.textContent = ''; inputs.forEach((i) => i.removeAttribute('aria-invalid')); };
  const fail = (msg, ...bad) => {
    err.textContent = msg;
    bad.forEach((i) => i.setAttribute('aria-invalid', 'true'));
    bad[0]?.focus();
  };
  return { err, reset, fail };
}

// Runs a form's request through run() (busy button, no double submits), with
// the server's error shown inline instead of as a toast; pick(message) names
// the inputs to flag. Resolves true on success.
async function submitInline(btn, errors, fn, okMsg, pick = () => []) {
  let failed = null;
  const ok = await run(btn, async () => {
    try { return await fn(); } catch (ex) { failed = ex; throw Object.assign(new Error(ex.message), { name: 'AbortError' }); }
  }, okMsg);
  if (failed) errors.fail(upper(failed.message), ...pick(failed.message));
  return ok;
}

const PW_RULE = { minlength: 12, maxlength: 72 };
const PW_MSG = 'Use 12–72 bytes (at least 12 characters).';
const pwLength = (v) => { const n = new TextEncoder().encode(v).length; return n >= 12 && n <= 72; };

function accountSection() {
  const cur = h('input', { type: 'password', name: 'current-password', autocomplete: 'current-password', required: true });
  const n1 = h('input', { type: 'password', name: 'new-password', autocomplete: 'new-password', required: true, ...PW_RULE });
  const n2 = h('input', { type: 'password', name: 'confirm-password', autocomplete: 'new-password', required: true, ...PW_RULE });
  const errors = formErrors('account-pw-err', [cur, n1, n2]);
  const btn = h('button', { class: 'btn primary', type: 'submit' }, 'Change password');
  // Wrong or empty tries count toward the sign-in limit, so nothing is sent
  // until the form is complete.
  const onsubmit = async (e) => {
    e.preventDefault();
    errors.reset();
    const empty = [cur, n1, n2].filter((i) => !i.value);
    if (empty.length) return errors.fail(empty[0] === cur ? 'Enter your current password' : 'Enter the new password twice', ...empty);
    if (!pwLength(n1.value)) return errors.fail(PW_MSG, n1);
    if (n1.value !== n2.value) return errors.fail('The new passwords don’t match', n2);
    if (await submitInline(btn, errors, () => api('/api/me/password', { method: 'PUT', body: { current: cur.value, new: n1.value } }), 'Password changed; other devices were signed out',
      (m) => (/current/i.test(m) ? [cur] : /password must/i.test(m) ? [n1] : []))) cur.value = n1.value = n2.value = '';
  };
  return h('div', { class: 'form' }, h('form', { class: 'form-section', style: { maxWidth: '480px' }, novalidate: true, onsubmit },
    h('h2', null, `Signed in as ${state.me.name}`),
    h('p', { class: 'muted', style: { margin: 0 } }, state.me.isAdmin ? 'Administrator' : 'User'),
    // Lets password managers file the new password under this account.
    h('input', { type: 'text', name: 'username', autocomplete: 'username', value: state.me.name, hidden: true, readonly: true, tabindex: -1 }),
    field('Current password', cur), field('New password', n1, 'Use 12–72 bytes.'), field('Confirm new password', n2),
    errors.err,
    h('div', null, btn)));
}

// ---------- server config (shared by several sections) ----------
// configForm edits /api/admin/config through a draft: Save (or Enter in a
// field) sends it, Save stays disabled until something differs from the
// saved config, and leaving the page with unsaved edits asks first.
// check(draft) may return [message, control] to stop a save with an inline error.
async function configForm(build, { check } = {}) {
  let cfg = await api('/api/admin/config');
  const info = await api('/api/admin/info');
  const draft = { ...cfg };
  let clean = JSON.stringify(cfg);
  const dirty = () => JSON.stringify(draft) !== clean;
  const bind = (k, conv = (x) => x) => (v) => { draft[k] = conv(v); };
  // Number fields: an empty (or unreadable) field keeps the saved value, and
  // shows it again on leaving the field. Save checks the ranges first.
  const controls = new Map();
  const num = (k, attrs = {}) => {
    const input = h('input', { type: 'number', inputmode: 'numeric', value: draft[k], ...attrs,
      oninput: (e) => { const v = e.target.valueAsNumber; draft[k] = Number.isNaN(v) ? cfg[k] : v; input.setCustomValidity(''); },
      onblur: () => { if (input.value === '' && !input.validity.badInput) { input.value = draft[k]; mark(); } } });
    controls.set(k, input);
    return input;
  };
  const text = (k, attrs = {}) => h('input', { type: 'text', value: draft[k] ?? '', ...attrs, oninput: (e) => { draft[k] = e.target.value; } });
  // A comma-separated list shown in a box that wraps; a new line counts as a comma.
  const list = (k, attrs = {}) => h('textarea', { rows: 2, value: draft[k] ?? '', autocapitalize: 'off', autocorrect: 'off', spellcheck: 'false', ...attrs,
    oninput: (e) => { draft[k] = e.target.value.split(/[,\n]/).map((x) => x.trim()).filter(Boolean).join(', '); } });
  const tg = (k, label, help, opts) => toggleRow(label, help, !!draft[k], bind(k), opts);
  const note = h('span', { class: 'muted small', hidden: true }, 'Unsaved changes');
  const err = h('span', { class: 'err', role: 'alert' });
  const save = h('button', { class: 'btn primary', type: 'submit', disabled: true }, 'Save changes');
  const mark = () => { const d = dirty(); if (!save.hasAttribute('aria-busy')) save.disabled = !d; note.hidden = !d; err.textContent = ''; };
  // Step mismatches are allowed on purpose (the server doesn't enforce
  // steps, and stored values may be off-step), but every setting is a whole number.
  const invalid = () => [...controls.values()].find((i) => {
    i.setCustomValidity(i.value !== '' && !Number.isInteger(i.valueAsNumber) && !i.validity.badInput ? 'Enter a whole number.' : '');
    return i.validity.rangeOverflow || i.validity.rangeUnderflow || i.validity.badInput || i.validity.customError;
  });
  const labelOf = (i) => i.getAttribute('aria-label') || '';
  const onSubmit = async (e) => {
    e.preventDefault();
    const bad = invalid();
    if (bad) { bad.focus(); bad.reportValidity(); return; }
    if (!dirty()) return;
    const [msg, ctl] = check?.(draft) || [];
    if (msg) { note.hidden = true; err.textContent = msg; ctl?.focus(); return; }
    let reset = [];
    const ok = await run(save, async () => {
      const sent = { ...draft };
      cfg = await api('/api/admin/config', { method: 'PUT', body: sent });
      clean = JSON.stringify(Object.assign({ ...draft }, cfg));
      // Fields edited while the save was in flight keep the new edit (and
      // stay unsaved); the rest take what the server stored.
      for (const k of Object.keys(cfg)) if (draft[k] === sent[k]) draft[k] = cfg[k];
      // The server puts values it can't use back to defaults: show what it kept.
      for (const [k, input] of controls) {
        if (sent[k] !== cfg[k]) reset.push(labelOf(input) || k);
        if (draft[k] === cfg[k]) input.value = cfg[k];
      }
      state.caps = { ...state.caps, subtitleSearch: !!cfg.openSubtitlesKey?.trim(), cacheEnabled: !!cfg.cacheEnabled };
      if (cfg.serverName && cfg.serverName !== state.serverName) {
        state.serverName = cfg.serverName;
        const logo = $('.topbar .logo');
        if (logo) { logo.querySelector('span').textContent = cfg.serverName; logo.setAttribute('aria-label', `${cfg.serverName} home`); }
        document.title = `Settings · ${cfg.serverName}`;
      }
    }, () => (reset.length ? null : 'Settings saved'), { busyLabel: 'Saving…' });
    if (!ok) return;
    mark();
    if (reset.length) toast(`Saved, but some values were out of range and were reset: ${reset.join(', ')}`, 'error');
  };
  // novalidate: Save checks the number fields itself (see invalid()).
  // A single section doesn't use the two-column layout (it'd leave one empty).
  const parts = build({ draft, num, text, list, tg, bind, info });
  const many = Array.isArray(parts) && parts.length > 1;
  const form = h('form', { class: many ? 'form cols' : 'form', novalidate: true, onsubmit: onSubmit, oninput: mark, onchange: mark },
    parts, h('div', { class: 'save-bar' }, save, note, err));
  setLeaveGuard(() => form.isConnected && dirty());
  return form;
}

// The webhook URL carries a secret token: offer a copy button, falling back
// to selecting the text where the clipboard API is unavailable (plain http).
function webhookHelp(url) {
  const code = h('code', { class: 'mono' }, url);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(url);
      toast('Webhook URL copied', 'ok');
    } catch {
      getSelection().selectAllChildren(code);
      toast('Press Ctrl+C (⌘C) to copy the selected URL');
    }
  };
  return h('div', { class: 'help' }, 'Sonarr/Radarr: add a Webhook connection pointing at ', code, ' to rescan instantly after imports. ',
    h('button', { class: 'btn sm', type: 'button', onclick: copy }, 'Copy URL'));
}

function serverSection() {
  return configForm(({ draft, num, text, list, tg, info }) => [
    h('div', { class: 'form-section' }, h('h2', null, 'General'),
      field('Server name', text('serverName'))),
    h('div', { class: 'form-section' }, h('h2', null, 'Library scanning'),
      h('div', { class: 'form-grid' },
        field('Rescan every (minutes)', num('scanIntervalMin', { min: 0 }), '0 disables periodic scans. Scans only stat files, so they are cheap.'),
        field('Probe workers', num('probeWorkers', { min: 1, max: 8 }), 'Parallel ffprobe processes for new files. 1 is best on a Pi.')),
      tg('scanOnStartup', 'Scan on startup'),
      webhookHelp(`${location.origin}${info.webhookUrl}`)),
    h('div', { class: 'form-section' }, h('h2', null, 'Network & remote access'),
      h('div', { class: 'form-grid' },
        field('Remote bitrate limit (kbps)', num('remoteMaxBitrate', { min: 0, step: 500 }), '0 = unlimited. Applies to clients outside the local networks below; files above it get transcoded.'),
        field('Stream write buffer (KB)', num('streamBufferKb', { min: 32, max: 4096 }))),
      field('Local networks (CIDR, comma separated)', list('localNetworks')),
      tg('trustProxy', 'Trust reverse-proxy headers', 'Enable only behind a proxy that replaces client-supplied forwarding headers.'),
      field('Trusted proxy peers (CIDR, comma separated)', list('trustedProxies'), 'Only these peers may supply client IP and HTTPS headers. Defaults to loopback; use exact proxy addresses where possible.')),
  ]);
}

function transcodingSection() {
  let first = null;
  // With every method off nothing can play.
  const check = (d) => (!d.enableDirectPlay && !d.enableRemux && !d.enableTranscode ? ['Keep at least one playback method on', first] : null);
  return configForm(({ draft, num, text, tg, bind, info }) => {
    const encs = info.ffmpeg.encoders || [];
    const encOpts = [['libx264', `libx264 (software)${encs.includes('libx264') ? '' : ' — not available'}`], ['h264_v4l2m2m', `h264_v4l2m2m (Raspberry Pi hardware)${encs.includes('h264_v4l2m2m') ? (info.ffmpeg.v4l2Device ? '' : ' — no /dev/video11') : ' — not in this ffmpeg'}`]];
    const direct = tg('enableDirectPlay', 'Direct Play', 'Send the original file untouched (range requests). Zero CPU.');
    first = direct.querySelector('input');
    return [
      h('div', { class: 'form-section' }, h('h2', null, 'Playback methods'),
        direct,
        tg('enableRemux', 'Direct Stream (remux)', 'Repackage into fragmented MP4 without re-encoding video. Very low CPU; audio is converted to AAC only if needed.'),
        tg('enableTranscode', 'Transcode', 'Re-encode video to H.264. CPU heavy on low-end boards.'),
        h('div', { class: 'form-grid' },
          field('Max simultaneous video transcodes', num('maxTranscodes', { min: 0, max: 16 }), '0 = unlimited. 1 is sensible on a Raspberry Pi.'),
          field('Max transcode resolution', h('select', { onchange: (e) => { draft.maxTranscodeHeight = +e.target.value; } }, [[480, '480p'], [720, '720p'], [1080, '1080p'], [2160, '4K']].map(([v, l]) => h('option', { value: v, selected: draft.maxTranscodeHeight === v }, l)))))),
      h('div', { class: 'form-section' }, h('h2', null, 'Video encoder'),
        field('Encoder', h('select', { onchange: (e) => { draft.videoEncoder = e.target.value; } }, encOpts.map(([v, l]) => h('option', { value: v, selected: draft.videoEncoder === v, disabled: !encs.includes(v) }, l)))),
        h('div', { class: 'form-grid' },
          field('x264 preset', h('select', { onchange: (e) => { draft.x264Preset = e.target.value; } }, ['ultrafast', 'superfast', 'veryfast', 'faster', 'fast', 'medium'].map((v) => h('option', { value: v, selected: draft.x264Preset === v }, v))), 'Faster presets use less CPU at slightly lower quality.'),
          field('x264 CRF', num('x264Crf', { min: 15, max: 35 }), 'Quality target (capped by the bitrate limit). Lower = better.'),
          field('Threads', num('transcodeThreads', { min: 0, max: 64 }), '0 = automatic.'),
          field('Process priority (nice)', num('ffmpegNice', { min: 0, max: 19 }), 'Higher = yields CPU to other services.')),
        tg('hwDecode', 'Hardware HEVC decoding (Raspberry Pi)', info.ffmpeg.hevcHwDecode ? 'Uses the Pi\'s HEVC decoder (/dev/video19) when transcoding HEVC; falls back to software automatically.' : 'Unavailable: needs dtoverlay=vc4-kms-v3d in /boot/firmware/config.txt (then reboot) and an ffmpeg with the drm hwaccel.', { disabled: !info.ffmpeg.hevcHwDecode }),
        tg('tonemap', 'HDR → SDR tone mapping', info.ffmpeg.hasZscale ? 'Converts HDR colours when transcoding. Expensive.' : 'Unavailable: this ffmpeg has no zscale filter.', { disabled: !info.ffmpeg.hasZscale })),
      h('div', { class: 'form-section' }, h('h2', null, 'Audio'),
        h('div', { class: 'form-grid' },
          field('Transcoded audio channels', h('select', { onchange: (e) => { draft.audioChannels = +e.target.value; } }, [[2, 'Stereo'], [6, '5.1 surround']].map(([v, l]) => h('option', { value: v, selected: draft.audioChannels === v }, l)))),
          field('AAC bitrate (stereo, kbps)', num('audioBitrate', { min: 64, max: 640, step: 16 })))),
      h('div', { class: 'form-section' }, h('h2', null, 'Streaming'),
        h('div', { class: 'form-grid' },
          field('Minimum fragment duration (ms)', num('fragmentMs', { min: 200, max: 10000, step: 100 }), 'Fragments start at video keyframes. Direct stream uses the source file’s keyframes.'),
          field('Keyframe interval (s)', num('keyframeSec', { min: 1, max: 10 }), 'For transcodes. Shorter = more precise seeking.'))),
    ];
  }, { check });
}

// The fields need Save; the Actions below the form run at once, with the
// saved settings.
async function metadataSection() {
  const tasks = await api('/api/admin/tasks').catch(() => null);
  const form = await configForm(({ draft, text, tg, num }) => [
    h('div', { class: 'form-section' }, h('h2', null, 'Providers'),
      h('p', { class: 'help', style: { margin: 0 } }, 'TMDB gives the richest data (posters, backdrops, cast, episode stills). Get a free API key at themoviedb.org → Settings → API. Without it, TVmaze is used for shows and your local Radarr (auto-detected) for movies — no keys needed.'),
      field('TMDB API key or read access token', text('tmdbKey', { type: 'password', placeholder: 'v3 key or v4 token', autocomplete: 'off' }), 'Saving a new key retries unmatched items automatically.'),
      field('Language', text('metadataLanguage', { placeholder: 'en-US' })),
      tg('enableTmdb', 'Use TMDB'),
      tg('enableTvmaze', 'Use TVmaze for shows (no key)'),
      h('div', { class: 'form-grid' },
        field('Radarr URL', text('radarrUrl', { placeholder: 'http://127.0.0.1:7878' }), 'Used as a keyless TMDB source for movies (matched by folder name).'),
        field('Radarr API key', text('radarrKey', { type: 'password', autocomplete: 'off' }), 'Radarr → Settings → General → API Key'))),
    h('div', { class: 'form-section' }, h('h2', null, 'Subtitle downloads'),
      h('p', { class: 'help', style: { margin: 0 } }, 'Search and download subtitles from the player (Subtitles → Search online). Needs a free API key: create an account at opensubtitles.com, then Profile → API consumers → New consumer. Username and password are optional but raise the daily download limit.'),
      field('OpenSubtitles API key', text('openSubtitlesKey', { type: 'password', autocomplete: 'off' })),
      h('div', { class: 'form-grid' },
        field('OpenSubtitles username', text('openSubtitlesUser', { autocomplete: 'off' })),
        field('OpenSubtitles password', text('openSubtitlesPass', { type: 'password', autocomplete: 'new-password' })))),
    h('div', { class: 'form-section' }, h('h2', null, 'Seek previews'),
      h('p', { class: 'help', style: { margin: 0 } }, 'Thumbnails shown while hovering or dragging the seek bar. Generated in the background from keyframes only, at the lowest priority, and paused during playback (except for files in the SSD cache). About 1–3 MB per movie.'),
      tg('trickplayEnabled', 'Generate seek previews'),
      h('div', { class: 'form-grid' },
        field('Interval (seconds)', num('trickplayInterval', { min: 2, max: 60 }), 'Changes apply to newly generated previews.'),
        field('Thumbnail width (px)', num('trickplayWidth', { min: 120, max: 480, step: 20 })))),
    h('div', { class: 'form-section' }, h('h2', null, 'Intro detection'),
      h('p', { class: 'help', style: { margin: 0 } }, 'Finds TV intros by comparing audio fingerprints of neighbouring episodes (like Jellyfin\'s Intro Skipper), or from chapters named Intro/Opening. Runs in the background at the lowest CPU priority after scans.'),
      tg('introDetect', 'Detect intros'),
      h('div', { class: 'form-grid' },
        field('Analyse the first (seconds)', num('introScanSecs', { min: 120, max: 1800, step: 30 }), 'Intros after cold opens can start a few minutes in.'),
        field('Shortest intro (seconds)', num('introMinSecs', { min: 5, max: 60 })),
        field('Longest intro (seconds)', num('introMaxSecs', { min: 20, max: 300 })))),
    h('div', { class: 'form-section' }, h('h2', null, 'Local'),
      tg('useLocalMetadata', 'Use local artwork & hints', 'poster.jpg / fanart.jpg / *-thumb.jpg, .plexmatch and .nfo ids.'),
      tg('generateThumbs', 'Generate missing episode thumbnails', 'Grabs one frame with ffmpeg when no still is available (cached).')),
  ]);
  const introOK = tasks?.intro?.available !== false;
  const actions = h('div', { class: 'form-section' }, h('h2', null, 'Actions'),
    h('p', { class: 'help', style: { margin: 0 } }, 'These run now, using the saved settings. Save your changes above first.'),
    h('div', { class: 'row wrap' },
      h('button', { class: 'btn', type: 'button', onclick: (e) => run(e.currentTarget, () => api('/api/admin/metadata/refresh', { method: 'POST', body: { missingOnly: true } }), 'Retrying unmatched items') }, 'Retry unmatched items'),
      h('button', { class: 'btn', type: 'button', onclick: async (e) => {
        const btn = e.currentTarget;
        if (await confirmDialog('Re-fetch metadata for every item? Manual matches are kept.', 'Refresh', false, 'Refresh all metadata?')) await run(btn, () => api('/api/admin/metadata/refresh', { method: 'POST', body: {} }), 'Refreshing all metadata in the background');
      } }, 'Refresh all metadata'),
      h('button', { class: 'btn', type: 'button', disabled: !introOK, 'aria-describedby': introOK ? null : 'intro-unavail', onclick: (e) => run(e.currentTarget, () => api('/api/admin/intro/scan', { method: 'POST' }), 'Intro detection started') }, 'Detect intros now')),
    introOK ? null : h('div', { class: 'help', id: 'intro-unavail' }, 'Intro detection is unavailable: this ffmpeg has no chromaprint support.'));
  return h('div', { class: 'form' }, form, actions);
}

// ---------- libraries ----------
// The list shows each library's scan progress while the scanner works on it
// (from /api/admin/tasks) and reloads when a scan finishes.
async function librariesSection(ctx) {
  const tasks = await api('/api/admin/tasks').catch(() => null);
  const libs = await api('/api/admin/libraries');
  const box = h('div', { class: 'form' });
  box.appendChild(h('div', { class: 'row' }, h('div', { class: 'spacer' }),
    h('button', { class: 'btn', disabled: !libs.length, dataset: { focusKey: 'lib-scan-all' }, onclick: (e) => run(e.currentTarget, () => api('/api/admin/scan', { method: 'POST', body: {} }), 'Scanning all libraries') }, h('span', { html: icons.refresh }), 'Scan all'),
    h('button', { class: 'btn primary', dataset: { focusKey: 'lib-add' }, onclick: () => libraryModal() }, h('span', { html: icons.plus }), 'Add library')));
  if (!libs.length) {
    box.appendChild(emptyState({ level: 'h2', title: 'Add your first library', text: 'Point Lex at the folder with your movies or TV shows, e.g. /media/movies.',
      actions: [h('button', { class: 'btn primary', onclick: () => libraryModal() }, 'Add library')] }));
  }
  const status = new Map();
  for (const l of libs) {
    const meta = h('span', { class: 'dim' }, `${KIND_LABELS[l.kind] || l.kind} · ${l.itemCount} item${l.itemCount === 1 ? '' : 's'} · scanned ${timeAgo(l.lastScan)}`);
    const live = h('span', { class: 'row small muted', hidden: true });
    status.set(l.name, { meta, live });
    box.appendChild(h('div', { class: 'form-section' },
      // The name keeps its line; the meta wraps below it on phones.
      h('div', { class: 'row wrap' }, h('h2', { style: { flex: '1 1 auto' } }, l.name), meta, live),
      l.lastScan && !l.itemCount ? h('div', { class: 'help warn' }, 'No media found — check the folder and the type.') : null,
      h('div', { class: 'chips' }, l.paths.map((p) => h('span', { class: 'chip mono', style: { paddingRight: '12px' } }, p))),
      h('div', { class: 'row' },
        h('button', { class: 'btn sm', 'aria-label': `Scan ${l.name}`, dataset: { focusKey: `lib-scan-${l.id}` }, onclick: (e) => run(e.currentTarget, () => api('/api/admin/scan', { method: 'POST', body: { libraryId: l.id } }), `Scanning ${l.name}`) }, 'Scan'),
        h('button', { class: 'btn sm', 'aria-label': `Edit ${l.name}`, dataset: { focusKey: `lib-edit-${l.id}` }, onclick: () => libraryModal(l) }, 'Edit'),
        h('button', { class: 'btn sm danger', 'aria-label': `Remove ${l.name}`, onclick: async (e) => {
          const btn = e.currentTarget;
          if (!(await confirmDialog(`Remove library “${l.name}”? Files on disk are not touched; watch history for its items is removed.`, 'Remove', true, 'Remove library?'))) return;
          if (!(await run(btn, () => api(`/api/admin/libraries/${l.id}`, { method: 'DELETE' }), `Removed ${l.name}`))) return;
          focusAfterRoute('lib-add'); // the card goes away
          await loadLibraries();
          refreshSoft();
        } }, 'Remove'))));
  }
  box.appendChild(h('div', { class: 'help' }, 'Types: Movies (one movie per folder or file), TV Shows (Show/Season N/episodes), Mixed (auto-detects episodes by S01E01 or Season folders — good for anime folders with both series and films).'));
  const show = (s) => {
    for (const [name, { meta, live }] of status) {
      const on = !!s?.running && s.library === name;
      meta.hidden = on;
      live.hidden = !on;
      if (on) live.replaceChildren(h('div', { class: 'spinner sm' }), s.phase === 'probing' ? `Probing · ${s.probeDone}/${s.probeTotal}` : `${upper(s.phase || 'scanning')}…`);
    }
  };
  show(tasks?.scan);
  // A scan that finished since this render (or one seen running that has
  // stopped) changes counts and "scanned" times: re-render in place.
  let seen = tasks?.scan || null;
  const banner = staleBanner(box);
  every(ctx, 2000, async () => {
    const { scan: s } = await api('/api/admin/tasks');
    if (!ctx.isCurrent()) return;
    if (seen && !s.running && (seen.running || s.finishedAt !== seen.finishedAt)) { refreshSoft(); return; }
    seen = s;
    show(s);
  }, banner.hooks());
  return h('div', null, banner.el, box);
}

const within = (p, base) => p === base || base === '/' || p.startsWith(`${base}/`);
const upper = (s) => s.charAt(0).toUpperCase() + s.slice(1);
let libModalId = 0;

function libraryModal(lib) {
  const uid = ++libModalId;
  const err = h('div', { class: 'err', role: 'alert', id: `lib-err-${uid}` });
  const name = h('input', { type: 'text', value: lib?.name || '', 'aria-describedby': err.id, oninput: () => name.removeAttribute('aria-invalid') });
  let kind = lib?.kind || 'movies';
  const paths = [...(lib?.paths || [])];
  let autoName = null;
  const live = h('div', { class: 'sr-only', 'aria-live': 'polite' });
  const folderNote = h('div', { class: 'help warn', role: 'status' });
  const chips = h('div', { class: 'chips' });
  const renderChips = () => chips.replaceChildren(...(paths.length
    ? paths.map((p, i) => h('span', { class: 'chip mono' }, p, h('button', { type: 'button', 'aria-label': `Remove folder ${p}`, title: 'Remove folder', html: icons.close, onclick: () => removeAt(i) })))
    : [h('span', { class: 'dim small' }, 'No folders added yet')]));
  const removeAt = (i) => {
    const [p] = paths.splice(i, 1);
    renderChips();
    folderNote.textContent = '';
    live.textContent = `Removed ${p}`;
    const btns = chips.querySelectorAll('button');
    (btns[i] || btns[i - 1] || browser.addBtn).focus();
  };
  const onPick = async (p) => {
    folderNote.textContent = '';
    err.textContent = '';
    if (paths.includes(p)) { folderNote.textContent = `${p} is already added.`; return; }
    const clash = paths.find((q) => within(p, q) || within(q, p));
    if (clash) {
      folderNote.textContent = within(p, clash) ? `${p} is inside ${clash}, which is already added.` : `${p} contains ${clash}, which is already added. Remove it first to use the larger folder.`;
      return;
    }
    if (p === '/' && !(await confirmDialog('Use the whole filesystem as a library folder?', 'Use /', false, 'Add / as a folder?'))) return;
    paths.push(p);
    renderChips();
    live.textContent = `Added ${p}`;
    // A new library is usually named after its folder (renamed again if that
    // folder is swapped for another, unless the name was typed).
    if (!lib && paths.length === 1 && p !== '/' && (!name.value.trim() || name.value === autoName)) {
      name.value = autoName = upper(p.split('/').pop());
      name.removeAttribute('aria-invalid');
    }
  };
  renderChips();
  const parent = (p) => p?.replace(/\/[^/]+$/, '');
  const browser = folderBrowser(onPick, [parent(lib?.paths?.[0]), parent(state.libraries[0]?.paths?.[0]), '/media', '/mnt']);
  const retypeNote = lib ? h('span', null, ` Changing the type rebuilds the library: watch progress, watched status and favourites for its ${lib.itemCount} items are reset for everyone.`) : null;
  const typeHelp = h('div', { class: 'help' }, 'Movies: one film per folder or file. TV Shows: Show/Season/episodes. Mixed: both, detected per folder.', retypeNote);
  const typeSel = select(kind, Object.entries(KIND_LABELS), (v) => { kind = v; retypeNote?.classList.toggle('warn', v !== lib.kind); });
  const foldersId = `lib-folders-${uid}`;
  const submit = async (e) => {
    const btn = e.currentTarget;
    if (btn.disabled) return;
    err.textContent = '';
    name.removeAttribute('aria-invalid');
    const nm = name.value.trim();
    if (!nm) { err.textContent = 'Enter a name'; name.setAttribute('aria-invalid', 'true'); name.focus(); return; }
    if (!paths.length) {
      err.textContent = browser.current() ? 'Press “Add this folder” to use the folder you’re viewing' : 'Choose a folder';
      (browser.current() ? browser.addBtn : browser.input).focus();
      return;
    }
    if (lib && (kind !== lib.kind || lib.paths.some((p) => !paths.includes(p)))) {
      const retype = kind !== lib.kind;
      const msg = retype
        ? `Changing “${lib.name}” to ${KIND_LABELS[kind]} rebuilds it. Watch progress, watched status and favourites for its ${lib.itemCount} items are reset for everyone.`
        : 'Titles in the removed folders, and everyone’s watch history for them, are removed from the library.';
      if (!(await confirmDialog(msg, 'Save changes', true, retype ? 'Change library type?' : 'Remove folders?'))) return;
    }
    let failed = null;
    const ok = await run(btn, async () => {
      try {
        const body = { name: nm, kind, paths };
        if (lib) await api(`/api/admin/libraries/${lib.id}`, { method: 'PUT', body });
        else await api('/api/admin/libraries', { method: 'POST', body });
      } catch (ex) {
        // Shown in the dialog rather than as a toast; AbortError keeps run() quiet.
        failed = ex;
        throw Object.assign(new Error(ex.message), { name: 'AbortError' });
      }
      m.close();
    }, lib ? 'Library updated; rescanning' : 'Library added; scanning now', { busyLabel: lib ? 'Saving…' : 'Adding…' });
    if (!ok) { if (failed) err.textContent = upper(failed.message); return; }
    await loadLibraries();
    refreshSoft();
  };
  const m = modal({ title: lib ? `Edit ${lib.name}` : 'Add library', wide: true, dismissible: false, body: [
    field('Name', name),
    field('Type', typeSel, typeHelp),
    h('div', { class: 'field', role: 'group', 'aria-labelledby': foldersId }, h('span', { id: foldersId }, 'Folders'), chips, folderNote),
    browser.el,
    live,
    err,
  ], actions: [
    h('button', { class: 'btn', type: 'button', onclick: () => m.close() }, 'Cancel'),
    h('button', { class: 'btn primary', type: 'button', onclick: submit }, lib ? 'Save' : 'Add library')] });
}

// folderBrowser lists server folders for the library editor. It opens at the
// first of starts that exists (falling back to /). current() is the folder
// last listed successfully; "Add this folder" adds that one, not whatever is
// typed in the box.
function folderBrowser(onPick, starts) {
  const input = h('input', { type: 'text', 'aria-label': 'Folder path', autocapitalize: 'off', autocorrect: 'off', spellcheck: 'false', style: { flex: '1 1 200px', minWidth: 0 } });
  const list = h('div', { class: 'fs-list' });
  let current = null;
  const addBtn = h('button', { class: 'btn sm primary', type: 'button', disabled: true, onclick: () => current && onPick(current) }, h('span', { html: icons.plus }), 'Add this folder');
  const row = (icon, label, onclick) => h('button', { type: 'button', onclick }, h('span', { html: icons[icon] }), label);
  const go = async (p, { quiet = false } = {}) => {
    const fromList = list.contains(document.activeElement);
    try {
      const r = await api(`/api/admin/fs?path=${encodeURIComponent(p)}`);
      current = r.path;
      input.value = r.path;
      addBtn.disabled = false;
      list.replaceChildren(...[
        r.path !== '/' ? row('up', '..', () => go(r.parent)) : null,
        ...r.dirs.map((d) => row('folder', d, () => go(`${r.path === '/' ? '' : r.path}/${d}`))),
        r.dirs.length ? null : h('div', { class: 'dim small', style: { padding: '8px' } }, 'No sub-folders'),
      ].filter(Boolean));
      if (fromList) list.querySelector('button')?.focus();
      return true;
    } catch (e) {
      if (quiet) return false;
      // Keep listing the last good folder, with the error above it.
      input.value = current ?? '';
      list.querySelector('.fs-err')?.remove();
      const msg = h('div', { class: 'fs-err small', role: 'alert' }, e.message);
      if (current) list.prepend(msg);
      else list.replaceChildren(msg, row('up', 'Open /', () => go('/')));
      if (fromList) list.querySelector('button')?.focus();
      return false;
    }
  };
  (async () => {
    for (const p of [...new Set(starts.filter(Boolean))]) if (await go(p, { quiet: true })) return;
    await go('/');
  })();
  const el = h('div', { style: { display: 'grid', gap: '8px' } },
    h('form', { class: 'row wrap', onsubmit: (e) => { e.preventDefault(); go(input.value.trim() || '/'); } }, input, h('button', { class: 'btn sm', type: 'submit' }, 'Go'), addBtn),
    list);
  return { el, addBtn, input, current: () => current };
}

// ---------- users & devices ----------
// Both tables become stacked rows on narrow screens (.tbl.stack); data-label
// names the cells whose header is then hidden.
const actionsTh = () => h('th', null, h('span', { class: 'sr-only' }, 'Actions'));
const fmtDay = (ts) => (ts ? new Date(ts * 1000).toLocaleDateString(undefined, { dateStyle: 'medium' }) : '');

async function usersSection() {
  const users = await api('/api/admin/users');
  const tbl = h('table', { class: 'tbl stack' }, h('tr', null, h('th', null, 'Name'), h('th', null, 'Role'), h('th', null, 'Created'), actionsTh()),
    users.map((u) => h('tr', null, h('td', null, h('b', null, u.name, u.id === state.me.id ? [' ', h('span', { class: 'dim' }, '(you)')] : null)), h('td', null, u.isAdmin ? 'Admin' : 'User'),
      h('td', { class: 'nowrap', 'data-label': 'Created' }, fmtDay(u.createdAt)),
      h('td', null, h('div', { class: 'row wrap tbl-actions' },
        // Resetting your own password here would sign you out everywhere.
        u.id === state.me.id
          ? h('a', { class: 'btn sm', href: '#/settings/account', dataset: { focusKey: 'settings-nav-account' }, onclick: () => focusAfterRoute('settings-nav-account') }, 'Change password')
          : h('button', { class: 'btn sm', 'aria-label': `Reset password for ${u.name}`, onclick: () => resetPasswordModal(u) }, 'Reset password'),
        // Your own role can only be changed by another admin.
        u.id !== state.me.id ? h('button', { class: 'btn sm', dataset: { focusKey: `user-admin-${u.id}` }, onclick: async (e) => {
          const btn = e.currentTarget;
          const ok = u.isAdmin
            ? await confirmDialog(`Make ${u.name} a regular user? They will no longer be able to change settings, libraries or users.`, 'Make user', false, 'Make user?')
            : await confirmDialog(`Make ${u.name} an administrator? They can change settings, libraries and users.`, 'Make admin', false, 'Make admin?');
          if (ok && await run(btn, () => api(`/api/admin/users/${u.id}`, { method: 'PUT', body: { isAdmin: !u.isAdmin } }), u.isAdmin ? `Made ${u.name} a regular user` : `Made ${u.name} an admin`)) refreshSoft();
        } }, u.isAdmin ? 'Make user' : 'Make admin') : null,
        u.id !== state.me.id ? h('button', { class: 'btn sm danger', 'aria-label': `Delete ${u.name}`, dataset: { focusKey: `user-del-${u.id}` }, onclick: async (e) => {
          const btn = e.currentTarget;
          if (!(await confirmDialog(`Delete ${u.name}? Their watch history, favourites and signed-in devices are removed. This can’t be undone.`, 'Delete user', true, 'Delete user?'))) return;
          if (!(await run(btn, () => api(`/api/admin/users/${u.id}`, { method: 'DELETE' }), `Deleted ${u.name}`))) return;
          // The row goes away: focus the next row's Delete (or the Add user form).
          const others = users.filter((x) => x.id !== state.me.id), i = others.indexOf(u), next = others[i + 1] || others[i - 1];
          focusAfterRoute(next ? `user-del-${next.id}` : 'user-new-name');
          refreshSoft();
        } }, 'Delete') : null)))));
  return h('div', { class: 'form' },
    h('div', { class: 'form-section' }, h('h2', null, 'Users'), h('div', { class: 'table-wrap' }, tbl)),
    addUserForm());
}

function addUserForm() {
  const name = h('input', { type: 'text', name: 'new-user-name', dataset: { focusKey: 'user-new-name' }, autocomplete: 'off', autocapitalize: 'none', autocorrect: 'off', spellcheck: 'false', required: true, maxlength: 64 });
  const pw = h('input', { type: 'password', name: 'new-user-password', dataset: { focusKey: 'user-new-password' }, autocomplete: 'new-password', required: true, ...PW_RULE });
  const errors = formErrors('add-user-err', [name, pw]);
  const btn = h('button', { class: 'btn primary', type: 'submit', dataset: { focusKey: 'user-create' } }, 'Create user');
  let admin = false;
  const onsubmit = async (e) => {
    e.preventDefault();
    errors.reset();
    if (!name.value.trim()) return errors.fail('Enter a username', name);
    if (!pwLength(pw.value)) return errors.fail(PW_MSG, pw);
    const nm = name.value.trim();
    if (await submitInline(btn, errors, () => api('/api/admin/users', { method: 'POST', body: { name: nm, password: pw.value, isAdmin: admin } }), `Created ${nm}`,
      (m) => (/password/i.test(m) ? [pw] : [name]))) refreshSoft();
  };
  return h('form', { class: 'form-section', novalidate: true, onsubmit }, h('h2', null, 'Add user'),
    h('div', { class: 'form-grid' }, field('Username', name), field('Password', pw, 'Use 12–72 bytes.')),
    toggleRow('Administrator', 'Can change settings, libraries and see stats.', false, (v) => { admin = v; }),
    errors.err,
    h('div', null, btn));
}

// An admin sets a new password for someone else; it signs them out everywhere.
function resetPasswordModal(u) {
  const formId = `reset-pw-${u.id}`;
  const pw = h('input', { type: 'password', name: 'new-password', autocomplete: 'new-password', required: true, ...PW_RULE });
  const errors = formErrors(`${formId}-err`, [pw]);
  const show = h('label', { class: 'row small muted', style: { gap: '8px', cursor: 'pointer' } },
    h('input', { type: 'checkbox', onchange: (e) => { pw.type = e.target.checked ? 'text' : 'password'; } }), 'Show password');
  const btn = h('button', { class: 'btn primary', type: 'submit', form: formId }, 'Reset password');
  const onsubmit = async (e) => {
    e.preventDefault();
    errors.reset();
    if (!pwLength(pw.value)) return errors.fail(PW_MSG, pw);
    if (await submitInline(btn, errors, () => api(`/api/admin/users/${u.id}`, { method: 'PUT', body: { password: pw.value } }), `Password reset for ${u.name}`, () => [pw])) m.close();
  };
  const m = modal({ title: `Reset password for ${u.name}`, body: h('form', { id: formId, novalidate: true, onsubmit, style: { display: 'grid', gap: '12px' } },
    field('New password', pw, `Use 12–72 bytes. ${u.name} will be signed out on all devices.`), show, errors.err),
  actions: [h('button', { class: 'btn', type: 'button', onclick: () => m.close() }, 'Cancel'), btn] });
  pw.focus();
}

async function devicesSection() {
  const devs = await api('/api/admin/devices');
  const signOut = async (e, d) => {
    const btn = e.currentTarget;
    if (d.current) {
      // Same as the account menu's Sign out.
      if (await run(btn, () => api('/api/auth/logout', { method: 'POST' }))) { setLeaveGuard(null); location.hash = ''; location.reload(); }
      return;
    }
    if (!(await confirmDialog(`Sign out ${d.userName} on ${d.client} (${d.ip})? They’ll need to sign in again.`, 'Sign out', true, 'Sign out device?'))) return;
    if (!(await run(btn, () => api(`/api/admin/devices/${d.prefix}`, { method: 'DELETE' }), `Signed out ${d.userName} on ${d.client}`))) return;
    // The row goes away: keep focus in the list, on the next row's button.
    const i = devs.indexOf(d), next = devs[i + 1] || devs[i - 1];
    if (next) focusAfterRoute(`device-${next.prefix}`);
    refreshSoft();
  };
  return h('div', { class: 'form' }, h('div', { class: 'form-section' }, h('h2', null, 'Signed-in devices'),
    h('div', { class: 'table-wrap' }, h('table', { class: 'tbl stack' }, h('tr', null, h('th', null, 'User'), h('th', null, 'Client'), h('th', null, 'IP'), h('th', null, 'Signed in'), h('th', null, 'Last seen'), actionsTh()),
      devs.map((d) => h('tr', null, h('td', null, h('b', null, d.userName)),
        h('td', null, d.client, d.current ? [' ', h('span', { class: 'chip this-device' }, 'This device')] : null),
        h('td', { class: 'mono' }, d.ip), h('td', { 'data-label': 'Signed in' }, fmtDate(d.created)), h('td', { 'data-label': 'Last seen' }, timeAgo(d.lastSeen)),
        h('td', null, h('div', { class: 'row wrap tbl-actions' }, h('button', { class: 'btn sm danger', dataset: { focusKey: `device-${d.prefix}` },
          'aria-label': d.current ? 'Sign out here (this device)' : `Sign out ${d.userName} on ${d.client} (${d.ip})`, onclick: (e) => signOut(e, d) }, d.current ? 'Sign out here' : 'Sign out')))))))));
}

// keepFocus re-renders box with rebuild() and, if focus was inside it, puts
// it back on the control with the same data-focus-key; when that one is gone,
// on the one keyed next (if given) or the box's first keyed control.
function keepFocus(box, rebuild, next = null) {
  const k = box.contains(document.activeElement) ? document.activeElement.dataset.focusKey : null;
  rebuild();
  if (k === undefined || k === null) return;
  const keyed = [...box.querySelectorAll('[data-focus-key]')];
  (keyed.find((el) => el.dataset.focusKey === k) || keyed.find((el) => el.dataset.focusKey === next) || keyed[0])?.focus({ preventScroll: true });
}

function taskRow(label, running, text) {
  return h('div', { class: 'task' }, running ? h('div', { class: 'spinner sm' }) : h('span', { class: 'good', html: icons.check, style: { width: '16px' } }), h('b', null, label), text);
}

// ---------- SSD cache ----------
async function cacheSection(ctx) {
  const statusBox = h('div', { class: 'form-section' });
  const listBox = h('div', { class: 'form-section' });
  let listSig = null, nextKey = null;
  const render = async () => {
    const r = await api('/api/admin/cache');
    if (!ctx.isCurrent()) return;
    const st = r.status;
    const pct = st.maxBytes ? (st.usedBytes / st.maxBytes) * 100 : 0;
    clear(statusBox).append(h('h2', null, 'Status'),
      h('div', { class: 'row small' }, h('span', { class: 'grow' }, `${fmtBytes(st.usedBytes)} of ${fmtBytes(st.maxBytes)} used · ${st.files} file${st.files === 1 ? '' : 's'}`), h('span', { class: 'muted' }, `disk free ${fmtBytes(st.diskFree)} of ${fmtBytes(st.diskTotal)}`)),
      meter(pct, 97),
      h('dl', { class: 'kv' },
        h('dt', null, 'Folder'), h('dd', { class: 'mono' }, st.dir),
        h('dt', null, 'Reads'), h('dd', null, `${st.hits} from cache · ${st.misses} from library disk (since start)`),
        h('dt', null, 'Copying'), h('dd', null, st.current ? `${st.current.name} — ${Math.round((st.current.done / st.current.size) * 100)}% of ${fmtBytes(st.current.size)} at ${fmtBytes(st.current.speed)}/s (${st.current.reason})` : 'nothing'),
        h('dt', null, 'Queue'), h('dd', null, st.queued.length ? st.queued.map((j) => j.name).join(', ') : 'empty'),
        st.lastError ? [h('dt', null, 'Last error'), h('dd', { class: 'bad' }, st.lastError)] : null));
    // The list (and its buttons) is rebuilt only when it changes, or once a
    // minute for the "… ago" times.
    const sig = `${Math.floor(Date.now() / 60000)}|${r.entries.map((e) => `${e.itemId}:${e.lastAccess}`).join()}`;
    if (sig === listSig) return;
    listSig = sig;
    keepFocus(listBox, () => clear(listBox).append(h('div', { class: 'row' }, h('h2', { class: 'grow' }, 'Cached files'),
      r.entries.length ? h('button', { class: 'btn sm danger', dataset: { focusKey: 'cache-clear' }, onclick: async (e) => {
        const btn = e.currentTarget;
        if (await confirmDialog('Delete every cached copy? Originals are not touched.', 'Clear', true) && await run(btn, () => api('/api/admin/cache/clear', { method: 'POST', body: { kind: 'media' } }))) refresh();
      } }, 'Clear cache') : null),
    r.entries.length ? h('div', { class: 'table-wrap' }, h('table', { class: 'tbl' },
      h('tr', null, ['Title', 'Size', 'Cached', 'Last used', 'Reason', ''].map((t) => h('th', null, t))),
      r.entries.map((e) => h('tr', null, h('td', null, h('a', { href: `#/item/${e.itemId}`, dataset: { focusKey: `cache-item-${e.itemId}` } }, e.title), h('div', { class: 'dim small ellipsis', style: { maxWidth: '380px' } }, e.name)), h('td', { class: 'nowrap' }, fmtBytes(e.size)), h('td', { class: 'nowrap' }, timeAgo(e.addedAt)), h('td', { class: 'nowrap' }, timeAgo(e.lastAccess)), h('td', null, e.reason),
        h('td', null, h('button', { class: 'btn sm', dataset: { focusKey: `cache-remove-${e.itemId}` }, 'aria-label': `Remove ${e.title} from the cache`, onclick: async (ev) => {
          const i = r.entries.indexOf(e), next = r.entries[i + 1] || r.entries[i - 1];
          nextKey = next ? `cache-remove-${next.itemId}` : null;
          if (await run(ev.currentTarget, () => api(`/api/admin/cache/items/${e.itemId}`, { method: 'DELETE' }), `Removed ${e.title} from the cache`)) refresh();
        } }, 'Remove'))))))
      : h('p', { class: 'muted', style: { margin: 0 } }, 'Nothing cached yet. Files are copied when played (and upcoming episodes are prefetched).')), nextKey);
  };
  const refresh = () => render().catch(() => {});
  const form = await configForm(({ draft, num, text, tg }) => [
    h('div', { class: 'form-section' }, h('h2', null, 'SSD cache'),
      h('p', { class: 'help', style: { margin: 0 } }, 'Keeps copies of media on a fast disk (e.g. the SSD) so playback — and seeking — reads from it instead of the hard drive, which can then stay asleep. Files are copied in the background at a capped speed; the least recently watched are evicted first.'),
      tg('cacheEnabled', 'Enable SSD cache'),
      field('Cache folder', text('cacheDir', { placeholder: 'default: <data folder>/cache' }), 'A "lex-cache" folder is created inside it. Leave empty to use Lex\'s data folder (on the SSD here).'),
      h('div', { class: 'form-grid' },
        field('Maximum size (GB)', num('cacheMaxGb', { min: 1 })),
        field('Keep free on disk (GB)', num('cacheMinFreeGb', { min: 0 })),
        field('Largest file to cache (GB)', num('cacheMaxFileGb', { min: 1 })),
        field('Copy speed limit (MB/s)', num('cacheSpeedMbs', { min: 0 }), '0 = unlimited. Keeps the hard drive responsive for playback.')),
      tg('cacheOnPlay', 'Cache what\'s being played', 'The rest of the file is read from the SSD after the copy finishes (after the next seek for remux/transcode).'),
      field('Prefetch upcoming episodes', h('select', { onchange: (e) => { draft.cachePrefetch = +e.target.value; } }, [0, 1, 2, 3, 5].map((v) => h('option', { value: v, selected: draft.cachePrefetch === v }, v ? `Next ${v}` : 'Off'))))),
  ]);
  await render();
  const page = h('div', { class: 'form' }, form, statusBox, listBox);
  const banner = staleBanner(page);
  every(ctx, 2000, render, banner.hooks());
  return h('div', null, banner.el, page);
}

// ---------- tasks & logs ----------
// Polled every few seconds. Only the status lines are replaced (Scan now is
// built once), and new log lines are appended, so focus and a text selection
// in the log survive the refresh.
async function logsSection(ctx) {
  const status = h('div', { style: { display: 'grid', gap: '16px' } });
  const tasks = h('div', { class: 'form-section' }, h('h2', null, 'Background tasks'), status,
    h('div', { class: 'row' }, h('button', { class: 'btn sm', dataset: { focusKey: 'scan-now' }, onclick: async (e) => {
      if (await run(e.currentTarget, () => api('/api/admin/scan', { method: 'POST', body: {} }), 'Scanning all libraries')) renderTasks().catch(() => {});
    } }, 'Scan now')));
  const logBox = h('div', { class: 'logs', tabindex: 0, role: 'region', 'aria-label': 'Server log' });
  let follow = true;
  logBox.addEventListener('scroll', () => { follow = logBox.scrollTop + logBox.clientHeight >= logBox.scrollHeight - 20; });
  const renderTasks = async () => {
    const t = await api('/api/admin/tasks');
    if (!ctx.isCurrent()) return;
    const s = t.scan, m = t.metadata;
    status.replaceChildren(
      taskRow('Library scan', s.running, s.running ? `${s.phase}${s.library ? ' · ' + s.library : ''}${s.phase === 'probing' ? ` · ${s.probeDone}/${s.probeTotal} ${s.current}` : ''}` : `idle · last finished ${timeAgo(s.finishedAt)} (found ${s.found}, +${s.added}, −${s.removed}, changed ${s.changed})`),
      taskRow('Metadata', m.running, m.running ? `${m.done}/${m.total} · ${m.current}` : `idle · last run ${timeAgo(m.lastRun)} (${m.matched} matched, ${m.missing} not found)`),
      taskRow('Intro detection', t.intro.running, !t.intro.available ? 'unavailable (ffmpeg without chromaprint)' : t.intro.running ? `${t.intro.done}/${t.intro.seasons} seasons · ${t.intro.current} · ${t.intro.found} found` : `idle · last run ${timeAgo(t.intro.lastRun)}`),
      taskRow('SSD cache', !!t.cache.current, !t.cache.enabled ? 'disabled' : t.cache.current ? `copying ${t.cache.current.name} · ${Math.round((t.cache.current.done / t.cache.current.size) * 100)}% · ${fmtBytes(t.cache.current.speed)}/s${t.cache.queued.length ? ` · ${t.cache.queued.length} queued` : ''}` : `idle · ${t.cache.files} files, ${fmtBytes(t.cache.usedBytes)} of ${fmtBytes(t.cache.maxBytes)}`),
      t.trickplay ? taskRow('Seek previews', t.trickplay.running, !t.trickplay.enabled ? 'disabled' : t.trickplay.running ? `generating ${t.trickplay.current} · ${t.trickplay.pending} left` : t.trickplay.paused ? `paused while something is playing · ${t.trickplay.pending} left` : t.trickplay.pending ? `${t.trickplay.pending} waiting` : `idle · all done${t.trickplay.failed ? ` (${t.trickplay.failed} failed)` : ''}`) : '',
      h('div', { class: 'task' }, h('b', null, 'ffmpeg jobs'), `${t.remuxJobs} remux · ${t.transcodeJobs} transcode`));
  };
  const today = () => new Date().toDateString();
  const stamp = (t) => (new Date(t).toDateString() === today() ? new Date(t).toLocaleTimeString() : new Date(t).toLocaleString());
  const key = (l) => `${l.t}|${l.level}|${l.msg}`;
  const lineEl = (l) => h('div', { class: l.level }, `${stamp(l.t)} ${l.level.padEnd(5)} ${l.msg}`);
  let shown = []; // keys of the rendered lines, oldest first
  let text = [];
  const renderLogs = async () => {
    const lines = await api('/api/admin/logs');
    if (!ctx.isCurrent()) return;
    // Don't move lines under a selection the user is making or copying.
    const sel = getSelection();
    if (sel && !sel.isCollapsed && logBox.contains(sel.anchorNode)) return;
    const keys = lines.map(key);
    const at = shown.length ? keys.lastIndexOf(shown[shown.length - 1]) : -1;
    if (at < 0) {
      logBox.replaceChildren(...lines.map(lineEl));
    } else {
      logBox.append(...lines.slice(at + 1).map(lineEl));
      // The server keeps a fixed number of lines: drop the ones it dropped.
      while (logBox.childElementCount > lines.length) logBox.firstElementChild.remove();
    }
    shown = keys;
    text = lines.map((l) => `${new Date(l.t).toISOString()} ${l.level.padEnd(5)} ${l.msg}`);
    if (follow) logBox.scrollTop = logBox.scrollHeight;
  };
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text.join('\n'));
      toast('Log copied', 'ok');
    } catch {
      getSelection().selectAllChildren(logBox);
      toast('Press Ctrl+C (⌘C) to copy the selected log');
    }
  };
  await renderTasks();
  await renderLogs();
  const page = h('div', { class: 'form', style: { maxWidth: 'none' } }, tasks,
    h('div', { class: 'form-section' }, h('div', { class: 'row' }, h('h2', { class: 'grow' }, 'Log'), h('button', { class: 'btn sm', type: 'button', onclick: copy }, 'Copy log')), logBox));
  const banner = staleBanner(page);
  every(ctx, 2000, renderTasks, banner.hooks('tasks'));
  every(ctx, 3000, renderLogs, banner.hooks('logs'));
  return h('div', null, banner.el, page);
}

async function aboutSection() {
  const i = await api('/api/admin/info');
  const f = i.ffmpeg;
  return h('div', { class: 'form' },
    h('div', { class: 'form-section' }, h('h2', null, 'AI disclosure'),
      h('p', null, 'Lex was built by large language models: LLM coding agents wrote its code, interface, tests and documentation under human direction. LLM-written code can contain mistakes, including security ones, and has not had an independent security review. Lex does not call AI model services at runtime.')),
    h('div', { class: 'form-section' }, h('h2', null, 'Lex'), h('dl', { class: 'kv' },
      h('dt', null, 'Version'), h('dd', null, i.version),
      h('dt', null, 'Go runtime'), h('dd', null, i.go),
      h('dt', null, 'Data folder'), h('dd', { class: 'mono' }, i.dataDir),
      h('dt', null, 'Database'), h('dd', null, fmtBytes(i.dbSize)))),
    h('div', { class: 'form-section' }, h('h2', null, 'ffmpeg'), h('dl', { class: 'kv' },
      h('dt', null, 'ffmpeg'), h('dd', { class: 'mono' }, `${f.path} (${f.version || 'not found'})`),
      h('dt', null, 'ffprobe'), h('dd', { class: 'mono' }, f.probe),
      h('dt', null, 'H.264 encoders'), h('dd', null, (f.encoders || []).join(', ') || 'none'),
      h('dt', null, 'V4L2 device'), h('dd', null, f.v4l2Device ? 'present' : 'not present (hardware encoding unavailable)'),
      h('dt', null, 'Tone mapping'), h('dd', null, f.hasZscale && f.hasTonemap ? 'available' : 'unavailable'))),
    h('div', { class: 'form-section' }, h('h2', null, 'Caches'),
      h('div', { class: 'toggle-row' }, h('div', { class: 'lbl' }, h('b', null, 'Artwork'), h('span', { class: 'help' }, `${fmtBytes(i.imageCache)} — re-downloaded on demand`)), h('button', { class: 'btn sm', 'aria-label': 'Clear artwork cache', dataset: { focusKey: 'clear-images' }, onclick: async (e) => { if (await run(e.currentTarget, () => api('/api/admin/cache/clear', { method: 'POST', body: { kind: 'images' } }), 'Artwork cache cleared')) refreshSoft(); } }, 'Clear')),
      h('div', { class: 'toggle-row' }, h('div', { class: 'lbl' }, h('b', null, 'Subtitles'), h('span', { class: 'help' }, `${fmtBytes(i.subsCache)} — extracted WebVTT`)), h('button', { class: 'btn sm', 'aria-label': 'Clear subtitle cache', dataset: { focusKey: 'clear-subs' }, onclick: async (e) => { if (await run(e.currentTarget, () => api('/api/admin/cache/clear', { method: 'POST', body: { kind: 'subs' } }), 'Subtitle cache cleared')) refreshSoft(); } }, 'Clear'))));
}

// ======================================================================
// Dashboard
// ======================================================================

const DASH = [['live', 'Live', 'broadcast'], ['playback', 'Playback', 'stats'], ['library', 'Library', 'library']];

export async function dashboardView(ctx, tab) {
  if (!state.me.isAdmin) return emptyState({ title: 'Admins only', text: 'Ask the server owner for access.', actions: [h('a', { class: 'btn primary', href: '#/' }, 'Go home')] });
  if (tab === 'history') tab = 'playback'; // merged into Playback
  const content = await ({ live: liveTab, playback: playbackTab, library: libraryTab }[tab] || liveTab)(ctx);
  return h('div', { class: 'page' },
    h('h1', { class: 'page-title' }, 'Dashboard'),
    h('div', { class: 'tabs', style: { marginTop: '14px' } }, DASH.map(([id, label]) => h('button', { class: id === tab ? 'active' : '', onclick: () => { location.hash = `#/dashboard/${id}`; } }, label))),
    content);
}

function stat(label, value, sub, extra) {
  return h('div', { class: 'stat' }, h('div', { class: 'l' }, label), h('div', { class: 'v' }, value), sub != null ? h('div', { class: 'sub' }, sub) : null, extra);
}

function meter(pct, hotAt = 85) {
  return h('div', { class: 'meter' }, h('i', { class: pct >= hotAt ? 'hot' : '', style: { width: `${Math.min(100, Math.max(0, pct))}%` } }));
}

const bps = (bytesPerSec) => fmtBitrate(bytesPerSec * 8);

// Live: what's playing first, then server health (each card with its own
// last-hour chart), then storage, then one line about the host.
async function liveTab(ctx) {
  const sessionsBox = h('div', { class: 'sessions' });
  const nowTitle = h('h2', { class: 'section-title' }, 'Now playing');
  const cards = { cpu: h('div', { class: 'dcard' }), mem: h('div', { class: 'dcard' }), temp: h('div', { class: 'dcard' }), net: h('div', { class: 'dcard' }) };
  const charts = { cpu: h('div', { class: 'dchart' }), mem: h('div', { class: 'dchart' }), temp: h('div', { class: 'dchart' }), net: h('div', { class: 'dchart' }) };
  const storage = h('div', { class: 'panel' });
  const host = h('div', { class: 'dash-host' });
  // Element.append would print null and stringify arrays; flatten like h().
  const put = (el, ...kids) => clear(el).append(...kids.flat(Infinity).filter((k) => k != null && k !== false));
  const card = (el, label, value, sub, ...rest) => put(el,
    h('div', { class: 'dcard-head' }, h('span', { class: 'l' }, label), h('span', { class: 'v' }, value)),
    h('div', { class: 'sub' }, sub || ''), ...rest);
  let hist = [], haveSystem = false;

  const renderSystem = async (withHistory) => {
    // Until a first load succeeds, keep asking for the history too.
    withHistory ||= !haveSystem;
    const r = await api(`/api/admin/stats/system${withHistory ? '?history=1' : ''}`);
    if (!ctx.isCurrent()) return;
    haveSystem = true;
    const s = r.snapshot, n = s.now;
    if (withHistory) hist = r.history || [];
    else { hist.push(n); if (hist.length > 1200) hist.shift(); }
    const memPct = s.memTotal ? (s.memUsed / s.memTotal) * 100 : 0;
    const flags = s.throttleFlags?.length ? s.throttleFlags.join(', ') : '';
    card(cards.cpu, 'CPU', `${n.cpu.toFixed(0)}%`,
      `load ${s.load.map((x) => x.toFixed(2)).join(' · ')}${s.freqMhz ? ` · ${Math.round(s.freqMhz)} MHz` : ''}`,
      h('div', { class: 'cores' }, (s.cores || []).map((c, i) => h('div', { class: 'core', title: `Core ${i}: ${c.toFixed(0)}%` }, h('i', { style: { height: `${c}%` } }), h('span', null, `${c.toFixed(0)}`)))),
      charts.cpu);
    card(cards.mem, 'Memory', `${memPct.toFixed(0)}%`,
      `${fmtBytes(s.memUsed)} of ${fmtBytes(s.memTotal)}${s.swapTotal ? ` · swap ${fmtBytes(s.swapUsed)}` : ''}`,
      h('div', { class: 'small dim' }, `Lex uses ${fmtBytes(s.procRss)} (Go heap ${fmtBytes(s.goHeap)}) · ${s.procCpu.toFixed(1)}% CPU`),
      charts.mem);
    // Hosts without a sensor (VMs, containers on a laptop) get no card.
    const hasTemp = !!n.temp || hist.some((p) => p.temp);
    cards.temp.hidden = !hasTemp;
    if (hasTemp) {
      card(cards.temp, 'Temperature', n.temp ? `${n.temp.toFixed(0)}°C` : '—',
        flags ? h('span', { class: 'warn' }, flags) : s.throttled ? 'No throttling' : '',
        charts.temp);
    }
    card(cards.net, 'Network', bps(n.tx),
      `out · in ${bps(n.rx)} · streaming ${bps(n.stream)}`,
      charts.net);
    const times = hist.map((p) => p.t);
    if (times.length > 1) {
      lineChart(charts.cpu, { times, height: 96, max: 100, fmt: (v) => `${v.toFixed(0)}%`, series: [{ name: 'Host', values: hist.map((p) => p.cpu) }, { name: 'Lex', values: hist.map((p) => p.pcpu / Math.max(1, s.numCpu)) }] });
      lineChart(charts.mem, { times, height: 96, max: 100, fmt: (v) => `${v.toFixed(0)}%`, series: [{ name: 'Used', values: hist.map((p) => p.mem) }] });
      if (hasTemp) lineChart(charts.temp, { times, height: 96, max: 90, fmt: (v) => `${v.toFixed(0)}°`, series: [{ name: 'SoC', values: hist.map((p) => p.temp) }] });
      lineChart(charts.net, { times, height: 96, fmt: (v) => bps(v), series: [{ name: 'Out', values: hist.map((p) => p.tx) }, { name: 'Streaming', values: hist.map((p) => p.stream) }] });
    }
    // Storage: disks, SSD cache and current disk activity in one place.
    const row = (name, used, total, hot, note) => h('div', { class: 'srow' },
      h('div', { class: 'row small' }, h('span', { class: 'grow ellipsis' }, name), h('span', { class: 'muted nowrap' }, note || `${fmtBytes(used)} of ${fmtBytes(total)} · ${fmtBytes(Math.max(0, total - used))} free`)),
      meter(total ? (used / total) * 100 : 0, hot));
    const c = r.cache;
    put(storage,
      h('div', { class: 'row', style: { marginBottom: '10px' } }, h('h3', { class: 'grow', style: { margin: 0 } }, 'Storage'),
        h('span', { class: 'small muted' }, `read ${fmtBytes(n.dr)}/s · write ${fmtBytes(n.dw)}/s`)),
      (s.disks || []).map((d) => row(h('span', { class: 'mono' }, d.path), d.used, d.total, 92)),
      c?.enabled ? row('SSD cache', c.usedBytes, c.maxBytes, 97,
        `${fmtBytes(c.usedBytes)} of ${fmtBytes(c.maxBytes)} · ${c.files} files${c.current ? ` · copying ${Math.round((c.current.done / c.current.size) * 100)}%` : ''}`) : null);
    host.textContent = [s.model || s.os, s.arch, `${s.numCpu} cores`, `up ${fmtUptime(s.uptime)}`,
      `${r.remuxJobs} remux · ${r.transcodeJobs} transcode jobs`].filter(Boolean).join('  ·  ');
  };

  const renderSessions = async () => {
    const list = await api('/api/admin/stats/sessions');
    if (!ctx.isCurrent()) return;
    nowTitle.textContent = list.length ? `Now playing · ${list.length}` : 'Now playing';
    // Keep expanded "details" open across refreshes.
    const open = new Set([...sessionsBox.querySelectorAll('details[open]')].map((d) => d.dataset.id));
    clear(sessionsBox);
    if (!list.length) { sessionsBox.appendChild(h('div', { class: 'panel dim' }, 'Nothing is playing right now.')); return; }
    list.sort((a, b) => b.startedAt - a.startedAt);
    for (const s of list) {
      const c = s.clientStats || {};
      const j = s.job;
      const pct = s.duration ? (s.position / s.duration) * 100 : 0;
      const health = [
        ['Buffer', `${(c.bufferAhead || 0).toFixed(0)}s`, (c.bufferAhead || 0) < 3 ? 'bad' : (c.bufferAhead || 0) < 10 ? 'warn' : ''],
        ['Delivery', bps(s.rate)],
        ['Stalls', c.bufferEvents ? `${c.bufferEvents} · ${(c.bufferSeconds || 0).toFixed(0)}s` : 'none', c.bufferEvents ? 'warn' : ''],
        ['Read from', s.cached ? 'SSD cache' : 'Library disk'],
      ];
      const details = [
        ['Video', `${s.videoIn || '?'} → ${s.videoOut || ''}`],
        ['Audio', `${s.audioIn || '?'} → ${s.audioOut || ''}`],
        ['Bitrate', `${fmtBitrate((s.srcBitrate || 0) * 1000)} source${s.method === 'transcode' ? ` → ${fmtBitrate((s.outBitrate || 0) * 1000)}` : ''} · ${fmtBytes(s.bytes)} sent`],
        ['Client', `${s.client} · ${s.ip}${s.remote ? ' (remote)' : ' (local)'} · ${c.bandwidth ? fmtBitrate(c.bandwidth) : '—'} download`],
        ['Picture', `${c.resolution || '—'}${c.totalFrames ? ` · ${c.droppedFrames} of ${c.totalFrames} frames dropped` : ''}`],
      ];
      if (j) details.push(['ffmpeg', j.exited ? (j.error ? `exited: ${j.error}` : 'finished') : `${j.throttled ? 'waiting (client buffer full)' : `${(j.speed || 0).toFixed(2)}x · ${Math.round(j.fps || 0)} fps`} · CPU ${Math.round(j.cpu || 0)}% · ${s.restarts} restart${s.restarts === 1 ? '' : 's'}`]);
      const det = h('details', { 'data-id': s.id, open: open.has(s.id) }, h('summary', null, 'Details'),
        h('dl', { class: 'kv' }, details.map(([k, v]) => [h('dt', null, k), h('dd', null, v)])));
      sessionsBox.appendChild(h('div', { class: 'session' },
        h('a', { class: 'poster', href: `#/item/${s.itemId}` }, h('img', { src: img({ id: s.itemId }, 'poster', 160), alt: '' })),
        h('div', { style: { minWidth: 0 } },
          h('div', { class: 'row' },
            h('div', { class: 'grow', style: { minWidth: 0 } },
              h('h3', { class: 'ellipsis' }, s.title),
              h('div', { class: 'dim small ellipsis' }, [s.subtitle, s.userName, s.client, s.remote ? 'remote' : 'local'].filter(Boolean).join(' · '))),
            h('span', { class: `method ${s.method}`, title: s.reasons?.map(reasonLabel).join('; ') || '' }, METHOD_LABEL[s.method] || s.method),
            h('button', { class: 'btn sm danger', title: 'Stop this stream', onclick: async (e) => {
              const btn = e.currentTarget;
              if (await confirmDialog(`Stop ${s.userName}'s stream?`, 'Stop', true) && await run(btn, () => api(`/api/admin/sessions/${s.id}`, { method: 'DELETE' }))) renderSessions().catch(() => {});
            } }, 'Stop')),
          h('div', { class: 'row small', style: { marginTop: '10px' } },
            h('span', { class: 'muted nowrap' }, `${s.paused ? 'Paused' : 'Playing'} · ${fmtTime(s.position)} / ${fmtTime(s.duration)}`), h('div', { class: 'grow' }, meter(pct, 101))),
          s.reasons?.length ? h('div', { class: 'small muted', style: { marginTop: '6px' } }, `Why ${s.method === 'transcode' ? 'transcoding' : 'converting'}: ${s.reasons.map(reasonLabel).join(' · ')}`) : null,
          h('div', { class: 'health' }, health.map(([k, v, cls]) => h('div', null, h('span', null, k), h('b', { class: cls || '' }, v)))),
          det)));
    }
  };

  // The first load failing leaves labelled placeholders, not empty boxes.
  const unavailable = () => {
    card(cards.cpu, 'CPU', '—', 'Unavailable');
    card(cards.mem, 'Memory', '—', 'Unavailable');
    card(cards.net, 'Network', '—', 'Unavailable');
    cards.temp.hidden = true;
    put(storage, h('h3', { style: { margin: 0 } }, 'Storage'), h('div', { class: 'dim small', style: { marginTop: '10px' } }, 'Unavailable'));
  };
  const [sysOk, sessOk] = await Promise.all([renderSystem(true).then(() => true, () => false), renderSessions().then(() => true, () => false)]);
  if (!sysOk) unavailable();
  if (!sessOk) put(sessionsBox, h('div', { class: 'panel dim' }, 'Unavailable'));
  const live = h('div', { class: 'dash-live' },
    h('section', null, h('div', { class: 'dash-head' }, nowTitle), sessionsBox),
    h('section', null, h('div', { class: 'dash-head' }, h('h2', { class: 'section-title' }, 'Server'), h('span', { class: 'muted small' }, 'last hour')),
      h('div', { class: 'dcards' }, cards.cpu, cards.mem, cards.temp, cards.net)),
    h('section', null, storage),
    host);
  const banner = staleBanner(live);
  if (!sysOk) banner.fail(null, 'system');
  if (!sessOk) banner.fail(null, 'sessions');
  // Every success clears that poller's failure, including a failed first load.
  const poll = (ms, key, fn) => every(ctx, ms, async () => { await fn(); banner.ok(key); }, { onFail: (t) => banner.fail(t, key) });
  poll(3000, 'system', () => renderSystem(false));
  poll(2000, 'sessions', renderSessions);
  return [banner.el, live];
}

async function playbackTab(ctx) {
  const days = +(ctx.query.get('days') || 30);
  const st = await api(`/api/admin/stats/playback?days=${days}&tz=${-new Date().getTimezoneOffset()}`);
  // Fill missing days so the chart has a continuous axis.
  const byDay = Object.fromEntries((st.days || []).map((d) => [d.day, d]));
  const labels = [], full = [], hours = [], plays = [];
  const nDays = Math.min(days, 120);
  for (let i = nDays - 1; i >= 0; i--) {
    const dt = new Date(Date.now() - i * 86400000);
    const key = `${dt.getFullYear()}-${String(dt.getMonth() + 1).padStart(2, '0')}-${String(dt.getDate()).padStart(2, '0')}`;
    labels.push(dt.toLocaleDateString(undefined, { month: 'short', day: 'numeric' }));
    full.push(dt.toLocaleDateString(undefined, { dateStyle: 'medium' }));
    hours.push(byDay[key]?.hours || 0);
    plays.push(byDay[key]?.plays || 0);
  }
  const hoursBox = h('div'), hourBox = h('div');
  const hod = Array.from({ length: 24 }, (_, i) => (st.hoursOfDay || []).find((b) => +b.key === i)?.count || 0);
  const methodColor = (it) => ({ direct: SERIES[0], remux: SERIES[1], transcode: SERIES[2] })[it.key] || SERIES[0];
  const mlabel = (k) => METHOD_LABEL[k] || k;
  const page = h('div', null,
    h('div', { class: 'toolbar' }, h('span', { class: 'muted' }, 'Range'), [7, 30, 90, 365].map((d) => h('button', { class: `btn sm ${d === days ? 'primary' : ''}`, onclick: () => { location.hash = `#/dashboard/playback?days=${d}`; } }, `${d} days`)),
      h('div', { class: 'spacer' }), h('button', { class: 'btn sm ghost', onclick: () => document.getElementById('history')?.scrollIntoView({ behavior: motionOK() ? 'smooth' : 'auto' }) }, 'Jump to history')),
    h('div', { class: 'stats-grid' },
      stat('Plays', st.plays.toLocaleString(), `${st.uniqueItems} titles${st.plays ? ` · ${Math.round((st.remotePlays / st.plays) * 100)}% remote` : ''}`),
      stat('Watch time', fmtHours(st.hours), `${fmtHours(st.hours / Math.max(1, days))} a day`),
      stat('Data served', fmtBytes(st.bytes), st.plays ? `${fmtBytes(st.bytes / st.plays)} per play` : ''),
      stat('Buffering', st.hours ? `${((st.bufferSeconds / (st.hours * 3600)) * 100).toFixed(2)}%` : '—', `of watch time · ${st.bufferEvents} stalls, ${st.bufferSeconds.toFixed(0)}s`)),
    h('div', { class: 'dash-2' },
      h('div', { class: 'panel' }, h('h3', null, 'Hours watched per day'), hoursBox),
      h('div', { class: 'panel' }, h('h3', null, 'Plays by hour of day'), hourBox)),
    h('div', { class: 'dash-3' },
      h('div', { class: 'panel' }, h('h3', null, 'How streams played'), methodsTable(st, mlabel, methodColor),
        st.reasons?.length ? [h('h4', { class: 'panel-sub' }, 'Why streams were converted'), barList(st.reasons.map((m) => ({ label: m.key, value: m.count })), (v) => `${v}`)] : null),
      h('div', { class: 'panel' }, h('h3', null, 'Most watched'), barList((st.topItems || []).map((m) => ({ label: m.key, value: m.count })), (v) => `${v} play${v === 1 ? '' : 's'}`)),
      h('div', { class: 'panel' }, h('h3', null, "Who's watching"), barList((st.users || []).map((m) => ({ label: m.key, value: m.value })), (v) => fmtHours(v)),
        h('h4', { class: 'panel-sub' }, 'Clients'), barList((st.clients || []).map((m) => ({ label: m.key || 'unknown', value: m.count })), (v) => `${v} play${v === 1 ? '' : 's'}`))));
  page.append(await historySection(ctx, days));
  // Paging through history keeps you at the table.
  if (ctx.query.get('offset')) requestAnimationFrame(() => document.getElementById('history')?.scrollIntoView());
  requestAnimationFrame(() => {
    columnChart(hoursBox, { labels, fullLabels: full, values: hours, fmt: (v) => (v > 0 && v < 1 ? `${Math.round(v * 60)}m` : `${+v.toFixed(1)}h`), tipLabel: 'watched' });
    columnChart(hourBox, { labels: hod.map((_, i) => `${i}h`), values: hod, fmt: (v) => `${Math.round(v)}`, tipLabel: 'plays' });
  });
  return page;
}

const fmtHours = (v) => (v > 0 && v < 1 ? `${Math.round(v * 60)} min` : v < 100 ? `${v.toFixed(1)} h` : `${Math.round(v).toLocaleString()} h`);

// Plays, share and stall time per playback method, in one table.
function methodsTable(st, mlabel, color) {
  const plays = Object.fromEntries((st.methods || []).map((m) => [m.key, m.count]));
  const stall = Object.fromEntries((st.bufferByMethod || []).map((m) => [m.key, m.value]));
  const total = Object.values(plays).reduce((a, b) => a + b, 0);
  const rows = ['direct', 'remux', 'transcode'].filter((k) => plays[k]);
  if (!rows.length) return h('div', { class: 'dim small' }, 'No plays yet.');
  return h('table', { class: 'tbl mtable' },
    h('tr', null, h('th', null, ''), h('th', null, 'Plays'), h('th', null, 'Stalled')),
    rows.map((k) => h('tr', null,
      h('td', null, h('i', { class: 'dot', style: { background: color({ key: k }) } }), mlabel(k)),
      h('td', null, `${plays[k]}`, h('span', { class: 'dim' }, ` · ${Math.round((plays[k] / total) * 100)}%`)),
      h('td', null, stall[k] != null ? `${stall[k].toFixed(2)}%` : '—'))));
}

async function libraryTab() {
  const st = await api('/api/admin/stats/library');
  const bucketList = (dim, b, fmtKey = (k) => k) => barList([...(b || [])].sort((x, y) => y.value - x.value)
    .map((x) => ({ label: `${fmtKey(x.key)} (${x.count})`, value: x.value, onClick: () => libraryTitles(dim, x.key, `${DIM_LABEL[dim]}: ${fmtKey(x.key)}`) })), (v) => fmtBytes(v));
  return h('div', null,
    h('div', { class: 'stats-grid' },
      stat('Movies', st.movies.toLocaleString()),
      stat('Shows', st.shows.toLocaleString(), `${st.seasons} seasons · ${st.episodes.toLocaleString()} episodes`),
      stat('Files', fmtBytes(st.totalBytes), `${st.files.toLocaleString()} files · ${fmtHours(st.totalDuration / 3600)} of video${st.unprobed ? ` · ${st.unprobed} awaiting analysis` : ''}${st.probeErrors ? ` · ${st.probeErrors} unreadable` : ''}`),
      stat('Metadata', `${st.metaMatched} matched`, `${st.metaMissing} not found · ${st.metaPending} pending`)),
    h('p', { class: 'small dim', style: { margin: '0 0 10px' } }, 'Bars show storage used and the number of files is in brackets. Click a row to see the titles in it.'),
    h('div', { class: 'dash-3' },
      h('div', { class: 'panel' }, h('h3', null, 'Libraries'), bucketList('library', st.libraries)),
      h('div', { class: 'panel' }, h('h3', null, 'Resolution'), bucketList('resolution', st.resolutions)),
      h('div', { class: 'panel' }, h('h3', null, 'HDR'), bucketList('hdr', st.hdr)),
      h('div', { class: 'panel' }, h('h3', null, 'Video codecs'), bucketList('video', st.videoCodecs, (k) => k.toUpperCase())),
      h('div', { class: 'panel' }, h('h3', null, 'Audio (any track)'), bucketList('audio', st.audioCodecs, (k) => k.toUpperCase())),
      h('div', { class: 'panel' }, h('h3', null, 'Containers'), bucketList('container', st.containers, (k) => k.toUpperCase()))));
}

const DIM_LABEL = { library: 'Library', resolution: 'Resolution', hdr: 'HDR', video: 'Video codec', audio: 'Audio track', container: 'Container' };

// libraryTitles shows the movies and shows in one Library-stats category.
async function libraryTitles(dim, key, label) {
  const list = h('div', { class: 'title-list' }, spinner());
  const m = modal({ title: label, body: list, wide: true });
  try {
    const rows = await api(`/api/admin/stats/library/titles?dim=${encodeURIComponent(dim)}&key=${encodeURIComponent(key)}`);
    const total = rows.reduce((a, r) => a + r.bytes, 0);
    clear(list).append(
      h('div', { class: 'small muted' }, `${rows.length} title${rows.length === 1 ? '' : 's'} · ${fmtBytes(total)}`),
      ...rows.map((r) => h('a', { class: 'title-row', href: `#/item/${r.itemId}`, onclick: () => m.close() },
        h('img', { src: img({ id: r.itemId }, 'poster', 80), alt: '', loading: 'lazy' }),
        h('div', { class: 'grow', style: { minWidth: 0 } },
          h('div', { class: 'ellipsis' }, r.title, r.year ? h('span', { class: 'dim' }, ` (${r.year})`) : null),
          h('div', { class: 'small dim' }, r.kind === 'show' ? `${r.files} episode${r.files === 1 ? '' : 's'}` : r.files > 1 ? `${r.files} files` : 'Movie')),
        h('span', { class: 'muted small nowrap' }, fmtBytes(r.bytes)))));
    if (!rows.length) list.append(h('div', { class: 'dim' }, 'Nothing here.'));
  } catch (e) {
    clear(list).append(h('div', { class: 'bad' }, e.message));
  }
}

// Every play, newest first, under the playback statistics.
async function historySection(ctx, days) {
  const offset = +(ctx.query.get('offset') || 0);
  const rows = await api(`/api/admin/history?limit=100&offset=${offset}`);
  const page = (o) => `#/dashboard/playback?days=${days}&offset=${o}`;
  return h('section', { id: 'history', style: { marginTop: '28px' } },
    h('div', { class: 'toolbar' }, h('h2', { class: 'section-title' }, 'History'),
      rows.length ? h('span', { class: 'muted small' }, `${offset + 1}–${offset + rows.length}`) : null, h('div', { class: 'spacer' }),
      offset > 0 ? h('a', { class: 'btn sm', href: page(Math.max(0, offset - 100)) }, 'Newer') : null,
      rows.length === 100 ? h('a', { class: 'btn sm', href: page(offset + 100) }, 'Older') : null,
      h('button', { class: 'btn sm danger', onclick: async (e) => {
        const btn = e.currentTarget;
        if (await confirmDialog('Delete all playback history? Watch progress is kept.', 'Delete', true) && await run(btn, () => api('/api/admin/history', { method: 'DELETE' }))) route();
      } }, 'Clear history')),
    rows.length ? h('div', { class: 'panel table-wrap' }, h('table', { class: 'tbl' },
      h('tr', null, ['When', 'User', 'Title', 'Method', 'Watched', 'Data', 'Stalls', 'Output', 'Client'].map((t) => h('th', null, t))),
      rows.map((r) => h('tr', null,
        h('td', { class: 'nowrap' }, fmtDate(r.startedAt)),
        h('td', null, r.userName),
        h('td', null, h('a', { href: `#/item/${r.itemId}` }, r.title)),
        h('td', { title: r.reasons }, h('span', { class: `method ${r.method}` }, METHOD_LABEL[r.method] || r.method), r.reasons ? h('div', { class: 'dim small' }, r.reasons) : null),
        h('td', { class: 'nowrap' }, fmtDuration(r.watched) || `${Math.round(r.watched)}s`),
        h('td', { class: 'nowrap' }, fmtBytes(r.bytes)),
        h('td', null, r.bufferEvents ? `${r.bufferEvents} (${r.bufferSeconds.toFixed(0)}s)` : '0'),
        h('td', { class: 'small' }, [r.videoOut, r.audioOut].filter(Boolean).join(' / ')),
        h('td', { class: 'small' }, `${r.client}`, h('div', { class: 'dim mono' }, `${r.ip}${r.remote ? ' · remote' : ''}`))))))
      : h('div', { class: 'empty' }, h('h2', null, 'No playback history yet'), h('p', null, 'Plays are recorded when a session ends.')));
}
