'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, 'web/app.js'), 'utf8');
const searchCode = source.slice(source.indexOf('async function searchScrapePreview('), source.indexOf('async function loadScrapeInspect('));
function fixture(api) {
  const body = { innerHTML: '' }, inspected = [];
  const state = { movieId: 7, searchSeq: 0, inspectSeq: 4, preview: {}, inspect: {}, provider: 'old', id: 'old' };
  const box = vm.createContext({ URLSearchParams, scrapeState: state, api, esc: value => String(value).replaceAll('<', '&lt;'),
    document: { querySelector(selector) { assert.equal(selector, '#scrape-results'); return body; } },
    loadScrapeInspect: async (...args) => { inspected.push(args); } });
  vm.runInContext(searchCode, box);
  return { box, state, body, inspected };
}
test('manual searches preserve exact query/URL/provider-ID and automatic search has no parameters', async () => {
  const calls = [];
  const env = fixture(async url => { calls.push(url); return { query: '', candidates: [] }; });
  for (const manual of [null, { q: 'ABP-123', metatube_id: '' }, { q: 'ABP-123', metatube_id: 'Provider1' },
    { q: '', metatube_id: 'Provider1:movie-id' }, { q: 'https://example.com/movie?id=a&lang=ja#detail', metatube_id: '' }]) {
    await env.box.searchScrapePreview(env.state, manual);
    const url = new URL(calls.at(-1), 'http://localhost');
    if (!manual) assert.equal(url.search, '');
    else for (const [key, value] of Object.entries(manual)) assert.equal(url.searchParams.get(key), value === '' ? null : value);
    assert.equal(env.state.inspect, null);
    assert.equal(env.state.id, '');
    assert.match(env.body.innerHTML, /没有搜到候选/);
  }
});
test('all-empty manual input remains an explicit request rather than automatic fallback', async () => {
  const calls = [];
  const env = fixture(async url => { calls.push(url); return { candidates: [] }; });
  await env.box.searchScrapePreview(env.state, { q: '', metatube_id: '' });
  const url = new URL(calls[0], 'http://localhost');
  assert.equal(url.searchParams.get('q'), '');
  assert.equal(url.searchParams.has('metatube_id'), false);
});
test('latest search wins and closed sessions ignore responses', async () => {
  const pending = [];
  const env = fixture(() => new Promise(resolve => pending.push(resolve)));
  const first = env.box.searchScrapePreview(env.state, { q: 'old' });
  const second = env.box.searchScrapePreview(env.state, { q: 'new' });
  assert.equal(env.state.inspectSeq, 6, 'search invalidates previous detail requests');
  pending[1]({ candidates: [{ provider: 'p', id: 'new' }], recommended: 0 }); await second;
  pending[0]({ candidates: [{ provider: 'p', id: 'old' }], recommended: 0 }); await first;
  assert.deepEqual(env.inspected, [['p', 'new']]);
  const closing = env.box.searchScrapePreview(env.state);
  env.box.scrapeState = null; env.body.innerHTML = 'closed';
  pending[2]({ candidates: [{ provider: 'p', id: 'closed' }] }); await closing;
  assert.equal(env.body.innerHTML, 'closed');
  assert.equal(env.inspected.length, 1);
});
test('search failure is escaped and leaves manual controls available for retry', async () => {
  let fail = true;
  const env = fixture(async () => { if (fail) throw Error('<offline>'); return { candidates: [], query: 'retry' }; });
  await env.box.searchScrapePreview(env.state, { q: '' });
  assert.match(env.body.innerHTML, /role="alert"/); assert.match(env.body.innerHTML, /&lt;offline>/);
  fail = false; await env.box.searchScrapePreview(env.state, { q: 'retry' });
  assert.match(env.body.innerHTML, /retry/);
  const open = source.slice(source.indexOf('async function openScrapePreview('), source.indexOf('async function searchScrapePreview('));
  assert.match(open, /<label>标题 \/ 番号 \/ URL<input/);
  assert.match(open, /<label>MetaTubeId<input/);
  assert.match(open, /addEventListener\('submit'/);
  assert.match(open, /id="scrape-results"/);
});
