// Thin fetch wrapper for the JSON API.

export class ApiError extends Error {
  constructor(message, status) { super(message); this.status = status; }
}

export async function api(path, { method = 'GET', body, signal, keepalive } = {}) {
  const opts = { method, signal, keepalive, credentials: 'same-origin', headers: {} };
  if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  let res;
  try {
    res = await fetch(path, opts);
  } catch (e) {
    if (e.name === 'AbortError') throw e;
    throw new ApiError('Network error: server unreachable', 0);
  }
  let data = null;
  const text = await res.text();
  if (text) {
    try { data = JSON.parse(text); } catch { data = text; }
  }
  if (!res.ok) {
    let msg = (data && data.error) || `HTTP ${res.status}`;
    if (res.status === 401 && !path.startsWith('/api/auth/')) {
      if (msg === 'not signed in') msg = 'Your session has ended';
      window.dispatchEvent(new CustomEvent('lex:unauthorized'));
    }
    throw new ApiError(msg, res.status);
  }
  return data;
}

// Image URL for an item. w is a width hint; v busts caches after metadata changes.
export function img(item, kind = 'poster', w = 320) {
  if (!item) return '';
  return `/api/items/${item.id}/image/${kind}?w=${w}&v=${item.updatedAt || 0}`;
}

// Cast indices refer to the stored, unfiltered cast list; no remote URL is sent by the browser.
export function castImg(item, index, w = 160) {
  return `/api/items/${item.id}/cast/${index}/image?w=${w}&v=${item.updatedAt || 0}`;
}
