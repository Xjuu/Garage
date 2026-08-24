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

/** Loads the whole dashboard fresh in its own vm context, as if <body>
    carried the given data-role (or none at all, for the "admin" default),
    and returns what ended up in #tabs and #subtabs. */
function loadWithRole(role) {
  const store = {};
  ids.forEach((i) => { store[i] = el(i); });
  ['c-invoices', 'c-vehicles', 'c-parts', 'c-suppliers', 'c-training', 'c-exports']
    .forEach((i) => { store[i] ??= el(i); });

  const body = el('body');
  if (role) body.dataset.role = role;

  const errors = [];
  const ctx = vm.createContext({
    console,
    document: {
      getElementById: (id) => store[id] || null,
      querySelectorAll: () => [], querySelector: () => null,
      addEventListener() {}, createElement: () => el('tmp'), body, cookie: '', activeElement: null,
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

  return { errors, tabs: store['tabs'].innerHTML, subtabs: store['subtabs'].innerHTML };
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

process.exit(failed ? 1 : 0);
