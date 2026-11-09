// Tabs, the route choice and the LAN form, against the production markup.
const tabAssert = (ok, message) => {if (!ok) throw new Error(message);};
const keyEvent = key => ({type: 'keydown', key, prevented: false, preventDefault() {this.prevented = true;}});
const selectedTab = () => tabNames.filter(tab => $('tab-' + tab).getAttribute('aria-selected') === 'true');

async function checkTabs() {
  tabAssert(selectedTab().join() === 'connection' && !$('panel-connection').hidden && $('panel-activity').hidden, 'Connection is not the first selected tab');
  tabAssert(['connection', 'copy', 'browse', 'free', 'activity'].every(tab => $('tab-' + tab).getAttribute('role') === 'tab' && $('tab-' + tab).parentElement === $('tabs')), 'tab order or roles');
  tabAssert($('tabs').getAttribute('role') === 'tablist', 'tab bar is not a tablist');
  // The markup starts the roving tabindex: only the selected tab is in the Tab order.
  tabAssert($('tab-connection').getAttribute('tabindex') === '0' && tabNames.filter(tab => tab !== 'connection').every(tab => $('tab-' + tab).getAttribute('tabindex') === '-1'), 'the markup does not start the roving tabindex');
  for (const tab of ['copy', 'browse', 'free']) {
    tabAssert($('tab-' + tab).disabled && $('tab-' + tab).getAttribute('aria-describedby') === 'tabs-note', tab + ' is not a disabled tab with its note');
    selectTab(tab);
    tabAssert(selectedTab().join() === 'connection', tab + ' was selected while disabled');
  }
  tabAssert($('tabs-note').textContent.includes('open once you connect') && !$('tabs-note').hidden, 'disabled tabs lack their note');
  const keys = [['ArrowRight', 'activity'], ['ArrowRight', 'connection'], ['ArrowLeft', 'activity'], ['Home', 'connection'], ['End', 'activity']];
  for (const [key, want] of keys) {
    const event = keyEvent(key);
    $('tabs').dispatchEvent(event);
    tabAssert(event.prevented && selectedTab().join() === want && document.activeElement === $('tab-' + want), key + ' did not move to ' + want);
    tabAssert($('panel-' + want).hidden === false && $('panel-' + (want === 'activity' ? 'connection' : 'activity')).hidden, key + ' left the wrong panel shown');
    tabAssert($('tab-' + want).tabIndex === 0 && tabNames.filter(tab => tab !== want).every(tab => $('tab-' + tab).tabIndex === -1), key + ' roving tabindex');
  }
  const other = keyEvent('a');
  $('tabs').dispatchEvent(other);
  tabAssert(!other.prevented && selectedTab().join() === 'activity', 'another key changed the tab');
  $('tab-connection').dispatchEvent({type: 'click'});
  tabAssert(selectedTab().join() === 'connection' && !$('panel-connection').hidden, 'click did not select Connection');
}

// Details and logs live in Activity, with every id kept; Connection keeps
// the whole account page.
async function checkActivityMove() {
  const inside = (id, panel) => {
    for (let e = $(id); e; e = e.parentElement) if (e === $(panel)) return true;
    return false;
  };
  for (const id of ['details', 'effective-settings', 'copy-settings', 'events', 'error-code', 'log-path', 'log-error', 'config-file', 'copy-file', 'copy', 'copy-feedback', 'diagnostic-copy']) {
    tabAssert(inside(id, 'panel-activity') && !inside(id, 'panel-connection'), id + ' is not in Activity');
  }
  for (const id of ['announcements', 'status', 'primary-action', 'start', 'notice', 'settings', 'options', 'goal', 'account', 'device-picker', 'advanced', 'running', 'route-choice', 'lan-route']) {
    tabAssert(inside(id, 'panel-connection') && !inside(id, 'panel-activity'), id + ' is not in Connection');
  }
  for (const [panel, ids] of [['panel-copy', ['c-main', 's-box', 'copy-form', 'h-details']], ['panel-browse', ['b-frame']], ['panel-free', ['x-box', 'x-find']]]) {
    for (const id of ids) tabAssert(inside(id, panel) && !inside(id, 'panel-connection') && !inside(id, 'panel-activity'), id + ' is not in ' + panel);
  }
  for (const id of ['quit', 'version', 'connection-text', 'connection-dot', 'copy-chip', 'preview-chip', 'free-chip', 'chip-live', 'tabs', 'copy-reach', 'lost', 'last-known', 'x-confirm', 'h-confirm']) {
    tabAssert(!inside(id, 'panel-connection') && !inside(id, 'panel-activity'), id + ' is not on every tab');
  }
  tabAssert($('details').attributes.open !== undefined, 'Activity opens with its details collapsed');
  tabAssert($('details').parentElement === $('panel-activity'), 'Details is not the Activity panel');
}

function routeFixture(statuses) {
  initialized = connected = true;
  pending = lanPending = '';
  lanReach = null;
  load(statuses.account.settings);
  render(statuses.account);
}

async function checkRouteChoice(statuses, scenario) {
  routeFixture(statuses);
  const shown = route => $('account-route').hidden === (route !== 'account') && $('lan-route').hidden === (route !== 'lan') &&
    $('route-account').checked === (route === 'account') && $('route-lan').checked === (route === 'lan');
  switch (scenario) {
  case 'old file': {
    const old = {...statuses.account.settings};
    delete old.route;
    delete old.lan_address;
    const status = {...statuses.account, settings: old};
    delete status.copy_busy;
    load(old);
    render(status);
    tabAssert(routeChoice === 'account' && shown('account') && !$('route-choice').disabled, 'an old file is not the account route');
    tabAssert($('connection-text').textContent === 'Not connected' && $('connection-dot').getAttribute('data-tone') === 'off', 'header line for no connection');
    tabAssert($('announcements').parentElement === $('account-route') && $('primary-action').parentElement === $('account-route'), 'account content moved');
    break;
  }
  case 'choose': {
    $('route-lan').checked = true;
    $('route-lan').dispatchEvent({type: 'change'});
    tabAssert(routeChoice === 'lan' && shown('lan') && !$('lan-options').disabled, 'choosing On this network');
    render(statuses.account);
    tabAssert(shown('lan'), 'a poll undid the choice');
    $('route-account').checked = true;
    $('route-account').dispatchEvent({type: 'change'});
    tabAssert(routeChoice === 'account' && shown('account'), 'choosing the account route');
    break;
  }
  case 'saved': {
    load(statuses.lan.settings);
    render(statuses.lan);
    tabAssert(routeChoice === 'lan' && shown('lan') && $('lan-address').value === statuses.lan.settings.lan_address, 'a saved LAN route reopens');
    tabAssert($('connection-text').textContent === 'On this network: ' + statuses.lan.settings.lan_address, 'header line for an untested LAN route');
    break;
  }
  case 'active': {
    routeChoice = 'lan';
    render({...statuses.account, active: true, cancel: true, status: 'Connecting to your Seestar…'});
    tabAssert($('route-choice').disabled && !$('route-help').hidden && shown('account') && $('lan-options').disabled, 'the choice is open while the account route runs');
    tabAssert($('lan-when').disabled, 'when to scan is open while the account route runs');
    tabAssert($('connection-text').textContent === 'Connecting to your Seestar…' && $('connection-dot').getAttribute('data-tone') === 'busy', 'header line while connecting');
    render({...statuses.account, active: true, ready: true, status: 'Connected to your Seestar'});
    tabAssert($('connection-text').textContent === 'Connected to your Seestar' && $('connection-dot').getAttribute('data-tone') === 'on', 'header line when connected');
    // Once a run has been ready, a retry, a busy telescope or an idle
    // release is busy, not green.
    for (const status of ['Connection to your Seestar interrupted. Retrying automatically.', 'Your Seestar is occupied. Retrying automatically.', 'Releasing idle session with your Seestar…']) {
      render({...statuses.account, active: true, cancel: false, ready: false, status});
      tabAssert($('connection-text').textContent === status && $('connection-dot').getAttribute('data-tone') === 'busy' && $('start').textContent === 'Stop', 'header line for ' + status + ': ' + $('connection-dot').getAttribute('data-tone'));
    }
    render(statuses.account);
    tabAssert(!$('route-choice').disabled && $('route-help').hidden, 'the choice stays locked after Stop');
    break;
  }
  case 'copying': {
    routeChoice = 'lan';
    render({...statuses.lan, copy_busy: true});
    tabAssert($('route-choice').disabled && !$('route-help').hidden && $('lan-options').disabled && shown('lan'), 'the choice or LAN form is open while copying');
    // When to scan stays open: copying takes it at its next connection.
    for (let e = $('lan-when'); e; e = e.parentElement) tabAssert(!e.disabled, 'when to scan is disabled while copying by ' + e.id);
    render(statuses.lan);
    tabAssert(!$('route-choice').disabled && !$('lan-options').disabled, 'the choice stays locked after copying');
    break;
  }
  default: throw new Error('unknown route scenario ' + scenario);
  }
}

async function checkLANForm(statuses, replies, scenario) {
  routeFixture(statuses);
  const calls = [];
  // The server's own scan answers: by typed address, or "" for a scan of
  // the whole network. An address with none got no answer.
  let scans = replies.scans.list;
  const reply = (base, action, body) => {
    calls.push([base + action, body === undefined ? undefined : JSON.parse(JSON.stringify(body))]);
    // Copy's status is the server's own, read after the LAN route is saved.
    if (base === '/copy/api/' && action === 'status') return statuses.copy;
    if (action === 'status') return statuses.saved;
    if (base === '/api/' && action === 'scan') {
      const r = scans[body.address || ''] || {status: 200, body: {ok: true, telescopes: [], replies: 0, blocked: false, message: ''}};
      if (r.status !== 200) throw r.body;
      return r.body;
    }
    if (base === '/copy/api/' && action === 'test') {
      const r = replies[body.address];
      if (r.status !== 200) throw r.body;
      return r.body;
    }
    if (action === 'route') {
      if (replies.save) throw replies.save;
      statuses.saved = statuses.lan;
      return {ok: true};
    }
    throw new Error('unexpected request ' + base + action);
  };
  api = async (action, body, base = '/api/') => reply(base, action, body);
  const settle = async () => {
    for (let i = 0; i < 200; i++) await null;
  };
  // Opening On this network with nothing saved there scans by itself.
  $('route-lan').checked = true;
  $('route-lan').dispatchEvent({type: 'change'});
  await settle();
  tabAssert(JSON.stringify(calls) === JSON.stringify([['/api/scan', {}]]), 'opening On this network did not scan once: ' + JSON.stringify(calls));
  calls.length = 0;
  const lan = statuses.lan.settings.lan_address;
  statuses.saved = statuses.account;
  switch (scenario) {
  case 'test passes': {
    scans = replies.scans.silent;
    $('lan-address').value = ' 192.0.2.20 ';
    await lanTest();
    tabAssert(JSON.stringify(calls) === JSON.stringify([['/api/scan', {address: '192.0.2.20'}], ['/copy/api/test', {address: '192.0.2.20'}]]), 'Test request ' + JSON.stringify(calls));
    // The saved route is Telescope Network, so the Test says to Save.
    tabAssert($('lan-result').textContent === replies['192.0.2.20'].body.message + ' Save to use this address for Copy, Browse and Free up space.' && $('lan-result').getAttribute('data-tone') === 'ok' && $('lan-address-error').hidden, 'Test result: ' + $('lan-result').textContent);
    tabAssert(!$('lan-options').disabled && $('lan-test').textContent === 'Test', 'Test left the form locked');
    break;
  }
  case 'test fails': {
    $('lan-address').value = '192.0.2.99';
    await lanTest();
    tabAssert($('lan-result').textContent === replies['192.0.2.99'].body.message && $('lan-result').getAttribute('data-tone') === 'error', 'failed Test result');
    break;
  }
  case 'test bad address': {
    $('lan-address').value = 'not an address!';
    await lanTest();
    tabAssert(!$('lan-address-error').hidden && $('lan-address-error').textContent === replies['not an address!'].body.fields.address, 'bad address message');
    tabAssert($('lan-address').getAttribute('aria-invalid') === 'true' && document.activeElement === $('lan-address') && $('lan-result').textContent === '', 'bad address focus');
    $('lan-address').dispatchEvent({type: 'input'});
    tabAssert($('lan-address-error').hidden && $('lan-address').getAttribute('aria-invalid') === null, 'typing kept the error');
    break;
  }
  case 'save': {
    $('lan-address').value = '192.0.2.20';
    let prevented = false;
    $('lan-form').dispatchEvent({type: 'submit', preventDefault() {prevented = true;}});
    for (let i = 0; i < 200 && (lanPending || calls.length < 5); i++) await null;
    tabAssert(prevented, 'Save submitted the form');
    tabAssert(JSON.stringify(calls.map(c => c[0])) === JSON.stringify(['/api/scan', '/copy/api/test', '/api/route', '/api/status', '/copy/api/status']), 'Save requests ' + JSON.stringify(calls));
    tabAssert(JSON.stringify(calls[2][1]) === JSON.stringify({settings: {route: 'lan', lan_address: replies['192.0.2.20'].body.address}}), 'Save body ' + JSON.stringify(calls[1][1]));
    tabAssert(state.settings.route === 'lan' && $('connection-text').textContent === 'On this network: reachable' && $('connection-dot').getAttribute('data-tone') === 'on', 'header after Test and Save: ' + $('connection-text').textContent);
    tabAssert($('lan-result').textContent === 'Saved. ' + lan + ' is reachable.' && $('lan-result').getAttribute('data-tone') === 'ok', 'Save result');
    tabAssert(lan === replies['192.0.2.20'].body.address, 'fixture: the saved address is the tested one');
    // Copy shows the saved telescope, and its token, by Saved.
    tabAssert(copyStatus === statuses.copy && statuses.copy.telescope?.token, 'Copy status after Save ' + JSON.stringify(copyStatus?.telescope));
    break;
  }
  case 'save after test': {
    $('lan-address').value = '192.0.2.20';
    await lanTest();
    calls.length = 0;
    await lanSave();
    tabAssert(JSON.stringify(calls.map(c => c[0])) === JSON.stringify(['/api/route', '/api/status', '/copy/api/status']), 'a passed Test ran again ' + JSON.stringify(calls));
    tabAssert($('lan-result').textContent === 'Saved. ' + lan + ' is reachable.' && $('lan-result').getAttribute('data-tone') === 'ok', 'Save result');
    // An edited address is Tested again before it is saved.
    $('lan-address').value = '192.0.2.99';
    calls.length = 0;
    await lanSave();
    tabAssert(JSON.stringify(calls.map(c => c[0])) === JSON.stringify(['/api/scan', '/copy/api/test']), 'an edited address was saved untested ' + JSON.stringify(calls));
    break;
  }
  case 'save unreachable': {
    $('lan-address').value = '192.0.2.99';
    await lanSave();
    tabAssert(JSON.stringify(calls.map(c => c[0])) === JSON.stringify(['/api/scan', '/copy/api/test']) && state.settings.route !== 'lan', 'an unreachable address was saved ' + JSON.stringify(calls));
    tabAssert($('lan-result').textContent === replies['192.0.2.99'].body.message && $('lan-result').getAttribute('data-tone') === 'error', 'unreachable Save result');
    break;
  }
  case 'save rejected': {
    $('lan-address').value = 'not an address!';
    await lanSave();
    tabAssert(!$('lan-address-error').hidden && $('lan-address-error').textContent === replies['not an address!'].body.fields.address && document.activeElement === $('lan-address'), 'rejected Save message');
    tabAssert(JSON.stringify(calls.map(c => c[0])) === JSON.stringify(['/api/scan', '/copy/api/test']) && state.settings.route !== 'lan', 'a rejected Save went on');
    // The server's own Test and its route lock refuse a Save the page's
    // Test passed.
    replies.save = replies.unreachable;
    $('lan-address').value = '192.0.2.20';
    $('lan-address').dispatchEvent({type: 'input'});
    await lanSave();
    tabAssert(!$('lan-address-error').hidden && $('lan-address-error').textContent === replies.unreachable.message && document.activeElement === $('lan-address') && state.settings.route !== 'lan', 'server-refused Save message');
    replies.save = replies.locked;
    $('lan-address').dispatchEvent({type: 'input'});
    await lanSave();
    tabAssert($('lan-result').textContent === replies.locked.message && $('lan-result').getAttribute('data-tone') === 'error' && $('lan-address-error').hidden, 'locked Save message');
    break;
  }
  case 'scan lists': {
    // The auto-scan listed both telescopes, without serials, each address
    // a button that fills it in.
    const found = $('lan-found').children;
    tabAssert(found.length === 2 && $('lan-scan-result').textContent === 'Found 2 telescopes. Pick an address to fill it in.' && $('lan-scan').textContent === 'Scan again' && !$('lan-scan').disabled, 'scan list: ' + found.length + ' ' + $('lan-scan-result').textContent);
    const row = found.find(li => li.children[0].textContent.startsWith('S50_A'));
    tabAssert(row && row.children[0].textContent === 'S50_A (Seestar S50)', 'row text ' + row?.children[0].textContent);
    const pick = row.children[2];
    tabAssert(pick.textContent === '192.0.2.20' && pick.attributes['aria-label'] === 'Use 192.0.2.20 for S50_A', 'address button');
    pick.dispatchEvent({type: 'click'});
    tabAssert($('lan-address').value === '192.0.2.20' && $('lan-result').textContent === 'Picked S50_A at 192.0.2.20. Test or Save to use it.' && document.activeElement === $('lan-address'), 'picking a row');
    tabAssert(!JSON.stringify(replies.scans.list).includes('SERIAL-'), 'the fixture carries a serial');
    // Scan again asks again; a blocked scan says Local Network is off.
    scans = replies.scans.blocked;
    $('lan-scan').dispatchEvent({type: 'click'});
    await settle();
    tabAssert(JSON.stringify(calls.map(c => c[0])) === JSON.stringify(['/api/scan']) && $('lan-found').children.length === 0, 'Scan again ' + JSON.stringify(calls));
    tabAssert($('lan-scan-result').textContent === replies.scans.blocked[''].body.message && $('lan-scan-result').getAttribute('data-tone') === 'error' && $('lan-scan-result').textContent.includes('System Settings'), 'blocked message ' + $('lan-scan-result').textContent);
    scans = replies.scans.silent;
    $('lan-scan').dispatchEvent({type: 'click'});
    await settle();
    tabAssert($('lan-scan-result').textContent.startsWith('No telescope answered.') && $('lan-scan-result').getAttribute('data-tone') === null, 'silence message ' + $('lan-scan-result').textContent);
    // Leaving and opening On this network again scans again.
    calls.length = 0;
    $('route-account').checked = true;
    $('route-account').dispatchEvent({type: 'change'});
    $('route-lan').checked = true;
    $('route-lan').dispatchEvent({type: 'change'});
    await settle();
    tabAssert(JSON.stringify(calls) === JSON.stringify([['/api/scan', {}]]), 'reopening did not scan ' + JSON.stringify(calls));
    break;
  }
  case 'test names': {
    // Test asks the typed address too and names what answered there.
    $('lan-address').value = '192.0.2.20';
    await lanTest();
    tabAssert($('lan-result').textContent === replies['192.0.2.20'].body.message + ' It answered as S50_A (Seestar S50). Save to use this address for Copy, Browse and Free up space.', 'Test result: ' + $('lan-result').textContent);
    break;
  }
  case 'saved route': {
    // A saved LAN route reopens without scanning.
    calls.length = 0;
    lanShown = false;
    load(statuses.lan.settings);
    render(statuses.lan);
    await settle();
    tabAssert(routeChoice === 'lan' && calls.length === 0 && $('lan-scan').textContent === 'Scan again', 'a saved LAN route scanned ' + JSON.stringify(calls));
    break;
  }
  default: throw new Error('unknown LAN scenario ' + scenario);
  }
}

// When to scan: the saved choice shows in the form, choosing saves it,
// a refused choice goes back, and Never keeps the page from scanning or
// probing unasked.
async function checkLANWhen(statuses, replies, scenario) {
  routeFixture(statuses);
  const calls = [];
  let refuse = null;
  let reply = (base, action, body) => {
    calls.push([base + action, body === undefined ? undefined : JSON.parse(JSON.stringify(body))]);
    if (action === 'status') return statuses.saved;
    if (base === '/api/' && action === 'scan') return replies.scans.list[body.address || ''] ? replies.scans.list[body.address || ''].body : {ok: true, telescopes: [], replies: 0, blocked: false, message: ''};
    if (base === '/copy/api/' && action === 'test') return replies[body.address].body;
    if (action === 'lan-scan') {
      if (refuse) throw refuse;
      statuses.saved = statuses[body.settings.lan_scan];
      return {ok: true};
    }
    throw new Error('unexpected request ' + base + action);
  };
  api = async (action, body, base = '/api/') => reply(base, action, body);
  const settle = async () => {
    for (let i = 0; i < 200; i++) await null;
  };
  const open = async () => {
    $('route-lan').checked = true;
    $('route-lan').dispatchEvent({type: 'change'});
    await settle();
  };
  const choose = async (when) => {
    $('lan-when').value = when;
    $('lan-when').dispatchEvent({type: 'change'});
    for (let i = 0; i < 200 && (lanPending || !calls.length); i++) await null;
  };
  switch (scenario) {
  case 'off': {
    // Never: opening the form doesn't scan, and Test doesn't probe.
    load(statuses.off.settings);
    render(statuses.off);
    tabAssert($('lan-when').value === 'off', 'the form shows ' + $('lan-when').value);
    await open();
    tabAssert(calls.length === 0 && $('lan-found').children.length === 0, 'Never scanned on opening ' + JSON.stringify(calls));
    $('lan-address').value = '192.0.2.20';
    await lanTest();
    tabAssert(JSON.stringify(calls) === JSON.stringify([['/copy/api/test', {address: '192.0.2.20'}]]), 'Never probed on Test ' + JSON.stringify(calls));
    tabAssert($('lan-result').textContent === replies['192.0.2.20'].body.message + ' Save to use this address for Copy, Browse and Free up space.', 'Test result ' + $('lan-result').textContent);
    // Scan, pressed, scans.
    calls.length = 0;
    $('lan-scan').dispatchEvent({type: 'click'});
    await settle();
    tabAssert(JSON.stringify(calls) === JSON.stringify([['/api/scan', {}]]) && $('lan-found').children.length === 2, 'Scan under Never ' + JSON.stringify(calls));
    break;
  }
  case 'choose': {
    statuses.saved = statuses.account;
    tabAssert($('lan-when').value === 'start', 'the default shows ' + $('lan-when').value);
    await open();
    tabAssert(JSON.stringify(calls) === JSON.stringify([['/api/scan', {}]]), 'the default did not scan on opening ' + JSON.stringify(calls));
    // As in a browser, disabling the focused select leaves focus on the
    // page's body.
    const select = $('lan-when');
    document.body = new TestElement();
    let disabled = select.disabled;
    Object.defineProperty(select, 'disabled', {get() {return disabled;}, set(v) {
      disabled = v;
      if (v && document.activeElement === select) document.activeElement = document.body;
    }});
    select.focus();
    calls.length = 0;
    await choose('off');
    tabAssert(JSON.stringify(calls) === JSON.stringify([['/api/lan-scan', {settings: {lan_scan: 'off'}}], ['/api/status', undefined]]), 'choosing Never ' + JSON.stringify(calls));
    tabAssert(state.settings.lan_scan === 'off' && $('lan-when').value === 'off' && $('lan-when-result').textContent === 'Saved.' && !$('lan-options').disabled, 'after choosing Never: ' + $('lan-when').value + ' ' + $('lan-when-result').textContent);
    tabAssert(document.activeElement === select, 'saving the choice left focus off it');
    // Now leaving and opening the form again doesn't scan.
    calls.length = 0;
    $('route-account').checked = true;
    $('route-account').dispatchEvent({type: 'change'});
    await open();
    tabAssert(calls.length === 0, 'reopening under Never scanned ' + JSON.stringify(calls));
    // A refused choice goes back to the saved one and says why.
    refuse = replies.locked;
    await choose('connect');
    tabAssert($('lan-when').value === 'off' && $('lan-when-result').textContent === replies.locked.message && $('lan-when-result').getAttribute('data-tone') === 'error', 'a refused choice: ' + $('lan-when').value + ' ' + $('lan-when-result').textContent);
    tabAssert(document.activeElement === select, 'a refused choice left focus off it');
    refuse = null;
    calls.length = 0;
    // A control focused while the choice saves keeps focus.
    const saving = reply;
    reply = (base, action, body) => {
      if (action === 'lan-scan') $('lan-address').focus();
      return saving(base, action, body);
    };
    await choose('connect');
    tabAssert(JSON.stringify(calls[0]) === JSON.stringify(['/api/lan-scan', {settings: {lan_scan: 'connect'}}]) && $('lan-when').value === 'connect', 'choosing every connection ' + JSON.stringify(calls));
    tabAssert(document.activeElement === $('lan-address'), 'saving the choice took focus from the address');
    break;
  }
  case 'newer': {
    // A value this page doesn't know shows, and acts, as the default.
    const newer = {...statuses.account, settings: {...statuses.account.settings, lan_scan: 'a newer mode'}};
    load(newer.settings);
    render(newer);
    tabAssert($('lan-when').value === 'start', 'a newer value shows ' + $('lan-when').value);
    await open();
    tabAssert(JSON.stringify(calls) === JSON.stringify([['/api/scan', {}]]), 'a newer value did not scan on opening ' + JSON.stringify(calls));
    break;
  }
  default: throw new Error('unknown when scenario ' + scenario);
  }
}

// A refused copy.json keeps Copy, Browse and Free up space off with its
// own note, naming the file, in place of the usual one.
async function checkCopyRefused(status) {
  initialized = connected = true;
  load(status.settings);
  render(status);
  tabAssert(!$('copy-refused').hidden && $('tabs-note').hidden, 'the refused note does not replace the usual one');
  tabAssert($('copy-refused').textContent.includes("the saved Copy settings couldn't be loaded safely") && $('copy-refused-path').textContent === status.files.copy, 'the refused note does not name the file');
  for (const tab of ['copy', 'browse', 'free']) {
    tabAssert($('tab-' + tab).disabled && $('tab-' + tab).getAttribute('aria-describedby') === 'copy-refused', tab + ' is not a disabled tab with the refused note');
  }
  render({...status, copy_refused: false});
  tabAssert($('copy-refused').hidden && !$('tabs-note').hidden && $('tab-copy').getAttribute('aria-describedby') === 'tabs-note', 'the usual note did not come back');
}
