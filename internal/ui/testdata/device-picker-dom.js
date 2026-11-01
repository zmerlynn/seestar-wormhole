// Minimal DOM for executing app.js's picker and diagnostic projection without
// a browser. HTML bindings are checked separately by the Go asset test.
class TestElement {
  constructor() {this.value = ''; this.hidden = true; this.children = []; this.attributes = {}; this.disabled = false; this.validity = {valid: true}; this.focusCount = 0;}
  set innerHTML(_) {throw new Error('HTML insertion is forbidden');}
  set outerHTML(_) {throw new Error('HTML insertion is forbidden');}
  insertAdjacentHTML() {throw new Error('HTML insertion is forbidden');}
  append(...children) {for (const child of children) {if (child.parentElement) child.parentElement.children = child.parentElement.children.filter(e => e !== child); child.parentElement = this; this.children.push(child);}}
  replaceChildren(...children) {this.children = children; this.value = '';}
  addEventListener(name, callback) {this.listeners ||= {}; (this.listeners[name] ||= []).push(callback);}
  dispatchEvent(event) {for (const callback of this.listeners?.[event.type] || []) callback(event); if (event.type === 'change' && this.onchange) this.onchange(event);}
  focus() {if (this === document.getElementById('device-choice') && document.getElementById('options').disabled) throw new Error('focus in disabled form'); this.focusCount++; document.activeElement = this;}
  removeAttribute(name) {delete this.attributes[name];}
  getAttribute(name) {return this.attributes[name] ?? null;}
  setAttribute(name, value) {this.attributes[name] = value;}
  querySelector() {return new TestElement();}
  closest() {return null;}
  createTHead() {return new TestElement();}
  createTBody() {return new TestElement();}
  insertRow() {return new TestElement();}
  insertCell() {return new TestElement();}
}
const testElements = new Map();
const document = {
  getElementById(id) {if (!testElements.has(id)) testElements.set(id, new TestElement()); return testElements.get(id);},
  createElement() {return new TestElement();},
  addEventListener() {}
};
const location = {hash: ''}, sessionStorage = {getItem() {return '';}};
const window = {}, history = {};
const fetch = () => new Promise(() => {});
function setTimeout() {}

const testFrames = [];
function requestAnimationFrame(callback) {testFrames.push(callback);}
