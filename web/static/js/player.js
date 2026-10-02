// Full-screen player: picks a playback plan from the server, plays it via
// direct <video src> or the MSE engine, and renders controls + stats.

import { h, icons, resLabel, fmtTime, fmtBitrate, fmtBytes, streamLabel, toast, releaseToasts, clear, langName, channelName, modal, containTab, METHOD_LABEL, reasonLabel, fmtEpisode } from './ui.js';
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

export function closePlayer() { current?.close(); }

// pauseForOverlay pauses playback while something covers the player (e.g.
// the shortcuts sheet). It returns a function that resumes it, if it was
// playing and the same player is still open.
export function pauseForOverlay() {
  const p = current, v = p?.video;
  if (!v || v.paused) return () => {};
  v.pause();
  return () => { if (current === p && v.paused) v.play().catch(() => {}); };
}

const END_FMT = new Intl.DateTimeFormat(undefined, { hour: 'numeric', minute: '2-digit' });
const DIRECT_STARTUP_TIMEOUT = 15000;
const SPEEDS = [0.5, 0.75, 1, 1.25, 1.5, 1.75, 2];
const DEFAULT_FONT = '/vendor/jassub/default.woff2';
const LANG_CHOICES = ['eng', 'spa', 'fre', 'ger', 'ita', 'por', 'hin', 'jpn', 'kor', 'chi', 'ara', 'rus', 'dut', 'swe', 'nor', 'dan', 'fin', 'pol', 'tur', 'tam', 'tel', 'ukr', 'heb', 'gre', 'cze', 'hun', 'rum', 'tha', 'vie', 'ind', 'may']
  .map((k) => [k, langName(k)]).sort((a, b) => a[1].localeCompare(b[1]));
const LANG1 = { en: 'eng', es: 'spa', fr: 'fre', de: 'ger', it: 'ita', pt: 'por', 'pt-pt': 'por', 'pt-br': 'por', hi: 'hin', ja: 'jpn', ko: 'kor', zh: 'chi', 'zh-cn': 'chi', 'zh-tw': 'chi', ar: 'ara', ru: 'rus', nl: 'dut', sv: 'swe', no: 'nor', da: 'dan', fi: 'fin', pl: 'pol', tr: 'tur', ta: 'tam', te: 'tel', uk: 'ukr', he: 'heb', el: 'gre', cs: 'cze', hu: 'hun', ro: 'rum', th: 'tha', vi: 'vie', id: 'ind', ms: 'may' };
const lang1to3 = (l) => LANG1[(l || '').toLowerCase()] || l;

// Stats panel formatting: durations in whole seconds up to ten minutes, then
// m:ss; positions are always m:ss.
const secs = (s) => (s < 600 ? `${Math.round(s)}s` : fmtTime(s));
const plural = (n, w) => `${n} ${w}${n === 1 ? '' : 's'}`;
// The server says "copy"; the UI says "copied".
const copied = (s) => (s || '').replace(/ \(copy\)$/, ' · copied');

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

// Style names used for signs, songs and effects rather than dialogue.
const ASS_SIGN_STYLE = /sign|^ts\b|title|kara|romaji|kanji|song|lyric|^op\b|^ed\d*\b|opening|ending|screen|insert|note|logo/i;

// Applies the viewer's subtitle size, position and background preferences to
// an ASS script's dialogue styles (signs and karaoke keep the release's look,
// since they're positioned over the picture on purpose).
function styleASS(text) {
  const size = { small: 0.8, medium: 1, large: 1.3 }[prefs.get('subSize')] || 1;
  const pos = prefs.get('subPos');
  const box = prefs.get('subBg');
  const end = text.indexOf('[Events]');
  if (end < 0) return text;
  const head = text.slice(0, end);
  const resY = +(head.match(/^PlayResY:\s*(\d+)/m) || [])[1] || 288;
  let format = null;
  const out = head.split('\n').map((line) => {
    const m = line.match(/^(Format|Style):\s*(.*)$/);
    if (!m) return line;
    const fields = m[2].split(',').map((f) => f.trim());
    if (m[1] === 'Format') { format = fields.map((f) => f.toLowerCase()); return line; }
    if (!format || fields.length < format.length) return line;
    const at = (k) => format.indexOf(k);
    const align = +fields[at('alignment')];
    // Only bottom-aligned (numpad 1-3) dialogue styles.
    if (ASS_SIGN_STYLE.test(fields[at('name')]) || !(align >= 1 && align <= 3)) return line;
    const set = (k, v) => { if (at(k) >= 0) fields[at(k)] = String(v); };
    set('fontsize', Math.round(+fields[at('fontsize')] * size * 10) / 10);
    const mv = +fields[at('marginv')] || Math.round(resY * 0.04);
    set('marginv', Math.round(pos === 'low' ? mv * 0.4 : pos === 'high' ? mv + resY * 0.1 : mv));
    if (box) {
      // BorderStyle 3 draws an opaque box in the outline colour.
      set('borderstyle', 3);
      set('outlinecolour', '&H60000000');
      set('outline', Math.max(1, Math.round(resY / 150)));
      set('shadow', 0);
    }
    return `Style: ${fields.join(',')}`;
  });
  return out.join('\n') + text.slice(end);
}

// Fetches a subtitle track; partial means the server is still extracting it.
async function fetchSub(url) {
  const res = await fetch(url, { credentials: 'same-origin' });
  if (!res.ok) throw new Error((await res.json().catch(() => ({}))).error || `HTTP ${res.status}`);
  return { text: await res.text(), partial: res.headers.get('X-Lex-Partial') === '1' };
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
    this.subOffset = 0;
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
    this.seek = h('div', { class: 'seek', role: 'slider', tabindex: '0', 'aria-label': 'Playback position', 'aria-valuemin': '0', 'aria-valuemax': '0', 'aria-valuenow': '0', 'aria-valuetext': '0:00 of 0:00', 'aria-disabled': 'true' }, h('div', { class: 'rail' }), h('div', { class: 'knob' }));
    this.rail = this.seek.firstChild;
    this.knob = this.seek.lastChild;
    this.fill = h('div', { class: 'fill' });
    this.rail.appendChild(this.fill);
    this.timeEl = h('span', { class: 'p-time' }, '0:00 / 0:00');
    this.endsEl = h('span', { class: 'p-ends', title: 'When playback will finish at the current speed' });
    this.methodEl = h('span', { class: 'p-method hide-mobile', title: 'Playback method (click for stats)', onclick: (e) => { e.stopPropagation(); this.toggleStats(); } });
    this.playBtn = b('play', 'Play (k)', () => this.togglePlay(), 'big');
    this.volBtn = b('volume', 'Mute (m)', () => this.toggleMute());
    this.volRange = h('input', { type: 'range', 'aria-label': 'Volume', min: 0, max: 1, step: 0.05, value: prefs.get('volume'), oninput: (e) => { this.video.volume = +e.target.value; this.video.muted = false; }, onclick: (e) => e.stopPropagation() });
    this.nextBtn = b('next', 'Next episode (n)', () => this.playNext(), 'hidden');
    this.fsBtn = b('fullscreen', 'Fullscreen (f)', () => this.toggleFullscreen());
    this.ccBtn = b('cc', 'Subtitles & audio (c)', (e) => this.toggleMenu('tracks'));
    this.gearBtn = b('gear', 'Settings', () => this.toggleMenu('settings'));
    this.pipBtn = document.pictureInPictureEnabled ? b('pip', 'Picture in picture', () => this.togglePip(), 'hide-mobile') : null;

    this.titleEl.id = 'player-title';
    this.restoreFocus = document.activeElement;
    this.root = h('div', { class: 'player', role: 'dialog', 'aria-modal': 'true', 'aria-labelledby': 'player-title', tabindex: '-1' },
      this.video,
      h('div', { class: 'shade-top' }), h('div', { class: 'shade-bot' }),
      this.subsEl,
      this.center,
      h('div', { class: 'p-top' }, b('back', 'Close (Esc)', () => this.close()), this.titleEl),
      h('div', { class: 'p-bot' },
        this.seek,
        h('div', { class: 'p-controls' },
          h('div', { class: 'p-transport' }, this.playBtn,
            b('back10', `Back ${prefs.get('skipBack')}s (←)`, () => this.skip(-prefs.get('skipBack'))),
            b('fwd30', `Forward ${prefs.get('skipFwd')}s (→)`, () => this.skip(prefs.get('skipFwd'))), this.timeEl, this.endsEl),
          h('div', { class: 'spacer' }),
          h('div', { class: 'p-tools' }, h('div', { class: 'vol' }, this.volBtn, this.volRange), this.methodEl,
            this.nextBtn, this.ccBtn, this.gearBtn, this.pipBtn, this.fsBtn))));
    this.applySubStyle();
    document.body.appendChild(this.root);
    this.background = document.getElementById('app');
    this.backgroundWasInert = this.background?.inert;
    if (this.background) this.background.inert = true;
    // Focus the player itself, not a control: Space must pause, not press a button.
    this.root.focus();
    document.body.style.overflow = 'hidden';
    this.video.volume = prefs.get('volume');
    this.video.muted = prefs.get('muted');
    this.showSpinner(true);
  }

  bind() {
    const v = this.video;
    this.onKey = (e) => this.key(e);
    document.addEventListener('keydown', this.onKey);
    this.onFs = () => {
      const fullscreen = !!document.fullscreenElement;
      this.fsBtn.innerHTML = fullscreen ? icons.exitfs : icons.fullscreen;
      this.labelButton(this.fsBtn, fullscreen ? 'Exit fullscreen (f)' : 'Fullscreen (f)');
    };
    document.addEventListener('fullscreenchange', this.onFs);
    this.onHide = () => this.sendStop(true);
    window.addEventListener('pagehide', this.onHide);

    v.addEventListener('play', () => { this.watchDirectStartup(); this.playBtn.innerHTML = icons.pause; this.labelButton(this.playBtn, 'Pause (k)'); this.poke(); this.beat(); });
    v.addEventListener('pause', () => { if (v.paused) this.startupWatch = null; this.playBtn.innerHTML = icons.play; this.labelButton(this.playBtn, 'Play (k)'); this.showUI(true); this.beat(); });
    v.addEventListener('waiting', () => {
      this.showSpinner(true);
      if (this.started && !v.seeking) { this.stalls.count++; this.stalls.since = performance.now(); }
    });
    const ready = () => {
      this.showSpinner(false);
      if (this.stalls.since) { this.stalls.secs += (performance.now() - this.stalls.since) / 1000; this.stalls.since = 0; }
    };
    v.addEventListener('playing', () => { ready(); this.started = true; this.startupWatch = null; });
    v.addEventListener('canplay', ready);
    v.addEventListener('seeked', () => { ready(); this.beat(); });
    v.addEventListener('seeking', () => this.showSpinner(true));
    v.addEventListener('timeupdate', () => { this.renderTime(); this.checkIntro(); if (this.subTrack) this.renderCues(this.subTrack); });
    v.addEventListener('progress', () => this.renderTime());
    v.addEventListener('volumechange', () => {
      this.volBtn.innerHTML = v.muted || v.volume === 0 ? icons.mute : icons.volume;
      this.labelButton(this.volBtn, v.muted || v.volume === 0 ? 'Unmute (m)' : 'Mute (m)');
      this.volRange.value = v.muted ? 0 : v.volume;
      prefs.set('volume', v.volume); prefs.set('muted', v.muted);
    });
    v.addEventListener('ended', () => this.onEnded());
    v.addEventListener('error', () => this.onVideoError());

    // Controls visibility.
    this.root.addEventListener('mousemove', () => this.poke());
    this.root.addEventListener('focusin', () => this.poke());
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
      if (!this.tip) {
        this.tipTime = h('span');
        this.tipThumb = h('div', { class: 'thumb' });
        this.tip = h('div', { class: 'tip' }, this.tipThumb, this.tipTime);
        this.seek.appendChild(this.tip);
        if (!this.trick && Date.now() - (this.trickTried || 0) > 60000) this.loadTrickplay();
      }
      const chap = [...(this.file?.info?.chapters || [])].reverse().find((c) => c.start <= t && c.title && !/^chapter \d+$/i.test(c.title));
      this.tipTime.textContent = chap ? `${fmtTime(t)} · ${chap.title}` : fmtTime(t);
      const w = this.renderThumb(t);
      // Keep the preview inside the player.
      const half = Math.max(w, this.tip.offsetWidth) / 2;
      const r = this.seek.getBoundingClientRect();
      this.tip.style.left = `${Math.min(Math.max(x, half + 4 - r.left), window.innerWidth - r.left - half - 4)}px`;
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
    this.seek.addEventListener('keydown', (e) => {
      if (e.metaKey || e.ctrlKey || e.altKey || !this.duration()) return;
      const t = this.video.currentTime;
      const targets = { ArrowLeft: t - prefs.get('skipBack'), ArrowDown: t - prefs.get('skipBack'), ArrowRight: t + prefs.get('skipFwd'), ArrowUp: t + prefs.get('skipFwd'), PageDown: t - 60, PageUp: t + 60, Home: 0, End: this.duration() - 0.5 };
      if (!(e.key in targets)) return;
      e.preventDefault();
      e.stopPropagation();
      this.seekTo(targets[e.key]);
      this.renderTime();
    });

    this.hbTimer = setInterval(() => this.beat(), prefs.get('heartbeat') * 1000);
    this.uiTimer = setInterval(() => this.tickUI(), 1000);
  }

  // ---------- loading ----------
  async start(itemId, start) {
    this.bufferRebuilds = 0;
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
      this.subOffset = 0;
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
      this.titleEl.append(h('b', null, this.detail.show?.title || it.showTitle || ''), h('span', null, `${fmtEpisode(it)} — ${it.title}`));
    } else {
      this.titleEl.append(h('b', null, it.title), h('span', null, [it.year, resLabel(this.file?.width, this.file?.height)].filter(Boolean).join(' · ')));
    }
  }

  duration() {
    return this.plan?.duration || this.file?.info?.duration || (isFinite(this.video.duration) ? this.video.duration : 0);
  }

  async loadPlan(start, overrides = {}) {
    const generation = this.planGeneration = (this.planGeneration || 0) + 1;
    this.startupWatch = null;
    this.hideError();
    this.showSpinner(true);
    const caps = detectCaps();
    let mode = overrides.mode || this.mode;
    // "HLS" isn't a server mode: it asks for HLS delivery with automatic
    // direct/remux/transcode choice. Only browsers with native HLS can use it.
    const forceHls = mode === 'hls';
    if (forceHls) {
      mode = 'auto';
      if (!this.video.canPlayType('application/vnd.apple.mpegurl')) {
        toast("This browser can't play HLS natively (Safari and iOS can) — using the normal stream");
        this.mode = 'auto';
        prefs.set('mode', 'auto');
      }
    }
    const req = {
      itemId: this.item.id, fileId: this.file.id, audio: this.audio, subtitle: this.subtitle,
      mode, forceHls: forceHls && this.mode === 'hls', maxBitrate: this.quality, audioLang: prefs.get('audioLang'), swEncode: !!this.swEncode,
      caps, sessionId: this.sessionId, start,
    };
    let res;
    for (let attempt = 0; ; attempt++) {
      if (this.closed || generation !== this.planGeneration) return false;
      try {
        res = await api('/api/playback/plan', { method: 'POST', body: req });
        break;
      } catch (e) {
        if (this.closed || generation !== this.planGeneration) return false;
        // A server restart briefly makes the proxy unavailable. Retrying
        // the same session's plan is safe; permission and media errors stay
        // terminal. Closing or selecting another plan cancels the retry.
        if (![0, 502, 503, 504].includes(e.status) || attempt >= 6) throw e;
        await new Promise(resolve => setTimeout(resolve, Math.min(8000, 1000 * (attempt + 1))));
      }
    }
    if (this.closed || generation !== this.planGeneration) return false;
    this.plan = res.plan;
    this.cached = !!res.cached;
    if (res.segments) this.segments = res.segments;
    this.sessionId = res.plan.sessionId;
    this.audio = res.plan.audio;
    this.file = { ...this.file, ...res.file, subtitles: this.file.subtitles };
    this.methodEl.textContent = METHOD_LABEL[this.plan.method] + (this.plan.hls ? ' · HLS' : '');
    this.methodEl.className = `p-method hide-mobile ${this.plan.method}`;
    this.methodEl.title = (this.plan.reasons || []).join('; ') || 'Playing the original file';
    if (this.trick?.fileId !== this.file.id) this.loadTrickplay();
    await this.attach(start);
    return !this.closed && generation === this.planGeneration;
  }

  teardown() {
    this.startupWatch = null;
    this.attachGeneration = (this.attachGeneration || 0) + 1;
    if (this.engine) { this.engine.destroy(); this.engine = null; }
    const v = this.video;
    v.pause();
    v.removeAttribute('src');
    for (const t of [...v.querySelectorAll('track')]) t.remove();
    try { v.load(); } catch {}
  }

  async attach(start) {
    this.teardown();
    const generation = this.attachGeneration;
    const v = this.video;
    this.started = false;
    this.startPosition = start;
    if (this.plan.hls) {
      // Native HLS (Safari, iOS, AirPlay): the playlist covers the whole file,
      // with segment N starting at N x segment length, so seek like a file.
      v.src = this.plan.url;
      if (start > 0) v.addEventListener('loadedmetadata', () => { v.currentTime = start; }, { once: true });
    } else if (this.plan.method === 'direct') {
      v.src = this.plan.url + (start > 0 ? `#t=${start.toFixed(2)}` : '');
      if (start > 0) {
        v.addEventListener('loadedmetadata', () => { if (Math.abs(v.currentTime - start) > 2) v.currentTime = start; }, { once: true });
      }
    } else {
      this.engine = new MseEngine(v, this.plan, {
        forward: prefs.get('bufferAhead'),
        back: prefs.get('backBuffer'),
        onError: (msg, status, decode, rebuild) => this.onStreamError(msg, status, decode, rebuild),
      });
      await this.engine.open(start);
    }
    if (this.closed || generation !== this.attachGeneration) return;
    this.setSubtitleTrack(this.subtitle);
    this.watchDirectStartup();
    try { await v.play(); } catch (e) {
      // A fallback replaces the source while the old play() is pending.
      // Its eventual AbortError must not change the new source's controls.
      if (this.closed || generation !== this.attachGeneration || this.failedPlan === this.plan) return;
      // A quick pause/play can reject the first play() after the next one
      // has already armed a fresh startup watch on this same source.
      if (e.name === 'AbortError' && !v.paused) return;
      this.startupWatch = null;
      // Autoplay with sound blocked: show paused state, user clicks play.
      this.playBtn.innerHTML = icons.play;
      this.showSpinner(false);
    }
    if (this.closed || generation !== this.attachGeneration) return;
    this.beat();
  }

  async replan(overrides) {
    // Metadata may never arrive for an unsupported native container, so
    // currentTime is still zero even when playback was requested at a resume point.
    const t = this.video.currentTime || (!this.started && this.startPosition) || 0;
    const wasPaused = this.video.paused && this.started;
    const generation = (this.planGeneration || 0) + 1;
    try {
      const applied = await this.loadPlan(t, overrides);
      if (applied && wasPaused) this.video.pause();
    } catch (e) { if (!this.closed && generation === this.planGeneration) this.showError(e.message); }
  }

  // ---------- errors & fallback ----------
  watchDirectStartup() {
    if (this.closed || this.started || !this.plan || this.plan.hls || this.plan.method !== 'direct' || this.failedPlan === this.plan) return;
    this.startupWatch = { plan: this.plan, since: performance.now() };
  }

  checkDirectStartup() {
    const watch = this.startupWatch;
    if (!watch) return;
    if (this.closed || this.started || this.video.paused || this.plan !== watch.plan) {
      this.startupWatch = null;
      return;
    }
    // Some browsers accept the MIME type but never decode the original
    // file or emit a media error. Retry via remux instead of waiting forever.
    if (performance.now() - watch.since >= DIRECT_STARTUP_TIMEOUT) {
      this.startupWatch = null;
      this.fallback('Direct Play did not start within 15 seconds');
    }
  }

  async onVideoError() {
    const err = this.video.error;
    if (!err || err.code === 1 || this.closed || !this.plan) return;
    // MEDIA_ERR_SRC_NOT_SUPPORTED on direct play, or decode errors: fall back.
    await this.fallback(`${this.plan.method} failed (${err.message || 'media error ' + err.code})`);
  }

  async onStreamError(msg, status, decode, rebuild) {
    if (rebuild && !this.bufferRebuilds) {
      if (this.closed || !this.plan || this.failedPlan === this.plan) return;
      this.failedPlan = this.plan;
      this.bufferRebuilds = 1;
      toast('Video buffer stalled — restarting playback');
      return this.replan({ mode: this.plan.method });
    }
    if (decode) return this.fallback(msg);
    this.showError(msg, status !== 503);
  }

  async fallback(reason) {
    // An error event and the startup deadline can report the same failure.
    // Each plan gets one fallback; a newly attached plan can fail separately.
    if (this.closed || !this.plan || this.failedPlan === this.plan) return;
    this.failedPlan = this.plan;
    this.startupWatch = null;
    if (this.plan.hls && this.mode === 'hls') { this.showError(`HLS playback failed: ${reason}`); return; }
    const order = ['direct', 'remux', 'transcode'];
    const idx = order.indexOf(this.plan.method);
    // A hardware-encoded transcode the browser can't parse: retry once with
    // the server's software encoder before giving up.
    if (idx === 2 && /hardware/.test(this.plan.videoOut || '') && !this.swEncode) {
      this.swEncode = true;
      toast('Hardware transcode failed in this browser — retrying with the software encoder');
      await this.replan({ mode: 'transcode' });
      return;
    }
    if (this.fallbacks >= 2 || idx >= 2) { this.showError(`Playback failed: ${reason}`); return; }
    this.fallbacks++;
    const next = order[idx + 1];
    toast(`${METHOD_LABEL[this.plan.method]} didn't work in this browser — switching to ${METHOD_LABEL[next]}`);
    this.mode = next;
    await this.replan({ mode: next });
  }

  showError(msg, retry = true) {
    this.startupWatch = null;
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
    this.destroyASS();
    if (this.subBlob) { URL.revokeObjectURL(this.subBlob); this.subBlob = null; }
    const s = this.subById(idx);
    if (!s || !s.textSub) return;
    // Styled (ASS/SSA) subtitles are rendered with libass so positioning,
    // fonts and karaoke survive; anything else goes through our overlay.
    if (/^(ass|ssa)$/.test(s.codec) && !s.downloaded && prefs.get('assRender') !== false && await this.setASS(s, token)) return;
    if (token !== this.subToken) return;
    const slow = setTimeout(() => { if (token === this.subToken) toast('Extracting subtitles from the file… they will appear shortly'); }, 1500);
    const install = (text) => {
      const old = this.subBlob;
      this.subBlob = URL.createObjectURL(new Blob([text], { type: 'text/vtt' }));
      const track = h('track', { kind: 'subtitles', label: streamLabel(s), srclang: (s.language || 'und').slice(0, 2), src: this.subBlob, default: true });
      for (const t of [...v.querySelectorAll('track')]) t.remove();
      v.appendChild(track);
      const tt = track.track;
      tt.mode = 'hidden';
      const render = () => this.renderCues(tt);
      tt.addEventListener('cuechange', render);
      track.addEventListener('load', () => { this.cueKey = null; render(); });
      this.subTrack = tt;
      if (old) URL.revokeObjectURL(old);
    };
    try {
      const url = `/api/files/${this.file.id}/subs/${idx}.vtt?v=${this.file.mtime || 0}`;
      const { text, partial } = await fetchSub(url);
      if (token !== this.subToken || this.closed) return;
      install(text);
      if (partial) this.pollSub(token, url, install);
    } catch (e) {
      if (token === this.subToken) toast(`Could not load subtitles: ${e.message}`, 'error');
    } finally {
      clearTimeout(slow);
    }
  }

  // The server is still extracting this track (big file, slow disk) and sent
  // what it has so far: keep fetching until it's complete.
  async pollSub(token, url, apply) {
    for (let wait = 3000; ; wait = Math.min(wait * 1.5, 15000)) {
      await new Promise((r) => setTimeout(r, wait));
      if (token !== this.subToken || this.closed) return;
      try {
        const { text, partial } = await fetchSub(url);
        if (token !== this.subToken || this.closed) return;
        await apply(text);
        if (!partial) return;
      } catch (e) {
        if (token === this.subToken) toast(`Could not load subtitles: ${e.message}`, 'error');
        return;
      }
    }
  }

  static assSupported() {
    return typeof Worker !== 'undefined' && typeof OffscreenCanvas !== 'undefined' && typeof WebAssembly !== 'undefined' &&
      'requestVideoFrameCallback' in HTMLVideoElement.prototype;
  }

  async setASS(s, token) {
    if (!Player.assSupported()) return false;
    try {
      const [{ default: JASSUB }, fonts] = await Promise.all([
        import('/vendor/jassub/jassub.js'),
        api(`/api/files/${this.file.id}/fonts`).catch(() => []),
      ]);
      if (token !== this.subToken || this.closed) return true;
      const url = `/api/files/${this.file.id}/subs/${s.index}.ass?v=${this.file.mtime || 0}`;
      const { text, partial } = await fetchSub(url);
      this.assRaw = text;
      const subContent = styleASS(text);
      if (token !== this.subToken || this.closed) return true;
      this.jassub = new JASSUB({
        video: this.video, subContent, fonts,
        workerUrl: '/vendor/jassub/worker.js',
        wasmUrl: '/vendor/jassub/jassub-worker.wasm',
        modernWasmUrl: '/vendor/jassub/jassub-worker-modern.wasm',
        // Liberation Sans is metric-compatible with Arial, the usual ASS default.
        availableFonts: { 'liberation sans': DEFAULT_FONT, arial: DEFAULT_FONT, helvetica: DEFAULT_FONT, 'arial unicode ms': DEFAULT_FONT },
        queryFonts: false, // querying the OS font list can stall rendering for seconds
        timeOffset: -this.subOffset,
        prescaleHeightLimit: 1080, maxRenderHeight: 1440,
      });
      await this.jassub.ready;
      if (partial) {
        const j = this.jassub;
        this.pollSub(token, url, async (text) => {
          if (this.jassub !== j) return;
          this.assRaw = text;
          await j.renderer.setTrack(styleASS(text));
          if (this.video.paused && j._lastDemandTime) j.manualRender(j._lastDemandTime, true).catch(() => {});
        });
      }
      return true;
    } catch (e) {
      console.warn('ASS renderer unavailable, using plain subtitles', e);
      this.destroyASS();
      return false;
    }
  }

  destroyASS() {
    if (this.jassub) { this.jassub.destroy().catch(() => {}); this.jassub = null; }
  }

  // Positive offset = subtitles appear later.
  setSubOffset(v) {
    this.subOffset = Math.max(-600, Math.min(600, Math.round(v * 10) / 10));
    if (this.jassub) {
      this.jassub.timeOffset = -this.subOffset;
      if (this.jassub._lastDemandTime) this.jassub.manualRender(this.jassub._lastDemandTime, true).catch(() => {});
    }
    this.cueKey = null;
    this.renderCues(this.subTrack);
    if (this.menu && this.menuName === 'tracks' && !this.menuPage) this.renderMenu();
  }

  nudgeSubs(d) {
    if (!this.subById(this.subtitle)?.textSub) return;
    this.setSubOffset(this.subOffset + d);
    toast(`Subtitle timing ${this.subOffset > 0 ? '+' : ''}${this.subOffset.toFixed(1)}s`, '', { key: 'subOffset' });
  }

  // Find subtitles on OpenSubtitles.com and add them to this file.
  searchSubtitles() {
    const wasPlaying = !this.video.paused;
    const langSel = h('select', { class: 'input' }, LANG_CHOICES.map(([k, l]) => h('option', { value: k, selected: k === (prefs.get('subLang') || 'eng') }, l)));
    const list = h('div', { class: 'sub-results' });
    const status = h('div', { class: 'muted small' });
    const downloaded = h('div', { class: 'sub-results' });
    const renderDownloaded = () => {
      clear(downloaded);
      const mine = (this.file.subtitles || []).filter((x) => x.downloaded);
      if (!mine.length) return;
      downloaded.append(h('div', { class: 'small muted', style: { margin: '2px 0 4px' } }, 'Downloaded for this file'));
      for (const d of mine) {
        downloaded.append(h('div', { class: 'sub-res' },
          h('div', { class: 'sub-res-main' }, h('b', null, d.title || 'Downloaded'), h('span', { class: 'muted small' }, langName(d.language))),
          h('button', { class: 'btn sm danger', onclick: async () => {
            try {
              await api(`/api/files/${this.file.id}/subs/${d.index}`, { method: 'DELETE' });
              this.file.subtitles = this.file.subtitles.filter((x) => x.index !== d.index);
              this.syncFileSubs();
              if (this.subtitle === d.index) this.chooseSubtitle(-1);
              renderDownloaded();
            } catch (e) { toast(e.message, 'error'); }
          } }, 'Remove')));
      }
    };
    const search = async () => {
      clear(list);
      status.textContent = 'Searching OpenSubtitles…';
      try {
        const res = await api(`/api/files/${this.file.id}/subsearch?lang=${encodeURIComponent(langSel.value)}`);
        status.textContent = res.length ? `${res.length} result${res.length === 1 ? '' : 's'} · exact-release matches first` : 'No subtitles found for this language.';
        for (const r of res.slice(0, 40)) {
          const tags = [
            r.hashMatch ? h('span', { class: 'tag ok' }, 'Exact match') : null,
            r.trusted ? h('span', { class: 'tag' }, 'Trusted') : null,
            r.hearingImpaired ? h('span', { class: 'tag' }, 'SDH') : null,
            r.ai ? h('span', { class: 'tag warn' }, 'Machine translated') : null,
          ];
          const btn = h('button', { class: 'btn sm primary' }, 'Use');
          btn.onclick = async () => {
            btn.disabled = true; btn.textContent = 'Downloading…';
            try {
              const st = await api(`/api/files/${this.file.id}/subsearch`, { method: 'POST', body: { fileId: r.fileId, language: r.language, release: r.release || r.fileName, hearingImpaired: r.hearingImpaired } });
              this.file.subtitles = [...(this.file.subtitles || []), st];
              this.syncFileSubs();
              m.close();
              await this.chooseSubtitle(st.index);
              toast('Subtitles added');
            } catch (e) {
              toast(e.message, 'error');
              btn.disabled = false; btn.textContent = 'Use';
            }
          };
          list.append(h('div', { class: 'sub-res' },
            h('div', { class: 'sub-res-main' },
              h('b', { title: r.fileName }, r.release || r.fileName || 'Untitled'),
              h('div', { class: 'sub-tags' }, langName(lang1to3(r.language)), ` · ${r.downloads.toLocaleString()} downloads`, ...tags)),
            btn));
        }
      } catch (e) {
        status.textContent = '';
        list.append(h('div', { class: 'sub-err' }, e.message));
      }
    };
    langSel.onchange = () => { prefs.set('subLang', langSel.value); search(); };
    if (wasPlaying) this.video.pause();
    const m = modal({
      title: 'Search subtitles', wide: true, parent: this.root,
      body: [h('div', { class: 'row', style: { gap: '10px', alignItems: 'center' } }, h('span', { class: 'muted' }, 'Language'), langSel, h('div', { class: 'spacer' }), status), downloaded, list],
      onClose: () => { if (wasPlaying) this.video.play().catch(() => {}); },
    });
    renderDownloaded();
    search();
  }

  // Keep the item's file list in sync so switching versions keeps new subs.
  syncFileSubs() {
    const f = (this.files || []).find((x) => x.id === this.file.id);
    if (f) f.subtitles = this.file.subtitles;
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
    // Styled subtitles: apply the same preferences to the dialogue styles.
    const j = this.jassub;
    if (j && this.assRaw) {
      clearTimeout(this.assRestyle);
      this.assRestyle = setTimeout(async () => {
        if (this.jassub !== j) return;
        await j.renderer.setTrack(styleASS(this.assRaw));
        if (j._lastDemandTime) j.manualRender(j._lastDemandTime, true).catch(() => {});
      }, 50);
    }
  }

  // Called on cuechange and every timeupdate: Chrome doesn't always fire
  // cuechange when playback starts right at a cue boundary.
  renderCues(tt) {
    const el = this.subsEl;
    if (!el) return;
    const off = this.subOffset || 0;
    const t = this.video.currentTime - off;
    const src = off ? tt?.cues : tt?.activeCues;
    const cues = src ? [...src].filter((c) => c.startTime <= t + 0.05 && c.endTime > t).sort((a, b) => a.startTime - b.startTime) : [];
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
  labelButton(button, label) {
    button.title = label;
    button.setAttribute('aria-label', label);
  }

  toggleMute() {
    const v = this.video;
    if (v.muted || v.volume === 0) { v.muted = false; if (v.volume === 0) v.volume = 1; }
    else v.muted = true;
  }

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
    this.hideTimer = setTimeout(() => { if (!this.video.paused && !this.menu && !this.root.querySelector(':focus-visible')) this.showUI(false); }, 3000);
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
    if (e.defaultPrevented || this.root.querySelector('.modal-bg')) return;
    if (e.key === 'Tab') {
      containTab(e, this.root);
      this.poke();
      return;
    }
    if (e.target.tagName === 'INPUT' && e.target.type === 'range') {
      // A slider keeps its own keys; the rest stay shortcuts after it was dragged.
      if (/^(Arrow|Home|End|Page)/.test(e.key)) return;
    } else if ((/^(INPUT|SELECT|TEXTAREA)$/.test(e.target.tagName) || e.target.isContentEditable) && e.key !== 'Escape') return;
    // Only a button reached by keyboard takes Space/Enter; after a mouse click Space still pauses.
    if (e.target.closest('button')?.matches(':focus-visible') && (e.key === ' ' || e.key === 'Enter')) return;
    if (e.metaKey || e.ctrlKey || e.altKey) return;
    const v = this.video;
    const k = e.key.toLowerCase();
    const map = {
      ' ': () => this.togglePlay(), k: () => this.togglePlay(),
      arrowleft: () => this.skip(-prefs.get('skipBack')), j: () => this.skip(-prefs.get('skipBack')),
      arrowright: () => this.skip(prefs.get('skipFwd')), l: () => this.skip(prefs.get('skipFwd')),
      arrowup: () => { v.volume = Math.min(1, v.volume + 0.05); v.muted = false; },
      arrowdown: () => { v.volume = Math.max(0, v.volume - 0.05); },
      f: () => this.toggleFullscreen(), m: () => this.toggleMute(),
      i: () => this.toggleStats(), c: () => this.toggleMenu('tracks'),
      n: () => this.playNext(),
      g: () => this.nudgeSubs(-0.1), h: () => this.nudgeSubs(0.1),
      escape: () => { if (this.menu) this.closeMenu(); else if (!document.fullscreenElement) this.close(); },
      home: () => this.seekTo(0), end: () => this.seekTo(this.duration() - 5),
    };
    if (/^[0-9]$/.test(k)) { this.seekTo((this.duration() * +k) / 10); e.preventDefault(); return; }
    if (map[k]) { e.preventDefault(); map[k](); this.poke(); }
  }

  // ---------- trickplay (seek previews) ----------
  async loadTrickplay() {
    const fileId = this.file?.id;
    this.trickTried = Date.now();
    this.trick = null;
    try {
      const m = await api(`/api/files/${fileId}/trickplay`);
      if (this.file?.id !== fileId || !m || !m.count) return;
      this.trick = { ...m, fileId };
      // Warm the first sheet so the first hover is instant.
      new Image().src = `/api/files/${fileId}/trickplay/1`;
    } catch {}
  }

  // Draws the preview for time t; returns its displayed width (0 if none).
  renderThumb(t) {
    const m = this.trick, el = this.tipThumb;
    if (!m || m.fileId !== this.file?.id) { el.style.display = 'none'; return 0; }
    const per = m.cols * m.rows;
    const i = Math.max(0, Math.min(m.count - 1, Math.floor(t / m.interval)));
    const sheet = Math.floor(i / per) + 1, k = i % per;
    const scale = Math.min(1, (window.innerWidth < 600 ? 160 : 240) / m.width);
    const w = Math.round(m.width * scale), ht = Math.round(m.height * scale);
    Object.assign(el.style, {
      display: 'block', width: `${w}px`, height: `${ht}px`,
      backgroundImage: `url(/api/files/${m.fileId}/trickplay/${sheet})`,
      backgroundSize: `${m.cols * w}px auto`,
      backgroundPosition: `-${(k % m.cols) * w}px -${Math.floor(k / m.cols) * ht}px`,
    });
    return w;
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
    this.renderEnds();
    this.seek.setAttribute('aria-valuemax', String(Math.max(0, Math.floor(d || 0))));
    this.seek.setAttribute('aria-valuenow', String(Math.max(0, Math.min(Math.floor(t || 0), Math.floor(d || 0)))));
    this.seek.setAttribute('aria-valuetext', `${fmtTime(t)} of ${fmtTime(d)}`);
    this.seek.setAttribute('aria-disabled', String(!d));
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

  // Wall-clock finish time. Paused, it slides forward with the clock, so the
  // UI tick refreshes it too.
  renderEnds() {
    const d = this.duration(), left = d - (this.dragT ?? this.video.currentTime);
    const txt = isFinite(d) && d && left > 0 ? `Ends at: ${END_FMT.format(Date.now() + (left / (this.video.playbackRate || 1)) * 1000)}` : '';
    if (this.endsEl.textContent !== txt) this.endsEl.textContent = txt;
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
    this.checkDirectStartup();
    const es = this.clientStats();
    this.bufHist.push(es.bufferAhead);
    if (this.bufHist.length > 90) this.bufHist.shift();
    if (this.statsEl) this.renderStats(es);
    this.renderEnds();
    this.checkUpNext();
  }

  toggleStats() {
    if (this.statsEl) { this.statsEl.remove(); this.statsEl = null; prefs.set('showStats', false); return; }
    if (!this.plan) return;
    prefs.set('showStats', true);
    const stop = (on) => (e) => { e.stopPropagation(); on(e); };
    this.statsDot = h('span', { class: 'ps-dot' });
    this.statsSum = h('span', { class: 'ps-sum' });
    this.statsCanvas = h('canvas');
    this.statsBody = h('div', { class: 'ps-body' });
    const compact = !!prefs.get('statsCompact');
    const toggle = h('button', { class: 'ps-toggle', title: 'Collapse / expand', 'aria-expanded': String(!compact), onclick: stop(() => {
      const c = this.statsEl.classList.toggle('compact');
      toggle.setAttribute('aria-expanded', String(!c));
      prefs.set('statsCompact', c);
    }) }, this.statsDot, this.statsSum);
    this.statsEl = h('div', { class: `p-stats${compact ? ' compact' : ''}`, onclick: (e) => e.stopPropagation() },
      h('div', { class: 'ps-head' }, toggle,
        h('button', { class: 'ps-btn', title: 'Copy stats as text', onclick: stop(() => this.copyStats()) }, 'Copy'),
        h('button', { class: 'ps-btn ps-close', title: 'Close (I)', 'aria-label': 'Close stats (I)', html: icons.close, onclick: stop(() => this.toggleStats()) })),
      h('div', { class: 'ps-graph' }, h('div', { class: 'ps-cap' }, 'Buffer · last 90s'), this.statsCanvas),
      this.statsBody);
    this.root.appendChild(this.statsEl);
    this.renderStats(this.clientStats());
    this.beat();
  }

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
    const sec = (title) => { rows = []; sections.push({ title, rows }); };
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
    const lvls = [bufLvl, dropLvl, jobLvl];
    const lvl = lvls.includes('bad') ? 'bad' : lvls.includes('warn') ? 'warn' : '';
    const status = cs.buffering ? 'Buffering' : lvl === 'bad' ? 'Error' : lvl === 'warn' ? 'Degraded' : 'Healthy';
    const summary = `${status} · ${secs(cs.bufferAhead)} buffered · ${plural(cs.bufferEvents, 'stall')} · ${cs.droppedFrames} dropped`;

    sec('Playback');
    r('Method', method, p.remote ? 'remote' : 'local');
    if (p.reasons?.length) { const why = p.reasons.map(reasonLabel).join('; '); r('Why', why, '', '', why); }
    r('Position', `${fmtTime(t)} / ${fmtTime(d)}`, v.playbackRate !== 1 ? `${v.playbackRate}x` : '');
    const seg = this.intro();
    if (seg) r('Intro', `${fmtTime(seg.start)}–${fmtTime(seg.end)}`, seg.source);
    r('Container', `${(f.name || '').split('.').pop()} → ${p.hls ? 'HLS' : p.method === 'direct' ? 'original file' : 'fragmented MP4'}`, p.hls ? p.mime : p.method === 'direct' ? 'range requests' : 'MSE');

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
    } else {
      r('Ahead', secs(cs.bufferAhead), 'browser managed', bufLvl);
    }
    r('Server rate', fmtBitrate(this.serverRate * 8));
    r('Stalls', String(cs.bufferEvents), `${cs.bufferSeconds.toFixed(1)}s total${cs.buffering ? ' · buffering now' : ''}`, cs.buffering ? 'bad' : cs.bufferEvents ? 'warn' : '');
    r('Read from', this.cached ? 'SSD cache' : 'library disk');
    r('Source bitrate', fmtBitrate(info.bitrate || 0));
    if (p.method === 'transcode') r('Target bitrate', fmtBitrate(p.bitrate * 1000));
    if (p.limitKbps) r('Bitrate limit', fmtBitrate(p.limitKbps * 1000));

    sec('Video');
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
    if (as) r('Source', `${as.codec.toUpperCase()} ${channelName(as.channels)}`, [as.sampleRate ? as.sampleRate / 1000 + 'kHz' : '', langName(as.language)].filter(Boolean).join(' · '));
    r('Output', copied(p.audioOut) || 'none');

    if (j) {
      sec('Server (ffmpeg)');
      r('State', j.exited ? (j.error ? `exited: ${j.error}` : 'finished') : j.throttled ? 'throttled' : 'running', j.throttled ? 'waiting for client' : '', j.error ? 'bad' : '', j.error || '');
      r('Speed', `${(j.speed || 0).toFixed(j.speed >= 10 ? 0 : 1)}x`, `${Math.round(j.fps || 0)} fps · CPU ${Math.round(j.cpu || 0)}%`, jobLvl);
      r('Ready to', fmtTime(done), `${secs(Math.max(0, done - t))} ahead`);
    }

    sec('Session');
    r('ID', p.sessionId.slice(0, 8), es ? plural(es.restarts, 'restart') : '', '', p.sessionId);
    return { lvl, summary, sections, es };
  }

  renderStats(cs) {
    if (!this.plan) return;
    const m = this.statsModel(cs);
    this.statsDot.className = `ps-dot ${m.lvl}`;
    this.statsSum.textContent = m.summary;
    const rows = [];
    for (const s of m.sections) {
      rows.push(h('tr', null, h('td', { class: 'hd', colspan: 2 }, s.title)));
      for (const row of s.rows) {
        const text = row.sub ? `${row.v} · ${row.sub}` : row.v;
        rows.push(h('tr', null, h('td', null, row.k),
          h('td', { class: row.lvl || null, title: row.full || text }, row.v, row.sub ? h('span', { class: 'sub' }, ` ${row.sub}`) : null)));
      }
    }
    clear(this.statsBody).appendChild(h('table', null, h('colgroup', null, h('col', { class: 'k' }), h('col')), rows));
    this.drawBufGraph(m.es);
  }

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
      ctx.strokeStyle = '#f2b33d'; ctx.lineWidth = 1.5; ctx.stroke();
      ctx.lineTo(x(hist.length - 1), ht); ctx.lineTo(x(0), ht); ctx.closePath();
      ctx.fillStyle = 'rgba(242,179,61,.12)'; ctx.fill();
    }
    ctx.font = '10px ui-monospace, Menlo, monospace'; ctx.fillStyle = 'rgba(255,255,255,.75)'; ctx.textAlign = 'right';
    ctx.fillText(target && target <= top ? `target ${secs(target)}` : `max ${secs(top)}`, w - 2, 10);
  }

  async copyStats() {
    const m = this.statsModel(this.clientStats());
    const lines = [`Lex playback stats · ${[...this.titleEl.children].map((c) => c.textContent).join(' · ')} · ${new Date().toISOString()}`, m.summary];
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
  }

  // ---------- menus ----------
  // Two-level menus: a main list of "Label  value ›" rows and toggles; a row
  // opens a sub-page of choices with a back button.
  closeMenu() { if (this.menu) { this.menu.remove(); this.menu = null; this.menuName = null; this.menuPage = null; } }

  toggleMenu(name) {
    if (this.menuName === name) { this.closeMenu(); return; }
    this.closeMenu();
    this.menuName = name;
    this.menuPage = null;
    this.menu = h('div', { class: 'p-menu', onclick: (e) => e.stopPropagation() });
    this.renderMenu();
    this.root.appendChild(this.menu);
    this.showUI(true);
  }

  openPage(page) { this.menuPage = page; this.renderMenu(); }

  qualityLabel(k) {
    const q = QUALITIES.find(([v]) => v === k);
    return q ? q[1].split(' · ')[0] : 'Original';
  }

  renderMenu() {
    const m = clear(this.menu);
    const row = (label, value, page) => h('button', { class: 'pm-row', onclick: () => this.openPage(page) },
      h('span', { class: 'pm-label' }, label), h('span', { class: 'pm-val' }, value), h('span', { class: 'pm-chev', html: icons.chevR }));
    const sw = (label, on, set, hint) => h('label', { class: 'pm-row pm-toggle' },
      h('span', { class: 'pm-label' }, label, hint ? h('small', null, hint) : null),
      h('span', { class: 'switch' }, h('input', { type: 'checkbox', checked: on, onchange: (e) => set(e.target.checked) }), h('i')));
    const page = (title, items) => {
      m.append(h('div', { class: 'pm-head' },
        h('button', { class: 'pm-back', html: icons.chevL, 'aria-label': 'Back', onclick: () => this.openPage(null) }), h('b', null, title)));
      const list = h('div', { class: 'pm-list' });
      for (const it of items) {
        list.appendChild(h('button', { class: `pm-opt ${it.active ? 'on' : ''}`, onclick: it.on },
          h('span', { class: 'ck', html: it.active ? icons.check : '' }),
          h('span', { class: 'pm-opt-l' }, it.label, it.sub ? h('small', null, it.sub) : null),
          it.right ? h('span', { class: 'pm-val' }, it.right) : null));
      }
      m.append(list);
    };
    const auds = (this.file.info?.streams || []).filter((s) => s.type === 'audio');
    const subs = this.file.subtitles || [];

    if (this.menuName === 'tracks') {
      switch (this.menuPage) {
        case 'subs':
          return page('Subtitles', [
            { label: 'Off', active: this.subtitle < 0, on: () => { this.chooseSubtitle(-1); this.closeMenu(); } },
            ...subs.map((s) => ({ label: streamLabel(s), active: this.subtitle === s.index, on: () => { this.chooseSubtitle(s.index); this.closeMenu(); } })),
            { label: 'Search online…', sub: 'OpenSubtitles.com', on: () => { this.closeMenu(); this.searchSubtitles(); } },
          ]);
        case 'audio':
          return page('Audio', auds.map((a) => ({ label: streamLabel(a), active: this.audio === a.index, on: () => { if (a.index !== this.audio) this.chooseAudio(a.index); this.closeMenu(); } })));
        case 'size':
          return page('Subtitle size', [['small', 'Small'], ['medium', 'Medium'], ['large', 'Large']].map(([k, l]) => ({ label: l, active: prefs.get('subSize') === k, on: () => { prefs.set('subSize', k); this.applySubStyle(); this.openPage(null); } })));
        case 'pos':
          return page('Subtitle position', [['low', 'Low'], ['normal', 'Normal'], ['high', 'High']].map(([k, l]) => ({ label: l, active: prefs.get('subPos') === k, on: () => { prefs.set('subPos', k); this.applySubStyle(); this.openPage(null); } })));
      }
      const cur = this.subById(this.subtitle);
      m.append(h('div', { class: 'pm-title' }, 'Subtitles & audio'));
      m.append(row('Subtitles', cur ? streamLabel(cur).split(' · ')[0] : 'Off', 'subs'));
      if (auds.length) m.append(row('Audio', streamLabel(auds.find((a) => a.index === this.audio) || auds[0]).split(' · ').slice(0, 2).join(' · '), 'audio'));
      if (cur && cur.textSub) {
        const off = this.subOffset || 0;
        const step = (d) => h('button', { class: 'pm-step', onclick: () => this.setSubOffset(Math.round((off + d) * 10) / 10) }, d > 0 ? `+${d}` : `${d}`);
        m.append(h('div', { class: 'pm-row pm-stepper' },
          h('span', { class: 'pm-label' }, 'Timing', h('small', null, 'keys G / H')),
          h('div', { class: 'pm-steps' }, step(-0.5), step(-0.1),
            h('button', { class: 'pm-step pm-zero', title: 'Reset', onclick: () => this.setSubOffset(0) }, `${off > 0 ? '+' : ''}${off.toFixed(1)}s`),
            step(0.1), step(0.5))));
      }
      m.append(h('div', { class: 'pm-sep' }));
      m.append(row('Size', { small: 'Small', medium: 'Medium', large: 'Large' }[prefs.get('subSize')] || 'Medium', 'size'));
      m.append(row('Position', { low: 'Low', normal: 'Normal', high: 'High' }[prefs.get('subPos')] || 'Normal', 'pos'));
      m.append(sw('Background box', prefs.get('subBg'), (v) => { prefs.set('subBg', v); this.applySubStyle(); }));
      return;
    }

    // settings
    const src = this.file?.info?.bitrate ? Math.round(this.file.info.bitrate / 1000) : 0;
    const modes = [['auto', 'Automatic', 'Direct play, then remux, then transcode'], ['direct', 'Direct play', 'Original file'], ['remux', 'Direct stream', 'Remux; video untouched'], ['transcode', 'Transcode', 'Re-encode on the server'], ['hls', 'HLS', 'For AirPlay and older devices']];
    switch (this.menuPage) {
      case 'quality':
        return page('Quality', QUALITIES.filter(([k]) => !k || !src || k <= src * 1.5 || k === this.quality).map(([k, l]) => ({
          label: l, active: this.quality === k, right: k === 0 && src ? fmtBitrate(src * 1000) : '',
          on: () => { this.quality = k; prefs.set('quality', k); this.closeMenu(); this.replan(); },
        })));
      case 'method':
        return page('Playback method', modes.map(([k, l, d]) => ({ label: l, sub: d, active: this.mode === k, on: () => { this.mode = k; prefs.set('mode', k); this.fallbacks = 0; this.swEncode = false; this.closeMenu(); this.replan(); } })));
      case 'speed':
        return page('Speed', SPEEDS.map((sp) => ({ label: sp === 1 ? 'Normal' : `${sp}x`, active: this.video.playbackRate === sp, on: () => { this.video.playbackRate = sp; this.openPage(null); } })));
      case 'version':
        return page('Version', this.files.map((f) => ({ label: `${resLabel(f.width, f.height)} ${(f.vcodec || '').toUpperCase()}`, right: fmtBytes(f.size), active: this.file.id === f.id, on: () => { this.file = { ...f, subtitles: f.subtitles }; this.chaptersDrawn = false; this.closeMenu(); this.replan(); } })));
    }
    m.append(h('div', { class: 'pm-title' }, 'Settings'));
    m.append(row('Quality', this.qualityLabel(this.quality), 'quality'));
    m.append(row('Playback', (modes.find(([k]) => k === this.mode) || modes[0])[1], 'method'));
    m.append(row('Speed', this.video.playbackRate === 1 ? 'Normal' : `${this.video.playbackRate}x`, 'speed'));
    if (this.files.length > 1) m.append(row('Version', `${resLabel(this.file.width, this.file.height)} ${(this.file.vcodec || '').toUpperCase()}`, 'version'));
    m.append(h('div', { class: 'pm-sep' }));
    m.append(sw('Stats for nerds', !!this.statsEl, () => this.toggleStats(), 'Shortcut: I'));
    m.append(sw('Autoplay next episode', prefs.get('autoplayNext'), (v) => prefs.set('autoplayNext', v)));
    m.append(sw('Skip intros automatically', prefs.get('autoSkipIntro'), (v) => prefs.set('autoSkipIntro', v)));
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
        h('b', null, `${fmtEpisode(next)} — ${next.title}`),
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
    // Fallbacks (remux/transcode, software encoder) were for the last file;
    // the next one starts from the user's chosen method again.
    this.fallbacks = 0;
    this.mode = prefs.get('mode');
    this.swEncode = false;
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
        artist: it.kind === 'episode' ? `${this.detail.show?.title || ''} · ${fmtEpisode(it)}` : String(it.year || ''),
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
    this.destroyASS();
    this.root.remove();
    releaseToasts(this.root);
    if (this.background?.isConnected) this.background.inert = this.backgroundWasInert;
    if (this.restoreFocus?.isConnected) this.restoreFocus.focus();
    document.body.style.overflow = '';
    if (current === this) current = null;
    if (this.onClose) this.onClose();
  }
}
