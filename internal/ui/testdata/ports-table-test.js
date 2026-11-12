// The Services and added ports tables, against the production markup.
const portsAssert = (ok, message) => {if (!ok) throw new Error(message);};

function portsFixture(ports = [], os = 'linux') {
  initialized = connected = true;
  pending = '';
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
