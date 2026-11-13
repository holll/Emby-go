const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, 'web/app.js'), 'utf8');
const embySource = fs.readFileSync(path.join(__dirname, 'web/emby.js'), 'utf8');
const tick = () => new Promise(setImmediate);

function section(start, end) {
  const from = source.indexOf(start);
  const to = source.indexOf(end, from);
  assert.ok(from >= 0 && to > from, `缺少代码段：${start}`);
  return source.slice(from, to);
}

// 媒体墙合集用到的页面主体段落（含 view 切换/返回/卡片点击绑定）。
const WALL_PAGE = section('async function pageItems()', '// reloadWall 只重置列表并重新拉取');
// loadWallPage 负责真正发查询、追加卡片，独立成段一起装载，测试才能看到线上参数。
const WALL_LOAD = section('async function loadWallPage()', '/* ------------------------------------------------------------ 详情抽屉');
// 真实查询构造器（emby.js）参与页面测试，避免用替身掩盖真实参数。
const wallEmbyQueryFrom = embySource.indexOf('function wallEmbyQuery');
const WALL_QUERY = embySource.slice(wallEmbyQueryFrom, embySource.indexOf('async function openDetail'));

function element(extra = {}) {
  return { innerHTML: '', textContent: '', hidden: false, classList: { toggle() {}, add() {} },
    handlers: {}, setAttribute() {}, focus() {}, getClientRects: () => [],
    insertAdjacentHTML(position, html) { this.innerHTML += html; },
    querySelector(selector) { return this.children?.[selector] || null; },
    addEventListener(type, fn) { this.handlers[type] = fn; }, ...extra };
}

// 只实现媒体墙需要的 DOM 查询：静态元素 + 选择器集合。
function dom({ selectors = {}, collections = [] } = {}) {
  const nodes = new Map();
  const node = id => {
    if (!nodes.has(id)) {
      const value = element();
      value.children = { '#wall-retry': element() };
      nodes.set(id, value);
    }
    return nodes.get(id);
  };
  const documentObject = {
    querySelector: id => selectors[id] !== undefined ? selectors[id] : node(id),
    querySelectorAll: selector => collections.filter(entry => entry.selector === selector).flatMap(entry => entry.nodes),
  };
  return { node, document: documentObject };
}

function wallSandbox({ search = '?view=collections', state = null, me = { id: '7', is_admin: false }, view = 'collections', collection = null,
  documentRef = dom(), history: historyRef = { state: {}, pushed: [], replaced: [], back() { this.backed = (this.backed || 0) + 1; },
    pushState(state, _, url) { this.state = state; this.pushed.push(url); }, replaceState(state, _, url) { this.state = state; this.replaced.push(url); } },
  embyRequest: embyRequestRef = async () => ({ Items: [], TotalRecordCount: 0 }), collections = [] } = {}) {
  const box = vm.createContext({
    URLSearchParams, Set, JSON,
    token: 'A', me, pageGen: 1, wallGen: 0, wallState: state, view, collection,
    WALL_PAGE_SIZE: 48, WALL_STATUS_TEXT: { favorite: '收藏', played: '已看', unplayed: '未看' }, pendingWallFocus: '',
    ENTITY_PARAMS: [['person', '演员'], ['genre', '类型'], ['studio', '厂商'], ['collection', '合集'], ['tag', '标签']],
    entityIdOf: (kind, value) => `${kind}:${value}`,
    esc: value => String(value ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]),
    icon: () => '', empty: (title, hint) => `EMPTY:${title}|${hint || ''}`, translationWidget: () => 'TRANSLATION-WIDGET',
    embyUserPath: () => '/Users/7', embyRequest: embyRequestRef, embyImageURL: (item, kind, width) => item.ImageTags?.[kind] ? `/emby/Items/${item.Id}/Images/${kind}?maxWidth=${width}` : '',
    location: { search, pathname: '/', hash: '#items' }, history: historyRef,
    content: element(), document: documentRef.document, debounce: fn => fn, toast() {}, closeDetail() {}, openPlayer() {}, openDetail() {},
    stopTranslationPolling() {}, stopProbePolling() {}, stopLogsPolling() {}, stopScanPolling() {},
    refreshPage: (render, gen) => (gen === box.pageGen ? render() : undefined),
    runProbeAll() {}, startProbePolling() {}, stopProbePolling() {}, wallMaybeLoadMore() {},
    movieDisplayTitle: item => item.Name || '',
    collections,
  });
  vm.runInContext(WALL_QUERY, box);
  vm.runInContext(WALL_LOAD, box);
  box.node = documentRef.node;
  box.historyRef = historyRef;
  return box;
}

function recorded(box, calls) {
  return calls.map(call => new URL(call, 'http://fixture'));
}

test('合集列表走 BoxSet 查询，不带影片库/状态/媒体字段，也不渲染影片专属控件', async () => {
  const calls = [];
  const box = wallSandbox({
    search: '?view=collections&search=%E7%B3%BB%E5%88%97',
    embyRequest: async url => { calls.push(url); return { Items: [], TotalRecordCount: 0 }; },
  });
  vm.runInContext(WALL_PAGE, box);
  await box.pageItems();
  const items = calls.filter(url => url.includes('/Items?'));
  assert.equal(items.length, 1, '合集列表只发一次 Items 查询（无合集详情请求）');
  const query = new URL(items[0], 'http://fixture').searchParams;
  assert.equal(query.get('IncludeItemTypes'), 'BoxSet');
  assert.equal(query.get('Recursive'), 'true');
  assert.equal(query.get('SortBy'), 'SortName');
  assert.equal(query.get('SortOrder'), 'Ascending');
  assert.equal(query.get('SearchTerm'), '系列');
  for (const absent of ['ParentId', 'Filters', 'Fields']) assert.equal(query.has(absent), false, `合集列表不应带 ${absent}`);
  const html = box.content.innerHTML;
  assert.match(html, /data-wall-view="collections"[^>]*aria-pressed="true"/);
  assert.match(html, /data-wall-view="movies"/);
  assert.match(html, /标题 A–Z/);
  assert.match(html, /标题 Z–A/);
  assert.doesNotMatch(html, /wall-status-filters/, '合集列表隐藏影片状态筛选');
  assert.doesNotMatch(html, /wall-maintenance/, '合集列表隐藏批量维护/刮削/翻译入口');
  assert.doesNotMatch(html, /data-status=/, '合集列表不渲染状态 pill');
});

test('进入合集：成员查询按合集 ParentId 走 Movie，页面出现标题与返回按钮', async () => {
  const calls = [];
  const box = wallSandbox({
    search: '?view=collections&collection=boxset:YQ',
    embyRequest: async url => { calls.push(url); if (url.includes('/Items/boxset%3AYQ')) return { Id: 'boxset:YQ', Name: '系列合集', Type: 'BoxSet', ChildCount: 3 }; return { Items: [], TotalRecordCount: 0 }; },
  });
  vm.runInContext(WALL_PAGE, box);
  await box.pageItems();
  assert.ok(calls.some(url => url === '/Users/7/Items/boxset%3AYQ'), '先读合集详情');
  const member = calls.find(url => url.includes('/Items?'));
  const query = new URL(member, 'http://fixture').searchParams;
  assert.equal(query.get('IncludeItemTypes'), 'Movie');
  assert.equal(query.get('ParentId'), 'boxset:YQ');
  assert.equal(query.get('SortBy'), 'SortName');
  const html = box.content.innerHTML;
  assert.match(html, /系列合集/);
  assert.match(html, /3 部影片/);
  assert.match(html, /id="collection-back"/);
  assert.match(html, /id="collection-title"/);
  assert.match(html, /wall-status-filters/, '合集成员仍可按收藏/已看筛选');
  assert.doesNotMatch(html, /wall-maintenance/, '合集内不提供影片索引批量维护');
});

test('合集排序只接受标题升降序，非标题排序参数会被清洗掉', async () => {
  const box = wallSandbox({ search: '?view=collections&sort=year' });
  vm.runInContext(WALL_PAGE, box);
  await box.pageItems();
  assert.equal(box.wallState.sort, 'title');
  assert.equal(box.wallState.order, 'asc');
  const cleaned = new URL(box.historyRef.replaced.at(-1), 'http://fixture').searchParams;
  assert.equal(cleaned.has('sort'), false);
  const descending = wallSandbox({ search: '?view=collections&sort=title-desc' });
  vm.runInContext(WALL_PAGE, descending);
  await descending.pageItems();
  assert.equal(descending.wallState.sort, 'title-desc');
  assert.equal(descending.wallState.order, 'desc');
});

test('影片/合集切换只改 view 并清掉状态、合集与排序参数', async () => {
  const switchNodes = [element({ dataset: { wallView: 'movies' } }), element({ dataset: { wallView: 'collections' } })];
  const box = wallSandbox({
    search: '?view=collections&status=played&collection=boxset:YQ&sort=title-desc',
    documentRef: dom({ collections: [{ selector: '[data-wall-view]', nodes: switchNodes }] }),
  });
  vm.runInContext(WALL_PAGE, box);
  await box.pageItems();
  // vm 里创建的函数跨 realm，instanceof Function 恒为 false，只能判 typeof。
  assert.equal(typeof switchNodes[0].handlers.click, 'function', '非当前 view 的按钮可点击');
  assert.equal(switchNodes[1].handlers.click, undefined, '当前 view 的按钮不绑定切换');
  switchNodes[0].handlers.click();
  const url = new URL(box.historyRef.pushed.at(-1), 'http://fixture');
  assert.equal(url.searchParams.has('view'), false);
  assert.equal(url.searchParams.has('collection'), false);
  assert.equal(url.searchParams.has('status'), false);
  assert.equal(url.searchParams.has('sort'), false);
  assert.equal(url.hash, '#items');
  assert.equal(box.pendingWallFocus, '[data-wall-view="movies"]');
});

test('返回合集：有进入历史时回退，直接打开合集链接时只清 collection', async () => {
  const backButton = element();
  const box = wallSandbox({
    search: '?view=collections&collection=boxset:YQ',
    documentRef: dom({ selectors: { '#collection-back': backButton } }),
    history: { state: { wallFromCollections: true }, pushed: [], replaced: [], back() { this.backed = (this.backed || 0) + 1; },
      pushState() {}, replaceState(state, _, url) { this.state = state; this.replaced.push(url); } },
    embyRequest: async url => url.includes('/Items/boxset') ? { Id: 'boxset:YQ', Name: '系列合集', ChildCount: 2 } : { Items: [], TotalRecordCount: 0 },
  });
  vm.runInContext(WALL_PAGE, box);
  await box.pageItems();
  backButton.handlers.click();
  assert.equal(box.historyRef.backed, 1, '来自列表时用 history.back 恢复进前的筛选');
  assert.equal(box.historyRef.replaced.length, 0);

  const directBack = element();
  const direct = wallSandbox({
    search: '?view=collections&collection=boxset:YQ',
    documentRef: dom({ selectors: { '#collection-back': directBack } }),
    embyRequest: async url => url.includes('/Items/boxset') ? { Id: 'boxset:YQ', Name: '系列合集' } : { Items: [], TotalRecordCount: 0 },
  });
  vm.runInContext(WALL_PAGE, direct);
  await direct.pageItems();
  directBack.handlers.click();
  assert.equal(direct.historyRef.backed, undefined, '没有可回退历史时不乱退');
  const url = new URL(direct.historyRef.replaced.at(-1), 'http://fixture');
  assert.equal(url.searchParams.has('collection'), false);
  assert.equal(url.searchParams.get('view'), 'collections');
});

test('合集卡片打开成员列表：编码进 URL 并保留 hash，不触发详情/播放', async () => {
  let detail = 0, quickplay = 0;
  const wall = element();
  const box = wallSandbox({
    search: '?view=collections',
    documentRef: dom({ selectors: { '#wall': wall } }),
    embyRequest: async () => ({ Items: [], TotalRecordCount: 0 }),
  });
  box.openDetail = () => { detail++; };
  box.openPlayer = () => { quickplay++; };
  vm.runInContext(WALL_PAGE, box);
  await box.pageItems();
  const target = { closest: selector => selector === '[data-collection]' ? { dataset: { collection: 'boxset:YQ' } } : null };
  await wall.handlers.click({ target, stopPropagation() {} });
  const url = new URL(box.historyRef.pushed.at(-1), 'http://fixture');
  assert.equal(url.searchParams.get('view'), 'collections');
  assert.equal(url.searchParams.get('collection'), 'boxset:YQ');
  assert.equal(url.hash, '#items');
  assert.equal(box.historyRef.state.wallFromCollections, true, '写入来源标记供返回判断');
  assert.equal(detail, 0);
  assert.equal(quickplay, 0);
  assert.ok(new URL(box.historyRef.pushed.at(-1), 'http://fixture').searchParams.get('collection') === 'boxset:YQ');
});

test('合集卡片只有合集角标与数量，没有快捷播放/管理按钮', () => {
  const box = wallSandbox({ me: { id: '7', is_admin: true } });
  vm.runInContext(section('function wallCard(item) {', 'const WALL_PAGE_SIZE'), box);
  const html = box.wallCard({ Id: 'boxset:YQ', Name: '系列合集', Type: 'BoxSet', ChildCount: 3, ImageTags: { Primary: 'v1' } });
  assert.match(html, /data-collection="boxset:YQ"/);
  assert.match(html, /3 部影片/);
  assert.match(html, /collection-mark/);
  assert.match(html, /Images\/Primary\?maxWidth=200/);
  assert.doesNotMatch(html, /data-quickplay/, '合集卡片不能直接播放');
  assert.doesNotMatch(html, /data-probe|data-reread|data-delete|data-translate-select/, '合集卡片不带影片管理入口');
  assert.doesNotMatch(html, /wall-badge|wall-progress/);
  const bare = box.wallCard({ Id: 'boxset:YQ', Name: '空合集', Type: 'BoxSet' });
  assert.match(bare, /collection-placeholder/);
  assert.match(bare, /查看影片/);
});

test('合集封面缺失时回落到占位图而不是空 src', () => {
  const box = wallSandbox();
  vm.runInContext(section('function wallCard(item) {', 'const WALL_PAGE_SIZE'), box);
  const html = box.wallCard({ Id: 'boxset:YQ', Name: '无封面', Type: 'BoxSet', ImageTags: {} });
  assert.doesNotMatch(html, /<img/);
  assert.match(html, /collection-placeholder/);
});

test('Emby 查询构造：合集列表与成员列表的参数互不串味', () => {
  const box = vm.createContext({ URLSearchParams, WALL_PAGE_SIZE: 48, entityIdOf: (kind, value) => `${kind}:${value}` });
  vm.runInContext(embySource, box);
  const list = box.wallEmbyQuery({ view: 'collections', entity: null, offset: 0, sort: 'title', order: 'asc', search: '', libraryID: '', status: '' });
  assert.equal(list.get('IncludeItemTypes'), 'BoxSet');
  assert.equal(list.get('SortBy'), 'SortName');
  assert.equal(list.get('SortOrder'), 'Ascending');
  assert.equal(list.has('ParentId'), false);
  assert.equal(list.has('Filters'), false);
  const desc = box.wallEmbyQuery({ view: 'collections', entity: null, offset: 0, sort: 'title-desc', order: 'desc', search: '', libraryID: '', status: '' });
  assert.equal(desc.get('SortBy'), 'SortName');
  assert.equal(desc.get('SortOrder'), 'Descending');
  const member = box.wallEmbyQuery({ view: 'collections', entity: ['collection', '合集', 'boxset:YQ'], offset: 48, sort: 'title', order: 'asc', search: '', libraryID: '', status: '' });
  assert.equal(member.get('IncludeItemTypes'), 'Movie');
  assert.equal(member.get('ParentId'), 'boxset:YQ');
  assert.equal(member.get('StartIndex'), '48');
  assert.equal(member.has('Filters'), false, '合集内不叠加影片状态筛选');
});