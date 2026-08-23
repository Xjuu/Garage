/* Boots the real dashboard scripts (same one-shared-scope technique as
 * ui-check.cjs, for the same reason: a cross-file reference that only
 * throws when the files run in page order) and exercises how a returned
 * invoice renders in the Invoices table specifically: the row reads as
 * settled rather than needing attention (greyed text, not a "Check" red
 * edge), and its Parts column names the state instead of listing parts
 * that, credited, aren't really "in stock" any more — while an ordinary,
 * not-returned invoice on the same page is untouched by either.
 *
 * Usage: node tools/returned-invoice-check.cjs
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

const el = (id) => ({
  id, textContent: '', innerHTML: '', value: '', hidden: hiddenIds.has(id),
  dataset: {}, style: {},
  classList: { toggle() {}, add() {}, remove() {}, contains: () => false },
  addEventListener() {}, setAttribute() {}, removeAttribute() {},
  appendChild() {}, removeChild() {}, remove() {}, insertAdjacentHTML() {},
  scrollIntoView() {}, focus() {}, blur() {},
  querySelectorAll: () => [], querySelector: () => null, closest: () => null,
  isConnected: true, clientWidth: 1200,
});

const store = {};
ids.forEach((i) => { store[i] = el(i); });
['c-invoices', 'c-vehicles', 'c-parts', 'c-suppliers', 'c-training', 'c-exports']
  .forEach((i) => { store[i] ??= el(i); });

// A very random test amount, same shape a real "create a fake invoice,
// then credit it" QA pass produces — a returned original, its own credit
// note (which must read exactly the same as the original in this list —
// a credit note IS the return, not a separate thing from one), and an
// ordinary invoice that must render completely normally. The credit note
// carries two line items on purpose: the Parts-column replacement has to
// hold regardless of how many items an invoice has, not just one.
const RETURNED = {
  ID: 1, InvoiceDate: '2026-08-20', Supplier: 'QA Test Supplier Ltd', InvoiceNumber: 'QATEST-0001',
  VehicleReg: 'QA12TST', Netto: 1581.43, VATAmount: 316.29, Brutto: 1897.71,
  Returned: true, CreditOf: null, NeedsReview: false,
  Items: [{ PartNumber: 'QA-PART-99', Desc: 'Random test part' }],
};
const CREDIT_NOTE = {
  ID: 3, InvoiceDate: '2026-08-21', Supplier: 'QA Test Supplier Ltd', InvoiceNumber: 'QATEST-0001-CN',
  VehicleReg: 'QA12TST', Netto: -1581.43, VATAmount: -316.29, Brutto: -1897.71,
  Returned: false, CreditOf: 1, NeedsReview: false,
  Items: [
    { PartNumber: 'QA-PART-99', Desc: 'Random test part (returned)' },
    { PartNumber: 'QA-PART-100', Desc: 'A second returned part' },
  ],
};
const ORDINARY = {
  ID: 2, InvoiceDate: '2026-08-19', Supplier: 'Millfield Autoparts Ltd', InvoiceNumber: 'HS351559',
  VehicleReg: 'MJ65VJZ', Netto: 209.31, VATAmount: 41.86, Brutto: 251.17,
  Returned: false, CreditOf: null, NeedsReview: false,
  Items: [{ PartNumber: 'OESE020303', Desc: 'EGR module' }],
};

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
  setTimeout, clearTimeout, setInterval: () => 0, clearInterval() {},
  fetch: async (url) => {
    if (url.startsWith('/api/invoices')) {
      return {
        ok: true, status: 200,
        json: async () => ({ invoices: [RETURNED, CREDIT_NOTE, ORDINARY], total: 3, netto: 0, vat: 0, brutto: 0 }),
      };
    }
    return { ok: true, status: 200, json: async () => ({}) };
  },
  Math, JSON, Object, Array, Number, String, Boolean, Date, Set, Map, Promise,
  Error, Intl, URLSearchParams, encodeURIComponent, parseInt, parseFloat, isNaN,
  confirm: () => true, alert() {},
});
ctx.globalThis = ctx;
process.on('unhandledRejection', (e) => errors.push(`unhandledRejection: ${e && e.message}`));

for (const file of ORDER) {
  try {
    new vm.Script(fs.readFileSync(path.join(ASSETS, file), 'utf8'), { filename: file }).runInContext(ctx);
  } catch (e) {
    errors.push(`${file}: ${e.constructor.name}: ${e.message}`);
  }
}

let failed = false;
function ok(cond, label) {
  console.log((cond ? 'ok  ' : 'FAIL') + ' - ' + label);
  if (!cond) failed = true;
}

(async () => {
  ok(errors.length === 0, 'every dashboard script loads without throwing: ' + errors.join('; '));
  ok(typeof ctx.loadInvoices === 'function', 'loadInvoices is defined');

  await ctx.loadInvoices();
  await new Promise((r) => setTimeout(r, 10));

  const html = store['inv-rows'].innerHTML;
  const rowFor = (id) => {
    const m = html.match(new RegExp(`<tr[^>]*data-id="${id}"[\\s\\S]*?</tr>`));
    return m ? m[0] : '';
  };

  const returnedRow = rowFor(RETURNED.ID);
  ok(returnedRow.includes('returned'), 'the returned original\'s row carries the "returned" class');
  ok(returnedRow.includes('Returned') && !returnedRow.includes('QA-PART-99'),
    'its Parts column says "Returned" instead of listing the (credited) part number');
  ok(!returnedRow.includes('flagged'),
    'a returned invoice does not also get the red "needs review" edge — it is settled, not a problem');

  // A credit note IS the return, not a separate concept — its row on this
  // list has to read exactly the same as the original's: grey, "Returned",
  // no "Credit" pill of its own, and no part numbers listed even though
  // this specific credit note carries two line items.
  const creditRow = rowFor(CREDIT_NOTE.ID);
  ok(creditRow.includes('returned'), 'the credit note\'s own row ALSO carries the "returned" class');
  ok(creditRow.includes('Returned'), 'and shows the "Returned" pill, same as the original');
  ok(!creditRow.includes('Credit</span>'), 'and does NOT show a separate "Credit" pill any more');
  ok(!creditRow.includes('QA-PART-99') && !creditRow.includes('QA-PART-100'),
    'neither of its two part numbers leaks into the Parts column');

  const ordinaryRow = rowFor(ORDINARY.ID);
  ok(!/class="clickable[^"]*returned/.test(ordinaryRow), 'an ordinary invoice\'s row does NOT carry the "returned" class');
  ok(ordinaryRow.includes('OESE020303'), 'an ordinary invoice still shows its real part number');

  process.exit(failed ? 1 : 0);
})();
