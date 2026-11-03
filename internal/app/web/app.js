// The seestar-copy app page: Setup, then the Copy, Browse and Settings
// tabs. The token in the URL fragment goes with every API call.
const $ = (s) => document.querySelector(s);
const token = location.hash.slice(1);
const form = $('#form');
const TABS = ['copy', 'browse', 'settings'];
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

// The header's dot shows the last contact with the saved address only:
// true, false or 'connecting'.
function contact(address, ok) {
  if (!saved || address !== saved.address) return;
  const dot = $('#hdot');
  dot.hidden = false;
  if (ok === 'connecting') {
    dot.className = 'dot';
    dot.textContent = 'Connecting…';
    return;
  }
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

// choose shows the window's folder picker; Cancel changes nothing.
async function choose() {
  const { status, data } = await api('folder', { folder: $('#f-folder').value });
  if (status === 200 && data && data.folder) {
    $('#f-folder').value = data.folder;
    showErrors({ ...currentErrors(), folder: '' });
  } else if (status !== 200) {
    showErrors({ ...currentErrors(), folder: (data && data.error) || "Couldn't open the folder picker. Type the folder's full path instead." });
  }
  $('#f-folder').focus();
}

// save sends the form; Save is disabled until the answer is in, so two
// saves' answers can't arrive out of order.
async function save(ev, startCopying = document.body.classList.contains('in-setup')) {
  ev.preventDefault();
  const btn = $('#f-save');
  if (btn.disabled) return;
  btn.disabled = true;
  $('#f-save-only').disabled = true;
  try {
    await saveForm(startCopying);
  } finally {
    btn.disabled = false;
    $('#f-save-only').disabled = false;
  }
}

async function saveForm(startCopying) {
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
    select('copy');
    if (startCopying) await copyAction('start');
    else await poll();
    $('#c-title').focus();
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
  if (!polling) polling = setInterval(poll, 1000);
}

// The Copy tab: the server words each state; the page shows its parts.
// Each state's buttons, and the states the help line shows in.
const SHOWS = {
  '#c-start': ['off', 'stopped', 'inuse', 'cantwrite'],
  '#c-check': ['uptodate', 'failures', 'listfail', 'unreachable'],
  '#c-stop': ['connecting', 'listing', 'copying', 'uptodate', 'failures', 'listfail', 'unreachable'],
  '#c-help': ['connecting', 'listing', 'copying', 'uptodate', 'failures', 'listfail', 'unreachable'],
};
const BUSY = { connecting: 'Connecting', listing: 'Looking for new files' };

let polling = 0;
let shown = { state: '', contact: '', files: '', seq: -1 };
let pressed = null; // a button the user pressed, until the state after it is shown

function text(sel, s) {
  $(sel).textContent = s || '';
}

function list(sel, items, li) {
  const ul = $(sel);
  ul.replaceChildren(...items.map(li));
}

function renderCopy(v) {
  const st = v.state;
  text('#c-title', v.title);
  text('#c-text', v.text);
  $('#c-box').className = ['listfail', 'inuse', 'cantwrite'].includes(st) ? 'notice err' : '';
  $('#c-busy').hidden = !BUSY[st];
  if (BUSY[st]) $('#c-busy').setAttribute('aria-label', BUSY[st]);
  const p = v.progress;
  $('#c-copying').hidden = !p;
  if (p) {
    $('#c-bar').max = p.total;
    $('#c-bar').value = p.done;
    text('#c-bytes', p.bytes);
    text('#c-speed', p.speed);
    text('#c-left', p.left);
    text('#c-found', p.found);
    list('#c-flight', p.flight, (f) => {
      const li = document.createElement('li');
      const name = document.createElement('span');
      name.textContent = f.path;
      const pct = document.createElement('span');
      pct.textContent = f.percent + '%';
      li.append(name, pct);
      return li;
    });
  }
  text('#c-summary', v.summary);
  text('#c-settling', v.settling);
  $('#c-failed').hidden = !v.failed;
  text('#c-notice', v.notice);
  const files = JSON.stringify(v.failed || []);
  if (files !== shown.files) {
    shown.files = files;
    list('#c-files', v.failed || [], (f) => {
      const li = document.createElement('li');
      li.textContent = f.path + ' — ' + f.reason;
      return li;
    });
  }
  for (const [sel, states] of Object.entries(SHOWS)) $(sel).hidden = !states.includes(st);
  // A stage change is announced; ticks within a stage are not.
  if (st !== shown.state) $('#c-live').textContent = v.title;
  shown.state = st;
  const chip = $('#hchip');
  chip.className = 'chip ' + (st === 'failures' ? 'warn' : v.on ? 'on' : 'off');
  chip.textContent = v.chip;
  if (v.contact !== shown.contact) {
    shown.contact = v.contact;
    if (v.contact) contact(saved.address, v.contact === 'connecting' ? 'connecting' : v.contact === 'ok');
  }
  // Settings can't change while copying is on.
  $('#f-set').disabled = v.on;
  $('#s-locked').hidden = !v.on;
  // When the button pressed went away, focus moves to the new state's
  // heading; background updates never move focus.
  if (pressed && pressed.offsetParent === null) {
    const lost = document.activeElement === pressed || document.activeElement === document.body;
    if (lost) (pressed.id === 's-stop' ? $('#f-addr') : $('#c-title')).focus();
    pressed = null;
  }
}

function renderActivity(lines, seq) {
  if (seq === shown.seq) return;
  shown.seq = seq;
  list('#c-log', lines, (e) => {
    const li = document.createElement('li');
    li.textContent = e.time + ' ' + e.text;
    if (e.warn) li.className = 'w';
    return li;
  });
}

async function poll() {
  const { status, data } = await api('status');
  if (status !== 200 || !data) return;
  renderCopy(data.copy);
  renderActivity(data.activity, data.activity_seq);
}

async function copyAction(action) {
  const { status, data } = await api('copy', { action });
  result('#c-err', status === 200 ? '' : (data && data.error) || "Couldn't reach seestar-copy. Close this window and reopen seestar-copy.", 'err');
  if (status === 200 && data) renderCopy(data);
  else pressed = null;
}

async function press(ev, action) {
  pressed = ev.currentTarget;
  await copyAction(action);
}

async function copyLog() {
  const btn = $('#c-clip');
  const all = [...$('#c-log').children].map((li) => li.textContent).join('\n');
  try {
    await navigator.clipboard.writeText(all);
    btn.textContent = 'Copied';
  } catch {
    btn.textContent = "Couldn't copy";
  }
  setTimeout(() => (btn.textContent = 'Copy to clipboard'), 2000);
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
  $('#f-choose').hidden = !data.native;
  form.hidden = false;
  if (data.set_up) {
    saved = data.settings;
    $('#haddr').textContent = saved.address;
    main();
    select('copy');
    $('#b-frame').src = '/';
    renderCopy(data.copy);
    renderActivity(data.activity, data.activity_seq);
  } else {
    document.body.classList.add('in-setup');
    $('#setup').hidden = false;
    $('#setup').after(form);
    $('#f-save').textContent = 'Save and start copying';
    $('#f-addr').focus();
  }
}

$('#f-test-btn').addEventListener('click', test);
$('#f-choose').addEventListener('click', choose);
form.addEventListener('submit', save);
$('#f-save-only').addEventListener('click', (ev) => save(ev, false));
$('#c-start').addEventListener('click', (ev) => press(ev, 'start'));
$('#c-check').addEventListener('click', (ev) => press(ev, 'check'));
$('#c-stop').addEventListener('click', (ev) => press(ev, 'stop'));
$('#s-stop').addEventListener('click', (ev) => press(ev, 'stop'));
$('#c-clip').addEventListener('click', copyLog);
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
