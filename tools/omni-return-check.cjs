/* Exercises the search bar's Return-key contract with real keydown bubbling,
 * not a reimplementation of it.
 *
 * This exists because building the feature nearly shipped a real bug: the
 * omni input's own Enter handler calls resetOmni(), which blurs the input.
 * That blur happens synchronously, mid-bubble — so by the time the same
 * keydown event reached the document-level "Return focuses search" handler,
 * document.activeElement had already changed and the guard that should have
 * skipped it no longer matched. The result was the search bar re-focusing
 * itself in the same keystroke that was supposed to clear it.
 *
 * tools/ui-check.cjs's element stubs don't model focus() moving
 * document.activeElement, or listener bubbling with stopPropagation — they
 * are built for boot/render verification, not interaction. This script adds
 * just enough of a real two-level bubble (target, then document) to make
 * that race reproducible, and pins nine scenarios against it.
 *
 * Usage: node tools/omni-return-check.cjs
 * Exits non-zero if any scenario disagrees with its expectation.
 */

'use strict';

const fs = require('fs');
const vm = require('vm');
const path = require('path');

const ASSETS = path.join(__dirname, '..', 'internal', 'web', 'assets');
const src = fs.readFileSync(path.join(ASSETS, 'omni.js'), 'utf8');

let activeElement = null;
let bodyEl;

function makeFocusable(id, tagName) {
  const el = {
    id, tagName, hidden: false, dataset: {}, value: '', _listeners: {},
    addEventListener(ev, fn) { (el._listeners[ev] ??= []).push(fn); },
    focus() { activeElement = el; },
    blur() { if (activeElement === el) activeElement = bodyEl; },
    isContentEditable: false,
  };
  return el;
}

function makeResultsEl() {
  // A minimal model of exactly the two operations omni.js performs on this
  // element: parsing hit buttons out of an assigned innerHTML string, and
  // finding one by class through querySelector/querySelectorAll.
  let items = [];
  const el = {
    id: 'omni-results', hidden: true,
    get innerHTML() { return items.map((i) => `<button data-kind="${i.kind}" data-ref="${i.ref}">`).join(''); },
    set innerHTML(html) {
      items = [...html.matchAll(/<button class="omni-hit" data-kind="([^"]*)" data-ref="([^"]*)">/g)]
        .map((m) => ({ kind: m[1], ref: m[2], classes: new Set() }));
    },
    querySelector(sel) {
      if (sel === '.omni-hit') return wrap(items[0]);
      if (sel === '.omni-hit.top') { const it = items.find((i) => i.classes.has('top')); return it ? wrap(it) : null; }
      return null;
    },
    querySelectorAll(sel) { return sel === '.omni-hit' ? items.map(wrap) : []; },
  };
  function wrap(it) {
    if (!it) return null;
    return { dataset: { kind: it.kind, ref: it.ref }, classList: { add: (c) => it.classes.add(c) }, addEventListener() {} };
  }
  return el;
}

function mkEl(id) {
  return { id, hidden: true, value: '', dataset: {}, addEventListener() {}, focus() {}, blur() {} };
}

bodyEl = { tagName: 'BODY' };
activeElement = bodyEl;

const omniEl = makeFocusable('omni', 'INPUT');
const clearBtn = makeFocusable('omni-clear', 'BUTTON');
clearBtn.hidden = true;

const store = {
  omni: omniEl,
  'omni-results': makeResultsEl(),
  'omni-clear': clearBtn,
  'gen-modal': { hidden: true },
  'files-modal': { hidden: true },
};
['omni-from', 'omni-to', 'omni-range-clear', 'f-q', 'f-from', 'f-to', 'f-supplier', 'f-reg', 'f-review']
  .forEach((i) => { store[i] = mkEl(i); });

const documentListeners = { keydown: [], click: [] };
const opened = [];
const ctx = vm.createContext({
  console,
  $: (id) => store[id] ??= mkEl(id),
  esc: (v) => String(v ?? ''),
  int: (n) => String(n ?? 0),
  money: (n) => Number(n || 0).toFixed(2),
  debounce: (fn) => fn,
  document: {
    addEventListener(ev, fn) { (documentListeners[ev] ??= []).push(fn); },
    get activeElement() { return activeElement; },
  },
  api: async () => ({}),
  state: { filters: {}, page: 1 },
  show: () => {},
  openVehicle: (ref) => opened.push(['vehicle', ref]),
  openPart: (ref) => opened.push(['part', ref]),
  openInvoice: (ref) => opened.push(['invoice', ref]),
});
new vm.Script(src, { filename: 'omni.js' }).runInContext(ctx);

// A real two-level bubble: target's own listeners run first, then
// document's, unless the target's handler called stopPropagation().
function dispatchKeydown(target, key) {
  const e = {
    key, metaKey: false, ctrlKey: false, _stopped: false,
    stopPropagation() { this._stopped = true; }, preventDefault() {},
  };
  (target._listeners?.keydown || []).forEach((fn) => fn(e));
  if (!e._stopped) (documentListeners.keydown || []).forEach((fn) => fn(e));
  return e;
}

const failures = [];
function check(label, condition) {
  console.log(`  ${condition ? 'ok  ' : 'FAIL'}  ${label}`);
  if (!condition) failures.push(label);
}

// A minimal fixture standing in for a real /api/search response: one vehicle
// hit, which renderOmni ranks first and marks .top.
const searchResult = {
  total: 1,
  vehicles: [{ kind: 'vehicle', ref: 'FG21OXA', title: 'FG21OXA', subtitle: '', count: 1, brutto: 204.24 }],
  parts: [], suppliers: [], invoices: [],
};

// 1. Selecting the top result via Enter clears and unfocuses the search bar.
omniEl.focus();
omniEl.value = '150';
ctx.renderOmni(searchResult);
opened.length = 0;
dispatchKeydown(omniEl, 'Enter');

check('Enter on the top hit opens it', JSON.stringify(opened[0]) === JSON.stringify(['vehicle', 'FG21OXA']));
check('search bar is emptied afterwards', omniEl.value === '');
check('the clear (×) button is hidden again', clearBtn.hidden === true);
check('the search bar loses focus, and stays lost through the same keystroke',
  activeElement === bodyEl);

// 2. Return with nothing focused reaches the search bar.
activeElement = bodyEl;
dispatchKeydown({ _listeners: {} }, 'Enter');
check('Return with nothing focused moves focus to search', activeElement === omniEl);

// 3. Return must not steal a focused button's own activation.
activeElement = clearBtn;
dispatchKeydown({ _listeners: {} }, 'Enter');
check('Return does not steal focus from a focused button', activeElement === clearBtn);

// 4. Return must not reach a search bar hidden behind an open dialog.
activeElement = bodyEl;
store['gen-modal'].hidden = false;
dispatchKeydown({ _listeners: {} }, 'Enter');
check('Return is ignored while a modal dialog is open', activeElement === bodyEl);
store['gen-modal'].hidden = true;

// 5. "/" is unrelated to this change and must still work exactly as before,
//    including through a focused button — its guard was intentionally left
//    untouched.
activeElement = clearBtn;
dispatchKeydown({ _listeners: {} }, '/');
check('"/" still focuses search even from a focused button (unchanged behaviour)',
  activeElement === omniEl);

console.log(failures.length ? `\n${failures.length} check(s) failed` : '\nall checks passed');
process.exit(failures.length ? 1 : 0);
