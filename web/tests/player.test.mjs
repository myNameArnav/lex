import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';

// Run the actual Player methods with a native video whose play() stays
// pending and never emits an error, as in the Firefox MKV startup hang.
async function fixture({ method = 'direct', hls = false, blocked = false, rejectRemux = false, apiGate = null, openGate = null, apiFailures = [] } = {}) {
  let now = 0;
  let timerID = 0;
  const timers = new Map();
  const requests = [], errors = [], spinners = [], subtitleCalls = [];
  class Video extends EventTarget {
    paused = true;
    currentTime = 0;
    readyState = 0;
    playable = false;
    pending = [];
    playCalls = 0;
    play() {
      this.playCalls++;
      if (blocked) return Promise.reject(new DOMException('Autoplay blocked', 'NotAllowedError'));
      this.paused = false;
      this.dispatchEvent(new Event('play'));
      if (this.playable) {
        this.dispatchEvent(new Event('playing'));
        return Promise.resolve();
      }
      return new Promise((resolve, reject) => this.pending.push({ resolve, reject }));
    }
    pause() {
      if (this.paused) return;
      this.paused = true;
      this.dispatchEvent(new Event('pause'));
      for (const p of this.pending.splice(0)) p.reject(new DOMException('Paused', 'AbortError'));
    }
    load() { this.currentTime = 0; this.readyState = 0; }
    removeAttribute() {}
    querySelectorAll() { return []; }
  }
  const video = new Video();
  const context = vm.createContext({
    performance: { now: () => now },
    document: new EventTarget(), window: new EventTarget(),
    setInterval: () => 1, clearInterval() {},
    setTimeout: (fn, ms) => { const id = ++timerID; timers.set(id, { fn, at: now + ms }); return id; },
    clearTimeout: id => timers.delete(id),
  });
  const deps = {
    './ui.js': Object.fromEntries(['h', 'resLabel', 'fmtTime', 'fmtBitrate', 'fmtBytes', 'streamLabel', 'clear', 'langName', 'channelName', 'modal', 'containTab'].map(k => [k, () => {}])),
    './api.js': {
      api: async (path, { body }) => {
        assert.equal(path, '/api/playback/plan');
        requests.push(body);
        if (apiGate) await apiGate;
        if (apiFailures.length) {
          const status = apiFailures.shift();
          throw Object.assign(new Error(`HTTP ${status}`), { status });
        }
        return { plan: { ...player.plan, method: body.mode, url: '/stream', hls: false }, file: player.file };
      }, img() {},
    },
    './caps.js': { detectCaps: () => ({ mse: true }) },
    './prefs.js': { prefs: { get: k => k === 'heartbeat' ? 10 : 0, set() {} }, QUALITIES: [] },
    './mse.js': { MseEngine: class {
      async open() {
        if (openGate) await (typeof openGate === 'function' ? openGate() : openGate);
        if (rejectRemux && player.plan.method === 'remux') {
          video.error = { code: 3, message: 'Decode failed' };
          video.dispatchEvent(new Event('error'));
          return;
        }
        video.playable = true;
        video.readyState = 4;
      }
      destroy() {}
    } },
  };
  deps['./ui.js'].icons = { play: 'play', pause: 'pause' };
  deps['./ui.js'].langName = value => value;
  deps['./ui.js'].toast = () => {};
  deps['./ui.js'].METHOD_LABEL = { direct: 'Direct Play', remux: 'Direct Stream', transcode: 'Transcode' };
  deps['./ui.js'].reasonLabel = value => value;
  deps['./ui.js'].fmtEpisode = it => `S${it.season} E${it.episode}`;
  const source = await readFile(new URL('../static/js/player.js', import.meta.url), 'utf8');
  const module = new vm.SourceTextModule(source + '\nexport { Player };', { context });
  await module.link(specifier => {
    const values = deps[specifier];
    return new vm.SyntheticModule(Object.keys(values), function () {
      for (const [name, value] of Object.entries(values)) this.setExport(name, value);
    }, { context });
  });
  await module.evaluate();
  const player = Object.assign(Object.create(module.namespace.Player.prototype), {
    video, plan: { method, hls, url: '/direct', sessionId: 'session', audio: 1 },
    item: { id: 1 }, file: { id: 2 }, trick: { fileId: 2 },
    mode: 'auto', quality: 0, audio: 1, subtitle: -1, sessionId: 'session',
    closed: false, started: false, fallbacks: 0, bufHist: [], stalls: { count: 0, secs: 0, since: 0 },
    root: Object.assign(new EventTarget(), { remove() {} }), seek: new EventTarget(),
    playBtn: { setAttribute() {} }, methodEl: {}, fsBtn: { setAttribute() {} },
    poke() {}, beat() {}, setSubtitleTrack: value => subtitleCalls.push(value), hideError() {}, showUI() {},
    clientStats: () => ({ bufferAhead: 0 }), checkUpNext() {}, renderEnds() {},
    showSpinner: value => spinners.push(value), showError: msg => errors.push(msg),
    sendStop() {}, destroyASS() {},
  });
  context.document.body = { style: {} };
  player.bind();
  const flush = async () => { for (let i = 0; i < 20; i++) await Promise.resolve(); };
  const advance = async ms => {
    const until = now + ms;
    do {
      now = Math.min(until, now + 500);
      for (const [id, timer] of [...timers]) if (timer.at <= now) { timers.delete(id); timer.fn(); }
      player.tickUI(); await flush();
    } while (now < until);
  };
  return { player, video, requests, errors, spinners, subtitleCalls, advance, flush };
}

function gate() {
  let release;
  const promise = new Promise(resolve => { release = resolve; });
  return { promise, release };
}

test('silent native startup falls back to remux once and preserves resume position', async () => {
  const f = await fixture();
  void f.player.attach(154);
  await f.advance(14999);
  assert.equal(f.requests.length, 0);
  await f.advance(1);
  assert.equal(f.requests.length, 1);
  assert.equal(f.requests[0].mode, 'remux');
  assert.equal(f.requests[0].start, 154);
  assert.equal(f.requests[0].sessionId, 'session');
  assert.equal(f.player.started, true);
  assert.equal(f.player.playBtn.innerHTML, 'pause');
  await f.advance(60000);
  assert.equal(f.requests.length, 1);
  assert.deepEqual(f.errors, []);
});

test('successful native startup does not fall back', async () => {
  const f = await fixture();
  f.video.playable = true;
  await f.player.attach(0);
  await f.advance(60000);
  assert.equal(f.requests.length, 0);
});

test('autoplay denial does not trigger codec fallback', async () => {
  const f = await fixture({ blocked: true });
  await f.player.attach(0);
  await f.advance(60000);
  assert.equal(f.requests.length, 0);
});

test('pause cancels the startup watch and play starts a fresh deadline', async () => {
  const f = await fixture();
  void f.player.attach(0);
  await f.advance(10000);
  f.video.pause();
  await f.advance(60000);
  assert.equal(f.requests.length, 0);
  f.video.play().catch(() => {});
  await f.advance(14999);
  assert.equal(f.requests.length, 0);
  await f.advance(1);
  assert.equal(f.requests.length, 1);
});

test('media error and startup timeout cannot replan the same source twice', async () => {
  const f = await fixture();
  void f.player.attach(0);
  f.video.error = { code: 4, message: 'Unsupported source' };
  f.video.dispatchEvent(new Event('error'));
  f.video.dispatchEvent(new Event('error'));
  await f.advance(15000);
  assert.equal(f.requests.length, 1);
  assert.equal(f.requests[0].mode, 'remux');
});

test('a quick pause/play cannot let an old play rejection cancel the new startup watch', async () => {
  const f = await fixture();
  void f.player.attach(0);
  f.video.pause();
  f.video.play().catch(() => {});
  await f.flush();
  await f.advance(15000);
  assert.equal(f.requests.length, 1);
  assert.equal(f.requests[0].mode, 'remux');
});

test('a failed remux plan can still fall back to transcode', async () => {
  const f = await fixture({ rejectRemux: true });
  void f.player.attach(0);
  await f.advance(15000);
  assert.deepEqual(f.requests.map(r => r.mode), ['remux', 'transcode']);
  assert.equal(f.player.plan.method, 'transcode');
  assert.equal(f.player.started, true);
  assert.equal(f.player.playBtn.innerHTML, 'pause');
});

test('the old native play rejection does not hide the spinner during MSE startup', async () => {
  const opening = gate();
  const f = await fixture({ openGate: opening.promise });
  void f.player.attach(0);
  await f.advance(15000);
  assert.equal(f.player.plan.method, 'remux');
  assert.equal(f.player.started, false);
  assert.equal(f.spinners.at(-1), true);
  opening.release();
  await f.flush();
  assert.equal(f.player.started, true);
});

test('an unsupported-source rejection cannot hide the spinner during the fallback request', async () => {
  const planning = gate();
  const f = await fixture({ apiGate: planning.promise });
  void f.player.attach(0);
  f.video.error = { code: 4, message: 'Unsupported source' };
  f.video.dispatchEvent(new Event('error'));
  for (const p of f.video.pending.splice(0)) p.reject(new DOMException('Unsupported source', 'NotSupportedError'));
  await f.flush();
  assert.equal(f.requests.length, 1);
  assert.equal(f.spinners.at(-1), true);
  planning.release();
  await f.flush();
  assert.equal(f.player.started, true);
});

test('a superseded MSE open cannot install subtitles or start playback again', async () => {
  const first = gate(), second = gate();
  const openings = [first.promise, second.promise];
  const f = await fixture({ method: 'remux', openGate: () => openings.shift() });
  void f.player.attach(0);
  void f.player.attach(25);
  second.release();
  await f.flush();
  assert.equal(f.video.playCalls, 1);
  assert.equal(f.subtitleCalls.length, 1);
  first.release();
  await f.flush();
  assert.equal(f.video.playCalls, 1);
  assert.equal(f.subtitleCalls.length, 1);
});

test('teardown and close cancel the startup watch', async () => {
  for (const action of ['teardown', 'close']) {
    const f = await fixture();
    void f.player.attach(0);
    f.player[action]();
    await f.advance(60000);
    assert.equal(f.requests.length, 0);
  }
});

test('the direct startup deadline does not apply to HLS or MSE playback', async () => {
  for (const options of [{ hls: true }, { method: 'remux' }, { method: 'transcode' }]) {
    const f = await fixture(options);
    void f.player.attach(0);
    await f.advance(60000);
    assert.equal(f.requests.length, 0);
  }
});

test('a mid-playback stuck remux buffer falls back at the current position', async () => {
  const f = await fixture({ method: 'remux' });
  void f.player.attach(0);
  await f.flush();
  f.video.currentTime = 610;
  await f.player.onStreamError('Buffer quota prevented playback from continuing', 0, true);
  await f.flush();
  assert.equal(f.requests.length, 1);
  assert.equal(f.requests[0].mode, 'transcode');
  assert.equal(f.requests[0].start, 610);
  assert.equal(f.player.started, true);
  assert.deepEqual(f.errors, []);
});

test('a quota stall rebuilds the same method once before falling back', async () => {
  const f = await fixture({ method: 'remux' });
  void f.player.attach(0);
  await f.flush();
  f.video.currentTime = 610;
  await f.player.onStreamError('Video buffer stalled', 0, true, true);
  await f.flush();
  assert.equal(f.requests[0].mode, 'remux');
  assert.equal(f.requests[0].start, 610);
  assert.equal(f.player.started, true);
  f.video.currentTime = 620;
  await f.player.onStreamError('Video buffer stalled again', 0, true, true);
  await f.flush();
  assert.deepEqual(f.requests.map(r => r.mode), ['remux', 'transcode']);
  assert.equal(f.requests[1].start, 620);
});

test('replanning through a brief server outage recovers at the current position', async () => {
  const f = await fixture({ method: 'remux', apiFailures: [502, 503] });
  f.video.currentTime = 610;
  void f.player.replan({ mode: 'remux' });
  await f.advance(10000);
  assert.equal(f.requests.length, 3);
  assert.ok(f.requests.every(r => r.start === 610 && r.mode === 'remux'));
  assert.deepEqual(f.errors, []);
  assert.equal(f.player.started, true);
});

test('closing during a plan retry does not keep requesting or show an error', async () => {
  const f = await fixture({ method: 'remux', apiFailures: [502] });
  void f.player.replan(); await f.flush();
  f.player.close(); await f.advance(10000);
  assert.equal(f.requests.length, 1);
  assert.deepEqual(f.errors, []);
});

test('a newer playback plan supersedes an older request waiting to retry', async () => {
  const f = await fixture({ method: 'remux', apiFailures: [502] });
  void f.player.replan({ mode: 'remux' }); await f.flush();
  await f.player.replan({ mode: 'transcode' });
  await f.advance(10000);
  assert.deepEqual(f.requests.map(r => r.mode), ['remux', 'transcode']);
  assert.equal(f.player.plan.method, 'transcode');
  assert.deepEqual(f.errors, []);
});

test('planning retries are bounded and permission errors are not retried', async () => {
  for (const status of [503, 403]) {
    const f = await fixture({ method: 'remux', apiFailures: Array(8).fill(status) });
    void f.player.replan();
    await f.advance(60000);
    assert.equal(f.requests.length, status === 503 ? 7 : 1);
    assert.deepEqual(f.errors, [`HTTP ${status}`]);
  }
});
