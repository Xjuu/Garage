// Login. Kept separate from app.js so the sign-in page loads nothing else.
//
// Four stages, one visible at a time: the password form, then — once the
// password is right — whichever of these the account still needs, in
// order: a forced change if it's still on a temporary password, then
// either the 2FA setup panel (this account has never finished it) or the
// 2FA verify panel (it already has). Nothing here ever treats a correct
// password alone as "signed in": that only happens once
// /api/login/totp/confirm or /api/login/totp/verify succeeds.

const passwordPanel = document.getElementById('form');
const changePasswordPanel = document.getElementById('change-password');
const setupPanel = document.getElementById('totp-setup');
const verifyPanel = document.getElementById('totp-verify');

const err = document.getElementById('err');
const submit = document.getElementById('submit');

function showPanel(panel) {
  for (const p of [passwordPanel, changePasswordPanel, setupPanel, verifyPanel]) p.hidden = p !== panel;
}

async function postJSON(url, body) {
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body || {}),
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || 'Something went wrong');
  return data;
}

// ── stage 1: password ───────────────────────────────────────────────────

passwordPanel.addEventListener('submit', async (e) => {
  e.preventDefault();
  err.textContent = '';
  submit.disabled = true;
  submit.textContent = 'Signing in…';

  try {
    const data = await postJSON('/api/login', {
      user: document.getElementById('user').value,
      password: document.getElementById('password').value,
    });
    await enterStage(data.stage);
  } catch (e2) {
    err.textContent = e2.message;
    submit.disabled = false;
    submit.textContent = 'Sign in';
    // Clear only the password: retyping a username you got right is annoying,
    // and the failure is far more often the password.
    const pw = document.getElementById('password');
    pw.value = '';
    pw.focus();
  }
});

// ── stage 1b: forced password change ────────────────────────────────────

const changeForm = document.getElementById('change-password-form');
const changeErr = document.getElementById('change-password-err');
const changeSubmit = document.getElementById('change-password-submit');
const newPassword = document.getElementById('new-password');

changeForm.addEventListener('submit', async (e) => {
  e.preventDefault();
  changeErr.textContent = '';
  changeSubmit.disabled = true;
  changeSubmit.textContent = 'Saving…';
  try {
    const data = await postJSON('/api/login/change-password', { password: newPassword.value });
    await enterStage(data.stage);
  } catch (e2) {
    changeErr.textContent = e2.message;
    changeSubmit.disabled = false;
    changeSubmit.textContent = 'Set password and continue';
    newPassword.value = '';
    newPassword.focus();
  }
});

// ── stage 2a: first-time 2FA setup ──────────────────────────────────────

const setupForm = document.getElementById('setup-form');
const setupErr = document.getElementById('setup-err');
const setupSubmit = document.getElementById('setup-submit');
const setupCode = document.getElementById('setup-code');

async function loadSetupQR() {
  const data = await postJSON('/api/login/totp/setup');
  document.getElementById('totp-qr').src = data.qr_png;
  document.getElementById('totp-secret').textContent = data.secret;
}

setupForm.addEventListener('submit', async (e) => {
  e.preventDefault();
  setupErr.textContent = '';
  setupSubmit.disabled = true;
  setupSubmit.textContent = 'Confirming…';
  try {
    await postJSON('/api/login/totp/confirm', { code: setupCode.value });
    location.href = '/';
  } catch (e2) {
    setupErr.textContent = e2.message;
    setupSubmit.disabled = false;
    setupSubmit.textContent = 'Confirm and finish setup';
    setupCode.value = '';
    setupCode.focus();
  }
});

// ── stage 2b: everyday 2FA verify ───────────────────────────────────────

const verifyForm = document.getElementById('verify-form');
const verifyErr = document.getElementById('verify-err');
const verifySubmit = document.getElementById('verify-submit');
const verifyCode = document.getElementById('verify-code');

verifyForm.addEventListener('submit', async (e) => {
  e.preventDefault();
  verifyErr.textContent = '';
  verifySubmit.disabled = true;
  verifySubmit.textContent = 'Verifying…';
  try {
    await postJSON('/api/login/totp/verify', { code: verifyCode.value });
    location.href = '/';
  } catch (e2) {
    verifyErr.textContent = e2.message;
    verifySubmit.disabled = false;
    verifySubmit.textContent = 'Verify';
    verifyCode.value = '';
    verifyCode.focus();
  }
});

// A 6-digit code submits itself the moment it's complete — one less click
// for the case that happens every single login.
for (const input of [setupCode, verifyCode]) {
  input.addEventListener('input', () => {
    input.value = input.value.replace(/\D/g, '').slice(0, 6);
    if (input.value.length === 6) input.form.requestSubmit();
  });
}

// "Not you?" clears the pending cookie and starts over at the password form.
for (const id of ['change-password-restart', 'setup-restart', 'verify-restart']) {
  document.getElementById(id).addEventListener('click', async (e) => {
    e.preventDefault();
    await fetch('/api/logout', { method: 'POST' }).catch(() => {});
    location.reload();
  });
}

// ── entering a stage, including resuming one after a reload ────────────

async function enterStage(stage) {
  if (stage === 'change_password') {
    showPanel(changePasswordPanel);
    newPassword.focus();
  } else if (stage === 'setup') {
    showPanel(setupPanel);
    setupCode.focus();
    await loadSetupQR();
  } else if (stage === 'verify') {
    showPanel(verifyPanel);
    verifyCode.focus();
  }
}

// A reload mid-2FA (the pending cookie is still good, but there is no
// session yet) would otherwise just show the password form again and throw
// away the QR code — this picks the flow back up where it left off.
(async () => {
  try {
    const s = await fetch('/api/session').then((r) => r.json());
    if (s.totp_pending) await enterStage(s.totp_stage);
  } catch { /* the plain password form is a fine fallback */ }
})();
