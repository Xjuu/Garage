/* Boots the real repairs/pinbox.js + repairs/pin.js — the sign-in page
 * itself — in a vm context with a fake DOM. This is the exact page a user
 * reported as "doesn't jump to the next box and doesn't log in at all",
 * which is what prompted rebuilding pinbox.js around one real input
 * instead of six separately-focused ones; this harness is the regression
 * test for that page specifically, not just the widget in isolation
 * (already covered from the upload page's re-verify prompt in
 * repairs-upload-check.cjs).
 *
 * Usage: node tools/repairs-pin-check.cjs
 * Exits non-zero if any check fails or a loaded script throws.
 */

'use strict';

const fs = require('fs');
const vm = require('vm');
const path = require('path');

const ASSETS = path.join(__dirname, '..', 'internal', 'web', 'assets', 'repairs');

const html = fs.readFileSync(path.join(ASSETS, 'pin.html'), 'utf8');
const ids = [...html.matchAll(/id="([^"]+)"/g)].map((m) => m[1]);

function makeEl(id) {
  const listeners = {};
  return {
    id, textContent: '', innerHTML: '', value: '', disabled: false,
    addEventListener(ev, fn) { (listeners[ev] ??= []).push(fn); },
    fire(ev, arg) { (listeners[ev] || []).forEach((fn) => fn(arg || { preventDefault() {} })); },
  };
}

function makeDisplayBox() {
  const classes = new Set();
  return {
    textContent: '',
    classList: {
      add: (c) => classes.add(c), remove: (c) => classes.delete(c),
      toggle: (c, on) => { (on ?? !classes.has(c)) ? classes.add(c) : classes.delete(c); },
      contains: (c) => classes.has(c),
    },
  };
}
function makeHiddenInput() {
  const listeners = {};
  return {
    value: '',
    addEventListener(ev, fn) { (listeners[ev] ??= []).push(fn); },
    fire(ev, arg) { (listeners[ev] || []).forEach((fn) => fn(arg)); },
    focus() { this.focused = true; },
  };
}
function makePinGroup() {
  const boxes = Array.from({ length: 6 }, makeDisplayBox);
  const hidden = makeHiddenInput();
  return {
    boxes, hidden,
    querySelectorAll(sel) { return sel === '.pin-box' ? boxes : []; },
    querySelector(sel) { return sel === '.pin-hidden-input' ? hidden : null; },
  };
}

const store = {};
ids.forEach((i) => { store[i] = makeEl(i); });
const pinGroup = makePinGroup();
store['pin-boxes'] = pinGroup;

let loginAccepts = '602314';
const fetchCalls = [];
async function fakeFetch(url, opts = {}) {
  fetchCalls.push({ url, opts });
  const body = JSON.parse(opts.body);
  if (body.code === loginAccepts) {
    return { ok: true, status: 200, json: async () => ({ ok: true }) };
  }
  return { ok: false, status: 401, json: async () => ({ error: 'incorrect code' }) };
}

const errors = [];
const ctx = vm.createContext({
  console,
  document: { getElementById: (id) => store[id] || null },
  location: { href: '', reloaded: false },
  setTimeout, clearTimeout,
  requestAnimationFrame: (fn) => setTimeout(fn, 0),
  fetch: fakeFetch,
  JSON, Object, Array, Promise,
});
ctx.window = ctx; ctx.globalThis = ctx;
ctx.location.reload = () => { ctx.location.reloaded = true; };

try {
  vm.runInContext(fs.readFileSync(path.join(ASSETS, 'pinbox.js'), 'utf8'), ctx, { filename: 'pinbox.js' });
  vm.runInContext(fs.readFileSync(path.join(ASSETS, 'pin.js'), 'utf8'), ctx, { filename: 'pin.js' });
} catch (e) {
  errors.push('threw while loading: ' + e.message);
}

const wait = (ms) => new Promise((r) => setTimeout(r, ms));

let failed = false;
function ok(cond, label) {
  console.log((cond ? 'ok  ' : 'FAIL') + ' - ' + label);
  if (!cond) failed = true;
}

function typeCode(code) {
  for (const digit of code) {
    pinGroup.hidden.value += digit;
    pinGroup.hidden.fire('input');
  }
}

(async () => {
  ok(errors.length === 0, 'pinbox.js and pin.js load without throwing: ' + errors.join('; '));
  await wait(10); // focus is deferred a frame — see pinbox.js's own comment on why
  ok(pinGroup.hidden.focused === true, 'the code field is focused as soon as the page loads');

  // ── wrong code ──
  typeCode('000000');
  await wait(10);
  ok(fetchCalls.length === 1 && fetchCalls[0].url === '/api/repairs/login',
    'typing all 6 digits auto-submits to /api/repairs/login: ' + JSON.stringify(fetchCalls[0]));
  ok(store['err'].textContent === 'incorrect code', 'a wrong code shows the real error: ' + store['err'].textContent);
  ok(ctx.location.reloaded === false, 'a wrong code does not reload the page');
  ok(pinGroup.hidden.value === '', 'a wrong code clears the field so the crew can try again');

  // ── correct code, typed digit by digit ──
  fetchCalls.length = 0;
  typeCode(loginAccepts);
  await wait(10);
  ok(fetchCalls.length === 1, 'the correct code also auto-submits on the 6th digit');
  const sent = JSON.parse(fetchCalls[0].opts.body);
  ok(sent.code === loginAccepts, 'the submitted code matches exactly what was typed: ' + sent.code);
  ok(ctx.location.reloaded === true, 'a correct code reloads the page (server then serves the real one)');

  // ── correct code, pasted in one go, submitted via the Continue button ──
  ctx.location.reloaded = false;
  fetchCalls.length = 0;
  pinGroup.hidden.value = loginAccepts;
  pinGroup.hidden.fire('input'); // a paste fires one 'input' with the whole value, same as typing
  await wait(10);
  ok(ctx.location.reloaded === true, 'a pasted 6-digit code completes and logs in the same as typing it');

  process.exit(failed ? 1 : 0);
})();
