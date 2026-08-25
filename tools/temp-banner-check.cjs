/* The "temporary account" and "read-only" banners have no JS behind them at
 * all — visibility is a pure CSS rule per line, keyed on <body data-temp="true">
 * and <body data-readonly="true"> respectively, which web.go's handleRoot
 * stamps for a TOTP-exempt and/or read-only account's session (see
 * data-role's own identical pattern, added earlier for role gating). This
 * checks both halves of that contract statically: each line's markup exists
 * and says what it's for, and the CSS hides each by default and shows only
 * the one whose attribute is present — independently, so both can show at
 * once for an account that's both temp and read-only.
 *
 * Usage: node tools/temp-banner-check.cjs
 * Exits non-zero if any half is missing.
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

function bannerLine(when) {
  const re = new RegExp(`<div class="temp-banner" data-when="${when}">([^<]*)</div>`);
  return html.match(re);
}

const temp = bannerLine('temp');
ok(!!temp, 'index.html has a temp-banner line for data-when="temp"');
ok(!!temp && /temporary/i.test(temp[1]),
  'its text actually says "temporary": ' + JSON.stringify(temp && temp[1]));

const readOnly = bannerLine('readonly');
ok(!!readOnly, 'index.html has a temp-banner line for data-when="readonly"');
ok(!!readOnly && /view.only|read.only/i.test(readOnly[1]),
  'its text actually says view/read-only: ' + JSON.stringify(readOnly && readOnly[1]));

// Both must sit outside every element that only renders for a signed-in
// session's data — i.e. they're in the always-present static markup, not
// something JS has to build.
ok(html.indexOf('class="temp-banner"') < html.indexOf('id="tabs"'),
  'the banners are static markup near the top of the page, before the nav');

ok(/\.temp-banner\s*\{[^}]*display:\s*none/.test(css),
  'both lines are hidden by default (display: none)');
ok(/body\[data-temp="true"\]\s*\.temp-banner\[data-when="temp"\]\s*\{[^}]*display:\s*(block|flex)/.test(css),
  'body[data-temp="true"] shows only the "temp" line');
ok(/body\[data-readonly="true"\]\s*\.temp-banner\[data-when="readonly"\]\s*\{[^}]*display:\s*(block|flex)/.test(css),
  'body[data-readonly="true"] shows only the "readonly" line');

process.exit(failed ? 1 : 0);
