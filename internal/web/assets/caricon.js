/* Vehicle silhouettes.
 *
 * Every path is drawn in `currentColor`, so the icon takes the colour of the
 * text around it: black on the light theme, white on the dark one, with no
 * second asset and no theme-detection code. Changing the palette changes the
 * icons for free.
 *
 * The shape follows the body type, because a fleet of taxis, MPVs and vans is
 * easier to scan when a van actually looks like a van.
 */

'use strict';

// Matched against "make model" in lower case, first hit wins — so the more
// specific patterns have to come before the general ones.
const BODY_PATTERNS = [
  [/\b(transit|transporter|vito|caddy|expert|traffic|trafic|proace|sprinter|ducato|boxer)\b/, 'van'],
  [/\b(tx4|txc|tx-?\d|e7|vista|black cab|london taxi|lti|levc)\b/, 'taxi'],
  [/\b(sharan|sharran|sharron|touran|alhambra|alhmabra|galaxy|s.?max|zafira|voxy|noahvoxy|jogger|tourneo|prius\+|prius plus|pruuis|verso)\b/, 'mpv'],
  [/\b(kuga|niro|xtrail|x-?trail|outlander|ch-?r|chr|arkana|hrv|hr-?v|ioniq|ionic|iconiq|niro|jogger)\b/, 'suv'],
  [/\b(estate|touring|avant|v90|superb|octavia|passat|mondeo|insignia)\b/, 'estate'],
];

/** Work out a body style from whatever the fleet export called it. */
function bodyStyle(make, model) {
  const text = `${make || ''} ${model || ''}`.toLowerCase();
  for (const [pattern, style] of BODY_PATTERNS) {
    if (pattern.test(text)) return style;
  }
  return 'saloon';
}

// Each silhouette is a single filled path plus two wheels, on a 64x28 canvas.
// Kept deliberately simple: at 34px wide, detail turns to mud.
const SHAPES = {
  saloon:
    'M4 20h56v-4c0-1.2-.9-2.2-2-2.4l-8-1.4-6.5-5.6C42.6 5.6 41.3 5 40 5H24c-1.6 0-3 .9-3.7 2.3L17 13.8l-9 1.5c-2.3.4-4 2.4-4 4.7z',
  estate:
    'M4 20h56v-5c0-1.3-1-2.4-2.3-2.6l-6.7-1-6.4-5.2C43.5 5.4 42.3 5 41 5H24c-1.6 0-3 .9-3.7 2.3L17 13.5l-9 1.5c-2.3.4-4 2.4-4 4.7z',
  mpv:
    'M4 20h56v-6c0-1.4-1-2.6-2.4-2.9l-5.6-1-7-5.7C44.2 3.5 43 3 41.7 3H23c-1.7 0-3.2 1-4 2.5L15 13l-7 1.4c-2.3.5-4 2.5-4 4.9z',
  van:
    'M4 20h56V8c0-1.7-1.3-3-3-3H23c-1.6 0-3 .9-3.7 2.3L15 14l-7 1.2c-2.3.4-4 2.4-4 4.8z',
  suv:
    'M4 20h56v-6c0-1.4-1.1-2.6-2.5-2.8l-6.5-1.1-6.6-5.4C43.3 3.9 42.1 3.5 41 3.5H24c-1.6 0-3.1 1-3.8 2.4L17 13l-9 1.5c-2.3.4-4 2.4-4 4.7z',
  taxi:
    'M4 20h56v-5c0-1.3-1-2.4-2.3-2.6l-7.7-1.2-6.4-5.4C42.5 4.5 41.3 4 40 4H24c-1.6 0-3 .9-3.7 2.3L17 13.2l-9 1.5c-2.3.4-4 2.4-4 4.7z',
};

/**
 * An inline SVG silhouette for a vehicle.
 *
 * @param {string} make
 * @param {string} model
 * @param {string} extraClass  optional, e.g. "lg" for the detail page
 */
function carIcon(make, model, extraClass = '') {
  const style = bodyStyle(make, model);
  const label = [make, model].filter(Boolean).join(' ') || 'vehicle';

  // The roof sign is what makes a taxi read as a taxi at this size.
  const roofSign = style === 'taxi'
    ? '<rect class="car-sign" x="26" y="0.5" width="12" height="4" rx="1.2"/>'
    : '';

  return `
    <svg class="car ${esc(extraClass)}" viewBox="0 0 64 28" role="img"
         aria-label="${esc(label)}" focusable="false">
      ${roofSign}
      <path class="car-body" d="${SHAPES[style]}"/>
      <circle class="car-wheel" cx="17" cy="21" r="4.4"/>
      <circle class="car-wheel" cx="47" cy="21" r="4.4"/>
      <circle class="car-hub" cx="17" cy="21" r="1.7"/>
      <circle class="car-hub" cx="47" cy="21" r="1.7"/>
    </svg>`;
}

/** Registration, callsign and model as one block, for use in a table cell. */
function vehicleCell(reg, make, model, callsign) {
  const spec = [make, model].filter(Boolean).join(' ');
  return `
    <span class="veh">
      ${carIcon(make, model)}
      <span class="veh-text">
        <span class="veh-reg">${esc(reg)}</span>
        <span class="veh-sub">
          ${callsign ? `<span class="callsign">${esc(callsign)}</span>` : ''}
          ${spec ? esc(spec) : '<span class="muted">not in the fleet list</span>'}
        </span>
      </span>
    </span>`;
}
