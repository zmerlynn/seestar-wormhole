// The shell's tab bar, dots, + menu, closing, Ctrl+Tab and focus, on the DOM
// double, against a double of the app's /api/tabs.
for (const id of testElements.keys()) if (!$(id).parentElement) testBody.append($(id));
const settle = async () => {for (let i = 0; i < 20; i++) await Promise.resolve();};
const assert = (ok, message) => {if (!ok) throw new Error(message);};
// app is the app double: its tabs, its closed profiles, and the requests
// the shell made; refuse answers an action with a refusal once.
const app = {tabs: [], closed: [], profiles: true, pick: {seq: 0, path: ''}, requests: [], refuse: {}, next: 2};
const tabEntry = (n, label, rest = {}) => ({path: '/p/' + n + '/', name: label === 'Default' ? 'default' : label, label, copying: false, active: false, ...rest});
testFetch = async (url, options) => {
  assert(options.headers['X-Seestar-Token'] === 'synthetic-token', 'no token on ' + url);
  const action = url.replace(/^api\//, ''), body = options.body ? JSON.parse(options.body) : undefined;
  if (body !== undefined) app.requests.push([action, body]);
  const reply = (status, v) => ({ok: status === 200, status, json: async () => v});
  if (app.refuse[action]) {
    const refusal = app.refuse[action];
    delete app.refuse[action];
    return reply(400, refusal);
  }
  switch (action) {
  case 'tabs': return reply(200, {tabs: app.tabs, closed: app.closed, profiles: app.profiles, pick: app.pick});
  case 'tabs/open': case 'tabs/new': {
    const label = body.name, t = tabEntry(app.next++, label);
    app.tabs.push(t);
    app.closed = app.closed.filter(p => p.name !== label);
    return reply(200, {path: t.path});
  }
  case 'tabs/close':
    app.tabs = app.tabs.filter(t => t.path !== body.path);
    return reply(200, {tabs: app.tabs, closed: app.closed, profiles: app.profiles, pick: app.pick});
  case 'quit':
    app.tabs = [];
    return reply(200, {ok: true});
  }
  return reply(404, {});
};
const tabsShown = () => $('profile-tabs').querySelectorAll('[role=tab]');
const labels = () => tabsShown().map(t => t.querySelector('.tab-label').textContent);
const selectedLabel = () => tabsShown().filter(t => t.getAttribute('aria-selected') === 'true').map(t => t.querySelector('.tab-label').textContent).join();
const shownFrames = () => $('frames').children.filter(f => f.classList.contains('shown')).map(f => f.dataset.path);
const press = (target, key, more = {}) => {let prevented = false; target.dispatchEvent({type: 'keydown', key, ctrlKey: false, shiftKey: false, altKey: false, metaKey: false, ...more, preventDefault() {prevented = true;}}); return prevented;};

async function checkTabBar() {
  app.tabs = [tabEntry(1, 'Default')];
  app.closed = [{name: 'Backyard', label: 'Backyard'}];
  await refresh();
  // Default alone: one selected tab, its frame shown and loaded with the
  // token, and the + offered.
  assert(JSON.stringify(labels()) === '["Default"]' && selectedLabel() === 'Default', 'tabs: ' + labels());
  assert(!$('add').hidden, '+ hidden');
  const frame = $('frames').children[0];
  assert(frame.src === '/p/1/#synthetic-token' && frame.getAttribute('role') === 'tabpanel' && shownFrames().join() === '/p/1/', 'frame ' + frame.src);
  const tab = tabsShown()[0];
  assert(tab.getAttribute('aria-controls') === frame.id && frame.getAttribute('aria-labelledby') === tab.id && tab.tabIndex === 0, 'tab and panel not linked');
  assert($('profile-tabs').getAttribute('role') === 'tablist', 'no tablist');
  const close = tab.parentElement.lastChild;
  assert(close.getAttribute('aria-label') === 'Close Default' && close.tabIndex === -1, 'close × ' + close.getAttribute('aria-label'));

  // A second tab opens in order and keeps the first's frame as it was.
  app.tabs.push(tabEntry(2, 'Garden'));
  await refresh();
  assert(JSON.stringify(labels()) === '["Default","Garden"]' && selectedLabel() === 'Default', 'two tabs: ' + labels() + ' / ' + selectedLabel());
  assert($('frames').children[0] === frame && $('frames').children.length === 2, 'the first frame was replaced');
  tabsShown()[1].dispatchEvent({type: 'click'});
  assert(selectedLabel() === 'Garden' && shownFrames().join() === '/p/2/' && $('frames').children[0] === frame, 'Garden not shown');
  await refresh();
  assert(selectedLabel() === 'Garden', 'a poll lost the selection');

  // An activation's pick selects its tab, once.
  app.pick = {seq: 1, path: '/p/1/'};
  await refresh();
  assert(selectedLabel() === 'Default', 'the pick was not selected');
  tabsShown()[1].dispatchEvent({type: 'click'});
  await refresh();
  assert(selectedLabel() === 'Garden', 'an old pick was selected again');

  // --state-dir: no +.
  app.profiles = false;
  await refresh();
  assert($('add').hidden, '+ shown without profiles');
  app.profiles = true;
  await refresh();
}

async function checkDots() {
  app.tabs = [tabEntry(1, 'Default'), tabEntry(2, 'Garden')];
  await refresh();
  const dot = n => tabsShown()[n].querySelector('.tab-dot');
  assert(dot(0).dataset.tone === 'off' && tabsShown()[0].title === 'Loading…', 'a loading page has a dot');
  testPages['/p/1/'] = {tone: 'on', words: 'Connected to Seestar S50 TEST0002 through Telescope Network', chip: {hidden: true, className: 'chip off', textContent: ''}};
  testPages['/p/2/'] = {tone: 'off', words: 'Backyard on this network, reachable', chip: {hidden: false, className: 'chip on', textContent: 'Copying'}};
  dots();
  assert(dot(0).dataset.tone === 'on' && tabsShown()[0].title === 'Connected to Seestar S50 TEST0002 through Telescope Network', 'connected: ' + dot(0).dataset.tone);
  assert(dot(1).dataset.tone === 'copy' && tabsShown()[1].title === 'Backyard on this network, reachable · Copying', 'copying: ' + dot(1).dataset.tone);
  assert(tabsShown()[1].querySelector('.hide').textContent === ', Backyard on this network, reachable · Copying', 'the state is not spoken');
  testPages['/p/2/'].chip.className = 'chip warn';
  testPages['/p/1/'].tone = 'busy';
  dots();
  assert(dot(1).dataset.tone === 'error' && dot(0).dataset.tone === 'busy', 'warn and busy: ' + dot(1).dataset.tone + ' ' + dot(0).dataset.tone);
  testPages['/p/2/'].chip.hidden = true;
  testPages['/p/2/'].tone = 'error';
  dots();
  assert(dot(1).dataset.tone === 'error', 'a failed connection is not an error');
}

async function checkMenu() {
  app.tabs = [tabEntry(1, 'Default')];
  app.closed = [{name: 'Backyard', label: 'Backyard'}];
  app.next = 2;
  app.requests = [];
  await refresh();
  // The closed profiles, then New profile…, the first focused.
  $('add').focus();
  $('add').dispatchEvent({type: 'click'});
  const rows = () => $('a-list').children.map(row => row.children[0].textContent);
  assert($('a-dialog').open && JSON.stringify(rows()) === '["Backyard","New profile…"]', 'menu: ' + JSON.stringify(rows()));
  assert(document.activeElement === $('a-list').children[0].children[0], 'the first row is not focused');
  // A refusal shows under the list.
  app.refuse['tabs/open'] = {code: 'profile_open', message: "Couldn't open Backyard: Backyard is open in another seestar-wormhole window: its lock, /synthetic/profiles/Backyard/ui.lock, is held."};
  $('a-list').children[0].children[0].dispatchEvent({type: 'click'});
  await settle();
  assert($('a-dialog').open && !$('a-err').hidden && $('a-err').textContent.startsWith("Couldn't open Backyard: Backyard is open in another"), 'refusal: ' + $('a-err').textContent);
  // Opened, it is a new tab, selected.
  $('a-list').children[0].children[0].dispatchEvent({type: 'click'});
  await settle();
  assert(!$('a-dialog').open && JSON.stringify(app.requests.at(-1)) === '["tabs/open",{"name":"Backyard"}]', 'not opened: ' + JSON.stringify(app.requests.at(-1)));
  assert(JSON.stringify(labels()) === '["Default","Backyard"]' && selectedLabel() === 'Backyard' && document.activeElement === tabsShown()[1], 'Backyard not selected: ' + selectedLabel());

  // New profile…: the name, a refusal under it, typing clears it, and a
  // created profile's tab is selected.
  $('add').dispatchEvent({type: 'click'});
  assert(JSON.stringify(rows()) === '["New profile…"]' && $('a-err').hidden, 'reopened: ' + JSON.stringify(rows()));
  $('a-list').children[0].children[0].dispatchEvent({type: 'click'});
  assert($('a-main').hidden && !$('a-form').hidden && document.activeElement === $('a-name') && $('a-name').value === '', 'the name is not asked');
  app.refuse['tabs/new'] = {code: 'profile_name', message: "There's already a profile named Backyard."};
  $('a-name').value = 'backyard';
  $('a-form').dispatchEvent({type: 'submit', preventDefault() {}});
  await settle();
  assert(JSON.stringify(app.requests.at(-1)) === '["tabs/new",{"name":"backyard"}]', 'the name was not sent as typed');
  assert($('a-dialog').open && !$('a-name-err').hidden && $('a-name-err').textContent === "There's already a profile named Backyard." && $('a-name').getAttribute('aria-invalid') === 'true', 'refusal not under the name');
  $('a-name').dispatchEvent({type: 'input'});
  assert($('a-name-err').hidden && $('a-name').getAttribute('aria-invalid') === 'false', 'typing kept the refusal');
  $('a-name').value = 'Garden';
  $('a-form').dispatchEvent({type: 'submit', preventDefault() {}});
  await settle();
  assert(!$('a-dialog').open && selectedLabel() === 'Garden', 'Garden not created and selected');
  // Reopened, it starts from the list; Cancel closes it.
  $('add').dispatchEvent({type: 'click'});
  assert(!$('a-main').hidden && $('a-form').hidden, 'reopened on the form');
  $('a-cancel').dispatchEvent({type: 'click'});
  assert(!$('a-dialog').open, 'Cancel left the menu open');
}

async function checkClose() {
  app.tabs = [tabEntry(1, 'Default', {active: true}), tabEntry(2, 'Garden', {copying: true, active: true}), tabEntry(3, 'Backyard')];
  app.requests = [];
  await refresh();
  tabsShown()[1].dispatchEvent({type: 'click'});
  // A tab that isn't connected closes without asking.
  tabsShown()[2].parentElement.lastChild.dispatchEvent({type: 'click'});
  await settle();
  assert(!$('k-confirm').open && JSON.stringify(app.requests.at(-1)) === '["tabs/close",{"path":"/p/3/"}]' && JSON.stringify(labels()) === '["Default","Garden"]', 'Backyard: ' + labels());
  // Copying, it asks first; Cancel keeps it.
  tabsShown()[1].parentElement.lastChild.dispatchEvent({type: 'click'});
  await settle();
  assert($('k-confirm').open && $('k-ctitle').textContent === 'Close Garden?' && $('k-cbody').textContent === 'Garden is copying. Closing its tab stops copying and its connection.' && $('k-cgo').textContent === 'Close tab', 'ask: ' + $('k-cbody').textContent);
  $('k-ccancel').dispatchEvent({type: 'click'});
  await settle();
  assert(!$('k-confirm').open && labels().length === 2 && app.requests.length === 1, 'Cancel closed Garden');
  // Delete on the selected tab asks the same; confirmed, it closes and
  // the tab next to it is selected.
  press(tabsShown()[1], 'Delete');
  await settle();
  assert($('k-confirm').open, 'Delete did not ask');
  $('k-cgo').dispatchEvent({type: 'click'});
  await settle();
  assert(JSON.stringify(app.requests.at(-1)) === '["tabs/close",{"path":"/p/2/"}]' && JSON.stringify(labels()) === '["Default"]' && selectedLabel() === 'Default', 'Garden not closed: ' + labels());
  // The last tab, connected: closing it quits.
  tabsShown()[0].parentElement.lastChild.dispatchEvent({type: 'click'});
  await settle();
  assert($('k-cbody').textContent === 'Default is connected. Closing its tab stops its connection, and quits the app, since it is the last tab.' && $('k-cgo').textContent === 'Close and quit', 'last: ' + $('k-cbody').textContent);
  $('k-cgo').dispatchEvent({type: 'click'});
  await settle();
  assert(!$('ended').hidden && $('tabbar').hidden && $('frames').children.length === 0, 'the last close did not end the shell');
}

async function checkQuit() {
  app.tabs = [tabEntry(1, 'Default'), tabEntry(2, 'Garden', {copying: true})];
  app.requests = [];
  await refresh();
  await window.confirmQuit();
  assert($('k-confirm').open && $('k-ctitle').textContent === 'Quit seestar-wormhole?' && $('k-cbody').textContent === 'Garden is copying. Quitting stops copying and every connection.' && $('k-cgo').textContent === 'Quit', 'quit ask: ' + $('k-cbody').textContent);
  $('k-ccancel').dispatchEvent({type: 'click'});
  assert(app.requests.length === 0, 'Cancel quit');
  app.tabs[1].copying = false;
  await window.confirmQuit();
  await settle();
  assert(!$('k-confirm').open && JSON.stringify(app.requests) === '[["quit",{}]]' && !$('ended').hidden, 'not copying, quit asked: ' + JSON.stringify(app.requests));
}

async function checkSwitchKeys() {
  app.tabs = [tabEntry(1, 'Default'), tabEntry(2, 'Garden'), tabEntry(3, 'Backyard')];
  await refresh();
  assert(selectedLabel() === 'Default', 'start ' + selectedLabel());
  assert(press(testBody, 'Tab', {ctrlKey: true}) && selectedLabel() === 'Garden', 'Ctrl+Tab: ' + selectedLabel());
  press(testBody, 'Tab', {ctrlKey: true});
  press(testBody, 'Tab', {ctrlKey: true});
  assert(selectedLabel() === 'Default' && document.activeElement === $('frames').children[0], 'Ctrl+Tab did not wrap: ' + selectedLabel());
  press(testBody, 'Tab', {ctrlKey: true, shiftKey: true});
  assert(selectedLabel() === 'Backyard', 'Ctrl+Shift+Tab: ' + selectedLabel());
  assert(!press(testBody, 'Tab') && selectedLabel() === 'Backyard', 'Tab alone switched');
  // From inside a page, once it has loaded.
  const frame = $('frames').children[0];
  frame.dispatchEvent({type: 'load'});
  press(testWindows['/p/1/'], 'Tab', {ctrlKey: true});
  assert(selectedLabel() === 'Default', 'Ctrl+Tab in a page: ' + selectedLabel());
  // Arrows, Home and End within the tab list.
  press(tabsShown()[0], 'ArrowRight');
  assert(selectedLabel() === 'Garden', 'ArrowRight: ' + selectedLabel());
  press(tabsShown()[1], 'End');
  assert(selectedLabel() === 'Backyard' && tabsShown()[2].tabIndex === 0 && tabsShown()[0].tabIndex === -1, 'End: ' + selectedLabel());
}

// The selected tab's page takes focus as it loads and as tabs switch,
// unless something in the shell has focus.
async function checkFrameFocus() {
  app.tabs = [tabEntry(1, 'Default'), tabEntry(2, 'Garden')];
  await refresh();
  const [one, two] = $('frames').children;
  assert(document.activeElement === one, 'the first page was not focused: ' + document.activeElement.tagName);
  // A page that loads takes focus again, since its frame's went to the
  // blank page it replaced; a hidden page's load does not.
  testFocused.length = 0;
  one.dispatchEvent({type: 'load'});
  two.dispatchEvent({type: 'load'});
  assert(JSON.stringify(testFocused) === '["/p/1/"]' && document.activeElement === one, 'loads focused ' + JSON.stringify(testFocused));
  // A poll leaves a focused page alone.
  testFocused.length = 0;
  await refresh();
  assert(testFocused.length === 0, 'a poll refocused the page');
  // Ctrl+Tab from a page lands in the next page.
  press(testWindows['/p/1/'], 'Tab', {ctrlKey: true});
  assert(selectedLabel() === 'Garden' && document.activeElement === two, 'Ctrl+Tab from a page: ' + document.activeElement.tagName);
  // Clicking a tab that took focus, or arrowing in the tab list, keeps
  // focus in the shell; so does a poll meanwhile.
  tabsShown()[0].focus();
  tabsShown()[0].dispatchEvent({type: 'click'});
  assert(selectedLabel() === 'Default' && document.activeElement === tabsShown()[0], 'click took focus from the tab');
  press(tabsShown()[0], 'ArrowRight');
  await refresh();
  assert(selectedLabel() === 'Garden' && document.activeElement === tabsShown()[1], 'arrow left the tab list');
  // Ctrl+Tab from the shell stays in the tab list.
  press(testBody, 'Tab', {ctrlKey: true});
  assert(selectedLabel() === 'Default' && document.activeElement === tabsShown()[0], 'Ctrl+Tab from the shell');
  // With nothing focused, a click on a tab that didn't take focus hands
  // it to the page.
  document.activeElement = testBody;
  tabsShown()[1].dispatchEvent({type: 'click'});
  assert(document.activeElement === two, 'nothing focused: ' + document.activeElement.tagName);
  // The window gaining focus hands it to the page, which may not have had
  // it when it loaded; a tab focused by the click that brought it does not.
  const later = [];
  setTimeout = callback => later.push(callback);
  try {
    testFocused.length = 0;
    testBody.dispatchEvent({type: 'window-focus'});
    later.splice(0).forEach(f => f());
    assert(JSON.stringify(testFocused) === '["/p/2/"]' && document.activeElement === two, 'window focus focused ' + JSON.stringify(testFocused));
    tabsShown()[1].focus();
    testFocused.length = 0;
    testBody.dispatchEvent({type: 'window-focus'});
    later.splice(0).forEach(f => f());
    assert(testFocused.length === 0 && document.activeElement === tabsShown()[1], 'window focus took focus from the tab');
  } finally {setTimeout = () => {};}
  await settle();
}
