// The WebAssembly Matroska → fragmented MP4 remuxer (vendor/lexremux, built
// from wasm/remux/remux.c by scripts/build-remux-wasm.sh). remux-worker.js
// runs it off the main thread with synchronous range requests; tests call
// it directly.

import createModule from '../vendor/lexremux/remux.js';

// Range requests start small after a jump (the header, the index, a seek
// target) and double while the remuxer reads straight through, so only one
// window of at most MAX_WINDOW bytes of the file is ever held.
export const MIN_WINDOW = 256 << 10;
export const MAX_WINDOW = 4 << 20;

// A synchronous request can't be aborted, so a seek waits for the one
// under way. On a slow link a window grows only as large as the measured
// read rate fetches in about this long (never under MIN_WINDOW).
export const TARGET_MS = 750;
// Weight of the newest request in the smoothed read rate.
const RATE_WEIGHT = 0.5;

// A failure of the remuxer itself (not of the network): the player then
// lets the server remux instead.
export class RemuxError extends Error {
  constructor(msg) { super(msg); this.remux = true; }
}

// RangeReader serves the remuxer's reads from a window of the file fetched
// by get(start, end) (inclusive), which returns { bytes, size } synchronously.
// now() is a clock in milliseconds, for the read rate.
export class RangeReader {
  constructor(get, now = () => performance.now()) {
    this.get = get;
    this.now = now;
    // Bytes per second over recent requests (0: none yet). Request
    // latency counts too, so small windows measure low and grow while the
    // link keeps up.
    this.rate = 0;
    this.start = 0;
    this.bytes = new Uint8Array(0);
    this.span = MIN_WINDOW / 2;
    this.size = 0;
    this.fetched = 0;
    this.requests = 0;
  }

  fill(pos) {
    const straight = this.requests > 0 && pos === this.start + this.bytes.byteLength;
    this.span = straight ? Math.min(this.span * 2, MAX_WINDOW, this.limit()) : MIN_WINDOW;
    const end = (this.size ? Math.min(this.size, pos + this.span) : pos + this.span) - 1;
    const t0 = this.now();
    const { bytes, size } = this.get(pos, end);
    this.measure(bytes.byteLength, this.now() - t0);
    this.requests++;
    this.fetched += bytes.byteLength;
    if (!bytes.byteLength) throw new RemuxError('the file ended early');
    this.start = pos;
    this.bytes = bytes;
    this.size = size;
  }

  // The largest window the link reads in about TARGET_MS.
  limit() {
    if (!this.rate) return MAX_WINDOW;
    return Math.max(MIN_WINDOW, Math.floor(this.rate * TARGET_MS / 1000));
  }

  measure(n, ms) {
    if (!n) return;
    const sample = n * 1000 / Math.max(ms, 1);
    this.rate = this.rate ? this.rate + RATE_WEIGHT * (sample - this.rate) : sample;
  }

  // Copies up to len bytes at pos into heap[ptr…]; returns how many.
  read(heap, ptr, len, pos) {
    if (pos < this.start || pos >= this.start + this.bytes.byteLength) this.fill(pos);
    const at = pos - this.start, n = Math.min(len, this.bytes.byteLength - at);
    heap.set(this.bytes.subarray(at, at + n), ptr);
    return n;
  }
}

// loadRemuxer returns handle(request) for the requests client-remux.js
// makes: open (the file), start (at a time) and pump (the next fragment).
export async function loadRemuxer(get) {
  const M = await createModule();
  const reader = new RangeReader(get);
  let readError = null;
  M.lexRead = (ptr, len, pos) => {
    try {
      return reader.read(M.HEAPU8, ptr, len, pos);
    } catch (e) {
      readError = e;
      return -1;
    }
  };
  const check = (r, what) => {
    if (r >= 0) return r;
    // A failed range request is what really went wrong.
    const e = readError || new RemuxError(`${what} failed: ${M.UTF8ToString(M._rx_error()) || r}`);
    readError = null;
    throw e;
  };
  const take = () => {
    const ptr = M._rx_out(), len = M._rx_out_len();
    const data = M.HEAPU8.slice(ptr, ptr + len);
    M._rx_out_clear();
    return data;
  };
  const stats = () => ({ fetched: reader.fetched, requests: reader.requests, rate: reader.rate, heap: M.HEAPU8.byteLength });

  return function handle(m) {
    switch (m.op) {
      case 'open':
        reader.fill(0);
        check(M._rx_open(reader.size, m.audio ?? -1, m.fragMs || 0), 'Opening the file');
        return { size: reader.size, ...stats() };
      case 'start':
        check(M._rx_start(m.time || 0), 'Starting');
        return stats();
      case 'pump': {
        const done = check(M._rx_pump(), 'Remuxing') === 1;
        return { data: take(), done, ...stats() };
      }
    }
    throw new Error(`unknown request ${m.op}`);
  };
}
