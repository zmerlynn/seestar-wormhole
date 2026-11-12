(async () => {
  const liveAPI = api;
  const liveTimeout = window.setTimeout;
  // Join the live poll at its scheduling boundary before replacing its API.
  // Otherwise it can run against a synthetic status and lose its next timer.
  await new Promise(resolve => {
    window.setTimeout = (callback, ...args) => {
      if (callback === poll) {
        resolve();
        return;
      }
      return liveTimeout(callback, ...args);
    };
  });
  const live = state;
  // A synthetic poll also reads copy's status through the stubbed API, which
  // answers with the connection's; each check starts from copy's live one.
  const liveCopy = copyStatus;
  window.setTimeout = () => {};
  const results = [];
  const setup = {...live.settings, email: 'synthetic@example.invalid', remember_password: false};
  const status = () => ({
    settings: setup,
    port_defaults: live.port_defaults, version: 'synthetic-version', effective: live.effective,
    status: 'Not connected', active: false, stopping: false, cancel: false, ready: false,
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
    copyStatus = liveCopy;
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
    await check('Connected telescope status row', async () => {
      const label = 'Seestar S50 Pro synthetic-choice-1234';
      const message = 'Connected to ' + label;
      const event = {time:'2026-01-01T19:16:00Z',message,code:'lifecycle',phase:'',reason:''};
      render({...status(),active:true,status:message,selected_device:label,events:[event]});
      assert($('status').textContent === message && !$('selected-device') && !$('change-telescope') && !$('change-telescope-help'), 'connected row retained separate telescope controls');
      assert($('start').textContent === 'Stop' && $('start').parentElement === $('primary-action') && $('announcements').nextElementSibling === $('primary-action') && $('settings').hidden, 'Stop must be directly below running status');
      assert($('events').children[0].textContent.endsWith('  '+message) && JSON.parse(diagnostic).events[0].message === message, 'selected status lost in events or copied summary');
    });
    await check('Buttons share font size', async () => {
      const size = getComputedStyle($('quit')).fontSize;
      for (const id of ['start','save-password','forget','retry-storage','copy']) {
        assert(getComputedStyle($(id)).fontSize === size, 'Quit size differs from '+id);
      }
      const primary = getComputedStyle($('start'));
      assert(Number(primary.fontWeight) >= 600 && primary.borderTopWidth === '2px', 'Connect/Stop lost bold text or border');
    });
    await check('Setup help and services grouping', async () => {
      applyGoal('custom');
      const custom = $('custom-help'), discovery = $('discovery-help');
      assert(!custom.hidden && custom.classList.contains('help') && !custom.classList.contains('error') && !custom.hasAttribute('aria-invalid') && !$('goal').hasAttribute('aria-invalid'), 'Custom is styled as an error');
      assert($('goal').getAttribute('aria-describedby') === 'goal-help custom-help', 'Use with help association missing');
      assert(discovery.previousElementSibling === $('discovery').closest('label') && discovery.classList.contains('help') && parseFloat(getComputedStyle(discovery).marginLeft) > 0, 'discovery help detached or not indented');
      assert($('ports-error').compareDocumentPosition($('ports')) & Node.DOCUMENT_POSITION_FOLLOWING && $('port-permissions').compareDocumentPosition($('ports')) & Node.DOCUMENT_POSITION_FOLLOWING, 'port recovery follows the port table');
      assert($('services-title').tagName === 'H2' && $('services-title').textContent === 'Services' && $('ports').getAttribute('aria-labelledby') === 'services-title', 'Services heading missing');
      assert(document.querySelector('label[for="serial"]').textContent === 'Serial number', 'Serial label');
      assert(document.querySelector('label[for="model"]').textContent === 'Model (optional)', 'Model label');
      for (const [id, placeholder] of [['serial', 'From your Seestar account'], ['model', 'e.g. S50 Pro']]) {
        const described = id === 'serial' ? 'serial-help serial-error' : id+'-error';
        assert($(id).placeholder === placeholder && !$(id).hasAttribute('value') && (id === 'serial' ? $('serial-help').hidden : !$(id+'-help')) && $(id).getAttribute('aria-describedby') === described, id+' example is not placeholder-only');
      }
      assert(Array.from($('details').querySelectorAll('h2')).map(e => e.textContent).join('|') === 'Recent events|Connection settings|Copy settings|Files', 'Details headings missing');
      assert(Array.from($('ports').querySelectorAll('th')).map(e => e.textContent).join('|') === 'Local port|Seestar port|Purpose', 'ambiguous port headings');
      assert($('ports-help').textContent === "Local ports are on this computer. Leave a port empty to turn off that service. Copy, Browse and Free up space don't need the SMB port.", 'local port definition missing');
    });
    await check('Version and external links', async () => {
      const s = status();
      render(s);
      assert($('version').textContent === s.version, 'footer lost the launch version');
      const bridge = window._wails;
      let opened = '', prevented = false;
      const link = $('releases-link');
      const click = {target: link, button: 0, preventDefault() {prevented = true;}};
      try {
        window._wails = {invoke(message) {opened = message;}};
        openExternalLink(click);
        assert(prevented && opened === 'seestar:open:'+link.href, 'native link did not use the browser bridge');
        window._wails = undefined;
        prevented = false;
        openExternalLink(click);
        assert(!prevented, 'browser link lost normal navigation');
      } finally {window._wails = bridge;}
      render({...s, failure: {code: 'http_permission', field: 'ports'}, failure_message: 'See the port permissions guide.'});
      assert(!$('port-permissions').hidden, 'permission error lacks a guide link');
      render(s);
      assert($('port-permissions').hidden, 'permission guide stayed after recovery');
    });
    await check('Empty Other address', async () => {
      $('password').value = 'synthetic-password';
      $('listen').value = 'other';
      $('bind').value = '  ';
      $('discovery').checked = false;
      let calls = 0;
      api = async () => { calls++; return status(); };
      await act('start');
      assert(calls === 0 && fieldErrors.bind === 'bind_empty' && document.activeElement === $('bind'), 'empty Other address submitted');
      assert($('bind-error').textContent === 'Enter a local network address, or choose This computer only or All networks.', 'empty address recovery missing');
    });
    await check('Actionable field announcements', async () => {
      for (const id of ['email','password','idle','bind','discovery','ports','serial','model']) {
        assert($(id + '-error').getAttribute('role') === 'alert', id + ' recovery is not an alert');
      }
      const focus = $('email').focus;
      try {
        $('email').focus = () => {
          assert(!$('email-error').hidden && $('email-error').textContent === fieldCopy.email, 'focused before revealing recovery');
          focus.call($('email'));
        };
        fieldError('email', fieldCopy.email, 'email', true);
      } finally { $('email').focus = focus; }
      fail({code:'signin', field:'password', message:'Check your account password.'});
      assert($('notice').hidden && $('password-error').textContent === 'Check your account password.' && !$('password-error').hidden, 'generic pointer displaced actionable recovery');
      actionError = '';
      render({...status(), failure:{code:'http_permission',field:'ports'}, failure_message:'See the port permissions guide.'});
      assert($('notice').hidden && !$('ports-error').hidden, 'async field failure lacks its own announcement');
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
      assert(!$('password').disabled && $('password').value === '' && !$('save-password').disabled, 'memory reload invented a third password state');
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
    await check('Save retained account password', async () => {
      state = {...status(), password:{...status().password,memory:true}};
      controls();
      assert(!$('save-password').disabled && !$('password').value, 'retained password requires retyping to save');
      let sent;
      api = async (name, body) => {if (name === 'password/save') {sent = body; return {ok:true};} return {...status(), password:{...status().password,saved:true}};};
      await act('password/save');
      assert(sent && sent.password === '' && sent.settings.email === setup.email && $('password').disabled, 'memory Save did not use the existing account action');
      $('email').value = 'different@example.invalid';
      controls();
      assert($('save-password').disabled, 'retained password crossed accounts');
    });
    await check('Password action focus', async () => {
      $('password').value = 'synthetic-password';
      const saved = {...status(), password:{...status().password,saved:true,code:'password_saved'}};
      api = async name => name === 'status' ? saved : {ok:true};
      await act('password/save');
      assert(document.activeElement === $('forget') && !$('forget').disabled, 'Save did not focus Remove');
      api = async () => {throw {code:'password_remove',field:'password',message:'Unlock and retry removal.'};};
      await act('forget');
      assert(document.activeElement === $('forget') && !$('forget').disabled && $('forget').textContent === 'Retry removal', 'failed Remove lost focus');
      api = async name => name === 'status' ? status() : {ok:true};
      await act('forget');
      assert(document.activeElement === $('password') && !$('password').disabled, 'Remove did not focus Password');
      $('email').focus();
      render(status());
      assert(document.activeElement === $('email'), 'routine render moved focus');
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
      render({...status(), effective: {...status().effective, listen: 'all_networks', idle: {mode: 'keep_connected'}, ports: {80:80,445:1234}}, active: true, discovery_started: true});
      assert($('run-summary').textContent === 'Listening on all networks · Seestar app discovery on · Keep connected', 'running summary inaccurate');
      const rows = $('running-ports').querySelectorAll('tbody tr');
      assert(rows.length === 11 && !$('running-ports').querySelector('input'), 'running ports must use read-only table');
      assert(Array.from($('running-ports').querySelectorAll('th')).map(e => e.textContent).join('|') === 'Local port|Seestar port|Purpose', 'ambiguous running port headings');
      assert(!$('running-ports-box').open && $('running-ports-box').querySelector('summary').textContent === 'Ports forwarded to your Seestar' && $('phone-steps').compareDocumentPosition($('running-ports-box')) & Node.DOCUMENT_POSITION_FOLLOWING, 'running ports must sit closed below the Seestar app steps');
      assert(rows[0].cells[0].textContent === '80' && rows[1].cells[0].textContent === '1234' && rows[2].cells[0].textContent === 'Off', 'running table lost effective values');
      assert($('phone-steps').querySelector('p').textContent === "On your phone, open the Seestar app and select your Seestar from the LAN tab.", 'phone instructions omit the LAN tab');
      assert(!$('phone-steps').hidden && $('phone-steps').querySelector('h2').textContent === 'Using the Seestar app' && !$('phone-steps').querySelector('ol'), 'Seestar app help too prominent or missing');
      assert(!$('copy-steps').hidden && $('copy-port').textContent === '1234' && $('copy-steps').querySelector('h2').textContent === 'Copying files' && $('phone-steps').compareDocumentPosition($('copy-steps')) & Node.DOCUMENT_POSITION_FOLLOWING && $('copy-steps').compareDocumentPosition($('running-ports-box')) & Node.DOCUMENT_POSITION_FOLLOWING, 'copying help missing, wrong port or misplaced');
      // Copy and Browse lead, with Go to Copy once the tabs are open; another
      // SMB app's port and the guide follow.
      const [lead, other] = $('copy-steps').querySelectorAll('p'), guide = $('copy-steps').querySelector('a');
      assert(lead.textContent.startsWith('Use the Copy tab') && lead.contains($('copy-steps-go')) && $('copy-steps-go').hidden, 'copying help does not lead with Copy, or offers Go to Copy while the tabs are locked');
      assert(other.textContent.startsWith('Another file-sharing (SMB) app') && other.contains($('copy-port')) && other.contains(guide) && guide.href.endsWith('/docs/copying.md#copy-with-a-graphical-app') && $('copy-steps-go').compareDocumentPosition($('copy-port')) & Node.DOCUMENT_POSITION_FOLLOWING && $('copy-port').compareDocumentPosition(guide) & Node.DOCUMENT_POSITION_FOLLOWING, 'SMB port or guide missing or out of order');
      render({...status(), copy_unlocked: true, effective: {...status().effective, listen: 'all_networks', ports: {80:80,445:1234}}, active: true});
      assert(!$('copy-steps-go').hidden && $('copy-steps-go').textContent === 'Go to Copy', 'Go to Copy missing once the tabs are open');
      render({...status(), active: true, effective: {...status().effective, listen: 'this_computer', idle: {mode: 'on_demand'}, ports: {80:8080}}});
      assert($('run-summary').textContent === 'Listening on this computer only · Seestar app discovery off · Connect when an app needs Seestar' && $('phone-steps').hidden && $('copy-steps').hidden, 'computer/idle summary inaccurate');
      render({...status(), active: true, effective: {...status().effective, listen: 'other'}});
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
      assert([...rows].map(row => row.cells[1].textContent).join() === '80,445,4350,4361,4554,4555,4700,4800,4801,4804,32323' && rows[10].cells[2].textContent === 'Alpaca (NINA and other astronomy apps)' && rows[1].cells[2].textContent === 'File sharing for other apps (SMB)', 'port rows or order');
      assert([...$('ports').querySelectorAll('input')].every(input => input.type === 'number' && input.min === '1' && input.max === '65535'), 'port number bounds');
      for (const value of ['0', '-1', '65536', '1.5']) {
        $('port-80').value = value;
        assert(!validate() && $('ports-error').hidden && !$('port-80').portError.hidden && $('port-80').getAttribute('aria-invalid') === 'true' && document.activeElement === $('port-80'), 'accepted invalid local ' + value);
        $('port-80').value = '8080';
        $('port-80').dispatchEvent(new Event('input'));
        assert($('port-80').portError.hidden && !$('port-80').hasAttribute('aria-invalid'), 'port error persists after a fix');
      }
      for (const action of ['start','password/save']) {
        $('port-80').value = '1.5';
        let calls = 0;
        api = async () => {calls++; return status();};
        await act(action);
        assert(calls === 0 && fieldErrors.ports === 'ports' && connected && document.activeElement === $('port-80'), action + ' skipped numeric validation');
      }
      // A duplicate local port is refused on the row edited last, unsent.
      $('port-80').value = $('port-4700').value;
      $('port-80').dispatchEvent(new Event('input'));
      let calls = 0;
      api = async () => {calls++; return status();};
      await act('password/save');
      assert(calls === 0 && document.activeElement === $('port-80') && $('port-80').portError.textContent === 'Local port ' + $('port-4700').value + ' is already used by Seestar control.' && $('port-4700').portError.hidden, 'duplicate local port not refused on its row');
      $('port-80').value = '8080';
      api = async () => {throw {code:'ports',field:'ports',message:'Use ports 1–65535, with no duplicates. Leave a port empty to turn off its service.'};};
      await act('password/save');
      assert(connected && fieldErrors.ports === 'ports' && !$('ports-error').hidden, 'server mapping semantics became lost controls');
      $('port-80').value = '';
      assert(validate() && settings().ports.find(p => p.device === 80).local === 0, 'empty local did not disable service');
      assert(settings().ports.length === 11 && settings().ports.every(p => typeof p.local === 'number' && typeof p.device === 'number'), 'ports became text');
    });
    await check('Added ports', async () => {
      $('password').value = 'synthetic-password';
      const input = (e, value) => {e.value = value; e.dispatchEvent(new Event('input'));};
      // shows checks a row's own error, which shows as the ports are edited.
      const shows = (e, message) => assert(e.portError.textContent === message && !e.portError.hidden && e.getAttribute('aria-invalid') === 'true' && e.getAttribute('aria-describedby').split(' ').includes(e.portError.id) && $('ports-error').hidden, 'not shown: ' + message + ' (' + e.portError.textContent + ')');
      const clean = e => assert(e.portError.hidden && !e.hasAttribute('aria-invalid'), 'error stayed: ' + e.portError.textContent);
      assert($('added-ports').hidden && !addedRows().length && $('add-port').textContent === 'Add a port' && $('ports').compareDocumentPosition($('add-port')) & Node.DOCUMENT_POSITION_FOLLOWING, 'Add a port missing below the Services table');
      $('add-port').click();
      let rows = addedRows();
      assert(rows.length === 1 && !$('added-ports').hidden && document.activeElement === rows[0].remote && rows[0].remote.getAttribute('aria-label') === 'Seestar port for added port 1' && rows[0].local.getAttribute('aria-label') === 'Local port for added port 1' && rows[0].row.querySelector('button').getAttribute('aria-label') === 'Remove added port 1', 'Add a port row');
      assert([...$('added-ports').querySelectorAll('th')].map(th => th.textContent).join() === 'Local port,Seestar port,' && rows[0].row.cells[0].contains(rows[0].local) && rows[0].row.cells[1].contains(rows[0].remote), 'added columns not Local, Seestar, Remove');
      clean(rows[0].remote);
      assert(!validate() && document.activeElement === rows[0].local, 'an empty added row was not refused');
      shows(rows[0].local, 'Enter a local port from 1 to 65535.');
      shows(rows[0].remote, 'Enter a Seestar port from 1 to 65535.');
      input(rows[0].remote, '5000');
      assert(rows[0].local.value === '5000' && !rows[0].row.classList.contains('remapped'), 'local port did not start as the Seestar port');
      clean(rows[0].local);
      input(rows[0].local, '5001');
      input(rows[0].remote, '5002');
      assert(rows[0].local.value === '5001' && rows[0].row.classList.contains('remapped'), 'an edited local port followed the Seestar port');
      $('add-port').click();
      rows = addedRows();
      input(rows[1].remote, '445');
      shows(rows[1].remote, 'Seestar port 445 is the File sharing for other apps (SMB) row above. Set its local port there.');
      input(rows[1].remote, '5002');
      shows(rows[1].remote, 'Seestar port 5002 is already added port 1.');
      input(rows[1].remote, '6000');
      clean(rows[1].remote);
      input(rows[1].local, '');
      shows(rows[1].local, 'Enter a local port from 1 to 65535.');
      input(rows[1].local, '5001');
      shows(rows[1].local, 'Local port 5001 is already used by added port 1.');
      clean(rows[0].local);
      input($('port-445'), '4445');
      input(rows[1].local, '4445');
      shows(rows[1].local, 'Local port 4445 is already used by File sharing for other apps (SMB).');
      clean($('port-445'));
      assert($('port-445').closest('tr').classList.contains('remapped'), 'SMB 4445 not shaded');
      // The permissions help shows by a local port below 1024 on Linux only.
      input(rows[1].local, '800');
      assert(validate() && $('port-permissions').hidden && rows[1].local.portLow.hidden === (hostOS !== 'linux'), 'permissions help for 800 on ' + hostOS);
      input(rows[1].local, '6000');
      assert(validate() && rows[1].local.portLow.hidden, 'the permissions help stayed for 6000');
      const sent = settings().ports;
      assert(sent.length === 13 && sent[11].device === 5002 && sent[11].local === 5001 && sent[12].device === 6000 && sent[12].local === 6000, 'added ports not sent in order: ' + JSON.stringify(sent));
      rows[0].row.querySelector('button').click();
      rows = addedRows();
      assert(rows.length === 1 && rows[0].remote.value === '6000' && rows[0].remote.getAttribute('aria-label') === 'Seestar port for added port 1' && document.activeElement === $('add-port'), 'Remove');
      applyGoal('phone');
      assert(addedRows().length === 1, 'a goal change dropped the added ports');
      let body;
      api = async (action, b) => {if (action === 'start') body = b; return status();};
      await act('start');
      assert(body && body.settings.ports.length === 12 && body.settings.ports[11].device === 6000 && body.settings.ports[11].local === 6000, 'Connect did not save the added port');
      load({...setup, ports: [{device: 445, local: 1445}, {device: 7000, local: 7001}]});
      rows = addedRows();
      assert(rows.length === 1 && rows[0].remote.value === '7000' && rows[0].local.value === '7001' && $('port-445').value === '1445', 'saved added port not loaded');
      render({...status(), active: true, effective: {...status().effective, ports: {80: 80, 7000: 7001}}});
      const running = $('running-ports').querySelectorAll('tbody tr');
      assert(running.length === 12 && running[11].cells[0].textContent === '7001' && running[11].cells[1].textContent === '7000' && running[11].cells[2].textContent === 'Added', 'running table lacks the added port');
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
      const event = {time: '2026-01-01T19:16:00Z', message: 'Account telescopes: S30 synthetic-details', code: 'lifecycle', phase: '', reason: ''};
      const effective = {idle: {mode: 'keep_connected', minutes: 0}, listen: 'all_networks', discovery: true, ports: {'80': 80}};
      render({...status(), effective, events: [event], log_path: 'synthetic-private-path'});
      assert($('effective-settings').textContent === JSON.stringify(effective, null, 2), 'Settings is not pretty JSON');
      const localTime = new Date(event.time).toLocaleTimeString([], {hour: 'numeric', minute: '2-digit'});
      assert($('events').children.length === 1 && $('events').children[0].textContent === localTime + '  Connection  Account telescopes: S30 synthetic-details', 'event category or UTC leaked into local list, or the source tag is missing');
      const detailsNodes = ['effective-settings','events','log-path','running-ports','ports'].map(id => $(id));
      const detailsObserver = new MutationObserver(() => {});
      for (const node of detailsNodes) detailsObserver.observe(node, {subtree: true, childList: true, characterData: true, attributes: true});
      render({...status(), effective: JSON.parse(JSON.stringify(effective)), events: [{...event}], log_path: 'synthetic-private-path'});
      assert(detailsObserver.takeRecords().length === 0, 'unchanged poll mutated Details or ports');
      detailsObserver.disconnect();
      const copied = JSON.parse(diagnostic);
      assert(Object.keys(copied).join() === 'app,version,error_code,settings,copy_settings,failures,events,log_path' && copied.app === 'seestar-wormhole' && copied.version === 'synthetic-version' && copied.events[0].time === event.time && copied.events[0].message === event.message && copied.events[0].source === 'connection' && copied.log_path === 'synthetic-private-path' && JSON.stringify(copied.copy_settings) === JSON.stringify(liveCopy?.settings ?? null) && !diagnostic.includes('synthetic@example.invalid') && !$('mappings'), 'copied object schema/privacy');
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
    await check('Activity shows only the Browse trail lines', async () => {
      const line = (source, message) => ({time: '2026-01-01T19:17:00Z', source, message, code: source + '_synthetic', phase: '', reason: ''});
      render({...status(), events: [line('connection', 'Synthetic connection line'), line('copy', 'Synthetic copy trail line'), line('delete', 'Synthetic delete trail line'), line('browse', 'Synthetic browse trail line')]});
      const texts = [...$('events').children].map(li => li.dataset.source + ':' + li.textContent);
      assert(texts.some(t => t.startsWith('browse:') && t.endsWith('  Browse  Synthetic browse trail line')) && texts.some(t => t.startsWith('connection:') && t.includes('Synthetic connection line')), 'a Browse or connection trail line is missing from Activity');
      assert(!texts.some(t => t.includes('Synthetic copy trail line') || t.includes('Synthetic delete trail line')), 'Copy\'s or Free up space\'s trail line repeats the ring in Activity');
      render({...status()});
    });
    await check('Account legend computed alignment', async () => {
      const legend = document.querySelector('#account legend'), style = getComputedStyle(legend);
      const textLeft = legend.getBoundingClientRect().left + parseFloat(style.paddingLeft) + parseFloat(style.borderLeftWidth);
      const inputLeft = $('email').getBoundingClientRect().left;
      assert(parseFloat(style.paddingLeft) === 0 && Math.abs(textLeft - inputLeft) < 1, 'Account text is indented from inputs');
      results.push('Account computed style: padding-left=' + style.paddingLeft + ', text-left=' + textLeft + ', input-left=' + inputLeft);
    });
    await check('Custom telescope fields', async () => {
      $('device-choice').value = 'custom';
      $('device-choice').dispatchEvent(new Event('change'));
      assert(!$('custom-device').hidden && !$('serial').closest('details') && !$('model').closest('details'), 'Custom fields remain in Advanced');
      $('model').value = 'synthetic';
      $('device-choice').value = '';
      $('device-choice').dispatchEvent(new Event('change'));
      assert($('custom-device').hidden && $('serial').value === '' && $('model').value === '', 'Find on my account retained Custom pair');
    });
  } finally {
    window.setTimeout = liveTimeout;
    api = liveAPI;
    pending = '';
    active = live.active;
    lastFailure = '';
    if (typeof actionError !== 'undefined') actionError = '';
    load(live.settings);
    copyStatus = liveCopy;
    clearErrors();
    say('');
    alertText('');
    render(live);
    $('advanced').open = false;
    $('start').focus();
    // The live loop is parked, and synthetic poll calls did not schedule work.
    // Resume exactly one loop with the real API after restoring the page.
    poll();
  }
  window.__guitestPageState = results.join('\n');
})()
