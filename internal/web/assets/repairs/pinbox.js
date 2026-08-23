'use strict';

/* A 6-digit PIN control that LOOKS like six boxes but is backed by one
   real, invisible text input covering the whole row — the same technique
   iOS's own passcode screen and most banking-app OTP entry use, and for a
   specific reason: six separate focusable inputs with JS jumping focus
   between them on every keystroke is a well-known cross-browser/mobile
   trap. Autofill suggestion bars, predictive text and IME composition
   routinely interfere with a programmatic .focus() fired from inside the
   very 'input' event handler that's still processing a keystroke — the
   failure mode is exactly "typing does nothing" or "never advances to the
   next box". With one real input there's nothing to hand focus between:
   it behaves like any ordinary text field, and native backspace and paste
   both work for free instead of needing their own handlers.

   Used by the sign-in screen. Returns { clear(), code() } so the caller
   can wipe the field after a wrong code and read the current value if
   needed. */
function setupPinBoxes(containerId, onComplete) {
  const container = document.getElementById(containerId);
  const boxes = Array.from(container.querySelectorAll('.pin-box'));
  const hidden = container.querySelector('.pin-hidden-input');

  function render() {
    const digits = hidden.value.split('');
    boxes.forEach((b, i) => { b.textContent = digits[i] || ''; });
    // The box lined up with wherever typing will land next, so the row
    // still reads left-to-right as you go even with only one real input.
    const at = digits.length < boxes.length ? digits.length : boxes.length - 1;
    boxes.forEach((b, i) => b.classList.toggle('active', i === at));
  }

  function code() {
    return hidden.value;
  }
  // Deferred a frame: calling .focus() in the same synchronous tick that
  // just unhid an element's container is a known cross-browser flake — the
  // browser hasn't necessarily finished laying the element out yet, and
  // focus() on something not yet paintable is quietly a no-op. The sign-in
  // page's field is visible from first paint, so this doesn't actually bite
  // today, but costs nothing and keeps this safe for any caller that does
  // start hidden.
  function focus() {
    requestAnimationFrame(() => hidden.focus());
  }
  function clear() {
    hidden.value = '';
    render();
    focus();
  }

  hidden.addEventListener('input', () => {
    hidden.value = hidden.value.replace(/\D/g, '').slice(0, boxes.length);
    render();
    if (hidden.value.length === boxes.length) onComplete(hidden.value);
  });

  render();
  focus();
  return { clear, code, focus };
}
