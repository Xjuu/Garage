/* Boots the real repairs/app.js (the main "log a visit" page) in a vm
 * context with a fake DOM and exercises the "add this as a new
 * registration?" gate: a typed registration that matches nothing on file
 * shows the prompt instead of silently revealing the form, clicking through
 * reveals it for that registration, a registration that IS on file skips
 * the prompt entirely and prefills the vehicle-details fields from its
 * most recent visit, and picking a suggestion from the dropdown (which by
 * definition already exists) never shows the prompt either.
 *
 * Usage: node tools/repairs-app-check.cjs
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
  const classes = new Set();
  return {
    dataset: { value },
    classList: {
      add: (c) => classes.add(c), remove: (c) => classes.delete(c),
      toggle: (c, on) => { (on ?? !classes.has(c)) ? classes.add(c) : classes.delete(c); },
      contains: (c) => classes.has(c),
    },
    addEventListener(ev, fn) { (listeners[ev] ??= []).push(fn); },
    fire(ev, arg) { (listeners[ev] || []).forEach((fn) => fn(arg)); },
    click() { this.fire('click'); },
  };
}

function makeEl(id, children = []) {
  const listeners = {};
  const classes = new Set();
  return {
    id, textContent: '', innerHTML: '', value: '', hidden: hiddenIds.has(id),
    disabled: false, dataset: {}, style: {}, children,
    classList: {
      add: (c) => classes.add(c), remove: (c) => classes.delete(c),
      toggle: (c, on) => { (on ?? !classes.has(c)) ? classes.add(c) : classes.delete(c); },
      contains: (c) => classes.has(c),
    },
    addEventListener(ev, fn) { (listeners[ev] ??= []).push(fn); },
    fire(ev, arg) { (listeners[ev] || []).forEach((fn) => fn(arg || { preventDefault() {} })); },
    click() { this.fire('click'); },
    focus() { this.fire('focus'); },
    select() {},
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
const history = {
  AB12CDE: [
    { service_date: '2026-01-21', service_type: 'full', service_type_other: '', mileage: 99700,
      timing_belt_changed: false, description: '', vin: 'TMBKW7NPXM7080118', make: 'Skoda', model: 'Superb',
      colour: 'Blue', cylinder_capacity: '1395CC', spare_keys: 'YES/2', fuel_type: 'Petrol/Hybrid',
      engine_size: '', engine_number: 'DGEB', tyre_size: '', radio_code: '', oil_amount: '' },
  ],
};

async function fakeFetch(url) {
  fetchCalls.push({ url });
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

(async () => {
  ok(errors.length === 0, 'app.js loads without throwing: ' + errors.join('; '));

  // ── an unrecognized registration prompts instead of revealing the form ──
  store['reg-input'].value = 'NEW99REG';
  store['reg-input'].fire('input');
  await wait(250); // checkReg is debounced 200ms, same as search
  ok(store['form'].hidden === true, 'a registration matching nothing keeps the form hidden');
  ok(store['history-section'].hidden === true, 'and keeps the history section hidden too');
  ok(store['new-reg-prompt'].hidden === false, 'and shows the "add as new?" prompt instead');
  ok(store['new-reg-text'].textContent.includes('NEW99REG'), 'the prompt names the registration: ' + store['new-reg-text'].textContent);

  store['new-reg-add'].click();
  await wait(10);
  ok(store['new-reg-prompt'].hidden === true, 'clicking "Add new registration" dismisses the prompt');
  ok(store['history-section'].hidden === false, 'and reveals the (empty) history section');
  ok(store['history-list'].innerHTML.includes('first visit'), 'which explains this will be the first visit');
  ok(store['form'].hidden === false, 'and reveals the form to log it');

  // ── a known registration skips the prompt and prefills from history ──
  fetchCalls.length = 0;
  store['reg-input'].value = 'AB12CDE';
  store['reg-input'].fire('input');
  await wait(250);
  ok(store['new-reg-prompt'].hidden === true, 'a registration that exists never shows the "add as new?" prompt');
  ok(store['form'].hidden === false, 'and reveals the form directly');
  ok(store['vin'].value === 'TMBKW7NPXM7080118', 'VIN prefilled from the most recent visit: got ' + store['vin'].value);
  ok(store['colour'].value === 'Blue', 'Colour prefilled: got ' + store['colour'].value);
  ok(store['spec-prefill-note'].hidden === false, 'the prefill note explains where the values came from');

  // ── picking a suggestion from the dropdown also skips the prompt ──
  fetchCalls.length = 0;
  store['reg-input'].value = '';
  store['reg-input'].fire('focus'); // browse-all
  await wait(10);
  const suggestion = { dataset: { reg: 'AB12CDE' }, listeners: {}, addEventListener(ev, fn) { this.listeners[ev] = fn; } };
  store['reg-results'].querySelectorAll = () => [suggestion];
  store['reg-input'].value = 'AB';
  store['reg-input'].fire('input');
  await wait(250);
  suggestion.listeners.click();
  await wait(10);
  ok(store['new-reg-prompt'].hidden === true, 'picking a known vehicle from the list never shows the "add as new?" prompt');
  ok(store['form'].hidden === false, 'and reveals the form for it');

  process.exit(failed ? 1 : 0);
})();
