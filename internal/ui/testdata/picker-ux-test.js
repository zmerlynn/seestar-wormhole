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
