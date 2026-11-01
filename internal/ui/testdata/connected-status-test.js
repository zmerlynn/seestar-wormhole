async function checkConnectedStatus(response, want) {
 const assert=(ok,message)=>{if(!ok)throw new Error(message);};
 initialized=connected=true;
 load(response.settings);
 let value='',writes=[];
 Object.defineProperty($('status'),'textContent',{configurable:true,get:()=>value,set:next=>{value=next;writes.push(next);}});
 render(response);
 assert(value===want && writes.length===1 && writes[0]===want,'selected status must be written once: '+JSON.stringify(writes));
 assert($('start').textContent==='Stop' && $('start').parentElement===$('primary-action') && !$('primary-action').hidden,'Stop below selected status');
 assert($('settings').hidden,'running settings visible');
 assert($('events').children.length===1 && $('events').children[0].textContent.endsWith('  '+want),'Recent events lost selected status');
 const copied=JSON.parse(diagnostic);
 assert(copied.events.length===1 && copied.events[0].message===want && copied.events[0].code==='lifecycle','copied summary lost selected lifecycle status');
 writes=[];render(response);
 assert(writes.length===0,'unchanged status poll wrote live region');
}
