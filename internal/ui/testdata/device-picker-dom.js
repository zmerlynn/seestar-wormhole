// Minimal DOM for executing app.js's picker and diagnostic projection without
// a browser. HTML bindings are checked separately by the Go asset test.
class TestElement {
  constructor() {this.value = ''; this.hidden = true; this.children = []; this.attributes = {}; this.disabled = false; this.validity = {valid: true};}
  set innerHTML(_) {throw new Error('HTML insertion is forbidden');}
  set outerHTML(_) {throw new Error('HTML insertion is forbidden');}
  insertAdjacentHTML() {throw new Error('HTML insertion is forbidden');}
  append(...children) {this.children.push(...children);}
  replaceChildren(...children) {this.children = children; this.value = '';}
  addEventListener() {}
  removeAttribute(name) {delete this.attributes[name];}
  setAttribute(name, value) {this.attributes[name] = value;}
  querySelector() {return new TestElement();}
  closest() {return new TestElement();}
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
