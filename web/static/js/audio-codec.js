// The WebAssembly AC-3/E-AC-3 → AAC converter (vendor/lexaudio, built from
// wasm/audio/audio.c by scripts/build-audio-wasm.sh). audio-worker.js runs
// it off the main thread; tests call it directly.

import createModule from '../vendor/lexaudio/audio.js';

function check(r, what) {
  if (r < 0) throw new Error(`${what} failed (${r})`);
}

// loadCodec returns handle(request), which serves the requests that
// client-audio.js's AudioConverter makes: open, decode and flush.
export async function loadCodec() {
  const M = await createModule();

  function collect() {
    const ptr = M._la_out(), len = M._la_out_len();
    const d = new DataView(M.HEAPU8.buffer, ptr, len);
    const frames = [];
    for (let at = 0; at < len;) {
      const size = d.getInt32(at + 12, true);
      frames.push({ pts: d.getFloat64(at, true), duration: d.getInt32(at + 8, true), data: M.HEAPU8.slice(ptr + at + 16, ptr + at + 16 + size) });
      at += 16 + size;
    }
    M._la_out_clear();
    let config = null;
    if (M._la_rate()) {
      const p = M._la_config();
      config = { rate: M._la_rate(), channels: M._la_channels(), asc: M.HEAPU8.slice(p, p + M._la_config_len()) };
    }
    return { frames, config };
  }

  return function handle(m) {
    switch (m.op) {
      case 'open':
        check(M._la_open(m.codec === 'ec-3' ? 1 : 0, m.timescale, m.channels, m.bitrate), 'Opening the audio decoder');
        return {};
      case 'decode': {
        let at = 0;
        for (let i = 0; i < m.sizes.length; i++) {
          const ptr = M._la_inbuf(m.sizes[i]);
          M.HEAPU8.set(m.data.subarray(at, at + m.sizes[i]), ptr);
          at += m.sizes[i];
          check(M._la_decode(m.sizes[i], m.pts[i]), 'Decoding audio');
        }
        return collect();
      }
      case 'flush':
        check(M._la_flush(), 'Flushing the audio encoder');
        return collect();
    }
    throw new Error(`unknown request ${m.op}`);
  };
}
