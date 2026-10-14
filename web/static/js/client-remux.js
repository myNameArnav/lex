// Browser-side Direct Stream. For Matroska files whose video and audio the
// browser can play (or convert itself), the player reads the original file
// with range requests on the Direct Play URL and remux-worker.js copies the
// tracks into the fragmented MP4 the server's ffmpeg remux would send. The
// server only serves file bytes. MseEngine reads a RemuxStream as it would
// read the server's response.

// The containers the remuxer reads, as the server names them.
export const CLIENT_REMUX = ['mkv', 'webm'];

export const clientRemuxSupported = () => typeof WebAssembly === 'object' && typeof Worker === 'function';

// The player recognises this prefix and lets the server remux instead.
const failure = (msg) => new DOMException(`Browser remux: ${msg}`, 'NotSupportedError');

// RemuxWorker is the main-thread side of remux-worker.js: one per
// playback, opened on the plan's file and audio track.
export class RemuxWorker {
  constructor({ url, audio = -1, fragMs = 0 }) {
    this.seq = 0;
    this.pending = new Map();
    this.fetched = 0;
    this.requests = 0;
    this.worker = new Worker(new URL('./remux-worker.js', import.meta.url), { type: 'module' });
    this.worker.onmessage = ({ data }) => {
      const p = this.pending.get(data.id);
      if (!p) return;
      this.pending.delete(data.id);
      if (data.fetched !== undefined) Object.assign(this, { fetched: data.fetched, requests: data.requests, heap: data.heap });
      if (data.remux) p.reject(failure(data.error));
      // Network and HTTP failures: the stream's usual error handling.
      else if (data.error) p.reject(Object.assign(new Error(data.error), { status: data.status, serverError: data.serverError }));
      else p.resolve(data);
    };
    this.worker.onerror = (e) => {
      e.preventDefault?.();
      this.fail(failure(e.message || "the remuxer didn't load"));
    };
    this.file = { url, audio, fragMs };
  }

  // open reads the file's header once; a lost connection lets the next
  // stream try again.
  open() {
    if (!this.opened) {
      this.opened = this.request({ op: 'open', ...this.file });
      this.opened.catch((e) => { if (e.status !== undefined) this.opened = null; });
    }
    return this.opened;
  }

  fail(err) {
    this.failed = err;
    for (const p of this.pending.values()) p.reject(err);
    this.pending.clear();
  }

  request(msg) {
    if (this.failed) return Promise.reject(this.failed);
    const id = ++this.seq;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.worker.postMessage({ ...msg, id });
    });
  }

  terminate() {
    this.worker.terminate();
    this.fail(failure('stopped'));
  }
}

// RemuxStream is the remuxer's output from time on (a seek starts a new
// one). read() resolves like a fetch body reader's: { done, value }.
export class RemuxStream {
  constructor(worker, time) {
    this.worker = worker;
    this.started = worker.open().then(() => this.request({ op: 'start', time }));
  }

  request(msg) {
    if (this.cancelled) return Promise.reject(new DOMException('Stream replaced', 'AbortError'));
    return this.worker.request(msg);
  }

  async read() {
    if (this.done) return { done: true };
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
