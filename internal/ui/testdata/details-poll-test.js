async function checkDetailsPoll(s) {
 const assert=(ok,message)=>{if(!ok)throw new Error(message);};
 initialized=connected=true;
 load(s.settings);
 render(s);
 const ids=['effective-settings','events','log-path','running-ports','ports'];
 const nodes=ids.map(id=>$(id));
 const children=nodes.map(node=>Array.from(node.children));
 const values=nodes.map(node=>node.textContent);
 let writes=[];
 for(const node of nodes) {
  let value=node.textContent;
  Object.defineProperty(node,'textContent',{configurable:true,get:()=>value,set:next=>{writes.push(node);value=next;}});
  const replace=node.replaceChildren;
  node.replaceChildren=function(...args){writes.push(node);return replace.apply(this,args);};
 }
 // A fresh response object with identical content must preserve every child.
 render(JSON.parse(JSON.stringify(s)));
 assert(writes.length===0,'unchanged poll wrote Details: '+writes.length);
 ids.forEach((id,i)=>{
  assert($(id)===nodes[i] && $(id).textContent===values[i],id+' identity/text changed');
  assert($(id).children.length===children[i].length && children[i].every((child,j)=>$(id).children[j]===child),id+' children replaced');
 });
 const changed=JSON.parse(JSON.stringify(s));
 changed.effective.ports['445']=12345;
 changed.events.push({time:'2026-01-01T00:01:00Z',message:'Account telescopes: S30 synthetic-details',code:'lifecycle'});
 changed.log_path='synthetic-new-path';
 render(changed);
 for(const id of ['effective-settings','events','log-path','running-ports']) assert(writes.includes($(id)),id+' change not written');
 assert($('effective-settings').textContent===JSON.stringify(changed.effective,null,2),'effective content stale');
 assert($('log-path').textContent==='Private log: synthetic-new-path','path stale');
 assert($('events').children.length===2 && $('events').children[1].textContent.endsWith('  Account telescopes: S30 synthetic-details'),'event content stale');
 assert($('running-ports').children[0]!==children[3][0] && $('running-ports').children[0].children[1].children[9].children[0].textContent===12345,'ports table stale');
 assert(JSON.parse(diagnostic).events[1].message===changed.events[1].message,'catalog missing from copy');
 writes=[];render(JSON.parse(JSON.stringify(changed)));
 assert(writes.length===0,'repeated changed poll wrote Details');
 changed.events=[];changed.log_path='';render(changed);
 assert($('events').children.length===0 && $('log-path').textContent==='','cleared Details stale');
}
