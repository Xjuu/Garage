/* Repairs log. Deliberately self-contained — not the main dashboard's
   app.js, which assumes a #tabs element and a dozen other things that only
   exist there and would throw on load here. A handful of small helpers
   duplicated below is simpler and safer than reusing a file built for a
   completely different page. */

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
  if (!res.ok) throw new Error(data.error || `request failed (${res.status})`);
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

function whenLocalShort(iso) {
  if (!iso) return '—';
  const d = new Date(iso);
  return isNaN(d) ? esc(iso) : d.toLocaleDateString('en-GB', { day: 'numeric', month: 'short', year: 'numeric' });
}

$('btn-signout').addEventListener('click', async () => {
  await api('/api/repairs/logout', { method: 'POST' }).catch(() => {});
  location.href = '/';
});

// ── state ─────────────────────────────────────────────────────────────────

let currentReg = '';
let serviceType = '';       // "full" | "mini" | "other"
let timingBeltChanged = false; // defaults to No

// ── registration: search, browse, pick or type freely ──────────────────────
// Unlike the (now-removed) parts counter, a registration here does not have
// to be picked from a list — a car's very first visit has no history and
// no registry row yet, so free typing always works; the list is there to
// help, not to gate.

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
    ? rows.map((reg) => `
        <button type="button" data-reg="${esc(reg)}">${esc(reg)}</button>`).join('')
    : `<div class="r-empty">${browsing ? 'No vehicles in the registry yet — you can still type a new one below.' : 'No match — type the full registration to log a new vehicle.'}</div>`;

  results.querySelectorAll('button[data-reg]').forEach((b) =>
    b.addEventListener('click', () => selectReg(b.dataset.reg)));
}
const searchReg = debounce(doSearchReg, 200);

$('reg-input').addEventListener('input', () => {
  searchReg();
  const v = $('reg-input').value.trim();
  if (v.length >= 2) checkReg(v); else hideHistoryAndForm();
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
  loadHistory(reg);
}

// A typed (not picked-from-the-list) registration is checked against the
// registry, invoices and the repairs log before the form appears — the
// same protection NormalizeReg's confusable-character fix exists for: a
// typo must never silently open a second history for a car that already
// has one. Debounced the same 200ms as search, so it only fires once
// someone's actually stopped typing.
const checkReg = debounce(async (reg) => {
  if (reg !== $('reg-input').value.trim()) return; // superseded by a later keystroke
  let exists;
  try {
    exists = (await api('/api/repairs/reg-exists?reg=' + encodeURIComponent(reg))).exists;
  } catch (e) {
    toast(e.message, true);
    return;
  }
  if (reg !== $('reg-input').value.trim()) return; // superseded while the request was in flight

  if (exists) {
    hideNewRegPrompt();
    loadHistory(reg);
  } else {
    $('history-section').hidden = true;
    $('form').hidden = true;
    prefillSpec(null);
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
  loadHistory(reg);
});

function hideHistoryAndForm() {
  currentReg = '';
  $('history-section').hidden = true;
  $('form').hidden = true;
  prefillSpec(null);
  hideNewRegPrompt();
}

async function loadHistory(reg) {
  currentReg = reg;
  let rows;
  try {
    rows = await api('/api/repairs/history?reg=' + encodeURIComponent(reg));
  } catch (e) {
    toast(e.message, true);
    return;
  }

  $('history-reg').textContent = reg.toUpperCase();
  $('history-section').hidden = false;
  $('history-list').innerHTML = rows.length
    ? rows.map(renderVisit).join('')
    : '<div class="rp-empty">Nothing logged for this vehicle yet — this will be its first visit.</div>';

  // The most recent visit already carries a full spec snapshot (every visit
  // does — see LogRepair) — reuse it instead of asking the crew to retype a
  // VIN or radio code that's already on file for a car that's been in before.
  prefillSpec(rows[0] || null);
  $('form').hidden = false;
}

const specFieldIds = {
  vin: 'vin', make: 'make', model: 'model', colour: 'colour',
  cylinder_capacity: 'cylinder-capacity', spare_keys: 'spare-keys',
  fuel_type: 'fuel-type', engine_size: 'engine-size', engine_number: 'engine-number',
  tyre_size: 'tyre-size', radio_code: 'radio-code', oil_amount: 'oil-amount',
};

function prefillSpec(latest) {
  for (const [key, id] of Object.entries(specFieldIds)) {
    $(id).value = latest ? (latest[key] || '') : '';
  }
  const note = $('spec-prefill-note');
  if (latest) {
    note.hidden = false;
    note.textContent = `Filled in from the visit on ${whenLocalShort(latest.service_date)} — change anything that's out of date.`;
  } else {
    note.hidden = true;
  }
}

function renderVisit(v) {
  const type = v.service_type === 'other' ? (v.service_type_other || 'Other') :
    v.service_type === 'full' ? 'Full service' : 'Mini service';
  const meta = [
    v.mileage ? `${Math.round(v.mileage).toLocaleString('en-GB')} miles` : '',
    v.timing_belt_changed ? 'timing belt changed' : '',
  ].filter(Boolean).join(' · ');
  return `
    <div class="rp-visit">
      <div class="rp-visit-head">
        <span class="rp-visit-type">${esc(type)}</span>
        <span class="rp-visit-date">${whenLocalShort(v.service_date)}</span>
      </div>
      ${meta ? `<div class="rp-visit-meta">${esc(meta)}</div>` : ''}
      ${v.description ? `<div class="rp-visit-desc">${esc(v.description)}</div>` : ''}
    </div>`;
}

// ── service type / timing belt choice buttons ───────────────────────────

function wireChoice(containerId, onPick) {
  $(containerId).querySelectorAll('.rp-choice-btn').forEach((b) => {
    b.addEventListener('click', () => {
      $(containerId).querySelectorAll('.rp-choice-btn').forEach((x) => x.classList.remove('active'));
      b.classList.add('active');
      onPick(b.dataset.value);
    });
  });
}

wireChoice('service-type-choice', (v) => {
  serviceType = v;
  $('service-type-other').hidden = v !== 'other';
  if (v === 'other') $('service-type-other').focus();
});

wireChoice('belt-choice', (v) => { timingBeltChanged = v === 'yes'; });
// Default to "No" — a worker who never touches this control still logs a
// visit that correctly says no belt was changed, not an unset value.
$('belt-choice').querySelector('[data-value="no"]').classList.add('active');

// ── submit ───────────────────────────────────────────────────────────────

$('form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const err = $('log-err');
  err.textContent = '';

  if (!currentReg) {
    err.textContent = 'Enter a registration first.';
    return;
  }
  if (!serviceType) {
    err.textContent = 'Choose a service type.';
    return;
  }
  const serviceTypeOther = $('service-type-other').value.trim();
  if (serviceType === 'other' && !serviceTypeOther) {
    err.textContent = 'Describe the service type.';
    $('service-type-other').focus();
    return;
  }

  const btn = $('btn-log');
  btn.disabled = true;
  btn.textContent = 'Logging…';
  try {
    await api('/api/repairs/log', {
      method: 'POST',
      json: {
        vehicle_reg: currentReg,
        service_type: serviceType,
        service_type_other: serviceTypeOther,
        mileage: Number($('mileage').value) || 0,
        timing_belt_changed: timingBeltChanged,
        description: $('description').value.trim(),
        vin: $('vin').value.trim(),
        make: $('make').value.trim(),
        model: $('model').value.trim(),
        colour: $('colour').value.trim(),
        cylinder_capacity: $('cylinder-capacity').value.trim(),
        spare_keys: $('spare-keys').value.trim(),
        fuel_type: $('fuel-type').value.trim(),
        engine_size: $('engine-size').value.trim(),
        engine_number: $('engine-number').value.trim(),
        tyre_size: $('tyre-size').value.trim(),
        radio_code: $('radio-code').value.trim(),
        oil_amount: $('oil-amount').value.trim(),
      },
    });
    toast(`Logged for ${currentReg.toUpperCase()}`);
    resetForm();
    loadHistory(currentReg); // show the entry just logged at the top of the list
  } catch (e) {
    err.textContent = e.message;
  }
  btn.disabled = false;
  btn.textContent = 'Log visit';
});

function resetForm() {
  serviceType = '';
  timingBeltChanged = false;
  $('service-type-choice').querySelectorAll('.rp-choice-btn').forEach((b) => b.classList.remove('active'));
  $('service-type-other').hidden = true;
  $('service-type-other').value = '';
  $('belt-choice').querySelectorAll('.rp-choice-btn').forEach((b) => b.classList.remove('active'));
  $('belt-choice').querySelector('[data-value="no"]').classList.add('active');
  $('mileage').value = '';
  $('description').value = '';
  ['vin', 'make', 'model', 'colour', 'cylinder-capacity', 'spare-keys',
    'fuel-type', 'engine-size', 'engine-number', 'tyre-size', 'radio-code', 'oil-amount']
    .forEach((id) => { $(id).value = ''; });
}

hideHistoryAndForm();
$('reg-input').focus();
