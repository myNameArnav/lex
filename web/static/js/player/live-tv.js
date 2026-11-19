// Live TV: tuning in to a channel, following its schedule from one program
// to the next, the LIVE / Go live button, and switching channels.
//
// Player methods (installed on the class in player.js). State they use:
// this.live = { channelId, channels, token, channel, now, next, skew,
// switching } and this.liveBtn; they call start(), resetForNext(), seekTo().

import { h, toast, fmtProgram, fmtClock } from '../ui.js';
import { api } from '../api.js';
import { retryApi } from './retry.js';

export const liveTv = {
  // tune joins a channel at the program airing now (or at at, server Unix
  // ms: the end of the program that just finished), as far into it as the
  // server's clock says.
  async tune(channelId, at = 0) {
    const live = this.live;
    live.channelId = channelId;
    const token = live.token = (live.token || 0) + 1;
    // Nothing of the next program has played: checkLive waits for it.
    this.started = false;
    this.showSpinner(true);
    this.setStatus('');
    try {
      const r = await retryApi(`/api/tv/${encodeURIComponent(channelId)}${at ? `?at=${at}` : ''}`, undefined, {
        cancelled: () => this.closed || token !== live.token,
        onRetry: (n) => this.setStatus(`Reconnecting to the server… (attempt ${n + 1})`),
      });
      if (this.closed || token !== live.token) return;
      // skew: how far this device's clock is ahead of the server's.
      Object.assign(live, { channel: r.channel, now: r.now, next: r.next || null, skew: Date.now() - r.serverTime });
      if (!live.channels.some((c) => c.id === r.channel.id)) live.channels = [r.channel];
      await this.start(r.now.item.id, Math.max(0, this.livePosition()));
    } catch (e) {
      if (this.closed || token !== live.token) return;
      // Live TV turned off, or nothing left to air: no point retrying.
      if (e.status === 404) this.showError(e.message, { title: 'Live TV isn’t available', retry: false });
      else this.planFailed(e);
    }
  },

  // livePosition: where in the current program the broadcast is now (s).
  livePosition() {
    const { now, skew } = this.live;
    return (Date.now() - skew - now.start) / 1000;
  },

  // The schedule moves on when the program's slot ends (a program running
  // past midnight is cut there), or when its file ends.
  checkLive() {
    const live = this.live;
    if (!live?.now || !this.started || this.stopped || this.errEl || live.switching) return;
    const slot = (live.now.end - live.now.start) / 1000;
    if (this.video.currentTime >= slot - 0.5) { this.nextProgram(); return; }
    const behind = this.livePosition() - this.video.currentTime > 15;
    if (this.liveBtn.classList.contains('behind') !== behind) {
      this.liveBtn.classList.toggle('behind', behind);
      this.liveBtn.textContent = behind ? 'Go live' : 'LIVE';
      this.liveBtn.title = behind ? 'Jump to what’s on now' : 'Playing live';
    }
  },

  async nextProgram() {
    const live = this.live;
    if (live.switching || this.stopped) return;
    live.switching = true;
    try {
      this.resetForNext();
      await this.tune(live.channelId, live.now?.end || 0);
    } finally { live.switching = false; }
  },

  // Back to the live point: a seek within this program, or tuning in again
  // once it has finished airing.
  goLive() {
    const live = this.live;
    if (!live?.now) return;
    const pos = this.livePosition();
    if (pos < (live.now.end - live.now.start) / 1000 - 5) {
      this.seekTo(pos);
      this.video.play().catch(() => {});
    } else this.nextProgram();
  },

  // The channel list (the Channels button): every channel with what's on
  // it now, from the guide (loaded when the list opens).
  async loadChannelGuide() {
    const live = this.live;
    try {
      const r = await api('/api/tv');
      live.guide = new Map(r.channels.map((c) => [c.id, c.programs]));
      live.channels = r.channels.map(({ id, number, name }) => ({ id, number, name }));
      live.skew = Date.now() - r.serverTime;
    } catch { return; }
    if (this.menuName === 'channels') this.renderMenu();
  },

  fillChannels(m) {
    const { channels, channelId, guide } = this.live;
    m.classList.add('pm-wide');
    m.append(h('div', { class: 'pm-title' }, 'Channels'),
      h('div', { role: 'radiogroup', 'aria-label': 'Channels' }, channels.map((ch) => {
        const now = guide?.get(ch.id)?.[0];
        const on = ch.id === channelId;
        return h('button', { class: `pm-opt pm-chan${on ? ' on' : ''}`, role: 'radio', 'aria-checked': String(on), 'data-key': `ch-${ch.id}`, onclick: () => this.chooseChannel(ch.id) },
          h('span', { class: 'pm-chan-n' }, ch.number),
          h('span', { class: 'pm-opt-l' }, ch.name, now ? h('small', null, `${fmtProgram(now.item)} · until ${fmtClock(now.end)}`) : null));
      })));
  },

  async chooseChannel(id) {
    this.closeMenu();
    if (id === this.live.channelId) { this.goLive(); return; }
    const ch = this.live.channels.find((c) => c.id === id);
    if (ch) toast(`${ch.number} · ${ch.name}`);
    this.resetForNext();
    await this.tune(id);
  },

  async switchChannel(dir) {
    const { channels, channelId } = this.live;
    if (channels.length < 2 || this.stopped) return;
    const i = channels.findIndex((c) => c.id === channelId);
    const ch = channels[(i + dir + channels.length) % channels.length];
    toast(`${ch.number} · ${ch.name}`);
    this.resetForNext();
    await this.tune(ch.id);
  },
};
