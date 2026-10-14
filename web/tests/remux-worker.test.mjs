import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';

// Runs remux-worker.js against a fake synchronous XMLHttpRequest and a fake
// remuxer that records what it was asked to do.
async function worker({ xhr = () => ({ status: 206, size: 100 }) } = {}) {
  const replies = [], ran = [], requests = [];
  class XMLHttpRequest {
    open(method, url, sync) { this.sync = sync; }
    setRequestHeader(k, v) { this.range = v; }
    getResponseHeader() { return `bytes 0-9/${this.size}`; }
    send() {
      requests.push(this);
      Object.assign(this, xhr(this));
      if (this.throws) throw this.throws;
      this.response = this.body || new ArrayBuffer(10);
    }
  }
  const self = { postMessage: (m) => replies.push(m) };
  const context = vm.createContext({ self, XMLHttpRequest, MessageChannel, TextDecoder, Uint8Array, Error, Object, Number, Promise, JSON, DOMException });
  const load = (name) => readFile(new URL(`../static/js/${name}`, import.meta.url), 'utf8');
  const module = new vm.SourceTextModule(await load('remux-worker.js'), { context });
  await module.link(async (specifier) => {
    if (specifier === './stream-error.js') return new vm.SourceTextModule(await load('stream-error.js'), { context });
    return new vm.SyntheticModule(['loadRemuxer', 'RemuxError'], function () {
      this.setExport('RemuxError', class extends Error {});
      this.setExport('loadRemuxer', async (get) => (m) => {
        ran.push(m.op + (m.stream ? m.stream : ''));
        if (m.op === 'open') get(0, 9);
        return {};
      });
    }, { context });
  });
  await module.evaluate();
  const settle = async () => { for (let i = 0; i < 20; i++) await new Promise((r) => setTimeout(r, 1)); };
  return { post: (m) => self.onmessage({ data: m }), replies, ran, requests, settle };
}

test('range requests are synchronous, time out, and a stalled one is a lost connection', async () => {
  const w = await worker({ xhr: () => ({ throws: new DOMException('timed out', 'TimeoutError') }) });
  w.post({ id: 1, op: 'open', url: '/f' });
  await w.settle();
  assert.equal(w.requests[0].sync, false);
  assert.ok(w.requests[0].timeout > 0 && w.requests[0].timeout <= 60000, `timeout ${w.requests[0].timeout}`);
  assert.equal(w.replies[0].status, 0);
  assert.equal(w.replies[0].remux, false);
});

test('Lex\'s JSON errors and other HTTP failures are told apart', async () => {
  for (const [body, serverError, error] of [['{"error":"Playback was stopped by the server admin"}', true, 'Playback was stopped by the server admin'], ['<html>', false, 'Stream failed (HTTP 410)']]) {
    const w = await worker({ xhr: () => ({ status: 410, body: new TextEncoder().encode(body).buffer }) });
    w.post({ id: 1, op: 'open', url: '/f' });
    await w.settle();
    assert.deepEqual([w.replies[0].status, w.replies[0].serverError, w.replies[0].error], [410, serverError, error]);
  }
});
