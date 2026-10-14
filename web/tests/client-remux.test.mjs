import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdtempSync, statSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { Worker } from 'node:worker_threads';

import { loadRemuxer, MAX_WINDOW } from '../static/js/remux-core.js';
import { AudioConverter } from '../static/js/client-audio.js';
import { loadCodec } from '../static/js/audio-codec.js';
import { BoxReader, children, parseInit } from '../static/js/mp4.js';

const has = (bin, ...args) => { try { execFileSync(bin, args.length ? args : ['-version'], { stdio: 'ignore' }); return true; } catch { return false; } };
const skip = !has('ffmpeg') || !has('ffprobe') ? 'ffmpeg not installed' : false;
const encoders = skip ? '' : execFileSync('ffmpeg', ['-hide_banner', '-encoders']).toString();

const dir = mkdtempSync(join(tmpdir(), 'lex-remux-'));
const ff = (...args) => execFileSync('ffmpeg', ['-v', 'error', '-y', ...args]);
const tone = ['-f', 'lavfi', '-i', 'sine=f=440:sample_rate=48000:d=12'];
const picture = ['-f', 'lavfi', '-i', 'testsrc2=s=320x180:r=24:d=12'];
// x264 with B-frames (b-pyramid: frames reorder two deep) and a keyframe
// every 2 s.
const x264 = ['-c:v', 'libx264', '-preset', 'veryfast', '-bf', '3', '-g', '48', '-keyint_min', '48', '-sc_threshold', '0'];

function fixture(name, ...args) {
  const file = join(dir, name);
  ff(...args, file);
  return file;
}

// A range-capable HTTP server and a synchronous client for it, both in a
// worker thread: the remuxer blocks the calling thread until its read is
// served, as it does in the browser's worker with synchronous requests.
// With split, responses stop short at arbitrary points, as a server may.
const host = `
const http = require('node:http'), fs = require('node:fs');
const { parentPort, workerData: { ctl, data } } = require('node:worker_threads');
const state = new Int32Array(ctl), meta = new Float64Array(ctl, 8), out = new Uint8Array(data);
let log = [], seed = 1;
const server = http.createServer((req, res) => {
  const u = new URL(req.url, 'http://x'), file = u.searchParams.get('f'), size = fs.statSync(file).size;
  const m = /^bytes=(\\d+)-(\\d+)$/.exec(req.headers.range || '');
  if (!m) { res.writeHead(200, { 'Content-Length': size }); fs.createReadStream(file).pipe(res); return; }
  const start = +m[1];
  let end = Math.min(+m[2], size - 1);
  if (u.searchParams.get('split')) { seed = (seed * 16807) % 2147483647; end = Math.min(end, start + seed % 300000); }
  log.push([start, end]);
  res.writeHead(206, { 'Content-Range': 'bytes ' + start + '-' + end + '/' + size, 'Content-Length': end - start + 1 });
  fs.createReadStream(file, { start, end }).pipe(res);
});
server.listen(0, '127.0.0.1', () => parentPort.postMessage({ port: server.address().port }));
parentPort.on('message', async (m) => {
  if (m.log) { parentPort.postMessage({ log }); log = []; return; }
  try {
    const res = await fetch(m.url, { headers: { Range: 'bytes=' + m.start + '-' + m.end } });
    const bytes = new Uint8Array(await res.arrayBuffer());
    out.set(bytes);
    meta[0] = bytes.byteLength;
    meta[1] = Number(/\\/(\\d+)$/.exec(res.headers.get('content-range'))[1]);
    Atomics.store(state, 0, res.status);
  } catch {
    Atomics.store(state, 0, -1);
  }
  Atomics.notify(state, 0);
});
`;

let server;
async function rangeHost() {
  if (server) return server;
  const ctl = new SharedArrayBuffer(24), data = new SharedArrayBuffer(MAX_WINDOW);
  const worker = new Worker(host, { eval: true, workerData: { ctl, data } });
  worker.unref();
  const { port } = await new Promise((res) => worker.once('message', res));
  const state = new Int32Array(ctl), meta = new Float64Array(ctl, 8), bytes = new Uint8Array(data);
  server = {
    url: (file, split) => `http://127.0.0.1:${port}/?f=${encodeURIComponent(file)}${split ? '&split=1' : ''}`,
    get: (url) => (start, end) => {
      Atomics.store(state, 0, 0);
      worker.postMessage({ url, start, end });
      Atomics.wait(state, 0, 0);
      if (Atomics.load(state, 0) !== 206) throw Object.assign(new Error('range request failed'), { status: state[0] });
      return { bytes: bytes.slice(0, meta[0]), size: meta[1] };
    },
    log: () => { worker.postMessage({ log: true }); return new Promise((res) => worker.once('message', (m) => res(m.log))); },
  };
  return server;
}

// The remuxer's whole output from time, as the player's worker produces it.
async function remux(file, { audio = 1, time = 0, split = false, seekFirst } = {}) {
  const host = await rangeHost();
  const handle = await loadRemuxer(host.get(host.url(file, split)));
  handle({ op: 'open', audio, fragMs: 1000 });
  // A seek after playing a while, as the player does.
  if (seekFirst !== undefined) {
    handle({ op: 'start', time: seekFirst });
    handle({ op: 'pump' });
  }
  handle({ op: 'start', time });
  const parts = [];
  let res;
  do {
    res = handle({ op: 'pump' });
    parts.push(res.data);
  } while (!res.done);
  return { mp4: Buffer.concat(parts), stats: res, requests: await host.log() };
}

function write(name, bytes) {
  const file = join(dir, name);
  writeFileSync(file, bytes);
  return file;
}

const probe = (file, what) => JSON.parse(execFileSync('ffprobe', ['-v', 'error', ...what, '-of', 'json', file]).toString());
const streams = (file) => probe(file, ['-show_entries', 'stream=index,codec_name,codec_tag_string,channels,sample_rate,start_time,duration']).streams;
const packets = (file, sel) => probe(file, ['-select_streams', sel, '-show_entries', 'packet=pts,dts,pts_time,dts_time,flags']).packets;

// What MSE sees: the top-level boxes, the tracks, and the video frames in
// decode order with their presentation and decode times (seconds: tfdt,
// sample durations and composition offsets, signed in version 1 truns) and
// keyframe flags. (ffprobe shifts negative composition offsets away.)
function mse(mp4) {
  const r = new BoxReader();
  r.push(mp4);
  const boxes = [], frames = [], frags = [];
  let init, video;
  for (let b; (b = r.next());) {
    boxes.push(b.type);
    if (b.type === 'moov') {
      init = parseInit(b.bytes);
      video = init.tracks.find((t) => t.handler === 'vide');
    }
    if (b.type !== 'moof') continue;
    const m = b.bytes, d = new DataView(m.buffer, m.byteOffset, m.byteLength);
    for (const traf of children(m).filter((c) => c.type === 'traf')) {
      const kids = children(m, traf.body, traf.end), find = (t) => kids.find((c) => c.type === t);
      const tfhd = find('tfhd'), tfdt = find('tfdt');
      const id = d.getUint32(tfhd.body + 4);
      if (id !== video.id) continue;
      const hf = d.getUint32(tfhd.body) & 0xffffff;
      let at = tfhd.body + 8, defDur = init.trex[id].duration, defFlags = init.trex[id].flags;
      if (hf & 0x2) at += 4;
      if (hf & 0x8) { defDur = d.getUint32(at); at += 4; }
      if (hf & 0x10) at += 4;
      if (hf & 0x20) defFlags = d.getUint32(at);
      let dts = m[tfdt.body] === 1 ? Number(d.getBigInt64(tfdt.body + 4)) : d.getUint32(tfdt.body + 4);
      for (const trun of kids.filter((c) => c.type === 'trun')) {
        const v = m[trun.body], f = d.getUint32(trun.body) & 0xffffff, n = d.getUint32(trun.body + 4);
        let p = trun.body + 8, first = null;
        if (f & 0x1) p += 4;
        if (f & 0x4) { first = d.getUint32(p); p += 4; }
        for (let i = 0; i < n; i++) {
          let dur = defDur, flags = i === 0 && first !== null ? first : defFlags, cts = 0;
          if (f & 0x100) { dur = d.getUint32(p); p += 4; }
          if (f & 0x200) p += 4;
          if (f & 0x400) { flags = d.getUint32(p); p += 4; }
          if (f & 0x800) { cts = v ? d.getInt32(p) : d.getUint32(p); p += 4; }
          const frame = { dts: dts / video.timescale, pts: (dts + cts) / video.timescale, key: !(flags & 0x10000) };
          if (i === 0) frags.push(frame);
          frames.push(frame);
          dts += dur;
        }
      }
    }
  }
  return { boxes, init, frames, frags };
}

const ms = (t) => Math.round(t * 1000);

// Every frame of the source from its first keyframe at or after from, at
// its own presentation time, decodable in order.
function checkVideo(src, mp4, from = 0) {
  const want = packets(src, 'v').map((p) => ms(+p.pts_time));
  const { frames } = mse(mp4);
  const got = frames.map((f) => ms(f.pts));
  assert.deepEqual(got, want.slice(want.indexOf(got[0])));
  assert.equal(got[0], from * 1000);
  assert.ok(frames[0].key, 'starts at a keyframe');
  for (let i = 1; i < frames.length; i++) assert.ok(frames[i].dts > frames[i - 1].dts, `frame ${i}: decode time not increasing`);
}

test('H.264 (B-frames) + AAC: the server remux\'s fMP4 with the file\'s timestamps', { skip }, async () => {
  const src = fixture('h264-aac.mkv', ...picture, ...tone, ...x264, '-c:a', 'aac', '-b:a', '128k');
  const { mp4, requests } = await remux(src);
  const out = write('h264-aac.mp4', mp4);
  const [v, a] = streams(out);
  assert.equal(v.codec_name, 'h264');
  assert.equal(a.codec_name, 'aac');
  checkVideo(src, mp4);
  // The audio, packet for packet at the source's times.
  assert.deepEqual(packets(out, 'a').map((p) => ms(+p.pts_time)), packets(src, 'a').map((p) => ms(+p.pts_time)));
  // The same kind of stream as the server's ffmpeg remux: ftyp, moov, then
  // fragments that each start at a keyframe, at least a second long.
  const ours = mse(mp4);
  const theirs = mse(execFileSync('ffmpeg', ['-v', 'error', '-copyts', '-fflags', '+genpts', '-i', src, '-map', '0:0', '-c:v', 'copy', '-map', '0:1', '-c:a', 'copy',
    '-map_metadata', '-1', '-map_chapters', '-1', '-sn', '-dn', '-avoid_negative_ts', 'disabled', '-f', 'mp4',
    '-movflags', '+frag_keyframe+empty_moov+default_base_moof+frag_discont', '-min_frag_duration', '1000000', 'pipe:1'], { maxBuffer: 1 << 28 }));
  assert.deepEqual(ours.boxes, theirs.boxes);
  assert.deepEqual(ours.init.tracks.map((t) => [t.handler, t.codec, t.timescale]), theirs.init.tracks.map((t) => [t.handler, t.codec, t.timescale]));
  assert.deepEqual(ours.frags.map((f) => [f.pts, f.key]), [[0, true], [2, true], [4, true], [6, true], [8, true], [10, true]]);
  // Unlike the server's remux, whose video shows a B-frame delay late.
  assert.equal(ms(theirs.frames[0].pts), 83);
  // Read in bounded range requests, about once.
  const size = statSync(src).size;
  const read = requests.reduce((n, [s, e]) => n + e - s + 1, 0);
  assert.ok(requests.every(([s, e]) => e - s < MAX_WINDOW), 'no request over the window');
  assert.ok(read < size * 1.5, `read ${read} of ${size} bytes`);
});

test('reads cut short at arbitrary points give the same bytes', { skip }, async () => {
  const src = join(dir, 'h264-aac.mkv');
  const whole = await remux(src);
  const split = await remux(src, { split: true });
  assert.ok(split.requests.length > whole.requests.length * 2, `${split.requests.length} vs ${whole.requests.length} requests`);
  assert.ok(Buffer.compare(whole.mp4, split.mp4) === 0, 'output differs');
});

test('a seek starts a fresh init segment at the keyframe before the target', { skip }, async () => {
  const src = join(dir, 'h264-aac.mkv');
  // After reading from 7 s, seeking back (as the player restarts at a seek).
  const { mp4 } = await remux(src, { time: 5, seekFirst: 7 });
  const a = packets(write('seek.mp4', mp4), 'a');
  // The cluster with the keyframe starts with a little audio before it.
  assert.ok(+a[0].pts_time > 3.9 && +a[0].pts_time <= 4, `audio from ${a[0].pts_time}`);
  const { boxes, frags } = mse(mp4);
  assert.deepEqual(boxes.slice(0, 2), ['ftyp', 'moov']);
  assert.deepEqual(frags.map((f) => [f.pts, f.key]), [[4, true], [6, true], [8, true], [10, true]]);
  checkVideo(src, mp4, 4);
});

test('E-AC-3: remuxed, then converted to AAC by the browser audio converter', { skip }, async () => {
  const src = fixture('h264-eac3.mkv', ...picture, ...tone, ...x264, '-c:a', 'eac3', '-b:a', '384k', '-ac', '6');
  const { mp4 } = await remux(src, { time: 3, seekFirst: 0 });
  const handle = await loadCodec();
  const conv = new AudioConverter({ request: async (m) => handle(m) }, { channels: 2, bitrate: 192 });
  const segs = [];
  for (let at = 0, n = 1; at < mp4.byteLength; at += n, n = (n * 7919) % 65521 + 1) segs.push(...await conv.push(mp4.subarray(at, at + n)));
  segs.push(...await conv.end());
  const out = write('eac3.mp4', Buffer.concat(segs));
  const [v, a] = streams(out);
  assert.equal(v.codec_name, 'h264');
  assert.equal(a.codec_name, 'aac');
  assert.equal(a.channels, 2);
  checkVideo(src, Buffer.concat(segs), 2);
  // ffprobe's duration is the end time here.
  assert.ok(Math.abs(a.start_time - 2) < 0.1, `audio starts at ${a.start_time}`);
  assert.ok(Math.abs(+a.duration - 12) < 0.1, `audio ends at ${a.duration}`);
});

test('HEVC (B-frames) is tagged hvc1 like the server\'s remux', { skip: skip || (!encoders.includes('libx265') && 'no libx265') }, async () => {
  const src = fixture('hevc.mkv', ...picture, ...tone, '-c:v', 'libx265', '-preset', 'ultrafast', '-x265-params', 'bframes=4:b-pyramid=1:keyint=48:min-keyint=48:scenecut=0:log-level=error', '-c:a', 'aac');
  const { mp4 } = await remux(src, { time: 6.5, seekFirst: 2 });
  const [v] = streams(write('hevc.mp4', mp4));
  assert.equal(v.codec_name, 'hevc');
  assert.equal(v.codec_tag_string, 'hvc1');
  checkVideo(src, mp4, 6);
  const all = await remux(src);
  checkVideo(src, all.mp4);
});

test('subtitles, attachments and other audio tracks are left out', { skip }, async () => {
  const font = join(dir, 'font.ttf');
  writeFileSync(font, Buffer.alloc(300_000, 7));
  const srt = join(dir, 'subs.srt');
  writeFileSync(srt, '1\n00:00:01,000 --> 00:00:03,000\nHello\n\n2\n00:00:05,000 --> 00:00:07,000\nWorld\n');
  const src = fixture('extras.mkv', ...picture, ...tone, '-f', 'lavfi', '-i', 'sine=f=220:sample_rate=48000:d=12', '-i', srt,
    '-map', '0:v', '-map', '1:a', '-map', '2:a', '-map', '3:s', ...x264, '-c:a:0', 'aac', '-c:a:1', 'libopus', '-c:s', 'srt',
    '-attach', font, '-metadata:s:t', 'mimetype=application/x-truetype-font');
  // The second audio track (stream 2, Opus) was picked.
  const { mp4 } = await remux(src, { audio: 2 });
  const out = write('extras.mp4', mp4);
  assert.deepEqual(streams(out).map((x) => x.codec_name), ['h264', 'opus']);
  checkVideo(src, mp4);
});

// A film whose first GOPs have no B-frames and later ones reorder deeply
// (b-adapt choosing none on a static opening, anime at bframes=16).
test('frames that reorder deeper later in the file are all copied', { skip }, async () => {
  const part = (name, bf) => fixture(name, '-f', 'lavfi', '-i', 'testsrc2=s=320x180:r=24:d=6', '-c:v', 'libx264', '-preset', 'veryfast',
    '-bf', String(bf), '-b-pyramid', 'normal', '-g', '48', '-keyint_min', '48', '-sc_threshold', '0');
  const list = join(dir, 'parts.txt');
  writeFileSync(list, `file '${part('flat.mkv', 0)}'\nfile '${part('deep.mkv', 8)}'\nfile '${part('deeper.mkv', 16)}'\n`);
  const src = fixture('reorder.mkv', '-f', 'concat', '-safe', '0', '-i', list, '-c', 'copy');
  checkVideo(src, (await remux(src, { audio: -1 })).mp4);
  checkVideo(src, (await remux(src, { audio: -1, time: 9, seekFirst: 0 })).mp4, 8);
});

test('files the remuxer can\'t copy fail with a remuxer error', { skip }, async () => {
  const src = fixture('mpeg4.mkv', ...picture, '-c:v', 'mpeg4');
  const host = await rangeHost();
  const handle = await loadRemuxer(host.get(host.url(src)));
  assert.throws(() => handle({ op: 'open', audio: -1 }), (e) => e.remux && /unsupported video codec mpeg4/.test(e.message));
  await host.log();
});
