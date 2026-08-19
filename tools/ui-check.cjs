/* Loads the dashboard's scripts the way a browser does — one shared global
   scope, each file executed in page order — then fires DOMContentLoaded and
   reports what rendered.
 *
 * This exists because two real bugs shipped that no other check caught:
 *   1. A function was called but never defined, so loadOverview threw and the
 *      whole front page came up blank.
 *   2. app.js referenced a name defined in exports.js, which the page loads
 *      later — a ReferenceError that killed every line after it, taking the
 *      sign-out button and the view registrations with it.
 *
 * Concatenating the files into one scope hides both, because declarations get
 * hoisted together. Node's vm module reproduces browser semantics exactly.
 *
 * Usage:  node tools/ui-check.cjs [routes.json]
 * routes.json maps an API path prefix to the JSON body to return; anything not
 * listed answers {}. Exits non-zero if any script or the boot handler throws.
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
// Elements carrying the hidden attribute must start hidden, or a check on
// dialog visibility would report the wrong thing.
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
// buildNav inserts these at runtime, so they exist in a real page even though
// they are absent from the static HTML.
['c-invoices', 'c-vehicles', 'c-parts', 'c-suppliers', 'c-training', 'c-exports']
  .forEach((i) => { store[i] ??= el(i); });

const routes = process.argv[2]
  ? JSON.parse(fs.readFileSync(process.argv[2], 'utf8'))
  : {};

const errors = [];
const docHandlers = {};
const missingIds = new Set();

const ctx = vm.createContext({
  console,
  document: {
    // A real browser returns null for an unknown id; auto-creating one would
    // hide exactly the breakage this is looking for.
    getElementById: (id) => { if (!store[id]) missingIds.add(id); return store[id] || null; },
    querySelectorAll: () => [], querySelector: () => null,
    addEventListener(ev, fn) { (docHandlers[ev] ??= []).push(fn); },
    createElement: () => el('tmp'), body: el('body'), cookie: '', activeElement: null,
  },
  window: { addEventListener() {}, location: { href: '' } },
  location: { href: '' },
  ResizeObserver: class { observe() {} },
  setTimeout: () => 0, setInterval: () => 0, clearTimeout() {}, clearInterval() {},
  fetch: async (url) => {
    const key = Object.keys(routes).find((k) => url.startsWith(k));
    return { ok: true, status: 200, json: async () => (key ? routes[key] : {}) };
  },
  Math, JSON, Object, Array, Number, String, Boolean, Date, Set, Map, Promise,
  Error, Intl, URLSearchParams, encodeURIComponent, parseInt, parseFloat, isNaN,
  confirm: () => true, alert() {},
});
ctx.globalThis = ctx;

process.on('unhandledRejection', (e) => errors.push(`unhandledRejection: ${e && e.message}`));

for (const file of ORDER) {
  try {
    new vm.Script(fs.readFileSync(path.join(ASSETS, file), 'utf8'), { filename: file })
      .runInContext(ctx);
  } catch (e) {
    errors.push(`${file}: ${e.constructor.name}: ${e.message}`);
  }
}

for (const fn of docHandlers.DOMContentLoaded || []) {
  try { fn({}); } catch (e) { errors.push(`DOMContentLoaded: ${e.constructor.name}: ${e.message}`); }
}

// Give the boot's promises a turn to settle before reporting.
setTimeout(() => {
  const count = (id, re) => (store[id] ? (store[id].innerHTML.match(re) || []).length : 0);

  console.log('  script errors     :', errors.length ? errors : 'none');
  console.log('  ids absent in HTML:', missingIds.size ? [...missingIds] : 'none');
  console.log('  key functions     :',
    ['show', 'loadOverview', 'loadInvoices', 'openGenerate', 'openFiles', 'loadRecentFiles']
      .map((n) => `${n}=${typeof ctx[n]}`).join('  '));
  console.log('  month title       :', JSON.stringify(store['month-title'].textContent));
  console.log('  month tiles       :', count('month-tiles', /class="tile"/g));
  console.log('  all-time tiles    :', count('tiles', /class="tile/g));
  console.log('  invoice badge     :', JSON.stringify(store['c-invoices'].textContent));
  console.log('  recent files rows :', count('ov-files', /<tr/g));
  console.log('  dialogs hidden    :',
    `generate=${store['gen-modal'].hidden} files=${store['files-modal'].hidden}`);

  // The spend chart builds its own day axis in the browser's timezone. Using
  // toISOString() there shifted every day back by one east of Greenwich and
  // dropped today's column entirely, so this pins the behaviour down. Run the
  // whole check under TZ=Asia/Tokyo or TZ=Pacific/Auckland to stress it.
  const dateProblems = [];
  if (typeof ctx.fillDays === 'function') {
    const days = ctx.fillDays(
      [{ date: '2026-08-19', brutto: 83.16, invoices: 1 }],
      '2026-07-21', '2026-08-19');

    if (days.length !== 30) dateProblems.push(`axis has ${days.length} days, want 30`);
    if (days[0]?.date !== '2026-07-21') dateProblems.push(`starts ${days[0]?.date}, want 2026-07-21`);
    if (days[days.length - 1]?.date !== '2026-08-19') {
      dateProblems.push(`ends ${days[days.length - 1]?.date}, want 2026-08-19 (today must be included)`);
    }
    const total = days.reduce((a, d) => a + d.brutto, 0);
    if (Math.abs(total - 83.16) > 0.005) {
      dateProblems.push(`plotted total £${total.toFixed(2)}, want £83.16 — a day was dropped`);
    }
  } else {
    dateProblems.push('fillDays is not defined');
  }
  console.log(`  spend axis (TZ=${process.env.TZ || 'system'}):`,
    dateProblems.length ? dateProblems : 'includes today, totals match');

  process.exit(errors.length || dateProblems.length ? 1 : 0);
}, 300);
