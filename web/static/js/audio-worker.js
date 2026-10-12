// Runs the audio converter (audio-codec.js) off the main thread. Requests
// are handled in order; each reply carries the request's id.

import { loadCodec } from './audio-codec.js';

const loading = loadCodec();

self.onmessage = async ({ data: m }) => {
  try {
    const res = (await loading)(m);
    self.postMessage({ id: m.id, ...res }, res.frames ? res.frames.map((f) => f.data.buffer) : []);
  } catch (e) {
    self.postMessage({ id: m.id, error: e.message || String(e) });
  }
};
