// Runs the remuxer (remux-core.js) off the main thread. WebAssembly can't
// wait for a fetch, so the file is read with synchronous range requests,
// which only workers may make. Requests are handled in order; each reply
// carries the request's id.

import { loadRemuxer, RemuxError } from './remux-core.js';

// A synchronous range request: { bytes, size } or an error with the HTTP
// status (0: no response).
function rangeGetter(url) {
  return (start, end) => {
    const x = new XMLHttpRequest();
    x.open('GET', url, false);
    x.responseType = 'arraybuffer';
    x.setRequestHeader('Range', `bytes=${start}-${end}`);
    try {
      x.send();
    } catch {
      throw Object.assign(new Error('Lost connection to the server while streaming.'), { status: 0 });
    }
    if (x.status === 206) {
      const size = Number(/\/(\d+)$/.exec(x.getResponseHeader('Content-Range') || '')?.[1]);
      if (size > 0) return { bytes: new Uint8Array(x.response), size };
    }
    // The whole file instead of the range: something between us and the
    // server ignores Range, so let the server remux.
    if (x.status === 200 || x.status === 206) throw new RemuxError('the server ignored a range request');
    let msg = `Stream failed (HTTP ${x.status})`;
    try { msg = JSON.parse(new TextDecoder().decode(x.response)).error || msg; } catch {}
    throw Object.assign(new Error(msg), { status: x.status, serverError: msg !== `Stream failed (HTTP ${x.status})` });
  };
}

let handle;
let queue = Promise.resolve();

self.onmessage = ({ data: m }) => {
  queue = queue.then(async () => {
    try {
      // Opened again after a failed attempt (a lost connection): the same module.
      if (m.op === 'open' && !handle) handle = await loadRemuxer(rangeGetter(m.url));
      const res = handle(m);
      self.postMessage({ id: m.id, ...res }, res.data ? [res.data.buffer] : []);
    } catch (e) {
      self.postMessage({ id: m.id, error: e.message || String(e), status: e.status, serverError: e.serverError, remux: e.status === undefined });
    }
  });
};
