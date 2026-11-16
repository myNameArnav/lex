import assert from 'node:assert/strict';
import test from 'node:test';
import { qualityOptions, transcodeHeight } from '../static/js/prefs.js';

const labels = (opts) => Object.fromEntries(qualityOptions(opts));

test('quality labels follow the server cap on transcode height', () => {
  // A Raspberry Pi caps transcodes at 720p: no option may promise 1080p.
  const pi = labels({ maxHeight: 720 });
  assert.equal(pi[0], 'Original');
  assert.equal(pi[40000], '40 Mbps · 720p');
  assert.equal(pi[8000], '8 Mbps · 720p');
  assert.equal(pi[4000], '4 Mbps · 720p');
  assert.equal(pi[1500], '1.5 Mbps · 480p');
  assert.equal(pi[720], '720 kbps · 360p');
  // A 1080p server: 40 Mbps transcodes to at most 1080p too.
  const pc = labels({ maxHeight: 1080 });
  assert.equal(pc[40000], '40 Mbps · 1080p');
  assert.equal(pc[8000], '8 Mbps · 1080p');
  assert.equal(pc[3000], '3 Mbps · 720p');
  // An older server that doesn't report its cap.
  assert.equal(labels({})[20000], '20 Mbps · 1080p');
});

test('quality labels in the player follow the source', () => {
  // A 480p source never comes out larger.
  const sd = labels({ maxHeight: 1080, srcHeight: 480, srcKbps: 2500 });
  assert.equal(sd[1500], '1.5 Mbps · 480p');
  assert.equal(sd[720], '720 kbps · 360p');
  // At or above the source bitrate the video can play untouched, so the
  // label names no resolution it might not deliver.
  assert.equal(sd[3000], '3 Mbps');
  // A 4K source at 30 Mbps on a 720p server.
  const uhd = labels({ maxHeight: 720, srcHeight: 2160, srcKbps: 30000 });
  assert.equal(uhd[40000], '40 Mbps');
  assert.equal(uhd[20000], '20 Mbps · 720p');
});

test('transcode height is even, like the server rounds it', () => {
  assert.equal(transcodeHeight(8000, 1080, 803), 802);
  assert.equal(transcodeHeight(8000, 0, 0), 1080);
});
