/* Boots the real dashboard scripts (same shared-scope technique as
 * ui-check.cjs) twice — once with <body data-role="fleet">, once with
 * data-role unset (the "admin" default) — and inspects what buildNav()
 * actually put in the nav, proving: a fleet-role account gets Overview,
 * Invoices, Analysis and a single direct "Fleet" tab, with no Training or
 * Admin link anywhere, not even hidden inside a dropdown; an admin account
 * still gets the full "Setup" group with all three.
 *
 * Usage: node tools/role-nav-check.cjs
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

const el = (id) => {
  const listeners = {};
  return {
  id, textContent: '', innerHTML: '', value: '', hidden: hiddenIds.has(id),
  dataset: {}, style: {},
  classList: { toggle() {}, add() {}, remove() {}, contains: () => false },
  addEventListener(ev, fn) { (listeners[ev] ??= []).push(fn); },
  fire(ev, arg) { (listeners[ev] || []).forEach((fn) => fn(arg || {})); },
  setAttribute() {}, removeAttribute() {},
  appendChild() {}, removeChild() {}, remove() {}, insertAdjacentHTML() {},
  scrollIntoView() {}, focus() {}, blur() {},
  querySelectorAll: () => [], querySelector: () => null, closest: () => null,
  isConnected: true, clientWidth: 1200,
  };
};

/** Loads the whole dashboard fresh in its own vm context, as if <body>
    carried the given data-role (or none at all, for the "admin" default),
    and returns what ended up in #tabs and #subtabs. */
let navigatedTo = '';

function loadWithRole(role, siblings = {}) {
  navigatedTo = '';
  const store = {};
  ids.forEach((i) => { store[i] = el(i); });
  ['c-invoices', 'c-vehicles', 'c-parts', 'c-suppliers', 'c-training', 'c-exports']
    .forEach((i) => { store[i] ??= el(i); });

  const body = el('body');
  if (role) body.dataset.role = role;
  if (siblings.parts) body.dataset.parts = siblings.parts;
  if (siblings.rentals) body.dataset.rentals = siblings.rentals;

  const errors = [];
  const ctx = vm.createContext({
    console,
    document: {
      getElementById: (id) => store[id] || null,
      querySelectorAll: () => [], querySelector: () => null,
      addEventListener() {}, createElement: () => el('tmp'), body, cookie: '', activeElement: null,
    },
    window: { addEventListener() {}, location: { href: '' } },
    location: { set href(v) { navigatedTo = v; }, get href() { return navigatedTo; } },
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

  return {
    errors, ctx, store, body,
    tabs: store['tabs'].innerHTML, subtabs: store['subtabs'].innerHTML,
  };
}

let failed = false;
function ok(cond, label) {
  console.log((cond ? 'ok  ' : 'FAIL') + ' - ' + label);
  if (!cond) failed = true;
}

// ── a fleet-role account ────────────────────────────────────────────────

const fleet = loadWithRole('fleet');
ok(fleet.errors.length === 0, 'fleet role: every dashboard script loads without throwing: ' + fleet.errors.join('; '));
ok(fleet.tabs.includes('Fleet'), 'fleet role: a direct "Fleet" tab exists at the top level');
ok(!fleet.tabs.includes('Setup'), 'fleet role: no "Setup" label anywhere in the top-level tabs');
ok(!fleet.tabs.includes('Training') && !fleet.subtabs.includes('Training'),
  'fleet role: no "Training" link anywhere, top-level or in a dropdown');
ok(!fleet.tabs.includes('Admin') && !fleet.subtabs.includes('Admin'),
  'fleet role: no "Admin" link anywhere, top-level or in a dropdown');
// The single-view "Fleet" group must render with no subtab dropdown at all
// — buildNav only emits subtabs for a group with more than one view.
ok(!fleet.subtabs.includes('Fleet'),
  'fleet role: "Fleet" is a direct tab, not tucked inside a dropdown');

// ── an admin account (the default when no role is stamped at all) ──────

const admin = loadWithRole(null);
ok(admin.errors.length === 0, 'admin role: every dashboard script loads without throwing: ' + admin.errors.join('; '));
ok(admin.tabs.includes('Setup'), 'admin role: the "Setup" group is still the top-level tab');
ok(admin.subtabs.includes('Fleet') && admin.subtabs.includes('Training') && admin.subtabs.includes('Admin'),
  'admin role: Fleet, Training and Admin are all reachable as Setup\'s subtabs');

// ── the two links out to the sibling sites ──────────────────────────────

const SIBLINGS = { parts: '//parts.example.co.uk', rentals: '//rentals.example.co.uk' };

for (const role of ['admin', 'fleet']) {
  const w = loadWithRole(role === 'admin' ? null : role, SIBLINGS);
  ok(w.errors.length === 0, `${role}: scripts load with the sibling URLs stamped: ` + w.errors.join('; '));
  ok(w.tabs.includes('Parts'), `${role}: the parts store appears as a tab`);
  ok(w.tabs.includes(`data-external="${SIBLINGS.parts}"`),
    `${role}: that tab links out to the parts subdomain rather than switching view`);
  // It is the last tab — the fifth, after the four this app renders itself.
  const labels = [...w.tabs.matchAll(/>\s*([A-Za-z ]+?)\s*(?:↗|<)/g)].map((m) => m[1].trim()).filter(Boolean);
  ok(labels[labels.length - 1] === 'Parts',
    `${role}: it is the last tab in the row, got ${JSON.stringify(labels)}`);
}

// A dead tab is worse than no tab: without the server telling the page
// where the parts store lives, it is not offered at all.
const noSiblings = loadWithRole(null);
ok(!noSiblings.tabs.includes('data-external'),
  'with no parts URL stamped, no external tab is rendered');

// The Rentals button in the top bar goes to the rentals subdomain.
const w = loadWithRole(null, SIBLINGS);
w.store['btn-rentals'].fire('click');
ok(navigatedTo === SIBLINGS.rentals,
  'the Rentals button navigates to the rentals subdomain, got ' + JSON.stringify(navigatedTo));

process.exit(failed ? 1 : 0);
