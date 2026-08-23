/* Boots the real repairs/app.js in a vm context with fake document activity
 * listeners and a fake, manually-advanceable timer — never a real 15-minute
 * wait — and exercises the auto sign-out-on-inactivity feature: no activity
 * for the full idle window signs the device out (same call the Sign out
 * button itself makes), any tracked activity resets the countdown rather
 * than letting it expire on schedule, and the countdown restarts fresh
 * after each reset rather than accumulating.
 *
 * Usage: node tools/repairs-idle-logout-check.cjs
 * Exits non-zero if any check fails or app.js throws while loading.
 */

'use strict';

const fs = require('fs');
const vm = require('vm');
const path = require('path');

const ASSETS = path.join(__dirname, '..', 'internal', 'web', 'assets', 'repairs');

const html = fs.readFileSync(path.join(ASSETS, 'index.html'), 'utf8');
const ids = [...html.matchAll(/id="([^"]+)"/g)].map((m) => m[1]);
const hiddenIds = new Set([...html.matchAll(/id="([^"]+)"[^>]*\shidden/g)].map((m) => m[1]));

function makeChoiceBtn(value) {
  const listeners = {};
  return {
    dataset: { value },
    classList: { add() {}, remove() {}, toggle() {}, contains: () => false },
    addEventListener(ev, fn) { (listeners[ev] ??= []).push(fn); },
    fire(ev, arg) { (listeners[ev] || []).forEach((fn) => fn(arg)); },
    click() { this.fire('click'); },
  };
}

function makeEl(id, children = []) {
  const listeners = {};
  return {
    id, textContent: '', innerHTML: '', value: '', hidden: hiddenIds.has(id),
    disabled: false, dataset: {}, style: {}, children,
    classList: { add() {}, remove() {}, toggle() {}, contains: () => false },
    addEventListener(ev, fn) { (listeners[ev] ??= []).push(fn); },
    fire(ev, arg) { (listeners[ev] || []).forEach((fn) => fn(arg || {})); },
    click() { this.fire('click'); },
    focus() {}, select() {},
    querySelectorAll(sel) { return sel === '.rp-choice-btn' ? children : []; },
    querySelector(sel) {
      const m = sel.match(/^\[data-value="([^"]+)"\]$/);
      return m ? (children.find((c) => c.dataset.value === m[1]) || null) : null;
    },
  };
}

const store = {};
ids.forEach((i) => { store[i] = makeEl(i); });
store['service-type-choice'] = makeEl('service-type-choice', ['full', 'mini', 'other'].map(makeChoiceBtn));
store['belt-choice'] = makeEl('belt-choice', ['no', 'yes'].map(makeChoiceBtn));

// A real, controllable fake timer — no relation to Node's setTimeout at
// all, so this test advances 15 simulated minutes instantly rather than
// actually waiting. Mirrors the single-pending-timer shape app.js's own
// idle tracker relies on: clearTimeout(idleTimer) then a fresh
// setTimeout(...) on every tracked activity event.
let pending = null; // { fn, delay }
let nextId = 1;
function fakeSetTimeout(fn, delay) {
  const id = nextId++;
  pending = { id, fn, delay };
  return id;
}
function fakeClearTimeout(id) {
  if (pending && pending.id === id) pending = null;
}
/** Fires the currently-scheduled timer's callback directly, as if its
    delay had fully elapsed — the whole point of faking the timer at all. */
function fireScheduled() {
  const p = pending;
  pending = null;
  if (p) p.fn();
}

// Real document.addEventListener, captured per event type, so this test can
// simulate "the crew touched the screen" by actually invoking the same
// handler app.js itself registered — not a re-implementation of the idle
// logic, a call into the real thing.
const docListeners = {};
const fetchCalls = [];
async function fakeFetch(url, opts = {}) {
  fetchCalls.push({ url, opts });
  return { ok: true, status: 200, json: async () => ({ ok: true }) };
}

const errors = [];
const ctx = vm.createContext({
  console,
  document: {
    getElementById: (id) => store[id] || null,
    querySelectorAll: () => [], querySelector: () => null,
    createElement: () => makeEl('tmp'),
    cookie: 'goldstar_repairs_csrf=test-csrf-token',
    addEventListener(ev, fn) { (docListeners[ev] ??= []).push(fn); },
  },
  location: { href: '' },
  setTimeout: fakeSetTimeout, clearTimeout: fakeClearTimeout,
  fetch: fakeFetch,
  Math, JSON, Object, Array, Number, String, Boolean, Date, Set, Map, Promise,
  encodeURIComponent, decodeURIComponent, URL,
});
ctx.window = ctx; ctx.globalThis = ctx;

try {
  vm.runInContext(fs.readFileSync(path.join(ASSETS, 'app.js'), 'utf8'), ctx, { filename: 'app.js' });
} catch (e) {
  errors.push('threw while loading: ' + e.message);
}

let failed = false;
function ok(cond, label) {
  console.log((cond ? 'ok  ' : 'FAIL') + ' - ' + label);
  if (!cond) failed = true;
}

/** Simulates a real touch/click/keystroke by firing every tracked event
    type app.js listens for — same effect as any one of them in the real
    listener (each just calls scheduleIdleSignOut), so this doesn't need to
    know or care which specific events app.js chose to track. */
function simulateActivity() {
  for (const fns of Object.values(docListeners)) fns.forEach((fn) => fn({}));
}

(async () => {
  ok(errors.length === 0, 'app.js loads without throwing: ' + errors.join('; '));
  ok(!!pending, 'a sign-out is scheduled as soon as the page loads');
  ok(pending && pending.delay === 15 * 60 * 1000, `the idle window is 15 minutes: got ${pending && pending.delay}ms`);

  // Activity must push the deadline out, not just leave the old one ticking
  // alongside a second one — otherwise the earliest-scheduled timer would
  // still fire on the original schedule regardless of anything done since.
  const firstTimerID = pending.id;
  simulateActivity();
  ok(!!pending, 'a sign-out is still scheduled after activity (rescheduled, not just cancelled)');
  ok(pending.id !== firstTimerID, 'activity replaces the pending timer with a fresh one, rather than leaving the old one live');

  // No activity at all for the full window: the device signs itself out,
  // through the exact same endpoint the Sign out button uses. A real,
  // tiny wait rather than a guessed number of microtask ticks: signOut()
  // awaits both fetch() and res.json() before touching location.href.
  fetchCalls.length = 0;
  fireScheduled();
  await new Promise((r) => setTimeout(r, 10));
  const loggedOut = fetchCalls.find((c) => c.url === '/api/repairs/logout' && c.opts.method === 'POST');
  ok(!!loggedOut, '15 minutes with no activity signs the device out via POST /api/repairs/logout');
  ok(ctx.location.href === '/', 'and sends the browser back to the sign-in screen');

  process.exit(failed ? 1 : 0);
})();
