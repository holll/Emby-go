'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname, 'web/artwork.js'), 'utf8');
const flush = async () => { for (let i = 0; i < 30; i++) await Promise.resolve(); };
function fixture(api, confirm = () => true, options = {}) {
  let next = 0, current = true, completed = 0;
  const nodes = new Map(), timers = new Map(), calls = [];
  function node(key) {
    if (!nodes.has(key)) nodes.set(key, { value: '', checked: false, hidden: false, disabled: false, textContent: '', innerHTML: '', dataset: {}, events: new Map(), querySelector() { return node('candidate-image'); }, addEventListener(e, fn) { this.events.set(e, fn); }, removeEventListener(e) { this.events.delete(e); }, setAttribute(k, v) { this[k] = v; }, removeAttribute(k) { delete this[k]; } });
    return nodes.get(key);
  }
  node('position').value = '10'; node('position-mode').value = 'percent';
  const host = { isConnected: true, innerHTML: '', querySelector(s) { return node(s.match(/data-art="([^"]+)"/)[1]); }, querySelectorAll() {
    const matches = [...node('candidates').innerHTML.matchAll(/data-candidate="(\d+)"/g)];
    return matches.map(m => { const n = node('candidate-' + m[1]); n.dataset.candidate = m[1]; return n; });
  } };
  const box = options.box || vm.createContext({ AbortController, URL: options.URL || URL, console, setTimeout(fn, ms) { const id = ++next; timers.set(id, { fn, ms }); return id; }, clearTimeout(id) { timers.delete(id); } });
  if (!options.box) vm.runInContext(source, box);
  const dispose = box.mountArtwork(host, { movie: { id: 19, Title: '<unsafe>' }, confirm, imageRequest: options.imageRequest, isCurrent: () => current, onComplete: () => completed++, apiRequest: (p, o) => { calls.push([p, o]); return p === '/artwork/status' ? Promise.resolve(options.latest || { task_id: 0, instance_id: 'test-instance', running: false, movie_ids: null }) : api(p, o); } });
  return { box, host, calls, timers, node, dispose, completed: () => completed, stale: () => { current = false; }, click: key => node(key).events.get('click')?.(), async tick(ms) { const entry = [...timers].find(([, t]) => t.ms === ms); assert.ok(entry, 'timer ' + ms); timers.delete(entry[0]); entry[1].fn(); await flush(); } };
}
test('real Chromium cover controls retain native keyboard focus, 44px targets and no overflow at mobile width', async t => {
  const { available, probe } = require('./web_browser_helper.cjs');
  if (!available(t)) return;
  const css = fs.readFileSync(path.join(__dirname, 'web/style.css'), 'utf8') + fs.readFileSync(path.join(__dirname, 'web/artwork.css'), 'utf8');
  const doc = `<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><meta charset="utf-8"><style>${css}body{padding:16px}#host{width:100%;max-width:343px}</style><div id="host"></div><script>${source}
    mountArtwork(document.querySelector('#host'), {movie:{id:19,Title:'A very long title '.repeat(20)},apiRequest:async path=>path.includes('capabilities')?({screenshot:true,provider:true}):({task_id:0,instance_id:'test-instance',running:false,movie_ids:null})});
    setTimeout(()=>{const button=document.querySelector('[data-art="screenshot"]'),select=document.querySelector('select'),input=document.querySelector('[data-art="position"]');button.focus(); const out={width:innerWidth,overflow:document.documentElement.scrollWidth>innerWidth,button:button.getBoundingClientRect().height,select:select.getBoundingClientRect().height,input:input.getBoundingClientRect().height,focus:document.activeElement===button,hidden:getComputedStyle(document.querySelector('[data-art="error"]')).display,gap:getComputedStyle(document.querySelector('.artwork-actions')).gap};document.body.innerHTML='<pre id="result"></pre>';document.querySelector('#result').textContent=JSON.stringify(out);},0);</script>`;
  const widths = [375, 768, 1280];
  const results = await probe(t, doc, widths);
  for (const [i, actual] of results.entries()) {
    assert.equal(actual.width, widths[i]);
    assert.equal(actual.overflow,false); assert.ok(actual.button>=44); assert.ok(actual.select>=44); assert.ok(actual.input>=44); assert.equal(actual.focus,true); assert.equal(actual.hidden,'none'); assert.equal(actual.gap,'8px');
  }
});

const caps = { screenshot: true, provider: true };
const status = extra => ({ task_id: 8, instance_id: 'test-instance', movie_ids: [19], running: false, success: 1, total: 1, done: 1, ...extra });

test('default screenshot is 10%, non-overwrite and requires confirmation; rejected confirmation never POSTs', async () => {
  const f = fixture(async () => caps, () => false); await flush(); f.click('screenshot'); await flush();
  assert.equal(f.calls.length, 2); assert.match(f.host.innerHTML, /value="10"/); assert.match(f.host.innerHTML, /&lt;unsafe&gt;/); f.dispose();
  const g = fixture(async p => p.includes('capabilities') ? caps : p.includes('/run') ? { task_id: 8, instance_id: 'test-instance' } : status());
  await flush(); g.click('screenshot'); await flush();
  assert.deepEqual(JSON.parse(g.calls.find(([p]) => p.endsWith('/run'))[1].body), { mode: 'screenshot', movie_ids: [19], overwrite: false, position_percent: 10 });
  assert.equal(g.completed(), 1); g.dispose();
});

test('candidate text escapes, thumbnails use fixed proxy, provider payload only replaces cover', async () => {
  const f = fixture(async p => p.includes('capabilities') ? caps : p.includes('candidates') ? { candidates: [{ provider: 'P"', id: 'a&b', title: '<img onerror=bad>', thumb: 'https://evil/private' }] } : p.includes('/run') ? { task_id: 8, instance_id: 'test-instance' } : status());
  await flush(); f.click('search'); await flush();
  assert.match(f.node('candidates').innerHTML, /&lt;img onerror=bad&gt;/);
  assert.match(f.node('candidates').innerHTML, /\/api\/admin\/scrape\/image\?kind=thumb/);
  assert.doesNotMatch(f.node('candidates').innerHTML, /https:\/\/evil|<img onerror/);
  f.click('candidate-0'); await flush();
  assert.deepEqual(JSON.parse(f.calls.find(([p]) => p.endsWith('/run'))[1].body), { mode: 'provider', movie_ids: [19], provider: 'P"', id: 'a&b', overwrite: true }); f.dispose();
});

test('candidate Blob previews use fixed proxy and revoke or abort on replacement and disposal', async () => {
  const revoked = [], requests = []; let resolve;
  const options = { URL: { createObjectURL: () => 'blob:safe-preview', revokeObjectURL: url => revoked.push(url) }, imageRequest: (p, o) => { requests.push([p, o]); return requests.length === 1 ? Promise.resolve({ type: 'image/png' }) : new Promise(done => { resolve = done; }); } };
  const f = fixture(async p => p.includes('capabilities') ? caps : { candidates: [{ provider: 'Fixture', id: '1', thumb: 'https://evil/private' }] }, undefined, options);
  await flush(); f.click('search'); await flush();
  assert.match(requests[0][0], /^\/api\/admin\/scrape\/image\?kind=thumb&provider=Fixture&id=1$/);
  assert.equal(f.node('candidate-image').src, 'blob:safe-preview');
  assert.doesNotMatch(f.node('candidates').innerHTML, /src=|https:\/\/evil|api_key=/);
  f.click('search'); await flush(); assert.deepEqual(revoked, ['blob:safe-preview']);
  f.dispose(); assert.equal(requests[1][1].signal.aborted, true); assert.equal(f.timers.size, 0);
  resolve({ type: 'image/png' }); await flush(); assert.deepEqual(revoked, ['blob:safe-preview']);
});

test('URL searches rejected, seconds validation, no automatic POST replay on failure', async () => {
  const f = fixture(async p => { if (p.includes('capabilities')) return caps; throw Error('secret https://private/token'); });
  await flush(); f.node('query').value = 'https://evil'; f.click('search'); await flush(); assert.equal(f.calls.length, 2);
  f.node('position').value = '-1'; f.click('screenshot'); await flush(); assert.equal(f.calls.length, 2);
  f.node('position-mode').value = 'seconds'; f.node('position').value = '42.5'; f.click('screenshot'); await flush();
  assert.equal(JSON.parse(f.calls.find(([p]) => p.endsWith('/run'))[1].body).position_seconds, 42.5);
  f.click('screenshot'); await flush(); assert.equal(f.calls.length, 3); assert.equal(f.node('screenshot').disabled, true);
  assert.doesNotMatch(f.node('error').textContent, /private|secret/); f.dispose();
});

test('screenshot percentage rejects 100 before confirmation or POST but accepts values below 100', async () => {
  let confirmations = 0;
  const f = fixture(async p => p.includes('capabilities') ? caps : p.endsWith('/run') ? { task_id: 8, instance_id: 'test-instance' } : status(), () => { confirmations++; return true; });
  await flush(); f.node('position').value = '100'; f.click('screenshot'); await flush();
  assert.equal(confirmations, 0); assert.equal(f.calls.length, 2); assert.match(f.node('error').textContent, /小于 100/);
  f.node('position').value = '99.999'; f.click('screenshot'); await flush();
  assert.equal(confirmations, 1); assert.equal(JSON.parse(f.calls.find(([p]) => p.endsWith('/run'))[1].body).position_percent, 99.999); f.dispose();
});

test('terminal response must match accepted task identity, status failures back off without POST', async () => {
  const f = fixture(async p => p.includes('capabilities') ? caps : p.includes('/run') ? { task_id: 8, instance_id: 'test-instance' } : status({ task_id: 99 }));
  await flush(); f.click('screenshot'); await flush(); assert.equal(f.completed(), 0); assert.equal(f.node('retry').hidden, false);
  await f.tick(2000); assert.equal(f.calls.filter(([p]) => p.endsWith('/run')).length, 1); assert.equal(f.completed(), 0); f.dispose(); assert.equal(f.timers.size, 0);
});

test('cancel identifies accepted task and terminal cancellation remains explicit', async () => {
  let cancelled = false;
  const f = fixture(async p => p.includes('capabilities') ? caps : p.endsWith('/run') ? { task_id: 8, instance_id: 'test-instance' } : p.endsWith('/cancel') ? (cancelled = true, { task_id: 8, instance_id: 'test-instance', status: 'cancelling' }) : status({ running: !cancelled, cancelled, success: 0 }));
  await flush(); f.click('screenshot'); await flush(); f.click('cancel'); await flush();
  assert.deepEqual(JSON.parse(f.calls.find(([p]) => p.endsWith('/cancel'))[1].body), { task_id: 8, instance_id: 'test-instance' });
  assert.match(f.node('state').textContent, /已取消/); assert.equal(f.completed(), 0); f.dispose();
});

test('dispose aborts GET, clears listeners and stale old responses cannot refresh another detail', async () => {
  let resolve;
  const f = fixture(p => p.includes('capabilities') ? Promise.resolve(caps) : p.endsWith('/run') ? Promise.resolve({ task_id: 8, instance_id: 'test-instance' }) : new Promise(done => { resolve = done; }));
  await flush(); f.click('screenshot'); await flush(); const signal = f.calls.at(-1)[1].signal;
  f.stale(); f.dispose(); assert.equal(signal.aborted, true); resolve(status()); await flush();
  assert.equal(f.completed(), 0); assert.equal(f.node('screenshot').events.size, 0); assert.equal(f.timers.size, 0);
});

test('late POST accepted after identity/detail change never observes or refreshes', async () => {
  let resolve;
  const f = fixture(p => p.includes('capabilities') ? Promise.resolve(caps) : new Promise(done => { resolve = done; }));
  await flush(); f.click('screenshot'); await flush(); f.stale(); f.dispose(); resolve({ task_id: 8, instance_id: 'test-instance' }); await flush();
  assert.equal(f.calls.length, 3); assert.equal(f.completed(), 0); assert.equal(f.timers.size, 0);
});

test('capability and candidate failures persist without rendering upstream secrets', async () => {
  const f = fixture(async () => { throw Error('https://private?token=secret'); }); await flush();
  assert.equal(f.node('screenshot').disabled, true); assert.equal(f.node('error').hidden, false); assert.doesNotMatch(f.node('error').textContent, /secret|private/); f.dispose();
  const g = fixture(async p => { if (p.includes('capabilities')) return caps; throw Error('secret'); }); await flush(); g.click('search'); await flush();
  assert.equal(g.node('error').hidden, false); assert.equal(g.node('search').disabled, false); g.dispose();
});

test('status GET times out at 10s and remains serial until timeout; cleanup prevents retries', async () => {
  const f = fixture(p => p.includes('capabilities') ? Promise.resolve(caps) : p.endsWith('/run') ? Promise.resolve({ task_id: 8, instance_id: 'test-instance' }) : new Promise(() => {}));
  await flush(); f.click('screenshot'); await flush(); f.click('retry'); await flush(); assert.equal(f.calls.length, 4);
  await f.tick(10000); assert.equal(f.calls.at(-1)[1].signal.aborted, true); assert.equal(f.node('retry').hidden, false);
  f.dispose(); assert.equal([...f.timers.values()].some(t => t.ms === 2000), false);
});

test('reopening restores the accepted task and refreshes completion only once across mounts', async () => {
  const api = async p => p.includes('capabilities') ? caps : status();
  const f = fixture(api, undefined, { latest: status({ running: true }) }); await flush();
  assert.equal(f.node('screenshot').disabled, false); // fixture poll immediately reaches terminal
  assert.equal(f.completed(), 1); assert.equal(f.calls.some(([p]) => p.includes('task_id=8')), true); f.dispose();
  const g = fixture(api, undefined, { box: f.box, latest: status() }); await flush();
  assert.equal(g.completed(), 0); assert.equal(g.calls.some(([p]) => p.endsWith('/run')), false); g.dispose();
});

test('other-movie tasks block mutations but cannot cancel or refresh this movie', async () => {
  const latest = status({ movie_ids: [77], running: true });
  const f = fixture(async p => p.includes('capabilities') ? caps : latest, undefined, { latest }); await flush();
  assert.equal(f.node('screenshot').disabled, true); assert.equal(f.node('cancel').hidden, true);
  assert.match(f.node('state').textContent, /其他影片/); assert.equal(f.completed(), 0);
  f.click('screenshot'); await flush(); assert.equal(f.calls.some(([p]) => p.endsWith('/run')), false); f.dispose();
});

test('ambiguous POST remains locked across close/reopen, then recovers late accepted identity', async () => {
  let resolve;
  const f = fixture(p => p.includes('capabilities') ? Promise.resolve(caps) : new Promise(done => { resolve = done; }));
  await flush(); f.click('screenshot'); await flush(); await f.tick(10000);
  assert.equal(f.node('retry').hidden, false); assert.equal(f.node('screenshot').disabled, true); f.dispose();
  const g = fixture(async p => p.includes('capabilities') ? caps : status(), undefined, { box: f.box }); await flush();
  assert.equal(g.node('screenshot').disabled, true); g.click('screenshot'); await flush();
  assert.equal(g.calls.some(([p]) => p.endsWith('/run')), false);
  resolve({ task_id: 8, instance_id: 'test-instance' }); await flush(); g.click('retry'); await flush();
  assert.equal(g.completed(), 1); assert.equal(g.node('screenshot').disabled, false); g.dispose();
});

test('definitive rejection unlocks inputs and conflict discovers the concurrent task without replay', async () => {
  const f = fixture(async p => { if (p.includes('capabilities')) return caps; throw Object.assign(new Error('private'), { status: 400 }); });
  await flush(); f.click('screenshot'); await flush(); assert.equal(f.node('screenshot').disabled, false);
  assert.match(f.node('state').textContent, /未提交/); assert.doesNotMatch(f.node('error').textContent, /private/); f.dispose();
  const options = {};
  const g = fixture(async p => {
    if (p.includes('capabilities')) return caps;
    if (p.endsWith('/run')) { options.latest = status({ running: true, movie_ids: [77] }); throw Object.assign(new Error('busy'), { status: 409 }); }
    return options.latest;
  }, undefined, options);
  await flush(); g.click('screenshot'); await flush(); assert.equal(g.node('screenshot').disabled, true);
  assert.match(g.node('state').textContent, /其他影片/); assert.equal(g.calls.filter(([p]) => p.endsWith('/run')).length, 1); g.dispose();
});

test('instance identity and every-polled target are verified before cancel or completion', async () => {
  for (const mismatch of [{ instance_id: 'restarted-instance' }, { movie_ids: [77] }]) {
    const f = fixture(async p => p.includes('capabilities') ? caps : p.endsWith('/run') ? { task_id: 8, instance_id: 'test-instance' } : status(mismatch));
    await flush(); f.click('screenshot'); await flush();
    assert.match(f.calls.at(-1)[0], /task_id=8&instance_id=test-instance/);
    assert.equal(f.node('cancel').disabled, true); assert.equal(f.completed(), 0);
    f.click('cancel'); await flush(); assert.equal(f.calls.some(([p]) => p.endsWith('/cancel')), false); f.dispose();
  }
  const f = fixture(async p => p.includes('capabilities') ? caps : status(), undefined, { latest: status() });
  await flush(); assert.equal(f.completed(), 1); f.dispose();
  const restarted = status({ instance_id: 'restarted-instance' });
  const g = fixture(async p => p.includes('capabilities') ? caps : restarted, undefined, { box: f.box, latest: restarted });
  await flush(); assert.equal(g.completed(), 1); g.dispose();
});

test('cancel requested during status read preserves serial polling until terminal', async () => {
  let resolve, cancelled = false, reads = 0;
  const f = fixture(p => p.includes('capabilities') ? Promise.resolve(caps) : p.endsWith('/run') ? Promise.resolve({ task_id: 8, instance_id: 'test-instance' }) : p.endsWith('/cancel') ? (cancelled = true, Promise.resolve({ task_id: 8, instance_id: 'test-instance' })) : cancelled ? Promise.resolve(status({ cancelled: true, success: 0 })) : ++reads === 1 ? Promise.resolve(status({ running: true, success: 0 })) : new Promise(done => { resolve = done; }));
  await flush(); f.click('screenshot'); await flush(); await f.tick(1200); f.click('cancel'); await flush();
  assert.equal(f.calls.filter(([p]) => p.includes('task_id=8')).length, 2);
  resolve(status({ running: true, success: 0 })); await flush(); await f.tick(1200);
  assert.match(f.node('state').textContent, /已取消/); assert.equal(f.node('cancel').hidden, true); f.dispose();
});
