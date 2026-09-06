/* Goldstar Rentals — the hire desk at rentals.<domain>.
 *
 * Self-contained: the dashboard's own app.js is a different application
 * with a different set of views, so this page loads only this file and the
 * shared stylesheet. The helpers below are deliberately the same shapes
 * (api/toast/esc/money) so anyone moving between the two files is reading
 * the same idioms. */

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

/** ISO date → "13 Apr 2026". Dates arrive as plain YYYY-MM-DD, so they are
    split by hand rather than passed through Date, which would drag the
    browser's timezone into a value that has no time in it at all. */
function ukDate(iso) {
  if (!iso || iso.length < 10) return '';
  const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun',
    'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
  const [y, m, d] = iso.slice(0, 10).split('-');
  return `${Number(d)} ${MONTHS[Number(m) - 1] || ''} ${y}`;
}

function todayISO() {
  const now = new Date();
  const p = (n) => String(n).padStart(2, '0');
  return `${now.getFullYear()}-${p(now.getMonth() + 1)}-${p(now.getDate())}`;
}

function readCookie(name) {
  return document.cookie.split('; ')
    .find((c) => c.startsWith(name + '='))?.split('=')[1] || '';
}

/** Same contract as the dashboard's own api(): CSRF header on every
    mutating call, and a read-only account refused before the request goes
    out — with /api/logout the one exception, since signing out is not a
    change to anything and a read-only account that couldn't reach it would
    be stuck signed in. */
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

// ── state ─────────────────────────────────────────────────────────────────

const state = {
  view: 'today',
  hireStatus: '',
  customers: [],
  cars: [],
};

// ── navigation ────────────────────────────────────────────────────────────

const viewLoaders = {};

function show(view) {
  state.view = view;
  document.querySelectorAll('#tabs button').forEach((b) =>
    b.setAttribute('aria-selected', String(b.dataset.view === view)));
  document.querySelectorAll('.view').forEach((s) =>
    s.classList.toggle('active', s.id === 'view-' + view));
  viewLoaders[view]?.().catch((e) => toast(e.message, true));
}

document.querySelectorAll('#tabs button').forEach((b) =>
  b.addEventListener('click', () => show(b.dataset.view)));

// ── today ─────────────────────────────────────────────────────────────────

/** A hire is late when its agreed end has passed and the keys are still
    out. Comparing ISO strings is exact here — both sides are YYYY-MM-DD,
    which sorts lexicographically the same way it sorts chronologically. */
const isOverdue = (a) => a.status === 'out' && a.ends_on < todayISO();

function statusPill(a) {
  if (isOverdue(a)) return '<span class="pill flag">Overdue</span>';
  const labels = {
    booked: '<span class="pill">Booked</span>',
    out: '<span class="pill solid">Out</span>',
    returned: '<span class="pill muted">Returned</span>',
    cancelled: '<span class="pill muted">Cancelled</span>',
  };
  return labels[a.status] || esc(a.status);
}

const carLabel = (a) =>
  `<span class="mono strong">${esc(a.registration)}</span>` +
  (a.make || a.model ? `<span class="car-sub">${esc([a.make, a.model].filter(Boolean).join(' '))}</span>` : '');

async function loadToday() {
  const [o, out, upcoming] = await Promise.all([
    api('/api/rentals/overview'),
    api('/api/rentals/agreements?status=out'),
    api('/api/rentals/agreements?status=booked'),
  ]);

  const tiles = [
    { k: 'Out now', v: o.out_now },
    { k: 'Overdue', v: o.overdue, alert: o.overdue > 0 },
    { k: 'Due back today', v: o.due_today },
    { k: 'Free to hire', v: o.available, m: `of ${o.fleet} car(s)` },
    { k: 'Value on hire', v: '£' + money(o.out_value) },
  ];
  $('today-tiles').innerHTML = tiles.map((t) => `
    <div class="tile${t.alert ? ' alert' : ''}">
      <div class="k">${esc(t.k)}</div>
      <div class="v">${typeof t.v === 'number' ? t.v : t.v}</div>
      ${t.m ? `<div class="m">${esc(t.m)}</div>` : ''}
    </div>`).join('');

  $('out-rows').innerHTML = out.length
    ? out.map((a) => `
        <tr class="${isOverdue(a) ? 'row-flag' : ''}">
          <td><span class="strong">${esc(a.customer_name)}</span>
              ${a.phone ? `<span class="car-sub mono">${esc(a.phone)}</span>` : ''}</td>
          <td>${carLabel(a)}</td>
          <td class="mono">${esc(ukDate(a.starts_on))}</td>
          <td class="mono">${esc(ukDate(a.ends_on))} ${isOverdue(a) ? statusPill(a) : ''}</td>
          <td class="num">${a.days}</td>
          <td class="num strong">£${money(a.total)}</td>
          <td class="num"><button class="btn sm" data-return="${a.id}">Returned</button></td>
        </tr>`).join('')
    : '<tr><td colspan="7" class="empty">No cars out right now</td></tr>';

  $('upcoming-rows').innerHTML = upcoming.length
    ? upcoming.map((a) => `
        <tr>
          <td><span class="strong">${esc(a.customer_name)}</span></td>
          <td>${carLabel(a)}</td>
          <td class="mono">${esc(ukDate(a.starts_on))}</td>
          <td class="mono">${esc(ukDate(a.ends_on))}</td>
          <td class="num"><button class="btn sm solid" data-pickup="${a.id}">Picked up</button></td>
        </tr>`).join('')
    : '<tr><td colspan="5" class="empty">Nothing booked ahead</td></tr>';

  wireAgreementButtons();
}

/** The two buttons that move a hire along, wired wherever they appear —
    Today lists them, Hires lists them again, and both want identical
    behaviour rather than two copies of it. */
function wireAgreementButtons() {
  document.querySelectorAll('[data-pickup]').forEach((b) =>
    b.addEventListener('click', () => setStatus(b.dataset.pickup, 'out', 'Marked as picked up')));
  document.querySelectorAll('[data-return]').forEach((b) =>
    b.addEventListener('click', () => setStatus(b.dataset.return, 'returned', 'Marked as returned')));
  document.querySelectorAll('[data-cancel]').forEach((b) =>
    b.addEventListener('click', () => setStatus(b.dataset.cancel, 'cancelled', 'Hire cancelled')));
}

async function setStatus(id, status, msg) {
  try {
    await api(`/api/rentals/agreements/${id}/status`, { method: 'PATCH', json: { status } });
    toast(msg);
    show(state.view);
  } catch (e) { toast(e.message, true); }
}

// ── hires ─────────────────────────────────────────────────────────────────

async function loadHires() {
  const q = state.hireStatus ? `?status=${encodeURIComponent(state.hireStatus)}` : '';
  const rows = await api('/api/rentals/agreements' + q);
  $('hire-rows').innerHTML = rows.length
    ? rows.map((a) => `
        <tr class="${isOverdue(a) ? 'row-flag' : ''}">
          <td><span class="strong">${esc(a.customer_name)}</span>
              ${a.phone ? `<span class="car-sub mono">${esc(a.phone)}</span>` : ''}</td>
          <td>${carLabel(a)}</td>
          <td class="mono">${esc(ukDate(a.starts_on))}</td>
          <td class="mono">${esc(ukDate(a.ends_on))}</td>
          <td>${statusPill(a)}</td>
          <td class="num">${a.days}</td>
          <td class="num strong">£${money(a.total)}</td>
          <td class="num">${hireActions(a)}</td>
        </tr>`).join('')
    : '<tr><td colspan="8" class="empty">No hires match</td></tr>';
  wireAgreementButtons();
}

function hireActions(a) {
  if (a.status === 'booked') {
    return `<button class="btn sm solid" data-pickup="${a.id}">Picked up</button>
            <button class="btn sm" data-cancel="${a.id}">Cancel</button>`;
  }
  if (a.status === 'out') {
    return `<button class="btn sm" data-return="${a.id}">Returned</button>`;
  }
  return '';
}

document.querySelectorAll('#hire-filters .chip').forEach((c) =>
  c.addEventListener('click', () => {
    state.hireStatus = c.dataset.status;
    document.querySelectorAll('#hire-filters .chip').forEach((o) =>
      o.setAttribute('aria-pressed', String(o === c)));
    loadHires().catch((e) => toast(e.message, true));
  }));

// ── cars ──────────────────────────────────────────────────────────────────

async function loadCars() {
  state.cars = await api('/api/rentals/vehicles');
  $('car-rows').innerHTML = state.cars.length
    ? state.cars.map((v) => `
        <tr class="${v.status === 'retired' ? 'row-muted' : ''}">
          <td>${carLabel(v)}</td>
          <td>${esc(v.colour) || '<span class="muted">—</span>'}</td>
          <td class="num strong">£${money(v.daily_rate)}</td>
          <td>
            <select class="inline-select" data-car-status="${v.id}">
              <option value="available"${v.status === 'available' ? ' selected' : ''}>Available</option>
              <option value="maintenance"${v.status === 'maintenance' ? ' selected' : ''}>Maintenance</option>
              <option value="retired"${v.status === 'retired' ? ' selected' : ''}>Retired</option>
            </select>
          </td>
          <td class="num"><button class="btn sm danger" data-del-car="${v.id}">Delete</button></td>
        </tr>`).join('')
    : '<tr><td colspan="5" class="empty"><strong>No hire cars yet</strong>Add one above to start booking.</td></tr>';

  document.querySelectorAll('[data-car-status]').forEach((sel) =>
    sel.addEventListener('change', async () => {
      const car = state.cars.find((c) => String(c.id) === sel.dataset.carStatus);
      try {
        await api(`/api/rentals/vehicles/${car.id}`, {
          method: 'PATCH',
          json: { ...car, status: sel.value },
        });
        toast(`${car.registration} is now ${sel.value}`);
        loadCars();
      } catch (e) { toast(e.message, true); loadCars(); }
    }));

  document.querySelectorAll('[data-del-car]').forEach((b) =>
    b.addEventListener('click', async () => {
      try {
        await api(`/api/rentals/vehicles/${b.dataset.delCar}`, { method: 'DELETE' });
        toast('Car removed from the pool');
        loadCars();
      } catch (e) { toast(e.message, true); }
    }));
}

$('nc-add').addEventListener('click', async () => {
  const body = {
    registration: $('nc-reg').value,
    make: $('nc-make').value,
    model: $('nc-model').value,
    colour: $('nc-colour').value,
    daily_rate: Number($('nc-rate').value) || 0,
    status: $('nc-status').value,
  };
  try {
    await api('/api/rentals/vehicles', { method: 'POST', json: body });
    ['nc-reg', 'nc-make', 'nc-model', 'nc-colour', 'nc-rate'].forEach((id) => { $(id).value = ''; });
    $('nc-status-msg').textContent = '';
    toast('Car added to the pool');
    loadCars();
  } catch (e) {
    $('nc-status-msg').textContent = e.message;
  }
});

// ── customers ─────────────────────────────────────────────────────────────

async function loadCustomers() {
  const q = $('cu-filter').value.trim();
  state.customers = await api('/api/rentals/customers' + (q ? `?q=${encodeURIComponent(q)}` : ''));

  // Hire counts come from the agreements list rather than a second
  // endpoint — the desk wants to know "is this a regular", and the number
  // is small enough that counting client-side beats another round trip.
  const all = await api('/api/rentals/agreements');
  const counts = {};
  all.forEach((a) => { counts[a.customer_id] = (counts[a.customer_id] || 0) + 1; });

  $('customer-rows').innerHTML = state.customers.length
    ? state.customers.map((c) => `
        <tr>
          <td class="strong">${esc(c.name)}</td>
          <td class="mono">${esc(c.phone) || '<span class="muted">—</span>'}</td>
          <td>${esc(c.email) || '<span class="muted">—</span>'}</td>
          <td class="mono">${esc(c.licence_no) || '<span class="muted">—</span>'}</td>
          <td class="num">${counts[c.id] || 0}</td>
          <td class="num"><button class="btn sm danger" data-del-cu="${c.id}">Delete</button></td>
        </tr>`).join('')
    : '<tr><td colspan="6" class="empty">No customers yet</td></tr>';

  document.querySelectorAll('[data-del-cu]').forEach((b) =>
    b.addEventListener('click', async () => {
      try {
        await api(`/api/rentals/customers/${b.dataset.delCu}`, { method: 'DELETE' });
        toast('Customer removed');
        loadCustomers();
      } catch (e) { toast(e.message, true); }
    }));
}

let filterTimer = null;
$('cu-filter').addEventListener('input', () => {
  clearTimeout(filterTimer);
  filterTimer = setTimeout(() => loadCustomers().catch((e) => toast(e.message, true)), 160);
});

$('ncu-add').addEventListener('click', async () => {
  const body = {
    name: $('ncu-name').value,
    phone: $('ncu-phone').value,
    email: $('ncu-email').value,
    licence_no: $('ncu-licence').value,
    address: $('ncu-address').value,
  };
  try {
    await api('/api/rentals/customers', { method: 'POST', json: body });
    ['ncu-name', 'ncu-phone', 'ncu-email', 'ncu-licence', 'ncu-address']
      .forEach((id) => { $(id).value = ''; });
    $('ncu-status-msg').textContent = '';
    toast('Customer added');
    loadCustomers();
  } catch (e) {
    $('ncu-status-msg').textContent = e.message;
  }
});

// ── new hire ──────────────────────────────────────────────────────────────

function openHire() {
  $('h-from').value = $('h-from').value || todayISO();
  $('h-to').value = $('h-to').value || todayISO();
  $('h-err').textContent = '';
  $('scrim').classList.add('open');
  $('hire-drawer').classList.add('open');
  $('hire-drawer').setAttribute('aria-hidden', 'false');
  loadHireCustomers();
  refreshAvailable();
}

function closeHire() {
  $('scrim').classList.remove('open');
  $('hire-drawer').classList.remove('open');
  $('hire-drawer').setAttribute('aria-hidden', 'true');
}

$('btn-new-hire').addEventListener('click', openHire);
$('hire-close').addEventListener('click', closeHire);
$('scrim').addEventListener('click', closeHire);

async function loadHireCustomers() {
  const list = await api('/api/rentals/customers');
  $('h-customer').innerHTML = '<option value="">— choose a customer —</option>' +
    list.map((c) => `<option value="${c.id}">${esc(c.name)}${c.phone ? ' · ' + esc(c.phone) : ''}</option>`).join('');
}

/** The available list is fetched from the same overlap rule the booking
    call enforces, so anything offered here is something the server will
    actually accept — the two can never drift into offering a car that is
    then refused. */
async function refreshAvailable() {
  const from = $('h-from').value;
  const to = $('h-to').value;
  const sel = $('h-car');
  if (!from || !to) {
    sel.innerHTML = '<option value="">— pick dates first —</option>';
    return;
  }
  if (to < from) {
    sel.innerHTML = '<option value="">— the end date is before the start —</option>';
    $('h-dates-hint').textContent = 'The end date cannot be before the start date.';
    return;
  }
  try {
    const cars = await api(`/api/rentals/available?from=${from}&to=${to}`);
    const days = Math.round((Date.parse(to) - Date.parse(from)) / 86400000) + 1;
    $('h-dates-hint').textContent =
      `${days} day${days === 1 ? '' : 's'} · ${cars.length} car${cars.length === 1 ? '' : 's'} free`;
    sel.innerHTML = cars.length
      ? '<option value="">— choose a car —</option>' + cars.map((c) =>
          `<option value="${c.id}" data-rate="${c.daily_rate}">${esc(c.registration)} · ${esc([c.make, c.model].filter(Boolean).join(' '))} · £${money(c.daily_rate)}/day</option>`).join('')
      : '<option value="">— nothing free over those dates —</option>';
    updateHireTotal();
  } catch (e) {
    $('h-dates-hint').textContent = e.message;
  }
}

function updateHireTotal() {
  const opt = $('h-car').selectedOptions[0];
  const rate = Number(opt?.dataset.rate || 0);
  const from = $('h-from').value;
  const to = $('h-to').value;
  if (!rate || !from || !to || to < from) { $('h-total').textContent = ''; return; }
  const days = Math.round((Date.parse(to) - Date.parse(from)) / 86400000) + 1;
  $('h-total').innerHTML =
    `${days} day${days === 1 ? '' : 's'} at £${money(rate)} — <strong>£${money(days * rate)}</strong>`;
}

$('h-from').addEventListener('change', refreshAvailable);
$('h-to').addEventListener('change', refreshAvailable);
$('h-car').addEventListener('change', updateHireTotal);

$('h-save').addEventListener('click', async () => {
  $('h-err').textContent = '';
  const body = {
    vehicle_id: Number($('h-car').value),
    customer_id: Number($('h-customer').value),
    starts_on: $('h-from').value,
    ends_on: $('h-to').value,
    notes: $('h-notes').value,
  };
  if (!body.vehicle_id) { $('h-err').textContent = 'Pick a car.'; return; }
  if (!body.customer_id) { $('h-err').textContent = 'Pick a customer.'; return; }

  const btn = $('h-save');
  btn.disabled = true;
  btn.textContent = 'Booking…';
  try {
    await api('/api/rentals/agreements', { method: 'POST', json: body });
    toast('Hire booked');
    $('h-notes').value = '';
    closeHire();
    show(state.view);
  } catch (e) {
    $('h-err').textContent = e.message;
  }
  btn.disabled = false;
  btn.textContent = 'Book it';
});

// ── boot ──────────────────────────────────────────────────────────────────

$('btn-logout').addEventListener('click', async () => {
  try { await api('/api/logout', { method: 'POST' }); } catch {}
  location.href = '/';
});

Object.assign(viewLoaders, {
  today: loadToday,
  hires: loadHires,
  cars: loadCars,
  customers: loadCustomers,
});

show('today');
