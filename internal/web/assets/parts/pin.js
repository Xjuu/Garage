'use strict';

const form = document.getElementById('form');
const code = document.getElementById('code');
const err = document.getElementById('err');
const submit = document.getElementById('submit');

// Digits only, and submit itself the moment all six are in — one less tap
// for something typed dozens of times a day at a workbench.
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
    const res = await fetch('/api/parts/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ code: code.value }),
    });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(data.error || 'Sign in failed');
    location.href = '/';
  } catch (e2) {
    err.textContent = e2.message;
    submit.disabled = false;
    submit.textContent = 'Continue';
    code.value = '';
    code.focus();
  }
});
