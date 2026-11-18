// Playback compatibility: what this device can play, and how much of the
// library it can play as-is (shown in Playback settings and on the dashboard).

import { h, clear, icons } from '../ui.js';
import { api } from '../api.js';
import { QUALITIES } from '../prefs.js';
import { compatRequest, TIERS, TIER_ORDER, causeLabel } from '../caps.js';
import { titlesModal } from '../admin.js';

// compatPanel shows, on the Library dashboard, how the library plays in
// this browser with this device's playback settings: how many files play as
// they are, how many the server must repackage or convert, and why. It
// loads on its own so the rest of the tab doesn't wait for every file to be
// checked.
export function compatPanel() {
  const body = h('div', { 'aria-live': 'polite' }, h('div', { class: 'row' }, h('div', { class: 'spinner sm' }), h('span', { class: 'muted small' }, 'Checking every file…')));
  const req = compatRequest();
  api('/api/admin/compat', { method: 'POST', body: req })
    // h() flattens the nested arrays and skips nulls; append() wouldn't.
    .then((sum) => clear(body).append(h('div', { class: 'compat' }, compatSummary(sum, req))))
    .catch((e) => clear(body).append(h('p', { class: 'bad', style: { margin: 0 } }, e.message)));
  return h('div', { class: 'panel compat-panel' }, h('h2', { class: 'panel-title' }, 'How it plays in this browser'), body);
}

export const TIER_COLOR = { direct: 'var(--method-direct)', browser: 'var(--good-ink)', remux: 'var(--method-remux)', transcode: 'var(--method-transcode)', unplayable: 'var(--bad-strong)', unknown: 'var(--bg4)' };

export const plural = (n, w) => `${n.toLocaleString()} ${w}${n === 1 ? '' : 's'}`;

// Groups that make the server work, worst first. Plays as-is and browser
// repackaging need nothing, so they only appear in the legend.
export const WORK_TIERS = ['unplayable', 'transcode', 'remux', 'unknown'];

export function compatSummary(sum, req) {
  if (!sum.files) return [h('p', { class: 'muted', style: { margin: 0 } }, 'No media files yet.')];
  const tiers = TIER_ORDER.filter((t) => sum.tiers[t]);
  const easy = (sum.tiers.direct || 0) + (sum.tiers.browser || 0) + (sum.tiers.remux || 0);
  const pct = (n) => { const p = (n / sum.files) * 100; return p > 0 && p < 1 ? '<1%' : `${Math.round(p)}%`; };
  const open = (tier, cause) => titlesModal(cause ? causeLabel(cause, sum) : TIERS[tier].label,
    () => api('/api/admin/compat/titles', { method: 'POST', body: { ...req, tier, cause } }),
    h('p', { class: 'small muted', style: { margin: '0 0 10px' } }, TIERS[tier].text));
  const headline = sum.tiers.transcode || sum.tiers.unplayable
    ? `${pct(easy)} of your files play here without being converted`
    : 'Everything plays here without being converted';
  // Being remote only matters when the server's remote bitrate limit applies.
  const capped = !req.maxBitrate && sum.remote && sum.causes.some((c) => c.kind === 'bitrate');
  const quality = (QUALITIES.find(([v]) => v === req.maxBitrate) || [0, `${req.maxBitrate} kbps`])[1];
  const method = { auto: 'Automatic', direct: 'Prefer Direct Play', remux: 'Always Direct Stream', transcode: 'Always Transcode' }[req.mode] || 'Automatic';
  const dot = (t) => h('i', { class: 'compat-dot', style: { background: TIER_COLOR[t] } });

  // The reasons in one group. When converting would fix it, only the
  // reasons it can't be repackaged count; "conversion is off" becomes a link.
  const group = (t) => {
    const converts = t === 'transcode' || t === 'unplayable';
    const all = sum.causes.filter((c) => c.tier === t && (!converts || c.stage === 'stream'));
    const off = all.find((c) => c.kind === 'setting' && c.key === 'transcode');
    const reasons = all.filter((c) => c !== off).sort((a, b) => (a.kind === 'audio') - (b.kind === 'audio') || b.files - a.files);
    return h('section', { class: 'compat-group' },
      h('button', { type: 'button', class: 'compat-head', onclick: () => open(t) },
        dot(t), h('b', null, TIERS[t].label), h('span', { class: 'muted' }, plural(sum.tiers[t], 'file')), h('span', { class: 'chev', 'aria-hidden': 'true', html: icons.chevR })),
      reasons.length ? h('ul', { class: 'compat-reasons' }, reasons.map((c) => h('li', null,
        h('button', { type: 'button', onclick: () => open(t, { stage: c.stage, kind: c.kind, key: c.key }) },
          h('span', null, causeLabel(c, sum)), h('span', { class: 'muted nowrap' }, c.files.toLocaleString()))))) : null,
      off ? h('p', { class: 'compat-fix small' }, 'Converting video is turned off. ', h('a', { href: '#/settings/transcoding' }, 'Turn it on'), ' to play these.') : null);
  };
  const work = WORK_TIERS.filter((t) => sum.tiers[t]);

  return [
    h('div', { class: 'compat-overview' },
      h('p', { class: 'compat-headline' }, headline),
      h('div', { class: 'compat-bar', role: 'img', 'aria-label': tiers.map((t) => `${TIERS[t].label}: ${sum.tiers[t]}`).join(', ') },
        tiers.map((t) => h('i', { style: { flexGrow: sum.tiers[t], background: TIER_COLOR[t] }, title: `${TIERS[t].label}: ${plural(sum.tiers[t], 'file')}` }))),
      h('ul', { class: 'compat-legend' }, tiers.map((t) => h('li', null,
        h('button', { type: 'button', onclick: () => open(t), title: TIERS[t].text },
          dot(t), h('span', null, TIERS[t].label), h('span', { class: 'num' }, sum.tiers[t].toLocaleString()), h('span', { class: 'muted num' }, pct(sum.tiers[t])))))),
      h('p', { class: 'help compat-help' },
        'This browser’s codecs and this device’s ', h('a', { href: '#/settings/preferences' }, 'playback settings'), ` (${quality}, ${method.toLowerCase()})${capped ? ', with the server’s limit for streaming from outside your network' : ''}. `,
        'Assumes the default audio track and no subtitles; image subtitles (PGS, VobSub) make the server convert the video.')),
    h('div', { class: 'compat-work' },
      h('h3', null, 'What needs work from the server'),
      work.length ? work.map(group) : h('p', { class: 'muted', style: { margin: 0 } }, 'Nothing: every file plays as it is or is repackaged by this browser.')),
  ];
}
