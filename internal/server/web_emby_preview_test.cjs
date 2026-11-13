'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const clientSource = fs.readFileSync(path.join(__dirname, 'web/emby.js'), 'utf8');
const previewSource = fs.readFileSync(path.join(__dirname, 'web/player.js'), 'utf8');
const flush = async () => { for (let i = 0; i < 12; i++) await Promise.resolve(); };
const deferred = () => { let resolve; const promise = new Promise(r => { resolve = r; }); return { promise, resolve }; };

function fixture() {
  class Element {
    constructor() { this.hidden = false; this.value = '0'; this.textContent = ''; this.style = {}; this.listeners = new Map(); this.children = new Map(); }
    setAttribute(name, value) { this[name] = value; }
    querySelector(selector) { if (!this.children.has(selector)) this.children.set(selector, new Element()); return this.children.get(selector); }
    addEventListener(event, fn) { this.listeners.set(event, fn); }
    removeEventListener(event, fn) { if (this.listeners.get(event) === fn) this.listeners.delete(event); }
    emit(event) { this.listeners.get(event)?.(); }
    remove() { this.removed = true; }
  }
  const requests = [], created = [], revoked = [], events = new Map();
  let storedIdentity = 'preview-owner', live = true, rail;
  const art = { duration: 60, seek: 0, on(name, fn) { events.set(name, fn); }, off(name, fn) { if (events.get(name) === fn) events.delete(name); } };
  const stage = { after(value) { rail = value; } };
  const context = vm.createContext({ AbortController, URLSearchParams, token: 'preview-owner',
    localStorage: { getItem: () => storedIdentity, removeItem() { storedIdentity = ''; } },
    window: { location: { replace() { throw new Error('unexpected login redirect'); } } },
    document: { createElement: () => new Element() },
    URL: { createObjectURL(blob) { const url = 'blob:fixture/' + created.length; created.push({ blob, url }); return url; }, revokeObjectURL(url) { revoked.push(url); } },
    fetch(url, options) { const pending = deferred(); requests.push({ url, options, respond: pending.resolve }); return pending.promise; }
  });
  vm.runInContext(clientSource + '\n' + previewSource, context);
  const dispose = context.mountEmbyPreview(art, 'item /1', stage, 'preview-owner', () => live);
  return { requests, created, revoked, events, art, dispose, context,
    rail: () => rail, el: selector => rail.querySelector(selector),
    identity(value) { storedIdentity = value; context.token = value; }, leavePage() { live = false; dispose(); } };
}
function response(body, status = 200) { return { ok: status >= 200 && status < 300, status, json: async () => body, blob: async () => body }; }
const thumbnails = [
  { PositionTicks: 125000000, ImageTag: 'tag &first', Url: 'https://untrusted.invalid/thumbnail?token=do-not-use' },
  { PositionTicks: 300000000, ImageTag: 'tag/second', Url: '/api/admin/movies/1/sprites/image?token=do-not-use' }
];
async function discover(f, values = thumbnails) { f.requests[0].respond(response({ Thumbnails: values })); await flush(); }
function select(f, index) { f.el('input').value = String(index); f.el('input').emit('input'); }

test('preview discovers standard ThumbnailSet and fetches Thumbnail by PositionTicks/tag with token header, ignoring supplied Url', async () => {
  const f = fixture();
  assert.equal(f.requests[0].url, '/emby/Items/item%20%2F1/ThumbnailSet?Width=320');
  assert.equal(f.requests[0].options.headers['X-Emby-Token'], 'preview-owner');
  await discover(f); assert.equal(f.rail().hidden, false); assert.equal(f.el('input').max, '1');
  select(f, 0); await flush();
  const image = f.requests[1], url = new URL(image.url, 'http://fixture');
  assert.equal(url.pathname, '/emby/Items/item%20%2F1/Images/Thumbnail');
  assert.equal(url.searchParams.get('Width'), '320'); assert.equal(url.searchParams.get('PositionTicks'), '125000000'); assert.equal(url.searchParams.get('tag'), 'tag &first');
  assert.equal(image.options.headers['X-Emby-Token'], 'preview-owner');
  assert.equal(url.searchParams.has('token'), false); assert.equal(url.searchParams.has('api_key'), false);
  assert(!image.url.includes('untrusted.invalid')); assert(!image.url.includes('do-not-use'));
  const blob = { syntheticImage: 1 }; image.respond(response(blob)); await flush();
  assert.equal(f.created.length, 1); assert.equal(f.created[0].blob, blob); assert.equal(f.el('img').src, 'blob:fixture/0'); assert.equal(f.el('img').hidden, false);
  assert.equal(f.el('output').textContent, '00:00:12'); assert.equal(f.el('input')['aria-valuetext'], '00:00:12');
  f.el('button').emit('click'); assert.equal(f.art.seek, 12.5);
  assert(f.requests.every(request => !request.options.method || request.options.method === 'GET'));
  f.dispose();
});

test('new selection aborts prior image, revokes replaced blob, and close aborts all streams/revokes final blob', async () => {
  const f = fixture(); await discover(f);
  select(f, 0); await flush(); f.requests[1].respond(response({ image: 1 })); await flush();
  select(f, 1); await flush(); assert(f.requests[1].options.signal.aborted);
  f.requests[2].respond(response({ image: 2 })); await flush();
  assert.deepEqual(f.revoked, ['blob:fixture/0']); assert.equal(f.el('img').src, 'blob:fixture/1');
  f.dispose();
  assert(f.requests[0].options.signal.aborted); assert(f.requests[2].options.signal.aborted);
  assert.deepEqual(f.revoked, ['blob:fixture/0', 'blob:fixture/1']); assert.equal(f.rail().removed, true); assert.equal(f.events.size, 0); assert.equal(f.el('input').listeners.size, 0);
});

test('page switch aborts image and a late response cannot create or render a blob', async () => {
  const f = fixture(); await discover(f); select(f, 0); await flush();
  f.leavePage(); assert(f.requests[1].options.signal.aborted);
  f.requests[1].respond(response({ late: true })); await flush();
  assert.equal(f.created.length, 0); assert.equal(f.el('img').src, undefined);
});

test('close before manifest resolves aborts discovery and never reveals empty controls', async () => {
  const f = fixture(); f.dispose(); assert(f.requests[0].options.signal.aborted);
  await discover(f); assert.equal(f.rail().removed, true); assert.equal(f.rail().hidden, true); assert.equal(f.requests.length, 1);
});

test('late manifest after identity change never opens rail or requests images', async () => {
  const f = fixture(); f.identity('someone-else'); await discover(f);
  assert.equal(f.rail().hidden, true); select(f, 0); await flush();
  assert.equal(f.requests.length, 1); assert.equal(f.created.length, 0); f.dispose();
});

test('identity change during manifest JSON decode prevents rendering even after response headers passed', async () => {
  const f = fixture(), body = deferred();
  f.requests[0].respond({ ok: true, status: 200, json: () => body.promise }); await flush();
  f.identity('someone-else'); body.resolve({ Thumbnails: thumbnails }); await flush();
  assert.equal(f.rail().hidden, true); assert.equal(f.requests.length, 1); f.dispose();
});

test('identity change while image blob is pending prevents object URL creation and render', async () => {
  const f = fixture(), body = deferred(); await discover(f); select(f, 0); await flush();
  f.requests[1].respond({ ok: true, blob: () => body.promise }); await flush();
  f.identity('someone-else'); body.resolve({ lateImage: true }); await flush();
  assert.equal(f.created.length, 0); assert.equal(f.el('img').src, undefined); f.dispose();
});

test('superseded image blob resolving last cannot replace the newest frame', async () => {
  const f = fixture(), body = deferred(); await discover(f); select(f, 0); await flush();
  f.requests[1].respond({ ok: true, blob: () => body.promise }); await flush();
  select(f, 1); await flush(); f.requests[2].respond(response({ current: true })); await flush();
  body.resolve({ stale: true }); await flush();
  assert.equal(f.created.length, 1); assert.deepEqual(f.created[0].blob, { current: true }); assert.equal(f.revoked.length, 0); f.dispose();
});

test('missing thumbnails and 404 keep preview hidden; unavailable manifest shows nonblocking notice', async () => {
  const empty = fixture(); await discover(empty, []);
  assert.equal(empty.rail().hidden, true); assert.equal(empty.requests.length, 1); empty.dispose();
  const absent = fixture(); absent.requests[0].respond(response({ error: 'not found' }, 404)); await flush();
  assert.equal(absent.rail().hidden, true); assert.equal(absent.requests.length, 1); absent.dispose();
  const failed = fixture(); failed.requests[0].respond(response({ error: 'unavailable' }, 500)); await flush();
  assert.equal(failed.rail().hidden, false); assert.equal(failed.el('input').disabled, true); assert.equal(failed.el('button').disabled, true);
  assert.equal(failed.el('p').hidden, false); assert.match(failed.el('p').textContent, /暂不可用，不影响播放/); failed.dispose();
});

test('hover timeline chooses standard ticks, clamps position and ignores mouseleave', async () => {
  const f = fixture(); await discover(f); const hover = f.events.get('setBar');
  hover('hover', 0.6, { type: 'mousemove' }); await flush();
  assert.equal(new URL(f.requests[1].url, 'http://fixture').searchParams.get('PositionTicks'), '300000000');
  hover('hover', 0.8, { type: 'mouseleave' }); await flush(); assert.equal(f.requests.length, 2);
  hover('hover', -1, { type: 'mousemove' }); await flush();
  assert.equal(new URL(f.requests[2].url, 'http://fixture').searchParams.get('PositionTicks'), '125000000'); f.dispose();
});
