(async () => {
  const live = state;
  const liveAPI = api;
  const results = [];
  const setup = {...live.settings, email: 'synthetic@example.invalid', remember_password: false};
  const status = () => ({
    settings: setup, run_settings: setup, mappings: [[80, 80]],
    status: 'Not connected', active: false, stopping: false, cancel: false,
    password: {saved: false, memory: false, busy: false, code: '', notice: ''},
    native: true, events: [], log_path: '', save_failed: false
  });
  const assert = (value, message) => {if (!value) throw new Error(message);};
  const reset = () => {
    setup.remember_password = false;
    active = false;
    initialized = true;
    connected = true;
    pending = '';
    lastFailure = '';
    if (typeof actionError !== 'undefined') actionError = '';
    if (typeof sourceChoice !== 'undefined') sourceChoice = '';
    load(setup);
    $('password').value = '';
    state = status();
    clearErrors();
    controls();
  };
  const check = async (name, run) => {
    try {
      reset();
      await run();
      results.push(name + ': PASS');
    } catch (e) {
      results.push(name + ': FAIL: ' + e.message);
    }
  };
  try {
    await check('R3 stale response', async () => {
      let release;
      api = () => new Promise(resolve => {release = resolve;});
      const old = refresh();
      api = async () => ({...status(), active: true, cancel: true, status: 'Connecting to your telescope…'});
      $('password').value = 'synthetic-password';
      await act('start');
      assert(active, 'new run was not rendered');
      release({...status(), status: 'Stopped'});
      await old;
      assert(active && $('start').textContent === 'Cancel' && $('options').disabled, 'late idle response restored Connect');
    });
    await check('R3 validation precedence', async () => {
      const rejected = {...status(), failure: {code: 'signin', field: 'password'}, failure_message: 'Sign-in was rejected.'};
      render(rejected);
      $('password').value = 'corrected-synthetic-password';
      api = async () => {throw {code: 'ports', field: 'ports', message: 'Check port mappings.'};};
      await act('start');
      render(rejected);
      assert(document.activeElement === $('ports') && !$('ports-error').hidden, 'old sign-in failure displaced the current ports error');
    });
    await check('R9 render focus', async () => {
      $('start').focus();
      render({...status(), failure: {code: 'signin', field: 'password'}, failure_message: 'Check the password.'});
      assert(document.activeElement === $('start'), 'render focused a field below Connect');
    });
    await check('S1 memory reload', async () => {
      initialized = false;
      api = async () => ({...status(), password: {...status().password, memory: true}});
      await refresh();
      assert($('password').readOnly && $('password').value === 'This launch only' && !$('save-password').disabled, 'reload lost backend memory state');
      $('change-password').click();
      $('password').value = 'new-synthetic-password';
      await refresh();
      assert($('password').value === 'new-synthetic-password' && !$('password').readOnly, 'poll erased a typed draft');
    });
    await check('U2 saved association', async () => {
      assert($('use-saved').hidden, 'saved source offered without an association');
      render({...status(), password: {...status().password, saved: true}});
      $('change-password').click();
      assert(!$('use-saved').hidden, 'saved source unavailable for its exact account');
      $('email').value = 'different@example.invalid';
      $('email').dispatchEvent(new Event('input'));
      assert($('use-saved').hidden, 'saved source crossed accounts');
    });
    await check('F3 removal retry and S4 cleanup', async () => {
      state = {...status(), password: {...status().password, saved: true}};
      setup.remember_password = true;
      load(setup);
      controls();
      api = async () => {throw {code: 'password_remove', field: 'password', message: 'Could not remove the keychain copy.'};};
      for (let i = 0; i < 2; i++) {
        await act('forget');
        assert(!$('forget').disabled, 'F3: Remove remained disabled after denial');
        assert($('forget').textContent === 'Retry removal', 'retry feedback missing');
      }
      api = async () => ({...status(), settings: {...setup, remember_password: false}, password: {...status().password, code: 'password_removed', notice: 'Password removed from keychain and this app.'}});
      await act('forget');
      assert($('password-error').hidden && !$('password').hasAttribute('aria-invalid'), 'S4: successful removal left the old field error');
      assert($('forget').hidden && !$('password').readOnly && $('password').value === '', 'removal left an obsolete source');
      assert($('feedback').textContent === 'Password removed from keychain and this app.', 'removal feedback missing');
      setup.remember_password = false;
    });
    await check('U3 diagnostic-only codes', async () => {
      $('password').value = 'synthetic-password';
      api = async () => {throw {code: 'signin', field: 'password', message: 'Check the password.'};};
      await act('start');
      assert(!$('notice').textContent.includes('[') && !$('password-error').textContent.includes('['), 'error code leaked into visible recovery text');
      assert($('error-code').textContent === 'Error code: signin', 'Details lost the error code');
    });
    await check('U4 running summary and U13 timing label', async () => {
      render({...status(), active: true, discovery_started: true});
      assert($('run-summary').textContent === 'Phone on this network · password for this launch only · discovery on', 'running summary is missing or inaccurate');
      assert($('connection-mode').textContent.startsWith('Connection timing: '), 'Details timing has no label');
    });
    await check('S5 explicit selection copy', async () => {
      $('model').value = 'synthetic';
      $('model').dispatchEvent(new Event('input'));
      assert($('telescope-selection').textContent === 'Selected in Advanced', 'explicit telescope still described as Automatic');
      $('model').value = '';
    });
  } finally {
    api = liveAPI;
    pending = '';
    active = live.active;
    lastFailure = '';
    if (typeof actionError !== 'undefined') actionError = '';
    if (typeof sourceChoice !== 'undefined') sourceChoice = '';
    load(live.settings);
    clearErrors();
    say('');
    alertText('');
    render(live);
    $('advanced').open = false;
    $('start').focus();
  }
  window.__guitestPageState = results.join('\n');
})()
