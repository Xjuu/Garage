/* Line charts drawn as SVG: a value axis with real numbers on it, gridlines,
   and a marker at every data point.

   The previous bar rendering was dishonest in two ways, and both are fixed
   here. It scaled every bar against the largest value in the set, so a single
   data point always drew as a full-height bar regardless of whether it was £5
   or £5,000; and it carried no value axis, so there was nothing to read the
   height against. This draws a zero-based axis with labelled gridlines, so a
   height on screen corresponds to an amount you can actually check. */

'use strict';

const CHART = {
  height: 240,
  padTop: 26,      // room for the value labels that sit above each marker
  padRight: 22,
  padBottom: 32,
  padLeft: 58,
};

// Below this many points every marker gets its value printed above it. A chart
// with three points and no numbers on it is just a shape; with the numbers it
// is a readable summary, which is what a short series actually needs.
const LABEL_ALL_UNDER = 11;

/** Round a maximum up to a readable number so axis labels land on 100 / 250 /
    500 rather than 437.28. */
function niceMax(value) {
  if (!(value > 0)) return 10;
  const mag = Math.pow(10, Math.floor(Math.log10(value)));
  const norm = value / mag;
  const step = norm <= 1 ? 1 : norm <= 2 ? 2 : norm <= 2.5 ? 2.5 : norm <= 5 ? 5 : 10;
  return step * mag;
}

function axisLabel(v) {
  if (v >= 1000) return '£' + (v / 1000).toFixed(v % 1000 === 0 ? 0 : 1) + 'k';
  if (v % 1 === 0) return '£' + v;
  return '£' + v.toFixed(2);
}

/** How many x labels can fit without overlapping, given the width available. */
function labelStride(count, usableWidth) {
  const perLabel = 58;
  const room = Math.max(1, Math.floor(usableWidth / perLabel));
  return Math.max(1, Math.ceil(count / room));
}

/**
 * Draw a line chart.
 *
 * @param {string} elementID  container to render into
 * @param {Array}  points     [{label, value, title}] in display order
 * @param {object} opts       {area: bool, onPick: fn(point, index)}
 */
function lineChart(elementID, points, opts = {}) {
  const el = $(elementID);
  if (!el) return;

  if (!points || points.length === 0) {
    el.innerHTML = '<div class="chart-empty">Nothing to plot for this period</div>';
    return;
  }

  // An axis drawn over all-zero data is worse than saying so plainly: it
  // implies a measurement where there was no activity at all.
  if (!points.some((p) => (Number(p.value) || 0) > 0)) {
    el.innerHTML = '<div class="chart-empty">No spend recorded in this period</div>';
    return;
  }

  const w = Math.max(el.clientWidth || 0, 320);
  const h = CHART.height;
  const x0 = CHART.padLeft;
  const y0 = CHART.padTop;
  const x1 = w - CHART.padRight;
  const y1 = h - CHART.padBottom;
  const plotW = x1 - x0;
  const plotH = y1 - y0;

  const values = points.map((p) => Number(p.value) || 0);
  const top = niceMax(Math.max(...values));

  // A single point has no line to draw, so it is centred and shown as a
  // marker. Stretching one value across the full width would invent a trend
  // that the data does not contain.
  const stepX = points.length > 1 ? plotW / (points.length - 1) : 0;
  const px = (i) => (points.length > 1 ? x0 + i * stepX : x0 + plotW / 2);
  const py = (v) => y1 - (Math.max(0, v) / top) * plotH;

  const ticks = 4;
  let grid = '';
  for (let i = 0; i <= ticks; i++) {
    const v = (top / ticks) * i;
    const y = py(v);
    grid += `<line class="c-grid" x1="${x0}" y1="${y}" x2="${x1}" y2="${y}"/>`;
    grid += `<text class="c-ylab" x="${x0 - 10}" y="${y + 4}" text-anchor="end">${esc(axisLabel(v))}</text>`;
  }

  const coords = points.map((p, i) => [px(i), py(Number(p.value) || 0)]);
  const path = coords.map(([x, y], i) => `${i === 0 ? 'M' : 'L'}${x.toFixed(1)},${y.toFixed(1)}`).join(' ');

  let area = '';
  if (opts.area !== false && coords.length > 1) {
    area = `<path class="c-area" d="${path} L${coords[coords.length - 1][0].toFixed(1)},${y1} L${coords[0][0].toFixed(1)},${y1} Z"/>`;
  }

  const line = coords.length > 1 ? `<path class="c-line" d="${path}"/>` : '';

  const stride = labelStride(points.length, plotW);
  let xlabs = '';
  points.forEach((p, i) => {
    // Always label the last point: it is the one the reader is usually
    // looking for, and a stride can otherwise skip it.
    if (i % stride !== 0 && i !== points.length - 1) return;
    xlabs += `<text class="c-xlab" x="${px(i)}" y="${y1 + 18}" text-anchor="middle">${esc(p.label)}</text>`;
  });

  // Markers last so they sit above the line. Each carries a wide invisible
  // hit area, because a 4px dot is unreasonable to hit with a mouse.
  const labelAll = points.length <= LABEL_ALL_UNDER;
  let dots = '';
  points.forEach((p, i) => {
    const v = Number(p.value) || 0;
    const [x, y] = coords[i];
    const title = p.title || `${p.label} — ${money(v)}`;

    // On a short series, print the value above the marker. Zero points are
    // skipped: labelling a run of "£0" adds clutter without information.
    if (labelAll && v > 0) {
      const clampedX = Math.min(Math.max(x, x0 + 14), x1 - 14);
      dots += `<text class="c-val" x="${clampedX}" y="${(y - 13).toFixed(1)}" text-anchor="middle">£${money(v)}</text>`;
    }
    dots += `<g class="c-pt" data-i="${i}" tabindex="0">
      <title>${esc(title)}</title>
      <circle class="c-hit" cx="${x}" cy="${y}" r="16"/>
      <circle class="c-dot" cx="${x}" cy="${y}" r="${labelAll ? 5 : 3.5}"/>
    </g>`;
  });

  el.innerHTML = `
    <svg class="chart" viewBox="0 0 ${w} ${h}" width="100%" height="${h}"
         preserveAspectRatio="xMidYMid meet" role="img">
      ${grid}
      <line class="c-axis" x1="${x0}" y1="${y0}" x2="${x0}" y2="${y1}"/>
      <line class="c-axis" x1="${x0}" y1="${y1}" x2="${x1}" y2="${y1}"/>
      ${area}${line}${dots}${xlabs}
    </svg>`;

  if (opts.onPick) {
    el.querySelectorAll('.c-pt').forEach((g) =>
      g.addEventListener('click', () => opts.onPick(points[Number(g.dataset.i)], Number(g.dataset.i))));
  }
}

// Charts are sized from clientWidth, so they must be redrawn when the window
// changes. Each container remembers how to rebuild itself.
const chartRedraw = new Map();

function drawChart(elementID, points, opts) {
  chartRedraw.set(elementID, () => lineChart(elementID, points, opts));
  lineChart(elementID, points, opts);
  watch(elementID);
}

// A container's width can change without the window resizing — opening a tab,
// or the drawer closing. ResizeObserver catches those; the window listener is
// the fallback where it is unavailable.
const observed = new Set();
let resizeTimer = null;

function redrawAll() {
  clearTimeout(resizeTimer);
  resizeTimer = setTimeout(() => {
    for (const [id, redraw] of chartRedraw) {
      const el = $(id);
      if (el?.isConnected && el.clientWidth > 0) redraw();
    }
  }, 120);
}

function watch(elementID) {
  if (observed.has(elementID) || typeof ResizeObserver === 'undefined') return;
  const el = $(elementID);
  if (!el) return;
  observed.add(elementID);
  let lastWidth = el.clientWidth;
  new ResizeObserver(() => {
    // Only a width change matters; height changes come from our own redraw and
    // would otherwise loop.
    if (Math.abs(el.clientWidth - lastWidth) < 2) return;
    lastWidth = el.clientWidth;
    redrawAll();
  }).observe(el);
}

window.addEventListener('resize', redrawAll);
