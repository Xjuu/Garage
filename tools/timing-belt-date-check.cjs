/* Boots the real dashboard scripts (same shared-scope technique as
 * ui-check.cjs) and calls the real renderVehicleSpec directly — no need to
 * fake a network round trip for a check this narrow — proving the "Last
 * timing belt change" card reads as a normal UK date (e.g. "13 Apr 2024")
 * rather than the raw ISO string the database actually stores.
 *
 * Usage: node tools/timing-belt-date-check.cjs
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
  fetch: async () => ({ ok: true, status: 200, json: async () => ({}) }),
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

ok(errors.length === 0, 'every dashboard script loads without throwing: ' + errors.join('; '));
ok(typeof ctx.renderVehicleSpec === 'function', 'renderVehicleSpec is defined');
ok(typeof ctx.ukDate === 'function', 'ukDate is defined');

ctx.renderVehicleSpec({ colour: 'Blue' }, '2024-04-13', true);
const html2 = store['veh-spec'].innerHTML;
ok(html2.includes('13 Apr 2024'), `the card shows a UK-formatted date: ${JSON.stringify(html2.match(/Last timing belt[\s\S]{0,120}/)?.[0])}`);
ok(!html2.includes('2024-04-13'), 'the raw ISO date does not leak through anywhere in the card');

// A vehicle with no recorded timing-belt change at all must not show a
// fabricated or blank-but-present card — the whole card is omitted, same
// as any other spec fact with nothing on file.
store['veh-spec'].innerHTML = '';
ctx.renderVehicleSpec({ colour: 'Blue' }, '', false);
ok(!store['veh-spec'].innerHTML.includes('Last timing belt'), 'no card at all when there is no recorded change');

process.exit(failed ? 1 : 0);
