async function checkReopenedAccount(page, want) {
 initialized=connected=true;
 load(page.settings);render(page);
 const pair=()=>({email:settings().email,serial:settings().serial,model:settings().model});
 const expected={email:want.email,serial:want.serial,model:want.model};
 if(JSON.stringify(pair())!==JSON.stringify(expected))throw new Error('reopened draft: '+JSON.stringify(pair())+' want '+JSON.stringify(expected));
 const index=pickerDevices.findIndex(d=>d.serial===want.serial&&d.model===want.model);
 if(want.serial){
  if(index<0||$('device-choice').value!==String(index))throw new Error('reopened saved pair not selected');
  if(!$('device-choice').children[index+1].textContent.startsWith('Saved: '))throw new Error('reopened saved provenance lost');
 }else if(pickerDevices.length||$('device-choice').value!=='')throw new Error('reopened account has previous saved choice');
 if(page.password.saved!==usingSaved())throw new Error('reopened password account feedback lost');
 if($('retry-storage').hidden===page.save_failed)throw new Error('reopened retry control lost');
}
