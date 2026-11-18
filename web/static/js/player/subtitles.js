// Subtitles: loading a track (text, ASS through JASSUB, or PGS images),
// drawing text cues in the player's own overlay, timing offsets, the
// viewer's style, searching for and downloading subtitles, and the iPhone
// native-fullscreen handoff.
//
// Player methods (installed on the class in player.js). State they use:
// this.subtitle (the chosen track), this.subToken, this.cues / subsEl,
// this.ass and this.pgs renderers, this.subOffset.

import { api } from '../api.js';
import { state } from '../app.js';
import { detectCaps } from '../caps.js';
import { PgsRenderer } from '../pgs.js';
import { prefs, lang1to3 } from '../prefs.js';
import { subStyleVars } from '../substyle.js';
import { rememberTracks, subtitleChoice } from '../track-memory.js';
import { toast, h, streamLabel, confirmDialog, run, clear, langName, modal } from '../ui.js';
import { vendorURL, LANG_CHOICES } from './format.js';
import { fetchSub, styleASS, fetchSubBytes, canRemoveSub } from './tracks.js';

// JASSUB (ASS subtitles) needs workers, OffscreenCanvas, WebAssembly and
// requestVideoFrameCallback.
export function assSupported() {
  return typeof Worker !== 'undefined' && typeof OffscreenCanvas !== 'undefined' && typeof WebAssembly !== 'undefined' &&
    'requestVideoFrameCallback' in HTMLVideoElement.prototype;
}

export const subtitles = {
  subById(idx) { return (this.file.subtitles || []).find((s) => s.index === idx); },

  // Subtitles are fetched first and attached as a blob: a <track> whose src is
  // still loading holds the video at HAVE_CURRENT_DATA (per the HTML spec),
  // and extracting subs from a large file can take a while on a Pi.
  // Resolves false when the track failed to load (or was superseded). plain
  // skips the ASS renderer (native fullscreen can only show a <track>).
  async setSubtitleTrack(idx, { plain = false } = {}) {
    const v = this.video;
    const token = (this.subToken = (this.subToken || 0) + 1);
    for (const t of [...v.querySelectorAll('track')]) t.remove();
    for (const tt of v.textTracks) tt.mode = 'disabled';
    this.subTrack = null;
    this.renderCues(null);
    this.destroyASS();
    this.destroyPGS();
    if (this.subBlob) { URL.revokeObjectURL(this.subBlob); this.subBlob = null; }
    const s = this.subById(idx);
    if (s && !s.textSub && this.plan?.drawSubs === 'pgs' && this.plan.subtitle === idx) return this.setPGS(s, token);
    if (!s || !s.textSub) return true;
    // Styled (ASS/SSA) subtitles are rendered with libass so positioning,
    // fonts and karaoke survive; anything else goes through our overlay.
    if (!plain && /^(ass|ssa)$/.test(s.codec) && !s.downloaded && prefs.get('assRender') !== false && await this.setASS(s, token)) return token === this.subToken;
    if (token !== this.subToken) return false;
    const slow = setTimeout(() => { if (token === this.subToken) toast('Extracting subtitles from the file… they will appear shortly'); }, 1500);
    const install = (text) => {
      const old = this.subBlob;
      this.subBlob = URL.createObjectURL(new Blob([text], { type: 'text/vtt' }));
      const track = h('track', { kind: 'subtitles', label: streamLabel(s), srclang: (s.language || 'und').slice(0, 2), src: this.subBlob, default: true });
      for (const t of [...v.querySelectorAll('track')]) t.remove();
      v.appendChild(track);
      const tt = track.track;
      tt.mode = this.root.classList.contains('native-fs') ? 'showing' : 'hidden';
      const render = () => this.renderCues(tt);
      tt.addEventListener('cuechange', render);
      track.addEventListener('load', () => { this.cueKey = null; render(); });
      this.subTrack = tt;
      if (old) URL.revokeObjectURL(old);
    };
    try {
      const url = `/api/files/${this.file.id}/subs/${idx}.vtt?v=${this.file.mtime || 0}`;
      const { text, partial } = await fetchSub(url);
      if (token !== this.subToken || this.closed) return false;
      install(text);
      if (partial) this.pollSub(token, url, install);
      return true;
    } catch (e) {
      if (token !== this.subToken) return false;
      toast(`Could not load subtitles: ${e.message}`, 'error');
      // Don't leave the failed track checked in the menu (or asked for by the next plan).
      if (this.subtitle === idx) {
        this.subtitle = -1;
        if (this.menuName === 'tracks') this.renderMenu();
      }
      return false;
    } finally {
      clearTimeout(slow);
    }
  },

  // The server is still extracting this track (big file, slow disk) and sent
  // what it has so far: keep fetching until it's complete.
  async pollSub(token, url, apply, fetcher = fetchSub) {
    for (let wait = 3000; ; wait = Math.min(wait * 1.5, 15000)) {
      await new Promise((r) => setTimeout(r, wait));
      if (token !== this.subToken || this.closed) return;
      try {
        const { text, data, partial } = await fetcher(url);
        if (token !== this.subToken || this.closed) return;
        await apply(data ?? text);
        if (!partial) return;
      } catch (e) {
        if (token === this.subToken) toast(`Could not load subtitles: ${e.message}`, 'error');
        return;
      }
    }
  },

  async setASS(s, token) {
    if (!assSupported()) return false;
    try {
      const [{ default: JASSUB }, fonts] = await Promise.all([
        import(vendorURL('jassub/jassub.js')),
        api(`/api/files/${this.file.id}/fonts`).catch(() => []),
      ]);
      if (token !== this.subToken || this.closed) return true;
      const url = `/api/files/${this.file.id}/subs/${s.index}.ass?v=${this.file.mtime || 0}`;
      const { text, partial } = await fetchSub(url);
      this.assRaw = text;
      const subContent = styleASS(text);
      if (token !== this.subToken || this.closed) return true;
      this.jassub = new JASSUB({
        video: this.video, subContent, fonts,
        workerUrl: vendorURL('jassub/worker.js'),
        wasmUrl: vendorURL('jassub/jassub-worker.wasm'),
        modernWasmUrl: vendorURL('jassub/jassub-worker-modern.wasm'),
        // Liberation Sans is metric-compatible with Arial, the usual ASS default.
        availableFonts: Object.fromEntries(['liberation sans', 'arial', 'helvetica', 'arial unicode ms'].map((f) => [f, vendorURL('jassub/default.woff2')])),
        queryFonts: false, // querying the OS font list can stall rendering for seconds
        timeOffset: -this.subOffset,
        prescaleHeightLimit: 1080, maxRenderHeight: 1440,
      });
      await this.jassub.ready;
      if (partial) {
        const j = this.jassub;
        this.pollSub(token, url, async (text) => {
          if (this.jassub !== j) return;
          this.assRaw = text;
          await j.renderer.setTrack(styleASS(text));
          if (this.video.paused && j._lastDemandTime) j.manualRender(j._lastDemandTime, true).catch(() => {});
        });
      }
      return true;
    } catch (e) {
      console.warn('ASS renderer unavailable, using plain subtitles', e);
      this.destroyASS();
      return false;
    }
  },

  // PGS subtitles drawn on a canvas (pgs.js) from the track the server
  // copies out. If that fails, the next plan burns them in instead.
  async setPGS(s, token) {
    const url = `/api/files/${this.file.id}/subs/${s.index}.sup?v=${this.file.mtime || 0}`;
    const slow = setTimeout(() => { if (token === this.subToken) toast('Extracting subtitles from the file… they will appear shortly'); }, 1500);
    try {
      const { data, partial } = await fetchSubBytes(url);
      if (token !== this.subToken || this.closed) return false;
      this.pgs = new PgsRenderer(this.video, this.pgsCanvas, { offset: this.subOffset || 0 });
      this.pgs.setTrack(data);
      if (partial) {
        const r = this.pgs;
        this.pollSub(token, url, (d) => { if (this.pgs === r) r.setTrack(d); }, fetchSubBytes);
      }
      return true;
    } catch (e) {
      if (token !== this.subToken || this.closed) return false;
      console.warn('PGS renderer failed; burning in', e);
      this.destroyPGS();
      this.noPgs = true;
      toast('Could not draw these subtitles here: burning them in (transcoding)');
      this.replan();
      return false;
    } finally {
      clearTimeout(slow);
    }
  },

  destroyPGS() {
    if (this.pgs) { this.pgs.destroy(); this.pgs = null; }
  },

  destroyASS() {
    if (this.jassub) { this.jassub.destroy().catch(() => {}); this.jassub = null; }
  },

  // Positive offset = subtitles appear later.
  setSubOffset(v) {
    this.subOffset = Math.max(-600, Math.min(600, Math.round(v * 10) / 10));
    rememberTracks(this.trackKey, { subOffset: this.subOffset });
    if (this.jassub) {
      this.jassub.timeOffset = -this.subOffset;
      if (this.jassub._lastDemandTime) this.jassub.manualRender(this.jassub._lastDemandTime, true).catch(() => {});
    }
    if (this.pgs) this.pgs.setOffset(this.subOffset);
    this.cueKey = null;
    this.renderCues(this.subTrack);
    if (this.menu && this.menuName === 'tracks' && (!this.menuPage || this.menuPage === 'subopts')) this.renderMenu();
  },

  nudgeSubs(d) {
    if (!this.subById(this.subtitle)?.textSub && !this.pgs) { toast('Turn on text subtitles to adjust timing', '', { key: 'subOffset' }); return; }
    this.setSubOffset(this.subOffset + d);
    toast(`Subtitle timing ${this.subOffset > 0 ? '+' : ''}${this.subOffset.toFixed(1)}s`, '', { key: 'subOffset' });
  },

  // Find subtitles on OpenSubtitles.com and add them to this file.
  searchSubtitles() {
    const wasPlaying = !this.video.paused;
    const langSel = h('select', { class: 'input' }, LANG_CHOICES.map(([k, l]) => h('option', { value: k, selected: k === (this.tracks?.subLang || prefs.get('subLang') || 'eng') }, l)));
    const list = h('div', { class: 'sub-results' });
    const status = h('div', { class: 'muted small', role: 'status' });
    const downloaded = h('div', { class: 'sub-results' });
    const remove = async (e, d) => {
      const btn = e.currentTarget;
      if (!await confirmDialog('Remove these downloaded subtitles for everyone?', 'Remove', true, 'Remove subtitles?')) return;
      const ok = await run(btn, () => api(`/api/files/${this.file.id}/subs/${d.index}`, { method: 'DELETE' }), 'Subtitles removed');
      if (!ok) return;
      this.file.subtitles = this.file.subtitles.filter((x) => x.index !== d.index);
      this.syncFileSubs();
      if (this.subtitle === d.index) this.chooseSubtitle(-1);
      renderDownloaded();
    };
    const renderDownloaded = () => {
      clear(downloaded);
      const mine = (this.file.subtitles || []).filter((x) => x.downloaded);
      if (!mine.length) return;
      downloaded.append(h('div', { class: 'small muted', style: { margin: '2px 0 4px' } }, 'Downloaded for this file'));
      for (const d of mine) {
        downloaded.append(h('div', { class: 'sub-res' },
          h('div', { class: 'sub-res-main' }, h('b', null, d.title || 'Downloaded'), h('span', { class: 'muted small' }, langName(d.language))),
          canRemoveSub(d) ? h('button', { class: 'btn sm danger', onclick: (e) => remove(e, d) }, 'Remove') : null));
      }
    };
    const failed = (e) => {
      status.textContent = '';
      if (e.code === 'not_allowed') {
        list.append(h('div', { class: 'sub-err', role: 'alert' }, 'Only admins can download subtitles on this server.'));
        return;
      }
      if (e.code === 'no_key') {
        list.append(h('div', { class: 'sub-err', role: 'alert' }, state.me?.isAdmin
          ? ['Subtitle search isn’t set up yet. Add an OpenSubtitles API key in ', h('a', { href: '#/settings/metadata', onclick: (ev) => { ev.preventDefault(); this.goTo('#/settings/metadata'); } }, 'Settings › Metadata'), '.']
          : 'Subtitle search isn’t set up on this server.'));
        return;
      }
      list.append(h('div', { class: 'sub-err row', role: 'alert' }, h('span', { class: 'spacer' }, e.message), h('button', { class: 'btn sm', onclick: search }, 'Retry')));
    };
    const search = async () => {
      clear(list);
      status.textContent = 'Searching OpenSubtitles…';
      try {
        const res = await api(`/api/files/${this.file.id}/subsearch?lang=${encodeURIComponent(langSel.value)}`);
        status.textContent = res.length ? `${res.length} result${res.length === 1 ? '' : 's'} · exact-release matches first` : 'No subtitles found for this language.';
        for (const r of res.slice(0, 40)) {
          const tags = [
            r.hashMatch ? h('span', { class: 'tag ok' }, 'Exact match') : null,
            r.trusted ? h('span', { class: 'tag' }, 'Trusted') : null,
            r.hearingImpaired ? h('span', { class: 'tag' }, 'SDH') : null,
            r.ai ? h('span', { class: 'tag warn' }, 'Machine translated') : null,
          ];
          const btn = h('button', { class: 'btn sm primary' }, 'Use');
          btn.onclick = async () => {
            btn.disabled = true; btn.textContent = 'Downloading…';
            try {
              const st = await api(`/api/files/${this.file.id}/subsearch`, { method: 'POST', body: { fileId: r.fileId, language: r.language, release: r.release || r.fileName, hearingImpaired: r.hearingImpaired } });
              const { remaining, ...track } = st;
              if (remaining != null) state.subsRemaining = remaining;
              this.file.subtitles = [...(this.file.subtitles || []), track];
              this.syncFileSubs();
              m.close();
              await this.chooseSubtitle(track.index);
              toast(remaining != null ? `Subtitles added · ${remaining} download${remaining === 1 ? '' : 's'} left today` : 'Subtitles added');
            } catch (e) {
              if (e.code === 'quota') {
                state.subsRemaining = 0;
                allowance.textContent = allowanceText();
                list.prepend(h('div', { class: 'sub-err', role: 'alert' }, 'Today’s OpenSubtitles download allowance has run out. It renews within 24 hours; subtitles already downloaded still work.'));
                btn.textContent = 'Use';
                return;
              }
              toast(e.message, 'error');
              btn.disabled = false; btn.textContent = 'Use';
            }
          };
          list.append(h('div', { class: 'sub-res' },
            h('div', { class: 'sub-res-main' },
              h('b', { title: r.fileName }, r.release || r.fileName || 'Untitled'),
              h('div', { class: 'sub-tags' }, langName(lang1to3(r.language)), ` · ${r.downloads.toLocaleString()} downloads`, ...tags)),
            btn));
        }
      } catch (e) {
        failed(e);
      }
    };
    // The allowance is shared by the server, known only from the last
    // download (this session): shown so nobody spends the last few blindly.
    const allowanceText = () => (state.subsRemaining == null ? '' : state.subsRemaining > 0
      ? `${state.subsRemaining} download${state.subsRemaining === 1 ? '' : 's'} left today on this server` : 'No downloads left today on this server');
    const allowance = h('div', { class: 'small muted' }, allowanceText());
    // A filter for this search, not the preferred subtitle language.
    langSel.onchange = search;
    if (wasPlaying) this.video.pause();
    const m = modal({
      title: 'Search subtitles', wide: true, parent: this.root,
      body: [h('div', { class: 'row wrap', style: { gap: '10px', alignItems: 'center' } },
        h('label', { class: 'row', style: { gap: '10px' } }, h('span', { class: 'muted' }, 'Language'), langSel), h('div', { class: 'spacer' }), status), allowance, downloaded, list],
      onClose: () => {
        if (this.closed) return;
        // The menu item that opened this is gone: back to the CC button.
        this.ccBtn.focus();
        if (wasPlaying) this.video.play().catch(() => {});
      },
    });
    renderDownloaded();
    search();
  },

  // Keep the item's file list in sync so switching versions keeps new subs.
  syncFileSubs() {
    const f = (this.files || []).find((x) => x.id === this.file.id);
    if (f) f.subtitles = this.file.subtitles;
  },

  // The choice is remembered for this device, but turning subtitles off here
  // never turns off forced/foreign-audio subtitles everywhere (that's a
  // Settings choice), and a forced track doesn't change the preferred language.
  async chooseSubtitle(idx) {
    const prev = this.subById(this.subtitle);
    const next = this.subById(idx);
    this.subtitle = idx;
    // PGS is drawn here when the current plan allows it (not native HLS);
    // other image subtitles, or burning in from Settings, need a transcode.
    const draw = next && !next.textSub && this.canDrawPGS(next);
    const needBurn = next && !next.textSub && !draw;
    const hadBurn = !!this.plan?.burnSubs;
    // The choice is remembered for this show or movie, not as the global
    // preference (Settings → Audio & subtitles keeps that).
    const mode = this.tracks?.subMode ?? prefs.get('subMode');
    const remember = (patch) => { if (!Object.keys(patch).length) return; this.tracks = { ...this.tracks, ...patch }; rememberTracks(this.trackKey, patch); };
    if (!next) remember(subtitleChoice(null, mode));
    let ok = true;
    if (needBurn || hadBurn) {
      if (needBurn) toast('Image-based subtitles need to be burned in: transcoding');
      await this.replan();
    } else {
      if (draw) this.plan = { ...this.plan, drawSubs: 'pgs', subtitle: idx };
      ok = await this.setSubtitleTrack(idx);
    }
    if (!next || !ok || this.subtitle !== idx) return;
    remember(subtitleChoice(next, mode));
  },

  canDrawPGS(s) {
    return s.codec === 'hdmv_pgs_subtitle' && !s.external && !!detectCaps().pgs && !this.noPgs && !prefs.get('burnImageSubs') && !!this.plan && !this.plan.hls;
  },

  // iPhone native fullscreen shows only <track> cues: show ours there (the
  // plain-text version of styled subtitles) and go back to the overlay after.
  onNativeFs(on) {
    this.root.classList.toggle('native-fs', on);
    if (on && this.jassub) { this.nativeAss = true; this.setSubtitleTrack(this.subtitle, { plain: true }); return; }
    if (!on && this.nativeAss) { this.nativeAss = false; this.setSubtitleTrack(this.subtitle); return; }
    if (this.subTrack) this.subTrack.mode = on ? 'showing' : 'hidden';
  },

  applySubStyle() {
    const r = this.root;
    r.dataset.subPos = prefs.get('subPos');
    for (const [k, v] of Object.entries(subStyleVars(prefs.all()))) r.style.setProperty(k, v);
    // Styled subtitles: apply the same preferences to the dialogue styles.
    const j = this.jassub;
    if (j && this.assRaw) {
      clearTimeout(this.assRestyle);
      this.assRestyle = setTimeout(async () => {
        if (this.jassub !== j) return;
        await j.renderer.setTrack(styleASS(this.assRaw));
        if (j._lastDemandTime) j.manualRender(j._lastDemandTime, true).catch(() => {});
      }, 50);
    }
  },

  // Called on cuechange and every timeupdate: Chrome doesn't always fire
  // cuechange when playback starts right at a cue boundary.
  renderCues(tt) {
    const el = this.subsEl;
    if (!el) return;
    const off = this.subOffset || 0;
    const t = this.video.currentTime - off;
    const src = off ? tt?.cues : tt?.activeCues;
    const cues = src ? [...src].filter((c) => c.startTime <= t + 0.05 && c.endTime > t).sort((a, b) => a.startTime - b.startTime) : [];
    const key = cues.map((c) => `${c.startTime}:${c.text}`).join('|');
    if (key === this.cueKey) return;
    this.cueKey = key;
    el.replaceChildren();
    for (const c of cues) {
      const line = h('div', { class: 'cue' });
      // getCueAsHTML returns a safe DocumentFragment (keeps <i>/<b>).
      try { line.appendChild(c.getCueAsHTML()); } catch { line.textContent = c.text; }
      el.appendChild(line);
    }
  },
};
