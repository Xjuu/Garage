/* Boots the real repairs/upload.js in a vm context with a fake DOM and
 * exercises: browsing/searching a registration, the "add as new
 * registration?" prompt for one that doesn't exist yet, loading a known
 * vehicle's current spec into the form, and saving an edit.
 *
 * Usage: node tools/repairs-upload-check.cjs
 * Exits non-zero if any check fails or a loaded script throws.
 */

'use strict';

const fs = require('fs');
const vm = require('vm');
const path = require('path');

const ASSETS = path.join(__dirname, '..', 'internal', 'web', 'assets', 'repairs');

const html = fs.readFileSync(path.join(ASSETS, 'upload.html'), 'utf8');
const ids = [...html.matchAll(/id="([^"]+)"/g)].map((m) => m[1]);
const hiddenIds = new Set([...html.matchAll(/id="([^"]+)"[^>]*\shidden/g)].map((m) => m[1]));

function makeEl(id) {
  const listeners = {};
  const classes = new Set();
  return {
    id, textContent: '', innerHTML: '', value: '', hidden: hiddenIds.has(id),
    disabled: false, dataset: {}, style: {},
    classList: {
      add: (c) => classes.add(c), remove: (c) => classes.delete(c),
      toggle: (c, on) => { (on ?? !classes.has(c)) ? classes.add(c) : classes.delete(c); },
      contains: (c) => classes.has(c),
    },
    addEventListener(ev, fn) { (listeners[ev] ??= []).push(fn); },
    fire(ev, arg) { (listeners[ev] || []).forEach((fn) => fn(arg || { preventDefault() {} })); },
    click() { this.fire('click'); },
    focus() { this.fire('focus'); },
    querySelectorAll: () => [],
  };
}

const store = {};
ids.forEach((i) => { store[i] = makeEl(i); });

const fetchCalls = [];
let vehicleData = { registration: 'AB12CDE', vin: 'OLDVIN', make: 'Ford', model: 'Transit', colour: 'White',
  cylinder_capacity: '2198CC', spare_keys: '1', fuel_type: 'Diesel', engine_size: '', engine_number: 'DK1',
  tyre_size: '185/75/16', radio_code: '6769' };

async function fakeFetch(url, opts = {}) {
  fetchCalls.push({ url, opts });
  if (url.startsWith('/api/repairs/search-vehicles')) {
    return { ok: true, status: 200, json: async () => ['AB12CDE'] };
  }
  if (url.startsWith('/api/repairs/reg-exists')) {
    const reg = decodeURIComponent(url.split('reg=')[1] || '');
    return { ok: true, status: 200, json: async () => ({ exists: reg === 'AB12CDE' }) };
  }
  if (url.startsWith('/api/repairs/upload/vehicle') && (!opts.method || opts.method === 'GET')) {
    return { ok: true, status: 200, json: async () => vehicleData };
  }
  if (url === '/api/repairs/upload/vehicle' && opts.method === 'POST') {
    return { ok: true, status: 200, json: async () => ({ ok: true }) };
  }
  return { ok: true, status: 200, json: async () => ({}) };
}

const errors = [];
const ctx = vm.createContext({
  console,
  document: {
    getElementById: (id) => store[id] || null,
    querySelectorAll: () => [],
    querySelector: () => null,
    createElement: () => makeEl('tmp'),
    cookie: 'goldstar_repairs_csrf=test-csrf-token',
  },
  location: { href: '' },
  setTimeout, clearTimeout,
  fetch: fakeFetch,
  Math, JSON, Object, Array, Number, String, Boolean, Date, Set, Map, Promise,
  encodeURIComponent, decodeURIComponent, URL,
});
ctx.window = ctx; ctx.globalThis = ctx;

try {
  vm.runInContext(fs.readFileSync(path.join(ASSETS, 'upload.js'), 'utf8'), ctx, { filename: 'upload.js' });
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
  ok(errors.length === 0, 'upload.js loads without throwing: ' + errors.join('; '));

  // ── registration browse / search ──
  await wait(10); // upload.js focuses reg-input on load, firing a browse-all search
  let last = fetchCalls[fetchCalls.length - 1];
  ok(!!last && last.url.startsWith('/api/repairs/search-vehicles'),
    'page load browses vehicles via the initial focus: ' + (last && last.url));

  // ── an unrecognized registration prompts before offering the form ──
  fetchCalls.length = 0;
  store['reg-input'].value = 'NEW99REG';
  store['reg-input'].fire('input');
  await wait(250); // checkReg is debounced 200ms, same as search
  ok(store['form'].hidden === true, 'a registration matching nothing keeps the form hidden');
  ok(store['new-reg-prompt'].hidden === false, 'and shows the "add as new?" prompt instead');
  ok(store['new-reg-text'].textContent.includes('NEW99REG'), 'the prompt names the registration: ' + store['new-reg-text'].textContent);

  // Clicking through actually reveals the (blank) form for that reg.
  store['new-reg-add'].click();
  await wait(10);
  ok(store['new-reg-prompt'].hidden === true, 'clicking "Add new registration" dismisses the prompt');
  ok(store['form'].hidden === false, 'and reveals the form to fill in');
  ok(store['form-reg'].textContent === 'NEW99REG', 'for the registration that was typed: ' + store['form-reg'].textContent);

  // ── loading a KNOWN vehicle skips the prompt and prefills the form ──
  fetchCalls.length = 0;
  store['reg-input'].value = 'AB12CDE';
  store['reg-input'].fire('input');
  await wait(250);
  ok(store['new-reg-prompt'].hidden === true, 'a registration that does exist never shows the "add as new?" prompt');
  ok(store['form'].hidden === false, 'selecting a vehicle reveals the update form');
  ok(store['vin'].value === 'OLDVIN', 'VIN prefilled from the current registry record: got ' + store['vin'].value);
  ok(store['make'].value === 'Ford', 'Make prefilled: got ' + store['make'].value);

  // ── saving an edit ──
  store['vin'].value = 'NEWVIN123';
  fetchCalls.length = 0;
  await store['form'].fire('submit');
  await wait(10);
  const saveCall = fetchCalls.find((c) => c.url === '/api/repairs/upload/vehicle' && c.opts.method === 'POST');
  ok(!!saveCall, 'submitting posts to /api/repairs/upload/vehicle');
  if (saveCall) {
    const body = JSON.parse(saveCall.opts.body);
    ok(body.vehicle_reg === 'AB12CDE' && body.vin === 'NEWVIN123', 'the save carries the edited field: ' + saveCall.opts.body);
    ok(saveCall.opts.headers['X-CSRF-Token'] === 'test-csrf-token', 'the save carries the CSRF token');
  }

  process.exit(failed ? 1 : 0);
})();
