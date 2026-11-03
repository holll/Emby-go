'use strict';
// Real Chromium, real scrape UI/functions/CSS, deferred API fixtures; no server.
// Run: node --test internal/server/web_manual_search_browser_test.cjs
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { available, browser } = require('./web_browser_helper.cjs');
const web = path.join(__dirname, 'web');
const source = fs.readFileSync(path.join(web, 'app.js'), 'utf8');
const start = source.indexOf('let scrapeState = null;');
const end = source.indexOf('/* ------------------------------------------------------------ 独立翻译 */', start);
assert.ok(start >= 0 && end > start, 'extract complete production scrape UI, not a copied implementation');
const scrapeCode = source.slice(start, end);
const index = fs.readFileSync(path.join(web, 'index.html'), 'utf8');
const modalStart = index.indexOf('<div id="scrape-overlay"');
const modalEnd = index.indexOf('<div id="detail-overlay"', modalStart);
assert.ok(modalStart >= 0 && modalEnd > modalStart);
const html = '<!doctype html><html><head><meta name="viewport" content="width=device-width, initial-scale=1"><style>' +
  fs.readFileSync(path.join(web, 'style.css'), 'utf8') + '</style></head><body>' + index.slice(modalStart, modalEnd) + '</body></html>';

async function fixture(t) {
  const b = await browser(t);
  await b.navigate('data:text/html;base64,' + Buffer.from(html).toString('base64'));
  await b.evaluate(`
    window.__calls = []; window.__errors = []; window.__toasts = [];
    addEventListener('error', e => __errors.push(e.message));
    addEventListener('unhandledrejection', e => __errors.push(String(e.reason)));
    const pageGen = 1, detailGen = 1, pageItems = [];
    const overlayPush = () => {}, overlayPop = () => {};
    const closeDetail = () => {}, refreshPage = () => {};
    const numberUnlessInTitle = (title, number) => title?.includes(number) ? '' : number;
    const toast = (...args) => __toasts.push(args);
    const esc = value => String(value ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
    const api = (url, options = {}) => new Promise((resolve, reject) => {
      __calls.push({url, method:options.method || 'GET', body:options.body, resolve, reject});
    });
    ${scrapeCode}
    window.__open = () => { void openScrapePreview({id:7}); };
    window.__settle = (index, data, error) => {
      if (error) __calls[index].reject(Error(error)); else __calls[index].resolve(data);
    };
  `);
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
async function calls(b) {
  return b.evaluate('__calls.map(({url, method, body}) => ({url, method, body}))');
}
async function settle(b, index, data, error) {
  await b.evaluate(`__settle(${index}, ${JSON.stringify(data)}, ${JSON.stringify(error)})`);
}
async function submit(b, q, id) {
  await b.evaluate(`document.querySelector('#scrape-query').value = ${JSON.stringify(q)};
    document.querySelector('#scrape-metatube-id').value = ${JSON.stringify(id)};
    document.querySelector('#scrape-manual-search').requestSubmit();`);
}
async function assertNoConfirm(b) {
  assert.equal(await b.evaluate('!!document.querySelector("#scrape-confirm")'), false, 'no confirmation before successful inspection');
  assert.equal((await calls(b)).filter(c => c.url === '/items/7/scrape').length, 0, 'search/inspect must never write');
}
for (const width of [1440, 390]) {
  test(`manual scrape search stays usable and writes only inspected selection (${width}px Chromium)`, { timeout:60000 }, async t => {
    if (!available(t)) return;
    const b = await fixture(t);
    await b.call('Emulation.setDeviceMetricsOverride', {width, height:900, deviceScaleFactor:1, mobile:width < 860});
    await b.evaluate('__open()');
    assert.deepEqual(await calls(b), [{url:'/items/7/scrape/preview', method:'GET'}]);
    await assertNoConfirm(b);
    await settle(b, 0, {query:'filename without a match', candidates:[]});
    await waitFor(b, 'document.querySelector("#scrape-results").textContent.includes("没有搜到候选")');
    assert.equal(await b.evaluate('document.querySelector("#scrape-overlay").hidden'), false);
    assert.deepEqual(await b.evaluate(`['scrape-query','scrape-metatube-id'].map(id => {
      const e = document.getElementById(id), r = e.getBoundingClientRect();
      return {visible:r.width > 0 && r.height > 0, label:!!e.labels.length, within:r.left >= 0 && r.right <= innerWidth + 1};
    })`), [{visible:true,label:true,within:true},{visible:true,label:true,within:true}], 'labelled manual inputs remain visible at viewport width');
    assert.equal(await b.evaluate('document.documentElement.scrollWidth <= innerWidth + 1'), true, 'no document horizontal overflow');
    await b.evaluate('window.__form = document.querySelector("#scrape-manual-search")');
    // A native form submission is manual even when both fields are empty.
    const queries = [
      {q:'', metatube_id:''},
      {q:'  ABP-123  ', metatube_id:'  Provider1  '},
      {q:'', metatube_id:'Provider1:movie-id/42?lang=ja&part=1'},
      {q:'https://example.invalid/movie?id=a%2Fb&lang=ja#detail', metatube_id:''},
    ];
    for (const query of queries) {
      const n = (await calls(b)).length;
      await submit(b, query.q, query.metatube_id);
      const request = (await calls(b))[n];
      const url = new URL(request.url, 'https://fixture.invalid');
      assert.equal(request.method, 'GET');
      assert.equal(url.pathname, '/items/7/scrape/preview');
      const expected = query.q.trim() && query.metatube_id.trim()
        ? [['q',query.q.trim()],['metatube_id',query.metatube_id.trim()]]
        : query.metatube_id.trim() ? [['metatube_id',query.metatube_id.trim()]] : [['q',query.q.trim()]];
      assert.deepEqual([...url.searchParams], expected, 'preserve exact values, omit unused optional fields, but keep explicit empty q=');
      await assertNoConfirm(b);
      await settle(b, n, {query:query.q.trim(), candidates:[]});
      await waitFor(b, 'document.querySelector("#scrape-results").textContent.includes("没有搜到候选")');
      assert.equal(await b.evaluate('__form === document.querySelector("#scrape-manual-search")'), true, 'results must not replace the form or its event handlers');
    }
    let n = (await calls(b)).length;
    await submit(b, 'retry title', 'Provider1');
    await settle(b, n, null, '<img src=x onerror="window.__injected=true"> offline');
    await waitFor(b, '!!document.querySelector("#scrape-results [role=alert]")');
    assert.match(await b.evaluate('document.querySelector("#scrape-results [role=alert]").textContent'), /<img.*offline/);
    assert.equal(await b.evaluate('!!document.querySelector("#scrape-results img") || !!window.__injected'), false, 'search errors are text, not injected HTML');
    assert.equal(await b.evaluate('document.querySelector("#scrape-query").value'), 'retry title');
    await assertNoConfirm(b);
    // Automatic retry ignores still-populated manual controls.
    n = (await calls(b)).length;
    await b.evaluate('document.querySelector("#scrape-auto-search").click()');
    assert.equal((await calls(b))[n].url, '/items/7/scrape/preview');
    await settle(b, n, {query:'automatic', candidates:[]});
    await waitFor(b, 'document.querySelector("#scrape-results").textContent.includes("没有搜到候选")');
    n = (await calls(b)).length;
    await submit(b, '', 'Provider1:exact-id');
    const candidate = {provider:'Provider1', id:'exact-id', title:'Exact inspected title', number:'ABP-123'};
    await settle(b, n, {query:'Provider1:exact-id', candidates:[candidate], recommended:0});
    await waitFor(b, `__calls.length === ${n + 2}`);
    assert.deepEqual((await calls(b))[n + 1], {url:'/items/7/scrape/inspect', method:'POST', body:JSON.stringify({provider:'Provider1',id:'exact-id'})});
    await assertNoConfirm(b); // Delayed inspect cannot expose a write button.
    await settle(b, n + 1, null, '<offline> inspect');
    await waitFor(b, '!!document.querySelector("#scrape-inspect-retry")');
    await assertNoConfirm(b);
    assert.match(await b.evaluate('document.querySelector("#scrape-results [role=alert]").textContent'), /<offline> inspect/);
    await b.evaluate('document.querySelector("#scrape-inspect-retry").click()');
    assert.deepEqual((await calls(b))[n + 2], (await calls(b))[n + 1], 'retry inspects exactly the same candidate');
    await settle(b, n + 2, {...candidate, fields:[{label:'标题', old:'Old title', next:'Exact inspected title', change:true}], images:[], summary:'Inspected metadata'});
    await waitFor(b, '!!document.querySelector("#scrape-confirm")');
    assert.match(await b.evaluate('document.querySelector(".scrape-detail").textContent'), /Exact inspected title/);
    assert.equal((await calls(b)).filter(c => c.url === '/items/7/scrape').length, 0, 'rendering successful inspect is still preview only');
    await b.evaluate('document.querySelector("#scrape-force").click(); document.querySelector("#scrape-confirm").click()');
    assert.deepEqual((await calls(b)).filter(c => c.url === '/items/7/scrape'), [{url:'/items/7/scrape', method:'POST', body:JSON.stringify({provider:'Provider1',id:'exact-id',overwrite:true})}]);
    assert.equal(await b.evaluate('document.querySelector("#scrape-confirm").disabled'), true);
    await settle(b, (await calls(b)).length - 1, {title:'Exact inspected title',images:0});
    await waitFor(b, 'document.querySelector("#scrape-overlay").hidden');
    assert.deepEqual(await b.evaluate('__errors'), [], 'no browser exceptions or unhandled rejections');
  });
}
