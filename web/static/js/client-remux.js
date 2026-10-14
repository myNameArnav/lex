// Browser-side Direct Stream. For Matroska files whose video and audio the
// browser can play (or convert itself), the player reads the original file
// with range requests on the Direct Play URL and remux-worker.js copies the
// tracks into the fragmented MP4 the server's ffmpeg remux would send. The
// server only serves file bytes. MseEngine reads a RemuxStream as it would
// read the server's response.

import { WorkerRPC } from './worker-rpc.js';

// The containers the remuxer reads, as the server names them.
export const CLIENT_REMUX = ['mkv', 'webm'];

export const clientRemuxSupported = () => typeof WebAssembly === 'object' && typeof Worker === 'function';

// The player recognises this prefix and lets the server remux instead.
const failure = (msg) => new DOMException(`Browser remux: ${msg}`, 'NotSupportedError');

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
      this.opened = this.request({ op: 'open', ...this.file });
      this.opened.catch((e) => { if (e.status !== undefined) this.opened = null; });
    }
    return this.opened;
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
