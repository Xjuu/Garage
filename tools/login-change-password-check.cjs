/* Boots the real login.js in a vm context with a fake DOM and drives the
 * forced-password-change stage a brand new account (like Klon's) lands on:
 * a correct password whose account still carries a temporary one shows the
 * change-password panel, not 2FA; submitting a new password posts to
 * /api/login/change-password and, on success, moves on to whatever stage
 * the server says is next (setup, here, since this account has no 2FA yet
 * either); a rejected change shows its error and does not advance; and a
 * page reload mid-flow (session lost, pending cookie still good) resumes on
 * the change-password panel via /api/session, not back at the password form.
 *
 * Usage: node tools/login-change-password-check.cjs
 * Exits non-zero if any check fails or login.js throws while loading.
 */

'use strict';

const fs = require('fs');
const vm = require('vm');
const path = require('path');

const ASSETS = path.join(__dirname, '..', 'internal', 'web', 'assets');

const html = fs.readFileSync(path.join(ASSETS, 'login.html'), 'utf8');
const ids = [...html.matchAll(/id="([^"]+)"/g)].map((m) => m[1]);
const hiddenIds = new Set([...html.matchAll(/id="([^"]+)"[^>]*\shidden/g)].map((m) => m[1]));

function makeEl(id) {
  const listeners = {};
  return {
    id, textContent: '', innerHTML: '', value: '', hidden: hiddenIds.has(id),
    disabled: false, dataset: {}, style: {},
    classList: { add() {}, remove() {}, toggle() {}, contains: () => false },
    addEventListener(ev, fn) { (listeners[ev] ??= []).push(fn); },
    fire(ev, arg) { (listeners[ev] || []).forEach((fn) => fn(arg || { preventDefault() {} })); },
    click() { this.fire('click'); },
    focus() {}, select() {},
  };
}

const store = {};
ids.forEach((i) => { store[i] = makeEl(i); });

let sessionResponse = { authenticated: false };
const fetchCalls = [];
async function fakeFetch(url, opts = {}) {
  fetchCalls.push({ url, opts, body: opts.body ? JSON.parse(opts.body) : undefined });
  if (url === '/api/session') {
    return { ok: true, status: 200, json: async () => sessionResponse };
  }
  if (url === '/api/login') {
    return { ok: true, status: 200, json: async () => ({ ok: false, stage: 'change_password' }) };
  }
  if (url === '/api/login/change-password') {
    const body = JSON.parse(opts.body);
    if (body.password === 'reused-temp-password') {
      return { ok: false, status: 400, json: async () => ({ error: 'that is still the temporary password' }) };
    }
    return { ok: true, status: 200, json: async () => ({ ok: false, stage: 'setup' }) };
  }
  if (url === '/api/login/totp/setup') {
    return { ok: true, status: 200, json: async () => ({ secret: 'ABCD', qr_png: 'data:image/png;base64,x', account: 'klon' }) };
  }
  return { ok: true, status: 200, json: async () => ({}) };
}

const errors = [];
const ctx = vm.createContext({
  console,
  document: {
    getElementById: (id) => store[id] || null,
    addEventListener() {},
  },
  location: { href: '' },
  setTimeout, clearTimeout,
  fetch: fakeFetch,
  Math, JSON, Object, Array, Number, String, Boolean, Date, Set, Map, Promise,
});
ctx.window = ctx; ctx.globalThis = ctx;

try {
  vm.runInContext(fs.readFileSync(path.join(ASSETS, 'login.js'), 'utf8'), ctx, { filename: 'login.js' });
} catch (e) {
  errors.push('threw while loading: ' + e.message);
}

const wait = (ms) => new Promise((r) => setTimeout(r, ms));

let failed = false;
function ok(cond, label) {
  console.log((cond ? 'ok  ' : 'FAIL') + ' - ' + label);
  if (!cond) failed = true;
}

(async () => {
  ok(errors.length === 0, 'login.js loads without throwing: ' + errors.join('; '));

  // Submitting the password form for an account whose stage comes back
  // "change_password" must show that panel, not jump to 2FA.
  store['user'].value = 'klon';
  store['password'].value = '1234567890';
  await store['form'].fire('submit');
  await wait(10);
  ok(store['change-password'].hidden === false, 'the change-password panel is shown for stage="change_password"');
  ok(store['totp-setup'].hidden === true && store['totp-verify'].hidden === true,
    '2FA panels stay hidden until the password is actually changed');

  // A rejected new password (e.g. still the temporary one) shows its error
  // and must not advance past this panel.
  store['new-password'].value = 'reused-temp-password';
  await store['change-password-form'].fire('submit');
  await wait(10);
  ok(store['change-password-err'].textContent.includes('temporary'),
    'a rejected password change shows the server\'s error: ' + JSON.stringify(store['change-password-err'].textContent));
  ok(store['change-password'].hidden === false, 'still on the change-password panel after a rejected attempt');

  // A genuinely new password succeeds and moves on to whatever the server
  // says is next — "setup" here, so the QR panel should load.
  fetchCalls.length = 0;
  store['new-password'].value = 'a genuinely new password';
  await store['change-password-form'].fire('submit');
  await wait(10);
  const sentPassword = fetchCalls.find((c) => c.url === '/api/login/change-password')?.body?.password;
  ok(sentPassword === 'a genuinely new password', 'the new password is posted to /api/login/change-password');
  ok(store['totp-setup'].hidden === false, 'a successful change moves straight to 2FA setup (stage="setup")');
  ok(fetchCalls.some((c) => c.url === '/api/login/totp/setup'), 'the setup QR is fetched once that panel shows');

  // A reload mid-flow (no session yet, but the pending cookie is still
  // good) must resume on the change-password panel, not the password form.
  const store2 = {};
  ids.forEach((i) => { store2[i] = makeEl(i); });
  sessionResponse = { authenticated: false, totp_pending: true, totp_stage: 'change_password' };
  const ctx2 = vm.createContext({
    console,
    document: { getElementById: (id) => store2[id] || null, addEventListener() {} },
    location: { href: '' },
    setTimeout, clearTimeout,
    fetch: (url, opts) => fakeFetch(url, opts),
    Math, JSON, Object, Array, Number, String, Boolean, Date, Set, Map, Promise,
  });
  ctx2.window = ctx2; ctx2.globalThis = ctx2;
  const oldStore = Object.assign({}, store);
  Object.keys(store).forEach((k) => delete store[k]);
  Object.assign(store, store2);
  try {
    vm.runInContext(fs.readFileSync(path.join(ASSETS, 'login.js'), 'utf8'), ctx2, { filename: 'login.js (reload)' });
  } catch (e) {
    errors.push('reload: threw while loading: ' + e.message);
  }
  await wait(10);
  ok(store2['change-password'].hidden === false,
    'reloading mid-flow resumes on the change-password panel via /api/session');
  Object.keys(store).forEach((k) => delete store[k]);
  Object.assign(store, oldStore);

  process.exit(failed || errors.length ? 1 : 0);
})();
