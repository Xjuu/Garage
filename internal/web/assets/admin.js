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
  const a = await api('/api/admin/status');

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

  $('model-pick')?.addEventListener('focus', loadModelOptions, { once: true });
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

Object.assign(viewLoaders, { admin: loadAdmin });
