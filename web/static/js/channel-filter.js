// Filtering Live TV channels by name, for the guide's search and the
// lineup in Settings. A query is words: a channel matches when one of its
// words starts with each of them ("hd", "star"), and a word with a minus in
// front leaves out the channels that have it ("-hd"). A number matches the
// channel's number too.

/** @typedef {{ name: string, number?: number }} Named */

// words splits a channel name into lowercase words: "&TV HD" → ["&tv", "tv", "hd"].
// A word glued to punctuation ("&TV", "Star-Plus") is also split at it.
export function words(name) {
  const out = new Set();
  for (const w of name.toLowerCase().split(/\s+/)) {
    if (!w) continue;
    out.add(w);
    for (const part of w.split(/[^\p{L}\p{N}]+/u)) if (part) out.add(part);
  }
  return [...out];
}

/**
 * parseQuery reads a query into the words a channel must have and those it
 * mustn't.
 * @param {string} q
 */
export function parseQuery(q) {
  const want = [], not = [];
  for (const t of q.toLowerCase().split(/\s+/)) {
    if (t.length > 1 && t[0] === '-') not.push(t.slice(1));
    else if (t && t !== '-') want.push(t);
  }
  return { want, not };
}

/**
 * matcher answers whether a channel matches q.
 * @param {string} q
 * @returns {(ch: Named) => boolean}
 */
export function matcher(q) {
  const { want, not } = parseQuery(q);
  if (!want.length && !not.length) return () => true;
  const has = (ws, ch, t) => ws.some((w) => w.startsWith(t)) || (/^\d+$/.test(t) && String(ch.number) === t);
  return (ch) => {
    const ws = words(ch.name);
    return want.every((t) => has(ws, ch, t)) && !not.some((t) => has(ws, ch, t));
  };
}

/**
 * commonWords are the words most channel names share ("HD", "News"), most
 * common first, for one-click filters: at least min channels each, and not
 * all of them (a word every channel has filters nothing).
 * @param {Named[]} chs
 */
export function commonWords(chs, limit = 12, min = 3) {
  const count = new Map(), label = new Map();
  for (const ch of chs) {
    // Whole words only, as written ("HD", not "hd" and "h"), once per channel.
    const seen = new Set();
    for (const raw of ch.name.split(/[^\p{L}\p{N}]+/u)) {
      const w = raw.toLowerCase();
      if (w.length < 2 || /^\d+$/.test(w) || seen.has(w)) continue;
      seen.add(w);
      count.set(w, (count.get(w) || 0) + 1);
      if (!label.has(w)) label.set(w, raw);
    }
  }
  // Counted as the filter counts them ("hd" also matches "HD2").
  return [...count].filter(([, n]) => n >= min && n < chs.length)
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .slice(0, limit)
    .map(([w]) => ({ word: w, label: label.get(w), count: chs.filter(matcher(w)).length }));
}

/**
 * toggleWord cycles a word in a query: not there → wanted → left out → not
 * there; what else the query says stays.
 * @param {string} q
 * @param {string} w
 */
export function toggleWord(q, w) {
  const toks = q.split(/\s+/).filter(Boolean);
  const lw = w.toLowerCase();
  const i = toks.findIndex((t) => t.toLowerCase() === lw);
  const j = toks.findIndex((t) => t.toLowerCase() === `-${lw}`);
  if (i >= 0) toks[i] = `-${w}`;
  else if (j >= 0) toks.splice(j, 1);
  else toks.push(w);
  return toks.join(' ');
}

/**
 * wordState is how q uses w: 'want', 'not' or ''.
 * @param {string} q
 * @param {string} w
 */
export function wordState(q, w) {
  const { want, not } = parseQuery(q);
  const lw = w.toLowerCase();
  return want.includes(lw) ? 'want' : not.includes(lw) ? 'not' : '';
}
