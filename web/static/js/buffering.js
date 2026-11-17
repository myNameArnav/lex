// Why is playback buffering? bufferingCause names the likely cause from
// what the player measures, in words for the viewer, with what would help.
// It returns null when nothing explains it (the player then just says it's
// still buffering).

const mbps = (bps) => `${(bps / 1e6).toFixed(bps < 1e7 ? 1 : 0)} Mbps`;

// needKbps: the stream's bitrate (plan.bitrate). bandwidth: how fast data
// arrives, in bits/s (0 if unknown). job: the server's ffmpeg progress
// ({ speed, exited, throttled }), if it converts. canTranscode: the server
// may lower the quality. method: the plan's method; rate: playback speed.
export function bufferingCause({ needKbps = 0, bandwidth = 0, remote = false, job = null, rate = 1, canTranscode = true, isAdmin = false, method = '' } = {}) {
  if (job && !job.exited && !job.throttled && job.speed > 0 && job.speed < rate * 0.95) {
    return {
      kind: 'server',
      text: `The server is converting this video slower than it plays (${job.speed.toFixed(1)}x).`,
      hint: method === 'transcode' ? 'A lower quality is quicker to convert.' : '',
    };
  }
  const need = needKbps * 1000 * rate;
  if (bandwidth > 0 && need > 0 && bandwidth < need * 1.1) {
    const where = remote
      ? 'Your connection to the server is too slow for this quality.'
      : "The server's network or disk can't keep up with this quality.";
    let hint;
    if (canTranscode) hint = 'Choose a lower quality in Settings → Quality.';
    else if (isAdmin) hint = 'A lower quality needs transcoding, which is turned off in Settings → Transcoding.';
    else hint = "A lower quality isn't available: this server doesn't transcode.";
    return { kind: 'network', text: `Video is arriving at ${mbps(bandwidth)} but needs about ${mbps(need)}. ${where}`, hint };
  }
  return null;
}
