/* Vehicle illustrations.
 *
 * Drawn as side elevations on a shared 96x40 canvas, every one facing left, so
 * a column of them lines up: same ground line, same wheelbase, same eye level.
 * Only the body above the wheels changes, which is what makes a van read as a
 * van at a glance without anything being labelled.
 *
 * Everything uses `currentColor`, so an icon takes the colour of the text
 * around it — black on the light theme, white on the dark one — with no second
 * asset and nothing watching the colour scheme. Glass is punched out in the
 * page background for the same reason.
 *
 * These are original silhouettes. They are not traced from anyone's artwork.
 */

'use strict';

// Matched against "make model" in lower case; first hit wins, so the specific
// patterns come before the general ones.
const BODY_PATTERNS = [
  [/\b(transit|transporter|vito|caddy|expert|traffic|trafic|proace|sprinter|procab)\b/, 'van'],
  [/\b(tx4|txc|tx-?\d|e7|vista|black cab|london taxi|\blti\b|levc)\b/, 'taxi'],
  [/\b(sharan|sharran|sharron|touran|alhambra|alhmabra|galaxy|s.?max|zafira|voxy|noahvoxy|tourneo|prius\s*\+|prius plus|pruuis|verso|alphard|jogger)\b/, 'mpv'],
  [/\b(kuga|niro|xtrail|x-?trail|outlander|ch-?r|chr|arkana|hr-?v|ioniq|ionic|iconiq|niro)\b/, 'suv'],
  [/\b(superb|octavia|passat|mondeo|insignia|v90|avant|estate|touring)\b/, 'estate'],
];

function bodyStyle(make, model) {
  const text = `${make || ''} ${model || ''}`.toLowerCase();
  for (const [pattern, style] of BODY_PATTERNS) {
    if (pattern.test(text)) return style;
  }
  return 'saloon';
}

// Shared geometry, so every vehicle sits on the same road at the same scale
// and a column of them lines up. All face left.
const WHEEL = { front: 25, rear: 71, y: 30, r: 7.2, hub: 2.9, arch: 9.4 };

// Each profile is a closed outline drawn nose-first from the front bumper.
// The differences are deliberately exaggerated — at 40px wide a realistic
// difference between a saloon and an estate is invisible, so the roofline and
// ride height carry the meaning.
const SHAPES = {
  // Three-box: long bonnet, low roof, separate boot.
  saloon: {
    body: 'M4 27V24c0-1.6 1.1-3 2.7-3.3L22 18l11-8.4C34.6 8.5 36.4 8 38.3 8h17.4c1.9 0 3.7.6 5.2 1.7L71 18l16.5 2.7C89.4 21 91 22.6 91 24.5V27z',
    glass: 'M36 17.5l8.6-6.6c.6-.5 1.4-.8 2.2-.8H50v7.4zM54 17.5v-7.4h2.4c.9 0 1.7.3 2.4.8l8.4 6.6z',
  },
  // Same nose, but the roof runs flat to the tail — no boot step.
  estate: {
    body: 'M4 27V24c0-1.6 1.1-3 2.7-3.3L22 18l11-8.4C34.6 8.5 36.4 8 38.3 8h30.4c2 0 3.9.7 5.4 2L86 20.4c3 .5 5 2 5 4.1V27z',
    glass: 'M36 17.5l8.6-6.6c.6-.5 1.4-.8 2.2-.8H50v7.4zM54 17.5v-7.4h13.6c1 0 2 .4 2.8 1l7.6 6.4z',
  },
  // One-box people carrier: short bonnet, steep screen, tall flat roof.
  mpv: {
    body: 'M4 27v-2.6c0-1.8 1.3-3.3 3.1-3.6L16 19.2l13.4-13C31 4.6 33.2 3.6 35.5 3.6h32.9c2.6 0 5 1.2 6.6 3.2L84 18.6l3.6 2.4c2 .5 3.4 2 3.4 3.8V27z',
    glass: 'M30 18.6L41.6 7.3c.6-.6 1.5-1 2.4-1H50v12.3zM54 18.6V6.3h13.8c1.2 0 2.3.5 3.1 1.5l8.6 10.8z',
  },
  // Raised ride height and a squarer shoulder; the roof stays car-like.
  suv: {
    body: 'M4 24v-3.4c0-1.8 1.3-3.3 3.1-3.6L20 14.6l9.8-8.4C31.4 4.9 33.4 4.2 35.5 4.2h25c2.1 0 4.1.7 5.7 2l9.8 8.4 12.9 2.4c1.8.3 3.1 1.8 3.1 3.6V24z',
    glass: 'M33.5 14l8-6.9c.6-.5 1.4-.8 2.2-.8H50V14zM54 14V6.3h4.3c.8 0 1.6.3 2.2.8l8 6.9z',
  },
  // Tall box: minimal bonnet, vertical rear, cargo body above the waistline.
  van: {
    body: 'M4 27v-2.6c0-1.7 1.2-3.2 2.9-3.5L16 19l10.6-11.3C28.2 6 30.4 5 32.7 5H84c3.9 0 7 3.1 7 7v15z',
    glass: 'M29 18.5l8.8-9.3c.7-.7 1.6-1.1 2.6-1.1H50v10.4zM54 18.5V8.1h27c1.1 0 2 .9 2 2v8.4z',
  },
  // Upright hackney cab: short nose, near-vertical screen, roof sign.
  taxi: {
    body: 'M4 27v-2.4c0-1.7 1.2-3.2 2.9-3.5L17 19l9.6-11C28.2 6.2 30.4 5.2 32.7 5.2H66c2.4 0 4.7 1.1 6.2 3L80 18l7 2.6c2.2.6 4 2 4 3.9V27z',
    glass: 'M30 18.4l8.4-9.6c.6-.7 1.5-1.1 2.5-1.1H50v10.7zM54 18.4V7.7h11.6c1.1 0 2.2.5 2.9 1.4L76 18.4z',
  },
};

/**
 * One vehicle, as inline SVG.
 *
 * @param {string} make
 * @param {string} model
 * @param {string} extraClass  "lg" on a detail page, "sm" in a dense table
 */
function carIcon(make, model, extraClass = '') {
  const style = bodyStyle(make, model);
  const shape = SHAPES[style];
  const label = [make, model].filter(Boolean).join(' ') || 'vehicle';

  // The roof sign is what makes a hackney read as a hackney at this size.
  const sign = style === 'taxi'
    ? '<rect class="car-sign" x="42" y="2.5" width="16" height="5" rx="1.6"/>'
    : '';

  // The arch is punched out of the body first, so the tyre sits inside the
  // bodywork instead of hanging beneath a flat slab. That gap is most of what
  // separates a finished vehicle icon from a rectangle with circles attached.
  const wheel = (cx) => `
    <circle class="car-arch" cx="${cx}" cy="${WHEEL.y}" r="${WHEEL.arch}"/>
    <circle class="car-tyre" cx="${cx}" cy="${WHEEL.y}" r="${WHEEL.r}"/>
    <circle class="car-hub"  cx="${cx}" cy="${WHEEL.y}" r="${WHEEL.hub}"/>`;

  return `
    <svg class="car ${esc(extraClass)}" viewBox="0 0 96 40" role="img"
         aria-label="${esc(label)}" focusable="false">
      ${sign}
      <path class="car-body" d="${shape.body}"/>
      <path class="car-glass" d="${shape.glass}"/>
      ${wheel(WHEEL.front)}${wheel(WHEEL.rear)}
    </svg>`;
}

/** Registration, callsign and model together, for a table cell. */
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
