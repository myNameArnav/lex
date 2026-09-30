// Full-screen player: picks a playback plan from the server, plays it via
// direct <video src> or the MSE engine, and renders controls + stats.

import { h, icons, resLabel, fmtTime, fmtBitrate, fmtBytes, streamLabel, toast, clear, langName, channelName } from './ui.js';
import { api, img } from './api.js';
import { detectCaps } from './caps.js';
import { prefs, QUALITIES } from './prefs.js';
import { MseEngine } from './mse.js';

let current = null;

export function openPlayer(opts) {
  if (current) current.close();
  current = new Player(opts);
  return current;
}

export function isPlayerOpen() { return !!current; }

const METHOD_LABEL = { direct: 'Direct Play', remux: 'Direct Stream', transcode: 'Transcode' };
const SPEEDS = [0.5, 0.75, 1, 1.25, 1.5, 1.75, 2];
const BUFFERS = [30, 60, 90, 180, 300, 600];

function pickAudio(file, lang) {
  const auds = (file.info?.streams || []).filter((s) => s.type === 'audio');
  if (!auds.length) return -1;
  return (lang && auds.find((a) => a.language === lang)) || auds.find((a) => a.default) || auds[0];
}

function pickSubtitle(file, audio) {
  const mode = prefs.get('subMode');
  const want = prefs.get('subLang');
  const subs = file.subtitles || [];
  if (mode === 'off' || !subs.length) return -1;
  const byLang = (l) => subs.filter((s) => s.language === l || (!s.language && l === want));
  const prefer = (list) => list.find((s) => s.textSub && !s.forced) || list.find((s) => !s.forced) || list[0];
  if (mode === 'always') {
    const s = prefer(byLang(want));
    return s ? s.index : -1;
  }
  // auto: forced subs for the audio language, or full subs if the audio is foreign.
  const alang = audio && audio !== -1 ? audio.language : '';
  const forced = subs.find((s) => s.forced && (s.language === alang || s.language === want));
  if (forced) return forced.index;
  if (want && alang && alang !== want) {
    const s = prefer(byLang(want));
    if (s) return s.index;
  }
  return -1;
}

class Player {
  constructor({ itemId, start = null, onClose }) {
    this.itemId = itemId;
    this.onClose = onClose;
    this.sessionId = null;
    this.engine = null;
    this.plan = null;
    this.mode = prefs.get('mode');
    this.quality = prefs.get('quality');
    this.fallbacks = 0;
    this.stalls = { count: 0, secs: 0, since: 0 };
    this.lastJob = null;
    this.serverRate = 0;
    this.started = false;
    this.bufHist = [];
    this.closed = false;
    this.upNextShown = false;
    this.build();
    this.bind();
    this.start(itemId, start);
  }

  // ---------- DOM ----------
  build() {
    const b = (name, title, on, cls = '') => h('button', { class: `pbtn ${cls}`, title, 'aria-label': title, html: icons[name], onclick: (e) => { e.stopPropagation(); on(e); } });
    this.video = h('video', { playsinline: true, preload: 'auto' });
    this.titleEl = h('div', { class: 'p-title' });
    this.center = h('div', { class: 'p-center' });
    // Subtitles are drawn by us (not ::cue) so we control position and size.
    this.subsEl = h('div', { class: 'p-subs', 'aria-live': 'polite' });
    this.seek = h('div', { class: 'seek' }, h('div', { class: 'rail' }), h('div', { class: 'knob' }));
    this.rail = this.seek.firstChild;
    this.knob = this.seek.lastChild;
    this.fill = h('div', { class: 'fill' });
    this.rail.appendChild(this.fill);
    this.timeEl = h('span', { class: 'p-time' }, '0:00 / 0:00');
    this.methodEl = h('span', { class: 'p-method hide-mobile', title: 'Playback method (click for stats)', onclick: (e) => { e.stopPropagation(); this.toggleStats(); } });
    this.playBtn = b('play', 'Play (k)', () => this.togglePlay(), 'big');
    this.volBtn = b('volume', 'Mute (m)', () => { this.video.muted = !this.video.muted; });
    this.volRange = h('input', { type: 'range', min: 0, max: 1, step: 0.05, value: prefs.get('volume'), oninput: (e) => { this.video.volume = +e.target.value; this.video.muted = false; }, onclick: (e) => e.stopPropagation() });
    this.nextBtn = b('next', 'Next episode (n)', () => this.playNext(), 'hidden');
    this.fsBtn = b('fullscreen', 'Fullscreen (f)', () => this.toggleFullscreen());
    this.ccBtn = b('cc', 'Subtitles & audio (c)', (e) => this.toggleMenu('tracks'));
    this.gearBtn = b('gear', 'Settings', () => this.toggleMenu('settings'));
    this.pipBtn = document.pictureInPictureEnabled ? b('pip', 'Picture in picture', () => this.togglePip(), 'hide-mobile') : null;

    this.root = h('div', { class: 'player' },
      this.video,
      h('div', { class: 'shade-top' }), h('div', { class: 'shade-bot' }),
      this.subsEl,
      this.center,
      h('div', { class: 'p-top' }, b('back', 'Close (Esc)', () => this.close()), this.titleEl),
      h('div', { class: 'p-bot' },
        this.seek,
        h('div', { class: 'p-controls' },
          this.playBtn,
          b('back10', `Back ${prefs.get('skipBack')}s (←)`, () => this.skip(-prefs.get('skipBack'))),
          b('fwd30', `Forward ${prefs.get('skipFwd')}s (→)`, () => this.skip(prefs.get('skipFwd'))),
          h('div', { class: 'vol' }, this.volBtn, this.volRange),
          this.timeEl,
          h('div', { class: 'spacer' }),
          this.methodEl,
          this.nextBtn, this.ccBtn, this.gearBtn, this.pipBtn, this.fsBtn)));
    this.applySubStyle();
    document.body.appendChild(this.root);
    document.body.style.overflow = 'hidden';
    this.video.volume = prefs.get('volume');
    this.video.muted = prefs.get('muted');
    this.showSpinner(true);
  }

  bind() {
    const v = this.video;
    this.onKey = (e) => this.key(e);
    document.addEventListener('keydown', this.onKey);
    this.onFs = () => { this.fsBtn.innerHTML = document.fullscreenElement ? icons.exitfs : icons.fullscreen; };
    document.addEventListener('fullscreenchange', this.onFs);
    this.onHide = () => this.sendStop(true);
    window.addEventListener('pagehide', this.onHide);

    v.addEventListener('play', () => { this.playBtn.innerHTML = icons.pause; this.poke(); this.beat(); });
    v.addEventListener('pause', () => { this.playBtn.innerHTML = icons.play; this.showUI(true); this.beat(); });
    v.addEventListener('waiting', () => {
      this.showSpinner(true);
      if (this.started && !v.seeking) { this.stalls.count++; this.stalls.since = performance.now(); }
    });
    const ready = () => {
      this.showSpinner(false);
      if (this.stalls.since) { this.stalls.secs += (performance.now() - this.stalls.since) / 1000; this.stalls.since = 0; }
    };
    v.addEventListener('playing', () => { ready(); this.started = true; });
    v.addEventListener('canplay', ready);
    v.addEventListener('seeked', () => { ready(); this.beat(); });
    v.addEventListener('seeking', () => this.showSpinner(true));
    v.addEventListener('timeupdate', () => { this.renderTime(); this.checkIntro(); if (this.subTrack) this.renderCues(this.subTrack); });
    v.addEventListener('progress', () => this.renderTime());
    v.addEventListener('volumechange', () => {
      this.volBtn.innerHTML = v.muted || v.volume === 0 ? icons.mute : icons.volume;
      this.volRange.value = v.muted ? 0 : v.volume;
      prefs.set('volume', v.volume); prefs.set('muted', v.muted);
    });
    v.addEventListener('ended', () => this.onEnded());
    v.addEventListener('error', () => this.onVideoError());

    // Controls visibility.
    this.root.addEventListener('mousemove', () => this.poke());
    this.root.addEventListener('click', (e) => {
      if (e.target === v || e.target === this.center) {
        if (this.menu) { this.closeMenu(); return; }
        if (matchMedia('(hover: none)').matches) { this.toggleUI(); return; }
        this.togglePlay();
      }
    });
    v.addEventListener('dblclick', () => this.toggleFullscreen());
    let lastTap = 0;
    v.addEventListener('touchend', (e) => {
      const now = Date.now();
      if (now - lastTap < 300) {
        const x = e.changedTouches[0].clientX / window.innerWidth;
        if (x < 0.35) this.skip(-prefs.get('skipBack'));
        else if (x > 0.65) this.skip(prefs.get('skipFwd'));
        e.preventDefault();
      }
      lastTap = now;
    });

    // Seek bar dragging.
    const seekAt = (e) => {
      const r = this.seek.getBoundingClientRect();
      const x = Math.min(Math.max((e.clientX ?? e.touches?.[0]?.clientX) - r.left, 0), r.width);
      return { t: (x / r.width) * this.duration(), x };
    };
    let dragging = false;
    const showTip = (e) => {
      const { t, x } = seekAt(e);
      if (!this.tip) { this.tip = h('div', { class: 'tip' }); this.seek.appendChild(this.tip); }
      this.tip.textContent = fmtTime(t);
      this.tip.style.left = `${x}px`;
      return t;
    };
    this.seek.addEventListener('pointerdown', (e) => {
      dragging = true;
      this.seek.setPointerCapture(e.pointerId);
      this.seek.classList.add('drag');
      this.dragT = showTip(e);
      this.renderTime();
    });
    this.seek.addEventListener('pointermove', (e) => {
      const t = showTip(e);
      if (dragging) { this.dragT = t; this.renderTime(); }
    });
    this.seek.addEventListener('pointerup', (e) => {
      if (!dragging) return;
      dragging = false;
      this.seek.classList.remove('drag');
      const t = seekAt(e).t;
      this.dragT = null;
      this.seekTo(t);
    });
    this.seek.addEventListener('pointerleave', () => { if (!dragging && this.tip) { this.tip.remove(); this.tip = null; } });
    this.seek.addEventListener('click', (e) => e.stopPropagation());

    this.hbTimer = setInterval(() => this.beat(), prefs.get('heartbeat') * 1000);
    this.uiTimer = setInterval(() => this.tickUI(), 1000);
  }

  // ---------- loading ----------
  async start(itemId, start) {
    try {
      const d = await api(`/api/items/${itemId}`);
      if (this.closed) return;
      this.detail = d;
      this.item = d.item;
      this.segments = d.segments || [];
      this.files = d.files || [];
      this.file = this.files[0];
      if (!this.file) throw new Error('No playable file for this item');
      const aud = pickAudio(this.file, prefs.get('audioLang'));
      this.audio = aud === -1 ? -1 : aud.index;
      this.subtitle = pickSubtitle(this.file, aud);
      this.renderTitle();
      this.nextBtn.classList.toggle('hidden', !d.next);
      if (start == null) {
        const ud = this.item.userData;
        start = ud && ud.position > 0 && !ud.played ? ud.position : 0;
      }
      await this.loadPlan(start);
      this.setupMediaSession();
      if (prefs.get('showStats') && !this.statsEl) this.toggleStats();
    } catch (e) {
      this.showError(e.message);
    }
  }

  renderTitle() {
    const it = this.item;
    clear(this.titleEl);
    if (it.kind === 'episode') {
      this.titleEl.append(h('b', null, this.detail.show?.title || it.showTitle || ''), h('span', null, `S${it.season} · E${it.episode} — ${it.title}`));
    } else {
      this.titleEl.append(h('b', null, it.title), h('span', null, [it.year, resLabel(this.file?.width, this.file?.height)].filter(Boolean).join(' · ')));
    }
  }

  duration() {
    return this.plan?.duration || this.file?.info?.duration || (isFinite(this.video.duration) ? this.video.duration : 0);
  }

  async loadPlan(start, overrides = {}) {
    this.hideError();
    this.showSpinner(true);
    const caps = detectCaps();
    const req = {
      itemId: this.item.id, fileId: this.file.id, audio: this.audio, subtitle: this.subtitle,
      mode: overrides.mode || this.mode, maxBitrate: this.quality, audioLang: prefs.get('audioLang'),
      caps, sessionId: this.sessionId, start,
    };
    const res = await api('/api/playback/plan', { method: 'POST', body: req });
    if (this.closed) return;
    this.plan = res.plan;
    this.cached = !!res.cached;
    if (res.segments) this.segments = res.segments;
    this.sessionId = res.plan.sessionId;
    this.audio = res.plan.audio;
    this.file = { ...this.file, ...res.file, subtitles: this.file.subtitles };
    this.methodEl.textContent = METHOD_LABEL[this.plan.method];
    this.methodEl.className = `p-method hide-mobile ${this.plan.method}`;
    this.methodEl.title = (this.plan.reasons || []).join('; ') || 'Playing the original file';
    await this.attach(start);
  }

  teardown() {
    if (this.engine) { this.engine.destroy(); this.engine = null; }
    const v = this.video;
    v.pause();
    v.removeAttribute('src');
    for (const t of [...v.querySelectorAll('track')]) t.remove();
    try { v.load(); } catch {}
  }

  async attach(start) {
    this.teardown();
    const v = this.video;
    this.started = false;
    if (this.plan.method === 'direct') {
      v.src = this.plan.url + (start > 0 ? `#t=${start.toFixed(2)}` : '');
      if (start > 0) {
        v.addEventListener('loadedmetadata', () => { if (Math.abs(v.currentTime - start) > 2) v.currentTime = start; }, { once: true });
      }
    } else {
      this.engine = new MseEngine(v, this.plan, {
        forward: prefs.get('forwardBuffer'),
        back: prefs.get('backBuffer'),
        onError: (msg, status, decode) => this.onStreamError(msg, status, decode),
      });
      await this.engine.open(start);
    }
    this.setSubtitleTrack(this.subtitle);
    try { await v.play(); } catch (e) {
      // Autoplay with sound blocked: show paused state, user clicks play.
      this.playBtn.innerHTML = icons.play;
      this.showSpinner(false);
    }
    this.beat();
  }

  async replan(overrides) {
    const t = this.video.currentTime || 0;
    const wasPaused = this.video.paused && this.started;
    try {
      await this.loadPlan(t, overrides);
      if (wasPaused) this.video.pause();
    } catch (e) { this.showError(e.message); }
  }

  // ---------- errors & fallback ----------
  async onVideoError() {
    const err = this.video.error;
    if (!err || err.code === 1 || this.closed || !this.plan) return;
    // MEDIA_ERR_SRC_NOT_SUPPORTED on direct play, or decode errors: fall back.
    await this.fallback(`${this.plan.method} failed (${err.message || 'media error ' + err.code})`);
  }

  async onStreamError(msg, status, decode) {
    if (decode) return this.fallback(msg);
    this.showError(msg, status !== 503);
  }

  async fallback(reason) {
    const order = ['direct', 'remux', 'transcode'];
    const idx = order.indexOf(this.plan.method);
    if (this.fallbacks >= 2 || idx >= 2) { this.showError(`Playback failed: ${reason}`); return; }
    this.fallbacks++;
    const next = order[idx + 1];
    toast(`${METHOD_LABEL[this.plan.method]} didn't work in this browser — switching to ${METHOD_LABEL[next]}`);
    this.mode = next;
    await this.replan({ mode: next });
  }

  showError(msg, retry = true) {
    this.showSpinner(false);
    this.hideError();
    this.errEl = h('div', { class: 'p-error' }, h('div', null,
      h('h2', { style: { margin: 0 } }, "Can't play this"),
      h('p', { class: 'muted', style: { margin: 0 } }, msg),
      h('div', { class: 'row' },
        retry ? h('button', { class: 'btn primary', onclick: () => { this.hideError(); this.replan(); } }, 'Retry') : null,
        this.plan?.method !== 'transcode' ? h('button', { class: 'btn', onclick: () => { this.hideError(); this.mode = 'transcode'; this.replan({ mode: 'transcode' }); } }, 'Try transcoding') : null,
        h('button', { class: 'btn', onclick: () => this.close() }, 'Close'))));
    this.root.appendChild(this.errEl);
  }

  hideError() { if (this.errEl) { this.errEl.remove(); this.errEl = null; } }

  // ---------- subtitles ----------
  subById(idx) { return (this.file.subtitles || []).find((s) => s.index === idx); }

  // Subtitles are fetched first and attached as a blob: a <track> whose src is
  // still loading holds the video at HAVE_CURRENT_DATA (per the HTML spec),
  // and extracting subs from a large file can take a while on a Pi.
  async setSubtitleTrack(idx) {
    const v = this.video;
    const token = (this.subToken = (this.subToken || 0) + 1);
    for (const t of [...v.querySelectorAll('track')]) t.remove();
    for (const tt of v.textTracks) tt.mode = 'disabled';
    this.subTrack = null;
    this.renderCues(null);
    if (this.subBlob) { URL.revokeObjectURL(this.subBlob); this.subBlob = null; }
    const s = this.subById(idx);
    if (!s || !s.textSub) return;
    const slow = setTimeout(() => { if (token === this.subToken) toast('Extracting subtitles from the file… they will appear shortly'); }, 1500);
    try {
      const res = await fetch(`/api/files/${this.file.id}/subs/${idx}.vtt`, { credentials: 'same-origin' });
      if (!res.ok) throw new Error((await res.json().catch(() => ({}))).error || `HTTP ${res.status}`);
      const text = await res.text();
      if (token !== this.subToken || this.closed) return;
      this.subBlob = URL.createObjectURL(new Blob([text], { type: 'text/vtt' }));
      const track = h('track', { kind: 'subtitles', label: streamLabel(s), srclang: (s.language || 'und').slice(0, 2), src: this.subBlob, default: true });
      v.appendChild(track);
      const tt = track.track;
      tt.mode = 'hidden';
      const render = () => this.renderCues(tt);
      tt.addEventListener('cuechange', render);
      track.addEventListener('load', render);
      this.subTrack = tt;
    } catch (e) {
      if (token === this.subToken) toast(`Could not load subtitles: ${e.message}`, 'error');
    } finally {
      clearTimeout(slow);
    }
  }

  async chooseSubtitle(idx) {
    const prev = this.subById(this.subtitle);
    const next = this.subById(idx);
    this.subtitle = idx;
    const needBurn = next && !next.textSub;
    const hadBurn = prev && !prev.textSub;
    if (next) { prefs.set('subLang', next.language || prefs.get('subLang')); if (prefs.get('subMode') === 'off') prefs.set('subMode', 'always'); }
    else prefs.set('subMode', 'off');
    if (needBurn || hadBurn) {
      if (needBurn) toast('Image-based subtitles need to be burned in: transcoding');
      await this.replan();
    } else {
      this.setSubtitleTrack(idx);
    }
  }

  async chooseAudio(idx) {
    this.audio = idx;
    const a = (this.file.info?.streams || []).find((s) => s.index === idx);
    if (a?.language) prefs.set('audioLang', a.language);
    await this.replan();
  }

  applySubStyle() {
    const r = this.root;
    r.dataset.subSize = prefs.get('subSize');
    r.dataset.subPos = prefs.get('subPos');
    r.classList.toggle('subs-nobg', !prefs.get('subBg'));
  }

  // Called on cuechange and every timeupdate: Chrome doesn't always fire
  // cuechange when playback starts right at a cue boundary.
  renderCues(tt) {
    const el = this.subsEl;
    if (!el) return;
    const t = this.video.currentTime;
    const cues = tt && tt.activeCues ? [...tt.activeCues].filter((c) => c.startTime <= t + 0.05 && c.endTime > t).sort((a, b) => a.startTime - b.startTime) : [];
    const key = cues.map((c) => `${c.startTime}:${c.text}`).join('|');
    if (key === this.cueKey) return;
    this.cueKey = key;
    el.replaceChildren();
    for (const c of cues) {
      const line = h('div', { class: 'cue' });
      // getCueAsHTML returns a safe DocumentFragment (keeps <i>/<b>).
      try { line.appendChild(c.getCueAsHTML()); } catch { line.textContent = c.text; }
      el.appendChild(line);
    }
  }

  // ---------- controls ----------
  togglePlay() {
    const v = this.video;
    if (v.paused) { v.play().catch(() => {}); this.flash('play'); }
    else { v.pause(); this.flash('pause'); }
  }

  skip(d) { this.seekTo(this.video.currentTime + d); }

  seekTo(t) {
    const d = this.duration();
    t = Math.max(0, Math.min(t, d ? d - 0.5 : t));
    this.video.currentTime = t;
    this.poke();
  }

  flash(name) {
    const f = h('div', { class: 'p-flash', html: icons[name] });
    this.center.appendChild(f);
    setTimeout(() => f.remove(), 600);
  }

  showSpinner(on) {
    if (on && !this.spin) { this.spin = h('div', { class: 'spinner' }); this.center.appendChild(this.spin); }
    if (!on && this.spin) { this.spin.remove(); this.spin = null; }
  }

  poke() {
    this.showUI(true);
    clearTimeout(this.hideTimer);
    this.hideTimer = setTimeout(() => { if (!this.video.paused && !this.menu) this.showUI(false); }, 3000);
  }

  showUI(on) { this.root.classList.toggle('hide-ui', !on); }
  toggleUI() { if (this.root.classList.contains('hide-ui')) this.poke(); else this.showUI(false); }

  toggleFullscreen() {
    if (document.fullscreenElement) document.exitFullscreen();
    else if (this.root.requestFullscreen) this.root.requestFullscreen().catch(() => {});
    else if (this.video.webkitEnterFullscreen) this.video.webkitEnterFullscreen();
  }

  async togglePip() {
    try {
      if (document.pictureInPictureElement) await document.exitPictureInPicture();
      else await this.video.requestPictureInPicture();
    } catch (e) { toast(e.message, 'error'); }
  }

  key(e) {
    if (e.target.tagName === 'INPUT' && e.target.type !== 'range') return;
    if (e.metaKey || e.ctrlKey || e.altKey) return;
    const v = this.video;
    const k = e.key.toLowerCase();
    const map = {
      ' ': () => this.togglePlay(), k: () => this.togglePlay(),
      arrowleft: () => this.skip(-prefs.get('skipBack')), j: () => this.skip(-prefs.get('skipBack')),
      arrowright: () => this.skip(prefs.get('skipFwd')), l: () => this.skip(prefs.get('skipFwd')),
      arrowup: () => { v.volume = Math.min(1, v.volume + 0.05); v.muted = false; },
      arrowdown: () => { v.volume = Math.max(0, v.volume - 0.05); },
      f: () => this.toggleFullscreen(), m: () => { v.muted = !v.muted; },
      i: () => this.toggleStats(), c: () => this.toggleMenu('tracks'),
      n: () => this.playNext(),
      escape: () => { if (this.menu) this.closeMenu(); else if (!document.fullscreenElement) this.close(); },
      home: () => this.seekTo(0), end: () => this.seekTo(this.duration() - 5),
    };
    if (/^[0-9]$/.test(k)) { this.seekTo((this.duration() * +k) / 10); e.preventDefault(); return; }
    if (map[k]) { e.preventDefault(); map[k](); this.poke(); }
  }

  // ---------- time / seek bar ----------
  bufferedRanges() {
    if (this.engine) return this.engine.ranges();
    const b = this.video.buffered, out = [];
    for (let i = 0; i < b.length; i++) out.push([b.start(i), b.end(i)]);
    return out;
  }

  renderTime() {
    const d = this.duration();
    const t = this.dragT ?? this.video.currentTime;
    const pct = d ? (t / d) * 100 : 0;
    this.fill.style.width = `${pct}%`;
    this.knob.style.left = `${pct}%`;
    this.timeEl.textContent = `${fmtTime(t)} / ${fmtTime(d)}`;
    // Buffered ranges (reuse nodes).
    const ranges = this.bufferedRanges();
    const bufs = [...this.rail.querySelectorAll('.buf')];
    ranges.forEach(([s, e], i) => {
      let el = bufs[i];
      if (!el) { el = h('div', { class: 'buf' }); this.rail.insertBefore(el, this.fill); }
      el.style.left = `${(s / d) * 100}%`;
      el.style.width = `${((e - s) / d) * 100}%`;
    });
    bufs.slice(ranges.length).forEach((el) => el.remove());
    if (!this.chaptersDrawn && d) {
      this.chaptersDrawn = true;
      for (const c of (this.file?.info?.chapters || []).slice(1)) this.rail.appendChild(h('div', { class: 'chap', style: { left: `${(c.start / d) * 100}%` }, title: c.title }));
      const seg = this.intro();
      if (seg) this.rail.insertBefore(h('div', { class: 'seg', style: { left: `${(seg.start / d) * 100}%`, width: `${((seg.end - seg.start) / d) * 100}%` }, title: 'Intro' }), this.fill);
    }
    if ('mediaSession' in navigator && d && navigator.mediaSession.setPositionState) {
      try { navigator.mediaSession.setPositionState({ duration: d, position: Math.min(t, d), playbackRate: this.video.playbackRate }); } catch {}
    }
  }

  // ---------- heartbeat & stats ----------
  clientStats() {
    const v = this.video;
    const q = v.getVideoPlaybackQuality ? v.getVideoPlaybackQuality() : null;
    const es = this.engine ? this.engine.stats() : null;
    let ahead = es ? es.ahead : 0;
    if (!es) {
      const b = v.buffered;
      for (let i = 0; i < b.length; i++) if (b.start(i) <= v.currentTime + 0.5 && b.end(i) >= v.currentTime) ahead = b.end(i) - v.currentTime;
    }
    const stallNow = this.stalls.since ? (performance.now() - this.stalls.since) / 1000 : 0;
    return {
      bufferAhead: ahead, bandwidth: es ? es.bandwidth : 0,
      droppedFrames: q ? q.droppedVideoFrames : 0, totalFrames: q ? q.totalVideoFrames : 0,
      buffering: !!this.stalls.since, bufferEvents: this.stalls.count, bufferSeconds: this.stalls.secs + stallNow,
      resolution: v.videoWidth ? `${v.videoWidth}x${v.videoHeight}` : '', volume: v.muted ? 0 : v.volume, rate: v.playbackRate,
    };
  }

  async beat() {
    if (!this.sessionId || this.closed || !this.plan) return;
    try {
      const r = await api('/api/playback/progress', { method: 'POST', body: { sessionId: this.sessionId, position: this.video.currentTime, paused: this.video.paused, stats: this.clientStats() } });
      this.lastJob = r.job || null;
      this.serverRate = r.serverRate || 0;
    } catch {}
  }

  sendStop(beacon) {
    if (!this.sessionId || this.stopSent) return;
    this.stopSent = true;
    const body = { sessionId: this.sessionId, position: this.video.currentTime };
    api('/api/playback/stop', { method: 'POST', body, keepalive: true }).catch(() => {});
  }

  tickUI() {
    if (this.closed) return;
    const es = this.clientStats();
    this.bufHist.push(es.bufferAhead);
    if (this.bufHist.length > 90) this.bufHist.shift();
    if (this.statsEl) this.renderStats(es);
    this.checkUpNext();
  }

  toggleStats() {
    if (this.statsEl) { this.statsEl.remove(); this.statsEl = null; prefs.set('showStats', false); return; }
    if (!this.plan) return;
    prefs.set('showStats', true);
    this.statsEl = h('div', { class: 'p-stats', onclick: (e) => e.stopPropagation() });
    this.root.appendChild(this.statsEl);
    this.renderStats(this.clientStats());
    this.beat();
  }

  renderStats(cs) {
    const p = this.plan, f = this.file, info = f?.info || {};
    if (!p) return;
    const vs = (info.streams || []).find((s) => s.type === 'video');
    const as = (info.streams || []).find((s) => s.index === p.audio);
    const es = this.engine ? this.engine.stats() : null;
    const j = this.lastJob;
    const rows = [];
    const hd = (t) => rows.push(h('tr', null, h('td', { class: 'hd', colspan: 2 }, t)));
    const r = (k, v) => rows.push(h('tr', null, h('td', null, k), h('td', null, v)));
    hd('Playback');
    r('Method', `${METHOD_LABEL[p.method]}${p.remote ? ' · remote' : ' · local'}`);
    if (p.reasons?.length) r('Why', p.reasons.join('; '));
    r('Session', `${p.sessionId}${es ? ` · restarts ${es.restarts}` : ''}`);
    r('Position', `${fmtTime(this.video.currentTime)} / ${fmtTime(this.duration())} · ${this.video.playbackRate}x`);
    hd('Video');
    if (vs) r('Source', `${vs.codec.toUpperCase()} ${vs.profile || ''} ${vs.width}x${vs.height} ${vs.frameRate ? vs.frameRate.toFixed(3).replace(/\.?0+$/, '') + 'fps' : ''} ${vs.bitDepth || 8}-bit ${vs.hdr || 'SDR'}`);
    r('Output', `${p.videoOut}${p.method !== 'direct' ? ` · ${p.mime}` : ''}`);
    r('Rendered', `${cs.resolution || '—'} · viewport ${this.video.clientWidth}x${this.video.clientHeight} @${window.devicePixelRatio}x`);
    r('Frames', `${cs.droppedFrames} dropped / ${cs.totalFrames} decoded${cs.totalFrames ? ` (${((cs.droppedFrames / cs.totalFrames) * 100).toFixed(2)}%)` : ''}`);
    hd('Audio');
    if (as) r('Source', `${as.codec.toUpperCase()} ${channelName(as.channels)} ${as.sampleRate ? as.sampleRate / 1000 + 'kHz' : ''} ${langName(as.language)}`);
    r('Output', p.audioOut || 'none');
    hd('Stream');
    r('Container', `${(f.name || '').split('.').pop()} → ${p.method === 'direct' ? 'original file (range requests)' : 'fragmented MP4 (MSE)'}`);
    r('Read from', this.cached ? 'SSD cache' : 'library disk');
    const seg = this.intro();
    if (seg) r('Intro', `${fmtTime(seg.start)}–${fmtTime(seg.end)} (${seg.source})`);
    r('Source bitrate', fmtBitrate((info.bitrate || 0)));
    if (p.method === 'transcode') r('Target bitrate', fmtBitrate(p.bitrate * 1000));
    if (p.limitKbps) r('Bitrate limit', fmtBitrate(p.limitKbps * 1000));
    r('Buffer ahead', `${cs.bufferAhead.toFixed(1)}s${es ? ` / target ${Math.round(es.target)}s${es.quotaLimited ? ` (browser memory limit; setting ${prefs.get('forwardBuffer')}s)` : ''} · back ${prefs.get('backBuffer')}s` : ' (browser managed)'}`);
    const canvas = h('canvas', { width: 360, height: 36 });
    rows.push(h('tr', null, h('td', { colspan: 2 }, canvas)));
    if (es) {
      r('Download', `${fmtBitrate(es.bandwidth)} · ${fmtBytes(es.bytes)} received${es.throttled ? ' · paused (buffer full)' : es.fetching ? '' : ' · idle'}`);
      r('Ranges', es.ranges.map(([s, e]) => `${fmtTime(s)}–${fmtTime(e)}`).join(', ') || '—');
    }
    r('Server rate', fmtBitrate(this.serverRate * 8));
    r('Stalls', `${cs.bufferEvents} · ${cs.bufferSeconds.toFixed(1)}s total${cs.buffering ? ' · buffering now' : ''}`);
    if (j) {
      hd('Server ffmpeg');
      r('State', j.exited ? (j.error ? `exited: ${j.error}` : 'finished') : j.throttled ? 'throttled (waiting for client)' : 'running');
      r('Speed', `${(j.speed || 0).toFixed(2)}x · ${Math.round(j.fps || 0)} fps · CPU ${Math.round(j.cpu || 0)}%`);
      r('Processed to', `${fmtTime(j.outTime)} (${Math.max(0, j.outTime - this.video.currentTime).toFixed(0)}s ahead)`);
    }
    clear(this.statsEl).appendChild(h('table', null, rows));
    // Buffer sparkline.
    const ctx = canvas.getContext('2d');
    const max = Math.max(10, ...this.bufHist);
    ctx.strokeStyle = '#f2b33d'; ctx.lineWidth = 1.5; ctx.beginPath();
    this.bufHist.forEach((v, i) => {
      const x = (i / 89) * 360, y = 34 - (v / max) * 32;
      i ? ctx.lineTo(x, y) : ctx.moveTo(x, y);
    });
    ctx.stroke();
    ctx.fillStyle = 'rgba(255,255,255,.4)'; ctx.font = '10px monospace'; ctx.fillText(`${max.toFixed(0)}s`, 330, 10);
  }

  // ---------- menus ----------
  closeMenu() { if (this.menu) { this.menu.remove(); this.menu = null; this.menuName = null; } }

  toggleMenu(name) {
    if (this.menuName === name) { this.closeMenu(); return; }
    this.closeMenu();
    this.menuName = name;
    this.menu = h('div', { class: 'p-menu', onclick: (e) => e.stopPropagation() });
    this.renderMenu();
    this.root.appendChild(this.menu);
    this.showUI(true);
  }

  renderMenu() {
    const m = clear(this.menu);
    const item = (label, on, active, small) => h('button', { onclick: on }, h('span', { class: 'ck', html: active ? icons.check : '' }), h('span', null, label), small ? h('small', null, small) : null);
    if (this.menuName === 'tracks') {
      m.appendChild(h('h4', null, 'Subtitles'));
      m.appendChild(item('Off', () => { this.chooseSubtitle(-1); this.closeMenu(); }, this.subtitle < 0));
      for (const s of this.file.subtitles || []) m.appendChild(item(streamLabel(s), () => { this.chooseSubtitle(s.index); this.closeMenu(); }, this.subtitle === s.index));
      const auds = (this.file.info?.streams || []).filter((s) => s.type === 'audio');
      if (auds.length) {
        m.appendChild(h('h4', null, 'Audio'));
        for (const a of auds) m.appendChild(item(streamLabel(a), () => { if (a.index !== this.audio) this.chooseAudio(a.index); this.closeMenu(); }, this.audio === a.index));
      }
      m.appendChild(h('h4', null, 'Subtitle style'));
      for (const [k, l] of [['small', 'Small'], ['medium', 'Medium'], ['large', 'Large']]) m.appendChild(item(l, () => { prefs.set('subSize', k); this.applySubStyle(); this.renderMenu(); }, prefs.get('subSize') === k));
      m.appendChild(h('h4', null, 'Subtitle position'));
      for (const [k, l] of [['low', 'Low'], ['normal', 'Normal'], ['high', 'High']]) m.appendChild(item(l, () => { prefs.set('subPos', k); this.applySubStyle(); this.renderMenu(); }, prefs.get('subPos') === k));
      m.appendChild(item('Background box', () => { prefs.set('subBg', !prefs.get('subBg')); this.applySubStyle(); this.renderMenu(); }, prefs.get('subBg')));
      return;
    }
    // settings
    m.appendChild(h('h4', null, 'Quality'));
    for (const [k, l] of QUALITIES) {
      const src = this.file?.info?.bitrate ? Math.round(this.file.info.bitrate / 1000) : 0;
      if (k && src && k > src * 1.5 && k !== this.quality) continue;
      m.appendChild(item(l, () => { this.quality = k; prefs.set('quality', k); this.closeMenu(); this.replan(); }, this.quality === k, k === 0 && src ? fmtBitrate(src * 1000) : ''));
    }
    m.appendChild(h('h4', null, 'Playback method'));
    for (const [k, l, d] of [['auto', 'Automatic', 'direct → remux → transcode'], ['direct', 'Force direct play', ''], ['remux', 'Force direct stream (remux)', ''], ['transcode', 'Force transcode', '']]) {
      m.appendChild(item(l, () => { this.mode = k; prefs.set('mode', k); this.fallbacks = 0; this.closeMenu(); this.replan(); }, this.mode === k, d));
    }
    m.appendChild(h('h4', null, 'Speed'));
    m.appendChild(h('div', { class: 'row wrap', style: { padding: '2px 8px 6px', gap: '4px' } }, SPEEDS.map((s) => h('button', {
      class: `btn sm ${this.video.playbackRate === s ? 'primary' : ''}`, style: { width: 'auto' },
      onclick: () => { this.video.playbackRate = s; this.renderMenu(); },
    }, `${s}x`))));
    if (this.plan?.method !== 'direct') {
      m.appendChild(h('h4', null, 'Buffer ahead'));
      m.appendChild(h('div', { class: 'row wrap', style: { padding: '2px 8px 6px', gap: '4px' } }, BUFFERS.map((s) => h('button', {
        class: `btn sm ${prefs.get('forwardBuffer') === s ? 'primary' : ''}`, style: { width: 'auto' },
        onclick: () => { prefs.set('forwardBuffer', s); if (this.engine) this.engine.opts.forward = s; this.renderMenu(); },
      }, s >= 60 ? `${s / 60}m` : `${s}s`))));
    }
    m.appendChild(h('h4', null, 'Options'));
    m.appendChild(item('Stats for nerds', () => { this.toggleStats(); this.renderMenu(); }, !!this.statsEl, 'i'));
    m.appendChild(item('Autoplay next episode', () => { prefs.set('autoplayNext', !prefs.get('autoplayNext')); this.renderMenu(); }, prefs.get('autoplayNext')));
    m.appendChild(item('Skip intros automatically', () => { prefs.set('autoSkipIntro', !prefs.get('autoSkipIntro')); this.renderMenu(); }, prefs.get('autoSkipIntro')));
    if (this.files.length > 1) {
      m.appendChild(h('h4', null, 'Version'));
      for (const f of this.files) m.appendChild(item(`${f.height ? f.height + 'p ' : ''}${(f.vcodec || '').toUpperCase()} · ${fmtBytes(f.size)}`, () => {
        this.file = f; this.chaptersDrawn = false; this.closeMenu(); this.replan();
      }, this.file.id === f.id));
    }
  }

  // ---------- intro skipping ----------
  intro() { return (this.segments || []).find((s) => s.kind === 'intro'); }

  checkIntro() {
    const seg = this.intro();
    const t = this.video.currentTime;
    const inside = !!seg && this.started && t >= seg.start - 0.5 && t < seg.end - 1.5;
    if (inside && prefs.get('autoSkipIntro') && !this.introSkipped) {
      this.introSkipped = true;
      this.seekTo(seg.end);
      toast('Skipped intro');
      return;
    }
    if (inside && !this.skipBtn) {
      this.skipBtn = h('button', { class: 'btn skip-intro', onclick: (e) => {
        e.stopPropagation();
        this.introSkipped = true;
        this.seekTo(seg.end);
      } }, 'Skip Intro', h('span', { html: icons.next }));
      this.root.appendChild(this.skipBtn);
    } else if (!inside && this.skipBtn) {
      this.skipBtn.remove();
      this.skipBtn = null;
    }
  }

  // ---------- next episode ----------
  checkUpNext() {
    const next = this.detail?.next;
    const d = this.duration(), t = this.video.currentTime;
    if (!next || !d || this.upNextShown || this.video.paused) return;
    const credits = (this.file?.info?.chapters || []).find((c) => /credit|outro|ending|ed\b/i.test(c.title || '') && c.start > d * 0.7);
    const at = credits ? credits.start : d - Math.max(20, prefs.get('countdown') + 5);
    if (t < at) return;
    this.upNextShown = true;
    let n = prefs.get('countdown');
    const count = h('span', null, String(n));
    const card = h('div', { class: 'upnext', onclick: (e) => e.stopPropagation() },
      h('img', { src: img(next, 'thumb', 480), alt: '' }),
      h('div', { class: 'b' },
        h('div', { class: 'small muted' }, 'Up next'),
        h('b', null, `S${next.season} · E${next.episode} — ${next.title}`),
        h('div', { class: 'row' },
          h('button', { class: 'btn primary sm', onclick: () => this.playNext() }, icons.play ? h('span', { html: icons.play, style: { width: '16px', display: 'inline-flex' } }) : null, prefs.get('autoplayNext') ? ['Play in ', count, 's'] : 'Play now'),
          h('button', { class: 'btn sm', onclick: () => { clearInterval(this.cdTimer); card.remove(); } }, 'Hide'))));
    this.root.appendChild(card);
    this.upNextEl = card;
    if (prefs.get('autoplayNext')) {
      this.cdTimer = setInterval(() => {
        if (this.video.paused) return;
        n--;
        count.textContent = String(n);
        if (n <= 0) { clearInterval(this.cdTimer); this.playNext(); }
      }, 1000);
    }
  }

  onEnded() {
    if (this.detail?.next && prefs.get('autoplayNext')) this.playNext();
    else this.showUI(true);
  }

  async playNext() {
    const next = this.detail?.next;
    if (!next) return;
    clearInterval(this.cdTimer);
    if (this.upNextEl) { this.upNextEl.remove(); this.upNextEl = null; }
    this.sendStop();
    this.stopSent = false;
    this.sessionId = null;
    this.upNextShown = false;
    this.chaptersDrawn = false;
    this.segments = [];
    this.introSkipped = false;
    if (this.skipBtn) { this.skipBtn.remove(); this.skipBtn = null; }
    for (const c of [...this.rail.querySelectorAll('.chap, .seg')]) c.remove();
    this.fallbacks = 0;
    this.stalls = { count: 0, secs: 0, since: 0 };
    this.teardown();
    const ud = next.userData;
    await this.start(next.id, ud && ud.position > 0 && !ud.played ? ud.position : 0);
    if (this.onClose) this.onChanged = true;
  }

  setupMediaSession() {
    if (!('mediaSession' in navigator)) return;
    const it = this.item;
    try {
      navigator.mediaSession.metadata = new MediaMetadata({
        title: it.kind === 'episode' ? it.title : it.title,
        artist: it.kind === 'episode' ? `${this.detail.show?.title || ''} · S${it.season}E${it.episode}` : String(it.year || ''),
        artwork: [{ src: img(it, it.kind === 'episode' ? 'thumb' : 'poster', 480), sizes: '480x480', type: 'image/jpeg' }],
      });
      const ms = navigator.mediaSession;
      ms.setActionHandler('play', () => this.video.play());
      ms.setActionHandler('pause', () => this.video.pause());
      ms.setActionHandler('seekbackward', () => this.skip(-prefs.get('skipBack')));
      ms.setActionHandler('seekforward', () => this.skip(prefs.get('skipFwd')));
      ms.setActionHandler('seekto', (d) => this.seekTo(d.seekTime));
      ms.setActionHandler('nexttrack', this.detail.next ? () => this.playNext() : null);
    } catch {}
  }

  close() {
    if (this.closed) return;
    this.closed = true;
    this.sendStop();
    clearInterval(this.hbTimer);
    clearInterval(this.uiTimer);
    clearInterval(this.cdTimer);
    clearTimeout(this.hideTimer);
    document.removeEventListener('keydown', this.onKey);
    document.removeEventListener('fullscreenchange', this.onFs);
    window.removeEventListener('pagehide', this.onHide);
    if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
    this.teardown();
    this.root.remove();
    document.body.style.overflow = '';
    if (current === this) current = null;
    if (this.onClose) this.onClose();
  }
}
