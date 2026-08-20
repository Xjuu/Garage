/* Training: reference invoices, the correct values for each, supplier hints,
   and the accuracy check. This is what teaches the extractor. */

'use strict';

let examples = [];

async function loadTraining() {
  const seq = beginLoad('training');
  const data = await api('/api/examples');
  if (stale('training', seq)) return;
  examples = data.examples || [];
  $('examples-folder').textContent = data.folder;

  const pending = examples.filter((e) => e.status !== 'ready').length;
  $('c-training').textContent = int(examples.length);

  $('example-rows').innerHTML = examples.length
    ? examples.map((e) => `
        <tr class="clickable" data-id="${e.id}">
          <td class="mono truncate">${esc(e.filename)}</td>
          <td>${dash(e.supplier)}</td>
          <td>${e.status === 'ready'
            ? '<span class="pill">Ready</span>'
            : '<span class="pill flag">Needs values</span>'}</td>
          <td class="mono">${esc((e.updated_at || '').slice(0, 10))}</td>
          <td><button class="btn sm">${e.status === 'ready' ? 'Edit' : 'Enter values'}</button></td>
        </tr>`).join('')
    : `<tr><td colspan="5" class="empty">
         <strong>No examples yet</strong>
         Drop a few real invoices above, then enter what each one should produce.
       </td></tr>`;

  $('example-rows').querySelectorAll('tr[data-id]').forEach((tr) =>
    tr.addEventListener('click', () => openExample(Number(tr.dataset.id))));

  if (pending > 0) {
    $('c-training').textContent = `${examples.length - pending}/${examples.length}`;
  }

  const hints = await api('/api/hints');
  $('hint-rows').innerHTML = hints.length
    ? hints.map((h) => `
        <tr>
          <td class="strong">${esc(h.supplier)}</td>
          <td>${esc(h.hint)}</td>
          <td><button class="btn sm danger del-hint" data-id="${h.id}">Delete</button></td>
        </tr>`).join('')
    : '<tr><td colspan="3" class="empty">No hints yet</td></tr>';
  $('hint-rows').querySelectorAll('button.del-hint').forEach((b) =>
    b.addEventListener('click', async () => {
      try {
        await api('/api/hints/' + b.dataset.id, { method: 'DELETE' });
        loadTraining();
      } catch (e) { toast(e.message, true); }
    }));

  const runs = await api('/api/evals');
  $('eval-rows').innerHTML = runs.length
    ? runs.map((r) => `
        <tr>
          <td class="mono">${esc((r.started_at || '').replace('T', ' ').slice(0, 16))}</td>
          <td class="mono">${esc(r.model)}</td>
          <td class="num">${int(r.examples)}</td>
          <td class="num">${int(r.fields_ok)}/${int(r.fields_all)}</td>
          <td class="num strong">${r.accuracy ? r.accuracy.toFixed(1) + '%' : '—'}</td>
        </tr>`).join('')
    : '<tr><td colspan="5" class="empty">No accuracy checks run yet</td></tr>';
}

/** The ground-truth form. Every field is captured twice: the correct value,
    and *where on the page it was found*. The location notes are what turn a
    one-off correction into lasting guidance — they become layout hints for
    that supplier, so the next invoice from them is read correctly first time. */
function truthForm(t) {
  const items = t.items || [];
  const sec = t.sections || {};

  const row = (key, label, value, placeholder, type = 'text') => `
    <div class="train-row">
      <div class="field">
        <label for="t-${key}">${esc(label)}</label>
        <input type="${type}" id="t-${key}" value="${esc(value ?? '')}"
               ${type === 'number' ? 'step="0.01"' : ''}>
      </div>
      <div class="field">
        <label for="s-${key}">Found under which section?</label>
        <input type="text" id="s-${key}" value="${esc(sec[key] || '')}"
               placeholder="${esc(placeholder)}">
      </div>
    </div>`;

  return `
    <div class="note" style="background:var(--g05);border-color:var(--g2);color:inherit">
      Enter the correct values, and say <strong>where on the invoice</strong> each one
      appears. The locations are saved as guidance for this supplier, so future
      invoices from them are read the same way. Leave a value blank to exclude
      it from scoring.
    </div>

    <div class="section-title">Who it is from</div>
    ${row('supplier', 'Company bought from', t.supplier, 'e.g. letterhead, top left')}
    ${row('invoice_number', 'Invoice number', t.invoice_number, "e.g. header box, labelled 'Invoice No'")}
    ${row('invoice_date', 'Date of purchase', t.invoice_date, "e.g. under the invoice number", 'date')}

    <div class="section-title">Vehicle</div>
    ${row('vehicle_reg', 'Registration', t.vehicle_reg, "e.g. 'Vehicle Reg' line under Account")}

    <div class="section-title">Money</div>
    ${row('netto', 'Price excluding VAT', numOrBlank(t.netto), 'e.g. totals box, bottom right', 'number')}
    ${row('vat_amount', 'VAT on that price', numOrBlank(t.vat_amount), "e.g. line marked 'VAT @ 20%'", 'number')}
    ${row('brutto', 'Total including VAT', numOrBlank(t.brutto), "e.g. 'TOTAL DUE'", 'number')}
    <div class="train-row">
      <div class="field">
        <label for="t-vat_rate">VAT rate %</label>
        <input type="number" step="0.01" id="t-vat_rate" value="${numOrBlank(t.vat_rate)}">
      </div>
      <div class="field">
        <label for="t-currency">Currency</label>
        <input type="text" id="t-currency" value="${esc(t.currency || 'GBP')}">
      </div>
    </div>

    <div class="section-title">Parts
      <button class="btn sm" id="t-add-row" style="margin-left:10px">Add line</button>
    </div>
    <div class="field" style="margin-bottom:12px">
      <label for="s-items">Where is the parts table?</label>
      <input type="text" id="s-items" value="${esc(sec.items || '')}"
             placeholder="e.g. middle of page, columns Part Number / Description / Qty / Unit / Net">
    </div>
    <div class="panel"><div class="table-scroll"><table>
      <thead><tr><th>Part number</th><th>Description</th>
        <th class="num">Qty</th><th class="num">Unit price</th>
        <th class="num">VAT</th><th class="num">Net</th><th></th></tr></thead>
      <tbody id="t-items">${items.map(itemRow).join('')}</tbody>
    </table></div></div>`;
}

function numOrBlank(v) {
  return v === undefined || v === null || v === '' ? '' : esc(String(v));
}

function itemRow(it = {}) {
  return `
    <tr>
      <td><input type="text" class="i-part" value="${esc(it.part_number || '')}"></td>
      <td><input type="text" class="i-desc" value="${esc(it.description || '')}"></td>
      <td><input type="number" step="0.01" class="i-qty" value="${numOrBlank(it.quantity)}"></td>
      <td><input type="number" step="0.01" class="i-unit" value="${numOrBlank(it.unit_price)}"></td>
      <td><input type="number" step="0.01" class="i-vat" value="${numOrBlank(it.vat_amount)}"></td>
      <td><input type="number" step="0.01" class="i-net" value="${numOrBlank(it.netto)}"></td>
      <td><button class="btn sm danger i-del">✕</button></td>
    </tr>`;
}

function wireItemRows() {
  $('t-items').querySelectorAll('button.i-del').forEach((b) =>
    b.addEventListener('click', () => b.closest('tr').remove()));
}

async function openExample(id) {
  const e = examples.find((x) => x.id === id);
  if (!e) return;

  state.editingExample = id;
  state.current = null;
  $('d-title').textContent = e.filename;

  let truth = {};
  if (e.truth_json) {
    try { truth = JSON.parse(e.truth_json); } catch { truth = {}; }
  }
  $('d-body').innerHTML = truthForm(truth);
  wireItemRows();
  $('t-add-row').addEventListener('click', () => {
    $('t-items').insertAdjacentHTML('beforeend', itemRow());
    wireItemRows();
  });

  showDrawerFooter('example');
  $('drawer').classList.add('open');
  $('scrim').classList.add('open');
}

/** Collect the form into the same shape the extractor emits, plus a `sections`
    map of where each field lives. Blank values are omitted so they are not
    scored; `sections` is never scored, only used as guidance. */
function collectTruth() {
  const t = {};
  const sections = {};

  const sectionOf = (key) => {
    const el = $('s-' + key);
    const v = el ? el.value.trim() : '';
    if (v) sections[key] = v;
  };

  for (const key of ['supplier', 'invoice_number', 'invoice_date', 'vehicle_reg', 'currency']) {
    const v = $('t-' + key).value.trim();
    if (v) t[key] = v;
    sectionOf(key);
  }
  for (const key of ['netto', 'vat_amount', 'vat_rate', 'brutto']) {
    const v = $('t-' + key).value.trim();
    if (v !== '') t[key] = Number(v);
    sectionOf(key);
  }
  sectionOf('items');

  const items = [];
  $('t-items').querySelectorAll('tr').forEach((tr) => {
    const part = tr.querySelector('.i-part').value.trim();
    const desc = tr.querySelector('.i-desc').value.trim();
    if (!part && !desc) return;
    const item = {};
    if (part) item.part_number = part;
    if (desc) item.description = desc;
    for (const [cls, key] of [['.i-qty', 'quantity'], ['.i-unit', 'unit_price'],
                              ['.i-vat', 'vat_amount'], ['.i-net', 'netto']]) {
      const v = tr.querySelector(cls).value.trim();
      if (v !== '') item[key] = Number(v);
    }
    items.push(item);
  });
  if (items.length) t.items = items;
  if (Object.keys(sections).length) t.sections = sections;
  return t;
}

$('e-save').addEventListener('click', async () => {
  const id = state.editingExample;
  if (!id) return;
  const truth = collectTruth();
  if (!Object.keys(truth).length) {
    toast('Fill in at least one field first', true);
    return;
  }
  try {
    await api('/api/examples/' + id, {
      method: 'PATCH',
      json: { supplier: truth.supplier || '', truth },
    });
    toast('Saved — this example now guides the extractor');
    closeDrawer();
    loadTraining();
  } catch (e) { toast(e.message, true); }
});

// Auto-fill runs one extraction so the operator corrects a populated form
// rather than typing every figure from scratch.
$('e-prefill').addEventListener('click', async () => {
  const id = state.editingExample;
  if (!id) return;
  const btn = $('e-prefill');
  btn.disabled = true;
  btn.textContent = 'Reading…';
  try {
    const res = await api('/api/examples/' + id + '/prefill', { method: 'POST' });
    // Keep whatever locations were already noted; only the values are refreshed.
    const keptSections = collectTruth().sections || {};
    $('d-body').innerHTML = truthForm({
      sections: keptSections,
      supplier: res.supplier,
      invoice_number: res.invoice_number,
      invoice_date: res.invoice_date,
      vehicle_reg: res.vehicle_reg,
      currency: res.currency,
      netto: res.netto,
      vat_amount: res.vat_amount,
      vat_rate: res.vat_rate,
      brutto: res.brutto,
      items: res.items,
    });
    wireItemRows();
    $('t-add-row').addEventListener('click', () => {
      $('t-items').insertAdjacentHTML('beforeend', itemRow());
      wireItemRows();
    });
    toast('Filled in — now correct anything it got wrong');
  } catch (e) {
    toast(e.message, true);
  } finally {
    btn.disabled = false;
    btn.textContent = 'Auto-fill with AI';
  }
});

$('e-doc').addEventListener('click', () => {
  if (state.editingExample) {
    window.open('/api/examples/' + state.editingExample + '/doc', '_blank', 'noopener');
  }
});

$('e-remove').addEventListener('click', async () => {
  const id = state.editingExample;
  if (!id || !confirm('Remove this example from training?\n\nThe file stays in the examples folder.')) return;
  try {
    await api('/api/examples/' + id, { method: 'DELETE' });
    toast('Removed');
    closeDrawer();
    loadTraining();
  } catch (e) { toast(e.message, true); }
});

$('scan-examples').addEventListener('click', async () => {
  try {
    const r = await api('/api/examples/scan', { method: 'POST' });
    toast(`${r.seen} file(s) in the folder, ${r.added} newly registered`);
    loadTraining();
  } catch (e) { toast(e.message, true); }
});

$('run-eval').addEventListener('click', async () => {
  try {
    await api('/api/eval', { method: 'POST' });
    showConsole(true);
    pollJob();
  } catch (e) { toast(e.message, true); }
});

$('add-hint').addEventListener('click', async () => {
  const supplier = $('hint-supplier').value.trim();
  const hint = $('hint-text').value.trim();
  if (!supplier || !hint) {
    toast('Both supplier and hint are needed', true);
    return;
  }
  try {
    await api('/api/hints', { method: 'POST', json: { supplier, hint } });
    $('hint-supplier').value = '';
    $('hint-text').value = '';
    toast('Hint saved');
    loadTraining();
  } catch (e) { toast(e.message, true); }
});

// Dropping an example uploads it into the examples folder, not the invoice
// database — a reference invoice is not a purchase to be counted.
const exampleDrop = $('example-drop');
['dragenter', 'dragover'].forEach((ev) =>
  exampleDrop.addEventListener(ev, (e) => { e.preventDefault(); exampleDrop.classList.add('hot'); }));
['dragleave', 'drop'].forEach((ev) =>
  exampleDrop.addEventListener(ev, (e) => { e.preventDefault(); exampleDrop.classList.remove('hot'); }));

async function uploadExamples(files) {
  if (!files || !files.length) return;
  const fd = new FormData();
  for (const f of files) fd.append('files', f);
  try {
    const r = await api('/api/examples/upload', { method: 'POST', body: fd });
    toast(`${r.saved} file(s) added`);
    loadTraining();
  } catch (e) { toast(e.message, true); }
}

exampleDrop.addEventListener('drop', (e) => uploadExamples(e.dataTransfer.files));
exampleDrop.addEventListener('click', () => {
  const picker = document.createElement('input');
  picker.type = 'file';
  picker.multiple = true;
  picker.accept = '.pdf,.png,.jpg,.jpeg,.webp,.heic';
  picker.addEventListener('change', () => uploadExamples(picker.files));
  picker.click();
});

Object.assign(viewLoaders, { training: loadTraining });
