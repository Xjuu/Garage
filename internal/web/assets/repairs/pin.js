'use strict';

const $ = (id) => document.getElementById(id);

const form = $('form');
const err = $('err');
const submit = $('submit');

async function trySubmit(code) {
  err.textContent = '';
  submit.disabled = true;
  submit.textContent = 'Checking…';
  try {
    const res = await fetch('/api/repairs/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ code }),
    });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(data.error || 'incorrect code');
    // Reloading, not a hardcoded redirect to "/": the server decides which
    // real page to serve for whatever URL is already in the address bar
    // once the device cookie proves it's authenticated.
    location.reload();
  } catch (e) {
    err.textContent = e.message;
    pin.clear();
  }
  submit.disabled = false;
  submit.textContent = 'Continue';
}

const pin = setupPinBoxes('pin-boxes', trySubmit);

form.addEventListener('submit', (e) => {
  e.preventDefault();
  trySubmit(pin.code());
});
