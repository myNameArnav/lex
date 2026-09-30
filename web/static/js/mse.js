// MseEngine plays a server-side remux/transcode (fragmented MP4 over one
// HTTP response) through Media Source Extensions.
//
// The server keeps the source file's timestamps (ffmpeg -copyts), so the
// video element's timeline is the file's timeline: seeking to an unbuffered
// point just restarts the stream there. Buffering is bounded by `forward`
// seconds: once enough is buffered we stop reading, TCP backpressure stalls
// ffmpeg on the server, and it costs nothing until playback catches up.

import { mediaSourceClass } from './caps.js';

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// Automatic buffering: fill whatever the browser will hold (Chrome's quota is
// ~150 MB), up to this many seconds.
const AUTO_AHEAD = 1200;

export class MseEngine {
  constructor(video, plan, opts) {
    this.video = video;
    this.url = plan.url;
    this.mime = plan.mime;
    this.duration = plan.duration;
    this.offset = -(plan.startTime || 0);
    this.opts = { forward: 0, back: 30, onError: () => {}, onEnded: () => {}, ...opts };
    this.gen = 0;
    this.ctrl = null;
    this.bytes = 0;
    this.bandwidth = 0; // bits/s while actually downloading
    this.restarts = -1;
    this.retries = 0;
    this.streamFrom = 0;
    this.fetching = false;
    this.ended = false;
    this.eos = false;
    this.destroyed = false;
    this.throttled = false;
    this.onSeeking = this.onSeeking.bind(this);
    this.seekTimer = null;
  }

  async open(start) {
    const MS = mediaSourceClass();
    this.ms = new MS();
    if (window.ManagedMediaSource && MS === window.ManagedMediaSource) this.video.disableRemotePlayback = true;
    this.objUrl = URL.createObjectURL(this.ms);
    const opened = new Promise((res) => this.ms.addEventListener('sourceopen', res, { once: true }));
    this.video.src = this.objUrl;
    await opened;
    if (this.destroyed) return;
    this.sb = this.ms.addSourceBuffer(this.mime);
    this.sb.mode = 'segments';
    try { this.ms.duration = this.duration; } catch {}
    this.sb.timestampOffset = this.offset;
    this.video.addEventListener('seeking', this.onSeeking);
    this.timer = setInterval(() => this.tick(), 500);
    this.load(start, false);
    if (start > 0) this.video.currentTime = start;
  }

  destroy() {
    this.destroyed = true;
    this.gen++;
    if (this.ctrl) this.ctrl.abort();
    clearInterval(this.timer);
    clearTimeout(this.seekTimer);
    this.video.removeEventListener('seeking', this.onSeeking);
    try { if (this.ms && this.ms.readyState === 'open') this.ms.endOfStream(); } catch {}
    if (this.objUrl) URL.revokeObjectURL(this.objUrl);
  }

  // ---------- buffer helpers ----------

  ranges() {
    const out = [];
    try {
      const b = this.sb ? this.sb.buffered : null;
      if (b) for (let i = 0; i < b.length; i++) out.push([b.start(i), b.end(i)]);
    } catch {}
    return out;
  }

  // Seconds buffered contiguously ahead of the playhead.
  ahead(t = this.video.currentTime) {
    for (const [s, e] of this.ranges()) {
      if (s - 1 <= t && t <= e) return e - t;
      if (s > t && s - t < 3) return e - t; // just started a new stream here
    }
    return 0;
  }

  bufferedEnd() {
    const t = this.video.currentTime;
    for (const [s, e] of this.ranges()) if (s - 1 <= t && t <= e + 0.5) return e;
    return t;
  }

  // End of the buffered range the running stream is writing into.
  producingEnd() {
    let end = this.streamFrom;
    for (const [s, e] of this.ranges()) {
      if (s <= this.streamFrom + 5 && e >= this.streamFrom - 1) end = Math.max(end, e);
    }
    return end;
  }

  whenIdle() {
    if (!this.sb || !this.sb.updating) return Promise.resolve();
    return new Promise((res) => {
      const done = () => { this.sb.removeEventListener('updateend', done); this.sb.removeEventListener('error', done); res(); };
      this.sb.addEventListener('updateend', done);
      this.sb.addEventListener('error', done);
    });
  }

  async remove(start, end) {
    if (!this.sb || end <= start) return;
    await this.whenIdle();
    try {
      if (this.ms.readyState === 'closed') return;
      this.sb.remove(start, end);
      await this.whenIdle();
    } catch {}
  }

  async evict(aggressive) {
    const t = this.video.currentTime;
    const back = aggressive ? 3 : this.opts.back;
    const r = this.ranges();
    if (r.length && r[0][0] < t - back - 1) await this.remove(0, t - back);
    if (aggressive) {
      // Also drop stale data far ahead that isn't contiguous with the playhead.
      for (const [s, e] of r) if (s > t + 5 && s > this.bufferedEnd() + 1) await this.remove(s, e);
    }
  }

  // Effective forward target: the user's setting (0 = automatic), capped by
  // what the browser's MSE memory quota turned out to hold.
  wanted() { return this.opts.forward > 0 ? this.opts.forward : AUTO_AHEAD; }

  target() { return Math.min(this.wanted(), this.quotaAhead || Infinity); }

  async append(data, gen) {
    for (;;) {
      if (gen !== this.gen || this.destroyed) return false;
      await this.whenIdle();
      if (gen !== this.gen) return false;
      try {
        if (this.ms.readyState === 'closed') return false;
        this.sb.appendBuffer(data);
        await this.whenIdle();
        return true;
      } catch (e) {
        if (e.name !== 'QuotaExceededError') throw e;
        // The SourceBuffer is full (Chrome allows ~150 MB). Drop what's
        // behind the playhead, remember how much fits, and wait for
        // playback to make room — never give up, or the stream stalls.
        await this.evict(true);
        const ahead = this.ahead();
        if (ahead > 10) this.quotaAhead = Math.max(10, Math.floor(ahead * 0.85));
        this.throttled = true;
        await sleep(500);
      }
    }
  }

  // ---------- streaming ----------

  // load (re)starts the server stream at time t. With keep=false the current
  // buffer is dropped (a seek elsewhere); with keep=true it's a resume.
  async load(t, keep) {
    const gen = ++this.gen;
    this.restarts++;
    if (this.ctrl) this.ctrl.abort();
    const ctrl = new AbortController();
    this.ctrl = ctrl;
    this.ended = false;
    this.eos = false;
    this.fetching = true;
    this.lastData = performance.now();
    this.streamFrom = t;
    await this.whenIdle();
    if (gen !== this.gen) return;
    try { if (this.ms.readyState === 'open') this.sb.abort(); } catch {}
    if (!keep) await this.remove(0, Infinity);
    if (gen !== this.gen) return;
    try { this.sb.timestampOffset = this.offset; } catch {}

    let res;
    try {
      res = await fetch(`${this.url}&t=${Math.max(0, t).toFixed(3)}`, { signal: ctrl.signal, credentials: 'same-origin', cache: 'no-store' });
    } catch (e) {
      if (gen === this.gen && e.name !== 'AbortError') this.networkError(gen, e);
      return;
    }
    if (!res.ok) {
      let msg = `Stream failed (HTTP ${res.status})`;
      try { const j = await res.json(); if (j.error) msg = j.error; } catch {}
      if (gen === this.gen) { this.fetching = false; this.opts.onError(msg, res.status); }
      return;
    }
    this.retries = 0;
    const reader = res.body.getReader();
    let pending = [], pendingBytes = 0, lastFlush = performance.now();
    const flush = async () => {
      if (!pendingBytes) return true;
      const buf = new Uint8Array(pendingBytes);
      let off = 0;
      for (const c of pending) { buf.set(c, off); off += c.byteLength; }
      pending = []; pendingBytes = 0; lastFlush = performance.now();
      return this.append(buf, gen);
    };
    try {
      while (gen === this.gen) {
        // Backpressure: stop reading when the forward buffer is full.
        let waited = false;
        while (gen === this.gen && this.ahead() > this.target()) {
          if (pendingBytes && !(await flush())) break;
          this.throttled = true;
          if (!this.throttledSince) this.throttledSince = performance.now();
          waited = true;
          await sleep(300);
        }
        this.throttled = false;
        if (gen !== this.gen) break;
        const t0 = performance.now();
        const r = await reader.read();
        this.lastData = performance.now();
        if (r.done) {
          await flush();
          if (gen === this.gen) this.streamDone(gen);
          break;
        }
        const n = r.value.byteLength;
        this.bytes += n;
        this.measure(n, waited ? performance.now() - t0 : 0);
        pending.push(r.value);
        pendingBytes += n;
        if (pendingBytes >= 1 << 20 || performance.now() - lastFlush > 250 || this.ahead() < 3) {
          if (!(await flush())) break;
        }
      }
    } catch (e) {
      if (gen === this.gen && e.name !== 'AbortError') {
        if (e.name === 'InvalidStateError' || e.name === 'NotSupportedError') {
          this.fetching = false;
          this.opts.onError(`The browser rejected the stream (${e.message}).`, 0, true);
        } else {
          this.networkError(gen, e);
        }
      }
    } finally {
      reader.cancel().catch(() => {});
      // However the loop ended, a stream that didn't reach the end must be
      // resumable: tick() restarts it from the buffered end.
      if (gen === this.gen && this.fetching) {
        this.fetching = false;
        this.ended = true;
      }
    }
  }

  // Throughput over wall-clock windows (≥1 s) of active downloading, so
  // bytes already queued in the browser don't read as absurd speeds.
  measure(n, _) {
    const now = performance.now();
    if (!this.win) this.win = { start: now, bytes: 0, idle: 0 };
    const w = this.win;
    w.bytes += n;
    if (this.throttledSince) { w.idle += now - this.throttledSince; this.throttledSince = 0; }
    const span = now - w.start - w.idle;
    if (now - w.start >= 1000 && span > 200) {
      const inst = (w.bytes * 8) / (span / 1000);
      this.bandwidth = this.bandwidth ? this.bandwidth * 0.7 + inst * 0.3 : inst;
      this.win = { start: now, bytes: 0, idle: 0 };
    }
  }

  streamDone(gen) {
    this.fetching = false;
    this.ended = true;
    const end = this.bufferedEnd();
    if (this.duration && end >= this.duration - 2) {
      this.eos = true;
      this.whenIdle().then(() => {
        if (gen !== this.gen) return;
        try { if (this.ms.readyState === 'open') this.ms.endOfStream(); } catch {}
      });
    }
    // Otherwise the stream stopped early; tick() resumes it when needed.
  }

  networkError(gen, e) {
    this.fetching = false;
    this.ended = true;
    this.retries++;
    if (this.retries > 6) {
      this.opts.onError('Lost connection to the server while streaming.', 0);
      return;
    }
    const at = this.bufferedEnd();
    setTimeout(() => { if (gen === this.gen && !this.destroyed) this.load(at, true); }, Math.min(8000, 1000 * this.retries));
  }

  onSeeking() {
    if (this.destroyed) return;
    const t = this.video.currentTime;
    for (const [s, e] of this.ranges()) if (s - 0.3 <= t && t < e - 0.5) return;
    // The running stream is about to reach this point: just wait.
    if (this.fetching && t >= this.streamFrom - 0.5 && t < this.producingEnd() + 8) return;
    clearTimeout(this.seekTimer);
    this.seekTimer = setTimeout(() => this.load(this.video.currentTime, false), 150);
  }

  tick() {
    if (this.destroyed || !this.sb) return;
    const v = this.video;
    const t = v.currentTime;
    // Jump small gaps (audio/video start mismatch after a seek).
    if (!v.seeking && v.readyState < 3) {
      const b = v.buffered;
      for (let i = 0; i < b.length; i++) {
        if (b.start(i) > t && b.start(i) - t < 2.5) { v.currentTime = b.start(i) + 0.05; break; }
      }
    }
    // Resume a stream that stopped before the end of the file (server
    // closed it while we weren't reading, network hiccup, quota…).
    if (this.ended && !this.eos && !this.fetching && this.ahead() < Math.min(20, this.target() / 2)) {
      this.load(this.bufferedEnd(), true);
    }
    // Watchdog: a connection that stays silent while we're starving (e.g. a
    // proxy kept a dead connection open) gets restarted.
    if (this.fetching && !this.throttled && this.ahead() < 5 && this.lastData && performance.now() - this.lastData > 15000) {
      this.lastData = performance.now();
      this.load(this.bufferedEnd(), true);
    }
    if (!this.sb.updating && performance.now() - (this.lastEvict || 0) > 5000) {
      this.lastEvict = performance.now();
      this.evict(false);
    }
  }

  stats() {
    const r = this.ranges();
    return {
      ahead: this.ahead(),
      target: this.target(),
      quotaLimited: !!this.quotaAhead && this.quotaAhead < this.wanted(),
      bandwidth: this.bandwidth,
      bytes: this.bytes,
      restarts: Math.max(0, this.restarts),
      ranges: r,
      throttled: this.throttled,
      fetching: this.fetching,
      streamFrom: this.streamFrom,
    };
  }
}
