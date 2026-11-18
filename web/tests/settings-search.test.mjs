import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';

// A small DOM: enough for h(), the search combobox and reveal(). Selectors
// are lists of "tag", ".class" or "tag.class".
function fakeDom() {
  class Node {}
  class Text extends Node {
    constructor(text) { super(); this.textContent = text; this.parentNode = null; }
  }
  const matches = (el, sel) => sel.split(',').some((one) => {
    const [tag, ...classes] = one.trim().split('.');
    return (!tag || el.tagName === tag.toUpperCase()) && classes.every((c) => el.classList.contains(c));
  });
  class Element extends Node {
    constructor(tag) {
      super();
      this.tagName = tag.toUpperCase();
      this.childNodes = []; this.attrs = {}; this.dataset = {}; this.style = {}; this.listeners = {};
      this.parentNode = null; this.className = ''; this.disabled = false; this.value = '';
      const el = this;
      this.classList = {
        contains: (c) => el.className.split(/\s+/).includes(c),
        add: (c) => { if (!el.classList.contains(c)) el.className = `${el.className} ${c}`.trim(); },
        remove: (c) => { el.className = el.className.split(/\s+/).filter((x) => x && x !== c).join(' '); },
      };
    }
    get id() { return this.attrs.id || ''; }
    set id(v) { this.attrs.id = v; }
    get type() { return this.attrs.type || ''; }
    get hidden() { return 'hidden' in this.attrs; }
    set hidden(v) { if (v) this.attrs.hidden = ''; else delete this.attrs.hidden; }
    get children() { return this.childNodes.filter((c) => c instanceof Element); }
    get firstElementChild() { return this.children[0] || null; }
    get textContent() { return this.childNodes.map((c) => c.textContent).join(''); }
    set textContent(v) { this.replaceChildren(new Text(String(v))); }
    set innerHTML(v) { this.html = v; }
    get offsetWidth() { return 0; }
    setAttribute(k, v) { this.attrs[k] = String(v); }
    getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
    removeAttribute(k) { delete this.attrs[k]; }
    addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
    dispatch(type, props = {}) {
      const e = { type, target: this, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }, stopPropagation() {}, ...props };
      for (let n = this; n; n = n.parentNode) (n.listeners?.[type] || []).forEach((fn) => fn(e));
      return e;
    }
    appendChild(c) { c.parentNode = this; this.childNodes.push(c); return c; }
    append(...cs) { cs.forEach((c) => this.appendChild(c)); }
    replaceChildren(...cs) { this.childNodes = []; cs.forEach((c) => this.appendChild(c)); }
    contains(n) { for (let x = n; x; x = x.parentNode) if (x === this) return true; return false; }
    querySelectorAll(sel) {
      const out = [];
      const walk = (n) => n.children.forEach((c) => { if (matches(c, sel)) out.push(c); walk(c); });
      walk(this);
      return out;
    }
    querySelector(sel) { return this.querySelectorAll(sel)[0] || null; }
    scrollIntoView(opts) { this.scrolled = opts; }
    focus() { document.activeElement = this; }
  }
  const document = {
    body: new Element('body'), activeElement: null,
    createElement: (t) => new Element(t), createTextNode: (t) => new Text(t),
    querySelector: () => null, querySelectorAll: () => [], addEventListener() {},
  };
  return { Node, document };
}

async function load() {
  const dom = fakeDom();
  const context = vm.createContext({
    Node: dom.Node, document: dom.document, window: { addEventListener() {} },
    setTimeout: () => 0, clearTimeout() {}, requestAnimationFrame() {}, matchMedia: () => ({ matches: false }),
    Date, Intl, String, Math, Promise,
  });
  const mod = async (file, link) => {
    const m = new vm.SourceTextModule(await readFile(new URL(`../static/js/${file}`, import.meta.url), 'utf8'), { context, identifier: file });
    await m.link(link);
    return m;
  };
  const ui = await mod('ui.js', () => { throw new Error('ui.js has no imports'); });
  const search = await mod('settings-search.js', (spec) => { assert.equal(spec, './ui.js'); return ui; });
  await search.evaluate();
  return { ...search.namespace, h: ui.namespace.h, document: dom.document };
}

test('search ranks label matches first and hides server settings from non-admins', async () => {
  const { searchSettings } = await load();
  const tmdb = searchSettings('tmdb', { isAdmin: true });
  assert.equal(tmdb[0].label, 'TMDB API key or read access token');
  assert.equal(tmdb[0].page, 'metadata');
  assert.equal(tmdb[0].section, 'Providers');
  // Keywords and several words: "api key" finds every key field.
  const keys = searchSettings('api key', { isAdmin: true }).map((e) => e.label);
  assert.ok(keys.includes('Radarr API key') && keys.includes('OpenSubtitles API key'));
  assert.ok(searchSettings('hevc', { isAdmin: true }).some((e) => e.label === 'Hardware HEVC decoding (Raspberry Pi)'));
  assert.equal(searchSettings('tmdb', { isAdmin: false }).length, 0);
  const pw = searchSettings('password', { isAdmin: false });
  assert.ok(pw.length > 0);
  assert.ok(pw.every((e) => !e.admin), 'non-admins only see their own pages');
  assert.equal(searchSettings('   ', { isAdmin: true }).length, 0);
  // Radarr's address and key live on one page with Sonarr's.
  assert.ok(searchSettings('radarr', { isAdmin: true }).filter((e) => e.label).every((e) => e.page === 'arr'));
});

test('every page is in a group, users get only their own pages, and pages have unique ids', async () => {
  const { PAGES, GROUPS, searchSettings } = await load();
  assert.equal(new Set(PAGES.map((p) => p[0])).size, PAGES.length);
  for (const [id, , , admin, group] of PAGES) {
    assert.ok(GROUPS.includes(group), `${id} has a known group`);
    assert.equal(admin, group !== 'You', `${id}: only "You" pages are for everyone`);
  }
  // Pages appear grouped, in the groups' order.
  const order = [...PAGES].map((p) => GROUPS.indexOf(p[4]));
  assert.ok(order.every((g, i) => i === 0 || g >= order[i - 1]));
  for (const q of ['theme', 'subtitle size', 'shortcuts']) {
    assert.ok(searchSettings(q, { isAdmin: false }).length > 0, `users can find ${q}`);
  }
});

test('every indexed setting names a label, toggle or heading the page shows', async () => {
  const { ENTRIES } = await load();
  const src = (await readFile(new URL('../static/js/admin.js', import.meta.url), 'utf8')).replace(/\\'/g, '\'');
  for (const e of ENTRIES) {
    for (const name of [e.label, e.section]) {
      if (!name || (name === 'Password' && e.page === 'account')) continue;
      assert.ok(src.includes(`'${name}'`), `admin.js shows no "${name}" (${e.page})`);
    }
  }
});

test('search → navigate → focus: typing, Enter, and the field is focused and highlighted', async () => {
  const { searchBox, reveal, h, document } = await load();
  // The Metadata page as settingsView renders it (field() and toggleRow()).
  const keyInput = h('input', { type: 'password' });
  const page = h('div', { class: 'settings-content' },
    h('div', { class: 'form-section' }, h('h2', null, 'Providers'),
      h('label', { class: 'field' }, h('span', null, 'Language'), h('input', { type: 'text' })),
      h('label', { class: 'field' }, h('span', null, 'TMDB API key or read access token'), keyInput, h('div', { class: 'help' }, 'Saving a new key…')),
      h('label', { class: 'toggle-row' }, h('span', { class: 'lbl' }, h('b', null, 'Use TMDB')), h('span', { class: 'switch' }, h('input', { type: 'checkbox' })))),
    h('div', { class: 'form-section' }, h('h2', null, 'Seek previews')));
  let went = null;
  const box = searchBox({ isAdmin: true, choose: (e) => { went = `#/settings/${e.page}`; reveal(page, e); } });
  const { input } = box;
  const list = box.el.querySelector('ul');
  const live = box.el.children.find((c) => c.getAttribute('role') === 'status');

  assert.equal(input.getAttribute('role'), 'combobox');
  assert.equal(input.getAttribute('aria-controls'), list.id);
  input.value = 'tmdb';
  input.dispatch('input');
  assert.equal(input.getAttribute('aria-expanded'), 'true');
  assert.equal(list.getAttribute('role'), 'listbox');
  assert.match(list.children[0].textContent, /^TMDB API key or read access token.*Metadata › Providers$/);
  assert.equal(input.getAttribute('aria-activedescendant'), list.children[0].id);
  assert.match(live.textContent, /^\d+ settings? found$/);
  // Arrow keys move through the results and wrap.
  input.dispatch('keydown', { key: 'ArrowDown' });
  assert.equal(input.getAttribute('aria-activedescendant'), list.children[1].id);
  input.dispatch('keydown', { key: 'ArrowUp' });
  input.dispatch('keydown', { key: 'ArrowUp' });
  assert.equal(input.getAttribute('aria-activedescendant'), list.children.at(-1).id);
  input.dispatch('keydown', { key: 'ArrowDown' });

  input.dispatch('keydown', { key: 'Enter' });
  assert.equal(went, '#/settings/metadata');
  assert.equal(document.activeElement, keyInput, 'the TMDB key field is focused');
  const row = keyInput.parentNode;
  assert.ok(row.classList.contains('setting-flash'), 'and highlighted');
  assert.equal(row.scrolled.block, 'center');
  assert.equal(input.value, '', 'the box clears');
  assert.equal(input.getAttribute('aria-expanded'), 'false');

  // A section result focuses its heading; a toggle result its switch.
  input.value = 'trickplay';
  input.dispatch('input');
  const i = [...list.children].findIndex((li) => /^Seek previewsPreviews & intros$/.test(li.textContent));
  list.children[i].dispatch('click');
  assert.equal(document.activeElement.tagName, 'H2');
  assert.equal(document.activeElement.textContent, 'Seek previews');
  assert.equal(reveal(page, { page: 'metadata', section: 'Providers', label: 'Use TMDB' }).type, 'checkbox');
});

test('no results says so, and Esc clears the box', async () => {
  const { searchBox } = await load();
  const { el, input } = searchBox({ isAdmin: false, choose: () => assert.fail('nothing to choose') });
  input.value = 'qqqq';
  input.dispatch('input');
  const empty = el.querySelector('p');
  assert.equal(empty.hidden, false);
  assert.equal(empty.textContent, 'No settings match ‘qqqq’');
  assert.equal(input.getAttribute('aria-expanded'), 'false');
  input.dispatch('keydown', { key: 'Enter' });
  const esc = input.dispatch('keydown', { key: 'Escape' });
  assert.ok(esc.defaultPrevented);
  assert.equal(input.value, '');
  assert.equal(el.querySelector('div').hidden, true);
});
