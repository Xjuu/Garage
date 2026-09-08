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

// One hire with every kind of money on it: five days at 45, insurance,
// three days late at 25 not yet charged, and extras.
const HIRE = {
  id: 1, customer_id: 7, customer_name: 'Alex Rider', phone: '+447700900123',
  registration: 'RE21NTL', make: 'Toyota', model: 'Corolla',
  starts_on: '2026-09-01', ends_on: yesterday, status: 'out',
  courtesy_for_reg: 'AB12CDE', days: 5, daily_rate: 45, total: 225,
  insurance_per_day: 12, insurance: 60,
  late_fee_per_day: 25, days_late: 3, late_fee_due: 75, late_fee: 0,
  extra_charges: 40, extra_note: 'returned empty',
  deposit: 150, deposit_returned: false, paid: false, chargeable: 325,
};
const MESSAGES = [
  { id: 2, customer_id: 7, body: 'second try', status: 'failed',
    error: 'unverified number', sent_by: 'klon', created_at: '2026-09-08 10:00' },
  { id: 1, customer_id: 7, body: 'your car is ready', status: 'sent',
    sent_by: 'klon', created_at: '2026-09-08 09:00' },
];
const DOCS = [
  { id: 5, kind: 'licence', filename: 'licence.jpg', bytes: 204800,
    created_at: '2026-09-08', uploaded_by: 'klon' },
];

const fetchCalls = [];
let failNext = null;
async function fakeFetch(url, opts = {}) {
  fetchCalls.push({
    url, opts,
    body: typeof opts.body === 'string' ? JSON.parse(opts.body) : undefined,
  });
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
  if (url === '/api/rentals/stats') {
    return json({
      on_hire_now: 300, billed_all_time: 1200, collected_all_time: 900,
      outstanding_now: 300, billed_this_month: 450, hires_this_month: 2,
      avg_hire_days: 4.5, avg_hire_value: 180, utilisation_pct: 50,
      top_cars: [{ vehicle_id: 11, registration: 'RE24NTL', make: 'Kia', model: 'Ceed',
        hires: 3, days: 12, billed: 456 }],
    });
  }
  if (url === '/api/rentals/courtesy') return json([OUT[0]]);
  if (url === '/api/rentals/settings') {
    return json({ rental_insurance_per_day: '12', rental_late_fee_per_day: '25',
      rental_deposit_default: '150' });
  }
  if (/^\/api\/rentals\/agreements\/\d+$/.test(url)) return json(HIRE);
  if (url.startsWith('/api/rentals/messages')) {
    if (opts.method === 'POST') return json({ ok: true, sent_to: '+447700900123' });
    return json(MESSAGES);
  }
  if (url.startsWith('/api/rentals/documents')) {
    if (opts.method === 'DELETE') return json({ ok: true });
    return json(DOCS);
  }
  if (url.startsWith('/api/rentals/agreements')) return json(OUT);
  if (url.startsWith('/api/rentals/vehicles')) return json([]);
  return json({ ok: true });
}

// A vm context has no FormData, and without one the document upload path
// throws a ReferenceError that kills the run rather than failing a check —
// so the browser API it depends on is modelled rather than left absent.
class FakeFormData {
  constructor() { this.fields = {}; }
  append(k, v) { this.fields[k] = v; }
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
  navigator: { clipboard: { writeText: async () => {} } },
  FormData: FakeFormData,
  open() {},
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

  // ── every tab actually opens ─────────────────────────────────────────
  // The bug this exists for: wireAgreementButtons was deleted with the old
  // Today view while the Hires tab still called it. app.js loaded fine, so
  // nothing caught it until someone clicked Hires in production. Loading a
  // file is not the same as running its screens.
  //
  // The loaders are reached by name rather than through viewLoaders: that
  // is a `const`, and a vm context only exposes function declarations, not
  // const bindings.
  body.dataset.readonly = '';
  for (const [name, fn] of [
    ['Today', ctx.loadToday], ['Courtesy cars', ctx.loadCourtesy], ['Money', ctx.loadMoney],
    ['Hires', ctx.loadHires], ['Cars', ctx.loadCars], ['Customers', ctx.loadCustomers],
  ]) {
    let threw = null;
    if (typeof fn !== 'function') { threw = new Error('loader is not defined at all'); }
    else {
      try { await fn(); } catch (e) { threw = e; }
    }
    ok(threw === null, `the ${name} tab loads without throwing: ` + (threw && threw.message));
  }

  // ── the money view ───────────────────────────────────────────────────
  await ctx.loadMoney();
  const tiles = store['money-tiles'].innerHTML;
  ok(tiles.includes('On hire right now') && tiles.includes('300.00'),
    'the money view shows what is on hire right now');
  ok(tiles.includes('Still owed') && tiles.includes('Collected all time'),
    'and what has been collected against what is still owed');
  ok(/tile alert[\s\S]{0,140}Still owed/.test(tiles),
    'money outstanding is flagged rather than stated flatly');
  ok(tiles.includes('50%'), 'and how much of the fleet is actually earning');
  ok(store['car-earnings'].innerHTML.includes('RE24NTL') &&
     store['car-earnings'].innerHTML.includes('456.00'),
    'earning is broken down per car');

  // ── courtesy pairing ─────────────────────────────────────────────────
  await ctx.loadCourtesy();
  const c = store['courtesy-panel'].innerHTML;
  ok(c.includes('AB12CDE') && c.includes('RE21NTL'),
    'the courtesy view pairs their car with the one they are driving');
  ok(c.indexOf('AB12CDE') < c.indexOf('RE21NTL'),
    'their own car reads first, then what they are in — "AB12CDE → driving RE21NTL"');
  ok(c.includes('In the workshop'), 'and says why they have it');

  // ── payment and texting on a hire ────────────────────────────────────
  await ctx.loadHires();
  const rows = store['hire-rows'].innerHTML;
  ok(rows.includes('data-pay='), 'an unpaid hire offers to take payment');
  ok(rows.includes('data-text='), 'and to text the customer their car is ready');

  fetchCalls.length = 0;
  await ctx.textCarReady(1);
  ok(fetchCalls.some((c2) => c2.url === '/api/rentals/agreements/1/text-ready' &&
    c2.opts.method === 'POST'), 'texting posts to the hire it is about');

  fetchCalls.length = 0;
  await ctx.takePayment(1);
  ok(fetchCalls.some((c2) => c2.url === '/api/rentals/agreements/1/pay' &&
    c2.opts.method === 'POST'), 'taking payment opens a checkout for that hire');

  // ── the hire drawer ──────────────────────────────────────────────────
  body.dataset.readonly = '';
  await ctx.openHire(1);
  await wait(10);
  ok(store['hire-drawer'].classList.contains('open'), 'a hire opens in its own drawer');
  ok(store['hd-title'].textContent === 'RE21NTL · Alex Rider', 'titled with the car and who has it');

  const bill = store['hd-bill'].innerHTML;
  ok(bill.includes('225.00'), 'the bill shows the hire itself');
  ok(bill.includes('Insurance') && bill.includes('60.00'), 'and the insurance taken');
  ok(bill.includes('325.00'), 'and totals what is actually owed');
  // The fee is showing as running up, not as charged.
  ok(/not charged/.test(bill) && bill.includes('75.00'),
    'a late fee not yet applied is shown as owed-if-charged, not as a debt');
  ok(bill.includes('Deposit held') && bill.includes('150.00'),
    'the deposit is shown as held, separate from the bill');
  ok(bill.includes('returned empty'), 'and extras say what they were for');

  // Applying, waiving, extras, deposit — each posts to its own endpoint.
  for (const [btn, url, method] of [
    ['hd-late-fee', '/api/rentals/agreements/1/late-fee', 'POST'],
    ['hd-waive-late', '/api/rentals/agreements/1/late-fee', 'POST'],
    ['hd-save-charges', '/api/rentals/agreements/1/charges', 'PATCH'],
    ['hd-deposit', '/api/rentals/agreements/1/deposit', 'POST'],
  ]) {
    fetchCalls.length = 0;
    await store[btn].fire('click');
    await wait(10);
    ok(fetchCalls.some((c2) => c2.url === url && c2.opts.method === method),
      `${btn} posts to ${url}`);
  }
  // Waiving is explicitly zero, not "no amount".
  fetchCalls.length = 0;
  await store['hd-waive-late'].fire('click');
  await wait(10);
  const waive = fetchCalls.find((c2) => c2.url.endsWith('/late-fee'));
  ok(waive && waive.body.amount === 0,
    'waiving sends an explicit 0 rather than leaving it to the default: ' + JSON.stringify(waive && waive.body));

  // ── messages ─────────────────────────────────────────────────────────
  const log = store['hd-messages'].innerHTML;
  ok(log.includes('your car is ready'), 'the thread shows what was sent');
  ok(log.includes('msg-failed') && log.includes('unverified number'),
    'and a bounced text is kept, with why');

  fetchCalls.length = 0;
  store['hd-message'].value = '';
  await store['hd-send'].fire('click');
  await wait(10);
  ok(!fetchCalls.some((c2) => c2.url.startsWith('/api/rentals/messages') && c2.opts.method === 'POST'),
    'an empty message is not sent');
  ok(/type something/i.test(store['hd-msg-err'].textContent), 'and says so');

  store['hd-message'].value = 'Your car will be ready tomorrow.';
  fetchCalls.length = 0;
  await store['hd-send'].fire('click');
  await wait(10);
  const sent = fetchCalls.find((c2) => c2.url.startsWith('/api/rentals/messages') && c2.opts.method === 'POST');
  ok(sent && sent.body.body === 'Your car will be ready tomorrow.' &&
     sent.body.customer_id === 7 && sent.body.agreement_id === 1,
    'a free-text message goes with the customer and the hire: ' + JSON.stringify(sent && sent.body));

  // ── documents ────────────────────────────────────────────────────────
  const docs = store['hd-docs'].innerHTML;
  ok(docs.includes('licence.jpg'), 'documents on the hire are listed');
  ok(docs.includes('/api/rentals/documents/5/file'), 'each one links to the file itself');
  ok(docs.includes('data-del-doc="5"'), 'and can be deleted');

  // Uploading with nothing chosen says so rather than posting an empty form.
  fetchCalls.length = 0;
  store['hd-doc-file'].files = [];
  await store['hd-doc-upload'].fire('click');
  await wait(10);
  ok(/choose a file/i.test(store['hd-doc-err'].textContent),
    'uploading nothing asks for a file: ' + JSON.stringify(store['hd-doc-err'].textContent));

  // The upload itself: multipart, with the file and what it belongs to.
  store['hd-doc-file'].files = [{ name: 'licence.jpg' }];
  store['hd-doc-kind'].value = 'licence';
  fetchCalls.length = 0;
  await store['hd-doc-upload'].fire('click');
  await wait(10);
  const up = fetchCalls.find((c2) => c2.url === '/api/rentals/documents' && c2.opts.method === 'POST');
  ok(!!up, 'uploading posts the file to the documents endpoint');
  ok(up && up.opts.body.fields.kind === 'licence' &&
     String(up.opts.body.fields.agreement_id) === '1' &&
     String(up.opts.body.fields.customer_id) === '7',
    'with what it is and what it belongs to: ' + JSON.stringify(up && up.opts.body.fields));
  ok(up && up.opts.headers['X-CSRF-Token'] === 'test-csrf-token',
    'and the CSRF token, which a multipart post has to carry itself');

  // A read-only account cannot upload either — the multipart path bypasses
  // api(), so the guard has to be repeated there.
  body.dataset.readonly = 'true';
  store['hd-doc-file'].files = [{ name: 'x.pdf' }];
  fetchCalls.length = 0;
  await store['hd-doc-upload'].fire('click');
  await wait(10);
  ok(/view-only/i.test(store['hd-doc-err'].textContent),
    'a read-only account cannot upload documents: ' + JSON.stringify(store['hd-doc-err'].textContent));
  ok(!fetchCalls.some((c2) => c2.url === '/api/rentals/documents'),
    'and nothing is posted');
  body.dataset.readonly = '';

  // ── editing a car ────────────────────────────────────────────────────
  ctx.openCarModal(FREE[0]);
  ok(store['car-modal-title'].textContent === 'Edit this car', 'editing reuses the add form');
  ok(store['c-reg'].value === 'RE24NTL' && store['c-mileage'].value === '41200',
    'prefilled from the car');
  ok(store['car-delete'].hidden === false, 'and offers to delete it');
  ok(store['car-extra'].hidden === false, 'with its details already open — there is something to see');

  fetchCalls.length = 0;
  store['c-colour'].value = 'Blue';
  await store['car-save'].fire('click');
  await wait(10);
  const edit = fetchCalls.find((c2) => c2.url === '/api/rentals/vehicles/11');
  ok(edit && edit.opts.method === 'PATCH' && edit.body.colour === 'Blue',
    'saving an edit PATCHes that car: ' + JSON.stringify(edit && edit.body));

  ctx.openCarModal(null);
  ok(store['car-modal-title'].textContent === 'Add one of our cars' && store['car-delete'].hidden,
    'adding a new one has no delete and its own title');

  // ── editing a customer ───────────────────────────────────────────────
  ctx.openCustomerModal({ id: 7, name: 'Alex Rider', phone: '+447700900123',
    email: '', licence_no: 'RIDER901', address: '', notes: '' });
  ok(store['cust-modal'].classList.contains('open'), 'a customer opens in its own form');
  ok(store['cu-name'].value === 'Alex Rider' && store['cu-licence'].value === 'RIDER901',
    'prefilled from the customer');
  fetchCalls.length = 0;
  await store['cust-save'].fire('click');
  await wait(10);
  const cu = fetchCalls.find((c2) => c2.url === '/api/rentals/customers/7');
  ok(cu && cu.opts.method === 'PATCH', 'saving PATCHes that customer');

  // ── the house rates reach the lend form ──────────────────────────────
  await ctx.loadRates();
  await ctx.openLend('11');
  await wait(10);
  ok(store['l-insurance'].value === '12' && store['l-latefee'].value === '25' &&
     store['l-deposit'].value === '150',
    'the lend form offers the house rates without retyping them');
  store['l-customer'].value = '7';
  fetchCalls.length = 0;
  await store['lend-save'].fire('click');
  await wait(10);
  const lent = fetchCalls.find((c2) => c2.url === '/api/rentals/lend');
  ok(lent && lent.body.insurance_per_day === 12 && lent.body.late_fee_per_day === 25 &&
     lent.body.deposit === 150,
    'and sends them with the hire: ' + JSON.stringify(lent && lent.body));

  process.exit(failed ? 1 : 0);
})();
