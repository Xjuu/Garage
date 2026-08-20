/* Boots the real parts-counter worker script (internal/web/assets/parts/app.js)
 * in a vm context with a fake DOM, then exercises: browse-on-focus showing a
 * full list with no query typed, typed search narrowing it, the step trail
 * reflecting which of part/vehicle/quantity is current, the quantity
 * stepper's +/- buttons, and logging a take resetting everything and
 * refocusing part-search (so the next item is immediately browsable too).
 *
 * Usage: node tools/parts-app-check.cjs
 * Exits non-zero if any check fails or the loaded script throws.
 */

'use strict';

const fs = require('fs');
const vm = require('vm');
const path = require('path');

const ASSETS = path.join(__dirname, '..', 'internal', 'web', 'assets', 'parts');

const html = fs.readFileSync(path.join(ASSETS, 'index.html'), 'utf8');
const ids = [...html.matchAll(/id="([^"]+)"/g)].map((m) => m[1]);
const hiddenIds = new Set([...html.matchAll(/id="([^"]+)"[^>]*\shidden/g)].map((m) => m[1]));

function makeEl(id) {
  const listeners = {};
  const classes = new Set();
  return {
    id, textContent: '', innerHTML: '', value: '', hidden: hiddenIds.has(id),
    dataset: {}, style: {}, disabled: false,
    classList: {
      add: (c) => classes.add(c),
      remove: (c) => classes.delete(c),
      toggle: (c, on) => { (on ?? !classes.has(c)) ? classes.add(c) : classes.delete(c); },
      contains: (c) => classes.has(c),
    },
    addEventListener(ev, fn) { (listeners[ev] ??= []).push(fn); },
    fire(ev, arg) { (listeners[ev] || []).forEach((fn) => fn(arg || {})); },
    click() { this.fire('click'); },
    focus() { this.fire('focus'); },
    select() {},
    setAttribute() {}, removeAttribute() {},
    appendChild() {}, removeChild() {}, remove() {},
    querySelectorAll: () => [], querySelector: () => null,
  };
}

const store = {};
ids.forEach((i) => { store[i] = makeEl(i); });

// The three step-trail markers app.js looks up by class, not id — set up
// separately from the id-keyed store the same way the real DOM would
// resolve document.querySelectorAll('.pc-trail-step').
const trailSteps = ['part', 'vehicle', 'qty'].map((s) => {
  const e = makeEl('trail-' + s);
  e.dataset.step = s;
  return e;
});

const fetchCalls = [];
const parts = [
  { part_number: 'JRP308W', description: 'Number plate fixing screw', stock: 7 },
  { part_number: 'BRK-22', description: 'Brake pad set', stock: 0 },
];
const vehicles = ['AB12CDE', 'XY99ZZZ'];

async function fakeFetch(url, opts = {}) {
  fetchCalls.push({ url, opts });
  if (url.startsWith('/api/parts/search-parts')) {
    const q = new URL('http://x' + url).searchParams.get('q') || '';
    const rows = q ? parts.filter((p) => p.part_number.includes(q.toUpperCase()) || p.description.includes(q)) : parts;
    return { ok: true, status: 200, json: async () => rows };
  }
  if (url.startsWith('/api/parts/search-vehicles')) {
    const q = new URL('http://x' + url).searchParams.get('q') || '';
    const rows = q ? vehicles.filter((v) => v.includes(q.toUpperCase())) : vehicles;
    return { ok: true, status: 200, json: async () => rows };
  }
  if (url.startsWith('/api/parts/take')) {
    return { ok: true, status: 200, json: async () => ({ part_number: 'JRP308W', description: '', stock: 4 }) };
  }
  if (url.startsWith('/api/parts/logout')) {
    return { ok: true, status: 200, json: async () => ({ ok: true }) };
  }
  return { ok: true, status: 200, json: async () => ({}) };
}

const errors = [];
const ctx = vm.createContext({
  console,
  document: {
    getElementById: (id) => store[id] || null,
    querySelectorAll: (sel) => (sel === '.pc-trail-step' ? trailSteps : []),
    querySelector: () => null,
    createElement: () => makeEl('tmp'),
    cookie: 'goldstar_parts_csrf=test-csrf-token',
  },
  location: { href: '' },
  setTimeout, clearTimeout, // real timers: debounce is genuinely exercised, not stubbed away
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

  // ── initial load: part-search auto-focuses, which browses everything ──
  await wait(10);
  let last = fetchCalls[fetchCalls.length - 1];
  ok(!!last && last.url === '/api/parts/search-parts?q=&limit=60',
    'focusing empty part search browses everything (got: ' + (last && last.url) + ')');
  ok(store['part-results-label'].hidden === false && store['part-results-label'].textContent.includes('All parts (2)'),
    'browse mode shows a count label: ' + store['part-results-label'].textContent);
  ok(store['part-results'].innerHTML.includes('JRP308W') && store['part-results'].innerHTML.includes('BRK-22'),
    'browse mode lists every part, not just some');

  // ── typing narrows the list without the browse limit ──
  fetchCalls.length = 0;
  store['part-search'].value = 'JRP';
  store['part-search'].fire('input');
  await wait(300); // let the 200ms debounce actually fire
  last = fetchCalls[fetchCalls.length - 1];
  ok(!!last && last.url === '/api/parts/search-parts?q=JRP', 'typing searches by the typed text, no browse limit: ' + (last && last.url));
  ok(store['part-results'].innerHTML.includes('JRP308W') && !store['part-results'].innerHTML.includes('BRK-22'),
    'typed search actually narrows the results shown');

  // ── picking a part moves the trail forward ──
  const partBtn = { dataset: { part: 'JRP308W' }, listeners: {}, addEventListener(ev, fn) { this.listeners[ev] = fn; } };
  // Re-render happened via innerHTML string above; call pickPart's own wiring by
  // re-triggering the search so the harness's button stub gets the real click
  // handler app.js attached, the same way querySelectorAll('button[data-part]')
  // would find it in a real DOM.
  store['part-results'].querySelectorAll = () => [partBtn];
  store['part-search'].value = 'JRP';
  store['part-search'].fire('input');
  await wait(300);
  partBtn.listeners.click();

  ok(store['picked-part'].hidden === false, 'picking a part reveals the picked-part panel');
  ok(store['step-part'].hidden === true, 'the part search step hides once a part is picked');
  ok(trailSteps.find((s) => s.dataset.step === 'part').classList.contains('done'),
    'trail marks the part step done once picked');
  ok(trailSteps.find((s) => s.dataset.step === 'vehicle').classList.contains('active'),
    'trail marks the vehicle step active next');

  // Focusing the part gave way to the vehicle step, which auto-focuses and
  // should have fired its own browse-everything fetch.
  await wait(10);
  last = fetchCalls[fetchCalls.length - 1];
  ok(!!last && last.url === '/api/parts/search-vehicles?q=&limit=60',
    'moving to the vehicle step browses every vehicle automatically: ' + (last && last.url));

  // ── picking a vehicle ──
  const vehBtn = { dataset: { reg: 'AB12CDE' }, listeners: {}, addEventListener(ev, fn) { this.listeners[ev] = fn; } };
  store['vehicle-results'].querySelectorAll = () => [vehBtn];
  store['vehicle-search'].value = '';
  store['vehicle-search'].fire('focus');
  await wait(10);
  vehBtn.listeners.click();
  ok(store['picked-vehicle-reg'].textContent === 'AB12CDE', 'picking a vehicle records its registration');
  ok(trailSteps.find((s) => s.dataset.step === 'vehicle').classList.contains('done'),
    'trail marks the vehicle step done once picked');
  ok(trailSteps.find((s) => s.dataset.step === 'qty').classList.contains('active'),
    'trail marks the quantity step active last');
  ok(store['step-qty'].hidden === false, 'the quantity step appears once both part and vehicle are picked');

  // ── quantity stepper ──
  store['qty'].value = '1';
  store['qty-plus'].click();
  store['qty-plus'].click();
  ok(store['qty'].value === 3, 'qty-plus increments by one twice: got ' + store['qty'].value);
  store['qty-minus'].click();
  ok(store['qty'].value === 2, 'qty-minus decrements by one: got ' + store['qty'].value);
  store['qty'].value = '0.5';
  store['qty-minus'].click();
  ok(Number(store['qty'].value) === 0.01, 'qty-minus floors at 0.01 rather than going to or below zero: got ' + store['qty'].value);

  // ── logging it ──
  store['qty'].value = '2';
  fetchCalls.length = 0;
  await store['btn-log'].fire('click');
  await wait(10);
  const takeCall = fetchCalls.find((c) => c.url === '/api/parts/take');
  ok(!!takeCall, 'logging posts to /api/parts/take');
  if (takeCall) {
    const body = JSON.parse(takeCall.opts.body);
    ok(body.part_number === 'JRP308W' && body.vehicle_reg === 'AB12CDE' && body.quantity === 2,
      'the take posts the picked part, vehicle and quantity: ' + takeCall.opts.body);
    ok(takeCall.opts.headers['X-CSRF-Token'] === 'test-csrf-token',
      'the take carries the CSRF token read from the cookie');
  }
  ok(store['picked-part'].hidden === true && store['picked-vehicle'].hidden === true,
    'a successful log resets back to picking a new part');

  process.exit(failed ? 1 : 0);
})();
