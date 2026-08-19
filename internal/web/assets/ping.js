/* Round-trip time from this browser to the server.
 *
 * This is what the person sitting at the machine actually experiences: the
 * whole path, browser to Cloudflare to the tunnel to the app and back. It is
 * deliberately not a server-side figure — the server measuring itself would
 * report a number that no user ever feels.
 */

'use strict';

const PING_EVERY = 20000;   // often enough to notice a problem, rare enough to ignore
const PING_SLOW = 400;      // beyond this the interface feels sluggish
const PING_SAMPLES = 3;     // median of the last few, so one blip does not shout

const pingHistory = [];

function median(values) {
  const sorted = [...values].sort((a, b) => a - b);
  return sorted[Math.floor(sorted.length / 2)];
}

async function measurePing() {
  const dot = $('ping-dot');
  const label = $('ping-ms');
  const wrap = $('ping');
  if (!dot || !label) return;

  const started = performance.now();
  try {
    const res = await fetch('/api/ping', { cache: 'no-store' });
    // A signed-out session is not a network problem, and saying "offline"
    // would send someone hunting for a fault that is not there.
    if (res.status === 401) {
      pingHistory.length = 0;
      dot.className = 'ping-dot';
      wrap.classList.remove('down');
      label.textContent = 'signed out';
      wrap.title = 'The session has expired — reload to sign in again';
      return;
    }
    if (!res.ok) throw new Error(String(res.status));
    await res.text();
  } catch {
    // A failure is worth showing plainly: the dashboard looks alive while
    // being unable to reach anything, which is the confusing case.
    pingHistory.length = 0;
    dot.className = 'ping-dot down';
    wrap.classList.add('down');
    label.textContent = 'offline';
    wrap.title = 'Cannot reach the server';
    return;
  }

  const ms = Math.round(performance.now() - started);
  pingHistory.push(ms);
  if (pingHistory.length > PING_SAMPLES) pingHistory.shift();

  const shown = median(pingHistory);
  wrap.classList.remove('down');
  dot.className = 'ping-dot ' + (shown <= PING_SLOW ? 'good' : 'slow');
  label.textContent = shown + ' ms';
  wrap.title = `Round trip from this browser to the server: ${ms} ms now, `
    + `${shown} ms median of the last ${pingHistory.length}`;
}

// Measured once the page has settled, so the first reading is not inflated by
// the work of loading everything else.
function startPing() {
  setTimeout(measurePing, 800);
  setInterval(measurePing, PING_EVERY);

  // A tab that has been in the background for hours shows a stale figure;
  // refresh it the moment someone looks again.
  document.addEventListener('visibilitychange', () => {
    if (!document.hidden) measurePing();
  });
}
