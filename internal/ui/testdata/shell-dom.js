// Minimal DOM for executing shell.js without a browser. Elements keep
// their children, classes, data and listeners; a frame's document is the
// header of the page testPages names for its path.
class TestElement {
  constructor(tag = 'div') {this.tagName = tag.toUpperCase(); this.value = ''; this.hidden = false; this.children = []; this.attributes = {}; this.dataset = {}; this.className = ''; this.textContent = ''; this.title = ''; this.tabIndex = 0; this.disabled = false; this.parentElement = null;}
  set innerHTML(_) {throw new Error('HTML insertion is forbidden');}
  get classList() {
    const e = this, list = () => e.className.split(' ').filter(Boolean);
    return {contains: c => list().includes(c), toggle(c, on) {const l = list().filter(x => x !== c); if (on) l.push(c); e.className = l.join(' ');}};
  }
  get lastChild() {return this.children.at(-1) || null;}
  get isConnected() {for (let e = this; e; e = e.parentElement) if (e.root) return true; return false;}
  append(...children) {for (const child of children) this.insertBefore(child, null);}
  insertBefore(child, before) {
    if (child.parentElement) child.parentElement.children = child.parentElement.children.filter(e => e !== child);
    child.parentElement = this;
    const i = before ? this.children.indexOf(before) : -1;
    if (i < 0) this.children.push(child); else this.children.splice(i, 0, child);
  }
  contains(e) {for (; e; e = e.parentElement) if (e === this) return true; return false;}
  remove() {if (this.parentElement) this.parentElement.children = this.parentElement.children.filter(e => e !== this); this.parentElement = null;}
  replaceChildren(...children) {for (const c of this.children) c.parentElement = null; this.children = []; this.append(...children);}
  addEventListener(name, callback) {this.listeners ||= {}; (this.listeners[name] ||= []).push(callback);}
  dispatchEvent(event) {for (const callback of this.listeners?.[event.type] || []) callback(event);}
  focus() {document.activeElement = this;}
  getAttribute(name) {return this.attributes[name] ?? null;}
  setAttribute(name, value) {this.attributes[name] = String(value);}
  matches(selector) {
    if (selector.startsWith('.')) return this.classList.contains(selector.slice(1));
    const role = /^\[role=(\w+)\]$/.exec(selector);
    if (role) return this.attributes.role === role[1];
    return this.tagName === selector.toUpperCase();
  }
  querySelectorAll(selector) {const found = []; const walk = e => {for (const c of e.children) {if (c.matches(selector)) found.push(c); walk(c);}}; walk(this); return found;}
  querySelector(selector) {return this.querySelectorAll(selector)[0] || null;}
  // A dialog opens and closes as a browser's does, and closing fires close.
  showModal() {this.open = true;}
  close() {if (!this.open) return; this.open = false; this.dispatchEvent({type: 'close'});}
  // A frame's page: its header's dot, words and Copy chip, and its window.
  get contentDocument() {
    const page = testPages[this.dataset.path];
    if (!page) return null;
    const ids = {'connection-dot': {getAttribute: () => page.tone}, 'connection-text': {textContent: page.words}, 'copy-chip': page.chip};
    return {getElementById: id => ids[id] || null};
  }
  // Focusing it focuses the frame, as a browser's shell sees it.
  get contentWindow() {
    const frame = this;
    return (testWindows[this.dataset.path] ||= Object.assign(new TestElement(), {focus() {document.activeElement = frame; testFocused.push(frame.dataset.path);}}));
  }
}
const testPages = {}, testWindows = {}, testFocused = [];
const testElements = new Map();
const testBody = new TestElement('body');
testBody.root = true;
const document = {
  activeElement: testBody, body: testBody,
  getElementById(id) {if (!testElements.has(id)) testElements.set(id, new TestElement()); return testElements.get(id);},
  createElement(tag) {return new TestElement(tag);},
  addEventListener(name, callback) {testBody.addEventListener(name, callback);}
};
const location = {hash: '#synthetic-token', pathname: '/'}, sessionStorage = {getItem() {return '';}, setItem() {}};
const history = {replaceState() {}};
const window = {addEventListener(name, callback) {testBody.addEventListener('window-' + name, callback);}};
window.top = window;
// testFetch answers the shell's API; the test replaces it.
let testFetch = () => new Promise(() => {});
function fetch(url, options) {return testFetch(url, options);}
function setTimeout() {}
