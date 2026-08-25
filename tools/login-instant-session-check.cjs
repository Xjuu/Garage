/* Boots the real login.js in a vm context with a fake DOM and drives the
 * TOTP-exempt "shared temporary account" flow: a correct password whose
 * account has nothing else pending (server returns {ok:true}, no "stage")
 * must redirect straight to "/" without ever showing any 2FA or
 * change-password panel — and the same for the change-password panel's own
 * submit, for the rarer case of an exempt account that was ALSO still on a
 * temporary password.
 *
 * Usage: node tools/login-instant-session-check.cjs
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

function newContext(loginResponse, changePasswordResponse) {
  const store = {};
  ids.forEach((i) => { store[i] = makeEl(i); });

  const fetchCalls = [];
  let redirected = false;
  const loc = {
    get href() { return ''; },
    set href(v) { redirected = true; },
  };

  async function fakeFetch(url, opts = {}) {
    fetchCalls.push({ url, opts });
    if (url === '/api/login') return { ok: true, status: 200, json: async () => loginResponse };
    if (url === '/api/login/change-password') {
      return { ok: true, status: 200, json: async () => changePasswordResponse };
    }
    return { ok: true, status: 200, json: async () => ({}) };
  }

  const ctx = vm.createContext({
    console,
    document: { getElementById: (id) => store[id] || null, addEventListener() {} },
    location: loc,
    setTimeout, clearTimeout,
    fetch: fakeFetch,
    Math, JSON, Object, Array, Number, String, Boolean, Date, Set, Map, Promise,
  });
  ctx.window = ctx; ctx.globalThis = ctx;

  const errors = [];
  try {
    vm.runInContext(fs.readFileSync(path.join(ASSETS, 'login.js'), 'utf8'), ctx, { filename: 'login.js' });
  } catch (e) {
    errors.push('threw while loading: ' + e.message);
  }

  return { store, fetchCalls, errors, isRedirected: () => redirected };
}

const wait = (ms) => new Promise((r) => setTimeout(r, ms));

let failed = false;
function ok(cond, label) {
  console.log((cond ? 'ok  ' : 'FAIL') + ' - ' + label);
  if (!cond) failed = true;
}

(async () => {
  // ── the ordinary case: the account has no password change pending, so
  //    the very first password submit is the whole login. ──────────────
  {
    const { store, errors, isRedirected } = newContext({ ok: true }, {});
    ok(errors.length === 0, 'login.js loads without throwing: ' + errors.join('; '));

    store['user'].value = 'temporary';
    store['password'].value = 'GoldStar1234!';
    await store['form'].fire('submit');
    await wait(10);

    ok(isRedirected(), 'a TOTP-exempt account redirects straight to "/" on the first submit');
    ok(store['totp-setup'].hidden === true && store['totp-verify'].hidden === true &&
      store['change-password'].hidden === true,
      'no login-flow panel is ever shown — there was nothing left to do');
  }

  // ── the rarer case: exempt from 2FA, but still had to replace a
  //    temporary password first — the change-password panel's own submit
  //    is what finishes the login. ─────────────────────────────────────
  {
    const { store, errors, isRedirected } = newContext(
      { ok: false, stage: 'change_password' },
      { ok: true },
    );
    ok(errors.length === 0, 'reload: login.js loads without throwing: ' + errors.join('; '));

    store['user'].value = 'temporary';
    store['password'].value = 'GoldStar1234!';
    await store['form'].fire('submit');
    await wait(10);
    ok(store['change-password'].hidden === false, 'the change-password panel is shown first');
    ok(!isRedirected(), 'not redirected yet — the password still needs changing');

    store['new-password'].value = 'a genuinely new password';
    await store['change-password-form'].fire('submit');
    await wait(10);
    ok(isRedirected(), 'redirects to "/" once the password is changed — no 2FA step follows for an exempt account');
  }

  process.exit(failed ? 1 : 0);
})();
