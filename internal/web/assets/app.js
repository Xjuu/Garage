/* Goldstar dashboard. No framework, no build step — the whole front end is
   this file plus app.css, embedded in the Go binary. */

'use strict';

// ── helpers ───────────────────────────────────────────────────────────────

const $ = (id) => document.getElementById(id);

/** Escape anything that came out of a PDF before it reaches innerHTML.
    Supplier names and descriptions are model output from an untrusted
    document, so they are never trusted as markup. */
function esc(v) {
  if (v === null || v === undefined) return '';
  return String(v)
    .replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;').replaceAll("'", '&#39;');
}

const nf = new Intl.NumberFormat('en-GB', { minimumFractionDigits: 2, maximumFractionDigits: 2 });
const nf0 = new Intl.NumberFormat('en-GB');

const money = (n) => nf.format(Number(n) || 0);
const int = (n) => nf0.format(Number(n) || 0);
const dash = (s) => (s === '' || s === null || s === undefined ? '<span class="muted">—</span>' : esc(s));

function readCookie(name) {
  return document.cookie.split('; ')
    .find((c) => c.startsWith(name + '='))?.split('=')[1] || '';
}

/** Every mutating call carries the CSRF cookie back as a header; the session
    cookie itself stays HttpOnly. */
async function api(path, opts = {}) {
  const o = { headers: {}, ...opts };
  if (o.method && o.method !== 'GET') {
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

/** A toast with a button — an offer, not a question that blocks anything.
    It sits for a few seconds and fades on its own exactly like any other
    toast if ignored; nothing about it demands a decision, and nothing on
    screen is disabled while it's up. Clicking the button both acts and
    dismisses it early. */
function actionToast(msg, actionLabel, onAction, ms = 6000) {
  const el = document.createElement('div');
  el.className = 'toast action';

  const text = document.createElement('span');
  text.textContent = msg;

  const btn = document.createElement('button');
  btn.className = 'btn sm';
  btn.textContent = actionLabel;
  btn.addEventListener('click', () => { el.remove(); onAction(); });

  el.appendChild(text);
  el.appendChild(btn);
  $('toasts').appendChild(el);
  setTimeout(() => el.remove(), ms);
}

function debounce(fn, ms) {
  let t;
  return (...a) => { clearTimeout(t); t = setTimeout(() => fn(...a), ms); };
}

/** Guards a view loader against overwriting the screen with a stale
    response. Switching tabs quickly, or typing a filter faster than the
    previous request for it has finished, starts a new fetch before the old
    one has come back — nothing stops the old one from resolving *after*
    the new one and painting outdated results over the current, correct
    ones, with no visible sign anything went wrong. It just looks like the
    page quietly showed the wrong thing.
    Usage: at the very top of an async loader, `const seq = beginLoad('x')`.
    Right after the fetch that could race (usually the first await), before
    any DOM is touched: `if (stale('x', seq)) return`. Only the most
    recently *started* call for that key is allowed to render — an older
    call finishing later always loses, regardless of resolution order. */
const loadSeq = {};
function beginLoad(key) { return (loadSeq[key] = (loadSeq[key] || 0) + 1); }
function stale(key, seq) { return loadSeq[key] !== seq; }

// ── state ─────────────────────────────────────────────────────────────────

const state = {
  view: 'overview',
  filters: { q: '', from: '', to: '', supplier: '', reg: '', review: '' },
  sort: 'date',
  dir: 'desc',
  page: 1,
  per: 50,
  total: 0,
  current: null,   // invoice open in the drawer
  parts: [],
  jobTimer: null,
};

function queryString(extra = {}) {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(state.filters)) if (v) p.set(k, v);
  p.set('sort', state.sort);
  p.set('dir', state.dir);
  p.set('page', state.page);
  p.set('per', state.per);
  for (const [k, v] of Object.entries(extra)) p.set(k, v);
  return p.toString();
}

// ── navigation ────────────────────────────────────────────────────────────

/* Navigation is two levels. Eleven top-level tabs meant the everyday pages —
   invoices, a car's costs — sat in the same undifferentiated row as things you
   touch once a month, like Training. Grouping puts five choices in the top row
   and hides the rest until they are relevant. */
const GROUPS = [
  { id: 'overview', label: 'Overview', views: [['overview', 'Overview']] },
  { id: 'invoices', label: 'Invoices', count: 'c-invoices', views: [['invoices', 'Invoices']] },
  {
    id: 'analysis', label: 'Analysis',
    views: [
      ['spending', 'Spending'],
      ['vehicles', 'Vehicles', 'c-vehicles'],
      ['parts', 'Parts', 'c-parts'],
      ['suppliers', 'Suppliers', 'c-suppliers'],
      ['vat', 'VAT'],
    ],
  },
  {
    id: 'setup', label: 'Setup',
    views: [
      ['fleet', 'Fleet'],
      ['training', 'Training', 'c-training'],
      ['admin', 'Admin'],
    ],
  },
];

// Detail pages are reached by clicking a row, not from the nav, but they still
// need to light up the group they belong to.
const EXTRA_VIEWS = { vehicle: 'analysis', part: 'analysis' };

const groupOf = {};
for (const g of GROUPS) for (const [view] of g.views) groupOf[view] = g.id;
Object.assign(groupOf, EXTRA_VIEWS);

function buildNav() {
  $('tabs').innerHTML = GROUPS.map((g) => `
    <button role="tab" data-group="${g.id}" data-view="${g.views[0][0]}">
      ${esc(g.label)}${g.count ? `<span class="count" id="${g.count}">0</span>` : ''}
    </button>`).join('');

  // Every sub-tab is rendered up front, including for inactive groups: the
  // count badges carry ids that other modules write into, and those must exist
  // whether or not the group is on screen.
  $('subtabs').innerHTML = GROUPS
    .filter((g) => g.views.length > 1)
    .map((g) => g.views.map(([view, label, count]) => `
      <button data-group="${g.id}" data-view="${view}" hidden>
        ${esc(label)}${count ? `<span class="count" id="${count}">0</span>` : ''}
      </button>`).join('')).join('');

  document.querySelectorAll('#tabs button, #subtabs button').forEach((b) =>
    b.addEventListener('click', () => show(b.dataset.view)));
}

// Every view actually shown gets remembered, so Escape can step back through
// it — same idea as a browser's back button, scoped to this one page. Capped
// so leaving the dashboard open for days doesn't grow this forever; nobody
// backs up more than a handful of steps in practice.
const viewHistory = [];
const VIEW_HISTORY_LIMIT = 30;
// Set while goBack() itself is calling show(), so that call doesn't push the
// view being left back onto the history it was just popped from — without
// this, Escape would step back one view and then be stuck bouncing between
// the last two forever instead of walking further back.
let navigatingBack = false;

function show(view) {
  if (!navigatingBack && state.view && state.view !== view) {
    viewHistory.push(state.view);
    if (viewHistory.length > VIEW_HISTORY_LIMIT) viewHistory.shift();
  }
  state.view = view;
  const group = groupOf[view] || 'overview';

  document.querySelectorAll('#tabs button').forEach((b) =>
    b.setAttribute('aria-selected', String(b.dataset.group === group)));

  document.querySelectorAll('#subtabs button').forEach((b) => {
    b.hidden = b.dataset.group !== group;
    b.setAttribute('aria-selected', String(b.dataset.view === view));
  });

  document.querySelectorAll('.view').forEach((s) =>
    s.classList.toggle('active', s.id === 'view-' + view));
  renderSectionShortcuts(view);
  loadView(view);
}

/** Steps back to whichever view was showing before the current one. A no-op
    on Overview with nothing behind it — there is nothing to go back to. */
function goBack() {
  const prev = viewHistory.pop();
  if (!prev) return;
  navigatingBack = true;
  show(prev);
  navigatingBack = false;
}

// Mutable so the fleet, training and admin modules can register their own
// views without this file needing to know about them.
const viewLoaders = {};

function loadView(view) {
  viewLoaders[view]?.().catch((e) => toast(e.message, true));
}

buildNav();

// ── overview ──────────────────────────────────────────────────────────────

async function loadOverview() {
  const seq = beginLoad('overview');
  const res = await api('/api/overview');
  if (stale('overview', seq)) return;
  const o = res.overview;
  renderThisMonth(res.this_month);

  $('c-invoices').textContent = int(o.invoices);
  $('c-vehicles').textContent = int(o.vehicles);
  $('c-suppliers').textContent = int(o.suppliers);
  $('overview-sub').textContent =
    `${int(o.invoices)} invoices · ${int(o.items)} line items`;

  const tiles = [
    { k: 'Invoices', v: int(o.invoices) },
    { k: 'Purchases', v: '£' + money(o.purchases), m: 'including VAT' },
    { k: 'VAT', v: '£' + money(o.vat), m: 'reclaimable input VAT' },
    { k: 'Vehicles', v: int(o.vehicles) },
    { k: 'Line items', v: int(o.items) },
  ];
  if (o.credit_count > 0) {
    tiles.push({
      k: 'Credit notes', v: '−£' + money(Math.abs(o.credits)),
      m: `${int(o.credit_count)} note(s)`,
    });
    tiles.push({ k: 'Net of credits', v: '£' + money(o.brutto), m: 'purchases less credits' });
  }
  if (o.needs_review > 0) {
    tiles.push({ k: 'Needs review', v: int(o.needs_review), m: 'figures did not reconcile', alert: true });
  }
  $('tiles').innerHTML = tiles.map((t) => `
    <div class="tile${t.alert ? ' alert' : ''}">
      <div class="k">${esc(t.k)}</div>
      <div class="v">${t.v}</div>
      ${t.m ? `<div class="m">${esc(t.m)}</div>` : ''}
    </div>`).join('');

  // Months arrive newest-first; a chart reads left-to-right oldest-first.
  const months = [...(o.months || [])].reverse();
  drawChart('bars', months.map((m) => ({
    label: m.month.slice(2),
    value: m.brutto,
    title: `${m.month} — £${money(m.brutto)} across ${int(m.invoices)} invoice(s)`,
  })));

  const vehicles = await api('/api/vehicles');
  $('top-vehicles').innerHTML = vehicles.length
    ? vehicles.slice(0, 8).map((v) => `
        <tr class="clickable" data-reg="${esc(v.vehicle_reg)}">
          <td>${vehicleCell(v.vehicle_reg, v.make, v.model, v.callsign)}</td>
          <td class="num">${int(v.invoices)}</td>
          <td class="num">${int(v.parts)}</td>
          <td class="num">${money(v.netto)}</td>
          <td class="num">${money(v.vat)}</td>
          <td class="num strong">${money(v.brutto)}</td>
        </tr>`).join('')
    : '<tr><td colspan="6" class="empty">No vehicle registrations recorded yet</td></tr>';

  $('top-vehicles').querySelectorAll('tr[data-reg]').forEach((tr) =>
    tr.addEventListener('click', () => openVehicle(tr.dataset.reg)));
}

// ── invoices ──────────────────────────────────────────────────────────────

async function loadInvoices() {
  const seq = beginLoad('invoices');
  const data = await api('/api/invoices?' + queryString());
  if (stale('invoices', seq)) return;
  state.total = data.total;

  $('inv-sub').textContent =
    `${int(data.total)} matching · net £${money(data.netto)} · VAT £${money(data.vat)} · gross £${money(data.brutto)}`;

  $('inv-rows').innerHTML = data.invoices.length
    ? data.invoices.map((inv) => {
        const parts = (inv.Items || [])
          .map((i) => i.PartNumber).filter(Boolean);
        const shown = parts.slice(0, 2).map((p) => `<span class="part">${esc(p)}</span>`).join(' ');
        const more = parts.length > 2 ? ` <span class="muted">+${parts.length - 2}</span>` : '';
        return `
        <tr class="clickable${inv.NeedsReview ? ' flagged' : ''}" data-id="${inv.ID}">
          <td class="mono">${dash(inv.InvoiceDate)}</td>
          <td class="truncate" title="${esc(inv.Supplier)}">${dash(inv.Supplier)}</td>
          <td class="mono">${dash(inv.InvoiceNumber)}</td>
          <td>${inv.VehicleReg ? `<span class="reg">${esc(inv.VehicleReg)}</span>` : '<span class="muted">—</span>'}</td>
          <td>${shown || '<span class="muted">—</span>'}${more}</td>
          <td class="num">${money(inv.Netto)}</td>
          <td class="num">${money(inv.VATAmount)}</td>
          <td class="num strong">${money(inv.Brutto)}</td>
          <td>${inv.NeedsReview ? '<span class="pill flag">Check</span>' : ''}</td>
        </tr>`;
      }).join('')
    : `<tr><td colspan="9" class="empty">
         <strong>Nothing to show</strong>
         Sync the mailbox, or drop an invoice above to get started.
       </td></tr>`;

  $('inv-rows').querySelectorAll('tr[data-id]').forEach((tr) =>
    tr.addEventListener('click', () => openInvoice(tr.dataset.id)));

  const from = data.total === 0 ? 0 : (state.page - 1) * state.per + 1;
  const to = Math.min(state.page * state.per, data.total);
  $('inv-info').textContent = `${int(from)}–${int(to)} of ${int(data.total)}`;
  $('page-prev').disabled = state.page <= 1;
  $('page-next').disabled = state.page * state.per >= data.total;

  // Scoped to the Invoices view specifically: the Fleet registry table has
  // its own th.sortable headers with entirely separate state (see fleet.js),
  // and an unscoped selector here would pick those up too and try to sort
  // the invoice list on a click meant for the registry.
  document.querySelectorAll('#view-invoices th.sortable').forEach((th) => {
    const on = th.dataset.sort === state.sort;
    th.classList.toggle('sorted', on);
    th.querySelector('.arrow').textContent = on && state.dir === 'asc' ? '↑' : '↓';
  });
}

document.querySelectorAll('#view-invoices th.sortable').forEach((th) =>
  th.addEventListener('click', () => {
    const col = th.dataset.sort;
    if (state.sort === col) {
      state.dir = state.dir === 'asc' ? 'desc' : 'asc';
    } else {
      state.sort = col;
      state.dir = 'desc';
    }
    state.page = 1;
    loadInvoices().catch((e) => toast(e.message, true));
  }));

const refilter = debounce(() => {
  state.page = 1;
  loadInvoices().catch((e) => toast(e.message, true));
}, 260);

$('f-q').addEventListener('input', (e) => { state.filters.q = e.target.value; refilter(); });
for (const [id, key] of [['f-from', 'from'], ['f-to', 'to'], ['f-supplier', 'supplier'],
                         ['f-reg', 'reg'], ['f-review', 'review']]) {
  $(id).addEventListener('change', (e) => {
    state.filters[key] = e.target.value;
    state.page = 1;
    loadInvoices().catch((err) => toast(err.message, true));
  });
}

$('f-clear').addEventListener('click', () => {
  state.filters = { q: '', from: '', to: '', supplier: '', reg: '', review: '' };
  ['f-q', 'f-from', 'f-to', 'f-supplier', 'f-reg', 'f-review'].forEach((id) => { $(id).value = ''; });
  state.page = 1;
  loadInvoices().catch((e) => toast(e.message, true));
});

$('page-prev').addEventListener('click', () => {
  if (state.page > 1) { state.page--; loadInvoices().catch((e) => toast(e.message, true)); }
});
$('page-next').addEventListener('click', () => {
  if (state.page * state.per < state.total) { state.page++; loadInvoices().catch((e) => toast(e.message, true)); }
});

function filterByVehicle(reg) {
  state.filters = { q: '', from: '', to: '', supplier: '', reg, review: '' };
  $('f-reg').value = reg;
  $('f-q').value = '';
  state.page = 1;
  show('invoices');
}

function filterBySupplier(supplier) {
  state.filters = { q: '', from: '', to: '', supplier, reg: '', review: '' };
  $('f-supplier').value = supplier;
  $('f-q').value = '';
  state.page = 1;
  show('invoices');
}

function searchFor(text) {
  state.filters = { q: text, from: '', to: '', supplier: '', reg: '', review: '' };
  $('f-q').value = text;
  ['f-supplier', 'f-reg', 'f-review'].forEach((id) => { $(id).value = ''; });
  state.page = 1;
  show('invoices');
}

async function loadFilters() {
  const f = await api('/api/filters');
  const fill = (id, values, current) => {
    $(id).innerHTML = '<option value="">All</option>' +
      values.map((v) => `<option value="${esc(v)}"${v === current ? ' selected' : ''}>${esc(v)}</option>`).join('');
  };
  fill('f-supplier', f.suppliers, state.filters.supplier);
  fill('f-reg', f.vehicles, state.filters.reg);
}

// ── drawer ────────────────────────────────────────────────────────────────

function field(label, key, value, type = 'text') {
  return `<div class="field">
    <label for="e-${key}">${esc(label)}</label>
    <input type="${type}" id="e-${key}" data-key="${key}" value="${esc(value)}"
           ${type === 'number' ? 'step="0.01"' : ''}>
  </div>`;
}

async function openInvoice(id) {
  try {
    const inv = await api('/api/invoices/' + id);
    state.current = inv;

    $('d-title').textContent = `${inv.Supplier || 'Invoice'} · ${inv.InvoiceNumber || '#' + inv.ID}`;

    const items = (inv.Items || []).map((it) => `
      <tr>
        <td>${it.PartNumber ? `<span class="part">${esc(it.PartNumber)}</span>` : '<span class="muted">—</span>'}</td>
        <td class="truncate" title="${esc(it.Desc)}">${dash(it.Desc)}</td>
        <td class="num">${it.Quantity ? int(it.Quantity) : '—'}</td>
        <td class="num">${money(it.UnitPrice)}</td>
        <td class="num">${money(it.Netto)}</td>
        <td class="num">${money(it.VATAmount)}</td>
        <td class="num strong">${money(it.Brutto)}</td>
      </tr>`).join('');

    $('d-body').innerHTML = `
      ${inv.NeedsReview && inv.Notes ? `<div class="note"><strong>Needs review.</strong> ${esc(inv.Notes)}</div>` : ''}

      <div class="grid2">
        ${field('Supplier', 'supplier', inv.Supplier)}
        ${field('Invoice number', 'invoice_number', inv.InvoiceNumber)}
        ${field('Date of purchase', 'invoice_date', inv.InvoiceDate, 'date')}
        ${field('Vehicle registration', 'vehicle_reg', inv.VehicleReg)}
        ${field('Currency', 'currency', inv.Currency)}
        ${field('VAT rate %', 'vat_rate', inv.VATRate, 'number')}
        ${field('Net', 'netto', inv.Netto, 'number')}
        ${field('VAT', 'vat_amount', inv.VATAmount, 'number')}
        ${field('Gross', 'brutto', inv.Brutto, 'number')}
      </div>

      <div class="section-title">Line items (${(inv.Items || []).length})</div>
      <div class="panel"><div class="table-scroll"><table>
        <thead><tr>
          <th>Part</th><th>Description</th><th class="num">Qty</th><th class="num">Unit</th>
          <th class="num">Net</th><th class="num">VAT</th><th class="num">Gross</th>
        </tr></thead>
        <tbody>${items || '<tr><td colspan="7" class="empty">No line items</td></tr>'}</tbody>
      </table></div></div>

      <div class="section-title">Provenance</div>
      <div class="panel" style="padding:14px 16px;font-size:13px;line-height:1.9">
        <div><span class="muted">From</span> ${dash(inv.MailFrom)}</div>
        <div><span class="muted">Subject</span> ${dash(inv.MailSubject)}</div>
        <div><span class="muted">Archived file</span> <span class="mono">${dash((inv.SourceFile || '').split('/').pop())}</span></div>
        <div><span class="muted">Checksum</span> <span class="mono">${esc((inv.FileSHA256 || '').slice(0, 16))}…</span></div>
      </div>`;

    // The source document is on disk, so this opens instantly and costs no API
  // call — the model only ever reads each invoice once, at ingest.
  $('d-original').href = '/api/invoices/' + inv.ID + '/file';
  showDrawerFooter('invoice');
    $('drawer').classList.add('open');
    $('drawer').setAttribute('aria-hidden', 'false');
    $('scrim').classList.add('open');
  } catch (e) {
    toast(e.message, true);
  }
}

function closeDrawer() {
  $('drawer').classList.remove('open');
  $('drawer').setAttribute('aria-hidden', 'true');
  $('scrim').classList.remove('open');
  state.current = null;
  state.editingVehicle = null;
  state.editingExample = null;
}

/** The drawer is reused for invoices, vehicles and training examples, so only
    the footer belonging to the current mode is shown. */
function showDrawerFooter(mode) {
  for (const name of ['invoice', 'vehicle', 'example']) {
    $('drawer-foot-' + name).style.display = name === mode ? 'flex' : 'none';
  }
}

$('d-close').addEventListener('click', closeDrawer);
$('scrim').addEventListener('click', closeDrawer);
document.addEventListener('keydown', (e) => { if (e.key === 'Escape') closeDrawer(); });

// Escape already closes whatever overlay is open — the drawer above, a
// modal, the search dropdown. When none of those are open, the same key
// steps back to the previous view instead, the way a browser's back button
// would. One Escape press does one thing: close the topmost thing on
// screen, or if there is nothing to close, go back — never both at once.
//
// Registered on the capture phase specifically, so it runs and reads this
// state BEFORE the handlers above have acted on the very same keystroke. A
// bubble-phase check here would run after closeDrawer() and the others had
// already closed everything, see their already-closed state, wrongly
// conclude nothing was open, and go back on top of the close.
document.addEventListener('keydown', (e) => {
  if (e.key !== 'Escape') return;
  const somethingOpen =
    $('drawer').classList.contains('open') ||
    !$('gen-modal').hidden ||
    !$('files-modal').hidden ||
    !$('omni-results').hidden;
  if (somethingOpen) return;
  goBack();
}, true);

// ── keyboard shortcuts ───────────────────────────────────────────────────
// The handful of actions worth a key of their own: the two most-clicked
// buttons in the topbar, and jumping straight to a top-level tab. Firing a
// real click() on the actual button — rather than calling its handler
// directly — means a shortcut is automatically a no-op exactly when the
// button itself would be: disabled while a sync or upload is already
// running, for instance, so nothing here needs to duplicate that state.
//
// 1-4 match the four tabs in the exact order they appear on screen — the
// same landing view a click on that tab uses (Analysis lands on Spending,
// Setup on Fleet, because that's what clicking the tab itself already does;
// a keyboard shortcut that behaved differently from the mouse would be its
// own kind of confusing).
const TOP_SHORTCUTS = [
  ['1', 'overview', 'Overview'],
  ['2', 'invoices', 'Invoices'],
  ['3', 'spending', 'Analysis'],
  ['4', 'fleet', 'Setup'],
];
const TOP_SHORTCUT_KEYS = Object.fromEntries(TOP_SHORTCUTS.map(([k, view]) => [k, view]));

// Once inside Analysis or Setup, its subtabs get their own first-letter
// shortcut — but only while a view from that group is actually on screen,
// and only for the groups that have subtabs to jump between at all.
//
// S, U and G are off-limits here, full stop — they stay Sync/Upload/Generate
// everywhere, including inside these sections, rather than being shadowed
// by a subtab that happens to also start with one of those letters. A
// letter's natural word keeps it when nothing else in the same section
// wants it too (Vehicles keeps V, Parts keeps P — neither collides with
// anything); a word that loses its own first letter, whether to the global
// reservation or to another subtab in the same group, falls back to the
// first letter later in its own name that neither reservation claims:
//   Spending  → S is global (Sync)                        → E (spEnding)
//   Suppliers → S is global, U is global                   → L (suppLiers)
//   VAT       → V is Vehicles' (the more central one keeps it) → A (vAt)
const SECTION_SHORTCUTS = {
  analysis: [
    ['v', 'vehicles', 'Vehicles'],
    ['p', 'parts', 'Parts'],
    ['e', 'spending', 'Spending'],
    ['l', 'suppliers', 'Suppliers'],
    ['a', 'vat', 'VAT'],
  ],
  setup: [
    ['f', 'fleet', 'Fleet'],
    ['t', 'training', 'Training'],
    ['a', 'admin', 'Admin'],
  ],
};

/** Redraws the contextual part of the shortcuts bar to match whichever
    section (if any) `view` belongs to — called from show() so it can never
    drift out of sync with what Escape/1-4/clicking a tab actually land on. */
function renderSectionShortcuts(view) {
  const group = groupOf[view];
  const items = SECTION_SHORTCUTS[group];
  if (!items) { $('section-shortcuts').innerHTML = ''; return; }

  // A labelled group of its own, not just more chips blended into the
  // always-on row: a plain divider was easy to miss, so this spells out
  // which section these belong to and gives them a visibly different chip
  // — a solid ink border instead of the soft grey one — so "always
  // available" and "only right now, in Analysis" read apart at a glance.
  const groupLabel = GROUPS.find((g) => g.id === group)?.label || '';
  $('section-shortcuts').innerHTML =
    `<span class="section-label">In ${esc(groupLabel)}</span>` +
    items.map(([k, , label]) =>
      `<span class="chip section-chip"><kbd>${esc(k.toUpperCase())}</kbd> ${esc(label)}</span>`).join('');
}

document.addEventListener('keydown', (e) => {
  if (e.metaKey || e.ctrlKey || e.altKey) return;

  // Never while typing, and never while a drawer or dialog already has the
  // user's attention — jumping tabs out from under an open invoice or a
  // half-filled form would lose more than it saves.
  const tag = document.activeElement?.tagName;
  if (tag === 'INPUT' || tag === 'SELECT' || tag === 'TEXTAREA' || document.activeElement?.isContentEditable) return;
  if ($('drawer').classList.contains('open')) return;
  if (!$('gen-modal').hidden || !$('files-modal').hidden) return;

  if (TOP_SHORTCUT_KEYS[e.key]) { e.preventDefault(); show(TOP_SHORTCUT_KEYS[e.key]); return; }

  // Section shortcuts never share a letter with S/U/G (see SECTION_SHORTCUTS),
  // so checking them first rather than after the switch below is just
  // organisation, not a priority order that actually has to matter.
  const key = e.key.toLowerCase();
  const section = SECTION_SHORTCUTS[groupOf[state.view]];
  const hit = section?.find(([k]) => k === key);
  if (hit) { e.preventDefault(); show(hit[1]); return; }

  switch (key) {
    case 's': e.preventDefault(); $('btn-sync').click(); break;
    case 'u': e.preventDefault(); $('btn-upload').click(); break;
    case 'g': e.preventDefault(); $('btn-sheet').click(); break;
  }
});

$('d-save').addEventListener('click', async () => {
  if (!state.current) return;
  const patch = {};
  $('d-body').querySelectorAll('input[data-key]').forEach((el) => {
    patch[el.dataset.key] = el.type === 'number' ? Number(el.value) : el.value;
  });
  try {
    await api('/api/invoices/' + state.current.ID, { method: 'PATCH', json: patch });
    toast('Saved');
    closeDrawer();
    refreshAll();
  } catch (e) { toast(e.message, true); }
});

$('d-reviewed').addEventListener('click', async () => {
  if (!state.current) return;
  try {
    await api('/api/invoices/' + state.current.ID, {
      method: 'PATCH', json: { needs_review: false },
    });
    toast('Marked reviewed');
    closeDrawer();
    refreshAll();
  } catch (e) { toast(e.message, true); }
});

$('d-doc').addEventListener('click', () => {
  if (state.current) window.open('/api/doc?id=' + state.current.ID, '_blank', 'noopener');
});

$('d-delete').addEventListener('click', async () => {
  if (!state.current) return;
  const label = `${state.current.Supplier || 'this invoice'} ${state.current.InvoiceNumber || ''}`.trim();
  if (!confirm(`Delete ${label}?\n\nThe archived document stays on disk — only the extracted record is removed.`)) return;
  try {
    await api('/api/invoices/' + state.current.ID, { method: 'DELETE' });
    toast('Deleted');
    closeDrawer();
    refreshAll();
  } catch (e) { toast(e.message, true); }
});

// ── aggregate views ───────────────────────────────────────────────────────

async function loadVehicles() {
  const seq = beginLoad('vehicles');
  const rows = await api('/api/vehicles');
  if (stale('vehicles', seq)) return;
  $('c-vehicles').textContent = int(rows.length);
  $('veh-rows').innerHTML = rows.length
    ? rows.map((v) => `
        <tr class="clickable" data-reg="${esc(v.vehicle_reg)}">
          <td>${vehicleCell(v.vehicle_reg, v.make, v.model, v.callsign)}</td>
          <td class="num">${int(v.invoices)}</td>
          <td class="num">${int(v.parts)}</td>
          <td class="num">${money(v.netto)}</td>
          <td class="num">${money(v.vat)}</td>
          <td class="num strong">${money(v.brutto)}</td>
          <td class="mono">${dash(v.last_date)}</td>
        </tr>`).join('')
    : '<tr><td colspan="7" class="empty"><strong>No vehicles yet</strong>Registrations appear once invoices name them.</td></tr>';

  $('veh-rows').querySelectorAll('tr[data-reg]').forEach((tr) =>
    tr.addEventListener('click', () => openVehicle(tr.dataset.reg)));
}

async function loadSuppliers() {
  const seq = beginLoad('suppliers');
  const rows = await api('/api/suppliers');
  if (stale('suppliers', seq)) return;
  $('c-suppliers').textContent = int(rows.length);
  $('sup-rows').innerHTML = rows.length
    ? rows.map((s) => `
        <tr class="clickable" data-supplier="${esc(s.supplier)}">
          <td class="strong">${esc(s.supplier)}</td>
          <td class="num">${int(s.invoices)}</td>
          <td class="num">${money(s.netto)}</td>
          <td class="num">${money(s.vat)}</td>
          <td class="num strong">${money(s.brutto)}</td>
          <td class="mono">${dash(s.last_date)}</td>
        </tr>`).join('')
    : '<tr><td colspan="6" class="empty">No suppliers yet</td></tr>';

  $('sup-rows').querySelectorAll('tr[data-supplier]').forEach((tr) =>
    tr.addEventListener('click', () => filterBySupplier(tr.dataset.supplier)));
}

async function loadParts() {
  const seq = beginLoad('parts');
  const rows = await api('/api/parts');
  if (stale('parts', seq)) return;
  state.parts = rows;
  $('c-parts').textContent = int(state.parts.length);
  renderParts();
}

function renderParts() {
  const q = $('parts-filter').value.trim().toLowerCase();
  const rows = q
    ? state.parts.filter((p) =>
        (p.part_number || '').toLowerCase().includes(q) ||
        (p.description || '').toLowerCase().includes(q))
    : state.parts;

  $('part-rows').innerHTML = rows.length
    ? rows.map((p) => `
        <tr class="clickable" data-part="${esc(p.part_number)}">
          <td><span class="part">${esc(p.part_number)}</span></td>
          <td class="truncate" title="${esc(p.description)}">${dash(p.description)}</td>
          <td class="num">${int(p.times)}</td>
          <td class="num">${int(p.quantity)}</td>
          <td class="num strong">${money(p.netto)}</td>
          <td class="num">${int(p.vehicles)}</td>
          <td class="mono">${dash(p.last_date)}</td>
        </tr>`).join('')
    : '<tr><td colspan="7" class="empty">No parts match</td></tr>';

  $('part-rows').querySelectorAll('tr[data-part]').forEach((tr) =>
    tr.addEventListener('click', () => openPart(tr.dataset.part)));
}

$('parts-filter').addEventListener('input', debounce(renderParts, 160));

async function loadVAT() {
  const seq = beginLoad('vat');
  const rows = await api('/api/months');
  if (stale('vat', seq)) return;
  const t = rows.reduce((a, m) => ({
    invoices: a.invoices + m.invoices, netto: a.netto + m.netto,
    vat: a.vat + m.vat, brutto: a.brutto + m.brutto,
  }), { invoices: 0, netto: 0, vat: 0, brutto: 0 });

  $('vat-rows').innerHTML = rows.length
    ? rows.map((m) => `
        <tr>
          <td class="mono strong">${esc(m.month)}</td>
          <td class="num">${int(m.invoices)}</td>
          <td class="num">${money(m.netto)}</td>
          <td class="num strong">${money(m.vat)}</td>
          <td class="num">${money(m.brutto)}</td>
        </tr>`).join('') + `
        <tr style="border-top:2px solid var(--ink)">
          <td class="strong">TOTAL</td>
          <td class="num strong">${int(t.invoices)}</td>
          <td class="num strong">${money(t.netto)}</td>
          <td class="num strong">${money(t.vat)}</td>
          <td class="num strong">${money(t.brutto)}</td>
        </tr>`
    : '<tr><td colspan="5" class="empty">No dated invoices yet</td></tr>';
}

// ── jobs: sync + upload ───────────────────────────────────────────────────

function showConsole(on) { $('console').classList.toggle('show', on); }

async function pollJob() {
  let s;
  try {
    s = await api('/api/job');
  } catch { return; }

  const dot = $('job-dot');
  dot.className = 'dot ' + s.state;
  $('job-title').textContent = {
    idle: 'Idle', running: `Running ${s.kind}…`,
    done: `Finished ${s.kind}`, failed: `${s.kind} failed`,
  }[s.state] || s.state;

  const lines = (s.log || []).map(esc);
  if (s.error) lines.push(`<span class="err">error: ${esc(s.error)}</span>`);
  const log = $('job-log');
  const atBottom = log.scrollTop + log.clientHeight >= log.scrollHeight - 30;
  log.innerHTML = lines.join('\n') || 'No output yet.';
  if (atBottom) log.scrollTop = log.scrollHeight;

  $('job-cancel').disabled = s.state !== 'running';
  const busy = s.state === 'running';
  $('btn-sync').disabled = busy;
  $('btn-upload').disabled = busy;
  $('sync-label').textContent = busy ? 'Syncing…' : 'Sync mailbox';

  if (busy) {
    if (!state.jobTimer) state.jobTimer = setInterval(pollJob, 1000);
  } else if (state.jobTimer) {
    clearInterval(state.jobTimer);
    state.jobTimer = null;
    if (s.state === 'done') toast(s.result || 'Finished');
    if (s.state === 'failed') toast(s.error || 'Job failed', true);
    refreshAll();
  }
}

$('btn-sync').addEventListener('click', async () => {
  try {
    await api('/api/fetch', { method: 'POST' });
    showConsole(true);
    pollJob();
  } catch (e) { toast(e.message, true); }
});

$('job-cancel').addEventListener('click', () => api('/api/job/cancel', { method: 'POST' }).catch(() => {}));
$('job-hide').addEventListener('click', () => showConsole(false));

async function uploadFiles(files) {
  if (!files || !files.length) return;
  const fd = new FormData();
  for (const f of files) fd.append('files', f);
  try {
    await api('/api/upload', { method: 'POST', body: fd });
    showConsole(true);
    pollJob();
  } catch (e) { toast(e.message, true); }
}

$('btn-upload').addEventListener('click', () => $('file-input').click());
$('file-input').addEventListener('change', (e) => {
  uploadFiles(e.target.files);
  e.target.value = '';
});

const dz = $('dropzone');
['dragenter', 'dragover'].forEach((ev) =>
  dz.addEventListener(ev, (e) => { e.preventDefault(); dz.classList.add('hot'); }));
['dragleave', 'drop'].forEach((ev) =>
  dz.addEventListener(ev, (e) => { e.preventDefault(); dz.classList.remove('hot'); }));
dz.addEventListener('drop', (e) => uploadFiles(e.dataTransfer.files));
dz.addEventListener('click', () => $('file-input').click());

// Dropping anywhere else must not make the browser navigate to the file.
['dragover', 'drop'].forEach((ev) =>
  window.addEventListener(ev, (e) => { if (e.target !== dz) e.preventDefault(); }));

// ── exports & session ─────────────────────────────────────────────────────

// Exports follow the active filters, so a download matches what is on screen.
// Wrapped in arrows on purpose. These two live in exports.js, which the page
// loads after this file, so naming them directly here reads an identifier that
// does not exist yet — a ReferenceError that kills the rest of this script.
// The arrow defers the lookup to click time, by which point every script has run.
$('btn-sheet').addEventListener('click', () => openGenerate());
$('btn-files').addEventListener('click', () => openFiles());

$('btn-logout').addEventListener('click', async () => {
  try { await api('/api/logout', { method: 'POST' }); } catch {}
  location.href = '/';
});

/** The headline the front page exists to answer: what has this month cost, and
    is that more or less than the same point last month. The comparison uses the
    same number of days into the previous month, because measuring a part-month
    against a whole one always reads as a fall. */
function renderThisMonth(m) {
  if (!m) return;

  let trend;
  if (!m.has_prev) {
    trend = { k: `vs ${m.prev_label}`, v: 'no data', s: 'nothing recorded in that period' };
  } else {
    const pct = m.change_pct;
    const dir = Math.abs(pct) < 0.5 ? 'flat' : (pct > 0 ? 'up' : 'down');
    const word = { up: 'higher', down: 'lower', flat: 'about level' }[dir];
    trend = {
      k: `vs ${m.prev_label}`,
      html: `<span class="trend ${dir}">${dir === 'flat' ? 'level' : (pct > 0 ? '+' : '') + pct.toFixed(1) + '%'}</span>`,
      s: `${word} than £${money(m.prev_brutto)} by day ${int(m.day_of_month)} of ${m.prev_label}`,
    };
  }

  $('month-title').textContent = m.month;
  // Credit notes are shown as their own tile rather than netted away. One big
  // credit can otherwise turn a month of real purchasing into a negative
  // "spend" figure that reads as an error.
  const monthTiles = [
    { k: 'Bought this month', v: '£' + money(m.purchases), s: `including VAT · to day ${int(m.day_of_month)}` },
    trend,
    { k: 'VAT', v: '£' + money(m.vat), s: 'reclaimable input VAT' },
    { k: 'Repairs', v: int(m.invoices - m.credit_count) },
    {
      k: 'Average / repair',
      v: '£' + money((m.invoices - m.credit_count) ? m.purchases / (m.invoices - m.credit_count) : 0),
    },
  ];
  if (m.credit_count > 0) {
    monthTiles.push({
      k: 'Credit notes', v: '−£' + money(Math.abs(m.credits)),
      s: `${int(m.credit_count)} note(s) · net £${money(m.brutto)} after credits`,
    });
  }
  $('month-tiles').innerHTML = monthTiles.map((t) => `
    <div class="tile">
      <div class="k">${esc(t.k)}</div>
      <div class="v">${t.html || esc(t.v)}</div>
      ${t.s ? `<div class="m">${esc(t.s)}</div>` : ''}
    </div>`).join('');
}

/** Badge counts are written by whichever view owns them, so a tab never opened
    would sit on a stale zero. This fills in the ones no view has loaded yet. */
async function refreshCounts() {
  // loadRecentFiles lives in exports.js and also sets the badge, so this is a
  // single request rather than two for the same data.
  //
  // Parts has no equivalent: unlike vehicles and suppliers, whose counts ride
  // along in the Overview response that already loads on every boot, nothing
  // else ever asks for the parts list — so its badge sat on a stale "0" until
  // the Parts tab was actually opened once. Fetched here as its own small
  // request specifically to fill that gap.
  await Promise.all([
    loadRecentFiles(),
    api('/api/parts').then((rows) => { $('c-parts').textContent = int(rows.length); }),
  ]);
}

// ── boot ──────────────────────────────────────────────────────────────────

function refreshAll() {
  loadFilters().catch(() => {});
  loadRecentFiles().catch(() => {});
  loadView(state.view);
  if (state.view !== 'overview') loadOverview().catch(() => {});
}

Object.assign(viewLoaders, {
  overview: loadOverview,
  invoices: loadInvoices,
  vehicles: loadVehicles,
  parts: loadParts,
  suppliers: loadSuppliers,
  vat: loadVAT,
});

// Boot only once every script has run. Several of the functions used here are
// defined in files the page loads after this one, so calling them at this
// file's top level would read names that do not exist yet — the mistake that
// silently killed the rest of this script once already. DOMContentLoaded fires
// after all classic scripts in the document have executed, which is exactly
// the guarantee needed.
document.addEventListener('DOMContentLoaded', () => {
  // Artwork first, so the first table drawn already uses it if present.
  loadCustomIcons().finally(() => show(state.view));
  startPing();
  loadFilters().catch(() => {});
  show('overview');
  refreshCounts().catch(() => {});
  pollJob();
});
