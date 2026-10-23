const test = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const path = require('node:path');

function fixture(matches = true) {
  const handlers = new Map();
  const nodes = {};
  let mutation;
  const doc = { activeElement: null, getElementById: id => nodes[id], querySelector: () => null,
    addEventListener: (type, fn) => handlers.set(type, fn) };
  function node(id) {
    const attrs = new Map();
    const listeners = new Map();
    return nodes[id] = { id, hidden: false, inert: false, isConnected: true, attrs,
      classList: { toggle() {} },
      addEventListener: (type, fn) => listeners.set(type, fn),
      dispatch(type, event = {}) { listeners.get(type)?.({ target: this, ...event }); },
      setAttribute: (k, v) => attrs.set(k, v), removeAttribute: k => attrs.delete(k),
      focus() { doc.activeElement = this; },
      getClientRects: () => [{}], closest: () => null,
      contains(other) { return other === this; }, querySelectorAll: () => [] };
  }
  ['nav-drawer', 'nav-toggle', 'nav-close', 'nav-backdrop', 'main-content', 'detail-overlay', 'player-overlay', 'scrape-overlay'].forEach(node);
  ['detail-overlay', 'player-overlay', 'scrape-overlay'].forEach(id => nodes[id].hidden = true);
  const first = nodes['nav-close'];
  const last = node('last-link');
  const group = { hidden: false, querySelector: () => group.hasItem ? first : null, hasItem: true };
  const drawer = nodes['nav-drawer'];
  drawer.querySelectorAll = selector => selector === '.nav-group' ? [group] : [first, last];
  drawer.querySelector = () => last;
  drawer.contains = item => [drawer, first, last].includes(item);
  const media = { matches, addEventListener: (_, fn) => media.change = fn };
  doc.body = node('body');
  const window = { matchMedia: () => media, addEventListener: (type, fn) => handlers.set(type, fn) };
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, 'web/navigation.js'), 'utf8'), {
    document: doc, window, MutationObserver: class { constructor(fn) { mutation = fn; } observe() {} }
  });
  function key(key, shiftKey = false) {
    const e = { key, shiftKey, prevented: false, stopped: false,
      preventDefault() { this.prevented = true; }, stopImmediatePropagation() { this.stopped = true; } };
    handlers.get('keydown')(e); return e;
  }
  function open() { nodes['nav-toggle'].focus(); nodes['nav-toggle'].dispatch('click'); }
  return { nodes, doc, drawer, first, last, media, key, open, group, mutate: () => mutation(), handlers };
}

test('mobile drawer isolates main and restores opener after Escape', () => {
  const f = fixture();
  assert.equal(f.drawer.inert, true);
  f.open();
  assert.equal(f.nodes['nav-toggle'].attrs.get('aria-expanded'), 'true');
  assert.equal(f.nodes['main-content'].inert, true);
  assert.equal(f.doc.activeElement, f.first);
  const e = f.key('Escape');
  assert.equal(e.prevented, true); assert.equal(e.stopped, true);
  assert.equal(f.doc.activeElement, f.nodes['nav-toggle']);
  assert.equal(f.drawer.inert, true);
  assert.equal(f.nodes['main-content'].inert, false);
  assert.equal(f.nodes['nav-backdrop'].hidden, true);
});

test('Tab trap wraps both ways and backdrop/navigation close', () => {
  const f = fixture(); f.open();
  assert.equal(f.key('Tab', true).prevented, true); assert.equal(f.doc.activeElement, f.last);
  assert.equal(f.key('Tab').prevented, true); assert.equal(f.doc.activeElement, f.first);
  f.nodes['nav-backdrop'].dispatch('click'); assert.equal(f.drawer.inert, true);
  f.open(); f.drawer.dispatch('click', { target: { closest: () => f.first } });
  assert.equal(f.drawer.inert, true);
});

test('breakpoint clears modal state; desktop drawer stays accessible', () => {
  const f = fixture(); f.open(); f.media.matches = false; f.media.change();
  assert.equal(f.drawer.inert, false); assert.equal(f.drawer.attrs.has('aria-modal'), false);
  assert.equal(f.nodes['main-content'].inert, false);
  assert.equal(f.nodes['nav-toggle'].attrs.get('aria-expanded'), 'false');
  f.media.matches = true; f.media.change(); assert.equal(f.drawer.inert, true);
});

test('media overlay cannot compete with navigation; empty role groups hide', () => {
  const f = fixture(); f.open(); f.nodes['detail-overlay'].hidden = false;
  f.group.hasItem = false; f.mutate();
  assert.equal(f.drawer.inert, true); assert.equal(f.nodes['main-content'].inert, false);
  assert.equal(f.group.hidden, true);
  f.open(); assert.equal(f.nodes['nav-toggle'].attrs.get('aria-expanded'), 'false');
  const e = f.key('Escape'); assert.equal(e.stopped, false);
});
