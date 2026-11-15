// Just enough ISO BMFF (MP4) to take apart the server's fragmented MP4 and
// put it back together with a different audio track: a streaming box
// splitter, parsers for the init segment and movie fragments, and writers
// for an AAC track.

const TEXT = new TextDecoder('latin1');

export const fourcc = (b, at) => TEXT.decode(b.subarray(at, at + 4));
const view = (b) => new DataView(b.buffer, b.byteOffset, b.byteLength);

// BoxReader cuts a byte stream into whole top-level boxes.
export class BoxReader {
  constructor() {
    this.buf = new Uint8Array(1 << 16);
    this.len = 0;
  }

  push(bytes) {
    if (this.len + bytes.byteLength > this.buf.byteLength) {
      let cap = this.buf.byteLength;
      while (cap < this.len + bytes.byteLength) cap *= 2;
      const b = new Uint8Array(cap);
      b.set(this.buf.subarray(0, this.len));
      this.buf = b;
    }
    this.buf.set(bytes, this.len);
    this.len += bytes.byteLength;
  }

  // The next complete box as { type, bytes } (bytes is a copy), or null.
  next() {
    if (this.len < 8) return null;
    const d = view(this.buf);
    let size = d.getUint32(0);
    if (size === 1) {
      if (this.len < 16) return null;
      size = Number(d.getBigUint64(8));
    } else if (size === 0 && fourcc(this.buf, 4) === 'moof') {
      if ((size = this.repairMoof()) < 0) return null;
    } else if (size < 8) {
      throw new Error(`unsupported MP4 box size ${size}`);
    }
    if (this.len < size) return null;
    const box = { type: fourcc(this.buf, 4), bytes: this.buf.slice(0, size) };
    this.buf.copyWithin(0, size, this.len);
    this.len -= size;
    return box;
  }

  // ffmpeg writes a box's size as a placeholder and seeks back to fill it
  // in. On a pipe that only works while the box is still in its 32 KB write
  // buffer; otherwise the size stays 0 and ffmpeg writes it after the box
  // instead. Long fragments of TrueHD (1200 samples a second, 8 bytes each
  // in the trun) arrive like that. repairMoof fills in the first box's
  // sizes and drops the stray ones, which leaves the moof ffmpeg meant (its
  // data offsets assume that), or returns -1 until the moof has arrived.
  repairMoof() {
    const b = this.buf, d = view(b);
    const size = (at) => {
      if (at + 16 > this.len) return -1;
      let n = d.getUint32(at);
      if (n) return n;
      const type = fourcc(b, at + 4);
      if (type === 'trun') {
        const flags = d.getUint32(at + 8), perSample = [0x100, 0x200, 0x400, 0x800].filter((f) => flags & f).length * 4;
        n = 16 + (flags & 0x1 ? 4 : 0) + (flags & 0x4 ? 4 : 0) + d.getUint32(at + 12) * perSample;
      } else if (type === 'moof' || type === 'traf') {
        // Children run up to the mdat (or, in a traf, the next traf).
        for (n = 8; ;) {
          const p = at + n;
          if (p + 8 > this.len) return -1;
          if (!/^[a-z0-9]{4}$/.test(fourcc(b, p + 4))) {
            // Not a box: a size ffmpeg couldn't put in its place.
            b.copyWithin(p, p + 4, this.len);
            this.len -= 4;
            continue;
          }
          const next = fourcc(b, p + 4);
          if (next === 'mdat' || (type === 'traf' && next === 'traf')) break;
          const c = size(p);
          if (c < 0) return -1;
          n += c;
        }
      } else {
        throw new Error(`unsupported MP4 box size 0 (${type})`);
      }
      d.setUint32(at, n);
      return n;
    };
    return size(0);
  }
}

// The child boxes inside b[start, end) as { type, start, end, body }, where
// body is the offset of the payload.
export function children(b, start = 8, end = b.byteLength) {
  const d = view(b), out = [];
  for (let at = start; at + 8 <= end;) {
    let size = d.getUint32(at), hdr = 8;
    if (size === 1) { size = Number(d.getBigUint64(at + 8)); hdr = 16; } else if (size === 0) size = end - at;
    if (size < hdr || at + size > end) throw new Error('malformed MP4 box');
    out.push({ type: fourcc(b, at + 4), start: at, end: at + size, body: at + hdr });
    at += size;
  }
  return out;
}

const child = (b, box, type) => children(b, box.body, box.end).find((c) => c.type === type);

// objectType reads the objectTypeIndication of an esds box (ES_Descriptor
// → DecoderConfigDescriptor, ISO 14496-1), or 0.
function objectType(b, esds) {
  let at = esds.body + 4;
  const tag = () => {
    const t = b[at++];
    for (let i = 0; i < 4 && b[at++] & 0x80; i++); // the size, 1-4 bytes
    return t;
  };
  if (tag() !== 3) return 0;
  const flags = b[at + 2];
  at += 3;
  if (flags & 0x80) at += 2; // dependsOn_ES_ID
  if (flags & 0x40) at += 1 + b[at]; // URL
  if (flags & 0x20) at += 2; // OCR_ES_Id
  return at < esds.end && tag() === 4 && at < esds.end ? b[at] : 0;
}

// parseInit reads the tracks of a moov box.
export function parseInit(moov) {
  const d = view(moov), top = children(moov);
  const tracks = [];
  for (const trak of top.filter((c) => c.type === 'trak')) {
    const tkhd = child(moov, trak, 'tkhd');
    const mdia = child(moov, trak, 'mdia');
    const mdhd = child(moov, mdia, 'mdhd');
    const hdlr = child(moov, mdia, 'hdlr');
    const stbl = child(moov, child(moov, mdia, 'minf'), 'stbl');
    const stsd = child(moov, stbl, 'stsd');
    const entry = children(moov, stsd.body + 8, stsd.end)[0];
    const t = {
      id: d.getUint32(tkhd.body + (moov[tkhd.body] === 1 ? 20 : 12)),
      timescale: d.getUint32(mdhd.body + (moov[mdhd.body] === 1 ? 20 : 12)),
      handler: fourcc(moov, hdlr.body + 8),
      codec: entry ? entry.type : '',
      bytes: moov.subarray(trak.start, trak.end),
    };
    if (t.handler === 'soun' && entry) {
      t.channels = d.getUint16(entry.body + 16);
      t.sampleRate = d.getUint16(entry.body + 24);
      const esds = entry.type === 'mp4a' && children(moov, entry.body + 28, entry.end).find((c) => c.type === 'esds');
      if (esds) t.objectType = objectType(moov, esds);
    }
    tracks.push(t);
  }
  const mvex = top.find((c) => c.type === 'mvex');
  const trex = {}, mvexOther = [];
  for (const c of mvex ? children(moov, mvex.body, mvex.end) : []) {
    if (c.type === 'trex') {
      trex[d.getUint32(c.body + 4)] = {
        duration: d.getUint32(c.body + 12), size: d.getUint32(c.body + 16), flags: d.getUint32(c.body + 20),
        bytes: moov.subarray(c.start, c.end),
      };
    } else mvexOther.push(moov.subarray(c.start, c.end));
  }
  const mvhd = top.find((c) => c.type === 'mvhd');
  return { tracks, trex, mvexOther, mvhd: moov.subarray(mvhd.start, mvhd.end) };
}

// parseFragment reads a moof and its mdat into tracks of samples. Sample
// offsets are relative to the start of the moof (default-base-is-moof, as
// ffmpeg's +default_base_moof writes them).
export function parseFragment(moof, mdat, trex) {
  const d = view(moof), trafs = [];
  for (const traf of children(moof).filter((c) => c.type === 'traf')) {
    const tfhd = child(moof, traf, 'tfhd');
    const flags = d.getUint32(tfhd.body) & 0xffffff;
    if (flags & 0x1 || !(flags & 0x20000)) throw new Error('unsupported fragment base offset');
    const id = d.getUint32(tfhd.body + 4);
    const def = { ...(trex[id] || { duration: 0, size: 0, flags: 0 }) };
    let at = tfhd.body + 8;
    if (flags & 0x2) at += 4;
    if (flags & 0x8) { def.duration = d.getUint32(at); at += 4; }
    if (flags & 0x10) { def.size = d.getUint32(at); at += 4; }
    if (flags & 0x20) def.flags = d.getUint32(at);
    // Read as signed: ffmpeg (-avoid_negative_ts disabled) starts audio a
    // few samples before zero.
    const tfdt = child(moof, traf, 'tfdt');
    let time = !tfdt ? 0 : moof[tfdt.body] === 1 ? Number(d.getBigInt64(tfdt.body + 4)) : d.getUint32(tfdt.body + 4);
    const t = { id, start: traf.start, end: traf.end, baseTime: time, runs: [], samples: [] };
    for (const trun of children(moof, traf.body, traf.end).filter((c) => c.type === 'trun')) {
      const tf = d.getUint32(trun.body) & 0xffffff;
      const count = d.getUint32(trun.body + 4);
      let p = trun.body + 8, offset = 0, offsetAt = -1;
      if (tf & 0x1) { offsetAt = p; offset = d.getInt32(p); p += 4; }
      if (tf & 0x4) p += 4;
      if (offsetAt < 0) throw new Error('fragment run without a data offset');
      const run = { offsetAt, offset, size: 0 };
      for (let i = 0; i < count; i++) {
        let duration = def.duration, size = def.size;
        if (tf & 0x100) { duration = d.getUint32(p); p += 4; }
        if (tf & 0x200) { size = d.getUint32(p); p += 4; }
        if (tf & 0x400) p += 4;
        if (tf & 0x800) p += 4;
        const pos = offset + run.size - moof.byteLength;
        if (pos < 0 || pos + size > mdat.byteLength) throw new Error('fragment sample outside its mdat');
        t.samples.push({ time, duration, data: mdat.subarray(pos, pos + size) });
        run.size += size;
        time += duration;
      }
      t.runs.push(run);
    }
    trafs.push(t);
  }
  return trafs;
}

// ---------- writing ----------

function concat(parts) {
  let n = 0;
  for (const p of parts) n += p.byteLength;
  const out = new Uint8Array(n);
  let at = 0;
  for (const p of parts) { out.set(p, at); at += p.byteLength; }
  return out;
}

function u32(...vals) {
  const b = new Uint8Array(vals.length * 4), d = view(b);
  vals.forEach((v, i) => d.setUint32(i * 4, v >>> 0));
  return b;
}

const ascii = (s) => Uint8Array.from(s, (c) => c.charCodeAt(0));

export function box(type, ...parts) {
  const body = concat(parts);
  return concat([u32(body.byteLength + 8), ascii(type), body]);
}

const fullbox = (type, version, flags, ...parts) => box(type, u32(((version << 24) | flags) >>> 0), ...parts);

// An MPEG-4 descriptor with a one-byte length (all of ours are short).
const descriptor = (tag, ...parts) => { const b = concat(parts); return concat([Uint8Array.of(tag, b.byteLength), b]); };

// audioTrak describes an AAC-LC track; asc is its AudioSpecificConfig.
export function audioTrak({ id, rate, channels, asc, bitrate }) {
  const matrix = u32(0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000);
  const tkhd = fullbox('tkhd', 0, 3, u32(0, 0, id, 0, 0, 0, 0), Uint8Array.of(0, 0, 0, 1, 1, 0, 0, 0), matrix, u32(0, 0));
  const mdhd = fullbox('mdhd', 0, 0, u32(0, 0, rate, 0), Uint8Array.of(0x55, 0xc4, 0, 0));
  const hdlr = fullbox('hdlr', 0, 0, u32(0), ascii('soun'), u32(0, 0, 0), ascii('SoundHandler\0'));
  const es = descriptor(3, Uint8Array.of(0, 0, 0),
    descriptor(4, Uint8Array.of(0x40, 0x15, 0, 0, 0), u32(bitrate, bitrate), descriptor(5, asc)),
    descriptor(6, Uint8Array.of(2)));
  const mp4a = box('mp4a', new Uint8Array(6), Uint8Array.of(0, 1), new Uint8Array(8),
    Uint8Array.of(channels >> 8, channels & 255, 0, 16, 0, 0, 0, 0, rate >> 8, rate & 255, 0, 0), fullbox('esds', 0, 0, es));
  const stbl = box('stbl', fullbox('stsd', 0, 0, u32(1), mp4a), fullbox('stts', 0, 0, u32(0)), fullbox('stsc', 0, 0, u32(0)),
    fullbox('stsz', 0, 0, u32(0, 0)), fullbox('stco', 0, 0, u32(0)));
  const dinf = box('dinf', fullbox('dref', 0, 0, u32(1), fullbox('url ', 0, 1)));
  const minf = box('minf', fullbox('smhd', 0, 0, u32(0)), dinf, stbl);
  return box('trak', tkhd, box('mdia', mdhd, hdlr, minf));
}

// Every audio sample is a sync sample.
export const audioTrex = (id) => fullbox('trex', 0, 0, u32(id, 1, 0, 0, 0x02000000));

// initSegment rebuilds the moov with the audio track (by id) replaced.
export function initSegment(ftyp, init, audioId, trak, trex) {
  const traks = init.tracks.map((t) => (t.id === audioId ? trak : t.bytes));
  const trexes = Object.entries(init.trex).map(([id, t]) => (+id === audioId ? trex : t.bytes));
  return concat([ftyp, box('moov', init.mvhd, ...traks, box('mvex', ...init.mvexOther, ...trexes))]);
}

// videoFragment copies trafs (as parsed by parseFragment) from moof into a
// new fragment with only their samples, fixing each run's data offset.
export function videoFragment(seq, moof, trafs) {
  const copies = trafs.map((t) => moof.slice(t.start, t.end));
  const moofLen = 8 + 16 + copies.reduce((n, c) => n + c.byteLength, 0);
  const data = [];
  let offset = moofLen + 8;
  trafs.forEach((t, i) => {
    const d = view(copies[i]);
    let s = 0;
    for (const run of t.runs) {
      d.setInt32(run.offsetAt - t.start, offset);
      const bytes = [];
      let size = 0;
      while (size < run.size) { const smp = t.samples[s++]; bytes.push(smp.data); size += smp.data.byteLength; }
      data.push(...bytes);
      offset += run.size;
    }
  });
  return concat([box('moof', fullbox('mfhd', 0, 0, u32(seq)), ...copies), box('mdat', ...data)]);
}

// audioFragment writes contiguous AAC frames ({ pts, duration, data }) as
// one fragment of track id.
export function audioFragment(seq, id, frames) {
  const trun = (offset) => fullbox('trun', 0, 0x301, u32(frames.length, offset), ...frames.map((f) => u32(f.duration, f.data.byteLength)));
  const traf = (offset) => box('traf', fullbox('tfhd', 0, 0x20000, u32(id)),
    fullbox('tfdt', 1, 0, u32(Math.floor(frames[0].pts / 2 ** 32), frames[0].pts >>> 0)), trun(offset));
  const moofLen = 8 + 16 + traf(0).byteLength;
  return concat([box('moof', fullbox('mfhd', 0, 0, u32(seq)), traf(moofLen + 8)), box('mdat', ...frames.map((f) => f.data))]);
}
