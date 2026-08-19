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

// The index of the currently highlighted hit, within the flat list of
// buttons rendered in DOM order (vehicles, then parts, suppliers, invoices —
// the same order renderOmni draws them in). Reset to 0 on every new render:
// a fresh set of results makes whatever position the arrow keys had reached
// in the old list meaningless.
let omniIndex = 0;

function omniHits() {
  return omniResults.hidden ? [] : [...omniResults.querySelectorAll('.omni-hit')];
}

/** Moves the "this is what Enter opens" marker onto one hit and off every
    other. Also drives aria-activedescendant, so a screen reader announces
    the same row the sighted highlight points at — this is a real listbox,
    not just a decorated list of buttons. */
function selectOmniHit(hits, index) {
  hits.forEach((el, i) => el.classList.toggle('selected', i === index));
  const chosen = hits[index];
  if (chosen) {
    omni.setAttribute('aria-activedescendant', chosen.id);
    chosen.scrollIntoView({ block: 'nearest' });
  } else {
    omni.removeAttribute('aria-activedescendant');
  }
}

function closeOmni() {
  omniResults.hidden = true;
  omniResults.innerHTML = '';
  omni.setAttribute('aria-expanded', 'false');
  omni.removeAttribute('aria-activedescendant');
}

/** Called once a result has actually been picked — by click or by Enter on
    the top hit. The search bar goes back to its resting state: empty, the
    clear button gone, focus released — rather than sitting there holding the
    query that has already done its job. */
function resetOmni() {
  omni.value = '';
  $('omni-clear').hidden = true;
  closeOmni();
  omni.blur();
}

/** A hit row. Amount means gross for invoices/vehicles/suppliers and net for
    parts, so each row is labelled rather than leaving the reader to guess.
    `index` is the row's position in the flat, whole-dropdown list — not its
    position within its own group — so it lines up with omniIndex. */
function hitRow(h, index) {
  const amountLabel = h.kind === 'part' ? 'net' : 'gross';
  const countLabel = h.kind === 'invoice' ? '' : `<small>${int(h.count)}×</small>`;
  return `
    <button class="omni-hit" id="omni-hit-${index}" role="option"
            data-kind="${esc(h.kind)}" data-ref="${esc(h.ref)}">
      <span class="kind">${KIND_BADGE[h.kind] || '?'}</span>
      <span class="body">
        <span class="t">${esc(h.title)}</span>
        <span class="s">${esc(h.subtitle || '')}${h.date ? ' · ' + esc(h.date) : ''}</span>
      </span>
      <span class="amt">£${money(h.brutto)} <small>${amountLabel}</small>${countLabel}</span>
    </button>`;
}

function renderOmni(res) {
  omni.setAttribute('aria-expanded', 'true');

  if (!res.total) {
    omniResults.innerHTML = '<div class="omni-empty">Nothing matches that.</div>';
    omniResults.hidden = false;
    omni.removeAttribute('aria-activedescendant');
    return;
  }

  let html = '';
  let index = 0;
  for (const kind of ['vehicles', 'parts', 'suppliers', 'invoices']) {
    const hits = res[kind] || [];
    if (!hits.length) continue;
    html += `<div class="omni-group-title">${KIND_LABEL[hits[0].kind]}</div>`;
    html += hits.map((h) => hitRow(h, index++)).join('');
  }
  omniResults.innerHTML = html;
  omniResults.hidden = false;

  const hits = omniHits();
  hits.forEach((b, i) => {
    b.addEventListener('click', () => {
      openHit(b.dataset.kind, b.dataset.ref);
      resetOmni();
    });
    // Hovering a row with the mouse moves the keyboard selection onto it
    // too, so the highlight and Enter never disagree about which hit is
    // "the" one — without this, arrowing to row 3 and then hovering row 1
    // would leave both looking selected in different ways.
    b.addEventListener('mouseenter', () => selectOmniHit(hits, i));
  });

  omniIndex = 0;
  selectOmniHit(hits, omniIndex);
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

// Up/Down move the highlight among however many hits are showing, without
// touching the typed query or re-running the search — this is purely about
// which row Enter will act on next.
omni.addEventListener('keydown', (e) => {
  if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
  const hits = omniHits();
  if (!hits.length) return;

  // Left unhandled, Up/Down do nothing useful in a single-line text input
  // anyway — this just makes the takeover explicit instead of relying on
  // that being true in every browser.
  e.preventDefault();

  // Clamped rather than wrapped: running off the end of the list and
  // silently landing back at the top is a worse surprise than the highlight
  // simply stopping at the last row.
  omniIndex = e.key === 'ArrowDown'
    ? Math.min(omniIndex + 1, hits.length - 1)
    : Math.max(omniIndex - 1, 0);
  selectOmniHit(hits, omniIndex);
});

// Enter opens whichever hit is currently highlighted — the first one by
// default, or wherever the arrow keys or a mouse hover last moved it to.
// Vehicles, parts and suppliers rank above invoices, so typing a plate and
// pressing Enter goes straight to that vehicle rather than to a filtered
// list containing it.
//
// Falling back to the filtered invoice list only when there is no hit to open
// — a date-only search, or a query that matched nothing — keeps that jump as
// a genuine fallback rather than the default action.
omni.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') { closeOmni(); omni.blur(); return; }
  if (e.key !== 'Enter') return;

  // resetOmni() below calls omni.blur(), which changes document.activeElement
  // synchronously — before this same keydown event finishes bubbling. Without
  // stopping it here, the document-level "Return focuses search" handler
  // added further down would see the input no longer focused and immediately
  // refocus it, undoing the reset in the same keystroke.
  e.stopPropagation();

  const hits = omniHits();
  const selected = hits[omniIndex];
  if (selected) {
    openHit(selected.dataset.kind, selected.dataset.ref);
    resetOmni();
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

// "/" or Return focuses the search bar from anywhere, unless something else
// already has a stronger claim on the keystroke.
document.addEventListener('keydown', (e) => {
  if (e.metaKey || e.ctrlKey) return;
  if (e.key !== '/' && e.key !== 'Enter') return;

  const active = document.activeElement;
  const tag = active?.tagName;
  if (tag === 'INPUT' || tag === 'SELECT' || tag === 'TEXTAREA') return;

  // Return carries native behaviour "/" does not: it activates a focused
  // button or link, and a modal dialog may want it for its own primary
  // action. Stealing it there would hijack the button, or send the cursor
  // to a search bar sitting invisibly behind an open dialog.
  if (e.key === 'Enter') {
    if (tag === 'BUTTON' || tag === 'A' || active?.isContentEditable) return;
    if (!$('gen-modal').hidden || !$('files-modal').hidden) return;
  }

  e.preventDefault();
  omni.focus();
});
