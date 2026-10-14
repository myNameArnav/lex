// Browser-side Direct Stream. For Matroska files whose video and audio the
// browser can play (or convert itself), the player reads the original file
// with range requests on the Direct Play URL and remux-worker.js copies the
// tracks into the fragmented MP4 the server's ffmpeg remux would send. The
// server only serves file bytes. MseEngine reads a RemuxStream as it would
// read the server's response.

import { WorkerRPC } from './worker-rpc.js';
import { streamError } from './stream-error.js';

// The containers the remuxer reads, as the server names them.
export const CLIENT_REMUX = ['mkv', 'webm'];

export const clientRemuxSupported = () => typeof WebAssembly === 'object' && typeof Worker === 'function';

// The player recognises this prefix and lets the server remux instead.
const failure = (msg) => new DOMException(`Browser remux: ${msg}`, 'NotSupportedError');

// The worker's synchronous reads can't stop a download once it has
// started. A proxy that ignores Range would make the first one fetch the
// whole file: check once, with a request that can be aborted, that the
// server answers ranges.
async function probeRanges(url) {
  const ctrl = new AbortController();
  let res;
  try {
    res = await fetch(url, { headers: { Range: 'bytes=0-0' }, credentials: 'same-origin', cache: 'no-store', signal: ctrl.signal });
  } catch {
    throw Object.assign(new Error('Lost connection to the server while streaming.'), { status: 0 });
  }
  try {
    if (res.status === 206 && /\/\d+$/.test(res.headers.get('Content-Range') || '')) return;
    if (res.ok) throw failure('the server ignored a range request');
    const { msg, serverError } = streamError(res.status, await res.text().catch(() => ''));
    throw Object.assign(new Error(msg), { status: res.status, serverError });
  } finally {
    ctrl.abort();
  }
}

// RemuxWorker is the main-thread side of remux-worker.js: one per
// playback, opened on the plan's file and audio track.
export class RemuxWorker extends WorkerRPC {
  constructor({ url, audio = -1, fragMs = 0 }) {
    super(new URL('./remux-worker.js', import.meta.url), failure, "the remuxer didn't load");
    this.file = { url, audio, fragMs };
    this.fetched = 0;
    this.requests = 0;
  }

  replyError(data) {
    if (data.replaced) return new DOMException('Stream replaced', 'AbortError');
    // Replies carry the reader's figures, for Stats for nerds.
    if (data.fetched !== undefined) Object.assign(this, { fetched: data.fetched, requests: data.requests, heap: data.heap });
    if (data.remux) return failure(data.error);
    // Network and HTTP failures: the stream's usual error handling.
    if (data.error) return Object.assign(new Error(data.error), { status: data.status, serverError: data.serverError });
    return null;
  }

  // open reads the file's header once; a lost connection lets the next
  // stream try again.
  open() {
    if (!this.opened) {
      this.opened = probeRanges(this.file.url).then(() => this.request({ op: 'open', ...this.file }));
      this.opened.catch((e) => { if (e.status !== undefined) this.opened = null; });
    }
    return this.opened;
  }

  // restart replaces a worker stuck in a range request, which is
  // synchronous and can't be aborted. The next stream opens the file again.
  restart() {
    this.worker.terminate();
    this.fail(new DOMException('Remuxer restarted', 'AbortError'));
    this.opened = null;
    this.spawn();
  }

}

let streams = 0;

// RemuxStream is the remuxer's output from time on (a seek starts a new
// one). read() resolves like a fetch body reader's: { done, value }. A
// stream starts the remuxer when it's first read, and the worker skips the
// requests of streams a newer one replaced, so scrubbing past many
// positions doesn't make it seek to each in turn.
export class RemuxStream {
  constructor(worker, time) {
    this.worker = worker;
    this.time = time;
    this.id = ++streams;
  }

  request(msg) {
    if (this.cancelled) return Promise.reject(new DOMException('Stream replaced', 'AbortError'));
    return this.worker.request({ ...msg, stream: this.id });
  }

  async read() {
    if (this.done) return { done: true };
    this.started ??= this.worker.open().then(() => this.request({ op: 'start', time: this.time }));
    await this.started;
    const res = await this.request({ op: 'pump' });
    this.done = res.done;
    // The last pump can carry the end of the file as well.
    if (!res.data.byteLength) return { done: true };
    return { done: false, value: res.data };
  }

  // Stops a stream that was replaced: its pending work mustn't reach the
  // worker, which the next stream restarts.
  cancel() {
    this.cancelled = true;
    return Promise.resolve();
  }
}
