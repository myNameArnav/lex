// Account settings: changing your password, and your signed-in devices.

import { h, confirmDialog, fmtDate, fmtDateShort, timeAgo, run } from '../ui.js';
import { api } from '../api.js';
import { state, refreshSoft, setLeaveGuard, focusAfterRoute } from '../app.js';
import { field, formActions, formErrors, submitInline, actionsTh } from './forms.js';

export const PW_RULE = { minlength: 12, maxlength: 72 };

export const PW_MSG = 'Use 12–72 bytes (at least 12 characters).';

export const pwLength = (v) => { const n = new TextEncoder().encode(v).length; return n >= 12 && n <= 72; };

export async function accountSection() {
  const devs = await api('/api/me/devices');
  const cur = h('input', { type: 'password', name: 'current-password', autocomplete: 'current-password', required: true });
  const n1 = h('input', { type: 'password', name: 'new-password', autocomplete: 'new-password', required: true, ...PW_RULE });
  const n2 = h('input', { type: 'password', name: 'confirm-password', autocomplete: 'new-password', required: true, ...PW_RULE });
  const errors = formErrors('account-pw-err', [cur, n1, n2]);
  const btn = h('button', { class: 'btn primary', type: 'submit' }, 'Change password');
  // Wrong or empty tries count toward the sign-in limit, so nothing is sent
  // until the form is complete.
  const onsubmit = async (e) => {
    e.preventDefault();
    errors.reset();
    const empty = [cur, n1, n2].filter((i) => !i.value);
    if (empty.length) return errors.fail(empty[0] === cur ? 'Enter your current password' : 'Enter the new password twice', ...empty);
    if (!pwLength(n1.value)) return errors.fail(PW_MSG, n1);
    if (n1.value !== n2.value) return errors.fail('The new passwords don’t match', n2);
    if (await submitInline(btn, errors, () => api('/api/me/password', { method: 'PUT', body: { current: cur.value, new: n1.value } }), 'Password changed; other devices were signed out',
      (m) => (/current/i.test(m) ? [cur] : /password must/i.test(m) ? [n1] : []))) cur.value = n1.value = n2.value = '';
  };
  return h('div', { class: 'form' }, h('form', { class: 'form-section', dataset: { searchSection: 'Password' }, novalidate: true, onsubmit },
    h('h2', null, `Signed in as ${state.me.name}`),
    h('p', { class: 'muted', style: { margin: 0 } }, state.me.isAdmin ? 'Administrator' : 'User'),
    // Lets password managers file the new password under this account.
    h('input', { type: 'text', name: 'username', autocomplete: 'username', value: state.me.name, hidden: true, readonly: true, tabindex: -1 }),
    field('Current password', cur), field('New password', n1, 'Use 12–72 bytes.'), field('Confirm new password', n2),
    formActions(btn, errors.err)), myDevices(devs));
}

// "Your devices" on the Account page: the user's own sessions, each with Sign
// out, plus Sign out everywhere else. Admins also see everyone's on Devices.
export function myDevices(devs) {
  const signOut = async (e, d) => {
    const btn = e.currentTarget;
    if (d.current) {
      if (await run(btn, () => api('/api/auth/logout', { method: 'POST' }))) { setLeaveGuard(null); location.hash = ''; location.reload(); }
      return;
    }
    if (!(await confirmDialog(`Sign out ${d.client} (${d.ip})? It will need to sign in again.`, 'Sign out', true, 'Sign out device?'))) return;
    if (!(await run(btn, () => api(`/api/me/devices/${d.prefix}`, { method: 'DELETE' }), `Signed out ${d.client}`))) return;
    const i = devs.indexOf(d), next = devs[i + 1] || devs[i - 1];
    if (next) focusAfterRoute(`my-device-${next.prefix}`);
    refreshSoft();
  };
  const others = devs.filter((d) => !d.current).length;
  const signOutOthers = async (e) => {
    if (!(await confirmDialog(`Sign out ${others === 1 ? 'your other device' : `your ${others} other devices`}? You stay signed in here.`, 'Sign out', true, 'Sign out everywhere else?'))) return;
    if (await run(e.currentTarget, () => api('/api/me/devices', { method: 'DELETE' }), 'Signed out everywhere else')) refreshSoft();
  };
  return h('div', { class: 'form-section' }, h('h2', null, 'Your devices'),
    h('p', { class: 'muted', style: { margin: 0 } }, 'Where you’re signed in. Sign out a lost or shared device here; changing your password also signs out every other device.'),
    h('div', { class: 'table-wrap' }, h('table', { class: 'tbl stack' }, h('tr', null, h('th', null, 'Client'), h('th', null, 'IP'), h('th', null, 'Signed in'), h('th', null, 'Last seen'), actionsTh()),
      devs.map((d) => h('tr', null,
        h('td', { class: 'nowrap' }, h('b', null, d.client), d.current ? [' ', h('span', { class: 'chip this-device' }, 'This device')] : null),
        h('td', { class: 'mono' }, d.ip), h('td', { class: 'nowrap', 'data-label': 'Signed in', title: fmtDate(d.created) }, fmtDateShort(d.created)), h('td', { class: 'nowrap', 'data-label': 'Last seen' }, timeAgo(d.lastSeen)),
        h('td', null, h('div', { class: 'row wrap tbl-actions' }, h('button', { class: 'btn sm danger', dataset: { focusKey: `my-device-${d.prefix}` },
          'aria-label': d.current ? 'Sign out here (this device)' : `Sign out ${d.client} (${d.ip})`, onclick: (e) => signOut(e, d) }, d.current ? 'Sign out here' : 'Sign out'))))))),
    others ? h('div', { class: 'row wrap' }, h('button', { class: 'btn danger', type: 'button', onclick: signOutOthers }, 'Sign out everywhere else')) : null);
}
