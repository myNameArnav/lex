// Night mode and voice boost, done in the browser with Web Audio so they
// cost the server nothing.
//
// Night mode compresses the dynamic range: loud scenes come down, and the
// compressor's own make-up gain (the Web Audio spec computes it from the
// threshold and ratio) brings quiet dialogue up. Voice boost lifts the centre channel of a
// multichannel track before the browser downmixes it to the speakers, and
// adds a little of the speech band of what's common to left and right (where
// a stereo mix puts dialogue). When the server downmixes surround audio
// itself, the player asks it for a centre-weighted downmix instead (see
// VoiceDownmix in internal/stream/voice.go).
//
// Routing a <video> through Web Audio can't be undone: createMediaElementSource
// takes the element's audio for good. So the graph is built the first time
// an effect is turned on, and turning effects off sets it to pass through.

export const NIGHT = { threshold: -30, knee: 12, ratio: 5, attack: 0.005, release: 0.25 };
export const VOICE = { centre: 2, mid: 0.6, low: 250, high: 4000 };

const Ctx = () => globalThis.AudioContext || globalThis.webkitAudioContext;

export const audioFxSupported = () => typeof Ctx() === 'function';

// fxLabel describes the active effects for the stats panel ('' for none).
export function fxLabel({ night, voice }) {
  if (night) return voice ? 'Night mode · voices boosted' : 'Night mode';
  return voice ? 'Voices boosted' : '';
}

// nodeSettings are the parameters of each node for these effects. Kept
// apart from the graph so they can be tested without Web Audio.
export function nodeSettings({ night, voice }) {
  return {
    centre: voice ? VOICE.centre : 1,
    mid: voice ? VOICE.mid : 0,
    comp: night ? { threshold: NIGHT.threshold, knee: NIGHT.knee, ratio: NIGHT.ratio, attack: NIGHT.attack, release: NIGHT.release } : { threshold: 0, knee: 0, ratio: 1, attack: 0.003, release: 0.25 },
    // A limiter stops the boosts clipping; with nothing on it lets
    // everything through.
    limit: night || voice ? { threshold: -1, knee: 0, ratio: 20, attack: 0.001, release: 0.1 } : { threshold: 0, knee: 0, ratio: 1, attack: 0.001, release: 0.1 },
  };
}

export class AudioFX {
  constructor(video) {
    this.video = video;
    this.ctx = null;
    this.state = { night: false, voice: false };
  }

  // Whether the element's audio goes through Web Audio (it does for good
  // once any effect has been on).
  get routed() { return !!this.ctx; }

  get active() { return this.state.night || this.state.voice; }

  set(state) {
    this.state = { night: !!state.night, voice: !!state.voice };
    if (!this.ctx && !this.active) return;
    if (!this.ctx) this.build();
    this.apply();
    this.resume();
  }

  // Browsers start an AudioContext suspended without a recent user
  // gesture; the player calls this on play.
  resume() {
    if (this.ctx && this.ctx.state === 'suspended') this.ctx.resume().catch(() => {});
  }

  build() {
    const ctx = new (Ctx())();
    const n = this.nodes = {};
    n.src = ctx.createMediaElementSource(this.video);
    // Up-mix whatever the element plays to 5.1 by the speaker rules: mono
    // goes to the centre, stereo to front left and right, 5.1 stays.
    n.in = ctx.createGain();
    Object.assign(n.in, { channelCount: 6, channelCountMode: 'explicit', channelInterpretation: 'speakers' });
    n.split = ctx.createChannelSplitter(6);
    n.merge = ctx.createChannelMerger(6);
    n.centre = ctx.createGain();
    n.src.connect(n.in).connect(n.split);
    for (let i = 0; i < 6; i++) {
      if (i === 2) n.split.connect(n.centre, 2).connect(n.merge, 0, 2);
      else n.split.connect(n.merge, i, i);
    }
    // Mid (L+R) speech band, added back to left and right.
    n.midSum = ctx.createGain();
    Object.assign(n.midSum, { channelCount: 1, channelCountMode: 'explicit', channelInterpretation: 'speakers' });
    n.midSum.gain.value = 0.5;
    n.split.connect(n.midSum, 0);
    n.split.connect(n.midSum, 1);
    n.hp = ctx.createBiquadFilter();
    n.hp.type = 'highpass';
    n.hp.frequency.value = VOICE.low;
    n.lp = ctx.createBiquadFilter();
    n.lp.type = 'lowpass';
    n.lp.frequency.value = VOICE.high;
    n.mid = ctx.createGain();
    n.midSum.connect(n.hp).connect(n.lp).connect(n.mid);
    n.mid.connect(n.merge, 0, 0);
    n.mid.connect(n.merge, 0, 1);
    // The compressor takes the 5.1 mix down to stereo (centre at -3 dB,
    // LFE left out); a centre lift survives that.
    n.comp = ctx.createDynamicsCompressor();
    n.limit = ctx.createDynamicsCompressor();
    n.merge.connect(n.comp).connect(n.limit).connect(ctx.destination);
    this.ctx = ctx;
  }

  apply() {
    const n = this.nodes, s = nodeSettings(this.state), t = this.ctx.currentTime;
    // Short ramps so a toggle doesn't click.
    const to = (param, v) => { param.cancelScheduledValues(t); param.setTargetAtTime(v, t, 0.03); };
    to(n.centre.gain, s.centre);
    to(n.mid.gain, s.mid);
    for (const [node, cfg] of [[n.comp, s.comp], [n.limit, s.limit]]) {
      for (const [k, v] of Object.entries(cfg)) node[k].setValueAtTime(v, t);
    }
  }

  close() {
    if (this.ctx) this.ctx.close().catch(() => {});
    this.ctx = null;
    this.nodes = null;
  }
}
