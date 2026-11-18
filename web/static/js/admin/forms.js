// Building blocks for settings pages: headings with help, labelled fields,
// toggles and selects, form errors and actions, and configForm (a form that
// edits part of the server config).

import { h, icons, toast, toggle, $, run } from '../ui.js';
import { api } from '../api.js';
import { state, setLeaveGuard, renderNav } from '../app.js';

// A setting's description sits behind an ⓘ button beside its name, so a
// page reads as a list of settings rather than paragraphs. The text stays in
// the page (hidden until opened), so it is still the control's description
// for screen readers and still found by the browser's find-in-page.
export let infoId = 0;

export function infoPair(name, help, tag = 'div') {
  const text = h(tag, { class: 'help info-text', id: `info-${++infoId}`, hidden: true }, help);
  const btn = h('button', { type: 'button', class: 'info-btn', title: 'More about this', 'aria-label': `About ${name}`, 'aria-expanded': 'false', 'aria-controls': text.id, html: icons.info,
    onclick: (e) => {
      e.preventDefault(); // not the label's click: it would toggle or focus the control
      text.hidden = !text.hidden;
      btn.setAttribute('aria-expanded', String(!text.hidden));
      btn.classList.toggle('on', !text.hidden);
    } });
  return [btn, text];
}

export const nameOf = (label) => (typeof label === 'string' ? label : label?.textContent || 'this setting');

// heading is a section's title with its description behind an ⓘ button.
export function heading(title, ...help) {
  const [btn, text] = infoPair(title, help, 'p');
  return [h('h2', { class: 'with-info' }, title, btn), text];
}

// field wraps a control with its label and help. The label alone names the
// control; the help describes it (so it isn't read as part of the name).
// Text help goes behind an ⓘ button; help that is already an element (a
// status or a warning) stays in view.
export let fieldId = 0;

export function field(label, input, help) {
  const id = `field-${++fieldId}`;
  let btn = null, helpEl = null;
  if (help instanceof Node) helpEl = help;
  else if (help != null && help !== '') [btn, helpEl] = infoPair(nameOf(label), help);
  if (helpEl) helpEl.id ||= `${id}-h`;
  const isControl = input.matches?.('input, select, textarea');
  if (isControl) {
    // aria-label for plain text: Chrome would read the label's CSS uppercase into the name.
    if (typeof label === 'string') input.setAttribute('aria-label', label);
    else input.setAttribute('aria-labelledby', `${id}-l`);
    if (helpEl) input.setAttribute('aria-describedby', [helpEl.id, input.getAttribute('aria-describedby')].filter(Boolean).join(' '));
    input.id ||= `${id}-c`;
  }
  // for= keeps the ⓘ button (the first button in the label) from becoming
  // the control the label clicks.
  return h('label', { class: 'field', for: btn ? (isControl ? input.id : `${id}-none`) : null }, h('span', { id: `${id}-l` }, label, btn), input, helpEl);
}

export let toggleId = 0;

export function toggleRow(label, help, checked, onchange, { disabled = false } = {}) {
  const control = toggle(checked, onchange, label);
  const input = control.firstChild;
  input.id = `toggle-${++toggleId}`;
  input.disabled = disabled;
  let btn = null, helpEl = null;
  if (help instanceof Node) helpEl = help;
  else if (help) [btn, helpEl] = infoPair(label, help, 'span');
  if (helpEl) {
    helpEl.id ||= `${input.id}-h`;
    helpEl.classList.add('help');
    input.setAttribute('aria-describedby', helpEl.id);
  }
  return h('label', { class: 'toggle-row', for: input.id }, h('span', { class: 'lbl' }, h('span', { class: 'lbl-name' }, h('b', null, label), btn), helpEl), control);
}

export function select(value, options, onchange) {
  return h('select', { onchange: (e) => onchange(e.target.value) }, options.map(([v, l]) => h('option', { value: String(v), selected: String(v) === String(value) }, l)));
}

// formActions puts a form's submit button and its error line side by side,
// so an error appearing (or clearing) doesn't push the button around.
export const formActions = (btn, err) => h('div', { class: 'form-actions' }, btn, err);

// formErrors gives a form an inline error line (role=alert) and helpers to
// flag fields: fail(msg, ...inputs) marks them invalid and focuses the first.
export function formErrors(id, inputs) {
  const err = h('div', { class: 'err', role: 'alert', id });
  for (const i of inputs) {
    i.setAttribute('aria-describedby', [i.getAttribute('aria-describedby'), id].filter(Boolean).join(' '));
    i.addEventListener('input', () => i.removeAttribute('aria-invalid'));
  }
  const reset = () => { err.textContent = ''; inputs.forEach((i) => i.removeAttribute('aria-invalid')); };
  const fail = (msg, ...bad) => {
    err.textContent = msg;
    bad.forEach((i) => i.setAttribute('aria-invalid', 'true'));
    bad[0]?.focus();
  };
  return { err, reset, fail };
}

// Runs a form's request through run() (busy button, no double submits), with
// the server's error shown inline instead of as a toast; pick(message) names
// the inputs to flag. Resolves true on success.
export async function submitInline(btn, errors, fn, okMsg, pick = () => []) {
  let failed = null;
  const ok = await run(btn, async () => {
    try { return await fn(); } catch (ex) { failed = ex; throw Object.assign(new Error(ex.message), { name: 'AbortError' }); }
  }, okMsg);
  if (failed) errors.fail(upper(failed.message), ...pick(failed.message));
  return ok;
}

// configForm edits /api/admin/config through a draft: Save (or Enter in a
// field) sends it, Save stays disabled until something differs from the
// saved config, and leaving the page with unsaved edits asks first.
// check(draft) may return [message, control] to stop a save with an inline error.
export async function configForm(build, { check, onSaved } = {}) {
  let cfg = await api('/api/admin/config');
  const info = await api('/api/admin/info');
  const draft = { ...cfg };
  let clean = JSON.stringify(cfg);
  const dirty = () => JSON.stringify(draft) !== clean;
  const bind = (k, conv = (x) => x) => (v) => { draft[k] = conv(v); };
  // Number fields: an empty (or unreadable) field keeps the saved value, and
  // shows it again on leaving the field. Save checks the ranges first.
  const controls = new Map();
  const num = (k, attrs = {}) => {
    const input = h('input', { type: 'number', inputmode: 'numeric', value: draft[k], ...attrs,
      oninput: (e) => { const v = e.target.valueAsNumber; draft[k] = Number.isNaN(v) ? cfg[k] : v; input.setCustomValidity(''); },
      onblur: () => { if (input.value === '' && !input.validity.badInput) { input.value = draft[k]; mark(); } } });
    controls.set(k, input);
    return input;
  };
  const text = (k, attrs = {}) => h('input', { type: 'text', value: draft[k] ?? '', ...attrs, oninput: (e) => { draft[k] = e.target.value; } });
  // A comma-separated list shown in a box that wraps; a new line counts as a comma.
  const list = (k, attrs = {}) => h('textarea', { rows: 2, value: draft[k] ?? '', autocapitalize: 'off', autocorrect: 'off', spellcheck: 'false', ...attrs,
    oninput: (e) => { draft[k] = e.target.value.split(/[,\n]/).map((x) => x.trim()).filter(Boolean).join(', '); } });
  const tg = (k, label, help, opts) => toggleRow(label, help, !!draft[k], bind(k), opts);
  const note = h('span', { class: 'muted small', hidden: true }, 'Unsaved changes');
  const err = h('span', { class: 'err', role: 'alert' });
  const save = h('button', { class: 'btn primary', type: 'submit', disabled: true }, 'Save changes');
  // The leave guard is registered on the first edit, not while building: a
  // form whose route was superseded mid-load would replace the shown form's guard.
  const guard = () => form.isConnected && dirty();
  const mark = () => {
    const d = dirty();
    if (!save.hasAttribute('aria-busy')) save.disabled = !d;
    note.hidden = !d;
    err.textContent = '';
    if (d) setLeaveGuard(guard);
  };
  // Step mismatches are allowed on purpose (the server doesn't enforce
  // steps, and stored values may be off-step), but every setting is a whole number.
  const invalid = () => [...controls.values()].find((i) => {
    i.setCustomValidity(i.value !== '' && !Number.isInteger(i.valueAsNumber) && !i.validity.badInput ? 'Enter a whole number.' : '');
    return i.validity.rangeOverflow || i.validity.rangeUnderflow || i.validity.badInput || i.validity.customError;
  });
  const labelOf = (i) => i.getAttribute('aria-label') || '';
  const onSubmit = async (e) => {
    e.preventDefault();
    const bad = invalid();
    if (bad) { bad.focus(); bad.reportValidity(); return; }
    if (!dirty()) return;
    const [msg, ctl] = check?.(draft) || [];
    if (msg) { note.hidden = true; err.textContent = msg; ctl?.focus(); return; }
    let reset = [];
    const ok = await run(save, async () => {
      const sent = { ...draft };
      cfg = await api('/api/admin/config', { method: 'PUT', body: sent });
      clean = JSON.stringify(Object.assign({ ...draft }, cfg));
      // Fields edited while the save was in flight keep the new edit (and
      // stay unsaved); the rest take what the server stored.
      for (const k of Object.keys(cfg)) if (draft[k] === sent[k]) draft[k] = cfg[k];
      // The server puts values it can't use back to defaults: show what it kept.
      for (const [k, input] of controls) {
        if (sent[k] !== cfg[k]) reset.push(labelOf(input) || k);
        if (draft[k] === cfg[k]) input.value = cfg[k];
      }
      // The server decides what's usable (see caps in auth.go).
      try { state.caps = (await api('/api/me')).caps || state.caps; } catch {}
      renderNav();
      if (cfg.serverName && cfg.serverName !== state.serverName) {
        state.serverName = cfg.serverName;
        const logo = $('.topbar .logo');
        if (logo) { logo.querySelector('span').textContent = cfg.serverName; logo.setAttribute('aria-label', `${cfg.serverName} home`); }
        document.title = `Settings · ${cfg.serverName}`;
      }
    }, () => (reset.length ? null : 'Settings saved'), { busyLabel: 'Saving…' });
    if (!ok) return;
    mark();
    onSaved?.(cfg);
    if (reset.length) toast(`Saved, but some values were out of range and were reset: ${reset.join(', ')}`, 'error');
  };
  // novalidate: Save checks the number fields itself (see invalid()).
  const parts = build({ draft, num, text, list, tg, bind, info });
  const form = h('form', { class: 'form', novalidate: true, onsubmit: onSubmit, oninput: mark, onchange: mark },
    parts, h('div', { class: 'save-bar' }, save, note, err));
  form.dirty = dirty;
  return form;
}

export const upper = (s) => s.charAt(0).toUpperCase() + s.slice(1);

// Both tables become stacked rows on narrow screens (.tbl.stack); data-label
// names the cells whose header is then hidden.
export const actionsTh = () => h('th', null, h('span', { class: 'sr-only' }, 'Actions'));

// keepFocus re-renders box with rebuild() and, if focus was inside it, puts
// it back on the control with the same data-focus-key; when that one is gone,
// on the one keyed next (if given) or the box's first keyed control.
export function keepFocus(box, rebuild, next = null) {
  const k = box.contains(document.activeElement) ? document.activeElement.dataset.focusKey : null;
  rebuild();
  if (k === undefined || k === null) return;
  const keyed = [...box.querySelectorAll('[data-focus-key]')];
  (keyed.find((el) => el.dataset.focusKey === k) || keyed.find((el) => el.dataset.focusKey === next) || keyed[0])?.focus({ preventScroll: true });
}
