// Browser-side audio conversion. When the browser can't decode a file's
// audio (AC-3/E-AC-3 in Firefox and Chrome), the server copies it into the
// stream untouched and AudioConverter swaps it for AAC on the way into
// Media Source Extensions: video fragments pass through, audio samples go
// through the WebAssembly decoder/encoder in audio-worker.js.

import { BoxReader, parseInit, parseFragment, initSegment, audioTrak, audioTrex, videoFragment, audioFragment } from './mp4.js';

// The codecs the converter decodes, as the server names them.
export const CLIENT_AUDIO = ['ac3', 'eac3'];

export const clientAudioSupported = () => typeof WebAssembly === 'object' && typeof Worker === 'function';

const failure = (msg) => new DOMException(`Browser audio decoding: ${msg}`, 'NotSupportedError');

// AudioWorker is the main-thread side of audio-worker.js.
export class AudioWorker {
  constructor() {
    this.seq = 0;
    this.pending = new Map();
    this.worker = new Worker(new URL('./audio-worker.js', import.meta.url), { type: 'module' });
    this.worker.onmessage = ({ data }) => {
      const p = this.pending.get(data.id);
      if (!p) return;
      this.pending.delete(data.id);
      if (data.error) p.reject(failure(data.error));
      else p.resolve(data);
    };
    this.worker.onerror = (e) => {
      e.preventDefault?.();
      this.fail(failure(e.message || "the decoder didn't load"));
    };
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
    this.fail(failure('stopped'));
  }
}

// How many video fragments may wait for the first decoded audio (which
// the init segment needs) before giving up.
const MAX_WAITING = 8;

// AudioConverter rewrites one server stream. push() takes the stream's
// bytes in order and returns the segments to append to the SourceBuffer.
export class AudioConverter {
  constructor(worker, { channels = 2, bitrate = 192 } = {}) {
    this.worker = worker;
    this.channels = channels;
    this.bitrate = bitrate;
    this.reader = new BoxReader();
    this.seq = 0;
    this.waiting = [];
  }

  // cancel stops a converter whose stream was replaced (a seek): its
  // pending work mustn't reach the worker, which the new stream reopens.
  cancel() { this.cancelled = true; }

  request(msg, transfer) {
    if (this.cancelled) return Promise.reject(new DOMException('Stream replaced', 'AbortError'));
    return this.worker.request(msg, transfer);
  }

  async push(bytes) {
    this.reader.push(bytes);
    const out = [];
    for (let b; (b = this.reader.next());) {
      switch (b.type) {
        case 'ftyp': this.ftyp = b.bytes; break;
        case 'moov': await this.openInit(b.bytes); break;
        case 'moof': this.moof = b.bytes; break;
        case 'mdat':
          if (!this.moof || !this.init) break;
          out.push(...await this.fragment(this.moof, b.bytes));
          this.moof = null;
          break;
      }
    }
    return out;
  }

  // end() flushes the encoder at the end of the stream.
  async end() {
    if (!this.audio) return [];
    const res = await this.request({ op: 'flush' });
    return this.output(null, res);
  }

  async openInit(moov) {
    if (!this.ftyp) throw failure('the stream has no ftyp');
    this.init = parseInit(moov);
    this.audio = this.init.tracks.find((t) => t.handler === 'soun');
    if (!this.audio || !['ac-3', 'ec-3'].includes(this.audio.codec)) throw failure(`unexpected audio track ${this.audio?.codec || 'none'}`);
    await this.request({ op: 'open', codec: this.audio.codec, timescale: this.audio.timescale, channels: this.channels, bitrate: this.bitrate });
  }

  async fragment(moof, mdat) {
    const trafs = parseFragment(moof, mdat, this.init.trex);
    const video = trafs.filter((t) => t.id !== this.audio.id);
    const audio = trafs.find((t) => t.id === this.audio.id);
    const vfrag = video.length ? videoFragment(++this.seq, moof, video) : null;
    if (!audio || !audio.samples.length) return this.output(vfrag, null);
    const sizes = audio.samples.map((s) => s.data.byteLength);
    const data = new Uint8Array(sizes.reduce((a, b) => a + b, 0));
    let at = 0;
    for (const s of audio.samples) { data.set(s.data, at); at += s.data.byteLength; }
    const res = await this.request({ op: 'decode', data, sizes, pts: audio.samples.map((s) => s.time) }, [data.buffer]);
    return this.output(vfrag, res);
  }

  // output turns a video fragment and converted audio into segments. Until
  // the encoder reports its configuration there's no init segment, so
  // video fragments wait.
  output(vfrag, res) {
    const out = [];
    if (res?.config && !this.config) {
      this.config = res.config;
      const trak = audioTrak({ id: this.audio.id, rate: res.config.rate, channels: res.config.channels, asc: res.config.asc, bitrate: this.bitrate * 1000 });
      out.push(initSegment(this.ftyp, this.init, this.audio.id, trak, audioTrex(this.audio.id)), ...this.waiting);
      this.waiting = [];
    }
    if (vfrag) {
      if (this.config) out.push(vfrag);
      else if (this.waiting.push(vfrag) > MAX_WAITING) throw failure('no audio could be decoded');
    }
    // AAC frames before zero are encoder priming; MP4 can't place them.
    const frames = (res?.frames || []).filter((f) => f.pts >= 0);
    // A fragment per contiguous run (a gap in the source restarts timing).
    for (let i = 0; i < frames.length;) {
      let j = i + 1;
      while (j < frames.length && frames[j].pts === frames[j - 1].pts + frames[j - 1].duration) j++;
      if (this.config) out.push(audioFragment(++this.seq, this.audio.id, frames.slice(i, j)));
      i = j;
    }
    return out;
  }
}
