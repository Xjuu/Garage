'use strict';

/* Wires up a row of 6 single-digit inputs as one PIN entry control —
   shared by the sign-in screen and the bulk-upload tool's re-verify
   prompt, since both need exactly the same behaviour: typing a digit
   advances focus, backspace on an empty box steps back, pasting a full
   6-digit code fills every box in one go, and the moment all 6 are
   filled onComplete(code) fires on its own — nobody has to find a button.

   Returns { clear(), code() } so the caller can wipe the boxes after a
   wrong code and read the current value if needed. */
function setupPinBoxes(containerId, onComplete) {
  const boxes = Array.from(document.querySelectorAll('#' + containerId + ' .pin-box'));

  function code() {
    return boxes.map((b) => b.value).join('');
  }
  function maybeComplete() {
    const c = code();
    if (/^\d{6}$/.test(c)) onComplete(c);
  }
  function clear() {
    boxes.forEach((b) => { b.value = ''; });
    boxes[0].focus();
  }

  boxes.forEach((box, i) => {
    box.addEventListener('input', () => {
      box.value = box.value.replace(/\D/g, '').slice(-1);
      if (box.value && i < boxes.length - 1) boxes[i + 1].focus();
      maybeComplete();
    });
    box.addEventListener('keydown', (e) => {
      if (e.key === 'Backspace' && !box.value && i > 0) boxes[i - 1].focus();
    });
    box.addEventListener('paste', (e) => {
      const text = (e.clipboardData || window.clipboardData).getData('text').replace(/\D/g, '');
      if (!text) return;
      e.preventDefault();
      for (let j = 0; j < boxes.length; j++) boxes[j].value = text[j] || '';
      const last = Math.min(text.length, boxes.length) - 1;
      if (last >= 0) boxes[last].focus();
      maybeComplete();
    });
  });

  boxes[0].focus();
  return { clear, code };
}
