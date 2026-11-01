// Extends the Custom Connect -> Stop reproduction with every save route.
async function checkSavedPicker(scenario) {
 const failures=[];
 const assert=(ok,message)=>{if(!ok)failures.push(message);};
 initialized=connected=true;
 const original={email:'synthetic@example.invalid',bind:'127.0.0.1',serial:'synthetic-old-1234',model:'Old',choice_label:'Old synthetic-old-1234'};
 const current={...original,serial:'synthetic-new-5678',model:'Future',choice_label:'Future synthetic-new-5678'};
 const received={serial:'synthetic-received-9012',model:'S30',label:'S30 synthetic-received-9012'};
 const devices=[received];
 if(scenario==='deduplicate') devices.push({...current,label:current.choice_label});
 if(scenario==='received saved pair') devices.push({...original,label:original.choice_label});
 const idle={active:false,status:'Stopped',settings:original,devices_email:original.email,devices,version:'synthetic',effective:{listen:'this_computer',idle:{mode:'keep_connected'},ports:{}},password:{memory:true},events:[]};
 load(original);render(idle);
 $('device-choice').value='custom';$('device-choice').onchange();
 $('serial').value=current.serial;$('model').value=current.model;
 $('mode').value='idle';$('idle').value=7;
 let saved=scenario==='change'?{...current,serial:'',model:'',choice_label:''}:current;
 if(scenario==='failed') {
  api=async name=>{if(name==='password/save')throw {code:'storage'};return {...idle,save_failed:true};};
  await act('password/save');
  saved=original;
 } else if(scenario==='boundary') {
  // The ready boundary saves the Custom pair, and Stop leaves that entry available.
  render({...idle,active:true,settings:current,selected_device:'Future ***5678'});
  api=async name=>name==='status'?{...idle,settings:current}:{ok:true};
  await act('stop');
 } else {
  api=async name=>name==='status'?{...idle,settings:saved}:{ok:true};
  const action={password:'password/save',change:'change-telescope',retry:'retry-storage'}[scenario];
  if(action) await act(action); else render({...idle,settings:saved});
 }
 const count=pair=>pickerDevices.filter(d=>d.serial===pair.serial&&d.model===pair.model).length;
 assert(count(saved)===(saved.serial?1:0),'saved entry is not the current pair exactly once');
 if(scenario!=='failed'&&scenario!=='received saved pair') assert(count(original)===0,'superseded saved-only entry remains');
 if(scenario==='failed') assert(count(current)===0,'failed save installed an unsaved pair');
 for(const device of devices) assert(count(device)===1,'received entry lost or duplicated');
 assert($('idle').value===7,'saved projection overwrote unrelated draft');
 if(!['change','retry'].includes(scenario)) assert($('serial').value===current.serial&&$('model').value===current.model,'saved projection overwrote Custom draft');
 // Automatic does not discard the saved entry; selecting it fills the current pair.
 $('device-choice').value='';$('device-choice').onchange();
 const index=pickerDevices.findIndex(d=>d.serial===saved.serial&&d.model===saved.model);
 if(saved.serial) {
  assert(index>=0,'Automatic lost the current saved choice');
  if(index>=0) {$('device-choice').value=String(index);$('device-choice').onchange();
   assert($('serial').value===saved.serial&&$('model').value===saved.model,'saved choice fills stale pair');}
 }
 if(failures.length)throw new Error(failures.join('; '));
}
