const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, 'web/app.js'), 'utf8');
const html = fs.readFileSync(path.join(__dirname, 'web/index.html'), 'utf8');

function section(start, end) {
  const from = source.indexOf(start);
  const to = source.indexOf(end, from);
  assert.ok(from >= 0 && to > from);
  return source.slice(from, to);
}
function sandbox(values = {}) {
  return vm.createContext({ URLSearchParams, AbortController, token: 'fixture-token', me: null,
    TRANSLATE_HINT: '翻译来源提示',
    pageGen: 1, render: 1, scheduledRender: 1, identity: values.token ?? 'fixture-token',
    // 页面清理会调用磁盘日志轮询的停止函数；被截取的代码段不包含该定义。
    stopLogsPolling() {}, scanClose: { hidden: true, addEventListener() {} },
    scanFirstRead: true, scanWallRefresh: null, scanObservedRuns: new Set(), scanNotifiedRuns: new Set(),
    localStorage: { getItem: () => values.token ?? 'fixture-token' }, ...values });
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
    get stored() { return stored; }, setStored(value) { stored = value; }, get removed() { return removed; }, get rendered() { return rendered; },
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
  let jsonReads = 0;
  const initialized = () => ({ ok: true, status: 204, json: async () => { jsonReads++; throw new Error('204 has no JSON'); } });
  const withCode = loginForm({ mode: 'initialize', Username: 'admin', Pw: 'secret123', confirm: 'secret123', Code: 'setup-code' }, initialized);
  await withCode.box.submit({ preventDefault() {}, target: {} }, true);
  assert.equal(JSON.parse(withCode.calls[0].options.body).Code, 'setup-code');

  const withoutCode = loginForm({ mode: 'initialize', Username: 'admin', Pw: 'secret123', confirm: 'secret123' }, initialized);
  await withoutCode.box.submit({ preventDefault() {}, target: {} }, true);
  assert.equal('Code' in JSON.parse(withoutCode.calls[0].options.body), false);
  assert.equal(jsonReads, 0, '初始化204不读取JSON');
  for (const env of [withCode, withoutCode]) {
    assert.deepEqual(env.stored, []);
    assert.deepEqual(env.redirects, []);
    assert.equal(vm.runInContext('mode', env.box), 'login');
    assert.match(env.rootNode.innerHTML, /欢迎回来/);
  }
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

test('登录非空token保留原始首尾空白，不擅自规范化凭据', async () => {
  const original = '  tok-1  ';
  const env = loginForm({ Username: 'admin', Pw: 'secret123' },
    () => ({ ok: true, status: 200, json: async () => ({ AccessToken: original }) }));
  await env.box.submit({ preventDefault() {}, target: {} }, false);
  assert.deepEqual(env.stored, [['emby_token', original]]);
  assert.equal(vm.runInContext('token', env.box), original);
  assert.deepEqual(env.redirects, ['/admin']);
});

for (const [label, body] of [
  ['null', null], ['array', []], ['missing', {}],
  ['empty', { AccessToken: '' }], ['whitespace', { AccessToken: '  \t' }],
  ['number', { AccessToken: 42 }], ['object', { AccessToken: {} }], ['token-array', { AccessToken: [] }]
]) {
  test(`登录畸形成功响应 ${label} 不污染凭据、不跳转且保留表单`, async () => {
    const env = loginForm({ Username: 'admin', Pw: 'secret123' },
      () => ({ ok: true, status: 200, json: async () => body }));
    vm.runInContext("token = 'previous-token'", env.box);
    await env.box.submit({ preventDefault() {}, target: {} }, false);
    assert.deepEqual(env.stored, []);
    assert.equal(vm.runInContext('token', env.box), 'previous-token');
    assert.deepEqual(env.redirects, []);
    assert.match(env.errorNode.textContent, /登录响应无效/);
    assert.equal(env.button.disabled, false);
    assert.equal(env.rootNode.innerHTML, '');
    assert.equal(env.box.document.querySelector('#auth-user').value, 'admin');
    assert.equal(env.box.document.querySelector('#auth-pw').value, 'secret123');
    assert.equal(env.calls.length, 1, '不自动重放登录POST');
  });
}

test('登录2xx无效JSON按协议错误提示，不存凭据或重放POST', async () => {
  const env = loginForm({ Username: 'admin', Pw: 'secret123' },
    () => ({ ok: true, status: 200, json: async () => { throw new SyntaxError('parser detail'); } }));
  vm.runInContext("token = 'previous-token'", env.box);
  await env.box.submit({ preventDefault() {}, target: {} }, false);
  assert.deepEqual(env.stored, []);
  assert.equal(vm.runInContext('token', env.box), 'previous-token');
  assert.deepEqual(env.redirects, []);
  assert.match(env.errorNode.textContent, /登录响应无效/);
  assert.doesNotMatch(env.errorNode.textContent, /parser detail/);
  assert.equal(env.button.disabled, false);
  assert.equal(env.rootNode.innerHTML, '');
  assert.equal(env.box.document.querySelector('#auth-user').value, 'admin');
  assert.equal(env.box.document.querySelector('#auth-pw').value, 'secret123');
  assert.equal(env.calls.length, 1);
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

test('设置页展示索引恢复列表与超过 7 天提示', () => {
  for (const snippet of [
    "api('/recovery')",
    '索引恢复',
    '已超过 7 天',
    '待恢复条目',
    'recovery.stale_over_7d'
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
  assert.equal(inside.prevented, true, '受控顺序：遮罩内 Tab 也由脚本推进');
  assert.equal(log.at(-1), 'player-second', 'Tab 从首项前进到第二项');

  active = secondBtn; // 真实浏览器里 focus() 已把焦点移到第二项
  const shift = { key: 'Tab', shiftKey: true, prevented: false, preventDefault() { this.prevented = true; } };
  box.overlayTrapTab(shift);
  assert.equal(shift.prevented, true);
  assert.equal(log.at(-1), 'player-close', 'Shift+Tab 从第二项回退到首项');
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
  assert.deepEqual(JSON.parse(JSON.stringify(changed)), { library_id: 3, mode: 'full' });
});

function scheduledSubmitFixture({ readable = true, library = '7', kind = 'probe', params = { limit: 50, status: 'manual', only_missing: false }, etag = '"sc1-fixture"', failure = 0 } = {}) {
  let submit; const calls = [], messages = [];
  const fields = { '#s-lib': { value: library }, '#s-status': { value: 'manual' }, '#s-only-missing': { checked: false }, '#s-name': { value: 'Renamed' }, '#s-enabled': { checked: false }, '#sched-submit': { disabled: false }, '#sched-form': { addEventListener(_, fn) { submit = fn; } } };
  fields['#s-scan-mode'] = { value: 'full' };
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
  assert.deepEqual(changed.calls[0].body.params, { library_id: 7, mode: 'full' });
});

test('损坏参数或已消失媒体库不允许静默覆盖或变成全部', async () => {
  for (const options of [{ readable: false }, { library: '' }]) {
    const env = scheduledSubmitFixture(options); await env.submit(); assert.equal(env.calls.length, 0); assert.equal(env.messages.length, 1);
  }
  assert.match(source, /paramsReadable \? '' : '<p role="alert">/);
});

test('计划任务 cron 校验 latest-wins、单次提交且提示服务器时区', async () => {
  assert.match(source, /let cronSeq = 0;/);
  assert.match(source, /if \(seq !== cronSeq \|\| cronEl\.value\.trim\(\) !== expr\) return;/);
  assert.match(source, /服务器时区 \$\{result\.time_zone \|\| '本地'\}/);
  assert.match(source, /if \(submit\.disabled\) return;/);
  const env = scheduledSubmitFixture();
  await env.submit();
  await env.submit();
  assert.equal(env.calls.length, 1);
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
    vm.runInContext(section("  document.querySelectorAll('[data-role]').forEach", '\n}\n\n/* ---------------------------------------------------------------- 运行日志 */'), env.box);
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
  vm.runInContext(section('async function deleteIndexedItem(', 'const STATUS_TEXT ='), box);
  vm.runInContext(section('async function pageItems()', '// reloadWall'), box);
  await box.pageItems();
  box.wallState.items = [{ id: 7 }];
  const card = { dataset: { play: '7' }, closest: selector => selector === '.wall-card' ? card : null };
  return { handlers, calls, card, box, element };
}
function wallKey(target, key, extra = {}) {
  return { target, key, defaultPrevented: false,
    preventDefault() { this.defaultPrevented = true; }, ...extra };
}

test('删除响应保留204兼容，Webhook失败或跳过不得提示远端删除成功', async () => {
  for (const status of [null, 'disabled', 'failed', 'skipped', 'sent']) {
    const calls = [], notices = [];
    const box = sandbox({ token: 'fixture', FormData: class {},
      fetch: async (url, options) => { calls.push([url, options.method]); return { ok: true, status: 204, headers: { get: () => status } }; },
      toast: (message, kind) => notices.push([message, kind]) });
    vm.runInContext(section('async function api(', 'const STATUS_TEXT ='), box);
    const result = await box.api('/items/7', { method: 'DELETE' });
    if (status === null) assert.equal(result, null);
    else assert.equal(result.webhook_status, status);
    calls.length = 0;
    await box.deleteIndexedItem(7);
    assert.deepEqual(calls, [['/api/admin/items/7', 'DELETE']], '只删除一次，不重试发送');
    assert.equal(notices.length, 1);
    assert.equal(notices[0][1], ['failed', 'skipped'].includes(status) ? 'error' : 'ok');
    if (status === 'failed') assert.match(notices[0][0], /索引已删除.*通知失败.*不要重复删除/);
    if (status === 'sent') assert.match(notices[0][0], /接收端已确认.*核对远端/);
  }
  assert.ok(!source.includes('仅删除数据库索引，不删除源文件。继续？'));
  assert.match(source, /confirm\('删除该影片索引？.*远端整个目录.*无法撤销/);
});

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
    fetch: async () => ({ status: 401 }), localStorage: { getItem: () => 'fixture', removeItem() { removed++; } },
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
  vm.runInContext(section('function progressCounts(', 'function renderScrapeProgress('), box);
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
function scanFixture(progress = async () => ({ running: false }), post = async () => ({ success: 2, pending: 1 }), task = async () => ({ status: 'succeeded', finished_at: 'now' })) {
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
      if (path.startsWith('/tasks/')) return task(path, options);
      assert.equal(path, '/scan/progress'); return progress(path, options);
    } });
  box.pages.items = box.pageItems;
  vm.runInContext(section('function startProgressPolling(', 'function stopScrapePolling()'), box);
  vm.runInContext(section('function progressCounts(', 'function renderScrapeProgress('), box);
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
    assert.equal(env.posts()[1].path, '/tasks/scan?library_id=9&mode=incremental');
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
    // R2：没有观察到运行身份的历史终态不算本轮完成，不通知、不排自动收起。
    assert.deepEqual(env.toasts, []); assert.equal(env.node('#scan-progress').hidden, true);
    assert.equal(env.has(4000), false); assert.equal(env.timers.size, 0);
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

test('扫描首读历史空闲/失败/error终态都不算本轮完成，不toast不自动隐藏', async () => {
  for (const terminal of [{ running: false }, { ...scanTerminal, failed: 1 }, { ...scanTerminal, error: 'fixture scan error' }]) {
    const env = scanFixture(async () => terminal);
    env.box.startScanPolling(); await env.fire(0);
    assert.equal(env.node('#scan').disabled, false); assert.deepEqual(env.toasts, []);
    assert.equal(env.timers.size, 0);
    // R2：没有观察到 run_id 的历史终态只确认空闲并隐藏面板，不冒充本轮结果。
    assert.equal(env.node('#scan-progress').hidden, true);
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
  let reads = 0;
  const terminal = { ...scanTerminal, instance_id: 'inst-1', run_id: '1' };
  const env = scanFixture(async () => reads++ === 0 ? { running: true, instance_id: 'inst-1', run_id: '1', done: 0 } : terminal);
  env.box.startScanPolling(); await env.fire(0); await env.fire(1500);
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
    assert.equal(env.posts().length, 1); assert.equal(env.posts()[0].path, '/tasks/scan?library_id=lib%20%261&mode=incremental');
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
  await env.box.runScan(); assert.equal(env.posts()[0].path, '/tasks/scan?mode=incremental');
  assert.equal(env.node('#scan').disabled, true); assert.equal(env.gets().length, 0);
  assert.match(env.toasts[0][0], /扫描任务已启动/);
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
    localStorage: { getItem: () => 'fixture', removeItem() { assert.fail('unexpected credential removal'); } },
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

test('图片请求带宽度预算与 1x/2x srcset（FE-11）', () => {
  // 媒体墙：200/400 两档；详情海报 120/240；详情背景 540/1080；详情海报带尺寸避免 CLS。
  assert.match(source, /posterBase \? `\$\{posterBase\}\?maxWidth=200 1x, \$\{posterBase\}\?maxWidth=400 2x` : ''/);
  assert.match(source, /class="detail-poster" width="104" height="156"/);
  assert.match(source, /\?maxWidth=120 1x, \$\{posterBase\}\?maxWidth=240 2x/);
  assert.match(source, /\?maxWidth=540 1x, \$\{backdropBase\}\?maxWidth=1080 2x/);
  assert.match(source, /class="detail-backdrop" src="\$\{esc\(backdrop\)\}"\$\{backdropSrcset \? ` srcset=/);
});
test('多用户管理 UI 契约：用户管理、媒体库授权与活动记录面板', () => {
  assert.match(source, /<h2 id="users-title">用户管理<\/h2>/);
  assert.match(source, /id="user-create-form"/);
  assert.match(source, /data-user-lib="\$\{u\.id\}"/);
  assert.match(source, /data-user-save=/);
  assert.match(source, /data-user-del=/);
  assert.match(source, /api\('\/users', \{ method: 'POST'/);
  assert.match(source, /\/users\/' \+ id \+ '\/libraries'/);
  assert.match(source, /<h2 id="audit-title">活动记录<\/h2>/);
  assert.match(source, /api\('\/login-events\?limit=50'\)/);
  assert.match(source, /api\('\/playback-events\?limit=50'\)/);
  assert.match(source, /id="pw-form"/);
  assert.match(source, /fetch\('\/api\/auth\/password'/);
  assert.match(source, /function renderSelfPassword\(\)/);
  // 普通用户（Policy.IsAdministrator=false）只保留设置页入口。
  assert.match(source, /if \(me && !me\.is_admin\) \{/);
  assert.match(source, /dto\.Policy && dto\.Policy\.IsAdministrator/);
});

test('活动记录是独立侧边栏页面（批次 89）', () => {
  assert.match(html, /data-page="activity"/);
  assert.match(html, /<span>活动记录<\/span>/);
  assert.match(source, /activity: \{ title: '活动记录', crumb: 'Archive \/ Activity' \}/);
  assert.match(source, /activity: pageActivity,/);
  assert.match(source, /async function pageActivity\(\)/);
  assert.match(source, /refreshPage\(pageActivity, gen\)/);
  const settingsBody = source.slice(source.indexOf('async function pageSettings()'), source.indexOf('async function pageSettings()') + 1200);
  assert.doesNotMatch(settingsBody, /login-events|playback-events/);
});

test('P1 修复：路由收起浮层、hover 控件可聚焦、筛选重绘还原焦点', () => {
  const appSource = fs.readFileSync(path.join(__dirname, 'web/app.js'), 'utf8');
  const css = fs.readFileSync(path.join(__dirname, 'web/style.css'), 'utf8');
  // P1-7：page() 入口收起所有浮层，函数按层级关闭。
  assert.match(appSource, /async function page\(name\) \{\n  \/\/ 浮层属于来源页上下文/);
  assert.match(appSource, /closeOverlaysForNavigation\(\);/);
  const navClose = appSource.slice(appSource.indexOf('function closeOverlaysForNavigation'), appSource.indexOf('function closeOverlaysForNavigation') + 500);
  for (const selector of ['#scrape-overlay', '#player-overlay', '#detail-overlay']) {
    assert.ok(navClose.includes(selector), `导航收起缺少 ${selector}`);
  }
  // P1-8：键盘焦点进入卡片时显示操作按钮与播放层。
  assert.match(css, /\.wall-card:focus-within \.wall-actions \{ opacity: 1; \}/);
  assert.match(css, /\.wall-card:focus-within \.wall-play \{ opacity: 1; \}/);
  // P1-9：重绘前记录触发控件，重绘后还原焦点。
  assert.match(appSource, /let pendingWallFocus = '';/);
  assert.match(appSource, /pendingWallFocus = '\[data-status="/);
  assert.match(appSource, /pendingWallFocus = '\[data-lib="/);
  assert.match(appSource, /pendingWallFocus = '#item-sort';/);
  assert.match(appSource, /const target = document\.querySelector\(pendingWallFocus\);/);
});

test('刮削候选慢回包不覆盖更新的选择（P1-10）', async () => {
  const resolvers = new Map();
  let renders = 0;
  const box = sandbox({
    scrapeState: null,
    renderScrapePreview() { renders++; },
    document: { querySelector: () => ({ innerHTML: '' }) },
    api: (path, options) => new Promise(resolve => { resolvers.set(JSON.parse(options.body).id, resolve); })
  });
  vm.runInContext(section('async function loadScrapeInspect(', 'function renderScrapePreview()'), box);
  const state = { movieId: 7, preview: {}, inspect: null, provider: '', id: '', inspectSeq: 0 };
  box.scrapeState = state;
  const slow = box.loadScrapeInspect('p', 'A');
  const fast = box.loadScrapeInspect('p', 'B');
  // 后点的 B 先回，慢的 A 后回也不能覆盖。
  resolvers.get('B')({ title: 'B' });
  await fast;
  assert.equal(state.id, 'B');
  resolvers.get('A')({ title: 'A' });
  await slow;
  assert.equal(state.id, 'B', '慢候选不得覆盖新选择');
  assert.equal(state.inspect.title, 'B');
  assert.equal(renders, 1, '只有最新回包触发重绘');
  // 关闭浮层后回包也不写状态。
  const closing = box.loadScrapeInspect('p', 'C');
  box.scrapeState = null;
  resolvers.get('C')({ title: 'C' });
  await closing;
  assert.equal(state.id, 'B');
});

test('运行日志独立页与 ffprobe 状态（生产可诊断）', async () => {
  // R4：日志从设置页拆到独立磁盘日志页，不再轮询 ring 的 /logs 接口。
  assert.match(html, /id="nav-logs"/);
  assert.match(source, /logs: pageLogs/);
  assert.match(source, /api\('\/logs\/disk' \+ query/);
  assert.match(source, /\?kind=app&limit=200/);
  assert.match(source, /id="dl-earlier"/);
  assert.match(source, /部分通知未送达，业务操作已完成/);
  assert.match(source, /ffprobe_error/);
  assert.match(source, /媒体信息探测/);
});

test('移动端表格：用户卡片化与日志堆叠', async () => {
  // 用户管理在窄屏按字段堆叠（data-label），日志表改为时间/级别一行、内容独占一行。
  const css = fs.readFileSync(path.join(__dirname, 'web/style.css'), 'utf8');
  assert.match(source, /<table class="user-table">/);
  assert.match(source, /data-label="媒体库授权"/);
  assert.match(css, /\.user-table thead \{ display: none; \}/);
  assert.match(css, /\.table-wrap table:not\(\.log-table\):not\(\.user-table\) \{ min-width: 560px; \}/);
  assert.match(css, /\.log-table td:nth-child\(3\) \{ margin-top: 2px; word-break: break-word; \}/);
});

const webhookUI = fs.readFileSync(path.join(__dirname, 'web/webhook-settings.js'), 'utf8');
const webhookEventNames = [...webhookUI.matchAll(/\['((?:item|playback|user)\.[a-z]+)',/g)].map(m => m[1]);
const whTarget = (over = {}) => ({ id: 'wh_1', name: '目标A', enabled: false, url_configured: true,
  events: ['item.rate'], format: 'json', timeout_seconds: 5, allow_private: false, ...over });
const whView = (targets = [whTarget()], tag = '"cfg-1"', multi = true) => ({ version: 2, config_etag: tag, multi_target: multi, targets });

function webhookPanel(api) {
  const makeNode = () => ({ value: '', checked: false, disabled: false, textContent: '', hidden: false, role: '',
    listeners: {}, addEventListener(type, fn) { this.listeners[type] = fn; },
    setAttribute(k, v) { this[k] = v; }, focus() { this.focused = true; } });
  const ids = ['webhook-title', 'wh-reload', 'wh-targets', 'wh-empty', 'wh-remove-preview', 'wh-confirm-remove-row',
    'wh-confirm-remove', 'wh-confirm-empty-row', 'wh-confirm-empty', 'wh-confirm-delete', 'wh-status', 'wh-add', 'wh-save'];
  const nodes = Object.fromEntries(ids.map(id => [id, makeNode()]));
  const targetsHost = nodes['wh-targets'];
  let cards = [];
  targetsHost.appendChild = card => { cards.push(card); };
  targetsHost.querySelectorAll = selector => {
    if (selector === '[data-target-index]') return cards.slice();
    if (selector === 'input, select, button') return cards.flatMap(card => card.fields());
    return [];
  };
  Object.defineProperty(targetsHost, 'innerHTML', { get: () => '', set: () => { cards = []; } });
  const cardStub = () => {
    const map = new Map();
    const field = selector => {
      if (!map.has(selector)) map.set(selector, makeNode());
      return map.get(selector);
    };
    const eventNodes = webhookEventNames.map(name => ({ dataset: { event: name }, checked: false, disabled: false }));
    return {
      ...makeNode(), open: false,
      dataset: { targetIndex: '0' },
      fields: () => [...map.values(), ...eventNodes],
      events: eventNodes,
      querySelector: field,
      querySelectorAll: selector => selector === '[data-event]' ? eventNodes : [],
      innerHTML: ''
    };
  };
  const host = { isConnected: true, innerHTML: '', querySelector: selector => nodes[selector.replace(/^#/, '')] };
  const calls = [], prompts = [];
  let active = true, confirms = true;
  const box = sandbox({ document: { createElement: () => cardStub() },
    confirm: text => { prompts.push(text); return confirms; } });
  vm.runInContext(webhookUI, box);
  const mounted = box.mountWebhookSettings(host, async (...args) => { calls.push(args); return api(...args); }, () => active);
  const card = index => cards[index];
  return {
    nodes, calls, host, mounted, prompts,
    cards: () => cards,
    card,
    field: (index, name) => cards[index].querySelector(`[data-field="${name}"]`),
    events: index => cards[index].events,
    add: () => nodes['wh-add'].listeners.click(),
    remove: index => cards[index].querySelector('[data-remove]').listeners.click(),
    save: () => nodes['wh-save'].listeners.click(),
    reload: () => nodes['wh-reload'].listeners.click(),
    confirm(value) { confirms = value; },
    leave() { active = false; host.isConnected = false; }
  };
}

test('Webhook多目标加载不回显地址，保存完整订阅及If-Match', async () => {
  const p = webhookPanel(async (path, options) => {
    assert.equal(path, '/webhook/targets');
    if (!options) return whView();
    return whView([whTarget({ name: '目标A', enabled: true, events: ['playback.stop'], format: 'form', timeout_seconds: 12, allow_private: true })], '"cfg-2"');
  });
  await p.mounted;
  assert.equal(p.cards().length, 1);
  assert.equal(p.events(0).length, 13);
  assert.equal(p.field(0, 'url').value, '');
  assert.equal(p.nodes['wh-save'].disabled, false);
  p.field(0, 'enabled').checked = true; p.field(0, 'allow-private').checked = true;
  p.field(0, 'url').value = 'https://receiver.example/private-token-canary'; p.field(0, 'format').value = 'form'; p.field(0, 'timeout').value = '12';
  for (const e of p.events(0)) e.checked = e.dataset.event === 'playback.stop';
  await p.save();
  assert.equal(p.calls.length, 2, '只GET一次与PUT一次，不试发、不重载整页');
  const options = p.calls[1][1], body = JSON.parse(options.body);
  assert.equal(options.method, 'PUT'); assert.equal(options.headers['If-Match'], '"cfg-1"');
  assert.deepEqual(body.targets[0].events, ['playback.stop']); assert.equal(body.targets[0].url, 'https://receiver.example/private-token-canary');
  assert.equal(body.targets[0].id, 'wh_1'); assert.equal(body.targets[0].format, 'form');
  assert.equal(body.targets[0].timeout_seconds, 12); assert.equal(body.targets[0].allow_private, true);
  assert.equal(body.confirm_delete, false); assert.equal(body.confirm_empty_targets, false);
  assert.equal(p.field(0, 'url').value, ''); assert.equal(p.nodes['wh-confirm-delete'].checked, false);
  assert.match(p.nodes['wh-status'].textContent, /即时生效.*重启保留.*未发送测试通知/);
  await p.save(); assert.equal(p.calls[2][1].headers['If-Match'], '"cfg-2"');
  assert.equal(JSON.parse(p.calls[2][1].body).targets[0].url, undefined, '下一次留空保留，不填充旧URL');
});

test('Webhook多目标新增目标由服务端分配ID，删除需清单确认，清空需显式确认', async () => {
  const p = webhookPanel(async (path, options) => {
    if (!options) return whView();
    const body = JSON.parse(options.body);
    if (body.targets.length === 0) return whView([], '"cfg-empty"');
    return whView([whTarget(), whTarget({ id: 'wh_new', name: '新目标', url_configured: true, events: ['item.updated'] })], '"cfg-2"');
  });
  await p.mounted;
  p.add();
  assert.equal(p.cards().length, 2);
  p.field(1, 'name').value = '新目标';
  p.field(1, 'url').value = 'https://receiver.example/new';
  for (const e of p.events(1)) e.checked = e.dataset.event === 'item.updated';
  await p.save();
  const body = JSON.parse(p.calls[1][1].body);
  assert.equal(body.targets.length, 2);
  assert.equal(body.targets[1].id, undefined, '新目标不伪造服务端 ID');
  assert.equal(body.targets[1].name, '新目标');
  assert.equal(p.cards().length, 2); assert.equal(p.field(1, 'name').value, '新目标');

  p.remove(1);
  assert.match(p.nodes['wh-remove-preview'].textContent, /保存后将删除/);
  await p.save();
  assert.equal(p.calls.length, 2, '未确认删除清单不发保存');
  p.nodes['wh-confirm-remove'].checked = true;
  p.remove(0);
  assert.equal(p.nodes['wh-confirm-empty-row'].hidden, false);
  await p.save();
  assert.equal(p.calls.length, 2, '清空集合还需空集合确认');
  p.nodes['wh-confirm-empty'].checked = true;
  await p.save();
  const emptyBody = JSON.parse(p.calls[2][1].body);
  assert.deepEqual(emptyBody.targets, []); assert.equal(emptyBody.confirm_empty_targets, true);
});

test('Webhook多目标读取失败只能显式重载；保存503保留草稿而不自动重试', async () => {
  let step = 0;
  const p = webhookPanel(async () => { if (++step !== 2) throw Object.assign(Error('network'), { status: 503 }); return whView(); });
  await p.mounted; assert.equal(p.nodes['wh-save'].disabled, true); assert.equal(p.nodes['wh-status'].role, 'alert');
  await p.reload(); assert.equal(p.nodes['wh-save'].disabled, false);
  p.field(0, 'url').value = 'https://receiver.example/draft';
  await p.save(); assert.equal(p.calls.length, 3); assert.equal(p.field(0, 'url').value, 'https://receiver.example/draft');
  assert.equal(p.nodes['wh-save'].disabled, false); assert.match(p.nodes['wh-status'].textContent, /保存未确认.*输入已保留/);
});

test('Webhook多目标412保留草稿并禁旧重提，显式确认重新加载才获取新令牌', async () => {
  let gets = 0;
  const p = webhookPanel(async (_, options) => {
    if (options) throw Object.assign(Error('conflict'), { status: 412 });
    return whView([whTarget()], gets++ === 0 ? '"old"' : '"new"');
  });
  await p.mounted; p.field(0, 'url').value = 'https://receiver.example/unsaved';
  await p.save(); assert.equal(p.nodes['wh-save'].disabled, true); assert.equal(p.field(0, 'url').value, 'https://receiver.example/unsaved');
  assert.match(p.nodes['wh-status'].textContent, /草稿已保留.*不会自动覆盖/);
  await p.save(); assert.equal(p.calls.length, 2);
  p.confirm(false); p.reload(); assert.equal(p.calls.length, 2); assert.equal(p.field(0, 'url').value, 'https://receiver.example/unsaved');
  p.confirm(true); await p.reload(); assert.equal(p.field(0, 'url').value, ''); assert.equal(p.nodes['wh-save'].disabled, false);
  await p.save(); assert.equal(p.calls[3][1].headers['If-Match'], '"new"');
});

test('Webhook多目标阻止空事件、坏超时及危险清除；删除订阅必须明确确认', async () => {
  const p = webhookPanel(async () => whView()); await p.mounted;
  for (const e of p.events(0)) e.checked = false; await p.save(); assert.equal(p.calls.length, 1);
  p.events(0).find(e => e.dataset.event === 'item.deleted').checked = true;
  p.field(0, 'timeout').value = '0'; await p.save(); assert.equal(p.calls.length, 1);
  p.field(0, 'timeout').value = '5'; p.field(0, 'enabled').checked = true; p.field(0, 'clear-url').checked = true;
  await p.save(); assert.equal(p.calls.length, 1);
  p.field(0, 'clear-url').checked = false; p.field(0, 'url').value = 'https://receiver.example/x';
  await p.save(); assert.equal(p.calls.length, 1, '启用删除订阅需确认');
  p.nodes['wh-confirm-delete'].checked = true; await p.save();
  assert.equal(p.calls.length, 2); assert.equal(JSON.parse(p.calls[1][1].body).confirm_delete, true);
  assert.equal(p.nodes['wh-confirm-delete'].checked, false, '每次保存都重新确认');
});

test('Webhook多目标在途提交单次，切页后迟到读取或保存不能覆盖新页面', async () => {
  let resolve;
  const p = webhookPanel(() => new Promise(r => { resolve = r; }));
  p.leave(); resolve(whView()); await p.mounted;
  assert.equal(p.nodes['wh-save'].disabled, true);
  let finish;
  const q = webhookPanel(async (_, opts) => opts ? new Promise(r => { finish = r; }) : whView());
  await q.mounted; q.field(0, 'url').value = 'https://receiver.example/draft';
  const saving = q.save(); await q.save(); assert.equal(q.calls.length, 2);
  q.leave(); finish(whView([], '"new"')); await saving;
  assert.equal(q.field(0, 'url').value, 'https://receiver.example/draft');
  assert.equal(q.nodes['wh-status'].textContent, '正在保存；不会发送测试通知…');
});

test('Webhook多目标独立资源在app之前加载，管理员设置挂载且不会请求真实试发接口', () => {
  assert.ok(html.indexOf('/web/webhook-settings.js') < html.indexOf('/web/app.js'));
  const settings = section('async function pageSettings()', 'async function pageScrape()');
  assert.match(settings, /if \(!me \|\| !me\.is_admin\)/);
  assert.match(settings, /id="webhook-settings"/);
  assert.match(settings, /mountWebhookSettings\(webhookHost, api, \(\) => gen === pageGen && webhookHost.isConnected\)/);
  assert.match(webhookUI, /type="password".*autocomplete="new-password"/);
  assert.ok(!webhookUI.includes('localStorage'));
  assert.equal((webhookUI.match(/apiRequest\('/g) || []).length, 2, '只提供读取目标与保存目标');
  assert.match(webhookUI, /If-Match/);
  assert.match(webhookUI, /confirm_empty_targets/);
  assert.match(webhookUI, /confirm_delete/);
});

const logSettingsUI = fs.readFileSync(path.join(__dirname, 'web/log-settings.js'), 'utf8');
function logPanel(api) {
  const nodes = Object.fromEntries(['form','fields','save','reload','status','days','confirm'].map(id => [id, { value: '', disabled: false, checked: false, textContent: '', setAttribute(k,v) { this[k]=v; } }]));
  const host = { isConnected: true, innerHTML: '', querySelector: s => nodes[s.slice(4)] };
  const calls = []; let active = true, confirmed = true;
  const box = sandbox({ confirm: () => confirmed }); vm.runInContext(logSettingsUI, box);
  const mounted = box.mountLogSettings(host, async (...args) => { calls.push(args); return api(...args); }, () => active);
  return { nodes, host, calls, mounted, submit: () => nodes.form.onsubmit({ preventDefault() {} }), reload: () => nodes.reload.onclick(), confirm: x => { confirmed=x; }, leave() { active=false;host.isConnected=false; } };
}
const logConfig = (days=0) => ({ retention_days: days, max_days:3650, config_etag: `"lr1-${days}"` });

test('日志期限默认不清理，非零保存须确认并带If-Match，成功及部分清理提示分离', async () => {
  let warning = false;
  const p = logPanel(async (path, options) => { assert.equal(path,'/logs/settings'); return options ? { ...logConfig(JSON.parse(options.body).retention_days), cleanup_warning: warning } : logConfig(); });
  await p.mounted;assert.equal(p.nodes.days.value,'0');assert.equal(p.nodes.save.disabled,false);
  for (const value of ['', '-1','3651','1.5']) { p.nodes.days.value=value;await p.submit(); }
  p.nodes.days.value='7';await p.submit();assert.equal(p.calls.length,1,'未确认不提交删除');
  p.nodes.confirm.checked=true;await p.submit();
  assert.equal(p.calls.length,2);assert.equal(p.calls[1][1].headers['If-Match'],'"lr1-0"');assert.equal(p.calls[1][1].method,'PUT');
  assert.deepEqual(JSON.parse(p.calls[1][1].body),{retention_days:7,confirm_cleanup:true});assert.equal(p.nodes.confirm.checked,false);
  p.nodes.confirm.checked=true;p.nodes.days.oninput();assert.equal(p.nodes.confirm.checked,false,'改天数重新确认');
  warning=true;p.nodes.confirm.checked=true;await p.submit();assert.match(p.nodes.status.textContent,/已保存.*未能清理/);assert.equal(p.nodes.status.role,'alert');
  p.nodes.days.value='0';await p.submit();assert.equal(JSON.parse(p.calls[3][1].body).retention_days,0);
});

test('日志期限读取失败禁保存，503保留草稿，412仅显式重载才解除', async () => {
  let failGet=true, mode=503;
  const p=logPanel(async (_,opts)=>{if(!opts){if(failGet)throw Error('network');return logConfig();}throw Object.assign(Error('save failed'),{status:mode});});
  await p.mounted;assert.equal(p.nodes.save.disabled,true);failGet=false;await p.reload();
  p.nodes.days.value='30';p.nodes.confirm.checked=true;await p.submit();assert.equal(p.nodes.days.value,'30');assert.equal(p.nodes.save.disabled,false);
  mode=412;await p.submit();assert.equal(p.nodes.save.disabled,true);assert.equal(p.nodes.days.value,'30');
  const n=p.calls.length;await p.submit();assert.equal(p.calls.length,n);p.confirm(false);await p.reload();assert.equal(p.calls.length,n);
  p.confirm(true);await p.reload();assert.equal(p.nodes.days.value,'0');assert.equal(p.nodes.save.disabled,false);
});

test('日志期限单次在途保存与导航晚回隔离', async () => {
  let finish;
  const p=logPanel(async (_,opts)=>opts?new Promise(r=>{finish=r;}):logConfig());await p.mounted;
  p.nodes.days.value='7';p.nodes.confirm.checked=true;const pending=p.submit();await p.submit();assert.equal(p.calls.length,2);
  p.leave();finish(logConfig(7));await pending;assert.equal(p.nodes.confirm.checked,true,'旧响应不改已离开页面');
  let resolve;const q=logPanel(()=>new Promise(r=>{resolve=r;}));q.leave();resolve(logConfig(30));await q.mounted;assert.equal(q.nodes.days.value,'');
});

test('日志期限嵌入资源先加载，管理员设置挂载且不提供任意路径输入', () => {
  assert.ok(html.indexOf('/web/log-settings.js')>=0 && html.indexOf('/web/log-settings.js')<html.indexOf('/web/app.js'));
  assert.match(source,/mountLogSettings\(logHost, api, \(\) => gen === pageGen && logHost.isConnected\)/);
  assert.match(logSettingsUI,/app-YYYY-MM-DD.log/);assert.match(logSettingsUI,/无法撤销/);
  assert.equal((logSettingsUI.match(/apiRequest\('/g)||[]).length,2);
});

// Review regression tests execute production functions, not only source-presence checks.
function identityAPIFixture() {
  let stored = 'A', removed = 0, reply, jsonReply;
  const redirects = [], calls = [];
  const box = sandbox({ token: 'A', FormData: class {},
    localStorage: { getItem: () => stored, removeItem() { stored = null; removed++; } },
    window: { location: { replace: p => redirects.push(p) } },
    fetch: (path, options) => { calls.push({ path, options }); return new Promise(r => { reply = r; }); } });
  vm.runInContext(section('async function api(', 'const STATUS_TEXT ='), box);
  return { box, calls, redirects, setStored(v) { stored = v; }, get stored() { return stored; }, get removed() { return removed; },
    reply(status, result = {}) { reply({ status, ok: status >= 200 && status < 300, json: async () => result }); },
    deferredJSON() { reply({ status: 200, ok: true, json: () => new Promise(r => { jsonReply = r; }) }); },
    finishJSON(value) { jsonReply(value); } };
}

test('身份变更：旧401、旧200及延迟JSON均不能清新token或交付旧数据', async () => {
  for (const status of [401, 200]) {
    const env = identityAPIFixture(), pending = env.box.api('/apikeys');
    env.setStored('B'); env.reply(status, { key: 'A-secret' });
    await assert.rejects(pending, e => e.code === 'IDENTITY_CHANGED' && e.status === 401);
    assert.equal(env.stored, 'B'); assert.equal(env.removed, 0); assert.deepEqual(env.redirects, []);
    assert.equal(env.calls[0].options.headers['X-Emby-Token'], 'A');
  }
  const env = identityAPIFixture(), pending = env.box.api('/apikeys');
  env.deferredJSON(); await new Promise(setImmediate); env.setStored('B'); env.finishJSON({ key: 'A-secret' });
  await assert.rejects(pending, e => e.code === 'IDENTITY_CHANGED');
  await assert.rejects(env.box.api('/settings'), e => e.code === 'IDENTITY_CHANGED');
  assert.equal(env.calls.length, 1, '旧标签不再发新业务请求');
});

for (const kind of ['admin', 'login']) test(`${kind} 旧启动401/200不覆盖或清除另一个标签的新身份`, async () => {
  for (const status of [401, 200]) {
    const env = authBootSandbox(kind, 401); let reply;
    env.box.fetch = () => new Promise(r => { reply = r; });
    const pending = env.boot(); env.setStored('B'); reply({ status, ok: status === 200 }); await pending;
    assert.equal(env.stored, 'B'); assert.equal(env.removed, 0); assert.deepEqual(env.redirects, []); assert.equal(env.rendered, 0);
  }
});

function logoutFixture() {
  let stored = 'A', finish, cleanups = 0;
  const calls = [], redirects = [], errors = [];
  const box = sandbox({ token: 'A', createdAPIKey: 'raw-secret', me: { is_admin: false }, pageGen: 1, wallGen: 2,
    scanWallRefresh: {}, content: { innerHTML: 'private page' }, scanPanel: { hidden: false },
    localStorage: { getItem: () => stored, removeItem() { stored = null; } },
    fetch: (path, options) => { calls.push({ path, options }); return new Promise(r => { finish = r; }); },
    window: { location: { replace: p => redirects.push(p) } }, toast: (...a) => errors.push(a),
    stopScrapePolling() { cleanups++; }, stopProbePolling() { cleanups++; }, stopScanPolling() { cleanups++; },
    cancelProbeHide() {}, cancelScanHide() {}, closeOverlaysForNavigation() { cleanups++; } });
  vm.runInContext(section('function clearSessionView()', 'async function boot()'), box);
  return { box, calls, redirects, errors, setStored(v) { stored = v; }, get stored() { return stored; }, get cleanups() { return cleanups; },
    finish(status) { finish({ status, ok: status >= 200 && status < 300 }); } };
}

test('普通用户退出当前会话只POST当前凭据，单次在途，成功清敏感DOM/观察器', async () => {
  const env = logoutFixture(), button = { disabled: false };
  const pending = env.box.logoutCurrentSession(button); await env.box.logoutCurrentSession(button);
  assert.equal(env.calls.length, 1); assert.equal(env.calls[0].path, '/api/auth/logout');
  assert.equal(env.calls[0].options.headers['X-Emby-Token'], 'A'); assert.equal(env.calls[0].options.method, 'POST');
  env.finish(204); await pending;
  assert.equal(env.stored, null); assert.equal(env.box.createdAPIKey, null); assert.equal(env.box.content.innerHTML, '');
  assert.equal(env.box.pageGen, 2); assert.equal(env.box.wallGen, 3); assert.equal(env.cleanups, 4);
  assert.deepEqual(env.redirects, ['/']); assert.match(html, /id="logout"[^>]*aria-label="退出当前会话"/);
});

test('退出迟到204/401不清B；退出503保留A并允许手动重试', async () => {
  for (const status of [204, 401]) {
    const env = logoutFixture(), pending = env.box.logoutCurrentSession({ disabled: false });
    env.setStored('B'); env.finish(status); await pending;
    assert.equal(env.stored, 'B'); assert.equal(env.cleanups, 0); assert.deepEqual(env.redirects, []);
  }
  const env = logoutFixture(), button = { disabled: false }, pending = env.box.logoutCurrentSession(button);
  env.finish(503); await pending; assert.equal(env.stored, 'A'); assert.equal(button.disabled, false); assert.equal(env.errors.length, 1);
});

test('跨标签身份切换与storage.clear主动销毁旧页面、密钥及扫描上下文', () => {
  for (const key of ['emby_token', null]) {
    const env = logoutFixture(); env.setStored('B'); env.box.identityStorageChanged({ key });
    assert.equal(env.stored, 'B'); assert.equal(env.box.createdAPIKey, null); assert.equal(env.box.scanWallRefresh, null);
    assert.equal(env.box.content.innerHTML, ''); assert.deepEqual(env.redirects, ['/']);
  }
});

test('密钥明文只在原创建代际显示；离页/返回与换身份后的创建回包均不复活', async () => {
  for (const change of ['none', 'navigate', 'identity']) {
    let stored = 'A', finish, submit, refreshes = 0;
    const box = sandbox({ token: 'A', gen: 1, pageGen: 1, createdAPIKey: null, content: {},
      FormData: class { get() { return 'name'; } }, localStorage: { getItem: () => stored },
      api: () => new Promise(r => { finish = r; }), toast() {}, pageSettings() {},
      refreshPage() { refreshes++; }, document: { querySelector: () => ({ addEventListener(_, fn) { submit = fn; } }) },
      pages: { items: async () => {}, settings: async () => {} }, stopScrapePolling() {}, stopProbePolling() {},
      cancelProbeHide() {}, cancelScanHide() {}, setMeta() {}, skeletonPanel: () => '' });
    vm.runInContext(section("  document.querySelector('#apikey-form').addEventListener", '  const logoutAll = document.querySelector'), box);
    vm.runInContext(section('async function page(name)', '/* ------------------------------------------------------------ 媒体信息探测'), box);
    const pending = submit({ preventDefault() {}, target: {} });
    if (change === 'navigate') { await box.page('items'); await box.page('settings'); }
    if (change === 'identity') stored = 'B';
    finish({ key: 'raw-secret' }); await pending;
    assert.equal(box.createdAPIKey, change === 'none' ? 'raw-secret' : null); assert.equal(refreshes, change === 'none' ? 1 : 0);
    await box.page('items'); assert.equal(box.createdAPIKey, null);
  }
});

test('扫描本次running→终态刷新一次；换页/筛选/身份不刷新新上下文', async () => {
  for (const change of ['none', 'page', 'wall', 'identity']) {
    let reads = 0;
    const env = scanFixture(async () => reads++ ? { ...scanTerminal, task_id: 7 } : { running: true, task_id: 7 },
      async () => ({ task_id: 7 }));
    await env.box.runScan(); assert.equal(env.reloads, 1);
    await env.fire(0);
    if (change === 'page') env.box.pageGen++;
    if (change === 'wall') env.box.wallGen++;
    if (change === 'identity') env.box.token = 'B';
    await env.fire(1500);
    assert.equal(env.reloads, change === 'none' ? 2 : 1);
    assert.equal(env.posts().length, 1); assert.equal(env.has(1500), false);
  }
});

test('扫描202首读历史终态必须按task_id确认；快速完成未见running也刷新', async () => {
  let taskReads = 0;
  const env = scanFixture(async () => scanTerminal, async () => ({ task_id: 7 }), async (path) => {
    assert.equal(path, '/tasks/7'); return taskReads++ ? { status: 'succeeded', finished_at: 'new' } : { status: 'queued' };
  });
  await env.box.runScan(); await env.fire(0);
  assert.equal(env.reloads, 1); assert.equal(env.node('#scan').disabled, true); assert.equal(env.has(1500), true);
  await env.fire(1500); assert.equal(env.reloads, 2); assert.equal(env.node('#scan').disabled, false);
  assert.equal(env.posts().length, 1); assert.equal(taskReads, 2);
  const fast = scanFixture(async () => scanTerminal, async () => ({ task_id: 8 }));
  await fast.box.runScan(); await fast.fire(0); assert.equal(fast.reloads, 2);
});

test('运行日志页转义日志文本并支持级别筛选，切页后晚回不写新页面', async () => {
  const nodes = new Map();
  const make = id => ({ id, isConnected: true, textContent: '', innerHTML: '', hidden: false, value: '',
    scrollTop: 0, scrollHeight: 100, clientHeight: 50, handlers: {},
    addEventListener(type, fn) { this.handlers[type] = fn; }, setAttribute() {}, style: {} });
  const content = {
    _html: '',
    get innerHTML() { return this._html; }, set innerHTML(value) { this._html = value; },
    querySelector(selector) { if (!nodes.has(selector)) nodes.set(selector, make(selector)); return nodes.get(selector); }
  };
  const timers = [], listeners = {};
  const esc = text => String(text).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  const box = sandbox({ me: { is_admin: true }, pageGen: 1, token: 'fixture-token', esc, empty: (a, b) => `${a} ${b}`,
    content, document: { hidden: false, addEventListener(type, fn) { listeners[type] = fn; }, removeEventListener() {}, querySelector: () => null },
    setTimeout: (fn, delay) => { timers.push([fn, delay]); return timers.length; }, clearTimeout() {},
    api: async () => ({ items: [{ id: '1', time: '2026-10-23T10:00:00Z', level: 'INFO', message: '<script>x</script> token=ABC' }],
      next_after: 'n1', next_before: 'b1', has_more: true }) });
  vm.runInContext(section('let logsTimer = null;', '/* ---------------------------------------------------------------- 路由 */'), box);
  await box.pageLogs();
  assert.match(nodes.get('#dl-body').innerHTML, /&lt;script&gt;/);
  assert.doesNotMatch(nodes.get('#dl-body').innerHTML, /<script>/);
  assert.equal(nodes.get('#dl-earlier').hidden, false);
  nodes.get('#dl-level').value = 'warn';
  nodes.get('#dl-level').handlers.change({ target: { value: 'warn' } });
  assert.doesNotMatch(nodes.get('#dl-body').innerHTML, /token=ABC/);
  assert.equal(timers.filter(([, delay]) => delay === 1000).length, 1, '只安排下一次增量轮询');
  box.stopLogsPolling();
  assert.equal(timers.filter(([, delay]) => delay === 1000).length, 1);
});

test('运行日志首读在切页后不渲染旧响应', async () => {
  const nodes = new Map();
  const make = id => ({ id, isConnected: true, textContent: '', innerHTML: '', hidden: false,
    scrollTop: 0, scrollHeight: 100, clientHeight: 50, handlers: {},
    addEventListener(type, fn) { this.handlers[type] = fn; }, setAttribute() {}, style: {} });
  const content = {
    innerHTML: '', querySelector(selector) { if (!nodes.has(selector)) nodes.set(selector, make(selector)); return nodes.get(selector); }
  };
  let resolve;
  const box = sandbox({ me: { is_admin: true }, pageGen: 1, token: 'fixture-token', esc: String, empty: () => '',
    content, document: { hidden: false, addEventListener() {}, removeEventListener() {}, querySelector: () => null },
    setTimeout: () => 0, clearTimeout() {}, api: () => new Promise(r => { resolve = r; }) });
  vm.runInContext(section('let logsTimer = null;', '/* ---------------------------------------------------------------- 路由 */'), box);
  const loading = box.pageLogs();
  box.pageGen = 2;
  resolve({ items: [{ id: 'old', time: '2026-10-23T10:00:00Z', level: 'INFO', message: 'old-response' }], next_after: 'x' });
  await loading;
  assert.equal(nodes.get('#dl-body').innerHTML, '', '旧页面响应不得写入新页面');
});

test('刮削inspect失败保留已有候选并提供重试，首次失败同样可重试，旧失败不可重绘', async () => {
  for (const initial of [false, true]) {
    let attempts = 0, retry, renders = 0;
    const state = { movieId: 1, inspectSeq: 0, preview: {}, inspect: initial ? null : { title: 'old' } };
    const body = { innerHTML: 'candidates', insertAdjacentHTML(_, markup) { this.innerHTML = markup + this.innerHTML; },
      querySelector() { return { addEventListener(_, fn) { retry = fn; } }; } };
    const box = sandbox({ scrapeState: state, document: { querySelector: () => body }, esc: String,
      renderScrapePreview() { renders++; body.innerHTML = 'candidates'; },
      api: async () => { if (!attempts++) throw Error('offline'); return { title: 'new' }; } });
    vm.runInContext(section('async function loadScrapeInspect(', 'function renderScrapePreview()'), box);
    await box.loadScrapeInspect('p', 'x'); assert.match(body.innerHTML, /role="alert"/); assert.match(body.innerHTML, /重试读取详情/);
    if (!initial) assert.match(body.innerHTML, /candidates/);
    retry(); await new Promise(setImmediate); assert.equal(state.inspect.title, 'new'); assert.ok(renders >= 1);
    let fail; box.api = () => new Promise((_, reject) => { fail = reject; });
    const old = box.loadScrapeInspect('p', 'old'); box.scrapeState = null; body.innerHTML = 'new page'; fail(Error('late'));
    await old; assert.equal(body.innerHTML, 'new page');
  }
});

test('Chromium真实CSS：管理按钮命中不再播放，hidden字段不占位，标题与播放可聚焦', t => {
  const { spawnSync } = require('node:child_process'), os = require('node:os');
  const chrome = process.env.CHROME_BIN || '/root/.cache/ms-playwright/chromium-1248/chrome-linux64/chrome';
  if (!fs.existsSync(chrome)) { t.skip('未安装浏览器；不自动安装'); return; }
  const box = sandbox({ me: { is_admin: true }, esc: String, icon: () => '', numberUnlessInTitle: () => '', STATUS_TEXT: {} });
  vm.runInContext(section('function wallCard(', 'const WALL_PAGE_SIZE'), box);
  const card = box.wallCard({ id: 1, status: 'success', Title: 'test' }, null);
  const css = fs.readFileSync(path.join(__dirname, 'web/style.css'), 'utf8');
  const doc = `<!doctype html><meta charset="utf-8"><style>${css}</style><div class="wall is-selecting" style="margin:20px;width:260px">${card}<div class="field" hidden id="f">probe<input></div></div><script>
    const f=document.querySelector('#f'); const hits=['probe','reread','delete'].map(k=>{const b=document.querySelector('[data-'+k+']'),r=b.getBoundingClientRect(),h=document.elementFromPoint(r.x+r.width/2,r.y+r.height/2);return h===b;});
    const title=document.querySelector('.wall-title'),play=document.querySelector('.wall-play'); title.focus();const titleFocus=document.activeElement===title;play.focus();
    const selected=document.querySelector('[data-translate-select]'),r=selected.getBoundingClientRect(),selectionHit=document.elementFromPoint(r.x+r.width/2,r.y+r.height/2)===selected;
    const out={width:innerWidth,selectionHit,display:getComputedStyle(f).display,height:f.getBoundingClientRect().height,hits,titleFocus,playFocus:document.activeElement===play};document.head.innerHTML='';document.body.innerHTML='<pre id="result"></pre>';document.querySelector('#result').textContent=JSON.stringify(out);</script>`;
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), 'emby-css-regression-'));
  try {
    for (const width of [1280, 500]) {
      const run = spawnSync(chrome, ['--headless', '--no-sandbox', '--disable-gpu', '--no-first-run', '--user-data-dir=' + profile,
        '--window-size=' + width + ',900', '--dump-dom', 'data:text/html;base64,' + Buffer.from(doc).toString('base64')], { encoding: 'utf8', timeout: 20000 });
      assert.equal(run.status, 0, run.stderr); const match = run.stdout.match(/<pre id="result">([^<]+)<\/pre>/); assert.ok(match, run.stdout);
      const actual = JSON.parse(match[1]); assert.equal(actual.width, width); assert.deepEqual(actual.hits, [true,true,true]); assert.equal(actual.selectionHit, true);
      assert.equal(actual.display, 'none'); assert.equal(actual.height, 0); assert.equal(actual.titleFocus, true); assert.equal(actual.playFocus, true);
    }
  } finally {
    assert.equal(path.dirname(profile), os.tmpdir()); assert.ok(path.basename(profile).startsWith('emby-css-regression-'));
    fs.rmSync(profile, { recursive: true, force: true });
  }
});

test('扫描任务终态保持failed数字计数；任务failed/skipped及成功带非致命失败都不自动隐藏', async () => {
  for (const status of ['failed', 'skipped', 'succeeded']) {
    const env = scanFixture(async () => ({ ...scanTerminal, failed: 3 }), async () => ({ task_id: 9 }),
      async () => ({ task_id: 9, status, finished_at: 'new', result: { failed: 3 }, error: status === 'failed' ? 'disk error' : '' }));
    await env.box.runScan(); await env.fire(0);
    assert.match(env.node('#scan-progress-title').textContent, /存在错误/);
    assert.match(env.node('#scan-progress-detail').textContent, /失败 3/);
    assert.doesNotMatch(env.node('#scan-progress-detail').textContent, /失败 true/);
    assert.equal(env.has(4000), false); assert.equal(env.node('#scan-progress').hidden, false);
    assert.equal(env.reloads, 2);
  }
  const failed = scanFixture(async () => scanTerminal, async () => ({ task_id: 10 }), async () => ({ status: 'failed', finished_at: 'new' }));
  await failed.box.runScan(); await failed.fire(0);
  assert.match(failed.node('#scan-progress-detail').textContent, /扫描任务未成功完成/);
  assert.equal(failed.has(4000), false);
});

test('扫描task_id确认失败沿统一退避，停止观察会中止任务GET且晚回不重绘', async () => {
  let attempts = 0;
  const retry = scanFixture(async () => scanTerminal, async () => ({ task_id: 11 }), async () => {
    if (!attempts++) throw Object.assign(Error('unavailable'), { status: 503 });
    return { status: 'succeeded', finished_at: 'new' };
  });
  await retry.box.runScan(); await retry.fire(0); assert.equal(retry.has(3000), true); assert.equal(retry.reloads, 1);
  await retry.fire(3000); assert.equal(retry.reloads, 2); assert.equal(retry.posts().length, 1);
  let finish, signal;
  const stopped = scanFixture(async () => scanTerminal, async () => ({ task_id: 12 }), (_, opts) => {
    signal = opts.signal; return new Promise(r => { finish = r; });
  });
  await stopped.box.runScan(); const pending = stopped.fire(0);
  await new Promise(setImmediate); stopped.box.stopScanPolling(); assert.equal(signal.aborted, true);
  const before = stopped.snapshot(); finish({ status: 'succeeded', finished_at: 'new' }); await pending;
  assert.equal(stopped.snapshot(), before); assert.equal(stopped.reloads, 1); assert.equal(stopped.has(1500), false);
});

function translationFixture(read = async () => ({ running: false }), post = async () => ({ task_id: 7 })) {
  const clock = controlledClock(), nodes = new Map(), calls = [], confirmations = [];
  const node = key => {
    if (!nodes.has(key)) nodes.set(key, { checked: key.includes('"title"'), disabled: false, textContent: '', handlers: {},
      addEventListener(event, fn) { this.handlers[event] = fn; } });
    return nodes.get(key);
  };
  const root = { querySelector: node };
  const box = sandbox({ pageGen: 1, scopeCurrent: true, completed: 0, AbortController,
    setTimeout: clock.setTimeout, clearTimeout: clock.clearTimeout, esc: String,
    confirm: text => { confirmations.push(text); return box.confirmed !== false; },
    api: async (url, opts = {}) => {
      calls.push({ url, ...opts });
      if (url === '/translate/run') return post(url, opts);
      if (url === '/translate/cancel') return { running: true };
      return read(url, opts);
    } });
  vm.runInContext(section('function startProgressPolling(', 'function stopScrapePolling()'), box);
  vm.runInContext(section('const TRANSLATE_HINT =', '/* ------------------------------------------------------------ 计划任务 */'), box);
  box.scope = { movie_ids: [1, 2] };
  const halt = box.mountTranslation(root, () => box.scope, () => box.scopeCurrent, () => box.completed++);
  return { ...clock, box, calls, confirmations, node, halt, root, click: key => node(`[data-translate-${key}]`).handlers.click() };
}

test('独立翻译payload严格范围/字段/limit；空选片不能退化全库，IDs不携带library_id', () => {
  const env = translationFixture();
  const payload = env.box.translationPayload({ movie_ids: [1, 1, 2], library_id: 9 }, ['title'], 10);
  assert.deepEqual(JSON.parse(JSON.stringify(payload)), { movie_ids: [1, 2], fields: ['title'], limit: 10 });
  for (const scope of [{ movie_ids: [] }, { movie_ids: [-1] }, { movie_ids: Array.from({ length: 1001 }, (_, i) => i + 1) }]) {
    assert.throws(() => env.box.translationPayload(scope, ['title']), /请选择/);
  }
  assert.throws(() => env.box.translationPayload({}, []), /字段/);
  assert.throws(() => env.box.translationPayload({}, ['plot'], 0.5), /整数/);
  assert.throws(() => env.box.translationPayload({}, ['title'], 100001), /整数/);
  assert.equal(env.box.translationPayload({ library_id: 8 }, ['plot']).library_id, 8);
  const html = env.box.translationWidget('x');
  assert.match(html, /data-translate-field="title" checked/); assert.doesNotMatch(html, /data-translate-field="plot" checked/);
  assert.match(html, /originaltitle/); assert.match(html, /可能已经是译文/); assert.match(html, /字段锁/);
  env.halt();
});

test('翻译提交前明确范围确认，单次POST，仅标题默认，不刮削/取图；终态按精确task_id刷新一次', async () => {
  let state = { running: false };
  const env = translationFixture(async () => state);
  await env.fire(0);
  await env.click('start'); await env.click('start');
  assert.equal(env.calls.filter(c => c.url === '/translate/run').length, 1);
  assert.match(env.confirmations[0], /所选 2 部影片/);
  assert.match(env.confirmations[0], /不重新刮削、不取图/);
  const body = JSON.parse(env.calls.find(c => c.url === '/translate/run').body);
  assert.deepEqual(body, { fields: ['title'], limit: 0, movie_ids: [1, 2] });
  state = { task_id: 7, running: false, success: 2, done: 2, total: 2, finished_at: 'now' };
  await env.fire(0);
  assert.equal(env.calls.at(-1).url, '/translate/status?task_id=7'); assert.equal(env.box.completed, 1);
  assert.equal(env.has(1500), false); assert.match(env.node('[data-translate-progress]').textContent, /成功 2/);
});

test('取消翻译携带观察任务ID，等待真实终态；替换任务/404不刷新、不取消新任务', async () => {
  let state = { task_id: 7, running: true };
  const env = translationFixture(async () => state);
  await env.fire(0); await env.click('cancel');
  assert.deepEqual(JSON.parse(env.calls.find(c => c.url === '/translate/cancel').body), { task_id: 7 });
  state = { task_id: 7, running: false, cancelled: true, done: 0 };
  await env.fire(0); assert.match(env.node('[data-translate-progress]').textContent, /已取消/); assert.equal(env.box.completed, 0);
  const other = translationFixture(async url => url.includes('?') ? { task_id: 99, running: true } : { running: false });
  await other.fire(0); await other.click('start'); await other.fire(0);
  assert.match(other.node('[data-translate-progress]').textContent, /已被替换/); assert.equal(other.box.completed, 0);
  await other.click('cancel'); assert.equal(other.calls.filter(c => c.url.includes('/cancel')).length, 0);
  other.halt();
  const missing = translationFixture(async url => { if (url.includes('?')) throw Object.assign(Error('gone'), { status: 404 }); return { running: false }; });
  await missing.fire(0); await missing.click('start'); await missing.fire(0);
  assert.equal(missing.has(3000), false); assert.equal(missing.box.completed, 0); assert.match(missing.node('[data-translate-progress]').textContent, /快照/);
});

test('翻译状态503退避只GET，10秒软超时/导航中止；旧POST成功不复活，旧终态不刷新新scope', async () => {
  let attempts = 0;
  const retry = translationFixture(async () => { if (!attempts++) throw Object.assign(Error('unavailable'), { status: 503 }); return { running: false }; });
  await retry.fire(0); assert.equal(retry.has(3000), true); assert.equal(retry.node('[data-translate-start]').disabled, true);
  await retry.fire(3000); assert.equal(retry.calls.filter(c => c.method === 'POST').length, 0); retry.halt();
  let finishRead, signal;
  const stopped = translationFixture((_, opts) => { signal = opts.signal; return new Promise(r => { finishRead = r; }); });
  const pending = stopped.fire(0); await new Promise(setImmediate); assert.equal(stopped.has(10000), true);
  stopped.box.stopTranslationPolling(); assert.equal(signal.aborted, true);
  const before = stopped.node('[data-translate-progress]').textContent; finishRead({ running: false, task_id: 7, success: 2 }); await pending;
  assert.equal(stopped.node('[data-translate-progress]').textContent, before); assert.equal(stopped.box.completed, 0);
  for (const change of [env => env.box.pageGen++, env => env.box.scopeCurrent = false, env => env.box.localStorage = { getItem: () => 'B' }]) {
    let finishPost;
    const late = translationFixture(async () => ({ running: false }), () => new Promise(r => { finishPost = r; }));
    await late.fire(0); const post = late.click('start'); change(late); finishPost({ task_id: 7 }); await post;
    assert.equal(late.has(0), false); assert.equal(late.box.completed, 0);
    const end = translationFixture(async url => url.includes('?') ? { running: false, task_id: 7, success: 1 } : { running: false });
    await end.fire(0); await end.click('start'); change(end); await end.fire(0); assert.equal(end.box.completed, 0);
    end.halt();
  }
});

test('翻译表单空字段/空ID不POST、取消范围确认不POST；计划保留unknown与ETag，换类型不泄漏', async () => {
  const env = translationFixture(); await env.fire(0);
  env.box.scope = { movie_ids: [] }; await env.click('start'); assert.match(env.node('[data-translate-progress]').textContent, /请选择/);
  env.box.scope = { movie_ids: [1] }; env.node('[data-translate-field="title"]').checked = false;
  await env.click('start'); assert.match(env.node('[data-translate-progress]').textContent, /至少选择/);
  env.node('[data-translate-field="plot"]').checked = true; env.box.confirmed = false; await env.click('start');
  assert.equal(env.calls.filter(c => c.method === 'POST').length, 0); env.halt();
  const box = sandbox({}); vm.runInContext(section('function scheduledFormParams(', 'async function pageScheduled('), box);
  const params = box.scheduledFormParams('translate', { type: 'translate' }, { fields: ['plot'], limit: 20, future: false }, { fields: ['title'], limit: 10, library_id: 3 });
  assert.deepEqual(JSON.parse(JSON.stringify(params)), { fields: ['title'], limit: 10, future: false, library_id: 3 });
  assert.deepEqual(JSON.parse(JSON.stringify(box.scheduledFormParams('translate', { type: 'probe' }, { status: 'all', only_missing: false }, { fields: ['title'], limit: 0, library_id: 0 }))), { fields: ['title'], limit: 0, library_id: 0 });
  assert.match(source, /!editing && kind === 'translate'.*checked = false/);
  assert.match(source, /取消“保存后参与调度”/); assert.match(source, /If-Match.*editing.config_etag/);
  assert.match(source, /data-show="translate"/);
});

test('翻译全skip/nochange仍刷新修复的索引；失败保留真实计数，取消无done不刷新', async () => {
  for (const summary of [{ success: 0, skipped: 2, done: 2 }, { success: 0, failed: 1, done: 1, error: 'failure' }]) {
    const env = translationFixture(async url => url.includes('?') ? { task_id: 7, running: false, finished_at: 'now', ...summary } : { running: false });
    await env.fire(0); await env.click('start'); await env.fire(0);
    assert.equal(env.box.completed, 1); assert.equal(env.node('[data-translate-start]').disabled, false);
    assert.match(env.node('[data-translate-progress]').textContent, /翻译结束/);
  }
});

test('翻译读取403停止、悬挂10秒中止退避；提交503保留选择且绝不自动重放POST', async () => {
  const denied = translationFixture(async () => { throw Object.assign(Error('forbidden'), { status: 403 }); });
  await denied.fire(0); assert.equal(denied.has(3000), false); assert.match(denied.node('[data-translate-progress]').textContent, /读取受限/);
  let signal, fail;
  const timeout = translationFixture((_, opts) => { signal = opts.signal; return new Promise((resolve, reject) => { fail = reject; }); });
  const request = timeout.fire(0); await new Promise(setImmediate);
  timeout.fire(10000); assert.equal(signal.aborted, true); fail(Error('aborted')); await request;
  assert.equal(timeout.has(3000), true); assert.match(timeout.node('[data-translate-progress]').textContent, /状态未知/); timeout.halt();
  const post = translationFixture(async () => ({ running: false }), async () => { throw Object.assign(Error('unavailable'), { status: 503 }); });
  await post.fire(0); await post.click('start'); await post.fire(0);
  assert.equal(post.calls.filter(c => c.url === '/translate/run').length, 1);
  assert.equal(post.node('[data-translate-field="title"]').checked, true);
  assert.equal(post.box.scope.movie_ids.length, 2); assert.equal(post.box.completed, 0);
});

test('真实翻译计划PATCH保留unknown/limit/fields与If-Match；412禁旧重提、空字段拒绝提交', async () => {
  for (const failure of [0, 412]) {
    const env = scheduledSubmitFixture({ kind: 'translate', params: { future: { preserve: true }, limit: 50, fields: ['plot'] }, failure });
    env.box.editing.type = 'translate';
    env.fields['#s-translate-title'] = { checked: true }; env.fields['#s-translate-plot'] = { checked: false }; env.fields['#s-translate-limit'] = { value: '20' };
    vm.runInContext(section('function translationPayload(', 'function translationWidget('), env.box);
    await env.submit();
    assert.deepEqual(env.calls[0].body.params, { future: { preserve: true }, limit: 20, fields: ['title'], library_id: 7 });
    assert.equal(env.calls[0].headers['If-Match'], '"sc1-fixture"'); assert.equal(env.calls[0].body.enabled, false);
    if (failure) { assert.equal(env.fields['#sched-conflict'].hidden, false); await env.submit(); assert.equal(env.calls.length, 1); }
  }
  const empty = scheduledSubmitFixture({ kind: 'translate' });
  empty.fields['#s-translate-title'] = { checked: false }; empty.fields['#s-translate-plot'] = { checked: false }; empty.fields['#s-translate-limit'] = { value: '0' };
  vm.runInContext(section('function translationPayload(', 'function translationWidget('), empty.box);
  await empty.submit(); assert.equal(empty.calls.length, 0); assert.match(empty.messages[0], /字段/);
  const visible = { library: [{ hidden: false }], scan: [{ hidden: false }], probe: [{ hidden: false }], translate: [{ hidden: true }] }, enabled = { checked: true };
  const box = sandbox({ editing: null, typeEl: { value: 'translate' }, document: { querySelector: () => enabled, querySelectorAll: selector => visible[selector.match(/"([^"]+)"/)[1]] } });
  vm.runInContext(section('  function syncParams()', '  // 即时校验'), box); box.syncParams();
  assert.equal(enabled.checked, false); assert.equal(visible.probe[0].hidden, true); assert.equal(visible.translate[0].hidden, false);
  box.typeEl.value = 'probe'; box.syncParams(); assert.equal(visible.translate[0].hidden, true); assert.equal(visible.probe[0].hidden, false);
});

test('媒体墙轻量多选不触发详情/播放，最多1000，搜索reload清空旧选片', async () => {
  const env = await wallKeyboardFixture();
  const input = { checked: true, dataset: { translateSelect: '7' }, matches: selector => selector === 'input' };
  const target = { closest: selector => selector.includes('data-translate-select') ? input : null };
  await env.handlers.click({ target, stopPropagation() {} });
  assert.equal(env.box.wallState.translateSelection.has('7'), true); assert.equal(env.calls.length, 0);
  input.checked = false; await env.handlers.click({ target, stopPropagation() {} }); assert.equal(env.box.wallState.translateSelection.size, 0);
  for (let i = 1; i <= 1000; i++) env.box.wallState.translateSelection.add(String(i));
  input.dataset.translateSelect = '1001'; input.checked = true;
  await env.handlers.click({ target, stopPropagation() {} }); assert.equal(input.checked, false); assert.equal(env.box.wallState.translateSelection.size, 1000);
  const count = { textContent: '' }, query = env.box.document.querySelector;
  env.box.document.querySelector = id => id === '#wall-translate-count' ? count : query(id);
  vm.runInContext(section('function reloadWall()', '// wallMaybeLoadMore'), env.box); env.box.reloadWall();
  assert.equal(env.box.wallState.translateSelection.size, 0); assert.match(count.textContent, /已选 0 部/); assert.equal(env.calls.length, 0);
});

test('手动执行禁调度的翻译计划也必须确认范围，保持enabled；损坏参数拒绝执行', async () => {
  const task = { id: 42, name: '手动翻译', type: 'translate', enabled: false, params: JSON.stringify({ library_id: 7, fields: ['plot'], limit: 20 }) };
  let click; const calls = [], confirmations = [], messages = [];
  const box = sandbox({ id: '42', items: [task], TRANSLATE_HINT: 'source hint', accepted: false,
    el: { disabled: false, addEventListener(_, fn) { click = fn; } },
    confirm: text => { confirmations.push(text); return box.accepted; },
    gen: 1, pageGen: 1, scheduledRunHolds: new Map(), scheduledWatchStop: null,
    document: { querySelector: () => ({ textContent: '', isConnected: true }) },
    setTimeout: () => 1, clearTimeout() {}, renderTaskLog: async () => {},
    api: async (url, options) => { calls.push({ url, ...options }); return {}; }, toast: message => messages.push(message) });
  vm.runInContext(section('function translationPayload(', 'function translationWidget('), box);
  vm.runInContext(section('      const holdKey =', "    } else if (role === 'edit')"), box);
  await click(); assert.equal(calls.length, 0); assert.match(confirmations[0], /媒体库 7/); assert.match(confirmations[0], /plot/); assert.match(confirmations[0], /20/);
  box.accepted = true; await click(); assert.equal(calls[0].url, '/scheduled/42/run'); assert.equal(task.enabled, false);
  box.scheduledRunHolds.clear(); box.el.disabled = false;
  task.params = '[]'; const before = calls.filter(c => c.url.endsWith('/run')).length;
  await click(); assert.equal(calls.filter(c => c.url.endsWith('/run')).length, before); assert.match(messages.at(-1), /参数无法确认/);
});

function scheduledRunFixture({ failure = 0, observeFailure = false, pending = false } = {}) {
  let click, resolvePost, now = 1000, next = 0;
  const timers = new Map(), calls = [], messages = [], signals = [];
  const status = { textContent: '', isConnected: true }, cell = { innerHTML: '' };
  const el = { disabled: false, isConnected: true, addEventListener(_, fn) { click = fn; } };
  const box = sandbox({ id: '42', items: [{ id: 42, type: 'scan' }], gen: 1, el,
    Date: { now: () => now }, scheduledRunHolds: new Map(), scheduledWatchStop: null,
    setTimeout(fn, delay) { const id = ++next; timers.set(id, { fn, delay }); return id; }, clearTimeout(id) { timers.delete(id); },
    document: { querySelector: selector => selector === '#scheduled-observation' ? status : cell },
    renderTaskLog: async options => { signals.push(options.signal); if (observeFailure) throw Error('offline'); },
    api: async (url, options = {}) => { calls.push({ url, options });
      if (url.endsWith('/run')) { if (pending) await new Promise(resolve => { resolvePost = resolve; }); if (failure) throw Object.assign(Error('rejected'), { status: failure }); return {}; }
      return { items: [{ id: 42, last_status: 'success', last_message: 'latest', last_run_at: '' }] };
    }, runBadge: value => value, esc: String, fmtTime: String, toast: message => messages.push(message) });
  vm.runInContext(section('      const holdKey =', "    } else if (role === 'edit')"), box);
  return { box, el, status, cell, calls, messages, signals, timers, click: () => click(), resolve: () => resolvePost(),
    async flush() { for (let i = 0; i < 8; i++) await Promise.resolve(); },
    async tick(delay) { now += delay; const due = [...timers].filter(([, t]) => t.delay === delay); for (const [id, timer] of due) { timers.delete(id); timer.fn(); } await this.flush(); } };
}

test('计划执行单次POST、接受不等于完成，刷新最近记录并有界观察和解锁', async () => {
  const env = scheduledRunFixture({ pending: true }); const first = env.click(); await env.click();
  assert.equal(env.calls.length, 1); assert.equal(env.el.disabled, true);
  env.resolve(); await first; await env.flush(); await env.click();
  assert.equal(env.calls.filter(c => c.url.endsWith('/run')).length, 1);
  assert.match(env.status.textContent, /不等于执行成功/); assert.match(env.status.textContent, /不能确认/);
  assert.match(env.cell.innerHTML, /latest/); assert.equal(env.calls.filter(c => c.url === '/scheduled').length, 1);
  await env.tick(5000); assert.equal(env.calls.filter(c => c.url === '/scheduled').length, 2);
  await env.tick(30000); assert.match(env.status.textContent, /观察已结束/);
  assert.equal(env.signals[0].aborted, true); assert.equal(env.box.scheduledWatchStop, null);
  const reads = env.calls.length; await env.tick(5000); assert.equal(env.calls.length, reads);
  await env.tick(30001); assert.equal(env.el.disabled, false);
});

test('计划观察读取失败不误报执行失败，网络未知不重发，明确4xx才可重试', async () => {
  const read = scheduledRunFixture({ observeFailure: true }); await read.click(); await read.flush();
  assert.match(read.status.textContent, /观察读取失败/); assert.match(read.status.textContent, /不是执行失败/);
  assert.equal(read.box.scheduledWatchStop, null); assert.equal(read.el.disabled, true);
  for (const failure of [500, 409]) {
    const env = scheduledRunFixture({ failure }); await env.click(); await env.flush();
    assert.match(env.status.textContent, failure === 500 ? /结果未知/ : /未被接受/);
    assert.equal(env.el.disabled, failure === 500); assert.equal(env.calls.length, 1);
    if (failure === 500) { await env.click(); assert.equal(env.calls.length, 1); }
  }
});

test('计划POST与观察跨页面或身份退出后不得更新反馈，停止时中断读取', async () => {
  for (const change of [env => env.box.pageGen++, env => { env.box.localStorage = { getItem: () => 'other' }; }]) {
    const env = scheduledRunFixture({ pending: true }); const request = env.click(); change(env); env.resolve(); await request;
    assert.equal(env.status.textContent, ''); assert.equal(env.calls.length, 1); assert.equal(env.messages.length, 0);
  }
  const env = scheduledRunFixture(); await env.click(); await env.flush();
  env.box.scheduledWatchStop(); assert.equal(env.signals[0].aborted, true);
  const reads = env.calls.length; await env.tick(5000); assert.equal(env.calls.length, reads);
});

test('无浮层提示不查询空选择器，错误持久且成功不覆盖浮层错误', () => {
  const nodes = [], feedback = { hidden: true, classList: { contains: () => true }, textContent: 'keep' };
  const box = sandbox({ document: { createElement: () => ({ classList: { add() {} }, setAttribute() {}, addEventListener() {}, appendChild() {} }),
    querySelector: selector => { assert.notEqual(selector, ''); return { querySelector: () => feedback }; } },
    topOverlaySelector: () => '', toasts: { appendChild: node => nodes.push(node) }, setTimeout() { throw Error('error must persist'); } });
  vm.runInContext(section('function toast(', '// debounce'), box); box.toast('persistent', 'error'); assert.equal(nodes.length, 1);
  box.topOverlaySelector = () => '#detail-overlay'; box.setTimeout = () => {};
  box.toast('later success'); assert.equal(feedback.textContent, 'keep');
});

// 增量扫描：只模拟DOM/API/时钟，不访问真实库，不自动允许空库删除。
test('扫描计划mode兼容full、保留未知高级参数，类型切换不泄漏；提交带ETag', async () => {
  const original = { mode: 'incremental', limit: 17, root_marker_path: '.mounted', root_marker_sha256: 'abc', allow_empty_removal: true, future: false };
  const env = scheduledSubmitFixture({ kind: 'scan', params: original });
  env.box.editing.type = 'scan'; env.fields['#s-scan-mode'].value = 'incremental';
  await env.submit();
  assert.deepEqual(env.calls[0].body.params, { ...original, library_id: 7 });
  assert.equal(env.calls[0].headers['If-Match'], '"sc1-fixture"'); assert.equal(env.calls[0].method, 'PATCH');
  assert.equal(original.mode, 'incremental');
  const legacy = env.box.scheduledFormParams('scan', { type: 'scan' }, { future: false }, { library_id: 2 });
  assert.deepEqual(JSON.parse(JSON.stringify(legacy)), { future: false, library_id: 2, mode: 'full' });
  const changed = env.box.scheduledFormParams('scan', { type: 'probe' }, { status: 'all', future: true }, { library_id: 0, mode: 'incremental' });
  assert.deepEqual(JSON.parse(JSON.stringify(changed)), { library_id: 0, mode: 'incremental' });
  assert.throws(() => env.box.scheduledFormParams('scan', null, {}, { library_id: 0, mode: 'bad' }), /扫描模式/);
  const invalid = scheduledSubmitFixture({ kind: 'scan' }); invalid.fields['#s-scan-mode'].value = '';
  await invalid.submit(); assert.equal(invalid.calls.length, 0); assert.match(invalid.messages[0], /扫描模式/);
  const conflict = scheduledSubmitFixture({ kind: 'scan', failure: 412 });
  await conflict.submit(); await conflict.submit(); assert.equal(conflict.calls.length, 1); assert.equal(conflict.fields['#sched-submit'].disabled, true);
});

test('扫描计划回填不静默覆盖坏mode，相关控件仅scan显示，旧计划full', () => {
  for (const [mode, expected, readable] of [[undefined, 'full', true], ['incremental', 'incremental', true], ['bad', 'bad', false], [null, null, false]]) {
    const nodes = new Map(); const node = id => { if (!nodes.has(id)) nodes.set(id, { value: '', hidden: true, disabled: false, checked: false }); return nodes.get(id); };
    const box = sandbox({ editing: { type: 'scan' }, params: mode === undefined ? {} : { mode }, paramsReadable: true,
      document: { querySelector: node } });
    vm.runInContext(section('  // 回填编辑中的参数。', '  syncParams();'), box);
    assert.equal(node('#s-scan-mode').value, expected); assert.equal(box.paramsReadable, readable);
    if (!readable) { assert.equal(node('#sched-submit').disabled, true); assert.match(node('#sched-conflict').textContent, /扫描模式参数无法读取/); }
  }
  const visible = Object.fromEntries(['library', 'scan', 'probe', 'translate'].map(k => [k, [{ hidden: false }]]));
  const box = sandbox({ typeEl: { value: 'scan' }, editing: true, document: { querySelectorAll: selector => visible[selector.match(/data-show="([^"]+)"/)[1]] } });
  vm.runInContext(section('  function syncParams()', '  // 即时校验表达式'), box);
  box.syncParams(); assert.equal(visible.scan[0].hidden, false); assert.equal(visible.probe[0].hidden, true);
  box.typeEl.value = 'reindex'; box.syncParams(); assert.equal(visible.scan[0].hidden, true); assert.equal(visible.library[0].hidden, true);
});

test('扫描本次task结果覆盖旧running与旧计数，失败保留部分成果与未清理提示', async () => {
  const result = { mode: 'incremental', groups_total: 7, groups_done: 4, success: 2, pending: 1, failed: 1,
    unchanged_groups: 1, added: 2, updated: 1, deleted: 0, index_changed: true, cleanup_performed: false, incomplete: true };
  const env = scanFixture(async () => ({ running: true, task_id: 99, failed: 42, deleted: 99, cleanup_performed: true }),
    async () => ({ task_id: 7 }), async path => { assert.equal(path, '/tasks/7'); return { task_id: 7, mode: 'incremental', status: 'failed', finished_at: 'now', result, error: 'read failed' }; });
  await env.box.runScan(); await env.fire(0);
  assert.equal(env.reloads, 2); assert.equal(env.node('#scan-progress-count').textContent, '4/7 组');
  const detail = env.node('#scan-progress-detail').textContent;
  assert.match(detail, /新增 2 · 更新 1 · 删除 0/); assert.match(detail, /失败 1/); assert.match(detail, /部分成果已保留/);
  assert.match(detail, /未执行缺失索引清理/); assert.doesNotMatch(detail, /42|99|已完成缺失索引清理/);
  assert.equal(env.has(4000), false); assert.equal(env.node('#scan-progress-close').hidden, false);
  assert.equal(env.posts().length, 1);
});

test('扫描极快无变化终态只采用本次false字段，安全未完成也不能自动隐藏', async () => {
  for (const incomplete of [false, true]) {
    const env = scanFixture(async () => ({ ...scanTerminal, task_id: 99, failed: 12, incomplete: true, cleanup_performed: true }),
      async () => ({ task_id: 8 }), async () => ({ task_id: 8, status: 'succeeded', finished_at: 'new', mode: 'incremental',
        result: { groups_total: 5, groups_done: 5, unchanged_groups: 5, failed: 0, incomplete, cleanup_performed: !incomplete, index_changed: false } }));
    await env.box.runScan(); await env.fire(0);
    assert.equal(env.reloads, 2); assert.doesNotMatch(env.node('#scan-progress-detail').textContent, /失败 12/);
    assert.match(env.node('#scan-progress-detail').textContent, /未变化 5 · 新增 0 · 更新 0 · 删除 0/);
    assert.equal(env.has(4000), !incomplete);
    if (incomplete) assert.match(env.node('#scan-progress-title').textContent, /存在错误/);
    env.box.stopScanPolling();
  }
});

test('扫描taskID不匹配只GET退避，不刷新冒名任务；本地换身份禁止旧请求与迟到刷新', async () => {
  const mismatch = scanFixture(async () => scanTerminal, async () => ({ task_id: 7 }),
    async () => ({ task_id: 8, status: 'succeeded', finished_at: 'wrong', result: { deleted: 100 } }));
  await mismatch.box.runScan(); await mismatch.fire(0);
  assert.equal(mismatch.reloads, 1); assert.equal(mismatch.has(3000), true); assert.equal(mismatch.posts().length, 1);
  assert.doesNotMatch(mismatch.node('#scan-progress-detail').textContent, /删除 100/); mismatch.box.stopScanPolling();
  let finish;
  const late = scanFixture(async () => ({ running: false }), () => new Promise(resolve => { finish = resolve; }));
  const submitting = late.box.runScan(); late.box.localStorage = { getItem: () => 'new-identity' }; finish({ task_id: 7 }); await submitting;
  assert.equal(late.reloads, 0); late.box.stopScanPolling();
  await late.box.runScan(); assert.equal(late.posts().length, 1);
  const current = scanFixture(async () => ({ ...scanTerminal, task_id: 7 }), async () => ({ task_id: 7 }));
  await current.box.runScan(); current.box.localStorage = { getItem: () => 'new-identity' }; await current.fire(0);
  assert.equal(current.reloads, 1); current.box.stopScanPolling();
});

test('手动扫描full/incremental均显式下发，不携带允许空删；模式有可见说明', async () => {
  for (const mode of ['full', 'incremental']) {
    const env = scanFixture(); env.node('#scan-mode').value = mode;
    await env.box.runScan(3); assert.equal(env.posts()[0].path, '/tasks/scan?library_id=3&mode=' + mode);
    assert.doesNotMatch(env.posts()[0].path, /allow_empty/); env.box.stopScanPolling();
  }
  const html = fs.readFileSync(path.join(__dirname, 'web/index.html'), 'utf8');
  assert.match(html, /label for="scan-mode"/); assert.match(html, /aria-describedby="scan-mode-description"/);
  assert.match(html, /两种模式都会完整枚举目录/);
});


test('设置分类只显示一组，切换不移除草稿，刷新保留分类且身份清理重置', () => {
  const keys = ['account', 'users', 'notifications', 'logs', 'recovery', 'service'];
  const make = key => ({ dataset: { settingsCategory: key, settingsPanel: key }, hidden: false,
    draft: key + '-draft', setAttribute(name, value) { this[name] = value; }, addEventListener(_, fn) { this.click = fn; } });
  const buttons = keys.map(make), panels = keys.map(make);
  const root = { querySelectorAll: selector => selector === '[data-settings-category]' ? buttons : panels };
  const box = sandbox(); vm.runInContext(section("let settingsCategory = 'account';", 'async function pageSettings('), box);
  box.mountSettingsCategories(root);
  assert.deepEqual(panels.filter(p => !p.hidden).map(p => p.dataset.settingsPanel), ['account']);
  buttons[1].click(); assert.equal(buttons[1]['aria-pressed'], 'true');
  assert.deepEqual(panels.filter(p => !p.hidden).map(p => p.dataset.settingsPanel), ['users']);
  box.mountSettingsCategories(root); assert.equal(panels[1].hidden, false); assert.equal(panels[1].draft, 'users-draft');
  vm.runInContext("settingsCategory = 'account'", box); box.mountSettingsCategories(root);
  assert.equal(panels[0].hidden, false); assert.equal(panels[1].hidden, true);
  assert.match(section('function clearSessionView()', 'function identityStorageChanged('), /settingsCategory = 'account'/);
  assert.match(section("document.querySelector('#apikey-form').addEventListener", 'const logoutAll'), /settingsCategory = 'users';[\s\S]*refreshPage/);
});

test('Webhook折叠摘要使用文字不回显秘密，已有目标默认闭合、新建展开且重绘不丢草稿', async () => {
  const name = '<img src=x onerror=alert(1)>';
  const p = webhookPanel(async () => whView([whTarget({ name })])); await p.mounted;
  assert.equal(p.cards().length, 1); assert.equal(p.card(0).open, false);
  assert.match(p.card(0).innerHTML, /<summary class="disclosure-summary">/);
  assert.match(p.card(0).querySelector('[data-target-summary]').textContent, /<img src=x/);
  assert.equal(p.card(0).innerHTML.includes(name), false);
  assert.match(p.card(0).querySelector('[data-target-summary]').textContent, /地址已保存 · 1 项订阅/);
  p.card(0).open = true; p.field(0, 'url').value = 'https://example.invalid/new-secret';
  p.field(0, 'name').value = '草稿'; p.field(0, 'enabled').checked = true;
  p.card(0).listeners.change(); assert.match(p.card(0).querySelector('[data-target-summary]').textContent, /^草稿 · 已启用/);
  assert.equal(p.card(0).querySelector('[data-target-summary]').textContent.includes('new-secret'), false);
  p.add(); assert.equal(p.card(0).open, true); assert.equal(p.card(1).open, true);
  assert.equal(p.field(0, 'url').value, 'https://example.invalid/new-secret'); assert.equal(p.field(0, 'name').value, '草稿');
  p.card(0).open = false; p.remove(1); assert.equal(p.card(0).open, false);
  assert.match(p.host.innerHTML, /wh-confirm-remove/); assert.match(p.host.innerHTML, /wh-confirm-empty/);
});

test('计划列表与刮削执行先于收起配置，墙和详情维护为原生可聚焦披露而不改业务ID', () => {
  const scheduled = section('async function pageScheduled(', 'async function pageLogs(');
  assert.ok(scheduled.indexOf('<h2>已配置任务</h2>') < scheduled.indexOf('id="sched-form"'));
  assert.match(scheduled, /<details class="panel disclosure" id="scheduled-editor"\$\{editing \? ' open' : ''\}/);
  const scrape = section('async function pageScrape(', 'function progressCounts(');
  assert.ok(scrape.indexOf('id="scrape-start"') < scrape.indexOf('id="scrape-form"'));
  assert.match(scrape, /<details class="panel disclosure" id="scrape-configuration">/);
  assert.match(scrape, /共享翻译服务/);
  const items = section('async function pageItems(', 'function reloadWall(');
  assert.ok(items.indexOf('id="item-search"') < items.indexOf('id="probe-media"'));
  assert.match(items, /<details class="disclosure wall-maintenance"><summary/);
  const detail = section('async function openDetail(', 'async function pageActivity(');
  assert.ok(detail.indexOf('id="detail-play"') < detail.indexOf('detail-maintenance'));
  assert.match(detail, /<details class="disclosure detail-maintenance"><summary/);
  assert.match(section('function overlayFocusables(', 'function overlayContains('), /summary,/);
});
