// Live TV: tuning in to a channel, following its schedule from one program
// to the next, the LIVE / Go live button, and switching channels.
//
// Player methods (installed on the class in player.js). State they use:
// this.live = { channelId, channels, token, channel, now, next, skew,
// switching } and this.liveBtn; they call start(), resetForNext(), seekTo().

import { h, icons, toast, fmtProgram, fmtClock } from '../ui.js';
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
      Object.assign(live, { channel: r.channel, now: r.now || null, next: r.next || null, skew: Date.now() - r.serverTime, stream: r.stream || null });
      if (!live.channels.some((c) => c.id === r.channel.id)) live.channels = [r.channel];
      this.root.classList.toggle('p-stream', !!r.stream);
      if (r.stream) { this.playStream(); return; }
      await this.start(r.now.item.id, Math.max(0, this.livePosition()));
    } catch (e) {
      if (this.closed || token !== live.token) return;
      // Live TV turned off, or nothing left to air: no point retrying.
      if (e.status === 404) this.showError(e.message, { title: 'Live TV isn’t available', retry: false });
      else this.planFailed(e);
    }
  },

  // playStream plays a live channel from the internet: the server's MP4 of
  // it, with no seeking and no playback session (nothing to resume).
  playStream() {
    const live = this.live, v = this.video;
    this.teardown();
    this.plan = null; this.file = null; this.files = []; this.segments = [];
    this.detail = { next: null };
    this.item = live.now?.item || { id: 0, kind: 'live', title: live.channel.name };
    this.renderTitle();
    this.nextBtn.classList.toggle('hidden', !(live.channels.length > 1));
    this.started = false;
    // A new request each time: the server starts fetching it afresh.
    v.src = `${live.stream.url}?t=${Date.now()}`;
    v.play().catch(() => { if (!this.closed) { this.playBtn.innerHTML = icons.play; this.showSpinner(false); } });
    this.setupMediaSession();
  },

  // The stream failed or ended: say why (the server's answer to the same
  // request), with Retry tuning in again.
  async streamFailed() {
    const live = this.live;
    if (this.closed || this.errEl || !live?.stream) return;
    let msg = 'The channel’s stream stopped.';
    const ctl = new AbortController();
    try {
      const res = await fetch(`${live.stream.url}?t=${Date.now()}`, { signal: ctl.signal, credentials: 'same-origin' });
      if (!res.ok) msg = (await res.json().catch(() => null))?.error || msg;
    } catch {}
    ctl.abort();
    if (this.closed || this.errEl || live.stream == null) return;
    this.showError(msg, { title: 'This channel isn’t playing' });
  },

  // A live channel's guide moved on: what's on now and next, for the title.
  async refreshStreamInfo() {
    const live = this.live;
    if (live.refreshing) return;
    live.refreshing = true;
    try {
      const r = await api(`/api/tv/${encodeURIComponent(live.channelId)}`);
      if (this.closed || !live.stream || r.channel.id !== live.channelId) return;
      Object.assign(live, { now: r.now || null, next: r.next || null, skew: Date.now() - r.serverTime });
      this.item = live.now?.item || { id: 0, kind: 'live', title: live.channel.name };
      this.renderTitle();
    } catch {} finally {
      // Without a guide there's nothing to follow; with one, at most a minute apart.
      setTimeout(() => { live.refreshing = false; }, 60000);
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
    if (live?.stream) {
      if (live.now && Date.now() - live.skew >= live.now.end) this.refreshStreamInfo();
      return;
    }
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
    // A live stream that ended: tell why rather than reconnecting in a loop.
    if (live.stream) { this.streamFailed(); return; }
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
    if (live?.stream) {
      // As far on as it has arrived.
      const b = this.video.buffered;
      if (b.length) this.video.currentTime = Math.max(0, b.end(b.length - 1) - 1);
      this.video.play().catch(() => {});
      return;
    }
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
