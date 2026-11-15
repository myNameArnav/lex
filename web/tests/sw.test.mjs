import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';

const ORIGIN = 'https://lex.example.com';
const source = await readFile(new URL('../static/sw.js', import.meta.url), 'utf8');

// Load sw.js the way the server stamps it, against a stub worker global with
// an in-memory Cache Storage and a scriptable fetch.
function fixture({ build = { version: 'v1', shell: ['/', '/css/app.css', '/js/app.js', '/offline.html', '/js/offline.js'] }, network } = {}) {
  const listeners = {};
  const stores = new Map(); // cache name -> Map(path -> Response)
  const keyOf = (r) => new URL(typeof r === 'string' ? r : r.url, ORIGIN).pathname;
  const open = async (name) => {
    if (!stores.has(name)) stores.set(name, new Map());
    const m = stores.get(name);
    return {
      put: async (k, res) => { m.set(keyOf(k), res); },
      addAll: async (reqs) => { for (const r of reqs) m.set(keyOf(r), new Response(`cached ${keyOf(r)}`, { headers: { 'content-type': 'text/html' } })); },
    };
  };
  const caches = {
    open,
    keys: async () => [...stores.keys()],
    delete: async (k) => stores.delete(k),
    match: async (k, { cacheName } = {}) => stores.get(cacheName)?.get(keyOf(k))?.clone(),
  };
  const fetches = [];
  const self = {
    location: new URL(`${ORIGIN}/sw.js`),
    addEventListener: (type, fn) => { listeners[type] = fn; },
    skipWaiting: () => {},
    clients: { claim: async () => {} },
  };
  const fetch = async (req, opts) => {
    fetches.push(keyOf(req));
    if (!network) throw new TypeError('Failed to fetch');
    return network(req, opts);
  };
  // Relative URLs resolve against the worker's origin, as in a browser.
  const WorkerRequest = class extends Request { constructor(input, init) { super(typeof input === 'string' ? new URL(input, ORIGIN) : input, init); } };
  const ctx = vm.createContext({ self, caches, fetch, Request: WorkerRequest, Response, Headers, URL, AbortController, setTimeout, clearTimeout, Set });
  vm.runInContext(source.replace('/*LEX_BUILD*/null', JSON.stringify(build)) + '\n;globalThis.__route = route; globalThis.__cacheable = cacheable; globalThis.__CACHE = CACHE;', ctx);

  // dispatch runs a fetch event: undefined when the worker let it through.
  const dispatch = async (path, init = {}, mode = 'cors', destination = '') => {
    const request = new Request(new URL(path, ORIGIN), init);
    Object.defineProperty(request, 'mode', { value: mode });
    Object.defineProperty(request, 'destination', { value: destination });
    let responded, waits = [];
    listeners.fetch({ request, respondWith: (p) => { responded = p; }, waitUntil: (p) => waits.push(p) });
    const res = responded && await responded;
    await Promise.all(waits);
    return res;
  };
  const lifecycle = async (type) => {
    let wait;
    listeners[type]({ waitUntil: (p) => { wait = p; } });
    await wait;
  };
  return { ctx, stores, caches, fetches, dispatch, lifecycle };
}

const req = (path, init = {}, mode = 'cors', destination = '') => {
  const r = new Request(new URL(path, ORIGIN), init);
  Object.defineProperty(r, 'mode', { value: mode });
  Object.defineProperty(r, 'destination', { value: destination });
  return r;
};

test('route sends only shell files and page loads through the worker', () => {
  const { ctx } = fixture();
  const route = (r) => ctx.__route(r, ORIGIN);
  assert.equal(route(req('/', {}, 'navigate', 'document')), 'navigate');
  assert.equal(route(req('/settings/users', {}, 'navigate', 'document')), 'navigate');
  assert.equal(route(req('/css/app.css')), 'shell');
  assert.equal(route(req('/js/app.js')), 'shell');
  // Not in the shell, or not a plain GET of it.
  assert.equal(route(req('/js/app.js?x=1')), 'bypass');
  assert.equal(route(req('/js/admin.js')), 'bypass');
  assert.equal(route(req('/css/app.css', { method: 'HEAD' })), 'bypass');
  assert.equal(route(req('/sw.js')), 'bypass');
  assert.equal(route(req('/vendor/lexremux/remux.wasm')), 'bypass');
  assert.equal(route(req('https://cdn.example.org/js/app.js')), 'bypass');
});

test('route never touches API, media, HLS or range requests', () => {
  const { ctx } = fixture();
  const route = (r) => ctx.__route(r, ORIGIN);
  assert.equal(route(req('/api/me')), 'bypass');
  assert.equal(route(req('/api/auth/login', { method: 'POST', body: '{}' })), 'bypass');
  assert.equal(route(req('/api/items/3/image/poster')), 'bypass');
  assert.equal(route(req('/api/hls/abc/index.m3u8')), 'bypass');
  assert.equal(route(req('/api/files/7')), 'bypass');
  // Even a page load of an API URL, and any ranged or media request.
  assert.equal(route(req('/api/files/7', {}, 'navigate', 'document')), 'bypass');
  assert.equal(route(req('/js/app.js', { headers: { Range: 'bytes=0-99' } })), 'bypass');
  assert.equal(route(req('/', {}, 'no-cors', 'video')), 'bypass');
  assert.equal(route(req('/', {}, 'no-cors', 'audio')), 'bypass');
  assert.equal(route(req('/', {}, 'no-cors', 'track')), 'bypass');
});

test('cacheable keeps only complete same-origin responses, and HTML for page loads', () => {
  const { ctx } = fixture();
  const res = (status, type = 'basic', ctype = 'text/html; charset=utf-8') => {
    const r = new Response(status === 204 ? null : 'x', { status, headers: { 'content-type': ctype } });
    Object.defineProperty(r, 'type', { value: type });
    return r;
  };
  assert.equal(ctx.__cacheable(res(200), 'navigate'), true);
  assert.equal(ctx.__cacheable(res(200, 'basic', 'image/png'), 'navigate'), false);
  assert.equal(ctx.__cacheable(res(200, 'basic', 'text/css'), 'shell'), true);
  assert.equal(ctx.__cacheable(res(206), 'shell'), false);
  assert.equal(ctx.__cacheable(res(404), 'shell'), false);
  assert.equal(ctx.__cacheable(res(200, 'opaque'), 'shell'), false);
});

test('install precaches the shell; activate drops older builds', async () => {
  const f = fixture();
  await f.caches.open('lex-shell-old');
  await f.caches.open('other-app');
  await f.lifecycle('install');
  assert.deepEqual([...f.stores.get('lex-shell-v1').keys()].sort(), ['/', '/css/app.css', '/js/app.js', '/js/offline.js', '/offline.html']);
  await f.lifecycle('activate');
  assert.deepEqual([...f.stores.keys()].sort(), ['lex-shell-v1', 'other-app']);
});

test('offline: page loads get the offline page, shell files the cached copy', async () => {
  const f = fixture();
  await f.lifecycle('install');
  const page = await f.dispatch('/settings', {}, 'navigate', 'document');
  assert.equal(await page.text(), 'cached /offline.html');
  const css = await f.dispatch('/css/app.css');
  assert.equal(await css.text(), 'cached /css/app.css');
  assert.equal(await f.dispatch('/api/me'), undefined, 'API calls are left to the browser');
});

test('offline before install: a built-in message instead of a browser error', async () => {
  const f = fixture();
  const page = await f.dispatch('/', {}, 'navigate', 'document');
  assert.equal(page.status, 503);
  assert.match(await page.text(), /Can’t reach your Lex server/);
});

test('online: network first, and the fresh copy replaces the cached one', async () => {
  const f = fixture({ network: (r) => new Response(`fresh ${new URL(r.url).pathname}`, { headers: { 'content-type': new URL(r.url).pathname === '/' || r.mode === 'navigate' ? 'text/html' : 'text/css' } }) });
  await f.lifecycle('install');
  // Response objects built in node have type 'default'; the worker stores only 'basic'.
  const realFetch = f.ctx.fetch;
  f.ctx.fetch = async (...a) => { const r = await realFetch(...a); Object.defineProperty(r, 'type', { value: 'basic' }); return r; };
  const css = await f.dispatch('/css/app.css');
  assert.equal(await css.text(), 'fresh /css/app.css');
  assert.equal(await f.stores.get('lex-shell-v1').get('/css/app.css').text(), 'fresh /css/app.css');
  const page = await f.dispatch('/library/2', {}, 'navigate', 'document');
  assert.equal(await page.text(), 'fresh /library/2');
  assert.equal(await f.stores.get('lex-shell-v1').get('/').text(), 'fresh /library/2', 'every page load is the shell, stored as /');
  // API calls never reach the worker's fetch, so nothing is stored for them.
  const before = f.fetches.length;
  assert.equal(await f.dispatch('/api/items/1'), undefined);
  assert.equal(f.fetches.length, before);
  assert.ok(![...f.stores.get('lex-shell-v1').keys()].some((k) => k.startsWith('/api/')));
});
