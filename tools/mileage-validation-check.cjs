/* Boots the real repairs/app.js in a vm context with a fake DOM, loads a
 * vehicle whose history already has a recorded mileage, and exercises the
 * "don't allow entering lower mileage" guard: a mileage lower than the
 * highest on record is blocked before the form ever submits; an equal or
 * higher mileage goes through; leaving mileage blank is still allowed
 * regardless of what's on record; and switching to a DIFFERENT vehicle
 * resets the floor rather than carrying over the previous car's mileage.
 *
 * Usage: node tools/mileage-validation-check.cjs
 * Exits non-zero if any check fails or app.js throws while loading.
 */

'use strict';

const fs = require('fs');
const vm = require('vm');
const path = require('path');

const ASSETS = path.join(__dirname, '..', 'internal', 'web', 'assets', 'repairs');

const html = fs.readFileSync(path.join(ASSETS, 'index.html'), 'utf8');
const ids = [...html.matchAll(/id="([^"]+)"/g)].map((m) => m[1]);
const hiddenIds = new Set([...html.matchAll(/id="([^"]+)"[^>]*\shidden/g)].map((m) => m[1]));

function makeChoiceBtn(value) {
  const listeners = {};
  return {
    dataset: { value },
    classList: { add() {}, remove() {}, toggle() {}, contains: () => false },
    addEventListener(ev, fn) { (listeners[ev] ??= []).push(fn); },
    fire(ev, arg) { (listeners[ev] || []).forEach((fn) => fn(arg)); },
    click() { this.fire('click'); },
  };
}

function makeEl(id, children = []) {
  const listeners = {};
  return {
    id, textContent: '', innerHTML: '', value: '', hidden: hiddenIds.has(id),
    disabled: false, dataset: {}, style: {}, children,
    classList: { add() {}, remove() {}, toggle() {}, contains: () => false },
    addEventListener(ev, fn) { (listeners[ev] ??= []).push(fn); },
    fire(ev, arg) { (listeners[ev] || []).forEach((fn) => fn(arg || { preventDefault() {} })); },
    click() { this.fire('click'); },
    focus() {}, select() {},
    querySelectorAll(sel) { return sel === '.rp-choice-btn' ? children : []; },
    querySelector(sel) {
      const m = sel.match(/^\[data-value="([^"]+)"\]$/);
      return m ? (children.find((c) => c.dataset.value === m[1]) || null) : null;
    },
  };
}

const store = {};
ids.forEach((i) => { store[i] = makeEl(i); });
store['service-type-choice'] = makeEl('service-type-choice', ['full', 'mini', 'other'].map(makeChoiceBtn));
store['belt-choice'] = makeEl('belt-choice', ['no', 'yes'].map(makeChoiceBtn));

const fetchCalls = [];
// AB12CDE's most recent visit recorded 50,000 miles; an earlier visit
// recorded 60,000 — deliberately out of chronological order, so the guard
// has to take the HIGHEST mileage on file, not just the newest row's.
const history = {
  AB12CDE: [
    { service_date: '2026-08-01', service_type: 'mini', mileage: 50000, timing_belt_changed: false,
      description: '', vin: '', make: '', model: '', colour: '', cylinder_capacity: '', spare_keys: '',
      fuel_type: '', engine_size: '', engine_number: '', tyre_size: '', radio_code: '', oil_amount: '' },
    { service_date: '2025-08-01', service_type: 'full', mileage: 60000, timing_belt_changed: false,
      description: '', vin: '', make: '', model: '', colour: '', cylinder_capacity: '', spare_keys: '',
      fuel_type: '', engine_size: '', engine_number: '', tyre_size: '', radio_code: '', oil_amount: '' },
  ],
  CD34EFG: [
    { service_date: '2026-01-01', service_type: 'mini', mileage: 10000, timing_belt_changed: false,
      description: '', vin: '', make: '', model: '', colour: '', cylinder_capacity: '', spare_keys: '',
      fuel_type: '', engine_size: '', engine_number: '', tyre_size: '', radio_code: '', oil_amount: '' },
  ],
};

async function fakeFetch(url, opts = {}) {
  fetchCalls.push({ url, opts });
  if (url.startsWith('/api/repairs/search-vehicles')) {
    return { ok: true, status: 200, json: async () => Object.keys(history) };
  }
  if (url.startsWith('/api/repairs/reg-exists')) {
    const reg = decodeURIComponent(url.split('reg=')[1] || '');
    return { ok: true, status: 200, json: async () => ({ exists: !!history[reg] }) };
  }
  if (url.startsWith('/api/repairs/history')) {
    const reg = decodeURIComponent(url.split('reg=')[1] || '');
    return { ok: true, status: 200, json: async () => history[reg] || [] };
  }
  if (url === '/api/repairs/log' && opts.method === 'POST') {
    return { ok: true, status: 200, json: async () => ({ ok: true }) };
  }
  return { ok: true, status: 200, json: async () => ({}) };
}

const errors = [];
const ctx = vm.createContext({
  console,
  document: {
    getElementById: (id) => store[id] || null,
    querySelectorAll: () => [], querySelector: () => null,
    createElement: () => makeEl('tmp'),
    cookie: 'goldstar_repairs_csrf=test-csrf-token',
    addEventListener() {},
  },
  addEventListener() {},
  location: { href: '' },
  setTimeout, clearTimeout,
  fetch: fakeFetch,
  Math, JSON, Object, Array, Number, String, Boolean, Date, Set, Map, Promise,
  encodeURIComponent, decodeURIComponent, URL,
});
ctx.window = ctx; ctx.globalThis = ctx;

try {
  vm.runInContext(fs.readFileSync(path.join(ASSETS, 'app.js'), 'utf8'), ctx, { filename: 'app.js' });
} catch (e) {
  errors.push('threw while loading: ' + e.message);
}

const wait = (ms) => new Promise((r) => setTimeout(r, ms));

let failed = false;
function ok(cond, label) {
  console.log((cond ? 'ok  ' : 'FAIL') + ' - ' + label);
  if (!cond) failed = true;
}

/** Fills in the minimum a submit needs to get past every OTHER guard, so
    only the mileage check itself is what a given attempt is testing. */
function fillMinimumValidForm(mileage) {
  store['service-type-choice'].children[0].click(); // "full"
  store['mileage'].value = mileage;
}

(async () => {
  ok(errors.length === 0, 'app.js loads without throwing: ' + errors.join('; '));

  store['reg-input'].value = 'AB12CDE';
  store['reg-input'].fire('input');
  await wait(250);
  ok(store['form'].hidden === false, 'the form is revealed for a known registration');

  // Lower than the highest on file (60,000), even though it's higher than
  // the most recent visit's own 50,000 — the guard must use the true
  // maximum across history, not just the latest row.
  fillMinimumValidForm('55000');
  fetchCalls.length = 0;
  await store['form'].fire('submit');
  ok(fetchCalls.every((c) => c.url !== '/api/repairs/log'),
    'a mileage lower than the highest on record (55,000 < 60,000) is blocked before submitting');
  ok(store['log-err'].textContent.includes('60,000'),
    'the error names the actual mileage on file: ' + JSON.stringify(store['log-err'].textContent));

  // Equal to the highest on file — allowed; a car can sit at the same
  // reading between two visits close together.
  fillMinimumValidForm('60000');
  fetchCalls.length = 0;
  await store['form'].fire('submit');
  await wait(10);
  ok(fetchCalls.some((c) => c.url === '/api/repairs/log'),
    'a mileage equal to the highest on record is allowed through');

  // Higher — the normal case — allowed.
  fillMinimumValidForm('61200');
  fetchCalls.length = 0;
  await store['form'].fire('submit');
  await wait(10);
  ok(fetchCalls.some((c) => c.url === '/api/repairs/log'),
    'a mileage higher than the highest on record is allowed through');

  // Blank — allowed regardless of what's on file; not every visit needs a
  // mileage reading.
  fillMinimumValidForm('');
  fetchCalls.length = 0;
  await store['form'].fire('submit');
  await wait(10);
  ok(fetchCalls.some((c) => c.url === '/api/repairs/log'),
    'leaving mileage blank is allowed regardless of the highest on record');

  // Switching to a DIFFERENT vehicle with a much lower mileage of its own
  // must not still be blocked by AB12CDE's 60,000 — the floor has to
  // reset per registration.
  store['reg-input'].value = 'CD34EFG';
  store['reg-input'].fire('input');
  await wait(250);
  fillMinimumValidForm('10500');
  fetchCalls.length = 0;
  await store['form'].fire('submit');
  await wait(10);
  ok(fetchCalls.some((c) => c.url === '/api/repairs/log'),
    'switching vehicles resets the mileage floor — 10,500 is fine for CD34EFG\'s own 10,000-mile history');

  process.exit(failed ? 1 : 0);
})();
