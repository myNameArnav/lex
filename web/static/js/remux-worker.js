// Runs the remuxer (remux-core.js) off the main thread. WebAssembly can't
// wait for a fetch, so the file is read with synchronous range requests,
// which only workers may make. Requests are handled in order; each reply
// carries the request's id.

import { loadRemuxer, RemuxError } from './remux-core.js';
import { streamError } from './stream-error.js';

// A range request (at most 4 MB) that takes longer than this has stalled;
// a synchronous one can't be aborted any other way.
const READ_TIMEOUT = 30000;

// A synchronous range request: { bytes, size } or an error with the HTTP
// status (0: no response).
function rangeGetter(url) {
  return (start, end) => {
    const x = new XMLHttpRequest();
    x.open('GET', url, false);
    x.responseType = 'arraybuffer';
    x.timeout = READ_TIMEOUT;
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
    const { msg, serverError } = streamError(x.status, new TextDecoder().decode(x.response));
    throw Object.assign(new Error(msg), { status: x.status, serverError });
  };
}

let handle;
let queue = Promise.resolve();
// The newest stream (a seek) the player has asked for: requests of older
// ones still queued are skipped rather than run.
let latest = 0;

// Lets the messages that arrived while a request ran be delivered (and
// update latest) before the next one runs.
const delivered = () => new Promise((resolve) => {
  const c = new MessageChannel();
  c.port1.onmessage = () => { c.port1.close(); resolve(); };
  c.port2.postMessage(0);
});

self.onmessage = ({ data: m }) => {
  if (m.stream > latest) latest = m.stream;
  queue = queue.then(async () => {
    await delivered();
    if (m.stream < latest) {
      self.postMessage({ id: m.id, replaced: true });
      return;
    }
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
