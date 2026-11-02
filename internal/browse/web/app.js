// The browse page: lists one folder of the share at a time. Every name is set
// with textContent and every URL built with encodeURIComponent.
const $ = (id) => document.getElementById(id);
const statusLine = $('status');
const table = $('list');
const rows = table.tBodies[0];
const crumbs = $('crumbs');
const viewer = $('viewer');
const viewed = viewer.querySelector('img');
const viewedText = $('vtext');
const viewNote = $('vnote');

const textLimit = 1 << 20; // the bytes of a .txt shown
const fitsLimit = 10 * 2880; // ten FITS header blocks

let generation = 0;
let loading = null;
let viewing = null; // the AbortController of the text being read

const pageURL = (p) => (p ? '/?path=' + encodeURIComponent(p) : '/');
const fileURL = (p, save) => '/file?path=' + encodeURIComponent(p) + (save ? '&download=1' : '');
const baseName = (p) => p.slice(p.lastIndexOf('/') + 1);
const plainClick = (ev) => ev.button === 0 && !ev.ctrlKey && !ev.metaKey && !ev.shiftKey && !ev.altKey;
const count = (n, word) => n + ' ' + word + (n === 1 ? '' : 's');
// withHint ends a message as a sentence and adds how to retry.
const withHint = (msg, hint) => msg.replace(/[^.!?]$/, '$&.') + ' ' + hint;
const listHint = 'Select Share or a parent folder above, or reload this page to retry.';
const viewHint = 'Close this preview and reopen the file to retry.';

function el(tag, text, cls) {
  const e = document.createElement(tag);
  if (text !== undefined) e.textContent = text;
  if (cls) e.className = cls;
  return e;
}

function humanSize(n) {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let v = n;
  let i = 0;
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000;
    i++;
  }
  return (i ? v.toFixed(1) : String(v)) + ' ' + units[i];
}

function folderLink(p, text) {
  const a = el('a', text);
  a.href = pageURL(p);
  a.addEventListener('click', (ev) => {
    if (!plainClick(ev)) return;
    ev.preventDefault();
    history.pushState(null, '', pageURL(p));
    load(p, true);
  });
  return a;
}

function showCrumbs(p) {
  crumbs.replaceChildren(folderLink('', 'Share'));
  if (p) {
    const parts = p.split('/');
    parts.forEach((part, i) => crumbs.append(' / ', folderLink(parts.slice(0, i + 1).join('/'), part)));
  }
  crumbs.lastChild.setAttribute('aria-current', 'page');
}

// fitsCards splits the start of a FITS file, one character per byte, into
// its 80-character header cards up to and including END, dropping blank ones.
// end tells whether END was found.
function fitsCards(text) {
  const cards = [];
  for (let i = 0; i + 80 <= text.length; i += 80) {
    const card = text.slice(i, i + 80).trimEnd();
    if (card) cards.push(card);
    if (card === 'END') return { cards, end: true };
  }
  return { cards, end: false };
}

// showText reads the start of a file, up to limit bytes, and shows it as
// text: a .txt as UTF-8, a FITS file as its header cards.
async function showText(e, kind, signal) {
  const limit = kind === 'fits' ? fitsLimit : textLimit;
  viewNote.textContent = 'Loading…';
  try {
    // The range is asked for whatever size the listing gave, which may be
    // out of date. An empty file is answered 200 with no body.
    const res = await fetch(fileURL(e.path), { headers: { Range: 'bytes=0-' + (limit - 1) }, signal });
    if (res.status !== 200 && res.status !== 206) {
      throw new Error((await res.text()).trim() || 'HTTP ' + res.status);
    }
    const all = new Uint8Array(await res.arrayBuffer());
    const bytes = all.subarray(0, limit);
    const total = Number((res.headers.get('Content-Range') || '').split('/')[1]) || all.length;
    if (kind === 'fits') {
      // latin1 decodes one byte to one character, so cards stay 80 bytes.
      const { cards, end } = fitsCards(new TextDecoder('latin1').decode(bytes));
      viewedText.textContent = cards.join('\n');
      viewNote.textContent = end ? '' : 'No END card in the first ' + bytes.length + ' bytes; showing what was read.';
    } else {
      const more = total > limit;
      // stream leaves out a character cut in two at the limit.
      viewedText.textContent = new TextDecoder().decode(bytes, { stream: more });
      viewNote.textContent = more ? 'First 1 MiB shown; save to see all.' : '';
    }
  } catch (err) {
    if (signal.aborted) return;
    viewNote.textContent = withHint('Could not read this file: ' + err.message, viewHint);
  }
}

// showImage shows a .jpg, with a note while it loads or if it fails. The
// listeners go with signal, so a closed or replaced viewer ignores them.
function showImage(e, name, signal) {
  viewNote.textContent = 'Loading image…';
  viewed.alt = 'Image preview of ' + name;
  viewed.addEventListener('load', () => { viewNote.textContent = ''; }, { signal });
  viewed.addEventListener('error', () => {
    viewed.hidden = true;
    viewNote.textContent = 'Could not load this image. Close this preview and reopen the file to retry.';
  }, { signal });
  viewed.src = fileURL(e.path);
}

function view(e, name) {
  if (viewing) viewing.abort();
  viewing = new AbortController();
  $('vname').textContent = name;
  $('vsave').href = fileURL(e.path, true);
  viewed.hidden = e.view !== 'image';
  viewedText.hidden = e.view === 'image';
  viewedText.textContent = '';
  viewNote.textContent = '';
  if (e.view === 'image') showImage(e, name, viewing.signal);
  else showText(e, e.view, viewing.signal);
  viewer.showModal();
}

function row(e) {
  const name = baseName(e.path);
  const file = e.type === 'file';
  const thumb = el('td', undefined, 'thumb');
  if (e.thumb) {
    const img = el('img');
    img.alt = '';
    img.loading = 'lazy';
    img.decoding = 'async';
    img.addEventListener('error', () => { img.hidden = true; });
    img.src = fileURL(e.thumb);
    thumb.append(img);
  }
  const nameCell = el('td', undefined, 'name');
  if (!file) {
    nameCell.append(folderLink(e.path, name + '/'));
  } else if (e.view) {
    const a = el('a', name);
    a.href = fileURL(e.path);
    a.addEventListener('click', (ev) => {
      if (!plainClick(ev)) return;
      ev.preventDefault();
      view(e, name);
    });
    nameCell.append(a);
    // The preview opens the viewer too, for the mouse only: the name is
    // the keyboard's way in.
    const img = thumb.firstChild;
    if (img) {
      img.className = 'open';
      img.addEventListener('click', () => view(e, name));
    }
  } else {
    nameCell.textContent = name;
  }
  const size = el('td', file ? humanSize(e.size) : '', 'num');
  if (file) size.title = e.size + ' bytes';
  const modified = el('td', new Date(e.mtime).toLocaleString());
  modified.title = e.mtime;
  const status = el('td', undefined, 'st');
  if (e.status) status.append(el('span', e.status, 'badge ' + e.status));
  const save = el('td');
  if (file) {
    const a = el('a', 'Save');
    a.href = fileURL(e.path, true);
    a.download = '';
    a.setAttribute('aria-label', 'Save ' + name);
    save.append(a);
  }
  const tr = el('tr');
  tr.append(thumb, nameCell, size, modified, status, save);
  return tr;
}

function render(data) {
  $('endpoint').textContent = data.endpoint;
  table.classList.toggle('statuses', data.statuses);
  const dirs = data.entries.filter((e) => e.type === 'dir');
  const files = data.entries.filter((e) => e.type === 'file');
  rows.replaceChildren(...dirs.map(row), ...files.map(row));
  table.hidden = rows.children.length === 0;
  statusLine.textContent = table.hidden ? 'Empty folder' : count(dirs.length, 'folder') + ', ' + count(files.length, 'file') + '.';
}

// load shows folder p. focus, for a navigation the user asked for, moves
// focus to the folder's own breadcrumb, since the link they used is gone.
async function load(p, focus) {
  const mine = ++generation;
  if (loading) loading.abort();
  loading = new AbortController();
  showCrumbs(p);
  if (focus) crumbs.lastChild.focus();
  statusLine.textContent = 'Loading…';
  table.hidden = true;
  rows.replaceChildren();
  let data;
  try {
    const res = await fetch('/api/list?path=' + encodeURIComponent(p), { signal: loading.signal });
    data = await res.json();
    if (mine !== generation) return;
    if (!res.ok) {
      statusLine.textContent = withHint(data.message, listHint);
      return;
    }
  } catch (err) {
    if (mine !== generation) return;
    statusLine.textContent = withHint('Could not load this folder: ' + err.message, listHint);
    return;
  }
  render(data);
}

const current = () => new URLSearchParams(location.search).get('path') || '';

$('vclose').addEventListener('click', () => viewer.close());
// A click on the backdrop targets the dialog itself, whose content all sits
// in #vbody. Pressing inside and releasing outside, as when selecting text,
// does not count.
let pressedBackdrop = false;
viewer.addEventListener('pointerdown', (ev) => { pressedBackdrop = ev.target === viewer; });
viewer.addEventListener('click', (ev) => {
  if (pressedBackdrop && ev.target === viewer) viewer.close();
});
viewer.addEventListener('close', () => {
  if (viewing) viewing.abort();
  viewed.removeAttribute('src');
  viewed.alt = '';
  viewedText.textContent = '';
});
window.addEventListener('popstate', () => load(current(), true));
load(current());
