// WorkerRPC is the main-thread side of a request/reply worker
// (audio-worker.js, remux-worker.js): each request carries an id and is
// settled by the reply with the same id. Once the worker fails or is
// stopped, every pending and later request fails.

export class WorkerRPC {
  // failure(message) makes the error for a failed request; loadError is
  // the message when the worker itself doesn't start.
  constructor(url, failure, loadError) {
    this.url = url;
    this.failure = failure;
    this.loadError = loadError;
    this.seq = 0;
    this.spawn();
  }

  spawn() {
    this.pending = new Map();
    this.failed = null;
    this.worker = new Worker(this.url, { type: 'module' });
    this.worker.onmessage = ({ data }) => {
      const p = this.pending.get(data.id);
      if (!p) return;
      this.pending.delete(data.id);
      const err = this.replyError(data);
      if (err) p.reject(err);
      else p.resolve(data);
    };
    this.worker.onerror = (e) => {
      e.preventDefault?.();
      this.fail(this.failure(e.message || this.loadError));
    };
  }

  // The error a reply carries, if any.
  replyError(data) {
    return data.error ? this.failure(data.error) : null;
  }

  fail(err) {
    this.failed = err;
    for (const p of this.pending.values()) p.reject(err);
    this.pending.clear();
  }

  request(msg, transfer = []) {
    if (this.failed) return Promise.reject(this.failed);
    const id = ++this.seq;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.worker.postMessage({ ...msg, id }, transfer);
    });
  }

  terminate() {
    this.worker.terminate();
    this.fail(this.failure('stopped'));
  }
}
