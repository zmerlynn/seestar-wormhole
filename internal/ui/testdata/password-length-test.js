// A typed password outside 8 to 32 UTF-16 units is refused with the
// account's length rule, as the server words it; an empty one too.
async function checkPasswordLength(want) {
  initialized = connected = true;
  $('email').value = 'owner@example.invalid';
  for (const [typed, refused] of [['', true], ['short', true], ['x'.repeat(33), true], ['x'.repeat(8), false], ['x'.repeat(32), false], ['\u{1F600}'.repeat(4), false], ['\u{1F600}'.repeat(17), true]]) {
    $('password').value = typed;
    validate();
    const shown = !$('password-error').hidden;
    if (shown !== refused) throw new Error(`a ${typed.length}-unit password: refused ${shown}, want ${refused}`);
    if (refused && $('password-error').textContent !== want) throw new Error('password copy: ' + $('password-error').textContent);
  }
}
