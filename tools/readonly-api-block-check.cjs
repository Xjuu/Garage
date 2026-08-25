/* Boots the real app.js in a vm context and calls its api() helper
 * directly, proving the client-side half of read-only enforcement: with
 * <body data-readonly="true">, a mutating call (POST/PUT/PATCH/DELETE)
 * never reaches fetch at all — it throws immediately with a clear message —
 * while a GET still goes through untouched, and a non-read-only body
 * behaves exactly as before. The server enforces this too (auth.Protect,
 * see internal/auth/auth_test.go); this is only the front end's own copy of
 * that same rule.
 *
 * Usage: node tools/readonly-api-block-check.cjs
 * Exits non-zero if any check fails or app.js throws while loading.
 */

'use strict';

const fs = require('fs');
const vm = require('vm');
const path = require('path');

const ASSETS = path.join(__dirname, '..', 'internal', 'web', 'assets');

const html = fs.readFileSync(path.join(ASSETS, 'index.html'), 'utf8');
const ids = [...html.matchAll(/id="([^"]+)"/g)].map((m) => m[1]);
const hiddenIds = new Set([...html.matchAll(/id="([^"]+)"[^>]*\shidden/g)].map((m) => m[1]));

const el = (id) => ({
  id, textContent: '', innerHTML: '', value: '', hidden: hiddenIds.has(id),
  dataset: {}, style: {},
  classList: { toggle() {}, add() {}, remove() {}, contains: () => false },
  addEventListener() {}, setAttribute() {}, removeAttribute() {},
  appendChild() {}, removeChild() {}, remove() {}, insertAdjacentHTML() {},
  scrollIntoView() {}, focus() {}, blur() {},
  querySelectorAll: () => [], querySelector: () => null, closest: () => null,
  isConnected: true, clientWidth: 1200,
});

const store = {};
ids.forEach((i) => { store[i] = el(i); });
['c-invoices', 'c-vehicles', 'c-parts', 'c-suppliers', 'c-training', 'c-exports']
  .forEach((i) => { store[i] ??= el(i); });

const fetchCalls = [];
async function fakeFetch(url, opts = {}) {
  fetchCalls.push({ url, opts });
  return { ok: true, status: 200, json: async () => ({ ok: true }) };
}

const body = el('body');
const ctx = vm.createContext({
  console,
  document: {
    getElementById: (id) => store[id] || null,
    querySelectorAll: () => [], querySelector: () => null,
    addEventListener() {}, createElement: () => el('tmp'),
    body, cookie: 'goldstar_csrf=test-csrf-token',
  },
  window: { addEventListener() {}, location: { href: '' } },
  location: { href: '' },
  ResizeObserver: class { observe() {} },
  setTimeout: () => 0, setInterval: () => 0, clearTimeout() {}, clearInterval() {},
  fetch: fakeFetch,
  Math, JSON, Object, Array, Number, String, Boolean, Date, Set, Map, Promise,
  Error, Intl, URLSearchParams, encodeURIComponent, parseInt, parseFloat, isNaN,
  confirm: () => true, alert() {},
});
ctx.globalThis = ctx;

const errors = [];
try {
  vm.runInContext(fs.readFileSync(path.join(ASSETS, 'app.js'), 'utf8'), ctx, { filename: 'app.js' });
} catch (e) {
  errors.push(`${e.constructor.name}: ${e.message}`);
}

let failed = false;
function ok(cond, label) {
  console.log((cond ? 'ok  ' : 'FAIL') + ' - ' + label);
  if (!cond) failed = true;
}

(async () => {
  ok(errors.length === 0, 'app.js loads without throwing: ' + errors.join('; '));
  ok(typeof ctx.api === 'function', 'api() is defined');

  // ── read-only: every mutating method is refused before fetch is called ──
  body.dataset.readonly = 'true';
  for (const method of ['POST', 'PUT', 'PATCH', 'DELETE']) {
    fetchCalls.length = 0;
    let threw = null;
    try {
      await ctx.api('/api/invoices/1', { method });
    } catch (e) {
      threw = e;
    }
    ok(threw !== null, `${method}: a read-only account's call throws instead of succeeding`);
    ok(threw && /view-only/i.test(threw.message),
      `${method}: the error names it a view-only account: ${threw && threw.message}`);
    ok(fetchCalls.length === 0, `${method}: fetch was never called — refused before the request went out`);
  }

  // GET must still work — "view only" means views keep working.
  fetchCalls.length = 0;
  await ctx.api('/api/invoices');
  ok(fetchCalls.length === 1 && fetchCalls[0].url === '/api/invoices',
    'a read-only account\'s GET request still reaches fetch');

  // ── an ordinary (non-read-only) body: mutating calls go through fine ──
  delete body.dataset.readonly;
  fetchCalls.length = 0;
  await ctx.api('/api/invoices/1', { method: 'DELETE' });
  ok(fetchCalls.length === 1 && fetchCalls[0].opts.method === 'DELETE',
    'without data-readonly, a DELETE call reaches fetch as normal');
  ok(fetchCalls[0].opts.headers['X-CSRF-Token'] === 'test-csrf-token',
    'and still carries the CSRF token, same as always');

  process.exit(failed ? 1 : 0);
})();
