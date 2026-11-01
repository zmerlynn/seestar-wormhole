// Run after device-picker-dom.js and the complete embedded app.js in JavaScriptCore.
function assertPicker(value, message) {if (!value) throw new Error(message);}
initialized = true;
connected = true;
$('listen').value = '127.0.0.1';
const wireModel = ' <img src=x onerror=alert(1)>\t ';
const choices = [
  {serial: 'synthetic-choice-1234', model: 'Seestar S50 Pro', label: 'Seestar S50 Pro synthetic-choice-1234'},
  {serial: ' synthetic-choice-5678 ', model: wireModel, label: '" <img src=x onerror=alert(1)>\\t " synthetic-choice-5678'}
];
const pickerStatus = {
  active: false, version: 'synthetic', status: 'Not connected', settings: {email: ''},
  effective: {listen: 'this_computer', idle: {mode: 'keep_connected'}, ports: {}},
  password: {}, events: [], failure: {code: 'selection_multiple', devices: choices},
  selected_device: 'Seestar S50 Pro ***1234'
};
render(pickerStatus);
assertPicker(!$('device-picker').hidden, 'multiple failure hides picker');
assertPicker($('device-choice').children.length === 3, 'picker omits choices or prompt');
assertPicker($('device-choice').children[0].textContent === 'Choose a telescope…', 'prompt wording');
assertPicker($('device-choice').children[1].textContent === choices[0].label && $('device-choice').children[2].textContent === choices[1].label, 'picker does not use escaped text labels');
$('device-choice').value = '1';
$('device-choice').onchange();
assertPicker($('serial').value === choices[1].serial, 'picker lost exact serial');
assertPicker($('model').value === wireModel, 'picker lost exact wire model');
assertPicker(settings().serial === choices[1].serial && settings().model === wireModel, 'submission changes wire pair');
assertPicker($('advanced').open, 'picker did not open Advanced');
assertPicker($('feedback').textContent === 'Telescope selection filled in Advanced. Choose Connect to try again.', 'picker recovery wording');
render(pickerStatus);
assertPicker($('device-choice').value === '1', 'poll reset picker selection');
assertPicker($('selected-device').textContent === 'Telescope: Seestar S50 Pro ***1234', 'selected telescope wording');
assertPicker(!diagnostic.includes('synthetic-choice-') && !diagnostic.includes('<img') && !diagnostic.includes('devices'), 'full choice identity in copied summary');
render({...pickerStatus, active: true});
assertPicker($('device-picker').hidden && $('device-choice').children.length === 1, 'active run retained choices');
render({...pickerStatus, failure: {code: 'selection'}});
assertPicker($('device-picker').hidden, 'unrelated failure exposes picker');
render({...pickerStatus, failure: null});
assertPicker($('device-picker').hidden, 'successful status exposes picker');
