/* Exercises the "show only this vehicle's invoices?" prompt: opening a
 * vehicle with invoices offers it as a dismissible toast, not a silent
 * automatic filter and not a blocking dialog; opening one with none offers
 * nothing; clicking the button actually applies the filter and navigates to
 * Invoices; ignoring it and letting the timeout fire touches nothing.
 *
 * filterByVehicle() already existed in app.js before this — the actual gap
 * was that nothing ever called it. This checks the wiring, not just the
 * function in isolation, driven through the real openVehicle() the way a
 * click on a vehicle row actually would — state.currentReg is a `const
 * state`-scoped binding this test cannot reach directly (only functions
 * declared in the same script are reified as properties on the vm context;
 * plain `const`/`let` bindings are not), so going through the real function
 * is not just more faithful, it is the only thing that works.
 *
 * Usage: node tools/vehicle-filter-prompt-check.cjs
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
    dataset: {}, style: {}, children: [],
    classList: {
      add: (c) => classes.add(c), remove: (c) => classes.delete(c), toggle() {},
      contains: (c) => classes.has(c),
    },
    addEventListener(ev, fn) { (listeners[ev] ??= []).push(fn); },
    click() { (listeners.click || []).forEach((fn) => fn({})); },
    appendChild(child) { this.children.push(child); },
    remove() {}, // real removal from #toasts isn't needed for what these checks inspect
    setAttribute() {}, removeAttribute() {}, removeChild() {}, insertAdjacentHTML() {},
    scrollIntoView() {}, focus() {}, blur() {},
    querySelectorAll: () => [], querySelector: () => null, closest: () => null,
    isConnected: true, clientWidth: 1200,
  };
}

const store = {};
ids.forEach((i) => { store[i] = el(i); });
// veh-capabilities-input/-save aren't in index.html — renderVehicleSpec
// builds them itself as an HTML string and wires listeners onto whatever
// getElementById finds afterwards, which only a real DOM materializes from
// that string. loadVehicleDetail() calls renderVehicleSpec on its way to
// the action toast this check is actually about, so it just needs
// renderVehicleSpec not to throw reaching for them.
['c-invoices', 'c-vehicles', 'c-parts', 'c-suppliers', 'c-training', 'c-exports',
  'veh-capabilities-input', 'veh-capabilities-save']
  .forEach((i) => { store[i] ??= el(i); });

// Real elements, not the generic stub: actionToast() actually builds its
// toast and button with this, and the test inspects what ends up inside.
function createElement() { return el('created-' + Math.random()); }

// setTimeout is captured, not run — proving "ignoring it does nothing" only
// works if the test controls whether the auto-dismiss timer actually fires,
// rather than waiting on a real clock.
const timers = [];
function setTimeoutStub(fn, ms) { timers.push({ fn, ms }); return timers.length; }

// Every request's URL is recorded — the most direct way to confirm the
// vehicle filter genuinely reached the request that Invoices sends, not
// just that some DOM field looks right.
const requestedURLs = [];
let apiResponse = {};
async function fetchStub(url) {
  requestedURLs.push(url);
  return { ok: true, status: 200, json: async () => apiResponse };
}

const ctx = vm.createContext({
  console,
  document: {
    getElementById: (id) => store[id] || null,
    querySelectorAll: () => [], querySelector: () => null,
    addEventListener() {}, createElement, body: el('body'), cookie: '', activeElement: null,
  },
  window: { addEventListener() {}, location: { href: '' } },
  location: { href: '' },
  ResizeObserver: class { observe() {} },
  setTimeout: setTimeoutStub, setInterval: () => 0, clearTimeout() {}, clearInterval() {},
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

function vehicleBody(reg, invoiceCount) {
  return {
    vehicle: {
      registration: reg, make: 'Ford', model: 'Focus', brutto: 100, netto: 80, vat: 20,
      invoices: invoiceCount, first_seen: '', last_seen: '',
    },
    avg_per_month: 0, months_active: 0, months: [], by_supplier: [], parts: [], invoices: [],
  };
}

// openVehicle() -> show('vehicle') -> loadView() fires loadVehicleDetail()
// without awaiting it (the real code is fire-and-forget there, matching a
// real click handler), and that function chains several awaits of its own
// (the fetch stub's promise, then .json(), then api()'s own status checks)
// before it touches the DOM — a couple of bare microtask ticks is not
// reliably enough turns of the queue to drain all of that. setImmediate is
// Node's real one (the vm context only stubs setTimeout), so this actually
// waits rather than assuming a fixed number of ticks is enough.
async function settle() {
  for (let i = 0; i < 10; i++) await new Promise((r) => setImmediate(r));
}

(async () => {

// 1. A vehicle with invoices gets offered the prompt.
apiResponse = vehicleBody('HJ72MHE', 3);
timers.length = 0;
ctx.openVehicle('HJ72MHE');
await settle();
const toasts = store['toasts'].children;
check('a vehicle with invoices gets one action toast', toasts.length === 1);
check('the toast names the actual registration', toasts[0]?.children?.[0]?.textContent.includes('HJ72MHE'));
check('an auto-dismiss timer was scheduled', timers.length === 1 && timers[0].ms > 0);

// 2. Clicking the button applies the filter and jumps to Invoices — the
//    real filterByVehicle(), proving the wiring, not just the toast itself.
requestedURLs.length = 0;
toasts[0].children[1].click(); // the button
await settle();
check('clicking "Show invoices" sets the vehicle filter', store['f-reg'].value === 'HJ72MHE');
check('clicking "Show invoices" actually requests that vehicle\'s invoices',
  requestedURLs.some((u) => u.includes('/api/invoices') && u.includes('reg=HJ72MHE')));

// 3. A vehicle with no invoices at all is not worth offering — there is
//    nothing to filter to.
store['toasts'].children = [];
apiResponse = vehicleBody('AB12CDE', 0);
timers.length = 0;
ctx.openVehicle('AB12CDE');
await settle();
check('a vehicle with zero invoices gets no prompt', store['toasts'].children.length === 0);

// 4. Ignoring the prompt and letting the timer fire must not filter
//    anything — the whole point is that doing nothing is a real option.
apiResponse = vehicleBody('CD34EFG', 5);
timers.length = 0;
store['f-reg'].value = '';
ctx.openVehicle('CD34EFG');
await settle();
timers[0].fn(); // simulate the auto-dismiss timeout actually firing
check('letting the prompt time out does not apply any filter', store['f-reg'].value === '');

if (failed) {
  console.log(`\n${failed} check(s) failed.`);
  process.exit(1);
}
console.log('\nall checks passed.');

})();
