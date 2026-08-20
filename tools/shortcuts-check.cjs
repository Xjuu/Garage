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
  return {
    id, textContent: '', innerHTML: '', value: '', hidden: hiddenIds.has(id),
    dataset: {}, style: {},
    classList: { toggle() {}, add() {}, remove() {}, contains: () => false },
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
const docHandlers = {};
const errors = [];

const ctx = vm.createContext({
  console,
  document: {
    getElementById: (id) => store[id] || null,
    querySelectorAll: () => [], querySelector: () => null,
    addEventListener(ev, fn) { (docHandlers[ev] ??= []).push(fn); },
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

// Spy on show() the way the real nav click-handler calls it, so a shortcut
// can be confirmed to have asked for the right view without needing the
// rest of the render pipeline (view sections, tab highlighting, ...) wired up.
const shown = [];
ctx.show = (view) => shown.push(view);

function press(key, opts = {}) {
  const e = { key, metaKey: false, ctrlKey: false, altKey: false, ...opts, preventDefault() {} };
  (docHandlers.keydown || []).forEach((fn) => fn(e));
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
store['drawer'].classList.contains = () => true; // simulate an open drawer
shown.length = 0;
press('1');
check('tab shortcuts are suppressed while the drawer is open', shown.length === 0);
store['drawer'].classList.contains = () => false;

store['gen-modal'].hidden = false; // simulate the Generate dialog being open
clicks.length = 0;
press('g');
check('shortcuts are suppressed while the Generate dialog is open', clicks.length === 0);
store['gen-modal'].hidden = true;

if (failed) {
  console.log(`\n${failed} check(s) failed.`);
  process.exit(1);
}
console.log('\nall checks passed.');
