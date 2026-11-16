// Detects what this browser can decode, both natively (<video src>) and via
// Media Source Extensions (fragmented MP4 from the server).

import { CLIENT_AUDIO, clientAudioSupported } from './client-audio.js';
import { CLIENT_REMUX, clientRemuxSupported } from './client-remux.js';
import { prefs } from './prefs.js';

let cached = null;

export function mediaSourceClass() {
  return window.ManagedMediaSource || window.MediaSource || window.WebKitMediaSource || null;
}

export function detectCaps() {
  if (cached) return cached;
  const v = document.createElement('video');
  const MS = mediaSourceClass();
  const mse = !!(MS && typeof MS.isTypeSupported === 'function');
  const mseOK = (type) => { try { return mse && MS.isTypeSupported(type); } catch { return false; } };
  const nativeOK = (type) => { try { return v.canPlayType(type) !== ''; } catch { return false; } };

  const videoCodecs = {
    h264: 'avc1.640033',
    h264_10: 'avc1.6E0033',
    hevc: 'hvc1.1.6.L153.B0',
    hevc10: 'hvc1.2.4.L153.B0',
    av1: 'av01.0.12M.08',
    av1_10: 'av01.0.12M.10',
    vp9: 'vp09.00.51.08',
    vp9_10: 'vp09.02.51.10',
    dv5: 'dvh1.05.06',
  };
  const audioCodecs = {
    aac: 'mp4a.40.2', mp3: 'mp4a.40.34', ac3: 'ac-3', eac3: 'ec-3', opus: 'opus', flac: 'fLaC', alac: 'alac',
  };
  const caps = { mse, direct: {}, video: {}, nativeVideo: {}, audio: {}, nativeAudio: {} };
  for (const [k, c] of Object.entries(videoCodecs)) {
    caps.video[k] = mseOK(`video/mp4; codecs="${c}"`);
    caps.nativeVideo[k] = nativeOK(`video/mp4; codecs="${c}"`);
  }
  // HEVC often reports only via one of the two tags.
  if (!caps.nativeVideo.hevc) caps.nativeVideo.hevc = nativeOK('video/mp4; codecs="hev1.1.6.L153.B0"');
  caps.video.vp8 = false;
  caps.nativeVideo.vp8 = nativeOK('video/webm; codecs="vp8"');
  caps.nativeVideo.vp9 = caps.nativeVideo.vp9 || nativeOK('video/webm; codecs="vp9"');
  caps.nativeVideo.av1 = caps.nativeVideo.av1 || nativeOK('video/webm; codecs="av01.0.12M.08"');
  for (const [k, c] of Object.entries(audioCodecs)) {
    caps.audio[k] = mseOK(`audio/mp4; codecs="${c}"`);
    caps.nativeAudio[k] = nativeOK(`audio/mp4; codecs="${c}"`);
  }
  caps.nativeAudio.mp3 = caps.nativeAudio.mp3 || nativeOK('audio/mpeg');
  caps.nativeAudio.vorbis = nativeOK('audio/webm; codecs="vorbis"');
  caps.nativeAudio.opus = caps.nativeAudio.opus || nativeOK('audio/webm; codecs="opus"');
  caps.nativeAudio.flac = caps.nativeAudio.flac || nativeOK('audio/flac');
  // Audio the player converts to AAC itself (client-audio.js).
  caps.clientAudio = mse && caps.audio.aac && clientAudioSupported() ? CLIENT_AUDIO : [];
  // Containers the player remuxes to fragmented MP4 itself (client-remux.js).
  caps.clientRemux = mse && clientRemuxSupported() ? CLIENT_REMUX : [];
  caps.direct.mp4 = nativeOK('video/mp4');
  caps.direct.webm = nativeOK('video/webm');
  caps.direct.mkv = nativeOK('video/x-matroska') || nativeOK('video/mkv');
  cached = caps;
  return caps;
}

// Readable names for the codec and container keys above.
const LABELS = {
  h264: 'H.264', h264_10: 'H.264 10-bit', hevc: 'HEVC', hevc10: 'HEVC 10-bit', av1: 'AV1', av1_10: 'AV1 10-bit',
  vp8: 'VP8', vp9: 'VP9', vp9_10: 'VP9 10-bit', dv5: 'Dolby Vision 5',
  aac: 'AAC', mp3: 'MP3', ac3: 'Dolby Digital (AC-3)', eac3: 'Dolby Digital Plus (E-AC-3)', opus: 'Opus', flac: 'FLAC', alac: 'ALAC', vorbis: 'Vorbis',
  dts: 'DTS', truehd: 'Dolby TrueHD',
  mp4: 'MP4', webm: 'WebM', mkv: 'MKV',
  // Only in library files, for the compatibility report.
  mpeg4: 'MPEG-4 (DivX/Xvid)', mpeg2video: 'MPEG-2', mpeg1video: 'MPEG-1', vc1: 'VC-1', wmv3: 'WMV', msmpeg4v3: 'DivX 3',
  mp2: 'MP2', wmav2: 'WMA', dts_hd: 'DTS-HD',
  avi: 'AVI', ts: 'MPEG-TS', m2ts: 'Blu-ray (M2TS)', mpg: 'MPEG', wmv: 'WMV', flv: 'FLV',
};

export function capsSummary(c = detectCaps()) {
  const on = (m) => Object.entries(m).filter(([, v]) => v).map(([k]) => LABELS[k] || k).join(', ') || 'none';
  return {
    'Media Source': c.mse ? (window.ManagedMediaSource ? 'ManagedMediaSource' : 'MediaSource') : 'not available',
    'Direct Play containers': on(c.direct),
    'Direct Play video': on(c.nativeVideo),
    'Direct Play audio': on(c.nativeAudio),
    'Streamed video (MSE)': on(c.video),
    'Streamed audio (MSE)': on(c.audio),
    'Audio converted in the browser': c.clientAudio.map((k) => LABELS[k] || k).join(', ') || 'none',
    'Remuxed in the browser': c.clientRemux.map((k) => LABELS[k] || k).join(', ') || 'none',
  };
}

// compatRequest is what /api/compat and /api/items/{id}/compat need: this
// browser's codecs and this device's playback settings, as the player sends
// them (player.js loadPlan).
export function compatRequest() {
  let mode = prefs.get('mode');
  const forceHls = mode === 'hls' && document.createElement('video').canPlayType('application/vnd.apple.mpegurl') !== '';
  if (mode === 'hls') mode = 'auto';
  return { caps: detectCaps(), mode, forceHls, maxBitrate: prefs.get('quality'), audioLang: prefs.get('audioLang') };
}

// How well a file plays here, from least to most work (stream/compat.go).
export const TIERS = {
  direct: { label: 'Plays as-is', text: 'The browser reads the file unchanged.' },
  browser: { label: 'Browser repackages', text: 'This browser repackages the file itself. No work for the server.' },
  remux: { label: 'Server repackages', text: 'The video is copied unchanged. Light work for the server.' },
  transcode: { label: 'Server converts', text: 'The video is re-encoded: heavy work for the server, and lower quality.' },
  unplayable: { label: 'Won’t play', text: 'No way to play it with the current settings.' },
  unknown: { label: 'Not analysed yet', text: 'The file hasn’t been probed yet.' },
};
export const TIER_ORDER = ['direct', 'browser', 'remux', 'transcode', 'unplayable', 'unknown'];

// causeLabel says one part of a verdict in plain words. Stage "direct" is why
// the file can't play as it is; "stream" is why the server does more than
// repackage it.
export function causeLabel(c, { remote = false } = {}) {
  const name = LABELS[c.key] || (c.key?.startsWith('pcm_') ? 'PCM' : (c.key || '').toUpperCase());
  const direct = c.stage === 'direct';
  switch (c.kind) {
    case 'container': return `Browser can’t open ${name} files directly`;
    case 'video': return direct ? `Browser can’t play ${name} video directly` : `Browser can’t decode ${name} video`;
    case 'audio': return direct ? `Browser can’t play ${name} audio directly` : `Server converts ${name} audio`;
    case 'track': return 'Preferred audio language isn’t the default track';
    case 'bitrate': return prefs.get('quality') > 0 ? 'Above this device’s quality setting' : remote ? 'Above the server’s limit for remote streaming' : 'Above the quality limit';
    case 'hls': return `HLS can only repackage MKV files, not ${name}`;
    case 'mode': return `“Always ${c.key === 'transcode' ? 'Transcode' : 'Direct Stream'}” is set on this device`;
    case 'setting': return { directplay: 'Direct Play is turned off on the server', remux: 'Remuxing is turned off on the server', transcode: 'Converting video is turned off on the server' }[c.key] || `${c.key} is turned off on the server`;
    case 'error': return c.key ? c.key[0].toUpperCase() + c.key.slice(1) : 'No playback method works';
  }
  return `${c.kind} ${c.key}`;
}

// verdictCauses picks the causes worth saying for a tier: when the server
// converts, why it can't just repackage; otherwise why it isn't played as is
// (plus audio the server converts).
export function verdictCauses(tier, causes) {
  if (tier === 'transcode' || tier === 'unplayable') {
    const s = causes.filter((c) => c.stage === 'stream');
    return s.length ? s : causes;
  }
  return causes;
}
