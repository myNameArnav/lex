import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { existsSync, mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import { AudioConverter, CLIENT_AUDIO } from '../static/js/client-audio.js';
import { loadCodec } from '../static/js/audio-codec.js';

const has = (bin) => { try { execFileSync(bin, ['-version'], { stdio: 'ignore' }); return true; } catch { return false; } };
const skip = !has('ffmpeg') || !has('ffprobe') ? 'ffmpeg not installed' : false;

// The server's remux of a file with 5.1 audio in codec (encoded at rate,
// keyframes every gop frames at 24 fps): both tracks copied, as
// stream.buildArgs writes it, from start seconds in.
// DTS and TrueHD only have experimental encoders, and ffmpeg calls TrueHD
// in MP4 experimental too.
function serverStream(dir, codec, start, rate = 48000, gop = 48) {
  const src = join(dir, `src-${codec}-${rate}-${gop}.mkv`);
  if (!existsSync(src)) {
    execFileSync('ffmpeg', ['-v', 'error', '-y', '-f', 'lavfi', '-i', 'testsrc2=s=320x180:r=24:d=12',
      '-f', 'lavfi', '-i', `sine=f=440:sample_rate=${rate}:d=12`, '-filter_complex', '[1:a]pan=5.1|FL=c0|FR=c0|FC=c0|LFE=c0|BL=c0|BR=c0[a]',
      '-map', '0:v', '-map', '[a]', '-c:v', 'libx264', '-preset', 'veryfast', '-g', String(gop), '-sc_threshold', '0',
      ...{ eac3: ['-c:a', 'eac3', '-b:a', '384k'], dts: ['-c:a', 'dca', '-b:a', '768k'], truehd: ['-c:a', 'truehd'] }[codec], '-strict', 'experimental', src]);
  }
  return execFileSync('ffmpeg', ['-v', 'error', ...(start ? ['-ss', String(start)] : []), '-copyts', '-fflags', '+genpts', '-i', src,
    '-map', '0:0', '-c:v', 'copy', '-map', '0:1', '-c:a', 'copy', ...(codec === 'truehd' ? ['-strict', 'experimental'] : []),
    '-map_metadata', '-1', '-sn', '-dn', '-avoid_negative_ts', 'disabled',
    '-f', 'mp4', '-movflags', '+frag_keyframe+empty_moov+delay_moov+default_base_moof+frag_discont', '-min_frag_duration', '1000000', 'pipe:1'],
  { maxBuffer: 1 << 28 });
}

async function convert(stream, channels = 2) {
  const handle = await loadCodec();
  const worker = { request: async (m) => handle(m) };
  const conv = new AudioConverter(worker, { channels, bitrate: 192 });
  const out = [];
  // Network-sized reads that split boxes anywhere.
  for (let at = 0, n = 1; at < stream.byteLength; at += n, n = (n * 7919) % 65521 + 1) {
    out.push(...await conv.push(stream.subarray(at, at + n)));
  }
  out.push(...await conv.end());
  return Buffer.concat(out);
}

function probe(file) {
  return JSON.parse(execFileSync('ffprobe', ['-v', 'error', '-show_entries', 'stream=codec_name,channels,sample_rate,start_time,duration',
    '-of', 'json', file]).toString()).streams;
}

test('the browser converts every codec the wasm decodes that MP4 carries', () => {
  // MLP decodes too, but ffmpeg can't put it in MP4.
  assert.deepEqual(CLIENT_AUDIO, ['ac3', 'eac3', 'dts', 'truehd']);
});

const dir = mkdtempSync(join(tmpdir(), 'lex-audio-'));
const names = { eac3: 'E-AC-3', dts: 'DTS', truehd: 'TrueHD' };
const cases = [
  ...['eac3', 'dts', 'truehd'].flatMap((codec) => [0, 5].map((start) => ({ codec, start, channels: 2 }))),
  // Configured for 5.1; 96 kHz goes down to 48 kHz.
  { codec: 'dts', start: 0, channels: 6 },
  { codec: 'truehd', start: 5, channels: 6, rate: 96000 },
  // 10 s fragments: ffmpeg can't fill in the TrueHD moof's size on a pipe.
  { codec: 'truehd', start: 0, channels: 2, gop: 240 },
];
for (const { codec, start, channels, rate, gop } of cases) {
  const what = `${names[codec]}${rate ? ` ${rate / 1000} kHz` : ''}${gop ? ` with ${gop / 24}s keyframes` : ''} becomes ${channels === 2 ? 'stereo' : '5.1'} AAC alongside the untouched video (from ${start}s)`;
  test(what, { skip }, async () => {
    const file = join(dir, `out-${codec}-${start}-${channels}-${rate}-${gop}.mp4`);
    writeFileSync(file, await convert(serverStream(dir, codec, start, rate, gop), channels));
    const [v, a] = probe(file);
    assert.equal(v.codec_name, 'h264');
    assert.equal(a.codec_name, 'aac');
    assert.equal(a.channels, channels);
    assert.equal(a.sample_rate, '48000');
    // Audio stays on the file's timeline, next to the video.
    assert.ok(Math.abs(a.start_time - v.start_time) < 0.2, `audio starts at ${a.start_time}, video at ${v.start_time}`);
    // The fragments decode to the whole tone, not silence.
    const res = spawnSync('ffmpeg', ['-i', file, '-map', '0:a', '-af', 'volumedetect', '-f', 'null', '-']).stderr.toString();
    // ffmpeg may set the filter up twice; the last report is the real one.
    const mean = +[...res.matchAll(/mean_volume: (-?[\d.]+) dB/g)].at(-1)[1];
    const seconds = +[...res.matchAll(/n_samples: (\d+)/g)].at(-1)[1] / channels / 48000;
    assert.ok(mean > -30, `mean volume ${mean} dB`);
    // Copied video starts at the keyframe before start; so does the audio.
    assert.ok(Math.abs(seconds - (12 - a.start_time)) < 0.5, `${seconds}s of audio from ${a.start_time}s`);
  });
}
