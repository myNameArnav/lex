// Stats for nerds: the panel's model (statsModel), drawing it and the
// buffer graph, copying it, and turning it on and off.
//
// Player methods (installed on the class in player.js). State they use:
// this.statsEl and the panel's parts, this.bufHist (buffer history for the
// graph), this.plan / this.file / this.engine for what they report.

import { fxLabel } from '../audio-fx.js';
import { prefs } from '../prefs.js';
import { h, icons, METHOD_LABEL, fmtTime, reasonLabel, fmtBitrate, fmtBytes, channelName, langName, clear, toast } from '../ui.js';
import { secs, plural, copied } from './format.js';
import { diskLevel } from '../buffering.js';

export const stats = {
  toggleStats() {
    if (this.statsEl) { this.statsEl.remove(); this.statsEl = null; prefs.set('showStats', false); this.statsChanged(); return; }
    if (!this.plan) return;
    prefs.set('showStats', true);
    const stop = (on) => (e) => { e.stopPropagation(); on(e); };
    this.statsSum = h('span', { class: 'ps-sum' });
    this.statsCanvas = h('canvas');
    this.statsIssues = h('div', { class: 'ps-issues', role: 'status' });
    this.statsTiles = h('div', { class: 'ps-tiles' });
    this.statsMethod = h('div', { class: 'ps-method' });
    this.statsSecs = new Map();
    this.statsSecBox = h('div', { class: 'ps-secs' });
    this.statsBody = h('div', { class: 'ps-body' }, this.statsTiles, this.statsMethod, this.statsSecBox);
    const compact = !!prefs.get('statsCompact');
    const toggle = h('button', { class: 'ps-toggle', title: 'Collapse / expand', 'aria-expanded': String(!compact), onclick: stop(() => {
      const c = this.statsEl.classList.toggle('compact');
      toggle.setAttribute('aria-expanded', String(!c));
      prefs.set('statsCompact', c);
    }) }, this.statsSum);
    this.statsEl = h('div', { class: `p-stats${compact ? ' compact' : ''}`, onclick: (e) => e.stopPropagation() },
      h('div', { class: 'ps-head' }, toggle,
        h('button', { class: 'ps-btn', title: 'Copy stats as text', onclick: stop(() => this.copyStats()) }, 'Copy'),
        h('button', { class: 'ps-btn ps-close', title: 'Close (I)', 'aria-label': 'Close stats (I)', html: icons.close, onclick: stop(() => this.toggleStats()) })),
      this.statsIssues,
      h('div', { class: 'ps-graph' }, h('div', { class: 'ps-cap' }, 'Buffer · last 90s'), this.statsCanvas),
      this.statsBody);
    this.root.appendChild(this.statsEl);
    this.renderStats(this.clientStats());
    this.statsChanged();
    this.beat();
  },

  // The badge and an open Settings menu show whether the panel is up.
  statsChanged() {
    this.methodEl.setAttribute('aria-pressed', String(!!this.statsEl));
    if (this.menuName === 'settings' && !this.menuPage) this.renderMenu();
  },

  // Everything the stats panel shows, as data: rendered into the panel and
  // flattened to text by the Copy button. lvl is '' | 'warn' | 'bad'.
  statsModel(cs) {
    const p = this.plan, f = this.file, info = f?.info || {}, v = this.video;
    const vs = (info.streams || []).find((s) => s.type === 'video');
    const as = (info.streams || []).find((s) => s.index === p.audio);
    const es = this.engine ? this.engine.stats() : null;
    const j = this.lastJob;
    const t = v.currentTime, d = this.duration(), method = METHOD_LABEL[p.method];
    const sections = [];
    let rows;
    const sec = (title) => { rows = []; sections.push({ title, rows, sum: '' }); };
    const sum = (text) => { sections[sections.length - 1].sum = text; };
    const r = (k, val, sub = '', lvl = '', full = '') => rows.push({ k, v: val, sub, lvl, full });

    // Health. A short buffer is fine once the rest of the file is in it.
    const needMore = d - t - cs.bufferAhead > 1;
    const bufLvl = cs.buffering ? 'bad' : needMore && cs.bufferAhead < (es ? Math.min(30, es.target / 2) : 10) ? 'warn' : '';
    const dropPct = cs.totalFrames ? (cs.droppedFrames / cs.totalFrames) * 100 : 0;
    const dropLvl = cs.totalFrames < 100 ? '' : dropPct > 5 ? 'bad' : dropPct > 1 ? 'warn' : '';
    // ffmpeg reports progress only once a second and not at all while the
    // client's full buffer blocks it; what the browser holds was produced too.
    const done = j ? Math.max(j.outTime || 0, es ? es.ranges.reduce((m, [s, e]) => (s <= t + 1 ? Math.max(m, e) : m), 0) : 0) : 0;
    const jobLvl = !j ? '' : j.error ? 'bad' : !j.exited && !j.throttled && j.speed && j.speed < v.playbackRate && done - t < 30 ? 'warn' : '';
    // The file route measures how much of its time goes to disk reads. That
    // share is only a problem while playback runs short and reads lag.
    const fileRoute = p.method === 'direct' || es?.fileBytes != null;
    const diskLvl = fileRoute ? diskLevel({ diskWait: this.diskWait, needKbps: p.bitrate, bandwidth: this.arrivalRate(),
      rate: v.playbackRate || 1, struggling: cs.buffering || !!bufLvl }) : '';
    const hostLvl = this.host.length ? 'warn' : '';
    const lvls = [bufLvl, dropLvl, jobLvl, diskLvl, hostLvl];
    const lvl = lvls.includes('bad') ? 'bad' : lvls.includes('warn') ? 'warn' : '';
    const status = cs.buffering ? 'Buffering' : lvl === 'bad' ? 'Error' : lvl === 'warn' ? 'Degraded' : 'Healthy';
    const summary = `${status} · ${secs(cs.bufferAhead)} buffered · ${plural(cs.bufferEvents, 'stall')} · ${cs.droppedFrames} dropped`;

    sec('Playback');
    sum(`${method} · ${fmtTime(t)} / ${fmtTime(d)}`);
    r('Method', method, p.remote ? 'remote' : 'local');
    if (p.reasons?.length) { const why = p.reasons.map(reasonLabel).join('; '); r('Why', why, '', '', why); }
    r('Position', `${fmtTime(t)} / ${fmtTime(d)}`, v.playbackRate !== 1 ? `${v.playbackRate}x` : '');
    for (const [seg, name] of [[this.intro(), 'Intro'], [this.credits(), 'Credits']]) {
      if (seg) r(name, `${fmtTime(seg.start)}–${fmtTime(seg.end)}`, seg.source);
    }
    r('Container', `${(f.name || '').split('.').pop()} → ${p.hls ? 'HLS' : p.method === 'direct' ? 'original file' : 'fragmented MP4'}`,
      p.hls ? p.mime : p.method === 'direct' ? 'range requests' : p.clientRemux ? 'remuxed in the browser · MSE' : 'MSE');

    sec('Buffer & network');
    if (es) {
      const why = es.quotaLimited ? 'browser memory limit' : prefs.get('bufferAhead') ? 'your setting' : 'auto';
      r('Ahead', `${secs(cs.bufferAhead)} / ${secs(es.target)}`, why, bufLvl);
      const cur = es.ranges.find(([s, e]) => s <= t + 0.5 && e >= t);
      r('Behind', `${secs(cur ? t - cur[0] : 0)} / ${secs(prefs.get('backBuffer'))}`);
      const idle = es.throttled || !es.fetching;
      r('Download', es.throttled ? 'paused' : es.fetching ? fmtBitrate(es.bandwidth) : 'idle',
        `${es.throttled ? 'buffer full · ' : ''}${idle && es.bandwidth ? `last ${fmtBitrate(es.bandwidth)} · ` : ''}${fmtBytes(es.bytes)} total`);
      const ranges = es.ranges.map(([s, e]) => `${fmtTime(s)}–${fmtTime(e)}`).join(', ') || '—';
      r('Ranges', ranges, '', '', ranges);
      sum(`${secs(cs.bufferAhead)} ahead · ${es.throttled ? 'download paused' : es.fetching ? fmtBitrate(es.bandwidth) : 'idle'}`);
    } else {
      sum(`${secs(cs.bufferAhead)} ahead`);
      r('Ahead', secs(cs.bufferAhead), 'browser managed', bufLvl);
    }
    if (es?.fileBytes != null) r('File read', fmtBytes(es.fileBytes), `range requests${es.fileRate ? ` at ${fmtBitrate(es.fileRate * 8)}` : ''} · remuxer memory ${fmtBytes(es.remuxHeap)}`);
    r('Server rate', fmtBitrate(this.serverRate * 8));
    const cause = cs.bufferEvents || cs.buffering ? this.bufferCause() : null;
    if (cause) r('Likely cause', ({ network: 'data arrives too slowly', disk: 'server disk too slow', cpu: 'server CPU maxed out', memory: 'server short of memory', throttled: 'server throttled' })[cause.kind] || 'server converts too slowly', cause.hint, 'warn', `${cause.text} ${cause.hint}`.trim());
    r('Stalls', String(cs.bufferEvents), `${cs.bufferSeconds.toFixed(1)}s total${cs.buffering ? ' · buffering now' : ''}`, cs.buffering ? 'bad' : cs.bufferEvents ? 'warn' : '');
    r('Read from', this.cached ? 'SSD cache' : 'library disk');
    if (fileRoute) r('Disk wait', `${Math.round(this.diskWait * 100)}%`, `of serving time spent reading the file${!diskLvl && this.diskWait >= 0.3 ? ' · reads keep up' : ''}`, diskLvl);
    const ld = this.load;
    const loadTxt = ld ? `CPU ${Math.round(ld.cpu)}% · I/O wait ${Math.round(ld.iowait)}% · ${fmtBytes(ld.memAvail)} free${ld.swapIO >= 1e5 ? ` · swapping ${fmtBytes(ld.swapIO)}/s` : ''}` : '';
    if (this.host.length || ld) r('Server', this.host.length ? this.host.map((x) => x.text).join(' ') : 'OK', loadTxt, hostLvl, this.host.map((x) => x.text).join(' '));
    r('Source bitrate', fmtBitrate(info.bitrate || 0));
    if (p.method === 'transcode') r('Target bitrate', fmtBitrate(p.bitrate * 1000));
    if (p.limitKbps) r('Bitrate limit', fmtBitrate(p.limitKbps * 1000));

    sec('Video');
    if (vs) sum(`${vs.codec.toUpperCase()} ${vs.height}p → ${p.videoOut && / \(copy\)$/.test(p.videoOut) ? 'copied' : (p.videoOut || '?')}`);
    if (vs) r('Source', `${vs.codec.toUpperCase()} ${vs.profile || ''} ${vs.width}x${vs.height}`.replace(/\s+/g, ' '),
      `${vs.frameRate ? vs.frameRate.toFixed(3).replace(/\.?0+$/, '') + 'fps · ' : ''}${vs.bitDepth || 8}-bit ${vs.hdr || 'SDR'}`);
    r('Output', copied(p.videoOut));
    if (p.method !== 'direct' && p.mime) r('MIME', p.mime, '', '', p.mime);
    if (v.videoWidth) {
      // object-fit: contain, so the picture is scaled by the tighter axis.
      const scale = Math.min(v.clientWidth / v.videoWidth, v.clientHeight / v.videoHeight) * window.devicePixelRatio;
      r('Decoded', `${v.videoWidth}x${v.videoHeight}`, `${Math.round(v.videoWidth * scale)}x${Math.round(v.videoHeight * scale)} on screen (${scale.toFixed(1)}x)`);
    }
    r('Dropped', plural(cs.droppedFrames, 'frame'), `${cs.totalFrames} decoded · ${dropPct.toFixed(2)}%`, dropLvl);

    sec('Audio');
    if (as) sum(`${as.codec.toUpperCase()} ${channelName(as.channels)} → ${p.audioOut && / \(copy\)$/.test(p.audioOut) ? 'copied' : (p.audioOut || 'none')}`);
    if (as) r('Source', `${as.codec.toUpperCase()} ${channelName(as.channels)}`, [as.sampleRate ? as.sampleRate / 1000 + 'kHz' : '', langName(as.language)].filter(Boolean).join(' · '));
    r('Output', copied(p.audioOut) || 'none');
    if (this.fx.active) r('Effects', fxLabel(this.fx.state), 'in the browser');

    if (j) {
      sec('Server (ffmpeg)');
      sum(j.exited ? (j.error ? 'failed' : 'finished') : j.throttled ? 'waiting for the player' : `${(j.speed || 0).toFixed(1)}x · ${secs(Math.max(0, done - t))} ready ahead`);
      r('State', j.exited ? (j.error ? `exited: ${j.error}` : 'finished') : j.throttled ? 'throttled' : 'running', j.throttled ? 'waiting for client' : '', j.error ? 'bad' : '', j.error || '');
      r('Speed', `${(j.speed || 0).toFixed(j.speed >= 10 ? 0 : 1)}x`, `${Math.round(j.fps || 0)} fps · CPU ${Math.round(j.cpu || 0)}%`, jobLvl);
      r('Ready to', fmtTime(done), `${secs(Math.max(0, done - t))} ahead`);
    }

    sec('Session');
    sum(p.sessionId.slice(0, 8));
    r('ID', p.sessionId.slice(0, 8), es ? plural(es.restarts, 'restart') : '', '', p.sessionId);
    // The few numbers worth watching, as tiles above the sections.
    const tiles = [
      { k: 'Buffer', v: secs(cs.bufferAhead), sub: es ? `of ${secs(es.target)}` : 'ahead', lvl: bufLvl },
      es ? { k: 'Download', v: es.throttled ? 'paused' : es.fetching ? fmtBitrate(es.bandwidth) : 'idle', sub: es.throttled ? 'buffer full' : fmtBytes(es.bytes) }
        : { k: 'Server rate', v: fmtBitrate(this.serverRate * 8), sub: 'to this player' },
      { k: 'Stalls', v: String(cs.bufferEvents), sub: `${cs.bufferSeconds.toFixed(1)}s total`, lvl: cs.buffering ? 'bad' : cs.bufferEvents ? 'warn' : '' },
      { k: 'Dropped', v: `${dropPct.toFixed(dropPct && dropPct < 1 ? 2 : 0)}%`, sub: plural(cs.droppedFrames, 'frame'), lvl: dropLvl },
    ];
    // Whatever is wrong, in words, so nobody has to scan the rows for color.
    const issues = [];
    const hostDisk = this.host.some((x) => x.kind === 'disk');
    if (cause) issues.push({ lvl: cs.buffering ? 'bad' : 'warn', text: `${cs.buffering ? 'Buffering' : 'Stalled'}: ${cause.text}${cause.hint ? ` ${cause.hint}` : ''}` });
    else if (cs.buffering) issues.push({ lvl: 'bad', text: 'Buffering: waiting for data.' });
    else if (bufLvl) issues.push({ lvl: bufLvl, text: 'The buffer is running low.' });
    if (dropLvl) issues.push({ lvl: dropLvl, text: `This device drops ${dropPct.toFixed(1)}% of frames: it is struggling to decode or draw the video.` });
    if (jobLvl === 'bad') issues.push({ lvl: 'bad', text: `The server's conversion failed: ${j.error}` });
    else if (jobLvl) issues.push({ lvl: 'warn', text: `The server converts slower than playback (${(j.speed || 0).toFixed(1)}x).` });
    for (const x of this.host) issues.push({ lvl: 'warn', text: x.text });
    if (diskLvl && !hostDisk) issues.push({ lvl: diskLvl, text: `The server spends ${Math.round(this.diskWait * 100)}% of its time on this stream waiting for the disk.` });
    const why = p.reasons?.length ? p.reasons.map(reasonLabel).join('; ') : '';
    return { lvl, status, summary, sections, es, tiles, issues, method: `${method} · ${p.remote ? 'remote' : 'local'}`, why };
  },

  // The panel opens on a summary: what's wrong (if anything), the buffer
  // graph, four headline numbers and how it plays. Each section of detail
  // is one line until opened; which ones are open is remembered.
  renderStats(cs) {
    if (!this.plan) return;
    const m = this.statsModel(cs);
    // Expanded, the tiles carry the numbers; collapsed, the header does.
    if (this.statsSum.dataset.v !== m.summary) {
      this.statsSum.dataset.v = m.summary;
      // The status word carries the level in its color.
      clear(this.statsSum).append(h('span', { class: `ps-status ${m.lvl}` }, m.status), h('span', { class: 'ps-sum-more' }, m.summary.slice(m.status.length)));
    }
    const put = (el, ...kids) => clear(el).append(...kids.flat().filter(Boolean));
    this.statsIssues.hidden = !m.issues.length;
    put(this.statsIssues, m.issues.map((x) => h('div', { class: `ps-issue ${x.lvl}` }, x.text)));
    put(this.statsTiles, m.tiles.map((x) => h('div', { class: `ps-tile ${x.lvl || ''}` },
      h('span', { class: 'k' }, x.k), h('b', null, x.v), h('span', { class: 'sub' }, x.sub))));
    put(this.statsMethod, m.method, m.why ? h('span', { class: 'sub' }, ` · ${m.why}`) : null);
    // Sections update in place, so a focused header keeps focus between ticks.
    const open = new Set(prefs.get('statsOpen') || []);
    const want = m.sections.map((sec) => {
      let el = this.statsSecs.get(sec.title);
      if (!el) {
        const id = `ps-sec-${this.statsSecs.size}`;
        const btn = h('button', { class: 'ps-sec-h', 'aria-controls': id, onclick: (e) => {
          e.stopPropagation();
          const now = new Set(prefs.get('statsOpen') || []);
          if (now.has(sec.title)) now.delete(sec.title); else now.add(sec.title);
          prefs.set('statsOpen', [...now]);
          this.renderStats(this.clientStats());
        } }, h('span', { class: 'chev', html: icons.chevR }), h('span', { class: 't' }, sec.title), h('span', { class: 's' }));
        el = { box: h('div', { class: 'ps-sec' }, btn, h('div', { id, class: 'ps-sec-b' })), btn };
        this.statsSecs.set(sec.title, el);
      }
      const isOpen = open.has(sec.title);
      const lvl = sec.rows.some((r) => r.lvl === 'bad') ? 'bad' : sec.rows.some((r) => r.lvl === 'warn') ? 'warn' : '';
      el.box.classList.toggle('open', isOpen);
      el.btn.setAttribute('aria-expanded', String(isOpen));
      const sumEl = el.btn.querySelector('.s');
      // Something off inside shows as the summary's color.
      sumEl.className = `s ${lvl}`;
      if (sumEl.textContent !== sec.sum) sumEl.textContent = sec.sum;
      const body = el.box.lastChild;
      if (isOpen) {
        put(body, h('table', null, h('colgroup', null, h('col', { class: 'k' }), h('col')), sec.rows.map((row) => {
          const text = row.sub ? `${row.v} · ${row.sub}` : row.v;
          return h('tr', null, h('td', null, row.k),
            h('td', { class: row.lvl || null, title: row.full || text }, row.v, row.sub ? ' ' : null, row.sub ? h('span', { class: 'sub' }, row.sub) : null));
        })));
      } else if (body.firstChild) clear(body);
      return el.box;
    });
    const box = this.statsSecBox;
    for (const el of [...box.children]) if (!want.includes(el)) el.remove();
    want.forEach((el, i) => { if (box.children[i] !== el) box.insertBefore(el, box.children[i] || null); });
    this.drawBufGraph(m.es);
  },

  // Buffer sparkline. The scale tops out just above the target so a full
  // buffer sits near the dashed target line and a draining one visibly drops.
  drawBufGraph(es) {
    const c = this.statsCanvas, dpr = window.devicePixelRatio || 1;
    const w = c.clientWidth, ht = c.clientHeight;
    if (!w) return;
    if (c.width !== w * dpr || c.height !== ht * dpr) { c.width = w * dpr; c.height = ht * dpr; }
    const ctx = c.getContext('2d');
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, w, ht);
    const hist = this.bufHist, peak = Math.max(0, ...hist);
    const target = es ? es.target : 0;
    const top = Math.max(10, target && target < 1200 ? target * 1.15 : 0, peak * 1.15);
    const pad = 2, y = (val) => ht - pad - (Math.min(val, top) / top) * (ht - pad * 2);
    const x = (i) => ((90 - hist.length + i) / 89) * w;
    if (target && target <= top) {
      ctx.strokeStyle = 'rgba(255,255,255,.35)'; ctx.lineWidth = 1; ctx.setLineDash([3, 3]);
      ctx.beginPath(); ctx.moveTo(0, y(target)); ctx.lineTo(w, y(target)); ctx.stroke(); ctx.setLineDash([]);
    }
    if (hist.length > 1) {
      ctx.beginPath();
      hist.forEach((val, i) => (i ? ctx.lineTo(x(i), y(val)) : ctx.moveTo(x(i), y(val))));
      // Blue like the dashboard's charts: yellow in this panel means a warning.
      ctx.strokeStyle = '#3987e5'; ctx.lineWidth = 1.5; ctx.stroke();
      ctx.lineTo(x(hist.length - 1), ht); ctx.lineTo(x(0), ht); ctx.closePath();
      ctx.fillStyle = 'rgba(57,135,229,.16)'; ctx.fill();
    }
    ctx.font = '10px ui-monospace, Menlo, monospace'; ctx.fillStyle = 'rgba(255,255,255,.75)'; ctx.textAlign = 'right';
    ctx.fillText(target && target <= top ? `target ${secs(target)}` : `max ${secs(top)}`, w - 2, 10);
  },

  async copyStats() {
    const m = this.statsModel(this.clientStats());
    const lines = [`lex playback stats · ${[...this.titleEl.children].map((c) => c.textContent).join(' · ')} · ${new Date().toISOString()}`, m.summary];
    for (const s of m.sections) {
      lines.push('', `[${s.title}]`);
      for (const row of s.rows) lines.push(`${row.k}: ${row.full || row.v}${row.sub ? ` · ${row.sub}` : ''}`);
    }
    const text = lines.join('\n');
    try { await navigator.clipboard.writeText(text); }
    catch {
      // Plain-http LAN installs have no Clipboard API.
      const ta = h('textarea', { style: { position: 'fixed', opacity: '0' } }, text);
      this.root.appendChild(ta); ta.select();
      const ok = document.execCommand('copy');
      ta.remove();
      if (!ok) { toast('Could not copy stats', 'error'); return; }
    }
    toast('Stats copied');
  },
};
