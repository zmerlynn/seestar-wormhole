async function checkFullSerial(response, want, catalog) {
 const assert=(ok,message)=>{if(!ok)throw new Error(message);};
 initialized=connected=true;
 load(response.settings);
 render(response);
 assert($('status').textContent===want,'status lost full serial');
 const messages=[want,catalog];
 assert($('events').children.length===2,'Recent events missing');
 messages.forEach((message,i)=>assert($('events').children[i].textContent.endsWith('  '+message),'Recent events lost full serial/order'));
 const copied=JSON.parse(diagnostic);
 assert(copied.events.length===2 && messages.every((message,i)=>copied.events[i].message===message),'copied summary lost full serial/order');
}
