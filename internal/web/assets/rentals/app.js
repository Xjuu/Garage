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
  rates: {},     // the house price list, loaded once
  settings: null,
  cal: { period: 'week', from: '', to: '' },
  hire: null,    // the agreement open in the drawer
  editingCar: null,
  editingCustomer: null,
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
  document.querySelectorAll('.side-link[data-view]').forEach((b) =>
    b.setAttribute('aria-current', String(b.dataset.view === view)));
  document.querySelectorAll('.view').forEach((s) =>
    s.classList.toggle('active', s.id === 'view-' + view));
  // The hire page is reached from a row rather than the sidebar, so
  // nothing in the sidebar is current while it is open.
  viewLoaders[view]?.().catch((e) => toast(e.message, true));
}

document.querySelectorAll('.side-link[data-view]').forEach((b) =>
  b.addEventListener('click', () => show(b.dataset.view)));

// Changing keys is an admin act — the server enforces that, and hiding the
// link from everyone else stops it being a door that opens onto a 403.
if (document.body.dataset.role && document.body.dataset.role !== 'admin') {
  $('tab-settings').hidden = true;
}

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

  renderJobs(await api('/api/rentals/today'));

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
          <button class="btn sm" data-open-hire="${a.id}">Open</button>
          <button class="btn sm" data-back="${a.id}" data-label="${esc(a.registration)} · ${esc(a.customer_name)}">It's back</button>
        </div>`).join('')
    : '<div class="empty-box">Every loan car is back on the forecourt.</div>';

  document.querySelectorAll('[data-lend]').forEach((b) =>
    b.addEventListener('click', () => openLend(b.dataset.lend)));
  document.querySelectorAll('[data-back]').forEach((b) =>
    b.addEventListener('click', () => openBack(b.dataset.back, b.dataset.label)));
  document.querySelectorAll('[data-open-hire]').forEach((b) =>
    b.addEventListener('click', () => openHire(b.dataset.openHire).catch((e) => toast(e.message, true))));

  renderSetupNote();
}

/** The three jobs a hire desk actually has each morning. Rendered as work
    to do rather than a status list: each row is one action away from being
    finished, and a group with nothing in it is left out entirely rather
    than shown as a proud zero. */
function renderJobs(t) {
  const groups = [
    { key: 'overdue', title: 'Overdue', tone: 'bad', rows: t.overdue,
      action: (a) => `<button class="btn sm" data-back="${a.id}" data-label="${esc(a.registration)} · ${esc(a.customer_name)}">It's back</button>` },
    { key: 'due', title: 'Due back today', tone: 'warn', rows: t.due_today,
      action: (a) => `<button class="btn sm" data-back="${a.id}" data-label="${esc(a.registration)} · ${esc(a.customer_name)}">It's back</button>` },
    { key: 'out', title: 'Going out', tone: 'good', rows: t.going_out,
      action: (a) => `<button class="btn sm solid" data-pickup="${a.id}">Picked up</button>` },
  ].filter((g) => g.rows && g.rows.length);

  if (!groups.length) {
    $('today-jobs').innerHTML =
      '<div class="jobs-clear">Nothing needs chasing today.</div>';
    return;
  }

  $('today-jobs').innerHTML = groups.map((g) => `
    <div class="job-group is-${g.tone}">
      <div class="job-head">${esc(g.title)}<span class="job-n">${g.rows.length}</span></div>
      ${g.rows.map((a) => `
        <div class="job-row">
          <span class="reg">${esc(a.registration)}</span>
          <div class="job-who">
            <div class="car-name">${esc(a.customer_name)}</div>
            <div class="car-sub">
              ${g.key === 'overdue' ? `${a.days_late} day${a.days_late === 1 ? '' : 's'} late · due ${esc(ukDate(a.ends_on))}`
                : g.key === 'due' ? `out since ${esc(ukDate(a.starts_on))}`
                : `booked ${esc(ukDate(a.starts_on))} → ${esc(ukDate(a.ends_on))}`}
              ${a.phone ? `· <span class="mono">${esc(a.phone)}</span>` : ''}
            </div>
          </div>
          ${g.action(a)}
          <button class="btn sm" data-open-hire="${a.id}">Open</button>
        </div>`).join('')}
    </div>`).join('');
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
          <td class="num">${hireActions(a)}
            <button class="btn sm" data-open-hire="${a.id}">Open</button></td>
        </tr>`).join('')
    : '<tr><td colspan="8" class="empty">No hires match</td></tr>';
  wireAgreementButtons();
}

/** The actions on a hire row, wired wherever they are rendered. Deleted
    along with the old Today view when the board replaced it, but the Hires
    tab still renders these buttons — which is exactly the shape of bug a
    "loads without throwing" check cannot catch, because the throw only
    happens once someone opens that tab. */
function wireAgreementButtons() {
  document.querySelectorAll('[data-pickup]').forEach((b) =>
    b.addEventListener('click', () => setStatus(b.dataset.pickup, 'out', 'Marked as picked up')));
  document.querySelectorAll('[data-cancel]').forEach((b) =>
    b.addEventListener('click', () => setStatus(b.dataset.cancel, 'cancelled', 'Hire cancelled')));
  // Returning goes through the same form the board uses, so a mileage
  // reading is taken here too rather than only on the forecourt.
  document.querySelectorAll('[data-return]').forEach((b) =>
    b.addEventListener('click', () => openBack(b.dataset.return, b.dataset.label || '')));
  document.querySelectorAll('[data-text]').forEach((b) =>
    b.addEventListener('click', () => textCarReady(b.dataset.text)));
  document.querySelectorAll('[data-pay]').forEach((b) =>
    b.addEventListener('click', () => takePayment(b.dataset.pay)));
  document.querySelectorAll('[data-open-hire]').forEach((b) =>
    b.addEventListener('click', () => openHire(b.dataset.openHire).catch((e) => toast(e.message, true))));
}

/** Tell a customer their own car is repaired. The message is composed
    server-side from the hire, so it can name their car and remind them to
    bring the loan one back — the desk does not retype it each time. */
async function textCarReady(id) {
  try {
    const res = await api(`/api/rentals/agreements/${id}/text-ready`, { method: 'POST' });
    toast(`Texted ${res.sent_to}`);
    show(state.view);
  } catch (e) { toast(e.message, true); }
}

/** Open (or re-open) the Stripe page for a hire. The link is copied to the
    clipboard as well as opened, because the usual next step is sending it
    to the customer rather than paying it at the counter. */
async function takePayment(id) {
  try {
    const res = await api(`/api/rentals/agreements/${id}/pay`, { method: 'POST' });
    if (navigator.clipboard) navigator.clipboard.writeText(res.url).catch(() => {});
    toast(res.reused ? 'Payment link copied (already open)' : 'Payment link copied');
    window.open(res.url, '_blank', 'noopener');
  } catch (e) { toast(e.message, true); }
}

async function setStatus(id, status, msg) {
  try {
    await api(`/api/rentals/agreements/${id}/status`, { method: 'PATCH', json: { status } });
    toast(msg);
    show(state.view);
  } catch (e) { toast(e.message, true); }
}

/** Why an action cannot be taken, or '' when it can. Returned as a
    disabled-with-a-reason rather than left to fail on click: "Stripe is not
    set up" is worth knowing before pressing, not after. */
function blockedBecause(what) {
  const st = state.settings;
  if (!st) return '';
  if (what === 'pay' && !st.stripe_ready) return 'Card payments are not set up — see Settings';
  if (what === 'text' && !st.twilio_ready) return 'Texting is not set up — see Settings';
  return '';
}

function actionBtn(label, attr, id, extraClass = '') {
  const why = blockedBecause(attr === 'data-pay' ? 'pay' : 'text');
  const cls = `btn sm ${extraClass}`.trim();
  if (why) return `<button class="${cls}" disabled title="${esc(why)}">${esc(label)}</button>`;
  return `<button class="${cls}" ${attr}="${id}">${esc(label)}</button>`;
}

function hireActions(a) {
  const money = a.paid
    ? '<span class="pill">Paid</span>'
    : actionBtn('Take payment', 'data-pay', a.id);
  if (a.status === 'out') {
    const text = a.ready_texted_at
      ? '<button class="btn sm" disabled title="Already sent">Text: car ready</button>'
      : actionBtn('Text: car ready', 'data-text', a.id);
    return `${money} ${text}
      <button class="btn sm" data-return="${a.id}" data-label="${esc(a.registration)} · ${esc(a.customer_name)}">Returned</button>`;
  }
  if (a.status === 'returned') return money;
  if (a.status === 'booked') {
    return `<button class="btn sm solid" data-pickup="${a.id}">Picked up</button>
            <button class="btn sm" data-cancel="${a.id}">Cancel</button>`;
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

// ── courtesy cars ─────────────────────────────────────────────────────────

async function loadCourtesy() {
  const rows = await api('/api/rentals/courtesy');
  $('courtesy-panel').innerHTML = rows.length
    ? rows.map((a) => `
        <div class="car-row ${isOverdue(a) ? 'row-flag' : ''}">
          <div class="car-what">
            <div class="car-name">${esc(a.customer_name)}</div>
            <div class="car-sub">
              <span class="pill flag-soft">In the workshop</span>
              <span class="reg">${esc(a.courtesy_for_reg)}</span>
              <span class="swap">→ driving</span>
              <span class="reg">${esc(a.registration)}</span>
              <span>${esc([a.make, a.model].filter(Boolean).join(' '))}</span>
              · back ${esc(ukDate(a.ends_on))}
              ${isOverdue(a) ? '<span class="pill flag">Overdue</span>' : ''}
            </div>
          </div>
          ${a.ready_texted_at
            ? '<button class="btn sm" disabled title="Already sent">Text: car ready</button>'
            : actionBtn('Text: car ready', 'data-text', a.id)}
          <button class="btn sm" data-open-hire="${a.id}">Open</button>
          <button class="btn sm" data-back="${a.id}" data-label="${esc(a.registration)} · ${esc(a.customer_name)}">It's back</button>
        </div>`).join('')
    : '<div class="empty-box"><strong>No courtesy cars out</strong>A loan becomes one when you say whose car is in for repair.</div>';

  document.querySelectorAll('[data-back]').forEach((b) =>
    b.addEventListener('click', () => openBack(b.dataset.back, b.dataset.label)));
  document.querySelectorAll('[data-text]').forEach((b) =>
    b.addEventListener('click', () => textCarReady(b.dataset.text)));
  document.querySelectorAll('[data-open-hire]').forEach((b) =>
    b.addEventListener('click', () => openHire(b.dataset.openHire).catch((e) => toast(e.message, true))));
}

// ── money ─────────────────────────────────────────────────────────────────

async function loadMoney() {
  const st = await api('/api/rentals/stats');
  $('money-tiles').innerHTML = [
    { k: 'On hire right now', v: '£' + money(st.on_hire_now), m: 'value of cars out' },
    { k: 'Billed this month', v: '£' + money(st.billed_this_month), m: `${st.hires_this_month} hire(s)` },
    { k: 'Collected all time', v: '£' + money(st.collected_all_time) },
    { k: 'Still owed', v: '£' + money(st.outstanding_now), alert: st.outstanding_now > 0 },
    { k: 'Fleet in use', v: Math.round(st.utilisation_pct) + '%' },
    { k: 'Typical hire', v: '£' + money(st.avg_hire_value), m: `${st.avg_hire_days.toFixed(1)} days` },
  ].map((t) => `
    <div class="tile${t.alert ? ' alert' : ''}">
      <div class="k">${esc(t.k)}</div><div class="v">${esc(t.v)}</div>
      ${t.m ? `<div class="m">${esc(t.m)}</div>` : ''}
    </div>`).join('');

  $('car-earnings').innerHTML = st.top_cars.length
    ? st.top_cars.map((c) => `
        <tr>
          <td><span class="reg">${esc(c.registration)}</span>
              <span class="car-sub">${esc([c.make, c.model].filter(Boolean).join(' '))}</span></td>
          <td class="num">${c.hires}</td>
          <td class="num">${c.days}</td>
          <td class="num strong">£${money(c.billed)}</td>
        </tr>`).join('')
    : '<tr><td colspan="4" class="empty">Nothing hired out yet</td></tr>';
}

// ── cars ──────────────────────────────────────────────────────────────────

async function loadCars() {
  state.cars = await api('/api/rentals/vehicles');
  state.allCars = state.cars;
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
          <td class="num">
            <button class="btn sm" data-edit-car="${v.id}">Edit</button>
            <button class="btn sm danger" data-del-car="${v.id}">Delete</button></td>
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

  document.querySelectorAll('[data-edit-car]').forEach((b) =>
    b.addEventListener('click', () => openCarModal(
      state.cars.find((c) => String(c.id) === b.dataset.editCar))));

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
          <td class="num">
            <button class="btn sm" data-msg-cu="${c.id}">Message</button>
            <button class="btn sm" data-edit-cu="${c.id}">Edit</button>
            <button class="btn sm danger" data-del-cu="${c.id}">Delete</button></td>
        </tr>`).join('')
    : '<tr><td colspan="6" class="empty">No customers yet</td></tr>';

  document.querySelectorAll('[data-edit-cu]').forEach((b) =>
    b.addEventListener('click', () => openCustomerModal(
      state.customers.find((c) => String(c.id) === b.dataset.editCu))));

  // Texting a customer with no hire open — chasing a returned car, say.
  document.querySelectorAll('[data-msg-cu]').forEach((b) =>
    b.addEventListener('click', async () => {
      const c = state.customers.find((x) => String(x.id) === b.dataset.msgCu);
      const body = prompt(`Text ${c.name}${c.phone ? ' (' + c.phone + ')' : ''}:`);
      if (!body || !body.trim()) return;
      try {
        const res = await api('/api/rentals/messages', {
          method: 'POST', json: { customer_id: c.id, body },
        });
        toast(`Texted ${res.sent_to}`);
      } catch (e) { toast(e.message, true); }
    }));

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

$('btn-add-customer')?.addEventListener('click', () => openCustomerModal(null));

$('ncu-add')?.addEventListener('click', async () => {
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
  ['lend-modal', 'car-modal', 'back-modal', 'cust-modal'].forEach((id) => {
    $(id).classList.remove('open');
    $(id).setAttribute('aria-hidden', 'true');
  });
}

$('scrim').addEventListener('click', closeModals);
['lend-close', 'lend-cancel', 'car-close', 'car-cancel', 'back-close', 'back-cancel',
  'cust-close', 'cust-cancel']
  .forEach((id) => $(id)?.addEventListener('click', closeModals));

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
  const car = (state.cars || []).find((c) => String(c.id) === String(vehicleID)) ||
    (state.allCars || []).find((c) => String(c.id) === String(vehicleID));
  if (!car) return;
  return openLendOn(car, todayISO());
}

/** The same form, opened for a particular car on a particular day — what
    clicking an empty square on the calendar means. */
async function openLendOn(car, startDay) {
  state.lending = car;
  state.lendFrom = startDay || todayISO();

  $('lend-err').textContent = '';
  $('lend-car').innerHTML =
    `<span class="reg">${esc(car.registration)}</span>
     <span>${esc([car.make, car.model].filter(Boolean).join(' '))}</span>`;
  // A week is the ordinary loan, and the date is the one field somebody
  // will change every time — so it starts somewhere sensible rather than
  // empty. From the calendar it counts from the day that was clicked.
  $('l-back').value = addDays(state.lendFrom || todayISO(), 7);
  $('l-mileage').value = car.mileage ? Math.round(car.mileage) : '';
  $('l-note').value = '';
  $('l-courtesy').value = '';
  $('l-courtesy-yn').value = 'no';
  $('l-courtesy-wrap').hidden = true;
  // The house price list, so the counter agrees to the normal rates
  // without retyping them — and can still override for this one hire.
  $('l-insurance').value = state.rates.insurance || '';
  $('l-latefee').value = state.rates.lateFee || '';
  $('l-deposit').value = state.rates.deposit || '';

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
        insurance_per_day: Number($('l-insurance').value) || 0,
        late_fee_per_day: Number($('l-latefee').value) || 0,
        deposit: Number($('l-deposit').value) || 0,
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
    if (state.view === 'hire' && state.hire) openHire(state.hire.id).catch(() => show('today'));
    else show(state.view);
  } catch (e) {
    $('back-err').textContent = e.message;
  }
});

// ── adding one of our cars ────────────────────────────────────────────────

/** One form for both adding and editing — the fields are identical, and two
    near-copies of it is how they drift apart. A car being edited also gets
    a Delete, which only makes sense once there is something to delete. */
function openCarModal(car) {
  state.editingCar = car || null;
  $('car-err').textContent = '';
  $('car-modal-title').textContent = car ? 'Edit this car' : 'Add one of our cars';
  $('car-delete').hidden = !car;
  $('c-reg').value = car ? car.registration : '';
  $('c-make').value = car ? car.make : '';
  $('c-model').value = car ? car.model : '';
  $('c-status').value = car ? car.status : 'available';
  $('c-year').value = car ? car.year : '';
  $('c-colour').value = car ? car.colour : '';
  $('c-mileage').value = car && car.mileage ? Math.round(car.mileage) : '';
  $('c-mot').value = car ? (car.mot_expires || '') : '';
  $('c-rate').value = car && car.daily_rate ? car.daily_rate : '';
  $('c-notes').value = car ? car.notes : '';
  // An existing car's details are worth seeing without a click.
  if (car) { $('car-extra').hidden = false; $('car-more').textContent = '− Fewer details'; }
  openModal('car-modal');
}

$('btn-add-car').addEventListener('click', () => openCarModal(null));

$('car-delete').addEventListener('click', async () => {
  if (!state.editingCar) return;
  $('car-err').textContent = '';
  try {
    await api(`/api/rentals/vehicles/${state.editingCar.id}`, { method: 'DELETE' });
    toast('Car removed from the pool');
    closeModals();
    show(state.view);
  } catch (e) { $('car-err').textContent = e.message; }
});

$('car-save').addEventListener('click', async () => {
  $('car-err').textContent = '';
  const btn = $('car-save');
  btn.disabled = true;
  const editing = state.editingCar;
  try {
    await api(editing ? `/api/rentals/vehicles/${editing.id}` : '/api/rentals/vehicles', {
      method: editing ? 'PATCH' : 'POST',
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
    toast(editing ? 'Car updated' : 'Added to the loan fleet');
    closeModals();
    show(state.view);
  } catch (e) {
    $('car-err').textContent = e.message;
  }
  btn.disabled = false;
});

// ── customers: add and edit through one form ──────────────────────────────

function openCustomerModal(c) {
  state.editingCustomer = c || null;
  $('cust-err').textContent = '';
  $('cust-modal-title').textContent = c ? 'Edit customer' : 'Add a customer';
  $('cu-name').value = c ? c.name : '';
  $('cu-phone').value = c ? c.phone : '';
  $('cu-email').value = c ? c.email : '';
  $('cu-licence').value = c ? c.licence_no : '';
  $('cu-address').value = c ? c.address : '';
  $('cu-notes').value = c ? c.notes : '';
  openModal('cust-modal');
}

$('cust-save').addEventListener('click', async () => {
  $('cust-err').textContent = '';
  const editing = state.editingCustomer;
  const body = {
    name: $('cu-name').value,
    phone: $('cu-phone').value,
    email: $('cu-email').value,
    licence_no: $('cu-licence').value,
    address: $('cu-address').value,
    notes: $('cu-notes').value,
  };
  try {
    await api(editing ? `/api/rentals/customers/${editing.id}` : '/api/rentals/customers', {
      method: editing ? 'PATCH' : 'POST', json: body,
    });
    toast(editing ? 'Customer updated' : 'Customer added');
    closeModals();
    show(state.view);
  } catch (e) { $('cust-err').textContent = e.message; }
});

// ── one hire, in full ─────────────────────────────────────────────────────

async function openHire(id) {
  const a = await api(`/api/rentals/agreements/${id}`);
  state.hire = a;
  state.cameFrom = state.view === 'hire' ? state.cameFrom : state.view;
  $('hd-title').textContent = `${a.registration} · ${a.customer_name}`;
  $('hd-status').innerHTML = statusPill(a);
  $('hd-money-err').textContent = '';
  $('hd-msg-err').textContent = '';
  $('hd-doc-err').textContent = '';

  $('hd-summary').innerHTML = `
    <div class="hd-line"><span class="reg">${esc(a.registration)}</span>
      <span>${esc([a.make, a.model].filter(Boolean).join(' '))}</span>
      ${statusPill(a)}</div>
    <div class="hd-line car-sub">
      ${esc(a.customer_name)}${a.phone ? ' · ' + esc(a.phone) : ''} ·
      ${esc(ukDate(a.starts_on))} → ${esc(ukDate(a.ends_on))} (${a.days} day${a.days === 1 ? '' : 's'})
      ${a.courtesy_for_reg ? `· <span class="pill">Courtesy for ${esc(a.courtesy_for_reg)}</span>` : ''}
    </div>
    ${a.notes ? `<div class="hd-line car-sub">${esc(a.notes)}</div>` : ''}`;

  renderBill(a);
  // The same "say why" treatment inside the drawer: a disabled button with
  // a reason beats a button that errors.
  const payWhy = blockedBecause('pay');
  $('hd-pay').disabled = !!payWhy || a.paid;
  $('hd-pay').title = payWhy || (a.paid ? 'Already paid' : '');
  $('hd-check-pay').disabled = !!payWhy;
  const textWhy = blockedBecause('text');
  $('hd-send').disabled = !!textWhy;
  $('hd-send').title = textWhy;
  $('hd-send-ready').disabled = !!textWhy;
  $('hd-send-ready').title = textWhy;
  $('hd-msg-err').textContent = textWhy;

  $('hd-extra').value = a.extra_charges || '';
  $('hd-extra-note').value = a.extra_note || '';
  $('hd-message').value = '';

  // A page rather than a drawer: the bill, the thread and the documents sit
  // beside each other instead of stacked in a narrow column. Nothing in the
  // sidebar is current while it is open, since it was reached from a row.
  document.querySelectorAll('.view').forEach((v) =>
    v.classList.toggle('active', v.id === 'view-hire'));
  document.querySelectorAll('.side-link[data-view]').forEach((b) =>
    b.setAttribute('aria-current', 'false'));
  state.view = 'hire';
  window.scrollTo(0, 0);

  loadHireMessages(a.customer_id);
  loadHireDocs(a);
}

/** The bill, line by line. Shown as arithmetic rather than one number
    because every line is something a customer might question, and "£360"
    on its own answers none of those questions. */
function renderBill(a) {
  const line = (label, amount, cls = '') =>
    `<tr class="${cls}"><td>${label}</td><td class="num">£${money(amount)}</td></tr>`;

  let rows = line(`Hire · ${a.days} day${a.days === 1 ? '' : 's'} at £${money(a.daily_rate)}`, a.total);
  if (a.insurance > 0) {
    rows += line(`Insurance · ${a.days} × £${money(a.insurance_per_day)}`, a.insurance);
  }
  if (a.late_fee > 0) {
    rows += line(`Late fee · ${a.days_late} day${a.days_late === 1 ? '' : 's'}`, a.late_fee, 'bill-flag');
  } else if (a.late_fee_due > 0) {
    rows += `<tr class="bill-muted"><td>${a.days_late} day${a.days_late === 1 ? '' : 's'} late
      · £${money(a.late_fee_due)} not charged</td><td class="num">—</td></tr>`;
  }
  if (a.extra_charges > 0) {
    rows += line(`Extras${a.extra_note ? ' · ' + esc(a.extra_note) : ''}`, a.extra_charges);
  }
  rows += `<tr class="bill-total"><td>${a.paid ? 'Paid' : 'To pay'}</td>
           <td class="num">£${money(a.chargeable)}</td></tr>`;
  if (a.deposit > 0) {
    rows += `<tr class="bill-muted"><td>Deposit held${a.deposit_returned ? ' · returned' : ''}</td>
             <td class="num">£${money(a.deposit)}</td></tr>`;
  }
  $('hd-bill').innerHTML = rows;
}

function closeHire() {
  state.hire = null;
  show(state.cameFrom || 'today');
}
$('hire-back').addEventListener('click', closeHire);

/** Every action in the drawer refreshes it from what came back, so what is
    on screen is the server's answer rather than a guess at it. */
async function hireAction(fn, errBox = 'hd-money-err') {
  if (!state.hire) return;
  $(errBox).textContent = '';
  try {
    const updated = await fn(state.hire);
    if (updated && updated.id) {
      state.hire = updated;
      renderBill(updated);
      $('hd-status').innerHTML = statusPill(updated);
    }
  } catch (e) {
    $(errBox).textContent = e.message;
  }
}

$('hd-save-charges').addEventListener('click', () => hireAction((a) =>
  api(`/api/rentals/agreements/${a.id}/charges`, {
    method: 'PATCH',
    json: { extra_charges: Number($('hd-extra').value) || 0, extra_note: $('hd-extra-note').value },
  })));

$('hd-late-fee').addEventListener('click', () => hireAction((a) =>
  api(`/api/rentals/agreements/${a.id}/late-fee`, { method: 'POST', json: {} })));

$('hd-waive-late').addEventListener('click', () => hireAction((a) =>
  api(`/api/rentals/agreements/${a.id}/late-fee`, { method: 'POST', json: { amount: 0 } })));

$('hd-deposit').addEventListener('click', () => hireAction((a) =>
  api(`/api/rentals/agreements/${a.id}/deposit`, {
    method: 'POST', json: { returned: !a.deposit_returned },
  })));

$('hd-pay').addEventListener('click', () => { if (state.hire) takePayment(state.hire.id); });

$('hd-check-pay').addEventListener('click', () => hireAction(async (a) => {
  const res = await api(`/api/rentals/agreements/${a.id}/payment`);
  toast(res.paid ? 'Paid' : `Not paid yet (${res.status || 'no payment started'})`);
  return api(`/api/rentals/agreements/${a.id}`);
}));

// ── messages in the drawer ────────────────────────────────────────────────

async function loadHireMessages(customerID) {
  const msgs = await api(`/api/rentals/messages?customer=${customerID}&limit=20`);
  $('hd-messages').innerHTML = msgs.length
    ? msgs.map((m) => `
        <div class="msg ${m.status === 'failed' ? 'msg-failed' : ''}">
          <div class="msg-body">${esc(m.body)}</div>
          <div class="msg-meta">
            ${esc((m.created_at || '').slice(0, 16))} · ${esc(m.sent_by || 'system')}
            ${m.status === 'failed' ? `· <span class="pill flag">failed: ${esc(m.error)}</span>` : ''}
          </div>
        </div>`).join('')
    : '<div class="car-sub">Nothing sent to this customer yet.</div>';
}

$('hd-send').addEventListener('click', async () => {
  if (!state.hire) return;
  $('hd-msg-err').textContent = '';
  const body = $('hd-message').value.trim();
  if (!body) { $('hd-msg-err').textContent = 'Type something first.'; return; }
  const btn = $('hd-send');
  btn.disabled = true;
  try {
    const res = await api('/api/rentals/messages', {
      method: 'POST',
      json: { customer_id: state.hire.customer_id, agreement_id: state.hire.id, body },
    });
    $('hd-message').value = '';
    toast(`Texted ${res.sent_to}`);
    loadHireMessages(state.hire.customer_id);
  } catch (e) {
    $('hd-msg-err').textContent = e.message;
    loadHireMessages(state.hire.customer_id); // a failure is logged too
  }
  btn.disabled = false;
});

// Prefills rather than sends: the wording is the server's, but somebody
// should still read it before it goes.
$('hd-send-ready').addEventListener('click', async () => {
  if (!state.hire) return;
  try {
    await api(`/api/rentals/agreements/${state.hire.id}/text-ready`, { method: 'POST' });
    toast('Sent');
    loadHireMessages(state.hire.customer_id);
  } catch (e) { $('hd-msg-err').textContent = e.message; }
});

// ── documents in the drawer ───────────────────────────────────────────────

async function loadHireDocs(a) {
  const docs = await api(`/api/rentals/documents?customer=${a.customer_id}&agreement=${a.id}`);
  $('hd-docs').innerHTML = docs.length
    ? docs.map((d) => `
        <div class="doc">
          <a href="/api/rentals/documents/${d.id}/file" target="_blank" rel="noopener">${esc(d.filename)}</a>
          <span class="pill">${esc(d.kind)}</span>
          <span class="car-sub">${Math.round(d.bytes / 1024).toLocaleString('en-GB')} KB ·
            ${esc((d.created_at || '').slice(0, 10))}${d.uploaded_by ? ' · ' + esc(d.uploaded_by) : ''}</span>
          <button class="btn sm danger" data-del-doc="${d.id}">Delete</button>
        </div>`).join('')
    : '<div class="car-sub">No documents yet — licence, signed agreement, damage photos.</div>';

  document.querySelectorAll('[data-del-doc]').forEach((b) =>
    b.addEventListener('click', async () => {
      try {
        await api(`/api/rentals/documents/${b.dataset.delDoc}`, { method: 'DELETE' });
        toast('Document deleted');
        loadHireDocs(a);
      } catch (e) { toast(e.message, true); }
    }));
}

$('hd-doc-upload').addEventListener('click', async () => {
  if (!state.hire) return;
  $('hd-doc-err').textContent = '';
  const input = $('hd-doc-file');
  const file = input.files && input.files[0];
  if (!file) { $('hd-doc-err').textContent = 'Choose a file first.'; return; }
  // The read-only guard lives in api(), which this bypasses for the
  // multipart body — so it is repeated here rather than left to the 403.
  if (document.body.dataset.readonly === 'true') {
    $('hd-doc-err').textContent = 'This is a view-only account — changes are disabled.';
    return;
  }

  const fd = new FormData();
  fd.append('file', file);
  fd.append('kind', $('hd-doc-kind').value);
  fd.append('agreement_id', state.hire.id);
  fd.append('customer_id', state.hire.customer_id);

  const btn = $('hd-doc-upload');
  btn.disabled = true;
  try {
    const res = await fetch('/api/rentals/documents', {
      method: 'POST', body: fd, headers: { 'X-CSRF-Token': readCookie('goldstar_csrf') },
    });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(data.error || 'upload failed');
    input.value = '';
    toast('Uploaded');
    loadHireDocs(state.hire);
  } catch (e) {
    $('hd-doc-err').textContent = e.message;
  }
  btn.disabled = false;
});

// ── calendar ──────────────────────────────────────────────────────────────

/** Date arithmetic on plain ISO strings, never on Date objects with a time
    in them: these are calendar days, and a timezone has no business
    deciding which day a hire starts on. */
function isoAdd(iso, days) {
  const d = new Date(iso + 'T12:00:00Z');
  d.setUTCDate(d.getUTCDate() + days);
  return d.toISOString().slice(0, 10);
}

function isoDaysBetween(a, b) {
  return Math.round((Date.parse(b + 'T12:00:00Z') - Date.parse(a + 'T12:00:00Z')) / 86400000);
}

/** Monday-first, because a hire desk's week does not start on Sunday. */
function startOfWeek(iso) {
  const d = new Date(iso + 'T12:00:00Z');
  return isoAdd(iso, -((d.getUTCDay() + 6) % 7));
}

function startOfMonth(iso) { return iso.slice(0, 8) + '01'; }

function endOfMonth(iso) {
  const d = new Date(iso + 'T12:00:00Z');
  return new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth() + 1, 0, 12))
    .toISOString().slice(0, 10);
}

/** Turns the chosen period into a window. Kept as one function so the
    chips, the arrows and "Today" can never disagree about what "this
    month" means. */
function windowFor(period, anchor) {
  switch (period) {
    case 'week': return [startOfWeek(anchor), isoAdd(startOfWeek(anchor), 6)];
    case 'next-week': return [startOfWeek(isoAdd(anchor, 7)), isoAdd(startOfWeek(isoAdd(anchor, 7)), 6)];
    case 'month': return [startOfMonth(anchor), endOfMonth(anchor)];
    case 'next-month': {
      const nextish = isoAdd(endOfMonth(anchor), 1);
      return [startOfMonth(nextish), endOfMonth(nextish)];
    }
    default: return [state.cal.from, state.cal.to];
  }
}

/** How far an arrow moves: a week for the week views, a month for the
    month ones, and the length of the window itself for a custom range. */
function stepFor(period) {
  if (period === 'week' || period === 'next-week') return 7;
  if (period === 'month' || period === 'next-month') return 0; // handled by month maths
  return isoDaysBetween(state.cal.from, state.cal.to) + 1;
}

async function loadCalendar() {
  const c = state.cal;
  if (!c.from || !c.to) {
    const [from, to] = windowFor(c.period, todayISO());
    c.from = from; c.to = to;
  }

  let cal;
  try {
    cal = await api(`/api/rentals/calendar?from=${c.from}&to=${c.to}`);
  } catch (e) {
    $('cal-grid').innerHTML = `<div class="empty-box">${esc(e.message)}</div>`;
    return;
  }

  const days = [];
  for (let d = cal.from; d <= cal.to; d = isoAdd(d, 1)) days.push(d);
  $('cal-sub').textContent = `${ukDate(cal.from)} → ${ukDate(cal.to)} · ${cal.cars.length} car(s)`;

  if (!cal.cars.length) {
    $('cal-grid').innerHTML =
      '<div class="empty-box"><strong>No cars yet</strong>Add a loan car and it will appear here.</div>';
    return;
  }

  // One column per day, sized in the grid template so a bar can span days
  // by column rather than by pixel arithmetic that drifts as it widens.
  const cols = `grid-template-columns: var(--cal-label) repeat(${days.length}, minmax(34px, 1fr));`;
  const today = todayISO();

  let head = `<div class="cal-row cal-head" style="${cols}"><div class="cal-label"></div>` +
    days.map((d) => {
      const dt = new Date(d + 'T12:00:00Z');
      const dow = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'][(dt.getUTCDay() + 6) % 7];
      const weekend = dt.getUTCDay() === 0 || dt.getUTCDay() === 6;
      return `<div class="cal-day ${weekend ? 'is-weekend' : ''} ${d === today ? 'is-today' : ''}">
        <span class="cal-dow">${dow}</span><span class="cal-dom">${Number(d.slice(8))}</span></div>`;
    }).join('') + '</div>';

  const byCar = {};
  cal.hires.forEach((h) => { (byCar[h.vehicle_id] ??= []).push(h); });

  const rows = cal.cars.map((car) => {
    const cells = days.map((d) =>
      `<div class="cal-cell ${d === today ? 'is-today' : ''}"
            data-free-car="${car.id}" data-free-day="${d}"></div>`).join('');

    // Each hire is one bar placed by grid column, clipped to the window.
    const bars = (byCar[car.id] || []).map((h) => {
      const from = h.starts_on < cal.from ? cal.from : h.starts_on;
      const to = h.ends_on > cal.to ? cal.to : h.ends_on;
      const start = isoDaysBetween(cal.from, from) + 2; // +1 for the label column, +1 for 1-based
      const span = Math.max(1, isoDaysBetween(from, to) + 1);
      const tone = h.status === 'returned' ? 'is-done'
        : isOverdue(h) ? 'is-late'
        : h.status === 'out' ? 'is-out' : 'is-booked';
      return `<button class="cal-bar ${tone}" data-open-hire="${h.id}"
        style="grid-column: ${start} / span ${span}"
        title="${esc(h.customer_name)} · ${esc(ukDate(h.starts_on))} → ${esc(ukDate(h.ends_on))}">
        <span>${esc(h.customer_name)}</span></button>`;
    }).join('');

    return `<div class="cal-row" style="${cols}">
      <div class="cal-label">
        <span class="reg">${esc(car.registration)}</span>
        <span class="car-sub">${esc([car.make, car.model].filter(Boolean).join(' '))}</span>
      </div>${cells}${bars}</div>`;
  }).join('');

  $('cal-grid').innerHTML = head + rows;

  document.querySelectorAll('[data-open-hire]').forEach((b) =>
    b.addEventListener('click', () => openHire(b.dataset.openHire).catch((e) => toast(e.message, true))));

  // An empty day is an invitation: it opens the lend form for that car,
  // starting on that day, which is the whole point of a planning grid.
  document.querySelectorAll('[data-free-car]').forEach((cell) =>
    cell.addEventListener('click', () => {
      const car = cal.cars.find((c2) => String(c2.id) === cell.dataset.freeCar);
      if (car) openLendOn(car, cell.dataset.freeDay);
    }));
}

/** Switching period, as the chips do it. Its own function so the chips,
    "Today" and anything else all move the window the same way. */
function loadCalendarPeriod(period) {
  state.cal.period = period;
  document.querySelectorAll('#cal-periods .chip').forEach((o) =>
    o.setAttribute('aria-pressed', String(o.dataset.period === period)));
  $('cal-custom').hidden = period !== 'custom';
  if (period === 'custom') {
    $('cal-from').value = state.cal.from;
    $('cal-to').value = state.cal.to;
    return Promise.resolve();
  }
  const [from, to] = windowFor(period, todayISO());
  state.cal.from = from;
  state.cal.to = to;
  return loadCalendar();
}

document.querySelectorAll('#cal-periods .chip').forEach((c) =>
  c.addEventListener('click', () => {
    loadCalendarPeriod(c.dataset.period).catch((e) => toast(e.message, true));
  }));

$('cal-apply').addEventListener('click', () => {
  const from = $('cal-from').value;
  const to = $('cal-to').value;
  if (!from || !to) { toast('Pick both dates', true); return; }
  state.cal.from = from; state.cal.to = to;
  loadCalendar().catch((e) => toast(e.message, true));
});

/** The arrows move by whatever the current period is worth — a week for a
    week, a month for a month, and its own length for a custom range. */
function shiftCalendar(dir) {
  const p = state.cal.period;
  if (p === 'month' || p === 'next-month') {
    const anchor = dir > 0 ? isoAdd(endOfMonth(state.cal.from), 1) : isoAdd(state.cal.from, -1);
    state.cal.from = startOfMonth(anchor);
    state.cal.to = endOfMonth(anchor);
  } else {
    const step = stepFor(p) * dir;
    state.cal.from = isoAdd(state.cal.from, step);
    state.cal.to = isoAdd(state.cal.to, step);
  }
  loadCalendar().catch((e) => toast(e.message, true));
}

$('cal-prev').addEventListener('click', () => shiftCalendar(-1));
$('cal-next').addEventListener('click', () => shiftCalendar(1));
$('cal-today').addEventListener('click', () => {
  const [from, to] = windowFor(state.cal.period === 'custom' ? 'week' : state.cal.period, todayISO());
  state.cal.from = from; state.cal.to = to;
  loadCalendar().catch((e) => toast(e.message, true));
});

// ── settings ──────────────────────────────────────────────────────────────

/** The price list and the two outside services, edited here rather than on
    the dashboard: the person who lends cars out is the person who knows
    what a day's insurance costs, and they are already on this site. */
async function loadSettings() {
  const st = await api('/api/rentals/settings');
  state.settings = st;

  $('set-ins').value = st.rental_insurance_per_day || '';
  $('set-late').value = st.rental_late_fee_per_day || '';
  $('set-dep').value = st.rental_deposit_default || '';
  $('set-tw-sid').value = st.twilio_account_sid || '';
  $('set-tw-from').value = st.twilio_from_number || '';
  $('set-st-pub').value = st.stripe_publishable_key || '';
  $('set-st-return').value = st.stripe_return_url || '';
  // The two secrets are never sent back, so their boxes always start empty
  // — the state line beside the heading is how you know one is saved.
  $('set-tw-token').value = '';
  $('set-st-key').value = '';

  $('set-tw-state').innerHTML = st.twilio_ready
    ? '<span class="pill">ready</span>'
    : '<span class="pill flag">not set up</span>';
  $('set-st-state').innerHTML = st.stripe_ready
    ? '<span class="pill">ready</span>'
    : '<span class="pill flag">not set up</span>';
}

$('set-save').addEventListener('click', async () => {
  $('set-msg').textContent = 'Saving…';
  try {
    await api('/api/rentals/settings', {
      method: 'POST',
      json: {
        rental_insurance_per_day: $('set-ins').value,
        rental_late_fee_per_day: $('set-late').value,
        rental_deposit_default: $('set-dep').value,
        twilio_account_sid: $('set-tw-sid').value,
        twilio_auth_token: $('set-tw-token').value,
        twilio_from_number: $('set-tw-from').value,
        stripe_secret_key: $('set-st-key').value,
        stripe_publishable_key: $('set-st-pub').value,
        stripe_return_url: $('set-st-return').value,
      },
    });
    $('set-msg').textContent = 'Saved';
    await loadSettings();
    await loadRates();
    renderSetupNote();
  } catch (e) {
    $('set-msg').textContent = e.message;
  }
});

$('set-tw-send').addEventListener('click', async () => {
  const to = $('set-tw-test').value.trim();
  if (!to) { $('set-tw-msg').textContent = 'Put a number in first.'; return; }
  $('set-tw-msg').textContent = 'Sending…';
  try {
    await api('/api/rentals/settings/test-text', { method: 'POST', json: { to } });
    $('set-tw-msg').textContent = 'Sent — check the phone.';
  } catch (e) {
    $('set-tw-msg').textContent = e.message;
  }
});

/** Says what is not set up, on the page someone is already looking at,
    rather than letting them find out by pressing a button that fails.
    Silent once everything works — a permanent strip is one nobody reads. */
function renderSetupNote() {
  const st = state.settings || {};
  const missing = [];
  if (!st.twilio_ready) missing.push('texting');
  if (!st.stripe_ready) missing.push('card payments');
  if (!st.rental_insurance_per_day && !st.rental_late_fee_per_day) missing.push('prices');

  const note = $('setup-note');
  if (!missing.length) { note.hidden = true; note.innerHTML = ''; return; }
  note.hidden = false;
  note.innerHTML =
    `<strong>Not set up yet:</strong> ${esc(missing.join(', '))}.
     Texts and payments will refuse until their keys are in.
     <a href="#" id="setup-note-link">Open Settings</a>`;
  $('setup-note-link').addEventListener('click', (e) => { e.preventDefault(); show('settings'); });
}

// ── boot ──────────────────────────────────────────────────────────────────

$('btn-logout').addEventListener('click', async () => {
  try { await api('/api/logout', { method: 'POST' }); } catch {}
  location.href = '/';
});

/** The price list, read once so the lend form can offer the house rates
    without a round trip every time somebody opens it. */
async function loadRates() {
  try {
    const s2 = await api('/api/rentals/settings');
    state.settings = s2;
    state.rates = {
      insurance: s2.rental_insurance_per_day || '',
      lateFee: s2.rental_late_fee_per_day || '',
      deposit: s2.rental_deposit_default || '',
    };
  } catch { /* the form just opens with blanks */ }
}

Object.assign(viewLoaders, {
  today: loadToday,
  calendar: loadCalendar,
  settings: loadSettings,
  courtesy: loadCourtesy,
  money: loadMoney,
  hires: loadHires,
  cars: loadCars,
  customers: loadCustomers,
});

// The price list and setup state are needed before the first board renders,
// or its buttons decide whether they can work from an empty answer.
loadRates().then(() => show('today'));
