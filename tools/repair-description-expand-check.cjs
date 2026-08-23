/* Boots the real dashboard scripts (same shared-scope technique as
 * ui-check.cjs) and drives loadVehicleDetail with two repair entries — one
 * with a long description, one short — proving: a long description gets a
 * "More" button and starts truncated; a short one gets no button at all
 * (nothing to expand); clicking "More" reveals the full text and flips the
 * button to "Less"; clicking again re-truncates it.
 *
 * Usage: node tools/repair-description-expand-check.cjs
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

function makeEl(id) {
  const listeners = {};
  const classes = new Set();
  return {
    id, textContent: '', innerHTML: '', value: '', hidden: hiddenIds.has(id),
    dataset: {}, style: {},
    classList: {
      add: (c) => classes.add(c), remove: (c) => classes.delete(c),
      toggle: (c, force) => {
        const on = force === undefined ? !classes.has(c) : force;
        on ? classes.add(c) : classes.delete(c);
        return on;
      },
      contains: (c) => classes.has(c),
    },
    addEventListener(ev, fn) { (listeners[ev] ??= []).push(fn); },
    fire(ev, arg) { (listeners[ev] || []).forEach((fn) => fn(arg || {})); },
    click() { this.fire('click'); },
    setAttribute() {}, removeAttribute() {},
    appendChild() {}, removeChild() {}, remove() {}, insertAdjacentHTML() {},
    scrollIntoView() {}, focus() {}, blur() {},
    querySelectorAll: () => [], querySelector: () => null, closest: () => null,
    isConnected: true, clientWidth: 1200,
  };
}

const store = {};
ids.forEach((i) => { store[i] = makeEl(i); });
['c-invoices', 'c-vehicles', 'c-parts', 'c-suppliers', 'c-training', 'c-exports']
  .forEach((i) => { store[i] ??= makeEl(i); });

// A minimal real-enough DOM for exactly the one element the new feature
// needs to walk (veh-repairs): parseable innerHTML → real child elements
// with real classList/dataset, so querySelectorAll('.desc-toggle') and the
// per-row data-desc-row lookup both work against the actual markup fleet.js
// generates, not a hand-modelled stand-in for it.
function makeRepairsTable() {
  const el = makeEl('veh-repairs');
  let cells = []; // { row, span, button|null }
  Object.defineProperty(el, 'innerHTML', {
    get() { return el._html || ''; },
    set(html) {
      el._html = html;
      cells = [];
      const rowRe = /data-desc-row="(\d+)"/g;
      const rows = new Set([...html.matchAll(rowRe)].map((m) => m[1]));
      for (const row of rows) {
        const long = new RegExp(`class="[^"]*desc-toggle[^"]*" data-desc-row="${row}"`).test(html);
        const spanClasses = new Set(
          new RegExp(`data-desc-row="${row}">\\s*<span class="([^"]*)"`).exec(html)?.[1].split(' ').filter(Boolean) || []);
        const cellClasses = new Set(['desc-cell']);
        const span = {
          classList: {
            add: (c) => spanClasses.add(c), remove: (c) => spanClasses.delete(c),
            toggle: (c, force) => {
              const on = force === undefined ? !spanClasses.has(c) : force;
              on ? spanClasses.add(c) : spanClasses.delete(c);
              return on;
            },
            contains: (c) => spanClasses.has(c),
          },
        };
        const cellObj = {
          dataset: { descRow: row },
          classList: {
            add: (c) => cellClasses.add(c), remove: (c) => cellClasses.delete(c),
            toggle: (c, force) => {
              const on = force === undefined ? !cellClasses.has(c) : force;
              on ? cellClasses.add(c) : cellClasses.delete(c);
              return on;
            },
            contains: (c) => cellClasses.has(c),
          },
          querySelector: (sel) => (sel === 'span' ? span : null),
        };
        let button = null;
        if (long) {
          const listeners = {};
          button = {
            dataset: { descRow: row },
            textContent: 'More',
            addEventListener(ev, fn) { (listeners[ev] ??= []).push(fn); },
            click() { (listeners.click || []).forEach((fn) => fn()); },
          };
        }
        cells.push({ row, cell: cellObj, span, button });
      }
    },
  });
  el.querySelectorAll = (sel) => {
    if (sel === '.desc-toggle') return cells.filter((c) => c.button).map((c) => c.button);
    return [];
  };
  el.querySelector = (sel) => {
    const m = sel.match(/\.desc-cell\[data-desc-row="(\d+)"\]/);
    if (!m) return null;
    return cells.find((c) => c.row === m[1])?.cell || null;
  };
  return el;
}
store['veh-repairs'] = makeRepairsTable();

const REPAIRS = [
  { service_date: '2026-08-01', service_type: 'full', mileage: 50000, timing_belt_changed: false,
    description: 'A short note.' },
  { service_date: '2026-07-01', service_type: 'mini', mileage: 48000, timing_belt_changed: false,
    description: 'This description is deliberately much longer than forty characters so it needs a way to expand.' },
];

const errors = [];
const ctx = vm.createContext({
  console,
  document: {
    getElementById: (id) => store[id] || null,
    querySelectorAll: () => [], querySelector: () => null,
    addEventListener() {}, createElement: () => makeEl('tmp'), body: makeEl('body'), cookie: '', activeElement: null,
  },
  window: { addEventListener() {}, location: { href: '' } },
  location: { href: '' },
  ResizeObserver: class { observe() {} },
  setTimeout, clearTimeout, setInterval: () => 0, clearInterval() {},
  fetch: async (url) => {
    if (url.startsWith('/api/vehicle/')) {
      return { ok: true, status: 200, json: async () => ({ vehicle: { registration: 'AB12CDE' }, repairs: REPAIRS }) };
    }
    return { ok: true, status: 200, json: async () => ({}) };
  },
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

let failed = false;
function ok(cond, label) {
  console.log((cond ? 'ok  ' : 'FAIL') + ' - ' + label);
  if (!cond) failed = true;
}

(async () => {
  ok(errors.length === 0, 'every dashboard script loads without throwing: ' + errors.join('; '));

  // state itself is a top-level `const` inside app.js, so it never becomes
  // a property on ctx (the vm module doesn't expose script-lexical
  // bindings that way) — openVehicle is the real entry point that sets
  // state.currentReg, same as an actual click on a vehicle row would.
  ctx.openVehicle('AB12CDE');
  await ctx.loadVehicleDetail();

  const buttons = store['veh-repairs'].querySelectorAll('.desc-toggle');
  ok(buttons.length === 1, `exactly one row (the long description) gets a button: got ${buttons.length}`);

  const longCell = store['veh-repairs'].querySelector('.desc-cell[data-desc-row="1"]');
  ok(!!longCell, 'the long description\'s cell is reachable by its row index');
  ok(longCell.querySelector('span').classList.contains('truncate'), 'the long description starts truncated');

  const btn = buttons[0];
  ok(btn.textContent === 'More', 'the button starts labelled "More"');

  btn.click();
  ok(longCell.classList.contains('expanded'), 'clicking "More" marks the cell expanded');
  ok(!longCell.querySelector('span').classList.contains('truncate'), 'and drops truncation from the text itself');
  ok(btn.textContent === 'Less', 'and flips the button to "Less"');

  btn.click();
  ok(!longCell.classList.contains('expanded'), 'clicking "Less" re-collapses the cell');
  ok(longCell.querySelector('span').classList.contains('truncate'), 'and truncation comes back');
  ok(btn.textContent === 'More', 'and the button flips back to "More"');

  process.exit(failed ? 1 : 0);
})();
