/* Admin: configuration, connection tests, and data management. */

'use strict';

function humanBytes(n) {
  if (!n) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB'];
  let i = 0;
  let v = Number(n);
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

async function loadAdmin() {
  const seq = beginLoad('admin');
  const a = await api('/api/admin/status');
  if (stale('admin', seq)) return;

  $('admin-tiles').innerHTML = [
    { k: 'Invoices', v: int(a.invoices) },
    { k: 'Line items', v: int(a.items) },
    { k: 'Database', v: humanBytes(a.db_bytes) },
    { k: 'Examples', v: `${int(a.examples_ready)}/${int(a.examples)}`, m: 'ready / total' },
    { k: 'Hints', v: int(a.hints) },
  ].map((t) => `
    <div class="tile"><div class="k">${esc(t.k)}</div><div class="v">${esc(t.v)}</div>
    ${t.m ? `<div class="m">${esc(t.m)}</div>` : ''}</div>`).join('');

  // Secrets are reported as set/unset only — the server never sends them back.
  const rows = [
    ['Mailbox', `${esc(a.imap_host)}:${a.imap_port} · ${esc(a.imap_mailbox)}`],
    ['Mailbox user', a.imap_user ? esc(a.imap_user) : warn('not set')],
    ['Mailbox password', a.imap_pass_set ? ok('set') : warn('not set')],
    ['Gemini key', a.gemini_set ? ok('set') : warn('not set')],
    ['Gemini model', modelPicker(a.gemini_model)],
    ['Scan window', `${a.lookback_days} days`],
    ['Dashboard address', esc(a.web_addr)],
    ['Dashboard password', a.password_set ? ok('set') : warn('NOT SET')],
    ['Two-factor authentication', a.totp_set ? ok('set up')
      : warn('will be required on the next login')],
    ['Secure cookies', a.cookie_secure ? ok('on') : warn('off — turn on behind a tunnel')],
    ['Data folder', `<span class="mono">${esc(a.data_dir)}</span>`],
    ['Examples folder', `<span class="mono">${esc(a.examples_dir)}</span>`],
    ['Exports folder', `<span class="mono">${esc(a.exports_dir)}</span>`],
  ];
  $('admin-config').innerHTML = rows.map(([k, v]) => `
    <tr><td class="strong" style="width:200px">${esc(k)}</td><td>${v}</td></tr>`).join('');

  // Prefill the mailbox form from what is configured; the password is never
  // sent back by the server, so that field always starts empty.
  $('mb-host').value = a.imap_host || 'imap.hostinger.com';
  $('mb-port').value = a.imap_port || 993;
  $('mb-user').value = a.imap_user || '';
  $('mb-folder').value = a.imap_mailbox || 'INBOX';
  $('mb-pass').placeholder = a.imap_pass_set
    ? 'a password is saved — leave blank to keep it'
    : 'mailbox password';

  renderBackups(a.backups, a.schedule);

  $('model-pick')?.addEventListener('focus', loadModelOptions, { once: true });
}

/** Backups and the nightly timer, together: they are the two things that fail
    quietly, and a failure is only useful if it is visible. */
function renderBackups(b, sched) {
  b = b || {}; sched = sched || {};

  $('backup-info').innerHTML = b.enabled
    ? `Keeping the newest ${int(b.keep)} snapshots in <span class="mono">${esc(b.folder)}</span>.
       ${b.count ? `${int(b.count)} stored (${humanBytes(b.bytes)}), latest
         <span class="mono">${esc(b.latest || '')}</span> at ${esc(whenLocal(b.latest_at))}.`
        : 'None taken yet — the first runs with tonight\'s sync.'}`
    : '<span class="pill flag">Off</span> Automatic backups are disabled (GOLDSTAR_BACKUP_KEEP=0).';

  // A run that failed is shown as a banner, not a log line, because the whole
  // problem with a broken nightly sync is that nobody goes looking for it.
  const alert = $('sync-alert');
  if (sched.alert) {
    alert.hidden = false;
    alert.className = 'note flag';
    alert.innerHTML =
      `<strong>The automatic sync is failing.</strong>
       ${int(sched.failures)} run(s) in a row have failed.
       ${sched.last_error ? `Last error: ${esc(sched.last_error)}.` : ''}
       ${sched.last_success
         ? `Last successful sync: ${esc(sched.last_success)} (${int(sched.days_since_success)} day(s) ago).`
         : 'There has been no successful automatic sync yet.'}
       New invoices are not being collected until this is fixed.`;
  } else {
    alert.hidden = true;
    alert.innerHTML = '';
  }
}

function ok(text) { return `<span class="pill">${esc(text)}</span>`; }
function warn(text) { return `<span class="pill flag">${esc(text)}</span>`; }

function modelPicker(current) {
  return `<select id="model-pick" style="max-width:280px">
            <option value="${esc(current)}">${esc(current)}</option>
          </select>
          <div class="m" style="margin-top:4px;color:var(--g4);font-size:12px">
            Changing this needs GEMINI_MODEL updating in your .env — the list is here to
            show what your key can call.</div>`;
}

// The model list costs an API round trip, so it is fetched only when the
// select is actually focused.
async function loadModelOptions() {
  const sel = $('model-pick');
  if (!sel) return;
  try {
    const d = await api('/api/admin/models');
    sel.innerHTML = d.models.map((m) =>
      `<option value="${esc(m)}"${m === d.current ? ' selected' : ''}>${esc(m)}</option>`).join('');
  } catch (e) {
    toast(e.message, true);
  }
}

function connResult(label, r) {
  const el = $('conn-result');
  const cls = r.ok ? '' : ' flag';
  el.innerHTML = `<span class="pill${cls}">${esc(label)}</span>
                  <span style="margin-left:8px">${esc(r.ok ? r.message : r.error)}</span>`;
}

$('test-imap').addEventListener('click', async () => {
  const b = $('test-imap');
  b.disabled = true; b.textContent = 'Testing…';
  try {
    connResult('Mailbox', await api('/api/admin/test-imap', { method: 'POST' }));
  } catch (e) { toast(e.message, true); }
  b.disabled = false; b.textContent = 'Test mailbox';
});

$('test-gemini').addEventListener('click', async () => {
  const b = $('test-gemini');
  b.disabled = true; b.textContent = 'Testing…';
  try {
    connResult('Gemini', await api('/api/admin/test-gemini', { method: 'POST' }));
  } catch (e) { toast(e.message, true); }
  b.disabled = false; b.textContent = 'Test Gemini';
});

$('change-pw').addEventListener('click', async () => {
  const current = $('pw-current').value;
  const next = $('pw-new').value;
  if (next.length < 10) {
    toast('New password must be at least 10 characters', true);
    return;
  }
  try {
    const r = await api('/api/admin/password', {
      method: 'POST', json: { current, new: next },
    });
    $('pw-current').value = '';
    $('pw-new').value = '';
    toast(r.message || 'Password changed');
  } catch (e) { toast(e.message, true); }
});

// ── mailbox ───────────────────────────────────────────────────────────────

async function saveMailbox(verify) {
  const btn = verify ? $('mb-save') : $('mb-save-only');
  const label = btn.textContent;
  btn.disabled = true;
  btn.textContent = verify ? 'Connecting…' : 'Saving…';
  try {
    const r = await api('/api/admin/mailbox', {
      method: 'POST',
      json: {
        host: $('mb-host').value.trim(),
        port: Number($('mb-port').value) || 993,
        user: $('mb-user').value.trim(),
        pass: $('mb-pass').value,
        mailbox: $('mb-folder').value.trim(),
        verify,
      },
    });
    const el = $('mb-result');
    el.innerHTML = r.ok
      ? `<span class="pill">Mailbox</span> <span style="margin-left:8px">${esc(r.message)}</span>`
      : `<span class="pill flag">Mailbox</span> <span style="margin-left:8px">${esc(r.error)}</span>`;
    if (r.ok) {
      $('mb-pass').value = '';   // never leave the secret sitting in the field
      toast(r.message);
      loadAdmin();
    }
  } catch (e) {
    toast(e.message, true);
  }
  btn.disabled = false;
  btn.textContent = label;
}

$('mb-save').addEventListener('click', () => saveMailbox(true));
$('mb-save-only').addEventListener('click', () => saveMailbox(false));

$('backup-db').addEventListener('click', () => { location.href = '/api/admin/backup'; });

$('backup-now').addEventListener('click', async () => {
  const b = $('backup-now');
  b.disabled = true; b.textContent = 'Backing up…';
  try {
    const r = await api('/api/admin/backup-now', { method: 'POST' });
    $('backup-result').innerHTML =
      `<span class="pill">Saved</span> <span style="margin-left:8px">${esc(r.name)} — ${humanBytes(r.bytes)}</span>`;
    loadAdmin();
  } catch (e) {
    $('backup-result').innerHTML =
      `<span class="pill flag">Failed</span> <span style="margin-left:8px">${esc(e.message)}</span>`;
  }
  b.disabled = false; b.textContent = 'Back up now';
});

$('vacuum-db').addEventListener('click', async () => {
  const b = $('vacuum-db');
  b.disabled = true; b.textContent = 'Compacting…';
  try {
    const r = await api('/api/admin/vacuum', { method: 'POST' });
    toast(`Compacted ${humanBytes(r.before)} → ${humanBytes(r.after)}`);
    loadAdmin();
  } catch (e) { toast(e.message, true); }
  b.disabled = false; b.textContent = 'Compact database';
});

// ── server log (temporary) ───────────────────────────────────────────────
// A quick way to see what the server has been doing without SSH access.
// Polls only while the Admin tab is on screen, and only scrolls the box
// down automatically when it was already scrolled to the bottom — so
// reading back through older lines is not fought by the next refresh.

const LOG_EVERY = 4000;

async function loadLogs() {
  const box = $('log-lines');
  if (!box) return;
  const atBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 12;
  try {
    const { lines } = await api('/api/admin/logs');
    box.innerHTML = (lines || []).map((l) =>
      `<div${/ALERT/.test(l) ? ' class="err"' : ''}>${esc(l)}</div>`).join('')
      || '<div style="color:var(--g4)">Nothing logged yet.</div>';
    if (atBottom) box.scrollTop = box.scrollHeight;
  } catch (e) {
    box.innerHTML = `<div class="err">${esc(e.message)}</div>`;
  }
}

setInterval(() => { if (state.view === 'admin') loadLogs(); }, LOG_EVERY);

// ── add 2FA to another device (temporary) ───────────────────────────────
// Re-shows the same secret from setup so it can be scanned into a second
// device, without touching totp.secret or requiring a reset.

$('show-totp-qr').addEventListener('click', async () => {
  const btn = $('show-totp-qr');
  const err = $('totp-reshow-err');
  err.textContent = '';
  btn.disabled = true;
  btn.textContent = 'Loading…';
  try {
    const data = await api('/api/admin/totp-qr');
    $('totp-reshow-qr').src = data.qr_png;
    $('totp-reshow-secret').textContent = data.secret;
    $('totp-reshow').hidden = false;
  } catch (e) {
    err.textContent = e.message;
  }
  btn.disabled = false;
  btn.textContent = 'Show QR code';
});

// ── parts counter admin ──────────────────────────────────────────────────

function whenLocalShort(iso) {
  if (!iso) return '—';
  const d = new Date(iso);
  return isNaN(d) ? esc(iso) : d.toLocaleString('en-GB', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' });
}

async function loadPartsAdmin() {
  const [ips, devices, takes] = await Promise.all([
    api('/api/admin/parts-ips'),
    api('/api/admin/parts-devices'),
    api('/api/admin/parts-takes'),
  ]);

  $('pi-rows').innerHTML = ips.length
    ? ips.map((a) => `
        <tr><td class="mono">${esc(a.ip)}</td><td>${dash(a.label)}</td>
          <td class="mono">${whenLocalShort(a.created_at)}</td>
          <td><button class="btn sm danger" data-ip="${esc(a.ip)}">Remove</button></td></tr>`).join('')
    : '<tr><td colspan="4" class="empty">No addresses allowed yet — the parts site refuses everything</td></tr>';
  $('pi-rows').querySelectorAll('button[data-ip]').forEach((b) =>
    b.addEventListener('click', async () => {
      try {
        await api('/api/admin/parts-ips?ip=' + encodeURIComponent(b.dataset.ip), { method: 'DELETE' });
        loadPartsAdmin();
      } catch (e) { toast(e.message, true); }
    }));

  $('pd-rows').innerHTML = devices.length
    ? devices.map((d) => `
        <tr><td class="mono truncate" title="${esc(d.label)}">${dash(d.label) || '<span class="muted">unknown device</span>'}</td>
          <td class="mono">${whenLocalShort(d.first_seen)}</td>
          <td class="mono">${whenLocalShort(d.last_seen)}</td>
          <td>${d.active ? '<span class="pill">Active</span>' : '<span class="pill flag">Revoked</span>'}</td>
          <td>${d.active ? `<button class="btn sm danger" data-id="${esc(d.id)}">Revoke</button>` : ''}</td></tr>`).join('')
    : '<tr><td colspan="5" class="empty">No device has signed in yet</td></tr>';
  $('pd-rows').querySelectorAll('button[data-id]').forEach((b) =>
    b.addEventListener('click', async () => {
      try {
        await api('/api/admin/parts-devices/revoke', { method: 'POST', json: { id: b.dataset.id } });
        loadPartsAdmin();
      } catch (e) { toast(e.message, true); }
    }));

  $('pt-rows').innerHTML = takes.length
    ? takes.map((t) => `
        <tr><td class="mono">${whenLocalShort(t.taken_at)}</td>
          <td><span class="part">${esc(t.part_number)}</span></td>
          <td><span class="reg">${esc(t.vehicle_reg)}</span></td>
          <td class="num">${int(t.quantity)}</td>
          <td class="mono truncate">${dash(t.device_name)}</td></tr>`).join('')
    : '<tr><td colspan="5" class="empty">Nothing logged yet</td></tr>';
}

$('pi-add').addEventListener('click', async () => {
  const ip = $('pi-ip').value.trim();
  if (!ip) { $('pi-ip').focus(); return; }
  try {
    await api('/api/admin/parts-ips', { method: 'POST', json: { ip, label: $('pi-label').value.trim() } });
    $('pi-ip').value = ''; $('pi-label').value = '';
    toast('Address allowed');
    loadPartsAdmin();
  } catch (e) { toast(e.message, true); }
});

$('pp-save').addEventListener('click', async () => {
  const pin = $('pp-pin').value.trim();
  const result = $('pp-result');
  result.innerHTML = '';
  if (!/^\d{4,12}$/.test(pin)) {
    result.innerHTML = '<span class="pill flag">4-12 digits only</span>';
    return;
  }
  try {
    const r = await api('/api/admin/parts-pin', { method: 'POST', json: { pin } });
    result.innerHTML = `<span class="pill">Saved</span> <span style="margin-left:8px">${esc(r.message)}</span>`;
    $('pp-pin').value = '';
  } catch (e) {
    result.innerHTML = `<span class="pill flag">Failed</span> <span style="margin-left:8px">${esc(e.message)}</span>`;
  }
});

Object.assign(viewLoaders, { admin: () => loadAdmin().then(loadLogs).then(loadPartsAdmin) });
