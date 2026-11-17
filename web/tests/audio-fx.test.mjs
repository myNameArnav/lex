import assert from 'node:assert/strict';
import test from 'node:test';
import { AudioFX, nodeSettings, fxLabel, NIGHT, VOICE } from '../static/js/audio-fx.js';

// A fake Web Audio graph: records connections and parameter values.
function fakeAudio() {
  const made = { contexts: 0, sources: 0, edges: [] };
  class Param {
    constructor(v = 0) { this.value = v; }
    cancelScheduledValues() {}
    setTargetAtTime(v) { this.value = v; }
    setValueAtTime(v) { this.value = v; }
  }
  class Node {
    constructor(kind) { this.kind = kind; this.channelCount = 2; }
    connect(to, out = 0, inp = 0) { made.edges.push([this.kind, to.kind, out, inp]); return to; }
  }
  const comp = () => Object.assign(new Node('comp'), { threshold: new Param(-24), knee: new Param(30), ratio: new Param(12), attack: new Param(0.003), release: new Param(0.25) });
  class Ctx {
    constructor() { made.contexts++; this.state = 'running'; this.currentTime = 0; this.destination = new Node('dest'); }
    createMediaElementSource() { made.sources++; return new Node('src'); }
    createGain() { return Object.assign(new Node('gain'), { gain: new Param(1) }); }
    createChannelSplitter() { return new Node('split'); }
    createChannelMerger() { return new Node('merge'); }
    createBiquadFilter() { return Object.assign(new Node('biquad'), { frequency: new Param(350) }); }
    createDynamicsCompressor() { return comp(); }
    resume() { return Promise.resolve(); }
    close() { this.state = 'closed'; return Promise.resolve(); }
  }
  globalThis.AudioContext = Ctx;
  return made;
}

test('nothing is routed through Web Audio until an effect is on', () => {
  const made = fakeAudio();
  const fx = new AudioFX({});
  fx.set({ night: false, voice: false });
  assert.equal(made.contexts, 0);
  assert.equal(fx.routed, false);
  fx.set({ night: true, voice: false });
  assert.equal(made.sources, 1);
  assert.equal(fx.routed, true);
  assert.equal(fx.nodes.comp.threshold.value, NIGHT.threshold);
  assert.equal(fx.nodes.comp.ratio.value, NIGHT.ratio);
  // Compressor, then limiter, then the speakers.
  assert.ok(made.edges.some(([from, to]) => from === 'comp' && to === 'dest'));
  assert.ok(made.edges.some(([from, to]) => from === 'comp' && to === 'comp'));
  // Turning everything off passes audio through; the element can't be
  // routed back, so the graph stays and isn't built twice.
  fx.set({ night: false, voice: false });
  assert.equal(made.sources, 1);
  assert.equal(fx.nodes.comp.threshold.value, 0);
  assert.equal(fx.nodes.comp.ratio.value, 1);
  assert.equal(fx.nodes.limit.ratio.value, 1);
  assert.equal(fx.nodes.centre.gain.value, 1);
  assert.equal(fx.nodes.mid.gain.value, 0);
  fx.set({ night: false, voice: true });
  assert.equal(fx.nodes.centre.gain.value, VOICE.centre);
  assert.equal(fx.nodes.mid.gain.value, VOICE.mid);
  assert.equal(fx.nodes.limit.ratio.value, 20);
  fx.close();
  assert.equal(fx.routed, false);
});

test('the centre channel goes through its own gain, the others straight to the merger', () => {
  const made = fakeAudio();
  const fx = new AudioFX({});
  fx.set({ voice: true });
  const splits = made.edges.filter(([from]) => from === 'split');
  assert.deepEqual(splits.filter(([, to]) => to === 'merge').map(([, , out, inp]) => [out, inp]), [[0, 0], [1, 1], [3, 3], [4, 4], [5, 5]]);
  assert.ok(splits.some(([, to, out]) => to === 'gain' && out === 2));
});

test('effect settings and labels', () => {
  assert.equal(nodeSettings({}).comp.ratio, 1);
  assert.equal(nodeSettings({ night: true }).comp.ratio, NIGHT.ratio);
  assert.equal(fxLabel({}), '');
  assert.equal(fxLabel({ night: true }), 'Night mode');
  assert.equal(fxLabel({ voice: true }), 'Voices boosted');
  assert.equal(fxLabel({ night: true, voice: true }), 'Night mode · voices boosted');
});
