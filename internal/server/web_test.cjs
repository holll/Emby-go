const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, 'web/app.js'), 'utf8');

function section(start, end) {
  const from = source.indexOf(start);
  const to = source.indexOf(end, from);
  assert.ok(from >= 0 && to > from);
  return source.slice(from, to);
}
function sandbox(values) {
  return vm.createContext({ URLSearchParams, ...values });
}

test('媒体墙请求失败后停止自动加载，手工重试后恢复', async () => {
  let attempts = 0, retry;
  const more = { textContent: '', innerHTML: '', getBoundingClientRect: () => ({ top: 0 }),
    querySelector: () => ({ addEventListener: (_, fn) => { retry = fn; } }) };
  const state = { loading: false, failed: false, done: false, offset: 0, items: [], userdata: {} };
  const box = sandbox({ wallState: state, wallGen: 1, WALL_PAGE_SIZE: 100,
    window: { innerHeight: 800 }, document: { querySelector: id => id === '#wall-more' ? more : null },
    esc: String, empty: () => '', STATUS_TEXT: {}, api: async () => {
      attempts++;
      if (attempts === 1) throw Error('模拟500');
      return { items: [], total: 0 };
    } });
  vm.runInContext(section('function wallMaybeLoadMore()', '/* ------------------------------------------------------------ 详情抽屉'), box);
  await box.loadWallPage();
  assert.equal(attempts, 1);
  assert.equal(state.failed, true);
  box.wallMaybeLoadMore();
  assert.equal(attempts, 1);
  retry();
  await new Promise(setImmediate);
  assert.equal(attempts, 2);
  assert.equal(state.failed, false);
  assert.equal(state.done, true);
});

test('筛选的旧 libraries 响应不能覆盖最新筛选', async () => {
  const requests = [];
  const element = { addEventListener() {}, value: '' };
  const box = sandbox({ pageGen: 1, wallGen: 0, wallState: null,
    location: { search: '?status=manual', pathname: '/', hash: '#items' },
    content: { innerHTML: '' }, ENTITY_PARAMS: [], esc: String, icon: () => '',
    api: () => new Promise(resolve => requests.push(resolve)),
    document: { querySelector: () => element, querySelectorAll: () => [] },
    debounce: fn => fn, runProbeAll() {}, loadWallPage: async () => {} });
  vm.runInContext(section('async function pageItems()', '// reloadWall'), box);
  const first = box.pageItems();
  box.location.search = '?status=success';
  const second = box.pageItems();
  requests[1]({ items: [] });
  await second;
  const rendered = box.content.innerHTML;
  assert.equal(box.wallState.status, 'success');
  requests[0]({ items: [] });
  await first;
  assert.equal(box.wallState.status, 'success');
  assert.equal(box.content.innerHTML, rendered);
});

test('旧表单保存完成后不能刷新已经切走的页面', async () => {
  const box = sandbox({ pageGen: 3 });
  vm.runInContext(section('function refreshPage(', '/* ---------------------------------------------------------------- 总览'), box);
  let rendered = 0;
  let finish;
  const request = new Promise(resolve => { finish = resolve; });
  const captured = box.pageGen;
  const save = (async () => { await request; return box.refreshPage(() => { rendered++; }, captured); })();
  box.pageGen++;
  finish();
  await save;
  assert.equal(rendered, 0);
  box.refreshPage(() => { rendered++; }, box.pageGen);
  assert.equal(rendered, 1);
});

test('字段锁保存保留未知字段并按勾选解锁已知字段', () => {
  const box = sandbox({});
  vm.runInContext(section('const EDIT_LOCK_FIELDS =', 'async function openDetail('), box);
  const inputs = [{ dataset: { lockField: 'Name' }, checked: false },
    { dataset: { lockField: 'Genres' }, checked: true }];
  const fields = box.editLockFields(['Name', 'FutureField', 'People'], inputs);
  assert.deepEqual(Array.from(fields), ['FutureField', 'People', 'Genres']);
  assert.deepEqual(Array.from(box.editLockFields(['Name'], [])), []);
  assert.match(source, /locked_fields: editLockFields\(lockedFields,/);
  assert.match(source, /lock_data: document\.querySelector\('#e-lock-data'\)\.checked/);
});
