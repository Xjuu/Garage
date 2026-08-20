/* Boots the real dashboard scripts the same way tools/ui-check.cjs does —
 * one shared vm context, files loaded in page order — then dispatches
 * synthetic keydown events at the document to exercise the keyboard
 * shortcuts in app.js: 1-4 switching tabs, S/U/G clicking the sync, upload
 * and export buttons, and the guards that must suppress all of it while
 * typing in a field or while a drawer/dialog is open.
 *
 * Usage: node tools/shortcuts-check.cjs
 * Exits non-zero if any check fails or a loaded script throws.
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

const clicks = []; // records which stub element .click() actually fired on

function el(id) {
  const listeners = {};
  // A real Set-backed classList, not a static stub: the drawer-open checks
  // below need add()/remove() to actually change what contains() reports,
  // or a real bug — closeDrawer() supposedly closing the drawer, but the
  // capture-phase check running after it already sees it as closed and goes
  // back on top of the close — would pass this harness by coincidence
  // rather than by the ordering actually being right.
  const classes = new Set();
  return {
    id, textContent: '', innerHTML: '', value: '', hidden: hiddenIds.has(id),
    dataset: {}, style: {},
    classList: {
      add: (c) => classes.add(c),
      remove: (c) => classes.delete(c),
      toggle: (c, on) => { (on ?? !classes.has(c)) ? classes.add(c) : classes.delete(c); },
      contains: (c) => classes.has(c),
    },
    addEventListener(ev, fn) { (listeners[ev] ??= []).push(fn); },
    click() { clicks.push(id); (listeners.click || []).forEach((fn) => fn({})); },
    setAttribute() {}, removeAttribute() {},
    appendChild() {}, removeChild() {}, remove() {}, insertAdjacentHTML() {},
    scrollIntoView() {}, focus() {}, blur() {},
    querySelectorAll: () => [], querySelector: () => null, closest: () => null,
    isConnected: true, clientWidth: 1200,
  };
}

const store = {};
ids.forEach((i) => { store[i] = el(i); });
['c-invoices', 'c-vehicles', 'c-parts', 'c-suppliers', 'c-training', 'c-exports']
  .forEach((i) => { store[i] ??= el(i); });

let activeElement = { tagName: 'BODY', isContentEditable: false };
// Capture-phase and bubble-phase listeners on the same target (document, in
// every case here) fire in two separate passes in a real browser — all
// capture-phase listeners first, in registration order, then all
// bubble-phase ones — regardless of which was registered first in the
// source. Tracking them in one flat list and calling it in registration
// order would only happen to get this right if the source order matched;
// modelling the two phases properly is what makes the ordering assertions
// below trustworthy rather than a coincidence of file layout.
const docCapture = {};
const docBubble = {};
const errors = [];

const ctx = vm.createContext({
  console,
  document: {
    getElementById: (id) => store[id] || null,
    querySelectorAll: () => [], querySelector: () => null,
    addEventListener(ev, fn, capture) {
      ((capture ? docCapture : docBubble)[ev] ??= []).push(fn);
    },
    createElement: () => el('tmp'), body: el('body'), cookie: '',
    get activeElement() { return activeElement; },
  },
  window: { addEventListener() {}, location: { href: '' } },
  location: { href: '' },
  ResizeObserver: class { observe() {} },
  setTimeout: () => 0, setInterval: () => 0, clearTimeout() {}, clearInterval() {},
  fetch: async () => ({ ok: true, status: 200, json: async () => ({}) }),
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
if (errors.length) {
  console.log('script errors:', errors);
  process.exit(1);
}

// Wrapping, not replacing: goBack()'s history bookkeeping lives inside the
// real show(), so a check that needs goBack() to actually work (popping
// viewHistory, restoring the previous view) needs the real function to keep
// running — a spy that swallows the call the way the tab-shortcut checks
// only needed would silently break that.
const realShow = ctx.show;
const shown = [];
ctx.show = (view) => { shown.push(view); return realShow(view); };

function press(key, opts = {}) {
  const e = { key, metaKey: false, ctrlKey: false, altKey: false, ...opts, preventDefault() {} };
  (docCapture.keydown || []).forEach((fn) => fn(e));
  (docBubble.keydown || []).forEach((fn) => fn(e));
}

let failed = 0;
function check(label, ok) {
  console.log((ok ? 'ok  - ' : 'FAIL- ') + label);
  if (!ok) failed++;
}

// 1. The four tab shortcuts.
for (const [key, view] of [['1', 'overview'], ['2', 'invoices'], ['3', 'spending'], ['4', 'fleet']]) {
  shown.length = 0;
  press(key);
  check(`"${key}" switches to ${view}`, shown.length === 1 && shown[0] === view);
}

// 2. Sync / Upload / Generate, both cases and confirming the RIGHT button
//    was the one clicked. .includes(), not an exact single-click check:
//    the real Upload button's own click handler also clicks the hidden
//    file input, so clicks[] legitimately gets more than one entry.
for (const [key, id] of [['s', 'btn-sync'], ['S', 'btn-sync'], ['u', 'btn-upload'], ['g', 'btn-sheet']]) {
  clicks.length = 0;
  press(key);
  check(`"${key}" clicks #${id}`, clicks.includes(id));
}

// 3. Typing in a field must suppress every shortcut — 's' in a text field is
//    a letter, not "sync the mailbox". Reset first: test 2's real 'g' press
//    genuinely opened the Generate dialog through its real click handler,
//    and leaving it open would make this check pass for the wrong reason —
//    the still-open dialog masking whatever the input-focus guard does or
//    does not do, rather than actually exercising it.
store['gen-modal'].hidden = true;
activeElement = { tagName: 'INPUT', isContentEditable: false };
clicks.length = 0; shown.length = 0;
press('s'); press('1'); press('g'); press('u');
check('shortcuts are suppressed while an input is focused', clicks.length === 0 && shown.length === 0);
activeElement = { tagName: 'BODY', isContentEditable: false };

// 4. A modifier held down must suppress it too — Ctrl/Cmd-S is the
//    browser's Save As, not this app's sync button.
clicks.length = 0;
press('s', { ctrlKey: true });
check('Ctrl-S does not trigger sync (leaves the browser shortcut alone)', clicks.length === 0);

// 5. An open drawer or dialog must suppress the tab-switching shortcuts —
//    jumping away from an open invoice or a half-filled dialog would lose
//    more than the key saves.
store['drawer'].classList.add('open'); // simulate an open drawer
shown.length = 0;
press('1');
check('tab shortcuts are suppressed while the drawer is open', shown.length === 0);
// '1' isn't Escape, so nothing in the real code closes the drawer here —
// clear it explicitly, or the next section's checks would see a drawer
// that looks open for a reason that has nothing to do with what they are
// testing, the same leftover-state mistake the Return fix's test caught
// earlier in this file's history.
store['drawer'].classList.remove('open');

store['gen-modal'].hidden = false; // simulate the Generate dialog being open
clicks.length = 0;
press('g');
check('shortcuts are suppressed while the Generate dialog is open', clicks.length === 0);
store['gen-modal'].hidden = true;

// 6. Escape steps back through the real view history built by test 1's
//    presses of 1→2→3→4 (overview → invoices → spending → fleet), so this
//    walks it back down: fleet → spending → invoices → overview → (nothing
//    left, no-op). Using the real show(), not the spy alone, is exactly why
//    it was wrapped rather than replaced — goBack()'s bookkeeping lives
//    inside it.
for (const want of ['spending', 'invoices', 'overview']) {
  shown.length = 0;
  press('Escape');
  check(`Escape steps back to ${want}`, shown[shown.length - 1] === want);
}

// 7. With nothing left in history, Escape is a no-op rather than an error —
//    Overview is home; there is nowhere further back to go.
shown.length = 0;
press('Escape');
check('Escape with empty history does nothing', shown.length === 0);

// 8. Escape must close an open overlay instead of going back — one press,
//    one action. Rebuild a little history first so there would be
//    somewhere to go back to if the guard were missing.
press('1'); press('2'); // history now has at least one entry again

store['drawer'].classList.add('open');
shown.length = 0;
press('Escape');
check('Escape does not go back while the drawer is open (closes it instead)', shown.length === 0);
// Unlike the '1' case above, this Escape press genuinely runs the real
// closeDrawer() (a separate bubble-phase handler, unconditional on the key
// being Escape), which really does clear 'open' — nothing to reset by hand.

store['gen-modal'].hidden = false;
shown.length = 0;
press('Escape');
check('Escape does not go back while the Generate dialog is open', shown.length === 0);
store['gen-modal'].hidden = true;

store['omni-results'].hidden = false; // simulate the search dropdown showing
shown.length = 0;
press('Escape');
check('Escape does not go back while the search dropdown is open', shown.length === 0);
store['omni-results'].hidden = true;

// 9. Section shortcuts: once inside Analysis (pressing '3' lands on
//    Spending), its own subtabs get first-letter keys — including S and U,
//    which deliberately shadow the global Sync/Upload shortcuts while
//    actually looking at this section. Two real collisions get resolved the
//    same way every time: the more central subtab keeps the plain letter
//    (Spending over Suppliers, Vehicles over VAT), the other takes its
//    second letter.
press('3'); // into Analysis, landing on Spending
check('the shortcuts bar actually shows the Analysis subtab chips',
  ['Spending', 'Vehicles', 'Parts', 'Suppliers', 'VAT'].every((label) => store['section-shortcuts'].innerHTML.includes(label)));
for (const [key, view] of [['s', 'spending'], ['v', 'vehicles'], ['p', 'parts'], ['u', 'suppliers'], ['a', 'vat']]) {
  shown.length = 0; clicks.length = 0;
  press(key);
  check(`inside Analysis, "${key}" goes to ${view}`, shown[0] === view);
}
check('"u" went to Suppliers, not Upload — the shadowing actually applies', clicks.length === 0);

// 10. Setup has no letter collisions, so this is really just confirming the
//     group-scoping picks the right table at all, not Analysis's leftover
//     one.
press('4'); // into Setup, landing on Fleet
for (const [key, view] of [['t', 'training'], ['a', 'admin'], ['f', 'fleet']]) {
  shown.length = 0;
  press(key);
  check(`inside Setup, "${key}" goes to ${view}`, shown[0] === view);
}

// 11. Outside Analysis and Setup, S and U must still mean Sync and Upload —
//     the shadowing is scoped to the section it belongs to, not global.
press('1'); // back to Overview, which has no subtabs at all
check('the section chips are gone on Overview, which has nothing to show there',
  store['section-shortcuts'].innerHTML === '');
clicks.length = 0; shown.length = 0;
press('s');
check('on Overview, "s" clicks Sync again (no shadowing outside Analysis)',
  clicks.includes('btn-sync') && shown.length === 0);

if (failed) {
  console.log(`\n${failed} check(s) failed.`);
  process.exit(1);
}
console.log('\nall checks passed.');
