// The file's chapters: listing them, which one is playing, and jumping
// between them (PgUp / PgDn).
//
// Player methods (installed on the class in player.js). They read
// this.file.info.chapters. The chapter ticks on the seek bar are drawn in
// renderTime(); the chapter menu is in the Settings menu.

export const chapters = {
  // The file's chapters in order, each with a title to show ('Chapter 3'
  // when untitled) and named: false when that title is only a number.
  chapters() {
    const d = this.duration();
    return (this.file?.info?.chapters || [])
      .filter((c) => !d || c.start < d)
      .sort((a, b) => a.start - b.start)
      .map((c, i) => ({ ...c, label: c.title || `Chapter ${i + 1}`, named: !!c.title && !/^chapter \d+$/i.test(c.title) }));
  },

  // The index of the chapter playing at t, or -1 before the first one.
  chapterAt(t, list = this.chapters()) {
    return list.findLastIndex((c) => c.start <= t + 0.25);
  },

  // dir 1: the next chapter. dir -1: back to the start of this one, or to
  // the one before when already near its start (like a CD player).
  jumpChapter(dir) {
    const list = this.chapters();
    if (list.length < 2) { this.osd('No chapters'); return; }
    const t = this.video.currentTime, i = this.chapterAt(t, list);
    const to = dir > 0 ? list[i + 1] : i >= 0 && t - list[i].start > 3 ? list[i] : list[i - 1];
    if (!to) { this.osd(dir > 0 ? 'Last chapter' : 'First chapter'); return; }
    this.seekTo(to.start);
    this.osd(to.label);
  },
};
