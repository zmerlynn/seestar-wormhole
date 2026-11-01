function uxFixture() {
 initialized = connected = true;
 const settings = {email:'synthetic@example.invalid',bind:'127.0.0.1',serial:'',model:''};
 const devices = [{serial:'synthetic-one',model:'S30',label:'S30 synthetic-one'}, {serial:'synthetic-two',model:'S50',label:'S50 synthetic-two'}];
 const idle = {active:false,status:'Not connected',settings,devices_email:settings.email,devices,version:'synthetic',effective:{listen:'this_computer',idle:{mode:'keep_connected'},ports:{}},password:{memory:true},events:[]};
 load(settings);
 return {idle, multiple:{...idle,status:'Choose a telescope',failure:{code:'selection_multiple',devices}}};
}
function uxAssert(ok, message) {if (!ok) throw new Error(message);}
async function checkStatusWrites() {
 const {multiple} = uxFixture(); render(multiple);
 const status = $('status'); let value = status.textContent, writes = [];
 Object.defineProperty(status, 'textContent', {get(){return value;},set(next){writes.push(next);value=next;}});
 $('device-choice').value = '0'; $('device-choice').onchange();
 uxAssert(writes.length===1 && writes[0]==='Ready to connect', 'choice must write final status once: '+JSON.stringify(writes));
 writes=[]; render(multiple);
 uxAssert(writes.length===0, 'unchanged chosen poll mutated status: '+JSON.stringify(writes));
 $('device-choice').value=''; $('device-choice').onchange();
 uxAssert(writes.length===1 && writes[0]==='Choose a telescope', 'empty choice must write final status once');
 writes=[]; render(multiple);
 uxAssert(writes.length===0, 'unchanged empty poll mutated status');
}

async function checkPickerLabels() {
 const {idle,multiple}=uxFixture(); render({...idle,devices:[]});
 const label=()=>$('device-choice').children[0].textContent;
 uxAssert(label()==='Find on my account', 'initial empty label');
 checkDiscoveryHelp('before discovery');
 render(multiple);
 uxAssert(label()==='Choose a telescope…', 'selection empty label');
 $('email').value='other@example.invalid'; $('email').dispatchEvent({type:'input'});
 uxAssert(label()==='Find on my account', 'account edit did not reset label');
 checkDiscoveryHelp('account edit');
 $('email').value=idle.settings.email; $('email').dispatchEvent({type:'input'});
 uxAssert(label()==='Choose a telescope…', 'same account selection label');
 render(idle);
 uxAssert(label()==='Find on my account', 'cleared step did not reset label');
 checkDiscoveryHelp('cleared step');
}

function checkDiscoveryHelp(step) {
 uxAssert(!$('selection-help').hidden && $('selection-help').textContent === 'Connect uses your only telescope, or asks which one if there are several.', 'discovery help '+step);
 uxAssert($('device-choice').attributes['aria-describedby'] === 'selection-help', 'discovery help association '+step);
}

async function checkManualPicker(scenario) {
 const {idle,multiple}=uxFixture(); render(scenario === 'initial order' ? idle : multiple);
 const picker=$('device-choice'); picker.focus();
 picker.value='custom'; picker.onchange();
 if (scenario === 'label') {
  uxAssert(picker.children[picker.children.length-1].textContent === 'Enter manually…', 'manual option label');
 } else if (scenario === 'order' || scenario === 'initial order') {
  uxAssert(!$('custom-device').hidden && $('start').parentElement === $('picker-action'), 'manual Connect location');
  const parent=$('custom-device').parentElement;
  uxAssert(parent === $('picker-action').parentElement && parent.children.indexOf($('custom-device')) < parent.children.indexOf($('picker-action')), 'manual fields must precede Connect in DOM');
  uxAssert(document.activeElement === picker, 'manual choice moved focus');
 } else if (scenario === 'empty status') {
  uxAssert($('status').textContent === 'Enter a serial number.', 'empty manual choice claimed readiness');
 } else if (scenario === 'serial help') {
  uxAssert(!$('serial-help').hidden && $('serial-help').textContent === 'Enter a serial number.', 'empty manual serial help');
  uxAssert($('serial').attributes['aria-describedby'].split(' ').includes('serial-help'), 'serial help association');
 } else if (scenario === 'validation') {
  let requests=0; api=async()=>{requests++;return idle;};
  $('serial').value='   '; $('serial').dispatchEvent({type:'input'});
  await act('start');
  uxAssert(requests === 0, 'empty manual serial made a request');
  uxAssert(document.activeElement === $('serial') && $('serial').attributes['aria-invalid'] === 'true' && !$('serial-error').hidden && $('serial-error').textContent === 'Enter a serial number.', 'empty manual serial validation');
 } else if (scenario === 'optional model') {
  $('serial').value='synthetic-manual'; $('serial').dispatchEvent({type:'input'});
  uxAssert($('status').textContent === 'Ready to connect' && $('serial-help').hidden, 'usable manual serial not ready');
  let submitted; api=async(name,body)=>{if(name==='start')submitted=body;return name==='status'?idle:{ok:true};};
  await act('start');
  uxAssert(submitted?.settings.serial === 'synthetic-manual' && submitted.settings.model === '', 'manual serial with optional Model was not submitted');
 }
}

async function checkSavedChoice(scenario) {
 const {idle}=uxFixture();
 const saved={...idle.settings,serial:'synthetic-saved',model:'<img>\tmodel',choice_label:'<img>\\tmodel synthetic-saved'};
 const response={...idle,settings:saved,devices:[]};
 load(saved); render(response);
 if(scenario === 'label') {
  uxAssert($('device-choice').children[1].textContent === 'Saved: '+saved.choice_label, 'saved-only label provenance or escaping');
  uxAssert(settings().serial === saved.serial && settings().model === saved.model, 'saved label changed the wire pair');
 } else if(scenario === 'help') {
  uxAssert(!$('selection-help').hidden && $('selection-help').textContent === 'Choose “Find on my account”, then Connect to load other telescopes.', 'saved-only reload help');
  $('device-choice').value=''; $('device-choice').onchange();
  uxAssert($('selection-help').textContent === 'Choose “Find on my account”, then Connect to load other telescopes.', 'saved reload help lost on empty choice');
 } else if(scenario === 'received') {
  render({...response,devices:[{serial:saved.serial,model:saved.model,label:saved.choice_label}]});
  uxAssert(pickerDevices.length === 1 && $('device-choice').children[1].textContent === saved.choice_label, 'received match still labeled Saved or duplicated');
  checkDiscoveryHelp('received saved pair');
  render(response);
  uxAssert($('device-choice').children[1].textContent === 'Saved: '+saved.choice_label, 'saved provenance did not return after received list cleared');
 } else if(scenario === 'account') {
  $('email').value='other@example.invalid'; $('email').dispatchEvent({type:'input'});
  uxAssert(!pickerDevices.length, 'other account retained saved-only option');
  checkDiscoveryHelp('saved account edit');
 }
}

async function checkChangeGroup() {
 const {idle}=uxFixture(); render({...idle,active:true,selected_device:'Synthetic ***1234'});
 uxAssert($('change-telescope').parentElement === $('change-telescope-group') && $('change-telescope-help').parentElement === $('change-telescope-group'), 'Change telescope and consequence must share a wrap group');
 uxAssert(!$('change-telescope-group').hidden, 'Change telescope group hidden with selection');
 render(idle);
 uxAssert($('change-telescope-group').hidden, 'orphaned Change telescope group');
}
