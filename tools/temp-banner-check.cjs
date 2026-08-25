/* The "temporary account" banner has no JS behind it at all — visibility is
 * a pure CSS rule keyed on <body data-temp="true">, which web.go's
 * handleRoot stamps only for a TOTP-exempt account's session (see
 * data-role's own identical pattern, added earlier for role gating). This
 * checks the two halves of that contract statically: the markup exists and
 * says what it's for, and the CSS actually hides it by default and shows it
 * only under body[data-temp="true"].
 *
 * Usage: node tools/temp-banner-check.cjs
 * Exits non-zero if either half is missing.
 */

'use strict';

const fs = require('fs');
const path = require('path');

const ASSETS = path.join(__dirname, '..', 'internal', 'web', 'assets');
const html = fs.readFileSync(path.join(ASSETS, 'index.html'), 'utf8');
const css = fs.readFileSync(path.join(ASSETS, 'app.css'), 'utf8');

let failed = false;
function ok(cond, label) {
  console.log((cond ? 'ok  ' : 'FAIL') + ' - ' + label);
  if (!cond) failed = true;
}

const bannerMatch = html.match(/<div class="temp-banner">([^<]*)<\/div>/);
ok(!!bannerMatch, 'index.html has a .temp-banner element');
ok(!!bannerMatch && /temporary/i.test(bannerMatch[1]),
  'its text actually says "temporary": ' + JSON.stringify(bannerMatch && bannerMatch[1]));

// It must sit outside every element that only renders for a signed-in
// session's data — i.e. it's in the always-present static markup, not
// something JS has to build.
ok(html.indexOf('class="temp-banner"') < html.indexOf('id="tabs"'),
  'the banner is static markup near the top of the page, before the nav');

ok(/\.temp-banner\s*\{[^}]*display:\s*none/.test(css),
  'the banner is hidden by default (display: none)');
ok(/body\[data-temp="true"\]\s*\.temp-banner\s*\{[^}]*display:\s*(block|flex)/.test(css),
  'body[data-temp="true"] .temp-banner overrides that to visible');

process.exit(failed ? 1 : 0);
