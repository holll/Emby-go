'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, 'web/app.js'), 'utf8');
const scrapeStart = source.indexOf('let scrapeState = null;');
const scrapeEnd = source.indexOf('/* ------------------------------------------------------------ 独立翻译 */', scrapeStart);
assert.ok(scrapeStart >= 0 && scrapeEnd > scrapeStart);
const escCode = source.slice(source.indexOf('const esc = '), source.indexOf('const icon = '));
const scrapeCode = source.slice(scrapeStart, scrapeEnd);

function fixture(api) {
  const calls = [], toasts = [], refreshes = [], closedDetails = [], popped = [];
  let active = null;
  function element() {
    const children = new Map();
    return {
      html: '', disabled: false, checked: true, hidden: false, handlers: {},
      set innerHTML(html) {
        this.html = html;
        children.clear();
        for (const match of html.matchAll(/id="([^"]+)"/g)) children.set('#' + match[1], element());
      },
      get innerHTML() { return this.html; },
      querySelector(selector) { return children.get(selector) || null; },
      querySelectorAll() { return []; },
      addEventListener(event, handler) { this.handlers[event] = handler; },
      focus() { active = this; },
      click() { if (!this.disabled) return this.handlers.click(); }
    };
  }
  const results = element(), body = element(), overlay = element();
  const nodes = { '#scrape-results': results, '#scrape-body': body, '#scrape-overlay': overlay };
  const box = vm.createContext({
    URLSearchParams, pageGen: 3, detailGen: 9, pageItems() {},
    document: { querySelector: selector => nodes[selector] || null },
    api: async (url, options) => { calls.push({ url, options }); return api(url, options); },
    toast: (...args) => toasts.push(args),
    closeDetail: () => closedDetails.push(true),
    refreshPage: (render, gen) => { refreshes.push(gen); assert.equal(overlay.hidden, !body.innerHTML.includes('scrape-done')); },
    overlayPop: selector => popped.push(selector),
    numberUnlessInTitle: () => ''
  });
  vm.runInContext(escCode + scrapeCode, box);
  vm.runInContext(`scrapeState = {
    movieId: 7, provider: 'p', id: 'm', searchSeq: 1, inspectSeq: 2,
    preview: { query: 'movie', candidates: [] }, inspect: { title: 'movie', fields: [] }
  }; renderScrapePreview();`, box);
  const confirm = results.querySelector('#scrape-confirm');
  return { box, calls, toasts, refreshes, closedDetails, popped, results, body, overlay, confirm,
    get active() { return active; }, submit: () => confirm.click() };
}

test('clean confirmation keeps existing toast, close, detail close and page refresh behavior', async () => {
  for (const result of [{ title: 'Movie', images: 2 }, { title: 'Movie', images: 2, warnings: [], refresh_error: '', auto_marks_error: '' }]) {
    const env = fixture(async () => result);
    await env.submit();
    assert.deepEqual(env.toasts, [['已写入「Movie」（图片 2 张）']]);
    assert.equal(env.overlay.hidden, true);
    assert.equal(env.closedDetails.length, 1);
    assert.deepEqual(env.refreshes, [3]);
    assert.equal(env.body.innerHTML, '');
    assert.equal(env.calls.length, 1);
    assert.equal(env.calls[0].url, '/items/7/scrape');
    assert.equal(env.calls[0].options.method, 'POST');
  }
});

for (const [label, partial] of [
  ['auto marks error', { auto_marks_error: 'ownership write failed' }],
  ['auto marks and index errors', { auto_marks_error: 'ownership write failed', refresh_error: 'database busy' }],
  ['all independent issues', { auto_marks_error: 'ownership write failed', warnings: ['poster download failed'], refresh_error: 'database busy' }],
  ['image warnings', { warnings: ['poster download failed'] }],
  ['index refresh error', { refresh_error: 'database busy' }],
  ['both independent issues', { warnings: ['thumb download failed', 'poster download failed'], refresh_error: 'database busy' }]
]) {
  test(`${label}: persistent saved-NFO result replaces write actions, Done only closes`, async () => {
    const env = fixture(async () => ({ title: 'Saved movie', images: 1, ...partial }));
    await env.submit();
    const html = env.body.innerHTML;
    assert.match(html, /role="status" aria-live="polite" aria-atomic="true" aria-labelledby="scrape-result-title"/);
    assert.match(html, /NFO 已保存/);
    assert.match(html, /Saved movie/);
    assert.match(html, /不会自动重试写入/);
    assert.equal(html.includes('标记归属需人工核对'), !!partial.auto_marks_error);
    if (partial.auto_marks_error) {
      assert.match(html, /不要直接重新刮削/);
      assert.match(html, /重扫不能恢复归属记录/);
      assert.match(html, /ownership write failed/);
    }
    assert.equal(html.includes('索引需重扫'), !!partial.refresh_error);
    assert.equal(html.includes('图片警告'), !!partial.warnings);
    if (partial.refresh_error) {
      assert.match(html, /媒体库中手动重扫/);
      assert.match(html, /database busy/);
    }
    for (const warning of partial.warnings || []) assert.ok(html.includes(warning));
    assert.doesNotMatch(html, /scrape-confirm|scrape-force|scrape-manual-search|确认写入|NFO.*保存失败/);
    assert.equal(env.overlay.hidden, false);
    assert.deepEqual(env.toasts, []);
    assert.deepEqual(env.refreshes, [3], 'refresh does not close the pending result panel');
    const done = env.body.querySelector('#scrape-done');
    assert.equal(env.active, done, 'focus moves from removed confirmation button to keyboard-operable Done');
    assert.match(html, /<button id="scrape-done" type="button"/);
    // Even a retained reference to the old disabled write button cannot submit again.
    await env.confirm.handlers.click();
    await done.click();
    assert.equal(env.calls.length, 1);
    assert.equal(env.overlay.hidden, true);
    assert.deepEqual(env.popped, ['#scrape-overlay']);
    assert.equal(vm.runInContext('scrapeState', env.box), null);
  });
}

test('partial result escapes untrusted title, refresh error, image count and each warning', async () => {
  const unsafe = `<img src=x onerror="boom('x')"> &`;
  const env = fixture(async () => ({ title: unsafe, images: unsafe, auto_marks_error: unsafe, refresh_error: unsafe, warnings: [unsafe, '<script>boom()</script>'] }));
  await env.submit();
  assert.doesNotMatch(env.body.innerHTML, /<img|<script>|onerror="/);
  assert.match(env.body.innerHTML, /&lt;img src=x onerror=&quot;boom\(&#39;x&#39;\)&quot;&gt; &amp;/);
  assert.match(env.body.innerHTML, /&lt;script&gt;boom\(\)&lt;\/script&gt;/);
});

for (const change of ['page', 'closed', 'replaced']) {
  for (const outcome of ['partial', 'clean', 'failure']) {
    test(`stale ${change} response ignores ${outcome} UI, toast, close and refresh`, async () => {
      let resolve, reject;
      const env = fixture(() => new Promise((yes, no) => { resolve = yes; reject = no; }));
      const pending = env.submit();
      assert.equal(env.confirm.disabled, true);
      vm.runInContext(change === 'page' ? 'pageGen++' : change === 'closed' ? 'scrapeState = null' : 'scrapeState = { movieId: 8 }', env.box);
      if (outcome === 'failure') reject(Error('old failure'));
      else resolve({ title: 'old title', images: 1, ...(outcome === 'partial' ? { refresh_error: 'old failure', warnings: ['old warning'] } : {}) });
      await pending;
      assert.equal(env.body.innerHTML, '');
      assert.equal(env.overlay.hidden, false);
      assert.deepEqual(env.toasts, []);
      assert.deepEqual(env.refreshes, []);
      assert.deepEqual(env.closedDetails, []);
    });
  }
}

test('partial result invalidates pending search/inspect and preserves a newer detail session', async () => {
  const env = fixture(async () => ({ title: 'Saved', images: 0, warnings: ['image unavailable'] }));
  vm.runInContext('detailGen++', env.box);
  await env.submit();
  assert.equal(vm.runInContext('scrapeState.searchSeq', env.box), 2);
  assert.equal(vm.runInContext('scrapeState.inspectSeq', env.box), 3);
  assert.equal(vm.runInContext('scrapeState.preview', env.box), null);
  assert.equal(vm.runInContext('scrapeState.inspect', env.box), null);
  assert.deepEqual(env.closedDetails, []);
});
