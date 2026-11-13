'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync(require('node:path').join(__dirname, 'web/log-stream.js'), 'utf8');

function fixture() {
  class Target {
    constructor() { this.listeners = new Map(); this.textContent = ''; this.value = 'app'; this.scrollHeight = 100; this.scrollTop = 0; this.clientHeight = 100; }
    addEventListener(name, fn) { this.listeners.set(name, fn); }
    removeEventListener(name, fn) { if (this.listeners.get(name) === fn) this.listeners.delete(name); }
    emit(name) { this.listeners.get(name)?.(); }
    setAttribute(name, value) { this[name] = value; }
  }
  const doc = new Target(), win = new Target(), elements = new Map();
  doc.hidden = false;
  const host = { ownerDocument: doc, innerHTML: '', querySelector(name) { if (!elements.has(name)) elements.set(name, new Target()); return elements.get(name); } };
  let identity = 'owner', authorized = 0, responseOverride = null;
  const calls = [], timers = new Map(); let nextTimer = 1;
  const context = vm.createContext({ TextDecoder, AbortController, module: { exports: {} }, localStorage: { getItem: () => identity },
    addEventListener: win.addEventListener.bind(win), removeEventListener: win.removeEventListener.bind(win),
    setTimeout(fn) { const id = nextTimer++; timers.set(id, fn); return id; }, clearTimeout(id) { timers.delete(id); },
    fetch: async (url, options) => {
      let pending, ended = false; const queue = [];
      const reader = {
        read() { if (queue.length) return Promise.resolve(queue.shift()); if (ended) return Promise.resolve({ done: true }); return new Promise(resolve => { pending = resolve; }); },
        cancel() { ended = true; pending?.({ done: true }); pending = null; return Promise.resolve(); }
      };
      const call = { url, options, reader,
        push(text) { const data = { done: false, value: new TextEncoder().encode(text) }; if (pending) { const resolve = pending; pending = null; resolve(data); } else queue.push(data); },
        end() { ended = true; pending?.({ done: true }); pending = null; },
        status: 200,
        pushBytes(value) { const data = { done: false, value }; if (pending) { const resolve = pending; pending = null; resolve(data); } else queue.push(data); }
      };
      calls.push(call);
      options.signal.addEventListener('abort', () => reader.cancel());
      return responseOverride || { ok: true, status: 200, headers: { get: () => 'text/event-stream; charset=utf-8' }, body: { getReader: () => reader } };
    }
  });
  vm.runInContext(source, context);
  const stop = () => context.mountLogStream(host, { token: 'owner', current: () => true, onUnauthorized: () => authorized++ });
  return { context, doc, win, calls, timers, host, response(value) { responseOverride = value; }, el: name => host.querySelector('#dl-' + name), mount: stop, identity(value) { identity = value; }, authorized: () => authorized,
    retry() { const [id, fn] = timers.entries().next().value; timers.delete(id); fn(); } };
}
const flush = async () => { for (let i = 0; i < 12; i++) await Promise.resolve(); };
const frame = (text, extra = {}) => 'event: logs\ndata: ' + JSON.stringify({ kind: 'app', text, cursor: 'cursor/1', reset: false, gap: false, has_more: false, ...extra }) + '\n\n';

test('SSE parser accepts fragmented CRLF, comments and multiline data without joining frames', () => {
  const f = fixture(), values = [];
  const parse = f.context.module.exports.createSSEParser((event, data) => values.push([event, data]));
  for (const chunk of [': heartbeat\r', '\nevent: logs\r\ndata: first\r', '\ndata: second\r\n\r', '\nevent: error\ndata: {}\n\n']) parse(chunk);
  assert.deepEqual(values, [['logs', 'first\nsecond'], ['error', '{}']]);
});

test('fetch SSE authenticates with header and displays exact raw multiline text safely', async () => {
  const f = fixture(), dispose = f.mount(); await flush();
  assert.equal(f.calls[0].options.headers['X-Emby-Token'], 'owner');
  assert.equal(f.calls[0].options.method, undefined);
  assert(!f.calls[0].url.includes('owner'));
  const original = '<img src=x onerror=alert(1)>\n\n{"level":"info"}\n  continuation\n中文🙂';
  const data = new TextEncoder().encode(frame(original));
  for (let i = 0; i < data.length; i += 7) { f.calls[0].pushBytes(data.slice(i, i + 7)); await flush(); }
  assert.equal(f.el('body').textContent, original);
  assert.equal(f.el('body').innerHTML, undefined);
  assert(!f.host.innerHTML.includes('aria-live'));
  dispose();
});

test('disconnect resumes using cursor; reset clears while gap alone preserves exact fragments', async () => {
  const f = fixture(), dispose = f.mount(); await flush();
  f.calls[0].push(frame('first\n')); await flush();
  f.calls[0].end(); await flush(); assert.equal(f.timers.size, 1);
  f.retry(); await flush(); assert(f.calls[1].url.includes('after=cursor%2F1'));
  f.calls[1].push(frame('second', { gap: true })); await flush();
  assert.equal(f.el('body').textContent, 'first\nsecond');
  assert.match(f.el('gap').textContent, /较早内容/);
  f.calls[1].push(frame('tail', { reset: true, gap: true })); await flush();
  assert.equal(f.el('body').textContent, 'tail');
  assert.match(f.el('gap').textContent, /缺口/);
  dispose(); assert.equal(f.timers.size, 0);
});

test('switching files aborts old stream, ignores stale data and starts without previous cursor', async () => {
  const f = fixture(), dispose = f.mount(); await flush();
  f.calls[0].push(frame('old')); await flush();
  f.el('kind').value = 'request'; f.el('kind').emit('change'); await flush();
  assert(f.calls[0].options.signal.aborted);
  assert.equal(f.calls[1].url, '/api/admin/logs/stream?kind=request');
  assert.equal(f.el('body').textContent, '');
  f.calls[1].push(frame('raw request', { kind: 'request' })); await flush();
  assert.equal(f.el('body').textContent, 'raw request');
  assert.match(f.el('source').textContent, /request-YYYY-MM-DD/);
  dispose(); assert(f.calls[1].options.signal.aborted);
  f.el('pause').emit('click'); f.doc.emit('visibilitychange'); await flush(); assert.equal(f.calls.length, 2);
});

test('pause and hidden page close stream, resume with cursor, never issue a write', async () => {
  const f = fixture(), dispose = f.mount(); await flush();
  f.calls[0].push(frame('raw')); await flush();
  f.el('pause').emit('click'); await flush(); assert(f.calls[0].options.signal.aborted);
  f.el('refresh').emit('click'); await flush(); assert.equal(f.calls.length, 1);
  f.el('pause').emit('click'); await flush(); assert.equal(f.calls.length, 2);
  f.doc.hidden = true; f.doc.emit('visibilitychange'); await flush(); assert(f.calls[1].options.signal.aborted); assert.equal(f.timers.size, 0);
  f.doc.hidden = false; f.doc.emit('visibilitychange'); await flush();
  assert(f.calls[2].url.includes('after=cursor%2F1'));
  assert(f.calls.every(call => !call.options.method || call.options.method === 'GET'));
  dispose(); assert.equal(f.doc.listeners.size, 0); assert.equal(f.win.listeners.size, 0);
});

test('identity changes clear sensitive text and prevent every reconnect path', async () => {
  const f = fixture(), dispose = f.mount(); await flush();
  f.calls[0].push(frame('private')); await flush();
  f.identity('different'); f.win.emit('storage'); await flush();
  assert(f.calls[0].options.signal.aborted); assert.equal(f.el('body').textContent, '');
  f.el('refresh').emit('click'); f.el('pause').emit('click'); f.el('pause').emit('click'); f.doc.emit('visibilitychange'); await flush();
  assert.equal(f.calls.length, 1); assert.equal(f.timers.size, 0); dispose();
});

test('invalid cursor clears old text and reconnects at tail; HTTP auth denial stops retries', async () => {
  const f = fixture(), dispose = f.mount(); await flush();
  f.calls[0].push(frame('old private text')); await flush();
  f.response({ ok: false, status: 400, json: async () => ({ code: 'LOGS_CURSOR_INVALID' }) });
  f.calls[0].end(); await flush(); f.retry(); await flush();
  assert.equal(f.el('body').textContent, ''); assert.equal(f.timers.size, 1);
  f.response({ ok: false, status: 403 }); f.retry(); await flush();
  assert.equal(f.calls[2].url, '/api/admin/logs/stream?kind=app');
  assert.equal(f.authorized(), 1); assert.equal(f.timers.size, 0);
  dispose();
});

test('stream auth error stops retry and visible tail stays bounded', async () => {
  const f = fixture(), dispose = f.mount(); await flush();
  f.calls[0].push(frame('x'.repeat(600000))); await flush();
  assert.equal(f.el('body').textContent.length, 512 * 1024);
  assert.match(f.el('gap').textContent, /显示长度/);
  f.calls[0].push('event: error\ndata: {"code":"AUTH_INVALID"}\n\n'); await flush();
  assert.equal(f.authorized(), 1); assert.equal(f.timers.size, 0);
  f.el('refresh').emit('click'); await flush(); assert.equal(f.calls.length, 1); dispose();
});
