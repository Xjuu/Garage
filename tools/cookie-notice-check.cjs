/* Exercises cookie-notice.js against the real file: shown on a first visit,
 * dismissed and remembered via localStorage (not a cookie — using a cookie
 * to remember "you were told about the cookies" would be its own small
 * joke), and staying hidden on a second "visit" that already has the flag
 * set.
 *
 * Usage: node tools/cookie-notice-check.cjs
 */

'use strict';

const fs = require('fs');
const vm = require('vm');
const path = require('path');

const src = fs.readFileSync(
  path.join(__dirname, '..', 'internal', 'web', 'assets', 'cookie-notice.js'), 'utf8');

let failed = 0;
function check(label, ok) {
  console.log((ok ? 'ok  - ' : 'FAIL- ') + label);
  if (!ok) failed++;
}

function makeLocalStorage() {
  const store = {};
  return {
    getItem: (k) => (k in store ? store[k] : null),
    setItem: (k, v) => { store[k] = String(v); },
    removeItem: (k) => { delete store[k]; },
  };
}

function run(localStorage) {
  const notice = { hidden: true };
  const listeners = [];
  const dismiss = { addEventListener: (ev, fn) => { if (ev === 'click') listeners.push(fn); } };
  const context = {
    document: { getElementById: (id) => (id === 'cookie-notice' ? notice : id === 'cookie-notice-dismiss' ? dismiss : null) },
    localStorage,
    console,
  };
  vm.createContext(context);
  new vm.Script(src, { filename: 'cookie-notice.js' }).runInContext(context);
  return { notice, click: () => listeners.forEach((fn) => fn()) };
}

// 1. First visit: nothing in storage yet, the notice must show.
{
  const ls = makeLocalStorage();
  const { notice } = run(ls);
  check('shown on a first visit (nothing dismissed yet)', notice.hidden === false);
}

// 2. Dismissing hides it immediately and records the flag — in
//    localStorage, never as a cookie.
{
  const ls = makeLocalStorage();
  const { notice, click } = run(ls);
  click();
  check('dismissing hides the notice', notice.hidden === true);
  check('dismissal is recorded in localStorage, not a cookie',
    ls.getItem('goldstar-cookie-notice-seen') === '1');
}

// 3. A later "visit" — a fresh script run against storage that already has
//    the flag — must not show it again.
{
  const ls = makeLocalStorage();
  run(ls).click(); // first visit, dismiss
  const { notice } = run(ls); // simulates a page reload / new visit
  check('stays hidden on a later visit once dismissed', notice.hidden === true);
}

// 4. localStorage being unavailable (private browsing, a locked-down
//    browser) must not throw and break the rest of the page.
{
  const throwing = {
    getItem() { throw new Error('blocked'); },
    setItem() { throw new Error('blocked'); },
  };
  let threw = false;
  try {
    const { notice, click } = run(throwing);
    click();
  } catch {
    threw = true;
  }
  check('a blocked localStorage does not throw', !threw);
}

if (failed) {
  console.log(`\n${failed} check(s) failed.`);
  process.exit(1);
}
console.log('\nall checks passed.');
