// player.js installs the methods of each player/*.js part onto
// Player.prototype at load time. This tells TypeScript about them.
import type { liveTv } from '../static/js/player/live-tv.js';
import type { upNext } from '../static/js/player/up-next.js';
import type { skipSegments } from '../static/js/player/skip-segments.js';
import type { chapters } from '../static/js/player/chapters.js';
import type { trickplay } from '../static/js/player/trickplay.js';
import type { subtitles } from '../static/js/player/subtitles.js';
import type { audio } from '../static/js/player/audio.js';
import type { stats } from '../static/js/player/stats.js';
import type { menus } from '../static/js/player/menus.js';

type Parts = typeof liveTv & typeof upNext & typeof skipSegments & typeof chapters
  & typeof trickplay & typeof subtitles & typeof audio & typeof stats & typeof menus;

declare module '../static/js/player.js' {
  interface Player extends Parts {
    // State the parts keep on the player.
    menu: HTMLElement | null; // menus.js
    statsEl: HTMLElement | null; // stats.js
    trick: any; // trickplay.js: the sprite sheet layout
    trickTried: number; // trickplay.js
    subTrack: TextTrack | null; // subtitles.js
    upNextAt: number; // up-next.js
    cdTimer: ReturnType<typeof setInterval> | null; // up-next.js
    hlsOK: boolean | undefined; // cached nativeHls()
  }
}
