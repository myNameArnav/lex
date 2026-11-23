// The Dashboard: Live (now playing, server health, storage), Playback
// (history and charts) and Library (counts, playback compatibility, titles
// that need attention) tabs.

import { h, clear, toast, modal, confirmDialog, spinner, fmtBytes, fmtBitrate, fmtTime, fmtDate, timeAgo, fmtUptime, fmtDuration, run, staleBanner, motionOK, METHOD_LABEL, reasonLabel, emptyState } from '../ui.js';
import { api, img } from '../api.js';
import { state, refreshSoft, focusAfterRoute, fixMatch } from '../app.js';
import { lineChart, columnChart, barList, axisHours, axisBitrate } from '../charts.js';
import { compatPanel } from './compat.js';
import { every } from './poll.js';

/** @typedef {import('../../../types/api.gen').Session} Session */
/** @typedef {import('../../../types/api.gen').PlaybackStats} PlaybackStats */
/** @typedef {import('../../../types/api.gen').ClientStats} ClientStats */

const DASH = [['live', 'Live'], ['playback', 'Playback'], ['library', 'Library']];

export async function dashboardView(ctx, tab) {
  if (!state.me.isAdmin) return emptyState({ title: 'Admins only', text: 'Ask the server owner for access.', actions: [h('a', { class: 'btn primary', href: '#/' }, 'Go home')] });
  if (tab === 'history') tab = 'playback'; // merged into Playback
  if (!DASH.some(([id]) => id === tab)) tab = 'live';
  const content = await ({ live: liveTab, playback: playbackTab, library: libraryTab })[tab](ctx);
  return h('div', { class: 'page' },
    h('h1', { class: 'page-title' }, 'Dashboard'),
    // Links, so they open in a new tab; the next view keeps focus on the tab.
    h('nav', { class: 'tabs', 'aria-label': 'Dashboard sections', style: { marginTop: '14px' } }, DASH.map(([id, label]) => h('a', {
      href: `#/dashboard/${id}`, class: id === tab ? 'active' : null, 'aria-current': id === tab ? 'page' : null,
      dataset: { focusKey: `dash-tab-${id}` },
      onclick: (e) => { if (id !== tab && !e.ctrlKey && !e.metaKey && !e.shiftKey) focusAfterRoute(`dash-tab-${id}`); },
    }, label))),
    content);
}

function stat(label, value, sub, extra) {
  return h('div', { class: 'stat' }, h('div', { class: 'l' }, label), h('div', { class: 'v' }, value), sub != null ? h('div', { class: 'sub' }, sub) : null, extra);
}

export function meter(pct, hotAt = 85) {
  return h('div', { class: 'meter' }, h('i', { class: pct >= hotAt ? 'hot' : '', style: { width: `${Math.min(100, Math.max(0, pct))}%` } }));
}

const bps = (bytesPerSec) => fmtBitrate(bytesPerSec * 8);

// Polled views update text in place: an unchanged node keeps any selection.
const setText = (el, v) => { if (el.textContent !== v) el.textContent = v; };

// Live: what's playing first, then server health (each card with its own
// last-hour chart), then storage, then one line about the host. Polls update
// the cards in place so focus, selection and open details survive.
async function liveTab(ctx) {
  const sessionsBox = h('div', { class: 'sessions' });
  const nowTitle = h('h2', { class: 'section-title', tabindex: '-1' }, 'Now playing');
  const mkCard = (label) => {
    const c = { label, v: h('span', { class: 'v' }), sub: h('div', { class: 'sub' }), extra: h('div', { class: 'dcard-extra' }), chart: h('div', { class: 'dchart' }) };
    c.el = h('div', { class: 'dcard' }, h('div', { class: 'dcard-head' }, h('span', { class: 'l' }, label), c.v), c.sub, c.extra, c.chart);
    return c;
  };
  // Disk instead of a temperature chart: on a Pi a busy drive is what
  // stalls playback, and the temperature is a number on the CPU card.
  const cards = { cpu: mkCard('CPU'), mem: mkCard('Memory'), disk: mkCard('Disk'), net: mkCard('Network out') };
  const health = h('div', { class: 'dash-health', role: 'status' });
  const storage = h('div', { class: 'panel' });
  const host = h('div', { class: 'dash-host' });
  // Element.append would print null and stringify arrays; flatten like h().
  const put = (el, ...kids) => clear(el).append(...kids.flat(Infinity).filter((k) => k != null && k !== false));
  const card = (c, value, sub, ...rest) => {
    setText(c.v, value);
    if (typeof sub === 'string') setText(c.sub, sub); else put(c.sub, sub);
    put(c.extra, rest);
  };
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
    // One line on whether the server is coping, in words.
    const strains = r.strains || [];
    health.className = `dash-health${strains.length ? ' warn' : ''}`;
    setText(health, strains.length ? strains.map((x) => x.text).join(' ') : 'Running normally');
    card(cards.cpu, `${n.cpu.toFixed(0)}%`,
      [[`${s.numCpu} cores`, s.freqMhz ? `${Math.round(s.freqMhz)} MHz` : '', n.temp ? `${n.temp.toFixed(0)}°C` : ''].filter(Boolean).join(' · '),
        flags ? h('span', { class: 'warn' }, ` · ${flags}`) : ''],
      h('div', { class: 'cores' }, (s.cores || []).map((c, i) => h('div', { class: 'core', title: `Core ${i}: ${c.toFixed(0)}%` }, h('i', { style: { height: `${c}%` } }), h('span', null, `${c.toFixed(0)}`)))));
    card(cards.mem, `${memPct.toFixed(0)}%`,
      `${fmtBytes(s.memUsed)} of ${fmtBytes(s.memTotal)}${s.swapTotal ? ` · swap ${fmtBytes(s.swapUsed)}` : ''}`,
      h('div', { class: 'small dim' }, `lex uses ${fmtBytes(s.procRss)} (Go heap ${fmtBytes(s.goHeap)}) · ${s.procCpu.toFixed(1)}% CPU`));
    // The busiest storage drive when the kernel says (Linux), else I/O wait:
    // time the CPU sat idle waiting for a disk.
    const busy = Object.entries(r.diskBusy || {}).sort((a, b) => b[1] - a[1])[0];
    card(cards.disk, `${(n.iowait || 0).toFixed(0)}%`, `I/O wait · read ${fmtBytes(n.dr)}/s · write ${fmtBytes(n.dw)}/s`,
      busy ? h('div', { class: 'small dim ellipsis', title: busy[0] }, `Busiest: ${busy[0]} · ${busy[1].toFixed(0)}% busy`) : null);
    card(cards.net, bps(n.tx), `in ${bps(n.rx)} · streaming ${bps(n.stream)}`);
    const times = hist.map((p) => p.t);
    if (times.length > 1) {
      const pct = (v) => `${v.toFixed(0)}%`;
      lineChart(cards.cpu.chart, { title: 'CPU over the last hour', times, height: 96, max: 100, ticks: 2, fmt: pct, series: [{ name: 'Host', values: hist.map((p) => p.cpu) }, { name: 'lex', values: hist.map((p) => p.pcpu / Math.max(1, s.numCpu)) }] });
      lineChart(cards.mem.chart, { title: 'Memory over the last hour', times, height: 96, max: 100, ticks: 2, fmt: pct, series: [{ name: 'Used', values: hist.map((p) => p.mem) }] });
      lineChart(cards.disk.chart, { title: 'I/O wait over the last hour', times, height: 96, max: 100, ticks: 2, fmt: pct, series: [{ name: 'I/O wait', values: hist.map((p) => p.iowait || 0) }] });
      // In bits per second so the gridlines land on round rates; an idle
      // server still gets a 0–1.5 Mbps scale.
      lineChart(cards.net.chart, { title: 'Network out over the last hour', times, height: 96, floor: 1.5e6, fmt: fmtBitrate, axisFmt: axisBitrate, series: [{ name: 'Out', values: hist.map((p) => p.tx * 8) }, { name: 'Streaming', values: hist.map((p) => p.stream * 8) }] });
    }
    // Storage: disks, SSD cache and current disk activity in one place. A
    // long path wraps its note underneath instead of widening the page.
    const row = (name, used, total, hot, note) => h('div', { class: 'srow' },
      h('div', { class: 'row wrap small' }, name, h('span', { class: 'muted' }, note || `${fmtBytes(Math.max(0, total - used))} free of ${fmtBytes(total)}`)),
      meter(total ? (used / total) * 100 : 0, hot));
    const c = r.cache;
    const pathSpan = (text, mono) => h('span', { class: `grow ellipsis${mono ? ' mono' : ''}`, title: text, style: { minWidth: 0, flex: '1 1 140px' } }, text);
    put(storage,
      h('h3', { style: { margin: '0 0 10px' } }, 'Storage'),
      (s.disks || []).map((d) => row(pathSpan(d.path, true), d.used, d.total, 92)),
      c?.enabled ? row(pathSpan('SSD cache'), c.usedBytes, c.maxBytes, 97,
        `${fmtBytes(c.usedBytes)} of ${fmtBytes(c.maxBytes)} · ${c.files} files${c.current ? ` · copying ${Math.round((c.current.done / c.current.size) * 100)}%` : ''}`) : null);
    host.textContent = [s.model || s.os, s.arch, `up ${fmtUptime(s.uptime)}`, `load ${s.load.map((x) => x.toFixed(2)).join(' ')}`,
      `${r.remuxJobs} remux · ${r.transcodeJobs} transcode jobs`].filter(Boolean).join('  ·  ');
  };

  const cardsById = new Map();
  // A stopped card goes away; focus moves to the list heading, not <body>.
  const refreshAfterStop = async () => {
    await renderSessions().catch(() => {});
    if (!document.activeElement || document.activeElement === document.body) nowTitle.focus({ preventScroll: true });
  };
  const nothing = h('div', { class: 'panel dim' }, 'Nothing is playing right now.');
  const renderSessions = async () => {
    const list = await api('/api/admin/stats/sessions');
    if (!ctx.isCurrent()) return;
    nowTitle.textContent = list.length ? `Now playing · ${list.length}` : 'Now playing';
    list.sort((a, b) => b.startedAt - a.startedAt || (a.id < b.id ? -1 : 1));
    const keys = new Set();
    const want = list.map((s) => {
      const key = `${s.id}:${s.itemId}`;
      keys.add(key);
      let card = cardsById.get(key);
      if (!card) cardsById.set(key, (card = sessionCard(s, refreshAfterStop)));
      card.update(s);
      return card.el;
    });
    for (const k of [...cardsById.keys()]) if (!keys.has(k)) cardsById.delete(k);
    if (!want.length) want.push(nothing);
    // Remove and insert only what changed: moving a card would drop focus
    // inside it.
    for (const el of [...sessionsBox.children]) if (!want.includes(el)) el.remove();
    want.forEach((el, i) => { if (sessionsBox.children[i] !== el) sessionsBox.insertBefore(el, sessionsBox.children[i] || null); });
  };

  // The first load failing leaves labelled placeholders, not empty boxes.
  const unavailable = () => {
    for (const k of ['cpu', 'mem', 'disk', 'net']) card(cards[k], '—', 'Unavailable');
    put(storage, h('h3', { style: { margin: 0 } }, 'Storage'), h('div', { class: 'dim small', style: { marginTop: '10px' } }, 'Unavailable'));
  };
  const [sysOk, sessOk] = await Promise.all([renderSystem(true).then(() => true, () => false), renderSessions().then(() => true, () => false)]);
  if (!sysOk) unavailable();
  if (!sessOk) put(sessionsBox, h('div', { class: 'panel dim' }, 'Unavailable'));
  const live = h('div', { class: 'dash-live' },
    h('section', null, h('div', { class: 'dash-head' }, nowTitle), sessionsBox),
    h('section', null, h('div', { class: 'dash-head' }, h('h2', { class: 'section-title' }, 'Server'), h('span', { class: 'muted small' }, 'last hour'), health),
      h('div', { class: 'dcards' }, cards.cpu.el, cards.mem.el, cards.disk.el, cards.net.el)),
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

// sessionCard builds one Now playing card; update(s) refreshes it in place.
/** @param {Session} s0 */
function sessionCard(s0, refresh) {
  let s = s0;
  const href = `#/item/${s.itemId}`;
  const title = h('a', { href });
  const sub = h('div', { class: 'dim small ellipsis' });
  const method = h('span');
  const status = h('span', { class: 'muted nowrap' });
  const bar = meter(0, 101);
  const reasons = h('div', { class: 'small muted', style: { marginTop: '6px' } });
  /** @type {[string, HTMLElement][]} */
  const health = ['Buffer', 'Delivery', 'Stalls', 'Read from'].map((k) => [k, h('b')]);
  const facts = ['Video', 'Audio', 'Bitrate', 'Client', 'Picture', 'ffmpeg'].map((k) => {
    const dt = h('dt', null, k), dd = h('dd');
    return { k, dt, dd };
  });
  const summary = h('summary', null, 'Details');
  const stop = h('button', { class: 'btn sm danger', onclick: async (e) => {
    const btn = e.currentTarget;
    const { userName, title: t, id } = s;
    if (!(await confirmDialog(`Stop ${userName}’s stream of ${t}? Their player will show that playback was stopped.`, 'Stop', true, 'Stop stream?'))) return;
    await run(btn, () => api(`/api/admin/sessions/${id}`, { method: 'DELETE' }).catch((err) => {
      throw err.status === 404 ? new Error('That stream had already ended') : err;
    }), `Stopped ${userName}’s stream`);
    refresh();
  } }, 'Stop');
  const el = h('div', { class: 'session' },
    // The title link names the item; the poster is a duplicate pointer target.
    h('a', { class: 'poster', href, tabindex: '-1', 'aria-hidden': 'true' }, h('img', { src: img({ id: s.itemId }, 'poster', 160), alt: '' })),
    h('div', { style: { minWidth: 0 } },
      h('div', { class: 'row session-head' },
        h('div', { class: 'grow', style: { minWidth: 0 } }, h('h3', { class: 'ellipsis' }, title), sub),
        method, stop),
      h('div', { class: 'row small', style: { marginTop: '10px' } }, status, h('div', { class: 'grow' }, bar)),
      reasons,
      h('div', { class: 'health' }, health.map(([k, b]) => h('div', null, h('span', null, k), b))),
      h('details', null, summary, h('dl', { class: 'kv' }, facts.map((f) => [f.dt, f.dd])))));
  const update = (next) => {
    s = next;
    /** @type {Partial<ClientStats>} */
    const c = s.clientStats || {};
    const j = s.job;
    setText(title, s.title);
    setText(sub, [s.subtitle, s.live ? 'Live TV' : '', s.userName, s.client, s.remote ? 'remote' : 'local'].filter(Boolean).join(' · '));
    method.className = `method ${s.method}`;
    method.title = s.reasons?.map(reasonLabel).join('; ') || '';
    setText(method, METHOD_LABEL[s.method] || s.method);
    stop.setAttribute('aria-label', `Stop ${s.userName}’s stream of ${s.title}`);
    summary.setAttribute('aria-label', `Details for ${s.title}`);
    setText(status, `${s.paused ? 'Paused' : 'Playing'} · ${fmtTime(s.position)} / ${fmtTime(s.duration)}`);
    /** @type {HTMLElement} */ (bar.firstChild).style.width = `${s.duration ? Math.min(100, (s.position / s.duration) * 100) : 0}%`;
    reasons.hidden = !s.reasons?.length;
    setText(reasons, s.reasons?.length ? `Why ${s.method === 'transcode' ? 'transcoding' : 'converting'}: ${s.reasons.map(reasonLabel).join(' · ')}` : '');
    const buf = c.bufferAhead || 0;
    [
      [`${buf.toFixed(0)}s`, buf < 3 ? 'bad' : buf < 10 ? 'warn' : ''],
      [bps(s.rate), ''],
      [c.bufferEvents ? `${c.bufferEvents} · ${(c.bufferSeconds || 0).toFixed(0)}s` : 'none', c.bufferEvents ? 'warn' : ''],
      [s.cached ? 'SSD cache' : 'Library disk', ''],
    ].forEach(([v, cls], i) => { const b = health[i][1]; setText(b, v); b.className = cls; });
    const vals = {
      Video: `${s.videoIn || '?'} → ${s.videoOut || ''}`,
      Audio: `${s.audioIn || '?'} → ${s.audioOut || ''}`,
      Bitrate: `${fmtBitrate((s.srcBitrate || 0) * 1000)} source${s.method === 'transcode' ? ` → ${fmtBitrate((s.outBitrate || 0) * 1000)}` : ''} · ${fmtBytes(s.bytes)} sent`,
      Client: `${s.client} · ${s.ip}${s.remote ? ' (remote)' : ' (local)'} · ${c.bandwidth ? fmtBitrate(c.bandwidth) : '—'} download`,
      Picture: `${c.resolution || '—'}${c.totalFrames ? ` · ${c.droppedFrames} of ${c.totalFrames} frames dropped` : ''}`,
      ffmpeg: j ? (j.exited ? (j.error ? `exited: ${j.error}` : 'finished') : `${j.throttled ? 'waiting (client buffer full)' : `${(j.speed || 0).toFixed(2)}x · ${Math.round(j.fps || 0)} fps`} · CPU ${Math.round(j.cpu || 0)}% · ${s.restarts} restart${s.restarts === 1 ? '' : 's'}`) : null,
    };
    for (const f of facts) {
      const v = vals[f.k];
      f.dt.hidden = f.dd.hidden = v == null;
      setText(f.dd, v ?? '');
    }
  };
  return { el, update };
}

const RANGES = [7, 30, 90, 365];

const dayKey = (dt) => `${dt.getFullYear()}-${String(dt.getMonth() + 1).padStart(2, '0')}-${String(dt.getDate()).padStart(2, '0')}`;

async function playbackTab(ctx) {
  const days = Math.max(1, Math.min(3650, Math.round(+ctx.query.get('days')) || 30));
  const [st, history] = await Promise.all([
    api(`/api/admin/stats/playback?days=${days}&tz=${-new Date().getTimezoneOffset()}`),
    historySection(ctx, days),
  ]);
  const range = h('div', { class: 'toolbar' },
    h('div', { class: 'seg range-group', role: 'group', 'aria-label': 'Range' },
      RANGES.map((d) => h('button', {
        class: 'seg-btn', type: 'button', 'aria-pressed': String(d === days), dataset: { focusKey: `range-${d}` },
        onclick: () => { if (d === days) return; focusAfterRoute(`range-${d}`); location.hash = `#/dashboard/playback?days=${d}`; },
      }, `${d} days`))),
    h('div', { class: 'spacer' }),
    st.plays ? h('button', { class: 'btn sm ghost', onclick: () => {
      const sec = document.getElementById('history');
      sec?.scrollIntoView({ behavior: motionOK() ? 'smooth' : 'auto' });
      sec?.querySelector('h2')?.focus({ preventScroll: true });
    } }, 'Jump to history') : null);
  // Paging through history keeps you at the table (Back restores its own
  // scroll). Pager links always carry offset, so Newer to the first page too.
  if (ctx.query.has('offset') && !ctx.restore) requestAnimationFrame(() => document.getElementById('history')?.scrollIntoView());
  if (!st.plays) {
    return h('div', null, range,
      emptyState({ level: 'h2', title: `No plays in the last ${days} days`, text: 'Plays are recorded when a session ends.' }),
      history);
  }

  // Fill missing days so the chart has a continuous axis; long ranges are
  // summed into weeks (starting Monday) so the bars stay readable.
  const byDay = Object.fromEntries((st.days || []).map((d) => [d.day, d]));
  const weekly = days > 120;
  const labels = [], full = [], hours = [], plays = [];
  let week = '';
  for (let i = days - 1; i >= 0; i--) {
    const dt = new Date();
    dt.setHours(12, 0, 0, 0);
    dt.setDate(dt.getDate() - i);
    const d = byDay[dayKey(dt)];
    if (weekly) {
      const mon = new Date(dt);
      mon.setDate(dt.getDate() - ((dt.getDay() + 6) % 7));
      if (dayKey(mon) !== week) {
        week = dayKey(mon);
        labels.push(mon.toLocaleDateString(undefined, { month: 'short', day: 'numeric' }));
        full.push(`Week of ${mon.toLocaleDateString(undefined, { dateStyle: 'medium' })}`);
        hours.push(0); plays.push(0);
      }
      hours[hours.length - 1] += d?.hours || 0;
      plays[plays.length - 1] += d?.plays || 0;
    } else {
      labels.push(dt.toLocaleDateString(undefined, { month: 'short', day: 'numeric' }));
      full.push(dt.toLocaleDateString(undefined, { dateStyle: 'medium' }));
      hours.push(d?.hours || 0);
      plays.push(d?.plays || 0);
    }
  }
  const hoursTitle = weekly ? 'Hours watched per week' : 'Hours watched per day';
  const hoursBox = h('div'), hourBox = h('div');
  const hod = Array.from({ length: 24 }, (_, i) => (st.hoursOfDay || []).find((b) => +b.key === i)?.count || 0);
  const hourName = (i) => new Date(2000, 0, 1, i).toLocaleTimeString([], { hour: 'numeric' });
  const fmtH = (v) => (v > 0 && Math.round(v * 60) < 60 ? `${Math.round(v * 60) || '<1'}m` : `${+v.toFixed(1)}h`);
  const page = h('div', null, range,
    h('div', { class: 'stats-grid' },
      stat('Plays', st.plays.toLocaleString(), `${st.uniqueItems} titles · ${Math.round((st.remotePlays / st.plays) * 100)}% remote`),
      stat('Watch time', fmtHours(st.hours), `${fmtHours(st.hours / Math.max(1, days))} a day`),
      stat('Data served', fmtBytes(st.bytes), `${fmtBytes(st.bytes / st.plays)} per play`),
      stat('Buffering', st.hours ? `${((st.bufferSeconds / (st.hours * 3600)) * 100).toFixed(2)}%` : '—', `of watch time · ${st.bufferEvents} stalls, ${st.bufferSeconds.toFixed(0)}s`)),
    h('div', { class: 'dash-2' },
      h('div', { class: 'panel' }, h('h2', { class: 'panel-title' }, hoursTitle), hoursBox,
        dataTable([weekly ? 'Week' : 'Day', 'Hours', 'Plays'], full.map((l, i) => (plays[i] ? [l, fmtHours(hours[i]), plays[i]] : null)), weekly ? 'Weeks without plays are left out.' : 'Days without plays are left out.')),
      h('div', { class: 'panel' }, h('h2', { class: 'panel-title' }, 'Plays by hour of day'), hourBox,
        dataTable(['Hour', 'Plays'], hod.map((v, i) => (v ? [hourName(i), v] : null)), 'Hours without plays are left out.'))),
    h('div', { class: 'dash-3' },
      h('div', { class: 'panel' }, h('h2', { class: 'panel-title' }, 'How streams played'), methodsTable(st),
        st.reasons?.length ? [h('h3', { class: 'panel-sub' }, 'Why streams were converted'),
          h('p', { class: 'small dim', style: { margin: '-4px 0 8px' } }, 'A play can count toward several reasons.'),
          barList(st.reasons.map((m) => ({ label: reasonLabel(m.key), value: m.count })), (v) => `${v}`, null, { wrap: true })] : null),
      h('div', { class: 'panel' }, h('h2', { class: 'panel-title' }, 'Most watched'), barList((st.topItems || []).map((m) => ({ label: m.key, value: m.count })), (v) => `${v} play${v === 1 ? '' : 's'}`)),
      h('div', { class: 'panel' }, h('h2', { class: 'panel-title' }, 'Who’s watching'), barList((st.users || []).map((m) => ({ label: m.key, value: m.value })), (v) => fmtHours(v)),
        h('h3', { class: 'panel-sub' }, 'Clients'), barList((st.clients || []).map((m) => ({ label: m.key || 'unknown', value: m.count })), (v) => `${v} play${v === 1 ? '' : 's'}`))),
    history);
  requestAnimationFrame(() => {
    const day = (i) => new Date(Date.now() - i * 86400000).toLocaleDateString(undefined, { dateStyle: 'medium' });
    columnChart(hoursBox, { title: `${hoursTitle}, ${day(days - 1)} to ${day(0)}`, labels, fullLabels: full, values: hours, fmt: fmtH, axisFmt: axisHours, tipLabel: 'watched', floor: 0.25 });
    columnChart(hourBox, { title: 'Plays by hour of day', labels: hod.map((_, i) => `${i}h`), fullLabels: hod.map((_, i) => hourName(i)), values: hod, fmt: (v) => `${Math.round(v)}`, tipLabel: 'plays' });
  });
  return page;
}

// dataTable puts a chart's values in a collapsed table for keyboard, screen
// reader and touch users. Rows that are null are left out.
function dataTable(head, rows, note) {
  rows = rows.filter(Boolean);
  return h('details', { class: 'chart-data' }, h('summary', null, 'Show data'),
    h('p', { class: 'small dim' }, note),
    h('table', { class: 'tbl' },
      h('tr', null, head.map((t) => h('th', { scope: 'col' }, t))),
      rows.map((r) => h('tr', null, r.map((v) => h('td', null, `${v}`))))));
}

// A few seconds read "<1 min", not "0 min"; nothing at all reads "0 h".
const fmtHours = (v) => {
  const min = Math.round(v * 60);
  return !(v > 0) ? '0 h' : min < 1 ? '<1 min' : min < 60 ? `${min} min` : v < 100 ? `${v.toFixed(1)} h` : `${Math.round(v).toLocaleString()} h`;
};

// Plays, share and stall time per playback method, in one table.
/** @param {PlaybackStats} st */
function methodsTable(st) {
  const plays = Object.fromEntries((st.methods || []).map((m) => [m.key, m.count]));
  const stall = Object.fromEntries((st.bufferByMethod || []).map((m) => [m.key, m.value]));
  const total = Object.values(plays).reduce((a, b) => a + b, 0);
  const rows = ['direct', 'remux', 'transcode'].filter((k) => plays[k]);
  if (!rows.length) return h('div', { class: 'dim small' }, 'No data yet');
  return h('table', { class: 'tbl mtable' },
    h('tr', null, h('th', { scope: 'col' }, h('span', { class: 'sr-only' }, 'Method')), h('th', { scope: 'col' }, 'Plays'), h('th', { scope: 'col' }, 'Stalled')),
    rows.map((k) => h('tr', null,
      h('td', null, h('i', { class: `dot ${k}`, 'aria-hidden': 'true' }), METHOD_LABEL[k] || k),
      h('td', null, `${plays[k]}`, h('span', { class: 'dim' }, ` · ${Math.round((plays[k] / total) * 100)}%`)),
      h('td', null, stall[k] != null ? `${stall[k].toFixed(2)}%` : '—'))));
}

async function libraryTab() {
  const st = await api('/api/admin/stats/library');
  const bucketList = (dim, b, fmtKey = (k) => k) => barList([...(b || [])].sort((x, y) => y.value - x.value)
    .map((x) => ({ label: fmtKey(x.key), note: `${x.count.toLocaleString()} file${x.count === 1 ? '' : 's'}`, value: x.value, onClick: () => libraryTitles(dim, x.key, `${DIM_LABEL[dim]}: ${fmtKey(x.key)}`) })), (v) => fmtBytes(v));
  const panel = (title, list) => h('div', { class: 'panel' }, h('h2', { class: 'panel-title' }, title), list);
  return h('div', null,
    h('div', { class: 'stats-grid' },
      stat('Movies', st.movies.toLocaleString()),
      stat('Shows', st.shows.toLocaleString(), `${st.seasons} seasons · ${st.episodes.toLocaleString()} episodes`),
      stat('Storage', fmtBytes(st.totalBytes), [`${st.files.toLocaleString()} files · ${fmtHours(st.totalDuration / 3600)} of video${st.unprobed ? ` · ${st.unprobed} awaiting analysis` : ''}`,
        st.probeErrors ? [' · ', h('button', { class: 'link-btn', type: 'button', 'aria-haspopup': 'dialog', onclick: () => unreadableModal() }, `${st.probeErrors} unreadable`)] : null]),
      stat('Metadata', `${st.metaMatched} matched`, [st.metaMissing ? h('button', { class: 'link-btn', type: 'button', 'aria-haspopup': 'dialog', onclick: () => unmatchedModal() }, `${st.metaMissing} not found`) : '0 not found', ` · ${st.metaPending} pending`])),
    st.files ? compatPanel() : null,
    h('div', { class: 'dash-3' },
      panel('Libraries', bucketList('library', st.libraries)),
      panel('Resolution', bucketList('resolution', st.resolutions)),
      panel('HDR', bucketList('hdr', st.hdr)),
      panel('Video codecs', bucketList('video', st.videoCodecs, (k) => k.replace(/^\S+/, (c) => c.toUpperCase()))),
      panel('Audio (any track)', bucketList('audio', st.audioCodecs, (k) => k.toUpperCase())),
      panel('Containers', bucketList('container', st.containers, (k) => k.toUpperCase()))));
}

const DIM_LABEL = { library: 'Library', resolution: 'Resolution', hdr: 'HDR', video: 'Video codec', audio: 'Audio track', container: 'Container' };

// libraryTitles shows the movies and shows in one Library-stats category.
function libraryTitles(dim, key, label) {
  return titlesModal(label, () => api(`/api/admin/stats/library/titles?dim=${encodeURIComponent(dim)}&key=${encodeURIComponent(key)}`));
}

const META_STATUS = { 2: 'Not found', 3: 'Local artwork only' };

// unmatchedModal lists the movies and shows without a metadata match, each
// with the folder it was parsed from (often why it missed) and Fix match.
export async function unmatchedModal() {
  const list = h('div', { class: 'title-list' }, spinner());
  const count = h('div', { class: 'small muted' });
  const m = modal({ title: 'Unmatched titles', wide: true, body: [
    h('p', { class: 'help', style: { marginTop: 0 } }, 'No metadata provider matched these. A folder name that isn’t “Title (Year)” is the usual cause: rename it, or pick the right title with Fix match.'), count, list] });
  const counted = (n) => { count.textContent = `${n} title${n === 1 ? '' : 's'}`; };
  try {
    const rows = await api('/api/admin/stats/library/unmatched');
    counted(rows.length);
    clear(list);
    if (!rows.length) list.append(h('div', { class: 'dim' }, 'Every title has a match.'));
    let left = rows.length;
    for (const r of rows) {
      const row = h('div', { class: 'title-row' },
        h('img', { src: img({ id: r.itemId }, 'poster', 80), alt: '', loading: 'lazy' }),
        h('div', { class: 'grow', style: { minWidth: 0 } },
          h('a', { class: 'ellipsis plain-link', href: `#/item/${r.itemId}`, onclick: () => m.close() }, r.title, r.year ? h('span', { class: 'dim' }, ` (${r.year})`) : null),
          h('div', { class: 'small dim ellipsis', title: r.path }, `${r.kind === 'show' ? 'Show' : 'Movie'} · ${r.library} · `, h('span', { class: 'mono' }, r.path)),
          h('div', { class: 'small muted' }, META_STATUS[r.metaStatus] || '')),
        h('button', { class: 'btn sm', type: 'button', onclick: () => fixMatch({ id: r.itemId, kind: r.kind, title: r.title, year: r.year }, { onDone: () => {
          row.remove();
          counted(--left);
          if (!left) list.append(h('div', { class: 'dim' }, 'Every title has a match.'));
        } }) }, 'Fix match'));
      list.append(row);
    }
  } catch (e) {
    clear(list).append(h('div', { class: 'bad' }, e.message));
  }
  return m;
}

// unreadableModal lists the files ffprobe couldn't read with the error, each
// with Probe again (failed files are otherwise retried with a growing delay).
async function unreadableModal() {
  const list = h('div', { class: 'title-list' }, spinner());
  const count = h('div', { class: 'small muted' });
  modal({ title: 'Unreadable files', wide: true, body: [
    h('p', { class: 'help', style: { marginTop: 0 } }, 'ffprobe couldn’t read these files, so they have no duration or codecs and may not play. A damaged or still-copying file, or a share that timed out, are the usual causes. lex retries them by itself with a growing delay.'), count, list] });
  const counted = (n) => { count.textContent = `${n} file${n === 1 ? '' : 's'}`; };
  try {
    const rows = await api('/api/admin/stats/library/unreadable');
    counted(rows.length);
    clear(list);
    if (!rows.length) list.append(h('div', { class: 'dim' }, 'Every file is readable.'));
    let left = rows.length;
    for (const r of rows) {
      const title = r.show ? `${r.show} · S${r.season} E${r.episode}` : r.title;
      const err = h('div', { class: 'small bad', style: { overflowWrap: 'anywhere' } }, r.error);
      const when = h('div', { class: 'small dim' }, `Tried ${r.attempts} time${r.attempts === 1 ? '' : 's'}, last ${timeAgo(r.probedAt)} · ${fmtBytes(r.size)}`);
      const btn = h('button', { class: 'btn sm', type: 'button' }, 'Probe again');
      const row = h('div', { class: 'title-row', style: { alignItems: 'flex-start' } },
        h('div', { class: 'grow', style: { minWidth: 0 } },
          h('a', { class: 'plain-link', href: `#/item/${r.itemId}` }, title),
          h('div', { class: 'small mono dim', style: { overflowWrap: 'anywhere' } }, r.path), err, when),
        btn);
      btn.onclick = async () => {
        let res = null;
        if (!(await run(btn, async () => { res = await api(`/api/admin/files/${r.fileId}/probe`, { method: 'POST' }); }, undefined, { busyLabel: 'Probing…' }))) return;
        if (res.ok) {
          toast('The file is readable now', 'ok');
          row.remove();
          counted(--left);
          if (!left) list.append(h('div', { class: 'dim' }, 'Every file is readable.'));
          return;
        }
        err.textContent = res.error;
        when.textContent = `Tried again just now · ${fmtBytes(r.size)}`;
        toast('Still unreadable', 'error');
      };
      list.append(row);
    }
  } catch (e) {
    clear(list).append(h('div', { class: 'bad' }, e.message));
  }
}

// titlesModal lists the movies and shows load() resolves to (LibraryTitle).
export async function titlesModal(label, load, intro = null) {
  const list = h('div', { class: 'title-list' }, spinner());
  const m = modal({ title: label, body: [intro, list], wide: true });
  try {
    const rows = await load();
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

// Every play, newest first, under the playback statistics. It covers all
// that is kept (not the selected range). Nothing at all renders nothing: the
// statistics' empty state covers it.
async function historySection(ctx, days) {
  const offset = Math.max(0, +ctx.query.get('offset') || 0);
  const [rows, cfg] = await Promise.all([api(`/api/admin/history?limit=100&offset=${offset}`), api('/api/admin/config').catch(() => null)]);
  const keep = cfg?.historyRetentionDays;
  if (!rows.length && !offset) return null;
  const page = (o) => `#/dashboard/playback?days=${days}&offset=${o}`;
  const pager = (label, o) => h('a', { class: 'btn sm', href: page(o), onclick: (e) => { if (!e.ctrlKey && !e.metaKey && !e.shiftKey) focusAfterRoute('history'); } }, label);
  const reasons = (r) => r.reasons ? r.reasons.split('; ').map(reasonLabel).join('; ') : '';
  return h('section', { id: 'history', style: { marginTop: '28px' } },
    h('div', { class: 'toolbar' }, h('h2', { class: 'section-title', tabindex: '-1', dataset: { focusKey: 'history' } }, keep > 0 ? `History · last ${keep} days` : 'History · all time'),
      h('a', { class: 'muted small', href: '#/settings/server' }, 'Retention'),
      rows.length ? h('span', { class: 'muted small' }, `${offset + 1}–${offset + rows.length}`) : null, h('div', { class: 'spacer' }),
      offset > 0 ? pager('Newer', Math.max(0, offset - 100)) : null,
      rows.length === 100 ? pager('Older', offset + 100) : null,
      h('button', { class: 'btn sm danger', onclick: async (e) => {
        const btn = e.currentTarget;
        if (!(await confirmDialog('Delete all playback history? Every total, chart and list on this tab is calculated from it and will reset to zero. Watch progress and watched status are kept.', 'Delete history', true, 'Delete playback history?'))) return;
        if (!(await run(btn, () => api('/api/admin/history', { method: 'DELETE' }), 'Playback history cleared'))) return;
        if (offset) location.hash = page(0); else refreshSoft();
      } }, 'Clear history')),
    rows.length ? h('div', { class: 'panel table-wrap' }, h('table', { class: 'tbl history' },
      h('tr', null, ['When', 'User', 'Title', 'Method', 'Watched', 'Data', 'Stalls', 'Output', 'Client'].map((t) => h('th', { scope: 'col' }, t))),
      rows.map((r) => h('tr', null,
        h('td', { class: 'nowrap h-when' }, fmtDate(r.startedAt)),
        h('td', { class: 'h-user' }, r.userName),
        h('td', { class: 'h-title' }, h('a', { href: `#/item/${r.itemId}` }, r.title), r.subtitle ? h('div', { class: 'dim small' }, r.subtitle) : null),
        h('td', { class: 'h-method', title: reasons(r) || null }, h('span', { class: `method ${r.method}` }, METHOD_LABEL[r.method] || r.method), r.reasons ? h('div', { class: 'dim small h-reasons' }, reasons(r)) : null),
        h('td', { class: 'nowrap h-num', 'data-label': 'Watched' }, r.watched < 60 ? `${Math.round(r.watched)}s` : fmtDuration(r.watched)),
        h('td', { class: 'nowrap h-num', 'data-label': 'Data' }, fmtBytes(r.bytes)),
        h('td', { class: 'nowrap h-num', 'data-label': 'Stalls' }, r.bufferEvents ? `${r.bufferEvents} (${r.bufferSeconds.toFixed(0)}s)` : '0'),
        h('td', { class: 'small h-output' }, [r.videoOut, r.audioOut].filter(Boolean).join(' / ')),
        h('td', { class: 'small h-client' }, `${r.client}`, h('div', { class: 'dim mono' }, `${r.ip}${r.remote ? ' · remote' : ''}`))))))
      : h('div', { class: 'empty' }, h('h3', null, 'No older plays'), h('p', null, pager('Back to the newest plays', 0))));
}
