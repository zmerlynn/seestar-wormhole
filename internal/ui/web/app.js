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
let initialized = false;
let ended = false;
let active = false;
async function api(action, body) {
  const options = {headers: {'X-Seestar-Token': token}, cache: 'no-store', redirect: 'error'};
  if (body !== undefined) {
    options.method = 'POST';
    options.headers['Content-Type'] = 'application/json';
    options.body = JSON.stringify(body);
  }
  const response = await fetch('/api/' + action, options);
  if (!response.ok) throw new Error('Action failed. Check settings, credentials, and private storage. Reopen the app if this page has lost access.');
  return response.json();
}
function settings() {
  return {version: 1, bind: $('bind').value, idle: $('mode').value === 'eager' ? '0' : $('idle').value,
    serial: $('serial').value, model: $('model').value, ports: $('ports').value.split(/\s+/).filter(Boolean),
    email: $('email').value, remember_password: $('remember').checked};
}
function load(s) {
  for (const name of ['email', 'bind', 'serial', 'model']) $(name).value = s[name];
  $('remember').checked = s.remember_password;
  $('mode').value = s.idle === '0' || s.idle === '0s' ? 'eager' : 'idle';
  $('idle').value = $('mode').value === 'eager' ? '2m' : s.idle;
  $('ports').value = (s.ports || []).join('\n');
}
function controls() {
  $('options').disabled = active;
  $('start').disabled = active;
  $('stop').disabled = !active;
  $('save').disabled = active;
  $('idle').disabled = $('mode').value === 'eager';
}
$('mode').addEventListener('change', controls);
async function poll() {
  if (ended) return;
  try {
    const s = await api('status');
    if (!initialized) {load(s.settings); initialized = true;}
    active = s.active;
    controls();
    $('status').textContent = s.status;
    if (s.notice) $('password-note').textContent = s.notice;
    else if (s.password_available) $('password-note').textContent = 'A password is available in memory.';
  } catch (_) { $('status').textContent = 'Control page disconnected. Reopen the app to reconnect.'; }
  if (!ended) setTimeout(poll, 1000);
}
for (const action of ['start', 'stop', 'save', 'forget', 'quit']) {
  $(action).addEventListener('click', async () => {
    $(action).disabled = true;
    try {
      const body = {settings: settings(), password: action === 'start' || action === 'save' ? $('password').value : ''};
      await api(action, body);
      if (action === 'start' || action === 'save' || action === 'forget') $('password').value = '';
      $('notice').textContent = action === 'save' ? 'Settings saved.' : '';
      if (action === 'quit') {
        ended = true;
        $('status').textContent = 'Quitting — releasing session. Wait for the process to exit.';
        for (const id of ['options', 'start', 'stop', 'save', 'quit']) $(id).disabled = true;
      }
    } catch (e) { $('notice').textContent = e.message; }
    finally { if (!ended) { controls(); $('quit').disabled = false; } }
  });
}
$('settings').addEventListener('submit', e => e.preventDefault());
poll();
