async function checkAccountPicker() {
 const failures=[];
 initialized = connected = true;
 const original = {email:'first@example.invalid',bind:'127.0.0.1',serial:'',model:''};
 const choices = [{serial:'synthetic-first-account-1234',model:'S30',label:'S30 synthetic-first-account-1234'}, {serial:'synthetic-first-account-5678',model:'S50',label:'S50 synthetic-first-account-5678'}];
 const idle = {active:false,status:'Choose a telescope',settings:original,version:'synthetic',effective:{listen:'this_computer',idle:{mode:'keep_connected'},ports:{}},password:{memory:true},events:[],devices_email:original.email,devices:choices,failure:{code:'selection_multiple',devices:choices}};
 load(original); render(idle);
 // Normalization must retain the same account's selection guard and list.
 $('email').value=' first @example.invalid ';
 $('email').dispatchEvent({type:'input'});
 render(idle);
 if (!selectionStep || pickerDevices.length!==2) failures.push('normalized same-account choices lost');
 $('email').value='second@example.invalid';
 $('email').dispatchEvent({type:'input'});
 if (selectionStep || pickerDevices.length) failures.push('account input kept old selection');
 $('password').value='synthetic-password'; $('password').dispatchEvent({type:'input'});
 // A delayed old status poll must neither restore A's choices nor its guard.
 render(idle);
 if (pickerDevices.some(d=>choices.some(old=>old.serial===d.serial))) failures.push('old status poll restored previous account choices');
 if (selectionStep) failures.push('old status poll restored previous account guard');
 let submitted=false;
 api=async name=>{if(name==='start')submitted=true; return name==='status'?{...idle,devices:[],failure:undefined,settings:{...original,email:'second@example.invalid'}}:{ok:true};};
 await act('start');
 if(!submitted) failures.push('new account cannot Connect with Find on my account: old selectionStep intercepted the action');
 if (selectionStep || pickerDevices.length) failures.push('new account retained old choices after start');
 // Reopening B's controls while an A status response exists must suppress it.
 load({...original,email:'second@example.invalid'}); render(idle);
 if (selectionStep || pickerDevices.length) failures.push('reopened account controls received old selection');
 if (failures.length) throw new Error(failures.join('; '));
}
