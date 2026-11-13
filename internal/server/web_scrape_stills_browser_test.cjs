'use strict';
// Production scrape UI/CSS in real Chromium; deferred metadata API and local SVG fixtures.
// This verifies browser interaction, not an actual MetaTube deployment or disk writes.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const { available, browser } = require('./web_browser_helper.cjs');
const web = path.join(__dirname, 'web');
const source = fs.readFileSync(path.join(web, 'app.js'), 'utf8');
const start = source.indexOf('let scrapeState = null;');
const end = source.indexOf('/* ------------------------------------------------------------ 独立翻译 */', start);
assert.ok(start >= 0 && end > start);
const index = fs.readFileSync(path.join(web, 'index.html'), 'utf8');
// 浮层是原生 <dialog>，切片按 id 定位标签起点，不绑定标签名。
const modalStart = index.lastIndexOf('<', index.indexOf('id="scrape-overlay"'));
const modalEnd = index.lastIndexOf('<', index.indexOf('id="detail-overlay"'));
assert.ok(modalStart >= 0 && modalEnd > modalStart);
const html = '<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><style>' +
  fs.readFileSync(path.join(web, 'style.css'), 'utf8') + '</style></head><body>' + index.slice(modalStart, modalEnd) + '</body></html>';
const version = 'a'.repeat(64), nextVersion = 'b'.repeat(64);
const candidates = [
  { provider: 'p', id: 'first', title: 'First candidate', update: false, position: 0 },
  { provider: 'p', id: 'second', title: 'Second candidate' }
];
function detail(v = version, id = 'first') {
  return { provider: 'p', id, title: 'Inspected candidate ' + id, summary: 'Preview only; nothing has been saved.', fields: [], images: [], overwrite: false,
    stills: [0, 1, 2].map(index => ({ index, preview: `/api/admin/scrape/image?kind=still&version=${v}&index=${index}` })) };
}
async function fixture(t, width) {
  const server = http.createServer((request, response) => {
    if (request.url === '/') { response.setHeader('Content-Type', 'text/html; charset=utf-8'); response.end(html); return; }
    if (request.url.startsWith('/api/admin/scrape/image?')) {
      response.setHeader('Content-Type', 'image/svg+xml');
      response.end('<svg xmlns="http://www.w3.org/2000/svg" width="320" height="180"><rect width="320" height="180" fill="#274b63"/><text x="24" y="96" fill="white" font-size="24">Still preview fixture</text></svg>');
      return;
    }
    response.writeHead(404).end();
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise(resolve => { server.close(resolve); server.closeAllConnections(); }));
  const b = await browser(t);
  await b.call('Emulation.setDeviceMetricsOverride', { width, height: 900, deviceScaleFactor: 1, mobile: width < 860 });
  await b.navigate(`http://127.0.0.1:${server.address().port}/`);
  // URL arrival can precede document load; install the deferred API only after the fixture DOM exists.
  await waitFor(b, 'document.readyState === "complete" && !!document.querySelector("#scrape-body")');
  await b.evaluate(`
    window.__calls = []; window.__errors = []; window.__toasts = []; window.__refreshes = 0;
    addEventListener('error', e => __errors.push(e.message));
    addEventListener('unhandledrejection', e => __errors.push(String(e.reason)));
    const pageGen = 1, detailGen = 1, pageItems = [];
    // 浮层是原生 <dialog>：stub 保留真实开关语义，否则切片里的内容不可见。
    const overlayPush = selector => { const el = document.querySelector(selector); if (el && !el.open) el.showModal(); };
    const overlayPop = selector => { const el = document.querySelector(selector); if (el && el.open) el.close(); };
    const closeDetail = () => {};
    const refreshPage = () => { __refreshes++; };
    const numberUnlessInTitle = () => '';
    const toast = (...args) => __toasts.push(args);
    const esc = value => String(value ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
    const api = (url, options = {}) => new Promise((resolve, reject) => {
      __calls.push({ url, method: options.method || 'GET', body: options.body, resolve, reject });
    });
    ${source.slice(start, end)}
    window.__open = () => { void openScrapePreview({id:7}, {saved:'',canonical:'',valid:false}); };
    window.__settle = (index, data, failure) => {
      if (failure) __calls[index].reject(Object.assign(Error(failure.message), {status:failure.status}));
      else __calls[index].resolve(data);
    };
  `);
  await b.evaluate('__open()');
  await settle(b, 0, { query: 'fixture', candidates, recommended: 0 });
  await waitFor(b, '__calls.length === 2');
  await settle(b, 1, detail());
  await waitFor(b, '!!document.querySelector("#scrape-confirm")');
  return b;
}
async function waitFor(b, expression) {
  await b.evaluate(`(async () => {
    const deadline = performance.now() + 3000;
    while (!(${expression})) {
      if (performance.now() > deadline) throw Error('Timed out waiting for: ' + ${JSON.stringify(expression)});
      await new Promise(resolve => setTimeout(resolve, 10));
    }
  })()`);
}
async function settle(b, index, data, failure) {
  await b.evaluate(`__settle(${index}, ${JSON.stringify(data)}, ${JSON.stringify(failure)})`);
}
async function click(b, selector) {
  await b.evaluate(`document.querySelector(${JSON.stringify(selector)}).click()`);
}
async function space(b) {
  await b.call('Input.dispatchKeyEvent', { type: 'keyDown', key: ' ', code: 'Space', windowsVirtualKeyCode: 32 });
  await b.call('Input.dispatchKeyEvent', { type: 'keyUp', key: ' ', code: 'Space', windowsVirtualKeyCode: 32 });
}
async function sent(b) {
  return b.evaluate('__calls.filter(c => c.url === "/items/7/scrape").map(c => JSON.parse(c.body))');
}
for (const width of [1440, 390]) {
  test(`ordered still selection, keyboard, 409 reread and partial success (${width}px Chromium)`, { timeout: 60000 }, async t => {
    if (!available(t)) return;
    const b = await fixture(t, width);
    assert.deepEqual(await sent(b), []);
    assert.equal(await b.evaluate('document.querySelectorAll(".scrape-still-check:checked").length'), 0);
    assert.equal(await b.evaluate('document.querySelector("#scrape-stills-count").textContent'), '默认下载全部剧照');
    assert.equal(await b.evaluate('document.querySelector("#scrape-stills-clear").textContent'), '恢复默认全部下载');
    assert.match(await b.evaluate('document.querySelector("#scrape-stills-help").textContent'), /默认全部下载不受 24 张限制/);
    assert.equal(await b.evaluate('document.documentElement.scrollWidth <= innerWidth + 1'), true);
    assert.equal(await b.evaluate('Array.from(document.querySelectorAll(".scrape-still")).every(e => { const r=e.getBoundingClientRect(); return r.width>=44 && r.height>=44; })'), true);
    await b.evaluate('document.querySelector("#scrape-still-2").scrollIntoView({block:"center"}); document.querySelector("#scrape-still-2").focus()');
    await space(b);
    assert.equal(await b.evaluate('document.activeElement.id'), 'scrape-still-2', 'native checkbox retains keyboard focus');
    assert.equal(await b.evaluate('document.querySelector("#scrape-still-2").checked'), true);
    await click(b, '#scrape-still-0');
    assert.match(await b.evaluate('document.querySelector("#scrape-still-order-2").textContent'), /fanart1\.webp/);
    assert.match(await b.evaluate('document.querySelector("#scrape-still-order-0").textContent'), /fanart2\.webp/);
    await click(b, '#scrape-still-2');
    assert.match(await b.evaluate('document.querySelector("#scrape-still-order-0").textContent'), /fanart1\.webp/);
    await click(b, '#scrape-still-2');
    assert.match(await b.evaluate('document.querySelector("#scrape-still-order-2").textContent'), /fanart2\.webp/);
    assert.deepEqual(await sent(b), [], 'selecting and reordering never writes');
    await click(b, '#scrape-confirm');
    assert.deepEqual(await sent(b), [{ provider: 'p', id: 'first', overwrite: false, update: false, position: 0, stills: { version, indices: [0, 2] } }]);
    assert.equal(await b.evaluate('Array.from(document.querySelectorAll("#scrape-body input, #scrape-manual-search button, .scrape-candidate")).every(e => e.disabled)'), true, 'write in flight freezes candidate, search and selection');
    await click(b, '#scrape-confirm');
    await click(b, '.scrape-candidate[data-idx="1"]');
    assert.equal(await b.evaluate('__calls.length'), 3, 'disabled interactions cannot write or inspect again');
    await settle(b, 2, null, { status: 409, message: '<conflict> not committed' });
    await waitFor(b, '!document.querySelector("#scrape-confirm").disabled');
    assert.equal((await sent(b)).length, 1, 'conflict must never automatically retry');
    // Both version mismatch and NFO-busy are 409; UI must not invent a cause.
    assert.doesNotMatch(await b.evaluate('document.querySelector("#scrape-results").textContent'), /剧照列表已变化|列表已更改/);
    await click(b, '#scrape-conflict-reload');
    await waitFor(b, '__calls.length === 4');
    await settle(b, 3, detail(nextVersion));
    await waitFor(b, '!!document.querySelector("#scrape-still-1")');
    assert.equal(await b.evaluate('document.querySelectorAll(".scrape-still-check:checked").length'), 0, 'reread clears old selection');
    await click(b, '#scrape-still-1');
    await click(b, '#scrape-confirm');
    assert.deepEqual((await sent(b))[1].stills, { version: nextVersion, indices: [1] });
    await settle(b, 4, { title: '<saved>', images: 3, warnings: ['<bad image>'], refresh_error: 'index <busy>' });
    await waitFor(b, '!!document.querySelector("#scrape-done")');
    assert.equal(await b.evaluate('document.activeElement.id'), 'scrape-done');
    assert.equal(await b.evaluate('!!document.querySelector("#scrape-confirm, #scrape-manual-search, .scrape-still-check")'), false);
    assert.match(await b.evaluate('document.querySelector("#scrape-body").textContent'), /NFO 已保存/);
    assert.equal(await b.evaluate('!!document.querySelector("#scrape-body saved, #scrape-body bad")'), false);
    await click(b, '#scrape-done');
    assert.equal(await b.evaluate('!document.querySelector("#scrape-overlay").open'), true);
    assert.deepEqual(await b.evaluate('__errors'), []);
  });
}

test('candidate change restores default-all, custom clear omits stills, and close isolates late write response (Chromium)', { timeout: 60000 }, async t => {
  if (!available(t)) return;
  const b = await fixture(t, 1440);
  await click(b, '#scrape-still-2');
  await click(b, '.scrape-candidate[data-idx="1"]');
  await waitFor(b, '__calls.length === 3');
  await settle(b, 2, detail(nextVersion, 'second'));
  await waitFor(b, '!!document.querySelector("#scrape-confirm")');
  assert.equal(await b.evaluate('document.querySelectorAll(".scrape-still-check:checked").length'), 0);
  assert.equal(await b.evaluate('document.querySelector("#scrape-stills-count").textContent'), '默认下载全部剧照');
  await click(b, '#scrape-still-1');
  assert.equal(await b.evaluate('document.querySelector("#scrape-stills-count").textContent'), '仅下载已选 1 张');
  assert.equal(await b.evaluate('document.querySelector("#scrape-still-order-0").textContent'), '不在自定义范围');
  await click(b, '#scrape-stills-clear');
  assert.equal(await b.evaluate('document.querySelector("#scrape-stills-count").textContent'), '默认下载全部剧照');
  assert.equal(await b.evaluate('document.querySelector("#scrape-still-order-0").textContent'), '默认下载全部');
  await click(b, '#scrape-confirm');
  assert.deepEqual(await sent(b), [{ provider: 'p', id: 'second', overwrite: false }]);
  await click(b, '#scrape-cancel-btn');
  await b.evaluate('__open()');
  await settle(b, 3, { title: 'old result', images: 5, warnings: ['old warning'] });
  assert.equal(await b.evaluate('!!document.querySelector("#scrape-done")'), false);
  assert.equal(await b.evaluate('__refreshes'), 0);
  assert.deepEqual(await b.evaluate('__toasts'), []);
  assert.equal(await b.evaluate('!document.querySelector("#scrape-overlay").open'), false);
  assert.deepEqual(await b.evaluate('__errors'), []);
});
