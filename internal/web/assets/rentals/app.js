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
  cars: [],      // what is free to lend right now, as the board last saw it
  lending: null, // the car the lend modal is about
  returning: null,
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

/** The forecourt: one row per car that could go out right now, and one per
    car that is already with someone. This is the whole hire desk — the
    other tabs are for looking things up afterwards. */
async function loadToday() {
  const [o, board] = await Promise.all([
    api('/api/rentals/overview'),
    api('/api/rentals/board'),
  ]);
  state.cars = board.free;

  $('board-sub').textContent =
    `${board.free.length} free to lend · ${board.out.length} out with customers`;
  $('free-count').textContent = `(${board.free.length})`;
  $('out-count').textContent = `(${board.out.length})`;

  $('today-tiles').innerHTML = [
    { k: 'Out now', v: o.out_now },
    { k: 'Overdue', v: o.overdue, alert: o.overdue > 0 },
    { k: 'Due back today', v: o.due_today },
    { k: 'Free to lend', v: o.available, m: `of ${o.fleet} car(s)` },
    { k: 'Value on hire', v: '£' + money(o.out_value) },
  ].map((t) => `
    <div class="tile${t.alert ? ' alert' : ''}">
      <div class="k">${esc(t.k)}</div><div class="v">${esc(t.v)}</div>
      ${t.m ? `<div class="m">${esc(t.m)}</div>` : ''}
    </div>`).join('');

  $('free-panel').innerHTML = board.free.length
    ? board.free.map((v) => `
        <div class="car-row">
          <span class="reg">${esc(v.registration)}</span>
          <div class="car-what">
            <div class="car-name">${esc([v.make, v.model].filter(Boolean).join(' ')) || '—'}</div>
            <div class="car-sub">${carSub(v)}</div>
          </div>
          <button class="btn solid sm" data-lend="${v.id}">Lend it out</button>
        </div>`).join('')
    : '<div class="empty-box"><strong>No cars free</strong>Every loan car is out, or none has been added yet.</div>';

  $('out-panel').innerHTML = board.out.length
    ? board.out.map((a) => `
        <div class="car-row ${isOverdue(a) ? 'row-flag' : ''}">
          <span class="reg">${esc(a.registration)}</span>
          <div class="car-what">
            <div class="car-name">${esc(a.customer_name)}</div>
            <div class="car-sub">
              ${esc([a.make, a.model].filter(Boolean).join(' '))} ·
              back ${esc(ukDate(a.ends_on))}
              ${isOverdue(a) ? '<span class="pill flag">Overdue</span>' : ''}
              ${a.courtesy_for_reg ? `<span class="pill">Courtesy for ${esc(a.courtesy_for_reg)}</span>` : ''}
            </div>
          </div>
          <button class="btn sm" data-back="${a.id}" data-label="${esc(a.registration)} · ${esc(a.customer_name)}">It's back</button>
        </div>`).join('')
    : '<div class="empty-box">Every loan car is back on the forecourt.</div>';

  document.querySelectorAll('[data-lend]').forEach((b) =>
    b.addEventListener('click', () => openLend(b.dataset.lend)));
  document.querySelectorAll('[data-back]').forEach((b) =>
    b.addEventListener('click', () => openBack(b.dataset.back, b.dataset.label)));
}

/** Colour and what is on the clock — the two things that tell one otherwise
    identical Corolla from another on a forecourt list. */
function carSub(v) {
  const bits = [];
  if (v.colour) bits.push(esc(v.colour));
  bits.push(`${Math.round(v.mileage || 0).toLocaleString('en-GB')} mi`);
  if (v.daily_rate) bits.push(`£${money(v.daily_rate)}/day`);
  return bits.join(' · ');
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

// ── modals ────────────────────────────────────────────────────────────────

function openModal(id) {
  $('scrim').classList.add('open');
  $(id).classList.add('open');
  $(id).setAttribute('aria-hidden', 'false');
}

function closeModals() {
  $('scrim').classList.remove('open');
  ['lend-modal', 'car-modal', 'back-modal'].forEach((id) => {
    $(id).classList.remove('open');
    $(id).setAttribute('aria-hidden', 'true');
  });
}

$('scrim').addEventListener('click', closeModals);
['lend-close', 'lend-cancel', 'car-close', 'car-cancel', 'back-close', 'back-cancel']
  .forEach((id) => $(id).addEventListener('click', closeModals));

/** The "+ more" expanders. Both modals open with only the fields that are
    actually required on screen; everything else is one click away, which is
    the difference between a form somebody fills in and one they dread. */
function wireMore(toggleID, panelID, moreText, lessText) {
  $(toggleID).addEventListener('click', () => {
    const open = $(panelID).hidden;
    $(panelID).hidden = !open;
    $(toggleID).textContent = open ? lessText : moreText;
    $(toggleID).setAttribute('aria-expanded', String(open));
  });
}
wireMore('lend-more', 'lend-extra',
  '+ While their car is in for repair, mileage, notes', '− Fewer options');
wireMore('car-more', 'car-extra', '+ More details', '− Fewer details');

// The courtesy registration only means anything once "yes" is chosen.
$('l-courtesy-yn').addEventListener('change', () => {
  $('l-courtesy-wrap').hidden = $('l-courtesy-yn').value !== 'yes';
});

// ── lending a car out ─────────────────────────────────────────────────────

async function openLend(vehicleID) {
  const car = state.cars.find((c) => String(c.id) === String(vehicleID));
  if (!car) return;
  state.lending = car;

  $('lend-err').textContent = '';
  $('lend-car').innerHTML =
    `<span class="reg">${esc(car.registration)}</span>
     <span>${esc([car.make, car.model].filter(Boolean).join(' '))}</span>`;
  // A week is the ordinary loan, and the date is the one field somebody
  // will change every time — so it starts somewhere sensible rather than empty.
  $('l-back').value = addDays(todayISO(), 7);
  $('l-mileage').value = car.mileage ? Math.round(car.mileage) : '';
  $('l-note').value = '';
  $('l-courtesy').value = '';
  $('l-courtesy-yn').value = 'no';
  $('l-courtesy-wrap').hidden = true;

  openModal('lend-modal');
  await loadHireCustomers('l-customer');
}

function addDays(iso, days) {
  const d = new Date(iso + 'T00:00:00');
  d.setDate(d.getDate() + days);
  const p = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

async function loadHireCustomers(selectID) {
  const list = await api('/api/rentals/customers');
  $(selectID).innerHTML = '<option value="">— choose a customer —</option>' +
    list.map((c) => `<option value="${c.id}">${esc(c.name)}${c.phone ? ' · ' + esc(c.phone) : ''}</option>`).join('');
}

$('lend-save').addEventListener('click', async () => {
  $('lend-err').textContent = '';
  if (!state.lending) return;
  const customerID = Number($('l-customer').value);
  if (!customerID) { $('lend-err').textContent = 'Who is taking it?'; return; }
  if (!$('l-back').value) { $('lend-err').textContent = 'When is it back?'; return; }

  const btn = $('lend-save');
  btn.disabled = true;
  btn.textContent = 'Lending…';
  try {
    await api('/api/rentals/lend', {
      method: 'POST',
      json: {
        vehicle_id: state.lending.id,
        customer_id: customerID,
        back_on: $('l-back').value,
        mileage_now: Number($('l-mileage').value) || 0,
        courtesy_for_reg: $('l-courtesy-yn').value === 'yes' ? $('l-courtesy').value : '',
        note: $('l-note').value,
      },
    });
    toast(`${state.lending.registration} is out`);
    closeModals();
    show(state.view);
  } catch (e) {
    $('lend-err').textContent = e.message;
  }
  btn.disabled = false;
  btn.textContent = 'Lend it out';
});

// ── bringing one back ─────────────────────────────────────────────────────

function openBack(agreementID, label) {
  state.returning = agreementID;
  $('back-err').textContent = '';
  $('back-car').textContent = label || '';
  $('b-mileage').value = '';
  openModal('back-modal');
}

$('back-save').addEventListener('click', async () => {
  $('back-err').textContent = '';
  try {
    await api(`/api/rentals/agreements/${state.returning}/back`, {
      method: 'POST',
      json: { mileage_in: Number($('b-mileage').value) || 0 },
    });
    toast('Back on the forecourt');
    closeModals();
    show(state.view);
  } catch (e) {
    $('back-err').textContent = e.message;
  }
});

// ── adding one of our cars ────────────────────────────────────────────────

$('btn-add-car').addEventListener('click', () => {
  $('car-err').textContent = '';
  ['c-reg', 'c-make', 'c-model', 'c-year', 'c-colour', 'c-mileage', 'c-mot', 'c-rate', 'c-notes']
    .forEach((id) => { $(id).value = ''; });
  $('c-status').value = 'available';
  openModal('car-modal');
});

$('car-save').addEventListener('click', async () => {
  $('car-err').textContent = '';
  const btn = $('car-save');
  btn.disabled = true;
  try {
    await api('/api/rentals/vehicles', {
      method: 'POST',
      json: {
        registration: $('c-reg').value,
        make: $('c-make').value,
        model: $('c-model').value,
        status: $('c-status').value,
        year: $('c-year').value,
        colour: $('c-colour').value,
        mileage: Number($('c-mileage').value) || 0,
        mot_expires: $('c-mot').value,
        daily_rate: Number($('c-rate').value) || 0,
        notes: $('c-notes').value,
      },
    });
    toast('Added to the loan fleet');
    closeModals();
    show(state.view);
  } catch (e) {
    $('car-err').textContent = e.message;
  }
  btn.disabled = false;
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
