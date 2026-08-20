/* Exercises the Fleet registry's client-side sort — by Make, Model and
 * Driver — against the real internal/web/assets/fleet.js, not a
 * reimplementation of its comparator.
 *
 * This is the scenario worth getting wrong in a copy-paste: app.js already
 * had a global `document.querySelectorAll('th.sortable')` click handler for
 * the Invoices table. Registry headers reusing the same class without their
 * own scoped handler would have every click on Make/Model/Driver silently
 * try to re-sort and reload the Invoices list instead. This script proves
 * the registry's own click handlers fire, mutate only registry state, and
 * sort correctly — including empty values sorting last regardless of
 * direction, and a second click on the same column flipping direction.
 *
 * Usage: node tools/registry-sort-check.cjs
 * Exits non-zero if any scenario disagrees with its expectation.
 */

'use strict';

const fs = require('fs');
const vm = require('vm');
const path = require('path');

const ASSETS = path.join(__dirname, '..', 'internal', 'web', 'assets');
const src = fs.readFileSync(path.join(ASSETS, 'fleet.js'), 'utf8');

let failed = 0;
function check(label, ok) {
  console.log((ok ? 'ok  - ' : 'FAIL- ') + label);
  if (!ok) failed++;
}

// A single permissive stub stands in for every element $() could be asked
// for — the sort logic under test never inspects most of them, so there is
// nothing to gain from a bespoke shape per id.
function makeGenericEl(id) {
  const el = {
    id, hidden: false, disabled: false, value: '', textContent: '',
    dataset: {}, className: '',
    _html: '',
    get innerHTML() { return el._html; },
    set innerHTML(v) { el._html = v; },
    addEventListener() {},
    querySelector() { return makeGenericEl(''); },
    querySelectorAll() { return []; },
    classList: { add() {}, remove() {}, toggle() {}, contains: () => false },
  };
  return el;
}

// The three Registry headers under test — real objects, reused across both
// the module's own click-handler registration and its render-time
// arrow/sorted-class updates, so a simulated click and a subsequent render
// check are looking at the same state.
function makeSortHeader(sortKey) {
  const arrow = { _text: '↓', get textContent() { return this._text; }, set textContent(v) { this._text = v; } };
  const classes = new Set();
  const listeners = [];
  return {
    dataset: { sort: sortKey },
    addEventListener(ev, fn) { if (ev === 'click') listeners.push(fn); },
    click() { listeners.forEach((fn) => fn()); },
    classList: {
      add: (c) => classes.add(c),
      remove: (c) => classes.delete(c),
      toggle: (c, on) => { on ? classes.add(c) : classes.delete(c); },
      contains: (c) => classes.has(c),
    },
    querySelector(sel) { return sel === '.arrow' ? arrow : null; },
    get arrowText() { return arrow.textContent; },
    get sorted() { return classes.has('sorted'); },
  };
}

const headers = {
  make: makeSortHeader('make'),
  model: makeSortHeader('model'),
  driver: makeSortHeader('driver'),
};
const headerList = [headers.make, headers.model, headers.driver];

const registryRowsEl = makeGenericEl('registry-rows');

const elements = new Map();
function $(id) {
  if (id === 'registry-rows') return registryRowsEl;
  if (!elements.has(id)) elements.set(id, makeGenericEl(id));
  return elements.get(id);
}

const documentStub = {
  querySelectorAll(sel) {
    if (sel === '#view-fleet th.sortable') return headerList;
    return [];
  },
  addEventListener() {},
};

// registryData, registrySort and registryDir are all module-scope `let`
// bindings inside fleet.js. A vm-run script's top-level `let` is NOT
// reified as a property on the context object the way a function
// declaration is — direct assignment to context.registryData from out here
// would silently create an unrelated property, leaving the real internal
// state untouched. Going through the real loadFleet() (which the click
// handlers also flow through) exercises the actual binding instead.
const TEST_REGISTRY = [
  { registration: 'AA11AAA', make: 'Toyota', model: 'Corolla', driver: 'Bob', brutto: 10, active: true },
  { registration: 'BB22BBB', make: 'ford', model: 'Focus', driver: '', brutto: 20, active: true },
  { registration: 'CC33CCC', make: 'Toyota', model: 'Avensis', driver: 'Alice', brutto: 30, active: true },
  { registration: 'DD44DDD', make: '', model: 'Transit', driver: 'Zara', brutto: 40, active: false },
];

async function fakeApi(url) {
  if (url === '/api/companies') return [];
  if (url === '/api/registry/unassigned') return [];
  if (url === '/api/registry') return TEST_REGISTRY;
  return {};
}

const context = {
  console,
  $, document: documentStub,
  esc: (s) => String(s ?? ''),
  dash: (s) => (s ? String(s) : '—'),
  money: (n) => Number(n || 0).toFixed(2),
  int: (n) => String(Math.round(Number(n) || 0)),
  carIcon: () => '',
  api: fakeApi,
  toast: () => {},
  show: () => {},
  openVehicle: () => {},
  openInvoice: () => {},
  openPart: () => {},
  state: { current: null, editingVehicle: null },
  viewLoaders: {},
  // Real implementations, not no-ops: this file loads fleet.js in isolation
  // (app.js, where these actually live, is never loaded here), and
  // loadFleet() now calls them for real on every fetch stage. A no-op stub
  // would make `stale()` always return false and silently stop testing
  // anything about ordering — these behave exactly like app.js's originals.
  loadSeq: {},
  beginLoad(key) { return (context.loadSeq[key] = (context.loadSeq[key] || 0) + 1); },
  stale(key, seq) { return context.loadSeq[key] !== seq; },
};
vm.createContext(context);
new vm.Script(src, { filename: 'fleet.js' }).runInContext(context);

function renderedRegs() {
  return [...registryRowsEl.innerHTML.matchAll(/veh-reg">([^<]+)</g)].map((m) => m[1]);
}

(async () => {

await context.loadFleet();

// 1. Default (unsorted): whatever order the API returned it in.
check('default order matches the API response',
  JSON.stringify(renderedRegs()) === JSON.stringify(['AA11AAA', 'BB22BBB', 'CC33CCC', 'DD44DDD']));

// 2. Clicking Make sorts ascending, case-insensitively, empty last.
headers.make.click();
check('Make ascending is case-insensitive (ford before Toyota)',
  JSON.stringify(renderedRegs()) === JSON.stringify(['BB22BBB', 'AA11AAA', 'CC33CCC', 'DD44DDD']));
check('Make header shows the ascending arrow and is marked sorted',
  headers.make.arrowText === '↑' && headers.make.sorted);
check('Model and Driver headers are not marked sorted while Make is active',
  !headers.model.sorted && !headers.driver.sorted);

// 3. Clicking Make again flips to descending — empty values still last, not
//    first: sorting on "nothing" has no direction of its own. Ties (the two
//    Toyotas) must keep their original relative order, not get reversed
//    along with everything else — that is what a plain sort-then-reverse
//    would have done wrong.
headers.make.click();
check('Make descending still puts the empty make last, not first',
  JSON.stringify(renderedRegs()) === JSON.stringify(['AA11AAA', 'CC33CCC', 'BB22BBB', 'DD44DDD']));
check('Make header shows the descending arrow', headers.make.arrowText === '↓');

// 4. Switching to a different column (Driver) resets to ascending rather
//    than carrying over Make's descending direction.
headers.driver.click();
check('switching to Driver starts ascending, not carrying over the old direction',
  JSON.stringify(renderedRegs()) === JSON.stringify(['CC33CCC', 'AA11AAA', 'DD44DDD', 'BB22BBB']));
check('Driver header is now the sorted one, Make is not',
  headers.driver.sorted && !headers.make.sorted);

// 5. Model, ascending, from a clean click (no prior state on that column).
headers.model.click(); // first click on Model: was already off, defaults asc
check('Model ascending', JSON.stringify(renderedRegs()) === JSON.stringify(['CC33CCC', 'AA11AAA', 'BB22BBB', 'DD44DDD']));

if (failed) {
  console.log(`\n${failed} check(s) failed.`);
  process.exit(1);
}
console.log('\nall checks passed.');

})();
