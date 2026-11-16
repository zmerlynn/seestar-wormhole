// A copy-only tab's wording, on the DOM double: the holder is another
// tab of this app, another seestar-wormhole window, or unknown.
async function checkCopyOnly(response, holder, framed) {
 const assert = (ok, message) => {if (!ok) throw new Error(message);};
 initialized = connected = true;
 load(response.settings);
 render(response);
 const kind = holder === 'tab' ? ' tab' : ' window', here = framed ? 'this tab' : 'this window';
 const named = holder ? 'The Garden' + kind : 'Another seestar-wormhole window';
 assert(!$('copy-only').hidden, 'copy-only notice hidden');
 assert($('copy-only-text').textContent === named + ' is serving the Seestar app' + (holder ? ' (Seestar S50 Pro TEST0002)' : '') + " on this computer's ports, so " + here + " can't. Copy, Browse and Free up space work here as usual.", 'notice: ' + $('copy-only-text').textContent);
 assert($('copy-only-how').textContent === 'To use the Seestar app with this telescope instead: Stop the connection in ' + (holder ? 'the Garden' + kind : 'the other window') + ', then Stop and Connect here.', 'how: ' + $('copy-only-how').textContent);
 assert($('connection-text').textContent.endsWith(' through Telescope Network, for copying only') && $('connection-dot').getAttribute('data-tone') === 'on', 'header: ' + $('connection-text').textContent);
 assert($('run-summary').hidden && $('phone-steps').hidden && $('copy-smb').hidden && $('running-ports-box').hidden, 'serving help still shown');
 // Serving again, the help is back and the notice gone.
 render({...response, copy_only: undefined});
 assert($('copy-only').hidden && !$('run-summary').hidden && !$('phone-steps').hidden && !$('copy-smb').hidden && !$('running-ports-box').hidden, 'serving help not restored');
 assert(!$('connection-text').textContent.includes('copying only'), 'still copying only');
}

// Framed in the shell, the page's Quit asks the shell, which quits every
// tab; alone, it quits itself.
async function checkFramedQuit(framed) {
 const assert = (ok, message) => {if (!ok) throw new Error(message);};
 const calls = [];
 let asked = 0;
 window.top.confirmQuit = () => asked++;
 api = async action => {calls.push(action); return {ok: true};};
 initialized = true;
 $('quit').dispatchEvent({type: 'click'});
 for (let i = 0; i < 5; i++) await Promise.resolve();
 if (framed) assert(asked === 1 && !calls.includes('quit'), 'framed Quit: asked ' + asked + ', calls ' + JSON.stringify(calls));
 else assert(asked === 0 && calls.includes('quit'), 'Quit: asked ' + asked + ', calls ' + JSON.stringify(calls));
}
