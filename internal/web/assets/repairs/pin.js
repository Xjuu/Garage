'use strict';

const $ = (id) => document.getElementById(id);

const form = $('form');
const code = $('code');
const err = $('err');
const submit = $('submit');

// Auto-submits once 6 digits are typed — a shared shop code entered on a
// shared device, not something worth an extra tap to confirm.
code.addEventListener('input', () => {
  code.value = code.value.replace(/\D/g, '').slice(0, 6);
  if (code.value.length === 6) form.requestSubmit();
});

form.addEventListener('submit', async (e) => {
  e.preventDefault();
  err.textContent = '';
  submit.disabled = true;
  submit.textContent = 'Checking…';
  try {
    const res = await fetch('/api/repairs/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ code: code.value }),
    });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(data.error || 'incorrect code');
    location.href = '/';
  } catch (e) {
    err.textContent = e.message;
    code.value = '';
    code.focus();
  }
  submit.disabled = false;
  submit.textContent = 'Continue';
});
