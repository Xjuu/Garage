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

// pinbox.js is backed by one real input covering the whole row (see its
// own comment for why) plus 6 display-only boxes it writes digits into —
// modelled here as a fake container exposing exactly the two lookups
// setupPinBoxes performs: querySelectorAll('.pin-box') for the display
// boxes, querySelector('.pin-hidden-input') for the one real field.
function makeDisplayBox() {
  const classes = new Set();
  return {
    textContent: '',
    classList: {
      add: (c) => classes.add(c), remove: (c) => classes.delete(c),
      toggle: (c, on) => { (on ?? !classes.has(c)) ? classes.add(c) : classes.delete(c); },
      contains: (c) => classes.has(c),
    },
  };
}
function makeHiddenInput() {
  const listeners = {};
  return {
    value: '',
    addEventListener(ev, fn) { (listeners[ev] ??= []).push(fn); },
    fire(ev, arg) { (listeners[ev] || []).forEach((fn) => fn(arg)); },
    focus() { this.focused = true; },
  };
}
function makePinGroup() {
  const boxes = Array.from({ length: 6 }, makeDisplayBox);
  const hidden = makeHiddenInput();
  return {
    boxes, hidden,
    querySelectorAll(sel) { return sel === '.pin-box' ? boxes : []; },
    querySelector(sel) { return sel === '.pin-hidden-input' ? hidden : null; },
  };
}

const store = {};
ids.forEach((i) => { store[i] = makeEl(i); });

const pinGroups = {
  'pin-boxes': makePinGroup(),
  'reverify-boxes': makePinGroup(),
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
  if (url.startsWith('/api/repairs/reg-exists')) {
    const reg = decodeURIComponent(url.split('reg=')[1] || '');
    return { ok: true, status: 200, json: async () => ({ exists: reg === 'AB12CDE' }) };
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
    getElementById: (id) => pinGroups[id] || store[id] || null,
    querySelectorAll: () => [],
    querySelector: () => null,
    createElement: () => makeEl('tmp'),
    cookie: 'goldstar_repairs_csrf=test-csrf-token',
  },
  location: { href: '' },
  setTimeout, clearTimeout,
  // A real browser API pinbox.js relies on to defer .focus() until after
  // layout — not a Node global, so stubbed the same way as the other
  // browser-only pieces below.
  requestAnimationFrame: (fn) => setTimeout(fn, 0),
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

// Types into the one real hidden input a digit at a time, the way real
// keystrokes would — appending to .value then firing 'input' — so the
// widget's own digit-filtering / completion logic runs exactly as it
// would in a browser.
function typeCode(groupId, code) {
  const { hidden } = pinGroups[groupId];
  for (const digit of code) {
    hidden.value += digit;
    hidden.fire('input');
  }
}

(async () => {
  ok(errors.length === 0, 'pinbox.js and upload.js load without throwing: ' + errors.join('; '));

  // ── pinbox widget behaviour, exercised through the sign-in group ──
  // One real input backs the whole row (see pinbox.js's own comment for
  // why, and repairs-app-check.cjs for the regression this design fixes) —
  // these checks drive it the way an actual keystroke or paste would and
  // confirm the display boxes and completion callback follow along.
  const group = pinGroups['pin-boxes'];
  let completedWith = null;
  // setupPinBoxes is declared inside the vm context (pinbox.js), not in this
  // outer Node scope — reached via the context object it was attached to,
  // the same way the other harnesses in this repo reach a page script's
  // top-level functions.
  const pinTest = ctx.setupPinBoxes('pin-boxes', (code) => { completedWith = code; });

  await wait(10); // focus is deferred a frame — see pinbox.js's own comment on why
  ok(group.hidden.focused === true, 'the real input is focused as soon as the widget is set up');

  typeCode('pin-boxes', '1122');
  ok(group.boxes[0].textContent === '1' && group.boxes[3].textContent === '2',
    'each display box mirrors the matching digit as it\'s typed');
  ok(completedWith === null, 'onComplete does not fire before all 6 digits are in');

  typeCode('pin-boxes', '33');
  ok(completedWith === '112233', 'onComplete fires with the full 6-digit code once every box is filled: got ' + completedWith);

  pinTest.clear();
  ok(group.hidden.value === '' && group.boxes.every((b) => b.textContent === ''),
    'clear() empties the input and every display box');
  await wait(10);
  ok(group.hidden.focused === true, 'clear() refocuses the input');

  // Paste support needs no special handling at all now — a paste into the
  // one real field just fires 'input' with the full value already there,
  // the same as typing it out digit by digit.
  completedWith = null;
  group.hidden.value = '998877';
  group.hidden.fire('input');
  ok(completedWith === '998877', 'pasting a 6-digit code (native paste → input) completes: got ' + completedWith);

  // Non-digit characters (a stray letter from a fumbled paste, an IME
  // artifact) are stripped rather than breaking the count.
  pinTest.clear();
  completedWith = null;
  group.hidden.value = '11a223b3';
  group.hidden.fire('input');
  ok(completedWith === '112233', 'non-digit characters are stripped before completion is checked: got ' + completedWith);

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
