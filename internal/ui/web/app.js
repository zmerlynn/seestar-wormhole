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
let lastFailure = '', state = null, sourceChoice = '', statusGeneration = 0, actionError = '';
let fieldErrors = {};
let currentGoal = 'phone', proposedGoal = '', retryRemoval = false, diagnostic = '', noticeCode = '';
const lostCopy = 'Connection to the controls was lost. The proxy may still be running. Reopen the app to reconnect.';
const fieldCopy = {
  email: 'Enter your Seestar account email.', password: 'Enter a password or use a saved password.',
  idle: 'Enter an idle time greater than zero.'
};
const goalNames = {
  phone: 'Phone on this network', computer: 'This computer only', custom: 'Custom'
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
function settings() {
  return {
    version: 1, bind: $('listen').value === 'other' ? $('bind').value.trim() : $('listen').value,
    discovery: $('discovery').checked, idle: $('mode').value === 'eager' ? '0' : Number($('idle').value) + 'm',
    serial: $('serial').value.trim(), model: $('model').value.trim(), ports: $('ports').value.split(/\s+/).filter(Boolean),
    email: $('email').value.trim(), remember_password: usingSaved()
  };
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
  currentGoal = goalFor(s);
  $('goal').value = currentGoal;

}
// The status projection is account-scoped and contains booleans, never a secret.
// Only an explicit draft/source choice belongs to the page.
function passwordState() {
  return state?.settings.email === $('email').value.trim() ? state.password : {};
}
function usingSaved() {
  return !!passwordState().saved && sourceChoice !== 'typed';
}
function editingPassword() {
  return sourceChoice === 'typed' || (!usingSaved() && !passwordState().memory);
}
function clearField(field) {
  $(field).removeAttribute('aria-invalid');
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
  if (['password_read','password_missing','keychain_timeout'].includes(code)) {
    if (!editingPassword()) $('password').value = '';
    sourceChoice = 'typed';
  }
  if (code === 'controls') connected = false;
}
function validate(passwordSave = false) {
  clearErrors();
  $('listen').removeAttribute('aria-invalid');
  if (!$('email').value || !$('email').validity.valid) {
    actionError = 'email';
    fieldError('email',fieldCopy.email,'email',true);
    return false;
  }
  if (!$('password').value && !usingSaved() && (sourceChoice === 'typed' || !passwordState().memory)) {
    actionError = 'password';
    fieldError('password',fieldCopy.password,'password',true);
    return false;
  }
  if (passwordSave) return true;
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
  const editing = editingPassword();
  const selectedSaved = usingSaved();
  const password = passwordState();
  if (editing && $('password').readOnly) $('password').value = '';
  $('password').readOnly = !editing;
  $('password').type = editing ? 'password' : 'text';
  if (!editing) $('password').value = selectedSaved ? 'Saved in keychain' : 'This launch only';
  $('save-password').hidden = selectedSaved && !editing;
  $('save-password').disabled = blocked || !!pending || password.busy || !$('email').validity.valid || !$('email').value || (!editing && !password.memory) || (editing && !$('password').value);
  $('change-password').hidden = editing;
  text('password-note', editing ? 'Used for this launch unless you save it to your OS keychain.' : selectedSaved ? 'A saved password is selected. Your OS may ask for permission when you connect.' : 'Password available for this launch only.');
  $('use-saved').hidden = !editing || !password.saved;
  $('forget').hidden = !password.saved;
  $('forget').disabled = blocked || !!pending || password.busy;
  $('forget').textContent = pending === 'forget' ? 'Removing saved password…' : retryRemoval ? 'Retry removal' : 'Remove saved password';
  $('retry-storage').disabled = blocked || !!pending;
  $('goal-help').textContent = currentGoal === 'phone' ? 'Keep this computer on the same network as your phone. In the Seestar app, use the LAN list.' : currentGoal === 'computer' ? 'Only apps on this computer can connect. A phone on the network cannot use this setup.' : 'Check Advanced settings before connecting.';
  $('trust').hidden = active || settings().bind === '127.0.0.1' || settings().bind === '::1';
  const code = actionError || (!active && state?.failure?.code) || '';
  text('error-code', code ? 'Error code: ' + code : '');
  $('error-code').hidden = !code;
  text('telescope-selection', $('serial').value.trim() || $('model').value.trim() ? 'Selected in Advanced' : 'Automatic (one telescope on this account)');
  $('advanced').querySelector('summary').tabIndex = blocked || active || !!pending ? -1 : 0;
}
function render(s) {
  state = s;
  active = s.active;
  text('status',s.status);
  $('last-known').hidden = true;
  $('quit').textContent = s.native ? 'Quit app' : 'Quit service';
  $('quit-help').textContent = s.native ? 'Quit stops the proxy and closes the app. Closing this window also quits.' : 'Closing this tab leaves the proxy running. Quit service stops it; you can then close this tab.';
  $('retry-storage').hidden = !s.save_failed;
  if (s.save_failed) alertText(s.password.code === 'password_removed' ? 'Password removed from keychain and this app, but settings could not be saved. Keep this window open and retry saving settings.' : s.password.code === 'password_saved' ? 'Password saved to keychain, but settings could not be saved. Keep this window open and retry saving settings.' : active ? 'Connected. Settings could not be saved; they may reset next time.' : 'Settings could not be saved; they may reset next time.');
  if (s.failure && !active && !actionError) {
    const newFailure = lastFailure !== s.failure.code;
    alertText(s.failure.code === 'release' ? '' : s.failure.field ? 'Check the highlighted setting.' : s.failure_message);
    if (newFailure && s.failure.field) fieldError(s.failure.field,s.failure_message + (s.failure.service ? ' Computer ' + s.failure.local + ' → telescope ' + s.failure.service + '.' : ''),s.failure.code);
    lastFailure = s.failure.code;
    if (newFailure && ['signin','password','password_read','password_missing','keychain_timeout'].includes(s.failure.code)) {
      if (!editingPassword()) $('password').value = '';
      sourceChoice = 'typed';
    }
  }
  if (active) lastFailure = '';
  if (pending !== 'quit' && s.password.code && s.password.code !== noticeCode) {
    noticeCode = s.password.code;
    if (['password_saved','password_removed'].includes(noticeCode) && state.settings.email === $('email').value.trim()) say(s.password.notice);
    else say('');
  }
  const run = s.run_settings;
  const goal = goalFor(run);
  $('run-summary').textContent = goalNames[goal] + ' · ' + (run.remember_password ? 'password saved in keychain' : 'password for this launch only') + ' · discovery ' + (s.discovery_started ? 'on' : 'off');
  $('run-help').textContent = s.cancel ? 'You can cancel while the connection is being established.' : '';
  $('phone-steps').hidden = !(active && run.discovery && s.discovery_started);
  $('computer-steps').hidden = !(active && goal === 'computer' && !s.cancel);
  $('connection-mode').textContent = 'Connection timing: ' + ((active ? run.idle : settings().idle) === '0' ? 'Keep connected' : 'Connect when an app needs the telescope');
  const map = s.mappings;
  $('mappings').textContent = 'Computer → telescope\n' + map.map(p => p.join(' → ')).join('\n');
  $('events').textContent = s.events.join('\n');
  $('log-path').textContent = s.log_path ? 'Private rolling log: ' + s.log_path : '';
  $('log-error').hidden = !s.log_failed;
  // Copy only fixed lifecycle events and numeric service mappings. No path,
  // account, device selection, bind address, URL, token, or raw settings.
  diagnostic = 'seestar-wormhole\n' + $('connection-mode').textContent + '\n' + $('mappings').textContent + '\n' + s.events.join('\n');
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
    load(s.settings);
    initialized = true;
  }
  if (!active && s.active) {
    $('password').value = '';
    sourceChoice = '';
  }
  render(s);
  if (first) $('start').focus();
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
  if (action === 'quit') say('Quitting — releasing the telescope session…');
  controls();
  let errorField = '';
  try {
    const typed = editingPassword() ? $('password').value : '';
    await api(action, {
      settings: settings(), password: ['start','password/save'].includes(action) ? typed : ''
    });
    if (ended || pending !== action) return;
    if (action === 'start') lastFailure = '';
    if (action === 'password/save' || action === 'forget') {
      sourceChoice = action === 'forget' ? 'typed' : '';
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
      e.message = action === 'password/save' ? 'Password saved to keychain, but settings could not be saved. Keep this window open and retry saving settings.' : 'Password removed from keychain and this app, but settings could not be saved. Keep this window open and retry saving settings.';
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
$('change-password').addEventListener('click', () => {
  sourceChoice = 'typed';
  $('password').value = '';
  controls();
});
$('use-saved').addEventListener('click', () => {
  if (!passwordState().saved) return;
  sourceChoice = 'saved';
  $('password').value = '';
  controls();
});
$('email').addEventListener('input', () => {
  sourceChoice = 'typed';
  $('password').value = '';
  controls();
});
$('password').addEventListener('input', controls);
function applyGoal(goal) {
  currentGoal = goal;
  $('goal').value = goal;
  proposedGoal = '';
  $('switch-confirm').hidden = true;
  if (goal !== 'custom') {
    $('listen').value = goal === 'phone' ? '0.0.0.0' : '127.0.0.1';
    $('bind').value = $('listen').value;
    $('discovery').checked = goal === 'phone';
    $('ports').value = goal === 'phone' ? '80:80' : '';
  }
  controls();
}
$('goal').addEventListener('change', () => {
  const choice = $('goal').value;
  if (currentGoal === 'custom' && choice !== 'custom') {
    proposedGoal = choice;
    $('goal').value = currentGoal;
    $('switch-confirm').hidden = false;
  } else applyGoal(choice);
});
$('switch-setup').addEventListener('click', () => applyGoal(proposedGoal));
$('keep-custom').addEventListener('click', () => {
  proposedGoal = '';
  $('switch-confirm').hidden = true;
});
for (const id of ['listen','bind','discovery','ports']) $(id).addEventListener('change', () => {
  currentGoal = 'custom';
  $('goal').value = 'custom';
  controls();
});
$('mode').addEventListener('change', controls);
for (const id of ['serial', 'model']) $(id).addEventListener('input', controls);
$('copy').addEventListener('click', async () => {
  try {
    await navigator.clipboard.writeText(diagnostic + '\n' + $('error-code').textContent);
    say('Diagnostic summary copied.');
  } catch (_) {
    $('diagnostic-copy').hidden = false;
    $('diagnostic-copy').value = diagnostic + '\n' + $('error-code').textContent;
    $('diagnostic-copy').focus();
    $('diagnostic-copy').select();
    say('Select and copy the diagnostic summary below.');
  }
});
controls();
poll();
