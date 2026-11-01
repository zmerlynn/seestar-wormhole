// Deferred response schedule: an action refresh is superseded by a pending poll.
async function checkChangeTelescopeAcrossPoll() {
 const original={email:'synthetic@example.invalid',serial:'synthetic-review-1234',model:'Future telescope',bind:'127.0.0.1'};
 const stopped={active:false,status:'Stopped. Seestar may still be releasing the session.',settings:{...original,serial:'',model:''},version:'synthetic',effective:{listen:'this_computer',idle:{mode:'keep_connected'},ports:{}},password:{memory:true},events:[]};
 initialized=connected=true;
 load(original);
 render({...stopped,active:true,settings:original,selected_device:'Future telescope ***1234'});
 const receipts=[];
 api=async name=>{
  if(name==='change-telescope')return {ok:true};
  if(name==='status')return new Promise(resolve=>receipts.push(resolve));
  throw new Error('unexpected action');
 };
 $('mode').value='idle'; $('idle').value=7;
 const changing=act('change-telescope');
 for(let i=0; i<10 && receipts.length<1; i++)await Promise.resolve();
 if(receipts.length!==1)throw new Error('first refresh missing');
 const polling=poll();
 if(receipts.length!==2)throw new Error('overlapping poll missing');
 receipts[0](stopped);
 await changing;
 receipts[1](stopped);
 await polling;
 if(state.settings.serial!==''||active)throw new Error('fixture did not reach cleared server status');
 if($('serial').value!==''||$('model').value!=='')throw new Error('FAIL: completed change and later poll leave old telescope fields: '+JSON.stringify({serial:$('serial').value,model:$('model').value}));
 if($('idle').value!==7)throw new Error('change overwrote an unrelated form draft');
 let submitted;
 api=async (name,body)=>{if(name==='start')submitted=body.settings;return name==='status'?stopped:{ok:true};};
 await act('start');
 if(!submitted||submitted.serial!==''||submitted.model!=='')throw new Error('Connect submitted an old telescope after change');
}
