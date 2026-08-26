/* Boots the real dashboard scripts (same shared-scope technique as
 * ui-check.cjs) and calls the real loadMakes() against a fake /api/makes
 * response, proving: the by-make table renders with an average-per-vehicle
 * column; the by-make-and-model table renders every row; a model well above
 * its own make's average gets the "flag" pill with a "+N%" label; a model at
 * or below its make's average, and the sole model of a single-model make,
 * render without it; and the bar chart actually gets drawn.
 *
 * Usage: node tools/makes-view-check.cjs
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

const MAKES = [
  { make: 'Ford', vehicles: 3, invoices: 5, netto: 583.33, vat: 116.67, brutto: 700, avg_per_vehicle: 233.33 },
  { make: 'Toyota', vehicles: 1, invoices: 1, netto: 250, vat: 50, brutto: 300, avg_per_vehicle: 300 },
];
const MODELS = [
  { make: 'Ford', model: 'Transit', vehicles: 1, invoices: 1, netto: 416.67, vat: 83.33, brutto: 500,
    avg_per_vehicle: 500, make_avg_per_vehicle: 233.33, pct_above_make_avg: 114.3 },
  { make: 'Toyota', model: 'Corolla', vehicles: 1, invoices: 1, netto: 250, vat: 50, brutto: 300,
    avg_per_vehicle: 300, make_avg_per_vehicle: 300, pct_above_make_avg: 0 },
  { make: 'Ford', model: 'Focus', vehicles: 2, invoices: 4, netto: 166.67, vat: 33.33, brutto: 200,
    avg_per_vehicle: 100, make_avg_per_vehicle: 233.33, pct_above_make_avg: -57.1 },
];

const fetchCalls = [];
async function fakeFetch(url) {
  fetchCalls.push(url);
  if (url === '/api/makes') return { ok: true, status: 200, json: async () => ({ makes: MAKES, models: MODELS }) };
  return { ok: true, status: 200, json: async () => ({}) };
}

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
  setTimeout: () => 0, setInterval: () => 0, clearTimeout() {}, clearInterval() {},
  fetch: fakeFetch,
  Math, JSON, Object, Array, Number, String, Boolean, Date, Set, Map, Promise,
  Error, Intl, URLSearchParams, encodeURIComponent, parseInt, parseFloat, isNaN,
  confirm: () => true, alert() {},
});
ctx.globalThis = ctx;

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
  ok(typeof ctx.loadMakes === 'function', 'loadMakes is defined');

  await ctx.loadMakes();
  ok(fetchCalls.includes('/api/makes'), 'loadMakes requests /api/makes');

  const makeRows = store['make-rows'].innerHTML;
  ok(makeRows.includes('Ford') && makeRows.includes('Toyota'), 'both makes appear in the by-make table');
  ok(makeRows.includes(money(233.33)), `the by-make table shows the average per vehicle: ${JSON.stringify(makeRows)}`);

  const modelRows = store['model-rows'].innerHTML;
  ok(modelRows.includes('Transit') && modelRows.includes('Corolla') && modelRows.includes('Focus'),
    'all three models appear in the by-make-and-model table');

  ok(modelRows.includes('+114%'), `the Transit's own +% figure is rendered: ${JSON.stringify(modelRows)}`);
  // Each row is checked in isolation (split on <tr>), not with a lookahead
  // across the whole table — a naive "does 'pill flag' appear anywhere
  // after this model's name" check can spill into a different row
  // depending on row order.
  const rowFor = (name) => modelRows.split('<tr>').find((r) => r.includes(name)) || '';
  ok(!rowFor('Corolla').includes('pill flag'),
    'the Corolla — the only model of its make, 0% above — gets no flag pill');
  ok(!rowFor('Focus').includes('pill flag'),
    'the Focus — below its make\'s average — gets no flag pill');
  ok(rowFor('Transit').includes('pill flag'),
    'the Transit — well above its make\'s average — gets the flag pill (isolated row check)');

  process.exit(failed ? 1 : 0);
})();

function money(n) {
  return new Intl.NumberFormat('en-GB', { minimumFractionDigits: 2, maximumFractionDigits: 2 }).format(n);
}
