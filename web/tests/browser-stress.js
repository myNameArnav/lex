// Temporary browser integration fixture. Serve alongside the actual Lex UI:
// /stress.html?items=<episode-id>,<movie-id>,<movie-id>&minutes=20
// Uses the signed-in browser, preserves preferences, and suppresses watch
// progress so testing doesn't mark the user's titles watched.
import { openPlayer } from '/js/player.js';
import { api } from '/js/api.js';
import { prefs } from '/js/prefs.js';

const params = new URLSearchParams(location.search);
const items = (params.get('items') || '').split(',').map(Number).filter(n => n > 0);
const minutes = Number(params.get('minutes') || 20);
const report = { browser: navigator.userAgent, started: new Date().toISOString(), checks: [], samples: [], errors: [] };
const output = document.querySelector('#stress-report');
const savedPrefs = prefs.all();
const realFetch = window.fetch.bind(window);
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
let player, phase = 'startup', fault, monitor, heartbeats;

function sample() {
  const v = player?.video, e = player?.engine;
  return { phase, elapsed: Math.round((Date.now() - Date.parse(report.started)) / 1000),
    position: v?.currentTime || 0, ready: v?.readyState, paused: v?.paused,
    frames: v?.getVideoPlaybackQuality?.().totalVideoFrames || 0,
    method: player?.plan?.method, ahead: e?.ahead(), retries: e?.retries,
    restarts: e?.restarts, quota: !!e?.quotaAhead, rebuilds: player?.bufferRebuilds || 0,
    fetching: e?.fetching, throttled: e?.throttled, updating: e?.sb?.updating,
    ended: e?.ended, eos: e?.eos, ranges: e?.ranges(), streamFrom: e?.streamFrom,
    bytes: e?.bytes, lastDataAgo: e?.lastData ? Math.round(performance.now() - e.lastData) : null,
    videoRanges: v ? Array.from({ length: v.buffered.length }, (_, i) => [v.buffered.start(i), v.buffered.end(i)]) : [],
    error: player?.errEl?.textContent || v?.error?.message || null };
}
function render() {
  const s = sample();
  document.title = `Lex stress | ${phase} | ${Math.round(s.position)}s | ${report.checks.length} passed`;
  output.textContent = JSON.stringify({ ...s, checks: report.checks, errors: report.errors }, null, 2);
}
async function until(predicate, ms, label) {
  const deadline = Date.now() + ms;
  while (!predicate()) {
    if (Date.now() > deadline) throw new Error(`${label}: timed out (${JSON.stringify(sample())})`);
    await delay(250);
  }
}
async function check(name, run) {
  phase = name; render();
  const start = Date.now();
  await run();
  report.checks.push({ name, seconds: Math.round((Date.now() - start) / 1000), sample: sample() });
  render();
}
async function advancing(seconds = 3, timeout = 30000) {
  const start = player.video.currentTime;
  await until(() => player.video.currentTime >= start + seconds && player.video.readyState >= 3 && !player.errEl,
    timeout, 'playback must advance');
}
async function start(item, position = 0) {
  if (player) player.close();
  player = openPlayer({ itemId: item, start: position, onClose() {} });
  // Override heartbeat methods before the async item request completes.
  player.beat = () => {};
  player.sendStop = () => {
    if (player.sessionId) void api('/api/playback/stop', { method: 'POST', body: { sessionId: player.sessionId, position: 0 } }).catch(() => {});
  };
  player.mode = 'remux'; player.quality = 0; player.video.muted = true;
  await until(() => player.plan && player.engine && player.started, 45000, 'remux startup');
  if (player.plan.method !== 'remux') throw new Error(`Unexpected startup fallback: ${player.plan.method}`);
  await advancing();
}
async function starve() {
  const e = player.engine, v = player.video;
  await e.remove(v.currentTime + 1, Infinity);
  return e;
}

window.fetch = async (input, options) => {
  const isStream = typeof input === 'string' && input.includes('&t=') && input.startsWith('/api/files/');
  if (isStream && fault?.remaining > 0) {
    fault.remaining--;
    if (fault.kind === 'silent') return new Promise((resolve, reject) => {
      options.signal.addEventListener('abort', () => reject(new TypeError('Injected aborted network read')), { once: true });
    });
    if (fault.kind === 'disconnect') throw new TypeError('Injected connection loss');
  }
  return realFetch(input, options);
};

async function run() {
  if (!items.length) throw new Error('Pass ?items=<episode-id>,<movie-id>,<movie-id>');
  prefs.set('autoplayNext', false); prefs.set('subMode', 'off');
  prefs.set('bufferAhead', 0); prefs.set('backBuffer', 30);
  monitor = setInterval(() => { report.samples.push(sample()); render(); }, 5000);
  heartbeats = setInterval(() => {
    if (player?.sessionId) void api('/api/playback/progress', { method: 'POST', body: {
      sessionId: player.sessionId, position: 0, paused: true,
      // Test-only diagnostics travel through the existing session telemetry;
      // the coordinator can read them without Firefox console automation.
      stats: { ...player.clientStats(), resolution: `stress:${JSON.stringify(sample())}` },
    } }).catch(() => {});
  }, 10000);
  await check('episode startup', () => start(items[0]));
  if (params.has('startupOnly')) return;
  if (params.has('continuousOnly')) {
    await check(`${minutes} minute continuous playback`, async () => {
      const began = Date.now();
      while (Date.now() - began < minutes * 60000) {
        await advancing(5, 15000);
      }
    });
    return;
  }
  if (!params.has('restartOnly')) {
    await check('repeated seeks', async () => {
      for (const target of [610, 30, 1000, 250, 60]) {
        player.video.currentTime = target;
        await until(() => !player.video.seeking && player.video.currentTime >= target && player.video.readyState >= 3, 30000, `seek ${target}`);
        await advancing(2);
      }
    });
    await check('pause under automatic buffer pressure', async () => {
      player.video.pause();
      const position = player.video.currentTime, rebuilds = player.bufferRebuilds;
      await delay(60000);
      if (Math.abs(player.video.currentTime - position) > 0.2 || !player.video.paused || player.bufferRebuilds !== rebuilds || player.errEl) throw new Error('Paused playback changed or failed');
      await player.video.play(); await advancing(5);
    });
    await check('two dropped connections recover', async () => {
      const e = await starve();
      fault = { kind: 'disconnect', remaining: 2 };
      void e.load(player.video.currentTime, true);
      await until(() => fault.remaining === 0, 10000, 'injected drops');
      await advancing(5, 30000); fault = null;
      if (e.retries !== 0) throw new Error('Retries did not reset after playable media');
    });
    await check('silent connection watchdog recovers', async () => {
      const e = await starve();
      const at = player.video.currentTime;
      fault = { kind: 'silent', remaining: 1 };
      void e.load(at, true);
      await until(() => e.restarts >= 1 && e.retries >= 1, 22000, 'watchdog retry');
      await advancing(5, 30000); fault = null;
    });
    await check('persistent quota rebuilds same method', async () => {
      const e = await starve();
      Object.defineProperty(e.sb, 'appendBuffer', { configurable: true, value() {
        throw new DOMException('Injected permanent quota pressure', 'QuotaExceededError');
      } });
      void e.load(player.video.currentTime, true);
      await until(() => player.bufferRebuilds === 1 && player.engine !== e, 30000, 'fresh browser buffer');
      await advancing(5, 30000);
      if (player.plan.method !== 'remux') throw new Error('Quota stall immediately transcoded');
    });
  }
  await check('server restart recovery', async () => {
    // Start with a fresh, small contiguous buffer. Merely removing the
    // future while continuing the old download creates an artificial hole.
    prefs.set('bufferAhead', 5);
    await start(items[0], 100);
    const engine = player.engine;
    engine.opts.forward = 2;
    void engine.load(player.video.currentTime, false);
    await advancing(3);
    const restarts = engine.restarts;
    phase = 'RESTART SERVER NOW'; render();
    const position = player.video.currentTime;
    const deadline = Date.now() + 300000;
    let down = false;
    while (!down && Date.now() < deadline) {
      try { down = !(await realFetch('/api/public/info', { cache: 'no-store' })).ok; }
      catch { down = true; }
      await delay(300);
    }
    if (!down) throw new Error('No actual server restart was observed');
    report.restartObserved = true;
    phase = 'server restart recovery'; render();
    await until(() => player.video.currentTime > position + 10 && !player.errEl &&
      (player.engine !== engine || engine.restarts > restarts), 300000, 'server restart recovered');
  });
  prefs.set('bufferAhead', 0);
  for (const item of items.slice(1)) {
    await check(`movie ${item} startup and quota pressure`, async () => {
      await start(item, 600);
      await delay(45000); await advancing(5);
      if (player.errEl) throw new Error('Movie errored');
      player.video.currentTime = 1800;
      await until(() => player.video.readyState >= 3 && !player.video.seeking, 45000, 'movie seek');
      await advancing(5);
    });
  }
  await check(`${minutes} minute continuous Firefox playback`, async () => {
    await start(items[0], 0);
    const began = Date.now(); let stalledSince = 0, lastPosition = player.video.currentTime;
    while (Date.now() - began < minutes * 60000) {
      await delay(1000);
      const v = player.video;
      if (v.currentTime <= lastPosition + 0.01) {
        if (!stalledSince) stalledSince = Date.now();
        if (Date.now() - stalledSince > 30000) throw new Error(`Continuous playback stopped advancing: ${JSON.stringify(sample())}`);
      } else stalledSince = 0;
      if (player.errEl || v.error || v.ended) throw new Error(`Continuous playback failed: ${JSON.stringify(sample())}`);
      lastPosition = v.currentTime;
    }
  });
}

run().catch(error => { report.errors.push({ phase, message: error.message }); }).finally(() => {
  fault = null; window.fetch = realFetch;
  clearInterval(monitor); clearInterval(heartbeats);
  if (player) player.close();
  for (const [k, v] of Object.entries(savedPrefs)) prefs.set(k, v);
  report.finished = new Date().toISOString(); report.pass = !report.errors.length;
  phase = report.pass ? 'PASS' : 'FAIL'; render();
  const blob = new Blob([JSON.stringify(report, null, 2)], { type: 'application/json' });
  const link = document.createElement('a'); link.href = URL.createObjectURL(blob);
  link.download = `lex-browser-stress-${Date.now()}.json`; link.textContent = 'Download results';
  output.append('\n', link); link.click();
});
