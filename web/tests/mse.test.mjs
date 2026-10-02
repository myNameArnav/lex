import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';

async function fixture(response, { quota = false, keyframeInterval = 0 } = {}) {
  let now = 1000, timerID = 0;
  const timers = new Map(), requests = [], errors = [];
  let ranges = [[600, 610]];
  const buffered = () => ({ length: ranges.length, start: i => ranges[i][0], end: i => ranges[i][1] });
  const video = Object.assign(new EventTarget(), {
    currentTime: 610, paused: false, seeking: false, readyState: 2,
  });
  const sb = Object.assign(new EventTarget(), {
    updating: false,
    abort() {},
    remove(start, end) {
      if (keyframeInterval && Number.isFinite(end)) end = Math.ceil(end / keyframeInterval) * keyframeInterval;
      ranges = ranges.flatMap(([s, e]) => e <= start || s >= end ? [[s, e]] : [
        ...(s < start ? [[s, start]] : []), ...(e > end ? [[end, e]] : []),
      ]);
    },
    appendBuffer(data) {
      if (typeof quota === 'function' ? quota() : quota) throw new DOMException('Buffer full', 'QuotaExceededError');
      // An initialization fragment is accepted but adds no playable frames.
      if (data[0] === 2) ranges = [[600, 625]];
    },
  });
  Object.defineProperty(video, 'buffered', { get: buffered });
  Object.defineProperty(sb, 'buffered', { get: buffered });
  const context = vm.createContext({
    Uint8Array, AbortController, DOMException, window: {},
    performance: { now: () => now },
    fetch: (url, opts) => { requests.push(url); return response(opts, requests.length); },
    setTimeout: (fn, ms) => { const id = ++timerID; timers.set(id, { at: now + ms, fn }); return id; },
    clearTimeout: id => timers.delete(id), clearInterval() {},
  });
  const source = await readFile(new URL('../static/js/mse.js', import.meta.url), 'utf8');
  const module = new vm.SourceTextModule(source, { context });
  await module.link(() => new vm.SyntheticModule(['mediaSourceClass'], function () {
    this.setExport('mediaSourceClass', () => null);
  }, { context }));
  await module.evaluate();
  const engine = new module.namespace.MseEngine(video, {
    url: '/stream?sid=test', mime: 'video/mp4', duration: 1200,
  }, { forward: 60, back: 30, onError: (...args) => errors.push(args) });
  engine.ms = { readyState: 'open', endOfStream() {} };
  engine.sb = sb;
  const flush = async () => { for (let i = 0; i < 30; i++) await Promise.resolve(); };
  const advance = async ms => {
    const until = now + ms;
    while (now < until) {
      now = Math.min(until, now + 500);
      for (const [id, timer] of [...timers]) if (timer.at <= now) { timers.delete(id); timer.fn(); }
      engine.tick();
      await flush();
    }
  };
  return { engine, video, sb, requests, errors, advance, flush, setRanges: value => { ranges = value; } };
}

const hanging = ({ signal }) => new Promise((resolve, reject) => {
  signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true });
});

function fragmentThenError(marker = 1) {
  let reads = 0;
  return { ok: true, body: { getReader: () => ({
    read: async () => {
      if (reads++ === 0) return { value: new Uint8Array([marker]), done: false };
      throw new TypeError('Connection closed');
    }, cancel: async () => {},
  }) } };
}

test('a mid-playback connection that repeatedly hangs has a bounded recovery budget', async () => {
  const f = await fixture(hanging);
  void f.engine.load(610, true);
  await f.advance(240000);
  assert.equal(f.errors.length, 1);
  assert.ok(f.requests.length <= 7, `made ${f.requests.length} requests without playable progress`);
  assert.equal(f.engine.fetching, false);
  f.engine.destroy();
});

test('accepted init fragments must not reset the retry budget without playable media', async () => {
  const f = await fixture(async () => fragmentThenError());
  void f.engine.load(610, true);
  await f.advance(60000);
  assert.equal(f.errors.length, 1);
  assert.ok(f.requests.length <= 7);
  f.engine.destroy();
});

test('a transient silent connection resumes from the buffered end when delivery recovers', async () => {
  const f = await fixture((opts, attempt) => attempt === 1 ? hanging(opts) : Promise.resolve({
    ok: true, body: { getReader: () => ({
      read: async () => ({ value: new Uint8Array([2]), done: false }), cancel: async () => {},
    }) },
  }));
  // Hold subsequent reads after the recovered media so the harness doesn't
  // keep appending forever while microtasks are flushed.
  const realAppend = f.sb.appendBuffer;
  f.sb.appendBuffer = data => { realAppend(data); f.engine.opts.forward = 1; };
  void f.engine.load(610, true);
  await f.advance(18000);
  assert.equal(f.requests.length, 2);
  assert.match(f.requests[1], /t=610\.000/);
  assert.ok(f.engine.ahead() >= 15);
  assert.deepEqual(f.errors, []);
  assert.equal(f.engine.retries, 0);
  f.engine.destroy();
});

test('persistent quota rejection after the buffer drains cannot wait forever', async () => {
  const f = await fixture(async () => fragmentThenError(2), { quota: true });
  void f.engine.load(610, true);
  await f.advance(60000);
  assert.equal(f.errors.length, 1);
  assert.equal(f.errors[0][2], true, 'a stuck SourceBuffer must request a fresh playback pipeline');
  f.engine.destroy();
});

for (const aggressive of [false, true]) {
  test(`buffer eviction preserves frames at the playhead (aggressive=${aggressive})`, async () => {
    const f = await fixture(hanging, { keyframeInterval: 10 });
    f.setRanges([[0, 120]]);
    f.video.currentTime = 65;
    f.engine.opts.back = 0;
    await f.engine.evict(aggressive);
    assert.ok(f.engine.ranges().some(([s, e]) => s <= 65 && e > 65));
    assert.equal(f.requests.length, 0, 'ordinary eviction must not restart playback');
    f.engine.destroy();
  });
}

test('unusually long keyframes that cross eviction recover at the playhead instead of skipping ahead', async () => {
  const f = await fixture(hanging, { keyframeInterval: 100 });
  f.setRanges([[0, 200]]);
  f.video.currentTime = 65;
  await f.engine.evict(true);
  await f.flush();
  assert.equal(f.requests.length, 1);
  assert.match(f.requests[0], /t=65\.000/);
  f.engine.destroy();
});

test('an intentionally paused full buffer waits without triggering fallback', async () => {
  const f = await fixture(async () => fragmentThenError(2), { quota: true });
  f.video.paused = true;
  void f.engine.load(610, true);
  await f.advance(60000);
  assert.deepEqual(f.errors, []);
  assert.equal(f.requests.length, 1);
  f.engine.destroy();
});

test('normal backpressure on a healthy full forward buffer does not consume retries', async () => {
  const f = await fixture(async () => ({ ok: true, body: { getReader: () => ({
    read: async () => { throw new Error('read while forward buffer full'); }, cancel: async () => {},
  }) } }));
  f.setRanges([[600, 900]]);
  void f.engine.load(610, true);
  await f.advance(60000);
  assert.deepEqual(f.errors, []);
  assert.equal(f.requests.length, 1);
  assert.equal(f.engine.retries, 0);
  f.engine.destroy();
});

test('temporary quota pressure recovers when space becomes available', async () => {
  let full = true;
  const f = await fixture(async () => ({ ok: true, body: { getReader: () => ({
    read: async () => ({ value: new Uint8Array([2]), done: false }), cancel: async () => {},
  }) } }), { quota: () => full });
  const realAppend = f.sb.appendBuffer;
  f.sb.appendBuffer = data => { realAppend(data); f.engine.opts.forward = 1; };
  void f.engine.load(610, true);
  await f.advance(5000);
  full = false;
  await f.advance(1000);
  assert.ok(f.engine.ahead() >= 15);
  assert.deepEqual(f.errors, []);
  assert.equal(f.requests.length, 1);
  f.engine.destroy();
});

test('a long-GOP eviction recovery does not repeatedly discard and refetch the same playhead', async () => {
  const f = await fixture(hanging, { keyframeInterval: 100 });
  f.video.currentTime = 65;
  for (let i = 0; i < 5; i++) {
    f.setRanges([[0, 200]]);
    await f.engine.evict(false);
    await f.flush();
  }
  assert.equal(f.requests.length, 1);
  assert.deepEqual(f.errors, []);
  f.engine.destroy();
});

test('a download resumed after healthy backpressure gets a fresh silence deadline', async () => {
  const f = await fixture(async () => ({ ok: true, body: { getReader: () => ({
    read: () => new Promise(() => {}), cancel: async () => {},
  }) } }));
  f.setRanges([[600, 900]]);
  void f.engine.load(610, true);
  await f.advance(60000);
  f.video.currentTime = 898;
  await f.advance(5000);
  assert.equal(f.engine.retries, 0);
  assert.equal(f.requests.length, 1);
  await f.advance(14000);
  assert.equal(f.requests.length, 2);
  f.engine.destroy();
});

test('watchdog aborts reported as network errors do not double-count a retry', async () => {
  const f = await fixture(({ signal }) => new Promise((resolve, reject) => {
    signal.addEventListener('abort', () => reject(new TypeError('Network request aborted')), { once: true });
  }));
  void f.engine.load(610, true);
  await f.advance(18000);
  assert.equal(f.engine.retries, 1);
  assert.equal(f.requests.length, 2);
  f.engine.destroy();
});

test('a large playable-buffer hole recovers even while network data keeps arriving', async () => {
  const f = await fixture(hanging);
  f.video.currentTime = 610.8;
  f.setRanges([[600, 611], [615, 700]]);
  void f.engine.load(610.8, true);
  // A live download masks the stall from the network-silence watchdog.
  for (let i = 0; i < 50; i++) {
    f.engine.lastData += 500;
    await f.advance(500);
  }
  assert.equal(f.errors.length, 1);
  assert.equal(f.errors[0][2], true);
  assert.equal(f.errors[0][3], true, 'try a fresh same-method pipeline');
  assert.equal(f.engine.fetching, false);
  f.engine.destroy();
});

test('paused playback and a hole that fills promptly do not rebuild the pipeline', async () => {
  const f = await fixture(hanging);
  f.video.currentTime = 610.8;
  f.setRanges([[600, 611], [615, 700]]);
  f.video.paused = true;
  await f.advance(30000);
  f.video.paused = false;
  await f.advance(10000);
  f.setRanges([[600, 700]]);
  f.video.readyState = 4;
  await f.advance(30000);
  assert.deepEqual(f.errors, []);
  f.engine.destroy();
});

test('temporary proxy errors during a server restart retry and recover', async () => {
  const f = await fixture(async (_opts, attempt) => attempt <= 2
    ? { ok: false, status: attempt === 1 ? 502 : 503, json: async () => { throw new SyntaxError('Proxy HTML response'); } }
    : { ok: true, body: { getReader: () => ({
      read: async () => ({ value: new Uint8Array([2]), done: false }), cancel: async () => {},
    }) } });
  const realAppend = f.sb.appendBuffer;
  f.sb.appendBuffer = data => { realAppend(data); f.engine.opts.forward = 1; };
  void f.engine.load(610, true);
  await f.advance(10000);
  assert.equal(f.requests.length, 3);
  assert.deepEqual(f.errors, []);
  assert.ok(f.engine.ahead() >= 15);
  assert.equal(f.engine.retries, 0);
  f.engine.destroy();
});

test('a permanently unavailable proxy uses the same bounded retry budget', async () => {
  const f = await fixture(async () => ({ ok: false, status: 502, json: async () => ({}) }));
  void f.engine.load(610, true);
  await f.advance(60000);
  assert.equal(f.requests.length, 7);
  assert.equal(f.errors.length, 1);
  f.engine.destroy();
});

test('the server transcode-limit 503 stays terminal and retains its explanation', async () => {
  const message = 'transcode limit reached: too many simultaneous transcodes on this server';
  const f = await fixture(async () => ({ ok: false, status: 503, json: async () => ({ error: message }) }));
  void f.engine.load(610, true);
  await f.advance(60000);
  assert.equal(f.requests.length, 1);
  assert.deepEqual(f.errors, [[message, 503]]);
  f.engine.destroy();
});

test('a large Firefox network read is appended in bounded batches', async () => {
  const payload = new Uint8Array(8 * 1024 * 1024 + 123);
  payload.fill(7);
  let reads = 0;
  const f = await fixture(async () => ({ ok: true, body: { getReader: () => ({
    read: async () => reads++ === 0 ? { value: payload, done: false } : { done: true },
    cancel: async () => {},
  }) } }));
  let appended = 0;
  f.sb.appendBuffer = data => {
    assert.ok(data.byteLength <= 1024 * 1024, `oversized append: ${data.byteLength}`);
    assert.equal(data[0], 7); assert.equal(data[data.length - 1], 7);
    appended += data.byteLength;
  };
  await f.engine.load(610, true);
  assert.equal(appended, payload.byteLength);
  assert.deepEqual(f.errors, []);
  f.engine.destroy();
});

test('backpressure also pauses processing within one large network read', async () => {
  let reads = 0, appends = 0;
  const f = await fixture(async () => ({ ok: true, body: { getReader: () => ({
    read: async () => reads++ === 0 ? { value: new Uint8Array(4 * 1024 * 1024), done: false } : { done: true },
    cancel: async () => {},
  }) } }));
  f.engine.opts.forward = 5;
  f.sb.appendBuffer = () => { appends++; f.setRanges([[600, f.video.currentTime + 15]]); };
  void f.engine.load(610, true);
  await f.flush();
  assert.equal(appends, 1);
  await f.advance(1000);
  assert.equal(appends, 1, 'a queued network read must obey the forward buffer limit');
  f.video.currentTime += 15;
  await f.advance(500);
  assert.equal(appends, 2, 'processing should resume after buffered video is consumed');
  assert.equal(reads, 1, 'do not read again until the current chunk is processed');
  assert.deepEqual(f.errors, []);
  f.engine.destroy();
});

test('a superseded large read cannot change the replacement stream state', async () => {
  let appends = 0, cancelled = 0;
  const f = await fixture((opts, attempt) => attempt > 1 ? hanging(opts) : Promise.resolve({
    ok: true, body: { getReader: () => ({
      read: async () => ({ value: new Uint8Array(4 * 1024 * 1024), done: false }),
      cancel: async () => { cancelled++; },
    }) },
  }));
  f.engine.opts.forward = 5;
  f.sb.appendBuffer = () => { appends++; f.setRanges([[600, 625]]); };
  void f.engine.load(610, true);
  await f.flush();
  assert.equal(appends, 1);
  void f.engine.load(610, false);
  await f.flush();
  f.engine.lastData = 1234; f.engine.throttled = true;
  await f.advance(500);
  assert.equal(appends, 1);
  assert.equal(cancelled, 1);
  assert.equal(f.engine.lastData, 1234);
  assert.equal(f.engine.throttled, true);
  assert.deepEqual(f.errors, []);
  f.engine.destroy();
});

test('quota recovery partway through a split read keeps every byte in order', async () => {
  const payload = new Uint8Array(3 * 1024 * 1024 + 123);
  for (let i = 0; i < payload.length; i++) payload[i] = i % 251;
  let reads = 0, attempts = 0, acceptedBytes = 0;
  const accepted = new Uint8Array(payload.length);
  const f = await fixture(async () => ({ ok: true, body: { getReader: () => ({
    read: async () => reads++ === 0 ? { value: payload, done: false } : { done: true },
    cancel: async () => {},
  }) } }));
  f.sb.appendBuffer = data => {
    if (++attempts === 2) throw new DOMException('Transient quota pressure', 'QuotaExceededError');
    accepted.set(data, acceptedBytes);
    acceptedBytes += data.length;
  };
  void f.engine.load(610, true);
  await f.flush();
  assert.equal(acceptedBytes, 1024 * 1024);
  await f.advance(500); await f.flush();
  assert.equal(acceptedBytes, payload.length);
  assert.equal(attempts, 5, 'four batches, with only the rejected batch retried');
  assert.ok(accepted.every((value, i) => value === payload[i]), 'no bytes lost, duplicated or reordered');
  assert.deepEqual(f.errors, []);
  f.engine.destroy();
});
