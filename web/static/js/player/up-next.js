// What happens at the end of a title: the Up next card and its countdown,
// playing the next episode, and the Finished card when nothing follows.
//
// Player methods (installed on the class in player.js). State they use:
// this.detail.next, this.upNextShown / upNextDismissed / upNextAt /
// upNextEl, this.cdTimer, this.endCard, this.shuffle.

import { h, icons, toast, fmtEpisode } from '../ui.js';
import { img } from '../api.js';
import { prefs } from '../prefs.js';

export const upNext = {
  checkUpNext() {
    const next = this.detail?.next;
    const d = this.duration(), t = this.video.currentTime;
    if (this.live || !next || !d || this.upNextShown || this.upNextDismissed || this.video.paused || this.errEl) return;
    const credits = this.credits();
    const at = credits ? credits.start : d - Math.max(20, prefs.get('countdown') + 5);
    if (t < at) return;
    this.upNextShown = true;
    this.upNextAt = at;
    let n = prefs.get('countdown');
    const autoplay = prefs.get('autoplayNext');
    // Skipping credits with autoplay on: no countdown, the next episode starts now.
    if (credits && autoplay && prefs.get('autoSkipCredits') && !this.creditsSkipped) {
      this.creditsSkipped = true;
      toast('Skipped credits');
      this.playNext();
      return;
    }
    // One text node: the button's flex gap would split 'Play in 9 s'.
    const label = h('span', null, autoplay ? `Play in ${n}s` : 'Play now');
    // Announced once, not on every tick of the countdown.
    const live = h('span', { class: 'sr-only', 'aria-live': 'polite' });
    const card = h('div', { class: 'upnext', role: 'region', 'aria-label': 'Up next', onclick: (e) => e.stopPropagation() },
      h('img', { src: img(next, 'thumb', 480), alt: '' }),
      h('div', { class: 'b' },
        h('div', { class: 'small muted' }, 'Up next'),
        h('b', null, `${fmtEpisode(next)} — ${next.title}`),
        h('div', { class: 'row' },
          h('button', { class: 'btn primary sm', onclick: () => this.playNext() }, h('span', { html: icons.play, style: { width: '16px', display: 'inline-flex' } }), label),
          h('button', { class: 'btn sm', onclick: () => { this.cancelUpNext(true); this.playBtn.focus(); } }, 'Cancel'))),
      live);
    // Never cover an open menu.
    this.closeMenu();
    this.root.appendChild(card);
    this.upNextEl = card;
    setTimeout(() => { live.textContent = `Up next: ${fmtEpisode(next)} ${next.title}${autoplay ? `, playing in ${n} seconds` : ''}`; }, 100);
    if (autoplay) {
      this.cdTimer = setInterval(() => {
        if (this.video.paused) return;
        n--;
        label.textContent = `Play in ${n}s`;
        if (n <= 0) { clearInterval(this.cdTimer); this.playNext(); }
      }, 1000);
    }
  },

  // dismiss: the viewer cancelled Up next, so this episode just ends.
  cancelUpNext(dismiss) {
    clearInterval(this.cdTimer);
    this.cdTimer = null;
    this.upNextEl?.remove();
    this.upNextEl = null;
    if (dismiss) this.upNextDismissed = true;
  },

  onEnded() {
    if (this.live) return this.nextProgram();
    if (this.detail?.next && prefs.get('autoplayNext') && !this.upNextDismissed) this.playNext();
    else this.onMovieEnd();
  },

  // Nothing plays next: offer the way out, or watching again. No auto-close.
  onMovieEnd() {
    this.cancelUpNext(false);
    this.removeEndCard();
    this.showUI(true);
    if (!this.item || this.errEl) return;
    const it = this.item;
    const card = h('div', { class: 'upnext end-card', role: 'region', 'aria-label': 'Finished', onclick: (e) => e.stopPropagation() },
      h('div', { class: 'b' },
        h('div', { class: 'small muted' }, 'Finished'),
        h('b', null, it.kind === 'episode' ? `${fmtEpisode(it)} — ${it.title}` : it.title),
        h('div', { class: 'row' },
          h('button', { class: 'btn primary sm', onclick: () => this.close() }, 'Back to details'),
          h('button', { class: 'btn sm', onclick: () => { this.removeEndCard(); this.seekTo(0); this.video.play().catch(() => {}); } }, 'Play from start'),
          // Autoplay off or Up next cancelled: the next episode is still a click away.
          this.detail?.next ? h('button', { class: 'btn sm', onclick: () => this.playNext() }, `Play ${fmtEpisode(this.detail.next)}`) : null)));
    this.closeMenu(false);
    this.root.appendChild(card);
    this.endCard = card;
    card.querySelector('button').focus();
  },

  removeEndCard() {
    if (!this.endCard) return;
    const hadFocus = this.endCard.contains(document.activeElement);
    this.endCard.remove();
    this.endCard = null;
    if (hadFocus) this.root.focus();
  },

  async playNext() {
    if (this.live) return this.switchChannel(1);
    const next = this.detail?.next;
    if (!next || this.stopped) return;
    if (this.shuffle) this.shuffle.pos++;
    this.resetForNext();
    const ud = next.userData;
    await this.start(next.id, ud?.position > 0 ? ud.position : 0);
    if (this.onClose) this.onChanged = true;
  },
};
