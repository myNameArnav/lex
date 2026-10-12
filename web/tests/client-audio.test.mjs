import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import { AudioConverter } from '../static/js/client-audio.js';
import { loadCodec } from '../static/js/audio-codec.js';

const has = (bin) => { try { execFileSync(bin, ['-version'], { stdio: 'ignore' }); return true; } catch { return false; } };
const skip = !has('ffmpeg') || !has('ffprobe') ? 'ffmpeg not installed' : false;

// The server's remux of a file with E-AC-3 audio: both tracks copied, as
// stream.buildArgs writes it, from start seconds in.
function serverStream(dir, start) {
  const src = join(dir, 'src.mkv');
  execFileSync('ffmpeg', ['-v', 'error', '-y', '-f', 'lavfi', '-i', 'testsrc2=s=320x180:r=24:d=12',
    '-f', 'lavfi', '-i', 'sine=f=440:sample_rate=48000:d=12', '-filter_complex', '[1:a]pan=5.1|FL=c0|FR=c0|FC=c0|LFE=c0|BL=c0|BR=c0[a]',
    '-map', '0:v', '-map', '[a]', '-c:v', 'libx264', '-preset', 'veryfast', '-g', '48', '-c:a', 'eac3', '-b:a', '384k', src]);
  return execFileSync('ffmpeg', ['-v', 'error', ...(start ? ['-ss', String(start)] : []), '-copyts', '-fflags', '+genpts', '-i', src,
    '-map', '0:0', '-c:v', 'copy', '-map', '0:1', '-c:a', 'copy', '-map_metadata', '-1', '-sn', '-dn', '-avoid_negative_ts', 'disabled',
    '-f', 'mp4', '-movflags', '+frag_keyframe+empty_moov+delay_moov+default_base_moof+frag_discont', '-min_frag_duration', '1000000', 'pipe:1'],
  { maxBuffer: 1 << 28 });
}

async function convert(stream) {
  const handle = await loadCodec();
  const worker = { request: async (m) => handle(m) };
  const conv = new AudioConverter(worker, { channels: 2, bitrate: 192 });
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

for (const start of [0, 5]) {
  test(`E-AC-3 becomes stereo AAC alongside the untouched video (from ${start}s)`, { skip }, async () => {
    const dir = mkdtempSync(join(tmpdir(), 'lex-audio-'));
    const file = join(dir, 'out.mp4');
    writeFileSync(file, await convert(serverStream(dir, start)));
    const [v, a] = probe(file);
    assert.equal(v.codec_name, 'h264');
    assert.equal(a.codec_name, 'aac');
    assert.equal(a.channels, 2);
    assert.equal(a.sample_rate, '48000');
    // Audio stays on the file's timeline, next to the video.
    assert.ok(Math.abs(a.start_time - v.start_time) < 0.2, `audio starts at ${a.start_time}, video at ${v.start_time}`);
    // The fragments decode to the whole tone, not silence.
    const res = spawnSync('ffmpeg', ['-i', file, '-map', '0:a', '-af', 'volumedetect', '-f', 'null', '-']).stderr.toString();
    // ffmpeg may set the filter up twice; the last report is the real one.
    const mean = +[...res.matchAll(/mean_volume: (-?[\d.]+) dB/g)].at(-1)[1];
    const seconds = +[...res.matchAll(/n_samples: (\d+)/g)].at(-1)[1] / 2 / 48000;
    assert.ok(mean > -30, `mean volume ${mean} dB`);
    // Copied video starts at the keyframe before start; so does the audio.
    assert.ok(Math.abs(seconds - (12 - a.start_time)) < 0.5, `${seconds}s of audio from ${a.start_time}s`);
  });
}
