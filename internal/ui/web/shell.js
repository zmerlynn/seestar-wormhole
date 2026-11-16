'use strict';
// The shell: a tab per open profile, each its profile's page in a frame
// under the tab's path, and a + that opens another profile or creates
// one. The frames stay loaded while hidden, so a tab keeps its state.
const $ = id => document.getElementById(id);
const tokenKey = 'seestar-control-token';
let token = location.hash.slice(1);
if (token) {
  try {
    sessionStorage.setItem(tokenKey, token);
  } catch (_) {
  }
  history.replaceState(null, '', location.pathname);
} else {
  try {
    token = sessionStorage.getItem(tokenKey) || '';
  } catch (_) {
  }
}
const lostCopy = 'Connection to the app was lost. Reopen the app to reconnect.';
// tabs is the last poll's open tabs; selected, the selected tab's path;
// picked, the last activation's sequence acted on; ended, that the app
// quit.
let tabs = [], selected = '', picked = 0, ended = false, closed = [], offered = false;
// acted: the user has pressed a key, in the shell or a page, or clicked
// something in the shell. Focus in the shell before then isn't the
// user's: WKWebView focuses the shell's first tab itself as its window
// first takes focus, and the page takes it back.
let acted = false;
async function api(action, body) {
  const options = {headers: {'X-Seestar-Token': token}, cache: 'no-store', redirect: 'error'};
  if (body !== undefined) {
    options.method = 'POST';
    options.headers['Content-Type'] = 'application/json';
    options.body = JSON.stringify(body);
  }
  let response;
  try {
    response = await fetch('api/' + action, options);
  } catch (_) {
    throw {code: 'controls', message: lostCopy};
  }
  if (!response.ok) {
    let detail;
    try {
      detail = await response.json();
    } catch (_) {
      throw {code: 'controls', message: lostCopy};
    }
    throw detail;
  }
  return response.json();
}
function text(id, value) {
  if ($(id).textContent !== value) $(id).textContent = value;
}
function note(message) {
  text('shell-note', message);
  $('shell-note').hidden = !message;
}
// A tab's dot is its page's own header state: copying while the header's
// Copy chip is on, needing attention when the chip or the connection's
// dot says so, else the connection's dot. Its title is the header's
// words.
function pageState(frame) {
  let doc;
  try {
    doc = frame.contentDocument;
  } catch (_) {
  }
  const dot = doc?.getElementById('connection-dot'), chip = doc?.getElementById('copy-chip');
  if (!dot) return {tone: 'off', words: 'Loading…'};
  const connection = dot.getAttribute('data-tone') || 'off', words = doc.getElementById('connection-text')?.textContent || '';
  const copy = chip && !chip.hidden ? chip.className : '';
  const tone = copy.includes('warn') || connection === 'error' ? 'error' : copy.includes(' on') ? 'copy' : connection;
  return {tone, words: words + (chip && !chip.hidden ? ' · ' + chip.textContent : '')};
}
function frameOf(path) {
  return [...$('frames').children].find(f => f.dataset.path === path);
}
function tabOf(path) {
  return [...$('profile-tabs').querySelectorAll('[role=tab]')].find(t => t.dataset.path === path);
}
// render keeps a tab, its close × and its frame per open tab, in opening
// order, adding and removing only what changed.
function render() {
  const list = $('profile-tabs');
  for (const row of [...list.children]) if (!tabs.some(t => t.path === row.dataset.path)) row.remove();
  for (const frame of [...$('frames').children]) if (!tabs.some(t => t.path === frame.dataset.path)) frame.remove();
  tabs.forEach((t, i) => {
    let row = [...list.children].find(r => r.dataset.path === t.path);
    if (!row) {
      row = document.createElement('div');
      row.className = 'ptab';
      row.dataset.path = t.path;
      row.setAttribute('role', 'presentation');
      const tab = document.createElement('button'), dot = document.createElement('span'), label = document.createElement('span'), state = document.createElement('span');
      tab.type = 'button';
      tab.id = 'ptab-' + t.path.split('/')[2];
      tab.dataset.path = t.path;
      tab.setAttribute('role', 'tab');
      tab.setAttribute('aria-controls', 'pframe-' + t.path.split('/')[2]);
      tab.setAttribute('aria-keyshortcuts', 'Delete');
      dot.className = 'tab-dot';
      dot.setAttribute('aria-hidden', 'true');
      dot.dataset.tone = 'off';
      label.className = 'tab-label';
      state.className = 'hide';
      tab.append(dot, label, state);
      tab.addEventListener('click', () => {
        acted = true;
        select(t.path);
      });
      tab.addEventListener('keydown', tabKey);
      const close = document.createElement('button');
      close.type = 'button';
      close.className = 'tab-close';
      close.tabIndex = -1;
      close.textContent = '×';
      close.addEventListener('click', () => {
        acted = true;
        askClose(t.path);
      });
      row.append(tab, close);
      const frame = document.createElement('iframe');
      frame.id = 'pframe-' + t.path.split('/')[2];
      frame.dataset.path = t.path;
      frame.setAttribute('role', 'tabpanel');
      frame.setAttribute('aria-labelledby', tab.id);
      frame.src = t.path + '#' + token;
      frame.addEventListener('load', () => {
        keysFrom(frame.contentWindow);
        if (frame.dataset.path === selected) handOff(true);
      });
      $('frames').append(frame);
    }
    if (list.children[i] !== row) list.insertBefore(row, list.children[i] || null);
    row.querySelector('.tab-label').textContent = t.label;
    row.lastChild.setAttribute('aria-label', 'Close ' + t.label);
    row.lastChild.title = 'Close ' + t.label;
  });
  if (!tabs.some(t => t.path === selected) && tabs.length) select(tabs[0].path, false);
  else select(selected, false);
  dots();
}
// dots reads each tab's page's header into its dot.
function dots() {
  for (const t of tabs) {
    const tab = tabOf(t.path), frame = frameOf(t.path);
    if (!tab || !frame) continue;
    const {tone, words} = pageState(frame), dot = tab.querySelector('.tab-dot');
    if (dot.dataset.tone !== tone) dot.dataset.tone = tone;
    if (tab.title !== words) tab.title = words;
    const state = tab.querySelector('.hide'), said = words ? ', ' + words : '';
    if (state.textContent !== said) state.textContent = said;
  }
}
// select shows the tab's frame; the others stay loaded, hidden. With
// focus it focuses the tab; otherwise the page takes focus unless
// something in the shell has it.
function select(path, focus) {
  selected = path;
  for (const tab of $('profile-tabs').querySelectorAll('[role=tab]')) {
    const on = tab.dataset.path === path;
    tab.setAttribute('aria-selected', on ? 'true' : 'false');
    tab.tabIndex = on ? 0 : -1;
  }
  for (const frame of $('frames').children) frame.classList.toggle('shown', frame.dataset.path === path);
  if (focus) tabOf(path)?.focus();
  else handOff(false);
}
// shellFocused tells whether the user's focus is on something in the
// shell rather than nowhere or in a page.
function shellFocused() {
  const a = document.activeElement;
  return acted && !!a && a !== document.body && a.tagName !== 'IFRAME';
}
// handOff gives the selected tab's page focus, so keyboard users land in
// it, unless the shell has focus. A page that just loaded takes it again,
// since the focus its frame had went to the page it replaced, as does the
// page when the window gains focus in the shell: a window not yet focused
// as the page loaded, as WKWebView's can be, gives its first focus to the
// shell.
function handOff(again) {
  const frame = frameOf(selected);
  if (!frame || shellFocused() || (document.activeElement === frame && !again)) return;
  try {
    frame.contentWindow.focus();
  } catch (_) {
  }
}
// step selects the next tab, or the previous one, wrapping.
function step(by) {
  const i = tabs.findIndex(t => t.path === selected), n = tabs.length;
  if (!n) return;
  select(tabs[((i + by) % n + n) % n].path, shellFocused());
}
function tabKey(event) {
  acted = true;
  const i = tabs.findIndex(t => t.path === selected), n = tabs.length;
  if (event.key === 'Delete') {
    event.preventDefault();
    askClose(selected);
    return;
  }
  const j = {ArrowRight: (i + 1) % n, ArrowLeft: (i + n - 1) % n, Home: 0, End: n - 1}[event.key];
  if (j === undefined) return;
  event.preventDefault();
  select(tabs[j].path, true);
}
// Ctrl+Tab and Ctrl+Shift+Tab switch tabs from the shell or any page,
// and from a page's own frames, Browse's, as each loads. Any key there
// is the user acting.
function switchKey(event) {
  acted = true;
  if (event.key !== 'Tab' || !event.ctrlKey || event.altKey || event.metaKey) return;
  event.preventDefault();
  step(event.shiftKey ? -1 : 1);
}
function keysFrom(win) {
  try {
    win.addEventListener('keydown', switchKey);
    for (const frame of win.document.querySelectorAll('iframe')) {
      frame.addEventListener('load', () => keysFrom(frame.contentWindow));
      if (frame.contentDocument?.readyState === 'complete') keysFrom(frame.contentWindow);
    }
  } catch (_) {
  }
}
// poll reads the open tabs every second, and selects the tab an
// activation asked for.
let generation = 0;
async function poll() {
  if (ended) return;
  await refresh();
  if (!ended) setTimeout(poll, 1000);
}
async function refresh() {
  const g = ++generation;
  let v;
  try {
    v = await api('tabs');
  } catch (e) {
    if (!ended && g === generation) note(e.message || lostCopy);
    return null;
  }
  if (ended || g !== generation) return v;
  apply(v);
  return v;
}
// apply shows the app's answer of its tabs.
function apply(v) {
  note(v.error || '');
  tabs = v.tabs || [];
  closed = v.closed || [];
  offered = !!v.profiles;
  $('add').hidden = !offered;
  if (v.pick?.seq > picked) {
    picked = v.pick.seq;
    if (tabs.some(t => t.path === v.pick.path)) selected = v.pick.path;
  }
  render();
}
// The + menu: the profiles that exist but aren't open, each opening its
// tab, then New profile…, which asks for a name.
let menuBack = null;
function openMenu() {
  acted = true;
  menuBack = document.activeElement;
  $('a-main').hidden = false;
  $('a-form').hidden = true;
  fieldError('a-err', '');
  const items = closed.map(p => {
    const row = document.createElement('li'), button = document.createElement('button');
    button.type = 'button';
    button.textContent = p.label;
    button.addEventListener('click', () => openAct('tabs/open', {name: p.name}, 'a-err'));
    row.append(button);
    return row;
  });
  const row = document.createElement('li'), button = document.createElement('button');
  button.type = 'button';
  button.id = 'a-new';
  button.textContent = 'New profile…';
  button.addEventListener('click', newProfile);
  row.append(button);
  $('a-list').replaceChildren(...items, row);
  $('a-dialog').showModal();
  $('a-list').querySelector('button').focus();
}
function fieldError(id, message) {
  text(id, message);
  $(id).hidden = !message;
  if (id === 'a-name-err') $('a-name').setAttribute('aria-invalid', message ? 'true' : 'false');
}
// openAct opens a profile, or creates one and opens it, and selects its
// tab; a refusal shows its message under the name or the list.
async function openAct(action, body, errorId) {
  try {
    const v = await api(action, body);
    $('a-dialog').close();
    await refresh();
    select(v.path, true);
  } catch (e) {
    fieldError(errorId, e.message || lostCopy);
    if (errorId === 'a-name-err') $('a-name').focus();
  }
}
function newProfile() {
  $('a-main').hidden = true;
  $('a-form').hidden = false;
  $('a-name').value = '';
  fieldError('a-name-err', '');
  $('a-name').focus();
}
// The confirmation: closing a tab that is copying or connected, or
// quitting while any tab copies. go is what its button does.
let confirmed = null;
function confirmWith(title, body, button, go) {
  text('k-ctitle', title);
  text('k-cbody', body);
  text('k-cgo', button);
  confirmed = go;
  $('k-confirm').showModal();
}
// askClose closes a tab, confirming first if its profile is copying or
// has a run active. Closing the last tab quits the app.
async function askClose(path) {
  const v = await refresh();
  const t = (v?.tabs || tabs).find(t => t.path === path);
  if (!t) return;
  if (!t.copying && !t.active) {
    closeTab(path);
    return;
  }
  const last = tabs.length === 1, stops = t.copying ? 'copying and its connection' : 'its connection';
  confirmWith('Close ' + t.label + '?', (t.copying ? t.label + ' is copying. ' : t.label + ' is connected. ') + 'Closing its tab stops ' + stops + (last ? ', and quits the app, since it is the last tab.' : '.'), last ? 'Close and quit' : 'Close tab', () => closeTab(path));
}
// closeTab closes a tab and shows the tabs the app answers are left,
// which may include one opened meanwhile: none left means the app quit.
// Closing the selected tab selects its neighbor; focus on the closing
// tab's row goes to the selected tab.
async function closeTab(path) {
  const before = tabs.map(t => t.path), i = before.indexOf(path);
  let v;
  try {
    v = await api('tabs/close', {path});
  } catch (e) {
    note(e.message || lostCopy);
    return;
  }
  if (!v.tabs?.length) {
    end();
    return;
  }
  generation++;
  const left = v.tabs.map(t => t.path);
  if (!left.includes(selected)) selected = [...before.slice(i + 1), ...before.slice(0, i).reverse()].find(p => left.includes(p)) || left[0];
  // Moved before the row goes, the shell's focus doesn't pass to a page.
  if (shellFocused() && tabOf(path)?.parentElement.contains(document.activeElement)) tabOf(selected)?.focus();
  apply(v);
}
// confirmQuit quits every tab, confirming once if any is copying. The
// window's close and each page's Quit come here.
async function confirmQuit() {
  const v = await refresh();
  const copying = (v?.tabs || tabs).filter(t => t.copying).map(t => t.label);
  if (!copying.length) {
    quit();
    return;
  }
  confirmWith('Quit seestar-wormhole?', copying.join(' and ') + (copying.length > 1 ? ' are' : ' is') + ' copying. Quitting stops copying and every connection.', 'Quit', quit);
}
async function quit() {
  try {
    await api('quit', {});
  } catch (_) {
  }
  end();
}
function end() {
  ended = true;
  $('tabbar').hidden = true;
  $('frames').hidden = true;
  $('frames').replaceChildren();
  note('');
  $('ended').hidden = false;
}
// The window's close asks confirmQuit by name.
window.confirmQuit = confirmQuit;
$('add').addEventListener('click', openMenu);
$('a-cancel').addEventListener('click', () => $('a-dialog').close());
$('a-back').addEventListener('click', () => $('a-dialog').close());
$('a-form').addEventListener('submit', e => {
  e.preventDefault();
  openAct('tabs/new', {name: $('a-name').value}, 'a-name-err');
});
$('a-name').addEventListener('input', () => fieldError('a-name-err', ''));
$('a-dialog').addEventListener('close', () => {
  if (menuBack?.isConnected && document.activeElement === document.body) menuBack.focus();
  menuBack = null;
});
$('k-cgo').addEventListener('click', () => {
  const go = confirmed;
  $('k-confirm').close();
  go?.();
});
$('k-ccancel').addEventListener('click', () => $('k-confirm').close());
$('k-confirm').addEventListener('close', () => {
  confirmed = null;
});
document.addEventListener('keydown', switchKey);
// Focus that lands in the shell before the user acts goes to the page.
document.addEventListener('focusin', event => {
  if (!acted && event.target.tagName !== 'IFRAME') setTimeout(() => handOff(true));
});
// Once the focus that brought the window's focus has landed: a click on
// a tab keeps it.
window.addEventListener('focus', () => {
  setTimeout(() => handOff(true));
  refresh();
});
poll();
