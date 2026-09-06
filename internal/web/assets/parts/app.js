/* Goldstar Parts store — the counter at parts.<domain>.
 *
 * A barcode scanner is a keyboard that types a code and presses Return, so
 * the whole counter is one focused text field: scan, see what it is, add or
 * take some off. Everything else on the page is there to serve that field,
 * which is why focus is returned to it after every action. */

'use strict';

const $ = (id) => document.getElementById(id);

function esc(v) {
  if (v === null || v === undefined) return '';
  return String(v)
    .replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;').replaceAll("'", '&#39;');
}

const nf = new Intl.NumberFormat('en-GB', { minimumFractionDigits: 2, maximumFractionDigits: 2 });
const money = (n) => nf.format(Number(n) || 0);
/** Quantities are counts, not money: 3 should read as "3", not "3.00", but
    a half-litre of something still has to show as 0.5. */
const qty = (n) => String(Math.round((Number(n) || 0) * 1000) / 1000);

function readCookie(name) {
  return document.cookie.split('; ')
    .find((c) => c.startsWith(name + '='))?.split('=')[1] || '';
}

async function api(path, opts = {}) {
  const o = { headers: {}, ...opts };
  if (o.method && o.method !== 'GET') {
    if (path !== '/api/logout' && document.body.dataset.readonly === 'true') {
      throw new Error('This is a view-only account — changes are disabled.');
    }
    o.headers['X-CSRF-Token'] = readCookie('goldstar_csrf');
  }
  if (o.json !== undefined) {
    o.headers['Content-Type'] = 'application/json';
    o.body = JSON.stringify(o.json);
    delete o.json;
  }
  const res = await fetch(path, o);
  if (res.status === 401) { location.href = '/'; throw new Error('signed out'); }
  const data = await res.json().catch(() => ({}));
  if (!res.ok) { const e = new Error(data.error || `request failed (${res.status})`); e.status = res.status; throw e; }
  return data;
}

function toast(msg, bad = false) {
  const el = document.createElement('div');
  el.className = 'toast' + (bad ? ' bad' : '');
  el.textContent = msg;
  $('toasts').appendChild(el);
  setTimeout(() => el.remove(), bad ? 6000 : 3200);
}

const REASONS = { received: 'Received', used: 'Fitted', correction: 'Correction' };

// ── state ─────────────────────────────────────────────────────────────────

const state = { view: 'counter', part: null };

const viewLoaders = {};

function show(view) {
  state.view = view;
  document.querySelectorAll('#tabs button').forEach((b) =>
    b.setAttribute('aria-selected', String(b.dataset.view === view)));
  document.querySelectorAll('.view').forEach((s) =>
    s.classList.toggle('active', s.id === 'view-' + view));
  viewLoaders[view]?.().catch((e) => toast(e.message, true));
  if (view === 'counter') focusScan();
}

document.querySelectorAll('#tabs button').forEach((b) =>
  b.addEventListener('click', () => show(b.dataset.view)));

/** The scan field is the counter's whole interface, so it takes focus back
    after every action — a scanner fires straight into whatever is focused,
    and a code typed into nothing is a code lost. */
function focusScan() {
  const el = $('scan');
  el.focus();
  el.select();
}

// ── scanning ──────────────────────────────────────────────────────────────

$('scan').addEventListener('keydown', (e) => {
  if (e.key !== 'Enter') return;
  e.preventDefault();
  const code = $('scan').value.trim();
  if (code) scan(code);
});

async function scan(barcode) {
  $('ad-err').textContent = '';
  try {
    const part = await api(`/api/stock/scan?barcode=${encodeURIComponent(barcode)}`);
    state.part = part;
    $('new-part').hidden = true;
    renderScanned(part);
    $('scan-hint').textContent = `Scanned ${part.barcode}`;
    loadPartMovements(part.id);
  } catch (e) {
    if (e.status === 404) {
      // A barcode nobody has entered yet is the normal way a new line
      // starts, so the counter offers to add it rather than erroring.
      state.part = null;
      $('scanned').hidden = true;
      $('np-barcode').value = barcode;
      $('new-part').hidden = false;
      $('scan-hint').textContent = `${barcode} is not on file yet`;
      $('np-number').focus();
      return;
    }
    toast(e.message, true);
  }
}

function renderScanned(p) {
  $('scanned').hidden = false;
  $('sc-part').textContent = p.part_number || p.barcode;
  $('sc-desc').textContent = p.description || '';
  $('sc-qty').textContent = qty(p.quantity);
  $('sc-low').innerHTML = p.low ? '<span class="pill flag">Low</span>' : '';

  const bits = [];
  bits.push(`<span class="mono">${esc(p.barcode)}</span>`);
  if (p.location) bits.push(`Shelf ${esc(p.location)}`);
  if (p.unit_cost) bits.push(`£${money(p.unit_cost)} each`);
  bits.push(p.fits_make
    ? `<span class="pill">Fits ${esc([p.fits_make, p.fits_model].filter(Boolean).join(' '))} only</span>`
    : '<span class="pill muted">Fits anything</span>');
  $('sc-meta').innerHTML = bits.join(' · ');
}

async function loadPartMovements(id) {
  const moves = await api(`/api/stock/parts/${id}/movements?limit=15`);
  $('sc-moves').innerHTML = moves.length
    ? moves.map(movementRow).join('')
    : '<tr><td colspan="6" class="empty">Nothing yet</td></tr>';
}

function movementRow(m) {
  return `<tr>
    <td class="mono">${esc((m.created_at || '').slice(0, 16))}</td>
    <td class="num ${m.delta < 0 ? 'delta-out' : 'delta-in'}">${m.delta > 0 ? '+' : ''}${qty(m.delta)}</td>
    <td>${esc(REASONS[m.reason] || m.reason)}</td>
    <td class="mono">${esc(m.vehicle_reg) || '<span class="muted">—</span>'}</td>
    <td>${esc(m.by_user) || '<span class="muted">—</span>'}</td>
    <td>${esc(m.note) || '<span class="muted">—</span>'}</td>
  </tr>`;
}

// ── adjusting ─────────────────────────────────────────────────────────────

/** sign is -1 for taking off the shelf, +1 for putting on. A correction
    sets the shelf to what was actually counted, so it works out its own
    delta from the number in the box rather than adding to it. */
async function adjust(sign, reason) {
  if (!state.part) return;
  $('ad-err').textContent = '';
  const typed = Number($('ad-qty').value);
  if (!Number.isFinite(typed) || typed < 0) {
    $('ad-err').textContent = 'How many?';
    return;
  }

  let delta = sign * typed;
  if (reason === 'correction') {
    delta = typed - state.part.quantity;
    if (delta === 0) {
      $('ad-err').textContent = `The shelf already says ${qty(typed)}.`;
      return;
    }
  } else if (typed === 0) {
    $('ad-err').textContent = 'How many?';
    return;
  }

  try {
    const updated = await api(`/api/stock/parts/${state.part.id}/adjust`, {
      method: 'POST',
      json: {
        delta,
        reason,
        vehicle_reg: $('ad-reg').value,
        note: $('ad-note').value,
      },
    });
    state.part = updated;
    renderScanned(updated);
    loadPartMovements(updated.id);
    loadOverview();
    $('ad-note').value = '';
    toast(`${updated.part_number || updated.barcode} — ${qty(updated.quantity)} on the shelf`);
    focusScan();
  } catch (e) {
    // A fitment refusal is the one error worth keeping on screen rather
    // than in a toast that fades: it is the answer to what was just asked.
    $('ad-err').textContent = e.message;
  }
}

$('ad-take').addEventListener('click', () => adjust(-1, 'used'));
$('ad-add').addEventListener('click', () => adjust(1, 'received'));
$('ad-correct').addEventListener('click', () => adjust(1, 'correction'));

// ── adding a new part ─────────────────────────────────────────────────────

$('np-add').addEventListener('click', async () => {
  $('np-err').textContent = '';
  const body = {
    barcode: $('np-barcode').value,
    part_number: $('np-number').value,
    description: $('np-desc').value,
    quantity: Number($('np-qty').value) || 0,
    min_quantity: Number($('np-min').value) || 0,
    unit_cost: Number($('np-cost').value) || 0,
    location: $('np-loc').value,
    fits_make: $('np-make').value,
    fits_model: $('np-model').value,
  };
  try {
    await api('/api/stock/parts', { method: 'POST', json: body });
    ['np-number', 'np-desc', 'np-loc', 'np-make', 'np-model'].forEach((id) => { $(id).value = ''; });
    ['np-qty', 'np-min', 'np-cost'].forEach((id) => { $(id).value = '0'; });
    $('new-part').hidden = true;
    toast('Added to the shelf');
    loadOverview();
    scan(body.barcode); // straight back to the counter view of what was just added
  } catch (e) {
    $('np-err').textContent = e.message;
  }
});

// ── overview / shelf / history ────────────────────────────────────────────

async function loadOverview() {
  const o = await api('/api/stock/overview');
  $('stock-tiles').innerHTML = [
    { k: 'Lines stocked', v: o.lines },
    { k: 'Below reorder', v: o.low_lines, alert: o.low_lines > 0 },
    { k: 'Units on shelf', v: qty(o.units) },
    { k: 'Stock value', v: '£' + money(o.value) },
    { k: 'Moved today', v: o.moved_today },
  ].map((t) => `
    <div class="tile${t.alert ? ' alert' : ''}">
      <div class="k">${esc(t.k)}</div><div class="v">${esc(t.v)}</div>
      ${t.m ? `<div class="m">${esc(t.m)}</div>` : ''}
    </div>`).join('');
}

async function loadShelf() {
  const q = $('shelf-filter').value.trim();
  const parts = await api('/api/stock/parts' + (q ? `?q=${encodeURIComponent(q)}` : ''));
  $('shelf-rows').innerHTML = parts.length
    ? parts.map((p) => `
        <tr class="${p.low ? 'row-flag' : ''}">
          <td><span class="strong">${esc(p.part_number || '—')}</span>
              <span class="car-sub">${esc(p.description)}</span></td>
          <td class="mono">${esc(p.barcode)}</td>
          <td>${p.fits_make
            ? esc([p.fits_make, p.fits_model].filter(Boolean).join(' '))
            : '<span class="muted">anything</span>'}</td>
          <td>${esc(p.location) || '<span class="muted">—</span>'}</td>
          <td class="num strong">${qty(p.quantity)}${p.low ? ' <span class="pill flag">Low</span>' : ''}</td>
          <td class="num">${p.min_quantity ? qty(p.min_quantity) : '<span class="muted">—</span>'}</td>
          <td class="num">£${money(p.quantity * p.unit_cost)}</td>
          <td class="num"><button class="btn sm" data-scan="${esc(p.barcode)}">Open</button></td>
        </tr>`).join('')
    : '<tr><td colspan="8" class="empty"><strong>Nothing on the shelf yet</strong>Scan a barcode on the Counter to add the first part.</td></tr>';

  document.querySelectorAll('[data-scan]').forEach((b) =>
    b.addEventListener('click', () => {
      show('counter');
      $('scan').value = b.dataset.scan;
      scan(b.dataset.scan);
    }));
}

let shelfTimer = null;
$('shelf-filter').addEventListener('input', () => {
  clearTimeout(shelfTimer);
  shelfTimer = setTimeout(() => loadShelf().catch((e) => toast(e.message, true)), 160);
});

async function loadHistory() {
  const moves = await api('/api/stock/movements?limit=200');
  $('history-rows').innerHTML = moves.length
    ? moves.map((m) => `<tr>
        <td class="mono">${esc((m.created_at || '').slice(0, 16))}</td>
        <td><span class="strong">${esc(m.part_number || m.barcode)}</span>
            <span class="car-sub">${esc(m.description)}</span></td>
        <td class="num ${m.delta < 0 ? 'delta-out' : 'delta-in'}">${m.delta > 0 ? '+' : ''}${qty(m.delta)}</td>
        <td>${esc(REASONS[m.reason] || m.reason)}</td>
        <td class="mono">${esc(m.vehicle_reg) || '<span class="muted">—</span>'}</td>
        <td>${esc(m.by_user) || '<span class="muted">—</span>'}</td>
        <td>${esc(m.note) || '<span class="muted">—</span>'}</td>
      </tr>`).join('')
    : '<tr><td colspan="7" class="empty">No movements yet</td></tr>';
}

// ── boot ──────────────────────────────────────────────────────────────────

$('btn-logout').addEventListener('click', async () => {
  try { await api('/api/logout', { method: 'POST' }); } catch {}
  location.href = '/';
});

Object.assign(viewLoaders, {
  counter: loadOverview,
  shelf: loadShelf,
  history: loadHistory,
});

show('counter');
