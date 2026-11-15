async function checkActivityError(trail) {
 const assert=(ok,message)=>{if(!ok)throw new Error(message);};
 state={events:trail.events, failures:trail.failures};
 renderEvents();
 const shown=$('events').children.map(item=>item.textContent);
 assert(shown.length===3,'Activity rows '+JSON.stringify(shown));
 const [browse, connection, plain]=trail.events.slice().reverse();
 assert(browse.error && connection.error && !plain.error,'trail errors '+JSON.stringify(trail.events));
 assert(shown[0].endsWith('  Browse  '+browse.message+' open /Users/me/Seestar/a.fit: permission denied'),'Browse row '+shown[0]);
 assert(shown[1].endsWith('  Connection  '+connection.message+' '+connection.error) && connection.error.includes('<b>dial tcp 192.0.2.10:443</b>: connection refused'),'connection row '+shown[1]);
 assert(shown[2].endsWith('  Connection  Connecting'),'row without an error '+shown[2]);
}
