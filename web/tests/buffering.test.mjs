import assert from 'node:assert/strict';
import test from 'node:test';
import { bufferingCause } from '../static/js/buffering.js';

test('a connection slower than the video is named, with both rates', () => {
  // The case that prompted it: an 8.2 Mbps film at 5.3 Mbps from outside home.
  const c = bufferingCause({ needKbps: 8227, bandwidth: 5_310_407, remote: true, canTranscode: false, isAdmin: true, method: 'remux' });
  assert.equal(c.kind, 'network');
  assert.match(c.text, /arriving at 5\.3 Mbps but needs about 8\.2 Mbps/);
  assert.match(c.text, /Your connection/);
  // Transcoding is off: say so rather than suggest a quality that fails.
  assert.match(c.hint, /turned off in Settings → Transcoding/);
  assert.match(bufferingCause({ needKbps: 8227, bandwidth: 5e6, canTranscode: false }).hint, /doesn't transcode/);
  assert.match(bufferingCause({ needKbps: 8227, bandwidth: 5e6, canTranscode: true }).hint, /lower quality in Settings → Quality/);
  // On the local network it's the server's side.
  assert.match(bufferingCause({ needKbps: 8227, bandwidth: 5e6 }).text, /server's network or disk/);
});

test('fast enough, or unknown, explains nothing', () => {
  assert.equal(bufferingCause({ needKbps: 8000, bandwidth: 40e6 }), null);
  assert.equal(bufferingCause({ needKbps: 8000, bandwidth: 0 }), null);
  assert.equal(bufferingCause({ needKbps: 0, bandwidth: 1e6 }), null);
  assert.equal(bufferingCause(), null);
});

test('a transcode below real time is the server, ahead of the network', () => {
  const c = bufferingCause({ needKbps: 4000, bandwidth: 2e6, job: { speed: 0.7 }, method: 'transcode' });
  assert.equal(c.kind, 'server');
  assert.match(c.text, /0\.7x/);
  assert.match(c.hint, /lower quality/);
  // Finished, or waiting on a full client buffer, isn't slow.
  assert.equal(bufferingCause({ needKbps: 4000, bandwidth: 40e6, job: { speed: 0.7, exited: true } }), null);
  assert.equal(bufferingCause({ needKbps: 4000, bandwidth: 40e6, job: { speed: 0.2, throttled: true } }), null);
  // Watching at 2x needs twice the rate.
  assert.equal(bufferingCause({ needKbps: 4000, bandwidth: 6e6, rate: 2 }).kind, 'network');
  assert.equal(bufferingCause({ needKbps: 4000, bandwidth: 6e6, rate: 1 }), null);
});
