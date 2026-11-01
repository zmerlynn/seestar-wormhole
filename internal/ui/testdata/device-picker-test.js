// Run against the complete production page script, without a browser or network.
async function checkDevicePicker() {
  const assert = (ok, message) => {if (!ok) throw new Error(message);};
  initialized = connected = true;
  const original = {email:'synthetic@example.invalid',bind:'127.0.0.1',serial:'',model:''};
  const choices = [
    {serial:'synthetic-choice-1234',model:'Seestar S50 Pro',label:'Seestar S50 Pro synthetic-choice-1234'},
    {serial:' synthetic-choice-5678 ',model:' <img>\t ',label:' <img>\\t   synthetic-choice-5678 '}
  ];
  const idle = {devices_email:original.email,active:false,status:'Not connected',settings:original,version:'synthetic',effective:{listen:'this_computer',idle:{mode:'keep_connected'},ports:{}},password:{memory:true},events:[]};
  load(original); render(idle);
  const picker = $('device-choice');
  assert(!$('device-picker').hidden && picker.children.length === 2, 'always-visible picker entries');
  assert(picker.children[0].value === '' && picker.children[0].textContent === 'Find on my account' && picker.value === '', 'Find on my account default entry');
  assert(picker.children[1].value === 'custom' && picker.children[1].textContent === 'Enter manually…', 'Custom entry');
  assert($('custom-device').hidden, 'Find on my account exposed Custom fields');
  picker.value = 'custom'; picker.onchange();
  assert(!$('custom-device').hidden, 'Custom did not reveal fields');
  $('serial').value = 'manual'; $('model').value = 'manual model';
  picker.value = ''; picker.onchange();
  assert($('custom-device').hidden && $('serial').value === '' && $('model').value === '', 'Find on my account did not hide and clear exact pair');
  const saved = {...original,serial:'synthetic-saved-9999',model:'Future telescope',choice_label:'Future telescope synthetic-saved-9999'};
  load(saved); render({...idle,settings:saved});
  assert(picker.children.length === 3 && picker.children[1].textContent === 'Saved: '+saved.choice_label && picker.value === '0', 'saved entry label and selection');
  assert($('serial').value === saved.serial && $('model').value === saved.model && $('custom-device').hidden, 'saved pair projection');
  load(original);
  pending = 'start';
  const multiple = {...idle,status:'Choose a telescope',failure:{code:'selection_multiple',devices:choices},failure_message:'Your account has several telescopes. Choose one, then Connect.'};
  render(multiple);
  assert(picker.focusCount === 0, 'picker focused before form enabled');
  pending = ''; controls();
  assert(document.activeElement === picker && picker.focusCount === 1 && !$('options').disabled, 'picker entry focus after enable');
  assert(picker.children.length === 4 && picker.children[1].textContent === choices[0].label && picker.children[2].textContent === choices[1].label, 'received entries and escaped labels');
  assert($('status').textContent === 'Choose a telescope' && $('notice').hidden && $('serial-error').hidden && $('model-error').hidden && !fieldErrors.model && !fieldErrors.serial && !$('serial').attributes['aria-invalid'] && $('error-code').hidden && !$('advanced').open && !String($('status').attributes.class || '').split(' ').includes('error') && $('status').attributes.role !== 'alert' && !picker.attributes['aria-invalid'], 'neutral selection presentation');
  assert(!$('selection-help').hidden && picker.attributes['aria-describedby'] === 'selection-help' && $('selection-help').attributes.class === 'help' && $('selection-help').attributes.role !== 'alert' && $('selection-help').textContent === 'Your account has several telescopes. Choose one, then Connect.', 'selection description link');
  assert($('start').parentElement === $('picker-action') && picker.parentElement.children.indexOf($('picker-action')) > picker.parentElement.children.indexOf($('custom-device')) && $('picker-action').parentElement === picker.parentElement && $('serial').parentElement === $('custom-device') && $('custom-device').parentElement === picker.parentElement && $('options').children.indexOf(picker.parentElement) < $('options').children.indexOf($('advanced')), 'Connect order and Custom location');
  $('password').focus(); render(multiple);
  assert(document.activeElement === $('password') && picker.focusCount === 1, 'poll stole focus');
  let requests = 0;
  api = async () => {requests++; throw new Error('unexpected account request');};
  await act('start');
  assert(requests === 0 && document.activeElement === picker, 'Find on my account Connect made an account request');
  picker.value = '1'; picker.onchange();
  assert($('serial').value === choices[1].serial && $('model').value === choices[1].model, 'received exact fill');
  assert(settings().serial === choices[1].serial && settings().model === choices[1].model, 'exact wire submission');
  assert($('status').textContent === 'Ready to connect' && requests === 0 && $('custom-device').hidden, 'chosen status or automatic connection');
  render(multiple);
  assert(picker.value === '1' && $('status').textContent === 'Ready to connect', 'poll reset choice or status');
  render({...idle,active:true}); render(idle);
  assert(picker.children.length === 4, 'received list lost during app run');
  fieldError('serial','Choose “Find on my account” to reload telescopes, or check Serial and Model.','selection',true);
  assert(picker.value === 'custom' && !$('custom-device').hidden && $('serial').attributes['aria-invalid'] === 'true' && document.activeElement === $('serial'), 'real mismatch did not target Custom fields');
  fieldError('model','Choose “Find on my account” to reload telescopes, or check Serial and Model.','model');
  picker.value = ''; picker.onchange();
  assert($('serial-error').hidden && $('model-error').hidden, 'entry choice retained field error');
  assert(!diagnostic.includes('synthetic-choice-') && !diagnostic.includes('<img') && !diagnostic.includes('devices'), 'full choices in diagnostic summary');
  assert($('start').parentElement === $('primary-action'), 'Connect not restored after selection step');
  render({...idle,active:true,selected_device:'Future telescope ***1234'});
  assert(!$('change-telescope-help').hidden && $('change-telescope').attributes['aria-describedby'] === 'change-telescope-help' && $('change-telescope-help').textContent === 'Stops the session.', 'Change telescope description link');
  render(idle);
  assert($('change-telescope-help').hidden, 'orphaned change description');
}


async function checkPickerInitialStep() {
 const assert = (ok, message) => {if (!ok) throw new Error(message);};
 const initial = {devices_email:'synthetic@example.invalid',active:false,status:'Choose a telescope',settings:{email:'synthetic@example.invalid',bind:'127.0.0.1',serial:'',model:''},port_defaults:[],version:'synthetic',effective:{listen:'this_computer',idle:{mode:'keep_connected'},ports:{}},password:{memory:true},events:[],failure:{code:'selection_multiple',devices:[{serial:'synthetic-choice-1234',model:'S30',label:'S30 synthetic-choice-1234'}]}};
 document.hasFocus = () => true;
 globalThis.requestAnimationFrame = callback => callback();
 api = async () => initial;
 await refresh();
 assert(document.activeElement === $('device-choice') && $('device-choice').focusCount === 1, 'reopened selection step focused Connect');
 $('password').focus();
 await refresh();
 assert(document.activeElement === $('password') && $('device-choice').focusCount === 1, 'reopened step poll stole focus');
}
