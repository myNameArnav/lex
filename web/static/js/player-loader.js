// The player and its helpers (MSE engine, audio effects, PGS…) load on
// demand, so a page load doesn't wait for them (#66). warmPlayer() starts
// the download once the first view is up, so pressing Play rarely waits.
// The sync checks answer for a player that isn't loaded yet: none is open.

import { toast } from './ui.js';

let mod = null;
let loading = null;

export function warmPlayer() {
  loading ||= import('./player.js').then((m) => (mod = m), (e) => { loading = null; throw e; });
  return loading;
}

export async function openPlayer(opts) {
  try {
    return (await warmPlayer()).openPlayer(opts);
  } catch (e) {
    toast(`Couldn't load the player: ${e.message}`, 'error');
    return null;
  }
}
export const isPlayerOpen = () => !!mod && mod.isPlayerOpen();
export const closePlayer = () => mod?.closePlayer();
export const pauseForOverlay = () => (mod ? mod.pauseForOverlay() : () => {});
