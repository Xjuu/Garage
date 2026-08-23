/* Boots the real dashboard scripts (same shared-scope technique as
 * ui-check.cjs) and calls the real renderVehicleSpec directly, proving the
 * "Capabilities" card is now an always-shown, editable field — pre-filled
 * with whatever's on file (or blank for an untagged vehicle, never omitted)
 * — and that clicking Save PATCHes the new capabilities endpoint with the
 * trimmed, uppercased value for the right registration.
 *
 * Usage: node tools/vehicle-capabilities-check.cjs
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

function el(id) {
  const listeners = {};
  return {
    id, textContent: '', innerHTML: '', value: '', hidden: hiddenIds.has(id),
    disabled: false, dataset: {}, style: {},
    classList: { toggle() {}, add() {}, remove() {}, contains: () => false },
    addEventListener(ev, fn) { (listeners[ev] ??= []).push(fn); },
    fire(ev, arg) { (listeners[ev] || []).forEach((fn) => fn(arg || { preventDefault() {} })); },
    setAttribute() {}, removeAttribute() {},
    appendChild() {}, removeChild() {}, remove() {}, insertAdjacentHTML() {},
    scrollIntoView() {}, focus() {}, blur() {},
    querySelectorAll: () => [], querySelector: () => null, closest: () => null,
    isConnected: true, clientWidth: 1200,
  };
}

// A stand-in whose innerHTML setter actually parses out the two elements
// renderVehicleSpec wires up afterwards (#veh-capabilities-input/-save), so
// the handlers it attaches land on the same objects the assertions check —
// exactly what the real DOM does, that a static el() object can't.
function makeSpecContainer() {
  const obj = el('veh-spec');
  Object.defineProperty(obj, 'innerHTML', {
    get() { return this._html || ''; },
    set(html) {
      this._html = html;
      if (html.includes('id="veh-capabilities-input"')) {
        const m = html.match(/id="veh-capabilities-input" value="([^"]*)"/);
        store['veh-capabilities-input'] = Object.assign(el('veh-capabilities-input'), { value: m ? m[1] : '' });
        store['veh-capabilities-save'] = el('veh-capabilities-save');
      }
    },
  });
  return obj;
}

const store = {};
ids.forEach((i) => { store[i] = el(i); });
['c-invoices', 'c-vehicles', 'c-parts', 'c-suppliers', 'c-training', 'c-exports']
  .forEach((i) => { store[i] ??= el(i); });
store['veh-spec'] = makeSpecContainer();

const fetchCalls = [];
async function fakeFetch(url, opts = {}) {
  fetchCalls.push({ url, opts });
  return { ok: true, status: 200, json: async () => ({ ok: true }) };
}

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
  setTimeout: (fn, ms) => setTimeout(fn, ms), setInterval: () => 0, clearTimeout, clearInterval() {},
  fetch: fakeFetch,
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

const wait = (ms) => new Promise((r) => setTimeout(r, ms));

let failed = false;
function ok(cond, label) {
  console.log((cond ? 'ok  ' : 'FAIL') + ' - ' + label);
  if (!cond) failed = true;
}

(async () => {
  ok(errors.length === 0, 'every dashboard script loads without throwing: ' + errors.join('; '));
  ok(typeof ctx.renderVehicleSpec === 'function', 'renderVehicleSpec is defined');

  ctx.renderVehicleSpec({ registration: 'FG21OXA', capabilities: 'F' }, '', false);
  ok(store['veh-capabilities-input'].value === 'F',
    `a tagged vehicle's input is pre-filled: ${JSON.stringify(store['veh-capabilities-input'].value)}`);

  ctx.renderVehicleSpec({ registration: 'ZZ99ZZZ', capabilities: '' }, '', false);
  ok(store['veh-spec'].innerHTML.includes('Capabilities'),
    'the Capabilities card is shown even for a vehicle nobody has tagged — not omitted');
  ok(store['veh-capabilities-input'].value === '',
    'its input is simply blank, not hidden');

  // Clicking Save PATCHes the right registration with the trimmed,
  // uppercased value — free-form text like "fgty68" typed in lowercase
  // still lands as the "FGTY68"-style code the field is meant to hold.
  store['veh-capabilities-input'].value = '  fgty68  ';
  fetchCalls.length = 0;
  store['veh-capabilities-save'].fire('click');
  await wait(10);
  const call = fetchCalls.find((c) => c.url === '/api/registry/ZZ99ZZZ/capabilities');
  ok(!!call, 'Save calls the capabilities endpoint for the right registration: ' +
    JSON.stringify(fetchCalls.map((c) => c.url)));
  ok(call && call.opts.method === 'PATCH', 'it PATCHes');
  ok(call && JSON.parse(call.opts.body).capabilities === 'FGTY68',
    `it sends the trimmed, uppercased value: ${call && call.opts.body}`);

  process.exit(failed ? 1 : 0);
})();
