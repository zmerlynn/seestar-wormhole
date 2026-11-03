// The seestar-copy app page: the Copy, Browse, Free up space and Settings
// tabs. Until settings are saved, only Settings opens. The token in the URL
// fragment goes with every API call.
const $ = (s) => document.querySelector(s);
const token = location.hash.slice(1);
const form = $('#form');
const TABS = ['copy', 'browse', 'free', 'settings'];
// Each field the server can refuse, and the control that shows it.
const FIELDS = { address: '#f-addr', folder: '#f-folder', interval: '#f-int', excludes: '#f-ex', includes: '#f-in' };

let saved = null; // the settings as saved
let passed = ''; // the address the last Test reached, until a Test of it fails
let history = ''; // the saved folder's copy history file, from status

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
    renderHistory();
    showErrors({ ...currentErrors(), folder: '' });
  } else if (status !== 200) {
    showErrors({ ...currentErrors(), folder: (data && data.error) || "Couldn't open the folder picker. Type the folder's full path instead." });
  }
  $('#f-folder').focus();
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
  const first = !saved;
  result('#f-saved', '');
  result('#h-result', '');
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
  await poll();
  if (first) {
    // The first Save opens the other tabs and goes to Copy, with copying
    // off; focus moves to its heading.
    gate();
    select('copy');
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

// gate keeps every tab but Settings shut until settings are saved.
function gate() {
  for (const t of TABS) if (t !== 'settings') $('#t-' + t).disabled = !saved;
  $('#s-first').hidden = !!saved;
  $('#haddr').hidden = !saved;
}

// renderHistory shows the saved folder's history file, or, while the
// Folder field holds an unsaved folder, a line saying to save first.
function renderHistory() {
  const edited = !!saved && $('#f-folder').value !== saved.folder;
  $('#h-box').hidden = !history;
  $('#h-saved').hidden = edited;
  $('#h-unsaved').hidden = !edited;
  text('#h-path', history);
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

let copyOn = false;
let shown = { state: '', contact: '', files: '', seq: -1, preview: '', pgroups: '' };
let pressed = null; // a button the user pressed, until the state after it is shown

function text(sel, s) {
  $(sel).textContent = s || '';
}

function list(sel, items, li) {
  const ul = $(sel);
  ul.replaceChildren(...items.map(li));
}

// row is a table row for a folder: its cells end with the folder, a count
// and a size.
function row(...cells) {
  const tr = document.createElement('tr');
  const n = cells.length;
  tr.append(
    ...cells.map((c, i) => {
      const td = document.createElement('td');
      td.append(c);
      if (i >= n - 2) td.className = 'num';
      else if (i === n - 3) td.className = 'name';
      return td;
    }),
  );
  return tr;
}

// renderPreview shows the Copy tab's preview, if any, and its buttons in
// the states Start shows in. A preview's state change is announced, and so
// is its going away: a Cancel, or Copy's title when it is discarded.
function renderPreview(v, st) {
  const p = v.preview;
  const ps = p ? p.state : '';
  $('#c-preview').hidden = !p;
  $('#c-preview').className = ps === 'failed' ? 'notice err' : '';
  text('#c-ptitle', p && p.title);
  text('#c-ptext', p && p.text);
  text('#c-psub', p && p.sub);
  $('#c-pbusy').hidden = ps !== 'running';
  const groups = (p && p.groups) || [];
  $('#c-ptable').hidden = !groups.length;
  const key = JSON.stringify(groups);
  if (key !== shown.pgroups) {
    shown.pgroups = key;
    list('#c-pgroups', groups, (g) => row(g.name, String(g.count), g.size));
  }
  const off = SHOWS['#c-start'].includes(st);
  $('#c-pbtn').hidden = $('#c-pnote').hidden = !off || ps === 'running';
  // A preview that ends hides Cancel; focus on it moves to the result.
  const cancel = $('#c-pcancel');
  const lost = document.activeElement === cancel;
  cancel.hidden = ps !== 'running';
  if (lost && p && ps !== 'running') $('#c-ptitle').focus();
  if (ps !== shown.preview && p) $('#c-live').textContent = p.title;
  else if (ps !== shown.preview && shown.preview) {
    $('#c-live').textContent = pressed === cancel ? 'Preview canceled. Nothing was copied.' : v.title;
  }
  shown.preview = ps;
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
  renderPreview(v, st);
  const chip = $('#hchip');
  chip.className = 'chip ' + (st === 'failures' ? 'warn' : v.on ? 'on' : 'off');
  chip.textContent = v.chip;
  if (v.contact !== shown.contact) {
    shown.contact = v.contact;
    if (v.contact) contact(saved.address, v.contact === 'connecting' ? 'connecting' : v.contact === 'ok');
  }
  // Settings can't change, nor the history clear, while copying is on.
  $('#f-set').disabled = $('#f-set2').disabled = v.on;
  $('#s-locked').hidden = !v.on;
  copyOn = v.on;
  clearable();
  // When the button pressed went away, focus moves to the new state's
  // heading; background updates never move focus.
  if (pressed && pressed.offsetParent === null) {
    const lost = document.activeElement === pressed || document.activeElement === document.body;
    if (lost) $(pressed.id === 's-stop' ? '#f-addr' : pressed.id === 'c-pbtn' && v.preview ? '#c-ptitle' : '#c-title').focus();
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
  history = data.history;
  renderHistory();
  renderCopy(data.copy);
  renderFree(data.free);
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

// The Free up space tab: the server words each state, as for Copy. The
// options show in these states, and each button in its own.
const XSHOWS = {
  '#x-options': ['start', 'nothing', 'checkfail'],
  '#x-find': ['start', 'nothing', 'checkfail'],
  '#x-find-note': ['start', 'nothing', 'checkfail'],
  '#x-cancel': ['checking'],
  '#x-preview': ['preview'],
  '#x-go': ['preview'],
  '#x-change': ['preview'],
  '#x-deleting': ['deleting'],
  '#x-stop': ['deleting'],
  '#x-done': ['done', 'stopped', 'failed'],
};
const XBOX = { checkfail: 'notice err', failed: 'notice err', nothing: 'notice', stopped: 'notice', done: 'notice ok' };

let free = { state: '', id: 0, kept: '', groups: [], trust: false };
let xpressed = null; // as pressed, for this tab

// size words a byte count as the server does: decimal units, with one
// decimal below 100.
function size(n) {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (n >= 1000 && i < units.length - 1) {
    n /= 1000;
    i++;
  }
  if (i === 0) return n + ' B';
  return (n < 100 ? n.toFixed(1) : n.toFixed(0)) + ' ' + units[i];
}

const files = (n) => (n === 1 ? '1 file' : n.toLocaleString('en-US') + ' files');

function fillFree(s) {
  $('#x-days').value = String(s.older_than_days);
  $('#x-trust').checked = s.trust_state;
  intro();
}

// intro words the Start text for the options as set.
function intro() {
  text(
    '#x-intro',
    $('#x-trust').checked
      ? "Free up space uses seestar-copy's saved copy records. Your copies will not be checked."
      : 'Free up space deletes files from the telescope only after seestar-copy has copied them and checked that your copy still matches.',
  );
  const days = Number($('#x-days').value);
  $('#x-restack').hidden = !(days > 0);
}

function daysError(msg) {
  text('#x-days-err', msg);
  $('#x-days-err').hidden = !msg;
  if (msg) $('#x-days').setAttribute('aria-invalid', 'true');
  else $('#x-days').removeAttribute('aria-invalid');
}

function renderFree(v) {
  const st = v.state;
  text('#x-title', v.title);
  text('#x-text', v.text);
  text('#x-sub', v.sub);
  $('#x-box').className = XBOX[st] || '';
  $('#x-changed').hidden = !v.changed;
  const p = v.progress;
  $('#x-busy').hidden = st !== 'checking' || !!p;
  $('#x-hash').hidden = st !== 'checking' || !p;
  if (p && st === 'checking') {
    $('#x-hbar').max = p.total;
    $('#x-hbar').value = p.done;
    text('#x-htext', p.text);
  }
  if (p && st === 'deleting') {
    $('#x-dbar').max = p.total;
    $('#x-dbar').value = p.done;
  }
  text('#x-dfile', v.file);
  for (const [sel, states] of Object.entries(XSHOWS)) $(sel).hidden = !states.includes(st);
  if (v.stopping) $('#x-stop').hidden = true;
  // An open confirm belongs to the preview it words: it closes before
  // another preview, or none, replaces that one's id and folders.
  if ((st !== 'preview' || v.id !== free.id) && $('#x-confirm').open) $('#x-confirm').close();
  if (st === 'preview' && v.id !== free.id) {
    free.id = v.id;
    free.trust = v.trust;
    free.groups = v.groups;
    list('#x-groups', v.groups, (g, i) => {
      const box = document.createElement('input');
      box.type = 'checkbox';
      box.checked = true;
      box.dataset.i = String(i);
      box.setAttribute('aria-label', 'Include ' + g.name);
      const details = document.createElement('details');
      const summary = document.createElement('summary');
      summary.textContent = g.name;
      const ul = document.createElement('ul');
      ul.append(
        ...g.files.map((f) => {
          const li = document.createElement('li');
          const name = document.createElement('span');
          name.textContent = f.path;
          const sz = document.createElement('span');
          sz.textContent = f.size;
          li.append(name, sz);
          return li;
        }),
      );
      details.append(summary, ul);
      return row(box, details, String(g.count), g.size);
    });
    count();
  }
  const kept = JSON.stringify([v.kept, v.kept_list]);
  if (kept !== free.kept) {
    free.kept = kept;
    text('#x-ksum', v.kept || '');
    list('#x-klist', v.kept_list || [], (f) => {
      const li = document.createElement('li');
      li.textContent = f.path + ' — ' + f.reason;
      return li;
    });
  }
  $('#x-kept').hidden = !v.kept || !v.kept_list || !['preview', 'nothing', 'done'].includes(st);
  if (st !== free.state) $('#x-live').textContent = v.title || '';
  free.state = st;
  clearable();
  // As for Copy: focus moves to the new state's heading only when the
  // button pressed went away.
  if (xpressed && xpressed.offsetParent === null) {
    const lost = document.activeElement === xpressed || document.activeElement === document.body;
    if (lost) (v.title ? $('#x-title') : $('#x-intro')).focus();
    xpressed = null;
  }
}

// checked is the preview's groups whose box is checked.
function checked() {
  return [...$('#x-groups').querySelectorAll('input:checked')].map((b) => free.groups[Number(b.dataset.i)]);
}

// count labels Delete with the checked folders' files.
function count() {
  const n = checked().reduce((a, g) => a + g.files.length, 0);
  text('#x-go', 'Delete ' + files(n) + '…');
  $('#x-go').disabled = n === 0;
}

async function freeAction(body) {
  const { status, data } = await api('delete', body);
  if (status === 400 && data && data.fields) {
    daysError(data.fields.days || '');
    $('#x-days').focus();
    xpressed = null;
    return;
  }
  daysError('');
  result('#x-err', status === 200 ? '' : (data && data.error) || "Couldn't reach seestar-copy. Close this window and reopen seestar-copy.", 'err');
  if (status === 200 && data) renderFree(data);
  else xpressed = null;
}

async function freePress(ev, body) {
  xpressed = ev.currentTarget;
  await freeAction(body);
}

function find(ev) {
  return freePress(ev, { action: 'find', days: $('#x-days').value, trust: $('#x-trust').checked });
}

// ask shows the confirm dialog for the checked folders.
function ask() {
  const groups = checked();
  const n = groups.reduce((a, g) => a + g.files.length, 0);
  const bytes = groups.reduce((a, g) => a + g.bytes, 0);
  text('#x-ctitle', 'Delete ' + files(n) + ' from the telescope?');
  text(
    '#x-cbody',
    'This frees ' + size(bytes) + ". It can't be undone: the telescope keeps no trash. Each file is checked again just before it's deleted." +
      (free.trust ? ' Your copies were not checked.' : ''),
  );
  text('#x-cgo', 'Delete ' + files(n));
  $('#x-confirm').showModal();
}

async function confirmDelete() {
  const folders = checked().map((g) => g.folder);
  xpressed = $('#x-go');
  $('#x-confirm').close();
  await freeAction({ action: 'delete', id: free.id, folders });
}

// clip copies s to the clipboard and says so on the button for a moment.
// For a path, the status region live also says so, in words a screen
// reader announces.
async function clip(btn, s, live, copied) {
  const label = (btn.dataset.label ||= btn.textContent);
  try {
    await navigator.clipboard.writeText(s);
    btn.textContent = 'Copied';
    if (live) result(live, copied, 'ok');
  } catch {
    btn.textContent = "Couldn't copy";
    if (live) result(live, "Couldn't copy the path. Select it and copy it manually.", 'err');
  }
  setTimeout(() => (btn.textContent = label), 2000);
}

// clearable disables Clear history while copying is on or Free up space
// is checking or deleting; the server refuses those too.
function clearable() {
  $('#h-clear').disabled = copyOn || ['checking', 'deleting'].includes(free.state);
}

async function clearHistory() {
  $('#h-confirm').close();
  const { status, data } = await api('clear', {});
  if (status === 200) result('#h-result', 'Copy history cleared.', 'ok');
  else result('#h-result', (data && data.error) || "Couldn't reach seestar-copy. Close this window and reopen seestar-copy.", 'err');
  await poll();
}

async function start() {
  const { status, data } = await api('status');
  if (status !== 200 || !data) {
    result('#loading', "Can't load seestar-copy. Close this window and reopen seestar-copy.", 'err');
    return;
  }
  $('#loading').hidden = true;
  $('#version').textContent = 'seestar-copy ' + data.version;
  $('#s-file').textContent = data.settings_file;
  $('#f-builtin').textContent = data.default_excludes.join(' · ');
  fill(data.settings);
  fillFree(data.settings);
  $('#f-choose').hidden = !data.native;
  $('#main').hidden = false;
  if (data.set_up) {
    saved = data.settings;
    $('#haddr').textContent = saved.address;
    $('#b-frame').src = '/';
  }
  gate();
  history = data.history;
  renderHistory();
  renderCopy(data.copy);
  renderFree(data.free);
  renderActivity(data.activity, data.activity_seq);
  setInterval(poll, 1000);
  if (saved) {
    select('copy');
  } else {
    select('settings');
    $('#f-addr').focus();
  }
}

$('#f-test-btn').addEventListener('click', test);
$('#f-choose').addEventListener('click', choose);
form.addEventListener('submit', save);
$('#c-start').addEventListener('click', (ev) => press(ev, 'start'));
$('#c-check').addEventListener('click', (ev) => press(ev, 'check'));
$('#c-stop').addEventListener('click', (ev) => press(ev, 'stop'));
$('#c-pbtn').addEventListener('click', (ev) => press(ev, 'preview'));
$('#c-pcancel').addEventListener('click', (ev) => press(ev, 'cancel'));
$('#s-stop').addEventListener('click', (ev) => press(ev, 'stop'));
$('#c-clip').addEventListener('click', (ev) =>
  clip(ev.currentTarget, [...$('#c-log').children].map((li) => li.textContent).join('\n')),
);
$('#h-clip').addEventListener('click', (ev) => clip(ev.currentTarget, history, '#h-result', 'Copy history path copied.'));
$('#s-clip').addEventListener('click', (ev) =>
  clip(ev.currentTarget, $('#s-file').textContent, '#f-saved', 'Settings file path copied.'),
);
$('#f-folder').addEventListener('input', renderHistory);
$('#h-clear').addEventListener('click', () => {
  result('#h-result', '');
  $('#h-confirm').showModal();
});
$('#h-ccancel').addEventListener('click', () => $('#h-confirm').close());
$('#h-cgo').addEventListener('click', clearHistory);
// Escape and Cancel close the dialog; focus goes back to Clear history.
$('#h-confirm').addEventListener('close', () => $('#h-clear').focus());
document.querySelector('nav.tabs').addEventListener('click', (ev) => {
  const t = ev.target.closest('.tab');
  if (t) select(t.dataset.tab);
});
// Tab keys: Arrow, Home and End move and select among the open tabs;
// focus follows.
document.querySelector('nav.tabs').addEventListener('keydown', (ev) => {
  const open = TABS.filter((t) => !$('#t-' + t).disabled);
  const i = open.findIndex((t) => $('#t-' + t).getAttribute('aria-selected') === 'true');
  const n = open.length;
  const j = { ArrowRight: (i + 1) % n, ArrowLeft: (i + n - 1) % n, Home: 0, End: n - 1 }[ev.key];
  if (j === undefined) return;
  ev.preventDefault();
  select(open[j]);
  $('#t-' + open[j]).focus();
});
$('#x-find').addEventListener('click', (ev) => find(ev));
$('#x-days').addEventListener('keydown', (ev) => ev.key === 'Enter' && find(ev));
$('#x-days').addEventListener('input', intro);
$('#x-trust').addEventListener('change', intro);
$('#x-cancel').addEventListener('click', (ev) => freePress(ev, { action: 'cancel' }));
$('#x-stop').addEventListener('click', (ev) => freePress(ev, { action: 'stop' }));
$('#x-change').addEventListener('click', (ev) => freePress(ev, { action: 'reset' }));
$('#x-done').addEventListener('click', (ev) => freePress(ev, { action: 'reset' }));
$('#x-go').addEventListener('click', ask);
$('#x-groups').addEventListener('change', count);
$('#x-ccancel').addEventListener('click', () => $('#x-confirm').close());
$('#x-cgo').addEventListener('click', confirmDelete);
// Escape and Cancel close the dialog; focus goes back to Delete.
$('#x-confirm').addEventListener('close', () => {
  if (!$('#x-go').hidden) $('#x-go').focus();
});
start();
