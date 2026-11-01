// Execute after device-picker-dom.js and app.js in a JavaScript engine, no window.
var changeTestDone = false;
var changeTestError = '';
(async () => {
  const assert = (ok, message) => {if (!ok) throw new Error(message);};
  const original = {email:'synthetic@example.invalid',serial:'synthetic-change-1234',model:'Future telescope'};
  const stopped = {active:false,status:'Stopped. Seestar may still be releasing the session.',settings:{...original,serial:'',model:''},version:'synthetic',effective:{listen:'this_computer',idle:{mode:'keep_connected'},ports:{}},password:{memory:true},events:[]};
  initialized = connected = true;
  $('start').hidden = false;
  load({...original,bind:'127.0.0.1'});
  const selected = {...stopped,active:true,selected_device:'Future telescope ***1234'};
  render(selected);
  assert(!$('change-telescope').hidden && !$('change-telescope').disabled, 'selected change control unavailable');
  for (const action of ['stop','password/save','retry-storage','change-telescope']) {
    pending = action; controls();
    assert($('change-telescope').disabled, 'change enabled during '+action);
  }
  pending = ''; render({...selected,stopping:true});
  assert($('change-telescope').disabled, 'change enabled while stopping');
  render({...stopped,selected_device:''});
  assert($('change-telescope').hidden, 'change visible without telescope');
  render(selected);
  let finish, sent;
  api = async (name, body) => {
    if (name === 'status') return stopped;
    assert(name === 'change-telescope', 'wrong change action');
    sent = body;
    await new Promise(resolve => {finish = resolve;});
    return {ok:true};
  };
  const changing = act('change-telescope');
  assert(pending === 'change-telescope' && $('change-telescope').disabled, 'change not blocked during request');
  assert($('serial').value === original.serial && $('model').value === original.model, 'fields cleared before response');
  assert(sent.password === '', 'change sent password');
  finish(); await changing;
  assert(!$('serial').value && !$('model').value, 'successful change left fields');
  assert(!active && $('change-telescope').hidden && $('selected-device').hidden, 'successful change left run or selection');
  assert(document.activeElement === $('start'), 'change did not focus Connect');
  load({...original,bind:'127.0.0.1'}); render(selected);
  api = async () => {throw {code:'storage',message:'Settings could not be saved; they may reset next time.'};};
  await act('change-telescope');
  assert($('serial').value === original.serial && $('model').value === original.model, 'failed save cleared fields');
  assert($('notice').textContent === 'Settings could not be saved; they may reset next time.' && !$('notice').hidden, 'failed save not reported');
  api = async name => name === 'status' ? stopped : {ok:true};
  await act('retry-storage');
  assert(!$('serial').value && !$('model').value && $('notice').hidden, 'save retry left fields or error');
})().then(() => {changeTestDone = true;}, e => {changeTestError = e.message; changeTestDone = true;});
