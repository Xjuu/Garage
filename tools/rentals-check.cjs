/* Boots the real rentals app.js in a vm context with a fake DOM and drives
 * the hire desk: the Today view's tiles and "out now" table, an overdue
 * hire being flagged as such, the availability lookup that feeds the new
 * hire drawer, booking, moving a hire along (picked up / returned), and
 * the read-only guard that has to hold here exactly as it does on the
 * dashboard.
 *
 * Usage: node tools/rentals-check.cjs
 * Exits non-zero if any check fails or app.js throws while loading.
 */

'use strict';

const fs = require('fs');
const vm = require('vm');
const path = require('path');

const ASSETS = path.join(__dirname, '..', 'internal', 'web', 'assets', 'rentals');
const html = fs.readFileSync(path.join(ASSETS, 'index.html'), 'utf8');
const ids = [...html.matchAll(/id="([^"]+)"/g)].map((m) => m[1]);

function makeEl(id) {
  const listeners = {};
  const classes = new Set();
  return {
    id, textContent: '', innerHTML: '', value: '', disabled: false,
    dataset: {}, style: {}, selectedOptions: [],
    classList: {
      add: (c) => classes.add(c), remove: (c) => classes.delete(c),
      toggle: (c, on) => { on ? classes.add(c) : classes.delete(c); },
      contains: (c) => classes.has(c),
    },
    addEventListener(ev, fn) { (listeners[ev] ??= []).push(fn); },
    fire(ev, arg) { (listeners[ev] || []).forEach((fn) => fn(arg || {})); },
    click() { this.fire('click'); },
    setAttribute() {}, getAttribute: () => null, removeAttribute() {},
    appendChild() {}, remove() {}, focus() {},
  };
}

const store = {};
ids.forEach((i) => { store[i] = makeEl(i); });
const body = makeEl('body');

// Two hires: one that ended yesterday and is still out (overdue), one due
// back well in the future.
const yesterday = new Date(Date.now() - 86400000).toISOString().slice(0, 10);
const nextYear = '2099-01-01';

const OUT = [
  { id: 1, customer_id: 7, customer_name: 'Alex Rider', phone: '+447700900123',
    registration: 'RE21NTL', make: 'Toyota', model: 'Corolla',
    starts_on: '2026-09-01', ends_on: yesterday, status: 'out', days: 4, total: 180 },
  { id: 2, customer_id: 8, customer_name: 'Sam Vimes', phone: '',
    registration: 'RE22NTL', make: 'Skoda', model: 'Octavia',
    starts_on: '2026-09-02', ends_on: nextYear, status: 'out', days: 3, total: 120 },
];
const BOOKED = [
  { id: 3, customer_id: 9, customer_name: 'Rincewind', phone: '',
    registration: 'RE23NTL', make: 'Ford', model: 'Focus',
    starts_on: nextYear, ends_on: nextYear, status: 'booked', days: 1, total: 40 },
];
const OVERVIEW = {
  out_now: 2, overdue: 1, upcoming: 1, due_today: 0,
  available: 3, fleet: 5, customers: 4, out_value: 300,
};
const AVAILABLE = [
  { id: 11, registration: 'RE24NTL', make: 'Kia', model: 'Ceed', daily_rate: 38 },
  { id: 12, registration: 'RE25NTL', make: 'Seat', model: 'Leon', daily_rate: 42 },
];

const fetchCalls = [];
async function fakeFetch(url, opts = {}) {
  fetchCalls.push({ url, opts, body: opts.body ? JSON.parse(opts.body) : undefined });
  const json = (v) => ({ ok: true, status: 200, json: async () => v });
  if (url === '/api/rentals/overview') return json(OVERVIEW);
  if (url === '/api/rentals/agreements?status=out') return json(OUT);
  if (url === '/api/rentals/agreements?status=booked') return json(BOOKED);
  if (url.startsWith('/api/rentals/agreements')) return json([...OUT, ...BOOKED]);
  if (url.startsWith('/api/rentals/available')) return json(AVAILABLE);
  if (url.startsWith('/api/rentals/customers')) {
    return json([{ id: 7, name: 'Alex Rider', phone: '+447700900123' }]);
  }
  if (url.startsWith('/api/rentals/vehicles')) return json([]);
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
  await wait(20); // show('today') fires loadToday() at the end of the file

  // ── Today ────────────────────────────────────────────────────────────
  const tiles = store['today-tiles'].innerHTML;
  ok(tiles.includes('Out now') && tiles.includes('Overdue') && tiles.includes('Free to hire'),
    'the Today tiles render');
  ok(/tile alert[\s\S]{0,120}Overdue/.test(tiles),
    'a non-zero overdue count gets the alert treatment');
  ok(tiles.includes('300.00'), 'the value on hire is money-formatted');

  const outRows = store['out-rows'].innerHTML;
  ok(outRows.includes('Alex Rider') && outRows.includes('RE21NTL'),
    'the out-now table lists customer and car');
  ok(outRows.includes('+447700900123'), 'it shows the number to ring them on');
  const alexRow = outRows.split('<tr').find((r) => r.includes('Alex Rider')) || '';
  const samRow = outRows.split('<tr').find((r) => r.includes('Sam Vimes')) || '';
  ok(alexRow.includes('row-flag') && alexRow.includes('Overdue'),
    'a hire whose end date has passed is flagged overdue');
  ok(!samRow.includes('row-flag'),
    'a hire still within its dates is not flagged');
  ok(outRows.includes('data-return="1"'), 'each out hire offers a "Returned" action');
  ok(store['upcoming-rows'].innerHTML.includes('data-pickup="3"'),
    'a booked hire offers a "Picked up" action');

  // ── moving a hire along ──────────────────────────────────────────────
  fetchCalls.length = 0;
  await ctx.setStatus(2, 'returned', 'Marked as returned');
  const patch = fetchCalls.find((c) => c.url === '/api/rentals/agreements/2/status');
  ok(!!patch, 'marking a hire returned PATCHes that hire: ' +
    JSON.stringify(fetchCalls.map((c) => c.url)));
  ok(patch && patch.opts.method === 'PATCH' && patch.body.status === 'returned',
    'with the new status in the body');
  ok(patch && patch.opts.headers['X-CSRF-Token'] === 'test-csrf-token',
    'and the CSRF token, same as every other mutating call in this system');

  // ── availability feeding the new-hire drawer ─────────────────────────
  store['h-from'].value = '2026-10-01';
  store['h-to'].value = '2026-10-03';
  fetchCalls.length = 0;
  await ctx.refreshAvailable();
  const avail = fetchCalls.find((c) => c.url.startsWith('/api/rentals/available'));
  ok(!!avail && avail.url.includes('from=2026-10-01') && avail.url.includes('to=2026-10-03'),
    'picking dates asks the server what is free over exactly those dates: ' + (avail && avail.url));
  ok(store['h-car'].innerHTML.includes('RE24NTL') && store['h-car'].innerHTML.includes('RE25NTL'),
    'the free cars become the options you can pick');
  ok(store['h-dates-hint'].textContent.includes('3 days') &&
     store['h-dates-hint'].textContent.includes('2 cars'),
    'the hint counts days inclusively and cars available: ' +
      JSON.stringify(store['h-dates-hint'].textContent));

  // An end date before the start is caught before anything is requested.
  store['h-to'].value = '2026-09-01';
  fetchCalls.length = 0;
  await ctx.refreshAvailable();
  ok(fetchCalls.length === 0, 'a backwards date range is not sent to the server at all');
  ok(/before the start/i.test(store['h-dates-hint'].textContent),
    'and says so: ' + JSON.stringify(store['h-dates-hint'].textContent));

  // ── booking ──────────────────────────────────────────────────────────
  store['h-from'].value = '2026-10-01';
  store['h-to'].value = '2026-10-03';
  store['h-car'].value = '11';
  store['h-customer'].value = '7';
  store['h-notes'].value = 'front bumper damage noted';
  fetchCalls.length = 0;
  await store['h-save'].fire('click');
  await wait(10);
  const booking = fetchCalls.find((c) => c.url === '/api/rentals/agreements' && c.opts.method === 'POST');
  ok(!!booking, 'Book it posts a new agreement');
  ok(booking && booking.body.vehicle_id === 11 && booking.body.customer_id === 7 &&
     booking.body.starts_on === '2026-10-01' && booking.body.ends_on === '2026-10-03',
    'with the chosen car, customer and dates: ' + JSON.stringify(booking && booking.body));

  // Missing a car or a customer is caught in the drawer, not by the server.
  store['h-car'].value = '';
  fetchCalls.length = 0;
  await store['h-save'].fire('click');
  await wait(10);
  ok(!fetchCalls.some((c) => c.opts.method === 'POST'),
    'booking with no car chosen never reaches the server');
  ok(/pick a car/i.test(store['h-err'].textContent),
    'and says which field is missing: ' + JSON.stringify(store['h-err'].textContent));

  // ── read-only accounts ───────────────────────────────────────────────
  body.dataset.readonly = 'true';
  fetchCalls.length = 0;
  let threw = null;
  try {
    await ctx.api('/api/rentals/agreements', { method: 'POST', json: { vehicle_id: 1 } });
  } catch (e) { threw = e; }
  ok(threw !== null && /view-only/i.test(threw.message),
    'a read-only account cannot book: ' + (threw && threw.message));
  ok(fetchCalls.length === 0, 'and the request never leaves the browser');
  fetchCalls.length = 0;
  await ctx.api('/api/rentals/agreements');
  ok(fetchCalls.length === 1, 'but it can still read — "view only" means views keep working');
  fetchCalls.length = 0;
  await ctx.api('/api/logout', { method: 'POST' });
  ok(fetchCalls.length === 1, 'and it can still sign itself out');

  process.exit(failed ? 1 : 0);
})();
