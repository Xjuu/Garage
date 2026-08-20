/* Exercises the reported bug: Analysis > Parts always said "0 parts" until
 * the tab had actually been clicked once. Unlike vehicles and suppliers,
 * whose counts ride along in the Overview response that already loads on
 * every boot, nothing fetched the parts count proactively — only
 * loadParts() itself set that badge, and nothing called it until the tab
 * was opened.
 *
 * Usage: node tools/parts-count-check.cjs
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

function el(id) {
  return {
    id, textContent: '', innerHTML: '', value: '', hidden: false, dataset: {}, style: {},
    classList: { add() {}, remove() {}, toggle() {}, contains: () => false },
    addEventListener() {}, click() {}, setAttribute() {}, removeAttribute() {},
    appendChild() {}, removeChild() {}, remove() {}, insertAdjacentHTML() {},
    scrollIntoView() {}, focus() {}, blur() {},
    querySelectorAll: () => [], querySelector: () => null, closest: () => null,
    isConnected: true, clientWidth: 1200,
  };
}

const store = {};
ids.forEach((i) => { store[i] = el(i); });
['c-invoices', 'c-vehicles', 'c-parts', 'c-suppliers', 'c-training', 'c-exports']
  .forEach((i) => { store[i] ??= el(i); });

// Every request is recorded, and each route answers with its own canned
// body — the point is confirming refreshCounts() actually asks for the
// parts list on its own, not relying on loadParts() ever having run.
const requestedURLs = [];
const routes = {
  '/api/parts': [{ part_number: 'A1' }, { part_number: 'B2' }, { part_number: 'C3' }],
  '/api/exports': [],
};
async function fetchStub(url) {
  requestedURLs.push(url);
  const key = Object.keys(routes).find((k) => url.startsWith(k));
  return { ok: true, status: 200, json: async () => (key ? routes[key] : {}) };
}

const ctx = vm.createContext({
  console,
  document: {
    getElementById: (id) => store[id] || null,
    querySelectorAll: () => [], querySelector: () => null,
    addEventListener() {}, createElement: () => el('tmp'), body: el('body'), cookie: '',
    activeElement: null,
  },
  window: { addEventListener() {}, location: { href: '' } },
  location: { href: '' },
  ResizeObserver: class { observe() {} },
  setTimeout: () => 0, setInterval: () => 0, clearTimeout() {}, clearInterval() {},
  fetch: fetchStub,
  Math, JSON, Object, Array, Number, String, Boolean, Date, Set, Map, Promise,
  Error, Intl, URLSearchParams, encodeURIComponent, parseInt, parseFloat, isNaN,
  confirm: () => true, alert() {},
});
ctx.globalThis = ctx;

const errors = [];
for (const file of ORDER) {
  try {
    new vm.Script(fs.readFileSync(path.join(ASSETS, file), 'utf8'), { filename: file }).runInContext(ctx);
  } catch (e) {
    errors.push(`${file}: ${e.constructor.name}: ${e.message}`);
  }
}
if (errors.length) {
  console.log('script errors:', errors);
  process.exit(1);
}

let failed = 0;
function check(label, ok) {
  console.log((ok ? 'ok  - ' : 'FAIL- ') + label);
  if (!ok) failed++;
}

(async () => {

check('c-parts starts at the page\'s default before anything has run', store['c-parts'].textContent === '');

// This is the actual boot-time call (see refreshAll() in app.js) — never
// having visited the Parts tab.
await ctx.refreshCounts();

check('refreshCounts() requested the parts list on its own',
  requestedURLs.some((u) => u.startsWith('/api/parts')));
check('the Parts badge reflects the real count without the tab ever being opened',
  store['c-parts'].textContent === '3');

if (failed) {
  console.log(`\n${failed} check(s) failed.`);
  process.exit(1);
}
console.log('\nall checks passed.');

})();
