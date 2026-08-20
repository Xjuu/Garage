/* Parts counter. Deliberately self-contained — this is not app.js from the
   main dashboard: that file assumes a #tabs element and a dozen other
   things that only exist there, and would throw on load here. A handful of
   small helpers duplicated below is simpler and safer than trying to reuse
   a file built for a completely different page. */

'use strict';

const $ = (id) => document.getElementById(id);

function esc(v) {
  if (v === null || v === undefined) return '';
  return String(v)
    .replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;').replaceAll("'", '&#39;');
}

function num(n) {
  const v = Number(n) || 0;
  return Number.isInteger(v) ? String(v) : v.toFixed(2);
}

function readCookie(name) {
  return document.cookie.split('; ')
    .find((c) => c.startsWith(name + '='))?.split('=')[1] || '';
}

async function api(path, opts = {}) {
  const o = { headers: {}, ...opts };
  if (o.method && o.method !== 'GET') {
    o.headers['X-CSRF-Token'] = readCookie('goldstar_parts_csrf');
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

$('btn-signout').addEventListener('click', async () => {
  await api('/api/parts/logout', { method: 'POST' }).catch(() => {});
  location.href = '/';
});

// ── state ─────────────────────────────────────────────────────────────────

let pickedPart = null;    // { part_number, description, stock }
let pickedVehicle = null; // registration string

function updateVisibility() {
  $('step-vehicle').hidden = !pickedPart;
  $('picked-vehicle').hidden = !pickedVehicle;
  $('step-qty').hidden = !(pickedPart && pickedVehicle);
  $('picked-part').hidden = !pickedPart;
  $('step-part').hidden = !!pickedPart;
  $('vehicle-search').hidden = !!pickedVehicle;
  if (pickedVehicle) $('vehicle-results').hidden = true;
}

// ── step 1: part ─────────────────────────────────────────────────────────

function renderPickedPart() {
  $('picked-part-num').textContent = pickedPart.part_number;
  $('picked-part-desc').textContent = pickedPart.description || '';
  $('picked-part-stock').textContent = `${num(pickedPart.stock)} in stock`;
  const photo = $('picked-part-photo');
  const src = `/api/parts/photo/${encodeURIComponent(pickedPart.part_number)}`;
  photo.onerror = () => { photo.hidden = true; };
  photo.onload = () => { photo.hidden = false; };
  photo.src = src;
}

function pickPart(p) {
  pickedPart = p;
  renderPickedPart();
  updateVisibility();
  $('vehicle-search').focus();
}

$('picked-part-clear').addEventListener('click', () => {
  pickedPart = null;
  pickedVehicle = null;
  $('part-search').value = '';
  $('vehicle-search').value = '';
  updateVisibility();
  $('part-search').focus();
});

const searchParts = debounce(async () => {
  const q = $('part-search').value.trim();
  const results = $('part-results');
  if (!q) { results.hidden = true; results.innerHTML = ''; return; }

  let rows;
  try {
    rows = await api('/api/parts/search-parts?q=' + encodeURIComponent(q));
  } catch (e) {
    toast(e.message, true);
    return;
  }

  results.hidden = false;
  results.innerHTML = rows.length
    ? rows.map((p) => `
        <button type="button" data-part="${esc(p.part_number)}">
          <span class="r-main">
            <span class="r-title">${esc(p.part_number)}</span>
            <span class="r-sub">${esc(p.description || '')}</span>
          </span>
          <span class="r-stock${p.stock <= 0 ? ' low' : ''}">${num(p.stock)} left</span>
        </button>`).join('')
    : '<div class="r-empty">Nothing matches that.</div>';

  results.querySelectorAll('button[data-part]').forEach((b) => {
    const row = rows.find((p) => p.part_number === b.dataset.part);
    b.addEventListener('click', () => pickPart(row));
  });
}, 200);

$('part-search').addEventListener('input', searchParts);

// ── step 2: vehicle ──────────────────────────────────────────────────────

function pickVehicle(reg) {
  pickedVehicle = reg;
  $('picked-vehicle-reg').textContent = reg;
  updateVisibility();
  $('qty').focus();
  $('qty').select();
}

$('picked-vehicle-clear').addEventListener('click', () => {
  pickedVehicle = null;
  $('vehicle-search').value = '';
  updateVisibility();
  $('vehicle-search').focus();
});

const searchVehicles = debounce(async () => {
  const q = $('vehicle-search').value.trim();
  const results = $('vehicle-results');
  if (!q) { results.hidden = true; results.innerHTML = ''; return; }

  let rows;
  try {
    rows = await api('/api/parts/search-vehicles?q=' + encodeURIComponent(q));
  } catch (e) {
    toast(e.message, true);
    return;
  }

  results.hidden = false;
  results.innerHTML = rows.length
    ? rows.map((reg) => `
        <button type="button" data-reg="${esc(reg)}">
          <span class="r-main"><span class="r-title">${esc(reg)}</span></span>
        </button>`).join('')
    : '<div class="r-empty">No vehicle matches that.</div>';

  results.querySelectorAll('button[data-reg]').forEach((b) =>
    b.addEventListener('click', () => pickVehicle(b.dataset.reg)));
}, 200);

$('vehicle-search').addEventListener('input', searchVehicles);

// ── step 3: quantity, and logging it ────────────────────────────────────

$('btn-log').addEventListener('click', async () => {
  const err = $('log-err');
  err.textContent = '';
  const quantity = Number($('qty').value);
  if (!(quantity > 0)) {
    err.textContent = 'Enter a quantity greater than zero.';
    return;
  }

  const btn = $('btn-log');
  btn.disabled = true;
  btn.textContent = 'Logging…';
  try {
    const updated = await api('/api/parts/take', {
      method: 'POST',
      json: { part_number: pickedPart.part_number, vehicle_reg: pickedVehicle, quantity },
    });
    toast(`Logged ${num(quantity)} × ${pickedPart.part_number} for ${pickedVehicle}`);

    // Ready for the next part straight away, rather than sitting on what was
    // just done — this is a counter someone uses dozens of times in a row.
    pickedPart = null;
    pickedVehicle = null;
    $('part-search').value = '';
    $('vehicle-search').value = '';
    $('qty').value = '1';
    updateVisibility();
    $('part-search').focus();
    void updated; // the fresh stock count already reached the toast above
  } catch (e) {
    err.textContent = e.message;
  }
  btn.disabled = false;
  btn.textContent = 'Log it';
});

updateVisibility();
$('part-search').focus();
