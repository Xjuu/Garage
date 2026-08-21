/* Bulk vehicle-data upload — a second, small page on the repairs site.
   Self-contained the same way app.js is: its own copies of the handful of
   helpers, no shared file with the main dashboard's script. */

'use strict';

const $ = (id) => document.getElementById(id);

function esc(v) {
  if (v === null || v === undefined) return '';
  return String(v)
    .replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;').replaceAll("'", '&#39;');
}

function readCookie(name) {
  return document.cookie.split('; ')
    .find((c) => c.startsWith(name + '='))?.split('=')[1] || '';
}

async function api(path, opts = {}) {
  const o = { headers: {}, ...opts };
  if (o.method && o.method !== 'GET') {
    o.headers['X-CSRF-Token'] = readCookie('goldstar_repairs_csrf');
  }
  if (o.json !== undefined) {
    o.headers['Content-Type'] = 'application/json';
    o.body = JSON.stringify(o.json);
    delete o.json;
  }
  const res = await fetch(path, o);
  if (res.status === 401) { location.href = '/'; throw new Error('signed out'); }
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    const err = new Error(data.error || `request failed (${res.status})`);
    err.status = res.status;
    err.body = data;
    throw err;
  }
  return data;
}

function toast(msg, bad = false) {
  const el = document.createElement('div');
  el.className = 'toast' + (bad ? ' bad' : '');
  el.textContent = msg;
  $('toasts').appendChild(el);
  setTimeout(() => el.remove(), bad ? 6000 : 3200);
}

function debounce(fn, ms) {
  let t;
  return (...a) => { clearTimeout(t); t = setTimeout(() => fn(...a), ms); };
}

$('btn-signout').addEventListener('click', async () => {
  await api('/api/repairs/logout', { method: 'POST' }).catch(() => {});
  location.href = '/';
});

// ── registration: search, browse, pick or type freely ──────────────────────

let currentReg = '';

async function doSearchReg() {
  const q = $('reg-input').value.trim();
  const browsing = !q;
  const results = $('reg-results');
  const label = $('reg-results-label');

  let rows;
  try {
    rows = await api('/api/repairs/search-vehicles?q=' + encodeURIComponent(q) + (browsing ? '&limit=60' : ''));
  } catch (e) {
    toast(e.message, true);
    return;
  }

  label.hidden = !browsing;
  label.textContent = browsing ? `All vehicles (${rows.length}) — keep typing to narrow it down` : '';

  results.hidden = false;
  results.innerHTML = rows.length
    ? rows.map((reg) => `<button type="button" data-reg="${esc(reg)}">${esc(reg)}</button>`).join('')
    : `<div class="r-empty">${browsing ? 'No vehicles in the registry yet — you can still type a new one below.' : 'No match — type the full registration to update a new vehicle.'}</div>`;

  results.querySelectorAll('button[data-reg]').forEach((b) =>
    b.addEventListener('click', () => selectReg(b.dataset.reg)));
}
const searchReg = debounce(doSearchReg, 200);

$('reg-input').addEventListener('input', () => {
  searchReg();
  const v = $('reg-input').value.trim();
  if (v.length >= 2) checkReg(v); else hideForm();
});
$('reg-input').addEventListener('focus', () => {
  if (!$('reg-input').value.trim()) doSearchReg();
});

function selectReg(reg) {
  $('reg-input').value = reg;
  $('reg-results').hidden = true;
  // Picked straight from the list, so it's already a real registration —
  // no need to ask whether to add it as a new one.
  hideNewRegPrompt();
  loadVehicle(reg);
}

// A typed (not picked-from-the-list) registration is checked before the
// form appears — the same protection NormalizeReg's confusable-character
// fix exists for: a typo must never silently start editing a car that
// already has a record under a slightly different spelling. Debounced the
// same 200ms as search, so it only fires once someone's stopped typing.
const checkReg = debounce(async (reg) => {
  if (reg !== $('reg-input').value.trim()) return;
  let exists;
  try {
    exists = (await api('/api/repairs/reg-exists?reg=' + encodeURIComponent(reg))).exists;
  } catch (e) {
    toast(e.message, true);
    return;
  }
  if (reg !== $('reg-input').value.trim()) return;

  if (exists) {
    hideNewRegPrompt();
    loadVehicle(reg);
  } else {
    $('form').hidden = true;
    showNewRegPrompt(reg);
  }
}, 200);

function showNewRegPrompt(reg) {
  $('new-reg-text').textContent = `${reg.toUpperCase()} isn't on file yet.`;
  $('new-reg-prompt').dataset.reg = reg;
  $('new-reg-prompt').hidden = false;
}
function hideNewRegPrompt() {
  $('new-reg-prompt').hidden = true;
}
$('new-reg-add').addEventListener('click', () => {
  const reg = $('new-reg-prompt').dataset.reg;
  hideNewRegPrompt();
  loadVehicle(reg);
});

function hideForm() {
  currentReg = '';
  $('form').hidden = true;
  hideNewRegPrompt();
}

const specFieldIds = {
  vin: 'vin', make: 'make', model: 'model', colour: 'colour',
  cylinder_capacity: 'cylinder-capacity', spare_keys: 'spare-keys',
  fuel_type: 'fuel-type', engine_size: 'engine-size', engine_number: 'engine-number',
  tyre_size: 'tyre-size', radio_code: 'radio-code',
};

async function loadVehicle(reg) {
  currentReg = reg;
  let v;
  try {
    v = await api('/api/repairs/upload/vehicle?reg=' + encodeURIComponent(reg));
  } catch (e) {
    toast(e.message, true);
    return;
  }
  if (currentReg !== reg) return; // a faster later lookup already won

  $('form-reg').textContent = reg.toUpperCase();
  for (const [key, id] of Object.entries(specFieldIds)) {
    $(id).value = v[key] || '';
  }
  $('form').hidden = false;
}

// ── save, with the upload throttle's re-verify step ─────────────────────

function currentSpecBody() {
  const body = { vehicle_reg: currentReg };
  for (const [key, id] of Object.entries(specFieldIds)) {
    body[key] = $(id).value.trim();
  }
  return body;
}

async function saveVehicle() {
  return api('/api/repairs/upload/vehicle', { method: 'POST', json: currentSpecBody() });
}

$('form').addEventListener('submit', async (e) => {
  e.preventDefault();
  if (!currentReg) return;
  const err = $('save-err');
  err.textContent = '';
  const btn = $('btn-save');
  btn.disabled = true;
  btn.textContent = 'Saving…';
  try {
    await saveVehicle();
    toast(`Updated ${currentReg.toUpperCase()}`);
  } catch (e) {
    if (e.status === 403 && e.body && e.body.error === 'reverify') {
      const ok = await requestReverify();
      if (ok) {
        try {
          await saveVehicle();
          toast(`Updated ${currentReg.toUpperCase()}`);
        } catch (e2) {
          err.textContent = e2.message;
        }
      }
    } else {
      err.textContent = e.message;
    }
  }
  btn.disabled = false;
  btn.textContent = 'Save changes';
});

// ── re-verify overlay ────────────────────────────────────────────────────

let reverifyPins = null;

function requestReverify() {
  return new Promise((resolve) => {
    const overlay = $('reverify-overlay');
    const err = $('reverify-err');
    err.textContent = '';
    overlay.hidden = false;

    const finish = (result) => {
      overlay.hidden = true;
      resolve(result);
    };

    $('reverify-cancel').onclick = () => finish(false);

    if (!reverifyPins) reverifyPins = setupPinBoxes('reverify-boxes', attempt);
    else reverifyPins.clear();

    async function attempt(code) {
      err.textContent = '';
      try {
        await api('/api/repairs/upload/verify', { method: 'POST', json: { code } });
        finish(true);
      } catch (e) {
        err.textContent = e.message;
        reverifyPins.clear();
      }
    }
  });
}

hideForm();
$('reg-input').focus();
