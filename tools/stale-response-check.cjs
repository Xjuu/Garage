/* Exercises the exact bug reported: "after a search you have this in
 * invoices even if there should be more if you switch quickly". Two
 * overlapping loadInvoices() calls — the second started before the first
 * has come back — with their responses arriving in the WRONG order (the
 * older call's response lands after the newer one's). Without a guard, the
 * older, stale response wins simply because it happened to finish last,
 * and the screen ends up showing results that don't match what's currently
 * selected.
 *
 * Loads the real scripts the way tools/ui-check.cjs does, with a
 * controllable fetch stub: this test decides exactly when each in-flight
 * request resolves, rather than relying on real timing (which would make
 * the race non-deterministic and the test flaky).
 *
 * Usage: node tools/stale-response-check.cjs
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

function el(id) {
  const listeners = {};
  const classes = new Set();
  return {
    id, textContent: '', innerHTML: '', value: '', hidden: hiddenIds.has(id),
    dataset: {}, style: {},
    classList: {
      add: (c) => classes.add(c), remove: (c) => classes.delete(c),
      toggle: (c, on) => { (on ?? !classes.has(c)) ? classes.add(c) : classes.delete(c); },
      contains: (c) => classes.has(c),
    },
    addEventListener(ev, fn) { (listeners[ev] ??= []).push(fn); },
    click() { (listeners.click || []).forEach((fn) => fn({})); },
    setAttribute() {}, removeAttribute() {},
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

// Every fetch() call is parked here instead of actually resolving, so the
// test controls exactly which one settles first — the entire point, since
// the real bug only shows up for a specific, otherwise-rare resolution
// order.
const pendingFetches = [];
function fetchStub(url) {
  return new Promise((resolve) => {
    pendingFetches.push({
      url,
      resolve: (body) => resolve({ ok: true, status: 200, json: async () => body }),
    });
  });
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

function invoiceBody(total, supplier) {
  return { total, netto: total, vat: 0, brutto: total, invoices: [{ ID: 1, Supplier: supplier, Items: [] }] };
}

(async () => {

// The reported scenario: loadInvoices() called once (say, a search that
// matched a lot — "the first, slower request"), then called again very
// soon after (switching away and back, or the next keystroke) before the
// first has returned. The SLOW one is the one issued FIRST; the FAST one
// is issued second but its response lands first — the out-of-order case
// that actually breaks something.
const call1 = ctx.loadInvoices(); // starts the "should have more results" request
const call2 = ctx.loadInvoices(); // the very next thing the user did, before call1 returned

check('both calls actually issued a request before either resolved', pendingFetches.length === 2);

// Resolve them in reverse order: the second (newer) call's response
// arrives first, then the first (older, stale) call's response arrives
// after it — exactly the ordering that would corrupt the screen without
// a guard.
pendingFetches[1].resolve(invoiceBody(11, 'Millfield Autoparts Ltd')); // call2's real, current data
await Promise.resolve(); // let call2's .then/render microtasks run
pendingFetches[0].resolve(invoiceBody(0, ''));                        // call1's now-stale data, arriving late
await call1;
await call2;

check('the newer call\'s total is what ends up on screen (11), not the stale 0',
  store['inv-sub'].textContent.includes('11'));
check('the stale response did not overwrite the rows with "Nothing to show"',
  !store['inv-rows'].innerHTML.includes('Nothing to show'));

if (failed) {
  console.log(`\n${failed} check(s) failed.`);
  process.exit(1);
}
console.log('\nall checks passed.');

})();
