'use strict';
const $ = id => document.getElementById(id);
const tokenKey = 'seestar-control-token';
let token = location.hash.slice(1);
if (token) {
  try {
    sessionStorage.setItem(tokenKey, token);
  } catch (_) {
  }
  history.replaceState(null, '', '/');
} else {
  try {
    token = sessionStorage.getItem(tokenKey) || '';
  } catch (_) {
  }
}
let initialized = false, ended = false, connected = false, pending = '', active = false;
let lastFailure = '', state = null, statusGeneration = 0, actionError = '';
let fieldErrors = {}, portDefaults = [], draftEmail = '';
let currentGoal = 'phone', retryRemoval = false, diagnostic = '', noticeCode = '';
// The route shown on Connection, a LAN action in flight, the last LAN Test,
// and the selected tab.
let routeChoice = 'account', lanPending = '', lanReach = null, currentTab = 'connection';
// The LAN scan: whether one runs, whether one has run, what it found, and
// whether the On this network panel was shown or just opened by the user.
let lanScanning = false, lanScanned = false, lanFound = [], lanShown = false, lanOpened = false;
const tabNames = ['connection', 'copy', 'browse', 'free', 'activity'];
const lostCopy = 'Connection to the controls was lost. The proxy may still be running. Reopen the app to reconnect.';
const copyLostCopy = "Couldn't read the status of Copy and Free up space. They show their last known state until it can be read again.";
// controlsLost: the controls' status poll or an action found the server
// gone. copyLost: copy's status poll failed. copyOld: the controls were lost
// and copy's status has not been read since. Each way the copy tabs show
// their last known state, marked stale, with their actions off.
let controlsLost = false, copyLost = false, copyOld = false;
const fieldCopy = {
  email: 'Enter your Seestar account email.', password: 'Enter a password of 8 to 32 characters, or use a saved password.',
  idle: 'Enter a wait time greater than zero.'
};
async function api(action, body, base = '/api/') {
  const options = {
    headers: {
      'X-Seestar-Token': token
    }, cache: 'no-store', redirect: 'error'
  };
  if (body !== undefined) {
    options.method = 'POST';
    options.headers['Content-Type'] = 'application/json';
    options.body = JSON.stringify(body);
  }
  const response = await fetch(base + action, options);
  if (!response.ok) {
    let detail;
    try {
      detail = await response.json();
    } catch (_) {
      throw {
        code: 'controls', message: lostCopy
      };
    }
    throw detail;
  }
  return response.json();
}
function accountEmail(value = $('email').value) {
  return value.replace(/ /g, '').trim();
}
function settings() {
  return {
    version: 1, bind: $('listen').value === 'other' ? $('bind').value.trim() : $('listen').value,
    discovery: $('discovery').checked, idle: $('mode').value === 'eager' ? '0' : Number($('idle').value) + 'm',
    serial: $('serial').value, model: $('model').value, ports: services.map(([device]) => ({device, local: Number($('port-' + device).value)})),
    email: accountEmail(), remember_password: usingSaved()
  };
}
const services = [
  [4700, 'Seestar control'], [80, 'Image downloads (web)'],
  [4800, 'Main camera images'], [4804, 'Wide camera images'],
  [4801, 'Album and files'], [4554, 'Main camera video'],
  [4555, 'Wide camera video (some models)'], [4350, 'Firmware updater'],
  [4361, 'Firmware upload'], [445, 'File sharing for other apps (SMB)']
];
function portValues(s) {
  const ports = Object.fromEntries(portDefaults.map(p => [p.device, p.local]));
  for (const p of s.ports || []) ports[p.device] = p.local;
  return ports;
}
function portsTable(id, ports, editable = false) {
  const key = JSON.stringify(services.map(([remote]) => ports[remote] || 'Off'));
  if (!editable && $(id).portsKey === key) return;
  const table = document.createElement('table');
  const head = table.createTHead().insertRow();
  for (const name of ['Local port', 'Seestar port', 'Purpose']) {
    const th = document.createElement('th');
    th.textContent = name;
    head.append(th);
  }
  const body = table.createTBody();
  for (const [remote, purpose] of services) {
    const row = body.insertRow(), local = row.insertCell();
    if (editable) {
      const input = document.createElement('input');
      input.id = 'port-' + remote;
      input.type = 'number';
      input.min = '1';
      input.max = '65535';
      input.step = '1';
      input.setAttribute('aria-label', 'Local port for ' + purpose);
      input.setAttribute('aria-describedby', 'ports-help ports-error');
      input.value = ports[remote] || '';
      input.addEventListener('input', () => {clearField('ports'); customGoal();});
      local.append(input);
    } else local.textContent = ports[remote] || 'Off';
    row.insertCell().textContent = remote;
    row.insertCell().textContent = purpose;
  }
  $(id).replaceChildren(table);
  if (!editable) $(id).portsKey = key;
}
function goalFor(s) {
  const ports = portValues(s);
  const standard = services.every(([remote]) => ports[remote] === (remote === 80 && s.discovery ? 80 : portValues({})[remote]));
  if (standard && s.bind === '0.0.0.0' && s.discovery) return 'phone';
  if (standard && s.bind === '127.0.0.1' && !s.discovery) return 'computer';
  return 'custom';
}
function load(s) {
  for (const name of ['email', 'bind', 'serial', 'model']) $(name).value = s[name] || '';
  draftEmail = accountEmail();
  $('listen').value = ['0.0.0.0', '127.0.0.1'].includes(s.bind) ? s.bind : 'other';
  $('discovery').checked = !!s.discovery;
  $('mode').value = s.idle_minutes > 0 ? 'idle' : 'eager';
  // The server projects parsed duration to minutes, including old compound durations.
  $('idle').value = s.idle_minutes || 2;
  portsTable('ports', portValues(s), true);
  currentGoal = goalFor(s);
  $('goal').value = currentGoal;
  // An older file has no route: the Telescope Network route.
  routeChoice = s.route === 'lan' ? 'lan' : 'account';
  $('lan-address').value = s.lan_address || '';
}
// The saved association belongs to this exact account; secrets stay in Go.
function passwordState() {
  return state?.settings.email === accountEmail() ? state.password : {};
}
function usingSaved() {
  return !!passwordState().saved;
}
function clearField(field) {
  $(field).removeAttribute('aria-invalid');
  if (field === 'ports') for (const [remote] of services) $('port-' + remote)?.removeAttribute('aria-invalid');
  if (field === 'bind') $('listen').removeAttribute('aria-invalid');
  $(field + '-error').hidden = true;
  delete fieldErrors[field];
}
// text sets an element's text only when it changed: WebKit drops a click
// whose press and release straddle a rewrite of the button's text, and the
// status poll renders every second.
function text(id,value) {
  if ($(id).textContent !== value) $(id).textContent = value;
}
function say(value) {
  text('feedback',value);
}
function alertText(value) {
  text('notice',value);
  $('notice').hidden = !value;
}
function clearErrors() {
  $('listen').removeAttribute('aria-invalid');
  for (const id of ['email','password','idle','bind','discovery','ports','serial','model']) {
    clearField(id);
  }
}
function fieldError(field, message, code, focus = false) {
  const e = $(field), help = $(field + '-error');
  if (!e || !help) return;
  if (['serial','model'].includes(field)) {
    $('device-choice').value = 'custom';
    $('custom-device').hidden = false;
  }
  if (e.closest('details')) $('advanced').open = true;
  const target = field === 'bind' && $('listen').value !== 'other' ? $('listen') : e;
  target.setAttribute('aria-invalid','true');
  fieldErrors[field] = code;
  text(field + '-error', message);
  help.hidden = false;
  if (focus) {
    controls();
    target.focus();
  }
}
function fail(e, focus = false) {
  const code = e.code || 'controls';
  actionError = code;
  // A lost server is said once, above every tab.
  alertText(e.field || code === 'controls' ? '' : (e.message || fieldCopy[code] || lostCopy));
  if (e.field) fieldError(e.field, e.message || fieldCopy[code] || 'Check this setting.', code, focus);
  if (code === 'password_remove') {
    retryRemoval = true;
  }
  if (code === 'controls') {
    connected = false;
    controlsLost = copyOld = true;
  }
}
function validate() {
  clearErrors();
  $('listen').removeAttribute('aria-invalid');
  if (!accountEmail()) {
    actionError = 'email';
    fieldError('email',fieldCopy.email,'email',true);
    return false;
  }
  // A string's length counts UTF-16 units, as the account's rule does.
  const typed = $('password').value.length;
  if (!usingSaved() && (typed ? typed < 8 || typed > 32 : !passwordState().memory)) {
    actionError = 'password';
    fieldError('password',fieldCopy.password,'password',true);
    return false;
  }
  if ($('listen').value === 'other' && !$('bind').value.trim()) {
    actionError = 'bind_empty';
    fieldError('bind', 'Enter a local network address, or choose This computer only or All networks.', 'bind_empty', true);
    return false;
  }
  for (const [remote] of services) {
    const input = $('port-' + remote);
    if (!input.validity.valid) {
      actionError = 'ports';
      fieldError('ports', input.validationMessage, 'ports');
      input.setAttribute('aria-invalid', 'true');
      input.focus();
      return false;
    }
  }
  if ($('mode').value === 'idle' && !(Number($('idle').value) > 0)) {
    actionError = 'idle';
    fieldError('idle',fieldCopy.idle,'idle',true);
    return false;
  }
  return true;
}
function controls() {
  const blocked = !initialized || !connected || ended;
  $('options').disabled = blocked || active || !!pending;
  $('settings').hidden = active;
  $('running').hidden = !active;
  $('start').type = active ? 'button' : 'submit';
  text('start', state?.stopping || pending === 'stop' ? 'Stopping…' : active ? (state?.cancel ? 'Cancel' : 'Stop') : 'Connect');
  $('start').disabled = blocked || state?.stopping || pending === 'stop' || pending === 'quit' || (!active && !!pending);
  $('quit').disabled = !initialized || ended || pending === 'quit';
  $('idle-options').hidden = $('mode').value !== 'idle';
  $('idle').disabled = $('mode').value !== 'idle';
  $('other-address').hidden = $('listen').value !== 'other';
  const selectedSaved = usingSaved();
  const password = passwordState();
  if (!selectedSaved && $('password').disabled) $('password').value = '';
  $('password').disabled = selectedSaved;
  $('password').type = selectedSaved ? 'text' : 'password';
  if (selectedSaved) $('password').value = 'Saved in keychain';
  $('save-password').hidden = selectedSaved;
  $('save-password').disabled = blocked || !!pending || password.busy || !accountEmail() || (!$('password').value && !password.memory);
  text('password-note', selectedSaved ? 'Saved in your keychain.' : 'Kept until you quit. Save to your keychain to use it next time.');
  $('forget').hidden = !password.saved;
  $('forget').disabled = blocked || !!pending || password.busy;
  text('forget', pending === 'forget' ? 'Removing saved password…' : retryRemoval ? 'Retry removal' : 'Remove saved password');
  $('retry-storage').disabled = blocked || !!pending;
  $('custom-help').hidden = currentGoal !== 'custom';
  $('goal-help').hidden = currentGoal === 'custom';
  $('goal-help').textContent = currentGoal === 'phone' ? 'Connect your phone to the same network as this computer.' : 'Only apps running here can connect.';
  $('trust').hidden = active || settings().bind === '127.0.0.1' || settings().bind === '::1';
  const failureCode = actionError || (!active && state?.failure?.code) || '';
  const code = failureCode === 'selection_multiple' ? '' : failureCode;
  $('port-permissions').hidden = code !== 'http_permission';
  text('error-code', code ? 'Error code: ' + code : '');
  $('error-code').hidden = !code;
  if (shownErrorCode !== code) {
    shownErrorCode = code;
    copyDiagnostic();
  }
  text('selection-help', selectionStep ? 'Your account has several telescopes. Choose one, then Connect.' : savedDevice && !receivedDevices.length ? 'Choose “Find on my account”, then Connect to load other telescopes.' : 'Connect uses your only telescope, or asks which one if there are several.');
  if ($('device-choice').children.length) textOptionLabel();
  const manual = $('device-choice').value === 'custom';
  const choosing = selectionStep || (!active && manual);
  const needsSerial = choosing && $('device-choice').value !== '' && !$('serial').value.trim();
  $('serial-help').hidden = !needsSerial;
  const actionArea = $(choosing ? 'picker-action' : 'primary-action');
  if ($('start').parentElement !== actionArea) actionArea.append($('start'));
  const statusText = ended ? (state?.native ? 'Quit complete.' : 'Service stopped. You can close this tab.') : choosing && state?.failure?.code !== 'release' ? (needsSerial ? 'Enter a serial number.' : $('device-choice').value === '' ? 'Choose a telescope' : 'Ready to connect') : state?.status || 'Loading…';
  text('status', statusText);
  $('advanced').querySelector('summary').tabIndex = blocked || active || !!pending ? -1 : 0;
  routeControls(blocked);
  lostLine();
  if (pickerFocusPending && !$('options').disabled) {
    pickerFocusPending = false;
    const picker = $('device-choice');
    picker.focus();
    // The action has moved and fields may have changed height. Reveal their
    // final layout once, while preserving any focus the user moves meanwhile.
    requestAnimationFrame(() => {
      if (document.activeElement !== picker || $('options').disabled || $('settings').hidden || !selectionStep) return;
      const style = getComputedStyle(picker);
      const ring = (parseFloat(style.outlineWidth) || 0) + Math.max(0, parseFloat(style.outlineOffset) || 0);
      const rect = picker.getBoundingClientRect(), help = $('selection-help').getBoundingClientRect(), start = $('start').getBoundingClientRect();
      const group = {top: rect.top - ring, bottom: Math.max(help.bottom, start.bottom + ring), left: Math.min(rect.left, start.left) - ring, right: Math.max(rect.right, start.right) + ring};
      const target = group.bottom - group.top <= innerHeight && group.right - group.left <= innerWidth ? group : {top: rect.top - ring, bottom: rect.bottom + ring, left: rect.left - ring, right: rect.right + ring};
      const dy = target.top < 0 ? Math.floor(target.top) : Math.max(0, Math.ceil(target.bottom - innerHeight));
      const dx = target.left < 0 ? Math.floor(target.left) : Math.max(0, Math.ceil(target.right - innerWidth));
      if (dx || dy) window.scrollBy(dx, dy);
    });
  }
}
// The route choice is locked while the Telescope Network route runs and while copying
// or deleting runs; the LAN form is too. When to scan stays open while
// copying, which takes it at its next connection.
function routeControls(blocked) {
  if (active) routeChoice = 'account';
  const busy = !!state?.copy_busy;
  $('route-choice').disabled = blocked || active || busy || !!pending || !!lanPending;
  $('route-help').hidden = !(active || busy);
  $('route-account').checked = routeChoice === 'account';
  $('route-lan').checked = routeChoice === 'lan';
  $('account-route').hidden = routeChoice !== 'account';
  $('lan-route').hidden = routeChoice !== 'lan';
  $('lan-options').disabled = blocked || active || busy || !!lanPending;
  $('lan-when').disabled = blocked || active || !!pending || !!lanPending;
  text('lan-test', lanPending === 'test' ? 'Testing…' : 'Test');
  text('lan-save', lanPending === 'save' ? 'Saving…' : 'Save');
  if (lanPending !== 'when' && $('lan-when').value !== lanWhen()) $('lan-when').value = lanWhen();
  $('lan-scan').disabled = lanScanning;
  text('lan-scan', lanScanning ? 'Scanning…' : lanScanned ? 'Scan again' : 'Scan');
  connectionLine();
  lanAutoScan();
}
// On this network scans by itself when the user opens it, and when it is
// shown with no address saved. A form locked by a run doesn't scan, and
// neither does one set never to scan.
function lanAutoScan() {
  const shown = !!state && !$('lan-route').hidden;
  const opened = shown && !lanShown;
  lanShown = shown;
  if (opened && !$('lan-options').disabled && lanWhen() !== 'off' && (lanOpened || !state.settings?.lan_address)) lanScan();
  if (shown) lanOpened = false;
}
function lanScanResult(message, tone = '') {
  text('lan-scan-result', message);
  if (tone) $('lan-scan-result').setAttribute('data-tone', tone);
  else $('lan-scan-result').removeAttribute('data-tone');
}
// lanRows lists what the scan found: a telescope's name, model and flags,
// and a button per address that fills it in. The scan's answer has no
// serial; Saved marks the telescope saved here.
function lanRows() {
  $('lan-found').replaceChildren(...lanFound.map(t => {
    const li = document.createElement('li');
    const name = document.createElement('span');
    const flags = [t.model, t.busy ? 'busy: the phone app may refuse to connect' : '', t.saved ? 'saved' : ''].filter(Boolean);
    name.textContent = (t.name || 'Seestar') + (flags.length ? ' (' + flags.join(', ') + ')' : '');
    li.append(name, document.createElement('br'));
    for (const address of t.addresses || []) {
      const pick = document.createElement('button');
      pick.type = 'button';
      pick.className = 'small';
      pick.textContent = address;
      pick.setAttribute('aria-label', 'Use ' + address + ' for ' + (t.name || 'Seestar'));
      pick.addEventListener('click', () => lanPick(t, address));
      li.append(pick);
    }
    return li;
  }));
}
// Picking a row fills the address in; Test and Save work as for a typed one.
function lanPick(t, address) {
  if ($('lan-options').disabled) return;
  $('lan-address').value = address;
  lanReach = null;
  lanError('');
  lanResult('Picked ' + (t.name || 'Seestar') + ' at ' + address + '. Test or Save to use it.');
  $('lan-address').focus();
}
// Scan asks this network for Seestars the way the phone app does.
async function lanScan() {
  if (lanScanning) return;
  lanScanning = true;
  lanScanResult('Looking for telescopes on this network…');
  controls();
  try {
    const r = await api('scan', {});
    lanFound = r.telescopes || [];
    lanRows();
    const n = lanFound.length;
    if (r.message) lanScanResult(r.message, r.blocked ? 'error' : '');
    else lanScanResult(n === 1 ? 'Found 1 telescope. Pick its address to fill it in.' : 'Found ' + n + ' telescopes. Pick an address to fill it in.');
  } catch (e) {
    lanScanResult(e.message || lostCopy, 'error');
  } finally {
    lanScanning = false;
    lanScanned = true;
    controls();
  }
}
// The header's line and dot: the proxy's status on the Telescope Network route, and
// the saved address and its last Test on this network. The dot is green
// only while connected now; a retry, a busy telescope or an idle release is
// busy.
function connectionLine() {
  let line = 'Loading…', tone = 'off';
  const saved = state?.settings?.route === 'lan' ? state.settings.lan_address || '' : '';
  if (state && saved && !active) {
    const tested = sameEndpoint(lanReach?.address, saved);
    line = !tested ? 'On this network: ' + saved : lanReach.ok ? 'On this network: reachable' : "Can't reach " + saved;
    tone = !tested ? 'off' : lanReach.ok ? 'on' : 'error';
  } else if (state) {
    line = active ? state.status : 'Not connected';
    tone = !active ? 'off' : state.ready ? 'on' : 'busy';
  }
  text('connection-text', line);
  if ($('connection-dot').getAttribute('data-tone') !== tone) $('connection-dot').setAttribute('data-tone', tone);
}
function lanError(message) {
  text('lan-address-error', message);
  $('lan-address-error').hidden = !message;
  if (message) $('lan-address').setAttribute('aria-invalid', 'true');
  else $('lan-address').removeAttribute('aria-invalid');
}
function lanResult(message, tone = '') {
  text('lan-result', message);
  if (tone) $('lan-result').setAttribute('data-tone', tone);
  else $('lan-result').removeAttribute('data-tone');
}
// endpointKey is a host:port as the server keeps it, in one spelling for
// every way of writing the same endpoint: a name in lower case, an IPv6
// address compressed and in lower case, an unscoped IPv4-mapped one as
// IPv4, and the port as a number. An IPv6 zone is an interface name, whose
// case matters, so it is kept exactly. Anything else is kept as it is.
function endpointKey(endpoint) {
  const m = /^(?:\[([^\]]*)\]|([^:]*)):(\d+)$/.exec(endpoint || '');
  if (!m) return endpoint || '';
  if (m[1] === undefined) return m[2].toLowerCase() + ':' + Number(m[3]);
  const at = m[1].indexOf('%');
  let host = (at < 0 ? m[1] : m[1].slice(0, at)).toLowerCase();
  const zone = at < 0 ? '' : m[1].slice(at);
  try {
    host = new URL('http://[' + host + ']/').hostname.slice(1, -1);
  } catch {}
  const v4 = !zone && /^::ffff:([0-9a-f]{1,4}):([0-9a-f]{1,4})$/.exec(host);
  if (v4) return [v4[1], v4[2]].map((h) => parseInt(h, 16)).flatMap((n) => [n >> 8, n & 255]).join('.') + ':' + Number(m[3]);
  return '[' + host + zone + ']:' + Number(m[3]);
}
// sameEndpoint reports whether a and b are the same endpoint.
function sameEndpoint(a, b) {
  return !!a && !!b && endpointKey(a) === endpointKey(b);
}
// Test checks that the address answers SMB, as seestar-copy's Test does.
async function lanTest() {
  if (lanPending) return null;
  const typed = $('lan-address').value.trim();
  lanPending = 'test';
  lanReach = null;
  lanError('');
  lanResult('Connecting to ' + (typed.includes(':') ? typed : typed + ':445') + '…');
  controls();
  let focus = '', r = null;
  // The scan request goes straight to the typed address too, so a Save
  // after it saves which telescope answered there; not when set never to
  // scan.
  const probe = typed && lanWhen() !== 'off' ? api('scan', {address: typed}).catch(() => null) : Promise.resolve(null);
  try {
    let seen;
    [r, seen] = await Promise.all([api('test', {address: typed}, '/copy/api/'), probe]);
    lanReach = {typed, address: r.address, ok: r.ok};
    // Test saves nothing: until Save, the copy tabs use the saved route.
    const saved = state?.settings?.route === 'lan' ? state.settings.lan_address : '';
    const unsaved = r.ok && !sameEndpoint(r.address, saved) ? ' Save to use this address for Copy, Browse and Free up space.' : '';
    const t = seen?.telescopes?.[0];
    const named = t ? ' It answered as ' + (t.name || 'a Seestar') + (t.model ? ' (' + t.model + ')' : '') + '.' : '';
    lanResult(r.message + named + unsaved, r.ok ? 'ok' : 'error');
  } catch (e) {
    if (e.fields?.address) {
      lanResult('');
      lanError(e.fields.address);
      focus = 'lan-address';
    } else lanResult(e.message || lostCopy, 'error');
  } finally {
    await probe;
    lanPending = '';
    controls();
    if (focus) $(focus).focus();
  }
  return r;
}
// Save Tests the address first, unless it just passed, and makes this
// network the route only if it answers; the server checks again that it
// answers, and the result line says so meanwhile.
async function lanSave() {
  if (lanPending) return;
  const typed = $('lan-address').value.trim();
  let address = lanReach?.ok && lanReach.typed === typed ? lanReach.address : '';
  if (!address) {
    const r = await lanTest();
    if (!r?.ok) return;
    address = r.address;
  }
  lanPending = 'save';
  lanError('');
  lanResult('Checking ' + address + '…');
  controls();
  let saved = false;
  try {
    await api('route', {settings: {route: 'lan', lan_address: address}});
    saved = true;
    await refresh();
  } catch (e) {
    if (e.field === 'lan_address') {
      lanError(e.message || 'Check this address.');
    } else lanResult(e.message || lostCopy, 'error');
  } finally {
    lanPending = '';
    controls();
  }
  if (!saved) {
    if (!$('lan-address-error').hidden) $('lan-address').focus();
    return;
  }
  lanResult('Saved. ' + address + ' is reachable.', 'ok');
}
// lanWhen is when a connect scans for the saved telescope: the saved
// lan_scan, and start, the default, for none or one this page doesn't know.
function lanWhen() {
  const when = state?.settings?.lan_scan;
  return when === 'connect' || when === 'off' ? when : 'start';
}
function lanWhenResult(message, tone = '') {
  text('lan-when-result', message);
  if (tone) $('lan-when-result').setAttribute('data-tone', tone);
  else $('lan-when-result').removeAttribute('data-tone');
}
// Choosing when to scan saves it at once; a refused choice goes back.
// Saving disables the select, which takes focus from it: it gets focus
// back unless another control took it meanwhile.
async function lanWhenSave() {
  if (lanPending) return;
  const select = $('lan-when'), when = select.value;
  const focused = document.activeElement === select;
  lanPending = 'when';
  lanWhenResult('');
  controls();
  try {
    await api('lan-scan', {settings: {lan_scan: when}});
    await refresh();
    lanWhenResult('Saved.', 'ok');
  } catch (e) {
    lanWhenResult(e.message || lostCopy, 'error');
  } finally {
    lanPending = '';
    controls();
    const lost = !document.activeElement || document.activeElement === document.body;
    if (focused && lost && !select.disabled && !$('lan-route').hidden) select.focus();
  }
}
// Tabs: only the enabled ones can be selected; Arrow, Home and End move
// among them and focus follows, as in seestar-copy.
function selectTab(name) {
  if (!tabNames.includes(name) || $('tab-' + name).disabled) return;
  currentTab = name;
  for (const tab of tabNames) {
    const on = tab === name;
    $('tab-' + tab).setAttribute('aria-selected', String(on));
    $('tab-' + tab).tabIndex = on ? 0 : -1;
    $('panel-' + tab).hidden = !on;
  }
  copyNotice();
  if (name === 'browse') browseFrame();
}
function tabKey(event) {
  const open = tabNames.filter(tab => !$('tab-' + tab).disabled);
  const i = open.indexOf(currentTab), n = open.length;
  const j = {ArrowRight: (i + 1) % n, ArrowLeft: (i + n - 1) % n, Home: 0, End: n - 1}[event.key];
  if (j === undefined) return;
  event.preventDefault();
  selectTab(open[j]);
  $('tab-' + open[j]).focus();
}
// Copy, Browse and Free up space: copy's API under /copy/api/. The tabs
// open once a route is saved; until then, and while the copier has no
// target, a notice says why and links to Connection.
const copyTabNames = ['copy', 'browse', 'free'];
const reachCopy = {
  not_connected: ['Not connected.', 'Go to Connection'],
  no_lan_address: ["Save the telescope's address on the Connection tab first.", 'Go to Connection']
};
// The last copy status, the saved copy settings (null until saved), the
// saved folder's history file, and whether Change… has the form open.
let copyStatus = null, copySaved = null, copyHistory = '', copyFormOpen = false, copyFilled = false;
// copyTelescope is the id of the telescope whose folder the folder field
// was filled with; each telescope has its own.
let copyTelescope = null;
// A refused copy.json keeps them off for the run, and its note, naming
// the file, replaces the usual one.
function copyTabs(s) {
  const open = !!s.copy_unlocked, refused = !!s.copy_refused, note = refused ? 'copy-refused' : 'tabs-note';
  for (const tab of copyTabNames) {
    const button = $('tab-' + tab);
    button.disabled = !open;
    if (open) button.removeAttribute('aria-describedby');
    else if (button.getAttribute('aria-describedby') !== note) button.setAttribute('aria-describedby', note);
  }
  $('tabs-note').hidden = open || refused;
  $('copy-refused').hidden = !refused;
  if (refused) text('copy-refused-path', s.files?.copy || '');
  $('copy-steps-go').hidden = !open;
  if (!open && copyTabNames.includes(currentTab)) selectTab('connection');
  copyNotice();
}
// Only Copy, while copying repeats, says copying will go on.
function copyNotice() {
  const reach = reachCopy[state?.copy_reach];
  $('copy-reach').hidden = !reach || !copyTabNames.includes(currentTab);
  if (!reach) return;
  const repeating = currentTab === 'copy' && state.copy_reach === 'not_connected' && copyStatus?.copy.on && !copySaved?.once;
  text('copy-reach-text', repeating ? 'Not connected. Copying resumes at the next check after you connect.' : reach[0]);
  text('copy-reach-go', reach[1]);
}
// The notice's link opens what it names: the address on this network,
// or the account's Connect.
function goReach() {
  const reach = state?.copy_reach;
  if (reach === 'no_lan_address') {
    routeChoice = 'lan';
    lanOpened = true;
  }
  selectTab('connection');
  controls();
  const target = $(reach === 'no_lan_address' ? 'lan-address' : 'start');
  (target.disabled ? $('tab-connection') : target).focus();
}
// Browse loads once a route is saved; until a folder is saved too, it
// shows the telescope's files without statuses. It loads again, once it is
// in view, when copy's status says what it shows moved: a folder saved or
// changed, a new selection, or the telescope reached another way, since an
// error the frame shows from before would stay until retried.
let browseView = null;
function browseFrame() {
  if (!state?.copy_unlocked) return;
  $('b-frame').hidden = false;
  const view = copyStatus?.browse_view;
  if ($('b-frame').src && (view === browseView || currentTab !== 'browse')) return;
  browseView = view;
  $('b-frame').src = '/browse/';
}
// goSetup opens Copy at its folder while the form shows, else at its tab.
function goSetup() {
  selectTab('copy');
  (copyFormOpen && !$('f-set').disabled ? $('f-folder') : $('tab-copy')).focus();
}
function put(id, value) {
  text(id, value || '');
}
// crawl shows what a run is looking at on one line, whole in its title.
function crawl(id, value) {
  put(id, value);
  $(id).title = value || '';
}
function listItems(id, items, item) {
  $(id).replaceChildren(...items.map(item));
}
function pathItem(f) {
  const li = document.createElement('li');
  li.textContent = f.path + ' — ' + f.reason;
  return li;
}
// groupRow is a table row for a folder: its cells end with the folder, a
// count and a size.
function groupRow(...cells) {
  const tr = document.createElement('tr');
  const n = cells.length;
  tr.append(...cells.map((c, i) => {
    const td = document.createElement('td');
    td.append(c);
    if (i >= n - 2) td.className = 'num';
    else if (i === n - 3) td.className = 'name';
    return td;
  }));
  return tr;
}
function result(id, value, tone = '') {
  $(id).className = 'result' + (tone ? ' ' + tone : '');
  put(id, value);
}
function copyError(e) {
  return e?.error || e?.message || lostCopy;
}

// The copy settings: a summary line, and the form Change… opens. The
// first visit has the form open, without Cancel.
const copyFields = {folder: 'f-folder', history_dir: 'f-hist', interval: 'f-int', files_at_once: 'f-files', excludes: 'f-ex', includes: 'f-in'};
const lines = value => value === '' ? [] : value.split('\n');
function copyFill(s) {
  $('f-folder').value = s.folder;
  $('f-hist').value = s.history_dir || '';
  $('f-int').value = copyInterval = String(s.interval_minutes);
  $('f-repeat').checked = !s.once;
  repeatChange();
  $('f-files').value = String(s.files_at_once);
  $('f-ex').value = s.excludes.join('\n');
  $('f-in').value = s.includes.join('\n');
  $('f-skip').checked = s.skip_built_in;
  $('f-start').checked = s.start_copying;
}
// The interval counts only while copying repeats. Copy once disables it,
// showing and sending its last valid value, kept for a later return to
// repeating, and drops an error it had. The value is kept in the digits
// the server reads, since the field also takes 12.0 or 1e1.
let copyInterval = '5';
function intervalInput() {
  const value = $('f-int').value.trim(), n = Number(value);
  if (value && Number.isInteger(n) && n >= 1 && n <= 1440) copyInterval = String(n);
}
function repeatChange() {
  const repeat = $('f-repeat').checked;
  if (!repeat) {
    $('f-int').value = copyInterval;
    put('f-int-err', '');
    $('f-int-err').hidden = true;
    $('f-int').removeAttribute('aria-invalid');
  }
  $('f-int').disabled = !repeat;
}
function copyInput() {
  return {
    folder: $('f-folder').value, history_dir: $('f-hist').value, interval: $('f-repeat').checked ? $('f-int').value : copyInterval, files_at_once: $('f-files').value,
    excludes: lines($('f-ex').value), includes: lines($('f-in').value),
    skip_built_in: $('f-skip').checked, start_copying: $('f-start').checked, once: !$('f-repeat').checked
  };
}
// showErrors puts each message under its field and returns the first
// field in error, in page order.
function showErrors(fields) {
  let first = null;
  for (const [name, id] of Object.entries(copyFields)) {
    const message = fields[name] || '';
    put(id + '-err', message);
    $(id + '-err').hidden = !message;
    if (message) {
      $(id).setAttribute('aria-invalid', 'true');
      first ||= $(id);
    } else $(id).removeAttribute('aria-invalid');
  }
  return first;
}
function copySummary(s) {
  const minutes = s.once ? 'once each time you start' : s.interval_minutes === 1 ? 'every minute' : 'every ' + s.interval_minutes + ' min';
  const parts = ['Copying to ' + s.folder + ' ' + minutes];
  if (s.files_at_once !== 4) parts.push(s.files_at_once === 1 ? '1 file at once' : s.files_at_once + ' files at once');
  if (s.skip_built_in) parts.push('skipping logs and system files');
  if (s.excludes.length) parts.push('leaving out ' + s.excludes.join(', '));
  if (s.includes.length) parts.push('only ' + s.includes.join(', '));
  return parts.join(', ') + '.' + (s.start_copying ? ' Starts when the app opens.' : '');
}
// The notice above a form no folder was saved from: the first visit's,
// or, once another telescope has a folder, this telescope's.
const firstNotices = {
  first: 'Choose the folder for your images, then press Save. Copying starts later, when you press Start copying.',
  new: 'This telescope has no folder yet. Each telescope has its own: choose one for its images, then press Save. Copying starts when you press Start copying.',
  unknown: "Connect to the telescope to choose its folder. Each telescope has its own, and this one is known once it's connected."
};
function renderSetup() {
  const first = !copySaved, telescope = copyStatus?.telescope;
  if (first) copyFormOpen = true;
  $('c-main').hidden = first;
  $('s-first').hidden = !first;
  put('s-first', firstNotices[telescope && !telescope.known ? 'unknown' : telescope?.new ? 'new' : 'first']);
  const offer = telescope?.offer;
  $('s-offer').hidden = !offer;
  put('s-offer-text', offer ? "This telescope's address is now known as " + offer.name + '. If it is the same telescope, it can keep the folder saved for the address, ' + offer.folder + '. Nothing changes until you choose it.' : '');
  $('s-line').hidden = first;
  put('s-summary', copySaved && copySummary(copySaved));
  $('copy-form').hidden = !copyFormOpen;
  $('s-change').setAttribute('aria-expanded', String(copyFormOpen));
  $('f-cancel').hidden = first;
  $('s-locked').hidden = !(copyOn && copyFormOpen);
  renderHistory();
}
function openCopyForm() {
  copyFormOpen = true;
  result('f-saved', '');
  renderSetup();
  $('f-folder').focus();
}
function closeCopyForm() {
  copyFormOpen = false;
  if (copySaved) copyFill(copySaved);
  showErrors({});
  renderSetup();
  $('s-change').focus();
}
// copySave sends the form; Save is disabled until the answer is in, so two
// saves' answers can't arrive out of order.
async function copySave() {
  const button = $('f-save');
  if (button.disabled || stale()) return;
  button.disabled = true;
  const first = !copySaved;
  result('f-saved', '');
  result('h-result', '');
  try {
    let data;
    try {
      data = await api('settings', copyInput(), '/copy/api/');
    } catch (e) {
      if (e?.fields) {
        showErrors(e.fields).focus();
        return;
      }
      showErrors({});
      result('f-saved', e?.error || "Couldn't save settings. Your changes have not been saved. Try saving again.", 'err');
      // Focus brings the answer into view, below the form.
      $('f-saved').focus();
      return;
    }
    showErrors({});
    copySaved = data.settings;
    copyFill(copySaved);
    copyFormOpen = false;
    renderSetup();
    // The first Save doesn't start copying; the notice above Start copying
    // says it is the next step, and takes focus so it is read.
    copyNext = first;
    await copyRefresh();
    if (first) $('c-next').focus();
    else {
      result('f-saved', 'Settings saved.', 'ok');
      $('s-change').focus();
    }
  } finally {
    button.disabled = false;
  }
}
// copyAdopt gives this telescope the folder saved for its address, as the
// offer above the form says.
async function copyAdopt() {
  const button = $('s-adopt');
  if (button.disabled || stale()) return;
  button.disabled = true;
  result('s-offer-result', '');
  try {
    const data = await api('adopt', {}, '/copy/api/');
    copySaved = data.settings;
    $('f-folder').value = data.settings.folder;
    showErrors({});
    copyFormOpen = false;
    await copyRefresh();
    result('f-saved', 'Settings saved.', 'ok');
    $('s-change').focus();
  } catch (e) {
    result('s-offer-result', e?.error || "Couldn't save settings. Try again.", 'err');
    $('s-offer-result').focus();
  } finally {
    button.disabled = false;
  }
}
// copyChoose shows the window's folder picker for a folder field, the
// images folder's or the copy history's, starting at the folder it shows;
// Cancel changes nothing. The other fields' errors stay as they are.
async function copyChoose(name) {
  const id = copyFields[name], shown = {};
  for (const [other, oid] of Object.entries(copyFields)) if (!$(oid + '-err').hidden) shown[other] = $(oid + '-err').textContent;
  try {
    const data = await api('folder', {folder: $(id).value || $(id).placeholder}, '/copy/api/');
    if (data.folder) {
      $(id).value = data.folder;
      renderHistory();
      showErrors({...shown, [name]: ''});
    }
  } catch (e) {
    showErrors({...shown, [name]: e?.error || "Couldn't open the folder picker. Type the folder's full path instead."});
  }
  $(id).focus();
}
// renderHistory shows the saved folder's history file, or, while the
// folder or the history folder field holds an unsaved one, a line saying
// to save first.
function renderHistory() {
  const edited = !!copySaved && ($('f-folder').value !== copySaved.folder || $('f-hist').value !== (copySaved.history_dir || ''));
  $('h-details').hidden = !copyHistory;
  $('h-saved').hidden = edited;
  $('h-unsaved').hidden = !edited;
  put('h-path', copyHistory);
}

// The Copy tab: the server words each state; the page shows its parts.
// Each state's buttons, and the states the help line shows in.
const copyShows = {
  'c-start': ['off', 'stopped', 'inuse', 'cantwrite', 'done'],
  'c-check': ['uptodate', 'failures', 'listfail', 'unreachable'],
  'c-stop': ['connecting', 'listing', 'copying', 'uptodate', 'failures', 'listfail', 'unreachable'],
  'c-help': ['connecting', 'listing', 'copying', 'uptodate', 'failures', 'listfail', 'unreachable']
};
const copyBusy = {connecting: 'Connecting', listing: 'Looking for new files'};
let copyOn = false, copyShown = {state: '', files: '', preview: '', pgroups: ''};
let pressed = null; // a button the user pressed, until the state after it is shown
// copyNext shows, after the first Save, that Start copying is the next
// step, until copying starts.
let copyNext = false;
// renderPreview shows the Copy tab's preview, if any, and its buttons in
// the states Start shows in. A preview's state change is announced, and so
// is its going away: a Cancel, or Copy's title when it is discarded.
function renderPreview(v, st) {
  const p = v.preview;
  const ps = p ? p.state : '';
  $('c-preview').hidden = !p;
  $('c-preview').className = ps === 'failed' ? 'notice err' : '';
  put('c-ptitle', p?.title);
  put('c-ptext', p?.text);
  put('c-psub', p?.sub);
  crawl('c-ppath', p?.path);
  put('c-ptocopy', p?.tocopy);
  $('c-pbusy').hidden = ps !== 'running';
  const groups = p?.groups || [];
  $('c-ptable').hidden = !groups.length;
  const key = JSON.stringify(groups);
  if (key !== copyShown.pgroups) {
    copyShown.pgroups = key;
    listItems('c-pgroups', groups, g => groupRow(g.name, String(g.count), g.size));
  }
  const off = copyShows['c-start'].includes(st);
  $('c-pbtn').hidden = $('c-pnote').hidden = !off || ps === 'running';
  // A preview that ends hides Cancel; focus on it moves to the result.
  const cancel = $('c-pcancel');
  const lost = document.activeElement === cancel;
  cancel.hidden = ps !== 'running';
  if (lost && p && ps !== 'running') $('c-ptitle').focus();
  if (ps !== copyShown.preview && p) put('c-live', p.title);
  else if (ps !== copyShown.preview && copyShown.preview) put('c-live', pressed === cancel ? 'Preview canceled. Nothing was copied.' : v.title);
  copyShown.preview = ps;
}
function renderCopy(v) {
  const st = v.state;
  put('c-title', v.title);
  put('c-text', v.text);
  // The path and the count change each second; they are not announced.
  crawl('c-path', v.path);
  put('c-tocopy', v.tocopy);
  $('c-box').className = ['listfail', 'inuse', 'cantwrite'].includes(st) ? 'notice err' : '';
  $('c-busy').hidden = !copyBusy[st];
  if (copyBusy[st]) $('c-busy').setAttribute('aria-label', copyBusy[st]);
  const p = v.progress;
  $('c-copying').hidden = !p;
  if (p) {
    $('c-bar').max = p.total;
    $('c-bar').value = p.done;
    put('c-bytes', p.bytes);
    put('c-speed', p.speed);
    put('c-left', p.left);
    put('c-found', p.found);
    listItems('c-flight', p.flight, f => {
      const li = document.createElement('li');
      const name = document.createElement('span');
      name.textContent = f.path;
      const percent = document.createElement('span');
      percent.textContent = f.percent + '%';
      li.append(name, percent);
      return li;
    });
  }
  put('c-summary', v.summary);
  put('c-settling', v.settling);
  $('c-failed').hidden = !v.failed;
  put('c-notice', v.notice);
  const files = JSON.stringify(v.failed || []);
  if (files !== copyShown.files) {
    copyShown.files = files;
    listItems('c-files', v.failed || [], pathItem);
  }
  for (const [id, states] of Object.entries(copyShows)) $(id).hidden = !states.includes(st);
  if (!copyShows['c-start'].includes(st)) copyNext = false;
  $('c-next').hidden = !copyNext;
  // A stage change is announced; ticks within a stage are not.
  if (st !== copyShown.state) put('c-live', v.title);
  copyShown.state = st;
  renderPreview(v, st);
  const chip = $('copy-chip');
  chip.className = 'chip ' + (st === 'failures' || st === 'done' && v.failed ? 'warn' : v.on ? 'on' : 'off');
  put('copy-chip', v.chip);
  chip.hidden = !state?.copy_unlocked || !copySaved || !v.chip;
  activityChip('preview-chip', v.preview?.chip, v.preview?.state);
  // Settings can't change, nor the history clear, while copying is on.
  $('f-set').disabled = v.on || stale();
  copyOn = v.on;
  $('s-locked').hidden = !(copyOn && copyFormOpen);
  clearable();
  // When the button pressed went away, focus moves to the new state's
  // heading; background updates never move focus.
  if (pressed && pressed.offsetParent === null) {
    const lost = document.activeElement === pressed || document.activeElement === document.body;
    if (lost) $(pressed.id === 's-stop' ? 'f-folder' : pressed.id === 'c-pbtn' && v.preview ? 'c-ptitle' : 'c-title').focus();
    pressed = null;
  }
}
async function copyAction(action) {
  try {
    const v = await api('copy', {action}, '/copy/api/');
    result('c-err', '');
    // Copying has started, even if a one-pass run is already done by the
    // time the answer comes and the state shows Start copying again.
    if (action === 'start') copyNext = false;
    renderCopy(v);
  } catch (e) {
    result('c-err', copyError(e), 'err');
    pressed = null;
  }
}
async function press(event, action) {
  if (stale()) return;
  pressed = event.currentTarget;
  await copyAction(action);
}

// The Free up space tab: the server words each state, as for Copy. The
// options show in these states, and each button in its own.
const freeShows = {
  'x-options': ['start', 'nothing', 'checkfail'],
  'x-find': ['start', 'nothing', 'checkfail'],
  'x-find-note': ['start', 'nothing', 'checkfail'],
  'x-cancel': ['checking'],
  'x-preview': ['preview'],
  'x-go': ['preview'],
  'x-change': ['preview'],
  'x-deleting': ['deleting'],
  'x-file': ['checking', 'deleting'],
  'x-stop': ['deleting'],
  'x-done': ['done', 'stopped', 'failed']
};
const freeBox = {checkfail: 'notice err', failed: 'notice err', nothing: 'notice', stopped: 'notice', done: 'notice ok'};
// The shown state and stage, the preview's id, options and groups with
// their boxes.
let freeShown = {state: '', stage: '', id: 0, kept: '', groups: [], boxes: [], trust: false};
let freePressed = null; // as pressed, for this tab
// byteSize words a byte count as the server does: decimal units, with one
// decimal below 100.
function byteSize(n) {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (n >= 1000 && i < units.length - 1) {
    n /= 1000;
    i++;
  }
  if (i === 0) return n + ' B';
  return (n < 100 ? n.toFixed(1) : n.toFixed(0)) + ' ' + units[i];
}
const fileCount = n => n === 1 ? '1 file' : n.toLocaleString('en-US') + ' files';
function freeFill(s) {
  $('x-days').value = String(s.older_than_days);
  $('x-trust').checked = s.trust_state;
  $('x-ex').value = s.free_excludes.join('\n');
  $('x-in').value = s.free_includes.join('\n');
  freeIntro();
}
// freeIntro words the Start text for the options as set, and names the
// saved folder whose copies, or copy history, the check uses.
function freeIntro() {
  const trust = $('x-trust').checked;
  put('x-intro', trust ? "Free up space uses this app's saved copy records. Your copies will not be checked."
    : 'Free up space deletes files from the telescope only after they have been copied here, and checks that your copy still matches.');
  put('x-folder-text', copySaved && (trust ? 'Copy history for: ' : 'Copies in: ') + copySaved.folder);
  $('x-restack').hidden = !(Number($('x-days').value) > 0);
}
// freeErrors puts each message under its option, as showErrors does for
// Copy's, and returns the first option in error.
const freeFields = {days: 'x-days', free_excludes: 'x-ex', free_includes: 'x-in'};
function freeErrors(fields) {
  let first = null;
  for (const [name, id] of Object.entries(freeFields)) {
    const message = fields[name] || '';
    put(id + '-err', message);
    $(id + '-err').hidden = !message;
    if (message) {
      $(id).setAttribute('aria-invalid', 'true');
      first ||= $(id);
    } else $(id).removeAttribute('aria-invalid');
  }
  return first;
}
// activityChip shows a run's bubble in the header while it is busy, beside
// copying's. Its text changes are not announced; a run starting, or a
// delete's next stage, is, once, when its tab isn't the one open. The
// announcement goes when its run ends, so the next run's start, worded
// the same, is announced too.
const chipShown = {};
let chipLive = '';
function activityChip(id, label, stage) {
  const chip = $(id);
  put(id, label);
  chip.hidden = !state?.copy_unlocked || !label;
  stage = label ? stage : '';
  if (stage && stage !== chipShown[id] && currentTab !== chipTabs[id]) {
    put('chip-live', label);
    chipLive = id;
  } else if (!stage && chipLive === id) {
    put('chip-live', '');
    chipLive = '';
  }
  chipShown[id] = stage;
}
const chipTabs = {'copy-chip': 'copy', 'preview-chip': 'copy', 'free-chip': 'free'};
// Free up space, like Browse, waits for Copy's folder; until it is saved,
// its notice says where to set it.
function renderFree(v) {
  const st = v.state;
  $('x-setup').hidden = !!copySaved;
  $('x-main').hidden = !copySaved;
  freeIntro();
  activityChip('free-chip', v.chip, st + (v.recheck ? '-recheck' : ''));
  put('x-title', v.title);
  put('x-text', v.text);
  put('x-sub', v.sub);
  $('x-box').className = freeBox[st] || '';
  $('x-changed').hidden = !v.changed;
  const p = v.progress;
  $('x-busy').hidden = st !== 'checking' || !!p;
  $('x-hash').hidden = st !== 'checking' || !p;
  if (p && st === 'checking') {
    $('x-hbar').max = p.total;
    $('x-hbar').value = p.done;
    put('x-htext', p.text);
  }
  if (p && st === 'deleting') {
    $('x-dbar').max = p.total;
    $('x-dbar').value = p.done;
  }
  crawl('x-file', v.file);
  for (const [id, states] of Object.entries(freeShows)) $(id).hidden = !states.includes(st);
  if (v.stopping) $('x-stop').hidden = true;
  // An open confirm belongs to the preview it words: it closes before
  // another preview, or none, replaces that one's id and folders.
  if ((st !== 'preview' || v.id !== freeShown.id) && $('x-confirm').open) $('x-confirm').close();
  if (st === 'preview' && v.id !== freeShown.id) {
    freeShown.id = v.id;
    freeShown.trust = v.trust;
    freeShown.groups = v.groups;
    freeShown.boxes = v.groups.map(g => {
      const box = document.createElement('input');
      box.type = 'checkbox';
      box.checked = true;
      box.setAttribute('aria-label', 'Include ' + g.name);
      return box;
    });
    listItems('x-groups', v.groups, (g, i) => {
      const details = document.createElement('details');
      const summary = document.createElement('summary');
      summary.textContent = g.name;
      const ul = document.createElement('ul');
      ul.append(...g.files.map(f => {
        const li = document.createElement('li');
        const name = document.createElement('span');
        name.textContent = f.path;
        const size = document.createElement('span');
        size.textContent = f.size;
        li.append(name, size);
        return li;
      }));
      details.append(summary, ul);
      return groupRow(freeShown.boxes[i], details, String(g.count), g.size);
    });
    freeCount();
  }
  const kept = JSON.stringify([v.kept, v.kept_list]);
  if (kept !== freeShown.kept) {
    freeShown.kept = kept;
    put('x-ksum', v.kept);
    listItems('x-klist', v.kept_list || [], pathItem);
  }
  $('x-kept').hidden = !v.kept || !v.kept_list || !['preview', 'nothing', 'done'].includes(st);
  // Checking the files again is a stage of its own before Deleting.
  const stage = st + (v.recheck ? '-recheck' : '');
  if (stage !== freeShown.stage) put('x-live', v.title);
  freeShown.state = st;
  freeShown.stage = stage;
  clearable();
  // As for Copy: focus moves to the new state's heading only when the
  // button pressed went away.
  if (freePressed && freePressed.offsetParent === null) {
    const lost = document.activeElement === freePressed || document.activeElement === document.body;
    if (lost) $(v.title ? 'x-title' : 'x-intro').focus();
    freePressed = null;
  }
}
// freeChecked is the preview's groups whose box is checked.
function freeChecked() {
  return freeShown.groups.filter((_, i) => freeShown.boxes[i].checked);
}
// freeCount labels Delete with the checked folders' files.
function freeCount() {
  const n = freeChecked().reduce((a, g) => a + g.files.length, 0);
  put('x-go', 'Delete ' + fileCount(n) + '…');
  $('x-go').disabled = n === 0 || stale();
}
async function freeAction(body) {
  let v;
  try {
    v = await api('delete', body, '/copy/api/');
  } catch (e) {
    freePressed = null;
    if (e?.fields) {
      freeErrors(e.fields)?.focus();
      return;
    }
    freeErrors({});
    result('x-err', copyError(e), 'err');
    return;
  }
  freeErrors({});
  result('x-err', '');
  renderFree(v);
}
async function freePress(event, body) {
  if (stale()) return;
  freePressed = event.currentTarget;
  await freeAction(body);
}
function freeFind(event) {
  return freePress(event, {
    action: 'find', days: $('x-days').value, trust: $('x-trust').checked,
    free_excludes: lines($('x-ex').value), free_includes: lines($('x-in').value)
  });
}
// freeAsk shows the confirm dialog for the checked folders.
function freeAsk() {
  const groups = freeChecked();
  const n = groups.reduce((a, g) => a + g.files.length, 0);
  const bytes = groups.reduce((a, g) => a + g.bytes, 0);
  put('x-ctitle', 'Delete ' + fileCount(n) + ' from the telescope?');
  put('x-cbody', 'This frees ' + byteSize(bytes) + ". It can't be undone: the telescope keeps no trash. Each file is checked again just before it's deleted." +
    (freeShown.trust ? ' Your copies were not checked.' : ''));
  put('x-cgo', 'Delete ' + fileCount(n));
  $('x-confirm').showModal();
}
async function confirmDelete() {
  if (stale()) return;
  const folders = freeChecked().map(g => g.folder);
  freePressed = $('x-go');
  $('x-confirm').close();
  await freeAction({action: 'delete', id: freeShown.id, folders});
}
// clearable disables Clear history while copying is on or Free up space
// is checking or deleting; the server refuses those too.
function clearable() {
  $('h-clear').disabled = copyOn || ['checking', 'deleting'].includes(freeShown.state) || stale();
}
async function clearHistory() {
  $('h-confirm').close();
  if (stale()) return;
  try {
    await api('clear', {}, '/copy/api/');
    result('h-result', 'Copy history cleared.', 'ok');
  } catch (e) {
    result('h-result', copyError(e), 'err');
  }
  await copyRefresh();
}
// clip copies a path to the clipboard and says so on the button for a
// moment, and in the status line a screen reader announces.
async function clip(button, value, live, copied) {
  const label = button.getAttribute('data-label') || button.textContent;
  button.setAttribute('data-label', label);
  try {
    await navigator.clipboard.writeText(value);
    button.textContent = 'Copied';
    result(live, copied, 'ok');
  } catch (_) {
    button.textContent = "Couldn't copy";
    result(live, "Couldn't copy the path. Select it and copy it manually.", 'err');
  }
  setTimeout(() => button.textContent = label, 2000);
}
function copyRender(c) {
  const wasSetUp = !!copySaved;
  copyStatus = c;
  copySaved = c.set_up ? c.settings : null;
  copyHistory = c.history;
  $('f-choose').hidden = $('f-hchoose').hidden = !c.choose;
  $('f-hist').placeholder = c.default_history || '';
  if (!copyFilled) {
    copyFilled = true;
    put('f-builtin', c.default_excludes.join(' · '));
    copyFill(c.settings);
    freeFill(c.settings);
  } else if (c.telescope && c.telescope.id !== copyTelescope) {
    // Another telescope: the folder field shows its folder, or the one
    // suggested for it. The settings all telescopes share stay as edited.
    $('f-folder').value = c.settings.folder;
    showErrors({});
    // A form open only because the last telescope had no folder closes.
    if (c.set_up && !wasSetUp) copyFormOpen = false;
  }
  copyTelescope = c.telescope ? c.telescope.id : null;
  renderSetup();
  renderCopy(c.copy);
  renderFree(c.free);
  browseFrame();
  copyNotice();
  text('copy-settings', JSON.stringify(c.settings, null, 2));
  renderEvents();
}
// stale reports that what the copy tabs show may be out of date.
function stale() {
  return controlsLost || (copyLost || copyOld) && !!state?.copy_unlocked;
}
// The actions that need the server's answer, off while stale.
const staleActions = ['c-start', 's-adopt', 'c-check', 'c-stop', 'c-pbtn', 'c-pcancel', 's-stop', 'h-cgo', 'x-find', 'x-cancel', 'x-change', 'x-stop', 'x-done', 'x-cgo'];
// lostLine says, above every tab, that the controls or copy's status are
// lost, with the connection's last known state, marks the copy tabs' views
// stale and turns their actions off. Drafts in their fields are kept, and
// all of it comes back once a poll succeeds.
function lostLine() {
  const old = stale(), copyFailed = copyLost && !!state?.copy_unlocked;
  $('lost').hidden = !controlsLost && !copyFailed;
  text('lost-text', controlsLost ? lostCopy : copyFailed ? copyLostCopy : '');
  // On this network, the header line is the last known state.
  const known = controlsLost && !(state?.settings?.route === 'lan' && !active);
  $('last-known').hidden = !known;
  text('last-known', known ? 'Last known state: ' + (state?.status || 'Loading…') : '');
  for (const id of ['c-box', 'c-preview', 'x-box', 'x-preview']) {
    if (old) $(id).setAttribute('data-stale', '');
    else $(id).removeAttribute('data-stale');
  }
  // Browse asks the server itself, so only the controls' loss stops it:
  // inert keeps its loaded view but takes no click, key or focus.
  for (const name of ['data-stale', 'inert']) {
    if (controlsLost) $('b-frame').setAttribute(name, '');
    else $('b-frame').removeAttribute(name);
  }
  for (const id of staleActions) $(id).disabled = old;
  $('f-set').disabled = copyOn || old;
  freeCount();
  clearable();
}
// copyRefresh reads copy's status; a failure leaves the last one shown,
// since the wormhole's own poll reports a lost server.
async function copyRefresh() {
  try {
    const c = await api('status', undefined, '/copy/api/');
    copyLost = copyOld = false;
    copyRender(c);
  } catch (_) {
    copyLost = true;
  }
  lostLine();
}
// Activity: one list, newest first, of the connection's events, Browse's
// trail lines, and copy's and Free up space's activity, each row tagged
// with its source. Copy's ring has the detail of Copy and Free up space,
// so of their trail lines only Browse's, which the ring lacks, are shown.
// A line with no source is the connection's. The source "delete" is Free
// up space's, named as its tab is.
const sourceNames = {connection: 'Connection', copy: 'Copy', browse: 'Browse', delete: 'Free up space'};
function activityRows() {
  const rows = (state?.events || []).map(event => ({...event, source: event.source || 'connection'}))
    .filter(event => event.source === 'connection' || event.source === 'browse').reverse();
  for (const e of copyStatus?.activity || []) rows.push({time: e.at, source: e.source, message: e.text, ...(e.warn ? {warn: true} : {})});
  // A stable sort keeps each source's own order within a timestamp.
  return rows.sort((a, b) => (Date.parse(b.time) || 0) - (Date.parse(a.time) || 0));
}
function eventLine(row) {
  return new Date(row.time).toLocaleTimeString([], {hour: 'numeric', minute: '2-digit'}) + '  ' + (sourceNames[row.source] || row.source) + '  ' + row.message;
}
let activityShown = [];
function renderEvents() {
  activityShown = activityRows();
  const eventLines = activityShown.map(eventLine);
  const eventNodes = Array.from($('events').children);
  if (eventLines.length !== eventNodes.length || eventLines.some((line, i) => line !== eventNodes[i].textContent)) {
    $('events').replaceChildren(...eventLines.map((line, i) => {
      const item = document.createElement('li');
      item.textContent = line;
      item.setAttribute('data-source', activityShown[i].source);
      if (activityShown[i].warn) item.setAttribute('data-warn', 'true');
      return item;
    }));
  }
  copyDiagnostic();
}
// The diagnostic summary is one paste: the version, the error code, both
// settings documents, the failing operations, the trail newest first and
// the log path. Without a connection error, the error code is the newest
// failure's.
let shownErrorCode = '';
// trailNewestFirst is the trail, newest first; a stable sort keeps the
// newer of two lines with one timestamp first.
function trailNewestFirst() {
  return (state?.events || []).map(event => ({...event, source: event.source || 'connection'})).reverse()
    .sort((a, b) => (Date.parse(b.time) || 0) - (Date.parse(a.time) || 0));
}
function copyDiagnostic() {
  const failures = state?.failures || [];
  const newest = failures.reduce((a, b) => !a || (Date.parse(b.last) || 0) >= (Date.parse(a.last) || 0) ? b : a, null);
  diagnostic = JSON.stringify({
    app: 'seestar-wormhole', version: state?.version || '', error_code: shownErrorCode || newest?.code || '', settings: state?.effective || null,
    copy_settings: copyStatus?.settings || null, failures, events: trailNewestFirst(), log_path: state?.log_path || ''
  }, null, 2);
}
let pickerKey = '', receivedDevices = [], savedDevice = null;
let pickerDevices = [], selectionStep = false, pickerFocusPending = false;
function textOptionLabel() {
  const option = $('device-choice').children[0];
  const label = selectionStep ? 'Choose a telescope…' : 'Find on my account';
  if (option.textContent !== label) option.textContent = label;
}
function selectPair() {
  const index = pickerDevices.findIndex(d => d.serial === $('serial').value && d.model === $('model').value);
  $('device-choice').value = index >= 0 ? String(index) : $('serial').value || $('model').value ? 'custom' : '';
  $('custom-device').hidden = $('device-choice').value !== 'custom';
}
function devicePicker(devices) {
  if (devices) receivedDevices = devices;
  const all = [...receivedDevices];
  if (savedDevice && !all.some(d => d.serial === savedDevice.serial && d.model === savedDevice.model)) all.unshift({...savedDevice, label: 'Saved: ' + savedDevice.label});
  const key = JSON.stringify(all);
  if (key === pickerKey) return;
  pickerKey = key;
  pickerDevices = all;
  const picker = $('device-choice');
  const custom = picker.value === 'custom';
  picker.replaceChildren();
  const automatic = document.createElement('option');
  automatic.textContent = 'Find on my account';
  automatic.value = '';
  picker.append(automatic);
  all.forEach((device, index) => {
    const option = document.createElement('option');
    option.textContent = device.label;
    option.value = String(index);
    picker.append(option);
  });
  const option = document.createElement('option');
  option.textContent = 'Enter manually…';
  option.value = 'custom';
  picker.append(option);
  selectPair();
  if (custom) {picker.value = 'custom'; $('custom-device').hidden = false;}
  picker.onchange = () => {
    $('custom-device').hidden = picker.value !== 'custom';
    if (picker.value !== 'custom') {
      const device = pickerDevices[Number(picker.value)];
      $('serial').value = picker.value === '' ? '' : device.serial;
      $('model').value = picker.value === '' ? '' : device.model;
    }
    clearField('serial');
    clearField('model');
    controls();
  };
}
function accountChoices(s) {
  const nextSelectionStep = accountEmail() === s.devices_email && !s.active && s.failure?.code === 'selection_multiple';
  if (nextSelectionStep && !selectionStep) {
    pickerFocusPending = true;
    clearField('serial');
    clearField('model');
  }
  selectionStep = nextSelectionStep;
  if (!selectionStep) pickerFocusPending = false;
  devicePicker(accountEmail() === s.devices_email ? s.devices || (selectionStep ? s.failure.devices : undefined) : []);
}
// Settings carry the last successfully saved pair, independently of form drafts.
function projectPicker(s) {
  const pair = s.settings;
  savedDevice = pair.email === accountEmail() && (pair.serial || pair.model) ? {serial: pair.serial || '', model: pair.model || '', label: pair.choice_label} : null;
  accountChoices(s);
}
function render(s) {
  state = s;
  projectPicker(s);
  text('version',s.version);
  active = s.active;
  text('quit', s.native ? 'Quit' : 'Quit service');
  $('quit-help').textContent = s.native ? 'Closing this window stops the connection and quits the app.' : 'Closing this tab leaves the service running. Choose Quit service to stop it.';
  $('retry-storage').hidden = !s.save_failed;
  if (s.save_failed) alertText(s.password.code === 'password_removed' ? 'Password removed from keychain and this app. Settings could not be saved. Keep this window open and choose Retry saving settings.' : s.password.code === 'password_saved' ? 'Password saved to keychain. Settings could not be saved. Keep this window open and choose Retry saving settings.' : 'Settings could not be saved; they may reset next time.');
  if (selectionStep) alertText('');
  if (s.failure && !active && !actionError && !selectionStep) {
    const newFailure = lastFailure !== s.failure.code;
    alertText(s.failure.code === 'release' || s.failure.field ? '' : s.failure_message);
    if (newFailure && s.failure.field) fieldError(s.failure.field,s.failure_message + (s.failure.service ? ' Local port ' + s.failure.local + ' → Seestar port ' + s.failure.service + '.' : ''),s.failure.code);
    lastFailure = s.failure.code;

  }
  if (active) lastFailure = '';
  if (pending !== 'quit' && s.password.code && s.password.code !== noticeCode) {
    noticeCode = s.password.code;
    if (['password_saved','password_removed'].includes(noticeCode) && state.settings.email === accountEmail()) say(s.password.notice);
    else say('');
  }
  const run = s.effective;
  const listen = {all_networks: 'all networks', this_computer: 'this computer only', other: 'a selected network address'}[run.listen];
  $('run-summary').textContent = 'Listening on ' + listen + ' · Seestar app discovery ' + (s.discovery_started ? 'on' : 'off') + ' · ' + (run.idle.mode === 'keep_connected' ? 'Keep connected' : 'Connect when an app needs Seestar');
  $('phone-steps').hidden = !(active && s.discovery_started);
  const smb = run.ports['445'];
  $('copy-steps').hidden = !(active && smb);
  $('copy-port').textContent = smb || '';
  portsTable('running-ports', run.ports);
  text('effective-settings', JSON.stringify(s.effective, null, 2));
  text('log-path', s.log_path ? 'Private log: ' + s.log_path : '');
  $('log-error').hidden = !s.log_failed;
  text('config-file', s.files?.config ? 'Connection settings: ' + s.files.config : '');
  text('copy-file', s.files?.copy ? 'Copy settings: ' + s.files.copy : '');
  renderEvents();
  copyTabs(s);
  controls();
}
async function refresh() {
  const generation = ++statusGeneration;
  let s;
  try {
    s = await api('status');
  } catch (e) {
    if (ended || generation !== statusGeneration) return;
    throw e;
  }
  // A superseding poll may own rendering, but the action still needs its reply.
  if (ended || generation !== statusGeneration) return s;
  if (!connected && initialized) alertText('');
  connected = true;
  controlsLost = false;
  const first = !initialized;
  if (first) {
    portDefaults = s.port_defaults;
    load(s.settings);
    initialized = true;
  }
  if (!active && s.active) {
    $('password').value = '';
  }
  render(s);
  if (first && !selectionStep) {
    // WKWebView can finish the status fetch before its native window is key.
    const focusStart = () => requestAnimationFrame(() => $('start').focus());
    if (document.hasFocus()) focusStart();
    else window.addEventListener('focus', focusStart, {once: true});
  }
  return s;
}
async function poll() {
  if (ended) return;
  try {
    if (!pending || ['stop','quit'].includes(pending)) {
      await refresh();
      if (!ended) await copyRefresh();
    }
  } catch (_) {
    if (ended) return;
    connected = false;
    controlsLost = copyOld = true;
    controls();
  }
  if (!ended) setTimeout(poll, 1000);
}
async function act(action) {
  if (pending && !['stop','quit'].includes(action)) return;
  if (action === 'start' && accountEmail() === state?.devices_email && selectionStep && $('device-choice').value === '') {
    $('device-choice').focus();
    return;
  }
  if (action === 'start' && ($('device-choice').value === 'custom' || (selectionStep && $('device-choice').value !== '')) && !$('serial').value.trim()) {
    actionError = 'serial';
    fieldError('serial', 'Enter a serial number.', 'serial', true);
    return;
  }
  if (['start','password/save'].includes(action) && !validate()) return;
  ++statusGeneration;
  actionError = '';
  pending = action;
  alertText('');
  if (action === 'start') {
    say('');
  }
  if (action === 'password/save') say('Saving password to keychain…');
  if (action === 'forget') say('Removing saved password…');
  if (action === 'quit') say('Quitting — releasing the Seestar session…');
  controls();
  let errorField = '', focusTarget = '';
  try {
    const typed = usingSaved() ? '' : $('password').value;
    await api(action, {
      settings: settings(), password: ['start','password/save'].includes(action) ? typed : ''
    });
    if (ended || pending !== action) return;
    if (action === 'start') lastFailure = '';
    if (action === 'password/save' || action === 'forget') {
      $('password').value = '';
      if (action === 'forget') {
        retryRemoval = false;
        if (['password_remove','keychain_timeout'].includes(fieldErrors.password)) clearField('password');
      }
    }
    if (action === 'quit') {
      ended = true;
      say('');
      controls();
      return;
    }
    const refreshed = await refresh();
    if (ended || pending !== action || !refreshed) return;
    if (action === 'retry-storage') {
      // The action may finish for a different account than the current draft.
      if (accountEmail(refreshed.settings.email) === accountEmail()) {
        $('serial').value = refreshed.settings.serial || '';
        $('model').value = refreshed.settings.model || '';
      }
      projectPicker(refreshed);
      selectPair();
      clearField('serial');
      clearField('model');
      alertText('');
      focusTarget = selectionStep ? '' : 'start';
    }
    if (['password/save','forget'].includes(action)) {
      say(passwordState().notice || '');
      focusTarget = action === 'password/save' ? 'forget' : 'password';
    }
  } catch (e) {
    if (ended || pending !== action) return;
    if (['password/save','forget'].includes(action)) say('');
    if (action === 'forget') {
      retryRemoval = true;
    }
    if (e.code === 'storage' && ['password/save','forget'].includes(action)) {
      e.message = action === 'password/save' ? 'Password saved to keychain. Settings could not be saved. Keep this window open and choose Retry saving settings.' : 'Password removed from keychain and this app. Settings could not be saved. Keep this window open and choose Retry saving settings.';
      try {
        await refresh();
      } catch (_) {
      }
    }
    fail(e);
    errorField = e.field || '';
  } finally {
    if (pending === action) pending = '';
    controls();
    if (errorField && !$('options').disabled) {
      let field = errorField === 'bind' && $('listen').value !== 'other' ? 'listen' : errorField;
      if (field === 'password' && $('password').disabled) field = 'forget';
      if (!$(field).disabled) $(field).focus();
    } else if (focusTarget && !$(focusTarget).disabled && !$(focusTarget).hidden) $(focusTarget).focus();
    else if (action === 'start' && active && !$('start').disabled) $('start').focus();
  }
}
$('settings').addEventListener('submit', e => {
  e.preventDefault();
  if (!active && !$('start').disabled) act('start');
});
$('start').addEventListener('click', () => {
  if (active) act('stop');
});
for (const [id,action] of [['save-password','password/save'],['forget','forget'],['quit','quit'],['retry-storage','retry-storage']]) {
  $(id).addEventListener('click', () => act(action));
}
$('email').addEventListener('input', () => {
  $('password').value = '';
  const email = accountEmail();
  if (email !== draftEmail) {
    // The draft belongs to the account being edited, which may be newer than
    // the latest status response. Clear once; later manual entry stays local.
    $('serial').value = '';
    $('model').value = '';
    $('device-choice').value = '';
    $('custom-device').hidden = true;
    clearField('serial');
    clearField('model');
    draftEmail = email;
  }
  if (state) projectPicker(state);
});
for (const id of ['email','password','idle','bind','serial','model']) $(id).addEventListener('input', () => {
  clearField(id);
  controls();
});
for (const id of ['listen','discovery','mode']) $(id).addEventListener('change', () => {
  clearField(id === 'listen' ? 'bind' : id === 'mode' ? 'idle' : id);
  controls();
});
function applyGoal(goal) {
  currentGoal = goal;
  $('goal').value = goal;
  if (goal !== 'custom') {
    $('listen').value = goal === 'phone' ? '0.0.0.0' : '127.0.0.1';
    $('bind').value = $('listen').value;
    $('discovery').checked = goal === 'phone';
    portsTable('ports', portValues({ports: goal === 'phone' ? [{device: 80, local: 80}] : []}), true);
  }
  controls();
}
$('goal').addEventListener('change', () => applyGoal($('goal').value));
function customGoal() {
  currentGoal = 'custom';
  $('goal').value = 'custom';
  controls();
}
for (const id of ['listen','bind','discovery']) $(id).addEventListener('change', customGoal);
$('copy').addEventListener('click', async () => {
  try {
    await navigator.clipboard.writeText(diagnostic);
    text('copy-feedback', 'Diagnostic summary copied.');
  } catch (_) {
    $('diagnostic-copy').hidden = false;
    $('diagnostic-copy').value = diagnostic;
    $('diagnostic-copy').focus();
    $('diagnostic-copy').select();
    text('copy-feedback', 'Copy the selected diagnostic summary below.');
  }
});
// The native page uses Wails' postMessage bridge; browser mode keeps normal links.
function openExternalLink(event) {
  const link = event.target.closest('a[href^="https://"]');
  if (!link || !window._wails?.invoke || event.button > 1) return;
  event.preventDefault();
  window._wails.invoke('seestar:open:' + link.href);
}
for (const tab of tabNames) $('tab-' + tab).addEventListener('click', () => selectTab(tab));
// A bubble opens its run's tab.
for (const [id, tab] of Object.entries(chipTabs)) $(id).addEventListener('click', () => {
  selectTab(tab);
  $('tab-' + tab).focus();
});
$('tabs').addEventListener('keydown', tabKey);
for (const id of ['route-account', 'route-lan']) $(id).addEventListener('change', () => {
  if ($(id).checked) routeChoice = $(id).value;
  lanOpened = routeChoice === 'lan';
  controls();
});
$('lan-form').addEventListener('submit', e => {
  e.preventDefault();
  if (!$('lan-options').disabled) lanSave();
});
$('lan-test').addEventListener('click', lanTest);
$('lan-scan').addEventListener('click', lanScan);
$('lan-when').addEventListener('change', lanWhenSave);
$('copy-reach-go').addEventListener('click', goReach);
$('copy-form').addEventListener('submit', e => {
  e.preventDefault();
  copySave();
});
$('s-change').addEventListener('click', openCopyForm);
$('s-adopt').addEventListener('click', copyAdopt);
$('f-cancel').addEventListener('click', closeCopyForm);
$('f-folder').addEventListener('input', renderHistory);
$('f-hist').addEventListener('input', renderHistory);
$('f-int').addEventListener('input', intervalInput);
$('f-repeat').addEventListener('change', repeatChange);
$('f-choose').addEventListener('click', () => copyChoose('folder'));
$('f-hchoose').addEventListener('click', () => copyChoose('history_dir'));
$('copy-steps-go').addEventListener('click', goSetup);
$('x-setup-go').addEventListener('click', goSetup);
$('x-folder-go').addEventListener('click', goSetup);
$('c-start').addEventListener('click', e => press(e, 'start'));
$('c-check').addEventListener('click', e => press(e, 'check'));
$('c-stop').addEventListener('click', e => press(e, 'stop'));
$('c-pbtn').addEventListener('click', e => press(e, 'preview'));
$('c-pcancel').addEventListener('click', e => press(e, 'cancel'));
$('s-stop').addEventListener('click', e => press(e, 'stop'));
$('h-clip').addEventListener('click', e => clip(e.currentTarget, copyHistory, 'h-result', 'Copy history path copied.'));
$('h-clear').addEventListener('click', () => {
  result('h-result', '');
  $('h-confirm').showModal();
});
$('h-ccancel').addEventListener('click', () => $('h-confirm').close());
$('h-cgo').addEventListener('click', clearHistory);
// Escape and Cancel close the dialog; focus goes back to Clear history.
$('h-confirm').addEventListener('close', () => $('h-clear').focus());
$('x-find').addEventListener('click', freeFind);
$('x-days').addEventListener('keydown', e => e.key === 'Enter' && freeFind(e));
$('x-days').addEventListener('input', freeIntro);
$('x-trust').addEventListener('change', freeIntro);
$('x-cancel').addEventListener('click', e => freePress(e, {action: 'cancel'}));
$('x-stop').addEventListener('click', e => freePress(e, {action: 'stop'}));
$('x-change').addEventListener('click', e => freePress(e, {action: 'reset'}));
$('x-done').addEventListener('click', e => freePress(e, {action: 'reset'}));
$('x-go').addEventListener('click', freeAsk);
$('x-groups').addEventListener('change', freeCount);
$('x-ccancel').addEventListener('click', () => $('x-confirm').close());
$('x-cgo').addEventListener('click', confirmDelete);
// Escape and Cancel close the dialog; focus goes back to Delete.
$('x-confirm').addEventListener('close', () => {
  if (!$('x-go').hidden) $('x-go').focus();
});
$('lan-address').addEventListener('input', () => lanError(''));
document.addEventListener('click', openExternalLink);
document.addEventListener('auxclick', openExternalLink);
portsTable('ports', {}, true);
controls();
poll();
