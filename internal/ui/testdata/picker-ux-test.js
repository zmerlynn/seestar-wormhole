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
