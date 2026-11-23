// Skipping intros and credits: the segments the server detected (or a
// credits chapter), the Skip Intro / Skip Credits button, and auto-skip.
//
// Player methods (installed on the class in player.js). State they use:
// this.segments, this.introSkipped / creditsSkipped, this.skipBtn /
// skipKind.

import { h, icons, toast } from '../ui.js';
import { prefs } from '../prefs.js';

export const skipSegments = {
  intro() { return (this.segments || []).find((s) => s.kind === 'intro'); },

  // The end credits: detected by the server, else a chapter named like
  // credits in the last 30%, else null (Up next then starts a fixed time
  // before the end).
  credits() {
    const seg = (this.segments || []).find((s) => s.kind === 'credits');
    if (seg) return seg;
    const d = this.duration(), chaps = this.file?.info?.chapters || [];
    const i = chaps.findIndex((c) => /credit|outro|ending|ed\b/i.test(c.title || '') && d && c.start > d * 0.7);
    if (i < 0) return null;
    const c = chaps[i];
    return { kind: 'credits', start: c.start, end: c.end > c.start ? c.end : chaps[i + 1]?.start || d, source: 'chapter' };
  },

  // What the Skip button skips at t: the intro, or the credits when no
  // episode follows (with one, the Up next card is the way on).
  skippable(t) {
    if (!this.started || this.live) return null;
    const inside = (seg) => !!seg && t >= seg.start - 0.5 && t < seg.end - 1.5;
    const intro = this.intro();
    if (inside(intro)) return { kind: 'intro', seg: intro };
    const credits = this.credits();
    if (inside(credits) && (!this.detail?.next || this.upNextDismissed)) return { kind: 'credits', seg: credits };
    return null;
  },

  skipSegment({ kind, seg }) {
    if (kind === 'intro') {
      this.introSkipped = true;
      this.seekTo(seg.end);
      return;
    }
    this.creditsSkipped = true;
    // Credits that run to the end go straight to the end card (seekTo stops
    // just short of the end, so 'ended' fires); otherwise on to what follows
    // them, like a post-credits scene.
    const d = this.duration();
    this.seekTo(d && seg.end >= d - 5 ? d : seg.end);
  },

  checkSkip() {
    const s = this.skippable(this.video.currentTime);
    const auto = s && (s.kind === 'intro' ? prefs.get('autoSkipIntro') && !this.introSkipped : prefs.get('autoSkipCredits') && !this.creditsSkipped);
    if (auto) {
      this.skipSegment(s);
      toast(s.kind === 'intro' ? 'Skipped intro' : 'Skipped credits');
      return;
    }
    if (this.skipBtn && this.skipKind !== s?.kind) {
      const hadFocus = this.skipBtn.contains(document.activeElement);
      this.skipBtn.remove();
      this.skipBtn = null;
      this.skipKind = null;
      if (hadFocus) this.root.focus();
    }
    if (s && !this.skipBtn) {
      const label = s.kind === 'intro' ? 'Skip intro' : 'Skip credits';
      this.skipKind = s.kind;
      this.skipBtn = h('button', { class: 'btn skip-intro', 'aria-keyshortcuts': 's', title: `${label} (s)`, onclick: (e) => {
        e.stopPropagation();
        // The segment as it is now, not when the button appeared.
        const cur = this.skippable(this.video.currentTime);
        if (cur) this.skipSegment(cur);
      } }, label, h('span', { html: icons.next }));
      // Early in Tab order, right after Close (it's positioned, so it doesn't move).
      this.root.insertBefore(this.skipBtn, this.root.querySelector(':scope > .p-bot'));
    }
  },
};
