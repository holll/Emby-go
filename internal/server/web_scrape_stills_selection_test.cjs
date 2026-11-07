'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, 'web/app.js'), 'utf8');
const code = source.slice(source.indexOf('let scrapeState = null;'), source.indexOf('/* ------------------------------------------------------------ 独立翻译 */'));
const escCode = source.slice(source.indexOf('const esc = '), source.indexOf('const icon = '));
const version = 'abcdef0123456789'.repeat(4);
const inspect = () => ({ title: 'Movie', fields: [], overwrite: false, stills: [0, 1, 2].map(index => ({ index, preview: `/api/admin/scrape/image?kind=still&index=${index}&version=${version}` })) });
const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; };

// Same VM/fake-DOM approach as the existing scrape tests; only the used DOM surface is modeled.
function fixture(respond = async () => ({ title: 'Saved', images: 0 })) {
  const calls = [], toasts = [], refreshes = [];
  let active;
  function element(tag = 'div', attrs = '') {
    const node = { tag, id: '', dataset: {}, disabled: /\bdisabled\b/.test(attrs), checked: /\bchecked\b/.test(attrs), hidden: false, handlers: {}, children: [], html: '', textContent: '',
      addEventListener(event, handler) { this.handlers[event] = handler; },
      focus() { active = this; },
      click() { if (!this.disabled) return this.handlers.click?.(); },
      querySelector(selector) { return this.querySelectorAll(selector)[0] || null; },
      querySelectorAll(selector) {
        const children = this.children.flatMap(child => [child, ...child.querySelectorAll('*')]);
        return children.filter(child => selector.split(',').some(part => {
          part = part.trim();
          return part === '*' || (part[0] === '#' ? child.id === part.slice(1) : part[0] === '.' ? child.className?.split(' ').includes(part.slice(1)) : child.tag === part);
        }));
      },
      insertAdjacentHTML(_where, html) { this.html = html + this.html; this.children.unshift(...parse(html)); }
    };
    for (const [, key, value] of attrs.matchAll(/([\w-]+)="([^"]*)"/g)) {
      if (key.startsWith('data-')) node.dataset[key.slice(5)] = value;
      else node[key === 'class' ? 'className' : key] = value;
    }
    Object.defineProperty(node, 'innerHTML', { get() { return this.html; }, set(html) { this.html = html; this.children = parse(html); } });
    return node;
  }
  function parse(html) { return [...html.matchAll(/<(\w+)([^>]*)>/g)].map(([, tag, attrs]) => element(tag, attrs)); }
  const results = element(), body = element(), overlay = element();
  body.children = [element('input', 'id="scrape-query"'), element('button', 'id="scrape-auto-search"'), results];
  const nodes = { '#scrape-results': results, '#scrape-body': body, '#scrape-overlay': overlay };
  const box = vm.createContext({ URLSearchParams, pageGen: 3, detailGen: 9, pageItems() {},
    document: { querySelector: selector => nodes[selector] || null },
    api: async (url, options) => { calls.push({ url, payload: options?.body ? JSON.parse(options.body) : undefined }); return respond(url, options); },
    toast: (...args) => toasts.push(args), closeDetail() {}, refreshPage: (_render, gen) => refreshes.push(gen), overlayPop() {}, numberUnlessInTitle: () => ''
  });
  vm.runInContext(escCode + code, box);
  box.initialInspect = inspect();
  vm.runInContext(`scrapeState = { movieId: 7, provider: 'p', id: 'old', update: false, position: 0, searchSeq: 1, inspectSeq: 2, preview: { query: 'movie', candidates: [{ provider: 'p', id: 'old' }, { provider: 'p', id: 'new' }] }, inspect: initialInspect }; renderScrapePreview();`, box);
  const get = id => results.querySelector('#' + id);
  return { box, calls, toasts, refreshes, body, results, overlay, get, get active() { return active; },
    state: () => vm.runInContext('scrapeState', box),
    check(index, checked = true) { const node = get('scrape-still-' + index); node.checked = checked; node.handlers.change(); },
    submit: () => get('scrape-confirm').click()
  };
}

test('ordered unique payload, deselection compaction/reselection append, version and explicit false/zero', async () => {
  const env = fixture();
  env.check(2); env.check(0); env.check(2, false); env.check(2);
  assert.equal(env.get('scrape-still-order-0').textContent, '→ fanart1.jpg');
  assert.equal(env.get('scrape-still-order-2').textContent, '→ fanart2.jpg');
  assert.equal(env.get('scrape-stills-count').textContent, '已选 2 张');
  await env.submit();
  assert.deepEqual(env.calls[0].payload, { provider: 'p', id: 'old', overwrite: false, update: false, position: 0, stills: { version, indices: [0, 2] } });
});

test('empty/default and cleared selection omit stills completely', async () => {
  for (const clear of [false, true]) {
    const env = fixture();
    if (clear) { env.check(1); env.get('scrape-stills-clear').click(); }
    await env.submit();
    assert.equal(Object.hasOwn(env.calls[0].payload, 'stills'), false);
  }
});

test('native labeled controls retain preview version and escape all remote still values', () => {
  const env = fixture();
  assert.match(env.results.innerHTML, /<label class="scrape-still" for="scrape-still-0">/);
  assert.match(env.results.innerHTML, /type="checkbox" data-index="0"/);
  assert.match(env.results.innerHTML, /version=/);
  assert.match(env.results.innerHTML, /确认写入后才保存/);
  assert.match(env.results.innerHTML, /不会删除未涉及的旧尾图/);
  env.state().inspect.stills = [{ index: '<script>', preview: '" onerror="bad' }];
  vm.runInContext('renderScrapePreview()', env.box);
  assert.doesNotMatch(env.results.innerHTML, /<script>|src="" onerror=/);
  assert.match(env.results.innerHTML, /&lt;script&gt;/);
  assert.equal(env.results.querySelector('.scrape-still-check').disabled, true);
});

test('successful candidate switch and reread clear selection; failed switch preserves old binding and selection', async () => {
  let fail = true;
  const env = fixture(async () => { if (fail) throw Error('<offline>'); return inspect(); });
  env.check(2);
  await env.box.loadScrapeInspect('p', 'new', true, 1);
  assert.equal(env.state().id, 'old');
  assert.deepEqual(Array.from(env.state().selectedStills), [2]);
  assert.match(env.results.innerHTML, /&lt;offline&gt;/);
  fail = false;
  await env.box.loadScrapeInspect('p', 'new', false, 0);
  assert.equal(env.state().id, 'new');
  assert.deepEqual(Array.from(env.state().selectedStills), []);
  env.check(1);
  await env.box.loadScrapeInspect('p', 'new', false, 0);
  assert.deepEqual(Array.from(env.state().selectedStills), []);
});

test('new search clears selection and late inspect cannot rebind it', async () => {
  const old = deferred();
  const env = fixture(url => url.endsWith('/inspect') ? old.promise : Promise.resolve({ query: 'new', candidates: [] }));
  env.check(2);
  const pending = env.box.loadScrapeInspect('p', 'late');
  await env.box.searchScrapePreview(env.state(), { q: 'new' });
  old.resolve(inspect()); await pending;
  assert.equal(env.state().inspect, null);
  assert.deepEqual(Array.from(env.state().selectedStills), []);
  assert.match(env.results.innerHTML, /没有搜到候选/);
});

test('latest inspect response wins; closed/replaced session ignores late detail', async () => {
  const first = deferred(), second = deferred(); let count = 0;
  const env = fixture(() => (++count === 1 ? first : second).promise);
  const a = env.box.loadScrapeInspect('p', 'a'), b = env.box.loadScrapeInspect('p', 'b');
  second.resolve(inspect()); await b; env.check(1);
  first.resolve(inspect()); await a;
  assert.equal(env.state().id, 'b'); assert.deepEqual(Array.from(env.state().selectedStills), [1]);
  const late = deferred();
  const closing = fixture(() => late.promise);
  const pending = closing.box.loadScrapeInspect('p', 'old');
  closing.box.closeScrapePreview();
  vm.runInContext('scrapeState = { movieId: 8 };', closing.box);
  closing.results.innerHTML = 'new session'; late.resolve(inspect()); await pending;
  assert.equal(closing.results.innerHTML, 'new session');
});

test('confirmation busy disables forms/candidates/selection, blocks duplicate/programmatic actions, and restores on failure', async () => {
  const pending = deferred(); const env = fixture(() => pending.promise);
  env.check(2); const oldCheckbox = env.get('scrape-still-0');
  const confirmation = env.submit();
  assert.equal(env.state().busy, true);
  assert.equal(env.body.querySelector('#scrape-query').disabled, true);
  for (const node of env.results.querySelectorAll('input, button')) assert.equal(node.disabled, node.id !== 'scrape-cancel-btn', node.id || node.className);
  await env.get('scrape-confirm').handlers.click();
  oldCheckbox.checked = true; oldCheckbox.handlers.change();
  await env.box.searchScrapePreview(env.state()); await env.box.loadScrapeInspect('p', 'other');
  assert.equal(env.calls.length, 1); assert.deepEqual(Array.from(env.state().selectedStills), [2]);
  pending.reject(Error('network')); await confirmation;
  assert.equal(env.state().busy, false);
  assert.equal(env.body.querySelector('#scrape-query').disabled, false);
  assert.equal(env.get('scrape-still-0').disabled, false);
  assert.equal(env.get('scrape-confirm').disabled, false);
  assert.deepEqual(env.toasts, [['network', 'error']]);
});

for (const message of ['剧照列表已变化', '扫库正在进行中']) {
  test(`409 offers inline reread without inference/automatic retry: ${message}`, async () => {
    let conflict = true;
    const env = fixture(async () => { if (conflict) throw Object.assign(Error(message), { status: 409 }); return inspect(); });
    env.check(1); await env.submit();
    assert.equal(env.calls.length, 1);
    assert.match(env.results.innerHTML, /确认未完成，未自动重试/);
    assert.doesNotMatch(env.results.innerHTML, /列表已变化/);
    assert.equal(env.active, env.get('scrape-conflict-reload'));
    conflict = false;
    await env.get('scrape-conflict-reload').click();
    // The click intentionally launches the existing void async detail loader.
    await new Promise(resolve => setImmediate(resolve));
    assert.deepEqual(Array.from(env.state().selectedStills), []);
    assert.equal(env.calls.length, 2);
  });
}

for (const result of [{ title: 'Saved', images: 1, warnings: ['<bad>'] }, { title: 'Saved', images: 0, refresh_error: '<busy>' }]) {
  test('partial success replaces search UI, keeps old selection/write handlers inert', async () => {
    const env = fixture(async () => result); env.check(1);
    const oldConfirm = env.get('scrape-confirm'), oldCheck = env.get('scrape-still-0');
    await env.submit();
    assert.match(env.body.innerHTML, /NFO 已保存/);
    assert.doesNotMatch(env.body.innerHTML, /scrape-manual-search|scrape-still-check|scrape-confirm/);
    await oldConfirm.handlers.click(); oldCheck.handlers.change();
    assert.equal(env.calls.length, 1); assert.equal(env.state().inspect, null);
    assert.equal(env.active.id, 'scrape-done');
  });
}

for (const outcome of ['success', 'partial', 'error']) {
  test(`late confirmation ${outcome} cannot contaminate reopened session`, async () => {
    const request = deferred(), env = fixture(() => request.promise);
    const pending = env.submit(); env.box.closeScrapePreview();
    vm.runInContext('scrapeState = { movieId: 8 };', env.box); env.body.innerHTML = 'new session';
    if (outcome === 'error') request.reject(Error('old'));
    else request.resolve({ title: 'Old', images: 0, ...(outcome === 'partial' ? { warnings: ['old'] } : {}) });
    await pending;
    assert.equal(env.body.innerHTML, 'new session'); assert.deepEqual(env.toasts, []); assert.deepEqual(env.refreshes, []);
  });
}
