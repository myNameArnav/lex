// Full-screen player: picks a playback plan from the server, plays it via
// direct <video src> or the MSE engine, and renders controls + stats.
// Self-contained pieces live in player/: features installed as Player
// methods (subtitles.js, audio.js, menus.js, stats.js, live-tv.js,
// up-next.js, skip-segments.js, chapters.js, trickplay.js) and plain
// helpers (tracks.js, format.js, retry.js).

import { h, icons, resLabel, fmtTime, fmtLeft, toast, releaseToasts, clear, containTab, METHOD_LABEL, reasonLabel, fmtEpisode, fmtProgram, fmtClock } from './ui.js';
import { api, img } from './api.js';
import { state } from './app.js';
import { detectCaps } from './caps.js';
import { prefs } from './prefs.js';
import { MseEngine, clock } from './mse.js';
import { bufferingCause } from './buffering.js';
import { AudioFX } from './audio-fx.js';
import { trackKey, trackPrefs } from './track-memory.js';
import { orderFiles, chosenFile } from './versions.js';
import { END_FMT, skipIcon } from './player/format.js';
import { pickAudio, pickSubtitle } from './player/tracks.js';
import { retryApi } from './player/retry.js';
import { liveTv } from './player/live-tv.js';
import { upNext } from './player/up-next.js';
import { skipSegments } from './player/skip-segments.js';
import { chapters } from './player/chapters.js';
import { trickplay } from './player/trickplay.js';
import { subtitles } from './player/subtitles.js';
import { audio } from './player/audio.js';
import { stats } from './player/stats.js';
import { menus } from './player/menus.js';

let current = null;

// Firefox on macOS only starts HEVC at an IDR frame, and open-GOP encodes
// have one only at 0:00, so it can't play them from anywhere else
// (mse.js: DISCARD_BYTES). Without a transcode there's no way around it.
const DISCARDED_HELP = 'This browser can only play this video from the beginning (Firefox on Mac can’t start this kind of HEVC partway through). Play it from the start, use Safari or Chrome, or ask an admin to turn transcoding on.';

export function openPlayer(opts) {
  // Replacing an open player keeps its history entry, so a pending back()
  // can't pop the new one.
  const reuseHistory = !!current;
  if (current) current.close({ keepHistory: true });
  current = new Player({ ...opts, reuseHistory });
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

const DIRECT_STARTUP_TIMEOUT = 15000;
// Failures to blame on the browser's remuxer rather than the codecs: its own
// errors, Chrome rejecting its output as unparseable, and the gaps Firefox
// leaves when it drops fragments it can't parse (it reports nothing else).
const REMUX_FAULT = /Browser remux|CHUNK_DEMUXER_ERROR_APPEND_FAILED|unplayable gap/;
const remuxFault = (reason, err) => REMUX_FAULT.test(`${reason} ${err?.message || ''}`);

export class Player {
  // shuffle: episodes to play in this order, from the first. channel: a Live
  // TV channel id to tune into, and channels the list to switch through.
  // extra: the file id of one of the item's local extras (a trailer...) to
  // play instead of the item; it starts from the top and saves no progress.
  constructor({ itemId, start = null, onClose, reuseHistory = false, shuffle = null, channel = null, channels = [], extra = null }) {
    this.onClose = onClose;
    this.extra = extra;
    this.shuffle = shuffle?.length ? { items: shuffle, pos: 0 } : null;
    this.live = channel ? { channelId: channel, channels } : null;
    this.sessionId = null;
    this.engine = null;
    this.plan = null;
    this.mode = prefs.get('mode');
    this.quality = prefs.get('quality');
    this.fallbacks = 0;
    this.stalls = { count: 0, secs: 0, since: 0 };
    this.lastJob = null;
    this.serverRate = 0;
    this.diskWait = 0;
    this.host = [];
    this.load = null;
    this.started = false;
    this.bufHist = [];
    this.closed = false;
    this.upNextShown = false;
    this.subOffset = 0;
    this.build();
    // Back (browser, Android gesture) closes the player instead of
    // navigating the page behind it. Same URL, so no hashchange or route.
    if (!reuseHistory) history.pushState({ ...history.state, lexPlayer: true }, '');
    this.bind();
    if (this.live) this.tune(channel);
    else this.start(this.shuffle ? this.shuffle.items[0].id : itemId, start);
  }

  // ---------- DOM ----------
  build() {
    // name: an icon name, or the icon's markup.
    const b = (name, title, on, cls = '') => h('button', { class: `pbtn ${cls}`, title, 'aria-label': title, html: icons[name] || name, onclick: (e) => { e.stopPropagation(); on(e); } });
    this.video = h('video', { playsinline: true, preload: 'auto' });
    this.fx = new AudioFX(this.video);
    // Firefox (no PiP API, so no Lex PiP button) draws its own hover PiP toggle
    // on the right edge, under the right-anchored menus, and it swallows their
    // clicks. It only takes effect if set before the first hover.
    if (!('pictureInPictureEnabled' in document)) this.video.disablePictureInPicture = true;
    this.titleEl = h('div', { class: 'p-title' });
    this.center = h('div', { class: 'p-center' });
    this.statusEl = h('div', { class: 'p-status', role: 'status' });
    // Subtitles are drawn by us (not ::cue) so we control position and size.
    // Not a live region: a screen reader would read every line over the dialogue.
    this.subsEl = h('div', { class: 'p-subs', 'aria-live': 'off' });
    this.pgsCanvas = h('canvas', { class: 'p-pgs', 'aria-hidden': 'true' });
    this.seek = h('div', { class: 'seek', role: 'slider', tabindex: '0', 'aria-label': 'Playback position', 'aria-valuemin': '0', 'aria-valuemax': '0', 'aria-valuenow': '0', 'aria-valuetext': '0:00 of 0:00', 'aria-disabled': 'true' }, h('div', { class: 'rail' }), h('div', { class: 'knob' }));
    this.rail = /** @type {HTMLElement} */ (this.seek.firstChild);
    this.knob = /** @type {HTMLElement} */ (this.seek.lastChild);
    this.fill = h('div', { class: 'fill' });
    this.rail.appendChild(this.fill);
    // Current time / duration; clicking the duration swaps it for the time left (as VLC does).
    this.nowEl = h('span', null, '0:00');
    this.endEl = h('span', null, '0:00');
    this.endLabel = h('span', { class: 'sr-only' });
    this.endBtn = h('button', { class: 'p-end', type: 'button', onclick: (e) => { e.stopPropagation(); prefs.set('timeLeft', !prefs.get('timeLeft')); this.renderTime(); } }, this.endLabel, this.endEl);
    this.timeEl = h('span', { class: 'p-time' }, h('span', { class: 'sr-only' }, 'Current time '), this.nowEl, h('span', { 'aria-hidden': 'true' }, ' / '), this.endBtn);
    this.endsEl = h('span', { class: 'p-ends', title: 'When playback will finish at the current speed' });
    this.chapEl = h('button', { class: 'p-chap hide-mobile', type: 'button', title: 'Chapters (PgUp / PgDn)', onclick: (e) => { e.stopPropagation(); this.toggleChapters(); } });
    this.methodEl = h('button', { class: 'p-method hide-mobile', type: 'button', title: 'Playback method (click for stats)', 'aria-pressed': 'false', onclick: (e) => { e.stopPropagation(); this.toggleStats(); } });
    this.playBtn = b('play', 'Play (k)', () => this.togglePlay(), 'big');
    this.volBtn = b('volume', 'Mute (m)', () => this.toggleMute());
    this.volRange = h('input', { type: 'range', 'aria-label': 'Volume', min: 0, max: 1, step: 0.05, value: prefs.get('volume'), oninput: (e) => { this.video.volume = +e.target.value; this.video.muted = false; }, onclick: (e) => e.stopPropagation() });
    this.nextBtn = b('next', this.live ? 'Next channel (n)' : 'Next episode (n)', () => this.playNext(), 'hidden');
    // Live TV: LIVE while at the live point, Go live once behind it.
    this.liveBtn = this.live ? h('button', { class: 'p-live', type: 'button', title: 'Back to what’s on now', onclick: (e) => { e.stopPropagation(); this.goLive(); } }, 'Live') : null;
    this.chanBtn = this.live ? h('button', { class: 'p-chans', type: 'button', title: 'Channels (t)', 'aria-haspopup': 'dialog', 'aria-expanded': 'false', onclick: (e) => { e.stopPropagation(); this.toggleMenu('channels'); } }, 'Channels') : null;
    this.fsBtn = b('fullscreen', 'Fullscreen (f)', () => this.toggleFullscreen());
    // A live stream has one audio track and no subtitles: no menu for them (CSS hides it).
    this.ccBtn = b('cc', 'Subtitles & audio (c)', (e) => this.toggleMenu('tracks'), 'p-cc');
    this.gearBtn = b('gear', 'Settings', () => this.toggleMenu('settings'));
    for (const btn of [this.ccBtn, this.gearBtn]) { btn.setAttribute('aria-haspopup', 'dialog'); btn.setAttribute('aria-expanded', 'false'); }
    this.pipBtn = document.pictureInPictureEnabled ? b('pip', 'Picture in picture', () => this.togglePip(), 'hide-mobile') : null;

    this.center.appendChild(this.statusEl);
    this.titleEl.id = 'player-title';
    this.restoreFocus = /** @type {HTMLElement | null} */ (document.activeElement);
    this.root = h('div', { class: `player${this.live ? ' p-tv' : ''}`, role: 'dialog', 'aria-modal': 'true', 'aria-labelledby': 'player-title', tabindex: '-1' },
      this.video, this.pgsCanvas,
      h('div', { class: 'shade-top' }), h('div', { class: 'shade-bot' }),
      this.subsEl,
      this.center,
      h('div', { class: 'p-top' }, b('back', 'Close (Esc)', () => this.close()), this.titleEl, this.liveBtn, this.chanBtn),
      h('div', { class: 'p-bot' },
        this.seek,
        h('div', { class: 'p-controls' },
          h('div', { class: 'p-transport' }, this.playBtn,
            b(skipIcon(-1, prefs.get('skipBack')), `Back ${prefs.get('skipBack')}s (←)`, () => this.skip(-prefs.get('skipBack'))),
            b(skipIcon(1, prefs.get('skipFwd')), `Forward ${prefs.get('skipFwd')}s (→)`, () => this.skip(prefs.get('skipFwd'))), this.timeEl, this.endsEl, this.chapEl),
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
    this.volRange.setAttribute('aria-valuetext', this.volumeText());
    this.showSpinner(true);
  }

  volumeText() { const v = this.video; return v.muted ? 'Muted' : `${Math.round(v.volume * 100)}%`; }

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
    this.onPop = () => this.close({ fromHistory: true });
    window.addEventListener('popstate', this.onPop);

    // Media keys, the lock screen or PiP can still call play() after an
    // admin Stop: the stop sticks.
    v.addEventListener('play', () => { if (this.stopped) { v.pause(); return; } this.applyFx(); this.removeEndCard(); this.watchDirectStartup(); this.playBtn.innerHTML = icons.pause; this.labelButton(this.playBtn, 'Pause (k)'); this.poke(); this.beat(); });
    v.addEventListener('pause', () => { if (v.paused) this.startupWatch = null; this.playBtn.innerHTML = icons.play; this.labelButton(this.playBtn, 'Play (k)'); this.showUI(true); this.beat(); });
    v.addEventListener('waiting', () => {
      this.showSpinner(true);
      if (!this.waitingSince) this.waitingSince = performance.now();
      if (this.started && !v.seeking) { this.stalls.count++; this.stalls.since = performance.now(); }
    });
    const ready = () => {
      if (this.waitLogged) {
        this.waitLogged = false;
        if (this.waitingSince) this.logEvent('resumed', `playing at ${clock(v.currentTime)} after ${((performance.now() - this.waitingSince) / 1000).toFixed(0)}s of buffering`);
      }
      this.showSpinner(false);
      this.waitingSince = 0;
      this.setStatus('');
      if (this.stalls.since) { this.stalls.secs += (performance.now() - this.stalls.since) / 1000; this.stalls.since = 0; }
    };
    v.addEventListener('playing', () => {
      ready(); this.started = true; this.startupWatch = null;
      // The element reconnected on its own while the recovery plan is still
      // retrying: keep this source; applying the plan would seek back.
      if (this.recovering && this.planPending === this.planGeneration) { this.planGeneration++; this.recovering = false; }
    });
    v.addEventListener('canplay', ready);
    v.addEventListener('seeked', () => {
      ready(); this.beat();
      // Seeking back before the Up next point: the card can come back later.
      if (this.upNextShown && v.currentTime < this.upNextAt) { this.cancelUpNext(false); this.upNextShown = false; }
    });
    v.addEventListener('seeking', () => { this.removeEndCard(); this.showSpinner(true); if (!this.waitingSince) this.waitingSince = performance.now(); });
    v.addEventListener('timeupdate', () => { this.renderTime(); this.checkSkip(); if (this.subTrack) this.renderCues(this.subTrack); });
    v.addEventListener('progress', () => this.renderTime());
    v.addEventListener('volumechange', () => {
      this.volBtn.innerHTML = v.muted || v.volume === 0 ? icons.mute : icons.volume;
      this.labelButton(this.volBtn, v.muted || v.volume === 0 ? 'Unmute (m)' : 'Mute (m)');
      this.volRange.value = String(v.muted ? 0 : v.volume);
      this.volRange.setAttribute('aria-valuetext', this.volumeText());
      prefs.set('volume', v.volume); prefs.set('muted', v.muted);
    });
    // Firefox fires 'waiting' at the very end too.
    v.addEventListener('ended', () => { ready(); this.onEnded(); });
    v.addEventListener('error', () => this.onVideoError());
    // iPhone fullscreen is the native player, which can't see our subtitle overlay.
    v.addEventListener('webkitbeginfullscreen', () => this.onNativeFs(true));
    v.addEventListener('webkitendfullscreen', () => this.onNativeFs(false));
    v.addEventListener('enterpictureinpicture', () => this.onPip(true));
    v.addEventListener('leavepictureinpicture', () => this.onPip(false));

    // Controls visibility. Only a real mouse move shows them: Android sends
    // a compatibility mousemove before every tap's click, which would show
    // the controls just before the tap toggles them hidden again.
    this.root.addEventListener('pointermove', (e) => { if (e.pointerType === 'mouse') this.poke(); });
    // Whether the controls were up when the tap began: tapping the video can
    // move focus (Firefox focuses it), and focusin shows them before the click.
    this.root.addEventListener('pointerdown', (e) => { this.lastPointer = e.pointerType; this.hiddenAtDown = this.root.classList.contains('hide-ui'); }, true);
    this.root.addEventListener('focusin', () => this.poke());
    // A mouse resting on the controls (e.g. reading the seek preview) keeps them up.
    for (const el of this.root.querySelectorAll(':scope > .p-top, :scope > .p-bot')) {
      el.addEventListener('pointerenter', (e) => { if (e.pointerType === 'mouse') this.overControls = true; });
      el.addEventListener('pointerleave', () => { if (this.overControls) { this.overControls = false; this.poke(); } });
    }
    this.root.addEventListener('click', (e) => {
      if (e.target === v || e.target === this.center) {
        // The second tap of a double-tap seek (if the browser still sends its
        // click): keep the controls the seek revealed.
        if (performance.now() < (this.skipClickUntil || 0)) { this.skipClickUntil = 0; return; }
        if (this.menu) { this.closeMenu(); return; }
        // A tap (touch or pen, also on touch laptops) toggles the controls.
        if (this.lastPointer && this.lastPointer !== 'mouse') { this.toggleUI(this.hiddenAtDown); return; }
        this.togglePlay();
      }
    });
    v.addEventListener('dblclick', () => this.toggleFullscreen());
    let lastTap = 0;
    v.addEventListener('touchend', (e) => {
      const now = Date.now();
      if (now - lastTap < 300 && !this.live) {
        const x = e.changedTouches[0].clientX / window.innerWidth;
        if (x < 0.35) this.skip(-prefs.get('skipBack'), true);
        else if (x > 0.65) this.skip(prefs.get('skipFwd'), true);
        e.preventDefault();
        this.skipClickUntil = performance.now() + 500;
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
      const chaps = this.chapters(), chap = chaps[this.chapterAt(t, chaps)];
      this.tipTime.textContent = chap?.named ? `${fmtTime(t)} · ${chap.title}` : fmtTime(t);
      const w = this.renderThumb(t);
      // Keep the preview inside the player.
      const half = Math.max(w, this.tip.offsetWidth) / 2;
      const r = this.seek.getBoundingClientRect();
      this.tip.style.left = `${Math.min(Math.max(x, half + 4 - r.left), window.innerWidth - r.left - half - 4)}px`;
      return t;
    };
    // Live TV plays on the clock: its bar shows where the program is, but
    // can't be moved.
    this.seek.addEventListener('pointerdown', (e) => {
      if (this.live) return;
      dragging = true;
      this.seek.setPointerCapture(e.pointerId);
      this.seek.classList.add('drag');
      this.dragT = showTip(e);
      this.renderTime();
      this.poke();
    });
    this.seek.addEventListener('pointermove', (e) => {
      if (this.live) return;
      const t = showTip(e);
      if (dragging) { this.dragT = t; this.renderTime(); this.poke(); }
    });
    this.seek.addEventListener('pointerup', (e) => {
      if (!dragging) return;
      dragging = false;
      this.seek.classList.remove('drag');
      const t = seekAt(e).t;
      this.dragT = null;
      this.seekTo(t); // pokes: the hide countdown restarts after release
    });
    this.seek.addEventListener('pointerleave', () => { if (!dragging && this.tip) { this.tip.remove(); this.tip = null; } });
    this.seek.addEventListener('click', (e) => e.stopPropagation());
    this.seek.addEventListener('keydown', (e) => {
      if (e.metaKey || e.ctrlKey || e.altKey || !this.duration() || this.live) return;
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
    // Retry repeats this when the item itself didn't load (also the next episode's).
    this.itemId = itemId;
    this.requestedStart = start;
    this.bufferRebuilds = 0;
    this.upNextDismissed = false;
    // A newer start (e.g. switching channels again) supersedes this one.
    const gen = this.startGen = (this.startGen || 0) + 1;
    const stale = () => this.closed || gen !== this.startGen;
    try {
      const d = await retryApi(`/api/items/${itemId}${this.extra ? `?extra=${this.extra}` : ''}`, undefined, {
        cancelled: stale,
        onRetry: (n) => this.setStatus(`Reconnecting to the server… (attempt ${n + 1})`),
      });
      if (stale()) return;
      // The queue decides what plays next: the shuffle order, or (Live TV)
      // the schedule, which plays on by itself.
      if (this.shuffle) d.next = this.shuffle.items[this.shuffle.pos + 1] || null;
      if (this.live) d.next = null;
      this.detail = d;
      this.item = d.item;
      this.segments = d.segments || [];
      this.files = orderFiles(d.files || []);
      this.file = chosenFile(this.item.id, this.files);
      if (!this.file) throw new Error('No playable file for this item');
      // Tracks chosen earlier for this show (or movie), else the preferences.
      this.trackKey = trackKey(this.item, !!this.live);
      const tp = this.tracks = trackPrefs(this.trackKey, (k) => prefs.get(k));
      const aud = pickAudio(this.file, tp.audioLang);
      this.audio = aud === -1 ? -1 : aud.index;
      this.subtitle = pickSubtitle(this.file, aud, tp);
      this.subOffset = tp.subOffset || 0;
      this.renderTitle();
      this.nextBtn.classList.toggle('hidden', !d.next && !(this.live?.channels.length > 1));
      if (start == null && this.extra) start = 0;
      if (start == null) {
        const ud = this.item.userData;
        start = ud?.position > 0 ? ud.position : 0;
      }
      // Retry resumes here even if this first plan fails before attach().
      this.started = false;
      this.startPosition = start;
      await this.loadPlan(start);
      this.setupMediaSession();
      if (prefs.get('showStats') && !this.statsEl) this.toggleStats();
    } catch (e) {
      if (!stale()) this.planFailed(e);
    }
  }

  renderTitle() {
    const it = this.item;
    clear(this.titleEl);
    if (this.live) {
      const { channel, next } = this.live;
      this.titleEl.append(h('b', null, fmtProgram(it)),
        h('span', null, [`${channel.number} · ${channel.name}`, next ? `Next at ${fmtClock(next.start)}: ${fmtProgram(next.item)}` : ''].filter(Boolean).join(' · ')));
    } else if (this.detail.extra) {
      this.titleEl.append(h('b', null, it.title), h('span', null, this.detail.extra.title));
    } else if (it.kind === 'episode') {
      this.titleEl.append(h('b', null, this.detail.show?.title || it.showTitle || ''), h('span', null, `${fmtEpisode(it)} · ${it.title}${this.shuffle ? ' · Shuffle' : ''}`));
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
    let caps = detectCaps();
    // Browser audio conversion failed for this file: the server converts.
    if (this.noClientAudio) caps = { ...caps, clientAudio: [] };
    // So did remuxing it in the browser: the server remuxes.
    if (this.noClientRemux) caps = { ...caps, clientRemux: [] };
    // Drawing PGS failed, or burning in was asked for in Settings.
    if (this.noPgs || prefs.get('burnImageSubs')) caps = { ...caps, pgs: false };
    let mode = overrides.mode || this.mode;
    // "HLS" isn't a server mode: it asks for HLS delivery with automatic
    // direct/remux/transcode choice. Only browsers with native HLS can use it.
    const forceHls = mode === 'hls';
    if (forceHls) {
      mode = 'auto';
      if (!this.nativeHls()) {
        toast('This browser can’t play HLS natively (Safari and iOS can). Using the normal stream');
        this.mode = 'auto';
        prefs.set('mode', 'auto');
      }
    }
    const req = {
      itemId: this.item.id, fileId: this.file.id, audio: this.audio, subtitle: this.subtitle,
      mode, forceHls: forceHls && this.mode === 'hls', maxBitrate: this.quality, audioLang: this.tracks?.audioLang ?? prefs.get('audioLang'), swEncode: !!this.swEncode, voiceBoost: !!prefs.get('voiceBoost'),
      caps, sessionId: this.sessionId, start, live: !!this.live,
    };
    // Retrying the same session's plan through a server restart is safe.
    // Closing or selecting another plan cancels the retry.
    const cancelled = () => this.closed || generation !== this.planGeneration;
    let res;
    this.planPending = generation;
    try {
      res = await retryApi('/api/playback/plan', { method: 'POST', body: req }, {
        cancelled, retries: overrides.retries ?? 6,
        onRetry: (n) => this.setStatus(`${this.recovering ? 'Connection lost. Reconnecting' : 'Reconnecting to the server'}… (attempt ${n + 1})`),
      });
    } catch (e) {
      if (cancelled()) return false;
      throw e;
    } finally {
      if (this.planPending === generation) this.planPending = null;
    }
    if (cancelled()) return false;
    this.setStatus('');
    this.plan = res.plan;
    this.cached = !!res.cached;
    if (res.segments) this.segments = res.segments;
    this.sessionId = res.plan.sessionId;
    this.audio = res.plan.audio;
    this.file = { ...this.file, ...res.file, subtitles: this.file.subtitles };
    const label = METHOD_LABEL[this.plan.method] + (this.plan.hls ? ' · HLS' : '');
    this.methodEl.textContent = label;
    this.methodEl.className = `p-method hide-mobile ${this.plan.method}`;
    this.methodEl.title = (this.plan.reasons || []).map(reasonLabel).join('; ') || 'Playing the original file';
    this.methodEl.setAttribute('aria-label', `Playback method: ${label}. Stats for nerds (i)`);
    if (this.trick?.fileId !== this.file.id) this.loadTrickplay();
    await this.attach(start);
    return !this.closed && generation === this.planGeneration;
  }

  // Asked once: Firefox logs a console warning for every 'no' answer.
  nativeHls() { return (this.hlsOK ??= !!this.video.canPlayType('application/vnd.apple.mpegurl')); }

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
      if (start > 0) v.addEventListener('loadedmetadata', () => { if (generation === this.attachGeneration) v.currentTime = start; }, { once: true });
    } else if (this.plan.method === 'direct') {
      v.src = this.plan.url + (start > 0 ? `#t=${start.toFixed(2)}` : '');
      if (start > 0) {
        // A source torn down before its metadata leaves this listener on
        // the element: it mustn't seek the next one (e.g. the next episode).
        v.addEventListener('loadedmetadata', () => { if (generation === this.attachGeneration && Math.abs(v.currentTime - start) > 2) v.currentTime = start; }, { once: true });
      }
    } else {
      this.engine = new MseEngine(v, this.plan, {
        forward: prefs.get('bufferAhead'),
        back: prefs.get('backBuffer'),
        onError: (msg, status, decode, rebuild) => this.onStreamError(msg, status, decode, rebuild),
        onEvent: (event, detail) => this.logEvent(event, detail),
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

  async replan(overrides = {}) {
    // Metadata may never arrive for an unsupported native container, so
    // currentTime is still zero even when playback was requested at a resume point.
    const t = this.video.currentTime || (!this.started && this.startPosition) || 0;
    // overrides.resume: an error paused the element, not the viewer.
    const wasPaused = this.video.paused && this.started && !overrides.resume;
    const generation = (this.planGeneration || 0) + 1;
    try {
      const applied = await this.loadPlan(t, overrides);
      if (applied && wasPaused) this.video.pause();
    } catch (e) { if (!this.closed && generation === this.planGeneration) this.planFailed(e); }
  }

  // A plan (or the item) couldn't be loaded.
  planFailed(e) {
    if (e.status === 410) this.stopByServer(e.message);
    // MSE refused the stream's type (addSourceBuffer): a codec problem, so
    // fall back as for a decode error.
    else if (e.name === 'NotSupportedError' && this.plan) this.fallback(`The browser rejected the stream (${e.message})`);
    // The browser threw away the video after a seek and the transcode it
    // fell back to is off: say what happened instead of the server's refusal.
    else if (this.discarded && e.status === 422 && /transcoding is disabled/.test(e.message)) {
      this.showError(DISCARDED_HELP, { title: 'Can’t play from here in this browser', retry: false });
    } else this.showError(e.message);
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
    if (this.live?.stream) { if (err && err.code !== 1) this.streamFailed(); return; }
    if (!err || err.code === 1 || this.closed || !this.plan || this.stopped) return;
    // The server went away mid-file: that says nothing about the codec.
    if (err.code === 2) return this.recoverNetwork(); // MEDIA_ERR_NETWORK
    // A stopped session's media requests fail too; the heartbeat says why.
    await this.beat();
    if (this.closed || this.stopped) return;
    // MEDIA_ERR_SRC_NOT_SUPPORTED on direct play, or decode errors: fall back.
    await this.fallback(`${this.plan.method} failed (${err.message || 'media error ' + err.code})`);
  }

  // Lost the connection to the server (restart, Wi-Fi): say so and replan the
  // same method at the same position, retrying for a minute or so. Doesn't
  // count as a failed plan, so no codec fallback.
  async recoverNetwork() {
    if (this.closed || this.stopped || this.recovering || !this.plan || this.errEl) return;
    this.recovering = true;
    this.startupWatch = null;
    this.setStatus('Connection lost. Reconnecting…');
    try {
      await this.replan({ mode: this.plan.hls ? this.mode : this.plan.method, resume: true, retries: 12 });
    } finally {
      this.recovering = false;
    }
  }

  // Direct/HLS playback waiting on data: a quick check tells a server outage
  // (reconnect now) from a slow network (just keep waiting).
  async probeServer() {
    this.lastProbe = performance.now();
    try {
      await api('/api/public/info');
    } catch (e) {
      if (e.name !== 'AbortError' && [0, 502, 503, 504].includes(e.status) && this.waitingSince) this.recoverNetwork();
    }
  }

  checkWaiting() {
    if (!this.waitingSince || this.closed || this.errEl || this.recovering || this.video.ended) return;
    const waited = performance.now() - this.waitingSince;
    // Spinning for a long time: put what the player sees in the server log.
    if (waited >= 20000 && !this.waitLogged) {
      this.waitLogged = true;
      const cause = this.bufferCause();
      this.logEvent('waiting', `${(waited / 1000).toFixed(0)}s at ${clock(this.video.currentTime)}, ${this.bufferSummary()}${cause ? ` — ${cause.text}` : ''}`);
    }
    if (this.engine?.retries > 0 && waited >= 1000) this.setStatus('Connection lost. Reconnecting…');
    else if (waited >= 3000 && (!this.statusEl.textContent || this.statusEl.dataset.cause)) {
      // Say why, once there's a likely cause; otherwise just that it's slow.
      const cause = this.started ? this.bufferCause() : null;
      if (cause) {
        this.setStatus(`${cause.text} ${cause.hint}`.trim());
        this.statusEl.dataset.cause = '1';
        // Stalling again for the same reason: say it where it outlasts the spinner.
        if (this.stalls.count >= 2 && !this.causeToasted) {
          this.causeToasted = true;
          toast(`Buffering: ${cause.text} ${cause.hint}`.trim(), '', { key: 'buffer-cause' });
        }
      } else if (waited >= 8000) this.setStatus('Still buffering…');
    }
    // The MSE engine retries its own stream.
    if (waited >= 1000 && !this.engine && this.plan && performance.now() - (this.lastProbe || 0) > 5000) this.probeServer();
  }

  // logEvent puts a player event (seek, stall, fallback, error) in the
  // server log next to the session's play/ended lines.
  logEvent(event, detail = '') {
    if (!this.sessionId || this.closed) return;
    api('/api/playback/event', { method: 'POST', body: { sessionId: this.sessionId, event, detail: String(detail).slice(0, 500) } }).catch(() => {});
  }

  bufferSummary() {
    if (this.engine) return `buffer ${this.engine.describe()}`;
    const b = this.video.buffered, r = [];
    for (let i = 0; i < b.length; i++) r.push(`${clock(b.start(i))}-${clock(b.end(i))}`);
    return `buffer [${r.join(' ') || 'empty'}], readyState ${this.video.readyState}, network ${this.video.networkState}`;
  }

  setStatus(text) {
    if (!this.statusEl) return;
    delete this.statusEl.dataset.cause;
    if (this.statusEl.textContent !== text) this.statusEl.textContent = text;
  }

  // How fast data arrives, in bits/s: the stream download, else the
  // browser remuxer's file reads, else what the server measures sending.
  arrivalRate() {
    const es = this.engine ? this.engine.stats() : null;
    return es?.bandwidth || (es?.fileRate ? es.fileRate * 8 : 0) || this.serverRate * 8;
  }

  // The likely reason playback is buffering (buffering.js), or null.
  bufferCause() {
    if (!this.plan) return null;
    return bufferingCause({
      needKbps: this.plan.bitrate, bandwidth: this.arrivalRate(), remote: !!this.plan.remote, job: this.lastJob, rate: this.video.playbackRate || 1,
      canTranscode: state.caps?.transcode !== false, isAdmin: !!state.me?.isAdmin, method: this.plan.method, diskWait: this.diskWait, host: this.host,
    });
  }

  // An admin stopped this session from the dashboard: stay stopped (no
  // replan, retry or fallback) and say why. Playing the title again starts
  // a new session.
  stopByServer(msg) {
    if (this.closed || this.stopped) return;
    this.stopped = true;
    this.planGeneration = (this.planGeneration || 0) + 1; // cancels pending plan retries
    this.cancelUpNext(false);
    // Keep the last frame and position on screen; just stop streaming.
    this.startupWatch = null;
    this.video.pause();
    if (this.engine) { this.engine.destroy(); this.engine = null; }
    this.clearMediaSession();
    this.showError(msg || 'Playback was stopped by the server admin', { title: 'Playback stopped', retry: false });
  }

  async onStreamError(msg, status, decode, rebuild) {
    if (status === 410) return this.stopByServer(msg);
    if (this.stopped) return;
    if (rebuild && !this.bufferRebuilds) {
      if (this.closed || !this.plan || this.failedPlan === this.plan) return;
      this.logEvent('rebuild', `${msg} — restarting the pipeline once`);
      this.failedPlan = this.plan;
      this.bufferRebuilds = 1;
      toast('Video buffer stalled. Restarting playback');
      return this.replan({ mode: this.plan.method });
    }
    if (decode) return this.fallback(msg);
    // 500: the server couldn't produce this stream (e.g. a remux), which a transcode may fix.
    this.showError(msg, { retry: status !== 503, transcode: status === 500 });
  }

  async fallback(reason) {
    // An error event and the startup deadline can report the same failure.
    // Each plan gets one fallback; a newly attached plan can fail separately.
    if (this.closed || !this.plan || this.failedPlan === this.plan || this.stopped) return;
    this.failedPlan = this.plan;
    this.startupWatch = null;
    if (reason.startsWith('The browser discarded the video')) this.discarded = true;
    const via = [this.plan.clientRemux && 'browser remux', this.plan.clientAudio && 'browser audio'].filter(Boolean).join(', ');
    this.logEvent('fallback', `${this.plan.method}${this.plan.hls ? ' (HLS)' : ''}${via ? ` (${via})` : ''} at ${clock(this.video.currentTime)}: ${reason}${this.video.error ? ` [media error ${this.video.error.code}: ${this.video.error.message || ''}]` : ''}`);
    if (this.plan.hls && this.mode === 'hls') { this.showError(`HLS playback failed: ${reason}`, { transcode: true }); return; }
    const order = ['direct', 'remux', 'transcode'];
    const idx = order.indexOf(this.plan.method);
    // Converting the audio in the browser: when the converter itself failed
    // (client-audio.js errors say so), or nothing is left to fall back to,
    // let the server convert it with the same method. Other failures are
    // usually the video (e.g. 10-bit HEVC the browser claims but can't
    // decode); the next method still converts the audio here.
    const serverAudio = () => {
      this.noClientAudio = true;
      toast('Couldn’t convert the audio in this browser. The server will convert it');
      return this.replan({ mode: this.mode, resume: true });
    };
    const clientAudio = this.plan.clientAudio && !this.noClientAudio;
    if (clientAudio && reason.includes('Browser audio decoding')) return serverAudio();
    // Remuxing in the browser: when the remuxer failed, or the browser
    // couldn't parse what it wrote, the server remuxes with the same
    // method. A video the browser can't decode still goes to transcode.
    if (this.plan.clientRemux && !this.noClientRemux && remuxFault(reason, this.video.error)) {
      this.noClientRemux = true;
      toast('Couldn’t remux this file in the browser. The server will remux it');
      return this.replan({ mode: this.mode, resume: true });
    }
    // A hardware-encoded transcode the browser can't parse: retry once with
    // the server's software encoder before giving up.
    if (idx === 2 && /hardware/.test(this.plan.videoOut || '') && !this.swEncode) {
      this.swEncode = true;
      toast('Hardware transcode failed in this browser. Retrying with the software encoder');
      await this.replan({ mode: 'transcode', resume: true });
      return;
    }
    if (this.fallbacks >= 2 || idx >= 2) {
      if (clientAudio) return serverAudio();
      this.showError(`Playback failed: ${reason}`, { transcode: true });
      return;
    }
    this.fallbacks++;
    const next = order[idx + 1];
    toast(`${METHOD_LABEL[this.plan.method]} didn’t work in this browser. Switching to ${METHOD_LABEL[next]}`);
    this.mode = next;
    // Media errors pause the element; that isn't the viewer pausing.
    await this.replan({ mode: next, resume: true });
  }

  // The error overlay is a modal alert over the player: the controls behind
  // it are inert and Tab stays inside. transcode offers 'Try transcoding',
  // only for failures a transcode can fix (codec/container, not network,
  // missing files or a stopped session).
  showError(msg, { retry = true, transcode = false, title = 'Can’t play this' } = {}) {
    if (this.plan) this.logEvent('error', `${title}: ${msg}`);
    this.startupWatch = null;
    this.waitingSince = 0;
    this.waitLogged = false;
    this.showSpinner(false);
    this.setStatus('');
    this.hideError();
    this.video.pause();
    this.errEl = h('div', { class: 'p-error', role: 'alertdialog', 'aria-modal': 'true', 'aria-labelledby': 'p-err-title', 'aria-describedby': 'p-err-msg' }, h('div', null,
      h('h2', { id: 'p-err-title', style: { margin: 0 } }, title),
      h('p', { id: 'p-err-msg', class: 'muted', style: { margin: 0 } }, msg),
      h('div', { class: 'row wrap' },
        retry ? h('button', { class: 'btn primary', onclick: () => this.retry() }, 'Try again') : null,
        transcode && this.item && this.plan && this.plan.method !== 'transcode' ? h('button', { class: 'btn', onclick: () => { this.hideError(); this.mode = 'transcode'; this.replan({ mode: 'transcode', resume: true }); } }, 'Try transcoding') : null,
        h('button', { class: 'btn', onclick: () => this.close() }, 'Close'))));
    this.root.appendChild(this.errEl);
    for (const el of this.root.querySelectorAll(':scope > .p-top, :scope > .p-bot')) el.inert = true;
    this.closeMenu();
    this.showUI(true);
    this.errEl.querySelector('button').focus();
  }

  // The error paused the video; Retry means play again. The item itself (or
  // the next episode) may never have loaded.
  retry() {
    this.hideError();
    // Live TV rejoins what's on now.
    if (this.live) { this.resetForNext(); this.tune(this.live.channelId); return; }
    if (this.item && String(this.item.id) === String(this.itemId)) this.replan({ resume: true });
    else this.start(this.itemId, this.requestedStart);
  }

  hideError() {
    if (!this.errEl) return;
    const hadFocus = this.errEl.contains(document.activeElement);
    this.errEl.remove();
    this.errEl = null;
    for (const el of this.root.querySelectorAll(':scope > .p-top, :scope > .p-bot')) el.inert = false;
    // Retry removed the focused button: keep focus in the player.
    if (hadFocus || document.activeElement === document.body) this.root.focus();
  }

  // Leave the player for an app page: the player's history entry is popped
  // first, or that Back would undo the navigation.
  goTo(hash) {
    if (history.state?.lexPlayer) window.addEventListener('popstate', () => { location.hash = hash; }, { once: true });
    else setTimeout(() => { location.hash = hash; });
    this.close();
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

  // show: on-screen '+30s' / '−10s' (keys and double-taps; the buttons are their own feedback).
  skip(d, show = false) {
    if (this.live) return;
    this.seekTo(this.video.currentTime + d);
    if (!show) return;
    // Repeated presses or taps add up in one bubble.
    const o = this.osdSkip;
    const sum = o && o.el.isConnected && Math.sign(o.sum) === Math.sign(d) ? o.sum + d : d;
    const el = this.osd(sum < 0 ? `−${-sum}s` : `+${sum}s`, sum < 0 ? 'left' : 'right');
    this.osdSkip = { el, sum };
  }

  // seekTo moves to t (s). Not on Live TV, which only follows the clock
  // (goLive sets the position itself).
  seekTo(t) {
    if (this.live) return;
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

  // A short on-screen message (volume, skips) that fades like flash().
  osd(text, side = 'center') {
    this.osdEl?.remove();
    clearTimeout(this.osdTimer);
    const el = this.osdEl = h('div', { class: `p-flash p-osd ${side}` }, text);
    this.center.appendChild(el);
    this.osdTimer = setTimeout(() => el.remove(), 900);
    return el;
  }

  osdVolume() { this.osd(this.video.muted ? 'Muted' : `Volume ${this.volumeText()}`); }

  showSpinner(on) {
    if (on && !this.spin) { this.spin = h('div', { class: 'spinner' }); this.center.appendChild(this.spin); }
    if (!on && this.spin) { this.spin.remove(); this.spin = null; }
  }

  poke() {
    this.showUI(true);
    clearTimeout(this.hideTimer);
    this.hideTimer = setTimeout(() => {
      if (!this.video.paused && !this.menu && !this.overControls && !this.seek.classList.contains('drag') && !this.root.querySelector(':focus-visible')) this.showUI(false);
    }, 3000);
  }

  showUI(on) { this.root.classList.toggle('hide-ui', !on); }
  toggleUI(hidden = this.root.classList.contains('hide-ui')) { if (hidden) this.poke(); else this.showUI(false); }

  toggleFullscreen() {
    if (document.fullscreenElement) document.exitFullscreen();
    else if (this.root.requestFullscreen) this.root.requestFullscreen().catch(() => {});
    else if (this.video.webkitEnterFullscreen) this.video.webkitEnterFullscreen();
  }

  onPip(on) {
    this.root.classList.toggle('pip', on);
    if (this.pipBtn) {
      this.labelButton(this.pipBtn, on ? 'Exit picture in picture' : 'Picture in picture');
      this.pipBtn.setAttribute('aria-pressed', String(on));
    }
    this.pipMsg?.remove();
    this.pipMsg = on ? h('div', { class: 'p-pipmsg' }, 'Playing in picture-in-picture') : null;
    if (on) this.center.appendChild(this.pipMsg);
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
      containTab(e, this.errEl || this.root);
      this.poke();
      return;
    }
    // The error overlay's buttons take every key; Escape still closes.
    if (this.errEl) {
      if (e.key === 'Escape' && !document.fullscreenElement) { e.preventDefault(); this.close(); }
      return;
    }
    if (this.menu && e.target.closest?.('.p-menu') && /^(Arrow(Up|Down)|Home|End)$/.test(e.key) && !(e.metaKey || e.ctrlKey || e.altKey)) {
      e.preventDefault();
      this.menuKey(e);
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
      arrowleft: () => this.skip(-prefs.get('skipBack'), true), j: () => this.skip(-prefs.get('skipBack'), true),
      arrowright: () => this.skip(prefs.get('skipFwd'), true), l: () => this.skip(prefs.get('skipFwd'), true),
      arrowup: () => { v.volume = Math.min(1, v.volume + 0.05); v.muted = false; this.osdVolume(); },
      arrowdown: () => { v.volume = Math.max(0, v.volume - 0.05); this.osdVolume(); },
      f: () => this.toggleFullscreen(), m: () => { this.toggleMute(); this.osdVolume(); },
      i: () => this.toggleStats(), c: () => { if (!this.live?.stream) this.toggleMenu('tracks'); },
      n: () => this.playNext(), s: () => this.skipBtn?.click(),
      pageup: () => (this.live ? this.switchChannel(1) : this.jumpChapter(1)), pagedown: () => (this.live ? this.switchChannel(-1) : this.jumpChapter(-1)),
      g: () => this.nudgeSubs(-0.1), h: () => this.nudgeSubs(0.1),
      t: () => { if (this.live) this.toggleMenu('channels'); },
      escape: () => { if (this.menu) this.closeMenu(); else if (!document.fullscreenElement) this.close(); },
      home: () => this.seekTo(0), end: () => this.seekTo(this.duration() - 5),
    };
    if (/^[0-9]$/.test(k) && !this.live) { this.seekTo((this.duration() * +k) / 10); e.preventDefault(); return; }
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
    if (this.live) { this.renderLiveTime(); return; }
    const d = this.duration();
    const t = this.dragT ?? this.video.currentTime;
    const pct = d ? (t / d) * 100 : 0;
    this.fill.style.width = `${pct}%`;
    this.knob.style.left = `${pct}%`;
    this.nowEl.textContent = fmtTime(t);
    const left = prefs.get('timeLeft') && d > 0;
    this.endEl.textContent = left ? fmtLeft(t, d) : fmtTime(d);
    if (this.endLeft !== left) {
      this.endLeft = left;
      this.endLabel.textContent = left ? 'Time left ' : 'Duration ';
      this.endBtn.title = left ? 'Show the duration' : 'Show the time left';
      this.endBtn.setAttribute('aria-pressed', String(left));
    }
    const chaps = this.chapters(), chap = chaps[this.chapterAt(t, chaps)];
    const chapText = chap?.named ? chap.title : '';
    if (this.chapEl && this.chapEl.textContent !== chapText) this.chapEl.textContent = chapText;
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
      for (const c of this.chapters().slice(1)) this.rail.appendChild(h('div', { class: 'chap', style: { left: `${(c.start / d) * 100}%` }, title: c.title }));
      for (const [seg, title] of [[this.intro(), 'Intro'], [this.credits(), 'Credits']]) {
        if (seg) this.rail.insertBefore(h('div', { class: 'seg', style: { left: `${(seg.start / d) * 100}%`, width: `${((seg.end - seg.start) / d) * 100}%` }, title }), this.fill);
      }
    }
    if ('mediaSession' in navigator && d && navigator.mediaSession.setPositionState) {
      try { navigator.mediaSession.setPositionState({ duration: d, position: Math.min(t, d), playbackRate: this.video.playbackRate }); } catch {}
    }
  }

  // Wall-clock finish time. Paused, it slides forward with the clock, so the
  // UI tick refreshes it too.
  renderEnds() {
    const d = this.duration(), left = d - (this.dragT ?? this.video.currentTime);
    const txt = isFinite(d) && d && left > 0 && !this.video.ended ? `Ends at: ${END_FMT.format(Date.now() + (left / (this.video.playbackRate || 1)) * 1000)}` : '';
    if (this.endsEl.textContent !== txt) this.endsEl.textContent = txt;
  }

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
    if (!this.sessionId || this.closed || !this.plan || this.stopped) return;
    try {
      const r = await api('/api/playback/progress', { method: 'POST', body: { sessionId: this.sessionId, fileId: this.file?.id, method: this.plan.method, position: this.video.currentTime, paused: this.video.paused, stats: this.clientStats() } });
      this.lastJob = r.job || null;
      this.serverRate = r.serverRate || 0;
      this.diskWait = r.diskWait || 0;
      this.host = r.host || [];
      this.load = r.load || null;
    } catch (e) {
      if (e.status === 410) this.stopByServer(e.message);
    }
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
    this.checkWaiting();
    this.checkUpNext();
    this.checkLive();
  }

  // resetForNext ends the current title's session and clears what belonged
  // to it, before another title starts in the same player.
  resetForNext() {
    this.closeMenu();
    this.cancelUpNext(false);
    this.removeEndCard();
    this.sendStop();
    this.stopSent = false;
    this.sessionId = null;
    this.upNextShown = false;
    this.chaptersDrawn = false;
    this.segments = [];
    this.introSkipped = false;
    this.creditsSkipped = false;
    if (this.skipBtn) { this.skipBtn.remove(); this.skipBtn = null; this.skipKind = null; }
    for (const c of [...this.rail.querySelectorAll('.chap, .seg')]) c.remove();
    // Fallbacks (remux/transcode, software encoder, server audio/remux) were for the last file;
    // the next one starts from the user's chosen method again.
    this.fallbacks = 0;
    this.mode = prefs.get('mode');
    this.swEncode = false;
    this.noClientAudio = false;
    this.noClientRemux = false;
    this.noPgs = false;
    this.stalls = { count: 0, secs: 0, since: 0 };
    this.causeToasted = false;
    this.teardown();
  }

  setupMediaSession() {
    if (!('mediaSession' in navigator)) return;
    const it = this.item;
    try {
      navigator.mediaSession.metadata = new MediaMetadata({
        title: this.detail.extra ? `${it.title}: ${this.detail.extra.title}` : it.title,
        artist: it.kind === 'episode' ? `${this.detail.show?.title || ''} · ${fmtEpisode(it)}` : String(it.year || ''),
        artwork: [{ src: img(it, it.kind === 'episode' ? 'thumb' : 'poster', 480), sizes: '480x480', type: 'image/jpeg' }],
      });
      const ms = navigator.mediaSession;
      ms.setActionHandler('play', () => this.video.play());
      ms.setActionHandler('pause', () => this.video.pause());
      ms.setActionHandler('seekbackward', this.live ? null : () => this.skip(-prefs.get('skipBack')));
      ms.setActionHandler('seekforward', this.live ? null : () => this.skip(prefs.get('skipFwd')));
      ms.setActionHandler('seekto', this.live ? null : (d) => this.seekTo(d.seekTime));
      ms.setActionHandler('nexttrack', this.detail.next || this.live ? () => this.playNext() : null);
      ms.setActionHandler('previoustrack', this.live ? () => this.switchChannel(-1) : null);
    } catch {}
  }

  clearMediaSession() {
    const ms = globalThis.navigator?.mediaSession;
    if (!ms) return;
    for (const a of /** @type {MediaSessionAction[]} */ (['play', 'pause', 'seekbackward', 'seekforward', 'seekto', 'nexttrack', 'previoustrack'])) {
      try { ms.setActionHandler(a, null); } catch {}
    }
  }

  // fromHistory: Back already popped the player's entry. keepHistory: another
  // player takes the entry over.
  close({ fromHistory = false, keepHistory = false } = {}) {
    if (this.closed) return;
    this.closed = true;
    window.removeEventListener('popstate', this.onPop);
    if (!fromHistory && !keepHistory && history.state?.lexPlayer) history.back();
    this.sendStop();
    clearInterval(this.hbTimer);
    clearInterval(this.uiTimer);
    clearInterval(this.cdTimer);
    clearTimeout(this.hideTimer);
    document.removeEventListener('keydown', this.onKey);
    document.removeEventListener('fullscreenchange', this.onFs);
    window.removeEventListener('pagehide', this.onHide);
    this.clearMediaSession();
    if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
    if (document.pictureInPictureElement === this.video) document.exitPictureInPicture().catch(() => {});
    // e.g. subtitle search, when Back closed the player under it.
    for (const d of this.root.querySelectorAll('dialog')) if (d.open) d.close();
    this.teardown();
    this.destroyASS();
    this.fx.close();
    this.destroyPGS();
    this.root.remove();
    releaseToasts(this.root);
    if (this.background?.isConnected) this.background.inert = this.backgroundWasInert;
    if (this.restoreFocus?.isConnected) this.restoreFocus.focus();
    document.body.style.overflow = '';
    if (current === this) current = null;
    if (this.onClose) this.onClose();
  }
}

// Features kept in player/*.js, installed as Player methods (non-enumerable,
// like the class's own), so they share the player's state through this.
for (const part of [liveTv, upNext, skipSegments, chapters, trickplay, subtitles, audio, stats, menus]) {
  for (const [name, desc] of Object.entries(Object.getOwnPropertyDescriptors(part))) {
    if (Object.hasOwn(Player.prototype, name)) throw new Error(`Player.${name} is defined twice`);
    Object.defineProperty(Player.prototype, name, { ...desc, enumerable: false });
  }
}
