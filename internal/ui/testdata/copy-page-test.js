// Copy, Browse and Free up space, against the production markup and the
// server's own statuses.
const copyAssert = (ok, message) => {if (!ok) throw new Error(message);};

function copyFixture(st, status) {
  initialized = connected = true;
  pending = lanPending = '';
  lanReach = null;
  load(status.settings);
  render(status);
}

async function checkCopyUnlock(st) {
  copyAssert(!st.locked.copy_unlocked && st.unlocked.copy_unlocked && st.unlocked.copy_reach === '', 'server statuses changed shape');
  copyFixture(st, st.locked);
  for (const tab of copyTabNames) {
    copyAssert($('tab-' + tab).disabled && $('tab-' + tab).getAttribute('aria-describedby') === 'tabs-note', tab + ' opened before a route was saved');
  }
  copyAssert(!$('tabs-note').hidden, 'the locked tabs lost their note');
  render(st.unlocked);
  for (const tab of copyTabNames) {
    copyAssert(!$('tab-' + tab).disabled && $('tab-' + tab).getAttribute('aria-describedby') === null, tab + ' did not open after a LAN save');
  }
  copyAssert($('tabs-note').hidden, 'the note stayed after the tabs opened');
  selectTab('copy');
  copyAssert(currentTab === 'copy' && !$('panel-copy').hidden && $('panel-connection').hidden, 'Copy could not be selected');
  copyAssert($('copy-reach').hidden, 'a ready route showed the reach notice');
  // Each reach state words the notice, and its link opens what it names.
  for (const [reach, wording, link, focus] of [
    ['not_connected', 'Not connected.', 'Go to Connection', 'start'],
    ['smb_off', 'SMB is off. Turn on its port in Connection > Advanced.', 'Open Advanced', 'ports'],
    ['no_lan_address', "Save the telescope's address on the Connection tab first.", 'Go to Connection', 'lan-address']
  ]) {
    render({...st.unlocked, copy_reach: reach});
    selectTab('free');
    copyAssert(!$('copy-reach').hidden && $('copy-reach-text').textContent === wording && $('copy-reach-go').textContent === link, reach + ' notice');
    $('advanced').open = false;
    goReach();
    copyAssert(currentTab === 'connection' && !$('panel-connection').hidden && $('copy-reach').hidden, reach + ' link did not open Connection');
    copyAssert(document.activeElement === $(focus) || document.activeElement === $('tab-connection'), reach + ' link focused ' + JSON.stringify(document.activeElement?.attributes));
    copyAssert(($('advanced').open === true) === (reach === 'smb_off'), reach + ' Advanced');
    if (reach === 'no_lan_address') copyAssert(routeChoice === 'lan', 'the LAN link did not choose On this network');
  }
  // Only a repeating copy, and only on Copy, says it resumes.
  render({...st.unlocked, copy_reach: 'not_connected'});
  for (const [copy, settings, tab, wording] of [
    [st.after.copy, st.after.settings, 'copy', 'Not connected.'],
    [{...st.after.copy, on: true}, st.after.settings, 'copy', 'Not connected. Copying resumes at the next check after you connect.'],
    [{...st.after.copy, on: true}, st.after.settings, 'browse', 'Not connected.'],
    [{...st.after.copy, on: true}, st.after.settings, 'free', 'Not connected.'],
    [{...st.after.copy, on: true}, {...st.after.settings, once: true}, 'copy', 'Not connected.']
  ]) {
    copyRender({...st.after, copy, settings});
    selectTab(tab);
    copyAssert(!$('copy-reach').hidden && $('copy-reach-text').textContent === wording, tab + (copy.on ? ' on' : ' off') + (settings.once ? ' once' : '') + ': ' + $('copy-reach-text').textContent);
  }
  // Losing the route while on a copy tab falls back to Connection.
  render(st.unlocked);
  selectTab('browse');
  render(st.locked);
  copyAssert(currentTab === 'connection' && $('panel-browse').hidden && $('tab-browse').disabled, 'a locked tab stayed selected');
  // The header chip shows only once copy is set up.
  copyRender(st.before);
  copyAssert($('copy-chip').hidden, 'the copy chip shows before setup');
}

async function checkCopySetup(st) {
  copyFixture(st, st.unlocked);
  copyAssert(!st.before.set_up && st.after.set_up && st.saved.browse_changed, 'server statuses changed shape');
  copyRender(st.before);
  copyAssert($('c-main').hidden && !$('s-first').hidden && $('s-line').hidden, 'the first visit shows the copy controls');
  copyAssert(!$('copy-form').hidden && $('f-cancel').hidden && $('f-save').textContent === 'Save', 'the first visit lacks the form or Save, or has Cancel');
  copyAssert($('f-builtin').textContent === st.before.default_excludes.join(' · '), 'built-in excludes');
  copyAssert($('b-frame').src === undefined || $('b-frame').src === '', 'Browse loaded before settings');
  // A field error goes under its field and takes focus.
  const calls = [];
  api = async (action, body, base) => {
    calls.push([action, base]);
    throw {fields: {folder: 'Choose a folder.'}};
  };
  await copySave();
  copyAssert(!$('f-folder-err').hidden && $('f-folder-err').textContent === 'Choose a folder.' && $('f-folder').getAttribute('aria-invalid') === 'true', 'the folder error is not shown');
  copyAssert(document.activeElement === $('f-folder') && !$('f-save').disabled, 'the folder error did not take focus');
  copyAssert(calls.length === 1 && calls[0][0] === 'settings' && calls[0][1] === '/copy/api/', 'Save posted elsewhere');
  // The first Save: the summary replaces the form, and Browse loads.
  let sent = null;
  api = async (action, body) => {
    if (action === 'settings') {
      sent = body;
      return st.saved;
    }
    return st.after;
  };
  $('f-folder').value = st.saved.settings.folder;
  $('f-ex').value = 'Video';
  await copySave();
  copyAssert(sent && sent.folder === st.saved.settings.folder && sent.interval === '5' && sent.files_at_once === '4' && sent.excludes.join() === 'Video' && sent.includes.length === 0, 'Save sent ' + JSON.stringify(sent));
  copyAssert($('f-folder-err').hidden && !$('f-folder').getAttribute('aria-invalid'), 'the old error stayed');
  copyAssert(!$('c-main').hidden && $('s-first').hidden && !$('s-line').hidden && $('copy-form').hidden, 'the summary did not replace the form');
  copyAssert($('s-summary').textContent === 'Copying to ' + st.saved.settings.folder + ' every 5 min, skipping logs and system files, leaving out Video.', 'summary: ' + $('s-summary').textContent);
  copyAssert($('b-frame').src === '/browse/', 'Browse did not load after the first save');
  copyAssert(document.activeElement === $('c-next') && !$('c-next').hidden, 'the first save did not say Start copying is next');
  copyAssert(!$('h-details').hidden && $('h-path').textContent === st.after.history, 'Copy history');
  // Change… opens the form; Cancel puts the saved values back.
  openCopyForm();
  copyAssert(!$('copy-form').hidden && $('s-change').getAttribute('aria-expanded') === 'true' && !$('f-cancel').hidden && $('f-save').textContent === 'Save', 'Change… did not open the form');
  $('f-folder').value = '/elsewhere';
  renderHistory();
  copyAssert($('h-saved').hidden && !$('h-unsaved').hidden, 'an unsaved folder showed the old history');
  closeCopyForm();
  copyAssert($('copy-form').hidden && $('f-folder').value === st.saved.settings.folder && $('s-change').getAttribute('aria-expanded') === 'false' && document.activeElement === $('s-change'), 'Cancel kept the edit');
  // A later save says so in place.
  openCopyForm();
  await copySave();
  copyAssert($('f-saved').textContent === 'Settings saved.' && document.activeElement === $('s-change'), 'a later save lacks its confirmation');
  copyAssert($('copy-chip').hidden && !st.after.copy.chip, 'the copy chip shows while copying is off');
  copyAssert($('preview-chip').hidden && $('free-chip').hidden, 'an idle preview or delete showed its bubble');
  // While copying runs, its chip shows and opens Copy.
  const running = {...st.after, copy: {...st.after.copy, state: 'copying', on: true, chip: 'Copying'}};
  copyRender(running);
  copyAssert(!$('copy-chip').hidden && $('copy-chip').textContent === 'Copying' && $('copy-chip').className === 'chip on', 'the copy chip is hidden while copying');
  selectTab('connection');
  $('copy-chip').dispatchEvent({type: 'click'});
  copyAssert(currentTab === 'copy' && document.activeElement === $('tab-copy'), 'the copy chip did not open Copy');
  render(st.locked);
  copyRender(running);
  copyAssert($('copy-chip').hidden, 'the copy chip shows without a route');
}

async function checkActivityMerge(st) {
  copyFixture(st, st.unlocked);
  const events = [
    {time: '2026-01-01T19:10:00Z', message: 'Account telescopes: S30 synthetic', code: 'lifecycle', phase: '', reason: ''},
    {time: '2026-01-01T19:30:00Z', message: 'Connected to your Seestar', code: 'lifecycle', phase: '', reason: ''}
  ];
  const activity = [
    {time: '7:40 PM', at: '2026-01-01T19:40:00Z', source: 'delete', text: 'Deleted 2 files (6 B).'},
    {time: '7:20 PM', at: '2026-01-01T19:20:00Z', source: 'copy', warn: true, text: "Couldn't copy 1 file."}
  ];
  render({...st.unlocked, events, log_path: 'synthetic-log'});
  copyRender({...st.after, activity, activity_seq: 2});
  const want = [['delete', 'Deleted 2 files (6 B).'], ['connection', 'Connected to your Seestar'], ['copy', "Couldn't copy 1 file."], ['connection', 'Account telescopes: S30 synthetic']];
  const names = {connection: 'Connection', copy: 'Copy', delete: 'Free up space'};
  const rows = $('events').children;
  copyAssert(rows.length === want.length, 'merged ' + rows.length + ' rows');
  want.forEach(([source, message], i) => {
    copyAssert(rows[i].textContent.endsWith('  ' + names[source] + '  ' + message) && rows[i].getAttribute('data-source') === source, 'row ' + i + ': ' + rows[i].textContent);
  });
  copyAssert(rows[2].getAttribute('data-warn') === 'true' && rows[0].getAttribute('data-warn') === null, 'the warning row is not marked');
  const copied = JSON.parse(diagnostic);
  copyAssert(Object.keys(copied).join() === 'app,version,error_code,settings,copy_settings,events,log_path', 'diagnostic keys ' + Object.keys(copied));
  copyAssert(JSON.stringify(copied.copy_settings) === JSON.stringify(st.after.settings) && JSON.stringify(copied.settings) === JSON.stringify(st.unlocked.effective), 'diagnostic settings');
  copyAssert(copied.log_path === 'synthetic-log' && copied.events.map(e => e.source + ':' + e.message).join('|') === want.map(w => w.join(':')).join('|'), 'diagnostic events or log path');
  copyAssert($('copy-settings').textContent === JSON.stringify(st.after.settings, null, 2), 'Copy settings in Details');
  copyAssert($('config-file').textContent.startsWith('Connection settings: ') && $('copy-file').textContent.startsWith('Copy settings: '), 'Files lacks the settings files');
  // An unchanged poll writes nothing.
  const before = Array.from(rows);
  copyRender({...st.after, activity, activity_seq: 2});
  copyAssert(Array.from($('events').children).every((row, i) => row === before[i]), 'an unchanged poll replaced the rows');
}

async function checkFreePreview(st) {
  copyFixture(st, st.unlocked);
  copyRender(st.after);
  copyAssert(st.after.free.state === 'start' && !$('x-find').hidden && $('x-go').hidden, 'Free starts elsewhere: ' + st.after.free.state);
  const preview = {
    state: 'preview', id: 7, title: '3 files (6 B) are copied and safe to delete.', groups: [
      {folder: 'MyWorks/M 31', name: 'M 31', count: 2, bytes: 4, size: '4 B', files: [{path: 'MyWorks/M 31/a.fit', size: '2 B'}, {path: 'MyWorks/M 31/b.fit', size: '2 B'}]},
      {folder: 'MyWorks/M 42', name: 'M 42', count: 1, bytes: 2, size: '2 B', files: [{path: 'MyWorks/M 42/c.fit', size: '2 B'}]}
    ]
  };
  renderFree(preview);
  copyAssert(!$('x-preview').hidden && !$('x-go').hidden && $('x-find').hidden && $('x-groups').children.length === 2, 'the preview is not shown');
  copyAssert($('x-go').textContent === 'Delete 3 files…' && !$('x-go').disabled, 'Delete label ' + $('x-go').textContent);
  freeShown.boxes[0].checked = false;
  freeCount();
  copyAssert($('x-go').textContent === 'Delete 1 file…', 'unchecking did not recount');
  let modal = false;
  $('x-confirm').showModal = () => {modal = true; $('x-confirm').open = true;};
  $('x-confirm').close = () => {$('x-confirm').open = false;};
  freeAsk();
  copyAssert(modal && $('x-ctitle').textContent === 'Delete 1 file from the telescope?' && $('x-cbody').textContent.startsWith('This frees 2 B.'), 'confirm wording');
  let sent = null;
  api = async (action, body) => {
    sent = [action, body];
    return {state: 'deleting', title: 'Deleting…', progress: {done: 0, total: 1}};
  };
  await confirmDelete();
  copyAssert(sent[0] === 'delete' && sent[1].action === 'delete' && sent[1].id === 7 && sent[1].folders.join() === 'MyWorks/M 42', 'deleted ' + JSON.stringify(sent));
  copyAssert(!$('x-confirm').open && $('x-preview').hidden && !$('x-stop').hidden, 'the delete did not start');
  freeShown.boxes.forEach(box => {box.checked = false;});
  renderFree(preview);
  freeCount();
  copyAssert($('x-go').disabled && $('x-go').textContent === 'Delete 0 files…', 'nothing checked still deletes');
}

// Choose… shows only with the window's folder picker. A pick fills the
// folder, Cancel leaves it, and a failure says to type it; each focuses it.
async function checkCopyChoose(st) {
  copyFixture(st, st.unlocked);
  copyAssert(st.before.choose === false, 'the browser status offers a folder picker');
  copyRender(st.before);
  copyAssert($('f-choose').hidden, 'Choose… shows in a browser');
  copyRender({...st.before, choose: true});
  copyAssert(!$('f-choose').hidden, 'Choose… is hidden in a window');
  const calls = [];
  let answer = {folder: '/picked'};
  api = async (action, body, base) => {
    calls.push([action, JSON.stringify(body), base]);
    if (answer instanceof Error) throw {error: answer.message};
    return answer;
  };
  $('f-folder').value = '/typed';
  showErrors({interval: 'Enter whole minutes.'});
  await copyChoose();
  copyAssert(calls.length === 1 && calls[0][0] === 'folder' && calls[0][1] === '{"folder":"/typed"}' && calls[0][2] === '/copy/api/', 'Choose… posted ' + JSON.stringify(calls));
  copyAssert($('f-folder').value === '/picked' && document.activeElement === $('f-folder'), 'the pick was not filled in');
  copyAssert(!$('f-int-err').hidden && $('f-int-err').textContent === 'Enter whole minutes.', 'the pick cleared another field\'s error');
  answer = {folder: ''};
  await copyChoose();
  copyAssert($('f-folder').value === '/picked' && $('f-folder-err').hidden, 'Cancel changed the folder');
  answer = new Error("Couldn't open the folder picker: synthetic. Type the folder's full path instead.");
  await copyChoose();
  copyAssert(!$('f-folder-err').hidden && $('f-folder-err').textContent === answer.message && $('f-folder').value === '/picked', 'the picker failure is not shown');
  copyAssert(document.activeElement === $('f-folder'), 'the failure did not focus the folder');
  copyAssert(!$('f-int-err').hidden, 'the failure cleared another field\'s error');
}

// A newly opened Browse says where to set the folder, and its link goes
// there; once saved, Browse shows the telescope's files.
async function checkBrowseSetup(st) {
  copyFixture(st, st.unlocked);
  copyRender(st.before);
  selectTab('browse');
  copyAssert(!$('panel-browse').hidden && !$('b-setup').hidden && $('b-frame').hidden, 'Browse is blank before setup');
  copyAssert(!$('b-frame').src, 'Browse loaded before setup');
  $('b-setup-go').dispatchEvent({type: 'click'});
  copyAssert(currentTab === 'copy' && !$('panel-copy').hidden && document.activeElement === $('f-folder'), 'Go to Copy did not open the folder field');
  copyRender(st.after);
  selectTab('browse');
  copyAssert($('b-setup').hidden && !$('b-frame').hidden && $('b-frame').src === '/browse/', 'Browse did not show after setup');
}

// The connected page's copying step leads with Copy and Browse, its Go to
// Copy opens the tab, and other SMB apps come second.
async function checkCopySteps(st) {
  copyFixture(st, st.unlocked);
  const connected = {...st.unlocked, active: true, effective: {...st.unlocked.effective, ports: {...st.unlocked.effective.ports, 445: 1234}}};
  render(connected);
  const steps = $('copy-steps').textContent, lead = steps.indexOf("Use the Copy tab to save your images to a folder on this computer, or Browse to view the telescope's files. Go to Copy"), other = steps.indexOf('Another file-sharing (SMB) app can also connect to this computer on port');
  copyAssert(!$('copy-steps').hidden && !$('copy-steps-go').hidden && $('copy-steps-go').parentElement === $('copy-steps'), 'the copying step lacks Go to Copy');
  copyAssert(lead > 0 && other > lead && String($('copy-port').textContent) === '1234', 'the copying step does not lead with Copy: ' + steps);
  copyRender(st.before);
  $('copy-steps-go').dispatchEvent({type: 'click'});
  copyAssert(currentTab === 'copy' && !$('panel-copy').hidden && document.activeElement === $('f-folder'), 'Go to Copy did not open the folder field');
  // Saved, with the form closed, it opens at the tab.
  selectTab('connection');
  copyFormOpen = false;
  copyRender(st.after);
  $('copy-steps-go').dispatchEvent({type: 'click'});
  copyAssert(currentTab === 'copy' && document.activeElement === $('tab-copy'), 'Go to Copy after setup focused ' + JSON.stringify(document.activeElement?.attributes));
  render({...connected, copy_unlocked: false});
  copyAssert($('copy-steps-go').hidden, 'Go to Copy shows while Copy is locked');
}

// Free up space before Copy's folder is saved says where to set it, as
// Browse does.
async function checkFreeSetup(st) {
  copyFixture(st, st.unlocked);
  copyRender(st.before);
  selectTab('free');
  copyAssert(!$('x-setup').hidden && $('x-main').hidden, 'Free up space offers its form before setup');
  copyAssert($('x-setup').textContent.startsWith('Choose the folder for your images on the Copy tab before freeing up space.'), 'setup wording: ' + $('x-setup').textContent);
  $('x-setup-go').dispatchEvent({type: 'click'});
  copyAssert(currentTab === 'copy' && document.activeElement === $('f-folder'), 'Go to Copy did not open the folder field');
  copyFormOpen = false;
  copyRender(st.after);
  selectTab('free');
  copyAssert($('x-setup').hidden && !$('x-main').hidden && !$('x-options').hidden, 'Free up space hides its form after setup');
}

// Free up space names the saved folder whose copies, or copy history, its
// check uses, and links to Copy.
async function checkFreeFolder(st) {
  copyFixture(st, st.unlocked);
  copyFormOpen = false;
  copyRender(st.after);
  selectTab('free');
  const folder = st.after.settings.folder;
  copyAssert($('x-folder-text').textContent === 'Copies in: ' + folder, 'proof folder: ' + $('x-folder-text').textContent);
  $('x-trust').checked = true;
  $('x-trust').dispatchEvent({type: 'change'});
  copyAssert($('x-folder-text').textContent === 'Copy history for: ' + folder, 'record folder: ' + $('x-folder-text').textContent);
  $('x-trust').checked = false;
  $('x-trust').dispatchEvent({type: 'change'});
  $('x-folder-go').dispatchEvent({type: 'click'});
  copyAssert(currentTab === 'copy' && document.activeElement === $('tab-copy'), 'the folder link did not open Copy');
}

// With the check for new files off, the interval is disabled, keeps its
// last valid value, and doesn't block Save.
async function checkCopyOnceInterval(st) {
  copyFixture(st, st.unlocked);
  copyRender(st.before);
  copyAssert($('f-repeat').checked && !$('f-int').disabled && $('f-int').value === '5', 'repeat starts on with its interval');
  $('f-int').value = '12';
  $('f-int').dispatchEvent({type: 'input'});
  $('f-int').value = '';
  $('f-int').dispatchEvent({type: 'input'});
  showErrors({interval: 'Enter a whole number of minutes from 1 to 1440.'});
  $('f-repeat').checked = false;
  $('f-repeat').dispatchEvent({type: 'change'});
  copyAssert($('f-int').disabled && $('f-int').value === '12', 'copy once left the interval at ' + JSON.stringify($('f-int').value) + (($('f-int').disabled) ? '' : ', enabled'));
  copyAssert($('f-int-err').hidden && $('f-int').getAttribute('aria-invalid') === null, 'copy once kept the interval error');
  let sent = null;
  api = async (action, body) => {
    if (action === 'settings') {
      sent = body;
      return {...st.saved, settings: {...st.saved.settings, once: true, interval_minutes: 12}};
    }
    return st.after;
  };
  $('f-folder').value = st.saved.settings.folder;
  await copySave();
  copyAssert(sent && sent.once === true && sent.interval === '12', 'Save sent ' + JSON.stringify(sent));
  $('f-repeat').checked = true;
  $('f-repeat').dispatchEvent({type: 'change'});
  copyAssert(!$('f-int').disabled && $('f-int').value === '12', 'repeating again lost the interval');
  // A whole number in another spelling is kept in the digits the server
  // reads.
  for (const [spelling, want] of [['12.0', '12'], ['1e1', '10'], ['+12', '12']]) {
    $('f-repeat').checked = true;
    $('f-repeat').dispatchEvent({type: 'change'});
    $('f-int').value = spelling;
    $('f-int').dispatchEvent({type: 'input'});
    $('f-repeat').checked = false;
    $('f-repeat').dispatchEvent({type: 'change'});
    copyAssert($('f-int').value === want, spelling + ' was kept as ' + JSON.stringify($('f-int').value));
    sent = null;
    await copySave();
    copyAssert(sent && sent.interval === want, spelling + ' was sent as ' + JSON.stringify(sent && sent.interval));
  }
}

// Copy's and Free up space's action errors are alerts, written only when
// they change, so a poll doesn't announce one again.
async function checkActionAlerts(st) {
  copyFixture(st, st.unlocked);
  copyRender(st.after);
  for (const [id, act] of [['c-err', () => copyAction('start')], ['x-err', () => freeAction({action: 'find'})]]) {
    copyAssert($(id).getAttribute('role') === 'alert', id + ' is not an alert');
    let writes = 0, shown = '';
    Object.defineProperty($(id), 'textContent', {get: () => shown, set: value => {writes++; shown = value;}, configurable: true});
    api = async () => {throw {error: "Couldn't preview: synthetic."};};
    await act();
    await act();
    copyAssert(shown === "Couldn't preview: synthetic." && writes === 1, id + ' wrote ' + writes + ' times: ' + shown);
    api = async () => st.after[id === 'c-err' ? 'copy' : 'free'];
    await act();
    copyAssert(shown === '' && writes === 2, id + ' was not cleared');
  }
}
