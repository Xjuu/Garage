/* Boots the real repairs/pinbox.js + repairs/upload.js in a vm context with
 * a fake DOM and exercises: the 6-box PIN widget (typing advances focus,
 * backspace-on-empty steps back, paste fills every box, onComplete fires
 * once all 6 digits are in), browsing/searching a registration, loading a
 * vehicle's current spec into the form, saving it, and — the part most
 * likely to have a real bug — a 403 "reverify" response bringing up the
 * PIN overlay and, once it's cleared, automatically retrying the save that
 * triggered it.
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

function makePinBox() {
  const listeners = {};
  return {
    value: '',
    addEventListener(ev, fn) { (listeners[ev] ??= []).push(fn); },
    fire(ev, arg) { (listeners[ev] || []).forEach((fn) => fn(arg)); },
    focus() { this.focused = true; },
  };
}

const store = {};
ids.forEach((i) => { store[i] = makeEl(i); });

// The 6 boxes inside each PIN group are anonymous in the markup (shared
// class, no individual ids) — modelled the same way the real DOM would
// resolve document.querySelectorAll('#container .pin-box').
const pinGroups = {
  'pin-boxes': Array.from({ length: 6 }, makePinBox),
  'reverify-boxes': Array.from({ length: 6 }, makePinBox),
};

const fetchCalls = [];
let vehicleData = { registration: 'AB12CDE', vin: 'OLDVIN', make: 'Ford', model: 'Transit', colour: 'White',
  cylinder_capacity: '2198CC', spare_keys: '1', fuel_type: 'Diesel', engine_size: '', engine_number: 'DK1',
  tyre_size: '185/75/16', radio_code: '6769' };
let uploadShouldReverify = false;
let reverifyAccepts = '112233';

async function fakeFetch(url, opts = {}) {
  fetchCalls.push({ url, opts });
  if (url.startsWith('/api/repairs/search-vehicles')) {
    return { ok: true, status: 200, json: async () => ['AB12CDE'] };
  }
  if (url.startsWith('/api/repairs/upload/vehicle') && (!opts.method || opts.method === 'GET')) {
    return { ok: true, status: 200, json: async () => vehicleData };
  }
  if (url === '/api/repairs/upload/vehicle' && opts.method === 'POST') {
    if (uploadShouldReverify) {
      return { ok: false, status: 403, json: async () => ({ error: 'reverify' }) };
    }
    return { ok: true, status: 200, json: async () => ({ ok: true }) };
  }
  if (url === '/api/repairs/upload/verify' && opts.method === 'POST') {
    const body = JSON.parse(opts.body);
    if (body.code === reverifyAccepts) {
      uploadShouldReverify = false; // the real server would reset the throttle here
      return { ok: true, status: 200, json: async () => ({ ok: true }) };
    }
    // 403, not 401 — a wrong re-verify code is not "this device's session
    // expired", and api()'s blanket 401-means-signed-out handling would
    // otherwise navigate the whole page away instead of just showing an
    // error next to the boxes. See repairsUploadVerify's own comment.
    return { ok: false, status: 403, json: async () => ({ error: 'incorrect code' }) };
  }
  return { ok: true, status: 200, json: async () => ({}) };
}

const errors = [];
const ctx = vm.createContext({
  console,
  document: {
    getElementById: (id) => store[id] || null,
    querySelectorAll: (sel) => {
      const m = sel.match(/^#([\w-]+) \.pin-box$/);
      if (m && pinGroups[m[1]]) return pinGroups[m[1]];
      return [];
    },
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
  vm.runInContext(fs.readFileSync(path.join(ASSETS, 'pinbox.js'), 'utf8'), ctx, { filename: 'pinbox.js' });
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

// Types a digit into each box of a pin group the way a real keystroke would
// — set .value then fire 'input' — so the widget's own advance-focus /
// completion logic runs exactly as it would in a browser.
function typeCode(groupId, code) {
  const boxes = pinGroups[groupId];
  for (let i = 0; i < code.length; i++) {
    boxes[i].value = code[i];
    boxes[i].fire('input');
  }
}

(async () => {
  ok(errors.length === 0, 'pinbox.js and upload.js load without throwing: ' + errors.join('; '));

  // ── pinbox widget behaviour, exercised through the sign-in group ──
  const boxes = pinGroups['pin-boxes'];
  let completedWith = null;
  // setupPinBoxes is declared inside the vm context (pinbox.js), not in this
  // outer Node scope — reached via the context object it was attached to,
  // the same way the other harnesses in this repo reach a page script's
  // top-level functions.
  const pinTest = ctx.setupPinBoxes('pin-boxes', (code) => { completedWith = code; });

  boxes[0].value = '1'; boxes[0].fire('input');
  ok(boxes[1].focused === true, 'typing a digit advances focus to the next box');

  boxes[1].value = ''; // simulate having nothing there, then backspacing
  boxes[1].fire('keydown', { key: 'Backspace' });
  ok(boxes[0].focused === true, 'backspace on an empty box steps focus back');

  pinTest.clear();
  completedWith = null;
  typeCode('pin-boxes', '112233');
  ok(completedWith === '112233', 'onComplete fires with the full 6-digit code once every box is filled: got ' + completedWith);

  pinTest.clear();
  ok(boxes.every((b) => b.value === ''), 'clear() empties every box');

  // Paste support: pasting a full code should distribute across all boxes
  // and fire completion too.
  completedWith = null;
  boxes[0].fire('paste', {
    preventDefault() {},
    clipboardData: { getData: () => '998877' },
  });
  ok(completedWith === '998877', 'pasting a 6-digit code fills every box and completes: got ' + completedWith);

  // ── registration browse / search ──
  await wait(10); // upload.js focuses reg-input on load, firing a browse-all search
  let last = fetchCalls[fetchCalls.length - 1];
  ok(!!last && last.url.startsWith('/api/repairs/search-vehicles'),
    'page load browses vehicles via the initial focus: ' + (last && last.url));

  // ── loading a vehicle prefills the form ──
  fetchCalls.length = 0;
  store['reg-input'].value = 'AB12CDE';
  store['reg-input'].fire('input');
  await wait(10);
  ok(store['form'].hidden === false, 'selecting a vehicle reveals the update form');
  ok(store['vin'].value === 'OLDVIN', 'VIN prefilled from the current registry record: got ' + store['vin'].value);
  ok(store['make'].value === 'Ford', 'Make prefilled: got ' + store['make'].value);

  // ── saving without needing to reverify ──
  uploadShouldReverify = false;
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
  ok(store['reverify-overlay'].hidden === true, 'no reverify needed — the overlay stays hidden');

  // ── the throttle firing: reverify overlay appears, wrong code stays open,
  //    correct code closes it AND automatically retries the original save ──
  uploadShouldReverify = true;
  store['vin'].value = 'THROTTLED-EDIT';
  const submitPromise = store['form'].fire('submit');
  await wait(10);
  ok(store['reverify-overlay'].hidden === false, 'a 403 "reverify" response opens the PIN overlay');

  typeCode('reverify-boxes', '000000'); // wrong code
  await wait(10);
  ok(store['reverify-overlay'].hidden === false, 'a wrong reverify code keeps the overlay open');
  ok(store['reverify-err'].textContent === 'incorrect code',
    'a wrong reverify code shows the real error, not a "signed out" redirect: ' + store['reverify-err'].textContent);
  ok(ctx.location.href === '', 'a wrong reverify code must not navigate the page away: location.href=' + ctx.location.href);

  fetchCalls.length = 0;
  typeCode('reverify-boxes', reverifyAccepts); // correct code
  await wait(20);
  ok(store['reverify-overlay'].hidden === true, 'the correct reverify code closes the overlay');
  const retried = fetchCalls.find((c) => c.url === '/api/repairs/upload/vehicle' && c.opts.method === 'POST');
  ok(!!retried, 'the original save is retried automatically after a successful reverify');
  if (retried) {
    const body = JSON.parse(retried.opts.body);
    ok(body.vin === 'THROTTLED-EDIT', 'the retried save still carries the edit that triggered the reverify prompt');
  }
  await submitPromise;

  process.exit(failed ? 1 : 0);
})();
