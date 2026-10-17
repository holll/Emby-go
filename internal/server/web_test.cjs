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
    debounce: fn => fn, runProbeAll() {}, stopProbePolling() {}, startProbePolling() {}, loadWallPage: async () => {} });
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

function authBootSandbox(kind, outcome) {
  let stored = 'fixture-token', removed = 0, retry, rendered = 0;
  const redirects = [], requests = [];
  const content = { innerHTML: '' };
  const button = { disabled: false, addEventListener(type, fn) { if (type === 'click') retry = fn; } };
  const element = { addEventListener() {} };
  let first = true;
  const box = sandbox({
    token: stored, content, root: content,
    localStorage: { getItem: () => stored, removeItem: () => { stored = null; removed++; } },
    window: { location: { replace: path => redirects.push(path) }, addEventListener() {} },
    location: { hash: '#overview' },
    document: { querySelectorAll: () => [], querySelector: id => id === '#auth-root' ? content : id === '#auth-retry' ? button : element, addEventListener() {} },
    fetch: async (path, options) => {
      requests.push({ path, options });
      if (path === '/api/auth/status') return { ok: true, json: async () => ({ initialized: true }) };
      if (first) {
        first = false;
        if (outcome === 'network') throw new TypeError('offline');
        return { ok: false, status: outcome };
      }
      return { ok: true, status: 200 };
    },
    pages: { overview() {} }, page: () => { rendered++; }, api: async () => ({}),
    startScanPolling() {}, // auth-only fixture: scan observation is outside this fixture's scope.
    wallMaybeLoadMore() {}, closePlayer() {}, closeScrapePreview() {}, detailOverlay: element
  });
  if (kind === 'admin') vm.runInContext(source.slice(source.indexOf('async function boot()'), source.lastIndexOf('\nboot();')), box);
  else {
    const login = fs.readFileSync(path.join(__dirname, 'web/login.js'), 'utf8');
    vm.runInContext(login.replace(/\nbootLogin\(\);\s*$/, ''), box);
  }
  return { box, content, button, requests, redirects,
    boot: () => kind === 'admin' ? box.boot() : box.bootLogin(),
    get stored() { return stored; }, get removed() { return removed; }, get rendered() { return rendered; },
    retry: () => retry({ currentTarget: button }) };
}

for (const kind of ['admin', 'login']) {
  for (const outcome of [503, 500, 403, 'network']) {
    test(`${kind} 启动验证 ${outcome} 保留凭据，手工重试后恢复`, async () => {
      const env = authBootSandbox(kind, outcome);
      await env.boot();
      assert.equal(env.stored, 'fixture-token');
      assert.equal(env.removed, 0);
      assert.deepEqual(env.redirects, []);
      assert.equal(env.requests.length, 1, '故障时不能转去初始化或登录检查');
      assert.match(env.content.innerHTML, /role="alert"/);
      assert.match(env.content.innerHTML, /重试验证/);
      env.retry();
      assert.equal(env.button.disabled, true);
      await new Promise(setImmediate);
      assert.equal(env.requests.length, 2);
      assert.equal(env.requests[1].options.headers['X-Emby-Token'], 'fixture-token');
      assert.equal(env.removed, 0);
      if (kind === 'admin') assert.equal(env.rendered, 1);
      else assert.deepEqual(env.redirects, ['/admin']);
    });
  }
  test(`${kind} 启动验证仅 401 清除凭据`, async () => {
    const env = authBootSandbox(kind, 401);
    await env.boot();
    assert.equal(env.stored, null);
    assert.equal(env.removed, 1);
    if (kind === 'admin') assert.deepEqual(env.redirects, ['/']);
    else {
      assert.match(env.content.innerHTML, /欢迎回来/);
      assert.equal(env.requests[1].path, '/api/auth/status');
    }
  });
}

function loginForm(values, respond) {
  const focused = [], stored = [], redirects = [], calls = [];
  const errorNode = { textContent: '' };
  const button = { disabled: false, textContent: '登录管理后台' };
  const user = { value: values.Username || '', disabled: false, focus: () => focused.push('#auth-user') };
  const pw = { value: values.Pw || '', disabled: false, focus: () => focused.push('#auth-pw') };
  const form = {
    dataset: { mode: values.mode || 'login' },
    querySelectorAll: () => [user, pw, button],
    querySelector: id => id === '#auth-submit' ? button : null,
    addEventListener() {}
  };
  const rootNode = { innerHTML: '' };
  const box = sandbox({
    FormData: class { constructor() { return new Map(Object.entries(values)); } },
    document: { querySelector: selector => ({
      '.auth-error': errorNode, '#auth-form': form, '#auth-pw': pw, '#auth-user': user, '#auth-root': rootNode
    })[selector] ?? null },
    localStorage: { getItem: () => null, setItem: (key, value) => stored.push([key, value]) },
    fetch: async (path, options) => { calls.push({ path, options }); return respond(path); },
    window: { location: { replace: path => redirects.push(path) } }
  });
  const login = fs.readFileSync(path.join(__dirname, 'web/login.js'), 'utf8');
  vm.runInContext(login.replace(/\nbootLogin\(\);\s*$/, ''), box);
  return { box, calls, focused, stored, redirects, errorNode, button, rootNode };
}

test('登录失败只更新错误提示，保留已输入账号密码', async () => {
  const env = loginForm({ Username: 'admin', Pw: 'secret123' },
    () => ({ ok: false, status: 401, json: async () => ({ error: '账号或密码错误' }) }));
  await env.box.submit({ preventDefault() {}, target: {} }, false);
  assert.equal(env.errorNode.textContent, '账号或密码错误');
  assert.equal(env.button.disabled, false);
  assert.equal(env.button.textContent, '登录管理后台');
  assert.equal(env.rootNode.innerHTML, '', '错误时不得重建表单');
  assert.deepEqual(env.focused, ['#auth-pw']);
  assert.equal(env.calls.length, 1);
  assert.equal(env.calls[0].path, '/Users/AuthenticateByName');
  assert.equal(JSON.parse(env.calls[0].options.body).Pw, 'secret123');
  assert.equal(env.stored.length, 0);
});

test('初始化可携带 setup_code，未填写时不下发空字段', async () => {
  const withCode = loginForm({ mode: 'initialize', Username: 'admin', Pw: 'secret123', confirm: 'secret123', Code: 'setup-code' },
    () => ({ ok: true, status: 204, json: async () => ({}) }));
  await withCode.box.submit({ preventDefault() {}, target: {} }, true);
  assert.equal(JSON.parse(withCode.calls[0].options.body).Code, 'setup-code');

  const withoutCode = loginForm({ mode: 'initialize', Username: 'admin', Pw: 'secret123', confirm: 'secret123' },
    () => ({ ok: true, status: 204, json: async () => ({}) }));
  await withoutCode.box.submit({ preventDefault() {}, target: {} }, true);
  assert.equal('Code' in JSON.parse(withoutCode.calls[0].options.body), false);
});

test('初始化两次密码不一致时不发请求、保留输入并聚焦密码', async () => {
  const env = loginForm({ Username: 'admin', Pw: 'secret123', confirm: 'other', mode: 'initialize' },
    () => { throw new Error('密码不一致时不应请求'); });
  await env.box.submit({ preventDefault() {}, target: {} }, true);
  assert.equal(env.errorNode.textContent, '两次输入的密码不一致');
  assert.equal(env.calls.length, 0);
  assert.deepEqual(env.focused, ['#auth-pw']);
  assert.equal(env.rootNode.innerHTML, '');
});

test('登录成功保存 token 并跳转管理后台', async () => {
  const env = loginForm({ Username: 'admin', Pw: 'secret123' },
    () => ({ ok: true, status: 200, json: async () => ({ AccessToken: 'tok-1' }) }));
  await env.box.submit({ preventDefault() {}, target: {} }, false);
  assert.deepEqual(env.stored, [['emby_token', 'tok-1']]);
  assert.deepEqual(env.redirects, ['/admin']);
});

test('筛选、计划任务与编辑展开暴露可访问名称和状态', () => {
  for (const snippet of [
    'id="item-search" aria-label="搜索媒体"',
    'id="item-sort" aria-label="排序方式"',
    'id="item-lib" aria-label="媒体库"',
    'data-lib="" aria-pressed="${libraryID === \x27\x27}"',
    'data-status="${value}" aria-pressed="${status === value}"',
    'id="entity-clear" title="清除该筛选" aria-label="清除筛选"',
    'id="detail-edit-toggle" class="btn" aria-expanded="false" aria-controls="detail-edit"',
    "editToggle.setAttribute('aria-expanded', String(!editSection.hidden))",
    'aria-label="计划任务：${esc(TASK_TYPE_TEXT[item.type] || item.type)}"'
  ]) assert.ok(source.includes(snippet), `missing: ${snippet}`);
});

test('API 密钥只显示前缀，新密钥原文仅展示一次', () => {
  for (const snippet of [
    'let createdAPIKey = null;',
    '${esc(item.key_prefix || \'—\')}…',
    'data-key-delete="${esc(item.id)}"',
    'data-copy-created',
    'createdAPIKey = created.key || null;',
    '新密钥只显示这一次'
  ]) assert.ok(source.includes(snippet), `missing: ${snippet}`);
});

test('设置页提供会话期限说明与退出全部会话', () => {
  for (const snippet of [
    '登录会话有效期',
    's.session_ttl_days > 0 ? `${s.session_ttl_days} 天（使用中自动续期）` : \'永不过期\'',
    'id="logout-all" class="btn btn-danger"',
    "api('/access-tokens', { method: 'DELETE' })",
    '已退出 ${result.revoked} 个会话'
  ]) assert.ok(source.includes(snippet), `missing: ${snippet}`);
});

test('遮罩层级决定 Esc 目标，Tab 在顶层遮罩循环并恢复触发焦点', () => {
  const log = [];
  const focusable = name => ({ name, hidden: false, disabled: false, focus() { log.push(name); } });
  const trigger = focusable('card-trigger');
  const closeBtn = focusable('player-close');
  const secondBtn = focusable('player-second');
  const overlays = {};
  const items = { '#scrape-overlay': [], '#player-overlay': [closeBtn, secondBtn], '#detail-overlay': [] };
  function overlayEl(selector) {
    if (!overlays[selector]) {
      const buttons = items[selector] || [];
      overlays[selector] = {
        hidden: true,
        focus() { log.push('focus ' + selector); },
        querySelectorAll: () => buttons,
        contains: element => buttons.includes(element)
      };
    }
    return overlays[selector];
  }
  let active = null;
  const box = sandbox({
    document: {
      get activeElement() { return active; }, set activeElement(value) { active = value; },
      querySelector: selector => selector in items ? overlayEl(selector) : null,
      contains: () => true, addEventListener() {}
    }
  });
  vm.runInContext(section('/* 遮罩焦点：', '/* 遮罩焦点结束 */'), box);
  overlayEl('#detail-overlay').hidden = false;
  overlayEl('#player-overlay').hidden = false;
  assert.equal(box.topOverlaySelector(), '#player-overlay', '播放器在详情之上');
  overlayEl('#scrape-overlay').hidden = false;
  assert.equal(box.topOverlaySelector(), '#scrape-overlay', '刮削预览在最上层');
  overlayEl('#scrape-overlay').hidden = true;

  active = trigger;
  box.overlayPush('#detail-overlay');
  assert.equal(log.at(-1), 'focus #detail-overlay', '内容渲染前先聚焦遮罩容器');
  box.overlayPop('#detail-overlay');
  assert.equal(log.at(-1), 'card-trigger', '关闭后恢复触发元素');

  active = null;
  const tab = { key: 'Tab', shiftKey: false, prevented: false, preventDefault() { this.prevented = true; } };
  box.overlayTrapTab(tab);
  assert.equal(tab.prevented, true);
  assert.equal(log.at(-1), 'player-close');

  active = closeBtn;
  const inside = { key: 'Tab', shiftKey: false, prevented: false, preventDefault() { this.prevented = true; } };
  box.overlayTrapTab(inside);
  assert.equal(inside.prevented, false, '遮罩内正常 Tab 不拦截');

  const shift = { key: 'Tab', shiftKey: true, prevented: false, preventDefault() { this.prevented = true; } };
  box.overlayTrapTab(shift);
  assert.equal(shift.prevented, true);
  assert.equal(log.at(-1), 'player-second', 'Shift+Tab 从首项回绕到末项');
});

function recoveryPanel() {
  const error = { textContent: '' };
  let html = '', retry;
  return { hidden: true, error,
    get innerHTML() { return html; },
    set innerHTML(value) { html = value; retry = value.includes('data-recover-edit') ? { disabled: false, addEventListener(_, fn) { this.click = fn; } } : null; },
    querySelector: id => id === '[data-recover-edit]' ? retry : error };
}

test('编辑部分成功保留警告、禁止重发旧表单，只按摘要恢复', async () => {
  let submit, closed = 0, refreshed = 0;
  const requests = [], messages = [];
  const panel = recoveryPanel(), save = { disabled: false };
  const form = { addEventListener(_, fn) { submit = fn; }, querySelectorAll: () => [] };
  const box = sandbox({ m: { id: 42 }, lockedFields: [], gen: 1,
    document: { querySelector: id => id === '#detail-edit-form' ? form : id === '#detail-edit-save' ? save : id === '#detail-edit-recovery' ? panel : { value: 'Old form', checked: false } },
    api: async (path, options) => { requests.push({ path, options }); return requests.length === 1 ? { status: 'partial_success', manual_status: 'pending', recovery: { nfo_sha256: 'digest' } } : { status: 'success', indexed: true }; },
    toast: text => messages.push(text), isCurrent: () => true,
    editLockFields: () => [], closeDetail: () => { closed++; }, pageItems() {}, refreshPage: () => { refreshed++; } });
  vm.runInContext(section('function renderEditRecovery(', 'async function openDetail('), box);
  vm.runInContext(section("  const editForm = document.querySelector('#detail-edit-form');", '  // 实体跳转'), box);
  await submit({ preventDefault() {} });
  assert.equal(closed, 0); assert.equal(refreshed, 0); assert.equal(save.disabled, true);
  assert.match(panel.innerHTML, /role="alert"/); assert.match(panel.innerHTML, /标记也未保存/);
  assert.equal(messages.some(x => x.includes('归类为手动录入')), false);
  await submit({ preventDefault() {} }); assert.equal(requests.length, 1);
  await panel.querySelector('[data-recover-edit]').click();
  assert.equal(requests.length, 2); assert.equal(requests[1].path, '/items/42/recover-edit');
  assert.deepEqual(JSON.parse(requests[1].options.body), { nfo_sha256: 'digest' });
  assert.equal(closed, 1); assert.equal(refreshed, 1);
});

test('恢复网络失败可重试，再次部分成功不误报成功', async () => {
  const panel = recoveryPanel(); let attempts = 0, recovered = 0;
  const partial = { status: 'partial_success', manual_status: 'saved', recovery: { nfo_sha256: 'digest' } };
  const box = sandbox({ api: async () => { attempts++; if (attempts === 1) throw Error('network unavailable'); return attempts === 2 ? partial : { status: 'success' }; } });
  vm.runInContext(section('function renderEditRecovery(', 'async function openDetail('), box);
  box.renderEditRecovery(panel, 42, partial, () => { recovered++; }, () => true);
  await panel.querySelector('[data-recover-edit]').click();
  assert.equal(panel.error.textContent, 'network unavailable'); assert.equal(panel.querySelector('[data-recover-edit]').disabled, false);
  await panel.querySelector('[data-recover-edit]').click(); assert.equal(recovered, 0);
  await panel.querySelector('[data-recover-edit]').click(); assert.equal(recovered, 1);
});

test('旧抽屉恢复完成不能关闭或刷新新页面', async () => {
  let finish, current = true, recovered = 0;
  const panel = recoveryPanel(); const box = sandbox({ api: () => new Promise(resolve => { finish = resolve; }) });
  vm.runInContext(section('function renderEditRecovery(', 'async function openDetail('), box);
  box.renderEditRecovery(panel, 42, { recovery: { nfo_sha256: 'digest' } }, () => { recovered++; }, () => current);
  const pending = panel.querySelector('[data-recover-edit]').click();
  assert.equal(panel.querySelector('[data-recover-edit]').disabled, true);
  current = false; finish({ status: 'success' }); await pending; assert.equal(recovered, 0);
});

test('计划探测回显保留旧success范围，显式all不再用空串', () => {
  assert.match(source, /<option value="all">全部已有 NFO 的影片<\/option>/);
  for (const [params, expected] of [[{}, 'success'], [{ status: null }, 'success'], [{ status: '' }, 'success'], [{ status: 'all' }, 'all'], [{ status: 'manual' }, 'manual']]) {
    const status = { value: 'success' };
    const box = sandbox({ editing: true, params, document: { querySelector: id => id === '#s-status' ? status : {} } });
    vm.runInContext(section('  // 回填编辑中的参数。', '  syncParams();'), box);
    assert.equal(status.value, expected);
  }
});

test('新计划探测提交严格状态版本和显式all', async () => {
  let submit, saved;
  const fields = { '#s-lib': { value: '0' }, '#s-status': { value: 'all' }, '#s-only-missing': { checked: false }, '#s-name': { value: 'Scope' }, '#s-enabled': { checked: false }, '#sched-submit': { disabled: false }, '#sched-form': { addEventListener(_, fn) { submit = fn; } } };
  const box = sandbox({ typeEl: { value: 'probe' }, cronEl: { value: '0 3 * * *' }, editing: null, params: {}, paramsReadable: true, gen: 1,
    document: { querySelector: id => fields[id] }, api: async (_, options) => { saved = JSON.parse(options.body); }, toast() {}, refreshPage() {}, pageScheduled() {} });
  vm.runInContext(section('function scheduledFormParams(', 'async function pageScheduled('), box);
  vm.runInContext(section("  document.querySelector('#sched-form').addEventListener('submit'", "  document.querySelectorAll('[data-role]')"), box);
  await submit({ preventDefault() {} });
  assert.equal(saved.params.status, 'all'); assert.equal(saved.params.probe_status_contract, 'explicit-v1');
  assert.equal(saved.params.only_missing, false);
});

test('同类型计划参数合并保留隐藏limit和false，换类型不泄漏旧字段', () => {
  const box = sandbox({}); vm.runInContext(section('function scheduledFormParams(', 'async function pageScheduled('), box);
  for (const kind of ['probe', 'scrape', 'scrape_avatars']) {
    const original = { library_id: 7, limit: 50, only_missing: false, overwrite: false };
    const merged = box.scheduledFormParams(kind, { type: kind }, original, { library_id: 7, status: 'all', only_missing: false });
    assert.equal(merged.limit, 50); assert.equal(merged.overwrite, false); assert.equal(merged.only_missing, false);
    assert.deepEqual(original, { library_id: 7, limit: 50, only_missing: false, overwrite: false });
  }
  const changed = box.scheduledFormParams('scan', { type: 'probe' }, { limit: 50, status: 'all', only_missing: false, probe_status_contract: 'explicit-v1' }, { library_id: 3 });
  assert.deepEqual(JSON.parse(JSON.stringify(changed)), { library_id: 3 });
});

function scheduledSubmitFixture({ readable = true, library = '7', kind = 'probe', params = { limit: 50, status: 'manual', only_missing: false }, etag = '"sc1-fixture"', failure = 0 } = {}) {
  let submit; const calls = [], messages = [];
  const fields = { '#s-lib': { value: library }, '#s-status': { value: 'manual' }, '#s-only-missing': { checked: false }, '#s-name': { value: 'Renamed' }, '#s-enabled': { checked: false }, '#sched-submit': { disabled: false }, '#sched-form': { addEventListener(_, fn) { submit = fn; } } };
  fields['#sched-conflict'] = { hidden: true, textContent: '' };
  const box = sandbox({ typeEl: { value: kind }, cronEl: { value: '0 3 * * *' }, editing: { id: 42, type: 'probe', config_etag: etag }, params, paramsReadable: readable, gen: 1,
    document: { querySelector: id => fields[id] }, api: async (path, options) => { calls.push({ path, body: JSON.parse(options.body), headers: options.headers, method: options.method }); if (failure) { const error = Error('fixture conflict'); error.status = failure; throw error; } }, toast: message => messages.push(message), refreshPage() {}, pageScheduled() {}, scheduledEditId: 42 });
  vm.runInContext(section('function scheduledFormParams(', 'async function pageScheduled('), box);
  vm.runInContext(section("  document.querySelector('#sched-form').addEventListener('submit'", "  document.querySelectorAll('[data-role]')"), box);
  return { submit: () => submit({ preventDefault() {} }), calls, messages, fields, box };
}

test('真实计划提交只改名称仍保留隐藏limit，类型切换从新参数开始', async () => {
  const same = scheduledSubmitFixture(); await same.submit();
  assert.equal(same.calls[0].path, '/scheduled/42'); assert.equal(same.calls[0].body.params.limit, 50);
  assert.equal(same.calls[0].body.params.only_missing, false);
  const changed = scheduledSubmitFixture({ kind: 'scan' }); await changed.submit();
  assert.deepEqual(changed.calls[0].body.params, { library_id: 7 });
});

test('损坏参数或已消失媒体库不允许静默覆盖或变成全部', async () => {
  for (const options of [{ readable: false }, { library: '' }]) {
    const env = scheduledSubmitFixture(options); await env.submit(); assert.equal(env.calls.length, 0); assert.equal(env.messages.length, 1);
  }
  assert.match(source, /paramsReadable \? '' : '<p role="alert">/);
});

test('计划存量参数解析拒绝损坏JSON和非对象，不能按空配置保存', () => {
  for (const value of ['not json', 'null', '[]', 'false', '42']) {
    const box = sandbox({ editing: { params: value }, observed: null });
    vm.runInContext(section('  let params = {}, paramsReadable = true;', '  const libOptions =') + '\nobserved = { paramsReadable, params };', box);
    assert.equal(box.observed.paramsReadable, false);
  }
});

test('人工清空提交PATCH的空串、空数组、0和false，旧后端失败不回退PUT', async () => {
  assert.match(source, /aria-describedby="edit-clear-hint"/);
  assert.match(source, /留空的文本、列表和年份会清除/);
  for (const unsupported of [false, true]) {
    let submit, closed = 0, refreshed = 0;
    const calls = [], messages = [], save = { disabled: false };
    const form = { addEventListener(_, fn) { submit = fn; }, querySelectorAll: () => [] };
    const box = sandbox({ m: { id: 42 }, lockedFields: [], gen: 1,
      document: { querySelector: id => id === '#detail-edit-form' ? form : id === '#detail-edit-save' ? save : { value: '', checked: false } },
      api: async (path, options) => { calls.push({ path, options }); if (unsupported) throw Error('PATCH unsupported'); return { status: 'success' }; },
      toast: message => messages.push(message), isCurrent: () => true,
      editLockFields: () => [], closeDetail: () => { closed++; }, pageItems() {}, refreshPage: () => { refreshed++; } });
    vm.runInContext(section("  const editForm = document.querySelector('#detail-edit-form');", '  // 实体跳转'), box);
    await submit({ preventDefault() {} });
    assert.equal(calls.length, 1); assert.equal(calls[0].path, '/items/42'); assert.equal(calls[0].options.method, 'PATCH');
    assert.deepEqual(JSON.parse(calls[0].options.body), { title: '', number: '', original_title: '', plot: '', year: 0, genres: [], tags: [], studios: [], maker: '', label: '', locked_fields: [], lock_data: false });
    assert.equal(closed, unsupported ? 0 : 1); assert.equal(refreshed, unsupported ? 0 : 1);
    if (unsupported) { assert.equal(save.disabled, false); assert.equal(messages[0], 'PATCH unsupported'); }
  }
});

test('计划编辑携带原令牌，冲突保留输入并禁止旧快照重提，缺令牌拒绝覆盖', async () => {
  const env = scheduledSubmitFixture({ failure: 412 }); await env.submit();
  assert.equal(env.calls[0].method, 'PATCH'); assert.equal(env.calls[0].headers['If-Match'], '"sc1-fixture"');
  for (const status of [404, 405, 501]) {
    const unsupported = scheduledSubmitFixture({ failure: status }); await unsupported.submit();
    assert.equal(unsupported.calls.length, 1); assert.equal(unsupported.calls[0].method, 'PATCH');
    assert.equal(unsupported.fields['#s-name'].value, 'Renamed');
  }
  assert.equal(env.fields['#s-name'].value, 'Renamed'); assert.equal(env.fields['#sched-submit'].disabled, true);
  assert.equal(env.fields['#sched-conflict'].hidden, false); assert.match(env.fields['#sched-conflict'].textContent, /不会自动覆盖/);
  await env.submit(); assert.equal(env.calls.length, 1);
  const missing = scheduledSubmitFixture({ etag: null }); await missing.submit(); assert.equal(missing.calls.length, 0);
  const unavailable = scheduledSubmitFixture({ failure: 503 }); await unavailable.submit(); assert.equal(unavailable.fields['#sched-submit'].disabled, false);
});

test('412后其他行切换成功或失败、删除均不重绘表单或替换原令牌', async () => {
  for (const [role, fail] of [['toggle', false], ['toggle', true], ['del', false]]) {
    const env = scheduledSubmitFixture({ failure: 412 }); await env.submit();
    let refreshed = 0, removed = 0;
    const el = { dataset: { id: '99', role }, checked: true, addEventListener(_, fn) { this.action = fn; }, closest: () => ({ remove() { removed++; } }) };
    env.box.document.querySelectorAll = () => [el]; env.box.pageGen = 1;
    env.box.api = async () => { if (fail) throw Error('fixture unavailable'); };
    env.box.refreshPage = () => { refreshed++; }; env.box.confirm = () => true;
    vm.runInContext(section("  document.querySelectorAll('[data-role]').forEach", '\n}\n\n/* ---------------------------------------------------------------- 路由 */'), env.box);
    await el.action();
    assert.equal(refreshed, 0); assert.equal(env.fields['#s-name'].value, 'Renamed');
    assert.equal(env.fields['#sched-conflict'].hidden, false); assert.equal(env.box.editing.config_etag, '"sc1-fixture"');
    if (role === 'del') assert.equal(removed, 1); else assert.equal(el.checked, !fail);
    await env.submit(); assert.equal(env.calls.length, 1);
  }
});

test('通用API保留412状态和稳定冲突码', async () => {
  const box = sandbox({ token: 'fixture', FormData: class {}, fetch: async () => ({ ok: false, status: 412, json: async () => ({ error: 'changed', code: 'SCHEDULED_CONFIG_CHANGED' }) }) });
  vm.runInContext(section('async function api(', 'const STATUS_TEXT ='), box);
  await assert.rejects(box.api('/scheduled/42'), error => error.status === 412 && error.code === 'SCHEDULED_CONFIG_CHANGED' && error.message === 'changed');
});

async function wallKeyboardFixture() {
  const handlers = {}, calls = [];
  const element = { value: '', addEventListener() {} };
  const wall = { addEventListener(type, fn) { handlers[type] = fn; } };
  const box = sandbox({ pageGen: 1, wallGen: 0, wallState: null,
    location: { search: '', pathname: '/', hash: '#items' },
    content: { innerHTML: '' }, ENTITY_PARAMS: [], esc: String, icon: () => '',
    api: async (url, options) => { if (url === '/libraries') return { items: [] }; calls.push([url, options.method]); return { status: 'manual' }; },
    document: { querySelector: id => id === '#wall' ? wall : element, querySelectorAll: () => [] },
    debounce: fn => fn, runProbeAll() {}, stopProbePolling() {}, startProbePolling() {}, loadWallPage: async () => {},
    openDetail: item => calls.push(['detail', item.id]),
    openPlayer: item => calls.push(['play', item.id]),
    probeItem: id => calls.push(['probe', id]),
    confirm: () => true, toast() {}, STATUS_TEXT: {}, refreshPage() {} });
  vm.runInContext(section('async function pageItems()', '// reloadWall'), box);
  await box.pageItems();
  box.wallState.items = [{ id: 7 }];
  const card = { dataset: { play: '7' }, closest: selector => selector === '.wall-card' ? card : null };
  return { handlers, calls, card };
}
function wallKey(target, key, extra = {}) {
  return { target, key, defaultPrevented: false,
    preventDefault() { this.defaultPrevented = true; }, ...extra };
}

test('媒体卡片不再整体充当按钮：模板无 role=button/tabindex，标题是独立按钮', () => {
  const src = fs.readFileSync(path.join(__dirname, 'web/app.js'), 'utf8');
  assert.ok(!/<article class="wall-card" data-play="\$\{item\.id\}"[^>]*role=/.test(src), '卡片不得整体 role=button');
  assert.ok(!/<article class="wall-card" data-play="\$\{item\.id\}"[^>]*tabindex/.test(src), '卡片不得整体可聚焦');
  assert.match(src, /class="wall-title" data-open="\$\{item\.id\}"/, '标题必须是可聚焦按钮');
  assert.ok(!src.includes("document.querySelector('#wall').addEventListener('keydown'"), '卡片级 keydown 必须移除（避免嵌套交互）');
});

test('点击标题按钮与整卡都打开详情，内部按钮仍只执行自身动作', async () => {
  const env = await wallKeyboardFixture();
  // 标题按钮点击 → 详情。
  await env.handlers.click({
    target: { closest: selector => selector === '[data-open]' ? { dataset: { open: '7' } } : null },
    stopPropagation() {},
  });
  assert.deepEqual(env.calls, [['detail', 7]]);
  // 海报等空白区域点击 → 仍走整卡分支打开详情。
  env.calls.length = 0;
  await env.handlers.click({
    target: { closest: selector => selector === '.wall-card' ? env.card : null },
    stopPropagation() {},
  });
  assert.deepEqual(env.calls, [['detail', 7]]);
  // 内部动作按钮不会被详情分支截获。
  for (const [field, expected] of [
    ['probe', ['probe', '7']], ['reread', ['/items/7/reread', 'POST']],
    ['delete', ['/items/7', 'DELETE']], ['quickplay', ['play', 7]]
  ]) {
    env.calls.length = 0;
    const button = { dataset: { [field]: '7' }, closest(selector) {
      if (selector === '.wall-card') return env.card;
      return selector === `[data-${field}]` ? this : null;
    } };
    await env.handlers.click({ target: button, stopPropagation() {} });
    assert.deepEqual(env.calls, [expected], `${field}: 只执行自身动作`);
  }
});

// 静态色值护栏，不是完整CSS渲染器：不覆盖图片、滤镜、梯度、继承opacity或任意叠层。
function cssContrastPalette() {
  const css = fs.readFileSync(path.join(__dirname, 'web/style.css'), 'utf8');
  const root = css.match(/:root\s*\{([^}]+)\}/)[1];
  const vars = new Map(Array.from(root.matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g), m => [m[1], m[2].trim()]));
  function color(value, seen = []) {
    if (value.startsWith('--')) {
      assert.ok(!seen.includes(value), '颜色变量不能成环');
      assert.ok(vars.has(value), `缺少颜色变量 ${value}`);
      return color(vars.get(value), [...seen, value]);
    }
    const ref = value.match(/^var\((--[\w-]+)\)$/);
    if (ref) return color(ref[1], seen);
    if (/^#[0-9a-f]{6}$/i.test(value)) return [1, 3, 5].map(i => parseInt(value.slice(i, i + 2), 16) / 255).concat(1);
    const rgba = value.match(/^rgba\(([^)]+)\)$/);
    assert.ok(rgba, `未支持的颜色表达式 ${value}`);
    const parts = rgba[1].split(',').map(Number);
    assert.equal(parts.length, 4);
    assert.ok(parts.every((n, i) => Number.isFinite(n) && n >= 0 && n <= (i === 3 ? 1 : 255)));
    return parts.map((n, i) => i === 3 ? n : n / 255);
  }
  function over(fg, bg) {
    assert.equal(bg[3], 1, '背景必须已合成到不透明底色');
    return fg.slice(0, 3).map((n, i) => n * fg[3] + bg[i] * (1 - fg[3])).concat(1);
  }
  function luminance(rgb) {
    const linear = rgb.slice(0, 3).map(v => v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4);
    return linear[0] * 0.2126 + linear[1] * 0.7152 + linear[2] * 0.0722;
  }
  function ratio(fg, bg) {
    const a = luminance(over(fg, bg)), b = luminance(bg);
    return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
  }
  return { color, over, ratio };
}

test('WCAG静态颜色计算校验参考值与旧低透明度文字失败基线', () => {
  const { color, ratio } = cssContrastPalette();
  assert.equal(ratio(color('#ffffff'), color('#000000')), 21);
  assert.equal(ratio(color('#000000'), color('#ffffff')), 21);
  assert.ok(Math.abs(ratio(color('#777777'), color('#ffffff')) - 4.478089) < 0.00001);
  assert.ok(ratio(color('--white-38'), color('--bg')) < 4.5, '旧dim透明度不能满足正文门槛');
});

test('低强调与次要文字在固定暗色表面模型上均达到4.5比1', () => {
  const { color, over, ratio } = cssContrastPalette();
  const bg = color('--bg'), panel = over(color('--panel'), bg);
  const surfaces = new Map([
    ['page', bg], ['panel', panel], ['panel-hover', over(color('--surface-hover'), panel)],
    ['panel-secondary', over(color('--panel-2'), panel)], ['glass-on-page', over(color('--glass'), bg)],
    ['table-row', over(color('--surface'), panel)], ['fallback-panel', color('#101012')]
  ]);
  for (const token of ['--dim', '--muted']) {
    for (const [name, background] of surfaces) {
      const actual = ratio(color(token), background);
      assert.ok(actual >= 4.5, `${token} / ${name}: ${actual.toFixed(4)} < 4.5`);
    }
  }
});

function controlledClock() {
  const timers = new Map();
  let id = 0;
  return { timers,
    setTimeout: (fn, delay) => { timers.set(++id, { fn, delay }); return id; },
    clearTimeout: key => timers.delete(key),
    fire(delay) {
      const match = Array.from(timers).find(([, timer]) => timer.delay === delay);
      assert.ok(match, `missing timer ${delay}`);
      timers.delete(match[0]);
      return match[1].fn();
    }, has(delay) { return Array.from(timers.values()).some(t => t.delay === delay); } };
}

function progressFixture(api) {
  const clock = controlledClock();
  const rendered = [], unavailable = [], requests = [];
  const box = sandbox({ AbortController, setTimeout: clock.setTimeout, clearTimeout: clock.clearTimeout,
    api: (path, options) => { requests.push({ path, signal: options.signal }); return api(path, options); } });
  vm.runInContext(section('function startProgressPolling(', 'function stopScrapePolling()'), box);
  const stop = box.startProgressPolling('/fixture/progress', p => rendered.push(p), delay => unavailable.push(delay));
  return { ...clock, stop, rendered, unavailable, requests };
}

test('进度轮询等待在途请求完成才安排下次，只有明确running=false结束', async () => {
  const responses = [];
  const env = progressFixture(() => new Promise(resolve => responses.push(resolve)));
  const first = env.fire(0);
  assert.equal(env.requests.length, 1);
  assert.equal(env.has(1500), false);
  assert.equal(env.has(10000), true);
  responses.shift()({ running: true, done: 1 }); await first;
  assert.equal(env.has(1500), true);
  const second = env.fire(1500);
  assert.equal(env.requests.length, 2);
  responses.shift()({ running: false, done: 2 }); await second;
  assert.equal(env.rendered.length, 2);
  assert.equal(env.timers.size, 0);
});

test('进度503、网络失败及畸形响应退避重试，成功后恢复正常间隔', async () => {
  const error = Object.assign(new Error('fixture unavailable'), { status: 503 });
  const outcomes = [error, new TypeError('offline'), {}, null, error, { running: true }, { running: false }];
  const env = progressFixture(async () => { const value = outcomes.shift(); if (value instanceof Error) throw value; return value; });
  for (const delay of [0, 3000, 6000, 12000, 15000]) await env.fire(delay);
  assert.deepEqual(env.unavailable, [3000, 6000, 12000, 15000, 15000]);
  assert.deepEqual(env.rendered, []);
  await env.fire(15000);
  assert.equal(env.has(1500), true);
  await env.fire(1500);
  assert.equal(env.timers.size, 0);
  assert.equal(env.rendered.length, 2);
});

test('停止进度观察会中止请求，旧响应不能渲染或继续轮询', async () => {
  let finish;
  const env = progressFixture(() => new Promise(resolve => { finish = resolve; }));
  const pending = env.fire(0);
  env.stop();
  assert.equal(env.requests[0].signal.aborted, true);
  finish({ running: false }); await pending;
  assert.deepEqual(env.rendered, []);
  assert.deepEqual(env.unavailable, []);
  assert.equal(env.timers.size, 0);
});

test('进度请求超时中止后按未知状态重试，而非宣告任务结束', async () => {
  const env = progressFixture((_, { signal }) => new Promise((resolve, reject) => {
    signal.addEventListener('abort', () => reject(new Error('fixture timeout')), { once: true });
  }));
  const pending = env.fire(0);
  env.fire(10000); await pending;
  assert.equal(env.requests[0].signal.aborted, true);
  assert.deepEqual(env.rendered, []);
  assert.deepEqual(env.unavailable, [3000]);
  assert.equal(env.has(3000), true);
  env.stop(); assert.equal(env.timers.size, 0);
});

test('进度401/403停止观察并标示读取受限，不伪造后台终态', async () => {
  for (const status of [401, 403]) {
    const env = progressFixture(async () => { throw Object.assign(new Error('denied'), { status }); });
    await env.fire(0);
    assert.deepEqual(env.unavailable, [null]);
    assert.deepEqual(env.rendered, []);
    assert.equal(env.timers.size, 0);
  }
});

test('刮削观察错误可见且切页停止，旧代回调不能更改当前页面', async () => {
  let callbacks, stops = 0, renders = 0;
  const detail = { textContent: '', setAttribute(name, value) { this[name] = value; } };
  const panel = { hidden: true };
  const box = sandbox({ pageGen: 1, scrapeTimer: null,
    startProgressPolling: (path, progress, unavailable) => { callbacks = { path, progress, unavailable }; return () => { stops++; }; },
    renderScrapeProgress: () => { renders++; }, scrapeEls: () => ({ panel, detail }),
    stopProbePolling() {}, cancelProbeHide() {}, cancelScanHide() {}, setMeta() {}, content: {},
    skeletonPanel: () => '', pages: { fixture: async () => {} } });
  vm.runInContext(section('function stopScrapePolling()', '/* ------------------------------------------------------------ 单条刮削预览'), box);
  box.startScrapePolling();
  assert.equal(callbacks.path, '/scrape/progress');
  callbacks.unavailable(3000);
  assert.equal(panel.hidden, false); assert.equal(detail.role, 'status');
  assert.match(detail.textContent, /未确认.*3秒后自动重试/);
  callbacks.progress({ running: true }); assert.equal(renders, 1);
  vm.runInContext(section('async function page(name)', '/* ------------------------------------------------------------ 媒体信息探测'), box);
  await box.page('fixture');
  assert.equal(stops, 1);
  const previous = detail.textContent;
  callbacks.unavailable(null); callbacks.progress({ running: false });
  assert.equal(detail.textContent, previous); assert.equal(renders, 1);
});

test('通用API在401保留稳定状态供观察器停止，仍执行原登出跳转', async () => {
  let removed = 0, redirects = 0;
  const box = sandbox({ token: 'fixture', FormData: class {},
    fetch: async () => ({ status: 401 }), localStorage: { removeItem() { removed++; } },
    window: { location: { replace() { redirects++; } } } });
  vm.runInContext(section('async function api(', 'const STATUS_TEXT ='), box);
  await assert.rejects(box.api('/fixture'), e => e.status === 401 && e.message === '登录已失效');
  assert.equal(removed, 1); assert.equal(redirects, 1);
});

async function scrapePageFixture(progress, post = async () => ({})) {
  const timers = new Map(), nodes = new Map(), calls = [];
  let next = 0;
  function node(id) {
    if (!nodes.has(id)) nodes.set(id, { value: '0', checked: false, hidden: true,
      textContent: '', innerHTML: '', style: {}, classList: { toggle() {} }, handlers: {},
      setAttribute(name, value) { this[name] = value; },
      addEventListener(type, callback) { this.handlers[type] = callback; } });
    return nodes.get(id);
  }
  const content = node('content');
  const box = sandbox({ pageGen: 1, AbortController, content,
    document: { querySelector: node }, esc: String, icon: () => '', toast() {},
    setTimeout(fn, delay) { timers.set(++next, { fn, delay }); return next; },
    clearTimeout(id) { timers.delete(id); },
    api: async (path, options = {}) => {
      calls.push({ path, options });
      if (path === '/scrape/progress') return progress(path, options);
      if (options.method === 'POST') return post(path, options);
      if (path === '/libraries') return [];
      if (path === '/scrape/settings') return {};
      if (path.startsWith('/scrape/candidates?')) return { total: 0 };
      throw Error('unexpected API ' + path);
    }, stopProbePolling() {}, cancelProbeHide() {}, cancelScanHide() {}, setMeta() {}, skeletonPanel: () => '',
    pages: { fixture: async () => {} } });
  vm.runInContext(section('async function pageScrape()', '/* ------------------------------------------------------------ 单条刮削预览'), box);
  vm.runInContext(section('async function page(name)', '/* ------------------------------------------------------------ 媒体信息探测'), box);
  // 初始化不得等待一条裸 progress Promise；有限微任务后应已渲染 shell。
  let ready = false, failure;
  const initial = box.pageScrape().then(() => { ready = true; }, e => { ready = true; failure = e; });
  for (let i = 0; i < 12; i++) await Promise.resolve();
  assert.equal(ready, true, 'page initialization blocked on progress GET');
  assert.ifError(failure); await initial;
  return { box, calls, node, content, timers,
    fire(delay) {
      const entry = [...timers].find(([, timer]) => timer.delay === delay);
      assert.ok(entry, `missing timer ${delay}`); timers.delete(entry[0]); return entry[1].fn();
    } };
}

test('真实刮削页先渲染未知状态，首次503、网络或畸形进度均由同一观察器恢复', async () => {
  for (const failure of [Object.assign(new Error('unavailable'), { status: 503 }), new TypeError('offline'), {}]) {
    let attempts = 0;
    const env = await scrapePageFixture(async () => {
      if (attempts++ === 0) { if (failure instanceof Error) throw failure; return failure; }
      return { running: false, finished_at: 1, done: 2 };
    });
    assert.equal(env.calls.filter(c => c.path === '/scrape/progress').length, 0);
    assert.equal(env.node('#scrape-progress').hidden, false);
    assert.match(env.node('#scrape-progress-detail').textContent, /正在读取.*未确认/);
    await env.fire(0);
    assert.match(env.node('#scrape-progress-detail').textContent, /未确认.*3秒/);
    assert.ok(env.calls.find(c => c.path === '/scrape/progress').options.signal);
    await env.fire(3000);
    assert.equal(env.node('#scrape-progress-title').textContent, '刮削结束');
    assert.equal(env.timers.size, 0);
  }
});

test('真实刮削页首次401或403停止观察而非吞错再开一轮', async () => {
  for (const status of [401, 403]) {
    const env = await scrapePageFixture(async () => { throw Object.assign(new Error('denied'), { status }); });
    await env.fire(0);
    assert.match(env.node('#scrape-progress-detail').textContent, /读取受限.*未确认/);
    assert.equal(env.calls.filter(c => c.path === '/scrape/progress').length, 1);
    assert.equal(env.timers.size, 0);
  }
});

test('真实刮削页首次悬挂请求也有软超时，随后退避恢复', async () => {
  let attempts = 0, signal;
  const env = await scrapePageFixture((_, options) => {
    signal = options.signal;
    if (attempts++ > 0) return { running: false, finished_at: 1 };
    return new Promise((resolve, reject) => signal.addEventListener('abort', () => reject(Error('timeout')), { once: true }));
  });
  const pending = env.fire(0); env.fire(10000); await pending;
  assert.equal(signal.aborted, true);
  assert.match(env.node('#scrape-progress-detail').textContent, /未确认.*3秒/);
  await env.fire(3000); assert.equal(env.timers.size, 0);
});

test('真实刮削页切走或同页刷新停止旧首读，晚回不渲染或复活', async () => {
  for (const refresh of [false, true]) {
    let resolve;
    const env = await scrapePageFixture(() => new Promise(done => { resolve = done; }));
    const pending = env.fire(0);
    const old = env.calls.find(c => c.path === '/scrape/progress').options.signal;
    if (refresh) await env.box.pageScrape(); else await env.box.page('fixture');
    assert.equal(old.aborted, true);
    const text = env.node('#scrape-progress-detail').textContent;
    resolve({ running: false, finished_at: 1 }); await pending;
    assert.equal(env.node('#scrape-progress-detail').textContent, text);
    assert.equal(env.timers.size, refresh ? 1 : 0);
    env.box.stopScrapePolling(); assert.equal(env.timers.size, 0);
  }
});

test('真实刮削页两个启动POST晚回不跨导航重启，当前页启动只替换一个观察器', async () => {
  for (const button of ['#scrape-start', '#avatar-start']) {
    let finish;
    const env = await scrapePageFixture(async () => ({ running: true }), () => new Promise(resolve => { finish = resolve; }));
    const pending = env.node(button).handlers.click();
    await env.box.page('fixture'); await env.box.pageScrape();
    const timersBefore = [...env.timers.keys()];
    finish({}); await pending;
    assert.deepEqual([...env.timers.keys()], timersBefore, 'old POST replaced new observer');
    const current = env.node(button).handlers.click(); finish({}); await current;
    assert.equal(env.timers.size, 1);
    assert.ok([...env.timers.values()].every(t => t.delay === 0));
    env.box.stopScrapePolling(); assert.equal(env.timers.size, 0);
  }
});


// 第22批：只运行真实函数的VM，不启动服务/浏览器；时钟与第19批串行轮询共用。
function probePageFixture(progress = async () => ({ running: false }), post = async () => ({ total: 2 })) {
  const clock = controlledClock(), nodes = new Map(), calls = [], toasts = [];
  function node(id) {
    if (!nodes.has(id)) nodes.set(id, { value: '', disabled: false, hidden: true, textContent: '', innerHTML: '',
      style: {}, handlers: {}, dataset: {}, classList: { toggle() {} },
      querySelector: () => node(id + ':span'), setAttribute(name, value) { this[name] = value; },
      addEventListener(type, fn) { this.handlers[type] = fn; } });
    return nodes.get(id);
  }
  const status = node('status-pill'); status.dataset.status = 'success';
  const content = node('content');
  const location = { search: '', pathname: '/', hash: '#items' };
  const box = sandbox({ AbortController, pageGen: 1, wallGen: 0, wallState: null,
    setTimeout: clock.setTimeout, clearTimeout: clock.clearTimeout, content, location,
    document: { querySelector: node, querySelectorAll: selector => selector === '.pill[data-status]' ? [status] : [] },
    history: { pushState(_, __, url) { location.search = new URL(url, 'http://fixture').search; },
      replaceState(_, __, url) { location.search = new URL(url, 'http://fixture').search; } }, URL,
    ENTITY_PARAMS: [], esc: String, icon: () => '', debounce: fn => fn,
    loadWallPage: async () => {}, toast: (...args) => toasts.push(args),
    stopScrapePolling() {}, cancelScanHide() {}, setMeta() {}, skeletonPanel: () => 'new page',
    pages: { fixture: async () => {} },
    api: (path, options = {}) => {
      calls.push({ path, options });
      if (path === '/libraries') return Promise.resolve({ items: [] });
      if (path === '/probe/media/progress') return progress(path, options);
      if (path === '/probe/media' && options.method === 'POST') return post(path, options);
      throw Error('unexpected API ' + path);
    } });
  vm.runInContext(section('function refreshPage(', '/* ---------------------------------------------------------------- 总览'), box);
  vm.runInContext(section('function startProgressPolling(', 'function stopScrapePolling()'), box);
  vm.runInContext(section('async function page(name)', '/* ------------------------------------------------------------ 媒体信息探测'), box);
  vm.runInContext(section('let probeTimer = null;', 'async function probeItem('), box);
  vm.runInContext(section('async function pageItems()', '// reloadWall'), box);
  vm.runInContext(section('function reloadWall()', '// wallMaybeLoadMore'), box);
  return { ...clock, box, node, calls, toasts, status, content,
    gets: () => calls.filter(c => c.path === '/probe/media/progress'),
    snapshot: () => JSON.stringify({ html: content.innerHTML, button: node('#probe-media').disabled,
      title: node('#probe-progress-title').textContent, detail: node('#probe-progress-detail').textContent,
      hidden: node('#probe-progress').hidden, toasts, timers: [...clock.timers.keys()] }) };
}
const probeTerminal = { running: false, finished_at: 1, total: 2, done: 2, success: 2, skipped: 0, failed: 0 };

async function probeReady(env) {
  let ready = false;
  const initial = env.box.pageItems().then(() => { ready = true; });
  for (let i = 0; i < 12; i++) await Promise.resolve();
  assert.equal(ready, true, 'pageItems must not await bare probe GET');
  await initial;
  assert.equal(env.node('#probe-media').disabled, true, 'initial read must forbid duplicate start');
  assert.equal(typeof env.node('#probe-media').handlers.click, 'function');
}

test('真实媒体墙首次503/network/畸形恢复只观察GET，未知不释放按钮或伪造终态', async () => {
  for (const failure of [Object.assign(Error('503'), { status: 503 }), new TypeError('offline'), {}, null, { running: 'false' }]) {
    let attempts = 0;
    const env = probePageFixture(async () => {
      if (attempts++ === 0) { if (failure instanceof Error) throw failure; return failure; }
      return probeTerminal;
    });
    await probeReady(env); assert.equal(env.gets().length, 0);
    await env.fire(0);
    assert.equal(env.node('#probe-progress').hidden, false);
    assert.equal(env.node('#probe-progress-detail').role, 'status');
    assert.match(env.node('#probe-progress-title').textContent, /状态未确认/);
    assert.match(env.node('#probe-progress-detail').textContent, /未确认.*3秒/);
    assert.equal(env.node('#probe-media').disabled, true); assert.deepEqual(env.toasts, []);
    assert.equal(env.calls.some(c => c.options.method === 'POST'), false);
    await env.fire(3000);
    assert.equal(env.node('#probe-media').disabled, false);
    assert.equal(env.node('#probe-progress-title').textContent, '探测完成');
    assert.deepEqual(env.toasts, [], 'history terminal after recovery is not a newly completed job');
    env.box.stopProbePolling(); assert.equal(env.timers.size, 0);
  }
});

test('真实媒体墙软超时未知后退避恢复，不伪造后台失败', async () => {
  let attempts = 0;
  const env = probePageFixture((_, { signal }) => {
    if (attempts++ > 0) return Promise.resolve({ running: false });
    return new Promise((resolve, reject) => signal.addEventListener('abort', () => reject(Error('timeout')), { once: true }));
  });
  await probeReady(env);
  const pending = env.fire(0); env.fire(10000); await pending;
  assert.equal(env.gets()[0].options.signal.aborted, true);
  assert.match(env.node('#probe-progress-detail').textContent, /未确认.*3秒/);
  assert.equal(env.node('#probe-media').disabled, true); assert.deepEqual(env.toasts, []);
  await env.fire(3000);
  assert.equal(env.node('#probe-media').disabled, false); assert.equal(env.timers.size, 0);
});

test('probe wrapper权限401/403停止且保持禁启动，明确空闲与历史终态不toast', async () => {
  for (const status of [401, 403]) {
    const env = probePageFixture(async () => { throw Object.assign(Error('denied'), { status }); });
    await probeReady(env); await env.fire(0);
    assert.match(env.node('#probe-progress-detail').textContent, /读取受限.*未确认/);
    assert.equal(env.node('#probe-media').disabled, true);
    assert.equal(env.timers.size, 0); assert.equal(env.gets().length, 1); assert.deepEqual(env.toasts, []);
  }
  for (const terminal of [{ running: false }, probeTerminal]) {
    const env = probePageFixture(async () => terminal);
    await probeReady(env); await env.fire(0);
    assert.equal(env.node('#probe-media').disabled, false); assert.deepEqual(env.toasts, []);
    env.box.stopProbePolling(); assert.equal(env.timers.size, 0);
  }
});

test('probe观察到running后明确终态才通知，失败示例/中止保留真实含义', async () => {
  for (const extra of [{}, { failed: 1, failures: ['fixture failure'] }, { cancelled: true }]) {
    let attempts = 0;
    const env = probePageFixture(async () => attempts++ ? { ...probeTerminal, ...extra } : { running: true, total: 2, done: 1 });
    await probeReady(env); await env.fire(0);
    assert.equal(env.node('#probe-media').disabled, true); assert.deepEqual(env.toasts, []);
    await env.fire(1500);
    assert.equal(env.node('#probe-media').disabled, false);
    assert.match(env.toasts[0][0], extra.cancelled ? /探测已中止/ : /探测完成/);
    assert.equal(env.toasts.length, extra.failed ? 2 : 1);
    if (extra.failed) { assert.equal(env.toasts[0][1], 'error'); assert.match(env.toasts[1][0], /fixture failure/); assert.equal(env.timers.size, 0); }
    env.box.stopProbePolling(); assert.equal(env.timers.size, 0);
  }
});

test('probe重复start只有一条活跃观察，stop后晚成功或晚失败不修改DOM不复活', async () => {
  for (const fail of [false, true]) {
    const replies = [];
    const env = probePageFixture((_, options) => new Promise((resolve, reject) => replies.push({ resolve, reject, signal: options.signal })));
    await probeReady(env); const old = env.fire(0);
    env.box.startProbePolling(); env.box.startProbePolling();
    assert.equal(replies[0].signal.aborted, true); assert.equal(env.timers.size, 1);
    const current = env.fire(0); assert.equal(env.gets().length, 2);
    const before = env.snapshot();
    if (fail) replies[0].reject(Error('late')); else replies[0].resolve(probeTerminal);
    await old; assert.equal(env.snapshot(), before);
    env.box.stopProbePolling(); const stopped = env.snapshot();
    if (fail) replies[1].reject(Error('late')); else replies[1].resolve(probeTerminal);
    await current; assert.equal(env.snapshot(), stopped); assert.equal(env.timers.size, 0);
  }
});

test('真实pageItems同页状态筛选和page导航中止旧GET，晚响应不跨代渲染', async () => {
  for (const navigation of [false, true]) {
    let resolve;
    const env = probePageFixture(() => new Promise(done => { resolve = done; }));
    await probeReady(env); const pending = env.fire(0), gen = env.box.pageGen;
    if (navigation) await env.box.page('fixture');
    else { env.status.handlers.click(); for (let i = 0; i < 12; i++) await Promise.resolve(); }
    assert.equal(env.gets()[0].options.signal.aborted, true);
    assert.equal(env.box.pageGen, navigation ? gen + 1 : gen);
    if (!navigation) { assert.equal(env.box.wallState.status, 'success'); assert.equal(env.box.wallGen, 2); }
    const before = env.snapshot(); resolve(probeTerminal); await pending;
    assert.equal(env.snapshot(), before);
    assert.equal(env.timers.size, navigation ? 0 : 1);
    env.box.stopProbePolling(); assert.equal(env.timers.size, 0);
  }
});

test('真实搜索reloadWall换wallGen仍须恢复探测观察而非静默忽略', async () => {
  const env = probePageFixture(async () => probeTerminal);
  await probeReady(env);
  env.node('#item-search').value = 'fixture';
  env.node('#item-search').handlers.input();
  assert.equal(env.box.wallState.search, 'fixture'); assert.equal(env.box.wallGen, 2);
  await env.fire(0);
  assert.equal(env.node('#probe-media').disabled, false, 'search changed wallGen but stranded probe observer');
  assert.equal(env.node('#probe-progress-title').textContent, '探测完成');
  env.box.stopProbePolling(); assert.equal(env.timers.size, 0);
});

test('probe POST旧成功和旧失败同时防pageGen/wallGen，不能替换新观察或DOM', async () => {
  for (const generation of ['page', 'wall']) for (const fail of [false, true]) {
    let resolve, reject;
    const env = probePageFixture(async () => ({ running: true }), () => new Promise((a, b) => { resolve = a; reject = b; }));
    await probeReady(env); const oldGet = env.fire(0); await oldGet;
    const pending = env.node('#probe-media').handlers.click();
    assert.equal(env.timers.size, 0, 'POST must stop previous observer');
    const oldPageGen = env.box.pageGen, oldWallGen = env.box.wallGen;
    if (generation === 'page') {
      await env.box.page('fixture');
      env.box.startProbePolling();
      assert.equal(env.box.wallGen, oldWallGen, 'isolate pageGen guard from wallGen');
    } else {
      await env.box.pageItems();
      assert.equal(env.box.pageGen, oldPageGen, 'isolate wallGen guard from pageGen');
    }
    const before = env.snapshot();
    if (fail) reject(Object.assign(Error('old denied'), { status: 403 })); else resolve({ total: 99 });
    await pending; assert.equal(env.snapshot(), before);
    assert.equal(env.calls.filter(c => c.options.method === 'POST').length, 1);
    env.box.stopProbePolling(); assert.equal(env.timers.size, 0);
  }
});

test('probe当前POST成功确认后即使首读终态也通知，无total不再观察且释放', async () => {
  for (const response of [{ total: 2 }, { total: 0 }, {}]) {
    const env = probePageFixture(async () => probeTerminal, async () => response);
    await probeReady(env); await env.node('#probe-media').handlers.click();
    const post = env.calls.find(c => c.options.method === 'POST');
    assert.equal(post.path, '/probe/media'); assert.deepEqual(JSON.parse(post.options.body), { only_missing: true });
    if (response.total) {
      assert.equal(env.timers.size, 1); assert.equal(env.node('#probe-media').disabled, true);
      await env.fire(0); assert.match(env.toasts[1][0], /探测完成/);
    } else {
      assert.equal(env.timers.size, 0); assert.equal(env.node('#probe-progress').hidden, true);
      assert.equal(env.gets().length, 0); assert.match(env.toasts[0][0], /没有可探测/);
    }
    assert.equal(env.node('#probe-media').disabled, false);
    env.box.stopProbePolling(); assert.equal(env.timers.size, 0);
  }
});

test('probe当前POST503/network失败重新观察，不宣称后台失败终态；401403受限停读', async () => {
  for (const status of [503, undefined, 401, 403]) {
    const error = Object.assign(Error('submit uncertain'), { status });
    let attempts = 0;
    const env = probePageFixture(async () => attempts++ ? probeTerminal : { running: true }, async () => { throw error; });
    await probeReady(env); await env.node('#probe-media').handlers.click();
    assert.equal(env.node('#probe-media').disabled, true);
    assert.equal(env.node('#probe-progress-title').textContent, '探测状态未确认');
    assert.ok(env.toasts.every(([text]) => !/探测完成|探测已中止/.test(text)));
    if (status === 401 || status === 403) {
      assert.equal(env.timers.size, 0); assert.match(env.node('#probe-progress-detail').textContent, /读取受限/);
      assert.equal(env.gets().length, 0);
    } else {
      assert.equal(env.timers.size, 1); await env.fire(0);
      assert.equal(env.node('#probe-media').disabled, true);
      await env.fire(1500); assert.equal(env.node('#probe-media').disabled, false);
      assert.match(env.toasts.at(-1)[0], /探测完成/);
    }
    env.box.stopProbePolling(); assert.equal(env.timers.size, 0);
  }
});

test('POST结果不明后首读历史终态只确认空闲，不伪造本轮完成通知', async () => {
  const env = probePageFixture(async () => probeTerminal, async () => { throw Error('network uncertain'); });
  await probeReady(env); await env.node('#probe-media').handlers.click();
  await env.fire(0);
  assert.equal(env.node('#probe-media').disabled, false);
  assert.deepEqual(env.toasts, [['network uncertain', 'error']]);
  env.box.stopProbePolling(); assert.equal(env.timers.size, 0);
});

test('旧搜索延时在导航或同页DOM重建后不能改URL或复活探测观察', async () => {
  for (const mode of ['page', 'replacement']) {
    const env = probePageFixture();
    let delayed;
    env.box.debounce = fn => { delayed = fn; return () => {}; };
    await probeReady(env);
    const stale = delayed, oldInput = env.node('#item-search');
    oldInput.value = 'stale search';
    if (mode === 'page') await env.box.page('fixture');
    else {
      const replacement = env.node('#replacement-search');
      env.box.document.querySelector = id => id === '#item-search' ? replacement : env.node(id);
      await env.box.pageItems();
    }
    const before = env.snapshot(), search = env.box.location.search, calls = env.calls.length;
    stale();
    assert.equal(env.snapshot(), before);
    assert.equal(env.box.location.search, search);
    assert.equal(env.calls.length, calls);
    env.box.stopProbePolling();
    assert.equal(env.timers.size, 0);
  }
});

test('boot不得裸读probe进度，恢复入口只在媒体墙DOM/button创建后', () => {
  assert.doesNotMatch(section('async function boot()', '\nboot();'), /api\(['"]\/probe\/media\/progress['"]/);
  const items = section('async function pageItems()', '// reloadWall');
  assert.ok(items.indexOf('stopProbePolling();') < items.indexOf("await api('/libraries')"));
  assert.ok(items.indexOf('startProbePolling();') > items.indexOf("probeButton.addEventListener('click', runProbeAll)"));
});

// 第23批：实际扫描函数 + 串行 helper；只模拟DOM/API/时钟，不启动服务器或浏览器。
function scanFixture(progress = async () => ({ running: false }), post = async () => ({ success: 2, pending: 1 })) {
  const clock = controlledClock(), nodes = new Map(), calls = [], toasts = [];
  let reloads = 0;
  function node(id) {
    if (!nodes.has(id)) nodes.set(id, { hidden: true, disabled: false, textContent: '', innerHTML: '',
      style: {}, handlers: {}, dataset: {}, classList: { toggle() {} },
      querySelector: () => node(id + ':span'), setAttribute(name, value) { this[name] = value; },
      addEventListener(type, fn) { this.handlers[type] = fn; } });
    return nodes.get(id);
  }
  const box = sandbox({ AbortController, pageGen: 1, wallGen: 1, content: node('content'),
    setTimeout: clock.setTimeout, clearTimeout: clock.clearTimeout,
    location: { hash: '#items' }, document: { querySelector: node, querySelectorAll: () => [] },
    pageItems() {}, pages: { fixture: async () => {} }, reloadWall() { reloads++; },
    toast: (...args) => toasts.push(args), stopScrapePolling() {}, stopProbePolling() {},
    cancelProbeHide() {}, setMeta() {}, skeletonPanel: () => 'page shell',
    api: (path, options = {}) => {
      calls.push({ path, options });
      if (options.method === 'POST') return post(path, options);
      assert.equal(path, '/scan/progress'); return progress(path, options);
    } });
  box.pages.items = box.pageItems;
  vm.runInContext(section('function startProgressPolling(', 'function stopScrapePolling()'), box);
  vm.runInContext(section('/* ---------------------------------------------------------------- 扫描进度 */', 'async function boot()'), box);
  vm.runInContext(section('async function page(name)', '/* ------------------------------------------------------------ 媒体信息探测'), box);
  return { ...clock, box, node, calls, toasts, get reloads() { return reloads; },
    gets: () => calls.filter(c => c.path === '/scan/progress'),
    posts: () => calls.filter(c => c.options.method === 'POST'),
    snapshot: () => JSON.stringify({ hidden: node('#scan-progress').hidden, title: node('#scan-progress-title').textContent,
      detail: node('#scan-progress-detail').textContent, disabled: node('#scan').disabled, toasts, timers: [...clock.timers.keys()] }) };
}
const scanTerminal = { running: false, finished_at: 1, total: 3, done: 3, success: 2, pending: 1 };

test('扫描所有入口在状态未知或正在运行时拒绝重复提交，明确空闲后才允许', async () => {
  for (const denied of [false, true]) {
    let reads = 0, writes = 0;
    const env = scanFixture(async () => reads++ ? scanTerminal : { running: true }, async () => {
      if (writes++ === 0) throw Object.assign(Error('uncertain'), { status: denied ? 403 : undefined });
      return { success: 1, pending: 0 };
    });
    await env.box.runScan();
    await env.box.runScan(7); // 媒体库/计划页行内入口不是被禁用的全局按钮。
    assert.equal(env.posts().length, 1, 'unconfirmed state must gate every local entry');
    if (denied) {
      assert.equal(env.timers.size, 0); env.box.startScanPolling();
    }
    await env.fire(0); await env.box.runScan(8);
    assert.equal(env.posts().length, 1, 'confirmed running must gate every local entry');
    await env.fire(1500); assert.equal(env.node('#scan').disabled, false);
    await env.box.runScan(9); assert.equal(env.posts().length, 2);
    assert.equal(env.posts()[1].path, '/scan?library_id=9');
    env.box.stopScanPolling(); assert.equal(env.timers.size, 0);
  }
});

test('扫描实际wrapper首GET失败退避恢复，仅明确终态释放且历史完成不toast', async () => {
  for (const failure of [Object.assign(Error('503'), { status: 503 }), new TypeError('offline'), {}, null, { running: 'false' }]) {
    let attempts = 0;
    const env = scanFixture(async () => { if (attempts++ === 0) { if (failure instanceof Error) throw failure; return failure; } return scanTerminal; });
    env.box.startScanPolling(); assert.equal(env.node('#scan').disabled, true);
    assert.equal(env.gets().length, 0); await env.fire(0);
    assert.equal(env.node('#scan-progress').hidden, false);
    assert.match(env.node('#scan-progress-title').textContent, /状态未确认/);
    assert.match(env.node('#scan-progress-detail').textContent, /未确认.*3秒/);
    assert.equal(env.node('#scan-progress-detail').role, 'status');
    assert.equal(env.node('#scan').disabled, true); assert.equal(env.posts().length, 0);
    await env.fire(3000); assert.equal(env.node('#scan').disabled, false);
    assert.match(env.node('#scan-progress-title').textContent, /扫描完成/);
    assert.deepEqual(env.toasts, []); assert.equal(env.has(4000), true);
    await env.fire(4000); assert.equal(env.node('#scan-progress').hidden, true); assert.equal(env.timers.size, 0);
  }
});

test('扫描实际wrapper多次GET错误指数退避到15秒，running恢复1500ms串行观察', async () => {
  const responses = [Error('offline'), Error('offline'), Error('offline'), Error('offline'), { running: true }, { running: false }];
  const env = scanFixture(async () => { const r = responses.shift(); if (r instanceof Error) throw r; return r; });
  env.box.startScanPolling();
  for (const delay of [0, 3000, 6000, 12000]) await env.fire(delay);
  assert.match(env.node('#scan-progress-detail').textContent, /15秒/);
  await env.fire(15000); assert.equal(env.has(1500), true);
  await env.fire(1500); assert.equal(env.node('#scan').disabled, false); assert.equal(env.timers.size, 0);
});

test('扫描实际wrapper首GET10秒软超时，中止后未知退避恢复而非后台失败', async () => {
  let attempts = 0;
  const env = scanFixture((_, { signal }) => {
    if (attempts++ > 0) return Promise.resolve({ running: false });
    return new Promise((resolve, reject) => signal.addEventListener('abort', () => reject(Error('timeout')), { once: true }));
  });
  env.box.startScanPolling(); const pending = env.fire(0);
  assert.equal(env.has(1500), false); env.fire(10000); await pending;
  assert.equal(env.gets()[0].options.signal.aborted, true);
  assert.match(env.node('#scan-progress-detail').textContent, /未确认.*3秒/);
  assert.equal(env.node('#scan').disabled, true); assert.deepEqual(env.toasts, []);
  await env.fire(3000); assert.equal(env.node('#scan').disabled, false); assert.equal(env.timers.size, 0);
});

test('扫描软超时不并发重读，忽略abort的GET晚回也不能被当作完成', async () => {
  let finish;
  const env = scanFixture(() => new Promise(resolve => { finish = resolve; }));
  env.box.startScanPolling(); const pending = env.fire(0); env.fire(10000);
  assert.equal(env.gets()[0].options.signal.aborted, true);
  assert.equal(env.timers.size, 0, 'soft deadline does not race a second GET against an unsettled request');
  finish(scanTerminal); await pending;
  assert.match(env.node('#scan-progress-title').textContent, /状态未确认/);
  assert.match(env.node('#scan-progress-detail').textContent, /未确认.*3秒/);
  assert.equal(env.node('#scan').disabled, true); assert.deepEqual(env.toasts, []);
  assert.equal(env.has(4000), false); assert.equal(env.has(3000), true);
  env.box.stopScanPolling(); assert.equal(env.timers.size, 0);
});

test('扫描GET401/403停止且禁启动，不自动重放提交', async () => {
  for (const status of [401, 403]) {
    const env = scanFixture(async () => { throw Object.assign(Error('denied'), { status }); });
    env.box.startScanPolling(); await env.fire(0);
    assert.match(env.node('#scan-progress-detail').textContent, /读取受限.*未确认/);
    assert.equal(env.node('#scan').disabled, true); assert.equal(env.timers.size, 0);
    assert.equal(env.gets().length, 1); assert.equal(env.posts().length, 0); assert.deepEqual(env.toasts, []);
  }
});

test('扫描空闲/历史失败及error终态不toast，失败面板不自动隐藏', async () => {
  for (const terminal of [{ running: false }, { ...scanTerminal, failed: 1 }, { ...scanTerminal, error: 'fixture scan error' }]) {
    const env = scanFixture(async () => terminal);
    env.box.startScanPolling(); await env.fire(0);
    assert.equal(env.node('#scan').disabled, false); assert.deepEqual(env.toasts, []);
    assert.equal(env.timers.size, 0);
    assert.equal(env.node('#scan-progress').hidden, !terminal.finished_at);
    if (terminal.finished_at) {
      assert.match(env.node('#scan-progress-title').textContent, /存在错误/);
      assert.match(env.node('#scan-progress-detail').textContent, terminal.error ? /fixture scan error/ : /失败 1/);
    }
  }
});

test('扫描重复观察/取消中止旧GET，晚成功或晚失败均不渲染不复活', async () => {
  for (const fail of [false, true]) {
    const replies = [];
    const env = scanFixture((_, { signal }) => new Promise((resolve, reject) => replies.push({ resolve, reject, signal })));
    env.box.startScanPolling(); const old = env.fire(0);
    env.box.startScanPolling(); env.box.startScanPolling();
    assert.equal(replies[0].signal.aborted, true); assert.equal(env.timers.size, 1);
    const current = env.fire(0), before = env.snapshot();
    if (fail) replies[0].reject(Error('late')); else replies[0].resolve(scanTerminal);
    await old; assert.equal(env.snapshot(), before);
    env.box.stopScanPolling(); const stopped = env.snapshot();
    if (fail) replies[1].reject(Error('late')); else replies[1].resolve(scanTerminal);
    await current; assert.equal(env.snapshot(), stopped); assert.equal(env.timers.size, 0);
  }
});

test('扫描全局工具栏跨实际page导航继续观察同一个GET', async () => {
  let finish;
  const env = scanFixture(() => new Promise(resolve => { finish = resolve; }));
  env.box.startScanPolling(); const pending = env.fire(0);
  await env.box.page('fixture');
  assert.equal(env.box.pageGen, 2); assert.equal(env.gets()[0].options.signal.aborted, false);
  finish({ running: true, current: '/fixture/new/movie.mkv', done: 1 }); await pending;
  assert.equal(env.node('#scan').disabled, true); assert.equal(env.node('#scan-progress').hidden, false);
  assert.match(env.node('#scan-progress-detail').textContent, /movie.mkv/);
  assert.equal(env.has(1500), true); env.box.stopScanPolling(); assert.equal(env.timers.size, 0);
});

test('扫描旧hide取消且即使旧回调已排队也不能关闭新scan', async () => {
  const env = scanFixture(async () => scanTerminal);
  env.box.startScanPolling(); await env.fire(0);
  const oldHide = [...env.timers.values()].find(t => t.delay === 4000).fn;
  env.box.startScanPolling(); assert.equal(env.has(4000), false);
  env.node('#scan-progress').hidden = false; oldHide();
  assert.equal(env.node('#scan-progress').hidden, false);
  env.box.stopScanPolling(); oldHide(); assert.equal(env.node('#scan-progress').hidden, false);
  assert.equal(env.timers.size, 0);
});

test('实际runScan提交重复保护，首GET空闲或旧终态仍继续读取且pending不解锁', async () => {
  for (const initial of [{ running: false }, scanTerminal]) {
    let finish, reads = 0;
    const env = scanFixture(async () => reads++ ? { running: true, done: 1 } : initial,
      () => new Promise(resolve => { finish = resolve; }));
    const pending = env.box.runScan('lib &1'); await env.box.runScan(2);
    assert.equal(env.posts().length, 1); assert.equal(env.posts()[0].path, '/scan?library_id=lib%20%261');
    await env.fire(0);
    assert.equal(env.node('#scan').disabled, true); assert.equal(env.node('#scan-progress').hidden, false);
    assert.match(env.node('#scan-progress-title').textContent, /请求进行中/);
    assert.equal(env.has(1500), true); assert.equal(env.has(4000), false); assert.deepEqual(env.toasts, []);
    await env.fire(1500); assert.equal(env.node('#scan').disabled, true);
    finish({ success: 2, pending: 1 }); await pending;
    assert.equal(env.node('#scan').disabled, true, 'POST success cannot confirm progress is idle');
    assert.equal(env.has(0), true); assert.equal(env.has(1500), false);
    assert.equal(env.posts().length, 1); assert.equal(env.toasts.length, 1); assert.equal(env.reloads, 1);
    env.box.stopScanPolling(); assert.equal(env.timers.size, 0);
  }
});

test('实际runScan成功后只由重读终态释放按钮，不重放POST或重复toast', async () => {
  const env = scanFixture(async () => scanTerminal);
  await env.box.runScan(); assert.equal(env.posts()[0].path, '/scan');
  assert.equal(env.node('#scan').disabled, true); assert.equal(env.gets().length, 0);
  assert.match(env.toasts[0][0], /扫描完成：2 已刮削 \/ 1 未刮削/);
  await env.fire(0); assert.equal(env.node('#scan').disabled, false);
  assert.equal(env.posts().length, 1); assert.equal(env.toasts.length, 1);
  env.box.stopScanPolling(); assert.equal(env.timers.size, 0);
});

test('实际runScan503/network失败后只重读确认状态，401403停止不重试POST', async () => {
  for (const status of [503, undefined, 401, 403]) {
    let reads = 0;
    const env = scanFixture(async () => reads++ ? scanTerminal : { running: true }, async () => { throw Object.assign(Error('submit uncertain'), { status }); });
    await env.box.runScan(); assert.equal(env.node('#scan').disabled, true);
    assert.deepEqual(env.toasts, [['submit uncertain', 'error']]);
    if (status === 401 || status === 403) {
      assert.equal(env.timers.size, 0); assert.equal(env.gets().length, 0);
      assert.match(env.node('#scan-progress-detail').textContent, /读取受限/);
    } else {
      await env.fire(0); assert.equal(env.node('#scan').disabled, true);
      await env.fire(1500); assert.equal(env.node('#scan').disabled, false);
      assert.deepEqual(env.toasts, [['submit uncertain', 'error']]);
    }
    assert.equal(env.posts().length, 1); env.box.stopScanPolling(); assert.equal(env.timers.size, 0);
  }
});

test('实际runScan提交失败后首读历史终态仅确认空闲，不toast本轮完成', async () => {
  const env = scanFixture(async () => scanTerminal, async () => { throw Error('network uncertain'); });
  await env.box.runScan(); assert.equal(env.node('#scan').disabled, true);
  await env.fire(0); assert.equal(env.node('#scan').disabled, false);
  assert.deepEqual(env.toasts, [['network uncertain', 'error']]);
  assert.equal(env.posts().length, 1); assert.equal(env.reloads, 0);
  env.box.stopScanPolling(); assert.equal(env.timers.size, 0);
});

test('实际runScan旧POST成功/错误不刷新切页或新wallGen，但全局观察继续', async () => {
  for (const generation of ['page', 'wall']) for (const fail of [false, true]) {
    let resolve, reject;
    const env = scanFixture(async () => ({ running: true }), () => new Promise((a, b) => { resolve = a; reject = b; }));
    const pending = env.box.runScan();
    if (generation === 'page') await env.box.page('fixture'); else env.box.wallGen++;
    if (fail) reject(Error('late submit error')); else resolve({ success: 1, pending: 0 });
    await pending; assert.equal(env.reloads, 0); assert.equal(env.posts().length, 1);
    assert.equal(env.timers.size, 1); await env.fire(0);
    assert.equal(env.node('#scan').disabled, true); assert.equal(env.has(1500), true);
    env.box.stopScanPolling(); assert.equal(env.timers.size, 0);
  }
});

test('实际boot首scan GET悬挂不阻塞初始page或导航，统一观察器负责10秒超时', async () => {
  const env = scanFixture((_, { signal }) => new Promise((resolve, reject) => {
    signal.addEventListener('abort', () => reject(Error('boot progress timeout')), { once: true });
  }));
  // 范围声明：boot/scan/helper均为实际函数；认证、DOM及page渲染/导航是调用桩，非浏览器集成。
  const rendered = [], events = {}, nav = env.node('nav'); nav.dataset.page = 'fixture';
  Object.assign(env.box, { token: 'fixture', fetch: async () => ({ ok: true, status: 200 }),
    window: { location: { replace() { assert.fail('unexpected redirect'); } }, addEventListener(type, fn) { events[type] = fn; } },
    localStorage: { removeItem() { assert.fail('unexpected credential removal'); } },
    closePlayer() {}, closeScrapePreview() {}, closeDetail() {}, wallMaybeLoadMore() {}, detailOverlay: env.node('detail'),
    page: name => { rendered.push(name); env.box.pageGen++; } });
  env.box.document.querySelectorAll = selector => selector === '.nav-item' ? [nav] : [];
  env.box.document.addEventListener = () => {};
  vm.runInContext(section('async function boot()', '\nboot();'), env.box);
  let ready = false;
  const boot = env.box.boot().then(() => { ready = true; });
  for (let i = 0; i < 12; i++) await Promise.resolve();
  assert.equal(ready, true, 'boot must render without awaiting bare scan progress'); await boot;
  assert.deepEqual(rendered, ['items']); assert.equal(env.gets().length, 0);
  const pending = env.fire(0); assert.equal(env.gets().length, 1); assert.equal(env.has(10000), true);
  nav.handlers.click(); assert.deepEqual(rendered, ['items', 'fixture']);
  env.box.location.hash = '#items'; events.popstate(); assert.deepEqual(rendered, ['items', 'fixture', 'items']);
  assert.equal(env.gets()[0].options.signal.aborted, false, 'navigation must not cancel global scan read');
  env.fire(10000); await pending; assert.equal(env.gets()[0].options.signal.aborted, true);
  assert.match(env.node('#scan-progress-detail').textContent, /未确认.*3秒/);
  assert.equal(env.node('#scan').disabled, true); env.box.stopScanPolling(); assert.equal(env.timers.size, 0);
});

test('探测文件成功但版本刷新失败保持错误面板，不生成成功通知或自动隐藏', async () => {
  for (const announced of [false, true]) {
    const error = '探测文件已保存，但缓存版本刷新未确认；请检查存储后重新扫库';
    const env = probePageFixture(async () => ({ ...probeTerminal, refresh_failed: 1, error }));
    await probeReady(env);
    env.box.startProbePolling(announced);
    await env.fire(0);
    assert.equal(env.node('#probe-media').disabled, false);
    assert.equal(env.node('#probe-progress').hidden, false);
    assert.match(env.node('#probe-progress-title').textContent, /存在错误/);
    assert.match(env.node('#probe-progress-detail').textContent, /文件已保存.*刷新未确认/);
    assert.equal(env.has(8000), false);
    assert.equal(env.timers.size, 0);
    assert.equal(env.toasts.length, announced ? 1 : 0);
    if (announced) { assert.equal(env.toasts[0][1], 'error'); assert.match(env.toasts[0][0], /刷新未确认/); }
  }
});
