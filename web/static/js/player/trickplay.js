// Trickplay: the thumbnail previews shown while hovering or dragging the
// seek bar, from sprite sheets the server generates.
//
// Player methods (installed on the class in player.js). State they use:
// this.trick (the sheet layout), this.trickTried, this.tipThumb.

import { api } from '../api.js';

export const trickplay = {
  async loadTrickplay() {
    const fileId = this.file?.id;
    this.trickTried = Date.now();
    this.trick = null;
    try {
      const m = await api(`/api/files/${fileId}/trickplay`);
      if (this.file?.id !== fileId || !m || !m.count) return;
      this.trick = { ...m, fileId };
      // Warm the first sheet so the first hover is instant.
      new Image().src = `/api/files/${fileId}/trickplay/1`;
    } catch {}
  },

  // Draws the preview for time t; returns its displayed width (0 if none).
  renderThumb(t) {
    const m = this.trick, el = this.tipThumb;
    if (!m || m.fileId !== this.file?.id) { el.style.display = 'none'; return 0; }
    const per = m.cols * m.rows;
    const i = Math.max(0, Math.min(m.count - 1, Math.floor(t / m.interval)));
    const sheet = Math.floor(i / per) + 1, k = i % per;
    const scale = Math.min(1, (window.innerWidth < 600 ? 160 : 240) / m.width);
    const w = Math.round(m.width * scale), ht = Math.round(m.height * scale);
    Object.assign(el.style, {
      display: 'block', width: `${w}px`, height: `${ht}px`,
      backgroundImage: `url(/api/files/${m.fileId}/trickplay/${sheet})`,
      backgroundSize: `${m.cols * w}px auto`,
      backgroundPosition: `-${(k % m.cols) * w}px -${Math.floor(k / m.cols) * ht}px`,
    });
    return w;
  },
};
