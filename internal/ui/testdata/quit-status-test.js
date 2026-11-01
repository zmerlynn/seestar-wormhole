async function checkQuitStatus(native) {
 initialized=connected=true;
 const settings={email:'synthetic@example.invalid',bind:'127.0.0.1',serial:'',model:''};
 const connectedState={active:true,status:'Connected to your Seestar',settings,version:'synthetic',effective:{listen:'this_computer',idle:{mode:'keep_connected'},ports:{}},password:{memory:true},events:[],native};
 load(settings);render(connectedState);
 const status=$('status');let value=status.textContent,writes=[];
 Object.defineProperty(status,'textContent',{get(){return value;},set(next){writes.push(next);value=next;}});
 api=async name=>{if(name==='quit')return {ok:true};throw new Error('unexpected request '+name);};
 await act('quit');
 const want=native?'Quit complete.':'Service stopped. You can close this tab.';
 print(JSON.stringify({native,ended,status:$('status').textContent,want,disabled:$('start').disabled}));
 if(!ended || $('status').textContent!==want || !$('start').disabled || !$('quit').disabled || !$('options').disabled)throw new Error('controls() overwrote successful quit status with stale server state');
 if(writes.length!==1||writes[0]!==want)throw new Error('Quit must write the final status exactly once: '+JSON.stringify(writes));
 writes=[];controls();render(connectedState);
 if(writes.length)throw new Error('unchanged terminal controls/poll wrote status: '+JSON.stringify(writes));
}

