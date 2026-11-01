// A failed save retains A's snapshot even after the page starts editing B.
async function checkRetryAccountDraft(before, after, account, manual) {
  initialized = connected = true;
  load(before.settings); render(before);
  if ($('retry-storage').hidden) throw new Error('failed save did not offer retry');
  const different = account === 'different';
  const email = different ? 'second@example.invalid' : before.settings.email;
  $('email').value = ' ' + email.replace('@', ' @') + ' ';
  $('email').dispatchEvent({type:'input'});
  if (manual) {
    $('device-choice').value = 'custom'; $('device-choice').dispatchEvent({type:'change'});
    $('serial').value = 'synthetic-manual-9012'; $('serial').dispatchEvent({type:'input'});
    $('model').value = 'Manual model'; $('model').dispatchEvent({type:'input'});
  }
  $('password').value = 'synthetic-password'; $('password').dispatchEvent({type:'input'});
  $('mode').value = 'idle'; $('idle').value = 7;
  const expected = {...settings(), ...(different ? {} : {serial:after.settings.serial, model:after.settings.model})};
  // Status normally normalizes this already; legacy saved settings can differ.
  const response = account === 'normalized owner' ? {...after, settings:{...after.settings, email:' first @example.invalid '}} : after;
  api = async name => name === 'status' ? response : {ok:true};
  await act('retry-storage');
  if (!$('retry-storage').hidden) throw new Error('successful retry still offered');
  // A legacy spaced owner does not project a saved password association.
  if (account === 'normalized owner') {
    $('password').value = 'synthetic-password'; $('password').dispatchEvent({type:'input'});
  }
  expected.remember_password = usingSaved();
  let submitted;
  api = async (name, body) => {if (name === 'start') submitted = body; return name === 'status' ? response : {ok:true};};
  await act('start');
  const want = {settings:expected, password:different || account === 'normalized owner' ? 'synthetic-password' : ''};
  if (JSON.stringify(submitted) !== JSON.stringify(want)) throw new Error('exact Connect payload after retry: got '+JSON.stringify(submitted)+' want '+JSON.stringify(want));
}

// Change shares the response-to-form path. Its old account's cleared response
// must preserve a newer account's manual draft as well as empty discovery.
async function checkChangeAccountDraft(manual) {
  const original = {email:'first@example.invalid',serial:'synthetic-first-1234',model:'First model',bind:'127.0.0.1'};
  const stopped = {active:false,status:'Stopped',settings:{...original,serial:'',model:''},effective:{listen:'this_computer',idle:{mode:'keep_connected'},ports:{}},password:{memory:true},events:[]};
  initialized = connected = true;
  load(original); render(stopped);
  $('email').value = 'second@example.invalid'; $('email').dispatchEvent({type:'input'});
  if (manual) {
    $('device-choice').value = 'custom'; $('device-choice').dispatchEvent({type:'change'});
    $('serial').value = 'synthetic-manual-9012'; $('serial').dispatchEvent({type:'input'});
    $('model').value = 'Manual model'; $('model').dispatchEvent({type:'input'});
  }
  $('password').value = 'synthetic-password'; $('password').dispatchEvent({type:'input'});
  const want = {settings:settings(), password:'synthetic-password'};
  api = async name => name === 'status' ? stopped : {ok:true};
  await act('change-telescope');
  let submitted;
  api = async (name, body) => {if (name === 'start') submitted = body; return name === 'status' ? stopped : {ok:true};};
  await act('start');
  if (JSON.stringify(submitted) !== JSON.stringify(want)) throw new Error('exact Connect payload after Change: got '+JSON.stringify(submitted)+' want '+JSON.stringify(want));
}
