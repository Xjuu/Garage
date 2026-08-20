/* Exercises the search bar's keyboard contract with real event bubbling, not
 * a reimplementation of it: Return jumping to search, arrow keys moving the
 * highlighted result, and Enter opening whichever one is highlighted.
 *
 * This exists because building the Return feature nearly shipped a real bug:
 * the omni input's own Enter handler calls resetOmni(), which blurs the
 * input. That blur happens synchronously, mid-bubble — so by the time the
 * same keydown event reached the document-level "Return focuses search"
 * handler, document.activeElement had already changed and the guard that
 * should have skipped it no longer matched. The result was the search bar
 * re-focusing itself in the same keystroke that was supposed to clear it.
 *
 * tools/ui-check.cjs's element stubs don't model focus() moving
 * document.activeElement, or listener bubbling with stopPropagation, or a
 * results list with real per-row classList/id state — they are built for
 * boot/render verification, not interaction. This script adds just enough of
 * a real two-level bubble (target, then document) and a minimal results-list
 * model to make that race, and the arrow-key selection logic, reproducible.
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
  const attrs = {};
  const el = {
    id, tagName, hidden: false, dataset: {}, value: '', _listeners: {},
    // Real elements only report .closest('.drawer.open') truthy when they
    // are actually inside the currently-open drawer's DOM subtree. This
    // stub is not a real tree, so tests opt an element into that case
    // explicitly by setting _inOpenDrawer, rather than modelling nesting.
    _inOpenDrawer: false,
    addEventListener(ev, fn) { (el._listeners[ev] ??= []).push(fn); },
    focus() { activeElement = el; },
    blur() { if (activeElement === el) activeElement = bodyEl; },
    setAttribute(name, value) { attrs[name] = String(value); },
    getAttribute(name) { return attrs[name] ?? null; },
    removeAttribute(name) { delete attrs[name]; },
    closest(sel) { return sel === '.drawer.open' && el._inOpenDrawer ? el : null; },
    isContentEditable: false,
    _attrs: attrs,
  };
  return el;
}

function makeResultsEl() {
  // A minimal model of what omni.js actually does to this element: parse hit
  // buttons out of an assigned innerHTML string (each carrying a stable id,
  // matching the real hitRow output), and support the per-row classList and
  // scrollIntoView calls that selectOmniHit makes on them.
  let items = [];
  const el = {
    id: 'omni-results', hidden: true,
    get innerHTML() {
      return items.map((i) => `<button class="omni-hit" id="${i.id}" data-kind="${i.kind}" data-ref="${i.ref}">`).join('');
    },
    set innerHTML(html) {
      items = [...html.matchAll(/<button class="omni-hit" id="([^"]*)" role="option"\s+data-kind="([^"]*)" data-ref="([^"]*)">/g)]
        .map((m) => ({ id: m[1], kind: m[2], ref: m[3], classes: new Set(), listeners: {} }));
    },
    querySelector(sel) {
      if (sel === '.omni-hit') return wrap(items[0]);
      return null;
    },
    querySelectorAll(sel) { return sel === '.omni-hit' ? items.map(wrap) : []; },
  };
  function wrap(it) {
    if (!it) return null;
    return {
      id: it.id,
      dataset: { kind: it.kind, ref: it.ref },
      classList: {
        add: (c) => it.classes.add(c),
        toggle: (c, force) => { force ? it.classes.add(c) : it.classes.delete(c); },
        contains: (c) => it.classes.has(c),
      },
      addEventListener(ev, fn) { (it.listeners[ev] ??= []).push(fn); },
      scrollIntoView() {},
    };
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
const shown = [];
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
  show: (view) => shown.push(view),
  openVehicle: (ref) => opened.push(['vehicle', ref]),
  openPart: (ref) => opened.push(['part', ref]),
  openInvoice: (ref) => opened.push(['invoice', ref]),
});
new vm.Script(src, { filename: 'omni.js' }).runInContext(ctx);

// A real two-level bubble: target's own listeners run first, in registration
// order (so both keydown handlers omni.js attaches to the input fire, exactly
// as a browser would), then document's, unless a target handler called
// stopPropagation().
function dispatchKeydown(target, key) {
  const e = {
    key, metaKey: false, ctrlKey: false, _stopped: false, _defaultPrevented: false,
    stopPropagation() { this._stopped = true; },
    preventDefault() { this._defaultPrevented = true; },
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

// A fixture with three hits — two vehicles, one part — standing in for a
// real /api/search response. renderOmni ranks vehicles before parts, so the
// DOM order (and therefore arrow-key order) is FG21OXA, AE18FJZ, then the
// part.
const searchResult = {
  total: 3,
  vehicles: [
    { kind: 'vehicle', ref: 'FG21OXA', title: 'FG21OXA', subtitle: '', count: 1, brutto: 204.24 },
    { kind: 'vehicle', ref: 'AE18FJZ', title: 'AE18FJZ', subtitle: '', count: 1, brutto: 178.09 },
  ],
  parts: [{ kind: 'part', ref: 'OESE020303', title: 'OESE020303', subtitle: '', count: 1, brutto: 209.31 }],
  suppliers: [], invoices: [],
};

function openTopResult() {
  omniEl.focus();
  omniEl.value = '150';
  ctx.renderOmni(searchResult);
  opened.length = 0;
}

// 1. With nothing arrowed, Enter opens the first hit — and selecting it
//    clears and unfocuses the search bar in the same keystroke.
openTopResult();
dispatchKeydown(omniEl, 'Enter');
check('Enter with no arrow-key move opens the first hit', JSON.stringify(opened[0]) === JSON.stringify(['vehicle', 'FG21OXA']));
check('search bar is emptied afterwards', omniEl.value === '');
check('the clear (×) button is hidden again', clearBtn.hidden === true);
check('the search bar loses focus, and stays lost through the same keystroke', activeElement === bodyEl);
check('aria-activedescendant is cleared once the list is gone', omniEl.getAttribute('aria-activedescendant') === null);

// 2. ArrowDown moves the highlight; Enter now opens the SECOND hit, not the
//    first — this is the actual feature being verified.
openTopResult();
dispatchKeydown(omniEl, 'ArrowDown');
check('aria-activedescendant follows the highlight to the second hit', omniEl.getAttribute('aria-activedescendant') === 'omni-hit-1');
dispatchKeydown(omniEl, 'Enter');
check('Enter after one ArrowDown opens the second hit, not the first', JSON.stringify(opened[0]) === JSON.stringify(['vehicle', 'AE18FJZ']));

// 3. ArrowDown twice more reaches the third and last hit, then clamps —
//    a fourth ArrowDown must not run off the end of the list.
openTopResult();
dispatchKeydown(omniEl, 'ArrowDown');
dispatchKeydown(omniEl, 'ArrowDown');
dispatchKeydown(omniEl, 'Enter');
check('two ArrowDowns reach the third hit', JSON.stringify(opened[0]) === JSON.stringify(['part', 'OESE020303']));

openTopResult();
for (let i = 0; i < 6; i++) dispatchKeydown(omniEl, 'ArrowDown'); // well past the end
dispatchKeydown(omniEl, 'Enter');
check('ArrowDown clamps at the last hit rather than wrapping', JSON.stringify(opened[0]) === JSON.stringify(['part', 'OESE020303']));

// 4. ArrowUp from the top clamps at zero rather than going negative or
//    wrapping to the bottom.
openTopResult();
dispatchKeydown(omniEl, 'ArrowUp');
dispatchKeydown(omniEl, 'ArrowUp');
dispatchKeydown(omniEl, 'Enter');
check('ArrowUp clamps at the first hit rather than wrapping', JSON.stringify(opened[0]) === JSON.stringify(['vehicle', 'FG21OXA']));

// 5. ArrowUp/Down after ArrowDown move relative to the current position, not
//    back to a fixed start — down, down, up should land on the second hit.
openTopResult();
dispatchKeydown(omniEl, 'ArrowDown');
dispatchKeydown(omniEl, 'ArrowDown');
dispatchKeydown(omniEl, 'ArrowUp');
dispatchKeydown(omniEl, 'Enter');
check('down, down, up lands back on the second hit', JSON.stringify(opened[0]) === JSON.stringify(['vehicle', 'AE18FJZ']));

// 6. Arrow keys with the dropdown closed (no query typed) must not throw and
//    must not do anything — there is nothing to move a highlight over.
activeElement = bodyEl;
omniEl.value = '';
ctx.closeOmni();
let threw = false;
try { dispatchKeydown(omniEl, 'ArrowDown'); } catch { threw = true; }
check('ArrowDown with the dropdown closed does not throw', !threw);

// 7. The reported bug, reproduced exactly: Return with nothing focused
//    (jumps to the empty search bar), then Return again with still nothing
//    typed and no date range. Nothing was ever searched, so the second
//    Return must do nothing — not silently land on the unfiltered Invoices
//    tab, which is what an empty double-Return used to do.
activeElement = bodyEl;
omniEl.value = '';
shown.length = 0;
dispatchKeydown({ _listeners: {} }, 'Enter'); // 1st Return: focuses the bar
check('first Return (nothing focused) reaches the search bar', activeElement === omniEl);
dispatchKeydown(omniEl, 'Enter'); // 2nd Return: bar is focused but still empty
check('second Return on a still-empty bar does not navigate anywhere', shown.length === 0);

// 8. A typed query that matched nothing must NOT fall back to Invoices —
//    the top search and the Invoices search box are related in exactly one
//    way (choosing a vehicle, then explicitly asking for its invoices), and
//    silently re-running an already-known-empty query as a second, unrelated
//    search used to be the opposite of that: a raw, unselected query
//    reappearing in a different box on its own.
omniEl.focus();
omniEl.value = 'no such plate';
ctx.renderOmni({ total: 0, vehicles: [], parts: [], suppliers: [], invoices: [] });
shown.length = 0;
dispatchKeydown(omniEl, 'Enter');
check('a query that matched nothing does not fall back to Invoices', shown.length === 0);
check('the unmatched query is left exactly as typed, not cleared', omniEl.value === 'no such plate');

// 9. A pure date range, with nothing typed, is still worth falling back to
//    Invoices for — no text is being carried across, so there is nothing to
//    correlate between the two searches, and dates scope the invoice list
//    directly.
omniEl.focus();
omniEl.value = '';
store['omni-from'].value = '2026-08-01';
ctx.closeOmni(); // no query typed, so nothing was ever rendered — just a date set
shown.length = 0;
dispatchKeydown(omniEl, 'Enter');
check('a date-only search still falls back to the Invoices tab', shown[0] === 'invoices');
store['omni-from'].value = '';

// 10. A plain page button holding focus — wherever the mouse left it after
//    the last click, not something the user is about to press Return on
//    again — must not block Return from reaching search. This is the
//    reported bug: Return "sometimes doesn't register", which was every
//    time focus happened to be sitting on any button anywhere on a page
//    this button-heavy.
activeElement = clearBtn;
dispatchKeydown({ _listeners: {} }, 'Enter');
check('Return still reaches search through a plain focused button', activeElement === omniEl);

// 10b. Inside the open drawer, a button is a real form action — "Save
//     changes", "Mark reviewed" — and Return has to activate it, the way
//     it would for any other on-screen form.
activeElement = clearBtn;
clearBtn._inOpenDrawer = true;
dispatchKeydown({ _listeners: {} }, 'Enter');
check('Return still activates a button inside the open drawer', activeElement === clearBtn);
clearBtn._inOpenDrawer = false;
activeElement = bodyEl;

// 11. Return must not reach a search bar hidden behind an open dialog.
activeElement = bodyEl;
store['gen-modal'].hidden = false;
dispatchKeydown({ _listeners: {} }, 'Enter');
check('Return is ignored while a modal dialog is open', activeElement === bodyEl);
store['gen-modal'].hidden = true;

// 12. "/" is unrelated to this change and must still work exactly as
//     before, including through a focused button — its guard was
//     intentionally left untouched.
activeElement = clearBtn;
dispatchKeydown({ _listeners: {} }, '/');
check('"/" still focuses search even from a focused button (unchanged behaviour)', activeElement === omniEl);

console.log(failures.length ? `\n${failures.length} check(s) failed` : '\nall checks passed');
process.exit(failures.length ? 1 : 0);
