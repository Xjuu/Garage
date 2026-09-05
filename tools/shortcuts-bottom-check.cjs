/* The shortcuts bar is pinned to the bottom of the viewport as page-level
 * chrome, not part of the scrolling header — this has no interactive JS of
 * its own (renderSectionShortcuts still just writes into #section-shortcuts
 * by id, wherever it lives), so what actually needs checking is the static
 * HTML/CSS contract:
 *   - the markup sits outside <header>, as a sibling after <main>
 *   - it's fixed to the bottom, one row that scrolls rather than wraps
 *     (a second line would make its height unpredictable)
 *   - main, and every other bottom-fixed element (toasts, the job console,
 *     the cookie notice), leave room for it via the shared --shortcuts-h
 *     variable, rather than each hard-coding a guess at its height
 *   - that variable collapses to 0 on the mobile breakpoint, where the bar
 *     itself disappears and nothing needs to make room for it any more
 *
 * Usage: node tools/shortcuts-bottom-check.cjs
 * Exits non-zero if any half of that contract is missing.
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

// ── markup lives outside <header>, after <main> closes ──────────────────

const headerOpen = html.indexOf('<header class="topbar">');
const headerClose = html.indexOf('</header>');
const barMarkup = html.indexOf('class="shortcuts-bar"');
const mainClose = html.indexOf('</main>');

ok(headerOpen !== -1 && headerClose !== -1 && barMarkup !== -1 && mainClose !== -1,
  'all four landmarks (<header>, </header>, .shortcuts-bar, </main>) are present in index.html');
ok(barMarkup < headerOpen || barMarkup > headerClose,
  'the shortcuts bar markup is not nested inside <header>');
ok(barMarkup > mainClose,
  'the shortcuts bar comes after </main> — page-level chrome, not header content');

// #section-shortcuts must still exist exactly once, wherever the bar lives —
// renderSectionShortcuts() finds it purely by id.
const sectionShortcutsCount = (html.match(/id="section-shortcuts"/g) || []).length;
ok(sectionShortcutsCount === 1, `#section-shortcuts appears exactly once: found ${sectionShortcutsCount}`);

// ── CSS: fixed to the bottom, one row, scrolls instead of wrapping ──────

const barRuleMatch = css.match(/\.shortcuts-bar\s*\{([^}]*)\}/);
ok(!!barRuleMatch, '.shortcuts-bar has its own CSS rule');
const barRule = barRuleMatch ? barRuleMatch[1] : '';

ok(/position:\s*fixed/.test(barRule), '.shortcuts-bar is position: fixed');
ok(/bottom:\s*0\b/.test(barRule), '.shortcuts-bar is pinned to bottom: 0');
ok(/flex-wrap:\s*nowrap/.test(barRule),
  '.shortcuts-bar never wraps to a second line (flex-wrap: nowrap)');
ok(/overflow-x:\s*auto/.test(barRule),
  '.shortcuts-bar scrolls horizontally instead — "fits" by scrolling, not by growing');

// ── everything that shares the bottom of the viewport reads the same
//    --shortcuts-h variable, instead of each guessing its own offset ────

ok(/--shortcuts-h:\s*[\d.]+px/.test(css), ':root defines --shortcuts-h as a concrete height');

for (const [selector, cssName] of [
  ['main', 'main'],
  ['\\.toasts', '.toasts'],
  ['\\.console', '.console'],
  ['\\.cookie-notice', '.cookie-notice'],
]) {
  const re = new RegExp(`${selector}\\s*\\{[^}]*var\\(--shortcuts-h\\)`, 's');
  // main's rule is written across multiple lines with a comment above it —
  // match loosely against the whole file rather than requiring the
  // selector and the var() to share one single-line capture.
  const found = css.includes('var(--shortcuts-h)') && new RegExp(`${cssName.replace('.', '\\.')}[\\s\\S]{0,300}?var\\(--shortcuts-h\\)`).test(css);
  ok(found, `${cssName} accounts for --shortcuts-h in its own positioning/padding`);
}

// ── mobile breakpoint gives the space back in one place ─────────────────

const mobileBlockMatch = css.match(/@media \(max-width: 720px\) \{\s*:root \{ --shortcuts-h: 0px; \}\s*\.shortcuts-bar \{ display: none; \}/);
ok(!!mobileBlockMatch,
  'the mobile breakpoint resets --shortcuts-h to 0 in the same rule that hides the bar');

process.exit(failed ? 1 : 0);
