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
let fieldErrors = {}, portDefaults = [];
let currentGoal = 'phone', retryRemoval = false, diagnostic = '', noticeCode = '';
const lostCopy = 'Connection to the controls was lost. The proxy may still be running. Reopen the app to reconnect.';
const fieldCopy = {
  email: 'Enter your Seestar account email.', password: 'Enter a password or use a saved password.',
  idle: 'Enter a wait time greater than zero.'
};
async function api(action, body) {
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
  const response = await fetch('/api/' + action, options);
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
function accountEmail() {
  return $('email').value.replace(/ /g, '').trim();
}
function settings() {
  return {
    version: 1, bind: $('listen').value === 'other' ? $('bind').value.trim() : $('listen').value,
    discovery: $('discovery').checked, idle: $('mode').value === 'eager' ? '0' : Number($('idle').value) + 'm',
    serial: $('serial').value.trim(), model: $('model').value.trim(), ports: services.map(([device]) => ({device, local: Number($('port-' + device).value)})),
    email: accountEmail(), remember_password: usingSaved()
  };
}
const services = [
  [4700, 'Seestar control'], [80, 'Image downloads (web)'],
  [4800, 'Main camera images'], [4804, 'Wide camera images'],
  [4801, 'Album and files'], [4554, 'Main camera video'],
  [4555, 'Wide camera video (some models)'], [4350, 'Firmware updater'],
  [4361, 'Firmware upload'], [445, 'File sharing (SMB)']
];
function portValues(s) {
  const ports = Object.fromEntries(portDefaults.map(p => [p.device, p.local]));
  for (const p of s.ports || []) ports[p.device] = p.local;
  return ports;
}
function portsTable(id, ports, editable = false) {
  const table = document.createElement('table');
  const head = table.createTHead().insertRow();
  for (const name of ['Computer port', 'Seestar port', 'Purpose']) {
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
  $('listen').value = ['0.0.0.0', '127.0.0.1'].includes(s.bind) ? s.bind : 'other';
  $('discovery').checked = !!s.discovery;
  $('mode').value = s.idle_minutes > 0 ? 'idle' : 'eager';
  // The server projects parsed duration to minutes, including old compound durations.
  $('idle').value = s.idle_minutes || 2;
  portsTable('ports', portValues(s), true);
  currentGoal = goalFor(s);
  $('goal').value = currentGoal;

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
  if (e.closest('details')) $('advanced').open = true;
  const target = field === 'bind' && $('listen').value !== 'other' ? $('listen') : e;
  target.setAttribute('aria-invalid','true');
  fieldErrors[field] = code;
  if (focus) {
    controls();
    target.focus();
  }
  help.textContent = message;
  help.hidden = false;
}
function fail(e, focus = false) {
  const code = e.code || 'controls';
  actionError = code;
  alertText(e.field ? 'Check the highlighted setting.' : (e.message || fieldCopy[code] || lostCopy));
  if (e.field) fieldError(e.field, e.message || fieldCopy[code] || 'Check this setting.', code, focus);
  if (code === 'password_remove') {
    retryRemoval = true;
  }
  if (code === 'controls') connected = false;
}
function validate(passwordSave = false) {
  clearErrors();
  $('listen').removeAttribute('aria-invalid');
  if (!accountEmail()) {
    actionError = 'email';
    fieldError('email',fieldCopy.email,'email',true);
    return false;
  }
  if (!$('password').value && !usingSaved() && !passwordState().memory) {
    actionError = 'password';
    fieldError('password',fieldCopy.password,'password',true);
    return false;
  }
  if ($('listen').value === 'other' && !$('bind').value.trim()) {
    actionError = 'bind_empty';
    fieldError('bind', 'Enter a local network address, or choose This computer only or All networks.', 'bind_empty', true);
    return false;
  }
  if (passwordSave) return true;
  const locals = new Set();
  for (const [remote] of services) {
    const input = $('port-' + remote), value = Number(input.value);
    if (!input.validity.valid || (value && locals.has(value))) {
      actionError = 'ports';
      fieldError('ports', 'Use ports 1–65535, with no duplicates. Leave a port empty to turn off its service.', 'ports');
      input.setAttribute('aria-invalid', 'true');
      input.focus();
      return false;
    }
    if (value) locals.add(value);
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
  $('start').textContent = state?.stopping || pending === 'stop' ? 'Stopping…' : active ? (state?.cancel ? 'Cancel' : 'Stop') : 'Connect';
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
  $('save-password').disabled = blocked || !!pending || password.busy || !accountEmail() || !$('password').value;
  text('password-note', selectedSaved ? 'Saved in your keychain.' : 'Kept until you quit. Save to your keychain to use it next time.');
  $('forget').hidden = !password.saved;
  $('forget').disabled = blocked || !!pending || password.busy;
  $('forget').textContent = pending === 'forget' ? 'Removing saved password…' : retryRemoval ? 'Retry removal' : 'Remove saved password';
  $('retry-storage').disabled = blocked || !!pending;
  $('custom-help').hidden = currentGoal !== 'custom';
  $('goal-help').hidden = currentGoal === 'custom';
  $('goal-help').textContent = currentGoal === 'phone' ? 'Keep your phone and this computer on the same network.' : 'Only apps on this computer can connect.';
  $('trust').hidden = active || settings().bind === '127.0.0.1' || settings().bind === '::1';
  const code = actionError || (!active && state?.failure?.code) || '';
  $('port-permissions').hidden = code !== 'http_permission';
  text('error-code', code ? 'Error code: ' + code : '');
  $('error-code').hidden = !code;
  text('telescope-selection', $('serial').value.trim() || $('model').value.trim() ? 'Selection entered' : 'Automatic');
  $('advanced').querySelector('summary').tabIndex = blocked || active || !!pending ? -1 : 0;
}
function render(s) {
  state = s;
  text('version',s.version);
  active = s.active;
  text('status',s.status);
  $('last-known').hidden = true;
  $('quit').textContent = s.native ? 'Quit app' : 'Quit service';
  $('quit-help').textContent = s.native ? 'Closing this window stops the connection and quits the app.' : 'Closing this tab leaves the service running. Choose Quit service to stop it.';
  $('retry-storage').hidden = !s.save_failed;
  if (s.save_failed) alertText(s.password.code === 'password_removed' ? 'Password removed from keychain and this app. Settings could not be saved. Keep this window open and choose Retry saving settings.' : s.password.code === 'password_saved' ? 'Password saved to keychain. Settings could not be saved. Keep this window open and choose Retry saving settings.' : 'Settings could not be saved; they may reset next time.');
  if (s.failure && !active && !actionError) {
    const newFailure = lastFailure !== s.failure.code;
    alertText(s.failure.code === 'release' ? '' : s.failure.field ? 'Check the highlighted setting.' : s.failure_message);
    if (newFailure && s.failure.field) fieldError(s.failure.field,s.failure_message + (s.failure.service ? ' Computer port ' + s.failure.local + ' → Seestar port ' + s.failure.service + '.' : ''),s.failure.code);
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
  $('effective-settings').textContent = JSON.stringify(s.effective, null, 2);
  $('events').replaceChildren(...s.events.map(event => {
    const item = document.createElement('li');
    item.textContent = new Date(event.time).toLocaleTimeString([], {hour: 'numeric', minute: '2-digit'}) + '  ' + event.message;
    return item;
  }));
  $('log-path').textContent = s.log_path ? 'Private log: ' + s.log_path : '';
  $('log-error').hidden = !s.log_failed;
  diagnostic = JSON.stringify({app: 'seestar-wormhole', version: s.version, settings: s.effective, events: s.events}, null, 2);
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
  if (ended || generation !== statusGeneration) return;
  if (!connected && initialized) alertText('');
  connected = true;
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
  if (first) {
    // WKWebView can finish the status fetch before its native window is key.
    const focusStart = () => requestAnimationFrame(() => $('start').focus());
    if (document.hasFocus()) focusStart();
    else window.addEventListener('focus', focusStart, {once: true});
  }
}
async function poll() {
  if (ended) return;
  try {
    if (!pending || ['stop','quit'].includes(pending)) await refresh();
  } catch (_) {
    if (ended) return;
    connected = false;
    alertText(lostCopy);
    $('last-known').hidden = false;
    $('last-known').textContent = 'Last known state: ' + (state?.status || 'Loading…');
    controls();
  }
  if (!ended) setTimeout(poll, 1000);
}
async function act(action) {
  if (pending && !['stop','quit'].includes(action)) return;
  if (['start','password/save'].includes(action) && !validate(action === 'password/save')) return;
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
  let errorField = '';
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
      $('status').textContent = state?.native ? 'Quit complete.' : 'Service stopped. You can close this tab.';
      controls();
      return;
    }
    await refresh();
    if (['password/save','forget'].includes(action)) say(passwordState().notice || '');
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
      const field = errorField === 'bind' && $('listen').value !== 'other' ? 'listen' : errorField;
      $(field).focus();
    } else if (action === 'start' && active && !$('start').disabled) $('start').focus();
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
document.addEventListener('click', openExternalLink);
document.addEventListener('auxclick', openExternalLink);
portsTable('ports', {}, true);
controls();
poll();
