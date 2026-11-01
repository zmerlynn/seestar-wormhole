async function checkDetailsPoll(s, scenario) {
 const assert=(ok,message)=>{if(!ok)throw new Error(message);};
 initialized=connected=true;
 load(s.settings);
 render(s);
 const ids=['effective-settings','events','log-path','running-ports','ports'];
 const nodes=ids.map(id=>$(id));
 let writes=[];
 for(const node of nodes) {
  let value=node.textContent;
  Object.defineProperty(node,'textContent',{configurable:true,get:()=>value,set:next=>{writes.push(node);value=next;}});
  const replace=node.replaceChildren;
  node.replaceChildren=function(...args){writes.push(node);return replace.apply(this,args);};
 }
 const checkContent=current=>{
  assert($('effective-settings').textContent===JSON.stringify(current.effective,null,2),'effective content stale');
  assert($('log-path').textContent===(current.log_path ? 'Private log: '+current.log_path : ''),'path stale');
  const lines=current.events.map(event=>new Date(event.time).toLocaleTimeString([], {hour:'numeric',minute:'2-digit'})+'  '+event.message);
  assert($('events').children.length===lines.length,'event count stale');
  lines.forEach((line,i)=>assert($('events').children[i].textContent===line,scenario+' event content/order stale at '+i));
  const copied=JSON.parse(diagnostic);
  assert(JSON.stringify(copied.events)===JSON.stringify(current.events),'copied events stale');
  assert(JSON.stringify(copied.settings)===JSON.stringify(current.effective),'copied effective settings stale');
 };
 const checkRepeated=current=>{
  const children=nodes.map(node=>Array.from(node.children));
  const values=nodes.map(node=>node.textContent);
  writes=[];
  render(JSON.parse(JSON.stringify(current)));
  assert(writes.length===0,'unchanged poll wrote Details: '+writes.length);
  ids.forEach((id,i)=>{
   assert($(id)===nodes[i] && $(id).textContent===values[i],id+' identity/text changed');
   assert($(id).children.length===children[i].length && children[i].every((child,j)=>$(id).children[j]===child),id+' children replaced');
  });
  checkContent(current);
 };
 checkContent(s);
 checkRepeated(s);
 const changed=JSON.parse(JSON.stringify(s));
 let changedIDs=['events'];
 switch(scenario) {
  case 'rolling':
   // At capacity, every newly received event replaces the oldest row.
   changed.events.shift();
   changed.events.push({time:'2026-01-01T02:00:00Z',message:'rolling-new',code:'lifecycle'});
   break;
  case 'replacement': changed.events[23].message='replacement with a different length';break;
  case 'same length': changed.events[23].message='other-23';break;
  case 'reorder': changed.events.reverse();break;
  case 'effective': changed.effective.ports['445']=5678;changedIDs=['effective-settings','running-ports'];break;
  case 'path': changed.log_path='alternate-path';changedIDs=['log-path'];break;
  default: throw new Error('unknown Details scenario');
 }
 assert(changed.events.length===s.events.length,'fixture must retain a full event buffer');
 const oldPorts=$('running-ports').children[0];
 writes=[];render(changed);
 for(const id of changedIDs) assert(writes.includes($(id)),id+' change not written');
 checkContent(changed);
 if(scenario==='effective') {
  assert($('running-ports').children[0]!==oldPorts,'changed ports retained old table');
  assert(String($('running-ports').children[0].children[1].children[9].children[0].textContent)==='5678','ports table stale');
 }
 checkRepeated(changed);
 const cleared={...changed,events:[],log_path:''};render(cleared);
 checkContent(cleared);
 checkRepeated(cleared);
 const repopulated={...changed,events:s.events.slice(-2),log_path:s.log_path};render(repopulated);
 checkContent(repopulated);
 checkRepeated(repopulated);
}
