import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';

// client-remux.js on the main thread, with a fake worker that answers each
// request and a fake fetch for the range probe.
async function fixture({ probe = () => ({ status: 206, range: 'bytes 0-0/1000' }), reply = () => ({}) } = {}) {
  const workers = [], probes = [];
  class Worker {
    constructor() { this.posted = []; workers.push(this); }
    postMessage(m) {
      this.posted.push(m);
      const r = reply(m);
      if (r) queueMicrotask(() => this.onmessage({ data: { id: m.id, ...r } }));
    }
    terminate() { this.terminated = true; }
  }
  const fetch = async (url, opts) => {
    const p = { url, range: opts.headers.Range, signal: opts.signal };
    probes.push(p);
    const r = probe(p);
    if (r.throws) throw r.throws;
    return { status: r.status, ok: r.status >= 200 && r.status < 300, headers: { get: () => r.range || null }, text: async () => r.body || '' };
  };
  const context = vm.createContext({ Worker, fetch, AbortController, DOMException, URL, queueMicrotask, Error, Object, Promise, JSON });
  const load = (name) => readFile(new URL(`../static/js/${name}`, import.meta.url), 'utf8');
  const module = new vm.SourceTextModule(await load('client-remux.js'), { context, initializeImportMeta: (meta) => { meta.url = 'http://lex.test/js/client-remux.js'; } });
  await module.link(async (specifier) => new vm.SourceTextModule(await load(specifier.slice(2)), { context }));
  await module.evaluate();
  const { RemuxWorker, RemuxStream } = module.namespace;
  return { worker: new RemuxWorker({ url: '/api/files/1/direct?sid=s&via=remux', audio: 1 }), RemuxStream, workers, probes };
}

const ops = (w) => w.posted.map((m) => m.op + (m.stream ? m.stream : ''));

test('the server is checked for range support once, with a request that is then aborted', async () => {
  const f = await fixture();
  await f.worker.open();
  await f.worker.open();
  assert.equal(f.probes.length, 1);
  assert.equal(f.probes[0].range, 'bytes=0-0');
  assert.ok(f.probes[0].signal.aborted);
  assert.deepEqual(ops(f.workers[0]), ['open']);
});

test('a proxy that ignores Range makes the server remux, before the file is read', async () => {
  const f = await fixture({ probe: () => ({ status: 200 }) });
  await assert.rejects(f.worker.open(), (e) => e.name === 'NotSupportedError' && e.message === 'Browser remux: the server ignored a range request');
  assert.ok(f.probes[0].signal.aborted, 'the whole-file download is cut off');
  assert.deepEqual(ops(f.workers[0]), []);
});

test('probe failures are HTTP errors (Lex\'s explained) or lost connections, which try again', async () => {
  let n = 0;
  const f = await fixture({ probe: () => (++n === 1 ? { throws: new TypeError('NetworkError') } : { status: 410, body: '{"error":"Playback was stopped by the server admin"}' }) });
  await assert.rejects(f.worker.open(), (e) => e.status === 0);
  await assert.rejects(f.worker.open(), (e) => e.status === 410 && e.serverError && e.message === 'Playback was stopped by the server admin');
});

test('a stuck worker is replaced and the file opened again', async () => {
  const f = await fixture({ reply: (m) => (m.op === 'pump' ? null : {}) });
  const s = new f.RemuxStream(f.worker, 0);
  const read = s.read();
  await new Promise((r) => setTimeout(r, 5));
  f.worker.restart();
  await assert.rejects(read, (e) => e.name === 'AbortError');
  assert.ok(f.workers[0].terminated);
  await f.worker.open();
  assert.deepEqual(ops(f.workers[1]), ['open']);
  assert.equal(f.probes.length, 2);
});

test('a stream starts the remuxer when first read, so replaced streams cost nothing', async () => {
  const f = await fixture({ reply: (m) => (m.op === 'pump' ? { data: new Uint8Array(4), done: false } : {}) });
  const skipped = new f.RemuxStream(f.worker, 10);
  skipped.cancel();
  const s = new f.RemuxStream(f.worker, 20);
  const { value } = await s.read();
  assert.equal(value.byteLength, 4);
  const posted = f.workers[0].posted;
  assert.deepEqual(posted.map((m) => m.op), ['open', 'start', 'pump']);
  assert.deepEqual([posted[1].time, posted[1].stream, posted[2].stream], [20, s.id, s.id]);
  // The worker's verdict on a replaced stream ends its read quietly.
  const g = await fixture({ reply: (m) => (m.op === 'start' ? { replaced: true } : {}) });
  await assert.rejects(new g.RemuxStream(g.worker, 5).read(), (e) => e.name === 'AbortError');
});
