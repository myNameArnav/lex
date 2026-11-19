// Versions of one title: several files that are the same movie or episode
// (cuts, resolutions) or the parts of a film split across files. Labels tell
// them apart, and the version last chosen for a title is remembered on this
// device. Kept free of the DOM so it can be tested on its own.

import { resLabel, fmtBytes } from './ui.js';

/** @typedef {import('../../types/api.gen').fileView} fileView */

const CODECS = { h264: 'H.264', hevc: 'HEVC', av1: 'AV1', vp9: 'VP9', mpeg4: 'MPEG-4', mpeg2video: 'MPEG-2', vc1: 'VC-1' };
const codecName = (c) => CODECS[c] || (c || '').toUpperCase();

// The video part of a label: "1080p H.264".
export const videoLabel = (f) => [resLabel(f.width, f.height), codecName(f.vcodec)].filter(Boolean).join(' ');

// versionLabel names file f among files: "Final Cut · 1080p H.264 · 12.3 GB"
// (or "Part 2 · …"). When that still matches another file's label, the file
// name tells them apart. size: false leaves the size out (the player shows it
// beside the label), though it still counts when comparing.
/** @param {fileView} f @param {fileView[]} files */
export function versionLabel(f, files, { size = true } = {}) {
  const full = (x) => [x.part ? `Part ${x.part}` : x.edition, videoLabel(x), fmtBytes(x.size)].filter(Boolean).join(' · ');
  const mine = full(f);
  if (files.some((x) => x !== f && x.id !== f.id && full(x) === mine)) return f.name || mine;
  return size ? mine : [f.part ? `Part ${f.part}` : f.edition, videoLabel(f)].filter(Boolean).join(' · ');
}

// The files of a split film come in part order; versions keep the server's
// order (highest resolution first).
/** @param {fileView[]} files */
export function orderFiles(files) {
  if (files.length > 1 && files.every((f) => f.part > 0)) return [...files].sort((a, b) => a.part - b.part);
  return files;
}

const KEY = 'lex.versions';
const MAX = 300;

function load() {
  try { return JSON.parse(localStorage.getItem(KEY) || '{}') || {}; } catch { return {}; }
}

// chosenFile is the file to play for an item: the version last chosen on this
// device if it still exists, else the first.
/** @param {number} itemId @param {fileView[]} files */
export function chosenFile(itemId, files) {
  const id = load()[itemId];
  return files.find((f) => f.id === id) || files[0];
}

// rememberFile records the version chosen for an item (the first is the
// default, so choosing it just forgets the choice).
export function rememberFile(itemId, fileId, files = []) {
  const m = load();
  delete m[itemId];
  if (files[0]?.id !== fileId) m[itemId] = fileId;
  const keys = Object.keys(m);
  for (const k of keys.slice(0, Math.max(0, keys.length - MAX))) delete m[k];
  try { localStorage.setItem(KEY, JSON.stringify(m)); } catch {}
}
