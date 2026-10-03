import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';

async function loadAPI(respond) {
  const events = [];
  const context = vm.createContext({
    fetch: async (path, opts) => respond(path, opts),
    window: { dispatchEvent: (e) => events.push(e.type) },
    CustomEvent: class { constructor(type) { this.type = type; } },
    JSON, Error,
  });
  const source = await readFile(new URL('../static/js/api.js', import.meta.url), 'utf8');
  const module = new vm.SourceTextModule(source, { context });
  await module.link(() => { throw new Error('api.js has no imports'); });
  await module.evaluate();
  return { api: module.namespace.api, events };
}

const reply = (status, body) => ({ ok: status < 400, status, text: async () => JSON.stringify(body) });

test('an expired session is reported once, in plain words', async () => {
  const { api, events } = await loadAPI(() => reply(401, { error: 'not signed in' }));
  await assert.rejects(api('/api/home'), (e) => e.status === 401 && e.message === 'Your session has ended');
  assert.deepEqual(events, ['lex:unauthorized']);
});

test('a failed sign-in is not a session expiry', async () => {
  const { api, events } = await loadAPI(() => reply(401, { error: 'invalid username or password' }));
  await assert.rejects(api('/api/auth/login', { method: 'POST', body: {} }), (e) => e.message === 'invalid username or password');
  assert.deepEqual(events, []);
});

test('network failures have status 0', async () => {
  const { api } = await loadAPI(() => { throw new TypeError('Failed to fetch'); });
  await assert.rejects(api('/api/home'), (e) => e.status === 0);
});
