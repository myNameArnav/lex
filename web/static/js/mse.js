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
const APPEND_BATCH_BYTES = 1 << 20;

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
    this.stalls = 0; // consecutive resumes that added nothing
    this.resumeAfter = 0;
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
    // remove() on an ended MediaSource reopens it, and then the video never
    // fires 'ended'. Nothing more is appended after end of stream anyway; a
    // seek outside the buffer goes through load(), which clears eos.
    if (this.eos) return;
    const t = this.video.currentTime;
    const gen = this.gen;
    // Removal extends to the next video keyframe. A tiny back buffer can
    // therefore delete the playhead itself, especially with copied video.
    const back = Math.max(10, this.evictionBack || 0, aggressive ? 10 : this.opts.back);
    const hadPlayhead = this.ranges().some(([s, e]) => s <= t && t < e);
    const r = this.ranges();
    if (r.length && r[0][0] < t - back - 1) await this.remove(0, t - back);
    if (gen !== this.gen || this.destroyed) return;
    if (hadPlayhead && !this.ranges().some(([s, e]) => s <= this.video.currentTime && this.video.currentTime < e)) {
      // Very long GOPs can cross even the safety margin. Retain more of
      // the preceding range on the next attempt so the same removal can't
      // cause a refetch loop, then refill at the playhead without skipping.
      this.evictionBack = Math.max(back * 2, t - r[0][0] + 1);
      this.load(this.video.currentTime, false);
      return;
    }
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
    let quotaSince = 0, quotaTime = this.video.currentTime;
    for (;;) {
      if (gen !== this.gen || this.destroyed) return false;
      await this.whenIdle();
      if (gen !== this.gen) return false;
      try {
        if (this.ms.readyState === 'closed') return false;
        this.sb.appendBuffer(data);
        await this.whenIdle();
        return gen === this.gen && !this.destroyed;
      } catch (e) {
        if (e.name !== 'QuotaExceededError') throw e;
        // The SourceBuffer is full (Chrome allows ~150 MB). Drop what's
        // behind the playhead, remember how much fits, and wait for
        // playback to make room. A starving, stuck buffer needs a new
        // playback pipeline instead of waiting forever.
        await this.evict(true);
        if (gen !== this.gen || this.destroyed) return false;
        const ahead = this.ahead();
        const now = performance.now();
        if (this.video.paused || ahead >= 5 || this.video.currentTime !== quotaTime) quotaSince = 0;
        else {
          if (!quotaSince) quotaSince = now;
          if (now - quotaSince >= 15000) throw new DOMException('Buffer quota prevented playback from continuing', 'QuotaStallError');
        }
        quotaTime = this.video.currentTime;
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
    this.bufferFailed = false;
    this.gapSince = 0;
    this.throttled = false;
    this.lastData = performance.now();
    this.streamFrom = t;
    if (!keep) { this.stalls = 0; this.resumeAfter = 0; }
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
      if (gen === this.gen && !ctrl.signal.aborted && e.name !== 'AbortError') this.networkError(gen, e);
      return;
    }
    if (!res.ok) {
      let msg = `Stream failed (HTTP ${res.status})`;
      let serverError = false;
      try { const j = await res.json(); if (j.error) { msg = j.error; serverError = true; } } catch {}
      if (gen !== this.gen || ctrl.signal.aborted) return;
      // Lex's own 503 explains the transcode limit; it isn't an outage.
      if ([502, 504].includes(res.status) || (res.status === 503 && !serverError)) this.networkError(gen, new Error(msg));
      else { this.fetching = false; this.opts.onError(msg, res.status); }
      return;
    }
    const reader = res.body.getReader();
    let pending = [], pendingBytes = 0, lastFlush = performance.now();
    const flush = async () => {
      if (!pendingBytes) return true;
      const buf = new Uint8Array(pendingBytes);
      let off = 0;
      for (const c of pending) { buf.set(c, off); off += c.byteLength; }
      pending = []; pendingBytes = 0; lastFlush = performance.now();
      const before = this.producingEnd();
      const ok = await this.append(buf, gen);
      // Processing queued bytes is progress even if no new read is needed.
      if (ok) this.lastData = performance.now();
      // Accepted init headers (or duplicate frames) are not recovery.
      if (ok && this.producingEnd() > Math.max(before, this.video.currentTime) + 0.1) this.retries = 0;
      return ok;
    };
    const waitForSpace = async () => {
      let waited = false;
      while (gen === this.gen && this.ahead() > this.target()) {
        if (pendingBytes && !(await flush())) return false;
        this.throttled = true;
        if (!this.throttledSince) this.throttledSince = performance.now();
        waited = true;
        await sleep(300);
      }
      // Intentional backpressure (including quota waits) is not network
      // silence. Start the watchdog clock when we resume processing.
      if (gen !== this.gen) return false;
      if (waited || this.throttled) this.lastData = performance.now();
      this.throttled = false;
      return true;
    };
    try {
      reading: while (gen === this.gen) {
        if (!(await waitForSpace())) break;
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
        this.measure(n, performance.now() - t0);
        // Firefox can return hundreds of MB in one read after we pause
        // downloading. One append may then exceed the entire MSE quota,
        // even with an empty buffer. Split it and apply backpressure between
        // batches; SourceBuffer accepts partial MP4 boxes across appends.
        for (let offset = 0; offset < n;) {
          if (!(await waitForSpace())) break reading;
          const size = Math.min(APPEND_BATCH_BYTES - pendingBytes, n - offset);
          pending.push(r.value.subarray(offset, offset + size));
          pendingBytes += size;
          offset += size;
          if (pendingBytes >= APPEND_BATCH_BYTES || performance.now() - lastFlush > 250 || this.ahead() < 3) {
            if (!(await flush())) break reading;
          }
        }
      }
    } catch (e) {
      if (gen === this.gen && !ctrl.signal.aborted && e.name !== 'AbortError') {
        if (e.name === 'QuotaStallError') {
          this.bufferError('The video buffer stopped accepting frames.');
        } else if (e.name === 'InvalidStateError' || e.name === 'NotSupportedError') {
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
        this.stopped(gen);
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
    if (this.duration && end >= this.duration - 2) this.finish(gen);
    // Otherwise the stream stopped early; tick() resumes it when needed.
    else this.stopped(gen);
  }

  // A stream stopped before the end. If it added nothing past where it
  // started (a truncated or still-downloading file whose probed duration is
  // too long, ffmpeg failing at a damaged spot), back off before tick()
  // resumes it, and after a few such attempts treat what's buffered as the
  // whole file so the video still fires `ended`.
  stopped(gen) {
    if (this.producingEnd() > this.streamFrom + 1) {
      this.stalls = 0;
      this.resumeAfter = 0;
      return;
    }
    this.stalls++;
    if (this.stalls >= 3) {
      if (this.ranges().length) this.finish(gen);
      else { this.resumeAfter = Infinity; this.opts.onError('The stream ended without any playable media.', 0); }
      return;
    }
    this.resumeAfter = performance.now() + 1000 * 2 ** this.stalls;
  }

  finish(gen) {
    this.eos = true;
    this.whenIdle().then(() => {
      if (gen !== this.gen) return;
      try { if (this.ms.readyState === 'open') this.ms.endOfStream(); } catch {}
    });
  }

  networkError(gen, e) {
    // The retry scheduled below owns the resume; leaving `ended` unset keeps
    // tick() from racing it and skipping the backoff.
    this.fetching = false;
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

  bufferError(message) {
    if (this.bufferFailed || this.destroyed) return;
    this.bufferFailed = true;
    this.gen++;
    this.ctrl?.abort();
    this.fetching = false;
    this.ended = false;
    this.opts.onError(message, 0, true, true);
  }

  tick() {
    if (this.destroyed || this.bufferFailed || !this.sb) return;
    // Anything that reopened the source after end of stream: close it again.
    if (this.eos && this.ms.readyState === 'open' && !this.sb.updating) {
      try { this.ms.endOfStream(); } catch {}
    }
    const v = this.video;
    const t = v.currentTime;
    // Jump small gaps (audio/video start mismatch after a seek).
    if (!v.seeking && v.readyState < 3) {
      const b = v.buffered;
      for (let i = 0; i < b.length; i++) {
        if (b.start(i) > t && b.start(i) - t < 2.5) { v.currentTime = b.start(i) + 0.05; break; }
      }
    }
    // A healthy download can still leave a large, unplayable hole. The
    // network watchdog cannot see this because bytes continue arriving.
    const b = v.buffered;
    let gap = false;
    if (!v.paused && !v.seeking && v.readyState < 3) {
      for (let i = 0; i < b.length; i++) {
        if (b.start(i) - t >= 2.5 && b.end(i) - b.start(i) > 2) { gap = true; break; }
      }
    }
    if (!gap || Math.abs(t - (this.gapTime ?? t)) > 0.1) this.gapSince = 0;
    this.gapTime = t;
    if (gap) {
      if (!this.gapSince) this.gapSince = performance.now();
      if (performance.now() - this.gapSince >= 15000) {
        this.bufferError('The browser left an unplayable gap in the video buffer.');
        return;
      }
    }
    // Resume a stream that stopped before the end of the file (server
    // closed it while we weren't reading, quota…), unless stopped() is
    // backing off after a resume that added nothing.
    if (this.ended && !this.eos && !this.fetching && performance.now() >= this.resumeAfter &&
        this.ahead() < Math.min(20, this.target() / 2)) {
      this.load(this.bufferedEnd(), true);
    }
    // Watchdog: a connection that stays silent while we're starving (e.g. a
    // proxy kept a dead connection open) gets restarted.
    if (this.fetching && !this.throttled && this.ahead() < 5 && this.lastData && performance.now() - this.lastData > 15000) {
      this.ctrl?.abort();
      this.networkError(this.gen, new Error('Stream stopped delivering data'));
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
