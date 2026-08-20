/* Fleet registry, plus the per-vehicle and per-part statistics pages.
   Relies on the helpers defined in app.js. */

'use strict';

let companies = [];

// ── navigation into the detail pages ─────────────────────────────────────

function openVehicle(reg) {
  state.currentReg = reg;
  show('vehicle');
}

function openPart(part) {
  state.currentPart = part;
  show('part');
}

$('veh-back').addEventListener('click', () => show('vehicles'));
$('part-back').addEventListener('click', () => show('parts'));

// ── vehicle detail ────────────────────────────────────────────────────────

async function loadVehicleDetail() {
  const reg = state.currentReg;
  if (!reg) { show('vehicles'); return; }

  // Keyed on the registration, not just "vehicle": clicking one vehicle row
  // then quickly clicking another before the first request finishes must
  // never let vehicle A's data land on a page currently open on vehicle B.
  const seq = beginLoad('vehicle:' + reg);
  const d = await api('/api/vehicle/' + encodeURIComponent(reg));
  if (stale('vehicle:' + reg, seq) || state.currentReg !== reg) return;
  const v = d.vehicle;

  // The silhouette sits in the heading beside the plate, at a size where the
  // body type actually reads.
  $('veh-title').innerHTML =
    `<span class="veh-hero">${carIcon(v.make, v.model, 'lg')}<span>${esc(v.registration)}</span></span>`;
  const bits = [v.make, v.model, v.year].filter(Boolean).join(' ');
  // The callsign is the number the office actually uses on the radio, so it
  // belongs in the heading of a vehicle's history rather than buried in notes.
  const callsign = (v.notes || '').startsWith('Callsign ')
    ? v.notes.slice('Callsign '.length).split(' · ')[0]
    : '';

  $('veh-sub').innerHTML = [
    callsign ? `<span class="callsign">${esc(callsign)}</span>` : '',
    esc(bits),
    esc(v.company_name || ''),
    v.driver ? 'driver ' + esc(v.driver) : '',
  ].filter(Boolean).join(' · ') || 'not in the registry yet';

  $('veh-tiles').innerHTML = [
    { k: 'Gross spend', v: '£' + money(v.brutto) },
    { k: 'Net', v: '£' + money(v.netto) },
    { k: 'VAT', v: '£' + money(v.vat) },
    { k: 'Invoices', v: int(v.invoices) },
    { k: 'Avg / month', v: '£' + money(d.avg_per_month), m: `over ${int(d.months_active)} month(s)` },
    { k: 'First seen', v: v.first_seen || '—' },
    { k: 'Last seen', v: v.last_seen || '—' },
  ].map((t) => `
    <div class="tile"><div class="k">${esc(t.k)}</div><div class="v">${t.v}</div>
    ${t.m ? `<div class="m">${esc(t.m)}</div>` : ''}</div>`).join('');

  $('veh-spending').onclick = () => openSpendingFor(v.registration);

  drawChart('veh-bars', (d.months || []).slice().reverse().map((m) => ({
    label: m.month.slice(2),
    value: m.brutto,
    title: `${m.month} — £${money(m.brutto)}`,
  })));

  $('veh-suppliers').innerHTML = (d.by_supplier || []).length
    ? d.by_supplier.map((s) => `
        <tr><td class="truncate">${esc(s.supplier)}</td>
        <td class="num">${int(s.invoices)}</td>
        <td class="num strong">${money(s.brutto)}</td></tr>`).join('')
    : '<tr><td colspan="3" class="empty">None</td></tr>';

  $('veh-parts').innerHTML = (d.parts || []).length
    ? d.parts.map((p) => `
        <tr class="clickable" data-part="${esc(p.part_number)}">
          <td><span class="part">${esc(p.part_number)}</span></td>
          <td class="num">${int(p.times)}</td>
          <td class="num strong">${money(p.netto)}</td></tr>`).join('')
    : '<tr><td colspan="3" class="empty">No parts recorded</td></tr>';
  $('veh-parts').querySelectorAll('tr[data-part]').forEach((tr) =>
    tr.addEventListener('click', () => openPart(tr.dataset.part)));

  $('veh-invoices').innerHTML = (d.invoices || []).length
    ? d.invoices.map((inv) => `
        <tr class="clickable" data-id="${inv.ID}">
          <td class="mono">${dash(inv.InvoiceDate)}</td>
          <td class="truncate">${dash(inv.Supplier)}</td>
          <td class="mono">${dash(inv.InvoiceNumber)}</td>
          <td class="num">${money(inv.Netto)}</td>
          <td class="num">${money(inv.VATAmount)}</td>
          <td class="num strong">${money(inv.Brutto)}</td></tr>`).join('')
    : '<tr><td colspan="6" class="empty">No invoices</td></tr>';
  $('veh-invoices').querySelectorAll('tr[data-id]').forEach((tr) =>
    tr.addEventListener('click', () => openInvoice(tr.dataset.id)));

  renderVehicleSpec(v, d.last_timing_belt, d.has_timing_belt);

  $('veh-repairs').innerHTML = (d.repairs || []).length
    ? d.repairs.map((r) => `
        <tr>
          <td class="mono">${dash(r.service_date)}</td>
          <td>${esc(serviceTypeLabel(r))}</td>
          <td class="num">${r.mileage ? int(r.mileage) : '—'}</td>
          <td>${r.timing_belt_changed ? '<span class="pill">Changed</span>' : ''}</td>
          <td class="truncate" title="${esc(r.description)}">${dash(r.description)}</td>
        </tr>`).join('')
    : '<tr><td colspan="5" class="empty">No repairs logged</td></tr>';

  // An offer, not an automatic jump: opening a vehicle used to silently
  // carry no filter over to Invoices at all — filterByVehicle already
  // existed to do it, it just had nothing wired up to call it. Landing on
  // this page and asking "did you want to work with just this car's
  // invoices?" as a toast that fades in a few seconds if ignored gets the
  // shortcut without ever deciding that for someone who just wanted to
  // glance at the vehicle's numbers.
  if (v.invoices > 0) {
    actionToast(`Show only ${v.registration}'s invoices?`, 'Show invoices',
      () => filterByVehicle(v.registration));
  }
}

function serviceTypeLabel(r) {
  if (r.service_type === 'other') return r.service_type_other || 'Other';
  return r.service_type === 'full' ? 'Full service' : 'Mini service';
}

// Every spec field is optional and only ever arrives once a repair visit
// has supplied it — the whole panel stays hidden until at least one exists,
// rather than showing a vehicle's page full of empty "—" rows by default.
function renderVehicleSpec(v, lastTimingBelt, hasTimingBelt) {
  const rows = [
    ['VIN / chassis number', v.vin],
    ['Colour', v.colour],
    ['Cylinder capacity', v.cylinder_capacity],
    ['Fuel type', v.fuel_type],
    ['Engine size', v.engine_size],
    ['Tyre size', v.tyre_size],
    ['Radio code', v.radio_code],
    ['Spare keys', v.spare_keys],
    ['Last timing belt change', hasTimingBelt ? lastTimingBelt : ''],
  ].filter(([, val]) => val);

  $('veh-spec-section').hidden = rows.length === 0;
  $('veh-spec').innerHTML = rows.map(([k, val]) =>
    `<div><span class="muted">${esc(k)}</span> ${esc(val)}</div>`).join('');
}

// ── part detail ───────────────────────────────────────────────────────────

async function loadPartDetail() {
  const part = state.currentPart;
  if (!part) { show('parts'); return; }

  // Same reasoning as loadVehicleDetail: keyed on the part number itself, so
  // clicking between two parts quickly can never let the slower one's data
  // land on a page that has since moved on to a different part.
  const seq = beginLoad('part:' + part);
  const d = await api('/api/part/' + encodeURIComponent(part));
  if (stale('part:' + part, seq) || state.currentPart !== part) return;
  $('part-title').textContent = d.part_number;
  $('part-sub').textContent = d.description || '';

  // A rising unit price across repeat purchases is the thing worth catching,
  // so the change is called out rather than left to be eyeballed.
  const trend = d.price_change_pct;
  const trendTile = Math.abs(trend) < 0.5
    ? { k: 'Price trend', v: 'flat' }
    : {
        k: 'Price trend',
        v: (trend > 0 ? '+' : '') + trend.toFixed(1) + '%',
        m: trend > 0 ? 'more expensive than first purchase' : 'cheaper than first purchase',
        alert: trend > 10,
      };

  $('part-tiles').innerHTML = [
    { k: 'Total (incl. VAT)', v: '£' + money(d.brutto) },
    { k: 'Total net', v: '£' + money(d.netto) },
    { k: 'VAT', v: '£' + money(d.vat) },
    { k: 'Purchases', v: int(d.times) },
    { k: 'Quantity', v: int(d.quantity) },
    { k: 'Avg unit', v: '£' + money(d.avg_unit_price) },
    { k: 'Cheapest', v: '£' + money(d.min_unit_price) },
    { k: 'Dearest', v: '£' + money(d.max_unit_price) },
    trendTile,
  ].map((t) => `
    <div class="tile${t.alert ? ' alert' : ''}"><div class="k">${esc(t.k)}</div>
    <div class="v">${esc(t.v)}</div>
    ${t.m ? `<div class="m">${esc(t.m)}</div>` : ''}</div>`).join('');

  // Price history is the clearest case for a line: the shape between points
  // is the whole point, and clicking one opens the invoice behind that price.
  drawChart('part-bars', (d.history || []).map((h) => ({
    label: (h.date || '').slice(5),
    value: h.unit_price,
    title: `${h.date} — £${money(h.unit_price)} from ${h.supplier}`,
    id: h.invoice_id,
  })), { onPick: (pt) => pt.id && openInvoice(pt.id) });

  $('part-history').innerHTML = (d.history || []).length
    ? d.history.slice().reverse().map((h) => `
        <tr class="clickable" data-id="${h.invoice_id}">
          <td class="mono">${dash(h.date)}</td>
          <td class="truncate">${dash(h.supplier)}</td>
          <td>${h.vehicle ? `<span class="reg">${esc(h.vehicle)}</span>` : '<span class="muted">—</span>'}</td>
          <td class="num">${int(h.quantity)}</td>
          <td class="num strong">${money(h.unit_price)}</td>
          <td class="num">${money(h.netto)}</td>
          <td class="num">${money(h.brutto)}</td></tr>`).join('')
    : '<tr><td colspan="7" class="empty">No purchases</td></tr>';
  $('part-history').querySelectorAll('tr[data-id]').forEach((tr) =>
    tr.addEventListener('click', () => openInvoice(tr.dataset.id)));
}

/** Adding a vehicle by hand, for cars that never came from a dispatch export
    — a new car, a courtesy vehicle, or one the export missed. Callsign is
    stored the same way the importer stores it, so the two agree. */
async function addVehicleToFleet() {
  const reg = $('nv-reg').value.trim();
  const status = $('nv-status');

  if (!reg) {
    status.innerHTML = '<span class="pill flag">Registration required</span>';
    $('nv-reg').focus();
    return;
  }

  const callsign = $('nv-callsign').value.trim();
  const body = {
    make: $('nv-make').value.trim(),
    model: $('nv-model').value.trim(),
    driver: $('nv-driver').value.trim(),
    notes: callsign ? 'Callsign ' + callsign : '',
    company_id: Number($('nv-company').value) || 0,
    active: true,
  };

  const btn = $('nv-add');
  btn.disabled = true;
  status.textContent = 'Adding…';
  try {
    await api('/api/registry/' + encodeURIComponent(reg), { method: 'PUT', json: body });
    status.innerHTML = `<span class="pill">Added</span> <span style="margin-left:8px">${esc(reg.toUpperCase())}</span>`;
    // Clear the identity fields but keep company selected: adding several
    // vehicles to the same firm in a row is the normal case.
    for (const id of ['nv-reg', 'nv-callsign', 'nv-make', 'nv-model', 'nv-driver']) $(id).value = '';
    $('nv-reg').focus();
    loadFleet();
  } catch (e) {
    status.innerHTML = `<span class="pill flag">Failed</span> <span style="margin-left:8px">${esc(e.message)}</span>`;
  }
  btn.disabled = false;
}

$('nv-add').addEventListener('click', addVehicleToFleet);
// Enter anywhere in the form submits, so a plate can be typed and added
// without reaching for the mouse.
for (const id of ['nv-reg', 'nv-callsign', 'nv-make', 'nv-model', 'nv-driver']) {
  $(id).addEventListener('keydown', (e) => { if (e.key === 'Enter') addVehicleToFleet(); });
}

// ── fleet registry ────────────────────────────────────────────────────────

async function loadFleet() {
  const seq = beginLoad('fleet');
  companies = await api('/api/companies');
  if (stale('fleet', seq)) return;

  // Keep the current choice across a reload so adding several vehicles to one
  // company does not mean re-picking it every time.
  const picked = $('nv-company').value;
  $('nv-company').innerHTML = companies.map((c) =>
    `<option value="${c.id}"${c.is_default ? ' data-default="1"' : ''}>${esc(c.name)}</option>`).join('');
  if (picked) $('nv-company').value = picked;

  $('company-tiles').innerHTML = companies.map((c) => `
    <div class="tile">
      <div class="k">${esc(c.name)}${c.is_default ? ' · default' : ''}</div>
      <div class="v">£${money(c.brutto)}</div>
      <div class="m">${int(c.vehicles)} vehicle(s) · ${int(c.invoices)} invoice(s)</div>
    </div>`).join('');

  const unassigned = await api('/api/registry/unassigned');
  if (stale('fleet', seq)) return;
  $('unassigned-rows').innerHTML = unassigned.length
    ? unassigned.map((u) => `
        <tr>
          <td><span class="reg">${esc(u.vehicle_reg)}</span></td>
          <td class="num">${int(u.invoices)}</td>
          <td class="num strong">${money(u.brutto)}</td>
          <td class="mono">${dash(u.last_date)}</td>
          <td><button class="btn sm assign" data-reg="${esc(u.vehicle_reg)}">Add to fleet</button></td>
        </tr>`).join('')
    : '<tr><td colspan="5" class="empty">Every plate seen on an invoice is registered</td></tr>';

  $('unassigned-rows').querySelectorAll('button.assign').forEach((b) =>
    b.addEventListener('click', () => assignVehicle(b.dataset.reg)));

  registryData = await api('/api/registry');
  if (stale('fleet', seq)) return;
  renderRegistryTable();
}

// ── registry sorting ──────────────────────────────────────────────────────
// Client-side: the whole registry is fetched in one call already, so
// reordering it is free and instant rather than a round trip. Kept entirely
// separate from the Invoices table's th.sortable handling in app.js — same
// look, unrelated state, so a click here never touches the invoices list.

let registryData = [];
let registrySort = '';
let registryDir = 'asc';

const REGISTRY_COMPARE = {
  make: (v) => (v.make || '').toLowerCase(),
  model: (v) => (v.model || '').toLowerCase(),
  driver: (v) => (v.driver || '').toLowerCase(),
};

function sortedRegistry() {
  const key = REGISTRY_COMPARE[registrySort];
  if (!key) return registryData; // default: whatever order the API sent

  // Vehicles with nothing in the column sort to the end regardless of
  // direction — an empty driver is not "before A" or "after Z", it is just
  // not worth sorting on. Sorting the two halves separately, rather than
  // sorting everything and reversing for "desc", is what keeps that true in
  // both directions: reversing a fully-sorted array would put the empties
  // that landed last in ascending order first in descending order instead.
  const withValue = [];
  const empty = [];
  for (const v of registryData) (key(v) ? withValue : empty).push(v);

  withValue.sort((a, b) => {
    const av = key(a), bv = key(b);
    const cmp = av < bv ? -1 : av > bv ? 1 : 0;
    return registryDir === 'desc' ? -cmp : cmp;
  });
  return [...withValue, ...empty];
}

function renderRegistryTable() {
  const registry = sortedRegistry();
  $('registry-rows').innerHTML = registry.length
    ? registry.map((v) => `
        <tr>
          <td class="clickable-reg">
            <span class="veh">
              ${carIcon(v.make, v.model)}
              <span class="veh-text">
                <span class="veh-reg">${esc(v.registration)}</span>
                ${v.notes ? `<span class="veh-sub">${esc(v.notes)}</span>` : ''}
              </span>
            </span>
          </td>
          <td>
            <select class="company-pick" data-reg="${esc(v.registration)}">
              ${companies.map((c) => `<option value="${c.id}"${c.id === v.company_id ? ' selected' : ''}>${esc(c.name)}</option>`).join('')}
            </select>
          </td>
          <td>${dash(v.make)}</td>
          <td>${dash(v.model)}</td>
          <td>${dash(v.driver)}</td>
          <td class="num strong">${money(v.brutto)}</td>
          <td>${v.active ? '<span class="pill">Active</span>' : '<span class="pill flag">Off fleet</span>'}</td>
          <td style="display:flex;gap:6px">
            <button class="btn sm edit" data-reg="${esc(v.registration)}">Edit</button>
            <button class="btn sm view" data-reg="${esc(v.registration)}">Stats</button>
          </td>
        </tr>`).join('')
    : '<tr><td colspan="8" class="empty"><strong>No vehicles registered</strong>Add one from the list above.</td></tr>';

  $('registry-rows').querySelectorAll('select.company-pick').forEach((sel) =>
    sel.addEventListener('change', async () => {
      try {
        await api('/api/registry/' + encodeURIComponent(sel.dataset.reg),
          { method: 'PUT', json: { company_id: Number(sel.value) } });
        toast('Reassigned');
        loadFleet();
      } catch (e) { toast(e.message, true); }
    }));
  $('registry-rows').querySelectorAll('button.edit').forEach((b) =>
    b.addEventListener('click', () => assignVehicle(b.dataset.reg)));
  $('registry-rows').querySelectorAll('button.view').forEach((b) =>
    b.addEventListener('click', () => openVehicle(b.dataset.reg)));

  document.querySelectorAll('#view-fleet th.sortable').forEach((th) => {
    const on = th.dataset.sort === registrySort;
    th.classList.toggle('sorted', on);
    th.querySelector('.arrow').textContent = on && registryDir === 'desc' ? '↓' : '↑';
  });
}

document.querySelectorAll('#view-fleet th.sortable').forEach((th) =>
  th.addEventListener('click', () => {
    const col = th.dataset.sort;
    if (registrySort === col) {
      registryDir = registryDir === 'asc' ? 'desc' : 'asc';
    } else {
      registrySort = col;
      registryDir = 'asc';
    }
    renderRegistryTable();
  }));

/** Vehicle editor. Reuses the drawer rather than introducing a second
    modal pattern, so editing feels the same everywhere. */
async function assignVehicle(reg) {
  let v = { registration: reg, make: '', model: '', year: '', driver: '', notes: '', active: true, company_id: null };
  try {
    v = await api('/api/vehicle/' + encodeURIComponent(reg)).then((d) => d.vehicle);
  } catch { /* not registered yet — the blank defaults above are correct */ }

  state.current = null;
  state.editingVehicle = reg;
  $('d-title').textContent = 'Vehicle ' + reg;
  $('d-body').innerHTML = `
    <div class="grid2">
      <div class="field"><label for="v-company">Company</label>
        <select id="v-company">
          ${companies.map((c) => `<option value="${c.id}"${c.id === v.company_id ? ' selected' : ''}>${esc(c.name)}</option>`).join('')}
        </select></div>
      <div class="field"><label for="v-make">Make</label>
        <input type="text" id="v-make" value="${esc(v.make)}"></div>
      <div class="field"><label for="v-model">Model</label>
        <input type="text" id="v-model" value="${esc(v.model)}"></div>
      <div class="field"><label for="v-year">Year</label>
        <input type="text" id="v-year" value="${esc(v.year)}"></div>
      <div class="field"><label for="v-driver">Driver</label>
        <input type="text" id="v-driver" value="${esc(v.driver)}"></div>
      <div class="field"><label for="v-active">On fleet</label>
        <select id="v-active">
          <option value="1"${v.active ? ' selected' : ''}>Active</option>
          <option value="0"${v.active ? '' : ' selected'}>Off fleet</option>
        </select></div>
    </div>
    <div class="field"><label for="v-notes">Notes</label>
      <input type="text" id="v-notes" value="${esc(v.notes)}"></div>`;

  showDrawerFooter('vehicle');
  $('drawer').classList.add('open');
  $('scrim').classList.add('open');
}

$('v-save').addEventListener('click', async () => {
  const reg = state.editingVehicle;
  if (!reg) return;
  try {
    await api('/api/registry/' + encodeURIComponent(reg), {
      method: 'PUT',
      json: {
        company_id: Number($('v-company').value),
        make: $('v-make').value,
        model: $('v-model').value,
        year: $('v-year').value,
        driver: $('v-driver').value,
        notes: $('v-notes').value,
        active: $('v-active').value === '1',
      },
    });
    toast('Vehicle saved');
    closeDrawer();
    loadFleet();
  } catch (e) { toast(e.message, true); }
});

$('v-remove').addEventListener('click', async () => {
  const reg = state.editingVehicle;
  if (!reg || !confirm(`Remove ${reg} from the registry?\n\nIts invoices are kept.`)) return;
  try {
    await api('/api/registry/' + encodeURIComponent(reg), { method: 'DELETE' });
    toast('Removed from registry');
    closeDrawer();
    loadFleet();
  } catch (e) { toast(e.message, true); }
});

$('add-company').addEventListener('click', async () => {
  const name = $('new-company').value.trim();
  if (!name) return;
  try {
    await api('/api/companies', { method: 'POST', json: { name } });
    $('new-company').value = '';
    toast('Company added');
    loadFleet();
  } catch (e) { toast(e.message, true); }
});

Object.assign(viewLoaders, {
  vehicle: loadVehicleDetail,
  part: loadPartDetail,
  fleet: loadFleet,
});
