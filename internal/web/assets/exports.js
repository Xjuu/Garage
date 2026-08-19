/* Spreadsheets: one dialog that asks format and period, and a popup listing
   what has already been generated. Both are modals rather than a tab — making
   a spreadsheet is an action taken from wherever you are, not a place to go. */

'use strict';

let previewData = null;
const gen = { format: 'xlsx', window: '30d' };

const FORMAT_HINT = {
  xlsx: 'Four sheets — invoices, parts, a monthly summary, and charts. Sorted by registration, then by price.',
  csv: 'One row per part, openable anywhere. Same order as the workbook, but no charts or summary.',
};

function humanSize(bytes) {
  if (!bytes) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB'];
  let i = 0;
  let v = Number(bytes);
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

/** Timestamps are stored as RFC3339; show them in the reader's own timezone
    rather than pretending everything happened in UTC. */
function whenLocal(iso) {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso || '—';
  return d.toLocaleString(undefined, {
    year: 'numeric', month: 'short', day: '2-digit',
    hour: '2-digit', minute: '2-digit',
  });
}

// ── modal plumbing ────────────────────────────────────────────────────────

function openModal(name) {
  $(name + '-scrim').hidden = false;
  $(name + '-modal').hidden = false;
}

function closeModal(name) {
  $(name + '-scrim').hidden = true;
  $(name + '-modal').hidden = true;
}

// Escape closes the topmost dialog. Files sits above Generate when both are
// open, so it is checked first.
document.addEventListener('keydown', (e) => {
  if (e.key !== 'Escape') return;
  if (!$('gen-modal').hidden) { closeModal('gen'); return; }
  if (!$('files-modal').hidden) closeModal('files');
});

// ── generate ──────────────────────────────────────────────────────────────

function openGenerate() {
  $('gen-status').innerHTML = '';
  syncGenChips();
  openModal('gen');
}

function syncGenChips() {
  $('gen-format').querySelectorAll('.chip').forEach((b) =>
    b.setAttribute('aria-pressed', String(b.dataset.format === gen.format)));
  $('gen-period').querySelectorAll('.chip').forEach((b) =>
    b.setAttribute('aria-pressed', String(b.dataset.window === gen.window)));
  $('gen-custom').hidden = gen.window !== 'custom';
  $('gen-format-hint').textContent = FORMAT_HINT[gen.format];
}

$('gen-format').querySelectorAll('.chip').forEach((b) =>
  b.addEventListener('click', () => { gen.format = b.dataset.format; syncGenChips(); }));
$('gen-period').querySelectorAll('.chip').forEach((b) =>
  b.addEventListener('click', () => { gen.window = b.dataset.window; syncGenChips(); }));

$('gen-go').addEventListener('click', async () => {
  const from = $('gen-from').value;
  const to = $('gen-to').value;
  if (gen.window === 'custom' && (!from || !to)) {
    $('gen-status').innerHTML = '<span class="pill flag">Dates</span> <span style="margin-left:8px">Pick both a start and an end date.</span>';
    return;
  }

  const btn = $('gen-go');
  btn.disabled = true;
  btn.textContent = 'Generating…';
  $('gen-status').textContent = '';
  try {
    const r = await api('/api/exports/generate', {
      method: 'POST',
      json: { window: gen.window, from, to, format: gen.format },
    });
    toast(`${r.name} — ${int(r.invoices)} invoice(s), ${int(r.items)} line(s)`);
    closeModal('gen');
    await openFiles();
    openPreview(r.name);
  } catch (e) {
    $('gen-status').innerHTML =
      `<span class="pill flag">Failed</span> <span style="margin-left:8px">${esc(e.message)}</span>`;
  }
  btn.disabled = false;
  btn.textContent = 'Generate';
});

$('gen-close').addEventListener('click', () => closeModal('gen'));
$('gen-cancel').addEventListener('click', () => closeModal('gen'));
$('gen-scrim').addEventListener('click', () => closeModal('gen'));

// ── saved files ───────────────────────────────────────────────────────────

async function openFiles() {
  openModal('files');
  await loadFiles();
}

async function loadFiles() {
  const d = await api('/api/exports');
  const files = d.files || [];

  $('c-exports').textContent = int(files.length);
  const c = d.cache || {};
  $('files-sub').textContent =
    `${d.folder} · ${int(c.hits)} listing(s) served from cache`;

  $('files-rows').innerHTML = files.length
    ? files.map((f) => {
        const dot = f.name.lastIndexOf('.');
        const base = dot > 0 ? f.name.slice(0, dot) : f.name;
        const ext = dot > 0 ? f.name.slice(dot) : '';
        return `
        <tr data-name="${esc(f.name)}">
          <td class="file-name">${esc(base)}<span class="ext">${esc(ext)}</span></td>
          <td>${f.stale ? '<span class="pill">unknown</span>' : esc(f.label || 'everything')}</td>
          <td class="num">${f.stale ? '—' : int(f.items)}</td>
          <td class="num">${f.brutto ? '£' + money(f.brutto) : '—'}</td>
          <td class="num">${esc(humanSize(f.bytes))}</td>
          <td class="mono">${esc(whenLocal(f.created))}</td>
          <td style="white-space:nowrap">
            <button class="btn sm act-preview">Preview</button>
            <button class="btn sm act-download">Download</button>
            <button class="btn sm danger act-delete">Delete</button>
          </td>
        </tr>`;
      }).join('')
    : `<tr><td colspan="7" class="empty">
         <strong>Nothing generated yet</strong>
         Use "Generate a new one" below to make your first spreadsheet.
       </td></tr>`;

  $('files-rows').querySelectorAll('tr[data-name]').forEach((tr) => {
    const name = tr.dataset.name;
    tr.querySelector('.act-preview')?.addEventListener('click', () => openPreview(name));
    tr.querySelector('.act-download')?.addEventListener('click', () => downloadExport(name));
    tr.querySelector('.act-delete')?.addEventListener('click', () => deleteExport(name));
  });
}

function downloadExport(name) {
  location.href = '/api/exports/file?name=' + encodeURIComponent(name);
}

async function deleteExport(name) {
  if (!confirm(`Delete ${name}? The file is removed from disk.`)) return;
  try {
    await api('/api/exports/file?name=' + encodeURIComponent(name), { method: 'DELETE' });
    if (previewData && previewData.name === name) closePreview();
    toast('Deleted ' + name);
    loadFiles();
  } catch (e) { toast(e.message, true); }
}

$('files-close').addEventListener('click', () => closeModal('files'));
$('files-done').addEventListener('click', () => closeModal('files'));
$('files-scrim').addEventListener('click', () => closeModal('files'));
// Only one dialog at a time: stacking two scrims double-darkens the page and
// leaves it ambiguous which one Escape closes.
$('files-generate').addEventListener('click', () => {
  closeModal('files');
  openGenerate();
});

/** The five most recent files, shown on the Overview so the last export is
    visible without opening anything. Defined here because this file owns the
    export endpoints; app.js calls it after the overview loads. */
async function loadRecentFiles() {
  let files = [];
  try {
    const d = await api('/api/exports');
    files = d.files || [];
    $('c-exports').textContent = int(files.length);
  } catch {
    $('ov-files').innerHTML =
      '<tr><td colspan="6" class="empty">Could not read the exports folder</td></tr>';
    return;
  }

  $('ov-files').innerHTML = files.length
    ? files.slice(0, 5).map((f) => {
        const dot = f.name.lastIndexOf('.');
        const base = dot > 0 ? f.name.slice(0, dot) : f.name;
        const ext = dot > 0 ? f.name.slice(dot) : '';
        return `
        <tr data-name="${esc(f.name)}">
          <td class="file-name">${esc(base)}<span class="ext">${esc(ext)}</span></td>
          <td>${f.stale ? '<span class="pill">unknown</span>' : esc(f.label || 'everything')}</td>
          <td class="num">${f.stale ? '—' : int(f.items)}</td>
          <td class="num">${esc(humanSize(f.bytes))}</td>
          <td class="mono">${esc(whenLocal(f.created))}</td>
          <td style="white-space:nowrap">
            <button class="btn sm ov-preview">Preview</button>
            <button class="btn sm ov-download">Download</button>
          </td>
        </tr>`;
      }).join('')
    : `<tr><td colspan="6" class="empty">
         <strong>No spreadsheets yet</strong>
         Press Generate above to make one.
       </td></tr>`;

  $('ov-files').querySelectorAll('tr[data-name]').forEach((tr) => {
    const name = tr.dataset.name;
    // Preview from the Overview opens the same popup, so there is one place
    // where a file is inspected rather than two that can drift apart.
    tr.querySelector('.ov-preview')?.addEventListener('click', async () => {
      await openFiles();
      openPreview(name);
    });
    tr.querySelector('.ov-download')?.addEventListener('click', () => downloadExport(name));
  });
}

$('ov-generate').addEventListener('click', openGenerate);
$('ov-all-files').addEventListener('click', openFiles);

// ── preview ───────────────────────────────────────────────────────────────

async function openPreview(name) {
  try {
    previewData = await api('/api/exports/preview?name=' + encodeURIComponent(name));
  } catch (e) {
    toast(e.message, true);
    return;
  }
  $('prev-name').textContent = previewData.name;
  $('files-preview').hidden = false;

  $('prev-tabs').innerHTML = previewData.sheets.map((s, i) =>
    `<button data-sheet="${i}" aria-selected="${i === 0}">${esc(s.name)}</button>`).join('');
  $('prev-tabs').querySelectorAll('button').forEach((b) =>
    b.addEventListener('click', () => showSheet(Number(b.dataset.sheet))));

  showSheet(0);
  $('files-preview').scrollIntoView({ behavior: 'smooth', block: 'nearest' });
}

/** Column letters, as a spreadsheet labels them: A..Z, then AA, AB, ... */
function colName(n) {
  let out = '';
  for (n += 1; n > 0; n = Math.floor((n - 1) / 26)) {
    out = String.fromCharCode(65 + ((n - 1) % 26)) + out;
  }
  return out;
}

// Columns holding money are right-aligned, as Excel would show them. Detected
// from the header text rather than by sniffing values, so an empty cell in a
// numeric column still aligns with the rest.
const NUMERIC_HEADER = /net|vat|brutto|gross|price|qty|quantity|total|rows|amount|lines/i;

function showSheet(index) {
  const sheet = previewData.sheets[index];
  if (!sheet) return;

  $('prev-tabs').querySelectorAll('button').forEach((b) =>
    b.setAttribute('aria-selected', String(Number(b.dataset.sheet) === index)));

  const cols = Math.max(sheet.header.length,
    ...sheet.rows.map((r) => r.length), 1);
  const numeric = [];
  for (let c = 0; c < cols; c++) numeric[c] = NUMERIC_HEADER.test(sheet.header[c] || '');

  // Column-letter strip across the top, exactly like a spreadsheet.
  $('prev-head').innerHTML =
    `<tr class="xl-cols">
       <th class="xl-corner"></th>
       ${Array.from({ length: cols }, (_, c) => `<th>${colName(c)}</th>`).join('')}
     </tr>`;

  // Row 1 is the file's own header, shown as a normal — if emphasised — row,
  // because that is where it really sits in the file.
  const headerRow = `
    <tr class="xl-headrow">
      <th class="xl-rownum">1</th>
      ${Array.from({ length: cols }, (_, c) =>
        `<td class="xl-cell${numeric[c] ? ' num' : ''}">${esc(sheet.header[c] || '')}</td>`).join('')}
    </tr>`;

  const bodyRows = sheet.rows.map((r, i) => `
    <tr>
      <th class="xl-rownum">${i + 2}</th>
      ${Array.from({ length: cols }, (_, c) =>
        `<td class="xl-cell${numeric[c] ? ' num' : ''}" title="${esc(r[c] || '')}">${esc(r[c] || '')}</td>`).join('')}
    </tr>`).join('');

  $('prev-body').innerHTML = headerRow + (bodyRows ||
    `<tr><th class="xl-rownum">2</th><td class="xl-cell empty" colspan="${cols}">This sheet is empty</td></tr>`);

  $('prev-note').textContent = sheet.truncated
    ? `Showing rows 1–${sheet.rows.length + 1} of ${sheet.total_rows + 1} — download the file for the rest.`
    : `${sheet.total_rows + 1} row(s) including the header.`;
}

function closePreview() {
  $('files-preview').hidden = true;
  previewData = null;
}

$('prev-close').addEventListener('click', closePreview);
$('prev-download').addEventListener('click', () => {
  if (previewData) downloadExport(previewData.name);
});
