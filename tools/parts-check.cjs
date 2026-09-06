/* Boots the real parts-store app.js in a vm context with a fake DOM and
 * drives the counter the way a barcode scanner does: a code typed into the
 * scan field followed by Return. Covers the known-barcode path, the
 * unknown-barcode path (which offers to add the part rather than erroring),
 * taking stock off the shelf for a car, a fitment refusal being kept on
 * screen rather than flashed in a toast, the stocktake correction working
 * out its own delta, and the read-only guard.
 *
 * Usage: node tools/parts-check.cjs
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
    disabled: false, dataset: {}, style: {},
    classList: {
      add: (c) => classes.add(c), remove: (c) => classes.delete(c),
      toggle: (c, on) => { on ? classes.add(c) : classes.delete(c); },
      contains: (c) => classes.has(c),
    },
    addEventListener(ev, fn) { (listeners[ev] ??= []).push(fn); },
    fire(ev, arg) { (listeners[ev] || []).forEach((fn) => fn(arg || { preventDefault() {} })); },
    click() { this.fire('click'); },
    setAttribute() {}, removeAttribute() {}, appendChild() {}, remove() {},
    focus() {}, select() {},
  };
}

const store = {};
ids.forEach((i) => { store[i] = makeEl(i); });
const body = makeEl('body');

const PADS = {
  id: 3, barcode: '5012345678900', part_number: 'BP-1234',
  description: 'Front brake pads', quantity: 10, min_quantity: 2,
  unit_cost: 24.5, location: 'A3', fits_make: 'Toyota', fits_model: 'Corolla', low: false,
};

let nextAdjust = null; // set per-test: either the updated part or an error
const fetchCalls = [];

async function fakeFetch(url, opts = {}) {
  fetchCalls.push({ url, opts, body: opts.body ? JSON.parse(opts.body) : undefined });
  const json = (v, status = 200) => ({ ok: status < 400, status, json: async () => v });

  if (url.startsWith('/api/stock/overview')) {
    return json({ lines: 4, units: 31, low_lines: 1, value: 512.5, moved_today: 6 });
  }
  if (url.startsWith('/api/stock/scan')) {
    const code = decodeURIComponent(url.split('barcode=')[1] || '');
    if (code === PADS.barcode) return json(PADS);
    return json({ error: 'that barcode is not on file yet' }, 404);
  }
  if (url.includes('/movements')) return json([]);
  if (url.includes('/adjust')) {
    if (nextAdjust && nextAdjust.error) {
      return json({ error: nextAdjust.error }, nextAdjust.status || 400);
    }
    return json(nextAdjust || { ...PADS, quantity: 9 });
  }
  if (url.startsWith('/api/stock/parts')) return json([]);
  if (url.startsWith('/api/stock/movements')) return json([]);
  return json({ ok: true });
}

const errors = [];
const ctx = vm.createContext({
  console,
  document: {
    getElementById: (id) => store[id] || null,
    querySelectorAll: () => [],
    createElement: () => makeEl('tmp'),
    addEventListener() {},
    body,
    cookie: 'goldstar_csrf=test-csrf-token',
  },
  location: { href: '' },
  setTimeout, clearTimeout,
  fetch: fakeFetch,
  Math, JSON, Object, Array, Number, String, Boolean, Date, Set, Map, Promise, Error,
  Intl, encodeURIComponent, decodeURIComponent,
});
ctx.window = ctx; ctx.globalThis = ctx;

try {
  vm.runInContext(fs.readFileSync(path.join(ASSETS, 'app.js'), 'utf8'), ctx, { filename: 'parts/app.js' });
} catch (e) {
  errors.push('threw while loading: ' + e.message);
}

const wait = (ms) => new Promise((r) => setTimeout(r, ms));
let failed = false;
function ok(cond, label) {
  console.log((cond ? 'ok  ' : 'FAIL') + ' - ' + label);
  if (!cond) failed = true;
}

/** What a scanner does: type the code into the focused field and press
    Return. Nothing else on the page is touched. */
async function scanBarcode(code) {
  store['scan'].value = code;
  store['scan'].fire('keydown', { key: 'Enter', preventDefault() {} });
  await wait(10);
}

(async () => {
  ok(errors.length === 0, 'parts app.js loads without throwing: ' + errors.join('; '));
  await wait(20);

  ok(store['stock-tiles'].innerHTML.includes('Below reorder'),
    'the counter opens with the stock tiles');
  ok(/tile alert[\s\S]{0,120}Below reorder/.test(store['stock-tiles'].innerHTML),
    'a non-zero low count gets the alert treatment');

  // ── scanning a known barcode ─────────────────────────────────────────
  await scanBarcode('5012345678900');
  ok(store['scanned'].hidden === false, 'a known barcode shows the part');
  ok(store['new-part'].hidden === true, 'and does not offer to add it again');
  ok(store['sc-part'].textContent === 'BP-1234', 'the part number leads');
  ok(store['sc-qty'].textContent === '10', 'the shelf quantity is shown as a count, not money');
  ok(store['sc-meta'].innerHTML.includes('Fits Toyota Corolla only'),
    'the fitment restriction is stated up front: ' + store['sc-meta'].innerHTML);

  // ── an unknown barcode is how a new line starts ──────────────────────
  await scanBarcode('9999999999999');
  ok(store['new-part'].hidden === false, 'an unknown barcode offers to add the part');
  ok(store['scanned'].hidden === true, 'and hides the previous part');
  ok(store['np-barcode'].value === '9999999999999',
    'with the scanned code already filled in: ' + store['np-barcode'].value);

  // ── taking stock off the shelf for a car ─────────────────────────────
  await scanBarcode('5012345678900');
  store['ad-qty'].value = '2';
  store['ad-reg'].value = 'AB12 CDE';
  store['ad-note'].value = 'nearside';
  nextAdjust = { ...PADS, quantity: 8 };
  fetchCalls.length = 0;
  store['ad-take'].fire('click');
  await wait(10);
  const take = fetchCalls.find((c) => c.url.includes('/adjust'));
  ok(!!take, 'taking stock posts an adjustment');
  ok(take && take.body.delta === -2,
    'as a negative delta of what was typed: ' + JSON.stringify(take && take.body));
  ok(take && take.body.reason === 'used' && take.body.vehicle_reg === 'AB12 CDE',
    'with the reason and the car it is going on');
  ok(take && take.opts.headers['X-CSRF-Token'] === 'test-csrf-token', 'and the CSRF token');
  ok(store['sc-qty'].textContent === '8', 'the shelf figure updates to what came back');

  // ── putting stock back ───────────────────────────────────────────────
  store['ad-qty'].value = '5';
  nextAdjust = { ...PADS, quantity: 13 };
  fetchCalls.length = 0;
  store['ad-add'].fire('click');
  await wait(10);
  const put = fetchCalls.find((c) => c.url.includes('/adjust'));
  ok(put && put.body.delta === 5 && put.body.reason === 'received',
    'putting stock back is a positive delta marked received: ' + JSON.stringify(put && put.body));

  // ── a stocktake correction works out its own delta ───────────────────
  // The shelf says 13; someone counted 9, so the movement is -4 rather
  // than the 9 they typed.
  store['ad-qty'].value = '9';
  nextAdjust = { ...PADS, quantity: 9 };
  fetchCalls.length = 0;
  store['ad-correct'].fire('click');
  await wait(10);
  const corr = fetchCalls.find((c) => c.url.includes('/adjust'));
  ok(corr && corr.body.delta === -4 && corr.body.reason === 'correction',
    'a correction posts the difference, not the counted figure: ' + JSON.stringify(corr && corr.body));

  // Correcting to the number already on the shelf is not a movement.
  store['ad-qty'].value = '9';
  fetchCalls.length = 0;
  store['ad-correct'].fire('click');
  await wait(10);
  ok(!fetchCalls.some((c) => c.url.includes('/adjust')),
    'correcting to the figure already shown sends nothing');

  // ── a fitment refusal stays on screen ────────────────────────────────
  store['ad-qty'].value = '1';
  store['ad-reg'].value = 'CC33CCC';
  nextAdjust = { error: 'this part only fits Toyota Corolla — CC33CCC is a Skoda Octavia', status: 409 };
  store['ad-take'].fire('click');
  await wait(10);
  ok(/only fits Toyota Corolla/.test(store['ad-err'].textContent),
    'a part refused for the wrong car says so, in place: ' + JSON.stringify(store['ad-err'].textContent));
  ok(store['sc-qty'].textContent === '9', 'and nothing moved');

  // ── read-only accounts ───────────────────────────────────────────────
  body.dataset.readonly = 'true';
  fetchCalls.length = 0;
  let threw = null;
  try {
    await ctx.api('/api/stock/parts/3/adjust', { method: 'POST', json: { delta: -1 } });
  } catch (e) { threw = e; }
  ok(threw !== null && /view-only/i.test(threw.message),
    'a read-only account cannot move stock: ' + (threw && threw.message));
  ok(fetchCalls.length === 0, 'and the request never leaves the browser');
  fetchCalls.length = 0;
  await ctx.api('/api/stock/parts');
  ok(fetchCalls.length === 1, 'but it can still look at the shelf');

  process.exit(failed ? 1 : 0);
})();
