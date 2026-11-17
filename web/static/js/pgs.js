// PGS (Blu-ray) subtitles drawn over the video, so the server needn't burn
// them into a transcode. The server copies the track out as a .sup file
// (see internal/stream/pgs.go); parseSup reads it into display sets and
// PgsRenderer draws the one for the video's current time on a canvas.
//
// A .sup file is a run of segments: "PG", PTS and DTS (90 kHz), a type and
// a length. A display set is a presentation composition (PCS: which objects
// go where), window, palette (PDS: YCbCr + alpha) and object (ODS: RLE
// bitmap) segments, ended by an END segment. Palettes and objects carry over
// between display sets until the next epoch start. Each display set stays
// on screen until the next one (one with no objects clears the screen).

const PDS = 0x14, ODS = 0x15, PCS = 0x16, END = 0x80;

// parseSup returns { width, height, sets } where each set is
// { start, end, objects: [{ x, y, crop, object, palette }] }, times in
// seconds. Objects are decoded only when drawn (decodeObject).
export function parseSup(buf) {
  const b = new Uint8Array(buf);
  const dv = new DataView(b.buffer, b.byteOffset, b.byteLength);
  const sets = [];
  let width = 0, height = 0;
  let palettes = new Map(), objects = new Map();
  let pcs = null;
  for (let off = 0; off + 13 <= b.length;) {
    if (b[off] !== 0x50 || b[off + 1] !== 0x47) break; // "PG"
    const pts = dv.getUint32(off + 2);
    const type = b[off + 10];
    const size = dv.getUint16(off + 11);
    const p = off + 13;
    if (p + size > b.length) break;
    off = p + size;
    if (type === PCS && size >= 11) {
      const state = b[p + 7];
      if (state & 0x80) { palettes = new Map(); objects = new Map(); } // epoch start
      width = dv.getUint16(p) || width;
      height = dv.getUint16(p + 2) || height;
      const comps = [];
      let q = p + 11;
      for (let i = 0, n = b[p + 10]; i < n && q + 8 <= p + size; i++) {
        const c = { id: dv.getUint16(q), x: dv.getUint16(q + 4), y: dv.getUint16(q + 6), crop: null };
        if (b[q + 3] & 0x40 && q + 16 <= p + size) {
          c.crop = { x: dv.getUint16(q + 8), y: dv.getUint16(q + 10), w: dv.getUint16(q + 12), h: dv.getUint16(q + 14) };
          q += 16;
        } else q += 8;
        comps.push(c);
      }
      pcs = { start: pts / 90000, paletteId: b[p + 9], comps };
    } else if (type === PDS && size >= 2) {
      // Palettes are replaced, not edited, so earlier sets keep theirs.
      const old = palettes.get(b[p]);
      const entries = old ? old.entries.slice() : new Array(256).fill(null);
      for (let q = p + 2; q + 5 <= p + size; q += 5) entries[b[q]] = [b[q + 1], b[q + 2], b[q + 3], b[q + 4]]; // Y, Cr, Cb, A
      palettes.set(b[p], { entries });
    } else if (type === ODS && size >= 4) {
      const id = dv.getUint16(p);
      const seq = b[p + 3];
      if (seq & 0x80) {
        if (size < 11) continue;
        objects.set(id, { w: dv.getUint16(p + 7), h: dv.getUint16(p + 9), chunks: [b.subarray(p + 11, p + size)] });
      } else {
        objects.get(id)?.chunks.push(b.subarray(p + 4, p + size));
      }
    } else if (type === END && pcs) {
      const palette = palettes.get(pcs.paletteId);
      const objs = [];
      for (const c of pcs.comps) {
        const object = objects.get(c.id);
        if (object && palette) objs.push({ x: c.x, y: c.y, crop: c.crop, object, palette });
      }
      sets.push({ start: pcs.start, end: Infinity, objects: objs });
      pcs = null;
    }
  }
  sets.sort((a, b) => a.start - b.start);
  for (let i = 0; i + 1 < sets.length; i++) sets[i].end = sets[i + 1].start;
  return { width, height, sets };
}

// paletteRGBA turns a palette's YCbCr entries into RGBA words (little
// endian, for a Uint32Array over ImageData). HD subtitles use BT.709.
export function paletteRGBA(palette, hd = true) {
  const out = new Uint32Array(256);
  const [kr, kb] = hd ? [0.2126, 0.0722] : [0.299, 0.114];
  const kg = 1 - kr - kb;
  const clamp = (v) => (v < 0 ? 0 : v > 255 ? 255 : Math.round(v));
  palette.entries.forEach((e, i) => {
    if (!e) return;
    const [y, cr, cb, a] = e;
    // Limited range (16-235 luma, 16-240 chroma) to full.
    const Y = (y - 16) * (255 / 219), Cb = (cb - 128) * (255 / 224), Cr = (cr - 128) * (255 / 224);
    const r = clamp(Y + 2 * (1 - kr) * Cr);
    const bl = clamp(Y + 2 * (1 - kb) * Cb);
    const g = clamp((Y - kr * r - kb * bl) / kg);
    out[i] = ((a << 24) | (bl << 16) | (g << 8) | r) >>> 0;
  });
  return out;
}

// decodeObject expands an object's run-length data into palette indexes.
export function decodeObject(object) {
  const { w, h } = object;
  const px = new Uint8Array(w * h);
  let len = 0;
  for (const c of object.chunks) len += c.length;
  const d = new Uint8Array(len);
  len = 0;
  for (const c of object.chunks) { d.set(c, len); len += c.length; }
  let x = 0, y = 0;
  for (let i = 0; i < d.length && y < h;) {
    const v = d[i++];
    let color = v, run = 1;
    if (v === 0) {
      const f = d[i++];
      if (f === 0) { x = 0; y++; continue; } // end of line
      run = f & 0x3f;
      if (f & 0x40) run = (run << 8) | d[i++];
      color = f & 0x80 ? d[i++] : 0;
    }
    const n = Math.min(run, w - x);
    if (n > 0) px.fill(color, y * w + x, y * w + x + n);
    x += run;
  }
  return px;
}

// setAt returns the display set showing at time t (seconds), or null.
export function setAt(sets, t) {
  let lo = 0, hi = sets.length - 1, found = -1;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    if (sets[mid].start <= t) { found = mid; lo = mid + 1; } else hi = mid - 1;
  }
  const s = found >= 0 ? sets[found] : null;
  return s && t < s.end && s.objects.length ? s : null;
}

// PgsRenderer draws a parsed track on a canvas laid over video.
export class PgsRenderer {
  constructor(video, canvas, { offset = 0 } = {}) {
    this.video = video;
    this.canvas = canvas;
    this.ctx = canvas.getContext('2d');
    this.offset = offset; // seconds; positive shows subtitles later
    this.track = { width: 0, height: 0, sets: [] };
    this.shown = undefined;
    this.bitmaps = new WeakMap(); // object -> palette -> canvas
    this.frame = 0;
    this.tick = () => { this.frame = 0; this.render(); this.schedule(); };
    this.onEvent = () => this.render(true);
    for (const e of ['seeked', 'timeupdate', 'play', 'loadedmetadata']) video.addEventListener(e, this.onEvent);
    this.resize = new ResizeObserver(() => this.render(true));
    this.resize.observe(video);
    this.schedule();
  }

  setTrack(buf) {
    this.track = parseSup(buf);
    this.render(true);
  }

  setOffset(s) { this.offset = s; this.render(true); }

  schedule() {
    if (this.frame || this.destroyed) return;
    this.frame = requestAnimationFrame(this.tick);
  }

  // The video picture's box inside the element (object-fit: contain).
  contentRect() {
    const v = this.video, cw = v.clientWidth, ch = v.clientHeight;
    const vw = v.videoWidth || this.track.width || cw, vh = v.videoHeight || this.track.height || ch;
    const scale = Math.min(cw / vw, ch / vh) || 1;
    const w = vw * scale, h = vh * scale;
    return { x: (cw - w) / 2, y: (ch - h) / 2, w, h };
  }

  render(force = false) {
    if (this.destroyed) return;
    const set = setAt(this.track.sets, this.video.currentTime - this.offset);
    const dpr = globalThis.devicePixelRatio || 1;
    const cw = Math.round(this.video.clientWidth * dpr), ch = Math.round(this.video.clientHeight * dpr);
    const sized = this.canvas.width === cw && this.canvas.height === ch;
    if (set === this.shown && sized && !force) return;
    if (!sized) { this.canvas.width = cw; this.canvas.height = ch; }
    this.shown = set;
    const g = this.ctx;
    g.setTransform(1, 0, 0, 1, 0, 0);
    g.clearRect(0, 0, cw, ch);
    if (!set) return;
    const r = this.contentRect();
    const sx = (r.w / (this.track.width || r.w)) * dpr, sy = (r.h / (this.track.height || r.h)) * dpr;
    g.imageSmoothingEnabled = true;
    g.imageSmoothingQuality = 'high';
    for (const o of set.objects) {
      const bmp = this.bitmap(o);
      if (!bmp) continue;
      const c = o.crop || { x: 0, y: 0, w: o.object.w, h: o.object.h };
      g.drawImage(bmp, c.x, c.y, c.w, c.h, r.x * dpr + o.x * sx, r.y * dpr + o.y * sy, c.w * sx, c.h * sy);
    }
  }

  // bitmap draws an object in its palette once and keeps it.
  bitmap(o) {
    let byPalette = this.bitmaps.get(o.object);
    if (!byPalette) this.bitmaps.set(o.object, (byPalette = new WeakMap()));
    let c = byPalette.get(o.palette);
    if (c) return c;
    const { w, h } = o.object;
    if (!w || !h) return null;
    const idx = decodeObject(o.object);
    const rgba = paletteRGBA(o.palette, (this.track.height || 1080) >= 720);
    const img = new ImageData(w, h);
    const px = new Uint32Array(img.data.buffer);
    for (let i = 0; i < idx.length; i++) px[i] = rgba[idx[i]];
    c = document.createElement('canvas');
    c.width = w;
    c.height = h;
    c.getContext('2d').putImageData(img, 0, 0);
    byPalette.set(o.palette, c);
    return c;
  }

  destroy() {
    this.destroyed = true;
    if (this.frame) cancelAnimationFrame(this.frame);
    for (const e of ['seeked', 'timeupdate', 'play', 'loadedmetadata']) this.video.removeEventListener(e, this.onEvent);
    this.resize.disconnect();
    this.ctx.clearRect(0, 0, this.canvas.width, this.canvas.height);
  }
}
