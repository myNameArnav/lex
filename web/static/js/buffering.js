// Why is playback buffering? bufferingCause names the likely cause from
// what the player measures and what the server reports about itself, in
// words for the viewer, with what would help. It returns null when nothing
// explains it (the player then just says it's still buffering).

const mbps = (bps) => `${(bps / 1e6).toFixed(bps < 1e7 ? 1 : 0)} Mbps`;

// What an admin can do about each kind of server overload (sysstats Strain).
const ADMIN_HINTS = {
  disk: 'Downloads or cache copies on the same drive slow it down.',
  cpu: 'Transcodes or scans are using the CPU.',
  memory: 'Other programs are using the memory.',
  throttled: "Check the Pi's power supply and cooling.",
};
const VIEWER_HINT = 'Should recover once the server catches up.';

// needKbps: the stream's bitrate (plan.bitrate). bandwidth: how fast data
// arrives, in bits/s (0 if unknown). job: the server's ffmpeg progress
// ({ speed, exited, throttled }), if it converts. canTranscode: the server
// may lower the quality. method: the plan's method; rate: playback speed.
// diskWait: the share of its time the server spent reading the file from
// disk rather than sending it (0-1, heartbeats; only for file reads).
// host: what is overloading the server ([{ kind, text }], worst first).
// Whether data arrives comfortably faster than the stream plays.
function keepsUp(needKbps, bandwidth, rate) {
  const need = needKbps * 1000 * rate;
  return bandwidth > 0 && need > 0 && bandwidth >= need * 1.1;
}

// How worrying a stream's disk wait is, for the stats panel: '' | 'warn' |
// 'bad'. diskWait is a share of serving time (disk vs. network), so a fast
// connection pushes it up even when the disk has plenty to spare. It only
// counts while playback is short of data (struggling: buffering or a low
// buffer) and data doesn't already arrive faster than the video plays.
export function diskLevel({ diskWait = 0, needKbps = 0, bandwidth = 0, rate = 1, struggling = false } = {}) {
  if (!struggling || keepsUp(needKbps, bandwidth, rate)) return '';
  return diskWait >= 0.6 ? 'bad' : diskWait >= 0.3 ? 'warn' : '';
}

export function bufferingCause({ needKbps = 0, bandwidth = 0, remote = false, job = null, rate = 1, canTranscode = true, isAdmin = false, method = '', diskWait = 0, host = [] } = {}) {
  const strain = (host || [])[0] || null;
  const serverHint = (kind) => (isAdmin ? ADMIN_HINTS[kind] || '' : VIEWER_HINT);
  const need = needKbps * 1000 * rate;
  const fastEnough = keepsUp(needKbps, bandwidth, rate);

  // This stream is mostly waiting on the server's drive.
  if (diskWait >= 0.6 && !fastEnough) {
    const disk = (host || []).find((s) => s.kind === 'disk');
    return {
      kind: 'disk',
      text: `${disk ? disk.text.replace(/\.$/, ';') : 'Server disk too slow:'} ${Math.round(diskWait * 100)}% of the stream's time goes to reading the file, not your connection.`,
      hint: serverHint('disk'),
    };
  }
  if (job && !job.exited && !job.throttled && job.speed > 0 && job.speed < rate * 0.95) {
    return {
      kind: 'server',
      text: `The server is converting this video slower than it plays (${job.speed.toFixed(1)}x).${strain ? ` ${strain.text}` : ''}`,
      hint: strain ? serverHint(strain.kind) : method === 'transcode' ? 'A lower quality is quicker to convert.' : '',
    };
  }
  if (bandwidth > 0 && need > 0 && !fastEnough) {
    const arriving = `Video is arriving at ${mbps(bandwidth)} but needs about ${mbps(need)}.`;
    // The server is overloaded: that, not the connection, is the likely cause.
    if (strain) return { kind: strain.kind, text: `${arriving} ${strain.text}`, hint: serverHint(strain.kind) };
    const where = remote
      ? 'Your connection to the server is too slow for this quality.'
      : "The server's network or disk can't keep up with this quality.";
    let hint;
    if (canTranscode) hint = 'Choose a lower quality in Settings → Playback.';
    else if (isAdmin) hint = 'A lower quality needs transcoding, which is turned off in Settings → Transcoding.';
    else hint = "A lower quality isn't available: this server doesn't transcode.";
    return { kind: 'network', text: `${arriving} ${where}`, hint };
  }
  // Nothing measured, but the server says it's overloaded.
  if (strain && !fastEnough) return { kind: strain.kind, text: strain.text, hint: serverHint(strain.kind) };
  return null;
}
