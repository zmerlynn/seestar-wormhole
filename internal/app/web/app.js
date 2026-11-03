// The seestar-copy app page: Setup, then the Browse and Settings tabs. The
// token in the URL fragment goes with every API call.
const $ = (s) => document.querySelector(s);
const token = location.hash.slice(1);
const form = $('#form');
const TABS = ['browse', 'settings'];
// Each field the server can refuse, and the control that shows it.
const FIELDS = { address: '#f-addr', folder: '#f-folder', interval: '#f-int', excludes: '#f-ex', includes: '#f-in' };

let saved = null; // the settings as saved
let passed = ''; // the address the last Test reached, until a Test of it fails

async function api(path, body) {
  const init = { headers: { 'X-Seestar-Copy-Token': token } };
  if (body !== undefined) {
    init.method = 'POST';
    init.headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(body);
  }
  const r = await fetch('/app/api/' + path, init);
  let data = null;
  try {
    data = await r.json();
  } catch {
    // A refused request answers in plain text.
  }
  return { status: r.status, data };
}

const lines = (s) => (s === '' ? [] : s.split('\n'));

function fill(s) {
  $('#f-addr').value = s.address;
  $('#f-folder').value = s.folder;
  $('#f-int').value = String(s.interval_minutes);
  $('#f-ex').value = s.excludes.join('\n');
  $('#f-in').value = s.includes.join('\n');
  $('#f-skip').checked = s.skip_built_in;
  $('#f-start').checked = s.start_copying;
}

function input() {
  return {
    address: $('#f-addr').value,
    folder: $('#f-folder').value,
    interval: $('#f-int').value,
    excludes: lines($('#f-ex').value),
    includes: lines($('#f-in').value),
    skip_built_in: $('#f-skip').checked,
    start_copying: $('#f-start').checked,
  };
}

// showErrors puts each message under its field and returns the first
// field in error, in page order.
function showErrors(fields) {
  let first = null;
  for (const [name, sel] of Object.entries(FIELDS)) {
    const el = $(sel);
    const msg = fields[name] || '';
    $(sel + '-err').textContent = msg;
    $(sel + '-err').hidden = !msg;
    if (msg) {
      el.setAttribute('aria-invalid', 'true');
      first = first || el;
    } else {
      el.removeAttribute('aria-invalid');
    }
  }
  return first;
}

function result(sel, text, cls) {
  const el = $(sel);
  el.className = 'result' + (cls ? ' ' + cls : '');
  el.textContent = text;
}

// The header's dot shows the last contact with the saved address only.
function contact(address, ok) {
  if (!saved || address !== saved.address) return;
  const dot = $('#hdot');
  dot.hidden = false;
  dot.className = 'dot ' + (ok ? 'ok' : 'err');
  dot.textContent = ok ? 'Reachable' : "Can't reach";
}

async function test() {
  const typed = $('#f-addr').value.trim();
  showErrors({ ...currentErrors(), address: '' });
  result('#f-test', 'Connecting to ' + (typed.includes(':') ? typed : typed + ':445') + '…');
  const { status, data } = await api('test', input());
  if (status === 400 && data && data.fields) {
    result('#f-test', '');
    showErrors({ ...currentErrors(), ...data.fields }).focus();
    return;
  }
  if (status !== 200 || !data) {
    result('#f-test', "Can't test now. Reload the window to retry.", 'err');
    return;
  }
  if (data.ok) passed = data.address;
  else if (data.address === passed) passed = '';
  result('#f-test', data.message, data.ok ? 'ok' : 'err');
  contact(data.address, data.ok);
}

// currentErrors is the messages shown now, keyed by field.
function currentErrors() {
  const out = {};
  for (const [name, sel] of Object.entries(FIELDS)) out[name] = $(sel + '-err').textContent;
  return out;
}

// save sends the form; Save is disabled until the answer is in, so two
// saves' answers can't arrive out of order.
async function save(ev) {
  ev.preventDefault();
  const btn = $('#f-save');
  if (btn.disabled) return;
  btn.disabled = true;
  try {
    await saveForm();
  } finally {
    btn.disabled = false;
  }
}

async function saveForm() {
  const setup = document.body.classList.contains('in-setup');
  result('#f-saved', '');
  const { status, data } = await api('settings', input());
  if (status === 400 && data && data.fields) {
    showErrors(data.fields).focus();
    return;
  }
  showErrors({});
  if (status !== 200 || !data) {
    result('#f-saved', (data && data.error) || "Couldn't save settings. Your changes have not been saved. Try saving again.", 'err');
    return;
  }
  const changed = !saved || saved.address !== data.settings.address;
  saved = data.settings;
  fill(saved);
  $('#haddr').textContent = saved.address;
  if (setup) {
    // Save went away with Setup: focus moves to the new state's heading.
    main();
    select('browse');
    $('#h-main').focus();
  } else {
    result('#f-saved', 'Settings saved.', 'ok');
  }
  if (data.browse_changed) $('#b-frame').src = '/';
  // The header shows a Test already passed for this address; otherwise
  // Test runs on Save, and a failure does not undo the save.
  if (saved.address === passed) {
    contact(saved.address, true);
  } else {
    if (changed) $('#hdot').hidden = true;
    test();
  }
}

function select(tab) {
  for (const t of TABS) {
    const on = t === tab;
    const btn = $('#t-' + t);
    btn.setAttribute('aria-selected', String(on));
    btn.tabIndex = on ? 0 : -1;
    $('#p-' + t).hidden = !on;
  }
}

function main() {
  document.body.classList.remove('in-setup');
  $('#setup').hidden = true;
  $('#main').hidden = false;
  $('#p-settings').insertBefore(form, $('#version'));
  $('#f-save').textContent = 'Save';
  result('#f-test', '');
}

async function start() {
  const { status, data } = await api('status');
  if (status !== 200 || !data) {
    result('#loading', "Can't load seestar-copy. Close this window and reopen seestar-copy.", 'err');
    return;
  }
  $('#loading').hidden = true;
  $('#version').textContent = 'seestar-copy ' + data.version;
  $('#f-builtin').textContent = data.default_excludes.join(' · ');
  fill(data.settings);
  form.hidden = false;
  if (data.set_up) {
    saved = data.settings;
    $('#haddr').textContent = saved.address;
    main();
    select('browse');
    $('#b-frame').src = '/';
  } else {
    document.body.classList.add('in-setup');
    $('#setup').hidden = false;
    $('#setup').after(form);
    $('#f-save').textContent = 'Save without copying';
    $('#f-addr').focus();
  }
}

$('#f-test-btn').addEventListener('click', test);
form.addEventListener('submit', save);
document.querySelector('nav.tabs').addEventListener('click', (ev) => {
  const t = ev.target.closest('.tab');
  if (t) select(t.dataset.tab);
});
// Tab keys: Arrow, Home and End move and select; focus follows.
document.querySelector('nav.tabs').addEventListener('keydown', (ev) => {
  const i = TABS.findIndex((t) => $('#t-' + t).getAttribute('aria-selected') === 'true');
  const n = TABS.length;
  const j = { ArrowRight: (i + 1) % n, ArrowLeft: (i + n - 1) % n, Home: 0, End: n - 1 }[ev.key];
  if (j === undefined) return;
  ev.preventDefault();
  select(TABS[j]);
  $('#t-' + TABS[j]).focus();
});
start();
