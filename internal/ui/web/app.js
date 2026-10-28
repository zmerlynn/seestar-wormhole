'use strict';
const $ = id => document.getElementById(id);
const tokenKey = 'seestar-control-token';
let token = location.hash.slice(1);
if (token) {
  try { sessionStorage.setItem(tokenKey, token); } catch (_) {}
  history.replaceState(null, '', '/');
} else {
  try { token = sessionStorage.getItem(tokenKey) || ''; } catch (_) {}
}
let initialized = false, ended = false, connected = false, pending = '', active = false;
let deferredFocus = '', lastFailure = '', state = null, selectedSaved = false, memory = false, editing = true, savedEmail = '', keyBusy = false;
let currentGoal = 'phone', proposedGoal = '', retryRemoval = false, diagnostic = '', noticeCode = '';
const lostCopy = 'Connection to the controls was lost. The proxy may still be running. Reopen seestar-wormhole to reconnect.';
const fieldCopy = {
  email: 'Enter your Seestar account email.', password: 'Enter a password or use a saved password.',
  idle: 'Enter an idle time greater than zero.', ports: 'This local port is used by more than one service.',
  discovery_bind: 'Phone discovery cannot use this address. Choose All networks or a local IPv4 network address.'
};
const goalNames = {phone: 'Phone on this network', computer: 'This computer only', custom: 'Custom'};
const defaultMappings = [[4700,4700],[4445,445],[8080,80],[4350,4350],[4554,4554],[4555,4555],[4801,4801],[4800,4800],[4804,4804],[4361,4361]];
async function api(action, body) {
  const options = {headers: {'X-Seestar-Token': token}, cache: 'no-store', redirect: 'error'};
  if (body !== undefined) {
    options.method = 'POST'; options.headers['Content-Type'] = 'application/json'; options.body = JSON.stringify(body);
  }
  const response = await fetch('/api/' + action, options);
  if (!response.ok) {
    let detail;
    try { detail = await response.json(); } catch (_) { throw {code: 'controls', message: lostCopy}; }
    throw detail;
  }
  return response.json();
}
function settings() {
  return {version: 1, bind: $('listen').value === 'other' ? $('bind').value.trim() : $('listen').value,
    discovery: $('discovery').checked, idle: $('mode').value === 'eager' ? '0' : Number($('idle').value) + 'm',
    serial: $('serial').value.trim(), model: $('model').value.trim(), ports: $('ports').value.split(/\s+/).filter(Boolean),
    email: $('email').value.trim(), remember_password: selectedSaved};
}
function goalFor(s) {
  if (s.bind === '0.0.0.0' && s.discovery && (s.ports || []).join() === '80:80') return 'phone';
  if (s.bind === '127.0.0.1' && !s.discovery && !(s.ports || []).length) return 'computer';
  return 'custom';
}
function load(s) {
  for (const name of ['email', 'bind', 'serial', 'model']) $(name).value = s[name] || '';
  $('listen').value = ['0.0.0.0', '127.0.0.1'].includes(s.bind) ? s.bind : 'other';
  $('discovery').checked = !!s.discovery;
  $('mode').value = s.idle_minutes > 0 ? 'idle' : 'eager';
  // The server projects parsed duration to minutes, including old compound durations.
  $('idle').value = s.idle_minutes || 2;
  $('ports').value = (s.ports || []).join('\n');
  currentGoal = goalFor(s); $('goal').value = currentGoal;
  selectedSaved = !!s.remember_password; savedEmail = selectedSaved ? s.email : '';
  editing = !selectedSaved;
  if (selectedSaved) $('password-note').textContent = 'A saved password is selected. Your OS may ask for permission when you connect.';
}
function text(id,value) {if ($(id).textContent !== value) $(id).textContent = value;}
function say(value) { text('feedback',value); }
function alertText(value) { text('notice',value); $('notice').hidden = !value; }
function clearErrors() {
  $('listen').removeAttribute('aria-invalid');
  for (const id of ['email','password','idle','bind','discovery','ports','serial','model']) {
    $(id).removeAttribute('aria-invalid'); $(id + '-error').hidden = true;
  }
}
function fieldError(field, message, code) {
  const e = $(field), help = $(field + '-error');
  if (!e || !help) return;
  if (e.closest('details')) $('advanced').open = true;
  const target = field === 'bind' && $('listen').value !== 'other' ? $('listen') : e;
  target.setAttribute('aria-invalid','true');
  if ($('options').disabled || $('settings').hidden) deferredFocus = target.id;
  else target.focus();
  help.textContent = message + ' [' + code + ']'; help.hidden = false;
}
function fail(e) {
  const code = e.code || 'controls';
  alertText((e.message || fieldCopy[code] || lostCopy) + ' [' + code + ']');
  if (e.field) fieldError(e.field, e.message || fieldCopy[code] || 'Check this setting.', code);
  if (code === 'password_remove') {retryRemoval = true;memory = false;}
  if (['password_read','password_missing','keychain_timeout'].includes(code)) {if (!editing) $('password').value = '';editing = true;selectedSaved = false;memory = false;}
  if (code === 'controls') connected = false;
}
function validate(passwordSave = false) {
  clearErrors(); $('listen').removeAttribute('aria-invalid');
  const s = settings();
  if (!$('email').value || !$('email').validity.valid) {fieldError('email',fieldCopy.email,'email');return false;}
  if (!$('password').value && !selectedSaved && !memory) {fieldError('password',fieldCopy.password,'password');return false;}
  if (passwordSave) return true;
  if ($('mode').value === 'idle' && !(Number($('idle').value) > 0)) {fieldError('idle',fieldCopy.idle,'idle');return false;}
  if (s.discovery && (s.bind === '127.0.0.1' || s.bind === '::1' || s.bind.includes(':'))) {fieldError('bind',fieldCopy.discovery_bind,'discovery_bind');return false;}
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
  $('idle-options').hidden = $('mode').value !== 'idle'; $('idle').disabled = $('mode').value !== 'idle';
  $('other-address').hidden = $('listen').value !== 'other';
  $('password').readOnly = !editing;
  $('password').type = editing ? 'password' : 'text';
  if (!editing) $('password').value = selectedSaved ? 'Saved in keychain' : 'This launch only';
  $('save-password').hidden = selectedSaved && !editing;
  $('save-password').disabled = blocked || !!pending || keyBusy || !$('email').validity.valid || !$('email').value || (!editing && !memory) || (editing && !$('password').value);
  $('change-password').hidden = editing;
  $('use-saved').hidden = !editing || !$('email').value;
  $('forget').hidden = savedEmail !== $('email').value || !savedEmail;
  $('forget').disabled = blocked || !!pending || keyBusy;
  $('forget').textContent = pending === 'forget' ? 'Removing saved password…' : retryRemoval ? 'Retry removal' : 'Remove saved password';
  $('retry-storage').disabled = blocked || !!pending;
  $('goal-help').textContent = currentGoal === 'phone' ? 'Keep this computer on the same network as your phone. In the Seestar app, use the LAN list.' : currentGoal === 'computer' ? 'Only apps on this computer can connect. A phone on the network cannot use this setup.' : 'Check Advanced settings before connecting.';
  $('trust').hidden = settings().bind === '127.0.0.1' || settings().bind === '::1';
  if (deferredFocus && !$('options').disabled && !$('settings').hidden) {$(deferredFocus).focus();deferredFocus = '';}
}
function render(s) {
  state = s; active = s.active; keyBusy = s.password.busy;
  text('status',s.status);
  $('last-known').hidden = true;
  $('quit').textContent = s.native ? 'Quit app' : 'Quit service';
  $('quit-help').textContent = s.native ? 'Quit stops the proxy and closes the app. Closing this window also quits.' : 'Closing this tab leaves the proxy running. Quit service stops it; you can then close this tab.';
  $('retry-storage').hidden = !s.save_failed && !s.storage_failed;
  if (s.save_failed) alertText('Connected. Settings could not be saved; they may reset next time.');
  else if (s.storage_failed) alertText('Password saved to keychain, but settings could not be saved. Keep this window open and retry saving settings.');
  if (s.failure && !active) {
    alertText(s.failure_message + ' [' + s.failure.code + ']');
    if (lastFailure !== s.failure.code && s.failure.field) fieldError(s.failure.field,s.failure_message + (s.failure.service ? ' Computer ' + s.failure.local + ' → telescope ' + s.failure.service + '.' : ''),s.failure.code);
    lastFailure = s.failure.code;
    if (['signin','password','password_read','password_missing','keychain_timeout'].includes(s.failure.code) && !editing) {editing = true;selectedSaved = false;$('password').value = '';}
  }
  if (active) lastFailure = '';
  if (s.password.code && s.password.code !== noticeCode) {
    noticeCode = s.password.code;
    if (noticeCode === 'password_saved' && s.settings.email === $('email').value) {selectedSaved = true;savedEmail = s.settings.email;memory = true;editing = false;$('password').value = '';}
    if (noticeCode === 'password_reading') say('Reading saved password…');
    else if (s.password.notice) say(s.password.notice);
  }
  const run = s.run_settings;
  const goal = goalFor(run);
  $('run-goal').textContent = 'Use with: ' + goalNames[goal];
  $('run-help').textContent = s.cancel ? 'You can cancel while the connection is being established.' : run.idle !== '0' ? 'The telescope connection starts when an app needs it.' : '';
  $('phone-steps').hidden = !(active && run.discovery && s.discovery_started);
  $('computer-steps').hidden = !(active && goal === 'computer' && !s.cancel);
  $('password-source').textContent = 'Password source: ' + (run.remember_password ? 'Saved in keychain' : 'This launch only');
  $('discovery-state').textContent = s.discovery_started ? 'Phone discovery is on.' : '';
  $('saved-state').textContent = s.settings_saved ? 'Settings saved.' : '';
  $('connection-mode').textContent = (active ? run.idle : settings().idle) === '0' ? 'Keep connected' : 'Connect when an app needs the telescope';
  const map = effectiveMappings(active ? run : settings());
  $('mappings').textContent = 'Computer → telescope\n' + map.map(p => p.join(' → ')).join('\n');
  $('events').textContent = s.events.join('\n');
  $('log-path').textContent = s.log_path ? 'Private rolling log: ' + s.log_path : '';
  $('log-error').hidden = !s.log_failed;
  // Copy only fixed lifecycle events and numeric service mappings. No path,
  // account, device selection, bind address, URL, token, or raw settings.
  diagnostic = 'seestar-wormhole\n' + $('connection-mode').textContent + '\n' + $('mappings').textContent + '\n' + s.events.join('\n');
  controls();
}
function effectiveMappings(s) {
  const overrides = (s.ports || []).map(p => p.split(':').map(Number));
  const result = defaultMappings.map(p => overrides.find(v => v[1] === p[1]) || p);
  for (const p of overrides) if (!result.some(v => v[1] === p[1])) result.push(p);
  return result.filter(p => p[0] > 0 && p.every(Number.isInteger));
}
async function refresh() {
  const s = await api('status');
  if (!connected && initialized) alertText('');
  connected = true;
  if (!initialized) {load(s.settings);initialized = true;}
  if (!active && s.active) $('password').value = '';
  if (active && !s.active && !s.failure) {
    memory = s.password.memory; editing = !selectedSaved && !memory;
    if (memory && !selectedSaved) $('password-note').textContent = 'Password available for this launch only.';
    if (editing) $('password').value = '';
  }
  const returning = active && !s.active;
  render(s);
  if (returning && !s.failure && !$('start').disabled) $('start').focus();
}
async function poll() {
  if (ended) return;
  try {await refresh();} catch (_) {
    connected = false; alertText(lostCopy);
    $('last-known').hidden = false; $('last-known').textContent = 'Last known state: ' + (state?.status || 'Loading…'); controls();
  }
  if (!ended) setTimeout(poll, 1000);
}
async function act(action) {
  if (pending && !['stop','quit'].includes(action)) return;
  if (['start','password/save'].includes(action) && !validate(action === 'password/save')) return;
  pending = action; alertText('');
  if (action === 'password/save') say('Saving password to keychain…');
  if (action === 'forget') say('Removing saved password…');
  controls();
  try {
    const typed = editing ? $('password').value : '';
    if (action === 'start') memory = !selectedSaved && (!!typed || memory);
    await api(action, {settings: settings(), password: ['start','password/save'].includes(action) ? typed : ''});
    if (action === 'password/save') {
      selectedSaved = true; savedEmail = $('email').value; editing = false; memory = true; $('password').value = '';
      $('password-note').textContent = 'Password saved to keychain. It will be checked when you connect.';
    }
    if (action === 'forget') {
      selectedSaved = false; savedEmail = ''; memory = false; editing = true; retryRemoval = false; $('password').value = '';
      $('password-note').textContent = 'Password removed from keychain and this app.';
    }
    if (action === 'quit') {
      ended = true;
      $('status').textContent = state?.native ? 'Quit complete.' : 'Service stopped. You can close this tab.';
      controls();return;
    }
    await refresh();
  } catch (e) {fail(e);} finally {pending = '';controls();if (action === 'start' && active && !$('start').disabled) $('start').focus();}
}
$('settings').addEventListener('submit', e => {e.preventDefault();if (!active && !$('start').disabled) act('start');});
$('start').addEventListener('click', () => {if (active) act('stop');});
for (const [id,action] of [['save-password','password/save'],['forget','forget'],['quit','quit'],['retry-storage','retry-storage']]) {
  $(id).addEventListener('click', () => act(id === 'retry-storage' && state?.save_failed ? 'save' : action));
}
$('change-password').addEventListener('click', () => {editing = true;selectedSaved = false;memory = false;$('password').value = '';$('password-note').textContent = 'Entering a password uses it for this launch. Save it to replace the keychain copy.';controls();$('password').focus();});
$('use-saved').addEventListener('click', () => {selectedSaved = true;editing = false;savedEmail = $('email').value;$('password').value = '';$('password-note').textContent = 'A saved password is selected. Your OS may ask for permission when you connect.';controls();});
$('email').addEventListener('input', () => {selectedSaved = false;memory = false;editing = true;$('password').value = '';$('password-note').textContent = 'Enter a password for this account.';controls();});
$('password').addEventListener('input', controls);
function applyGoal(goal) {
  currentGoal = goal; $('goal').value = goal; proposedGoal = ''; $('switch-confirm').hidden = true;
  if (goal !== 'custom') { $('listen').value = goal === 'phone' ? '0.0.0.0' : '127.0.0.1'; $('bind').value = $('listen').value; $('discovery').checked = goal === 'phone'; $('ports').value = goal === 'phone' ? '80:80' : ''; }
  controls();
}
$('goal').addEventListener('change', () => {
  const choice = $('goal').value;
  if (currentGoal === 'custom' && choice !== 'custom') {proposedGoal = choice;$('goal').value = currentGoal;$('switch-confirm').hidden = false;}
  else applyGoal(choice);
});
$('switch-setup').addEventListener('click', () => applyGoal(proposedGoal));
$('keep-custom').addEventListener('click', () => {proposedGoal = '';$('switch-confirm').hidden = true;});
for (const id of ['listen','bind','discovery','ports']) $(id).addEventListener('change', () => {currentGoal = 'custom';$('goal').value = 'custom';controls();});
$('mode').addEventListener('change', controls);
$('copy').addEventListener('click', async () => {
  try {await navigator.clipboard.writeText(diagnostic);say('Diagnostic summary copied.');}
  catch (_) { $('diagnostic-copy').hidden = false;$('diagnostic-copy').value = diagnostic;$('diagnostic-copy').focus();$('diagnostic-copy').select();say('Select and copy the diagnostic summary below.'); }
});
controls();poll();
