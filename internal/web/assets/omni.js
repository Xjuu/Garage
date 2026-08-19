/* Global search bar: one query across invoices, vehicles, parts and suppliers,
   with a date range that narrows all of them at once. */

'use strict';

const omni = $('omni');
const omniResults = $('omni-results');

// Single-letter badges keep the row compact while still saying what a hit is.
const KIND_BADGE = { invoice: 'I', vehicle: 'V', part: 'P', supplier: 'S' };
const KIND_LABEL = {
  invoice: 'Invoices', vehicle: 'Vehicles', part: 'Parts', supplier: 'Suppliers',
};

function omniQuery() {
  return {
    q: omni.value.trim(),
    from: $('omni-from').value,
    to: $('omni-to').value,
  };
}

function closeOmni() {
  omniResults.hidden = true;
  omniResults.innerHTML = '';
}

/** A hit row. Amount means gross for invoices/vehicles/suppliers and net for
    parts, so each row is labelled rather than leaving the reader to guess. */
function hitRow(h) {
  const amountLabel = h.kind === 'part' ? 'net' : 'gross';
  const countLabel = h.kind === 'invoice' ? '' : `<small>${int(h.count)}×</small>`;
  return `
    <button class="omni-hit" data-kind="${esc(h.kind)}" data-ref="${esc(h.ref)}">
      <span class="kind">${KIND_BADGE[h.kind] || '?'}</span>
      <span class="body">
        <span class="t">${esc(h.title)}</span>
        <span class="s">${esc(h.subtitle || '')}${h.date ? ' · ' + esc(h.date) : ''}</span>
      </span>
      <span class="amt">£${money(h.brutto)} <small>${amountLabel}</small>${countLabel}</span>
    </button>`;
}

function renderOmni(res) {
  if (!res.total) {
    omniResults.innerHTML = '<div class="omni-empty">Nothing matches that.</div>';
    omniResults.hidden = false;
    return;
  }

  let html = '';
  for (const kind of ['vehicles', 'parts', 'suppliers', 'invoices']) {
    const hits = res[kind] || [];
    if (!hits.length) continue;
    html += `<div class="omni-group-title">${KIND_LABEL[hits[0].kind]}</div>`;
    html += hits.map(hitRow).join('');
  }
  omniResults.innerHTML = html;
  omniResults.hidden = false;

  omniResults.querySelectorAll('.omni-hit').forEach((b) =>
    b.addEventListener('click', () => {
      openHit(b.dataset.kind, b.dataset.ref);
      closeOmni();
    }));

  // The first hit rendered is what Enter opens, so it is marked to show that
  // rather than leaving the shortcut invisible.
  omniResults.querySelector('.omni-hit')?.classList.add('top');
}

/** Send the date range along when jumping to the invoice list, so a search
    scoped to a period stays scoped after navigating. */
function openHit(kind, ref) {
  const { from, to } = omniQuery();

  if (kind === 'vehicle') { openVehicle(ref); return; }
  if (kind === 'part') { openPart(ref); return; }
  if (kind === 'invoice') { openInvoice(ref); return; }

  if (kind === 'supplier') {
    state.filters = { q: '', from, to, supplier: ref, reg: '', review: '' };
    $('f-supplier').value = ref;
    $('f-from').value = from;
    $('f-to').value = to;
    $('f-q').value = '';
    state.page = 1;
    show('invoices');
  }
}

const runOmni = debounce(async () => {
  const { q, from, to } = omniQuery();
  $('omni-clear').hidden = !q;

  if (!q && !from && !to) { closeOmni(); return; }
  try {
    const p = new URLSearchParams();
    if (q) p.set('q', q);
    if (from) p.set('from', from);
    if (to) p.set('to', to);
    renderOmni(await api('/api/search?' + p.toString()));
  } catch (e) {
    toast(e.message, true);
  }
}, 220);

omni.addEventListener('input', runOmni);
omni.addEventListener('focus', () => { if (omniResults.innerHTML) omniResults.hidden = false; });
$('omni-from').addEventListener('change', runOmni);
$('omni-to').addEventListener('change', runOmni);

// Enter opens the top result — the first hit rendered, which is also the one
// marked with .top. Vehicles, parts and suppliers rank above invoices, so
// typing a plate and pressing Enter goes straight to that vehicle rather than
// to a filtered list containing it.
//
// Falling back to the filtered invoice list only when there is no hit to open
// — a date-only search, or a query that matched nothing — keeps that jump as
// a genuine fallback rather than the default action.
omni.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') { closeOmni(); omni.blur(); return; }
  if (e.key !== 'Enter') return;

  const topHit = !omniResults.hidden && omniResults.querySelector('.omni-hit.top');
  if (topHit) {
    openHit(topHit.dataset.kind, topHit.dataset.ref);
    closeOmni();
    return;
  }

  const { q, from, to } = omniQuery();
  state.filters = { q, from, to, supplier: '', reg: '', review: '' };
  $('f-q').value = q;
  $('f-from').value = from;
  $('f-to').value = to;
  ['f-supplier', 'f-reg', 'f-review'].forEach((id) => { $(id).value = ''; });
  state.page = 1;
  closeOmni();
  show('invoices');
});

$('omni-clear').addEventListener('click', () => {
  omni.value = '';
  $('omni-clear').hidden = true;
  closeOmni();
  omni.focus();
});

$('omni-range-clear').addEventListener('click', () => {
  $('omni-from').value = '';
  $('omni-to').value = '';
  runOmni();
});

// Clicking away dismisses the panel, but clicks inside it must survive long
// enough for the hit handler to fire.
document.addEventListener('click', (e) => {
  if (!e.target.closest('.searchbar')) closeOmni();
});

// "/" focuses search from anywhere, unless something else already has focus.
document.addEventListener('keydown', (e) => {
  if (e.key !== '/' || e.metaKey || e.ctrlKey) return;
  const tag = document.activeElement?.tagName;
  if (tag === 'INPUT' || tag === 'SELECT' || tag === 'TEXTAREA') return;
  e.preventDefault();
  omni.focus();
});
