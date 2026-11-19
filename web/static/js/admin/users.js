// Users and devices: accounts, library access, password resets, and every signed-in device.

import { h, modal, confirmDialog, fmtDate, fmtDateShort, timeAgo, run } from '../ui.js';
import { api } from '../api.js';
import { state, refreshSoft, setLeaveGuard, focusAfterRoute } from '../app.js';
import { PW_RULE, PW_MSG, pwLength } from './account.js';
import { field, toggleRow, formActions, formErrors, submitInline, actionsTh } from './forms.js';

/** @typedef {import('../../../types/api.gen').User} User */
/** @typedef {import('../../../types/api.gen').Library} Library */

const fmtDay = (ts) => (ts ? new Date(ts * 1000).toLocaleDateString(undefined, { dateStyle: 'medium' }) : '');

// What a user may watch, in a few words.
/** @param {User} u @param {Library[]} libs */
function accessLabel(u, libs) {
  if (u.isAdmin || u.allLibraries) return 'All libraries';
  const names = libs.filter((l) => (u.libraries || []).includes(l.id)).map((l) => l.name);
  return names.length ? names.join(', ') : 'None';
}

export async function usersSection() {
  // For an admin, /api/libraries lists every library.
  const [users, libs] = await Promise.all([api('/api/admin/users'), api('/api/libraries')]);
  const tbl = h('table', { class: 'tbl stack' }, h('tr', null, h('th', null, 'Name'), h('th', null, 'Role'), h('th', null, 'Libraries'), h('th', null, 'Created'), actionsTh()),
    users.map((u) => h('tr', null, h('td', null, h('b', null, u.name, u.id === state.me.id ? [' ', h('span', { class: 'dim' }, '(you)')] : null)), h('td', null, u.isAdmin ? 'Admin' : 'User'),
      h('td', { 'data-label': 'Libraries', class: u.isAdmin || u.allLibraries ? 'muted' : null }, accessLabel(u, libs)),
      h('td', { class: 'nowrap', 'data-label': 'Created' }, fmtDay(u.createdAt)),
      h('td', null, h('div', { class: 'row wrap tbl-actions' },
        // Admins always see every library.
        u.isAdmin ? null : h('button', { class: 'btn sm', 'aria-label': `Library access for ${u.name}`, dataset: { focusKey: `user-libs-${u.id}` }, onclick: () => libraryAccessModal(u, libs) }, 'Libraries'),
        // Resetting your own password here would sign you out everywhere.
        u.id === state.me.id
          ? h('a', { class: 'btn sm', href: '#/settings/account', dataset: { focusKey: 'settings-nav-account' }, onclick: () => focusAfterRoute('settings-nav-account') }, 'Change password')
          : h('button', { class: 'btn sm', 'aria-label': `Reset password for ${u.name}`, onclick: () => resetPasswordModal(u) }, 'Reset password'),
        // Your own role can only be changed by another admin.
        u.id !== state.me.id ? h('button', { class: 'btn sm', dataset: { focusKey: `user-admin-${u.id}` }, onclick: async (e) => {
          const btn = e.currentTarget;
          const ok = u.isAdmin
            ? await confirmDialog(`Make ${u.name} a regular user? They will no longer be able to change settings, libraries or users.`, 'Make user', false, 'Make user?')
            : await confirmDialog(`Make ${u.name} an administrator? They can change settings, libraries and users.`, 'Make admin', false, 'Make admin?');
          if (ok && await run(btn, () => api(`/api/admin/users/${u.id}`, { method: 'PUT', body: { isAdmin: !u.isAdmin } }), u.isAdmin ? `Made ${u.name} a regular user` : `Made ${u.name} an admin`)) refreshSoft();
        } }, u.isAdmin ? 'Make user' : 'Make admin') : null,
        u.id !== state.me.id ? h('button', { class: 'btn sm danger', 'aria-label': `Delete ${u.name}`, dataset: { focusKey: `user-del-${u.id}` }, onclick: async (e) => {
          const btn = e.currentTarget;
          if (!(await confirmDialog(`Delete ${u.name}? Their watch history, favorites and signed-in devices are removed. This can’t be undone.`, 'Delete user', true, 'Delete user?'))) return;
          if (!(await run(btn, () => api(`/api/admin/users/${u.id}`, { method: 'DELETE' }), `Deleted ${u.name}`))) return;
          // The row goes away: focus the next row's Delete (or the Add user form).
          const others = users.filter((x) => x.id !== state.me.id), i = others.indexOf(u), next = others[i + 1] || others[i - 1];
          focusAfterRoute(next ? `user-del-${next.id}` : 'user-new-name');
          refreshSoft();
        } }, 'Delete') : null)))));
  return h('div', { class: 'form' },
    h('div', { class: 'form-section' }, h('h2', null, 'Users'), h('div', { class: 'table-wrap' }, tbl)),
    addUserForm());
}

function addUserForm() {
  const name = h('input', { type: 'text', name: 'new-user-name', dataset: { focusKey: 'user-new-name' }, autocomplete: 'off', autocapitalize: 'none', autocorrect: 'off', spellcheck: 'false', required: true, maxlength: 64 });
  const pw = h('input', { type: 'password', name: 'new-user-password', dataset: { focusKey: 'user-new-password' }, autocomplete: 'new-password', required: true, ...PW_RULE });
  const errors = formErrors('add-user-err', [name, pw]);
  const btn = h('button', { class: 'btn primary', type: 'submit', dataset: { focusKey: 'user-create' } }, 'Create user');
  let admin = false;
  const onsubmit = async (e) => {
    e.preventDefault();
    errors.reset();
    if (!name.value.trim()) return errors.fail('Enter a username', name);
    if (!pwLength(pw.value)) return errors.fail(PW_MSG, pw);
    const nm = name.value.trim();
    if (await submitInline(btn, errors, () => api('/api/admin/users', { method: 'POST', body: { name: nm, password: pw.value, isAdmin: admin } }), `Created ${nm}`,
      (m) => (/password/i.test(m) ? [pw] : [name]))) refreshSoft();
  };
  return h('form', { class: 'form-section', novalidate: true, onsubmit }, h('h2', null, 'Add user'),
    h('div', { class: 'form-grid' }, field('Username', name), field('Password', pw, 'Use 12–72 bytes.')),
    toggleRow('Administrator', 'Can change settings, libraries and see stats.', false, (v) => { admin = v; }),
    formActions(btn, errors.err));
}

// Which libraries a regular user may see and play: all of them (including
// ones added later), or the ones ticked. The server hides everything else.
/** @param {User} u @param {Library[]} libs */
function libraryAccessModal(u, libs) {
  const formId = `user-libs-form-${u.id}`;
  let all = u.allLibraries !== false;
  const picked = new Set(u.libraries || []);
  const errors = formErrors(`${formId}-err`, []);
  const groupId = `${formId}-l`;
  const boxes = libs.map((l) => h('input', { type: 'checkbox', checked: picked.has(l.id), disabled: all,
    onchange: (e) => { if (e.target.checked) picked.add(l.id); else picked.delete(l.id); } }));
  const group = h('div', { class: 'field' }, h('span', { id: groupId }, 'Only these libraries'),
    libs.length
      ? h('div', { class: 'check-chips', role: 'group', 'aria-labelledby': groupId }, libs.map((l, i) => h('label', null, boxes[i], l.name)))
      : h('p', { class: 'muted small', style: { margin: 0 } }, 'No libraries yet.'));
  const btn = h('button', { class: 'btn primary', type: 'submit', form: formId }, 'Save');
  const onsubmit = async (e) => {
    e.preventDefault();
    errors.reset();
    if (!all && !picked.size && !(await confirmDialog(`With no library ticked, ${u.name} can’t watch anything.`, 'Save anyway', false, 'No libraries?'))) return;
    const body = { allLibraries: all, libraries: all ? [] : [...picked] };
    if (await submitInline(btn, errors, () => api(`/api/admin/users/${u.id}`, { method: 'PUT', body }), `Saved library access for ${u.name}`)) {
      m.close();
      focusAfterRoute(`user-libs-${u.id}`);
      refreshSoft();
    }
  };
  const m = modal({ title: `Library access for ${u.name}`, body: h('form', { id: formId, novalidate: true, onsubmit, style: { display: 'grid', gap: '12px' } },
    toggleRow('All libraries', 'Includes libraries added later.', all, (v) => { all = v; boxes.forEach((b) => { b.disabled = v; }); }),
    group,
    h('p', { class: 'muted small', style: { margin: 0 } }, `${u.name} won’t see, search or play anything in other libraries, or Live TV channels that air them.`),
    errors.err),
  actions: [h('button', { class: 'btn', type: 'button', onclick: () => m.close() }, 'Cancel'), btn] });
}

// An admin sets a new password for someone else; it signs them out everywhere.
/** @param {User} u */
function resetPasswordModal(u) {
  const formId = `reset-pw-${u.id}`;
  const pw = h('input', { type: 'password', name: 'new-password', autocomplete: 'new-password', required: true, ...PW_RULE });
  const errors = formErrors(`${formId}-err`, [pw]);
  const show = h('label', { class: 'row small muted', style: { gap: '8px', cursor: 'pointer' } },
    h('input', { type: 'checkbox', onchange: (e) => { pw.type = e.target.checked ? 'text' : 'password'; } }), 'Show password');
  const btn = h('button', { class: 'btn primary', type: 'submit', form: formId }, 'Reset password');
  const onsubmit = async (e) => {
    e.preventDefault();
    errors.reset();
    if (!pwLength(pw.value)) return errors.fail(PW_MSG, pw);
    if (await submitInline(btn, errors, () => api(`/api/admin/users/${u.id}`, { method: 'PUT', body: { password: pw.value } }), `Password reset for ${u.name}`, () => [pw])) m.close();
  };
  const m = modal({ title: `Reset password for ${u.name}`, body: h('form', { id: formId, novalidate: true, onsubmit, style: { display: 'grid', gap: '12px' } },
    field('New password', pw, `Use 12–72 bytes. ${u.name} will be signed out on all devices.`), show, errors.err),
  actions: [h('button', { class: 'btn', type: 'button', onclick: () => m.close() }, 'Cancel'), btn] });
  pw.focus();
}

export async function devicesSection() {
  const devs = await api('/api/admin/devices');
  const signOut = async (e, d) => {
    const btn = e.currentTarget;
    if (d.current) {
      // Same as the account menu's Sign out.
      if (await run(btn, () => api('/api/auth/logout', { method: 'POST' }))) { setLeaveGuard(null); location.hash = ''; location.reload(); }
      return;
    }
    if (!(await confirmDialog(`Sign out ${d.userName} on ${d.client} (${d.ip})? They’ll need to sign in again.`, 'Sign out', true, 'Sign out device?'))) return;
    if (!(await run(btn, () => api(`/api/admin/devices/${d.prefix}`, { method: 'DELETE' }), `Signed out ${d.userName} on ${d.client}`))) return;
    // The row goes away: keep focus in the list, on the next row's button.
    const i = devs.indexOf(d), next = devs[i + 1] || devs[i - 1];
    if (next) focusAfterRoute(`device-${next.prefix}`);
    refreshSoft();
  };
  return h('div', { class: 'form' }, h('div', { class: 'form-section' }, h('h2', null, 'Signed-in devices'),
    h('div', { class: 'table-wrap' }, h('table', { class: 'tbl stack' }, h('tr', null, h('th', null, 'User'), h('th', null, 'Client'), h('th', null, 'IP'), h('th', null, 'Signed in'), h('th', null, 'Last seen'), actionsTh()),
      devs.map((d) => h('tr', null, h('td', null, h('b', null, d.userName)),
        h('td', { class: 'nowrap' }, d.client, d.current ? [' ', h('span', { class: 'chip this-device' }, 'This device')] : null),
        h('td', { class: 'mono' }, d.ip), h('td', { class: 'nowrap', 'data-label': 'Signed in', title: fmtDate(d.created) }, fmtDateShort(d.created)), h('td', { class: 'nowrap', 'data-label': 'Last seen' }, timeAgo(d.lastSeen)),
        h('td', null, h('div', { class: 'row wrap tbl-actions' }, h('button', { class: 'btn sm danger', dataset: { focusKey: `device-${d.prefix}` },
          'aria-label': d.current ? 'Sign out here (this device)' : `Sign out ${d.userName} on ${d.client} (${d.ip})`, onclick: (e) => signOut(e, d) }, d.current ? 'Sign out here' : 'Sign out')))))))));
}
