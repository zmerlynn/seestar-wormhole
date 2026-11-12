// The Services and added ports tables, against the production markup.
const portsAssert = (ok, message) => {if (!ok) throw new Error(message);};

function portsFixture(ports = [], os = 'linux') {
  initialized = connected = true;
  pending = '';
  hostOS = os;
  portDefaults = [{local: 4700, device: 4700}, {local: 4445, device: 445}, {local: 8080, device: 80}, {local: 4350, device: 4350}, {local: 4554, device: 4554}, {local: 4555, device: 4555}, {local: 4801, device: 4801}, {local: 4800, device: 4800}, {local: 4804, device: 4804}, {local: 4361, device: 4361}, {local: 32323, device: 32323}];
  load({version: 1, bind: '127.0.0.1', discovery: false, idle_minutes: 0, ports, email: 'synthetic@example.invalid'});
}

// The Services rows are in Seestar port order, in the editable table and
// the running one.
async function checkPortOrder() {
  portsFixture();
  const order = '80,445,4350,4361,4554,4555,4700,4800,4801,4804,32323';
  const body = $('ports').children[0].children[1];
  portsAssert(body.children.map(row => row.children[1].textContent).join() === order, 'Services order: ' + body.children.map(row => row.children[1].textContent).join());
  portsAssert(settings().ports.map(p => p.device).join() === order, 'sent order');
  portsTable('running-ports', {80: 8080, 445: 4445, 7000: 7001});
  const running = $('running-ports').children[0].children[1];
  portsAssert(running.children.map(row => row.children[1].textContent).join() === order + ',7000', 'running order');
}

const portsEdit = (input, value) => {
  input.value = value;
  input.dispatchEvent({type: 'input'});
};
// The fake DOM finds no element by id it did not make, so a Services input
// is found by its row.
const servicePort = remote => serviceRows.find(r => r.device() === remote).local;
const shaded = remote => serviceRows.find(r => r.device() === remote).row.className === 'remapped';
const portMessage = input => input.portError.hidden ? '' : input.portError.textContent;

// A row is shaded while its local port is set and differs from its Seestar
// port, and the shading follows edits.
async function checkPortShading() {
  portsFixture([{device: 4361, local: 0}]);
  portsAssert(shaded(445) && shaded(80), 'remapped rows shaded');
  portsAssert(!shaded(4700), 'same port not shaded');
  portsAssert(!shaded(4361), 'off row not shaded');
  portsEdit(servicePort(4700), '4701');
  portsAssert(shaded(4700), 'edit shades');
  portsEdit(servicePort(80), '');
  portsAssert(!shaded(80), 'turning off unshades');
  portsEdit(servicePort(445), '445');
  portsAssert(!shaded(445), 'same port unshades');
  const remote = addPortRow();
  portsEdit(remote, '7000');
  const entry = addedRows()[0];
  portsAssert(entry.row.className === '', 'added follows, not shaded');
  portsEdit(entry.local, '7001');
  portsAssert(entry.row.className === 'remapped', 'added remapped shaded');
}

// Added rows put the Local port first, as Services does; Add a port focuses
// the Seestar port, and typing it fills an untouched Local port.
async function checkAddedColumns() {
  portsFixture();
  $('add-port').dispatchEvent({type: 'click'});
  const {row, remote, local} = addedRows()[0];
  const heads = $('added-ports').children[0].children[0].children[0].children.map(th => th.textContent);
  portsAssert(heads.join() === 'Local port,Seestar port,', 'headings: ' + heads.join());
  portsAssert(row.children[0].children[0] === local && row.children[1].children[0] === remote && row.children[2].children[0].textContent === 'Remove', 'column order');
  portsAssert(document.activeElement === remote, 'focus in Seestar port');
  portsEdit(remote, '7000');
  portsAssert(local.value === '7000', 'local follows');
}

// Each port field shows its own error as the ports are edited.
async function checkPortRowErrors() {
  portsFixture();
  const remote = addPortRow();
  const {local} = addedRows()[0];
  portsAssert(!portMessage(remote) && !portMessage(local), 'new row starts clean');
  portsEdit(remote, '445');
  portsAssert(portMessage(remote) === 'Seestar port 445 is the File sharing for other apps (SMB) row above. Set its local port there.', 'services port: ' + portMessage(remote));
  portsAssert(remote.getAttribute('aria-invalid') === 'true' && remote.getAttribute('aria-describedby').split(' ').includes(remote.portError.id), 'described');
  portsEdit(remote, '32323');
  portsAssert(portMessage(remote).includes('Alpaca'), 'names Alpaca');
  portsEdit(remote, '7000');
  portsAssert(!portMessage(remote) && remote.getAttribute('aria-invalid') === null && !remote.getAttribute('aria-describedby').includes('port-error'), 'cleared');
  portsEdit(local, '');
  portsAssert(portMessage(local) === 'Enter a local port from 1 to 65535.', 'required: ' + portMessage(local));
  portsEdit(local, '4445');
  portsAssert(portMessage(local) === 'Local port 4445 is already used by File sharing for other apps (SMB).', 'duplicate: ' + portMessage(local));
  portsAssert(!portMessage(servicePort(445)), 'the Services row keeps no error');
  portsEdit(servicePort(445), '');
  portsAssert(!portMessage(local), 'freed');
  portsEdit(servicePort(80), '4700');
  portsAssert(portMessage(servicePort(80)) === 'Local port 4700 is already used by Seestar control.' && !portMessage(servicePort(4700)), 'the edited row duplicates');
  portsEdit(servicePort(80), '8080');
  // Connect checks an untouched empty row and focuses its first error.
  $('password').value = 'synthetic-password';
  addPortRow();
  const second = addedRows()[1];
  portsAssert(!portMessage(second.remote), 'untouched');
  portsAssert(!validate(), 'refused');
  portsAssert(portMessage(second.remote) === 'Enter a Seestar port from 1 to 65535.' && portMessage(second.local) === 'Enter a local port from 1 to 65535.', 'Connect checks all');
  portsAssert(document.activeElement === second.local && actionError === 'ports', 'focus first error, in column order');
}

// A Local port that follows its Seestar port claims the number as it is
// typed, so the duplicate it makes is its own error, not an earlier row's.
async function checkFollowedPortDuplicate() {
  portsFixture();
  $('password').value = 'synthetic-password';
  portsEdit(servicePort(445), '5000');
  const remote = addPortRow(), first = addedRows()[0];
  portsEdit(remote, '5000');
  portsAssert(first.local.value === '5000' && portMessage(first.local) === 'Local port 5000 is already used by File sharing for other apps (SMB).', 'followed duplicate: ' + portMessage(first.local));
  portsAssert(!portMessage(servicePort(445)), 'the Services row keeps no error');
  portsAssert(!validate() && document.activeElement === first.local, 'Connect focuses the followed duplicate');
  // The same between added rows.
  portsEdit(first.local, '6000');
  const second = addPortRow();
  portsEdit(second, '6000');
  const {local} = addedRows()[1];
  portsAssert(portMessage(local) === 'Local port 6000 is already used by added port 1.' && !portMessage(first.local), 'added duplicate: ' + portMessage(first.local));
  portsAssert(!validate() && document.activeElement === local, 'Connect focuses the added duplicate');
}

// A refusal naming a port shows on that port's row too, until edited.
async function checkPortRefusal() {
  portsFixture([{device: 7000, local: 7000}]);
  fail({code: 'port_conflict', field: 'ports', service: 4700, local: 4700, message: 'Another program is using local port 4700.'});
  portsAssert(portMessage(servicePort(4700)) === 'Another program is using local port 4700.', 'services row: ' + portMessage(servicePort(4700)));
  portsAssert(!$('ports-error').hidden, 'still under the table');
  const {local} = addedRows()[0];
  fail({code: 'port_conflict', field: 'ports', service: 7000, local: 7000, message: 'Another program is using local port 7000.'});
  portsAssert(portMessage(local) === 'Another program is using local port 7000.' && !portMessage(servicePort(4700)), 'added row');
  portsEdit(local, '7001');
  portsAssert(!portMessage(local), 'edit clears');
}

// A local port below 1024 shows the permissions guide by its row while
// edited, in both tables, only on Linux.
async function checkLowPortHelp() {
  for (const os of ['linux', 'darwin', 'windows']) {
    portsFixture([], os);
    const linux = os === 'linux';
    const shown = input => !input.portLow.hidden && input.getAttribute('aria-describedby').split(' ').includes(input.portLow.id);
    portsEdit(servicePort(80), '80');
    portsAssert(shown(servicePort(80)) === linux, os + ': port 80');
    portsAssert(!shown(servicePort(445)), os + ': 4445');
    portsEdit(servicePort(80), '8080');
    portsAssert(!shown(servicePort(80)), os + ': 8080');
    const remote = addPortRow(), {local} = addedRows()[0];
    portsEdit(remote, '800');
    portsAssert(shown(local) === linux && !remote.portLow, os + ': added 800');
    portsEdit(local, '8000');
    portsAssert(!shown(local), os + ': added 8000');
    portsEdit(local, '0');
    portsAssert(!shown(local), os + ': an invalid port shows its error alone');
    portsAssert($('port-permissions').hidden, os + ': the guide under the table');
  }
}
