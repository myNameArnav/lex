// Audio: choosing the audio track, and the night mode / voice boost effects
// (audio-fx.js), including when an effect needs the server to re-plan.
//
// Player methods (installed on the class in player.js). State they use:
// this.audio (the chosen track), this.fx.

import { audioFxSupported } from '../audio-fx.js';
import { prefs } from '../prefs.js';
import { rememberTracks } from '../track-memory.js';

export const audio = {
  // Night mode and voice boost (audio-fx.js). Native HLS (Safari, AirPlay)
  // isn't routed through Web Audio: AirPlay wouldn't carry the effects.
  fxAvailable() { return audioFxSupported() && !this.plan?.hls; },

  applyFx() {
    if (!this.fxAvailable() && !this.fx.routed) return;
    try {
      this.fx.set({ night: prefs.get('nightMode'), voice: prefs.get('voiceBoost') });
    } catch (e) {
      console.warn('audio effects', e);
    }
  },

  // Voice boost changes what the server sends when it downmixes surround
  // audio to stereo itself.
  voiceNeedsReplan() {
    const a = (this.file?.info?.streams || []).find((s) => s.index === this.plan?.audio);
    return !!this.plan && !this.plan.audioCopy && (a?.channels || 0) > 2;
  },

  async chooseAudio(idx) {
    this.audio = idx;
    const a = (this.file.info?.streams || []).find((s) => s.index === idx);
    if (a?.language) {
      this.tracks = { ...this.tracks, audioLang: a.language };
      rememberTracks(this.trackKey, { audioLang: a.language });
    }
    await this.replan();
  },
};
