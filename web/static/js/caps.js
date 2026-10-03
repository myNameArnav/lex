// Detects what this browser can decode, both natively (<video src>) and via
// Media Source Extensions (fragmented MP4 from the server).

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
  mp4: 'MP4', webm: 'WebM', mkv: 'MKV',
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
  };
}
