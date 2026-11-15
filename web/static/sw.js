// Lex service worker: keeps the installed app from opening on a browser
// error page when the server can't be reached.
//
// - Only the app shell (index, css, the /js modules, icons, manifest) is
//   cached, network-first, so a new build is picked up on the next load.
// - Navigations that fail show /offline.html ("Can't reach your Lex server").
// - Everything else bypasses the worker untouched: /api/ (authenticated data,
//   images, HLS, files), range and media requests, the wasm vendor bundles,
//   cross-origin requests and anything that isn't a GET.
'use strict';

// The server replaces the null below with { version, shell } for this build
// (internal/api/server.go). The fallback keeps the file valid on its own.
const BUILD = /*LEX_BUILD*/null || { version: 'dev', shell: ['/', '/css/app.css', '/js/app.js'] };
const CACHE_PREFIX = 'lex-shell-';
const CACHE = CACHE_PREFIX + BUILD.version;
const OFFLINE = '/offline.html';
const PRECACHE = [...new Set([...BUILD.shell, OFFLINE, '/js/offline.js'])];
// A navigation that hangs this long counts as unreachable.
const NAV_TIMEOUT_MS = 10000;
const MEDIA_DESTINATIONS = new Set(['audio', 'video', 'track']);

// route decides how the worker handles a request: 'bypass' (don't call
// respondWith, so the browser fetches it as if there were no worker),
// 'navigate' (page load: network, else the offline page) or 'shell'
// (network, else the cached copy).
function route(req, origin, shell = PRECACHE) {
  if (req.method !== 'GET') return 'bypass';
  const url = new URL(req.url);
  if (url.origin !== origin) return 'bypass';
  const p = url.pathname;
  if (p.startsWith('/api/') || p.startsWith('/vendor/') || p === '/sw.js') return 'bypass';
  if (req.headers.has('range') || MEDIA_DESTINATIONS.has(req.destination)) return 'bypass';
  if (req.mode === 'navigate') return 'navigate';
  if (url.search === '' && shell.includes(p)) return 'shell';
  return 'bypass';
}

// cacheable: only complete, same-origin 200s are stored. A navigation is
// stored only when it is the HTML shell (a direct load of /icons/x.png is a
// navigation too).
function cacheable(res, mode) {
  if (!res || res.status !== 200 || res.type !== 'basic') return false;
  if (mode === 'navigate') return (res.headers.get('content-type') || '').startsWith('text/html');
  return true;
}

async function put(key, res) {
  try {
    const cache = await caches.open(CACHE);
    await cache.put(key, res);
  } catch { /* quota or private mode: the network copy was still served */ }
}

async function navigate(event) {
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), NAV_TIMEOUT_MS);
  try {
    const res = await fetch(event.request, { signal: ctrl.signal });
    // Every page load is the SPA shell, so it is stored once, as '/'.
    if (cacheable(res, 'navigate')) event.waitUntil(put('/', res.clone()));
    return res;
  } catch {
    return (await caches.match(OFFLINE, { cacheName: CACHE })) ||
      new Response('<!doctype html><meta charset="utf-8"><title>Lex</title><p>Can’t reach your Lex server.</p>',
        { status: 503, headers: { 'Content-Type': 'text/html; charset=utf-8' } });
  } finally {
    clearTimeout(timer);
  }
}

async function shell(event, pathname) {
  try {
    const res = await fetch(event.request);
    if (cacheable(res, 'shell')) event.waitUntil(put(pathname, res.clone()));
    return res;
  } catch (e) {
    const hit = await caches.match(pathname, { cacheName: CACHE });
    if (hit) return hit;
    throw e;
  }
}

self.addEventListener('install', (event) => {
  // Everything is network-first, so a new worker can take over at once.
  self.skipWaiting();
  event.waitUntil(caches.open(CACHE).then((cache) =>
    cache.addAll(PRECACHE.map((p) => new Request(p, { cache: 'reload' })))));
});

self.addEventListener('activate', (event) => {
  event.waitUntil((async () => {
    for (const key of await caches.keys()) {
      if (key.startsWith(CACHE_PREFIX) && key !== CACHE) await caches.delete(key);
    }
    await self.clients.claim();
  })());
});

self.addEventListener('fetch', (event) => {
  const kind = route(event.request, self.location.origin);
  if (kind === 'navigate') event.respondWith(navigate(event));
  else if (kind === 'shell') event.respondWith(shell(event, new URL(event.request.url).pathname));
});
