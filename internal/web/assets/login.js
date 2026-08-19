// Login. Kept separate from app.js so the sign-in page loads nothing else.
const form = document.getElementById('form');
const err = document.getElementById('err');
const submit = document.getElementById('submit');

form.addEventListener('submit', async (e) => {
  e.preventDefault();
  err.textContent = '';
  submit.disabled = true;
  submit.textContent = 'Signing in…';

  try {
    const res = await fetch('/api/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        user: document.getElementById('user').value,
        password: document.getElementById('password').value,
      }),
    });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(data.error || 'Sign in failed');
    location.href = '/';
  } catch (e) {
    err.textContent = e.message;
    submit.disabled = false;
    submit.textContent = 'Sign in';
    // Clear only the password: retyping a username you got right is annoying,
    // and the failure is far more often the password.
    const pw = document.getElementById('password');
    pw.value = '';
    pw.focus();
  }
});
