// Tabs, the route choice and the LAN form, against the production markup.
const tabAssert = (ok, message) => {if (!ok) throw new Error(message);};
const keyEvent = key => ({type: 'keydown', key, prevented: false, preventDefault() {this.prevented = true;}});
const selectedTab = () => tabNames.filter(tab => $('tab-' + tab).getAttribute('aria-selected') === 'true');

async function checkTabs() {
  tabAssert(selectedTab().join() === 'connection' && !$('panel-connection').hidden && $('panel-activity').hidden, 'Connection is not the first selected tab');
  tabAssert(['connection', 'copy', 'browse', 'free', 'activity'].every(tab => $('tab-' + tab).getAttribute('role') === 'tab' && $('tab-' + tab).parentElement === $('tabs')), 'tab order or roles');
  tabAssert($('tabs').getAttribute('role') === 'tablist', 'tab bar is not a tablist');
  for (const tab of ['copy', 'browse', 'free']) {
    tabAssert($('tab-' + tab).disabled && $('tab-' + tab).getAttribute('aria-describedby') === 'tabs-note', tab + ' is not a disabled tab with its note');
    selectTab(tab);
    tabAssert(selectedTab().join() === 'connection', tab + ' was selected while disabled');
  }
  tabAssert($('tabs-note').textContent.includes('Coming in this build'), 'disabled tabs lack their note');
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
  for (const id of ['details', 'effective-settings', 'events', 'error-code', 'log-path', 'log-error', 'copy', 'copy-feedback', 'diagnostic-copy']) {
    tabAssert(inside(id, 'panel-activity') && !inside(id, 'panel-connection'), id + ' is not in Activity');
  }
  for (const id of ['announcements', 'status', 'primary-action', 'start', 'notice', 'settings', 'options', 'goal', 'account', 'device-picker', 'advanced', 'running', 'route-choice', 'lan-route']) {
    tabAssert(inside(id, 'panel-connection') && !inside(id, 'panel-activity'), id + ' is not in Connection');
  }
  for (const id of ['quit', 'version', 'connection-text', 'connection-dot', 'tabs']) {
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
    tabAssert($('connection-text').textContent === 'Connecting to your Seestar…' && $('connection-dot').getAttribute('data-tone') === 'busy', 'header line while connecting');
    render({...statuses.account, active: true, status: 'Connected to your Seestar'});
    tabAssert($('connection-text').textContent === 'Connected to your Seestar' && $('connection-dot').getAttribute('data-tone') === 'on', 'header line when connected');
    render(statuses.account);
    tabAssert(!$('route-choice').disabled && $('route-help').hidden, 'the choice stays locked after Stop');
    break;
  }
  case 'copying': {
    routeChoice = 'lan';
    render({...statuses.lan, copy_busy: true});
    tabAssert($('route-choice').disabled && !$('route-help').hidden && $('lan-options').disabled && shown('lan'), 'the choice or LAN form is open while copying');
    render(statuses.lan);
    tabAssert(!$('route-choice').disabled && !$('lan-options').disabled, 'the choice stays locked after copying');
    break;
  }
  default: throw new Error('unknown route scenario ' + scenario);
  }
}

async function checkLANForm(statuses, replies, scenario) {
  routeFixture(statuses);
  $('route-lan').checked = true;
  $('route-lan').dispatchEvent({type: 'change'});
  const calls = [];
  const reply = (base, action, body) => {
    calls.push([base + action, body === undefined ? undefined : JSON.parse(JSON.stringify(body))]);
    if (action === 'status') return statuses.saved;
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
  const lan = statuses.lan.settings.lan_address;
  statuses.saved = statuses.account;
  switch (scenario) {
  case 'test passes': {
    $('lan-address').value = ' 192.0.2.20 ';
    await lanTest();
    tabAssert(JSON.stringify(calls) === JSON.stringify([['/copy/api/test', {address: '192.0.2.20'}]]), 'Test request ' + JSON.stringify(calls));
    tabAssert($('lan-result').textContent === replies['192.0.2.20'].body.message && $('lan-result').getAttribute('data-tone') === 'ok' && $('lan-address-error').hidden, 'Test result');
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
    for (let i = 0; i < 200 && (lanPending || calls.length < 3); i++) await null;
    tabAssert(prevented, 'Save submitted the form');
    tabAssert(JSON.stringify(calls.map(c => c[0])) === JSON.stringify(['/api/route', '/api/status', '/copy/api/test']), 'Save requests ' + JSON.stringify(calls));
    tabAssert(JSON.stringify(calls[0][1]) === JSON.stringify({settings: {route: 'lan', lan_address: '192.0.2.20'}}), 'Save body ' + JSON.stringify(calls[0][1]));
    tabAssert(state.settings.route === 'lan' && $('connection-text').textContent === 'On this network: reachable' && $('connection-dot').getAttribute('data-tone') === 'on', 'header after Save and Test: ' + $('connection-text').textContent);
    tabAssert(lan === replies['192.0.2.20'].body.address, 'fixture: the saved address is the tested one');
    break;
  }
  case 'save after test': {
    $('lan-address').value = '192.0.2.20';
    await lanTest();
    calls.length = 0;
    await lanSave();
    tabAssert(JSON.stringify(calls.map(c => c[0])) === JSON.stringify(['/api/route', '/api/status']), 'a passed Test ran again ' + JSON.stringify(calls));
    tabAssert($('lan-result').textContent === 'Saved. ' + lan + ' is reachable.' && $('lan-result').getAttribute('data-tone') === 'ok', 'Save result');
    break;
  }
  case 'save rejected': {
    replies.save = replies.badSave;
    $('lan-address').value = 'not an address!';
    await lanSave();
    tabAssert(!$('lan-address-error').hidden && $('lan-address-error').textContent === replies.badSave.message && document.activeElement === $('lan-address'), 'rejected Save message');
    tabAssert(JSON.stringify(calls.map(c => c[0])) === JSON.stringify(['/api/route']) && state.settings.route !== 'lan', 'a rejected Save went on');
    replies.save = replies.locked;
    $('lan-address').value = '192.0.2.20';
    await lanSave();
    tabAssert($('lan-result').textContent === replies.locked.message && $('lan-result').getAttribute('data-tone') === 'error' && $('lan-address-error').hidden, 'locked Save message');
    break;
  }
  default: throw new Error('unknown LAN scenario ' + scenario);
  }
}
