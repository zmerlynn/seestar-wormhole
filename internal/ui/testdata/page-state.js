(async () => {
  const live = state;
  const liveAPI = api;
  const liveTimeout = window.setTimeout;
  window.setTimeout = () => {};
  const results = [];
  const setup = {...live.settings, email: 'synthetic@example.invalid', remember_password: false};
  const status = () => ({
    settings: setup, run_settings: setup, mappings: [[80, 80]],
    port_defaults: live.port_defaults, version: 'synthetic-version', effective: live.effective,
    status: 'Not connected', active: false, stopping: false, cancel: false,
    password: {saved: false, memory: false, busy: false, code: '', notice: ''},
    native: true, events: [], log_path: '', save_failed: false
  });
  const assert = (value, message) => {if (!value) throw new Error(message);};
  const reset = () => {
    ended = false;
    setup.remember_password = false;
    active = false;
    initialized = true;
    connected = true;
    pending = '';
    lastFailure = '';
    if (typeof actionError !== 'undefined') actionError = '';
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
    await check('Setup help and services grouping', async () => {
      applyGoal('custom');
      const custom = $('custom-help'), discovery = $('discovery-help');
      assert(!custom.hidden && custom.classList.contains('help') && !custom.classList.contains('error') && !custom.hasAttribute('aria-invalid') && !$('goal').hasAttribute('aria-invalid'), 'Custom is styled as an error');
      assert($('goal').getAttribute('aria-describedby') === 'goal-help custom-help', 'Use with help association missing');
      assert(discovery.previousElementSibling === $('discovery').closest('label') && discovery.classList.contains('help') && parseFloat(getComputedStyle(discovery).marginLeft) > 0, 'discovery help detached or not indented');
      assert($('services-title').tagName === 'H2' && $('services-title').textContent === 'Services' && $('ports').getAttribute('aria-labelledby') === 'services-title', 'Services heading missing');
      for (const [id, placeholder] of [['serial', 'From your Seestar account'], ['model', 'e.g. S50 Pro']]) {
        assert($(id).placeholder === placeholder && !$(id).hasAttribute('value') && !$(id+'-help') && $(id).getAttribute('aria-describedby').includes('selection-help'), id+' example is not placeholder-only');
      }
      assert(Array.from($('ports').querySelectorAll('th')).map(e => e.textContent).join('|') === 'Computer port|Seestar port|Purpose', 'ambiguous port headings');
    });
    await check('R3 stale response', async () => {
      let release;
      api = () => new Promise(resolve => {release = resolve;});
      const old = refresh();
      api = async () => ({...status(), active: true, cancel: true, status: 'Connecting to your Seestar…'});
      $('password').value = 'synthetic-password';
      await act('start');
      assert(active, 'new run was not rendered');
      release({...status(), status: 'Stopped'});
      await old;
      assert(active && $('start').textContent === 'Cancel' && $('options').disabled, 'late idle response restored Connect');
    });
    for (const action of ['stop', 'quit']) await check('Release progress during ' + action, async () => {
      render({...status(), active: true, status: 'Connected to your Seestar'});
      let release;
      api = async name => name === action ? new Promise(resolve => {release = resolve;}) :
        {...status(), active: true, stopping: true, status: 'Stopping — releasing the Seestar session…'};
      const finishing = act(action);
      await poll();
      assert(pending === action && !ended, 'release completed before its response');
      assert(state.stopping && $('status').textContent === 'Stopping — releasing the Seestar session…' && $('settings').hidden, 'pending action hid release progress');
      assert($('start').disabled, 'Connect enabled before release joined');
      release({ok: true});
      await finishing;
      assert(pending === '' && ended === (action === 'quit'), 'release response did not finish the action');
    });
    await check('R3 validation precedence', async () => {
      const rejected = {...status(), failure: {code: 'signin', field: 'password'}, failure_message: 'Sign-in was rejected.'};
      render(rejected);
      $('password').value = 'corrected-synthetic-password';
      api = async () => {throw {code: 'ports', field: 'ports', message: 'Check port mappings.'};};
      await act('start');
      render(rejected);
      assert(document.activeElement === $('ports') && !$('ports-error').hidden, 'old sign-in failure displaced the current ports error');
      api = async action => action === 'start' ? {ok: true} : rejected;
      await act('start');
      assert(!$('password-error').hidden, 'accepted run with the same failure lost its field error');
    });
    await check('R9 render focus', async () => {
      initialized = false;
      api = async () => status();
      await refresh();
      await new Promise(requestAnimationFrame);
      assert(document.activeElement === $('start'), 'initial status did not focus Connect');
      $('goal').focus();
      await refresh();
      await new Promise(requestAnimationFrame);
      assert(document.activeElement === $('goal'), 'poll stole focus from Use with');
      $('start').focus();
      render({...status(), failure: {code: 'signin', field: 'password'}, failure_message: 'Check the password.'});
      assert(document.activeElement === $('start'), 'render focused a field below Connect');
    });
    await check('Two password states', async () => {
      initialized = false;
      api = async () => ({...status(), password: {...status().password, memory: true}});
      await refresh();
      assert(!$('password').disabled && $('password').value === '' && $('save-password').disabled, 'memory reload invented a third password state');
      $('password').value = 'new-synthetic-password';
      $('password').dispatchEvent(new Event('input'));
      await refresh();
      assert($('password').value === 'new-synthetic-password' && !$('password').disabled, 'poll erased a typed draft');
      assert(!$('save-password').disabled && $('password-note').textContent === 'Kept until you quit. Save to your keychain to use it next time.', 'editable password controls');
      render({...status(), password: {...status().password, saved: true}});
      assert($('password').disabled && $('password').value === 'Saved in keychain' && $('save-password').hidden && !$('forget').hidden, 'saved state is editable');
      assert($('password-note').textContent === 'Saved in your keychain.', 'saved help repeats confirmation');
      assert(getComputedStyle($('password')).backgroundColor !== getComputedStyle($('email')).backgroundColor && getComputedStyle($('password')).outlineStyle === 'none', 'saved state is not visibly disabled');
      assert(!$('change-password') && !$('use-saved'), 'obsolete password source controls');
      $('email').value = 'different@example.invalid';
      $('email').dispatchEvent(new Event('input'));
      assert(!$('password').disabled && $('forget').hidden && $('password').value === '', 'saved association crossed accounts');
      $('email').value = setup.email;
      $('email').dispatchEvent(new Event('input'));
      assert($('password').disabled, 'saved association did not return for exact email');
      render(status());
      assert(!$('password').disabled && $('password').value === '', 'removed placeholder became a password');
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
      assert($('forget').hidden && !$('password').disabled && $('password').value === '', 'removal left an obsolete source');
      assert($('feedback').textContent === 'Password removed from keychain and this app.', 'removal feedback missing');
      setup.remember_password = false;
    });
    await check('U3 diagnostic-only codes', async () => {
      $('email').value = '';
      await act('start');
      assert($('error-code').textContent === 'Error code: email', 'submit validation did not update the diagnostic code');
      $('email').value = setup.email;
      $('password').value = 'synthetic-password';
      api = async () => {throw {code: 'signin', field: 'password', message: 'Check the password.'};};
      await act('start');
      assert(!$('notice').textContent.includes('[') && !$('password-error').textContent.includes('['), 'error code leaked into visible recovery text');
      assert($('error-code').textContent === 'Error code: signin', 'Details lost the error code');
    });
    await check('Running view uses effective settings', async () => {
      const run = {...setup, bind: '0.0.0.0', discovery: true, idle: '0'};
      render({...status(), effective: {...status().effective, listen: 'all_networks', idle: {mode: 'keep_connected'}}, run_settings: run, active: true, discovery_started: true, mappings: [[80,80], [1234,445]]});
      assert($('run-summary').textContent === 'Listening on all networks · Seestar app discovery on · Keep connected', 'running summary inaccurate');
      const rows = $('running-ports').querySelectorAll('tbody tr');
      assert(rows.length === 10 && !$('running-ports').querySelector('input'), 'running ports must use read-only table');
      assert(rows[1].cells[0].textContent === '80' && rows[9].cells[0].textContent === '1234' && rows[2].cells[0].textContent === 'Off', 'running table lost effective values');
      assert(!$('phone-steps').hidden && $('phone-steps').querySelector('h2').textContent === 'Using the Seestar app' && !$('phone-steps').querySelector('ol'), 'Seestar app help too prominent or missing');
      render({...status(), active: true, effective: {...status().effective, listen: 'this_computer', idle: {mode: 'on_demand'}}, run_settings: {...run, bind: '127.0.0.1', discovery: false, idle: '2m'}});
      assert($('run-summary').textContent === 'Listening on this computer only · Seestar app discovery off · Connect when an app needs Seestar' && $('phone-steps').hidden, 'computer/idle summary inaccurate');
      render({...status(), active: true, effective: {...status().effective, listen: 'other'}, run_settings: {...run, bind: 'synthetic-address'}});
      assert($('run-summary').textContent.startsWith('Listening on a selected network address ·'), 'Other listen summary missing');
    });
    await check('Custom switches immediately', async () => {
      $('listen').value = 'other';
      $('listen').dispatchEvent(new Event('change'));
      assert($('goal').value === 'custom' && !$('custom-help').hidden && $('custom-help').classList.contains('help'), 'Custom warning missing');
      assert($('custom-help').textContent === 'Set in Advanced. Choosing another setup replaces these settings.', 'Custom copy');
      $('port-80').value = '1234';
      $('goal').value = 'computer';
      $('goal').dispatchEvent(new Event('change'));
      assert($('goal').value === 'computer' && $('listen').value === '127.0.0.1' && !$('discovery').checked && $('port-80').value === '8080' && $('custom-help').hidden, 'Custom trapped setup selection');
      $('goal').value = 'phone';
      $('goal').dispatchEvent(new Event('change'));
      assert($('port-80').value === '80' && $('port-445').value === '4445' && $('discovery').checked, 'Phone defaults changed');
      assert(!$('switch-confirm') && !$('switch-setup') && !$('keep-custom'), 'confirmation flow remains');
    });
    await check('Structured ports and validation', async () => {
      $('password').value = 'synthetic-password';
      const rows = $('ports').querySelectorAll('tbody tr');
      assert(rows.length === 10 && rows[0].cells[1].textContent === '4700' && rows[9].cells[2].textContent === 'File sharing (SMB)', 'port rows or order');
      assert([...$('ports').querySelectorAll('input')].every(input => input.type === 'number' && input.min === '1' && input.max === '65535'), 'port number bounds');
      for (const value of ['0', '-1', '65536', '1.5', $('port-4700').value]) {
        $('port-80').value = value;
        assert(!validate() && !$('ports-error').hidden && $('port-80').getAttribute('aria-invalid') === 'true', 'accepted invalid/duplicate local ' + value);
        $('port-80').dispatchEvent(new Event('input'));
        assert($('ports-error').hidden && !$('port-80').hasAttribute('aria-invalid'), 'port error persists during edit');
      }
      $('port-80').value = '';
      assert(validate() && settings().ports.find(p => p.device === 80).local === 0, 'empty local did not disable service');
      assert(settings().ports.length === 10 && settings().ports.every(p => typeof p.local === 'number' && typeof p.device === 'number'), 'ports became text');
    });
    await check('Field errors clear on edit and email normalization', async () => {
      assert($('email').type === 'text' && $('email').inputMode === 'email' && $('email').autocomplete === 'username', 'browser email validity still active');
      for (const id of ['email','password','idle','bind','serial','model']) {
        fieldError(id, 'Synthetic field error.', id);
        $(id).dispatchEvent(new Event('input'));
        assert($(id + '-error').hidden && !$(id).hasAttribute('aria-invalid'), 'typing did not clear ' + id);
      }
      for (const [id, field] of [['listen','bind'], ['discovery','discovery'], ['mode','idle']]) {
        fieldError(field, 'Synthetic field error.', field);
        $(id).dispatchEvent(new Event('change'));
        assert($(field + '-error').hidden && !$(id).hasAttribute('aria-invalid'), 'change did not clear ' + id);
      }
      $('email').value = '  Mixed. Case+Tag @Example.Invalid  ';
      $('password').value = 'synthetic-password';
      assert(settings().email === 'Mixed.Case+Tag@Example.Invalid', 'email not stripped/trimmed or was lowercased');
      $('email').value = 'vendor-will-validate';
      assert(validate(), 'page added email format validation');
      $('email').value = '   ';
      assert(!validate() && $('email-error').textContent === 'Enter your Seestar account email.', 'empty email copy');
    });
    await check('Details and copied summary are JSON', async () => {
      const event = {time: '2026-01-01T19:16:00Z', message: 'App opened.', code: 'lifecycle', phase: '', reason: ''};
      const effective = {idle: {mode: 'keep_connected', minutes: 0}, listen: 'all_networks', discovery: true, ports: {'80': 80}};
      render({...status(), effective, events: [event], log_path: 'synthetic-private-path'});
      assert($('effective-settings').textContent === JSON.stringify(effective, null, 2), 'Settings is not pretty JSON');
      const localTime = new Date(event.time).toLocaleTimeString([], {hour: 'numeric', minute: '2-digit'});
      assert($('events').children.length === 1 && $('events').children[0].textContent === localTime + '  Application opened.', 'event category or UTC leaked into local list');
      const copied = JSON.parse(diagnostic);
      assert(Object.keys(copied).join() === 'app,version,settings,events' && copied.app === 'seestar-wormhole' && copied.version === 'synthetic-version' && copied.events[0].time === event.time && !diagnostic.includes('synthetic-private-path') && !$('mappings'), 'copied object schema/privacy');
      const descriptor = Object.getOwnPropertyDescriptor(navigator, 'clipboard');
      let clipboard = '';
      Object.defineProperty(navigator, 'clipboard', {configurable: true, value: {writeText: async value => {clipboard = value;}}});
      try {
        const feedback = $('feedback').textContent;
        $('copy').click();
        await Promise.resolve();
        assert(clipboard === diagnostic && $('copy-feedback').textContent === 'Diagnostic summary copied.' && $('feedback').textContent === feedback, 'copy confirmation entered main status or JSON gained extra text');
        assert($('copy-feedback').previousElementSibling === $('copy'), 'copy confirmation is not next to Copy');
      } finally {
        if (descriptor) Object.defineProperty(navigator, 'clipboard', descriptor);
        else delete navigator.clipboard;
      }
    });
    await check('Account legend computed alignment', async () => {
      const legend = document.querySelector('#account legend'), style = getComputedStyle(legend);
      const textLeft = legend.getBoundingClientRect().left + parseFloat(style.paddingLeft) + parseFloat(style.borderLeftWidth);
      const inputLeft = $('email').getBoundingClientRect().left;
      assert(parseFloat(style.paddingLeft) === 0 && Math.abs(textLeft - inputLeft) < 1, 'Account text is indented from inputs');
      results.push('Account computed style: padding-left=' + style.paddingLeft + ', text-left=' + textLeft + ', input-left=' + inputLeft);
    });
    await check('S5 explicit selection copy', async () => {
      $('model').value = 'synthetic';
      $('model').dispatchEvent(new Event('input'));
      assert($('telescope-selection').textContent === 'Selection entered', 'explicit telescope still described as Automatic');
      $('model').value = '';
    });
  } finally {
    window.setTimeout = liveTimeout;
    api = liveAPI;
    pending = '';
    active = live.active;
    lastFailure = '';
    if (typeof actionError !== 'undefined') actionError = '';
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
