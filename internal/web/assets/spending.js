/* Spending: a window of spend against the window before it, a daily chart,
   and a scrollable day-by-day list of what was actually bought. */

'use strict';

const spend = { period: '30d', from: '', to: '', reg: '', scope: '' };

const WEEKDAY = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];

function weekdayOf(iso) {
  const d = new Date(iso + 'T00:00:00');
  return Number.isNaN(d.getTime()) ? '' : WEEKDAY[d.getDay()];
}

/** Fill in the days with no spend. The query returns only days that had
    activity, but a chart with gaps closed up would misrepresent the shape of
    a month — a quiet week has to look quiet. */
function fillDays(series, from, to) {
  const byDate = new Map(series.map((d) => [d.date, d]));
  const out = [];
  const cursor = new Date(from + 'T00:00:00');
  const end = new Date(to + 'T00:00:00');
  if (Number.isNaN(cursor.getTime()) || Number.isNaN(end.getTime())) return series;

  // Guard against a pathological range producing an unbounded loop.
  let guard = 0;
  while (cursor <= end && guard++ < 800) {
    const iso = cursor.toISOString().slice(0, 10);
    out.push(byDate.get(iso) || { date: iso, invoices: 0, netto: 0, vat: 0, brutto: 0 });
    cursor.setDate(cursor.getDate() + 1);
  }
  return out;
}

async function loadSpending() {
  const p = new URLSearchParams();
  p.set('period', spend.period);
  if (spend.period === 'custom') {
    if (!spend.from || !spend.to) {
      $('spend-sub').textContent = 'Pick a start and an end date.';
      return;
    }
    p.set('from', spend.from);
    p.set('to', spend.to);
  }
  if (spend.reg) p.set('reg', spend.reg);
  if (spend.scope) p.set('scope', spend.scope);

  const t = await api('/api/spending?' + p.toString());

  const scopeLabel = { general: 'general stock only', vehicle: 'vehicle work only' }[t.scope] || '';
  $('spend-sub').textContent = [
    `${t.from} → ${t.to}`,
    t.vehicle ? `vehicle ${t.vehicle}` : '',
    scopeLabel,
  ].filter(Boolean).join(' · ');

  // Direction only — spending more is not automatically bad, and colouring it
  // red would editorialise.
  let trendTile;
  if (!t.has_prev) {
    trendTile = { k: 'vs previous', v: 'no data', m: `nothing recorded ${t.prev_from} → ${t.prev_to}` };
  } else {
    const pct = t.change_pct;
    const dir = Math.abs(pct) < 0.5 ? 'flat' : (pct > 0 ? 'up' : 'down');
    const word = { up: 'higher', down: 'lower', flat: 'about level' }[dir];
    trendTile = {
      k: 'vs previous',
      html: `<span class="trend ${dir}">${dir === 'flat' ? '' : (pct > 0 ? '+' : '') + pct.toFixed(1) + '%'}</span>`,
      m: `${word} than £${money(t.prev_brutto)} in the ${t.days} days before`,
    };
  }

  $('spend-tiles').innerHTML = [
    { k: t.label, v: '£' + money(t.brutto), m: 'total including VAT' },
    trendTile,
    { k: 'Net', v: '£' + money(t.netto) },
    { k: 'VAT', v: '£' + money(t.vat) },
    { k: 'Repairs', v: int(t.invoices) },
    {
      k: 'Average / repair',
      v: '£' + money(t.invoices ? t.brutto / t.invoices : 0),
      m: t.invoices ? `across ${int(t.invoices)} invoice(s)` : 'nothing in this period',
    },
  ].map((x) => `
    <div class="tile">
      <div class="k">${esc(x.k)}</div>
      <div class="v">${x.html || esc(x.v)}</div>
      ${x.m ? `<div class="m">${esc(x.m)}</div>` : ''}
    </div>`).join('');

  const days = fillDays(t.series, t.from, t.to);
  drawChart('spend-bars', days.map((d) => ({
    label: d.date.slice(5),
    value: d.brutto,
    title: `${d.date} (${weekdayOf(d.date)}) — £${money(d.brutto)} across ${int(d.invoices)} invoice(s)`,
  })));

  renderDays(t.detail);
}

/** The scrollable breakdown: one block per day, each line showing what it was
    and what it cost, with a sticky day header so the date stays visible while
    scrolling through a long day. */
function renderDays(detail) {
  if (!detail.length) {
    $('spend-days').innerHTML =
      '<div class="empty"><strong>Nothing bought in this period</strong>Try a longer window.</div>';
    return;
  }

  $('spend-days').innerHTML = detail.map((day) => `
    <div class="day-block">
      <div class="day-head">
        <span class="d">${esc(day.date)}</span>
        <span class="w">${esc(weekdayOf(day.date))}</span>
        <span class="tot">£${money(day.brutto)}</span>
      </div>
      ${day.lines.map((l) => `
        <div class="day-line" data-id="${l.invoice_id}">
          <span class="what">
            <span class="n">
              ${l.part_number ? `<span class="part">${esc(l.part_number)}</span> ` : ''}${esc(l.description || '—')}
            </span>
            <span class="meta">
              ${esc(l.supplier)}
              ${l.is_general
                ? ' · <span class="tag-general">General stock</span>'
                : (l.vehicle_reg ? ` · <span class="reg">${esc(l.vehicle_reg)}</span>` : '')}
            </span>
          </span>
          <span class="qty">${l.quantity ? int(l.quantity) + ' ×' : ''}</span>
          <span class="price">£${money(l.unit_price || l.netto)}
            <small>${l.quantity > 1 ? '£' + money(l.brutto) + ' total' : 'inc VAT £' + money(l.brutto)}</small>
          </span>
        </div>`).join('')}
    </div>`).join('');

  $('spend-days').querySelectorAll('.day-line').forEach((el) =>
    el.addEventListener('click', () => openInvoice(el.dataset.id)));
}

// ── controls ──────────────────────────────────────────────────────────────

$('spend-periods').querySelectorAll('.chip').forEach((b) =>
  b.addEventListener('click', () => {
    spend.period = b.dataset.period;
    $('spend-periods').querySelectorAll('.chip').forEach((x) =>
      x.setAttribute('aria-pressed', String(x === b)));
    loadSpending().catch((e) => toast(e.message, true));
  }));

// Typing a date implies a custom window, so the chip follows automatically
// rather than leaving the dates silently ignored.
for (const id of ['spend-from', 'spend-to']) {
  $(id).addEventListener('change', () => {
    spend.from = $('spend-from').value;
    spend.to = $('spend-to').value;
    if (spend.from && spend.to) {
      spend.period = 'custom';
      $('spend-periods').querySelectorAll('.chip').forEach((x) =>
        x.setAttribute('aria-pressed', String(x.dataset.period === 'custom')));
    }
    loadSpending().catch((e) => toast(e.message, true));
  });
}

$('spend-reg').addEventListener('input', debounce(() => {
  spend.reg = $('spend-reg').value.trim();
  loadSpending().catch((e) => toast(e.message, true));11
}, 300));

$('spend-scope').addEventListener('change', () => {
  spend.scope = $('spend-scope').value;
  loadSpending().catch((e) => toast(e.message, true));
});

/** Entry point used by the vehicle page, so "how much on this car lately"
    is one click rather than retyping the registration. */
function openSpendingFor(reg) {
  spend.reg = reg || '';
  $('spend-reg').value = spend.reg;
  show('spending');
}

Object.assign(viewLoaders, { spending: loadSpending });
