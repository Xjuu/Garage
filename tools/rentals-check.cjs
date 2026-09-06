/* Boots the real rentals app.js in a vm context with a fake DOM and drives
 * the hire desk the way the counter does: a board of cars free to lend
 * beside the ones already out, "Lend it out" on a car, the short form with
 * its fussier fields folded away, bringing one back with a reading, and
 * adding a car to the loan fleet. Plus the read-only guard, which has to
 * hold here exactly as it does on the dashboard.
 *
 * Usage: node tools/rentals-check.cjs
 */

'use strict';

const fs = require('fs');
const vm = require('vm');
const path = require('path');

const ASSETS = path.join(__dirname, '..', 'internal', 'web', 'assets', 'rentals');
const html = fs.readFileSync(path.join(ASSETS, 'index.html'), 'utf8');
const ids = [...html.matchAll(/id="([^"]+)"/g)].map((m) => m[1]);
const hiddenIds = new Set([...html.matchAll(/id="([^"]+)"[^>]*\shidden/g)].map((m) => m[1]));

function makeEl(id) {
  const listeners = {};
  const classes = new Set();
  // A real <input> coerces whatever it is given to a string. Without that
  // here, a number assigned in app.js reads back as a number and every
  // comparison against what a form would actually hold is a false signal.
  let value = '';
  return {
    get value() { return value; },
    set value(v) { value = v === null || v === undefined ? '' : String(v); },
    id, textContent: '', innerHTML: '', disabled: false,
    hidden: hiddenIds.has(id), dataset: {}, style: {}, selectedOptions: [],
    classList: {
      add: (c) => classes.add(c), remove: (c) => classes.delete(c),
      toggle: (c, on) => { on ? classes.add(c) : classes.delete(c); },
      contains: (c) => classes.has(c),
    },
    addEventListener(ev, fn) { (listeners[ev] ??= []).push(fn); },
    fire(ev, arg) { (listeners[ev] || []).forEach((fn) => fn(arg || {})); },
    click() { this.fire('click'); },
    setAttribute(k, v) { this.dataset['attr_' + k] = v; },
    getAttribute: () => null, removeAttribute() {},
    appendChild() {}, remove() {}, focus() {},
  };
}

const store = {};
ids.forEach((i) => { store[i] = makeEl(i); });
const body = makeEl('body');

const yesterday = new Date(Date.now() - 86400000).toISOString().slice(0, 10);
const FREE = [
  { id: 11, registration: 'RE24NTL', make: 'Kia', model: 'Ceed', colour: 'Silver',
    mileage: 41200, daily_rate: 38, status: 'available' },
  { id: 12, registration: 'RE25NTL', make: 'Seat', model: 'Leon', colour: '',
    mileage: 0, daily_rate: 0, status: 'available' },
];
const OUT = [
  { id: 1, customer_id: 7, customer_name: 'Alex Rider', phone: '+447700900123',
    registration: 'RE21NTL', make: 'Toyota', model: 'Corolla',
    starts_on: '2026-09-01', ends_on: yesterday, status: 'out',
    courtesy_for_reg: 'AB12CDE', days: 4, total: 180 },
  { id: 2, customer_id: 8, customer_name: 'Sam Vimes', phone: '',
    registration: 'RE22NTL', make: 'Skoda', model: 'Octavia',
    starts_on: '2026-09-02', ends_on: '2099-01-01', status: 'out',
    courtesy_for_reg: '', days: 3, total: 120 },
];

const fetchCalls = [];
let failNext = null;
async function fakeFetch(url, opts = {}) {
  fetchCalls.push({ url, opts, body: opts.body ? JSON.parse(opts.body) : undefined });
  const json = (v, status = 200) => ({ ok: status < 400, status, json: async () => v });
  if (failNext && opts.method === 'POST') {
    const f = failNext; failNext = null;
    return json({ error: f.error }, f.status || 409);
  }
  if (url === '/api/rentals/overview') {
    return json({ out_now: 2, overdue: 1, upcoming: 0, due_today: 0,
      available: 2, fleet: 4, customers: 3, out_value: 300 });
  }
  if (url === '/api/rentals/board') return json({ free: FREE, out: OUT });
  if (url.startsWith('/api/rentals/customers')) {
    return json([{ id: 7, name: 'Alex Rider', phone: '+447700900123' }]);
  }
  if (url.startsWith('/api/rentals/agreements')) return json([]);
  if (url.startsWith('/api/rentals/vehicles')) return json([]);
  return json({ ok: true });
}

const errors = [];
const ctx = vm.createContext({
  console,
  document: {
    getElementById: (id) => store[id] || null,
    querySelectorAll: (sel) => {
      // Only the board's own buttons are looked up this way; the harness
      // drives them through the rendered markup instead.
      if (sel === '#tabs button' || sel === '.view') return [];
      return [];
    },
    createElement: () => makeEl('tmp'),
    addEventListener() {},
    body,
    cookie: 'goldstar_csrf=test-csrf-token',
  },
  location: { href: '' },
  setTimeout, clearTimeout,
  fetch: fakeFetch,
  Math, JSON, Object, Array, Number, String, Boolean, Date, Set, Map, Promise,
  Intl, encodeURIComponent,
});
ctx.window = ctx; ctx.globalThis = ctx;

try {
  vm.runInContext(fs.readFileSync(path.join(ASSETS, 'app.js'), 'utf8'), ctx, { filename: 'rentals/app.js' });
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
  ok(errors.length === 0, 'rentals app.js loads without throwing: ' + errors.join('; '));
  await wait(20);

  // ── the board ────────────────────────────────────────────────────────
  ok(store['board-sub'].textContent === '2 free to lend · 2 out with customers',
    'the header counts both columns: ' + JSON.stringify(store['board-sub'].textContent));

  const free = store['free-panel'].innerHTML;
  ok(free.includes('RE24NTL') && free.includes('Kia Ceed'), 'free cars list plate and model');
  ok(free.includes('Silver · 41,200 mi · £38.00/day'),
    'with colour, what is on the clock and the day rate: ' + (free.match(/Silver[^<]*/) || [''])[0]);
  ok((free.match(/data-lend=/g) || []).length === 2, 'every free car offers "Lend it out"');

  const out = store['out-panel'].innerHTML;
  ok(out.includes('Alex Rider') && out.includes('RE21NTL'),
    'the out column leads with who has the car');
  const alex = out.split('<div class="car-row').find((r) => r.includes('Alex Rider')) || '';
  const sam = out.split('<div class="car-row').find((r) => r.includes('Sam Vimes')) || '';
  ok(alex.includes('row-flag') && alex.includes('Overdue'), 'a loan past its date is flagged overdue');
  ok(!sam.includes('row-flag'), 'one still within its dates is not');
  ok(alex.includes('Courtesy for AB12CDE'),
    'a courtesy car says which car it is standing in for');
  ok(!sam.includes('Courtesy for'), 'a plain loan does not');

  // ── lending one out ──────────────────────────────────────────────────
  await ctx.openLend('11');
  await wait(10);
  ok(store['lend-modal'].classList.contains('open'), 'Lend it out opens the form');
  ok(store['lend-car'].innerHTML.includes('RE24NTL') && store['lend-car'].innerHTML.includes('Kia Ceed'),
    'which restates the car it is about');
  ok(store['l-mileage'].value === '41200',
    'with the odometer prefilled from the car: ' + store['l-mileage'].value);
  const back = new Date(Date.now() + 7 * 86400000).toISOString().slice(0, 10);
  ok(store['l-back'].value === back,
    'and a week out as the default return date: ' + store['l-back'].value);
  ok(store['l-customer'].innerHTML.includes('Alex Rider'), 'the customer list is loaded');

  // The fussy fields start folded away.
  ok(store['lend-extra'].hidden === true, 'the extra options start collapsed');
  store['lend-more'].fire('click');
  ok(store['lend-extra'].hidden === false, 'and open when asked for');
  ok(/fewer options/i.test(store['lend-more'].textContent),
    'the toggle then offers to fold them back: ' + JSON.stringify(store['lend-more'].textContent));

  // The registration only appears once this is said to be a courtesy car.
  ok(store['l-courtesy-wrap'].hidden === true, 'the courtesy registration is hidden by default');
  store['l-courtesy-yn'].value = 'yes';
  store['l-courtesy-yn'].fire('change');
  ok(store['l-courtesy-wrap'].hidden === false, 'and appears when this is a courtesy car');

  // Nothing is sent without a customer.
  fetchCalls.length = 0;
  store['l-customer'].value = '';
  await store['lend-save'].fire('click');
  await wait(10);
  ok(!fetchCalls.some((c) => c.url === '/api/rentals/lend'),
    'lending with nobody chosen never reaches the server');
  ok(/who is taking it/i.test(store['lend-err'].textContent),
    'and says what is missing: ' + JSON.stringify(store['lend-err'].textContent));

  store['l-customer'].value = '7';
  store['l-courtesy'].value = 'AB12 CDE';
  store['l-mileage'].value = '41250';
  store['l-note'].value = 'quarter tank';
  fetchCalls.length = 0;
  await store['lend-save'].fire('click');
  await wait(10);
  const lend = fetchCalls.find((c) => c.url === '/api/rentals/lend');
  ok(!!lend, 'lending posts to /api/rentals/lend');
  ok(lend && lend.body.vehicle_id === 11 && lend.body.customer_id === 7 &&
     lend.body.back_on === back && lend.body.mileage_now === 41250 &&
     lend.body.courtesy_for_reg === 'AB12 CDE' && lend.body.note === 'quarter tank',
    'with everything the counter typed: ' + JSON.stringify(lend && lend.body));
  ok(lend && lend.opts.headers['X-CSRF-Token'] === 'test-csrf-token', 'and the CSRF token');

  // A "no" answer must not smuggle a registration through.
  await ctx.openLend('11');
  await wait(10);
  store['l-customer'].value = '7';
  store['l-courtesy'].value = 'AB12 CDE';
  store['l-courtesy-yn'].value = 'no';
  fetchCalls.length = 0;
  await store['lend-save'].fire('click');
  await wait(10);
  const plain = fetchCalls.find((c) => c.url === '/api/rentals/lend');
  ok(plain && plain.body.courtesy_for_reg === '',
    'answering "just a loan" sends no courtesy registration: ' + JSON.stringify(plain && plain.body.courtesy_for_reg));

  // A refusal from the server stays on the form.
  await ctx.openLend('11');
  await wait(10);
  store['l-customer'].value = '7';
  failNext = { error: 'that car is already out or booked over those dates', status: 409 };
  await store['lend-save'].fire('click');
  await wait(10);
  ok(/already out/.test(store['lend-err'].textContent),
    'a refusal is shown on the form, not flashed away: ' + JSON.stringify(store['lend-err'].textContent));

  // ── bringing one back ────────────────────────────────────────────────
  ctx.openBack('1', 'RE21NTL · Alex Rider');
  ok(store['back-modal'].classList.contains('open'), 'It\'s back opens its own form');
  ok(store['back-car'].textContent.includes('Alex Rider'), 'naming the loan being closed');
  store['b-mileage'].value = '41999';
  fetchCalls.length = 0;
  await store['back-save'].fire('click');
  await wait(10);
  const ret = fetchCalls.find((c) => c.url === '/api/rentals/agreements/1/back');
  ok(!!ret && ret.body.mileage_in === 41999,
    'returning posts the reading it came back on: ' + JSON.stringify(ret && ret.body));

  // ── adding a loan car ────────────────────────────────────────────────
  store['btn-add-car'].fire('click');
  ok(store['car-modal'].classList.contains('open'), 'Add a loan car opens its form');
  ok(store['car-extra'].hidden === true, 'with the optional details collapsed');
  store['c-reg'].value = 'RE26NTL';
  store['c-make'].value = 'Ford';
  store['c-model'].value = 'Focus';
  store['c-status'].value = 'maintenance';
  store['c-mileage'].value = '100';
  store['c-mot'].value = '2027-03-01';
  store['c-rate'].value = '42.50';
  fetchCalls.length = 0;
  await store['car-save'].fire('click');
  await wait(10);
  const add = fetchCalls.find((c) => c.url === '/api/rentals/vehicles' && c.opts.method === 'POST');
  ok(!!add, 'saving posts a new loan car');
  ok(add && add.body.registration === 'RE26NTL' && add.body.status === 'maintenance' &&
     add.body.mileage === 100 && add.body.mot_expires === '2027-03-01' && add.body.daily_rate === 42.5,
    'with everything the form collected: ' + JSON.stringify(add && add.body));

  // ── read-only accounts ───────────────────────────────────────────────
  body.dataset.readonly = 'true';
  fetchCalls.length = 0;
  let threw = null;
  try {
    await ctx.api('/api/rentals/lend', { method: 'POST', json: { vehicle_id: 11 } });
  } catch (e) { threw = e; }
  ok(threw !== null && /view-only/i.test(threw.message),
    'a read-only account cannot lend a car out: ' + (threw && threw.message));
  ok(fetchCalls.length === 0, 'and the request never leaves the browser');
  fetchCalls.length = 0;
  await ctx.api('/api/rentals/board');
  ok(fetchCalls.length === 1, 'but it can still see the board');

  process.exit(failed ? 1 : 0);
})();
