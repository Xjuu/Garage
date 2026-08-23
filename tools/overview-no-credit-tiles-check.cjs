/* Boots the real dashboard scripts (same shared-scope technique as
 * ui-check.cjs) and calls the real loadOverview with a response that has
 * live credit notes on it, proving the Overview tiles never mention
 * "Credit notes" or "Net of credits" — a credit note already shows where
 * it matters (greyed out as "Returned" in the Invoices list), not as a
 * second, separate concept surfaced here too.
 *
 * Usage: node tools/overview-no-credit-tiles-check.cjs
 * Exits non-zero if any check fails or a script throws while loading.
 */

'use strict';

const fs = require('fs');
const vm = require('vm');
const path = require('path');

const ASSETS = path.join(__dirname, '..', 'internal', 'web', 'assets');
const ORDER = ['ping.js', 'caricon.js', 'chart.js', 'app.js', 'omni.js', 'spending.js',
  'exports.js', 'fleet.js', 'training.js', 'admin.js'];

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

// A live credit note on the response — the exact shape a real "invoice,
// then its credit note" pair produces (see store.Overview): Purchases and
// Credits split apart, CreditCount > 0, Brutto the true net.
const OVERVIEW = {
  invoices: 21, items: 47, vehicles: 5, suppliers: 3, needs_review: 0,
  netto: 1900, vat: 380, brutto: 2000,
  purchases: 2500, credits: -500, credit_count: 1, months: [],
};

const errors = [];
const ctx = vm.createContext({
  console,
  document: {
    getElementById: (id) => store[id] || null,
    querySelectorAll: () => [], querySelector: () => null,
    addEventListener() {}, createElement: () => el('tmp'), body: el('body'), cookie: '', activeElement: null,
  },
  window: { addEventListener() {}, location: { href: '' } },
  location: { href: '' },
  ResizeObserver: class { observe() {} },
  setTimeout, clearTimeout, setInterval: () => 0, clearInterval() {},
  fetch: async (url) => {
    if (url.startsWith('/api/overview')) {
      return { ok: true, status: 200, json: async () => ({ overview: OVERVIEW, this_month: {} }) };
    }
    if (url.startsWith('/api/vehicles')) {
      return { ok: true, status: 200, json: async () => [] };
    }
    return { ok: true, status: 200, json: async () => ({}) };
  },
  Math, JSON, Object, Array, Number, String, Boolean, Date, Set, Map, Promise,
  Error, Intl, URLSearchParams, encodeURIComponent, parseInt, parseFloat, isNaN,
  confirm: () => true, alert() {},
});
ctx.globalThis = ctx;
process.on('unhandledRejection', (e) => errors.push(`unhandledRejection: ${e && e.message}`));

for (const file of ORDER) {
  try {
    new vm.Script(fs.readFileSync(path.join(ASSETS, file), 'utf8'), { filename: file }).runInContext(ctx);
  } catch (e) {
    errors.push(`${file}: ${e.constructor.name}: ${e.message}`);
  }
}

let failed = false;
function ok(cond, label) {
  console.log((cond ? 'ok  ' : 'FAIL') + ' - ' + label);
  if (!cond) failed = true;
}

(async () => {
  ok(errors.length === 0, 'every dashboard script loads without throwing: ' + errors.join('; '));
  ok(typeof ctx.loadOverview === 'function', 'loadOverview is defined');

  await ctx.loadOverview();
  await new Promise((r) => setTimeout(r, 10));

  const html = store['tiles'].innerHTML;
  ok(html.includes('Purchases'), 'Overview still shows the Purchases tile');
  ok(!html.includes('Credit notes'), 'Overview does NOT show a "Credit notes" tile, even with a live credit note');
  ok(!html.includes('Net of credits'), 'Overview does NOT show a "Net of credits" tile either');

  process.exit(failed ? 1 : 0);
})();
