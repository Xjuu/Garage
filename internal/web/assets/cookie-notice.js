/* A one-time disclosure that the site uses cookies — shown on both the
 * sign-in page and the dashboard, since a session cookie gets set at the
 * password step, before the dashboard is ever reached.
 *
 * Nothing here is a consent gate: the cookies in question (session, CSRF,
 * the 2FA pending cookie) are strictly necessary for sign-in and security,
 * not tracking or advertising, so there is nothing to opt out of — blocking
 * them would just break login. This is a disclosure, not a cookie wall.
 *
 * Dismissal is remembered in localStorage, not a cookie: using a cookie to
 * remember "you were told about the cookies" would be a little absurd, and
 * localStorage never leaves the browser, so it adds nothing to disclose.
 */

'use strict';

(() => {
  const SEEN_KEY = 'goldstar-cookie-notice-seen';
  const el = document.getElementById('cookie-notice');
  const dismiss = document.getElementById('cookie-notice-dismiss');
  if (!el || !dismiss) return;

  let seen = false;
  try { seen = localStorage.getItem(SEEN_KEY) === '1'; } catch { /* private browsing etc. */ }
  if (!seen) el.hidden = false;

  dismiss.addEventListener('click', () => {
    el.hidden = true;
    try { localStorage.setItem(SEEN_KEY, '1'); } catch { /* not fatal — it just reappears next visit */ }
  });
})();
