// Polling for settings and dashboard pages: every() repeats a refresh while
// its page is showing, and stopTimers() ends them all when the page changes.

// Pollers owned by the current admin view. Each stops at its next tick once
// its view is no longer current (navigated away or re-rendered; a navigation
// cancelled by the leave guard keeps them running), and all stop when the
// session expires.
let pollers = [];

function stopTimers() { pollers.forEach((p) => clearInterval(p.t)); pollers = []; }

// every polls fn every ms while ctx is current. Ticks are skipped while the
// tab is hidden or a dialog is open (a re-render would pull the dialog's
// opener out from under it), and run at once when the tab is shown again.
// fn should throw (or reject) when its request fails: after two failures in
// a row onFail(lastOkAt) is called, and onRecover() after the next success.
/**
 * @param {any} ctx
 * @param {number} ms
 * @param {() => any} fn
 * @param {{ onFail?: (lastOkAt: number) => void, onRecover?: () => void }} [hooks]
 */
export function every(ctx, ms, fn, { onFail, onRecover } = {}) {
  let fails = 0, lastOk = Date.now(), busy = false;
  const p = {};
  p.tick = async () => {
    if (!ctx.isCurrent()) { clearInterval(p.t); pollers = pollers.filter((x) => x !== p); return; }
    if (busy || document.hidden || document.querySelector('dialog[open]')) return;
    busy = true;
    try {
      await fn();
      if (fails >= 2) onRecover?.();
      fails = 0;
      lastOk = Date.now();
    } catch {
      if (++fails === 2) onFail?.(lastOk);
    } finally {
      busy = false;
    }
  };
  p.t = setInterval(p.tick, ms);
  pollers.push(p);
  return p.t;
}

window.addEventListener('lex:unauthorized', stopTimers);
document.addEventListener('visibilitychange', () => { if (!document.hidden) pollers.forEach((p) => p.tick()); });
