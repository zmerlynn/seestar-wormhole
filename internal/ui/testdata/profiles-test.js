// The Profiles dialog, the profile's label and a copy-only window's
// wording, on the DOM double. The dialog double opens and closes as a
// browser's does, and closing fires close.
function profileDialogDouble() {
 const dialog = $('p-dialog');
 dialog.open = false;
 dialog.showModal = function () {this.open = true;};
 dialog.close = function () {if (!this.open) return; this.open = false; this.dispatchEvent({type: 'close'});};
 return dialog;
}
const settle = async () => {for (let i = 0; i < 5; i++) await Promise.resolve();};

async function checkProfilesDialog() {
 const assert = (ok, message) => {if (!ok) throw new Error(message);};
 const dialog = profileDialogDouble();
 const requests = [];
 let answer = {};
 api = async (action, body) => {
  requests.push([action, body]);
  if (action === 'profiles') return {profiles: [
   {name: 'default', label: 'Default', self: true, open: false},
   {name: 'Backyard', label: 'Backyard', self: false, open: false},
   {name: 'Garden', label: 'Garden', self: false, open: true}]};
  if (answer[action]) throw answer[action];
  return {ok: true};
 };
 const quit = $('quit');
 quit.focus();
 quit.isConnected = true;

 // From the footer: Cancel, focused; rows say which is this window and
 // which are open.
 $('profiles-open').dispatchEvent({type: 'click'});
 await settle();
 assert(dialog.open, 'Profiles did not open');
 assert(!$('p-cancel').hidden && $('p-show').hidden && document.activeElement === $('p-cancel'), 'from the footer, Cancel is shown and focused');
 const rows = $('p-list').children.map(row => row.children[0].textContent);
 assert(JSON.stringify(rows) === JSON.stringify(['Default — this window', 'Backyard', 'Garden — open']), 'rows: ' + JSON.stringify(rows));
 $('p-list').children[2].children[0].dispatchEvent({type: 'click'});
 await settle();
 assert(JSON.stringify(requests.at(-1)) === JSON.stringify(['profiles/open', {name: 'Garden'}]), 'Garden not opened: ' + JSON.stringify(requests.at(-1)));
 assert(!dialog.open && document.activeElement === quit, 'opening Garden left the dialog open or lost focus');

 // A profile that couldn't be opened says why under the list.
 answer['profiles/open'] = {code: 'profile_open', message: "Couldn't open Backyard: synthetic failure."};
 $('profiles-open').dispatchEvent({type: 'click'});
 await settle();
 $('p-list').children[1].children[0].dispatchEvent({type: 'click'});
 await settle();
 assert(dialog.open && !$('p-err').hidden && $('p-err').textContent === "Couldn't open Backyard: synthetic failure.", 'open failure not shown');
 $('p-cancel').dispatchEvent({type: 'click'});
 assert(!dialog.open, 'Cancel left the dialog open');

 // From a relaunch: Show this window, focused; Escape closes as it does.
 renderProfile({label: 'Default', ask: true});
 await settle();
 assert(dialog.open && !$('p-show').hidden && $('p-cancel').hidden && document.activeElement === $('p-show'), 'a relaunch shows and focuses Show this window');
 assert($('p-err').hidden, 'an old failure is still shown');
 dialog.close();
 assert(!dialog.open, 'Escape left the dialog open');

 // New profile: a refusal shows under the name, typing clears it, and a
 // created profile closes the dialog.
 $('profiles-open').dispatchEvent({type: 'click'});
 await settle();
 $('p-new').dispatchEvent({type: 'click'});
 assert($('p-main').hidden && !$('p-form').hidden && document.activeElement === $('p-name'), 'New profile not shown with the name focused');
 answer['profiles/new'] = {code: 'profile_name', message: "There's already a profile named Garden."};
 $('p-name').value = 'garden';
 $('p-form').dispatchEvent({type: 'submit', preventDefault() {}});
 await settle();
 assert(JSON.stringify(requests.at(-1)) === JSON.stringify(['profiles/new', {name: 'garden'}]), 'name not sent as typed');
 assert(dialog.open && !$('p-name-err').hidden && $('p-name-err').textContent === "There's already a profile named Garden." && $('p-name').getAttribute('aria-invalid') === 'true', 'refusal not shown under the name');
 $('p-name').dispatchEvent({type: 'input'});
 assert($('p-name-err').hidden && $('p-name').getAttribute('aria-invalid') === 'false', 'typing kept the refusal');
 delete answer['profiles/new'];
 $('p-name').value = 'Backyard S30';
 $('p-form').dispatchEvent({type: 'submit', preventDefault() {}});
 await settle();
 assert(!dialog.open, 'a created profile left the dialog open');

 // Reopened, it starts from the list again.
 $('profiles-open').dispatchEvent({type: 'click'});
 await settle();
 assert(!$('p-main').hidden && $('p-form').hidden, 'reopened on the form');
 $('p-cancel').dispatchEvent({type: 'click'});
}

async function checkProfileLabel() {
 const assert = (ok, message) => {if (!ok) throw new Error(message);};
 profileDialogDouble();
 renderProfile(undefined);
 assert($('profiles-open').hidden && $('profile-name').hidden && document.title === 'seestar-wormhole', 'a run of no profile shows one');
 renderProfile({label: ''});
 assert(!$('profiles-open').hidden && $('profile-name').hidden && document.title === 'seestar-wormhole', 'Default alone is named');
 renderProfile({label: 'Garden'});
 assert(!$('profile-name').hidden && $('profile-name').textContent === '· Garden' && document.title === 'seestar-wormhole — Garden', 'Garden not named: ' + $('profile-name').textContent + ' / ' + document.title);
 assert(!$('p-dialog').open, 'a status without ask opened Profiles');
}

async function checkCopyOnly(response, holder) {
 const assert = (ok, message) => {if (!ok) throw new Error(message);};
 profileDialogDouble();
 initialized = connected = true;
 load(response.settings);
 render(response);
 const named = holder ? 'The Garden window' : 'Another seestar-wormhole window';
 assert(!$('copy-only').hidden, 'copy-only notice hidden');
 assert($('copy-only-text').textContent === named + ' is serving the Seestar app' + (holder ? ' (Seestar S50 Pro TEST0002)' : '') + " on this computer's ports, so this window can't. Copy, Browse and Free up space work here as usual.", 'notice: ' + $('copy-only-text').textContent);
 assert($('copy-only-how').textContent === 'To use the Seestar app with this telescope instead: Stop the connection in ' + (holder ? 'the Garden window' : 'the other window') + ', then Stop and Connect here.', 'how: ' + $('copy-only-how').textContent);
 assert($('connection-text').textContent.endsWith(' through Telescope Network, for copying only') && $('connection-dot').getAttribute('data-tone') === 'on', 'header: ' + $('connection-text').textContent);
 assert($('run-summary').hidden && $('phone-steps').hidden && $('copy-smb').hidden && $('running-ports-box').hidden, 'serving help still shown');
 // Serving again, the help is back and the notice gone.
 render({...response, copy_only: undefined});
 assert($('copy-only').hidden && !$('run-summary').hidden && !$('phone-steps').hidden && !$('copy-smb').hidden && !$('running-ports-box').hidden, 'serving help not restored');
 assert(!$('connection-text').textContent.includes('copying only'), 'still copying only');
}
