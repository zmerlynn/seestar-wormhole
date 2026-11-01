// Chosen received and saved pairs must leave the draft on an account edit.
async function checkSelectedAccountChange(origin, manual) {
 const failures=[];
 const check=(ok,message)=>{if(!ok)failures.push(message);};
 initialized=connected=true;
 const first={email:'first@example.invalid',bind:'127.0.0.1',serial:'',model:''};
 const pair={serial:'synthetic-account-one-1234',model:'Future one',label:'Future one synthetic-account-one-1234'};
 const choices=[pair,{serial:'synthetic-account-one-5678',model:'Future two',label:'Future two synthetic-account-one-5678'}];
 const saved=origin==='saved'?{...first,serial:pair.serial,model:pair.model,choice_label:pair.label}:first;
 const old={active:false,status:origin==='saved'?'Stopped':'Choose a telescope',settings:saved,devices_email:first.email,devices:origin==='saved'?[]:choices,version:'synthetic',effective:{listen:'this_computer',idle:{mode:'keep_connected'},ports:{}},password:{memory:true},events:[],failure:origin==='saved'?undefined:{code:'selection_multiple',devices:choices}};
 load(saved);render(old);
 if(origin==='received'){$('device-choice').value='0';$('device-choice').dispatchEvent({type:'change'});}
 check(settings().serial===pair.serial&&settings().model===pair.model,'fixture did not choose first pair');
 $('email').focus();
 const focus=document.activeElement;
 const before=settings();
 $('email').value=' first @example.invalid ';$('email').dispatchEvent({type:'input'});
 check(JSON.stringify(settings())===JSON.stringify(before),'normalization-only edit changed settings');
 const samePicker=$('device-choice').value;
 render(old);
 check($('device-choice').value===samePicker,'same-account poll lost choice');
 $('email').value='second@example.invalid';$('email').dispatchEvent({type:'input'});
 const empty=()=>settings().serial===''&&settings().model===''&&$('device-choice').value==='';
 check(empty(),'account edit did not clear pair and select discovery');
 check(document.activeElement===focus,'account edit moved focus');
 $('password').value='synthetic-password';$('password').dispatchEvent({type:'input'});
 render(old);render(old);
 check(empty(),'delayed previous-account polls restored pair or picker');
 check(document.activeElement===focus,'delayed polls moved focus');
 check(!selectionStep&&pickerDevices.length===0,'old account choices or guard survived');
 const expected={...before,email:'second@example.invalid',serial:'',model:''};
 if(manual){
  $('device-choice').value='custom';$('device-choice').dispatchEvent({type:'change'});
  $('serial').value='synthetic-manual-two-9012';$('serial').dispatchEvent({type:'input'});
  $('model').value='Future manual';$('model').dispatchEvent({type:'input'});
  Object.assign(expected,{serial:'synthetic-manual-two-9012',model:'Future manual'});
  // B's normalization-only edits must preserve manual entry even while state
  // still belongs to A; comparing only against state.settings.email loses it.
  $('email').value=' second @example.invalid ';$('email').dispatchEvent({type:'input'});
  $('password').value='synthetic-password';$('password').dispatchEvent({type:'input'});
  render(old);render(old);
  check($('device-choice').value==='custom','manual entry lost its picker state');
 }
 let submitted;
 api=async(name,body)=>{if(name==='start')submitted=body.settings;return name==='status'?{...old,settings:{...first,email:'second@example.invalid'},devices_email:'second@example.invalid',devices:[],failure:undefined}:{ok:true};};
 await act('start');
 check(JSON.stringify(submitted)===JSON.stringify(expected),'exact Connect settings: got '+JSON.stringify(submitted)+' want '+JSON.stringify(expected));
 if(failures.length)throw new Error(failures.join('; '));
}
