// Read geometry only when the queued layout callback runs, after focus.
async function checkPickerLayoutReveal() {
 const assert=(ok,message)=>{if(!ok)throw new Error(message);};
 initialized=connected=true;
 const settings={email:'synthetic@example.invalid',serial:'',model:''};
 load(settings);
 const idle={active:false,status:'Not connected',settings,devices_email:settings.email,devices:[],effective:{listen:'this_computer',idle:{mode:'keep_connected'},ports:{}},password:{memory:true},events:[]};
 const choosing={...idle,status:'Choose a telescope',failure:{code:'selection_multiple',devices:[{serial:'synthetic-choice',model:'S30',label:'S30 synthetic-choice'}]}};
 const boxes={'device-choice':{top:700,bottom:744,left:20,right:740},'selection-help':{top:750,bottom:780,left:20,right:740},'start':{top:800,bottom:844,left:20,right:140}};
 let reads=0,scrolls=[];
 for(const [id,box] of Object.entries(boxes)) $(id).getBoundingClientRect=()=>{reads++;return {...box};};
 window.scrollBy=(x,y)=>{scrolls.push([x,y]);for(const box of Object.values(boxes)){box.top-=y;box.bottom-=y;box.left-=x;box.right-=x;}};
 globalThis.innerHeight=600;globalThis.innerWidth=800;
 globalThis.getComputedStyle=()=>({outlineWidth:'3px',outlineOffset:'3px'});
 const enter=()=>{render(idle);render(choosing);};
 const flush=()=>{const frames=testFrames.splice(0);frames.forEach(callback=>callback());};
 enter();
 assert(document.activeElement===$('device-choice'),'picker did not receive focus');
 assert(reads===0 && scrolls.length===0 && testFrames.length===1,'reveal must wait for layout once');
 // The action can move after the focus call; read its final layout.
 boxes.start.top=830.25;boxes.start.bottom=874.25;
 controls();render(choosing);
 assert(testFrames.length===1,'unchanged polls queued another reveal');
 const focused=$('device-choice').focusCount;
 flush();
 assert(scrolls.length===1 && scrolls[0][1]===281,'fitted group was not revealed after layout: '+JSON.stringify(scrolls));
 assert(boxes['device-choice'].top>=6 && boxes.start.bottom<=innerHeight-6,'picker outline or Connect clipped');
 assert($('device-choice').focusCount===focused,'layout reveal focused again');
 controls();render(choosing);flush();
 assert(scrolls.length===1 && $('device-choice').focusCount===focused,'poll repeated reveal or focus');
 // If the group is too tall, reveal the focused picker and its full outline.
 boxes['device-choice'].top=-2;boxes['device-choice'].bottom=42;boxes.start.top=900;boxes.start.bottom=944;
 enter();flush();
 assert(scrolls.length===2 && scrolls[1][1]===-8,'oversized group failed to reveal picker outline');
 // Horizontal clipping also includes the focus outline.
 boxes['device-choice'].left=2;boxes['device-choice'].right=722;
 enter();flush();
 assert(scrolls.length===3 && scrolls[2][0]===-4,'horizontal outline was not revealed');
 // A user moving focus before the callback must keep their new focus/viewport.
 Object.assign(boxes['device-choice'],{top:700,bottom:744,left:20,right:740});
 Object.assign(boxes['selection-help'],{top:750,bottom:780});
 Object.assign(boxes.start,{top:800,bottom:844});
 enter();$('password').focus();flush();
 assert(document.activeElement===$('password') && scrolls.length===3,'delayed reveal displaced user');
 // A new active session must cancel the queued idle-picker reveal.
 enter();render({...idle,active:true});flush();
 assert(scrolls.length===3,'hidden picker caused delayed scrolling');
}
