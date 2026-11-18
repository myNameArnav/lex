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

test('a server waiting on its own disk is named instead of the connection', () => {
  // The case that prompted it: torrents writing to the same USB drive while
  // a remote viewer watched; the file reads stalled, not the network.
  const c = bufferingCause({ needKbps: 15000, bandwidth: 14e6, remote: true, isAdmin: true, method: 'remux', diskWait: 0.9 });
  assert.equal(c.kind, 'disk');
  assert.match(c.text, /server's disk is too slow/);
  assert.doesNotMatch(c.text, /Your connection to the server is too slow/);
  assert.match(c.hint, /downloads/);
  assert.doesNotMatch(bufferingCause({ needKbps: 15000, bandwidth: 0, diskWait: 0.9 }).hint, /downloads/);
  // Mostly waiting on the network: still the connection.
  assert.equal(bufferingCause({ needKbps: 15000, bandwidth: 14e6, remote: true, diskWait: 0.2 }).kind, 'network');
  // Data arrives fast enough: the disk isn't why it's buffering.
  assert.equal(bufferingCause({ needKbps: 8000, bandwidth: 40e6, diskWait: 0.9 }), null);
});

test('an overloaded server is named instead of the connection', () => {
  const cpu = [{ kind: 'cpu', text: "The server's processor is 99% busy." }];
  const c = bufferingCause({ needKbps: 15000, bandwidth: 5e6, remote: true, isAdmin: true, host: cpu });
  assert.equal(c.kind, 'cpu');
  assert.match(c.text, /arriving at 5\.0 Mbps.*processor is 99% busy/);
  assert.doesNotMatch(c.text, /Your connection/);
  assert.match(c.hint, /transcodes/);
  assert.equal(bufferingCause({ needKbps: 15000, bandwidth: 5e6, host: cpu }).hint, 'Playback picks up when the server catches up.');
  // A slow conversion says why the server is slow.
  const j = bufferingCause({ needKbps: 4000, job: { speed: 0.6 }, host: [{ kind: 'memory', text: 'The server is short of memory (40 MB free).' }], isAdmin: true });
  assert.equal(j.kind, 'server');
  assert.match(j.text, /0\.6x\)\. The server is short of memory/);
  assert.match(j.hint, /memory/);
  // Nothing measured yet, but the host is struggling.
  assert.equal(bufferingCause({ host: [{ kind: 'throttled', text: 'x' }] }).kind, 'throttled');
  // Data arrives fast enough: the load isn't why.
  assert.equal(bufferingCause({ needKbps: 8000, bandwidth: 40e6, host: cpu }), null);
  // The disk strain's own words are used for a stream waiting on disk.
  assert.match(bufferingCause({ needKbps: 15000, bandwidth: 14e6, diskWait: 0.9, host: [{ kind: 'disk', text: 'Drives at 65%.' }] }).text, /^Drives at 65%\. It is waiting on the drive/);
});
